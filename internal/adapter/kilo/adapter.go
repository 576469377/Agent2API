// Package kilo 接入 Kilo 官方匿名免费通道，始终不发送账号、组织或 BYOK 凭据。
package kilo

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/576469377/Agent2API/internal/adapter"
	"github.com/576469377/Agent2API/internal/adapter/openrouter"
	"github.com/576469377/Agent2API/internal/llm"
)

const Platform = "kilo"
const DefaultBaseURL = "https://ai-gateway.kilo.ai/api/v1"

type Config struct {
	BaseURL                                                              string
	RequestTimeout, StreamIdleTimeout, StreamTotalTimeout, ModelCacheTTL time.Duration
}

type Adapter struct {
	cfg      Config
	client   *http.Client
	gate     chan struct{}
	mu       sync.Mutex
	models   []adapter.ModelInfo
	expires  time.Time
	revision uint64
	serial   uint64
}

func New(cfg Config) (*Adapter, error) {
	if cfg.BaseURL == "" {
		cfg.BaseURL = DefaultBaseURL
	}
	cfg.BaseURL = strings.TrimRight(cfg.BaseURL, "/")
	u, err := url.Parse(cfg.BaseURL)
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return nil, fmt.Errorf("invalid Kilo base_url")
	}
	if cfg.RequestTimeout <= 0 {
		cfg.RequestTimeout = time.Minute
	}
	if cfg.StreamIdleTimeout <= 0 {
		cfg.StreamIdleTimeout = 2 * time.Minute
	}
	if cfg.StreamTotalTimeout <= 0 {
		cfg.StreamTotalTimeout = 30 * time.Minute
	}
	if cfg.ModelCacheTTL <= 0 || cfg.ModelCacheTTL > time.Minute {
		cfg.ModelCacheTTL = time.Minute
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.ResponseHeaderTimeout = cfg.RequestTimeout
	return &Adapter{cfg: cfg, gate: make(chan struct{}, 1), client: &http.Client{Transport: transport, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}, nil
}

func (*Adapter) Name() string { return Platform }
func (a *Adapter) Describe() adapter.Description {
	a.mu.Lock()
	count := len(a.models)
	a.mu.Unlock()
	return adapter.Description{ID: Platform, Name: "Kilo Free", BaseURL: a.cfg.BaseURL, Status: "active", Models: count, Account: "匿名免费通道", Notes: "无需 API Key；仅明确免费的具体模型，按出口 IP 共享限流，剩余次数未知"}
}

func (a *Adapter) request(ctx context.Context, method, path string, body []byte) (*http.Response, error) {
	finish := func(_ int, err error) error { return err }
	if method == http.MethodPost {
		var err error
		ctx, finish, err = llm.BeginAttempt(ctx, Platform, a.cfg.RequestTimeout)
		if err != nil {
			return nil, err
		}
	}
	req, err := http.NewRequestWithContext(adapter.TraceRequest(ctx), method, a.cfg.BaseURL+path, bytes.NewReader(body))
	if err != nil {
		return nil, finish(0, err)
	}
	// 不读取 API Key 环境变量，也不转发下游鉴权；匿名权限在上游只能访问免费模型。
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "Agent2API/0.2")
	resp, err := a.client.Do(req)
	status := 0
	if resp != nil {
		status = resp.StatusCode
	}
	if err = finish(status, err); err != nil {
		if resp != nil {
			resp.Body.Close()
		}
		return nil, llm.Wrap(err)
	}
	if status >= 200 && status < 300 {
		return resp, nil
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	f := &llm.Failure{Code: fmt.Sprintf("http_%d", status), Message: "Kilo: " + openrouter.ChatError(raw, http.StatusText(status))}
	switch {
	case status == 429:
		f.RateLimited = true
		var detail struct {
			Error struct {
				Metadata struct {
					LimitSource string `json:"limit_source"`
				} `json:"metadata"`
			} `json:"error"`
		}
		_ = json.Unmarshal(raw, &detail)
		if detail.Error.Metadata.LimitSource == "upstream_provider_shared_pool" {
			f.Message += "；该模型供应商的共享容量暂时受限，请稍后重试或选择其他免费模型"
		} else if strings.Contains(strings.ToLower(f.Message), "rate limit exceeded for free models") {
			f.Message = "Kilo 免费通道受到出口 IP 共享限流；请等待恢复，切换账号不会增加该额度"
		} else if strings.Contains(strings.ToLower(f.Message), "sign up for free to continue") {
			f.Message = "Kilo 匿名免费请求暂时受限，请等待上游恢复；本次不会改用账号密钥、收费模型或 BYOK"
		}
	case status == 402:
		f.QuotaExhausted, f.RateLimited = true, true
		f.Message = "Kilo 匿名免费通道暂不可用；本次没有发送付费账号凭据，也不会切换收费模型"
	case status == 401 || status == 403:
		f.ClientFixable = true
	case status >= 500:
		f.UpstreamFault = true
	default:
		f.ClientFixable = true
	}
	if seconds, err := strconv.Atoi(resp.Header.Get("Retry-After")); err == nil && seconds > 0 {
		f.RetryAfterSeconds = seconds
	} else if until, err := http.ParseTime(resp.Header.Get("Retry-After")); err == nil {
		f.RetryAfterSeconds = max(0, int(time.Until(until).Seconds())+1)
	}
	llm.NoteAttemptFailure(ctx, f)
	return nil, f
}

type catalogModel struct {
	ID           string                     `json:"id"`
	Name         string                     `json:"name"`
	Description  string                     `json:"description"`
	IsFree       *bool                      `json:"isFree"`
	MayTrain     *bool                      `json:"mayTrainOnYourPrompts"`
	Expiration   json.RawMessage            `json:"expiration_date"`
	Context      int                        `json:"context_length"`
	Pricing      map[string]json.RawMessage `json:"pricing"`
	Parameters   []string                   `json:"supported_parameters"`
	Architecture struct {
		Input  []string `json:"input_modalities"`
		Output []string `json:"output_modalities"`
	} `json:"architecture"`
	Top struct {
		Context   int `json:"context_length"`
		MaxOutput int `json:"max_completion_tokens"`
	} `json:"top_provider"`
}

func zeroPricing(prices map[string]json.RawMessage) bool {
	if len(prices["prompt"]) == 0 || len(prices["completion"]) == 0 {
		return false
	}
	for _, raw := range prices {
		value := string(raw)
		if len(raw) > 0 && raw[0] == '"' && json.Unmarshal(raw, &value) != nil {
			return false
		}
		n, ok := new(big.Rat).SetString(value)
		if !ok || n.Sign() != 0 {
			return false
		}
	}
	return true
}

func expiry(raw json.RawMessage) (time.Time, bool) {
	if len(raw) == 0 || string(raw) == "null" {
		return time.Time{}, true
	}
	var value string
	if json.Unmarshal(raw, &value) != nil || value == "" {
		return time.Time{}, false
	}
	for _, layout := range []string{time.RFC3339Nano, "2006-01-02"} {
		if t, err := time.Parse(layout, value); err == nil {
			return t, true
		}
	}
	return time.Time{}, false
}

func concrete(id string) bool {
	lower := strings.ToLower(id)
	return id != "" && strings.TrimSpace(id) == id && !strings.ContainsAny(id, "\r\n?#") && !strings.Contains(lower, "router") && !strings.HasPrefix(lower, "kilo-auto") && lower != "auto" && lower != "auto-free" && !strings.HasPrefix(lower, "auto/")
}

func decodeCatalog(raw []byte, now time.Time) ([]adapter.ModelInfo, error) {
	var wire struct {
		Data []catalogModel `json:"data"`
	}
	if json.Unmarshal(raw, &wire) != nil || wire.Data == nil {
		return nil, fmt.Errorf("Kilo 返回了无效的免费模型目录")
	}
	counts := map[string]int{}
	for _, row := range wire.Data {
		counts[row.ID]++
	}
	models := []adapter.ModelInfo{}
	for _, row := range wire.Data {
		until, valid := expiry(row.Expiration)
		if !concrete(row.ID) || counts[row.ID] != 1 || row.IsFree == nil || !*row.IsFree || !zeroPricing(row.Pricing) || !valid || (!until.IsZero() && !now.Before(until)) || len(row.Architecture.Output) != 1 || row.Architecture.Output[0] != "text" || !slices.Contains(row.Parameters, "tools") {
			continue
		}
		context := row.Context
		if row.Top.Context > 0 && (context <= 0 || row.Top.Context < context) {
			context = row.Top.Context
		}
		if context <= 0 || row.Top.MaxOutput <= 0 {
			continue
		}
		pricing := &adapter.ModelPricing{Status: "free", Source: "kilo_models", ObservedAt: now.UTC().Format(time.RFC3339Nano), UpstreamLabel: "Kilo 匿名免费通道"}
		if !until.IsZero() {
			pricing.ExpiresAt = until.UTC().Format(time.RFC3339Nano)
		}
		description := row.Description
		if row.MayTrain != nil && *row.MayTrain {
			description += "\n上游声明此免费端点可能保留提示并用于训练。"
		}
		models = append(models, adapter.ModelInfo{ID: row.ID, DisplayName: row.Name, Description: description, ContextTokens: context, MaxOutputTokens: min(row.Top.MaxOutput, context), SupportsImages: slices.Contains(row.Architecture.Input, "image"), SupportsTools: slices.Contains(row.Parameters, "tools"), SupportsThinking: slices.Contains(row.Parameters, "reasoning") || slices.Contains(row.Parameters, "reasoning_effort"), Pricing: pricing})
	}
	slices.SortFunc(models, func(a, b adapter.ModelInfo) int { return strings.Compare(a.ID, b.ID) })
	return models, nil
}

func cloneModels(models []adapter.ModelInfo) []adapter.ModelInfo {
	out := slices.Clone(models)
	for i := range out {
		if out[i].Pricing != nil {
			p := *out[i].Pricing
			out[i].Pricing = &p
		}
	}
	return out
}

func (a *Adapter) listModels(ctx context.Context, force bool) ([]adapter.ModelInfo, error) {
	a.mu.Lock()
	serial := a.serial
	a.mu.Unlock()
	ctx, cancel := context.WithTimeout(ctx, a.cfg.RequestTimeout)
	defer cancel()
	select {
	case a.gate <- struct{}{}:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	defer func() { <-a.gate }()
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		a.mu.Lock()
		if time.Now().Before(a.expires) && (!force || a.serial > serial) {
			models := cloneModels(a.models)
			a.mu.Unlock()
			return models, nil
		}
		revision := a.revision
		a.mu.Unlock()
		resp, err := a.request(ctx, http.MethodGet, "/models", nil)
		if err != nil {
			return nil, err
		}
		raw, err := io.ReadAll(io.LimitReader(resp.Body, (16<<20)+1))
		resp.Body.Close()
		if err != nil || len(raw) > 16<<20 {
			return nil, fmt.Errorf("无法读取 Kilo 免费模型目录")
		}
		checked := time.Now()
		models, err := decodeCatalog(raw, checked)
		if err != nil {
			return nil, err
		}
		a.mu.Lock()
		if revision != a.revision {
			a.mu.Unlock()
			continue
		}
		a.models, a.expires = models, checked.Add(a.cfg.ModelCacheTTL)
		a.serial++
		for _, model := range models {
			if model.Pricing.ExpiresAt != "" {
				until, _ := time.Parse(time.RFC3339Nano, model.Pricing.ExpiresAt)
				if until.Before(a.expires) {
					a.expires = until
				}
			}
		}
		models = cloneModels(models)
		a.mu.Unlock()
		return models, nil
	}
}

func (a *Adapter) ListModels(ctx context.Context) ([]adapter.ModelInfo, error) {
	return a.listModels(ctx, false)
}
func (a *Adapter) InvalidateModels() {
	a.mu.Lock()
	a.revision++
	a.expires = time.Time{}
	a.mu.Unlock()
}
func (a *Adapter) VerifyConnection(ctx context.Context) error {
	models, err := a.listModels(ctx, true)
	if err == nil && len(models) == 0 {
		return fmt.Errorf("Kilo 当前未提供符合要求的具体免费模型")
	}
	return err
}

func (a *Adapter) Stream(ctx context.Context, req llm.RequestMessages) (llm.ResponseStream, error) {
	// 每次生成及工具续轮重新读取目录；读取失败或免费活动结束时不使用旧缓存发起生成。
	models, err := a.listModels(ctx, true)
	if err != nil {
		return nil, err
	}
	var selected *adapter.ModelInfo
	for i := range models {
		if models[i].ID == req.Model {
			selected = &models[i]
			break
		}
	}
	if selected == nil {
		return nil, &llm.Failure{Code: "free_model_required", Message: "Kilo 仅允许当前目录确认免费的具体模型: " + req.Model, ClientFixable: true}
	}
	if len(req.Tools) > 0 && !selected.SupportsTools {
		return nil, &llm.Failure{Code: "tools_not_supported", Message: "Kilo 此模型未声明工具调用能力", ClientFixable: true}
	}
	body, err := json.Marshal(openrouter.ChatRequest(req))
	if err != nil {
		return nil, err
	}
	streamCtx, cancel := context.WithTimeout(ctx, a.cfg.StreamTotalTimeout)
	resp, err := a.request(streamCtx, http.MethodPost, "/chat/completions", body)
	if err != nil {
		cancel()
		return nil, err
	}
	if !strings.HasPrefix(strings.ToLower(resp.Header.Get("Content-Type")), "text/event-stream") {
		resp.Body.Close()
		cancel()
		return nil, &llm.Failure{Code: "invalid_stream", Message: "Kilo 返回了非 SSE 生成响应", UpstreamFault: true}
	}
	return openrouter.ChatStream(resp, req.Model, cancel, a.cfg.StreamIdleTimeout, "Kilo", "匿名免费通道"), nil
}

var _ adapter.Adapter = (*Adapter)(nil)
