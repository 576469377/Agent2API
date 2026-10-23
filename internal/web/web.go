// Package web provides the embedded, build-free web console.
package web

import (
	"crypto/sha256"
	"embed"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"strings"
	"sync"
)

//go:embed static
var staticFS embed.FS

type assets struct {
	fsys  fs.FS
	etags map[string]string
}

// Hash immutable embedded files once. Keep serving the FS directly rather than
// retaining another copy of every asset or hashing it on each page load.
func newAssets(fsys fs.FS) (*assets, error) {
	a := &assets{fsys: fsys, etags: make(map[string]string)}
	err := fs.WalkDir(fsys, ".", func(name string, entry fs.DirEntry, err error) error {
		if err != nil || entry.IsDir() {
			return err
		}
		file, err := fsys.Open(name)
		if err != nil {
			return err
		}
		hash := sha256.New()
		_, err = io.Copy(hash, file)
		closeErr := file.Close()
		if err != nil {
			return err
		}
		if closeErr != nil {
			return closeErr
		}
		a.etags[name] = fmt.Sprintf("\"%x\"", hash.Sum(nil))
		return nil
	})
	return a, err
}

var embeddedAssets = sync.OnceValues(func() (*assets, error) {
	fsys, err := fs.Sub(staticFS, "static")
	if err != nil {
		return nil, err
	}
	return newAssets(fsys)
})

func serve(w http.ResponseWriter, r *http.Request, name string) {
	a, err := embeddedAssets()
	if err != nil {
		http.Error(w, "静态资源不可用", http.StatusInternalServerError)
		return
	}
	a.serve(w, r, name)
}

func (a *assets) serve(w http.ResponseWriter, r *http.Request, name string) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		http.Error(w, "仅支持 GET / HEAD", http.StatusMethodNotAllowed)
		return
	}
	etag, ok := a.etags[name]
	if !ok {
		http.NotFound(w, r)
		return
	}
	// Revalidate module imports as well as the entry point after every upgrade.
	// An unchanged asset now returns 304 instead of transferring its body again.
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("ETag", etag)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	http.ServeFileFS(w, r, a.fsys, name)
}

// Index serves the console entry point, including conditional GET and HEAD.
func Index(w http.ResponseWriter, r *http.Request) {
	serve(w, r, "index.html")
}

// Static handles paths after the /static/ prefix has been removed.
func Static() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		serve(w, r, strings.TrimPrefix(r.URL.Path, "/"))
	})
}
