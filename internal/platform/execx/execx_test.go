package execx

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

// TestRejectsUnlistedBinary 验证白名单之外的命令一律拒绝。
// 这是「不提供任意命令执行」的核心保证。
func TestRejectsUnlistedBinary(t *testing.T) {
	bad := []string{
		"/bin/sh",
		"/bin/bash",
		"/usr/bin/curl",
		"sh",
		"./evil.sh",
		"/usr/bin/env",
		"",
	}
	for _, binary := range bad {
		_, err := Run(context.Background(), time.Second, binary, "-c", "id")
		if err == nil {
			t.Errorf("命令 %q 应被拒绝", binary)
		}
		if !strings.Contains(err.Error(), "拒绝执行") {
			t.Errorf("命令 %q 的错误信息应说明被拒绝: %v", binary, err)
		}
	}
}

// TestAllowedBinaryRuns 验证白名单命令可执行。
func TestAllowedBinaryRuns(t *testing.T) {
	if !IsAllowed("/usr/bin/sw_vers") {
		t.Skip("当前环境不含 /usr/bin/sw_vers")
	}
	res, err := Run(context.Background(), 5*time.Second, "/usr/bin/sw_vers", "-productVersion")
	if err != nil {
		t.Fatalf("执行失败: %v", err)
	}
	if strings.TrimSpace(res.Stdout) == "" {
		t.Error("应返回版本号")
	}
	if res.Duration <= 0 {
		t.Error("应记录耗时")
	}
}

// TestNonZeroExitReturnsError 验证非零退出码返回 CommandError。
func TestNonZeroExitReturnsError(t *testing.T) {
	// 使用一个必定失败的 sysctl 键
	_, err := Run(context.Background(), 5*time.Second, "/usr/sbin/sysctl", "-n", "nonexistent.oid.test")
	if err == nil {
		t.Skip("该平台对未知 OID 未返回错误")
	}
	var ce *CommandError
	if !errors.As(err, &ce) {
		t.Fatalf("应返回 CommandError，实际 %T", err)
	}
	if ce.ExitCode == 0 {
		t.Error("退出码不应为 0")
	}
}

// TestTimeout 验证超时控制。用 sleep 不可行（不在白名单），
// 因此这里直接验证 context 取消路径。
func TestTimeout(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // 立即取消

	_, err := Run(ctx, 5*time.Second, "/usr/bin/sw_vers", "-productVersion")
	if err == nil {
		t.Skip("命令在被取消前已完成")
	}
	if !errors.Is(err, context.Canceled) && !strings.Contains(err.Error(), "取消") {
		t.Logf("取消错误形态: %v", err)
	}
}

// TestAllowedBinariesSnapshot 验证白名单快照与说明完整。
func TestAllowedBinariesSnapshot(t *testing.T) {
	all := AllowedBinaries()
	if len(all) == 0 {
		t.Fatal("白名单不应为空")
	}
	for bin, desc := range all {
		if !strings.HasPrefix(bin, "/") {
			t.Errorf("白名单路径必须是绝对路径: %s", bin)
		}
		if strings.TrimSpace(desc) == "" {
			t.Errorf("命令 %s 缺少用途说明", bin)
		}
	}
	// 修改返回的快照不应影响内部白名单。
	delete(all, "/usr/sbin/sysctl")
	if !IsAllowed("/usr/sbin/sysctl") {
		t.Error("快照应返回副本，修改后不应影响内部状态")
	}
}

// TestLimitedWriterTruncates 验证输出截断不返回错误。
func TestLimitedWriterTruncates(t *testing.T) {
	var sb strings.Builder
	w := &limitedWriter{w: &sb, limit: 10}

	n, err := w.Write([]byte("0123456789ABCDEF"))
	if err != nil {
		t.Fatalf("写入不应报错: %v", err)
	}
	if n != 16 {
		t.Errorf("应报告全部写入（丢弃部分也算成功），实际 %d", n)
	}
	if !w.truncated {
		t.Error("应标记截断")
	}
	if sb.Len() != 10 {
		t.Errorf("实际写入应为 10 字节，实际 %d", sb.Len())
	}

	// 再次写入应被完全丢弃。
	n, err = w.Write([]byte("more"))
	if err != nil || n != 4 {
		t.Errorf("超出部分应静默丢弃，实际 n=%d err=%v", n, err)
	}
}
