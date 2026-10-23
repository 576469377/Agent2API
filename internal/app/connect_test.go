package app

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/576469377/Agent2API/internal/adapter"
	"github.com/576469377/Agent2API/internal/config"
)

type setupFixture struct {
	fakeAdapter
	calls, invalidations, saves, checks int
	err                                 error
}

func (f *setupFixture) ListModels(context.Context) ([]adapter.ModelInfo, error) {
	f.calls++
	return []adapter.ModelInfo{{ID: "free-model"}}, f.err
}
func (f *setupFixture) InvalidateModels()                        { f.invalidations++ }
func (f *setupFixture) SaveAPIKey(context.Context, string) error { f.saves++; return f.err }
func (f *setupFixture) VerifyConnection(context.Context) error   { f.checks++; return f.err }

func setupApp(t *testing.T, id string, fixture *setupFixture) *App {
	t.Helper()
	hub, err := NewHub([]*PlatformRuntime{{ID: id, Adapter: fixture, Cfg: config.PlatformConfig{AccountsDir: filepath.Join(t.TempDir(), "must-not-create")}}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	cfg := config.Default()
	cfg.MetricsFile = ""
	return New(cfg, hub, hub.logger)
}

func TestEveryImplementedPlatformHasDiscoverableOnboarding(t *testing.T) {
	a := setupApp(t, "workbuddy", &setupFixture{})
	w := httptest.NewRecorder()
	a.apiPlatforms(w, httptest.NewRequest("GET", "/api/platforms", nil))
	var got platformsResponse
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	seen := map[string]*adapter.ConnectionSetup{}
	for _, p := range got.Active {
		seen[p.ID] = p.Connection
	}
	for _, p := range got.Available {
		seen[p.ID] = p.Connection
	}
	for _, p := range builtinPlatforms {
		if p.Implemented && seen[p.ID] == nil {
			t.Fatalf("implemented platform %s has no account setup", p.ID)
		}
		if !p.Implemented && seen[p.ID] != nil {
			t.Fatalf("planned platform %s advertised as implemented", p.ID)
		}
	}
	implemented := 0
	for _, platform := range builtinPlatforms {
		if platform.Implemented {
			implemented++
		}
	}
	if len(got.Active) != 1 || len(got.Available) != implemented-1 {
		t.Fatal("enabled and available platforms were confused")
	}
}

func TestNonBrowserPlatformsNeverCreateWorkBuddyCredentialDirectories(t *testing.T) {
	for _, id := range []string{"openrouter", "cursor"} {
		a := setupApp(t, id, &setupFixture{})
		w := httptest.NewRecorder()
		a.startAccountLogin(w, httptest.NewRequest("POST", "/api/accounts/manage?platform="+id, nil))
		if w.Code != 400 {
			t.Fatal("unsupported browser login accepted")
		}
		if _, err := os.Stat(a.hub.Platform(id).AccountsDir()); !os.IsNotExist(err) {
			t.Fatal("created a pool for a direct integration")
		}
	}
}

func TestKiroConnectionCheckUsesDirectAdapterAndRejectsUnknownLoginProvider(t *testing.T) {
	f := &setupFixture{}
	a := setupApp(t, "kiro", f)
	r := httptest.NewRequest("POST", "/api/accounts/connect?platform=kiro", strings.NewReader(`{"action":"check"}`))
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	a.apiAccountsConnect(w, r)
	if w.Code != 200 || f.checks != 1 || f.saves != 0 || f.invalidations != 1 {
		t.Fatalf("Kiro connection did not refresh its direct adapter: %d %s", w.Code, w.Body.String())
	}
	w = httptest.NewRecorder()
	a.startAccountLogin(w, httptest.NewRequest("POST", "/api/accounts/manage?platform=kiro&provider=unknown", nil))
	if w.Code != 400 {
		t.Fatal("unknown Kiro provider accepted")
	}
	if _, e := os.Stat(a.hub.Platform("kiro").AccountsDir()); !os.IsNotExist(e) {
		t.Fatal("Kiro created a WorkBuddy pool")
	}
}

func TestConnectionActionsValidateInputAndReportUpstreamAuthWithoutGateway401(t *testing.T) {
	f := &setupFixture{}
	a := setupApp(t, "openrouter", f)
	call := func(body, contentType string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("POST", "/api/accounts/connect?platform=openrouter", strings.NewReader(body))
		r.Header.Set("Content-Type", contentType)
		w := httptest.NewRecorder()
		a.Handler().ServeHTTP(w, r)
		return w
	}
	for _, body := range []string{`{"action":"save_key","extra":true}`, `{"action":"check"}{}`, `{"action":"unknown"}`, `{"action":"check","api_key":"secret"}`} {
		if call(body, "application/json").Code != 400 {
			t.Fatal("invalid input accepted")
		}
	}
	if call(`{"action":"save_key"}`, "text/plain").Code != 415 {
		t.Fatal("cross-origin form content accepted")
	}
	if f.saves != 0 || f.checks != 0 {
		t.Fatal("invalid inputs reached credentials")
	}
	if w := call(`{"action":"save_key","api_key":"fixture"}`, "application/json"); w.Code != 200 || !strings.Contains(w.Body.String(), `"model_count":1`) {
		t.Fatalf("save: %d %s", w.Code, w.Body.String())
	}
	f.err = errors.New("upstream credential rejected")
	if w := call(`{"action":"check"}`, "application/json"); w.Code != 422 {
		t.Fatalf("upstream auth failure misreported as gateway auth: %d", w.Code)
	}
}

func TestConnectionEndpointRequiresGatewayAuthentication(t *testing.T) {
	a := setupApp(t, "cursor", &setupFixture{})
	a.cfg.Auth.APIKey = "gateway-secret"
	r := httptest.NewRequest("POST", "/api/accounts/connect?platform=cursor", strings.NewReader(`{"action":"check"}`))
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	a.Handler().ServeHTTP(w, r)
	if w.Code != http.StatusUnauthorized {
		t.Fatal("connection endpoint bypassed gateway authentication")
	}
}

func TestTargetedRefreshRetainsOtherProvidersAndReturnsTargetFailure(t *testing.T) {
	one, other := &setupFixture{}, &setupFixture{}
	h, err := NewHub([]*PlatformRuntime{{ID: "cursor", Adapter: one}, {ID: "workbuddy", Adapter: other}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	one.err = errors.New("desktop login missing")
	if err := h.RefreshPlatform(context.Background(), "cursor"); err == nil {
		t.Fatal("failed desktop detection reported success")
	}
	if one.calls != 2 || one.invalidations != 1 || other.calls != 1 || other.invalidations != 0 {
		t.Fatal("targeted check disturbed another provider")
	}
	if len(h.CachedModels()) != 2 {
		t.Fatal("failed check discarded previous catalogues")
	}
}

type gatedSetupFixture struct {
	fakeAdapter
	calls, invalidations atomic.Int32
	entered              chan struct{}
	release              <-chan struct{}
}

func (f *gatedSetupFixture) ListModels(ctx context.Context) ([]adapter.ModelInfo, error) {
	if f.calls.Add(1) == 2 {
		close(f.entered)
		select {
		case <-f.release:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
		return []adapter.ModelInfo{{ID: "old-model"}}, nil
	}
	return []adapter.ModelInfo{{ID: "fresh-model"}}, nil
}

func (f *gatedSetupFixture) InvalidateModels() { f.invalidations.Add(1) }

// Done is evaluated as a select is entered. Both old requests remain gated,
// so the only three selects are the full-refresh collector and its two waiters.
type catalogWaitContext struct {
	context.Context
	selects chan struct{}
}

func (c catalogWaitContext) Done() <-chan struct{} {
	select {
	case c.selects <- struct{}{}:
	default:
	}
	return c.Context.Done()
}

func TestExplicitCatalogRefreshInvalidatesBothProvidersAfterPreviousFlight(t *testing.T) {
	gate := make(chan struct{})
	one := &gatedSetupFixture{entered: make(chan struct{}), release: gate}
	other := &gatedSetupFixture{entered: make(chan struct{}), release: gate}
	h, err := NewHub([]*PlatformRuntime{{ID: "cursor", Adapter: one}, {ID: "workbuddy", Adapter: other}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	previous := make(chan error, 2)
	for _, rt := range h.Platforms() {
		go func(rt *PlatformRuntime) { previous <- h.refreshPlatformCatalog(ctx, rt, false) }(rt)
	}
	for _, fixture := range []*gatedSetupFixture{one, other} {
		select {
		case <-fixture.entered:
		case <-ctx.Done():
			t.Fatal("previous catalogue request did not enter the adapter")
		}
	}
	waiting := catalogWaitContext{Context: ctx, selects: make(chan struct{}, 3)}
	done := make(chan error, 1)
	go func() { done <- h.RefreshAllPlatforms(waiting) }()
	for range 3 {
		select {
		case <-waiting.selects:
		case <-ctx.Done():
			t.Fatal("explicit refresh did not wait for both previous flights")
		}
	}
	if one.calls.Load() != 2 || other.calls.Load() != 2 || one.invalidations.Load() != 0 || other.invalidations.Load() != 0 {
		t.Fatal("explicit refresh invalidated or queried before previous flights finished")
	}
	close(gate)
	for range 2 {
		if err := <-previous; err != nil {
			t.Fatal(err)
		}
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if one.invalidations.Load() != 1 || other.invalidations.Load() != 1 || one.calls.Load() != 3 || other.calls.Load() != 3 {
		t.Fatal("explicit refresh reused a previous flight or stale adapter caches")
	}
	for _, model := range h.CachedModels() {
		if model.ID != "fresh-model" {
			t.Fatalf("explicit refresh published an old snapshot: %s", model.ID)
		}
	}
}
