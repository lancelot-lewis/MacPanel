package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"

	"github.com/lancelot-lewis/MacPanel/internal/agent"
	"github.com/lancelot-lewis/MacPanel/internal/audit"
	"github.com/lancelot-lewis/MacPanel/internal/config"
	"github.com/lancelot-lewis/MacPanel/internal/database"
	"github.com/lancelot-lewis/MacPanel/internal/platform"
	"github.com/lancelot-lewis/MacPanel/internal/platform/darwin"
	"github.com/lancelot-lewis/MacPanel/internal/platform/darwin/launchd"
	"github.com/lancelot-lewis/MacPanel/internal/platform/fake"
	"github.com/lancelot-lewis/MacPanel/internal/tasksched"
	"github.com/lancelot-lewis/MacPanel/internal/taskstore"
)

// powerWiring 汇总 M3 能力的装配结果。
type powerWiring struct {
	deps        *agent.PowerDeps
	enabled     bool
	disabledWhy string
}

// wirePower 装配电源与计划任务能力。
//
// 三种运行形态：
//  1. root + 非 dev：真实 launchd + 真实重启。生产路径。
//  2. 非 root + 非 dev：不启用。既装不了 LaunchDaemon 也无法重启，
//     与其给一个「看起来能点但永远失败」的界面，不如明确说明原因。
//  3. dev：dry-run。plist 写到 ./.dev/launchd，重启只记录不执行，
//     用于完整验证同步逻辑与界面。
func wirePower(
	ctx context.Context,
	cfg config.Config,
	log *slog.Logger,
	db *database.DB,
	devMode bool,
) powerWiring {
	isRoot := os.Geteuid() == 0

	if !devMode && !isRoot {
		return powerWiring{
			disabledWhy: "需要以 root 运行 Agent 才能安装系统级任务并执行重启",
		}
	}

	binPath, err := os.Executable()
	if err != nil {
		return powerWiring{disabledWhy: fmt.Sprintf("无法确定自身可执行路径: %v", err)}
	}
	// 解析符号链接：launchd 用这个路径启动，指向 symlink 时
	// 若目标被替换会导致新旧版本混用。
	if resolved, err := filepath.EvalSymlinks(binPath); err == nil {
		binPath = resolved
	}

	var installer *launchd.Installer
	var power platform.PowerController
	var extraArgs []string

	if devMode {
		// 必须是绝对路径：launchd 的 StandardOutPath 与 plist 路径
		// 都要求绝对路径，而 launchd 启动的进程 cwd 是 /。
		// 相对路径会导致 Spec 校验失败，且失败发生在「任务已入库」之后，
		// 表现为「创建成功但从不执行」——很难排查。
		launchdDir, err := filepath.Abs(filepath.Join(".dev", "launchd"))
		if err != nil {
			return powerWiring{disabledWhy: fmt.Sprintf("解析开发用 launchd 目录失败: %v", err)}
		}
		if err := os.MkdirAll(launchdDir, 0o755); err != nil {
			return powerWiring{disabledWhy: fmt.Sprintf("创建开发用 launchd 目录失败: %v", err)}
		}
		installer = launchd.NewDryRunInstaller(launchdDir)
		power = fake.NewPower()
		extraArgs = []string{"--dev"}
		log.Warn("电源与调度处于 dry-run：plist 只写文件、重启只记录，不会真的执行")
	} else {
		installer = launchd.NewInstaller("", launchd.NewExecRunner())
		power = darwin.NewPower()
	}

	logDir := cfg.LogDir
	if devMode {
		absLogDir, err := filepath.Abs(filepath.Join(".dev", "logs"))
		if err != nil {
			return powerWiring{disabledWhy: fmt.Sprintf("解析任务日志目录失败: %v", err)}
		}
		logDir = absLogDir
		if err := os.MkdirAll(logDir, 0o755); err != nil {
			return powerWiring{disabledWhy: fmt.Sprintf("创建任务日志目录失败: %v", err)}
		}
	} else {
		// 生产环境的任务日志目录由安装器创建，这里只确保存在。
		if err := os.MkdirAll(logDir, 0o755); err != nil {
			log.Warn("创建任务日志目录失败", "dir", logDir, "error", err)
		}
	}

	sched, err := tasksched.New(tasksched.Options{
		Store:     taskstore.New(db),
		Installer: installer,
		Power:     power,
		Auditor:   audit.NewStore(db, log),
		Logger:    log,
		BinPath:   binPath,
		LogDir:    logDir,
		ExtraArgs: extraArgs,
	})
	if err != nil {
		return powerWiring{disabledWhy: fmt.Sprintf("构造调度器失败: %v", err)}
	}

	return powerWiring{deps: &agent.PowerDeps{Scheduler: sched}, enabled: true}
}
