// Package pfrules 把规则集生成 PF anchor 文本。
//
// 设计核心：**生成器只产出一个 anchor 的规则文本**，绝不生成
// `set skip on`、`scrub`、`nat`、`rdr` 或任何主规则集指令。
// 原因见 PLAN.md 不变量 8：`set skip on lo0` 属于全局配置，
// 不能在一个 anchor 内生效；写入主规则集会干扰其它程序。
//
// 生成文本的顺序是有意义的：PF 是首个匹配即生效（配 quick），
// 因此「强制放行」必须排在「用户规则」之前，「默认拒绝」必须排在最后。
package pfrules

import (
	"fmt"
	"strings"

	"github.com/lancelot-lewis/MacPanel/internal/model"
)

// Header 是生成文件的头部注释。
// 写入这行是刻意的：任何人 cat 这个文件都知道它是被谁管理的、
// 不要手工编辑——否则下次应用会被覆盖。
const Header = `# 由 MacPanel 自动生成，请勿手工编辑。
# 手工修改会在下次应用规则时被覆盖。
# 管理的锚点名：%s
`

// Generated 是生成结果。
type Generated struct {
	// Content 是完整的 anchor 规则文本。
	Content string
	// RuleCount 是实际生成的用户规则条数（不含强制放行与默认拒绝）。
	RuleCount int
	// Enforced 列出本次强制加入的系统规则，便于界面如实告知用户
	// 「你写的规则之外，我还加了这些」。
	Enforced []EnforcedRule
	// Skipped 列出被跳过的规则及原因（例如禁用的规则）。
	Skipped []SkippedRule
}

// EnforcedRule 描述一条由生成器强制添加的规则。
type EnforcedRule struct {
	// Key 稳定标识，便于前端展示固定文案。
	Key string `json:"key"`
	// Reason 为什么要强制加这条。
	Reason string `json:"reason"`
	// Rule 实际生成的 PF 语句。
	Rule string `json:"rule"`
}

// SkippedRule 描述被跳过的用户规则。
type SkippedRule struct {
	RuleID int64  `json:"ruleId"`
	Name   string `json:"name"`
	Reason string `json:"reason"`
}

// 强制规则的稳定键，前端据此展示说明文案。
const (
	EnforcedLoopback     = "loopback"
	EnforcedIPv6Loopback = "ipv6-loopback"
	EnforcedOutbound     = "outbound"
	EnforcedICMPv6       = "icmpv6-essential"
	EnforcedTunnelKeep   = "tunnels-pass"
	EnforcedDefaultDeny  = "default-deny"
)

