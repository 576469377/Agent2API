//go:build workbuddy_live

package workbuddy

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/576469377/Agent2API/internal/config"
	"github.com/576469377/Agent2API/internal/llm"
)

// Explicit opt-in only. Uses a synthetic fixture and a freshly verified zero-rate
// model. Never logs credentials, object keys, signed URLs or private messages.
func TestLiveImageReferenceProbe(t *testing.T) {
	a, root, ctx := liveImageAccount(t)
	raw, err := os.ReadFile(filepath.Join(root, "bin/history-image-optimization-20261024/fixture.png"))
	if err != nil {
		t.Fatal("synthetic fixture missing")
	}
	runLiveImageReferenceProbe(t, a, root, ctx, raw)
}

func liveImageAccount(t *testing.T) (*Adapter, string, context.Context) {
	t.Helper()
	root, _ := filepath.Abs("../../..")
	cfg, err := config.Load(filepath.Join(root, "config.json"))
	if err != nil {
		t.Fatal("local configuration unavailable")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Minute)
	t.Cleanup(cancel)
	request, _ := http.NewRequestWithContext(ctx, "GET", "http://127.0.0.1:8787/api/accounts/manage", nil)
	request.Header.Set("Authorization", "Bearer "+cfg.Auth.APIKey)
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal("local account metadata unavailable")
	}
	var state struct {
		Accounts []struct {
			Platform string
			Healthy  bool
			Identity struct {
				CredentialPath string `json:"credential_path"`
				NeedsRefresh   bool   `json:"needs_refresh"`
			} `json:"identity"`
		} `json:"accounts"`
	}
	err = json.NewDecoder(response.Body).Decode(&state)
	response.Body.Close()
	if err != nil {
		t.Fatal("invalid account metadata")
	}
	path := ""
	for _, ac := range state.Accounts {
		if ac.Platform == GlobalPlatform && ac.Healthy && !ac.Identity.NeedsRefresh {
			path = ac.Identity.CredentialPath
			break
		}
	}
	if path == "" {
		t.Fatal("no current healthy account")
	}
	a, err := New(Config{Platform: GlobalPlatform, CredentialPath: path})
	if err != nil {
		t.Fatal("account could not be loaded")
	}
	models, err := a.ReadModelCatalog(ctx)
	if err != nil {
		t.Fatal("catalogue verification failed")
	}
	free := false
	for _, m := range models {
		if m.ID == "deepseek-v4.1-flash" && m.Pricing != nil && m.Pricing.Status == "free" && m.Pricing.CreditMultiplier != nil && *m.Pricing.CreditMultiplier == 0 && m.SupportsImages {
			free = true
		}
	}
	if !free {
		t.Fatal("zero-rate image model not currently verified")
	}
	return a, root, ctx
}

