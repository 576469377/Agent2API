// Package messages 实现 Anthropic Messages 协议的编解码。
//
// 这份实现决定了 Claude Code / Claude 系客户端能否直接使用本网关。
package messages

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"sort"

	"github.com/576469377/Agent2API/internal/api/common"
	"github.com/576469377/Agent2API/internal/llm"
)

// Protocol 是 Anthropic Messages 协议实现。
type Protocol struct{}

// Name 返回协议标识。
func (Protocol) Name() string { return "anthropic-messages" }

// ───────────────────────── 请求解码 ─────────────────────────

type request struct {
	Model         string         `json:"model"`
	System        any            `json:"system"`
	Messages      []rawMessage   `json:"messages"`
	MaxTokens     *int           `json:"max_tokens"`
	Tools         []rawTool      `json:"tools"`
	ToolChoice    any            `json:"tool_choice"`
	Stream        bool           `json:"stream"`
	Temperature   *float64       `json:"temperature"`
	TopP          *float64       `json:"top_p"`
	TopK          *int           `json:"top_k"`
	StopSequences []string       `json:"stop_sequences"`
	Thinking      *rawThinking   `json:"thinking"`
	Metadata      map[string]any `json:"metadata"`
}

type rawThinking struct {
	Type         string `json:"type"`
	BudgetTokens int    `json:"budget_tokens"`
}

type rawMessage struct {
	Role    string          `json:"role"`
	Content json.RawMessage `json:"content"`
}

type rawContentBlock struct {
	Type      string          `json:"type"`
	Text      string          `json:"text"`
	Thinking  string          `json:"thinking"`
	Signature string          `json:"signature"`
	ID        string          `json:"id"`
	Name      string          `json:"name"`
	Input     json.RawMessage `json:"input"`
	ToolUseID string          `json:"tool_use_id"`
	Content   json.RawMessage `json:"content"`
	IsError   bool            `json:"is_error"`
	Source    map[string]any  `json:"source"`
}

type rawTool struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	InputSchema map[string]any `json:"input_schema"`
}

// DecodeRequest 把 Anthropic 请求解码为 IR。
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
		Model:       r.Model,
		Messages:    make([]llm.Message, 0, len(r.Messages)),
		MaxTokens:   r.MaxTokens,
		Temperature: r.Temperature,
		TopP:        r.TopP,
		TopK:        r.TopK,
		Stop:        r.StopSequences,
		Stream:      r.Stream,
	}
	out.SystemPrompt = extractSystem(r.System)

	for _, m := range r.Messages {
		msg := llm.Message{}
		switch m.Role {
		case "assistant":
			msg.Role = llm.RoleAssistant
		default:
			msg.Role = llm.RoleUser
		}
		blocks, err := decodeContent(m.Content)
		if err != nil {
			return llm.RequestMessages{}, err
		}
		// tool_result 属于 user 角色
		hasToolResult := false
		for _, b := range blocks {
			if b.Type == llm.ContentToolResult {
				hasToolResult = true
				msg.ToolCallID = b.ToolResult.ToolCallID
			}
		}
		if hasToolResult {
			msg.Role = llm.RoleTool
		}
		msg.Content = blocks
		out.Messages = append(out.Messages, msg)
	}

	if len(r.Tools) > 0 {
		out.Tools = make([]llm.ToolDefinition, 0, len(r.Tools))
		for _, t := range r.Tools {
			if t.Name == "" {
				continue
			}
			out.Tools = append(out.Tools, llm.ToolDefinition{
				Name:        t.Name,
				Description: t.Description,
				Parameters:  t.InputSchema,
			})
		}
	}
	if r.ToolChoice != nil {
		out.ToolChoice = normalizeToolChoice(r.ToolChoice)
	}
	if r.Thinking != nil && r.Thinking.Type == "enabled" && out.ReasoningEffort == "" {
		// Anthropic 用 budget_tokens 表达思考预算，映射到通用的 effort。
		switch {
		case r.Thinking.BudgetTokens >= 8000:
			out.ReasoningEffort = "high"
		default:
			out.ReasoningEffort = "medium"
		}
	}
	return out, nil
}

// extractSystem 兼容 string 与 [{type:text,text}] 两种形态。
func extractSystem(v any) string {
	switch t := v.(type) {
	case string:
		return t
	case []any:
		out := ""
		for _, item := range t {
			if m, ok := item.(map[string]any); ok {
				if s, ok := m["text"].(string); ok {
					out += s
				}
			}
		}
		return out
	}
	return ""
}

