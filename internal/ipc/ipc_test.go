package ipc

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/lancelot-lewis/MacPanel/internal/config"
	"github.com/lancelot-lewis/MacPanel/internal/model"
)

// shortSocketPath 返回一个长度安全的临时 Socket 路径。
//
// 不能用 t.TempDir()：它会把测试名拼进路径，像
// /var/folders/.../TestStaleSocketRefusedWhenLive1962477740/001/agent.sock
// 就会超过 sun_path 的 104 字节上限，导致 bind: invalid argument。
// 这里用固定短前缀 + 随机后缀，既短又不冲突。
func shortSocketPath(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "hlm")
	if err != nil {
		t.Fatalf("创建临时目录失败: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	path := filepath.Join(dir, "a.sock")
	if len(path) >= maxSocketPathLen {
		t.Fatalf("测试 Socket 路径仍然过长（%d 字节）: %s", len(path), path)
	}
	return path
}

// newTestToken 生成测试用令牌。
func newTestToken(t *testing.T) *config.Token {
	t.Helper()
	tok, err := config.GenerateToken()
	if err != nil {
		t.Fatalf("生成令牌失败: %v", err)
	}
	return tok
}

// startTestServer 启动一个真实 Unix Socket 服务端，返回客户端与清理函数。
func startTestServer(t *testing.T, register func(*Server)) (*Client, *Server, string) {
	t.Helper()

	socketPath := shortSocketPath(t)
	tok := newTestToken(t)

	srv, err := NewServer(ServerOptions{
		SocketPath: socketPath,
		Token:      tok,
		Logger:     quietLogger(),
		Timeout:    3 * time.Second,
	})
	if err != nil {
		t.Fatalf("创建服务端失败: %v", err)
	}
	if register != nil {
		register(srv)
	}
	if err := srv.Listen(); err != nil {
		t.Fatalf("监听失败: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		_ = srv.Serve(ctx)
		close(done)
	}()

	t.Cleanup(func() {
		cancel()
		_ = srv.Close()
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			t.Log("服务端未在 2 秒内退出")
		}
	})

	client, err := NewClient(socketPath, tok, 3*time.Second)
	if err != nil {
		t.Fatalf("创建客户端失败: %v", err)
	}
	return client, srv, socketPath
}

// TestCallSuccess 验证正常调用链路。
func TestCallSuccess(t *testing.T) {
	type params struct {
		Name string `json:"name"`
	}
	type result struct {
		Greeting string `json:"greeting"`
	}

	client, _, _ := startTestServer(t, func(s *Server) {
		s.Register("test.echo", func(ctx context.Context, p json.RawMessage) (any, error) {
			var in params
			if err := json.Unmarshal(p, &in); err != nil {
				return nil, model.NewError(model.CodeInvalidArgument, "参数非法")
			}
			return result{Greeting: "你好 " + in.Name}, nil
		})
	})

	var out result
	if err := client.Call(context.Background(), "test.echo", params{Name: "世界"}, &out); err != nil {
		t.Fatalf("调用失败: %v", err)
	}
	if out.Greeting != "你好 世界" {
		t.Fatalf("结果不符: %q", out.Greeting)
	}
}

// TestUnknownMethodRejected 验证未注册方法被拒绝。
func TestUnknownMethodRejected(t *testing.T) {
	client, _, _ := startTestServer(t, func(s *Server) {
		s.Register("test.ok", func(context.Context, json.RawMessage) (any, error) {
			return "ok", nil
		})
	})

	err := client.Call(context.Background(), "system.exec", nil, nil)
	if err == nil {
		t.Fatal("期望拒绝未知方法，实际成功")
	}
	if code := model.CodeOf(err); code != model.CodeNotFound {
		t.Fatalf("期望 not_found，实际 %s（%v）", code, err)
	}
}

// TestWrongTokenRejected 验证错误令牌被拒绝。
func TestWrongTokenRejected(t *testing.T) {
	_, _, socketPath := startTestServer(t, func(s *Server) {
		s.Register("test.ok", func(context.Context, json.RawMessage) (any, error) {
			return "ok", nil
		})
	})

	wrongTok := newTestToken(t)
	badClient, err := NewClient(socketPath, wrongTok, 3*time.Second)
	if err != nil {
		t.Fatalf("创建客户端失败: %v", err)
	}

	callErr := badClient.Call(context.Background(), "test.ok", nil, nil)
	if callErr == nil {
		t.Fatal("期望拒绝错误令牌，实际成功")
	}
	if code := model.CodeOf(callErr); code != model.CodeUnauthorized {
		t.Fatalf("期望 unauthorized，实际 %s（%v）", code, callErr)
	}
}

// TestHandlerErrorPropagated 验证 handler 的业务错误码透传。
func TestHandlerErrorPropagated(t *testing.T) {
	client, _, _ := startTestServer(t, func(s *Server) {
		s.Register("test.unavailable", func(context.Context, json.RawMessage) (any, error) {
			return nil, model.Unavailable("PF 状态不可读")
		})
		s.Register("test.internal", func(context.Context, json.RawMessage) (any, error) {
			return nil, errors.New("数据库连接池耗尽，路径 /secret/path")
		})
	})

	err := client.Call(context.Background(), "test.unavailable", nil, nil)
	if code := model.CodeOf(err); code != model.CodeUnavailable {
		t.Fatalf("期望 unavailable，实际 %s", code)
	}

	// 内部错误的细节不得泄漏给调用方。
	err = client.Call(context.Background(), "test.internal", nil, nil)
	if code := model.CodeOf(err); code != model.CodeInternal {
		t.Fatalf("期望 internal，实际 %s", code)
	}
	if msg := model.AsError(err).Message; msg == "" || bytes.Contains([]byte(msg), []byte("/secret/path")) {
		t.Fatalf("内部错误信息泄漏了细节: %q", msg)
	}
}

// TestFrameTooLargeRejected 验证超长帧被拒绝，避免内存放大。
func TestFrameTooLargeRejected(t *testing.T) {
	_, _, socketPath := startTestServer(t, func(s *Server) {
		s.Register("test.ok", func(context.Context, json.RawMessage) (any, error) {
			return "ok", nil
		})
	})

	conn, err := net.Dial("unix", socketPath)
	if err != nil {
		t.Fatalf("连接失败: %v", err)
	}
	defer conn.Close()

	// 声明一个超过上限的长度，但只发少量数据：服务端应立即拒绝而不预分配。
	header := []byte{0x00, 0xFF, 0xFF, 0xFF}
	if _, err := conn.Write(header); err != nil {
		t.Fatalf("写入帧头失败: %v", err)
	}
	_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))

	buf := make([]byte, 4096)
	n, err := conn.Read(buf)
	if err != nil {
		// 服务端直接关闭连接也算正确拒绝。
		return
	}
	var resp Response
	if err := json.Unmarshal(buf[:n], &resp); err == nil && resp.OK {
		t.Fatal("超长帧不应被接受")
	}
}

