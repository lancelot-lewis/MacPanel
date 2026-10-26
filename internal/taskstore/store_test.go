package taskstore

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/lancelot-lewis/MacPanel/internal/database"
	"github.com/lancelot-lewis/MacPanel/internal/model"
)

func newTestStore(t *testing.T) *Store {
	t.Helper()
	db, err := database.Open(context.Background(), filepath.Join(t.TempDir(), "task.db"))
	if err != nil {
		t.Fatalf("打开数据库失败: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return New(db)
}

// sampleTask 构造一个合法的每日重启任务。
func sampleTask() model.ScheduledTask {
	return model.ScheduledTask{
		Name:               "每日凌晨重启",
		Kind:               model.TaskKindReboot,
		Recurrence:         model.Recurrence{Kind: model.RecurDaily, TimeOfDay: "04:30"},
		Timezone:           "Asia/Shanghai",
		Enabled:            true,
		CatchUp:            model.CatchUpNever,
		MinIntervalSeconds: int(model.DefaultMinRebootInterval.Seconds()),
	}
}

// TestCreateAndGetRoundTrip 验证任务全字段往返一致。
func TestCreateAndGetRoundTrip(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	next := time.Now().UTC().Add(6 * time.Hour).Truncate(time.Second)
	task := sampleTask()
	task.CatchUp = model.CatchUpWithinWindow
	task.CatchUpWindowSeconds = int(2 * time.Hour.Seconds())
	task.MinIntervalSeconds = int(45 * time.Minute.Seconds())
	task.NextRunAt = &next

	created, err := s.Create(ctx, task)
	if err != nil {
		t.Fatalf("创建失败: %v", err)
	}
	if created.ID <= 0 {
		t.Fatalf("应分配 ID，实际 %d", created.ID)
	}

	got, err := s.Get(ctx, created.ID)
	if err != nil {
		t.Fatalf("读取失败: %v", err)
	}

	if got.Name != task.Name {
		t.Errorf("名称不一致：%q != %q", got.Name, task.Name)
	}
	if got.Recurrence.Kind != model.RecurDaily || got.Recurrence.TimeOfDay != "04:30" {
		t.Errorf("执行周期未正确往返: %+v", got.Recurrence)
	}
	if got.CatchUp != model.CatchUpWithinWindow {
		t.Errorf("补执行策略不一致: %q", got.CatchUp)
	}
	if got.CatchUpWindowSeconds != task.CatchUpWindowSeconds {
		t.Errorf("补执行窗口不一致: %d != %d", got.CatchUpWindowSeconds, task.CatchUpWindowSeconds)
	}
	if got.MinIntervalSeconds != task.MinIntervalSeconds {
		t.Errorf("最短间隔不一致: %d != %d", got.MinIntervalSeconds, task.MinIntervalSeconds)
	}
	if !got.Enabled {
		t.Error("启用状态丢失")
	}
	if got.NextRunAt == nil || !got.NextRunAt.Equal(next) {
		t.Errorf("下次执行时间不一致: %v != %v", got.NextRunAt, next)
	}
}

// TestListOrdersByCreation 验证列表按创建时间升序。
func TestListOrdersByCreation(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	for _, name := range []string{"任务A", "任务B", "任务C"} {
		task := sampleTask()
		task.Name = name
		if _, err := s.Create(ctx, task); err != nil {
			t.Fatalf("创建 %s 失败: %v", name, err)
		}
	}

	list, err := s.List(ctx)
	if err != nil {
		t.Fatalf("列表查询失败: %v", err)
	}
	if len(list) != 3 {
		t.Fatalf("应有 3 个任务，实际 %d", len(list))
	}
	want := []string{"任务A", "任务B", "任务C"}
	for i, w := range want {
		if list[i].Name != w {
			t.Errorf("第 %d 个应为 %q，实际 %q", i+1, w, list[i].Name)
		}
	}
}

// TestCreateRejectsInvalidTask 验证非法任务不落库。
func TestCreateRejectsInvalidTask(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	bad := sampleTask()
	bad.MinIntervalSeconds = 60 // 低于 30 分钟下限

	if _, err := s.Create(ctx, bad); err == nil {
		t.Error("应拒绝低于最短间隔下限的任务")
	}

	// 确认没有写入
	list, err := s.List(ctx)
	if err != nil {
		t.Fatalf("列表查询失败: %v", err)
	}
	if len(list) != 0 {
		t.Errorf("非法任务不应写入，实际有 %d 条", len(list))
	}
}

// TestGetNotFound 验证不存在的任务返回明确错误。
func TestGetNotFound(t *testing.T) {
	s := newTestStore(t)
	if _, err := s.Get(context.Background(), 9999); err == nil {
		t.Error("应返回未找到错误")
	}
}

// TestUpdateChangesDefinitionOnly 验证更新不会覆盖执行状态。
//
// 这是关键约束：执行状态（last_run_at / fail_count）由 RecordResult 维护。
// 若 Update 也写这些字段，用户改个名字就会把失败计数清零，
// 熔断机制随之失效。
func TestUpdateChangesDefinitionOnly(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	created, err := s.Create(ctx, sampleTask())
	if err != nil {
		t.Fatalf("创建失败: %v", err)
	}

	// 制造一次失败，使 fail_count = 1
	if err := s.RecordResult(ctx, created.ID, model.ExecutionFailed, "模拟失败", nil); err != nil {
		t.Fatalf("记录失败结果出错: %v", err)
	}

	task, err := s.Get(ctx, created.ID)
	if err != nil {
		t.Fatalf("读取失败: %v", err)
	}
	if task.FailCount != 1 {
		t.Fatalf("失败计数应为 1，实际 %d", task.FailCount)
	}

	// 改名，但不动执行状态字段
	task.Name = "改名后的任务"
	if err := s.Update(ctx, task); err != nil {
		t.Fatalf("更新失败: %v", err)
	}

	after, err := s.Get(ctx, created.ID)
	if err != nil {
		t.Fatalf("重新读取失败: %v", err)
	}
	if after.Name != "改名后的任务" {
		t.Errorf("名称未更新：%q", after.Name)
	}
	if after.FailCount != 1 {
		t.Errorf("失败计数不应被 Update 清零，实际 %d", after.FailCount)
	}
	if after.LastError != "模拟失败" {
		t.Errorf("最近错误不应被清空，实际 %q", after.LastError)
	}
}

// TestUpdateNotFound 验证更新不存在的任务返回明确错误。
func TestUpdateNotFound(t *testing.T) {
	s := newTestStore(t)
	task := sampleTask()
	task.ID = 9999
	if err := s.Update(context.Background(), task); err == nil {
		t.Error("应返回未找到错误")
	}
}

// TestDeleteRemovesExecutions 验证删除任务时执行记录一并清除。
func TestDeleteRemovesExecutions(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	created, err := s.Create(ctx, sampleTask())
	if err != nil {
		t.Fatalf("创建失败: %v", err)
	}
	if err := s.RecordSkipped(ctx, created.ID, "测试跳过"); err != nil {
		t.Fatalf("写入跳过记录失败: %v", err)
	}

	if err := s.Delete(ctx, created.ID); err != nil {
		t.Fatalf("删除失败: %v", err)
	}

	// 任务应不存在
	if _, err := s.Get(ctx, created.ID); err == nil {
		t.Error("任务应已删除")
	}

	// 执行记录也应清除
	execs, err := s.ListExecutions(ctx, created.ID, 10)
	if err != nil {
		t.Fatalf("查询执行记录失败: %v", err)
	}
	if len(execs) != 0 {
		t.Errorf("执行记录应随任务删除，实际剩 %d 条", len(execs))
	}
}

// TestRecordSuccessResetsFailCount 验证成功执行清零失败计数。
func TestRecordSuccessResetsFailCount(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	created, _ := s.Create(ctx, sampleTask())

	// 先失败两次
	for i := 0; i < 2; i++ {
		if err := s.RecordResult(ctx, created.ID, model.ExecutionFailed, "失败", nil); err != nil {
			t.Fatalf("记录失败出错: %v", err)
		}
	}
	task, _ := s.Get(ctx, created.ID)
	if task.FailCount != 2 {
		t.Fatalf("应为 2 次失败，实际 %d", task.FailCount)
	}

	// 成功后清零
	if err := s.RecordResult(ctx, created.ID, model.ExecutionSuccess, "", nil); err != nil {
		t.Fatalf("记录成功出错: %v", err)
	}
	task, _ = s.Get(ctx, created.ID)
	if task.FailCount != 0 {
		t.Errorf("成功后失败计数应清零，实际 %d", task.FailCount)
	}
	if task.LastResult != model.ExecutionSuccess {
		t.Errorf("最近结果应为 success，实际 %q", task.LastResult)
	}
}

// TestAutoDisableAfterConsecutiveFailures 验证连续失败自动禁用。
//
// 这是防「无限失败重试」的关键：若每次都失败（例如进程无 root），
// 不熔断只会在系统日志里不停刷错误。
func TestAutoDisableAfterConsecutiveFailures(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	created, _ := s.Create(ctx, sampleTask())

	for i := 0; i < model.MaxConsecutiveFailures; i++ {
		if err := s.RecordResult(ctx, created.ID, model.ExecutionFailed, "模拟失败", nil); err != nil {
			t.Fatalf("第 %d 次记录失败出错: %v", i+1, err)
		}
	}

	task, err := s.Get(ctx, created.ID)
	if err != nil {
		t.Fatalf("读取失败: %v", err)
	}
	if task.Enabled {
		t.Error("连续失败达到阈值后应自动禁用")
	}
	if task.DisabledReason == "" {
		t.Error("应记录自动禁用的原因")
	}
}

// TestSkippedDoesNotCountAsFailure 验证跳过不计入失败。
//
// 「跳过」是保护机制生效的结果（如间隔不足），不是错误。
// 若计入失败，正常运行的防循环重启反而会把任务自己熔断掉。
func TestSkippedDoesNotCountAsFailure(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	created, _ := s.Create(ctx, sampleTask())
	for i := 0; i < 5; i++ {
		if err := s.RecordResult(ctx, created.ID, model.ExecutionSkipped, "间隔不足", nil); err != nil {
			t.Fatalf("记录跳过出错: %v", err)
		}
	}

	task, _ := s.Get(ctx, created.ID)
	if task.FailCount != 0 {
		t.Errorf("跳过不应增加失败计数，实际 %d", task.FailCount)
	}
	if !task.Enabled {
		t.Error("跳过不应导致禁用")
	}
	// last_run_at 的语义是「真正执行过」，跳过不应写入
	if task.LastRunAt != nil {
		t.Errorf("跳过不应更新 last_run_at，实际 %v", task.LastRunAt)
	}
}

// TestRecordResultUnknown 验证未知结果被拒。
func TestRecordResultUnknown(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	created, _ := s.Create(ctx, sampleTask())

	if err := s.RecordResult(ctx, created.ID, "bogus", "", nil); err == nil {
		t.Error("应拒绝未知的执行结果")
	}
}

// TestExecutionLifecycle 验证执行记录的完整生命周期。
func TestExecutionLifecycle(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	created, _ := s.Create(ctx, sampleTask())

	execID, err := s.StartExecution(ctx, created.ID)
	if err != nil {
		t.Fatalf("创建执行记录失败: %v", err)
	}

	// 刚创建时是 running，没有结束时间
	execs, err := s.ListExecutions(ctx, created.ID, 10)
	if err != nil {
		t.Fatalf("查询失败: %v", err)
	}
	if len(execs) != 1 || execs[0].Result != model.ExecutionRunning {
		t.Fatalf("应为 1 条 running 记录，实际 %+v", execs)
	}
	if execs[0].FinishedAt != nil {
		t.Error("running 记录不应有结束时间")
	}
	if execs[0].TaskName != created.Name {
		t.Errorf("应带出任务名，实际 %q", execs[0].TaskName)
	}

	// 收尾
	if err := s.FinishExecution(ctx, execID, model.ExecutionSuccess, "已执行"); err != nil {
		t.Fatalf("收尾失败: %v", err)
	}
	execs, _ = s.ListExecutions(ctx, created.ID, 10)
	if execs[0].Result != model.ExecutionSuccess {
		t.Errorf("结果应更新，实际 %q", execs[0].Result)
	}
	if execs[0].FinishedAt == nil {
		t.Error("完成后应有结束时间")
	}
	if execs[0].Detail != "已执行" {
		t.Errorf("说明未保存，实际 %q", execs[0].Detail)
	}
}

// TestRecoverStaleExecutions 验证遗留 running 记录被收尾。
//
// 场景：Agent 在执行重启前崩溃，留下永远 running 的记录。
// 界面上看起来「有个任务一直在跑」，实际什么都没在跑。
func TestRecoverStaleExecutions(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	created, _ := s.Create(ctx, sampleTask())
	if _, err := s.StartExecution(ctx, created.ID); err != nil {
		t.Fatalf("创建执行记录失败: %v", err)
	}

	n, err := s.RecoverStaleExecutions(ctx, time.Time{})
	if err != nil {
		t.Fatalf("清理失败: %v", err)
	}
	if n != 1 {
		t.Errorf("应清理 1 条遗留记录，实际 %d", n)
	}

	execs, _ := s.ListExecutions(ctx, created.ID, 10)
	if execs[0].Result != model.ExecutionFailed {
		t.Errorf("遗留记录应标记为失败，实际 %q", execs[0].Result)
	}
	if execs[0].Detail == "" {
		t.Error("应说明为何标记失败")
	}

	// 幂等：再次调用不应影响已收尾的记录
	n, err = s.RecoverStaleExecutions(ctx, time.Time{})
	if err != nil {
		t.Fatalf("二次清理失败: %v", err)
	}
	if n != 0 {
		t.Errorf("二次清理不应有变更，实际 %d", n)
	}
}

// TestListExecutionsFilterAndLimit 验证筛选与条数限制。
func TestListExecutionsFilterAndLimit(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	taskA, _ := s.Create(ctx, sampleTask())
	taskB := sampleTask()
	taskB.Name = "另一个任务"
	taskB, _ = s.Create(ctx, taskB)

	for i := 0; i < 3; i++ {
		if err := s.RecordSkipped(ctx, taskA.ID, "A 跳过"); err != nil {
			t.Fatalf("写入失败: %v", err)
		}
	}
	if err := s.RecordSkipped(ctx, taskB.ID, "B 跳过"); err != nil {
		t.Fatalf("写入失败: %v", err)
	}

	onlyA, err := s.ListExecutions(ctx, taskA.ID, 10)
	if err != nil {
		t.Fatalf("查询失败: %v", err)
	}
	if len(onlyA) != 3 {
		t.Errorf("任务 A 应有 3 条记录，实际 %d", len(onlyA))
	}

	all, err := s.ListExecutions(ctx, 0, 10)
	if err != nil {
		t.Fatalf("查询失败: %v", err)
	}
	if len(all) != 4 {
		t.Errorf("全部应有 4 条记录，实际 %d", len(all))
	}

	limited, err := s.ListExecutions(ctx, 0, 2)
	if err != nil {
		t.Fatalf("查询失败: %v", err)
	}
	if len(limited) != 2 {
		t.Errorf("limit=2 应返回 2 条，实际 %d", len(limited))
	}
}

// TestPendingRebootRoundTrip 验证待执行重启的持久化。
func TestPendingRebootRoundTrip(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	// 初始为空
	got, err := s.LoadPendingReboot(ctx)
	if err != nil {
		t.Fatalf("读取失败: %v", err)
	}
	if got != nil {
		t.Errorf("初始应无待执行重启，实际 %+v", got)
	}

	at := time.Now().UTC().Add(30 * time.Minute).Truncate(time.Second)
	p := model.PendingReboot{
		ScheduledAt: at,
		Reason:      "手动延迟重启",
		RequestedBy: "han@local",
		CreatedAt:   time.Now().UTC().Truncate(time.Second),
	}
	if err := s.SavePendingReboot(ctx, p); err != nil {
		t.Fatalf("保存失败: %v", err)
	}

	got, err = s.LoadPendingReboot(ctx)
	if err != nil {
		t.Fatalf("读取失败: %v", err)
	}
	if got == nil {
		t.Fatal("应读到待执行重启")
	}
	if !got.ScheduledAt.Equal(at) {
		t.Errorf("计划时刻不一致: %v != %v", got.ScheduledAt, at)
	}
	if got.Reason != p.Reason || got.RequestedBy != p.RequestedBy {
		t.Errorf("字段丢失: %+v", got)
	}

	// 再次保存应覆盖而不是报唯一冲突
	p.Reason = "改过的原因"
	if err := s.SavePendingReboot(ctx, p); err != nil {
		t.Fatalf("覆盖保存失败: %v", err)
	}
	got, _ = s.LoadPendingReboot(ctx)
	if got.Reason != "改过的原因" {
		t.Errorf("应覆盖为新值，实际 %q", got.Reason)
	}

	// 清除
	if err := s.ClearPendingReboot(ctx); err != nil {
		t.Fatalf("清除失败: %v", err)
	}
	got, _ = s.LoadPendingReboot(ctx)
	if got != nil {
		t.Errorf("清除后应为空，实际 %+v", got)
	}

	// 幂等：清除空记录不报错
	if err := s.ClearPendingReboot(ctx); err != nil {
		t.Errorf("重复清除不应报错: %v", err)
	}
}

// TestLoadPendingRebootCorrupted 验证损坏数据被清理而不是让功能不可用。
func TestLoadPendingRebootCorrupted(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	// 直接写入一段非法 JSON
	_, err := s.db.SQL().ExecContext(ctx,
		`INSERT INTO settings (key, value, updated_at) VALUES (?, ?, ?)`,
		pendingRebootKey, "{not json", database.FormatTime(database.Now()))
	if err != nil {
		t.Fatalf("写入测试数据失败: %v", err)
	}

	got, err := s.LoadPendingReboot(ctx)
	if err != nil {
		t.Fatalf("损坏数据不应导致报错: %v", err)
	}
	if got != nil {
		t.Errorf("损坏数据应返回 nil，实际 %+v", got)
	}

	// 坏数据应已被清理
	got, _ = s.LoadPendingReboot(ctx)
	if got != nil {
		t.Error("损坏数据应被清除")
	}
}

// TestSetLauncherLabel 验证调度器标签的读写。
func TestSetLauncherLabel(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	created, _ := s.Create(ctx, sampleTask())
	if err := s.SetLauncherLabel(ctx, created.ID, "com.homelab.manager.task.7"); err != nil {
		t.Fatalf("设置标签失败: %v", err)
	}

	task, _ := s.Get(ctx, created.ID)
	if task.LauncherLabel != "com.homelab.manager.task.7" {
		t.Errorf("标签未保存，实际 %q", task.LauncherLabel)
	}
}

// TestCreateBackfillsDescription 验证人读描述被自动填充。
func TestCreateBackfillsDescription(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	task := sampleTask()
	task.Recurrence = model.Recurrence{
		Kind:      model.RecurWeekly,
		TimeOfDay: "03:00",
		Weekdays:  []int{1, 4},
	}
	if _, err := s.Create(ctx, task); err != nil {
		t.Fatalf("创建失败: %v", err)
	}

	var desc string
	if err := s.db.SQL().QueryRowContext(ctx,
		`SELECT schedule FROM scheduled_tasks LIMIT 1`).Scan(&desc); err != nil {
		t.Fatalf("查询描述失败: %v", err)
	}
	if desc != "每周一、周四 03:00" {
		t.Errorf("描述应为人读形式，实际 %q", desc)
	}
}
