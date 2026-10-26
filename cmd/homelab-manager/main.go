// Command homelab-manager 是 HomeLab 管理面板的统一入口。
//
// 子命令：
//
//	server  非特权 Web 服务（含内嵌前端）
//	agent   Root 特权 Agent（Unix Socket RPC）
//	status  通过 CLI 查询 Agent 状态（不需要浏览器）
//	token   生成 / 安装 IPC 共享令牌（安装器与本地开发用）
//	config  打印生效配置（排查配置问题）
//	version 打印版本
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/lancelot-lewis/MacPanel/internal/agent"
	"github.com/lancelot-lewis/MacPanel/internal/app"
	"github.com/lancelot-lewis/MacPanel/internal/audit"
	"github.com/lancelot-lewis/MacPanel/internal/auth"
	"github.com/lancelot-lewis/MacPanel/internal/config"
	"github.com/lancelot-lewis/MacPanel/internal/database"
	"github.com/lancelot-lewis/MacPanel/internal/ipc"
	"github.com/lancelot-lewis/MacPanel/internal/logger"
	"github.com/lancelot-lewis/MacPanel/internal/platform"
	"github.com/lancelot-lewis/MacPanel/internal/platform/darwin"
	"github.com/lancelot-lewis/MacPanel/internal/platform/fake"
	"github.com/lancelot-lewis/MacPanel/internal/server"
	"github.com/lancelot-lewis/MacPanel/internal/version"
	"github.com/lancelot-lewis/MacPanel/web"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintf(os.Stderr, "错误: %v\n", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	if len(args) == 0 {
		printUsage()
		return errors.New("缺少子命令")
	}

	cmd := args[0]
	rest := args[1:]

	switch cmd {
	case "server":
		return runServer(rest)
	case "agent":
		return runAgent(rest)
	case "status":
		return runStatus(rest)
	case "token":
		return runToken(rest)
	case "config":
		return runConfig(rest)
	case "version", "--version", "-v":
		fmt.Println(version.Current().String())
		return nil
	case "help", "--help", "-h":
		printUsage()
		return nil
	default:
		printUsage()
		return fmt.Errorf("未知子命令: %s", cmd)
	}
}

func printUsage() {
	fmt.Print(`homelab-manager — macOS HomeLab 管理面板

用法:
  homelab-manager <子命令> [选项]

子命令:
  server      启动 Web 服务（默认仅监听 127.0.0.1，非特权）
  agent       启动 Root 特权 Agent（提供 Unix Socket RPC）
  status      查询 Agent 状态（命令行方式，无需浏览器）
  token       生成并写入 IPC 共享令牌
  config      打印生效配置并校验
  version     打印版本信息

环境变量:
  HLM_LISTEN_ADDR        监听地址，默认 127.0.0.1:7788
  HLM_ALLOW_REMOTE       是否允许远程访问（true/false，默认 false）
  HLM_SOCKET_PATH        Agent Socket 路径
  HLM_TOKEN_PATH         IPC 令牌文件路径
  HLM_DATA_DIR           数据目录（SQLite）
  HLM_LOG_DIR            日志目录
  HLM_ANCHOR_PATH        PF anchor 规则文件路径
  HLM_ALLOWED_PEER_UIDS  额外允许的 IPC 对端 UID（逗号分隔）
  HLM_LOG_LEVEL          日志级别（debug/info/warn/error）
  HLM_USE_FAKE_COLLECTOR 设为 1 时使用模拟采集器（仅用于开发/演示）
`)
}

// ---- server ----