// TestReplayRejected 验证重复请求 ID 被拒绝。
func TestReplayRejected(t *testing.T) {
	socketPath := shortSocketPath(t)
	tok := newTestToken(t)

	srv, err := NewServer(ServerOptions{
		SocketPath: socketPath,
		Token:      tok,
		Logger:     quietLogger(),
		Timeout:    3 * time.Second,
	})
	if err != nil {
		t.Fatalf("创建服务端失败: %v", err)
	}
	srv.Register("test.ok", func(context.Context, json.RawMessage) (any, error) {
		return "ok", nil
	})
	if err := srv.Listen(); err != nil {
		t.Fatalf("监听失败: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	go func() { _ = srv.Serve(ctx) }()
	t.Cleanup(func() {
		cancel()
		_ = srv.Close()
	})

	send := func(id string, issuedAt int64) Response {
		conn, err := net.Dial("unix", socketPath)
		if err != nil {
			t.Fatalf("连接失败: %v", err)
		}
		defer conn.Close()

		payload, err := json.Marshal(Request{
			ProtocolVersion: ProtocolVersion,
			ID:              id,
			Method:          "test.ok",
			Token:           tok.String(),
			IssuedAt:        issuedAt,
		})
		if err != nil {
			t.Fatalf("序列化失败: %v", err)
		}
		if err := writeFrame(conn, payload, MaxRequestBytes); err != nil {
			t.Fatalf("发送失败: %v", err)
		}
		_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
		body, err := readFrame(conn, MaxResponseBytes)
		if err != nil {
			t.Fatalf("读取响应失败: %v", err)
		}
		var resp Response
		if err := json.Unmarshal(body, &resp); err != nil {
			t.Fatalf("解析响应失败: %v", err)
		}
		return resp
	}

	// 先确认正常请求可通过。
	first := send("fixed-id-123", time.Now().Unix())
	if !first.OK {
		t.Fatalf("首次请求应成功: %+v", first.Error)
	}

	// 同一 ID 再来一次必须被拒绝。
	second := send("fixed-id-123", time.Now().Unix())
	if second.OK {
		t.Fatal("重复请求 ID 应被拒绝")
	}
	if second.Error == nil {
		t.Fatal("重复请求应返回错误")
	}
}

// TestStaleTimestampRejected 验证时间戳超窗被拒绝。
func TestStaleTimestampRejected(t *testing.T) {
	socketPath := shortSocketPath(t)
	tok := newTestToken(t)

	srv, err := NewServer(ServerOptions{
		SocketPath: socketPath,
		Token:      tok,
		Logger:     quietLogger(),
		Timeout:    3 * time.Second,
	})
	if err != nil {
		t.Fatalf("创建服务端失败: %v", err)
	}
	srv.Register("test.ok", func(context.Context, json.RawMessage) (any, error) {
		return "ok", nil
	})
	if err := srv.Listen(); err != nil {
		t.Fatalf("监听失败: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	go func() { _ = srv.Serve(ctx) }()
	t.Cleanup(func() {
		cancel()
		_ = srv.Close()
	})

	conn, err := net.Dial("unix", socketPath)
	if err != nil {
		t.Fatalf("连接失败: %v", err)
	}
	defer conn.Close()

	payload, _ := json.Marshal(Request{
		ProtocolVersion: ProtocolVersion,
		ID:              "stale-id",
		Method:          "test.ok",
		Token:           tok.String(),
		IssuedAt:        time.Now().Add(-10 * time.Minute).Unix(),
	})
	if err := writeFrame(conn, payload, MaxRequestBytes); err != nil {
		t.Fatalf("发送失败: %v", err)
	}
	_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	body, err := readFrame(conn, MaxResponseBytes)
	if err != nil {
		t.Fatalf("读取响应失败: %v", err)
	}
	var resp Response
	if err := json.Unmarshal(body, &resp); err != nil {
		t.Fatalf("解析响应失败: %v", err)
	}
	if resp.OK {
		t.Fatal("过期时间戳应被拒绝")
	}
}

// TestSocketPermissions 验证 Socket 文件权限为 0660、目录 0750。
func TestSocketPermissions(t *testing.T) {
	_, _, socketPath := startTestServer(t, func(s *Server) {
		s.Register("test.ok", func(context.Context, json.RawMessage) (any, error) {
			return "ok", nil
		})
	})

	info, err := os.Lstat(socketPath)
	if err != nil {
		t.Fatalf("读取 Socket 信息失败: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o660 {
		t.Fatalf("Socket 权限应为 0660，实际 %o", perm)
	}

	dirInfo, err := os.Stat(filepath.Dir(socketPath))
	if err != nil {
		t.Fatalf("读取目录信息失败: %v", err)
	}
	if perm := dirInfo.Mode().Perm(); perm != 0o750 {
		t.Fatalf("Socket 目录权限应为 0750，实际 %o", perm)
	}
}

// TestStaleSocketRefusedWhenLive 验证已有实例在监听时拒绝重复启动。
func TestStaleSocketRefusedWhenLive(t *testing.T) {
	_, srv, socketPath := startTestServer(t, func(s *Server) {
		s.Register("test.ok", func(context.Context, json.RawMessage) (any, error) {
			return "ok", nil
		})
	})
	_ = srv

	tok := newTestToken(t)
	second, err := NewServer(ServerOptions{
		SocketPath: socketPath,
		Token:      tok,
		Logger:     quietLogger(),
		Timeout:    time.Second,
	})
	if err != nil {
		t.Fatalf("创建第二个服务端失败: %v", err)
	}
	if err := second.Listen(); err == nil {
		second.Close()
		t.Fatal("已有实例监听时，第二个实例应拒绝启动")
	}
}

// TestNonSocketPathNotRemoved 验证非 Socket 文件不会被删除。
func TestNonSocketPathNotRemoved(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "not-a-socket")
	if err := os.WriteFile(path, []byte("重要数据"), 0o600); err != nil {
		t.Fatalf("准备文件失败: %v", err)
	}

	srv, err := NewServer(ServerOptions{
		SocketPath: path,
		Token:      newTestToken(t),
		Logger:     quietLogger(),
		Timeout:    time.Second,
	})
	if err != nil {
		t.Fatalf("创建服务端失败: %v", err)
	}
	if err := srv.Listen(); err == nil {
		srv.Close()
		t.Fatal("路径已存在普通文件时应拒绝启动")
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("原文件被删除: %v", err)
	}
	if string(data) != "重要数据" {
		t.Fatalf("原文件内容被改动: %q", data)
	}
}

// TestDecodeStrictRejectsUnknownFields 验证多余字段被拒绝。
func TestDecodeStrictRejectsUnknownFields(t *testing.T) {
	var req Request
	err := decodeStrict([]byte(`{"protocolVersion":1,"id":"x","method":"m","token":"t","issuedAt":0,"extra":"bad"}`), &req)
	if err == nil {
		t.Fatal("期望拒绝未知字段")
	}
	if !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("期望 ErrInvalidRequest，实际 %v", err)
	}
}

// TestSocketPathTooLongRejected 验证超长 Socket 路径被提前拒绝。
// 若留给 bind 报错，用户只会看到 "invalid argument"，很难定位。
func TestSocketPathTooLongRejected(t *testing.T) {
	longPath := filepath.Join(os.TempDir(), strings.Repeat("x", 200), "agent.sock")

	srv, err := NewServer(ServerOptions{
		SocketPath: longPath,
		Token:      newTestToken(t),
		Logger:     quietLogger(),
		Timeout:    time.Second,
	})
	if err != nil {
		t.Fatalf("创建服务端失败: %v", err)
	}

	listenErr := srv.Listen()
	if listenErr == nil {
		srv.Close()
		t.Fatal("超长路径应被拒绝")
	}
	if !strings.Contains(listenErr.Error(), "过长") {
		t.Errorf("错误信息应说明路径过长: %v", listenErr)
	}
}

// TestValidateSocketPathBounds 验证边界值。
func TestValidateSocketPathBounds(t *testing.T) {
	// 恰好上限 - 1 应通过
	okPath := "/" + strings.Repeat("a", maxSocketPathLen-2)
	if err := validateSocketPath(okPath); err != nil {
		t.Errorf("长度 %d 应通过，实际: %v", len(okPath), err)
	}
	// 等于上限应失败
	badPath := "/" + strings.Repeat("a", maxSocketPathLen-1)
	if err := validateSocketPath(badPath); err == nil {
		t.Errorf("长度 %d 应被拒绝", len(badPath))
	}
}
