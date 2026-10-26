package server

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"log/slog"
	"net"
	"net/http"
	"runtime/debug"
	"strings"
	"time"

	"github.com/lancelot-lewis/MacPanel/internal/auth"
	"github.com/lancelot-lewis/MacPanel/internal/logger"
	"github.com/lancelot-lewis/MacPanel/internal/model"
)

type ctxKey int

const ctxKeySession ctxKey = iota

// requestIDFrom 取出请求 ID。
func requestIDFrom(ctx context.Context) string { return logger.RequestID(ctx) }

// sessionFrom 取出认证会话（仅远程模式存在）。
func sessionFrom(ctx context.Context) (auth.Session, bool) {
	s, ok := ctx.Value(ctxKeySession).(auth.Session)
	return s, ok
}

// newRequestID 生成短请求 ID，便于日志串联。
func newRequestID() string {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		return "unknown"
	}
	return hex.EncodeToString(b)
}

// withRequestID 为每个请求分配 ID。
func withRequestID(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := newRequestID()
		w.Header().Set("X-Request-Id", id)
		next.ServeHTTP(w, r.WithContext(logger.WithRequestID(r.Context(), id)))
	})
}

// statusRecorder 记录响应状态码与字节数，供访问日志使用。
type statusRecorder struct {
	http.ResponseWriter
	status int
	bytes  int
}

func (s *statusRecorder) WriteHeader(code int) {
	if s.status == 0 {
		s.status = code
	}
	s.ResponseWriter.WriteHeader(code)
}

func (s *statusRecorder) Write(b []byte) (int, error) {
	if s.status == 0 {
		s.status = http.StatusOK
	}
	n, err := s.ResponseWriter.Write(b)
	s.bytes += n
	return n, err
}

// withAccessLog 记录访问日志。不记录查询串原文中的敏感值（本应用不使用查询传参）。
func withAccessLog(log *slog.Logger, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w}
		next.ServeHTTP(rec, r)
		if rec.status == 0 {
			rec.status = http.StatusOK
		}
		level := slog.LevelInfo
		if rec.status >= 500 {
			level = slog.LevelError
		} else if rec.status >= 400 {
			level = slog.LevelWarn
		}
		log.Log(r.Context(), level, "http 请求",
			"method", r.Method,
			"path", r.URL.Path,
			"status", rec.status,
			"bytes", rec.bytes,
			"durationMs", time.Since(start).Milliseconds(),
			"remote", remoteHost(r),
			"requestId", requestIDFrom(r.Context()),
		)
	})
}

// withRecover 捕获 panic，返回 500 并记录堆栈，避免单个请求打挂进程。
func withRecover(log *slog.Logger, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if rec := recover(); rec != nil {
				log.Error("请求处理 panic",
					"panic", rec,
					"path", r.URL.Path,
					"requestId", requestIDFrom(r.Context()),
					"stack", string(debug.Stack()),
				)
				writeJSON(w, r, http.StatusInternalServerError, envelope{
					OK:    false,
					Error: model.NewError(model.CodeInternal, "服务器内部错误"),
				})
			}
		}()
		next.ServeHTTP(w, r)
	})
}

// withSecurityHeaders 设置安全响应头。
//
// CSP 说明：前端为同源静态资源 + 内联样式（Vite 构建产物含少量内联），
// 因此 style-src 允许 'unsafe-inline'；script-src 不允许内联，全部由打包产物提供。
func withSecurityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("Cross-Origin-Opener-Policy", "same-origin")
		h.Set("Cross-Origin-Resource-Policy", "same-origin")
		h.Set("Permissions-Policy", "geolocation=(), microphone=(), camera=()")
		h.Set("Content-Security-Policy",
			"default-src 'self'; "+
				"script-src 'self'; "+
				"style-src 'self' 'unsafe-inline'; "+
				"img-src 'self' data:; "+
				"font-src 'self'; "+
				"connect-src 'self'; "+
				"frame-ancestors 'none'; "+
				"base-uri 'self'; "+
				"form-action 'self'")
		next.ServeHTTP(w, r)
	})
}

// hostGuard 校验 Host 头，防御 DNS rebinding。
//
// 允许的 Host：配置的监听地址（含端口）、localhost、以及回环地址。
// 远程模式下还允许显式配置的外部域名（MACPANEL_PUBLIC_HOST）。
type hostGuard struct {
	allowed  map[string]bool
	wildcard bool // 监听所有网卡时，Host 只做基础校验
}

