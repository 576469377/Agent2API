package openrouter

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/576469377/Agent2API/internal/llm"
)

const catalogue = `{"data":[
{"id":"vendor/free:free","name":"Free","context_length":32000,"top_provider":{"max_completion_tokens":8000},"pricing":{"prompt":"0","completion":"0","request":"0"},"architecture":{"input_modalities":["text","image"],"output_modalities":["text"]},"supported_parameters":["tools","reasoning"]},
{"id":"vendor/paid","pricing":{"prompt":"0.01","completion":"0.02"}},
{"id":"vendor/fake:free","pricing":{"prompt":"0","completion":"0","image":"0.1"}},
{"id":"vendor/unknown:free"},
{"id":"openrouter/auto","pricing":{"prompt":"-1","completion":"-1"}},
{"id":"vendor/free-audio","architecture":{"output_modalities":["text","audio"]},"pricing":{"prompt":"0","completion":"0"}},
{"id":"openrouter/free","context_length":10000,"pricing":{"prompt":"0","completion":"0"}}
]}`

func testAdapter(t *testing.T, handler http.HandlerFunc) *Adapter {
	t.Helper()
	t.Setenv("OPENROUTER_API_KEY", "")
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	a, err := New(Config{BaseURL: server.URL, APIKey: "test-key"})
	if err != nil {
		t.Fatal(err)
	}
	return a
}

func TestCatalogueAndFreeRequestGuard(t *testing.T) {
	var calls atomic.Int32
	a := testAdapter(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer test-key" {
			t.Error("missing upstream key")
		}
		if r.URL.Path != "/models" {
			calls.Add(1)
			t.Error("a forbidden model reached generation")
		}
		io.WriteString(w, catalogue)
	})
	models, err := a.ListModels(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(models) != 2 || models[0].ID != "vendor/free:free" || !models[0].SupportsImages || !models[0].SupportsTools || !models[0].SupportsThinking || models[0].Pricing.Status != "free" || models[0].MaxOutputTokens != 8000 {
		t.Fatalf("wrong free metadata: %+v", models)
	}
	for _, id := range []string{"vendor/paid", "vendor/fake:free", "vendor/unknown:free", "openrouter/auto", "vendor/invented:free"} {
		_, err = a.Stream(context.Background(), llm.RequestMessages{Model: id})
		if err == nil || llm.Wrap(err).Code != "free_model_required" {
			t.Fatalf("%s: %v", id, err)
		}
	}
	if calls.Load() != 0 {
		t.Fatal("paid request sent")
	}
}

func TestFreePricingRejectsUnknownAndAllAdditionalCharges(t *testing.T) {
	for _, raw := range []string{`null`, `{}`, `{"prompt":"0"}`, `{"prompt":null,"completion":"0"}`, `{"prompt":"NaN","completion":"0"}`, `{"prompt":"-1","completion":"0"}`, `{"prompt":"1e-999","completion":"0"}`, `{"prompt":"0","completion":"0","request":"0.01"}`, `{"prompt":"0","completion":"0","web_search":"1"}`} {
		var prices map[string]json.RawMessage
		json.Unmarshal([]byte(raw), &prices)
		if freePricing(prices) {
			t.Errorf("charged/unknown price considered free: %s", raw)
		}
	}
	for _, raw := range []string{`{"prompt":"0","completion":"0.0000"}`, `{"prompt":0,"completion":"0e-9","image":"0"}`} {
		var prices map[string]json.RawMessage
		json.Unmarshal([]byte(raw), &prices)
		if !freePricing(prices) {
			t.Errorf("zero rejected: %s", raw)
		}
	}
}

func TestCacheInvalidationRejectsNewlyPaidModel(t *testing.T) {
	var paid atomic.Bool
	a := testAdapter(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/models" {
			t.Fatal("paid generation sent")
		}
		body := catalogue
		if paid.Load() {
			body = strings.Replace(body, `"prompt":"0"`, `"prompt":"1"`, -1)
		}
		io.WriteString(w, body)
	})
	if _, err := a.ListModels(context.Background()); err != nil {
		t.Fatal(err)
	}
	paid.Store(true)
	a.InvalidateModels()
	_, err := a.Stream(context.Background(), llm.RequestMessages{Model: "vendor/free:free"})
	if err == nil || llm.Wrap(err).Code != "free_model_required" {
		t.Fatalf("did not reject new price: %v", err)
	}
}

