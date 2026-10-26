package agent

import (
	"context"
	"encoding/json"

	"github.com/lancelot-lewis/MacPanel/internal/ipc"
	"github.com/lancelot-lewis/MacPanel/internal/model"
	"github.com/lancelot-lewis/MacPanel/internal/tasksched"
)

// M3 方法名。
const (
	MethodPowerStatus        = "power.status"
	MethodPowerReboot        = "power.reboot"
	MethodPowerCancel        = "power.cancel"
	MethodScheduleList       = "schedule.list"
	MethodScheduleCreate     = "schedule.create"
	MethodScheduleUpdate     = "schedule.update"
	MethodScheduleDelete     = "schedule.delete"
	MethodScheduleExecutions = "schedule.executions"
	MethodScheduleRunNow     = "schedule.run_now"
	MethodScheduleSync       = "schedule.sync"
)

// PowerDeps 是 M3 电源与调度能力依赖。
//
// 与 FirewallDeps 一样按「依赖是否就绪」区分：未就绪时方法仍注册
// （前端据此判断能力），但调用返回未实现并说明原因。
type PowerDeps struct {
	Scheduler *tasksched.Scheduler
}

// SetPowerDeps 注入电源与调度能力。
func (a *Agent) SetPowerDeps(deps *PowerDeps) {
	a.power = deps
}

// registerPower 注册 M3 方法。
func (a *Agent) registerPower(srv *ipc.Server) {
	srv.Register(MethodPowerStatus, a.handlePowerStatus)
	srv.Register(MethodPowerReboot, a.handlePowerReboot)
	srv.Register(MethodPowerCancel, a.handlePowerCancel)
	srv.Register(MethodScheduleList, a.handleScheduleList)
	srv.Register(MethodScheduleCreate, a.handleScheduleCreate)
	srv.Register(MethodScheduleUpdate, a.handleScheduleUpdate)
	srv.Register(MethodScheduleDelete, a.handleScheduleDelete)
	srv.Register(MethodScheduleExecutions, a.handleScheduleExecutions)
	srv.Register(MethodScheduleRunNow, a.handleScheduleRunNow)
	srv.Register(MethodScheduleSync, a.handleScheduleSync)
}

// powerReady 检查依赖是否就绪，未就绪时返回统一错误。
func (a *Agent) powerReady() error {
	if a.power == nil || a.power.Scheduler == nil {
		return model.NotImplemented("电源与计划任务（Agent 未装配调度器）")
	}
	return nil
}

// ---- power.* ----

func (a *Agent) handlePowerStatus(ctx context.Context, params json.RawMessage) (any, error) {
	if err := requireNoParams(params); err != nil {
		return nil, err
	}
	if err := a.powerReady(); err != nil {
		return nil, err
	}
	return a.power.Scheduler.Status(ctx)
}

// rebootParams 是 power.reboot 的参数。
type rebootParams struct {
	DelaySeconds int    `json:"delaySeconds"`
	Reason       string `json:"reason"`
	Actor        string `json:"actor"`
	RemoteAddr   string `json:"remoteAddr"`
	RequestID    string `json:"requestId"`
}

// handlePowerReboot 排定重启。
//
// 注意：本方法不校验「用户是否确认了风险」——那是 Server 层的职责
// （它面对用户交互）。Agent 面对的是受信任的 Server，只做参数与
// 能力校验。两层分开的好处是 Agent 不依赖 UI 语义。
func (a *Agent) handlePowerReboot(ctx context.Context, params json.RawMessage) (any, error) {
	if err := a.powerReady(); err != nil {
		return nil, err
	}

	var p rebootParams
	if err := decodeParams(params, &p); err != nil {
		return nil, err
	}
	if len(p.Reason) > 200 {
		return nil, model.NewError(model.CodeInvalidArgument, "原因不能超过 200 个字符")
	}

	return a.power.Scheduler.ScheduleReboot(ctx, tasksched.ScheduleRequest{
		DelaySeconds: p.DelaySeconds,
		Reason:       p.Reason,
		Actor:        p.Actor,
		RemoteAddr:   p.RemoteAddr,
		RequestID:    p.RequestID,
	})
}

func (a *Agent) handlePowerCancel(ctx context.Context, params json.RawMessage) (any, error) {
	if err := a.powerReady(); err != nil {
		return nil, err
	}

	var p rebootParams
	if err := decodeParams(params, &p); err != nil {
		return nil, err
	}
	return a.power.Scheduler.CancelReboot(ctx, p.Actor, p.RemoteAddr, p.RequestID)
}

// ---- schedule.* ----

