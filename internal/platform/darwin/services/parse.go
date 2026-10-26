// Package services 扫描与分类 macOS 的 launchd 服务。
//
// 数据来源（实测 macOS 27）：
//   - `launchctl list` 在 macOS 27 上对普通用户返回空且退出码 1，
//     不能作为数据源；因此改用 `launchctl print system`，
//     它输出 services 段，每行形如 `<PID> <标志> <label>`。
//   - plist 文件路径用于「配置来源」展示与判断域归属。
//
// 只读优先：本包只做扫描与分类，不执行任何启停操作。
// 启停由上层在「受管白名单 + 安全检查」之后才允许。
package services

import (
	"strconv"
	"strings"
)

// Domain 是 launchd 的域。
type Domain string

const (
	// DomainSystem 是系统域（LaunchDaemons），开机即启动，不依赖用户登录。
	DomainSystem Domain = "system"
	// DomainGUI 是当前用户的图形会话域（LaunchAgents）。
	DomainGUI Domain = "gui"
)

// Kind 是服务的归属分类，决定是否允许操作。
type Kind string

const (
	// KindSystemCritical 是系统关键服务（com.apple.* 等），永远只读。
	//
	// 停止这类服务的后果不可预测：可能是网络栈、可能是文件系统守护进程。
	// 本应用不提供任何操作入口。
	KindSystemCritical Kind = "system_critical"

	// KindSelf 是本应用自己的服务。
	//
	// 永远只读。停掉 Agent 等于切断面板的唯一数据来源；
	// 停掉 Server 等于让当前页面失联。用户要停它们应该走 CLI 或 launchctl，
	// 而不是在「可能由这个服务提供访问」的场景下点按钮。
	KindSelf Kind = "self"

	// KindThirdParty 是第三方或用户自建服务。默认只读，
	// 只有显式加入受管白名单后才允许启停。
	KindThirdParty Kind = "third_party"
)

// SelfPrefix 是本应用 launchd 标签的前缀。
const SelfPrefix = "com.homelab.manager."

// applePrefixes 是系统关键服务的标签前缀。
//
// 除 com.apple. 外还包含几个 Apple 内部使用的前缀：早期系统组件
// 有 com.openssh.sshd（这是 Apple 提供的 sshd）、com.vix.cron 等。
var applePrefixes = []string{
	"com.apple.",
	"com.openssh.",
	"com.vix.",
	"com.google.keystone.", // 更新代理，属系统级基础设施
}

// Classify 按标签判断服务归属。
func Classify(label string) Kind {
	if strings.HasPrefix(label, SelfPrefix) {
		return KindSelf
	}
	for _, p := range applePrefixes {
		if strings.HasPrefix(label, p) {
			return KindSystemCritical
		}
	}
	return KindThirdParty
}

// Service 是一个扫描到的服务。
type Service struct {
	Label string `json:"label"`
	// DisplayName 是 plist 里的 Label 或推导出的可读名（尽力而为）。
	DisplayName string `json:"displayName,omitempty"`
	Domain      Domain `json:"domain"`
	Kind        Kind   `json:"kind"`

	// PID 为 0 表示当前未运行。
	PID int `json:"pid"`
	// Running 是 PID > 0 的便捷判断。
	Running bool `json:"running"`

	// PlistPath 是配置来源。为空表示没在标准目录找到
	// （可能是内嵌 plist 或已被删除但仍加载）。
	PlistPath string `json:"plistPath,omitempty"`

	// Managed 表示在受管白名单中且允许控制。
	Managed bool `json:"managed"`
	// Controllable 表示当前是否允许启停（受管 + 非关键 + 非自身）。
	Controllable bool `json:"controllable"`
	// BlockedReason 说明为什么不可操作。
	BlockedReason string `json:"blockedReason,omitempty"`

	// LastExit 是上次退出状态（解析自 print 输出的第二列）。
	// 空字符串表示未知。
	LastExit string `json:"lastExit,omitempty"`
}

