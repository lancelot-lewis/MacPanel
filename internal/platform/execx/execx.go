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
	"strconv"
	"strings"
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
	"/bin/launchctl":         "launchd 服务查询与受管启停（参数级白名单见 launchctlGuard）",
	"/sbin/shutdown":         "受控重启（参数级白名单见 shutdownGuard）",
	"/usr/bin/plutil":        "校验 plist 语法（仅 -lint，只解析不安装）",
	"/usr/sbin/netstat":      "网络路由与连接查询（仅 -rn/-an 只读形式）",
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
	"/sbin/pfctl":       pfctlGuard,
	"/bin/launchctl":    launchctlGuard,
	"/sbin/shutdown":    shutdownGuard,
	"/usr/bin/plutil":   plutilGuard,
	"/usr/sbin/netstat": netstatGuard,
}

// launchctlGuard 限制 launchctl 的子命令集合。
//
// launchctl 是本项目里权限最高的工具：bootstrap 一个 plist 等于以 root
// 执行其中 ProgramArguments 指定的任意程序。因此这里只放行本应用真正
// 需要的子命令，并且**要求目标必须是 system 域**（不允许操作 user/GUI 域，
// 那属于用户会话，本应用不该介入）。
//
// 注意：这层只做「形态」限制。真正的边界在调用方（launchd 包）——
// 它把 plist 路径限定在 /Library/LaunchDaemons/com.macpanel.*，
// 把 label 限定在本应用前缀。两层缺一不可：只靠参数形态挡不住
// "bootstrap system /tmp/evil.plist"。
func launchctlGuard(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("launchctl 必须带子命令")
	}

	// 明确禁止的子命令：它们要么能加载任意程序，要么会改变全局状态。
	forbidden := map[string]string{
		"submit":     "以 root 提交任意命令执行（任意代码执行）",
		"load":       "旧式加载接口，参数语义宽松",
		"unload":     "旧式卸载接口，参数语义宽松",
		"reboot":     "重启用户会话，不在本应用职责内",
		"shutdown":   "关机，应通过受控重启接口",
		"halt":       "停机，不在本应用职责内",
		"setenv":     "修改 launchd 环境变量会影响所有服务",
		"unsetenv":   "修改 launchd 环境变量会影响所有服务",
		"config":     "修改 launchd 全局配置",
		"attach":     "调试用途",
		"managerpid": "调试用途",
	}
	sub := args[0]
	if reason, bad := forbidden[sub]; bad {
		return fmt.Errorf("launchctl %s 被禁止：%s", sub, reason)
	}

	switch sub {
	case "list":
		// `launchctl list` 列出全部服务；带 label 时查询单个。
		// 两者都是只读。
		if len(args) > 2 {
			return fmt.Errorf("launchctl list 最多接受一个 label 参数")
		}
		return nil

	case "print":
		// 必须指定 target。print 是只读查询，因此允许查询任意域
		// （诊断需要看 gui/<uid> 与 user/<uid>），
		// 但改变状态的子命令仍只允许 system 域。
		if len(args) < 2 {
			return fmt.Errorf("launchctl print 需要指定目标")
		}
		return requireReadableDomain(args[1])

	case "bootstrap":
		// bootstrap <domain> <plist-path>
		// 只允许 system 域：用户域属于交互会话，不在本应用职责内。
		if len(args) < 3 {
			return fmt.Errorf("launchctl bootstrap 需要 <domain> <plist>")
		}
		if args[1] != "system" {
			return fmt.Errorf("launchctl bootstrap 只允许 system 域，实际 %q", args[1])
		}
		if !strings.HasSuffix(args[2], ".plist") {
			return fmt.Errorf("launchctl bootstrap 的目标必须是 plist 文件")
		}
		return nil

	case "bootout", "kickstart", "enable", "disable":
		// bootout 接受 system/<label> 或 domain + plist；统一要求 system 域。
		if len(args) < 2 {
			return fmt.Errorf("launchctl %s 需要指定目标", sub)
		}
		return requireSystemDomain(args[1])

	default:
		return fmt.Errorf("launchctl 子命令 %q 不在允许范围内", sub)
	}
}

// requireReadableDomain 允许只读查询任意域。
//
// 与 requireSystemDomain 的区别：print 不改变任何状态，
// 而排查「某服务为什么没起来」经常需要看用户域。
// 因此这里放宽到 system / gui/<uid> / user/<uid>。
func requireReadableDomain(target string) error {
	if target == "" {
		return fmt.Errorf("launchctl 目标不能为空")
	}
	for _, prefix := range []string{"system", "gui/", "user/", "pid/"} {
		if target == prefix || strings.HasPrefix(target, prefix) {
			return nil
		}
	}
	return fmt.Errorf("launchctl 目标域不被允许：%q", target)
}

