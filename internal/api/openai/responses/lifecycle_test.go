package responses

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/576469377/Agent2API/internal/llm"
)

func BenchmarkLongTextStream(b *testing.B) {
	delta := strings.Repeat("x", 128)
	b.ReportAllocs()
	b.SetBytes(128 * 1024)
	for i := 0; i < b.N; i++ {
		enc := Protocol{}.NewStreamEncoder("model", true)
		enc.Encode(llm.ResponseEvent{Type: llm.EventStart})
		enc.Encode(llm.ResponseEvent{Type: llm.EventTextStart})
		for j := 0; j < 1024; j++ {
			enc.Encode(llm.ResponseEvent{Type: llm.EventTextDelta, Delta: delta})
		}
		enc.Encode(llm.ResponseEvent{Type: llm.EventTextEnd})
		enc.Encode(llm.ResponseEvent{Type: llm.EventDone})
	}
}

// The SDK accumulates text into an existing content part, and reads sequence
// numbers on deltas and completion. Missing either breaks responses.stream().
func TestStreamLifecycleInitializesContentBeforeDeltas(t *testing.T) {
	enc := Protocol{}.NewStreamEncoder("model", true)
	events := []llm.ResponseEvent{
		{Type: llm.EventStart},
		{Type: llm.EventThinkingStart, ContentIndex: 0},
		{Type: llm.EventThinkingDelta, ContentIndex: 0, Delta: "consider"},
		{Type: llm.EventThinkingEnd, ContentIndex: 0},
		{Type: llm.EventTextStart, ContentIndex: 1},
		{Type: llm.EventTextDelta, ContentIndex: 1, Delta: "hello"},
		{Type: llm.EventTextEnd, ContentIndex: 1},
		{Type: llm.EventToolCallStart, ContentIndex: 2, ToolCallID: "c1", ToolName: "lookup"},
		{Type: llm.EventToolCallDelta, ContentIndex: 2, Delta: "{}"},
		{Type: llm.EventToolCallEnd, ContentIndex: 2, ToolCallID: "c1", ToolName: "lookup", ToolArguments: "{}"},
		{Type: llm.EventDone, StopReason: llm.StopReasonToolCalls},
	}
	var names []string
	items := map[int]map[string]any{}
	for _, event := range events {
		frames, err := enc.Encode(event)
		if err != nil {
			t.Fatal(err)
		}
		for _, frame := range frames {
			var data map[string]any
			if err := json.Unmarshal(frame.Data, &data); err != nil {
				t.Fatal(err)
			}
			sequence, ok := data["sequence_number"].(float64)
			if !ok || int(sequence) != len(names) {
				t.Fatalf("invalid sequence in %s: %v", frame.Name, data)
			}
			names = append(names, frame.Name)
			index, _ := data["output_index"].(float64)
			switch frame.Name {
			case "response.output_item.added":
				items[int(index)] = data["item"].(map[string]any)
			case "response.content_part.added":
				item := items[int(index)]
				item["content"] = append(item["content"].([]any), data["part"])
			case "response.output_text.delta":
				content := items[int(index)]["content"].([]any)
				if len(content) != 1 {
					t.Fatal("text delta arrived before its content part")
				}
				part := content[0].(map[string]any)
				part["text"] = part["text"].(string) + data["delta"].(string)
				if _, ok := data["logprobs"].([]any); !ok {
					t.Fatal("delta is missing logprobs")
				}
			case "response.output_text.done":
				content := items[int(index)]["content"].([]any)
				if content[0].(map[string]any)["text"] != data["text"] {
					t.Fatal("final text differs from accumulated deltas")
				}
			}
		}
	}
	want := []string{
		"response.created",
		"response.output_item.added", "response.reasoning_summary_part.added", "response.reasoning_summary_text.delta", "response.reasoning_summary_text.done", "response.reasoning_summary_part.done", "response.output_item.done",
		"response.output_item.added", "response.content_part.added", "response.output_text.delta", "response.output_text.done", "response.content_part.done", "response.output_item.done",
		"response.output_item.added", "response.function_call_arguments.delta", "response.function_call_arguments.done", "response.output_item.done",
		"response.completed",
	}
	if !reflect.DeepEqual(names, want) {
		t.Fatalf("stream lifecycle = %v; want %v", names, want)
	}
}

