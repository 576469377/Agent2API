package app

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/576469377/Agent2API/internal/adapter"
	"github.com/576469377/Agent2API/internal/config"
	"github.com/576469377/Agent2API/internal/llm"
)

type imageGuardAdapter struct {
	fakeAdapter
	models []adapter.ModelInfo
	calls  int
	got    llm.RequestMessages
}

func (a *imageGuardAdapter) ListModels(context.Context) ([]adapter.ModelInfo, error) {
	return a.models, nil
}

func (a *imageGuardAdapter) Stream(ctx context.Context, req llm.RequestMessages) (llm.ResponseStream, error) {
	a.calls++
	a.got = req
	return a.fakeAdapter.Stream(ctx, req)
}

func imageGuardApp(t *testing.T, cfg config.Config) (*App, *imageGuardAdapter, *imageGuardAdapter, *bytes.Buffer) {
	t.Helper()
	models := []adapter.ModelInfo{{ID: "hy3"}, {ID: "step-5-preview"}, {ID: "deepseek-v4.1-flash"}}
	cn, global := &imageGuardAdapter{models: models}, &imageGuardAdapter{models: models}
	cn.events = []llm.ResponseEvent{{Type: llm.EventDone}}
	global.events = []llm.ResponseEvent{{Type: llm.EventDone}}
	var logs bytes.Buffer
	logger := log.New(&logs, "", 0)
	hub, err := NewHub([]*PlatformRuntime{{ID: "workbuddy", Adapter: cn}, {ID: "workbuddy-global", Adapter: global}}, logger)
	if err != nil {
		t.Fatal(err)
	}
	hub.SetAliases(cfg.Models.Aliases)
	return New(cfg, hub, logger), cn, global, &logs
}

func anthropicImageHistory(model string, count int, stream bool) []byte {
	messages := make([]any, 0, count*2)
	for index := 0; index < count; index++ {
		id := fmt.Sprintf("call_%d", index)
		messages = append(messages,
			map[string]any{"role": "assistant", "content": []any{map[string]any{"type": "tool_use", "id": id, "name": "read_image", "input": map[string]any{}}}},
			map[string]any{"role": "user", "content": []any{map[string]any{"type": "tool_result", "tool_use_id": id, "content": []any{
				map[string]any{"type": "image", "source": map[string]any{"type": "base64", "media_type": "image/png", "data": "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mP8/x8AAwMCAO+a9mgAAAAASUVORK5CYII="}},
			}}}})
	}
	body, _ := json.Marshal(map[string]any{"model": model, "messages": messages, "stream": stream, "max_tokens": 32})
	return body
}

func TestCountWireImagesIncludesToolResultsAndIgnoresOpaqueText(t *testing.T) {
	for _, tc := range []struct {
		protocol, body string
		want           int
	}{
		{"anthropic-messages", `{"messages":[{"role":"user","content":[{"type":"image","source":{}},{"type":"tool_result","content":[{"type":"image","source":{"data":"invalid"}},{"type":"text","text":"image"}]},{"type":"text","text":"{\"type\":\"image\"}"},{"type":"tool_use","input":{"type":"image"}}]}],"tools":[{"input_schema":{"type":"image"}}]}`, 2},
		{"anthropic-messages", `{"messages":[{"content":[{"type":"tool_result","content":"[{\"type\":\"image\"}]"}]}]}`, 0},
		{"openai-chat", `{"messages":[{"role":"user","content":[{"type":"image_url","image_url":{}},{"type":"text","text":"image"}]},{"role":"tool","content":[{"type":"image_url","image_url":{"url":"data:invalid"}}]}]}`, 2},
		{"openai-responses", `{"input":[{"type":"message","role":"user","content":[{"type":"input_image","image_url":"invalid"}]},{"type":"function_call_output","output":[{"type":"input_image","image_url":"invalid"}]}]}`, 2},
		{"openai-responses", `{"input":"[{\"type\":\"input_image\"}]"}`, 0},
	} {
		t.Run(tc.protocol+tc.body[:15], func(t *testing.T) {
			if got := countWireImages([]byte(tc.body), tc.protocol); got != tc.want {
				t.Fatalf("images=%d want=%d", got, tc.want)
			}
		})
	}
}

