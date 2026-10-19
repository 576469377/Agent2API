package app

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/576469377/Agent2API/internal/adapter/workbuddy"
	"github.com/576469377/Agent2API/internal/llm"
)

// DefaultAccountsDir 是未配置 accounts_dir 时的兜底号池位置，
// 也是控制台「添加账号」凭证的默认落盘处。号池监视器（cmd 层 poolwatch）
// 监视同一位置，新凭证无需重启即可入池。
func DefaultAccountsDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ".workbuddy"
	}
	return filepath.Join(home, ".workbuddy")
}

// 登录会话的状态。
const (
	loginPending = "pending" // 已生成授权 URL，等用户在浏览器完成
	loginSuccess = "success"
	loginFailed  = "failed"
)

// loginSessionTTL 是已完成会话的保留时长：够前端轮询取到结果即可。
const loginSessionTTL = 5 * time.Minute

// loginSession 是一次进行中的设备码登录。
//
// 设备码登录必须异步：用户要在浏览器里完成授权（最长 5 分钟），
// 同步 HTTP 请求既会超时，也没法在等待期间把授权 URL 交给前端。
type loginSession struct {
	ID        string    `json:"id"`
	Status    string    `json:"status"`
	Platform  string    `json:"platform"` // 本次登录所属平台
	AuthURL   string    `json:"auth_url,omitempty"`
	OutPath   string    `json:"out_path,omitempty"`
	Account   string    `json:"account,omitempty"` // 成功后回填昵称/UID
	Error     string    `json:"error,omitempty"`
	StartedAt time.Time `json:"started_at"`
	doneAt    time.Time
}

// loginManager 管理进行中的登录会话。
//
// 多平台集成后一个管理器服务所有平台：会话带 Platform 标记，
// Start 按平台分派登录方式（当前仅 workbuddy 设备码；新平台在此追加分支）。
type loginManager struct {
	logf func(format string, args ...any)

	mu                sync.Mutex
	sessions          map[string]*loginSession
	credentialMu      sync.Mutex
	replaceCredential func(string, *workbuddy.Credential) (string, bool, error)
}

func newLoginManager(logf func(string, ...any)) *loginManager {
	return &loginManager{logf: logf, sessions: map[string]*loginSession{}}
}

