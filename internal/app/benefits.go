package app

import (
	"context"
	"net/http"
	"time"

	"github.com/576469377/Agent2API/internal/adapter"
)

func (a *App) accountForBenefits(w http.ResponseWriter, r *http.Request) (adapter.Adapter, bool) {
	rt, ok := a.platformOr400(w, r)
	if !ok {
		return nil, false
	}
	rows := rt.AccountStatuses()
	label := r.URL.Query().Get("label")
	if label == "" && len(rows) != 1 {
		http.Error(w, "此平台有多个账号，请指定 label", http.StatusBadRequest)
		return nil, false
	}
	for _, row := range rows {
		if label == "" || row.Label == label {
			return row.Adapter, true
		}
	}
	http.Error(w, "账号不存在", http.StatusNotFound)
	return nil, false
}

func (a *App) apiAccountBenefits(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", "GET")
		http.Error(w, "仅支持 GET", http.StatusMethodNotAllowed)
		return
	}
	selected, ok := a.accountForBenefits(w, r)
	if !ok {
		return
	}
	p, ok := selected.(adapter.BenefitsProvider)
	if !ok {
		http.Error(w, "此账号未提供积分或额度查询", http.StatusBadRequest)
		return
	}
	if r.URL.Query().Get("refresh") == "1" {
		ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
		defer cancel()
		if p.RefreshAccountBenefits(ctx) != nil {
			writeJSONWithStatus(w, http.StatusUnprocessableEntity, map[string]any{"error": map[string]string{"message": "上游积分或额度读取失败，已保留上次有效读数"}, "benefits": p.AccountBenefits()})
			return
		}
	}
	writeJSON(w, map[string]any{"benefits": p.AccountBenefits()})
}

// Discover pooled adapters as well as single connections. A new/relogged account
// joins automatically; account-list GETs never trigger network IO or claims.
func (a *App) runAccountBackground(ctx context.Context) {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	last := map[string]time.Time{}
	type accountJob struct {
		source adapter.Adapter
		ctx    context.Context
		cancel context.CancelFunc
	}
	jobs := map[string]accountJob{}
	defer func() {
		for _, job := range jobs {
			job.cancel()
		}
	}()
	capacity := make(chan struct{}, 3)
	for {
		if ctx.Err() != nil {
			return
		}
		present := map[string]bool{}
		for _, rt := range a.hub.Platforms() {
			for _, row := range rt.AccountStatuses() {
				key := rt.ID + "\x00" + row.Label
				present[key] = true
				job, exists := jobs[key]
				if !exists || job.source != row.Adapter {
					if exists {
						job.cancel()
					}
					child, cancel := context.WithCancel(ctx)
					job = accountJob{source: row.Adapter, ctx: child, cancel: cancel}
					jobs[key] = job
					delete(last, key)
				}
				if p, ok := row.Adapter.(interface{ StartBackground(context.Context) }); ok {
					p.StartBackground(job.ctx)
				}
				p, ok := row.Adapter.(adapter.BenefitsProvider)
				if !ok {
					continue
				}
				if time.Since(last[key]) < 5*time.Minute {
					continue
				}
				view := p.AccountBenefits()
				if view.CheckedAt != nil && time.Since(*view.CheckedAt) < 5*time.Minute && view.Status == "ready" {
					continue
				}
				select {
				case capacity <- struct{}{}:
					last[key] = time.Now()
					go func(p adapter.BenefitsProvider, parent context.Context) {
						defer func() { <-capacity }()
						job, cancel := context.WithTimeout(parent, 30*time.Second)
						defer cancel()
						_ = p.RefreshAccountBenefits(job)
					}(p, job.ctx)
				case <-ctx.Done():
					return
				default:
				}
			}
		}
		for key := range last {
			if !present[key] {
				delete(last, key)
			}
		}
		for key, job := range jobs {
			if !present[key] {
				job.cancel()
				delete(jobs, key)
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}
