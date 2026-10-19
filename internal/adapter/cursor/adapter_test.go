package cursor

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/576469377/Agent2API/internal/llm"
)

func testAdapter(t *testing.T, generate http.HandlerFunc) (*Adapter, *atomic.Bool, *atomic.Int32) {
	t.Helper()
	paid := &atomic.Bool{}
	calls := &atomic.Int32{}
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer test-token" {
			t.Error("upstream credential missing")
		}
		if r.Header.Get("X-Cursor-Client-Version") != "3.24.18" || r.Header.Get("X-Cursor-Checksum") == "" {
			t.Error("missing actual client identity")
		}
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/auth/full_stripe_profile":
			membership := "free"
			if paid.Load() {
				membership = "pro"
			}
			json.NewEncoder(w).Encode(map[string]any{"membershipType": membership, "individualMembershipType": membership, "subscriptionStatus": "unpaid", "isTeamMember": false, "isOnStudentPlan": false})
		case "/aiserver.v1.DashboardService/GetHardLimit":
			io.WriteString(w, `{"noUsageBasedAllowed":true}`)
		case "/aiserver.v1.ServerConfigService/GetServerConfig":
			io.WriteString(w, `{"configVersion":"current-config","clientVersionStatus":{"minSupportedClientVersion":"3.12.0"}}`)
		case "/agent.v1.AgentService/GetUsableModels":
			io.WriteString(w, `{"models":[{"modelId":"composer-2.5"},{"modelId":"paid-model"}]}`)
		case "/agent.v1.AgentService/Run":
			calls.Add(1)
			generate(w, r)
		default:
			t.Errorf("unexpected endpoint %s", r.URL.Path)
			w.WriteHeader(404)
		}
	})
	server := httptest.NewUnstartedServer(h)
	server.EnableHTTP2 = true
	server.StartTLS()
	t.Cleanup(server.Close)
	a, err := New(Config{BaseURL: server.URL, StreamIdleTimeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	a.client = server.Client()
	a.load = func(context.Context, string) (credentials, error) {
		return credentials{token: "test-token", version: "3.24.18", commit: "actual-commit", machine: "test-device"}, nil
	}
	return a, paid, calls
}

func startResponse(t *testing.T, w http.ResponseWriter, r *http.Request) map[string]any {
	t.Helper()
	if r.ProtoMajor != 2 {
		t.Errorf("HTTP/2 required")
	}
	if r.Header.Get("X-Cursor-Config-Version") != "current-config" {
		t.Error("server config not forwarded")
	}
	_, raw, err := readFrame(r.Body)
	if err != nil {
		t.Fatal(err)
	}
	var input map[string]any
	if json.Unmarshal(raw, &input) != nil {
		t.Fatal("bad payload")
	}
	w.Header().Set("Content-Type", "application/connect+json")
	w.WriteHeader(200)
	w.(http.Flusher).Flush()
	return input["runRequest"].(map[string]any)
}
func send(t *testing.T, w http.ResponseWriter, msg any) {
	t.Helper()
	if err := writeFrame(w, msg); err != nil {
		t.Error(err)
	}
	w.(http.Flusher).Flush()
}
func drain(t *testing.T, stream llm.ResponseStream) ([]llm.ResponseEvent, error) {
	t.Helper()
	var out []llm.ResponseEvent
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	for {
		ev, err := stream.Recv(ctx)
		if err == io.EOF {
			return out, nil
		}
		if err != nil {
			return out, err
		}
		out = append(out, ev)
	}
}
func input() llm.RequestMessages {
	return llm.RequestMessages{Model: verifiedModel, Messages: []llm.Message{{Role: llm.RoleUser, Content: []llm.Content{{Type: llm.ContentText, Text: "Hello"}}}}}
}

func TestFreeGuardRecheckedAndUnverifiedModelsBlocked(t *testing.T) {
	a, paid, calls := testAdapter(t, func(w http.ResponseWriter, r *http.Request) {
		startResponse(t, w, r)
		send(t, w, map[string]any{"interactionUpdate": map[string]any{"turnEnded": map[string]any{}}})
	})
	models, err := a.ListModels(context.Background())
	if err != nil || len(models) != 1 || models[0].Pricing.Source != "cursor_free_plan" {
		t.Fatalf("catalogue: %v %v", models, err)
	}
	paid.Store(true)
	if _, err = a.Stream(context.Background(), input()); err == nil {
		t.Fatal("paid state accepted")
	}
	for _, id := range []string{"auto", "default", "composer-2.5-fast", "paid-model"} {
		r := input()
		r.Model = id
		if _, err = a.Stream(context.Background(), r); err == nil {
			t.Fatal("unverified model accepted")
		}
	}
	if calls.Load() != 0 {
		t.Fatal("generation occurred before guards passed")
	}
}

func TestMissingOrBillableAccountStateStopsBeforeGenerationSetup(t *testing.T) {
	for _, field := range []string{"membershipType", "individualMembershipType", "subscriptionStatus", "isTeamMember", "isOnStudentPlan", "noUsageBasedAllowed", "billing_enabled", "trial", "team", "student"} {
		t.Run(field, func(t *testing.T) {
			profile := map[string]any{"membershipType": "free", "individualMembershipType": "free", "subscriptionStatus": "unpaid", "isTeamMember": false, "isOnStudentPlan": false}
			limits := map[string]any{"noUsageBasedAllowed": true}
			switch field {
			case "billing_enabled":
				limits["noUsageBasedAllowed"] = false
			case "trial":
				profile["subscriptionStatus"] = "trialing"
			case "team":
				profile["isTeamMember"] = true
			case "student":
				profile["isOnStudentPlan"] = true
			case "noUsageBasedAllowed":
				delete(limits, field)
			default:
				delete(profile, field)
			}
			var later atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/auth/full_stripe_profile" {
					json.NewEncoder(w).Encode(profile)
				} else if strings.HasSuffix(r.URL.Path, "GetHardLimit") {
					json.NewEncoder(w).Encode(limits)
				} else {
					later.Add(1)
					io.WriteString(w, `{}`)
				}
			}))
			defer server.Close()
			a, _ := New(Config{BaseURL: server.URL})
			a.load = func(context.Context, string) (credentials, error) {
				return credentials{token: "test-token", version: "3.24.18", machine: "test-device"}, nil
			}
			if _, err := a.Stream(context.Background(), input()); err == nil || later.Load() != 0 {
				t.Fatal("unconfirmed free account proceeded to generation setup")
			}
		})
	}
}

