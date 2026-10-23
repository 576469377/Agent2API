package cursor

import (
	"context"
	"fmt"
	"time"

	"github.com/576469377/Agent2API/internal/adapter"
)

func (a *Adapter) AccountBenefits() adapter.AccountBenefits { return a.benefits.Snapshot("cursor") }
func (a *Adapter) RefreshAccountBenefits(ctx context.Context) error {
	return a.benefits.Refresh(ctx, "cursor", func(ctx context.Context) (adapter.AccountBenefits, error) {
		view := adapter.AccountBenefits{Note: "套餐用量按上游美分换算美元；免费额度未返回时保持未知，不启用按量付费。"}
		cred, err := a.load(ctx, a.cfg.CredentialPath)
		if err != nil {
			return view, err
		}
		var wire struct {
			Enabled *bool  `json:"enabled"`
			End     string `json:"billingCycleEnd"`
			Plan    *struct {
				Used      *float64 `json:"totalSpend"`
				Limit     *float64 `json:"limit"`
				Remaining *float64 `json:"remaining"`
			} `json:"planUsage"`
		}
		if err = a.unary(ctx, cred, "/aiserver.v1.DashboardService/GetCurrentPeriodUsage", struct{}{}, &wire); err != nil {
			return view, err
		}
		if wire.Plan == nil {
			view.Note = "当前账号未返回可量化的 Free 剩余额度；签到未提供。"
			return view, nil
		}
		money := func(v *float64) *float64 {
			if v == nil {
				return nil
			}
			return adapter.BenefitAmount(*v / 100)
		}
		balance := adapter.AccountBalance{ID: "plan-usage", Name: "套餐额度", Unit: "USD", Used: money(wire.Plan.Used), Total: money(wire.Plan.Limit), Remaining: money(wire.Plan.Remaining)}
		if wire.End != "" {
			var n int64
			if _, err = fmt.Sscan(wire.End, &n); err == nil && n > 0 {
				if n < 1e12 {
					n *= 1000
				}
				reset := time.UnixMilli(n).UTC()
				balance.ResetsAt = &reset
			}
		}
		view.Balances = []adapter.AccountBalance{balance}
		return view, nil
	})
}
