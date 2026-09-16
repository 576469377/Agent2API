// Package chat 实现 OpenAI Chat Completions 协议的编解码。
package chat

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"time"

	"github.com/576469377/Agent2API/internal/api/common"
	"github.com/576469377/Agent2API/internal/llm"
)

// Protocol 是 Chat Completions 协议实现。
type Protocol struct{}

// Name 返回协议标识。
func (Protocol) Name() string { return "openai-chat" }

// ───────────────────────── 请求解码 ─────────────────────────

type request struct {
	Model         string       `json:"model"`
	Messages      []rawMessage `json:"messages"`
	Stream        bool         `json:"stream"`
	StreamOptions *struct {
		IncludeUsage bool `json:"include_usage"`
	} `json:"stream_options"`
	Tools           []rawTool `json:"tools"`
	ToolChoice      any       `json:"tool_choice"`
	MaxTokens       *int      `json:"max_tokens"`
	Temperature     *float64  `json:"temperature"`
	TopP            *float64  `json:"top_p"`
	TopK            *int      `json:"top_k"`
	Stop            any       `json:"stop"`
	Seed            *int64    `json:"seed"`
	ReasoningEffort string    `json:"reasoning_effort"`
	ResponseFormat  *struct {
		Type string `json:"type"`
	} `json:"response_format"`
}

type rawMessage struct {
	Role       string        `json:"role"`
	Content    any           `json:"content"`
	ToolCalls  []rawToolCall `json:"tool_calls"`
	ToolCallID string        `json:"tool_call_id"`
}

