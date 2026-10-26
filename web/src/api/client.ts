// 统一 API 客户端。
//
// 设计要点：
//  - 所有请求都带 X-HLM-Request 头（远程模式下后端强制要求，用于 CSRF 防护）；
//  - credentials: 'same-origin' 携带会话 cookie，但绝不向第三方泄露；
//  - 错误统一转换为 ApiError 抛出的 ApiException，调用方据此展示可读信息；
//  - 不吞异常：网络错误必须显式暴露，不能静默显示旧数据。

import type {
  ApiError,
  AuthStatus,
  Envelope,
  FirewallStatus,
  MetaInfo,
  NetworkSnapshot,
  SystemOverview,
  AuditEntry,
} from './types'

export class ApiException extends Error {
  readonly code: string
  readonly status: number
  readonly details?: Record<string, unknown>

  constructor(status: number, err: ApiError) {
    super(err.message)
    this.name = 'ApiException'
    this.code = err.code
    this.status = status
    this.details = err.details
  }
}

export interface ApiResult<T> {
  data: T
  meta?: Envelope<T>['meta']
}

async function request<T>(path: string, init: RequestInit = {}): Promise<ApiResult<T>> {
  const headers = new Headers(init.headers)
  headers.set('Accept', 'application/json')
  // 远程模式必需的自定义头；本地模式下后端忽略。
  headers.set('X-HLM-Request', '1')
  if (init.body && !headers.has('Content-Type')) {
    headers.set('Content-Type', 'application/json')
  }

  let resp: Response
  try {
    resp = await fetch(path, {
      ...init,
      headers,
      credentials: 'same-origin',
      // 明确禁止缓存：状态数据必须实时。
      cache: 'no-store',
    })
  } catch (e) {
    throw new ApiException(0, {
      code: 'network_error',
      message: `无法连接后端服务：${(e as Error).message}`,
    })
  }

  let body: Envelope<T> | null = null
  const text = await resp.text()
  if (text) {
    try {
      body = JSON.parse(text) as Envelope<T>
    } catch {
      throw new ApiException(resp.status, {
        code: 'invalid_response',
        message: `后端返回了非 JSON 响应（HTTP ${resp.status}）`,
      })
    }
  }

  if (!resp.ok || !body || body.ok === false) {
    throw new ApiException(
      resp.status,
      body?.error ?? {
        code: 'http_error',
        message: `请求失败（HTTP ${resp.status}）`,
      },
    )
  }

  return { data: body.data as T, meta: body.meta }
}

export const api = {
  async authStatus(): Promise<ApiResult<AuthStatus>> {
    return request<AuthStatus>('/api/v1/auth/status')
  },

  async login(password: string): Promise<ApiResult<unknown>> {
    return request('/api/v1/auth/login', {
      method: 'POST',
      body: JSON.stringify({ password }),
    })
  },

  async logout(): Promise<ApiResult<unknown>> {
    return request('/api/v1/auth/logout', { method: 'POST' })
  },

  async setPassword(currentPassword: string, newPassword: string): Promise<ApiResult<unknown>> {
    return request('/api/v1/auth/password', {
      method: 'POST',
      body: JSON.stringify({ currentPassword, newPassword }),
    })
  },

  async meta(): Promise<ApiResult<MetaInfo>> {
    return request<MetaInfo>('/api/v1/meta')
  },

  async overview(): Promise<ApiResult<SystemOverview>> {
    return request<SystemOverview>('/api/v1/system/overview')
  },

  async network(): Promise<ApiResult<NetworkSnapshot>> {
    return request<NetworkSnapshot>('/api/v1/system/network')
  },

  async firewallStatus(): Promise<ApiResult<FirewallStatus>> {
    return request<FirewallStatus>('/api/v1/firewall/status')
  },

  async audit(limit = 50, action = ''): Promise<ApiResult<{ items: AuditEntry[]; limit: number }>> {
    const qs = new URLSearchParams({ limit: String(limit) })
    if (action) qs.set('action', action)
    return request(`/api/v1/audit?${qs.toString()}`)
  },
}

// ---- M2：防火墙规则 ----

import type {
  ApplyResult,
  FirewallTemplate,
  PreviewResult,
  RuleSet,
  RulesGetResult,
  TransactionState,
} from './firewall'

export const firewallApi = {
  async getRules(): Promise<ApiResult<RulesGetResult>> {
    return request<RulesGetResult>('/api/v1/firewall/rules')
  },

  async saveRules(ruleSet: RuleSet): Promise<ApiResult<PreviewResult>> {
    return request<PreviewResult>('/api/v1/firewall/rules', {
      method: 'PUT',
      body: JSON.stringify(ruleSet),
    })
  },

  /** 预览未保存的规则（改了还没存时也能看生成的 PF 文本） */
  async preview(ruleSet?: RuleSet): Promise<ApiResult<PreviewResult>> {
    return request<PreviewResult>('/api/v1/firewall/preview', {
      method: 'POST',
      body: JSON.stringify(ruleSet ? { ruleSet } : {}),
    })
  },

  /** 应用规则。必须显式确认风险——后端会校验 confirmsRisk。 */
  async apply(ruleSet?: RuleSet): Promise<ApiResult<ApplyResult>> {
    return request<ApplyResult>('/api/v1/firewall/apply', {
      method: 'POST',
      body: JSON.stringify({ ...(ruleSet ? { ruleSet } : {}), confirmsRisk: true }),
    })
  },

  async confirm(): Promise<ApiResult<TransactionState>> {
    return request<TransactionState>('/api/v1/firewall/confirm', { method: 'POST' })
  },

  async rollback(reason: string): Promise<ApiResult<TransactionState>> {
    return request<TransactionState>('/api/v1/firewall/rollback', {
      method: 'POST',
      body: JSON.stringify({ reason }),
    })
  },

  async transaction(): Promise<ApiResult<{ state: TransactionState }>> {
    return request<{ state: TransactionState }>('/api/v1/firewall/transaction')
  },

  async templates(): Promise<ApiResult<{ items: FirewallTemplate[] }>> {
    return request<{ items: FirewallTemplate[] }>('/api/v1/firewall/templates')
  },
}

