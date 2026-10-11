package app

import (
	"context"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/576469377/Agent2API/internal/adapter"
	"github.com/576469377/Agent2API/internal/config"
	"github.com/576469377/Agent2API/internal/llm"
)

type catalogAdapter struct {
	recordingAdapter
	models []adapter.ModelInfo
	err    error
}

func (a *catalogAdapter) Stream(ctx context.Context, req llm.RequestMessages) (llm.ResponseStream, error) {
	if a.err != nil {
		a.gotModel = req.Model
		return nil, a.err
	}
	return a.recordingAdapter.Stream(ctx, req)
}

func (a *catalogAdapter) ListModels(context.Context) ([]adapter.ModelInfo, error) {
	return a.models, nil
}

func TestRegionalRoutingAndCatalogRefresh(t *testing.T) {
	cn := &catalogAdapter{models: []adapter.ModelInfo{{ID: "shared", ContextTokens: 100}}}
	global := &catalogAdapter{models: []adapter.ModelInfo{{ID: "shared", ContextTokens: 200}}}
	h, err := NewHub([]*PlatformRuntime{{ID: "workbuddy", Adapter: cn}, {ID: "workbuddy-global", Adapter: global}}, log.New(io.Discard, "", 0))
	if err != nil {
		t.Fatal(err)
	}
	if h.Route("shared").ID != "workbuddy" {
		t.Fatal("legacy IDs must retain configured priority")
	}
	for _, pid := range []string{"workbuddy", "workbuddy-global"} {
		model, hint := h.ResolveAlias(pid + "/shared")
		if model != "shared" || h.RouteWithHint(model, hint).ID != pid {
			t.Fatal("qualified ID must select its region")
		}
	}
	cfg := config.Default()
	cfg.MetricsFile = ""
	app := New(cfg, h, log.New(io.Discard, "", 0))
	w := httptest.NewRecorder()
	app.models(w, httptest.NewRequest("GET", "/v1/models", nil))
	var catalogue struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &catalogue); err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for _, m := range catalogue.Data {
		if seen[m.ID] {
			t.Fatal("duplicate catalogue ID", m.ID)
		}
		seen[m.ID] = true
	}
	for _, id := range []string{"shared", "workbuddy/shared", "workbuddy-global/shared"} {
		if !seen[id] {
			t.Fatal("missing route", id)
		}
	}
	cn.models = nil
	global.models = append(global.models, adapter.ModelInfo{ID: "new"})
	_, _ = h.ListModels(context.Background())
	if h.OwnerOf("shared") != "workbuddy-global" || h.OwnerOf("new") != "workbuddy-global" {
		t.Fatal("refresh must rebuild ownership")
	}
}

func TestQualifiedModelsAcrossProtocols(t *testing.T) {
	cases := []struct {
		path, body string
		proto      Protocol
	}{
		{"/v1/chat/completions", `{"model":"workbuddy-global/shared","messages":[{"role":"user","content":"hi"}],"stream":STREAM}`, chatProtocol{}},
		{"/v1/messages", `{"model":"workbuddy-global/shared","max_tokens":32,"messages":[{"role":"user","content":"hi"}],"stream":STREAM}`, anthropicProtocol{}},
		{"/v1/responses", `{"model":"workbuddy-global/shared","input":"hi","stream":STREAM}`, responsesProtocol{}},
	}
	for _, c := range cases {
		for _, stream := range []string{"true", "false"} {
			t.Run(c.path+stream, func(t *testing.T) {
				h, cn, global := newAliasHub(t, nil)
				// Use the test hub's platform IDs, then check the exact upstream model.
				cfg := config.Default()
				cfg.MetricsFile = ""
				a := New(cfg, h, log.New(io.Discard, "", 0))
				body := strings.ReplaceAll(strings.ReplaceAll(c.body, "workbuddy-global/", "other/"), "STREAM", stream)
				w := httptest.NewRecorder()
				a.serve(w, httptest.NewRequest(http.MethodPost, c.path, strings.NewReader(body)), c.proto)
				if w.Code != 200 || global.gotModel != "shared" || cn.gotModel != "" {
					t.Fatalf("wrong route: %d model=%q cn=%q: %s", w.Code, global.gotModel, cn.gotModel, w.Body.String())
				}
			})
		}
	}
}

func TestSameModelFallbackDoesNotOverridePinnedRoutes(t *testing.T) {
	for _, tc := range []struct {
		name, model                 string
		enabled, shared, wantGlobal bool
		failure                     *llm.Failure
	}{
		{"quota", "shared", true, true, true, &llm.Failure{Code: "quota", QuotaExhausted: true}},
		{"rate", "shared", true, true, true, &llm.Failure{Code: "rate_limited", RateLimited: true}},
		{"server", "shared", true, true, true, &llm.Failure{Code: "http_503", UpstreamFault: true}},
		{"pinned", "workbuddy/shared", true, true, false, &llm.Failure{Code: "rate_limited", RateLimited: true}},
		{"disabled", "shared", false, true, false, &llm.Failure{Code: "rate_limited", RateLimited: true}},
		{"different model", "shared", true, false, false, &llm.Failure{Code: "rate_limited", RateLimited: true}},
		{"auth", "shared", true, true, false, &llm.Failure{Code: "unauthorized", Unauthorized: true}},
		{"bad request", "shared", true, true, false, &llm.Failure{Code: "http_400", ClientFixable: true}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cn := &catalogAdapter{models: []adapter.ModelInfo{{ID: "shared"}}, err: tc.failure}
			global := &catalogAdapter{models: []adapter.ModelInfo{{ID: "other"}}}
			if tc.shared {
				global.models = []adapter.ModelInfo{{ID: "shared"}}
			}
			h, err := NewHub([]*PlatformRuntime{{ID: "workbuddy", Adapter: cn}, {ID: "workbuddy-global", Adapter: global}}, log.New(io.Discard, "", 0))
			if err != nil {
				t.Fatal(err)
			}
			cfg := config.Default()
			cfg.MetricsFile = ""
			cfg.Models.FallbackOnUnavailable = tc.enabled
			a := New(cfg, h, log.New(io.Discard, "", 0))
			model, hint := h.ResolveAlias(tc.model)
			stream, rt, err := a.openRoutedStream(context.Background(), llm.RequestMessages{Model: model}, hint)
			if tc.wantGlobal {
				if err != nil || stream == nil || rt.ID != "workbuddy-global" || global.gotModel != "shared" {
					t.Fatalf("fallback failed: %v, %s", err, rt.ID)
				}
			} else if err == nil || global.gotModel != "" {
				t.Fatal("must not cross platforms")
			}
		})
	}
}
