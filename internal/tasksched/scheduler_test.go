package tasksched

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/lancelot-lewis/MacPanel/internal/audit"
	"github.com/lancelot-lewis/MacPanel/internal/database"
	"github.com/lancelot-lewis/MacPanel/internal/model"
	"github.com/lancelot-lewis/MacPanel/internal/platform/darwin/launchd"
	"github.com/lancelot-lewis/MacPanel/internal/platform/fake"
	"github.com/lancelot-lewis/MacPanel/internal/taskstore"
)

// fakeInstaller 是可控的 launchd 安装器。
//
// 用真实临时目录而不是内存 map：Sync 会读磁盘文件做内容哈希比较，
// 纯内存实现会让「内容未变时不重装」这条逻辑永远测不到。
type fakeInstaller struct {
	mu     sync.Mutex
	dir    string
	loaded map[string]bool
	calls  []string

	failInstall bool
	failRemove  bool
}

func newFakeInstaller(t *testing.T) *fakeInstaller {
	t.Helper()
	return &fakeInstaller{dir: t.TempDir(), loaded: map[string]bool{}}
}

func (f *fakeInstaller) path(label string) string {
	return filepath.Join(f.dir, label+".plist")
}

func (f *fakeInstaller) Install(ctx context.Context, spec launchd.Spec) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, "install:"+spec.Label)
	if f.failInstall {
		return fmt.Errorf("模拟安装失败")
	}
	data, err := launchd.RenderPlist(spec)
	if err != nil {
		return err
	}
	if err := os.WriteFile(f.path(spec.Label), data, 0o644); err != nil {
		return err
	}
	f.loaded[spec.Label] = true
	return nil
}

func (f *fakeInstaller) Remove(ctx context.Context, label string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, "remove:"+label)
	if f.failRemove {
		return fmt.Errorf("模拟卸载失败")
	}
	_ = os.Remove(f.path(label))
	delete(f.loaded, label)
	return nil
}

func (f *fakeInstaller) State(ctx context.Context, label string) (launchd.InstallState, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	path := f.path(label)
	st := launchd.InstallState{PlistPath: path, Loaded: f.loaded[label]}
	if _, err := os.Stat(path); err == nil {
		st.PlistExists = true
	}
	return st, nil
}

func (f *fakeInstaller) ListManaged() ([]string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	entries, err := os.ReadDir(f.dir)
	if err != nil {
		return nil, nil
	}
	out := make([]string, 0, len(entries))
	for _, e := range entries {
		label := strings.TrimSuffix(e.Name(), ".plist")
		if strings.HasPrefix(label, launchd.LabelPrefix) {
			out = append(out, label)
		}
	}
	return out, nil
}

func (f *fakeInstaller) has(label string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	_, err := os.Stat(f.path(label))
	return err == nil
}

func (f *fakeInstaller) callCount(prefix string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, c := range f.calls {
		if len(c) >= len(prefix) && c[:len(prefix)] == prefix {
			n++
		}
	}
	return n
}

// fakeAuditor 收集审计记录。
type fakeAuditor struct {
	mu      sync.Mutex
	entries []audit.Entry
}

func (a *fakeAuditor) Write(ctx context.Context, e audit.Entry) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.entries = append(a.entries, e)
	return nil
}

func (a *fakeAuditor) actions() []string {
	a.mu.Lock()
	defer a.mu.Unlock()
	out := make([]string, 0, len(a.entries))
	for _, e := range a.entries {
		out = append(out, e.Action)
	}
	return out
}

func (a *fakeAuditor) hasAction(action string) bool {
	for _, got := range a.actions() {
		if got == action {
			return true
		}
	}
	return false
}

// testEnv 汇总调度器与依赖，便于断言。
type testEnv struct {
	sched     *Scheduler
	store     *taskstore.Store
	installer *fakeInstaller
	power     *fake.Power
	auditor   *fakeAuditor

	mu  sync.Mutex
	now time.Time

	// reboots 记录实际执行的重启调用。
	reboots []int
}

func newTestEnv(t *testing.T) *testEnv {
	t.Helper()

	db, err := database.Open(context.Background(), filepath.Join(t.TempDir(), "sched.db"))
	if err != nil {
		t.Fatalf("打开数据库失败: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	env := &testEnv{
		store:     taskstore.New(db),
		installer: newFakeInstaller(t),
		power:     fake.NewPower(),
		auditor:   &fakeAuditor{},
		now:       time.Date(2026, 10, 26, 12, 0, 0, 0, mustLoc(t)),
	}

	// 让仓储层与调度器共用同一个可控时钟：否则「12 分钟前执行过」
	// 这类断言会依赖真实运行时刻，出现随运行时间漂移的间歇性失败。
	env.store.SetNowForTest(env.currentTime)

	sched, err := New(Options{
		Store:        env.store,
		Installer:    env.installer,
		Power:        env.power,
		Auditor:      env.auditor,
		BinPath:      "/usr/local/bin/homelab-manager",
		LogDir:       "/tmp/hlm-test-logs",
		Now:          env.currentTime,
		TickInterval: time.Hour, // 测试中不真的跑 ticker
	})
	if err != nil {
		t.Fatalf("创建调度器失败: %v", err)
	}

	env.sched = sched

	// 用假执行器记录重启调用，绝不真的执行 shutdown。
	sched.SetRebootExecutorForTest(func(ctx context.Context, delay int) error {
		env.mu.Lock()
		defer env.mu.Unlock()
		env.reboots = append(env.reboots, delay)
		return nil
	})

	t.Cleanup(sched.Stop)
	return env
}

func mustLoc(t *testing.T) *time.Location {
	t.Helper()
	loc, err := time.LoadLocation("Asia/Shanghai")
	if err != nil {
		t.Fatalf("加载时区失败: %v", err)
	}
	return loc
}

func (e *testEnv) currentTime() time.Time {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.now
}

func (e *testEnv) advance(d time.Duration) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.now = e.now.Add(d)
}

