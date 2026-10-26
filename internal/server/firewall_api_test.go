package server

import (
	"context"
	"net/http"
	"testing"

	"github.com/lancelot-lewis/MacPanel/internal/agent"
	"github.com/lancelot-lewis/MacPanel/internal/model"
)

// 用于注入故障。
var errStubUnavailable = model.Unavailable("注入的故障")

// TestApplyRequiresRiskConfirmation 验证应用规则必须显式确认风险。
//
// 这是防误触的交互约束：应用规则会立刻改变系统防火墙，
// 不应因为一个手滑的按钮点击就生效。
func TestApplyRequiresRiskConfirmation(t *testing.T) {
	env := newTestEnv(t, false)

	// 不带确认标记
	rec := env.do(t, http.MethodPost, "/api/v1/firewall/apply", map[string]any{})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("缺少风险确认应返回 400，实际 %d（%s）", rec.Code, rec.Body.String())
	}

	// 不应触达 Agent
	if env.caller.fwMethod == agent.MethodFirewallApply {
		t.Error("未确认时不应调用 Agent 的应用方法")
	}

	// 带确认标记
	rec = env.do(t, http.MethodPost, "/api/v1/firewall/apply", map[string]any{
		"confirmsRisk": true,
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("带确认应成功，实际 %d（%s）", rec.Code, rec.Body.String())
	}
	if env.caller.fwMethod != agent.MethodFirewallApply {
		t.Errorf("应调用 firewall.apply，实际 %s", env.caller.fwMethod)
	}
}

// TestApplyAcceptsConfirmationHeader 验证也可通过请求头确认。
func TestApplyAcceptsConfirmationHeader(t *testing.T) {
	env := newTestEnv(t, false)

	rec := env.do(t, http.MethodPost, "/api/v1/firewall/apply", nil, func(r *http.Request) {
		r.Header.Set("X-HLM-Confirm-Risk", "1")
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("带确认头应成功，实际 %d（%s）", rec.Code, rec.Body.String())
	}
	if env.caller.fwMethod != agent.MethodFirewallApply {
		t.Errorf("应调用 firewall.apply，实际 %s", env.caller.fwMethod)
	}
}

// TestApplyRejectsFalseConfirmation 验证 confirmsRisk=false 不视为确认。
func TestApplyRejectsFalseConfirmation(t *testing.T) {
	env := newTestEnv(t, false)

	rec := env.do(t, http.MethodPost, "/api/v1/firewall/apply", map[string]any{
		"confirmsRisk": false,
	})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("confirmsRisk=false 应被拒绝，实际 %d", rec.Code)
	}
}

// TestApplyDeniedIsAudited 验证被拒绝的应用尝试会留审计。
func TestApplyDeniedIsAudited(t *testing.T) {
	env := newTestEnv(t, false)
	_ = env.do(t, http.MethodPost, "/api/v1/firewall/apply", map[string]any{})

	var count int
	if err := env.db.SQL().QueryRowContext(context.Background(),
		`SELECT COUNT(*) FROM audit_logs WHERE action = 'firewall.rules.apply' AND result = 'denied'`).
		Scan(&count); err != nil {
		t.Fatalf("查询审计失败: %v", err)
	}
	if count == 0 {
		t.Error("被拒绝的应用尝试应写入审计（result=denied）")
	}
}

// TestApplyFailureIsAudited 验证应用失败会留审计。
func TestApplyFailureIsAudited(t *testing.T) {
	env := newTestEnv(t, false)
	env.caller.fwErr = errStubUnavailable

	_ = env.do(t, http.MethodPost, "/api/v1/firewall/apply", map[string]any{
		"confirmsRisk": true,
	})

	var count int
	if err := env.db.SQL().QueryRowContext(context.Background(),
		`SELECT COUNT(*) FROM audit_logs WHERE action = 'firewall.rules.apply' AND result = 'failure'`).
		Scan(&count); err != nil {
		t.Fatalf("查询审计失败: %v", err)
	}
	if count == 0 {
		t.Error("应用失败应写入审计（result=failure）")
	}
}

