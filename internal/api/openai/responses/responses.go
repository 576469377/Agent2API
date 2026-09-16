// Package responses 实现 OpenAI Responses API 协议的编解码。
package responses

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"time"

	"github.com/576469377/Agent2API/internal/api/common"
	"github.com/576469377/Agent2API/internal/llm"
)

// Protocol 是 Responses 协议实现。
type Protocol struct{}

// Name 返回协议标识。
func (Protocol) Name() string { return "openai-responses" }

// ───────────────────────── 请求解码 ─────────────────────────

type request struct {
	Model           string          `json:"model"`
	Input           json.RawMessage `json:"input"`
	Instructions    string          `json:"instructions"`
	Tools           []rawTool       `json:"tools"`
	ToolChoice      any             `json:"tool_choice"`
	MaxOutputTokens *int            `json:"max_output_tokens"`
	Temperature     *float64        `json:"temperature"`
	TopP            *float64        `json:"top_p"`
	Stream          bool            `json:"stream"`
	Reasoning       *struct {
		Effort  string `json:"effort"`
		Summary string `json:"summary"`
	} `json:"reasoning"`
	ParallelToolCalls *bool `json:"parallel_tool_calls"`
	Text              *struct {
		Format *struct {
			Type string `json:"type"`
		} `json:"format"`
	} `json:"text"`
	PreviousResponseID string `json:"previous_response_id"`
}

type rawTool struct {
	Type        string         `json:"type"`
	Name        string         `json:"name"`
	Description string         `json:"description"`
	Parameters  map[string]any `json:"parameters"`
}

type rawInputMessage struct {
	Role      string          `json:"role"`
	Content   json.RawMessage `json:"content"`
	Type      string          `json:"type"`
	CallID    string          `json:"call_id"`
	Name      string          `json:"name"`
	Arguments string          `json:"arguments"`
	Output    json.RawMessage `json:"output"`
}

// DecodeRequest 把 Responses 请求解码为 IR。
func (Protocol) DecodeRequest(body []byte) (llm.RequestMessages, error) {
	var r request
	if err := json.Unmarshal(body, &r); err != nil {
		return llm.RequestMessages{}, &llm.Failure{
			Code: "invalid_request", Message: "解析请求体失败: " + err.Error(), Cause: err, ClientFixable: true,
		}
	}
	if r.Model == "" {
		return llm.RequestMessages{}, &llm.Failure{Code: "missing_model", Message: "缺少 model 字段", ClientFixable: true}
	}

	out := llm.RequestMessages{
		Model:        r.Model,
		SystemPrompt: r.Instructions,
		Messages:     nil,
		MaxTokens:    r.MaxOutputTokens,
		Temperature:  r.Temperature,
		TopP:         r.TopP,
		Stream:       r.Stream,
		IncludeUsage: true,
	}
	if r.Reasoning != nil && r.Reasoning.Effort != "" {
		out.ReasoningEffort = r.Reasoning.Effort
	}
	if r.Text != nil && r.Text.Format != nil && r.Text.Format.Type == "json_object" {
		out.JSONMode = true
	}

	msgs, err := decodeInput(r.Input)
	if err != nil {
		return llm.RequestMessages{}, err
	}
	out.Messages = msgs

	if len(r.Tools) > 0 {
		out.Tools = make([]llm.ToolDefinition, 0, len(r.Tools))
		for _, t := range r.Tools {
			name := t.Name
			if name == "" {
				continue
			}
			out.Tools = append(out.Tools, llm.ToolDefinition{
				Name: name, Description: t.Description, Parameters: t.Parameters,
			})
		}
	}
	if r.ToolChoice != nil {
		out.ToolChoice = normalizeToolChoice(r.ToolChoice)
	}
	return out, nil
}

