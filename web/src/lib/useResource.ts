// 组合式函数：统一「加载 / 刷新 / 错误」三态，避免各页面重复样板。
import { onBeforeUnmount, onMounted, ref } from 'vue'
import type { Ref } from 'vue'
import { ApiException } from '../api/client'

export interface UseResourceOptions<T> {
  // 首次加载失败时不抛错，而是记录在 error 上。
  immediate?: boolean
  // 自动刷新间隔（毫秒），0 表示不刷新。
  intervalMs?: number
  // 是否在标签页不可见时暂停刷新。
  pauseWhenHidden?: boolean
  onLoaded?: (data: T) => void
}

export interface UseResource<T> {
  data: Ref<T | null>
  error: Ref<string | null>
  errorCode: Ref<string | null>
  loading: Ref<boolean>
  // meta 中的降级提示
  degraded: Ref<boolean>
  notes: Ref<string[]>
  lastUpdated: Ref<number | null>
  refresh: () => Promise<void>
  active: Ref<boolean>
}

export function useResource<T>(
  loader: () => Promise<{ data: T; meta?: { degraded?: boolean; notes?: string[] } }>,
  options: UseResourceOptions<T> = {},
): UseResource<T> {
  const { immediate = true, intervalMs = 0, pauseWhenHidden = true, onLoaded } = options

  const data = ref<T | null>(null) as Ref<T | null>
  const error = ref<string | null>(null)
  const errorCode = ref<string | null>(null)
  const loading = ref(false)
  const degraded = ref(false)
  const notes = ref<string[]>([])
  const lastUpdated = ref<number | null>(null)
  const active = ref(true)

  let timer: number | null = null
  let inFlight = false

  async function refresh(): Promise<void> {
    // 防止轮询叠加导致请求风暴。
    if (inFlight) return
    inFlight = true
    loading.value = true
    try {
      const res = await loader()
      data.value = res.data
      error.value = null
      errorCode.value = null
      degraded.value = res.meta?.degraded ?? false
      notes.value = res.meta?.notes ?? []
      lastUpdated.value = Date.now()
      onLoaded?.(res.data)
    } catch (e) {
      if (e instanceof ApiException) {
        error.value = e.message
        errorCode.value = e.code
      } else {
        error.value = e instanceof Error ? e.message : String(e)
        errorCode.value = 'unknown'
      }
      // 关键：失败时保留上一次成功的数据，但通过 error 明确告知「这不是最新状态」，
      // 绝不把旧数据当成当前状态展示而不给提示。
    } finally {
      loading.value = false
      inFlight = false
    }
  }

  function startTimer() {
    if (!intervalMs || intervalMs <= 0) return
    stopTimer()
    timer = window.setInterval(() => {
      if (pauseWhenHidden && document.hidden) return
      void refresh()
    }, intervalMs)
  }

  function stopTimer() {
    if (timer !== null) {
      window.clearInterval(timer)
      timer = null
    }
  }

  function onVisibility() {
    if (!document.hidden && active.value) {
      // 回到前台立即刷新一次，避免显示过期数据。
      void refresh()
    }
  }

  onMounted(() => {
    if (immediate) void refresh()
    startTimer()
    if (pauseWhenHidden) document.addEventListener('visibilitychange', onVisibility)
  })

  onBeforeUnmount(() => {
    stopTimer()
    active.value = false
    document.removeEventListener('visibilitychange', onVisibility)
  })

  return {
    data,
    error,
    errorCode,
    loading,
    degraded,
    notes,
    lastUpdated,
    refresh,
    active,
  }
}
