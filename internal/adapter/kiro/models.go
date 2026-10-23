package kiro

import (
	"encoding/json"
	"math"
	"strings"
	"time"

	"github.com/576469377/Agent2API/internal/adapter"
	"github.com/576469377/Agent2API/internal/llm"
)

var verifiedModels = map[string]bool{"deepseek-3.2": true, "minimax-m2.5": true, "minimax-m2.1": true, "glm-5": true, "qwen3-coder-next": true}

func finite(v float64) bool { return !math.IsInf(v, 0) && !math.IsNaN(v) }
func verifyFree(u usageData) (float64, error) {
	fail := func() (float64, error) {
		return 0, clientFailure("kiro_free_only", "Kiro 仅接入已确认的个人 Free 月度额度；付费、企业、试用、追加积分或无法核验的套餐不接入")
	}
	if u.Plan == nil || strings.TrimSpace(*u.Plan) != "KIRO FREE" || u.Overages == nil || *u.Overages || u.Enterprise == nil || *u.Enterprise || u.OverageCapable == nil || *u.OverageCapable || u.Bonus == nil || len(u.Bonus) > 0 || u.AddOns == nil || len(u.AddOns) > 0 || len(u.Breakdowns) != 1 {
		return fail()
	}
	b := u.Breakdowns[0]
	if b.Resource != "CREDIT" || b.Used == nil || b.Limit == nil || b.Overages == nil || b.Charges == nil || b.HasLimit == nil || !*b.HasLimit || !finite(*b.Used) || !finite(*b.Limit) || *b.Limit <= 0 || *b.Used < 0 || *b.Overages != 0 || *b.Charges != 0 {
		return fail()
	}
	remaining := *b.Limit - *b.Used
	if remaining <= 0 {
		return 0, &llm.Failure{Code: "kiro_free_credits_exhausted", Message: "Kiro 免费积分已用尽，请等待下个额度周期；不会购买追加额度或切换付费套餐", QuotaExhausted: true, RateLimited: true}
	}
	return remaining, nil
}

func decodeModels(raw []byte, remaining float64, now time.Time) ([]adapter.ModelInfo, error) {
	var catalog struct {
		Models []struct {
			ID      string   `json:"model_id"`
			Name    string   `json:"model_name"`
			Context int      `json:"context_window_tokens"`
			Rate    *float64 `json:"rate_multiplier"`
			Unit    string   `json:"rate_unit"`
		} `json:"models"`
		Default string `json:"default_model"`
	}
	if json.Unmarshal(raw, &catalog) != nil {
		return nil, upstreamFailure("kiro_catalog_invalid", "Kiro CLI 模型目录格式无效")
	}
	out := []adapter.ModelInfo{}
	seen := map[string]bool{}
	stamp := now.UTC().Format(time.RFC3339)
	for _, m := range catalog.Models {
		if !verifiedModels[m.ID] || seen[m.ID] || m.Context <= 0 || m.Rate == nil || !finite(*m.Rate) || *m.Rate <= 0 || !strings.EqualFold(m.Unit, "Credit") {
			continue
		}
		seen[m.ID] = true
		rate := *m.Rate
		out = append(out, adapter.ModelInfo{ID: m.ID, DisplayName: m.Name, ContextTokens: m.Context, MaxOutputTokens: 0, SupportsTools: true, IsDefault: m.ID == catalog.Default,
			Description: "官方 Kiro CLI / ACP；仅个人 Free 月度额度。上下文来自账号目录；输出上限和 Token 用量未提供，不支持图片、JSON 模式、stop、输出预算与思考控制。",
			Pricing:     &adapter.ModelPricing{Status: "credits", CreditMultiplier: &rate, Source: "kiro_cli_catalog", ObservedAt: stamp, UpstreamLabel: "真实积分倍率，由已核验的 Kiro Free 月度额度支付"},
			Access:      &adapter.ModelAccess{Basis: "recurring_free_allowance", Plan: "free", Source: "kiro_cli_free_plan", RemainingCredits: remaining, CheckedAt: stamp}})
	}
	if len(out) == 0 {
		return nil, clientFailure("kiro_no_verified_models", "Kiro 当前账号目录没有已验证的可自选免费模型")
	}
	return out, nil
}
