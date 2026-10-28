package firewalld

import (
	"os"
	"strings"

	"github.com/lancelot-lewis/MacPanel/internal/model"
)

// anchorReferenced 检查主配置文件是否引用了指定 anchor。
//
// 返回三个状态而不是一个 bool，是因为「读不到配置」与「配置里确实没有」
// 是完全不同的事实，不能互相冒充：
//
//	known=false            → 无法判断（文件不可读），调用方不应据此阻断操作；
//	known=true, ref=false  → 已确认未引用，此时加载 anchor 不会产生任何效果；
//	known=true, ref=true   → 已引用。
//
// 判定只看非注释行里同时出现 anchor 关键字与该 anchor 名，
// 覆盖 `anchor "x"` 与 `load anchor "x" from "..."` 两种写法。
func anchorReferenced(confPath, anchorName string) (ref bool, known bool) {
	if confPath == "" || anchorName == "" {
		return false, false
	}
	data, err := os.ReadFile(confPath)
	if err != nil {
		return false, false
	}
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if strings.Contains(line, "anchor") && strings.Contains(line, anchorName) {
			return true, true
		}
	}
	return false, true
}

// ensureAnchorReferenced 是应用规则前的硬前置校验。
//
// 为什么必须阻断：anchor 只有在 /etc/pf.conf 里被 anchor / load anchor 引用后，
// 主规则集才会跳转进去评估。没有引用时 `pfctl -a <name> -f <file>` 依然会
// 「成功」——规则静态地存在、甚至出现在 `pfctl -s Anchors` 里，但没有任何包
// 会命中它们。此时告诉用户「已应用」是彻头彻尾的假状态：防火墙看起来在工作，
// 实际一点没拦。宁可失败并说清原因，也不返回一个漂亮的假成功。
func (m *Manager) ensureAnchorReferenced() error {
	ref, known := anchorReferenced(m.pfConfPath, m.anchorName)
	if !known {
		// 无法确认时不阻断：路径未配置（-dev/测试）或配置不可读（非 root）。
		// 这类情况在状态接口里会以 warnings 形式如实呈现，不在这里假装通过。
		return nil
	}
	if ref {
		return nil
	}
	return model.NewError(model.CodeConflict,
		"anchor %q 未在 %s 中被引用，规则即使加载也不会被任何流量匹配。"+
			"请重新安装 PKG（安装脚本会写入 anchor 与 load anchor 两行），"+
			"或手工追加后执行 sudo pfctl -f %s",
		m.anchorName, m.pfConfPath, m.pfConfPath)
}
