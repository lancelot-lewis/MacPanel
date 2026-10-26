package servicestore

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/lancelot-lewis/MacPanel/internal/database"
	"github.com/lancelot-lewis/MacPanel/internal/model"
)

func newTestStore(t *testing.T) *Store {
	t.Helper()
	db, err := database.Open(context.Background(), filepath.Join(t.TempDir(), "svc.db"))
	if err != nil {
		t.Fatalf("打开数据库失败: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return New(db)
}

// sampleService 构造一条合法登记记录。
func sampleService() model.ManagedService {
	return model.ManagedService{
		Label:        "ddns-go",
		Domain:       "system",
		PlistPath:    "/Library/LaunchDaemons/ddns-go.plist",
		DisplayName:  "DDNS 客户端",
		AllowControl: true,
	}
}

// TestUpsertAndGet 验证登记与读取。
func TestUpsertAndGet(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	got, err := s.Upsert(ctx, sampleService())
	if err != nil {
		t.Fatalf("登记失败: %v", err)
	}
	if got.ID <= 0 {
		t.Errorf("应分配 ID，实际 %d", got.ID)
	}
	if !got.AllowControl {
		t.Error("授权状态丢失")
	}

	loaded, err := s.Get(ctx, "ddns-go")
	if err != nil {
		t.Fatalf("读取失败: %v", err)
	}
	if loaded.DisplayName != "DDNS 客户端" {
		t.Errorf("显示名不符: %q", loaded.DisplayName)
	}
	if loaded.PlistPath != "/Library/LaunchDaemons/ddns-go.plist" {
		t.Errorf("路径不符: %q", loaded.PlistPath)
	}
}

// TestUpsertIsIdempotent 验证重复登记是覆盖而不是报冲突。
func TestUpsertIsIdempotent(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	if _, err := s.Upsert(ctx, sampleService()); err != nil {
		t.Fatalf("首次登记失败: %v", err)
	}

	updated := sampleService()
	updated.DisplayName = "改名后"
	updated.AllowControl = false
	if _, err := s.Upsert(ctx, updated); err != nil {
		t.Fatalf("重复登记应成功: %v", err)
	}

	got, _ := s.Get(ctx, "ddns-go")
	if got.DisplayName != "改名后" {
		t.Errorf("应覆盖为新值，实际 %q", got.DisplayName)
	}
	if got.AllowControl {
		t.Error("授权状态应被覆盖为 false")
	}

	// 仍应只有一条
	list, _ := s.List(ctx)
	if len(list) != 1 {
		t.Errorf("不应产生重复记录，实际 %d 条", len(list))
	}
}

// TestAllowedSetExcludesUnauthorized 验证只返回已授权的标签。
//
// 登记但未授权的服务仍要展示（allow_control=0），
// 但不能出现在「可操作」集合里。
func TestAllowedSetExcludesUnauthorized(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	a := sampleService()
	a.Label = "svc-a"
	a.PlistPath = "/Library/LaunchDaemons/svc-a.plist"
	a.AllowControl = true
	if _, err := s.Upsert(ctx, a); err != nil {
		t.Fatalf("登记 a 失败: %v", err)
	}

	b := sampleService()
	b.Label = "svc-b"
	b.PlistPath = "/Library/LaunchDaemons/svc-b.plist"
	b.AllowControl = false
	if _, err := s.Upsert(ctx, b); err != nil {
		t.Fatalf("登记 b 失败: %v", err)
	}

	allowed, err := s.AllowedSet(ctx)
	if err != nil {
		t.Fatalf("查询失败: %v", err)
	}
	if !allowed["svc-a"] {
		t.Error("已授权的 svc-a 应在集合内")
	}
	if allowed["svc-b"] {
		t.Error("未授权的 svc-b 不应在集合内")
	}
}

// TestUpsertRejectsInvalid 验证非法登记被拒绝。
//
// 这些校验是「不提供任意 plist 加载」的基础：
// plist 路径会被用来 bootstrap 服务，而 bootstrap 任意 plist
// 等于以 root 执行其中指定的程序。
func TestUpsertRejectsInvalid(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	cases := []struct {
		name   string
		mutate func(*model.ManagedService)
	}{
		{"空标签", func(m *model.ManagedService) { m.Label = "" }},
		{"标签含斜杠", func(m *model.ManagedService) { m.Label = "a/b" }},
		{"系统关键服务", func(m *model.ManagedService) {
			m.Label = "com.apple.Finder"
			m.PlistPath = "/System/Library/LaunchDaemons/com.apple.Finder.plist"
		}},
		{"本应用自身", func(m *model.ManagedService) {
			m.Label = "com.macpanel.agent"
			m.PlistPath = "/Library/LaunchDaemons/com.macpanel.agent.plist"
		}},
		{"非 system 域", func(m *model.ManagedService) { m.Domain = "gui" }},
		{"路径不在允许目录", func(m *model.ManagedService) {
			m.PlistPath = "/tmp/ddns-go.plist"
		}},
		{"路径含穿越", func(m *model.ManagedService) {
			m.PlistPath = "/Library/LaunchDaemons/../tmp/ddns-go.plist"
		}},
		{"文件名与标签不一致", func(m *model.ManagedService) {
			m.PlistPath = "/Library/LaunchDaemons/other.plist"
		}},
		{"非 plist 后缀", func(m *model.ManagedService) {
			m.PlistPath = "/Library/LaunchDaemons/ddns-go.sh"
		}},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			m := sampleService()
			c.mutate(&m)
			if _, err := s.Upsert(ctx, m); err == nil {
				t.Errorf("应拒绝非法登记: %+v", m)
			}
		})
	}

	// 确认都没有落库
	list, _ := s.List(ctx)
	if len(list) != 0 {
		t.Errorf("非法登记不应落库，实际 %d 条", len(list))
	}
}

// TestDelete 验证取消登记。
func TestDelete(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	if _, err := s.Upsert(ctx, sampleService()); err != nil {
		t.Fatalf("登记失败: %v", err)
	}
	if err := s.Delete(ctx, "ddns-go"); err != nil {
		t.Fatalf("取消登记失败: %v", err)
	}
	if _, err := s.Get(ctx, "ddns-go"); err == nil {
		t.Error("记录应已删除")
	}

	// 幂等性：再删一次应返回未找到而不是 panic
	if err := s.Delete(ctx, "ddns-go"); err == nil {
		t.Error("删除不存在的记录应返回错误")
	}
}

// TestGetNotFound 验证不存在的记录返回明确错误。
func TestGetNotFound(t *testing.T) {
	s := newTestStore(t)
	if _, err := s.Get(context.Background(), "nope"); err == nil {
		t.Error("应返回未找到错误")
	}
}

// TestListOrdersByLabel 验证列表排序稳定。
func TestListOrdersByLabel(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	for _, label := range []string{"zzz", "aaa", "mmm"} {
		m := sampleService()
		m.Label = label
		m.PlistPath = "/Library/LaunchDaemons/" + label + ".plist"
		if _, err := s.Upsert(ctx, m); err != nil {
			t.Fatalf("登记 %s 失败: %v", label, err)
		}
	}

	list, _ := s.List(ctx)
	if len(list) != 3 {
		t.Fatalf("应有 3 条，实际 %d", len(list))
	}
	want := []string{"aaa", "mmm", "zzz"}
	for i, w := range want {
		if list[i].Label != w {
			t.Errorf("第 %d 条应为 %q，实际 %q", i+1, w, list[i].Label)
		}
	}
}
