package app

import (
	"context"
	"github.com/576469377/Agent2API/internal/adapter"
	"github.com/576469377/Agent2API/internal/config"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

type accountBenefitFixture struct {
	fakeAdapter
	amount    float64
	refreshes atomic.Int32
	claims    atomic.Int32
}

func (f *accountBenefitFixture) AccountBenefits() adapter.AccountBenefits {
	return adapter.AccountBenefits{Status: "ready", Balances: []adapter.AccountBalance{{ID: "credits", Unit: "credits", Remaining: adapter.BenefitAmount(f.amount)}}}
}
func (f *accountBenefitFixture) RefreshAccountBenefits(context.Context) error {
	f.refreshes.Add(1)
	return nil
}
func (f *accountBenefitFixture) CheckinStatus() adapter.CheckinView {
	return adapter.CheckinView{Enabled: true, Time: "09:00", Timezone: "Asia/Shanghai"}
}
func (f *accountBenefitFixture) RefreshCheckin(context.Context) error { return nil }
func (f *accountBenefitFixture) ConfigureCheckin(bool, string) error  { return nil }
func (f *accountBenefitFixture) ClaimCheckin(context.Context) error   { f.claims.Add(1); return nil }

func TestAccountBenefitsAndCheckinsSelectExactPoolMember(t *testing.T) {
	first, second := &accountBenefitFixture{amount: 100}, &accountBenefitFixture{amount: 220}
	pool := adapter.NewPool("workbuddy", nil)
	pool.Add("a", first)
	pool.Add("b", second)
	h, err := NewHub([]*PlatformRuntime{{ID: "workbuddy", Adapter: pool, Pool: pool}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	cfg := config.Default()
	cfg.MetricsFile = ""
	app := New(cfg, h, nil)
	request := func(method, path, body string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		if method == "POST" {
			r.Header.Set("Content-Type", "application/json")
		}
		app.Handler().ServeHTTP(w, r)
		return w
	}
	if w := request("GET", "/api/accounts/benefits?platform=workbuddy&label=b", ""); w.Code != 200 || !strings.Contains(w.Body.String(), `"remaining":220`) {
		t.Fatal(w.Code, w.Body.String())
	}
	if first.refreshes.Load() != 0 || second.refreshes.Load() != 0 || first.claims.Load() != 0 || second.claims.Load() != 0 {
		t.Fatal("cached GET caused an upstream action")
	}
	if w := request("GET", "/api/accounts/benefits?platform=workbuddy&label=b&refresh=1", ""); w.Code != 200 || first.refreshes.Load() != 0 || second.refreshes.Load() != 1 {
		t.Fatal("wrong balance refreshed", w.Code)
	}
	if w := request("POST", "/api/accounts/checkin?platform=workbuddy&label=b", `{"action":"claim"}`); w.Code != 200 || first.claims.Load() != 0 || second.claims.Load() != 1 {
		t.Fatal("wrong account claimed", w.Code)
	}
	for _, path := range []string{"/api/accounts/checkin?platform=workbuddy", "/api/accounts/benefits?platform=workbuddy"} {
		if w := request("GET", path, ""); w.Code != 400 {
			t.Fatal("ambiguous account selected", w.Code)
		}
	}
	if w := request("GET", "/api/accounts/benefits?platform=workbuddy&label=missing", ""); w.Code != 404 {
		t.Fatal(w.Code)
	}
	app.cfg.Auth.APIKey = "fixture-key"
	if w := request("GET", "/api/accounts/benefits?platform=workbuddy&label=b&refresh=1", ""); w.Code != 401 || second.refreshes.Load() != 1 {
		t.Fatal("unauthorized balance operation", w.Code)
	}
}
