package ipc

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"time"

	"github.com/lancelot-lewis/MacPanel/internal/config"
	"github.com/lancelot-lewis/MacPanel/internal/model"
)

// Client 是 Server / CLI 侧的 IPC 客户端。
// 每次 Call 建立独立连接，避免长连接在 Agent 重启后失效导致误判。
type Client struct {
	socketPath string
	token      *config.Token
	timeout    time.Duration
}

// NewClient 创建客户端。
func NewClient(socketPath string, token *config.Token, timeout time.Duration) (*Client, error) {
	if socketPath == "" {
		return nil, errors.New("ipc: SocketPath 不能为空")
	}
	if token == nil {
		return nil, errors.New("ipc: 必须提供共享令牌")
	}
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	return &Client{socketPath: socketPath, token: token, timeout: timeout}, nil
}

// NewClientFromFiles 从配置文件加载令牌并创建客户端。
func NewClientFromFiles(cfg config.Config) (*Client, error) {
	tok, err := config.LoadToken(cfg.TokenPath)
	if err != nil {
		return nil, err
	}
	return NewClient(cfg.SocketPath, tok, cfg.IPCTimeout)
}

// Call 调用 Agent 方法，把结果反序列化到 out。
// out 可为 nil（表示忽略返回值）。
func (c *Client) Call(ctx context.Context, method string, params any, out any) error {
	if method == "" {
		return model.NewError(model.CodeInvalidArgument, "方法名不能为空")
	}

	reqID, err := newRequestID()
	if err != nil {
		return err
	}

	var rawParams json.RawMessage
	if params != nil {
		b, err := json.Marshal(params)
		if err != nil {
			return model.NewError(model.CodeInvalidArgument, "参数序列化失败: %v", err)
		}
		rawParams = b
	}

	payload, err := json.Marshal(Request{
		ProtocolVersion: ProtocolVersion,
		ID:              reqID,
		Method:          method,
		Token:           c.token.String(),
		IssuedAt:        time.Now().Unix(),
		Params:          rawParams,
	})
	if err != nil {
		return fmt.Errorf("ipc: 请求序列化失败: %w", err)
	}

	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()

	var dialer net.Dialer
	conn, err := dialer.DialContext(ctx, "unix", c.socketPath)
	if err != nil {
		// 连接失败通常意味着 Agent 未运行；给出明确可诊断的错误码。
		return model.Unavailable("无法连接 Agent：%s", friendlyDialError(err, c.socketPath))
	}
	defer conn.Close()

	if deadline, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(deadline)
	}

	if err := writeFrame(conn, payload, MaxRequestBytes); err != nil {
		return model.Unavailable("请求发送失败：%v", err)
	}

	body, err := readFrame(conn, MaxResponseBytes)
	if err != nil {
		return model.Unavailable("读取 Agent 响应失败：%v", err)
	}

	var resp Response
	if err := json.Unmarshal(body, &resp); err != nil {
		return model.Unavailable("Agent 响应格式非法：%v", err)
	}
	if resp.ID != reqID {
		// 响应 ID 不匹配说明连接被串用了，属于严重异常。
		return model.Unavailable("Agent 响应与请求不匹配")
	}
	if !resp.OK {
		if resp.Error != nil {
			return resp.Error
		}
		return model.NewError(model.CodeInternal, "Agent 返回未知错误")
	}
	if out != nil && len(resp.Result) > 0 {
		if err := json.Unmarshal(resp.Result, out); err != nil {
			return model.Unavailable("Agent 结果解析失败：%v", err)
		}
	}
	return nil
}

// Ping 探测 Agent 是否可用。
func (c *Client) Ping(ctx context.Context) error {
	return c.Call(ctx, "agent.health", nil, nil)
}

func friendlyDialError(err error, path string) string {
	if errors.Is(err, os.ErrNotExist) {
		return fmt.Sprintf("Socket 不存在（%s），Agent 可能未启动", path)
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "连接超时"
	}
	return err.Error()
}

// newRequestID 生成 128 bit 随机 ID，避免碰撞导致误判重放。
func newRequestID() (string, error) {
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("ipc: 生成请求 ID 失败: %w", err)
	}
	return hex.EncodeToString(buf), nil
}
