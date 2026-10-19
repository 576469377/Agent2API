package workbuddy

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"time"

	"github.com/576469377/Agent2API/internal/llm"
)

// DefaultCredentialPaths 返回本机桌面客户端已登录凭证的默认位置。
//
// 导出给上层：号池需要逐个枚举候选，而不是让适配器挑第一个文件了事——
// 第一个文件损坏时不该放弃后面的候选。
func DefaultCredentialPaths() []string {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil
	}
	switch runtime.GOOS {
	case "darwin":
		return []string{
			filepath.Join(home, "Library/Application Support/CodeBuddyExtension/Data/Public/auth/workbuddy-desktop.info"),
			filepath.Join(home, ".workbuddy/session.json"),
			filepath.Join(home, ".codebuddy-session.json"),
		}
	case "windows":
		local := os.Getenv("LOCALAPPDATA")
		if local == "" {
			local = filepath.Join(home, "AppData/Local")
		}
		roaming := os.Getenv("APPDATA")
		if roaming == "" {
			roaming = filepath.Join(home, "AppData/Roaming")
		}
		return []string{
			filepath.Join(roaming, "CodeBuddyExtension/Data/Public/auth/workbuddy-desktop.info"),
			filepath.Join(local, "CodeBuddyExtension/Data/Public/auth/workbuddy-desktop.info"),
			filepath.Join(home, ".workbuddy/session.json"),
		}
	default:
		return []string{
			filepath.Join(home, ".config/CodeBuddyExtension/Data/Public/auth/workbuddy-desktop.info"),
			filepath.Join(home, ".workbuddy/session.json"),
			filepath.Join(home, ".codebuddy-session.json"),
		}
	}
}

// Credential 是上游账号凭证。
type Credential struct {
	AccessToken  string `json:"accessToken"`
	RefreshToken string `json:"refreshToken"`
	TokenType    string `json:"tokenType"`
	ExpiresAt    int64  `json:"expiresAt"` // 毫秒
	RefreshExpAt int64  `json:"refreshExpiresAt"`
	Domain       string `json:"domain"`

	UID            string `json:"-"`
	Nickname       string `json:"-"`
	EnterpriseID   string `json:"-"`
	DepartmentInfo string `json:"-"`
	MachineID      string `json:"-"`
}

// credentialFile 是凭证文件的磁盘形态。
type credentialFile struct {
	Auth struct {
		AccessToken  string `json:"accessToken"`
		RefreshToken string `json:"refreshToken"`
		TokenType    string `json:"tokenType"`
		ExpiresAt    int64  `json:"expiresAt"`
		RefreshExpAt int64  `json:"refreshExpiresAt"`
		Domain       string `json:"domain"`
	} `json:"auth"`
	Account struct {
		UID            string `json:"uid"`
		Nickname       string `json:"nickname"`
		EnterpriseID   string `json:"enterpriseId"`
		DepartmentInfo string `json:"departmentInfo"`
	} `json:"account"`
	MachineID string `json:"machineId"`
}

