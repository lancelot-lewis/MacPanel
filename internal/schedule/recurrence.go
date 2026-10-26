// Package schedule 实现计划任务的执行时刻计算与执行决策。
//
// 本包是纯逻辑：不读写文件、不调用系统命令、不看当前进程权限，
// 因此可以用注入的时间点完整测试所有边界（夏令时、跨天、错过执行）。
//
// 设计原则：
//   - 时间计算必须显式给定时区，绝不依赖 time.Local 的隐式默认值；
//   - 任何「跳过」都要给出可读原因，因为它会进审计和界面；
//   - 所有上限都是硬编码的安全不变量，不通过配置放宽。
package schedule

import (
	"fmt"
	"time"

	"github.com/lancelot-lewis/MacPanel/internal/model"
)

// maxSearchDays 是搜索下次执行时刻的最大天数。
//
// 15 天足以覆盖：weekly 最多等 7 天，daily 最多等 1 天，
// 加上夏令时跳变可能导致某天时刻不存在。超过这个值仍未找到
// 说明表达式本身有问题，不再无限搜索。
const maxSearchDays = 15

// NextAfter 计算严格晚于 now 的下一个执行时刻。
//
// 返回 false 表示没有下次（仅 once 在执行过之后会这样）。
// 时区取自 task.Timezone：改系统时区不影响已存在的任务语义，
// 这是显式存储时区的目的。
func NextAfter(task model.ScheduledTask, now time.Time) (time.Time, bool) {
	loc := task.Location()
	r := task.Recurrence

	switch r.Kind {
	case model.RecurDaily:
		hour, minute, err := model.ParseTimeOfDay(r.TimeOfDay)
		if err != nil {
			return time.Time{}, false
		}
		return nextDaily(now, loc, hour, minute)

	case model.RecurWeekly:
		hour, minute, err := model.ParseTimeOfDay(r.TimeOfDay)
		if err != nil {
			return time.Time{}, false
		}
		return nextWeekly(now, loc, hour, minute, r.Weekdays)

	case model.RecurIntervalHours:
		return nextInterval(now, task.LastRunAt, r.IntervalHours)

	case model.RecurOnce:
		if r.OnceAt == nil || r.OnceAt.IsZero() {
			return time.Time{}, false
		}
		at := r.OnceAt.In(loc)
		if at.After(now) {
			return at, true
		}
		return time.Time{}, false

	default:
		return time.Time{}, false
	}
}

// nextDaily 找下一个「每天 HH:MM」。
//
// DST 处理：春季跳变会让某些墙钟时刻（如 02:30）在某天不存在。
// time.Date 会把不存在的时刻归一化到相邻时间，这里通过比对
// Hour/Minute 是否被改动来识别这种情况，并跳过当天。
// 若强行执行归一化后的时刻，用户会看到「设了 02:30，实际 03:30 跑的」。
func nextDaily(now time.Time, loc *time.Location, hour, minute int) (time.Time, bool) {
	base := now.In(loc)
	y, m, d := base.Date()

	for i := 0; i < maxSearchDays; i++ {
		cand := time.Date(y, m, d+i, hour, minute, 0, 0, loc)
		if cand.Hour() != hour || cand.Minute() != minute {
			continue // 该墙钟时刻在当天不存在（DST 跳变），跳过
		}
		if cand.After(now) {
			return cand, true
		}
	}
	return time.Time{}, false
}

// nextWeekly 找下一个「每周指定星期几 HH:MM」。
func nextWeekly(now time.Time, loc *time.Location, hour, minute int, weekdays []int) (time.Time, bool) {
	allowed := make(map[time.Weekday]bool, len(weekdays))
	for _, d := range weekdays {
		allowed[time.Weekday(d)] = true
	}
	if len(allowed) == 0 {
		return time.Time{}, false
	}

	base := now.In(loc)
	y, m, d := base.Date()

	for i := 0; i < maxSearchDays; i++ {
		cand := time.Date(y, m, d+i, hour, minute, 0, 0, loc)
		if cand.Hour() != hour || cand.Minute() != minute {
			continue
		}
		if !allowed[cand.Weekday()] {
			continue
		}
		if cand.After(now) {
			return cand, true
		}
	}
	return time.Time{}, false
}

// nextInterval 找下一个「每隔 N 小时」。
//
// 基准是上次执行时间而不是「整点对齐」：从上次执行 +N 小时，
// 才是「间隔 N 小时」的真实语义。若以整点对齐，用户看到的是
// 「每 3 小时」，实际执行时刻却会随首次执行时间漂移。
//
// 没有历史执行记录时以 now 为基准，即首次执行在 N 小时后。
func nextInterval(now time.Time, lastRun *time.Time, hours int) (time.Time, bool) {
	if hours < 1 {
		return time.Time{}, false
	}
	step := time.Duration(hours) * time.Hour

	base := now
	if lastRun != nil && lastRun.Before(now) {
		base = *lastRun
	}

	// 向上取整到严格晚于 now 的第一个基准+N步。
	elapsed := now.Sub(base)
	steps := elapsed/step + 1
	next := base.Add(time.Duration(steps) * step)

	// 极端情况下（时钟回拨）可能算出过去时刻，兜底加一步。
	for !next.After(now) {
		next = next.Add(step)
	}
	return next, true
}

