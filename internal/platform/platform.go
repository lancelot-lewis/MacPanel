// Package platform 定义与操作系统交互的抽象接口。
//
// 分层目的：
//   - 业务模块（agent/api）只依赖本包接口，不直接调用系统命令；
//   - darwin 子包实现真实调用，fake 子包提供可测试的模拟实现；
//   - 便于在非 macOS 环境跑单元测试，也便于注入失败场景。
//
// 明确约束：本包只提供只读能力。M1 阶段不包含任何写操作（PF 应用、重启、
// launchctl 变更），写入能力将在 M2/M3 单独建模并加入事务与回滚。
package platform

import (
	"context"

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
