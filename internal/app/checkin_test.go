package app

import (
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/576469377/Agent2API/internal/adapter"
	"github.com/576469377/Agent2API/internal/config"
)

type checkinAPIFixture struct {
	fakeAdapter
	state                         adapter.CheckinView
	claims, refreshes, configures int
	err                           error
}

func (f *checkinAPIFixture) CheckinStatus() adapter.CheckinView   { return f.state }
func (f *checkinAPIFixture) RefreshCheckin(context.Context) error { f.refreshes++; return f.err }
func (f *checkinAPIFixture) ClaimCheckin(context.Context) error   { f.claims++; return f.err }
func (f *checkinAPIFixture) ConfigureCheckin(enabled bool, at string) error {
	f.configures++
	f.state.Enabled, f.state.Time = enabled, at
	return f.err
}

func checkinAPIApp(t *testing.T) (*App, *checkinAPIFixture) {
	t.Helper()
	f := &checkinAPIFixture{state: adapter.CheckinView{Enabled: true, Time: "09:00", Timezone: "Asia/Shanghai"}}
	h, err := NewHub([]*PlatformRuntime{{ID: "workbuddy", Adapter: f}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	cfg := config.Default()
	cfg.MetricsFile = ""
	return New(cfg, h, nil), f
}

func TestCheckinReadDoesNotClaimAndManagedAccountIncludesCachedBalance(t *testing.T) {
	a, f := checkinAPIApp(t)
	for _, path := range []string{"/api/accounts/checkin?platform=workbuddy", "/api/accounts/manage"} {
		w := httptest.NewRecorder()
		a.Handler().ServeHTTP(w, httptest.NewRequest("GET", path, nil))
		if w.Code != 200 || !strings.Contains(w.Body.String(), `"checkin"`) || !strings.Contains(w.Body.String(), `"09:00"`) {
			t.Fatal("missing check-in view", w.Code)
		}
	}
	if f.claims != 0 || f.refreshes != 0 {
		t.Fatal("viewing accounts caused an upstream operation")
	}
	w := httptest.NewRecorder()
	a.Handler().ServeHTTP(w, httptest.NewRequest("GET", "/api/accounts/checkin?platform=workbuddy&refresh=1", nil))
	if w.Code != 200 || f.refreshes != 1 || f.claims != 0 {
		t.Fatal("refresh unexpectedly claimed credits")
	}
}

func TestCheckinWritesRequireAuthenticatedJSONAndStrictActions(t *testing.T) {
	a, f := checkinAPIApp(t)
	post := func(body, contentType string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("POST", "/api/accounts/checkin?platform=workbuddy", strings.NewReader(body))
		r.Header.Set("Content-Type", contentType)
		w := httptest.NewRecorder()
		a.Handler().ServeHTTP(w, r)
		return w
	}
	if post(`{"action":"claim"}`, "text/plain").Code != 415 {
		t.Fatal("form submission accepted")
	}
	for _, body := range []string{`{"action":"configure","time":"09:00"}`, `{"action":"claim","enabled":true}`, `{"action":"unknown"}`, `{"action":"claim","extra":true}`, `{"action":"claim"}{}`} {
		if post(body, "application/json").Code != 400 {
			t.Fatal("invalid action accepted", body)
		}
	}
	if f.claims != 0 || f.configures != 0 {
		t.Fatal("invalid input changed state")
	}
	if post(`{"action":"configure","enabled":true,"time":"09:00"}`, "application/json").Code != 200 || f.configures != 1 {
		t.Fatal("schedule not saved")
	}
	f.err = errors.New("scheduled check-in failed")
	w := post(`{"action":"claim"}`, "application/json")
	var got map[string]any
	if json.Unmarshal(w.Body.Bytes(), &got) != nil || w.Code != 422 || got["checkin"] == nil {
		t.Fatal("upstream failure hid current state")
	}
	a.cfg.Auth.APIKey = "test-gateway"
	if post(`{"action":"claim"}`, "application/json").Code != 401 || f.claims != 1 {
		t.Fatal("authentication bypassed")
	}
}