// Generate 生成 anchor 规则文本。
//
// 调用方必须先通过 RuleSet.Validate()，本函数只做生成不做业务校验
// （但仍会跳过无法表达的规则并在 Skipped 中说明，绝不静默丢弃）。
func Generate(ruleSet model.RuleSet, anchorName string) (*Generated, error) {
	if anchorName == "" {
		return nil, model.NewError(model.CodeInvalidArgument, "锚点名不能为空")
	}
	if err := ruleSet.Validate(); err != nil {
		return nil, err
	}

	var b strings.Builder
	gen := &Generated{}

	fmt.Fprintf(&b, Header, anchorName)
	b.WriteString("\n")

	// ---------- 第一段：强制放行 ----------
	//
	// 这些规则不受用户配置影响，也不能被用户删掉。
	// 顺序上的考虑：
	//   lo0 放行必须最先，否则本机自身访问会被后续任何 block 影响；
	//   出站放行紧随其后，保证主动连接与返回流量正常。
	//
	// 注意：这里没有用 `set skip on lo0`——那是全局配置，anchor 内无效，
	// 且会干扰其它程序的 PF 使用。用 `pass quick on lo0 all` 达到同等效果
	// 且作用域仅限本 anchor。
	b.WriteString("# ---- 强制放行（由本应用添加，不可移除）----\n")

	writeEnforced(&b, gen, EnforcedLoopback,
		"回环接口必须放行，否则本机自身访问（包括面板）会中断",
		"pass quick on lo0 all")

	writeEnforced(&b, gen, EnforcedIPv6Loopback,
		"IPv6 回环地址必须可达，避免 ::1 访问被拦截",
		"pass in quick inet6 from ::1 to ::1")

	writeEnforced(&b, gen, EnforcedOutbound,
		"主动发起的 IPv6 出站连接及其返回流量必须正常（本应用只控制入站）",
		"pass out quick inet6 all keep state")

	b.WriteString("\n# ---- 强制放行：IPv6 必需的控制协议 ----\n")

	// ICMPv6 不能整类放行也不能整类拒绝：
	// 邻居发现（NDP）、路由通告、路径 MTU 发现都依赖它。
	// 全拒会导致 IPv6 直接不可用；全放行又会暴露过多信息。
	// 这里精确放行必需类型，其余由后续默认拒绝处理。
	writeEnforced(&b, gen, EnforcedICMPv6,
		"邻居发现、路由通告、路径 MTU 发现等 IPv6 必需机制依赖这些 ICMPv6 类型；"+
			"全部封禁会导致 IPv6 完全不可用",
		"pass in quick inet6 proto ipv6-icmp from any to any icmp6-type { 1, 2, 128, 129, 133, 134, 135, 136, 137 }")

	b.WriteString("\n")

	// ---------- 第二段：用户规则 ----------
	if len(ruleSet.Rules) > 0 {
		b.WriteString("# ---- 用户规则（按顺序匹配，首个命中即生效）----\n")
	}

	var hasEnabled bool
	for _, r := range model.SortByPosition(ruleSet.Rules) {
		if !r.Enabled {
			gen.Skipped = append(gen.Skipped, SkippedRule{
				RuleID: r.ID,
				Name:   r.Name,
				Reason: "规则已禁用",
			})
			continue
		}
		line, err := renderRule(r)
		if err != nil {
			// 不静默丢弃：明确记录原因，让调用方决定是否阻断整个应用。
			gen.Skipped = append(gen.Skipped, SkippedRule{
				RuleID: r.ID,
				Name:   r.Name,
				Reason: err.Error(),
			})
			continue
		}
		hasEnabled = true
		gen.RuleCount++
		fmt.Fprintf(&b, "# [%d] %s\n", r.ID, sanitizeComment(r.Name))
		if r.Description != "" {
			fmt.Fprintf(&b, "#     %s\n", sanitizeComment(r.Description))
		}
		b.WriteString(line)
		b.WriteString("\n")
	}

	if !hasEnabled {
		b.WriteString("# （无启用的用户规则）\n")
	}

	// ---------- 第三段：默认拒绝 ----------
	//
	// 这是唯一可能导致「失联」的部分，因此：
	//   1. 只在用户显式指定接口时才生成；
	//   2. 拒绝危险接口参与（模型层已校验，这里再防一次）；
	//   3. 生成位置固定在最后，保证前面的放行规则先生效。
	if len(ruleSet.DefaultDenyInterfaces) > 0 {
		b.WriteString("\n# ---- 默认拒绝入站（仅在指定接口上）----\n")
		for _, iface := range ruleSet.DefaultDenyInterfaces {
			iface = strings.TrimSpace(iface)
			if model.IsDangerousInterface(iface) {
				// 模型层已拦截，这里属于纵深防御。
				return nil, model.NewError(model.CodeInvalidArgument,
					"接口 %s 不允许用于默认拒绝（会切断回环或隧道流量）", iface)
			}
			rule := fmt.Sprintf("block in quick inet6 on %s all", iface)
			fmt.Fprintf(&b, "# 接口 %s：除上述放行外，其余 IPv6 入站流量一律拒绝\n", iface)
			b.WriteString(rule + "\n")
			gen.Enforced = append(gen.Enforced, EnforcedRule{
				Key:    EnforcedDefaultDeny,
				Reason: fmt.Sprintf("按配置对接口 %s 实施默认拒绝入站", iface),
				Rule:   rule,
			})
		}
	} else {
		b.WriteString("\n# 未配置默认拒绝接口：本次仅添加放行规则，不阻断任何入站流量。\n")
	}

	// 收尾：把累积的文本写入返回值。
	// 这里容易漏——漏了会导致「生成成功但内容为空」，
	// 校验器会报「缺少所有强制规则」，症状看起来像生成器逻辑错，
	// 实际只是忘了赋值。测试 TestGenerateContainsMandatoryRules 就是为此而写。
	gen.Content = b.String()

	return gen, nil
}

