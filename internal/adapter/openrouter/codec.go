package openrouter

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/576469377/Agent2API/internal/llm"
)

// ChatRequest 复用 OpenAI Chat 的历史、工具和缓存转换；平台价格策略由调用方添加。
func ChatRequest(req llm.RequestMessages) map[string]any {
	body := buildRequest(req)
	delete(body, "provider")
	return body
}

// ChatError 只提取并脱敏错误消息，不将上游原始请求或元数据带回客户端。
func ChatError(raw []byte, fallback string) string {
	var wire struct {
		Error *apiError `json:"error"`
	}
	if json.Unmarshal(raw, &wire) == nil && wire.Error != nil {
		return wire.Error.summary(fallback)
	}
	return fallback
}

// ChatStream 让使用同一 SSE 契约的平台共用工具增量、取消和用量解析。
func ChatStream(resp *http.Response, model string, cancel context.CancelFunc, idle time.Duration, platform, account string) llm.ResponseStream {
	return &namedChatStream{ResponseStream: newStream(resp, model, cancel, idle), platform: platform, account: account}
}

type namedChatStream struct {
	llm.ResponseStream
	platform, account string
}

func (s *namedChatStream) AccountLabel() string { return s.account }

func (s *namedChatStream) Close() error {
	if closer, ok := s.ResponseStream.(io.Closer); ok {
		return closer.Close()
	}
	return nil
}

func (s *namedChatStream) rename(err error) error {
	if f, ok := err.(*llm.Failure); ok {
		copy := *f
		copy.Message = strings.ReplaceAll(copy.Message, "OpenRouter", s.platform)
		return &copy
	}
	return err
}

func (s *namedChatStream) Recv(ctx context.Context) (llm.ResponseEvent, error) {
	event, err := s.ResponseStream.Recv(ctx)
	if event.Error != nil {
		event.Error = llm.Wrap(s.rename(event.Error))
	}
	return event, s.rename(err)
}
