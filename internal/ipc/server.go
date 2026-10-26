package ipc

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	"github.com/lancelot-lewis/MacPanel/internal/config"
	"github.com/lancelot-lewis/MacPanel/internal/model"
)

// Handler 是固定签名的方法实现。参数已在分发层做长度与格式校验，
// 但 handler 内部仍必须自行校验业务参数（双重保险）。
type Handler func(ctx context.Context, params json.RawMessage) (any, error)

// Server 是 Agent 侧的 IPC 服务端。
type Server struct {
	socketPath string
	token      *config.Token
	allowedUID []uint32
	log        *slog.Logger
	timeout    time.Duration

	mu       sync.RWMutex
	handlers map[string]Handler

	// 防重放：记录最近见过的请求 ID 与到期时间。
	seenMu sync.Mutex
	seen   map[string]time.Time

	activeConns atomic.Int32
	listener    net.Listener

	startedAt time.Time
}

// ServerOptions 构造参数。
type ServerOptions struct {
	SocketPath      string
	Token           *config.Token
	AllowedPeerUIDs []uint32
	Logger          *slog.Logger
	Timeout         time.Duration
}

// NewServer 创建 IPC 服务端。不会打开 Socket。
func NewServer(opts ServerOptions) (*Server, error) {
	if opts.SocketPath == "" {
		return nil, errors.New("ipc: SocketPath 不能为空")
	}
	if opts.Token == nil {
		return nil, errors.New("ipc: 必须提供共享令牌")
	}
	if opts.Logger == nil {
		opts.Logger = slog.Default()
	}
	if opts.Timeout <= 0 {
		opts.Timeout = 10 * time.Second
	}
	return &Server{
		socketPath: opts.SocketPath,
		token:      opts.Token,
		allowedUID: opts.AllowedPeerUIDs,
		log:        opts.Logger,
		timeout:    opts.Timeout,
		handlers:   make(map[string]Handler),
		seen:       make(map[string]time.Time),
		startedAt:  time.Now(),
	}, nil
}

// Register 注册一个方法。重复注册直接 panic，避免运行时静默覆盖。
func (s *Server) Register(method string, h Handler) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.handlers[method]; exists {
		panic("ipc: 方法重复注册: " + method)
	}
	s.handlers[method] = h
}

// Methods 返回已注册方法名快照。
func (s *Server) Methods() []string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]string, 0, len(s.handlers))
	for k := range s.handlers {
		out = append(out, k)
	}
	return out
}

// StartedAt 返回服务启动时间。
func (s *Server) StartedAt() time.Time { return s.startedAt }

// Listen 创建并监听 Socket。
//
// 关键点：
//   - 先创建父目录并收紧权限到 0750；
//   - 若存在残留 Socket 文件（上次异常退出），只有在确认它已无人监听时才删除；
//   - Socket 文件权限设为 0660，组可读以便 Server 组访问，其他账户不可达。
func (s *Server) Listen() error {
	if err := validateSocketPath(s.socketPath); err != nil {
		return err
	}

	dir := filepath.Dir(s.socketPath)
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return fmt.Errorf("ipc: 创建 Socket 目录失败: %w", err)
	}
	// 目录权限可能是历史遗留，显式收紧。
	if err := os.Chmod(dir, 0o750); err != nil {
		return fmt.Errorf("ipc: 收紧 Socket 目录权限失败: %w", err)
	}

	if err := removeStaleSocket(s.socketPath); err != nil {
		return err
	}

	ln, err := net.Listen("unix", s.socketPath)
	if err != nil {
		return fmt.Errorf("ipc: 监听 Socket 失败: %w", err)
	}
	if err := os.Chmod(s.socketPath, 0o660); err != nil {
		ln.Close()
		return fmt.Errorf("ipc: 设置 Socket 权限失败: %w", err)
	}
	s.listener = ln
	return nil
}

