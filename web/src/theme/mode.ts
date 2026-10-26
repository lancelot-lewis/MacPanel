// 主题模式管理。
//
// 为什么不用 Naive UI 的 useOsTheme 直接判断：
// 本项目同时用 CSS 变量（自有布局、Tailwind）和 Naive UI 组件。
// 两套必须严格同步，否则会出现「卡片是深色的、页面底色是浅色的」这种撕裂。
// 因此这里统一由一个响应式来源驱动：
//   系统偏好变化 → mode 变化 → 同时更新 html.dark 类 + Naive UI 的 theme 属性。
//
// 用户可显式切换（设置页），显式选择会覆盖系统偏好并持久化到 localStorage。

import { computed, ref, watch } from 'vue'
import { darkTheme, type GlobalTheme } from 'naive-ui'
import { darkOverrides, lightOverrides } from '../theme'

export type ThemeMode = 'light' | 'dark'

const STORAGE_KEY = 'hlm.theme'

// 用户显式选择的模式；null 表示跟随系统。
const explicitMode = ref<ThemeMode | null>(readStoredMode())

// 系统当前偏好
const systemDark = ref(prefersDark())

// 监听系统偏好变化
const mql = typeof window !== 'undefined' ? window.matchMedia('(prefers-color-scheme: dark)') : null
mql?.addEventListener('change', (e) => {
  systemDark.value = e.matches
})

function prefersDark(): boolean {
  if (typeof window === 'undefined') return false
  return window.matchMedia('(prefers-color-scheme: dark)').matches
}

function readStoredMode(): ThemeMode | null {
  if (typeof localStorage === 'undefined') return null
  const v = localStorage.getItem(STORAGE_KEY)
  return v === 'light' || v === 'dark' ? v : null
}

/** 当前生效的模式 */
export const mode = computed<ThemeMode>(() =>
  explicitMode.value ?? (systemDark.value ? 'dark' : 'light'),
)

/** 是否跟随系统 */
export const followingSystem = computed(() => explicitMode.value === null)

/** 传给 <NConfigProvider :theme> */
export const naiveTheme = computed<GlobalTheme | null>(() =>
  mode.value === 'dark' ? darkTheme : null,
)

/** 传给 <NConfigProvider :theme-overrides> */
export const naiveOverrides = computed(() =>
  mode.value === 'dark' ? darkOverrides : lightOverrides,
)

/** 切换模式；传 null 表示恢复跟随系统 */
export function setMode(next: ThemeMode | null): void {
  explicitMode.value = next
  if (next === null) {
    localStorage.removeItem(STORAGE_KEY)
  } else {
    localStorage.setItem(STORAGE_KEY, next)
  }
}

export function toggleMode(): void {
  setMode(mode.value === 'dark' ? 'light' : 'dark')
}

/** 把模式同步到 DOM（html.dark 类 + color-scheme） */
function applyToDOM(m: ThemeMode): void {
  const root = document.documentElement
  root.classList.toggle('dark', m === 'dark')
  // 让浏览器原生控件（滚动条、表单）也跟随
  root.style.colorScheme = m
}

// 初次应用 + 后续变化
applyToDOM(mode.value)
watch(mode, applyToDOM)
