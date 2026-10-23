package workbuddy

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/576469377/Agent2API/internal/llm"
)

func globalTestCredential(t *testing.T, domain string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "session.json")
	doc := map[string]any{"auth": map[string]any{"accessToken": "test-token", "refreshToken": "test-refresh", "domain": domain}, "account": map[string]any{"uid": "u1"}}
	raw, _ := json.Marshal(doc)
	if err := os.WriteFile(p, raw, 0600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestGlobalCredentialIsolation(t *testing.T) {
	t.Setenv("USERPROFILE", t.TempDir())
	t.Setenv("HOME", t.TempDir())
	t.Setenv("LOCALAPPDATA", t.TempDir())
	for _, p := range DefaultCredentialPathsForPlatform(GlobalPlatform) {
		if strings.Contains(p, "workbuddy-desktop.info") || strings.Contains(p, `.workbuddy\`) || strings.Contains(p, ".workbuddy/") {
			t.Fatalf("global discovered domestic path: %s", p)
		}
	}
	if AccountsDirForPlatform(GlobalPlatform) == AccountsDirForPlatform("workbuddy") {
		t.Fatal("account directories must be isolated")
	}
	for _, tc := range []struct {
		platform, domain string
		want             bool
	}{
		{GlobalPlatform, "www.workbuddy.ai", true}, {"workbuddy", "www.workbuddy.cn", true},
		{GlobalPlatform, "www.workbuddy.cn", false}, {"workbuddy", "www.workbuddy.ai", false},
	} {
		a, err := New(Config{Platform: tc.platform, CredentialPath: globalTestCredential(t, tc.domain)})
		if (err == nil) != tc.want {
			t.Fatalf("%s / %s: %v", tc.platform, tc.domain, err)
		}
		if err == nil && a.cli.baseURL != BaseURLForPlatform(tc.platform) {
			t.Fatalf("wrong upstream: %s", a.cli.baseURL)
		}
	}
}

func TestGlobalModelsNeverFallbackToDomestic(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(503) }))
	defer srv.Close()
	a, err := New(Config{Platform: GlobalPlatform, BaseURL: srv.URL, CredentialPath: globalTestCredential(t, "www.workbuddy.ai")})
	if err != nil {
		t.Fatal(err)
	}
	if models, err := a.ListModels(context.Background()); err == nil || len(models) != 0 {
		t.Fatalf("global must not expose domestic snapshot: %v, %v", models, err)
	}
}

func TestGlobalChatFallbackOnlyOnMissingRoute(t *testing.T) {
	for _, status := range []int{404, 405, 400, 403, 429} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			fallbackHits := 0
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Header.Get("X-Domain") != "www.workbuddy.ai" {
					t.Error("wrong domain")
				}
				if r.URL.Path == chatCompletionsPath {
					w.WriteHeader(status)
					return
				}
				if r.URL.Path != "/console/chat/completions" {
					t.Errorf("unexpected path %s", r.URL.Path)
				}
				fallbackHits++
				w.Header().Set("Content-Type", "text/event-stream")
				_, _ = w.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"ok\"}}]}\n\ndata: [DONE]\n\n"))
			}))
			defer srv.Close()
			a, err := New(Config{Platform: GlobalPlatform, BaseURL: srv.URL, CredentialPath: globalTestCredential(t, "www.workbuddy.ai")})
			if err != nil {
				t.Fatal(err)
			}
			s, err := a.dial(context.Background(), upstreamRequest{Model: "fast-model"}, a.newConversationMetadata(""))
			want := status == 404 || status == 405
			if (err == nil) != want || (fallbackHits == 1) != want {
				t.Fatalf("status=%d fallback=%d err=%v", status, fallbackHits, err)
			}
			if s != nil {
				_ = s.(interface{ Close() error }).Close()
			}
		})
	}
}

func TestGlobalLoginAndRefresh(t *testing.T) {
	var statePlatform string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/v2/plugin/auth/state":
			statePlatform = r.URL.Query().Get("platform")
			if r.Header.Get("X-No-Authorization") != "true" {
				t.Error("missing no-auth headers")
			}
			_, _ = w.Write([]byte(`{"code":0,"data":{"state":"s1","authUrl":"https://www.workbuddy.ai/login"}}`))
		case "/v2/plugin/auth/token":
			_, _ = w.Write([]byte(`{"code":0,"data":{"accessToken":"t1","refreshToken":"r1","expiresIn":3600}}`))
		case "/v2/plugin/login/account":
			_, _ = w.Write([]byte(`{"code":0,"data":{"uid":"u1","nickname":"test"}}`))
		case tokenRefreshPath:
			if r.Header.Get("X-Domain") != "www.workbuddy.ai" || r.Header.Get("X-Refresh-Token") != "r1" {
				t.Error("refresh headers wrong")
			}
			_, _ = w.Write([]byte(`{"code":0,"data":{"accessToken":"t2","refreshToken":"r2","expiresIn":3600,"refreshExpiresIn":7200}}`))
		default:
			t.Errorf("unexpected path %s", r.URL.Path)
			w.WriteHeader(404)
		}
	}))
	defer srv.Close()
	p := filepath.Join(t.TempDir(), "session.json")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cred, err := Login(ctx, srv.URL, LoginOptions{Region: GlobalPlatform, OutputPath: p})
	if err != nil {
		t.Fatal(err)
	}
	if statePlatform != "workbuddy-ai" || cred.Domain != "www.workbuddy.ai" {
		t.Fatalf("wrong international login: %s %s", statePlatform, cred.Domain)
	}
	a, err := New(Config{Platform: GlobalPlatform, BaseURL: srv.URL, CredentialPath: p})
	if err != nil {
		t.Fatal(err)
	}
	if err := a.auth.Refresh(ctx); err != nil {
		t.Fatal(err)
	}
	saved, err := loadCredentialFile(p)
	if err != nil || saved.AccessToken != "t2" || saved.RefreshToken != "r2" || saved.RefreshExpAt <= time.Now().UnixMilli() {
		t.Fatalf("refresh was not persisted: %v", err)
	}
}

func TestFlatCredential(t *testing.T) {
	p := filepath.Join(t.TempDir(), "flat.json")
	_ = os.WriteFile(p, []byte(`{"accessToken":"t","refreshToken":"r","domain":"www.workbuddy.ai","expiresAt":123456,"uid":"u"}`), 0600)
	c, err := loadCredentialFile(p)
	if err != nil || c.AccessToken != "t" || c.ExpiresAt != 123456 || c.UID != "u" {
		t.Fatalf("flat credential failed: %v", err)
	}
}

func TestGlobalRequiresInitialSystemPrompt(t *testing.T) {
	for _, system := range []string{"", "custom prompt"} {
		t.Run(system, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var body upstreamRequest
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Error(err)
				}
				if body.Model != "default-model" || len(body.Messages) != 2 || body.Messages[0].Role != "system" || body.Messages[1].Content != "hello" {
					t.Errorf("invalid global payload: %+v", body)
				}
				if system != "" && body.Messages[0].Content != system {
					t.Error("custom system prompt was replaced")
				}
				w.Header().Set("Content-Type", "text/event-stream")
				_, _ = w.Write([]byte("data: [DONE]\n\n"))
			}))
			defer srv.Close()
			a, err := New(Config{Platform: GlobalPlatform, BaseURL: srv.URL, CredentialPath: globalTestCredential(t, "www.workbuddy.ai")})
			if err != nil {
				t.Fatal(err)
			}
			s, err := a.Stream(context.Background(), llm.RequestMessages{SystemPrompt: system, Messages: []llm.Message{{Role: llm.RoleUser, Content: []llm.Content{{Type: llm.ContentText, Text: "hello"}}}}})
			if err != nil {
				t.Fatal(err)
			}
			_ = s.(interface{ Close() error }).Close()
		})
	}
}
