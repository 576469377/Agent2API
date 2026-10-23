package web

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"
)

func TestEmbeddedAssetsRevalidateAndSupportHead(t *testing.T) {
	for _, test := range []struct {
		name, path string
		handler    http.Handler
	}{
		{"index", "/", http.HandlerFunc(Index)},
		{"module", "/app.js", Static()},
		{"style", "/workbench.css?v=old", Static()},
	} {
		t.Run(test.name, func(t *testing.T) {
			get := httptest.NewRecorder()
			test.handler.ServeHTTP(get, httptest.NewRequest(http.MethodGet, test.path, nil))
			etag := get.Header().Get("ETag")
			if get.Code != http.StatusOK || get.Body.Len() == 0 || etag == "" {
				t.Fatalf("initial asset: status=%d bytes=%d etag=%q", get.Code, get.Body.Len(), etag)
			}
			for _, match := range []string{etag, "W/" + etag, "\"old-version\", " + etag} {
				r := httptest.NewRequest(http.MethodGet, test.path, nil)
				r.Header.Set("If-None-Match", match)
				w := httptest.NewRecorder()
				test.handler.ServeHTTP(w, r)
				if w.Code != http.StatusNotModified || w.Body.Len() != 0 || w.Header().Get("Cache-Control") != "no-cache" || w.Header().Get("ETag") != etag {
					t.Fatalf("conditional asset: status=%d bytes=%d headers=%v", w.Code, w.Body.Len(), w.Header())
				}
			}
			head := httptest.NewRecorder()
			test.handler.ServeHTTP(head, httptest.NewRequest(http.MethodHead, test.path, nil))
			if head.Code != http.StatusOK || head.Body.Len() != 0 || head.Header().Get("Content-Length") != get.Header().Get("Content-Length") || head.Header().Get("ETag") != etag {
				t.Fatalf("HEAD differs from GET: %d %v", head.Code, head.Header())
			}
		})
	}
}

func TestAssetVersionTracksContentsAndPreservesRanges(t *testing.T) {
	var previous string
	for _, body := range []string{"version-one", "version-two"} {
		a, err := newAssets(fstest.MapFS{"app.js": {Data: []byte(body)}})
		if err != nil {
			t.Fatal(err)
		}
		r := httptest.NewRequest(http.MethodGet, "/app.js", nil)
		r.Header.Set("If-None-Match", previous)
		w := httptest.NewRecorder()
		a.serve(w, r, "app.js")
		if w.Code != http.StatusOK || w.Body.String() != body || w.Header().Get("ETag") == previous {
			t.Fatalf("updated asset reused stale version: %d %q", w.Code, w.Body.String())
		}
		previous = w.Header().Get("ETag")
		r = httptest.NewRequest(http.MethodGet, "/app.js", nil)
		r.Header.Set("Range", "bytes=0-6")
		r.Header.Set("If-Range", previous)
		w = httptest.NewRecorder()
		a.serve(w, r, "app.js")
		if w.Code != http.StatusPartialContent || w.Body.String() != "version" {
			t.Fatalf("range lost: %d %q", w.Code, w.Body.String())
		}
	}
}

func TestAssetsRejectDirectoriesMissingPathsAndWrites(t *testing.T) {
	for _, path := range []string{"/", "/missing.js", "/../web.go", "/static/../app.js"} {
		w := httptest.NewRecorder()
		Static().ServeHTTP(w, httptest.NewRequest(http.MethodGet, path, nil))
		if w.Code != http.StatusNotFound || w.Header().Get("ETag") != "" {
			t.Fatalf("unexpected file at %q: %d", path, w.Code)
		}
	}
	w := httptest.NewRecorder()
	Static().ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/app.js", strings.NewReader("replace")))
	if w.Code != http.StatusMethodNotAllowed || w.Header().Get("Allow") != "GET, HEAD" {
		t.Fatalf("write method: %d %v", w.Code, w.Header())
	}
}

type discardResponse struct{ header http.Header }

func (w discardResponse) Header() http.Header                 { return w.header }
func (w discardResponse) WriteHeader(int)                     {}
func (w discardResponse) Write(p []byte) (int, error)         { return len(p), nil }
func (w discardResponse) ReadFrom(r io.Reader) (int64, error) { return io.Copy(io.Discard, r) }

func BenchmarkConsoleRevalidation(b *testing.B) {
	a, err := embeddedAssets()
	if err != nil {
		b.Fatal(err)
	}
	for _, cached := range []bool{false, true} {
		name := "full"
		if cached {
			name = "not-modified"
		}
		b.Run(name, func(b *testing.B) {
			r := httptest.NewRequest(http.MethodGet, "/app.js", nil)
			if cached {
				r.Header.Set("If-None-Match", a.etags["app.js"])
			}
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				a.serve(discardResponse{header: make(http.Header)}, r, "app.js")
			}
		})
	}
}
