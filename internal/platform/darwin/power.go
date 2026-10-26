package darwin

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/lancelot-lewis/MacPanel/internal/platform/execx"
)

// ShutdownPath 是 macOS 上的关机/重启工具。
const ShutdownPath = "/sbin/shutdown"

// Power 实现 platform.PowerController。
type Power struct {
	// euid 可在测试中注入，避免测试进程的权限影响判定。
	euid func() int
	// run 可在测试中注入，避免真的重启机器。
	run func(ctx context.Context, delaySeconds int) error

	// BootTimeFn 可在测试中替换，避免依赖真实的 kern.boottime。
	BootTimeFn func(ctx context.Context) (time.Time, error)
}

// NewPower 创建真实的电源控制器。
func NewPower() *Power {
	p := &Power{
		euid: os.Geteuid,
		run:  runShutdown,
	}
	p.BootTimeFn = BootTime
	return p
}

// NewPowerForTest 创建可注入依赖的实例（仅供测试）。
func NewPowerForTest(euid func() int, run func(context.Context, int) error) *Power {
	p := &Power{euid: euid, run: run}
	p.BootTimeFn = BootTime
	return p
}

// Capability 判断是否具备重启能力。
//
// 只有 root 能调用 shutdown。非 root 时返回明确原因，
// 让界面能显示「已排定但不会生效」——这比让用户以为设置成功要好得多。
func (p *Power) Capability() (bool, string) {
	if p.euid() != 0 {
		return false, "当前进程不是 root，无法执行系统重启（需以 root 运行 Agent）"
	}
	return true, ""
}

// Reboot 执行重启。
//
// delaySeconds 会转成 shutdown 的 +N 分钟。注意 shutdown 的粒度是分钟，
// 因此这里向上取整：传入 90 秒会变成 +2 分钟，而不是被截断成 +1
// （提前重启比延后重启更容易造成意外）。
func (p *Power) Reboot(ctx context.Context, delaySeconds int) error {
	if ok, reason := p.Capability(); !ok {
		return fmt.Errorf("%s", reason)
	}
	if delaySeconds < 0 {
		return fmt.Errorf("延后秒数不能为负: %d", delaySeconds)
	}
	// 上限与 shutdownGuard 的 +60 保持一致。
	if delaySeconds > 60*60 {
		return fmt.Errorf("延后时间不能超过 60 分钟，实际 %d 秒", delaySeconds)
	}
	return p.run(ctx, delaySeconds)
}

// Uptime 返回系统运行时长。
func (p *Power) Uptime(ctx context.Context) (time.Duration, error) {
	boot, err := p.BootTime(ctx)
	if err != nil {
		return 0, err
	}
	up := time.Since(boot)
	if up < 0 {
		// 时钟回拨：返回 0 而不是负值，避免界面显示 "-3 小时"。
		return 0, nil
	}
	return up, nil
}

// BootTime 返回本次启动时刻。
func (p *Power) BootTime(ctx context.Context) (time.Time, error) {
	if p.BootTimeFn != nil {
		return p.BootTimeFn(ctx)
	}
	return BootTime(ctx)
}

// ceildivMinutes 把秒向上取整为分钟，0 秒返回 0（表示立即）。
//
// 单独抽成函数是为了能被测试直接验证：向上取整的语义很关键，
// 90 秒若被截断成 +1 分钟就变成「提前重启」，而延后一分钟
// 只需要多等 30 秒。
func ceildivMinutes(seconds int) int {
	if seconds <= 0 {
		return 0
	}
	minutes := (seconds + 59) / 60
	if minutes < 1 {
		minutes = 1
	}
	return minutes
}

// runShutdown 调用系统 shutdown 执行重启。
func runShutdown(ctx context.Context, delaySeconds int) error {
	when := "now"
	if delaySeconds > 0 {
		when = fmt.Sprintf("+%d", ceildivMinutes(delaySeconds))
	}

	// 超时设为 10 秒：shutdown 应该在收到请求后立即返回，
	// 若它卡住说明系统状态异常，本应用也不该无限等待。
	if _, err := execx.Run(ctx, 10*time.Second, ShutdownPath, "-r", when); err != nil {
		return fmt.Errorf("执行重启失败: %w", err)
	}
	return nil
}