func newHostGuard(listenAddr string, allowRemote bool, publicHosts []string) *hostGuard {
	g := &hostGuard{allowed: map[string]bool{}}
	add := func(h string) {
		h = strings.ToLower(strings.TrimSpace(h))
		if h != "" {
			g.allowed[h] = true
		}
	}
	add(listenAddr)
	if host, port, err := net.SplitHostPort(listenAddr); err == nil {
		add(net.JoinHostPort("localhost", port))
		add(net.JoinHostPort("127.0.0.1", port))
		add(net.JoinHostPort("[::1]", port))
		if host == "" || host == "0.0.0.0" || host == "::" {
			g.wildcard = true
		}
	}
	for _, h := range publicHosts {
		add(h)
	}
	return g
}

func (g *hostGuard) check(r *http.Request) bool {
	host := strings.ToLower(r.Host)
	if host == "" {
		return false
	}
	if g.allowed[host] {
		return true
	}
	// 去掉端口再比一次，兼容默认端口访问。
	if h, _, err := net.SplitHostPort(host); err == nil {
		if g.allowed[h] {
			return true
		}
	}
	if g.wildcard {
		// 监听所有网卡时无法预先枚举 Host，退化为「必须是合法主机名或 IP」。
		h := host
		if hh, _, err := net.SplitHostPort(host); err == nil {
			h = hh
		}
		return isValidHostLiteral(h)
	}
	return false
}

// isValidHostLiteral 判断是否为 IP 或合法主机名（拒绝奇怪字符）。
func isValidHostLiteral(h string) bool {
	if h == "" || len(h) > 253 {
		return false
	}
	if net.ParseIP(h) != nil {
		return true
	}
	for _, r := range h {
		switch {
		case r >= 'a' && r <= 'z':
		case r >= '0' && r <= '9':
		case r == '-' || r == '.':
		default:
			return false
		}
	}
	return true
}

// withHostGuard 拒绝 Host 头不匹配的请求。
func withHostGuard(g *hostGuard, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !g.check(r) {
			writeJSON(w, r, http.StatusMisdirectedRequest, envelope{
				OK:    false,
				Error: model.NewError(model.CodeForbidden, "Host 头不被允许"),
			})
			return
		}
		next.ServeHTTP(w, r)
	})
}

// withOriginGuard 校验 Origin / Referer，阻断跨站表单提交与跨站 fetch。
//
// 规则：变更类方法（非 GET/HEAD/OPTIONS）若携带 Origin 或 Referer，
// 其主机必须是本机允许的 Host；不携带则视为同源请求（浏览器同源策略
// 会为跨站请求强制附带 Origin，因此缺失 Origin 的变更请求风险可控）。
// 远程模式下额外强制要求自定义头 X-HLM-Request，见 withSessionAuth。
func withOriginGuard(g *hostGuard, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet, http.MethodHead, http.MethodOptions:
			next.ServeHTTP(w, r)
			return
		}

		origin := r.Header.Get("Origin")
		referer := r.Header.Get("Referer")
		if origin == "" && referer == "" {
			next.ServeHTTP(w, r)
			return
		}

		var source string
		if origin != "" {
			source = origin
		} else {
			source = referer
		}
		host := originHost(source)
		if host == "" || !g.check(&http.Request{Host: host}) {
			writeJSON(w, r, http.StatusForbidden, envelope{
				OK:    false,
				Error: model.NewError(model.CodeForbidden, "请求来源不被允许"),
			})
			return
		}
		next.ServeHTTP(w, r)
	})
}

// originHost 从 Origin/Referer URL 中提取 host[:port]。
func originHost(u string) string {
	// 形如 https://example.com:7788 或 http://127.0.0.1:7788/path
	schemeEnd := strings.Index(u, "://")
	if schemeEnd < 0 {
		return ""
	}
	rest := u[schemeEnd+3:]
	if i := strings.IndexAny(rest, "/?#"); i >= 0 {
		rest = rest[:i]
	}
	return rest
}

// remoteHost 返回客户端地址的 IP（去掉端口），用于日志与限速键。
func remoteHost(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// isLocalRequest 判断请求是否来自本机。
// 只信任 TCP 对端地址，不信任 X-Forwarded-For（可被伪造）。
func isLocalRequest(r *http.Request) bool {
	host := remoteHost(r)
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
