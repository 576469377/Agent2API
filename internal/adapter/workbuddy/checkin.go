package workbuddy

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/576469377/Agent2API/internal/adapter"
)

var rewardZone = time.FixedZone("Asia/Shanghai", 8*3600)

type checkinRecord struct {
	Version  int                 `json:"version"`
	Account  string              `json:"account_hash"`
	View     adapter.CheckinView `json:"checkin"`
	Attempts int                 `json:"attempts"`
	RetryAt  time.Time           `json:"retry_at,omitempty"`
}
type accountCheckin struct {
	a     *Adapter
	mu    sync.Mutex
	op    sync.Mutex
	once  sync.Once
	path  string
	state checkinRecord
	busy  bool
}

func newAccountCheckin(a *Adapter) *accountCheckin {
	c := &accountCheckin{a: a, path: a.cfg.CredentialPath + ".checkin-state", state: checkinRecord{Version: 1, View: adapter.CheckinView{Enabled: true, Time: "09:00", Timezone: "Asia/Shanghai", LastResult: "pending"}}}
	if raw, err := os.ReadFile(c.path); err == nil {
		var saved checkinRecord
		if len(raw) <= 128*1024 && json.Unmarshal(raw, &saved) == nil && saved.Version == 1 && checkinTime(saved.View.Time) {
			c.state = saved
		} else {
			c.state.View.Enabled = false
			c.state.View.LastError = "签到设置无法读取，请重新保存设置"
			c.state.View.LastResult = "failed"
		}
	} else if !os.IsNotExist(err) {
		c.state.View.Enabled = false
		c.state.View.LastError = "签到设置无法读取，请检查文件权限"
		c.state.View.LastResult = "failed"
	}
	return c
}
func checkinTime(value string) bool {
	t, e := time.Parse("15:04", value)
	return e == nil && t.Format("15:04") == value
}
func (c *accountCheckin) resetLocked(now time.Time) {
	key := fmt.Sprintf("%x", sha256.Sum256([]byte(c.a.Name()+":"+c.a.AccountUID())))
	if c.state.Account != key {
		v := c.state.View
		settingsError := c.state.Account == "" && v.LastResult == "failed" && v.LastError != ""
		c.state = checkinRecord{Version: 1, Account: key, View: adapter.CheckinView{Enabled: v.Enabled, Time: v.Time, Timezone: "Asia/Shanghai", LastResult: "pending"}}
		if settingsError {
			c.state.View.LastResult = v.LastResult
			c.state.View.LastError = v.LastError
		}
	}
	day := now.In(rewardZone).Format("2006-01-02")
	if c.state.View.Today != day {
		c.state.View.Today = day
		c.state.View.CheckedIn = nil
		c.state.View.Available = nil
		c.state.View.DailyCredits = nil
		c.state.View.StatusCheckedAt = nil
		c.state.Attempts = 0
		c.state.RetryAt = time.Time{}
	}
}
func (c *accountCheckin) saveLocked() error {
	raw, err := json.MarshalIndent(c.state, "", "  ")
	if err != nil {
		return fmt.Errorf("签到设置无法编码")
	}
	f, err := os.CreateTemp(filepath.Dir(c.path), ".workbuddy-checkin-*.tmp")
	if err != nil {
		return fmt.Errorf("签到设置无法保存，请检查目录权限")
	}
	defer os.Remove(f.Name())
	if err = f.Chmod(0o600); err == nil {
		_, err = f.Write(raw)
	}
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err != nil || closeErr != nil {
		return fmt.Errorf("签到设置无法写入")
	}
	if os.Rename(f.Name(), c.path) != nil {
		return fmt.Errorf("签到设置无法更新")
	}
	return nil
}
func (a *Adapter) CheckinStatus() adapter.CheckinView {
	c := a.checkins
	if c == nil {
		return adapter.CheckinView{Timezone: "Asia/Shanghai", Time: "09:00"}
	}
	now := time.Now()
	c.mu.Lock()
	defer c.mu.Unlock()
	c.resetLocked(now)
	v := c.state.View
	v.Busy = c.busy
	v.CanManual = !v.Busy && c.state.Attempts < 6 && (v.CheckedIn == nil || !*v.CheckedIn) && (v.Available == nil || *v.Available) && !now.Before(c.state.RetryAt)
	if v.Enabled {
		t, _ := time.Parse("15:04", v.Time)
		local := now.In(rewardZone)
		next := time.Date(local.Year(), local.Month(), local.Day(), t.Hour(), t.Minute(), 0, 0, rewardZone)
		if (v.CheckedIn != nil && *v.CheckedIn) || (v.Available != nil && !*v.Available) || c.state.Attempts >= 6 {
			next = next.AddDate(0, 0, 1)
		} else if !next.After(now) {
			next = now
			if c.state.RetryAt.After(next) {
				next = c.state.RetryAt
			}
		}
		v.NextRunAt = &next
	}
	return v
}
func (a *Adapter) ConfigureCheckin(enabled bool, at string) error {
	if !checkinTime(at) {
		return fmt.Errorf("签到时间应为 HH:MM（北京时间）")
	}
	c := a.checkins
	c.mu.Lock()
	defer c.mu.Unlock()
	before := c.state
	c.state.View.Enabled = enabled
	c.state.View.Time = at
	if err := c.saveLocked(); err != nil {
		c.state = before
		return err
	}
	return nil
}

