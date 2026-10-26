package main

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/lancelot-lewis/MacPanel/internal/config"
	"github.com/lancelot-lewis/MacPanel/internal/model"
	"github.com/lancelot-lewis/MacPanel/internal/platform/darwin/firewalld"
)

// TestFirewallStatePathFollowsSocketDir 确认状态文件跟随 Socket 目录。
//
// 这条断言是防止「CLI 与 Agent 各算一次」的回归：两者一旦漂移，
// 会出现「面板显示待确认、命令行显示空闲」这种最难排查的不一致。
func TestFirewallStatePathFollowsSocketDir(t *testing.T) {
	cases := []struct {
		socket string
		want   string
	}{
		{"/var/run/homelab-manager/agent.sock", "/var/run/homelab-manager/firewall-state.json"},
		{"/tmp/x/agent.sock", "/tmp/x/firewall-state.json"},
	}
	for _, c := range cases {
		cfg := config.Config{SocketPath: c.socket}
		if got := FirewallStatePath(cfg); got != c.want {
			t.Errorf("SocketPath=%s 得到 %s，期望 %s", c.socket, got, c.want)
		}
	}
}

// TestFirewallStatePathMatchesAgentWiring 交叉校验 CLI 与 Agent 用的是同一份路径。
//
// wireFirewall 已经改用 FirewallStatePath，这里再断一次：
// 若将来有人把它内联回去，这个测试会失败而不是悄悄漂移。
func TestFirewallStatePathMatchesAgentWiring(t *testing.T) {
	cfg := config.Defaults()
	want := filepath.Join(filepath.Dir(cfg.SocketPath), "firewall-state.json")
	if got := FirewallStatePath(cfg); got != want {
		t.Errorf("路径不一致: CLI=%s Agent=%s", got, want)
	}
}

// TestPhaseTextCoversAllPhases 确认每个阶段都有中文释义。
// 新增阶段时忘了补这里，会出现输出直接打印英文枚举的糟糕体验。
func TestPhaseTextCoversAllPhases(t *testing.T) {
	for _, p := range []firewalld.Phase{
		firewalld.PhaseIdle,
		firewalld.PhasePending,
		firewalld.PhaseConfirmed,
		firewalld.PhaseRolledBack,
		firewalld.PhaseFailed,
	} {
		if got := phaseText(p); got == string(p) {
			t.Errorf("阶段 %s 缺少中文释义", p)
		}
	}
}

// TestRenderLogArgs 覆盖 slog 风格参数的渲染。
//
// 这条测试的由来：最初用 fmt.Sprintf(msg, args...) 展开 key/value，
// 运行时输出 "%!(EXTRA string=reason, ...)"。
// 参数个数奇偶都要覆盖——Logger 接口不保证调用方成对传参。
func TestRenderLogArgs(t *testing.T) {
	cases := []struct {
		name string
		msg  string
		args []any
		want string
	}{
		{"无参数", "已回滚", nil, "已回滚"},
		{"成对", "已回滚", []any{"reason", "超时"}, "已回滚 reason=超时"},
		{"多对", "已回滚", []any{"revision", 3, "reason", "超时"}, "已回滚 revision=3 reason=超时"},
		{"奇数个", "已回滚", []any{"orphan"}, "已回滚 orphan"},
	}
	for _, c := range cases {
		if got := renderLogArgs(c.msg, c.args); got != c.want {
			t.Errorf("%s: 得到 %q，期望 %q", c.name, got, c.want)
		}
	}
}

// TestLoadedMark 确认 anchor 列表里能否识别出本应用 anchor。
func TestLoadedMark(t *testing.T) {
	if got := loadedMark([]string{"com.other", "com.homelab.manager.ipv6"}, "com.homelab.manager.ipv6"); !strings.Contains(got, "含本应用") {
		t.Errorf("包含时应标记，实际 %q", got)
	}
	if got := loadedMark([]string{"com.other"}, "com.homelab.manager.ipv6"); !strings.Contains(got, "不含") {
		t.Errorf("不包含时应说明，实际 %q", got)
	}
}

// TestStatusHintsGuideWhenPending 确认待确认状态下会给出可执行的下一步。
//
// 救援命令的输出必须包含「你现在该执行什么」，而不是只罗列字段。
func TestStatusHintsGuideWhenPending(t *testing.T) {
	env := firewallEnv{dryRun: true}
	hints := statusHints(env, model.FirewallStatus{}, nil, firewalld.State{Phase: firewalld.PhasePending})

	joined := strings.Join(hints, "\n")
	if !strings.Contains(joined, "dry-run") {
		t.Error("dry-run 下必须明确告知不会改动系统")
	}
	if !strings.Contains(joined, "firewall rollback") {
		t.Error("pending 状态必须提示 rollback 命令")
	}
}
