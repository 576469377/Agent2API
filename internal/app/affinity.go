package app

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
)

// 会话亲和（session affinity）。
//
// 动机（按重要度）：
//  1. 长上下文会话（如 Claude Code 的 88k token）若每轮落到不同账号，
//     上游要重复处理全部输入——实测同一会话三轮请求落到两个账号，
//     每轮耗时稳定在 6.6~7s。
//  2. 同一会话固定在同一账号，模型行为与限流状态更可预测。
//
// 注意：实测 WorkBuddy 上游**没有可观测的 prompt cache**（同前缀三次请求
// 延迟无递减、usage 无 cached_tokens），所以这里不做「保缓存」的承诺；
// 亲和的价值在重复处理成本与一致性。上游若未来加入缓存，此机制自动受益。

// sessionKeyOf 从请求体提取会话路由键。
//
// 优先级：
//  1. Anthropic: metadata.user_id（Claude Code 每轮都发）
//  2. OpenAI: user 字段
//  3. 兜底：系统提示 + 首条 user 消息的哈希——它们在会话内稳定。
//     （完整消息列表逐轮增长，直接哈希会每轮变化，绝不能用。）
//
// body 是原始请求体（避免依赖具体协议结构）；解析失败返回空串，
// 该请求退化为普通调度。
func sessionKeyOf(body []byte) string {
	var probe struct {
		Metadata struct {
			UserID string `json:"user_id"`
		} `json:"metadata"`
		User     string          `json:"user"`
		System   json.RawMessage `json:"system"`
		Messages []struct {
			Role    string          `json:"role"`
			Content json.RawMessage `json:"content"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(body, &probe); err != nil {
		return ""
	}
	if probe.Metadata.UserID != "" {
		return "u:" + probe.Metadata.UserID
	}
	if probe.User != "" {
		return "u:" + probe.User
	}
	// 兜底：系统提示 + 首条 user 消息。
	h := sha256.New()
	if len(probe.System) > 0 {
		h.Write(probe.System)
	}
	for _, m := range probe.Messages {
		if m.Role == "user" {
			h.Write(m.Content)
			break
		}
	}
	sum := h.Sum(nil)
	if len(sum) == 0 {
		return ""
	}
	return "h:" + hex.EncodeToString(sum[:8])
}
