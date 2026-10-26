// Package orbstack 通过 Docker Socket 只读列出容器。
//
// 为什么单独建模而不是复用 execx：
// Docker Socket 是 Unix Socket 上的 HTTP API，不是命令行工具。
// 而且它的权限极高（等价于 root）：能创建特权容器、挂载任意目录。
//
// 安全约束（本包的全部能力就这些）：
//   - 只发 GET /containers/json，没有任何写操作；
//   - 不提供 /containers/{id}/inspect（那会返回环境变量，常含凭据）；
//   - 有超时与响应大小上限。
package orbstack

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/lancelot-lewis/MacPanel/internal/model"
)

// 候选 Socket 路径（按优先级）。
//
// /var/run/docker.sock 通常是 OrbStack 创建的符号链接，
// 因此优先用它——用户配置的 DOCKER_HOST 若指向别处会被它覆盖，
// 但那属于用户明确改变默认行为的场景。
var candidateSockets = []string{
	"/var/run/docker.sock",
}

// maxResponseBytes 限制响应体大小。
//
// 容器数量多时 /containers/json 的响应可能达到几 MB，
// 但超过 8 MiB 基本可以判定异常（或有人在伪造响应）。
const maxResponseBytes = 8 << 20

// Client 是 Docker API 的只读客户端。
type Client struct {
	socketPath string
	http       *http.Client
	// probeOnly 为 true 时只判断可用性，不列容器（用于状态查询）。
	probeOnly bool
}

// Options 是客户端构造参数。
type Options struct {
	// SocketPath 显式指定 Socket 路径；为空时按候选顺序探测。
	SocketPath string
	// Timeout 是单次请求超时，默认 5 秒。
	Timeout time.Duration
}

// New 创建客户端。
func New(opts Options) *Client {
	timeout := opts.Timeout
	if timeout <= 0 {
		timeout = 5 * time.Second
	}

	// 只创建一个实例：拨号闭包与返回的 Client 必须共享同一个
	// socketPath，否则 Status 报的路径与实际连接的路径会不一致
	// （早期实现创建了两个实例，导致 Status 总是回落到自动探测）。
	c := &Client{socketPath: opts.SocketPath}

	transport := &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			path := c.socketPath
			if path == "" {
				path = detectSocket()
			}
			if path == "" {
				return nil, fmt.Errorf("未找到 Docker Socket")
			}
			var d net.Dialer
			return d.DialContext(ctx, "unix", path)
		},
		// Docker Socket 不需要代理
		Proxy: nil,
		// 每次请求新建连接：Socket 上的 keep-alive 在守护进程重启后
		// 会留下失效连接，导致下一次请求莫名失败。
		DisableKeepAlives: true,
	}

	c.http = &http.Client{
		Transport: transport,
		Timeout:   timeout,
	}
	return c
}

// detectSocket 按候选顺序找可用的 Socket。
func detectSocket() string {
	for _, p := range candidateSockets {
		if info, err := os.Stat(p); err == nil && info.Mode()&os.ModeSocket != 0 {
			return p
		}
	}

	// OrbStack 的用户级路径（不需要 /var/run 的符号链接）
	if home, err := os.UserHomeDir(); err == nil {
		p := filepath.Join(home, ".orbstack", "run", "docker.sock")
		if info, err := os.Stat(p); err == nil && info.Mode()&os.ModeSocket != 0 {
			return p
		}
	}

	return ""
}

// Status 探测 OrbStack/Docker 是否可用。
//
// 不可用是正常状态（用户可能没装 OrbStack），因此返回状态结构
// 而不是 error：界面需要的是「为什么不可用」，不是一个异常。
func (c *Client) Status(ctx context.Context) model.OrbStackStatus {
	socket := c.socketPath
	if socket == "" {
		socket = detectSocket()
	}
	if socket == "" {
		return model.OrbStackStatus{
			Available: false,
			Reason:    "未找到 Docker Socket（OrbStack 未安装或未启动）",
		}
	}

	st := model.OrbStackStatus{SocketPath: socket}

	// 用 /version 做最小探测：不需要容器权限，也能验证 API 可达。
	body, err := c.get(ctx, "/version")
	if err != nil {
		st.Reason = fmt.Sprintf("连接 Docker Socket 失败：%v", err)
		return st
	}

	var v struct {
		Version    string `json:"Version"`
		APIVersion string `json:"ApiVersion"`
	}
	if err := json.Unmarshal(body, &v); err == nil {
		if v.APIVersion != "" {
			st.Version = "API " + v.APIVersion
		} else if v.Version != "" {
			st.Version = v.Version
		}
	}

	st.Available = true
	return st
}

