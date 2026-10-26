package database

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
)

// openTestDB 在临时目录创建数据库。
func openTestDB(t *testing.T) *DB {
	t.Helper()
	path := filepath.Join(t.TempDir(), "test.db")
	db, err := Open(context.Background(), path)
	if err != nil {
		t.Fatalf("打开数据库失败: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

// TestMigrateCreatesSchema 验证迁移创建了所有预期表。
func TestMigrateCreatesSchema(t *testing.T) {
	db := openTestDB(t)
	if got := db.SchemaVersion(); got != 1 {
		t.Fatalf("schema 版本应为 1，实际 %d", got)
	}

	expected := []string{
		"settings", "firewall_rules", "firewall_revisions",
		"scheduled_tasks", "task_executions", "managed_services",
		"audit_logs", "metric_samples", "auth_sessions", "auth_attempts",
		"schema_migrations",
	}
	for _, table := range expected {
		var name string
		err := db.SQL().QueryRowContext(context.Background(),
			`SELECT name FROM sqlite_master WHERE type='table' AND name = ?`, table).Scan(&name)
		if err != nil {
			t.Errorf("表 %s 未创建: %v", table, err)
		}
	}
}

// TestMigrateIdempotent 验证重复打开不会重复执行迁移。
func TestMigrateIdempotent(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "idempotent.db")

	db1, err := Open(context.Background(), path)
	if err != nil {
		t.Fatalf("首次打开失败: %v", err)
	}
	v1 := db1.SchemaVersion()
	_ = db1.Close()

	db2, err := Open(context.Background(), path)
	if err != nil {
		t.Fatalf("二次打开失败: %v", err)
	}
	defer db2.Close()

	if db2.SchemaVersion() != v1 {
		t.Fatalf("版本发生变化: %d → %d", v1, db2.SchemaVersion())
	}

	var count int
	if err := db2.SQL().QueryRowContext(context.Background(),
		`SELECT COUNT(*) FROM schema_migrations`).Scan(&count); err != nil {
		t.Fatalf("统计迁移记录失败: %v", err)
	}
	if count != v1 {
		t.Fatalf("迁移记录数应等于版本号，实际 %d 条 / 版本 %d", count, v1)
	}
}

// TestWALEnabled 验证启用了 WAL 与 foreign_keys。
func TestWALEnabled(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()

	var mode string
	if err := db.SQL().QueryRowContext(ctx, `PRAGMA journal_mode`).Scan(&mode); err != nil {
		t.Fatalf("读取 journal_mode 失败: %v", err)
	}
	if mode != "wal" {
		t.Errorf("journal_mode 应为 wal，实际 %s", mode)
	}

	var fk int
	if err := db.SQL().QueryRowContext(ctx, `PRAGMA foreign_keys`).Scan(&fk); err != nil {
		t.Fatalf("读取 foreign_keys 失败: %v", err)
	}
	if fk != 1 {
		t.Errorf("foreign_keys 应启用，实际 %d", fk)
	}
}

// TestFirewallRulesConstraints 验证规则表约束真实生效。
func TestFirewallRulesConstraints(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	now := FormatTime(Now())

	insert := func(action, direction, protocol string, from, to any) error {
		_, err := db.SQL().ExecContext(ctx,
			`INSERT INTO firewall_rules
			 (name, position, action, direction, protocol, port_from, port_to, created_at, updated_at)
			 VALUES ('t', 0, ?, ?, ?, ?, ?, ?, ?)`,
			action, direction, protocol, from, to, now, now)
		return err
	}

	// 合法规则
	if err := insert("pass", "in", "tcp", 443, 443); err != nil {
		t.Fatalf("合法规则应插入成功: %v", err)
	}
	// 合法端口范围
	if err := insert("block", "in", "tcp", 11010, 11013); err != nil {
		t.Fatalf("端口范围应插入成功: %v", err)
	}
	// 无端口（ICMPv6）
	if err := insert("pass", "in", "icmp6", nil, nil); err != nil {
		t.Fatalf("无端口的 icmp6 规则应插入成功: %v", err)
	}

	// 非法 action
	if err := insert("drop", "in", "tcp", 1, 1); err == nil {
		t.Error("非法 action 应被约束拒绝")
	}
	// 非法 direction
	if err := insert("pass", "both", "tcp", 1, 1); err == nil {
		t.Error("非法 direction 应被约束拒绝")
	}
	// 端口越界
	if err := insert("pass", "in", "tcp", 0, 1); err == nil {
		t.Error("端口 0 应被约束拒绝")
	}
	if err := insert("pass", "in", "tcp", 1, 70000); err == nil {
		t.Error("端口 70000 应被约束拒绝")
	}
	// 范围倒置
	if err := insert("pass", "in", "tcp", 500, 100); err == nil {
		t.Error("倒置端口范围应被约束拒绝")
	}
}

// TestForeignKeyCascade 验证任务删除时执行记录级联删除。
func TestForeignKeyCascade(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	now := FormatTime(Now())

	res, err := db.SQL().ExecContext(ctx,
		`INSERT INTO scheduled_tasks (name, kind, schedule, created_at, updated_at)
		 VALUES ('重启', 'reboot', '0 4 * * *', ?, ?)`, now, now)
	if err != nil {
		t.Fatalf("插入任务失败: %v", err)
	}
	taskID, _ := res.LastInsertId()

	if _, err := db.SQL().ExecContext(ctx,
		`INSERT INTO task_executions (task_id, started_at, result) VALUES (?, ?, 'success')`,
		taskID, now); err != nil {
		t.Fatalf("插入执行记录失败: %v", err)
	}

	if _, err := db.SQL().ExecContext(ctx,
		`DELETE FROM scheduled_tasks WHERE id = ?`, taskID); err != nil {
		t.Fatalf("删除任务失败: %v", err)
	}

	var count int
	if err := db.SQL().QueryRowContext(ctx,
		`SELECT COUNT(*) FROM task_executions WHERE task_id = ?`, taskID).Scan(&count); err != nil {
		t.Fatalf("统计执行记录失败: %v", err)
	}
	if count != 0 {
		t.Errorf("级联删除失效，剩余 %d 条执行记录", count)
	}
}

// TestTxRollback 验证事务失败时回滚。
func TestTxRollback(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	now := FormatTime(Now())

	err := db.Tx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO settings (key, value, updated_at) VALUES ('k', 'v', ?)`, now); err != nil {
			return err
		}
		return sql.ErrNoRows // 故意失败
	})
	if err == nil {
		t.Fatal("期望事务返回错误")
	}

	var count int
	if err := db.SQL().QueryRowContext(ctx,
		`SELECT COUNT(*) FROM settings WHERE key = 'k'`).Scan(&count); err != nil {
		t.Fatalf("查询失败: %v", err)
	}
	if count != 0 {
		t.Error("事务失败后数据应回滚")
	}
}

// TestAuditLogsResultConstraint 验证审计结果枚举约束。
func TestAuditLogsResultConstraint(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	now := FormatTime(Now())

	_, err := db.SQL().ExecContext(ctx,
		`INSERT INTO audit_logs (at, actor, action, result) VALUES (?, 'a', 'act', 'maybe')`, now)
	if err == nil {
		t.Fatal("非法 result 应被约束拒绝")
	}
}