func runLiveImageReferenceProbe(t *testing.T, a *Adapter, root string, ctx context.Context, raw []byte) {
	key := fmt.Sprintf("uploads/%d-agent2api-synthetic-probe.png", time.Now().UnixMilli())
	var signed struct {
		Code int `json:"code"`
		Data struct {
			Items []struct {
				ObjectKey   string `json:"object_key"`
				UploadURL   string `json:"upload_url"`
				DownloadURL string `json:"download_url"`
			} `json:"items"`
		} `json:"data"`
	}
	err := a.cli.doJSON(ctx, "POST", "/console/as/support/presigned_url", map[string]any{"object_keys": []string{key}}, a.auth.BuildHeaders(headerModeFull), &signed)
	if err != nil {
		t.Fatalf("presign failed: %s", llm.Wrap(err).Code)
	}
	if len(signed.Data.Items) != 1 {
		t.Fatalf("presign returned no item: code=%d", signed.Code)
	}
	item := signed.Data.Items[0]
	u, err := url.Parse(item.UploadURL)
	if err != nil || u.Scheme != "https" {
		t.Fatal("invalid upload target")
	}
	out := map[string]any{"synthetic_only": true, "upload_host": u.Hostname(), "presign_code": signed.Code, "raw_bytes": len(raw)}
	put, _ := http.NewRequestWithContext(ctx, "PUT", item.UploadURL, bytes.NewReader(raw))
	put.Header.Set("Content-Type", "image/png")
	client := &http.Client{Timeout: 30 * time.Second}
	res, err := client.Do(put)
	if err != nil {
		t.Fatal("synthetic upload transport failed")
	}
	io.Copy(io.Discard, res.Body)
	res.Body.Close()
	out["put_status"] = res.StatusCode
	if res.StatusCode != 200 {
		t.Fatal("synthetic upload rejected")
	}
	download, _ := http.NewRequestWithContext(ctx, "GET", item.DownloadURL, nil)
	res, err = client.Do(download)
	if err != nil {
		t.Fatal("signed download failed")
	}
	got, _ := io.ReadAll(io.LimitReader(res.Body, int64(len(raw)+1)))
	res.Body.Close()
	out["download_status"] = res.StatusCode
	out["sha256_identical"] = sha256.Sum256(raw) == sha256.Sum256(got)
	du, _ := url.Parse(item.DownloadURL)
	out["download_host"] = du.Hostname()
	out["signed_download"] = du.RawQuery != ""
	out["signature_window"] = du.Query().Get("q-sign-time")
	unsigned := *du
	unsigned.RawQuery = ""
	res, err = client.Get(unsigned.String())
	if err == nil {
		out["unsigned_download_status"] = res.StatusCode
		io.Copy(io.Discard, res.Body)
		res.Body.Close()
	}
	cache := a.imageRefs
	for _, kind := range []string{"inline", "reference", "cache_cold", "cache_warm", "cache_after_restart"} {
		a.imageRefs = nil
		if strings.HasPrefix(kind, "cache_") {
			a.imageRefs = cache
		}
		if kind == "cache_after_restart" {
			cache.save()
			a.imageRefs = newAccountImageCache(a.cli.baseURL, a.auth.Credential())
		}
		image := item.DownloadURL
		if kind != "reference" {
			image = "data:image/png;base64," + base64.StdEncoding.EncodeToString(raw)
		}
		max := 64
		req := llm.RequestMessages{Model: "deepseek-v4.1-flash", Stream: true, MaxTokens: &max, Messages: []llm.Message{
			{Role: llm.RoleUser, Content: []llm.Content{{Type: llm.ContentText, Text: "Remember this text marker: RIVER-284."}, {Type: llm.ContentImage, ImageURL: image}}},
			{Role: llm.RoleAssistant, Content: []llm.Content{{Type: llm.ContentText, Text: "I have the image and the text marker."}}},
			{Role: llm.RoleUser, Content: []llm.Content{{Type: llm.ContentText, Text: "Read the code from the earlier image and the earlier text marker. Output ONLY code|marker, without explanation."}}},
		}}
		ex := llm.NewExecution(1)
		start := time.Now()
		s, e := a.Stream(llm.WithExecution(ctx, ex), req)
		var answer strings.Builder
		if e == nil {
			for {
				ev, next := s.Recv(ctx)
				if next != nil {
					if next != llm.ErrStreamDone && next != io.EOF {
						e = next
					}
					break
				}
				if ev.Type == llm.EventTextDelta {
					answer.WriteString(ev.Delta)
				}
				if ev.Type == llm.EventError {
					e = ev.Error
					break
				}
				if ev.Type == llm.EventDone {
					break
				}
			}
			if c, ok := s.(io.Closer); ok {
				c.Close()
			}
		}
		trace := ex.Snapshot()
		result := map[string]any{"elapsed_ms": time.Since(start).Milliseconds(), "image_and_text_context_preserved": strings.TrimSpace(answer.String()) == "A7K93Z|RIVER-284", "images": trace.ImagePreparation}
		if len(trace.Attempts) > 0 {
			result["request_bytes"] = trace.Attempts[0].RequestBytes
		}
		if e != nil {
			result["error_code"] = llm.Wrap(e).Code
		}
		out[kind] = result
	}
	report, _ := json.Marshal(out)
	t.Log(string(report))
	os.WriteFile(filepath.Join(root, "bin/history-image-optimization-20261024/probe-report.json"), report, 0600)
}

