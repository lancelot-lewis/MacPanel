// Package tasksched 把模型层的调度计算、持久化、launchd 与电源操作编排起来。
//
// 分层：本包只做「流程编排」，不做时间计算（schedule 包）、不做 plist 生成
// （launchd 包）、不直接调系统命令（platform.PowerController）。
// 这样每个环节都能独立测试，而编排逻辑的测试只需要 fake 依赖。
package tasksched

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"sync"
	"time"

	"github.com/lancelot-lewis/MacPanel/internal/audit"
	"github.com/lancelot-lewis/MacPanel/internal/model"
	"github.com/lancelot-lewis/MacPanel/internal/platform"
	"github.com/lancelot-lewis/MacPanel/internal/platform/darwin/launchd"
	"github.com/lancelot-lewis/MacPanel/internal/schedule"
	"github.com/lancelot-lewis/MacPanel/internal/taskstore"
)

// Auditor 记录高风险操作。用接口是为了测试不必依赖审计表结构。
type Auditor interface {
	Write(ctx context.Context, e audit.Entry) error
}

// TaskInstaller 抽象 launchd 的安装操作。
//
// 用接口而不是 *launchd.Installer：测试需要精确控制「安装失败」
// 「plist 残留」这类场景，而真实实现会写 /Library/LaunchDaemons
// 并调用 launchctl。*launchd.Installer 天然满足本接口。
type TaskInstaller interface {
	Install(ctx context.Context, spec launchd.Spec) error
	Remove(ctx context.Context, label string) error
	State(ctx context.Context, label string) (launchd.InstallState, error)
	ListManaged() ([]string, error)
}

// Options 是调度器的构造参数。
type Options struct {
	Store     *taskstore.Store
	Installer TaskInstaller
	Power     platform.PowerController
	Auditor   Auditor
	Logger    *slog.Logger

	// BinPath 是本应用二进制的绝对路径（用于生成 launchd 的 ProgramArguments）。
	BinPath string
	// LogDir 是任务标准输出的目录。
	LogDir string

	// ExtraArgs 追加到 launchd 任务的命令行末尾（例如开发模式的 --dev）。
	//
	// 用显式参数而不是让 Scheduler 自己读环境变量：launchd 启动的进程
	// 没有登录 shell 的环境，靠环境变量传递开发模式会静默失效。
	ExtraArgs []string

	// Now 可注入，便于测试时间相关逻辑。
	Now func() time.Time
	// TickInterval 是错过执行检查的间隔。
	TickInterval time.Duration
}

// Scheduler 是计划任务与电源操作的编排器。
type Scheduler struct {
	store     *taskstore.Store
	installer TaskInstaller
	power     platform.PowerController
	auditor   Auditor
	log       *slog.Logger
	binPath   string
	logDir    string
	extraArgs []string
	now       func() time.Time
	tick      time.Duration

	mu      sync.Mutex
	pending *model.PendingReboot
	timer   *time.Timer
	stopCh  chan struct{}
	stopped bool
	// executeReboot 可在测试中替换，避免真的重启机器。
	executeReboot func(ctx context.Context, delaySeconds int) error
}

// New 创建调度器。
func New(opts Options) (*Scheduler, error) {
	if opts.Store == nil {
		return nil, errors.New("tasksched: 缺少 Store")
	}
	if opts.Installer == nil {
		return nil, errors.New("tasksched: 缺少 Installer")
	}
	if opts.Power == nil {
		return nil, errors.New("tasksched: 缺少 Power")
	}
	if opts.BinPath == "" {
		return nil, errors.New("tasksched: 缺少 BinPath（启动 launchd 任务需要本应用绝对路径）")
	}

	log := opts.Logger
	if log == nil {
		log = slog.Default()
	}
	now := opts.Now
	if now == nil {
		now = func() time.Time { return time.Now().UTC() }
	}
	tick := opts.TickInterval
	if tick <= 0 {
		tick = time.Minute
	}
	logDir := opts.LogDir
	if logDir == "" {
		logDir = launchd.LogDir
	}

	return &Scheduler{
		store:     opts.Store,
		installer: opts.Installer,
		power:     opts.Power,
		auditor:   opts.Auditor,
		log:       log,
		binPath:   opts.BinPath,
		logDir:    logDir,
		extraArgs: opts.ExtraArgs,
		now:       now,
		tick:      tick,
		stopCh:    make(chan struct{}),
	}, nil
}