// removeStaleSocket 只在确认无监听者时删除残留 Socket。
// 直接删除可能踩到正在运行的实例，因此先尝试连接。
func removeStaleSocket(path string) error {
	info, err := os.Lstat(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return fmt.Errorf("ipc: 检查 Socket 文件失败: %w", err)
	}
	if info.Mode()&os.ModeSocket == 0 {
		// 非 Socket 文件绝不删除，避免误删配置或数据。
		return fmt.Errorf("ipc: 路径 %s 已存在且不是 Socket 文件，拒绝覆盖", path)
	}

	conn, dialErr := net.DialTimeout("unix", path, 500*time.Millisecond)
	if dialErr == nil {
		conn.Close()
		return fmt.Errorf("ipc: %s 已有实例在监听，拒绝重复启动", path)
	}
	if err := os.Remove(path); err != nil {
		return fmt.Errorf("ipc: 清理残留 Socket 失败: %w", err)
	}
	return nil
}

// Serve 接受连接直到 ctx 取消或 listener 关闭。
func (s *Server) Serve(ctx context.Context) error {
	if s.listener == nil {
		return errors.New("ipc: 尚未调用 Listen")
	}
	var wg sync.WaitGroup
	defer wg.Wait()

	go func() {
		<-ctx.Done()
		_ = s.listener.Close()
	}()

	// 定期清理过期的防重放记录。
	go s.gcSeenLoop(ctx)

	for {
		conn, err := s.listener.Accept()
		if err != nil {
			select {
			case <-ctx.Done():
				return nil
			default:
			}
			if errors.Is(err, net.ErrClosed) {
				return nil
			}
			// 单次 Accept 失败不应导致服务退出，但需要限速避免日志风暴。
			s.log.Warn("接受连接失败", "error", err)
			time.Sleep(50 * time.Millisecond)
			continue
		}

		if s.activeConns.Load() >= MaxConns {
			s.log.Warn("连接数超限，拒绝新连接")
			conn.Close()
			continue
		}

		s.activeConns.Add(1)
		wg.Add(1)
		go func(c net.Conn) {
			defer wg.Done()
			defer s.activeConns.Add(-1)
			s.handleConn(ctx, c)
		}(conn)
	}
}

// Close 停止监听并清理 Socket 文件。
func (s *Server) Close() error {
	if s.listener != nil {
		_ = s.listener.Close()
	}
	if err := os.Remove(s.socketPath); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

func (s *Server) gcSeenLoop(ctx context.Context) {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-ticker.C:
			s.seenMu.Lock()
			for id, exp := range s.seen {
				if now.After(exp) {
					delete(s.seen, id)
				}
			}
			s.seenMu.Unlock()
		}
	}
}

// handleConn 处理单条连接上的多次请求（客户端可复用连接）。
func (s *Server) handleConn(ctx context.Context, conn net.Conn) {
	defer conn.Close()

	// 当前实现按单请求-单响应处理，但保留循环以便客户端复用连接。
	// 为避免长连接占用，一次连接最多处理 8 个请求。
	for i := 0; i < 8; i++ {
		_ = conn.SetDeadline(time.Now().Add(s.timeout))

		payload, err := readFrame(conn, MaxRequestBytes)
		if err != nil {
			// 客户端正常关闭（读不到帧头）不是错误，不打日志。
			if isClosedErr(err) {
				return
			}
			s.log.Warn("读取请求帧失败", "error", err)
			s.writeError(conn, "", model.NewError(model.CodeInvalidArgument, "请求帧非法"))
			return
		}

		resp := s.dispatch(ctx, conn, payload)
		body, err := json.Marshal(resp)
		if err != nil {
			s.log.Error("序列化响应失败", "error", err)
			return
		}
		if err := writeFrame(conn, body, MaxResponseBytes); err != nil {
			s.log.Warn("写入响应帧失败", "error", err)
			return
		}
	}
}