func runServer(args []string) error {
	fs := flag.NewFlagSet("server", flag.ContinueOnError)
	devMode := fs.Bool("dev", false, "开发模式：数据与日志写到 ./.dev 下")
	if err := fs.Parse(args); err != nil {
		return err
	}

	cfg, err := loadConfig(config.ModeServer, *devMode)
	if err != nil {
		return err
	}

	log, cleanup, err := logger.New(logger.Options{
		Level:     envOr("HLM_LOG_LEVEL", "info"),
		Dir:       cfg.LogDir,
		Component: "server",
	})
	if err != nil {
		return fmt.Errorf("初始化日志失败: %w", err)
	}
	defer cleanup()

	db, err := database.Open(context.Background(), cfg.DatabasePath())
	if err != nil {
		return err
	}
	defer db.Close()

	client, err := ipc.NewClientFromFiles(cfg)
	if err != nil {
		// Server 可以在 Agent 不可用时启动：UI 会显示 Agent 不可达，
		// 这比「整个面板起不来」更利于排障。但需要明确告警。
		log.Warn("无法加载 IPC 令牌，Agent 调用将不可用", "error", err)
	}

	var svc *app.Service
	if client != nil {
		svc = app.NewService(client)
	} else {
		svc = app.NewService(unavailableCaller{})
	}

	authMgr := auth.NewManager(db)
	auditStore := audit.NewStore(db, log)

	webFS, webErr := web.DistFS()
	if webErr != nil {
		log.Warn("未找到内嵌前端资源，UI 不可用（请先构建前端）", "error", webErr)
		webFS = nil
	}

	srv := server.New(server.Options{
		Config:      cfg,
		Service:     svc,
		Audit:       auditStore,
		Auth:        authMgr,
		Logger:      log,
		WebFS:       webFS,
		PublicHosts: cfg.PublicHosts,
	})

	httpServer := &http.Server{
		Addr:              cfg.NormalizedListenAddr(),
		Handler:           srv.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      60 * time.Second,
		IdleTimeout:       120 * time.Second,
		MaxHeaderBytes:    16 << 10,
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	// 定期清理过期会话，避免表无限增长。
	go purgeLoop(ctx, authMgr, log)

	errCh := make(chan error, 1)
	go func() {
		log.Info("Web 服务启动",
			"addr", httpServer.Addr,
			"mode", map[bool]string{true: "remote", false: "local"}[cfg.AllowRemote],
			"db", cfg.DatabasePath(),
			"version", version.Version,
		)
		if err := httpServer.ListenAndServe(); err != nil && !errors.Is(err, httpServerCloseErr) {
			errCh <- err
		}
		close(errCh)
	}()

	select {
	case <-ctx.Done():
		log.Info("收到退出信号，正在关闭")
	case err := <-errCh:
		if err != nil {
			return fmt.Errorf("HTTP 服务异常退出: %w", err)
		}
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), cfg.ShutdownTimeout)
	defer cancel()
	if err := httpServer.Shutdown(shutdownCtx); err != nil {
		log.Warn("关闭 HTTP 服务超时", "error", err)
	}
	return nil
}

var httpServerCloseErr = errors.New("http: Server closed")

func purgeLoop(ctx context.Context, mgr *auth.Manager, log interface {
	Warn(string, ...any)
}) {
	ticker := time.NewTicker(30 * time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := mgr.PurgeExpired(ctx); err != nil {
				log.Warn("清理过期会话失败", "error", err)
			}
		}
	}
}

// ---- agent ----

func runAgent(args []string) error {
	fs := flag.NewFlagSet("agent", flag.ContinueOnError)
	devMode := fs.Bool("dev", false, "开发模式：Socket 与日志写到 ./.dev 下")
	if err := fs.Parse(args); err != nil {
		return err
	}

	cfg, err := loadConfig(config.ModeAgent, *devMode)
	if err != nil {
		return err
	}

	log, cleanup, err := logger.New(logger.Options{
		Level:     envOr("HLM_LOG_LEVEL", "info"),
		Dir:       cfg.LogDir,
		Component: "agent",
	})
	if err != nil {
		return fmt.Errorf("初始化日志失败: %w", err)
	}
	defer cleanup()

	token, err := config.LoadToken(cfg.TokenPath)
	if err != nil {
		return err
	}

	var collector platform.Collector
	if envOr("HLM_USE_FAKE_COLLECTOR", "") == "1" {
		log.Warn("使用模拟采集器：所有状态数据均为假数据，仅用于开发")
		collector = fake.New()
	} else {
		collector = darwin.New(platform.DefaultConfig(cfg.AnchorName, cfg.AnchorPath))
	}

	ag, err := agent.New(agent.Options{Collector: collector, Logger: log})
	if err != nil {
		return err
	}

	srv, err := ipc.NewServer(ipc.ServerOptions{
		SocketPath:      cfg.SocketPath,
		Token:           token,
		AllowedPeerUIDs: cfg.AllowedPeerUIDs,
		Logger:          log,
		Timeout:         cfg.IPCTimeout,
	})
	if err != nil {
		return err
	}

	// 装配 M2 防火墙能力。失败时降级为「只读可用、写入不可用」，
	// 不阻断 Agent 启动——这比整体起不来更利于排障。
	var fwWiring firewallWiring
	if dbForFw, derr := database.Open(context.Background(), cfg.DatabasePath()); derr != nil {
		log.Warn("防火墙能力不可用：数据库打开失败", "error", derr)
	} else {
		defer dbForFw.Close()
		fwWiring = wireFirewall(context.Background(), cfg, log, dbForFw, *devMode)
		if fwWiring.enabled {
			ag.SetFirewallDeps(fwWiring.deps)
			log.Info("防火墙读写能力已启用")
		} else {
			log.Warn("防火墙读写能力未启用", "reason", fwWiring.disabledWhy)
		}
	}

	ag.RegisterAll(srv)

	if err := srv.Listen(); err != nil {
		return err
	}
	defer srv.Close()

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	log.Info("Agent 启动",
		"socket", cfg.SocketPath,
		"euid", ipc.CurrentEUID(),
		"methods", ag.Methods(),
		"version", version.Version,
	)

	// 运行期间不再需要 token 值，主动丢弃引用以减少泄漏面。
	_ = token

	return srv.Serve(ctx)
}

