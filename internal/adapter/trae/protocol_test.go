package trae

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/576469377/Agent2API/internal/llm"
)

func fixture(t *testing.T, name string) []byte {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("../../../fixtures", name))
	if err != nil {
		t.Fatal(err)
	}
	return raw
}
func replay(t *testing.T, name string) []llm.ResponseEvent {
	t.Helper()
	s := newStream(io.NopCloser(strings.NewReader(string(fixture(t, name)))), func() {}, time.Second, "deepseek-v4.1-flash")
	defer s.Close()
	var out []llm.ResponseEvent
	for {
		e, err := s.Recv(context.Background())
		if errors.Is(err, llm.ErrStreamDone) {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, e)
	}
	return out
}

func TestReplayNativeTextThinkingAndUsage(t *testing.T) {
	events := replay(t, "trae-stream-text.sse")
	var text, thinking string
	var starts, done int
	for _, e := range events {
		switch e.Type {
		case llm.EventStart:
			starts++
			if e.ResponseID != "101" {
				t.Fatalf("numeric response ID was not preserved: %q", e.ResponseID)
			}
		case llm.EventDone:
			done++
			if e.StopReason != llm.StopReasonStop || e.Usage == nil || e.Usage.InputTokens != 38 || e.Usage.OutputTokens != 19 || e.Usage.ReasoningTokens != 17 {
				t.Fatalf("unexpected final usage: %+v", e.Usage)
			}
		case llm.EventTextDelta:
			text += e.Delta
		case llm.EventThinkingDelta:
			thinking += e.Delta
		}
	}
	if text != "OK" || thinking == "" || starts != 1 || done != 1 {
		t.Fatalf("text=%q, starts=%d, done=%d", text, starts, done)
	}
	if events[1].Type != llm.EventThinkingStart || events[len(events)-2].Type != llm.EventTextEnd {
		t.Fatal("content block boundaries were lost")
	}
}

func TestReplayNativeToolAndResultCache(t *testing.T) {
	events := replay(t, "trae-stream-toolcall.sse")
	var call llm.ResponseEvent
	var names []string
	for _, e := range events {
		if e.Type == llm.EventToolCallStart {
			names = append(names, e.ToolName)
		}
		if e.Type == llm.EventToolCallEnd {
			call = e
		}
	}
	if len(names) != 1 || names[0] != "lookup_test_value" || call.ToolCallID != "fixture-call" || call.ToolArguments != `{"tag": "demo"}` || !call.ToolArgumentsDone {
		t.Fatalf("invalid call: %+v", call)
	}
	if events[len(events)-1].StopReason != llm.StopReasonToolCalls {
		t.Fatal("tool stop reason missing")
	}
	events = replay(t, "trae-stream-tool-result.sse")
	done := events[len(events)-1]
	if done.Usage == nil || done.Usage.InputTokens != 376 || done.Usage.CachedTokens != 256 || done.Usage.OutputTokens != 16 {
		t.Fatalf("cache was invented or double counted: %+v", done.Usage)
	}
}

func TestStreamRequiresDoneAndDoesNotInventUsage(t *testing.T) {
	output := "event: output\ndata: {\"response\":\"ok\"}\n\n"
	s := newStream(io.NopCloser(strings.NewReader(output)), func() {}, time.Second, "m")
	defer s.Close()
	for {
		e, err := s.Recv(context.Background())
		if err != nil {
			if errors.Is(err, llm.ErrStreamDone) {
				t.Fatal("truncated stream succeeded")
			}
			break
		}
		if e.Type == llm.EventDone {
			t.Fatal("truncated stream emitted done")
		}
	}
	s = newStream(io.NopCloser(strings.NewReader(output+"event: done\ndata: {\"finish_reason\":\"stop\"}\n\n")), func() {}, time.Second, "m")
	defer s.Close()
	for {
		e, err := s.Recv(context.Background())
		if err != nil {
			if !errors.Is(err, llm.ErrStreamDone) {
				t.Fatal(err)
			}
			break
		}
		if e.Type == llm.EventDone && e.Usage != nil {
			t.Fatal("missing usage became measured zero")
		}
	}
}

func TestNativeIDsKeepIntegerPrecisionAndMissingUsageStaysUnknown(t *testing.T) {
	for _, raw := range []string{`{"prompt_completion_id":9007199254740993}`, `{"prompt_completion_id":"9007199254740993"}`} {
		s := &stream{}
		if err := s.consume("metadata", []byte(raw)); err != nil || s.id != "9007199254740993" {
			t.Fatalf("response ID lost precision: %q, %v", s.id, err)
		}
	}
	s := &stream{}
	for _, raw := range []string{`{}`, `{"cache_read_input_tokens":12}`, `{"prompt_tokens":20,"completion_tokens":5}`} {
		if err := s.consume("token_usage", []byte(raw)); err != nil || s.usage != nil {
			t.Fatal("incomplete usage became measured zero")
		}
	}
	if err := s.consume("token_usage", []byte(`{"prompt_tokens":0,"completion_tokens":0,"total_tokens":0}`)); err != nil || s.usage == nil {
		t.Fatal("explicit measured zero was lost")
	}
}

