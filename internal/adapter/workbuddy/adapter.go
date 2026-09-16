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
		for _, p := range defaultCredentialPaths() {
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

// Stream 实现 adapter.Adapter。
//
// 上游不支持非流式，因此这里始终以流式请求；调用方需要单条完整响应时
// 在 app 层聚合即可。
func (a *Adapter) Stream(ctx context.Context, req llm.RequestMessages) (llm.ResponseStream, error) {
	if err := a.auth.EnsureValid(); err != nil {
		a.logf("凭证刷新失败: %v", err)
		// 刷新失败不阻断请求：旧 token 可能仍有效。
	}

	body := buildUpstreamRequest(req, a.sanitize.Load())

	s, err := a.dial(ctx, body)
	if err == nil {
		return s, nil
	}

	// 401 视为凭证过期：刷新一次后重试。
	if isUnauthorized(err) {
		a.logf("收到 401，尝试刷新 token 后重试")
		if refreshErr := a.auth.Refresh(); refreshErr != nil {
			a.logf("刷新 token 失败: %v", refreshErr)
			return nil, err
		}
		return a.dial(ctx, body)
	}
	return nil, err
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