// decodeInput 兼容 string 与消息数组两种 input 形态。
func decodeInput(raw json.RawMessage) ([]llm.Message, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	if raw[0] == '"' {
		var s string
		if err := json.Unmarshal(raw, &s); err != nil {
			return nil, &llm.Failure{Code: "invalid_input", Message: "解析 input 失败", Cause: err, ClientFixable: true}
		}
		if s == "" {
			return nil, nil
		}
		return []llm.Message{{Role: llm.RoleUser, Content: []llm.Content{{Type: llm.ContentText, Text: s}}}}, nil
	}

	var items []rawInputMessage
	if err := json.Unmarshal(raw, &items); err != nil {
		return nil, &llm.Failure{Code: "invalid_input", Message: "解析 input 失败: " + err.Error(), Cause: err, ClientFixable: true}
	}
	out := make([]llm.Message, 0, len(items))
	for _, it := range items {
		switch it.Type {
		case "function_call":
			out = append(out, llm.Message{Role: llm.RoleAssistant, Content: []llm.Content{{
				Type:     llm.ContentToolCall,
				ToolCall: &llm.ToolCall{ID: it.CallID, Name: it.Name, Arguments: it.Arguments},
			}}})
			continue
		case "function_call_output":
			blocks, err := decodeContentBlocks(it.Output)
			if err != nil {
				return nil, err
			}
			tr := &llm.ToolResult{ToolCallID: it.CallID, Blocks: blocks}
			for _, block := range blocks {
				if block.Type == llm.ContentText {
					tr.Content += block.Text
				}
			}
			out = append(out, llm.Message{Role: llm.RoleTool, ToolCallID: it.CallID, Content: []llm.Content{{
				Type: llm.ContentToolResult, ToolResult: tr,
			}}})
			continue
		}

		role := llm.RoleUser
		if it.Role == "assistant" {
			role = llm.RoleAssistant
		}
		msg := llm.Message{Role: role}
		blocks, err := decodeContentBlocks(it.Content)
		if err != nil {
			return nil, err
		}
		msg.Content = blocks
		out = append(out, msg)
	}
	return out, nil
}

func decodeContentBlocks(raw json.RawMessage) ([]llm.Content, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	if raw[0] == '"' {
		var s string
		if err := json.Unmarshal(raw, &s); err == nil {
			if s == "" {
				return nil, nil
			}
			return []llm.Content{{Type: llm.ContentText, Text: s}}, nil
		}
	}
	var parts []struct {
		Type     string `json:"type"`
		Text     string `json:"text"`
		ImageURL string `json:"image_url"`
	}
	if err := json.Unmarshal(raw, &parts); err != nil {
		return nil, &llm.Failure{Code: "invalid_content", Message: "解析 content 失败", Cause: err, ClientFixable: true}
	}
	out := make([]llm.Content, 0, len(parts))
	for _, p := range parts {
		switch p.Type {
		case "input_text", "output_text", "text":
			if p.Text != "" {
				out = append(out, llm.Content{Type: llm.ContentText, Text: p.Text})
			}
		case "input_image":
			if p.ImageURL != "" {
				out = append(out, llm.Content{Type: llm.ContentImage, ImageURL: p.ImageURL})
			}
		}
	}
	return out, nil
}

func normalizeToolChoice(v any) *llm.ToolChoice {
	switch t := v.(type) {
	case string:
		switch t {
		case "none":
			return &llm.ToolChoice{Mode: llm.ToolChoiceNone}
		case "required":
			return &llm.ToolChoice{Mode: llm.ToolChoiceRequired}
		default:
			return &llm.ToolChoice{Mode: llm.ToolChoiceAuto}
		}
	case map[string]any:
		name, _ := t["name"].(string)
		if name != "" {
			return &llm.ToolChoice{Mode: llm.ToolChoiceFunction, FunctionName: name}
		}
		if typ, _ := t["type"].(string); typ == "function" {
			return &llm.ToolChoice{Mode: llm.ToolChoiceRequired}
		}
	}
	return nil
}

// ───────────────────────── 流式编码 ─────────────────────────

// StreamEncoder 把 IR 事件编码为 Responses 流式事件。
type StreamEncoder struct {
	id      string
	model   string
	created int64
	// itemID 把 output_index 映射到 output item 的 id。
	itemID    map[int]string
	text      map[int]string
	thinking  map[int]string
	completed map[int]any
}

// NewStreamEncoder 构造流式编码器。
func (Protocol) NewStreamEncoder(model string, includeUsage bool) *StreamEncoder {
	return &StreamEncoder{
		id:        "resp_" + randomHex(24),
		model:     model,
		created:   time.Now().Unix(),
		itemID:    map[int]string{},
		text:      map[int]string{},
		thinking:  map[int]string{},
		completed: map[int]any{},
	}
}

