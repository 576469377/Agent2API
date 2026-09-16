package messages

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/576469377/Agent2API/internal/api/common"
	"github.com/576469377/Agent2API/internal/llm"
)

// fakeStream 按顺序回放给定的 IR 事件，用于离线测试协议编码。
type fakeStream struct {
	events []llm.ResponseEvent
	pos    int
}

func (f *fakeStream) Recv(context.Context) (llm.ResponseEvent, error) {
	if f.pos >= len(f.events) {
		return llm.ResponseEvent{}, llm.ErrStreamDone
	}
	ev := f.events[f.pos]
	f.pos++
	return ev, nil
}

// TestStreamBlockPairing 保证 content_block_start 与 content_block_stop 严格一一配对。
//
// 曾出现的 bug：结束事件没有把块从「未关闭」集合中移除，收尾阶段又对同一个块
// 补发了一次 content_block_stop。Anthropic 客户端 SDK 会对已关闭的块二次结算，
// 表现为整段回复在界面上被渲染两遍。
func TestStreamBlockPairing(t *testing.T) {
	p := Protocol{}
	enc := p.NewStreamEncoder("test-model", true)

	events := []llm.ResponseEvent{
		{Type: llm.EventStart, Model: "test-model"},
		{Type: llm.EventThinkingStart, ContentIndex: 0},
		{Type: llm.EventThinkingDelta, ContentIndex: 0, Delta: "让我想想"},
		{Type: llm.EventThinkingEnd, ContentIndex: 0},
		{Type: llm.EventTextStart, ContentIndex: 1},
		{Type: llm.EventTextDelta, ContentIndex: 1, Delta: "你好"},
		{Type: llm.EventTextEnd, ContentIndex: 1},
		{Type: llm.EventDone, StopReason: llm.StopReasonStop, Usage: &llm.Usage{OutputTokens: 10}},
	}

	var starts, stops []int
	var names []string
	for _, ev := range events {
		out, err := enc.Encode(ev)
		if err != nil {
			t.Fatalf("编码事件 %s 失败: %v", ev.Type, err)
		}
		for _, sse := range out {
			names = append(names, sse.Name)
			switch sse.Name {
			case "content_block_start":
				starts = append(starts, blockIndex(t, sse))
			case "content_block_stop":
				stops = append(stops, blockIndex(t, sse))
			}
		}
	}

	if len(starts) != len(stops) {
		t.Fatalf("start 与 stop 数量不等: start=%v stop=%v（完整序列: %v）", starts, stops, names)
	}
	// 同一个下标不得被 stop 两次
	seen := map[int]bool{}
	for _, idx := range stops {
		if seen[idx] {
			t.Fatalf("下标 %d 被 stop 了两次（完整序列: %v）", idx, names)
		}
		seen[idx] = true
	}
	// 每个 stop 的下标都必须先 start 过
	started := map[int]bool{}
	for _, idx := range starts {
		started[idx] = true
	}
	for _, idx := range stops {
		if !started[idx] {
			t.Fatalf("下标 %d 未 start 就 stop（完整序列: %v）", idx, names)
		}
	}
}

// TestStreamDoneClosesDanglingBlocks 兜底：流异常中断、块未闭合时，
// done 事件应恰好补一次 stop，不多不少。
func TestStreamDoneClosesDanglingBlocks(t *testing.T) {
	p := Protocol{}
	enc := p.NewStreamEncoder("test-model", true)

	events := []llm.ResponseEvent{
		{Type: llm.EventStart, Model: "test-model"},
		{Type: llm.EventTextStart, ContentIndex: 0},
		{Type: llm.EventTextDelta, ContentIndex: 0, Delta: "半句话"},
		// 刻意不发 EventTextEnd
		{Type: llm.EventDone, StopReason: llm.StopReasonLength},
	}

	stops := 0
	for _, ev := range events {
		out, err := enc.Encode(ev)
		if err != nil {
			t.Fatalf("编码失败: %v", err)
		}
		for _, sse := range out {
			if sse.Name == "content_block_stop" {
				stops++
			}
		}
	}
	if stops != 1 {
		t.Fatalf("未闭合的块应恰好补 1 次 stop，实际 %d", stops)
	}
}

