package app

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/576469377/Agent2API/internal/adapter"
	"github.com/576469377/Agent2API/internal/adapter/workbuddy"
	"github.com/576469377/Agent2API/internal/config"
	"github.com/576469377/Agent2API/internal/llm"
)

func TestSharedBudgetAcrossHTTPRetriesAccountsAndPlatforms(t *testing.T) {
	var calls atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v3/config" {
			_, _ = io.WriteString(w, `{"code":0,"data":{"models":[{"id":"m","name":"M","maxOutputTokens":4096,"supportsToolCall":true,"credits":"x0.00"}]}}`)
			return
		}
		calls.Add(1)
		if r.Header.Get("X-Domain") == "www.workbuddy.cn" {
			w.WriteHeader(429)
			_, _ = io.WriteString(w, `{"code":14003,"msg":"too many requests"}`)
		} else {
			w.WriteHeader(503)
			_, _ = io.WriteString(w, `{"code":500,"msg":"temporary unavailable"}`)
		}
	}))
	defer server.Close()
	var runtimes []*PlatformRuntime
	for _, platform := range []string{"workbuddy", "workbuddy-global"} {
		pool := adapter.NewPool(platform, nil)
		domain := "www.workbuddy.cn"
		if platform == "workbuddy-global" {
			domain = "www.workbuddy.ai"
		}
		for i := 0; i < 2; i++ {
			path := filepath.Join(t.TempDir(), "account.json")
			raw, _ := json.Marshal(map[string]any{"auth": map[string]any{"accessToken": "fixture-token", "domain": domain}, "account": map[string]any{"uid": fmt.Sprintf("fixture-%s-%d", platform, i)}})
			if err := os.WriteFile(path, raw, 0600); err != nil {
				t.Fatal(err)
			}
			upstream, err := workbuddy.New(workbuddy.Config{Platform: platform, BaseURL: server.URL, CredentialPath: path, RequestTimeout: time.Second})
			if err != nil {
				t.Fatal(err)
			}
			pool.Add(fmt.Sprintf("account-%d", i), upstream)
		}
		runtimes = append(runtimes, &PlatformRuntime{ID: platform, Adapter: pool})
	}
	logger := log.New(io.Discard, "", 0)
	hub, err := NewHub(runtimes, logger)
	if err != nil {
		t.Fatal(err)
	}
	cfg := config.Default()
	cfg.Gateway.MaxUpstreamAttempts = 4
	cfg.Models.FallbackOnUnavailable = true
	a := New(cfg, hub, logger)
	request := httptest.NewRequest("POST", "/v1/messages", strings.NewReader(`{"model":"m","max_tokens":32,"stream":true,"messages":[{"role":"user","content":"private-fixture-prompt"}]}`))
	request.Header.Set("X-Request-ID", "untrusted-input")
	w := httptest.NewRecorder()
	a.Handler().ServeHTTP(w, request)
	if w.Code != 503 || calls.Load() != 4 {
		t.Fatalf("status=%d upstream calls=%d body=%s", w.Code, calls.Load(), w.Body.String())
	}
	requestID := w.Header().Get("X-Request-ID")
	if len(requestID) != 32 || requestID == "untrusted-input" || w.Header().Get("X-Upstream-Attempts") != "4" {
		t.Fatal("incorrect response trace")
	}
	rows := a.metrics.Snapshot().Recent
	if len(rows) != 1 || rows[0].RequestID != requestID || rows[0].AttemptCount != 4 || len(rows[0].Attempts) != 4 {
		t.Fatal("retry attempts inflated logical request metrics or were lost")
	}
	raw, _ := json.Marshal(rows[0].Attempts)
	if strings.Contains(string(raw), "private-fixture-prompt") || strings.Contains(string(raw), "fixture-token") {
		t.Fatal("private content leaked into attempts")
	}
	if rows[0].ErrorCode != "retry_budget_exhausted" {
		t.Fatalf("failure code=%s", rows[0].ErrorCode)
	}
}

