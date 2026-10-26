// Package servicestore 是「受管服务白名单」的持久化层。
//
// 职责：只做存取与字段映射。安全校验在 model.ManagedService.Validate
// 与 services.ValidateLabelForControl 两处（分层各自独立）。
package servicestore

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/lancelot-lewis/MacPanel/internal/database"
	"github.com/lancelot-lewis/MacPanel/internal/model"
)

// Store 提供受管服务的读写。
type Store struct {
	db *database.DB
}

// New 创建仓储。
func New(db *database.DB) *Store { return &Store{db: db} }

// ErrNotFound 表示登记记录不存在。
var ErrNotFound = errors.New("受管服务未登记")

// List 返回全部登记记录（含未授权的）。
func (s *Store) List(ctx context.Context) ([]model.ManagedService, error) {
	rows, err := s.db.SQL().QueryContext(ctx, `
		SELECT id, label, domain, plist_path, display_name, allow_control, created_at, updated_at
		FROM managed_services
		ORDER BY label ASC`)
	if err != nil {
		return nil, fmt.Errorf("查询受管服务失败: %w", err)
	}
	defer rows.Close()

	out := make([]model.ManagedService, 0, 8)
	for rows.Next() {
		var (
			m         model.ManagedService
			allow     int
			createdAt string
			updatedAt string
		)
		if err := rows.Scan(&m.ID, &m.Label, &m.Domain, &m.PlistPath,
			&m.DisplayName, &allow, &createdAt, &updatedAt); err != nil {
			return nil, fmt.Errorf("扫描受管服务失败: %w", err)
		}
		m.AllowControl = allow == 1
		if t, err := database.ParseTime(createdAt); err == nil {
			m.CreatedAt = t
		}
		if t, err := database.ParseTime(updatedAt); err == nil {
			m.UpdatedAt = t
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// Get 按标签读取登记记录。
func (s *Store) Get(ctx context.Context, label string) (model.ManagedService, error) {
	row := s.db.SQL().QueryRowContext(ctx, `
		SELECT id, label, domain, plist_path, display_name, allow_control, created_at, updated_at
		FROM managed_services WHERE label = ?`, label)

	var (
		m         model.ManagedService
		allow     int
		createdAt string
		updatedAt string
	)
	err := row.Scan(&m.ID, &m.Label, &m.Domain, &m.PlistPath,
		&m.DisplayName, &allow, &createdAt, &updatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return model.ManagedService{}, ErrNotFound
	}
	if err != nil {
		return model.ManagedService{}, fmt.Errorf("读取受管服务失败: %w", err)
	}
	m.AllowControl = allow == 1
	if t, err := database.ParseTime(createdAt); err == nil {
		m.CreatedAt = t
	}
	if t, err := database.ParseTime(updatedAt); err == nil {
		m.UpdatedAt = t
	}
	return m, nil
}

// Upsert 新增或更新登记记录。
//
// 用 UPSERT 而不是「先查再插」：避免并发下的竞态。
// 重复登记同一服务应当幂等覆盖，而不是报唯一约束冲突。
func (s *Store) Upsert(ctx context.Context, m model.ManagedService) (model.ManagedService, error) {
	if err := m.Validate(); err != nil {
		return model.ManagedService{}, err
	}

	now := database.FormatTime(database.Now())
	allow := 0
	if m.AllowControl {
		allow = 1
	}

	_, err := s.db.SQL().ExecContext(ctx, `
		INSERT INTO managed_services
		(label, domain, plist_path, display_name, allow_control, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(label) DO UPDATE SET
			domain = excluded.domain,
			plist_path = excluded.plist_path,
			display_name = excluded.display_name,
			allow_control = excluded.allow_control,
			updated_at = excluded.updated_at`,
		m.Label, m.Domain, m.PlistPath, m.DisplayName, allow, now, now)
	if err != nil {
		return model.ManagedService{}, fmt.Errorf("登记受管服务失败: %w", err)
	}

	return s.Get(ctx, m.Label)
}

// Delete 取消登记。
func (s *Store) Delete(ctx context.Context, label string) error {
	if label == "" {
		return model.NewError(model.CodeInvalidArgument, "服务标签不能为空")
	}
	res, err := s.db.SQL().ExecContext(ctx,
		`DELETE FROM managed_services WHERE label = ?`, label)
	if err != nil {
		return fmt.Errorf("取消登记失败: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("确认删除结果失败: %w", err)
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// RegisteredSet 返回全部已登记标签，供列表接口区分「已登记」与「未登记」。
func (s *Store) RegisteredSet(ctx context.Context) (map[string]bool, error) {
	rows, err := s.db.SQL().QueryContext(ctx, `SELECT label FROM managed_services`)
	if err != nil {
		return nil, fmt.Errorf("查询已登记服务失败: %w", err)
	}
	defer rows.Close()

	out := make(map[string]bool, 8)
	for rows.Next() {
		var label string
		if err := rows.Scan(&label); err != nil {
			return nil, fmt.Errorf("扫描标签失败: %w", err)
		}
		out[label] = true
	}
	return out, rows.Err()
}

// AllowedSet 返回「已授权启停」的标签集合，供列表接口判定 Controllable。
//
// 只取 allow_control=1：登记了但未授权的服务仍应展示为只读。
func (s *Store) AllowedSet(ctx context.Context) (map[string]bool, error) {
	rows, err := s.db.SQL().QueryContext(ctx,
		`SELECT label FROM managed_services WHERE allow_control = 1`)
	if err != nil {
		return nil, fmt.Errorf("查询已授权服务失败: %w", err)
	}
	defer rows.Close()

	out := make(map[string]bool, 8)
	for rows.Next() {
		var label string
		if err := rows.Scan(&label); err != nil {
			return nil, fmt.Errorf("扫描标签失败: %w", err)
		}
		out[label] = true
	}
	return out, rows.Err()
}
