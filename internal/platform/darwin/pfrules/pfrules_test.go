package pfrules

import (
	"strings"
	"testing"

	"github.com/lancelot-lewis/MacPanel/internal/model"
)

// ptr 是测试用的小工具。
func ptr(v int) *int { return &v }

// rule 构造一条合法规则，测试中按需覆盖字段。
func rule(name string, mutate func(*model.FirewallRule)) model.FirewallRule {
	r := model.FirewallRule{
		ID:         1,
		Name:       name,
		Action:     model.ActionPass,
		Direction:  model.DirectionIn,
		Protocol:   model.ProtocolTCP,
		PortFrom:   ptr(443),
		PortTo:     ptr(443),
		SourceCIDR: "any",
		Interface:  "any",
		Enabled:    true,
	}
	if mutate != nil {
		mutate(&r)
	}
	return r
}

// TestGenerateContainsMandatoryRules 验证强制规则始终存在。
func TestGenerateContainsMandatoryRules(t *testing.T) {
	gen, err := Generate(model.RuleSet{}, "com.test.ipv6")
	if err != nil {
		t.Fatalf("生成失败: %v", err)
	}

	mustContain := []string{
		"pass quick on lo0 all",
		"pass in quick inet6 from ::1 to ::1",
		"pass out quick inet6 all keep state",
		"proto ipv6-icmp",
		"icmp6-type",
	}
	for _, want := range mustContain {
		if !strings.Contains(gen.Content, want) {
			t.Errorf("生成结果缺少必需规则: %q\n---\n%s", want, gen.Content)
		}
	}

	// 强制规则清单必须完整上报，前端据此如实告知用户
	if len(gen.Enforced) < 4 {
		t.Errorf("强制规则应至少 4 条，实际 %d", len(gen.Enforced))
	}
}

// TestGenerateNeverEmitsGlobalDirectives 验证绝不生成全局配置指令。
//
// 这是不变量 8 的文本保证：set/scrub/nat/rdr 会干扰其它程序。
func TestGenerateNeverEmitsGlobalDirectives(t *testing.T) {
	rs := model.RuleSet{
		Rules:                 []model.FirewallRule{rule("Caddy", nil)},
		DefaultDenyInterfaces: []string{"en0"},
	}
	gen, err := Generate(rs, "com.test.ipv6")
	if err != nil {
		t.Fatalf("生成失败: %v", err)
	}

	forbidden := []string{
		"set skip on",
		"set limit",
		"set timeout",
		"scrub ",
		"\nnat ",
		"\nrdr ",
		"binat ",
		"\ntable ",
		"include ",
		"load anchor",
		"pfctl -d",
	}
	for _, bad := range forbidden {
		if strings.Contains(gen.Content, bad) {
			t.Errorf("生成结果不得包含 %q\n---\n%s", bad, gen.Content)
		}
	}
}

// TestAllRulesAreInet6 验证每条 pass/block 语句都限定 inet6（不变量 1）。
func TestAllRulesAreInet6(t *testing.T) {
	rs := model.RuleSet{
		Rules: []model.FirewallRule{
			rule("TCP", nil),
			rule("UDP", func(r *model.FirewallRule) {
				r.Protocol = model.ProtocolUDP
				r.PortFrom = ptr(2222)
				r.PortTo = ptr(2222)
			}),
			rule("ICMP", func(r *model.FirewallRule) {
				r.Protocol = model.ProtocolICMP6
				r.PortFrom = nil
				r.PortTo = nil
			}),
			rule("CIDR", func(r *model.FirewallRule) {
				r.SourceCIDR = "2001:db8::/32"
			}),
		},
		DefaultDenyInterfaces: []string{"en0"},
	}
	gen, err := Generate(rs, "com.test.ipv6")
	if err != nil {
		t.Fatalf("生成失败: %v", err)
	}

	for i, raw := range strings.Split(gen.Content, "\n") {
		line := strings.TrimSpace(raw)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		// lo0 放行同时覆盖 v4/v6，是唯一例外（且它是纯放行，不会限制 IPv4）
		if strings.Contains(line, "on lo0") {
			continue
		}
		if !strings.Contains(line, "inet6") {
			t.Errorf("第 %d 行未限定 inet6: %q", i+1, line)
		}
	}
}

