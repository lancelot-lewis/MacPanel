// Package ipc 实现 Server 与 Root Agent 之间的 Unix Domain Socket 通信。
//
// 安全模型（详见 docs/security.md）：
//  1. Socket 文件由 root 创建，权限 0660，所在目录 0750，属主 root:admin 之外的账户不可达。
//  2. 每次连接都通过 getpeereid(2) 读取对端 euid/egid，与允许列表比对。
//  3. 每个请求额外携带共享令牌，使用常量时间比较，用于区分「同 UID 的其它进程」。
//  4. 请求带 issuedAt 时间戳与唯一 ID，超出 ±60 秒窗口或重复 ID 一律拒绝（防重放）。
//  5. 方法名必须在显式注册表中，参数在 handler 内部再做结构校验。
//
// 协议格式：4 字节大端长度前缀 + JSON 负载。请求与响应帧各自独立。
package ipc

import (
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/lancelot-lewis/MacPanel/internal/model"
)

// 帧长度上限。请求保持很小，避免内存放大攻击；响应允许较大（列表类数据）。
const (
	MaxRequestBytes  = 64 << 10 // 64 KiB
	MaxResponseBytes = 4 << 20  // 4 MiB
	// MaxConns 限制并发连接数，防止本地 DoS。
	MaxConns = 16
	// ReplayWindow 是请求时间戳允许的偏移范围。
	ReplayWindow = 60 * time.Second
)

// ProtocolVersion 随协议不兼容变更递增。
const ProtocolVersion = 1

// Request 是客户端发往 Agent 的请求。
type Request struct {
	ProtocolVersion int             `json:"protocolVersion"`
	ID              string          `json:"id"`
	Method          string          `json:"method"`
	Token           string          `json:"token"`
	IssuedAt        int64           `json:"issuedAt"` // Unix 秒
	Params          json.RawMessage `json:"params,omitempty"`
}

// Response 是 Agent 返回的响应。
type Response struct {
	ProtocolVersion int             `json:"protocolVersion"`
	ID              string          `json:"id"`
	OK              bool            `json:"ok"`
	Result          json.RawMessage `json:"result,omitempty"`
	Error           *model.Error    `json:"error,omitempty"`
}

// 协议级错误。均为可公开的技术性错误，不含路径或凭据细节。
var (
	ErrFrameTooLarge    = errors.New("ipc: 帧长度超出限制")
	ErrProtocolVersion  = errors.New("ipc: 协议版本不兼容")
	ErrInvalidRequest   = errors.New("ipc: 请求格式非法")
	ErrReplayDetected   = errors.New("ipc: 请求重放或时间戳无效")
	ErrUnknownMethod    = errors.New("ipc: 未知方法")
	ErrUnauthorized     = errors.New("ipc: 对端身份校验失败")
	ErrConnLimitReached = errors.New("ipc: 连接数已达上限")
)

// FrameTooLargeError 携带实际长度，便于日志定位。
type FrameTooLargeError struct {
	Limit int
	Got   int
}

func (e *FrameTooLargeError) Error() string {
	return fmt.Sprintf("ipc: 帧长度 %d 超过上限 %d", e.Got, e.Limit)
}

func (e *FrameTooLargeError) Is(target error) bool { return target == ErrFrameTooLarge }

// MethodResult 是 handler 的返回：数据 + 错误。
type MethodResult struct {
	Data any
	Err  error
}