type rawToolCall struct {
	ID       string `json:"id"`
	Type     string `json:"type"`
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"function"`
}

type rawTool struct {
	Type     string `json:"type"`
	Function struct {
		Name        string         `json:"name"`
		Description string         `json:"description"`
		Parameters  map[string]any `json:"parameters"`
	} `json:"function"`
}

// DecodeRequest 把 Chat Completions 请求解码为 IR。
func (Protocol) DecodeRequest(body []byte) (llm.RequestMessages, error) {
	var r request
	if err := json.Unmarshal(body, &r); err != nil {
		return llm.RequestMessages{}, &llm.Failure{
			Code: "invalid_request", Message: "解析请求体失败: " + err.Error(), Cause: err, ClientFixable: true,
		}
	}
	if r.Model == "" {
		return llm.RequestMessages{}, &llm.Failure{
			Code: "missing_model", Message: "缺少 model 字段", ClientFixable: true,
		}
	}

	out := llm.RequestMessages{
		Model:           r.Model,
		Messages:        make([]llm.Message, 0, len(r.Messages)),
		MaxTokens:       r.MaxTokens,
		Temperature:     r.Temperature,
		TopP:            r.TopP,
		TopK:            r.TopK,
		Seed:            r.Seed,
		ReasoningEffort: r.ReasoningEffort,
		Stream:          r.Stream,
		IncludeUsage:    r.StreamOptions != nil && r.StreamOptions.IncludeUsage,
	}

	for _, m := range r.Messages {
		msg, err := convertMessage(m)
		if err != nil {
			return llm.RequestMessages{}, err
		}
		// system 单独提取：上游要求它置顶，且脱敏只作用于它。
		if msg.Role == llm.RoleUser && m.Role == "system" {
			out.SystemPrompt = joinContent(msg)
			continue
		}
		out.Messages = append(out.Messages, msg)
	}

	if r.Stop != nil {
		out.Stop = normalizeStop(r.Stop)
	}
	if r.ResponseFormat != nil && r.ResponseFormat.Type == "json_object" {
		out.JSONMode = true
	}
	if len(r.Tools) > 0 {
		out.Tools = make([]llm.ToolDefinition, 0, len(r.Tools))
		for _, t := range r.Tools {
			if t.Function.Name == "" {
				continue
			}
			out.Tools = append(out.Tools, llm.ToolDefinition{
				Name:        t.Function.Name,
				Description: t.Function.Description,
				Parameters:  t.Function.Parameters,
			})
		}
	}
	if r.ToolChoice != nil {
		out.ToolChoice = normalizeToolChoice(r.ToolChoice)
	}
	return out, nil
}

func convertMessage(m rawMessage) (llm.Message, error) {
	role := llm.RoleUser
	switch m.Role {
	case "system":
		role = llm.RoleUser // system 由调用方提取
	case "assistant":
		role = llm.RoleAssistant
	case "tool":
		role = llm.RoleTool
	case "user":
		role = llm.RoleUser
	}

	msg := llm.Message{Role: role, ToolCallID: m.ToolCallID}

	// content 可能是 string，也可能是 [{type,text}|{type,image_url}]
	switch c := m.Content.(type) {
	case nil:
	case string:
		if c != "" {
			msg.Content = append(msg.Content, llm.Content{Type: llm.ContentText, Text: c})
		}
	case []any:
		for _, item := range c {
			part, _ := item.(map[string]any)
			if part == nil {
				continue
			}
			switch part["type"] {
			case "text":
				if t, ok := part["text"].(string); ok && t != "" {
					msg.Content = append(msg.Content, llm.Content{Type: llm.ContentText, Text: t})
				}
			case "image_url":
				if u := extractImageURL(part["image_url"]); u != "" {
					msg.Content = append(msg.Content, llm.Content{Type: llm.ContentImage, ImageURL: u})
				}
			}
		}
	}

	for _, tc := range m.ToolCalls {
		msg.Content = append(msg.Content, llm.Content{
			Type:     llm.ContentToolCall,
			ToolCall: &llm.ToolCall{ID: tc.ID, Name: tc.Function.Name, Arguments: tc.Function.Arguments},
		})
	}
	if role == llm.RoleTool && len(msg.Content) > 0 {
		// 归一化为 ToolResult，便于上游还原；若含图片则一并保留在 Blocks 里。
		tr := &llm.ToolResult{ToolCallID: m.ToolCallID, Content: joinContent(msg)}
		if contentHasImage(msg.Content) {
			tr.Blocks = msg.Content
		}
		msg.Content = []llm.Content{{Type: llm.ContentToolResult, ToolResult: tr}}
	}
	return msg, nil
}

func extractImageURL(v any) string {
	switch t := v.(type) {
	case string:
		return t
	case map[string]any:
		if u, ok := t["url"].(string); ok {
			return u
		}
	}
	return ""
}

func joinContent(m llm.Message) string {
	out := ""
	for _, c := range m.Content {
		if c.Type == llm.ContentText {
			out += c.Text
		}
	}
	return out
}

// contentHasImage 判断内容块序列里是否含有图片。
func contentHasImage(blocks []llm.Content) bool {
	for _, b := range blocks {
		if b.Type == llm.ContentImage {
			return true
		}
	}
	return false
}

func normalizeStop(v any) []string {
	switch t := v.(type) {
	case string:
		if t == "" {
			return nil
		}
		return []string{t}
	case []any:
		out := make([]string, 0, len(t))
		for _, s := range t {
			if str, ok := s.(string); ok && str != "" {
				out = append(out, str)
			}
		}
		return out
	}
	return nil
}

// normalizeToolChoice 把 OpenAI 的 object/string 形式归一化。
// 注意：上游只接受 string，转换在上游适配器的 payload 层完成，这里先归一到 IR。
func normalizeToolChoice(v any) *llm.ToolChoice {
	switch t := v.(type) {
	case string:
		switch t {
		case "none":
			return &llm.ToolChoice{Mode: llm.ToolChoiceNone}
		case "required", "any":
			return &llm.ToolChoice{Mode: llm.ToolChoiceRequired}
		default:
			return &llm.ToolChoice{Mode: llm.ToolChoiceAuto}
		}
	case map[string]any:
		fn, _ := t["function"].(map[string]any)
		name, _ := fn["name"].(string)
		if typ, _ := t["type"].(string); typ == "function" && name != "" {
			return &llm.ToolChoice{Mode: llm.ToolChoiceFunction, FunctionName: name}
		}
		if typ, _ := t["type"].(string); typ == "function" {
			return &llm.ToolChoice{Mode: llm.ToolChoiceRequired}
		}
	}
	return nil
}

// ───────────────────────── 流式编码 ─────────────────────────

type delta struct {
	Role             string          `json:"role,omitempty"`
	Content          string          `json:"content,omitempty"`
	ReasoningContent string          `json:"reasoning_content,omitempty"`
	ToolCalls        []deltaToolCall `json:"tool_calls,omitempty"`
}

type deltaToolCall struct {
	Index    int    `json:"index"`
	ID       string `json:"id,omitempty"`
	Type     string `json:"type,omitempty"`
	Function struct {
		Name      string `json:"name,omitempty"`
		Arguments string `json:"arguments,omitempty"`
	} `json:"function"`
}

type choice struct {
	Index        int     `json:"index"`
	Delta        delta   `json:"delta"`
	FinishReason *string `json:"finish_reason,omitempty"`
}

type streamChunk struct {
	ID      string   `json:"id"`
	Object  string   `json:"object"`
	Created int64    `json:"created"`
	Model   string   `json:"model"`
	Choices []choice `json:"choices"`
	Usage   *usage   `json:"usage,omitempty"`
}

type usage struct {
	PromptTokens     int           `json:"prompt_tokens"`
	CompletionTokens int           `json:"completion_tokens"`
	TotalTokens      int           `json:"total_tokens"`
	Details          *usageDetails `json:"completion_tokens_details,omitempty"`
}

type usageDetails struct {
	ReasoningTokens int `json:"reasoning_tokens"`
	CachedTokens    int `json:"cached_tokens,omitempty"`
}

// StreamEncoder 把 IR 事件编码为 Chat Completions 流式帧。
type StreamEncoder struct {
	id       string
	created  int64
	model    string
	roleSent bool
	// toolOrdinal 把 IR 的全局 ContentIndex 映射到 tool_calls 数组下标。
	toolOrdinal map[int]int
	nextOrdinal int
}

// NewStreamEncoder 构造流式编码器。
func (Protocol) NewStreamEncoder(model string, includeUsage bool) *StreamEncoder {
	return &StreamEncoder{
		id:          "chatcmpl-" + randomHex(16),
		created:     time.Now().Unix(),
		model:       model,
		toolOrdinal: map[int]int{},
	}
}

// Encode 把一个 IR 事件编码为若干 SSE 帧。
func (e *StreamEncoder) Encode(ev llm.ResponseEvent) ([]common.SSEEvent, error) {
	if ev.Model != "" {
		e.model = ev.Model
	}

	switch ev.Type {
	case llm.EventStart:
		return nil, nil

	case llm.EventTextStart, llm.EventThinkingStart:
		if e.roleSent {
			return nil, nil
		}
		e.roleSent = true
		return e.chunk(func(c *choice) { c.Delta.Role = "assistant" })

	case llm.EventTextDelta:
		if ev.Delta == "" {
			return nil, nil
		}
		if !e.roleSent {
			e.roleSent = true
			return e.chunk(func(c *choice) {
				c.Delta.Role = "assistant"
				c.Delta.Content = ev.Delta
			})
		}
		return e.chunk(func(c *choice) { c.Delta.Content = ev.Delta })

	case llm.EventThinkingDelta:
		if ev.Delta == "" {
			return nil, nil
		}
		if !e.roleSent {
			e.roleSent = true
			return e.chunk(func(c *choice) {
				c.Delta.Role = "assistant"
				c.Delta.ReasoningContent = ev.Delta
			})
		}
		return e.chunk(func(c *choice) { c.Delta.ReasoningContent = ev.Delta })

	case llm.EventTextEnd, llm.EventThinkingEnd:
		return nil, nil

	case llm.EventToolCallStart:
		ord, ok := e.toolOrdinal[ev.ContentIndex]
		if !ok {
			ord = e.nextOrdinal
			e.toolOrdinal[ev.ContentIndex] = ord
			e.nextOrdinal++
		}
		return e.chunk(func(c *choice) {
			var tc deltaToolCall
			tc.Index = ord
			tc.ID = ev.ToolCallID
			tc.Type = "function"
			tc.Function.Name = ev.ToolName
			c.Delta.ToolCalls = []deltaToolCall{tc}
		})

	case llm.EventToolCallDelta:
		ord, ok := e.toolOrdinal[ev.ContentIndex]
		if !ok {
			ord = e.nextOrdinal
			e.toolOrdinal[ev.ContentIndex] = ord
			e.nextOrdinal++
		}
		return e.chunk(func(c *choice) {
			var tc deltaToolCall
			tc.Index = ord
			tc.Function.Arguments = ev.Delta
			c.Delta.ToolCalls = []deltaToolCall{tc}
		})

	case llm.EventToolCallEnd:
		return nil, nil

	case llm.EventDone:
		return e.done(ev)

	case llm.EventError:
		// 流中断必须给客户端一个明确的失败信号：官方 SDK 看到 data 帧里的
		// error 字段会抛 APIError（openai-python _streaming.py 的既有契约）。
		// 旧实现返回 0 帧——客户端已收到 200，只能拿到半截内容，
		// 无法区分「正常结束」与「上游挂了」。
		// 刻意不发 finish_reason / [DONE]：那等于谎报正常收尾。
		body, err := json.Marshal(common.BuildErrorPayload(ev.Error))
		if err != nil {
			body = []byte(`{"error":{"message":"internal server error","type":"server_error"}}`)
		}
		return []common.SSEEvent{{Data: body}}, nil
	}
	return nil, nil
}

func (e *StreamEncoder) done(ev llm.ResponseEvent) ([]common.SSEEvent, error) {
	reason := string(ev.StopReason)
	if reason == "" {
		reason = "stop"
	}
	var u *usage
	if ev.Usage != nil {
		u = &usage{
			PromptTokens:     ev.Usage.InputTokens,
			CompletionTokens: ev.Usage.OutputTokens,
			TotalTokens:      ev.Usage.TotalTokens,
		}
		if ev.Usage.ReasoningTokens > 0 || ev.Usage.CachedTokens > 0 {
			u.Details = &usageDetails{ReasoningTokens: ev.Usage.ReasoningTokens, CachedTokens: ev.Usage.CachedTokens}
		}
	}
	data, err := json.Marshal(streamChunk{
		ID: e.id, Object: "chat.completion.chunk", Created: e.created, Model: e.model,
		Choices: []choice{{Index: 0, FinishReason: &reason}},
		Usage:   u,
	})
	if err != nil {
		return nil, err
	}
	return []common.SSEEvent{{Data: data}, {Name: "[DONE]", Data: []byte("[DONE]")}}, nil
}

func (e *StreamEncoder) chunk(mutate func(*choice)) ([]common.SSEEvent, error) {
	c := choice{Index: 0}
	mutate(&c)
	data, err := json.Marshal(streamChunk{
		ID: e.id, Object: "chat.completion.chunk", Created: e.created, Model: e.model,
		Choices: []choice{c},
	})
	if err != nil {
		return nil, err
	}
	return []common.SSEEvent{{Data: data}}, nil
}

// EncodeFinal 把聚合后的消息编码为非流式响应。
func (Protocol) EncodeFinal(msg *llm.AssistantMessage, model string) ([]byte, error) {
	if msg == nil {
		msg = &llm.AssistantMessage{}
	}
	text := ""
	thinking := ""
	var toolCalls []finalToolCall
	for _, c := range msg.Content {
		switch c.Type {
		case llm.ContentText:
			text += c.Text
		case llm.ContentThinking:
			thinking += c.Thinking
		case llm.ContentToolCall:
			if c.ToolCall != nil {
				toolCalls = append(toolCalls, finalToolCall{
					ID:   c.ToolCall.ID,
					Type: "function",
					Function: struct {
						Name      string `json:"name"`
						Arguments string `json:"arguments"`
					}{Name: c.ToolCall.Name, Arguments: c.ToolCall.Arguments},
				})
			}
		}
	}

	message := map[string]any{"role": "assistant", "content": text}
	if thinking != "" {
		message["reasoning_content"] = thinking
	}
	if len(toolCalls) > 0 {
		message["tool_calls"] = toolCalls
	} else {
		message["tool_calls"] = nil
	}

	reason := string(msg.StopReason)
	if reason == "" {
		reason = "stop"
	}
	id := msg.ResponseID
	if id == "" {
		id = "chatcmpl-" + randomHex(16)
	}
	m := model
	if msg.Model != "" {
		m = msg.Model
	}

	resp := map[string]any{
		"id":      id,
		"object":  "chat.completion",
		"created": time.Now().Unix(),
		"model":   m,
		"choices": []map[string]any{
			{"index": 0, "message": message, "finish_reason": reason, "logprobs": nil},
		},
		"usage": map[string]any{
			"prompt_tokens":     msg.Usage.InputTokens,
			"completion_tokens": msg.Usage.OutputTokens,
			"total_tokens":      msg.Usage.TotalTokens,
		},
	}
	return json.Marshal(resp)
}

type finalToolCall struct {
	ID       string `json:"id"`
	Type     string `json:"type"`
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"function"`
}

