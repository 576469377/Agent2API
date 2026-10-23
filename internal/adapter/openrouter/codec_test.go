package openrouter

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestSharedChatCodecKeepsProviderIdentityAndClose(t *testing.T) {
	r, w := io.Pipe()
	defer w.Close()
	canceled := false
	s := ChatStream(&http.Response{Body: r}, "test-model", func() { canceled = true }, time.Minute, "Kilo", "anonymous")
	if s.(interface{ AccountLabel() string }).AccountLabel() != "anonymous" {
		t.Fatal("provider identity lost")
	}
	if err := s.(io.Closer).Close(); err != nil || !canceled {
		t.Fatal("shared codec lost stream cleanup")
	}
	s = ChatStream(&http.Response{Body: io.NopCloser(strings.NewReader("data: {\"error\":{\"code\":429,\"message\":\"rate limit\"}}\n\n"))}, "test-model", func() {}, time.Minute, "Kilo", "anonymous")
	if _, err := s.Recv(context.Background()); err == nil || strings.Contains(err.Error(), "OpenRouter") || !strings.Contains(err.Error(), "Kilo") {
		t.Fatalf("wrong error identity: %v", err)
	}
}
