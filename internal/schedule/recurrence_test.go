package schedule

import (
	"testing"
	"time"

	"github.com/lancelot-lewis/MacPanel/internal/model"
)

// newTask 构造测试用任务。
func newTask(rec model.Recurrence) model.ScheduledTask {
	return model.ScheduledTask{
		Name:               "测试任务",
		Kind:               model.TaskKindReboot,
		Recurrence:         rec,
		Timezone:           "Asia/Shanghai",
		Enabled:            true,
		CatchUp:            model.CatchUpNever,
		MinIntervalSeconds: int(model.DefaultMinRebootInterval.Seconds()),
	}
}

// mustLoad 加载时区，失败直接终止（时区数据缺失说明环境异常）。
func mustLoad(t *testing.T, name string) *time.Location {
	t.Helper()
	loc, err := time.LoadLocation(name)
	if err != nil {
		t.Fatalf("加载时区 %s 失败: %v", name, err)
	}
	return loc
}

// TestNextAfterDaily 验证每日执行的两种基本情形：
// 今天还没到 → 今天；今天已过 → 明天。
func TestNextAfterDaily(t *testing.T) {
	loc := mustLoad(t, "Asia/Shanghai")
	task := newTask(model.Recurrence{Kind: model.RecurDaily, TimeOfDay: "04:30"})

	cases := []struct {
		name string
		now  time.Time
		want time.Time
	}{
		{
			name: "当天凌晨，未到 04:30",
			now:  time.Date(2026, 10, 26, 2, 0, 0, 0, loc),
			want: time.Date(2026, 10, 26, 4, 30, 0, 0, loc),
		},
		{
			name: "当天已过 04:30",
			now:  time.Date(2026, 10, 26, 9, 0, 0, 0, loc),
			want: time.Date(2026, 10, 27, 4, 30, 0, 0, loc),
		},
		{
			name: "恰好等于 04:30 时取次日",
			now:  time.Date(2026, 10, 26, 4, 30, 0, 0, loc),
			want: time.Date(2026, 10, 27, 4, 30, 0, 0, loc),
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, ok := NextAfter(task, c.now)
			if !ok {
				t.Fatal("应能找到下次执行时刻")
			}
			if !got.Equal(c.want) {
				t.Errorf("期望 %s，实际 %s", c.want, got)
			}
		})
	}
}

// TestNextAfterDailyCrossMonth 验证跨月与跨年边界不过度依赖日期算术。
func TestNextAfterDailyCrossMonth(t *testing.T) {
	loc := mustLoad(t, "Asia/Shanghai")
	task := newTask(model.Recurrence{Kind: model.RecurDaily, TimeOfDay: "23:00"})

	got, ok := NextAfter(task, time.Date(2026, 12, 31, 23, 30, 0, 0, loc))
	if !ok {
		t.Fatal("应能找到下次执行时刻")
	}
	want := time.Date(2027, 1, 1, 23, 0, 0, 0, loc)
	if !got.Equal(want) {
		t.Errorf("跨年应得到 %s，实际 %s", want, got)
	}
}

// TestNextAfterWeekly 验证每周多天的选择与跨周。
func TestNextAfterWeekly(t *testing.T) {
	loc := mustLoad(t, "Asia/Shanghai")
	// 2026-10-26 是周一。选周一与周四 03:00。
	task := newTask(model.Recurrence{
		Kind:      model.RecurWeekly,
		TimeOfDay: "03:00",
		Weekdays:  []int{int(time.Monday), int(time.Thursday)},
	})

	// 周一 01:00 → 当天 03:00（周一）
	got, ok := NextAfter(task, time.Date(2026, 10, 26, 1, 0, 0, 0, loc))
	if !ok {
		t.Fatal("应能找到下次执行时刻")
	}
	if want := time.Date(2026, 10, 26, 3, 0, 0, 0, loc); !got.Equal(want) {
		t.Errorf("期望周一当天 %s，实际 %s", want, got)
	}

	// 周一 05:00 → 周四 03:00
	got, ok = NextAfter(task, time.Date(2026, 10, 26, 5, 0, 0, 0, loc))
	if !ok {
		t.Fatal("应能找到下次执行时刻")
	}
	if want := time.Date(2026, 10, 29, 3, 0, 0, 0, loc); !got.Equal(want) {
		t.Errorf("期望周四 %s，实际 %s", want, got)
	}

	// 周四 05:00 → 下周一 03:00
	got, ok = NextAfter(task, time.Date(2026, 10, 29, 5, 0, 0, 0, loc))
	if !ok {
		t.Fatal("应能找到下次执行时刻")
	}
	if want := time.Date(2026, 11, 2, 3, 0, 0, 0, loc); !got.Equal(want) {
		t.Errorf("期望下周一 %s，实际 %s", want, got)
	}
}

