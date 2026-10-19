package cursor

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"strings"

	"github.com/576469377/Agent2API/internal/llm"
)

func requestError(message string) error {
	return &llm.Failure{Code: "cursor_unsupported_request", Message: message, ClientFixable: true}
}
func newID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b[:])
}

// Match Cursor's MCP name normalization, including its 64-character limit.
func toolName(name string) string {
	var b strings.Builder
	for _, c := range "agent2api-" + name {
		if c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '_' || c == '-' {
			b.WriteRune(c)
		} else {
			b.WriteByte('_')
		}
	}
	name = b.String()
	if len(name) > 64 {
		sum := sha256.Sum256([]byte(name))
		name = name[:57] + hex.EncodeToString(sum[:])[:7]
	}
	return name
}

func buildRequest(req llm.RequestMessages) (map[string]any, []map[string]any, map[string]string, error) {
	if req.Temperature != nil || req.TopP != nil || req.TopK != nil || req.Seed != nil || req.JSONMode || len(req.Stop) > 0 || req.ReasoningEffort != "" {
		return nil, nil, nil, requestError("Cursor Agent 不提供 temperature / top_p / top_k / seed / stop / JSON 模式或 reasoning_effort 参数，请移除这些参数")
	}
	if req.MaxTokens != nil && *req.MaxTokens <= 0 {
		return nil, nil, nil, requestError("max_tokens 必须为正数")
	}
	tools := []map[string]any{}
	names := map[string]bool{}
	for _, t := range req.Tools {
		if t.Name == "" || names[t.Name] {
			return nil, nil, nil, requestError("工具名不能为空或重复")
		}
		names[t.Name] = true
		if req.ToolChoice != nil && (req.ToolChoice.Mode == llm.ToolChoiceNone || (req.ToolChoice.Mode == llm.ToolChoiceFunction && t.Name != req.ToolChoice.FunctionName)) {
			continue
		}
		schema := t.Parameters
		if schema == nil {
			schema = map[string]any{"type": "object", "properties": map[string]any{}}
		}
		tools = append(tools, map[string]any{"name": toolName(t.Name), "toolName": t.Name, "providerIdentifier": "agent2api", "description": t.Description, "inputSchema": schema})
	}
	if req.ToolChoice != nil && (req.ToolChoice.Mode == llm.ToolChoiceRequired || req.ToolChoice.Mode == llm.ToolChoiceFunction) && len(tools) == 0 {
		return nil, nil, nil, requestError("tool_choice 指定的工具不存在")
	}
	if len(req.Messages) == 0 {
		return nil, nil, nil, requestError("Cursor 请求需要消息")
	}
	var roots []map[string]any
	callNames := map[string]string{}
	for _, m := range req.Messages {
		for _, c := range m.Content {
			if c.Type == llm.ContentToolCall && c.ToolCall != nil {
				callNames[c.ToolCall.ID] = toolName(c.ToolCall.Name)
			}
		}
	}
	lastText := ""
	last := len(req.Messages) - 1
	for i, m := range req.Messages {
		var text strings.Builder
		parts := []map[string]any{}
		results := []map[string]any{}
		for _, c := range m.Content {
			switch c.Type {
			case llm.ContentText:
				text.WriteString(c.Text)
				parts = append(parts, map[string]any{"type": "text", "text": c.Text})
			case llm.ContentThinking: // Retain visible history without fabricating signed reasoning.
			case llm.ContentToolCall:
				if c.ToolCall == nil || m.Role != llm.RoleAssistant {
					return nil, nil, nil, requestError("工具调用内容缺失")
				}
				var args map[string]any
				if json.Unmarshal([]byte(c.ToolCall.Arguments), &args) != nil || args == nil {
					return nil, nil, nil, requestError("工具参数必须是 JSON 对象")
				}
				parts = append(parts, map[string]any{"type": "tool-call", "toolCallId": c.ToolCall.ID, "toolName": toolName(c.ToolCall.Name), "args": args})
			case llm.ContentToolResult:
				if c.ToolResult == nil {
					return nil, nil, nil, requestError("工具结果内容缺失")
				}
				r := c.ToolResult
				for _, b := range r.Blocks {
					if b.Type != llm.ContentText {
						return nil, nil, nil, requestError("Cursor 当前不支持图片工具结果")
					}
				}
				result := r.Content
				if len(r.Blocks) > 0 {
					var b strings.Builder
					for _, v := range r.Blocks {
						b.WriteString(v.Text)
					}
					result = b.String()
				}
				id := r.ToolCallID
				if id == "" {
					id = m.ToolCallID
				}
				if callNames[id] == "" {
					return nil, nil, nil, requestError("工具结果缺少对应的历史调用")
				}
				results = append(results, map[string]any{"type": "tool-result", "toolCallId": id, "toolName": callNames[id], "result": result, "isError": r.IsError})
			default:
				return nil, nil, nil, requestError("Cursor Composer 2.5 当前仅接入文本和客户端工具调用")
			}
		}
		if m.Role == llm.RoleTool && len(results) == 0 {
			if callNames[m.ToolCallID] == "" {
				return nil, nil, nil, requestError("工具结果缺少对应的历史调用")
			}
			results = append(results, map[string]any{"type": "tool-result", "toolCallId": m.ToolCallID, "toolName": callNames[m.ToolCallID], "result": text.String()})
			parts = nil
		}
		if len(results) > 0 {
			roots = append(roots, map[string]any{"role": "tool", "content": results})
		}
		if i == last && m.Role == llm.RoleUser && len(results) == 0 {
			lastText = text.String()
			continue
		}
		role := "user"
		if m.Role == llm.RoleAssistant {
			role = "assistant"
		}
		// Cursor's root prompt uses AI SDK CoreMessage blocks. Keeping calls and
		// results structured prevents the model from copying pseudo-tool markup
		// into ordinary text on the next turn. Nothing is executed here.
		if len(parts) > 0 {
			roots = append(roots, map[string]any{"role": role, "content": parts})
		}
	}
	if lastText == "" {
		lastText = "Continue the conversation using the supplied history and tool results."
	}
	blobs := map[string]string{}
	ids := []string{}
	for _, root := range roots {
		raw, _ := json.Marshal(root)
		sum := sha256.Sum256(raw)
		id := base64.StdEncoding.EncodeToString(sum[:])
		ids = append(ids, id)
		blobs[id] = base64.StdEncoding.EncodeToString(raw)
	}
	instructions := req.SystemPrompt
	if len(tools) > 0 {
		instructions += "\nOnly the supplied agent2api MCP tools are available to this API client. Use their exact input schemas for file or shell operations; host tools are unavailable. Invoke MCP tools through the tool interface. Do not print tool calls or <tool_call> markup as response text."
	}
	if req.ToolChoice != nil && (req.ToolChoice.Mode == llm.ToolChoiceRequired || req.ToolChoice.Mode == llm.ToolChoiceFunction) {
		instructions += "\nUse one of the supplied agent2api tools for this response."
	}
	if instructions != "" {
		// Cursor retains its own agent system prompt and ignores legacy rule
		// fields on this route. Explicitly carry client instructions as context.
		lastText = "<client_instructions>\n" + instructions + "\n</client_instructions>\n\n" + lastText
	}
	context := map[string]any{"env": map[string]any{"osVersion": "API client"}, "tools": tools, "webSearchEnabled": false, "webFetchEnabled": false, "searchConversationsEnabled": false, "sendMessageEnabled": false}
	if len(tools) > 0 {
		descriptors := []map[string]any{}
		for _, t := range tools {
			descriptors = append(descriptors, map[string]any{"toolName": t["toolName"], "description": t["description"], "inputSchema": t["inputSchema"]})
		}
		// Make schema discovery self-contained. The desktop's filesystem MCP
		// mode otherwise asks API clients to read nonexistent descriptor files.
		context["mcpInfoComplete"] = true
		context["mcpFileSystemInfoComplete"] = true
		context["mcpFileSystemOptions"] = map[string]any{"enabled": false}
		context["mcpMetaToolOptions"] = map[string]any{"enabled": true, "mcpDescriptors": []map[string]any{{"serverName": "agent2api", "serverIdentifier": "agent2api", "tools": descriptors}}}
	}
	mode := 2
	if len(tools) > 0 {
		mode = 1
	}
	payload := map[string]any{
		"conversationState": map[string]any{"rootPromptMessagesJson": ids},
		"action":            map[string]any{"userMessageAction": map[string]any{"userMessage": map[string]any{"text": lastText, "messageId": newID(), "mode": mode}, "requestContext": context}},
		"modelDetails":      map[string]any{"modelId": verifiedModel, "maxMode": false}, "requestedModel": map[string]any{"modelId": verifiedModel, "maxMode": false, "builtInModel": true},
		"mcpTools": map[string]any{"mcpTools": tools}, "conversationId": newID(),
	}
	return payload, tools, blobs, nil
}
