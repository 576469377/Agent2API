package main

import (
	"bufio"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func testAssets(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	for name, text := range map[string]string{"index.html": "<main>console</main>", "app.js": "export const version = 1;"} {
		if err := os.WriteFile(filepath.Join(root, name), []byte(text), 0600); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

func TestConsoleForwardsAuthenticatedRequests(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if r.Method != "POST" || r.URL.RequestURI() != "/api/accounts/manage?platform=workbuddy-global&action=reset" || r.Header.Get("Authorization") != "Bearer test-secret" || string(body) != `{"label":"account"}` {
			t.Errorf("proxy changed method, route, credentials or body")
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = io.WriteString(w, `{"error":{"message":"quota exhausted"}}`)
	}))
	defer upstream.Close()
	handler, err := newHandler(upstream.URL, testAssets(t))
	if err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest("POST", "/api/accounts/manage?platform=workbuddy-global&action=reset", strings.NewReader(`{"label":"account"}`))
	r.Header.Set("Authorization", "Bearer test-secret")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)
	if w.Code != 429 || !strings.Contains(w.Body.String(), "quota exhausted") {
		t.Fatalf("upstream response changed: %d %s", w.Code, w.Body.String())
	}
}

func TestConsoleFlushesStreamBeforeCompletion(t *testing.T) {
	finish := make(chan struct{})
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: first\n\n")
		w.(http.Flusher).Flush()
		select {
		case <-finish:
		case <-r.Context().Done():
		}
		_, _ = io.WriteString(w, "data: [DONE]\n\n")
	}))
	defer upstream.Close()
	handler, err := newHandler(upstream.URL, testAssets(t))
	if err != nil {
		t.Fatal(err)
	}
	console := httptest.NewServer(handler)
	defer console.Close()
	// Release upstream before closing either server, even on a test failure.
	defer close(finish)
	client := &http.Client{Timeout: 3 * time.Second}
	response, err := client.Get(console.URL + "/v1/chat/completions")
	if err != nil {
		t.Fatalf("first SSE chunk was buffered: %v", err)
	}
	defer response.Body.Close()
	line, err := bufio.NewReader(response.Body).ReadString('\n')
	if err != nil || line != "data: first\n" {
		t.Fatalf("first SSE frame unavailable before completion: %q %v", line, err)
	}
}

func TestConsoleServesUpdatedFilesWithoutGatewayRestart(t *testing.T) {
	root := testAssets(t)
	handler, err := newHandler("http://127.0.0.1:1", root)
	if err != nil {
		t.Fatal(err)
	}
	for _, text := range []string{"first", "updated"} {
		if err := os.WriteFile(filepath.Join(root, "app.js"), []byte(text), 0600); err != nil {
			t.Fatal(err)
		}
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, httptest.NewRequest("GET", "/static/app.js", nil))
		if w.Code != 200 || w.Body.String() != text || w.Header().Get("Cache-Control") != "no-cache" {
			t.Fatalf("asset not updated: %d %s", w.Code, w.Body.String())
		}
	}
	for _, path := range []string{"/static/", "/config.json", "/static/../config.json"} {
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, httptest.NewRequest("GET", path, nil))
		if w.Code != 404 {
			t.Errorf("non-asset path %s exposed: %d", path, w.Code)
		}
	}
}
