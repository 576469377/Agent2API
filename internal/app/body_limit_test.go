package app

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/576469377/Agent2API/internal/config"
)

func TestLargeMultimodalTransportBodyPassesFormer32MiBLimit(t *testing.T) {
	cfg := config.Default()
	a, _, upstream, _ := imageGuardApp(t, cfg)
	body := `{"model":"workbuddy-global/deepseek-v4.1-flash","messages":[{"role":"user","content":"fixture"}]}`
	// Whitespace exercises the transport guard without spending time decoding images.
	body += strings.Repeat(" ", 33<<20)
	w := httptest.NewRecorder()
	a.Handler().ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body)))
	if w.Code != http.StatusOK || upstream.calls != 1 {
		t.Fatalf("large valid body: status=%d upstream calls=%d", w.Code, upstream.calls)
	}
}

func TestConfiguredBodyLimitReturns413BeforeCallingUpstream(t *testing.T) {
	cfg := config.Default()
	cfg.Server.MaxBodyMiB = 1
	a, cn, upstream, _ := imageGuardApp(t, cfg)
	body := `{"model":"workbuddy-global/deepseek-v4.1-flash","messages":[]}` + strings.Repeat(" ", 1<<20)
	w := httptest.NewRecorder()
	a.Handler().ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body)))
	if w.Code != http.StatusRequestEntityTooLarge || cn.calls+upstream.calls != 0 || !strings.Contains(w.Body.String(), "body_too_large") {
		t.Fatalf("oversized body: status=%d upstream calls=%d body=%s", w.Code, cn.calls+upstream.calls, w.Body.String())
	}
}

type brokenBody struct{}

func (brokenBody) Read([]byte) (int, error) { return 0, errors.New("read failed") }
func (brokenBody) Close() error             { return nil }

func TestBodyReadFailureIsNotReportedAsOversize(t *testing.T) {
	a := newTestApp(t, &fakeAdapter{})
	r := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	r.Body = io.ReadCloser(brokenBody{})
	w := httptest.NewRecorder()
	a.Handler().ServeHTTP(w, r)
	if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "body_read_failed") {
		t.Fatalf("read failure: status=%d body=%s", w.Code, w.Body.String())
	}
}
