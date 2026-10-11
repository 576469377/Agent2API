package workbuddy

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"testing"
)

func TestEncryptedCredentialDocument(t *testing.T) {
	key := bytes.Repeat([]byte{42}, 32)
	a, err := sealCredentialField("access-test", key)
	if err != nil {
		t.Fatal(err)
	}
	r, _ := sealCredentialField("refresh-test", key)
	n, _ := sealCredentialField("nickname-test", key)
	doc := map[string]any{"auth": map[string]any{"accessToken": a, "refreshToken": r, "domain": "www.workbuddy.ai"}, "account": map[string]any{"nickname": n}, "accounts": []any{map[string]any{"nickname": n}}}
	raw, _ := json.Marshal(doc)
	calls := 0
	decoded, err := decodeCredentialDocumentUsing(raw, func(global bool) ([]byte, error) {
		calls++
		if !global {
			t.Error("wrong installation selected")
		}
		return bytes.Clone(key), nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if calls != 1 || !bytes.Contains(decoded, []byte("access-test")) || bytes.Contains(decoded, []byte("$wbEncrypted")) {
		t.Fatalf("credential decode failed, key calls=%d", calls)
	}
	if bytes.Contains(raw, []byte("access-test")) {
		t.Fatal("original document exposed token")
	}
}

func TestCredentialEnvelopeRejectsWrongKeyAndTampering(t *testing.T) {
	key := bytes.Repeat([]byte{42}, 32)
	v, _ := sealCredentialField("token", key)
	raw, _ := json.Marshal(v)
	_ = json.Unmarshal(raw, &v)
	if _, err := openCredentialField(v, bytes.Repeat([]byte{43}, 32)); err == nil {
		t.Fatal("wrong key accepted")
	}
	b, _ := base64.StdEncoding.DecodeString(v["envelope"].(string))
	var env credentialEnvelope
	_ = json.Unmarshal(b, &env)
	ct, _ := base64.StdEncoding.DecodeString(env.Ciphertext)
	ct[0] ^= 1
	env.Ciphertext = base64.StdEncoding.EncodeToString(ct)
	b, _ = json.Marshal(env)
	v["envelope"] = base64.StdEncoding.EncodeToString(b)
	if _, err := openCredentialField(v, key); err == nil {
		t.Fatal("tampered token accepted")
	}
	v["scheme"] = "asym-v1"
	if _, err := openCredentialField(v, key); err == nil {
		t.Fatal("unknown encryption must fail closed")
	}
}

func TestPlainCredentialDoesNotInvokeHelper(t *testing.T) {
	_, err := decodeCredentialDocumentUsing([]byte(`{"auth":{"accessToken":"plain"}}`), func(bool) ([]byte, error) { t.Fatal("helper called for plaintext"); return nil, nil })
	if err != nil {
		t.Fatal(err)
	}
}
