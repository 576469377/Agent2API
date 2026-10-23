package trae

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"

	"github.com/576469377/Agent2API/internal/llm"
)

func requestFailure(message string) *llm.Failure {
	return &llm.Failure{Code: "trae_unsupported_request", Message: message, ClientFixable: true}
}

func buildRequest(r llm.RequestMessages, m catalogModel) (map[string]any, error) {
	if r.JSONMode || len(r.Stop) > 0 {
		return nil, requestFailure("TRAE 当前不支持 JSON 模式或 stop 停止序列")
	}
	var id [16]byte
	if r.SessionKey != "" {
		sum := sha256.Sum256([]byte(r.SessionKey + "\x00" + m.Info.ID))
		copy(id[:], sum[:16])
	} else {
		if _, err := rand.Read(id[:]); err != nil {
			return nil, err
		}
	}
	id[6] = id[6]&0x0f | 0x40
	id[8] = id[8]&0x3f | 0x80
	x := hex.EncodeToString(id[:])
	sid := fmt.Sprintf("%s-%s-%s-%s-%s", x[:8], x[8:12], x[12:16], x[16:20], x[20:])
	messages := []map[string]any{}
	parts := func(blocks []llm.Content) ([]map[string]any, error) {
		out := []map[string]any{}
		for _, b := range blocks {
			switch b.Type {
			case llm.ContentImage:
				return nil, requestFailure("TRAE 当前仅验证文本和工具，暂未开放图片输入")
			case llm.ContentText:
				out = append(out, map[string]any{"type": "text", "text": b.Text})
			}
		}
		return out, nil
	}
	if len(r.SystemBlocks) > 0 {
		p, err := parts(r.SystemBlocks)
		if err != nil {
			return nil, err
		}
		messages = append(messages, map[string]any{"role": "system", "content": p})
	} else if r.SystemPrompt != "" {
		messages = append(messages, map[string]any{"role": "system", "content": []map[string]any{{"type": "text", "text": r.SystemPrompt}}})
	}
	for _, msg := range r.Messages {
		role := "user"
		if msg.Role == llm.RoleAssistant {
			role = "assistant"
		}
		if msg.Role == llm.RoleTool {
			role = "tool"
		}
		content := []llm.Content{}
		calls := []map[string]any{}
		flush := func() error {
			if len(content) == 0 && len(calls) == 0 {
				return nil
			}
			p, err := parts(content)
			if err != nil {
				return err
			}
			item := map[string]any{"role": role, "content": p}
			if len(calls) > 0 {
				item["tool_calls"] = calls
			}
			if role == "tool" {
				if msg.ToolCallID == "" {
					return requestFailure("工具结果缺少 tool_call_id")
				}
				item["tool_call_id"] = msg.ToolCallID
			}
			messages = append(messages, item)
			content = nil
			calls = nil
			return nil
		}
		for _, b := range msg.Content {
			switch b.Type {
			case llm.ContentToolCall:
				if b.ToolCall == nil || b.ToolCall.ID == "" || b.ToolCall.Name == "" {
					return nil, requestFailure("工具调用缺少 id 或名称")
				}
				t := b.ToolCall
				arguments := t.Arguments
				if arguments == "" {
					arguments = "{}"
				}
				if !json.Valid([]byte(arguments)) {
					return nil, requestFailure("工具参数必须为 JSON")
				}
				calls = append(calls, map[string]any{"id": t.ID, "type": "function", "function_call": map[string]any{"name": t.Name, "arguments": arguments}})
			case llm.ContentToolResult:
				if err := flush(); err != nil {
					return nil, err
				}
				if b.ToolResult == nil {
					return nil, requestFailure("工具结果为空")
				}
				t := b.ToolResult
				id := t.ToolCallID
				if id == "" {
					id = msg.ToolCallID
				}
				if id == "" {
					return nil, requestFailure("工具结果缺少 tool_call_id")
				}
				blocks := t.Blocks
				if len(blocks) == 0 {
					blocks = []llm.Content{{Type: llm.ContentText, Text: t.Content}}
				}
				p, err := parts(blocks)
				if err != nil {
					return nil, err
				}
				if t.IsError {
					p = append([]map[string]any{{"type": "text", "text": "Client tool failed:\n"}}, p...)
				}
				messages = append(messages, map[string]any{"role": "tool", "tool_call_id": id, "content": p})
			default:
				content = append(content, b)
			}
		}
		if err := flush(); err != nil {
			return nil, err
		}
	}
	if len(messages) == 0 {
		return nil, requestFailure("消息不能为空")
	}
	body := map[string]any{"config_name": m.Info.ID, "model_name": m.WireName, "conversation_id": sid, "session_id": sid, "stream": true, "raw_chat_function": "chat_v3", "config_source": m.Source, "messages": messages}
	tools := []map[string]any{}
	names := map[string]bool{}
	for _, t := range r.Tools {
		if t.Name == "" || names[t.Name] {
			return nil, requestFailure("工具名称不能为空或重复")
		}
		names[t.Name] = true
		p := t.Parameters
		if p == nil {
			p = map[string]any{"type": "object", "properties": map[string]any{}}
		}
		encoded, err := json.Marshal(p)
		if err != nil {
			return nil, requestFailure("工具 JSON Schema 无法编码")
		}
		// 真实网关要求 schema 是 JSON 字符串；对象形式会直接返回 HTTP 400。
		tools = append(tools, map[string]any{"type": "function", "function": map[string]any{"name": t.Name, "description": t.Description, "parameters": string(encoded)}})
	}
	if len(tools) > 0 {
		body["tools"] = tools
	}
	if r.ToolChoice != nil {
		choice := string(r.ToolChoice.Mode)
		if r.ToolChoice.Mode == llm.ToolChoiceFunction {
			choice = r.ToolChoice.FunctionName
			if !names[choice] {
				return nil, requestFailure("指定的工具不在 tools 中")
			}
		}
		switch r.ToolChoice.Mode {
		case llm.ToolChoiceAuto, llm.ToolChoiceNone, llm.ToolChoiceRequired, llm.ToolChoiceFunction:
			body["tool_choice"] = choice
		default:
			return nil, requestFailure("工具选择方式无效")
		}
	}
	// 实测 max_tokens=4 仍返回 79 completion tokens。输出预算和采样由平台控制，
	// 不把这些字段转发后宣称生效；目录与用户指南明确列出这一限制。
	return body, nil
}
