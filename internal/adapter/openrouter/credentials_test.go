package openrouter

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSavedKeyIsValidatedAndSurvivesRestart(t *testing.T) {
	t.Setenv("OPENROUTER_API_KEY", "startup-key")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/key" || r.Header.Get("Authorization") != "Bearer replacement-key" {
			t.Error("wrong validation request")
		}
		fmt.Fprint(w, `{"data":{"is_free_tier":true,"is_management_key":false}}`)
	}))
	defer server.Close()
	cfg := Config{BaseURL: server.URL, CredentialPath: filepath.Join(t.TempDir(), "key.json")}
	a, _ := New(cfg)
	if a.key() != "startup-key" {
		t.Fatal("startup credential lost")
	}
	if err := a.SaveAPIKey(context.Background(), " replacement-key "); err != nil {
		t.Fatal(err)
	}
	if a.key() != "replacement-key" {
		t.Fatal("saved key did not activate")
	}
	reloaded, _ := New(cfg)
	if reloaded.key() != "replacement-key" {
		t.Fatal("saved connection did not survive restart")
	}
	if err := reloaded.VerifyConnection(context.Background()); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(fmt.Sprintf("%+v", reloaded.Describe()), "replacement-key") {
		t.Fatal("key leaked in description")
	}
}

func TestInvalidOrUnconfirmedKeyPreservesPreviousConnection(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
		body   string
	}{
		{"unauthorized", 401, `{"error":{"message":"candidate-secret"}}`},
		{"server failure", 503, `candidate-secret`},
		{"malformed", 200, `{"data":null}`},
		{"missing evidence", 200, `{"data":{}}`},
		{"management key", 200, `{"data":{"is_free_tier":false,"is_management_key":true}}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(tc.status); fmt.Fprint(w, tc.body) }))
			defer server.Close()
			path := filepath.Join(t.TempDir(), "key.json")
			before := []byte(`{"version":1,"api_key":"previous-key"}`)
			if err := os.WriteFile(path, before, 0o600); err != nil {
				t.Fatal(err)
			}
			a, _ := New(Config{BaseURL: server.URL, CredentialPath: path})
			err := a.SaveAPIKey(context.Background(), "candidate-secret")
			if err == nil || strings.Contains(err.Error(), "candidate-secret") {
				t.Fatal("unconfirmed key accepted or leaked")
			}
			after, _ := os.ReadFile(path)
			if string(after) != string(before) || a.key() != "previous-key" {
				t.Fatal("failed validation replaced the original connection")
			}
		})
	}
}

func TestKeyValidationRejectsRedirectAndFailedSaveDoesNotActivate(t *testing.T) {
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { t.Error("credential followed redirect") }))
	defer target.Close()
	redirect := true
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if redirect {
			http.Redirect(w, r, target.URL, http.StatusTemporaryRedirect)
			return
		}
		fmt.Fprint(w, `{"data":{"is_free_tier":true}}`)
	}))
	defer server.Close()
	a, _ := New(Config{BaseURL: server.URL, APIKeyEnv: "UNUSED_OPENROUTER_TEST_KEY", APIKey: "previous-key", CredentialPath: filepath.Join(t.TempDir(), "key.json")})
	if err := a.SaveAPIKey(context.Background(), "candidate-secret"); err == nil {
		t.Fatal("redirect accepted")
	}
	redirect = false
	blocked := filepath.Join(t.TempDir(), "not-a-directory")
	if err := os.WriteFile(blocked, []byte("sentinel"), 0o600); err != nil {
		t.Fatal(err)
	}
	a.cfg.CredentialPath = filepath.Join(blocked, "key.json")
	if err := a.SaveAPIKey(context.Background(), "candidate-secret"); err == nil || a.key() != "previous-key" {
		t.Fatal("failed persistence activated key")
	}
}

func TestMalformedSavedKeyFailsClosedWithoutUsingStartupKey(t *testing.T) {
	t.Setenv("OPENROUTER_API_KEY", "startup-key")
	path := filepath.Join(t.TempDir(), "key.json")
	os.WriteFile(path, []byte(`{"version":1,"api_key":""}`), 0o600)
	a, _ := New(Config{CredentialPath: path})
	if a.key() != "" || a.Describe().Status != "unauthenticated" {
		t.Fatal("damaged explicit connection silently fell back")
	}
}
