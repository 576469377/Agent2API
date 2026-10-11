package workbuddy

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// WorkBuddy's fields policy uses AES-GCM sym-v1 envelopes. Obtain the local
// installation's key from its native storage API, never from a bundled constant.
// Key material stays in memory; stdout is captured by this process, not logged.
func localCredentialKey(global bool) ([]byte, error) {
	exe := os.Getenv("AGENT2API_WORKBUDDY_EXECUTABLE")
	if exe == "" {
		home, _ := os.UserHomeDir()
		name := "WorkBuddy"
		if global {
			name = "WorkBuddyAI"
		}
		switch runtime.GOOS {
		case "windows":
			exe = filepath.Join(os.Getenv("LOCALAPPDATA"), "Programs", name, name+".exe")
		case "darwin":
			app := name
			if global {
				app = "WorkBuddy AI"
			}
			exe = filepath.Join("/Applications", app+".app", "Contents/MacOS", name)
		default:
			exe = filepath.Join(home, ".local/share", name, name)
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, exe, "-e", `process.stdout.write(process._linkedBinding('electron_browser_workbuddy_storage').loggerGet())`)
	for _, e := range os.Environ() {
		if !strings.HasPrefix(strings.ToUpper(e), "ELECTRON_RUN_AS_NODE=") {
			cmd.Env = append(cmd.Env, e)
		}
	}
	cmd.Env = append(cmd.Env, "ELECTRON_RUN_AS_NODE=1")
	hideCredentialHelper(cmd)
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("无法读取本机 WorkBuddy 加密密钥，请检查安装位置或设置 AGENT2API_WORKBUDDY_EXECUTABLE")
	}
	defer clear(out)
	var payload struct {
		Version int    `json:"version"`
		Secret  string `json:"atRestSecretKey"`
	}
	if json.Unmarshal(out, &payload) != nil || payload.Version != 1 {
		return nil, fmt.Errorf("不支持的 WorkBuddy 密钥格式")
	}
	secret, err := base64.StdEncoding.Strict().DecodeString(payload.Secret)
	if err != nil || len(secret) != 32 {
		return nil, fmt.Errorf("无效的 WorkBuddy 密钥")
	}
	defer clear(secret)
	key := sha256.Sum256([]byte(payload.Secret))
	return key[:], nil
}

type credentialEnvelope struct {
	Suite      int    `json:"suite"`
	KeyID      string `json:"keyId"`
	Nonce      string `json:"nonce"`
	AuthTag    string `json:"authTag"`
	Ciphertext string `json:"ciphertext"`
}

func credentialAAD(keyID string) []byte {
	out := append([]byte("WB-AAD\x00"), 1)
	for _, s := range []string{"WBEV1", "sym-v1"} {
		out = binary.BigEndian.AppendUint32(out, uint32(len(s)))
		out = append(out, s...)
	}
	out = binary.BigEndian.AppendUint32(out, 1)
	out = binary.BigEndian.AppendUint32(out, uint32(len(keyID)))
	out = append(out, keyID...)
	return append(out, 2, 0, 0)
}

func credentialGCM(key []byte) (cipher.AEAD, string, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, "", err
	}
	gcm, err := cipher.NewGCM(block)
	hash := sha256.Sum256(key)
	return gcm, hex.EncodeToString(hash[:8]), err
}

func openCredentialField(v map[string]any, key []byte) (string, error) {
	if v["$wbEncrypted"] != float64(1) || v["scheme"] != nil {
		return "", fmt.Errorf("不支持的 WorkBuddy 加密字段格式（仅支持 sym-v1）")
	}
	s, _ := v["envelope"].(string)
	raw, err := base64.StdEncoding.Strict().DecodeString(s)
	if err != nil {
		return "", fmt.Errorf("无效的凭证加密信封")
	}
	var e credentialEnvelope
	if json.Unmarshal(raw, &e) != nil || e.Suite != 1 {
		return "", fmt.Errorf("不支持的凭证加密信封")
	}
	gcm, keyID, err := credentialGCM(key)
	if err != nil {
		return "", err
	}
	if e.KeyID != keyID {
		return "", fmt.Errorf("凭证与本机 WorkBuddy 安装密钥不匹配")
	}
	nonce, nerr := base64.StdEncoding.Strict().DecodeString(e.Nonce)
	tag, terr := base64.StdEncoding.Strict().DecodeString(e.AuthTag)
	body, berr := base64.StdEncoding.Strict().DecodeString(e.Ciphertext)
	if nerr != nil || terr != nil || berr != nil || len(nonce) != gcm.NonceSize() || len(tag) != gcm.Overhead() {
		return "", fmt.Errorf("无效的凭证加密字段")
	}
	plain, err := gcm.Open(nil, nonce, append(body, tag...), credentialAAD(e.KeyID))
	if err != nil {
		return "", fmt.Errorf("凭证加密字段完整性校验失败")
	}
	defer clear(plain)
	return string(plain), nil
}

func sealCredentialField(s string, key []byte) (map[string]any, error) {
	gcm, keyID, err := credentialGCM(key)
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}
	body := gcm.Seal(nil, nonce, []byte(s), credentialAAD(keyID))
	split := len(body) - gcm.Overhead()
	e := credentialEnvelope{Suite: 1, KeyID: keyID, Nonce: base64.StdEncoding.EncodeToString(nonce), AuthTag: base64.StdEncoding.EncodeToString(body[split:]), Ciphertext: base64.StdEncoding.EncodeToString(body[:split])}
	raw, err := json.Marshal(e)
	return map[string]any{"$wbEncrypted": 1, "envelope": base64.StdEncoding.EncodeToString(raw)}, err
}

func decodeCredentialDocument(raw []byte) ([]byte, error) {
	return decodeCredentialDocumentUsing(raw, localCredentialKey)
}

func decodeCredentialDocumentUsing(raw []byte, keyReader func(bool) ([]byte, error)) ([]byte, error) {
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		return nil, err
	}
	var key []byte
	defer func() { clear(key) }()
	auth, _ := doc["auth"].(map[string]any)
	domain, _ := auth["domain"].(string)
	if domain == "" {
		domain, _ = doc["domain"].(string)
	}
	var visit func(any) (any, error)
	visit = func(v any) (any, error) {
		switch x := v.(type) {
		case map[string]any:
			if _, encrypted := x["$wbEncrypted"]; encrypted {
				if key == nil {
					var err error
					key, err = keyReader(globalDomain(domain))
					if err != nil {
						return nil, err
					}
				}
				return openCredentialField(x, key)
			}
			for k, v := range x {
				y, err := visit(v)
				if err != nil {
					return nil, err
				}
				x[k] = y
			}
		case []any:
			for i, v := range x {
				y, err := visit(v)
				if err != nil {
					return nil, err
				}
				x[i] = y
			}
		}
		return v, nil
	}
	if _, err := visit(doc); err != nil {
		return nil, err
	}
	return json.Marshal(doc)
}
