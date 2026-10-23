package main

import (
	"context"
	"time"
)

// runPeriodic waits one interval before the first pass and stops with the server.
func runPeriodic(ctx context.Context, interval time.Duration, run func()) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if ctx.Err() != nil {
				return
			}
			run()
		}
	}
}
