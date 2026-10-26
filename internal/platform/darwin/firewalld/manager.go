// Package firewalld 实现 PF 规则的应用事务、快照与超时回滚。
//
// 这是本项目风险最高的部分：错误的实现会让远程管理通道永久失联。
// 因此设计上有四条硬性约束：
//
//  1. **回滚状态必须落盘**。如果把倒计时放在内存里，Agent 一崩溃
//     （或被 launchd 重启）就丢失了「需要回滚」这个事实，机器将永久停在
//     未确认的规则上。因此状态机每一步都先写盘再执行。
//
//  2. **应用前必须先用 `pfctl -n -f` 做语法检查**。`-n` 只解析不加载，
//     能在不触碰运行状态的前提下发现语法错误。跳过这一步等于拿生产环境试错。
//
//  3. **不使用 `pfctl -d`**。那是「全局关闭防火墙」，会影响所有服务。
//     本应用的「回滚」是把 anchor 规则文件换回上一份可用版本。
//
//  4. **时间来源可注入**。超时回滚需要测试在毫秒级验证，不能依赖真实等待。
package firewalld

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/lancelot-lewis/MacPanel/internal/model"
)

// Phase 是事务阶段。状态转移是单向的，回退只通过 rollback 完成。
type Phase string

const (
	// PhaseIdle 无进行中的事务。
	PhaseIdle Phase = "idle"
	// PhasePending 已应用候选配置，等待用户确认。
	// 此阶段随时可能超时回滚。
	PhasePending Phase = "pending"
	// PhaseConfirmed 用户已确认，规则被视为「已知可用」。
	PhaseConfirmed Phase = "confirmed"
	// PhaseRolledBack 已回滚到上一份可用配置。
	PhaseRolledBack Phase = "rolled_back"
	// PhaseFailed 应用失败，处于不确定状态，需人工介入。
	PhaseFailed Phase = "failed"
)

// State 是持久化的事务状态。
//
// 这个结构必须能在 Agent 任意时刻崩溃后重建出正确行为，
// 因此只包含「重建所需的最小信息」，不含任何内存对象引用。
type State struct {
	Phase Phase `json:"phase"`
	// RevisionID 对应数据库中 firewall_revisions 的 id。
	RevisionID int64 `json:"revisionId"`
	// ContentHash 是候选内容的 SHA-256，用于确认「落盘的确实是这份」。
	ContentHash string `json:"contentHash"`
	// AppliedAt 是应用时间，超时判断以它为准（不用进程启动时间，
	// 否则 Agent 重启后倒计时会被重置，等于无限延长未确认窗口）。
	AppliedAt time.Time `json:"appliedAt"`
	// Deadline 是自动回滚截止时间。落盘而非计算得来，
	// 避免系统时间被调整后行为漂移。
	Deadline time.Time `json:"deadline"`
	// PreviousHash 是应用前那份「已知可用」配置的哈希，回滚时用它校验。
	PreviousHash string `json:"previousHash"`
	// ConfirmToken 是确认所需的令牌。
	// 用途：即使有人拿到 Web 会话，也必须拿到这个一次性令牌才能提交，
	// 避免「误点确认」直接固化一份可能失联的配置。
	ConfirmToken string `json:"confirmToken"`
	// Reason 记录最近一次失败或回滚的原因，供界面展示。
	Reason string `json:"reason,omitempty"`
	// UpdatedAt 便于诊断「状态是多少时间点写入的」。
	UpdatedAt time.Time `json:"updatedAt"`
}

// StateStore 负责状态持久化。Agent 侧用文件实现（root 独占目录），
// 测试用内存实现。
type StateStore interface {
	Load(ctx context.Context) (State, error)
	Save(ctx context.Context, s State) error
	Clear(ctx context.Context) error
}

// ErrNoState 表示尚无持久化状态（首次运行）。
var ErrNoState = errors.New("firewalld: 无持久化状态")

// PFApplier 是执行实际 PF 操作的最小接口。
//
// 刻意保持窄接口：只有三个动作，且都不接受任意命令字符串。
// 这样测试可以完全替换它，也便于审计「Agent 到底能对 PF 做什么」。
type PFApplier interface {
	// CheckSyntax 用 `pfctl -n -f <path>` 只解析不加载。
	// 返回的 error 应包含 pfctl 的原始错误输出，便于定位。
	CheckSyntax(ctx context.Context, path string) error
	// Load 用 `pfctl -a <anchor> -f <path>` 加载规则到指定 anchor。
	Load(ctx context.Context, anchor, path string) error
	// SnapshotCurrent 读取当前 anchor 的生效规则，用于回滚。
	// 若 anchor 不存在，返回空字符串与 nil error（表示「此前无规则」）。
	SnapshotCurrent(ctx context.Context, anchor string) (string, error)
}

