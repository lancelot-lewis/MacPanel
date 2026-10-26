// Package execx 提供受控的外部命令执行。
//
// 安全要点：
//   - 绝不经过 shell，参数以数组传递，避免命令注入；
//   - 只允许调用显式登记的可执行文件（白名单），拒绝任意路径；
//   - 强制超时，避免系统命令卡死拖垮 Agent；
//   - 限制输出大小，防止异常输出打爆内存。
package execx

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"path/filepath"
	"time"
)

// MaxOutputBytes 单次命令输出上限。超出即截断并标记。
const MaxOutputBytes = 1 << 20 // 1 MiB

// allowedBinaries 是允许执行的可执行文件绝对路径白名单。
// 新增系统命令必须在此登记，并有明确说明。
//
// 注意路径差异：macOS 上 pfctl 位于 /sbin，而 sysctl/networksetup 位于 /usr/sbin，
// vm_stat/sw_vers 位于 /usr/bin。写错路径会导致「命令不存在」而不是权限错误，
// 因此这里保留真实路径，不做多路径猜测。
var allowedBinaries = map[string]string{
	"/usr/sbin/sysctl":       "读取内核与硬件参数（只读）",
	"/sbin/pfctl":            "PF 查询与 anchor 规则加载（参数级白名单见 pfctlGuard）",
	"/usr/bin/sw_vers":       "读取 macOS 版本",
	"/usr/sbin/networksetup": "查询网络硬件端口（只读列表）",
	"/usr/bin/vm_stat":       "读取虚拟内存统计（只读）",
}

// argGuards 是「参数级」白名单，只对危险命令生效。
//
// 为什么必须有它：仅校验二进制路径是不够的。pfctl 既能
//
//	-a <anchor> -f <file>   （只影响本应用 anchor，安全）
//
// 也能
//
//	-d                      （全局关闭 PF，等于整机失去防火墙）
//	-f /etc/pf.conf         （全量刷新主规则集，会与其他程序打架）
//	-F all                  （清空所有规则）
//
// 一旦 pfctl 进白名单，上述调用就都能发起。因此在参数层面再做一次限制。
var argGuards = map[string]func(args []string) error{
	"/sbin/pfctl": pfctlGuard,
}

// pfctlGuard 限制 pfctl 只接受三类调用：
//
//	-s info | -s Anchors          只读查询状态
//	-n -f <file>                  语法检查（不加载）
//	-a <anchor> -f <file>         加载到指定 anchor
//	-a <anchor> -sr               读取该 anchor 的规则
//
// 显式拒绝 -d/-e/-F/-k/-x 等会改变全局状态或输出的选项。
func pfctlGuard(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("pfctl 必须带参数")
	}

	// 明确禁止的危险短选项。逐个列出而不是「见 d 就拒」，
	// 避免误伤 -d 之外的合法用法（本应用不使用 -d 的任何语义）。
	forbiddenOpts := map[string]string{
		"-d": "关闭 PF（会全局禁用防火墙，影响所有服务）",
		"-e": "启用 PF（不在本应用职责内）",
		"-F": "清空规则表（影响全部 anchor）",
		"-k": "终止全部状态（影响所有连接）",
		"-x": "设置调试级别（调试用途，禁止）",
		"-z": "清空计数器（会破坏状态统计）",
		"-m": "合并规则（语义复杂，不在此实现）",
		"-N": "只解析主配置（不在此实现）",
		"-O": "只解析选项（不在此实现）",
	}
	has := func(flag string) bool {
		for _, a := range args {
			if a == flag {
				return true
			}
		}
		return false
	}
	for flag, reason := range forbiddenOpts {
		if has(flag) {
			return fmt.Errorf("pfctl %s 被禁止：%s", flag, reason)
		}
	}

	// 只读查询：-s <modifier>
	if has("-s") {
		if !has("info") && !has("Anchors") {
			return fmt.Errorf("pfctl -s 只允许 info 或 Anchors 子命令")
		}
		return nil
	}

	// 语法检查：-n -f <file>
	if has("-n") {
		if !has("-f") {
			return fmt.Errorf("pfctl -n 必须配合 -f <file>")
		}
		return nil
	}

	// anchor 操作：-a <anchor> 且必须带 -f 或 -sr
	if has("-a") {
		if !has("-f") && !has("-sr") {
			return fmt.Errorf("pfctl -a 必须配合 -f <file> 或 -sr")
		}
		return nil
	}

	return fmt.Errorf("pfctl 调用不在允许范围内：%v", args)
}

