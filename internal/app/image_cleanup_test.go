package app

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/576469377/Agent2API/internal/config"
	"github.com/576469377/Agent2API/internal/llm"
	"github.com/576469377/Agent2API/internal/obs"
)

func TestTrimOldestImagesPreservesNewestAndDoesNotMutateHistory(t *testing.T) {
	result := &llm.ToolResult{ToolCallID: "call-1", Content: "tool text", IsError: true, Blocks: []llm.Content{
		{Type: llm.ContentText, Text: "tool text"}, {Type: llm.ContentImage, ImageURL: "image-2"},
	}}
	req := llm.RequestMessages{Model: "hy3", SystemPrompt: "system", SessionKey: "stable-session", Messages: []llm.Message{
		{Role: llm.RoleUser, Content: []llm.Content{{Type: llm.ContentText, Text: "first text"}, {Type: llm.ContentImage, ImageURL: "image-1"}}},
		{Role: llm.RoleAssistant, Content: []llm.Content{{Type: llm.ContentThinking, Thinking: "thought", ThinkingSignature: "signature"}, {Type: llm.ContentToolCall, ToolCall: &llm.ToolCall{ID: "call-1", Name: "read", Arguments: "{}"}}}},
		{Role: llm.RoleTool, ToolCallID: "call-1", Content: []llm.Content{{Type: llm.ContentToolResult, ToolResult: result}}},
		{Role: llm.RoleUser, Content: []llm.Content{{Type: llm.ContentImage, ImageURL: "image-3"}, {Type: llm.ContentText, Text: "continue"}}},
	}, Tools: []llm.ToolDefinition{{Name: "read"}}}
	before, _ := json.Marshal(req)
	prepared, removed := trimOldestRequestImages(req, 1)
	if removed != 2 || decodedImageCount(prepared) != 1 || requestImageCount(prepared) != 1 || len(prepared.Messages) != len(req.Messages) {
		t.Fatal("cleanup must retain exactly the newest image without dropping messages")
	}
	if prepared.Messages[3].Content[0].ImageURL != "image-3" || prepared.Messages[0].Content[1].Text != omittedImageNote {
		t.Fatal("cleanup did not keep newest image or expose an omission marker")
	}
	keptResult := prepared.Messages[2].Content[0].ToolResult
	if keptResult.ToolCallID != result.ToolCallID || !keptResult.IsError || !strings.HasPrefix(keptResult.Content, "tool text") || !strings.Contains(keptResult.Content, "省略") {
		t.Fatal("tool result identity, original text and explicit omission marker must survive")
	}
	if !reflect.DeepEqual(req.Messages[1], prepared.Messages[1]) || !reflect.DeepEqual(req.Tools, prepared.Tools) || prepared.SessionKey != req.SessionKey || prepared.SystemPrompt != req.SystemPrompt {
		t.Fatal("cleanup changed tools, thinking, system prompt or affinity")
	}
	after, _ := json.Marshal(req)
	if !bytes.Equal(before, after) {
		t.Fatal("cleanup mutated original client history or nested pointers")
	}
	// A later route can still prepare the same original request with another limit.
	other, removed := trimOldestRequestImages(req, 2)
	if removed != 1 || decodedImageCount(other) != 2 || other.Messages[2].Content[0].ToolResult.Blocks[1].ImageURL != "image-2" {
		t.Fatal("a prior route's cleanup leaked into a later route")
	}
}

func TestImageOnlyToolResultRemainsACompleteResult(t *testing.T) {
	req := llm.RequestMessages{Messages: []llm.Message{{Role: llm.RoleTool, ToolCallID: "call-1", Content: []llm.Content{{Type: llm.ContentToolResult, ToolResult: &llm.ToolResult{ToolCallID: "call-1", Blocks: []llm.Content{
		{Type: llm.ContentText, Text: "original block text"}, {Type: llm.ContentImage, ImageURL: "old"},
	}}}}}, {Role: llm.RoleUser, Content: []llm.Content{{Type: llm.ContentImage, ImageURL: "new"}}}}}
	prepared, removed := trimOldestRequestImages(req, 1)
	result := prepared.Messages[0].Content[0].ToolResult
	if removed != 1 || result.ToolCallID != "call-1" || !strings.Contains(result.Content, "original block text") || !strings.Contains(result.Content, "省略") || prepared.Messages[0].Role != llm.RoleTool {
		t.Fatal("removing all images from a tool result must preserve its text and association")
	}
}

