// Package common 提供下游协议共用的构建块：SSE 写出与错误载荷。
//
// 三份协议编码器共用这里的实现，避免错误字段与 SSE 格式在各处漂移。
package common

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/576469377/Agent2API/internal/llm"
)

// SSEEvent 是一条待写出的 SSE 帧。
//
// Name 为空表示 Chat Completions 的 data-only 风格；Name 为 "[DONE]" 表示终止帧。
type SSEEvent struct {
	Name string
	Data []byte
}

// AppendSSE 把一帧追加到 dst，返回新的切片。
//
// 让调用方持有 dst 的所有权，便于批次合并时复用缓冲，避免每帧分配。
func AppendSSE(dst []byte, name string, data []byte) []byte {
	if name == "[DONE]" {
		return append(dst, "data: [DONE]\n\n"...)
	}
	if name != "" {
		dst = append(dst, "event: "...)
		dst = append(dst, name...)
		dst = append(dst, '\n')
	}
	dst = append(dst, "data: "...)
	dst = append(dst, data...)
	dst = append(dst, '\n', '\n')
	return dst
}

// WriteSSEHeaders 设置流式响应头。
func WriteSSEHeaders(w http.ResponseWriter) {
	h := w.Header()
	h.Set("Content-Type", "text/event-stream; charset=utf-8")
	h.Set("Cache-Control", "no-cache")
	h.Set("Connection", "keep-alive")
	h.Set("X-Accel-Buffering", "no") // 禁止 nginx 缓冲
}

// ErrorPayload 是统一的错误响应体。
type ErrorPayload struct {
	Error ErrorDetail `json:"error"`
}

// ErrorDetail 是错误详情。三协议共用同一份字段清单。
type ErrorDetail struct {
	Message string `json:"message"`
	Type    string `json:"type"`
	Code    string `json:"code,omitempty"`
	Param   string `json:"param,omitempty"`
}

// BuildErrorPayload 构造错误响应体。
//
// 这是唯一的错误字段清单：三协议共用，避免各处抄写漂移。
func BuildErrorPayload(f *llm.Failure) ErrorPayload {
	detail := ErrorDetail{
		Message: "internal server error",
		Type:    "server_error",
		Code:    "internal_error",
	}
	if f != nil {
		if f.Message != "" {
			detail.Message = f.Message
		}
		detail.Type = f.ErrorType()
		detail.Code = f.ErrorCode()
	}
	return ErrorPayload{Error: detail}
}

// WriteError 写出 JSON 错误响应。
func WriteError(w http.ResponseWriter, f *llm.Failure) {
	status := http.StatusBadGateway
	if f != nil {
		status = f.HTTPStatus()
	}
	if status == 499 {
		// 499 是 nginx 的非标准码，客户端断开时不该真的下发。
		status = http.StatusOK
	}
	body, err := json.Marshal(BuildErrorPayload(f))
	if err != nil {
		body = []byte(`{"error":{"message":"internal server error","type":"server_error"}}`)
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write(body)
}

// ErrNoContent 表示本帧不产生任何输出。
var ErrNoContent = errors.New("no content")
