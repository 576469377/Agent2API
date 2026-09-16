package app

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"sync"
	"time"

	"github.com/576469377/Agent2API/internal/adapter/workbuddy"
	"github.com/576469377/Agent2API/internal/llm"
)

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
	AuthURL   string    `json:"auth_url,omitempty"`
	OutPath   string    `json:"out_path,omitempty"`
	Account   string    `json:"account,omitempty"` // 成功后回填昵称/UID
	Error     string    `json:"error,omitempty"`
	StartedAt time.Time `json:"started_at"`
	doneAt    time.Time
}

// loginManager 管理进行中的登录会话。
type loginManager struct {
	baseURL string
	logf    func(format string, args ...any)

	mu       sync.Mutex
	sessions map[string]*loginSession
}

func newLoginManager(baseURL string, logf func(string, ...any)) *loginManager {
	return &loginManager{baseURL: baseURL, logf: logf, sessions: map[string]*loginSession{}}
}

// Start 发起一次设备码登录，立刻返回会话（含授权 URL）。
// outPath 为空时用 workbuddy 的默认凭证路径。
func (m *loginManager) Start(outPath string) (*loginSession, error) {
	s := &loginSession{
		ID:        newSessionID(),
		Status:    loginPending,
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

		cred, err := workbuddy.Login(ctx, m.baseURL, workbuddy.LoginOptions{
			BaseURL:    m.baseURL,
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
