package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/lancelot-lewis/MacPanel/internal/audit"
	"github.com/lancelot-lewis/MacPanel/internal/config"
	"github.com/lancelot-lewis/MacPanel/internal/database"
	"github.com/lancelot-lewis/MacPanel/internal/logger"
	"github.com/lancelot-lewis/MacPanel/internal/platform"
	"github.com/lancelot-lewis/MacPanel/internal/platform/darwin"
	"github.com/lancelot-lewis/MacPanel/internal/platform/darwin/launchd"
	"github.com/lancelot-lewis/MacPanel/internal/platform/fake"
	"github.com/lancelot-lewis/MacPanel/internal/tasksched"
	"github.com/lancelot-lewis/MacPanel/internal/taskstore"
)

// runTaskRun 执行一次计划任务。由 launchd 按 StartCalendarInterval 触发。
//
// 为什么是独立子命令而不是让常驻 Agent 自己计时：
//   - launchd 是系统级调度，Agent 被停掉时任务仍会按时触发；
//   - 每次执行都是全新进程，不会因为 Agent 长期运行而累积状态；
//   - 退出码直接反映结果，launchd 的日志更可读。
//
// 本命令由 launchd 以 root 启动，因此能真正执行重启。
// 它的输出会被重定向到 /Library/Logs/HomeLabManager/task-<id>.log。
func runTaskRun(args []string) error {
	fs := flag.NewFlagSet("task-run", flag.ContinueOnError)
	taskID := fs.Int64("task", 0, "要执行的任务 ID")
	devMode := fs.Bool("dev", false, "开发模式：使用 ./.dev 下的数据目录，且不真的重启")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *taskID <= 0 {
		return fmt.Errorf("必须通过 --task 指定任务 ID")
	}

	cfg, err := loadConfig(config.ModeAgent, *devMode)
	if err != nil {
		return err
	}

	log, cleanup, err := logger.New(logger.Options{
		Level:     envOr("HLM_LOG_LEVEL", "info"),
		Dir:       cfg.LogDir,
		Component: fmt.Sprintf("task-%d", *taskID),
	})
	if err != nil {
		return fmt.Errorf("初始化日志失败: %w", err)
	}
	defer cleanup()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	db, err := database.Open(ctx, cfg.DatabasePath())
	if err != nil {
		return fmt.Errorf("打开数据库失败: %w", err)
	}
	defer db.Close()

	var power platform.PowerController
	var installer *launchd.Installer
	extraArgs := []string{}
	logDir := cfg.LogDir

	if *devMode {
		// 开发模式：绝不真的重启。这个开关是硬的，不依赖权限判断，
		// 避免开发机上误触发真实重启。
		power = fake.NewPower()
		installer = launchd.NewDryRunInstaller(filepath.Join(os.TempDir(), "hlm-taskrun-dev"))
		extraArgs = []string{"--dev"}
	} else {
		power = darwin.NewPower()
		installer = launchd.NewInstaller("", launchd.NewExecRunner())
	}

	sched, err := tasksched.New(tasksched.Options{
		Store:     taskstore.New(db),
		Installer: installer,
		Power:     power,
		Auditor:   audit.NewStore(db, log),
		Logger:    log,
		BinPath:   selfPath(),
		LogDir:    logDir,
		ExtraArgs: extraArgs,
	})
	if err != nil {
		return err
	}

	exec, err := sched.RunTask(ctx, *taskID)
	if err != nil {
		// 输出到标准错误：launchd 会把它写进 task-<id>.err.log，
		// 便于用户直接看到「为什么没执行」。
		fmt.Fprintf(os.Stderr, "任务 %d 执行失败: %v\n", *taskID, err)
		return err
	}

	fmt.Printf("任务 %d（%s）结果: %s\n", exec.TaskID, exec.TaskName, exec.Result)
	if exec.Detail != "" {
		fmt.Printf("说明: %s\n", exec.Detail)
	}

	// 跳过不算失败：退出码为 0 可以让 launchd 不把日志标成 error。
	// 真正的失败已在上面通过返回 error 体现。
	return nil
}

// selfPath 返回本可执行文件的绝对路径（解析符号链接）。
func selfPath() string {
	exe, err := os.Executable()
	if err != nil {
		return "homelab-manager"
	}
	if resolved, err := filepath.EvalSymlinks(exe); err == nil {
		return resolved
	}
	return exe
}
