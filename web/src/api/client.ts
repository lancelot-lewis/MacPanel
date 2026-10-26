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
