// M3 电源与计划任务的类型定义。
// 与 internal/model/schedule.go 一一对应。

export type RecurrenceKind = 'daily' | 'weekly' | 'interval_hours' | 'once'
export type CatchUpPolicy = 'never' | 'within_window'
export type ExecutionResult = 'running' | 'success' | 'failed' | 'skipped'

export interface Recurrence {
  kind: RecurrenceKind
  /** "HH:MM"，daily / weekly 必填 */
  timeOfDay?: string
  /** 0=周日，weekly 必填 */
  weekdays?: number[]
  /** interval_hours 必填 */
  intervalHours?: number
  /** once 必填（RFC3339） */
  onceAt?: string
}

export interface ScheduledTask {
  id: number
  name: string
  kind: 'reboot'
  recurrence: Recurrence
  timezone: string
  enabled: boolean
  catchUp: CatchUpPolicy
  catchUpWindowSeconds: number
  minIntervalSeconds: number
  lastRunAt?: string
  lastResult?: ExecutionResult
  lastError?: string
  nextRunAt?: string
  failCount: number
  createdAt: string
  updatedAt: string
  disabledReason?: string
  launcherLabel?: string
}

export interface TaskExecution {
  id: number
  taskId: number
  taskName?: string
  startedAt: string
  finishedAt?: string
  result: ExecutionResult
  detail?: string
}

export interface PendingReboot {
  scheduledAt: string
  reason: string
  requestedBy: string
  createdAt: string
  taskId?: number
}

export interface PowerStatus {
  pending?: PendingReboot
  canReboot: boolean
  rebootUnavailableReason?: string
  lastRebootAt?: string
  uptimeSeconds: number
  timezone: string
  scheduleDaemonInstalled: boolean
}

export interface SyncResult {
  installed: string[]
  updated: string[]
  removed: string[]
  unchanged: string[]
  errors: string[]
}

/** 新建任务时的默认值，与后端 model 的默认值保持一致 */
export function emptyTask(timezone: string): ScheduledTask {
  return {
    id: 0,
    name: '',
    kind: 'reboot',
    recurrence: { kind: 'daily', timeOfDay: '04:30' },
    timezone,
    enabled: true,
    catchUp: 'never',
    catchUpWindowSeconds: 0,
    // 30 分钟：后端把低于此值的配置直接判为非法（防循环重启）
    minIntervalSeconds: 1800,
    failCount: 0,
    createdAt: '',
    updatedAt: '',
  }
}

/** 星期名称，索引与 time.Weekday 一致 */
export const WEEKDAY_LABELS = ['周日', '周一', '周二', '周三', '周四', '周五', '周六']

/** 生成周期表达式的中文描述（与后端 Describe 保持一致） */
export function describeRecurrence(r: Recurrence): string {
  switch (r.kind) {
    case 'daily':
      return `每天 ${r.timeOfDay ?? '--:--'}`
    case 'weekly': {
      const days = (r.weekdays ?? []).map((d) => WEEKDAY_LABELS[d] ?? '?').join('、')
      return `每${days} ${r.timeOfDay ?? '--:--'}`
    }
    case 'interval_hours': {
      const h = r.intervalHours ?? 0
      if (h >= 24 && h % 24 === 0) return `每 ${h / 24} 天`
      return `每 ${h} 小时`
    }
    case 'once':
      return r.onceAt ? `单次 ${formatDateTime(r.onceAt)}` : '单次（未指定时间）'
    default:
      return '未设置'
  }
}

/** 把 RFC3339 转成本地可读时间 */
export function formatDateTime(iso: string | undefined): string {
  if (!iso) return '—'
  const d = new Date(iso)
  if (Number.isNaN(d.getTime())) return '—'
  const pad = (n: number) => String(n).padStart(2, '0')
  return `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())} ${pad(d.getHours())}:${pad(d.getMinutes())}`
}

/** 相对时间描述，用于「3 分钟前」「还有 2 小时」 */
export function relativeTime(iso: string | undefined, now = Date.now()): string {
  if (!iso) return '—'
  const t = new Date(iso).getTime()
  if (Number.isNaN(t)) return '—'
  const diff = t - now
  const abs = Math.abs(diff)
  const suffix = diff >= 0 ? '后' : '前'

  if (abs < 60_000) return diff >= 0 ? '即将' : '刚刚'
  if (abs < 3_600_000) return `${Math.round(abs / 60_000)} 分钟${suffix}`
  if (abs < 86_400_000) return `${Math.round(abs / 3_600_000)} 小时${suffix}`
  return `${Math.round(abs / 86_400_000)} 天${suffix}`
}

/** 时长（秒）转中文，用于运行时长展示 */
export function formatUptime(seconds: number): string {
  if (!Number.isFinite(seconds) || seconds <= 0) return '—'
  const days = Math.floor(seconds / 86400)
  const hours = Math.floor((seconds % 86400) / 3600)
  const minutes = Math.floor((seconds % 3600) / 60)
  if (days > 0) return `${days} 天 ${hours} 小时`
  if (hours > 0) return `${hours} 小时 ${minutes} 分钟`
  return `${minutes} 分钟`
}

/** 执行结果的中文标签与展示类型 */
export const RESULT_META: Record<ExecutionResult, { label: string; type: 'success' | 'error' | 'warning' | 'info' }> = {
  success: { label: '成功', type: 'success' },
  failed: { label: '失败', type: 'error' },
  skipped: { label: '已跳过', type: 'warning' },
  running: { label: '执行中', type: 'info' },
}

/**
 * 转换为提交给后端的载荷。
 *
 * 为什么必须剔除字段：`createdAt` / `updatedAt` / `lastRunAt` 等是
 * 服务端生成的状态，客户端回传空字符串会让 Go 的 time.Time 解析失败
 * （`parsing time "" as "2006-01-02T15:04:05Z07:00"`），
 * 表现为「表单填对了却提交不了」。这类字段本就不该由客户端决定，
 * 剔除比让后端容错更干净。
 */
export function toTaskPayload(t: ScheduledTask): Record<string, unknown> {
  const payload: Record<string, unknown> = {
    name: t.name,
    kind: t.kind,
    recurrence: t.recurrence,
    timezone: t.timezone,
    enabled: t.enabled,
    catchUp: t.catchUp,
    catchUpWindowSeconds: t.catchUpWindowSeconds,
    minIntervalSeconds: t.minIntervalSeconds,
  }
  if (t.id > 0) payload.id = t.id
  return payload
}
