package config

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// tokenBytes 是共享 IPC 令牌的原始长度（32 字节 = 256 bit）。
const tokenBytes = 32

// Token 是 Server 与 Agent 之间的共享密钥，用于在 UID 校验之外再确认调用方身份。
//
// 为什么 UID 校验之外还需要 token：
//   - UID 校验只能回答「谁在调用」，无法回答「是不是本应用的 Server」。
//     同一 UID 下的任何进程都能通过 UID 校验，因此再加一层不可猜测的共享密钥。
//   - 令牌文件放在 Server 不可读、只有 root 可读的目录（或 root 组可读），
//     使非特权进程无法窃取。
type Token struct {
	value []byte
}

// LoadToken 从路径读取令牌。文件必须存在、格式合法，否则返回错误（不自动生成）。
func LoadToken(path string) (*Token, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, fmt.Errorf("IPC 令牌文件不存在: %s（请通过安装器初始化，或运行 token 子命令生成）", path)
		}
		return nil, fmt.Errorf("读取 IPC 令牌失败: %w", err)
	}
	tok, err := ParseToken(string(raw))
	if err != nil {
		return nil, fmt.Errorf("IPC 令牌文件 %s 无效: %w", path, err)
	}
	return tok, nil
}

// ParseToken 解析十六进制令牌，要求长度精确匹配，避免弱令牌。
func ParseToken(s string) (*Token, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil, errors.New("令牌为空")
	}
	raw, err := hex.DecodeString(s)
	if err != nil {
		return nil, errors.New("令牌必须是十六进制字符串")
	}
	if len(raw) != tokenBytes {
		return nil, fmt.Errorf("令牌长度必须为 %d 字节（%d 个十六进制字符），实际 %d 字节", tokenBytes, tokenBytes*2, len(raw))
	}
	return &Token{value: raw}, nil
}

// GenerateToken 生成新的随机令牌。
func GenerateToken() (*Token, error) {
	buf := make([]byte, tokenBytes)
	if _, err := rand.Read(buf); err != nil {
		return nil, fmt.Errorf("生成随机令牌失败: %w", err)
	}
	return &Token{value: buf}, nil
}

// String 返回十六进制表示，仅供写入文件与调试。
func (t *Token) String() string {
	if t == nil {
		return ""
	}
	return hex.EncodeToString(t.value)
}

// Equals 常量时间比较，避免时序侧信道。
func (t *Token) Equals(other *Token) bool {
	if t == nil || other == nil {
		return false
	}
	if len(t.value) != len(other.value) {
		return false
	}
	return subtle.ConstantTimeCompare(t.value, other.value) == 1
}

// MatchesHex 常量时间比较客户端提交的十六进制令牌。
func (t *Token) MatchesHex(candidate string) bool {
	parsed, err := ParseToken(candidate)
	if err != nil {
		// 仍然做一次比较，保持耗时稳定，避免通过错误信息探测。
		_ = subtle.ConstantTimeCompare(t.value, t.value)
		return false
	}
	return t.Equals(parsed)
}

// WriteTokenFile 以 0600 权限原子写入令牌，用于安装器与 CLI 初始化。
func WriteTokenFile(path string, tok *Token) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return fmt.Errorf("创建令牌目录失败: %w", err)
	}
	tmp, err := os.CreateTemp(dir, ".ipc-token-*")
	if err != nil {
		return fmt.Errorf("创建临时令牌文件失败: %w", err)
	}
	tmpName := tmp.Name()
	defer func() {
		// 失败路径清理；成功 rename 后该路径已不存在。
		_ = os.Remove(tmpName)
	}()

	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return fmt.Errorf("设置令牌文件权限失败: %w", err)
	}
	if _, err := tmp.WriteString(tok.String() + "\n"); err != nil {
		tmp.Close()
		return fmt.Errorf("写入令牌失败: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return fmt.Errorf("同步令牌文件失败: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("关闭临时令牌文件失败: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("替换令牌文件失败: %w", err)
	}
	return nil
}
