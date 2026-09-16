package responses

import (
	"github.com/576469377/Agent2API/internal/llm"
	"encoding/json"
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
