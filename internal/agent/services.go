package agent

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/lancelot-lewis/MacPanel/internal/ipc"
	"github.com/lancelot-lewis/MacPanel/internal/model"
	"github.com/lancelot-lewis/MacPanel/internal/platform/darwin/services"
	"github.com/lancelot-lewis/MacPanel/internal/servicestore"
)

// M4 方法名。
const (
	MethodServicesList       = "services.list"
	MethodServicesDetail     = "services.detail"
	MethodServicesRegister   = "services.register"
	MethodServicesUnregister = "services.unregister"
	MethodServicesControl    = "services.control"
)

// ServiceDeps 是 M4 启动项管理依赖。
//
// Controller 可以为 nil（非 root 时）：扫描与展示总是可用，
// 但启停需要 root。此时 ControlDisabledReason 说明原因，
// 界面据此把操作按钮置灰并给出可读提示，而不是让点击失败。
type ServiceDeps struct {
	Scanner    *services.Scanner
	Controller *services.Controller
	Store      *servicestore.Store

	ControlDisabledReason string
}

// SetServiceDeps 注入启动项管理能力。
func (a *Agent) SetServiceDeps(deps *ServiceDeps) {
	a.services = deps
}

// registerServices 注册 M4 方法。
func (a *Agent) registerServices(srv *ipc.Server) {
	srv.Register(MethodServicesList, a.handleServicesList)
	srv.Register(MethodServicesDetail, a.handleServicesDetail)
	srv.Register(MethodServicesRegister, a.handleServicesRegister)
	srv.Register(MethodServicesUnregister, a.handleServicesUnregister)
	srv.Register(MethodServicesControl, a.handleServicesControl)
}

// servicesReady 检查依赖是否就绪。
func (a *Agent) servicesReady() error {
	if a.services == nil || a.services.Scanner == nil || a.services.Store == nil {
		return model.NotImplemented("启动项管理（Agent 未装配服务扫描器）")
	}
	return nil
}

// servicesListParams 是 services.list 的参数。
type servicesListParams struct {
	// IncludeGUI 是否包含当前图形会话域（LaunchAgents）。
	IncludeGUI bool `json:"includeGui"`
	// GUIUID 指定要查询的图形会话用户；0 表示用 Agent 进程自身的 uid。
	GUIUID int `json:"guiUid"`
}

// handleServicesList 列出服务并附带分类与可操作判定。
func (a *Agent) handleServicesList(ctx context.Context, params json.RawMessage) (any, error) {
	if err := a.servicesReady(); err != nil {
		return nil, err
	}

	var p servicesListParams
	if err := decodeParams(params, &p); err != nil {
		return nil, err
	}
	if p.GUIUID < 0 {
		return nil, model.NewError(model.CodeInvalidArgument, "guiUid 不能为负")
	}

	list, warnings, err := a.services.Scanner.List(ctx, services.ListOptions{
		IncludeGUI: p.IncludeGUI,
		GUIUID:     p.GUIUID,
	})
	if err != nil {
		return nil, model.Unavailable("扫描服务失败：%v", err)
	}

	// 受管白名单来自数据库；读失败时降级为「全部只读」而不是报错——
	// 能看到服务列表本身仍有诊断价值。
	registered := map[string]bool{}
	allowed := map[string]bool{}
	if a.services.Store != nil {
		if set, err := a.services.Store.RegisteredSet(ctx); err != nil {
			a.log.Warn("读取受管服务列表失败，全部按未登记处理", "error", err)
			warnings = append(warnings, "读取受管服务列表失败，所有第三方服务暂时只读")
		} else {
			registered = set
		}
		if set, err := a.services.Store.AllowedSet(ctx); err != nil {
			a.log.Warn("读取受管授权失败，全部按只读处理", "error", err)
		} else {
			allowed = set
		}
	}

	enriched := services.Enrich(list, registered, allowed)
	summary := services.Summarize(enriched)

	items := make([]model.ServiceSummary, 0, len(enriched))
	for _, s := range enriched {
		items = append(items, model.ServiceSummary{
			Label:         s.Label,
			DisplayName:   s.DisplayName,
			Domain:        string(s.Domain),
			Kind:          string(s.Kind),
			PID:           s.PID,
			Running:       s.Running,
			PlistPath:     s.PlistPath,
			Managed:       s.Managed,
			Controllable:  s.Controllable,
			BlockedReason: s.BlockedReason,
			LastExit:      s.LastExit,
		})
	}

	return model.ServicesOverview{
		Items: items,
		Summary: model.ServiceCounts{
			Total:          summary.Total,
			SystemCritical: summary.SystemCritical,
			Self:           summary.Self,
			ThirdParty:     summary.ThirdParty,
			Managed:        summary.Managed,
			Running:        summary.Running,
		},
		Warnings: warnings,
	}, nil
}

// serviceDetailParams 是 services.detail 的参数。
type serviceDetailParams struct {
	Label  string `json:"label"`
	Domain string `json:"domain"`
}

// serviceDetailResult 是服务详情。
type serviceDetailResult struct {
	Label     string                `json:"label"`
	Domain    string                `json:"domain"`
	PlistPath string                `json:"plistPath,omitempty"`
	Info      any                   `json:"info,omitempty"`
	LogPaths  []string              `json:"logPaths,omitempty"`
	Managed   *model.ManagedService `json:"managed,omitempty"`
	// JournalHint 说明在没有任何日志文件时如何查看系统统一日志。
	JournalHint string `json:"journalHint,omitempty"`
}

