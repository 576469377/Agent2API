package app

import (
	"encoding/json"
	"errors"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func clientTestRequest(a *App, client, method, body string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, "http://127.0.0.1:8787/api/clients/"+client, strings.NewReader(body))
	r.RemoteAddr = "127.0.0.1:55555"
	r.Header.Set("Authorization", "Bearer test-gw-key")
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("X-Agent2API-Local-Action", client+"-sync")
	r.Header.Set("Origin", "http://localhost:8788")
	w := httptest.NewRecorder()
	a.Handler().ServeHTTP(w, r)
	return w
}

func TestPiEndpointCreatesConfigWithoutChangingDefaultsOrHistory(t *testing.T) {
	a, _, _ := zcodeApp(t)
	a.piPath = filepath.Join(t.TempDir(), "agent", "models.json")
	if w := clientTestRequest(a, "pi", "GET", ""); w.Code != 200 || !strings.Contains(w.Body.String(), `"available":true`) {
		t.Fatal("pi first setup not available", w.Body.String())
	}
	if _, err := os.Stat(filepath.Dir(a.piPath)); !os.IsNotExist(err) {
		t.Fatal("GET created a directory")
	}
	w := clientTestRequest(a, "pi", "POST", `{"platforms":["workbuddy"]}`)
	if w.Code != 200 || strings.Contains(w.Body.String(), "test-gw-key") || !strings.Contains(w.Body.String(), `"model_count":1`) {
		t.Fatalf("pi setup failed: %d %s", w.Code, w.Body.String())
	}
	raw, _ := os.ReadFile(a.piPath)
	var doc map[string]any
	json.Unmarshal(raw, &doc)
	providers := doc["providers"].(map[string]any)
	provider := providers["agent2api-workbuddy"].(map[string]any)
	if provider["baseUrl"] != "http://127.0.0.1:8787/v1" || provider["apiKey"] != "test-gw-key" {
		t.Fatal("wrong connection configuration")
	}
	stat, _ := os.Stat(a.piPath)
	w = clientTestRequest(a, "pi", "POST", `{"platforms":["workbuddy"]}`)
	after, _ := os.Stat(a.piPath)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"status":"unchanged"`) || !stat.ModTime().Equal(after.ModTime()) {
		t.Fatal("repeat setup rewrote the file", w.Body.String())
	}
	for _, name := range []string{"settings.json", "auth.json", "sessions"} {
		if _, err := os.Stat(filepath.Join(filepath.Dir(a.piPath), name)); !os.IsNotExist(err) {
			t.Fatal("setup created an unrelated file")
		}
	}
}

func TestClientEndpointsRejectRemoteOriginsPathsAndMissingAuth(t *testing.T) {
	a, _, _ := zcodeApp(t)
	a.piPath = filepath.Join(t.TempDir(), "agent", "models.json")
	a.ccSwitchPath = filepath.Join(t.TempDir(), "cc-switch.db")
	for _, client := range []string{"pi", "cc-switch"} {
		for _, tc := range []struct {
			name, remote, origin, action, body, key string
			want                                    int
		}{
			{"remote", "192.0.2.1:100", "http://localhost:8787", client + "-sync", `{"platforms":["workbuddy"]}`, "test-gw-key", 403},
			{"cross origin", "127.0.0.1:100", "https://foreign.example", client + "-sync", `{"platforms":["workbuddy"]}`, "test-gw-key", 403},
			{"missing header", "127.0.0.1:100", "http://localhost:8787", "", `{"platforms":["workbuddy"]}`, "test-gw-key", 400},
			{"arbitrary path", "127.0.0.1:100", "http://localhost:8787", client + "-sync", `{"platforms":["workbuddy"],"path":"other"}`, "test-gw-key", 400},
			{"arbitrary URL", "127.0.0.1:100", "http://localhost:8787", client + "-sync", `{"platforms":["workbuddy"],"base_url":"https://foreign.example"}`, "test-gw-key", 400},
			{"unknown platform", "127.0.0.1:100", "http://localhost:8787", client + "-sync", `{"platforms":["unknown"]}`, "test-gw-key", 400},
			{"empty platform", "127.0.0.1:100", "http://localhost:8787", client + "-sync", `{"platforms":[]}`, "test-gw-key", 400},
			{"trailing JSON", "127.0.0.1:100", "http://localhost:8787", client + "-sync", `{"platforms":["workbuddy"]} {}`, "test-gw-key", 400},
			{"no auth", "127.0.0.1:100", "http://localhost:8787", client + "-sync", `{"platforms":["workbuddy"]}`, "", 401},
		} {
			t.Run(client+" "+tc.name, func(t *testing.T) {
				r := httptest.NewRequest("POST", "/api/clients/"+client, strings.NewReader(tc.body))
				r.RemoteAddr = tc.remote
				r.Header.Set("Authorization", "Bearer "+tc.key)
				r.Header.Set("Origin", tc.origin)
				r.Header.Set("Content-Type", "application/json")
				r.Header.Set("X-Agent2API-Local-Action", tc.action)
				w := httptest.NewRecorder()
				a.Handler().ServeHTTP(w, r)
				if w.Code != tc.want {
					t.Fatalf("got %d, want %d", w.Code, tc.want)
				}
			})
		}
	}
	if _, err := os.Stat(filepath.Dir(a.piPath)); !os.IsNotExist(err) {
		t.Fatal("rejected requests created client files")
	}
}

func TestPiEndpointFailureLeavesConfigurationUntouched(t *testing.T) {
	for _, failure := range []string{"catalogue", "concurrent edit", "mutex", "credential"} {
		t.Run(failure, func(t *testing.T) {
			a, fixture, _ := zcodeApp(t)
			a.piPath = filepath.Join(t.TempDir(), "models.json")
			expected := `{"providers":{"other":{"models":[]}}}`
			os.WriteFile(a.piPath, []byte(expected), 0o600)
			switch failure {
			case "catalogue":
				fixture.err = errors.New("offline")
			case "concurrent edit":
				expected = `{"providers":{},"user_edit":true}`
				fixture.change = func() { os.WriteFile(a.piPath, []byte(expected), 0o600) }
			case "mutex":
				a.clientSyncMu.Lock()
				defer a.clientSyncMu.Unlock()
			case "credential":
				rt := a.hub.Platform("workbuddy")
				rt.Adapter = rejectedZCodeAuth{rt.Adapter}
			}
			w := clientTestRequest(a, "pi", "POST", `{"platforms":["workbuddy"]}`)
			if w.Code < 400 {
				t.Fatal("unsafe update accepted")
			}
			raw, _ := os.ReadFile(a.piPath)
			if string(raw) != expected {
				t.Fatal("configuration overwritten")
			}
		})
	}
}

func TestCCSwitchStatusDoesNotCreateDatabase(t *testing.T) {
	a, _, _ := zcodeApp(t)
	a.ccSwitchPath = filepath.Join(t.TempDir(), "missing", "cc-switch.db")
	w := clientTestRequest(a, "cc-switch", "GET", "")
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"available":false`) {
		t.Fatal("missing database reported ready")
	}
	if _, err := os.Stat(filepath.Dir(a.ccSwitchPath)); !os.IsNotExist(err) {
		t.Fatal("GET created a database directory")
	}
}
