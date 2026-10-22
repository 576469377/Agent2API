package cursor

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/576469377/Agent2API/internal/adapter"
	"github.com/576469377/Agent2API/internal/llm"
)

const Platform = "cursor"
const DefaultBaseURL = "https://api2.cursor.sh"
const verifiedModel = "composer-2.5"

type Config struct {
	BaseURL, CredentialPath                               string
	RequestTimeout, StreamIdleTimeout, StreamTotalTimeout time.Duration
}

type Adapter struct {
	cfg    Config
	client *http.Client
	load   func(context.Context, string) (credentials, error)
	mu     sync.Mutex
	ready  bool
}

func New(cfg Config) (*Adapter, error) {
	if cfg.BaseURL == "" {
		cfg.BaseURL = DefaultBaseURL
	}
	cfg.BaseURL = strings.TrimRight(cfg.BaseURL, "/")
	if !validBase(cfg.BaseURL) {
		return nil, fmt.Errorf("invalid Cursor base_url")
	}
	if cfg.RequestTimeout <= 0 {
		cfg.RequestTimeout = 30 * time.Second
	}
	if cfg.StreamIdleTimeout <= 0 {
		cfg.StreamIdleTimeout = 120 * time.Second
	}
	if cfg.StreamTotalTimeout <= 0 {
		cfg.StreamTotalTimeout = 30 * time.Minute
	}
	t := http.DefaultTransport.(*http.Transport).Clone()
	t.ForceAttemptHTTP2 = true
	t.ResponseHeaderTimeout = cfg.RequestTimeout
	return &Adapter{cfg: cfg, client: &http.Client{Transport: t, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}, load: readCredentials}, nil
}

func validBase(value string) bool {
	u, err := url.Parse(value)
	return err == nil && u.Host != "" && (u.Scheme == "http" || u.Scheme == "https") && u.User == nil && u.RawQuery == "" && u.Fragment == ""
}

func (a *Adapter) Name() string { return Platform }
func (a *Adapter) Describe() adapter.Description {
	a.mu.Lock()
	ready := a.ready
	a.mu.Unlock()
	d := adapter.Description{ID: Platform, Name: "Cursor", BaseURL: a.cfg.BaseURL, Account: "桌面 Free 账号", Status: "unauthenticated", Notes: "请在当前用户的 Cursor 中登录；只允许个人 Free 额度"}
	if ready {
		d.Status = "active"
		d.Models = 1
		d.Notes = "Composer 2.5 · Free 限额 · 每次生成重新检查计费状态"
	}
	return d
}
func (a *Adapter) InvalidateModels() { a.mu.Lock(); a.ready = false; a.mu.Unlock() }

func headers(c credentials) http.Header {
	h := http.Header{}
	h.Set("Authorization", "Bearer "+c.token)
	h.Set("Content-Type", "application/json")
	h.Set("Connect-Protocol-Version", "1")
	h.Set("X-Cursor-Client-Version", c.version)
	h.Set("X-Cursor-Client-Commit", c.commit)
	h.Set("X-Cursor-Client-Type", "ide")
	h.Set("X-Cursor-Checksum", c.checksum(time.Now()))
	h.Set("X-Ghost-Mode", "true")
	return h
}

