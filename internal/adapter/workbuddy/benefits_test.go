package workbuddy

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/576469377/Agent2API/internal/adapter"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"
)

func benefitFixture(t *testing.T, handler http.HandlerFunc) *Adapter {
	t.Helper()
	s := httptest.NewServer(handler)
	t.Cleanup(s.Close)
	path := filepath.Join(t.TempDir(), "account.json")
	if err := os.WriteFile(path, []byte(`{"auth":{"accessToken":"fixture","tokenType":"Bearer"},"account":{"uid":"fixture-user"}}`), 0600); err != nil {
		t.Fatal(err)
	}
	a, err := New(Config{Platform: "workbuddy", BaseURL: s.URL, CredentialPath: path})
	if err != nil {
		t.Fatal(err)
	}
	return a
}

func TestWorkBuddyCreditSummaryPreservesNumbersAndRejectsMissingFields(t *testing.T) {
	var bad atomic.Bool
	a := benefitFixture(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/billing/meter/get-user-resource-summary" {
			t.Error(r.URL.Path)
		}
		if bad.Load() {
			fmt.Fprint(w, `{"code":0,"data":{"Packages":[{}]}}`)
		} else {
			fmt.Fprint(w, `{"code":0,"data":{"Packages":[{"CycleTotalCapacity":"500","CycleUsedCapacity":100.5,"CycleRemainCapacity":"399.5"}]}}`)
		}
	})
	if err := a.RefreshAccountBenefits(context.Background()); err != nil {
		t.Fatal(err)
	}
	b := a.AccountBenefits()
	if b.Balances[0].Remaining == nil || *b.Balances[0].Remaining != 399.5 {
		t.Fatal(b)
	}
	bad.Store(true)
	if a.RefreshAccountBenefits(context.Background()) == nil {
		t.Fatal("incomplete credits treated as zero")
	}
	if b = a.AccountBenefits(); b.Status != "stale" || *b.Balances[0].Remaining != 399.5 {
		t.Fatal("last reading lost", b)
	}
}

func TestWorkBuddyCheckinConfirmsAndSkipsAfterRestart(t *testing.T) {
	var checked atomic.Bool
	var claims atomic.Int32
	a := benefitFixture(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v2/billing/meter/checkin-activity-status":
			fmt.Fprintf(w, `{"code":0,"data":{"active":true,"today_checked_in":%v,"daily_credit":100}}`, checked.Load())
		case "/v2/billing/meter/daily-checkin":
			claims.Add(1)
			checked.Store(true)
			fmt.Fprint(w, `{"code":0}`)
		case "/billing/meter/get-user-resource-summary":
			fmt.Fprint(w, `{"code":0,"data":{"Packages":[]}}`)
		default:
			t.Error(r.URL.Path)
		}
	})
	if a.CheckinStatus().CheckedIn != nil || claims.Load() != 0 {
		t.Fatal("snapshot generated a request")
	}
	if err := a.ClaimCheckin(context.Background()); err != nil {
		t.Fatal(err)
	}
	if v := a.CheckinStatus(); v.CheckedIn == nil || !*v.CheckedIn || v.LastResult != "success" {
		t.Fatal(v)
	}
	restarted, err := New(a.cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err = restarted.ClaimCheckin(context.Background()); err != nil || claims.Load() != 1 {
		t.Fatal("duplicate claim after restart", claims.Load(), err)
	}
	raw, _ := os.ReadFile(a.checkins.path)
	var record checkinRecord
	if json.Unmarshal(raw, &record) != nil || record.Account == "fixture-user" {
		t.Fatal("unsafe check-in state")
	}
}

func TestWorkBuddyLostClaimReplyIsVerifiedBeforeRetry(t *testing.T) {
	var checked atomic.Bool
	var claims atomic.Int32
	a := benefitFixture(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v2/billing/meter/checkin-activity-status":
			fmt.Fprintf(w, `{"code":0,"data":{"active":true,"today_checked_in":%v}}`, checked.Load())
		case "/v2/billing/meter/daily-checkin":
			claims.Add(1)
			checked.Store(true)
			w.WriteHeader(503)
			fmt.Fprint(w, `{"code":503}`)
		default:
			fmt.Fprint(w, `{"code":0,"data":{"Packages":[]}}`)
		}
	})
	if err := a.ClaimCheckin(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := a.ClaimCheckin(context.Background()); err != nil || claims.Load() != 1 {
		t.Fatal("replayed successful claim", err, claims.Load())
	}
}

func TestWorkBuddyClosedActivityAndUnsavedStateNeverClaim(t *testing.T) {
	for _, closed := range []bool{true, false} {
		t.Run(fmt.Sprint(closed), func(t *testing.T) {
			var claims atomic.Int32
			a := benefitFixture(t, func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/v2/billing/meter/daily-checkin" {
					claims.Add(1)
				}
				fmt.Fprintf(w, `{"code":0,"data":{"active":%v,"today_checked_in":false}}`, !closed)
			})
			if !closed {
				a.checkins.path = t.TempDir()
			}
			_ = a.ClaimCheckin(context.Background())
			if claims.Load() != 0 {
				t.Fatal("claimed inactive or unpersisted reward")
			}
		})
	}
}

var _ adapter.BenefitsProvider = (*Adapter)(nil)
var _ adapter.CheckinProvider = (*Adapter)(nil)

func TestWorkBuddyBackgroundCatchesUpDueCheckin(t *testing.T) {
	var checked atomic.Bool
	claimed := make(chan struct{}, 1)
	a := benefitFixture(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v2/billing/meter/checkin-activity-status":
			fmt.Fprintf(w, `{"code":0,"data":{"active":true,"today_checked_in":%v}}`, checked.Load())
		case "/v2/billing/meter/daily-checkin":
			checked.Store(true)
			claimed <- struct{}{}
			fmt.Fprint(w, `{"code":0}`)
		default:
			fmt.Fprint(w, `{"code":0,"data":{"Packages":[]}}`)
		}
	})
	if err := a.ConfigureCheckin(true, "00:00"); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	a.StartBackground(ctx)
	select {
	case <-claimed:
	case <-time.After(3 * time.Second):
		t.Fatal("background did not claim an eligible overdue reward")
	}
}

func TestWorkBuddyCorruptCheckinSettingsRemainPausedAndExplainWhy(t *testing.T) {
	a := benefitFixture(t, func(w http.ResponseWriter, r *http.Request) {
		t.Error("snapshot must not contact upstream")
	})
	if err := os.WriteFile(a.checkins.path, []byte("invalid"), 0600); err != nil {
		t.Fatal(err)
	}
	a.checkins = newAccountCheckin(a)
	v := a.CheckinStatus()
	if v.Enabled || v.LastResult != "failed" || v.LastError == "" {
		t.Fatal("invalid settings lost their diagnostic", v)
	}
}
