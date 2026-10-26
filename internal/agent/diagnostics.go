package agent

import (
	"context"
	"encoding/json"
	"time"

	"github.com/lancelot-lewis/MacPanel/internal/ipc"
	"github.com/lancelot-lewis/MacPanel/internal/model"
	"github.com/lancelot-lewis/MacPanel/internal/platform/darwin/diag"
	"github.com/lancelot-lewis/MacPanel/internal/platform/orbstack"
)

// M6 方法名。
const (
	MethodDiagnosticsSnapshot = "diagnostics.snapshot"
	MethodDiagnosticsTCPProbe = "diagnostics.tcp_probe"
	MethodOrbStackContainers  = "orbstack.containers"
)

// DiagDeps 是 M6 诊断与容器能力依赖。
type DiagDeps struct {
	// Prober 执行 TCP 探测。
	Prober *diag.Prober
	// Collector 采集路由与监听端口。
	Collector *diag.Collector
	// OrbStack 只读容器列表；为 nil 表示不可用。
	OrbStack *orbstack.Client
	// OrbStackReason 说明 OrbStack 为何不可用（nil 时有效）。
	OrbStackReason string
	// AllowNonLocalProbe 是否允许探测外部地址。
	//
	// 默认 false：诊断目标是「我配的服务起来了吗」，不是网络扫描。
	// 开放任意目标会让面板变成探测工具。
	AllowNonLocalProbe bool
}

// SetDiagDeps 注入诊断能力。
func (a *Agent) SetDiagDeps(deps *DiagDeps) {
	a.diag = deps
}

// registerDiag 注册 M6 方法。
func (a *Agent) registerDiag(srv *ipc.Server) {
	srv.Register(MethodDiagnosticsSnapshot, a.handleDiagnosticsSnapshot)
	srv.Register(MethodDiagnosticsTCPProbe, a.handleTCPProbe)
	srv.Register(MethodOrbStackContainers, a.handleOrbStackContainers)
}

// diagReady 检查依赖。
func (a *Agent) diagReady() error {
	if a.diag == nil || a.diag.Prober == nil || a.diag.Collector == nil {
		return model.NotImplemented("网络诊断（Agent 未装配诊断器）")
	}
	return nil
}

func (a *Agent) handleDiagnosticsSnapshot(ctx context.Context, params json.RawMessage) (any, error) {
	if err := requireNoParams(params); err != nil {
		return nil, err
	}
	if err := a.diagReady(); err != nil {
		return nil, err
	}

	snap, err := a.diag.Collector.Snapshot(ctx)
	if err != nil {
		return nil, model.Unavailable("采集网络诊断快照失败：%v", err)
	}
	return snap, nil
}

// tcpProbeParams 是 TCP 探测参数。
type tcpProbeParams struct {
	Host string `json:"host"`
	Port int    `json:"port"`
	// TimeoutMs 是超时毫秒数，默认 3000，上限 10000。
	TimeoutMs int `json:"timeoutMs"`
}

// handleTCPProbe 检测 TCP 端口连通性。
func (a *Agent) handleTCPProbe(ctx context.Context, params json.RawMessage) (any, error) {
	if err := a.diagReady(); err != nil {
		return nil, err
	}

	var p tcpProbeParams
	if err := decodeParams(params, &p); err != nil {
		return nil, err
	}

	allowNonLocal := a.diag.AllowNonLocalProbe
	if err := model.ValidateProbeTarget(p.Host, p.Port, !allowNonLocal); err != nil {
		return nil, err
	}

	// 即使关掉了「只允许本机」，也要求目标确实是本机已配置的地址，
	// 除非管理员显式开启 AllowNonLocalProbe。
	if !allowNonLocal {
		local, err := diag.LocalAddresses()
		if err != nil {
			a.log.Warn("枚举本机地址失败，仅允许回环与 localhost", "error", err)
		} else if !diag.IsLocalTarget(p.Host, local) {
			return nil, model.NewError(model.CodeInvalidArgument,
				"目标 %q 不是本机地址；如需检测外部地址请在配置中开启 AllowNonLocalProbe", p.Host)
		}
	}

	timeout := 3 * time.Second
	if p.TimeoutMs > 0 {
		// 上限 10 秒：再长就没有诊断意义了（用户会以为界面卡死）。
		if p.TimeoutMs > 10000 {
			p.TimeoutMs = 10000
		}
		timeout = time.Duration(p.TimeoutMs) * time.Millisecond
	}

	res := a.diag.Prober.Probe(ctx, p.Host, p.Port, timeout)
	a.log.Info("TCP 探测完成",
		"target", res.Target, "outcome", res.Outcome, "latencyMs", res.LatencyMs)
	return res, nil
}

// handleOrbStackContainers 列出容器（只读）。
func (a *Agent) handleOrbStackContainers(ctx context.Context, params json.RawMessage) (any, error) {
	if err := requireNoParams(params); err != nil {
		return nil, err
	}
	if a.diag == nil || a.diag.OrbStack == nil {
		reason := "OrbStack 未装配"
		if a.diag != nil && a.diag.OrbStackReason != "" {
			reason = a.diag.OrbStackReason
		}
		// 不可用不算错误：返回状态结构，界面据此显示「为什么没有容器」。
		return model.ContainersOverview{
			Status: model.OrbStackStatus{Available: false, Reason: reason},
			Items:  []model.Container{},
		}, nil
	}

	items, st, err := a.diag.OrbStack.Containers(ctx)
	if err != nil {
		// 读取失败但仍返回状态：界面需要区分「不可用」与「读取出错」。
		return model.ContainersOverview{
			Status: model.OrbStackStatus{
				Available:  st.Available,
				SocketPath: st.SocketPath,
				Version:    st.Version,
				Reason:     err.Error(),
			},
			Items: []model.Container{},
		}, nil
	}

	if items == nil {
		items = []model.Container{}
	}
	return model.ContainersOverview{Status: st, Items: items}, nil
}
