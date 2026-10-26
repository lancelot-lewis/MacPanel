package server

import (
	"io"
	"io/fs"
	"net/http"
	"path"
	"strings"

	"github.com/lancelot-lewis/MacPanel/internal/model"
)

// webHandler 提供内嵌前端资源，并对未知路径回落到 index.html（SPA 路由）。
//
// 安全要点：
//   - 使用 fs.FS 而非拼接磁盘路径，从根本上避免目录穿越；
//   - 仅对无扩展名路径做 SPA 回落，避免把缺失的 .js 请求伪装成 HTML；
//   - 静态资源加长缓存，index.html 不缓存（保证升级后立即生效）。
func (s *Server) webHandler() http.Handler {
	fileServer := http.FileServer(http.FS(s.webFS))

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.Header().Set("Allow", "GET, HEAD")
			writeJSON(w, r, http.StatusMethodNotAllowed, envelope{
				OK:    false,
				Error: model.NewError(model.CodeInvalidArgument, "方法不被允许"),
			})
			return
		}

		clean := path.Clean("/" + r.URL.Path)
		name := strings.TrimPrefix(clean, "/")
		if name == "" {
			name = "index.html"
		}

		if st, err := fs.Stat(s.webFS, name); err == nil && !st.IsDir() {
			// 带内容哈希的构建产物可长期缓存。
			if strings.HasPrefix(clean, "/assets/") {
				w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
			} else {
				w.Header().Set("Cache-Control", "no-cache")
			}
			if ct := contentTypeFor(name); ct != "" {
				w.Header().Set("Content-Type", ct)
			}
			fileServer.ServeHTTP(w, r)
			return
		}

		// 只对「前端路由」回落；带扩展名的静态请求直接 404。
		if path.Ext(clean) != "" && path.Ext(clean) != ".html" {
			http.NotFound(w, r)
			return
		}
		serveIndex(w, r, s.webFS)
	})
}

func serveIndex(w http.ResponseWriter, r *http.Request, webFS fs.FS) {
	f, err := webFS.Open("index.html")
	if err != nil {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte("前端资源缺失：index.html 未找到。\n"))
		return
	}
	defer f.Close()

	stat, err := f.Stat()
	if err != nil {
		http.Error(w, "读取前端资源失败", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	http.ServeContent(w, r, "index.html", stat.ModTime(), f.(io.ReadSeeker))
}

// contentTypeFor 返回扩展名对应类型。Go 的 mime 包已能处理多数类型，
// 这里只补齐 mime 表里可能缺失的项。
func contentTypeFor(name string) string {
	switch strings.ToLower(path.Ext(name)) {
	case ".js", ".mjs":
		return "text/javascript; charset=utf-8"
	case ".css":
		return "text/css; charset=utf-8"
	case ".svg":
		return "image/svg+xml"
	case ".woff2":
		return "font/woff2"
	case ".json":
		return "application/json; charset=utf-8"
	}
	return ""
}

// noCache 供测试辅助：强制响应不缓存。
func noCache(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		h.ServeHTTP(w, r)
	})
}
