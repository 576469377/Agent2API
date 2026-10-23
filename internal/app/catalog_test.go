package app

import (
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/576469377/Agent2API/internal/adapter"
	"github.com/576469377/Agent2API/internal/config"
)

type controlledCatalog struct {
	recordingAdapter
	mu                 sync.Mutex
	calls              int
	gate               <-chan struct{}
	entered            chan<- struct{}
	err                error
	ignoreCancellation bool
}

func (a *controlledCatalog) ListModels(ctx context.Context) ([]adapter.ModelInfo, error) {
	a.mu.Lock()
	a.calls++
	gate, entered, err, ignoreCancellation := a.gate, a.entered, a.err, a.ignoreCancellation
	a.mu.Unlock()
	if entered != nil {
		entered <- struct{}{}
	}
	if gate != nil {
		if ignoreCancellation {
			<-gate
		} else {
			select {
			case <-gate:
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}
	}
	return []adapter.ModelInfo{{ID: "shared"}}, err
}

func TestConsoleCatalogDoesNotWaitForRefreshAndKeepsRegionsOnFailure(t *testing.T) {
	cn, global := &controlledCatalog{}, &controlledCatalog{}
	h, err := NewHub([]*PlatformRuntime{{ID: "workbuddy", Adapter: cn}, {ID: "workbuddy-global", Adapter: global}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	cfg := config.Default()
	cfg.MetricsFile = ""
	a := New(cfg, h, h.logger)
	gate, entered := make(chan struct{}), make(chan struct{}, 4)
	for _, ad := range []*controlledCatalog{cn, global} {
		ad.mu.Lock()
		ad.gate, ad.entered = gate, entered
		ad.mu.Unlock()
	}
	global.mu.Lock()
	global.err = errors.New("catalogue unavailable")
	global.mu.Unlock()
	finished := make(chan struct{})
	go func() { defer close(finished); _, _ = h.ListModels(context.Background()) }()
	var release sync.Once
	defer func() { release.Do(func() { close(gate) }); <-finished }()
	// Both platforms must start before either is released.
	for i := 0; i < 2; i++ {
		select {
		case <-entered:
		case <-time.After(time.Second):
			t.Fatal("catalogue refresh serialized across platforms")
		}
	}
	response := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		w := httptest.NewRecorder()
		a.apiModels(w, httptest.NewRequest("GET", "/api/models", nil))
		response <- w
	}()
	select {
	case w := <-response:
		var got struct {
			Models    []ModelInfo `json:"models"`
			Platforms []struct {
				Models []adapter.ModelInfo `json:"models"`
			} `json:"platforms"`
			Refreshing bool `json:"refreshing"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
			t.Fatal(err)
		}
		if !got.Refreshing || len(got.Models) != 2 || len(got.Platforms) != 2 {
			t.Fatalf("cached regions missing: %s", w.Body.String())
		}
	case <-time.After(time.Second):
		t.Fatal("management API waited for upstream")
	}
	// A second caller joins the in-flight refresh without new upstream calls.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, _ = h.ListModels(ctx)
	for _, ad := range []*controlledCatalog{cn, global} {
		ad.mu.Lock()
		calls := ad.calls
		ad.mu.Unlock()
		if calls != 2 {
			t.Fatalf("duplicate catalogue reads: %d", calls)
		}
	}
	release.Do(func() { close(gate) })
	<-finished
	statuses, refreshing := h.CatalogStatuses()
	if refreshing || len(h.CachedModels()) != 2 || statuses[0].Status != "ready" || statuses[1].Status != "error" || statuses[1].ModelCount != 1 {
		t.Fatalf("failed region must retain data and expose failure: %+v", statuses)
	}
}

func TestSelectedCatalogRefreshDoesNotWaitForOtherPlatform(t *testing.T) {
	fast, slow := &controlledCatalog{}, &controlledCatalog{}
	h, err := NewHub([]*PlatformRuntime{{ID: "workbuddy", Adapter: fast}, {ID: "cursor", Adapter: slow}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	gate, entered := make(chan struct{}), make(chan struct{}, 1)
	slow.mu.Lock()
	slow.gate, slow.entered = gate, entered
	slow.mu.Unlock()
	finished := make(chan error, 1)
	go func() { finished <- h.RefreshPlatform(context.Background(), "cursor") }()
	defer func() { close(gate); <-finished }()
	<-entered
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := h.RefreshPlatform(ctx, "workbuddy"); err != nil {
		t.Fatalf("unrelated platform blocked selected catalogue: %v", err)
	}
	a := &App{hub: h}
	if err := a.verifyClientCatalog(ctx, []string{"workbuddy"}); err != nil {
		t.Fatalf("unrelated refresh blocked client configuration: %v", err)
	}
	statuses, refreshing := h.CatalogStatuses()
	if !refreshing || statuses[0].Status != "ready" {
		t.Fatalf("selected platform was not independently published: %+v", statuses)
	}
}

func TestHubInitialCatalogsStartInParallel(t *testing.T) {
	gate, entered := make(chan struct{}), make(chan struct{}, 2)
	first, second := &controlledCatalog{gate: gate, entered: entered}, &controlledCatalog{gate: gate, entered: entered}
	done := make(chan error, 1)
	go func() {
		_, err := NewHub([]*PlatformRuntime{{ID: "workbuddy", Adapter: first}, {ID: "cursor", Adapter: second}}, nil)
		done <- err
	}()
	defer func() { close(gate); <-done }()
	for i := 0; i < 2; i++ {
		select {
		case <-entered:
		case <-time.After(time.Second):
			t.Fatal("startup waits for the first platform before querying the next")
		}
	}
}

func TestFullCatalogRefreshAlsoFetchesPlatformsOutsideCurrentFlight(t *testing.T) {
	fast, slow := &controlledCatalog{}, &controlledCatalog{}
	h, err := NewHub([]*PlatformRuntime{{ID: "workbuddy", Adapter: fast}, {ID: "cursor", Adapter: slow}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	gate, entered, otherEntered := make(chan struct{}), make(chan struct{}, 1), make(chan struct{}, 1)
	slow.mu.Lock()
	slow.gate, slow.entered = gate, entered
	slow.mu.Unlock()
	fast.mu.Lock()
	fast.entered = otherEntered
	fast.mu.Unlock()
	selectedDone, allDone := make(chan error, 1), make(chan error, 1)
	go func() { selectedDone <- h.RefreshPlatform(context.Background(), "cursor") }()
	<-entered
	go func() { _, err := h.ListModels(context.Background()); allDone <- err }()
	defer func() { close(gate); <-selectedDone; <-allDone }()
	select {
	case <-otherEntered:
	case <-time.After(time.Second):
		t.Fatal("full refresh joined a selected platform without fetching the others")
	}
}

func TestTargetedCatalogCancellationKeepsCacheAndReleasesFlight(t *testing.T) {
	ad := &controlledCatalog{}
	h, err := NewHub([]*PlatformRuntime{{ID: "workbuddy", Adapter: ad}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	before, _ := h.CatalogStatuses()
	gate, entered := make(chan struct{}), make(chan struct{}, 1)
	ad.mu.Lock()
	ad.gate, ad.entered, ad.ignoreCancellation = gate, entered, true
	ad.mu.Unlock()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- h.RefreshPlatform(ctx, "workbuddy") }()
	defer close(gate)
	<-entered
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("got %v, want cancellation", err)
		}
	case <-time.After(time.Second):
		t.Fatal("cancelled catalogue request is still blocked on upstream")
	}
	statuses, refreshing := h.CatalogStatuses()
	if refreshing || statuses[0].Status != "ready" || statuses[0].Error != "" || !statuses[0].UpdatedAt.Equal(*before[0].UpdatedAt) || len(h.CachedModels()) != 1 {
		t.Fatalf("cancelled refresh changed its cached catalogue: %+v", statuses)
	}
}

func TestScheduledCatalogRefreshDoesNotWaitForOtherPlatform(t *testing.T) {
	fast, slow := &controlledCatalog{}, &controlledCatalog{}
	h, err := NewHub([]*PlatformRuntime{{ID: "workbuddy", Adapter: fast}, {ID: "cursor", Adapter: slow}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	gate, entered, otherEntered := make(chan struct{}), make(chan struct{}, 1), make(chan struct{}, 1)
	slow.mu.Lock()
	slow.gate, slow.entered = gate, entered
	slow.mu.Unlock()
	fast.mu.Lock()
	fast.entered = otherEntered
	fast.mu.Unlock()
	done := make(chan error, 1)
	go func() { done <- h.RefreshPlatform(context.Background(), "cursor") }()
	defer func() { close(gate); <-done }()
	<-entered
	h.mu.Lock()
	h.catalogChecked = time.Now().Add(-2 * time.Minute)
	h.mu.Unlock()
	h.RefreshCatalogIfDue()
	select {
	case <-otherEntered:
	case <-time.After(time.Second):
		t.Fatal("scheduled refresh skipped idle platforms while another was refreshing")
	}
}