// ParsePrintServices 解析 `launchctl print <domain>` 输出中的 services 段。
//
// 输出形如：
//
//	services = {
//		       0      - 	com.apple.backgroundassets.managed.relay.service
//		   58793   (pe) 	com.apple.lskdd
//	}
//
// 只依赖「段内每行最后一列是标签、第一列是 PID」这个结构，
// 不假定中间的标志列存在或是什么内容——不同 macOS 版本这里会变，
// 但首尾两列一直稳定。
func ParsePrintServices(output string) []Service {
	lines := strings.Split(output, "\n")

	var (
		inServices bool
		out        []Service
	)

	for _, line := range lines {
		trimmed := strings.TrimSpace(line)

		if !inServices {
			// 段起始：`services = {`
			if strings.HasPrefix(trimmed, "services = {") {
				inServices = true
			}
			continue
		}

		// 段结束：缩进回到只有一层（"\t}"）。
		// 用 TrimSpace 后等于 "}" 判断，避免依赖 tab 宽度。
		if trimmed == "}" {
			break
		}
		if trimmed == "" {
			continue
		}

		svc, ok := parseServiceLine(trimmed)
		if !ok {
			continue
		}
		out = append(out, svc)
	}

	return out
}

// parseServiceLine 解析 services 段中的一行。
func parseServiceLine(line string) (Service, bool) {
	// 标签里的字符集是受限的（字母、数字、点、连字符），
	// 因此按空白拆分后最后一列即标签。
	fields := strings.Fields(line)
	if len(fields) < 2 {
		return Service{}, false
	}

	label := fields[len(fields)-1]
	if !looksLikeLabel(label) {
		return Service{}, false
	}

	pid, err := strconv.Atoi(fields[0])
	if err != nil {
		// 首列不是数字说明这不是服务行（可能是其它段的内容混进来）。
		return Service{}, false
	}

	svc := Service{
		Label:   label,
		PID:     pid,
		Running: pid > 0,
		Kind:    Classify(label),
	}
	// 中间的标志列（如 "-"、"(pe)"、"0"）作为上次退出状态参考。
	if len(fields) >= 3 {
		svc.LastExit = strings.Join(fields[1:len(fields)-1], " ")
	}
	return svc, true
}

// looksLikeLabel 做一次宽松的标签格式检查。
//
// 注意：**不能要求标签包含点**。实测系统里有 `ddns-go` 这类
// 完全合法的无点标签（用户自建服务的常见命名）。
// 早期版本加了「必须含点」的条件，导致这类服务在列表里凭空消失。
//
// 因此只做两件事：
//  1. 字符集检查（排除含空格、斜杠等不可能出现在标签里的字符）；
//  2. 至少要有一个字母（排除纯数字、纯符号的噪声行）。
func looksLikeLabel(s string) bool {
	if s == "" || len(s) > 256 {
		return false
	}
	// 不以数字开头：PID 已单独解析，剩下的首列为数字几乎肯定是噪声。
	if s[0] >= '0' && s[0] <= '9' {
		return false
	}

	hasLetter := false
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z':
			hasLetter = true
		case r >= '0' && r <= '9', r == '.', r == '-', r == '_':
			// 允许
		default:
			return false
		}
	}
	return hasLetter
}

// PlistCandidates 返回某个标签可能的 plist 路径（按优先级）。
//
// 顺序即优先级：先看用户/管理员安装的位置，再看系统自带。
// 同名标签出现在多个位置时，先被 launchd 加载的那个才生效——
// 展示时把首选路径放前面，与「配置来源」的直觉一致。
func PlistCandidates(label string, domain Domain) []string {
	const (
		daemonsLocal  = "/Library/LaunchDaemons/"
		daemonsSystem = "/System/Library/LaunchDaemons/"
		agentsLocal   = "/Library/LaunchAgents/"
	)

	if domain == DomainSystem {
		return []string{
			daemonsLocal + label + ".plist",
			daemonsSystem + label + ".plist",
		}
	}
	return []string{
		agentsLocal + label + ".plist",
		daemonsLocal + label + ".plist",
	}
}

// ExplainBlock 生成不可操作的原因说明。
//
// 三类拦截各有不同原因，界面上要分开表达：
// 「系统关键服务」是设计上的禁止，「未加入受管」是可解除的限制，
// 「本应用自身」则是防止把自己关在门外。
func ExplainBlock(kind Kind, managed bool) string {
	switch kind {
	case KindSystemCritical:
		return "系统关键服务，本应用不提供操作（避免破坏系统功能）"
	case KindSelf:
		return "本应用自身的服务，停止后管理面板会失联"
	default:
		if !managed {
			return "未加入受管列表，默认只读（可在设置中显式授权）"
		}
		return ""
	}
}

// IsControllable 判断在给定分类与受管状态下是否允许启停。
func IsControllable(kind Kind, managed bool) bool {
	return kind == KindThirdParty && managed
}
