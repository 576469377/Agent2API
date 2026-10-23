package workbuddy

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/http/httptrace"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/576469377/Agent2API/internal/adapter"
	"github.com/576469377/Agent2API/internal/llm"
)

type uploadRoundTripFunc func(*http.Request) (*http.Response, error)

func (f uploadRoundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func uploadTestContext(t *testing.T) (context.Context, *llm.Execution) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	t.Cleanup(cancel)
	e := llm.NewExecution(6)
	return llm.WithExecution(ctx, e), e
}

// Reproduce both scopes of the previous bug: an account is already uploading a
// large body, and more concurrent chats plus control requests arrive repeatedly.
// The upstream offers HTTP/2, but only control traffic should negotiate it.
func TestStalledUploadDoesNotBlockSameAccountChatsOrControl(t *testing.T) {
	started, release := make(chan struct{}), make(chan struct{})
	var badProtocol, fastCalls atomic.Int64
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/control" {
			if r.ProtoMajor != 2 {
				badProtocol.Add(1)
			}
		} else if r.ProtoMajor != 1 {
			badProtocol.Add(1)
		}
		if r.URL.Path == "/stalled" {
			close(started)
			<-release
			w.Header().Set("Connection", "close")
			return
		}
		_, _ = io.Copy(io.Discard, r.Body)
		fastCalls.Add(1)
		_, _ = io.WriteString(w, "ok")
	}))
	server.EnableHTTP2 = true
	server.StartTLS()
	defer server.Close()
	defer close(release)
	c := newHTTPClient(server.URL, 0)
	c.uploadIdleTimeout = 2 * time.Second
	roots := x509.NewCertPool()
	roots.AddCert(server.Certificate())
	for _, client := range []*http.Client{c.http, c.streamHTTP} {
		transport := client.Transport.(*http.Transport)
		if transport.TLSClientConfig == nil {
			transport.TLSClientConfig = &tls.Config{}
		}
		transport.TLSClientConfig.RootCAs = roots
		t.Cleanup(transport.CloseIdleConnections)
	}
	ctx, execution := uploadTestContext(t)
	a := &Adapter{cli: c, cfg: Config{Platform: GlobalPlatform}}
	stalled := make(chan error, 1)
	go func() {
		resp, err := a.openChatAttempt(ctx, "/stalled", upstreamRequest{Model: "fixture", Messages: []upstreamMessage{{Role: "user", Content: strings.Repeat("x", 16<<20)}}}, http.Header{})
		if resp != nil {
			resp.Body.Close()
		}
		stalled <- err
	}()
	select {
	case <-started:
	case <-ctx.Done():
		t.Fatal("stalled fixture did not start")
	}
	for wave := 0; wave < 3; wave++ {
		var wg sync.WaitGroup
		for i := 0; i < 8; i++ {
			wg.Add(1)
			go func(control bool) {
				defer wg.Done()
				var resp *http.Response
				var err error
				if control {
					resp, err = c.http.Get(server.URL + "/control")
				} else {
					resp, err = c.openStream(ctx, "/fast", map[string]string{"prompt": "fixture"}, http.Header{})
				}
				if err != nil {
					t.Errorf("unrelated request blocked by upload: %v", err)
					return
				}
				defer resp.Body.Close()
				if _, err := io.Copy(io.Discard, resp.Body); err != nil {
					t.Error(err)
				}
			}(i%4 == 0)
		}
		wg.Wait()
	}
	if fastCalls.Load() != 24 || badProtocol.Load() != 0 {
		t.Fatalf("connection isolation failed: fast=%d protocol errors=%d", fastCalls.Load(), badProtocol.Load())
	}
	select {
	case err := <-stalled:
		f := llm.Wrap(err)
		if f == nil || f.Code != "upstream_upload_timeout" || f.HTTPStatus() != 504 || f.Canceled || !f.NoRetry || shouldRetryDial(err) {
			t.Fatalf("stalled upload was not released without replay: %v", err)
		}
	case <-ctx.Done():
		t.Fatal("stalled socket write was never interrupted")
	}
	snapshot := execution.Snapshot()
	p := snapshot.Attempts[0]
	if snapshot.AttemptCount != 1 || p.Status != 504 || p.RequestBytes <= 16<<20 || p.UploadedBytes <= 0 || p.UploadedBytes >= p.RequestBytes || p.UploadCompletedAtMs != 0 || p.UpstreamProtocol != "HTTP/1.1" {
		t.Fatalf("missing stalled-upload evidence: %+v", snapshot)
	}
	if c.streamHTTP.Timeout != 0 || c.streamHTTP.Transport.(*http.Transport).MaxConnsPerHost != 0 {
		t.Fatal("isolation must preserve long streams and configured unlimited concurrency")
	}
}

