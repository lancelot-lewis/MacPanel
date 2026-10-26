//go:build darwin

package darwin

import (
	"fmt"
	"syscall"

	"github.com/lancelot-lewis/MacPanel/internal/model"
)

// statFS 读取单个挂载点的容量。
//
// 说明：APFS 上多个卷共享同一容器，statfs 报告的 Blocks/Bavail 是容器的
// 数值，因此根卷与数据卷可能给出相同的总量。这属于系统真实语义，
// 前端应如实展示，不做「估算」掩盖。
func statFS(path string) (model.DiskUsage, error) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(path, &st); err != nil {
		return model.DiskUsage{}, fmt.Errorf("statfs %s 失败: %w", path, err)
	}
	bsize := int64(st.Bsize)
	total := int64(st.Blocks) * bsize
	free := int64(st.Bavail) * bsize
	used := total - free
	ratio := 0.0
	if total > 0 {
		ratio = float64(used) / float64(total)
	}

	fsName := cstr(st.Fstypename[:])
	mount := cstr(st.Mntonname[:])
	if mount == "" {
		mount = path
	}

	return model.DiskUsage{
		MountPoint: mount,
		FileSystem: fsName,
		TotalBytes: total,
		FreeBytes:  free,
		UsedBytes:  used,
		UsedRatio:  ratio,
	}, nil
}

// cstr 把固定长度字节数组转换为字符串（遇到 NUL 截断）。
// macOS 的 Statfs_t 中 Fstypename/Mntonname 是 [N]int8，需先转 []byte。
func cstr(b []int8) string {
	buf := make([]byte, 0, len(b))
	for _, c := range b {
		if c == 0 {
			break
		}
		buf = append(buf, byte(c))
	}
	return string(buf)
}
