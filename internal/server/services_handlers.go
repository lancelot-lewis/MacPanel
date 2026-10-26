package server

import (
	"net/http"

	"github.com/lancelot-lewis/MacPanel/internal/audit"
	"github.com/lancelot-lewis/MacPanel/internal/model"
)

// M4 审计动作名。
const (
	auditServiceRegister   = "service.register"
	auditServiceUnregister = "service.unregister"
	auditServiceControl    = "service.control"
)

// handleServicesList 列出服务。
func (s *Server) handleServicesList(w http.ResponseWriter, r *http.Request) {
	params := map[string]any{}

	// includeGui：是否包含当前图形会话域。
	if v := r.URL.Query().Get("includeGui"); v == "1" || v == "true" {
		params["includeGui"] = true
	}
	if v := r.URL.Query().Get("guiUid"); v != "" {
		uid, err := parseNonNegativeInt(v)
		if err != nil {
			writeError(w, r, model.NewError(model.CodeInvalidArgument, "guiUid 必须是非负整数"))
			return
		}
		params["guiUid"] = uid
	}

	raw, err := s.svc.ServicesList(r.Context(), params)
	if err != nil {
		s.auditRead(r, "services.list", err)
		writeError(w, r, err)
		return
	}
	writeRawData(w, r, raw)
}

// handleServiceDetail 读取服务详情（配置来源与日志入口）。
func (s *Server) handleServiceDetail(w http.ResponseWriter, r *http.Request) {
	label := r.URL.Query().Get("label")
	if label == "" {
		writeError(w, r, model.NewError(model.CodeInvalidArgument, "缺少 label 参数"))
		return
	}

	params := map[string]any{"label": label}
	if d := r.URL.Query().Get("domain"); d != "" {
		params["domain"] = d
	}

	raw, err := s.svc.ServiceDetail(r.Context(), params)
	if err != nil {
		s.auditRead(r, "services.detail", err)
		writeError(w, r, err)
		return
	}
	writeRawData(w, r, raw)
}

// handleServiceRegister 登记受管服务。
//
// 登记本身不改变系统状态（只写数据库），因此不需要风险确认。
// 但它是「允许操作」的前置，所以仍写审计——审计里能看到
// 「谁在什么时候授权了哪个服务」。
func (s *Server) handleServiceRegister(w http.ResponseWriter, r *http.Request) {
	var body map[string]any
	if err := decodeJSON(w, r, &body); err != nil {
		writeError(w, r, err)
		return
	}

	raw, err := s.svc.ServiceRegister(r.Context(), body)
	if err != nil {
		s.auditWrite(r, auditServiceRegister, audit.ResultFailure, err.Error())
		writeError(w, r, err)
		return
	}
	s.auditWrite(r, auditServiceRegister, audit.ResultSuccess, stringField(body, "label"))
	writeRawData(w, r, raw)
}

// handleServiceUnregister 取消登记。
func (s *Server) handleServiceUnregister(w http.ResponseWriter, r *http.Request) {
	label := r.URL.Query().Get("label")
	if label == "" {
		writeError(w, r, model.NewError(model.CodeInvalidArgument, "缺少 label 参数"))
		return
	}

	raw, err := s.svc.ServiceUnregister(r.Context(), map[string]any{"label": label})
	if err != nil {
		s.auditWrite(r, auditServiceUnregister, audit.ResultFailure, err.Error())
		writeError(w, r, err)
		return
	}
	s.auditWrite(r, auditServiceUnregister, audit.ResultSuccess, label)
	writeRawData(w, r, raw)
}

// handleServiceControl 启停受管服务。
//
// 需要风险确认：停止一个 daemon 的影响可能超出预期
// （它可能是用户唯一的管理通道，例如 sshd、远程桌面、VPN）。
// 这里不做「哪些服务危险」的智能判断——那需要理解服务语义，
// 而误判的代价是失联。统一要求确认更可靠。
func (s *Server) handleServiceControl(w http.ResponseWriter, r *http.Request) {
	var body map[string]any
	if err := decodeJSON(w, r, &body); err != nil {
		writeError(w, r, err)
		return
	}

	label := stringField(body, "label")
	action := stringField(body, "action")
	if label == "" || action == "" {
		writeError(w, r, model.NewError(model.CodeInvalidArgument, "缺少 label 或 action"))
		return
	}

	if !hasRiskConfirmation(r, body) {
		s.auditWrite(r, auditServiceControl, audit.ResultDenied, "缺少风险确认标记: "+label)
		writeError(w, r, model.NewError(model.CodeInvalidArgument,
			"操作服务可能中断正在运行的功能，甚至导致失联。请在请求中携带 confirmsRisk=true"))
		return
	}

	// 服务端也带一份确认标记：Agent 侧会再检查一次，
	// 作为「Server 实现改动后漏校验」的兜底。
	body["confirmsRisk"] = true

	raw, err := s.svc.ServiceControl(r.Context(), body)
	if err != nil {
		s.auditWrite(r, auditServiceControl, audit.ResultFailure,
			label+" "+action+" "+err.Error())
		writeError(w, r, err)
		return
	}
	s.auditWrite(r, auditServiceControl, audit.ResultSuccess, label+" "+action)
	writeRawData(w, r, raw)
}

// stringField 取字符串字段，缺失时返回空串。
func stringField(body map[string]any, key string) string {
	if v, ok := body[key].(string); ok {
		return v
	}
	return ""
}

// parseNonNegativeInt 解析非负整数。
func parseNonNegativeInt(s string) (int, error) {
	n := 0
	for _, r := range s {
		if r < '0' || r > '9' {
			return 0, model.NewError(model.CodeInvalidArgument, "不是非负整数: %q", s)
		}
		n = n*10 + int(r-'0')
		if n > 1<<30 {
			return 0, model.NewError(model.CodeInvalidArgument, "数值过大: %q", s)
		}
	}
	if s == "" {
		return 0, model.NewError(model.CodeInvalidArgument, "空值")
	}
	return n, nil
}
