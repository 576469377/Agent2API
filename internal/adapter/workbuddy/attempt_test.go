package workbuddy

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/576469377/Agent2API/internal/llm"
)

// 图片历史会使上传及首个响应超过非流式的超时，不能因此重拨同一请求。
func TestChatAttemptAllowsSlowUploadAndHeaders(t *testing.T) {
	var calls atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		time.Sleep(80 * time.Millisecond)
		if _, err := io.Copy(io.Discard, r.Body); err != nil {
			return
		}
		time.Sleep(80 * time.Millisecond)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: [DONE]\n\n")
	}))
	defer server.Close()
	a := &Adapter{cli: newHTTPClient(server.URL, 0), cfg: Config{Platform: GlobalPlatform, RequestTimeout: 10 * time.Millisecond}}
	execution := llm.NewExecution(6)
	execution.StartRouting(3 * time.Second)
	parent, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	ctx := llm.WithExecution(parent, execution)
	// 大于 TCP 缓冲区，让服务端慢读取也覆盖真实上传等待。
	body := upstreamRequest{Model: "m", Messages: []upstreamMessage{{Role: "user", Content: strings.Repeat("fixture ", 1<<20)}}}
	resp, err := a.openChatAttempt(ctx, chatCompletionsPath, body, http.Header{})
	if err != nil {
		t.Fatalf("valid slow request canceled: %v", err)
	}
	defer resp.Body.Close()
	if _, err := io.Copy(io.Discard, resp.Body); err != nil {
		t.Fatal(err)
	}
	trace := execution.Snapshot()
	if calls.Load() != 1 || trace.AttemptCount != 1 || trace.Attempts[0].Status != 200 || trace.Attempts[0].ErrorCode != "" {
		t.Fatalf("slow upload was retried or misclassified: %+v", trace)
	}
}

func TestChatAttemptStillHonorsParentDeadline(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		<-r.Context().Done()
	}))
	defer server.Close()
	a := &Adapter{cli: newHTTPClient(server.URL, 0), cfg: Config{Platform: GlobalPlatform}}
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	_, err := a.openChatAttempt(ctx, chatCompletionsPath, upstreamRequest{Model: "m"}, http.Header{})
	if f := llm.Wrap(err); f == nil || !f.Timeout || f.Canceled {
		t.Fatalf("stream total deadline lost: %v", err)
	}
}
