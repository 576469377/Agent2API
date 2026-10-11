// console serves the workspace UI without restarting a running gateway.
// API requests are forwarded unchanged to that gateway, including SSE streams.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io/fs"
	"log"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
)

func main() {
	listen := flag.String("listen", "127.0.0.1:8788", "console listen address")
	upstream := flag.String("upstream", "http://127.0.0.1:8787", "running gateway URL")
	assets := flag.String("assets", "internal/web/static", "console asset directory")
	flag.Parse()
	handler, err := newHandler(*upstream, *assets)
	if err != nil {
		log.Fatal(err)
	}
	server := &http.Server{
		Addr: *listen, Handler: handler,
		ReadHeaderTimeout: 10 * time.Second, IdleTimeout: 60 * time.Second,
		// No write timeout: a model stream can run for several minutes.
	}
	log.Printf("console http://%s -> %s (gateway remains running)", *listen, *upstream)
	log.Fatal(server.ListenAndServe())
}

func newHandler(upstream, assets string) (http.Handler, error) {
	target, err := url.Parse(upstream)
	if err != nil || target.Host == "" || (target.Scheme != "http" && target.Scheme != "https") || target.User != nil || target.RawQuery != "" || target.Fragment != "" || (target.Path != "" && target.Path != "/") {
		return nil, fmt.Errorf("upstream must be an HTTP(S) gateway origin")
	}
	root, err := filepath.Abs(assets)
	if err != nil {
		return nil, err
	}
	if _, err := os.Stat(filepath.Join(root, "index.html")); err != nil {
		return nil, fmt.Errorf("console index: %w", err)
	}
	proxy := httputil.NewSingleHostReverseProxy(target)
	proxy.FlushInterval = -1 // Flush every write, including SSE chunks.
	proxy.Transport = http.DefaultTransport.(*http.Transport).Clone()
	proxy.ErrorHandler = func(w http.ResponseWriter, r *http.Request, err error) {
		log.Printf("gateway connection failed: %v", err)
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.WriteHeader(http.StatusBadGateway)
		_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]string{
			"message": "无法连接现有网关，请检查网关地址与运行状态。",
		}})
	}
	files := http.StripPrefix("/static/", http.FileServer(noDirectories{http.Dir(root)}))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/api/") || strings.HasPrefix(r.URL.Path, "/v1/") || r.URL.Path == "/health" || r.URL.Path == "/metrics" {
			proxy.ServeHTTP(w, r)
			return
		}
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.Header().Set("Allow", "GET, HEAD")
			http.Error(w, "仅支持 GET / HEAD", http.StatusMethodNotAllowed)
			return
		}
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		switch {
		case r.URL.Path == "/" || r.URL.Path == "/index.html":
			http.ServeFile(w, r, filepath.Join(root, "index.html"))
		case strings.HasPrefix(r.URL.Path, "/static/"):
			files.ServeHTTP(w, r)
		default:
			http.NotFound(w, r)
		}
	}), nil
}

// Only serve files, never expose directory listings.
type noDirectories struct{ http.FileSystem }

func (f noDirectories) Open(name string) (http.File, error) {
	file, err := f.FileSystem.Open(name)
	if err != nil {
		return nil, err
	}
	stat, err := file.Stat()
	if err != nil || stat.IsDir() {
		_ = file.Close()
		return nil, fs.ErrNotExist
	}
	return file, nil
}