func (e *testEnv) rebootCount() int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return len(e.reboots)
}

// dailyTask 构造一个每日 04:30 的重启任务。
func dailyTask(name string) model.ScheduledTask {
	return model.ScheduledTask{
		Name:               name,
		Kind:               model.TaskKindReboot,
		Recurrence:         model.Recurrence{Kind: model.RecurDaily, TimeOfDay: "04:30"},
		Timezone:           "Asia/Shanghai",
		Enabled:            true,
		CatchUp:            model.CatchUpNever,
		MinIntervalSeconds: int(model.DefaultMinRebootInterval.Seconds()),
	}
}

// TestSyncInstallsEnabledTask 验证启用任务被安装到 launchd。
func TestSyncInstallsEnabledTask(t *testing.T) {
	env := newTestEnv(t)
	ctx := context.Background()

	task := dailyTask("每日重启")
	created, err := env.store.Create(ctx, task)
	if err != nil {
		t.Fatalf("创建任务失败: %v", err)
	}

	res, err := env.sched.Sync(ctx)
	if err != nil {
		t.Fatalf("同步失败: %v", err)
	}

	label := launcherLabel(created.ID)
	if !env.installer.has(label) {
		t.Errorf("应按 label %s 安装 plist", label)
	}
	if len(res.Installed) != 1 {
		t.Errorf("应报告 1 个安装，实际 %v", res.Installed)
	}
	if len(res.Errors) != 0 {
		t.Errorf("不应有错误: %v", res.Errors)
	}
}

// TestSyncSkipsDisabledTask 验证禁用任务不安装。
func TestSyncSkipsDisabledTask(t *testing.T) {
	env := newTestEnv(t)
	ctx := context.Background()

	task := dailyTask("禁用的任务")
	task.Enabled = false
	created, err := env.store.Create(ctx, task)
	if err != nil {
		t.Fatalf("创建任务失败: %v", err)
	}

	if _, err := env.sched.Sync(ctx); err != nil {
		t.Fatalf("同步失败: %v", err)
	}

	if env.installer.has(launcherLabel(created.ID)) {
		t.Error("禁用任务不应安装 plist")
	}
}

// TestSyncIsIdempotent 验证重复同步不会反复重装。
//
// 每次都重装会 bootout + bootstrap，短暂中断任务加载状态；
// 更重要的是会掩盖「内容没变」这个事实，让排查变难。
func TestSyncIsIdempotent(t *testing.T) {
	env := newTestEnv(t)
	ctx := context.Background()

	task := dailyTask("每日重启")
	if _, err := env.store.Create(ctx, task); err != nil {
		t.Fatalf("创建任务失败: %v", err)
	}

	if _, err := env.sched.Sync(ctx); err != nil {
		t.Fatalf("首次同步失败: %v", err)
	}
	firstCount := env.installer.callCount("install:")

	res, err := env.sched.Sync(ctx)
	if err != nil {
		t.Fatalf("二次同步失败: %v", err)
	}

	if got := env.installer.callCount("install:"); got != firstCount {
		t.Errorf("内容未变时不应重装：首次 %d 次，二次累计 %d 次", firstCount, got)
	}
	if len(res.Unchanged) != 1 {
		t.Errorf("应报告 1 个未变更，实际 %v", res.Unchanged)
	}
}

// TestSyncRemovesOrphanPlist 验证孤儿 plist 被清理。
//
// 这是最关键的一条：任务删除后若 plist 残留，launchd 仍会按原计划
// 执行重启，表现为「任务删了但机器还在重启」。
func TestSyncRemovesOrphanPlist(t *testing.T) {
	env := newTestEnv(t)
	ctx := context.Background()

	// 模拟一个残留的 plist（数据库里没有对应任务）
	orphan := launcherLabel(999)
	if err := env.installer.Install(ctx, launchd.Spec{
		Label:                 orphan,
		ProgramArguments:      []string{"/usr/local/bin/homelab-manager", "task-run", "--task", "999"},
		StartCalendarInterval: []launchd.CalendarInterval{{Hour: launchd.IntPtr(4), Minute: launchd.IntPtr(30)}},
	}); err != nil {
		t.Fatalf("准备孤儿 plist 失败: %v", err)
	}

	res, err := env.sched.Sync(ctx)
	if err != nil {
		t.Fatalf("同步失败: %v", err)
	}

	if env.installer.has(orphan) {
		t.Error("孤儿 plist 应被清理，否则仍会按计划执行重启")
	}
	if len(res.Removed) != 1 {
		t.Errorf("应报告 1 个移除，实际 %v", res.Removed)
	}
}

