package trae

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"time"

	"github.com/576469377/Agent2API/internal/adapter"
	"github.com/576469377/Agent2API/internal/llm"
)

var checkinZone = time.FixedZone("Asia/Shanghai", 8*60*60)

type CreditBalance = adapter.CreditBalance
type CheckinView = adapter.CheckinView

func (a *Adapter) AccountBenefits() adapter.AccountBenefits {
	v := a.CheckinStatus()
	out := adapter.AccountBenefits{Status: "pending", Balances: []adapter.AccountBalance{}}
	if v.Balance != nil {
		b := v.Balance
		out.Status = "ready"
		out.CheckedAt = &b.CheckedAt
		out.Balances = append(out.Balances, adapter.AccountBalance{ID: "credits", Name: "积分余额", Unit: "credits", Total: adapter.BenefitAmount(b.Total), Used: adapter.BenefitAmount(b.Used), Remaining: adapter.BenefitAmount(b.Remaining), ResetsAt: b.ExpiresAt})
	}
	if v.LastError != "" {
		out.Error = v.LastError
		if out.CheckedAt != nil {
			out.Status = "stale"
		} else {
			out.Status = "error"
		}
	}
	return out
}
func (a *Adapter) RefreshAccountBenefits(ctx context.Context) error { return a.RefreshCheckin(ctx) }

type pendingCheckin struct {
	Day         string          `json:"day"`
	Expected    float64         `json:"expected"`
	TotalBefore float64         `json:"total_before"`
	Before      map[string]bool `json:"before"`
}

type checkinDisk struct {
	Version         int             `json:"version"`
	Enabled         bool            `json:"enabled"`
	Time            string          `json:"time"`
	Account         string          `json:"account_hash,omitempty"`
	Day             string          `json:"day,omitempty"`
	Checked         bool            `json:"checked"`
	Available       *bool           `json:"available,omitempty"`
	DailyCredits    *float64        `json:"daily_credits,omitempty"`
	Balance         *CreditBalance  `json:"balance,omitempty"`
	StatusCheckedAt *time.Time      `json:"status_checked_at,omitempty"`
	LastAttemptAt   *time.Time      `json:"last_attempt_at,omitempty"`
	LastSuccessAt   *time.Time      `json:"last_success_at,omitempty"`
	LastResult      string          `json:"last_result"`
	LastError       string          `json:"last_error,omitempty"`
	Attempts        int             `json:"attempts"`
	RetryAt         *time.Time      `json:"retry_at,omitempty"`
	Rewards         map[string]bool `json:"verified_rewards,omitempty"`
	Pending         *pendingCheckin `json:"pending,omitempty"`
}

type checkinController struct {
	a     *Adapter
	mu    sync.Mutex
	op    sync.Mutex
	once  sync.Once
	wake  chan struct{}
	state checkinDisk
	path  string
	busy  bool
}

func validCheckinTime(value string) bool {
	if len(value) != 5 || value[2] != ':' {
		return false
	}
	h, e1 := strconv.Atoi(value[:2])
	m, e2 := strconv.Atoi(value[3:])
	return e1 == nil && e2 == nil && h >= 0 && h < 24 && m >= 0 && m < 60 && fmt.Sprintf("%02d:%02d", h, m) == value
}

func newCheckinController(a *Adapter) *checkinController {
	path := a.cfg.CheckinStatePath
	if path == "" {
		path = a.cfg.CredentialPath
		if path == "" {
			path = DefaultCredentialPath()
		}
		path += ".checkin.json"
	}
	c := &checkinController{a: a, path: path, wake: make(chan struct{}, 1), state: checkinDisk{Version: 1, Enabled: true, Time: "09:00", LastResult: "pending"}}
	f, err := os.Open(path)
	if os.IsNotExist(err) {
		return c
	}
	if err == nil {
		defer f.Close()
		raw, readErr := io.ReadAll(io.LimitReader(f, 128*1024+1))
		var state checkinDisk
		if readErr == nil && len(raw) <= 128*1024 && json.Unmarshal(raw, &state) == nil && state.Version == 1 && validCheckinTime(state.Time) {
			c.state = state
			return c
		}
	}
	c.state.Enabled, c.state.LastResult, c.state.LastError = false, "failed", "签到设置无法读取，请重新保存设置"
	return c
}

func accountHash(uid string) string { return fmt.Sprintf("%x", sha256.Sum256([]byte(uid))) }

