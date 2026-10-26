package main

import (
	"fmt"
	"log/slog"
	"os"

	"github.com/lancelot-lewis/MacPanel/internal/agent"
	"github.com/lancelot-lewis/MacPanel/internal/database"
	"github.com/lancelot-lewis/MacPanel/internal/platform/darwin/services"
	"github.com/lancelot-lewis/MacPanel/internal/servicestore"
)

// serviceWiring 汇总 M4 能力的装配结果。
type serviceWiring struct {
	deps        *agent.ServiceDeps
	enabled     bool
	disabledWhy string
}

// wireServices 装配启动项管理能力。
//
// 分层策略：
//   - 扫描与展示：总是启用（只读、无副作用，是诊断的基础能力）；
//   - 启停操作：仅 root 启用。launchctl bootstrap/bootout 需要 root，
//     非 root 时装配 Controller 只会让用户点了才发现失败。
//     因此这里不装配，并通过 ControlDisabledReason 明确告知原因。
func wireServices(log *slog.Logger, db *database.DB) serviceWiring {
	if db == nil {
		return serviceWiring{disabledWhy: "数据库不可用"}
	}

	deps := &agent.ServiceDeps{
		Scanner: services.NewScanner(),
		Store:   servicestore.New(db),
	}

	if os.Geteuid() == 0 {
		deps.Controller = services.NewController()
		log.Info("启动项启停能力已启用（root）")
	} else {
		deps.ControlDisabledReason = fmt.Sprintf(
			"当前进程不是 root（euid=%d），无法启停系统服务；服务列表仍可查看",
			os.Geteuid())
		log.Warn("启动项启停能力未启用", "reason", deps.ControlDisabledReason)
	}

	return serviceWiring{deps: deps, enabled: true}
}
