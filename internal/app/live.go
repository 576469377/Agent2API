package app

import (
	"context"
	"io"
	"time"
	"unicode/utf8"

	"github.com/576469377/Agent2API/internal/llm"
	"github.com/576469377/Agent2API/internal/obs"
)

// Observe an unchanged IR stream for both streaming and aggregated responses.
// The collector retains counts, never the generated content.
type observedStream struct {
	inner   llm.ResponseStream
	metrics *obs.Metrics
	record  *obs.Record
}

func (s *observedStream) Recv(ctx context.Context) (llm.ResponseEvent, error) {
	event, err := s.inner.Recv(ctx)
	if err != nil {
		return event, err
	}
	switch event.Type {
	case llm.EventTextDelta, llm.EventThinkingDelta, llm.EventToolCallDelta:
		characters := int64(utf8.RuneCountInString(event.Delta))
		s.record.OutputCharacters += characters
		s.metrics.ObserveLive(s.record.LiveID, characters, time.Now())
	}
	if event.Usage != nil {
		s.record.InputTokens = int64(event.Usage.InputTokens)
		s.record.OutputTokens = int64(event.Usage.OutputTokens)
		s.record.ReasoningTokens = int64(event.Usage.ReasoningTokens)
	}
	return event, nil
}
func (s *observedStream) Close() error {
	if c, ok := s.inner.(io.Closer); ok {
		return c.Close()
	}
	return nil
}