// requireSystemDomain 要求 target 形如 system 或 system/<label>。
//
// 拒绝 user/<uid>/... 与 gui/<uid>/...：那些是用户会话域，
// 本应用以 root 运行，没有理由操作他人会话。
func requireSystemDomain(target string) error {
	if target == "system" {
		return nil
	}
	if strings.HasPrefix(target, "system/") {
		label := strings.TrimPrefix(target, "system/")
		if label == "" {
			return fmt.Errorf("launchctl 目标缺少 label")
		}
		return nil
	}
	return fmt.Errorf("launchctl 目标必须是 system 域，实际 %q", target)
}

// plutilGuard 限制 plutil 只做语法校验。
//
// plutil 也能改写 plist 文件（-replace、-insert 等），那是本应用
// 不需要的能力：所有 plist 都由本项目自己生成，用不到就地编辑。
func plutilGuard(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("plutil 必须带参数")
	}
	if args[0] != "-lint" {
		return fmt.Errorf("plutil 只允许 -lint（语法校验），实际 %q", args[0])
	}
	if len(args) != 2 {
		return fmt.Errorf("plutil -lint 只接受一个文件参数")
	}
	return nil
}

// netstatGuard 限制 netstat 只做只读查询。
//
// 允许的形式（本应用诊断需要的全部）：
//
//	-rn -f inet6    路由表
//	-an -f inet6    全部连接（从中筛 LISTEN）
//	-an -f inet     IPv4 连接（诊断对比用）
//
// netstat 还有 -d（删除路由）等写操作选项，必须封死。
func netstatGuard(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("netstat 必须带参数")
	}

	// 明确禁止会改变系统状态的选项。
	forbidden := map[string]string{
		"-d":       "删除路由条目（会改变系统网络配置）",
		"-fdelete": "删除路由条目",
		"-nand":    "不在本应用职责内",
	}
	for _, a := range args {
		if reason, bad := forbidden[a]; bad {
			return fmt.Errorf("netstat %s 被禁止：%s", a, reason)
		}
	}

	// 必须带家族限定，避免输出混杂。
	hasFamily := false
	for i, a := range args {
		if a == "-f" && i+1 < len(args) {
			fam := args[i+1]
			if fam != "inet6" && fam != "inet" {
				return fmt.Errorf("netstat -f 只允许 inet 或 inet6，实际 %q", fam)
			}
			hasFamily = true
		}
	}
	if !hasFamily {
		return fmt.Errorf("netstat 必须指定 -f inet 或 -f inet6")
	}

	hasRoute := false
	hasAll := false
	for _, a := range args {
		switch a {
		case "-rn", "-r":
			hasRoute = true
		case "-an", "-a":
			hasAll = true
		}
	}
	if !hasRoute && !hasAll {
		return fmt.Errorf("netstat 只允许 -rn（路由）或 -an（连接）查询")
	}
	return nil
}

// shutdownGuard 限制 shutdown 只做「延后重启」。
//
// 为什么不能放开：shutdown 既能把机器重启，也能关机（-h）与断电（-p）。
// 本应用只承诺「受控重启」，绝不该有关机的可能路径——
// 关机后如果机器不在物理可及范围，用户就彻底失联了。
//
// 只允许：
//
//	-r now           立即重启
//	-r +N            N 分钟后重启（1 ≤ N ≤ 60）
func shutdownGuard(args []string) error {
	if len(args) != 2 {
		return fmt.Errorf("shutdown 只允许 -r <now|+N> 两个参数，实际 %v", args)
	}
	if args[0] != "-r" {
		return fmt.Errorf("shutdown 只允许 -r（重启），实际 %q；本应用不支持关机或断电", args[0])
	}

	when := args[1]
	if when == "now" {
		return nil
	}
	if !strings.HasPrefix(when, "+") {
		return fmt.Errorf("shutdown 时间必须是 now 或 +N，实际 %q", when)
	}
	n, err := strconv.Atoi(strings.TrimPrefix(when, "+"))
	if err != nil {
		return fmt.Errorf("shutdown 延后分钟数无法解析: %q", when)
	}
	if n < 1 || n > 60 {
		return fmt.Errorf("shutdown 延后分钟数必须在 1-60 之间，实际 %d", n)
	}
	return nil
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
