package server

import (
	"errors"
	"net/http"

	"github.com/lancelot-lewis/MacPanel/internal/audit"
	"github.com/lancelot-lewis/MacPanel/internal/auth"
	"github.com/lancelot-lewis/MacPanel/internal/model"
)

// authStatusResponse 是 GET /api/v1/auth/status 的返回。
type authStatusResponse struct {
	// Mode 为 local 时前端不需要登录；remote 时必须先登录。
	Mode string `json:"mode"`
	// Authenticated 表示当前请求是否已获授权。
	Authenticated bool `json:"authenticated"`
	// PasswordSet 表示是否已配置管理密码（远程模式必需）。
	PasswordSet bool `json:"passwordSet"`
	// RequiresPasswordSetup 为 true 时前端应引导设置初始密码。
	RequiresPasswordSetup bool `json:"requiresPasswordSetup"`
	// Session 仅在已登录时返回。
	Session *auth.Session `json:"session,omitempty"`
}

// handleAuthStatus 返回认证状态。该接口不返回任何系统数据。
func (s *Server) handleAuthStatus(w http.ResponseWriter, r *http.Request) {
	passwordSet := false
	if s.auth != nil {
		if v, err := s.auth.HasPassword(r.Context()); err == nil {
			passwordSet = v
		}
	}

	resp := authStatusResponse{
		Mode:        accessMode(s.cfg.AllowRemote),
		PasswordSet: passwordSet,
	}

	if !s.cfg.AllowRemote {
		// 本地模式：回环请求即视为已授权。
		resp.Authenticated = isLocalRequest(r)
		writeData(w, r, resp)
		return
	}

	if sess, ok := sessionFrom(r.Context()); ok {
		resp.Authenticated = true
		resp.Session = &sess
	} else if cookie, err := r.Cookie(auth.SessionCookieName); err == nil && cookie.Value != "" {
		// 该路由在认证中间件之外，需要手动校验会话。
		if sess, err := s.auth.ValidateSession(r.Context(), cookie.Value); err == nil {
			resp.Authenticated = true
			resp.Session = &sess
		}
	}
	writeData(w, r, resp)
}

type loginRequest struct {
	Password string `json:"password"`
}

// handleLogin 处理远程模式的密码登录。
//
// 防护：
//   - 按来源 IP 限速（auth 层，5 次/15 分钟）；
//   - 失败统一返回 401 且不区分「无密码」与「密码错」，避免探测；
//   - 成功后轮换会话，旧会话不影响。
func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	if !s.cfg.AllowRemote {
		// 本地模式不提供登录：避免出现两套认证路径。
		writeError(w, r, model.NewError(model.CodeNotImplemented,
			"当前为仅本机访问模式，无需登录"))
		return
	}
	if s.auth == nil {
		writeError(w, r, model.NewError(model.CodeInternal, "认证模块未初始化"))
		return
	}

	var req loginRequest
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, r, err)
		return
	}
	if req.Password == "" {
		writeError(w, r, model.NewError(model.CodeInvalidArgument, "请输入密码"))
		return
	}
	if len(req.Password) > 1024 {
		writeError(w, r, model.NewError(model.CodeInvalidArgument, "密码过长"))
		return
	}

	addr := remoteHost(r)
	err := s.auth.VerifyPassword(r.Context(), addr, req.Password)
	if err != nil {
		switch {
		case errors.Is(err, auth.ErrTooManyAttempts):
			s.auditAuth(r, "auth.login", audit.ResultDenied, "rate_limited")
			writeError(w, r, model.NewError(model.CodeRateLimited,
				"登录尝试过于频繁，请稍后再试"))
		case errors.Is(err, auth.ErrNoPassword):
			// 对外仍返回统一提示，但审计记录真实原因。
			s.auditAuth(r, "auth.login", audit.ResultDenied, "no_password_set")
			writeError(w, r, model.NewError(model.CodeUnauthorized, "用户名或密码错误"))
		default:
			s.auditAuth(r, "auth.login", audit.ResultFailure, "invalid_credentials")
			writeError(w, r, model.NewError(model.CodeUnauthorized, "用户名或密码错误"))
		}
		return
	}

	token, sess, err := s.auth.CreateSession(r.Context(), addr, r.UserAgent())
	if err != nil {
		s.auditAuth(r, "auth.login", audit.ResultFailure, "session_create_failed")
		writeError(w, r, err)
		return
	}
	setSessionCookie(w, r, token, int(auth.SessionTTL.Seconds()))

	s.auditAuth(r, "auth.login", audit.ResultSuccess, "")
	writeData(w, r, map[string]any{"session": sess})
}

// handleLogout 撤销当前会话。
func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	if s.auth != nil {
		if cookie, err := r.Cookie(auth.SessionCookieName); err == nil && cookie.Value != "" {
			_ = s.auth.RevokeSession(r.Context(), cookie.Value)
		}
	}
	clearSessionCookie(w, r)
	s.auditAuth(r, "auth.logout", audit.ResultSuccess, "")
	writeData(w, r, map[string]any{"ok": true})
}

