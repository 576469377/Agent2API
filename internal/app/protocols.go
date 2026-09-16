package app

import (
	"github.com/576469377/Agent2API/internal/api/anthropic/messages"
	"github.com/576469377/Agent2API/internal/api/common"
	"github.com/576469377/Agent2API/internal/api/openai/chat"
	"github.com/576469377/Agent2API/internal/api/openai/responses"
	"github.com/576469377/Agent2API/internal/llm"
)

// 本文件把三个具体协议包适配到 app 的 Protocol 接口。
//
// Go 的接口不满足协变返回，因此需要这层薄封装；它们不含任何业务逻辑，
// 只是把具体类型的 *StreamEncoder 提升为 StreamEncoder 接口。

// ───────────────────────── Chat Completions ─────────────────────────

type chatProtocol struct{}

func (chatProtocol) Name() string { return "openai-chat" }

func (chatProtocol) DecodeRequest(body []byte) (llm.RequestMessages, error) {
	return chat.Protocol{}.DecodeRequest(body)
}

func (chatProtocol) NewStreamEncoder(model string, includeUsage bool) StreamEncoder {
	return chat.Protocol{}.NewStreamEncoder(model, includeUsage)
}

func (chatProtocol) EncodeFinal(msg *llm.AssistantMessage, model string) ([]byte, error) {
	return chat.Protocol{}.EncodeFinal(msg, model)
}

func (p chatProtocol) AppendSSE(dst []byte, name string, data []byte) []byte {
	return chat.Protocol{}.AppendSSE(dst, name, data)
}

func (chatProtocol) StreamErrorEvents() bool { return chat.Protocol{}.StreamErrorEvents() }

// ───────────────────────── Responses ─────────────────────────

type responsesProtocol struct{}

func (responsesProtocol) Name() string { return "openai-responses" }

func (responsesProtocol) DecodeRequest(body []byte) (llm.RequestMessages, error) {
	return responses.Protocol{}.DecodeRequest(body)
}

func (responsesProtocol) NewStreamEncoder(model string, includeUsage bool) StreamEncoder {
	return responses.Protocol{}.NewStreamEncoder(model, includeUsage)
}

func (responsesProtocol) EncodeFinal(msg *llm.AssistantMessage, model string) ([]byte, error) {
	return responses.Protocol{}.EncodeFinal(msg, model)
}

func (responsesProtocol) AppendSSE(dst []byte, name string, data []byte) []byte {
	return responses.Protocol{}.AppendSSE(dst, name, data)
}

func (responsesProtocol) StreamErrorEvents() bool { return responses.Protocol{}.StreamErrorEvents() }

// ───────────────────────── Anthropic Messages ─────────────────────────

type anthropicProtocol struct{}

func (anthropicProtocol) Name() string { return "anthropic-messages" }

func (anthropicProtocol) DecodeRequest(body []byte) (llm.RequestMessages, error) {
	return messages.Protocol{}.DecodeRequest(body)
}

func (anthropicProtocol) NewStreamEncoder(model string, includeUsage bool) StreamEncoder {
	return messages.Protocol{}.NewStreamEncoder(model, includeUsage)
}

func (anthropicProtocol) EncodeFinal(msg *llm.AssistantMessage, model string) ([]byte, error) {
	return messages.Protocol{}.EncodeFinal(msg, model)
}

func (anthropicProtocol) AppendSSE(dst []byte, name string, data []byte) []byte {
	return messages.Protocol{}.AppendSSE(dst, name, data)
}

func (anthropicProtocol) StreamErrorEvents() bool { return messages.Protocol{}.StreamErrorEvents() }

// 编译期断言：确保三个封装都满足 Protocol。
var (
	_ Protocol = chatProtocol{}
	_ Protocol = responsesProtocol{}
	_ Protocol = anthropicProtocol{}
	_ common.SSEEvent
)
