package model

import (
	"fmt"
	"strings"
	"time"
)

// RecurrenceKind 是递归表达式的类型。
//
// 刻意不用 cron 字符串：cron 有 5~7 个字段、多种方言，解析歧义多
// （`0 4 * * *` 是每天 4 点还是每小时？取决于实现），而用户真正需要的
// 只有「每天几点」「每周几几点」「每隔几小时」三种。结构化字段让非法
// 组合在类型层就表达不出来，校验也更精确。
type RecurrenceKind string

const (
	RecurDaily         RecurrenceKind = "daily"          // 每天 HH:MM
	RecurWeekly        RecurrenceKind = "weekly"         // 每周指定星期几的 HH:MM
	RecurIntervalHours RecurrenceKind = "interval_hours" // 每隔 N 小时
	RecurOnce          RecurrenceKind = "once"           // 仅在指定时刻执行一次
)

// catchUp 策略：错过计划时刻后是否补执行。
type CatchUpPolicy string

const (
	// CatchUpNever 错过就跳过，只记录一条 skipped。默认值。
	//
	// 为什么默认不补执行：重启/关机是破坏性操作。机器关机三天后开机
	// 立刻重启，对用户是意外行为。要补执行必须显式开启并设置容忍窗口。
	CatchUpNever CatchUpPolicy = "never"
	// CatchUpWithinWindow 在容忍窗口内补执行一次。
	CatchUpWithinWindow CatchUpPolicy = "within_window"
)

// 任务类型。
const (
	TaskKindReboot = "reboot"
)

// 常量：调度相关的上限与默认值。
const (
	// DefaultMinRebootInterval 是两次重启之间的最短间隔。
	//
	// 30 分钟来自 PLAN.md 5.3 的建议值。它的作用不是「礼貌」而是
	// 安全阀：若规则配置错误导致任务被频繁触发（例如每小时的表达式
	// 被误设成每分钟），这个下限能把「无限重启」变成「每 30 分钟一次」，
	// 用户仍有窗口通过 SSH 或本地终端介入。
	DefaultMinRebootInterval = 30 * time.Minute

	// DefaultCatchUpWindow 是补执行的默认容忍窗口。
	// 超过这个时长仍未执行，就放弃本次并记录 skipped。
	DefaultCatchUpWindow = 6 * time.Hour

	// MaxIntervalHours 限制「每隔 N 小时」的上界（一年）。
	MaxIntervalHours = 8760

	// MaxConsecutiveFailures 是连续失败多少次后自动禁用任务。
	//
	// 触发条件通常是环境问题（无 root、配置损坏）。继续重试只会
	// 在系统日志里刷错误，且如果是重启任务，可能造成反复重启尝试。
	MaxConsecutiveFailures = 3
)

// Recurrence 是结构化的递归表达式。
type Recurrence struct {
	Kind RecurrenceKind `json:"kind"`

	// TimeOfDay 是 "HH:MM"（24 小时制），daily/weekly 必填。
	// 用字符串而非 time.Time：它表达的是「墙上时间」，没有日期与时区，
	// 存成时间戳会引入「用哪个日期」这个无意义的问题。
	TimeOfDay string `json:"timeOfDay,omitempty"`

	// Weekdays 是 weekly 的生效星期，0=周日。
	Weekdays []int `json:"weekdays,omitempty"`

	// IntervalHours 是 interval_hours 的间隔小时数。
	IntervalHours int `json:"intervalHours,omitempty"`

	// OnceAt 是 once 的唯一执行时刻（绝对时间，带时区）。
	OnceAt *time.Time `json:"onceAt,omitempty"`
}

// IsZero 判断表达式是否为空。
func (r Recurrence) IsZero() bool {
	return r.Kind == ""
}