func TestUploadGuardAllowsContinuousProgressAndLongHeaderAndBodyWait(t *testing.T) {
	c := newHTTPClient("https://fixture.invalid", 0)
	c.uploadIdleTimeout = 100 * time.Millisecond
	body := map[string]string{"history": strings.Repeat("x", 16<<10)}
	want, _ := json.Marshal(body)
	var requestContext context.Context
	c.streamHTTP.Transport = uploadRoundTripFunc(func(r *http.Request) (*http.Response, error) {
		requestContext = r.Context()
		trace := httptrace.ContextClientTrace(r.Context())
		trace.GotConn(httptrace.GotConnInfo{})
		trace.WroteHeaders()
		if r.GetBody != nil || r.ContentLength != int64(len(want)) {
			t.Error("request may be silently replayed or length changed")
		}
		var got bytes.Buffer
		buffer := make([]byte, 1024)
		for {
			n, err := r.Body.Read(buffer)
			got.Write(buffer[:n])
			if err == io.EOF {
				break
			}
			if err != nil {
				return nil, err
			}
			time.Sleep(12 * time.Millisecond)
			if r.Context().Err() != nil {
				return nil, context.Cause(r.Context())
			}
		}
		if !bytes.Equal(got.Bytes(), want) {
			t.Error("upload body changed")
		}
		trace.WroteRequest(httptrace.WroteRequestInfo{})
		time.Sleep(150 * time.Millisecond)
		if r.Context().Err() != nil {
			return nil, context.Cause(r.Context())
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader("data: [DONE]\n\n")), Header: http.Header{}}, nil
	})
	ctx, e := uploadTestContext(t)
	a := &Adapter{cli: c, cfg: Config{Platform: GlobalPlatform}}
	// Use openStream directly with a tracked attempt to assert exact body bytes.
	child, finish, err := llm.BeginAttempt(ctx, a.cfg.Platform, 0)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := c.openStream(adapter.TraceRequest(child), "/chat", body, http.Header{})
	if err := finish(200, err); err != nil {
		t.Fatalf("active upload or header wait was timed out: %v", err)
	}
	time.Sleep(150 * time.Millisecond)
	if requestContext.Err() != nil {
		t.Fatal("upload guard canceled a long response stream")
	}
	if _, err := io.Copy(io.Discard, resp.Body); err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if requestContext.Err() == nil {
		t.Fatal("closing the stream did not release its context")
	}
	p := e.Snapshot().Attempts[0]
	if p.RequestBytes != int64(len(want)) || p.UploadedBytes != p.RequestBytes || p.UploadCompletedAtMs == 0 || p.UploadLastProgressAtMs == 0 {
		t.Fatalf("completed-upload progress incorrect: %+v", p)
	}
}

