package agent

import (
	"errors"
	"io"
	"log/slog"
)

// errTest 是测试用的普通错误，含看似敏感的路径信息。
var errTest = errors.New("底层失败：读取 /Users/someone/secret/config 时出错")

// quietLogger 返回丢弃输出的 logger。
func quietLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelError}))
}
