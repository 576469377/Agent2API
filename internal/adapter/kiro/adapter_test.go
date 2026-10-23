package kiro

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/576469377/Agent2API/internal/llm"
)

func TestMain(m *testing.M) {
	if os.Getenv("AGENT2API_KIRO_TEST_HELPER") == "1" {
		mockCLI()
		os.Exit(0)
	}
	os.Exit(m.Run())
}
func fixture(t *testing.T, name string) []byte {
	t.Helper()
	b, e := os.ReadFile(filepath.Join("..", "..", "..", "fixtures", name))
	if e != nil {
		t.Fatal(e)
	}
	return b
}

func mockCLI() {
	root := os.Getenv("AGENT2API_KIRO_FIXTURES")
	if len(os.Args) > 1 && os.Args[1] == "login" {
		os.Stdout.WriteString("Authorize this device: TEST-CODE\nhttps://app.kiro.dev/device\n")
		return
	}
	if len(os.Args) > 1 && os.Args[1] == "whoami" {
		os.Stdout.WriteString(`{"accountType":"BuilderId"}`)
		return
	}
	if len(os.Args) > 1 && os.Args[1] == "chat" {
		b, _ := os.ReadFile(filepath.Join(root, "kiro-model-catalog.json"))
		os.Stdout.Write(b)
		return
	}
	var mu sync.Mutex
	send := func(v any) { mu.Lock(); defer mu.Unlock(); json.NewEncoder(os.Stdout).Encode(v) }
	var endpoint string
	client := &http.Client{Timeout: 20 * time.Second}
	post := func(method string, params any) map[string]any {
		raw, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": method, "params": params})
		resp, e := client.Post(endpoint, "application/json", bytes.NewReader(raw))
		if e != nil {
			return nil
		}
		defer resp.Body.Close()
		var v map[string]any
		json.NewDecoder(resp.Body).Decode(&v)
		return v
	}
	scan := bufio.NewScanner(os.Stdin)
	scan.Buffer(make([]byte, 4096), 4*1024*1024)
	for scan.Scan() {
		var f rpcFrame
		if json.Unmarshal(scan.Bytes(), &f) != nil {
			continue
		}
		reply := func(result any) {
			send(map[string]any{"jsonrpc": "2.0", "id": json.RawMessage(f.ID), "result": result})
		}
		switch f.Method {
		case "initialize":
			reply(map[string]any{"protocolVersion": 1, "agentCapabilities": map[string]any{"mcpCapabilities": map[string]bool{"http": true}}})
		case "session/new":
			var p struct {
				Servers []struct {
					URL string `json:"url"`
				} `json:"mcpServers"`
			}
			json.Unmarshal(f.Params, &p)
			if len(p.Servers) > 0 {
				endpoint = p.Servers[0].URL
			}
			reply(map[string]any{"sessionId": "test-session", "modes": map[string]any{"currentModeId": "agent2api-proxy"}, "models": map[string]any{"availableModels": []any{map[string]string{"modelId": "qwen3-coder-next"}}}})
		case "_kiro.dev/commands/execute":
			b, _ := os.ReadFile(filepath.Join(root, "kiro-free-usage.json"))
			var v map[string]any
			json.Unmarshal(b, &v)
			if plan := os.Getenv("AGENT2API_KIRO_PLAN"); plan != "" {
				v["data"].(map[string]any)["planName"] = plan
			}
			if os.Getenv("AGENT2API_KIRO_EXHAUSTED") == "1" {
				breakdown := v["data"].(map[string]any)["usageBreakdowns"].([]any)[0].(map[string]any)
				breakdown["used"] = breakdown["limit"]
			}
			reply(v)
		case "session/set_model":
			reply(map[string]any{})
		case "session/prompt":
			if path := os.Getenv("AGENT2API_KIRO_AUDIT"); path != "" {
				file, _ := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0600)
				if file != nil {
					file.WriteString("prompt\n")
					file.Close()
				}
			}
			id := append(json.RawMessage(nil), f.ID...)
			go func() {
				if os.Getenv("AGENT2API_KIRO_HANG") == "1" {
					return
				}
				text := "mock-answer"
				if endpoint != "" {
					catalog := post("tools/list", map[string]any{})
					if catalog == nil {
						return
					}
					tools := catalog["result"].(map[string]any)["tools"].([]any)
					if len(tools) == 0 {
						return
					}
					name := tools[0].(map[string]any)["name"].(string)
					count := 1
					if os.Getenv("AGENT2API_KIRO_PARALLEL") == "1" {
						count = 2
					}
					results := make(chan map[string]any, count)
					for i := 0; i < count; i++ {
						go func() {
							results <- post("tools/call", map[string]any{"name": name, "arguments": map[string]any{"tag": "bridge-test"}})
						}()
					}
					text = ""
					for i := 0; i < count; i++ {
						result := <-results
						if result == nil {
							return
						}
						raw, _ := json.Marshal(result["result"])
						text += string(raw)
					}
				}
				send(map[string]any{"jsonrpc": "2.0", "method": "session/update", "params": map[string]any{"sessionId": "test-session", "update": map[string]any{"sessionUpdate": "agent_message_chunk", "content": map[string]string{"type": "text", "text": text}}}})
				send(map[string]any{"jsonrpc": "2.0", "id": id, "result": map[string]string{"stopReason": "end_turn"}})
			}()
		}
	}
}

