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
	t.Setenv("MACPANEL_LISTEN_ADDR", "127.0.0.1:9000")
	t.Setenv("MACPANEL_ALLOW_REMOTE", "false")
	t.Setenv("MACPANEL_ALLOWED_PEER_UIDS", "502, 503")
	t.Setenv("MACPANEL_DATA_DIR", "/tmp/hlm-test-data")

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
	t.Setenv("MACPANEL_ALLOW_REMOTE", "yes")
	if _, err := LoadFromEnv(ModeServer); err == nil {
		t.Fatal("非法布尔值应报错")
	}
}

// TestLoadFromEnvRejectsBadUIDs 验证非法 UID 列表报错。
func TestLoadFromEnvRejectsBadUIDs(t *testing.T) {
	t.Setenv("MACPANEL_ALLOWED_PEER_UIDS", "502,abc")
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

	// 空主机名等于「所有网卡」。改写成 127.0.0.1 会让已开启的远程访问
	// 悄悄退化成只有本机能连，而校验阶段又按「所有网卡」放行。
	cfg.ListenAddr = ":7788"
	if got := cfg.NormalizedListenAddr(); got != ":7788" {
		t.Errorf("空主机名不应被改写成回环: %s", got)
	}
}

// TestWildcardListenAndLoopbackURL 验证所有网卡监听下的本机入口推导。
func TestWildcardListenAndLoopbackURL(t *testing.T) {
	cases := []struct {
		addr     string
		wildcard bool
		loopback string
	}{
		{"127.0.0.1:7788", false, "http://127.0.0.1:7788"},
		{"0.0.0.0:7788", true, "http://127.0.0.1:7788"},
		{":7788", true, "http://127.0.0.1:7788"},
		{"10.10.10.10:7788", false, ""},
	}
	for _, c := range cases {
		cfg := Defaults()
		cfg.ListenAddr = c.addr
		if got := cfg.IsWildcardListen(); got != c.wildcard {
			t.Errorf("%s: 期望 wildcard=%v，实际 %v", c.addr, c.wildcard, got)
		}
		if got := cfg.LoopbackURL(); got != c.loopback {
			t.Errorf("%s: 本机入口期望 %q，实际 %q", c.addr, c.loopback, got)
		}
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

// TestPublicHostsParsing 验证反向代理场景的额外 Host 配置。
//
// 这是实际踩过的坑：面板经代理以别的域名暴露时，写请求的 Origin
// 是那个域名，而后端只知道自己监听 127.0.0.1 —— 所有 POST 都会被 403。
func TestPublicHostsParsing(t *testing.T) {
	t.Setenv("MACPANEL_PUBLIC_HOSTS", " homelab.example.com:443 , localhost:5199 ,, ")

	cfg, err := LoadFromEnv(ModeServer)
	if err != nil {
		t.Fatalf("加载配置失败: %v", err)
	}

	want := []string{"homelab.example.com:443", "localhost:5199"}
	if len(cfg.PublicHosts) != len(want) {
		t.Fatalf("应解析出 %d 项，实际 %d：%v", len(want), len(cfg.PublicHosts), cfg.PublicHosts)
	}
	for i, w := range want {
		if cfg.PublicHosts[i] != w {
			t.Errorf("第 %d 项应为 %q，实际 %q", i, w, cfg.PublicHosts[i])
		}
	}
}

// TestPublicHostsEmpty 验证未配置时不产生空字符串项。
func TestPublicHostsEmpty(t *testing.T) {
	cfg, err := LoadFromEnv(ModeServer)
	if err != nil {
		t.Fatalf("加载配置失败: %v", err)
	}
	if len(cfg.PublicHosts) != 0 {
		t.Errorf("未配置时不应有额外 Host，实际 %v", cfg.PublicHosts)
	}
}

// writeConf 在临时目录写入配置文件并返回路径。
func writeConf(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "server.conf")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("写入测试配置失败: %v", err)
	}
	return path
}

