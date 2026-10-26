package agent

import (
	"fmt"
	"os"
	"path/filepath"
)

// anchorFileMode 是 anchor 规则文件的权限。
//
// 0600：只有 root 可读写。规则文件是安全敏感资产——
// 能改它就能改防火墙行为，因此不允许多余的读取权限。
const anchorFileMode = 0o600

// writeAnchorFile 原子写入 anchor 规则文件。
//
// 为什么必须原子：pfctl 加载时会读取这个文件。
// 如果正在写入时被加载，会读到半截规则——可能是一份「只放行了前半部分」的配置，
// 而丢失的部分恰好是回环放行，结果就是本机失联。
//
// 流程：写临时文件（同目录）→ chmod → fsync → rename。
// rename 在同一文件系统内是原子的，因此 pfctl 要么看到旧文件、要么看到新文件。
func writeAnchorFile(path, content string) error {
	if path == "" {
		return fmt.Errorf("规则文件路径不能为空")
	}
	if !filepath.IsAbs(path) {
		return fmt.Errorf("规则文件路径必须是绝对路径，实际 %q", path)
	}

	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return fmt.Errorf("创建规则目录失败：%w", err)
	}

	tmp, err := os.CreateTemp(dir, ".anchor-*")
	if err != nil {
		return fmt.Errorf("创建临时规则文件失败：%w", err)
	}
	tmpName := tmp.Name()
	defer func() {
		// rename 成功后该路径已不存在，Remove 失败可忽略。
		_ = os.Remove(tmpName)
	}()

	if err := tmp.Chmod(anchorFileMode); err != nil {
		tmp.Close()
		return fmt.Errorf("设置规则文件权限失败：%w", err)
	}
	if _, err := tmp.WriteString(content); err != nil {
		tmp.Close()
		return fmt.Errorf("写入规则失败：%w", err)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return fmt.Errorf("同步规则文件失败：%w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("关闭临时规则文件失败：%w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("替换规则文件失败：%w", err)
	}
	return nil
}

// readAnchorFile 读取 anchor 规则文件，供诊断与展示。
// 文件不存在时返回空字符串（表示本应用尚未接管）。
func readAnchorFile(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return "", nil
		}
		return "", fmt.Errorf("读取规则文件失败：%w", err)
	}
	return string(data), nil
}
