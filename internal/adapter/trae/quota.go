package trae

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"math"
	"time"

	"github.com/576469377/Agent2API/internal/llm"
)

type quotaResponse struct {
	Credits *bool `json:"is_credits_billing"`
	Dollar  *bool `json:"is_dollar_usage_billing"`
	Trial   struct {
		InTrial *bool `json:"is_in_trial"`
	} `json:"trial_status"`
	Summary struct {
		Total *float64 `json:"total_amount"`
		Used  *float64 `json:"consumed_amount"`
	} `json:"usage_summary"`
	Packs []quotaPack `json:"user_entitlement_pack_list"`
}

type quotaPack struct {
	Status *int `json:"status"`
	Group  *int `json:"group_type"`
	Base   struct {
		Product   *int     `json:"product_type"`
		Charge    *float64 `json:"charge_amount"`
		Endpoint  *int     `json:"available_endpoint"`
		EntStatus *int     `json:"ent_status"`
		Start     *int64   `json:"start_time"`
		End       *int64   `json:"end_time"`
		Quota     struct {
			Credits *float64 `json:"credits_limit"`
		} `json:"quota"`
	} `json:"entitlement_base_info"`
}

// 只认可本网关确认到账的签到奖励；凭证账号、积分包范围和有效期均参与匹配。
func rewardFingerprint(p quotaPack) string {
	raw, _ := json.Marshal(p)
	return fmt.Sprintf("%x", sha256.Sum256(raw))
}

func freeOnlyFailure() *llm.Failure {
	return &llm.Failure{Code: "trae_free_only", Message: "TRAE 仅允许个人 Free、非试用、无按量计费，积分限月赠与已核对到账的签到奖励；其他套餐或积分来源暂未验证", ClientFixable: true}
}

func verifyFreeQuota(raw []byte, now time.Time) (float64, error) {
	return verifyFreeQuotaApproved(raw, now, nil)
}

func verifyFreeQuotaApproved(raw []byte, now time.Time, approved map[string]bool) (float64, error) {
	var q quotaResponse
	if json.Unmarshal(raw, &q) != nil || q.Credits == nil || !*q.Credits || q.Dollar == nil || *q.Dollar || q.Trial.InTrial == nil || *q.Trial.InTrial || q.Summary.Total == nil || q.Summary.Used == nil || len(q.Packs) == 0 {
		return 0, freeOnlyFailure()
	}
	plan, credits := false, 0.0
	for _, p := range q.Packs {
		b := p.Base
		if b.Product == nil || b.Charge == nil || b.Endpoint == nil || b.EntStatus == nil || b.Start == nil || b.End == nil || p.Status == nil {
			return 0, freeOnlyFailure()
		}
		if *b.End <= now.Unix() {
			continue
		}
		if *b.Start > now.Unix() || *b.Charge != 0 || *b.Endpoint != 0 || *b.EntStatus != 0 || *p.Status != 1 {
			return 0, freeOnlyFailure()
		}
		switch *b.Product {
		case 0:
			plan = true
		case 2:
			// group_type=3 是本机真实响应确认的每月登录赠送；未验证来源不算免费。
			if p.Group == nil || (*p.Group != 3 && !approved[rewardFingerprint(p)]) || b.Quota.Credits == nil || !finite(*b.Quota.Credits) || *b.Quota.Credits <= 0 {
				return 0, freeOnlyFailure()
			}
			credits += *b.Quota.Credits
		default:
			return 0, freeOnlyFailure()
		}
	}
	if !plan || !finite(*q.Summary.Total) || !finite(*q.Summary.Used) || *q.Summary.Used < 0 || math.Abs(credits-*q.Summary.Total) > 0.000001 {
		return 0, freeOnlyFailure()
	}
	remaining := credits - *q.Summary.Used
	if remaining <= 0 {
		return 0, &llm.Failure{Code: "trae_free_credits_exhausted", Message: "TRAE 已验证的免费积分已用尽，不切换付费套餐或购买积分", QuotaExhausted: true, RateLimited: true}
	}
	return remaining, nil
}

func finite(n float64) bool { return !math.IsNaN(n) && !math.IsInf(n, 0) }
