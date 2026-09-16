package common

import (
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/576469377/Agent2API/internal/llm"
)

func TestAppendSSEFormats(t *testing.T) {
	// data-only 风格（Chat Completions）
	if got := string(AppendSSE(nil, "", []byte(`{"a":1}`))); got != "data: {\"a\":1}\n\n" {
		t.Fatalf("data-only 帧=%q", got)
	}
	// 带事件名（Anthropic / Responses）
	if got := string(AppendSSE(nil, "message_delta", []byte(`{}`))); got != "event: message_delta\ndata: {}\n\n" {
		t.Fatalf("命名帧=%q", got)
	}
	// 终止帧
	if got := string(AppendSSE(nil, "[DONE]", nil)); got != "data: [DONE]\n\n" {
		t.Fatalf("[DONE] 帧=%q", got)
	}
	// 追加语义：多次调用应顺序拼接
	dst := AppendSSE(nil, "", []byte("1"))
	dst = AppendSSE(dst, "", []byte("2"))
	if !strings.Contains(string(dst), "1") || !strings.Contains(string(dst), "2") {
		t.Fatalf("追加语义破坏: %q", dst)
	}
}

func TestWriteErrorStatusMapping(t *testing.T) {
	cases := []struct {
		name string
		f    *llm.Failure
		want int
	}{
		{"nil", nil, 502},
		{"unauthorized", llm.NewFailure("invalid_api_key", "Invalid API key", nil), 401},
		{"rate_limited", llm.NewFailure("rate_limited", "rate limit exceeded", nil), 429},
		{"context_length", llm.NewFailure("x", "maximum context length", nil), 413},
		{"upstream", llm.NewFailure("upstream_read_failed", "broken", nil), 502},
	}
	for _, c := range cases {
		w := httptest.NewRecorder()
		WriteError(w, c.f)
		if got := w.Result().StatusCode; got != c.want {
			t.Errorf("%s: status=%d, want %d", c.name, got, c.want)
		}
	}
}

// TestWriteErrorBodyShape 保证错误体是 {error:{message,type,code}} 结构。
func TestWriteErrorBodyShape(t *testing.T) {
	w := httptest.NewRecorder()
	WriteError(w, llm.NewFailure("my_code", "boom", nil))
	body := w.Body.String()
	for _, key := range []string{`"message":"boom"`, `"type":`, `"code":"my_code"`} {
		if !strings.Contains(body, key) {
			t.Errorf("错误体缺 %s: %s", key, body)
		}
	}
}

func TestWriteSSEHeaders(t *testing.T) {
	w := httptest.NewRecorder()
	WriteSSEHeaders(w)
	h := w.Result().Header
	if !strings.Contains(h.Get("Content-Type"), "text/event-stream") {
		t.Fatalf("Content-Type=%q", h.Get("Content-Type"))
	}
	if h.Get("Cache-Control") == "" {
		t.Fatal("缺 Cache-Control（SSE 不得被中间层缓存）")
	}
	if h.Get("X-Accel-Buffering") == "" {
		t.Fatal("缺 X-Accel-Buffering: no（nginx 后面会被缓冲成假死流）")
	}
}
