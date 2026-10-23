package main

import (
	"context"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/576469377/Agent2API/internal/adapter"
	"github.com/576469377/Agent2API/internal/adapter/trae"
	"github.com/576469377/Agent2API/internal/adapter/workbuddy"
	"github.com/576469377/Agent2API/internal/app"
	"github.com/576469377/Agent2API/internal/config"
	"github.com/576469377/Agent2API/internal/llm"
)

func TestTraeColdStartupHasIndependentWebAccount(t *testing.T) {
	sandboxHome(t)
	cfg := config.Default()
	cfg.Upstream.Platform = trae.Platform
	cfg.Upstream.AccountsDir = filepath.Join(t.TempDir(), "workbuddy-pool")
	hub, err := buildHub(cfg, log.New(io.Discard, "", 0))
	if err != nil || len(hub.Platforms()) != 1 {
		t.Fatalf("cold TRAE startup failed: %v", err)
	}
	rt := hub.Platform(trae.Platform)
	if rt == nil || rt.Pool != nil || rt.Adapter.Name() != trae.Platform {
		t.Fatal("TRAE incorrectly entered WorkBuddy account-pool discovery")
	}
	if _, err := os.Stat(cfg.Upstream.AccountsDir); !os.IsNotExist(err) {
		t.Fatal("TRAE created or read a WorkBuddy pool directory")
	}
	if d := rt.Adapter.(adapter.Describer).Describe(); d.Status != "unauthenticated" {
		t.Fatal("unsigned TRAE account claimed ready")
	}
}

func TestColdStartupKeepsLoginPlatforms(t *testing.T) {
	sandboxHome(t)
	logger := log.New(io.Discard, "", 0)
	for _, tc := range []struct {
		name, platform string
		want           int
	}{{"automatic", "", 2}, {"global only", workbuddy.GlobalPlatform, 1}} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := config.Default()
			cfg.Upstream.Platform = tc.platform
			hub, err := buildHub(cfg, logger)
			if err != nil {
				t.Fatalf("first login must not require existing credentials: %v", err)
			}
			if len(hub.Platforms()) != tc.want {
				t.Fatalf("platform count = %d, want %d", len(hub.Platforms()), tc.want)
			}
			for _, rt := range hub.Platforms() {
				if rt.Pool == nil || len(rt.AccountStatuses()) != 0 {
					t.Fatalf("%s must have an empty, usable pool", rt.ID)
				}
				if d := rt.Pool.Describe(); d.Status != "unauthenticated" {
					t.Fatalf("empty pool must not claim active: %+v", d)
				}
				_, err := rt.Adapter.Stream(context.Background(), llm.RequestMessages{})
				if err == nil || llm.Wrap(err).HTTPStatus() != http.StatusServiceUnavailable {
					t.Fatalf("unsigned pool must reject generation with 503: %v", err)
				}
			}
			application := app.New(cfg, hub, logger)
			for _, path := range []string{"/api/status", "/api/platforms"} {
				response := httptest.NewRecorder()
				application.Handler().ServeHTTP(response, httptest.NewRequest(http.MethodGet, path, nil))
				var body struct {
					Active    []adapter.Description `json:"active"`
					Platforms []adapter.Description `json:"platforms"`
				}
				if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
					t.Fatal(err)
				}
				items := body.Active
				if path == "/api/status" {
					items = body.Platforms
				}
				if response.Code != 200 || len(items) != tc.want {
					t.Fatalf("%s did not advertise unsigned platforms", path)
				}
				for _, d := range items {
					if d.BaseURL == "" || d.Status != "unauthenticated" {
						t.Fatalf("missing unsigned platform description: %+v", d)
					}
				}
			}
		})
	}
}

