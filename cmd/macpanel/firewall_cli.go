package main

// firewall 子命令：本地终端的防火墙救援通道。
//
// 为什么必须有它（PLAN.md 第 5.2 节「必须考虑 Agent 崩溃、GUI 关闭、远程连接中断」）：
//   网页上的「回滚」按钮依赖 Web Server、IPC、Agent 三跳都通。
//   而恰恰是「规则把 SSH 挡在外面」这种故障会让这条链路断掉，
//   此时浏览器点不动，唯一能救回来的就是本地终端。
//
// 因此本文件刻意**不经过 IPC**：直接读 root 独占的事务状态文件、
// 直接调用 firewalld.Manager。Agent 挂了、Server 起不来、端口被挡住，
// 这些命令依然可用。
//
// 安全边界与面板完全一致：仍然只走 firewalld → PFExecutor → pfctlGuard，
// 没有新的特权路径。本文件不引入任何新的 exec 调用。

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/lancelot-lewis/MacPanel/internal/config"
	"github.com/lancelot-lewis/MacPanel/internal/model"
	"github.com/lancelot-lewis/MacPanel/internal/platform"
	"github.com/lancelot-lewis/MacPanel/internal/platform/darwin"
	"github.com/lancelot-lewis/MacPanel/internal/platform/darwin/firewalld"
)

func runFirewallCLI(args []string) error {
	if len(args) == 0 {
		printFirewallUsage()
		return errors.New("缺少 firewall 子命令")
	}
	switch args[0] {
	case "status":
		return runFirewallStatus(args[1:])
	case "rollback":
		return runFirewallRollback(args[1:])
	case "unload":
		return runFirewallUnload(args[1:])
	case "help", "--help", "-h":
		printFirewallUsage()
		return nil
	default:
		printFirewallUsage()
		return fmt.Errorf("未知 firewall 子命令: %s", args[0])
	}
}

func printFirewallUsage() {
	fmt.Print(`用法:
  macpanel firewall <status|rollback|unload> [选项]

子命令:
  status    查看 PF 状态、事务阶段、回滚倒计时（纯只读）
  rollback  回滚当前事务，清空本应用 anchor 规则（幂等）
  unload    无条件清空本应用 anchor，用于规则导致失联时的最后手段

选项:
  -dev      开发模式：读写 ./.dev 下的状态文件
  -json     机器可读输出
  -dry-run  只校验参数，不真正调用 pfctl

说明:
  非 root 运行时自动进入 dry-run：输出与流程一致，但不会改动系统。
  这三个命令都不经过 Web Server 与 IPC，因此 Agent 未运行时同样可用。
  绝不会执行 pfctl -d，也不会触碰其它程序的 PF 配置。
`)
}

// ---- status ----

func runFirewallStatus(args []string) error {
	env, err := openFirewallEnv("status", args)
	if err != nil {
		return err
	}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	collector := darwin.New(platform.DefaultConfig(env.cfg.AnchorName, env.cfg.AnchorPath))
	fw, fwErr := collector.FirewallStatus(ctx)

	st, stErr := env.manager.State(ctx)

	out := buildFirewallStatus(env, fw, fwErr, st, stErr)
	if env.asJSON {
		return printJSON(out)
	}
	printFirewallStatus(out)
	return nil
}

// ---- rollback / unload ----

func runFirewallRollback(args []string) error {
	env, err := openFirewallEnv("rollback", args)
	if err != nil {
		return err
	}

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	st, err := env.manager.Rollback(ctx, "命令行救援回滚")
	if err != nil {
		// idle 是最常见的「无需回滚」：此时若 anchor 里还有残留规则，
		// 提示改用 unload，而不是让用户以为命令失效。
		if model.AsError(err).Code == model.CodeConflict {
			return fmt.Errorf("%w\n若 anchor 中仍有本应用的规则残留，请改用：macpanel firewall unload", err)
		}
		return err
	}

	if env.asJSON {
		return printJSON(map[string]any{
			"dryRun": env.dryRun,
			"phase":  string(st.Phase),
			"reason": st.Reason,
		})
	}
	fmt.Printf("已回滚%s\n", dryRunSuffix(env.dryRun))
	fmt.Printf("  阶段 %s\n", phaseText(st.Phase))
	if st.RevisionID != 0 {
		fmt.Printf("  涉及修订 #%d\n", st.RevisionID)
	}
	if st.Reason != "" {
		fmt.Printf("  原因 %s\n", st.Reason)
	}
	fmt.Println("  本应用 anchor 已清空，其它程序的 PF 配置未受影响")
	return nil
}