// TestVerifyPassesOnGeneratedContent 验证自己生成的规则能通过自己的校验。
func TestVerifyPassesOnGeneratedContent(t *testing.T) {
	cases := []model.RuleSet{
		{}, // 空规则集
		{Rules: []model.FirewallRule{rule("仅放行", nil)}}, // 只有放行
		{
			Rules:                 []model.FirewallRule{rule("放行+拒绝", nil)},
			DefaultDenyInterfaces: []string{"en0"},
		},
		{
			Rules: []model.FirewallRule{
				rule("多规则1", nil),
				rule("多规则2", func(r *model.FirewallRule) {
					r.ID = 2
					r.Action = model.ActionBlock
					r.PortFrom = ptr(23)
					r.PortTo = ptr(23)
				}),
			},
			DefaultDenyInterfaces: []string{"en0", "en5"},
		},
	}

	for i, rs := range cases {
		gen, err := Generate(rs, "com.test.ipv6")
		if err != nil {
			t.Fatalf("用例 %d 生成失败: %v", i, err)
		}
		v := Verify(gen.Content, rs.DefaultDenyInterfaces)
		if !v.OK {
			t.Errorf("用例 %d 校验未通过：语法 %v 不变量 %v\n---\n%s",
				i, v.SyntaxErrors, v.InvariantViolations, gen.Content)
		}
	}
}

// TestVerifyDetectsForbiddenDirectives 验证校验器能拒绝全局指令。
func TestVerifyDetectsForbiddenDirectives(t *testing.T) {
	cases := []struct{ name, content string }{
		{"set skip", "set skip on lo0\npass quick on lo0 all"},
		{"scrub", "scrub in all\npass quick on lo0 all"},
		{"nat", "nat on en0 from any to any\npass quick on lo0 all"},
		{"rdr", "rdr pass on en0 proto tcp to port 80\npass quick on lo0 all"},
		{"include", "include \"/etc/pf.anchors/other\"\npass quick on lo0 all"},
	}

	for _, c := range cases {
		v := Verify(c.content, nil)
		if v.OK {
			t.Errorf("%s 应被拒绝，实际通过", c.name)
		}
		if len(v.SyntaxErrors) == 0 {
			t.Errorf("%s 应产生语法错误", c.name)
		}
	}
}

// TestVerifyDetectsMissingLoopback 验证缺少回环放行会被发现。
func TestVerifyDetectsMissingLoopback(t *testing.T) {
	// 只有出站与 icmp，没有 lo0
	content := `pass out quick inet6 all keep state
pass in quick inet6 proto ipv6-icmp from any to any icmp6-type { 128 }`
	v := Verify(content, nil)
	if v.OK {
		t.Fatal("缺少 lo0 放行应校验失败")
	}
	var found bool
	for _, iv := range v.InvariantViolations {
		if iv.Key == "missing-loopback-pass" {
			found = true
			if iv.Invariant != 2 {
				t.Errorf("应关联不变量 2，实际 %d", iv.Invariant)
			}
		}
	}
	if !found {
		t.Errorf("应报告 missing-loopback-pass，实际: %+v", v.InvariantViolations)
	}
}

// TestVerifyDetectsMissingICMPv6 验证缺少 ICMPv6 会被发现（不变量 4）。
func TestVerifyDetectsMissingICMPv6(t *testing.T) {
	content := `pass quick on lo0 all
pass in quick inet6 from ::1 to ::1
pass out quick inet6 all keep state`
	v := Verify(content, nil)
	var found bool
	for _, iv := range v.InvariantViolations {
		if iv.Key == "missing-icmpv6" {
			found = true
			if iv.Invariant != 4 {
				t.Errorf("应关联不变量 4，实际 %d", iv.Invariant)
			}
		}
	}
	if !found {
		t.Errorf("应报告 missing-icmpv6，实际: %+v", v.InvariantViolations)
	}
}

// TestVerifyDetectsNonInet6Rule 验证未限定 inet6 的语句被识别（不变量 1）。
func TestVerifyDetectsNonInet6Rule(t *testing.T) {
	content := `pass quick on lo0 all
pass in quick inet6 from ::1 to ::1
pass out quick inet6 all keep state
pass in quick inet6 proto ipv6-icmp from any to any icmp6-type { 128 }
pass in quick proto tcp from any to any port 8080`
	v := Verify(content, nil)
	if v.OK {
		t.Fatal("未限定 inet6 的规则应被识别")
	}
	var found bool
	for _, iv := range v.InvariantViolations {
		if iv.Key == "may-affect-ipv4" {
			found = true
			if iv.Invariant != 1 {
				t.Errorf("应关联不变量 1，实际 %d", iv.Invariant)
			}
		}
	}
	if !found {
		t.Errorf("应报告 may-affect-ipv4，实际: %+v", v.InvariantViolations)
	}
}

