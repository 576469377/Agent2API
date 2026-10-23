package trae

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/576469377/Agent2API/internal/llm"
)

type stream struct {
	body             io.ReadCloser
	scanner          *bufio.Scanner
	cancel           context.CancelFunc
	idle             *time.Timer
	idleTimeout      time.Duration
	timedOut         atomic.Bool
	closeOnce        sync.Once
	pending          []llm.ResponseEvent
	started, done    bool
	model, id        string
	block            llm.ResponseEventType
	blockIndex, next int
	tools            map[int]*toolState
	order            []int
	usage            *llm.Usage
}
type toolState struct {
	index          int
	id, name, args string
	started        bool
}

func newStream(body io.ReadCloser, cancel context.CancelFunc, idle time.Duration, model string) *stream {
	s := &stream{body: body, cancel: cancel, idleTimeout: idle, model: model, tools: map[int]*toolState{}}
	s.scanner = bufio.NewScanner(body)
	s.scanner.Buffer(make([]byte, 64*1024), 4*1024*1024)
	initialized := make(chan struct{})
	s.idle = time.AfterFunc(idle, func() {
		<-initialized // Publish the timer before Close can read or stop it.
		s.timedOut.Store(true)
		s.Close()
	})
	close(initialized)
	return s
}
func (s *stream) Close() error {
	var err error
	s.closeOnce.Do(func() { s.idle.Stop(); s.cancel(); err = s.body.Close() })
	return err
}
func (s *stream) AccountLabel() string { return "网页 Free 账号" }

func (s *stream) Recv(ctx context.Context) (llm.ResponseEvent, error) {
	stop := context.AfterFunc(ctx, func() { s.Close() })
	defer stop()
	for {
		if len(s.pending) > 0 {
			e := s.pending[0]
			s.pending = s.pending[1:]
			return e, nil
		}
		if s.done {
			return llm.ResponseEvent{}, llm.ErrStreamDone
		}
		if err := ctx.Err(); err != nil {
			s.Close()
			return llm.ResponseEvent{}, err
		}
		event, data, err := s.frame()
		if s.timedOut.Load() {
			s.Close()
			return llm.ResponseEvent{}, &llm.Failure{Code: "trae_stream_idle", Message: "TRAE 流长时间没有数据", Timeout: true, Cause: context.DeadlineExceeded}
		}
		if err != nil {
			s.Close()
			return llm.ResponseEvent{}, llm.Wrap(&llm.Failure{Code: "trae_stream_incomplete", Message: "TRAE 流未正常结束", Cause: err, UpstreamFault: true})
		}
		if err = s.consume(event, data); err != nil {
			s.Close()
			return llm.ResponseEvent{}, err
		}
	}
}

func (s *stream) frame() (string, []byte, error) {
	event := ""
	lines := []string{}
	for s.scanner.Scan() {
		s.idle.Reset(s.idleTimeout)
		line := strings.TrimSuffix(s.scanner.Text(), "\r")
		if line == "" {
			if len(lines) > 0 {
				return event, []byte(strings.Join(lines, "\n")), nil
			}
			continue
		}
		if strings.HasPrefix(line, "event:") {
			event = strings.TrimSpace(line[6:])
		}
		if strings.HasPrefix(line, "data:") {
			lines = append(lines, strings.TrimPrefix(line[5:], " "))
		}
	}
	if len(lines) > 0 {
		return event, []byte(strings.Join(lines, "\n")), nil
	}
	if err := s.scanner.Err(); err != nil {
		return "", nil, err
	}
	return "", nil, io.ErrUnexpectedEOF
}

