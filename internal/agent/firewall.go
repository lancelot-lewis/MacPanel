package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/lancelot-lewis/MacPanel/internal/model"
	"github.com/lancelot-lewis/MacPanel/internal/platform/darwin/firewalld"
	"github.com/lancelot-lewis/MacPanel/internal/platform/darwin/pfrules"
)

// M2 新增的方法名。保持「域.动作」命名，便于审计检索。
const (
	MethodFirewallRulesGet    = "firewall.rules.get"
	MethodFirewallRulesSet    = "firewall.rules.set"
	MethodFirewallPreview     = "firewall.preview"
	MethodFirewallApply       = "firewall.apply"
	MethodFirewallConfirm     = "firewall.confirm"
	MethodFirewallRollback    = "firewall.rollback"
	MethodFirewallTemplates   = "firewall.templates"
	MethodFirewallTransaction = "firewall.transaction"
)

// FirewallDeps 是 M2 防火墙能力所需的依赖。
//
// 用接口注入而非直接 new：Agent 可以在测试里替换成 fake，
// 也能在非 macOS 环境下构造（此时功能返回未实现）。
type FirewallDeps struct {
	// Manager 应用事务。为 nil 时所有写方法返回「未实现」。
	Manager *firewalld.Manager
	// Rules 持久化（Agent 是唯一写入方）。
	Rules RuleStore
	// AnchorName 本应用 anchor 名。
	AnchorName string
	// RulePath anchor 规则文件路径（Agent 负责写入内容）。
	RulePath string
	// ConfirmTimeout 确认超时（用于展示）。
	ConfirmTimeout time.Duration
	// GeneratorVersion 记录到版本表，便于诊断。
	GeneratorVersion string
}

// RuleStore 是 Agent 侧的规则持久化接口。
type RuleStore interface {
	Load(ctx context.Context) (model.RuleSet, error)
	Save(ctx context.Context, rs model.RuleSet) (model.RuleSet, error)
	SaveRevision(ctx context.Context, content, hash string, ruleCount, enforcedCount int, genVer string) (int64, error)
	MarkRevision(ctx context.Context, id int64, status, errMsg string) error
	LastKnownGood(ctx context.Context, excludeID int64) (string, string, error)
}

// SetFirewallDeps 注入 M2 防火墙依赖。
// 未调用时相关方法返回「未实现」——这是刻意的：
// 非 macOS 或未配置时不应假装功能可用。
func (a *Agent) SetFirewallDeps(deps FirewallDeps) {
	a.fw = &deps
}

// 说明：M2 方法名已在 AllMethods() 中统一声明，
// 这里不再往 a.methods 追加，避免「构造时 4 个 + 追加 8 个」的双来源问题。

// firewallReady 检查依赖是否就绪。
func (a *Agent) firewallReady() error {
	if a.fw == nil || a.fw.Manager == nil || a.fw.Rules == nil {
		return model.NotImplemented("IPv6 防火墙读写")
	}
	return nil
}

// ---- firewall.rules.get ----

func (a *Agent) handleRulesGet(ctx context.Context, params json.RawMessage) (any, error) {
	if err := requireNoParams(params); err != nil {
		return nil, err
	}
	if err := a.firewallReady(); err != nil {
		return nil, err
	}
	rs, err := a.fw.Rules.Load(ctx)
	if err != nil {
		return nil, model.Unavailable("读取规则失败：%v", err)
	}

	// 同时返回当前事务状态，前端据此决定是否显示「待确认」横幅。
	st, err := a.fw.Manager.State(ctx)
	if err != nil {
		a.log.Warn("读取事务状态失败", "error", err)
	}

	return rulesGetResult{
		RuleSet:     rs,
		Transaction: st,
	}, nil
}

type rulesGetResult struct {
	model.RuleSet
	Transaction firewalld.State `json:"transaction"`
}

// ---- firewall.rules.set ----

// rulesSetParams 是保存规则的参数。
type rulesSetParams struct {
	model.RuleSet
}

func (a *Agent) handleRulesSet(ctx context.Context, params json.RawMessage) (any, error) {
	if err := a.firewallReady(); err != nil {
		return nil, err
	}
	if len(params) == 0 {
		return nil, model.NewError(model.CodeInvalidArgument, "缺少规则集参数")
	}

	var p rulesSetParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, model.NewError(model.CodeInvalidArgument, "参数解析失败：%v", err)
	}

	// 先校验（Save 内部也会校验，这里提前做以便给出更清晰的错误）
	if err := p.RuleSet.Validate(); err != nil {
		return nil, err
	}
	saved, err := a.fw.Rules.Save(ctx, p.RuleSet)
	if err != nil {
		return nil, err
	}

	a.log.Info("规则已保存", "count", len(saved.Rules))

	// 保存后立即用回填了 ID 的规则生成预览，
	// 否则文本里的编号全为 0，排查时无法对应到具体规则。
	gen, verr := pfrules.Generate(saved, a.fw.AnchorName)
	if verr != nil {
		return nil, model.NewError(model.CodeInternal, "规则已保存但生成失败：%v", verr)
	}

	return previewResult{
		Content:      gen.Content,
		RuleCount:    gen.RuleCount,
		Enforced:     gen.Enforced,
		Skipped:      gen.Skipped,
		Verification: pfrules.Verify(gen.Content, saved.DefaultDenyInterfaces),
	}, nil
}

