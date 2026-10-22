package app

import (
	"context"
	"errors"
	"io"
	"time"
	"unicode/utf8"

	"github.com/576469377/Agent2API/internal/llm"
	"github.com/576469377/Agent2API/internal/obs"
)

// Observe an unchanged IR stream for both streaming and aggregated responses.
// The collector retains counts, never the generated content.
type observedStream struct {
	inner          llm.ResponseStream
	metrics        *obs.Metrics
	record         *obs.Record
	toolCharacters map[int]int64
	done           bool
}

func (s *observedStream) Recv(ctx context.Context) (llm.ResponseEvent, error) {
	event, err := s.inner.Recv(ctx)
	if err != nil {
		if errors.Is(err, llm.ErrStreamDone) && !s.done {
			return event, &llm.Failure{Code: "upstream_incomplete", Message: "上游响应缺少完成事件", Cause: io.ErrUnexpectedEOF, UpstreamFault: true}
		}
		return event, err
	}
	var characters int64
	if event.Type == llm.EventError && event.Error != nil {
		llm.NoteAttemptFailure(ctx, event.Error)
		return event, event.Error
	}
	switch event.Type {
	case llm.EventTextDelta, llm.EventThinkingDelta, llm.EventToolCallDelta:
		characters = int64(utf8.RuneCountInString(event.Delta))
		if event.Type == llm.EventToolCallDelta {
			if s.toolCharacters == nil {
				s.toolCharacters = map[int]int64{}
			}
			s.toolCharacters[event.ContentIndex] += characters
		}
	case llm.EventToolCallStart, llm.EventToolCallEnd:
		if s.toolCharacters == nil {
			s.toolCharacters = map[int]int64{}
		}
		full := int64(utf8.RuneCountInString(event.ToolArguments))
		characters = max(0, full-s.toolCharacters[event.ContentIndex])
		s.toolCharacters[event.ContentIndex] += characters
	case llm.EventDone:
		s.done = true
	}
	if characters > 0 {
		now := time.Now()
		if s.record.FirstOutputMs == nil {
			ms := max(0, now.Sub(s.record.Time).Milliseconds())
			s.record.FirstOutputMs = &ms
		}
		s.record.OutputCharacters += characters
		s.metrics.ObserveLive(s.record.LiveID, characters, now)
	}
	if event.Usage != nil {
		s.record.UsageReported = true
		s.record.InputTokens = int64(event.Usage.InputTokens)
		s.record.OutputTokens = int64(event.Usage.OutputTokens)
		s.record.ReasoningTokens = int64(event.Usage.ReasoningTokens)
		s.record.CachedTokens = int64(event.Usage.CachedTokens)
		s.record.CacheWriteTokens = int64(event.Usage.CacheWriteTokens)
	}
	return event, nil
}
func (s *observedStream) Close() error {
	if c, ok := s.inner.(io.Closer); ok {
		return c.Close()
	}
	return nil
}
