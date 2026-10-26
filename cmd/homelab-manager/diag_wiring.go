package main

import (
	"log/slog"

	"github.com/lancelot-lewis/MacPanel/internal/agent"
	"github.com/lancelot-lewis/MacPanel/internal/platform/darwin/diag"
	"github.com/lancelot-lewis/MacPanel/internal/platform/orbstack"
)

// diagWiring 汇总 M6 诊断能力的装配结果。
type diagWiring struct {
	deps    *agent.DiagDeps
	enabled bool
}

// wireDiag 装配网络诊断与容器只读能力。
//
// 全部只读，因此总是启用（不需要 root）：
//   - TCP 探测用 Go 原生 dial，不依赖系统命令；
//   - 路由与监听端口用 netstat（execx 里做了参数级限制）；
//   - OrbStack 通过 Docker Socket 只读访问。
//
// AllowNonLocalProbe 默认关闭：开放任意目标会让面板变成端口扫描器，
// 而本应用真正需要的是「我配的服务起来了吗」——那总是本机地址。
func wireDiag(log *slog.Logger) diagWiring {
	allowNonLocal := envOr("HLM_ALLOW_NONLOCAL_PROBE", "") == "1"
	if allowNonLocal {
		log.Warn("已允许 TCP 探测外部地址：面板可作为网络探测工具使用，请确认这是预期行为")
	}

	return diagWiring{
		deps: &agent.DiagDeps{
			Prober:             diag.NewProber(),
			Collector:          diag.NewCollector(),
			OrbStack:           orbstack.New(orbstack.Options{}),
			AllowNonLocalProbe: allowNonLocal,
		},
		enabled: true,
	}
}