// TestNextAfterIntervalUsesLastRun 验证「每隔 N 小时」以历史执行时间为基准。
//
// 这条语义很关键：若以整点对齐，用户看到的「每 6 小时」实际会漂移，
// 例如首次在 04:17 执行，之后每次都在 04:17 而非 00:00/06:00。
func TestNextAfterIntervalUsesLastRun(t *testing.T) {
	task := newTask(model.Recurrence{Kind: model.RecurIntervalHours, IntervalHours: 6})
	loc := mustLoad(t, "Asia/Shanghai")

	lastRun := time.Date(2026, 10, 26, 1, 17, 0, 0, loc)
	task.LastRunAt = &lastRun

	now := time.Date(2026, 10, 26, 3, 0, 0, 0, loc)
	got, ok := NextAfter(task, now)
	if !ok {
		t.Fatal("应能找到下次执行时刻")
	}
	if want := time.Date(2026, 10, 26, 7, 17, 0, 0, loc); !got.Equal(want) {
		t.Errorf("应从上一次执行 +6 小时得到 %s，实际 %s", want, got)
	}
}

// TestNextAfterIntervalSkipsMissedSteps 验证长时间未执行时不会返回「过去时刻」。
//
// 场景：每 6 小时执行，但机器关机了 3 天。若直接返回 last+6h，
// 得到的是过去的时间戳，会立刻触发一次执行，而且下一次仍算过去，
// 可能连环触发。正确行为是返回严格晚于 now 的第一个步点。
func TestNextAfterIntervalSkipsMissedSteps(t *testing.T) {
	task := newTask(model.Recurrence{Kind: model.RecurIntervalHours, IntervalHours: 6})
	loc := mustLoad(t, "Asia/Shanghai")

	lastRun := time.Date(2026, 10, 20, 12, 0, 0, 0, loc)
	task.LastRunAt = &lastRun

	now := time.Date(2026, 10, 26, 15, 30, 0, 0, loc)
	got, ok := NextAfter(task, now)
	if !ok {
		t.Fatal("应能找到下次执行时刻")
	}
	if !got.After(now) {
		t.Fatalf("必须严格晚于 now，实际 %s <= %s", got, now)
	}
	if want := time.Date(2026, 10, 26, 18, 0, 0, 0, loc); !got.Equal(want) {
		t.Errorf("期望 %s，实际 %s", want, got)
	}
}

// TestNextAfterOnce 验证单次任务：未来有下次，过去没有。
func TestNextAfterOnce(t *testing.T) {
	loc := mustLoad(t, "Asia/Shanghai")
	at := time.Date(2026, 10, 27, 4, 0, 0, 0, loc)

	task := newTask(model.Recurrence{Kind: model.RecurOnce, OnceAt: &at})

	got, ok := NextAfter(task, time.Date(2026, 10, 26, 12, 0, 0, 0, loc))
	if !ok {
		t.Fatal("未来时刻应有下次执行")
	}
	if !got.Equal(at) {
		t.Errorf("期望 %s，实际 %s", at, got)
	}

	_, ok = NextAfter(task, time.Date(2026, 10, 28, 12, 0, 0, 0, loc))
	if ok {
		t.Error("已过去的单次任务不应再有下次执行")
	}
}

