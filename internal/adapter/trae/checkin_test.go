package trae

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

type checkinFixture struct {
	mu               sync.Mutex
	checked          bool
	claims, statuses int
	claimCode        int
	badReward        bool
	rewardStart      *int64
	claimDelay       time.Duration
	confirm          bool
	deviceIDs        map[string]bool
	quota            quotaResponse
}

func newCheckinFixture(t *testing.T) (*Adapter, *checkinFixture, Config, string) {
	t.Helper()
	f := &checkinFixture{confirm: true, deviceIDs: map[string]bool{}}
	if err := json.Unmarshal(fixture(t, "trae-free-quota.json"), &f.quota); err != nil {
		t.Fatal(err)
	}
	used := 10.0
	f.quota.Summary.Used = &used
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		if r.Header.Get("Authorization") != "Cloud-IDE-JWT fixture-access" || r.Header.Get("x-device-id") != "fixture-device" {
			t.Error("credential or stable device missing")
		}
		f.deviceIDs[r.Header.Get("x-device-id")] = true
		switch r.URL.Path {
		case "/trae/api/v2/ug/checkin_credits/status":
			f.statuses++
			fmt.Fprintf(w, `{"code":0,"enable":true,"checked_in":%v,"credits":100}`, f.checked)
		case "/trae/api/v2/ug/checkin_credits/claim":
			f.claims++
			if f.claimDelay > 0 {
				time.Sleep(f.claimDelay)
			}
			if f.claimCode != 0 {
				fmt.Fprintf(w, `{"code":%d,"message":"private upstream body"}`, f.claimCode)
				return
			}
			if f.confirm && !f.checked {
				f.checked = true
				pack := f.quota.Packs[1]
				group := 17
				credits := 100.0
				pack.Group = &group
				pack.Base.Quota.Credits = &credits
				if f.rewardStart != nil {
					pack.Base.Start = f.rewardStart
				}
				if f.badReward {
					charge := 1.0
					pack.Base.Charge = &charge
				}
				f.quota.Packs = append(f.quota.Packs, pack)
				total := 600.0
				f.quota.Summary.Total = &total
			}
			fmt.Fprint(w, `{"code":0}`)
		case "/trae/api/v2/pay/ide_user_ent_usage":
			json.NewEncoder(w).Encode(f.quota)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)
	cfg := Config{BaseURL: server.URL, CredentialPath: filepath.Join(t.TempDir(), "session.json")}
	a, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	prepareCheckinAdapter(a, server.URL)
	return a, f, cfg, server.URL
}

func prepareCheckinAdapter(a *Adapter, base string) {
	a.authBase = base
	a.load = func(string) (Credential, error) {
		return Credential{Token: "fixture-access", UserID: "fixture-user", ExpiresAt: time.Now().Add(24 * time.Hour).Format(time.RFC3339), Device: &deviceInfo{DeviceID: "fixture-device", DeviceBrand: "Fixture", DeviceType: "PC"}}, nil
	}
}

func checkinToday(hour, minute int) time.Time {
	now := time.Now().In(checkinZone)
	return time.Date(now.Year(), now.Month(), now.Day(), hour, minute, 0, 0, checkinZone)
}

func TestCheckinAcceptsRewardActivatedDuringClaimWithoutRepeatingIt(t *testing.T) {
	a, f, _, _ := newCheckinFixture(t)
	now := checkinToday(9, 0)
	start := now.Add(time.Second).Unix()
	f.rewardStart, f.claimDelay = &start, 1100*time.Millisecond
	if err := a.checkins.run(context.Background(), true, false, now); err != nil {
		t.Fatal("new reward was compared with the pre-claim timestamp:", err)
	}
	view := a.checkins.snapshot(now.Add(2 * time.Second))
	if f.claims != 1 || view.LastResult != "success" || view.Balance == nil || !view.Balance.VerifiedFree || view.Balance.Remaining != 590 {
		t.Fatalf("reward activation failed: %+v", view)
	}
}

