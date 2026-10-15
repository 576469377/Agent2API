package app

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/576469377/Agent2API/internal/adapter"
	"github.com/576469377/Agent2API/internal/config"
	"github.com/576469377/Agent2API/internal/llm"
	"github.com/576469377/Agent2API/internal/obs"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

type liveTestAdapter struct{ emitted, release chan struct{} }

func (a *liveTestAdapter) Name() string { return "live-test" }
func (a *liveTestAdapter) ListModels(context.Context) ([]adapter.ModelInfo, error) {
	return []adapter.ModelInfo{{ID: "m"}}, nil
}
func (a *liveTestAdapter) Stream(context.Context, llm.RequestMessages) (llm.ResponseStream, error) {
	return &liveTestStream{adapter: a}, nil
}

type liveTestStream struct {
	adapter *liveTestAdapter
	pos     int
}

func (s *liveTestStream) Recv(ctx context.Context) (llm.ResponseEvent, error) {
	s.pos++
	switch s.pos {
	case 1:
		return llm.ResponseEvent{Type: llm.EventStart, Model: "m"}, nil
	case 2:
		return llm.ResponseEvent{Type: llm.EventTextStart}, nil
	case 3:
		return llm.ResponseEvent{Type: llm.EventTextDelta, Delta: "你好world"}, nil
	case 4:
		close(s.adapter.emitted)
		select {
		case <-s.adapter.release:
		case <-ctx.Done():
			return llm.ResponseEvent{}, ctx.Err()
		}
		return llm.ResponseEvent{Type: llm.EventDone, Usage: &llm.Usage{InputTokens: 10, OutputTokens: 3}}, nil
	default:
		return llm.ResponseEvent{}, llm.ErrStreamDone
	}
}
func TestLiveEndpointObservesStreamingAndFinalWithoutChangingOutput(t *testing.T) {
	for _, streaming := range []bool{false, true} {
		t.Run(fmt.Sprint(streaming), func(t *testing.T) {
			adp := &liveTestAdapter{make(chan struct{}), make(chan struct{})}
			var release sync.Once
			t.Cleanup(func() { release.Do(func() { close(adp.release) }) })
			hub, err := NewHub([]*PlatformRuntime{{ID: "p", Adapter: adp}}, log.New(io.Discard, "", 0))
			if err != nil {
				t.Fatal(err)
			}
			cfg := config.Default()
			app := New(cfg, hub, nil)
			server := httptest.NewServer(app.Handler())
			defer func() { release.Do(func() { close(adp.release) }); server.Close() }()
			done := make(chan error, 1)
			go func() {
				response, err := http.Post(server.URL+"/v1/chat/completions", "application/json", strings.NewReader(fmt.Sprintf(`{"model":"m","stream":%v,"messages":[{"role":"user","content":"test"}]}`, streaming)))
				if err == nil {
					body, readErr := io.ReadAll(response.Body)
					response.Body.Close()
					err = readErr
					if !strings.Contains(string(body), "你好world") {
						err = fmt.Errorf("output changed: %s", body)
					}
				}
				done <- err
			}()
			select {
			case <-adp.emitted:
			case <-time.After(2 * time.Second):
				t.Fatal("stream did not produce output")
			}
			response, err := http.Get(server.URL + "/api/live")
			if err != nil {
				t.Fatal(err)
			}
			var live obs.LiveSnapshot
			err = json.NewDecoder(response.Body).Decode(&live)
			response.Body.Close()
			if err != nil {
				t.Fatal(err)
			}
			if live.InFlight != 1 || len(live.Active) != 1 || live.Active[0].OutputCharacters != 7 || live.Active[0].EstimatedOutputTokens != nil {
				t.Fatalf("bad live progress: %+v", live)
			}
			release.Do(func() { close(adp.release) })
			select {
			case err := <-done:
				if err != nil {
					t.Fatal(err)
				}
			case <-time.After(2 * time.Second):
				t.Fatal("request stuck")
			}
			if got := app.metrics.LiveSnapshot(); got.InFlight != 0 || len(got.Active) != 0 {
				t.Fatalf("request leaked: %+v", got)
			}
			if got := app.metrics.Snapshot(); got.OutputTokens != 3 || got.InputTokens != 10 {
				t.Fatal("settled usage changed")
			}
		})
	}
}
