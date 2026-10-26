// Package firewallstore 是防火墙规则的持久化层。
//
// 职责边界：只管「存取」，不校验业务规则（校验在 model 层），
// 也不生成 PF 文本（在 pfrules 包）。这样每一层都能独立测试。
package firewallstore

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/lancelot-lewis/MacPanel/internal/database"
	"github.com/lancelot-lewis/MacPanel/internal/model"
)

// Store 提供规则的读写。
type Store struct {
	db *database.DB
}

// New 创建仓储。
func New(db *database.DB) *Store { return &Store{db: db} }

// Load 读取完整规则集（规则 + 默认拒绝接口）。
func (s *Store) Load(ctx context.Context) (model.RuleSet, error) {
	var rs model.RuleSet

	rows, err := s.db.SQL().QueryContext(ctx, `
		SELECT id, name, description, position, action, direction, protocol,
		       port_from, port_to, source_cidr, interface, enabled, builtin_template
		FROM firewall_rules
		ORDER BY position ASC, id ASC`)
	if err != nil {
		return rs, fmt.Errorf("查询规则失败: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var (
			r         model.FirewallRule
			portFrom  sql.NullInt64
			portTo    sql.NullInt64
			enabled   int
			action    string
			direction string
			protocol  string
		)
		if err := rows.Scan(
			&r.ID, &r.Name, &r.Description, &r.Position,
			&action, &direction, &protocol,
			&portFrom, &portTo, &r.SourceCIDR, &r.Interface, &enabled, &r.BuiltinTemplate,
		); err != nil {
			return rs, fmt.Errorf("扫描规则失败: %w", err)
		}
		r.Action = model.RuleAction(action)
		r.Direction = model.RuleDirection(direction)
		r.Protocol = model.RuleProtocol(protocol)
		if portFrom.Valid {
			v := int(portFrom.Int64)
			r.PortFrom = &v
		}
		if portTo.Valid {
			v := int(portTo.Int64)
			r.PortTo = &v
		}
		r.Enabled = enabled == 1
		rs.Rules = append(rs.Rules, r)
	}
	if err := rows.Err(); err != nil {
		return rs, fmt.Errorf("遍历规则失败: %w", err)
	}

	ifaces, err := s.loadDenyInterfaces(ctx)
	if err != nil {
		return rs, err
	}
	rs.DefaultDenyInterfaces = ifaces
	return rs, nil
}

func (s *Store) loadDenyInterfaces(ctx context.Context) ([]string, error) {
	rows, err := s.db.SQL().QueryContext(ctx,
		`SELECT interface FROM firewall_deny_interfaces ORDER BY interface ASC`)
	if err != nil {
		return nil, fmt.Errorf("查询默认拒绝接口失败: %w", err)
	}
	defer rows.Close()

	var out []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, fmt.Errorf("扫描接口失败: %w", err)
		}
		out = append(out, name)
	}
	return out, rows.Err()
}

