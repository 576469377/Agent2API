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
//
// 必须加 no-cache：go:embed 的文件 ModTime 恒为零值（实测），
// http.FileServer 因此既不发 Last-Modified 也不发 ETag，只能靠
// Cache-Control 阻止浏览器缓存。而 index.html 引用 app.js 时没有版本
// 查询串 —— 一旦 JS 被缓存，升级网关后就会出现「新 HTML 调新字段、
// 旧 JS 读旧字段」的混搭，这类故障最难定位。
func Static() http.Handler {
	fsys, err := sub()
	if err != nil {
		// embed 路径是编译期常量，这里理论上不可达。
		return http.NotFoundHandler()
	}
	return noCache(http.FileServer(http.FS(fsys)))
}

// noCache 给响应加上「每次都要重新校验」的缓存头。
//
// 用 no-cache 而非 no-store：静态文件本身可以留在磁盘缓存里，
// 只是每次使用前必须向服务端确认，既避免混搭又不牺牲重复加载速度。
func noCache(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-cache")
		next.ServeHTTP(w, r)
	})
}