func TestZCodeRepeatedContinueSucceedsWithOptInCleanup(t *testing.T) {
	for _, tc := range []struct {
		model        string
		count, limit int
	}{{"step-5-preview", 61, 60}, {"hy3", 201, 200}} {
		for _, stream := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/%t", tc.model, stream), func(t *testing.T) {
				cfg := config.Default()
				qualified := "workbuddy-global/" + tc.model
				cfg.Models.ImageOverflowPolicies = map[string]string{qualified: config.ImageOverflowTrimOldest}
				a, _, global, logs := imageGuardApp(t, cfg)
				body := anthropicImageHistory(qualified, tc.count, stream)
				for repeat := 0; repeat < 2; repeat++ {
					w := httptest.NewRecorder()
					a.Handler().ServeHTTP(w, httptest.NewRequest("POST", "/v1/messages", bytes.NewReader(body)))
					if w.Code != 200 || decodedImageCount(global.got) != tc.limit || w.Header().Get("X-Agent2API-Images-Removed") != "1" {
						t.Fatalf("continuation stuck after cleanup: status=%d body=%s", w.Code, w.Body.String())
					}
					firstResult := global.got.Messages[1].Content[0].ToolResult
					if firstResult.ToolCallID != "call_0" || !strings.Contains(firstResult.Content, "省略") {
						t.Fatal("old image was not replaced while keeping the tool result")
					}
					row := a.metrics.Snapshot().Recent[0]
					if *row.ImageCount != tc.count || *row.ForwardedImageCount != tc.limit || row.RemovedImageCount != 1 || row.ImageOverflowPolicy != config.ImageOverflowTrimOldest {
						t.Fatal("cleanup must be visible and distinguish received versus forwarded images")
					}
				}
				if global.calls != 2 || !strings.Contains(logs.String(), "images_removed=1") || countWireImages(body, "anthropic-messages") != tc.count {
					t.Fatal("retry was replayed, client history changed, or cleanup was not logged")
				}
			})
		}
	}
}

func TestCleanupDoesNotMisdiagnoseSubsequentProvider400(t *testing.T) {
	cfg := config.Default()
	cfg.Models.ImageOverflowPolicies = map[string]string{"workbuddy-global/step-5-preview": config.ImageOverflowTrimOldest}
	a, _, global, _ := imageGuardApp(t, cfg)
	global.err = &llm.Failure{Code: "http_400", Message: "Invalid request parameters", ClientFixable: true}
	w := httptest.NewRecorder()
	a.Handler().ServeHTTP(w, httptest.NewRequest("POST", "/v1/messages", bytes.NewReader(anthropicImageHistory("workbuddy-global/step-5-preview", 61, true))))
	if w.Code != 400 || !strings.Contains(w.Body.String(), "原因未知") || strings.Contains(w.Body.String(), "image_count_exceeded") || decodedImageCount(global.got) != 60 {
		t.Fatal("a 400 after trimming below the limit must not be called image overflow")
	}
}

func TestCleanupFallbackUsesOriginalHistoryAndFinalAttemptStatistics(t *testing.T) {
	cfg := config.Default()
	cfg.Models.FallbackOnUnavailable = true
	cfg.Models.ImageLimits = map[string]int{"workbuddy/hy3": 1, "workbuddy-global/hy3": 2}
	cfg.Models.ImageOverflowPolicies = map[string]string{"workbuddy/hy3": config.ImageOverflowTrimOldest}
	a, cn, global, _ := imageGuardApp(t, cfg)
	cn.err = &llm.Failure{Code: "rate_limited", RateLimited: true}
	w := httptest.NewRecorder()
	a.Handler().ServeHTTP(w, httptest.NewRequest("POST", "/v1/messages", bytes.NewReader(anthropicImageHistory("hy3", 2, true))))
	row := a.metrics.Snapshot().Recent[0]
	if w.Code != 200 || decodedImageCount(cn.got) != 1 || decodedImageCount(global.got) != 2 || row.RemovedImageCount != 0 || *row.ForwardedImageCount != 2 || row.Platform != "workbuddy-global" || w.Header().Get("X-Agent2API-Images-Removed") != "" {
		t.Fatal("cleanup or its diagnostics leaked from the primary into the fallback")
	}
}

