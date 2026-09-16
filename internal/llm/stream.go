package llm

import (
	"context"
	"io"
)

// StopReason 是生成停止原因。
type StopReason string

const (
	StopReasonStop          StopReason = "stop"
	StopReasonToolCalls     StopReason = "tool_calls"
	StopReasonLength        StopReason = "length"
	StopReasonContentFilter StopReason = "content_filter"
	StopReasonError         StopReason = "error"
)

// ResponseEventType 是流式响应事件类型。
type ResponseEventType string

const (
	EventStart         ResponseEventType = "start"
	EventTextStart     ResponseEventType = "text_start"
	EventTextDelta     ResponseEventType = "text_delta"
	EventTextEnd       ResponseEventType = "text_end"
	EventThinkingStart ResponseEventType = "thinking_start"
	EventThinkingDelta ResponseEventType = "thinking_delta"
	EventThinkingEnd   ResponseEventType = "thinking_end"
	EventToolCallStart ResponseEventType = "toolcall_start"
	EventToolCallDelta ResponseEventType = "toolcall_delta"
	EventToolCallEnd   ResponseEventType = "toolcall_end"
	EventDone          ResponseEventType = "done"
	EventError         ResponseEventType = "error"
)

// ResponseEvent 是供应商无关的响应增量事件。
//
// 采用「扁平结构体 + 类型枚举」而非「每种事件一个类型」：事件枚举是封闭且数量固定的，
// switch 分发比注册表更少间接层；下游编码器只需按 Type 取自己关心的字段。
type ResponseEvent struct {
	Type ResponseEventType

	// ContentIndex 是事件对应的助手内容块下标。
	ContentIndex int

	// Delta 是增量文本：文本增量、思考增量、未完成的工具参数片段。
	Delta string

	// ToolCall* 用于工具调用事件。Start 带 ID 与 Name；
	// Delta 只带 Arguments 分片；End 带完整 Arguments。
	ToolCallID        string
	ToolName          string
	ToolArguments     string
	ToolArgumentsDone bool

	// Usage 只在 EventDone 携带。
	Usage *Usage

	// StopReason 只在 EventDone / EventError 携带。
	StopReason StopReason

	// Model / ResponseID 由 EventStart 携带，后续事件为空。
	Model      string
	ResponseID string

	// Error 只在 EventError 携带。
	Error *Failure
}

// ErrStreamDone 是流正常结束的信号，等价于 io.EOF。
var ErrStreamDone = io.EOF

// ResponseStream 按顺序提供一次助手响应的增量事件。
//
// 这是全项目最划算的抽象：一个阻塞 Recv 就把「真流式 SSE / 分页拉取 / 轮询任务状态」
// 全部归一化。上游无论如何变化，下游编排层无需改动。
type ResponseStream interface {
	// Recv 返回下一个响应事件；流正常结束时返回 ErrStreamDone。
	Recv(ctx context.Context) (ResponseEvent, error)
}

// AssistantMessage 是聚合后的完整助手消息，用于非流式响应。
type AssistantMessage struct {
	Content    []Content
	Model      string
	ResponseID string
	Usage      Usage
	StopReason StopReason
}