func TestLiveLargeImageHistoryBenchmark(t *testing.T) {
	a, root, ctx := liveImageAccount(t)
	dir := filepath.Join(root, "bin/history-image-optimization-20261024/large-fixtures")
	files, err := os.ReadDir(dir)
	if err != nil || len(files) != 72 {
		t.Fatal("72 synthetic image fixtures required")
	}
	parts := []llm.Content{{Type: llm.ContentText, Text: "Remember this earlier text marker: RIVER-284. Here are the images in chronological order."}}
	var rawBytes int
	for _, file := range files {
		raw, e := os.ReadFile(filepath.Join(dir, file.Name()))
		if e != nil {
			t.Fatal("fixture read failed")
		}
		rawBytes += len(raw)
		parts = append(parts, llm.Content{Type: llm.ContentImage, ImageURL: "data:image/png;base64," + base64.StdEncoding.EncodeToString(raw)})
	}
	max := 96
	req := llm.RequestMessages{Model: "deepseek-v4.1-flash", Stream: true, MaxTokens: &max, Messages: []llm.Message{
		{Role: llm.RoleUser, Content: parts},
		{Role: llm.RoleAssistant, Content: []llm.Content{{Type: llm.ContentText, Text: "I have seen the image history and the earlier text marker."}}},
		{Role: llm.RoleUser, Content: []llm.Content{{Type: llm.ContentText, Text: "Read the codes in the FIRST and LAST images in the earlier history, and the earlier text marker. Output ONLY firstcode|lastcode|marker. Do not summarize or explain."}}},
	}}
	req.SessionKey = fmt.Sprintf("synthetic-image-history-%d", time.Now().UnixNano())
	cache := newImageReferenceCache(a.imageRefs.scope, filepath.Join(root, fmt.Sprintf("bin/history-image-optimization-20261024/bench-cache-%d.json", time.Now().UnixMilli())))
	out := map[string]any{"synthetic_only": true, "image_count": len(files), "original_image_bytes": rawBytes}
	for _, kind := range []string{"baseline", "cold", "warm", "warm_repeat", "restart"} {
		a.imageRefs = cache
		if kind == "baseline" {
			a.imageRefs = nil
		}
		if kind == "restart" {
			cache.save()
			a.imageRefs = newImageReferenceCache(cache.scope, cache.file)
		}
		ex := llm.NewExecution(1)
		start := time.Now()
		first := int64(0)
		s, e := a.Stream(llm.WithExecution(ctx, ex), req)
		var answer strings.Builder
		if e == nil {
			for {
				ev, next := s.Recv(ctx)
				if next != nil {
					if next != llm.ErrStreamDone && next != io.EOF {
						e = next
					}
					break
				}
				if ev.Type == llm.EventTextDelta {
					if first == 0 {
						first = time.Since(start).Milliseconds()
					}
					answer.WriteString(ev.Delta)
				}
				if ev.Type == llm.EventError {
					e = ev.Error
					break
				}
				if ev.Type == llm.EventDone {
					break
				}
			}
			if c, ok := s.(io.Closer); ok {
				c.Close()
			}
		}
		snap := ex.Snapshot()
		result := map[string]any{"elapsed_ms": time.Since(start).Milliseconds(), "first_text_ms": first, "context_preserved": strings.TrimSpace(answer.String()) == "A7K93Z|B9M42Q|RIVER-284", "images": snap.ImagePreparation}
		if len(snap.Attempts) > 0 {
			p := snap.Attempts[0]
			result["chat_request_bytes"] = p.RequestBytes
			result["chat_upload_ms"] = p.UploadCompletedAtMs - p.UploadStartedAtMs
		}
		if e != nil {
			result["error_code"] = llm.Wrap(e).Code
		}
		out[kind] = result
		line, _ := json.Marshal(map[string]any{"phase": kind, "result": result})
		t.Log(string(line))
	}
	report, _ := json.Marshal(out)
	os.WriteFile(filepath.Join(root, "bin/history-image-optimization-20261024/benchmark-report.json"), report, 0600)
}
