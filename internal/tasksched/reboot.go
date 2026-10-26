package tasksched

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/lancelot-lewis/MacPanel/internal/audit"
	"github.com/lancelot-lewis/MacPanel/internal/model"
	"github.com/lancelot-lewis/MacPanel/internal/schedule"
)

// ScheduleRequest 是一次重启排定请求。
type ScheduleRequest struct {
	// DelaySeconds 为 0 表示尽快重启。
	DelaySeconds int
	// Reason 是发起原因，写入审计与界面。
	Reason string
	// Actor 是发起者身份（用户名或远程地址）。
	Actor string
	// RemoteAddr 是远程地址（可空）。
	RemoteAddr string
	// RequestID 关联 HTTP 请求，便于日志串联。
	RequestID string
}

// 立即重启时的响应缓冲。
//
// 为什么不是 0：立即调用 shutdown 会让 HTTP 响应来不及写回浏览器，
// 用户看到的是「请求失败」，而实际上机器已经在重启——这比慢 2 秒糟得多。
const immediateRebootGrace = 2 * time.Second

// ErrNotPermitted 表示当前进程不具备重启能力。
var ErrNotPermitted = errors.New("当前进程不具备重启能力")

// ScheduleReboot 排定一次重启。
//
// 流程：校验 → 落盘（先持久化）→ 设定时器 → 审计。
//
// 为什么先落盘：定时器在内存里，Agent 崩溃后「已排定 30 分钟后重启」
// 会静默消失，用户以为设置成功，实际什么都没发生。
func (s *Scheduler) ScheduleReboot(ctx context.Context, req ScheduleRequest) (model.PowerStatus, error) {
	if req.DelaySeconds < 0 {
		return model.PowerStatus{}, model.NewError(model.CodeInvalidArgument, "延后时间不能为负")
	}
	// 与 execx 的 shutdownGuard 上限一致（+60 分钟）。
	if req.DelaySeconds > 60*60 {
		return model.PowerStatus{}, model.NewError(model.CodeInvalidArgument,
			"延后时间不能超过 60 分钟")
	}

	if ok, reason := s.power.Capability(); !ok {
		// 不当成「成功但不会生效」：那会让用户以为重启已排定。
		return model.PowerStatus{}, model.NewError(model.CodeUnavailable,
			"无法执行重启：%s", reason)
	}

	now := s.now()
	at := now.Add(time.Duration(req.DelaySeconds) * time.Second)
	if req.DelaySeconds == 0 {
		at = now.Add(immediateRebootGrace)
	}

	pending := model.PendingReboot{
		ScheduledAt: at,
		Reason:      req.Reason,
		RequestedBy: req.Actor,
		CreatedAt:   now,
	}

	s.mu.Lock()
	if s.pending != nil {
		existing := *s.pending
		s.mu.Unlock()
		return model.PowerStatus{}, model.NewError(model.CodeConflict,
			"已有待执行的重启（计划于 %s，原因：%s），请先取消",
			existing.ScheduledAt.Format("2006-01-02 15:04:05"), existing.Reason)
	}
	s.pending = &pending
	s.armTimerLocked(ctx, at)
	s.mu.Unlock()

	if err := s.store.SavePendingReboot(ctx, pending); err != nil {
		// 落盘失败 → 撤销内存状态，避免「界面上有、重启后消失」。
		s.mu.Lock()
		s.pending = nil
		if s.timer != nil {
			s.timer.Stop()
			s.timer = nil
		}
		s.mu.Unlock()
		return model.PowerStatus{}, fmt.Errorf("保存待执行重启失败: %w", err)
	}

	s.audit(ctx, audit.Entry{
		Actor:      req.Actor,
		Action:     "power.reboot.schedule",
		Target:     "system",
		Result:     audit.ResultSuccess,
		RemoteAddr: req.RemoteAddr,
		RequestID:  req.RequestID,
		Detail: map[string]any{
			"scheduledAt":  at.Format(time.RFC3339),
			"delaySeconds": req.DelaySeconds,
			"reason":       req.Reason,
		},
	})

	s.log.Info("已排定重启",
		"scheduledAt", at.Format(time.RFC3339),
		"delaySeconds", req.DelaySeconds,
		"actor", req.Actor)

	return s.Status(ctx)
}

