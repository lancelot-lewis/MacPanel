package server

import (
	"encoding/json"
	"net/http"

	"github.com/lancelot-lewis/MacPanel/internal/audit"
	"github.com/lancelot-lewis/MacPanel/internal/model"
)

// 防火墙写操作的审计动作名。
// 用固定常量而非拼字符串，便于审计检索与统计。
const (
	auditRulesSave     = "firewall.rules.save"
	auditRulesApply    = "firewall.rules.apply"
	auditRulesConfirm  = "firewall.rules.confirm"
	auditRulesRollback = "firewall.rules.rollback"
	auditRulesPreview  = "firewall.rules.preview"
)

// handleRulesGet 读取规则集与事务状态。
func (s *Server) handleRulesGet(w http.ResponseWriter, r *http.Request) {
	raw, err := s.svc.FirewallRules(r.Context())
	if err != nil {
		s.auditRead(r, "firewall.rules.get", err)
		writeError(w, r, err)
		return
	}
	s.auditReadOK(r, "firewall.rules.get", "")
	writeRawData(w, r, raw)
}

// handleRulesPreview 预览规则生成结果。
//
// 预览是只读操作（不写文件、不调 pfctl），但会记录审计：
// 它能反推出用户的规则意图，属于有诊断价值的操作。
func (s *Server) handleRulesPreview(w http.ResponseWriter, r *http.Request) {
	var params any
	if r.ContentLength > 0 {
		var body map[string]any
		if err := decodeJSON(w, r, &body); err != nil {
			writeError(w, r, err)
			return
		}
		params = body
	}

	raw, err := s.svc.FirewallPreview(r.Context(), params)
	if err != nil {
		s.auditRead(r, auditRulesPreview, err)
		writeError(w, r, err)
		return
	}
	s.auditReadOK(r, auditRulesPreview, "")
	writeRawData(w, r, raw)
}

// handleRulesSave 保存规则集。
//
// 保存不触碰系统状态（只写数据库与生成预览），因此失败风险低。
// 但仍审计，因为它是「应用」的前置步骤，能反映用户意图。
func (s *Server) handleRulesSave(w http.ResponseWriter, r *http.Request) {
	var body map[string]any
	if err := decodeJSON(w, r, &body); err != nil {
		writeError(w, r, err)
		return
	}

	raw, err := s.svc.FirewallRulesSave(r.Context(), body)
	if err != nil {
		s.auditWrite(r, auditRulesSave, audit.ResultFailure, err.Error())
		writeError(w, r, err)
		return
	}

	s.auditWrite(r, auditRulesSave, audit.ResultSuccess, "规则集已保存")
	writeRawData(w, r, raw)
}

// handleRulesApply 应用规则——最高风险操作。
//
// 这是唯一会改变系统防火墙状态的操作。防护措施：
//  1. 必须显式带确认标记（避免误触）；
//  2. 完整审计（谁、何时、多少条规则）；
//  3. 后端已有语法检查 + 不变量校验 + 超时回滚。
func (s *Server) handleRulesApply(w http.ResponseWriter, r *http.Request) {
	var body map[string]any
	if r.ContentLength > 0 {
		if err := decodeJSON(w, r, &body); err != nil {
			writeError(w, r, err)
			return
		}
	}

	// 显式确认：要求请求体或头部带 confirmsRisk=true。
	// 这不是安全边界（攻击者也能带上），而是防误触的交互约束；
	// 真正的安全边界是认证与回滚机制。
	if !hasRiskConfirmation(r, body) {
		s.auditWrite(r, auditRulesApply, audit.ResultDenied, "缺少风险确认标记")
		writeError(w, r, model.NewError(model.CodeInvalidArgument,
			"应用规则会立即改变系统防火墙。请在请求中携带 confirmsRisk=true 以确认你已了解风险"))
		return
	}

	var params any
	if body != nil {
		params = body
	}

	raw, err := s.svc.FirewallApply(r.Context(), params)
	if err != nil {
		s.auditWrite(r, auditRulesApply, audit.ResultFailure, model.AsError(err).Message)
		writeError(w, r, err)
		return
	}

	s.auditWrite(r, auditRulesApply, audit.ResultSuccess, "规则已应用，等待确认")
	writeRawData(w, r, raw)
}

// handleRulesConfirm 确认当前配置。
func (s *Server) handleRulesConfirm(w http.ResponseWriter, r *http.Request) {
	raw, err := s.svc.FirewallConfirm(r.Context())
	if err != nil {
		s.auditWrite(r, auditRulesConfirm, audit.ResultFailure, model.AsError(err).Message)
		writeError(w, r, err)
		return
	}
	s.auditWrite(r, auditRulesConfirm, audit.ResultSuccess, "配置已确认")
	writeRawData(w, r, raw)
}

