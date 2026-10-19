package workbuddy

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/576469377/Agent2API/internal/llm"
)

func TestInputCacheUsageAliases(t *testing.T) {
	for _, tc := range []struct {
		name, fields string
		read, write  int
	}{
		{"prompt_details", `"prompt_tokens_details":{"cached_tokens":60,"cache_write_tokens":20},"completion_tokens_details":{"cached_tokens":999,"reasoning_tokens":3}`, 60, 20},
		{"anthropic_alias", `"cache_read_input_tokens":60,"cache_creation_input_tokens":20`, 60, 20},
		{"deepseek_alias", `"prompt_cache_hit_tokens":60,"prompt_cache_write_tokens":20`, 60, 20},
		{"flat_alias", `"cached_tokens":60`, 60, 0},
		{"aliases_are_not_additive", `"prompt_tokens_details":{"cached_tokens":60},"cache_read_input_tokens":60,"prompt_cache_hit_tokens":60,"cached_tokens":60`, 60, 0},
		{"reported_zero_wins", `"prompt_tokens_details":{"cached_tokens":0},"cached_tokens":60,"completion_tokens_details":{"cached_tokens":999}`, 0, 0},
		{"output_cache_is_not_input_cache", `"completion_tokens_details":{"cached_tokens":999}`, 0, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			wire := fmt.Sprintf("data: {\"id\":\"test\",\"model\":\"free\",\"choices\":[{\"delta\":{\"content\":\"OK\"},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":100,\"completion_tokens\":5,\"total_tokens\":105,%s}}\n\ndata: [DONE]\n\n", tc.fields)
			s := newUpstreamStream(&http.Response{Body: io.NopCloser(strings.NewReader(wire))})
			defer s.Close()
			var usage *llm.Usage
			for {
				ev, err := s.Recv(context.Background())
				if err == llm.ErrStreamDone {
					break
				}
				if err != nil {
					t.Fatal(err)
				}
				if ev.Type == llm.EventDone {
					usage = ev.Usage
				}
			}
			if usage == nil || usage.CachedTokens != tc.read || usage.CacheWriteTokens != tc.write || usage.InputTokens != 100 {
				t.Fatalf("usage=%+v, want read=%d write=%d total input=100", usage, tc.read, tc.write)
			}
		})
	}
}
