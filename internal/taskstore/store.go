// Package taskstore 是计划任务与执行记录的持久化层。
//
// 职责边界：只管存取与字段映射，不计算下次执行时刻（在 schedule 包），
// 也不判断该不该执行（同样在 schedule 包）。这样调度逻辑可以脱离
// 数据库完整测试。
package taskstore

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/lancelot-lewis/MacPanel/internal/database"
	"github.com/lancelot-lewis/MacPanel/internal/model"
)

// Store 提供任务与执行记录的读写。
type Store struct {
	db *database.DB
	// now 是时间源。默认取 realtime，测试可注入固定时刻，
	// 让「12 分钟前执行过」这类断言不依赖运行时的真实时钟。
	now func() time.Time
}

// New 创建仓储。
func New(db *database.DB) *Store {
	return &Store{db: db, now: func() time.Time { return database.Now() }}
}

// SetNowForTest 替换时间源（仅供测试）。
//
// 为什么需要：执行状态的断言（间隔不足、熔断）依赖具体时间差。
// 用真实时钟会让测试结果随运行时刻漂移，甚至出现「白天通过、
// 凌晨失败」这种最难排查的间歇性失败。
func (s *Store) SetNowForTest(fn func() time.Time) {
	if fn != nil {
		s.now = fn
	}
}

// ErrNotFound 表示任务不存在。
var ErrNotFound = errors.New("计划任务不存在")

// 待执行重启在 settings 表中的键。
const pendingRebootKey = "power.pending_reboot"

// selectColumns 是所有查询共用的列清单。
//
// 集中定义是为了避免各查询的列顺序不一致——Scan 按位置绑定，
// 一旦某个查询漏了一列，就会把后面的值错位赋给错误的字段，
// 而这种错误往往只在特定界面才暴露。
const selectColumns = `
	id, name, kind, recurrence_json, schedule, timezone, enabled,
	catch_up, catch_up_window_seconds, min_interval_seconds,
	last_run_at, last_result, last_error, next_run_at, fail_count,
	created_at, updated_at, disabled_reason, launcher_label`

// List 读取全部任务，按创建时间升序。
func (s *Store) List(ctx context.Context) ([]model.ScheduledTask, error) {
	rows, err := s.db.SQL().QueryContext(ctx,
		`SELECT `+selectColumns+` FROM scheduled_tasks ORDER BY created_at ASC, id ASC`)
	if err != nil {
		return nil, fmt.Errorf("查询计划任务失败: %w", err)
	}
	defer rows.Close()

	out := make([]model.ScheduledTask, 0, 8)
	for rows.Next() {
		task, err := scanTask(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, task)
	}
	return out, rows.Err()
}

// Get 按 ID 读取任务。
func (s *Store) Get(ctx context.Context, id int64) (model.ScheduledTask, error) {
	row := s.db.SQL().QueryRowContext(ctx,
		`SELECT `+selectColumns+` FROM scheduled_tasks WHERE id = ?`, id)
	task, err := scanTask(row)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return model.ScheduledTask{}, ErrNotFound
		}
		return model.ScheduledTask{}, err
	}
	return task, nil
}

