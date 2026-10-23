package openrouter

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"net/http"

	"github.com/576469377/Agent2API/internal/adapter"
)

func (a *Adapter) benefitKey() string { return fmt.Sprintf("%x", sha256.Sum256([]byte(a.key()))) }
func (a *Adapter) AccountBenefits() adapter.AccountBenefits {
	return a.benefits.Snapshot(a.benefitKey())
}
func (a *Adapter) RefreshAccountBenefits(ctx context.Context) error {
	return a.benefits.Refresh(ctx, a.benefitKey(), func(ctx context.Context) (adapter.AccountBenefits, error) {
		view := adapter.AccountBenefits{Note: "美元余额与免费模型每日请求数独立；此查询不启用付费模型。"}
		if a.key() == "" {
			return view, fmt.Errorf("OpenRouter API Key 未配置")
		}
		resp, err := a.request(ctx, http.MethodGet, "/credits", nil)
		if err == nil {
			var wire struct {
				Data struct {
					Total *float64 `json:"total_credits"`
					Used  *float64 `json:"total_usage"`
				} `json:"data"`
			}
			err = json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&wire)
			resp.Body.Close()
			if err == nil && wire.Data.Total != nil && wire.Data.Used != nil {
				t, u := adapter.BenefitAmount(*wire.Data.Total), adapter.BenefitAmount(*wire.Data.Used)
				if t != nil && u != nil {
					view.Balances = append(view.Balances, adapter.AccountBalance{ID: "account-credits", Name: "账户余额", Unit: "USD", Total: t, Used: u, Remaining: adapter.BenefitAmount(*t - *u)})
				}
			}
		}
		// A normal inference key may not have permission to query account-wide credits.
		resp, keyErr := a.request(ctx, http.MethodGet, "/key", nil)
		if keyErr != nil {
			if len(view.Balances) > 0 {
				return view, nil
			}
			return view, keyErr
		}
		defer resp.Body.Close()
		var key struct {
			Data struct {
				Limit     *float64 `json:"limit"`
				Remaining *float64 `json:"limit_remaining"`
				Usage     *float64 `json:"usage"`
				Daily     *float64 `json:"usage_daily"`
			} `json:"data"`
		}
		if err = json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&key); err != nil {
			if len(view.Balances) > 0 {
				return view, nil
			}
			return view, err
		}
		if key.Data.Limit != nil || key.Data.Remaining != nil {
			view.Balances = append(view.Balances, adapter.AccountBalance{ID: "key-budget", Name: "API Key 预算", Unit: "USD", Total: adapter.BenefitOptional(key.Data.Limit), Used: adapter.BenefitOptional(key.Data.Usage), Remaining: adapter.BenefitOptional(key.Data.Remaining)})
		}
		if key.Data.Daily != nil {
			view.Balances = append(view.Balances, adapter.AccountBalance{ID: "daily-spend", Name: "今日消耗", Unit: "USD", Used: adapter.BenefitAmount(*key.Data.Daily), Note: "美元消耗，不是免费请求次数"})
		}
		if len(view.Balances) == 0 {
			view.Note = "上游未提供可读取的余额或 API Key 预算；免费请求余量未知。"
		}
		return view, nil
	})
}