// TestNextAfterRespectsTaskTimezone 验证同一绝对时刻在不同任务时区下
// 得到不同的墙钟结果。
//
// 这是「显式存时区」的意义：两台机器或两个任务可以各自按自己的
// 时区理解「每天 04:30」，而不受系统时区设置影响。
func TestNextAfterRespectsTaskTimezone(t *testing.T) {
	// 同一绝对时刻：UTC 2026-10-26 00:00
	now := time.Date(2026, 10, 26, 0, 0, 0, 0, time.UTC)

	shTask := newTask(model.Recurrence{Kind: model.RecurDaily, TimeOfDay: "04:30"})
	shTask.Timezone = "Asia/Shanghai"

	nyTask := newTask(model.Recurrence{Kind: model.RecurDaily, TimeOfDay: "04:30"})
	nyTask.Timezone = "America/New_York"

	shNext, ok := NextAfter(shTask, now)
	if !ok {
		t.Fatal("上海任务应有下次")
	}
	// UTC 00:00 = 上海 08:00，已过 04:30 → 次日（10-27）04:30 上海时间
	if want := time.Date(2026, 10, 27, 4, 30, 0, 0, mustLoad(t, "Asia/Shanghai")); !shNext.Equal(want) {
		t.Errorf("上海任务期望 %s，实际 %s", want, shNext.UTC())
	}

	nyNext, ok := NextAfter(nyTask, now)
	if !ok {
		t.Fatal("纽约任务应有下次")
	}
	// UTC 00:00 = 纽约前一日 20:00，未到 04:30 → 当天（10-26）04:30 纽约时间
	if want := time.Date(2026, 10, 26, 4, 30, 0, 0, mustLoad(t, "America/New_York")); !nyNext.Equal(want) {
		t.Errorf("纽约任务期望 %s，实际 %s", want, nyNext.UTC())
	}

	if shNext.Equal(nyNext) {
		t.Error("不同时区的任务不应算出相同的绝对时刻")
	}
}

// TestNextAfterSkipsNonexistentWallClock 验证夏令时春季跳变时，
// 不存在的墙钟时刻被跳过而不是被静默归一化。
//
// 美东 2026-03-08 02:30 不存在（02:00 直接跳到 03:00）。
// 若返回归一化后的 03:30，用户会看到「设了 02:30，实际 03:30 跑」。
func TestNextAfterSkipsNonexistentWallClock(t *testing.T) {
	ny := mustLoad(t, "America/New_York")

	// 先确认测试环境确实有这次跳变，否则跳过（tzdata 差异时不假失败）。
	probe := time.Date(2026, 3, 8, 2, 30, 0, 0, ny)
	if probe.Hour() == 2 {
		t.Skip("当前 tzdata 中 2026-03-08 02:30 存在，跳过 DST 用例")
	}

	task := newTask(model.Recurrence{Kind: model.RecurDaily, TimeOfDay: "02:30"})
	task.Timezone = "America/New_York"

	now := time.Date(2026, 3, 7, 12, 0, 0, 0, ny)
	got, ok := NextAfter(task, now)
	if !ok {
		t.Fatal("应能找到下次执行时刻")
	}

	if got.Hour() != 2 || got.Minute() != 30 {
		t.Errorf("返回时刻必须是真实的 02:30，实际 %s", got)
	}
	// 03-08 的 02:30 不存在，应落到 03-09 02:30
	if want := time.Date(2026, 3, 9, 2, 30, 0, 0, ny); !got.Equal(want) {
		t.Errorf("应跳过不存在的时刻到 %s，实际 %s", want, got)
	}
}

// TestDecideSkipsDisabled 验证禁用任务不执行。
func TestDecideSkipsDisabled(t *testing.T) {
	task := newTask(model.Recurrence{Kind: model.RecurDaily, TimeOfDay: "04:30"})
	task.Enabled = false
	task.DisabledReason = "连续失败"

	d := Decide(task, time.Now(), false)
	if d.Action != ActionSkip {
		t.Errorf("禁用任务应跳过，实际 %s", d.Action)
	}
	if d.Reason == "" {
		t.Error("跳过必须给出原因")
	}
}

// TestDecideSkipsWhenPendingRebootExists 验证已有待执行重启时不叠加。
//
// 多份待执行叠加会造成「用户取消了一次，却仍被重启」。
func TestDecideSkipsWhenPendingRebootExists(t *testing.T) {
	task := newTask(model.Recurrence{Kind: model.RecurDaily, TimeOfDay: "04:30"})

	d := Decide(task, time.Now(), true)
	if d.Action != ActionSkip {
		t.Errorf("已有待执行重启时应跳过，实际 %s", d.Action)
	}
}