func (e *StreamEncoder) item(idx int, kind string) string {
	if id, ok := e.itemID[idx]; ok {
		return id
	}
	prefix := "msg_"
	switch kind {
	case "reasoning":
		prefix = "rs_"
	case "function_call":
		prefix = "fc_"
	}
	id := prefix + randomHex(24)
	e.itemID[idx] = id
	return id
}

func (e *StreamEncoder) ev(name string, payload map[string]any) ([]common.SSEEvent, error) {
	if name == "response.output_item.done" {
		e.completed[payload["output_index"].(int)] = payload["item"]
	}
	payload["type"] = name
	data, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	return []common.SSEEvent{{Name: name, Data: data}}, nil
}

// Encode 把一个 IR 事件编码为若干 SSE 帧。
func (e *StreamEncoder) Encode(ev llm.ResponseEvent) ([]common.SSEEvent, error) {
	switch ev.Type {
	case llm.EventStart:
		if ev.Model != "" {
			e.model = ev.Model
		}
		return e.ev("response.created", map[string]any{
			"response": e.responseShell("in_progress", nil),
		})

	case llm.EventThinkingStart:
		id := e.item(ev.ContentIndex, "reasoning")
		return e.ev("response.output_item.added", map[string]any{
			"output_index": ev.ContentIndex,
			"item": map[string]any{
				"id": id, "type": "reasoning", "summary": []any{},
			},
		})

	case llm.EventThinkingDelta:
		e.thinking[ev.ContentIndex] += ev.Delta
		id := e.item(ev.ContentIndex, "reasoning")
		return e.ev("response.reasoning_summary_text.delta", map[string]any{
			"item_id": id, "output_index": ev.ContentIndex, "summary_index": 0, "delta": ev.Delta,
		})

	case llm.EventThinkingEnd:
		id := e.item(ev.ContentIndex, "reasoning")
		return e.ev("response.output_item.done", map[string]any{
			"output_index": ev.ContentIndex,
			"item":         map[string]any{"id": id, "type": "reasoning", "summary": []any{map[string]any{"type": "summary_text", "text": e.thinking[ev.ContentIndex]}}},
		})

	case llm.EventTextStart:
		id := e.item(ev.ContentIndex, "message")
		return e.ev("response.output_item.added", map[string]any{
			"output_index": ev.ContentIndex,
			"item": map[string]any{
				"id": id, "type": "message", "role": "assistant", "status": "in_progress", "content": []any{},
			},
		})

	case llm.EventTextDelta:
		e.text[ev.ContentIndex] += ev.Delta
		id := e.item(ev.ContentIndex, "message")
		return e.ev("response.output_text.delta", map[string]any{
			"item_id": id, "output_index": ev.ContentIndex, "content_index": 0, "delta": ev.Delta,
		})

	case llm.EventTextEnd:
		id := e.item(ev.ContentIndex, "message")
		return e.ev("response.output_item.done", map[string]any{
			"output_index": ev.ContentIndex,
			"item": map[string]any{
				"id": id, "type": "message", "role": "assistant", "status": "completed",
				"content": []any{map[string]any{"type": "output_text", "text": e.text[ev.ContentIndex], "annotations": []any{}}},
			},
		})

	case llm.EventToolCallStart:
		id := e.item(ev.ContentIndex, "function_call")
		return e.ev("response.output_item.added", map[string]any{
			"output_index": ev.ContentIndex,
			"item": map[string]any{
				"id": id, "type": "function_call", "call_id": ev.ToolCallID,
				"name": ev.ToolName, "arguments": "", "status": "in_progress",
			},
		})

	case llm.EventToolCallDelta:
		id := e.item(ev.ContentIndex, "function_call")
		return e.ev("response.function_call_arguments.delta", map[string]any{
			"item_id": id, "output_index": ev.ContentIndex, "delta": ev.Delta,
		})

	case llm.EventToolCallEnd:
		id := e.item(ev.ContentIndex, "function_call")
		return e.ev("response.output_item.done", map[string]any{
			"output_index": ev.ContentIndex,
			"item": map[string]any{
				"id": id, "type": "function_call", "call_id": ev.ToolCallID,
				"name": ev.ToolName, "arguments": ev.ToolArguments, "status": "completed",
			},
		})

	case llm.EventDone:
		usage := map[string]any{"input_tokens": 0, "output_tokens": 0, "total_tokens": 0}
		if ev.Usage != nil {
			usage = map[string]any{
				"input_tokens":  ev.Usage.InputTokens,
				"output_tokens": ev.Usage.OutputTokens,
				"total_tokens":  ev.Usage.TotalTokens,
			}
		}
		return e.ev("response.completed", map[string]any{
			"response": e.responseShell("completed", usage),
		})

	case llm.EventError:
		msg := "internal error"
		if ev.Error != nil && ev.Error.Message != "" {
			msg = ev.Error.Message
		}
		return e.ev("error", map[string]any{
			"code": "server_error", "message": msg,
		})
	}
	return nil, nil
}