// ---- firewall.preview ----

// previewParams 允许预览未保存的规则集（用于「改了还没存」的即时预览）。
type previewParams struct {
	// RuleSet 为空时使用已保存的规则集。
	RuleSet *model.RuleSet `json:"ruleSet"`
}

type previewResult struct {
	Content      string                 `json:"content"`
	RuleCount    int                    `json:"ruleCount"`
	Enforced     []pfrules.EnforcedRule `json:"enforced"`
	Skipped      []pfrules.SkippedRule  `json:"skipped"`
	Verification pfrules.Verification   `json:"verification"`
}

func (a *Agent) handlePreview(ctx context.Context, params json.RawMessage) (any, error) {
	if err := a.firewallReady(); err != nil {
		return nil, err
	}

	var p previewParams
	if len(params) > 0 {
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, model.NewError(model.CodeInvalidArgument, "参数解析失败：%v", err)
		}
	}

	rs := model.RuleSet{}
	if p.RuleSet != nil {
		rs = *p.RuleSet
	} else {
		loaded, err := a.fw.Rules.Load(ctx)
		if err != nil {
			return nil, model.Unavailable("读取规则失败：%v", err)
		}
		rs = loaded
	}

	gen, err := pfrules.Generate(rs, a.fw.AnchorName)
	if err != nil {
		return nil, err
	}
	return previewResult{
		Content:      gen.Content,
		RuleCount:    gen.RuleCount,
		Enforced:     gen.Enforced,
		Skipped:      gen.Skipped,
		Verification: pfrules.Verify(gen.Content, rs.DefaultDenyInterfaces),
	}, nil
}

// ---- firewall.apply ----

// applyParams 是应用规则参数。
type applyParams struct {
	// RuleSet 为空时使用已保存的规则集。
	RuleSet *model.RuleSet `json:"ruleSet"`
}

type applyResult struct {
	RevisionID     int64                `json:"revisionId"`
	ContentHash    string               `json:"contentHash"`
	AppliedAt      time.Time            `json:"appliedAt"`
	Deadline       time.Time            `json:"deadline"`
	ConfirmTimeout int                  `json:"confirmTimeoutSeconds"`
	Warnings       []string             `json:"warnings,omitempty"`
	Verification   pfrules.Verification `json:"verification"`
}

// handleApply 生成并应用规则。
//
// 完整流程（每一步失败都不能跳过）：
//  1. 取规则集（入参或已保存）
//  2. 生成 PF 文本
//  3. 自校验不变量（不通过则拒绝，不触达 pfctl）
//  4. 写 anchor 文件（0600，root 专用）
//  5. 记录候选版本
//  6. 交给 Manager 执行：语法检查 → 快照 → 落盘状态 → 加载 → 进入 pending
func (a *Agent) handleApply(ctx context.Context, params json.RawMessage) (any, error) {
	if err := a.firewallReady(); err != nil {
		return nil, err
	}

	var p applyParams
	if len(params) > 0 {
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, model.NewError(model.CodeInvalidArgument, "参数解析失败：%v", err)
		}
	}

	rs := model.RuleSet{}
	if p.RuleSet != nil {
		rs = *p.RuleSet
	} else {
		loaded, err := a.fw.Rules.Load(ctx)
		if err != nil {
			return nil, model.Unavailable("读取规则失败：%v", err)
		}
		rs = loaded
	}

	// 2. 生成
	gen, err := pfrules.Generate(rs, a.fw.AnchorName)
	if err != nil {
		return nil, err
	}

	// 3. 自校验：不通过就拒绝，绝不把可疑规则送去 pfctl
	verify := pfrules.Verify(gen.Content, rs.DefaultDenyInterfaces)
	if !verify.OK {
		msgs := make([]string, 0, len(verify.SyntaxErrors)+len(verify.InvariantViolations))
		for _, v := range verify.SyntaxErrors {
			msgs = append(msgs, fmt.Sprintf("第 %d 行：%s", v.Line, v.Message))
		}
		for _, v := range verify.InvariantViolations {
			msgs = append(msgs, fmt.Sprintf("不变量 %d：%s", v.Invariant, v.Message))
		}
		return nil, model.NewError(model.CodeInvalidArgument,
			"规则未通过自校验，已拒绝应用：%s", joinLimited(msgs, 5))
	}

	// 4. 写 anchor 文件
	if err := writeAnchorFile(a.fw.RulePath, gen.Content); err != nil {
		return nil, model.NewError(model.CodeInternal, "写入规则文件失败：%v", err)
	}

	// 5. 记录候选版本
	hash := firewalld.HashContent(gen.Content)
	revisionID, err := a.fw.Rules.SaveRevision(ctx, gen.Content, hash,
		gen.RuleCount, len(gen.Enforced), a.fw.GeneratorVersion)
	if err != nil {
		return nil, err
	}

	// 6. 执行事务
	applyRes, err := a.fw.Manager.Apply(ctx, gen.Content, revisionID, hash)
	if err != nil {
		_ = a.fw.Rules.MarkRevision(ctx, revisionID, "failed", model.AsError(err).Message)
		return nil, err
	}
	if err := a.fw.Rules.MarkRevision(ctx, revisionID, "applied", ""); err != nil {
		a.log.Warn("标记版本状态失败", "error", err)
	}

	a.log.Warn("防火墙规则已应用，等待确认",
		"revision", revisionID, "rules", gen.RuleCount, "deadline", applyRes.State.Deadline.Format(time.RFC3339))

	return applyResult{
		RevisionID:     revisionID,
		ContentHash:    hash,
		AppliedAt:      applyRes.State.AppliedAt,
		Deadline:       applyRes.State.Deadline,
		ConfirmTimeout: int(a.fw.ConfirmTimeout.Seconds()),
		Warnings:       applyRes.Warnings,
		Verification:   verify,
	}, nil
}

