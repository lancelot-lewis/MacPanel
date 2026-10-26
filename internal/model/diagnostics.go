package model

import (
	"fmt"
	"time"
)

// TCPProbeResult 是一次 TCP 连通性检测的结果。
//
// 语义约定（重要）：Probe 返回的是「观测结果」而不是「成功/失败」。
// 连接被拒绝（端口没人监听）与连接超时（可能被防火墙丢弃）是
// 完全不同的问题，必须分开表达——把它们都归为「失败」会让用户
// 无法判断是服务没起来，还是防火墙规则写错了。
type TCPProbeResult struct {
	// Target 是检测目标（host:port）。
	Target string `json:"target"`
	Host   string `json:"host"`
	Port   int    `json:"port"`

	// Reachable 表示 TCP 三次握手成功。
	Reachable bool `json:"reachable"`
	// Outcome 是细化的结论：
	//   connected  成功建立连接
	//   refused    连接被拒绝（端口未监听，或服务拒绝）
	//   timeout    超时（无响应，常见于被防火墙丢弃）
	//   unreachable 网络不可达（无路由）
	//   error      其它错误
	Outcome string `json:"outcome"`
	// Detail 是给用户看的说明。
	Detail string `json:"detail,omitempty"`

	// LatencyMs 是建立连接耗时（仅成功时有意义）。
	LatencyMs int64 `json:"latencyMs,omitempty"`
	// ProbedAt 是检测时刻。
	ProbedAt time.Time `json:"probedAt"`
}

// 探测结论取值。
const (
	ProbeConnected   = "connected"
	ProbeRefused     = "refused"
	ProbeTimeout     = "timeout"
	ProbeUnreachable = "unreachable"
	ProbeError       = "error"
)

// Route 是一条路由表条目。
type Route struct {
	Destination string `json:"destination"`
	Gateway     string `json:"gateway"`
	Flags       string `json:"flags"`
	Interface   string `json:"interface"`
	Expire      string `json:"expire,omitempty"`
}

// Listener 是一个监听中的端口。
type Listener struct {
	// Protocol 是 tcp4/tcp6/udp4/udp6。
	Protocol string `json:"protocol"`
	// Address 是监听地址（如 *、::1、127.0.0.1）。
	Address string `json:"address"`
	Port    int    `json:"port"`
}

// DiagnosticsSnapshot 是网络诊断快照。
type DiagnosticsSnapshot struct {
	// IPv6Routes 是 IPv6 路由表。
	IPv6Routes []Route `json:"ipv6Routes"`
	// IPv6Listeners 是 IPv6 监听端口。
	IPv6Listeners []Listener `json:"ipv6Listeners"`
	// Missing 记录无法采集的项及原因。
	Missing []MissingMetric `json:"missing,omitempty"`
	// CollectedAt 是采集时刻。
	CollectedAt time.Time `json:"collectedAt"`
}

// OrbStackStatus 是 OrbStack/Docker 的可用性。
type OrbStackStatus struct {
	// Available 表示能连上 Docker Socket 并读到容器列表。
	Available bool `json:"available"`
	// SocketPath 是实际使用的 Socket 路径（不含敏感信息）。
	SocketPath string `json:"socketPath,omitempty"`
	// Version 是 Docker API 版本（尽力而为）。
	Version string `json:"version,omitempty"`
	// Reason 是不可用时说明原因。
	Reason string `json:"reason,omitempty"`
}

// Container 是一个容器（只读视图）。
//
// 刻意只暴露最少字段：完整容器配置包含环境变量（常有凭据），
// 而本应用只需要「有哪些容器、在不在跑」。
type Container struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Image  string `json:"image"`
	State  string `json:"state"`
	Status string `json:"status"`
	// Ports 是映射端口描述（如 "443/tcp → 0.0.0.0:443"）。
	Ports []string `json:"ports,omitempty"`
}

// ContainersOverview 是容器列表返回。
type ContainersOverview struct {
	Status OrbStackStatus `json:"status"`
	Items  []Container    `json:"items"`
}

// ValidateProbeTarget 校验 TCP 探测目标。
//
// 限制为回环与本机地址：本应用的诊断目标是「我配的服务起来了吗」，
// 而不是「扫描网络」。开放任意目标会让面板变成探测工具，
// 也让「谁在什么时候探测了哪个地址」这类信息进入审计，反而增加风险。
func ValidateProbeTarget(host string, port int, allowLoopbackOnly bool) error {
	if host == "" {
		return NewError(CodeInvalidArgument, "目标主机不能为空")
	}
	if len(host) > 255 {
		return NewError(CodeInvalidArgument, "目标主机过长")
	}
	if port < 1 || port > 65535 {
		return NewError(CodeInvalidArgument, "端口必须在 1-65535 之间，实际 %d", port)
	}

	if allowLoopbackOnly && !IsLocalHost(host) {
		return NewError(CodeInvalidArgument,
			"出于安全考虑，只能检测本机地址（127.0.0.1、::1、localhost 或本机已配置的地址）；"+
				"实际 %q", host)
	}
	return nil
}

// localHostNames 是公认可代表本机的名字。
var localHostNames = map[string]bool{
	"localhost": true,
	"127.0.0.1": true,
	"::1":       true,
	"0.0.0.0":   true,
	"[::1]":     true,
}

// IsLocalHost 判断主机名是否代表本机。
//
// 除固定名字外，还接受 127.0.0.0/8 与 fe80::/10、fc00::/7 之外
// 的常见本机形态判断由调用方补充（调用方持有实际接口地址列表）。
func IsLocalHost(host string) bool {
	if localHostNames[host] {
		return true
	}
	// 127.0.0.0/8
	if len(host) > 4 && host[:4] == "127." {
		return true
	}
	return false
}

// DescribeProbe 生成探测结果的中文说明。
func DescribeProbe(outcome string) string {
	switch outcome {
	case ProbeConnected:
		return "连接成功，端口在监听"
	case ProbeRefused:
		return "连接被拒绝：目标端口没有服务在监听"
	case ProbeTimeout:
		return "连接超时：没有响应，可能被防火墙丢弃"
	case ProbeUnreachable:
		return "网络不可达：没有到目标的路由"
	default:
		return "检测失败"
	}
}

// FormatTarget 生成 host:port 展示形式（IPv6 会加方括号）。
func FormatTarget(host string, port int) string {
	if len(host) > 0 && host[0] == '[' {
		return fmt.Sprintf("%s:%d", host, port)
	}
	for _, r := range host {
		if r == ':' {
			return fmt.Sprintf("[%s]:%d", host, port)
		}
	}
	return fmt.Sprintf("%s:%d", host, port)
}