// TestDecideEnforcesMinInterval 验证最短执行间隔（防循环重启）。
func TestDecideEnforcesMinInterval(t *testing.T) {
	loc := mustLoad(t, "Asia/Shanghai")
	now := time.Date(2026, 10, 26, 12, 0, 0, 0, loc)

	task := newTask(model.Recurrence{Kind: model.RecurDaily, TimeOfDay: "04:30"})

	// 12 分钟前刚执行过 → 跳过
	recent := now.Add(-12 * time.Minute)
	task.LastRunAt = &recent
	d := Decide(task, now, false)
	if d.Action != ActionSkip {
		t.Errorf("距上次仅 12 分钟应跳过，实际 %s", d.Action)
	}

	// 31 分钟前执行过 → 放行
	ok := now.Add(-31 * time.Minute)
	task.LastRunAt = &ok
	d = Decide(task, now, false)
	if d.Action != ActionRun {
		t.Errorf("距上次 31 分钟应执行，实际 %s（%s）", d.Action, d.Reason)
	}
}

// TestDecideDetectsClockRollback 验证系统时钟回拨时不执行。
//
// 时钟异常时执行重启，之后无法判断「上次执行到底发生在什么时候」，
// 可能造成反复重启。
func TestDecideDetectsClockRollback(t *testing.T) {
	loc := mustLoad(t, "Asia/Shanghai")
	now := time.Date(2026, 10, 26, 12, 0, 0, 0, loc)

	future := now.Add(2 * time.Hour)
	task := newTask(model.Recurrence{Kind: model.RecurDaily, TimeOfDay: "04:30"})
	task.LastRunAt = &future

	d := Decide(task, now, false)
	if d.Action != ActionSkip {
		t.Errorf("时钟回拨时应跳过，实际 %s", d.Action)
	}
}

// TestDecideDisablesAfterRepeatedFailures 验证连续失败熔断。
func TestDecideDisablesAfterRepeatedFailures(t *testing.T) {
	task := newTask(model.Recurrence{Kind: model.RecurDaily, TimeOfDay: "04:30"})
	task.FailCount = model.MaxConsecutiveFailures

	d := Decide(task, time.Now(), false)
	if d.Action != ActionDisable {
		t.Errorf("连续失败 %d 次应自动禁用，实际 %s", task.FailCount, d.Action)
	}
}

// TestDecideAllowsAfterPartialFailures 验证未达阈值的失败仍允许执行。
func TestDecideAllowsAfterPartialFailures(t *testing.T) {
	task := newTask(model.Recurrence{Kind: model.RecurDaily, TimeOfDay: "04:30"})
	task.FailCount = model.MaxConsecutiveFailures - 1

	d := Decide(task, time.Now(), false)
	if d.Action != ActionRun {
		t.Errorf("未达熔断阈值应继续执行，实际 %s", d.Action)
	}
}

// TestEvaluateMissedNotMissed 验证未过期时不算错过。
func TestEvaluateMissedNotMissed(t *testing.T) {
	loc := mustLoad(t, "Asia/Shanghai")
	now := time.Date(2026, 10, 26, 3, 0, 0, 0, loc)
	next := now.Add(time.Hour)

	task := newTask(model.Recurrence{Kind: model.RecurDaily, TimeOfDay: "04:30"})
	task.NextRunAt = &next

	res := EvaluateMissed(task, now)
	if res.Missed {
		t.Error("尚未到期不应算作错过")
	}
}

// TestEvaluateMissedNoCatchUpByDefault 验证默认策略不补执行。
//
// 这是安全默认值：关机三天后开机立刻重启，对用户是意外行为。
func TestEvaluateMissedNoCatchUpByDefault(t *testing.T) {
	loc := mustLoad(t, "Asia/Shanghai")
	now := time.Date(2026, 10, 26, 8, 0, 0, 0, loc)
	missed := time.Date(2026, 10, 26, 4, 30, 0, 0, loc)

	task := newTask(model.Recurrence{Kind: model.RecurDaily, TimeOfDay: "04:30"})
	task.NextRunAt = &missed

	res := EvaluateMissed(task, now)
	if !res.Missed {
		t.Fatal("应识别为错过")
	}
	if res.ShouldRun {
		t.Error("默认策略下不应补执行")
	}
	if res.Reason == "" {
		t.Error("应说明为什么不补执行")
	}
}