// TestGenerateRejectsDangerousDefaultDeny 验证危险接口不能用于默认拒绝。
func TestGenerateRejectsDangerousDefaultDeny(t *testing.T) {
	for _, iface := range []string{"lo0", "utun3", "ipsec0", "gif0"} {
		rs := model.RuleSet{DefaultDenyInterfaces: []string{iface}}
		if _, err := Generate(rs, "com.test.ipv6"); err == nil {
			t.Errorf("接口 %s 用于默认拒绝应被拒绝", iface)
		}
	}
}

// TestGenerateReportsSkippedRules 验证禁用规则被记录而非静默丢弃。
func TestGenerateReportsSkippedRules(t *testing.T) {
	rs := model.RuleSet{
		Rules: []model.FirewallRule{
			rule("启用的", nil),
			rule("禁用的", func(r *model.FirewallRule) {
				r.ID = 2
				r.Enabled = false
			}),
		},
	}
	gen, err := Generate(rs, "com.test.ipv6")
	if err != nil {
		t.Fatalf("生成失败: %v", err)
	}

	if gen.RuleCount != 1 {
		t.Errorf("生效规则数应为 1，实际 %d", gen.RuleCount)
	}
	if len(gen.Skipped) != 1 {
		t.Fatalf("应记录 1 条跳过，实际 %d", len(gen.Skipped))
	}
	if gen.Skipped[0].Name != "禁用的" {
		t.Errorf("跳过的规则名不符: %s", gen.Skipped[0].Name)
	}
	if gen.Skipped[0].Reason == "" {
		t.Error("跳过原因不能为空")
	}
}

// TestCommentInjectionPrevented 验证规则名中的换行不会破坏文件结构。
//
// 这是真实的安全问题：如果名称含 \n 且直接写入注释，
// 攻击者可以在注释位置插入任意 PF 规则。
func TestCommentInjectionPrevented(t *testing.T) {
	evil := "正常名称\npass in quick inet6 from any to any port 22"
	rs := model.RuleSet{
		Rules: []model.FirewallRule{rule(evil, nil)},
	}
	gen, err := Generate(rs, "com.test.ipv6")
	if err != nil {
		t.Fatalf("生成失败: %v", err)
	}

	// 注入的规则不应以独立行出现
	for _, raw := range strings.Split(gen.Content, "\n") {
		line := strings.TrimSpace(raw)
		if line == "pass in quick inet6 from any to any port 22" {
			t.Fatalf("注释注入成功，生成了额外规则:\n%s", gen.Content)
		}
	}

	// 名称应被压成单行注释
	if !strings.Contains(gen.Content, "正常名称 pass in quick inet6") {
		t.Errorf("名称应被压成单行，实际内容:\n%s", gen.Content)
	}
}

// TestPortRangeRendering 验证端口范围用 PF 的 a:b 语法。
func TestPortRangeRendering(t *testing.T) {
	rs := model.RuleSet{
		Rules: []model.FirewallRule{
			rule("范围", func(r *model.FirewallRule) {
				r.PortFrom = ptr(11010)
				r.PortTo = ptr(11013)
			}),
		},
	}
	gen, err := Generate(rs, "com.test.ipv6")
	if err != nil {
		t.Fatalf("生成失败: %v", err)
	}
	if !strings.Contains(gen.Content, "port 11010:11013") {
		t.Errorf("端口范围应渲染为 11010:11013，实际:\n%s", gen.Content)
	}
}

// TestDefaultDenyOnlyWhenConfigured 验证未配置接口时不生成默认拒绝。
func TestDefaultDenyOnlyWhenConfigured(t *testing.T) {
	gen, err := Generate(model.RuleSet{Rules: []model.FirewallRule{rule("放行", nil)}}, "com.test.ipv6")
	if err != nil {
		t.Fatalf("生成失败: %v", err)
	}
	if strings.Contains(gen.Content, "block in quick inet6 on") {
		t.Error("未配置默认拒绝接口时不应生成 block 规则")
	}
	if !strings.Contains(gen.Content, "不阻断任何入站流量") {
		t.Error("应明确说明本次未阻断入站流量")
	}
}

