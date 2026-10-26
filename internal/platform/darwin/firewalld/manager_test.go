package firewalld

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/lancelot-lewis/MacPanel/internal/model"
)

// fakeApplier 记录所有调用，并可注入失败。
type fakeApplier struct {
	mu sync.Mutex

	// 注入的错误
	syntaxErr error
	loadErr   map[string]error // key: path
	snapshot  string
	snapErr   error

	// 调用记录
	syntaxCalls   []string
	loadCalls     []string
	snapshotCalls int
}

func newFakeApplier() *fakeApplier {
	return &fakeApplier{loadErr: map[string]error{}}
}

func (f *fakeApplier) CheckSyntax(_ context.Context, path string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.syntaxCalls = append(f.syntaxCalls, path)
	return f.syntaxErr
}

func (f *fakeApplier) Load(_ context.Context, anchor, path string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.loadCalls = append(f.loadCalls, anchor+"|"+path)
	if err, ok := f.loadErr[path]; ok {
		return err
	}
	return nil
}

func (f *fakeApplier) SnapshotCurrent(_ context.Context, _ string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.snapshotCalls++
	return f.snapshot, f.snapErr
}

// fakeClock 是可推进的时钟。
type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func newFakeClock() *fakeClock {
	return &fakeClock{t: time.Date(2026, 10, 26, 10, 0, 0, 0, time.UTC)}
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

// newTestManager 构造带 fake 依赖的 Manager。
func newTestManager(t *testing.T) (*Manager, *fakeApplier, *MemoryStore, *fakeClock) {
	t.Helper()
	applier := newFakeApplier()
	store := NewMemoryStore()
	clock := newFakeClock()

	m, err := New(Options{
		Store:          store,
		Applier:        applier,
		AnchorName:     "com.test.ipv6",
		RulePath:       "/tmp/test-anchor",
		ConfirmTimeout: 60 * time.Second,
		Now:            clock.Now,
	})
	if err != nil {
		t.Fatalf("创建 Manager 失败: %v", err)
	}
	return m, applier, store, clock
}

// TestApplyEntersPending 验证应用后进入 pending 并设置截止时间。
func TestApplyEntersPending(t *testing.T) {
	m, applier, _, clock := newTestManager(t)
	ctx := context.Background()

	res, err := m.Apply(ctx, "pass in quick inet6 all", 1, "prevhash")
	if err != nil {
		t.Fatalf("应用失败: %v", err)
	}

	if res.State.Phase != PhasePending {
		t.Errorf("阶段应为 pending，实际 %s", res.State.Phase)
	}
	if res.State.RevisionID != 1 {
		t.Errorf("revision 应为 1，实际 %d", res.State.RevisionID)
	}
	if res.State.ContentHash == "" {
		t.Error("应记录内容哈希")
	}
	// 截止时间应为 当前 + 超时
	want := clock.Now().Add(60 * time.Second)
	if !res.State.Deadline.Equal(want) {
		t.Errorf("截止时间应为 %v，实际 %v", want, res.State.Deadline)
	}

	// 必须先做语法检查，再加载
	if len(applier.syntaxCalls) != 1 {
		t.Errorf("应调用语法检查 1 次，实际 %d", len(applier.syntaxCalls))
	}
	if len(applier.loadCalls) != 1 {
		t.Errorf("应加载 1 次，实际 %d", len(applier.loadCalls))
	}
	if !strings.Contains(applier.loadCalls[0], "/tmp/test-anchor") {
		t.Errorf("加载路径不符: %s", applier.loadCalls[0])
	}
}

// TestApplyAbortsOnSyntaxError 验证语法检查失败时不做任何变更。
//
// 这是「先验证后部署」的核心：语法错误绝不能触达运行状态。
func TestApplyAbortsOnSyntaxError(t *testing.T) {
	m, applier, store, _ := newTestManager(t)
	applier.syntaxErr = errors.New("syntax error on line 5")

	_, err := m.Apply(context.Background(), "bad rules", 1, "")
	if err == nil {
		t.Fatal("语法错误应导致应用失败")
	}
	if code := model.CodeOf(err); code != model.CodeInvalidArgument {
		t.Errorf("错误码应为 invalid_argument，实际 %s", code)
	}

	// 关键断言：不应加载任何规则
	if len(applier.loadCalls) != 0 {
		t.Errorf("语法检查失败后不应加载规则，实际加载 %d 次", len(applier.loadCalls))
	}
	// 也不应写入 pending 状态
	st, _ := store.Load(context.Background())
	if st.Phase == PhasePending {
		t.Error("语法失败后不应进入 pending 状态")
	}
}

// TestConfirmMakesRulePermanent 验证确认后不再有截止时间。
func TestConfirmMakesRulePermanent(t *testing.T) {
	m, _, _, _ := newTestManager(t)
	ctx := context.Background()

	if _, err := m.Apply(ctx, "rules", 1, ""); err != nil {
		t.Fatalf("应用失败: %v", err)
	}
	st, err := m.Confirm(ctx)
	if err != nil {
		t.Fatalf("确认失败: %v", err)
	}
	if st.Phase != PhaseConfirmed {
		t.Errorf("阶段应为 confirmed，实际 %s", st.Phase)
	}
	if !st.Deadline.IsZero() {
		t.Error("确认后应清除截止时间（不再自动回滚）")
	}

	// 已确认状态下 PurgeExpired 不应回滚
	rolledBack, err := m.PurgeExpired(ctx)
	if err != nil {
		t.Fatalf("PurgeExpired 失败: %v", err)
	}
	if rolledBack {
		t.Error("已确认的配置不应被自动回滚")
	}
}

// TestConfirmIsIdempotent 验证重复确认不报错。
func TestConfirmIsIdempotent(t *testing.T) {
	m, _, _, _ := newTestManager(t)
	ctx := context.Background()

	if _, err := m.Apply(ctx, "rules", 1, ""); err != nil {
		t.Fatalf("应用失败: %v", err)
	}
	if _, err := m.Confirm(ctx); err != nil {
		t.Fatalf("首次确认失败: %v", err)
	}
	// 用户刷新页面后又点了一次
	if _, err := m.Confirm(ctx); err != nil {
		t.Fatalf("重复确认应幂等，实际: %v", err)
	}
}

// TestConfirmRejectedAfterDeadline 验证超时后确认被拒绝并触发回滚。
//
// 这条最关键：如果超时后还能确认，「回滚倒计时」就形同虚设。
func TestConfirmRejectedAfterDeadline(t *testing.T) {
	m, applier, _, clock := newTestManager(t)
	ctx := context.Background()

	if _, err := m.Apply(ctx, "rules", 1, ""); err != nil {
		t.Fatalf("应用失败: %v", err)
	}
	// 推进到超过截止时间
	clock.Advance(61 * time.Second)

	_, err := m.Confirm(ctx)
	if err == nil {
		t.Fatal("超时后确认应被拒绝")
	}
	if code := model.CodeOf(err); code != model.CodeConflict {
		t.Errorf("错误码应为 conflict，实际 %s", code)
	}

	// 应已触发回滚：加载 /dev/null 清空 anchor
	var cleared bool
	for _, call := range applier.loadCalls {
		if strings.Contains(call, "/dev/null") {
			cleared = true
		}
	}
	if !cleared {
		t.Errorf("超时确认应触发回滚（加载 /dev/null），实际调用: %v", applier.loadCalls)
	}

	st, _ := m.State(ctx)
	if st.Phase != PhaseRolledBack {
		t.Errorf("阶段应为 rolled_back，实际 %s", st.Phase)
	}
}

// TestPurgeExpiredRollsBack 验证无用户操作时也会自动回滚。
func TestPurgeExpiredRollsBack(t *testing.T) {
	m, applier, _, clock := newTestManager(t)
	ctx := context.Background()

	if _, err := m.Apply(ctx, "rules", 1, ""); err != nil {
		t.Fatalf("应用失败: %v", err)
	}

	// 未超时：不动作
	if rb, _ := m.PurgeExpired(ctx); rb {
		t.Error("未超时不应回滚")
	}

	clock.Advance(59 * time.Second)
	if rb, _ := m.PurgeExpired(ctx); rb {
		t.Error("59 秒时不应回滚")
	}

	clock.Advance(2 * time.Second) // 共 61 秒
	rb, err := m.PurgeExpired(ctx)
	if err != nil {
		t.Fatalf("PurgeExpired 失败: %v", err)
	}
	if !rb {
		t.Fatal("超过 60 秒应自动回滚")
	}

	st, _ := m.State(ctx)
	if st.Phase != PhaseRolledBack {
		t.Errorf("阶段应为 rolled_back，实际 %s", st.Phase)
	}
	if !strings.Contains(st.Reason, "时限") && !strings.Contains(st.Reason, "超时") {
		t.Errorf("回滚原因应说明超时/超时限，实际: %s", st.Reason)
	}
	_ = applier
}

// TestRollbackIsIdempotent 验证重复回滚不报错。
func TestRollbackIsIdempotent(t *testing.T) {
	m, _, _, _ := newTestManager(t)
	ctx := context.Background()

	if _, err := m.Apply(ctx, "rules", 1, ""); err != nil {
		t.Fatalf("应用失败: %v", err)
	}
	if _, err := m.Rollback(ctx, "测试"); err != nil {
		t.Fatalf("首次回滚失败: %v", err)
	}
	if _, err := m.Rollback(ctx, "测试"); err != nil {
		t.Fatalf("重复回滚应幂等，实际: %v", err)
	}
}

// TestRecoverRollsBackExpiredOnStartup 验证 Agent 重启后能回滚超时配置。
//
// 这是「崩溃后可恢复」的核心场景：
// Agent 在 pending 期间崩溃，launchd 重启它，此时配置已超时未确认。
// 若不回滚，机器会永久停在一份没人确认过的规则上。
func TestRecoverRollsBackExpiredOnStartup(t *testing.T) {
	applier := newFakeApplier()
	store := NewMemoryStore()
	clock := newFakeClock()

	newManager := func() *Manager {
		m, err := New(Options{
			Store:          store,
			Applier:        applier,
			AnchorName:     "com.test.ipv6",
			RulePath:       "/tmp/test-anchor",
			ConfirmTimeout: 60 * time.Second,
			Now:            clock.Now,
		})
		if err != nil {
			t.Fatalf("创建 Manager 失败: %v", err)
		}
		return m
	}

	// 第一个进程：应用后崩溃（不确认）
	m1 := newManager()
	if _, err := m1.Apply(context.Background(), "rules", 7, ""); err != nil {
		t.Fatalf("应用失败: %v", err)
	}
	// 模拟崩溃后经过 2 分钟（远超 60 秒）
	clock.Advance(2 * time.Minute)

	// 第二个进程：启动时恢复
	m2 := newManager()
	recovered, err := m2.Recover(context.Background())
	if err != nil {
		t.Fatalf("恢复失败: %v", err)
	}
	if !recovered {
		t.Fatal("应检测到超时配置并回滚")
	}

	st, _ := m2.State(context.Background())
	if st.Phase != PhaseRolledBack {
		t.Errorf("恢复后阶段应为 rolled_back，实际 %s", st.Phase)
	}
	if !strings.Contains(st.Reason, "重启") {
		t.Errorf("原因应说明是重启恢复，实际: %s", st.Reason)
	}
}

// TestRecoverKeepsUnexpiredDeadline 验证未超时时保留倒计时（不重置）。
//
// 关键：如果 Recover 重置了 Deadline，用户关掉浏览器、Agent 重启，
// 就等于无限延长了未确认窗口，「60 秒回滚」不再有意义。
func TestRecoverKeepsUnexpiredDeadline(t *testing.T) {
	applier := newFakeApplier()
	store := NewMemoryStore()
	clock := newFakeClock()

	newManager := func() *Manager {
		m, _ := New(Options{
			Store:          store,
			Applier:        applier,
			AnchorName:     "com.test.ipv6",
			RulePath:       "/tmp/test-anchor",
			ConfirmTimeout: 60 * time.Second,
			Now:            clock.Now,
		})
		return m
	}

	m1 := newManager()
	res, err := m1.Apply(context.Background(), "rules", 1, "")
	if err != nil {
		t.Fatalf("应用失败: %v", err)
	}
	originalDeadline := res.State.Deadline

	// 只过了 20 秒就重启
	clock.Advance(20 * time.Second)

	m2 := newManager()
	recovered, err := m2.Recover(context.Background())
	if err != nil {
		t.Fatalf("恢复失败: %v", err)
	}
	if recovered {
		t.Fatal("未超时不应回滚")
	}

	st, _ := m2.State(context.Background())
	if st.Phase != PhasePending {
		t.Errorf("应保持 pending，实际 %s", st.Phase)
	}
	// 截止时间必须原样保留，不能被重置为「重启时刻 + 60 秒」
	if !st.Deadline.Equal(originalDeadline) {
		t.Errorf("截止时间不应被重置：原 %v，现 %v", originalDeadline, st.Deadline)
	}

	// 再推进 41 秒（共 61 秒），应超时
	clock.Advance(41 * time.Second)
	rb, err := m2.PurgeExpired(context.Background())
	if err != nil {
		t.Fatalf("PurgeExpired 失败: %v", err)
	}
	if !rb {
		t.Error("累计超过 60 秒应回滚（证明倒计时未被重启重置）")
	}
}

// TestApplyRollsBackPendingFirst 验证应用新规则前先回滚未确认的旧配置。
func TestApplyRollsBackPendingFirst(t *testing.T) {
	m, applier, _, _ := newTestManager(t)
	ctx := context.Background()

	if _, err := m.Apply(ctx, "rules-v1", 1, ""); err != nil {
		t.Fatalf("首次应用失败: %v", err)
	}
	// 不确认，直接应用第二份
	res, err := m.Apply(ctx, "rules-v2", 2, "")
	if err != nil {
		t.Fatalf("二次应用失败: %v", err)
	}

	// 应提示「已自动回滚上一份」
	if len(res.Warnings) == 0 {
		t.Error("应提示已回滚上一份未确认配置")
	}

	// 应有一次 /dev/null 加载（回滚），一次新规则加载
	var cleared, loaded int
	for _, call := range applier.loadCalls {
		if strings.Contains(call, "/dev/null") {
			cleared++
		} else {
			loaded++
		}
	}
	if cleared != 1 {
		t.Errorf("应回滚 1 次，实际 %d", cleared)
	}
	if loaded != 2 {
		t.Errorf("应加载 2 次规则（首次 + 二次），实际 %d", loaded)
	}
}

// TestApplyRollsBackOnLoadFailure 验证加载失败时自动回滚。
func TestApplyRollsBackOnLoadFailure(t *testing.T) {
	m, applier, _, _ := newTestManager(t)
	ctx := context.Background()

	applier.loadErr["/tmp/test-anchor"] = errors.New("pfctl: load failed")

	_, err := m.Apply(ctx, "rules", 1, "")
	if err == nil {
		t.Fatal("加载失败应返回错误")
	}
	// 错误信息应提到已回滚，并给出救援命令
	msg := model.AsError(err).Message
	if !strings.Contains(msg, "回滚") {
		t.Errorf("错误信息应说明回滚情况，实际: %s", msg)
	}

	// 应尝试过加载 /dev/null 回滚
	var cleared bool
	for _, call := range applier.loadCalls {
		if strings.Contains(call, "/dev/null") {
			cleared = true
		}
	}
	if !cleared {
		t.Error("加载失败后应自动回滚")
	}
}

// TestApplyRejectsEmptyContent 验证空内容被拒绝。
func TestApplyRejectsEmptyContent(t *testing.T) {
	m, _, _, _ := newTestManager(t)
	if _, err := m.Apply(context.Background(), "", 1, ""); err == nil {
		t.Fatal("空规则内容应被拒绝")
	}
}

// TestConfirmRejectedWhenIdle 验证无事务时确认报错。
func TestConfirmRejectedWhenIdle(t *testing.T) {
	m, _, _, _ := newTestManager(t)
	_, err := m.Confirm(context.Background())
	if err == nil {
		t.Fatal("无待确认配置时应报错")
	}
	if code := model.CodeOf(err); code != model.CodeConflict {
		t.Errorf("错误码应为 conflict，实际 %s", code)
	}
}

// TestRollbackRejectedWhenIdle 验证无事务时回滚报错。
func TestRollbackRejectedWhenIdle(t *testing.T) {
	m, _, _, _ := newTestManager(t)
	_, err := m.Rollback(context.Background(), "x")
	if err == nil {
		t.Fatal("无配置可回滚时应报错")
	}
}

// TestStatePersistedAcrossInstances 验证状态真的落盘（换实例仍可读）。
func TestStatePersistedAcrossInstances(t *testing.T) {
	applier := newFakeApplier()
	store := NewMemoryStore()
	clock := newFakeClock()

	mk := func() *Manager {
		m, _ := New(Options{
			Store: store, Applier: applier,
			AnchorName: "com.test.ipv6", RulePath: "/tmp/test-anchor",
			ConfirmTimeout: 60 * time.Second, Now: clock.Now,
		})
		return m
	}

	m1 := mk()
	if _, err := m1.Apply(context.Background(), "rules", 42, "prev"); err != nil {
		t.Fatalf("应用失败: %v", err)
	}

	// 新实例读同一 store
	m2 := mk()
	st, err := m2.State(context.Background())
	if err != nil {
		t.Fatalf("读取状态失败: %v", err)
	}
	if st.RevisionID != 42 {
		t.Errorf("revision 应保留为 42，实际 %d", st.RevisionID)
	}
	if st.PreviousHash != "prev" {
		t.Errorf("前一版本哈希应保留，实际 %s", st.PreviousHash)
	}
}

// TestNewValidatesOptions 验证构造参数校验。
func TestNewValidatesOptions(t *testing.T) {
	base := Options{
		Store: NewMemoryStore(), Applier: newFakeApplier(),
		AnchorName: "com.test.ipv6", RulePath: "/tmp/x",
	}

	cases := []struct {
		name   string
		mutate func(*Options)
	}{
		{"缺 Store", func(o *Options) { o.Store = nil }},
		{"缺 Applier", func(o *Options) { o.Applier = nil }},
		{"缺 AnchorName", func(o *Options) { o.AnchorName = "" }},
		{"超时过短", func(o *Options) { o.ConfirmTimeout = time.Second }},
		{"超时过长", func(o *Options) { o.ConfirmTimeout = time.Hour }},
	}

	for _, c := range cases {
		o := base
		c.mutate(&o)
		if _, err := New(o); err == nil {
			t.Errorf("%s 应报错", c.name)
		}
	}
}

// TestHashContentStable 验证哈希稳定且区分内容。
func TestHashContentStable(t *testing.T) {
	a := HashContent("pass in quick inet6 all")
	b := HashContent("pass in quick inet6 all")
	c := HashContent("pass in quick inet6 all ")

	if a != b {
		t.Error("相同内容应得到相同哈希")
	}
	if a == c {
		t.Error("不同内容（多一个空格）应得到不同哈希")
	}
	if len(a) != 64 {
		t.Errorf("SHA-256 十六进制应为 64 字符，实际 %d", len(a))
	}
}

// TestUnloadClearsAnchorFromAnyPhase 验证 Unload 不挑当前阶段。
//
// 这是它与 Rollback 的唯一实质区别，也是 CLI 救援依赖的性质：
// 状态文件可能与 PF 实际状态不一致（被删过、被手改过），
// 那时 Rollback 会因「没有事务」拒绝执行，而 Unload 必须仍然可用。
func TestUnloadClearsAnchorFromAnyPhase(t *testing.T) {
	phases := []Phase{PhaseIdle, PhaseConfirmed, PhaseFailed, PhaseRolledBack}

	for _, start := range phases {
		t.Run(string(start), func(t *testing.T) {
			m, applier, store, _ := newTestManager(t)
			ctx := context.Background()

			if err := store.Save(ctx, State{
				Phase:       start,
				RevisionID:  7,
				ContentHash: HashContent("block drop in all"),
				Reason:      "测试起点",
			}); err != nil {
				t.Fatalf("准备状态失败: %v", err)
			}

			st, err := m.Unload(ctx, "救援测试")
			if err != nil {
				t.Fatalf("Unload 失败: %v", err)
			}
			if st.Phase != PhaseIdle {
				t.Errorf("Unload 后应为 idle，实际 %s", st.Phase)
			}
			if st.ContentHash != "" {
				t.Errorf("Unload 后不应保留 ContentHash（anchor 已无内容），实际 %q", st.ContentHash)
			}
			if len(applier.loadCalls) != 1 || applier.loadCalls[0] != "com.test.ipv6|/dev/null" {
				t.Errorf("应恰好加载一次 /dev/null，实际 %v", applier.loadCalls)
			}

			// 再次读取应回到空闲，且修订号保留（便于回溯是哪一版被撤了）
			got, err := m.State(ctx)
			if err != nil {
				t.Fatalf("读取状态失败: %v", err)
			}
			if got.Phase != PhaseIdle || got.RevisionID != 7 {
				t.Errorf("持久化状态不符: phase=%s revision=%d", got.Phase, got.RevisionID)
			}
		})
	}
}

// TestUnloadAlsoWorksOnPending 验证 Unload 能覆盖 pending 事务。
func TestUnloadAlsoWorksOnPending(t *testing.T) {
	m, _, _, _ := newTestManager(t)
	ctx := context.Background()

	if _, err := m.Apply(ctx, "pass in quick inet6 all", 3, ""); err != nil {
		t.Fatalf("准备 pending 状态失败: %v", err)
	}

	st, err := m.Unload(ctx, "救援测试")
	if err != nil {
		t.Fatalf("Unload 失败: %v", err)
	}
	if st.Phase != PhaseIdle {
		t.Errorf("pending 下 Unload 应回到 idle，实际 %s", st.Phase)
	}
}

// TestUnloadKeepsStateOnFailure 验证清空失败时不改写状态。
//
// 失败原因必须让用户看得见；同时状态要保持不变，
// 否则下一次会因为状态已被改写而失去重试的线索。
func TestUnloadKeepsStateOnFailure(t *testing.T) {
	m, applier, _, _ := newTestManager(t)
	ctx := context.Background()

	applier.loadErr["/dev/null"] = errors.New("pfctl 模拟失败")

	if err := m.store.Save(ctx, State{Phase: PhaseConfirmed, RevisionID: 9}); err != nil {
		t.Fatalf("准备状态失败: %v", err)
	}

	if _, err := m.Unload(ctx, "救援测试"); err == nil {
		t.Fatal("加载失败时 Unload 应返回错误")
	}

	st, err := m.State(ctx)
	if err != nil {
		t.Fatalf("读取状态失败: %v", err)
	}
	if st.Phase != PhaseConfirmed || st.RevisionID != 9 {
		t.Errorf("失败后状态不应变化，实际 phase=%s revision=%d", st.Phase, st.RevisionID)
	}
}
