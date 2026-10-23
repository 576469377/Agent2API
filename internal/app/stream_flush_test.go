package app

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/576469377/Agent2API/internal/llm"
)

// Middleware may expose Unwrap without implementing the optional Flusher
// interface. ResponseController must still reach the underlying writer.
type wrappedStreamWriter struct{ http.ResponseWriter }

func (w wrappedStreamWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

type errorOnlyFlusher struct{ http.ResponseWriter }

func (w errorOnlyFlusher) FlushError() error { return io.ErrClosedPipe }

func TestStreamFlushesThroughMiddlewareWrappers(t *testing.T) {
	for _, fail := range []bool{false, true} {
		a := newTestApp(t, &fakeAdapter{events: []llm.ResponseEvent{
			{Type: llm.EventStart}, {Type: llm.EventTextDelta, Delta: "test"},
			{Type: llm.EventDone},
		}})
		recorder := httptest.NewRecorder()
		var underlying http.ResponseWriter = recorder
		if fail {
			underlying = errorOnlyFlusher{recorder}
		}
		writer := wrappedStreamWriter{underlying}
		a.Handler().ServeHTTP(writer, httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"m","stream":true,"messages":[{"role":"user","content":"test"}]}`)))
		if fail {
			stats := a.metrics.Snapshot()
			if stats.OK != 0 || stats.Failed != 1 || stats.Recent[0].Status != 499 {
				t.Fatalf("wrapped flush failure was counted as success: %+v", stats)
			}
		} else if !recorder.Flushed {
			t.Fatal("wrapped stream was never flushed")
		}
	}
}