// blockIndex 从 SSE 帧里取出 index 字段。
func blockIndex(t *testing.T, sse common.SSEEvent) int {
	t.Helper()
	var payload struct {
		Index int `json:"index"`
	}
	if err := json.Unmarshal(sse.Data, &payload); err != nil {
		t.Fatalf("解析 SSE 数据失败: %v", err)
	}
	return payload.Index
}

// ───────────────────────── 请求解码（图片 / 工具结果） ─────────────────────────

func decodeAnthropic(t *testing.T, body string) llm.RequestMessages {
	t.Helper()
	req, err := (Protocol{}).DecodeRequest([]byte(body))
	if err != nil {
		t.Fatalf("解码失败: %v", err)
	}
	return req
}

// TestDecodeAnthropicImageBuildsDataURL 覆盖 Bug 1：
// Anthropic 的 base64 图片必须补全 data:image/png;base64, 前缀，否则上游无法解析（400）。
func TestDecodeAnthropicImageBuildsDataURL(t *testing.T) {
	body := `{
		"model": "deepseek-v4.1-flash",
		"messages": [
			{"role":"user","content":[
				{"type":"text","text":"这是什么颜色？"},
				{"type":"image","source":{"type":"base64","media_type":"image/png","data":"iVBORw0KGgoAAAANS"}}
			]}
		]
	}`
	req := decodeAnthropic(t, body)
	if len(req.Messages) != 1 {
		t.Fatalf("期望 1 条消息，实际 %d", len(req.Messages))
	}
	var img *llm.Content
	for i := range req.Messages[0].Content {
		if req.Messages[0].Content[i].Type == llm.ContentImage {
			img = &req.Messages[0].Content[i]
		}
	}
	if img == nil {
		t.Fatal("未解析出图片内容块")
	}
	if !strings.HasPrefix(img.ImageURL, "data:image/png;base64,") {
		t.Errorf("图片 URL 缺少 data: 前缀，实际: %q", img.ImageURL)
	}
	if !strings.Contains(img.ImageURL, "iVBORw0KGgoAAAANS") {
		t.Errorf("图片 base64 数据丢失: %q", img.ImageURL)
	}
}

// TestDecodeAnthropicImageURLPassthrough 验证 url 类型的图片源原样透传（已是完整 URL）。
func TestDecodeAnthropicImageURLPassthrough(t *testing.T) {
	body := `{
		"model": "deepseek-v4.1-flash",
		"messages": [
			{"role":"user","content":[
				{"type":"image","source":{"type":"url","url":"https://example.com/a.png"}}
			]}
		]
	}`
	req := decodeAnthropic(t, body)
	var img *llm.Content
	for i := range req.Messages[0].Content {
		if req.Messages[0].Content[i].Type == llm.ContentImage {
			img = &req.Messages[0].Content[i]
		}
	}
	if img == nil || img.ImageURL != "https://example.com/a.png" {
		t.Fatalf("url 类型图片应原样透传，实际 %+v", img)
	}
}