// CancelReboot 取消待执行的重启。
func (s *Scheduler) CancelReboot(ctx context.Context, actor, remoteAddr, requestID string) (model.PowerStatus, error) {
	s.mu.Lock()
	had := s.pending != nil
	if s.timer != nil {
		s.timer.Stop()
		s.timer = nil
	}
	s.pending = nil
	s.mu.Unlock()

	if err := s.store.ClearPendingReboot(ctx); err != nil {
		return model.PowerStatus{}, fmt.Errorf("清除待执行重启失败: %w", err)
	}

	// 取消一个不存在的重启不算错误（幂等），但要如实记录，
	// 便于排查「为什么用户说取消了却还在重启」。
	result := audit.ResultSuccess
	detail := map[string]any{}
	if !had {
		detail["note"] = "调用时没有待执行的重启"
	}

	s.audit(ctx, audit.Entry{
		Actor:      actor,
		Action:     "power.reboot.cancel",
		Target:     "system",
		Result:     result,
		RemoteAddr: remoteAddr,
		RequestID:  requestID,
		Detail:     detail,
	})

	s.log.Info("已取消待执行重启", "actor", actor, "hadPending", had)
	return s.Status(ctx)
}

// cancelPendingRebootLocked 在调用方已持锁外的情况下取消（内部使用）。
func (s *Scheduler) cancelPendingRebootLocked(ctx context.Context, reason string) error {
	if err := s.store.ClearPendingReboot(ctx); err != nil {
		return err
	}
	s.audit(ctx, audit.Entry{
		Actor:  "system",
		Action: "power.reboot.cancel",
		Target: "system",
		Result: audit.ResultSuccess,
		Detail: map[string]any{"reason": reason},
	})
	return nil
}

// Status 返回电源与调度状态。
func (s *Scheduler) Status(ctx context.Context) (model.PowerStatus, error) {
	now := s.now()

	s.mu.Lock()
	var pending *model.PendingReboot
	if s.pending != nil {
		p := *s.pending
		pending = &p
	}
	s.mu.Unlock()

	ok, reason := s.power.Capability()

	st := model.PowerStatus{
		Pending:                 pending,
		CanReboot:               ok,
		RebootUnavailableReason: reason,
		Timezone:                systemTimezoneName(),
	}

	if bt, err := s.power.BootTime(ctx); err == nil {
		b := bt.UTC()
		st.LastRebootAt = &b
		if up := now.Sub(b); up > 0 {
			st.UptimeSeconds = int64(up.Seconds())
		}
	}

	// 调度器是否已安装：只要有一个本应用的 launchd 任务存在即视为已安装。
	if labels, err := s.installer.ListManaged(); err == nil && len(labels) > 0 {
		st.ScheduleDaemonInstalled = true
	}

	return st, nil
}

// restorePendingReboot 从磁盘恢复待执行的重启。
//
// 三种情形：
//  1. 系统在排定之后重启过 → 意图已达成，清除记录（否则会「重启后再重启」）；
//  2. 计划时刻已过（Agent 崩溃期间错过）→ 立即执行：这是用户明确的一次性
//     意图，不像周期任务那样有「错过就跳过」的余地；
//  3. 计划时刻在未来 → 重新设定时器（用绝对时刻，不重置倒计时）。
func (s *Scheduler) restorePendingReboot(ctx context.Context) error {
	pending, err := s.store.LoadPendingReboot(ctx)
	if err != nil {
		return err
	}
	if pending == nil {
		return nil
	}

	bootTime, bootErr := s.power.BootTime(ctx)

	// 情形 1：已重启过。
	if bootErr == nil && bootTime.After(pending.CreatedAt) {
		if err := s.store.ClearPendingReboot(ctx); err != nil {
			return fmt.Errorf("清除已完成的待执行重启失败: %w", err)
		}
		s.audit(ctx, audit.Entry{
			Actor:  "system",
			Action: "power.reboot.completed",
			Target: "system",
			Result: audit.ResultSuccess,
			Detail: map[string]any{
				"note":        "系统已在排定时间之后重启，判定该次重启已生效",
				"scheduledAt": pending.ScheduledAt.Format(time.RFC3339),
				"bootTime":    bootTime.Format(time.RFC3339),
			},
		})
		s.log.Info("待执行重启已生效，清除记录", "scheduledAt", pending.ScheduledAt)
		return nil
	}

	s.mu.Lock()
	s.pending = pending
	s.mu.Unlock()

	now := s.now()
	if !pending.ScheduledAt.After(now) {
		// 情形 2：已过期 → 立即执行。
		s.log.Warn("待执行重启已过期，立即执行",
			"scheduledAt", pending.ScheduledAt, "delay", now.Sub(pending.ScheduledAt))
		go s.firePending(context.WithoutCancel(ctx), "恢复时已过期")
		return nil
	}

	// 情形 3：未来 → 用绝对时刻重新设定时器。
	s.mu.Lock()
	s.armTimerLocked(ctx, pending.ScheduledAt)
	s.mu.Unlock()
	s.log.Info("已恢复待执行重启", "scheduledAt", pending.ScheduledAt,
		"remaining", pending.ScheduledAt.Sub(now))
	return nil
}

