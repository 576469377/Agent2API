package workbuddy

import (
	"net/http"
	"testing"
	"time"

	"github.com/576469377/Agent2API/internal/adapter"
)

func TestDescribeNeverFetchesCatalogOrRefreshesCredentials(t *testing.T) {
	a, upstreamCalls, refreshCalls := newRetryTestAdapter(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	})
	if got := a.Describe(); got.Models != 0 || got.Status != "active" {
		t.Fatalf("unexpected local state: %+v", got)
	}
	a.models.set([]adapter.ModelInfo{{ID: "cached"}})
	a.models.mu.Lock()
	a.models.fetchedAt = time.Now().Add(-time.Hour)
	a.models.mu.Unlock()
	if got := a.Describe(); got.Models != 1 {
		t.Fatal("expired cache count lost")
	}
	a.InvalidateModels()
	_ = a.Describe()
	if upstreamCalls.Load() != 0 || refreshCalls.Load() != 0 {
		t.Fatal("status read performed upstream I/O")
	}
}