func TestCheckinScheduleRunsAtNineAndRestartSkipsDuplicateClaim(t *testing.T) {
	a, f, cfg, base := newCheckinFixture(t)
	a.checkins.tick(context.Background(), checkinToday(8, 59))
	if f.claims != 0 {
		t.Fatal("claimed before scheduled time")
	}
	view := a.checkins.snapshot(checkinToday(8, 59))
	if view.CheckedIn == nil || *view.CheckedIn || view.Balance == nil || view.Balance.Remaining != 490 || view.CanManual {
		t.Fatal("initial status or fallback gate incorrect")
	}
	a.checkins.tick(context.Background(), checkinToday(9, 0))
	view = a.checkins.snapshot(checkinToday(9, 0))
	if f.claims != 1 || view.CheckedIn == nil || !*view.CheckedIn || view.LastResult != "success" || view.Balance.Remaining != 590 || !view.Balance.VerifiedFree || view.CanManual {
		t.Fatalf("scheduled claim failed: %+v", view)
	}
	if view.NextRunAt == nil || view.NextRunAt.In(checkinZone).Hour() != 9 || !view.NextRunAt.After(checkinToday(23, 59)) {
		t.Fatal("next run is not tomorrow at 09:00 Beijing time")
	}
	reloaded, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	prepareCheckinAdapter(reloaded, base)
	reloaded.checkins.tick(context.Background(), checkinToday(12, 0))
	if f.claims != 1 {
		t.Fatal("restart repeated today's claim")
	}
	if len(reloaded.approvedRewards("fixture-user")) != 1 || len(reloaded.approvedRewards("another-user")) != 0 {
		t.Fatal("verified reward was lost or leaked between accounts")
	}
	raw, _ := json.Marshal(f.quota)
	if _, err := verifyFreeQuotaApproved(raw, time.Now(), reloaded.approvedRewards("fixture-user")); err != nil {
		t.Fatal("confirmed reward unusable after restart", err)
	}
	if _, err := verifyFreeQuota(raw, time.Now()); err == nil {
		t.Fatal("unknown reward accepted without receipt evidence")
	}
}

