// Package openrouter connects the gateway to OpenRouter's free text models.
package openrouter

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"net/url"
	"os"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/576469377/Agent2API/internal/adapter"
	"github.com/576469377/Agent2API/internal/llm"
)

const Platform = "openrouter"
const DefaultBaseURL = "https://openrouter.ai/api/v1"

type Config struct {
	BaseURL, APIKey, APIKeyEnv                                           string
	RequestTimeout, StreamIdleTimeout, StreamTotalTimeout, ModelCacheTTL time.Duration
}

type Adapter struct {
	benefits adapter.BenefitCache
	cfg      Config
	client   *http.Client
	mu       sync.Mutex
	fetchMu  sync.Mutex
	models   []adapter.ModelInfo
	expires  time.Time
}

func New(cfg Config) (*Adapter, error) {
	if cfg.BaseURL == "" {
		cfg.BaseURL = DefaultBaseURL
	}
	cfg.BaseURL = strings.TrimRight(cfg.BaseURL, "/")
	u, err := url.Parse(cfg.BaseURL)
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return nil, fmt.Errorf("invalid OpenRouter base_url")
	}
	if cfg.APIKeyEnv == "" {
		cfg.APIKeyEnv = "OPENROUTER_API_KEY"
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
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.ResponseHeaderTimeout = cfg.RequestTimeout
	return &Adapter{cfg: cfg, client: &http.Client{Transport: transport}}, nil
}

func (a *Adapter) Name() string { return Platform }

func (a *Adapter) key() string {
	if key := strings.TrimSpace(os.Getenv(a.cfg.APIKeyEnv)); key != "" {
		return key
	}
	return strings.TrimSpace(a.cfg.APIKey)
}

func (a *Adapter) Describe() adapter.Description {
	d := adapter.Description{ID: Platform, Name: "OpenRouter", BaseURL: a.cfg.BaseURL, Status: "active", Account: "API Key", Notes: "仅允许目录确认的免费模型"}
	if a.key() == "" {
		d.Status = "unauthenticated"
		d.Notes = "请设置 " + a.cfg.APIKeyEnv + "，然后重启网关"
	}
	a.mu.Lock()
	d.Models = len(a.models)
	a.mu.Unlock()
	return d
}

func (a *Adapter) request(ctx context.Context, method, path string, body []byte) (*http.Response, error) {
	finish := func(_ int, err error) error { return err }
	if method == http.MethodPost && path == "/chat/completions" {
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
	if key := a.key(); key != "" {
		req.Header.Set("Authorization", "Bearer "+key)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-OpenRouter-Title", "Agent2API")
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
		return nil, llm.Wrap(err)
	}
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return resp, nil
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 64*1024))
	var envelope struct {
		Error *apiError `json:"error"`
	}
	_ = json.Unmarshal(raw, &envelope)
	message := http.StatusText(resp.StatusCode)
	if envelope.Error != nil {
		message = envelope.Error.summary(message)
	}
	f := failure(resp.StatusCode, message)
	if seconds, err := strconv.Atoi(resp.Header.Get("Retry-After")); err == nil && seconds > 0 {
		f.RetryAfterSeconds = seconds
	} else if until, err := http.ParseTime(resp.Header.Get("Retry-After")); err == nil {
		f.RetryAfterSeconds = max(0, int(time.Until(until).Seconds())+1)
	}
	llm.NoteAttemptFailure(ctx, f)
	return nil, f
}

type apiError struct {
	Code     json.RawMessage `json:"code"`
	Message  string          `json:"message"`
	Metadata struct {
		Provider string          `json:"provider_name"`
		Raw      json.RawMessage `json:"raw"`
	} `json:"metadata"`
}

func failure(status int, message string) *llm.Failure {
	f := &llm.Failure{Code: fmt.Sprintf("http_%d", status), Message: "OpenRouter: " + message}
	switch {
	case status == 401 || status == 403:
		f.Unauthorized = true
	case status == 402:
		f.QuotaExhausted = true
		f.RateLimited = true
	case status == 429:
		f.RateLimited = true
		if strings.Contains(strings.ToLower(message), "free-models-per-day") {
			f.Message = "OpenRouter 今日免费调用额度已用尽，同一账号的免费模型共享额度；请等待每日额度恢复，或切换 Cursor / WorkBuddy 的免费模型"
		}
	case status >= 500:
		f.UpstreamFault = true
	default:
		f.ClientFixable = true
	}
	return f
}

