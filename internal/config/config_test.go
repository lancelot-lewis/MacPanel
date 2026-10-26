package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestDefaultsAreSafe 验证默认配置是安全值：只监听回环、禁止远程、路径为绝对路径。
func TestDefaultsAreSafe(t *testing.T) {
	cfg := Defaults()

	if cfg.AllowRemote {
		t.Error("默认必须关闭远程访问")
	}
	if !strings.HasPrefix(cfg.ListenAddr, "127.0.0.1:") {
		t.Errorf("默认必须监听 127.0.0.1，实际 %s", cfg.ListenAddr)
	}
	if cfg.SocketPath != SystemSocketPath {
		t.Errorf("Socket 路径不符: %s", cfg.SocketPath)
	}
	for name, p := range map[string]string{
		"socket": cfg.SocketPath,
		"data":   cfg.DataDir,
		"anchor": cfg.AnchorPath,
	} {
		if !filepath.IsAbs(p) {
			t.Errorf("%s 路径必须是绝对路径: %s", name, p)
		}
	}
}

// TestValidateRejectsRemoteWithoutOptIn 验证非回环地址在未开启远程时被拒绝。
func TestValidateRejectsRemoteWithoutOptIn(t *testing.T) {
	cases := []string{
		"0.0.0.0:7788",
		":7788",
		"192.168.1.10:7788",
		"[::]:7788",
		"10.0.0.1:7788",
	}
	for _, addr := range cases {
		cfg := Defaults()
		cfg.Mode = ModeServer
		cfg.ListenAddr = addr
		cfg.AllowRemote = false

		if err := cfg.Validate(); err == nil {
			t.Errorf("地址 %s 在未开启远程时应校验失败", addr)
		}
	}
}

// TestValidateAllowsRemoteWhenOptedIn 验证显式开启远程后非回环地址可用。
func TestValidateAllowsRemoteWhenOptedIn(t *testing.T) {
	for _, addr := range []string{"0.0.0.0:7788", "192.168.1.10:7788"} {
		cfg := Defaults()
		cfg.Mode = ModeServer
		cfg.ListenAddr = addr
		cfg.AllowRemote = true

		if err := cfg.Validate(); err != nil {
			t.Errorf("地址 %s 在开启远程后应校验通过，实际: %v", addr, err)
		}
	}
}

// TestValidateListenAddrFormats 验证地址格式校验。
func TestValidateListenAddrFormats(t *testing.T) {
	bad := []string{
		"",
		"127.0.0.1",        // 缺端口
		"127.0.0.1:abc",    // 端口非数字
		"127.0.0.1:99999",  // 端口越界
		"example.com:7788", // 非 IP 且非 localhost
	}
	for _, addr := range bad {
		cfg := Defaults()
		cfg.Mode = ModeServer
		cfg.ListenAddr = addr
		if err := cfg.Validate(); err == nil {
			t.Errorf("地址 %q 应校验失败", addr)
		}
	}

	good := []string{"127.0.0.1:7788", "localhost:7788", "[::1]:7788"}
	for _, addr := range good {
		cfg := Defaults()
		cfg.Mode = ModeServer
		cfg.ListenAddr = addr
		if err := cfg.Validate(); err != nil {
			t.Errorf("地址 %q 应校验通过，实际: %v", addr, err)
		}
	}
}

// TestValidateRejectsBadMode 验证未知模式被拒绝。
func TestValidateRejectsBadMode(t *testing.T) {
	cfg := Defaults()
	cfg.Mode = "daemon"
	if err := cfg.Validate(); err == nil {
		t.Fatal("未知模式应校验失败")
	}
}

// TestValidateRejectsRelativePaths 验证相对路径被拒绝。
func TestValidateRejectsRelativePaths(t *testing.T) {
	cfg := Defaults()
	cfg.Mode = ModeAgent
	cfg.SocketPath = "run/agent.sock"
	if err := cfg.Validate(); err == nil {
		t.Fatal("相对 Socket 路径应校验失败")
	}
}

// TestLoadFromEnv 验证环境变量覆盖与解析。
func TestLoadFromEnv(t *testing.T) {
	t.Setenv("HLM_LISTEN_ADDR", "127.0.0.1:9000")
	t.Setenv("HLM_ALLOW_REMOTE", "false")
	t.Setenv("HLM_ALLOWED_PEER_UIDS", "502, 503")
	t.Setenv("HLM_DATA_DIR", "/tmp/hlm-test-data")

	cfg, err := LoadFromEnv(ModeServer)
	if err != nil {
		t.Fatalf("加载配置失败: %v", err)
	}
	if cfg.ListenAddr != "127.0.0.1:9000" {
		t.Errorf("ListenAddr 未生效: %s", cfg.ListenAddr)
	}
	if cfg.DataDir != "/tmp/hlm-test-data" {
		t.Errorf("DataDir 未生效: %s", cfg.DataDir)
	}
	if len(cfg.AllowedPeerUIDs) != 2 || cfg.AllowedPeerUIDs[0] != 502 || cfg.AllowedPeerUIDs[1] != 503 {
		t.Errorf("AllowedPeerUIDs 解析错误: %v", cfg.AllowedPeerUIDs)
	}
}

