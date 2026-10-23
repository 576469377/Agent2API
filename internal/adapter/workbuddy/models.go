package workbuddy

import (
	"context"
	"encoding/json"
	"math"
	"net/http"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/576469377/Agent2API/internal/adapter"
	"github.com/576469377/Agent2API/internal/llm"
)

// defaultModel 是未指定模型时使用的上游 id。
const defaultModel = "auto"

// configModel 是 /v3/config 返回的模型条目。
type configModel struct {
	ID                string          `json:"id"`
	Name              string          `json:"name"`
	DescriptionZh     string          `json:"descriptionZh"`
	DescriptionEn     string          `json:"descriptionEn"`
	MaxInputTokens    int             `json:"maxInputTokens"`
	MaxOutputTokens   int             `json:"maxOutputTokens"`
	MaxAllowedSize    int             `json:"maxAllowedSize"`
	SupportsImages    bool            `json:"supportsImages"`
	SupportsToolCall  bool            `json:"supportsToolCall"`
	SupportsReasoning bool            `json:"supportsReasoning"`
	OnlyReasoning     bool            `json:"onlyReasoning"`
	IsDefault         bool            `json:"isDefault"`
	Vendor            string          `json:"vendor"`
	Tags              []string        `json:"tags"`
	Credits           json.RawMessage `json:"credits"`
}

type configResponse struct {
	Code int    `json:"code"`
	Msg  string `json:"msg"`
	Data struct {
		Endpoint string        `json:"endpoint"`
		Models   []configModel `json:"models"`
	} `json:"data"`
}

// modelCache 缓存上游模型清单。
//
// 模型清单必须动态拉取：实测现网清单与同类项目内置的快照差异极大
// （旧模型下线、新模型上线），硬编码清单必然腐烂。
type modelCache struct {
	mu        sync.RWMutex
	models    []adapter.ModelInfo
	fetchedAt time.Time
	ttl       time.Duration
}

func newModelCache(ttl time.Duration) *modelCache {
	return &modelCache{ttl: ttl}
}

func (c *modelCache) get() []adapter.ModelInfo {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if time.Since(c.fetchedAt) < c.ttl && len(c.models) > 0 {
		return c.models
	}
	return nil
}

func (c *modelCache) set(models []adapter.ModelInfo) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.models = models
	c.fetchedAt = time.Now()
}

// fetchModels 从 /v3/config 拉取模型清单。
func (a *Adapter) fetchModels(ctx context.Context) ([]adapter.ModelInfo, error) {
	if err := a.auth.EnsureValid(ctx); err != nil {
		return nil, err
	}
	models, err := a.ReadModelCatalog(ctx)
	if isUnauthorized(err) {
		if refreshErr := a.auth.Refresh(ctx); refreshErr != nil {
			return nil, refreshErr
		}
		return a.ReadModelCatalog(ctx)
	}
	return models, err
}