// SetRebootExecutorForTest 替换实际的重启执行函数（仅供测试）。
//
// 为什么需要：真实实现会调用 shutdown -r，测试中绝不能执行。
// 但「排定 → 触发 → 执行」这条链路又必须被测到。
func (s *Scheduler) SetRebootExecutorForTest(fn func(ctx context.Context, delaySeconds int) error) {
	s.executeReboot = fn
}

// Start 恢复状态并启动后台检查。
//
// 启动顺序很重要：
//  1. 收尾遗留的执行记录（否则界面显示「有个任务一直在跑」）；
//  2. 恢复待执行重启（且先判断系统是否已经重启过）；
//  3. 同步 launchd（补齐缺失的 plist、清掉孤儿）；
//  4. 启动定期检查。
func (s *Scheduler) Start(ctx context.Context) error {
	// 用系统的启动时刻判定「重启任务是否已经生效」：
	// 重启会把进程杀掉，遗留 running 记录是预期行为而非失败。
	var boot time.Time
	if bt, err := s.power.BootTime(ctx); err == nil {
		boot = bt
	}
	if n, err := s.store.RecoverStaleExecutions(ctx, boot); err != nil {
		s.log.Warn("收尾遗留执行记录失败", "error", err)
	} else if n > 0 {
		s.log.Info("已收尾遗留的执行记录", "count", n)
	}

	if err := s.restorePendingReboot(ctx); err != nil {
		// 恢复失败不应阻止启动：电源功能降级，其余功能照常。
		s.log.Warn("恢复待执行重启失败", "error", err)
	}

	if _, err := s.Sync(ctx); err != nil {
		s.log.Warn("同步 plan 任务到 launchd 失败", "error", err)
	}

	go s.loop(ctx)
	return nil
}

// Stop 停止后台检查与定时器。
func (s *Scheduler) Stop() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.stopped {
		return
	}
	s.stopped = true
	close(s.stopCh)
	if s.timer != nil {
		s.timer.Stop()
		s.timer = nil
	}
}

// loop 定期检查错过的执行。
func (s *Scheduler) loop(ctx context.Context) {
	ticker := time.NewTicker(s.tick)
	defer ticker.Stop()

	for {
		select {
		case <-s.stopCh:
			return
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := s.CheckMissed(ctx); err != nil {
				s.log.Warn("检查错过的执行失败", "error", err)
			}
		}
	}
}

// SyncResult 是一次同步的结果，用于诊断「为什么某任务没生效」。
type SyncResult struct {
	Installed []string `json:"installed"`
	Updated   []string `json:"updated"`
	Removed   []string `json:"removed"`
	Unchanged []string `json:"unchanged"`
	Errors    []string `json:"errors"`
}

