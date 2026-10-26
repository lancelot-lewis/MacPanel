package firewalld

import (
	"crypto/sha256"
	"encoding/hex"
)

// HashContent 返回内容的 SHA-256 十六进制摘要。
//
// 用途：确认「落盘/加载的确实是这份内容」。不用于安全比较
// （不需要常量时间，因为哈希本身不是凭据）。
func HashContent(content string) string {
	sum := sha256.Sum256([]byte(content))
	return hex.EncodeToString(sum[:])
}
