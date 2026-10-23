package main

import (
	"context"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/576469377/Agent2API/internal/adapter"
	"github.com/576469377/Agent2API/internal/adapter/workbuddy"
	"github.com/576469377/Agent2API/internal/app"
	"github.com/576469377/Agent2API/internal/llm"
)

type keepaliveProbe struct {
	started chan struct{}
	calls   atomic.Int32
}

func (p *keepaliveProbe) Name() string                                            { return "workbuddy" }
func (p *keepaliveProbe) ListModels(context.Context) ([]adapter.ModelInfo, error) { return nil, nil }
func (p *keepaliveProbe) Stream(context.Context, llm.RequestMessages) (llm.ResponseStream, error) {
	panic("keepalive must not generate")
}
func (p *keepaliveProbe) EnsureValid(ctx context.Context) error {
	p.calls.Add(1)
	close(p.started)
	<-ctx.Done()
	return ctx.Err()
}

func TestKeepaliveCancellationStopsTheCurrentPass(t *testing.T) {
	pool := adapter.NewPool("workbuddy", nil)
	logger := log.New(io.Discard, "", 0)
	hub, err := app.NewHub([]*app.PlatformRuntime{{ID: "workbuddy", Adapter: pool, Pool: pool}}, logger)
	if err != nil {
		t.Fatal(err)
	}
	first, second := &keepaliveProbe{started: make(chan struct{})}, &keepaliveProbe{started: make(chan struct{})}
	pool.Add("first", first)
	pool.Add("second", second)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() { refreshExpiring(ctx, hub, logger); close(done) }()
	select {
	case <-first.started:
	case <-time.After(time.Second):
		t.Fatal("keepalive did not reach its first account")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("keepalive did not stop its current pass")
	}
	if first.calls.Load() != 1 || second.calls.Load() != 0 {
		t.Fatal("keepalive began another account after shutdown")
	}
}

func TestKeepaliveRefreshesWorkBuddyCredentials(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v2/plugin/auth/token/refresh" {
			t.Errorf("keepalive requested unexpected path %s", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
			return
		}
		calls.Add(1)
		io.WriteString(w, `{"code":0,"data":{"accessToken":"new-test-token","expiresIn":3600}}`)
	}))
	defer server.Close()
	path := filepath.Join(t.TempDir(), "account.json")
	if err := os.WriteFile(path, []byte(`{"auth":{"accessToken":"test-token","refreshToken":"test-refresh","domain":"www.workbuddy.cn","expiresAt":1},"account":{"uid":"test-user"}}`), 0600); err != nil {
		t.Fatal(err)
	}
	account, err := workbuddy.New(workbuddy.Config{BaseURL: server.URL, CredentialPath: path})
	if err != nil {
		t.Fatal(err)
	}
	pool := adapter.NewPool("workbuddy", nil)
	logger := log.New(io.Discard, "", 0)
	hub, err := app.NewHub([]*app.PlatformRuntime{{ID: "workbuddy", Adapter: pool, Pool: pool}}, logger)
	if err != nil {
		t.Fatal(err)
	}
	pool.Add("fixture", account)
	refreshExpiring(context.Background(), hub, logger)
	if calls.Load() != 1 {
		t.Fatalf("WorkBuddy keepalive performed %d refreshes, want one", calls.Load())
	}
	refreshExpiring(context.Background(), hub, logger)
	if calls.Load() != 1 {
		t.Fatal("keepalive refreshed a credential which is still valid")
	}
}
