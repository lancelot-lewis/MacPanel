package pfrules

import (
	"fmt"
	"strings"

	"github.com/lancelot-lewis/MacPanel/internal/model"
)

// Violation 是一条不变量违反。
type Violation struct {
	// Invariant 对应 PLAN.md 5.2 的「必须满足的网络不变量」编号（1-8）。
	Invariant int    `json:"invariant"`
	Key       string `json:"key"`
	Message   string `json:"message"`
	// Line 是相关规则的行号（从 1 开始），0 表示整体性问题。
	Line int `json:"line,omitempty"`
}

// Verification 是校验结果。
type Verification struct {
	// OK 为 true 表示可以进入下一步（语法与不变量都通过）。
	OK bool `json:"ok"`
	// SyntaxErrors 是语法层面问题（纯 Go 解析得出）。
	SyntaxErrors []Violation `json:"syntaxErrors"`
	// InvariantViolations 是网络不变量问题。
	InvariantViolations []Violation `json:"invariantViolations"`
	// Stats 用于界面展示本份规则的规模。
	Stats VerifyStats `json:"stats"`
}

// VerifyStats 是规则文本的统计信息。
type VerifyStats struct {
	TotalLines    int `json:"totalLines"`
	RuleLines     int `json:"ruleLines"`
	PassRules     int `json:"passRules"`
	BlockRules    int `json:"blockRules"`
	Inet6Rules    int `json:"inet6Rules"`
	NonInet6Rules int `json:"nonInet6Rules"`
	CommentLines  int `json:"commentLines"`
}

// 允许的 PF 关键字白名单。
//
// 为什么需要白名单而不是黑名单：黑名单总会漏。
// 这里只允许 anchor 内真正需要的动词，任何 `set`/`scrub`/`nat`/`rdr`/
// `table`/`include` 都会被拒绝——它们要么是全局配置，要么会引入外部文件。
var allowedVerbs = map[string]bool{
	"pass":  true,
	"block": true,
}

// forbiddenVerbs 是明确禁止的关键字，给出更有针对性的错误信息。
var forbiddenVerbs = map[string]string{
	"set":       "set 指令属于全局配置，不能在 anchor 内生效，且会干扰其它程序",
	"scrub":     "scrub 属于主规则集配置，本应用不修改",
	"nat":       "NAT 不在本应用职责范围内",
	"rdr":       "端口转发不在本应用职责范围内",
	"binat":     "NAT 不在本应用职责范围内",
	"table":     "自定义表会引入额外状态，当前不支持",
	"include":   "不允许包含外部文件（会产生不可审计的依赖）",
	"anchor":    "不允许嵌套 anchor（会让状态难以追踪）",
	"load":      "不允许加载其它 anchor",
	"dummynet":  "流量整形不在本应用职责范围内",
	"antispoof": "antispoof 会生成隐式规则，超出本应用的可预测范围",
}

