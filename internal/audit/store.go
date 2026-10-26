// Package audit 负责写入与查询操作审计。
//
// 原则（PLAN.md 第 4 节）：所有高风险操作都必须留痕；审计写入失败
// 不应让已成功的只读请求失败，但变更类操作必须要求审计成功。
package audit

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/lancelot-lewis/MacPanel/internal/database"
)

// Result 审计结果。
type Result string

const (
	ResultSuccess Result = "success"
	ResultFailure Result = "failure"
	ResultDenied  Result = "denied"
)

// Entry 一条审计记录。
type Entry struct {
	ID         int64          `json:"id"`
	At         time.Time      `json:"at"`
	Actor      string         `json:"actor"`
	Action     string         `json:"action"`
	Target     string         `json:"target,omitempty"`
	Result     Result         `json:"result"`
	RemoteAddr string         `json:"remoteAddr,omitempty"`
	RequestID  string         `json:"requestId,omitempty"`
	Detail     map[string]any `json:"detail,omitempty"`
}

// Store 是审计存储。
type Store struct {
	db  *database.DB
	log *slog.Logger
}

// NewStore 创建审计存储。
func NewStore(db *database.DB, log *slog.Logger) *Store {
	if log == nil {
		log = slog.Default()
	}
	return &Store{db: db, log: log}
}

// Write 写入一条审计记录。
// 只记录非敏感信息：调用方身份、动作、目标、结果，不记录令牌或请求体全文。
func (s *Store) Write(ctx context.Context, e Entry) error {
	if e.At.IsZero() {
		e.At = time.Now().UTC()
	}
	var detail sql.NullString
	if len(e.Detail) > 0 {
		b, err := json.Marshal(e.Detail)
		if err != nil {
			return fmt.Errorf("审计明细序列化失败: %w", err)
		}
		detail = sql.NullString{String: string(b), Valid: true}
	}

	_, err := s.db.SQL().ExecContext(ctx,
		`INSERT INTO audit_logs (at, actor, action, target, result, remote_addr, request_id, detail)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		database.FormatTime(e.At), e.Actor, e.Action, e.Target,
		string(e.Result), e.RemoteAddr, e.RequestID, detail,
	)
	if err != nil {
		return fmt.Errorf("写入审计日志失败: %w", err)
	}
	return nil
}

// BestEffort 写入审计但不返回错误，仅记录日志。
// 用于只读请求等「审计失败不阻断业务」的场景。
func (s *Store) BestEffort(ctx context.Context, e Entry) {
	if err := s.Write(ctx, e); err != nil {
		s.log.Warn("审计写入失败", "action", e.Action, "error", err)
	}
}

// List 按时间倒序返回审计记录，limit 上限 500。
func (s *Store) List(ctx context.Context, limit int, action string) ([]Entry, error) {
	if limit <= 0 {
		limit = 50
	}
	if limit > 500 {
		limit = 500
	}

	query := `SELECT id, at, actor, action, target, result, remote_addr, request_id, detail
	          FROM audit_logs`
	args := []any{}
	if action != "" {
		query += ` WHERE action = ?`
		args = append(args, action)
	}
	query += ` ORDER BY at DESC, id DESC LIMIT ?`
	args = append(args, limit)

	rows, err := s.db.SQL().QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("查询审计日志失败: %w", err)
	}
	defer rows.Close()

	out := make([]Entry, 0, limit)
	for rows.Next() {
		var (
			e      Entry
			atStr  string
			detail sql.NullString
			res    string
		)
		if err := rows.Scan(&e.ID, &atStr, &e.Actor, &e.Action, &e.Target,
			&res, &e.RemoteAddr, &e.RequestID, &detail); err != nil {
			return nil, fmt.Errorf("扫描审计记录失败: %w", err)
		}
		e.Result = Result(res)
		if t, err := database.ParseTime(atStr); err == nil {
			e.At = t
		}
		if detail.Valid && detail.String != "" {
			var m map[string]any
			if err := json.Unmarshal([]byte(detail.String), &m); err == nil {
				e.Detail = m
			}
		}
		out = append(out, e)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("遍历审计记录失败: %w", err)
	}
	return out, nil
}
