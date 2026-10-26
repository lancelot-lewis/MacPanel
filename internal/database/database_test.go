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

	// 期望版本取自迁移列表本身，避免每新增一次迁移就要改一遍测试
	// （那种「改测试让它变绿」的习惯会掩盖真正的迁移缺失）。
	wantVersion := len(migrations)
	if got := db.SchemaVersion(); got != wantVersion {
		t.Fatalf("schema 版本应为 %d，实际 %d", wantVersion, got)
	}

	expected := []string{
		"settings", "firewall_rules", "firewall_revisions",
		"scheduled_tasks", "task_executions", "managed_services",
		"audit_logs", "metric_samples", "auth_sessions", "auth_attempts",
		"schema_migrations",
		// v2 新增
		"firewall_deny_interfaces",
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

// TestMigrateV2AddsColumns 验证 v2 增加的列存在且默认值合理。
func TestMigrateV2AddsColumns(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()

	// firewall_revisions 的两列
	now := FormatTime(Now())
	res, err := db.SQL().ExecContext(ctx,
		`INSERT INTO firewall_revisions (status, content, content_hash, created_at)
		 VALUES ('candidate', 'x', 'h', ?)`, now)
	if err != nil {
		t.Fatalf("插入版本记录失败: %v", err)
	}
	id, _ := res.LastInsertId()

	var genVer string
	var enforced int
	if err := db.SQL().QueryRowContext(ctx,
		`SELECT generator_version, enforced_count FROM firewall_revisions WHERE id = ?`, id).
		Scan(&genVer, &enforced); err != nil {
		t.Fatalf("读取新增列失败: %v", err)
	}
	if genVer != "" {
		t.Errorf("generator_version 默认应为空串，实际 %q", genVer)
	}
	if enforced != 0 {
		t.Errorf("enforced_count 默认应为 0，实际 %d", enforced)
	}

	// firewall_rules 的 builtin_template 列
	if _, err := db.SQL().ExecContext(ctx,
		`INSERT INTO firewall_rules
		 (name, position, action, direction, protocol, port_from, port_to, created_at, updated_at)
		 VALUES ('t', 0, 'pass', 'in', 'tcp', 443, 443, ?, ?)`, now, now); err != nil {
		t.Fatalf("插入规则失败: %v", err)
	}
	var tpl string
	if err := db.SQL().QueryRowContext(ctx,
		`SELECT builtin_template FROM firewall_rules LIMIT 1`).Scan(&tpl); err != nil {
		t.Fatalf("读取 builtin_template 失败: %v", err)
	}
	if tpl != "" {
		t.Errorf("builtin_template 默认应为空串，实际 %q", tpl)
	}
}

// TestDenyInterfacesConstraints 验证默认拒绝接口表的约束。
func TestDenyInterfacesConstraints(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	now := FormatTime(Now())

	// 合法接口
	if _, err := db.SQL().ExecContext(ctx,
		`INSERT INTO firewall_deny_interfaces (interface, created_at) VALUES ('en0', ?)`, now); err != nil {
		t.Fatalf("合法接口应插入成功: %v", err)
	}

	// 主键重复
	if _, err := db.SQL().ExecContext(ctx,
		`INSERT INTO firewall_deny_interfaces (interface, created_at) VALUES ('en0', ?)`, now); err == nil {
		t.Error("重复接口应被主键拒绝")
	}

	// 空接口名
	if _, err := db.SQL().ExecContext(ctx,
		`INSERT INTO firewall_deny_interfaces (interface, created_at) VALUES ('', ?)`, now); err == nil {
		t.Error("空接口名应被 CHECK 拒绝")
	}

	// 过长接口名（>15）
	long := "abcdefghijklmnopqrstuvwxyz"
	if _, err := db.SQL().ExecContext(ctx,
		`INSERT INTO firewall_deny_interfaces (interface, created_at) VALUES (?, ?)`, long, now); err == nil {
		t.Error("过长接口名应被 CHECK 拒绝")
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

// TestMigrateV3AddsTaskColumns 验证 v3 为计划任务增加的列存在且默认值合理。
//
// 重点验证默认值：min_interval_seconds 默认必须是 1800（30 分钟），
// 因为它是防循环重启的安全下限；若默认成 0，任何历史任务都会
// 通过「最短间隔」检查，等于没有保护。
func TestMigrateV3AddsTaskColumns(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()

	now := FormatTime(Now())
	_, err := db.SQL().ExecContext(ctx, `
		INSERT INTO scheduled_tasks (name, kind, schedule, created_at, updated_at)
		VALUES ('历史任务', 'reboot', '每天 04:30', ?, ?)`, now, now)
	if err != nil {
		t.Fatalf("插入任务失败: %v", err)
	}

	var (
		recurrenceJSON string
		catchUp        string
		catchUpWindow  int
		minInterval    int
		failCount      int
		lastError      string
		disabledReason string
		launcherLabel  string
	)
	err = db.SQL().QueryRowContext(ctx, `
		SELECT recurrence_json, catch_up, catch_up_window_seconds,
		       min_interval_seconds, fail_count, last_error,
		       disabled_reason, launcher_label
		FROM scheduled_tasks WHERE name = '历史任务'`).Scan(
		&recurrenceJSON, &catchUp, &catchUpWindow, &minInterval,
		&failCount, &lastError, &disabledReason, &launcherLabel)
	if err != nil {
		t.Fatalf("读取新增列失败: %v", err)
	}

	if recurrenceJSON != "" {
		t.Errorf("历史行的 recurrence_json 应为空串，实际 %q", recurrenceJSON)
	}
	if catchUp != "never" {
		t.Errorf("补执行策略默认应为 never，实际 %q", catchUp)
	}
	if catchUpWindow != 0 {
		t.Errorf("补执行窗口默认应为 0，实际 %d", catchUpWindow)
	}
	if minInterval != 1800 {
		t.Errorf("最短间隔默认应为 1800 秒（防循环重启），实际 %d", minInterval)
	}
	if failCount != 0 || lastError != "" || disabledReason != "" || launcherLabel != "" {
		t.Errorf("其余默认值应全为空/零: %d %q %q %q",
			failCount, lastError, disabledReason, launcherLabel)
	}
}

// TestMigrationsAreOrdered 验证迁移版本连续且升序。
//
// 版本号跳跃会让「按版本升序执行」的逻辑跳过某个迁移而不报错，
// 结果是表结构缺少字段，直到运行时才暴露。
func TestMigrationsAreOrdered(t *testing.T) {
	for i, m := range migrations {
		want := i + 1
		if m.Version != want {
			t.Errorf("第 %d 个迁移的版本应为 %d，实际 %d（%s）",
				i+1, want, m.Version, m.Description)
		}
		if m.Description == "" {
			t.Errorf("迁移 v%d 缺少描述", m.Version)
		}
		if m.Up == nil {
			t.Errorf("迁移 v%d 缺少 Up 函数", m.Version)
		}
	}
}