func TestImageGuardBoundariesAndRegionalIsolation(t *testing.T) {
	for _, tc := range []struct {
		model string
		count int
		want  int
	}{
		{"workbuddy-global/step-5-preview", 60, 200}, {"workbuddy-global/step-5-preview", 61, 400},
		{"workbuddy-global/hy3", 200, 200}, {"workbuddy-global/hy3", 201, 400},
		{"workbuddy-global/deepseek-v4.1-flash", 248, 200},
		{"workbuddy/hy3", 201, 200}, {"workbuddy/step-5-preview", 201, 200},
		{"hy3", 201, 200}, {"step-alias", 61, 400},
	} {
		for _, stream := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/%d/stream=%t", tc.model, tc.count, stream), func(t *testing.T) {
				cfg := config.Default()
				cfg.Models.Aliases = map[string]string{"step-alias": "workbuddy-global/step-5-preview"}
				a, cn, global, logs := imageGuardApp(t, cfg)
				body := anthropicImageHistory(tc.model, tc.count, stream)
				w := httptest.NewRecorder()
				a.Handler().ServeHTTP(w, httptest.NewRequest("POST", "/v1/messages", bytes.NewReader(body)))
				if w.Code != tc.want {
					t.Fatalf("status=%d want=%d body=%s", w.Code, tc.want, w.Body.String())
				}
				if tc.want == 400 {
					if cn.calls+global.calls != 0 || !strings.Contains(w.Body.String(), "image_count_exceeded") || !strings.Contains(w.Body.String(), "开新对话") {
						t.Fatal("oversized request must fail before any upstream invocation")
					}
				} else {
					decoded, err := (anthropicProtocol{}).DecodeRequest(body)
					if err != nil {
						t.Fatal(err)
					}
					used := cn
					if global.calls > 0 {
						used = global
					}
					if cn.calls+global.calls != 1 || !reflect.DeepEqual(decoded.Messages, used.got.Messages) {
						t.Fatal("guard changed history, images or tool content")
					}
				}
				rows := a.metrics.Snapshot().Recent
				if len(rows) != 1 || rows[0].ImageCount == nil || *rows[0].ImageCount != tc.count || rows[0].Status != tc.want {
					t.Fatal("incorrect request image observability")
				}
				if !strings.Contains(logs.String(), "images=") {
					t.Fatal("request log must include image count")
				}
			})
		}
	}
}

func TestImageGuardIsAppliedToFallbackTarget(t *testing.T) {
	cfg := config.Default()
	cfg.Models.FallbackOnUnavailable = true
	a, cn, global, _ := imageGuardApp(t, cfg)
	cn.err = &llm.Failure{Code: "rate_limited", RateLimited: true}
	w := httptest.NewRecorder()
	a.Handler().ServeHTTP(w, httptest.NewRequest("POST", "/v1/messages", bytes.NewReader(anthropicImageHistory("step-5-preview", 61, true))))
	if w.Code != 400 || cn.calls != 1 || global.calls != 0 || a.metrics.Snapshot().Recent[0].Platform != "workbuddy-global" {
		t.Fatal("fallback must enforce the target's own guard before sending")
	}
}

func TestAmbiguousImageErrorsDoNotInventACause(t *testing.T) {
	a, _, _, _ := imageGuardApp(t, config.Default())
	for _, phrase := range []string{"Invalid request parameters", "the request parameters were rejected by the model provider", "Please start a new conversation, replace the image, and try again."} {
		original := &llm.Failure{Code: "http_400", Message: phrase, ClientFixable: true}
		for _, tc := range []struct {
			platform string
			count    int
			excess   bool
		}{{"workbuddy-global", 61, true}, {"workbuddy-global", 60, false}, {"workbuddy", 201, false}} {
			f := a.explainImageFailure(original, tc.platform, "step-5-preview", tc.count)
			if f.HTTPStatus() != 400 {
				t.Fatal("mapped error lost HTTP 400")
			}
			if tc.excess {
				if f.Code != "image_count_exceeded" || !strings.Contains(f.Message, "61") || !strings.Contains(f.Message, "60") {
					t.Fatal("excess must show count and per-platform limit")
				}
			} else if !strings.Contains(f.Message, phrase) || !strings.Contains(f.Message, "原因未知") || f.Code != original.Code {
				t.Fatal("ambiguous non-excess error must preserve the original without a diagnosis")
			}
		}
		if original.Message != phrase {
			t.Fatal("shared upstream error was mutated")
		}
	}
}

