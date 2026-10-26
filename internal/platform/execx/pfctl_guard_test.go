package execx

import (
	"strings"
	"testing"
)

// TestPFctlGuardRejectsDangerousFlags 验证 pfctl 的危险选项被拒绝。
//
// 这是本项目最重要的一道安全边界：pfctl 在白名单内，
// 但 `-d` 会全局关闭防火墙、`-F` 会清空所有规则、`-f /etc/pf.conf` 会全量刷新主规则集。
// 一旦这些被允许，Agent 就从「只读面板」变成了「能把机器搞失联的工具」。
func TestPFctlGuardRejectsDangerousFlags(t *testing.T) {
	cases := []struct {
		name string
		args []string
	}{
		{"关闭 PF", []string{"-d"}},
		{"启用 PF", []string{"-e"}},
		{"清空规则", []string{"-F", "all"}},
		{"终止状态", []string{"-k", "0.0.0.0"}},
		{"调试级别", []string{"-x", "debug"}},
		{"清空计数", []string{"-z"}},
		{"合并规则", []string{"-m", "-f", "/tmp/x"}},
		{"关闭 PF 混在合法参数中", []string{"-a", "com.test", "-f", "/tmp/x", "-d"}},
		{"无参数", []string{}},
	}

	for _, c := range cases {
		if err := pfctlGuard(c.args); err == nil {
			t.Errorf("%s（%v）应被拒绝", c.name, c.args)
		}
	}
}

// TestPFctlGuardAllowsExpectedCalls 验证本应用真正需要的调用都能通过。
func TestPFctlGuardAllowsExpectedCalls(t *testing.T) {
	allowed := [][]string{
		{"-s", "info"},
		{"-s", "Anchors"},
		{"-n", "-f", "/etc/pf.anchors/com.homelab.manager.ipv6"},
		{"-a", "com.homelab.manager.ipv6", "-f", "/etc/pf.anchors/com.homelab.manager.ipv6"},
		{"-a", "com.homelab.manager.ipv6", "-f", "/dev/null"},
		{"-a", "com.homelab.manager.ipv6", "-sr"},
	}

	for _, args := range allowed {
		if err := pfctlGuard(args); err != nil {
			t.Errorf("%v 应被允许，实际: %v", args, err)
		}
	}
}

// TestPFctlGuardRejectsUnknownCombinations 验证不合规的参数组合被拒绝。
func TestPFctlGuardRejectsUnknownCombinations(t *testing.T) {
	bad := [][]string{
		{"-s", "rules"},        // 只允许 info / Anchors
		{"-s", "all"},          // 同上
		{"-n"},                 // -n 必须配 -f
		{"-a", "com.test"},     // -a 必须配 -f 或 -sr
		{"-f", "/etc/pf.conf"}, // 裸 -f 是「全量加载主配置」，禁止
		{"-ss"},                // 不认识的选项
	}

	for _, args := range bad {
		if err := pfctlGuard(args); err == nil {
			t.Errorf("%v 应被拒绝", args)
		}
	}
}

// TestRunEnforcesArgGuard 验证 Run 会实际调用参数校验。
func TestRunEnforcesArgGuard(t *testing.T) {
	// 即使二进制在白名单内，危险参数也必须在执行前被拦下。
	_, err := Run(t.Context(), 0, "/sbin/pfctl", "-d")
	if err == nil {
		t.Fatal("-d 应被拒绝")
	}
	if !strings.Contains(err.Error(), "禁止") && !strings.Contains(err.Error(), "拒绝") {
		t.Errorf("错误信息应说明被拒绝，实际: %v", err)
	}
}

// TestArgGuardScopeLimited 验证只有登记了 guard 的命令才做参数校验。
//
// sysctl 的参数不需要白名单（它本身只读），若给它加 guard 会误伤。
func TestArgGuardScopeLimited(t *testing.T) {
	if _, ok := argGuards["/usr/sbin/sysctl"]; ok {
		t.Error("sysctl 不应有参数 guard（它本身是只读命令）")
	}
	if _, ok := argGuards["/sbin/pfctl"]; !ok {
		t.Error("pfctl 必须有参数 guard")
	}
}
