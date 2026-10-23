package workbuddy

import (
	"bytes"
	"context"
	"crypto/md5"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"image"
	"image/png"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/576469377/Agent2API/internal/llm"
)

const imageFixtureHost = "fixture.cos.test.myqcloud.com"

func referenceFixture(t *testing.T) string {
	t.Helper()
	i := image.NewNRGBA(image.Rect(0, 0, 128, 128))
	state := uint64(471928)
	for n := range i.Pix {
		state = state*6364136223846793005 + 1
		i.Pix[n] = byte(state >> 32)
	}
	var b bytes.Buffer
	if err := png.Encode(&b, i); err != nil {
		t.Fatal(err)
	}
	raw := b.Bytes()
	return "data:image/png;base64," + base64.StdEncoding.EncodeToString(raw)
}

type imageFixtureStore struct {
	mu                  sync.Mutex
	objects             map[string][]byte
	puts, signs, checks atomic.Int64
	unavailable         atomic.Bool
	reverse             atomic.Bool
}

func newReferenceAdapter(t *testing.T) (*Adapter, *imageFixtureStore) {
	t.Helper()
	store := &imageFixtureStore{objects: map[string][]byte{}}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == imagePresignPath {
			store.signs.Add(1)
			if store.unavailable.Load() {
				w.WriteHeader(503)
				return
			}
			var in struct {
				Keys []string `json:"object_keys"`
			}
			json.NewDecoder(r.Body).Decode(&in)
			if r.Header.Get("Authorization") == "" {
				t.Error("signing request missing account authorization")
			}
			items := []map[string]string{}
			for _, key := range in.Keys {
				v := "https://" + imageFixtureHost + "/" + key + fmt.Sprintf("?q-sign-time=1%%3B%d&q-signature=fixture", time.Now().Add(time.Hour).Unix())
				items = append(items, map[string]string{"object_key": key, "upload_url": v, "download_url": v})
			}
			if store.reverse.Load() {
				for i, j := 0, len(items)-1; i < j; i, j = i+1, j-1 {
					items[i], items[j] = items[j], items[i]
				}
			}
			json.NewEncoder(w).Encode(map[string]any{"code": 0, "data": map[string]any{"items": items}})
			return
		}
		if r.Header.Get("Authorization") != "" || r.Header.Get("X-User-Id") != "" {
			t.Error("account credentials leaked to object storage")
		}
		if r.Method == http.MethodPut {
			raw, _ := io.ReadAll(r.Body)
			sum := md5.Sum(raw)
			if r.Header.Get("Content-MD5") != base64.StdEncoding.EncodeToString(sum[:]) {
				t.Error("original-byte checksum missing")
			}
			store.mu.Lock()
			store.objects[r.URL.Path] = raw
			store.mu.Unlock()
			store.puts.Add(1)
			return
		}
		store.checks.Add(1)
		store.mu.Lock()
		raw, ok := store.objects[r.URL.Path]
		store.mu.Unlock()
		if !ok {
			w.WriteHeader(404)
			return
		}
		sum := md5.Sum(raw)
		w.Header().Set("ETag", "\""+hex.EncodeToString(sum[:])+"\"")
		if r.Header.Get("Range") == "bytes=0-0" {
			w.WriteHeader(206)
			w.Write(raw[:1])
			return
		}
		w.Write(raw)
	}))
	t.Cleanup(server.Close)
	a, err := New(Config{Platform: GlobalPlatform, BaseURL: server.URL, CredentialPath: globalTestCredential(t, "www.workbuddy.ai")})
	if err != nil {
		t.Fatal(err)
	}
	a.imageRefs = newImageReferenceCache(imageAccountScope(a.cli.baseURL, a.auth.Credential()), "")
	transport := a.cli.streamHTTP.Transport
	t.Cleanup(a.cli.streamHTTP.CloseIdleConnections)
	t.Cleanup(a.cli.http.CloseIdleConnections)
	target, _ := url.Parse(server.URL)
	a.cli.streamHTTP.Transport = uploadRoundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.URL.Host != imageFixtureHost {
			return nil, fmt.Errorf("unexpected object target")
		}
		clone := r.Clone(r.Context())
		u := *r.URL
		u.Scheme, u.Host = target.Scheme, target.Host
		clone.URL = &u
		clone.Host = ""
		return transport.RoundTrip(clone)
	})
	return a, store
}