// dispatch 完成身份校验 → 请求解析 → 方法查找 → 执行。
func (s *Server) dispatch(ctx context.Context, conn net.Conn, payload []byte) Response {
	cred, err := PeerCredFromConn(conn)
	if err != nil {
		s.log.Warn("读取对端凭据失败，拒绝请求", "error", err)
		return errorResponse("", model.NewError(model.CodeForbidden, "无法确认调用方身份"))
	}
	if !UIDAllowed(cred, s.allowedUID) {
		s.log.Warn("对端 UID 不在允许列表", "peerUID", cred.UID)
		return errorResponse("", model.NewError(model.CodeForbidden, "调用方身份不受信任"))
	}

	var req Request
	if err := decodeStrict(payload, &req); err != nil {
		return errorResponse("", model.NewError(model.CodeInvalidArgument, "请求格式非法"))
	}

	if req.ProtocolVersion != ProtocolVersion {
		return errorResponse(req.ID, model.NewError(model.CodeInvalidArgument,
			"协议版本不兼容：服务端 %d，客户端 %d", ProtocolVersion, req.ProtocolVersion))
	}
	if req.ID == "" || len(req.ID) > 64 {
		return errorResponse("", model.NewError(model.CodeInvalidArgument, "请求 ID 非法"))
	}

	// 令牌校验（常量时间比较）。
	if !s.token.MatchesHex(req.Token) {
		s.log.Warn("共享令牌校验失败", "peerUID", cred.UID, "method", req.Method)
		return errorResponse(req.ID, model.NewError(model.CodeUnauthorized, "调用方凭据无效"))
	}

	// 防重放：时间窗 + 唯一 ID。
	if err := s.checkReplay(req); err != nil {
		s.log.Warn("拒绝重放请求", "error", err, "method", req.Method)
		return errorResponse(req.ID, model.NewError(model.CodeForbidden, "请求时间戳或 ID 无效"))
	}

	s.mu.RLock()
	handler, ok := s.handlers[req.Method]
	s.mu.RUnlock()
	if !ok {
		// 不区分「方法不存在」与「方法无权访问」，避免探测可用接口。
		return errorResponse(req.ID, model.NewError(model.CodeNotFound, "方法不存在"))
	}

	data, herr := handler(ctx, req.Params)
	if herr != nil {
		e := model.AsError(herr)
		if e.Code == model.CodeInternal {
			// 内部错误只记录日志，响应中不泄漏细节。
			s.log.Error("方法执行失败", "method", req.Method, "error", herr)
			return errorResponse(req.ID, model.NewError(model.CodeInternal, "内部错误"))
		}
		return errorResponse(req.ID, e)
	}

	result, err := json.Marshal(data)
	if err != nil {
		s.log.Error("序列化结果失败", "method", req.Method, "error", err)
		return errorResponse(req.ID, model.NewError(model.CodeInternal, "内部错误"))
	}
	return Response{
		ProtocolVersion: ProtocolVersion,
		ID:              req.ID,
		OK:              true,
		Result:          result,
	}
}

func (s *Server) checkReplay(req Request) error {
	now := time.Now()
	issued := time.Unix(req.IssuedAt, 0)
	if d := now.Sub(issued); d > ReplayWindow || d < -ReplayWindow {
		return fmt.Errorf("时间戳偏移 %s 超出窗口", d.Round(time.Second))
	}

	s.seenMu.Lock()
	defer s.seenMu.Unlock()
	if _, dup := s.seen[req.ID]; dup {
		return errors.New("请求 ID 重复")
	}
	s.seen[req.ID] = now.Add(2 * ReplayWindow)
	return nil
}

func (s *Server) writeError(conn net.Conn, id string, e *model.Error) {
	resp := errorResponse(id, e)
	body, err := json.Marshal(resp)
	if err != nil {
		return
	}
	_ = writeFrame(conn, body, MaxResponseBytes)
}

func errorResponse(id string, e *model.Error) Response {
	return Response{ProtocolVersion: ProtocolVersion, ID: id, OK: false, Error: e}
}

// isClosedErr 判断错误是否为对端正常关闭连接，用于避免把正常断开记为告警。
func isClosedErr(err error) bool {
	return errors.Is(err, io.EOF) || errors.Is(err, net.ErrClosed) || errors.Is(err, os.ErrClosed)
}
