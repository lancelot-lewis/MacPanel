package server

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/lancelot-lewis/MacPanel/internal/agent"
	"github.com/lancelot-lewis/MacPanel/internal/app"
	"github.com/lancelot-lewis/MacPanel/internal/audit"
	"github.com/lancelot-lewis/MacPanel/internal/auth"
	"github.com/lancelot-lewis/MacPanel/internal/config"
	"github.com/lancelot-lewis/MacPanel/internal/database"
	"github.com/lancelot-lewis/MacPanel/internal/model"
)

// stubCaller 是可编程的 IPC 桩。
type stubCaller struct {
	overview model.SystemOverview
	network  model.NetworkSnapshot
	firewall model.FirewallStatus
	health   model.AgentHealth
	err      error
	calls    []string

	// M2：防火墙读写
	fwErr       error
	fwMethod    string
	fwParams    any
	fwCallCount int
}

// handleFirewallStub 处理 M2 方法，返回固定 JSON。
func (s *stubCaller) handleFirewallStub(method string, params any, out any) error {
	s.fwMethod = method
	s.fwParams = params
	s.fwCallCount++
	if s.fwErr != nil {
		return s.fwErr
	}
	if raw, ok := out.(*json.RawMessage); ok {
		*raw = json.RawMessage(`{"stub":true}`)
		return nil
	}
	return nil
}

func (s *stubCaller) Call(_ context.Context, method string, params any, out any) error {
	s.calls = append(s.calls, method)
	if s.err != nil {
		return s.err
	}
	switch method {
	case agent.MethodSystemOverview:
		*(out.(*model.SystemOverview)) = s.overview
	case agent.MethodSystemNetwork:
		*(out.(*model.NetworkSnapshot)) = s.network
	case agent.MethodFirewallStatus:
		*(out.(*model.FirewallStatus)) = s.firewall
	case agent.MethodFirewallRulesGet,
		agent.MethodFirewallRulesSet,
		agent.MethodFirewallPreview,
		agent.MethodFirewallApply,
		agent.MethodFirewallConfirm,
		agent.MethodFirewallRollback,
		agent.MethodFirewallTransaction,
		agent.MethodFirewallTemplates:
		return s.handleFirewallStub(method, params, out)
	case agent.MethodHealth:
		*(out.(*model.AgentHealthResponse)) = model.AgentHealthResponse{
			AgentHealth: s.health,
			Methods:     []string{"a"},
		}
	}
	return nil
}

// testEnv 是一套完整的测试环境。
type testEnv struct {
	handler http.Handler
	caller  *stubCaller
	db      *database.DB
	auth    *auth.Manager
	cfg     config.Config
}