// ---- M3：电源与计划任务 ----

import type {
  PowerStatus,
  ScheduledTask,
  SyncResult,
  TaskExecution,
} from './power'
import { toTaskPayload } from './power'

export const powerApi = {
  async status(): Promise<ApiResult<PowerStatus>> {
    return request<PowerStatus>('/api/v1/power/status')
  },

  /**
   * 排定重启。必须显式带 confirmsRisk —— 后端会校验，
   * 缺失时直接 400 且不触达 Agent。
   */
  async reboot(delaySeconds: number, reason: string): Promise<ApiResult<PowerStatus>> {
    return request<PowerStatus>('/api/v1/power/reboot', {
      method: 'POST',
      body: JSON.stringify({ delaySeconds, reason, confirmsRisk: true }),
    })
  },

  async cancelReboot(): Promise<ApiResult<PowerStatus>> {
    return request<PowerStatus>('/api/v1/power/cancel', { method: 'POST', body: '{}' })
  },
}

export const scheduleApi = {
  async list(): Promise<ApiResult<{ items: ScheduledTask[] }>> {
    return request<{ items: ScheduledTask[] }>('/api/v1/schedules')
  },

  async create(task: ScheduledTask): Promise<ApiResult<ScheduledTask>> {
    return request<ScheduledTask>('/api/v1/schedules', {
      method: 'POST',
      body: JSON.stringify(toTaskPayload(task)),
    })
  },

  async update(task: ScheduledTask): Promise<ApiResult<ScheduledTask>> {
    return request<ScheduledTask>(`/api/v1/schedules/${task.id}`, {
      method: 'PUT',
      body: JSON.stringify(toTaskPayload(task)),
    })
  },

  async remove(id: number): Promise<ApiResult<{ ok: boolean }>> {
    return request<{ ok: boolean }>(`/api/v1/schedules/${id}`, { method: 'DELETE' })
  },

  async executions(taskId = 0, limit = 50): Promise<ApiResult<{ items: TaskExecution[] }>> {
    const q = new URLSearchParams({ limit: String(limit) })
    if (taskId > 0) q.set('taskId', String(taskId))
    return request<{ items: TaskExecution[] }>(`/api/v1/schedules/executions?${q}`)
  },

  /** 立即执行一次。同样需要风险确认。 */
  async runNow(id: number): Promise<ApiResult<TaskExecution>> {
    return request<TaskExecution>(`/api/v1/schedules/${id}/run`, {
      method: 'POST',
      body: JSON.stringify({ confirmsRisk: true }),
    })
  },

  async sync(): Promise<ApiResult<SyncResult>> {
    return request<SyncResult>('/api/v1/schedules/sync', { method: 'POST', body: '{}' })
  },
}

// ---- M4：启动项管理 ----

import type {
  ManagedService,
  ServiceActionResult,
  ServiceDetail,
  ServicesOverview,
} from './services'

export const servicesApi = {
  async list(includeGui = true): Promise<ApiResult<ServicesOverview>> {
    const q = new URLSearchParams()
    if (includeGui) q.set('includeGui', '1')
    return request<ServicesOverview>(`/api/v1/services?${q}`)
  },

  async detail(label: string, domain = 'system'): Promise<ApiResult<ServiceDetail>> {
    const q = new URLSearchParams({ label, domain })
    return request<ServiceDetail>(`/api/v1/services/detail?${q}`)
  },

  /** 登记受管服务。登记后仍需 allowControl 才允许启停。 */
  async register(svc: {
    label: string
    domain: string
    plistPath: string
    displayName?: string
    allowControl: boolean
  }): Promise<ApiResult<ManagedService>> {
    return request<ManagedService>('/api/v1/services/register', {
      method: 'POST',
      body: JSON.stringify(svc),
    })
  },

  async unregister(label: string): Promise<ApiResult<{ ok: boolean }>> {
    const q = new URLSearchParams({ label })
    return request<{ ok: boolean }>(`/api/v1/services/register?${q}`, { method: 'DELETE' })
  },

  /** 启停服务。必须带 confirmsRisk。 */
  async control(label: string, action: string): Promise<ApiResult<ServiceActionResult>> {
    return request<ServiceActionResult>('/api/v1/services/control', {
      method: 'POST',
      body: JSON.stringify({ label, action, confirmsRisk: true }),
    })
  },
}

// ---- M6：网络诊断与容器 ----

import type {
  ContainersOverview,
  DiagnosticsSnapshot,
  TCPProbeResult,
} from './diagnostics'

export const diagApi = {
  async snapshot(): Promise<ApiResult<DiagnosticsSnapshot>> {
    return request<DiagnosticsSnapshot>('/api/v1/diagnostics')
  },

  /** 检测 TCP 连通性。目标默认限本机地址（后端强制）。 */
  async probe(host: string, port: number, timeoutMs = 3000): Promise<ApiResult<TCPProbeResult>> {
    return request<TCPProbeResult>('/api/v1/diagnostics/tcp', {
      method: 'POST',
      body: JSON.stringify({ host, port, timeoutMs }),
    })
  },
}

export const orbstackApi = {
  async containers(): Promise<ApiResult<ContainersOverview>> {
    return request<ContainersOverview>('/api/v1/orbstack/containers')
  },
}