func TestCleanupUnknownAndDomesticModelsKeepAllImages(t *testing.T) {
	cfg := config.Default()
	cfg.Models.ImageOverflowPolicies = map[string]string{"workbuddy-global/hy3": config.ImageOverflowTrimOldest, "workbuddy-global/deepseek-v4.1-flash": config.ImageOverflowTrimOldest}
	a, cn, global, _ := imageGuardApp(t, cfg)
	for _, model := range []string{"workbuddy/hy3", "workbuddy-global/deepseek-v4.1-flash"} {
		w := httptest.NewRecorder()
		a.Handler().ServeHTTP(w, httptest.NewRequest("POST", "/v1/messages", bytes.NewReader(anthropicImageHistory(model, 248, true))))
		used := global
		if model == "workbuddy/hy3" {
			used = cn
		}
		if w.Code != 200 || decodedImageCount(used.got) != 248 || a.metrics.Snapshot().Recent[0].RemovedImageCount != 0 {
			t.Fatal("cleanup must not guess limits or affect another region")
		}
	}
}

func TestCleanupFailsClosedWhenImageDecodingCannotMatchCount(t *testing.T) {
	cfg := config.Default()
	cfg.Models.ImageLimits = map[string]int{"workbuddy-global/hy3": 1}
	cfg.Models.ImageOverflowPolicies = map[string]string{"workbuddy-global/hy3": config.ImageOverflowTrimOldest}
	a, _, global, _ := imageGuardApp(t, cfg)
	count := 2
	req := llm.RequestMessages{Model: "hy3", InputImageCount: &count, Messages: []llm.Message{{Role: llm.RoleUser, Content: []llm.Content{{Type: llm.ContentImage, ImageURL: "valid"}}}}}
	_, _, err := a.openRoutedStream(context.Background(), req, "workbuddy-global", &obs.Record{})
	if err == nil || llm.Wrap(err).Code != "image_cleanup_unavailable" || global.calls != 0 || decodedImageCount(req) != 1 {
		t.Fatal("uncertain decoding must not silently delete images or replay the request")
	}
}

func TestImageCleanupAcrossStandardProtocols(t *testing.T) {
	for _, tc := range []struct{ path, body string }{
		{"/v1/chat/completions", `{"model":"workbuddy-global/hy3","messages":[{"role":"user","content":[{"type":"text","text":"keep text"},{"type":"image_url","image_url":{"url":"old-image"}},{"type":"image_url","image_url":{"url":"new-image"}}]}]}`},
		{"/v1/responses", `{"model":"workbuddy-global/hy3","input":[{"type":"function_call","call_id":"call-1","name":"read","arguments":"{}"},{"type":"function_call_output","call_id":"call-1","output":[{"type":"input_text","text":"keep tool text"},{"type":"input_image","image_url":"old-image"}]},{"role":"user","content":[{"type":"input_image","image_url":"new-image"},{"type":"input_text","text":"continue"}]}]}`},
	} {
		cfg := config.Default()
		cfg.Models.ImageLimits = map[string]int{"workbuddy-global/hy3": 1}
		cfg.Models.ImageOverflowPolicies = map[string]string{"workbuddy-global/hy3": config.ImageOverflowTrimOldest}
		a, _, global, _ := imageGuardApp(t, cfg)
		w := httptest.NewRecorder()
		a.Handler().ServeHTTP(w, httptest.NewRequest("POST", tc.path, strings.NewReader(tc.body)))
		if w.Code != 200 || decodedImageCount(global.got) != 1 || w.Header().Get("X-Agent2API-Images-Forwarded") != "1" {
			t.Fatalf("cleanup failed for %s: %s", tc.path, w.Body.String())
		}
		encoded, _ := json.Marshal(global.got.Messages)
		if bytes.Contains(encoded, []byte("old-image")) || !bytes.Contains(encoded, []byte("new-image")) || !bytes.Contains(encoded, []byte("keep")) {
			t.Fatal("cleanup did not preserve latest image and original text")
		}
	}
}
