// Package auth 实现认证与会话管理。
//
// 访问模型分两种（本地模式/远程模式）：
//
//	本地模式（默认，监听回环）：请求来自本机浏览器，视为已授权。
//	  仍然做 Host 与 Origin 校验，防止 DNS rebinding 与跨站请求。
//
//	远程模式（需显式开启）：必须先用密码登录换取会话 cookie。
//	  强制要求 HTTPS（由反向代理或内置 TLS 承担）、SameSite=Strict cookie、
//	  以及自定义请求头（X-HLM-Request），三者共同构成 CSRF 防护。
//
// 密码永不明文存储：使用 scrypt + 每用户随机盐，比较时用常量时间。
package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/scrypt"

	"github.com/lancelot-lewis/MacPanel/internal/database"
)

// 会话与限速参数。
const (
	SessionCookieName = "hlm_session"
	// SessionTTL 会话有效期。远程管理属低频操作，8 小时足够且风险可控。
	SessionTTL = 8 * time.Hour
	// SessionIdleTTL 超过该时长未使用即失效。
	SessionIdleTTL = 2 * time.Hour
	// MaxLoginFailures 单地址窗口内允许的失败次数。
	MaxLoginFailures = 5
	// LoginFailureWindow 失败计数窗口。
	LoginFailureWindow = 15 * time.Minute
	// MinPasswordLength 密码最短长度。远程管理面板不允许弱口令。
	MinPasswordLength = 12
)

// scrypt 参数。N=2^15 在 M 系列芯片上约 50-100ms，兼顾安全与体验。
const (
	scryptN      = 1 << 15
	scryptR      = 8
	scryptP      = 1
	scryptKeyLen = 32
	saltLen      = 16
)

// ErrInvalidCredentials 表示用户名/密码错误。对外统一返回，不区分具体原因。
var ErrInvalidCredentials = errors.New("auth: 凭据无效")

// ErrNoPassword 表示尚未设置管理密码。
var ErrNoPassword = errors.New("auth: 尚未设置管理密码")

// ErrTooManyAttempts 表示触发登录限速。
var ErrTooManyAttempts = errors.New("auth: 登录尝试过于频繁，请稍后再试")

// ErrSessionInvalid 表示会话不存在、过期或已撤销。
var ErrSessionInvalid = errors.New("auth: 会话无效")

// Session 是已认证会话的视图。
type Session struct {
	ID         string    `json:"id"`
	CreatedAt  time.Time `json:"createdAt"`
	LastSeenAt time.Time `json:"lastSeenAt"`
	ExpiresAt  time.Time `json:"expiresAt"`
	RemoteAddr string    `json:"remoteAddr"`
}

// Manager 负责密码、会话与登录限速。
type Manager struct {
	db *database.DB

	// 单实例内串行化登录尝试，避免并发爆破绕过计数。
	loginMu sync.Mutex
}

// NewManager 创建认证管理器。
func NewManager(db *database.DB) *Manager {
	return &Manager{db: db}
}

const passwordSettingKey = "auth.password_hash"

// HasPassword 判断是否已设置管理密码。
func (m *Manager) HasPassword(ctx context.Context) (bool, error) {
	var v string
	err := m.db.SQL().QueryRowContext(ctx,
		`SELECT value FROM settings WHERE key = ?`, passwordSettingKey).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("auth: 读取密码配置失败: %w", err)
	}
	return strings.TrimSpace(v) != "", nil
}

// SetPassword 设置（或重置）管理密码。要求调用方已完成授权（本地模式或 CLI）。
func (m *Manager) SetPassword(ctx context.Context, password string) error {
	if len(password) < MinPasswordLength {
		return fmt.Errorf("auth: 密码长度至少 %d 个字符", MinPasswordLength)
	}
	hash, err := hashPassword(password)
	if err != nil {
		return err
	}
	_, err = m.db.SQL().ExecContext(ctx,
		`INSERT INTO settings (key, value, updated_at) VALUES (?, ?, ?)
		 ON CONFLICT(key) DO UPDATE SET value = excluded.value, updated_at = excluded.updated_at`,
		passwordSettingKey, hash, database.FormatTime(database.Now()))
	if err != nil {
		return fmt.Errorf("auth: 保存密码失败: %w", err)
	}
	// 改密码后撤销所有既有会话，避免旧会话继续可用。
	return m.RevokeAllSessions(ctx)
}