func TestImageReferencesKeepHistoryAndOriginalBytesAcrossReuseAndRestart(t *testing.T) {
	a, store := newReferenceAdapter(t)
	source := referenceFixture(t)
	req := llm.RequestMessages{Model: "deepseek-v4.1-flash", Messages: []llm.Message{
		{Role: llm.RoleUser, Content: []llm.Content{{Type: llm.ContentText, Text: "Keep earliest context"}, {Type: llm.ContentImage, ImageURL: source}}},
		{Role: llm.RoleAssistant, Content: []llm.Content{{Type: llm.ContentText, Text: "Earlier response"}}},
		{Role: llm.RoleUser, Content: []llm.Content{{Type: llm.ContentImage, ImageURL: source}, {Type: llm.ContentImage, ImageURL: "https://example.com/original.png"}, {Type: llm.ContentText, Text: "Keep latest context"}}},
	}}
	for step := 0; step < 3; step++ {
		ex := llm.NewExecution(6)
		body := buildUpstreamRequest(req, false)
		ctx := llm.WithExecution(context.Background(), ex)
		got, err := a.prepareImageReferences(ctx, body)
		if err != nil {
			t.Fatal(err)
		}
		first := got.Messages[0].Content.([]contentPart)
		last := got.Messages[2].Content.([]contentPart)
		if first[0].Text != "Keep earliest context" || last[2].Text != "Keep latest context" || got.Messages[1].Content != "Earlier response" || first[1].ImageURL.URL != last[0].ImageURL.URL || last[1].ImageURL.URL != "https://example.com/original.png" {
			t.Fatal("history or image order changed")
		}
		if req.Messages[0].Content[1].ImageURL != source {
			t.Fatal("source request was mutated")
		}
		if store.puts.Load() != 1 {
			t.Fatalf("historical image uploaded again at step %d: %d", step, store.puts.Load())
		}
		stats := ex.Snapshot().ImagePreparation
		if stats.ImageReferences != 2 || stats.ImagesPrepared != 2 || stats.ImageReferencesReused != map[bool]int{true: 1, false: 2}[step == 0] {
			t.Fatalf("incorrect reference counts: %+v", stats)
		}
		if step == 1 {
			a.imageRefs.file = filepath.Join(t.TempDir(), "private-references.json")
			a.imageRefs.save()
			a.imageRefs = newImageReferenceCache(a.imageRefs.scope, a.imageRefs.file)
		}
	}
	raw, _ := base64.StdEncoding.DecodeString(strings.SplitN(source, ",", 2)[1])
	store.mu.Lock()
	defer store.mu.Unlock()
	for _, uploaded := range store.objects {
		if !bytes.Equal(raw, uploaded) {
			t.Fatal("image bytes changed")
		}
	}
	if store.checks.Load() != 1 {
		t.Fatal("persisted image was not checked before reuse")
	}
}

func TestImageReferenceRenewalAndDeletedObjectRecovery(t *testing.T) {
	a, store := newReferenceAdapter(t)
	source := referenceFixture(t)
	run := func() {
		t.Helper()
		_, err := a.prepareImageReferences(context.Background(), buildUpstreamRequest(llm.RequestMessages{Model: "deepseek-v4.1-flash", Messages: []llm.Message{{Role: llm.RoleUser, Content: []llm.Content{{Type: llm.ContentImage, ImageURL: source}}}}}, false))
		if err != nil {
			t.Fatal(err)
		}
	}
	run()
	a.imageRefs.mu.Lock()
	for k, v := range a.imageRefs.entries {
		v.ExpiresAt = time.Now().Add(-time.Minute)
		a.imageRefs.entries[k] = v
	}
	a.imageRefs.mu.Unlock()
	run()
	if store.signs.Load() != 2 || store.puts.Load() != 1 {
		t.Fatal("renewing a signature uploaded the picture again")
	}
	store.mu.Lock()
	store.objects = map[string][]byte{}
	store.mu.Unlock()
	a.imageRefs.mu.Lock()
	for k, v := range a.imageRefs.entries {
		v.VerifiedAt = time.Time{}
		a.imageRefs.entries[k] = v
	}
	a.imageRefs.mu.Unlock()
	run()
	if store.puts.Load() != 2 {
		t.Fatal("missing stored image was not restored from original bytes")
	}
}