func TestConnectBidiBlobAndCompletion(t *testing.T) {
	a, _, _ := testAdapter(t, func(w http.ResponseWriter, r *http.Request) {
		startResponse(t, w, r)
		send(t, w, map[string]any{"kvServerMessage": map[string]any{"setBlobArgs": map[string]any{"blobId": "a", "blobData": "eA=="}}})
		_, ack, err := readFrame(r.Body)
		if err != nil || !bytes.Contains(ack, []byte(`"setBlobResult"`)) {
			t.Errorf("missing zero-ID KV ack: %s %v", ack, err)
			return
		}
		send(t, w, map[string]any{"interactionUpdate": map[string]any{"thinkingDelta": map[string]any{"text": "Consider"}}})
		send(t, w, map[string]any{"interactionUpdate": map[string]any{"textDelta": map[string]any{"text": "OK"}}})
		send(t, w, map[string]any{"interactionUpdate": map[string]any{"turnEnded": map[string]any{"inputTokens": "10", "outputTokens": "2", "cacheReadTokens": "1"}}})
	})
	s, err := a.Stream(context.Background(), input())
	if err != nil {
		t.Fatal(err)
	}
	events, err := drain(t, s)
	if err != nil {
		t.Fatal(err)
	}
	var text string
	for _, e := range events {
		if e.Type == llm.EventTextDelta {
			text += e.Delta
		}
	}
	last := events[len(events)-1]
	if text != "OK" || last.Type != llm.EventDone || last.Usage.TotalTokens != 12 {
		t.Fatalf("bad events: %+v", events)
	}
}

func TestToolSchemaAndCallAreRelayedWithoutExecution(t *testing.T) {
	a, _, _ := testAdapter(t, func(w http.ResponseWriter, r *http.Request) {
		startResponse(t, w, r)
		send(t, w, map[string]any{"execServerMessage": map[string]any{"mcpStateExecArgs": map[string]any{}}})
		_, ack, err := readFrame(r.Body)
		if err != nil || !bytes.Contains(ack, []byte(`"lookup"`)) {
			t.Errorf("schema missing: %s %v", ack, err)
			return
		}
		send(t, w, map[string]any{"execServerMessage": map[string]any{"mcpArgs": map[string]any{"providerIdentifier": "agent2api", "toolName": "lookup", "toolCallId": "call-1", "args": map[string]any{"city": "Shanghai"}}}})
	})
	r := input()
	r.Tools = []llm.ToolDefinition{{Name: "lookup", Parameters: map[string]any{"type": "object"}}}
	s, err := a.Stream(context.Background(), r)
	if err != nil {
		t.Fatal(err)
	}
	events, err := drain(t, s)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, e := range events {
		if e.Type == llm.EventToolCallEnd {
			found = e.ToolName == "lookup" && strings.Contains(e.ToolArguments, "Shanghai")
		}
	}
	if !found || events[len(events)-1].StopReason != llm.StopReasonToolCalls {
		t.Fatalf("tool not relayed: %+v", events)
	}
}

