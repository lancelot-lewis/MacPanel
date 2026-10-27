package launchd

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/lancelot-lewis/MacPanel/internal/platform/execx"
)

// 常用路径。
const (
	LaunchctlPath = "/bin/launchctl"
	PlutilPath    = "/usr/bin/plutil"
)

// Runner 抽象 launchctl 的调用，便于测试注入。
//
// 为什么不直接在 Installer 里调 execx：安装流程包含「写文件 → 校验 →
// bootstrap」多步，其中任何一步失败都要能测试到回滚分支。把命令执行
// 抽象出来后，测试可以用 fake 精确模拟「bootstrap 失败」。
type Runner interface {
	// Run 执行 launchctl 子命令（不含二进制路径）。
	Run(ctx context.Context, args ...string) error
	// Output 执行并返回标准输出（用于 print / list 查询）。
	Output(ctx context.Context, args ...string) (string, error)
}

// execRunner 是基于 execx 的真实实现。
type execRunner struct{}

// NewExecRunner 返回真实命令执行器。
func NewExecRunner() Runner { return execRunner{} }

func (execRunner) Run(ctx context.Context, args ...string) error {
	// launchctl 有时会长时间阻塞（例如等待服务退出），
	// 因此超时设得比只读命令宽松。
	_, err := execx.Run(ctx, 30*time.Second, LaunchctlPath, args...)
	return err
}

func (execRunner) Output(ctx context.Context, args ...string) (string, error) {
	res, err := execx.Run(ctx, 15*time.Second, LaunchctlPath, args...)
	return res.Stdout, err
}

// InstallState 描述一个任务的安装状态。
//
// 分开报告「文件在」与「已加载」：两者不一致是常见的故障形态。
// 例如 plist 写了但 bootstrap 失败 —— 界面上必须能看出这一点，
// 而不是笼统显示「已安装」。
type InstallState struct {
	// PlistExists 表示 plist 文件存在（开机时 launchd 会自动加载）。
	PlistExists bool `json:"plistExists"`
	// Loaded 表示当前运行中的 launchd 已加载该任务。
	Loaded bool `json:"loaded"`
	// PlistPath 是文件路径。
	PlistPath string `json:"plistPath"`
}

// Healthy 表示文件与加载状态一致。
func (s InstallState) Healthy() bool { return s.PlistExists == s.Loaded }

// Installer 负责 plist 的安装与卸载。
type Installer struct {
	dir string
	run Runner

	// validate 为 false 时跳过 plutil 校验（仅测试与 dry-run）。
	validate bool
	// dryRun 为 true 时不调用 launchctl。
	dryRun bool
}

// NewInstaller 创建安装器。dir 为空时使用系统默认目录。
func NewInstaller(dir string, runner Runner) *Installer {
	if dir == "" {
		dir = DaemonDir
	}
	if runner == nil {
		runner = NewExecRunner()
	}
	return &Installer{dir: dir, run: runner, validate: true}
}

// NewDryRunInstaller 创建一个「只写文件、不调用 launchctl」的安装器。
//
// 用途：开发模式（非 root）。此时无法 bootstrap 系统级任务，但如果
// 什么都不做，前端就看不到任何状态，开发时无从验证同步逻辑。
// 因此写真实 plist 文件到指定目录，状态查询按「文件存在即已加载」处理。
//
// 必须明确：dry-run 下任务**不会真的被执行**。这只能用于界面开发，
// 不能用于验证调度是否生效。
func NewDryRunInstaller(dir string) *Installer {
	if dir == "" {
		dir = DaemonDir
	}
	return &Installer{dir: dir, run: noopRunner{}, validate: false, dryRun: true}
}

// noopRunner 不做任何实际调用。
type noopRunner struct{}

func (noopRunner) Run(ctx context.Context, args ...string) error { return nil }

func (noopRunner) Output(ctx context.Context, args ...string) (string, error) {
	return "dry-run", nil
}

// NewInstallerWithoutValidation 创建一个跳过 plutil 校验的安装器（仅测试用）。
//
// 生产路径必须校验：plist 语法错误会让 launchd 静默忽略该任务，
// 用户看到的是「设置成功但从不执行」。
func NewInstallerWithoutValidation(dir string, runner Runner) *Installer {
	inst := NewInstaller(dir, runner)
	inst.validate = false
	return inst
}

// Dir 返回安装目录。
func (i *Installer) Dir() string { return i.dir }