// TestSyncKeepsTaskAfterDeleteFailure 验证删除失败时不会误删正常任务。
func TestSyncKeepsTaskAfterDeleteFailure(t *testing.T) {
	env := newTestEnv(t)
	ctx := context.Background()

	created, err := env.store.Create(ctx, dailyTask("正常任务"))
	if err != nil {
		t.Fatalf("创建任务失败: %v", err)
	}
	if _, err := env.sched.Sync(ctx); err != nil {
		t.Fatalf("同步失败: %v", err)
	}

	env.installer.failRemove = true
	if _, err := env.sched.Sync(ctx); err != nil {
		t.Fatalf("同步失败: %v", err)
	}

	if !env.installer.has(launcherLabel(created.ID)) {
		t.Error("正常任务的 plist 不应被清理")
	}
}

// TestSyncReportsInstallFailure 验证安装失败被报告而不是静默。
func TestSyncReportsInstallFailure(t *testing.T) {
	env := newTestEnv(t)
	ctx := context.Background()

	if _, err := env.store.Create(ctx, dailyTask("任务")); err != nil {
		t.Fatalf("创建任务失败: %v", err)
	}

	env.installer.failInstall = true
	res, err := env.sched.Sync(ctx)
	if err != nil {
		t.Fatalf("同步本身不应返回错误（错误应体现在结果里）: %v", err)
	}
	if len(res.Errors) == 0 {
		t.Error("安装失败应体现在 Errors 中，不能静默")
	}
}

// TestSyncUpdatesNextRunAt 验证同步会回填下次执行时间。
func TestSyncUpdatesNextRunAt(t *testing.T) {
	env := newTestEnv(t)
	ctx := context.Background()

	created, err := env.store.Create(ctx, dailyTask("每日重启"))
	if err != nil {
		t.Fatalf("创建任务失败: %v", err)
	}
	if _, err := env.sched.Sync(ctx); err != nil {
		t.Fatalf("同步失败: %v", err)
	}

	got, err := env.store.Get(ctx, created.ID)
	if err != nil {
		t.Fatalf("读取失败: %v", err)
	}
	if got.NextRunAt == nil {
		t.Fatal("应回填下次执行时间")
	}
	// 当前是 12:00，已过 04:30 → 下次应为次日 04:30
	want := time.Date(2026, 10, 27, 4, 30, 0, 0, mustLoc(t))
	if !got.NextRunAt.Equal(want) {
		t.Errorf("下次执行时间应为 %s，实际 %s", want, got.NextRunAt)
	}
}

// TestScheduleRebootPersists 验证排定重启先落盘。
func TestScheduleRebootPersists(t *testing.T) {
	env := newTestEnv(t)
	ctx := context.Background()

	st, err := env.sched.ScheduleReboot(ctx, ScheduleRequest{
		DelaySeconds: 600,
		Reason:       "手动测试",
		Actor:        "han@local",
	})
	if err != nil {
		t.Fatalf("排定失败: %v", err)
	}
	if st.Pending == nil {
		t.Fatal("状态应包含待执行重启")
	}

	// 直接查库确认已落盘（不依赖内存状态）
	saved, err := env.store.LoadPendingReboot(ctx)
	if err != nil {
		t.Fatalf("读取失败: %v", err)
	}
	if saved == nil {
		t.Fatal("待执行重启必须落盘，否则 Agent 重启后会静默消失")
	}
	if saved.Reason != "手动测试" {
		t.Errorf("原因未保存，实际 %q", saved.Reason)
	}

	// 审计已写
	if !env.auditor.hasAction("power.reboot.schedule") {
		t.Error("排定重启应写审计")
	}
}

// TestScheduleRebootRejectsSecond 验证不能叠加待执行重启。
func TestScheduleRebootRejectsSecond(t *testing.T) {
	env := newTestEnv(t)
	ctx := context.Background()

	if _, err := env.sched.ScheduleReboot(ctx, ScheduleRequest{
		DelaySeconds: 600, Reason: "第一次", Actor: "a",
	}); err != nil {
		t.Fatalf("首次排定失败: %v", err)
	}

	_, err := env.sched.ScheduleReboot(ctx, ScheduleRequest{
		DelaySeconds: 900, Reason: "第二次", Actor: "a",
	})
	if err == nil {
		t.Fatal("已有待执行重启时应拒绝")
	}
	if code := model.CodeOf(err); code != model.CodeConflict {
		t.Errorf("应为冲突错误，实际 %q", code)
	}
}

