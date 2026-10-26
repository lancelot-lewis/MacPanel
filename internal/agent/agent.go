// Package agent 是 Root Agent 的实现：注册固定 RPC 方法并驱动 IPC 服务端。
//
// 设计约束（PLAN.md 第 4 节安全要求）：
//   - 只暴露固定方法名，不提供 exec / 任意文件读写 / 任意 plist 或 PF 语句执行；
//   - 每个方法自行校验参数，即便分发层已做基础校验；
//   - M1 阶段全部方法只读，任何写操作都返回「未实现」而不是假装成功。
package agent

import (
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"time"

	"github.com/lancelot-lewis/MacPanel/internal/ipc"
	"github.com/lancelot-lewis/MacPanel/internal/model"
	"github.com/lancelot-lewis/MacPanel/internal/platform"
	"github.com/lancelot-lewis/MacPanel/internal/version"
)

// 方法名常量。使用「域.动作」命名，便于前后端对照与审计检索。
const (
	MethodHealth         = "agent.health"
	MethodSystemOverview = "system.overview"
	MethodSystemNetwork  = "system.network"
	MethodFirewallStatus = "firewall.status"
)

// Agent 持有采集器与元信息，负责把方法注册到 IPC 服务端。
type Agent struct {
	collector platform.Collector
	log       *slog.Logger
	startedAt time.Time
	methods   []string
	// fw 是 M2 防火墙能力依赖。为 nil 时相关方法返回「未实现」。
	fw *FirewallDeps
	// power 是 M3 电源与调度能力依赖。为 nil 时相关方法返回「未实现」。
	power *PowerDeps
	// services 是 M4 启动项管理依赖。为 nil 时相关方法返回「未实现」。
	services *ServiceDeps
	// diag 是 M6 网络诊断与容器能力依赖。
	diag *DiagDeps
}

// Options 构造参数。
type Options struct {
	Collector platform.Collector
	Logger    *slog.Logger
}

// New 创建 Agent。
func New(opts Options) (*Agent, error) {
	if opts.Collector == nil {
		return nil, model.NewError(model.CodeInternal, "必须提供状态采集器")
	}
	if opts.Logger == nil {
		opts.Logger = slog.Default()
	}
	return &Agent{
		collector: opts.Collector,
		log:       opts.Logger,
		startedAt: time.Now().UTC(),
		// 方法列表的单一来源：全部在这里声明，RegisterAll 只负责注册。
		// 曾经的写法是构造时列 4 个、registerFirewall 再 append 8 个，
		// 导致 handleHealth 报出的方法与实际注册的方法可能不一致
		// （取决于是否调用过 RegisterAll）。统一到一处可消除这类漂移。
		methods: AllMethods(),
	}, nil
}

// AllMethods 返回本 Agent 支持的全部方法名。
//
// 这是方法清单的唯一来源：RegisterAll 按它注册，
// handleHealth 按它上报，测试按它校验。
func AllMethods() []string {
	return []string{
		// M1：只读
		MethodHealth,
		MethodSystemOverview,
		MethodSystemNetwork,
		MethodFirewallStatus,
		// M2：防火墙读写
		MethodFirewallRulesGet,
		MethodFirewallRulesSet,
		MethodFirewallPreview,
		MethodFirewallApply,
		MethodFirewallConfirm,
		MethodFirewallRollback,
		MethodFirewallTransaction,
		MethodFirewallTemplates,
		// M3：电源与计划任务
		MethodPowerStatus,
		MethodPowerReboot,
		MethodPowerCancel,
		MethodScheduleList,
		MethodScheduleCreate,
		MethodScheduleUpdate,
		MethodScheduleDelete,
		MethodScheduleExecutions,
		MethodScheduleRunNow,
		MethodScheduleSync,
		// M4：启动项管理
		MethodServicesList,
		MethodServicesDetail,
		MethodServicesRegister,
		MethodServicesUnregister,
		MethodServicesControl,
		// M6：网络诊断与容器
		MethodDiagnosticsSnapshot,
		MethodDiagnosticsTCPProbe,
		MethodOrbStackContainers,
	}
}

