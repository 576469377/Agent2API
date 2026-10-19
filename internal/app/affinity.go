package app

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"strings"
)

// 会话亲和（session affinity）。
//
// 动机（按重要度）：
//  1. 长上下文会话（如 Claude Code 的 88k token）若每轮落到不同账号，
//     上游要重复处理全部输入——实测同一会话三轮请求落到两个账号，
//     每轮耗时稳定在 6.6~7s。
//  2. 同一会话固定在同一账号，模型行为与限流状态更可预测。
//
// 亲和避免无故切换账号；缓存是否命中仍以该次上游 usage 为准，
// 不能仅凭响应耗时或没有上报缓存字段就断定上游不支持缓存。

// sessionKeyOf 提取会话路由键。
//
// 优先级：
//  1. body.session_id，然后显式会话请求头：session_id / X-Session-Id / Session-Id
//     ——客户端明说「这是同一个会话」时最可信（sub2api 也是用 session_id 头
//     做粘性会话）。注意 Nginx 默认**丢弃带下划线的请求头**，自建反代要
//     `underscores_in_headers on;` 才能透传 session_id。
//  2. prompt_cache_key（显式缓存路由标识）
//  3. Anthropic metadata.user_id，然后 OpenAI user 字段
//  4. 兜底：系统提示 + 首条 user 消息的哈希——它们在会话内稳定。
//     （完整消息列表逐轮增长，直接哈希会每轮变化，绝不能用。）
//
// body 是原始请求体（避免依赖具体协议结构）；都提取不到返回空串，
// 该请求退化为普通调度。
func sessionKeyOf(body []byte, h http.Header) string {
	var probe struct {
		SessionID      string `json:"session_id"`
		PromptCacheKey string `json:"prompt_cache_key"`
		Metadata       struct {
			UserID string `json:"user_id"`
		} `json:"metadata"`
		User         string            `json:"user"`
		System       json.RawMessage   `json:"system"`
		Instructions json.RawMessage   `json:"instructions"`
		Input        json.RawMessage   `json:"input"`
		Messages     []affinityMessage `json:"messages"`
	}
	err := json.Unmarshal(body, &probe)
	if strings.TrimSpace(probe.SessionID) != "" {
		return "s:" + probe.SessionID
	}
	for _, name := range []string{"session_id", "X-Session-Id", "Session-Id"} {
		if v := strings.TrimSpace(h.Get(name)); v != "" {
			return "s:" + v
		}
	}
	if err != nil {
		return ""
	}
	if probe.PromptCacheKey != "" {
		return "p:" + probe.PromptCacheKey
	}
	if probe.Metadata.UserID != "" {
		return "u:" + probe.Metadata.UserID
	}
	if probe.User != "" {
		return "u:" + probe.User
	}
	// Fallback covers Messages, Chat Completions and Responses. Hash framed
	// values rather than a growing transcript; no conversation means no binding.
	parts := []json.RawMessage{}
	for _, system := range []json.RawMessage{probe.System, probe.Instructions} {
		if meaningfulAffinityContent(system) {
			parts = append(parts, system)
		}
	}
	messages := probe.Messages
	var input string
	var firstUser json.RawMessage
	if len(messages) == 0 && len(probe.Input) > 0 {
		if json.Unmarshal(probe.Input, &input) == nil && input != "" {
			firstUser = probe.Input
		} else {
			_ = json.Unmarshal(probe.Input, &messages)
		}
	}
	for _, m := range messages {
		if !meaningfulAffinityContent(m.Content) {
			continue
		}
		if m.Role == "system" || m.Role == "developer" {
			parts = append(parts, m.Content)
		}
		if m.Role == "user" {
			firstUser = m.Content
			break
		}
	}
	if !meaningfulAffinityContent(firstUser) {
		return ""
	}
	parts = append(parts, firstUser)
	seed, err := json.Marshal(parts)
	if err != nil {
		return ""
	}
	digest := sha256.Sum256(seed)
	return "h:" + hex.EncodeToString(digest[:16])
}

type affinityMessage struct {
	Role    string          `json:"role"`
	Content json.RawMessage `json:"content"`
}

func meaningfulAffinityContent(raw json.RawMessage) bool {
	switch strings.TrimSpace(string(raw)) {
	case "", "null", `""`, "[]":
		return false
	default:
		return true
	}
}