func writeEnforced(b *strings.Builder, gen *Generated, key, reason, rule string) {
	b.WriteString(rule + "\n")
	gen.Enforced = append(gen.Enforced, EnforcedRule{
		Key:    key,
		Reason: reason,
		Rule:   rule,
	})
}

// renderRule 把单条用户规则渲染为 PF 语句。
//
// 所有语句都带 inet6 限定——这是最硬的不变量：
// 无论用户怎么配，生成的规则都不会影响 IPv4。
func renderRule(r model.FirewallRule) (string, error) {
	if err := r.Validate(); err != nil {
		return "", err
	}

	var parts []string
	parts = append(parts, string(r.Action), "in", "quick", "inet6")

	if iface := strings.TrimSpace(r.Interface); iface != "" && !strings.EqualFold(iface, "any") {
		parts = append(parts, "on", iface)
	}

	// 协议。any 时不写 proto，但仍保留 inet6 限定。
	switch r.Protocol {
	case model.ProtocolTCP:
		parts = append(parts, "proto", "tcp")
	case model.ProtocolUDP:
		parts = append(parts, "proto", "udp")
	case model.ProtocolICMP6:
		parts = append(parts, "proto", "ipv6-icmp")
	case model.ProtocolAny:
		// 不写 proto
	}

	// 来源
	src := "any"
	if cidr, err := model.NormalizeCIDR(r.SourceCIDR); err != nil {
		return "", err
	} else if cidr != "any" {
		src = cidr
	}
	parts = append(parts, "from", src, "to", "any")

	// 端口
	if r.PortFrom != nil {
		switch {
		case r.PortTo == nil || *r.PortTo == *r.PortFrom:
			parts = append(parts, "port", fmt.Sprintf("%d", *r.PortFrom))
		default:
			parts = append(parts, "port", fmt.Sprintf("%d:%d", *r.PortFrom, *r.PortTo))
		}
	}

	// 有状态跟踪：让返回流量自动通过，避免用户还要写出站规则。
	if r.Action == model.ActionPass && (r.Protocol == model.ProtocolTCP || r.Protocol == model.ProtocolUDP) {
		parts = append(parts, "keep", "state")
	}

	return strings.Join(parts, " "), nil
}

// sanitizeComment 清理将写入注释的文本。
//
// 注释里出现换行会截断规则文件结构，等于注入。
// 这里把所有控制字符与换行替换为空格，并限制长度。
func sanitizeComment(s string) string {
	s = strings.Map(func(r rune) rune {
		switch r {
		case '\n', '\r', '\t':
			return ' '
		}
		if r < 0x20 || r == 0x7f {
			return ' '
		}
		return r
	}, s)
	s = strings.TrimSpace(s)
	// 按 rune 截断，不能按字节：len(s) 是字节数，中文会被切成半个字符，
	// 生成的文件就不再是合法 UTF-8（注释里出现「��」）。
	if r := []rune(s); len(r) > 100 {
		s = string(r[:100]) + "…"
	}
	if s == "" {
		return "(未命名)"
	}
	return s
}
