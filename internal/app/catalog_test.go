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
	mu      sync.Mutex
	calls   int
	gate    <-chan struct{}
	entered chan<- struct{}
	err     error
}

func (a *controlledCatalog) ListModels(ctx context.Context) ([]adapter.ModelInfo, error) {
	a.mu.Lock()
	a.calls++
	gate, entered, err := a.gate, a.entered, a.err
	a.mu.Unlock()
	if entered != nil {
		entered <- struct{}{}
	}
	if gate != nil {
		select {
		case <-gate:
		case <-ctx.Done():
			return nil, ctx.Err()
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
