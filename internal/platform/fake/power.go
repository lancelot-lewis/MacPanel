package fake

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/lancelot-lewis/MacPanel/internal/platform"
)

// Power 是可控的模拟电源控制器。
//
// 用途：在没有 root 的开发环境与单元测试中走通「排定重启 → 执行」
// 的完整流程，而不真的重启机器。
//
// 安全设计：默认 CanReboot 为 true 以便测试正向路径，但**永不执行**真实重启——
// Reboot 只记录调用。需要模拟失败时设置 RebootErr。
type Power struct {
	// CanReboot 为 false 时模拟非 root 环境。
	CanReboot bool
	// DenyReason 是非 root 时的原因文案。
	DenyReason string
	// RebootErr 非空时 Reboot 返回该错误。
	RebootErr error
	// BootAt 是模拟的启动时刻。
	BootAt time.Time

	mu sync.Mutex
	// Calls 记录所有 Reboot 调用（延后秒数），供断言。
	Calls []int
}

var _ platform.PowerController = (*Power)(nil)

// NewPower 返回可用的模拟电源控制器（能重启，记录调用）。
func NewPower() *Power {
	return &Power{
		CanReboot:  true,
		DenyReason: "模拟环境：不具备重启能力",
		BootAt:     time.Now().Add(-48 * time.Hour),
	}
}

// NewUnprivilegedPower 返回模拟非 root 环境的控制器。
func NewUnprivilegedPower() *Power {
	return &Power{
		CanReboot:  false,
		DenyReason: "当前进程不是 root，无法执行系统重启",
		BootAt:     time.Now().Add(-2 * time.Hour),
	}
}

// Capability 返回模拟的重启能力。
func (p *Power) Capability() (bool, string) {
	if p.CanReboot {
		return true, ""
	}
	return false, p.DenyReason
}

// Reboot 记录调用但不真的重启。
func (p *Power) Reboot(ctx context.Context, delaySeconds int) error {
	ok, reason := p.Capability()
	if !ok {
		return fmt.Errorf("%s", reason)
	}
	if p.RebootErr != nil {
		return p.RebootErr
	}
	if delaySeconds < 0 {
		return fmt.Errorf("延后秒数不能为负: %d", delaySeconds)
	}

	p.mu.Lock()
	defer p.mu.Unlock()
	p.Calls = append(p.Calls, delaySeconds)
	return nil
}

// Uptime 返回模拟的运行时长。
func (p *Power) Uptime(ctx context.Context) (time.Duration, error) {
	up := time.Since(p.BootAt)
	if up < 0 {
		return 0, nil
	}
	return up, nil
}

// BootTime 返回模拟的启动时刻。
func (p *Power) BootTime(ctx context.Context) (time.Time, error) {
	return p.BootAt, nil
}

// RebootCount 返回已记录的 Reboot 调用次数（并发安全）。
func (p *Power) RebootCount() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.Calls)
}