func TestUnavailableReferenceServicePreservesAllInlineImages(t *testing.T) {
	a, store := newReferenceAdapter(t)
	store.unavailable.Store(true)
	source := referenceFixture(t)
	req := llm.RequestMessages{Model: "deepseek-v4.1-flash", Messages: []llm.Message{{Role: llm.RoleUser, Content: []llm.Content{{Type: llm.ContentText, Text: "Do not lose context"}, {Type: llm.ContentImage, ImageURL: source}}}}}
	for i := 0; i < 2; i++ {
		want := buildUpstreamRequest(req, false)
		before, _ := json.Marshal(want)
		got, err := a.prepareImageReferences(context.Background(), want)
		after, _ := json.Marshal(got)
		if err != nil || !bytes.Equal(before, after) {
			t.Fatal("optional upload failure changed the original history")
		}
	}
	if store.signs.Load() != 1 || store.puts.Load() != 0 {
		t.Fatal("unavailable optional upload service was repeatedly called")
	}
	old := a.imageRefs.scope
	a.imageRefs.scope = "different-account"
	got, _ := a.prepareImageReferences(context.Background(), buildUpstreamRequest(req, false))
	if !reflect.DeepEqual(got, buildUpstreamRequest(req, false)) {
		t.Fatal("image reference crossed accounts")
	}
	a.imageRefs.scope = old
}

func TestBatchImageReferencesMatchObjectsByKeyAndIncludeToolResults(t *testing.T) {
	a, store := newReferenceAdapter(t)
	store.reverse.Store(true)
	first := referenceFixture(t)
	raw, _ := base64.StdEncoding.DecodeString(strings.SplitN(first, ",", 2)[1])
	raw = append(raw, 17)
	second := "data:image/png;base64," + base64.StdEncoding.EncodeToString(raw)
	req := llm.RequestMessages{Model: "deepseek-v4.1-flash", Messages: []llm.Message{
		{Role: llm.RoleUser, Content: []llm.Content{{Type: llm.ContentImage, ImageURL: first}}},
		{Role: llm.RoleTool, Content: []llm.Content{{Type: llm.ContentToolResult, ToolResult: &llm.ToolResult{ToolCallID: "original-tool", Blocks: []llm.Content{{Type: llm.ContentText, Text: "Original tool text"}, {Type: llm.ContentImage, ImageURL: second}}}}}},
	}}
	got, err := a.prepareImageReferences(context.Background(), buildUpstreamRequest(req, false))
	if err != nil {
		t.Fatal(err)
	}
	if store.signs.Load() != 1 || store.puts.Load() != 2 {
		t.Fatalf("first image batch was not signed together: signs=%d puts=%d", store.signs.Load(), store.puts.Load())
	}
	for i, source := range []string{first, second} {
		parts := got.Messages[i].Content.([]contentPart)
		part := parts[len(parts)-1]
		u, _ := url.Parse(part.ImageURL.URL)
		store.mu.Lock()
		stored := store.objects[u.Path]
		store.mu.Unlock()
		original, _ := base64.StdEncoding.DecodeString(strings.SplitN(source, ",", 2)[1])
		if !bytes.Equal(stored, original) {
			t.Fatal("out-of-order signature response associated a reference with the wrong historical image")
		}
	}
	if got.Messages[1].ToolCallID != "original-tool" || got.Messages[1].Content.([]contentPart)[0].Text != "Original tool text" {
		t.Fatal("tool context changed")
	}
}

