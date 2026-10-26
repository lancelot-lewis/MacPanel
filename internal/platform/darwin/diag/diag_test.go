package diag

import (
	"context"
	"errors"
	"net"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/lancelot-lewis/MacPanel/internal/model"
)

// fakeDialer 按预设返回连接或错误。
type fakeDialer struct {
	err   error
	delay time.Duration
	conn  net.Conn
}

func (f *fakeDialer) DialContext(ctx context.Context, network, address string) (net.Conn, error) {
	if f.delay > 0 {
		select {
		case <-time.After(f.delay):
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	if f.err != nil {
		return nil, f.err
	}
	if f.conn != nil {
		return f.conn, nil
	}
	// 返回一个 pipe 作为“已建立”的连接
	c1, c2 := net.Pipe()
	_ = c2.Close()
	return c1, nil
}

func fixedNow() time.Time {
	return time.Date(2026, 10, 26, 12, 0, 0, 0, time.UTC)
}

// TestProbeConnected 验证连接成功。
func TestProbeConnected(t *testing.T) {
	p := NewProberForTest(&fakeDialer{}, fixedNow)

	res := p.Probe(context.Background(), "127.0.0.1", 443, time.Second)

	if !res.Reachable || res.Outcome != model.ProbeConnected {
		t.Fatalf("应判定为连接成功，实际 %+v", res)
	}
	if res.Target != "127.0.0.1:443" {
		t.Errorf("目标格式不符: %q", res.Target)
	}
	if res.ProbedAt.IsZero() {
		t.Error("应记录检测时刻")
	}
}

// TestProbeRefused 验证连接被拒绝的分类。
//
// 这是最常见的情况：端口没有服务监听。必须与超时区分，
// 因为两者的排查方向完全不同（服务没起来 vs 防火墙丢弃）。
func TestProbeRefused(t *testing.T) {
	p := NewProberForTest(
		&fakeDialer{err: &net.OpError{Op: "dial", Err: syscall.ECONNREFUSED}},
		fixedNow,
	)

	res := p.Probe(context.Background(), "::1", 8080, time.Second)

	if res.Reachable {
		t.Error("被拒绝不应判定为可达")
	}
	if res.Outcome != model.ProbeRefused {
		t.Errorf("应归类为 refused，实际 %q（%s）", res.Outcome, res.Detail)
	}
	// IPv6 目标应加方括号
	if res.Target != "[::1]:8080" {
		t.Errorf("IPv6 目标格式不符: %q", res.Target)
	}
}

// TestProbeTimeout 验证超时分类。
func TestProbeTimeout(t *testing.T) {
	// 用一个永不返回的 dialer，配合很短的超时
	p := NewProberForTest(&fakeDialer{delay: time.Hour}, fixedNow)

	res := p.Probe(context.Background(), "127.0.0.1", 443, 50*time.Millisecond)

	if res.Outcome != model.ProbeTimeout {
		t.Errorf("应归类为 timeout，实际 %q（%s）", res.Outcome, res.Detail)
	}
	if !strings.Contains(res.Detail, "防火墙") {
		t.Errorf("超时说明应提示可能被防火墙丢弃，实际 %q", res.Detail)
	}
}

// TestProbeUnreachable 验证网络不可达分类。
func TestProbeUnreachable(t *testing.T) {
	p := NewProberForTest(
		&fakeDialer{err: &net.OpError{Op: "dial", Err: syscall.EHOSTUNREACH}},
		fixedNow,
	)

	res := p.Probe(context.Background(), "2408::1", 443, time.Second)
	if res.Outcome != model.ProbeUnreachable {
		t.Errorf("应归类为 unreachable，实际 %q", res.Outcome)
	}
}

// TestProbeDNSFailure 验证域名解析失败归类为不可达。
func TestProbeDNSFailure(t *testing.T) {
	p := NewProberForTest(
		&fakeDialer{err: &net.DNSError{Err: "no such host", Name: "nope.invalid"}},
		fixedNow,
	)

	res := p.Probe(context.Background(), "nope.invalid", 443, time.Second)
	if res.Outcome != model.ProbeUnreachable {
		t.Errorf("DNS 失败应归类为 unreachable，实际 %q", res.Outcome)
	}
	if !strings.Contains(res.Detail, "解析") {
		t.Errorf("应说明是解析问题，实际 %q", res.Detail)
	}
}

// ---- 路由解析 ----

// TestParseRoutes 验证 IPv6 路由表解析。
func TestParseRoutes(t *testing.T) {
	out := `Routing tables

Internet:
Destination        Gateway            Flags        Netif Expire
default            192.168.1.1        UGScg          en0

Internet6:
Destination                             Gateway                                 Flags               Netif Expire
default                                 fe80::1%en0                             UGcg                  en0
default                                 fe80::%utun1                            UGcIg               utun1
::1                                     ::1                                     UHL                   lo0
2408:8000::/20                          fe80::1%en0                             UGc                   en0
fe80::%en0/64                           link#15                                 UCI                   en0
`
	routes := ParseRoutes(out)

	if len(routes) != 5 {
		t.Fatalf("应解析出 5 条 IPv6 路由，实际 %d: %+v", len(routes), routes)
	}
	if routes[0].Destination != "default" || routes[0].Interface != "en0" {
		t.Errorf("首条路由不符: %+v", routes[0])
	}
	if routes[0].Flags != "UGcg" {
		t.Errorf("Flags 不符: %q", routes[0].Flags)
	}

	// ::1 回环路由
	found := false
	for _, r := range routes {
		if r.Destination == "::1" && r.Interface == "lo0" {
			found = true
		}
	}
	if !found {
		t.Error("应包含 ::1 回环路由")
	}
}

// TestParseRoutesIgnoresIPv4 验证 IPv4 段不被混入。
func TestParseRoutesIgnoresIPv4(t *testing.T) {
	out := `Internet:
Destination        Gateway            Flags        Netif Expire
default            192.168.1.1        UGScg          en0
`
	if routes := ParseRoutes(out); len(routes) != 0 {
		t.Errorf("IPv4 段不应被解析为 IPv6 路由，实际 %+v", routes)
	}
}

// TestParseRoutesEmpty 验证空输入不 panic。
func TestParseRoutesEmpty(t *testing.T) {
	if routes := ParseRoutes(""); len(routes) != 0 {
		t.Errorf("空输入应返回空，实际 %+v", routes)
	}
}

// ---- 监听端口解析 ----

// TestParseListeners 验证只保留 LISTEN 与 UDP 绑定。
func TestParseListeners(t *testing.T) {
	out := `Active Internet connections (including servers)
Proto Recv-Q Send-Q  Local Address          Foreign Address        (state)
tcp6       0      0  *.443                  *.*                    LISTEN
tcp6       0      0  ::1.9876               *.*                    LISTEN
tcp6       0      0  2409:8a55:d6d5:7.11010 fd07:b51a:cc66:0.50563 ESTABLISHED
tcp6       0      0  2409:8a55:d6d5:7.11010 2409:8a55:d6d5:7.50562 SYN_RCVD
udp6       0      0  *.5353                 *.*
tcp4       0      0  127.0.0.1.8080         *.*                    LISTEN
`
	listeners := ParseListeners(out)

	// 应保留：443(LISTEN)、::1.9876(LISTEN)、udp 5353；排除 2 条非 LISTEN、1 条 tcp4
	if len(listeners) != 3 {
		t.Fatalf("应解析出 3 个监听，实际 %d: %+v", len(listeners), listeners)
	}

	var ports []int
	for _, l := range listeners {
		ports = append(ports, l.Port)
	}
	want := map[int]bool{443: true, 9876: true, 5353: true}
	for _, p := range ports {
		if !want[p] {
			t.Errorf("出现不应保留的端口: %d", p)
		}
	}
}

// TestParseListenersAcceptsDualStack 验证 tcp46/udp46 双栈监听被识别。
//
// 回归测试：真实环境里服务绑 * 时协议列是 tcp46，
// 早期实现只匹配 tcp6，导致「没有 IPv6 监听端口」的错误结论
// （实测本机有 28 个双栈监听，界面却显示 0 个）。
func TestParseListenersAcceptsDualStack(t *testing.T) {
	out := `Proto Recv-Q Send-Q  Local Address          Foreign Address        (state)
tcp46      0      0  *.443                  *.*                    LISTEN
tcp46      0      0  *.8080                 *.*                    LISTEN
udp46      0      0  *.5353                 *.*
tcp6       0      0  ::1.9876               *.*                    LISTEN
`
	got := ParseListeners(out)
	if len(got) != 4 {
		t.Fatalf("应识别 4 个监听（含双栈），实际 %d: %+v", len(got), got)
	}

	ports := map[int]bool{}
	for _, l := range got {
		ports[l.Port] = true
	}
	for _, p := range []int{443, 8080, 5353, 9876} {
		if !ports[p] {
			t.Errorf("缺少端口 %d", p)
		}
	}
}

// TestParseListenersExcludesEstablished 验证已建立连接不被当作监听。
//
// 这是关键区分：「有连接进来」与「在监听」是不同的事，
// 把 ESTABLISHED 也算进去会让用户以为自己在监听某个端口。
func TestParseListenersExcludesEstablished(t *testing.T) {
	out := `Proto Recv-Q Send-Q  Local Address          Foreign Address        (state)
tcp6       0      0  2409:8a55:d6d5:7.11010 fd07:b51a:cc66:0.50563 ESTABLISHED
`
	if got := ParseListeners(out); len(got) != 0 {
		t.Errorf("ESTABLISHED 不应被当作监听，实际 %+v", got)
	}
}

// TestSplitAddrPort 验证地址端口分离。
//
// macOS 用点号分隔端口，而 IPv6 地址里可能嵌入 IPv4（含多个点），
// 因此必须取最后一个点。
func TestSplitAddrPort(t *testing.T) {
	cases := []struct {
		in   string
		addr string
		port int
		ok   bool
	}{
		{"*.443", "*", 443, true},
		{"::1.9876", "::1", 9876, true},
		{"2409:8a55:d6d5:7.11010", "2409:8a55:d6d5:7", 11010, true},
		{"::ffff:192.0.2.1.443", "::ffff:192.0.2.1", 443, true},
		{"noport", "", 0, false},
		{".443", "", 0, false},
		{"addr.", "", 0, false},
		{"addr.abc", "", 0, false},
		{"addr.70000", "", 0, false},
	}

	for _, c := range cases {
		addr, port, ok := splitAddrPort(c.in)
		if ok != c.ok {
			t.Errorf("%q 的 ok 应为 %v，实际 %v", c.in, c.ok, ok)
			continue
		}
		if !c.ok {
			continue
		}
		if addr != c.addr || port != c.port {
			t.Errorf("%q 期望 (%q,%d)，实际 (%q,%d)", c.in, c.addr, c.port, addr, port)
		}
	}
}

// TestCollectorSnapshotPartialFailure 验证单项失败不影响另一项。
func TestCollectorSnapshotPartialFailure(t *testing.T) {
	c := NewCollectorForTest(
		func(ctx context.Context, args ...string) (string, error) {
			// 路由表读取失败，连接表成功
			for _, a := range args {
				if a == "-rn" {
					return "", errors.New("permission denied")
				}
			}
			// 必须包含标准标题行，否则会被判为「读不到连接表」。
			return "Active Internet connections (including servers)\n" +
				"Proto Recv-Q Send-Q  Local Address  Foreign Address  (state)\n" +
				"tcp6 0 0 *.443 *.* LISTEN\n", nil
		},
		fixedNow,
	)

	snap, err := c.Snapshot(context.Background())
	if err != nil {
		t.Fatalf("不应整体失败: %v", err)
	}
	if len(snap.Missing) != 1 {
		t.Errorf("应记录 1 项缺失，实际 %d: %+v", len(snap.Missing), snap.Missing)
	}
	if len(snap.IPv6Listeners) != 1 {
		t.Errorf("连接表应仍被解析，实际 %+v", snap.IPv6Listeners)
	}
	if len(snap.IPv6Routes) != 0 {
		t.Errorf("路由表失败时应为空，实际 %+v", snap.IPv6Routes)
	}
}

// TestCollectorReportsUnreadableConnectionTable 验证读不到连接表时报告不可用。
//
// 回归测试：某些受限环境下 netstat -an 只返回 multipath 段，
// 此时按 0 条解析会让界面显示「没有 IPv6 监听端口」——
// 那是错误结论，必须显式报告「不可用」。
func TestCollectorReportsUnreadableConnectionTable(t *testing.T) {
	c := NewCollectorForTest(
		func(ctx context.Context, args ...string) (string, error) {
			for _, a := range args {
				if a == "-rn" {
					return "", errors.New("skip")
				}
			}
			// 只有 multipath 段，没有标准连接表
			return "Active Multipath Internet connections\nProto/ID  Flags      Local Address\n", nil
		},
		fixedNow,
	)

	snap, err := c.Snapshot(context.Background())
	if err != nil {
		t.Fatalf("不应整体失败: %v", err)
	}

	var found bool
	for _, m := range snap.Missing {
		if m.Metric == "ipv6Listeners" {
			found = true
			if !strings.Contains(m.Reason, "不可用") {
				t.Errorf("原因应说明不可用，实际 %q", m.Reason)
			}
		}
	}
	if !found {
		t.Error("读不到标准连接表时应记入 Missing，而不是返回 0 条监听")
	}
}

// TestIsLocalTarget 验证本机目标判定。
func TestIsLocalTarget(t *testing.T) {
	local := map[string]bool{
		"127.0.0.1":  true,
		"::1":        true,
		"2408::abcd": true,
	}

	cases := map[string]bool{
		"localhost":    true,
		"127.0.0.1":    true,
		"127.0.0.5":    true, // 127/8 全段
		"::1":          true,
		"2408::abcd":   true, // 本机接口地址
		"[2408::abcd]": true,
		"8.8.8.8":      false,
		"example.com":  false,
	}
	for host, want := range cases {
		if got := IsLocalTarget(host, local); got != want {
			t.Errorf("%q 应判定为本机=%v，实际 %v", host, want, got)
		}
	}
}

// TestValidateProbeTarget 验证目标校验。
func TestValidateProbeTarget(t *testing.T) {
	// 非法端口
	for _, port := range []int{0, -1, 65536} {
		if err := model.ValidateProbeTarget("127.0.0.1", port, true); err == nil {
			t.Errorf("端口 %d 应被拒绝", port)
		}
	}

	// 限制模式下的外部地址
	if err := model.ValidateProbeTarget("8.8.8.8", 53, true); err == nil {
		t.Error("限制模式应拒绝外部地址")
	}

	// 不限制时接受
	if err := model.ValidateProbeTarget("8.8.8.8", 53, false); err != nil {
		t.Errorf("非限制模式应接受外部地址，实际: %v", err)
	}

	// 空主机
	if err := model.ValidateProbeTarget("", 443, true); err == nil {
		t.Error("空主机应被拒绝")
	}
}
