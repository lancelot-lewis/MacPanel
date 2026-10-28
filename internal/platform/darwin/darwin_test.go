package darwin

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/lancelot-lewis/MacPanel/internal/model"
	"github.com/lancelot-lewis/MacPanel/internal/platform"
)

// newTestCollector 创建指向临时 anchor 路径的采集器。
func newTestCollector(t *testing.T, anchorPath string) *Collector {
	t.Helper()
	cfg := platform.DefaultConfig("com.test.ipv6", anchorPath)
	cfg.PFConfPath = filepath.Join(t.TempDir(), "pf.conf")
	return New(cfg)
}

// TestSystemOverviewRealReads 在真实 macOS 上验证采集成功且无伪造值。
func TestSystemOverviewRealReads(t *testing.T) {
	c := newTestCollector(t, filepath.Join(t.TempDir(), "anchor"))
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	ov, err := c.SystemOverview(ctx)
	if err != nil {
		t.Fatalf("采集系统概览失败: %v", err)
	}
	if ov.Hostname == "" {
		t.Error("主机名不应为空")
	}
	if ov.OS.ProductVersion == "" {
		t.Error("macOS 版本不应为空")
	}
	if ov.CPU.LogicalCores <= 0 {
		t.Error("逻辑核心数应大于 0")
	}
	if ov.Memory.TotalBytes <= 0 {
		t.Error("内存总量应大于 0")
	}
	if ov.Memory.State == model.MetricOK {
		if ov.Memory.UsedBytes == nil || *ov.Memory.UsedBytes < 0 {
			t.Error("内存可用时应给出非负的已用量")
		}
		if ov.Memory.UsedBytes != nil && *ov.Memory.UsedBytes > ov.Memory.TotalBytes {
			t.Error("已用内存不应超过总量")
		}
	}
	if len(ov.Disks) == 0 {
		t.Error("应至少读取到一个卷")
	}
	for _, d := range ov.Disks {
		if d.TotalBytes <= 0 {
			t.Errorf("卷 %s 总容量异常: %d", d.MountPoint, d.TotalBytes)
		}
		if d.UsedRatio < 0 || d.UsedRatio > 1.0001 {
			t.Errorf("卷 %s 使用率超出 [0,1]: %f", d.MountPoint, d.UsedRatio)
		}
	}
	if ov.BootTime == nil {
		t.Error("启动时间应可读")
	} else if ov.BootTime.After(time.Now().Add(time.Minute)) {
		t.Error("启动时间不应在未来")
	}
	if ov.CollectedAt.IsZero() {
		t.Error("采集时间不应为零值")
	}
}

// TestNetworkSnapshotRealReads 验证网络接口读取。
func TestNetworkSnapshotRealReads(t *testing.T) {
	c := newTestCollector(t, filepath.Join(t.TempDir(), "anchor"))
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	snap, err := c.NetworkSnapshot(ctx)
	if err != nil {
		t.Fatalf("采集网络失败: %v", err)
	}
	if len(snap.Interfaces) == 0 {
		t.Fatal("应至少有一个网络接口")
	}

	var hasLoopback bool
	for _, iface := range snap.Interfaces {
		if iface.Name == "" {
			t.Error("接口名不应为空")
		}
		if iface.Loopback {
			hasLoopback = true
			if iface.Kind != model.KindLoopback {
				t.Errorf("lo0 应归类为 loopback，实际 %s", iface.Kind)
			}
		}
		for _, a := range iface.IPv4 {
			if a.Family != "ipv4" {
				t.Errorf("IPv4 地址族标记错误: %s", a.Family)
			}
			if net := a.Address; net == "" {
				t.Error("地址不应为空")
			}
		}
		for _, a := range iface.IPv6 {
			if a.Family != "ipv6" {
				t.Errorf("IPv6 地址族标记错误: %s", a.Family)
			}
		}
	}
	if !hasLoopback {
		t.Error("应识别到回环接口")
	}
}

// TestFirewallStatusWithoutRoot 验证非 root 下 PF 不可读时如实报告。
func TestFirewallStatusWithoutRoot(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("当前为 root，跳过非特权场景")
	}

	c := newTestCollector(t, filepath.Join(t.TempDir(), "anchor"))
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	st, err := c.FirewallStatus(ctx)
	if err != nil {
		t.Fatalf("采集防火墙状态失败: %v", err)
	}

	if st.PF.ReadAvailable {
		t.Log("当前环境可读 PF 状态（可能已授权）")
	} else {
		// 核心断言：不可读时 enabled 必须为 nil，不能用 false 冒充「已关闭」。
		if st.PF.Enabled != nil {
			t.Errorf("PF 不可读时 Enabled 必须为 nil，实际 %v", *st.PF.Enabled)
		}
		if st.PF.State != "unknown" {
			t.Errorf("PF 不可读时状态应为 unknown，实际 %s", st.PF.State)
		}
		if st.PF.ReadError == "" {
			t.Error("不可读时应给出原因")
		}
		if len(st.Warnings) == 0 {
			t.Error("不可读时应给出警告说明")
		}
	}

	if st.Writable {
		t.Error("M1 阶段 Writable 必须为 false")
	}
	if st.CollectedAt.IsZero() {
		t.Error("采集时间不应为零值")
	}
}