// TestScheduleRebootRejectsBadDelay 验证延后时间边界。
func TestScheduleRebootRejectsBadDelay(t *testing.T) {
	env := newTestEnv(t)
	ctx := context.Background()

	if _, err := env.sched.ScheduleReboot(ctx, ScheduleRequest{DelaySeconds: -1, Actor: "a"}); err == nil {
		t.Error("负数应被拒绝")
	}
	if _, err := env.sched.ScheduleReboot(ctx, ScheduleRequest{DelaySeconds: 4000, Actor: "a"}); err == nil {
		t.Error("超过 60 分钟应被拒绝")
	}
}

// TestScheduleRebootRejectsWithoutCapability 验证非 root 时明确拒绝。
//
// 不能返回「已排定但不会生效」：用户会以为重启已安排，结果什么都不会发生。
func TestScheduleRebootRejectsWithoutCapability(t *testing.T) {
	env := newTestEnv(t)
	ctx := context.Background()
	env.power.CanReboot = false

	_, err := env.sched.ScheduleReboot(ctx, ScheduleRequest{DelaySeconds: 0, Actor: "a"})
	if err == nil {
		t.Fatal("不具备能力时应拒绝")
	}
	if code := model.CodeOf(err); code != model.CodeUnavailable {
		t.Errorf("应为不可用错误，实际 %q", code)
	}
}

// TestCancelRebootIsIdempotent 验证取消幂等。
func TestCancelRebootIsIdempotent(t *testing.T) {
	env := newTestEnv(t)
	ctx := context.Background()

	if _, err := env.sched.ScheduleReboot(ctx, ScheduleRequest{
		DelaySeconds: 600, Reason: "测试", Actor: "a",
	}); err != nil {
		t.Fatalf("排定失败: %v", err)
	}

	if _, err := env.sched.CancelReboot(ctx, "a", "", ""); err != nil {
		t.Fatalf("取消失败: %v", err)
	}

	// 再取消一次（已经没有待执行）
	if _, err := env.sched.CancelReboot(ctx, "a", "", ""); err != nil {
		t.Errorf("重复取消不应报错: %v", err)
	}

	saved, _ := env.store.LoadPendingReboot(ctx)
	if saved != nil {
		t.Error("取消后不应还有待执行重启")
	}
}

// TestRestoreClearsCompletedReboot 验证系统已重启过时清除记录。
//
// 这是防「重启后再重启」的关键判断：pending 是重启前设的，
// 系统已经重启过了，说明意图已达成，不能再执行一次。
func TestRestoreClearsCompletedReboot(t *testing.T) {
	env := newTestEnv(t)
	ctx := context.Background()

	now := env.currentTime()
	// 排定在 10 分钟前，但系统启动时间在排定之后（说明已重启过）
	pending := model.PendingReboot{
		ScheduledAt: now.Add(-10 * time.Minute),
		Reason:      "重启前设置",
		RequestedBy: "a",
		CreatedAt:   now.Add(-20 * time.Minute),
	}
	if err := env.store.SavePendingReboot(ctx, pending); err != nil {
		t.Fatalf("准备数据失败: %v", err)
	}
	env.power.BootAt = now.Add(-5 * time.Minute) // 5 分钟前刚启动

	if err := env.sched.restorePendingReboot(ctx); err != nil {
		t.Fatalf("恢复失败: %v", err)
	}

	saved, _ := env.store.LoadPendingReboot(ctx)
	if saved != nil {
		t.Error("系统已重启过时应清除待执行记录，否则会再次重启")
	}
	if !env.auditor.hasAction("power.reboot.completed") {
		t.Error("应记录「已生效」审计")
	}
	if env.rebootCount() != 0 {
		t.Error("不应再次执行重启")
	}
}

// TestRestoreKeepsFutureDeadline 验证未来时刻保留原计划（不重置倒计时）。
func TestRestoreKeepsFutureDeadline(t *testing.T) {
	env := newTestEnv(t)
	ctx := context.Background()

	now := env.currentTime()
	pending := model.PendingReboot{
		ScheduledAt: now.Add(20 * time.Minute),
		Reason:      "延迟重启",
		RequestedBy: "a",
		CreatedAt:   now.Add(-10 * time.Minute),
	}
	if err := env.store.SavePendingReboot(ctx, pending); err != nil {
		t.Fatalf("准备数据失败: %v", err)
	}
	// 系统启动在排定之前（未重启过）
	env.power.BootAt = now.Add(-48 * time.Hour)

	if err := env.sched.restorePendingReboot(ctx); err != nil {
		t.Fatalf("恢复失败: %v", err)
	}

	st, err := env.sched.Status(ctx)
	if err != nil {
		t.Fatalf("查询状态失败: %v", err)
	}
	if st.Pending == nil {
		t.Fatal("应恢复待执行重启")
	}
	if !st.Pending.ScheduledAt.Equal(pending.ScheduledAt) {
		t.Errorf("计划时刻不应被重置：%s != %s", st.Pending.ScheduledAt, pending.ScheduledAt)
	}
}

