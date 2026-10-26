package agent

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/lancelot-lewis/MacPanel/internal/model"
)

// TestFirewallMethodsReturnNotImplementedWithoutDeps 验证依赖未就绪时返回「未实现」。
//
// 这是「不伪造能力」的体现：非 macOS 或未配置时，
// 写方法必须明确说「未实现」，而不是返回内部错误或假装成功。
func TestFirewallMethodsReturnNotImplementedWithoutDeps(t *testing.T) {
	a := newTestAgent(t) // 没有 SetFirewallDeps
	ctx := context.Background()

	cases := []struct {
		name   string
		handle func(context.Context, json.RawMessage) (any, error)
	}{
		{"rules.get", a.handleRulesGet},
		{"rules.set", a.handleRulesSet},
		{"preview", a.handlePreview},
		{"apply", a.handleApply},
		{"confirm", a.handleConfirm},
		{"rollback", a.handleRollback},
		{"transaction", a.handleTransaction},
	}

	for _, c := range cases {
		_, err := c.handle(ctx, nil)
		if err == nil {
			t.Errorf("%s 在依赖未就绪时应返回错误", c.name)
			continue
		}
		if code := model.CodeOf(err); code != model.CodeNotImplemented {
			t.Errorf("%s 错误码应为 not_implemented，实际 %s", c.name, code)
		}
	}
}

// TestTemplatesAvailableWithoutDeps 验证模板不依赖依赖就绪。
//
// 用户即使无法应用规则，也应该能看到模板内容。
func TestTemplatesAvailableWithoutDeps(t *testing.T) {
	a := newTestAgent(t)
	raw, err := a.handleTemplates(context.Background(), nil)
	if err != nil {
		t.Fatalf("模板应可用，实际: %v", err)
	}
	m, ok := raw.(map[string]any)
	if !ok {
		t.Fatalf("返回类型不符: %T", raw)
	}
	items, ok := m["items"]
	if !ok {
		t.Fatal("应返回 items")
	}
	if items == nil {
		t.Error("模板列表不应为空")
	}
}

// TestAllMethodsIncludesFirewall 验证 AllMethods 是完整清单。
func TestAllMethodsIncludesFirewall(t *testing.T) {
	all := AllMethods()

	// 不能有重复：重复会导致 RegisterAll 里 ipc 的 panic
	seen := map[string]bool{}
	for _, m := range all {
		if seen[m] {
			t.Errorf("方法名重复: %s", m)
		}
		seen[m] = true
	}

	// M1 的 4 个
	for _, m := range []string{MethodHealth, MethodSystemOverview, MethodSystemNetwork, MethodFirewallStatus} {
		if !seen[m] {
			t.Errorf("缺少 M1 方法: %s", m)
		}
	}
	// M2 的 8 个
	for _, m := range []string{
		MethodFirewallRulesGet, MethodFirewallRulesSet, MethodFirewallPreview,
		MethodFirewallApply, MethodFirewallConfirm, MethodFirewallRollback,
		MethodFirewallTransaction, MethodFirewallTemplates,
	} {
		if !seen[m] {
			t.Errorf("缺少 M2 方法: %s", m)
		}
	}

	if len(all) != 12 {
		t.Errorf("方法总数应为 12，实际 %d", len(all))
	}
}

// TestNoDangerousMethodsInAllMethods 验证方法清单里没有通用执行入口。
//
// 这条测试是「Agent 不提供任意命令执行」的最后一道防线：
// 任何人新增方法时，若起了个危险名字，这里会立刻失败。
func TestNoDangerousMethodsInAllMethods(t *testing.T) {
	dangerous := []string{
		"exec", "shell", "sh", "bash", "run", "spawn", "system",
		"file.read", "file.write", "file.delete", "file.remove",
		"readfile", "writefile", "os.exec",
		"pf.disable", "pf.enable", "pf.flush", "pf.kill",
		"launchctl.load", "launchctl.unload", "service.restart",
		"reboot", "shutdown", "poweroff",
	}
	for _, m := range AllMethods() {
		lower := strings.ToLower(m)
		for _, d := range dangerous {
			if lower == d || strings.HasSuffix(lower, "."+d) {
				t.Errorf("危险方法名不应存在: %s", m)
			}
		}
	}
}

// TestJoinLimited 验证错误信息拼接的截断。
func TestJoinLimited(t *testing.T) {
	msgs := []string{"a", "b", "c", "d", "e", "f", "g"}
	got := joinLimited(msgs, 3)
	if !strings.Contains(got, "等 7 项") {
		t.Errorf("超限时应提示总数，实际: %s", got)
	}
	if strings.Contains(got, "g") {
		t.Errorf("不应包含第 7 项，实际: %s", got)
	}

	short := joinLimited([]string{"x", "y"}, 5)
	if short != "x；y" {
		t.Errorf("未超限时应全部拼接，实际: %s", short)
	}
}
