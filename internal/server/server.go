package server

import (
	"context"
	"io/fs"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/lancelot-lewis/MacPanel/internal/app"
	"github.com/lancelot-lewis/MacPanel/internal/audit"
	"github.com/lancelot-lewis/MacPanel/internal/auth"
	"github.com/lancelot-lewis/MacPanel/internal/config"
	"github.com/lancelot-lewis/MacPanel/internal/model"
	"github.com/lancelot-lewis/MacPanel/internal/version"
)

// Options 是 Server 构造参数。
type Options struct {
	Config      config.Config
	Service     *app.Service
	Audit       *audit.Store
	Auth        *auth.Manager
	Logger      *slog.Logger
	WebFS       fs.FS // 内嵌前端资源根目录（可为 nil，此时不提供 UI）
	PublicHosts []string
}

// Server 持有 HTTP 依赖。
type Server struct {
	cfg       config.Config
	svc       *app.Service
	audit     *audit.Store
	auth      *auth.Manager
	log       *slog.Logger
	webFS     fs.FS
	hosts     *hostGuard
	startedAt time.Time
}

// New 创建 Server。
func New(opts Options) *Server {
	if opts.Logger == nil {
		opts.Logger = slog.Default()
	}
	return &Server{
		cfg:       opts.Config,
		svc:       opts.Service,
		audit:     opts.Audit,
		auth:      opts.Auth,
		log:       opts.Logger,
		webFS:     opts.WebFS,
		hosts:     newHostGuard(opts.Config.NormalizedListenAddr(), opts.Config.AllowRemote, opts.PublicHosts),
		startedAt: time.Now().UTC(),
	}
}

// Handler 构建完整路由。
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()

	authz := &authorizer{allowRemote: s.cfg.AllowRemote, sessions: s.auth}

	// --- 无需认证的路由 ---
	//
	// /api/v1/healthz 只返回存活状态，不含任何系统信息，可安全公开。
	// 它用于反向代理健康检查，也用于前端判断后端是否可达。
	mux.HandleFunc("GET /api/v1/healthz", s.handleHealthz)

	// 认证相关：本地模式下 /auth/status 公开（前端据此决定是否显示登录页），
	// 远程模式下 status 也可读，但 login/logout 有独立限速与校验。
	mux.HandleFunc("GET /api/v1/auth/status", s.handleAuthStatus)
	mux.HandleFunc("POST /api/v1/auth/login", s.handleLogin)
	// 设置初始密码仅允许本地模式或已登录状态，二者在 handler 内判断。
	mux.HandleFunc("POST /api/v1/auth/password", s.handleSetPassword)

	// --- 需要认证的 API ---
	api := http.NewServeMux()
	api.HandleFunc("GET /api/v1/system/overview", s.handleOverview)
	api.HandleFunc("GET /api/v1/system/network", s.handleNetwork)
	api.HandleFunc("GET /api/v1/firewall/status", s.handleFirewallStatus)

	// M2+ 接口：明确返回未实现，避免前端伪装成功。
	// M2：防火墙规则读写
	api.HandleFunc("GET /api/v1/firewall/rules", s.handleRulesGet)
	api.HandleFunc("PUT /api/v1/firewall/rules", s.handleRulesSave)
	api.HandleFunc("POST /api/v1/firewall/preview", s.handleRulesPreview)
	api.HandleFunc("POST /api/v1/firewall/apply", s.handleRulesApply)
	api.HandleFunc("POST /api/v1/firewall/confirm", s.handleRulesConfirm)
	api.HandleFunc("POST /api/v1/firewall/rollback", s.handleRulesRollback)
	api.HandleFunc("GET /api/v1/firewall/transaction", s.handleRulesTransaction)
	api.HandleFunc("GET /api/v1/firewall/templates", s.handleRulesTemplates)
	api.HandleFunc("GET /api/v1/firewall/revisions", s.handleRulesRevisions)
	// M3：电源与计划任务
	api.HandleFunc("GET /api/v1/power/status", s.handlePowerStatus)
	api.HandleFunc("POST /api/v1/power/reboot", s.handlePowerReboot)
	api.HandleFunc("POST /api/v1/power/cancel", s.handlePowerCancel)
	api.HandleFunc("GET /api/v1/schedules", s.handleScheduleList)
	api.HandleFunc("POST /api/v1/schedules", s.handleScheduleCreate)
	api.HandleFunc("PUT /api/v1/schedules/{id}", s.handleScheduleUpdate)
	api.HandleFunc("DELETE /api/v1/schedules/{id}", s.handleScheduleDelete)
	api.HandleFunc("GET /api/v1/schedules/executions", s.handleScheduleExecutions)
	api.HandleFunc("POST /api/v1/schedules/{id}/run", s.handleScheduleRunNow)
	api.HandleFunc("POST /api/v1/schedules/sync", s.handleScheduleSync)
	api.HandleFunc("GET /api/v1/services", s.handleNotImplemented("启动项管理"))

	api.HandleFunc("GET /api/v1/audit", s.handleAuditList)
	api.HandleFunc("GET /api/v1/meta", s.handleMeta)
	api.HandleFunc("POST /api/v1/auth/logout", s.handleLogout)

	mux.Handle("/api/v1/", authz.requireAuth(api))

	// --- 前端静态资源 ---
	if s.webFS != nil {
		mux.Handle("/", s.webHandler())
	} else {
		mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
			if strings.HasPrefix(r.URL.Path, "/api/") {
				writeJSON(w, r, http.StatusNotFound, envelope{
					OK:    false,
					Error: model.NewError(model.CodeNotFound, "接口不存在"),
				})
				return
			}
			w.Header().Set("Content-Type", "text/plain; charset=utf-8")
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write([]byte("前端资源未构建：请先运行 make web-build，或使用开发模式 npm run dev。\n"))
		})
	}

	// 中间件顺序（由外到内）：
	// requestID → accessLog → recover → securityHeaders → hostGuard → originGuard → mux
	var h http.Handler = mux
	h = withOriginGuard(s.hosts, h)
	h = withHostGuard(s.hosts, h)
	h = withSecurityHeaders(h)
	h = withRecover(s.log, h)
	h = withAccessLog(s.log, h)
	h = withRequestID(h)
	return h
}