type setPasswordRequest struct {
	// CurrentPassword 在已登录/已设置密码时必填，用于防止会话被劫持后改密码。
	CurrentPassword string `json:"currentPassword"`
	NewPassword     string `json:"newPassword"`
}

// handleSetPassword 设置或修改管理密码。
//
// 授权规则（按「是否已设置密码」分两种，避免出现死锁）：
//
//  1. 尚未设置密码（初始化）：
//     只允许来自回环地址的请求。远程模式下若直接要求登录，
//     会出现「登录需要密码、设密码需要登录」的死锁，
//     等于永远无法启用远程访问。因此引导阶段只认本机。
//
//  2. 已设置密码（修改）：
//     本地模式要求本机；远程模式要求会话有效 + 当前密码正确。
//     两者都必须提供 currentPassword，防止本机任意进程直接改密码。
func (s *Server) handleSetPassword(w http.ResponseWriter, r *http.Request) {
	if s.auth == nil {
		writeError(w, r, model.NewError(model.CodeInternal, "认证模块未初始化"))
		return
	}

	// hasPassword 必须无条件查询：无论哪种模式，
	// 「已设密码」时都要强制校验当前密码。
	hasPassword, err := s.auth.HasPassword(r.Context())
	if err != nil {
		writeError(w, r, err)
		return
	}

	if !hasPassword {
		// 初始化阶段：只允许本机。
		if !isLocalRequest(r) {
			writeError(w, r, model.NewError(model.CodeForbidden,
				"初始密码只能在本机设置：请在运行本服务的 Mac 上打开 http://127.0.0.1:<端口> 完成设置"))
			return
		}
	} else if s.cfg.AllowRemote {
		// 修改阶段 + 远程模式：必须已登录。
		//
		// 注意：本路由注册在认证中间件之外（初始密码设置必须允许未登录访问），
		// 因此 context 中不会有 sessionFrom 注入的会话，必须在此手动校验 cookie。
		if !s.hasValidSession(r) {
			writeError(w, r, model.NewError(model.CodeUnauthorized, "请先登录"))
			return
		}
	} else {
		// 修改阶段 + 本地模式：必须是本机。
		if !isLocalRequest(r) {
			writeError(w, r, model.NewError(model.CodeForbidden, "仅允许在本机修改密码"))
			return
		}
	}

	var req setPasswordRequest
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, r, err)
		return
	}
	if req.NewPassword == "" {
		writeError(w, r, model.NewError(model.CodeInvalidArgument, "新密码不能为空"))
		return
	}
	if len(req.NewPassword) < auth.MinPasswordLength {
		writeError(w, r, model.NewError(model.CodeInvalidArgument,
			"密码长度至少 %d 个字符", auth.MinPasswordLength))
		return
	}

	// 已存在密码时必须验证当前密码，防止「本机任意进程直接改密码」。
	if hasPassword {
		if req.CurrentPassword == "" {
			writeError(w, r, model.NewError(model.CodeInvalidArgument, "修改密码需要提供当前密码"))
			return
		}
		if err := s.auth.VerifyPassword(r.Context(), remoteHost(r), req.CurrentPassword); err != nil {
			s.auditAuth(r, "auth.password", audit.ResultDenied, "current_password_invalid")
			writeError(w, r, model.NewError(model.CodeUnauthorized, "当前密码不正确"))
			return
		}
	}

	if err := s.auth.SetPassword(r.Context(), req.NewPassword); err != nil {
		writeError(w, r, err)
		return
	}

	// 改密码会撤销全部会话；远程模式下为新密码直接发放新会话，避免用户被登出。
	// 初始化（原本无密码）时同样发放：用户刚在本机设完密码，
	// 若同时开着远程页面，可以直接进入已登录状态。
	if s.cfg.AllowRemote {
		token, _, err := s.auth.CreateSession(r.Context(), remoteHost(r), r.UserAgent())
		if err == nil {
			setSessionCookie(w, r, token, int(auth.SessionTTL.Seconds()))
		}
	}

	s.auditAuth(r, "auth.password", audit.ResultSuccess, "")
	writeData(w, r, map[string]any{"ok": true, "initialized": !hasPassword})
}

func (s *Server) auditAuth(r *http.Request, action string, result audit.Result, reason string) {
	if s.audit == nil {
		return
	}
	entry := audit.Entry{
		Actor:      ident(r),
		Action:     action,
		Result:     result,
		RemoteAddr: remoteHost(r),
		RequestID:  requestIDFrom(r.Context()),
	}
	if reason != "" {
		entry.Detail = map[string]any{"reason": reason}
	}
	s.audit.BestEffort(r.Context(), entry)
}

// hasValidSession 手动校验会话。
//
// 用于注册在认证中间件之外、但又需要「已登录」的路由（如修改密码）。
// 中间件内已经注入过会话时直接复用，避免重复查询数据库。
func (s *Server) hasValidSession(r *http.Request) bool {
	if s.auth == nil {
		return false
	}
	if _, ok := sessionFrom(r.Context()); ok {
		return true
	}
	cookie, err := r.Cookie(auth.SessionCookieName)
	if err != nil || cookie.Value == "" {
		return false
	}
	_, err = s.auth.ValidateSession(r.Context(), cookie.Value)
	return err == nil
}