// VerifyPassword 校验密码，失败时记录尝试次数。
func (m *Manager) VerifyPassword(ctx context.Context, remoteAddr, password string) error {
	m.loginMu.Lock()
	defer m.loginMu.Unlock()

	if err := m.checkRate(ctx, remoteAddr); err != nil {
		return err
	}

	var stored string
	err := m.db.SQL().QueryRowContext(ctx,
		`SELECT value FROM settings WHERE key = ?`, passwordSettingKey).Scan(&stored)
	if errors.Is(err, sql.ErrNoRows) || (err == nil && strings.TrimSpace(stored) == "") {
		return ErrNoPassword
	}
	if err != nil {
		return fmt.Errorf("auth: 读取密码配置失败: %w", err)
	}

	ok, verr := verifyHash(stored, password)
	if verr != nil {
		// 存储格式损坏：记录失败但对外仍返回凭据无效，避免泄漏内部状态。
		_ = m.recordAttempt(ctx, remoteAddr, false)
		return ErrInvalidCredentials
	}
	if !ok {
		if err := m.recordAttempt(ctx, remoteAddr, false); err != nil {
			return err
		}
		return ErrInvalidCredentials
	}

	if err := m.recordAttempt(ctx, remoteAddr, true); err != nil {
		return err
	}
	return nil
}

// CreateSession 创建会话，返回明文令牌（仅此处可见，DB 只存哈希）。
func (m *Manager) CreateSession(ctx context.Context, remoteAddr, userAgent string) (string, Session, error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", Session{}, fmt.Errorf("auth: 生成会话令牌失败: %w", err)
	}
	token := base64.RawURLEncoding.EncodeToString(raw)
	now := database.Now()
	expires := now.Add(SessionTTL)
	id := hashToken(token)[:16]

	_, err := m.db.SQL().ExecContext(ctx,
		`INSERT INTO auth_sessions (id, token_hash, created_at, last_seen_at, expires_at, remote_addr, user_agent)
		 VALUES (?, ?, ?, ?, ?, ?, ?)`,
		id, hashToken(token),
		database.FormatTime(now), database.FormatTime(now), database.FormatTime(expires),
		remoteAddr, truncate(userAgent, 200),
	)
	if err != nil {
		return "", Session{}, fmt.Errorf("auth: 创建会话失败: %w", err)
	}

	return token, Session{
		ID:         id,
		CreatedAt:  now,
		LastSeenAt: now,
		ExpiresAt:  expires,
		RemoteAddr: remoteAddr,
	}, nil
}

// ValidateSession 校验会话令牌，并刷新最后使用时间。
func (m *Manager) ValidateSession(ctx context.Context, token string) (Session, error) {
	if token == "" {
		return Session{}, ErrSessionInvalid
	}
	id := hashToken(token)[:16]

	var (
		s        Session
		created  string
		lastSeen string
		expires  string
		revoked  sql.NullString
	)
	err := m.db.SQL().QueryRowContext(ctx,
		`SELECT id, created_at, last_seen_at, expires_at, remote_addr, revoked_at
		 FROM auth_sessions WHERE id = ? AND token_hash = ?`, id, hashToken(token),
	).Scan(&s.ID, &created, &lastSeen, &expires, &s.RemoteAddr, &revoked)
	if errors.Is(err, sql.ErrNoRows) {
		return Session{}, ErrSessionInvalid
	}
	if err != nil {
		return Session{}, fmt.Errorf("auth: 查询会话失败: %w", err)
	}
	if revoked.Valid {
		return Session{}, ErrSessionInvalid
	}

	now := database.Now()
	if t, err := database.ParseTime(expires); err == nil && now.After(t) {
		return Session{}, ErrSessionInvalid
	}
	if t, err := database.ParseTime(lastSeen); err == nil && now.Sub(t) > SessionIdleTTL {
		// 空闲超时：主动撤销，避免长期挂着的会话被复用。
		_ = m.RevokeSession(ctx, token)
		return Session{}, ErrSessionInvalid
	}

	s.CreatedAt, _ = database.ParseTime(created)
	s.LastSeenAt, _ = database.ParseTime(lastSeen)
	s.ExpiresAt, _ = database.ParseTime(expires)

	// 节流刷新：每 30 秒最多写一次，避免每个请求都产生写事务。
	if now.Sub(s.LastSeenAt) > 30*time.Second {
		_, _ = m.db.SQL().ExecContext(ctx,
			`UPDATE auth_sessions SET last_seen_at = ? WHERE id = ?`,
			database.FormatTime(now), id)
	}
	return s, nil
}

// RevokeSession 撤销单个会话。
func (m *Manager) RevokeSession(ctx context.Context, token string) error {
	id := hashToken(token)[:16]
	_, err := m.db.SQL().ExecContext(ctx,
		`UPDATE auth_sessions SET revoked_at = ? WHERE id = ? AND revoked_at IS NULL`,
		database.FormatTime(database.Now()), id)
	if err != nil {
		return fmt.Errorf("auth: 撤销会话失败: %w", err)
	}
	return nil
}

// RevokeAllSessions 撤销全部会话。
func (m *Manager) RevokeAllSessions(ctx context.Context) error {
	_, err := m.db.SQL().ExecContext(ctx,
		`UPDATE auth_sessions SET revoked_at = ? WHERE revoked_at IS NULL`,
		database.FormatTime(database.Now()))
	if err != nil {
		return fmt.Errorf("auth: 撤销全部会话失败: %w", err)
	}
	return nil
}

