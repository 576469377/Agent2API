package openrouter

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"

	"github.com/576469377/Agent2API/internal/llm"
)

func content(blocks []llm.Content) any {
	parts := []map[string]any{}
	var text strings.Builder
	image := false
	cache := false
	for _, c := range blocks {
		switch c.Type {
		case llm.ContentText:
			text.WriteString(c.Text)
			parts = append(parts, map[string]any{"type": "text", "text": c.Text})
		case llm.ContentImage:
			image = true
			parts = append(parts, map[string]any{"type": "image_url", "image_url": map[string]string{"url": c.ImageURL}})
		}
		if c.CacheControl != nil && (c.Type == llm.ContentText || c.Type == llm.ContentImage) {
			parts[len(parts)-1]["cache_control"] = c.CacheControl
			cache = true
		}
	}
	if image || cache {
		return parts
	}
	return text.String()
}

func buildRequest(req llm.RequestMessages) map[string]any {
	messages := []map[string]any{}
	if len(req.SystemBlocks) > 0 {
		messages = append(messages, map[string]any{"role": "system", "content": content(req.SystemBlocks)})
	} else if req.SystemPrompt != "" {
		messages = append(messages, map[string]any{"role": "system", "content": req.SystemPrompt})
	}
	for _, m := range req.Messages {
		role := "user"
		if m.Role == llm.RoleAssistant {
			role = "assistant"
		}
		if m.Role == llm.RoleTool {
			role = "tool"
		}
		msg := map[string]any{"role": role, "content": content(m.Content)}
		calls := []map[string]any{}
		hasResult := false
		for _, c := range m.Content {
			if c.Type == llm.ContentToolCall && c.ToolCall != nil {
				tc := c.ToolCall
				calls = append(calls, map[string]any{"id": tc.ID, "type": "function", "function": map[string]any{"name": tc.Name, "arguments": tc.Arguments}})
			}
			if c.Type == llm.ContentToolResult && c.ToolResult != nil {
				r := c.ToolResult
				body := any(r.Content)
				if len(r.Blocks) > 0 {
					body = content(r.Blocks)
				}
				if c.CacheControl != nil {
					if parts, ok := body.([]map[string]any); ok && len(parts) > 0 {
						parts[len(parts)-1]["cache_control"] = c.CacheControl
					} else if text, ok := body.(string); ok {
						body = []map[string]any{{"type": "text", "text": text, "cache_control": c.CacheControl}}
					}
				}
				id := r.ToolCallID
				if id == "" {
					id = m.ToolCallID
				}
				messages = append(messages, map[string]any{"role": "tool", "tool_call_id": id, "content": body})
				hasResult = true
			}
		}
		if len(calls) > 0 {
			msg["tool_calls"] = calls
		}
		if role == "tool" {
			msg["tool_call_id"] = m.ToolCallID
			if hasResult {
				continue
			}
		}
		if hasResult && msg["content"] == "" && len(calls) == 0 {
			continue
		}
		messages = append(messages, msg)
	}
	out := map[string]any{
		"model": req.Model, "messages": messages, "stream": true, "stream_options": map[string]any{"include_usage": true},
		// This upstream constraint also protects requests when catalogue prices change.
		"provider": map[string]any{"max_price": map[string]int{"prompt": 0, "completion": 0, "request": 0, "image": 0}},
	}
	if req.CacheControl != nil {
		out["cache_control"] = req.CacheControl
	}
	if req.PromptCacheKey != "" {
		out["prompt_cache_key"] = req.PromptCacheKey
	}
	if req.SessionKey != "" {
		// Stable routing without forwarding local user identifiers to the provider.
		sum := sha256.Sum256([]byte(req.SessionKey))
		out["session_id"] = "agent2api_" + hex.EncodeToString(sum[:])
	}
	if req.MaxTokens != nil {
		out["max_tokens"] = *req.MaxTokens
	}
	if req.Temperature != nil {
		out["temperature"] = *req.Temperature
	}
	if req.TopP != nil {
		out["top_p"] = *req.TopP
	}
	if req.TopK != nil {
		out["top_k"] = *req.TopK
	}
	if req.Seed != nil {
		out["seed"] = *req.Seed
	}
	if len(req.Stop) > 0 {
		out["stop"] = req.Stop
	}
	if req.JSONMode {
		out["response_format"] = map[string]string{"type": "json_object"}
	}
	if req.ReasoningEffort != "" {
		out["reasoning"] = map[string]any{"effort": req.ReasoningEffort}
	}
	if len(req.Tools) > 0 {
		tools := []map[string]any{}
		for _, t := range req.Tools {
			p := t.Parameters
			if p == nil {
				p = map[string]any{"type": "object", "properties": map[string]any{}}
			}
			tool := map[string]any{"type": "function", "function": map[string]any{"name": t.Name, "description": t.Description, "parameters": p}}
			if t.CacheControl != nil {
				tool["cache_control"] = t.CacheControl
			}
			tools = append(tools, tool)
		}
		out["tools"] = tools
	}
	if req.ToolChoice != nil {
		if req.ToolChoice.Mode == llm.ToolChoiceFunction {
			out["tool_choice"] = map[string]any{"type": "function", "function": map[string]string{"name": req.ToolChoice.FunctionName}}
		} else {
			out["tool_choice"] = string(req.ToolChoice.Mode)
		}
	}
	return out
}