func TestRoutingDeadlineCancelsWaitingButNotAnAcceptedStream(t *testing.T) {
	for _, accepted := range []bool{false, true} {
		logger := log.New(io.Discard, "", 0)
		pool := adapter.NewPool("fake", nil)
		delay := 0
		if !accepted {
			delay = 1000
		}
		pool.Add("test", &fakeAdapter{events: []llm.ResponseEvent{{Type: llm.EventStart}, {Type: llm.EventDone}}, perEventDelay: delay})
		hub, err := NewHub([]*PlatformRuntime{{ID: "fake", Adapter: pool}}, logger)
		if err != nil {
			t.Fatal(err)
		}
		a := New(config.Default(), hub, logger)
		execution := llm.NewExecution(6)
		execution.StartRouting(30 * time.Millisecond)
		parent, cancel := context.WithCancel(context.Background())
		ctx := llm.WithExecution(parent, execution)
		stream, _, err := a.openRoutedStream(ctx, llm.RequestMessages{Model: "m"}, "")
		if accepted {
			if err != nil {
				t.Fatal(err)
			}
			time.Sleep(50 * time.Millisecond)
			if _, err := stream.Recv(ctx); err != nil {
				t.Fatal("routing timer canceled an accepted stream")
			}
			stream.(io.Closer).Close()
		} else {
			if f := llm.Wrap(err); f.Code != "routing_timeout" || f.HTTPStatus() != 503 {
				t.Fatalf("routing timeout=%v", err)
			}
			if pool.Statuses()[0].InFlight != 0 || !pool.Statuses()[0].Healthy {
				t.Fatal("routing timeout leaked capacity or damaged the account")
			}
		}
		cancel()
	}
}

func TestWorkBuddyRoutingTimeoutKeepsAccountAndAttemptClassification(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v3/config" {
			_, _ = io.WriteString(w, `{"code":0,"data":{"models":[{"id":"m","name":"M","credits":"x0.00"}]}}`)
			return
		}
		_, _ = io.Copy(io.Discard, r.Body)
		<-r.Context().Done()
	}))
	defer server.Close()
	path := filepath.Join(t.TempDir(), "account.json")
	if err := os.WriteFile(path, []byte(`{"auth":{"accessToken":"fixture-token","expiresAt":9999999999999},"account":{"uid":"fixture"}}`), 0600); err != nil {
		t.Fatal(err)
	}
	upstream, err := workbuddy.New(workbuddy.Config{BaseURL: server.URL, CredentialPath: path})
	if err != nil {
		t.Fatal(err)
	}
	pool := adapter.NewPool("workbuddy", nil)
	pool.Add("fixture", upstream)
	logger := log.New(io.Discard, "", 0)
	hub, err := NewHub([]*PlatformRuntime{{ID: "workbuddy", Adapter: pool}}, logger)
	if err != nil {
		t.Fatal(err)
	}
	a := New(config.Default(), hub, logger)
	execution := llm.NewExecution(6)
	execution.StartRouting(100 * time.Millisecond)
	ctx := llm.WithExecution(context.Background(), execution)
	_, _, err = a.openRoutedStream(ctx, llm.RequestMessages{Model: "m"}, "workbuddy")
	if f := llm.Wrap(err); f == nil || f.Code != "routing_timeout" || f.HTTPStatus() != 503 {
		t.Fatalf("routing timeout lost: %v", err)
	}
	trace := execution.Snapshot()
	if trace.AttemptCount != 1 || trace.Attempts[0].Status != 503 || trace.Attempts[0].ErrorCode != "routing_timeout" {
		t.Fatalf("routing timer recorded as client cancellation: %+v", trace)
	}
	if status := pool.Statuses()[0]; status.InFlight != 0 || !status.Healthy {
		t.Fatal("local routing timeout leaked capacity or damaged account")
	}
}