func TestIncompleteAndHostActionsAreFailures(t *testing.T) {
	for _, kind := range []string{"truncated", "shell", "unknown_tool", "error"} {
		t.Run(kind, func(t *testing.T) {
			a, _, _ := testAdapter(t, func(w http.ResponseWriter, r *http.Request) {
				startResponse(t, w, r)
				switch kind {
				case "truncated":
					send(t, w, map[string]any{"interactionUpdate": map[string]any{"textDelta": map[string]any{"text": "partial"}}})
				case "shell":
					send(t, w, map[string]any{"execServerMessage": map[string]any{"shellArgs": map[string]any{"command": "do not execute"}}})
				case "unknown_tool":
					send(t, w, map[string]any{"execServerMessage": map[string]any{"mcpArgs": map[string]any{"providerIdentifier": "agent2api", "toolName": "unknown"}}})
				case "error":
					raw := []byte(`{"error":{"code":"permission_denied","message":"test-token"}}`)
					frame := make([]byte, 5+len(raw))
					frame[0] = 2
					binary.BigEndian.PutUint32(frame[1:], uint32(len(raw)))
					copy(frame[5:], raw)
					w.Write(frame)
					w.(http.Flusher).Flush()
				}
			})
			s, err := a.Stream(context.Background(), input())
			if err != nil {
				t.Fatal(err)
			}
			events, err := drain(t, s)
			if err == nil || strings.Contains(err.Error(), "test-token") {
				t.Fatalf("unsafe success/error: %v", err)
			}
			for _, e := range events {
				if e.Type == llm.EventDone {
					t.Fatal("failure reported as completion")
				}
			}
		})
	}
}

func TestIdleTimeoutAndCallerCancel(t *testing.T) {
	for _, cancelEarly := range []bool{false, true} {
		t.Run(strconvBool(cancelEarly), func(t *testing.T) {
			a, _, _ := testAdapter(t, func(w http.ResponseWriter, r *http.Request) { startResponse(t, w, r); <-r.Context().Done() })
			a.cfg.StreamIdleTimeout = 30 * time.Millisecond
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			s, err := a.Stream(ctx, input())
			if err != nil {
				t.Fatal(err)
			}
			if cancelEarly {
				cancel()
			}
			_, err = drain(t, s)
			if err == nil {
				t.Fatal("unterminated stream accepted")
			}
		})
	}
}
func strconvBool(b bool) string {
	if b {
		return "cancel"
	}
	return "idle"
}

