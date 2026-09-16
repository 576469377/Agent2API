package workbuddy

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/576469377/Agent2API/internal/llm"
)

// replayFixture 把录制的真实上游 SSE 回放成 IR 事件序列。
//
// 断言 IR 事件序列而不是字节——这正是 IR 层的额外红利：
// 协议转换可以完全离线测试，不打真实上游。
func replayFixture(t *testing.T, name string) []llm.ResponseEvent {
	t.Helper()
	path := filepath.Join("..", "..", "..", "fixtures", name)
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Skipf("缺少 fixture %s: %v", name, err)
	}
	resp := &http.Response{Body: io.NopCloser(bytes.NewReader(raw))}
	stream := newUpstreamStream(resp)
	defer stream.Close()

	var events []llm.ResponseEvent
	for {
		ev, err := stream.Recv(context.Background())
		if err != nil {
			if err == llm.ErrStreamDone {
				break
			}
			t.Fatalf("回放失败: %v", err)
		}
		events = append(events, ev)
	}
	return events
}

func countType(events []llm.ResponseEvent, typ llm.ResponseEventType) int {
	n := 0
	for _, e := range events {
		if e.Type == typ {
			n++
		}
	}
	return n
}

// TestReplayTextThenToolCall 覆盖「先输出文本、再输出工具调用」的场景。
//
// 这个场景曾暴露一个真实 bug：上游 tool_calls[].index 从 0 开始，
// 与文本块的 IR 内容下标撞号，导致聚合时工具调用被文本块覆盖而丢失。
func TestReplayTextThenToolCall(t *testing.T) {
	events := replayFixture(t, "workbuddy-stream-text-then-toolcall.jsonl")

	if got := countType(events, llm.EventStart); got != 1 {
		t.Fatalf("期望 1 个 start 事件，实际 %d", got)
	}
	if got := countType(events, llm.EventTextDelta); got == 0 {
		t.Fatal("期望有文本增量事件")
	}
	if got := countType(events, llm.EventToolCallStart); got != 1 {
		t.Fatalf("期望 1 个 toolcall_start 事件，实际 %d", got)
	}
	if got := countType(events, llm.EventToolCallEnd); got != 1 {
		t.Fatalf("期望 1 个 toolcall_end 事件，实际 %d", got)
	}

	// 文本块与工具调用块必须使用不同的 ContentIndex
	var textIdx, toolIdx = -1, -1
	for _, e := range events {
		switch e.Type {
		case llm.EventTextDelta:
			textIdx = e.ContentIndex
		case llm.EventToolCallStart:
			toolIdx = e.ContentIndex
		}
	}
	if textIdx == toolIdx {
		t.Fatalf("文本块与工具调用块下标冲突（都是 %d），会导致聚合时互相覆盖", textIdx)
	}

	var end llm.ResponseEvent
	for _, e := range events {
		if e.Type == llm.EventToolCallEnd {
			end = e
		}
	}
	if end.ToolName != "get_weather" {
		t.Errorf("工具名应为 get_weather，实际 %q", end.ToolName)
	}
	if !strings.Contains(end.ToolArguments, "北京") {
		t.Errorf("工具参数应包含 北京，实际 %q", end.ToolArguments)
	}
	if end.ToolCallID == "" {
		t.Error("工具调用 ID 不应为空")
	}
}

// TestReplayTextStream 覆盖纯文本流（含思考过程）。
func TestReplayTextStream(t *testing.T) {
	events := replayFixture(t, "workbuddy-stream-text.jsonl")
	if got := countType(events, llm.EventTextDelta); got == 0 {
		t.Fatal("期望有文本增量事件")
	}
	var done *llm.ResponseEvent
	for i := range events {
		if events[i].Type == llm.EventDone {
			done = &events[i]
		}
	}
	if done == nil {
		t.Fatal("期望有 done 事件")
	}
	if done.Usage == nil {
		t.Error("done 事件应携带 usage")
	}
}

// TestReplayToolCallStream 覆盖纯工具调用流。
func TestReplayToolCallStream(t *testing.T) {
	events := replayFixture(t, "workbuddy-stream-toolcall.jsonl")
	if got := countType(events, llm.EventToolCallStart); got == 0 {
		t.Fatal("期望有 toolcall_start 事件")
	}
	var args string
	for _, e := range events {
		if e.Type == llm.EventToolCallEnd {
			args = e.ToolArguments
		}
	}
	if !strings.Contains(args, "city") {
		t.Errorf("工具参数应包含 city，实际 %q", args)
	}
}