func (s *stream) consume(event string, raw []byte) error {
	switch event {
	case "metadata":
		var d struct {
			ID json.RawMessage `json:"prompt_completion_id"`
		}
		if json.Unmarshal(raw, &d) != nil {
			return fault("trae_invalid_stream", "TRAE 元数据格式无效")
		}
		// 上游使用大整数 ID，不能经 float64 转换丢失精度。
		s.id = scalarString(d.ID)
	case "output":
		var d struct {
			Response  string `json:"response"`
			Reasoning string `json:"reasoning_content"`
			Calls     []struct {
				Index    int    `json:"index"`
				ID       string `json:"id"`
				Function struct {
					Name      string `json:"name"`
					Arguments string `json:"arguments"`
				} `json:"function_call"`
			} `json:"tool_calls"`
		}
		if json.Unmarshal(raw, &d) != nil {
			return fault("trae_invalid_stream", "TRAE 输出格式无效")
		}
		if !s.started && (d.Response != "" || d.Reasoning != "" || len(d.Calls) > 0) {
			s.started = true
			s.pending = append(s.pending, llm.ResponseEvent{Type: llm.EventStart, Model: s.model, ResponseID: s.id})
		}
		if d.Reasoning != "" {
			s.delta(llm.EventThinkingDelta, d.Reasoning)
		}
		if d.Response != "" {
			s.delta(llm.EventTextDelta, d.Response)
		}
		for _, c := range d.Calls {
			t := s.tools[c.Index]
			if t == nil {
				t = &toolState{index: s.next}
				s.next++
				s.tools[c.Index] = t
				s.order = append(s.order, c.Index)
			}
			if c.ID != "" {
				t.id = c.ID
			}
			t.name += c.Function.Name
			t.args += c.Function.Arguments
			if !t.started {
				if t.id == "" || t.name == "" {
					continue
				}
				s.closeBlock()
				t.started = true
				s.pending = append(s.pending, llm.ResponseEvent{Type: llm.EventToolCallStart, ContentIndex: t.index, ToolCallID: t.id, ToolName: t.name})
				if t.args != "" {
					s.pending = append(s.pending, llm.ResponseEvent{Type: llm.EventToolCallDelta, ContentIndex: t.index, Delta: t.args})
				}
			} else if c.Function.Arguments != "" {
				s.pending = append(s.pending, llm.ResponseEvent{Type: llm.EventToolCallDelta, ContentIndex: t.index, Delta: c.Function.Arguments})
			}
		}
	case "token_usage":
		var u struct {
			Prompt     *int `json:"prompt_tokens"`
			Completion *int `json:"completion_tokens"`
			Total      *int `json:"total_tokens"`
			Read       int  `json:"cache_read_input_tokens"`
			Write      int  `json:"cache_creation_input_tokens"`
			Reasoning  int  `json:"reasoning_tokens"`
		}
		if json.Unmarshal(raw, &u) != nil || u.Read < 0 || u.Write < 0 || u.Reasoning < 0 {
			return fault("trae_invalid_usage", "TRAE 用量格式无效")
		}
		if u.Prompt == nil || u.Completion == nil || u.Total == nil {
			// 空帧或只有缓存字段不能证明一次完整生成的实测用量。
			return nil
		}
		if *u.Prompt < 0 || *u.Completion < 0 || *u.Total < 0 {
			return fault("trae_invalid_usage", "TRAE 用量格式无效")
		}
		s.usage = &llm.Usage{InputTokens: *u.Prompt, OutputTokens: *u.Completion, TotalTokens: *u.Total, CachedTokens: u.Read, CacheWriteTokens: u.Write, ReasoningTokens: u.Reasoning}
	case "done":
		var d struct {
			Reason string `json:"finish_reason"`
		}
		if json.Unmarshal(raw, &d) != nil {
			return fault("trae_invalid_stream", "TRAE 结束事件格式无效")
		}
		if !s.started {
			return fault("trae_empty_stream", "TRAE 没有返回有效回答")
		}
		reason := llm.StopReason(d.Reason)
		if reason == llm.StopReasonError {
			return fault("trae_generation_failed", "TRAE 生成失败")
		}
		if reason == "" {
			return fault("trae_missing_stop_reason", "TRAE 未报告结束原因")
		}
		s.closeBlock()
		for _, key := range s.order {
			t := s.tools[key]
			if !t.started || !json.Valid([]byte(t.args)) {
				return fault("trae_invalid_tool", "TRAE 工具调用不完整")
			}
			s.pending = append(s.pending, llm.ResponseEvent{Type: llm.EventToolCallEnd, ContentIndex: t.index, ToolCallID: t.id, ToolName: t.name, ToolArguments: t.args, ToolArgumentsDone: true})
		}
		if len(s.order) > 0 {
			reason = llm.StopReasonToolCalls
		}
		s.pending = append(s.pending, llm.ResponseEvent{Type: llm.EventDone, Model: s.model, ResponseID: s.id, Usage: s.usage, StopReason: reason})
		s.done = true
		s.Close()
	case "error":
		var d struct {
			Code      json.RawMessage `json:"code"`
			ErrorCode json.RawMessage `json:"error_code"`
		}
		_ = json.Unmarshal(raw, &d)
		code := scalarString(d.Code)
		if code == "" {
			code = scalarString(d.ErrorCode)
		}
		f := fault("trae_upstream_"+safeAuthCode(code), "TRAE 上游报告错误")
		if n, _ := strconv.Atoi(code); n == 4011 || n == 4021 {
			f.QuotaExhausted = true
			f.RateLimited = true
			f.UpstreamFault = false
		}
		return f
		// timing_cost 和 extra_info 可能含内部标识或缓存材料，不进入 IR、日志或样本。
	}
	return nil
}

func (s *stream) delta(kind llm.ResponseEventType, text string) {
	if s.block != kind {
		s.closeBlock()
		s.block = kind
		s.blockIndex = s.next
		s.next++
		start := llm.EventTextStart
		if kind == llm.EventThinkingDelta {
			start = llm.EventThinkingStart
		}
		s.pending = append(s.pending, llm.ResponseEvent{Type: start, ContentIndex: s.blockIndex})
	}
	s.pending = append(s.pending, llm.ResponseEvent{Type: kind, ContentIndex: s.blockIndex, Delta: text})
}
func (s *stream) closeBlock() {
	if s.block == "" {
		return
	}
	end := llm.EventTextEnd
	if s.block == llm.EventThinkingDelta {
		end = llm.EventThinkingEnd
	}
	s.pending = append(s.pending, llm.ResponseEvent{Type: end, ContentIndex: s.blockIndex})
	s.block = ""
}
