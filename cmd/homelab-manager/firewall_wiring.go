package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"github.com/lancelot-lewis/MacPanel/internal/agent"
	"github.com/lancelot-lewis/MacPanel/internal/config"
	"github.com/lancelot-lewis/MacPanel/internal/database"
	"github.com/lancelot-lewis/MacPanel/internal/firewallstore"
	"github.com/lancelot-lewis/MacPanel/internal/platform/darwin/firewalld"
)

// firewallWiring 汇总 M2 防火墙能力的装配结果。
type firewallWiring struct {
	deps        agent.FirewallDeps
	manager     *firewalld.Manager
	enabled     bool
	disabledWhy string
}

// wireFirewall 装配 M2 防火墙依赖。
//
// 装配失败时不让 Agent 启动失败，而是返回 enabled=false 与原因：
// 这样只读功能（系统概览、网络、PF 状态）仍可用，
// 前端能明确显示「防火墙写入不可用：<原因>」。
// 比「整个 Agent 起不来」更利于排障。
func wireFirewall(
	ctx context.Context,
	cfg config.Config,
	log *slog.Logger,
	db *database.DB,
	devMode bool,
) firewallWiring {
	// 非 macOS 直接跳过（darwin 实现有 build tag，这里用运行时判断）
	if _, err := os.Stat("/sbin/pfctl"); err != nil {
		return firewallWiring{
			enabled:     false,
			disabledWhy: "当前系统没有 pfctl（非 macOS 或系统异常）",
		}
	}

	store := firewallstore.New(db)

	// 状态文件放在 root 独占目录，与数据库分离：
	// 数据库可能被非特权账户改坏，回滚状态不能依赖它。
	stateDir := filepath.Dir(cfg.SocketPath)
	statePath := filepath.Join(stateDir, "firewall-state.json")
	stateStore, err := firewalld.NewFileStore(statePath)
	if err != nil {
		return firewallWiring{disabledWhy: fmt.Sprintf("状态存储初始化失败：%v", err)}
	}

	// dry-run：开发模式或非 root 时不真的调用 pfctl。
	// 这样开发环境能完整走通流程（含状态机与界面），但不会改动系统。
	dryRun := devMode || os.Geteuid() != 0

	applier, err := firewalld.NewPFExecutor(cfg.AnchorName, cfg.AnchorPath, dryRun)
	if err != nil {
		return firewallWiring{disabledWhy: fmt.Sprintf("PF 执行器初始化失败：%v", err)}
	}

	timeout := 60 * time.Second
	if v := os.Getenv("HLM_CONFIRM_TIMEOUT"); v != "" {
		if d, perr := time.ParseDuration(v); perr == nil {
			timeout = d
		} else {
			log.Warn("HLM_CONFIRM_TIMEOUT 解析失败，使用默认 60s", "value", v, "error", perr)
		}
	}

	manager, err := firewalld.New(firewalld.Options{
		Store:          stateStore,
		Applier:        applier,
		AnchorName:     cfg.AnchorName,
		RulePath:       cfg.AnchorPath,
		ConfirmTimeout: timeout,
		Logger:         slogAdapter{log},
	})
	if err != nil {
		return firewallWiring{disabledWhy: fmt.Sprintf("事务管理器初始化失败：%v", err)}
	}

	// 启动恢复：处理上次崩溃时残留的未确认配置。
	// 这一步必须在开始对外服务前完成，否则残留的 pending 状态
	// 会与新的应用请求冲突。
	recovered, err := manager.Recover(ctx)
	if err != nil {
		log.Error("防火墙状态恢复失败", "error", err)
		return firewallWiring{disabledWhy: fmt.Sprintf("状态恢复失败：%v", err)}
	}
	if recovered {
		log.Warn("已回滚上次残留的未确认配置")
	}

	if dryRun {
		log.Warn("防火墙写操作运行在 dry-run 模式：不会真正调用 pfctl",
			"reason", func() string {
				if devMode {
					return "开发模式"
				}
				return "非 root 用户"
			}())
	}

	return firewallWiring{
		enabled: true,
		manager: manager,
		deps: agent.FirewallDeps{
			Manager:          manager,
			Rules:            store,
			AnchorName:       cfg.AnchorName,
			RulePath:         cfg.AnchorPath,
			ConfirmTimeout:   timeout,
			GeneratorVersion: "m2-1",
		},
	}
}

// slogAdapter 把 *slog.Logger 适配为 firewalld.Logger。
//
// 单独写适配而不是让 firewalld 直接依赖 slog：
// 保持 firewalld 包对日志实现零依赖，便于测试与复用。
type slogAdapter struct{ l *slog.Logger }

func (s slogAdapter) Info(msg string, args ...any)  { s.l.Info(msg, args...) }
func (s slogAdapter) Warn(msg string, args ...any)  { s.l.Warn(msg, args...) }
func (s slogAdapter) Error(msg string, args ...any) { s.l.Error(msg, args...) }