func TestCheckinFailureBackoffAndManualFallbackNeverRotateDevice(t *testing.T) {
	a, f, _, _ := newCheckinFixture(t)
	f.claimCode = 9074
	at := checkinToday(9, 0)
	a.checkins.tick(context.Background(), at)
	view := a.checkins.snapshot(at)
	if view.LastResult != "failed" || view.CheckedIn == nil || *view.CheckedIn || view.CanManual {
		t.Fatal("rate-limited claim incorrectly succeeded or bypassed backoff")
	}
	a.checkins.tick(context.Background(), at.Add(time.Minute))
	if f.claims != 1 {
		t.Fatal("retried during backoff")
	}
	a.checkins.tick(context.Background(), at.Add(15*time.Minute))
	if f.claims != 2 || len(f.deviceIDs) != 1 {
		t.Fatal("retry count or stable device changed")
	}
	if !a.checkins.snapshot(at.Add(30 * time.Minute)).CanManual {
		t.Fatal("failed automatic run has no manual fallback")
	}
	f.claimCode = 0
	if err := a.checkins.run(context.Background(), true, true, at.Add(30*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if f.claims != 3 || a.checkins.snapshot(at.Add(30*time.Minute)).CanManual {
		t.Fatal("manual fallback repeated or remained enabled after success")
	}
}

func TestClaimResponseAloneCannotMarkCheckinSuccessful(t *testing.T) {
	a, f, _, _ := newCheckinFixture(t)
	f.confirm = false
	if err := a.checkins.run(context.Background(), true, false, checkinToday(9, 0)); err == nil {
		t.Fatal("unconfirmed account status accepted")
	}
	v := a.checkins.snapshot(checkinToday(9, 0))
	if v.CheckedIn == nil || *v.CheckedIn || v.LastResult != "failed" || len(a.approvedRewards("fixture-user")) != 0 {
		t.Fatal("false success or unverified grant")
	}
}

func TestCheckinRejectsPaidRewardEvenWhenAccountStatusSaysChecked(t *testing.T) {
	a, f, _, _ := newCheckinFixture(t)
	f.badReward = true
	if err := a.checkins.run(context.Background(), true, false, checkinToday(9, 0)); err == nil {
		t.Fatal("paid reward accepted")
	}
	v := a.checkins.snapshot(checkinToday(9, 0))
	if v.CheckedIn == nil || !*v.CheckedIn || v.Balance == nil || v.Balance.VerifiedFree || len(a.approvedRewards("fixture-user")) != 0 {
		t.Fatal("paid or unknown eligibility was hidden")
	}
}

func TestCheckinSettingsPersistAndFirstRunManualIsDisabled(t *testing.T) {
	a, _, cfg, _ := newCheckinFixture(t)
	if err := a.ClaimCheckin(context.Background()); err == nil {
		t.Fatal("manual button bypassed primary automatic flow")
	}
	for _, bad := range []string{"9:00", "24:00", "09:60", "-1:30"} {
		if a.ConfigureCheckin(true, bad) == nil {
			t.Fatal("invalid schedule accepted", bad)
		}
	}
	if err := a.ConfigureCheckin(false, "09:00"); err != nil {
		t.Fatal(err)
	}
	reloaded, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if reloaded.CheckinStatus().Enabled || reloaded.CheckinStatus().NextRunAt != nil {
		t.Fatal("paused schedule did not survive restart")
	}
}

func TestPendingSuccessfulClaimReconcilesAfterRestartWithoutAnotherClaim(t *testing.T) {
	a, f, cfg, base := newCheckinFixture(t)
	before := map[string]bool{}
	for _, p := range f.quota.Packs {
		before[rewardFingerprint(p)] = true
	}
	at := checkinToday(9, 0)
	a.checkins.state.Account = accountHash("fixture-user")
	a.checkins.state.Pending = &pendingCheckin{Day: at.Format("2006-01-02"), Expected: 100, TotalBefore: 500, Before: before}
	if err := a.checkins.saveLocked(); err != nil {
		t.Fatal(err)
	}
	// 模拟上游已领取、进程在保存最终结果前退出。
	if _, err := a.checkinRequest(context.Background(), mustCheckinCredential(a), true); err != nil {
		t.Fatal(err)
	}
	reloaded, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	prepareCheckinAdapter(reloaded, base)
	reloaded.checkins.tick(context.Background(), at.Add(time.Hour))
	if f.claims != 1 || len(reloaded.approvedRewards("fixture-user")) != 1 {
		t.Fatal("recovery repeated claim or lost grant evidence")
	}
}

func mustCheckinCredential(a *Adapter) Credential { c, _ := a.load(""); return c }

func TestReplayRealCheckinStatus(t *testing.T) {
	a, _, _, _ := newCheckinFixture(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write(fixture(t, "trae-checkin-status.json")) }))
	defer server.Close()
	a.authBase = server.URL
	result, err := a.checkinRequest(context.Background(), mustCheckinCredential(a), false)
	if err != nil || result.Enable == nil || !*result.Enable || result.Checked == nil || *result.Checked || result.Credits == nil || *result.Credits != 100 {
		t.Fatal("real status frame changed", err)
	}
}

func TestAccountSwitchHidesPreviousBalanceAndNoConcurrentClaim(t *testing.T) {
	a, f, _, _ := newCheckinFixture(t)
	a.checkins.tick(context.Background(), checkinToday(9, 0))
	a.load = func(string) (Credential, error) {
		return Credential{Token: "fixture-access", UserID: "different-user", ExpiresAt: time.Now().Add(time.Hour).Format(time.RFC3339)}, nil
	}
	v := a.CheckinStatus()
	if v.Balance != nil || v.CheckedIn != nil || v.CanManual {
		t.Fatal("previous account's balance or check-in was exposed")
	}
	a.checkins.op.Lock()
	if err := a.checkins.run(context.Background(), true, false, checkinToday(9, 15)); err == nil {
		t.Fatal("concurrent claim accepted")
	}
	a.checkins.op.Unlock()
	if f.claims != 1 {
		t.Fatal("second claim escaped serialization")
	}
}
