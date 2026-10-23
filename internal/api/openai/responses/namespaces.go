package responses

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/576469377/Agent2API/internal/llm"
)

type toolName struct{ Namespace, Name string }

// namespaceToolName 为只接受扁平工具名的上游生成稳定且短于 64 字符的名称。
// 名称不依赖工具顺序，后续工具结果和 Kiro 原会话可以继续使用同一标识。
func namespaceToolName(namespace, name string) string {
	sum := sha256.Sum256([]byte(namespace + "\x00" + name))
	return "a2api_ns_" + hex.EncodeToString(sum[:20])
}

func invalidTools(message string) error {
	return &llm.Failure{Code: "invalid_tools", Message: message, ClientFixable: true}
}

func decodeTools(raw []rawTool) ([]llm.ToolDefinition, map[string]toolName, error) {
	var tools []llm.ToolDefinition
	names := map[string]toolName{}
	seen := map[string]bool{}
	add := func(t rawTool, namespace, description string) error {
		if t.Name == "" {
			return invalidTools("工具名称不能为空")
		}
		name := t.Name
		if namespace != "" {
			name = namespaceToolName(namespace, name)
			if t.Type != "function" && t.Type != "" {
				return invalidTools("命名空间内暂仅支持 function 工具，请勿将自由文本或嵌套命名空间当作函数")
			}
			names[name] = toolName{Namespace: namespace, Name: t.Name}
			t.Description = fmt.Sprintf("Namespace %s: %s\n%s", namespace, description, t.Description)
		} else {
			names[name] = toolName{Name: t.Name}
		}
		if seen[name] {
			return invalidTools("工具名称重复或与命名空间的内部标识冲突")
		}
		seen[name] = true
		tools = append(tools, llm.ToolDefinition{Name: name, Description: t.Description, Parameters: t.Parameters, CacheControl: t.CacheControl})
		return nil
	}
	for _, t := range raw {
		if t.Type == "namespace" {
			if t.Name == "" || len(t.Tools) == 0 {
				return nil, nil, invalidTools("命名空间必须包含名称和可调用函数")
			}
			for _, child := range t.Tools {
				if err := add(child, t.Name, t.Description); err != nil {
					return nil, nil, err
				}
			}
		} else if t.Name != "" {
			if err := add(t, "", ""); err != nil {
				return nil, nil, err
			}
		}
	}
	return tools, names, nil
}

// Bind 为单个 HTTP 请求保留工具名称映射，不能在并发请求间共享或覆盖映射。
// 请求和历史送往上游时使用扁平标识，流式及非流式输出均恢复 name + namespace。
func Bind(body []byte) (Protocol, error) {
	var r request
	if err := json.Unmarshal(body, &r); err != nil {
		return Protocol{}, &llm.Failure{Code: "invalid_request", Message: "解析请求体失败", Cause: err, ClientFixable: true}
	}
	_, names, err := decodeTools(r.Tools)
	if err != nil {
		return Protocol{}, err
	}
	var history []rawInputMessage
	// 字符串 input 没有历史工具调用；输入错误由 DecodeRequest 原有检查处理。
	if json.Unmarshal(r.Input, &history) == nil {
		for _, item := range history {
			if item.Type == "function_call" && item.Namespace != "" {
				names[namespaceToolName(item.Namespace, item.Name)] = toolName{Namespace: item.Namespace, Name: item.Name}
			}
		}
	}
	return Protocol{toolNames: names}, nil
}

func toolItem(names map[string]toolName, id, callID, name, arguments, status string) map[string]any {
	item := map[string]any{"id": id, "type": "function_call", "call_id": callID, "name": name, "arguments": arguments, "status": status}
	original, ok := names[name]
	if !ok {
		// 部分模型会把描述中的命名空间再加到内部别名前。只接受当前请求中
		// 已登记的完整 namespace.alias 配对，不能猜测叶子函数或任意前缀。
		if split := strings.LastIndexByte(name, '.'); split >= 0 {
			if known, exists := names[name[split+1:]]; exists && known.Namespace == name[:split] {
				original, ok = known, true
			}
		}
	}
	if ok {
		item["name"] = original.Name
		if original.Namespace != "" {
			item["namespace"] = original.Namespace
		}
	}
	return item
}