// TestAnchorAbsent 验证 anchor 不存在时的判定。
func TestAnchorAbsent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "com.test.ipv6")
	c := newTestCollector(t, path)
	ctx := context.Background()

	st, err := c.FirewallStatus(ctx)
	if err != nil {
		t.Fatalf("采集失败: %v", err)
	}
	if st.Anchor.Present {
		t.Error("anchor 文件不存在时应报告 Present=false")
	}
	if st.Anchor.State != model.AnchorAbsent {
		t.Errorf("状态应为 absent，实际 %s", st.Anchor.State)
	}
	if st.Managed != model.StateNotConfigured {
		t.Errorf("期望状态应为 not_configured，实际 %s", st.Managed)
	}
}

// TestAnchorPresentButNotReferenced 验证文件存在但未在主配置引用时给出警告。
func TestAnchorPresentButNotReferenced(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "com.test.ipv6")
	if err := os.WriteFile(path, []byte("pass in quick proto tcp to port 443\n"), 0o600); err != nil {
		t.Fatalf("准备 anchor 失败: %v", err)
	}

	cfg := platform.DefaultConfig("com.test.ipv6", path)
	cfg.PFConfPath = filepath.Join(dir, "pf.conf")
	// 主配置不含该 anchor 引用。
	if err := os.WriteFile(cfg.PFConfPath, []byte("# empty\n"), 0o600); err != nil {
		t.Fatalf("准备 pf.conf 失败: %v", err)
	}

	c := New(cfg)
	st, err := c.FirewallStatus(context.Background())
	if err != nil {
		t.Fatalf("采集失败: %v", err)
	}

	if !st.Anchor.Present {
		t.Error("文件存在时应报告 Present=true")
	}
	if st.Anchor.Referenced {
		t.Error("主配置未引用时应报告 Referenced=false")
	}
	if st.Managed == model.StateConfigured {
		t.Error("未被引用时不应判定为 configured")
	}
	// 规则文件存在但没人评估它：既不是「已配置」，也不是「未配置」，
	// 而是一种必须显式暴露的漂移状态。
	if st.Managed != model.StateDrift {
		t.Errorf("未被引用时应判定为 drift，实际 %s", st.Managed)
	}
	var found bool
	for _, w := range st.Warnings {
		if w != "" && contains(w, "未引用") && contains(w, "不生效") {
			found = true
		}
	}
	if !found {
		t.Errorf("应给出「未被引用」的警告，实际警告: %v", st.Warnings)
	}
}

// TestAnchorReferencedByPFConf 验证正确识别主配置引用。
func TestAnchorReferencedByPFConf(t *testing.T) {
	dir := t.TempDir()
	name := "com.test.ipv6"
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte("pass in proto tcp to port 443\n"), 0o600); err != nil {
		t.Fatalf("准备 anchor 失败: %v", err)
	}

	cfg := platform.DefaultConfig(name, path)
	cfg.PFConfPath = filepath.Join(dir, "pf.conf")
	conf := "scrub-anchor \"com.apple/*\"\nanchor \"" + name + "\"\nload anchor \"" + name + "\" from \"" + path + "\"\n"
	if err := os.WriteFile(cfg.PFConfPath, []byte(conf), 0o600); err != nil {
		t.Fatalf("准备 pf.conf 失败: %v", err)
	}

	c := New(cfg)
	st, err := c.FirewallStatus(context.Background())
	if err != nil {
		t.Fatalf("采集失败: %v", err)
	}
	if !st.Anchor.Referenced {
		t.Error("应识别出主配置引用")
	}
}

// TestPFConfCommentIgnored 验证注释行不算引用。
func TestPFConfCommentIgnored(t *testing.T) {
	dir := t.TempDir()
	conf := "# anchor \"com.test.ipv6\"\n"
	path := filepath.Join(dir, "pf.conf")
	if err := os.WriteFile(path, []byte(conf), 0o600); err != nil {
		t.Fatalf("准备失败: %v", err)
	}
	if pfConfReferences(path, "com.test.ipv6") {
		t.Error("注释行不应被当作引用")
	}
}

// TestParseAnchorList 验证 anchor 列表解析。
func TestParseAnchorList(t *testing.T) {
	out := "com.apple/250.ApplicationFirewall\ncom.macpanel.ipv6\n\n  com.other  \n"
	list := parseAnchorList(out)
	if len(list) != 3 {
		t.Fatalf("应解析出 3 项，实际 %d: %v", len(list), list)
	}
	if list[1] != "com.macpanel.ipv6" {
		t.Errorf("第二项不符: %s", list[1])
	}
	if list[2] != "com.other" {
		t.Errorf("应去除首尾空白: %q", list[2])
	}
}

// TestLoadAverageParsing 验证 vm.loadavg 解析。
func TestLoadAverageParsing(t *testing.T) {
	la, err := loadAverage(context.Background())
	if err != nil {
		t.Fatalf("解析负载失败: %v", err)
	}
	if la.One < 0 || la.Five < 0 || la.Fifteen < 0 {
		t.Error("负载不应为负")
	}
}

func contains(s, sub string) bool {
	return len(sub) == 0 || (len(s) >= len(sub) && indexOf(s, sub) >= 0)
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