// newTestEnv 构建测试环境。allowRemote=false 为本地模式。
func newTestEnv(t *testing.T, allowRemote bool) *testEnv {
	t.Helper()

	dbPath := filepath.Join(t.TempDir(), "api.db")
	db, err := database.Open(context.Background(), dbPath)
	if err != nil {
		t.Fatalf("打开数据库失败: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	cfg := config.Defaults()
	cfg.Mode = config.ModeServer
	cfg.ListenAddr = "127.0.0.1:7788"
	cfg.AllowRemote = allowRemote

	caller := &stubCaller{
		overview: model.SystemOverview{
			Hostname:    "test-host",
			OS:          model.OSInfo{ProductVersion: "15.0", Build: "24A1", Architecture: "arm64"},
			CPU:         model.CPUInfo{Model: "Apple M4", PhysicalCores: 10, LogicalCores: 10},
			Memory:      model.MemoryInfo{TotalBytes: 16 << 30, State: model.MetricOK},
			CollectedAt: time.Now().UTC(),
		},
		network: model.NetworkSnapshot{
			Interfaces: []model.Interface{{
				Name: "en0", Up: true, Kind: model.KindEthernet,
				IPv6: []model.Address{{Address: "2001:db8::1", PrefixLen: 64, Family: "ipv6", Scope: "global"}},
			}},
			HasIPv6: true, HasGlobalV6: true, CollectedAt: time.Now().UTC(),
		},
		firewall: model.FirewallStatus{
			Managed:     model.StateNotConfigured,
			PF:          model.PFInfo{State: "unknown", ReadAvailable: false, ReadError: "permission denied"},
			Anchor:      model.AnchorInfo{Name: "com.homelab.manager.ipv6", State: model.AnchorAbsent},
			CollectedAt: time.Now().UTC(),
			Writable:    false,
		},
		health: model.AgentHealth{Version: "test", StartedAt: time.Now().UTC(), IsRoot: true, MethodCount: 4},
	}

	svc := app.NewService(caller)
	authMgr := auth.NewManager(db)
	auditStore := audit.NewStore(db, quietLogger())

	srv := New(Options{
		Config:  cfg,
		Service: svc,
		Audit:   auditStore,
		Auth:    authMgr,
		Logger:  quietLogger(),
		WebFS:   nil,
	})

	return &testEnv{handler: srv.Handler(), caller: caller, db: db, auth: authMgr, cfg: cfg}
}

// do 发起请求。host 为空时使用默认 Host。
func (e *testEnv) do(t *testing.T, method, path string, body any, mutate ...func(*http.Request)) *httptest.ResponseRecorder {
	t.Helper()

	var rdr io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("序列化请求体失败: %v", err)
		}
		rdr = bytes.NewReader(b)
	}
	req := httptest.NewRequest(method, path, rdr)
	// httptest.NewRequest 会把 Host 设为 example.com，这里改成本机监听地址，
	// 否则会被 HostGuard 拒绝。
	req.Host = "127.0.0.1:7788"
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	// httptest 默认 RemoteAddr 是 192.0.2.1:1234，需要改成回环以模拟本机访问。
	req.RemoteAddr = "127.0.0.1:54321"
	for _, m := range mutate {
		m(req)
	}

	rec := httptest.NewRecorder()
	e.handler.ServeHTTP(rec, req)
	return rec
}

// decode 解析响应。
func decode(t *testing.T, rec *httptest.ResponseRecorder) envelope {
	t.Helper()
	var env envelope
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
		t.Fatalf("解析响应失败: %v（body=%s）", err, rec.Body.String())
	}
	return env
}

// TestHealthzPublic 验证健康检查无需认证且不含敏感信息。
func TestHealthzPublic(t *testing.T) {
	env := newTestEnv(t, false)
	rec := env.do(t, http.MethodGet, "/api/v1/healthz", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("期望 200，实际 %d", rec.Code)
	}
	body := rec.Body.String()
	if strings.Contains(body, "password") || strings.Contains(body, "token") {
		t.Errorf("健康检查不应包含敏感字段: %s", body)
	}
}

// TestOverviewLocalMode 验证本机请求可直接读取系统概览。
func TestOverviewLocalMode(t *testing.T) {
	env := newTestEnv(t, false)
	rec := env.do(t, http.MethodGet, "/api/v1/system/overview", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("期望 200，实际 %d（%s）", rec.Code, rec.Body.String())
	}
	env2 := decode(t, rec)
	if !env2.OK {
		t.Fatalf("期望成功: %+v", env2.Error)
	}
}

// TestOverviewRejectsNonLocalInLocalMode 验证本地模式下外部地址被拒绝。
func TestOverviewRejectsNonLocalInLocalMode(t *testing.T) {
	env := newTestEnv(t, false)
	rec := env.do(t, http.MethodGet, "/api/v1/system/overview", nil, func(r *http.Request) {
		r.RemoteAddr = "192.168.1.50:12345"
	})
	if rec.Code != http.StatusForbidden {
		t.Fatalf("非本机请求应被拒绝，实际 %d", rec.Code)
	}
}

// TestRemoteModeRequiresAuth 验证远程模式未登录返回 401。
func TestRemoteModeRequiresAuth(t *testing.T) {
	env := newTestEnv(t, true)
	rec := env.do(t, http.MethodGet, "/api/v1/system/overview", nil, func(r *http.Request) {
		r.Header.Set("X-HLM-Request", "1")
		r.RemoteAddr = "192.168.1.50:12345"
	})
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("未登录应返回 401，实际 %d", rec.Code)
	}
}

