package workbuddy

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/576469377/Agent2API/internal/llm"
)

// chunk 是上游流式响应的一帧。
//
// 实测结构（2026-09-15）：
//
//	{"id":"cmb-...","model":"glm-5.3","object":"chat.completion.chunk","created":1700000004,
//	 "choices":[{"index":0,"delta":{"role":"assistant","content":"","reasoning_content":"...",
//	   "function_call":null,"refusal":"","tool_calls":[],"extra_fields":null},
//	   "logprobs":null,"finish_reason":""}],"usage":null}
type chunk struct {
	ID      string `json:"id"`
	Model   string `json:"model"`
	Object  string `json:"object"`
	Created int64  `json:"created"`
	Choices []struct {
		Index int `json:"index"`
		Delta struct {
			Role             string          `json:"role"`
			Content          string          `json:"content"`
			ReasoningContent string          `json:"reasoning_content"`
			Refusal          string          `json:"refusal"`
			ToolCalls        []chunkToolCall `json:"tool_calls"`
		} `json:"delta"`
		FinishReason *string `json:"finish_reason"`
		Logprobs     any     `json:"logprobs"`
	} `json:"choices"`
	Usage *chunkUsage `json:"usage"`
}

type chunkToolCall struct {
	Index    int    `json:"index"`
	ID       string `json:"id"`
	Type     string `json:"type"`
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"function"`
}

type chunkUsage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
	Details          struct {
		ReasoningTokens int `json:"reasoning_tokens"`
		CachedTokens    int `json:"cached_tokens"`
	} `json:"completion_tokens_details"`
}

// toolCallState 累计一个工具调用的分片。
//
// index 是上游的 tool_calls[].index（仅用于把分片归组）；
// irIndex 是本流内唯一的内容块下标。两者必须分开：上游的 index 从 0 开始，
// 会与文本/思考块的 ContentIndex 撞号，导致聚合时工具调用被文本块覆盖。
type toolCallState struct {
	index    int
	irIndex  int
	id       string
	name     string
	args     strings.Builder
	started  bool
	deferred bool // 首片没带 name，暂缓 Start 事件
}

// upstreamStream 把上游 SSE 逐帧翻译成 IR 事件。
type upstreamStream struct {
	resp    *http.Response
	scanner *bufio.Scanner

	// pending 缓存「一帧产生多个事件」时待投递的事件。
	pending []llm.ResponseEvent

	started bool
	model   string
	id      string

	nextIndex int

	textIdx  int
	textOpen bool

	thinkIdx  int
	thinkOpen bool

	tools     map[int]*toolCallState
	toolOrder []int

	// cancel 由 dial 注入，Close 时调用以释放超时 ctx。
	cancel context.CancelFunc

	stopReason llm.StopReason
	usage      *llm.Usage

	finished bool
}

func newUpstreamStream(resp *http.Response) *upstreamStream {
	s := &upstreamStream{
		resp:       resp,
		tools:      map[int]*toolCallState{},
		stopReason: llm.StopReasonStop,
	}
	s.scanner = bufio.NewScanner(resp.Body)
	s.scanner.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	return s
}

// Recv 实现 llm.ResponseStream。
func (s *upstreamStream) Recv(ctx context.Context) (llm.ResponseEvent, error) {
	for len(s.pending) > 0 {
		ev := s.pending[0]
		s.pending = s.pending[1:]
		return ev, nil
	}
	if s.finished {
		return llm.ResponseEvent{}, llm.ErrStreamDone
	}

	for s.scanner.Scan() {
		if err := ctx.Err(); err != nil {
			return llm.ResponseEvent{}, err
		}
		line := s.scanner.Text()
		// 上游只发 data: 行，不发 event: 行；其他行一律忽略。
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if data == "" {
			continue
		}
		if data == "[DONE]" {
			s.finished = true
			s.emitFinish()
			if len(s.pending) > 0 {
				ev := s.pending[0]
				s.pending = s.pending[1:]
				return ev, nil
			}
			return llm.ResponseEvent{}, llm.ErrStreamDone
		}

		var c chunk
		if err := json.Unmarshal([]byte(data), &c); err != nil {
			// 单帧解析失败不应中断整条流：上游偶发夹杂非 JSON 保活行。
			continue
		}
		s.consume(&c)
		if len(s.pending) > 0 {
			break
		}
	}
	if err := s.scanner.Err(); err != nil {
		if err == context.Canceled || ctx.Err() != nil {
			return llm.ResponseEvent{}, ctx.Err()
		}
		return llm.ResponseEvent{}, llm.Wrap(&llm.Failure{
			Code: "upstream_read_failed", Message: "读取上游流失败: " + err.Error(),
			Cause: err, UpstreamFault: true,
		})
	}
	if len(s.pending) > 0 {
		ev := s.pending[0]
		s.pending = s.pending[1:]
		return ev, nil
	}
	// 流意外结束（没有 [DONE]）：按正常收尾处理，避免丢失已生成内容。
	s.finished = true
	s.emitFinish()
	if len(s.pending) > 0 {
		ev := s.pending[0]
		s.pending = s.pending[1:]
		return ev, nil
	}
	return llm.ResponseEvent{}, llm.ErrStreamDone
}

// Close 释放上游连接与超时 ctx。
func (s *upstreamStream) Close() error {
	if s.cancel != nil {
		s.cancel()
		s.cancel = nil
	}
	if s.resp != nil && s.resp.Body != nil {
		return s.resp.Body.Close()
	}
	return nil
}

// consume 把一帧翻译成零到多个 IR 事件。
func (s *upstreamStream) consume(c *chunk) {
	if !s.started {
		s.started = true
		s.model = c.Model
		s.id = c.ID
		s.pending = append(s.pending, llm.ResponseEvent{
			Type:       llm.EventStart,
			Model:      c.Model,
			ResponseID: c.ID,
		})
	}
	if c.Model != "" {
		s.model = c.Model
	}

	for _, ch := range c.Choices {
		d := ch.Delta

		// 思考过程：上游字段是 reasoning_content，同类实现普遍漏掉了它。
		if d.ReasoningContent != "" {
			if !s.thinkOpen {
				s.closeText()
				s.thinkIdx = s.allocIndex()
				s.thinkOpen = true
				s.pending = append(s.pending, llm.ResponseEvent{Type: llm.EventThinkingStart, ContentIndex: s.thinkIdx})
			}
			s.pending = append(s.pending, llm.ResponseEvent{
				Type: llm.EventThinkingDelta, ContentIndex: s.thinkIdx, Delta: d.ReasoningContent,
			})
		}

		if d.Content != "" {
			if s.thinkOpen {
				s.closeThinking()
			}
			if !s.textOpen {
				s.textIdx = s.allocIndex()
				s.textOpen = true
				s.pending = append(s.pending, llm.ResponseEvent{Type: llm.EventTextStart, ContentIndex: s.textIdx})
			}
			s.pending = append(s.pending, llm.ResponseEvent{
				Type: llm.EventTextDelta, ContentIndex: s.textIdx, Delta: d.Content,
			})
		}

		for _, tc := range d.ToolCalls {
			s.consumeToolCall(tc)
		}

		if ch.FinishReason != nil && *ch.FinishReason != "" {
			// 上游中间帧的 finish_reason 是空字符串，必须忽略；
			// 只有非空值才是真正的停止原因。
			s.stopReason = mapStopReason(*ch.FinishReason)
		}
	}

	if c.Usage != nil {
		s.usage = &llm.Usage{
			InputTokens:     c.Usage.PromptTokens,
			OutputTokens:    c.Usage.CompletionTokens,
			TotalTokens:     c.Usage.TotalTokens,
			ReasoningTokens: c.Usage.Details.ReasoningTokens,
			CachedTokens:    c.Usage.Details.CachedTokens,
		}
	}
}

// consumeToolCall 累计工具调用分片。
//
// 上游首片带 id + name，后续片 name 为空、只带 arguments 片段，
// 因此必须按 index 缓存并回填。
func (s *upstreamStream) consumeToolCall(tc chunkToolCall) {
	st, ok := s.tools[tc.Index]
	if !ok {
		st = &toolCallState{index: tc.Index, irIndex: s.allocIndex()}
		s.tools[tc.Index] = st
		s.toolOrder = append(s.toolOrder, tc.Index)
	}
	if tc.ID != "" {
		st.id = tc.ID
	}
	if tc.Function.Name != "" {
		st.name = tc.Function.Name
	}
	st.args.WriteString(tc.Function.Arguments)

	if !st.started {
		if st.name == "" {
			// 首片没有 name：暂缓 Start，等拿到 name 或流结束时再补，
			// 避免下游拿到无名工具调用。
			st.deferred = true
			return
		}
		// startToolCall 会把已缓冲的参数一次性补发，因此这里必须 return，
		// 否则同一段参数会被下发两次。
		s.startToolCall(st)
		return
	}
	if tc.Function.Arguments != "" {
		s.pending = append(s.pending, llm.ResponseEvent{
			Type: llm.EventToolCallDelta, ContentIndex: st.irIndex, Delta: tc.Function.Arguments,
		})
	}
}

func (s *upstreamStream) startToolCall(st *toolCallState) {
	if st.started {
		return
	}
	if st.deferred {
		// 补发 Start 前先把已缓冲的参数作为 delta 发出
		st.deferred = false
	}
	s.closeText()
	s.closeThinking()
	st.started = true
	if st.id == "" {
		st.id = "call_" + randomHex(12)
	}
	s.pending = append(s.pending, llm.ResponseEvent{
		Type: llm.EventToolCallStart, ContentIndex: st.irIndex,
		ToolCallID: st.id, ToolName: st.name,
	})
	if buffered := st.args.String(); buffered != "" {
		s.pending = append(s.pending, llm.ResponseEvent{
			Type: llm.EventToolCallDelta, ContentIndex: st.irIndex, Delta: buffered,
		})
	}
}

// emitFinish 在流结束时补齐所有未闭合的内容块。
func (s *upstreamStream) emitFinish() {
	// 未拿到 name 但已有分片的工具调用，也要补发 Start。
	for _, idx := range s.toolOrder {
		if st := s.tools[idx]; st != nil && !st.started {
			s.startToolCall(st)
		}
	}
	s.closeText()
	s.closeThinking()
	for _, idx := range s.toolOrder {
		st := s.tools[idx]
		if st == nil || !st.started {
			continue
		}
		s.pending = append(s.pending, llm.ResponseEvent{
			Type:              llm.EventToolCallEnd,
			ContentIndex:      st.irIndex,
			ToolCallID:        st.id,
			ToolName:          st.name,
			ToolArguments:     st.args.String(),
			ToolArgumentsDone: true,
		})
	}
	if s.stopReason == "" {
		s.stopReason = llm.StopReasonStop
	}
	s.pending = append(s.pending, llm.ResponseEvent{
		Type:       llm.EventDone,
		StopReason: s.stopReason,
		Usage:      s.usage,
		Model:      s.model,
		ResponseID: s.id,
	})
}

func (s *upstreamStream) closeText() {
	if !s.textOpen {
		return
	}
	s.textOpen = false
	s.pending = append(s.pending, llm.ResponseEvent{Type: llm.EventTextEnd, ContentIndex: s.textIdx})
}

func (s *upstreamStream) closeThinking() {
	if !s.thinkOpen {
		return
	}
	s.thinkOpen = false
	s.pending = append(s.pending, llm.ResponseEvent{Type: llm.EventThinkingEnd, ContentIndex: s.thinkIdx})
}

func (s *upstreamStream) allocIndex() int {
	i := s.nextIndex
	s.nextIndex++
	return i
}

// mapStopReason 把上游 finish_reason 映射到 IR。
func mapStopReason(reason string) llm.StopReason {
	switch reason {
	case "stop":
		return llm.StopReasonStop
	case "tool_calls", "function_call":
		return llm.StopReasonToolCalls
	case "length", "max_tokens":
		return llm.StopReasonLength
	case "content_filter":
		return llm.StopReasonContentFilter
	default:
		return llm.StopReasonStop
	}
}

// parseRetryAfter 解析 Retry-After 头（秒）。
func parseRetryAfter(v string) int {
	if v == "" {
		return 0
	}
	n, err := strconv.Atoi(strings.TrimSpace(v))
	if err != nil {
		return 0
	}
	return n
}

// idleReadCloser 在超过指定时间没有数据到达时主动断开上游连接。
//
// 上游存在「流无限期运行」的现象（实测观察到 6+ 分钟、2000+ chunk 的异常流）。
// http.Client.Timeout 只作用于单次读间隔且会误杀长流，因此需要独立的空闲看门狗。
type idleReadCloser struct {
	rc      io.ReadCloser
	timeout time.Duration
	mu      sync.Mutex
	timer   *time.Timer
	closed  bool
}

func newIdleReadCloser(rc io.ReadCloser, d time.Duration) io.ReadCloser {
	if d <= 0 {
		return rc
	}
	r := &idleReadCloser{rc: rc, timeout: d}
	r.timer = time.AfterFunc(d, func() {
		r.mu.Lock()
		defer r.mu.Unlock()
		if r.closed {
			return
		}
		r.closed = true
		_ = r.rc.Close()
	})
	return r
}

func (r *idleReadCloser) Read(p []byte) (int, error) {
	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		return 0, io.ErrUnexpectedEOF
	}
	r.timer.Reset(r.timeout)
	r.mu.Unlock()

	n, err := r.rc.Read(p)

	r.mu.Lock()
	if !r.closed {
		r.timer.Reset(r.timeout)
	}
	r.mu.Unlock()
	return n, err
}

func (r *idleReadCloser) Close() error {
	r.mu.Lock()
	r.closed = true
	r.timer.Stop()
	r.mu.Unlock()
	return r.rc.Close()
}

// randomHex 生成随机十六进制串，用于补全缺失的工具调用 ID。
func randomHex(n int) string {
	const alphabet = "0123456789abcdef"
	buf := make([]byte, n)
	if _, err := rand.Read(buf); err != nil {
		// 熵源不可用时退化为时间戳，保证不阻塞请求。
		for i := range buf {
			buf[i] = byte(time.Now().UnixNano() >> uint(i%8*8))
		}
	}
	out := make([]byte, n)
	for i, b := range buf {
		out[i] = alphabet[int(b)%len(alphabet)]
	}
	return string(out)
}

var _ io.Closer = (*upstreamStream)(nil)
