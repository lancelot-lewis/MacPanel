package auth

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/lancelot-lewis/MacPanel/internal/database"
)

// newTestManager 创建基于临时数据库的认证管理器。
func newTestManager(t *testing.T) *Manager {
	t.Helper()
	path := filepath.Join(t.TempDir(), "auth.db")
	db, err := database.Open(context.Background(), path)
	if err != nil {
		t.Fatalf("打开数据库失败: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return NewManager(db)
}

// TestPasswordTooShort 验证短密码被拒绝。
func TestPasswordTooShort(t *testing.T) {
	m := newTestManager(t)
	err := m.SetPassword(context.Background(), "short")
	if err == nil {
		t.Fatal("短密码应被拒绝")
	}
	if !strings.Contains(err.Error(), "至少") {
		t.Errorf("错误信息应说明长度要求: %v", err)
	}
}

// TestSetAndVerifyPassword 验证设置与校验密码。
func TestSetAndVerifyPassword(t *testing.T) {
	m := newTestManager(t)
	ctx := context.Background()

	has, err := m.HasPassword(ctx)
	if err != nil {
		t.Fatalf("查询密码状态失败: %v", err)
	}
	if has {
		t.Fatal("初始状态不应有密码")
	}

	const pw = "correct-horse-battery"
	if err := m.SetPassword(ctx, pw); err != nil {
		t.Fatalf("设置密码失败: %v", err)
	}

	has, err = m.HasPassword(ctx)
	if err != nil || !has {
		t.Fatalf("设置后应报告已有密码: has=%v err=%v", has, err)
	}

	if err := m.VerifyPassword(ctx, "127.0.0.1", pw); err != nil {
		t.Fatalf("正确密码应通过: %v", err)
	}
	if err := m.VerifyPassword(ctx, "127.0.0.1", "wrong-password-here"); !errors.Is(err, ErrInvalidCredentials) {
		t.Fatalf("错误密码应返回 ErrInvalidCredentials，实际 %v", err)
	}
}

// TestVerifyWithoutPassword 验证未设密码时返回明确错误。
func TestVerifyWithoutPassword(t *testing.T) {
	m := newTestManager(t)
	err := m.VerifyPassword(context.Background(), "127.0.0.1", "anything-at-all")
	if !errors.Is(err, ErrNoPassword) {
		t.Fatalf("应返回 ErrNoPassword，实际 %v", err)
	}
}

// TestPasswordHashNotPlaintext 验证数据库不存明文密码。
func TestPasswordHashNotPlaintext(t *testing.T) {
	path := filepath.Join(t.TempDir(), "plain.db")
	db, err := database.Open(context.Background(), path)
	if err != nil {
		t.Fatalf("打开数据库失败: %v", err)
	}
	defer db.Close()

	m := NewManager(db)
	const pw = "super-secret-password-123"
	if err := m.SetPassword(context.Background(), pw); err != nil {
		t.Fatalf("设置密码失败: %v", err)
	}

	var stored string
	if err := db.SQL().QueryRowContext(context.Background(),
		`SELECT value FROM settings WHERE key = 'auth.password_hash'`).Scan(&stored); err != nil {
		t.Fatalf("读取哈希失败: %v", err)
	}
	if strings.Contains(stored, pw) {
		t.Fatal("数据库中不得出现明文密码")
	}
	if !strings.HasPrefix(stored, "scrypt$") {
		t.Errorf("哈希格式不符: %.20s...", stored)
	}
}

// TestRateLimitAfterFailures 验证连续失败触发限速。
func TestRateLimitAfterFailures(t *testing.T) {
	m := newTestManager(t)
	ctx := context.Background()
	const pw = "a-good-long-password"

	if err := m.SetPassword(ctx, pw); err != nil {
		t.Fatalf("设置密码失败: %v", err)
	}

	addr := "10.0.0.99"
	for i := 0; i < MaxLoginFailures; i++ {
		err := m.VerifyPassword(ctx, addr, "wrong-guess-here")
		if !errors.Is(err, ErrInvalidCredentials) {
			t.Fatalf("第 %d 次失败应返回 ErrInvalidCredentials，实际 %v", i+1, err)
		}
	}

	// 超过阈值后，即使密码正确也应被限速。
	err := m.VerifyPassword(ctx, addr, pw)
	if !errors.Is(err, ErrTooManyAttempts) {
		t.Fatalf("超过阈值应返回 ErrTooManyAttempts，实际 %v", err)
	}

	// 其它地址不受影响。
	if err := m.VerifyPassword(ctx, "10.0.0.100", pw); err != nil {
		t.Fatalf("其它地址不应被限速: %v", err)
	}
}

// TestSuccessfulLoginResetsFailures 验证成功登录清零失败计数。
func TestSuccessfulLoginResetsFailures(t *testing.T) {
	m := newTestManager(t)
	ctx := context.Background()
	const pw = "another-good-password"
	if err := m.SetPassword(ctx, pw); err != nil {
		t.Fatalf("设置密码失败: %v", err)
	}

	addr := "10.0.0.50"
	for i := 0; i < MaxLoginFailures-1; i++ {
		_ = m.VerifyPassword(ctx, addr, "bad-guess-attempt")
	}
	if err := m.VerifyPassword(ctx, addr, pw); err != nil {
		t.Fatalf("成功登录应通过: %v", err)
	}

	// 计数已清零，应还能继续尝试。
	for i := 0; i < MaxLoginFailures-1; i++ {
		err := m.VerifyPassword(ctx, addr, "bad-guess-again")
		if errors.Is(err, ErrTooManyAttempts) {
			t.Fatalf("成功登录后计数应清零，第 %d 次即被限速", i+1)
		}
	}
}

// TestSessionLifecycle 验证会话创建、校验、撤销。
func TestSessionLifecycle(t *testing.T) {
	m := newTestManager(t)
	ctx := context.Background()

	token, sess, err := m.CreateSession(ctx, "127.0.0.1", "test-agent")
	if err != nil {
		t.Fatalf("创建会话失败: %v", err)
	}
	if token == "" || sess.ID == "" {
		t.Fatal("会话令牌与 ID 不应为空")
	}

	got, err := m.ValidateSession(ctx, token)
	if err != nil {
		t.Fatalf("校验会话失败: %v", err)
	}
	if got.ID != sess.ID {
		t.Errorf("会话 ID 不符: %s != %s", got.ID, sess.ID)
	}

	if err := m.RevokeSession(ctx, token); err != nil {
		t.Fatalf("撤销会话失败: %v", err)
	}
	if _, err := m.ValidateSession(ctx, token); !errors.Is(err, ErrSessionInvalid) {
		t.Fatalf("撤销后应无效，实际 %v", err)
	}
}

// TestValidateSessionRejectsGarbage 验证伪造令牌被拒绝。
func TestValidateSessionRejectsGarbage(t *testing.T) {
	m := newTestManager(t)
	ctx := context.Background()

	for _, token := range []string{"", "not-a-real-token", strings.Repeat("x", 200)} {
		if _, err := m.ValidateSession(ctx, token); !errors.Is(err, ErrSessionInvalid) {
			t.Errorf("令牌 %q 应被拒绝，实际 %v", token, err)
		}
	}
}

// TestChangePasswordRevokesSessions 验证改密码后旧会话全部失效。
func TestChangePasswordRevokesSessions(t *testing.T) {
	m := newTestManager(t)
	ctx := context.Background()

	if err := m.SetPassword(ctx, "first-password-here"); err != nil {
		t.Fatalf("设置密码失败: %v", err)
	}
	token, _, err := m.CreateSession(ctx, "127.0.0.1", "ua")
	if err != nil {
		t.Fatalf("创建会话失败: %v", err)
	}
	if _, err := m.ValidateSession(ctx, token); err != nil {
		t.Fatalf("新会话应有效: %v", err)
	}

	if err := m.SetPassword(ctx, "second-password-here"); err != nil {
		t.Fatalf("修改密码失败: %v", err)
	}
	if _, err := m.ValidateSession(ctx, token); !errors.Is(err, ErrSessionInvalid) {
		t.Fatalf("改密码后旧会话应失效，实际 %v", err)
	}
}

// TestSessionTokenNotStoredPlaintext 验证数据库只存哈希。
func TestSessionTokenNotStoredPlaintext(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sess.db")
	db, err := database.Open(context.Background(), path)
	if err != nil {
		t.Fatalf("打开数据库失败: %v", err)
	}
	defer db.Close()

	m := NewManager(db)
	token, _, err := m.CreateSession(context.Background(), "127.0.0.1", "ua")
	if err != nil {
		t.Fatalf("创建会话失败: %v", err)
	}

	var storedHash string
	if err := db.SQL().QueryRowContext(context.Background(),
		`SELECT token_hash FROM auth_sessions LIMIT 1`).Scan(&storedHash); err != nil {
		t.Fatalf("读取 token_hash 失败: %v", err)
	}
	if storedHash == token {
		t.Fatal("会话令牌不得明文存储")
	}
	if strings.Contains(storedHash, token) {
		t.Fatal("存储值不应包含原始令牌")
	}
}

// TestSessionIdleTimeout 验证空闲超时后会话失效。
func TestSessionIdleTimeout(t *testing.T) {
	path := filepath.Join(t.TempDir(), "idle.db")
	db, err := database.Open(context.Background(), path)
	if err != nil {
		t.Fatalf("打开数据库失败: %v", err)
	}
	defer db.Close()

	m := NewManager(db)
	ctx := context.Background()
	token, _, err := m.CreateSession(ctx, "127.0.0.1", "ua")
	if err != nil {
		t.Fatalf("创建会话失败: %v", err)
	}

	// 手动把 last_seen_at 改到空闲阈值之外。
	old := database.FormatTime(time.Now().UTC().Add(-SessionIdleTTL - time.Minute))
	if _, err := db.SQL().ExecContext(ctx,
		`UPDATE auth_sessions SET last_seen_at = ?`, old); err != nil {
		t.Fatalf("更新 last_seen_at 失败: %v", err)
	}

	if _, err := m.ValidateSession(ctx, token); !errors.Is(err, ErrSessionInvalid) {
		t.Fatalf("空闲超时应判定会话失效，实际 %v", err)
	}
}

// TestVerifyHashRejectsTamperedParams 验证被篡改的 scrypt 参数被拒绝（防 DoS）。
func TestVerifyHashRejectsTamperedParams(t *testing.T) {
	// N 过大：应被参数上限拒绝而不是真的去计算。
	huge := "scrypt$1073741824$8$1$c2FsdA$aGFzaA"
	if _, err := verifyHash(huge, "any"); err == nil {
		t.Fatal("超范围 scrypt 参数应被拒绝")
	}

	bad := "bcrypt$1000$8$1$c2FsdA$aGFzaA"
	if _, err := verifyHash(bad, "any"); err == nil {
		t.Fatal("不支持的哈希算法应被拒绝")
	}
}

// TestPurgeExpired 验证过期清理。
func TestPurgeExpired(t *testing.T) {
	m := newTestManager(t)
	ctx := context.Background()

	if _, _, err := m.CreateSession(ctx, "127.0.0.1", "ua"); err != nil {
		t.Fatalf("创建会话失败: %v", err)
	}
	// 手动把过期时间改到过去。
	if err := m.db.SQL().QueryRowContext(ctx, `SELECT 1`).Scan(new(int)); err != nil {
		t.Fatalf("探测失败: %v", err)
	}
	past := database.FormatTime(time.Now().UTC().Add(-time.Hour))
	if _, err := m.db.SQL().ExecContext(ctx,
		`UPDATE auth_sessions SET expires_at = ?`, past); err != nil {
		t.Fatalf("更新过期时间失败: %v", err)
	}

	if err := m.PurgeExpired(ctx); err != nil {
		t.Fatalf("清理失败: %v", err)
	}

	var count int
	if err := m.db.SQL().QueryRowContext(ctx,
		`SELECT COUNT(*) FROM auth_sessions`).Scan(&count); err != nil {
		t.Fatalf("统计失败: %v", err)
	}
	if count != 0 {
		t.Errorf("过期会话应被清理，剩余 %d 条", count)
	}
}