func TestConcurrentHistoryPreparationUploadsOneCopyPerAccount(t *testing.T) {
	a, store := newReferenceAdapter(t)
	source := referenceFixture(t)
	var wg sync.WaitGroup
	var uploaded atomic.Int64
	for n := 0; n < 20; n++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ex := llm.NewExecution(6)
			ctx := llm.WithExecution(context.Background(), ex)
			req := llm.RequestMessages{Model: "deepseek-v4.1-flash", Messages: []llm.Message{{Role: llm.RoleUser, Content: []llm.Content{{Type: llm.ContentImage, ImageURL: source}}}}}
			got, err := a.prepareImageReferences(ctx, buildUpstreamRequest(req, false))
			if err != nil || strings.HasPrefix(got.Messages[0].Content.([]contentPart)[0].ImageURL.URL, "data:") {
				t.Error("concurrent preparation failed")
			}
			stats := ex.Snapshot()
			uploaded.Add(stats.ImageUploadBytes)
			encoded, _ := json.Marshal(stats)
			if strings.Contains(string(encoded), "q-signature") || strings.Contains(string(encoded), source) {
				t.Error("image data or signed URLs escaped into metrics")
			}
		}()
	}
	wg.Wait()
	raw, _ := base64.StdEncoding.DecodeString(strings.SplitN(source, ",", 2)[1])
	if store.puts.Load() != 1 || uploaded.Load() != int64(len(raw)) {
		t.Fatalf("duplicate concurrent uploads or accounting: puts=%d reported bytes=%d", store.puts.Load(), uploaded.Load())
	}
	cred := a.auth.Credential()
	other := cred
	other.UID = "another-account"
	if imageAccountScope(a.cli.baseURL, cred) == imageAccountScope(a.cli.baseURL, other) || imageAccountScope(a.cli.baseURL, cred) == imageAccountScope("https://other.workbuddy.ai", cred) {
		t.Fatal("reference cache crossed account or origin boundaries")
	}
}

func TestSharedImageUploadKeepsOtherWaitersAliveAndStopsWhenAllLeave(t *testing.T) {
	c := newImageReferenceCache("fixture", "")
	entered, release := make(chan struct{}), make(chan struct{})
	var calls atomic.Int64
	loader := func(ctx context.Context, _ imageReference) (imageReference, int64, error) {
		calls.Add(1)
		close(entered)
		select {
		case <-release:
			return imageReference{URL: "fixture", ExpiresAt: time.Now().Add(time.Hour), VerifiedAt: time.Now()}, 12, nil
		case <-ctx.Done():
			return imageReference{}, 0, ctx.Err()
		}
	}
	first, cancel := context.WithCancel(context.Background())
	firstDone := make(chan error, 1)
	secondDone := make(chan error, 1)
	go func() { _, _, _, err := c.get(first, "key", loader); firstDone <- err }()
	<-entered
	go func() {
		_, reused, uploaded, err := c.get(context.Background(), "key", loader)
		if err == nil && (!reused || uploaded != 0) {
			err = fmt.Errorf("joined transfer counted twice")
		}
		secondDone <- err
	}()
	deadline := time.Now().Add(time.Second)
	for {
		c.mu.Lock()
		waiters := c.flights["key"].waiters
		c.mu.Unlock()
		if waiters == 2 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("second subscriber did not join")
		}
		time.Sleep(time.Millisecond)
	}
	cancel()
	if err := <-firstDone; err != context.Canceled {
		t.Fatal("caller cancellation lost")
	}
	close(release)
	if err := <-secondDone; err != nil || calls.Load() != 1 {
		t.Fatalf("shared upload canceled or repeated: %v calls=%d", err, calls.Load())
	}
	canceled := make(chan struct{})
	caller, stop := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, _, _, err := c.get(caller, "other", func(ctx context.Context, _ imageReference) (imageReference, int64, error) {
			close(canceled)
			<-ctx.Done()
			return imageReference{}, 0, ctx.Err()
		})
		done <- err
	}()
	<-canceled
	stop()
	<-done
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.flights) != 0 {
		t.Fatal("last canceled waiter leaked an upload")
	}
}