func TestResponseStopReasonsMatchForStreamingAndFinal(t *testing.T) {
	for _, tc := range []struct {
		reason         llm.StopReason
		status, detail string
	}{
		{llm.StopReasonStop, "completed", ""},
		{llm.StopReasonToolCalls, "completed", ""},
		{llm.StopReasonLength, "incomplete", "max_output_tokens"},
		{llm.StopReasonContentFilter, "incomplete", "content_filter"},
		{llm.StopReasonError, "failed", ""},
	} {
		t.Run(string(tc.reason), func(t *testing.T) {
			p := Protocol{}
			frames, err := p.NewStreamEncoder("model", true).Encode(llm.ResponseEvent{Type: llm.EventDone, StopReason: tc.reason})
			if err != nil {
				t.Fatal(err)
			}
			if frames[0].Name != "response."+tc.status {
				t.Fatalf("wrong terminal event: %s", frames[0].Name)
			}
			var streamed struct {
				Response map[string]any `json:"response"`
			}
			if err := json.Unmarshal(frames[0].Data, &streamed); err != nil {
				t.Fatal(err)
			}
			encoded, err := p.EncodeFinal(&llm.AssistantMessage{StopReason: tc.reason}, "model")
			if err != nil {
				t.Fatal(err)
			}
			var final map[string]any
			if err := json.Unmarshal(encoded, &final); err != nil {
				t.Fatal(err)
			}
			for _, response := range []map[string]any{streamed.Response, final} {
				if response["status"] != tc.status {
					t.Fatalf("status=%v; want %s", response["status"], tc.status)
				}
				if tc.detail != "" && response["incomplete_details"].(map[string]any)["reason"] != tc.detail {
					t.Fatalf("lost truncation reason: %v", response)
				}
				if tc.status == "failed" && response["error"] == nil {
					t.Fatal("failed response has no error")
				}
			}
		})
	}
}

func TestFinalResponsePreservesBlockOrderAndThinkingSummary(t *testing.T) {
	content := []llm.Content{
		{Type: llm.ContentThinking, Thinking: "consider"},
		{Type: llm.ContentText, Text: "before"},
		{Type: llm.ContentToolCall, ToolCall: &llm.ToolCall{ID: "c1", Name: "lookup", Arguments: "{}"}},
		{Type: llm.ContentText, Text: "after"},
	}
	data, err := (Protocol{}).EncodeFinal(&llm.AssistantMessage{Content: content}, "model")
	if err != nil {
		t.Fatal(err)
	}
	var final struct {
		Output []map[string]any `json:"output"`
		Text   string           `json:"output_text"`
	}
	if err := json.Unmarshal(data, &final); err != nil {
		t.Fatal(err)
	}
	if len(final.Output) != 4 {
		t.Fatalf("output lost its block boundaries: %s", data)
	}
	for i, want := range []string{"reasoning", "message", "function_call", "message"} {
		if final.Output[i]["type"] != want {
			t.Fatalf("output[%d] = %v; want %s", i, final.Output[i], want)
		}
	}
	summary := final.Output[0]["summary"].([]any)
	if len(summary) != 1 || summary[0].(map[string]any)["text"] != "consider" {
		t.Fatal("thinking summary was lost")
	}
	if final.Text != "beforeafter" {
		t.Fatal("combined output_text was lost")
	}
	for i, want := range map[int]string{1: "before", 3: "after"} {
		if final.Output[i]["content"].([]any)[0].(map[string]any)["text"] != want {
			t.Fatal("text blocks were reordered")
		}
	}
}