// TestGenerateValidatesInput 验证非法规则集在生成前就被拒绝。
func TestGenerateValidatesInput(t *testing.T) {
	cases := []struct {
		name string
		rs   model.RuleSet
	}{
		{"端口倒置", model.RuleSet{Rules: []model.FirewallRule{
			rule("坏端口", func(r *model.FirewallRule) { r.PortFrom = ptr(500); r.PortTo = ptr(100) }),
		}}},
		{"IPv4 CIDR", model.RuleSet{Rules: []model.FirewallRule{
			rule("v4", func(r *model.FirewallRule) { r.SourceCIDR = "192.168.1.0/24" }),
		}}},
		{"非法接口名", model.RuleSet{Rules: []model.FirewallRule{
			rule("坏接口", func(r *model.FirewallRule) { r.Interface = "en0; rm -rf /" }),
		}}},
		{"TCP 缺端口", model.RuleSet{Rules: []model.FirewallRule{
			rule("无端口", func(r *model.FirewallRule) { r.PortFrom = nil; r.PortTo = nil }),
		}}},
	}

	for _, c := range cases {
		if _, err := Generate(c.rs, "com.test.ipv6"); err == nil {
			t.Errorf("%s 应在生成前被拒绝", c.name)
		}
	}
}

// TestTemplatesAreValid 验证所有内置模板生成的规则集都能通过校验。
func TestTemplatesAreValid(t *testing.T) {
	for _, tpl := range BuiltinTemplates() {
		rs := model.RuleSet{Rules: tpl.Rules}
		gen, err := Generate(rs, "com.test.ipv6")
		if err != nil {
			t.Errorf("模板 %s 生成失败: %v", tpl.Key, err)
			continue
		}
		v := Verify(gen.Content, nil)
		if !v.OK {
			t.Errorf("模板 %s 校验失败：语法 %v 不变量 %v", tpl.Key, v.SyntaxErrors, v.InvariantViolations)
		}
	}
}

// TestTemplatesHaveNote 验证模板都带有必须核实的提示。
func TestTemplatesHaveNote(t *testing.T) {
	for _, tpl := range BuiltinTemplates() {
		if strings.TrimSpace(tpl.Note) == "" {
			t.Errorf("模板 %s 缺少 Note：端口必须让用户与实际监听核对", tpl.Key)
		}
		if tpl.DisplayName == "" || tpl.Description == "" {
			t.Errorf("模板 %s 缺少展示信息", tpl.Key)
		}
		if len(tpl.Rules) == 0 {
			t.Errorf("模板 %s 没有规则", tpl.Key)
		}
	}
}

// TestTemplatesAvoidUnintendedWideRanges 验证模板不会意外放行大量端口。
//
// 这是一个真实犯过的错误：Caddy 模板最初写成 port 80:443，
// 而 PF 的 a:b 是「端口范围」，会连 81-442 全部放行。
// 服务只需要 80 与 443 两个点，必须拆成两条规则。
func TestTemplatesAvoidUnintendedWideRanges(t *testing.T) {
	// 端口区间跨度超过 10 的，必须是有明确业务含义的连续区间（如 EasyTier 11010-11013）。
	const maxSaneSpan = 10
	// 允许的宽区间白名单（key: 规则名片段）
	allowed := map[string]bool{
		"easytier": true, // 11010-11013，组网服务的连续端口段
	}

	for _, tpl := range BuiltinTemplates() {
		for _, r := range tpl.Rules {
			if r.PortFrom == nil || r.PortTo == nil {
				continue
			}
			span := *r.PortTo - *r.PortFrom
			if span > maxSaneSpan && !allowed[tpl.Key] {
				t.Errorf("模板 %s 的规则 %q 端口跨度 %d（%d-%d）过大："+
					"PF 的 a:b 会放行区间内全部端口，请拆成独立规则",
					tpl.Key, r.Name, span, *r.PortFrom, *r.PortTo)
			}
		}
	}
}

// TestCaddyTemplateUsesExactPorts 专门锁定 Caddy 端口，防止回归。
func TestCaddyTemplateUsesExactPorts(t *testing.T) {
	tpl, ok := FindTemplate("caddy")
	if !ok {
		t.Fatal("应找到 caddy 模板")
	}

	ports := map[int]bool{}
	for _, r := range tpl.Rules {
		if r.PortFrom == nil {
			continue
		}
		if r.PortTo == nil || *r.PortTo != *r.PortFrom {
			t.Errorf("Caddy 规则 %q 使用了端口范围 %d-%v，应拆成单端口规则",
				r.Name, *r.PortFrom, r.PortTo)
		}
		ports[*r.PortFrom] = true
	}

	for _, want := range []int{80, 443} {
		if !ports[want] {
			t.Errorf("Caddy 模板应包含端口 %d", want)
		}
	}
}

func TestFindTemplate(t *testing.T) {
	tpl, ok := FindTemplate("caddy")
	if !ok {
		t.Fatal("应找到 caddy 模板")
	}
	if tpl.DisplayName == "" {
		t.Error("模板应有名称")
	}
	if _, ok := FindTemplate("不存在"); ok {
		t.Error("不存在的模板不应返回 true")
	}
}

// TestFindTemplate 验证按 key 查找。
