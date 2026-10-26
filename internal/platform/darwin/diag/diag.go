// Package diag 实现网络诊断：TCP 连通性检测、IPv6 路由与监听端口。
//
// 全部只读，不改变系统状态。TCP 探测用 Go 原生 net.DialTimeout
// 而不是 shell 命令——没有任何理由为此引入 curl/nc 依赖。
package diag

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/lancelot-lewis/MacPanel/internal/model"
	"github.com/lancelot-lewis/MacPanel/internal/platform/execx"
)

// NetstatPath 是 macOS 上的网络统计工具。
const NetstatPath = "/usr/sbin/netstat"

// Dialer 抽象 TCP 拨号，便于测试注入（不真的建连接）。
type Dialer interface {
	DialContext(ctx context.Context, network, address string) (net.Conn, error)
}

// Prober 执行 TCP 连通性检测。
type Prober struct {
	dialer Dialer
	// now 可注入，便于测试断言时间字段。
	now func() time.Time
}

// NewProber 创建真实探测器的。
func NewProber() *Prober {
	return &Prober{
		dialer: &net.Dialer{},
		now:    func() time.Time { return time.Now().UTC() },
	}
}

// NewProberForTest 创建可注入依赖的探测器（仅供测试）。
func NewProberForTest(d Dialer, now func() time.Time) *Prober {
	if now == nil {
		now = func() time.Time { return time.Now().UTC() }
	}
	return &Prober{dialer: d, now: now}
}

// Probe 检测目标 TCP 端口是否可连接。
//
// 结论分类而不是简单的成功/失败：
//
//	refused    端口没人监听  → 服务没起来
//	timeout    无响应        → 可能被防火墙丢弃（PF 规则写错的典型表现）
//	unreachable 无路由       → 地址/接口问题
//
// 区分这三者对用户判断问题所在至关重要。
func (p *Prober) Probe(ctx context.Context, host string, port int, timeout time.Duration) model.TCPProbeResult {
	if timeout <= 0 {
		timeout = 3 * time.Second
	}

	target := model.FormatTarget(host, port)
	res := model.TCPProbeResult{
		Target:   target,
		Host:     host,
		Port:     port,
		ProbedAt: p.now(),
	}

	// 用带超时的子 context：既尊重调用方取消，也保证有上限。
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	start := time.Now()
	conn, err := p.dialer.DialContext(ctx, "tcp", target)
	elapsed := time.Since(start)

	if err == nil {
		_ = conn.Close()
		res.Reachable = true
		res.Outcome = model.ProbeConnected
		res.Detail = model.DescribeProbe(model.ProbeConnected)
		res.LatencyMs = elapsed.Milliseconds()
		return res
	}

	res.Outcome, res.Detail = classifyDialError(err, timeout)
	return res
}

// classifyDialError 把拨号错误归类为可读的结论。
func classifyDialError(err error, timeout time.Duration) (outcome, detail string) {
	// 超时判断必须先做：它可能同时满足其它条件。
	if errors.Is(err, context.DeadlineExceeded) || isTimeout(err) {
		return model.ProbeTimeout,
			fmt.Sprintf("连接超时（%s 内无响应）：可能被防火墙丢弃，或目标主机不在线", timeout)
	}

	// 连接被拒绝：端口没有监听。这是最常见也最明确的情况。
	if errors.Is(err, syscall.ECONNREFUSED) {
		return model.ProbeRefused, model.DescribeProbe(model.ProbeRefused)
	}

	if errors.Is(err, syscall.EHOSTUNREACH) || errors.Is(err, syscall.ENETUNREACH) {
		return model.ProbeUnreachable, model.DescribeProbe(model.ProbeUnreachable)
	}

	// DNS 解析失败等
	var dnsErr *net.DNSError
	if errors.As(err, &dnsErr) {
		return model.ProbeUnreachable, fmt.Sprintf("无法解析主机：%v", dnsErr)
	}

	return model.ProbeError, err.Error()
}

// isTimeout 判断错误是否为超时（兼容 net.Error 接口）。
func isTimeout(err error) bool {
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return true
	}
	return false
}

// ---- 路由与监听端口 ----

