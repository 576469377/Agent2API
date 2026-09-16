// Package workbuddy 实现 WorkBuddy / CodeBuddy 平台的上游适配。
//
// 上游要点（2026-09-15 实测）：
//   - Base URL: https://copilot.tencent.com
//   - 聊天：POST /v2/chat/completions，请求体本身就是 OpenAI Chat Completions 格式
//   - 模型：GET /v3/config（需用 CodeBuddyIDE UA，否则 12403）
//   - 上游不支持非流式（code 11101），非流式响应由代理侧聚合
//   - 流式只有 data: 行，终止帧 data: [DONE]
//   - 思考过程在 delta.reasoning_content
package workbuddy

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sync/atomic"
	"time"

	"github.com/576469377/Agent2API/internal/adapter"
	"github.com/576469377/Agent2API/internal/llm"
)

// Config 是 WorkBuddy 适配器配置。
type Config struct {
	BaseURL string

	// CredentialPath 指定凭证文件；为空则按平台默认路径依次探测。
	CredentialPath string

	// Sanitize 开启内容脱敏。接入 Claude Code / Codex 类客户端时必须开启，
	// 否则上游审核会拦截大量请求。
	Sanitize bool

	RequestTimeout     time.Duration // 非流式请求的 ctx 超时
	StreamIdleTimeout  time.Duration // 流式空闲超时（看门狗）
	StreamTotalTimeout time.Duration // 流式总时长上限
	ModelCacheTTL      time.Duration

	// Logger 用于输出内部日志；nil 时静默。
	Logger func(format string, args ...any)
}

// DefaultConfig 返回合理默认值。
func DefaultConfig() Config {
	return Config{
		BaseURL:            DefaultBaseURL,
		Sanitize:           true,
		RequestTimeout:     60 * time.Second,
		StreamIdleTimeout:  120 * time.Second,
		StreamTotalTimeout: 30 * time.Minute,
		ModelCacheTTL:      30 * time.Minute,
	}
}

// Adapter 是 WorkBuddy 平台适配器。
type Adapter struct {
	cli    *httpClient
	auth   *Auth
	models *modelCache
	cfg    Config
	// sanitize 是运行期可变的脱敏开关，控制台可以在不重启的情况下切换。
	sanitize atomic.Bool
}

// New 构造适配器。credentialPath 为空时自动探测本机已登录凭证。
func New(cfg Config) (*Adapter, error) {
	if cfg.BaseURL == "" {
		cfg.BaseURL = DefaultBaseURL
	}
	if cfg.RequestTimeout <= 0 {
		cfg.RequestTimeout = 60 * time.Second
	}
	if cfg.StreamIdleTimeout <= 0 {
		cfg.StreamIdleTimeout = 120 * time.Second
	}
	if cfg.StreamTotalTimeout <= 0 {
		cfg.StreamTotalTimeout = 30 * time.Minute
	}
	if cfg.ModelCacheTTL <= 0 {
		cfg.ModelCacheTTL = 30 * time.Minute
	}

	cred, err := loadCredentialFile(cfg.CredentialPath)
	if err != nil {
		return nil, err
	}
	credPath := cfg.CredentialPath
	if credPath == "" {
		for _, p := range DefaultCredentialPaths() {
			if _, statErr := os.Stat(p); statErr == nil {
				credPath = p
				break
			}
		}
	}

	cli := newHTTPClient(cfg.BaseURL, 0)
	auth := newAuth(cli, cred, credPath)
	a := &Adapter{
		cli:    cli,
		auth:   auth,
		models: newModelCache(cfg.ModelCacheTTL),
		cfg:    cfg,
	}
	a.sanitize.Store(cfg.Sanitize)
	return a, nil
}

// Name 实现 adapter.Adapter。
func (a *Adapter) Name() string { return "workbuddy" }

// CredentialInfo 返回当前账号摘要，用于 /health 与日志。
func (a *Adapter) CredentialInfo() string {
	c := a.auth.Credential()
	if c.Nickname != "" {
		return c.Nickname
	}
	if c.UID != "" {
		return c.UID
	}
	return "unknown"
}

// AccountUID 返回账号的稳定唯一标识（凭证文件里的 account.uid）。
// 号池用它做「同一账号不得重复入池」的判定——昵称可重复，uid 不会。
func (a *Adapter) AccountUID() string {
	return a.auth.Credential().UID
}