// TestRestoreFiresExpiredReboot 验证过期重启立即执行。
func TestRestoreFiresExpiredReboot(t *testing.T) {
	env := newTestEnv(t)
	ctx := context.Background()

	now := env.currentTime()
	pending := model.PendingReboot{
		ScheduledAt: now.Add(-5 * time.Minute),
		Reason:      "崩溃期间错过",
		RequestedBy: "a",
		CreatedAt:   now.Add(-35 * time.Minute),
	}
	if err := env.store.SavePendingReboot(ctx, pending); err != nil {
		t.Fatalf("准备数据失败: %v", err)
	}
	// 启动时间在设置之前（说明这段时间没重启过）
	env.power.BootAt = now.Add(-48 * time.Hour)

	if err := env.sched.restorePendingReboot(ctx); err != nil {
		t.Fatalf("恢复失败: %v", err)
	}

	// firePending 在 goroutine 里执行，等它完成
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if env.rebootCount() > 0 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	if env.rebootCount() == 0 {
		t.Error("过期的待执行重启应被立即执行（这是用户明确的一次性意图）")
	}
}

// TestRunTaskExecutesAndRecords 验证任务执行与记录。
func TestRunTaskExecutesAndRecords(t *testing.T) {
	env := newTestEnv(t)
	ctx := context.Background()

	created, err := env.store.Create(ctx, dailyTask("每日重启"))
	if err != nil {
		t.Fatalf("创建任务失败: %v", err)
	}

	exec, err := env.sched.RunTask(ctx, created.ID)
	if err != nil {
		t.Fatalf("执行失败: %v", err)
	}
	if exec.Result != model.ExecutionSuccess {
		t.Errorf("应执行成功，实际 %q（%s）", exec.Result, exec.Detail)
	}
	if env.rebootCount() != 1 {
		t.Errorf("应调用一次重启，实际 %d", env.rebootCount())
	}

	// 执行记录已写入
	execs, err := env.store.ListExecutions(ctx, created.ID, 10)
	if err != nil {
		t.Fatalf("查询执行记录失败: %v", err)
	}
	if len(execs) != 1 || execs[0].Result != model.ExecutionSuccess {
		t.Errorf("应有 1 条成功记录，实际 %+v", execs)
	}

	// last_run_at 已更新
	task, _ := env.store.Get(ctx, created.ID)
	if task.LastRunAt == nil {
		t.Error("应更新 last_run_at")
	}
	if task.LastResult != model.ExecutionSuccess {
		t.Errorf("最近结果应为 success，实际 %q", task.LastResult)
	}
}

// TestRunTaskSkipsWithinMinInterval 验证间隔不足时跳过且不重启。
func TestRunTaskSkipsWithinMinInterval(t *testing.T) {
	env := newTestEnv(t)
	ctx := context.Background()

	created, err := env.store.Create(ctx, dailyTask("每日重启"))
	if err != nil {
		t.Fatalf("创建任务失败: %v", err)
	}

	// 12 分钟前刚执行过。
	// 用 RecordResult 而不是 Update：Update 刻意不写执行状态，
	// 直接改结构体字段再 Update 会被（正确地）忽略。
	env.advance(-12 * time.Minute)
	if err := env.store.RecordResult(ctx, created.ID, model.ExecutionSuccess, "", nil); err != nil {
		t.Fatalf("准备数据失败: %v", err)
	}
	env.advance(12 * time.Minute)

	exec, err := env.sched.RunTask(ctx, created.ID)
	if err != nil {
		t.Fatalf("执行不应报错: %v", err)
	}
	if exec.Result != model.ExecutionSkipped {
		t.Errorf("应跳过，实际 %q", exec.Result)
	}
	if env.rebootCount() != 0 {
		t.Error("跳过时绝不能执行重启")
	}
	if exec.Detail == "" {
		t.Error("跳过必须说明原因")
	}
}

// TestRunTaskDisablesAfterFailures 验证连续失败熔断。
func TestRunTaskDisablesAfterFailures(t *testing.T) {
	env := newTestEnv(t)
	ctx := context.Background()

	created, err := env.store.Create(ctx, dailyTask("每日重启"))
	if err != nil {
		t.Fatalf("创建任务失败: %v", err)
	}

	// 制造连续失败达到阈值（RecordResult 会在达阈值时自动禁用）
	for i := 0; i < model.MaxConsecutiveFailures; i++ {
		if err := env.store.RecordResult(ctx, created.ID, model.ExecutionFailed, "模拟失败", nil); err != nil {
			t.Fatalf("准备数据失败: %v", err)
		}
	}

	exec, err := env.sched.RunTask(ctx, created.ID)
	if err != nil {
		t.Fatalf("执行不应报错: %v", err)
	}
	// 任务已被自动禁用，因此判定为「跳过」而不是「失败」：
	// 禁用状态在 Decide 里是第一道检查，先于熔断计数。
	if exec.Result != model.ExecutionSkipped {
		t.Errorf("已禁用任务应跳过，实际 %q", exec.Result)
	}
	if env.rebootCount() != 0 {
		t.Error("熔断时绝不能执行重启")
	}

	// 任务应处于禁用状态，且有可读原因
	task, _ := env.store.Get(ctx, created.ID)
	if task.Enabled {
		t.Error("连续失败达阈值后应被自动禁用")
	}
	if task.DisabledReason == "" {
		t.Error("应记录禁用原因")
	}
}

