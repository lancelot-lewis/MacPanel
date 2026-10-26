package server

import (
	"context"
	"net/http"
	"strings"

	"github.com/lancelot-lewis/MacPanel/internal/auth"
	"github.com/lancelot-lewis/MacPanel/internal/model"
)

// authorizer 决定请求是否已获授权。
//
// 两种模式：
//   - 本地模式（allowRemote=false）：来自回环地址的请求直接视为已授权。
//     这符合「单机个人自用」的使用场景，也避免为一个本机面板引入登录摩擦。
//     但 Host/Origin 校验仍然生效，因此跨站与 DNS rebinding 仍被阻断。
//   - 远程模式（allowRemote=true）：必须携带有效会话 cookie；请求还必须带
//     自定义头 X-HLM-Request（浏览器跨站请求无法在不触发预检的情况下附加
//     自定义头，因此构成 CSRF 的第三重保护）。
type authorizer struct {
	allowRemote bool
	sessions    *auth.Manager
}

// requireAuth 是认证中间件。
func (a *authorizer) requireAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !a.allowRemote {
			if !isLocalRequest(r) {
				// 本地模式下出现非本机请求：可能是监听地址被改动或前面加了反代，
				// 一律拒绝而不是静默信任。
				writeJSON(w, r, http.StatusForbidden, envelope{
					OK:    false,
					Error: model.NewError(model.CodeForbidden, "当前为仅本机访问模式，拒绝外部请求"),
				})
				return
			}
			next.ServeHTTP(w, r)
			return
		}

		// 远程模式：即使用户从本机访问也要求登录，行为一致、心智负担更低。
		if r.Header.Get("X-HLM-Request") != "1" {
			writeJSON(w, r, http.StatusForbidden, envelope{
				OK:    false,
				Error: model.NewError(model.CodeForbidden, "缺少必需的请求头 X-HLM-Request"),
			})
			return
		}

		cookie, err := r.Cookie(auth.SessionCookieName)
		if err != nil || cookie.Value == "" {
			writeJSON(w, r, http.StatusUnauthorized, envelope{
				OK:    false,
				Error: model.NewError(model.CodeUnauthorized, "未登录"),
			})
			return
		}

		sess, err := a.sessions.ValidateSession(r.Context(), cookie.Value)
		if err != nil {
			clearSessionCookie(w, r)
			writeJSON(w, r, http.StatusUnauthorized, envelope{
				OK:    false,
				Error: model.NewError(model.CodeUnauthorized, "会话已失效，请重新登录"),
			})
			return
		}

		next.ServeHTTP(w, r.WithContext(withSession(r.Context(), sess)))
	})
}

func withSession(ctx context.Context, s auth.Session) context.Context {
	return context.WithValue(ctx, ctxKeySession, s)
}

// ident 返回审计用的调用方标识。
func ident(r *http.Request) string {
	if sess, ok := sessionFrom(r.Context()); ok {
		return "session:" + sess.ID
	}
	if isLocalRequest(r) {
		return "local:" + remoteHost(r)
	}
	return "remote:" + remoteHost(r)
}

// setSessionCookie 写入会话 cookie。
// SameSite=Strict 阻断跨站携带；HttpOnly 阻断脚本读取。
func setSessionCookie(w http.ResponseWriter, r *http.Request, token string, maxAgeSeconds int) {
	http.SetCookie(w, &http.Cookie{
		Name:     auth.SessionCookieName,
		Value:    token,
		Path:     "/",
		HttpOnly: true,
		Secure:   isSecureRequest(r),
		SameSite: http.SameSiteStrictMode,
		MaxAge:   maxAgeSeconds,
	})
}

func clearSessionCookie(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, &http.Cookie{
		Name:     auth.SessionCookieName,
		Value:    "",
		Path:     "/",
		HttpOnly: true,
		Secure:   isSecureRequest(r),
		SameSite: http.SameSiteStrictMode,
		MaxAge:   -1,
	})
}

// isSecureRequest 判断当前连接是否为 HTTPS。
// 代理场景下由 X-Forwarded-Proto 指示；该头只在反向代理正确覆写时可信，
// 因此仅用于设置 cookie 的 Secure 标志，不作为授权依据。
func isSecureRequest(r *http.Request) bool {
	if r.TLS != nil {
		return true
	}
	proto := strings.ToLower(strings.TrimSpace(r.Header.Get("X-Forwarded-Proto")))
	return proto == "https"
}