func (a *Adapter) approvedRewards(uid string) map[string]bool {
	if a.checkins == nil {
		return nil
	}
	c := a.checkins
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.state.Account != accountHash(uid) {
		return nil
	}
	out := map[string]bool{}
	for key, value := range c.state.Rewards {
		out[key] = value
	}
	return out
}

func (c *checkinController) saveLocked() error {
	raw, err := json.MarshalIndent(c.state, "", "  ")
	if err != nil {
		return fmt.Errorf("无法编码签到设置")
	}
	if err := os.MkdirAll(filepath.Dir(c.path), 0o700); err != nil {
		return fmt.Errorf("无法创建签到设置目录")
	}
	f, err := os.CreateTemp(filepath.Dir(c.path), ".trae-checkin-*.tmp")
	if err != nil {
		return fmt.Errorf("无法保存签到设置")
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
		return fmt.Errorf("无法写入签到设置")
	}
	if err = os.Rename(f.Name(), c.path); err != nil {
		return fmt.Errorf("无法更新签到设置")
	}
	return nil
}

func (a *Adapter) CheckinStatus() CheckinView {
	v := a.checkins.snapshot(time.Now())
	cred, err := a.load(a.cfg.CredentialPath)
	a.checkins.mu.Lock()
	account := a.checkins.state.Account
	a.checkins.mu.Unlock()
	if err != nil || !expiryTime(cred.ExpiresAt).After(time.Now()) || account != accountHash(cred.UserID) {
		// 登录切换后不可把旧账号余额和签到结果展示给新连接。
		v.Balance, v.Available, v.CheckedIn, v.DailyCredits, v.StatusCheckedAt = nil, nil, nil, nil, nil
		v.CanManual = false
	}
	return v
}

func (c *checkinController) snapshot(now time.Time) CheckinView {
	c.mu.Lock()
	defer c.mu.Unlock()
	s := c.state
	today := now.In(checkinZone).Format("2006-01-02")
	v := CheckinView{Enabled: s.Enabled, Time: s.Time, Timezone: "Asia/Shanghai", Today: today, Balance: s.Balance, LastAttemptAt: s.LastAttemptAt, LastSuccessAt: s.LastSuccessAt, LastResult: s.LastResult, LastError: s.LastError, Busy: c.busy}
	if s.Day == today {
		v.Available, v.DailyCredits, v.StatusCheckedAt = s.Available, s.DailyCredits, s.StatusCheckedAt
		if s.StatusCheckedAt != nil {
			checked := s.Checked
			v.CheckedIn = &checked
		}
		v.CanManual = s.LastResult == "failed" && !s.Checked && !c.busy && s.LastAttemptAt != nil && s.LastAttemptAt.In(checkinZone).Format("2006-01-02") == today && (s.RetryAt == nil || !now.Before(*s.RetryAt))
	}
	if s.Enabled {
		h, _ := strconv.Atoi(s.Time[:2])
		m, _ := strconv.Atoi(s.Time[3:])
		local := now.In(checkinZone)
		next := time.Date(local.Year(), local.Month(), local.Day(), h, m, 0, 0, checkinZone)
		if s.Day == today && s.Checked {
			next = next.AddDate(0, 0, 1)
		} else if !next.After(now) {
			next = now
			if s.RetryAt != nil && s.RetryAt.After(now) {
				next = *s.RetryAt
			}
			if s.Day == today && s.Attempts >= 6 {
				next = time.Date(local.Year(), local.Month(), local.Day()+1, h, m, 0, 0, checkinZone)
			}
		}
		v.NextRunAt = &next
	}
	return v
}

func (a *Adapter) ConfigureCheckin(enabled bool, at string) error {
	if !validCheckinTime(at) {
		return fmt.Errorf("签到时间应为 HH:MM（北京时间）")
	}
	c := a.checkins
	c.mu.Lock()
	before := c.state
	c.state.Enabled, c.state.Time = enabled, at
	err := c.saveLocked()
	if err != nil {
		c.state = before
	}
	c.mu.Unlock()
	if err == nil {
		select {
		case c.wake <- struct{}{}:
		default:
		}
	}
	return err
}

// 后台任务与网页生命周期无关；重新启动后先查今日状态再决定是否领取。
func (a *Adapter) StartBackground(ctx context.Context) {
	c := a.checkins
	c.once.Do(func() {
		go func() {
			ticker := time.NewTicker(30 * time.Second)
			defer ticker.Stop()
			for {
				c.tick(ctx, time.Now())
				select {
				case <-ctx.Done():
					return
				case <-ticker.C:
				case <-c.wake:
				}
			}
		}()
	})
}

