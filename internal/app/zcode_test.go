package app

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/576469377/Agent2API/internal/adapter"
	"github.com/576469377/Agent2API/internal/config"
)

type zcodeCatalogFixture struct {
	fakeAdapter
	err    error
	change func()
}

func (f *zcodeCatalogFixture) ListModels(context.Context) ([]adapter.ModelInfo, error) {
	if f.change != nil {
		f.change()
	}
	zero := 0.0
	return []adapter.ModelInfo{{ID: "free", ContextTokens: 10000, MaxOutputTokens: 1000, SupportsTools: true, Pricing: &adapter.ModelPricing{Status: "free", CreditMultiplier: &zero, Source: "upstream_config", ObservedAt: time.Now().UTC().Format(time.RFC3339)}}}, f.err
}

func zcodeApp(t *testing.T) (*App, *zcodeCatalogFixture, string) {
	t.Helper()
	f := &zcodeCatalogFixture{}
	h, err := NewHub([]*PlatformRuntime{{ID: "workbuddy", Adapter: f}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	cfg := config.Default()
	cfg.MetricsFile = ""
	cfg.Auth.APIKey = "test-gw-key"
	a := New(cfg, h, h.logger)
	path := filepath.Join(t.TempDir(), "provider_config.json")
	source := `{"schemaVersion":1,"config":{"providerOrder":["other"],"providerConfigRules":{"providerRules":[{"providerId":"other","enabled":true}]},"defaultModelSelection":{"providerId":"other","modelId":"keep"}}}`
	if err := os.WriteFile(path, []byte(source), 0o600); err != nil {
		t.Fatal(err)
	}
	a.zcodePath = path
	return a, f, path
}

func zcodeRequest(a *App, method, body string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, "http://127.0.0.1:8787/api/clients/zcode", strings.NewReader(body))
	r.RemoteAddr = "127.0.0.1:55555"
	r.Header.Set("Authorization", "Bearer test-gw-key")
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("X-Agent2API-Local-Action", "zcode-sync")
	r.Header.Set("Origin", "http://localhost:8788")
	w := httptest.NewRecorder()
	a.Handler().ServeHTTP(w, r)
	return w
}

func TestZCodeButtonEndpointConfiguresModelsAndRepeatDoesNotRewrite(t *testing.T) {
	a, _, path := zcodeApp(t)
	if w := zcodeRequest(a, "GET", ""); w.Code != 200 || !strings.Contains(w.Body.String(), `"available":true`) {
		t.Fatalf("status: %d %s", w.Code, w.Body.String())
	}
	w := zcodeRequest(a, "POST", "{}")
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"model_count":1`) || strings.Contains(w.Body.String(), "test-gw-key") {
		t.Fatalf("sync: %d %s", w.Code, w.Body.String())
	}
	before, _ := os.Stat(path)
	w = zcodeRequest(a, "POST", "{}")
	after, _ := os.Stat(path)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"status":"unchanged"`) || !before.ModTime().Equal(after.ModTime()) {
		t.Fatal("repeat click rewrote the configuration")
	}
	raw, _ := os.ReadFile(path)
	var doc map[string]any
	json.Unmarshal(raw, &doc)
	cfg := doc["config"].(map[string]any)
	if cfg["defaultModelSelection"].(map[string]any)["providerId"] != "other" {
		t.Fatal("foreign default changed")
	}
	providers := cfg["providerConfigRules"].(map[string]any)["providerRules"].([]any)
	connection := providers[1].(map[string]any)["config"].(map[string]any)
	if connection["api"].(map[string]any)["baseUrl"] != "http://127.0.0.1:8787/v1" {
		t.Fatal("wrong gateway address")
	}
}