// armTimerLocked 设置到点触发的定时器。调用方必须已持锁。
func (s *Scheduler) armTimerLocked(ctx context.Context, at time.Time) {
	if s.timer != nil {
		s.timer.Stop()
	}
	// 用调度器自己的时钟算延迟，而不是 time.Until（真实时钟）。
	//
	// 两个时钟不一致时会出问题：若 at 来自注入时钟而延迟取真实时钟，
	// 目标时刻在「真实现在」之前就会把 delay 算成 0 → 定时器立刻触发，
	// 于是「排定 10 分钟后重启」变成「立即重启」。生产环境两者一致，
	// 但这类「只有测试才暴露」的分歧恰恰说明混用时钟是错的。
	delay := at.Sub(s.now())
	if delay < 0 {
		delay = 0
	}
	// 用 WithoutCancel：HTTP 请求结束后 ctx 会被取消，
	// 但重启不能因为「响应写完了」就放弃执行。
	bg := context.WithoutCancel(ctx)
	s.timer = time.AfterFunc(delay, func() {
		s.firePending(bg, "定时触发")
	})
}

// firePending 执行待执行的重启。
func (s *Scheduler) firePending(ctx context.Context, trigger string) {
	s.mu.Lock()
	if s.stopped {
		s.mu.Unlock()
		return
	}
	pending := s.pending
	s.pending = nil
	if s.timer != nil {
		s.timer.Stop()
		s.timer = nil
	}
	s.mu.Unlock()

	if pending == nil {
		return
	}

	s.log.Info("开始执行重启", "trigger", trigger, "reason", pending.Reason)

	var err error
	if s.executeReboot != nil {
		err = s.executeReboot(ctx, 0)
	} else {
		err = s.power.Reboot(ctx, 0)
	}

	if err != nil {
		// 失败时清除记录：保留会让定时器立刻重试，形成「失败→重试」循环。
		if clrErr := s.store.ClearPendingReboot(ctx); clrErr != nil {
			s.log.Error("重启失败且清除记录也失败", "error", clrErr)
		}
		s.audit(ctx, audit.Entry{
			Actor:  "system",
			Action: "power.reboot.execute",
			Target: "system",
			Result: audit.ResultFailure,
			Detail: map[string]any{
				"error":   err.Error(),
				"trigger": trigger,
				"reason":  pending.Reason,
			},
		})
		s.log.Error("执行重启失败", "error", err, "trigger", trigger)
		return
	}

	// 成功：机器马上会重启，这里尽量把记录写完。
	if clrErr := s.store.ClearPendingReboot(ctx); clrErr != nil {
		s.log.Warn("清除待执行重启记录失败（系统即将重启，通常可忽略）", "error", clrErr)
	}
	s.audit(ctx, audit.Entry{
		Actor:  "system",
		Action: "power.reboot.execute",
		Target: "system",
		Result: audit.ResultSuccess,
		Detail: map[string]any{
			"trigger": trigger,
			"reason":  pending.Reason,
		},
	})
}

// checkOnceConflict 检查是否已有冲突的待执行重启。
func (s *Scheduler) checkOnceConflict(ctx context.Context) error {
	s.mu.Lock()
	hasPending := s.pending != nil
	pendingAt := time.Time{}
	if hasPending {
		pendingAt = s.pending.ScheduledAt
	}
	s.mu.Unlock()

	if !hasPending {
		// 内存没有，但磁盘可能有（例如刚启动尚未恢复）。再查一次更稳。
		p, err := s.store.LoadPendingReboot(ctx)
		if err != nil {
			return err
		}
		if p != nil {
			hasPending, pendingAt = true, p.ScheduledAt
		}
	}
	if hasPending {
		return model.NewError(model.CodeConflict,
			"已有待执行的重启（计划于 %s），无法再创建单次重启任务",
			pendingAt.Format("2006-01-02 15:04:05"))
	}
	return nil
}