// ---- status ----

func runStatus(args []string) error {
	fs := flag.NewFlagSet("status", flag.ContinueOnError)
	asJSON := fs.Bool("json", false, "以 JSON 输出")
	devMode := fs.Bool("dev", false, "开发模式")
	if err := fs.Parse(args); err != nil {
		return err
	}

	cfg, err := loadConfig(config.ModeServer, *devMode)
	if err != nil {
		return err
	}

	client, err := ipc.NewClientFromFiles(cfg)
	if err != nil {
		return err
	}
	svc := app.NewService(client)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	health, healthErr := svc.AgentHealth(ctx)
	fw, fwErr := svc.FirewallStatus(ctx)
	ov, ovErr := svc.SystemOverview(ctx)

	if *asJSON {
		return printJSON(map[string]any{
			"agent":    health,
			"agentErr": errString(healthErr),
			"firewall": fw,
			"fwErr":    errString(fwErr),
			"system":   ov,
			"sysErr":   errString(ovErr),
		})
	}

	fmt.Println("== Agent ==")
	if healthErr != nil {
		fmt.Printf("  不可达: %v\n", healthErr)
	} else {
		fmt.Printf("  版本 %s  运行 %s  root=%v  方法数 %d\n",
			health.Version, time.Since(health.StartedAt).Round(time.Second), health.IsRoot, health.MethodCount)
	}

	fmt.Println("\n== 系统 ==")
	if ovErr != nil {
		fmt.Printf("  不可用: %v\n", ovErr)
	} else {
		fmt.Printf("  主机 %s  macOS %s (%s)  %s\n",
			ov.Hostname, ov.OS.ProductVersion, ov.OS.Build, ov.CPU.Model)
		if ov.CPU.LoadAverage != nil {
			fmt.Printf("  负载 %.2f / %.2f / %.2f\n",
				ov.CPU.LoadAverage.One, ov.CPU.LoadAverage.Five, ov.CPU.LoadAverage.Fifteen)
		}
		if ov.Memory.State == "ok" && ov.Memory.UsedBytes != nil {
			fmt.Printf("  内存 已用 %.1f GiB / 共 %.1f GiB\n",
				float64(*ov.Memory.UsedBytes)/(1<<30), float64(ov.Memory.TotalBytes)/(1<<30))
		} else {
			fmt.Println("  内存 不可用")
		}
		for _, d := range ov.Disks {
			fmt.Printf("  磁盘 %s (%s) 已用 %.1f%% 可用 %.1f GiB\n",
				d.MountPoint, d.FileSystem, d.UsedRatio*100, float64(d.FreeBytes)/(1<<30))
		}
		for _, m := range ov.Missing {
			fmt.Printf("  ! %s 不可用: %s\n", m.Metric, m.Reason)
		}
	}

	fmt.Println("\n== IPv6 防火墙 ==")
	if fwErr != nil {
		fmt.Printf("  不可用: %v\n", fwErr)
	} else {
		pfState := "未知"
		if fw.PF.Enabled != nil {
			if *fw.PF.Enabled {
				pfState = "已启用"
			} else {
				pfState = "已关闭"
			}
		}
		fmt.Printf("  PF 状态: %s（可读=%v）\n", pfState, fw.PF.ReadAvailable)
		if fw.PF.ReadError != "" {
			fmt.Printf("  读取限制: %s\n", fw.PF.ReadError)
		}
		fmt.Printf("  Anchor %s: %s（文件存在=%v 主配置引用=%v）\n",
			fw.Anchor.Name, fw.Anchor.State, fw.Anchor.Present, fw.Anchor.Referenced)
		fmt.Printf("  期望状态: %s，可写: %v（M1 只读）\n", fw.Managed, fw.Writable)
		for _, w := range fw.Warnings {
			fmt.Printf("  ! %s\n", w)
		}
	}
	return nil
}

