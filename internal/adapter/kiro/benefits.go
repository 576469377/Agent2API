package kiro

import (
	"context"
	"errors"
	"time"

	"github.com/576469377/Agent2API/internal/adapter"
	"github.com/576469377/Agent2API/internal/llm"
)

func (a *Adapter) AccountBenefits() adapter.AccountBenefits { return a.benefits.Snapshot(Platform) }
func (a *Adapter) RefreshAccountBenefits(ctx context.Context) error {
	_, err := a.ListModels(ctx)
	if freeCreditsExhausted(err) {
		return nil // The measured balance is valid even though generation is unavailable.
	}
	return err
}

func freeCreditsExhausted(err error) bool {
	var failure *llm.Failure
	return errors.As(err, &failure) && failure.Code == "kiro_free_credits_exhausted"
}

func (a *Adapter) recordUsage(u usageData) {
	_ = a.benefits.Refresh(context.Background(), Platform, func(context.Context) (adapter.AccountBenefits, error) {
		remaining, err := verifyFree(u)
		if err != nil && !freeCreditsExhausted(err) {
			return adapter.AccountBenefits{}, err
		}
		b := u.Breakdowns[0]
		balance := adapter.AccountBalance{ID: "monthly-free", Name: "Kiro Free 月度额度", Unit: "credits", Total: adapter.BenefitOptional(b.Limit), Used: adapter.BenefitOptional(b.Used), Remaining: adapter.BenefitAmount(remaining), Note: "积分不是请求次数或 Token；不购买追加额度。"}
		if reset, e := time.Parse("2006-01-02", u.Reset); e == nil {
			balance.ResetsAt = &reset
		}
		return adapter.AccountBenefits{Balances: []adapter.AccountBalance{balance}, Note: "官方 CLI 核验的个人 Free 月度积分；签到与追加额度不接入。"}, nil
	})
}
