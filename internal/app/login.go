package app

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
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

	mu       sync.Mutex
	sessions map[string]*loginSession
}

func newLoginManager(logf func(string, ...any)) *loginManager {
	return &loginManager{logf: logf, sessions: map[string]*loginSession{}}
}

// Start 发起一次登录，立刻返回会话（含授权 URL）。
// platform 决定登录方式；当前仅实现 workbuddy 的设备码授权。
func (m *loginManager) Start(platform, outPath string) (*loginSession, error) {
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

		cred, err := workbuddy.Login(ctx, workbuddy.DefaultBaseURL, workbuddy.LoginOptions{
			BaseURL:    workbuddy.DefaultBaseURL,
			OutputPath: outPath,
			OnAuthURL: func(u string) {
				select {
				case urlReady <- u:
				default:
				}
			},
		})

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
		if cred.Nickname != "" {
			s.Account = cred.Nickname
		} else {
			s.Account = cred.UID
		}
		// 去重：同一账号（uid）若在同目录已有凭证，删掉刚写的这份。
		//
		// 为什么要删而不是留着：同一账号两份凭证会形成两条独立刷新链，
		// 若上游 refresh token 是一次轮换型，它们会互相顶掉会话（表现为
		// 随机 401）。实测用户在控制台重复点「添加账号」就会写出多份。
		if dup := findDuplicateCredential(outPath, cred.UID); dup != "" {
			if rmErr := os.Remove(outPath); rmErr == nil {
				s.OutPath = dup // 让前端提示「已存在，复用原有凭证」
				m.logf("账号 %s 已存在（%s），本次登录凭证已丢弃避免重复入池", s.Account, filepath.Base(dup))
			}
		}
		m.logf("登录成功: %s", s.Account)
	}()

	// 等一小会儿拿授权 URL：拿不到也不算失败（用户可能已经在别处完成），
	// 前端可以继续轮询状态。
	select {
	case u := <-urlReady:
		m.mu.Lock()
		s.AuthURL = u
		m.mu.Unlock()
	case <-time.After(15 * time.Second):
		m.logf("等待授权 URL 超时（继续在后台完成登录）")
	}
	return s, nil
}

// Get 查询会话状态。已完成且过期的会话会被清理。
func (m *loginManager) Get(id string) (*loginSession, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	s, ok := m.sessions[id]
	if !ok {
		return nil, &llm.Failure{Code: "login_not_found", Message: "登录会话不存在或已过期", ClientFixable: true}
	}
	if !s.doneAt.IsZero() && time.Since(s.doneAt) > loginSessionTTL {
		delete(m.sessions, id)
		return nil, &llm.Failure{Code: "login_not_found", Message: "登录会话已过期", ClientFixable: true}
	}
	return s, nil
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
				AccessToken string `json:"accessToken"`
			} `json:"auth"`
			Account struct {
				UID string `json:"uid"`
			} `json:"account"`
		}
		if err := json.Unmarshal(raw, &doc); err != nil {
			continue
		}
		// 只把「有效且有 token」的当作重复源，避免被垃圾文件挡住。
		if doc.Account.UID == uid && doc.Auth.AccessToken != "" {
			return p
		}
	}
	return ""
}
