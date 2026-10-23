package main

import (
	"context"
	"errors"
	"net/http"
	"sync"
	"time"
)

// http.Server.Close cancels requests but does not wait for their handlers.
// Keep the final accounting inside that wait, without wrapping ResponseWriter.
type requestDrain struct {
	next     http.Handler
	mu       sync.Mutex
	active   int
	stopping bool
	done     chan struct{}
}

func newRequestDrain(next http.Handler) *requestDrain {
	return &requestDrain{next: next, done: make(chan struct{})}
}

func (d *requestDrain) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	d.mu.Lock()
	if d.stopping {
		d.mu.Unlock()
		http.Error(w, "服务正在关闭", http.StatusServiceUnavailable)
		return
	}
	d.active++
	d.mu.Unlock()
	defer func() {
		d.mu.Lock()
		d.active--
		if d.stopping && d.active == 0 {
			close(d.done)
		}
		d.mu.Unlock()
	}()
	d.next.ServeHTTP(w, r)
}

func (d *requestDrain) stop() <-chan struct{} {
	d.mu.Lock()
	defer d.mu.Unlock()
	if !d.stopping {
		d.stopping = true
		if d.active == 0 {
			close(d.done)
		}
	}
	return d.done
}

// First drain normally. On timeout, cancel active requests and let their metric
// defers finish before saving. Bound cleanup even if a handler ignores cancel.
func shutdownAndPersist(ctx context.Context, srv *http.Server, requests *requestDrain, persist func()) error {
	err := srv.Shutdown(ctx)
	done := requests.stop()
	if err != nil {
		err = errors.Join(err, srv.Close())
		cleanup, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		select {
		case <-done:
		case <-cleanup.Done():
			err = errors.Join(err, errors.New("等待请求收尾超时"))
		}
	}
	persist()
	return err
}