func TestStreamIdleAndCancellationCloseRead(t *testing.T) {
	for _, idle := range []bool{false, true} {
		r, w := io.Pipe()
		ctx, cancel := context.WithCancel(context.Background())
		timeout := time.Minute
		if idle {
			timeout = 20 * time.Millisecond
		}
		s := newStream(r, func() {}, timeout, "m")
		done := make(chan error, 1)
		go func() { _, err := s.Recv(ctx); done <- err }()
		if !idle {
			// Enter the read loop before cancelling, so this covers a blocked read.
			if _, err := w.Write([]byte(": heartbeat\n")); err != nil {
				t.Fatal(err)
			}
			cancel()
		}
		select {
		case err := <-done:
			if err == nil {
				t.Fatal("read succeeded")
			}
			if !idle && !errors.Is(err, context.Canceled) {
				t.Fatalf("client cancellation became an upstream failure: %v", err)
			}
		case <-time.After(time.Second):
			t.Fatal("read did not cancel")
		}
		cancel()
		s.Close()
		w.Close()
	}
}

func TestStreamImmediateIdlePublishesTimerBeforeCallback(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	for i := 0; i < 32; i++ {
		r, w := io.Pipe()
		s := newStream(r, func() {}, 0, "m")
		_, err := s.Recv(ctx)
		s.Close()
		w.Close()
		if err == nil || llm.Wrap(err).Code != "trae_stream_idle" {
			t.Fatal("immediate idle did not close the initialized stream", err)
		}
	}
}

func TestFreeQuotaRejectsPaidUnknownTrialAndExhaustion(t *testing.T) {
	raw := fixture(t, "trae-free-quota.json")
	now := time.Now()
	if n, err := verifyFreeQuota(raw, now); err != nil || n != 500 {
		t.Fatalf("free quota: %v %v", n, err)
	}
	cases := map[string]func(map[string]any){
		"paid_plan": func(q map[string]any) {
			q["user_entitlement_pack_list"].([]any)[0].(map[string]any)["entitlement_base_info"].(map[string]any)["product_type"] = 1
		},
		"purchased": func(q map[string]any) {
			q["user_entitlement_pack_list"].([]any)[1].(map[string]any)["entitlement_base_info"].(map[string]any)["charge_amount"] = 1
		},
		"unknown_source": func(q map[string]any) { q["user_entitlement_pack_list"].([]any)[1].(map[string]any)["group_type"] = 99 },
		"trial":          func(q map[string]any) { q["trial_status"].(map[string]any)["is_in_trial"] = true },
		"paygo":          func(q map[string]any) { q["is_dollar_usage_billing"] = true },
		"unknown":        func(q map[string]any) { delete(q, "is_dollar_usage_billing") },
		"missing_usage":  func(q map[string]any) { delete(q, "usage_summary") },
		"spent":          func(q map[string]any) { q["usage_summary"].(map[string]any)["consumed_amount"] = 500 },
	}
	for name, change := range cases {
		t.Run(name, func(t *testing.T) {
			var q map[string]any
			json.Unmarshal(raw, &q)
			change(q)
			bad, _ := json.Marshal(q)
			if _, err := verifyFreeQuota(bad, now); err == nil {
				t.Fatal("unsafe account accepted")
			}
		})
	}
}