// TestDecodeAnthropicToolResultKeepsImage 覆盖 Bug 2：
// tool_result 内的图片（如 Claude Code Read 工具读图）必须随文本一起保留。
func TestDecodeAnthropicToolResultKeepsImage(t *testing.T) {
	body := `{
		"model": "deepseek-v4.1-flash",
		"messages": [
			{"role":"user","content":[
				{"type":"tool_result","tool_use_id":"tu_1","content":[
					{"type":"text","text":"图片内容如下"},
					{"type":"image","source":{"type":"base64","media_type":"image/jpeg","data":"/9j/4AAQSkZJRg"}}
				]}
			]}
		]
	}`
	req := decodeAnthropic(t, body)
	if req.Messages[0].Role != llm.RoleTool {
		t.Fatalf("tool_result 应映射为 RoleTool，实际 %v", req.Messages[0].Role)
	}
	var tr *llm.ToolResult
	for i := range req.Messages[0].Content {
		if req.Messages[0].Content[i].Type == llm.ContentToolResult {
			tr = req.Messages[0].Content[i].ToolResult
		}
	}
	if tr == nil {
		t.Fatal("未解析出 tool_result")
	}
	if tr.ToolCallID != "tu_1" {
		t.Errorf("ToolCallID 错误: %q", tr.ToolCallID)
	}
	if tr.Content != "图片内容如下" {
		t.Errorf("文本丢失: %q", tr.Content)
	}
	if len(tr.Blocks) != 2 {
		t.Fatalf("期望 2 个结构块（文本+图片），实际 %d", len(tr.Blocks))
	}
	var foundImg bool
	for _, b := range tr.Blocks {
		if b.Type == llm.ContentImage {
			foundImg = true
			if !strings.HasPrefix(b.ImageURL, "data:image/jpeg;base64,") {
				t.Errorf("tool_result 内图片 URL 缺少前缀: %q", b.ImageURL)
			}
		}
	}
	if !foundImg {
		t.Error("tool_result 内的图片被丢弃")
	}
}

// TestDecodeAnthropicToolResultTextOnly 保证纯文本 tool_result 行为不变。
func TestDecodeAnthropicToolResultTextOnly(t *testing.T) {
	body := `{
		"model": "deepseek-v4.1-flash",
		"messages": [
			{"role":"user","content":[
				{"type":"tool_result","tool_use_id":"tu_2","content":"纯文本结果"}
			]}
		]
	}`
	req := decodeAnthropic(t, body)
	tr := req.Messages[0].Content[0].ToolResult
	if tr.Content != "纯文本结果" {
		t.Errorf("文本应为 纯文本结果，实际 %q", tr.Content)
	}
	if len(tr.Blocks) != 0 {
		t.Errorf("纯文本不应产生 Blocks，实际 %d", len(tr.Blocks))
	}
}

// TestStreamMessageDeltaCarriesInputTokens 是「input_tokens 恒为 0」的回归测试。
//
// 曾有 bug：message_start 硬编码 input_tokens=0，而上游只在流末尾回传 usage，
// message_delta 又只发 output_tokens——流式客户端全程看到输入 token 为 0。
// 修复依据官方 SDK 的累积覆盖语义（message_delta.usage 非零即覆盖快照）：
// 在 message_delta 补发真实 input_tokens。
func TestStreamMessageDeltaCarriesInputTokens(t *testing.T) {
	p := Protocol{}
	enc := p.NewStreamEncoder("test-model", true)

	events := []llm.ResponseEvent{
		{Type: llm.EventStart, Model: "test-model"},
		{Type: llm.EventTextDelta, ContentIndex: 0, Delta: "你好"},
		{Type: llm.EventDone, StopReason: llm.StopReasonStop,
			Usage: &llm.Usage{InputTokens: 1234, OutputTokens: 56}},
	}

	var deltaUsage struct {
		Input  int `json:"input_tokens"`
		Output int `json:"output_tokens"`
	}
	found := false
	for _, ev := range events {
		out, err := enc.Encode(ev)
		if err != nil {
			t.Fatalf("编码 %s 失败: %v", ev.Type, err)
		}
		for _, sse := range out {
			if sse.Name != "message_delta" {
				continue
			}
			found = true
			var frame struct {
				Usage struct {
					Input  int `json:"input_tokens"`
					Output int `json:"output_tokens"`
				} `json:"usage"`
			}
			if err := json.Unmarshal(sse.Data, &frame); err != nil {
				t.Fatalf("message_delta 解析失败: %v", err)
			}
			deltaUsage = frame.Usage
		}
	}
	if !found {
		t.Fatal("没有 message_delta 帧")
	}
	if deltaUsage.Input != 1234 {
		t.Fatalf("message_delta 的 input_tokens=%d, want 1234（客户端 SDK 靠它覆盖 message_start 的 0）", deltaUsage.Input)
	}
	if deltaUsage.Output != 56 {
		t.Fatalf("message_delta 的 output_tokens=%d, want 56", deltaUsage.Output)
	}
}