// AccountIdentity 是账号的身份与凭证健康度（供控制台展示登录状态）。
type AccountIdentity struct {
	UID      string `json:"uid,omitempty"`
	Nickname string `json:"nickname,omitempty"`
	CredPath string `json:"credential_path,omitempty"`
	// ExpiresAt 是 access token 到期时刻（Unix 毫秒）；0 表示未知。
	ExpiresAt int64 `json:"expires_at,omitempty"`
	// RefreshExpAt 是 refresh token 到期时刻；它过期就只能重新登录。
	RefreshExpAt int64 `json:"refresh_expires_at,omitempty"`
	// NeedsRefresh 表示 access token 已过期或即将过期（网关会自行刷新）。
	NeedsRefresh bool `json:"needs_refresh"`
	// RefreshExpired 表示 refresh token 也已过期——**只能重新登录**。
	RefreshExpired bool `json:"refresh_expired"`
}

// AccountIdentity 返回账号身份与凭证健康度。
func (a *Adapter) AccountIdentity() AccountIdentity {
	c := a.auth.Credential()
	id := AccountIdentity{
		UID: c.UID, Nickname: c.Nickname,
		CredPath:  a.auth.CredentialPath(),
		ExpiresAt: c.ExpiresAt, RefreshExpAt: c.RefreshExpAt,
	}
	now := time.Now().UnixMilli()
	id.NeedsRefresh = c.ExpiresAt > 0 && c.ExpiresAt < now+60_000
	// refresh token 过期是终态：网关自己救不回来，必须重新登录。
	id.RefreshExpired = c.RefreshExpAt > 0 && c.RefreshExpAt < now
	return id
}

// retryAttemptLimit 是 dial 的最大尝试次数（含首次）。
const retryAttemptLimit = 3

// retryMaxBackoff 是退避上限；下限为 500ms，按 2 倍递增。
const retryMaxBackoff = 4 * time.Second

// Stream 实现 adapter.Adapter。
//
// 上游不支持非流式，因此这里始终以流式请求；调用方需要单条完整响应时
// 在 app 层聚合即可。
//
// 重试语义：dial 失败意味着连接尚未建立、请求体未被消费，重拨是安全的；
// 一旦拿到流（哪怕第一帧还没到），失败就走流内错误通道，绝不在这一层重发，
// 否则会把已生成的半截内容变成两份。可重试的只有传输错误与 5xx/429；
// 4xx 语义拒绝（参数错、鉴权错）重试没有意义，直接失败。
func (a *Adapter) Stream(ctx context.Context, req llm.RequestMessages) (llm.ResponseStream, error) {
	if err := a.auth.EnsureValid(); err != nil {
		a.logf("凭证刷新失败: %v", err)
		// 刷新失败不阻断请求：旧 token 可能仍有效。
	}

	body := buildUpstreamRequest(req, a.sanitize.Load())

	backoff := 500 * time.Millisecond
	var lastErr error
	var refreshedOnAttempt bool
	for attempt := 1; attempt <= retryAttemptLimit; attempt++ {
		if attempt > 1 {
			a.logf("重试上游连接（第 %d/%d 次）: %s", attempt, retryAttemptLimit, lastErr)
		}
		s, err := a.dial(ctx, body)
		if err == nil {
			return s, nil
		}
		lastErr = err

		// 客户端已放弃：不再重试，原样返回。
		if ctx.Err() != nil {
			return nil, err
		}

		// 401 视为凭证过期：刷新后立刻重试。每次 attempt 只允许刷新一次，
		// 否则「chat 恒 401 而 refresh 恒 200」会因 attempt--/attempt++ 抵消
		// 变成无界循环（实测可 3 秒打近 4 万次上游）。
		if isUnauthorized(err) {
			if refreshedOnAttempt {
				a.logf("刷新后仍 401，放弃: %s", err)
				return nil, err
			}
			refreshedOnAttempt = true
			a.logf("收到 401，尝试刷新 token 后重试")
			if refreshErr := a.auth.Refresh(); refreshErr != nil {
				a.logf("刷新 token 失败: %v", refreshErr)
				return nil, err
			}
			attempt-- // 刷新本身不消耗退避节奏，但仍受 refreshedOnAttempt 闸门保护
			continue
		}

		// 不可重试的错误（语义拒绝等）直接失败。
		if !shouldRetryDial(err) {
			return nil, err
		}
		// 最后一次失败无需等待。
		if attempt == retryAttemptLimit {
			break
		}

		// 指数退避；上游给了 Retry-After 时取两者较大值。
		// Retry-After 最终受 retryMaxBackoff 钳制（4s）：这是有意的安全阀——
		// 上游（或中间的攻击者）给一个 Retry-After: 86400 时不该真的把
		// 客户端请求挂起一整天，超出部分宁可放弃重试。持续限流的场景下
		// 重试名额会更快耗尽并失败返回，那是可接受的降级。
		wait := backoff
		if ra := retryAfterOf(err); ra > wait {
			wait = ra
		}
		if wait > retryMaxBackoff {
			wait = retryMaxBackoff
		}
		select {
		case <-ctx.Done():
			return nil, err
		case <-time.After(wait):
		}
		backoff *= 2
	}
	return nil, lastErr
}