func TestNativeLoginSurfacesOfficialDeviceChallengeWithoutReadingCredentials(t *testing.T) {
	a := mockAdapter(t)
	var codes, urls []string
	err := Login(context.Background(), LoginOptions{CLIPath: a.cfg.CLIPath, Provider: "google", OnChallenge: func(code, url string) {
		if code != "" {
			codes = append(codes, code)
		}
		if url != "" {
			urls = append(urls, url)
		}
	}})
	if err != nil || len(codes) == 0 || codes[0] != "TEST-CODE" || len(urls) == 0 || urls[len(urls)-1] != "https://app.kiro.dev/device" {
		t.Fatal("native login challenge lost", err, codes, urls)
	}
}

func mockAdapter(t *testing.T) *Adapter {
	t.Helper()
	exe, e := os.Executable()
	if e != nil {
		t.Fatal(e)
	}
	fixtures, e := filepath.Abs(filepath.Join("..", "..", "..", "fixtures"))
	if e != nil {
		t.Fatal(e)
	}
	t.Setenv("AGENT2API_KIRO_TEST_HELPER", "1")
	t.Setenv("AGENT2API_KIRO_FIXTURES", fixtures)
	a, e := New(Config{CLIPath: exe, WorkRoot: t.TempDir(), RequestTimeout: 10 * time.Second, StreamTotalTimeout: 30 * time.Second})
	if e != nil {
		t.Fatal(e)
	}
	return a
}
func request() llm.RequestMessages {
	return llm.RequestMessages{Model: "qwen3-coder-next", Messages: []llm.Message{{Role: llm.RoleUser, Content: []llm.Content{{Type: llm.ContentText, Text: "Return a test answer."}}}}}
}
func collect(t *testing.T, s llm.ResponseStream) []llm.ResponseEvent {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	var out []llm.ResponseEvent
	for {
		v, e := s.Recv(ctx)
		if e == io.EOF {
			break
		}
		if e != nil {
			t.Fatal(e)
		}
		out = append(out, v)
		if v.Type == llm.EventError {
			t.Fatalf("unexpected event: %+v", v.Error)
		}
	}
	return out
}