// applyOnceTask 把「单次任务」转成待执行重启。
//
// 为什么不用 launchd：StartCalendarInterval 没有「年」字段，无法表达
// 「仅在 2026-11-01 04:30 执行一次」。若用 StartInterval 或日历近似，
// 装错就会变成周期性重启，风险远大于收益。
func (s *Scheduler) applyOnceTask(ctx context.Context, task model.ScheduledTask) error {
	at := task.Recurrence.OnceAt
	if at == nil {
		return model.NewError(model.CodeInvalidArgument, "单次任务缺少执行时刻")
	}

	if ok, reason := s.power.Capability(); !ok {
		return model.NewError(model.CodeUnavailable, "无法排定重启：%s", reason)
	}

	pending := model.PendingReboot{
		ScheduledAt: at.In(time.Local),
		Reason:      fmt.Sprintf("计划任务「%s」", task.Name),
		RequestedBy: "scheduler",
		CreatedAt:   s.now(),
		TaskID:      task.ID,
	}

	s.mu.Lock()
	s.pending = &pending
	s.armTimerLocked(ctx, pending.ScheduledAt)
	s.mu.Unlock()

	if err := s.store.SavePendingReboot(ctx, pending); err != nil {
		s.mu.Lock()
		s.pending = nil
		if s.timer != nil {
			s.timer.Stop()
			s.timer = nil
		}
		s.mu.Unlock()
		return fmt.Errorf("保存待执行重启失败: %w", err)
	}

	s.audit(ctx, audit.Entry{
		Actor:  "system",
		Action: "power.reboot.schedule",
		Target: "system",
		Result: audit.ResultSuccess,
		Detail: map[string]any{
			"note":        "由单次计划任务触发",
			"taskId":      task.ID,
			"taskName":    task.Name,
			"scheduledAt": pending.ScheduledAt.Format(time.RFC3339),
		},
	})
	return nil
}

// RunTask 执行指定任务。供 launchd 触发的 CLI 子命令与补执行调用。
func (s *Scheduler) RunTask(ctx context.Context, taskID int64) (model.TaskExecution, error) {
	return s.runTask(ctx, taskID, s.now(), "launchd 触发")
}

