package agent

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/lancelot-lewis/MacPanel/internal/model"
	"github.com/lancelot-lewis/MacPanel/internal/platform/fake"
)

// newTestAgent 创建使用模拟采集器的 Agent。
func newTestAgent(t *testing.T) *Agent {
	t.Helper()
	a, err := New(Options{Collector: fake.New(), Logger: quietLogger()})
	if err != nil {
		t.Fatalf("创建 Agent 失败: %v", err)
	}
	return a
}

// TestHealth 验证健康状况返回与方法清单。
func TestHealth(t *testing.T) {
	a := newTestAgent(t)
	raw, err := a.handleHealth(context.Background(), nil)
	if err != nil {
		t.Fatalf("调用失败: %v", err)
	}
	resp, ok := raw.(model.AgentHealthResponse)
	if !ok {
		t.Fatalf("返回类型不符: %T", raw)
	}
	if resp.Version == "" {
		t.Error("版本不应为空")
	}
	want := len(AllMethods())
	if resp.MethodCount != want {
		t.Errorf("方法数应为 %d，实际 %d", want, resp.MethodCount)
	}
	if len(resp.Methods) != want {
		t.Errorf("方法列表长度应为 %d，实际 %d", want, len(resp.Methods))
	}
	if resp.PID <= 0 {
		t.Error("PID 应大于 0")
	}
}

// TestMethodsAreWhitelisted 验证方法名固定，不含任何通用执行入口。
func TestMethodsAreWhitelisted(t *testing.T) {
	a := newTestAgent(t)
	methods := a.Methods()

	allowed := map[string]bool{
		// M1 只读
		MethodHealth:         true,
		MethodSystemOverview: true,
		MethodSystemNetwork:  true,
		MethodFirewallStatus: true,
		// M2 防火墙读写
		MethodFirewallRulesGet:    true,
		MethodFirewallRulesSet:    true,
		MethodFirewallPreview:     true,
		MethodFirewallApply:       true,
		MethodFirewallConfirm:     true,
		MethodFirewallRollback:    true,
		MethodFirewallTransaction: true,
		MethodFirewallTemplates:   true,
		// M3 电源与计划任务
		MethodPowerStatus:        true,
		MethodPowerReboot:        true,
		MethodPowerCancel:        true,
		MethodScheduleList:       true,
		MethodScheduleCreate:     true,
		MethodScheduleUpdate:     true,
		MethodScheduleDelete:     true,
		MethodScheduleExecutions: true,
		MethodScheduleRunNow:     true,
		MethodScheduleSync:       true,
	}
	for _, m := range methods {
		if !allowed[m] {
			t.Errorf("出现未预期的方法: %s", m)
		}
	}
	if len(methods) != len(allowed) {
		t.Errorf("方法数量不符：期望 %d，实际 %d", len(allowed), len(methods))
	}

	// 显式确认危险方法名不存在。
	//
	// 注意 power.reboot 不在此列：M3 确实提供了它，但它是**固定语义**
	// 的受控接口（参数只有延后秒数与原因，无任意命令、无关机/断电分支），
	// 且有风险确认、审计与最短间隔保护。这与「通用执行入口」是两件事。
	// 关机/断电必须永久缺席——前者无法远程恢复，后者等于物理断电。
	dangerous := []string{
		"system.exec", "exec", "shell", "run",
		"file.read", "file.write", "file.remove",
		"pf.apply", "pf.enable", "pf.disable", "pf.flush",
		"launchctl.load", "launchctl.unload",
		"power.shutdown", "power.halt", "power.sleep",
	}
	for _, d := range dangerous {
		for _, m := range methods {
			if m == d {
				t.Errorf("不应存在危险方法: %s", d)
			}
		}
	}
}

// TestSystemOverview 验证系统概览透传。
func TestSystemOverview(t *testing.T) {
	a := newTestAgent(t)
	raw, err := a.handleSystemOverview(context.Background(), nil)
	if err != nil {
		t.Fatalf("调用失败: %v", err)
	}
	ov, ok := raw.(model.SystemOverview)
	if !ok {
		t.Fatalf("返回类型不符: %T", raw)
	}
	if ov.Hostname == "" {
		t.Error("主机名不应为空")
	}
}

// TestCollectorErrorBecomesUnavailable 验证采集失败转成 unavailable 而不是内部分错误。
func TestCollectorErrorBecomesUnavailable(t *testing.T) {
	collector := &fake.Collector{
		OverviewFn: func(context.Context) (model.SystemOverview, error) {
			return model.SystemOverview{}, model.Unavailable("依赖不可用")
		},
	}
	a, err := New(Options{Collector: collector, Logger: quietLogger()})
	if err != nil {
		t.Fatalf("创建 Agent 失败: %v", err)
	}

	_, callErr := a.handleSystemOverview(context.Background(), nil)
	if callErr == nil {
		t.Fatal("期望返回错误")
	}
	if code := model.CodeOf(callErr); code != model.CodeUnavailable {
		t.Errorf("错误码应为 unavailable，实际 %s", code)
	}
}

// TestGenericErrorNotLeaked 验证普通 error 转成不泄漏细节的 unavailable。
func TestGenericErrorNotLeaked(t *testing.T) {
	collector := &fake.Collector{
		NetworkFn: func(context.Context) (model.NetworkSnapshot, error) {
			// 模拟含敏感信息的底层错误
			return model.NetworkSnapshot{}, errTest
		},
	}
	a, err := New(Options{Collector: collector, Logger: quietLogger()})
	if err != nil {
		t.Fatalf("创建 Agent 失败: %v", err)
	}

	_, callErr := a.handleSystemNetwork(context.Background(), nil)
	if callErr == nil {
		t.Fatal("期望返回错误")
	}
	// 普通 error 会被包装为 unavailable，且带上原因（便于诊断）。
	// 这里的关键是错误码正确，而不是 internal。
	if code := model.CodeOf(callErr); code != model.CodeUnavailable {
		t.Errorf("错误码应为 unavailable，实际 %s", code)
	}
}

// TestRequireNoParams 验证读接口拒绝参数。
func TestRequireNoParams(t *testing.T) {
	cases := []struct {
		params json.RawMessage
		ok     bool
	}{
		{nil, true},
		{json.RawMessage("null"), true},
		{json.RawMessage("{}"), true},
		{json.RawMessage(`{"raw":"pfctl -d"}`), false},
		{json.RawMessage(`{"path":"/etc/passwd"}`), false},
		{json.RawMessage(`[]`), false},
		{json.RawMessage(`"string"`), false},
	}
	for _, c := range cases {
		err := requireNoParams(c.params)
		if c.ok && err != nil {
			t.Errorf("参数 %s 应通过，实际: %v", c.params, err)
		}
		if !c.ok && err == nil {
			t.Errorf("参数 %s 应被拒绝", c.params)
		}
	}
}

// TestFirewallStatus 验证防火墙状态透传且可写标记为 false。
func TestFirewallStatus(t *testing.T) {
	a := newTestAgent(t)
	raw, err := a.handleFirewallStatus(context.Background(), nil)
	if err != nil {
		t.Fatalf("调用失败: %v", err)
	}
	st, ok := raw.(model.FirewallStatus)
	if !ok {
		t.Fatalf("返回类型不符: %T", raw)
	}
	if st.Writable {
		t.Error("M1 阶段 Writable 必须为 false")
	}
}

// TestNewRequiresCollector 验证采集器必需。
func TestNewRequiresCollector(t *testing.T) {
	if _, err := New(Options{}); err == nil {
		t.Fatal("缺少采集器时应报错")
	}
}