// TestParseConfigFile 验证注释、空行与各配置项的解析。
func TestParseConfigFile(t *testing.T) {
	path := writeConf(t, `
# 注释
listen = 0.0.0.0:7788

allow_remote = true
public_hosts = a.example.com:443 , b:5199,,
`)
	v, err := ParseConfigFile(path)
	if err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	if v.Listen != "0.0.0.0:7788" {
		t.Errorf("listen 解析错误: %q", v.Listen)
	}
	if v.AllowRemote == nil || !*v.AllowRemote {
		t.Errorf("allow_remote 解析错误: %v", v.AllowRemote)
	}
	if len(v.PublicHosts) != 2 || v.PublicHosts[0] != "a.example.com:443" {
		t.Errorf("public_hosts 解析错误: %v", v.PublicHosts)
	}
}

// TestParseConfigFileMissing 验证文件缺失不算错误（纯默认配置是合法状态）。
func TestParseConfigFileMissing(t *testing.T) {
	v, err := ParseConfigFile(filepath.Join(t.TempDir(), "nope.conf"))
	if err != nil {
		t.Fatalf("缺失文件不应报错: %v", err)
	}
	if v.Listen != "" || v.AllowRemote != nil || v.PublicHosts != nil {
		t.Errorf("缺失文件应返回零值，实际 %+v", v)
	}
}

// TestParseConfigFileErrors 验证语法错误与未知键必须报错。
//
// 未知键静默忽略会让用户以为改生效了，实际还在跑默认值。
func TestParseConfigFileErrors(t *testing.T) {
	cases := map[string]string{
		"缺等号":  "listen 0.0.0.0:7788\n",
		"未知键":  "lstien = 0.0.0.0:7788\n",
		"布尔非法": "allow_remote = yes\n",
		"空地址":  "listen =\n",
	}
	for name, body := range cases {
		if _, err := ParseConfigFile(writeConf(t, body)); err == nil {
			t.Errorf("%s 应报错", name)
		}
	}
}

// TestLoadConfigFilePrecedence 验证 默认值 < 文件 < 环境变量 的覆盖顺序。
func TestLoadConfigFilePrecedence(t *testing.T) {
	path := writeConf(t, "listen = 0.0.0.0:7788\nallow_remote = true\n")

	cfg, err := Load(LoadOptions{Mode: ModeServer, ConfigFile: path})
	if err != nil {
		t.Fatalf("加载失败: %v", err)
	}
	if cfg.ListenAddr != "0.0.0.0:7788" || !cfg.AllowRemote {
		t.Errorf("文件未生效: addr=%s allowRemote=%v", cfg.ListenAddr, cfg.AllowRemote)
	}
	if cfg.ConfigFile != path {
		t.Errorf("ConfigFile 未记录: %s", cfg.ConfigFile)
	}

	t.Setenv("MACPANEL_LISTEN_ADDR", "127.0.0.1:9002")
	cfg, err = Load(LoadOptions{Mode: ModeServer, ConfigFile: path})
	if err != nil {
		t.Fatalf("加载失败: %v", err)
	}
	if cfg.ListenAddr != "127.0.0.1:9002" {
		t.Errorf("环境变量应覆盖文件，实际 %s", cfg.ListenAddr)
	}
}

// TestLoadConfigFileRejectsRemoteWithoutOptIn 验证文件里开了非回环监听
// 却没开 allow_remote 时直接失败，而不是启动后静默降级。
func TestLoadConfigFileRejectsRemoteWithoutOptIn(t *testing.T) {
	path := writeConf(t, "listen = 0.0.0.0:7788\n")
	if _, err := Load(LoadOptions{Mode: ModeServer, ConfigFile: path}); err == nil {
		t.Fatal("非回环监听缺少 allow_remote=true 时应报错")
	}
}

// TestWriteConfigFileRoundTrip 验证写入后能被原样读回，且权限为 0640。
func TestWriteConfigFileRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "server.conf")
	yes := true
	want := FileValues{Listen: "0.0.0.0:7788", AllowRemote: &yes, PublicHosts: []string{"a:1"}}

	if err := WriteConfigFile(path, want); err != nil {
		t.Fatalf("写入失败: %v", err)
	}
	got, err := ParseConfigFile(path)
	if err != nil {
		t.Fatalf("读回失败: %v", err)
	}
	if got.Listen != want.Listen || got.AllowRemote == nil || !*got.AllowRemote ||
		len(got.PublicHosts) != 1 || got.PublicHosts[0] != "a:1" {
		t.Errorf("往返不一致: %+v", got)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("读取文件信息失败: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o640 {
		t.Errorf("配置文件权限应为 0640，实际 %o", perm)
	}
}