// TestEvaluateMissedWithinWindow 验证窗口内补执行。
func TestEvaluateMissedWithinWindow(t *testing.T) {
	loc := mustLoad(t, "Asia/Shanghai")
	now := time.Date(2026, 10, 26, 6, 0, 0, 0, loc)
	missed := time.Date(2026, 10, 26, 4, 30, 0, 0, loc) // 延迟 1.5 小时

	task := newTask(model.Recurrence{Kind: model.RecurDaily, TimeOfDay: "04:30"})
	task.CatchUp = model.CatchUpWithinWindow
	task.CatchUpWindowSeconds = int(model.DefaultCatchUpWindow.Seconds())
	task.NextRunAt = &missed

	res := EvaluateMissed(task, now)
	if !res.ShouldRun {
		t.Fatalf("窗口内应补执行，原因: %s", res.Reason)
	}
	if !res.ScheduledFor.Equal(missed) {
		t.Errorf("补执行的计划时刻应为 %s，实际 %s", missed, res.ScheduledFor)
	}
}

// TestEvaluateMissedOutsideWindow 验证超出窗口不补执行。
//
// 关机一周的场景：即使开启了补执行，也不应在开机瞬间重启。
func TestEvaluateMissedOutsideWindow(t *testing.T) {
	loc := mustLoad(t, "Asia/Shanghai")
	now := time.Date(2026, 10, 26, 20, 0, 0, 0, loc)
	missed := time.Date(2026, 10, 26, 4, 30, 0, 0, loc) // 延迟 15.5 小时

	task := newTask(model.Recurrence{Kind: model.RecurDaily, TimeOfDay: "04:30"})
	task.CatchUp = model.CatchUpWithinWindow
	task.CatchUpWindowSeconds = int(model.DefaultCatchUpWindow.Seconds())
	task.NextRunAt = &missed

	res := EvaluateMissed(task, now)
	if res.ShouldRun {
		t.Error("超出补执行窗口不应执行")
	}
	if res.Reason == "" {
		t.Error("应说明超出窗口")
	}
}

// TestEvaluateMissedCountsOccurrences 验证错过次数的统计。
func TestEvaluateMissedCountsOccurrences(t *testing.T) {
	loc := mustLoad(t, "Asia/Shanghai")
	// 10-23 04:30 之后一直没执行，直到 10-26 06:00 才检查
	firstMissed := time.Date(2026, 10, 23, 4, 30, 0, 0, loc)
	now := time.Date(2026, 10, 26, 6, 0, 0, 0, loc)

	task := newTask(model.Recurrence{Kind: model.RecurDaily, TimeOfDay: "04:30"})
	task.NextRunAt = &firstMissed

	res := EvaluateMissed(task, now)
	if !res.Missed {
		t.Fatal("应识别为错过")
	}
	// 10-24、10-25、10-26 三次计划时刻落在区间内
	if res.Times < 3 {
		t.Errorf("应统计出至少 3 次错过，实际 %d", res.Times)
	}
}

// TestOccurrencesBetweenDaily 验证区间内时刻列举。
func TestOccurrencesBetweenDaily(t *testing.T) {
	loc := mustLoad(t, "Asia/Shanghai")
	task := newTask(model.Recurrence{Kind: model.RecurDaily, TimeOfDay: "04:30"})

	from := time.Date(2026, 10, 23, 12, 0, 0, 0, loc)
	to := time.Date(2026, 10, 26, 12, 0, 0, 0, loc)

	got := OccurrencesBetween(task, from, to)
	if len(got) != 3 {
		t.Fatalf("应得到 3 个时刻，实际 %d 个：%v", len(got), got)
	}
	wantDays := []int{24, 25, 26}
	for i, d := range wantDays {
		if got[i].Day() != d {
			t.Errorf("第 %d 个应为 10-%02d，实际 %s", i+1, d, got[i])
		}
	}
}