func TestPayloadAndStreamToolsThinkingAndUsage(t *testing.T) {
	a := testAdapter(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/models" {
			io.WriteString(w, catalogue)
			return
		}
		var body map[string]any
		if json.NewDecoder(r.Body).Decode(&body) != nil {
			t.Error("invalid request")
		}
		if body["model"] != "vendor/free:free" || body["stream"] != true {
			t.Error("wrong upstream request")
		}
		price := body["provider"].(map[string]any)["max_price"].(map[string]any)
		for _, k := range []string{"prompt", "completion", "request", "image"} {
			if price[k] != float64(0) {
				t.Errorf("missing zero price %s", k)
			}
		}
		if body["tool_choice"].(map[string]any)["function"].(map[string]any)["name"] != "look" {
			t.Error("named tool choice lost")
		}
		msgs := body["messages"].([]any)
		if msgs[0].(map[string]any)["content"] != "system unchanged" {
			t.Error("system prompt modified")
		}
		if msgs[3].(map[string]any)["role"] != "tool" || msgs[3].(map[string]any)["tool_call_id"] != "prior" {
			t.Error("tool result association lost")
		}
		if len(msgs[3].(map[string]any)["content"].([]any)) != 2 {
			t.Error("tool result image lost")
		}
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, ": heartbeat\n\ndata: {\"id\":\"gen-test\",\"model\":\"vendor/free:free\",\"choices\":[{\"index\":0,\"delta\":{\"reasoning\":\"think\"}}]}\n\n")
		io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"content\":\"hello\",\"tool_calls\":[{\"index\":0,\"id\":\"call-1\",\"function\":{\"arguments\":\"{\\\"x\\\":\"}}]}}]}\n\n")
		io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"tool_calls\":[{\"index\":0,\"function\":{\"name\":\"look\",\"arguments\":\"1}\"}}]},\"finish_reason\":\"tool_calls\"}]}\n\n")
		io.WriteString(w, "data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"tool_calls\"}],\ndata: \"usage\":{\"prompt_tokens\":12,\"completion_tokens\":8,\"total_tokens\":20,\"prompt_tokens_details\":{\"cached_tokens\":3},\"completion_tokens_details\":{\"reasoning_tokens\":2}}}\n\ndata: [DONE]\n\n")
	})
	stream, err := a.Stream(context.Background(), llm.RequestMessages{Model: "vendor/free:free", SystemPrompt: "system unchanged", Messages: []llm.Message{
		{Role: llm.RoleUser, Content: []llm.Content{{Type: llm.ContentImage, ImageURL: "data:image/png;base64,AAAA"}}},
		{Role: llm.RoleAssistant, Content: []llm.Content{{Type: llm.ContentToolCall, ToolCall: &llm.ToolCall{ID: "prior", Name: "look", Arguments: "{}"}}}},
		{Role: llm.RoleTool, ToolCallID: "prior", Content: []llm.Content{{Type: llm.ContentToolResult, ToolResult: &llm.ToolResult{ToolCallID: "prior", Blocks: []llm.Content{{Type: llm.ContentText, Text: "result"}, {Type: llm.ContentImage, ImageURL: "https://example.invalid/image.png"}}}}}},
	}, Tools: []llm.ToolDefinition{{Name: "look"}}, ToolChoice: &llm.ToolChoice{Mode: llm.ToolChoiceFunction, FunctionName: "look"}})
	if err != nil {
		t.Fatal(err)
	}
	var args, text, think string
	indices := map[int]llm.ResponseEventType{}
	done := false
	for {
		ev, err := stream.Recv(context.Background())
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		switch ev.Type {
		case llm.EventThinkingStart, llm.EventTextStart, llm.EventToolCallStart:
			if _, ok := indices[ev.ContentIndex]; ok {
				t.Error("content index collision")
			}
			indices[ev.ContentIndex] = ev.Type
		case llm.EventTextDelta:
			text += ev.Delta
		case llm.EventThinkingDelta:
			think += ev.Delta
		case llm.EventToolCallDelta:
			args += ev.Delta
		case llm.EventToolCallEnd:
			if ev.ToolArguments != `{"x":1}` || ev.ToolName != "look" || ev.ToolCallID != "call-1" {
				t.Errorf("bad tool: %+v", ev)
			}
		case llm.EventDone:
			done = true
			if ev.StopReason != llm.StopReasonToolCalls || ev.Usage == nil || ev.Usage.CachedTokens != 3 || ev.Usage.ReasoningTokens != 2 || ev.Usage.TotalTokens != 20 {
				t.Errorf("bad usage/finish: %+v", ev)
			}
		}
	}
	if !done || text != "hello" || think != "think" || args != `{"x":1}` {
		t.Fatalf("response lost: %v %q %q %q", done, text, think, args)
	}
}