// AppendSSE 使用 data-only 风格。
func (Protocol) AppendSSE(dst []byte, name string, data []byte) []byte {
	if name == "[DONE]" {
		return common.AppendSSE(dst, "[DONE]", nil)
	}
	return common.AppendSSE(dst, "", data)
}

// StreamErrorEvents 表示流式客户端是否把流内错误事件当作可重试信号。
// Chat Completions 客户端通常不会，返回 false。
func (Protocol) StreamErrorEvents() bool { return false }

// ───────────────────────── 模型列表 ─────────────────────────

// EncodeModels 输出 /v1/models。
func (Protocol) EncodeModels(models []modelEntry) ([]byte, error) {
	return json.Marshal(map[string]any{
		"object": "list",
		"data":   models,
	})
}

// ModelEntry 是 /v1/models 的条目。
type ModelEntry struct {
	ID      string `json:"id"`
	Object  string `json:"object"`
	Created int64  `json:"created"`
	OwnedBy string `json:"owned_by"`
}

type modelEntry = ModelEntry

// NewModelEntry 构造模型条目。
func NewModelEntry(id string, created int64) ModelEntry {
	return ModelEntry{ID: id, Object: "model", Created: created, OwnedBy: "workbuddy"}
}

func randomHex(n int) string {
	buf := make([]byte, (n+1)/2)
	if _, err := rand.Read(buf); err != nil {
		return "0000000000000000"
	}
	return hex.EncodeToString(buf)[:n]
}
