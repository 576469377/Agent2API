// Package web 提供内置网页控制台。
//
// 采用 go:embed 把静态资源编进二进制，保持「单文件、零依赖」的交付形态——
// 不需要额外的前端构建步骤，也不需要单独的静态服务器。
package web

import (
	"embed"
	"io/fs"
	"net/http"
)

//go:embed static
var staticFS embed.FS

// sub 是 static 目录对应的只读文件系统。
func sub() (fs.FS, error) {
	return fs.Sub(staticFS, "static")
}

// Index 返回控制台页面。
func Index(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "仅支持 GET", http.StatusMethodNotAllowed)
		return
	}
	fsys, err := sub()
	if err != nil {
		http.Error(w, "静态资源不可用", http.StatusInternalServerError)
		return
	}
	raw, err := fs.ReadFile(fsys, "index.html")
	if err != nil {
		http.Error(w, "控制台页面缺失", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	_, _ = w.Write(raw)
}

// Static 返回静态资源处理器（对应 /static/ 前缀）。
func Static() http.Handler {
	fsys, err := sub()
	if err != nil {
		// embed 路径是编译期常量，这里理论上不可达。
		return http.NotFoundHandler()
	}
	return http.FileServer(http.FS(fsys))
}