func (c *checkinController) tick(ctx context.Context, now time.Time) {
	if cred, err := c.a.load(c.a.cfg.CredentialPath); err == nil {
		c.mu.Lock()
		if !c.busy {
			c.resetAccountLocked(cred.UserID)
		}
		c.mu.Unlock()
	}
	c.mu.Lock()
	retryAt := c.state.RetryAt
	c.mu.Unlock()
	if retryAt != nil && now.Before(*retryAt) {
		return
	}
	v := c.snapshot(now)
	if v.Enabled && v.NextRunAt != nil && !v.NextRunAt.After(now) && !v.Busy {
		_ = c.run(ctx, true, false, now)
		return
	}
	if v.StatusCheckedAt == nil || now.Sub(*v.StatusCheckedAt) >= 15*time.Minute {
		_ = c.run(ctx, false, false, now)
	}
}

func (a *Adapter) RefreshCheckin(ctx context.Context) error {
	return a.checkins.run(ctx, false, false, time.Now())
}
func (a *Adapter) ClaimCheckin(ctx context.Context) error {
	return a.checkins.run(ctx, true, true, time.Now())
}

type checkinWire struct {
	Code    int      `json:"code"`
	Enable  *bool    `json:"enable"`
	Checked *bool    `json:"checked_in"`
	Credits *float64 `json:"credits"`
}

func (a *Adapter) checkinRequest(ctx context.Context, cred Credential, claim bool) (checkinWire, error) {
	path := "/trae/api/v2/ug/checkin_credits/status"
	if claim {
		path = "/trae/api/v2/ug/checkin_credits/claim"
	}
	if cred.Device == nil || cred.Device.DeviceID == "" {
		return checkinWire{}, fmt.Errorf("签到缺少原授权设备信息，请重新授权")
	}
	raw, err := a.read(ctx, cred, a.authBase, path, map[string]any{})
	if err != nil {
		return checkinWire{}, err
	}
	var result checkinWire
	if json.Unmarshal(raw, &result) != nil {
		return result, fmt.Errorf("签到响应格式无效")
	}
	if result.Code != 0 {
		switch result.Code {
		case 9074:
			return result, fmt.Errorf("TRAE 当前参与用户过多，稍后自动重试（9074）")
		case 9095:
			return result, fmt.Errorf("TRAE 返回设备已签到，需重新确认本账号状态（9095）")
		default:
			return result, fmt.Errorf("TRAE 签到失败（%d）", result.Code)
		}
	}
	if !claim && (result.Enable == nil || result.Checked == nil) {
		return result, fmt.Errorf("无法确认今日签到状态")
	}
	if result.Credits != nil && (!finite(*result.Credits) || *result.Credits <= 0) {
		result.Credits = nil
	}
	return result, nil
}

func (c *checkinController) resetAccountLocked(uid string) {
	key := accountHash(uid)
	if c.state.Account == key {
		return
	}
	c.state = checkinDisk{Version: 1, Enabled: c.state.Enabled, Time: c.state.Time, Account: key, LastResult: "pending"}
}

func (c *checkinController) updateStatus(wire checkinWire, now time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	today := now.In(checkinZone).Format("2006-01-02")
	if c.state.Day != today {
		c.state.Attempts, c.state.RetryAt = 0, nil
	}
	c.state.Day, c.state.Checked, c.state.Available, c.state.DailyCredits, c.state.StatusCheckedAt = today, *wire.Checked, wire.Enable, wire.Credits, &now
}

