package services

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/lancelot-lewis/MacPanel/internal/platform/execx"
)

// LaunchctlPath 是 launchctl 的绝对路径。
const LaunchctlPath = "/bin/launchctl"

// Scanner 扫描 launchd 服务状态。
//
// 依赖以函数字段注入，便于测试时不真的调用 launchctl、
// 也不需要真的存在那些 plist 文件。
type Scanner struct {
	// runPrint 执行 `launchctl print <domain>` 并返回标准输出。
	runPrint func(ctx context.Context, domain string) (string, error)
	// readFile 读取 plist 内容。
	readFile func(path string) ([]byte, error)
	// stat 检查文件是否存在。
	stat func(path string) error
	// uid 返回当前用户 ID，用于构造 gui/<uid> 域。
	uid func() int
}

// NewScanner 创建真实的扫描器。
func NewScanner() *Scanner {
	return &Scanner{
		runPrint: func(ctx context.Context, domain string) (string, error) {
			// print 输出可能很大（系统域 400+ 服务），
			// 但仍远小于 execx 的 1 MiB 上限。
			res, err := execx.Run(ctx, 10*time.Second, LaunchctlPath, "print", domain)
			if err != nil {
				return "", err
			}
			return res.Stdout, nil
		},
		readFile: os.ReadFile,
		stat: func(path string) error {
			_, err := os.Stat(path)
			return err
		},
		uid: os.Getuid,
	}
}

// NewScannerForTest 创建可注入依赖的扫描器（仅供测试）。
func NewScannerForTest(
	runPrint func(ctx context.Context, domain string) (string, error),
	readFile func(string) ([]byte, error),
	stat func(string) error,
	uid func() int,
) *Scanner {
	return &Scanner{runPrint: runPrint, readFile: readFile, stat: stat, uid: uid}
}

// ListOptions 控制扫描范围。
type ListOptions struct {
	// IncludeGUI 是否扫描当前用户的图形会话域。
	//
	// 默认扫描：用户想知道「哪些登录项在跑」时这是必要信息。
	// 但以 root 运行 Agent 时，gui/<uid> 是 root 的会话而非登录用户，
	// 因此调用方需要显式传入要查询的 uid。
	IncludeGUI bool
	// GUIUID 是要查询的图形会话用户 ID（IncludeGUI 为 true 时有效）。
	GUIUID int
}

// List 扫描服务列表。
//
// 单个域扫描失败不会让整体失败：system 域可能因权限不足读不到，
// 但 gui 域仍可展示。失败原因通过返回的 warnings 告知调用方，
// 而不是伪造「没有服务」。
func (s *Scanner) List(ctx context.Context, opts ListOptions) ([]Service, []string, error) {
	var (
		out      []Service
		warnings []string
	)

	systemOut, err := s.runPrint(ctx, "system")
	if err != nil {
		warnings = append(warnings, fmt.Sprintf("无法读取 system 域：%v", err))
	} else {
		for _, svc := range ParsePrintServices(systemOut) {
			svc.Domain = DomainSystem
			out = append(out, svc)
		}
	}

	if opts.IncludeGUI {
		uid := opts.GUIUID
		if uid <= 0 {
			// 未指定时用当前进程的 uid。Agent 以 root 运行时
			// 这是 root 的会话，与登录用户不同——调用方应显式传 uid。
			uid = s.uid()
		}
		domain := fmt.Sprintf("gui/%d", uid)
		guiOut, err := s.runPrint(ctx, domain)
		if err != nil {
			warnings = append(warnings, fmt.Sprintf("无法读取 %s 域：%v", domain, err))
		} else {
			for _, svc := range ParsePrintServices(guiOut) {
				svc.Domain = DomainGUI
				out = append(out, svc)
			}
		}
	}

	// 探测 plist 路径（配置来源）。这一步对每个服务都可能做文件 stat，
	// 但只有 400 个左右、且都是缓存命中的 stat，成本可接受。
	for i := range out {
		out[i].PlistPath = s.findPlist(out[i].Label, out[i].Domain)
	}

	// 稳定排序：先按域（system 在前），再按标签。
	// 不用 map 遍历，否则刷新时顺序抖动，用户会觉得列表在乱跳。
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Domain != out[j].Domain {
			return out[i].Domain == DomainSystem
		}
		return out[i].Label < out[j].Label
	})

	return out, warnings, nil
}

// findPlist 在标准目录中查找服务的 plist，返回首个存在路径。
func (s *Scanner) findPlist(label string, domain Domain) string {
	for _, p := range PlistCandidates(label, domain) {
		if err := s.stat(p); err == nil {
			return p
		}
	}
	return ""
}

