// Package app 是 Server 侧的应用服务层：把 IPC 调用封装成类型化方法。
//
// 这样 HTTP handler 不直接拼接方法名与 JSON，也不会误以为「调用成功即数据有效」。
// 依赖注入客户端接口，便于测试中替换为桩实现。
package app

import (
	"context"

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
