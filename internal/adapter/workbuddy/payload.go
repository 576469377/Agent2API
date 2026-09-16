package workbuddy

import (
	"encoding/json"

	"github.com/576469377/Agent2API/internal/llm"
)

// upstreamMessage 是上游 chat/completions 的消息格式。
// 上游直接吃 OpenAI Chat Completions 兼容结构，因此请求侧几乎无需转换。
type upstreamMessage struct {
	Role       string             `json:"role"`
	Content    any                `json:"content"` // string 或 []contentPart
	ToolCalls  []upstreamToolCall `json:"tool_calls,omitempty"`
	ToolCallID string             `json:"tool_call_id,omitempty"`
}

type contentPart struct {
	Type     string       `json:"type"`
	Text     string       `json:"text,omitempty"`
	ImageURL *imageURLRef `json:"image_url,omitempty"`
}

type imageURLRef struct {
	URL string `json:"url"`
}

type upstreamToolCall struct {
	ID       string             `json:"id"`
	Type     string             `json:"type"`
	Function upstreamToolCallFn `json:"function"`
}

type upstreamToolCallFn struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

type upstreamTool struct {
	Type     string         `json:"type"`
	Function upstreamToolFn `json:"function"`
}

type upstreamToolFn struct {
	Name        string         `json:"name"`
	Description string         `json:"description,omitempty"`
	Parameters  map[string]any `json:"parameters,omitempty"`
}

// upstreamRequest 是发往 /v2/chat/completions 的请求体。
type upstreamRequest struct {
	Model    string            `json:"model"`
	Messages []upstreamMessage `json:"messages"`
	Stream   bool              `json:"stream"`

	// 非 OpenAI 标准字段，但上游接受；代理用它们控制超时。
	StreamOptions map[string]any `json:"stream_options,omitempty"`
	Tools         []upstreamTool `json:"tools,omitempty"`
	// ToolChoice 必须是 string！上游 Go 后端此字段为 string 类型，
	// object 形式会触发 400: cannot unmarshal object into Go struct field
	// Request.tool_choice of type string。
	ToolChoice any `json:"tool_choice,omitempty"`

	MaxTokens   *int     `json:"max_tokens,omitempty"`
	Temperature *float64 `json:"temperature,omitempty"`
	TopP        *float64 `json:"top_p,omitempty"`
	TopK        *int     `json:"top_k,omitempty"`
	Stop        []string `json:"stop,omitempty"`
	Seed        *int64   `json:"seed,omitempty"`

	ReasoningEffort string         `json:"reasoning_effort,omitempty"`
	ResponseFormat  map[string]any `json:"response_format,omitempty"`
}

// buildUpstreamRequest 把 IR 请求转换为上游请求。
//
// 注意：上游不支持非流式（code 11101），因此 Stream 恒为 true，
// 非流式客户端的响应由代理侧本地聚合。
func buildUpstreamRequest(req llm.RequestMessages, sanitize bool) upstreamRequest {
	model := req.Model
	if model == "" {
		model = defaultModel
	}

	msgs := make([]upstreamMessage, 0, len(req.Messages)+1)
	if req.SystemPrompt != "" {
		content := req.SystemPrompt
		if sanitize {
			content = SanitizeSystemPrompt(content)
		}
		msgs = append(msgs, upstreamMessage{Role: "system", Content: content})
	}
	msgs = append(msgs, convertMessages(req.Messages, sanitize)...)

	out := upstreamRequest{
		Model:         model,
		Messages:      msgs,
		Stream:        true,
		StreamOptions: map[string]any{"include_usage": true},
		MaxTokens:     req.MaxTokens,
		Temperature:   req.Temperature,
		TopP:          req.TopP,
		TopK:          req.TopK,
		Stop:          req.Stop,
		Seed:          req.Seed,
	}
	if req.ReasoningEffort != "" {
		out.ReasoningEffort = req.ReasoningEffort
	}
	if req.JSONMode {
		out.ResponseFormat = map[string]any{"type": "json_object"}
	}
	if len(req.Tools) > 0 {
		out.Tools = convertTools(req.Tools, sanitize)
	}
	if req.ToolChoice != nil {
		out.ToolChoice = normalizeToolChoice(req.ToolChoice)
	}
	return out
}

// convertMessage 把 IR 消息转换为上游消息。
func convertMessage(m llm.Message, sanitize bool) upstreamMessage {
	var (
		textParts []string
		parts     []contentPart
		hasImage  bool
		toolCalls []upstreamToolCall
		toolRes   string
		isError   bool
	)

	for _, c := range m.Content {
		switch c.Type {
		case llm.ContentText:
			textParts = append(textParts, c.Text)
			parts = append(parts, contentPart{Type: "text", Text: c.Text})
		case llm.ContentImage:
			if c.ImageURL != "" {
				hasImage = true
				parts = append(parts, contentPart{Type: "image_url", ImageURL: &imageURLRef{URL: c.ImageURL}})
			}
		case llm.ContentToolCall:
			if c.ToolCall != nil {
				toolCalls = append(toolCalls, upstreamToolCall{
					ID:   c.ToolCall.ID,
					Type: "function",
					Function: upstreamToolCallFn{
						Name:      c.ToolCall.Name,
						Arguments: c.ToolCall.Arguments,
					},
				})
			}
		case llm.ContentToolResult:
			if c.ToolResult != nil {
				toolRes = c.ToolResult.Content
				isError = c.ToolResult.IsError
			}
		}
	}

	switch m.Role {
	case llm.RoleAssistant:
		msg := upstreamMessage{Role: "assistant", ToolCalls: toolCalls}
		msg.Content = joinText(textParts)
		if len(msg.ToolCalls) == 0 {
			msg.ToolCalls = nil
		} else {
			// 上游靠 id 把 tool 结果关联回调用，空 id 会导致匹配失败。
			for i := range msg.ToolCalls {
				if msg.ToolCalls[i].ID == "" {
					msg.ToolCalls[i].ID = "call_" + randomHex(12)
				}
			}
		}
		return msg

	case llm.RoleTool:
		// 上游不区分工具错误，退化成普通 tool 结果。
		_ = isError
		content := toolRes
		if content == "" {
			content = joinText(textParts)
		}
		return upstreamMessage{Role: "tool", Content: content, ToolCallID: m.ToolCallID}

	default: // RoleUser
		if hasImage {
			return upstreamMessage{Role: "user", Content: parts}
		}
		return upstreamMessage{Role: "user", Content: joinText(textParts)}
	}
}