// TestRunTaskDisableActionAudited 验证熔断动作本身被审计。
//
// 用未禁用的任务 + 高失败计数，直接命中 Decide 的 ActionDisable 分支。
func TestRunTaskDisableActionAudited(t *testing.T) {
	env := newTestEnv(t)
	ctx := context.Background()

	created, err := env.store.Create(ctx, dailyTask("每日重启"))
	if err != nil {
		t.Fatalf("创建任务失败: %v", err)
	}

	// 直接构造「失败计数超标但尚未禁用」的边界状态：
	// 先累计失败，再把 enabled 改回 true（模拟用户在别处手动改库）。
	//
	// 必须把失败时间推到 30 分钟以前：Decide 会先检查最短间隔，
	// 间隔不满足时会先返回「跳过」，测不到熔断分支。
	env.advance(-40 * time.Minute)
	for i := 0; i < model.MaxConsecutiveFailures; i++ {
		if err := env.store.RecordResult(ctx, created.ID, model.ExecutionFailed, "模拟失败", nil); err != nil {
			t.Fatalf("准备数据失败: %v", err)
		}
	}
	env.advance(40 * time.Minute)

	task, _ := env.store.Get(ctx, created.ID)
	task.Enabled = true
	if err := env.store.Update(ctx, task); err != nil {
		t.Fatalf("准备数据失败: %v", err)
	}

	exec, err := env.sched.RunTask(ctx, created.ID)
	if err != nil {
		t.Fatalf("执行不应报错: %v", err)
	}
	if exec.Result != model.ExecutionFailed {
		t.Errorf("应因熔断而失败，实际 %q", exec.Result)
	}
	if env.rebootCount() != 0 {
		t.Error("熔断时绝不能执行重启")
	}
	if !env.auditor.hasAction("schedule.task.auto_disable") {
		t.Error("熔断应写审计")
	}
}

// TestRunTaskRecordsFailureWhenRebootFails 验证重启失败被记录。
func TestRunTaskRecordsFailureWhenRebootFails(t *testing.T) {
	env := newTestEnv(t)
	ctx := context.Background()

	created, err := env.store.Create(ctx, dailyTask("每日重启"))
	if err != nil {
		t.Fatalf("创建任务失败: %v", err)
	}

	env.sched.SetRebootExecutorForTest(func(ctx context.Context, delay int) error {
		return fmt.Errorf("模拟重启失败")
	})

	exec, err := env.sched.RunTask(ctx, created.ID)
	if err != nil {
		t.Fatalf("执行不应返回错误（结果应体现在记录里）: %v", err)
	}
	if exec.Result != model.ExecutionFailed {
		t.Errorf("应记录失败，实际 %q", exec.Result)
	}

	task, _ := env.store.Get(ctx, created.ID)
	if task.FailCount != 1 {
		t.Errorf("失败计数应加 1，实际 %d", task.FailCount)
	}
	if task.LastError == "" {
		t.Error("应记录错误原因")
	}
}

// TestCreateOnceTaskBecomesPending 验证单次任务转为待执行重启。
//
// launchd 的 StartCalendarInterval 没有年份字段，无法表达
// 「仅在某个日期执行一次」。用 pending 机制是唯一正确的做法。
func TestCreateOnceTaskBecomesPending(t *testing.T) {
	env := newTestEnv(t)
	ctx := context.Background()

	at := env.currentTime().Add(30 * time.Minute)
	task := model.ScheduledTask{
		Name:               "单次重启",
		Kind:               model.TaskKindReboot,
		Recurrence:         model.Recurrence{Kind: model.RecurOnce, OnceAt: &at},
		Timezone:           "Asia/Shanghai",
		Enabled:            true,
		CatchUp:            model.CatchUpNever,
		MinIntervalSeconds: int(model.DefaultMinRebootInterval.Seconds()),
	}

	created, err := env.sched.Create(ctx, task)
	if err != nil {
		t.Fatalf("创建失败: %v", err)
	}

	// 不应安装 plist
	if env.installer.has(launcherLabel(created.ID)) {
		t.Error("单次任务不应安装 launchd plist")
	}

	// 应成为待执行重启
	st, err := env.sched.Status(ctx)
	if err != nil {
		t.Fatalf("查询状态失败: %v", err)
	}
	if st.Pending == nil {
		t.Fatal("单次任务应转为待执行重启")
	}
	if st.Pending.TaskID != created.ID {
		t.Errorf("待执行重启应关联任务 ID %d，实际 %d", created.ID, st.Pending.TaskID)
	}
}