// Save 用给定规则集覆盖现有规则。
//
// 整个替换在一个事务内完成：失败时不会留下「删了一半」的中间状态。
// 这是必须的——规则集不完整会导致生成出错误的 PF 文本。
//
// 返回值是写入后的规则（含数据库分配的 ID）。调用方需要 ID 来生成
// 带正确编号的 PF 注释；不返回的话界面与生成文本里会出现全为 0 的 ID，
// 排查问题时无法对应到具体规则。
func (s *Store) Save(ctx context.Context, rs model.RuleSet) (model.RuleSet, error) {
	// 先校验再落库：宁可拒绝，也不写入一份无法生成规则的数据。
	if err := rs.Validate(); err != nil {
		return rs, err
	}

	now := database.FormatTime(time.Now().UTC())

	saved := model.RuleSet{
		Rules:                 make([]model.FirewallRule, 0, len(rs.Rules)),
		DefaultDenyInterfaces: append([]string{}, rs.DefaultDenyInterfaces...),
	}

	err := s.db.Tx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `DELETE FROM firewall_rules`); err != nil {
			return fmt.Errorf("清空旧规则失败: %w", err)
		}

		for i, r := range rs.Rules {
			var portFrom, portTo any
			if r.PortFrom != nil {
				portFrom = *r.PortFrom
			}
			if r.PortTo != nil {
				portTo = *r.PortTo
			}
			enabled := 0
			if r.Enabled {
				enabled = 1
			}
			sourceCIDR := r.SourceCIDR
			if sourceCIDR == "" {
				sourceCIDR = "any"
			}
			iface := r.Interface
			if iface == "" {
				iface = "any"
			}

			res, err := tx.ExecContext(ctx, `
				INSERT INTO firewall_rules
				(name, description, position, action, direction, protocol,
				 port_from, port_to, source_cidr, interface, enabled, builtin_template,
				 created_at, updated_at)
				VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
				r.Name, r.Description, i, string(r.Action), string(r.Direction), string(r.Protocol),
				portFrom, portTo, sourceCIDR, iface, enabled, r.BuiltinTemplate,
				now, now,
			)
			if err != nil {
				return fmt.Errorf("插入第 %d 条规则失败: %w", i+1, err)
			}

			id, err := res.LastInsertId()
			if err != nil {
				return fmt.Errorf("读取第 %d 条规则 ID 失败: %w", i+1, err)
			}
			// 回填 ID 与规范化后的 position，供调用方使用
			out := r
			out.ID = id
			out.Position = i
			if out.SourceCIDR == "" {
				out.SourceCIDR = "any"
			}
			if out.Interface == "" {
				out.Interface = "any"
			}
			saved.Rules = append(saved.Rules, out)
		}

		if _, err := tx.ExecContext(ctx, `DELETE FROM firewall_deny_interfaces`); err != nil {
			return fmt.Errorf("清空旧接口配置失败: %w", err)
		}
		for _, iface := range rs.DefaultDenyInterfaces {
			if _, err := tx.ExecContext(ctx,
				`INSERT INTO firewall_deny_interfaces (interface, created_at) VALUES (?, ?)`,
				iface, now); err != nil {
				return fmt.Errorf("插入默认拒绝接口 %s 失败: %w", iface, err)
			}
		}
		return nil
	})
	if err != nil {
		return rs, err
	}
	return saved, nil
}

// Revision 是一条规则版本记录。
type Revision struct {
	ID            int64      `json:"id"`
	Status        string     `json:"status"`
	Content       string     `json:"content"`
	ContentHash   string     `json:"contentHash"`
	RuleCount     int        `json:"ruleCount"`
	EnforcedCount int        `json:"enforcedCount"`
	CreatedAt     time.Time  `json:"createdAt"`
	AppliedAt     *time.Time `json:"appliedAt"`
	RollbackAt    *time.Time `json:"rollbackAt"`
	Error         string     `json:"error"`
}

// 版本状态。
const (
	RevisionCandidate  = "candidate"
	RevisionApplied    = "applied"
	RevisionRolledBack = "rolled_back"
	RevisionFailed     = "failed"
)

// SaveRevision 记录一份候选配置。
func (s *Store) SaveRevision(ctx context.Context, content, hash string, ruleCount, enforcedCount int, generatorVersion string) (int64, error) {
	res, err := s.db.SQL().ExecContext(ctx, `
		INSERT INTO firewall_revisions
		(status, content, content_hash, rule_count, created_at, generator_version, enforced_count)
		VALUES (?, ?, ?, ?, ?, ?, ?)`,
		RevisionCandidate, content, hash, ruleCount,
		database.FormatTime(time.Now().UTC()), generatorVersion, enforcedCount)
	if err != nil {
		return 0, fmt.Errorf("保存规则版本失败: %w", err)
	}
	return res.LastInsertId()
}

// MarkRevision 更新版本状态。
func (s *Store) MarkRevision(ctx context.Context, id int64, status, errMsg string) error {
	now := time.Now().UTC()
	var query string
	var args []any

	switch status {
	case RevisionApplied:
		query = `UPDATE firewall_revisions SET status = ?, applied_at = ?, error = ? WHERE id = ?`
		args = []any{status, database.FormatTime(now), errMsg, id}
	case RevisionRolledBack:
		query = `UPDATE firewall_revisions SET status = ?, rollback_at = ?, error = ? WHERE id = ?`
		args = []any{status, database.FormatTime(now), errMsg, id}
	default:
		query = `UPDATE firewall_revisions SET status = ?, error = ? WHERE id = ?`
		args = []any{status, errMsg, id}
	}

	if _, err := s.db.SQL().ExecContext(ctx, query, args...); err != nil {
		return fmt.Errorf("更新版本状态失败: %w", err)
	}
	return nil
}

// LastKnownGood 返回最近一份「已确认可用」的配置内容。
//
// 回滚时优先用它恢复，而不是清空 anchor——
// 让用户在回滚后仍保有上一份经过验证的规则。
// 返回空字符串表示没有历史可用版本。
func (s *Store) LastKnownGood(ctx context.Context, excludeID int64) (string, string, error) {
	var content, hash string
	err := s.db.SQL().QueryRowContext(ctx, `
		SELECT content, content_hash FROM firewall_revisions
		WHERE status = ? AND id != ?
		ORDER BY applied_at DESC, id DESC LIMIT 1`,
		RevisionApplied, excludeID).Scan(&content, &hash)
	if err == sql.ErrNoRows {
		return "", "", nil
	}
	if err != nil {
		return "", "", fmt.Errorf("查询上一份可用配置失败: %w", err)
	}
	return content, hash, nil
}

// ListRevisions 返回最近的版本记录（不含 content，避免响应过大）。
func (s *Store) ListRevisions(ctx context.Context, limit int) ([]Revision, error) {
	if limit <= 0 || limit > 100 {
		limit = 20
	}
	rows, err := s.db.SQL().QueryContext(ctx, `
		SELECT id, status, content_hash, rule_count, enforced_count,
		       created_at, applied_at, rollback_at, COALESCE(error, '')
		FROM firewall_revisions
		ORDER BY id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, fmt.Errorf("查询版本记录失败: %w", err)
	}
	defer rows.Close()

	var out []Revision
	for rows.Next() {
		var (
			r          Revision
			createdAt  string
			appliedAt  sql.NullString
			rollbackAt sql.NullString
		)
		if err := rows.Scan(&r.ID, &r.Status, &r.ContentHash, &r.RuleCount,
			&r.EnforcedCount, &createdAt, &appliedAt, &rollbackAt, &r.Error); err != nil {
			return nil, fmt.Errorf("扫描版本记录失败: %w", err)
		}
		if t, err := database.ParseTime(createdAt); err == nil {
			r.CreatedAt = t
		}
		if appliedAt.Valid {
			if t, err := database.ParseTime(appliedAt.String); err == nil {
				r.AppliedAt = &t
			}
		}
		if rollbackAt.Valid {
			if t, err := database.ParseTime(rollbackAt.String); err == nil {
				r.RollbackAt = &t
			}
		}
		out = append(out, r)
	}
	return out, rows.Err()
}
