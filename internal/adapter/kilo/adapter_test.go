package kilo

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/576469377/Agent2API/internal/llm"
)

func TestPublicCatalogFixture(t *testing.T) {
	raw, err := os.ReadFile("../../../fixtures/kilo-model-catalog.json")
	if err != nil {
		t.Fatal(err)
	}
	models, err := decodeCatalog(raw, time.Date(2026, 10, 24, 0, 0, 0, 0, time.UTC))
	if err != nil || len(models) != 3 {
		t.Fatalf("public catalogue contract changed: %d %v", len(models), err)
	}
	for _, model := range models {
		if !model.SupportsTools || model.MaxOutputTokens <= 0 || model.ContextTokens <= 0 {
			t.Fatalf("capability metadata lost: %+v", model)
		}
	}
}

func freeRow() map[string]any {
	return map[string]any{"id": "vendor/concrete:free", "name": "Test free model", "isFree": true,
		"pricing":        map[string]any{"prompt": "0", "completion": "0", "request": "0"},
		"context_length": 1000000, "top_provider": map[string]any{"context_length": 1000000, "max_completion_tokens": 64000},
		"architecture":         map[string]any{"input_modalities": []string{"text", "image"}, "output_modalities": []string{"text"}},
		"supported_parameters": []string{"tools", "reasoning", "max_tokens"}}
}

func catalog(rows ...map[string]any) []byte {
	raw, _ := json.Marshal(map[string]any{"data": rows})
	return raw
}

func TestFreeCatalogRequiresExplicitEvidenceAndExpiry(t *testing.T) {
	now := time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)
	for _, test := range []struct {
		name   string
		change func(map[string]any)
	}{
		{"missing flag", func(m map[string]any) { delete(m, "isFree") }},
		{"false flag despite zero price", func(m map[string]any) { m["isFree"] = false }},
		{"missing price", func(m map[string]any) { delete(m["pricing"].(map[string]any), "prompt") }},
		{"paid", func(m map[string]any) { m["pricing"].(map[string]any)["completion"] = "0.001" }},
		{"paid cache", func(m map[string]any) { m["pricing"].(map[string]any)["input_cache_read"] = "0.1" }},
		{"dynamic", func(m map[string]any) { m["pricing"].(map[string]any)["prompt"] = "-1" }},
		{"null price", func(m map[string]any) { m["pricing"].(map[string]any)["prompt"] = nil }},
		{"invalid price", func(m map[string]any) { m["pricing"].(map[string]any)["prompt"] = "unknown" }},
		{"router", func(m map[string]any) { m["id"] = "openrouter/free" }},
		{"auto", func(m map[string]any) { m["id"] = "kilo-auto/free" }},
		{"expired", func(m map[string]any) { m["expiration_date"] = "2030-01-01" }},
		{"invalid expiry", func(m map[string]any) { m["expiration_date"] = "later" }},
		{"numeric expiry", func(m map[string]any) { m["expiration_date"] = 1 }},
		{"audio", func(m map[string]any) { m["architecture"].(map[string]any)["output_modalities"] = []string{"audio"} }},
		{"unknown output", func(m map[string]any) { delete(m, "architecture") }},
		{"not a coding tool model", func(m map[string]any) { delete(m, "supported_parameters") }},
		{"unknown capacity", func(m map[string]any) { delete(m, "context_length"); delete(m, "top_provider") }},
	} {
		t.Run(test.name, func(t *testing.T) {
			m := freeRow()
			test.change(m)
			models, err := decodeCatalog(catalog(m), now)
			if err != nil || len(models) != 0 {
				t.Fatalf("unsafe model published: %+v (%v)", models, err)
			}
		})
	}
	m := freeRow()
	m["expiration_date"] = "2030-01-02"
	m["mayTrainOnYourPrompts"] = true
	models, err := decodeCatalog(catalog(m), now)
	if err != nil || len(models) != 1 || models[0].Pricing.Source != "kilo_models" || models[0].Pricing.ExpiresAt != "2030-01-02T00:00:00Z" || !models[0].SupportsTools || !strings.Contains(models[0].Description, "训练") {
		t.Fatalf("valid metadata lost: %+v %v", models, err)
	}
	models, _ = decodeCatalog(catalog(freeRow(), freeRow()), now)
	if len(models) != 0 {
		t.Fatal("ambiguous duplicate published")
	}
}

func drain(t *testing.T, stream llm.ResponseStream) []llm.ResponseEvent {
	t.Helper()
	if closer, ok := stream.(io.Closer); ok {
		defer closer.Close()
	}
	var events []llm.ResponseEvent
	for {
		event, err := stream.Recv(context.Background())
		if errors.Is(err, llm.ErrStreamDone) {
			return events
		}
		if err != nil {
			t.Fatal(err)
		}
		events = append(events, event)
	}
}