// Validate 校验表达式自身的合法性。
//
// 只校验「表达式说得通」，不校验业务上限（如最短重启间隔）——
// 那属于调度策略，在 schedule 包里判定。
func (r Recurrence) Validate() error {
	switch r.Kind {
	case RecurDaily:
		if _, _, err := ParseTimeOfDay(r.TimeOfDay); err != nil {
			return err
		}
		return nil

	case RecurWeekly:
		if _, _, err := ParseTimeOfDay(r.TimeOfDay); err != nil {
			return err
		}
		if len(r.Weekdays) == 0 {
			return NewError(CodeInvalidArgument, "每周执行必须至少选择一天")
		}
		seen := make(map[int]bool, len(r.Weekdays))
		for _, d := range r.Weekdays {
			if d < 0 || d > 6 {
				return NewError(CodeInvalidArgument, "星期取值必须是 0（周日）到 6（周六），实际 %d", d)
			}
			if seen[d] {
				return NewError(CodeInvalidArgument, "星期 %d 重复", d)
			}
			seen[d] = true
		}
		return nil

	case RecurIntervalHours:
		if r.IntervalHours < 1 {
			return NewError(CodeInvalidArgument, "间隔小时数必须至少为 1")
		}
		if r.IntervalHours > MaxIntervalHours {
			return NewError(CodeInvalidArgument, "间隔小时数不能超过 %d（一年）", MaxIntervalHours)
		}
		return nil

	case RecurOnce:
		if r.OnceAt == nil {
			return NewError(CodeInvalidArgument, "单次执行必须指定时间")
		}
		if r.OnceAt.IsZero() {
			return NewError(CodeInvalidArgument, "单次执行时间不能是零值")
		}
		return nil

	case "":
		return NewError(CodeInvalidArgument, "必须指定执行周期")

	default:
		return NewError(CodeInvalidArgument, "未知的执行周期类型 %q", string(r.Kind))
	}
}

// ParseTimeOfDay 解析 "HH:MM" 形式的时刻。
//
// 刻意不接受 "4:30"、"04:30:00"、"24:00" 等变体：
// 时刻格式只有一种写法，就不存在「用户以为设了 4:30，实际解析成别的东西」
// 这类静默错误。
func ParseTimeOfDay(s string) (hour, minute int, err error) {
	if len(s) != 5 || s[2] != ':' {
		return 0, 0, NewError(CodeInvalidArgument, "时刻格式必须是 HH:MM（例如 04:30），实际 %q", s)
	}
	if _, err := fmt.Sscanf(s, "%02d:%02d", &hour, &minute); err != nil {
		return 0, 0, NewError(CodeInvalidArgument, "时刻 %q 无法解析", s)
	}
	if hour < 0 || hour > 23 {
		return 0, 0, NewError(CodeInvalidArgument, "小时必须在 00-23 之间，实际 %d", hour)
	}
	if minute < 0 || minute > 59 {
		return 0, 0, NewError(CodeInvalidArgument, "分钟必须在 00-59 之间，实际 %d", minute)
	}
	return hour, minute, nil
}

// FormatTimeOfDay 把时分格式化为 "HH:MM"。
func FormatTimeOfDay(hour, minute int) string {
	return fmt.Sprintf("%02d:%02d", hour, minute)
}

// 星期名称（周日在前，与 time.Weekday 一致）。
var weekdayNames = [...]string{"周日", "周一", "周二", "周三", "周四", "周五", "周六"}

// WeekdayName 返回中文星期名。
func WeekdayName(d int) string {
	if d < 0 || d > 6 {
		return "未知"
	}
	return weekdayNames[d]
}

// Describe 生成人类可读的中文描述，用于界面与审计记录。
func (r Recurrence) Describe() string {
	switch r.Kind {
	case RecurDaily:
		h, m, err := ParseTimeOfDay(r.TimeOfDay)
		if err != nil {
			return "每天（时刻无效）"
		}
		return fmt.Sprintf("每天 %s", FormatTimeOfDay(h, m))

	case RecurWeekly:
		h, m, err := ParseTimeOfDay(r.TimeOfDay)
		if err != nil {
			return "每周（时刻无效）"
		}
		names := make([]string, 0, len(r.Weekdays))
		for _, d := range r.Weekdays {
			names = append(names, WeekdayName(d))
		}
		return fmt.Sprintf("每%s %s", strings.Join(names, "、"), FormatTimeOfDay(h, m))

	case RecurIntervalHours:
		if r.IntervalHours%24 == 0 && r.IntervalHours >= 24 {
			return fmt.Sprintf("每 %d 天", r.IntervalHours/24)
		}
		return fmt.Sprintf("每 %d 小时", r.IntervalHours)

	case RecurOnce:
		if r.OnceAt == nil {
			return "单次（未指定时间）"
		}
		return fmt.Sprintf("单次 %s", r.OnceAt.Format("2006-01-02 15:04"))

	default:
		return "未设置执行周期"
	}
}