// dockerContainer 是 Docker API 返回的容器结构（只取需要的字段）。
type dockerContainer struct {
	ID    string   `json:"Id"`
	Names []string `json:"Names"`
	Image string   `json:"Image"`
	State string   `json:"State"`
	// Status 是给人看的描述（如 "Up 3 hours"）。
	Status string `json:"Status"`
	Ports  []struct {
		IP          string `json:"IP"`
		PrivatePort int    `json:"PrivatePort"`
		PublicPort  int    `json:"PublicPort"`
		Type        string `json:"Type"`
	} `json:"Ports"`
}

// Containers 返回容器列表。
func (c *Client) Containers(ctx context.Context) ([]model.Container, model.OrbStackStatus, error) {
	st := c.Status(ctx)
	if !st.Available {
		return nil, st, nil
	}

	// all=1 包含已停止的容器：诊断时「容器存在但没起来」是关键信息。
	body, err := c.get(ctx, "/containers/json?all=1")
	if err != nil {
		return nil, st, fmt.Errorf("读取容器列表失败: %w", err)
	}

	var raw []dockerContainer
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, st, fmt.Errorf("解析容器列表失败: %w", err)
	}

	items := make([]model.Container, 0, len(raw))
	for _, c := range raw {
		items = append(items, model.Container{
			ID:     shortID(c.ID),
			Name:   primaryName(c.Names),
			Image:  c.Image,
			State:  c.State,
			Status: c.Status,
			Ports:  formatPorts(c),
		})
	}
	return items, st, nil
}

// get 发一个 GET 请求并返回响应体。
func (c *Client) get(ctx context.Context, path string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://docker"+path, nil)
	if err != nil {
		return nil, err
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		// 读一小段错误信息便于诊断，但不把整个体读进来。
		snippet, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return nil, fmt.Errorf("Docker API 返回 %d: %s", resp.StatusCode, strings.TrimSpace(string(snippet)))
	}

	// 用 LimitReader 防止异常响应打爆内存。
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))
	if err != nil {
		return nil, err
	}
	if len(body) >= maxResponseBytes {
		return nil, fmt.Errorf("响应超过 %d 字节上限", maxResponseBytes)
	}
	return body, nil
}

// shortID 截取容器 ID 前 12 位（Docker 的惯例显示长度）。
func shortID(id string) string {
	if len(id) <= 12 {
		return id
	}
	return id[:12]
}

// primaryName 取容器的主名称（去掉前导斜杠）。
func primaryName(names []string) string {
	if len(names) == 0 {
		return ""
	}
	return strings.TrimPrefix(names[0], "/")
}

// formatPorts 生成端口的可读描述。
//
// 只展示有意义的映射：未映射的内部端口也列出来（标注为内部），
// 因为「容器在跑但端口没映射」正是常见的困惑点。
func formatPorts(c dockerContainer) []string {
	out := make([]string, 0, len(c.Ports))
	for _, p := range c.Ports {
		if p.PublicPort > 0 {
			out = append(out, fmt.Sprintf("%s/%d → %s:%d", p.Type, p.PrivatePort, hostOf(p.IP), p.PublicPort))
			continue
		}
		out = append(out, fmt.Sprintf("%s/%d（未映射）", p.Type, p.PrivatePort))
	}
	return out
}

// hostOf 把 0.0.0.0 展示为 *（表示监听所有地址）。
func hostOf(ip string) string {
	if ip == "" || ip == "0.0.0.0" || ip == "::" {
		return "*"
	}
	return ip
}
