package workbuddy

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/576469377/Agent2API/internal/llm"
)

// newRetryTestAdapter 起一个可控上游，返回 (adapter, chatHits, refreshHits)。
//
// credentialFixture 是最小可用凭证：expiresAt 在远期，EnsureValid 不触发主动刷新，
// 使 401 只能来自聊天端点本身。
func newRetryTestAdapter(t *testing.T, handler http.HandlerFunc) (*Adapter, *atomic.Int64, *atomic.Int64) {
	t.Helper()
	var chatHits, refreshHits atomic.Int64
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case tokenRefreshPath:
			refreshHits.Add(1)
			handler(w, r)
		default:
			chatHits.Add(1)
			handler(w, r)
		}
	}))
	t.Cleanup(ts.Close)

	dir := t.TempDir()
	credPath := filepath.Join(dir, "cred.json")
	cred := `{"auth":{"accessToken":"tok-old","refreshToken":"rt","tokenType":"Bearer","expiresAt":9999999999999},"account":{"uid":"u1"}}`
	if err := os.WriteFile(credPath, []byte(cred), 0o644); err != nil {
		t.Fatal(err)
	}

	adp, err := New(Config{
		BaseURL: ts.URL, CredentialPath: credPath,
		StreamIdleTimeout:  time.Second,
		StreamTotalTimeout: 30 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	return adp, &chatHits, &refreshHits
}

// TestRetryGivesUpAfterRefreshStillUnauthorized 是「401 刷新循环」的回归测试。
//
// 曾有 bug：401 分支用 attempt-- 抵消 for 的 attempt++，当上游聊天恒 401
// 而刷新恒成功（两者的校验逻辑独立：refresh 只验 refreshToken，chat 还要验
// 订阅权益，场景真实可达）时，Stream 变成无界循环——实测 3 秒打近 4 万次上游。
// 修复后：每次 attempt 只允许刷新一次，刷新后仍 401 立即放弃。
func TestRetryGivesUpAfterRefreshStillUnauthorized(t *testing.T) {
	adp, chatHits, refreshHits := newRetryTestAdapter(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == tokenRefreshPath {
			_, _ = w.Write([]byte(`{"code":0,"msg":"ok","data":{"accessToken":"tok-new","expiresIn":3600000}}`))
			return
		}
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"code":401,"msg":"token expired"}`))
	})

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_, err := adp.Stream(ctx, llm.RequestMessages{
		Model:    "m",
		Messages: []llm.Message{{Role: llm.RoleUser, Content: []llm.Content{{Type: llm.ContentText, Text: "hi"}}}},
	})
	if err == nil {
		t.Fatal("刷新后仍 401 必须失败返回")
	}
	// 有界：首次 + 一次刷新重试 = 2 次 chat 命中（容忍极端时序下 3 次）。
	if got := chatHits.Load(); got > 3 {
		t.Fatalf("401 刷新后仍失败应立即放弃, chatHits=%d refreshHits=%d", got, refreshHits.Load())
	}
	if got := refreshHits.Load(); got > 2 {
		t.Fatalf("刷新次数应有界, refreshHits=%d", got)
	}
}

// TestRetryOnServerErrorThenSuccess 覆盖重试的主路径：上游瞬时 5xx 后恢复。
func TestRetryOnServerErrorThenSuccess(t *testing.T) {
	var mode atomic.Int64 // 1 = 第一次请求（回 5xx），2+ = 返回正常流
	adp, chatHits, _ := newRetryTestAdapter(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		if mode.Add(1) == 1 {
			w.WriteHeader(http.StatusBadGateway)
			_, _ = w.Write([]byte(`{"code":500,"msg":"transient"}`))
			return
		}
		_, _ = w.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"ok\"}}]}\n\n"))
		_, _ = w.Write([]byte("data: [DONE]\n\n"))
	})

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	s, err := adp.Stream(ctx, llm.RequestMessages{
		Model:    "m",
		Messages: []llm.Message{{Role: llm.RoleUser, Content: []llm.Content{{Type: llm.ContentText, Text: "hi"}}}},
	})
	if err != nil {
		t.Fatalf("5xx 后重试应成功: %v", err)
	}
	if c, ok := s.(interface{ Close() error }); ok {
		_ = c.Close()
	}
	if got := chatHits.Load(); got != 2 {
		t.Fatalf("应重试一次后成功, chatHits=%d", got)
	}
}

// TestRetryNotOnClientError 覆盖 4xx 语义拒绝不重试。
func TestRetryNotOnClientError(t *testing.T) {
	adp, chatHits, _ := newRetryTestAdapter(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"code":400,"msg":"bad request"}`))
	})

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_, err := adp.Stream(ctx, llm.RequestMessages{
		Model:    "m",
		Messages: []llm.Message{{Role: llm.RoleUser, Content: []llm.Content{{Type: llm.ContentText, Text: "hi"}}}},
	})
	if err == nil {
		t.Fatal("400 应失败")
	}
	if got := chatHits.Load(); got != 1 {
		t.Fatalf("400 不应重试, chatHits=%d", got)
	}
}