// ScheduledTask 是一条计划任务。
type ScheduledTask struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`
	Kind string `json:"kind"`

	// Recurrence 是执行周期表达式。
	Recurrence Recurrence `json:"recurrence"`

	// Timezone 是计算墙上时间所用的时区名（IANA，如 Asia/Shanghai）。
	//
	// 必须显式存储而不是用系统默认：用户改了系统时区后，
	// 「每天 04:30 重启」应该按新时区解释还是保持原时区？
	// 显式存储让语义确定——按任务自己的时区执行，界面上也能提示不一致。
	Timezone string `json:"timezone"`

	Enabled bool `json:"enabled"`

	// CatchUp 决定错过计划时刻后是否补执行。
	CatchUp CatchUpPolicy `json:"catchUp"`

	// CatchUpWindowSeconds 是补执行的容忍窗口（仅 within_window 有效）。
	CatchUpWindowSeconds int `json:"catchUpWindowSeconds"`

	// MinIntervalSeconds 是两次执行之间的最短间隔。
	MinIntervalSeconds int `json:"minIntervalSeconds"`

	LastRunAt  *time.Time `json:"lastRunAt,omitempty"`
	LastResult string     `json:"lastResult,omitempty"`
	LastError  string     `json:"lastError,omitempty"`
	NextRunAt  *time.Time `json:"nextRunAt,omitempty"`
	FailCount  int        `json:"failCount"`
	CreatedAt  time.Time  `json:"createdAt"`
	UpdatedAt  time.Time  `json:"updatedAt"`

	// DisabledReason 记录被自动禁用的原因（如连续失败熔断）。
	DisabledReason string `json:"disabledReason,omitempty"`

	// LauncherLabel 是已安装的 launchd 任务标签，空串表示未安装。
	//
	// 记下来是为了能精确卸载：卸载时必须用当初安装的那个标签，
	// 而不是按「应该是叫什么」去猜，否则会留下孤儿 launchd 任务。
	LauncherLabel string `json:"launcherLabel,omitempty"`
}

// Validate 校验任务定义。
//
// 业务上限（最短重启间隔）也在这里检查：它是安全不变量，不该等到
// 执行时才拒绝。用户可以设更长的间隔，但不能设更短的。
func (t ScheduledTask) Validate() error {
	if strings.TrimSpace(t.Name) == "" {
		return NewError(CodeInvalidArgument, "任务名称不能为空")
	}
	if len(t.Name) > 64 {
		return NewError(CodeInvalidArgument, "任务名称不能超过 64 个字符")
	}
	if t.Kind != TaskKindReboot {
		return NewError(CodeInvalidArgument, "不支持的任务类型 %q", t.Kind)
	}
	if err := t.Recurrence.Validate(); err != nil {
		return err
	}
	if t.Timezone == "" {
		return NewError(CodeInvalidArgument, "必须指定时区")
	}
	if _, err := time.LoadLocation(t.Timezone); err != nil {
		return NewError(CodeInvalidArgument, "时区 %q 无效", t.Timezone)
	}

	if t.MinIntervalSeconds < int(DefaultMinRebootInterval.Seconds()) {
		return NewError(CodeInvalidArgument,
			"最短执行间隔不能小于 %d 分钟（防止循环重启）",
			int(DefaultMinRebootInterval.Minutes()))
	}
	if t.MinIntervalSeconds > int(365*24*time.Hour.Seconds()) {
		return NewError(CodeInvalidArgument, "最短执行间隔不能超过一年")
	}

	switch t.CatchUp {
	case "", CatchUpNever:
		// 空值按 never 处理（数据库里的默认值可能是空串）
	case CatchUpWithinWindow:
		if t.CatchUpWindowSeconds <= 0 {
			return NewError(CodeInvalidArgument, "开启补执行时必须设置容忍窗口")
		}
		// 容忍窗口不能超过 7 天：否则关机一周后开机立刻重启，
		// 对用户是意外行为。
		if t.CatchUpWindowSeconds > int(7*24*time.Hour.Seconds()) {
			return NewError(CodeInvalidArgument, "补执行容忍窗口不能超过 7 天")
		}
	default:
		return NewError(CodeInvalidArgument, "未知的补执行策略 %q", string(t.CatchUp))
	}

	return nil
}

// CatchUpWindow 返回容忍窗口时长，never 时为 0。
func (t ScheduledTask) CatchUpWindow() time.Duration {
	if t.CatchUp != CatchUpWithinWindow {
		return 0
	}
	return time.Duration(t.CatchUpWindowSeconds) * time.Second
}

// MinInterval 返回最短执行间隔。
func (t ScheduledTask) MinInterval() time.Duration {
	if t.MinIntervalSeconds <= 0 {
		return DefaultMinRebootInterval
	}
	return time.Duration(t.MinIntervalSeconds) * time.Second
}

// Location 返回任务的时区，失败时回落到 UTC（不 panic：
// 时区数据在极少数精简系统上可能缺失）。
func (t ScheduledTask) Location() *time.Location {
	loc, err := time.LoadLocation(t.Timezone)
	if err != nil {
		return time.UTC
	}
	return loc
}

// TaskExecution 是一次执行记录。
type TaskExecution struct {
	ID         int64      `json:"id"`
	TaskID     int64      `json:"taskId"`
	TaskName   string     `json:"taskName,omitempty"`
	StartedAt  time.Time  `json:"startedAt"`
	FinishedAt *time.Time `json:"finishedAt,omitempty"`
	// Result 取值：running / success / failed / skipped
	Result string `json:"result"`
	// Detail 是给人看的说明（例如「距上次重启仅 12 分钟，低于 30 分钟下限」）。
	Detail string `json:"detail,omitempty"`
}

// 执行结果取值。
const (
	ExecutionRunning = "running"
	ExecutionSuccess = "success"
	ExecutionFailed  = "failed"
	ExecutionSkipped = "skipped"
)

// PendingReboot 表示一个已排定但尚未执行的重启。
//
// 之所以要持久化：延迟重启的倒计时必须能在 Agent 崩溃或系统重启后
// 恢复。放内存里会让「30 分钟后重启」在 Agent 重启后静默消失，
// 用户以为已经排定，实际什么都没发生。
type PendingReboot struct {
	// ScheduledAt 是计划的绝对执行时刻。
	ScheduledAt time.Time `json:"scheduledAt"`
	// Reason 是发起原因（审计与界面展示）。
	Reason string `json:"reason"`
	// RequestedBy 是发起者身份（本机用户名或远程地址）。
	RequestedBy string `json:"requestedBy"`
	// CreatedAt 是排定时刻。
	CreatedAt time.Time `json:"createdAt"`
	// TaskID 非 0 表示由计划任务触发，0 表示手动发起。
	TaskID int64 `json:"taskId,omitempty"`
}

// PowerStatus 是电源与调度能力的当前状态。
type PowerStatus struct {
	// Pending 是待执行的重启，nil 表示没有。
	Pending *PendingReboot `json:"pending,omitempty"`
	// CanReboot 表示当前进程是否有能力真正执行重启。
	//
	// 非 root 时无法调用 reboot/shutdown，此时界面必须显式说明
	// 「已排定但不会生效」，而不是让用户以为设置成功。
	CanReboot bool `json:"canReboot"`
	// RebootUnavailableReason 说明为什么不能重启。
	RebootUnavailableReason string `json:"rebootUnavailableReason,omitempty"`
	// LastRebootAt 是上一次重启时间（尽力而为，可能读不到）。
	LastRebootAt *time.Time `json:"lastRebootAt,omitempty"`
	// Uptime 是当前系统运行时长。
	UptimeSeconds int64 `json:"uptimeSeconds"`
	// Timezone 是系统时区名。
	Timezone string `json:"timezone"`
	// ScheduleDaemonInstalled 表示 launchd 调度器是否已安装。
	ScheduleDaemonInstalled bool `json:"scheduleDaemonInstalled"`
}
