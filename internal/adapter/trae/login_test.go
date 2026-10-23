package trae

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"
)

func TestLoginPKCEAndCallbackIdentity(t *testing.T) {
	var exchanges atomic.Int32
	var exchangeBody map[string]any
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		exchanges.Add(1)
		if r.URL.Path != "/trae/api/v3/oauth/ExchangeToken" {
			t.Errorf("path = %s", r.URL.Path)
		}
		_ = json.NewDecoder(r.Body).Decode(&exchangeBody)
		json.NewEncoder(w).Encode(map[string]any{"Result": map[string]any{
			"Token": "unit-test-only", "RefreshToken": "test-refresh", "TokenExpireAt": time.Now().Add(time.Hour).Format(time.RFC3339),
			"UserInfo": map[string]string{"UserID": "fixture-user", "Region": "CN"},
		}})
	}))
	defer upstream.Close()
	out := filepath.Join(t.TempDir(), "trae.session.json")
	ready := make(chan string, 1)
	done := make(chan error, 1)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	client := &http.Client{Timeout: 2 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	go func() {
		_, err := login(ctx, LoginOptions{OutputPath: out, OnAuthURL: func(u string) { ready <- u }, device: loginDevice{"fixture-device", "fixture-machine"}}, upstream.URL, client)
		done <- err
	}()
	var entry string
	select {
	case entry = <-ready:
	case <-ctx.Done():
		t.Fatal("login URL missing")
	}
	resp, err := client.Get(entry)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	authorize, err := url.Parse(resp.Header.Get("Location"))
	if err != nil {
		t.Fatal(err)
	}
	q := authorize.Query()
	if authorize.Host != "www.trae.cn" || q.Get("code_challenge_method") != "S256" || q.Get("client_id") != publicClientID || q.Get("device_id") != "fixture-device" || q.Get("machine_id") != "fixture-machine" {
		t.Fatal("unexpected authorization parameters")
	}
	callback := q.Get("auth_callback_url")
	if u, _ := url.Parse(callback); u.Hostname() != "127.0.0.1" || u.Path != "/authorize" {
		t.Fatal("callback must stay on loopback")
	}
	resp, err = client.Get(callback + "?loginTraceID=wrong&authCodeInfo=%7B%7D")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest || exchanges.Load() != 0 {
		t.Fatal("unmatched callback reached exchange")
	}
	cbQuery := url.Values{"loginTraceID": {q.Get("login_trace_id")}, "authCodeInfo": {`{"AuthCode":"fixture-code"}`}, "host": {"https://untrusted.invalid"}, "userInfo": {`{"UserID":"spoofed"}`}}
	resp, err = client.Get(callback + "?" + cbQuery.Encode())
	if err != nil {
		t.Fatal(err)
	}
	page, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK || len(page) == 0 {
		t.Fatalf("callback status = %d", resp.StatusCode)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-ctx.Done():
		t.Fatal("callback not completed")
	}
	if exchanges.Load() != 1 || exchangeBody["AuthCode"] != "fixture-code" {
		t.Fatal("unexpected token exchange")
	}
	hash := sha256.Sum256([]byte(exchangeBody["CodeVerifier"].(string)))
	if base64.RawURLEncoding.EncodeToString(hash[:]) != q.Get("code_challenge") {
		t.Fatal("PKCE verifier mismatch")
	}
	c, err := readCredential(out)
	if err != nil || c.UserID != "fixture-user" || c.Token != "unit-test-only" {
		t.Fatal("trusted exchange identity was not saved")
	}
}

func TestCredentialDoesNotReplaceDifferentAccount(t *testing.T) {
	out := filepath.Join(t.TempDir(), "trae.session.json")
	c := Credential{UserID: "first", Token: "unit-test-only", ClientID: publicClientID, Region: "CN", ExpiresAt: time.Now().Add(time.Hour).Format(time.RFC3339)}
	if err := saveCredential(out, c); err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(out)
	c.UserID = "second"
	if err := saveCredential(out, c); err == nil {
		t.Fatal("different account overwritten")
	}
	after, _ := os.ReadFile(out)
	if string(before) != string(after) {
		t.Fatal("original credential changed")
	}
}