// PurgeExpired 清理过期会话与旧登录尝试记录。
func (m *Manager) PurgeExpired(ctx context.Context) error {
	now := database.FormatTime(database.Now())
	if _, err := m.db.SQL().ExecContext(ctx,
		`DELETE FROM auth_sessions WHERE expires_at < ?`, now); err != nil {
		return fmt.Errorf("auth: 清理过期会话失败: %w", err)
	}
	cutoff := database.FormatTime(database.Now().Add(-LoginFailureWindow))
	if _, err := m.db.SQL().ExecContext(ctx,
		`DELETE FROM auth_attempts WHERE at < ?`, cutoff); err != nil {
		return fmt.Errorf("auth: 清理登录记录失败: %w", err)
	}
	return nil
}

func (m *Manager) checkRate(ctx context.Context, remoteAddr string) error {
	cutoff := database.FormatTime(database.Now().Add(-LoginFailureWindow))
	var failures int
	err := m.db.SQL().QueryRowContext(ctx,
		`SELECT COUNT(*) FROM auth_attempts
		 WHERE remote_addr = ? AND success = 0 AND at > ?`,
		remoteAddr, cutoff).Scan(&failures)
	if err != nil {
		return fmt.Errorf("auth: 统计登录失败次数失败: %w", err)
	}
	if failures >= MaxLoginFailures {
		return ErrTooManyAttempts
	}
	return nil
}

func (m *Manager) recordAttempt(ctx context.Context, remoteAddr string, success bool) error {
	successInt := 0
	if success {
		successInt = 1
	}
	_, err := m.db.SQL().ExecContext(ctx,
		`INSERT INTO auth_attempts (at, remote_addr, success) VALUES (?, ?, ?)`,
		database.FormatTime(database.Now()), remoteAddr, successInt)
	if err != nil {
		return fmt.Errorf("auth: 记录登录尝试失败: %w", err)
	}
	// 成功后清理该地址的失败记录，避免影响后续登录。
	if success {
		_, _ = m.db.SQL().ExecContext(ctx,
			`DELETE FROM auth_attempts WHERE remote_addr = ? AND success = 0`, remoteAddr)
	}
	return nil
}

// hashPassword 生成 scrypt 哈希，格式：scrypt$N$r$p$<salt_b64>$<hash_b64>
func hashPassword(password string) (string, error) {
	salt := make([]byte, saltLen)
	if _, err := rand.Read(salt); err != nil {
		return "", fmt.Errorf("auth: 生成盐失败: %w", err)
	}
	key, err := scrypt.Key([]byte(password), salt, scryptN, scryptR, scryptP, scryptKeyLen)
	if err != nil {
		return "", fmt.Errorf("auth: 计算密码哈希失败: %w", err)
	}
	return fmt.Sprintf("scrypt$%d$%d$%d$%s$%s",
		scryptN, scryptR, scryptP,
		base64.RawStdEncoding.EncodeToString(salt),
		base64.RawStdEncoding.EncodeToString(key)), nil
}

// verifyHash 常量时间校验密码。
func verifyHash(stored, password string) (bool, error) {
	parts := strings.Split(stored, "$")
	if len(parts) != 6 || parts[0] != "scrypt" {
		return false, fmt.Errorf("auth: 密码哈希格式不支持")
	}
	var n, r, p int
	if _, err := fmt.Sscanf(parts[1], "%d", &n); err != nil {
		return false, fmt.Errorf("auth: 解析 scrypt 参数失败")
	}
	if _, err := fmt.Sscanf(parts[2], "%d", &r); err != nil {
		return false, fmt.Errorf("auth: 解析 scrypt 参数失败")
	}
	if _, err := fmt.Sscanf(parts[3], "%d", &p); err != nil {
		return false, fmt.Errorf("auth: 解析 scrypt 参数失败")
	}
	// 限制参数上限，防止被篡改的哈希拖慢甚至 OOM。
	if n < 1<<12 || n > 1<<18 || r < 1 || r > 32 || p < 1 || p > 4 {
		return false, fmt.Errorf("auth: scrypt 参数超出允许范围")
	}

	salt, err := base64.RawStdEncoding.DecodeString(parts[4])
	if err != nil {
		return false, fmt.Errorf("auth: 解析盐失败")
	}
	want, err := base64.RawStdEncoding.DecodeString(parts[5])
	if err != nil {
		return false, fmt.Errorf("auth: 解析哈希失败")
	}

	got, err := scrypt.Key([]byte(password), salt, n, r, p, len(want))
	if err != nil {
		return false, fmt.Errorf("auth: 计算密码哈希失败")
	}
	return subtle.ConstantTimeCompare(got, want) == 1, nil
}

func hashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max]
}
