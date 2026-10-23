package openrouter

import (
	"context"
	"errors"
	"io"
	"net/http"
	"testing"
	"time"
)

func TestRecvCancellationInterruptsSilentStream(t *testing.T) {
	reader, writer := io.Pipe()
	defer writer.Close()
	s := newStream(&http.Response{Body: reader}, "vendor/free:free", func() {}, time.Minute)
	defer s.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	result := make(chan error, 1)
	go func() {
		_, err := s.Recv(ctx)
		result <- err
	}()
	// A heartbeat proves Recv passed its entry check and is reading SSE frames.
	if _, err := writer.Write([]byte(": heartbeat\n")); err != nil {
		t.Fatal(err)
	}
	cancel()
	select {
	case err := <-result:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("Recv cancellation = %v, want context.Canceled", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Recv remained blocked after its context was cancelled")
	}
}
