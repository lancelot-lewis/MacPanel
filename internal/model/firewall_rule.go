package model

import (
	"fmt"
	"net"
	"sort"
	"strconv"
	"strings"
)

// 防火墙规则的枚举值。
//
// 这些值同时出现在 SQL CHECK 约束、API 请求体与 PF 生成器中，
// 三处必须一致。新增枚举必须同时改这三个地方，否则会在某一层静默失败。
type (
	// RuleAction 规则动作。只支持 pass / block：
	// 不使用 `match`（会改变后续规则的 state 语义，容易误配）。
	RuleAction string
	// RuleDirection 方向。只做入站控制——出站全部放行，
	// 避免「装了面板把自己网断了」。
	RuleDirection string
	// RuleProtocol 协议。
	RuleProtocol string
)

const (
	ActionPass  RuleAction = "pass"
	ActionBlock RuleAction = "block"

	DirectionIn RuleDirection = "in"

	ProtocolTCP   RuleProtocol = "tcp"
	ProtocolUDP   RuleProtocol = "udp"
	ProtocolICMP6 RuleProtocol = "icmp6"
	ProtocolAny   RuleProtocol = "any"
)

// FirewallRule 是一条 IPv6 入站规则。
//
// 端口表达：PortFrom 为空表示「任意端口」（仅 icmp6/any 允许）。
// PortTo 为空或等于 PortFrom 表示单端口。用两个独立字段而非
// 「端口或范围」字符串，是为了让校验在结构层就完成，避免解析歧义。
type FirewallRule struct {
	ID          int64         `json:"id"`
	Name        string        `json:"name"`
	Description string        `json:"description"`
	Position    int           `json:"position"`
	Action      RuleAction    `json:"action"`
	Direction   RuleDirection `json:"direction"`
	Protocol    RuleProtocol  `json:"protocol"`
	// PortFrom / PortTo：nil 表示不限端口。两者都有时 PortFrom <= PortTo。
	PortFrom   *int   `json:"portFrom"`
	PortTo     *int   `json:"portTo"`
	SourceCIDR string `json:"sourceCidr"`
	// Interface 为空或 "any" 表示不限接口（但生成器仍会加 inet6 限定）。
	Interface string `json:"interface"`
	Enabled   bool   `json:"enabled"`
	// BuiltinTemplate 非空表示这条规则来自内置模板（仅作来源标记，不影响生成）。
	BuiltinTemplate string `json:"builtinTemplate,omitempty"`
}

// RuleSet 是一组规则与其全局设置。
type RuleSet struct {
	Rules []FirewallRule `json:"rules"`
	// DefaultDenyInterfaces 需要实施「默认拒绝入站」的接口白名单。
	// 为空时不做默认拒绝——这是刻意的保守默认：
	// 宁可不生效，也不要让用户在没准备好的情况下把自己锁在外面。
	DefaultDenyInterfaces []string `json:"defaultDenyInterfaces"`
}

// 校验错误的上限，避免一次返回上百条错误淹没界面。
const maxRuleErrors = 20