// responseShell 构造 response 对象骨架。
func (e *StreamEncoder) responseShell(status string, usage map[string]any) map[string]any {
	if usage == nil {
		usage = map[string]any{"input_tokens": 0, "output_tokens": 0, "total_tokens": 0}
	}
	output := []any{}
	text := ""
	for i := 0; i < len(e.itemID); i++ {
		if item, ok := e.completed[i]; ok {
			output = append(output, item)
		}
		text += e.text[i]
	}
	return map[string]any{
		"id": e.id, "object": "response", "created_at": e.created, "status": status,
		"model": e.model, "output": output, "output_text": text,
		"parallel_tool_calls": true, "tool_choice": "auto", "tools": []any{},
		"usage": usage,
	}
}

// EncodeFinal 把聚合后的消息编码为非流式响应。
func (Protocol) EncodeFinal(msg *llm.AssistantMessage, model string) ([]byte, error) {
	if msg == nil {
		msg = &llm.AssistantMessage{}
	}
	output := make([]any, 0, len(msg.Content))
	text := ""
	for _, c := range msg.Content {
		switch c.Type {
		case llm.ContentText:
			text += c.Text
		case llm.ContentThinking:
			output = append(output, map[string]any{
				"id": "rs_" + randomHex(24), "type": "reasoning", "summary": []any{},
			})
		case llm.ContentToolCall:
			if c.ToolCall != nil {
				output = append(output, map[string]any{
					"id": "fc_" + randomHex(24), "type": "function_call", "call_id": c.ToolCall.ID,
					"name": c.ToolCall.Name, "arguments": c.ToolCall.Arguments, "status": "completed",
				})
			}
		}
	}
	if text != "" {
		output = append(output, map[string]any{
			"id": "msg_" + randomHex(24), "type": "message", "role": "assistant", "status": "completed",
			"content": []any{map[string]any{"type": "output_text", "text": text, "annotations": []any{}}},
		})
	}
	id := msg.ResponseID
	if id == "" {
		id = "resp_" + randomHex(24)
	}
	m := model
	if msg.Model != "" {
		m = msg.Model
	}
	return json.Marshal(map[string]any{
		"id": id, "object": "response", "created_at": time.Now().Unix(), "status": "completed",
		"model": m, "output": output, "output_text": text,
		"parallel_tool_calls": true, "tool_choice": "auto", "tools": []any{},
		"usage": map[string]any{
			"input_tokens":  msg.Usage.InputTokens,
			"output_tokens": msg.Usage.OutputTokens,
			"total_tokens":  msg.Usage.TotalTokens,
		},
	})
}

// AppendSSE 使用带 event 名的风格。
func (Protocol) AppendSSE(dst []byte, name string, data []byte) []byte {
	return common.AppendSSE(dst, name, data)
}

// StreamErrorEvents 为 true：Responses 客户端（如 Codex）会重试流内错误事件。
func (Protocol) StreamErrorEvents() bool { return true }

func randomHex(n int) string {
	buf := make([]byte, (n+1)/2)
	if _, err := rand.Read(buf); err != nil {
		return "000000000000000000000000"
	}
	return hex.EncodeToString(buf)[:n]
}