// ---- token ----

func runToken(args []string) error {
	fs := flag.NewFlagSet("token", flag.ContinueOnError)
	outPath := fs.String("out", "", "输出路径（默认使用配置中的 TokenPath）")
	force := fs.Bool("force", false, "覆盖已存在的令牌文件")
	devMode := fs.Bool("dev", false, "开发模式")
	if err := fs.Parse(args); err != nil {
		return err
	}

	cfg, err := loadConfig(config.ModeAgent, *devMode)
	if err != nil {
		return err
	}
	path := cfg.TokenPath
	if *outPath != "" {
		path = *outPath
	}

	if _, err := os.Stat(path); err == nil && !*force {
		return fmt.Errorf("令牌文件已存在: %s（如需重新生成请加 -force，注意会导致 Server 与 Agent 凭据不一致）", path)
	}

	tok, err := config.GenerateToken()
	if err != nil {
		return err
	}
	if err := config.WriteTokenFile(path, tok); err != nil {
		return err
	}
	fmt.Printf("已生成 IPC 令牌: %s\n", path)
	fmt.Println("提示：该文件必须仅 root 可读（0600），Server 需以同一令牌运行。")
	return nil
}

// ---- config ----

func runConfig(args []string) error {
	fs := flag.NewFlagSet("config", flag.ContinueOnError)
	mode := fs.String("mode", "server", "server 或 agent")
	devMode := fs.Bool("dev", false, "开发模式")
	if err := fs.Parse(args); err != nil {
		return err
	}

	m := config.ModeServer
	if *mode == "agent" {
		m = config.ModeAgent
	}
	cfg, err := loadConfig(m, *devMode)
	if err != nil {
		return err
	}
	return printJSON(map[string]any{
		"mode":            cfg.Mode,
		"listenAddr":      cfg.NormalizedListenAddr(),
		"allowRemote":     cfg.AllowRemote,
		"socketPath":      cfg.SocketPath,
		"tokenPath":       cfg.TokenPath,
		"dataDir":         cfg.DataDir,
		"logDir":          cfg.LogDir,
		"anchorName":      cfg.AnchorName,
		"anchorPath":      cfg.AnchorPath,
		"ipcTimeout":      cfg.IPCTimeout.String(),
		"allowedPeerUIDs": cfg.AllowedPeerUIDs,
	})
}

// ---- helpers ----

// loadConfig 加载配置；dev 模式下把路径改到当前目录的 .dev 下。
func loadConfig(mode config.Mode, dev bool) (config.Config, error) {
	cfg, err := config.LoadFromEnv(mode)
	if err != nil {
		return cfg, err
	}
	if dev {
		base, err := os.Getwd()
		if err != nil {
			return cfg, err
		}
		root := filepath.Join(base, ".dev")
		cfg.DataDir = filepath.Join(root, "data")
		cfg.LogDir = filepath.Join(root, "logs")
		cfg.SocketPath = filepath.Join(root, "run", "agent.sock")
		cfg.TokenPath = filepath.Join(root, "run", "ipc.token")
		cfg.AnchorPath = filepath.Join(root, "pf.anchor")
	}
	if err := cfg.Validate(); err != nil {
		return cfg, err
	}
	return cfg, nil
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func errString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

func printJSON(v any) error {
	enc := newJSONEncoder(os.Stdout)
	return enc.Encode(v)
}

// unavailableCaller 在 Agent 客户端不可用时返回明确的不可用错误。
type unavailableCaller struct{}

func (unavailableCaller) Call(context.Context, string, any, any) error {
	return fmt.Errorf("Agent 客户端未初始化（缺少 IPC 令牌）")
}
