package openrouter

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

func DefaultCredentialPath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".agent2api", "openrouter.session.json")
}

type savedCredential struct {
	Version int    `json:"version"`
	APIKey  string `json:"api_key"`
}

func validKey(key string) bool {
	if key == "" || len(key) > 4096 {
		return false
	}
	for _, c := range key {
		if c < 33 || c > 126 {
			return false
		}
	}
	return true
}

// 显式保存的连接优先于启动凭据；文件损坏时拒绝回退，但保留修复入口。
func (a *Adapter) loadSavedKey() {
	if a.cfg.CredentialPath == "" {
		return
	}
	f, err := os.Open(a.cfg.CredentialPath)
	if errors.Is(err, os.ErrNotExist) {
		return
	}
	a.storedPresent = true
	if err != nil {
		return
	}
	defer f.Close()
	raw, err := io.ReadAll(io.LimitReader(f, 16*1024+1))
	defer clear(raw)
	var c savedCredential
	if err == nil && len(raw) <= 16*1024 && json.Unmarshal(raw, &c) == nil && c.Version == 1 && validKey(c.APIKey) {
		a.storedKey = c.APIKey
	}
}

// VerifyConnection 只检测认证，不生成模型用量，也不返回密钥；客户端拒绝重定向。
func (a *Adapter) VerifyConnection(ctx context.Context) error {
	return a.verifyKey(ctx, a.key())
}

func (a *Adapter) verifyKey(ctx context.Context, key string) error {
	if !validKey(key) {
		return fmt.Errorf("请填写有效的 OpenRouter API Key")
	}
	ctx, cancel := context.WithTimeout(ctx, 25*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, a.cfg.BaseURL+"/key", nil)
	if err != nil {
		return fmt.Errorf("无法创建 OpenRouter 密钥检测请求")
	}
	req.Header.Set("Authorization", "Bearer "+key)
	resp, err := a.client.Do(req)
	if err != nil {
		return fmt.Errorf("无法连接 OpenRouter 检测密钥，请检查网络后重试")
	}
	defer resp.Body.Close()
	if resp.StatusCode == 401 || resp.StatusCode == 403 {
		return fmt.Errorf("OpenRouter 拒绝此 API Key，请检查密钥是否有效")
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("OpenRouter 密钥检测暂不可用（HTTP %d）", resp.StatusCode)
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 64*1024+1))
	defer clear(raw)
	var result struct {
		Data *struct {
			IsFreeTier      *bool `json:"is_free_tier"`
			IsManagementKey bool  `json:"is_management_key"`
		} `json:"data"`
	}
	if err != nil || len(raw) > 64*1024 || json.Unmarshal(raw, &result) != nil || result.Data == nil || result.Data.IsFreeTier == nil {
		return fmt.Errorf("OpenRouter 返回了无法确认的密钥信息，请稍后重试")
	}
	if result.Data.IsManagementKey {
		return fmt.Errorf("请使用模型调用 API Key，管理密钥不能用于此连接")
	}
	return nil
}

// SaveAPIKey 先验证，再原子保存；任何失败都保留之前的连接。
func (a *Adapter) SaveAPIKey(ctx context.Context, key string) error {
	key = strings.TrimSpace(key)
	if a.cfg.CredentialPath == "" {
		return fmt.Errorf("此连接未配置密钥保存路径")
	}
	a.fetchMu.Lock()
	defer a.fetchMu.Unlock()
	if err := a.verifyKey(ctx, key); err != nil {
		return err
	}
	raw, err := json.Marshal(savedCredential{Version: 1, APIKey: key})
	if err != nil {
		return fmt.Errorf("无法保存 OpenRouter 密钥")
	}
	defer clear(raw)
	dir := filepath.Dir(a.cfg.CredentialPath)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("无法创建 OpenRouter 凭证目录")
	}
	f, err := os.CreateTemp(dir, ".openrouter-*.tmp")
	if err != nil {
		return fmt.Errorf("无法创建 OpenRouter 凭证文件")
	}
	defer os.Remove(f.Name())
	if err = f.Chmod(0o600); err == nil {
		_, err = f.Write(raw)
	}
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err != nil || closeErr != nil {
		return fmt.Errorf("写入 OpenRouter 凭证失败，原连接已保留")
	}
	if err = os.Rename(f.Name(), a.cfg.CredentialPath); err != nil {
		return fmt.Errorf("更新 OpenRouter 凭证失败，原连接已保留")
	}
	a.mu.Lock()
	a.storedKey, a.storedPresent, a.expires = key, true, time.Time{}
	a.mu.Unlock()
	return nil
}