func TestLoginCancellationClosesListener(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	ready := make(chan string, 1)
	done := make(chan error, 1)
	out := filepath.Join(t.TempDir(), "trae.session.json")
	go func() {
		_, err := login(ctx, LoginOptions{OutputPath: out, OnAuthURL: func(u string) { ready <- u }}, authBaseURL, http.DefaultClient)
		done <- err
	}()
	var entry string
	select {
	case entry = <-ready:
	case <-time.After(3 * time.Second):
		cancel()
		t.Fatal("URL missing")
	}
	cancel()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("cancellation succeeded")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("listener did not stop")
	}
	client := &http.Client{Timeout: time.Second}
	if resp, err := client.Get(entry); err == nil {
		resp.Body.Close()
		t.Fatal("canceled login still listens")
	}
}

func TestExchangeRejectsUnknownIdentityAndExpiredToken(t *testing.T) {
	for _, result := range []map[string]any{
		{"Token": "unit-test-only", "TokenExpireAt": time.Now().Add(time.Hour).Format(time.RFC3339)},
		{"Token": "unit-test-only", "UserID": "fixture-user", "TokenExpireAt": time.Now().Add(-time.Hour).Format(time.RFC3339)},
		{"Token": "unit-test-only", "UserID": "fixture-user", "TokenExpireAt": time.Now().Add(time.Hour).Format(time.RFC3339), "UserInfo": map[string]string{"Region": "US"}},
	} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			json.NewEncoder(w).Encode(map[string]any{"Result": result})
		}))
		_, err := exchangeToken(context.Background(), srv.Client(), srv.URL, "fixture-code", "verifier", "", testAuthorizationGrant(t))
		srv.Close()
		if err == nil {
			t.Fatal("incomplete or unsupported identity accepted")
		}
	}
}

func TestExchangeResolvesMissingUserInfoFromTrustedProfile(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/cloudide/api/v3/trae/GetUserInfo" {
			if r.Header.Get("x-cloudide-token") != "unit-test-only" || r.Header.Get("Authorization") != "" {
				t.Error("unexpected profile authentication")
			}
			json.NewEncoder(w).Encode(map[string]any{"Result": map[string]string{"UserID": "trusted-user", "Region": "China-North"}})
			return
		}
		json.NewEncoder(w).Encode(map[string]any{"Result": map[string]any{"Token": "unit-test-only", "TokenExpireAt": time.Now().Add(time.Hour).UnixMilli(), "RefreshToken": "test-refresh"}})
	}))
	defer srv.Close()
	c, err := exchangeToken(context.Background(), srv.Client(), srv.URL, "fixture-code", "verifier", "", testAuthorizationGrant(t))
	if err != nil || c.UserID != "trusted-user" || c.Region != "CN" {
		t.Fatalf("profile resolution failed: %v", err)
	}
}

func TestExchangeFailureKeepsTrustedTokenAndSafeBusinessCode(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/cloudide/api/v3/trae/GetUserInfo" {
			w.WriteHeader(503)
			return
		}
		json.NewEncoder(w).Encode(map[string]any{"Result": map[string]any{"Token": "unit-test-only", "TokenExpireAt": time.Now().Add(time.Hour).UnixMilli()}})
	}))
	defer srv.Close()
	c, err := exchangeToken(context.Background(), srv.Client(), srv.URL, "fixture-code", "verifier", "", testAuthorizationGrant(t))
	if err == nil || c.Token == "" {
		t.Fatal("trusted exchange result was discarded")
	}
}

func testAuthorizationGrant(t *testing.T) authorizationGrant {
	t.Helper()
	g, err := newAuthorizationGrant("fixture-code", "verifier", loginDevice{"fixture-device", "fixture-machine"})
	if err != nil {
		t.Fatal(err)
	}
	return g
}