// runTask 执行任务的核心流程。
//
// 顺序即安全设计：
//  1. 安全检查（禁用 / 已有待执行 / 间隔不足 / 熔断）；
//  2. 写 running 记录（先留痕，避免执行后进程被杀导致无记录）；
//  3. 真正执行重启；
//  4. 补齐结果。
func (s *Scheduler) runTask(
	ctx context.Context, taskID int64, scheduledFor time.Time, trigger string,
) (model.TaskExecution, error) {
	task, err := s.store.Get(ctx, taskID)
	if err != nil {
		return model.TaskExecution{}, err
	}

	s.mu.Lock()
	hasPending := s.pending != nil
	s.mu.Unlock()

	decision := schedule.Decide(task, s.now(), hasPending)

	switch decision.Action {
	case schedule.ActionSkip:
		if err := s.store.RecordSkipped(ctx, task.ID, decision.Reason); err != nil {
			s.log.Warn("记录跳过失败", "task", task.Name, "error", err)
		}
		// 推进下次执行时间，否则每次 tick 都会重复判定同一次计划。
		next, ok := schedule.NextAfter(task, s.now())
		var nextPtr *time.Time
		if ok {
			nextPtr = &next
		}
		if err := s.store.RecordResult(ctx, task.ID, model.ExecutionSkipped, decision.Reason, nextPtr); err != nil {
			s.log.Warn("推进下次执行时间失败", "task", task.Name, "error", err)
		}
		s.log.Info("跳过任务", "task", task.Name, "reason", decision.Reason)
		return model.TaskExecution{
			TaskID: task.ID, TaskName: task.Name,
			StartedAt: s.now(), Result: model.ExecutionSkipped, Detail: decision.Reason,
		}, nil

	case schedule.ActionDisable:
		if err := s.store.RecordResult(ctx, task.ID, model.ExecutionFailed,
			decision.Reason, nil); err != nil {
			s.log.Warn("记录熔断失败", "task", task.Name, "error", err)
		}
		if _, err := s.Sync(ctx); err != nil {
			s.log.Warn("熔断后同步失败", "error", err)
		}
		s.audit(ctx, audit.Entry{
			Actor:  "system",
			Action: "schedule.task.auto_disable",
			Target: fmt.Sprintf("task:%d", task.ID),
			Result: audit.ResultFailure,
			Detail: map[string]any{"taskName": task.Name, "reason": decision.Reason},
		})
		return model.TaskExecution{
			TaskID: task.ID, TaskName: task.Name,
			StartedAt: s.now(), Result: model.ExecutionFailed, Detail: decision.Reason,
		}, nil
	}

	// 真正执行
	execID, err := s.store.StartExecution(ctx, task.ID)
	if err != nil {
		return model.TaskExecution{}, err
	}

	// 先算出下次执行时间再执行：重启会杀掉进程，
	// 执行之后再算就来不及写库了。
	next, hasNext := schedule.NextAfter(task, s.now())
	var nextPtr *time.Time
	if hasNext {
		nextPtr = &next
	}

	var rebootErr error
	if s.executeReboot != nil {
		rebootErr = s.executeReboot(ctx, 0)
	} else {
		rebootErr = s.power.Reboot(ctx, 0)
	}

	result := model.ExecutionSuccess
	detail := fmt.Sprintf("%s，已提交重启请求", trigger)
	if rebootErr != nil {
		result = model.ExecutionFailed
		detail = fmt.Sprintf("%s，重启失败: %v", trigger, rebootErr)
	}

	if err := s.store.FinishExecution(ctx, execID, result, detail); err != nil {
		s.log.Warn("补齐执行记录失败", "task", task.Name, "error", err)
	}
	if err := s.store.RecordResult(ctx, task.ID, result, detail, nextPtr); err != nil {
		s.log.Warn("更新任务状态失败", "task", task.Name, "error", err)
	}

	auditResult := audit.ResultSuccess
	if rebootErr != nil {
		auditResult = audit.ResultFailure
	}
	s.audit(ctx, audit.Entry{
		Actor:  "system",
		Action: "schedule.task.run",
		Target: fmt.Sprintf("task:%d", task.ID),
		Result: auditResult,
		Detail: map[string]any{
			"taskName":     task.Name,
			"trigger":      trigger,
			"scheduledFor": scheduledFor.Format(time.RFC3339),
			"detail":       detail,
		},
	})

	s.log.Info("任务执行完成", "task", task.Name, "result", result, "detail", detail)

	now := s.now()
	return model.TaskExecution{
		ID: execID, TaskID: task.ID, TaskName: task.Name,
		StartedAt: now, FinishedAt: &now, Result: result, Detail: detail,
	}, nil
}

// audit 写入审计，失败只记日志。
//
// 为什么不返回错误：审计写入失败不应让已经发生的重启「回滚」——
// 那样只会让状态与事实更不一致。失败会留在程序日志里。
func (s *Scheduler) audit(ctx context.Context, e audit.Entry) {
	if s.auditor == nil {
		return
	}
	if e.At.IsZero() {
		e.At = s.now()
	}
	if err := s.auditor.Write(ctx, e); err != nil {
		s.log.Error("写入审计失败", "action", e.Action, "error", err)
	}
}

// systemTimezoneName 返回系统的 IANA 时区名（如 Asia/Shanghai）。
//
// 为什么需要 IANA 名而不是缩写：CST 有歧义（中国标准时间、
// 美国中部时间、古巴标准时间都是 CST）。新建任务时要落一个
// 明确的时区名，否则换台机器解释就变了。
//
// Go 标准库没有直接获取系统时区的接口，因此按可靠性依次尝试：
//  1. TZ 环境变量（显式配置优先）；
//  2. /etc/localtime 的符号链接指向（macOS 上形如
//     /var/db/timezone/zoneinfo/Asia/Shanghai）；
//  3. 回落到 Zone() 的缩写（有歧义，但比空字符串好）。
func systemTimezoneName() string {
	if tz := os.Getenv("TZ"); tz != "" {
		if _, err := time.LoadLocation(tz); err == nil {
			return tz
		}
	}

	if target, err := os.Readlink("/etc/localtime"); err == nil {
		const marker = "zoneinfo/"
		if i := strings.Index(target, marker); i >= 0 {
			name := target[i+len(marker):]
			if _, err := time.LoadLocation(name); err == nil {
				return name
			}
		}
	}

	name, _ := time.Now().Zone()
	return name
}