// Validate 校验单条规则，返回字段级错误。
//
// 校验必须严格：宁可拒绝可疑输入，也不生成一个「看起来对但语义错」的 PF 规则。
func (r *FirewallRule) Validate() error {
	var problems []string
	add := func(format string, args ...any) {
		if len(problems) < maxRuleErrors {
			problems = append(problems, fmt.Sprintf(format, args...))
		}
	}

	name := strings.TrimSpace(r.Name)
	if name == "" {
		add("规则名称不能为空")
	} else if len(name) > 64 {
		add("规则名称不能超过 64 个字符")
	}
	if len(r.Description) > 256 {
		add("备注不能超过 256 个字符")
	}

	if r.Action != ActionPass && r.Action != ActionBlock {
		add("动作必须是 pass 或 block，当前为 %q", r.Action)
	}
	// 只支持入站。若传 out，明确报错而不是静默改成 in——
	// 静默改写会让用户以为规则生效了，实际语义不同。
	if r.Direction != DirectionIn {
		add("仅支持入站规则（direction=in），当前为 %q", r.Direction)
	}
	switch r.Protocol {
	case ProtocolTCP, ProtocolUDP, ProtocolICMP6, ProtocolAny:
	default:
		add("协议必须是 tcp/udp/icmp6/any，当前为 %q", r.Protocol)
	}

	// 端口校验
	hasFrom := r.PortFrom != nil
	hasTo := r.PortTo != nil
	if hasFrom != hasTo && r.Protocol != ProtocolICMP6 {
		// icmp6 不需端口；其它协议要么都给要么都不给。
		add("端口范围必须同时指定起始与结束端口（仅填一端会产生歧义）")
	}
	if r.Protocol == ProtocolICMP6 || r.Protocol == ProtocolAny {
		if hasFrom || hasTo {
			if r.Protocol == ProtocolICMP6 {
				add("icmp6 规则不能指定端口")
			}
		}
	}
	if r.Protocol == ProtocolTCP || r.Protocol == ProtocolUDP {
		if !hasFrom {
			// TCP/UDP 不限端口等价于放行全部端口，风险极高，要求显式声明。
			add("%s 规则必须指定端口", r.Protocol)
		}
	}
	if hasFrom {
		if *r.PortFrom < 1 || *r.PortFrom > 65535 {
			add("起始端口必须在 1-65535 之间，当前为 %d", *r.PortFrom)
		}
	}
	if hasTo {
		if *r.PortTo < 1 || *r.PortTo > 65535 {
			add("结束端口必须在 1-65535 之间，当前为 %d", *r.PortTo)
		}
	}
	if hasFrom && hasTo && *r.PortFrom > *r.PortTo {
		add("起始端口不能大于结束端口（%d > %d）", *r.PortFrom, *r.PortTo)
	}

	// 来源 CIDR
	cidr := strings.TrimSpace(r.SourceCIDR)
	if cidr != "" && !strings.EqualFold(cidr, "any") {
		if _, err := ParseIPv6CIDR(cidr); err != nil {
			add("来源地址必须是 IPv6 CIDR 或 any：%v", err)
		}
	}

	// 接口名
	iface := strings.TrimSpace(r.Interface)
	if iface != "" && !strings.EqualFold(iface, "any") {
		if err := ValidateInterfaceName(iface); err != nil {
			add("%v", err)
		}
	}

	if len(problems) > 0 {
		return NewError(CodeInvalidArgument, "规则 %q 校验失败：%s",
			name, strings.Join(problems, "；"))
	}
	return nil
}

// Validate 校验整个规则集：逐条规则 + 集合级约束。
func (rs *RuleSet) Validate() error {
	if len(rs.Rules) > 500 {
		return NewError(CodeInvalidArgument, "规则数量不能超过 500 条，当前 %d 条", len(rs.Rules))
	}

	var problems []string
	for i := range rs.Rules {
		if err := rs.Rules[i].Validate(); err != nil {
			problems = append(problems, fmt.Sprintf("第 %d 条：%s", i+1, AsError(err).Message))
			if len(problems) >= maxRuleErrors {
				break
			}
		}
	}

	// 默认拒绝接口必须合法且不重复
	seen := map[string]bool{}
	for _, iface := range rs.DefaultDenyInterfaces {
		iface = strings.TrimSpace(iface)
		if err := ValidateInterfaceName(iface); err != nil {
			problems = append(problems, fmt.Sprintf("默认拒绝接口无效：%v", err))
			continue
		}
		if seen[iface] {
			problems = append(problems, fmt.Sprintf("默认拒绝接口重复：%s", iface))
		}
		seen[iface] = true
		if isDangerousInterface(iface) {
			problems = append(problems, fmt.Sprintf(
				"接口 %s 不能用于默认拒绝：会导致本机回环或隧道流量被切断", iface))
		}
	}

	if len(problems) > 0 {
		return NewError(CodeInvalidArgument, "规则集校验失败：%s", strings.Join(problems, "；"))
	}
	return nil
}

