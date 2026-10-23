package main

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/576469377/Agent2API/internal/app"
	"github.com/576469377/Agent2API/internal/config"
)

func TestOpenRouterGatewayProtocols(t *testing.T) {
	t.Setenv("OPENROUTER_API_KEY", "")
	var generations atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/models" {
			io.WriteString(w, `{"data":[{"id":"author/model:free","name":"Free","context_length":32000,"top_provider":{"max_completion_tokens":8000},"pricing":{"prompt":"0","completion":"0"}},{"id":"author/paid","pricing":{"prompt":"1","completion":"1"}}]}`)
			return
		}
		generations.Add(1)
		var body struct {
			Model string `json:"model"`
		}
		if json.NewDecoder(r.Body).Decode(&body) != nil || body.Model != "author/model:free" || r.Header.Get("Authorization") != "Bearer test-upstream" {
			t.Error("gateway route or upstream credential incorrect")
		}
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, "data: {\"id\":\"gen-test\",\"model\":\"author/model:free\",\"choices\":[{\"delta\":{\"content\":\"gateway hello\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
	}))
	defer upstream.Close()
	cfg := config.Default()
	cfg.Auth.APIKey = "test-gateway"
	cfg.Upstream.Platforms = []config.PlatformConfig{{ID: "openrouter", BaseURL: upstream.URL, APIKey: "test-upstream", CredentialPath: filepath.Join(t.TempDir(), "openrouter.session.json")}}
	logger := log.New(io.Discard, "", 0)
	hub, err := buildHub(cfg, logger)
	if err != nil {
		t.Fatal(err)
	}
	application := app.New(cfg, hub, logger)
	for _, tc := range []struct{ path, input, end string }{
		{"/v1/chat/completions", `"messages":[{"role":"user","content":"hi"}]`, "[DONE]"},
		{"/v1/responses", `"input":"hi"`, "response.completed"},
		{"/v1/messages", `"max_tokens":100,"messages":[{"role":"user","content":"hi"}]`, "message_stop"},
	} {
		for _, stream := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/stream=%v", tc.path, stream), func(t *testing.T) {
				body := fmt.Sprintf(`{"model":"openrouter/author/model:free","stream":%v,%s}`, stream, tc.input)
				r := httptest.NewRequest(http.MethodPost, tc.path, strings.NewReader(body))
				r.Header.Set("Authorization", "Bearer test-gateway")
				w := httptest.NewRecorder()
				application.Handler().ServeHTTP(w, r)
				if w.Code != 200 || !strings.Contains(w.Body.String(), "gateway hello") || (stream && !strings.Contains(w.Body.String(), tc.end)) {
					t.Fatalf("protocol failure: %d %s", w.Code, w.Body.String())
				}
			})
		}
		body := fmt.Sprintf(`{"model":"openrouter/author/paid",%s}`, tc.input)
		r := httptest.NewRequest(http.MethodPost, tc.path, strings.NewReader(body))
		r.Header.Set("Authorization", "Bearer test-gateway")
		w := httptest.NewRecorder()
		application.Handler().ServeHTTP(w, r)
		if w.Code != 400 || !strings.Contains(w.Body.String(), "free_model_required") {
			t.Fatalf("paid model not rejected: %d %s", w.Code, w.Body.String())
		}
	}
	if generations.Load() != 6 {
		t.Fatalf("generation count %d, want exactly six free calls", generations.Load())
	}
	for _, path := range []string{"/api/config", "/api/platforms", "/v1/models"} {
		w := httptest.NewRecorder()
		r := httptest.NewRequest(http.MethodGet, path, nil)
		r.Header.Set("Authorization", "Bearer test-gateway")
		application.Handler().ServeHTTP(w, r)
		if strings.Contains(w.Body.String(), "test-upstream") || strings.Contains(w.Body.String(), "test-gateway") {
			t.Fatal("management response disclosed a key")
		}
		if path == "/v1/models" && (strings.Contains(w.Body.String(), "author/paid") || !strings.Contains(w.Body.String(), "openrouter/author/model:free")) {
			t.Fatal("free catalogue routing incorrect")
		}
	}
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/api/config", strings.NewReader(`{"refresh_models":true}`))
	r.Header.Set("Authorization", "Bearer test-gateway")
	application.Handler().ServeHTTP(w, r)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"refresh_models":1`) {
		t.Fatal("OpenRouter catalogue could not be invalidated")
	}
}
