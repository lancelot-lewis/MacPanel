package database

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

// migration 是一次 schema 升级。Up 必须是幂等可重试的 DDL 集合。
type migration struct {
	Version     int
	Description string
	Up          func(ctx context.Context, tx *sql.Tx) error
}

// migrations 必须按 Version 升序追加，已发布的条目不可修改（只能新增）。
var migrations = []migration{
	{
		Version:     1,
		Description: "初始 schema：设置、防火墙规则、定时任务、审计、指标、会话",
		Up:          migrateV1,
	},
}

const createSchemaMigrations = `
CREATE TABLE IF NOT EXISTS schema_migrations (
	version     INTEGER PRIMARY KEY,
	description TEXT    NOT NULL,
	applied_at  TEXT    NOT NULL
);`

func (d *DB) migrate(ctx context.Context) error {
	if _, err := d.sql.ExecContext(ctx, createSchemaMigrations); err != nil {
		return fmt.Errorf("创建 schema_migrations 表失败: %w", err)
	}

	current, err := d.currentVersion(ctx)
	if err != nil {
		return err
	}

	for _, m := range migrations {
		if m.Version <= current {
			continue
		}
		err := d.Tx(ctx, func(tx *sql.Tx) error {
			if err := m.Up(ctx, tx); err != nil {
				return fmt.Errorf("迁移 v%d（%s）失败: %w", m.Version, m.Description, err)
			}
			_, err := tx.ExecContext(ctx,
				`INSERT INTO schema_migrations (version, description, applied_at) VALUES (?, ?, ?)`,
				m.Version, m.Description, FormatTime(Now()))
			return err
		})
		if err != nil {
			return err
		}
		current = m.Version
	}

	d.version = current
	return nil
}

func (d *DB) currentVersion(ctx context.Context) (int, error) {
	var v sql.NullInt64
	err := d.sql.QueryRowContext(ctx, `SELECT MAX(version) FROM schema_migrations`).Scan(&v)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return 0, nil
		}
		return 0, fmt.Errorf("读取 schema 版本失败: %w", err)
	}
	if !v.Valid {
		return 0, nil
	}
	return int(v.Int64), nil
}

