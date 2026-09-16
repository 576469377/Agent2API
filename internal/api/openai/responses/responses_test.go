package responses

import (
	"encoding/json"
	"github.com/576469377/Agent2API/internal/llm"
	"testing"
)

func TestCompletedStreamRetainsText(t *testing.T) {
	e := Protocol{}.NewStreamEncoder("test", false)
	events := []llm.ResponseEvent{
		{Type: llm.EventStart}, {Type: llm.EventTextStart, ContentIndex: 0},
		{Type: llm.EventTextDelta, ContentIndex: 0, Delta: "O"},
		{Type: llm.EventTextDelta, ContentIndex: 0, Delta: "K"},
		{Type: llm.EventTextEnd, ContentIndex: 0}, {Type: llm.EventDone},
	}
	for _, ev := range events {
		frames, err := e.Encode(ev)
		if err != nil {
			t.Fatal(err)
		}
		for _, f := range frames {
			var d map[string]any
			if err := json.Unmarshal(f.Data, &d); err != nil {
				t.Fatal(err)
			}
			if f.Name == "response.output_item.done" {
				item := d["item"].(map[string]any)
				content := item["content"].([]any)
				if content[0].(map[string]any)["text"] != "OK" {
					t.Fatal("completed item lost text")
				}
			}
			if f.Name == "response.completed" {
				r := d["response"].(map[string]any)
				if r["output_text"] != "OK" || len(r["output"].([]any)) != 1 {
					t.Fatal("completed response lost output")
				}
			}
		}
	}
}

func TestImageToolOutput(t *testing.T) {
	req, err := (Protocol{}).DecodeRequest([]byte(`{"model":"test","input":[{"type":"function_call_output","call_id":"t1","output":[{"type":"input_text","text":"image"},{"type":"input_image","image_url":"data:image/png;base64,AAAA"}]}]}`))
	if err != nil {
		t.Fatal(err)
	}
	tr := req.Messages[0].Content[0].ToolResult
	if tr.Content != "image" || len(tr.Blocks) != 2 || tr.Blocks[1].ImageURL != "data:image/png;base64,AAAA" {
		t.Fatalf("image output lost: %+v", tr)
	}
}
func TestStringToolOutput(t *testing.T) {
	req, err := (Protocol{}).DecodeRequest([]byte(`{"model":"test","input":[{"type":"function_call_output","call_id":"t1","output":"OK"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	if req.Messages[0].Content[0].ToolResult.Content != "OK" {
		t.Fatal("text output lost")
	}
}

// TestStreamIncompleteToolCallKeptInOutput 是「output 丢项」的回归测试。
//
// 曾有 bug：responseShell 用 `for i := 0; i < len(e.itemID); i++` 遍历一个
// 按内容下标索引的稀疏 map，并用 completed 的成员判定决定是否输出——
// 当块「已 start 但未 end」（例如工具调用中途上游断流）时，该条目静默
// 从 output[] 消失，客户端看到的响应缺一块。
// 修复后：按 itemID 实际 key 遍历，未完成条目合成 incomplete 终态。
func TestStreamIncompleteToolCallKeptInOutput(t *testing.T) {
	p := Protocol{}
	enc := p.NewStreamEncoder("test-model", true)

	events := []llm.ResponseEvent{
		{Type: llm.EventStart, Model: "test-model"},
		{Type: llm.EventTextStart, ContentIndex: 0},
		{Type: llm.EventTextDelta, ContentIndex: 0, Delta: "让我查一下"},
		// 工具调用开始并收到部分参数，但流在其 End 之前中断。
		{Type: llm.EventToolCallStart, ContentIndex: 1, ToolCallID: "call_1", ToolName: "lookup"},
		{Type: llm.EventToolCallDelta, ContentIndex: 1, Delta: `{"q":"x`},
		// 刻意不发 ToolCallEnd / TextEnd，直接收尾。
		{Type: llm.EventDone, StopReason: llm.StopReasonError,
			Usage: &llm.Usage{InputTokens: 1, OutputTokens: 2}},
	}

	var completedItem map[string]any
	for _, ev := range events {
		out, err := enc.Encode(ev)
		if err != nil {
			t.Fatalf("编码 %s 失败: %v", ev.Type, err)
		}
		for _, sse := range out {
			if sse.Name != "response.completed" {
				continue
			}
			var frame struct {
				Response struct {
					Output []map[string]any `json:"output"`
				} `json:"response"`
			}
			if err := json.Unmarshal(sse.Data, &frame); err != nil {
				t.Fatalf("response.completed 解析失败: %v", err)
			}
			completedItem = map[string]any{}
			for _, item := range frame.Response.Output {
				completedItem[item["type"].(string)] = item
			}
		}
	}
	if completedItem == nil {
		t.Fatal("没有 response.completed 帧")
	}
	fcAny, ok := completedItem["function_call"]
	if !ok {
		t.Fatalf("中断的工具调用必须出现在 output[] 中（旧实现会静默丢掉）: %v", completedItem)
	}
	fc := fcAny.(map[string]any)
	if fc["status"] != "incomplete" {
		t.Fatalf("未完成条目的 status=%v, want incomplete", fc["status"])
	}
	if fc["name"] != "lookup" {
		t.Fatalf("工具名丢失: %v", fc)
	}
	if fc["arguments"] != `{"q":"x` {
		t.Fatalf("已收到的参数分片丢失: %v", fc["arguments"])
	}
	msgAny, ok := completedItem["message"]
	if !ok {
		t.Fatalf("中断的文本块也应在 output[] 中: %v", completedItem)
	}
	if msgAny.(map[string]any)["status"] != "incomplete" {
		t.Fatalf("中断的文本块也应是 incomplete: %v", completedItem)
	}
}
