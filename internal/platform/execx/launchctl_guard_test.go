package execx

import (
	"context"
	"strings"
	"testing"
	"time"
)

// TestLaunchctlGuardRejectsDangerousSubcommands 验证危险子命令被拒绝。
//
// launchctl 是本项目权限最高的工具：bootstrap 一个 plist 等于以 root
// 执行其中 ProgramArguments 指定的程序。这些子命令必须永久关闭。
func TestLaunchctlGuardRejectsDangerousSubcommands(t *testing.T) {
	cases := [][]string{
		{"submit", "-l", "x", "-p", "/bin/sh"},
		{"load", "/tmp/evil.plist"},
		{"unload", "/tmp/evil.plist"},
		{"reboot"},
		{"shutdown"},
		{"halt"},
		{"setenv", "PATH", "/tmp"},
		{"unsetenv", "PATH"},
		{"config", "system", "umask"},
		{"attach", "x"},
	}

	for _, args := range cases {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			if err := launchctlGuard(args); err == nil {
				t.Errorf("应拒绝 launchctl %v", args)
			}
		})
	}
}

// TestLaunchctlGuardRejectsNonSystemDomain 验证只允许 system 域。
//
// 用户域（gui/<uid>、user/<uid>）属于交互会话，
// 本应用以 root 运行，没有理由介入他人会话。
func TestLaunchctlGuardRejectsNonSystemDomain(t *testing.T) {
	cases := [][]string{
		{"print", "gui/501/com.example"},
		{"print", "user/501/com.example"},
		{"bootstrap", "gui/501", "/Library/LaunchAgents/x.plist"},
		{"bootstrap", "user/501", "/Library/LaunchAgents/x.plist"},
		{"bootout", "gui/501/com.example"},
		{"kickstart", "user/501/com.example"},
	}

	for _, args := range cases {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			if err := launchctlGuard(args); err == nil {
				t.Errorf("应拒绝非 system 域：%v", args)
			}
		})
	}
}

// TestLaunchctlGuardAllowsExpectedCalls 验证本应用真正需要的调用被放行。
func TestLaunchctlGuardAllowsExpectedCalls(t *testing.T) {
	cases := [][]string{
		{"list"},
		{"list", "com.homelab.manager.agent"},
		{"print", "system/com.homelab.manager.agent"},
		{"print", "system"},
		{"bootstrap", "system", "/Library/LaunchDaemons/com.homelab.manager.task.1.plist"},
		{"bootout", "system/com.homelab.manager.task.1"},
		{"kickstart", "system/com.homelab.manager.server"},
		{"enable", "system/com.homelab.manager.task.1"},
		{"disable", "system/com.homelab.manager.task.1"},
	}

	for _, args := range cases {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			if err := launchctlGuard(args); err != nil {
				t.Errorf("应允许 launchctl %v，实际错误: %v", args, err)
			}
		})
	}
}

// TestLaunchctlGuardRejectsMalformedBootstrap 验证 bootstrap 参数校验。
func TestLaunchctlGuardRejectsMalformedBootstrap(t *testing.T) {
	if err := launchctlGuard([]string{"bootstrap", "system"}); err == nil {
		t.Error("缺少 plist 路径应被拒绝")
	}
	if err := launchctlGuard([]string{"bootstrap", "system", "/tmp/evil.sh"}); err == nil {
		t.Error("目标不是 plist 应被拒绝")
	}
	if err := launchctlGuard(nil); err == nil {
		t.Error("空参数应被拒绝")
	}
}

// TestShutdownGuardOnlyAllowsReboot 验证 shutdown 只允许重启。
//
// 关键点：关机（-h）与断电（-p）必须永久关闭。若用户把机器放在
// 物理不可及的位置，误关机等于彻底失联。
func TestShutdownGuardOnlyAllowsReboot(t *testing.T) {
	forbidden := [][]string{
		{"-h", "now"},          // 关机
		{"-p", "now"},          // 断电
		{"-s", "now"},          // 休眠
		{"-r"},                 // 缺时间
		{"-r", "now", "extra"}, // 多余参数
		{"-r", "+0"},           // 延后 0 分钟无意义
		{"-r", "+61"},          // 超过 60 分钟上限
		{"-r", "-5"},           // 负值
		{"-r", "1200"},         // 缺 + 前缀
		{"now"},                // 缺 -r
	}

	for _, args := range forbidden {
		t.Run(strings.Join(args, "_"), func(t *testing.T) {
			if err := shutdownGuard(args); err == nil {
				t.Errorf("应拒绝 shutdown %v", args)
			}
		})
	}
}

// TestShutdownGuardAllowsExpectedCalls 验证允许的重启用例。
func TestShutdownGuardAllowsExpectedCalls(t *testing.T) {
	allowed := [][]string{
		{"-r", "now"},
		{"-r", "+1"},
		{"-r", "+30"},
		{"-r", "+60"},
	}
	for _, args := range allowed {
		t.Run(strings.Join(args, "_"), func(t *testing.T) {
			if err := shutdownGuard(args); err != nil {
				t.Errorf("应允许 shutdown %v，实际错误: %v", args, err)
			}
		})
	}
}

// TestNewBinariesAreRegistered 验证新增命令已进白名单。
func TestNewBinariesAreRegistered(t *testing.T) {
	for _, bin := range []string{"/bin/launchctl", "/sbin/shutdown"} {
		if !IsAllowed(bin) {
			t.Errorf("%s 应已登记进白名单", bin)
		}
		if _, ok := argGuards[bin]; !ok {
			t.Errorf("%s 必须有参数守卫（它属于危险命令）", bin)
		}
	}
}

// TestRunRejectsGuardViolation 验证 Run 确实会执行参数守卫。
//
// 只测守卫函数本身不够：若 Run 忘记调用它，守卫形同虚设。
func TestRunRejectsGuardViolation(t *testing.T) {
	ctx := context.Background()
	if _, err := Run(ctx, time.Second, "/sbin/shutdown", "-h", "now"); err == nil {
		t.Error("Run 应拒绝 shutdown -h")
	}
	if _, err := Run(ctx, time.Second, "/bin/launchctl", "submit", "-l", "x", "-p", "/bin/sh"); err == nil {
		t.Error("Run 应拒绝 launchctl submit")
	}
	// 白名单外的路径仍被拒绝
	if _, err := Run(ctx, time.Second, "/bin/rm", "-rf", "/"); err == nil {
		t.Error("Run 应拒绝白名单外命令")
	}
}
