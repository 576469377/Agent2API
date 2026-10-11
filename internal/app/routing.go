package app

import (
	"context"
	"strings"

	"github.com/576469377/Agent2API/internal/llm"
)

// openRoutedStream only falls back before an upstream stream has been accepted.
// Once accepted, every event in that response stays on that platform.
func (a *App) openRoutedStream(ctx context.Context, req llm.RequestMessages, hint string) (llm.ResponseStream, *PlatformRuntime, error) {
	primary := a.hub.RouteWithHint(req.Model, hint)
	stream, err := primary.Adapter.Stream(ctx, req)
	if err == nil || hint != "" || !a.cfg.Models.FallbackOnUnavailable || ctx.Err() != nil {
		return stream, primary, err
	}
	failure := llm.Wrap(err)
	eligible := func(f *llm.Failure) bool {
		return f.RateLimited || f.QuotaExhausted || strings.HasPrefix(f.Code, "http_5")
	}
	if !eligible(failure) {
		return stream, primary, err
	}
	a.hub.mu.RLock()
	var alternatives []*PlatformRuntime
	for _, pid := range a.hub.order {
		if pid == primary.ID {
			continue
		}
		for _, m := range a.hub.catalog[pid] {
			if m.ID == req.Model {
				alternatives = append(alternatives, a.hub.platforms[pid])
				break
			}
		}
	}
	a.hub.mu.RUnlock()
	for _, rt := range alternatives {
		if ctx.Err() != nil {
			break
		}
		a.logger.Printf("模型 %s: %s 在建流前拒绝请求，尝试同名模型 %s", req.Model, primary.ID, rt.ID)
		next, nextErr := rt.Adapter.Stream(ctx, req)
		if nextErr == nil {
			return next, rt, nil
		}
		// The final error and metrics must describe the last actual attempt,
		// rather than hiding it behind the primary platform's earlier failure.
		stream, primary, err = next, rt, nextErr
		if !eligible(llm.Wrap(nextErr)) {
			break
		}
	}
	return stream, primary, err
}
