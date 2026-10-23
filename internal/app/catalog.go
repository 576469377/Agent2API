package app

import (
	"context"
	"sort"
	"time"

	"github.com/576469377/Agent2API/internal/adapter"
)

type CatalogStatus struct {
	Platform   string     `json:"platform"`
	Status     string     `json:"status"`
	ModelCount int        `json:"model_count"`
	UpdatedAt  *time.Time `json:"updated_at,omitempty"`
	Error      string     `json:"error,omitempty"`
}

// CachedModels serves management views without performing upstream I/O.
func (h *Hub) CachedModels() []ModelInfo {
	h.mu.RLock()
	out := []ModelInfo{}
	for _, pid := range h.order {
		for _, model := range h.catalog[pid] {
			out = append(out, ModelInfo{ModelInfo: model, Platform: pid, RoutingID: pid + "/" + model.ID, DefaultRoute: h.index[model.ID] == pid})
		}
	}
	h.mu.RUnlock()
	out = append(out, aliasModelInfos(h.Aliases())...)
	sort.Slice(out, func(i, j int) bool {
		if out[i].Platform != out[j].Platform {
			return out[i].Platform < out[j].Platform
		}
		return out[i].ID < out[j].ID
	})
	return out
}

func (h *Hub) CatalogStatuses() ([]CatalogStatus, bool) {
	h.mu.RLock()
	defer h.mu.RUnlock()
	out := make([]CatalogStatus, 0, len(h.order))
	for _, pid := range h.order {
		status, ok := h.catalogStatus[pid]
		if !ok {
			status = CatalogStatus{Platform: pid, Status: "unknown"}
		}
		status.ModelCount = len(h.catalog[pid])
		out = append(out, status)
	}
	return out, h.catalogFlight != nil
}

func (h *Hub) RefreshCatalogIfDue() {
	h.mu.RLock()
	due := h.catalogFlight == nil && time.Since(h.catalogChecked) >= time.Minute
	h.mu.RUnlock()
	if due {
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			_, _ = h.ListModels(ctx)
		}()
	}
}

// Coalesce concurrent refreshes; fetch regions in parallel and retain each
// region's last good data independently when another region is unavailable.
func (h *Hub) refreshCatalog(ctx context.Context) error {
	return h.refreshCatalogFor(ctx, "", false)
}

// RefreshPlatform 只重新检测指定连接，避免影响其他平台的缓存和状态。
func (h *Hub) RefreshPlatform(ctx context.Context, platform string) error {
	return h.refreshCatalogFor(ctx, platform, true)
}

// RefreshAllPlatforms 等待旧刷新结束后再使缓存失效，确保手动检查取得本次费率。
func (h *Hub) RefreshAllPlatforms(ctx context.Context) error {
	return h.refreshCatalogFor(ctx, "", true)
}

func (h *Hub) refreshCatalogFor(ctx context.Context, only string, fresh bool) error {
	h.mu.Lock()
	if flight := h.catalogFlight; flight != nil {
		h.mu.Unlock()
		select {
		case <-flight:
			if fresh {
				return h.refreshCatalogFor(ctx, only, fresh)
			}
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	flight := make(chan struct{})
	h.catalogFlight = flight
	h.mu.Unlock()
	type result struct {
		platform string
		models   []adapter.ModelInfo
		err      error
	}
	runtimes := h.Platforms()
	results := make(chan result, len(runtimes))
	pending := map[string]bool{}
	for _, rt := range runtimes {
		if only != "" && rt.ID != only {
			continue
		}
		if fresh {
			if c, ok := rt.Adapter.(interface{ InvalidateModels() }); ok {
				c.InvalidateModels()
			}
		}
		pending[rt.ID] = true
		go func(rt *PlatformRuntime) {
			models, err := rt.Adapter.ListModels(ctx)
			results <- result{rt.ID, models, err}
		}(rt)
	}
	updates := make(map[string]result, len(runtimes))
	for len(pending) > 0 {
		select {
		case row := <-results:
			updates[row.platform] = row
			delete(pending, row.platform)
		case <-ctx.Done():
			for pid := range pending {
				updates[pid] = result{platform: pid, err: ctx.Err()}
			}
			clear(pending)
		}
	}
	stamp := time.Now().UTC()
	h.mu.Lock()
	for pid, row := range updates {
		status := h.catalogStatus[pid]
		status.Platform = pid
		if row.err != nil {
			status.Status, status.Error = "error", row.err.Error()
		} else {
			h.catalog[pid] = row.models
			status.Status, status.Error, status.UpdatedAt = "ready", "", &stamp
		}
		status.ModelCount = len(h.catalog[pid])
		h.catalogStatus[pid] = status
	}
	h.index = map[string]string{}
	for _, pid := range h.order {
		for _, model := range h.catalog[pid] {
			if _, exists := h.index[model.ID]; !exists {
				h.index[model.ID] = pid
			}
		}
	}
	h.catalogChecked = time.Now()
	h.catalogFlight = nil
	close(flight)
	h.mu.Unlock()
	if only != "" {
		return updates[only].err
	}
	return ctx.Err()
}
