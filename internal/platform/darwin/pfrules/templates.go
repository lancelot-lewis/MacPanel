package pfrules

import "github.com/lancelot-lewis/MacPanel/internal/model"

// Template 是一个内置服务模板。
//
// 模板只是「起点」而非「真相」：端口必须与服务的实际监听配置核对。
// 因此每个模板都带 Note 提醒用户验证，前端在套用后要求用户确认。
type Template struct {
	Key         string `json:"key"`
	DisplayName string `json:"displayName"`
	Description string `json:"description"`
	// Note 提示用户必须核实的点。空字符串表示无需额外确认。
	Note  string               `json:"note,omitempty"`
	Rules []model.FirewallRule `json:"rules"`
}

// intPtr 包内小工具，避免反复取地址。
func intPtr(v int) *int { return &v }

// BuiltinTemplates 返回内置模板。
//
// 端口来源见 PLAN.md 5.2：
//
//	Caddy     TCP 80/443、UDP 443（HTTP/3）
//	sish      TCP/UDP 2222
//	Tailscale TCP/UDP 3478、40000
//	EasyTier  TCP/UDP 11010-11013
//
// 这些都是「原配置」的默认值。用户若改过服务端口，必须自行调整，
// 因此每个模板的 Note 都要求核对。
func BuiltinTemplates() []Template {
	return []Template{
		{
			Key:         "caddy",
			DisplayName: "Caddy Web 服务器",
			Description: "对外提供 HTTP/HTTPS 服务，含 HTTP/3（QUIC over UDP 443）",
			Note:        "若修改过 Caddyfile 中的监听端口或关闭了 HTTP/3，请相应调整",
			Rules: []model.FirewallRule{
				{
					// 必须拆成两条而不能写成 80:443 ——
					// PF 的 a:b 表示端口范围，会连 81-442 一起放行（过度授权）。
					Name:            "Caddy HTTP",
					Description:     "HTTP 访问入口（80）",
					Action:          model.ActionPass,
					Direction:       model.DirectionIn,
					Protocol:        model.ProtocolTCP,
					PortFrom:        intPtr(80),
					PortTo:          intPtr(80),
					SourceCIDR:      "any",
					Interface:       "any",
					Enabled:         true,
					BuiltinTemplate: "caddy",
				},
				{
					Name:            "Caddy HTTPS",
					Description:     "HTTPS 访问入口（443）",
					Action:          model.ActionPass,
					Direction:       model.DirectionIn,
					Protocol:        model.ProtocolTCP,
					PortFrom:        intPtr(443),
					PortTo:          intPtr(443),
					SourceCIDR:      "any",
					Interface:       "any",
					Enabled:         true,
					BuiltinTemplate: "caddy",
				},
				{
					Name:            "Caddy HTTP/3",
					Description:     "QUIC（UDP 443）",
					Action:          model.ActionPass,
					Direction:       model.DirectionIn,
					Protocol:        model.ProtocolUDP,
					PortFrom:        intPtr(443),
					PortTo:          intPtr(443),
					SourceCIDR:      "any",
					Interface:       "any",
					Enabled:         true,
					BuiltinTemplate: "caddy",
				},
			},
		},
		{
			Key:         "sish",
			DisplayName: "sish 反向隧道",
			Description: "HTTP(S) 反向隧道服务，默认监听 2222",
			Note:        "若 sish 的 --ssh-address 使用了其它端口，请修改",
			Rules: []model.FirewallRule{
				{
					Name:            "sish 隧道",
					Action:          model.ActionPass,
					Direction:       model.DirectionIn,
					Protocol:        model.ProtocolTCP,
					PortFrom:        intPtr(2222),
					PortTo:          intPtr(2222),
					SourceCIDR:      "any",
					Interface:       "any",
					Enabled:         true,
					BuiltinTemplate: "sish",
				},
				{
					Name:            "sish 隧道（UDP）",
					Action:          model.ActionPass,
					Direction:       model.DirectionIn,
					Protocol:        model.ProtocolUDP,
					PortFrom:        intPtr(2222),
					PortTo:          intPtr(2222),
					SourceCIDR:      "any",
					Interface:       "any",
					Enabled:         true,
					BuiltinTemplate: "sish",
				},
			},
		},
		{
			Key:         "tailscale",
			DisplayName: "Tailscale",
			Description: "WireGuard 与中继所需的 UDP 端口",
			Note: "Tailscale 通常通过出站连接打洞，这些入站端口主要影响直连成功率；" +
				"若不为 Tailscale 配置 DERPMap 可只放行 41641",
			Rules: []model.FirewallRule{
				{
					Name:            "Tailscale 中继",
					Action:          model.ActionPass,
					Direction:       model.DirectionIn,
					Protocol:        model.ProtocolUDP,
					PortFrom:        intPtr(3478),
					PortTo:          intPtr(3478),
					SourceCIDR:      "any",
					Interface:       "any",
					Enabled:         true,
					BuiltinTemplate: "tailscale",
				},
				{
					Name:            "Tailscale 直连",
					Action:          model.ActionPass,
					Direction:       model.DirectionIn,
					Protocol:        model.ProtocolUDP,
					PortFrom:        intPtr(40000),
					PortTo:          intPtr(40000),
					SourceCIDR:      "any",
					Interface:       "any",
					Enabled:         true,
					BuiltinTemplate: "tailscale",
				},
			},
		},
		{
			Key:         "easytier",
			DisplayName: "EasyTier",
			Description: "组网服务默认使用的端口区间",
			Note:        "端口区间较大（11010-11013），若只使用部分节点请缩小范围",
			Rules: []model.FirewallRule{
				{
					Name:            "EasyTier TCP",
					Action:          model.ActionPass,
					Direction:       model.DirectionIn,
					Protocol:        model.ProtocolTCP,
					PortFrom:        intPtr(11010),
					PortTo:          intPtr(11013),
					SourceCIDR:      "any",
					Interface:       "any",
					Enabled:         true,
					BuiltinTemplate: "easytier",
				},
				{
					Name:            "EasyTier UDP",
					Action:          model.ActionPass,
					Direction:       model.DirectionIn,
					Protocol:        model.ProtocolUDP,
					PortFrom:        intPtr(11010),
					PortTo:          intPtr(11013),
					SourceCIDR:      "any",
					Interface:       "any",
					Enabled:         true,
					BuiltinTemplate: "easytier",
				},
			},
		},
		{
			Key:         "ssh-restricted",
			DisplayName: "SSH（限局域网）",
			Description: "只允许本地网段访问 SSH，比全网开放更安全",
			Note: "示例使用 2409::/16，请改为你实际的上游前缀；" +
				"注意这会限制非本地网段的 SSH 访问",
			Rules: []model.FirewallRule{
				{
					Name:            "SSH（限制来源）",
					Action:          model.ActionPass,
					Direction:       model.DirectionIn,
					Protocol:        model.ProtocolTCP,
					PortFrom:        intPtr(22),
					PortTo:          intPtr(22),
					SourceCIDR:      "2409::/16",
					Interface:       "any",
					Enabled:         true,
					BuiltinTemplate: "ssh-restricted",
				},
			},
		},
	}
}

// FindTemplate 按 key 查找模板。
func FindTemplate(key string) (Template, bool) {
	for _, t := range BuiltinTemplates() {
		if t.Key == key {
			return t, true
		}
	}
	return Template{}, false
}