func TestUploadGuardCoversBlockedFinalWriteAndPreservesCallerCancellation(t *testing.T) {
	for _, callerCancels := range []bool{false, true} {
		t.Run(map[bool]string{false: "final-write-stall", true: "caller-canceled"}[callerCancels], func(t *testing.T) {
			c := newHTTPClient("https://fixture.invalid", 0)
			c.uploadIdleTimeout = 80 * time.Millisecond
			ctx, e := uploadTestContext(t)
			ctx, cancel := context.WithCancel(ctx)
			defer cancel()
			c.streamHTTP.Transport = uploadRoundTripFunc(func(r *http.Request) (*http.Response, error) {
				trace := httptrace.ContextClientTrace(r.Context())
				trace.GotConn(httptrace.GotConnInfo{})
				trace.WroteHeaders()
				_, _ = io.Copy(io.Discard, r.Body)
				if callerCancels {
					cancel()
				}
				// The reader is at EOF, but the final socket write has not finished.
				<-r.Context().Done()
				trace.WroteRequest(httptrace.WroteRequestInfo{Err: r.Context().Err()})
				return nil, r.Context().Err()
			})
			a := &Adapter{cli: c, cfg: Config{Platform: GlobalPlatform}}
			_, err := a.openChatAttempt(ctx, "/chat", upstreamRequest{Model: "fixture"}, http.Header{})
			f := llm.Wrap(err)
			wantStatus := 504
			if callerCancels {
				wantStatus = 499
			}
			if f == nil || f.HTTPStatus() != wantStatus || f.NoRetry == callerCancels {
				t.Fatalf("wrong cancellation cause: %v", err)
			}
			p := e.Snapshot().Attempts[0]
			if p.UploadedBytes != p.RequestBytes || p.UploadCompletedAtMs != 0 || p.Status != wantStatus {
				t.Fatalf("EOF treated as completed upload: %+v", p)
			}
		})
	}
}

func TestUploadGuardPreservesTimeoutCauseAfterEarlyResponseHeaders(t *testing.T) {
	c := newHTTPClient("https://fixture.invalid", 0)
	c.uploadIdleTimeout = 60 * time.Millisecond
	c.streamHTTP.Transport = uploadRoundTripFunc(func(r *http.Request) (*http.Response, error) {
		trace := httptrace.ContextClientTrace(r.Context())
		trace.GotConn(httptrace.GotConnInfo{})
		trace.WroteHeaders()
		buffer := make([]byte, 1)
		_, _ = r.Body.Read(buffer)
		// An upstream can send headers before it finishes receiving the body.
		// Keep the guard until the socket writer, not response headers, finishes.
		return &http.Response{StatusCode: 200, Header: http.Header{}, Body: uploadBlockedResponse{ctx: r.Context()}}, nil
	})
	ctx, e := uploadTestContext(t)
	a := &Adapter{cli: c, cfg: Config{Platform: GlobalPlatform}}
	resp, err := a.openChatAttempt(ctx, "/chat", upstreamRequest{Model: "fixture"}, http.Header{})
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	_, err = newUpstreamStream(resp).Recv(ctx)
	f := llm.Wrap(err)
	if f == nil || f.Code != "upstream_upload_timeout" || !f.NoRetry || f.Canceled || f.HTTPStatus() != 504 || shouldRetryDial(err) {
		t.Fatalf("early headers masked stalled upload or allowed replay: %v", err)
	}
	if e.Snapshot().Attempts[0].UploadCompletedAtMs != 0 {
		t.Fatal("response headers incorrectly marked upload complete")
	}
}

func TestClosedUploadResponseNeverReturnsAnEmptySuccessfulEvent(t *testing.T) {
	for _, callerCanceled := range []bool{false, true} {
		ctx, cancel := context.WithCancel(context.Background())
		if callerCanceled {
			cancel()
		}
		transportCtx, closeTransport := context.WithCancel(context.Background())
		closeTransport()
		stream := newUpstreamStream(&http.Response{Body: uploadBlockedResponse{ctx: transportCtx}})
		_, err := stream.Recv(ctx)
		f := llm.Wrap(err)
		cancel()
		if f == nil {
			t.Fatal("closed connection returned a successful empty event, leaving the caller waiting")
		}
		if callerCanceled {
			if f.HTTPStatus() != 499 {
				t.Fatalf("caller cancellation lost: %v", err)
			}
		} else if f.HTTPStatus() != 502 || f.Canceled || !f.NoRetry || shouldRetryDial(err) {
			t.Fatalf("transport cancellation was masked or retried: %v", err)
		}
	}
}

type uploadBlockedResponse struct{ ctx context.Context }

func (b uploadBlockedResponse) Read([]byte) (int, error) { <-b.ctx.Done(); return 0, b.ctx.Err() }
func (b uploadBlockedResponse) Close() error             { return nil }