// Collector 采集路由表与监听端口。
type Collector struct {
	// run 执行 netstat（可注入）。
	run func(ctx context.Context, args ...string) (string, error)
	// now 可注入。
	now func() time.Time
}

// NewCollector 创建真实采集器。
func NewCollector() *Collector {
	return &Collector{
		run: func(ctx context.Context, args ...string) (string, error) {
			res, err := execx.Run(ctx, 10*time.Second, NetstatPath, args...)
			if err != nil {
				return "", err
			}
			return res.Stdout, nil
		},
		now: func() time.Time { return time.Now().UTC() },
	}
}

// NewCollectorForTest 创建可注入依赖的采集器（仅供测试）。
func NewCollectorForTest(run func(context.Context, ...string) (string, error), now func() time.Time) *Collector {
	if now == nil {
		now = func() time.Time { return time.Now().UTC() }
	}
	return &Collector{run: run, now: now}
}

// Snapshot 采集 IPv6 路由与监听端口。
//
// 两项独立采集：一项失败不影响另一项，失败原因记录在 Missing 中。
// 这比「整体失败」有用得多——能读到的部分仍有诊断价值。
func (c *Collector) Snapshot(ctx context.Context) (model.DiagnosticsSnapshot, error) {
	snap := model.DiagnosticsSnapshot{CollectedAt: c.now()}

	if out, err := c.run(ctx, "-rn", "-f", "inet6"); err != nil {
		snap.Missing = append(snap.Missing, model.MissingMetric{
			Metric: "ipv6Routes",
			Reason: fmt.Sprintf("读取路由表失败：%v", err),
		})
	} else {
		snap.IPv6Routes = ParseRoutes(out)
	}

	if out, err := c.run(ctx, "-an", "-f", "inet6"); err != nil {
		snap.Missing = append(snap.Missing, model.MissingMetric{
			Metric: "ipv6Listeners",
			Reason: fmt.Sprintf("读取连接表失败：%v", err),
		})
	} else if !looksLikeConnectionTable(out) {
		// 关键：区分「没有监听」与「读不到连接表」。
		//
		// 实测在某些受限环境（沙箱、权限不足）下，netstat -an 只返回
		// "Active Multipath Internet connections" 段，完全没有普通连接表。
		// 此时若按 0 条解析，界面会显示「没有 IPv6 监听端口」——
		// 那是错误结论，会把用户引向完全错误的排查方向。
		snap.Missing = append(snap.Missing, model.MissingMetric{
			Metric: "ipv6Listeners",
			Reason: "netstat 未返回标准连接表（可能受权限或运行环境限制），监听端口列表不可用",
		})
	} else {
		snap.IPv6Listeners = ParseListeners(out)
	}

	return snap, nil
}

// ParseRoutes 解析 `netstat -rn -f inet6` 输出。
//
// 只取 IPv6 段（Internet6:），忽略 IPv4 段与表头。
func ParseRoutes(output string) []model.Route {
	lines := strings.Split(output, "\n")

	var (
		out       []model.Route
		inIPv6    bool
		sawHeader bool
	)

	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}

		// 段标记：Internet6: 之后是 IPv6 路由。
		if strings.HasSuffix(trimmed, ":") && !strings.HasPrefix(trimmed, "Destination") {
			inIPv6 = trimmed == "Internet6:"
			sawHeader = false
			continue
		}
		if !inIPv6 {
			continue
		}

		// 表头行
		if strings.HasPrefix(trimmed, "Destination") {
			sawHeader = true
			continue
		}
		if !sawHeader {
			continue
		}

		fields := strings.Fields(trimmed)
		if len(fields) < 4 {
			continue
		}

		r := model.Route{
			Destination: fields[0],
			Gateway:     fields[1],
			Flags:       fields[2],
			Interface:   fields[3],
		}
		if len(fields) >= 5 {
			r.Expire = fields[4]
		}
		out = append(out, r)
	}

	return out
}

// looksLikeConnectionTable 判断 netstat 输出是否包含普通连接表。
//
// 只检查标志性标题行：正常输出必然以
// "Active Internet connections (including servers)" 开头。
// 只看这一段而不推断具体内容，避免把格式变化误判成「有数据」。
func looksLikeConnectionTable(output string) bool {
	return strings.Contains(output, "Active Internet connections")
}