// TestRemoteModeRequiresCustomHeader 验证缺少 X-HLM-Request 头被拒绝（CSRF 防护）。
func TestRemoteModeRequiresCustomHeader(t *testing.T) {
	env := newTestEnv(t, true)
	ctx := context.Background()
	if err := env.auth.SetPassword(ctx, "a-strong-password-here"); err != nil {
		t.Fatalf("设置密码失败: %v", err)
	}
	token, _, err := env.auth.CreateSession(ctx, "192.168.1.50", "ua")
	if err != nil {
		t.Fatalf("创建会话失败: %v", err)
	}

	rec := env.do(t, http.MethodGet, "/api/v1/system/overview", nil, func(r *http.Request) {
		r.AddCookie(&http.Cookie{Name: auth.SessionCookieName, Value: token})
		// 故意不设置 X-HLM-Request
		r.RemoteAddr = "192.168.1.50:12345"
	})
	if rec.Code != http.StatusForbidden {
		t.Fatalf("缺少 X-HLM-Request 应返回 403，实际 %d", rec.Code)
	}
}

// TestRemoteModeWithSession 验证已登录 + 自定义头可访问。
func TestRemoteModeWithSession(t *testing.T) {
	env := newTestEnv(t, true)
	ctx := context.Background()
	if err := env.auth.SetPassword(ctx, "a-strong-password-here"); err != nil {
		t.Fatalf("设置密码失败: %v", err)
	}
	token, _, err := env.auth.CreateSession(ctx, "192.168.1.50", "ua")
	if err != nil {
		t.Fatalf("创建会话失败: %v", err)
	}

	rec := env.do(t, http.MethodGet, "/api/v1/system/overview", nil, func(r *http.Request) {
		r.AddCookie(&http.Cookie{Name: auth.SessionCookieName, Value: token})
		r.Header.Set("X-HLM-Request", "1")
		r.RemoteAddr = "192.168.1.50:12345"
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("已登录应可访问，实际 %d（%s）", rec.Code, rec.Body.String())
	}
}

// TestHostGuardRejectsForeignHost 验证 Host 头不匹配被拒绝（DNS rebinding 防护）。
func TestHostGuardRejectsForeignHost(t *testing.T) {
	env := newTestEnv(t, false)
	rec := env.do(t, http.MethodGet, "/api/v1/system/overview", nil, func(r *http.Request) {
		r.Host = "evil.example.com"
	})
	if rec.Code != http.StatusMisdirectedRequest {
		t.Fatalf("非法 Host 应被拒绝，实际 %d", rec.Code)
	}
}

// TestOriginGuardRejectsCrossSite 验证跨站 Origin 的写请求被拒绝。
func TestOriginGuardRejectsCrossSite(t *testing.T) {
	env := newTestEnv(t, false)
	rec := env.do(t, http.MethodPost, "/api/v1/auth/logout", nil, func(r *http.Request) {
		r.Header.Set("Origin", "https://evil.example.com")
	})
	if rec.Code != http.StatusForbidden {
		t.Fatalf("跨站 Origin 应被拒绝，实际 %d", rec.Code)
	}
}

// TestOriginGuardAllowsSameOrigin 验证同源 Origin 通过。
func TestOriginGuardAllowsSameOrigin(t *testing.T) {
	env := newTestEnv(t, false)
	rec := env.do(t, http.MethodPost, "/api/v1/auth/logout", nil, func(r *http.Request) {
		r.Header.Set("Origin", "http://127.0.0.1:7788")
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("同源请求应通过，实际 %d（%s）", rec.Code, rec.Body.String())
	}
}

// TestNotImplementedReturns501 验证未实现接口返回 501 而非伪造成功。
func TestNotImplementedReturns501(t *testing.T) {
	env := newTestEnv(t, false)
	// M2 已实现防火墙规则读写，因此这里只保留仍未实现的接口。
	paths := []struct{ method, path string }{
		{http.MethodPost, "/api/v1/power/reboot"},
		{http.MethodGet, "/api/v1/services"},
		{http.MethodPost, "/api/v1/schedules"},
	}
	for _, p := range paths {
		rec := env.do(t, p.method, p.path, nil)
		if rec.Code != http.StatusNotImplemented {
			t.Errorf("%s %s 应返回 501，实际 %d", p.method, p.path, rec.Code)
		}
		e := decode(t, rec)
		if e.OK {
			t.Errorf("%s %s 不应返回成功", p.method, p.path)
		}
		if e.Error == nil || e.Error.Code != model.CodeNotImplemented {
			t.Errorf("%s %s 错误码应为 not_implemented", p.method, p.path)
		}
	}
}

// TestFirewallStatusDegraded 验证 PF 不可读时标记 degraded。
func TestFirewallStatusDegraded(t *testing.T) {
	env := newTestEnv(t, false)
	rec := env.do(t, http.MethodGet, "/api/v1/firewall/status", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("期望 200，实际 %d", rec.Code)
	}
	e := decode(t, rec)
	if e.Meta == nil || !e.Meta.Degraded {
		t.Error("PF 不可读时应标记 degraded=true")
	}
	if len(e.Meta.Notes) == 0 {
		t.Error("应给出降级说明")
	}
}

// TestFirewallStatusNotWritable 验证 M1 阶段明确声明不可写。
func TestFirewallStatusNotWritable(t *testing.T) {
	env := newTestEnv(t, false)
	rec := env.do(t, http.MethodGet, "/api/v1/firewall/status", nil)
	e := decode(t, rec)

	raw, ok := e.Data.(map[string]any)
	if !ok {
		t.Fatalf("data 类型不符: %T", e.Data)
	}
	if writable, ok := raw["writable"].(bool); !ok || writable {
		t.Error("M1 阶段 firewall.writable 必须为 false")
	}
}

// TestAgentErrorReturns503 验证 Agent 不可用时返回 503 且不返回伪造数据。
func TestAgentErrorReturns503(t *testing.T) {
	env := newTestEnv(t, false)
	env.caller.err = model.Unavailable("Agent 不可达")

	rec := env.do(t, http.MethodGet, "/api/v1/system/overview", nil)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("期望 503，实际 %d（%s）", rec.Code, rec.Body.String())
	}
	e := decode(t, rec)
	if e.OK {
		t.Error("Agent 不可用时不应返回成功")
	}
	if e.Data != nil {
		t.Errorf("Agent 不可用时不应返回数据: %+v", e.Data)
	}
}

// TestDecodeJSONRejectsUnknownField 验证未知字段被拒绝。
func TestDecodeJSONRejectsUnknownField(t *testing.T) {
	env := newTestEnv(t, false)
	rec := env.do(t, http.MethodPost, "/api/v1/auth/password", map[string]any{
		"newPassword": "a-sufficient-password",
		"admin":       true, // 未知字段
	})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("未知字段应返回 400，实际 %d（%s）", rec.Code, rec.Body.String())
	}
}