func TestVerifyFreeRejectsUnverifiedBilling(t *testing.T) {
	raw := fixture(t, "kiro-free-usage.json")
	var valid usageEnvelope
	json.Unmarshal(raw, &valid)
	if n, e := verifyFree(valid.Data); e != nil || n != 48.75 {
		t.Fatalf("%v %v", n, e)
	}
	for _, name := range []string{"paid", "trial", "overages", "enterprise", "capable", "missing", "bonus", "addon", "exhausted", "invalid"} {
		t.Run(name, func(t *testing.T) {
			var v usageEnvelope
			json.Unmarshal(raw, &v)
			u := &v.Data
			yes := true
			zero := -1.0
			switch name {
			case "paid":
				p := "KIRO PRO"
				u.Plan = &p
			case "trial":
				p := "KIRO FREE TRIAL"
				u.Plan = &p
			case "overages":
				u.Overages = &yes
			case "enterprise":
				u.Enterprise = &yes
			case "capable":
				u.OverageCapable = &yes
			case "missing":
				u.AddOns = nil
			case "bonus":
				u.Bonus = []json.RawMessage{json.RawMessage(`{}`)}
			case "addon":
				u.AddOns = []json.RawMessage{json.RawMessage(`{}`)}
			case "exhausted":
				u.Breakdowns[0].Used = u.Breakdowns[0].Limit
			case "invalid":
				u.Breakdowns[0].Used = &zero
			}
			if _, e := verifyFree(*u); e == nil {
				t.Fatal("unsafe billing accepted")
			}
		})
	}
}

func TestCatalogueKeepsRealRatesAndUnknownOutput(t *testing.T) {
	m, e := decodeModels(fixture(t, "kiro-model-catalog.json"), 50, time.Now())
	if e != nil || len(m) != 5 {
		t.Fatal(e, len(m))
	}
	for _, v := range m {
		if v.MaxOutputTokens != 0 || v.SupportsThinking || v.Pricing.Status != "credits" || *v.Pricing.CreditMultiplier <= 0 {
			t.Fatalf("invented metadata: %+v", v)
		}
	}
}

func TestOfficialACPTextAndNoTokenFabrication(t *testing.T) {
	a := mockAdapter(t)
	s, e := a.Stream(context.Background(), request())
	if e != nil {
		t.Fatal(e)
	}
	defer s.(io.Closer).Close()
	events := collect(t, s)
	last := events[len(events)-1]
	if last.Type != llm.EventDone || last.StopReason != llm.StopReasonStop || last.Usage != nil {
		t.Fatalf("bad final event: %+v", last)
	}
}

func TestMCPToolHandoffResumesOneNativePrompt(t *testing.T) {
	a := mockAdapter(t)
	audit := filepath.Join(t.TempDir(), "audit.txt")
	t.Setenv("AGENT2API_KIRO_AUDIT", audit)
	r := request()
	r.SessionKey = "h:first-request"
	r.Messages[0].Content[0].CacheControl = &llm.CacheControl{Type: "ephemeral"}
	r.Tools = []llm.ToolDefinition{{Name: "get_value", Description: "Read the value for the requested tag", Parameters: map[string]any{"type": "object", "properties": map[string]any{"tag": map[string]string{"type": "string"}}}}}
	ctx, cancel := context.WithCancel(context.Background())
	s, e := a.Stream(ctx, r)
	if e != nil {
		t.Fatal(e)
	}
	events := collect(t, s)
	s.(io.Closer).Close()
	cancel()
	var call *llm.ToolCall
	for _, v := range events {
		if v.Type == llm.EventToolCallEnd {
			call = &llm.ToolCall{ID: v.ToolCallID, Name: v.ToolName, Arguments: v.ToolArguments}
		}
	}
	if call == nil {
		t.Fatal("no native MCP tool call")
	}
	if events[len(events)-1].StopReason != llm.StopReasonToolCalls {
		t.Fatal("tool handoff not completed")
	}
	r.Messages = append(r.Messages, llm.Message{Role: llm.RoleAssistant, Content: []llm.Content{{Type: llm.ContentToolCall, ToolCall: call}}}, llm.Message{Role: llm.RoleTool, Content: []llm.Content{{Type: llm.ContentToolResult, ToolResult: &llm.ToolResult{ToolCallID: call.ID, Content: "value=57", IsError: true}}}})
	r.SessionKey = "h:next-request"
	r.Messages[0].Content[0].CacheControl = nil
	changed := r
	changed.SystemPrompt = "different caller"
	if _, e = a.Stream(context.Background(), changed); e == nil {
		t.Fatal("cross-request resume accepted")
	}
	s, e = a.Stream(context.Background(), r)
	if e != nil {
		t.Fatal(e)
	}
	defer s.(io.Closer).Close()
	events = collect(t, s)
	var text strings.Builder
	for _, v := range events {
		if v.Type == llm.EventTextDelta {
			text.WriteString(v.Delta)
		}
	}
	if !strings.Contains(text.String(), "value=57") || !strings.Contains(text.String(), `"isError":true`) {
		t.Fatal("tool result lost", text.String())
	}
	logged, _ := os.ReadFile(audit)
	if string(logged) != "prompt\n" {
		t.Fatal("tool continuation made another billed prompt", string(logged))
	}
}

