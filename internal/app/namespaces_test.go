package app

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/576469377/Agent2API/internal/config"
	"github.com/576469377/Agent2API/internal/llm"
)

type namespaceAdapter struct{ fakeAdapter }

func (*namespaceAdapter) Stream(_ context.Context, r llm.RequestMessages) (llm.ResponseStream, error) {
	if len(r.Tools) != 1 || !strings.HasPrefix(r.Tools[0].Name, "a2api_ns_") {
		return nil, fmt.Errorf("namespace was treated as a function")
	}
	if len(r.Messages) > 1 {
		call := r.Messages[1].Content[0].ToolCall
		result := r.Messages[2].Content[0].ToolResult
		if call == nil || result == nil || call.Name != r.Tools[0].Name || call.ID != result.ToolCallID {
			return nil, fmt.Errorf("tool history did not restore its internal name")
		}
		return &eventStream{events: []llm.ResponseEvent{
			{Type: llm.EventStart}, {Type: llm.EventTextStart}, {Type: llm.EventTextDelta, Delta: result.Content}, {Type: llm.EventTextEnd}, {Type: llm.EventDone},
		}}, nil
	}
	name := r.Tools[0].Name
	return &eventStream{events: []llm.ResponseEvent{
		{Type: llm.EventStart},
		{Type: llm.EventToolCallStart, ToolCallID: "call_fixture", ToolName: name},
		{Type: llm.EventToolCallDelta, Delta: "{}"},
		{Type: llm.EventToolCallEnd, ToolCallID: "call_fixture", ToolName: name, ToolArguments: "{}", ToolArgumentsDone: true},
		{Type: llm.EventDone, StopReason: llm.StopReasonToolCalls},
	}}, nil
}

func TestResponsesNamespaceHTTPRoundTrip(t *testing.T) {
	cfg := config.Default()
	cfg.MetricsFile = ""
	hub, err := NewHub([]*PlatformRuntime{{ID: "fake", Adapter: &namespaceAdapter{}}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(New(cfg, hub, nil).Handler())
	defer server.Close()
	for _, stream := range []bool{false, true} {
		for _, namespace := range []string{"left", "right"} {
			t.Run(fmt.Sprintf("%s/stream=%v", namespace, stream), func(t *testing.T) {
				request := map[string]any{
					"model": "fake/test-model", "stream": stream,
					"input": []any{map[string]any{"role": "user", "content": "lookup"}},
					"tools": []any{map[string]any{"type": "namespace", "name": namespace, "tools": []any{map[string]any{"type": "function", "name": "lookup", "parameters": map[string]any{"type": "object"}}}}},
				}
				post := func() []map[string]any {
					t.Helper()
					raw, _ := json.Marshal(request)
					r, err := http.Post(server.URL+"/v1/responses", "application/json", strings.NewReader(string(raw)))
					if err != nil {
						t.Fatal(err)
					}
					defer r.Body.Close()
					body, err := io.ReadAll(r.Body)
					if err != nil || r.StatusCode != 200 {
						t.Fatalf("status %d: %s (%v)", r.StatusCode, body, err)
					}
					var out struct {
						Output []map[string]any `json:"output"`
					}
					if !stream {
						if err = json.Unmarshal(body, &out); err != nil {
							t.Fatal(err)
						}
					} else {
						for _, line := range strings.Split(string(body), "\n") {
							if !strings.HasPrefix(line, "data:") {
								continue
							}
							var frame struct {
								Type     string          `json:"type"`
								Response json.RawMessage `json:"response"`
							}
							json.Unmarshal([]byte(strings.TrimSpace(strings.TrimPrefix(line, "data:"))), &frame)
							if frame.Type == "response.completed" {
								if err = json.Unmarshal(frame.Response, &out); err != nil {
									t.Fatal(err)
								}
							}
						}
					}
					if len(out.Output) != 1 {
						t.Fatalf("output=%v", out.Output)
					}
					return out.Output
				}
				call := post()[0]
				if call["name"] != "lookup" || call["namespace"] != namespace || call["call_id"] != "call_fixture" {
					t.Fatalf("namespace output lost: %v", call)
				}
				request["input"] = append(request["input"].([]any), call, map[string]any{"type": "function_call_output", "call_id": "call_fixture", "output": "opaque-result"})
				message := post()[0]
				content := message["content"].([]any)
				if content[0].(map[string]any)["text"] != "opaque-result" {
					t.Fatalf("tool result lost: %v", message)
				}
			})
		}
	}
}