func decodeContent(raw json.RawMessage) ([]llm.Content, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	trimmed := raw
	if trimmed[0] == '"' {
		var s string
		if err := json.Unmarshal(raw, &s); err == nil {
			if s == "" {
				return nil, nil
			}
			return []llm.Content{{Type: llm.ContentText, Text: s}}, nil
		}
	}
	var blocks []rawContentBlock
	if err := json.Unmarshal(raw, &blocks); err != nil {
		return nil, &llm.Failure{Code: "invalid_content", Message: "解析 content 失败: " + err.Error(), Cause: err, ClientFixable: true}
	}
	out := make([]llm.Content, 0, len(blocks))
	for _, b := range blocks {
		switch b.Type {
		case "text":
			if b.Text != "" {
				out = append(out, llm.Content{Type: llm.ContentText, Text: b.Text})
			}
		case "thinking":
			if b.Thinking != "" {
				out = append(out, llm.Content{Type: llm.ContentThinking, Thinking: b.Thinking, ThinkingSignature: b.Signature})
			}
		case "tool_use":
			args := "{}"
			if len(b.Input) > 0 {
				args = string(b.Input)
			}
			out = append(out, llm.Content{Type: llm.ContentToolCall, ToolCall: &llm.ToolCall{
				ID: b.ID, Name: b.Name, Arguments: args,
			}})
		case "tool_result":
			tr := &llm.ToolResult{ToolCallID: b.ToolUseID, IsError: b.IsError}
			if len(b.Content) > 0 {
				var s string
				if err := json.Unmarshal(b.Content, &s); err == nil {
					tr.Content = s
				} else {
					var inner []rawContentBlock
					if err := json.Unmarshal(b.Content, &inner); err == nil {
						for _, ib := range inner {
							switch ib.Type {
							case "text":
								if ib.Text != "" {
									tr.Content += ib.Text
									tr.Blocks = append(tr.Blocks, llm.Content{Type: llm.ContentText, Text: ib.Text})
								}
							case "image":
								// Claude Code 的 Read 工具会把图片作为 image 块嵌进 tool_result，
								// 必须原样保留，否则模型拿不到图。
								if u := decodeImageSource(ib.Source); u != "" {
									tr.Blocks = append(tr.Blocks, llm.Content{Type: llm.ContentImage, ImageURL: u})
								}
							}
						}
					} else {
						tr.Content = string(b.Content)
					}
				}
			}
			out = append(out, llm.Content{Type: llm.ContentToolResult, ToolResult: tr})
		case "image":
			if url := decodeImageSource(b.Source); url != "" {
				out = append(out, llm.Content{Type: llm.ContentImage, ImageURL: url})
			}
		}
	}
	return out, nil
}

// decodeImageSource 把 Anthropic 的 image source 转成 IR 期望的 data: URL 或直连 URL。
//
// Anthropic 的 base64 图片只给裸 data 与 media_type，缺少 data:image/png;base64, 前缀，
// 直接塞进 image_url 上游无法解析（实测 400 Invalid request parameters）。这里补全前缀。
func decodeImageSource(src map[string]any) string {
	if src == nil {
		return ""
	}
	switch src["type"] {
	case "base64":
		media, _ := src["media_type"].(string)
		if media == "" {
			media = "image/png"
		}
		data, _ := src["data"].(string)
		if data == "" {
			return ""
		}
		return "data:" + media + ";base64," + data
	case "url":
		if u, ok := src["url"].(string); ok {
			return u
		}
	}
	// 兼容缺省 type 但带 url 的写法
	if u, ok := src["url"].(string); ok {
		return u
	}
	if d, ok := src["data"].(string); ok {
		media, _ := src["media_type"].(string)
		if media == "" {
			media = "image/png"
		}
		return "data:" + media + ";base64," + d
	}
	return ""
}

func normalizeToolChoice(v any) *llm.ToolChoice {
	switch t := v.(type) {
	case string:
		switch t {
		case "none":
			return &llm.ToolChoice{Mode: llm.ToolChoiceNone}
		case "any":
			return &llm.ToolChoice{Mode: llm.ToolChoiceRequired}
		default:
			return &llm.ToolChoice{Mode: llm.ToolChoiceAuto}
		}
	case map[string]any:
		typ, _ := t["type"].(string)
		name, _ := t["name"].(string)
		switch typ {
		case "tool":
			return &llm.ToolChoice{Mode: llm.ToolChoiceFunction, FunctionName: name}
		case "any":
			return &llm.ToolChoice{Mode: llm.ToolChoiceRequired}
		case "none":
			return &llm.ToolChoice{Mode: llm.ToolChoiceNone}
		}
	}
	return nil
}

// ───────────────────────── 流式编码 ─────────────────────────