// TestOccurrencesBetweenWeeklyFiltersDays 验证每周任务只列出生效日。
func TestOccurrencesBetweenWeeklyFiltersDays(t *testing.T) {
	loc := mustLoad(t, "Asia/Shanghai")
	task := newTask(model.Recurrence{
		Kind:      model.RecurWeekly,
		TimeOfDay: "03:00",
		Weekdays:  []int{int(time.Monday)},
	})

	// 2026-10-19 至 2026-11-02，应包含 10-19、10-26、11-02 三个周一
	from := time.Date(2026, 10, 18, 0, 0, 0, 0, loc)
	to := time.Date(2026, 11, 2, 12, 0, 0, 0, loc)

	got := OccurrencesBetween(task, from, to)
	if len(got) != 3 {
		t.Fatalf("应得到 3 个周一，实际 %d 个：%v", len(got), got)
	}
	for _, o := range got {
		if o.Weekday() != time.Monday {
			t.Errorf("%s 不是周一", o)
		}
	}
}

// TestNextRebootAllowedAt 验证最早可重启时刻的计算。
func TestNextRebootAllowedAt(t *testing.T) {
	task := newTask(model.Recurrence{Kind: model.RecurDaily, TimeOfDay: "04:30"})

	if got := NextRebootAllowedAt(task); got != nil {
		t.Error("从未执行过时应返回 nil")
	}

	loc := mustLoad(t, "Asia/Shanghai")
	last := time.Date(2026, 10, 26, 12, 0, 0, 0, loc)
	task.LastRunAt = &last

	got := NextRebootAllowedAt(task)
	if got == nil {
		t.Fatal("有执行记录时应返回时刻")
	}
	if want := last.Add(30 * time.Minute); !got.Equal(want) {
		t.Errorf("期望 %s，实际 %s", want, *got)
	}
}

// TestHumanDuration 验证时长格式化的中文输出。
func TestHumanDuration(t *testing.T) {
	cases := []struct {
		d    time.Duration
		want string
	}{
		{30 * time.Second, "30 秒"},
		{5 * time.Minute, "5 分钟"},
		{2 * time.Hour, "2 小时"},
		{90 * time.Minute, "1 小时 30 分钟"},
		{50 * time.Hour, "2 天 2 小时"},
		{48 * time.Hour, "2 天"},
	}
	for _, c := range cases {
		if got := humanDuration(c.d); got != c.want {
			t.Errorf("%v 期望 %q，实际 %q", c.d, c.want, got)
		}
	}
}

// TestRecurrenceValidateRejectsBadInput 验证表达式的非法输入被拒绝。
//
// 这些是「宁可拒绝也不猜」的例子：时刻格式只有一种写法，
// 非法组合在类型层就表达不出来。
func TestRecurrenceValidateRejectsBadInput(t *testing.T) {
	cases := []struct {
		name string
		rec  model.Recurrence
	}{
		{"空类型", model.Recurrence{}},
		{"未知类型", model.Recurrence{Kind: "cron"}},
		{"时刻格式缺冒号", model.Recurrence{Kind: model.RecurDaily, TimeOfDay: "0430"}},
		{"时刻格式单数字小时", model.Recurrence{Kind: model.RecurDaily, TimeOfDay: "4:30"}},
		{"小时越界", model.Recurrence{Kind: model.RecurDaily, TimeOfDay: "24:00"}},
		{"分钟越界", model.Recurrence{Kind: model.RecurDaily, TimeOfDay: "04:60"}},
		{"每周未选星期", model.Recurrence{Kind: model.RecurWeekly, TimeOfDay: "04:30"}},
		{"星期越界", model.Recurrence{Kind: model.RecurWeekly, TimeOfDay: "04:30", Weekdays: []int{7}}},
		{"星期重复", model.Recurrence{Kind: model.RecurWeekly, TimeOfDay: "04:30", Weekdays: []int{1, 1}}},
		{"间隔为 0", model.Recurrence{Kind: model.RecurIntervalHours, IntervalHours: 0}},
		{"间隔超上限", model.Recurrence{Kind: model.RecurIntervalHours, IntervalHours: model.MaxIntervalHours + 1}},
		{"单次未指定时间", model.Recurrence{Kind: model.RecurOnce}},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if err := c.rec.Validate(); err == nil {
				t.Errorf("应拒绝 %+v", c.rec)
			}
		})
	}
}