// ---- firewall.confirm ----

func (a *Agent) handleConfirm(ctx context.Context, params json.RawMessage) (any, error) {
	if err := a.firewallReady(); err != nil {
		return nil, err
	}
	if err := requireNoParams(params); err != nil {
		return nil, err
	}

	st, err := a.fw.Manager.Confirm(ctx)
	if err != nil {
		return nil, err
	}
	if st.RevisionID > 0 {
		if err := a.fw.Rules.MarkRevision(ctx, st.RevisionID, "applied", ""); err != nil {
			a.log.Warn("标记版本状态失败", "error", err)
		}
	}
	a.log.Info("防火墙规则已确认", "revision", st.RevisionID)
	return st, nil
}

// ---- firewall.rollback ----

// rollbackParams 可指定原因，便于审计。
type rollbackParams struct {
	Reason string `json:"reason"`
}

func (a *Agent) handleRollback(ctx context.Context, params json.RawMessage) (any, error) {
	if err := a.firewallReady(); err != nil {
		return nil, err
	}

	var p rollbackParams
	if len(params) > 0 {
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, model.NewError(model.CodeInvalidArgument, "参数解析失败：%v", err)
		}
	}
	if len(p.Reason) > 200 {
		return nil, model.NewError(model.CodeInvalidArgument, "回滚原因过长（最多 200 字符）")
	}

	st, err := a.fw.Manager.Rollback(ctx, p.Reason)
	if err != nil {
		return nil, err
	}
	if st.RevisionID > 0 {
		if err := a.fw.Rules.MarkRevision(ctx, st.RevisionID, "rolled_back", p.Reason); err != nil {
			a.log.Warn("标记版本状态失败", "error", err)
		}
	}
	a.log.Warn("防火墙规则已回滚", "revision", st.RevisionID, "reason", p.Reason)
	return st, nil
}

// ---- firewall.transaction ----

func (a *Agent) handleTransaction(ctx context.Context, params json.RawMessage) (any, error) {
	if err := requireNoParams(params); err != nil {
		return nil, err
	}
	if err := a.firewallReady(); err != nil {
		return nil, err
	}

	// 读事务状态时顺便检查超时：这样即使后台定时器没跑，
	// 用户打开页面也能看到「已回滚」的正确状态。
	if _, err := a.fw.Manager.PurgeExpired(ctx); err != nil {
		a.log.Warn("处理超时事务失败", "error", err)
	}

	st, err := a.fw.Manager.State(ctx)
	if err != nil {
		return nil, err
	}
	return transactionResult{State: st}, nil
}

type transactionResult struct {
	State firewalld.State `json:"state"`
}

// ---- firewall.templates ----

func (a *Agent) handleTemplates(ctx context.Context, params json.RawMessage) (any, error) {
	if err := requireNoParams(params); err != nil {
		return nil, err
	}
	// 模板是静态数据，不依赖 Manager 就绪（用户可以看模板但无法应用）。
	return map[string]any{"items": pfrules.BuiltinTemplates()}, nil
}

// joinLimited 拼接错误信息并限制条数，避免响应过长。
func joinLimited(msgs []string, max int) string {
	if len(msgs) <= max {
		return joinStrings(msgs, "；")
	}
	return joinStrings(msgs[:max], "；") + fmt.Sprintf(" 等 %d 项", len(msgs))
}

func joinStrings(ss []string, sep string) string {
	out := ""
	for i, s := range ss {
		if i > 0 {
			out += sep
		}
		out += s
	}
	return out
}
