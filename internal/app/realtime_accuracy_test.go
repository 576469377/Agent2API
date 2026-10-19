package app

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/576469377/Agent2API/internal/llm"
	"github.com/576469377/Agent2API/internal/obs"
)

type failedClientWriter struct {
	header       http.Header
	flushFailure bool
}

func (w *failedClientWriter) Header() http.Header { return w.header }
func (w *failedClientWriter) WriteHeader(int)     {}
func (w *failedClientWriter) Write(p []byte) (int, error) {
	if w.flushFailure {
		return len(p), nil
	}
	return 0, io.ErrClosedPipe
}
func (w *failedClientWriter) Flush()            {}
func (w *failedClientWriter) FlushError() error { return io.ErrClosedPipe }

func TestBrokenClientWritesAndFlushesAreNeverSuccessfulMetrics(t *testing.T) {
	for _, stream := range []bool{false, true} {
		for _, flushFailure := range []bool{false, true} {
			if !stream && flushFailure {
				continue
			}
			t.Run(fmt.Sprintf("stream=%t/flush=%t", stream, flushFailure), func(t *testing.T) {
				a := newTestApp(t, &fakeAdapter{events: []llm.ResponseEvent{{Type: llm.EventStart}, {Type: llm.EventTextDelta, Delta: "test"}, {Type: llm.EventDone, Usage: &llm.Usage{InputTokens: 2, OutputTokens: 1}}}})
				w := &failedClientWriter{header: make(http.Header), flushFailure: flushFailure}
				body := fmt.Sprintf(`{"model":"m","stream":%t,"messages":[{"role":"user","content":"test"}]}`, stream)
				a.Handler().ServeHTTP(w, httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(body)))
				s := a.metrics.Snapshot()
				live := a.metrics.LiveSnapshot()
				if s.Total != 1 || s.OK != 0 || s.Failed != 1 || s.Recent[0].Status != 499 || live.InFlight != 0 || len(live.Active) != 0 {
					t.Fatalf("broken client counted as success/leaked: %+v", s)
				}
			})
		}
	}
}

func TestPrematureUpstreamEOFIsAFailureForBothResponseModes(t *testing.T) {
	for _, stream := range []bool{false, true} {
		a := newTestApp(t, &fakeAdapter{events: []llm.ResponseEvent{{Type: llm.EventTextDelta, Delta: "partial"}}})
		body := fmt.Sprintf(`{"model":"m","stream":%t,"messages":[{"role":"user","content":"test"}]}`, stream)
		w := httptest.NewRecorder()
		a.Handler().ServeHTTP(w, httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(body)))
		s := a.metrics.Snapshot()
		if s.OK != 0 || s.Failed != 1 || s.InFlight != 0 || s.Recent[0].UsageReported || !strings.Contains(s.Recent[0].Error, "upstream_incomplete") {
			t.Fatalf("premature EOF is not a real success: %+v", s)
		}
		if s.UsageHistory.Days[0].Models[0].UnknownTokenRequests != 1 {
			t.Fatal("unreported partial usage became measured zero")
		}
	}
}

func TestLiveOneShotToolArgumentsAreCountedExactlyOnce(t *testing.T) {
	for _, split := range []bool{false, true} {
		m := obs.New()
		now := time.Now()
		id := m.BeginLive("chat", now)
		events := []llm.ResponseEvent{{Type: llm.EventToolCallStart, ContentIndex: 1, ToolName: "read"}}
		if split {
			events = append(events, llm.ResponseEvent{Type: llm.EventToolCallDelta, ContentIndex: 1, Delta: `{"x":`}, llm.ResponseEvent{Type: llm.EventToolCallDelta, ContentIndex: 1, Delta: `"你好"}`})
		}
		events = append(events, llm.ResponseEvent{Type: llm.EventToolCallEnd, ContentIndex: 1, ToolArguments: `{"x":"你好"}`}, llm.ResponseEvent{Type: llm.EventDone, Usage: &llm.Usage{InputTokens: 0, OutputTokens: 0}})
		rec := &obs.Record{LiveID: id, Time: now}
		stream := &observedStream{inner: &eventStream{events: events}, metrics: m, record: rec}
		for {
			_, err := stream.Recv(context.Background())
			if err == llm.ErrStreamDone {
				break
			}
			if err != nil {
				t.Fatal(err)
			}
		}
		if rec.OutputCharacters != 10 || !rec.UsageReported || rec.FirstOutputMs == nil || m.LiveSnapshot().Active[0].OutputCharacters != 10 {
			t.Fatalf("tool arguments missing/doubled or zero usage lost: %+v", rec)
		}
	}
}

func TestMetricsEndpointsCannotBeStoredAsFreshLiveData(t *testing.T) {
	a := newTestApp(t, &fakeAdapter{})
	for _, path := range []string{"/api/live", "/api/metrics"} {
		w := httptest.NewRecorder()
		a.Handler().ServeHTTP(w, httptest.NewRequest("GET", path, nil))
		if w.Header().Get("Cache-Control") != "no-store" {
			t.Fatalf("%s can serve stale cached counters", path)
		}
	}
}

type failedRequestBody struct{ err error }

func (b failedRequestBody) Read([]byte) (int, error) { return 0, b.err }
func (b failedRequestBody) Close() error             { return nil }

func TestRequestBodyFailureStatusMatchesRecordedOutcome(t *testing.T) {
	for _, tc := range []struct {
		err    error
		status int
	}{{&http.MaxBytesError{Limit: 1}, 413}, {io.ErrUnexpectedEOF, 400}, {context.Canceled, 499}} {
		a := newTestApp(t, &fakeAdapter{})
		r := httptest.NewRequest("POST", "/v1/chat/completions", nil)
		r.Body = failedRequestBody{err: tc.err}
		w := httptest.NewRecorder()
		a.Handler().ServeHTTP(w, r)
		s := a.metrics.Snapshot()
		// 499 is the recorded cancellation outcome. The common error writer
		// keeps its existing HTTP-200 error-body compatibility for cancellation.
		wireMatches := w.Code == tc.status
		if tc.status == 499 {
			wireMatches = w.Code == 200 && strings.Contains(w.Body.String(), "request_body_read_failed")
		}
		if !wireMatches || s.Recent[0].Status != tc.status || s.OK != 0 || s.InFlight != 0 {
			t.Fatalf("wire=%d recorded=%+v", w.Code, s.Recent[0])
		}
	}
}
