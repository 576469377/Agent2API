package trae

import (
	"context"
	"errors"
	"io"
	"sync"
	"testing"
	"time"
)

// A read may return buffered bytes concurrently with Close. Hold that read
// until cancellation closes the body so the close/reset ordering is repeatable.
type closingStreamBody struct {
	started, closed chan struct{}
	start, close    sync.Once
	delivered       bool
}

func (b *closingStreamBody) Read(p []byte) (int, error) {
	b.start.Do(func() { close(b.started) })
	<-b.closed
	if b.delivered {
		return 0, io.EOF
	}
	b.delivered = true
	return copy(p, "event: metadata\ndata: {}\n\n"), io.EOF
}

func (b *closingStreamBody) Close() error {
	b.close.Do(func() { close(b.closed) })
	return nil
}

func TestCancelledStreamDoesNotRestartIdleTimer(t *testing.T) {
	body := &closingStreamBody{started: make(chan struct{}), closed: make(chan struct{})}
	s := newStream(body, func() {}, time.Hour, "test")
	defer s.Close()
	defer s.idle.Stop()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { _, err := s.Recv(ctx); done <- err }()
	select {
	case <-body.started:
	case <-time.After(time.Second):
		t.Fatal("stream did not start reading")
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("cancelled read = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("cancelled stream did not finish")
	}
	if s.idle.Stop() {
		t.Fatal("buffered bytes restarted the idle timer after stream close")
	}
}