// RegisterAll 把全部方法注册到 IPC 服务端。
func (a *Agent) RegisterAll(srv *ipc.Server) {
	srv.Register(MethodHealth, a.handleHealth)
	srv.Register(MethodSystemOverview, a.handleSystemOverview)
	srv.Register(MethodSystemNetwork, a.handleSystemNetwork)
	srv.Register(MethodFirewallStatus, a.handleFirewallStatus)

	// M2 防火墙读写。方法总是注册（便于前端按能力探测），
	// 依赖未就绪时由各自的 handler 返回「未实现」。
	srv.Register(MethodFirewallRulesGet, a.handleRulesGet)
	srv.Register(MethodFirewallRulesSet, a.handleRulesSet)
	srv.Register(MethodFirewallPreview, a.handlePreview)
	srv.Register(MethodFirewallApply, a.handleApply)
	srv.Register(MethodFirewallConfirm, a.handleConfirm)
	srv.Register(MethodFirewallRollback, a.handleRollback)
	srv.Register(MethodFirewallTransaction, a.handleTransaction)
	srv.Register(MethodFirewallTemplates, a.handleTemplates)

	// M3 电源与计划任务。同样总是注册，依赖未就绪时返回「未实现」。
	a.registerPower(srv)

	// M4 启动项管理。
	a.registerServices(srv)

	// M6 网络诊断与容器。
	a.registerDiag(srv)
}

// Methods 返回已实现的方法名。
func (a *Agent) Methods() []string {
	out := make([]string, len(a.methods))
	copy(out, a.methods)
	return out
}

// handleHealth 返回 Agent 自身状态。
func (a *Agent) handleHealth(ctx context.Context, params json.RawMessage) (any, error) {
	if err := requireNoParams(params); err != nil {
		return nil, err
	}
	return model.AgentHealthResponse{
		AgentHealth: model.AgentHealth{
			Version:     version.Version,
			StartedAt:   a.startedAt,
			PID:         os.Getpid(),
			Platform:    version.Current().Platform,
			IsRoot:      ipc.CurrentEUID() == 0,
			MethodCount: len(a.methods),
			ServerClock: time.Now().UTC(),
		},
		Methods: a.methods,
	}, nil
}

func (a *Agent) handleSystemOverview(ctx context.Context, params json.RawMessage) (any, error) {
	if err := requireNoParams(params); err != nil {
		return nil, err
	}
	ov, err := a.collector.SystemOverview(ctx)
	if err != nil {
		// 采集整体失败：报告不可用，不返回部分伪造数据。
		if e, ok := err.(*model.Error); ok {
			return nil, e
		}
		a.log.Warn("采集系统概览失败", "error", err)
		return nil, model.Unavailable("系统概览暂时不可用：%v", err)
	}
	return ov, nil
}

func (a *Agent) handleSystemNetwork(ctx context.Context, params json.RawMessage) (any, error) {
	if err := requireNoParams(params); err != nil {
		return nil, err
	}
	snap, err := a.collector.NetworkSnapshot(ctx)
	if err != nil {
		if e, ok := err.(*model.Error); ok {
			return nil, e
		}
		a.log.Warn("采集网络快照失败", "error", err)
		return nil, model.Unavailable("网络状态暂时不可用：%v", err)
	}
	return snap, nil
}

func (a *Agent) handleFirewallStatus(ctx context.Context, params json.RawMessage) (any, error) {
	if err := requireNoParams(params); err != nil {
		return nil, err
	}
	st, err := a.collector.FirewallStatus(ctx)
	if err != nil {
		if e, ok := err.(*model.Error); ok {
			return nil, e
		}
		a.log.Warn("采集防火墙状态失败", "error", err)
		return nil, model.Unavailable("防火墙状态暂时不可用：%v", err)
	}
	return st, nil
}

// requireNoParams 拒绝任何参数。
//
// 严格起见：读接口若真的带参数，说明调用方理解错了协议，
// 直接报错比静默忽略更安全（避免将来给接口加参数时出现静默兼容问题）。
func requireNoParams(params json.RawMessage) error {
	trimmed := json.RawMessage(params)
	if len(trimmed) == 0 {
		return nil
	}
	// 允许显式 null 与空对象。
	if string(trimmed) == "null" || string(trimmed) == "{}" {
		return nil
	}
	return model.NewError(model.CodeInvalidArgument, "该方法不接受任何参数")
}
