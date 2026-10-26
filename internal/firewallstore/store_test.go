package firewallstore

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/lancelot-lewis/MacPanel/internal/database"
	"github.com/lancelot-lewis/MacPanel/internal/model"
)

func newTestStore(t *testing.T) *Store {
	t.Helper()
	db, err := database.Open(context.Background(), filepath.Join(t.TempDir(), "fw.db"))
	if err != nil {
		t.Fatalf("打开数据库失败: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return New(db)
}

func p(v int) *int { return &v }

func sampleRules() model.RuleSet {
	return model.RuleSet{
		Rules: []model.FirewallRule{
			{
				Name: "Caddy", Description: "web", Action: model.ActionPass,
				Direction: model.DirectionIn, Protocol: model.ProtocolTCP,
				PortFrom: p(443), PortTo: p(443), SourceCIDR: "any",
				Interface: "any", Enabled: true, BuiltinTemplate: "caddy",
			},
			{
				Name: "EasyTier", Action: model.ActionPass,
				Direction: model.DirectionIn, Protocol: model.ProtocolUDP,
				PortFrom: p(11010), PortTo: p(11013), SourceCIDR: "2001:db8::/32",
				Interface: "en0", Enabled: false,
			},
		},
		DefaultDenyInterfaces: []string{"en0"},
	}
}

// TestSaveAndLoadRoundTrip 验证往返一致。
func TestSaveAndLoadRoundTrip(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	want := sampleRules()
	if _, err := s.Save(ctx, want); err != nil {
		t.Fatalf("保存失败: %v", err)
	}

	got, err := s.Load(ctx)
	if err != nil {
		t.Fatalf("读取失败: %v", err)
	}

	if len(got.Rules) != len(want.Rules) {
		t.Fatalf("规则数不符：%d != %d", len(got.Rules), len(want.Rules))
	}

	// 第一条
	if got.Rules[0].Name != "Caddy" {
		t.Errorf("名称不符: %s", got.Rules[0].Name)
	}
	if got.Rules[0].PortFrom == nil || *got.Rules[0].PortFrom != 443 {
		t.Errorf("端口不符: %v", got.Rules[0].PortFrom)
	}
	if !got.Rules[0].Enabled {
		t.Error("启用状态应保留")
	}
	if got.Rules[0].BuiltinTemplate != "caddy" {
		t.Errorf("模板来源应保留，实际 %q", got.Rules[0].BuiltinTemplate)
	}

	// 第二条：端口范围 + 禁用 + 指定接口 + CIDR
	if got.Rules[1].PortFrom == nil || *got.Rules[1].PortFrom != 11010 {
		t.Errorf("范围起始不符: %v", got.Rules[1].PortFrom)
	}
	if got.Rules[1].PortTo == nil || *got.Rules[1].PortTo != 11013 {
		t.Errorf("范围结束不符: %v", got.Rules[1].PortTo)
	}
	if got.Rules[1].Enabled {
		t.Error("禁用状态应保留")
	}
	if got.Rules[1].Interface != "en0" {
		t.Errorf("接口不符: %s", got.Rules[1].Interface)
	}
	if got.Rules[1].SourceCIDR != "2001:db8::/32" {
		t.Errorf("CIDR 不符: %s", got.Rules[1].SourceCIDR)
	}

	// 默认拒绝接口
	if len(got.DefaultDenyInterfaces) != 1 || got.DefaultDenyInterfaces[0] != "en0" {
		t.Errorf("默认拒绝接口不符: %v", got.DefaultDenyInterfaces)
	}
}

// TestSaveReplacesExisting 验证保存是整体替换。
func TestSaveReplacesExisting(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	if _, err := s.Save(ctx, sampleRules()); err != nil {
		t.Fatalf("首次保存失败: %v", err)
	}

	// 换成只有一条规则
	next := model.RuleSet{
		Rules: []model.FirewallRule{{
			Name: "OnlyOne", Action: model.ActionPass, Direction: model.DirectionIn,
			Protocol: model.ProtocolTCP, PortFrom: p(22), PortTo: p(22),
			SourceCIDR: "any", Interface: "any", Enabled: true,
		}},
	}
	if _, err := s.Save(ctx, next); err != nil {
		t.Fatalf("二次保存失败: %v", err)
	}

	got, err := s.Load(ctx)
	if err != nil {
		t.Fatalf("读取失败: %v", err)
	}
	if len(got.Rules) != 1 || got.Rules[0].Name != "OnlyOne" {
		t.Errorf("应只剩一条规则，实际 %d 条", len(got.Rules))
	}
	// 旧接口配置也应清空
	if len(got.DefaultDenyInterfaces) != 0 {
		t.Errorf("默认拒绝接口应被清空，实际 %v", got.DefaultDenyInterfaces)
	}
}

// TestSaveRejectsInvalid 验证非法规则集不会被写入。
//
// 关键：宁可拒绝，也不写入一份「无法生成规则」的数据，
// 否则后续读取会持续失败。
func TestSaveRejectsInvalid(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	bad := model.RuleSet{
		Rules: []model.FirewallRule{{
			Name: "坏端口", Action: model.ActionPass, Direction: model.DirectionIn,
			Protocol: model.ProtocolTCP, PortFrom: p(500), PortTo: p(100),
			SourceCIDR: "any", Interface: "any", Enabled: true,
		}},
	}
	if _, err := s.Save(ctx, bad); err == nil {
		t.Fatal("端口倒置应被拒绝")
	}

	// 数据库应保持为空（未被写入半份数据）
	got, err := s.Load(ctx)
	if err != nil {
		t.Fatalf("读取失败: %v", err)
	}
	if len(got.Rules) != 0 {
		t.Errorf("非法数据不应写入，实际有 %d 条", len(got.Rules))
	}
}

// TestSaveIsAtomic 验证失败时不留半份数据。
func TestSaveIsAtomic(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	// 先存一份合法数据
	if _, err := s.Save(ctx, sampleRules()); err != nil {
		t.Fatalf("首次保存失败: %v", err)
	}

	// 再存一份非法数据（应整体失败）
	bad := model.RuleSet{
		Rules: []model.FirewallRule{
			{Name: "好", Action: model.ActionPass, Direction: model.DirectionIn,
				Protocol: model.ProtocolTCP, PortFrom: p(80), PortTo: p(80),
				SourceCIDR: "any", Interface: "any", Enabled: true},
			{Name: "坏", Action: model.ActionPass, Direction: model.DirectionIn,
				Protocol: model.ProtocolTCP, PortFrom: p(70000), PortTo: p(70000),
				SourceCIDR: "any", Interface: "any", Enabled: true},
		},
	}
	if _, err := s.Save(ctx, bad); err == nil {
		t.Fatal("非法规则集应被拒绝")
	}

	// 原有数据应完好（未被删除）
	got, err := s.Load(ctx)
	if err != nil {
		t.Fatalf("读取失败: %v", err)
	}
	if len(got.Rules) != 2 {
		t.Errorf("原有数据应保留 2 条，实际 %d 条", len(got.Rules))
	}
	if got.Rules[0].Name != "Caddy" {
		t.Errorf("原有数据被破坏，第一条是 %q", got.Rules[0].Name)
	}
}

// TestEmptyRuleSet 验证空规则集可正常读写。
func TestEmptyRuleSet(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	if _, err := s.Save(ctx, model.RuleSet{}); err != nil {
		t.Fatalf("保存空规则集失败: %v", err)
	}
	got, err := s.Load(ctx)
	if err != nil {
		t.Fatalf("读取失败: %v", err)
	}
	if len(got.Rules) != 0 {
		t.Errorf("应为空，实际 %d 条", len(got.Rules))
	}
}

// TestRevisionLifecycle 验证版本记录的状态流转。
func TestRevisionLifecycle(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	id, err := s.SaveRevision(ctx, "pass in quick inet6 all", "hash1", 1, 4, "m2")
	if err != nil {
		t.Fatalf("保存版本失败: %v", err)
	}
	if id <= 0 {
		t.Fatalf("应返回自增 ID，实际 %d", id)
	}

	// 标记为已应用
	if err := s.MarkRevision(ctx, id, RevisionApplied, ""); err != nil {
		t.Fatalf("标记已应用失败: %v", err)
	}

	list, err := s.ListRevisions(ctx, 10)
	if err != nil {
		t.Fatalf("列出失败: %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("应有 1 条记录，实际 %d", len(list))
	}
	if list[0].Status != RevisionApplied {
		t.Errorf("状态应为 applied，实际 %s", list[0].Status)
	}
	if list[0].AppliedAt == nil {
		t.Error("应有应用时间")
	}
	if list[0].EnforcedCount != 4 {
		t.Errorf("强制规则数应为 4，实际 %d", list[0].EnforcedCount)
	}

	// 标记为已回滚
	if err := s.MarkRevision(ctx, id, RevisionRolledBack, "超时回滚"); err != nil {
		t.Fatalf("标记回滚失败: %v", err)
	}
	list, _ = s.ListRevisions(ctx, 10)
	if list[0].Status != RevisionRolledBack {
		t.Errorf("状态应为 rolled_back，实际 %s", list[0].Status)
	}
	if list[0].RollbackAt == nil {
		t.Error("应有回滚时间")
	}
	if list[0].Error != "超时回滚" {
		t.Errorf("原因应保留，实际 %q", list[0].Error)
	}
}

// TestLastKnownGood 验证「上一份可用配置」的查找。
func TestLastKnownGood(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	// 没有历史时返回空
	content, hash, err := s.LastKnownGood(ctx, 0)
	if err != nil {
		t.Fatalf("查询失败: %v", err)
	}
	if content != "" || hash != "" {
		t.Error("无历史时应返回空")
	}

	// 建两条：一条已应用，一条候选
	id1, _ := s.SaveRevision(ctx, "content-v1", "hash-v1", 1, 4, "m2")
	if err := s.MarkRevision(ctx, id1, RevisionApplied, ""); err != nil {
		t.Fatalf("标记失败: %v", err)
	}
	_, _ = s.SaveRevision(ctx, "content-v2", "hash-v2", 2, 4, "m2") // 候选，未确认

	// 排除 id1 后应没有其它「已应用」版本
	content, _, err = s.LastKnownGood(ctx, id1)
	if err != nil {
		t.Fatalf("查询失败: %v", err)
	}
	if content != "" {
		t.Errorf("排除当前版本后应无可用历史，实际 %q", content)
	}

	// 不排除时应返回 v1
	content, hash, err = s.LastKnownGood(ctx, 0)
	if err != nil {
		t.Fatalf("查询失败: %v", err)
	}
	if content != "content-v1" || hash != "hash-v1" {
		t.Errorf("应返回 v1，实际 content=%q hash=%q", content, hash)
	}
}

// TestSaveBackfillsIDs 验证保存后返回的规则带有数据库分配的 ID。
//
// 这是一个真实犯过的缺陷：Save 原先不返回 ID，导致生成的 PF 文本里
// 每条规则的注释编号都是 [0]，排查问题时无法把日志对应到具体规则。
func TestSaveBackfillsIDs(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	saved, err := s.Save(ctx, sampleRules())
	if err != nil {
		t.Fatalf("保存失败: %v", err)
	}

	if len(saved.Rules) != 2 {
		t.Fatalf("应返回 2 条规则，实际 %d", len(saved.Rules))
	}
	for i, r := range saved.Rules {
		if r.ID <= 0 {
			t.Errorf("第 %d 条规则的 ID 应大于 0，实际 %d", i+1, r.ID)
		}
		if r.Position != i {
			t.Errorf("第 %d 条规则的 position 应为 %d，实际 %d", i+1, i, r.Position)
		}
	}

	// ID 应互不相同
	if saved.Rules[0].ID == saved.Rules[1].ID {
		t.Errorf("两条规则的 ID 不应相同: %d", saved.Rules[0].ID)
	}

	// 重新读取时 ID 应与保存时一致
	loaded, err := s.Load(ctx)
	if err != nil {
		t.Fatalf("读取失败: %v", err)
	}
	if loaded.Rules[0].ID != saved.Rules[0].ID {
		t.Errorf("重新读取的 ID 不一致：%d != %d", loaded.Rules[0].ID, saved.Rules[0].ID)
	}

	// 空值字段应被规范化为 "any"
	if saved.Rules[1].BuiltinTemplate != "" && saved.Rules[1].SourceCIDR == "" {
		t.Error("SourceCIDR 应被规范化为 any")
	}
}