// Create 创建任务，返回带 ID 与计算结果的任务。
//
// 这里会回填 NextRunAt：创建后界面上立刻能看到「下次执行时间」，
// 不必等下一次调度扫描。
func (s *Store) Create(ctx context.Context, task model.ScheduledTask) (model.ScheduledTask, error) {
	if err := task.Validate(); err != nil {
		return model.ScheduledTask{}, err
	}

	normalize(&task)
	now := s.now()
	task.CreatedAt = now
	task.UpdatedAt = now

	recJSON, err := json.Marshal(task.Recurrence)
	if err != nil {
		return model.ScheduledTask{}, fmt.Errorf("序列化执行周期失败: %w", err)
	}

	res, err := s.db.SQL().ExecContext(ctx, `
		INSERT INTO scheduled_tasks
		(name, kind, recurrence_json, schedule, timezone, enabled,
		 catch_up, catch_up_window_seconds, min_interval_seconds,
		 next_run_at, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		task.Name, task.Kind, string(recJSON), task.Recurrence.Describe(),
		task.Timezone, boolToInt(task.Enabled),
		string(task.CatchUp), task.CatchUpWindowSeconds, task.MinIntervalSeconds,
		nullTime(task.NextRunAt), database.FormatTime(now), database.FormatTime(now),
	)
	if err != nil {
		return model.ScheduledTask{}, fmt.Errorf("插入计划任务失败: %w", err)
	}

	id, err := res.LastInsertId()
	if err != nil {
		return model.ScheduledTask{}, fmt.Errorf("获取新任务 ID 失败: %w", err)
	}
	task.ID = id
	return task, nil
}

// Update 更新任务定义（不含执行状态）。
//
// 只更新用户可编辑的字段：执行状态（last_run_at / fail_count 等）
// 由 RecordResult 维护，混在一起写会在并发场景下互相覆盖。
func (s *Store) Update(ctx context.Context, task model.ScheduledTask) error {
	if err := task.Validate(); err != nil {
		return err
	}

	normalize(&task)
	recJSON, err := json.Marshal(task.Recurrence)
	if err != nil {
		return fmt.Errorf("序列化执行周期失败: %w", err)
	}

	res, err := s.db.SQL().ExecContext(ctx, `
		UPDATE scheduled_tasks SET
			name = ?, recurrence_json = ?, schedule = ?, timezone = ?, enabled = ?,
			catch_up = ?, catch_up_window_seconds = ?, min_interval_seconds = ?,
			next_run_at = ?, updated_at = ?
		WHERE id = ?`,
		task.Name, string(recJSON), task.Recurrence.Describe(), task.Timezone,
		boolToInt(task.Enabled), string(task.CatchUp), task.CatchUpWindowSeconds,
		task.MinIntervalSeconds, nullTime(task.NextRunAt),
		database.FormatTime(database.Now()), task.ID,
	)
	if err != nil {
		return fmt.Errorf("更新计划任务失败: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("确认更新结果失败: %w", err)
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// Delete 删除任务及其执行记录。
//
// 执行记录依赖 ON DELETE CASCADE。若外键约束未开启，需要显式删除——
// 因此这里手动删一次，不依赖数据库配置。
func (s *Store) Delete(ctx context.Context, id int64) error {
	return s.db.Tx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `DELETE FROM task_executions WHERE task_id = ?`, id); err != nil {
			return fmt.Errorf("删除执行记录失败: %w", err)
		}
		res, err := tx.ExecContext(ctx, `DELETE FROM scheduled_tasks WHERE id = ?`, id)
		if err != nil {
			return fmt.Errorf("删除计划任务失败: %w", err)
		}
		n, err := res.RowsAffected()
		if err != nil {
			return fmt.Errorf("确认删除结果失败: %w", err)
		}
		if n == 0 {
			return ErrNotFound
		}
		return nil
	})
}

// RecordResult 记录一次执行结果并更新任务状态。
//
// success 会清零失败计数；failed 会累加并在达到阈值时自动禁用。
// 这两条逻辑放在仓储层之外（在 agent 侧）也可以，但放在这里能让
// 「结果写入」与「状态推进」保持原子性，避免出现「记了失败但没加计数」。
func (s *Store) RecordResult(
	ctx context.Context, taskID int64, result string, detail string, nextRun *time.Time,
) error {
	return s.db.Tx(ctx, func(tx *sql.Tx) error {
		now := database.FormatTime(s.now())

		var err error
		switch result {
		case model.ExecutionSuccess:
			_, err = tx.ExecContext(ctx, `
				UPDATE scheduled_tasks SET
					last_run_at = ?, last_result = ?, last_error = '',
					fail_count = 0, next_run_at = ?, updated_at = ?
				WHERE id = ?`,
				now, result, nullTime(nextRun), now, taskID)

		case model.ExecutionFailed:
			// fail_count 在 SQL 里自增，避免「读-改-写」的竞态。
			_, err = tx.ExecContext(ctx, `
				UPDATE scheduled_tasks SET
					last_run_at = ?, last_result = ?, last_error = ?,
					fail_count = fail_count + 1, next_run_at = ?, updated_at = ?
				WHERE id = ?`,
				now, result, detail, nullTime(nextRun), now, taskID)
			if err == nil {
				// 达到阈值则自动禁用，并留下可读原因。
				if _, err = tx.ExecContext(ctx, `
					UPDATE scheduled_tasks SET
						enabled = 0,
						disabled_reason = ?
					WHERE id = ? AND fail_count >= ?`,
					fmt.Sprintf("连续失败 %d 次后自动禁用", model.MaxConsecutiveFailures),
					taskID, model.MaxConsecutiveFailures); err != nil {
					return fmt.Errorf("自动禁用任务失败: %w", err)
				}
			}

		case model.ExecutionSkipped:
			// 跳过不算失败，不动 fail_count，也不更新 last_run_at
			// （last_run_at 的语义是「真正执行过」）。
			_, err = tx.ExecContext(ctx, `
				UPDATE scheduled_tasks SET
					last_result = ?, last_error = '', next_run_at = ?, updated_at = ?
				WHERE id = ?`,
				result, nullTime(nextRun), now, taskID)

		default:
			return fmt.Errorf("未知的执行结果 %q", result)
		}
		if err != nil {
			return fmt.Errorf("更新任务执行状态失败: %w", err)
		}
		return nil
	})
}

// ResetFailures 清零失败计数与禁用原因。
//
// 独立方法而不是并入 Update：Update 刻意不碰执行状态（否则用户改个名字
// 就能把熔断计数清零）。只有「用户在界面上主动重新启用」这一个场景
// 才应该重置，因此用显式调用表达意图。
func (s *Store) ResetFailures(ctx context.Context, taskID int64) error {
	_, err := s.db.SQL().ExecContext(ctx, `
		UPDATE scheduled_tasks SET
			fail_count = 0, disabled_reason = '', last_error = '', updated_at = ?
		WHERE id = ?`,
		database.FormatTime(s.now()), taskID)
	if err != nil {
		return fmt.Errorf("重置失败计数失败: %w", err)
	}
	return nil
}

// StartExecution 插入一条 running 记录，返回记录 ID。
func (s *Store) StartExecution(ctx context.Context, taskID int64) (int64, error) {
	now := s.now()
	res, err := s.db.SQL().ExecContext(ctx, `
		INSERT INTO task_executions (task_id, started_at, result, detail)
		VALUES (?, ?, ?, '')`,
		taskID, database.FormatTime(now), model.ExecutionRunning)
	if err != nil {
		return 0, fmt.Errorf("创建执行记录失败: %w", err)
	}
	return res.LastInsertId()
}

// FinishExecution 补齐执行记录的结束时间与结果。
func (s *Store) FinishExecution(ctx context.Context, execID int64, result, detail string) error {
	_, err := s.db.SQL().ExecContext(ctx, `
		UPDATE task_executions SET finished_at = ?, result = ?, detail = ?
		WHERE id = ?`,
		database.FormatTime(s.now()), result, detail, execID)
	if err != nil {
		return fmt.Errorf("更新执行记录失败: %w", err)
	}
	return nil
}

// RecordSkipped 直接写入一条 skipped 记录（开始与结束合为一步）。
//
// 跳过不需要「先 running 再 finish」：它没有持续时长，
// 分两步写只会在崩溃时留下永远 running 的脏记录。
func (s *Store) RecordSkipped(ctx context.Context, taskID int64, detail string) error {
	now := database.FormatTime(s.now())
	_, err := s.db.SQL().ExecContext(ctx, `
		INSERT INTO task_executions (task_id, started_at, finished_at, result, detail)
		VALUES (?, ?, ?, ?, ?)`,
		taskID, now, now, model.ExecutionSkipped, detail)
	if err != nil {
		return fmt.Errorf("写入跳过记录失败: %w", err)
	}
	return nil
}

// ListExecutions 按任务读取执行记录（最近优先）。taskID 为 0 表示全部任务。
func (s *Store) ListExecutions(ctx context.Context, taskID int64, limit int) ([]model.TaskExecution, error) {
	if limit <= 0 {
		limit = 50
	}
	if limit > 500 {
		limit = 500
	}

	query := `
		SELECT e.id, e.task_id, COALESCE(t.name, ''), e.started_at, e.finished_at, e.result, COALESCE(e.detail, '')
		FROM task_executions e
		LEFT JOIN scheduled_tasks t ON t.id = e.task_id`
	args := []any{}
	if taskID > 0 {
		query += ` WHERE e.task_id = ?`
		args = append(args, taskID)
	}
	query += ` ORDER BY e.started_at DESC, e.id DESC LIMIT ?`
	args = append(args, limit)

	rows, err := s.db.SQL().QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("查询执行记录失败: %w", err)
	}
	defer rows.Close()

	out := make([]model.TaskExecution, 0, limit)
	for rows.Next() {
		var (
			e          model.TaskExecution
			startedAt  string
			finishedAt sql.NullString
		)
		if err := rows.Scan(&e.ID, &e.TaskID, &e.TaskName, &startedAt,
			&finishedAt, &e.Result, &e.Detail); err != nil {
			return nil, fmt.Errorf("扫描执行记录失败: %w", err)
		}
		if t, err := database.ParseTime(startedAt); err == nil {
			e.StartedAt = t
		}
		if finishedAt.Valid && finishedAt.String != "" {
			if t, err := database.ParseTime(finishedAt.String); err == nil {
				e.FinishedAt = &t
			}
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// RecoverStaleExecutions 收尾遗留的 running 记录。
//
// 为什么需要：Agent 在执行重启前退出，会留下永远 running 的记录。
// 界面上看起来「有个任务一直在跑」，而实际上什么都没在跑。
// Agent 启动时调用一次，把这些记录收尾。
//
// bootTime 用来区分两种遗留：重启任务执行后进程被杀是**预期行为**，
// 系统随后重启过就说明那次执行成功了。若一律标为失败，用户会看到
// 「每天重启都失败」这种与实际相反的结论。
//
// bootTime 为零值时退化为「全部标失败」。
func (s *Store) RecoverStaleExecutions(ctx context.Context, bootTime time.Time) (int, error) {
	now := database.FormatTime(s.now())

	// 第一步：重启类任务 + 执行时刻早于本次启动 → 判定成功。
	// 用 started_at < bootTime 而不是 <=：同一秒内的先后无法区分，
	// 保守地当作未重启（走失败分支）更能提醒用户去核对。
	if !bootTime.IsZero() {
		if _, err := s.db.SQL().ExecContext(ctx, `
			UPDATE task_executions SET
				result = ?, finished_at = ?, detail = ?
			WHERE result = ? AND task_id IN (
				SELECT id FROM scheduled_tasks WHERE kind = ?
			) AND started_at < ?`,
			model.ExecutionSuccess, now,
			"系统已重启，判定该次重启执行成功",
			model.ExecutionRunning, model.TaskKindReboot,
			database.FormatTime(bootTime)); err != nil {
			return 0, fmt.Errorf("判定已生效的重启任务失败: %w", err)
		}
	}

	// 第二步：其余遗留记录标为失败。
	res, err := s.db.SQL().ExecContext(ctx, `
		UPDATE task_executions SET
			result = ?, finished_at = ?, detail = ?
		WHERE result = ?`,
		model.ExecutionFailed, now,
		"执行过程中进程退出，无法确认该次执行结果", model.ExecutionRunning)
	if err != nil {
		return 0, fmt.Errorf("清理遗留执行记录失败: %w", err)
	}
	n, _ := res.RowsAffected()

	// 统计总数（两步之和），供调用方记录日志。
	var total int
	if err := s.db.SQL().QueryRowContext(ctx,
		`SELECT COUNT(*) FROM task_executions WHERE result IN (?, ?) AND finished_at = ?`,
		model.ExecutionSuccess, model.ExecutionFailed, now).Scan(&total); err != nil {
		return int(n), nil
	}
	return total, nil
}

// ---- 待执行重启 ----

// SavePendingReboot 持久化待执行的重启。
//
// 必须落盘：延迟重启的倒计时要在 Agent 崩溃或系统重启后恢复。
// 放内存里会让「30 分钟后重启」在 Agent 重启后静默消失。
func (s *Store) SavePendingReboot(ctx context.Context, p model.PendingReboot) error {
	raw, err := json.Marshal(p)
	if err != nil {
		return fmt.Errorf("序列化待执行重启失败: %w", err)
	}
	_, err = s.db.SQL().ExecContext(ctx, `
		INSERT INTO settings (key, value, updated_at) VALUES (?, ?, ?)
		ON CONFLICT(key) DO UPDATE SET value = excluded.value, updated_at = excluded.updated_at`,
		pendingRebootKey, string(raw), database.FormatTime(s.now()))
	if err != nil {
		return fmt.Errorf("保存待执行重启失败: %w", err)
	}
	return nil
}

// LoadPendingReboot 读取待执行的重启，不存在时返回 nil。
func (s *Store) LoadPendingReboot(ctx context.Context) (*model.PendingReboot, error) {
	var raw string
	err := s.db.SQL().QueryRowContext(ctx,
		`SELECT value FROM settings WHERE key = ?`, pendingRebootKey).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("读取待执行重启失败: %w", err)
	}
	if raw == "" {
		return nil, nil
	}

	var p model.PendingReboot
	if err := json.Unmarshal([]byte(raw), &p); err != nil {
		// 数据损坏时不应让整个电源功能不可用：清掉坏数据并继续。
		if _, derr := s.db.SQL().ExecContext(ctx,
			`DELETE FROM settings WHERE key = ?`, pendingRebootKey); derr != nil {
			return nil, fmt.Errorf("清理损坏的待执行重启记录失败: %w", derr)
		}
		return nil, nil
	}
	return &p, nil
}

// ClearPendingReboot 清除待执行的重启。
func (s *Store) ClearPendingReboot(ctx context.Context) error {
	_, err := s.db.SQL().ExecContext(ctx, `DELETE FROM settings WHERE key = ?`, pendingRebootKey)
	if err != nil {
		return fmt.Errorf("清除待执行重启失败: %w", err)
	}
	return nil
}

// SetLauncherLabel 记录任务对应的 launchd 标签。
//
// 单独一个方法而不是走 Update：Update 会整体覆盖用户可编辑字段，
// 而标签只由调度器安装/卸载流程维护，两者的写入时机完全不同。
func (s *Store) SetLauncherLabel(ctx context.Context, taskID int64, label string) error {
	_, err := s.db.SQL().ExecContext(ctx, `
		UPDATE scheduled_tasks SET launcher_label = ?, updated_at = ? WHERE id = ?`,
		label, database.FormatTime(s.now()), taskID)
	if err != nil {
		return fmt.Errorf("更新调度器标签失败: %w", err)
	}
	return nil
}

// rowScanner 抽象 *sql.Row 与 *sql.Rows 的共同能力，让 scanTask 两者通用。
type rowScanner interface {
	Scan(dest ...any) error
}

// scanTask 把一行扫描为任务。
func scanTask(sc rowScanner) (model.ScheduledTask, error) {
	var (
		t              model.ScheduledTask
		recurrenceJSON string
		scheduleDesc   string
		enabled        int
		catchUp        string
		lastRunAt      sql.NullString
		lastResult     sql.NullString
		lastError      string
		nextRunAt      sql.NullString
		createdAt      string
		updatedAt      string
		disabledReason string
		launcherLabel  string
	)

	if err := sc.Scan(
		&t.ID, &t.Name, &t.Kind, &recurrenceJSON, &scheduleDesc, &t.Timezone, &enabled,
		&catchUp, &t.CatchUpWindowSeconds, &t.MinIntervalSeconds,
		&lastRunAt, &lastResult, &lastError, &nextRunAt, &t.FailCount,
		&createdAt, &updatedAt, &disabledReason, &launcherLabel,
	); err != nil {
		return model.ScheduledTask{}, err
	}

	t.Enabled = enabled == 1
	t.CatchUp = model.CatchUpPolicy(catchUp)
	t.LastError = lastError
	t.DisabledReason = disabledReason
	t.LauncherLabel = launcherLabel

	// recurrence_json 为空说明是 v3 之前写入的历史行。
	// 此时按 schedule 描述无法还原结构化表达式，宁可让校验拦下，
	// 也不要猜一个可能错的表达式去执行重启。
	if recurrenceJSON != "" {
		var rec model.Recurrence
		if err := json.Unmarshal([]byte(recurrenceJSON), &rec); err == nil {
			t.Recurrence = rec
		}
	}

	if lastRunAt.Valid && lastRunAt.String != "" {
		if v, err := database.ParseTime(lastRunAt.String); err == nil {
			t.LastRunAt = &v
		}
	}
	if lastResult.Valid {
		t.LastResult = lastResult.String
	}
	if nextRunAt.Valid && nextRunAt.String != "" {
		if v, err := database.ParseTime(nextRunAt.String); err == nil {
			t.NextRunAt = &v
		}
	}
	if v, err := database.ParseTime(createdAt); err == nil {
		t.CreatedAt = v
	}
	if v, err := database.ParseTime(updatedAt); err == nil {
		t.UpdatedAt = v
	}
	return t, nil
}

// normalize 补齐默认值，避免「空字符串策略」在后续判断中产生歧义。
func normalize(t *model.ScheduledTask) {
	if t.Timezone == "" {
		t.Timezone = "Asia/Shanghai"
	}
	if t.CatchUp == "" {
		t.CatchUp = model.CatchUpNever
	}
	if t.Kind == "" {
		t.Kind = model.TaskKindReboot
	}
	if t.MinIntervalSeconds <= 0 {
		t.MinIntervalSeconds = int(model.DefaultMinRebootInterval.Seconds())
	}
}

func boolToInt(v bool) int {
	if v {
		return 1
	}
	return 0
}

func nullTime(t *time.Time) any {
	if t == nil {
		return nil
	}
	return database.FormatTime(*t)
}