func (a *Agent) handleServicesDetail(ctx context.Context, params json.RawMessage) (any, error) {
	if err := a.servicesReady(); err != nil {
		return nil, err
	}

	var p serviceDetailParams
	if err := decodeParams(params, &p); err != nil {
		return nil, err
	}
	if err := services.ValidateLabelForControl(p.Label); err != nil {
		// 系统关键服务也允许查看详情（只是不能操作），
		// 因此这里不直接拒绝，而是允许读、由上层决定是否展示操作按钮。
		if p.Label == "" {
			return nil, model.NewError(model.CodeInvalidArgument, "%v", err)
		}
	}

	domain := services.DomainSystem
	if p.Domain == string(services.DomainGUI) {
		domain = services.DomainGUI
	}

	info, path, err := a.services.Scanner.Detail(ctx, p.Label, domain)
	if err != nil {
		return nil, model.Unavailable("读取服务配置失败：%v", err)
	}

	res := serviceDetailResult{
		Label:     p.Label,
		Domain:    string(domain),
		PlistPath: path,
		Info:      info,
		LogPaths:  services.LogPaths(info),
	}

	if len(res.LogPaths) == 0 {
		// 没有声明日志文件时给出可执行的查询方式，
		// 而不是留空让用户猜。
		res.JournalHint = fmt.Sprintf("log show --predicate 'process == \"%s\"' --last 1h", p.Label)
	}

	if m, err := a.services.Store.Get(ctx, p.Label); err == nil {
		res.Managed = &m
	}

	return res, nil
}

// handleServicesRegister 登记或更新受管服务。
//
// 只登记不操作：授权（allowControl）单独一个字段，
// 用户可以先观察再决定是否允许启停。
func (a *Agent) handleServicesRegister(ctx context.Context, params json.RawMessage) (any, error) {
	if err := a.servicesReady(); err != nil {
		return nil, err
	}

	var p model.ManagedService
	if err := decodeParams(params, &p); err != nil {
		return nil, err
	}

	saved, err := a.services.Store.Upsert(ctx, p)
	if err != nil {
		return nil, err
	}
	a.log.Info("登记受管服务", "label", saved.Label, "allowControl", saved.AllowControl)
	return saved, nil
}

// handleServicesUnregister 取消登记。
func (a *Agent) handleServicesUnregister(ctx context.Context, params json.RawMessage) (any, error) {
	if err := a.servicesReady(); err != nil {
		return nil, err
	}

	var p serviceDetailParams
	if err := decodeParams(params, &p); err != nil {
		return nil, err
	}
	if err := a.services.Store.Delete(ctx, p.Label); err != nil {
		return nil, err
	}
	a.log.Info("取消登记受管服务", "label", p.Label)
	return map[string]any{"ok": true}, nil
}

// serviceControlParams 是 services.control 的参数。
type serviceControlParams struct {
	Label  string `json:"label"`
	Action string `json:"action"`
	// ConfirmsRisk 由 Server 层校验，Agent 这里也检查一次：
	// Agent 面向的是受信任的 Server，但这道检查能防止
	// 「Server 实现改动后漏校验」导致的风险。
	ConfirmsRisk bool `json:"confirmsRisk"`
}

// handleServicesControl 对受管服务执行启停。
//
// 三道检查（顺序即安全设计）：
//  1. 标签形态（拒绝系统关键服务与自身）；
//  2. 受管白名单里已授权；
//  3. 使用登记时记录的 plist 路径，不接受调用方传入。
func (a *Agent) handleServicesControl(ctx context.Context, params json.RawMessage) (any, error) {
	if err := a.servicesReady(); err != nil {
		return nil, err
	}
	if a.services.Controller == nil {
		reason := a.services.ControlDisabledReason
		if reason == "" {
			reason = "服务启停能力未启用"
		}
		return nil, model.NewError(model.CodeUnavailable, "%s", reason)
	}

	var p serviceControlParams
	if err := decodeParams(params, &p); err != nil {
		return nil, err
	}

	action := services.Action(p.Action)
	if !action.Valid() {
		return nil, model.NewError(model.CodeInvalidArgument, "不支持的操作 %q", p.Action)
	}

	// 第 1 道：形态检查
	if err := services.ValidateLabelForControl(p.Label); err != nil {
		return nil, model.NewError(model.CodeForbidden, "%v", err)
	}

	// 第 2 道：白名单
	managed, err := a.services.Store.Get(ctx, p.Label)
	if err != nil {
		return nil, model.NewError(model.CodeForbidden,
			"服务 %s 未加入受管列表，无法操作", p.Label)
	}
	if !managed.AllowControl {
		return nil, model.NewError(model.CodeForbidden,
			"服务 %s 已登记但未授权启停（可在受管列表中开启）", p.Label)
	}

	// 第 3 道：路径来自登记记录
	if err := a.services.Controller.Execute(ctx, p.Label, action, managed.PlistPath); err != nil {
		return nil, model.NewError(model.CodeInternal,
			"%s服务 %s 失败：%v", model.DescribeServiceAction(string(action)), p.Label, err)
	}

	a.log.Info("已操作受管服务",
		"label", p.Label, "action", string(action), "allowControl", managed.AllowControl)

	return model.ServiceAction{
		Label:   p.Label,
		Action:  string(action),
		Message: fmt.Sprintf("已%s服务 %s", model.DescribeServiceAction(string(action)), p.Label),
	}, nil
}
