package workbuddy

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sync"
	"time"

	"github.com/576469377/Agent2API/internal/llm"
)

// uploadGuard measures a lack of progress, never the total upload duration.
// It also covers a blocked final write after the body reader reached EOF.
type uploadGuard struct {
	ctx          context.Context
	cancel       context.CancelCauseFunc
	idle         time.Duration
	mu           sync.Mutex
	timer        *time.Timer
	lastProgress time.Time
	done         bool
}

func newUploadGuard(ctx context.Context, idle time.Duration) *uploadGuard {
	child, cancel := context.WithCancelCause(ctx)
	return &uploadGuard{ctx: child, cancel: cancel, idle: idle}
}

func (g *uploadGuard) start() {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.done || g.timer != nil || g.idle <= 0 || g.ctx.Err() != nil {
		return
	}
	g.lastProgress = time.Now()
	g.timer = time.AfterFunc(g.idle, g.checkIdle)
}

func (g *uploadGuard) progress(n int) {
	if n <= 0 {
		return
	}
	g.mu.Lock()
	if !g.done {
		g.lastProgress = time.Now()
	}
	g.mu.Unlock()
	llm.ObserveUpload(g.ctx, int64(n))
}

func (g *uploadGuard) checkIdle() {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.done || g.ctx.Err() != nil {
		return
	}
	if remaining := g.idle - time.Since(g.lastProgress); remaining > 0 {
		g.timer.Reset(remaining)
		return
	}
	g.done = true
	// A timeout is our decision, not a caller cancellation. Returning this
	// cause directly preserves 504 and stops all adapter/pool replay loops.
	g.cancel(&llm.Failure{
		Code:    "upstream_upload_timeout",
		Message: fmt.Sprintf("上传请求连续 %s 无进展，已断开连接；为避免重复提交，本次未自动重试", g.idle),
		Cause:   context.DeadlineExceeded, Timeout: true, UpstreamFault: true, NoRetry: true,
	})
}

func (g *uploadGuard) finishUpload() {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.done = true
	if g.timer != nil {
		g.timer.Stop()
	}
}

func (g *uploadGuard) close() {
	g.finishUpload()
	g.cancel(nil)
}

func isUploadTimeout(err error) bool {
	var f *llm.Failure
	return errors.As(err, &f) && f.Code == "upstream_upload_timeout"
}

type uploadReader struct {
	io.Reader
	guard *uploadGuard
}

func (r *uploadReader) Read(p []byte) (int, error) {
	n, err := r.Reader.Read(p)
	r.guard.progress(n)
	return n, err
}

type uploadResponseBody struct {
	io.ReadCloser
	guard *uploadGuard
}

func (b *uploadResponseBody) Read(p []byte) (int, error) {
	n, err := b.ReadCloser.Read(p)
	if err != nil {
		if cause := context.Cause(b.guard.ctx); isUploadTimeout(cause) {
			err = cause
		}
	}
	return n, err
}

func (b *uploadResponseBody) Close() error {
	b.guard.close()
	return b.ReadCloser.Close()
}