// TestLoadFromEnvRejectsBadBool 验证非法布尔值直接报错，不静默降级。
func TestLoadFromEnvRejectsBadBool(t *testing.T) {
	t.Setenv("HLM_ALLOW_REMOTE", "yes")
	if _, err := LoadFromEnv(ModeServer); err == nil {
		t.Fatal("非法布尔值应报错")
	}
}

// TestLoadFromEnvRejectsBadUIDs 验证非法 UID 列表报错。
func TestLoadFromEnvRejectsBadUIDs(t *testing.T) {
	t.Setenv("HLM_ALLOWED_PEER_UIDS", "502,abc")
	if _, err := LoadFromEnv(ModeAgent); err == nil {
		t.Fatal("非法 UID 应报错")
	}
}

// TestTokenRoundTrip 验证令牌生成、写入、读取、比较。
func TestTokenRoundTrip(t *testing.T) {
	tok, err := GenerateToken()
	if err != nil {
		t.Fatalf("生成令牌失败: %v", err)
	}
	if len(tok.String()) != tokenBytes*2 {
		t.Fatalf("令牌十六进制长度不符: %d", len(tok.String()))
	}

	path := filepath.Join(t.TempDir(), "token")
	if err := WriteTokenFile(path, tok); err != nil {
		t.Fatalf("写入令牌失败: %v", err)
	}

	loaded, err := LoadToken(path)
	if err != nil {
		t.Fatalf("读取令牌失败: %v", err)
	}
	if !tok.Equals(loaded) {
		t.Fatal("令牌往返后不一致")
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("读取文件信息失败: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Fatalf("令牌文件权限应为 0600，实际 %o", perm)
	}
}

// TestTokenMismatch 验证不同令牌不相等。
func TestTokenMismatch(t *testing.T) {
	a, _ := GenerateToken()
	b, _ := GenerateToken()
	if a.Equals(b) {
		t.Fatal("两个随机令牌不应相等")
	}
	if a.MatchesHex(b.String()) {
		t.Fatal("错误令牌不应匹配")
	}
	if !a.MatchesHex(a.String()) {
		t.Fatal("正确令牌应匹配")
	}
}

// TestParseTokenRejectsWeak 验证弱令牌被拒绝。
func TestParseTokenRejectsWeak(t *testing.T) {
	cases := []string{
		"",
		"short",
		"zzzz",                              // 非十六进制
		strings.Repeat("a", tokenBytes*2-2), // 少一字节
		strings.Repeat("a", tokenBytes*2+2), // 多一字节
	}
	for _, c := range cases {
		if _, err := ParseToken(c); err == nil {
			t.Errorf("弱令牌 %q 应被拒绝", c)
		}
	}
}

// TestLoadTokenMissingFile 验证缺失文件返回可诊断错误。
func TestLoadTokenMissingFile(t *testing.T) {
	_, err := LoadToken(filepath.Join(t.TempDir(), "nope"))
	if err == nil {
		t.Fatal("缺失文件应报错")
	}
	if !strings.Contains(err.Error(), "不存在") {
		t.Errorf("错误信息应说明文件不存在: %v", err)
	}
}

// TestIPCTimeoutBounds 验证超时参数边界。
func TestIPCTimeoutBounds(t *testing.T) {
	cfg := Defaults()
	cfg.Mode = ModeServer
	cfg.IPCTimeout = 0
	if err := cfg.Validate(); err == nil {
		t.Fatal("超时为 0 应校验失败")
	}

	cfg.IPCTimeout = 2 * time.Minute
	if err := cfg.Validate(); err == nil {
		t.Fatal("超时超过 1 分钟应校验失败")
	}
}

// TestNormalizedListenAddr 验证 localhost 归一化。
func TestNormalizedListenAddr(t *testing.T) {
	cfg := Defaults()
	cfg.ListenAddr = "localhost:7788"
	if got := cfg.NormalizedListenAddr(); got != "127.0.0.1:7788" {
		t.Errorf("归一化结果不符: %s", got)
	}

	cfg.ListenAddr = "[::1]:7788"
	if got := cfg.NormalizedListenAddr(); got != "[::1]:7788" {
		t.Errorf("IPv6 回环地址应保持不变: %s", got)
	}
}

// TestIsLoopbackListen 验证回环判定。
func TestIsLoopbackListen(t *testing.T) {
	cases := map[string]bool{
		"127.0.0.1:7788": true,
		"localhost:7788": true,
		"[::1]:7788":     true,
		"0.0.0.0:7788":   false,
		"192.168.1.5:80": false,
	}
	for addr, want := range cases {
		cfg := Defaults()
		cfg.ListenAddr = addr
		if got := cfg.IsLoopbackListen(); got != want {
			t.Errorf("%s: 期望 %v，实际 %v", addr, want, got)
		}
	}
}
