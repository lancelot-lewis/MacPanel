package firewalld

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/lancelot-lewis/MacPanel/internal/model"
)

// newManagerWithPFConf 构造一个带主配置路径的 Manager，用于验证前置校验。
func newManagerWithPFConf(t *testing.T, confPath string) (*Manager, *fakeApplier) {
	t.Helper()
	applier := newFakeApplier()
	m, err := New(Options{
		Store:          NewMemoryStore(),
		Applier:        applier,
		AnchorName:     "com.test.ipv6",
		RulePath:       "/tmp/test-anchor",
		PFConfPath:     confPath,
		ConfirmTimeout: 60 * time.Second,
		Now:            func() time.Time { return time.Unix(0, 0).UTC() },
	})
	if err != nil {
		t.Fatalf("创建 Manager 失败: %v", err)
	}
	return m, applier
}

// TestApplyRejectsWhenAnchorNotReferenced 验证主配置未引用 anchor 时拒绝应用。
//
// 回归：早期只做了 `pfctl -a <anchor> -f <file>` 就报成功。anchor 未被
// /etc/pf.conf 引用时，规则加载成功却永远不会被任何包匹配——界面显示
// 「已应用」，实际没有拦任何流量，属于典型的假状态。
func TestApplyRejectsWhenAnchorNotReferenced(t *testing.T) {
	dir := t.TempDir()
	conf := filepath.Join(dir, "pf.conf")
	if err := os.WriteFile(conf, []byte("scrub-anchor \"com.apple/*\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	m, applier := newManagerWithPFConf(t, conf)
	_, err := m.Apply(context.Background(), "pass in quick inet6 all", 1, "")
	if err == nil {
		t.Fatal("主配置未引用 anchor 时应拒绝应用，实际返回成功")
	}
	if !strings.Contains(err.Error(), "未在") || !strings.Contains(err.Error(), "com.test.ipv6") {
		t.Errorf("错误信息应说明 anchor 未被引用，实际：%v", err)
	}
	if got := model.CodeOf(err); got != model.CodeConflict {
		t.Errorf("错误码应为 %s，实际 %s", model.CodeConflict, got)
	}
	if n := len(applier.loadCalls); n != 0 {
		t.Errorf("前置校验失败时不应加载任何规则，实际加载了 %d 次", n)
	}
}

// TestApplyProceedsWhenAnchorReferenced 验证主配置已引用时正常放行。
func TestApplyProceedsWhenAnchorReferenced(t *testing.T) {
	dir := t.TempDir()
	conf := filepath.Join(dir, "pf.conf")
	body := "anchor \"com.test.ipv6\"\nload anchor \"com.test.ipv6\" from \"/tmp/test-anchor\"\n"
	if err := os.WriteFile(conf, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}

	m, applier := newManagerWithPFConf(t, conf)
	if _, err := m.Apply(context.Background(), "pass in quick inet6 all", 1, ""); err != nil {
		t.Fatalf("已引用时应允许应用，实际失败：%v", err)
	}
	if len(applier.loadCalls) == 0 {
		t.Error("应实际调用加载规则")
	}
}

// TestApplySkipsCheckWhenPFConfUnreadable 验证无法读取主配置时不阻断。
//
// 读不到 ≠ 未引用。非 root 或路径未配置时必须放行，
// 由状态接口以 warnings 形式如实告知，不能在这里假装通过或误判失败。
func TestApplySkipsCheckWhenPFConfUnreadable(t *testing.T) {
	m, _ := newManagerWithPFConf(t, "/nonexistent/pf.conf")
	if _, err := m.Apply(context.Background(), "pass in quick inet6 all", 1, ""); err != nil {
		t.Fatalf("无法读取主配置时不应阻断应用，实际失败：%v", err)
	}
}

// TestAnchorReferencedIgnoresComments 验证注释行不算引用。
func TestAnchorReferencedIgnoresComments(t *testing.T) {
	dir := t.TempDir()
	conf := filepath.Join(dir, "pf.conf")
	body := "# anchor \"com.test.ipv6\"\nscrub-anchor \"com.apple/*\"\n"
	if err := os.WriteFile(conf, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	ref, known := anchorReferenced(conf, "com.test.ipv6")
	if !known {
		t.Fatal("文件可读时应返回 known=true")
	}
	if ref {
		t.Error("注释中的 anchor 不应算作引用")
	}
}