// ParseListeners 解析 `netstat -an -f inet6` 输出中的监听端口。
//
// 只保留 LISTEN 状态的 TCP 与全部 UDP（UDP 无连接状态，
// 存在绑定即视为监听）。已建立的连接不列入——那是「谁连着」，
// 属于另一个问题。
func ParseListeners(output string) []model.Listener {
	lines := strings.Split(output, "\n")

	var (
		out       []model.Listener
		sawHeader bool
	)

	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}

		if strings.HasPrefix(trimmed, "Proto") {
			sawHeader = true
			continue
		}
		if !sawHeader {
			continue
		}

		fields := strings.Fields(trimmed)
		if len(fields) < 4 {
			continue
		}

		// 协议列有四种形态（实测）：
		//   tcp6   仅监听 IPv6
		//   tcp46  同时监听 IPv4 与 IPv6（双栈）——服务绑 * 时的常见形态
		//   udp6 / udp46 同理
		//
		// 早期只匹配 tcp6/udp6，结果双栈监听全部漏掉，
		// 界面上显示「没有 IPv6 监听端口」而实际有 28 个。
		proto := fields[0]
		switch proto {
		case "tcp6", "tcp46", "udp6", "udp46":
		default:
			continue
		}

		// Local Address 是第 4 列（索引 3）。
		local := fields[3]
		addr, port, ok := splitAddrPort(local)
		if !ok {
			continue
		}

		// TCP 只保留 LISTEN；UDP 没有状态列，全部保留。
		if strings.HasPrefix(proto, "tcp") {
			if len(fields) < 6 || fields[5] != "LISTEN" {
				continue
			}
		}

		out = append(out, model.Listener{
			Protocol: proto,
			Address:  addr,
			Port:     port,
		})
	}

	return out
}

// splitAddrPort 从 netstat 的 Local Address 中分离地址与端口。
//
// macOS 的格式是「地址.端口」（用点号分隔，不是冒号）：
//
//	*.443
//	::1.9876
//	2409:8a55:d6d5:7.11010
//
// 因此取最后一个点号即可。不能取第一个：IPv6 地址里可能有嵌入的
// IPv4（::ffff:192.0.2.1），那会含多个点。
func splitAddrPort(s string) (addr string, port int, ok bool) {
	idx := strings.LastIndex(s, ".")
	if idx <= 0 || idx == len(s)-1 {
		return "", 0, false
	}

	p, err := strconv.Atoi(s[idx+1:])
	if err != nil || p < 0 || p > 65535 {
		return "", 0, false
	}
	return s[:idx], p, true
}

// ---- 本机地址 ----

// LocalAddresses 返回本机已配置的地址集合，用于「只允许检测本机」的判定。
//
// 除回环与名字外，把所有接口地址也算作本机：用户可能想验证
// 「我的公网 IPv6 能不能连到本机的 443」，那正是最常用的诊断场景。
func LocalAddresses() (map[string]bool, error) {
	out := make(map[string]bool, 8)
	// 固定名字
	for _, h := range []string{"localhost", "127.0.0.1", "::1", "0.0.0.0"} {
		out[h] = true
	}

	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return out, fmt.Errorf("枚举本机地址失败: %w", err)
	}
	for _, a := range addrs {
		var ip net.IP
		switch v := a.(type) {
		case *net.IPNet:
			ip = v.IP
		case *net.IPAddr:
			ip = v.IP
		}
		if ip == nil {
			continue
		}
		out[ip.String()] = true
	}
	return out, nil
}

// IsLocalTarget 判断目标主机是否为本机（含已配置的接口地址）。
func IsLocalTarget(host string, local map[string]bool) bool {
	if local[host] {
		return true
	}
	if model.IsLocalHost(host) {
		return true
	}
	// 带方括号的 IPv6 字面量
	if strings.HasPrefix(host, "[") && strings.HasSuffix(host, "]") {
		return local[strings.Trim(host, "[]")]
	}
	// 接口名形式（如 en0）：netstat/ifconfig 场景常见
	if _, err := net.InterfaceByName(host); err == nil {
		return true
	}
	return false
}

// Hostname 返回本机主机名（尽力而为）。
func Hostname() string {
	h, err := os.Hostname()
	if err != nil {
		return ""
	}
	return h
}