type rewardStatus struct {
	Code int `json:"code"`
	Data *struct {
		Active  *bool    `json:"active"`
		Checked *bool    `json:"today_checked_in"`
		Daily   *float64 `json:"daily_credit"`
		Today   *float64 `json:"today_credit"`
	} `json:"data"`
}

func (a *Adapter) rewardStatus(ctx context.Context) (rewardStatus, error) {
	var wire rewardStatus
	if err := a.benefitRequest(ctx, "/v2/billing/meter/checkin-activity-status", &wire); err != nil {
		return wire, err
	}
	if wire.Code != 0 || wire.Data == nil || wire.Data.Active == nil || wire.Data.Checked == nil {
		return wire, fmt.Errorf("上游未提供可确认的签到状态（%d）", wire.Code)
	}
	return wire, nil
}
func (a *Adapter) RefreshCheckin(ctx context.Context) error { return a.checkins.run(ctx, false) }
func (a *Adapter) ClaimCheckin(ctx context.Context) error   { return a.checkins.run(ctx, true) }

func (c *accountCheckin) run(ctx context.Context, claim bool) (err error) {
	c.op.Lock()
	defer c.op.Unlock()
	if ctx.Err() != nil {
		return ctx.Err()
	}
	now := time.Now()
	c.mu.Lock()
	c.resetLocked(now)
	c.busy = true
	c.mu.Unlock()
	defer func() {
		c.mu.Lock()
		c.busy = false
		if err != nil {
			c.state.View.LastResult = "failed"
			c.state.View.LastError = "签到状态读取或领取失败，请稍后刷新或检查登录状态"
			c.state.RetryAt = time.Now().Add(15 * time.Minute)
		}
		saveErr := c.saveLocked()
		c.mu.Unlock()
		if err == nil {
			err = saveErr
		}
	}()
	status, err := c.a.rewardStatus(ctx)
	if err != nil {
		return err
	}
	c.mu.Lock()
	v := &c.state.View
	v.Available = status.Data.Active
	v.CheckedIn = status.Data.Checked
	v.DailyCredits = status.Data.Today
	if v.DailyCredits == nil {
		v.DailyCredits = status.Data.Daily
	}
	if v.DailyCredits != nil {
		v.DailyCredits = adapter.BenefitAmount(*v.DailyCredits)
	}
	stamp := time.Now().UTC()
	v.StatusCheckedAt = &stamp
	v.LastError = ""
	if *v.CheckedIn {
		v.LastResult = "already_checked"
		c.state.RetryAt = time.Time{}
		c.mu.Unlock()
		return nil
	}
	if !*v.Available {
		v.LastResult = "unavailable"
		c.mu.Unlock()
		return nil
	}
	if !claim {
		c.mu.Unlock()
		return nil
	}
	if now.Before(c.state.RetryAt) || c.state.Attempts >= 6 {
		c.mu.Unlock()
		return fmt.Errorf("签到正在等待重试，请稍后检查")
	}
	c.state.Attempts++
	v.LastAttemptAt = &stamp
	v.LastResult = "pending"
	c.state.RetryAt = now.Add(15 * time.Minute)
	if err = c.saveLocked(); err != nil {
		c.mu.Unlock()
		return err
	}
	c.mu.Unlock()
	// A lost claim response is followed by a status check, never a blind replay.
	var result struct {
		Code int `json:"code"`
	}
	claimErr := c.a.benefitRequest(ctx, "/v2/billing/meter/daily-checkin", &result)
	verified, verifyErr := c.a.rewardStatus(ctx)
	if verifyErr == nil && *verified.Data.Checked {
		c.mu.Lock()
		c.state.View.CheckedIn = verified.Data.Checked
		c.state.View.LastResult = "success"
		success := time.Now().UTC()
		c.state.View.LastSuccessAt = &success
		c.state.RetryAt = time.Time{}
		c.mu.Unlock()
		_ = c.a.RefreshAccountBenefits(ctx)
		return nil
	}
	if claimErr != nil {
		return claimErr
	}
	return fmt.Errorf("签到领取尚未确认（%d），将先检查状态再重试", result.Code)
}

func (a *Adapter) StartBackground(ctx context.Context) {
	c := a.checkins
	if c == nil {
		return
	}
	c.once.Do(func() {
		go func() {
			ticker := time.NewTicker(30 * time.Second)
			defer ticker.Stop()
			for {
				if ctx.Err() != nil {
					return
				}
				v := a.CheckinStatus()
				if v.Enabled && v.NextRunAt != nil && !v.NextRunAt.After(time.Now()) && !v.Busy {
					_ = a.ClaimCheckin(ctx)
				} else if v.StatusCheckedAt == nil || time.Since(*v.StatusCheckedAt) > 15*time.Minute {
					c.mu.Lock()
					retry := c.state.RetryAt
					c.mu.Unlock()
					if !time.Now().Before(retry) {
						_ = a.RefreshCheckin(ctx)
					}
				}
				select {
				case <-ctx.Done():
					return
				case <-ticker.C:
				}
			}
		}()
	})
}
