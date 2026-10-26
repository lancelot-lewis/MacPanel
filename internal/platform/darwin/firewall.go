//go:build darwin

package darwin

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/lancelot-lewis/MacPanel/internal/model"
	"github.com/lancelot-lewis/MacPanel/internal/platform/execx"
)

// FirewallStatus 采集 PF 与本应用 anchor 的只读状态。
//
// 关键原则（PLAN.md 第 5.2 节不变量 7）：
//   - 绝不使用 pfctl -d，也绝不修改 PF 任何状态；
//   - 读不到 /dev/pf（非 root）时把 enabled 置为 nil 并给出 readError，
//     不用 false 表示「已关闭」；
//   - anchor 文件不可读时只报告存在性，不猜测内容。
func (c *Collector) FirewallStatus(ctx context.Context) (model.FirewallStatus, error) {
	now := time.Now().UTC()
	st := model.FirewallStatus{
		Managed:     model.StateNotConfigured,
		CollectedAt: now,
		Writable:    false, // M1 未实现写路径，明确声明不可写
	}

	// --- PF 全局状态 ---
	st.PF = collectPFInfo(ctx)
	if !st.PF.ReadAvailable {
		st.Warnings = append(st.Warnings,
			"无法读取 PF 运行状态（需要 root 权限访问 /dev/pf）；页面显示的 PF 状态为「未知」而不是「已关闭」")
	}

	// --- 本应用 anchor ---
	st.Anchor = c.collectAnchorInfo()

	// --- 已加载的 anchor 列表（需要 root） ---
	if st.PF.ReadAvailable {
		res, err := execx.Run(ctx, cmdTimeout, "/sbin/pfctl", "-s", "Anchors")
		if err == nil {
			st.LoadedAnchors = parseAnchorList(res.Stdout)
		} else {
			st.Warnings = append(st.Warnings, "无法列出已加载 anchor："+reasonOf(err))
		}
	}

	// 期望状态判定：只有 anchor 文件存在且主配置引用它，才算「已配置」。
	if st.Anchor.Present && st.Anchor.Referenced {
		st.Managed = model.StateConfigured
		if st.PF.ReadAvailable && st.Anchor.State != model.AnchorLoaded {
			st.Managed = model.StateDrift
			st.Warnings = append(st.Warnings,
				"anchor 规则文件与主配置引用均存在，但 PF 未加载该 anchor，实际状态与期望不一致")
		}
	}
	if st.Anchor.Present && !st.Anchor.Referenced {
		st.Warnings = append(st.Warnings,
			"anchor 规则文件存在，但未在 /etc/pf.conf 中引用，规则不会生效")
	}

	return st, nil
}

func collectPFInfo(ctx context.Context) model.PFInfo {
	info := model.PFInfo{State: "unknown"}

	res, err := execx.Run(ctx, cmdTimeout, "/sbin/pfctl", "-s", "info")
	if err != nil {
		// 权限不足是最常见原因：明确区分「不可读」与「已关闭」。
		info.ReadAvailable = false
		info.ReadError = reasonOf(err)
		if strings.Contains(strings.ToLower(info.ReadError), "permission denied") {
			info.Detail = "当前进程无权限读取 PF 状态（/dev/pf 仅 root 可访问）"
		}
		return info
	}

	info.ReadAvailable = true
	out := res.Stdout
	switch {
	case strings.Contains(out, "Status: Enabled"):
		enabled := true
		info.Enabled = &enabled
		info.State = "enabled"
	case strings.Contains(out, "Status: Disabled"):
		enabled := false
		info.Enabled = &enabled
		info.State = "disabled"
	}

	if strings.Contains(out, "Enforce: enabled") {
		v := true
		info.EnforceState = &v
	} else if strings.Contains(out, "Enforce: disabled") {
		v := false
		info.EnforceState = &v
	}
	return info
}

func (c *Collector) collectAnchorInfo() model.AnchorInfo {
	info := model.AnchorInfo{
		Name:     c.cfg.AnchorName,
		RulePath: c.cfg.AnchorPath,
		State:    model.AnchorUnknown,
	}

	// 文件存在性（Lstat 不需要读权限）
	if fst, err := os.Lstat(c.cfg.AnchorPath); err == nil {
		info.Present = true
		size := fst.Size()
		info.SizeBytes = &size
		mod := fst.ModTime().UTC()
		info.LastModified = &mod

		if fst.Mode().IsRegular() {
			info.State = model.AnchorPresent
			// 尝试读取内容以确认非空；无权限时保留 present 状态。
			if data, err := os.ReadFile(c.cfg.AnchorPath); err == nil {
				if len(strings.TrimSpace(string(data))) > 0 {
					info.State = model.AnchorPresent // 内容非空，但加载状态仍需 pfctl 确认
				}
			} else {
				info.Notes = append(info.Notes,
					"规则文件无读取权限（属主为 root），只能确认文件存在")
			}
		} else {
			info.Notes = append(info.Notes, "路径存在但不是普通文件")
		}
	} else if os.IsNotExist(err) {
		info.State = model.AnchorAbsent
		info.Notes = append(info.Notes, "规则文件不存在（M1 不创建任何 anchor 文件）")
	} else {
		info.Notes = append(info.Notes, "无法检查规则文件："+err.Error())
	}

	// 主配置是否引用该 anchor
	info.Referenced = pfConfReferences(c.cfg.PFConfPath, c.cfg.AnchorName)
	return info
}

// pfConfReferences 检查 /etc/pf.conf 是否引用指定 anchor。
// 该文件通常 0600 root:wheel，非 root 读不到，此时返回 false 并
// 由调用方附加警告，绝不能因此推断「未引用」是事实。
func pfConfReferences(confPath, anchorName string) bool {
	data, err := os.ReadFile(confPath)
	if err != nil {
		return false
	}
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "#") {
			continue
		}
		if strings.Contains(line, "anchor") && strings.Contains(line, anchorName) {
			return true
		}
	}
	return false
}

func parseAnchorList(out string) []string {
	var list []string
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		list = append(list, line)
	}
	return list
}

// String 便于日志输出。
func (c *Collector) String() string {
	return fmt.Sprintf("darwin.Collector{anchor=%s}", c.cfg.AnchorName)
}