func (a *Adapter) unary(ctx context.Context, c credentials, path string, body any, out any) error {
	ctx, cancel := context.WithTimeout(ctx, a.cfg.RequestTimeout)
	defer cancel()
	var data []byte
	method := "GET"
	if body != nil {
		method = "POST"
		data, _ = json.Marshal(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, a.cfg.BaseURL+path, bytes.NewReader(data))
	if err != nil {
		return err
	}
	req.Header = headers(c)
	resp, err := a.client.Do(req)
	if err != nil {
		return transportError(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return httpFailure(resp.StatusCode)
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 8*1024*1024+1))
	if err != nil {
		return transportError(err)
	}
	if len(raw) > 8*1024*1024 || json.Unmarshal(raw, out) != nil {
		return fault("invalid_response", "Cursor 返回了无效的接口数据")
	}
	return nil
}

type serverConfig struct {
	ConfigVersion       string `json:"configVersion"`
	ClientVersionStatus struct {
		Minimum string `json:"minSupportedClientVersion"`
	} `json:"clientVersionStatus"`
	AgentURLConfig struct {
		URL string `json:"agentUrl"`
	} `json:"agentUrlConfig"`
}

func (a *Adapter) authorize(ctx context.Context) (credentials, serverConfig, error) {
	var server serverConfig
	c, err := a.load(ctx, a.cfg.CredentialPath)
	if err != nil {
		return c, server, &llm.Failure{Code: "cursor_login_required", Message: err.Error(), Unauthorized: true}
	}
	var profile struct {
		Membership   string `json:"membershipType"`
		Individual   string `json:"individualMembershipType"`
		Subscription string `json:"subscriptionStatus"`
		Team         *bool  `json:"isTeamMember"`
		Student      *bool  `json:"isOnStudentPlan"`
	}
	var limits struct {
		Disabled *bool `json:"noUsageBasedAllowed"`
	}
	if err = a.unary(ctx, c, "/auth/full_stripe_profile", nil, &profile); err != nil {
		return c, server, err
	}
	if err = a.unary(ctx, c, "/aiserver.v1.DashboardService/GetHardLimit", struct{}{}, &limits); err != nil {
		return c, server, err
	}
	if profile.Membership != "free" || profile.Individual != "free" || profile.Subscription != "unpaid" || profile.Team == nil || *profile.Team || profile.Student == nil || *profile.Student || limits.Disabled == nil || !*limits.Disabled {
		a.InvalidateModels()
		return c, server, &llm.Failure{Code: "cursor_free_only", Message: "Cursor 仅允许已确认的个人 Free、非试用、非团队且禁用按量付费的账号；计费状态不符或不完整", ClientFixable: true}
	}
	if err = a.unary(ctx, c, "/aiserver.v1.ServerConfigService/GetServerConfig", struct{}{}, &server); err != nil {
		return c, server, err
	}
	if !versionAtLeast(c.version, server.ClientVersionStatus.Minimum) || server.ConfigVersion == "" {
		return c, server, &llm.Failure{Code: "cursor_update_required", Message: "请将 Cursor 升级到服务器当前支持的版本", ClientFixable: true}
	}
	return c, server, nil
}

func (a *Adapter) checkModel(ctx context.Context, c credentials) error {
	var catalogue struct {
		Models []struct {
			ID  string `json:"modelId"`
			Max bool   `json:"maxMode"`
		} `json:"models"`
	}
	if err := a.unary(ctx, c, "/agent.v1.AgentService/GetUsableModels", struct{}{}, &catalogue); err != nil {
		return err
	}
	for _, m := range catalogue.Models {
		if m.ID == verifiedModel && !m.Max {
			return nil
		}
	}
	return &llm.Failure{Code: "cursor_model_unavailable", Message: "当前 Cursor 账号未提供已验证的 Composer 2.5", ClientFixable: true}
}

func (a *Adapter) ListModels(ctx context.Context) ([]adapter.ModelInfo, error) {
	c, _, err := a.authorize(ctx)
	if err != nil {
		a.InvalidateModels()
		return nil, err
	}
	if err = a.checkModel(ctx, c); err != nil {
		a.InvalidateModels()
		return nil, err
	}
	a.mu.Lock()
	a.ready = true
	a.mu.Unlock()
	return []adapter.ModelInfo{{ID: verifiedModel, DisplayName: "Composer 2.5 (Free 额度)", Description: "个人 Free 限额；额度耗尽后报错，不启用付费；系统指令按客户端上下文转交，保留 Cursor 原生规则", ContextTokens: 200000, MaxOutputTokens: 8192, SupportsThinking: true, SupportsTools: true, IsDefault: true,
		Pricing: &adapter.ModelPricing{Status: "free", Source: "cursor_free_plan", UpstreamLabel: "个人 Free 限额，已禁用按量付费", ObservedAt: time.Now().UTC().Format(time.RFC3339)}}}, nil
}

func (a *Adapter) Stream(ctx context.Context, req llm.RequestMessages) (llm.ResponseStream, error) {
	if req.Model == "" {
		req.Model = verifiedModel
	}
	if req.Model != verifiedModel {
		return nil, &llm.Failure{Code: "cursor_free_model_required", Message: "Cursor 当前仅接入已验证的免费额度模型 composer-2.5；禁止 Auto / Max / 其他未验证模型", ClientFixable: true}
	}
	payload, tools, blobs, err := buildRequest(req)
	if err != nil {
		return nil, err
	}
	c, server, err := a.authorize(ctx)
	if err != nil {
		return nil, err
	}
	if err = a.checkModel(ctx, c); err != nil {
		return nil, err
	}
	target := server.AgentURLConfig.URL
	if a.cfg.BaseURL != DefaultBaseURL {
		target = a.cfg.BaseURL
	} else {
		u, e := url.Parse(target)
		if e != nil || !validBase(target) || u.Scheme != "https" || !strings.HasSuffix(u.Hostname(), ".cursor.sh") || u.Path != "" || u.Port() != "" {
			return nil, fault("invalid_agent_url", "Cursor 返回了不受信任的生成地址")
		}
	}
	streamCtx, cancel := context.WithTimeout(ctx, a.cfg.StreamTotalTimeout)
	attemptCtx, finish, err := llm.BeginAttempt(streamCtx, Platform, a.cfg.RequestTimeout)
	if err != nil {
		cancel()
		return nil, err
	}
	reader, writer := io.Pipe()
	httpReq, err := http.NewRequestWithContext(attemptCtx, "POST", strings.TrimRight(target, "/")+"/agent.v1.AgentService/Run", reader)
	if err != nil {
		cancel()
		reader.Close()
		writer.Close()
		return nil, finish(0, err)
	}
	httpReq.Header = headers(c)
	httpReq.Header.Set("Content-Type", "application/connect+json")
	httpReq.Header.Set("X-Cursor-Config-Version", server.ConfigVersion)
	initial := make(chan error, 1)
	go func() { initial <- writeFrame(writer, map[string]any{"runRequest": payload}) }()
	resp, err := a.client.Do(httpReq)
	status := 0
	if resp != nil {
		status = resp.StatusCode
	}
	err = finish(status, err)
	if err != nil {
		if resp != nil {
			resp.Body.Close()
		}
		cancel()
		reader.Close()
		writer.Close()
		return nil, transportError(err)
	}
	cleanup := func() { cancel(); resp.Body.Close(); reader.Close(); writer.Close() }
	if resp.StatusCode != 200 {
		cleanup()
		failure := httpFailure(resp.StatusCode)
		llm.NoteAttemptFailure(ctx, failure)
		return nil, failure
	}
	if resp.ProtoMajor != 2 || !strings.HasPrefix(resp.Header.Get("Content-Type"), "application/connect+json") {
		cleanup()
		return nil, fault("unsupported_transport", "Cursor 生成需要 HTTP/2 Connect JSON")
	}
	select {
	case err = <-initial:
	case <-streamCtx.Done():
		err = streamCtx.Err()
	}
	if err != nil {
		cleanup()
		return nil, transportError(err)
	}
	return newStream(streamCtx, resp.Body, writer, cancel, req, tools, blobs, a.cfg.StreamIdleTimeout), nil
}

func fault(code, message string) *llm.Failure {
	return &llm.Failure{Code: code, Message: message, UpstreamFault: true}
}
func transportError(err error) *llm.Failure {
	// Never echo request URLs, headers, arbitrary upstream bodies, or credentials.
	f := fault("cursor_transport", "Cursor 连接失败")
	f.Cause = err
	return f
}
func httpFailure(status int) *llm.Failure {
	f := &llm.Failure{Code: fmt.Sprintf("cursor_http_%d", status), Message: fmt.Sprintf("Cursor 接口返回 HTTP %d", status)}
	switch {
	case status == 401 || status == 403:
		f.Unauthorized = true
	case status == 402:
		f.QuotaExhausted = true
		f.RateLimited = true
	case status == 429:
		f.RateLimited = true
	case status >= 500:
		f.UpstreamFault = true
	default:
		f.ClientFixable = true
	}
	return f
}
