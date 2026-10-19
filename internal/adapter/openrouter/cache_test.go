package openrouter

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/576469377/Agent2API/internal/api/anthropic/messages"
	"github.com/576469377/Agent2API/internal/api/openai/chat"
	"github.com/576469377/Agent2API/internal/llm"
)

func TestClientCacheBreakpointsReachOpenRouter(t *testing.T) {
	for _, protocol := range []string{"messages", "chat"} {
		t.Run(protocol, func(t *testing.T) {
			body := `{"model":"vendor/free:free","cache_control":{"type":"ephemeral","ttl":"1h"},"system":[{"type":"text","text":"stable prefix","cache_control":{"type":"ephemeral"}},{"type":"text","text":"second block"}],"messages":[{"role":"user","content":[{"type":"text","text":"question","cache_control":{"type":"ephemeral"}}]}],"tools":[{"name":"read","description":"reads","input_schema":{"type":"object"},"cache_control":{"type":"ephemeral"}}]}`
			var req llm.RequestMessages
			var err error
			if protocol == "messages" {
				req, err = (messages.Protocol{}).DecodeRequest([]byte(body))
			} else {
				body = `{"model":"vendor/free:free","prompt_cache_key":"explicit-key","cache_control":{"type":"ephemeral","ttl":"1h"},"messages":[{"role":"system","content":[{"type":"text","text":"stable prefix","cache_control":{"type":"ephemeral"}},{"type":"text","text":"second block"}]},{"role":"user","content":[{"type":"text","text":"question","cache_control":{"type":"ephemeral"}}]}],"tools":[{"type":"function","function":{"name":"read","description":"reads","parameters":{"type":"object"}},"cache_control":{"type":"ephemeral"}}]}`
				req, err = (chat.Protocol{}).DecodeRequest([]byte(body))
			}
			if err != nil {
				t.Fatal(err)
			}
			req.SessionKey = "s:private-local-session"
			raw, err := json.Marshal(buildRequest(req))
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(string(raw), "private-local-session") {
				t.Fatal("raw local identity leaked into upstream session id")
			}
			var wire struct {
				SessionID    string           `json:"session_id"`
				CacheControl llm.CacheControl `json:"cache_control"`
				Messages     []struct {
					Content []struct {
						Text         string            `json:"text"`
						CacheControl *llm.CacheControl `json:"cache_control"`
					} `json:"content"`
				} `json:"messages"`
				Tools []struct {
					CacheControl *llm.CacheControl `json:"cache_control"`
				} `json:"tools"`
			}
			if err := json.Unmarshal(raw, &wire); err != nil {
				t.Fatal(err)
			}
			if wire.CacheControl.TTL != "1h" || len(wire.Messages) != 2 || len(wire.Messages[0].Content) != 2 || wire.Messages[0].Content[0].Text != "stable prefix" || wire.Messages[0].Content[0].CacheControl == nil || wire.Messages[1].Content[0].CacheControl == nil || len(wire.Tools) != 1 || wire.Tools[0].CacheControl == nil {
				t.Fatalf("cache boundaries lost: %s", raw)
			}
			if wire.SessionID == "" || len(wire.SessionID) > 256 {
				t.Fatal("missing/oversized upstream session")
			}
			next, _ := json.Marshal(buildRequest(req))
			if string(raw) != string(next) {
				t.Fatal("identical requests changed the upstream prefix")
			}
			req.SessionKey = "s:different-session"
			other := buildRequest(req)["session_id"]
			if other == wire.SessionID {
				t.Fatal("independent sessions collapsed")
			}
		})
	}
}

func TestOpenRouterCacheWritesReachUsageWithoutRemovingFreeGuard(t *testing.T) {
	a := testAdapter(t, func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/models") {
			io.WriteString(w, catalogue)
			return
		}
		var wire map[string]any
		if err := json.NewDecoder(r.Body).Decode(&wire); err != nil {
			t.Fatal(err)
		}
		provider := wire["provider"].(map[string]any)
		if provider["max_price"].(map[string]any)["prompt"] != float64(0) {
			t.Fatal("free guard disappeared")
		}
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, "data: {\"id\":\"test\",\"model\":\"vendor/free:free\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"OK\"},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":100,\"completion_tokens\":5,\"total_tokens\":105,\"prompt_tokens_details\":{\"cached_tokens\":60,\"cache_write_tokens\":20}}}\n\ndata: [DONE]\n\n")
	})
	s, err := a.Stream(context.Background(), llm.RequestMessages{Model: "vendor/free:free", Messages: []llm.Message{{Role: llm.RoleUser, Content: []llm.Content{{Type: llm.ContentText, Text: "test"}}}}})
	if err != nil {
		t.Fatal(err)
	}
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
	if usage == nil || usage.CachedTokens != 60 || usage.CacheWriteTokens != 20 || usage.InputTokens != 100 {
		t.Fatalf("usage=%+v", usage)
	}
}

func TestMultipleSystemMessagesKeepTheirCacheBoundaries(t *testing.T) {
	req, err := (chat.Protocol{}).DecodeRequest([]byte(`{"model":"free","messages":[{"role":"system","content":[{"type":"text","text":"first","cache_control":{"type":"ephemeral"}}]},{"role":"system","content":"second"},{"role":"user","content":"question"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	if req.SystemPrompt != "first\n\nsecond" || len(req.SystemBlocks) != 3 || req.SystemBlocks[0].CacheControl == nil {
		t.Fatalf("earlier system/cache prefix was lost: %+v", req.SystemBlocks)
	}
}