func TestAmbiguousErrorExplanationReachesClientAndMetrics(t *testing.T) {
	for _, stream := range []bool{true, false} {
		a, _, global, _ := imageGuardApp(t, config.Default())
		global.err = &llm.Failure{Code: "http_400", Message: "Invalid request parameters", ClientFixable: true}
		w := httptest.NewRecorder()
		a.Handler().ServeHTTP(w, httptest.NewRequest("POST", "/v1/messages", bytes.NewReader(anthropicImageHistory("workbuddy-global/step-5-preview", 1, stream))))
		if w.Code != 400 || !strings.Contains(w.Body.String(), "Invalid request parameters") || !strings.Contains(w.Body.String(), "原因未知") || !strings.Contains(a.metrics.Snapshot().Recent[0].Error, "原因未知") {
			t.Fatal("client and metrics must get the same actionable explanation")
		}
	}
}

func TestImageLimitCatalogAndConfigurationOverrides(t *testing.T) {
	cfg := config.Default()
	cfg.Models.ImageLimits = map[string]int{"workbuddy-global/hy3": 1, "workbuddy-global/step-5-preview": 0, "workbuddy/hy3": 3}
	cfg.Models.Aliases = map[string]string{"photo-alias": "workbuddy-global/hy3"}
	a, _, _, _ := imageGuardApp(t, cfg)
	for _, tc := range []struct {
		model string
		count int
		want  int
	}{{"workbuddy-global/hy3", 2, 400}, {"workbuddy-global/step-5-preview", 61, 200}, {"workbuddy/hy3", 4, 400}} {
		w := httptest.NewRecorder()
		a.Handler().ServeHTTP(w, httptest.NewRequest("POST", "/v1/messages", bytes.NewReader(anthropicImageHistory(tc.model, tc.count, true))))
		if w.Code != tc.want {
			t.Fatalf("override ignored for %s", tc.model)
		}
	}
	w := httptest.NewRecorder()
	a.Handler().ServeHTTP(w, httptest.NewRequest("GET", "/api/models", nil))
	var catalog struct {
		Models    []modelWithImageLimit `json:"models"`
		Platforms []struct {
			ID     string                `json:"id"`
			Models []modelWithImageLimit `json:"models"`
		} `json:"platforms"`
	}
	if json.Unmarshal(w.Body.Bytes(), &catalog) != nil {
		t.Fatal("invalid catalog")
	}
	for _, model := range catalog.Models {
		want := cfg.Models.ImageLimitFor(model.Platform, model.ID)
		if model.ID == "photo-alias" {
			want = cfg.Models.ImageLimitFor("workbuddy-global", "hy3")
		}
		if !reflect.DeepEqual(want, model.ImageLimit) {
			t.Fatal("catalog must expose effective per-platform guard, including disabled/unknown")
		}
	}
	if len(catalog.Models) != 7 || len(catalog.Platforms) != 2 {
		t.Fatal("catalog lost models, alias or platforms")
	}
	for _, platform := range catalog.Platforms {
		for _, model := range platform.Models {
			if !reflect.DeepEqual(cfg.Models.ImageLimitFor(platform.ID, model.ID), model.ImageLimit) {
				t.Fatal("grouped catalog guard differs from actual platform rule")
			}
		}
	}
}

func TestImageGuardAcrossStandardProtocols(t *testing.T) {
	for _, tc := range []struct {
		path, body string
	}{
		{"/v1/chat/completions", `{"model":"workbuddy-global/hy3","messages":[{"role":"user","content":[{"type":"image_url","image_url":{"url":"data:image/png;base64,broken"}},{"type":"image_url","image_url":{"url":"data:image/png;base64,broken"}}]}]}`},
		{"/v1/responses", `{"model":"workbuddy-global/hy3","input":[{"type":"message","role":"user","content":[{"type":"input_image","image_url":"data:image/png;base64,broken"},{"type":"input_image","image_url":"data:image/png;base64,broken"}]}]}`},
	} {
		cfg := config.Default()
		cfg.Models.ImageLimits = map[string]int{"workbuddy-global/hy3": 1}
		a, cn, global, _ := imageGuardApp(t, cfg)
		w := httptest.NewRecorder()
		a.Handler().ServeHTTP(w, httptest.NewRequest(http.MethodPost, tc.path, strings.NewReader(tc.body)))
		if w.Code != 400 || cn.calls+global.calls != 0 || *a.metrics.Snapshot().Recent[0].ImageCount != 2 {
			t.Fatal("all protocols must enforce images before contacting upstream")
		}
	}
}