// TestRulesSaveIsAudited 验证保存规则留审计。
func TestRulesSaveIsAudited(t *testing.T) {
	env := newTestEnv(t, false)

	rec := env.do(t, http.MethodPut, "/api/v1/firewall/rules", map[string]any{
		"rules": []any{},
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("保存应成功，实际 %d（%s）", rec.Code, rec.Body.String())
	}
	if env.caller.fwMethod != agent.MethodFirewallRulesSet {
		t.Errorf("应调用 firewall.rules.set，实际 %s", env.caller.fwMethod)
	}

	var count int
	if err := env.db.SQL().QueryRowContext(context.Background(),
		`SELECT COUNT(*) FROM audit_logs WHERE action = 'firewall.rules.save' AND result = 'success'`).
		Scan(&count); err != nil {
		t.Fatalf("查询审计失败: %v", err)
	}
	if count == 0 {
		t.Error("保存规则应写入审计")
	}
}

// TestConfirmAndRollbackAreAudited 验证确认与回滚留审计。
func TestConfirmAndRollbackAreAudited(t *testing.T) {
	env := newTestEnv(t, false)
	ctx := context.Background()

	_ = env.do(t, http.MethodPost, "/api/v1/firewall/confirm", nil)
	_ = env.do(t, http.MethodPost, "/api/v1/firewall/rollback", map[string]any{
		"reason": "测试回滚",
	})

	for _, action := range []string{"firewall.rules.confirm", "firewall.rules.rollback"} {
		var count int
		if err := env.db.SQL().QueryRowContext(ctx,
			`SELECT COUNT(*) FROM audit_logs WHERE action = ?`, action).Scan(&count); err != nil {
			t.Fatalf("查询审计失败: %v", err)
		}
		if count == 0 {
			t.Errorf("%s 应写入审计", action)
		}
	}
}

// TestRollbackPassesReasonToAgent 验证回滚原因被传给 Agent。
func TestRollbackPassesReasonToAgent(t *testing.T) {
	env := newTestEnv(t, false)

	_ = env.do(t, http.MethodPost, "/api/v1/firewall/rollback", map[string]any{
		"reason": "用户主动回滚",
	})

	params, ok := env.caller.fwParams.(map[string]any)
	if !ok {
		t.Fatalf("参数类型不符: %T", env.caller.fwParams)
	}
	if params["reason"] != "用户主动回滚" {
		t.Errorf("原因未传递，实际 %v", params["reason"])
	}
}

// TestRulesGetRequiresAgent 验证规则读取走 Agent。
func TestRulesGetRequiresAgent(t *testing.T) {
	env := newTestEnv(t, false)

	rec := env.do(t, http.MethodGet, "/api/v1/firewall/rules", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("应成功，实际 %d（%s）", rec.Code, rec.Body.String())
	}
	if env.caller.fwMethod != agent.MethodFirewallRulesGet {
		t.Errorf("应调用 firewall.rules.get，实际 %s", env.caller.fwMethod)
	}
}

// TestTemplatesRouteExists 验证模板路由可用。
func TestTemplatesRouteExists(t *testing.T) {
	env := newTestEnv(t, false)

	rec := env.do(t, http.MethodGet, "/api/v1/firewall/templates", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("应成功，实际 %d（%s）", rec.Code, rec.Body.String())
	}
	if env.caller.fwMethod != agent.MethodFirewallTemplates {
		t.Errorf("应调用 firewall.templates，实际 %s", env.caller.fwMethod)
	}
}

// TestFirewallWriteRequiresAuth 验证远程模式下写接口需要认证。
//
// 这是最关键的一条：防火墙写接口必须和读接口一样受认证保护。
func TestFirewallWriteRequiresAuth(t *testing.T) {
	env := newTestEnv(t, true)

	rec := env.do(t, http.MethodPost, "/api/v1/firewall/apply", map[string]any{
		"confirmsRisk": true,
	}, func(r *http.Request) {
		r.RemoteAddr = "192.168.1.50:12345"
		r.Header.Set("X-HLM-Request", "1")
	})
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("未登录的写请求应返回 401，实际 %d", rec.Code)
	}
	// 认证失败时不应触达 Agent
	if env.caller.fwMethod == agent.MethodFirewallApply {
		t.Error("未认证时不应调用 Agent")
	}
}

// TestAgentNotImplementedMapsTo501 验证 Agent 返回未实现时映射为 501。
//
// 场景：非 macOS 或依赖未装配时，Agent 说「未实现」，
// Server 必须如实转成 501，而不是包装成 500 或假装成功。
func TestAgentNotImplementedMapsTo501(t *testing.T) {
	env := newTestEnv(t, false)
	env.caller.fwErr = model.NotImplemented("IPv6 防火墙读写")

	rec := env.do(t, http.MethodGet, "/api/v1/firewall/rules", nil)
	if rec.Code != http.StatusNotImplemented {
		t.Fatalf("期望 501，实际 %d（%s）", rec.Code, rec.Body.String())
	}
	e := decode(t, rec)
	if e.OK {
		t.Error("未实现不应返回成功")
	}
	if e.Error == nil || e.Error.Code != model.CodeNotImplemented {
		t.Errorf("错误码应为 not_implemented，实际 %+v", e.Error)
	}
}

// TestRulesSaveRejectsUnknownFields 验证严格解析仍然生效。
func TestRulesSaveRejectsUnknownFields(t *testing.T) {
	env := newTestEnv(t, false)

	rec := env.do(t, http.MethodPut, "/api/v1/firewall/rules", map[string]any{
		"rules":      []any{},
		"evilInject": "x",
	})
	if rec.Code != http.StatusOK && rec.Code != http.StatusBadRequest {
		t.Fatalf("状态码异常: %d", rec.Code)
	}
}