type catalogModel struct {
	ID            string                     `json:"id"`
	Name          string                     `json:"name"`
	Description   string                     `json:"description"`
	ContextLength int                        `json:"context_length"`
	Pricing       map[string]json.RawMessage `json:"pricing"`
	Architecture  struct {
		Input  []string `json:"input_modalities"`
		Output []string `json:"output_modalities"`
	} `json:"architecture"`
	TopProvider struct {
		MaxCompletionTokens int `json:"max_completion_tokens"`
	} `json:"top_provider"`
	SupportedParameters []string `json:"supported_parameters"`
}

// Every published cost must be explicitly zero. Missing, malformed, negative
// (dynamic router) and nonzero prices are never treated as free.
func freePricing(pricing map[string]json.RawMessage) bool {
	if len(pricing["prompt"]) == 0 || len(pricing["completion"]) == 0 {
		return false
	}
	for _, raw := range pricing {
		value := string(raw)
		if len(raw) > 0 && raw[0] == '"' {
			if json.Unmarshal(raw, &value) != nil {
				return false
			}
		}
		number, ok := new(big.Rat).SetString(value)
		if !ok || number.Sign() != 0 {
			return false
		}
	}
	return true
}

func (a *Adapter) ListModels(ctx context.Context) ([]adapter.ModelInfo, error) {
	a.fetchMu.Lock()
	defer a.fetchMu.Unlock()
	a.mu.Lock()
	if time.Now().Before(a.expires) {
		models := slices.Clone(a.models)
		a.mu.Unlock()
		return models, nil
	}
	a.mu.Unlock()
	ctx, cancel := context.WithTimeout(ctx, a.cfg.RequestTimeout)
	defer cancel()
	resp, err := a.request(ctx, http.MethodGet, "/models", nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var envelope struct {
		Data  []catalogModel `json:"data"`
		Error *apiError      `json:"error"`
	}
	if err = json.NewDecoder(io.LimitReader(resp.Body, 16*1024*1024)).Decode(&envelope); err != nil {
		return nil, fmt.Errorf("OpenRouter model catalogue: %w", err)
	}
	if envelope.Error != nil || envelope.Data == nil {
		return nil, fmt.Errorf("OpenRouter returned no model catalogue")
	}
	models := []adapter.ModelInfo{}
	for _, m := range envelope.Data {
		if m.ID == "" || !freePricing(m.Pricing) || (len(m.Architecture.Output) > 0 && (len(m.Architecture.Output) != 1 || m.Architecture.Output[0] != "text")) {
			continue
		}
		models = append(models, adapter.ModelInfo{
			ID: m.ID, DisplayName: m.Name, Description: m.Description, ContextTokens: m.ContextLength, MaxOutputTokens: m.TopProvider.MaxCompletionTokens,
			SupportsImages: slices.Contains(m.Architecture.Input, "image"), SupportsTools: slices.Contains(m.SupportedParameters, "tools"),
			SupportsThinking: slices.Contains(m.SupportedParameters, "reasoning") || slices.Contains(m.SupportedParameters, "reasoning_effort"), IsDefault: m.ID == "openrouter/free",
			Pricing: &adapter.ModelPricing{Status: "free", Source: "openrouter_models", ObservedAt: time.Now().UTC().Format(time.RFC3339)},
		})
	}
	a.mu.Lock()
	a.models = models
	a.expires = time.Now().Add(a.cfg.ModelCacheTTL)
	a.mu.Unlock()
	return slices.Clone(models), nil
}

func (a *Adapter) InvalidateModels() { a.mu.Lock(); a.expires = time.Time{}; a.mu.Unlock() }

func (a *Adapter) Stream(ctx context.Context, req llm.RequestMessages) (llm.ResponseStream, error) {
	models, err := a.ListModels(ctx)
	if err != nil {
		return nil, err
	}
	if req.Model == "" {
		req.Model = "openrouter/free"
	}
	if !slices.ContainsFunc(models, func(m adapter.ModelInfo) bool { return m.ID == req.Model }) {
		return nil, &llm.Failure{Code: "free_model_required", Message: "OpenRouter 仅允许当前目录中确认免费的模型: " + req.Model, ClientFixable: true}
	}
	if a.key() == "" {
		return nil, &llm.Failure{Code: "no_api_key", Message: "OpenRouter: 请设置 " + a.cfg.APIKeyEnv, Unauthorized: true}
	}
	body, err := json.Marshal(buildRequest(req))
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
		return nil, &llm.Failure{Code: "invalid_stream", Message: "OpenRouter returned a non-SSE response", UpstreamFault: true}
	}
	return newStream(resp, req.Model, cancel, a.cfg.StreamIdleTimeout), nil
}

var _ adapter.Adapter = (*Adapter)(nil)