func TestAnonymousToolsRoundTripAndPriceChange(t *testing.T) {
	t.Setenv("KILO_API_KEY", "must-not-be-forwarded")
	t.Setenv("OPENROUTER_API_KEY", "must-not-be-forwarded")
	var paid atomic.Bool
	var posts, catalogs atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		for _, header := range []string{"Authorization", "X-Api-Key", "X-KiloCode-OrganizationId"} {
			if r.Header.Get(header) != "" {
				t.Errorf("credential forwarded: %s", header)
			}
		}
		if r.URL.Path == "/models" {
			catalogs.Add(1)
			m := freeRow()
			if paid.Load() {
				m["pricing"].(map[string]any)["prompt"] = "1"
			}
			w.Write(catalog(m))
			return
		}
		if r.URL.Path != "/chat/completions" {
			t.Errorf("unexpected endpoint: %s", r.URL.Path)
			w.WriteHeader(404)
			return
		}
		n := posts.Add(1)
		var body map[string]any
		if json.NewDecoder(r.Body).Decode(&body) != nil {
			t.Error("invalid request")
		}
		if body["model"] != "vendor/concrete:free" || body["provider"] != nil {
			t.Error("unexpected routing policy")
		}
		w.Header().Set("Content-Type", "text/event-stream")
		if n == 1 {
			tools := body["tools"].([]any)
			if tools[0].(map[string]any)["function"].(map[string]any)["name"] != "lookup" {
				t.Error("tool schema lost")
			}
			io.WriteString(w, "data: {\"choices\":[{\"index\":0,\"delta\":{\"tool_calls\":[{\"index\":0,\"id\":\"call_fixture\",\"function\":{\"name\":\"lookup\",\"arguments\":\"{\\\"key\\\":\\\"opaque\\\"}\"}}]},\"finish_reason\":\"tool_calls\"}]}\n\ndata: [DONE]\n\n")
		} else {
			messages := body["messages"].([]any)
			last := messages[len(messages)-1].(map[string]any)
			if last["role"] != "tool" || last["tool_call_id"] != "call_fixture" || last["content"] != "opaque-result" {
				t.Errorf("tool continuation lost: %v", last)
			}
			io.WriteString(w, "data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"opaque-result\"},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":15,\"completion_tokens\":3,\"total_tokens\":18}}\n\ndata: [DONE]\n\n")
		}
	}))
	defer server.Close()
	a, _ := New(Config{BaseURL: server.URL})
	req := llm.RequestMessages{Model: "vendor/concrete:free", Messages: []llm.Message{{Role: llm.RoleUser, Content: []llm.Content{{Type: llm.ContentText, Text: "lookup"}}}}, Tools: []llm.ToolDefinition{{Name: "lookup", Parameters: map[string]any{"type": "object"}}}}
	stream, err := a.Stream(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	var call *llm.ToolCall
	for _, event := range drain(t, stream) {
		if event.Type == llm.EventToolCallEnd {
			call = &llm.ToolCall{ID: event.ToolCallID, Name: event.ToolName, Arguments: event.ToolArguments}
		}
	}
	if call == nil || call.ID != "call_fixture" || call.Name != "lookup" || call.Arguments != `{"key":"opaque"}` {
		t.Fatalf("tool delta lost: %+v", call)
	}
	req.Messages = append(req.Messages, llm.Message{Role: llm.RoleAssistant, Content: []llm.Content{{Type: llm.ContentToolCall, ToolCall: call}}}, llm.Message{Role: llm.RoleTool, Content: []llm.Content{{Type: llm.ContentToolResult, ToolResult: &llm.ToolResult{ToolCallID: call.ID, Content: "opaque-result"}}}})
	stream, err = a.Stream(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	events := drain(t, stream)
	if end := events[len(events)-1]; end.Type != llm.EventDone || end.Usage == nil || end.Usage.InputTokens != 15 {
		t.Fatal("usage lost")
	}
	models, _ := a.ListModels(context.Background())
	models[0].Pricing.Status = "modified"
	models, _ = a.ListModels(context.Background())
	if models[0].Pricing.Status != "free" {
		t.Fatal("caller mutated cached authorization")
	}
	paid.Store(true)
	_, err = a.Stream(context.Background(), req)
	if f := llm.Wrap(err); f == nil || f.Code != "free_model_required" || posts.Load() != 2 || catalogs.Load() != 3 {
		t.Fatalf("price change generated or stale cache accepted: %v, posts=%d catalogs=%d", err, posts.Load(), catalogs.Load())
	}
}

func TestCancellationErrorsAndRedirects(t *testing.T) {
	for _, status := range []int{401, 402, 429, 503} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Retry-After", "7")
				w.WriteHeader(status)
				io.WriteString(w, `{"error":{"message":"Unavailable"}}`)
			}))
			defer server.Close()
			a, _ := New(Config{BaseURL: server.URL})
			_, err := a.ListModels(context.Background())
			f := llm.Wrap(err)
			if f == nil || f.RetryAfterSeconds != 7 || !strings.Contains(f.Message, "Kilo") || (status == 429 && !f.RateLimited) || (status == 503 && !f.UpstreamFault) {
				t.Fatalf("classification lost: %+v", f)
			}
		})
	}
	var redirected atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { redirected.Add(1) }))
	defer target.Close()
	redirect := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, target.URL, 307) }))
	defer redirect.Close()
	a, _ := New(Config{BaseURL: redirect.URL})
	if _, err := a.ListModels(context.Background()); err == nil || redirected.Load() != 0 {
		t.Fatal("redirect followed")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := a.ListModels(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel ignored: %v", err)
	}
}

func TestProviderRateLimitIsNotGatewayIPQuota(t *testing.T) {
	for _, test := range []struct {
		body     string
		sharedIP bool
	}{
		{`{"error":{"message":"Provider returned error","metadata":{"provider_name":"Poolside","limit_source":"upstream_provider_shared_pool"}}}`, false},
		{`{"error":{"message":"Rate limit exceeded for free models. Please try again later."}}`, true},
		{`{"error":{"message":"Sign up for free to continue and explore 500 other models. Or come back later."}}`, false},
	} {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(429); io.WriteString(w, test.body) }))
		a, _ := New(Config{BaseURL: server.URL})
		_, err := a.ListModels(context.Background())
		server.Close()
		f := llm.Wrap(err)
		if f == nil || !f.RateLimited || strings.Contains(f.Message, "出口 IP") != test.sharedIP {
			t.Fatalf("wrong scope: %+v", f)
		}
	}
}
