package app

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/576469377/Agent2API/internal/adapter"
	"github.com/576469377/Agent2API/internal/adapter/workbuddy"
	"github.com/576469377/Agent2API/internal/llm"
)

func writeLoginFixture(t *testing.T, path, uid, token string) {
	t.Helper()
	doc := map[string]any{"auth": map[string]any{"accessToken": token, "refreshToken": "refresh", "tokenType": "Bearer", "domain": "www.workbuddy.ai"}, "account": map[string]any{"uid": uid}, "machineId": "existing-device"}
	raw, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestReloginReplacesExistingPoolSessionWithoutRestartOrDuplicate(t *testing.T) {
	var authorization atomic.Value
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/v2/plugin/auth/state":
			fmt.Fprint(w, `{"code":0,"data":{"state":"fixture-state","authUrl":"https://fixture.invalid/authorize"}}`)
		case "/v2/plugin/auth/token":
			fmt.Fprint(w, `{"code":0,"data":{"accessToken":"new-token","refreshToken":"new-refresh","domain":"www.workbuddy.ai","tokenType":"Bearer"}}`)
		case "/v2/plugin/login/account":
			fmt.Fprint(w, `{"code":0,"data":{"uid":"fixture-user"}}`)
		case "/v2/chat/completions":
			authorization.Store(r.Header.Get("Authorization"))
			w.Header().Set("Content-Type", "text/event-stream")
			fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"OK\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	dir := t.TempDir()
	existing := filepath.Join(dir, "existing.json")
	writeLoginFixture(t, existing, "fixture-user", "old-token")
	adp, err := workbuddy.New(workbuddy.Config{Platform: workbuddy.GlobalPlatform, BaseURL: server.URL, CredentialPath: existing})
	if err != nil {
		t.Fatal(err)
	}
	pool := adapter.NewPool(workbuddy.GlobalPlatform, nil)
	pool.Add("existing.json", adp)
	rt := &PlatformRuntime{ID: workbuddy.GlobalPlatform, Adapter: pool, Pool: pool}
	a := &App{hub: &Hub{platforms: map[string]*PlatformRuntime{workbuddy.GlobalPlatform: rt}}}
	m := newLoginManager(t.Logf)
	m.replaceCredential = a.replaceLoginCredential
	requested := filepath.Join(dir, "requested.json")
	session, err := m.Start(workbuddy.GlobalPlatform, requested, server.URL)
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		session, err = m.Get(session.ID)
		if err != nil {
			t.Fatal(err)
		}
		if session.Status != loginPending {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if session.Status != loginSuccess || session.OutPath != existing || pool.Len() != 1 {
		t.Fatalf("relogin not installed: status=%s pool=%d", session.Status, pool.Len())
	}
	if _, err := os.Stat(requested); !os.IsNotExist(err) {
		t.Fatal("relogin created a duplicate watched credential")
	}
	staged, _ := filepath.Glob(requested + ".*.login.tmp")
	if len(staged) != 0 {
		t.Fatal("committed session was not cleaned up")
	}
	var doc struct {
		Auth struct {
			AccessToken string `json:"accessToken"`
		} `json:"auth"`
	}
	raw, _ := os.ReadFile(existing)
	if json.Unmarshal(raw, &doc) != nil || doc.Auth.AccessToken != "new-token" {
		t.Fatal("fresh login was discarded")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	s, err := pool.Stream(ctx, llm.RequestMessages{Model: "hy3", Messages: []llm.Message{{Role: llm.RoleUser, Content: []llm.Content{{Type: llm.ContentText, Text: "Hello"}}}}})
	if err != nil {
		t.Fatal(err)
	}
	for {
		_, err = s.Recv(ctx)
		if err == llm.ErrStreamDone {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
	}
	if authorization.Load() != "Bearer new-token" {
		t.Fatal("next pooled request used stale credentials")
	}
}

func TestCredentialCommitKeepsFreshSessionWhenUpdateFails(t *testing.T) {
	dir := t.TempDir()
	old := filepath.Join(dir, "old.json")
	stage := filepath.Join(dir, "new.login.tmp")
	writeLoginFixture(t, old, "fixture-user", "old-token")
	writeLoginFixture(t, stage, "fixture-user", "new-token")
	m := newLoginManager(t.Logf)
	m.replaceCredential = func(string, *workbuddy.Credential) (string, bool, error) {
		return "", true, fmt.Errorf("fixture save failed")
	}
	_, err := m.commitCredential(workbuddy.GlobalPlatform, old, stage, &workbuddy.Credential{UID: "fixture-user", AccessToken: "new-token"})
	if err == nil {
		t.Fatal("failed commit reported success")
	}
	if _, err := os.Stat(stage); err != nil {
		t.Fatal("fresh session was deleted on save failure")
	}
	var doc struct {
		Auth struct {
			AccessToken string `json:"accessToken"`
		} `json:"auth"`
	}
	raw, _ := os.ReadFile(old)
	_ = json.Unmarshal(raw, &doc)
	if doc.Auth.AccessToken != "old-token" {
		t.Fatal("old session overwritten after failed commit")
	}
}

func TestCredentialCommitUpdatesOfflineDuplicateAndProtectsOtherIdentity(t *testing.T) {
	for _, same := range []bool{true, false} {
		t.Run(fmt.Sprint(same), func(t *testing.T) {
			dir := t.TempDir()
			old := filepath.Join(dir, "existing.json")
			stage := filepath.Join(dir, "staged.login.tmp")
			writeLoginFixture(t, old, "fixture-user", "old-token")
			uid := "fixture-user"
			if !same {
				uid = "other-user"
			}
			writeLoginFixture(t, stage, uid, "new-token")
			m := newLoginManager(t.Logf)
			path, err := m.commitCredential(workbuddy.GlobalPlatform, old, stage, &workbuddy.Credential{UID: uid, AccessToken: "new-token", Domain: "www.workbuddy.ai", TokenType: "Bearer"})
			if same {
				if err != nil || path != old {
					t.Fatal("offline duplicate was not updated")
				}
				if _, err := os.Stat(stage); !os.IsNotExist(err) {
					t.Fatal("staged session retained after successful update")
				}
			} else {
				if err == nil {
					t.Fatal("different account overwrote existing credentials")
				}
				if _, err := os.Stat(stage); err != nil {
					t.Fatal("different account session was discarded")
				}
			}
		})
	}
}