// ReadModelCatalog only reads configuration using the existing access token.
// It never generates text, refreshes credentials, or uses an invented catalogue.
// This also allows a separate console snapshot while the gateway stays running.
func (a *Adapter) ReadModelCatalog(ctx context.Context) ([]adapter.ModelInfo, error) {
	var resp configResponse
	headers := a.auth.BuildHeaders(headerModeConfig)
	err := a.cli.doJSON(ctx, http.MethodGet, configPath+"?repos=", nil, headers, &resp)
	if err != nil {
		return nil, err
	}
	if resp.Code != 0 {
		return nil, &llm.Failure{Code: "model_config_failed", Message: resp.Msg, UpstreamFault: true}
	}
	if len(resp.Data.Models) == 0 {
		return nil, &llm.Failure{Code: "empty_model_list", Message: "上游返回的模型清单为空", UpstreamFault: true}
	}
	out := make([]adapter.ModelInfo, 0, len(resp.Data.Models))
	observedAt := time.Now().UTC().Format(time.RFC3339)
	for _, m := range resp.Data.Models {
		if isNonChatModel(m) {
			continue
		}
		desc := m.DescriptionZh
		if desc == "" {
			desc = m.DescriptionEn
		}
		out = append(out, adapter.ModelInfo{
			ID:               m.ID,
			DisplayName:      m.Name,
			Description:      desc,
			ContextTokens:    m.MaxInputTokens,
			MaxOutputTokens:  m.MaxOutputTokens,
			SupportsImages:   m.SupportsImages,
			SupportsTools:    m.SupportsToolCall,
			SupportsThinking: m.SupportsReasoning,
			IsDefault:        m.IsDefault,
			Pricing:          modelPricing(m, observedAt),
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

var creditRatePattern = regexp.MustCompile(`(?i)^x([0-9]+(?:\.[0-9]+)?)(?:\s+credits)?$`)

func modelPricing(m configModel, observedAt string) *adapter.ModelPricing {
	p := &adapter.ModelPricing{Status: "unknown", Source: "upstream_config", ObservedAt: observedAt}
	var label string
	if json.Unmarshal(m.Credits, &label) == nil {
		label = strings.TrimSpace(label)
		// Only a documented multiplier shape is recognised; preserve other
		// display labels without guessing their billing units.
		if len([]rune(label)) <= 128 {
			p.UpstreamLabel = label
		}
		if match := creditRatePattern.FindStringSubmatch(label); match != nil {
			if value, err := strconv.ParseFloat(match[1], 64); err == nil && !math.IsInf(value, 0) && (value > 0 || !strings.ContainsAny(match[1], "123456789")) {
				p.CreditMultiplier = &value
				p.Status = "credits"
				if value == 0 {
					p.Status = "free"
				}
				return p
			}
		}
	}
	// Automatic selection has no fixed rate only when the upstream says so.
	desc := strings.ToLower(m.DescriptionZh + " " + m.DescriptionEn)
	if label == "" && (strings.Contains(desc, "积分倍率随之浮动") || strings.Contains(desc, "credit multiplier varies")) {
		p.Status = "dynamic"
	}
	return p
}

// isNonChatModel 过滤掉代码补全、图像生成等非聊天模型。
//
// 判据：补全类模型的 maxOutputTokens 极小（≤256）且不支持工具调用；
// 另有若干显式前缀（codewise- / nes- / hunyuan-image）。
func isNonChatModel(m configModel) bool {
	for _, tag := range m.Tags {
		switch tag {
		case "text-to-image", "image-to-image", "text-to-video", "image-to-video":
			return true
		}
	}
	switch {
	case strings.HasPrefix(m.ID, "codewise-"),
		strings.HasPrefix(m.ID, "nes-"),
		strings.HasPrefix(m.ID, "hunyuan-image"):
		return true
	case m.MaxOutputTokens > 0 && m.MaxOutputTokens <= 256 && !m.SupportsToolCall:
		return true
	}
	return false
}

// fallbackModels 是拉取失败时的兜底清单（2026-09-15 实测快照）。
func fallbackModels() []adapter.ModelInfo {
	ids := []string{
		"auto", "hy4-preview-f", "hy3", "hy3-x",
		"deepseek-v4.1-flash", "deepseek-v4-pro", "deepseek-v4-flash",
		"glm-5.3", "glm-5.3-flash", "glm-5.2", "glm-5.1", "glm-5v-turbo",
		"kimi-k3-1", "kimi-k2.7", "kimi-k2.6", "minimax-m3",
	}
	out := make([]adapter.ModelInfo, 0, len(ids))
	for _, id := range ids {
		out = append(out, adapter.ModelInfo{
			ID:               id,
			DisplayName:      id,
			SupportsImages:   true,
			SupportsTools:    true,
			SupportsThinking: true,
			IsDefault:        id == defaultModel,
		})
	}
	return out
}

// ListModels 实现 adapter.Adapter。远程拉取失败时降级到内置清单，
// 保证 /v1/models 不会因为上游抖动而不可用。
func (a *Adapter) ListModels(ctx context.Context) ([]adapter.ModelInfo, error) {
	if cached := a.models.get(); cached != nil {
		return cached, nil
	}
	models, err := a.fetchModels(ctx)
	if err != nil {
		if fb := fallbackModels(); a.cfg.Platform != GlobalPlatform && len(fb) > 0 {
			a.logf("warn: 拉取模型清单失败，使用内置兜底清单: %v", err)
			return fb, nil
		}
		return nil, err
	}
	a.models.set(models)
	return models, nil
}

// invalidate 让缓存失效。
func (c *modelCache) invalidate() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.fetchedAt = time.Time{}
	c.models = nil
}