// StreamEncoder 把 IR 事件编码为 Anthropic 流式事件。
//
// Anthropic 的 content block 下标是全消息内递增的，与 IR 的 ContentIndex 语义一致，
// 因此可以直接映射，无需额外的下标表。
type StreamEncoder struct {
	id       string
	model    string
	blockIdx int
	// startedBlock 记录已发出 content_block_start 的下标；
	// openBlock 记录已 start 但尚未 stop 的下标。
	//
	// 两者必须分开维护，且 stop 时必须从 openBlock 移除。否则收尾阶段会对同一
	// 个已经关闭的内容块再补发一次 content_block_stop，客户端 SDK 会二次结算
	// 该块，表现为整段回复被渲染两遍。
	startedBlock map[int]bool
	openBlock    map[int]bool
}

// NewStreamEncoder 构造流式编码器。
func (Protocol) NewStreamEncoder(model string, includeUsage bool) *StreamEncoder {
	return &StreamEncoder{
		id:           "msg_" + randomHex(24),
		model:        model,
		startedBlock: map[int]bool{},
		openBlock:    map[int]bool{},
	}
}

func (e *StreamEncoder) ev(name string, payload any) ([]common.SSEEvent, error) {
	data, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	return []common.SSEEvent{{Name: name, Data: data}}, nil
}

// ensureStarted 保证指定下标的内容块已发出 content_block_start。
// 已发过则直接返回空，绝不重复 start。
func (e *StreamEncoder) ensureStarted(idx int, kind string) ([]common.SSEEvent, error) {
	if e.startedBlock[idx] {
		return nil, nil
	}
	e.startedBlock[idx] = true
	e.openBlock[idx] = true

	block := map[string]any{"type": "text", "text": ""}
	if kind == "thinking" {
		block = map[string]any{"type": "thinking", "thinking": "", "signature": ""}
	}
	return e.ev("content_block_start", map[string]any{
		"type":          "content_block_start",
		"index":         idx,
		"content_block": block,
	})
}

// stopBlock 关闭指定下标的内容块。已关闭或从未开启则不做任何事。
func (e *StreamEncoder) stopBlock(idx int) ([]common.SSEEvent, error) {
	if !e.openBlock[idx] {
		return nil, nil
	}
	delete(e.openBlock, idx)
	return e.ev("content_block_stop", map[string]any{
		"type":  "content_block_stop",
		"index": idx,
	})
}

// Encode 把一个 IR 事件编码为若干 SSE 帧。
func (e *StreamEncoder) Encode(ev llm.ResponseEvent) ([]common.SSEEvent, error) {
	switch ev.Type {
	case llm.EventStart:
		if ev.Model != "" {
			e.model = ev.Model
		}
		return e.ev("message_start", map[string]any{
			"type": "message_start",
			"message": map[string]any{
				"id": e.id, "type": "message", "role": "assistant",
				"model": e.model, "content": []any{},
				"stop_reason": nil, "stop_sequence": nil,
				"usage": map[string]any{"input_tokens": 0, "output_tokens": 1},
			},
		})

	case llm.EventThinkingStart:
		return e.ensureStarted(ev.ContentIndex, "thinking")

	case llm.EventThinkingDelta:
		pre, err := e.ensureStarted(ev.ContentIndex, "thinking")
		if err != nil {
			return nil, err
		}
		d, err := e.ev("content_block_delta", map[string]any{
			"type":  "content_block_delta",
			"index": ev.ContentIndex,
			"delta": map[string]any{"type": "thinking_delta", "thinking": ev.Delta},
		})
		return append(pre, d...), err

	case llm.EventTextStart:
		return e.ensureStarted(ev.ContentIndex, "text")

	case llm.EventTextDelta:
		pre, err := e.ensureStarted(ev.ContentIndex, "text")
		if err != nil {
			return nil, err
		}
		d, err := e.ev("content_block_delta", map[string]any{
			"type":  "content_block_delta",
			"index": ev.ContentIndex,
			"delta": map[string]any{"type": "text_delta", "text": ev.Delta},
		})
		return append(pre, d...), err

	case llm.EventToolCallStart:
		if e.startedBlock[ev.ContentIndex] {
			return nil, nil
		}
		e.startedBlock[ev.ContentIndex] = true
		e.openBlock[ev.ContentIndex] = true
		return e.ev("content_block_start", map[string]any{
			"type":  "content_block_start",
			"index": ev.ContentIndex,
			"content_block": map[string]any{
				"type":  "tool_use",
				"id":    ev.ToolCallID,
				"name":  ev.ToolName,
				"input": map[string]any{},
			},
		})

	case llm.EventToolCallDelta:
		pre, err := e.ensureStarted(ev.ContentIndex, "text")
		if err != nil {
			return nil, err
		}
		d, err := e.ev("content_block_delta", map[string]any{
			"type":  "content_block_delta",
			"index": ev.ContentIndex,
			"delta": map[string]any{"type": "input_json_delta", "partial_json": ev.Delta},
		})
		return append(pre, d...), err

	case llm.EventTextEnd, llm.EventThinkingEnd, llm.EventToolCallEnd:
		return e.stopBlock(ev.ContentIndex)

	case llm.EventDone:
		return e.done(ev)

	case llm.EventError:
		msg := "internal error"
		if ev.Error != nil && ev.Error.Message != "" {
			msg = ev.Error.Message
		}
		return e.ev("error", map[string]any{
			"type":  "error",
			"error": map[string]any{"type": "api_error", "message": msg},
		})
	}
	return nil, nil
}

