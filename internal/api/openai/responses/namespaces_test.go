package responses

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/576469377/Agent2API/internal/llm"
)

func namespaceFixture(t *testing.T) []byte {
	t.Helper()
	body, err := os.ReadFile(filepath.Join("..", "..", "..", "..", "fixtures", "codex-namespaced-tools.json"))
	if err != nil {
		t.Fatal(err)
	}
	return body
}

func TestCodexNamespaceFixture(t *testing.T) {
	body := namespaceFixture(t)
	p, err := Bind(body)
	if err != nil {
		t.Fatal(err)
	}
	r, err := p.DecodeRequest(body)
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Tools) != 2 || r.Tools[0].Name != "exec_command" {
		t.Fatalf("普通工具与分组工具未正确展开: %+v", r.Tools)
	}
	tool := r.Tools[1]
	if tool.Name == "mcp__acceptance" || len(tool.Name) > 64 || tool.Parameters["type"] != "object" || tool.Parameters["additionalProperties"] != false {
		t.Fatalf("命名空间被误当成可执行函数或参数丢失: %+v", tool)
	}
	if !strings.Contains(tool.Description, "mcp__acceptance") || !strings.Contains(tool.Description, "disposable") {
		t.Fatal("模型缺少实际工具描述")
	}
	for _, stream := range []bool{false, true} {
		var items []map[string]any
		if !stream {
			encoded, err := p.EncodeFinal(&llm.AssistantMessage{Content: []llm.Content{{Type: llm.ContentToolCall, ToolCall: &llm.ToolCall{ID: "call_fixture", Name: tool.Name, Arguments: "{}"}}}}, "test-model")
			if err != nil {
				t.Fatal(err)
			}
			var response struct {
				Output []map[string]any `json:"output"`
			}
			json.Unmarshal(encoded, &response)
			items = response.Output
		} else {
			enc := p.NewStreamEncoder("test-model", false)
			for _, ev := range []llm.ResponseEvent{
				{Type: llm.EventStart},
				{Type: llm.EventToolCallStart, ToolCallID: "call_fixture", ToolName: tool.Name},
				{Type: llm.EventToolCallDelta, Delta: "{}"},
				{Type: llm.EventToolCallEnd, ToolCallID: "call_fixture", ToolName: tool.Name, ToolArguments: "{}", ToolArgumentsDone: true},
				{Type: llm.EventDone, StopReason: llm.StopReasonToolCalls},
			} {
				frames, err := enc.Encode(ev)
				if err != nil {
					t.Fatal(err)
				}
				for _, frame := range frames {
					var data struct {
						Item     map[string]any `json:"item"`
						Response struct {
							Output []map[string]any `json:"output"`
						} `json:"response"`
					}
					json.Unmarshal(frame.Data, &data)
					if data.Item != nil {
						items = append(items, data.Item)
					}
					items = append(items, data.Response.Output...)
				}
			}
		}
		if len(items) == 0 {
			t.Fatal("工具调用丢失")
		}
		for _, item := range items {
			if item["name"] != "read_acceptance_file" || item["namespace"] != "mcp__acceptance" || item["call_id"] != "call_fixture" {
				t.Fatalf("流式=%v 工具输出无法由 Codex 路由: %+v", stream, item)
			}
		}
	}
}

func TestNamespaceHistoryChoiceAndOrder(t *testing.T) {
	body := []byte(`{"model":"m","tools":[{"type":"namespace","name":"left","tools":[{"type":"function","name":"lookup","parameters":{"type":"object"}}]},{"type":"namespace","name":"right","tools":[{"type":"function","name":"lookup","parameters":{"type":"object"}}]}],"tool_choice":{"type":"function","namespace":"right","name":"lookup"},"input":[{"type":"function_call","namespace":"left","name":"lookup","arguments":"{}","call_id":"c1"},{"type":"function_call_output","call_id":"c1","output":"value"}]}`)
	r, err := (Protocol{}).DecodeRequest(body)
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Tools) != 2 || r.Tools[0].Name == r.Tools[1].Name || r.ToolChoice.FunctionName != r.Tools[1].Name {
		t.Fatalf("跨命名空间同名函数或强制选择发生混淆: %+v", r)
	}
	if r.Messages[0].Content[0].ToolCall.Name != r.Tools[0].Name || r.Messages[1].Content[0].ToolResult.ToolCallID != "c1" {
		t.Fatal("工具历史未使用相同的内部名称或调用关联丢失")
	}
	// 顺序调整不改变名称，工具定义移除后仍可正确回放历史。
	for _, input := range []string{
		`{"model":"m","tools":[{"type":"function","name":"other"},{"type":"namespace","name":"left","tools":[{"type":"function","name":"lookup"}]}]}`,
		`{"model":"m","input":[{"type":"function_call","namespace":"left","name":"lookup","call_id":"c1","arguments":"{}"}]}`,
	} {
		p, err := Bind([]byte(input))
		if err != nil {
			t.Fatal(err)
		}
		if p.toolNames[r.Tools[0].Name] != (toolName{Namespace: "left", Name: "lookup"}) {
			t.Fatal("历史映射不稳定")
		}
	}
}

