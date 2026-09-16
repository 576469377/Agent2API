package app

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/576469377/Agent2API/internal/adapter"
	"github.com/576469377/Agent2API/internal/config"
	"github.com/576469377/Agent2API/internal/llm"
)

// fakeAdapter 是可编程的假上游：Stream 返回预设事件或错误。
type fakeAdapter struct {
	events   []llm.ResponseEvent
	err      error
	name     string
	useModel string
	// perEventDelay 若非零，每次 Recv 之间真实等待，用于让 TPS 的
	// 生成耗时窗口超过亚毫秒精度。
	perEventDelay int
}

func (f *fakeAdapter) Stream(ctx context.Context, req llm.RequestMessages) (llm.ResponseStream, error) {
	if f.err != nil {
		return nil, f.err
	}
	return &eventStream{events: f.events, delayMs: f.perEventDelay}, nil
}

type eventStream struct {
	events  []llm.ResponseEvent
	pos     int
	delayMs int
}

func (s *eventStream) Recv(ctx context.Context) (llm.ResponseEvent, error) {
	if s.pos >= len(s.events) {
		return llm.ResponseEvent{}, llm.ErrStreamDone
	}
	if s.delayMs > 0 {
		select {
		case <-ctx.Done():
			return llm.ResponseEvent{}, ctx.Err()
		case <-time.After(time.Duration(s.delayMs) * time.Millisecond):
		}
	}
	ev := s.events[s.pos]
	s.pos++
	return ev, nil
}

func (f *fakeAdapter) ListModels(context.Context) ([]adapter.ModelInfo, error) {
	return nil, nil
}

func (f *fakeAdapter) Name() string {
	if f.name != "" {
		return f.name
	}
	return "fake"
}

// ───────────────────────── aggregate ─────────────────────────

// TestAggregateOrderAndTools 覆盖编排核心的聚合：分块文本按序还原、
// 工具调用聚合为带参数的完整调用、thinking 与文本顺序保持。
func TestAggregateOrderAndTools(t *testing.T) {
	events := []llm.ResponseEvent{
		{Type: llm.EventStart, Model: "m1", ResponseID: "r1"},
		{Type: llm.EventTextStart, ContentIndex: 0},
		{Type: llm.EventTextDelta, ContentIndex: 0, Delta: "你好"},
		{Type: llm.EventTextDelta, ContentIndex: 0, Delta: "，"},
		{Type: llm.EventTextEnd, ContentIndex: 0},
		{Type: llm.EventToolCallStart, ContentIndex: 1, ToolCallID: "c1", ToolName: "lookup"},
		{Type: llm.EventToolCallDelta, ContentIndex: 1, Delta: `{"q":`},
		{Type: llm.EventToolCallDelta, ContentIndex: 1, Delta: `"x"}`},
		{Type: llm.EventToolCallEnd, ContentIndex: 1},
		{Type: llm.EventDone, StopReason: llm.StopReasonToolCalls,
			Usage: &llm.Usage{InputTokens: 2, OutputTokens: 3}},
	}
	msg, err := aggregate(context.Background(), &eventStream{events: events})
	if err != nil {
		t.Fatalf("aggregate: %v", err)
	}
	if msg.Model != "m1" || msg.ResponseID != "r1" {
		t.Fatalf("start 元数据丢失: %+v", msg)
	}
	if msg.StopReason != llm.StopReasonToolCalls {
		t.Fatalf("StopReason=%v", msg.StopReason)
	}
	if msg.Usage.OutputTokens != 3 {
		t.Fatalf("Usage=%+v", msg.Usage)
	}
	if len(msg.Content) != 2 {
		t.Fatalf("应有 2 个内容块, got %d: %+v", len(msg.Content), msg.Content)
	}
	if msg.Content[0].Text != "你好，" {
		t.Fatalf("文本分片未按序拼接: %q", msg.Content[0].Text)
	}
	tc := msg.Content[1].ToolCall
	if tc == nil || tc.ID != "c1" || tc.Name != "lookup" || tc.Arguments != `{"q":"x"}` {
		t.Fatalf("工具调用聚合错误: %+v", msg.Content[1].ToolCall)
	}
}

// TestAggregatePropagatesError 保证上游错误原样穿透。
func TestAggregatePropagatesError(t *testing.T) {
	want := &llm.Failure{Code: "upstream_read_failed", Message: "断流"}
	_, err := aggregate(context.Background(), &eventStream{events: []llm.ResponseEvent{
		{Type: llm.EventError, Error: want},
	}})
	if err == nil {
		t.Fatal("EventError 必须返回错误")
	}
	var f *llm.Failure
	if !errors.As(err, &f) || f.Code != "upstream_read_failed" {
		t.Fatalf("错误应保持类型与码: %v", err)
	}
}

// TestAggregateEmptyStreamDropsToolCallArgs 覆盖空参数工具调用的兜底：args 为空时应为 "{}"。
func TestAggregateEmptyStreamDropsToolCallArgs(t *testing.T) {
	events := []llm.ResponseEvent{
		{Type: llm.EventToolCallStart, ContentIndex: 0, ToolCallID: "c1", ToolName: "ping"},
		{Type: llm.EventToolCallEnd, ContentIndex: 0},
		{Type: llm.EventDone, StopReason: llm.StopReasonToolCalls},
	}
	msg, err := aggregate(context.Background(), &eventStream{events: events})
	if err != nil {
		t.Fatalf("aggregate: %v", err)
	}
	if got := msg.Content[0].ToolCall.Arguments; got != "{}" {
		t.Fatalf("空参数应为 {}, got %q", got)
	}
}

// ───────────────────────── serve / writeStream ─────────────────────────