// handleHealthz 返回最小存活信息。
func (s *Server) handleHealthz(w http.ResponseWriter, r *http.Request) {
	writeData(w, r, map[string]any{
		"status":    "ok",
		"version":   version.Version,
		"uptimeSec": int(time.Since(s.startedAt).Seconds()),
	})
}

// handleMeta 返回版本与运行环境信息（不含敏感配置）。
func (s *Server) handleMeta(w http.ResponseWriter, r *http.Request) {
	info := version.Current()
	agentInfo, agentErr := s.svc.AgentHealth(r.Context())

	notes := []string{}
	if agentErr != nil {
		notes = append(notes, "Agent 不可达："+friendlyErr(agentErr))
	}

	writeDataWithMeta(w, r, map[string]any{
		"server":         info,
		"agent":          agentInfo,
		"agentReachable": agentErr == nil,
		"access": map[string]any{
			"mode":        accessMode(s.cfg.AllowRemote),
			"listenAddr":  s.cfg.NormalizedListenAddr(),
			"allowRemote": s.cfg.AllowRemote,
		},
		"features": map[string]any{
			// 明确的能力声明：前端据此禁用按钮，而不是假装可用。
			"firewallRead":    true,
			"firewallWrite":   true,
			"powerControl":    true,
			"serviceControl":  false,
			"scheduleControl": true,
		},
	}, &meta{Degraded: agentErr != nil, Notes: notes})
}

func accessMode(allowRemote bool) string {
	if allowRemote {
		return "remote"
	}
	return "local"
}

func friendlyErr(err error) string {
	e := model.AsError(err)
	if e == nil {
		return ""
	}
	return e.Message
}

// handleNotImplemented 为未实现的接口返回 501，而不是伪造成功。
func (s *Server) handleNotImplemented(feature string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		s.audit.BestEffort(r.Context(), audit.Entry{
			Actor:      ident(r),
			Action:     "api." + r.URL.Path,
			Target:     feature,
			Result:     audit.ResultDenied,
			RemoteAddr: remoteHost(r),
			RequestID:  requestIDFrom(r.Context()),
			Detail:     map[string]any{"reason": "not_implemented", "method": r.Method},
		})
		writeError(w, r, model.NotImplemented(feature))
	}
}

// Shutdown 语义由 cmd 层控制；此处仅暴露启动时间供测试。
func (s *Server) StartedAt() time.Time { return s.startedAt }

var _ = context.Background