// TestCreateOnceTaskConflictsWithPending 验证冲突时拒绝。
func TestCreateOnceTaskConflictsWithPending(t *testing.T) {
	env := newTestEnv(t)
	ctx := context.Background()

	if _, err := env.sched.ScheduleReboot(ctx, ScheduleRequest{
		DelaySeconds: 600, Reason: "已有", Actor: "a",
	}); err != nil {
		t.Fatalf("排定失败: %v", err)
	}

	at := env.currentTime().Add(30 * time.Minute)
	task := model.ScheduledTask{
		Name:               "单次重启",
		Kind:               model.TaskKindReboot,
		Recurrence:         model.Recurrence{Kind: model.RecurOnce, OnceAt: &at},
		Timezone:           "Asia/Shanghai",
		Enabled:            true,
		MinIntervalSeconds: int(model.DefaultMinRebootInterval.Seconds()),
	}

	_, err := env.sched.Create(ctx, task)
	if err == nil {
		t.Fatal("已有待执行重启时应拒绝创建单次任务")
	}
	if code := model.CodeOf(err); code != model.CodeConflict {
		t.Errorf("应为冲突错误，实际 %q", code)
	}
}

// TestDeleteRemovesLauncher 验证删除任务时移除 plist。
func TestDeleteRemovesLauncher(t *testing.T) {
	env := newTestEnv(t)
	ctx := context.Background()

	created, err := env.sched.Create(ctx, dailyTask("要删除的任务"))
	if err != nil {
		t.Fatalf("创建失败: %v", err)
	}
	label := launcherLabel(created.ID)
	if !env.installer.has(label) {
		t.Fatal("应先安装 plist")
	}

	if err := env.sched.Delete(ctx, created.ID); err != nil {
		t.Fatalf("删除失败: %v", err)
	}
	if env.installer.has(label) {
		t.Error("删除任务必须移除 plist，否则仍会按计划执行重启")
	}
}

// TestDeleteReportsLauncherCleanupFailure 验证清理失败时明确报错。
func TestDeleteReportsLauncherCleanupFailure(t *testing.T) {
	env := newTestEnv(t)
	ctx := context.Background()

	created, err := env.sched.Create(ctx, dailyTask("任务"))
	if err != nil {
		t.Fatalf("创建失败: %v", err)
	}
	env.installer.failRemove = true

	err = env.sched.Delete(ctx, created.ID)
	if err == nil {
		t.Error("清理 launchd 失败时应报错，不能假装删除成功")
	}
}

// TestUpdateCannotTamperExecState 验证更新接口不能改写执行状态。
//
// 关键：若允许通过 HTTP 请求体写入 fail_count / last_run_at，
// 熔断与间隔保护都能被绕过。
func TestUpdateCannotTamperExecState(t *testing.T) {
	env := newTestEnv(t)
	ctx := context.Background()

	created, err := env.sched.Create(ctx, dailyTask("任务"))
	if err != nil {
		t.Fatalf("创建失败: %v", err)
	}

	// 制造失败，使 fail_count = 2
	for i := 0; i < 2; i++ {
		if err := env.store.RecordResult(ctx, created.ID, model.ExecutionFailed, "失败", nil); err != nil {
			t.Fatalf("准备数据失败: %v", err)
		}
	}

	// 提交一个「看似干净」的任务对象，试图把 fail_count 改成 0
	got, err := env.store.Get(ctx, created.ID)
	if err != nil {
		t.Fatalf("读取失败: %v", err)
	}
	got.FailCount = 0
	got.LastRunAt = nil
	got.Name = "改个名字"

	updated, err := env.sched.Update(ctx, got)
	if err != nil {
		t.Fatalf("更新失败: %v", err)
	}
	if updated.FailCount != 2 {
		t.Errorf("失败计数不应被请求体改写，实际 %d", updated.FailCount)
	}
	if updated.Name != "改个名字" {
		t.Errorf("名称应被更新，实际 %q", updated.Name)
	}
}

// TestUpdateReenableClearsFailCount 验证重新启用时清零失败计数。
//
// 否则用户手动启用后，下次失败会立刻再被熔断。
func TestUpdateReenableClearsFailCount(t *testing.T) {
	env := newTestEnv(t)
	ctx := context.Background()

	created, err := env.sched.Create(ctx, dailyTask("任务"))
	if err != nil {
		t.Fatalf("创建失败: %v", err)
	}

	// 触发熔断：连续失败达阈值会被自动禁用
	for i := 0; i < model.MaxConsecutiveFailures; i++ {
		if err := env.store.RecordResult(ctx, created.ID, model.ExecutionFailed, "模拟失败", nil); err != nil {
			t.Fatalf("准备数据失败: %v", err)
		}
	}

	task, _ := env.store.Get(ctx, created.ID)
	if task.Enabled {
		t.Fatal("应已被熔断禁用")
	}

	// 用户重新启用
	task.Enabled = true
	updated, err := env.sched.Update(ctx, task)
	if err != nil {
		t.Fatalf("更新失败: %v", err)
	}
	if updated.FailCount != 0 {
		t.Errorf("重新启用后失败计数应清零，实际 %d", updated.FailCount)
	}
	if updated.DisabledReason != "" {
		t.Errorf("重新启用后应清除禁用原因，实际 %q", updated.DisabledReason)
	}
}

