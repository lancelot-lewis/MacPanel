package ipc

import (
	"io"
	"log/slog"
)

// quietLogger 返回丢弃输出的 logger，避免测试日志噪声。
func quietLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelError}))
}
