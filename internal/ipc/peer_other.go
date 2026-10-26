//go:build !darwin && !linux

package ipc

import (
	"errors"
	"net"
)

// PeerCred 是对端进程凭据。
type PeerCred struct {
	PID int
	UID uint32
	GID uint32
}

// ErrPeerCredUnsupported 在不支持读取对端凭据的平台上返回。
// 这里刻意不做「读不到就放行」的降级：读不到凭据即视为不可信。
var ErrPeerCredUnsupported = errors.New("ipc: 当前平台不支持读取 Unix Socket 对端凭据")

// PeerCredFromConn 在不支持的平台上始终失败。
func PeerCredFromConn(net.Conn) (PeerCred, error) {
	return PeerCred{}, ErrPeerCredUnsupported
}

// UIDAllowed 在不支持的平台上始终返回 false。
func UIDAllowed(PeerCred, []uint32) bool { return false }

// CurrentEUID 在不支持的平台上返回 0。
func CurrentEUID() uint32 { return 0 }
