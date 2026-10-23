package workbuddy

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/576469377/Agent2API/internal/llm"
)

func reloginFixture(t *testing.T) (*Adapter, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "account.json")
	raw := []byte(`{"auth":{"accessToken":"old-token","refreshToken":"old-refresh","domain":"www.workbuddy.ai","tokenType":"Bearer"},"account":{"uid":"fixture-user","nickname":"old"},"machineId":"existing-device","custom":{"keep":true}}`)
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	a, err := New(Config{Platform: GlobalPlatform, CredentialPath: path})
	if err != nil {
		t.Fatal(err)
	}
	return a, path
}

func TestReloginImmediatelyUsesAndPersistsNewSession(t *testing.T) {
	a, path := reloginFixture(t)
	fresh := &Credential{UID: "fixture-user", AccessToken: "new-token", RefreshToken: "new-refresh", TokenType: "Bearer", Domain: "www.workbuddy.ai", Nickname: "new", MachineID: "login-device", ExpiresAt: 123456789, RefreshExpAt: 234567890}
	if err := a.ReplaceCredential(fresh); err != nil {
		t.Fatal(err)
	}
	if got := a.auth.BuildHeaders(headerModeFull).Get("Authorization"); got != "Bearer new-token" {
		t.Fatal("running adapter retained old session")
	}
	loaded, err := loadCredentialFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.AccessToken != "new-token" || loaded.RefreshToken != "new-refresh" || loaded.Nickname != "new" || loaded.MachineID != "existing-device" || loaded.RefreshExpAt != fresh.RefreshExpAt {
		t.Fatal("new session, account metadata or existing device was lost")
	}
	raw, _ := os.ReadFile(path)
	var doc map[string]any
	_ = json.Unmarshal(raw, &doc)
	if doc["custom"] == nil {
		t.Fatal("unrelated credential metadata was removed")
	}
}

func TestReloginMismatchAndSaveFailureKeepOldSession(t *testing.T) {
	for _, reason := range []string{"uid", "region", "save"} {
		t.Run(reason, func(t *testing.T) {
			a, path := reloginFixture(t)
			fresh := &Credential{UID: "fixture-user", AccessToken: "new-token", Domain: "www.workbuddy.ai", TokenType: "Bearer"}
			switch reason {
			case "uid":
				fresh.UID = "different-user"
			case "region":
				fresh.Domain = "www.workbuddy.cn"
			case "save":
				if err := os.Mkdir(path+".tmp", 0o700); err != nil {
					t.Fatal(err)
				}
			}
			if err := a.ReplaceCredential(fresh); err == nil {
				t.Fatal("unsafe replacement succeeded")
			}
			if got := a.auth.BuildHeaders(headerModeFull).Get("Authorization"); got != "Bearer old-token" {
				t.Fatal("failed replacement changed live session")
			}
			loaded, err := loadCredentialFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if loaded.AccessToken != "old-token" {
				t.Fatal("failed replacement changed saved session")
			}
		})
	}
}

func TestReloginDoesNotLogOrInventFailureCause(t *testing.T) {
	a, _ := reloginFixture(t)
	err := a.ReplaceCredential(nil)
	if err == nil || llm.Wrap(err).Code != "credential_identity_mismatch" {
		t.Fatal("missing session was accepted")
	}
}

func TestReloginPersistencePreservesEncryptedTokensAndAccountFields(t *testing.T) {
	key := bytes.Repeat([]byte{42}, 32)
	access, _ := sealCredentialField("old-token", key)
	refresh, _ := sealCredentialField("old-refresh", key)
	nickname, _ := sealCredentialField("old-name", key)
	doc := map[string]any{"auth": map[string]any{"accessToken": access, "refreshToken": refresh, "domain": "www.workbuddy.ai"}, "account": map[string]any{"uid": "fixture-user", "nickname": nickname}}
	raw, _ := json.Marshal(doc)
	path := filepath.Join(t.TempDir(), "encrypted.json")
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	cred := &Credential{UID: "fixture-user", AccessToken: "new-token", RefreshToken: "new-refresh", Nickname: "new-name", Domain: "www.workbuddy.ai"}
	auth := newAuth(newHTTPClient("", 0), cred, path)
	reads := 0
	reader := func(global bool) ([]byte, error) {
		if !global {
			t.Fatal("wrong regional encryption key")
		}
		reads++
		return bytes.Clone(key), nil
	}
	if err := auth.persistCredentialUsing(cred, reader); err != nil {
		t.Fatal(err)
	}
	saved, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(saved, []byte("new-token")) || bytes.Contains(saved, []byte("new-refresh")) || bytes.Contains(saved, []byte("new-name")) {
		t.Fatal("encrypted credential fields became plaintext")
	}
	decoded, err := decodeCredentialDocumentUsing(saved, reader)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(decoded, []byte("new-token")) || !bytes.Contains(decoded, []byte("new-refresh")) || !bytes.Contains(decoded, []byte("new-name")) || reads != 2 {
		t.Fatal("encrypted update could not be read back")
	}
}