func TestPayloadPreservesHistoryAndRejectsUnsupportedOptions(t *testing.T) {
	r := input()
	r.SystemPrompt = "Answer concisely"
	r.Messages = append([]llm.Message{{Role: llm.RoleAssistant, Content: []llm.Content{{Type: llm.ContentText, Text: "Earlier"}}}}, r.Messages...)
	p, _, blobs, err := buildRequest(r)
	if err != nil || len(blobs) != 1 {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(p)
	if !bytes.Contains(raw, []byte("Answer concisely")) || !bytes.Contains(raw, []byte("client_instructions")) {
		t.Fatal("system rules lost")
	}
	temp := 0.1
	r.Temperature = &temp
	if _, _, _, err = buildRequest(r); err == nil {
		t.Fatal("unsupported sampling silently ignored")
	}
	if !versionAtLeast("3.24.18", "3.12.0") || versionAtLeast("3.0.9", "3.12.0") || versionAtLeast("invalid", "3.12.0") {
		t.Fatal("version comparison invalid")
	}
}

func TestToolContinuationUsesNativeCoreMessages(t *testing.T) {
	r := input()
	r.Tools = []llm.ToolDefinition{{Name: "Read"}}
	r.Messages = append(r.Messages,
		llm.Message{Role: llm.RoleAssistant, Content: []llm.Content{
			{Type: llm.ContentText, Text: "Reading"},
			{Type: llm.ContentToolCall, ToolCall: &llm.ToolCall{ID: "first", Name: "Read", Arguments: `{"file_path":"missing.txt"}`}},
			{Type: llm.ContentToolCall, ToolCall: &llm.ToolCall{ID: "second", Name: "Read", Arguments: `{"file_path":"README.md"}`}},
		}},
		llm.Message{Role: llm.RoleUser, Content: []llm.Content{
			{Type: llm.ContentToolResult, ToolResult: &llm.ToolResult{ToolCallID: "first", Content: "not found", IsError: true}},
			{Type: llm.ContentToolResult, ToolResult: &llm.ToolResult{ToolCallID: "second", Blocks: []llm.Content{{Type: llm.ContentText, Text: "file content"}}}},
			{Type: llm.ContentText, Text: "Continue reviewing"},
		}},
	)
	p, tools, blobs, err := buildRequest(r)
	if err != nil {
		t.Fatal(err)
	}
	context := p["action"].(map[string]any)["userMessageAction"].(map[string]any)["requestContext"].(map[string]any)
	if context["mcpInfoComplete"] != true || context["mcpFileSystemOptions"].(map[string]any)["enabled"] != false {
		t.Fatal("MCP discovery still depends on desktop descriptor files")
	}
	meta := context["mcpMetaToolOptions"].(map[string]any)
	descriptors := meta["mcpDescriptors"].([]map[string]any)
	if meta["enabled"] != true || len(descriptors) != 1 || descriptors[0]["serverIdentifier"] != "agent2api" || len(descriptors[0]["tools"].([]map[string]any)) != 1 {
		t.Fatal("inline client schemas missing")
	}
	ids := p["conversationState"].(map[string]any)["rootPromptMessagesJson"].([]string)
	var history []map[string]any
	for _, id := range ids {
		raw, err := base64.StdEncoding.DecodeString(blobs[id])
		if err != nil {
			t.Fatal(err)
		}
		var message map[string]any
		if json.Unmarshal(raw, &message) != nil {
			t.Fatal("invalid history blob")
		}
		history = append(history, message)
	}
	if len(history) != 4 || history[1]["role"] != "assistant" || history[2]["role"] != "tool" || history[3]["role"] != "user" {
		t.Fatalf("history role/order lost: %+v", history)
	}
	call := history[1]["content"].([]any)[1].(map[string]any)
	if call["type"] != "tool-call" || call["toolName"] != tools[0]["name"] || call["toolCallId"] != "first" || call["args"].(map[string]any)["file_path"] != "missing.txt" {
		t.Fatalf("call was flattened or renamed: %+v", call)
	}
	results := history[2]["content"].([]any)
	first, second := results[0].(map[string]any), results[1].(map[string]any)
	if first["type"] != "tool-result" || first["toolName"] != call["toolName"] || first["isError"] != true || second["toolCallId"] != "second" || second["result"] != "file content" {
		t.Fatalf("tool result association lost: %+v", results)
	}
	// A tool-only last message must remain a tool result, not a new user prompt.
	r.Messages[len(r.Messages)-1] = llm.Message{Role: llm.RoleTool, ToolCallID: "first", Content: []llm.Content{{Type: llm.ContentText, Text: "plain result"}}}
	if _, _, _, err := buildRequest(r); err != nil {
		t.Fatal(err)
	}
	r.Messages[len(r.Messages)-1].ToolCallID = "unknown"
	if _, _, _, err := buildRequest(r); err == nil {
		t.Fatal("orphan result silently accepted")
	}
}

func TestNativeReadIsReturnedToDeclaredClientTool(t *testing.T) {
	for _, allowed := range []bool{true, false} {
		t.Run(strconvBool(allowed), func(t *testing.T) {
			a, _, _ := testAdapter(t, func(w http.ResponseWriter, r *http.Request) {
				startResponse(t, w, r)
				send(t, w, map[string]any{"execServerMessage": map[string]any{"readArgs": map[string]any{"path": "C:/fixture/README.md", "toolCallId": "native-read", "offset": 0, "limit": 20}}})
				<-r.Context().Done()
			})
			r := input()
			r.Tools = []llm.ToolDefinition{{Name: "Read", Parameters: map[string]any{"type": "object", "properties": map[string]any{"file_path": map[string]any{"type": "string"}, "offset": map[string]any{"type": "number"}, "limit": map[string]any{"type": "number"}}, "required": []string{"file_path"}}}}
			if !allowed {
				r.ToolChoice = &llm.ToolChoice{Mode: llm.ToolChoiceNone}
			}
			stream, err := a.Stream(context.Background(), r)
			if err != nil {
				t.Fatal(err)
			}
			events, err := drain(t, stream)
			if !allowed {
				if err == nil {
					t.Fatal("tool_choice none bypassed")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			found := false
			for _, event := range events {
				if event.Type == llm.EventToolCallEnd {
					found = true
					if event.ToolName != "Read" || event.ToolCallID != "native-read" || event.ToolArguments != `{"file_path":"C:/fixture/README.md","limit":20,"offset":1}` {
						t.Fatalf("wrong client Read mapping: %+v", event)
					}
				}
			}
			if !found || events[len(events)-1].StopReason != llm.StopReasonToolCalls {
				t.Fatal("native operation was not returned as a tool call")
			}
		})
	}
	if acceptsRead(map[string]any{"properties": map[string]any{"file_path": map[string]any{"type": "string"}}, "required": []any{"token"}}, map[string]any{"file_path": "test"}) {
		t.Fatal("incompatible custom Read schema accepted")
	}
}