func runFirewallUnload(args []string) error {
	env, err := openFirewallEnv("unload", args)
	if err != nil {
		return err
	}

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	st, err := env.manager.Unload(ctx, "命令行强制卸载本应用规则")
	if err != nil {
		return err
	}

	if env.asJSON {
		return printJSON(map[string]any{
			"dryRun": env.dryRun,
			"phase":  string(st.Phase),
			"reason": st.Reason,
		})
	}
	fmt.Printf("已清空本应用 anchor%s\n", dryRunSuffix(env.dryRun))
	fmt.Printf("  anchor %s\n", env.cfg.AnchorName)
	fmt.Printf("  规则文件 %s（文件内容保留，未删除）\n", env.cfg.AnchorPath)
	fmt.Printf("  阶段 %s\n", phaseText(st.Phase))
	fmt.Println("  提示：若不再需要本应用接管 PF，请另行移除 /etc/pf.conf 中的 anchor 引用")
	return nil
}

// ---- 公共装配 ----

// firewallEnv 汇总 firewall 子命令的运行环境。
type firewallEnv struct {
	cfg     config.Config
	manager *firewalld.Manager
	dryRun  bool
	asJSON  bool
}

// openFirewallEnv 解析公共参数并装配 Manager。
//
// dry-run 的判定与 Agent 内的 wireFirewall 保持一致：
// 只要不是 root 就一定不碰 pfctl。宁可让用户看一段「什么都没发生」的输出，
// 也不能让非特权进程悄悄改了 PF 或反过来因为权限错误留下半截状态。
func openFirewallEnv(sub string, args []string) (firewallEnv, error) {
	var env firewallEnv

	fs := flag.NewFlagSet("firewall "+sub, flag.ContinueOnError)
	devMode := fs.Bool("dev", false, "开发模式")
	fs.BoolVar(&env.asJSON, "json", false, "机器可读输出")
	forceDry := fs.Bool("dry-run", false, "不真正调用 pfctl")
	if err := fs.Parse(args); err != nil {
		return env, err
	}

	cfg, err := loadConfig(config.ModeAgent, *devMode)
	if err != nil {
		return env, err
	}
	env.cfg = cfg
	env.dryRun = *forceDry || *devMode || os.Geteuid() != 0

	// 状态文件路径必须与 Agent 的一致，否则 CLI 会读到另一份状态，
	// 出现「面板说 pending、命令行说 idle」这种最难的排障题。
	stateStore, err := firewalld.NewFileStore(FirewallStatePath(cfg))
	if err != nil {
		return env, err
	}
	applier, err := firewalld.NewPFExecutor(cfg.AnchorName, cfg.AnchorPath, env.dryRun)
	if err != nil {
		return env, err
	}
	env.manager, err = firewalld.New(firewalld.Options{
		Store:          stateStore,
		Applier:        applier,
		AnchorName:     cfg.AnchorName,
		RulePath:       cfg.AnchorPath,
		PFConfPath:     cfg.PFConfPath,
		ConfirmTimeout: firewalld.DefaultConfirmTimeout,
		Logger:         cliFirewallLogger{dryRun: env.dryRun},
	})
	if err != nil {
		return env, err
	}
	return env, nil
}

// FirewallStatePath 返回防火墙事务状态文件路径。
//
// 放在 Socket 同级目录是为了继承它的权限（0750 root:wheel）：
// 状态文件决定「要不要回滚」，绝不能被非特权账户改写。
// Agent 装配与本 CLI 必须共用这一个函数——各写一份会导致状态漂移。
func FirewallStatePath(cfg config.Config) string {
	return filepath.Join(filepath.Dir(cfg.SocketPath), "firewall-state.json")
}

// ---- 输出构造 ----

type transactionView struct {
	Phase      string     `json:"phase"`
	PhaseText  string     `json:"phaseText"`
	RevisionID int64      `json:"revisionId,omitempty"`
	AppliedAt  *time.Time `json:"appliedAt,omitempty"`
	Deadline   *time.Time `json:"deadline,omitempty"`
	// Remaining 只在 pending 阶段给出：这是「还有多久自动恢复」的唯一提示。
	Remaining string `json:"remaining,omitempty"`
	Expired   bool   `json:"expired,omitempty"`
	Reason    string `json:"reason,omitempty"`
}