func TestZCodeSyncRejectsRemoteCrossOriginAndArbitraryPaths(t *testing.T) {
	a, _, path := zcodeApp(t)
	before, _ := os.ReadFile(path)
	for _, tc := range []struct {
		name, remote, forwarded, origin, header, body string
		want                                          int
	}{
		{"remote", "192.0.2.1:1234", "", "http://localhost:8787", "zcode-sync", "{}", 403},
		{"remote proxy", "127.0.0.1:1234", "192.0.2.1", "http://localhost:8787", "zcode-sync", "{}", 403},
		{"cross origin", "127.0.0.1:1234", "", "https://evil.example", "zcode-sync", "{}", 403},
		{"missing action header", "127.0.0.1:1234", "", "http://localhost:8787", "", "{}", 400},
		{"arbitrary path", "127.0.0.1:1234", "", "http://localhost:8787", "zcode-sync", `{"path":"other"}`, 400},
		{"trailing JSON", "127.0.0.1:1234", "", "http://localhost:8787", "zcode-sync", `{} {}`, 400},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest("POST", "/api/clients/zcode", strings.NewReader(tc.body))
			r.RemoteAddr = tc.remote
			r.Header.Set("Authorization", "Bearer test-gw-key")
			r.Header.Set("Content-Type", "application/json")
			r.Header.Set("Origin", tc.origin)
			r.Header.Set("X-Agent2API-Local-Action", tc.header)
			r.Header.Set("X-Forwarded-For", tc.forwarded)
			w := httptest.NewRecorder()
			a.Handler().ServeHTTP(w, r)
			if w.Code != tc.want {
				t.Fatalf("got %d, want %d", w.Code, tc.want)
			}
		})
	}
	raw, _ := os.ReadFile(path)
	if string(raw) != string(before) {
		t.Fatal("rejected request changed file")
	}
	r := httptest.NewRequest("POST", "/api/clients/zcode", strings.NewReader("{}"))
	r.RemoteAddr = "127.0.0.1:1234"
	w := httptest.NewRecorder()
	a.Handler().ServeHTTP(w, r)
	if w.Code != http.StatusUnauthorized {
		t.Fatal("gateway authentication bypassed")
	}
}

func TestZCodeSyncPreservesFileOnCatalogueFailureOrConcurrentEdit(t *testing.T) {
	for _, kind := range []string{"catalogue failure", "concurrent edit", "already syncing"} {
		t.Run(kind, func(t *testing.T) {
			a, f, path := zcodeApp(t)
			before, _ := os.ReadFile(path)
			expected := before
			switch kind {
			case "catalogue failure":
				f.err = errors.New("unavailable")
			case "concurrent edit":
				expected = []byte(`{"edited":true}`)
				f.change = func() { os.WriteFile(path, expected, 0o600) }
			case "already syncing":
				a.zcodeMu.Lock()
				defer a.zcodeMu.Unlock()
			}
			w := zcodeRequest(a, "POST", "{}")
			if w.Code < 400 {
				t.Fatal("unsafe sync was accepted")
			}
			after, _ := os.ReadFile(path)
			if string(after) != string(expected) {
				t.Fatal("original configuration overwritten")
			}
			backups, _ := filepath.Glob(path + ".bak-*")
			if len(backups) != 0 {
				t.Fatal("failed sync created a backup or wrote configuration")
			}
		})
	}
}

func TestZCodeStatusReportsMissingProfileWithoutCreatingIt(t *testing.T) {
	a, _, _ := zcodeApp(t)
	a.zcodePath = filepath.Join(t.TempDir(), "missing", "provider_config.json")
	w := zcodeRequest(a, "GET", "")
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"available":false`) {
		t.Fatal("missing profile was reported available")
	}
	if _, err := os.Stat(filepath.Dir(a.zcodePath)); !os.IsNotExist(err) {
		t.Fatal("status request created a profile")
	}
}

type rejectedZCodeAuth struct{ adapter.Adapter }

func (rejectedZCodeAuth) VerifyConnection(context.Context) error {
	return errors.New("invalid credential")
}

func TestZCodeSyncDoesNotImportPublicCatalogueWithInvalidCredentials(t *testing.T) {
	a, _, path := zcodeApp(t)
	before, _ := os.ReadFile(path)
	rt := a.hub.Platform("workbuddy")
	rt.Adapter = rejectedZCodeAuth{rt.Adapter}
	w := zcodeRequest(a, "POST", "{}")
	if w.Code != 422 {
		t.Fatal("unusable credentials accepted for client setup")
	}
	after, _ := os.ReadFile(path)
	if string(before) != string(after) {
		t.Fatal("failed credential check changed client configuration")
	}
}
