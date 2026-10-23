package app

import (
	"context"
	"fmt"
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

type catalogRefresh struct {
	done chan struct{}
	err  error
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
	return out, len(h.catalogFlights) != 0
}

func (h *Hub) RefreshCatalogIfDue() {
	h.mu.Lock()
	due := time.Since(h.catalogChecked) >= time.Minute
	if due {
		h.catalogChecked = time.Now()
	}
	h.mu.Unlock()
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
	if err := ctx.Err(); err != nil {
		return err
	}
	if only != "" {
		rt := h.Platform(only)
		if rt == nil {
			return fmt.Errorf("平台 %s 未配置", only)
		}
		return h.refreshPlatformCatalog(ctx, rt, fresh)
	}
	runtimes := h.Platforms()
	results := make(chan struct{}, len(runtimes))
	for _, rt := range runtimes {
		go func(rt *PlatformRuntime) {
			_ = h.refreshPlatformCatalog(ctx, rt, fresh)
			results <- struct{}{}
		}(rt)
	}
	for range runtimes {
		select {
		case <-results:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	h.mu.Lock()
	h.catalogChecked = time.Now()
	h.mu.Unlock()
	return ctx.Err()
}

func (h *Hub) refreshPlatformCatalog(ctx context.Context, rt *PlatformRuntime, fresh bool) error {
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		h.mu.Lock()
		previous := h.catalogFlights[rt.ID]
		if previous == nil {
			break // Keep the lock until this platform's flight is reserved.
		}
		h.mu.Unlock()
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-previous.done:
			if !fresh {
				return previous.err
			}
		}
	}
	flight := &catalogRefresh{done: make(chan struct{})}
	h.catalogFlights[rt.ID] = flight
	h.mu.Unlock()

	if fresh {
		if c, ok := rt.Adapter.(interface{ InvalidateModels() }); ok {
			c.InvalidateModels()
		}
	}
	type result struct {
		models []adapter.ModelInfo
		err    error
	}
	results := make(chan result, 1)
	go func() {
		models, err := rt.Adapter.ListModels(ctx)
		results <- result{models, err}
	}()
	var row result
	select {
	case row = <-results:
		if row.err == nil {
			row.err = ctx.Err()
		}
	case <-ctx.Done():
		row.err = ctx.Err()
	}
	stamp := time.Now().UTC()
	h.mu.Lock()
	status := h.catalogStatus[rt.ID]
	status.Platform = rt.ID
	if row.err != nil {
		status.Status, status.Error = "error", row.err.Error()
	} else {
		h.catalog[rt.ID] = row.models
		status.Status, status.Error, status.UpdatedAt = "ready", "", &stamp
	}
	status.ModelCount = len(h.catalog[rt.ID])
	h.catalogStatus[rt.ID] = status
	h.index = map[string]string{}
	for _, pid := range h.order {
		for _, model := range h.catalog[pid] {
			if _, exists := h.index[model.ID]; !exists {
				h.index[model.ID] = pid
			}
		}
	}
	flight.err = row.err
	delete(h.catalogFlights, rt.ID)
	close(flight.done)
	h.mu.Unlock()
	return row.err
}
