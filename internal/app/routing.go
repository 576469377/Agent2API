package app

import (
	"context"
	"io"
	"strings"
	"time"

	"github.com/576469377/Agent2API/internal/adapter"
	"github.com/576469377/Agent2API/internal/llm"
	"github.com/576469377/Agent2API/internal/obs"
)

// openRoutedStream only falls back before an upstream stream has been accepted.
// Once accepted, every event in that response stays on that platform.
func (a *App) openRoutedStream(ctx context.Context, req llm.RequestMessages, hint string, records ...*obs.Record) (llm.ResponseStream, *PlatformRuntime, error) {
	execution := llm.ExecutionFrom(ctx)
	if execution == nil {
		return a.routeStream(ctx, req, hint, records...)
	}
	routingCtx, cancel := context.WithCancel(ctx)
	expired := make(chan struct{})
	timer := time.AfterFunc(execution.Remaining(), func() { cancel(); close(expired) })
	stream, rt, err := a.routeStream(routingCtx, req, hint, records...)
	if !timer.Stop() {
		<-expired
		if closer, ok := stream.(io.Closer); ok {
			closer.Close()
		}
		cancel()
		if ctx.Err() != nil {
			return nil, rt, ctx.Err()
		}
		failure := &llm.Failure{Code: "routing_timeout", Message: "本次请求等待上游的总时间已用尽，请稍后再试", Local: true}
		llm.NoteAttemptFailure(ctx, failure)
		return nil, rt, failure
	}
	if err != nil {
		cancel()
		return stream, rt, err
	}
	return &routedStream{ResponseStream: stream, cancel: cancel}, rt, nil
}

type routedStream struct {
	llm.ResponseStream
	cancel context.CancelFunc
}

func (s *routedStream) AccountLabel() string { return adapter.AccountOf(s.ResponseStream) }
func (s *routedStream) Close() error {
	defer s.cancel()
	if closer, ok := s.ResponseStream.(io.Closer); ok {
		return closer.Close()
	}
	return nil
}

func (a *App) routeStream(ctx context.Context, req llm.RequestMessages, hint string, records ...*obs.Record) (llm.ResponseStream, *PlatformRuntime, error) {
	primary := a.hub.RouteWithHint(req.Model, hint)
	stream, err := a.openPlatformStream(ctx, primary, req, records...)
	if err == nil || hint != "" || !a.cfg.Models.FallbackOnUnavailable || ctx.Err() != nil {
		return stream, primary, err
	}
	failure := llm.Wrap(err)
	eligible := func(f *llm.Failure) bool {
		return !f.Local && (f.RateLimited || f.QuotaExhausted || strings.HasPrefix(f.Code, "http_5"))
	}
	if !eligible(failure) {
		return stream, primary, err
	}
	a.hub.mu.RLock()
	var alternatives []*PlatformRuntime
	for _, pid := range a.hub.order {
		if pid == primary.ID {
			continue
		}
		for _, m := range a.hub.catalog[pid] {
			if m.ID == req.Model {
				alternatives = append(alternatives, a.hub.platforms[pid])
				break
			}
		}
	}
	a.hub.mu.RUnlock()
	for _, rt := range alternatives {
		if ctx.Err() != nil {
			break
		}
		a.logger.Printf("模型 %s: %s 在建流前拒绝请求，尝试同名模型 %s", req.Model, primary.ID, rt.ID)
		next, nextErr := a.openPlatformStream(ctx, rt, req, records...)
		if nextErr == nil {
			return next, rt, nil
		}
		// The final error and metrics must describe the last actual attempt,
		// rather than hiding it behind the primary platform's earlier failure.
		stream, primary, err = next, rt, nextErr
		if !eligible(llm.Wrap(nextErr)) {
			break
		}
	}
	return stream, primary, err
}