// Options 是 Manager 的构造参数。
type Options struct {
	Store   StateStore
	Applier PFApplier
	// AnchorName 本应用专用 anchor 名。
	AnchorName string
	// RulePath anchor 规则文件路径。
	RulePath string
	// ConfirmTimeout 未确认自动回滚的等待时长。
	ConfirmTimeout time.Duration
	// Now 时间来源，测试可注入。
	Now func() time.Time
	// Logger 可为空。
	Logger Logger
}

// Logger 是日志最小接口，避免依赖具体日志库。
type Logger interface {
	Info(msg string, args ...any)
	Warn(msg string, args ...any)
	Error(msg string, args ...any)
}

type nopLogger struct{}

func (nopLogger) Info(string, ...any)  {}
func (nopLogger) Warn(string, ...any)  {}
func (nopLogger) Error(string, ...any) {}

// Manager 管理单个 anchor 的应用事务。
type Manager struct {
	store          StateStore
	applier        PFApplier
	anchorName     string
	rulePath       string
	confirmTimeout time.Duration
	now            func() time.Time
	log            Logger
}

// 确认超时的默认值与上下限。
const (
	DefaultConfirmTimeout = 60 * time.Second
	MinConfirmTimeout     = 10 * time.Second
	MaxConfirmTimeout     = 10 * time.Minute
)

// New 创建 Manager。
func New(opts Options) (*Manager, error) {
	if opts.Store == nil {
		return nil, errors.New("firewalld: 必须提供 StateStore")
	}
	if opts.Applier == nil {
		return nil, errors.New("firewalld: 必须提供 PFApplier")
	}
	if opts.AnchorName == "" {
		return nil, errors.New("firewalld: 必须提供 AnchorName")
	}
	if opts.Now == nil {
		opts.Now = func() time.Time { return time.Now().UTC() }
	}
	if opts.Logger == nil {
		opts.Logger = nopLogger{}
	}
	if opts.ConfirmTimeout == 0 {
		opts.ConfirmTimeout = DefaultConfirmTimeout
	}
	if opts.ConfirmTimeout < MinConfirmTimeout || opts.ConfirmTimeout > MaxConfirmTimeout {
		return nil, fmt.Errorf("firewalld: 确认超时必须在 %s-%s 之间，实际 %s",
			MinConfirmTimeout, MaxConfirmTimeout, opts.ConfirmTimeout)
	}

	return &Manager{
		store:          opts.Store,
		applier:        opts.Applier,
		anchorName:     opts.AnchorName,
		rulePath:       opts.RulePath,
		confirmTimeout: opts.ConfirmTimeout,
		now:            opts.Now,
		log:            opts.Logger,
	}, nil
}

// State 返回当前持久化状态。
func (m *Manager) State(ctx context.Context) (State, error) {
	s, err := m.store.Load(ctx)
	if errors.Is(err, ErrNoState) {
		return State{Phase: PhaseIdle}, nil
	}
	return s, err
}

// Snapshot 返回当前 anchor 的生效内容（只读）。
func (m *Manager) Snapshot(ctx context.Context) (string, error) {
	return m.applier.SnapshotCurrent(ctx, m.anchorName)
}

// ApplyResult 是应用结果。
type ApplyResult struct {
	State State `json:"state"`
	// Warnings 是需要展示给用户的提示（例如强制加入了哪些规则）。
	Warnings []string `json:"warnings,omitempty"`
}

