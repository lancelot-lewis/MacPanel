//go:build linux

package ipc

import (
	"fmt"
	"net"

	"golang.org/x/sys/unix"
)

// peerCredFromConnLinux 通过 SO_PEERCRED 读取对端凭据。
// Linux 上可同时拿到 pid，便于审计与排障。
func peerCredFromConn(conn net.Conn) (PeerCred, error) {
	uc, ok := conn.(*net.UnixConn)
	if !ok {
		return PeerCred{}, fmt.Errorf("ipc: 连接不是 Unix 连接")
	}
	raw, err := uc.SyscallConn()
	if err != nil {
		return PeerCred{}, fmt.Errorf("ipc: 获取底层连接失败: %w", err)
	}

	var cred PeerCred
	var credErr error
	if err := raw.Control(func(fd uintptr) {
		ucred, err := unix.GetsockoptUcred(int(fd), unix.SOL_SOCKET, unix.SO_PEERCRED)
		if err != nil {
			credErr = fmt.Errorf("ipc: 读取 SO_PEERCRED 失败: %w", err)
			return
		}
		cred.PID = int(ucred.Pid)
		cred.UID = ucred.Uid
		cred.GID = ucred.Gid
	}); err != nil {
		return PeerCred{}, fmt.Errorf("ipc: 访问 Socket 控制接口失败: %w", err)
	}
	if credErr != nil {
		return PeerCred{}, credErr
	}
	return cred, nil
}

// CurrentEUID 返回当前进程有效 UID。
func CurrentEUID() uint32 { return uint32(unix.Geteuid()) }