// Sync 把数据库中的任务状态同步到 launchd。
//
// 三类动作：
//   - 启用中的 daily/weekly/interval 任务 → 确保 plist 存在且内容最新；
//   - once 任务 → 不装 plist（launchd 不支持年份，转成待执行重启）；
//   - 禁用或已删除 → 移除 plist。
//
// 同时清理「数据库里没有、但磁盘上有」的孤儿 plist。这一步不能省：
// 用户删掉任务后若 plist 残留，launchd 仍会按原计划执行重启，
// 表现为「任务删了但机器还在重启」。
func (s *Scheduler) Sync(ctx context.Context) (SyncResult, error) {
	var res SyncResult

	tasks, err := s.store.List(ctx)
	if err != nil {
		return res, err
	}

	wanted := make(map[string]bool, len(tasks))

	for _, task := range tasks {
		label := launcherLabel(task.ID)

		// 禁用或 once 任务 → 不装 plist
		if !task.Enabled || task.Recurrence.Kind == model.RecurOnce {
			if err := s.removeIfPresent(ctx, label); err != nil {
				res.Errors = append(res.Errors, fmt.Sprintf("%s: %v", task.Name, err))
			} else {
				res.Removed = append(res.Removed, label)
			}
			continue
		}

		spec, err := s.buildSpec(task)
		if err != nil {
			res.Errors = append(res.Errors, fmt.Sprintf("%s: %v", task.Name, err))
			continue
		}
		wanted[label] = true

		changed, err := s.ensureInstalled(ctx, label, spec)
		if err != nil {
			res.Errors = append(res.Errors, fmt.Sprintf("%s: %v", task.Name, err))
			continue
		}
		if changed {
			res.Installed = append(res.Installed, label)
		} else {
			res.Unchanged = append(res.Unchanged, label)
		}

		// 同步后刷新 next_run_at，让界面显示真实的下次执行时间。
		if next, ok := schedule.NextAfter(task, s.now()); ok {
			if task.NextRunAt == nil || !task.NextRunAt.Equal(next) {
				task.NextRunAt = &next
				if err := s.store.Update(ctx, task); err != nil {
					s.log.Warn("更新下次执行时间失败", "task", task.Name, "error", err)
				}
			}
		}
	}

	// 清理孤儿 plist
	managed, err := s.installer.ListManaged()
	if err != nil {
		res.Errors = append(res.Errors, fmt.Sprintf("列举已安装任务失败: %v", err))
	}
	for _, label := range managed {
		if wanted[label] {
			continue
		}
		// 只删「确实不该存在」的：再确认一次没有对应任务。
		if s.labelBelongsToLiveTask(ctx, label, tasks) {
			continue
		}
		if err := s.installer.Remove(ctx, label); err != nil {
			res.Errors = append(res.Errors, fmt.Sprintf("清理 %s: %v", label, err))
			continue
		}
		res.Removed = append(res.Removed, label)
		s.log.Info("已清理孤儿 launchd 任务", "label", label)
	}

	return res, nil
}

// labelBelongsToLiveTask 判断某个 label 是否仍对应一个启用中的任务。
func (s *Scheduler) labelBelongsToLiveTask(ctx context.Context, label string, tasks []model.ScheduledTask) bool {
	for _, t := range tasks {
		if launcherLabel(t.ID) == label && t.Enabled && t.Recurrence.Kind != model.RecurOnce {
			return true
		}
	}
	return false
}

// ensureInstalled 确保 plist 与期望内容一致且已加载，返回是否发生了变更。
//
// 用内容哈希比较而不是「总是重装」：launchctl 调用有成本，
// 而且重装会 bootout 再 bootstrap，短暂中断任务的加载状态。
func (s *Scheduler) ensureInstalled(ctx context.Context, label string, spec launchd.Spec) (bool, error) {
	desired, err := launchd.RenderPlist(spec)
	if err != nil {
		return false, err
	}
	desiredHash := hashBytes(desired)

	state, err := s.installer.State(ctx, label)
	if err != nil {
		return false, err
	}

	if state.PlistExists && state.Loaded {
		if currentHash, err := s.plistHash(state.PlistPath); err == nil && currentHash == desiredHash {
			return false, nil // 内容与加载状态都正确
		}
	}

	if err := s.installer.Install(ctx, spec); err != nil {
		return false, err
	}

	if err := s.store.SetLauncherLabel(ctx, taskIDFromLabel(label), label); err != nil {
		// 标签写不进去不影响运行，只是后续卸载不精确。
		s.log.Warn("记录 launchd 标签失败", "label", label, "error", err)
	}
	return true, nil
}