// Verify 校验生成的规则文本。
//
// 双层校验：
//  1. 语法层：逐行解析，确认动词合法、语句结构完整；
//  2. 不变量层：程序化确认 PLAN.md 5.2 的 8 条不变量中
//     可以在文本层面验证的部分。
//
// 这不替代 `pfctl -n -f`（后者由 agent 在应用前调用），
// 而是在生成后立刻给出可读反馈，避免把明显错误的规则送去 pfctl。
func Verify(content string, defaultDenyInterfaces []string) Verification {
	v := Verification{OK: true}

	lines := strings.Split(content, "\n")
	v.Stats.TotalLines = len(lines)

	var hasLoopbackPass, hasIPv6Loopback, hasOutboundPass bool
	var hasIcmpv6Pass bool
	var hasDefaultDeny bool
	var defaultDenySeen = map[string]bool{}

	for idx, raw := range lines {
		lineNo := idx + 1
		line := strings.TrimSpace(raw)

		if line == "" {
			continue
		}
		if strings.HasPrefix(line, "#") {
			v.Stats.CommentLines++
			continue
		}

		v.Stats.RuleLines++

		// --- 语法层 ---
		fields := strings.Fields(line)
		if len(fields) < 2 {
			v.addSyntax(lineNo, "语句过短，至少需要「动作 + 方向」")
			continue
		}
		verb := fields[0]

		if reason, forbidden := forbiddenVerbs[verb]; forbidden {
			v.addSyntax(lineNo, fmt.Sprintf("禁止使用 %q：%s", verb, reason))
			continue
		}
		if !allowedVerbs[verb] {
			v.addSyntax(lineNo, fmt.Sprintf("未知动作 %q（只允许 pass / block）", verb))
			continue
		}

		// 方向可以出现在 quick 之前或之后，两种都是合法写法：
		//   pass in quick ...
		//   pass quick on lo0 all      ← quick 在前时方向可省略（隐含 in/out 均可）
		// 因此不能简单取 fields[1] 判断。
		direction := ""
		hasDirection := false
		for _, f := range fields[1:] {
			if f == "in" || f == "out" {
				direction = f
				hasDirection = true
				break
			}
			if f == "quick" || f == "log" || f == "on" {
				continue
			}
			break
		}
		if !hasDirection && !strings.Contains(line, "quick") {
			v.addSyntax(lineNo, "缺少方向（in/out）")
			continue
		}

		// keep state 只能在 pass 上
		if verb == "block" && strings.Contains(line, "keep state") {
			v.addSyntax(lineNo, "block 规则不能带 keep state")
		}

		// 端口必须成对出现（PF 的 port a:b 语法）
		if strings.Contains(line, "port") {
			after := line[strings.Index(line, "port")+len("port"):]
			after = strings.TrimSpace(after)
			if after == "" {
				v.addSyntax(lineNo, "port 后缺少端口值")
			}
		}

		// 关键字顺序：PF 要求 on <接口> 出现在地址族（inet/inet6）之前。
		// `block in quick inet6 on en0 all` 会被 pfctl 判为 syntax error，
		// 正确写法是 `block in quick on en0 inet6 all`。
		if idxOn, idxAF := indexOfField(fields, "on"), indexOfAny(fields, "inet", "inet6"); idxOn >= 0 && idxAF >= 0 && idxOn > idxAF {
			v.addSyntax(lineNo, "on 必须写在 inet/inet6 之前（正确写法：block in quick on en0 inet6 all）")
		}

		// --- 不变量层 ---
		switch {
		case verb == "pass" && strings.Contains(line, "on lo0"):
			// `pass quick on lo0 all` 不带 in/out 是刻意的：
			// 同时放行进出两个方向，且同时覆盖 v4/v6。
			hasLoopbackPass = true
		case verb == "pass" && strings.Contains(line, "from ::1") && strings.Contains(line, "to ::1"):
			hasIPv6Loopback = true
		case verb == "pass" && direction == "out" && strings.Contains(line, "inet6"):
			hasOutboundPass = true
		case verb == "pass" && strings.Contains(line, "ipv6-icmp") && strings.Contains(line, "icmp6-type"):
			hasIcmpv6Pass = true
		}

		// 统计 inet6 限定
		if strings.Contains(line, " inet6") || strings.HasSuffix(line, "inet6") {
			v.Stats.Inet6Rules++
		} else {
			// 回环放行 `pass quick on lo0 all` 不带 inet6 是刻意的（同时覆盖 v4/v6），
			// 不计入违规。
			if !strings.Contains(line, "on lo0") {
				v.Stats.NonInet6Rules++
			}
		}

		if verb == "pass" {
			v.Stats.PassRules++
		} else if verb == "block" {
			v.Stats.BlockRules++
			if strings.Contains(line, " on ") {
				parts := strings.Fields(line)
				for i, f := range parts {
					if f == "on" && i+1 < len(parts) {
						iface := parts[i+1]
						// 不变量 6：不能无条件放行/拒绝全部 utun*/bridge*
						if strings.HasPrefix(iface, "utun") || strings.HasPrefix(iface, "bridge") {
							v.addInvariant(lineNo, 6, "tunnel-interface-deny",
								fmt.Sprintf("不应针对 %s 做默认拒绝：VPN/网桥接口可能承载远程管理通道", iface))
						}
						if model.IsDangerousInterface(iface) {
							v.addInvariant(lineNo, 2, "loopback-protected",
								fmt.Sprintf("接口 %s 属于必须放行的类别（回环/隧道），不能出现在 block 规则中", iface))
						}
						defaultDenySeen[iface] = true
					}
				}
				hasDefaultDeny = true
			}
		}
	}

	// ---------- 不变量：必须存在的强制放行 ----------
	if !hasLoopbackPass {
		v.addInvariant(0, 2, "missing-loopback-pass",
			"缺少 lo0 放行规则：本机自身访问与面板都会被切断")
	}
	if !hasIPv6Loopback {
		v.addInvariant(0, 2, "missing-ipv6-loopback",
			"缺少 ::1 回环放行：IPv6 本机访问会被拦截")
	}
	if !hasOutboundPass {
		v.addInvariant(0, 3, "missing-outbound-pass",
			"缺少 IPv6 出站放行：主动发起的连接及其返回流量会中断")
	}
	if !hasIcmpv6Pass {
		v.addInvariant(0, 4, "missing-icmpv6",
			"缺少必需 ICMPv6 类型放行：邻居发现与路径 MTU 发现会失效，"+
				"IPv6 实际不可用")
	}

	// ---------- 不变量 1：不得限制 IPv4 ----------
	//
	// anchor 内的所有语句都必须带 inet6（或显式的回环放行）。
	// 若出现无 inet6 的 pass/block 且不是 lo0，说明可能影响 IPv4。
	for idx, raw := range lines {
		line := strings.TrimSpace(raw)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if !strings.Contains(line, "inet6") && !strings.Contains(line, "on lo0") {
			v.addInvariant(idx+1, 1, "may-affect-ipv4",
				"该语句未限定 inet6，可能影响 IPv4 行为（违反「自定义规则不限制 IPv4」）")
		}
	}

	// ---------- 不变量 7：不得关闭 PF ----------
	// 生成器不会产出 pfctl -d，这里检查文本里没有类似意图的注释配置。
	if strings.Contains(content, "pfctl -d") {
		v.addInvariant(0, 7, "pf-disable-detected",
			"检测到关闭 PF 的意图：本应用绝不以关闭 PF 作为暂停或卸载手段")
	}

	// ---------- 默认拒绝接口一致性 ----------
	if len(defaultDenyInterfaces) > 0 {
		for _, iface := range defaultDenyInterfaces {
			if !defaultDenySeen[iface] {
				v.addInvariant(0, 6, "default-deny-missing",
					fmt.Sprintf("配置要求对 %s 默认拒绝，但生成的规则中没有对应语句", iface))
			}
		}
	}
	_ = hasDefaultDeny

	v.OK = len(v.SyntaxErrors) == 0 && len(v.InvariantViolations) == 0
	return v
}

func (v *Verification) addSyntax(line int, msg string) {
	v.SyntaxErrors = append(v.SyntaxErrors, Violation{Key: "syntax", Message: msg, Line: line})
}

// indexOfField 返回 tokens 中第一个完全等于 want 的下标，找不到返回 -1。
func indexOfField(tokens []string, want string) int {
	for i, t := range tokens {
		if t == want {
			return i
		}
	}
	return -1
}

// indexOfAny 返回 tokens 中第一个命中 wants 任一项的下标，找不到返回 -1。
func indexOfAny(tokens []string, wants ...string) int {
	for i, t := range tokens {
		for _, w := range wants {
			if t == w {
				return i
			}
		}
	}
	return -1
}

func (v *Verification) addInvariant(line, invariant int, key, msg string) {
	v.InvariantViolations = append(v.InvariantViolations, Violation{
		Invariant: invariant,
		Key:       key,
		Message:   msg,
		Line:      line,
	})
}
