package openrouter

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/576469377/Agent2API/internal/llm"
)

type toolDelta struct {
	Index    int    `json:"index"`
	ID       string `json:"id"`
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"function"`
}

type streamChunk struct {
	ID      string    `json:"id"`
	Model   string    `json:"model"`
	Error   *apiError `json:"error"`
	Choices []struct {
		Index int `json:"index"`
		Delta struct {
			Content          string      `json:"content"`
			Reasoning        string      `json:"reasoning"`
			ReasoningContent string      `json:"reasoning_content"`
			ToolCalls        []toolDelta `json:"tool_calls"`
		} `json:"delta"`
		FinishReason string    `json:"finish_reason"`
		Error        *apiError `json:"error"`
	} `json:"choices"`
	Usage *struct {
		Prompt        int `json:"prompt_tokens"`
		Completion    int `json:"completion_tokens"`
		Total         int `json:"total_tokens"`
		PromptDetails struct {
			Cached     int `json:"cached_tokens"`
			CacheWrite int `json:"cache_write_tokens"`
		} `json:"prompt_tokens_details"`
		CompletionDetails struct {
			Reasoning int `json:"reasoning_tokens"`
		} `json:"completion_tokens_details"`
	} `json:"usage"`
}

type toolState struct {
	index          int
	id, name, args string
	started        bool
}

type upstreamStream struct {
	body              io.ReadCloser
	scanner           *bufio.Scanner
	cancel            context.CancelFunc
	idle              *time.Timer
	idleTimeout       time.Duration
	timedOut          atomic.Bool
	closeOnce         sync.Once
	pending           []llm.ResponseEvent
	started, finished bool
	model, id         string
	next              int
	block             llm.ResponseEventType
	blockIndex        int
	tools             map[int]*toolState
	toolOrder         []int
	stop              llm.StopReason
	usage             *llm.Usage
}

func newStream(resp *http.Response, model string, cancel context.CancelFunc, idle time.Duration) *upstreamStream {
	s := &upstreamStream{body: resp.Body, cancel: cancel, idleTimeout: idle, model: model, tools: map[int]*toolState{}, stop: llm.StopReasonStop}
	s.scanner = bufio.NewScanner(resp.Body)
	s.scanner.Buffer(make([]byte, 64*1024), 4*1024*1024)
	s.idle = time.AfterFunc(idle, func() { s.timedOut.Store(true); cancel() })
	return s
}

func (s *upstreamStream) Close() error {
	var err error
	s.closeOnce.Do(func() { s.idle.Stop(); err = s.body.Close(); s.cancel() })
	return err
}

func (s *upstreamStream) AccountLabel() string { return "API Key" }

func (s *upstreamStream) Recv(ctx context.Context) (llm.ResponseEvent, error) {
	stop := context.AfterFunc(ctx, func() { s.Close() })
	defer stop()
	for {
		if len(s.pending) > 0 {
			ev := s.pending[0]
			s.pending = s.pending[1:]
			return ev, nil
		}
		if s.finished {
			return llm.ResponseEvent{}, llm.ErrStreamDone
		}
		if err := ctx.Err(); err != nil {
			s.Close()
			return llm.ResponseEvent{}, err
		}
		var data []string
		for s.scanner.Scan() {
			s.idle.Reset(s.idleTimeout)
			line := s.scanner.Text()
			if line == "" {
				if len(data) > 0 {
					break
				}
				continue
			}
			if strings.HasPrefix(line, "data:") {
				data = append(data, strings.TrimPrefix(strings.TrimPrefix(line, "data:"), " "))
			}
		}
		if err := ctx.Err(); err != nil {
			s.Close()
			return llm.ResponseEvent{}, err
		}
		if s.timedOut.Load() {
			s.Close()
			return llm.ResponseEvent{}, &llm.Failure{Code: "stream_idle_timeout", Message: "OpenRouter stream idle timeout", Timeout: true, Cause: context.DeadlineExceeded}
		}
		if len(data) == 0 {
			err := s.scanner.Err()
			if err == nil {
				err = io.ErrUnexpectedEOF
			}
			s.Close()
			return llm.ResponseEvent{}, llm.Wrap(&llm.Failure{Code: "upstream_read_failed", Message: "OpenRouter stream ended before [DONE]", Cause: err, UpstreamFault: true})
		}
		payload := strings.Join(data, "\n")
		if strings.TrimSpace(payload) == "[DONE]" {
			if !s.started {
				s.Close()
				return llm.ResponseEvent{}, &llm.Failure{Code: "empty_stream", Message: "OpenRouter returned no completion", UpstreamFault: true}
			}
			s.finish()
			s.finished = true
			s.Close()
			continue
		}
		var chunk streamChunk
		if err := json.Unmarshal([]byte(payload), &chunk); err != nil {
			s.Close()
			return llm.ResponseEvent{}, &llm.Failure{Code: "invalid_stream", Message: "Invalid OpenRouter SSE JSON", Cause: err, UpstreamFault: true}
		}
		if chunk.Error != nil {
			s.Close()
			return llm.ResponseEvent{}, streamFailure(chunk.Error)
		}
		for _, ch := range chunk.Choices {
			if ch.Error != nil {
				s.Close()
				return llm.ResponseEvent{}, streamFailure(ch.Error)
			}
			if ch.FinishReason == "error" {
				s.Close()
				return llm.ResponseEvent{}, &llm.Failure{Code: "upstream_error", Message: "OpenRouter generation failed", UpstreamFault: true}
			}
		}
		s.consume(chunk)
	}
}

func streamFailure(e *apiError) *llm.Failure {
	code, err := strconv.Atoi(strings.Trim(string(e.Code), "\""))
	if err != nil || code < 400 {
		code = 502
	}
	return failure(code, e.summary("Upstream generation failed"))
}

func (s *upstreamStream) consume(c streamChunk) {
	if c.Model != "" {
		s.model = c.Model
	}
	if c.ID != "" {
		s.id = c.ID
	}
	if !s.started && len(c.Choices) > 0 {
		s.started = true
		s.pending = append(s.pending, llm.ResponseEvent{Type: llm.EventStart, Model: s.model, ResponseID: s.id})
	}
	for _, ch := range c.Choices {
		if ch.Index != 0 {
			continue
		}
		d := ch.Delta
		reasoning := d.Reasoning
		if reasoning == "" {
			reasoning = d.ReasoningContent
		}
		if reasoning != "" {
			s.delta(llm.EventThinkingDelta, reasoning)
		}
		if d.Content != "" {
			s.delta(llm.EventTextDelta, d.Content)
		}
		for _, tc := range d.ToolCalls {
			st := s.tools[tc.Index]
			if st == nil {
				st = &toolState{index: s.next}
				s.next++
				s.tools[tc.Index] = st
				s.toolOrder = append(s.toolOrder, tc.Index)
			}
			if tc.ID != "" {
				st.id = tc.ID
			}
			if tc.Function.Name != "" {
				st.name += tc.Function.Name
			}
			st.args += tc.Function.Arguments
			if !st.started {
				if st.id == "" || st.name == "" {
					continue
				}
				s.closeBlock()
				st.started = true
				s.pending = append(s.pending, llm.ResponseEvent{Type: llm.EventToolCallStart, ContentIndex: st.index, ToolCallID: st.id, ToolName: st.name})
				if st.args != "" {
					s.pending = append(s.pending, llm.ResponseEvent{Type: llm.EventToolCallDelta, ContentIndex: st.index, Delta: st.args})
				}
			} else if tc.Function.Arguments != "" {
				s.pending = append(s.pending, llm.ResponseEvent{Type: llm.EventToolCallDelta, ContentIndex: st.index, Delta: tc.Function.Arguments})
			}
		}
		if ch.FinishReason != "" {
			s.stop = llm.StopReason(ch.FinishReason)
		}
	}
	if c.Usage != nil {
		u := c.Usage
		s.usage = &llm.Usage{InputTokens: u.Prompt, OutputTokens: u.Completion, TotalTokens: u.Total, CachedTokens: u.PromptDetails.Cached, CacheWriteTokens: u.PromptDetails.CacheWrite, ReasoningTokens: u.CompletionDetails.Reasoning}
	}
}

func (s *upstreamStream) delta(kind llm.ResponseEventType, text string) {
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

func (s *upstreamStream) closeBlock() {
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

func (s *upstreamStream) finish() {
	s.closeBlock()
	for _, idx := range s.toolOrder {
		t := s.tools[idx]
		if !t.started {
			s.pending = append(s.pending, llm.ResponseEvent{Type: llm.EventError, Error: &llm.Failure{Code: "invalid_tool_call", Message: fmt.Sprintf("OpenRouter tool call %d missing id/name", idx), UpstreamFault: true}})
			return
		}
		s.pending = append(s.pending, llm.ResponseEvent{Type: llm.EventToolCallEnd, ContentIndex: t.index, ToolCallID: t.id, ToolName: t.name, ToolArguments: t.args, ToolArgumentsDone: true})
	}
	s.pending = append(s.pending, llm.ResponseEvent{Type: llm.EventDone, Model: s.model, ResponseID: s.id, Usage: s.usage, StopReason: s.stop})
}