func (c *checkinController) readBalance(ctx context.Context, cred Credential, now time.Time) (quotaResponse, error) {
	raw, err := c.a.read(ctx, cred, c.a.authBase, "/trae/api/v2/pay/ide_user_ent_usage", map[string]any{"require_usage": true, "req_source": 1, "full_data": true})
	if err != nil {
		return quotaResponse{}, err
	}
	var q quotaResponse
	if json.Unmarshal(raw, &q) != nil || q.Summary.Total == nil || q.Summary.Used == nil || !finite(*q.Summary.Total) || !finite(*q.Summary.Used) || *q.Summary.Total < 0 || *q.Summary.Used < 0 || *q.Summary.Used > *q.Summary.Total {
		return q, fmt.Errorf("积分余额无法确认")
	}
	reconcileErr := c.reconcileReward(q, now)
	_, freeErr := verifyFreeQuotaApproved(raw, now, c.a.approvedRewards(cred.UserID))
	// 余额为零时仍显示明确的零；能否生成由原有免费校验决定。
	verified := freeErr == nil || llm.Wrap(freeErr).Code == "trae_free_credits_exhausted"
	balance := &CreditBalance{Total: *q.Summary.Total, Used: *q.Summary.Used, Remaining: *q.Summary.Total - *q.Summary.Used, VerifiedFree: verified, CheckedAt: now}
	for _, p := range q.Packs {
		if p.Base.End != nil && p.Base.Product != nil && *p.Base.Product == 2 && *p.Base.End > now.Unix() {
			end := time.Unix(*p.Base.End, 0)
			if balance.ExpiresAt == nil || end.Before(*balance.ExpiresAt) {
				balance.ExpiresAt = &end
			}
		}
	}
	c.mu.Lock()
	c.state.Balance = balance
	c.mu.Unlock()
	return q, reconcileErr
}

// 仅确认本次签到新增且金额与官方奖励一致的零价通用奖励包，不放开未知来源。
func (c *checkinController) reconcileReward(q quotaResponse, now time.Time) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	pending := c.state.Pending
	if pending == nil {
		return nil
	}
	if !c.state.Checked || c.state.Day != pending.Day {
		return nil
	}
	if q.Summary.Total == nil || math.Abs(*q.Summary.Total-pending.TotalBefore-pending.Expected) > 0.000001 {
		return fmt.Errorf("签到状态已确认，奖励积分到账待核对；稍后刷新余额")
	}
	approved := map[string]bool{}
	for k, v := range c.state.Rewards {
		approved[k] = v
	}
	added := 0.0
	for _, p := range q.Packs {
		key := rewardFingerprint(p)
		if pending.Before[key] {
			continue
		}
		b := p.Base
		if b.Product == nil || *b.Product != 2 || b.Charge == nil || *b.Charge != 0 || b.Endpoint == nil || *b.Endpoint != 0 || b.EntStatus == nil || *b.EntStatus != 0 || p.Status == nil || *p.Status != 1 || b.Start == nil || *b.Start > now.Unix() || b.End == nil || *b.End <= now.Unix() || p.Group == nil || b.Quota.Credits == nil || !finite(*b.Quota.Credits) || *b.Quota.Credits <= 0 {
			return fmt.Errorf("签到奖励来源或使用范围尚未确认，保留免费限制")
		}
		approved[key] = true
		added += *b.Quota.Credits
	}
	if math.Abs(added-pending.Expected) > 0.000001 {
		return fmt.Errorf("签到奖励金额尚未确认，保留免费限制")
	}
	raw, _ := json.Marshal(q)
	if _, err := verifyFreeQuotaApproved(raw, now, approved); err != nil {
		return fmt.Errorf("签到后账号免费权益无法确认")
	}
	// 只保留当前仍在上游目录中的奖励，避免凭据旁的状态文件无限增长。
	live := map[string]bool{}
	for _, pack := range q.Packs {
		key := rewardFingerprint(pack)
		if approved[key] {
			live[key] = true
		}
	}
	before := c.state
	c.state.Rewards, c.state.Pending = live, nil
	if err := c.saveLocked(); err != nil {
		c.state = before
		return err
	}
	return nil
}

