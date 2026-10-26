//go:build darwin

package firewalld

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/lancelot-lewis/MacPanel/internal/platform/execx"
)

// pfctlPath 是 pfctl 的绝对路径。
// 注意：macOS 上它在 /sbin，不是 /usr/sbin（这个差异实际踩过）。
const pfctlPath = "/sbin/pfctl"

// cmdTimeout 单条 pfctl 命令超时。
// 语法检查在大规则集上可能稍慢，给 10 秒。
const cmdTimeout = 10 * time.Second

// PFExecutor 是真实的 PF 执行器。
//
// 安全约束（逐条对应 PLAN.md 的要求）：
//   - 只执行三种固定操作：-n -f（语法检查）、-a <anchor> -f（加载）、-s Anchors / -a <anchor> -sr（快照）
//   - **拒绝任何 -d / -e / -F / -f <主配置> 调用**：那是全局开关或全量刷新，
//     会影响其它程序，也可能让机器失去防火墙。
//   - anchor 名与文件路径在构造时就校验，不接受运行时可变输入。
type PFExecutor struct {
	anchorName string
	// dryRun 为 true 时不真的执行 pfctl，只检查参数合法性。
	// 用于开发环境（非 root）验证流程，以及在 CI 中跑集成测试。
	dryRun bool
	// allowedPaths 是允许加载的规则文件路径白名单。
	// 只允许本应用的 anchor 文件与 /dev/null（回滚用）。
	allowedPaths map[string]bool
}

// NewPFExecutor 创建执行器。
//
// rulePath 是本应用的 anchor 规则文件；除此之外只允许 /dev/null。
func NewPFExecutor(anchorName, rulePath string, dryRun bool) (*PFExecutor, error) {
	if anchorName == "" {
		return nil, fmt.Errorf("firewalld: anchorName 不能为空")
	}
	// anchor 名会出现在命令行参数里，因此严格校验字符集。
	if err := validateAnchorName(anchorName); err != nil {
		return nil, err
	}
	if !filepath.IsAbs(rulePath) {
		return nil, fmt.Errorf("firewalld: 规则文件路径必须是绝对路径，实际 %q", rulePath)
	}

	return &PFExecutor{
		anchorName: anchorName,
		dryRun:     dryRun,
		allowedPaths: map[string]bool{
			rulePath:    true,
			"/dev/null": true,
		},
	}, nil
}

// validateAnchorName 校验 anchor 名。
// 只允许点分标识符（com.macpanel.ipv6），拒绝任何 shell/路径元字符。
func validateAnchorName(name string) error {
	if len(name) > 100 {
		return fmt.Errorf("firewalld: anchor 名过长（最多 100 字符）")
	}
	for _, c := range name {
		switch {
		case c >= 'a' && c <= 'z':
		case c >= 'A' && c <= 'Z':
		case c >= '0' && c <= '9':
		case c == '.' || c == '-' || c == '_':
		default:
			return fmt.Errorf("firewalld: anchor 名 %q 含非法字符 %q", name, c)
		}
	}
	return nil
}

// CheckSyntax 用 pfctl -n -f 只解析不加载。
//
// `-n` 是关键：它让 pfctl 解析并报告错误但不触碰运行状态，
// 因此可以在应用前安全地验证语法。跳过这一步等于拿生产环境试错。
func (e *PFExecutor) CheckSyntax(ctx context.Context, path string) error {
	if !e.allowedPaths[path] {
		return fmt.Errorf("firewalld: 拒绝检查白名单之外的路径 %q", path)
	}
	if e.dryRun {
		// dry-run 下仍要确认文件可读，否则「语法通过」是假的。
		if _, err := os.Stat(path); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("dry-run 检查文件失败：%w", err)
		}
		return nil
	}

	res, err := execx.Run(ctx, cmdTimeout, pfctlPath, "-n", "-f", path)
	if err != nil {
		// 把 pfctl 的原始 stderr 附上：这是定位语法问题的唯一线索。
		detail := strings.TrimSpace(res.Stderr)
		if detail == "" {
			detail = err.Error()
		}
		return fmt.Errorf("pfctl 语法检查失败：%s", detail)
	}
	return nil
}

// Load 把规则文件加载到指定 anchor。
//
// 命令形如：pfctl -a com.macpanel.ipv6 -f /etc/pf.anchors/...
// 只影响本应用的 anchor，不触碰主规则集与其它 anchor。
func (e *PFExecutor) Load(ctx context.Context, anchor, path string) error {
	if anchor != e.anchorName {
		return fmt.Errorf("firewalld: 拒绝操作非本应用的 anchor %q", anchor)
	}
	if !e.allowedPaths[path] {
		return fmt.Errorf("firewalld: 拒绝加载白名单之外的路径 %q", path)
	}
	if e.dryRun {
		return nil
	}

	res, err := execx.Run(ctx, cmdTimeout, pfctlPath, "-a", anchor, "-f", path)
	if err != nil {
		detail := strings.TrimSpace(res.Stderr)
		if detail == "" {
			detail = err.Error()
		}
		return fmt.Errorf("pfctl 加载规则失败：%s", detail)
	}
	return nil
}

// SnapshotCurrent 读取 anchor 当前的生效规则。
//
// 用 `pfctl -a <anchor> -sr` 输出该 anchor 的规则文本。
// anchor 不存在时 pfctl 返回空输出（不是错误），这里统一返回空字符串。
func (e *PFExecutor) SnapshotCurrent(ctx context.Context, anchor string) (string, error) {
	if anchor != e.anchorName {
		return "", fmt.Errorf("firewalld: 拒绝读取非本应用的 anchor %q", anchor)
	}
	if e.dryRun {
		return "", nil
	}

	res, err := execx.Run(ctx, cmdTimeout, pfctlPath, "-a", anchor, "-sr")
	if err != nil {
		// anchor 未被加载时 pfctl 可能返回非零；这不算故障。
		if strings.Contains(strings.ToLower(res.Stderr), "no such anchor") ||
			strings.Contains(strings.ToLower(res.Stderr), "not found") {
			return "", nil
		}
		detail := strings.TrimSpace(res.Stderr)
		if detail == "" {
			detail = err.Error()
		}
		return "", fmt.Errorf("读取 anchor 规则失败：%s", detail)
	}
	return res.Stdout, nil
}

// 编译期断言。
var _ PFApplier = (*PFExecutor)(nil)
