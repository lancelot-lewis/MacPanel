package server

import (
	"net/http"

	"github.com/lancelot-lewis/MacPanel/internal/audit"
	"github.com/lancelot-lewis/MacPanel/internal/model"
)

// handleOverview 返回系统概览。
//
// 关键约定：Agent 返回的 Missing 字段直接透传给前端，
// 存在缺失项时 meta.degraded=true，让 UI 能区分「确实为 0」和「读不到」。
func (s *Server) handleOverview(w http.ResponseWriter, r *http.Request) {
	ov, err := s.svc.SystemOverview(r.Context())
	if err != nil {
		s.auditRead(r, "system.overview", err)
		writeError(w, r, err)
		return
	}

	var notes []string
	for _, m := range ov.Missing {
		notes = append(notes, "指标不可用："+m.Metric+"（"+m.Reason+"）")
	}
	if s.audit != nil {
		s.audit.BestEffort(r.Context(), audit.Entry{
			Actor:      ident(r),
			Action:     "system.overview",
			Result:     audit.ResultSuccess,
			RemoteAddr: remoteHost(r),
			RequestID:  requestIDFrom(r.Context()),
		})
	}
	writeDataWithMeta(w, r, ov, &meta{Degraded: len(ov.Missing) > 0, Notes: notes})
}

// handleNetwork 返回网络接口快照。
func (s *Server) handleNetwork(w http.ResponseWriter, r *http.Request) {
	snap, err := s.svc.NetworkSnapshot(r.Context())
	if err != nil {
		s.auditRead(r, "system.network", err)
		writeError(w, r, err)
		return
	}

	var notes []string
	for _, m := range snap.Missing {
		notes = append(notes, "数据不可用："+m.Metric+"（"+m.Reason+"）")
	}
	s.audit.BestEffort(r.Context(), audit.Entry{
		Actor:      ident(r),
		Action:     "system.network",
		Result:     audit.ResultSuccess,
		RemoteAddr: remoteHost(r),
		RequestID:  requestIDFrom(r.Context()),
	})
	writeDataWithMeta(w, r, snap, &meta{Degraded: len(snap.Missing) > 0, Notes: notes})
}

// handleFirewallStatus 返回防火墙只读状态。
func (s *Server) handleFirewallStatus(w http.ResponseWriter, r *http.Request) {
	st, err := s.svc.FirewallStatus(r.Context())
	if err != nil {
		s.auditRead(r, "firewall.status", err)
		writeError(w, r, err)
		return
	}

	s.audit.BestEffort(r.Context(), audit.Entry{
		Actor:      ident(r),
		Action:     "firewall.status",
		Target:     st.Anchor.Name,
		Result:     audit.ResultSuccess,
		RemoteAddr: remoteHost(r),
		RequestID:  requestIDFrom(r.Context()),
	})

	notes := append([]string{}, st.Warnings...)
	if !st.PF.ReadAvailable {
		notes = append(notes, "PF 运行状态不可读，这不等于 PF 已关闭")
	}
	writeDataWithMeta(w, r, st, &meta{Degraded: !st.PF.ReadAvailable, Notes: notes})
}

// handleAuditList 返回审计日志。
func (s *Server) handleAuditList(w http.ResponseWriter, r *http.Request) {
	if s.audit == nil {
		writeError(w, r, model.NewError(model.CodeInternal, "审计模块未初始化"))
		return
	}
	limit := 50
	if v := r.URL.Query().Get("limit"); v != "" {
		n, err := parseIntBounded(v, 1, 500)
		if err != nil {
			writeError(w, r, model.NewError(model.CodeInvalidArgument,
				"limit 必须是 1-500 之间的整数"))
			return
		}
		limit = n
	}
	action := r.URL.Query().Get("action")
	if len(action) > 100 {
		writeError(w, r, model.NewError(model.CodeInvalidArgument, "action 过长"))
		return
	}

	entries, err := s.audit.List(r.Context(), limit, action)
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeData(w, r, map[string]any{"items": entries, "limit": limit})
}

// auditRead 记录读接口失败。
func (s *Server) auditRead(r *http.Request, action string, callErr error) {
	if s.audit == nil {
		return
	}
	s.audit.BestEffort(r.Context(), audit.Entry{
		Actor:      ident(r),
		Action:     action,
		Result:     audit.ResultFailure,
		RemoteAddr: remoteHost(r),
		RequestID:  requestIDFrom(r.Context()),
		Detail:     map[string]any{"code": string(model.CodeOf(callErr))},
	})
}

func parseIntBounded(s string, min, max int) (int, error) {
	n := 0
	if s == "" {
		return 0, errInvalidInt
	}
	neg := false
	i := 0
	if s[0] == '-' {
		neg = true
		i = 1
	}
	for ; i < len(s); i++ {
		c := s[i]
		if c < '0' || c > '9' {
			return 0, errInvalidInt
		}
		n = n*10 + int(c-'0')
		if n > 1<<20 {
			return 0, errInvalidInt
		}
	}
	if neg {
		n = -n
	}
	if n < min || n > max {
		return 0, errInvalidInt
	}
	return n, nil
}

var errInvalidInt = model.NewError(model.CodeInvalidArgument, "整数参数非法")