func TestCatalogKeepsPositiveRatesAndExcludesRestrictedModel(t *testing.T) {
	models, err := decodeModels(fixture(t, "trae-model-catalog.json"), 500, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if len(models) != 3 {
		t.Fatalf("models=%d", len(models))
	}
	for _, m := range models {
		if m.Info.ID == "Doubao-Seed-Evolving" || m.Info.Pricing.Status != "credits" || *m.Info.Pricing.CreditMultiplier <= 0 || m.Info.Access == nil || m.Info.Access.OverageEnabled {
			t.Fatalf("invalid free metadata for %s", m.Info.ID)
		}
		if m.Info.ID == "Doubao-Seed-2.1-Turbo" {
			if m.Info.ContextTokens != 256000 || m.Info.MaxOutputTokens != 32000 || m.WireName != m.Info.ID+"__dev" {
				t.Fatal("Seed limits changed", m)
			}
		} else if m.Info.ContextTokens != 1000000 || m.Info.MaxOutputTokens != 64000 || m.WireName != m.Info.ID+"__max" {
			t.Fatal("solo_agent maximum window and matching wire variant were lost", m)
		}
	}
}

func TestCatalogFallsBackToDevWhenMaximumVariantIsIncomplete(t *testing.T) {
	var catalog map[string]any
	json.Unmarshal(fixture(t, "trae-model-catalog.json"), &catalog)
	for _, item := range catalog["config_info_list"].([]any) {
		m := item.(map[string]any)
		if m["config_name"] == "deepseek-v4.1-flash" {
			m["model_detail_list"] = m["model_detail_list"].([]any)[:1]
		}
		if m["config_name"] == "glm-5.3-flash" {
			m["display_config"].(map[string]any)["max_mode"] = false
		}
	}
	raw, _ := json.Marshal(catalog)
	models, err := decodeModels(raw, 500, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range models {
		if m.Info.ID != "Doubao-Seed-2.1-Turbo" && (m.Info.ContextTokens != 200000 || m.Info.MaxOutputTokens != 32000 || !strings.HasSuffix(m.WireName, "__dev")) {
			t.Fatal("incomplete maximum metadata published", m)
		}
	}
}

func TestPayloadPreservesNativeToolSchemaAndContinuation(t *testing.T) {
	r := llm.RequestMessages{SessionKey: "private-session", Tools: []llm.ToolDefinition{{Name: "lookup_test_value", Parameters: map[string]any{"type": "object", "properties": map[string]any{"tag": map[string]any{"type": "string"}}}}}, Messages: []llm.Message{
		{Role: llm.RoleAssistant, Content: []llm.Content{{Type: llm.ContentToolCall, ToolCall: &llm.ToolCall{ID: "fixture-call", Name: "lookup_test_value", Arguments: `{"tag":"demo"}`}}}},
		{Role: llm.RoleUser, Content: []llm.Content{{Type: llm.ContentToolResult, ToolResult: &llm.ToolResult{ToolCallID: "fixture-call", Content: `{"value":7}`}}}},
	}}
	m := catalogModel{WireName: "deepseek-v4.1-flash__dev", Source: 1}
	m.Info.ID = "deepseek-v4.1-flash"
	p, err := buildRequest(r, m)
	if err != nil {
		t.Fatal(err)
	}
	tools := p["tools"].([]map[string]any)
	if _, ok := tools[0]["function"].(map[string]any)["parameters"].(string); !ok {
		t.Fatal("schema was not a JSON string")
	}
	messages := p["messages"].([]map[string]any)
	if messages[1]["role"] != "tool" || messages[1]["tool_call_id"] != "fixture-call" {
		t.Fatal("tool result lost correlation")
	}
	call := messages[0]["tool_calls"].([]map[string]any)[0]
	if call["function_call"] == nil {
		t.Fatal("native function_call envelope missing")
	}
	p2, _ := buildRequest(r, m)
	if p2["session_id"] != p["session_id"] || p["session_id"] == r.SessionKey {
		t.Fatal("session identity not stable and private")
	}
}

func TestAdapterRechecksFreePlanBeforeGenerationAndSharesBudget(t *testing.T) {
	quota := fixture(t, "trae-free-quota.json")
	catalog := fixture(t, "trae-model-catalog.json")
	reply := fixture(t, "trae-stream-text.sse")
	var paid atomic.Bool
	var generations atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/trae/api/v2/pay/ide_user_ent_usage":
			if paid.Load() {
				w.Write([]byte(`{"is_credits_billing":true}`))
			} else {
				w.Write(quota)
			}
		case "/api/ide/v1/get_detail_param":
			var body struct {
				Function string `json:"function"`
			}
			_ = json.NewDecoder(r.Body).Decode(&body)
			if r.Header.Get("x-ide-function") != "solo_agent" || r.Header.Get("x-app-function") != "solo_agent" || body.Function != "solo_agent" || r.Header.Get("x-ide-version-code") != catalogProtocolVersionCode {
				t.Error("catalog request must identify its native function")
				w.Write([]byte(`{"config_info_list":[]}`))
				return
			}
			w.Write(catalog)
		case "/api/ide/v2/llm_raw_chat":
			var body struct {
				Model string `json:"model_name"`
			}
			_ = json.NewDecoder(r.Body).Decode(&body)
			if body.Model != "deepseek-v4.1-flash__max" {
				t.Error("generation did not use the advertised 1M variant")
			}
			generations.Add(1)
			w.Header().Set("Content-Type", "text/event-stream")
			w.Write(reply)
		default:
			w.WriteHeader(404)
		}
	}))
	defer srv.Close()
	a, _ := New(Config{BaseURL: srv.URL})
	a.authBase = srv.URL
	a.load = func(string) (Credential, error) {
		return Credential{UserID: "fixture-user", Token: "unit-test-only", ExpiresAt: time.Now().Add(time.Hour).Format(time.RFC3339)}, nil
	}
	execution := llm.NewExecution(1)
	ctx := llm.WithExecution(context.Background(), execution)
	req := llm.RequestMessages{Model: "deepseek-v4.1-flash", Messages: []llm.Message{{Role: llm.RoleUser, Content: []llm.Content{{Type: llm.ContentText, Text: "OK"}}}}}
	s, err := a.Stream(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	for {
		_, err = s.Recv(ctx)
		if err != nil {
			if !errors.Is(err, llm.ErrStreamDone) {
				t.Fatal(err)
			}
			break
		}
	}
	if execution.Snapshot().AttemptCount != 1 || generations.Load() != 1 {
		t.Fatal("read-only calls consumed generation attempts")
	}
	paid.Store(true)
	if _, err := a.Stream(context.Background(), req); err == nil {
		t.Fatal("cached catalog bypassed account check")
	}
	if generations.Load() != 1 {
		t.Fatal("unsafe request reached generation")
	}
}
