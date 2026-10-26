// Package fake 提供 platform.Collector 的模拟实现。
//
// 用途：
//   - 在非 macOS 环境运行测试与前端开发；
//   - 注入失败场景，验证「不可用不伪造」的处理路径。
//
// 注意：fake 数据必须在 UI 上明确标注为模拟数据，不能与真实状态混淆。
package fake

import (
	"context"
	"time"

	"github.com/lancelot-lewis/MacPanel/internal/model"
	"github.com/lancelot-lewis/MacPanel/internal/platform"
)

// Collector 是可控的模拟采集器。
type Collector struct {
	// OverviewFn 非空时优先使用，便于注入错误。
	OverviewFn func(ctx context.Context) (model.SystemOverview, error)
	NetworkFn  func(ctx context.Context) (model.NetworkSnapshot, error)
	FirewallFn func(ctx context.Context) (model.FirewallStatus, error)
}

var _ platform.Collector = (*Collector)(nil)

// New 返回带示例数据的采集器。
func New() *Collector { return &Collector{} }

// SystemOverview 返回示例系统概览。
func (c *Collector) SystemOverview(ctx context.Context) (model.SystemOverview, error) {
	if c.OverviewFn != nil {
		return c.OverviewFn(ctx)
	}
	now := time.Now().UTC()
	used := int64(8 << 30)
	free := int64(8 << 30)
	avail := int64(9 << 30)
	boot := now.Add(-72 * time.Hour)
	uptime := 72 * 3600.0
	return model.SystemOverview{
		Hostname: "fake-mac-mini",
		OS: model.OSInfo{
			ProductVersion: "15.0.0-fake",
			Build:          "24A000",
			Kernel:         "Darwin 24.0.0",
			Architecture:   "arm64",
		},
		CPU: model.CPUInfo{
			Model:         "Apple M4 (fake)",
			PhysicalCores: 10,
			LogicalCores:  10,
			LoadAverage:   &model.LoadAvg{One: 0.42, Five: 0.35, Fifteen: 0.30},
		},
		Memory: model.MemoryInfo{
			TotalBytes:     16 << 30,
			UsedBytes:      &used,
			FreeBytes:      &free,
			AvailableBytes: &avail,
			State:          model.MetricOK,
		},
		Disks: []model.DiskUsage{{
			MountPoint: "/",
			FileSystem: "apfs",
			TotalBytes: 228 << 30,
			FreeBytes:  80 << 30,
			UsedBytes:  148 << 30,
			UsedRatio:  0.65,
		}},
		BootTime:      &boot,
		UptimeSeconds: &uptime,
		CollectedAt:   now,
	}, nil
}

// NetworkSnapshot 返回示例网络接口。
func (c *Collector) NetworkSnapshot(ctx context.Context) (model.NetworkSnapshot, error) {
	if c.NetworkFn != nil {
		return c.NetworkFn(ctx)
	}
	return model.NetworkSnapshot{
		CollectedAt: time.Now().UTC(),
		HasIPv6:     true,
		HasGlobalV6: true,
		Interfaces: []model.Interface{
			{
				Name: "lo0", Index: 1, Kind: model.KindLoopback, Up: true, Loopback: true, MTU: 16384,
				Flags: []string{"up", "loopback", "running"},
				IPv4:  []model.Address{{Address: "127.0.0.1", PrefixLen: 8, Family: "ipv4", Scope: "loopback", IsLoopback: true}},
				IPv6:  []model.Address{{Address: "::1", PrefixLen: 128, Family: "ipv6", Scope: "loopback", IsLoopback: true}},
			},
			{
				Name: "en0", Index: 4, Kind: model.KindEthernet, Up: true, MTU: 1500,
				HardwareAddr: "aa:bb:cc:dd:ee:ff",
				Flags:        []string{"up", "broadcast", "running", "multicast"},
				IPv4: []model.Address{{
					Address: "192.168.1.10", PrefixLen: 24, Family: "ipv4", Scope: "private", IsPrivate: true,
				}},
				IPv6: []model.Address{
					{Address: "fe80::1", PrefixLen: 64, Family: "ipv6", Scope: "link-local", IsLinkLocal: true},
					{Address: "2001:db8::10", PrefixLen: 64, Family: "ipv6", Scope: "global"},
				},
			},
		},
	}, nil
}

// FirewallStatus 返回示例防火墙状态（明确标记为未配置）。
func (c *Collector) FirewallStatus(ctx context.Context) (model.FirewallStatus, error) {
	if c.FirewallFn != nil {
		return c.FirewallFn(ctx)
	}
	enabled := false
	size := int64(0)
	return model.FirewallStatus{
		Managed: model.StateNotConfigured,
		PF: model.PFInfo{
			Enabled:       &enabled,
			State:         "disabled",
			ReadAvailable: true,
			Detail:        "模拟数据（fake collector）",
		},
		Anchor: model.AnchorInfo{
			Name:       "com.macpanel.ipv6",
			RulePath:   "/etc/pf.anchors/com.macpanel.ipv6",
			State:      model.AnchorAbsent,
			Present:    false,
			Referenced: false,
			SizeBytes:  &size,
			Notes:      []string{"模拟数据：规则文件未创建"},
		},
		Warnings:    []string{"当前使用模拟采集器，所有数值均非真实系统状态"},
		CollectedAt: time.Now().UTC(),
		Writable:    false,
	}, nil
}