func TestNamespaceIncompleteAndRequestIsolation(t *testing.T) {
	var wg sync.WaitGroup
	for _, namespace := range []string{"left", "right"} {
		wg.Add(1)
		go func(namespace string) {
			defer wg.Done()
			body := []byte(`{"model":"m","tools":[{"type":"namespace","name":"` + namespace + `","tools":[{"type":"function","name":"lookup"}]}]}`)
			p, err := Bind(body)
			if err != nil {
				t.Error(err)
				return
			}
			r, _ := p.DecodeRequest(body)
			e := p.NewStreamEncoder("m", false)
			e.Encode(llm.ResponseEvent{Type: llm.EventToolCallStart, ToolCallID: "c1", ToolName: r.Tools[0].Name})
			e.Encode(llm.ResponseEvent{Type: llm.EventToolCallDelta, Delta: `{"q":`})
			frames, err := e.Encode(llm.ResponseEvent{Type: llm.EventDone, StopReason: llm.StopReasonError})
			if err != nil {
				t.Error(err)
				return
			}
			var out struct {
				Response struct {
					Output []map[string]any `json:"output"`
				} `json:"response"`
			}
			json.Unmarshal(frames[0].Data, &out)
			item := out.Response.Output[0]
			if item["namespace"] != namespace || item["name"] != "lookup" || item["status"] != "incomplete" || item["arguments"] != `{"q":` {
				t.Errorf("并发映射或中断条目损坏: %+v", item)
			}
		}(namespace)
	}
	wg.Wait()
}

func TestInvalidNamespaceTools(t *testing.T) {
	for _, tools := range []string{
		`[{"type":"namespace","tools":[{"type":"function","name":"read"}]}]`,
		`[{"type":"namespace","name":"empty","tools":[]}]`,
		`[{"type":"namespace","name":"ns","tools":[{"type":"function","name":""}]}]`,
		`[{"type":"namespace","name":"ns","tools":[{"type":"function","name":"read"},{"type":"function","name":"read"}]}]`,
		`[{"type":"namespace","name":"ns","tools":[{"type":"namespace","name":"nested","tools":[]}]}]`,
		`[{"type":"namespace","name":"ns","tools":[{"type":"custom","name":"patch"}]}]`,
		`[{"type":"function","name":"` + namespaceToolName("ns", "read") + `"},{"type":"namespace","name":"ns","tools":[{"type":"function","name":"read"}]}]`,
	} {
		_, err := Bind([]byte(`{"model":"m","tools":` + tools + `}`))
		var failure *llm.Failure
		if !errors.As(err, &failure) || !failure.ClientFixable || failure.Code != "invalid_tools" {
			t.Errorf("无效分组未明确拒绝: %v", err)
		}
	}
}

func TestQualifiedInternalAliasOnlyRestoresItsRegisteredNamespace(t *testing.T) {
	p, err := Bind(namespaceFixture(t))
	if err != nil {
		t.Fatal(err)
	}
	alias := namespaceToolName("mcp__acceptance", "read_acceptance_file")
	for _, name := range []string{alias, "mcp__acceptance." + alias} {
		body, err := p.EncodeFinal(&llm.AssistantMessage{Content: []llm.Content{{Type: llm.ContentToolCall, ToolCall: &llm.ToolCall{ID: "call_fixture", Name: name, Arguments: "{}"}}}}, "test-model")
		if err != nil {
			t.Fatal(err)
		}
		var result struct {
			Output []map[string]any `json:"output"`
		}
		if json.Unmarshal(body, &result) != nil || result.Output[0]["name"] != "read_acceptance_file" || result.Output[0]["namespace"] != "mcp__acceptance" {
			t.Fatalf("registered qualified alias not restored: %s", body)
		}
	}
	for _, name := range []string{"foreign." + alias, "mcp__acceptance.read_acceptance_file", "mcp__acceptance.unknown"} {
		item := toolItem(p.toolNames, "fc_fixture", "call_fixture", name, "{}", "completed")
		if item["name"] != name || item["namespace"] != nil {
			t.Fatalf("unregistered tool was guessed: %+v", item)
		}
	}
	var fixture map[string]any
	json.Unmarshal(namespaceFixture(t), &fixture)
	qualified := "mcp__acceptance." + alias
	fixture["tools"] = append(fixture["tools"].([]any), map[string]any{"type": "function", "name": qualified, "parameters": map[string]any{"type": "object"}})
	raw, _ := json.Marshal(fixture)
	p, err = Bind(raw)
	if err != nil {
		t.Fatal(err)
	}
	item := toolItem(p.toolNames, "fc_fixture", "call_fixture", qualified, "{}", "completed")
	if item["name"] != qualified || item["namespace"] != nil {
		t.Fatal("an explicitly registered flat tool was rerouted")
	}
}