func TestPaidAccountNeverPrompts(t *testing.T) {
	a := mockAdapter(t)
	t.Setenv("AGENT2API_KIRO_PLAN", "KIRO PRO")
	audit := filepath.Join(t.TempDir(), "audit.txt")
	t.Setenv("AGENT2API_KIRO_AUDIT", audit)
	if _, e := a.Stream(context.Background(), request()); e == nil {
		t.Fatal("paid plan accepted")
	}
	if _, e := os.Stat(audit); !os.IsNotExist(e) {
		t.Fatal("paid account reached generation")
	}
}

func TestParallelMCPRequestsAreDeliveredWithoutLosingAQueuedCall(t *testing.T) {
	a := mockAdapter(t)
	t.Setenv("AGENT2API_KIRO_PARALLEL", "1")
	r := request()
	r.Tools = []llm.ToolDefinition{{Name: "get_value", Parameters: map[string]any{"type": "object", "properties": map[string]any{}}}}
	seen := map[string]bool{}
	finished := false
	for turn := 0; turn < 4; turn++ {
		s, e := a.Stream(context.Background(), r)
		if e != nil {
			t.Fatal(e)
		}
		events := collect(t, s)
		s.(io.Closer).Close()
		var calls []llm.Content
		for _, v := range events {
			if v.Type == llm.EventToolCallEnd {
				if seen[v.ToolCallID] {
					t.Fatal("same pending call delivered twice")
				}
				seen[v.ToolCallID] = true
				calls = append(calls, llm.Content{Type: llm.ContentToolCall, ToolCall: &llm.ToolCall{ID: v.ToolCallID, Name: v.ToolName, Arguments: v.ToolArguments}})
			}
		}
		if events[len(events)-1].StopReason == llm.StopReasonStop {
			finished = true
			break
		}
		r.Messages = append(r.Messages, llm.Message{Role: llm.RoleAssistant, Content: calls})
		for _, b := range calls {
			r.Messages = append(r.Messages, llm.Message{Role: llm.RoleTool, Content: []llm.Content{{Type: llm.ContentToolResult, ToolResult: &llm.ToolResult{ToolCallID: b.ToolCall.ID, Content: "value=57"}}}})
		}
	}
	if !finished || len(seen) != 2 {
		t.Fatalf("parallel calls lost: completed=%v calls=%d", finished, len(seen))
	}
}

