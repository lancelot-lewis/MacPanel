// Package config 负责加载与校验运行配置。
//
// 设计原则：
//  1. 默认值必须是安全值：只监听 127.0.0.1、远程访问关闭、路径固定在系统目录。
//  2. 校验失败必须直接返回错误终止启动，不做「猜一个值」的兜底。
//  3. 远程访问相关的配置项在未显式开启时一律不生效。
package config

import (
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// Mode 进程角色。
type Mode string

const (
	ModeServer Mode = "server"
	ModeAgent  Mode = "agent"
)

// 系统级安装路径。PKG 安装器会把文件放到这些位置；开发环境用 DefaultDev* 覆盖。
const (
	SystemConfigDir  = "/Library/Application Support/HomeLabManager"
	SystemDataDir    = "/Library/Application Support/HomeLabManager/data"
	SystemLogDir     = "/Library/Logs/HomeLabManager"
	SystemRunDir     = "/var/run/homelab-manager"
	SystemSocketPath = SystemRunDir + "/agent.sock"
	SystemTokenPath  = SystemConfigDir + "/ipc.token"
	SystemAnchorName = "com.homelab.manager.ipv6"
	SystemAnchorPath = "/etc/pf.anchors/" + SystemAnchorName
)

// Defaults 返回一组安全默认配置。
func Defaults() Config {
	return Config{
		ListenAddr: "127.0.0.1:7788",
		SocketPath: SystemSocketPath,
		TokenPath:  SystemTokenPath,
		DataDir:    SystemDataDir,
		LogDir:     SystemLogDir,
		AnchorName: SystemAnchorName,
		AnchorPath: SystemAnchorPath,
		// IPC / HTTP 超时
		IPCTimeout:      5 * time.Second,
		ShutdownTimeout: 10 * time.Second,
		// 远程访问默认完全关闭
		AllowRemote: false,
		// 允许调用 Agent 的 UID 白名单（为空则仅允许当前进程 euid 相同者）
		AllowedPeerUIDs: nil,
	}
}

// Config 是 Server 与 Agent 共用的运行配置。
type Config struct {
	Mode       Mode
	ListenAddr string
	SocketPath string
	TokenPath  string
	DataDir    string
	LogDir     string
	AnchorName string
	AnchorPath string

	IPCTimeout      time.Duration
	ShutdownTimeout time.Duration

	// AllowRemote 为 true 时才允许监听非回环地址。默认 false。
	AllowRemote bool

	// PublicHosts 是反向代理/开发代理场景下额外允许的 Host（含端口）。
	//
	// 为什么需要：写请求会校验 Origin，而 Origin 是浏览器地址栏的 host。
	// 若面板经反向代理以 https://homelab.example.com 暴露，Origin 就是它，
	// 而后端只知道自己监听 127.0.0.1:7788 —— 此时所有 POST/PUT 都会被 403。
	// 这里显式列出可信的外部访问地址，避免为了「能用」而放宽整个校验。
	PublicHosts []string

	// AllowedPeerUIDs 是允许连接 Agent Socket 的额外 UID。
	// Agent 自身 euid 与 Server 的 euid 始终允许。空表示只信任本进程 euid。
	AllowedPeerUIDs []uint32

	// ServeWeb 表示是否由 Server 提供内嵌前端资源。
	ServeWeb bool
}

// LoadFromEnv 在 Defaults 基础上叠加 HLM_* 环境变量，然后执行校验。
func LoadFromEnv(mode Mode) (Config, error) {
	cfg := Defaults()
	cfg.Mode = mode
	cfg.ServeWeb = true

	if v := os.Getenv("HLM_LISTEN_ADDR"); v != "" {
		cfg.ListenAddr = v
	}
	if v := os.Getenv("HLM_SOCKET_PATH"); v != "" {
		cfg.SocketPath = v
	}
	if v := os.Getenv("HLM_TOKEN_PATH"); v != "" {
		cfg.TokenPath = v
	}
	if v := os.Getenv("HLM_DATA_DIR"); v != "" {
		cfg.DataDir = v
	}
	if v := os.Getenv("HLM_LOG_DIR"); v != "" {
		cfg.LogDir = v
	}
	if v := os.Getenv("HLM_ANCHOR_PATH"); v != "" {
		cfg.AnchorPath = v
	}
	if v := os.Getenv("HLM_ALLOW_REMOTE"); v != "" {
		b, err := strconv.ParseBool(v)
		if err != nil {
			return cfg, fmt.Errorf("HLM_ALLOW_REMOTE 不是合法布尔值: %q", v)
		}
		cfg.AllowRemote = b
	}
	if v := os.Getenv("HLM_PUBLIC_HOSTS"); v != "" {
		cfg.PublicHosts = splitAndTrim(v)
	}
	if v := os.Getenv("HLM_ALLOWED_PEER_UIDS"); v != "" {
		uids, err := parseUIDList(v)
		if err != nil {
			return cfg, err
		}
		cfg.AllowedPeerUIDs = uids
	}
	if err := cfg.Validate(); err != nil {
		return cfg, err
	}
	return cfg, nil
}

// splitAndTrim 按逗号切分并去空白，丢弃空项。
func splitAndTrim(raw string) []string {
	parts := strings.Split(raw, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}

func parseUIDList(raw string) ([]uint32, error) {
	parts := strings.Split(raw, ",")
	out := make([]uint32, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		n, err := strconv.ParseUint(p, 10, 32)
		if err != nil {
			return nil, fmt.Errorf("HLM_ALLOWED_PEER_UIDS 含非法 UID %q", p)
		}
		out = append(out, uint32(n))
	}
	return out, nil
}

// Validate 校验配置的合法性与安全性。
func (c *Config) Validate() error {
	switch c.Mode {
	case ModeServer, ModeAgent:
	default:
		return fmt.Errorf("未知运行模式: %q", c.Mode)
	}

	if c.SocketPath == "" || !filepath.IsAbs(c.SocketPath) {
		return fmt.Errorf("SocketPath 必须是非空绝对路径，当前为 %q", c.SocketPath)
	}
	if c.DataDir == "" || !filepath.IsAbs(c.DataDir) {
		return fmt.Errorf("DataDir 必须是非空绝对路径，当前为 %q", c.DataDir)
	}
	if c.AnchorPath == "" || !filepath.IsAbs(c.AnchorPath) {
		return fmt.Errorf("AnchorPath 必须是非空绝对路径，当前为 %q", c.AnchorPath)
	}
	if c.AnchorName == "" || strings.ContainsAny(c.AnchorName, "/ \t\n") {
		return fmt.Errorf("AnchorName 非法: %q", c.AnchorName)
	}
	if c.IPCTimeout <= 0 || c.IPCTimeout > time.Minute {
		return fmt.Errorf("IPCTimeout 必须在 (0, 1m] 区间内，当前为 %s", c.IPCTimeout)
	}

	if c.Mode == ModeServer {
		if err := validateListenAddr(c.ListenAddr, c.AllowRemote); err != nil {
			return err
		}
	}
	return nil
}

// validateListenAddr 强制「默认只监听回环」这一安全不变量。
// 只填端口时补成 127.0.0.1；非回环地址必须同时打开 AllowRemote。
func validateListenAddr(addr string, allowRemote bool) error {
	if addr == "" {
		return fmt.Errorf("ListenAddr 不能为空")
	}
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return fmt.Errorf("ListenAddr 格式非法（应为 host:port）: %w", err)
	}
	if port == "" {
		return fmt.Errorf("ListenAddr 缺少端口: %q", addr)
	}
	if _, err := strconv.ParseUint(port, 10, 16); err != nil {
		return fmt.Errorf("ListenAddr 端口非法: %q", port)
	}

	if host == "" || host == "0.0.0.0" || host == "::" {
		if !allowRemote {
			return fmt.Errorf("ListenAddr=%q 会监听所有网卡，但未开启远程访问；"+
				"如需远程访问请显式设置 HLM_ALLOW_REMOTE=true 并完成认证配置", addr)
		}
		return nil
	}

	ip := net.ParseIP(host)
	if ip == nil {
		// 允许 localhost 字面量
		if strings.EqualFold(host, "localhost") {
			return nil
		}
		return fmt.Errorf("ListenAddr 主机必须是 IP 或 localhost，当前为 %q", host)
	}
	if !ip.IsLoopback() && !allowRemote {
		return fmt.Errorf("ListenAddr=%q 不是回环地址，但未开启远程访问", addr)
	}
	return nil
}

// IsLoopbackListen 判断配置是否只监听回环地址。
func (c *Config) IsLoopbackListen() bool {
	host, _, err := net.SplitHostPort(c.ListenAddr)
	if err != nil {
		return false
	}
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// DatabasePath 返回 SQLite 文件路径。
func (c *Config) DatabasePath() string {
	return filepath.Join(c.DataDir, "homelab.db")
}

// NormalizedListenAddr 把 localhost 换成 127.0.0.1，便于日志与监听一致。
func (c *Config) NormalizedListenAddr() string {
	host, port, err := net.SplitHostPort(c.ListenAddr)
	if err != nil {
		return c.ListenAddr
	}
	if strings.EqualFold(host, "localhost") {
		return net.JoinHostPort("127.0.0.1", port)
	}
	if host == "" {
		return net.JoinHostPort("127.0.0.1", port)
	}
	return net.JoinHostPort(host, port)
}
