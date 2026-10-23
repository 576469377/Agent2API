package app

import (
	"context"
	"errors"
	"reflect"
	"sync"
	"testing"
	"time"
)

type catalogWaitingContext struct {
	context.Context
	entered chan struct{}
	once    sync.Once
}

func (c *catalogWaitingContext) Done() <-chan struct{} {
	c.once.Do(func() { close(c.entered) })
	return c.Context.Done()
}

func TestCatalogCallerCancellationPreservesPublishedState(t *testing.T) {
	for _, prior := range []string{"ready", "error", "unknown"} {
		t.Run(prior, func(t *testing.T) {
			ad := &controlledCatalog{}
			rt := &PlatformRuntime{ID: "workbuddy-global", Adapter: ad}
			h, err := NewHub([]*PlatformRuntime{rt}, nil)
			if err != nil {
				t.Fatal(err)
			}
			if prior == "error" {
				ad.mu.Lock()
				ad.err = errors.New("real upstream rejection")
				ad.mu.Unlock()
				if err := h.RefreshPlatform(context.Background(), rt.ID); err == nil {
					t.Fatal("fixture did not publish upstream failure")
				}
			} else if prior == "unknown" {
				delete(h.catalogStatus, rt.ID)
				delete(h.catalog, rt.ID)
				h.index = map[string]string{}
			}
			beforeStatus, _ := h.CatalogStatuses()
			beforeModels := h.CachedModels()
			gate, entered := make(chan struct{}), make(chan struct{}, 1)
			ad.mu.Lock()
			ad.gate, ad.entered = gate, entered
			ad.mu.Unlock()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			ownerDone := make(chan error, 1)
			go func() { ownerDone <- h.RefreshPlatform(ctx, rt.ID) }()
			<-entered
			waitCtx := &catalogWaitingContext{Context: context.Background(), entered: make(chan struct{})}
			waiterDone := make(chan error, 1)
			go func() { waiterDone <- h.refreshPlatformCatalog(waitCtx, rt, false) }()
			<-waitCtx.entered
			cancel()
			for _, done := range []<-chan error{ownerDone, waiterDone} {
				select {
				case err := <-done:
					if !errors.Is(err, context.Canceled) {
						t.Fatalf("canceled flight reported %v", err)
					}
				case <-time.After(time.Second):
					t.Fatal("canceled catalogue flight did not release its waiters")
				}
			}
			afterStatus, refreshing := h.CatalogStatuses()
			if refreshing || !reflect.DeepEqual(beforeStatus, afterStatus) || !reflect.DeepEqual(beforeModels, h.CachedModels()) {
				t.Fatalf("client cancellation changed published state: before=%+v after=%+v refreshing=%v", beforeStatus, afterStatus, refreshing)
			}
			ad.mu.Lock()
			ad.gate, ad.entered, ad.err = nil, nil, nil
			ad.mu.Unlock()
			if err := h.RefreshPlatform(context.Background(), rt.ID); err != nil {
				t.Fatalf("fresh refresh after cancellation failed: %v", err)
			}
			recovered, refreshing := h.CatalogStatuses()
			if refreshing || recovered[0].Status != "ready" || recovered[0].Error != "" {
				t.Fatalf("fresh refresh did not recover: %+v", recovered)
			}
		})
	}
}

func TestCatalogDeadlineStillPublishesFailure(t *testing.T) {
	ad := &controlledCatalog{}
	h, err := NewHub([]*PlatformRuntime{{ID: "workbuddy-global", Adapter: ad}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	before, _ := h.CatalogStatuses()
	ad.mu.Lock()
	ad.gate = make(chan struct{})
	ad.mu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	if err := h.RefreshPlatform(ctx, "workbuddy-global"); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("deadline returned %v", err)
	}
	status, refreshing := h.CatalogStatuses()
	if refreshing || status[0].Status != "error" || status[0].Error != context.DeadlineExceeded.Error() || !status[0].UpdatedAt.Equal(*before[0].UpdatedAt) || len(h.CachedModels()) != 1 {
		t.Fatalf("deadline was hidden or changed cached data: %+v", status)
	}
}

func TestCatalogAdapterCancellationWithoutCallerCancellationIsFailure(t *testing.T) {
	ad := &controlledCatalog{}
	h, err := NewHub([]*PlatformRuntime{{ID: "workbuddy-global", Adapter: ad}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	ad.mu.Lock()
	ad.err = context.Canceled
	ad.mu.Unlock()
	if err := h.RefreshPlatform(context.Background(), "workbuddy-global"); !errors.Is(err, context.Canceled) {
		t.Fatalf("adapter cancellation returned %v", err)
	}
	status, _ := h.CatalogStatuses()
	if status[0].Status != "error" || status[0].Error != context.Canceled.Error() {
		t.Fatalf("unrelated adapter cancellation was silently ignored: %+v", status)
	}
}