type firewallStatusOutput struct {
	DryRun      bool                  `json:"dryRun"`
	StatePath   string                `json:"statePath"`
	Firewall    *model.FirewallStatus `json:"firewall,omitempty"`
	FirewallErr string                `json:"firewallError,omitempty"`
	Transaction transactionView       `json:"transaction"`
	TxErr       string                `json:"transactionError,omitempty"`
	Hints       []string              `json:"hints,omitempty"`
}

func buildFirewallStatus(env firewallEnv, fw model.FirewallStatus, fwErr error, st firewalld.State, stErr error) firewallStatusOutput {
	out := firewallStatusOutput{
		DryRun:    env.dryRun,
		StatePath: FirewallStatePath(env.cfg),
	}
	if fwErr != nil {
		out.FirewallErr = fwErr.Error()
	} else {
		fwCopy := fw
		out.Firewall = &fwCopy
	}

	out.Transaction = transactionView{
		Phase:      string(st.Phase),
		PhaseText:  phaseText(st.Phase),
		RevisionID: st.RevisionID,
		Reason:     st.Reason,
	}
	if stErr != nil {
		out.TxErr = stErr.Error()
	}
	if !st.AppliedAt.IsZero() {
		t := st.AppliedAt
		out.Transaction.AppliedAt = &t
	}
	if !st.Deadline.IsZero() {
		t := st.Deadline
		out.Transaction.Deadline = &t
		if st.Phase == firewalld.PhasePending {
			remaining := time.Until(st.Deadline)
			if remaining > 0 {
				out.Transaction.Remaining = remaining.Round(time.Second).String()
			} else {
				out.Transaction.Expired = true
				out.Hints = append(out.Hints,
					"待确认配置已超过截止时间：Agent 存活时会自动回滚；若 Agent 未运行，请执行 firewall rollback")
			}
		}
	}

	out.Hints = append(out.Hints, statusHints(env, fw, fwErr, st)...)
	return out
}

// statusHints 把「当前状态意味着什么、下一步该做什么」写出来。
//
// 这不是锦上添花：半夜 SSH 断连、只有终端可用时，
// 用户需要的不是一串字段，而是一句「执行这条命令」。
func statusHints(env firewallEnv, fw model.FirewallStatus, fwErr error, st firewalld.State) []string {
	var hints []string
	if env.dryRun {
		hints = append(hints, "当前处于 dry-run：输出真实，但不会改动 PF（非 root 或显式指定 -dry-run）")
	}
	if fwErr == nil && fw.PF.Enabled != nil && !*fw.PF.Enabled {
		hints = append(hints, "PF 当前处于关闭状态：本应用从不开关 PF，这是系统或其它程序的状态")
	}
	if fwErr == nil && fw.Anchor.Present && !fw.Anchor.Referenced {
		hints = append(hints, fmt.Sprintf(
			"规则文件存在但 %s 未引用本 anchor，规则不会生效", "/etc/pf.conf"))
	}
	switch st.Phase {
	case firewalld.PhasePending:
		hints = append(hints, "存在待确认配置：不想保留可执行 firewall rollback 立即恢复")
	case firewalld.PhaseConfirmed:
		hints = append(hints, "当前规则已确认生效；firewall rollback 可将其撤回")
	case firewalld.PhaseFailed:
		hints = append(hints, "上次事务以失败告终，建议执行 firewall rollback 让状态回到干净值")
	}
	return hints
}

