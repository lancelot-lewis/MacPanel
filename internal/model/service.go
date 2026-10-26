package model

import (
	"fmt"
	"strings"
	"time"
)

// ManagedService 是「允许通过面板启停」的服务登记记录。
//
// 为什么需要单独一张表而不是靠「服务是不是第三方」推断：
// 启停第三方 daemon 是有风险的操作（它可能是用户唯一的管理通道，
// 比如 sshd、远程桌面、VPN）。默认只读 + 显式登记，才能保证
// 用户是「知道自己在做什么」的情况下授权。
type ManagedService struct {
	ID    int64  `json:"id"`
	Label string `json:"label"`
	// Domain 只允许 system。用户域（gui/<uid>）属于交互会话，
	// 本应用的启停能力不覆盖它。
	Domain      string `json:"domain"`
	PlistPath   string `json:"plistPath"`
	DisplayName string `json:"displayName,omitempty"`
	// AllowControl 为 false 时只展示不操作（用于「先登记观察，再授权」）。
	AllowControl bool      `json:"allowControl"`
	CreatedAt    time.Time `json:"createdAt"`
	UpdatedAt    time.Time `json:"updatedAt"`
}

// 允许登记的 plist 目录。
//
// 只接受这两个前缀：plist 路径会被用来 bootstrap 服务，
// 而 bootstrap 任意 plist 等于以 root 执行其中指定的程序。
// 因此路径来源必须受限，不接受调用方传入任意路径。
var allowedPlistDirs = []string{
	"/Library/LaunchDaemons/",
	"/System/Library/LaunchDaemons/",
}

// Validate 校验登记记录。
func (m ManagedService) Validate() error {
	label := strings.TrimSpace(m.Label)
	if label == "" {
		return NewError(CodeInvalidArgument, "服务标签不能为空")
	}
	if len(label) > 256 {
		return NewError(CodeInvalidArgument, "服务标签过长")
	}
	if strings.ContainsAny(label, "/ \t\n\r") {
		return NewError(CodeInvalidArgument, "服务标签含非法字符")
	}
	// 拒绝系统关键服务与本应用自身：即使调用方想登记也不行。
	// 这是与 services.ValidateLabelForControl 重复的一道校验，
	// 但两处独立实现、面向不同层（数据层 vs 执行层），
	// 任一处漏掉都还有另一处兜底。
	if strings.HasPrefix(label, "com.apple.") {
		return NewError(CodeInvalidArgument, "不允许登记系统关键服务：%s", label)
	}
	if strings.HasPrefix(label, "com.macpanel.") {
		return NewError(CodeInvalidArgument, "不允许登记本应用自身的服务：%s", label)
	}

	if m.Domain == "" {
		return NewError(CodeInvalidArgument, "必须指定服务域")
	}
	if m.Domain != "system" {
		return NewError(CodeInvalidArgument,
			"只支持 system 域的服务；用户域（gui/%s）不在本应用的启停范围内", "uid")
	}

	if m.PlistPath == "" {
		return NewError(CodeInvalidArgument, "必须指定 plist 路径")
	}
	if !isAllowedPlistPath(m.PlistPath) {
		return NewError(CodeInvalidArgument,
			"plist 路径必须位于 /Library/LaunchDaemons 或 /System/Library/LaunchDaemons 下，实际 %q",
			m.PlistPath)
	}
	if strings.Contains(m.PlistPath, "..") {
		return NewError(CodeInvalidArgument, "plist 路径不能含 ..")
	}
	if !strings.HasSuffix(m.PlistPath, ".plist") {
		return NewError(CodeInvalidArgument, "plist 路径必须以 .plist 结尾")
	}
	// 路径的文件名应与标签一致：不一致通常意味着登记错了服务。
	// 允许不一致会带来「点 A 服务的按钮却操作了 B」的风险。
	base := m.PlistPath[strings.LastIndex(m.PlistPath, "/")+1:]
	if base != label+".plist" {
		return NewError(CodeInvalidArgument,
			"plist 文件名应与标签一致（期望 %s.plist，实际 %s）", label, base)
	}

	if len(m.DisplayName) > 128 {
		return NewError(CodeInvalidArgument, "显示名不能超过 128 个字符")
	}
	return nil
}

// isAllowedPlistPath 判断路径是否落在允许的目录内。
func isAllowedPlistPath(p string) bool {
	for _, dir := range allowedPlistDirs {
		if strings.HasPrefix(p, dir) {
			return true
		}
	}
	return false
}

// ServiceAction 是服务操作请求的结果。
type ServiceAction struct {
	Label  string `json:"label"`
	Action string `json:"action"`
	// Message 是给用户看的补充说明（例如「服务未运行，已跳过」）。
	Message string `json:"message,omitempty"`
}

// ServicesOverview 是服务列表接口的返回。
type ServicesOverview struct {
	Items   []ServiceSummary `json:"items"`
	Summary ServiceCounts    `json:"summary"`
	// Warnings 记录部分域读取失败之类的问题。
	// 用 warnings 而不是直接报错：能读到的部分仍然有价值。
	Warnings []string `json:"warnings,omitempty"`
}

// ServiceSummary 是列表里的一项（字段与 services.Service 对齐，
// 但定义在 model 以避免 model 依赖 platform）。
type ServiceSummary struct {
	Label         string `json:"label"`
	DisplayName   string `json:"displayName,omitempty"`
	Domain        string `json:"domain"`
	Kind          string `json:"kind"`
	PID           int    `json:"pid"`
	Running       bool   `json:"running"`
	PlistPath     string `json:"plistPath,omitempty"`
	Managed       bool   `json:"managed"`
	Controllable  bool   `json:"controllable"`
	BlockedReason string `json:"blockedReason,omitempty"`
	LastExit      string `json:"lastExit,omitempty"`
}

// ServiceCounts 是分类统计。
type ServiceCounts struct {
	Total          int `json:"total"`
	SystemCritical int `json:"systemCritical"`
	Self           int `json:"self"`
	ThirdParty     int `json:"thirdParty"`
	Managed        int `json:"managed"`
	Running        int `json:"running"`
}

// DescribeServiceAction 生成操作的中文描述，用于审计与界面提示。
func DescribeServiceAction(action string) string {
	switch action {
	case "start":
		return "启动"
	case "stop":
		return "停止"
	case "restart":
		return "重启"
	default:
		return fmt.Sprintf("未知操作(%s)", action)
	}
}