// loadCredentialFile 从磁盘读取凭证，按候选路径依次尝试。
func loadCredentialFile(path string) (*Credential, error) {
	if path == "" {
		for _, p := range DefaultCredentialPaths() {
			if _, err := os.Stat(p); err == nil {
				path = p
				break
			}
		}
	}
	if path == "" {
		return nil, &llm.Failure{Code: "no_credential", Message: "未找到 WorkBuddy 凭证文件，请先运行 agent2api login", ClientFixable: true}
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, &llm.Failure{Code: "credential_read_failed", Message: "读取凭证文件失败: " + err.Error(), Cause: err}
	}
	decoded, err := decodeCredentialDocument(raw)
	if err != nil {
		return nil, &llm.Failure{Code: "credential_parse_failed", Message: "解析凭证失败: " + err.Error(), Cause: err}
	}
	defer clear(decoded)
	raw = decoded
	var cf credentialFile
	if err := json.Unmarshal(raw, &cf); err != nil || cf.Auth.AccessToken == "" {
		// 兼容扁平格式（部分版本直接把 token 放在顶层）
		var flat struct {
			AccessToken  string `json:"accessToken"`
			RefreshToken string `json:"refreshToken"`
			Domain       string `json:"domain"`
			UID          string `json:"uid"`
			TokenType    string `json:"tokenType"`
			ExpiresAt    int64  `json:"expiresAt"`
			RefreshExpAt int64  `json:"refreshExpiresAt"`
		}
		if err2 := json.Unmarshal(raw, &flat); err2 == nil && flat.AccessToken != "" {
			cf.Auth.AccessToken = flat.AccessToken
			cf.Auth.RefreshToken = flat.RefreshToken
			cf.Auth.Domain = flat.Domain
			cf.Account.UID = flat.UID
			cf.Auth.TokenType = flat.TokenType
			cf.Auth.ExpiresAt = flat.ExpiresAt
			cf.Auth.RefreshExpAt = flat.RefreshExpAt
		} else {
			return nil, &llm.Failure{Code: "credential_parse_failed", Message: "解析凭证文件失败", Cause: err}
		}
	}
	if cf.Auth.AccessToken == "" {
		return nil, &llm.Failure{Code: "credential_empty", Message: "凭证文件中没有 accessToken，请重新登录", ClientFixable: true}
	}

	c := &Credential{
		AccessToken:    cf.Auth.AccessToken,
		RefreshToken:   cf.Auth.RefreshToken,
		TokenType:      cf.Auth.TokenType,
		ExpiresAt:      cf.Auth.ExpiresAt,
		RefreshExpAt:   cf.Auth.RefreshExpAt,
		Domain:         cf.Auth.Domain,
		UID:            cf.Account.UID,
		Nickname:       cf.Account.Nickname,
		EnterpriseID:   cf.Account.EnterpriseID,
		DepartmentInfo: cf.Account.DepartmentInfo,
		MachineID:      cf.MachineID,
	}
	if c.MachineID == "" {
		c.MachineID = stableMachineID()
	}
	if c.TokenType == "" {
		c.TokenType = "Bearer"
	}
	if c.Domain == "" {
		c.Domain = "www.workbuddy.cn"
	}
	return c, nil
}

// stableMachineID 生成稳定机器 ID（UUID v5，基于 hostname-username）。
func stableMachineID() string {
	host, _ := os.Hostname()
	user := os.Getenv("USER")
	if user == "" {
		user = os.Getenv("USERNAME")
	}
	ns := [16]byte{0x6b, 0xa7, 0xb8, 0x10, 0x9d, 0xad, 0x11, 0xd1, 0x80, 0xb4, 0x00, 0xc0, 0x4f, 0xd4, 0x30, 0xc8}
	h := sha1.New()
	h.Write(ns[:])
	h.Write([]byte(host + "-" + user))
	sum := h.Sum(nil)
	b := make([]byte, 16)
	copy(b, sum[:16])
	b[6] = (b[6] & 0x0f) | 0x50 // version 5
	b[8] = (b[8] & 0x3f) | 0x80 // variant RFC 4122
	return formatUUID(b)
}

func formatUUID(b []byte) string {
	hexs := hex.EncodeToString(b)
	return hexs[0:8] + "-" + hexs[8:12] + "-" + hexs[12:16] + "-" + hexs[16:20] + "-" + hexs[20:32]
}

// Auth 管理凭证的生命周期：读取、刷新、登录。
type Auth struct {
	mu   sync.Mutex
	cred *Credential
	cli  *httpClient
	// CredentialPath 非空时，刷新成功后回写磁盘。
	credentialPath string
	// refreshing 表示一次刷新正在进行（两段式刷新的中间态）。
	// 刷新的网络 I/O 在锁外执行，否则上游变慢时（最长 20s）
	// 同账号的所有并发请求都会阻塞在 BuildHeaders 的锁上。
	refreshing bool
}

func newAuth(cli *httpClient, cred *Credential, path string) *Auth {
	return &Auth{cli: cli, cred: cred, credentialPath: path}
}

// CredentialPath 返回凭证文件路径（供控制台展示与「重新登录写回哪个文件」）。
func (a *Auth) CredentialPath() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.credentialPath
}

// Credential 返回当前凭证快照。
func (a *Auth) Credential() Credential {
	a.mu.Lock()
	defer a.mu.Unlock()
	return *a.cred
}