// TestPasswordTooShortViaAPI 验证短密码被拒绝。
func TestPasswordTooShortViaAPI(t *testing.T) {
	env := newTestEnv(t, false)
	rec := env.do(t, http.MethodPost, "/api/v1/auth/password", map[string]any{
		"newPassword": "short",
	})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("短密码应返回 400，实际 %d", rec.Code)
	}
}

// TestSetPasswordRequiresCurrentPassword 验证改密码需提供当前密码。
func TestSetPasswordRequiresCurrentPassword(t *testing.T) {
	env := newTestEnv(t, false)
	ctx := context.Background()
	if err := env.auth.SetPassword(ctx, "initial-password-123"); err != nil {
		t.Fatalf("设置初始密码失败: %v", err)
	}

	// 不提供当前密码：应拒绝。
	rec := env.do(t, http.MethodPost, "/api/v1/auth/password", map[string]any{
		"newPassword": "another-password-456",
	})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("缺少当前密码应返回 400，实际 %d（%s）", rec.Code, rec.Body.String())
	}

	// 提供错误的当前密码：应拒绝。
	rec = env.do(t, http.MethodPost, "/api/v1/auth/password", map[string]any{
		"currentPassword": "wrong-current-one",
		"newPassword":     "another-password-456",
	})
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("当前密码错误应返回 401，实际 %d", rec.Code)
	}

	// 正确的当前密码：应通过。
	rec = env.do(t, http.MethodPost, "/api/v1/auth/password", map[string]any{
		"currentPassword": "initial-password-123",
		"newPassword":     "another-password-456",
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("正确当前密码应通过，实际 %d（%s）", rec.Code, rec.Body.String())
	}
}