// removeIfPresent 移除已安装的任务，不存在时静默成功。
func (s *Scheduler) removeIfPresent(ctx context.Context, label string) error {
	state, err := s.installer.State(ctx, label)
	if err != nil {
		return err
	}
	if !state.PlistExists && !state.Loaded {
		return nil
	}
	return s.installer.Remove(ctx, label)
}

// plistHash 计算磁盘上 plist 的哈希。
func (s *Scheduler) plistHash(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	return hashBytes(data), nil
}

// buildSpec 把任务转成 launchd 配置。
func (s *Scheduler) buildSpec(task model.ScheduledTask) (launchd.Spec, error) {
	label := launcherLabel(task.ID)
	spec := launchd.Spec{
		Label: label,
		// 任务由独立 CLI 子命令执行，而不是常驻 Agent：
		// 这样即使 Agent 被停掉，launchd 仍能按计划执行。
		ProgramArguments: append(
			[]string{s.binPath, "task-run", "--task", fmt.Sprintf("%d", task.ID)},
			s.extraArgs...,
		),
		// RunAtLoad 必须为 false（Spec 会校验）：为 true 时每次
		// bootstrap 都会立即执行一次，对重启任务是明确的隐患。
		RunAtLoad:         false,
		StandardOutPath:   fmt.Sprintf("%s/task-%d.log", s.logDir, task.ID),
		StandardErrorPath: fmt.Sprintf("%s/task-%d.err.log", s.logDir, task.ID),
	}

	switch task.Recurrence.Kind {
	case model.RecurDaily:
		hour, minute, err := model.ParseTimeOfDay(task.Recurrence.TimeOfDay)
		if err != nil {
			return spec, err
		}
		spec.StartCalendarInterval = []launchd.CalendarInterval{
			{Hour: launchd.IntPtr(hour), Minute: launchd.IntPtr(minute)},
		}

	case model.RecurWeekly:
		hour, minute, err := model.ParseTimeOfDay(task.Recurrence.TimeOfDay)
		if err != nil {
			return spec, err
		}
		for _, d := range task.Recurrence.Weekdays {
			spec.StartCalendarInterval = append(spec.StartCalendarInterval, launchd.CalendarInterval{
				Weekday: launchd.IntPtr(d),
				Hour:    launchd.IntPtr(hour),
				Minute:  launchd.IntPtr(minute),
			})
		}

	case model.RecurIntervalHours:
		spec.StartIntervalSeconds = task.Recurrence.IntervalHours * 3600

	default:
		return spec, fmt.Errorf("不支持用 launchd 调度的周期类型 %q", task.Recurrence.Kind)
	}

	return spec, nil
}

// ---- 任务 CRUD ----

// List 返回全部任务。
func (s *Scheduler) List(ctx context.Context) ([]model.ScheduledTask, error) {
	return s.store.List(ctx)
}