// migrateV1 建立首版全部表。字段语义见 docs/architecture.md「数据模型」。
func migrateV1(ctx context.Context, tx *sql.Tx) error {
	stmts := []string{
		// 应用键值设置
		`CREATE TABLE IF NOT EXISTS settings (
			key        TEXT PRIMARY KEY,
			value      TEXT NOT NULL,
			updated_at TEXT NOT NULL
		)`,

		// 用户编辑的防火墙规则（M2 才写入，此处先建表）
		`CREATE TABLE IF NOT EXISTS firewall_rules (
			id          INTEGER PRIMARY KEY AUTOINCREMENT,
			name        TEXT    NOT NULL,
			description TEXT    NOT NULL DEFAULT '',
			position    INTEGER NOT NULL DEFAULT 0,
			action      TEXT    NOT NULL CHECK (action IN ('pass','block')),
			direction   TEXT    NOT NULL CHECK (direction IN ('in','out')),
			protocol    TEXT    NOT NULL CHECK (protocol IN ('tcp','udp','icmp6','any')),
			port_from   INTEGER,
			port_to     INTEGER,
			source_cidr TEXT    NOT NULL DEFAULT 'any',
			interface   TEXT    NOT NULL DEFAULT 'any',
			enabled     INTEGER NOT NULL DEFAULT 1 CHECK (enabled IN (0,1)),
			created_at  TEXT    NOT NULL,
			updated_at  TEXT    NOT NULL,
			CHECK (port_from IS NULL OR (port_from BETWEEN 1 AND 65535)),
			CHECK (port_to   IS NULL OR (port_to   BETWEEN 1 AND 65535)),
			CHECK (port_from IS NULL OR port_to IS NULL OR port_from <= port_to)
		)`,
		`CREATE INDEX IF NOT EXISTS idx_firewall_rules_position ON firewall_rules(position)`,

		// 规则版本：candidate / applied / rolled_back
		`CREATE TABLE IF NOT EXISTS firewall_revisions (
			id            INTEGER PRIMARY KEY AUTOINCREMENT,
			status        TEXT    NOT NULL CHECK (status IN ('candidate','applied','rolled_back','failed')),
			content       TEXT    NOT NULL,
			content_hash  TEXT    NOT NULL,
			rule_count    INTEGER NOT NULL DEFAULT 0,
			created_at    TEXT    NOT NULL,
			applied_at    TEXT,
			rollback_at   TEXT,
			error         TEXT
		)`,
		`CREATE INDEX IF NOT EXISTS idx_firewall_revisions_status ON firewall_revisions(status, created_at DESC)`,

		// 定时任务与执行记录（M3）
		`CREATE TABLE IF NOT EXISTS scheduled_tasks (
			id            INTEGER PRIMARY KEY AUTOINCREMENT,
			name          TEXT    NOT NULL,
			kind          TEXT    NOT NULL,
			schedule      TEXT    NOT NULL,
			timezone      TEXT    NOT NULL DEFAULT 'Asia/Shanghai',
			enabled       INTEGER NOT NULL DEFAULT 1 CHECK (enabled IN (0,1)),
			last_run_at   TEXT,
			last_result   TEXT,
			next_run_at   TEXT,
			created_at    TEXT    NOT NULL,
			updated_at    TEXT    NOT NULL
		)`,

		`CREATE TABLE IF NOT EXISTS task_executions (
			id          INTEGER PRIMARY KEY AUTOINCREMENT,
			task_id     INTEGER NOT NULL REFERENCES scheduled_tasks(id) ON DELETE CASCADE,
			started_at  TEXT    NOT NULL,
			finished_at TEXT,
			result      TEXT    NOT NULL CHECK (result IN ('running','success','failed','skipped')),
			detail      TEXT
		)`,
		`CREATE INDEX IF NOT EXISTS idx_task_executions_task ON task_executions(task_id, started_at DESC)`,

		// 受管服务白名单（M4）：只允许对这里登记的服务操作
		`CREATE TABLE IF NOT EXISTS managed_services (
			id            INTEGER PRIMARY KEY AUTOINCREMENT,
			label         TEXT    NOT NULL UNIQUE,
			domain        TEXT    NOT NULL CHECK (domain IN ('system','user')),
			plist_path    TEXT    NOT NULL,
			display_name  TEXT    NOT NULL DEFAULT '',
			allow_control INTEGER NOT NULL DEFAULT 0 CHECK (allow_control IN (0,1)),
			created_at    TEXT    NOT NULL,
			updated_at    TEXT    NOT NULL
		)`,

		// 审计日志：高风险操作必须留痕
		`CREATE TABLE IF NOT EXISTS audit_logs (
			id          INTEGER PRIMARY KEY AUTOINCREMENT,
			at          TEXT    NOT NULL,
			actor       TEXT    NOT NULL,
			action      TEXT    NOT NULL,
			target      TEXT    NOT NULL DEFAULT '',
			result      TEXT    NOT NULL CHECK (result IN ('success','failure','denied')),
			remote_addr TEXT    NOT NULL DEFAULT '',
			request_id  TEXT    NOT NULL DEFAULT '',
			detail      TEXT
		)`,
		`CREATE INDEX IF NOT EXISTS idx_audit_logs_at ON audit_logs(at DESC)`,
		`CREATE INDEX IF NOT EXISTS idx_audit_logs_action ON audit_logs(action, at DESC)`,

		// 指标采样
		`CREATE TABLE IF NOT EXISTS metric_samples (
			id          INTEGER PRIMARY KEY AUTOINCREMENT,
			at          TEXT    NOT NULL,
			metric      TEXT    NOT NULL,
			value       REAL    NOT NULL,
			labels      TEXT    NOT NULL DEFAULT ''
		)`,
		`CREATE INDEX IF NOT EXISTS idx_metric_samples_metric ON metric_samples(metric, at DESC)`,

		// 认证会话（远程访问）
		`CREATE TABLE IF NOT EXISTS auth_sessions (
			id            TEXT PRIMARY KEY,
			token_hash    TEXT    NOT NULL,
			created_at    TEXT    NOT NULL,
			last_seen_at  TEXT    NOT NULL,
			expires_at    TEXT    NOT NULL,
			remote_addr   TEXT    NOT NULL DEFAULT '',
			user_agent    TEXT    NOT NULL DEFAULT '',
			revoked_at    TEXT
		)`,
		`CREATE INDEX IF NOT EXISTS idx_auth_sessions_expires ON auth_sessions(expires_at)`,

		// 登录失败记录，用于限速
		`CREATE TABLE IF NOT EXISTS auth_attempts (
			id          INTEGER PRIMARY KEY AUTOINCREMENT,
			at          TEXT    NOT NULL,
			remote_addr TEXT    NOT NULL,
			success     INTEGER NOT NULL CHECK (success IN (0,1))
		)`,
		`CREATE INDEX IF NOT EXISTS idx_auth_attempts_addr ON auth_attempts(remote_addr, at DESC)`,
	}

	for _, s := range stmts {
		if _, err := tx.ExecContext(ctx, s); err != nil {
			return fmt.Errorf("执行 DDL 失败: %w\nSQL: %s", err, s)
		}
	}
	return nil
}