// shouldRetryDial 判断连接失败是否值得重拨：传输层断裂或 5xx/429。
func shouldRetryDial(err error) bool {
	if isRetryableTransportError(err) {
		return true
	}
	var f *llm.Failure
	if errors.As(err, &f) {
		if f.RateLimited || f.UpstreamFault {
			return true
		}
	}
	return false
}

// retryAfterOf 提取上游建议的等待时长；无则返回 0。
func retryAfterOf(err error) time.Duration {
	var f *llm.Failure
	if errors.As(err, &f) && f.RetryAfterSeconds > 0 {
		return time.Duration(f.RetryAfterSeconds) * time.Second
	}
	return 0
}

// dial 建立上游流式连接。
//
// 超时 ctx 的 cancel 交给流对象保管（Close 时调用），因此这里不能 defer cancel：
// 流要在函数返回后继续存活。
func (a *Adapter) dial(ctx context.Context, body upstreamRequest) (llm.ResponseStream, error) {
	streamCtx, cancel := context.WithTimeout(ctx, a.cfg.StreamTotalTimeout)
	headers := a.auth.BuildHeaders(headerModeFull)

	resp, err := a.cli.openStream(streamCtx, chatCompletionsPath, body, headers)
	if err != nil {
		cancel()
		return nil, err
	}
	resp.Body = newIdleReadCloser(resp.Body, a.cfg.StreamIdleTimeout)
	stream := newUpstreamStream(resp)
	stream.cancel = cancel
	return &closeGuard{ResponseStream: stream, closer: stream}, nil
}

// closeGuard 确保流用尽后释放上游连接。
type closeGuard struct {
	llm.ResponseStream
	closer interface{ Close() error }
}

func (g *closeGuard) Recv(ctx context.Context) (llm.ResponseEvent, error) {
	ev, err := g.ResponseStream.Recv(ctx)
	if err != nil {
		_ = g.closer.Close()
	}
	return ev, err
}

func (g *closeGuard) Close() error { return g.closer.Close() }

// isUnauthorized 判断是否为认证失败。
func isUnauthorized(err error) bool {
	if err == nil {
		return false
	}
	var f *llm.Failure
	if !errors.As(err, &f) {
		return false
	}
	return f.Code == "unauthorized" || f.Code == "http_401"
}

func (a *Adapter) logf(format string, args ...any) {
	if a.cfg.Logger != nil {
		a.cfg.Logger(format, args...)
	}
}

// String 便于调试。
func (a *Adapter) String() string {
	return fmt.Sprintf("workbuddy[base=%s account=%s]", a.cfg.BaseURL, a.CredentialInfo())
}

// 编译期断言。
var _ adapter.Adapter = (*Adapter)(nil)

// Describe 实现 adapter.Describer，向控制台暴露运行时信息。
func (a *Adapter) Describe() adapter.Description {
	desc := adapter.Description{
		ID:      "workbuddy",
		Name:    "WorkBuddy / CodeBuddy",
		BaseURL: a.cfg.BaseURL,
		Account: a.CredentialInfo(),
	}
	// 用一次模型探测判定健康状态：控制台需要知道上游是否真的可达。
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	models, err := a.ListModels(ctx)
	switch {
	case err != nil:
		desc.Status = "error"
		desc.Notes = "上游不可达: " + err.Error()
	case len(models) == 0:
		desc.Status = "degraded"
		desc.Notes = "上游返回空模型清单"
	default:
		desc.Status = "active"
		desc.Models = len(models)
	}
	return desc
}

// SetSanitize 实现 adapter.Configurable。
func (a *Adapter) SetSanitize(v bool) { a.sanitize.Store(v) }

// Sanitize 实现 adapter.Configurable。
func (a *Adapter) Sanitize() bool { return a.sanitize.Load() }

// InvalidateModels 实现 adapter.Configurable：让模型清单缓存失效。
func (a *Adapter) InvalidateModels() { a.models.invalidate() }
