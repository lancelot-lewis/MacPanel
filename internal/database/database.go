// Package database 封装 SQLite 连接、迁移与基础访问。
//
// 约定：
//   - 使用 WAL 模式，兼顾并发读与单写。
//   - 迁移按版本号顺序执行，失败即回滚并保留当前版本。
//   - 所有时间以 RFC3339 UTC 文本存储，便于人工排查。
package database

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	_ "modernc.org/sqlite" // 纯 Go SQLite 驱动，无需 cgo
)

// DB 包装 *sql.DB 并记录 schema 版本。
type DB struct {
	sql     *sql.DB
	version int
	path    string
}

// Open 打开（必要时创建）数据库并执行迁移。
func Open(ctx context.Context, path string) (*DB, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return nil, fmt.Errorf("创建数据目录失败: %w", err)
	}

	dsn := fmt.Sprintf("file:%s?_pragma=busy_timeout(5000)&_pragma=foreign_keys(1)", path)
	sqlDB, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("打开数据库失败: %w", err)
	}
	// SQLite 单写者模型：限制连接数以避免 SQLITE_BUSY 抖动。
	sqlDB.SetMaxOpenConns(1)
	sqlDB.SetMaxIdleConns(1)
	sqlDB.SetConnMaxLifetime(0)

	if err := sqlDB.PingContext(ctx); err != nil {
		sqlDB.Close()
		return nil, fmt.Errorf("数据库不可用: %w", err)
	}

	// WAL 需在每次打开后设置；journal_mode 是持久化属性，但仍显式声明。
	for _, pragma := range []string{
		"PRAGMA journal_mode=WAL",
		"PRAGMA synchronous=NORMAL",
		"PRAGMA busy_timeout=5000",
		"PRAGMA foreign_keys=ON",
	} {
		if _, err := sqlDB.ExecContext(ctx, pragma); err != nil {
			sqlDB.Close()
			return nil, fmt.Errorf("设置 %s 失败: %w", pragma, err)
		}
	}

	db := &DB{sql: sqlDB, path: path}
	if err := db.migrate(ctx); err != nil {
		sqlDB.Close()
		return nil, err
	}
	return db, nil
}

// SQL 暴露底层连接供各模块使用。
func (d *DB) SQL() *sql.DB { return d.sql }

// SchemaVersion 返回当前 schema 版本。
func (d *DB) SchemaVersion() int { return d.version }

// Path 返回数据库文件路径。
func (d *DB) Path() string { return d.path }

// Close 关闭数据库。
func (d *DB) Close() error { return d.sql.Close() }

// Now 返回统一时间戳（UTC）。
func Now() time.Time { return time.Now().UTC() }

// FormatTime 把时间格式化为存储用字符串。
func FormatTime(t time.Time) string { return t.UTC().Format(time.RFC3339Nano) }

// ParseTime 解析存储的时间字符串。
func ParseTime(s string) (time.Time, error) {
	return time.Parse(time.RFC3339Nano, s)
}

// Tx 在事务中执行 fn，自动回滚与提交。
func (d *DB) Tx(ctx context.Context, fn func(*sql.Tx) error) error {
	tx, err := d.sql.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("开启事务失败: %w", err)
	}
	if err := fn(tx); err != nil {
		if rbErr := tx.Rollback(); rbErr != nil && !errors.Is(rbErr, sql.ErrTxDone) {
			return fmt.Errorf("%w（回滚也失败: %v）", err, rbErr)
		}
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("提交事务失败: %w", err)
	}
	return nil
}
