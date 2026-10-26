//go:build darwin

package ipc

import (
	"fmt"
	"net"

	"golang.org/x/sys/unix"
)

// Darwin 上 SOL_LOCAL / LOCAL_PEERCRED 的常量值。
// x/sys/unix 未导出这两个常量（仅在 GetsockoptXucred 注释里提到用法），
// 因此在此显式声明，与 <sys/un.h> 中的定义一致。
const (
	solLocal      = 0x0
	localPeerCred = 0x001
)

// peerCredFromConnDarwin 通过 LOCAL_PEERCRED 读取对端 xucred。
//
// 之所以不用「Socket 路径可达」作为信任依据：路径权限只说明谁「可能」
// 连上来，真正决定身份的是内核返回的凭据。macOS 的 xucred 只含 euid 与
// 附加组，不含 pid，因此 pid 字段保持 0，身份判定以 euid + 共享令牌为主。
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
		xu, err := unix.GetsockoptXucred(int(fd), solLocal, localPeerCred)
		if err != nil {
			credErr = fmt.Errorf("ipc: 读取 LOCAL_PEERCRED 失败: %w", err)
			return
		}
		if xu.Version != 0 {
			credErr = fmt.Errorf("ipc: xucred 版本不受支持: %d", xu.Version)
			return
		}
		cred.UID = xu.Uid
		// xucred 不携带 egid，仅携带附加组列表；GID 保持 0 不参与判定。
		cred.GID = 0
		cred.PID = 0
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
