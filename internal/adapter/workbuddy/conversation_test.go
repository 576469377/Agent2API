package workbuddy

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/576469377/Agent2API/internal/llm"
)

var conversationIDPattern = regexp.MustCompile(`^[0-9a-f]{32}$`)

type observedConversation struct {
	conversation, request, message, authorization string
}

func observeConversation(r *http.Request) observedConversation {
	return observedConversation{
		conversation:  r.Header.Get("X-Conversation-ID"),
		request:       r.Header.Get("X-Conversation-Request-ID"),
		message:       r.Header.Get("X-Conversation-Message-ID"),
		authorization: r.Header.Get("Authorization"),
	}
}

func consumeConversationStream(t *testing.T, a *Adapter, req llm.RequestMessages) []llm.ResponseEvent {
	t.Helper()
	stream, err := a.Stream(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	defer stream.(interface{ Close() error }).Close()
	var events []llm.ResponseEvent
	for {
		event, err := stream.Recv(context.Background())
		if errors.Is(err, llm.ErrStreamDone) {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		events = append(events, event)
	}
	if countType(events, llm.EventDone) != 1 {
		t.Fatal("stream did not complete")
	}
	return events
}

// 缺少会话头时模拟现网的 11128，成功后回放真实工具调用样本。
func TestChatConversationHeadersRequiredWithoutSession(t *testing.T) {
	fixture, err := os.ReadFile(filepath.Join("..", "..", "..", "fixtures", "workbuddy-stream-text-then-toolcall.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	for _, platform := range []string{"workbuddy", GlobalPlatform} {
		t.Run(platform, func(t *testing.T) {
			captured := make(chan observedConversation, 2)
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				ids := observeConversation(r)
				captured <- ids
				for _, id := range []string{ids.conversation, ids.request, ids.message} {
					if !conversationIDPattern.MatchString(id) {
						w.WriteHeader(http.StatusBadRequest)
						_, _ = w.Write([]byte(`{"code":11128,"msg":"request illegal"}`))
						return
					}
				}
				w.Header().Set("Content-Type", "text/event-stream")
				_, _ = w.Write(fixture)
			}))
			defer srv.Close()
			domain := "www.workbuddy.cn"
			if platform == GlobalPlatform {
				domain = "www.workbuddy.ai"
			}
			a, err := New(Config{Platform: platform, BaseURL: srv.URL, CredentialPath: globalTestCredential(t, domain)})
			if err != nil {
				t.Fatal(err)
			}
			req := llm.RequestMessages{Model: "hy3", Messages: []llm.Message{{Role: llm.RoleUser, Content: []llm.Content{{Type: llm.ContentText, Text: "hello"}}}}}
			var observed []observedConversation
			for i := 0; i < 2; i++ {
				events := consumeConversationStream(t, a, req)
				observed = append(observed, <-captured)
				if countType(events, llm.EventToolCallEnd) != 1 || countType(events, llm.EventTextDelta) == 0 {
					t.Fatal("conversation headers broke the tool stream")
				}
			}
			if len(observed) != 2 || observed[0].conversation == observed[1].conversation || observed[0].request == observed[1].request || observed[0].message == observed[1].message {
				t.Fatal("unidentified requests reused conversation metadata")
			}
		})
	}
}

