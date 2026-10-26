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
	"/sbin/pfctl":            "查询 PF 状态（仅 -s 查询子命令）",
	"/usr/bin/sw_vers":       "读取 macOS 版本",
	"/usr/sbin/networksetup": "查询网络硬件端口（只读列表）",
	"/usr/bin/vm_stat":       "读取虚拟内存统计（只读）",
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