// Create 创建任务并立即同步到 launchd。
//
// 为什么立即同步而不是等下个轮询：用户点保存后期望「马上生效」。
// 若等轮询，界面上会有一段「显示已保存但下次执行时间是空的」窗口。
func (s *Scheduler) Create(ctx context.Context, task model.ScheduledTask) (model.ScheduledTask, error) {
	if task.Recurrence.Kind == model.RecurOnce {
		// once 任务走待执行重启，需要先确认没有冲突的 pending。
		if err := s.checkOnceConflict(ctx); err != nil {
			return model.ScheduledTask{}, err
		}
	}

	// 预校验：能算出 plist 才允许落库。
	//
	// 否则会出现「任务已保存、界面显示正常，但 launchd 里什么都没有」
	// 的状态——同步失败只写日志，用户完全看不到。宁可创建时就报错。
	if task.Enabled && task.Recurrence.Kind != model.RecurOnce {
		if _, err := s.buildSpec(task); err != nil {
			return model.ScheduledTask{}, model.NewError(model.CodeInvalidArgument,
				"任务无法转换为 launchd 计划：%v", err)
		}
	}

	if next, ok := schedule.NextAfter(task, s.now()); ok {
		task.NextRunAt = &next
	}

	created, err := s.store.Create(ctx, task)
	if err != nil {
		return model.ScheduledTask{}, err
	}

	if created.Recurrence.Kind == model.RecurOnce {
		if err := s.applyOnceTask(ctx, created); err != nil {
			// 待执行重启设置失败时回滚任务，避免留下「任务在但永不执行」。
			if delErr := s.store.Delete(ctx, created.ID); delErr != nil {
				s.log.Error("回滚任务失败，需要人工清理", "taskId", created.ID, "error", delErr)
			}
			return model.ScheduledTask{}, err
		}
	}

	res, err := s.Sync(ctx)
	if err != nil {
		s.log.Warn("创建任务后同步失败", "task", created.Name, "error", err)
	}
	for _, e := range res.Errors {
		s.log.Warn("创建任务后同步有错误", "task", created.Name, "error", e)
	}

	// 重新读取：同步过程会回填 launcherLabel，
	// 直接返回 create 时的对象会让界面看不到「已安装到 launchd」。
	if fresh, err := s.store.Get(ctx, created.ID); err == nil {
		return fresh, nil
	}
	return created, nil
}

// Update 更新任务并重新同步。
func (s *Scheduler) Update(ctx context.Context, task model.ScheduledTask) (model.ScheduledTask, error) {
	// 用数据库里的执行状态覆盖传入值：调用方（HTTP 请求体）不应能
	// 直接改写 last_run_at / fail_count，否则熔断机制可被绕过。
	existing, err := s.store.Get(ctx, task.ID)
	if err != nil {
		return model.ScheduledTask{}, err
	}
	task.LastRunAt = existing.LastRunAt
	task.LastResult = existing.LastResult
	task.LastError = existing.LastError
	task.FailCount = existing.FailCount
	task.DisabledReason = existing.DisabledReason
	task.LauncherLabel = existing.LauncherLabel
	task.CreatedAt = existing.CreatedAt

	// 用户重新启用被熔断禁用的任务时，需要清掉失败计数，
	// 否则下一次失败会立刻再次触发熔断。
	//
	// 注意：这一步必须用专门的 ResetFailures，因为 store.Update
	// 刻意不写执行状态字段（防止改名字时顺手清零熔断计数）。
	needReset := existing.DisabledReason != "" && task.Enabled
	if needReset {
		task.DisabledReason = ""
		task.FailCount = 0
	}

	// 同 Create：先确认能生成合法 plist，再写库。
	if task.Enabled && task.Recurrence.Kind != model.RecurOnce {
		if _, err := s.buildSpec(task); err != nil {
			return model.ScheduledTask{}, model.NewError(model.CodeInvalidArgument,
				"任务无法转换为 launchd 计划：%v", err)
		}
	}

	if next, ok := schedule.NextAfter(task, s.now()); ok {
		task.NextRunAt = &next
	} else {
		task.NextRunAt = nil
	}

	if err := s.store.Update(ctx, task); err != nil {
		return model.ScheduledTask{}, err
	}
	if needReset {
		if err := s.store.ResetFailures(ctx, task.ID); err != nil {
			s.log.Warn("重置失败计数失败", "task", task.Name, "error", err)
		}
	}

	if _, err := s.Sync(ctx); err != nil {
		s.log.Warn("更新任务后同步失败", "task", task.Name, "error", err)
	}

	updated, err := s.store.Get(ctx, task.ID)
	if err != nil {
		return task, nil
	}
	return updated, nil
}

