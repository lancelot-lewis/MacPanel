package ipc

import (
	"fmt"
)

// maxSocketPathLen 是 Unix Domain Socket 路径的最大长度。
//
// sun_path 在 Darwin 上为 104 字节（含结尾 NUL），Linux 为 108 字节。
// 取更严格的 104 作为上界：超长路径会在 bind 时返回
// "invalid argument"，错误信息很不直观，因此在 Listen 前显式校验。
const maxSocketPathLen = 104

// validateSocketPath 校验 Socket 路径长度，并返回可读错误。
func validateSocketPath(path string) error {
	if len(path) >= maxSocketPathLen {
		return fmt.Errorf(
			"ipc: Socket 路径过长（%d 字节，上限 %d）：%s；"+
				"请改用更短的目录（例如 %s）",
			len(path), maxSocketPathLen-1, path, "/var/run/homelab-manager/agent.sock")
	}
	return nil
}