// Start 发起一次登录，立刻返回会话（含授权 URL）。
// platform 决定登录方式；当前仅实现 workbuddy 的设备码授权。
func (m *loginManager) Start(platform, outPath string, baseURLs ...string) (*loginSnapshot, error) {
	if platform == "cursor" {
		return nil, &llm.Failure{Code: "cursor_desktop_login", Message: "请在本机 Cursor 桌面端登录个人 Free 账号，再刷新模型目录", ClientFixable: true}
	}
	if platform != "workbuddy" && platform != workbuddy.GlobalPlatform {
		return nil, &llm.Failure{Code: "api_key_required", Message: "该平台使用 API Key；OpenRouter 请设置 OPENROUTER_API_KEY 后重启网关", ClientFixable: true}
	}
	baseURL := workbuddy.BaseURLForPlatform(platform)
	if len(baseURLs) > 0 && baseURLs[0] != "" {
		baseURL = baseURLs[0]
	}
	s := &loginSession{
		ID:        newSessionID(),
		Status:    loginPending,
		Platform:  platform,
		OutPath:   outPath,
		StartedAt: time.Now(),
	}
	m.mu.Lock()
	m.sessions[s.ID] = s
	m.mu.Unlock()

	// 有了授权 URL 立刻记录下来，前端可以马上展示/打开。
	urlReady := make(chan string, 1)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 6*time.Minute)
		defer cancel()

		// Keep credentials out of the watched *.json files until account identity
		// is known. A different Google account must not overwrite an existing file.
		stagedPath := outPath + "." + s.ID + ".login.tmp"
		cred, err := workbuddy.Login(ctx, baseURL, workbuddy.LoginOptions{
			Region:     platform,
			BaseURL:    baseURL,
			OutputPath: stagedPath,
			OnAuthURL: func(u string) {
				select {
				case urlReady <- u:
				default:
				}
			},
		})
		finalPath := outPath
		if err == nil {
			finalPath, err = m.commitCredential(platform, outPath, stagedPath, cred)
		}

		m.mu.Lock()
		defer m.mu.Unlock()
		s.doneAt = time.Now()
		if err != nil {
			s.Status = loginFailed
			s.Error = err.Error()
			m.logf("登录失败: %v", err)
			return
		}
		s.Status = loginSuccess
		s.OutPath = finalPath
		if cred.Nickname != "" {
			s.Account = cred.Nickname
		} else {
			s.Account = cred.UID
		}
		m.logf("登录成功: %s", s.Account)
	}()

	// 等一小会儿拿授权 URL：拿不到也不算失败（前端轮询时会看到）。
	// 注意：登录 goroutine 持锁写 AuthURL，这里也持锁读——15s 超时只是
	// 决定「Start 要不要等」，不影响 URL 最终可见（Get 快照会带上）。
	select {
	case u := <-urlReady:
		m.mu.Lock()
		s.AuthURL = u
		m.mu.Unlock()
	case <-time.After(15 * time.Second):
		m.logf("等待授权 URL 超时（继续在后台完成登录，URL 可稍后从轮询获取）")
	}
	// 返回锁内快照（s 的字段还会被登录 goroutine 写）。
	m.mu.Lock()
	defer m.mu.Unlock()
	return &loginSnapshot{
		ID: s.ID, Status: s.Status, AuthURL: s.AuthURL, OutPath: s.OutPath,
		Account: s.Account, Error: s.Error, StartedAt: s.StartedAt,
	}, nil
}

// Commit the new session before removing its staging file. Failed commits
// retain that file for recovery instead of reporting a false login success.
func (m *loginManager) commitCredential(platform, outPath, stagedPath string, cred *workbuddy.Credential) (string, error) {
	m.credentialMu.Lock()
	defer m.credentialMu.Unlock()
	if cred == nil || cred.UID == "" || cred.AccessToken == "" {
		return "", fmt.Errorf("新登录态缺少账号或凭证，未替换原登录态")
	}
	if m.replaceCredential != nil {
		path, found, err := m.replaceCredential(platform, cred)
		if err != nil {
			return "", err
		}
		if found {
			if err := os.Remove(stagedPath); err != nil && !os.IsNotExist(err) {
				m.logf("新登录态已应用，临时凭证清理失败")
			}
			return path, nil
		}
	}
	target := outPath
	if duplicate := findDuplicateCredential(stagedPath, cred.UID); duplicate != "" {
		// Preserve native encrypted fields when the matching file has not yet
		// joined the pool. A failed load/update keeps the staged session intact.
		adp, err := workbuddy.New(workbuddy.Config{Platform: platform, CredentialPath: duplicate})
		if err != nil {
			return "", err
		}
		if err := adp.ReplaceCredential(cred); err != nil {
			return "", err
		}
		if err := os.Remove(stagedPath); err != nil && !os.IsNotExist(err) {
			m.logf("新登录态已保存，临时凭证清理失败")
		}
		return duplicate, nil
	} else if _, err := os.Stat(target); err == nil {
		return "", fmt.Errorf("目标凭证已属于其他账号，新登录态已保留，请使用其他账号名称")
	} else if !os.IsNotExist(err) {
		return "", err
	}
	if err := os.Rename(stagedPath, target); err != nil {
		return "", err
	}
	return target, nil
}

func (a *App) replaceLoginCredential(platform string, cred *workbuddy.Credential) (string, bool, error) {
	rt := a.hub.Platform(platform)
	if rt == nil {
		return "", false, fmt.Errorf("登录目标平台不存在")
	}
	for _, status := range rt.AccountStatuses() {
		adp, ok := status.Adapter.(*workbuddy.Adapter)
		if !ok || adp.AccountUID() != cred.UID {
			continue
		}
		if err := adp.ReplaceCredential(cred); err != nil {
			return "", true, err
		}
		if rt.Pool != nil {
			rt.Pool.AccountReauthenticated(status.Label)
		}
		return adp.AccountIdentity().CredPath, true, nil
	}
	return "", false, nil
}