// plistPath 计算某 label 的文件路径，并强制它落在安装目录内。
//
// 二次校验落点：即使 Label 校验已经挡住 / 和 ..，这里仍然再查一遍
// 绝对路径前缀。因为「路径必须在预期目录内」是安全不变量，
// 值得用冗余检查来保证。
func (i *Installer) plistPath(label string) (string, error) {
	if err := ValidateLabel(label); err != nil {
		return "", err
	}
	p := filepath.Join(i.dir, label+".plist")
	if !strings.HasPrefix(p, i.dir+string(os.PathSeparator)) {
		return "", fmt.Errorf("launchd: plist 路径越界: %q", p)
	}
	return p, nil
}

// State 查询某 label 的安装状态。
func (i *Installer) State(ctx context.Context, label string) (InstallState, error) {
	path, err := i.plistPath(label)
	if err != nil {
		return InstallState{}, err
	}

	st := InstallState{PlistPath: path}
	if _, err := os.Stat(path); err == nil {
		st.PlistExists = true
	} else if !os.IsNotExist(err) {
		return st, fmt.Errorf("launchd: 检查 plist 失败: %w", err)
	}

	if i.dryRun {
		// dry-run 没有真实的 launchd 状态，按文件存在即视作已加载，
		// 否则每次同步都会认为「需要重装」而反复写文件。
		st.Loaded = st.PlistExists
		return st, nil
	}

	// print 成功即表示已加载。失败分两种：服务不存在（正常情况）
	// 与 launchctl 本身出错。前者不报错，后者也不能让状态查询整体失败——
	// 否则 launchctl 出问题时整个界面会报错而无从判断文件状态。
	if _, err := i.run.Output(ctx, "print", "system/"+label); err == nil {
		st.Loaded = true
	}
	return st, nil
}

// Install 写入 plist 并加载。
//
// 流程与失败处理：
//  1. 校验 Spec（安全不变量）；
//  2. 渲染 plist；
//  3. plutil -lint 校验语法；
//  4. 若已加载则先 bootout（否则 bootstrap 会因已存在而失败）；
//  5. 原子写入 plist 文件；
//  6. bootstrap 加载。失败则删除刚写的文件，避免留下孤儿 plist
//     ——否则下次开机 launchd 会自动加载它，变成「安装失败但实际生效」。
func (i *Installer) Install(ctx context.Context, spec Spec) error {
	if err := spec.Validate(); err != nil {
		return err
	}
	data, err := RenderPlist(spec)
	if err != nil {
		return err
	}

	path, err := i.plistPath(spec.Label)
	if err != nil {
		return err
	}

	if i.validate {
		if err := validatePlist(ctx, data); err != nil {
			return err
		}
	}

	if i.dryRun {
		// dry-run：只写文件。文件存在即视为「已安装」。
		return writeFileAtomic(path, data, 0o644)
	}

	// 已加载时先卸载，否则 bootstrap 报 "service already loaded"。
	if _, err := i.run.Output(ctx, "print", "system/"+spec.Label); err == nil {
		if err := i.run.Run(ctx, "bootout", "system/"+spec.Label); err != nil {
			// 卸载失败不直接放弃：可能刚好在两次检查之间被卸载了。
			// 但若后续 bootstrap 也失败，错误信息会体现出来。
			_ = err
		}
	}

	if err := writeFileAtomic(path, data, 0o644); err != nil {
		return err
	}

	if err := i.run.Run(ctx, "bootstrap", "system", path); err != nil {
		// 回滚：删掉文件，避免「安装失败但开机仍会加载」。
		if rmErr := os.Remove(path); rmErr != nil && !os.IsNotExist(rmErr) {
			return fmt.Errorf("launchd: bootstrap 失败(%v)，且清理 plist 也失败(%v)，需手工删除 %s",
				err, rmErr, path)
		}
		return fmt.Errorf("launchd: 加载任务失败: %w", err)
	}
	return nil
}

