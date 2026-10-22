package workbuddy

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
)

type conversationMetadata struct {
	id, requestID, messageID string
}

func (a *Adapter) newConversationMetadata(sessionKey string) conversationMetadata {
	id := randomHex(32)
	if sessionKey != "" {
		// 不发送包含客户端身份信息的原始路由键；按账号和区域隔离，
		// 也不使用每轮增长的消息列表，以免破坏多轮会话的上游缓存亲和。
		seed, _ := json.Marshal([4]string{"Agent2API.workbuddy.conversation.v1", a.cfg.Platform, a.AccountUID(), sessionKey})
		digest := sha256.Sum256(seed)
		id = hex.EncodeToString(digest[:16])
	}
	return conversationMetadata{id: id, requestID: randomHex(32), messageID: randomHex(32)}
}

func (m conversationMetadata) apply(headers http.Header) {
	// 国际版聊天接口会以 11128 拒绝缺少这些会话字段的请求。
	// 无显式会话的单次 API 调用也必须携带完整字段。
	headers.Set("X-Conversation-ID", m.id)
	headers.Set("X-Conversation-Request-ID", m.requestID)
	headers.Set("X-Conversation-Message-ID", m.messageID)
}