func TestUpstreamFailures(t *testing.T) {
	for _, status := range []int{401, 402, 429, 503} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			a := testAdapter(t, func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/models" {
					io.WriteString(w, catalogue)
					return
				}
				w.Header().Set("Retry-After", "17")
				w.WriteHeader(status)
				io.WriteString(w, `{"error":{"message":"test upstream error"}}`)
			})
			_, err := a.Stream(context.Background(), llm.RequestMessages{Model: "vendor/free:free"})
			f := llm.Wrap(err)
			if f == nil || f.RetryAfterSeconds != 17 || (status == 401 && !f.Unauthorized) || (status == 402 && !f.QuotaExhausted) || (status == 429 && !f.RateLimited) || (status == 503 && !f.UpstreamFault) {
				t.Fatalf("bad classification: %+v", f)
			}
		})
	}
	for name, data := range map[string]string{"inband": `data: {"error":{"code":429,"message":"quota"}}` + "\n\n", "truncated": `data: {"choices":[{"delta":{"content":"partial"}}]}` + "\n\n", "invalid": "data: {broken\n\n"} {
		t.Run(name, func(t *testing.T) {
			a := testAdapter(t, func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/models" {
					io.WriteString(w, catalogue)
					return
				}
				w.Header().Set("Content-Type", "text/event-stream")
				io.WriteString(w, data)
			})
			s, err := a.Stream(context.Background(), llm.RequestMessages{Model: "vendor/free:free"})
			if err != nil {
				t.Fatal(err)
			}
			for {
				ev, err := s.Recv(context.Background())
				if err != nil {
					if errors.Is(err, io.EOF) {
						t.Error("failure became success")
					}
					if name == "inband" && !llm.Wrap(err).RateLimited {
						t.Error("in-band status lost")
					}
					break
				}
				if ev.Type == llm.EventDone {
					t.Fatal("failure emitted done")
				}
			}
		})
	}
}

func TestMissingKeyAndIdleTimeout(t *testing.T) {
	a := testAdapter(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/models" {
			io.WriteString(w, catalogue)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.(http.Flusher).Flush()
		<-r.Context().Done()
	})
	a.cfg.APIKey = ""
	if a.Describe().Status != "unauthenticated" {
		t.Fatal("missing key reported active")
	}
	_, err := a.Stream(context.Background(), llm.RequestMessages{Model: "vendor/free:free"})
	if err == nil || !llm.Wrap(err).Unauthorized {
		t.Fatalf("missing key: %v", err)
	}
	a.cfg.APIKey = "test-key"
	a.cfg.StreamIdleTimeout = 30 * time.Millisecond
	s, err := a.Stream(context.Background(), llm.RequestMessages{Model: "vendor/free:free"})
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.Recv(context.Background())
	if err == nil || !llm.Wrap(err).Timeout {
		t.Fatalf("idle timeout not enforced: %v", err)
	}
}

func TestProviderErrorDetailsAreUsefulAndBounded(t *testing.T) {
	for _, raw := range []string{
		`{"message":"max_tokens exceeds provider limit","request":{"prompt":"private prompt"},"request_id":"private-id"}`,
		`"{\"error\":{\"message\":\"max_tokens exceeds provider limit\"},\"request_id\":\"private-id\"}"`,
	} {
		a := testAdapter(t, func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/models" {
				io.WriteString(w, catalogue)
				return
			}
			w.WriteHeader(400)
			fmt.Fprintf(w, `{"error":{"message":"Provider returned error","metadata":{"provider_name":"AtlasCloud","raw":%s}}}`, raw)
		})
		_, err := a.Stream(context.Background(), llm.RequestMessages{Model: "vendor/free:free"})
		f := llm.Wrap(err)
		if f == nil || f.Message != "OpenRouter: Provider returned error [AtlasCloud]: max_tokens exceeds provider limit" || !f.ClientFixable {
			t.Fatalf("provider detail lost: %v", err)
		}
		if strings.Contains(f.Message, "private") {
			t.Fatal("raw metadata leaked")
		}
	}
	var e apiError
	json.Unmarshal([]byte(`{"code":429,"message":"Provider error Bearer test-token","metadata":{"provider_name":"Provider","raw":{"msg":"retry sk-test-secret-secret","prompt":"private"}}}`), &e)
	f := streamFailure(&e)
	if !f.RateLimited || strings.Contains(f.Message, "test-token") || strings.Contains(f.Message, "test-secret") || strings.Contains(f.Message, "private") {
		t.Fatalf("unsafe stream error: %+v", f)
	}
	if len([]rune(errorText(strings.Repeat("长", 1000), 512))) != 513 {
		t.Fatal("unbounded provider error")
	}
	f = failure(429, "Rate limit exceeded: free-models-per-day. Add 10 credits to unlock more requests")
	if !f.RateLimited || f.Unauthorized || !strings.Contains(f.Message, "共享额度") || strings.Contains(f.Message, "credits") {
		t.Fatalf("daily free limit unclear: %+v", f)
	}
}