// ParseIPv6CIDR 解析并规范化 IPv6 CIDR。
//
// 拒绝 IPv4：本面板只做 IPv6 防火墙，混入 IPv4 会违反
// 「自定义规则不限制 IPv4」这一不变量。
func ParseIPv6CIDR(s string) (*net.IPNet, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil, fmt.Errorf("CIDR 不能为空")
	}
	// 不带掩码的单地址按 /128 处理
	if !strings.Contains(s, "/") {
		ip := net.ParseIP(s)
		if ip == nil {
			return nil, fmt.Errorf("%q 不是合法地址", s)
		}
		if ip.To4() != nil {
			return nil, fmt.Errorf("%q 是 IPv4 地址，本面板只处理 IPv6", s)
		}
		return &net.IPNet{IP: ip.To16(), Mask: net.CIDRMask(128, 128)}, nil
	}

	ip, ipnet, err := net.ParseCIDR(s)
	if err != nil {
		return nil, fmt.Errorf("%q 不是合法 CIDR: %v", s, err)
	}
	if ip.To4() != nil {
		return nil, fmt.Errorf("%q 是 IPv4 CIDR，本面板只处理 IPv6", s)
	}
	ones, bits := ipnet.Mask.Size()
	if bits != 128 {
		return nil, fmt.Errorf("%q 的掩码位数不是 IPv6（%d 位）", s, bits)
	}
	if ones < 16 {
		// 前缀过短等于放行极大量地址，几乎肯定是误操作。
		return nil, fmt.Errorf("%q 的前缀长度过短（/%d），最宽允许 /16", s, ones)
	}
	return ipnet, nil
}

// NormalizeCIDR 把 CIDR 规范化（网络地址 + 规范前缀），
// 便于比较与去重。空串与 any 统一返回 "any"。
func NormalizeCIDR(s string) (string, error) {
	s = strings.TrimSpace(s)
	if s == "" || strings.EqualFold(s, "any") {
		return "any", nil
	}
	ipnet, err := ParseIPv6CIDR(s)
	if err != nil {
		return "", err
	}
	ones, _ := ipnet.Mask.Size()
	return fmt.Sprintf("%s/%d", ipnet.IP.String(), ones), nil
}

// ValidateInterfaceName 校验网络接口名。
//
// macOS 接口名形如 en0 / en5 / utun3 / bridge0 / lo0 / awdl0。
// 这里只允许小写字母与数字，长度 <= 15（BSD IFNAMSIZ-1），
// 从根上排除 shell 元字符与路径分隔符进入 PF 规则文本。
func ValidateInterfaceName(name string) error {
	name = strings.TrimSpace(name)
	if name == "" {
		return fmt.Errorf("接口名不能为空")
	}
	if len(name) > 15 {
		return fmt.Errorf("接口名 %q 过长（最多 15 个字符）", name)
	}
	for _, c := range name {
		if (c < 'a' || c > 'z') && (c < '0' || c > '9') {
			return fmt.Errorf("接口名 %q 含非法字符 %q（只允许小写字母与数字）", name, c)
		}
	}
	return nil
}

// isDangerousInterface 判断接口是否绝不允许用于「默认拒绝」。
//
// lo0 是回环——拒绝它会立刻切断本机自身访问（PLAN.md 不变量 2）。
// utun*/ipsec* 是 VPN 隧道，拒绝它们可能切断用户的远程管理通道。
// 这两类都由生成器强制放行，因此也不应出现在默认拒绝列表里。
func isDangerousInterface(name string) bool {
	name = strings.ToLower(strings.TrimSpace(name))
	switch {
	case name == "lo0", name == "lo":
		return true
	case strings.HasPrefix(name, "utun"):
		return true
	case strings.HasPrefix(name, "ipsec"):
		return true
	case strings.HasPrefix(name, "gif"):
		return true
	case strings.HasPrefix(name, "stf"):
		return true
	}
	return false
}

// IsDangerousInterface 供其它包判断接口是否属于必须强制放行的类别。
func IsDangerousInterface(name string) bool { return isDangerousInterface(name) }

// SortByPosition 返回按 position 升序排列的副本。
// 相同 position 时按 ID 升序，保证生成结果稳定（便于 diff 与测试）。
func SortByPosition(rules []FirewallRule) []FirewallRule {
	out := make([]FirewallRule, len(rules))
	copy(out, rules)
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Position != out[j].Position {
			return out[i].Position < out[j].Position
		}
		return out[i].ID < out[j].ID
	})
	return out
}

// FormatPortRange 返回人类可读的端口描述，用于日志与审计。
func (r *FirewallRule) FormatPortRange() string {
	if r.PortFrom == nil {
		return ""
	}
	if r.PortTo == nil || *r.PortTo == *r.PortFrom {
		return strconv.Itoa(*r.PortFrom)
	}
	return fmt.Sprintf("%d-%d", *r.PortFrom, *r.PortTo)
}

// IntPtr 便于构造端口指针，主要供测试与模板使用。
func IntPtr(v int) *int { return &v }
