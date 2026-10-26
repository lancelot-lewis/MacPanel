package audit

import (
	"context"
	"io"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lancelot-lewis/MacPanel/internal/database"
)

func newTestStore(t *testing.T) (*Store, *database.DB) {
	t.Helper()
	db, err := database.Open(context.Background(), filepath.Join(t.TempDir(), "audit.db"))
	if err != nil {
		t.Fatalf("打开数据库失败: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	log := slog.New(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelError}))
	return NewStore(db, log), db
}

// TestWriteAndList 验证写入与查询。
func TestWriteAndList(t *testing.T) {
	s, _ := newTestStore(t)
	ctx := context.Background()

	entry := Entry{
		Actor:      "local:127.0.0.1",
		Action:     "firewall.apply",
		Target:     "com.homelab.manager.ipv6",
		Result:     ResultSuccess,
		RemoteAddr: "127.0.0.1",
		RequestID:  "req-1",
		Detail:     map[string]any{"rules": 5},
	}
	if err := s.Write(ctx, entry); err != nil {
		t.Fatalf("写入失败: %v", err)
	}

	items, err := s.List(ctx, 10, "")
	if err != nil {
		t.Fatalf("查询失败: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("应有 1 条记录，实际 %d", len(items))
	}
	got := items[0]
	if got.Actor != entry.Actor || got.Action != entry.Action {
		t.Errorf("字段不符: %+v", got)
	}
	if got.Result != ResultSuccess {
		t.Errorf("结果不符: %s", got.Result)
	}
	if got.Detail == nil || got.Detail["rules"] == nil {
		t.Errorf("明细丢失: %+v", got.Detail)
	}
	if got.At.IsZero() {
		t.Error("时间不应为零值")
	}
}

// TestListFiltersByAction 验证按动作过滤。
func TestListFiltersByAction(t *testing.T) {
	s, _ := newTestStore(t)
	ctx := context.Background()

	for _, action := range []string{"firewall.status", "system.overview", "firewall.status"} {
		if err := s.Write(ctx, Entry{Actor: "test", Action: action, Result: ResultSuccess}); err != nil {
			t.Fatalf("写入失败: %v", err)
		}
	}

	items, err := s.List(ctx, 10, "firewall.status")
	if err != nil {
		t.Fatalf("查询失败: %v", err)
	}
	if len(items) != 2 {
		t.Fatalf("应过滤出 2 条，实际 %d", len(items))
	}
	for _, it := range items {
		if it.Action != "firewall.status" {
			t.Errorf("过滤失效: %s", it.Action)
		}
	}
}

// TestListLimitClamped 验证 limit 边界处理。
func TestListLimitClamped(t *testing.T) {
	s, _ := newTestStore(t)
	ctx := context.Background()

	for i := 0; i < 5; i++ {
		if err := s.Write(ctx, Entry{Actor: "t", Action: "a", Result: ResultSuccess}); err != nil {
			t.Fatalf("写入失败: %v", err)
		}
	}

	// limit <= 0 时回落到默认值 50，不应报错也不应取 0 条。
	items, err := s.List(ctx, 0, "")
	if err != nil {
		t.Fatalf("查询失败: %v", err)
	}
	if len(items) != 5 {
		t.Errorf("应返回全部 5 条，实际 %d", len(items))
	}

	// limit 超上限被压到 500（此处数据只有 5 条，重点是不报错）。
	if _, err := s.List(ctx, 10000, ""); err != nil {
		t.Fatalf("超限 limit 不应报错: %v", err)
	}
}

// TestResultConstraintEnforced 验证非法结果被数据库约束拒绝。
func TestResultConstraintEnforced(t *testing.T) {
	s, _ := newTestStore(t)
	err := s.Write(context.Background(), Entry{
		Actor:  "t",
		Action: "a",
		Result: Result("maybe"),
	})
	if err == nil {
		t.Fatal("非法 result 应被拒绝")
	}
}

// TestBestEffortDoesNotPanic 验证 best-effort 写入失败只记日志。
func TestBestEffortDoesNotPanic(t *testing.T) {
	s, db := newTestStore(t)
	ctx := context.Background()
	// 关闭数据库制造失败
	_ = db.Close()
	// 不应 panic
	s.BestEffort(ctx, Entry{Actor: "t", Action: "a", Result: ResultSuccess})
}

// TestNoSecretsInDetail 验证审计内容不含密码类字段（约定：调用方不得写入）。
func TestNoSecretsInDetail(t *testing.T) {
	s, db := newTestStore(t)
	ctx := context.Background()

	if err := s.Write(ctx, Entry{
		Actor:  "local:127.0.0.1",
		Action: "auth.login",
		Result: ResultFailure,
		Detail: map[string]any{"reason": "invalid_credentials"},
	}); err != nil {
		t.Fatalf("写入失败: %v", err)
	}

	rows, err := db.SQL().QueryContext(ctx, `SELECT COALESCE(detail,''), COALESCE(target,'') FROM audit_logs`)
	if err != nil {
		t.Fatalf("查询失败: %v", err)
	}
	defer rows.Close()
	for rows.Next() {
		var detail, target string
		if err := rows.Scan(&detail, &target); err != nil {
			t.Fatalf("扫描失败: %v", err)
		}
		combined := detail + target
		for _, secret := range []string{"password", "token", "cookie", "secret"} {
			if strings.Contains(strings.ToLower(combined), secret) {
				t.Errorf("审计内容含疑似敏感字段 %q: %s", secret, combined)
			}
		}
	}
}
