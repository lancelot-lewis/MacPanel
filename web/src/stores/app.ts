// 全局状态：认证与元信息。
//
// 刻意不引入 Pinia：当前只有两个共享状态，用简单的响应式单例即可，
// 避免为将来可能的复杂度提前付费。

import { computed, reactive, readonly } from 'vue'
import { api, ApiException } from '../api/client'
import type { AuthStatus, MetaInfo } from '../api/types'

interface State {
  auth: AuthStatus | null
  meta: MetaInfo | null
  authLoading: boolean
  metaLoading: boolean
  authError: string | null
  metaError: string | null
  // agentReachable 单独记录，便于布局层显示顶部告警。
  lastMetaAt: number | null
}

const state = reactive<State>({
  auth: null,
  meta: null,
  authLoading: false,
  metaLoading: false,
  authError: null,
  metaError: null,
  lastMetaAt: null,
})

export const store = {
  state: readonly(state),

  isRemote: computed(() => state.auth?.mode === 'remote'),
  isAuthenticated: computed(() => state.auth?.authenticated ?? false),
  needsLogin: computed(
    () => state.auth?.mode === 'remote' && !state.auth.authenticated,
  ),
  agentReachable: computed(() => state.meta?.agentReachable ?? false),
  features: computed(() => state.meta?.features ?? null),

  async loadAuth(): Promise<void> {
    state.authLoading = true
    state.authError = null
    try {
      const { data } = await api.authStatus()
      state.auth = data
    } catch (e) {
      // 认证状态查询失败时不要伪造「已登录」或「本地模式」，
      // 保留 null 让 UI 显示为未知。
      state.auth = null
      state.authError = e instanceof ApiException ? e.message : String(e)
    } finally {
      state.authLoading = false
    }
  },

  async loadMeta(): Promise<void> {
    state.metaLoading = true
    state.metaError = null
    try {
      const { data } = await api.meta()
      state.meta = data
      state.lastMetaAt = Date.now()
    } catch (e) {
      state.metaError = e instanceof ApiException ? e.message : String(e)
    } finally {
      state.metaLoading = false
    }
  },

  async login(password: string): Promise<void> {
    await api.login(password)
    await this.loadAuth()
    await this.loadMeta()
  },

  async logout(): Promise<void> {
    try {
      await api.logout()
    } finally {
      state.auth = null
      state.meta = null
      await this.loadAuth()
    }
  },
}