func TestFirstGlobalLoginJoinsPreviouslyEmptyPool(t *testing.T) {
	sandboxHome(t)
	logger := log.New(io.Discard, "", 0)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/v2/plugin/auth/state":
			if r.URL.Query().Get("platform") != "workbuddy-ai" {
				t.Error("first Global login used the domestic client identity")
			}
			io.WriteString(w, `{"code":0,"data":{"state":"test-state","authUrl":"https://example.invalid/authorize"}}`)
		case "/v2/plugin/auth/token":
			io.WriteString(w, `{"code":0,"data":{"accessToken":"test-token","refreshToken":"test-refresh","expiresIn":3600}}`)
		case "/v2/plugin/login/account":
			io.WriteString(w, `{"code":0,"data":{"uid":"test-global-account","nickname":"Test Global"}}`)
		case "/v3/config":
			io.WriteString(w, `{"code":0,"data":{"models":[{"id":"test-model"}]}}`)
		default:
			t.Errorf("unexpected upstream path %s", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer upstream.Close()
	cnPath := filepath.Join(t.TempDir(), "domestic.json")
	if err := os.WriteFile(cnPath, []byte(`{"auth":{"accessToken":"test-token","refreshToken":"test-refresh","domain":"www.workbuddy.cn"},"account":{"uid":"test-domestic-account"}}`), 0600); err != nil {
		t.Fatal(err)
	}
	cfg := config.Default()
	cfg.Upstream.Platforms = []config.PlatformConfig{
		{ID: "workbuddy", CredentialPath: cnPath, BaseURL: upstream.URL},
		{ID: workbuddy.GlobalPlatform, BaseURL: upstream.URL},
	}
	hub, err := buildHub(cfg, logger)
	if err != nil {
		t.Fatal(err)
	}
	rt := hub.Platform(workbuddy.GlobalPlatform)
	if rt == nil || rt.Pool == nil || len(rt.AccountStatuses()) != 0 {
		t.Fatal("domestic-only credentials hid the Global login runtime")
	}
	application := app.New(cfg, hub, logger)
	response := httptest.NewRecorder()
	application.Handler().ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/api/accounts/manage?platform=workbuddy-global&name=account-test.json", nil))
	var session struct {
		ID, Status, Error string
	}
	if response.Code != 200 || json.Unmarshal(response.Body.Bytes(), &session) != nil || session.ID == "" {
		t.Fatal("cannot initiate a first Global login from the console")
	}
	deadline := time.Now().Add(5 * time.Second)
	for session.Status != "success" && time.Now().Before(deadline) {
		if session.Status == "failed" {
			t.Fatalf("mock login failed: %s", session.Error)
		}
		time.Sleep(10 * time.Millisecond)
		status := httptest.NewRecorder()
		application.Handler().ServeHTTP(status, httptest.NewRequest(http.MethodGet, "/api/accounts/login-status?id="+session.ID, nil))
		if err := json.Unmarshal(status.Body.Bytes(), &session); err != nil {
			t.Fatal(err)
		}
	}
	if session.Status != "success" {
		t.Fatal("mock login did not finish")
	}
	dir := workbuddy.AccountsDirForPlatform(workbuddy.GlobalPlatform)
	if _, err := os.Stat(filepath.Join(dir, "account-test.json")); err != nil {
		t.Fatal("Global credential was not saved to its own pool directory")
	}
	// Run the same scan used by the periodic watcher; no restart or new Hub.
	scanAccountDir(rt, dir, cfg, logger, map[string]string{})
	scanAccountDir(rt, dir, cfg, logger, map[string]string{})
	if len(rt.AccountStatuses()) != 1 || len(hub.Platform("workbuddy").AccountStatuses()) != 1 {
		t.Fatal("first login was not loaded exactly once, or crossed regions")
	}
	if rt.Pool.Describe().Status != "active" {
		t.Fatal("signed-in pool did not become active")
	}
	models, err := hub.ListModels(context.Background())
	if err != nil || len(models) != 2 || hub.OwnerOf("workbuddy-global/test-model") != workbuddy.GlobalPlatform {
		t.Fatal("newly signed-in Global model did not become routable")
	}
}