// OccurrencesBetween 返回 (from, to] 区间内所有计划执行时刻，按时间升序。
//
// 用于补执行判定与「这次错过了几次」的诊断。返回值有上限，
// 防止跨度过大时产生海量时间点。
func OccurrencesBetween(task model.ScheduledTask, from, to time.Time) []time.Time {
	loc := task.Location()
	r := task.Recurrence

	const maxOccurrences = 200
	var out []time.Time

	addIfInRange := func(t time.Time) bool {
		if t.After(from) && !t.After(to) {
			out = append(out, t)
			if len(out) >= maxOccurrences {
				return true
			}
		}
		return false
	}

	switch r.Kind {
	case model.RecurDaily, model.RecurWeekly:
		hour, minute, err := model.ParseTimeOfDay(r.TimeOfDay)
		if err != nil {
			return nil
		}
		var allowed map[time.Weekday]bool
		if r.Kind == model.RecurWeekly {
			allowed = make(map[time.Weekday]bool, len(r.Weekdays))
			for _, d := range r.Weekdays {
				allowed[time.Weekday(d)] = true
			}
		}

		start := from.In(loc)
		y, m, d := start.Date()
		days := int(to.Sub(from).Hours()/24) + 2
		if days > maxSearchDays*2 {
			days = maxSearchDays * 2
		}
		for i := 0; i <= days; i++ {
			cand := time.Date(y, m, d+i, hour, minute, 0, 0, loc)
			if cand.Hour() != hour || cand.Minute() != minute {
				continue
			}
			if allowed != nil && !allowed[cand.Weekday()] {
				continue
			}
			if addIfInRange(cand) {
				return out
			}
		}

	case model.RecurIntervalHours:
		if r.IntervalHours < 1 {
			return nil
		}
		step := time.Duration(r.IntervalHours) * time.Hour
		base := from
		if task.LastRunAt != nil && task.LastRunAt.Before(from) {
			base = *task.LastRunAt
		}
		for t := base.Add(step); !t.After(to); t = t.Add(step) {
			if addIfInRange(t) {
				return out
			}
		}

	case model.RecurOnce:
		if r.OnceAt != nil {
			addIfInRange(r.OnceAt.In(loc))
		}
	}

	return out
}

// DecisionAction 是执行决策的结果类型。
type DecisionAction string

const (
	// ActionRun 表示本次应当真正执行。
	ActionRun DecisionAction = "run"
	// ActionSkip 表示本次应当跳过，但仍要记录（说明为什么没执行）。
	ActionSkip DecisionAction = "skip"
	// ActionDisable 表示应当执行但被安全机制拦下，并自动禁用任务。
	ActionDisable DecisionAction = "disable"
)

// Decision 是一次执行决策。
type Decision struct {
	Action DecisionAction
	// Reason 是给人看的中文原因，会写入 task_executions.detail 与审计。
	Reason string
}

// Decide 判定「计划时刻已到」之后是否真的执行。
//
// 判定顺序本身就是安全设计，不要随意调换：
//  1. 禁用状态 —— 最简单的排除；
//  2. 已有待执行的重启 —— 多份待执行叠加会产生「排队重启」，
//     用户取消了一次却仍被重启；
//  3. 最短间隔 —— 防循环重启的最后一道闸；
//  4. 连续失败熔断 —— 环境问题导致的反复失败不应无限重试。
func Decide(task model.ScheduledTask, now time.Time, hasPendingReboot bool) Decision {
	if !task.Enabled {
		return Decision{
			Action: ActionSkip,
			Reason: "任务已禁用" + disabledSuffix(task.DisabledReason),
		}
	}

	if hasPendingReboot {
		return Decision{
			Action: ActionSkip,
			Reason: "已有待执行的重启，本次跳过（避免叠加）",
		}
	}

	// 最少间隔判定。LastRunAt 为空表示从未执行过，直接放行。
	if task.LastRunAt != nil {
		gap := now.Sub(*task.LastRunAt)
		if gap < 0 {
			// 系统时钟被回拨。此处不放行：时钟异常时执行重启，
			// 之后无法判断「上次执行到底发生在什么时候」。
			return Decision{
				Action: ActionSkip,
				Reason: "系统时钟异常（当前时间早于上次执行时间），暂不执行",
			}
		}
		if min := task.MinInterval(); gap < min {
			return Decision{
				Action: ActionSkip,
				Reason: fmt.Sprintf("距上次执行仅 %s，低于最短间隔 %s（防循环重启）",
					humanDuration(gap), humanDuration(min)),
			}
		}
	}

	// 连续失败熔断。
	if task.FailCount >= model.MaxConsecutiveFailures {
		return Decision{
			Action: ActionDisable,
			Reason: fmt.Sprintf("已连续失败 %d 次，自动禁用任务以避免反复失败",
				task.FailCount),
		}
	}

	return Decision{Action: ActionRun, Reason: "计划时刻已到"}
}

