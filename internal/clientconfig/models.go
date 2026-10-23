// Package clientconfig 为本机客户端提供一致的免费模型筛选与配置写入。
package clientconfig

import (
	"math"
	"time"

	"github.com/576469377/Agent2API/internal/adapter"
)

type Model struct {
	adapter.ModelInfo
	Platform     string
	RoutingID    string
	DefaultRoute bool
}

func fresh(stamp string, now time.Time) bool {
	t, err := time.Parse(time.RFC3339Nano, stamp)
	return err == nil && now.Sub(t) >= -time.Minute && now.Sub(t) <= 5*time.Minute
}

// Eligible 依赖刚确认的价格和免费权益，不能按模型名或历史快照推断免费。
func Eligible(m Model, now time.Time) bool {
	p := m.Pricing
	unknownKiroOutput := m.Platform == "kiro" && m.MaxOutputTokens == 0
	if m.ID == "" || m.ContextTokens <= 0 || (m.MaxOutputTokens <= 0 && !unknownKiroOutput) || p == nil || !fresh(p.ObservedAt, now) {
		return false
	}
	if m.RoutingID != "" && m.RoutingID != m.Platform+"/"+m.ID {
		return false
	}
	if m.Platform == "trae-cn" || m.Platform == "kiro" {
		pricingSource, accessSource := "trae_account_catalog", "trae_free_plan"
		if m.Platform == "kiro" {
			pricingSource, accessSource = "kiro_cli_catalog", "kiro_cli_free_plan"
		}
		a := m.Access
		return p.Status == "credits" && p.Source == pricingSource && p.CreditMultiplier != nil &&
			*p.CreditMultiplier > 0 && !math.IsInf(*p.CreditMultiplier, 0) && !math.IsNaN(*p.CreditMultiplier) &&
			a != nil && a.Source == accessSource && a.Basis == "recurring_free_allowance" && a.Plan == "free" &&
			!a.OverageEnabled && a.RemainingCredits > 0 && !math.IsInf(a.RemainingCredits, 0) && !math.IsNaN(a.RemainingCredits) && fresh(a.CheckedAt, now)
	}
	if p.Status != "free" {
		return false
	}
	switch m.Platform {
	case "workbuddy", "workbuddy-global":
		return p.Source == "upstream_config" && p.CreditMultiplier != nil && *p.CreditMultiplier == 0
	case "openrouter":
		return p.Source == "openrouter_models"
	case "cursor":
		return p.Source == "cursor_free_plan"
	default:
		return false
	}
}

func Route(m Model) string { return m.Platform + "/" + m.ID }

func OutputLimit(m Model) int {
	// Kiro does not publish an output ceiling in its account catalogue. This is
	// a client-side recommended budget only; ACP does not enforce max_tokens.
	if m.Platform == "kiro" && m.MaxOutputTokens == 0 {
		return 8192
	}
	if m.Platform == "openrouter" {
		return min(m.MaxOutputTokens, 8192)
	}
	return m.MaxOutputTokens
}