func printFirewallStatus(o firewallStatusOutput) {
	fmt.Println("== IPv6 防火墙 ==")
	fmt.Printf("  模式       %s\n", map[bool]string{true: "dry-run（不会改动系统）", false: "真实写入"}[o.DryRun])
	fmt.Printf("  事务状态   %s\n", o.StatePath)

	if o.FirewallErr != "" {
		fmt.Printf("  系统状态   不可用: %s\n", o.FirewallErr)
	} else if f := o.Firewall; f != nil {
		pfState := "未知（不可读）"
		if f.PF.Enabled != nil {
			pfState = map[bool]string{true: "已启用", false: "已关闭"}[*f.PF.Enabled]
		}
		fmt.Printf("  PF         %s\n", pfState)
		if f.PF.ReadError != "" {
			fmt.Printf("             读取受限: %s\n", f.PF.ReadError)
		}
		fmt.Printf("  Anchor     %s（%s）\n", f.Anchor.Name, anchorStateText(string(f.Anchor.State)))
		fmt.Printf("  规则文件   %s（存在=%v，主配置引用=%v）\n",
			f.Anchor.RulePath, f.Anchor.Present, f.Anchor.Referenced)
		if f.Anchor.SizeBytes != nil {
			fmt.Printf("             大小 %d 字节\n", *f.Anchor.SizeBytes)
		}
		if f.Anchor.LastModified != nil {
			fmt.Printf("             最后修改 %s\n", f.Anchor.LastModified.Format("2006-01-02 15:04:05 MST"))
		}
		if len(f.LoadedAnchors) > 0 {
			fmt.Printf("  已加载     %d 个 anchor%s\n", len(f.LoadedAnchors),
				loadedMark(f.LoadedAnchors, f.Anchor.Name))
		}
		fmt.Printf("  期望状态   %s\n", f.Managed)
	}

	fmt.Println("\n== 事务 ==")
	if o.TxErr != "" {
		fmt.Printf("  读取失败: %s\n", o.TxErr)
	} else {
		t := o.Transaction
		fmt.Printf("  阶段       %s\n", t.PhaseText)
		if t.RevisionID != 0 {
			fmt.Printf("  修订       #%d\n", t.RevisionID)
		}
		if t.AppliedAt != nil {
			fmt.Printf("  应用时间   %s\n", t.AppliedAt.Format("2006-01-02 15:04:05 MST"))
		}
		if t.Deadline != nil && t.Phase == string(firewalld.PhasePending) {
			if t.Expired {
				fmt.Printf("  截止时间   %s（已超时）\n", t.Deadline.Format("2006-01-02 15:04:05 MST"))
			} else {
				fmt.Printf("  截止时间   %s（剩余 %s，超时将自动回滚）\n",
					t.Deadline.Format("2006-01-02 15:04:05 MST"), t.Remaining)
			}
		}
		if t.Reason != "" {
			fmt.Printf("  说明       %s\n", t.Reason)
		}
	}

	if len(o.Hints) > 0 {
		fmt.Println("\n== 提示 ==")
		for _, h := range o.Hints {
			fmt.Printf("  · %s\n", h)
		}
	}
}

// loadedMark 标出本应用 anchor 是否在已加载列表中。
func loadedMark(list []string, self string) string {
	for _, a := range list {
		if a == self {
			return "（含本应用 anchor）"
		}
	}
	return "（不含本应用 anchor）"
}

func phaseText(p firewalld.Phase) string {
	switch p {
	case firewalld.PhaseIdle:
		return "空闲（本应用未加载规则）"
	case firewalld.PhasePending:
		return "待确认（回滚倒计时进行中）"
	case firewalld.PhaseConfirmed:
		return "已确认（规则持续生效）"
	case firewalld.PhaseRolledBack:
		return "已回滚"
	case firewalld.PhaseFailed:
		return "失败"
	default:
		return string(p)
	}
}

func anchorStateText(s string) string {
	switch s {
	case string(model.AnchorLoaded):
		return "已加载"
	case string(model.AnchorPresent):
		return "文件存在"
	case string(model.AnchorAbsent):
		return "文件不存在"
	case string(model.AnchorUnknown):
		return "加载状态未知"
	default:
		return s
	}
}

func dryRunSuffix(dryRun bool) string {
	if dryRun {
		return "（dry-run：未真正改动 PF）"
	}
	return ""
}

// cliFirewallLogger 把 Manager 的日志接到终端。
//
// 注意 Logger 接口是 slog 风格（msg + key/value 交替参数），
// 不能用 fmt.Sprintf(msg, args...) 展开——那样会把日志渲染成
// "%!(EXTRA string=reason, ...)" 这种噪音。
//
// dry-run 下压掉 Info 级别：那些条目会让用户误以为真的执行了加载动作。
type cliFirewallLogger struct{ dryRun bool }

func (l cliFirewallLogger) Info(msg string, _ ...any) {
	if l.dryRun {
		return
	}
	fmt.Println("  ·", msg)
}

func (l cliFirewallLogger) Warn(msg string, args ...any) {
	fmt.Println("  !", renderLogArgs(msg, args))
}

func (l cliFirewallLogger) Error(msg string, args ...any) {
	fmt.Println("  ✗", renderLogArgs(msg, args))
}

// renderLogArgs 把 slog 风格的 key/value 参数渲染成 k=v 列表。
// 奇数个 value（不成对）时按原样追加，避免丢信息。
func renderLogArgs(msg string, args []any) string {
	if len(args) == 0 {
		return msg
	}
	var b strings.Builder
	b.WriteString(msg)
	for i := 0; i < len(args); i += 2 {
		b.WriteString(" ")
		if i+1 < len(args) {
			fmt.Fprintf(&b, "%v=%v", args[i], args[i+1])
		} else {
			fmt.Fprintf(&b, "%v", args[i])
		}
	}
	return b.String()
}