func (c *checkinController) run(ctx context.Context, claim, manual bool, now time.Time) (err error) {
	startedAt := time.Now()
	if !c.op.TryLock() {
		return fmt.Errorf("签到查询或执行中，请稍后查看")
	}
	defer c.op.Unlock()
	c.mu.Lock()
	pending := c.state.Pending != nil
	c.mu.Unlock()
	if claim || pending {
		c.a.quotaMu.Lock()
		defer c.a.quotaMu.Unlock()
	} else {
		c.a.quotaMu.RLock()
		defer c.a.quotaMu.RUnlock()
	}
	ctx, cancel := context.WithTimeout(ctx, 25*time.Second)
	defer cancel()
	c.mu.Lock()
	if manual && !(c.state.Day == now.In(checkinZone).Format("2006-01-02") && c.state.LastResult == "failed" && !c.state.Checked && c.state.LastAttemptAt != nil && c.state.LastAttemptAt.In(checkinZone).Format("2006-01-02") == c.state.Day) {
		c.mu.Unlock()
		return fmt.Errorf("立即签到仅在自动签到失败后用于补救")
	}
	if manual && c.state.RetryAt != nil && now.Before(*c.state.RetryAt) {
		c.mu.Unlock()
		return fmt.Errorf("请等待下一次重试时间，避免重复请求触发限流")
	}
	c.busy = true
	c.mu.Unlock()
	defer func() {
		c.mu.Lock()
		defer c.mu.Unlock()
		c.busy = false
		if err != nil {
			c.state.LastResult, c.state.LastError = "failed", err.Error()
			retry := now.Add(15 * time.Minute)
			c.state.RetryAt = &retry
		}
		if saveErr := c.saveLocked(); saveErr != nil && err == nil {
			err = saveErr
			c.state.LastResult, c.state.LastError = "failed", saveErr.Error()
		}
	}()
	cred, err := c.a.load(c.a.cfg.CredentialPath)
	if err != nil {
		return err
	}
	if !expiryTime(cred.ExpiresAt).After(now.Add(30 * time.Second)) {
		return loginRequired()
	}
	c.mu.Lock()
	c.resetAccountLocked(cred.UserID)
	c.mu.Unlock()
	if claim {
		c.mu.Lock()
		today := now.In(checkinZone).Format("2006-01-02")
		if c.state.Day != today {
			c.state.Day, c.state.Checked, c.state.Attempts = today, false, 0
			c.state.StatusCheckedAt = nil
		}
		c.state.Attempts++
		c.state.LastAttemptAt = &now
		err = c.saveLocked()
		c.mu.Unlock()
		if err != nil {
			return err
		}
	}
	wire, err := c.a.checkinRequest(ctx, cred, false)
	if err != nil {
		return err
	}
	c.updateStatus(wire, now)
	q, balanceErr := c.readBalance(ctx, cred, now)
	if *wire.Checked {
		if balanceErr != nil {
			return balanceErr
		}
		c.mu.Lock()
		c.state.LastResult, c.state.LastError, c.state.RetryAt = "already_checked", "", nil
		if c.state.LastSuccessAt == nil || c.state.LastSuccessAt.In(checkinZone).Format("2006-01-02") != c.state.Day {
			c.state.LastSuccessAt = &now
		}
		c.mu.Unlock()
		return nil
	}
	if !claim {
		return balanceErr
	}
	if !*wire.Enable {
		return fmt.Errorf("此账号当前未开放每日签到")
	}
	if wire.Credits == nil {
		return fmt.Errorf("无法确认今日奖励积分，未发起领取")
	}
	if balanceErr != nil {
		return balanceErr
	}
	raw, _ := json.Marshal(q)
	if _, err = verifyFreeQuotaApproved(raw, now, c.a.approvedRewards(cred.UserID)); err != nil {
		// 用完月赠积分也可领取，但其他免费边界仍须完整通过。
		if llm.Wrap(err).Code != "trae_free_credits_exhausted" {
			return err
		}
	}
	c.mu.Lock()
	c.state.Pending = &pendingCheckin{Day: c.state.Day, Expected: *wire.Credits, TotalBefore: *q.Summary.Total, Before: map[string]bool{}}
	for _, p := range q.Packs {
		c.state.Pending.Before[rewardFingerprint(p)] = true
	}
	err = c.saveLocked()
	c.mu.Unlock()
	if err != nil {
		return err
	}
	_, claimErr := c.a.checkinRequest(ctx, cred, true)
	// 领取结果可能因超时而未知，只有账号状态确认后才能显示已签到。
	wire, err = c.a.checkinRequest(ctx, cred, false)
	if err != nil {
		if claimErr != nil {
			return claimErr
		}
		return err
	}
	// 新奖励的生效时间来自领取过程，不能用请求开始前的旧时刻判断为未来积分。
	observedAt := now.Add(time.Since(startedAt))
	c.updateStatus(wire, observedAt)
	if !*wire.Checked {
		if claimErr != nil {
			return claimErr
		}
		return fmt.Errorf("领取请求已发送，今日签到状态仍未确认")
	}
	if _, err = c.readBalance(ctx, cred, observedAt); err != nil {
		return err
	}
	c.mu.Lock()
	c.state.LastResult, c.state.LastError, c.state.RetryAt = "success", "", nil
	c.state.LastSuccessAt = &observedAt
	c.mu.Unlock()
	c.a.InvalidateModels()
	return nil
}
