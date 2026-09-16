package chat

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/576469377/Agent2API/internal/api/common"
	"github.com/576469377/Agent2API/internal/llm"
)

// encodeAll 依次编码事件并拼接 SSE 文本，便于整体断言。
func encodeAll(t *testing.T, events []llm.ResponseEvent) (frames []common.SSEEvent, raw string) {
	t.Helper()
	p := Protocol{}
	enc := p.NewStreamEncoder("test-model", true)
	for _, ev := range events {
		out, err := enc.Encode(ev)
		if err != nil {
			t.Fatalf("编码 %s 失败: %v", ev.Type, err)
		}
		for _, f := range out {
			dst := p.AppendSSE(nil, f.Name, f.Data)
			raw += string(dst)
			frames = append(frames, f)
		}
	}
	return frames, raw
}

// TestStreamErrorEmitsErrorFrame 是「流中断静默截断」的回归测试。
//
// 曾有 bug：EventError 编码返回 0 帧，客户端已收到 HTTP 200，
// 上游中途失败时只能拿到半截内容——没有 finish_reason、没有 [DONE]、
// 没有错误帧，无法区分「正常结束」与「上游挂了」。
// 官方 SDK（openai-python _streaming.py）把「data 帧含 error 字段」视为
// 流内错误并抛 APIError，因此这里必须发出该形状。
func TestStreamErrorEmitsErrorFrame(t *testing.T) {
	frames, _ := encodeAll(t, []llm.ResponseEvent{
		{Type: llm.EventStart, Model: "m"},
		{Type: llm.EventTextStart, ContentIndex: 0},
		{Type: llm.EventTextDelta, ContentIndex: 0, Delta: "半截"},
		{Type: llm.EventError, Error: &llm.Failure{Code: "upstream_read_failed", Message: "读取上游流失败"}},
	})

	if len(frames) == 0 {
		t.Fatal("EventError 必须产生至少一帧，否则客户端无法感知失败")
	}
	var payload struct {
		Error *struct {
			Message string `json:"message"`
			Type    string `json:"type"`
			Code    string `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal(frames[len(frames)-1].Data, &payload); err != nil {
		t.Fatalf("错误帧不是合法 JSON: %v", err)
	}
	if payload.Error == nil {
		t.Fatalf("错误帧必须含 error 字段: %s", frames[len(frames)-1].Data)
	}
	if payload.Error.Message != "读取上游流失败" {
		t.Fatalf("错误消息=%q", payload.Error.Message)
	}

	// 错误后不得再补发 finish_reason / [DONE]——那等于谎报正常收尾。
	raw := strings.Join(nil, "")
	for _, f := range frames {
		raw += string(f.Data)
	}
	if strings.Contains(raw, "finish_reason") || strings.Contains(raw, "[DONE]") {
		t.Fatal("错误帧之后不得出现 finish_reason 或 [DONE]")
	}
}

// TestStreamErrorWithNilFailure 保证空错误对象也能编码（防御性）。
func TestStreamErrorWithNilFailure(t *testing.T) {
	frames, _ := encodeAll(t, []llm.ResponseEvent{{Type: llm.EventError}})
	if len(frames) != 1 {
		t.Fatalf("EventError(nil) 应产生 1 帧, got %d", len(frames))
	}
	if !strings.Contains(string(frames[0].Data), `"error"`) {
		t.Fatalf("应含 error 字段: %s", frames[0].Data)
	}
}

// TestStreamHappyPathUsageInFinalChunk 覆盖正常流的收尾契约：
// usage 只出现在带 finish_reason 的终帧，随后必须紧跟 [DONE]。
func TestStreamHappyPathUsageInFinalChunk(t *testing.T) {
	frames, raw := encodeAll(t, []llm.ResponseEvent{
		{Type: llm.EventStart, Model: "m"},
		{Type: llm.EventTextDelta, ContentIndex: 0, Delta: "你好"},
		{Type: llm.EventDone, StopReason: llm.StopReasonStop, Usage: &llm.Usage{InputTokens: 3, OutputTokens: 5, TotalTokens: 8}},
	})
	last := frames[len(frames)-1]
	if last.Name != "[DONE]" {
		t.Fatalf("最后一帧应为 [DONE], got %q", last.Name)
	}
	// 终帧（[DONE] 前一帧）应同时带 finish_reason 与 usage。
	tail := frames[len(frames)-2]
	var chunk struct {
		Choices []struct {
			FinishReason *string `json:"finish_reason"`
		} `json:"choices"`
		Usage *struct {
			PromptTokens     int `json:"prompt_tokens"`
			CompletionTokens int `json:"completion_tokens"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(tail.Data, &chunk); err != nil {
		t.Fatalf("终帧解析失败: %v", err)
	}
	if len(chunk.Choices) != 1 || chunk.Choices[0].FinishReason == nil {
		t.Fatalf("终帧应带 finish_reason: %s", tail.Data)
	}
	if chunk.Usage == nil || chunk.Usage.CompletionTokens != 5 {
		t.Fatalf("终帧应带 usage: %s", tail.Data)
	}
	if !strings.HasSuffix(strings.TrimSpace(raw), "data: [DONE]") {
		t.Fatalf("流应以 [DONE] 结尾: %q", raw[len(raw)-40:])
	}
}

// TestDecodeRequestSystemExtraction 覆盖 system 提取、role 透传与 Dropped 场景。
func TestDecodeRequestSystemExtraction(t *testing.T) {
	body := `{"model":"m","messages":[
		{"role":"system","content":"be nice"},
		{"role":"user","content":"hi"},
		{"role":"assistant","content":"hello","tool_calls":null}
	],"stream":true}`
	req, err := (Protocol{}).DecodeRequest([]byte(body))
	if err != nil {
		t.Fatalf("DecodeRequest: %v", err)
	}
	if req.SystemPrompt != "be nice" {
		t.Fatalf("SystemPrompt=%q", req.SystemPrompt)
	}
	if len(req.Messages) != 2 {
		t.Fatalf("system 之外的消息应保留 2 条, got %d", len(req.Messages))
	}
	if !req.Stream {
		t.Fatal("stream=true 应被透传")
	}
}

func TestDecodeRequestMissingModel(t *testing.T) {
	_, err := (Protocol{}).DecodeRequest([]byte(`{"messages":[]}`))
	f, ok := err.(*llm.Failure)
	if !ok || f.Code != "missing_model" {
		t.Fatalf("缺 model 应返回 missing_model, got %v", err)
	}
}

// TestDecodeRequestToolChoice 覆盖 object/string 两种形式的归一化。
func TestDecodeRequestToolChoice(t *testing.T) {
	p := Protocol{}
	req, err := p.DecodeRequest([]byte(`{"model":"m","tool_choice":"auto","messages":[]}`))
	if err != nil || req.ToolChoice == nil || req.ToolChoice.Mode != llm.ToolChoiceAuto {
		t.Fatalf("string auto: %v %+v", err, req.ToolChoice)
	}
	req, err = p.DecodeRequest([]byte(`{"model":"m","tool_choice":{"type":"function","function":{"name":"f"}},"messages":[]}`))
	if err != nil || req.ToolChoice == nil || req.ToolChoice.Mode != llm.ToolChoiceFunction || req.ToolChoice.FunctionName != "f" {
		t.Fatalf("object function: %v %+v", err, req.ToolChoice)
	}
}

// TestEncodeFinalStops 覆盖非流式响应的 stop_reason 与工具调用聚合。
func TestEncodeFinalStops(t *testing.T) {
	msg := &llm.AssistantMessage{
		StopReason: llm.StopReasonToolCalls,
		Content: []llm.Content{
			{Type: llm.ContentText, Text: "让我查一下"},
			{Type: llm.ContentToolCall, ToolCall: &llm.ToolCall{ID: "c1", Name: "lookup", Arguments: `{"q":"x"}`}},
		},
		Usage: llm.Usage{InputTokens: 2, OutputTokens: 3},
	}
	out, err := (Protocol{}).EncodeFinal(msg, "m")
	if err != nil {
		t.Fatalf("EncodeFinal: %v", err)
	}
	var resp struct {
		Choices []struct {
			FinishReason string `json:"finish_reason"`
			Message      struct {
				Content   string `json:"content"`
				ToolCalls []struct {
					ID       string `json:"id"`
					Function struct {
						Name      string `json:"name"`
						Arguments string `json:"arguments"`
					} `json:"function"`
				} `json:"tool_calls"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(out, &resp); err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	if resp.Choices[0].FinishReason != "tool_calls" {
		t.Fatalf("finish_reason=%q, want tool_calls", resp.Choices[0].FinishReason)
	}
	if len(resp.Choices[0].Message.ToolCalls) != 1 || resp.Choices[0].Message.ToolCalls[0].Function.Name != "lookup" {
		t.Fatalf("工具调用未正确聚合: %s", out)
	}
	if resp.Choices[0].Message.Content != "让我查一下" {
		t.Fatalf("文本丢失: %s", out)
	}
}