// TestCheckMissedSkipsByDefault 验证默认策略下错过就跳过。
func TestCheckMissedSkipsByDefault(t *testing.T) {
	env := newTestEnv(t)
	ctx := context.Background()

	created, err := env.store.Create(ctx, dailyTask("每日重启"))
	if err != nil {
		t.Fatalf("创建任务失败: %v", err)
	}

	// 计划时刻在 2 小时前（已错过）
	past := env.currentTime().Add(-2 * time.Hour)
	task, _ := env.store.Get(ctx, created.ID)
	task.NextRunAt = &past
	if err := env.store.Update(ctx, task); err != nil {
		t.Fatalf("准备数据失败: %v", err)
	}

	if err := env.sched.CheckMissed(ctx); err != nil {
		t.Fatalf("检查失败: %v", err)
	}

	if env.rebootCount() != 0 {
		t.Error("默认策略下不应补执行")
	}

	execs, _ := env.store.ListExecutions(ctx, created.ID, 10)
	if len(execs) == 0 {
		t.Fatal("应记录一条 skipped")
	}
	if execs[0].Result != model.ExecutionSkipped {
		t.Errorf("应记录为 skipped，实际 %q", execs[0].Result)
	}
}

// TestCheckMissedRunsWithinWindow 验证窗口内补执行。
func TestCheckMissedRunsWithinWindow(t *testing.T) {
	env := newTestEnv(t)
	ctx := context.Background()

	task := dailyTask("每日重启")
	task.CatchUp = model.CatchUpWithinWindow
	task.CatchUpWindowSeconds = int(model.DefaultCatchUpWindow.Seconds())

	created, err := env.store.Create(ctx, task)
	if err != nil {
		t.Fatalf("创建任务失败: %v", err)
	}

	// 1.5 小时前错过，在 6 小时窗口内
	past := env.currentTime().Add(-90 * time.Minute)
	got, _ := env.store.Get(ctx, created.ID)
	got.NextRunAt = &past
	if err := env.store.Update(ctx, got); err != nil {
		t.Fatalf("准备数据失败: %v", err)
	}

	if err := env.sched.CheckMissed(ctx); err != nil {
		t.Fatalf("检查失败: %v", err)
	}

	if env.rebootCount() != 1 {
		t.Errorf("窗口内应补执行一次，实际 %d", env.rebootCount())
	}
}

// TestStatusReportsCapability 验证状态包含能力信息。
func TestStatusReportsCapability(t *testing.T) {
	env := newTestEnv(t)
	ctx := context.Background()

	st, err := env.sched.Status(ctx)
	if err != nil {
		t.Fatalf("查询失败: %v", err)
	}
	if !st.CanReboot {
		t.Error("fake 默认应可重启")
	}
	if st.UptimeSeconds <= 0 {
		t.Errorf("运行时长应为正值，实际 %d", st.UptimeSeconds)
	}
	if st.LastRebootAt == nil {
		t.Error("应返回上次启动时刻")
	}

	// 非 root 场景
	env.power.CanReboot = false
	st, _ = env.sched.Status(ctx)
	if st.CanReboot {
		t.Error("应反映不具备重启能力")
	}
	if st.RebootUnavailableReason == "" {
		t.Error("应说明为什么不能重启")
	}
}

// TestLauncherLabelRoundTrip 验证标签与任务 ID 的互转。
func TestLauncherLabelRoundTrip(t *testing.T) {
	for _, id := range []int64{0, 1, 7, 12345} {
		label := launcherLabel(id)
		if got := taskIDFromLabel(label); got != id {
			t.Errorf("ID %d 往返得到 %d（label=%s）", id, got, label)
		}
	}

	// 非本应用的 label 应返回 0
	if got := taskIDFromLabel("com.apple.finder"); got != 0 {
		t.Errorf("外部 label 应返回 0，实际 %d", got)
	}
}

// TestLocalBinPathValidation 验证缺少 BinPath 时拒绝构造。
//
// 没有它就无法生成 launchd 的 ProgramArguments，
// 装上去的任务会因为找不到程序而静默失败。
func TestLocalBinPathValidation(t *testing.T) {
	_, err := New(Options{
		Store:     nil,
		Installer: newFakeInstaller(t),
		Power:     fake.NewPower(),
		BinPath:   "/usr/local/bin/homelab-manager",
	})
	if err == nil {
		t.Error("缺少 Store 应拒绝构造")
	}

	_, err = New(Options{
		Store:     &taskstore.Store{},
		Installer: newFakeInstaller(t),
		Power:     fake.NewPower(),
		BinPath:   "",
	})
	if err == nil {
		t.Error("缺少 BinPath 应拒绝构造")
	}
}