// TestChangePasswordInRemoteMode 验证远程模式下已登录取可改密码。
//
// 这是一个真实存在过的缺陷：/auth/password 注册在认证中间件之外
// （初始密码设置必须允许未登录访问），因此 handler 内的 sessionFrom
// 永远为空，导致已设置密码后无法在远程模式改密码。这里用手动会话校验修复，
// 本测试防止回归。
func TestChangePasswordInRemoteMode(t *testing.T) {
	env := newTestEnv(t, true)
	ctx := context.Background()

	// 通过 API 完成初始化（本机请求），拿到会话 cookie。
	rec := env.do(t, http.MethodPost, "/api/v1/auth/password", map[string]any{
		"newPassword": "initial-password-789",
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("初始化失败: %d（%s）", rec.Code, rec.Body.String())
	}
	cookie := sessionCookieFrom(t, rec)
	if cookie == "" {
		t.Fatal("初始化后应下发会话 cookie")
	}

	// 用该会话改密码：应成功（修复前这里会 401）。
	rec = env.do(t, http.MethodPost, "/api/v1/auth/password", map[string]any{
		"currentPassword": "initial-password-789",
		"newPassword":     "changed-password-321",
	}, func(r *http.Request) {
		r.AddCookie(&http.Cookie{Name: auth.SessionCookieName, Value: cookie})
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("远程模式已登录应能改密码，实际 %d（%s）", rec.Code, rec.Body.String())
	}

	// 新密码可登录。
	if err := env.auth.VerifyPassword(ctx, "10.0.0.5", "changed-password-321"); err != nil {
		t.Errorf("新密码应可登录: %v", err)
	}
	// 旧密码失效。
	if err := env.auth.VerifyPassword(ctx, "10.0.0.5", "initial-password-789"); err == nil {
		t.Error("旧密码应失效")
	}
}

// TestChangePasswordRemoteWithoutSessionRejected 验证远程模式未登录不能改密码。
func TestChangePasswordRemoteWithoutSessionRejected(t *testing.T) {
	env := newTestEnv(t, true)

	// 先初始化密码（本机请求）。
	rec := env.do(t, http.MethodPost, "/api/v1/auth/password", map[string]any{
		"newPassword": "initial-password-789",
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("初始化失败: %d", rec.Code)
	}

	// 不带会话改密码：应 401。
	rec = env.do(t, http.MethodPost, "/api/v1/auth/password", map[string]any{
		"currentPassword": "initial-password-789",
		"newPassword":     "another-password-555",
	}, func(r *http.Request) {
		r.RemoteAddr = "192.168.1.77:1234"
	})
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("未登录改密码应返回 401，实际 %d（%s）", rec.Code, rec.Body.String())
	}

	// 伪造会话 cookie：同样拒绝。
	rec = env.do(t, http.MethodPost, "/api/v1/auth/password", map[string]any{
		"currentPassword": "initial-password-789",
		"newPassword":     "another-password-555",
	}, func(r *http.Request) {
		r.RemoteAddr = "192.168.1.77:1234"
		r.AddCookie(&http.Cookie{Name: auth.SessionCookieName, Value: "forged-token"})
	})
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("伪造会话应返回 401，实际 %d", rec.Code)
	}
}

// sessionCookieFrom 从响应中取出会话 cookie 的值。
func sessionCookieFrom(t *testing.T, rec *httptest.ResponseRecorder) string {
	t.Helper()
	for _, c := range rec.Result().Cookies() {
		if c.Name == auth.SessionCookieName && c.Value != "" {
			return c.Value
		}
	}
	return ""
}

// TestInitialPasswordOnlyFromLocalhost 验证初始密码只能从本机设置。
//
// 这条规则是为了在「远程模式」下打破「登录需要密码、设密码需要登录」的死锁：
// 引导阶段只认回环地址，之后改密码才要求会话 + 当前密码。
func TestInitialPasswordOnlyFromLocalhost(t *testing.T) {
	env := newTestEnv(t, true)

	// 1. 远程地址尝试初始化：必须拒绝（403），而不是 401 死锁。
	rec := env.do(t, http.MethodPost, "/api/v1/auth/password", map[string]any{
		"newPassword": "init-password-123",
	}, func(r *http.Request) {
		r.RemoteAddr = "192.168.1.50:12345"
		r.Header.Set("X-HLM-Request", "1")
	})
	if rec.Code != http.StatusForbidden {
		t.Fatalf("远程初始化应返回 403，实际 %d（%s）", rec.Code, rec.Body.String())
	}

	// 2. 本机地址初始化：应成功，且标记 initialized=true。
	rec = env.do(t, http.MethodPost, "/api/v1/auth/password", map[string]any{
		"newPassword": "init-password-123",
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("本机初始化应成功，实际 %d（%s）", rec.Code, rec.Body.String())
	}
	e := decode(t, rec)
	data, _ := e.Data.(map[string]any)
	if initialized, ok := data["initialized"].(bool); !ok || !initialized {
		t.Errorf("初始化响应应标记 initialized=true，实际 %v", data)
	}

	// 3. 初始化后可以用新密码登录。
	rec = env.do(t, http.MethodPost, "/api/v1/auth/login",
		map[string]any{"password": "init-password-123"},
		func(r *http.Request) {
			r.RemoteAddr = "192.168.1.50:12345"
			r.Header.Set("X-HLM-Request", "1")
		})
	if rec.Code != http.StatusOK {
		t.Fatalf("初始化后应能登录，实际 %d（%s）", rec.Code, rec.Body.String())
	}
}

// TestLoginDisabledInLocalMode 验证本地模式不提供登录接口。
func TestLoginDisabledInLocalMode(t *testing.T) {
	env := newTestEnv(t, false)
	rec := env.do(t, http.MethodPost, "/api/v1/auth/login", map[string]any{"password": "x"})
	if rec.Code != http.StatusNotImplemented {
		t.Fatalf("本地模式登录应返回 501，实际 %d", rec.Code)
	}
}

// TestLoginRateLimited 验证登录限速。
func TestLoginRateLimited(t *testing.T) {
	env := newTestEnv(t, true)
	ctx := context.Background()
	if err := env.auth.SetPassword(ctx, "a-strong-password-here"); err != nil {
		t.Fatalf("设置密码失败: %v", err)
	}

	// 连续失败直到限速。
	var gotRateLimit bool
	for i := 0; i < auth.MaxLoginFailures+2; i++ {
		rec := env.do(t, http.MethodPost, "/api/v1/auth/login",
			map[string]any{"password": "wrong-password-x"},
			func(r *http.Request) {
				r.RemoteAddr = "203.0.113.9:1234"
				r.Header.Set("X-HLM-Request", "1")
			})
		if rec.Code == http.StatusTooManyRequests {
			gotRateLimit = true
			break
		}
	}
	if !gotRateLimit {
		t.Fatal("连续失败应触发限速（429）")
	}
}

// TestAuthStatusReportsMode 验证认证状态返回访问模式。
func TestAuthStatusReportsMode(t *testing.T) {
	for _, allowRemote := range []bool{false, true} {
		env := newTestEnv(t, allowRemote)
		rec := env.do(t, http.MethodGet, "/api/v1/auth/status", nil)
		if rec.Code != http.StatusOK {
			t.Fatalf("期望 200，实际 %d", rec.Code)
		}
		e := decode(t, rec)
		data, ok := e.Data.(map[string]any)
		if !ok {
			t.Fatalf("data 类型不符: %T", e.Data)
		}
		want := "local"
		if allowRemote {
			want = "remote"
		}
		if got, _ := data["mode"].(string); got != want {
			t.Errorf("mode 期望 %s，实际 %s", want, got)
		}
	}
}

// TestAuditListLimitValidation 验证 limit 参数校验。
func TestAuditListLimitValidation(t *testing.T) {
	env := newTestEnv(t, false)
	for _, q := range []string{"limit=0", "limit=501", "limit=abc", "limit=-5"} {
		rec := env.do(t, http.MethodGet, "/api/v1/audit?"+q, nil)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("limit 参数 %q 应返回 400，实际 %d", q, rec.Code)
		}
	}
	rec := env.do(t, http.MethodGet, "/api/v1/audit?limit=10", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("合法 limit 应返回 200，实际 %d（%s）", rec.Code, rec.Body.String())
	}
}

// TestSecurityHeaders 验证安全响应头存在。
func TestSecurityHeaders(t *testing.T) {
	env := newTestEnv(t, false)
	rec := env.do(t, http.MethodGet, "/api/v1/healthz", nil)

	want := map[string]string{
		"X-Content-Type-Options": "nosniff",
		"X-Frame-Options":        "DENY",
		"Referrer-Policy":        "no-referrer",
	}
	for k, v := range want {
		if got := rec.Header().Get(k); got != v {
			t.Errorf("响应头 %s 期望 %q，实际 %q", k, v, got)
		}
	}
	if csp := rec.Header().Get("Content-Security-Policy"); !strings.Contains(csp, "frame-ancestors 'none'") {
		t.Errorf("CSP 应包含 frame-ancestors 'none'，实际 %q", csp)
	}
	if ra := rec.Header().Get("Cache-Control"); ra != "no-store" {
		t.Errorf("API 响应应禁止缓存，实际 %q", ra)
	}
	if rid := rec.Header().Get("X-Request-Id"); rid == "" {
		t.Error("应返回 X-Request-Id")
	}
}

// TestMetaReportsFeatureFlags 验证能力声明为 false（M1 未实现写操作）。
func TestMetaReportsFeatureFlags(t *testing.T) {
	env := newTestEnv(t, false)
	rec := env.do(t, http.MethodGet, "/api/v1/meta", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("期望 200，实际 %d", rec.Code)
	}
	e := decode(t, rec)
	data := e.Data.(map[string]any)
	features := data["features"].(map[string]any)

	// M2 已实现防火墙写入，其余仍为 false
	for _, k := range []string{"powerControl", "serviceControl", "scheduleControl"} {
		if v, ok := features[k].(bool); !ok || v {
			t.Errorf("M2 阶段 %s 仍必须为 false", k)
		}
	}
	for _, k := range []string{"firewallRead", "firewallWrite"} {
		if v, _ := features[k].(bool); !v {
			t.Errorf("M2 阶段 %s 应为 true", k)
		}
	}
}

// TestAuditRecorded 验证审计记录写入。
func TestAuditRecorded(t *testing.T) {
	env := newTestEnv(t, false)
	_ = env.do(t, http.MethodGet, "/api/v1/firewall/status", nil)

	var count int
	if err := env.db.SQL().QueryRowContext(context.Background(),
		`SELECT COUNT(*) FROM audit_logs WHERE action = 'firewall.status'`).Scan(&count); err != nil {
		t.Fatalf("查询审计失败: %v", err)
	}
	if count == 0 {
		t.Error("防火墙状态查询应写入审计日志")
	}
}

// TestAuditDoesNotLogSecrets 验证审计不含敏感信息。
func TestAuditDoesNotLogSecrets(t *testing.T) {
	env := newTestEnv(t, true)
	ctx := context.Background()
	if err := env.auth.SetPassword(ctx, "secret-password-xyz"); err != nil {
		t.Fatalf("设置密码失败: %v", err)
	}

	_ = env.do(t, http.MethodPost, "/api/v1/auth/login",
		map[string]any{"password": "secret-password-xyz"},
		func(r *http.Request) {
			r.RemoteAddr = "203.0.113.20:1234"
			r.Header.Set("X-HLM-Request", "1")
		})

	rows, err := env.db.SQL().QueryContext(ctx,
		`SELECT COALESCE(detail, '') FROM audit_logs`)
	if err != nil {
		t.Fatalf("查询审计失败: %v", err)
	}
	defer rows.Close()
	for rows.Next() {
		var detail string
		if err := rows.Scan(&detail); err != nil {
			t.Fatalf("扫描失败: %v", err)
		}
		if strings.Contains(detail, "secret-password-xyz") {
			t.Fatal("审计日志不得记录密码明文")
		}
	}
}

// TestMethodNotAllowedOnReadEndpoint 验证 GET 端点不接受 POST。
func TestMethodNotAllowedOnReadEndpoint(t *testing.T) {
	env := newTestEnv(t, false)
	rec := env.do(t, http.MethodPost, "/api/v1/system/overview", nil)
	if rec.Code != http.StatusMethodNotAllowed && rec.Code != http.StatusNotFound {
		t.Fatalf("POST 到只读端点应被拒绝，实际 %d", rec.Code)
	}
}

// quietLogger 返回丢弃输出的 logger。
func quietLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelError}))
}