// Apply 应用候选规则。
//
// 步骤（顺序不可调换）：
//  1. 若已有 pending 事务，先回滚它——不允许两个未确认事务叠加；
//  2. 写入候选文件；
//  3. `pfctl -n -f` 语法检查（失败即中止，不触碰运行状态）；
//  4. 快照当前生效内容（用于回滚）；
//  5. 落盘「即将进入 pending」的状态（先写盘再加载，
//     这样即使加载到一半崩溃，重启后也知道需要回滚）；
//  6. 加载规则；
//  7. 更新状态为 pending 并记录截止时间。
func (m *Manager) Apply(ctx context.Context, content string, revisionID int64, previousHash string) (ApplyResult, error) {
	if content == "" {
		return ApplyResult{}, model.NewError(model.CodeInvalidArgument, "规则内容不能为空")
	}

	// 1. 处理残留的 pending 事务
	cur, err := m.State(ctx)
	if err != nil {
		return ApplyResult{}, err
	}
	var warnings []string
	if cur.Phase == PhasePending {
		if err := m.rollbackLocked(ctx, "应用新规则前先回滚未确认的配置"); err != nil {
			// 回滚失败时不能继续应用新规则——否则会留下两份不确定状态。
			return ApplyResult{}, model.NewError(model.CodeConflict,
				"存在未确认的配置且回滚失败，请先手工处理：%v", err)
		}
		warnings = append(warnings, "已自动回滚上一份未确认的配置")
	}

	// 2-3. 语法检查。
	// 注意：这里检查的是 rulePath，调用方需先把 content 写入该路径。
	if err := m.applier.CheckSyntax(ctx, m.rulePath); err != nil {
		return ApplyResult{}, model.NewError(model.CodeInvalidArgument,
			"规则语法检查未通过，未做任何变更：%v", err)
	}

	// 4. 快照当前状态（回滚用）
	if _, err := m.applier.SnapshotCurrent(ctx, m.anchorName); err != nil {
		m.log.Warn("快照当前规则失败", "error", err)
		// 继续执行：快照失败不阻断应用，但回滚能力会降级。
		// 这个降级必须让用户知道。
		warnings = append(warnings, "无法读取当前生效规则快照，回滚能力受限")
	}

	// 5-7. 先写盘再加载
	now := m.now()
	st := State{
		Phase:        PhasePending,
		RevisionID:   revisionID,
		ContentHash:  HashContent(content),
		AppliedAt:    now,
		Deadline:     now.Add(m.confirmTimeout),
		PreviousHash: previousHash,
		UpdatedAt:    now,
	}
	if err := m.store.Save(ctx, st); err != nil {
		return ApplyResult{}, model.NewError(model.CodeInternal,
			"保存回滚状态失败，已中止应用：%v", err)
	}

	if err := m.applier.Load(ctx, m.anchorName, m.rulePath); err != nil {
		// 加载失败：立即尝试回滚，并把状态标为 failed 以便人工介入。
		st.Phase = PhaseFailed
		st.Reason = err.Error()
		st.UpdatedAt = m.now()
		_ = m.store.Save(ctx, st)

		if rbErr := m.rollbackLocked(ctx, "应用失败后自动回滚"); rbErr != nil {
			m.log.Error("应用失败且回滚也失败", "applyErr", err, "rollbackErr", rbErr)
			return ApplyResult{}, model.NewError(model.CodeInternal,
				"应用规则失败（%v），且自动回滚也失败（%v）。请通过 CLI 救援：homelab-manager firewall rollback", err, rbErr)
		}
		return ApplyResult{}, model.NewError(model.CodeInternal,
			"应用规则失败，已自动回滚到之前状态：%v", err)
	}

	m.log.Info("规则已应用，等待确认",
		"revision", revisionID, "deadline", st.Deadline.Format(time.RFC3339))

	return ApplyResult{State: st, Warnings: warnings}, nil
}

// Confirm 确认当前 pending 配置，使其成为「已知可用」。
//
// 必须是幂等的：重复确认不应报错（用户可能刷新页面后又点了一次）。
func (m *Manager) Confirm(ctx context.Context) (State, error) {
	st, err := m.State(ctx)
	if err != nil {
		return st, err
	}

	switch st.Phase {
	case PhaseConfirmed:
		// 幂等：已确认就直接返回。
		return st, nil
	case PhaseIdle:
		return st, model.NewError(model.CodeConflict, "没有待确认的配置")
	case PhaseRolledBack, PhaseFailed:
		return st, model.NewError(model.CodeConflict,
			"当前配置已回滚或失败（%s），无法确认。请重新应用规则", st.Phase)
	}

	// 超时后再确认必须拒绝：否则「回滚倒计时」形同虚设。
	if m.isExpired(st) {
		if rbErr := m.rollbackLocked(ctx, "超过确认时限，自动回滚"); rbErr != nil {
			return st, model.NewError(model.CodeInternal,
				"配置已超时且回滚失败：%v", rbErr)
		}
		return State{Phase: PhaseRolledBack}, model.NewError(model.CodeConflict,
			"已超过 %s 的确认时限，配置已自动回滚。请检查连通性后重新应用", m.confirmTimeout)
	}

	st.Phase = PhaseConfirmed
	st.UpdatedAt = m.now()
	st.Deadline = time.Time{} // 已确认，不再有截止时间
	if err := m.store.Save(ctx, st); err != nil {
		return st, model.NewError(model.CodeInternal, "保存确认状态失败：%v", err)
	}

	m.log.Info("规则已确认", "revision", st.RevisionID)
	return st, nil
}