// loginSnapshot 是会话状态的不可变快照。
// 直接返回 *loginSession 会与登录 goroutine 的字段写入构成数据竞争
// （撕裂读），所以查询时在锁内复制一份。
type loginSnapshot struct {
	ID        string    `json:"id"`
	Status    string    `json:"status"`
	AuthURL   string    `json:"auth_url,omitempty"`
	OutPath   string    `json:"out_path,omitempty"`
	Account   string    `json:"account,omitempty"`
	Error     string    `json:"error,omitempty"`
	StartedAt time.Time `json:"started_at"`
}

// Get 查询会话状态（锁内快照）。已完成且过期的会话会被清理；
// 超过 24h 从未被查询的会话也顺带清理（防无界增长）。
func (m *loginManager) Get(id string) (*loginSnapshot, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	s, ok := m.sessions[id]
	if !ok {
		return nil, &llm.Failure{Code: "login_not_found", Message: "登录会话不存在或已过期", ClientFixable: true}
	}
	now := time.Now()
	if !s.doneAt.IsZero() && now.Sub(s.doneAt) > loginSessionTTL {
		delete(m.sessions, id)
		return nil, &llm.Failure{Code: "login_not_found", Message: "登录会话已过期", ClientFixable: true}
	}
	// 顺带清理：启动超过 24h 的会话无论是否完成都回收
	//（正常流程几秒到几分钟就结束，24h 还在说明客户端早丢了）。
	for k, other := range m.sessions {
		if now.Sub(other.StartedAt) > 24*time.Hour {
			delete(m.sessions, k)
		}
	}
	return &loginSnapshot{
		ID: s.ID, Status: s.Status, AuthURL: s.AuthURL, OutPath: s.OutPath,
		Account: s.Account, Error: s.Error, StartedAt: s.StartedAt,
	}, nil
}

func newSessionID() string {
	buf := make([]byte, 8)
	if _, err := rand.Read(buf); err != nil {
		return "sess00000000000000"
	}
	return hex.EncodeToString(buf)
}

// findDuplicateCredential 在 outPath 所在目录里查找**除自身外**是否存在
// 同一 uid 的凭证文件，返回其路径；没有则返回空串。
//
// 只看 uid 不看昵称：昵称可重复，uid 才是稳定标识。
func findDuplicateCredential(outPath, uid string) string {
	if uid == "" {
		return ""
	}
	dir := filepath.Dir(outPath)
	entries, err := os.ReadDir(dir)
	if err != nil {
		return ""
	}
	for _, e := range entries {
		if e.IsDir() || !strings.EqualFold(filepath.Ext(e.Name()), ".json") {
			continue
		}
		p := filepath.Join(dir, e.Name())
		if p == outPath {
			continue
		}
		raw, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		var doc struct {
			Auth struct {
				AccessToken json.RawMessage `json:"accessToken"`
			} `json:"auth"`
			Account struct {
				UID string `json:"uid"`
			} `json:"account"`
		}
		if err := json.Unmarshal(raw, &doc); err != nil {
			continue
		}
		// 只把「有效且有 token」的当作重复源，避免被垃圾文件挡住。
		var token string
		valid := json.Unmarshal(doc.Auth.AccessToken, &token) == nil && token != ""
		if !valid {
			var encrypted struct {
				Marker   int    `json:"$wbEncrypted"`
				Envelope string `json:"envelope"`
			}
			valid = json.Unmarshal(doc.Auth.AccessToken, &encrypted) == nil && encrypted.Marker == 1 && encrypted.Envelope != ""
		}
		if doc.Account.UID == uid && valid {
			return p
		}
	}
	return ""
}