func (a *Agent) handleScheduleList(ctx context.Context, params json.RawMessage) (any, error) {
	if err := requireNoParams(params); err != nil {
		return nil, err
	}
	if err := a.powerReady(); err != nil {
		return nil, err
	}
	tasks, err := a.power.Scheduler.List(ctx)
	if err != nil {
		return nil, err
	}
	return map[string]any{"items": tasks}, nil
}

// scheduleParams 是创建/更新任务的参数。
type scheduleParams struct {
	Task model.ScheduledTask `json:"task"`
}

func (a *Agent) handleScheduleCreate(ctx context.Context, params json.RawMessage) (any, error) {
	if err := a.powerReady(); err != nil {
		return nil, err
	}

	var p scheduleParams
	if err := decodeParams(params, &p); err != nil {
		return nil, err
	}

	created, err := a.power.Scheduler.Create(ctx, p.Task)
	if err != nil {
		return nil, err
	}
	return created, nil
}

func (a *Agent) handleScheduleUpdate(ctx context.Context, params json.RawMessage) (any, error) {
	if err := a.powerReady(); err != nil {
		return nil, err
	}

	var p scheduleParams
	if err := decodeParams(params, &p); err != nil {
		return nil, err
	}
	if p.Task.ID <= 0 {
		return nil, model.NewError(model.CodeInvalidArgument, "缺少任务 ID")
	}

	return a.power.Scheduler.Update(ctx, p.Task)
}

// deleteParams 是删除任务的参数。
type deleteParams struct {
	ID int64 `json:"id"`
}

func (a *Agent) handleScheduleDelete(ctx context.Context, params json.RawMessage) (any, error) {
	if err := a.powerReady(); err != nil {
		return nil, err
	}

	var p deleteParams
	if err := decodeParams(params, &p); err != nil {
		return nil, err
	}
	if p.ID <= 0 {
		return nil, model.NewError(model.CodeInvalidArgument, "缺少任务 ID")
	}

	if err := a.power.Scheduler.Delete(ctx, p.ID); err != nil {
		return nil, err
	}
	return map[string]any{"ok": true}, nil
}

// executionsParams 是查询执行记录的参数。
type executionsParams struct {
	TaskID int64 `json:"taskId"`
	Limit  int   `json:"limit"`
}

func (a *Agent) handleScheduleExecutions(ctx context.Context, params json.RawMessage) (any, error) {
	if err := a.powerReady(); err != nil {
		return nil, err
	}

	var p executionsParams
	if err := decodeParams(params, &p); err != nil {
		return nil, err
	}
	if p.Limit < 0 || p.Limit > 500 {
		return nil, model.NewError(model.CodeInvalidArgument, "limit 必须在 0-500 之间")
	}

	items, err := a.power.Scheduler.ListExecutions(ctx, p.TaskID, p.Limit)
	if err != nil {
		return nil, err
	}
	return map[string]any{"items": items}, nil
}

// handleScheduleRunNow 立即执行一次任务（手动触发）。
//
// 与 launchd 触发走同一条路径：安全检查（间隔、熔断）同样生效。
// 这样「手动点一下」不会成为绕过防循环重启的后门。
func (a *Agent) handleScheduleRunNow(ctx context.Context, params json.RawMessage) (any, error) {
	if err := a.powerReady(); err != nil {
		return nil, err
	}

	var p deleteParams
	if err := decodeParams(params, &p); err != nil {
		return nil, err
	}
	if p.ID <= 0 {
		return nil, model.NewError(model.CodeInvalidArgument, "缺少任务 ID")
	}

	return a.power.Scheduler.RunTask(ctx, p.ID)
}

func (a *Agent) handleScheduleSync(ctx context.Context, params json.RawMessage) (any, error) {
	if err := requireNoParams(params); err != nil {
		return nil, err
	}
	if err := a.powerReady(); err != nil {
		return nil, err
	}
	return a.power.Scheduler.Sync(ctx)
}

// decodeParams 解析 JSON 参数。
//
// 与 firewall.go 里各处内联的 json.Unmarshal 相比，这里集中处理
// 空参数与解析错误，避免每个 handler 重复四行样板。
// 空参数（长度为 0 或字面量 null）视为「全部使用默认值」。
func decodeParams(params json.RawMessage, out any) error {
	if len(params) == 0 {
		return nil
	}
	if string(params) == "null" {
		return nil
	}
	if err := json.Unmarshal(params, out); err != nil {
		return model.NewError(model.CodeInvalidArgument, "参数解析失败：%v", err)
	}
	return nil
}