func newTestApp(t *testing.T, adp *fakeAdapter) *App {
	t.Helper()
	cfg := config.Default()
	cfg.MetricsFile = "" // 测试中不落盘
	return New(cfg, adp, log.New(io.Discard, "", 0))
}

// TestStreamErrorInBandError 是端到端回归：
// 上游在产出部分内容后失败，流式客户端必须收到流内错误帧，
// 而不是拿到 200 + 半截内容后无声结束。
func TestStreamErrorInBandError(t *testing.T) {
	adp := &fakeAdapter{events: []llm.ResponseEvent{
		{Type: llm.EventStart, Model: "m"},
		{Type: llm.EventTextDelta, ContentIndex: 0, Delta: "半截"},
		{Type: llm.EventError, Error: &llm.Failure{Code: "upstream_read_failed", Message: "断流", UpstreamFault: true}},
	}}
	a := newTestApp(t, adp)

	body := `{"model":"m","messages":[{"role":"user","content":"hi"}],"stream":true}`
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
	w := httptest.NewRecorder()
	a.serve(w, req, chatProtocol{})

	resp := w.Result()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("首帧已提交后状态码应为 200, got %d", resp.StatusCode)
	}
	raw, _ := io.ReadAll(resp.Body)
	s := string(raw)
	if !strings.Contains(s, `"error"`) {
		t.Fatalf("流内必须含错误帧, got: %s", s)
	}
	if strings.Contains(s, "[DONE]") {
		t.Fatal("错误后不得补发 [DONE]")
	}
}

// TestStreamNonPostRejected 保证协议入口只收 POST。
func TestStreamNonPostRejected(t *testing.T) {
	a := newTestApp(t, &fakeAdapter{})
	req := httptest.NewRequest(http.MethodGet, "/v1/chat/completions", nil)
	w := httptest.NewRecorder()
	a.serve(w, req, chatProtocol{})
	if w.Result().StatusCode != http.StatusMethodNotAllowed {
		t.Fatal("GET 应 405")
	}
}

// TestWithAuth 保证鉴权开关行为：未配置 key 放行；配置后必须带对。
func TestWithAuth(t *testing.T) {
	a := newTestApp(t, &fakeAdapter{})
	a.cfg.Auth.APIKey = "secret"

	ok := httptest.NewRequest(http.MethodPost, "/v1/models", nil)
	ok.Header.Set("Authorization", "Bearer secret")
	w1 := httptest.NewRecorder()
	a.withAuth(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(200) })(w1, ok)
	if w1.Result().StatusCode != 200 {
		t.Fatal("正确 Bearer key 应放行")
	}

	w2 := httptest.NewRecorder()
	wrong := httptest.NewRequest(http.MethodPost, "/v1/models", nil)
	wrong.Header.Set("X-Api-Key", "nope")
	a.withAuth(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(200) })(w2, wrong)
	if w2.Result().StatusCode != 401 {
		t.Fatal("错误 key 应 401")
	}
}

// ───────────────────────── 指标记录路径 ─────────────────────────

// TestServeRecordsMetricsInFlight 验证 serve 前后 in-flight 计数守恒。
func TestServeRecordsMetricsInFlight(t *testing.T) {
	adp := &fakeAdapter{events: []llm.ResponseEvent{
		{Type: llm.EventTextDelta, ContentIndex: 0, Delta: "hi"},
		{Type: llm.EventDone, StopReason: llm.StopReasonStop, Usage: &llm.Usage{OutputTokens: 1}},
	}}
	a := newTestApp(t, adp)

	body := `{"model":"m","messages":[{"role":"user","content":"hi"}],"stream":true}`
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
	a.serve(httptest.NewRecorder(), req, chatProtocol{})

	snap := a.metrics.Snapshot()
	if snap.InFlight != 0 {
		t.Fatalf("请求结束后 in-flight 应归零, got %d", snap.InFlight)
	}
	if snap.Total != 1 || snap.OK != 1 || snap.OutputTokens != 1 {
		t.Fatalf("指标未记录: %+v", snap)
	}
}

// TestServeGenMsRecordedForStream 验证流式请求采集 TPS 分母（GenMs）。
// 用带真实延迟的事件流绕过亚毫秒精度：生成窗口 <1ms 时不采样是守卫的正确行为。
func TestServeGenMsRecordedForStream(t *testing.T) {
	adp := &fakeAdapter{perEventDelay: 3, events: []llm.ResponseEvent{
		{Type: llm.EventTextDelta, ContentIndex: 0, Delta: "hello"},
		{Type: llm.EventTextDelta, ContentIndex: 0, Delta: " world"},
		{Type: llm.EventDone, StopReason: llm.StopReasonStop, Usage: &llm.Usage{OutputTokens: 5}},
	}}
	a := newTestApp(t, adp)

	body := `{"model":"m","messages":[{"role":"user","content":"hi"}],"stream":true}`
	a.serve(httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body)), chatProtocol{})

	snap := a.metrics.Snapshot()
	if snap.TPSSamples != 1 {
		t.Fatalf("流式请求应计入 TPS 样本, samples=%d", snap.TPSSamples)
	}
	if snap.AvgTPS <= 0 {
		t.Fatalf("AvgTPS 应为正, got %v", snap.AvgTPS)
	}
}

// ───────────────────────── 假适配器接口约束 ─────────────────────────

var _ adapter.Adapter = (*fakeAdapter)(nil)

// TestNotFoundPath 确保 /nope 返回 404（曾是 400）。
func TestNotFoundPath(t *testing.T) {
	a := newTestApp(t, &fakeAdapter{})
	srv := httptest.NewServer(a.Handler())
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/nope")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("/nope 应 404, got %d", resp.StatusCode)
	}
	var payload map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&payload)
}