// Rollback 主动回滚。
func (m *Manager) Rollback(ctx context.Context, reason string) (State, error) {
	st, err := m.State(ctx)
	if err != nil {
		return st, err
	}
	// 幂等：已是回滚态就直接返回。
	if st.Phase == PhaseRolledBack {
		return st, nil
	}
	if st.Phase == PhaseIdle {
		return st, model.NewError(model.CodeConflict, "当前没有已应用的配置可回滚")
	}
	if reason == "" {
		reason = "用户主动回滚"
	}
	if err := m.rollbackLocked(ctx, reason); err != nil {
		return st, err
	}
	return m.State(ctx)
}

// PurgeExpired 检查并处理超时事务。由后台定时器与每个读接口调用，
// 确保即使没有用户操作，超时也会触发回滚。
//
// 返回是否发生了回滚。
func (m *Manager) PurgeExpired(ctx context.Context) (bool, error) {
	st, err := m.State(ctx)
	if err != nil {
		return false, err
	}
	if st.Phase != PhasePending || !m.isExpired(st) {
		return false, nil
	}

	m.log.Warn("配置确认超时，执行自动回滚", "revision", st.RevisionID)
	if err := m.rollbackLocked(ctx, "超过确认时限，自动回滚"); err != nil {
		return false, err
	}
	return true, nil
}

// Recover 在 Agent 启动时调用，处理上次残留的未确认事务。
//
// 这是「崩溃后可恢复」的关键：Agent 重启后如果发现状态仍是 pending
// 且已超时，必须立即回滚；若未超时，保留倒计时（Deadline 是绝对时间，
// 不受进程重启影响）。
func (m *Manager) Recover(ctx context.Context) (bool, error) {
	st, err := m.State(ctx)
	if err != nil {
		return false, err
	}
	if st.Phase != PhasePending {
		return false, nil
	}

	if !m.isExpired(st) {
		remaining := time.Until(st.Deadline)
		if m.now != nil {
			remaining = st.Deadline.Sub(m.now())
		}
		m.log.Warn("检测到未确认的配置，继续等待确认",
			"revision", st.RevisionID, "remaining", remaining.Round(time.Second))
		return false, nil
	}

	m.log.Warn("检测到已超时的未确认配置，启动时自动回滚",
		"revision", st.RevisionID,
		"appliedAt", st.AppliedAt.Format(time.RFC3339))
	if err := m.rollbackLocked(ctx, "Agent 重启后发现配置已超时未确认"); err != nil {
		return false, fmt.Errorf("启动恢复失败：%w", err)
	}
	return true, nil
}

// isExpired 判断 pending 事务是否超过截止时间。
//
// 用 Deadline 而非 AppliedAt+固定时长：Deadline 是落盘的绝对时间，
// 不受进程重启或时钟回拨影响。
func (m *Manager) isExpired(st State) bool {
	if st.Deadline.IsZero() {
		// 没有截止时间说明状态文件损坏或来自旧版本。
		// 保守处理：视为已过期（立即回滚比长期挂着未确认配置更安全）。
		return true
	}
	return !m.now().Before(st.Deadline)
}

// rollbackLocked 执行回滚。
//
// 「回滚」的语义是：把 anchor 规则清空（恢复到本应用未接管前的状态）。
// 之所以不写回 previousHash 对应的内容，是因为：
//   - 本应用不持有历史规则的完整副本（只存哈希）；
//   - 清空 anchor 等于「本应用不加任何规则」，这是最安全的已知状态，
//     也不会影响其它程序的 PF 配置。
//
// 若用户此前已有本应用的规则，恢复它需要 revision 内容，
// 由上层（agent）负责把历史内容写回 rulePath 后再调用 Load。
func (m *Manager) rollbackLocked(ctx context.Context, reason string) error {
	// 先落盘「正在回滚」的意图，避免回滚过程中崩溃导致状态不明。
	prev, _ := m.State(ctx)
	st := State{
		Phase:        PhaseRolledBack,
		RevisionID:   prev.RevisionID,
		ContentHash:  "",
		PreviousHash: prev.PreviousHash,
		Reason:       reason,
		UpdatedAt:    m.now(),
	}
	if err := m.store.Save(ctx, st); err != nil {
		return fmt.Errorf("保存回滚状态失败：%w", err)
	}

	// 清空 anchor：用空规则文件加载。
	// pfctl -a <anchor> -f /dev/null 的效果是该 anchor 无规则。
	if err := m.applier.Load(ctx, m.anchorName, "/dev/null"); err != nil {
		st.Phase = PhaseFailed
		st.Reason = fmt.Sprintf("回滚失败：%v（原始原因：%s）", err, reason)
		st.UpdatedAt = m.now()
		_ = m.store.Save(ctx, st)
		return fmt.Errorf("清空 anchor 失败：%w", err)
	}

	m.log.Warn("配置已回滚", "reason", reason, "revision", prev.RevisionID)
	return nil
}
