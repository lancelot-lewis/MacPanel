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
	SystemConfigDir  = "/Library/Application Support/MacPanel"
	SystemDataDir    = "/Library/Application Support/MacPanel/data"
	SystemLogDir     = "/Library/Logs/MacPanel"
	SystemRunDir     = "/var/run/macpanel"
	SystemSocketPath = SystemRunDir + "/agent.sock"
	SystemTokenPath  = SystemConfigDir + "/ipc.token"
	// SystemServerConfPath 保存监听地址与远程访问开关。
	//
	// 刻意不放在 launchd plist 里：plist 由 postinstall 每次按模板重写，
	// 用户改过的监听地址会在升级时丢失；而 pkg 升级不应覆盖用户配置。
	SystemServerConfPath = SystemConfigDir + "/server.conf"
	SystemAnchorName     = "com.macpanel.ipv6"
	SystemAnchorPath     = "/etc/pf.anchors/" + SystemAnchorName
	// SystemPFConfPath 是 PF 主规则集配置。
	// 应用规则前要确认它引用了本应用的 anchor —— 未引用时规则不会被评估。
	SystemPFConfPath = "/etc/pf.conf"
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
		PFConfPath: SystemPFConfPath,
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
	// PFConfPath 是 PF 主规则集配置路径。应用规则前会检查它是否引用 AnchorName，
	// 未引用则拒绝应用（否则规则加载成功但永远不会被任何包匹配）。
	PFConfPath string

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

	// SocketGID 是 Agent Socket 文件的属组。非 0 时把 Socket 的组改成它。
	//
	// 为什么必须能改：Socket 由 root 创建，默认继承 root:wheel 且权限 0660。
	// Server 以 _macpanel 运行，而它不在 wheel 组里，others 位是 ---，
	// 结果是「连不上 Socket」——目录权限正确也没用，因为卡在 socket 文件上。
	// Socket 每次启动都会重建，所以只能在代码里改，不能靠安装脚本补一次。
	SocketGID uint32

	// ServeWeb 表示是否由 Server 提供内嵌前端资源。
	ServeWeb bool

	// ConfigFile 是本进程实际读取的配置文件路径（仅用于诊断输出）。
	// 为空表示没有配置文件，全部取值来自环境变量与默认值。
	ConfigFile string
}

// LoadOptions 描述一次配置加载。
type LoadOptions struct {
	Mode Mode
	// ConfigFile 是 server.conf 路径，为空表示跳过文件。
	ConfigFile string
}

// Load 按「默认值 → 配置文件 → 环境变量」的顺序叠加，最后执行校验。
//
// 环境变量优先级最高：临时改一个值做排障时不必动文件；而常驻配置放文件里，
// 因为它能在 pkg 升级后存活。
func Load(opts LoadOptions) (Config, error) {
	cfg := Defaults()
	cfg.Mode = opts.Mode
	cfg.ServeWeb = true
	cfg.ConfigFile = opts.ConfigFile

	if opts.ConfigFile != "" {
		fv, err := ParseConfigFile(opts.ConfigFile)
		if err != nil {
			return cfg, err
		}
		if fv.Listen != "" {
			cfg.ListenAddr = fv.Listen
		}
		if fv.AllowRemote != nil {
			cfg.AllowRemote = *fv.AllowRemote
		}
		if fv.PublicHosts != nil {
			cfg.PublicHosts = fv.PublicHosts
		}
	}

	if err := applyEnvOverrides(&cfg); err != nil {
		return cfg, err
	}

	if err := cfg.Validate(); err != nil {
		return cfg, err
	}
	return cfg, nil
}

// LoadFromEnv 只叠加环境变量（不读配置文件），用于测试与纯 env 场景。
func LoadFromEnv(mode Mode) (Config, error) {
	return Load(LoadOptions{Mode: mode})
}