// Remove 卸载并删除 plist。
//
// 即使 bootout 失败也要继续删文件：多数情况下失败原因是「本来就没加载」，
// 而残留文件才是真正的问题（下次开机会被自动加载）。
func (i *Installer) Remove(ctx context.Context, label string) error {
	// 安全边界：本应用自身的服务（com.macpanel.agent / com.macpanel.server）
	// 与计划任务共用 LabelPrefix，但它们不是任务。这里直接拒绝，
	// 让任何调用方都不可能误删本应用自己的 LaunchDaemon 定义。
	if !strings.HasPrefix(label, LabelPrefix+"task.") {
		return fmt.Errorf("launchd: 拒绝删除非任务 label %q", label)
	}

	path, err := i.plistPath(label)
	if err != nil {
		return err
	}

	if i.dryRun {
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("launchd: 删除 plist 失败: %w", err)
		}
		return nil
	}

	bootoutErr := i.run.Run(ctx, "bootout", "system/"+label)

	rmErr := os.Remove(path)
	if rmErr != nil && !os.IsNotExist(rmErr) {
		return fmt.Errorf("launchd: 删除 plist 失败: %w", rmErr)
	}
	if rmErr != nil && bootoutErr != nil {
		// 文件和加载状态都不存在 → 本来就没安装，不算错误。
		return nil
	}
	if bootoutErr != nil {
		// 文件删掉了，但服务可能仍在运行。明确告知而不是假装成功。
		return fmt.Errorf("launchd: plist 已删除，但卸载运行中的任务失败（可能需重启才能完全生效）: %w", bootoutErr)
	}
	return nil
}

// ListManaged 列出安装目录下属于本应用的 label。
//
// 只返回带 LabelPrefix 的文件：目录里可能有其它程序（甚至系统）的任务，
// 全都读出来既没必要也可能误导用户。
//
// 注意：返回仅代表「文件存在」。是否加载需逐个查 State。
func (i *Installer) ListManaged() ([]string, error) {
	entries, err := os.ReadDir(i.dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("launchd: 读取目录失败: %w", err)
	}

	out := make([]string, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if !strings.HasSuffix(name, ".plist") {
			continue
		}
		label := strings.TrimSuffix(name, ".plist")
		if strings.HasPrefix(label, LabelPrefix) {
			out = append(out, label)
		}
	}
	return out, nil
}

// validatePlist 用 plutil 校验 plist 语法。
//
// plutil -lint 只解析不安装，是安全操作。为什么必须做：
// plist 语法错误时 launchd 会静默忽略该任务，用户看到的现象是
// 「设置成功但从不执行」——这种静默失败最难排查。
func validatePlist(ctx context.Context, data []byte) error {
	// 写临时文件供 plutil 读取。放在系统临时目录，
	// 文件名带固定前缀便于排查残留。
	f, err := os.CreateTemp("", "hlm-plist-*.plist")
	if err != nil {
		return fmt.Errorf("launchd: 创建临时文件失败: %w", err)
	}
	tmpPath := f.Name()
	defer func() { _ = os.Remove(tmpPath) }()

	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		return fmt.Errorf("launchd: 写入临时文件失败: %w", err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("launchd: 关闭临时文件失败: %w", err)
	}

	res, err := execx.Run(ctx, 10*time.Second, PlutilPath, "-lint", tmpPath)
	if err != nil {
		return fmt.Errorf("launchd: plist 语法校验失败: %w（输出: %s）", err, trimOutput(res.Stdout+res.Stderr))
	}
	return nil
}

// writeFileAtomic 原子写入文件。
//
// 先写临时文件再 rename：若直接写目标文件，launchd 可能读到写了一半的
// plist（尤其是开机瞬间与安装并发时），结果是加载一个不完整的任务。
func writeFileAtomic(path string, data []byte, perm os.FileMode) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("launchd: 创建目录 %s 失败: %w", dir, err)
	}

	tmp, err := os.CreateTemp(dir, ".hlm-tmp-*.plist")
	if err != nil {
		return fmt.Errorf("launchd: 创建临时文件失败: %w", err)
	}
	tmpPath := tmp.Name()

	cleanup := func() {
		_ = tmp.Close()
		_ = os.Remove(tmpPath)
	}

	if _, err := tmp.Write(data); err != nil {
		cleanup()
		return fmt.Errorf("launchd: 写入 plist 失败: %w", err)
	}
	if err := tmp.Chmod(perm); err != nil {
		cleanup()
		return fmt.Errorf("launchd: 设置 plist 权限失败: %w", err)
	}
	// fsync 后再 rename：否则掉电可能留下「文件名对但内容为空」的 plist。
	if err := tmp.Sync(); err != nil {
		cleanup()
		return fmt.Errorf("launchd: 同步 plist 失败: %w", err)
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("launchd: 关闭 plist 失败: %w", err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("launchd: 替换 plist 失败: %w", err)
	}
	return nil
}

func trimOutput(s string) string {
	const max = 300
	if len(s) <= max {
		return s
	}
	return s[:max] + "..."
}
