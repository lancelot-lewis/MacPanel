package darwin

import (
	"context"
	"strings"
	"testing"
	"time"
)

// newTestPower 构造可注入依赖的电源控制器，返回记录调用的闭包。
func newTestPower(euid int) (*Power, *[]int) {
	calls := []int{}
	p := NewPowerForTest(
		func() int { return euid },
		func(ctx context.Context, delay int) error {
			calls = append(calls, delay)
			return nil
		},
	)
	return p, &calls
}

// TestPowerCapabilityRequiresRoot 验证非 root 时不能重启。
//
// 这不是「权限不足报错」这么简单：界面需要知道「为什么不能」，
// 才能显示「已排定但不会生效」而不是让用户以为设置成功。
func TestPowerCapabilityRequiresRoot(t *testing.T) {
	p, _ := newTestPower(501)
	ok, reason := p.Capability()
	if ok {
		t.Error("非 root 不应具备重启能力")
	}
	if !strings.Contains(reason, "root") {
		t.Errorf("原因应说明需要 root，实际 %q", reason)
	}

	root, _ := newTestPower(0)
	if ok, reason := root.Capability(); !ok {
		t.Errorf("root 应具备重启能力，实际原因 %q", reason)
	}
}

// TestPowerRebootRejectedWhenNotRoot 验证非 root 时拒绝执行。
func TestPowerRebootRejectedWhenNotRoot(t *testing.T) {
	p, calls := newTestPower(501)
	if err := p.Reboot(context.Background(), 0); err == nil {
		t.Error("非 root 应拒绝重启")
	}
	if len(*calls) != 0 {
		t.Error("拒绝时不应调用系统命令")
	}
}

// TestPowerRebootImmediate 验证立即重启（delay=0）。
func TestPowerRebootImmediate(t *testing.T) {
	p, calls := newTestPower(0)
	if err := p.Reboot(context.Background(), 0); err != nil {
		t.Fatalf("重启失败: %v", err)
	}
	if len(*calls) != 1 || (*calls)[0] != 0 {
		t.Errorf("应记录一次 delay=0 的调用，实际 %v", *calls)
	}
}

// TestPowerRebootDelayPassedThrough 验证延后秒数传递。
func TestPowerRebootDelayPassedThrough(t *testing.T) {
	p, calls := newTestPower(0)
	if err := p.Reboot(context.Background(), 600); err != nil {
		t.Fatalf("重启失败: %v", err)
	}
	if (*calls)[0] != 600 {
		t.Errorf("应传递 600 秒，实际 %d", (*calls)[0])
	}
}

// TestPowerRebootRejectsBadDelay 验证非法延后值被拒绝。
func TestPowerRebootRejectsBadDelay(t *testing.T) {
	p, calls := newTestPower(0)

	if err := p.Reboot(context.Background(), -1); err == nil {
		t.Error("负数应被拒绝")
	}
	// 超过 60 分钟：与 shutdownGuard 的 +60 上限一致
	if err := p.Reboot(context.Background(), 60*60+1); err == nil {
		t.Error("超过 60 分钟应被拒绝")
	}
	if len(*calls) != 0 {
		t.Errorf("拒绝时不应调用系统命令，实际 %v", *calls)
	}
}

// TestPowerUptime 验证运行时长计算。
func TestPowerUptime(t *testing.T) {
	// 注入一个 2 小时前的启动时刻
	boot := time.Now().Add(-2 * time.Hour)
	p := NewPowerForTest(
		func() int { return 0 },
		func(ctx context.Context, delay int) error { return nil },
	)
	p.BootTimeFn = func(ctx context.Context) (time.Time, error) { return boot, nil }

	up, err := p.Uptime(context.Background())
	if err != nil {
		t.Fatalf("计算运行时长失败: %v", err)
	}
	// 允许几秒误差
	if up < 2*time.Hour-time.Minute || up > 2*time.Hour+time.Minute {
		t.Errorf("运行时长应约为 2 小时，实际 %v", up)
	}
}

// TestPowerUptimeHandlesClockRollback 验证时钟回拨时不返回负值。
//
// 界面显示「-3 小时」会让用户以为程序出错，返回 0 更诚实。
func TestPowerUptimeHandlesClockRollback(t *testing.T) {
	p := NewPowerForTest(
		func() int { return 0 },
		func(ctx context.Context, delay int) error { return nil },
	)
	// 启动时刻在「未来」
	p.BootTimeFn = func(ctx context.Context) (time.Time, error) {
		return time.Now().Add(time.Hour), nil
	}

	up, err := p.Uptime(context.Background())
	if err != nil {
		t.Fatalf("不应报错: %v", err)
	}
	if up != 0 {
		t.Errorf("时钟回拨时应返回 0，实际 %v", up)
	}
}

// TestShutdownArgsFormat 验证 shutdown 参数的实际形态。
//
// 这条测试的是「交给系统的到底是 now 还是 +N」：向上取整的语义
// 必须被测到，否则 90 秒可能被截断成 +1 分钟（提前重启）。
func TestShutdownArgsFormat(t *testing.T) {
	// 直接验证 runShutdown 的取整逻辑（不实际执行命令）
	cases := []struct {
		seconds int
		wantMin int
	}{
		{0, 0},     // now
		{1, 1},     // 向上取整到 1 分钟
		{60, 1},    // 正好 1 分钟
		{61, 2},    // 向上取整到 2 分钟
		{90, 2},    // 1.5 分钟 → 2
		{3600, 60}, // 60 分钟上限
	}
	for _, c := range cases {
		got := ceildivMinutes(c.seconds)
		if got != c.wantMin {
			t.Errorf("%d 秒应取整为 %d 分钟，实际 %d", c.seconds, c.wantMin, got)
		}
	}
}

// TestPowerRejectsDelayAboveGuardLimit 验证与 execx 守卫的上限一致。
//
// 两处上限必须一致：若 Power 允许 120 分钟而 shutdownGuard 只允许 60，
// 用户会看到「设置成功」但命令被拒，错误出现在最靠后的环节。
func TestPowerRejectsDelayAboveGuardLimit(t *testing.T) {
	p, _ := newTestPower(0)
	if err := p.Reboot(context.Background(), 61*60); err == nil {
		t.Error("超过 60 分钟的延后应被拒绝")
	}
	// 正好 60 分钟应通过
	if err := p.Reboot(context.Background(), 60*60); err != nil {
		t.Errorf("60 分钟应通过，实际: %v", err)
	}
}
