package app

import (
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/576469377/Agent2API/internal/llm"
)

func TestCacheUsageSurvivesEveryProtocolAndMetrics(t *testing.T) {
	for _, path := range []string{"/v1/chat/completions", "/v1/messages", "/v1/responses"} {
		for _, stream := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/stream=%t", path, stream), func(t *testing.T) {
				a := newTestApp(t, &fakeAdapter{events: []llm.ResponseEvent{
					{Type: llm.EventStart, Model: "m"}, {Type: llm.EventTextDelta, Delta: "OK"},
					{Type: llm.EventDone, StopReason: llm.StopReasonStop, Usage: &llm.Usage{InputTokens: 100, OutputTokens: 10, TotalTokens: 110, ReasoningTokens: 2, CachedTokens: 60, CacheWriteTokens: 20}},
				}})
				body := fmt.Sprintf(`{"model":"m","messages":[{"role":"user","content":"test"}],"input":"test","max_tokens":16,"stream":%t,"stream_options":{"include_usage":true}}`, stream)
				w := httptest.NewRecorder()
				a.Handler().ServeHTTP(w, httptest.NewRequest("POST", path, strings.NewReader(body)))
				if w.Code != 200 {
					t.Fatalf("HTTP %d: %s", w.Code, w.Body.String())
				}
				var object map[string]any
				if stream {
					for _, line := range strings.Split(w.Body.String(), "\n") {
						if !strings.HasPrefix(line, "data:") {
							continue
						}
						var event map[string]any
						if json.Unmarshal([]byte(strings.TrimSpace(strings.TrimPrefix(line, "data:"))), &event) != nil {
							continue
						}
						if response, ok := event["response"].(map[string]any); ok {
							event = response
						}
						if usage, ok := event["usage"].(map[string]any); ok && usage["output_tokens"] == float64(10) {
							object = event
						}
						if usage, ok := event["usage"].(map[string]any); ok && usage["completion_tokens"] == float64(10) {
							object = event
						}
					}
				} else if err := json.Unmarshal(w.Body.Bytes(), &object); err != nil {
					t.Fatal(err)
				}
				usage, ok := object["usage"].(map[string]any)
				if !ok {
					t.Fatalf("missing usage: %s", w.Body.String())
				}
				if path == "/v1/messages" {
					if usage["input_tokens"] != float64(20) || usage["cache_read_input_tokens"] != float64(60) || usage["cache_creation_input_tokens"] != float64(20) {
						t.Fatalf("Anthropic input counts overlap or disappear: %v", usage)
					}
				} else {
					detailName := "prompt_tokens_details"
					outputName := "completion_tokens_details"
					if path == "/v1/responses" {
						detailName = "input_tokens_details"
						outputName = "output_tokens_details"
					}
					details, ok := usage[detailName].(map[string]any)
					if !ok || details["cached_tokens"] != float64(60) || details["cache_write_tokens"] != float64(20) {
						t.Fatalf("cache detail missing: %v", usage)
					}
					output, ok := usage[outputName].(map[string]any)
					if !ok || output["reasoning_tokens"] != float64(2) || output["cached_tokens"] != nil {
						t.Fatalf("output usage mixes input cache: %v", usage)
					}
				}
				recent := a.metrics.Snapshot().Recent
				if len(recent) != 1 || recent[0].CachedTokens != 60 || recent[0].CacheWriteTokens != 20 || recent[0].InputTokens != 100 {
					t.Fatalf("metrics lost cache or changed total input: %+v", recent)
				}
			})
		}
	}
}