func joinText(parts []string) string {
	out := ""
	for _, p := range parts {
		out += p
	}
	return out
}

// toolResultHasImage 判断工具结果块里是否含有图片。
func toolResultHasImage(blocks []llm.Content) bool {
	for _, b := range blocks {
		if b.Type == llm.ContentImage {
			return true
		}
	}
	return false
}

// convertTools 转换工具定义。
//
// 上游要求 parameters 是非空 dict 且必须含 type 字段，否则 400。
// 这里过滤掉不合规的定义，而不是把错误抛给上游。
//
// sanitize 时对描述同样脱敏：工具描述常含攻击术语（安全类工具的常规描述，
// 如 "exploit"、"SQL injection"），会被上游关键词审核误伤。
// 此前 SanitizeToolDescription 已实现却从未接线，工具描述成了漏网之鱼。
func convertTools(defs []llm.ToolDefinition, sanitize bool) []upstreamTool {
	out := make([]upstreamTool, 0, len(defs))
	for _, d := range defs {
		if d.Name == "" {
			continue
		}
		params := d.Parameters
		if len(params) == 0 {
			// 上游不接受空 parameters，补一个最小 object schema。
			params = map[string]any{"type": "object", "properties": map[string]any{}}
		}
		if _, ok := params["type"]; !ok {
			params = cloneWithType(params)
		}
		desc := d.Description
		if sanitize {
			desc = SanitizeToolDescription(desc)
		}
		out = append(out, upstreamTool{
			Type: "function",
			Function: upstreamToolFn{
				Name:        d.Name,
				Description: desc,
				Parameters:  params,
			},
		})
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func cloneWithType(params map[string]any) map[string]any {
	out := make(map[string]any, len(params)+1)
	for k, v := range params {
		out[k] = v
	}
	out["type"] = "object"
	return out
}

// normalizeToolChoice 把 IR 工具选择归一化成上游要求的 string。
//
// 上游 Go 后端的 tool_choice 是 string 类型，OpenAI 标准的 object 形式
// （{"type":"function","function":{"name":"X"}}）会触发 400。
func normalizeToolChoice(tc *llm.ToolChoice) any {
	switch tc.Mode {
	case llm.ToolChoiceNone:
		return "none"
	case llm.ToolChoiceRequired:
		return "required"
	case llm.ToolChoiceFunction:
		if tc.FunctionName != "" {
			return tc.FunctionName
		}
		return "required"
	default:
		return "auto"
	}
}

// convertMessages 把 IR 消息序列转换为上游消息序列。
//
// 关键点：一条 RoleTool 的 IR 消息可能携带**多个**工具结果——Claude Code 经常把
// 一批工具的返回合并放进同一条 user 消息里。而上游要求每个 tool_call 对应一条
// 独立的 tool 消息，因此这里必须展开成多条，否则会触发上游的
// "tool calls and tool results do not match"。
func convertMessages(msgs []llm.Message, sanitize bool) []upstreamMessage {
	out := make([]upstreamMessage, 0, len(msgs)+2)
	for _, m := range msgs {
		if m.Role != llm.RoleTool {
			out = append(out, convertMessage(m, sanitize))
			continue
		}
		results := make([]*llm.ToolResult, 0, 2)
		for _, c := range m.Content {
			if c.Type == llm.ContentToolResult && c.ToolResult != nil {
				results = append(results, c.ToolResult)
			}
		}
		if len(results) == 0 {
			// 没有结构化的工具结果块，退化为普通转换。
			out = append(out, convertMessage(m, sanitize))
			continue
		}
		for _, r := range results {
			content := r.Content
			if content == "" {
				content = "(工具无输出)"
			}
			msg := upstreamMessage{Role: "tool", ToolCallID: r.ToolCallID}
			// 仅当工具结果确实含图片时改用多模态数组形式；纯文本结果仍走字符串，
			// 避免改变既有行为、也避免上游对 tool 消息 content 的数组形式过敏。
			if len(r.Blocks) > 0 && toolResultHasImage(r.Blocks) {
				parts := make([]contentPart, 0, len(r.Blocks))
				for _, blk := range r.Blocks {
					switch blk.Type {
					case llm.ContentText:
						if blk.Text != "" {
							parts = append(parts, contentPart{Type: "text", Text: blk.Text})
						}
					case llm.ContentImage:
						if blk.ImageURL != "" {
							parts = append(parts, contentPart{Type: "image_url", ImageURL: &imageURLRef{URL: blk.ImageURL}})
						}
					}
				}
				msg.Content = parts
			} else {
				msg.Content = content
			}
			out = append(out, msg)
		}
	}
	return out
}

// marshalJSON 便于调试与测试。
func (r upstreamRequest) marshalJSON() ([]byte, error) { return json.Marshal(r) }
