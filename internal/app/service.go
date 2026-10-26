// Package app 是 Server 侧的应用服务层：把 IPC 调用封装成类型化方法。
//
// 这样 HTTP handler 不直接拼接方法名与 JSON，也不会误以为「调用成功即数据有效」。
// 依赖注入客户端接口，便于测试中替换为桩实现。
package app

import (
	"context"
	"encoding/json"

	"github.com/lancelot-lewis/MacPanel/internal/agent"
	"github.com/lancelot-lewis/MacPanel/internal/model"
)

// Caller 是 IPC 客户端的最小接口。
type Caller interface {
	Call(ctx context.Context, method string, params any, out any) error
}

// Service 提供业务方法。
type Service struct {
	client Caller
}

// NewService 创建服务。
func NewService(client Caller) *Service {
	return &Service{client: client}
}

// SystemOverview 获取系统概览。
func (s *Service) SystemOverview(ctx context.Context) (model.SystemOverview, error) {
	var out model.SystemOverview
	if err := s.client.Call(ctx, agent.MethodSystemOverview, nil, &out); err != nil {
		return out, err
	}
	return out, nil
}

// NetworkSnapshot 获取网络快照。
func (s *Service) NetworkSnapshot(ctx context.Context) (model.NetworkSnapshot, error) {
	var out model.NetworkSnapshot
	if err := s.client.Call(ctx, agent.MethodSystemNetwork, nil, &out); err != nil {
		return out, err
	}
	return out, nil
}

// FirewallStatus 获取防火墙状态。
func (s *Service) FirewallStatus(ctx context.Context) (model.FirewallStatus, error) {
	var out model.FirewallStatus
	if err := s.client.Call(ctx, agent.MethodFirewallStatus, nil, &out); err != nil {
		return out, err
	}
	return out, nil
}

// AgentHealth 获取 Agent 健康状况。
func (s *Service) AgentHealth(ctx context.Context) (model.AgentHealth, error) {
	var raw model.AgentHealthResponse
	if err := s.client.Call(ctx, agent.MethodHealth, nil, &raw); err != nil {
		return model.AgentHealth{}, err
	}
	return raw.AgentHealth, nil
}

// AgentHealthDetail 返回含方法列表的完整健康状况。
func (s *Service) AgentHealthDetail(ctx context.Context) (model.AgentHealthResponse, error) {
	var raw model.AgentHealthResponse
	if err := s.client.Call(ctx, agent.MethodHealth, nil, &raw); err != nil {
		return raw, err
	}
	return raw, nil
}

