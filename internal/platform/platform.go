// Package platform 定义与操作系统交互的抽象接口。
//
// 分层目的：
//   - 业务模块（agent/api）只依赖本包接口，不直接调用系统命令；
//   - darwin 子包实现真实调用，fake 子包提供可测试的模拟实现；
//   - 便于在非 macOS 环境跑单元测试，也便于注入失败场景。
//
// 权限演进：
//   - M1 只有只读能力（Collector）；
//   - M2 的 PF 写入在 platform/darwin/firewalld 里独立建模（含事务与回滚）；
//   - M3 增加 PowerController（受控重启），写能力集中在固定接口后。
package platform

import (
	"context"
	"time"

	"github.com/lancelot-lewis/MacPanel/internal/model"
)

// Collector 是只读系统状态采集接口。
type Collector interface {
	// SystemOverview 返回系统概览。无法采集的指标应体现在 Missing 字段，
	// 而不是返回零值冒充正常。
	SystemOverview(ctx context.Context) (model.SystemOverview, error)
	// NetworkSnapshot 返回网络接口与地址快照。
	NetworkSnapshot(ctx context.Context) (model.NetworkSnapshot, error)
	// FirewallStatus 返回 PF 与本应用 Anchor 的只读状态。
	FirewallStatus(ctx context.Context) (model.FirewallStatus, error)
}

// PowerController 是受控电源操作接口。
//
// 只有「重启」，没有关机与断电：本应用面向无人值守的 HomeLab，
// 关机后若机器不在物理可及范围，用户就彻底失联了。
// 因此 shutdownGuard 在命令层也把 -h/-p 封死。
type PowerController interface {
	// Capability 返回是否具备重启能力，以及不具备时的可读原因。
	//
	// 不返回 error：能力缺失是正常状态（非 root 运行），
	// 界面需要的是「为什么不能」而不是一个错误。
	Capability() (ok bool, reason string)
	// Reboot 执行重启。delaySeconds 为 0 表示立即重启。
	//
	// 调用方必须已确认风险并写入审计——本方法不做二次确认，
	// 因为它只是执行原语。
	Reboot(ctx context.Context, delaySeconds int) error
	// Uptime 返回系统已运行时长。
	Uptime(ctx context.Context) (time.Duration, error)
	// BootTime 返回本次启动时刻。
	BootTime(ctx context.Context) (time.Time, error)
}

// Config 是采集器配置。
type Config struct {
	// AnchorName 本应用专用 anchor 名（不带路径）。
	AnchorName string
	// AnchorPath anchor 规则文件绝对路径。
	AnchorPath string
	// PFConfPath 主配置文件路径，用于检查 anchor 是否被引用。
	PFConfPath string
}

// DefaultConfig 返回 macOS 上的标准路径。
func DefaultConfig(anchorName, anchorPath string) Config {
	return Config{
		AnchorName: anchorName,
		AnchorPath: anchorPath,
		PFConfPath: "/etc/pf.conf",
	}
}
