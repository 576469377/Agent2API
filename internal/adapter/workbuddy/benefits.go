package workbuddy

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/576469377/Agent2API/internal/adapter"
)

func (a *Adapter) AccountBenefits() adapter.AccountBenefits {
	return a.benefits.Snapshot(a.AccountUID())
}
func (a *Adapter) benefitRequest(ctx context.Context, path string, out any) error {
	if err := a.auth.EnsureValid(ctx); err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return fmt.Errorf("WorkBuddy 登录状态需要更新")
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	return a.cli.doJSON(ctx, http.MethodPost, path, map[string]any{}, a.auth.BuildHeaders(headerModeFull), out)
}
func (a *Adapter) RefreshAccountBenefits(ctx context.Context) error {
	return a.benefits.Refresh(ctx, a.AccountUID(), func(ctx context.Context) (adapter.AccountBenefits, error) {
		view := adapter.AccountBenefits{Note: "上游套餐积分汇总，包含已到账赠送积分；余额不改变免费模型路由。"}
		var wire struct {
			Code int `json:"code"`
			Data *struct {
				Packages []struct {
					Total     json.Number `json:"CycleTotalCapacity"`
					Used      json.Number `json:"CycleUsedCapacity"`
					Remaining json.Number `json:"CycleRemainCapacity"`
				} `json:"Packages"`
			} `json:"data"`
		}
		if err := a.benefitRequest(ctx, "/billing/meter/get-user-resource-summary", &wire); err != nil {
			return view, err
		}
		if wire.Code != 0 || wire.Data == nil {
			return view, fmt.Errorf("WorkBuddy 积分查询失败（%d）", wire.Code)
		}
		if wire.Data.Packages == nil {
			return view, fmt.Errorf("WorkBuddy 未返回积分套餐汇总")
		}
		var total, used, left float64
		for _, p := range wire.Data.Packages {
			t, e1 := strconv.ParseFloat(string(p.Total), 64)
			u, e2 := strconv.ParseFloat(string(p.Used), 64)
			l, e3 := strconv.ParseFloat(string(p.Remaining), 64)
			if e1 != nil || e2 != nil || e3 != nil || adapter.BenefitAmount(t) == nil || adapter.BenefitAmount(u) == nil || adapter.BenefitAmount(l) == nil {
				return view, fmt.Errorf("WorkBuddy 积分字段不完整")
			}
			total += t
			used += u
			left += l
		}
		view.Balances = []adapter.AccountBalance{{ID: "credits", Name: "积分余额", Unit: "credits", Total: adapter.BenefitAmount(total), Used: adapter.BenefitAmount(used), Remaining: adapter.BenefitAmount(left)}}
		return view, nil
	})
}
