//go:build darwin || linux

package ipc

import "net"

// PeerCred 是对端进程凭据。
type PeerCred struct {
	PID int
	UID uint32
	GID uint32
}

// PeerCredFromConn 读取对端凭据，具体实现由平台文件提供。
func PeerCredFromConn(conn net.Conn) (PeerCred, error) {
	return peerCredFromConn(conn)
}

// UIDAllowed 判断对端 UID 是否在白名单内。
//
// 白名单为空时，只允许与当前进程 euid 相同的对端（即本应用的 Server），
// 这样安装后无需额外配置即可工作；若要允许其它账户，必须显式列出。
func UIDAllowed(cred PeerCred, allowed []uint32) bool {
	self := CurrentEUID()
	if cred.UID == self {
		return true
	}
	for _, uid := range allowed {
		if cred.UID == uid {
			return true
		}
	}
	return false
}