func TestChatConversationStableAcrossHistoryAndAdapterReload(t *testing.T) {
	const privateSession = "s:private-session\r\nnot-a-header"
	captured := make(chan observedConversation, 6)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ids := observeConversation(r)
		captured <- ids
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		raw, _ := json.Marshal(body)
		if strings.Contains(string(raw), "private-session") || strings.Contains(r.Header.Get("X-Conversation-ID"), "private-session") {
			t.Error("private session key leaked upstream")
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"ok\"}}]}\n\ndata: [DONE]\n\n"))
	}))
	defer srv.Close()
	path := globalTestCredential(t, "www.workbuddy.ai")
	newAdapter := func(platform, path string) *Adapter {
		t.Helper()
		a, err := New(Config{Platform: platform, BaseURL: srv.URL, CredentialPath: path})
		if err != nil {
			t.Fatal(err)
		}
		return a
	}
	a := newAdapter(GlobalPlatform, path)
	req := llm.RequestMessages{Model: "hy3", SessionKey: privateSession, Messages: []llm.Message{{Role: llm.RoleUser, Content: []llm.Content{{Type: llm.ContentText, Text: "first"}}}}}
	consumeConversationStream(t, a, req)
	req.Messages = append(req.Messages, llm.Message{Role: llm.RoleAssistant, Content: []llm.Content{{Type: llm.ContentText, Text: "reply"}}}, llm.Message{Role: llm.RoleUser, Content: []llm.Content{{Type: llm.ContentText, Text: "follow-up"}}})
	consumeConversationStream(t, a, req)
	consumeConversationStream(t, newAdapter(GlobalPlatform, path), req)
	req.SessionKey = "s:another-session"
	consumeConversationStream(t, a, req)
	req.SessionKey = privateSession
	consumeConversationStream(t, newAdapter("workbuddy", globalTestCredential(t, "www.workbuddy.cn")), req)
	otherPath := globalTestCredential(t, "www.workbuddy.ai")
	raw, err := os.ReadFile(otherPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(otherPath, []byte(strings.Replace(string(raw), `"u1"`, `"u2"`, 1)), 0600); err != nil {
		t.Fatal(err)
	}
	consumeConversationStream(t, newAdapter(GlobalPlatform, otherPath), req)
	observed := make([]observedConversation, 6)
	for i := range observed {
		observed[i] = <-captured
	}
	for _, item := range observed {
		if !conversationIDPattern.MatchString(item.conversation) {
			t.Fatal("invalid conversation ID")
		}
	}
	if observed[0].conversation != observed[1].conversation || observed[0].conversation != observed[2].conversation {
		t.Fatal("history growth or adapter reload changed the conversation")
	}
	for _, index := range []int{3, 4, 5} {
		if observed[0].conversation == observed[index].conversation {
			t.Fatal("session, region or account isolation was lost")
		}
	}
	seen := map[string]bool{}
	for _, item := range observed {
		for _, id := range []string{item.request, item.message} {
			if !conversationIDPattern.MatchString(id) || seen[id] {
				t.Fatal("turn IDs were missing or reused across generations")
			}
			seen[id] = true
		}
	}
}

func TestChatConversationMetadataSurvivesAuthRefresh(t *testing.T) {
	captured := make(chan observedConversation, 2)
	a, _, _ := newRetryTestAdapter(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == tokenRefreshPath {
			_, _ = w.Write([]byte(`{"code":0,"data":{"accessToken":"tok-new","refreshToken":"rt-new","expiresIn":3600}}`))
			return
		}
		captured <- observeConversation(r)
		if r.Header.Get("Authorization") == "Bearer tok-old" {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"code":401,"msg":"token expired"}`))
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: [DONE]\n\n"))
	})
	consumeConversationStream(t, a, llm.RequestMessages{Model: "hy3", SessionKey: "stable-session"})
	first, second := <-captured, <-captured
	if first.authorization != "Bearer tok-old" || second.authorization != "Bearer tok-new" {
		t.Fatal("refresh did not update authorization")
	}
	first.authorization, second.authorization = "", ""
	if first != second || !conversationIDPattern.MatchString(first.conversation) {
		t.Fatal("refresh retry changed conversation metadata")
	}
}

func TestChatConversationMetadataSurvivesRouteFallback(t *testing.T) {
	captured := make(chan observedConversation, 2)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		captured <- observeConversation(r)
		if r.URL.Path == chatCompletionsPath {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		if r.URL.Path != "/console/chat/completions" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: [DONE]\n\n"))
	}))
	defer srv.Close()
	a, err := New(Config{Platform: GlobalPlatform, BaseURL: srv.URL, CredentialPath: globalTestCredential(t, "www.workbuddy.ai")})
	if err != nil {
		t.Fatal(err)
	}
	consumeConversationStream(t, a, llm.RequestMessages{Model: "hy3"})
	first, second := <-captured, <-captured
	if first != second || !conversationIDPattern.MatchString(first.conversation) {
		t.Fatal("route fallback changed conversation metadata")
	}
}