// Result 是命令执行结果。
type Result struct {
	Stdout    string
	Stderr    string
	ExitCode  int
	Truncated bool
	Duration  time.Duration
}

// CommandError 表示命令以非零码退出。
type CommandError struct {
	Binary   string
	Args     []string
	ExitCode int
	Stderr   string
}

func (e *CommandError) Error() string {
	return fmt.Sprintf("命令 %s 退出码 %d: %s", filepath.Base(e.Binary), e.ExitCode, trim(e.Stderr))
}

// MissingBinaryError 表示白名单内的可执行文件在本机不存在。
type MissingBinaryError struct{ Binary string }

func (e *MissingBinaryError) Error() string {
	return fmt.Sprintf("系统命令不存在: %s", e.Binary)
}

// Run 执行白名单命令。binary 必须是绝对路径且在 allowedBinaries 中。
func Run(ctx context.Context, timeout time.Duration, binary string, args ...string) (Result, error) {
	if _, ok := allowedBinaries[binary]; !ok {
		return Result{}, fmt.Errorf("execx: 拒绝执行未登记的命令 %q", binary)
	}
	// 参数级校验：仅对登记了 guard 的命令执行。
	// 这一层的必要性见 argGuards 的注释——pfctl 在白名单内，
	// 但并非它所有参数都允许。
	if guard, ok := argGuards[binary]; ok {
		if err := guard(args); err != nil {
			return Result{}, fmt.Errorf("execx: %w", err)
		}
	}

	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	start := time.Now()
	cmd := exec.CommandContext(ctx, binary, args...)

	var stdout, stderr bytes.Buffer
	outW := &limitedWriter{w: &stdout, limit: MaxOutputBytes}
	errW := &limitedWriter{w: &stderr, limit: 64 << 10}
	cmd.Stdout = outW
	cmd.Stderr = errW

	runErr := cmd.Run()
	res := Result{
		Stdout:    stdout.String(),
		Stderr:    stderr.String(),
		Duration:  time.Since(start),
		Truncated: outW.truncated || errW.truncated,
	}

	if runErr != nil {
		var exitErr *exec.ExitError
		if errors.As(runErr, &exitErr) {
			res.ExitCode = exitErr.ExitCode()
			return res, &CommandError{
				Binary:   binary,
				Args:     args,
				ExitCode: res.ExitCode,
				Stderr:   res.Stderr,
			}
		}
		if errors.Is(runErr, exec.ErrNotFound) {
			return res, &MissingBinaryError{Binary: binary}
		}
		if ctx.Err() != nil {
			return res, fmt.Errorf("execx: 命令超时或被取消: %w", ctx.Err())
		}
		return res, fmt.Errorf("execx: 执行 %s 失败: %w", binary, runErr)
	}
	return res, nil
}

// IsAllowed 供测试断言白名单。
func IsAllowed(binary string) bool {
	_, ok := allowedBinaries[binary]
	return ok
}

// AllowedBinaries 返回白名单快照，便于文档与自检输出。
func AllowedBinaries() map[string]string {
	out := make(map[string]string, len(allowedBinaries))
	for k, v := range allowedBinaries {
		out[k] = v
	}
	return out
}

type limitedWriter struct {
	w         io.Writer
	limit     int
	written   int
	truncated bool
}

func (l *limitedWriter) Write(p []byte) (int, error) {
	total := len(p)
	if l.written >= l.limit {
		l.truncated = true
		// 报告全部写入（实际丢弃），否则 os/exec 会因 short write 提前终止命令。
		return total, nil
	}
	remaining := l.limit - l.written
	if len(p) > remaining {
		p = p[:remaining]
		l.truncated = true
	}
	n, err := l.w.Write(p)
	l.written += n
	if err != nil {
		return n, err
	}
	// 即使只写了部分，也要报告「已消费全部输入」，避免调用方误判。
	return total, nil
}

func trim(s string) string {
	const max = 200
	s = string(bytes.TrimSpace([]byte(s)))
	if len(s) > max {
		return s[:max] + "..."
	}
	return s
}