func (a *Auth) replaceCredential(cred *Credential) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if cred == nil || cred.UID == "" || cred.UID != a.cred.UID || cred.AccessToken == "" || globalDomain(cred.Domain) != globalDomain(a.cred.Domain) {
		return &llm.Failure{Code: "credential_identity_mismatch", Message: "新登录态与当前账号或区域不匹配", ClientFixable: true}
	}
	previous := a.cred
	next := *cred
	if previous.MachineID != "" {
		next.MachineID = previous.MachineID
	}
	a.cred = &next
	if err := a.persistLocked(); err != nil {
		a.cred = previous
		return &llm.Failure{Code: "credential_write_failed", Message: "新登录态保存失败，原登录态已保留", Cause: err}
	}
	return nil
}

// BuildHeaders 在锁内构造请求头，避免与刷新竞争导致读到半更新的凭证。
func (a *Auth) BuildHeaders(mode headerMode) http.Header {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.cli.authHeaders(a.cred, mode)
}

// expiresSoon 判断 token 是否即将过期（提前 60 秒刷新）。
func (c *Credential) expiresSoon() bool {
	if c.ExpiresAt == 0 {
		return false // 没有过期时间就当作有效
	}
	return c.ExpiresAt < time.Now().UnixMilli()+60_000
}

// EnsureValid 保证凭证有效，必要时刷新一次。
// EnsureValid 保证凭证有效，必要时刷新一次。
//
// 两段式：锁内只做「检查过期 → 抢占刷新权」，网络 I/O 与磁盘写回在锁外。
// 否则刷新最长占锁 20 秒（上游变慢时），同账号的所有并发请求都会
// 阻塞在 BuildHeaders 上。已刷新失败的并发请求拿到的错误与直接刷新一致。
func (a *Auth) EnsureValid() error {
	a.mu.Lock()
	if !a.cred.expiresSoon() {
		a.mu.Unlock()
		return nil
	}
	if a.refreshing {
		// 别的 goroutine 正在刷新：不排队等（会阻塞请求路径），
		// 用当前凭证继续——旧 token 大概率仍有效；真失效了下次再刷。
		a.mu.Unlock()
		return nil
	}
	a.refreshing = true
	a.mu.Unlock()
	defer func() {
		a.mu.Lock()
		a.refreshing = false
		a.mu.Unlock()
	}()

	return a.Refresh()
}

// Refresh 强制刷新 token。
func (a *Auth) Refresh() error {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.refreshLocked()
}

