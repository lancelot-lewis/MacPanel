// Command macpanel 是 MacPanel 管理面板的统一入口。
//
// 子命令：
//
//	server   非特权 Web 服务（含内嵌前端）
//	agent    Root 特权 Agent（Unix Socket RPC）
//	status   通过 CLI 查询 Agent 状态（不需要浏览器）
//	firewall 防火墙状态与本地回滚（不依赖 Agent 与浏览器，用于网络失联时救援）
//	token    生成 / 安装 IPC 共享令牌（安装器与本地开发用）
//	config   查看 / 修改持久化配置（server.conf）
//	version  打印版本
//	task-run 由 launchd 触发的计划任务入口（非交互 humans 勿用）
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
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
	case "task-run":
		return runTaskRun(rest)
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
	case "firewall":
		return runFirewallCLI(rest)
	case "help", "--help", "-h":
		printUsage()
		return nil
	default:
		printUsage()
		return fmt.Errorf("未知子命令: %s", cmd)
	}
}

func printUsage() {
	fmt.Print(`macpanel — MacPanel 管理面板（macOS）

用法:
  macpanel <子命令> [选项]

子命令:
  server      启动 Web 服务（默认仅监听 127.0.0.1，非特权）
  agent       启动 Root 特权 Agent（提供 Unix Socket RPC）
  status      查询 Agent 状态（命令行方式，无需浏览器）
  token       生成并写入 IPC 共享令牌
  config      查看 / 修改持久化配置（server.conf）
  version     打印版本信息
  firewall    防火墙状态查看与本地回滚（不经 IPC，救援用）

config 用法:
  macpanel config                              打印生效配置（含配置文件路径）
  macpanel config set listen 0.0.0.0:7788      监听所有网卡（自动开启 allow_remote）
  macpanel config set allow-remote true        允许非回环访问
  macpanel config unset listen                  删除该项，回落默认值
  改完需重启服务：sudo launchctl kickstart -k system/com.macpanel.server

环境变量（优先级高于配置文件）:
  MACPANEL_LISTEN_ADDR        监听地址，默认 127.0.0.1:7788
  MACPANEL_ALLOW_REMOTE       是否允许非回环访问（true/false，默认 false）
  MACPANEL_SOCKET_PATH        Agent Socket 路径
  MACPANEL_TOKEN_PATH         IPC 令牌文件路径
  MACPANEL_DATA_DIR           数据目录（SQLite）
  MACPANEL_LOG_DIR            日志目录
  MACPANEL_ANCHOR_PATH        PF anchor 规则文件路径
  MACPANEL_ALLOWED_PEER_UIDS  额外允许的 IPC 对端 UID（逗号分隔）
  MACPANEL_LOG_LEVEL          日志级别（debug/info/warn/error）
  MACPANEL_USE_FAKE_COLLECTOR 设为 1 时使用模拟采集器（仅用于开发/演示）

配置文件:
  /Library/Application Support/MacPanel/server.conf 保存监听地址与远程访问开关。
  pkg 升级不会覆盖它。开启非回环监听后，所有访问（含本机）都需要登录，
  因此必须先在本机设置管理密码。
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
		Level:     envOr("MACPANEL_LOG_LEVEL", "info"),
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

	// 监听非回环地址时，把「从哪进」和「能不能登进来」写进日志。
	// 这两个坑都只能靠日志暴露：面板打不开时用户看不到 UI 提示，
	// 而未设密码时页面能打开、登录必然失败。
	if !cfg.IsLoopbackListen() {
		if url := cfg.LoopbackURL(); url != "" {
			log.Warn("监听非回环地址，所有访问（含本机）都需要登录",
				"addr", cfg.NormalizedListenAddr(), "localURL", url)
		} else {
			log.Warn("监听非回环地址且不含回环，127.0.0.1 不可用",
				"addr", cfg.NormalizedListenAddr())
		}
		if has, err := authMgr.HasPassword(context.Background()); err == nil && !has {
			log.Warn("尚未设置管理密码：远程访问无法登录，请先在本机打开面板完成初始密码设置")
		}
	}

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
			if !cfg.IsLoopbackListen() && !cfg.IsWildcardListen() {
				return fmt.Errorf("HTTP 服务异常退出: %w（监听 %s；地址不存在会导致 bind 失败，"+
					"可用 macpanel config set listen 修改）", err, cfg.NormalizedListenAddr())
			}
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
		Level:     envOr("MACPANEL_LOG_LEVEL", "info"),
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
	if envOr("MACPANEL_USE_FAKE_COLLECTOR", "") == "1" {
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
		SocketGID:       cfg.SocketGID,
		Logger:          log,
		Timeout:         cfg.IPCTimeout,
	})
	if err != nil {
		return err
	}

	// 装配 M2 防火墙与 M3 电源/调度能力。
	//
	// 两者共用同一个数据库连接池。失败都只降级不阻断启动——
	// Agent 起不来会让整个面板失联，比部分功能不可用严重得多。
	var fwWiring firewallWiring
	var pwWiring powerWiring
	var svWiring serviceWiring
	var dgWiring diagWiring
	if db, derr := database.Open(context.Background(), cfg.DatabasePath()); derr != nil {
		log.Warn("防火墙与电源能力不可用：数据库打开失败", "error", derr)
	} else {
		defer db.Close()

		fwWiring = wireFirewall(context.Background(), cfg, log, db, *devMode)
		if fwWiring.enabled {
			ag.SetFirewallDeps(fwWiring.deps)
			log.Info("防火墙读写能力已启用")
		} else {
			log.Warn("防火墙读写能力未启用", "reason", fwWiring.disabledWhy)
		}

		pwWiring = wirePower(context.Background(), cfg, log, db, *devMode)
		if pwWiring.enabled {
			ag.SetPowerDeps(pwWiring.deps)
			log.Info("电源与计划任务能力已启用")
		} else {
			log.Warn("电源与计划任务能力未启用", "reason", pwWiring.disabledWhy)
		}

		svWiring = wireServices(log, db)
		if svWiring.enabled {
			ag.SetServiceDeps(svWiring.deps)
		} else {
			log.Warn("启动项管理能力未启用", "reason", svWiring.disabledWhy)
		}
	}

	// 诊断能力不依赖数据库，放在 db 分支之外：即使数据库打不开，
	// 网络诊断仍应可用（它恰恰是排查环境问题的工具）。
	dgWiring = wireDiag(log)
	if dgWiring.enabled {
		ag.SetDiagDeps(dgWiring.deps)
	}

	ag.RegisterAll(srv)

	if err := srv.Listen(); err != nil {
		return err
	}
	defer srv.Close()

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	// 调度器在 ctx 就绪后启动：它要跑后台 ticker，
	// 并且恢复待执行重启需要能感知关闭信号。
	if pwWiring.enabled && pwWiring.deps != nil {
		if err := pwWiring.deps.Scheduler.Start(ctx); err != nil {
			log.Warn("启动调度器失败", "error", err)
		}
		defer pwWiring.deps.Scheduler.Stop()
	}

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
		fmt.Printf("  期望状态: %s，可写: %v\n", fw.Managed, fw.Writable)
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
	devMode := fs.Bool("dev", false, "开发模式：读写 ./.dev 下的配置")
	if err := fs.Parse(args); err != nil {
		return err
	}

	// 子命令在标志之后：macpanel config [-dev] [show|set|unset] ...
	if rest := fs.Args(); len(rest) > 0 {
		switch rest[0] {
		case "set":
			return runConfigSet(*devMode, rest[1:])
		case "unset":
			return runConfigUnset(*devMode, rest[1:])
		case "show":
		default:
			return fmt.Errorf("未知 config 子命令 %q（支持 show / set / unset）", rest[0])
		}
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
		"configFile":      cfg.ConfigFile,
		"listenAddr":      cfg.NormalizedListenAddr(),
		"allowRemote":     cfg.AllowRemote,
		"loopbackURL":     cfg.LoopbackURL(),
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

// serverConfPath 返回要读写的 server.conf 路径。
func serverConfPath(dev bool) (string, error) {
	if !dev {
		return config.SystemServerConfPath, nil
	}
	base, err := os.Getwd()
	if err != nil {
		return "", err
	}
	return filepath.Join(base, ".dev", "server.conf"), nil
}

// runConfigSet 修改并持久化一项 server.conf 配置。
//
// 只改文件、不重启服务：重启由用户显式执行，避免「敲完命令服务突然断开」
// 这种难以归因的行为（尤其当用户正通过局域网访问面板时）。
func runConfigSet(dev bool, argv []string) error {
	if len(argv) < 2 {
		return errors.New("用法: macpanel config set <listen|allow-remote|public-hosts> <值>")
	}
	key := argv[0]
	value := strings.Join(argv[1:], " ")

	path, err := serverConfPath(dev)
	if err != nil {
		return err
	}
	vals, err := config.ParseConfigFile(path)
	if err != nil {
		return err
	}

	autoRemote := false
	switch key {
	case "listen":
		// 先只校验地址格式（allowRemote 传 true 表示「不在这里判断远程开关」）。
		if err := config.ValidateListenAddr(value, true); err != nil {
			return err
		}
		vals.Listen = value
		// 非回环监听必须同时开 allow_remote，否则启动直接失败。
		// 这里自动打开并明确告知，比让用户先撞一次启动错误更省事。
		if !config.IsLoopbackAddr(value) && (vals.AllowRemote == nil || !*vals.AllowRemote) {
			on := true
			vals.AllowRemote = &on
			autoRemote = true
		}
	case "allow-remote":
		b, err := strconv.ParseBool(value)
		if err != nil {
			return fmt.Errorf("allow-remote 只接受 true 或 false，收到 %q", value)
		}
		vals.AllowRemote = &b
	case "public-hosts":
		vals.PublicHosts = splitHostList(value)
	default:
		return fmt.Errorf("未知配置项 %q（支持 listen / allow-remote / public-hosts）", key)
	}

	return writeServerConf(path, vals, autoRemote)
}

// runConfigUnset 删除一项配置，使其回落到默认值。
func runConfigUnset(dev bool, argv []string) error {
	if len(argv) < 1 {
		return errors.New("用法: macpanel config unset <listen|allow-remote|public-hosts>")
	}
	key := argv[0]

	path, err := serverConfPath(dev)
	if err != nil {
		return err
	}
	vals, err := config.ParseConfigFile(path)
	if err != nil {
		return err
	}

	switch key {
	case "listen":
		vals.Listen = ""
	case "allow-remote":
		vals.AllowRemote = nil
	case "public-hosts":
		vals.PublicHosts = nil
	default:
		return fmt.Errorf("未知配置项 %q（支持 listen / allow-remote / public-hosts）", key)
	}
	return writeServerConf(path, vals, false)
}

// writeServerConf 校验并落盘，然后打印生效所需的手动步骤。
func writeServerConf(path string, vals config.FileValues, autoRemote bool) error {
	// 用真实配置跑一遍校验：不合法就拒绝写入，避免留下
	// 「文件写好了但服务起不来」这种只能靠读日志才能发现的状态。
	check := config.Defaults()
	check.Mode = config.ModeServer
	if vals.Listen != "" {
		check.ListenAddr = vals.Listen
	}
	if vals.AllowRemote != nil {
		check.AllowRemote = *vals.AllowRemote
	}
	if vals.PublicHosts != nil {
		check.PublicHosts = vals.PublicHosts
	}
	if err := check.Validate(); err != nil {
		return err
	}

	if err := config.WriteConfigFile(path, vals); err != nil {
		return err
	}
	_ = os.Chmod(path, 0o640)
	fixConfigOwnership(path)

	fmt.Printf("已写入 %s\n", path)
	if autoRemote {
		fmt.Println("已同时开启 allow_remote（监听非回环地址的必要条件）。")
	}

	if !check.IsLoopbackListen() {
		fmt.Println()
		fmt.Println("注意：远程访问要求登录，所有请求（含本机）都必须先登录。")
		if url := check.LoopbackURL(); url != "" {
			fmt.Printf("      尚未设置管理密码时，请先在本机打开 %s 完成设置。\n", url)
		} else {
			fmt.Println("      ⚠ 该地址不含回环，本机将无法通过 127.0.0.1 访问。")
			fmt.Println("        若尚未设置管理密码，改完会同时失去「本机设置初始密码」的入口。")
			fmt.Printf("        需要回滚：sudo macpanel config set listen 127.0.0.1:7788\n")
		}
	}

	fmt.Println()
	fmt.Println("重启服务使其生效：")
	fmt.Println("  sudo launchctl kickstart -k system/com.macpanel.server")
	return nil
}

// fixConfigOwnership 把配置文件的属组改为 _macpanel。
//
// Server 以 _macpanel 运行，需要读这个文件；root 写出来的文件属组是 wheel，
// 只设权限位是 0640 也不够。与 ipc.token 是同一类问题。
func fixConfigOwnership(path string) {
	if os.Geteuid() != 0 {
		return
	}
	g, err := user.LookupGroup("_macpanel")
	if err != nil {
		return
	}
	gid, err := strconv.Atoi(g.Gid)
	if err != nil {
		return
	}
	if err := os.Chown(path, 0, gid); err != nil {
		fmt.Printf("警告: 无法把 %s 的属组改为 _macpanel: %v\n", path, err)
	}
}

func splitHostList(raw string) []string {
	parts := strings.Split(raw, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// ---- helpers ----

// loadConfig 加载配置；dev 模式下把路径改到当前目录的 .dev 下。
//
// 配置文件优先于默认值、低于环境变量：监听地址这类「用户设定且必须
// 在 pkg 升级后保留」的值放文件里，plist 只保留不随用户意图变化的环境。
func loadConfig(mode config.Mode, dev bool) (config.Config, error) {
	confPath, err := serverConfPath(dev)
	if err != nil {
		return config.Config{}, err
	}
	cfg, err := config.Load(config.LoadOptions{Mode: mode, ConfigFile: confPath})
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
