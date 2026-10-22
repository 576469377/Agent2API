package app

import (
	"context"
	"net/http"
	"strconv"

	"github.com/576469377/Agent2API/internal/llm"
)

func writeExecutionHeaders(w http.ResponseWriter, ctx context.Context) {
	if execution := llm.ExecutionFrom(ctx); execution != nil {
		trace := execution.Snapshot()
		w.Header().Set("X-Upstream-Attempts", strconv.Itoa(trace.AttemptCount))
		w.Header().Set("X-Queue-Ms", strconv.FormatInt(trace.QueueMs, 10))
	}
}