// FirewallRules 读取规则集与事务状态。
func (s *Service) FirewallRules(ctx context.Context) (json.RawMessage, error) {
	var out json.RawMessage
	if err := s.client.Call(ctx, agent.MethodFirewallRulesGet, nil, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// FirewallRulesSave 保存规则集，返回新生成的预览。
func (s *Service) FirewallRulesSave(ctx context.Context, ruleSet any) (json.RawMessage, error) {
	var out json.RawMessage
	params := map[string]any{"rules": ruleSet}
	// rules.set 期望的是扁平的 RuleSet 结构，这里直接传规则集本身
	if rs, ok := ruleSet.(map[string]any); ok {
		params = rs
	}
	if err := s.client.Call(ctx, agent.MethodFirewallRulesSet, params, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// FirewallPreview 预览规则生成结果。
func (s *Service) FirewallPreview(ctx context.Context, params any) (json.RawMessage, error) {
	var out json.RawMessage
	if err := s.client.Call(ctx, agent.MethodFirewallPreview, params, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// FirewallApply 应用规则。
func (s *Service) FirewallApply(ctx context.Context, params any) (json.RawMessage, error) {
	var out json.RawMessage
	if err := s.client.Call(ctx, agent.MethodFirewallApply, params, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// FirewallConfirm 确认当前配置。
func (s *Service) FirewallConfirm(ctx context.Context) (json.RawMessage, error) {
	var out json.RawMessage
	if err := s.client.Call(ctx, agent.MethodFirewallConfirm, nil, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// FirewallRollback 回滚配置。
func (s *Service) FirewallRollback(ctx context.Context, reason string) (json.RawMessage, error) {
	var out json.RawMessage
	params := map[string]any{"reason": reason}
	if err := s.client.Call(ctx, agent.MethodFirewallRollback, params, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// FirewallTransaction 查询事务状态。
func (s *Service) FirewallTransaction(ctx context.Context) (json.RawMessage, error) {
	var out json.RawMessage
	if err := s.client.Call(ctx, agent.MethodFirewallTransaction, nil, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// FirewallTemplates 读取内置模板。
func (s *Service) FirewallTemplates(ctx context.Context) (json.RawMessage, error) {
	var out json.RawMessage
	if err := s.client.Call(ctx, agent.MethodFirewallTemplates, nil, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// ---- M3：电源与计划任务 ----
//
// 与防火墙方法一致：参数与响应都用 json.RawMessage 直传。
// Agent 的返回结构会随版本演进，Server 二次编解码可能静默丢字段。

// PowerStatus 读取电源与调度状态。
func (s *Service) PowerStatus(ctx context.Context) (json.RawMessage, error) {
	var out json.RawMessage
	if err := s.client.Call(ctx, agent.MethodPowerStatus, nil, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// PowerReboot 排定重启。
func (s *Service) PowerReboot(ctx context.Context, params any) (json.RawMessage, error) {
	var out json.RawMessage
	if err := s.client.Call(ctx, agent.MethodPowerReboot, params, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// PowerCancel 取消待执行的重启。
func (s *Service) PowerCancel(ctx context.Context, params any) (json.RawMessage, error) {
	var out json.RawMessage
	if err := s.client.Call(ctx, agent.MethodPowerCancel, params, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// ScheduleList 读取全部计划任务。
func (s *Service) ScheduleList(ctx context.Context) (json.RawMessage, error) {
	var out json.RawMessage
	if err := s.client.Call(ctx, agent.MethodScheduleList, nil, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// ScheduleCreate 创建计划任务。
func (s *Service) ScheduleCreate(ctx context.Context, params any) (json.RawMessage, error) {
	var out json.RawMessage
	if err := s.client.Call(ctx, agent.MethodScheduleCreate, params, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// ScheduleUpdate 更新计划任务。
func (s *Service) ScheduleUpdate(ctx context.Context, params any) (json.RawMessage, error) {
	var out json.RawMessage
	if err := s.client.Call(ctx, agent.MethodScheduleUpdate, params, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// ScheduleDelete 删除计划任务。
func (s *Service) ScheduleDelete(ctx context.Context, params any) (json.RawMessage, error) {
	var out json.RawMessage
	if err := s.client.Call(ctx, agent.MethodScheduleDelete, params, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// ScheduleExecutions 读取执行记录。
func (s *Service) ScheduleExecutions(ctx context.Context, params any) (json.RawMessage, error) {
	var out json.RawMessage
	if err := s.client.Call(ctx, agent.MethodScheduleExecutions, params, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// ScheduleRunNow 立即执行一次任务。
func (s *Service) ScheduleRunNow(ctx context.Context, params any) (json.RawMessage, error) {
	var out json.RawMessage
	if err := s.client.Call(ctx, agent.MethodScheduleRunNow, params, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// ScheduleSync 把任务同步到 launchd。
func (s *Service) ScheduleSync(ctx context.Context) (json.RawMessage, error) {
	var out json.RawMessage
	if err := s.client.Call(ctx, agent.MethodScheduleSync, nil, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// ---- M4：启动项管理 ----

// ServicesList 列出服务。
func (s *Service) ServicesList(ctx context.Context, params any) (json.RawMessage, error) {
	var out json.RawMessage
	if err := s.client.Call(ctx, agent.MethodServicesList, params, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// ServiceDetail 读取单个服务的配置详情。
func (s *Service) ServiceDetail(ctx context.Context, params any) (json.RawMessage, error) {
	var out json.RawMessage
	if err := s.client.Call(ctx, agent.MethodServicesDetail, params, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// ServiceRegister 登记受管服务。
func (s *Service) ServiceRegister(ctx context.Context, params any) (json.RawMessage, error) {
	var out json.RawMessage
	if err := s.client.Call(ctx, agent.MethodServicesRegister, params, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// ServiceUnregister 取消登记。
func (s *Service) ServiceUnregister(ctx context.Context, params any) (json.RawMessage, error) {
	var out json.RawMessage
	if err := s.client.Call(ctx, agent.MethodServicesUnregister, params, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// ServiceControl 启停受管服务。
func (s *Service) ServiceControl(ctx context.Context, params any) (json.RawMessage, error) {
	var out json.RawMessage
	if err := s.client.Call(ctx, agent.MethodServicesControl, params, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// ---- M6：网络诊断与容器 ----

// DiagnosticsSnapshot 采集路由表与监听端口。
func (s *Service) DiagnosticsSnapshot(ctx context.Context) (json.RawMessage, error) {
	var out json.RawMessage
	if err := s.client.Call(ctx, agent.MethodDiagnosticsSnapshot, nil, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// TCPProbe 检测 TCP 连通性。
func (s *Service) TCPProbe(ctx context.Context, params any) (json.RawMessage, error) {
	var out json.RawMessage
	if err := s.client.Call(ctx, agent.MethodDiagnosticsTCPProbe, params, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// OrbStackContainers 列出容器。
func (s *Service) OrbStackContainers(ctx context.Context) (json.RawMessage, error) {
	var out json.RawMessage
	if err := s.client.Call(ctx, agent.MethodOrbStackContainers, nil, &out); err != nil {
		return nil, err
	}
	return out, nil
}