// handleRulesRollback 回滚配置。
func (s *Server) handleRulesRollback(w http.ResponseWriter, r *http.Request) {
	reason := ""
	if r.ContentLength > 0 {
		var body struct {
			Reason string `json:"reason"`
		}
		if err := decodeJSON(w, r, &body); err != nil {
			writeError(w, r, err)
			return
		}
		reason = body.Reason
	}

	raw, err := s.svc.FirewallRollback(r.Context(), reason)
	if err != nil {
		s.auditWrite(r, auditRulesRollback, audit.ResultFailure, model.AsError(err).Message)
		writeError(w, r, err)
		return
	}
	detail := "配置已回滚"
	if reason != "" {
		detail += "：" + reason
	}
	s.auditWrite(r, auditRulesRollback, audit.ResultSuccess, detail)
	writeRawData(w, r, raw)
}

// handleRulesTransaction 查询事务状态。
//
// 前端用它驱动倒计时横幅。这里的读操作会顺带触发超时检查
// （Agent 侧 PurgeExpired），因此即使后台定时器没跑，
// 用户也能看到正确的「已回滚」状态。
func (s *Server) handleRulesTransaction(w http.ResponseWriter, r *http.Request) {
	raw, err := s.svc.FirewallTransaction(r.Context())
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeRawData(w, r, raw)
}

// handleRulesTemplates 读取内置模板。
func (s *Server) handleRulesTemplates(w http.ResponseWriter, r *http.Request) {
	raw, err := s.svc.FirewallTemplates(r.Context())
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeRawData(w, r, raw)
}

// handleRulesRevisions 读取版本历史。
//
// 版本历史来自数据库（Server 侧），不需要经过 Agent。
// 当前实现返回空列表并说明——真正的历史查询需要 Server 持有数据库连接，
// 这属于后续增量（当前 Server 已有 db，但为保持职责清晰先留接口）。
func (s *Server) handleRulesRevisions(w http.ResponseWriter, r *http.Request) {
	writeRawData(w, r, json.RawMessage(`{"items":[],"note":"版本历史查询将在后续提供"}`))
}

// hasRiskConfirmation 判断请求是否带有风险确认标记。
//
// 两种方式任选其一：
//   - 请求体 confirmsRisk: true；
//   - 请求头 X-HLM-Confirm-Risk: 1。
//
// 用两种方式是因为 GET 之外的表单提交可能不方便改 body。
func hasRiskConfirmation(r *http.Request, body map[string]any) bool {
	if r.Header.Get("X-HLM-Confirm-Risk") == "1" {
		return true
	}
	if body == nil {
		return false
	}
	v, ok := body["confirmsRisk"]
	if !ok {
		return false
	}
	b, ok := v.(bool)
	return ok && b
}

// writeRawData 输出已经序列化好的 JSON（来自 Agent 的原始响应）。
//
// 为什么用原始 JSON 而非重新序列化：Agent 的返回结构可能随版本演进，
// Server 不做二次编解码可以避免「字段被静默丢弃」。
func writeRawData(w http.ResponseWriter, r *http.Request, raw json.RawMessage) {
	writeJSON(w, r, http.StatusOK, envelope{OK: true, Data: raw})
}

// auditWrite 记录写操作。
//
// 写操作的审计不允许失败静默通过——但也不应因为审计失败而回滚已完成的系统变更
// （那会造成更严重的不一致）。折中：审计失败只记日志，但这里用 BestEffort
// 明确表达「尽力而为」，并在日志中留痕。
func (s *Server) auditWrite(r *http.Request, action string, result audit.Result, detail string) {
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
	if detail != "" {
		entry.Detail = map[string]any{"detail": truncate(detail, 200)}
	}
	s.audit.BestEffort(r.Context(), entry)
}

// auditReadOK 记录成功的读操作。
func (s *Server) auditReadOK(r *http.Request, action, detail string) {
	if s.audit == nil {
		return
	}
	entry := audit.Entry{
		Actor:      ident(r),
		Action:     action,
		Result:     audit.ResultSuccess,
		RemoteAddr: remoteHost(r),
		RequestID:  requestIDFrom(r.Context()),
	}
	if detail != "" {
		entry.Detail = map[string]any{"detail": truncate(detail, 200)}
	}
	s.audit.BestEffort(r.Context(), entry)
}

func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max] + "…"
}