func TestCacheHintsAndInferredAffinityCanMoveButExplicitIdentityRemainsBound(t *testing.T) {
	r := request()
	r.SessionKey = "h:first-wire-shape"
	r.Messages[0].Content[0].CacheControl = &llm.CacheControl{Type: "ephemeral"}
	r.Tools = []llm.ToolDefinition{{Name: "Read", CacheControl: &llm.CacheControl{Type: "ephemeral"}}}
	bind, prefix := requestBinding(r), messageHash(r.Messages)
	r.SessionKey = "h:next-wire-shape"
	r.Messages[0].Content[0].CacheControl = nil
	r.Tools[0].CacheControl = nil
	if requestBinding(r) != bind || messageHash(r.Messages) != prefix {
		t.Fatal("cache hints changed continuation identity")
	}
	r.SessionKey = "s:explicit-caller"
	if requestBinding(r) == bind {
		t.Fatal("explicit session identity was ignored")
	}
	r.Messages[0].Content[0].Text = "different conversation"
	if messageHash(r.Messages) == prefix {
		t.Fatal("different conversation was accepted")
	}
	withResult := []llm.Message{{Role: llm.RoleTool, Content: []llm.Content{{Type: llm.ContentToolResult, ToolResult: &llm.ToolResult{ToolCallID: "old", Content: "text", Blocks: []llm.Content{{Type: llm.ContentText, Text: "text", CacheControl: &llm.CacheControl{Type: "ephemeral"}}}}}}}}
	canonical := messageHash(withResult)
	if withResult[0].Content[0].ToolResult.Blocks[0].CacheControl == nil {
		t.Fatal("history canonicalization mutated caller data")
	}
	withResult[0].Content[0].ToolResult.Blocks[0].CacheControl = nil
	if messageHash(withResult) != canonical {
		t.Fatal("nested tool-result cache hint changed identity")
	}
}

func TestCancellationCleansOwnedWorkspaceAndPending(t *testing.T) {
	a := mockAdapter(t)
	t.Setenv("AGENT2API_KIRO_HANG", "1")
	s, e := a.Stream(context.Background(), request())
	if e != nil {
		t.Fatal(e)
	}
	s.(io.Closer).Close()
	entries, e := os.ReadDir(a.cfg.WorkRoot)
	if e != nil || len(entries) != 0 {
		t.Fatal("owned workspace leaked", e, len(entries))
	}
	if len(a.pending) != 0 {
		t.Fatal("pending tool leaked")
	}
}

func TestUnsafeModelsAndHostToolsRejected(t *testing.T) {
	a := mockAdapter(t)
	r := request()
	r.Model = "auto"
	if _, e := a.Stream(context.Background(), r); e == nil {
		t.Fatal("Auto passed")
	}
	r = request()
	r.JSONMode = true
	if _, e := a.Stream(context.Background(), r); e == nil {
		t.Fatal("JSON mode passed")
	}
	work, e := prepareWorkspace(a.cfg.WorkRoot)
	if e != nil {
		t.Fatal(e)
	}
	defer removeWorkspace(a.cfg.WorkRoot, work)
	raw, e := os.ReadFile(filepath.Join(work, ".kiro", "agents", "agent2api-proxy.json"))
	if e != nil {
		t.Fatal(e)
	}
	var profile struct {
		Tools, Allowed                []string
		Resources                     []any
		IncludeMcpJson, IncludePowers bool
	}
	json.Unmarshal(raw, &profile)
	if len(profile.Tools) != 1 || profile.Tools[0] != "@agent2api" || len(profile.Resources) != 0 || profile.IncludeMcpJson || profile.IncludePowers {
		t.Fatal("native host resources enabled")
	}
}

func TestAuthorizationURLAllowlistAndAPIKeyIsolation(t *testing.T) {
	for _, u := range []string{"http://app.kiro.dev/device", "https://evil.example/auth", "https://kiro.dev.evil.example/auth", "https://user:pass@app.kiro.dev/"} {
		if safeAuthorizationURL(u) != "" {
			t.Fatal("unsafe auth URL", u)
		}
	}
	if safeAuthorizationURL("https://app.kiro.dev/device") == "" {
		t.Fatal("official URL rejected")
	}
	t.Setenv("KIRO_API_KEY", "test-paid-key")
	cmd := cliCommand(context.Background(), "kiro-cli", "", "acp")
	for _, v := range cmd.Env {
		if strings.HasPrefix(v, "KIRO_API_KEY=") {
			t.Fatal("paid API key inherited")
		}
	}
}
