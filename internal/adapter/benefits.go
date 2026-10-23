package adapter

import (
	"context"
	"math"
	"sync"
	"time"
)

// AccountBalance keeps upstream units separate. A nil number is unknown, not zero.
type AccountBalance struct {
	ID        string     `json:"id"`
	Name      string     `json:"name"`
	Unit      string     `json:"unit"`
	Total     *float64   `json:"total,omitempty"`
	Used      *float64   `json:"used,omitempty"`
	Remaining *float64   `json:"remaining,omitempty"`
	ResetsAt  *time.Time `json:"resets_at,omitempty"`
	Note      string     `json:"note,omitempty"`
}

type AccountBenefits struct {
	Status    string           `json:"status"`
	CheckedAt *time.Time       `json:"checked_at,omitempty"`
	Balances  []AccountBalance `json:"balances"`
	Note      string           `json:"note,omitempty"`
	Error     string           `json:"error,omitempty"`
}

type BenefitsProvider interface {
	AccountBenefits() AccountBenefits
	RefreshAccountBenefits(context.Context) error
}

// CreditBalance and CheckinView are shared by platforms offering daily rewards.
type CreditBalance struct {
	Total        float64    `json:"total"`
	Used         float64    `json:"used"`
	Remaining    float64    `json:"remaining"`
	VerifiedFree bool       `json:"verified_free"`
	CheckedAt    time.Time  `json:"checked_at"`
	ExpiresAt    *time.Time `json:"expires_at,omitempty"`
}

type CheckinView struct {
	Enabled         bool           `json:"enabled"`
	Time            string         `json:"time"`
	Timezone        string         `json:"timezone"`
	Today           string         `json:"today"`
	Available       *bool          `json:"available,omitempty"`
	CheckedIn       *bool          `json:"checked_in,omitempty"`
	DailyCredits    *float64       `json:"daily_credits,omitempty"`
	Balance         *CreditBalance `json:"balance,omitempty"`
	StatusCheckedAt *time.Time     `json:"status_checked_at,omitempty"`
	LastAttemptAt   *time.Time     `json:"last_attempt_at,omitempty"`
	LastSuccessAt   *time.Time     `json:"last_success_at,omitempty"`
	LastResult      string         `json:"last_result"`
	LastError       string         `json:"last_error,omitempty"`
	NextRunAt       *time.Time     `json:"next_run_at,omitempty"`
	Busy            bool           `json:"busy"`
	CanManual       bool           `json:"can_manual"`
}

type CheckinProvider interface {
	CheckinStatus() CheckinView
	RefreshCheckin(context.Context) error
	ConfigureCheckin(bool, string) error
	ClaimCheckin(context.Context) error
}

// BenefitCache is read without network IO by account lists. Failed refreshes
// retain the last measured values and explicitly mark them stale.
type BenefitCache struct {
	mu   sync.RWMutex
	op   sync.Mutex
	key  string
	view AccountBenefits
}

func (c *BenefitCache) Snapshot(key string) AccountBenefits {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if c.key != key || c.view.Status == "" {
		return AccountBenefits{Status: "pending", Balances: []AccountBalance{}}
	}
	v := c.view
	v.Balances = append([]AccountBalance{}, v.Balances...)
	if v.Status == "ready" && v.CheckedAt != nil && time.Since(*v.CheckedAt) > 15*time.Minute {
		v.Status = "stale"
	}
	return v
}

func (c *BenefitCache) Refresh(ctx context.Context, key string, fetch func(context.Context) (AccountBenefits, error)) error {
	c.op.Lock()
	defer c.op.Unlock()
	v, err := fetch(ctx)
	c.mu.Lock()
	defer c.mu.Unlock()
	if err != nil {
		if c.key != key {
			c.view = AccountBenefits{Balances: []AccountBalance{}}
		}
		c.key = key
		c.view.Status = "error"
		if c.view.CheckedAt != nil {
			c.view.Status = "stale"
		}
		c.view.Error = "读取上游积分或额度失败，请稍后刷新或检查登录状态"
		return err
	}
	now := time.Now().UTC()
	v.CheckedAt = &now
	v.Status = "ready"
	v.Error = ""
	if v.Balances == nil {
		v.Balances = []AccountBalance{}
	}
	c.key, c.view = key, v
	return nil
}

func BenefitAmount(v float64) *float64 {
	if math.IsNaN(v) || math.IsInf(v, 0) || v < 0 {
		return nil
	}
	return &v
}

func BenefitOptional(v *float64) *float64 {
	if v == nil {
		return nil
	}
	return BenefitAmount(*v)
}