func (a *Auth) refreshLocked() error {
	if a.cred.RefreshToken == "" {
		return &llm.Failure{Code: "no_refresh_token", Message: "凭证缺少 refreshToken，无法刷新，请重新登录", ClientFixable: true}
	}
	headers := a.cli.authHeaders(a.cred, headerModeRefresh)
	headers.Set("X-Auth-Refresh-Source", "plugin")

	var resp struct {
		Code int             `json:"code"`
		Msg  string          `json:"msg"`
		Data json.RawMessage `json:"data"`
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if err := a.cli.doJSON(ctx, http.MethodPost, tokenRefreshPath, nil, headers, &resp); err != nil {
		return err
	}
	if resp.Code != 0 {
		return &llm.Failure{Code: fmt.Sprintf("upstream_%d", resp.Code), Message: "刷新 token 失败: " + resp.Msg}
	}
	tok, err := unwrapToken(resp.Data)
	if err != nil {
		return err
	}
	if tok.AccessToken == "" {
		return &llm.Failure{Code: "refresh_empty", Message: "刷新响应中没有 accessToken"}
	}
	a.cred.AccessToken = tok.AccessToken
	if tok.RefreshToken != "" {
		a.cred.RefreshToken = tok.RefreshToken
	}
	if tok.ExpiresAt > 0 {
		a.cred.ExpiresAt = tok.ExpiresAt
	} else if tok.ExpiresIn > 0 {
		a.cred.ExpiresAt = time.Now().UnixMilli() + tok.ExpiresIn*1000
	}
	if tok.Domain != "" {
		a.cred.Domain = tok.Domain
	}
	if tok.TokenType != "" {
		a.cred.TokenType = tok.TokenType
	}
	if tok.RefreshExpiresAt > 0 {
		a.cred.RefreshExpAt = tok.RefreshExpiresAt
	} else if tok.RefreshExpiresIn > 0 {
		a.cred.RefreshExpAt = time.Now().UnixMilli() + tok.RefreshExpiresIn*1000
	}
	if err := a.persistLocked(); err != nil {
		return &llm.Failure{Code: "credential_write_failed", Message: "刷新成功但凭证保存失败: " + err.Error(), Cause: err}
	}
	return nil
}

// tokenPayload 是刷新/登录返回的 token 结构。
type tokenPayload struct {
	AccessToken      string `json:"accessToken"`
	RefreshToken     string `json:"refreshToken"`
	TokenType        string `json:"tokenType"`
	ExpiresIn        int64  `json:"expiresIn"`
	ExpiresAt        int64  `json:"expiresAt"`
	Domain           string `json:"domain"`
	RefreshExpiresAt int64  `json:"refreshExpiresAt"`
	RefreshExpiresIn int64  `json:"refreshExpiresIn"`
}

func unwrapToken(raw json.RawMessage) (tokenPayload, error) {
	var tok tokenPayload
	if len(raw) == 0 {
		return tok, nil
	}
	// 上游是双层 data 信封：{data:{data:{...}}} 或 {data:{...}}
	var outer map[string]json.RawMessage
	if err := json.Unmarshal(raw, &outer); err == nil {
		if inner, ok := outer["data"]; ok {
			var innerMap map[string]json.RawMessage
			if err := json.Unmarshal(inner, &innerMap); err == nil {
				if _, nested := innerMap["data"]; nested {
					raw = innerMap["data"]
				} else {
					raw = inner
				}
			} else {
				raw = inner
			}
		}
	}
	if err := json.Unmarshal(raw, &tok); err != nil {
		return tok, &llm.Failure{Code: "token_parse_failed", Message: "解析 token 响应失败: " + err.Error(), Cause: err}
	}
	return tok, nil
}

// persistLocked 把刷新后的凭证回写磁盘（尽力而为，失败不影响内存态）。
func (a *Auth) persistLocked() error {
	return a.persistLockedUsing(localCredentialKey)
}

func (a *Auth) persistLockedUsing(keyReader func(bool) ([]byte, error)) error {
	if a.credentialPath == "" {
		return nil
	}
	raw, err := os.ReadFile(a.credentialPath)
	if err != nil {
		return err
	}
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		return err
	}
	authObj, _ := doc["auth"].(map[string]any)
	flat := authObj == nil && doc["accessToken"] != nil
	if flat {
		authObj = doc
	}
	if authObj == nil {
		authObj = map[string]any{}
	}
	var key []byte
	defer func() { clear(key) }()
	setString := func(target map[string]any, field, value string) error {
		if wrapper, ok := target[field].(map[string]any); ok && wrapper["$wbEncrypted"] != nil {
			if wrapper["scheme"] != nil {
				return fmt.Errorf("不支持的凭证加密格式")
			}
			if key == nil {
				key, err = keyReader(globalDomain(a.cred.Domain))
				if err != nil {
					return err
				}
			}
			sealed, err := sealCredentialField(value, key)
			if err != nil {
				return err
			}
			target[field] = sealed
		} else {
			target[field] = value
		}
		return nil
	}
	for field, value := range map[string]string{"accessToken": a.cred.AccessToken, "refreshToken": a.cred.RefreshToken} {
		if err := setString(authObj, field, value); err != nil {
			return err
		}
	}
	authObj["expiresAt"] = a.cred.ExpiresAt
	authObj["domain"] = a.cred.Domain
	authObj["tokenType"] = a.cred.TokenType
	authObj["refreshExpiresAt"] = a.cred.RefreshExpAt
	authObj["lastRefreshTime"] = time.Now().UnixMilli()
	if !flat {
		doc["auth"] = authObj
		account, _ := doc["account"].(map[string]any)
		if account == nil {
			account = map[string]any{}
		}
		for field, value := range map[string]string{"uid": a.cred.UID, "nickname": a.cred.Nickname, "enterpriseId": a.cred.EnterpriseID, "departmentInfo": a.cred.DepartmentInfo} {
			if err := setString(account, field, value); err != nil {
				return err
			}
		}
		doc["account"] = account
	}
	doc["machineId"] = a.cred.MachineID
	out, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return err
	}
	tmp := a.credentialPath + ".tmp"
	if err := os.WriteFile(tmp, out, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, a.credentialPath)
}