func (e *StreamEncoder) done(ev llm.ResponseEvent) ([]common.SSEEvent, error) {
	// 只关闭仍未关闭的块。正常情况下上游适配器已发过 End，这里应为空；
	// 保留它是为了兜底「流异常中断、块未闭合」的情况。
	indices := make([]int, 0, len(e.openBlock))
	for idx := range e.openBlock {
		indices = append(indices, idx)
	}
	sort.Ints(indices)

	out := make([]common.SSEEvent, 0, len(indices)+2)
	for _, idx := range indices {
		stopped, err := e.stopBlock(idx)
		if err != nil {
			return nil, err
		}
		out = append(out, stopped...)
	}

	outTokens := 0
	if ev.Usage != nil {
		outTokens = ev.Usage.OutputTokens
	}
	deltaData, err := json.Marshal(map[string]any{
		"type": "message_delta",
		"delta": map[string]any{
			"stop_reason":   mapStopReason(ev.StopReason),
			"stop_sequence": nil,
		},
		"usage": map[string]any{"output_tokens": outTokens},
	})
	if err != nil {
		return nil, err
	}
	out = append(out, common.SSEEvent{Name: "message_delta", Data: deltaData})

	stopData, _ := json.Marshal(map[string]any{"type": "message_stop"})
	out = append(out, common.SSEEvent{Name: "message_stop", Data: stopData})
	return out, nil
}

// mapStopReason 把 IR 停止原因映射为 Anthropic 的 stop_reason。
func mapStopReason(r llm.StopReason) string {
	switch r {
	case llm.StopReasonToolCalls:
		return "tool_use"
	case llm.StopReasonLength:
		return "max_tokens"
	default:
		return "end_turn"
	}
}

// EncodeFinal 把聚合后的消息编码为非流式响应。
func (Protocol) EncodeFinal(msg *llm.AssistantMessage, model string) ([]byte, error) {
	if msg == nil {
		msg = &llm.AssistantMessage{}
	}
	content := make([]any, 0, len(msg.Content))
	for _, c := range msg.Content {
		switch c.Type {
		case llm.ContentText:
			if c.Text != "" {
				content = append(content, map[string]any{"type": "text", "text": c.Text})
			}
		case llm.ContentThinking:
			if c.Thinking != "" {
				content = append(content, map[string]any{
					"type": "thinking", "thinking": c.Thinking, "signature": c.ThinkingSignature,
				})
			}
		case llm.ContentToolCall:
			if c.ToolCall != nil {
				var input any
				if err := json.Unmarshal([]byte(c.ToolCall.Arguments), &input); err != nil {
					input = map[string]any{}
				}
				content = append(content, map[string]any{
					"type": "tool_use", "id": c.ToolCall.ID, "name": c.ToolCall.Name, "input": input,
				})
			}
		}
	}
	if len(content) == 0 {
		content = append(content, map[string]any{"type": "text", "text": ""})
	}
	id := msg.ResponseID
	if id == "" {
		id = "msg_" + randomHex(24)
	}
	m := model
	if msg.Model != "" {
		m = msg.Model
	}
	return json.Marshal(map[string]any{
		"id": id, "type": "message", "role": "assistant", "model": m,
		"content": content, "stop_reason": mapStopReason(msg.StopReason), "stop_sequence": nil,
		"usage": map[string]any{
			"input_tokens":  msg.Usage.InputTokens,
			"output_tokens": msg.Usage.OutputTokens,
		},
	})
}

// AppendSSE 使用带 event 名的风格。
func (Protocol) AppendSSE(dst []byte, name string, data []byte) []byte {
	return common.AppendSSE(dst, name, data)
}

// StreamErrorEvents 为 true：Anthropic 客户端会把流内 error 事件当作可重试信号。
func (Protocol) StreamErrorEvents() bool { return true }

func randomHex(n int) string {
	buf := make([]byte, (n+1)/2)
	if _, err := rand.Read(buf); err != nil {
		return "000000000000000000000000"
	}
	return hex.EncodeToString(buf)[:n]
}