// disabledSuffix 拼接自动禁用的附加说明。
func disabledSuffix(reason string) string {
	if reason == "" {
		return ""
	}
	return "（" + reason + "）"
}

// MissedResult 描述「检查过期计划时刻」的结论。
type MissedResult struct {
	// Missed 表示确实存在已过期但未执行的计划时刻。
	Missed bool
	// ShouldRun 表示按补执行策略判定为「现在补执行」。
	ShouldRun bool
	// ScheduledFor 是那个被错过的计划时刻。
	ScheduledFor time.Time
	// Times 是窗口内错过的次数（用于诊断，例如睡眠多天错过了 3 次）。
	Times int
	// Reason 是结论说明。
	Reason string
}

// EvaluateMissed 判定过期的计划时刻该如何处理。
//
// 调用时机：Agent 启动后、以及每次定时唤醒时。系统睡眠期间
// Agent 不会被执行，醒来后 next_run_at 就可能停在过去。
//
// 这里的核心安全判断是「补执行的容忍窗口」：
// 关机一周后开机立刻重启，用户会认为这是故障。因此只有显式开启
// 补执行、且错过时长在窗口内时才补；否则跳过并把计划推到下一次。
func EvaluateMissed(task model.ScheduledTask, now time.Time) MissedResult {
	if task.NextRunAt == nil {
		return MissedResult{}
	}
	if !task.NextRunAt.Before(now) {
		return MissedResult{}
	}

	missedAt := *task.NextRunAt
	delay := now.Sub(missedAt)

	// 统计窗口内到底错过了几次，便于用户理解「为什么只补一次」。
	occurrences := OccurrencesBetween(task, missedAt.Add(-time.Second), now)
	times := len(occurrences)
	if times == 0 {
		times = 1
	}

	window := task.CatchUpWindow()
	if window <= 0 {
		return MissedResult{
			Missed:       true,
			ShouldRun:    false,
			ScheduledFor: missedAt,
			Times:        times,
			Reason: fmt.Sprintf("错过了 %s 的计划时刻（已延迟 %s），按策略不补执行",
				missedAt.Format("2006-01-02 15:04"), humanDuration(delay)),
		}
	}

	if delay > window {
		return MissedResult{
			Missed:       true,
			ShouldRun:    false,
			ScheduledFor: missedAt,
			Times:        times,
			Reason: fmt.Sprintf("错过了 %s 的计划时刻（已延迟 %s，超过补执行窗口 %s）",
				missedAt.Format("2006-01-02 15:04"), humanDuration(delay), humanDuration(window)),
		}
	}

	return MissedResult{
		Missed:       true,
		ShouldRun:    true,
		ScheduledFor: missedAt,
		Times:        times,
		Reason: fmt.Sprintf("补执行 %s 错过的计划（延迟 %s，在窗口 %s 内）",
			missedAt.Format("2006-01-02 15:04"), humanDuration(delay), humanDuration(window)),
	}
}

// NextRebootAllowedAt 计算「最早可以再次重启」的时刻。
//
// 用于界面提示：用户点重启时若距上次不足 30 分钟，
// 直接告诉他还要等多久，而不是给一个笼统的「不允许」。
func NextRebootAllowedAt(task model.ScheduledTask) *time.Time {
	if task.LastRunAt == nil {
		return nil
	}
	at := task.LastRunAt.Add(task.MinInterval())
	return &at
}

// humanDuration 把时长格式化为中文可读形式。
//
// 刻意不用 time.Duration.String()：它输出 "1h30m0s"，
// 在中文界面里读起来很别扭，而且"1.5 小时"比"90m0s"更直观。
func humanDuration(d time.Duration) string {
	if d < 0 {
		d = -d
	}
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%d 秒", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%d 分钟", int(d.Minutes()))
	case d < 24*time.Hour:
		h := int(d.Hours())
		m := int(d.Minutes()) % 60
		if m == 0 {
			return fmt.Sprintf("%d 小时", h)
		}
		return fmt.Sprintf("%d 小时 %d 分钟", h, m)
	default:
		days := int(d.Hours()) / 24
		h := int(d.Hours()) % 24
		if h == 0 {
			return fmt.Sprintf("%d 天", days)
		}
		return fmt.Sprintf("%d 天 %d 小时", days, h)
	}
}