// Delete 删除任务并移除对应的 launchd 配置。
func (s *Scheduler) Delete(ctx context.Context, id int64) error {
	task, err := s.store.Get(ctx, id)
	if err != nil {
		return err
	}

	if err := s.store.Delete(ctx, id); err != nil {
		return err
	}

	label := launcherLabel(id)
	if task.LauncherLabel != "" {
		label = task.LauncherLabel
	}
	if err := s.removeIfPresent(ctx, label); err != nil {
		// 任务已从数据库删除，plist 残留会自动执行重启，因此这里
		// 必须让调用方知道清理失败。
		return fmt.Errorf("任务已删除，但清理 launchd 配置失败（可能导致仍在按计划执行）: %w", err)
	}

	// 若待执行重启由该任务触发，一并取消。
	s.mu.Lock()
	if s.pending != nil && s.pending.TaskID == id {
		s.mu.Unlock()
		if err := s.cancelPendingRebootLocked(ctx, "触发任务已被删除"); err != nil {
			s.log.Warn("取消关联的待执行重启失败", "error", err)
		}
	} else {
		s.mu.Unlock()
	}
	return nil
}

// ListExecutions 返回执行记录。
func (s *Scheduler) ListExecutions(ctx context.Context, taskID int64, limit int) ([]model.TaskExecution, error) {
	return s.store.ListExecutions(ctx, taskID, limit)
}

// CheckMissed 检查并处理错过的计划时刻。
//
// 由后台 ticker 调用。处理方式由每个任务的补执行策略决定：
// 默认「错过就跳过」——重启是破坏性操作，关机三天后开机立刻重启
// 对用户是意外行为。
func (s *Scheduler) CheckMissed(ctx context.Context) error {
	tasks, err := s.store.List(ctx)
	if err != nil {
		return err
	}

	now := s.now()
	for _, task := range tasks {
		if !task.Enabled || task.NextRunAt == nil {
			continue
		}

		res := schedule.EvaluateMissed(task, now)
		if !res.Missed {
			continue
		}

		if res.ShouldRun {
			s.log.Info("补执行错过的计划", "task", task.Name, "reason", res.Reason)
			// 补执行不绕过安全判定：Decide 仍会检查间隔与熔断。
			if _, err := s.runTask(ctx, task.ID, res.ScheduledFor, "补执行"); err != nil {
				s.log.Warn("补执行失败", "task", task.Name, "error", err)
			}
			continue
		}

		// 不补执行：记录 skipped 并推进下次时间。
		next, ok := schedule.NextAfter(task, now)
		var nextPtr *time.Time
		if ok {
			nextPtr = &next
		}
		if err := s.store.RecordSkipped(ctx, task.ID, res.Reason); err != nil {
			s.log.Warn("记录跳过失败", "task", task.Name, "error", err)
		}
		if err := s.store.RecordResult(ctx, task.ID, model.ExecutionSkipped, res.Reason, nextPtr); err != nil {
			s.log.Warn("推进下次执行时间失败", "task", task.Name, "error", err)
		}
		s.log.Info("跳过错过的计划", "task", task.Name, "reason", res.Reason)
	}
	return nil
}

// ---- 辅助 ----

// launcherLabel 生成任务对应的 launchd 标签。
//
// 只用 ASCII 小写字母、数字、点与连字符（launchd 包会校验）。
// 刻意不把任务名拼进标签：任务名是用户输入的中文，既不适合做
// 文件名，也会让 label 随改名而变化（改名就得卸载重装）。
func launcherLabel(taskID int64) string {
	return fmt.Sprintf("%stask.%d", launchd.LabelPrefix, taskID)
}

// taskIDFromLabel 从标签反解析任务 ID。解析失败返回 0。
func taskIDFromLabel(label string) int64 {
	var id int64
	if _, err := fmt.Sscanf(label, launchd.LabelPrefix+"task.%d", &id); err != nil {
		return 0
	}
	return id
}

func hashBytes(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}
