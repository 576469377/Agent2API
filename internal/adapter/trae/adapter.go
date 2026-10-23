package trae

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/576469377/Agent2API/internal/adapter"
	"github.com/576469377/Agent2API/internal/llm"
)

const DefaultBaseURL = "https://trae-api-cn.mchost.guru"

// 目录接口要求版本码；固定为已知原生协议版本，不按调用时间或账号动态变化。
const catalogProtocolVersionCode = "20261023"
const catalogFunction = "solo_agent"

type Config struct {
	BaseURL, CredentialPath                                              string
	CheckinStatePath                                                     string
	RequestTimeout, StreamIdleTimeout, StreamTotalTimeout, ModelCacheTTL time.Duration
}

type Adapter struct {
	cfg      Config
	client   *http.Client
	load     func(string) (Credential, error)
	authBase string
	mu       sync.Mutex
	quotaMu  sync.RWMutex
	models   []catalogModel
	expires  time.Time
	uid      string
	ready    bool
	checkins *checkinController
}

func New(cfg Config) (*Adapter, error) {
	if cfg.BaseURL == "" {
		cfg.BaseURL = DefaultBaseURL
	}
	cfg.BaseURL = strings.TrimRight(cfg.BaseURL, "/")
	u, e := url.Parse(cfg.BaseURL)
	if e != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Scheme != "https" && u.Scheme != "http") {
		return nil, fmt.Errorf("TRAE base_url 无效")
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
		cfg.ModelCacheTTL = time.Minute
	}
	t := http.DefaultTransport.(*http.Transport).Clone()
	t.ForceAttemptHTTP2 = true
	t.ResponseHeaderTimeout = cfg.RequestTimeout
	a := &Adapter{cfg: cfg, client: &http.Client{Transport: t, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}, load: readCredential, authBase: authBaseURL}
	a.checkins = newCheckinController(a)
	return a, nil
}

func (a *Adapter) Name() string { return Platform }
func (a *Adapter) InvalidateModels() {
	a.mu.Lock()
	a.expires = time.Time{}
	a.ready = false
	a.mu.Unlock()
}
func (a *Adapter) Describe() adapter.Description {
	a.mu.Lock()
	ready, count := a.ready, len(a.models)
	a.mu.Unlock()
	d := adapter.Description{ID: Platform, Name: "TRAE 国内版", BaseURL: a.cfg.BaseURL, Account: "网页 Free 账号", Status: "unauthenticated", Notes: "通过官方网页授权；仅允许已验证的个人 Free 赠送积分"}
	if ready {
		d.Status = "active"
		d.Models = count
		d.Notes = "每次生成重新核对免费权益；额度耗尽或套餐改变时停止"
	} else if c, err := a.load(a.cfg.CredentialPath); err == nil && time.Now().Before(expiryTime(c.ExpiresAt)) {
		d.Status = "degraded"
		d.Notes = "网页授权已保存，免费权益与模型目录待检查；可重新检测连接"
	}
	return d
}

func headers(c Credential) http.Header {
	h := http.Header{"Content-Type": {"application/json"}, "Accept": {"application/json"}, "User-Agent": {"Agent2API"}}
	h.Set("Authorization", "Cloud-IDE-JWT "+c.Token)
	h.Set("x-cloudide-token", c.Token)
	h.Set("x-ide-token", c.Token)
	h.Set("x-uid", c.UserID)
	h.Set("x-app-id", "6eefa01c-1036-4c7e-9ca5-d891f63bfcd8")
	h.Set("x-app-version", "default")
	h.Set("x-ide-version", authProtocolVersion)
	h.Set("x-ide-version-code", catalogProtocolVersionCode)
	h.Set("x-ide-version-type", "stable")
	if c.Device != nil {
		h.Set("x-device-id", c.Device.DeviceID)
		h.Set("x-machine-id", c.Device.MachineID)
	}
	return h
}

func (a *Adapter) read(ctx context.Context, c Credential, base, path string, body any) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, a.cfg.RequestTimeout)
	defer cancel()
	raw, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, "POST", base+path, bytes.NewReader(raw))
	if err != nil {
		return nil, err
	}
	req.Header = headers(c)
	if strings.HasPrefix(path, "/trae/api/v2/ug/checkin_credits/") && c.Device != nil {
		req.Header.Set("x-device-brand", c.Device.DeviceBrand)
		req.Header.Set("x-device-type", c.Device.DeviceType)
	}
	if path == "/api/ide/v1/get_detail_param" {
		req.Header.Set("x-ide-function", catalogFunction)
		req.Header.Set("x-app-function", catalogFunction)
	}
	resp, err := a.client.Do(req)
	if err != nil {
		return nil, transportFailure(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, httpFailure(resp.StatusCode)
	}
	raw, err = io.ReadAll(io.LimitReader(resp.Body, 16*1024*1024+1))
	if err != nil {
		return nil, transportFailure(err)
	}
	if len(raw) > 16*1024*1024 {
		return nil, fault("trae_response_too_large", "TRAE 配置响应过大")
	}
	return raw, nil
}

func (a *Adapter) authorize(ctx context.Context) (Credential, float64, error) {
	// 签到到账与来源确认期间等待，避免把中间状态误报为未知积分来源。
	a.quotaMu.RLock()
	defer a.quotaMu.RUnlock()
	c, err := a.load(a.cfg.CredentialPath)
	if err != nil {
		return c, 0, err
	}
	if !expiryTime(c.ExpiresAt).After(time.Now().Add(30 * time.Second)) {
		return c, 0, loginRequired()
	}
	raw, err := a.read(ctx, c, a.authBase, "/trae/api/v2/pay/ide_user_ent_usage", map[string]any{"require_usage": true, "req_source": 1, "full_data": true})
	if err != nil {
		return c, 0, err
	}
	remaining, err := verifyFreeQuotaApproved(raw, time.Now(), a.approvedRewards(c.UserID))
	return c, remaining, err
}

