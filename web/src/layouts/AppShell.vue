<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { store } from '../stores/app'

const route = useRoute()
const router = useRouter()
const mobileNavOpen = ref(false)

const nav = [
  { name: 'overview', path: '/', label: '概览' },
  { name: 'firewall', path: '/firewall', label: 'IPv6 防火墙' },
  { name: 'network', path: '/network', label: '网络诊断' },
  { name: 'audit', path: '/audit', label: '操作审计' },
  { name: 'settings', path: '/settings', label: '设置' },
]

// 登录页不套用管理布局。
const isLogin = computed(() => route.name === 'login')

const accessLabel = computed(() => {
  const auth = store.state.auth
  if (!auth) return '状态未知'
  return auth.mode === 'local' ? '仅本机访问' : '远程访问'
})

const agentBadge = computed(() => {
  const meta = store.state.meta
  if (!meta) return { text: 'Agent 状态未知', tone: 'muted' as const }
  if (!meta.agentReachable) return { text: 'Agent 不可达', tone: 'danger' as const }
  return { text: 'Agent 正常', tone: 'ok' as const }
})

onMounted(() => {
  if (!store.state.auth) void store.loadAuth()
  if (!store.state.meta) void store.loadMeta()
})

function closeNav() {
  mobileNavOpen.value = false
}

async function handleLogout() {
  await store.logout()
  void router.push('/login')
}
</script>

<template>
  <!-- 登录页：极简容器 -->
  <div v-if="isLogin" class="min-h-dvh">
    <slot />
  </div>

  <!-- 管理界面 -->
  <div v-else class="flex min-h-dvh flex-col lg:flex-row">
    <!-- 侧边栏（桌面）/ 顶部栏 + 抽屉（移动） -->
    <aside
      class="shrink-0 border-b border-[var(--color-border-subtle)] bg-[var(--color-surface)] lg:w-56 lg:border-r lg:border-b-0"
    >
      <div class="flex items-center justify-between px-4 py-3 lg:py-4">
        <div class="flex items-center gap-2">
          <div
            class="grid size-7 place-items-center rounded-md text-[11px] font-bold text-white"
            style="background: var(--color-accent)"
            aria-hidden="true"
          >
            HL
          </div>
          <div class="leading-tight">
            <div class="text-[13px] font-semibold">HomeLab 管理</div>
            <div class="text-[10px] text-[var(--color-text-secondary)]">{{ accessLabel }}</div>
          </div>
        </div>
        <button
          type="button"
          class="rounded p-1.5 text-[var(--color-text-secondary)] hover:bg-black/5 lg:hidden dark:hover:bg-white/10"
          :aria-expanded="mobileNavOpen"
          aria-controls="main-nav"
          aria-label="切换导航菜单"
          @click="mobileNavOpen = !mobileNavOpen"
        >
          <svg width="18" height="18" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2">
            <path d="M3 6h18M3 12h18M3 18h18" stroke-linecap="round" />
          </svg>
        </button>
      </div>

      <nav
        id="main-nav"
        class="px-2 pb-2 lg:pb-4"
        :class="mobileNavOpen ? 'block' : 'hidden lg:block'"
        aria-label="主导航"
      >
        <RouterLink
          v-for="item in nav"
          :key="item.name"
          :to="item.path"
          class="mb-0.5 flex items-center rounded-md px-2.5 py-2 text-[13px] transition-colors"
          :class="
            route.path === item.path
              ? 'bg-[color-mix(in_oklab,var(--color-accent)_12%,transparent)] font-medium text-[var(--color-accent)]'
              : 'text-[var(--color-text-primary)] hover:bg-black/5 dark:hover:bg-white/5'
          "
          :aria-current="route.path === item.path ? 'page' : undefined"
          @click="closeNav"
        >
          {{ item.label }}
        </RouterLink>
      </nav>
    </aside>

    <div class="flex min-w-0 flex-1 flex-col">
      <!-- 顶部状态条 -->
      <header
        class="flex items-center justify-between gap-3 border-b border-[var(--color-border-subtle)] px-4 py-2.5"
      >
        <h1 class="m-0 text-[15px] font-semibold">{{ route.meta.title ?? '' }}</h1>
        <div class="flex items-center gap-2">
          <span
            class="inline-flex items-center gap-1.5 rounded-full px-2 py-0.5 text-[11px] font-medium"
            :style="{
              background: `color-mix(in oklab, var(--color-${agentBadge.tone === 'ok' ? 'ok' : agentBadge.tone === 'danger' ? 'danger' : 'text-secondary'}) 12%, transparent)`,
              color:
                agentBadge.tone === 'ok'
                  ? 'var(--color-ok)'
                  : agentBadge.tone === 'danger'
                    ? 'var(--color-danger)'
                    : 'var(--color-text-secondary)',
            }"
          >
            <span
              class="size-1.5 rounded-full"
              :style="{
                background:
                  agentBadge.tone === 'ok'
                    ? 'var(--color-ok)'
                    : agentBadge.tone === 'danger'
                      ? 'var(--color-danger)'
                      : 'var(--color-text-secondary)',
              }"
              aria-hidden="true"
            />
            {{ agentBadge.text }}
          </span>

          <button
            v-if="store.state.auth?.mode === 'remote' && store.state.auth.authenticated"
            type="button"
            class="rounded-md border border-[var(--color-border-subtle)] px-2.5 py-1 text-[12px] hover:bg-black/5 dark:hover:bg-white/5"
            @click="handleLogout"
          >
            退出登录
          </button>
        </div>
      </header>

      <main class="min-w-0 flex-1 p-4 lg:p-6">
        <slot />
      </main>

      <footer
        class="px-4 py-3 text-[11px] text-[var(--color-text-secondary)] lg:px-6"
        v-if="store.state.meta"
      >
        服务端 {{ store.state.meta.server.version }} · Go {{ store.state.meta.server.goVersion }} ·
        {{ store.state.meta.server.platform }} · 监听 {{ store.state.meta.access.listenAddr }}
      </footer>
    </div>
  </div>
</template>
