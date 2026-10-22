package adapter

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/http/httptrace"
	"strings"
	"testing"
	"time"

	"github.com/576469377/Agent2API/internal/llm"
)

func TestRequestTraceObservesUploadAndProtectsAcceptedAndNewerAttempts(t *testing.T) {
	parent, cancel := context.WithCancel(context.Background())
	defer cancel()
	execution := llm.NewExecution(6)
	ctx := llm.WithExecution(parent, execution)
	child, finish, err := llm.BeginAttempt(ctx, "fixture", 0)
	if err != nil {
		t.Fatal(err)
	}
	traced := TraceRequest(child)
	trace := httptrace.ContextClientTrace(traced)
	trace.WroteHeaders()
	if execution.Snapshot().Phase != "uploading" {
		t.Fatal("headers did not start upload phase")
	}
	trace.WroteRequest(httptrace.WroteRequestInfo{})
	if execution.Snapshot().Phase != "waiting_headers" {
		t.Fatal("completed upload did not start response wait")
	}
	if err := finish(200, nil); err != nil {
		t.Fatal(err)
	}
	trace.WroteHeaders()
	if execution.Snapshot().Phase != "waiting_output" {
		t.Fatal("late upload callback replaced accepted stream phase")
	}
	execution.SetPhase("streaming")
	trace.WroteRequest(httptrace.WroteRequestInfo{})
	if execution.Snapshot().Phase != "streaming" {
		t.Fatal("late bidirectional upload callback replaced active output")
	}
	_, nextFinish, err := llm.BeginAttempt(ctx, "next", 0)
	if err != nil {
		t.Fatal(err)
	}
	trace.WroteRequest(httptrace.WroteRequestInfo{})
	if execution.Snapshot().Phase != "connecting" {
		t.Fatal("old attempt callback replaced current attempt")
	}
	_ = nextFinish(200, nil)
	if TraceRequest(parent) != parent {
		t.Fatal("requests without execution tracking should remain unchanged")
	}
}

func TestRequestTraceWorksWithHTTPTransport(t *testing.T) {
	uploaded := make(chan struct{})
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		close(uploaded)
		select {
		case <-release:
			_, _ = io.WriteString(w, "ok")
		case <-r.Context().Done():
		}
	}))
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	execution := llm.NewExecution(1)
	ctx, finish, err := llm.BeginAttempt(llm.WithExecution(ctx, execution), "fixture", 0)
	if err != nil {
		t.Fatal(err)
	}
	request, err := http.NewRequestWithContext(TraceRequest(ctx), "POST", server.URL, strings.NewReader("fixture"))
	if err != nil {
		t.Fatal(err)
	}
	completed := make(chan error, 1)
	go func() {
		response, err := server.Client().Do(request)
		if response != nil {
			defer response.Body.Close()
		}
		completed <- finish(200, err)
	}()
	select {
	case <-uploaded:
	case <-ctx.Done():
		t.Fatal("upload did not reach test server")
	}
	for execution.Snapshot().Phase != "waiting_headers" && ctx.Err() == nil {
		time.Sleep(time.Millisecond)
	}
	if execution.Snapshot().Phase != "waiting_headers" {
		t.Fatal("request is still shown as connecting after upload")
	}
	close(release)
	if err := <-completed; err != nil {
		t.Fatal(err)
	}
}