func (a *Adapter) catalog(ctx context.Context, c Credential, remaining float64) ([]catalogModel, error) {
	a.mu.Lock()
	if a.uid == c.UserID && time.Now().Before(a.expires) && len(a.models) > 0 {
		models := append([]catalogModel(nil), a.models...)
		a.mu.Unlock()
		for i := range models {
			p := *models[i].Info.Access
			p.RemainingCredits = remaining
			p.CheckedAt = time.Now().UTC().Format(time.RFC3339)
			models[i].Info.Access = &p
		}
		return models, nil
	}
	a.mu.Unlock()
	raw, err := a.read(ctx, c, a.cfg.BaseURL, "/api/ide/v1/get_detail_param", map[string]any{"function": catalogFunction, "config_names": nil, "need_prompt": false, "current_config_info": nil, "poly_prompt": true, "mode_type": nil, "agent_type": nil})
	if err != nil {
		return nil, err
	}
	models, err := decodeModels(raw, remaining, time.Now())
	if err != nil {
		return nil, err
	}
	a.mu.Lock()
	a.models = models
	a.uid = c.UserID
	a.expires = time.Now().Add(a.cfg.ModelCacheTTL)
	a.ready = true
	a.mu.Unlock()
	return append([]catalogModel(nil), models...), nil
}

func (a *Adapter) ListModels(ctx context.Context) ([]adapter.ModelInfo, error) {
	c, remaining, err := a.authorize(ctx)
	if err != nil {
		a.InvalidateModels()
		return nil, err
	}
	models, err := a.catalog(ctx, c, remaining)
	if err != nil {
		a.InvalidateModels()
		return nil, err
	}
	out := make([]adapter.ModelInfo, len(models))
	for i, m := range models {
		out[i] = m.Info
	}
	return out, nil
}

func (a *Adapter) Stream(ctx context.Context, r llm.RequestMessages) (llm.ResponseStream, error) {
	if r.Model == "" {
		r.Model = "deepseek-v4.1-flash"
	}
	if !enabledModels[r.Model] {
		return nil, &llm.Failure{Code: "trae_verified_model_required", Message: "TRAE 只允许当前已验证的明确模型，不透传未知模型或 Auto", ClientFixable: true}
	}
	c, remaining, err := a.authorize(ctx)
	if err != nil {
		a.InvalidateModels()
		return nil, err
	}
	models, err := a.catalog(ctx, c, remaining)
	if err != nil {
		return nil, err
	}
	var selected *catalogModel
	for i := range models {
		if models[i].Info.ID == r.Model {
			selected = &models[i]
			break
		}
	}
	if selected == nil {
		return nil, &llm.Failure{Code: "trae_model_unavailable", Message: "当前 TRAE 账号未提供所选模型", ClientFixable: true}
	}
	payload, err := buildRequest(r, *selected)
	if err != nil {
		return nil, err
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	streamCtx, cancel := context.WithTimeout(ctx, a.cfg.StreamTotalTimeout)
	attemptCtx, finish, err := llm.BeginAttempt(streamCtx, Platform, a.cfg.RequestTimeout)
	if err != nil {
		cancel()
		return nil, err
	}
	req, err := http.NewRequestWithContext(adapter.TraceRequest(attemptCtx), "POST", a.cfg.BaseURL+"/api/ide/v2/llm_raw_chat", bytes.NewReader(raw))
	if err != nil {
		cancel()
		return nil, finish(0, err)
	}
	req.Header = headers(c)
	req.Header.Set("Accept", "text/event-stream")
	req.Header.Set("x-app-function", "chat_v3")
	req.Header.Set("x-ide-function", "chat_v3")
	resp, err := a.client.Do(req)
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
		return nil, transportFailure(err)
	}
	if resp.StatusCode != 200 {
		resp.Body.Close()
		cancel()
		f := httpFailure(resp.StatusCode)
		llm.NoteAttemptFailure(ctx, f)
		return nil, f
	}
	if !strings.HasPrefix(strings.ToLower(resp.Header.Get("Content-Type")), "text/event-stream") {
		resp.Body.Close()
		cancel()
		return nil, fault("trae_invalid_stream", "TRAE 未返回 SSE 响应")
	}
	return newStream(resp.Body, cancel, a.cfg.StreamIdleTimeout, r.Model), nil
}

func fault(code, message string) *llm.Failure {
	return &llm.Failure{Code: code, Message: message, UpstreamFault: true}
}
func transportFailure(err error) *llm.Failure {
	return llm.Wrap(&llm.Failure{Code: "trae_transport", Message: "TRAE 连接失败", Cause: err, UpstreamFault: true})
}
func httpFailure(status int) *llm.Failure {
	f := &llm.Failure{Code: "trae_http_" + strconv.Itoa(status), Message: "TRAE 拒绝请求（HTTP " + strconv.Itoa(status) + "）"}
	if status == 401 || status == 403 {
		f.Unauthorized = true
		f.ClientFixable = true
	} else if status == 429 {
		f.RateLimited = true
	} else if status >= 500 {
		f.UpstreamFault = true
	} else {
		f.ClientFixable = true
	}
	return f
}

var _ adapter.Adapter = (*Adapter)(nil)