// applyEnvOverrides 把 MACPANEL_* 环境变量叠加到 cfg 上。
func applyEnvOverrides(cfg *Config) error {
	if v := os.Getenv("MACPANEL_LISTEN_ADDR"); v != "" {
		cfg.ListenAddr = v
	}
	if v := os.Getenv("MACPANEL_SOCKET_PATH"); v != "" {
		cfg.SocketPath = v
	}
	if v := os.Getenv("MACPANEL_TOKEN_PATH"); v != "" {
		cfg.TokenPath = v
	}
	if v := os.Getenv("MACPANEL_DATA_DIR"); v != "" {
		cfg.DataDir = v
	}
	if v := os.Getenv("MACPANEL_LOG_DIR"); v != "" {
		cfg.LogDir = v
	}
	if v := os.Getenv("MACPANEL_ANCHOR_PATH"); v != "" {
		cfg.AnchorPath = v
	}
	if v := os.Getenv("MACPANEL_PF_CONF_PATH"); v != "" {
		cfg.PFConfPath = v
	}
	if v := os.Getenv("MACPANEL_ALLOW_REMOTE"); v != "" {
		b, err := strconv.ParseBool(v)
		if err != nil {
			return fmt.Errorf("MACPANEL_ALLOW_REMOTE 不是合法布尔值: %q", v)
		}
		cfg.AllowRemote = b
	}
	if v := os.Getenv("MACPANEL_PUBLIC_HOSTS"); v != "" {
		cfg.PublicHosts = splitAndTrim(v)
	}
	if v := os.Getenv("MACPANEL_SOCKET_GID"); v != "" {
		gid, err := strconv.ParseUint(v, 10, 32)
		if err != nil {
			return fmt.Errorf("MACPANEL_SOCKET_GID 不是合法 GID: %w", err)
		}
		cfg.SocketGID = uint32(gid)
	}
	if v := os.Getenv("MACPANEL_ALLOWED_PEER_UIDS"); v != "" {
		uids, err := parseUIDList(v)
		if err != nil {
			return err
		}
		cfg.AllowedPeerUIDs = uids
	}
	return nil
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
			return nil, fmt.Errorf("MACPANEL_ALLOWED_PEER_UIDS 含非法 UID %q", p)
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
		if err := ValidateListenAddr(c.ListenAddr, c.AllowRemote); err != nil {
			return err
		}
	}
	return nil
}

// ValidateListenAddr 强制「默认只监听回环」这一安全不变量。
// 空主机名、0.0.0.0、:: 视为监听所有网卡；这些与非回环 IP 一样，
// 必须显式打开 AllowRemote 才能通过校验。
func ValidateListenAddr(addr string, allowRemote bool) error {
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
				"如需远程访问请显式设置 MACPANEL_ALLOW_REMOTE=true 并完成认证配置", addr)
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

// IsLoopbackAddr 判断地址是否只监听回环。
func IsLoopbackAddr(addr string) bool {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return false
	}
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// IsLoopbackListen 判断配置是否只监听回环地址。
func (c *Config) IsLoopbackListen() bool {
	return IsLoopbackAddr(c.ListenAddr)
}

// IsWildcardListen 判断是否监听所有网卡（0.0.0.0 / :: / 空主机名）。
func (c *Config) IsWildcardListen() bool {
	host, _, err := net.SplitHostPort(c.ListenAddr)
	if err != nil {
		return false
	}
	return host == "" || host == "0.0.0.0" || host == "::"
}

// LoopbackURL 返回可用的本机访问地址；回环没有被监听时返回空字符串。
//
// 用途：远程模式下初次设置密码只接受回环来源，启动日志必须把可用的
// 本机地址打出来，否则用户看着「服务已启动」却找不到入口。
func (c *Config) LoopbackURL() string {
	_, port, err := net.SplitHostPort(c.ListenAddr)
	if err != nil {
		return ""
	}
	if c.IsLoopbackListen() || c.IsWildcardListen() {
		return "http://127.0.0.1:" + port
	}
	return ""
}

// DatabasePath 返回 SQLite 文件路径。
func (c *Config) DatabasePath() string {
	return filepath.Join(c.DataDir, "macpanel.db")
}

// NormalizedListenAddr 统一监听地址写法，便于日志与监听保持一致。
//
// 只把 localhost 换成 127.0.0.1。空主机名（":7788"）代表「所有网卡」，
// 绝不能改写成 127.0.0.1 —— 那会把已允许远程的配置悄悄降级成只有本机能连，
// 而校验阶段又按「所有网卡」放行，两边判断不一致。
func (c *Config) NormalizedListenAddr() string {
	host, port, err := net.SplitHostPort(c.ListenAddr)
	if err != nil {
		return c.ListenAddr
	}
	if strings.EqualFold(host, "localhost") {
		return net.JoinHostPort("127.0.0.1", port)
	}
	return net.JoinHostPort(host, port)
}
