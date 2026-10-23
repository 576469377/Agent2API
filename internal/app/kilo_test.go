package app

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/576469377/Agent2API/internal/adapter/kilo"
	"github.com/576469377/Agent2API/internal/config"
)

func TestKiloAnonymousConnectDoesNotSaveCredentialsOrCreatePool(t *testing.T) {
	fixture := &setupFixture{}
	a := setupApp(t, "kilo", fixture)
	for _, input := range []struct {
		body   string
		status int
	}{
		{`{"action":"check"}`, http.StatusOK},
		{`{"action":"save_key","api_key":"not-accepted"}`, http.StatusBadRequest},
		{`{"action":"check","api_key":"not-accepted"}`, http.StatusBadRequest},
	} {
		w := httptest.NewRecorder()
		r := httptest.NewRequest("POST", "/api/accounts/connect?platform=kilo", strings.NewReader(input.body))
		r.Header.Set("Content-Type", "application/json")
		a.apiAccountsConnect(w, r)
		if w.Code != input.status {
			t.Fatalf("status %d: %s", w.Code, w.Body.String())
		}
	}
	if fixture.saves != 0 || fixture.checks != 1 {
		t.Fatalf("unexpected credential operation: %+v", fixture)
	}
	w := httptest.NewRecorder()
	a.startAccountLogin(w, httptest.NewRequest("POST", "/api/accounts/manage?platform=kilo", nil))
	if w.Code != 400 {
		t.Fatal("device OAuth accepted for anonymous access")
	}
	if _, err := os.Stat(a.hub.Platform("kilo").AccountsDir()); !os.IsNotExist(err) {
		t.Fatal("anonymous connection created WorkBuddy credential directory")
	}
}

func TestKiloHTTPProtocolsAndFreeOnlyToolContinuation(t *testing.T) {
	var posts atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "" || r.Header.Get("X-KiloCode-OrganizationId") != "" {
			t.Error("downstream credential forwarded")
		}
		if r.URL.Path == "/models" {
			io.WriteString(w, `{"data":[{"id":"vendor/test:free","name":"test","isFree":true,"pricing":{"prompt":"0","completion":"0"},"context_length":4096,"top_provider":{"max_completion_tokens":256},"architecture":{"output_modalities":["text"]},"supported_parameters":["tools"]}]}`)
			return
		}
		posts.Add(1)
		var body map[string]any
		if json.NewDecoder(r.Body).Decode(&body) != nil {
			t.Error("invalid body")
			w.WriteHeader(400)
			return
		}
		tool := body["tools"].([]any)[0].(map[string]any)["function"].(map[string]any)["name"].(string)
		messages := body["messages"].([]any)
		last := messages[len(messages)-1].(map[string]any)
		w.Header().Set("Content-Type", "text/event-stream")
		var delta map[string]any
		finish := "tool_calls"
		if last["role"] == "tool" {
			if last["tool_call_id"] != "call_fixture" || last["content"] != "opaque-tool-value" {
				t.Errorf("tool value or association lost: %+v", last)
			}
			previous := messages[len(messages)-2].(map[string]any)["tool_calls"].([]any)[0].(map[string]any)["function"].(map[string]any)["name"]
			if previous != tool {
				t.Error("history did not restore canonical tool identity")
			}
			delta, finish = map[string]any{"content": "opaque-tool-value"}, "stop"
		} else {
			// 实测模型会再次加 namespace 前缀；这里只允许恢复已登记的同一配对。
			if strings.HasPrefix(tool, "a2api_ns_") {
				tool = "mcp__acceptance." + tool
			}
			delta = map[string]any{"tool_calls": []any{map[string]any{"index": 0, "id": "call_fixture", "function": map[string]any{"name": tool, "arguments": "{}"}}}}
		}
		chunk, _ := json.Marshal(map[string]any{"choices": []any{map[string]any{"index": 0, "delta": delta, "finish_reason": finish}}})
		fmt.Fprintf(w, "data: %s\n\ndata: [DONE]\n\n", chunk)
	}))
	defer upstream.Close()
	a, err := kilo.New(kilo.Config{BaseURL: upstream.URL})
	if err != nil {
		t.Fatal(err)
	}
	hub, err := NewHub([]*PlatformRuntime{{ID: kilo.Platform, Adapter: a}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	cfg := config.Default()
	cfg.MetricsFile = ""
	server := httptest.NewServer(New(cfg, hub, nil).Handler())
	defer server.Close()
	for _, protocol := range []string{"chat/completions", "responses", "messages"} {
		for _, stream := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/stream=%v", protocol, stream), func(t *testing.T) {
				body := map[string]any{"model": "kilo/vendor/test:free", "stream": stream}
				input := []any{map[string]any{"role": "user", "content": "look up the value"}}
				tool := map[string]any{"name": "lookup", "description": "look up value", "parameters": map[string]any{"type": "object"}}
				switch protocol {
				case "responses":
					tool["type"] = "function"
					body["tools"] = []any{map[string]any{"type": "namespace", "name": "mcp__acceptance", "tools": []any{tool}}}
					body["input"] = input
				case "messages":
					body["tools"] = []any{map[string]any{"name": "lookup", "input_schema": tool["parameters"]}}
					body["messages"] = input
				default:
					body["tools"] = []any{map[string]any{"type": "function", "function": tool}}
					body["messages"] = input
				}
				post := func() string {
					t.Helper()
					raw, _ := json.Marshal(body)
					r, err := http.Post(server.URL+"/v1/"+protocol, "application/json", strings.NewReader(string(raw)))
					if err != nil {
						t.Fatal(err)
					}
					defer r.Body.Close()
					data, _ := io.ReadAll(r.Body)
					if r.StatusCode != 200 {
						t.Fatalf("HTTP %d: %s", r.StatusCode, data)
					}
					return string(data)
				}
				first := post()
				if !strings.Contains(first, `"name":"lookup"`) || !strings.Contains(first, "call_fixture") {
					t.Fatalf("tool call lost: %s", first)
				}
				switch protocol {
				case "responses":
					if !strings.Contains(first, `"namespace":"mcp__acceptance"`) {
						t.Fatal("qualified alias was not restored")
					}
					body["input"] = append(input, map[string]any{"type": "function_call", "name": "lookup", "namespace": "mcp__acceptance", "call_id": "call_fixture", "arguments": "{}"}, map[string]any{"type": "function_call_output", "call_id": "call_fixture", "output": "opaque-tool-value"})
				case "messages":
					body["messages"] = append(input, map[string]any{"role": "assistant", "content": []any{map[string]any{"type": "tool_use", "name": "lookup", "id": "call_fixture", "input": map[string]any{}}}}, map[string]any{"role": "user", "content": []any{map[string]any{"type": "tool_result", "tool_use_id": "call_fixture", "content": "opaque-tool-value"}}})
				default:
					body["messages"] = append(input, map[string]any{"role": "assistant", "tool_calls": []any{map[string]any{"id": "call_fixture", "type": "function", "function": map[string]any{"name": "lookup", "arguments": "{}"}}}}, map[string]any{"role": "tool", "tool_call_id": "call_fixture", "content": "opaque-tool-value"})
				}
				if !strings.Contains(post(), "opaque-tool-value") {
					t.Fatal("tool result lost")
				}
			})
		}
	}
	before := posts.Load()
	r, err := http.Post(server.URL+"/v1/chat/completions", "application/json", strings.NewReader(`{"model":"kilo/vendor/paid","messages":[{"role":"user","content":"refuse"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	r.Body.Close()
	if r.StatusCode != 400 || posts.Load() != before {
		t.Fatal("unverified route generated a request")
	}
}