// Detail 返回单个服务的配置详情。
//
// 找不到 plist 时不报错：服务可能来自内嵌 plist 或文件已被删除但仍加载。
// 这种情况本身是有价值的诊断信息，调用方据 PlistPath 为空即可判断。
func (s *Scanner) Detail(ctx context.Context, label string, domain Domain) (PlistInfo, string, error) {
	path := s.findPlist(label, domain)
	if path == "" {
		return PlistInfo{}, "", nil
	}

	data, err := s.readFile(path)
	if err != nil {
		return PlistInfo{}, path, fmt.Errorf("读取 plist 失败: %w", err)
	}

	info, err := ParsePlist(data)
	if err != nil {
		return PlistInfo{}, path, err
	}
	return info, path, nil
}

// Enrich 把受管状态与操作权限判定合并进服务列表。
//
// 单独一步而不是放在 List 里：受管状态来自数据库，而 List 只看系统状态。
// 分开后 List 可以在没有数据库的场景（如 CLI 诊断）下复用。
//
// 两个集合必须分开传：
//   - registered 表示「已登记」（可能只是观察，不允许操作）；
//   - allowed 表示「已授权启停」。
//
// 早期实现只传一个集合，导致「已登记但未授权」在界面上显示成
// 「未登记」，用户看不到「授权启停」入口——登记了却无法进一步操作。
func Enrich(list []Service, registered, allowed map[string]bool) []Service {
	out := make([]Service, len(list))
	copy(out, list)

	for i := range out {
		svc := &out[i]
		svc.Managed = registered[svc.Label]

		if svc.Kind == KindThirdParty && allowed[svc.Label] {
			svc.Controllable = true
			svc.BlockedReason = ""
			continue
		}

		svc.Controllable = false
		if svc.Kind == KindThirdParty && svc.Managed {
			// 已登记但未授权：给出与「未登记」不同的提示，
			// 因为这两者的下一步动作完全不同。
			svc.BlockedReason = "已登记但未授权启停（可在此授权）"
			continue
		}
		svc.BlockedReason = ExplainBlock(svc.Kind, svc.Managed)
	}

	return out
}

// Summary 汇总各类服务数量，用于界面顶部的概览。
type Summary struct {
	Total          int `json:"total"`
	SystemCritical int `json:"systemCritical"`
	Self           int `json:"self"`
	ThirdParty     int `json:"thirdParty"`
	Managed        int `json:"managed"`
	Running        int `json:"running"`
}

// Summarize 统计服务分类数量。
func Summarize(list []Service) Summary {
	var s Summary
	s.Total = len(list)
	for _, svc := range list {
		switch svc.Kind {
		case KindSystemCritical:
			s.SystemCritical++
		case KindSelf:
			s.Self++
		default:
			s.ThirdParty++
		}
		if svc.Managed {
			s.Managed++
		}
		if svc.Running {
			s.Running++
		}
	}
	return s
}

// ValidateLabelForControl 校验某个标签是否允许启停操作的形态要求。
//
// 这是「白名单之外的第二道门槛」：即使白名单里误加了一条
// com.apple.finder，这里也会拒绝。两层独立校验的原因是
// 白名单可能存在历史脏数据或被误改。
func ValidateLabelForControl(label string) error {
	if label == "" {
		return fmt.Errorf("服务标签不能为空")
	}
	if len(label) > 256 {
		return fmt.Errorf("服务标签过长")
	}
	// 不允许路径分隔符与空白：标签会被拼进 launchctl 参数。
	if strings.ContainsAny(label, "/ \t\n\r") {
		return fmt.Errorf("服务标签含非法字符")
	}

	switch Classify(label) {
	case KindSystemCritical:
		return fmt.Errorf("系统关键服务不允许操作：%s", label)
	case KindSelf:
		return fmt.Errorf("本应用自身的服务不允许通过面板操作：%s", label)
	}
	return nil
}

// LogPaths 返回服务的日志候选路径。
//
// 优先用 plist 里声明的路径；没声明时给出系统统一日志的查询方式。
// 不返回「猜出来的文件路径」——那会让用户去找一个不存在的文件。
func LogPaths(info PlistInfo) []string {
	var out []string
	if info.StandardOutPath != "" {
		out = append(out, info.StandardOutPath)
	}
	if info.StandardErrorPath != "" {
		out = append(out, info.StandardErrorPath)
	}
	if len(out) == 0 {
		return nil
	}

	// 去重（两个字段常指向同一文件）。
	seen := make(map[string]bool, len(out))
	uniq := out[:0]
	for _, p := range out {
		if seen[p] {
			continue
		}
		seen[p] = true
		uniq = append(uniq, filepath.Clean(p))
	}
	return uniq
}
