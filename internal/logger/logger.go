// Package logger 提供结构化日志。
//
// 约束：
//   - 不记录令牌、会话 cookie、密码等凭据。
//   - 输出 JSON 行日志，便于 launchd 收集与后续检索。
package logger

import (
	"context"
	"io"
	"log/slog"
	"os"
	"strings"
)

// Options 日志选项。
type Options struct {
	Level string // debug / info / warn / error
	// Dir 非空时同时写入文件 dir/<component>.log；为空则只写 stderr。
	Dir       string
	Component string // server / agent / cli
	JSON      bool
}

// New 创建 logger。返回的 cleanup 用于关闭文件句柄。
func New(opts Options) (*slog.Logger, func() error, error) {
	level := parseLevel(opts.Level)
	handlerOpts := &slog.HandlerOptions{Level: level}

	var writers []io.Writer
	writers = append(writers, os.Stderr)

	var closer io.Closer
	if opts.Dir != "" {
		if err := os.MkdirAll(opts.Dir, 0o750); err != nil {
			return nil, nil, err
		}
		name := opts.Component
		if name == "" {
			name = "homelab-manager"
		}
		f, err := os.OpenFile(
			strings.TrimRight(opts.Dir, "/")+"/"+name+".log",
			os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o640,
		)
		if err != nil {
			return nil, nil, err
		}
		writers = append(writers, f)
		closer = f
	}

	out := io.MultiWriter(writers...)
	var handler slog.Handler
	if opts.JSON {
		handler = slog.NewJSONHandler(out, handlerOpts)
	} else {
		handler = slog.NewTextHandler(out, handlerOpts)
	}

	component := opts.Component
	l := slog.New(handler).With("component", component)

	cleanup := func() error {
		if closer != nil {
			return closer.Close()
		}
		return nil
	}
	return l, cleanup, nil
}

func parseLevel(s string) slog.Level {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "debug":
		return slog.LevelDebug
	case "warn", "warning":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}

type ctxKey struct{}

// WithRequestID 把请求 ID 放入 context。
func WithRequestID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, ctxKey{}, id)
}

// RequestID 取出请求 ID，缺失返回空串。
func RequestID(ctx context.Context) string {
	if v, ok := ctx.Value(ctxKey{}).(string); ok {
		return v
	}
	return ""
}