// TestRecurrenceValidateAcceptsGoodInput 验证合法表达式通过。
func TestRecurrenceValidateAcceptsGoodInput(t *testing.T) {
	at := time.Date(2026, 10, 27, 4, 0, 0, 0, time.UTC)
	cases := []model.Recurrence{
		{Kind: model.RecurDaily, TimeOfDay: "00:00"},
		{Kind: model.RecurDaily, TimeOfDay: "23:59"},
		{Kind: model.RecurWeekly, TimeOfDay: "04:30", Weekdays: []int{0, 1, 2, 3, 4, 5, 6}},
		{Kind: model.RecurIntervalHours, IntervalHours: 1},
		{Kind: model.RecurIntervalHours, IntervalHours: model.MaxIntervalHours},
		{Kind: model.RecurOnce, OnceAt: &at},
	}
	for _, r := range cases {
		if err := r.Validate(); err != nil {
			t.Errorf("%+v 应通过校验，实际 %v", r, err)
		}
	}
}

// TestTaskValidateRejectsShortInterval 验证最短重启间隔是硬性下限。
//
// 这不是「建议值」：允许设置更短间隔等于给循环重启开绿灯。
func TestTaskValidateRejectsShortInterval(t *testing.T) {
	task := newTask(model.Recurrence{Kind: model.RecurDaily, TimeOfDay: "04:30"})
	task.MinIntervalSeconds = 60 // 1 分钟

	if err := task.Validate(); err == nil {
		t.Error("应拒绝低于 30 分钟的最短间隔")
	}
}

// TestTaskValidateRejectsBadTimezone 验证时区名校验。
func TestTaskValidateRejectsBadTimezone(t *testing.T) {
	task := newTask(model.Recurrence{Kind: model.RecurDaily, TimeOfDay: "04:30"})
	task.Timezone = "Mars/Olympus"

	if err := task.Validate(); err == nil {
		t.Error("应拒绝无效时区")
	}
}

// TestTaskValidateCatchUp 验证补执行策略的边界。
func TestTaskValidateCatchUp(t *testing.T) {
	base := newTask(model.Recurrence{Kind: model.RecurDaily, TimeOfDay: "04:30"})

	// 未设窗口却开了补执行 → 拒绝
	bad := base
	bad.CatchUp = model.CatchUpWithinWindow
	if err := bad.Validate(); err == nil {
		t.Error("开启补执行但未设窗口应被拒绝")
	}

	// 窗口超过 7 天 → 拒绝
	tooLong := base
	tooLong.CatchUp = model.CatchUpWithinWindow
	tooLong.CatchUpWindowSeconds = int(8 * 24 * time.Hour.Seconds())
	if err := tooLong.Validate(); err == nil {
		t.Error("补执行窗口超过 7 天应被拒绝")
	}

	// 合理窗口 → 通过
	okCase := base
	okCase.CatchUp = model.CatchUpWithinWindow
	okCase.CatchUpWindowSeconds = int(6 * time.Hour.Seconds())
	if err := okCase.Validate(); err != nil {
		t.Errorf("合理配置应通过，实际 %v", err)
	}
}

// TestRecurrenceDescribe 验证中文描述。
func TestRecurrenceDescribe(t *testing.T) {
	cases := []struct {
		rec  model.Recurrence
		want string
	}{
		{model.Recurrence{Kind: model.RecurDaily, TimeOfDay: "04:30"}, "每天 04:30"},
		{
			model.Recurrence{Kind: model.RecurWeekly, TimeOfDay: "03:00", Weekdays: []int{1, 4}},
			"每周一、周四 03:00",
		},
		{model.Recurrence{Kind: model.RecurIntervalHours, IntervalHours: 6}, "每 6 小时"},
		{model.Recurrence{Kind: model.RecurIntervalHours, IntervalHours: 48}, "每 2 天"},
	}
	for _, c := range cases {
		if got := c.rec.Describe(); got != c.want {
			t.Errorf("期望 %q，实际 %q", c.want, got)
		}
	}
}
