<script setup lang="ts">
// 应用骨架：侧边栏 + 顶栏 + 内容区。
//
// 用 NLayout / NLayoutSider / NMenu 而不是手写 div：
// NMenu 自带键盘导航、焦点管理与无障碍语义，手写这些很容易出错，
// 而管理面板是长期自用的工具，键盘效率很重要。
//
// 移动端不用 NDrawer 而是收成顶部下拉菜单：
// 侧栏只有 8 项，抽屉的额外层级反而增加操作步数。

import { computed, h, onMounted, ref } from 'vue'
import type { VNodeChild } from 'vue'
import { RouterLink, useRoute, useRouter } from 'vue-router'
import { NIcon, useDialog, useMessage } from 'naive-ui'
import type { MenuOption } from 'naive-ui'
import { store } from '../stores/app'
import { followingSystem, mode, setMode, toggleMode } from '../theme/mode'

const route = useRoute()
const router = useRouter()
const message = useMessage()
const dialog = useDialog()

const collapsed = ref(false)

// 用内联 SVG 而非图标库：只有 8 个图标，不值得引入额外依赖。
// 返回 VNode 数组（h('svg', props, children) 的 children 形态）。
function renderIcon(children: VNodeChild[]) {
  return () =>
    h(NIcon, null, {
      default: () =>
        h(
          'svg',
          {
            viewBox: '0 0 24 24',
            fill: 'none',
            stroke: 'currentColor',
            'stroke-width': 1.8,
            'stroke-linecap': 'round',
            'stroke-linejoin': 'round',
            width: 16,
            height: 16,
          },
          children,
        ),
    })
}

const iconPaths: Record<string, VNodeChild[]> = {
  overview: [
    h('path', { d: 'M3 12l9-8 9 8' }),
    h('path', { d: 'M5 10v10h14V10' }),
  ],
  firewall: [
    h('path', { d: 'M12 3l7 3v6c0 4-3 7-7 9-4-2-7-5-7-9V6l7-3z' }),
    h('path', { d: 'M9 12l2 2 4-4' }),
  ],
  network: [
    h('circle', { cx: 12, cy: 12, r: 9 }),
    h('path', { d: 'M3 12h18M12 3c3 3 3 15 0 18M12 3c-3 3-3 15 0 18' }),
  ],
  power: [
    // 电源符号：圆弧 + 中间一竖
    h('path', { d: 'M12 3v9' }),
    h('path', { d: 'M18.4 6.6a9 9 0 11-12.8 0' }),
  ],
  services: [
    // 列表/堆叠图标，表示启动项
    h('path', { d: 'M4 6h16M4 12h16M4 18h10' }),
    h('circle', { cx: 20, cy: 18, r: 1.6 }),
  ],
  orbstack: [
    // 立方体图标，表示容器
    h('path', { d: 'M12 3l8 4.5v9L12 21l-8-4.5v-9L12 3z' }),
    h('path', { d: 'M12 12l8-4.5M12 12v9M12 12L4 7.5' }),
  ],
  audit: [h('path', { d: 'M4 5h16v14H4z' }), h('path', { d: 'M8 9h8M8 13h5' })],
  settings: [
    h('circle', { cx: 12, cy: 12, r: 3 }),
    h('path', {
      d: 'M19.4 15a1.6 1.6 0 00.3 1.8l.1.1a2 2 0 11-2.8 2.8l-.1-.1a1.6 1.6 0 00-1.8-.3 1.6 1.6 0 00-1 1.5V21a2 2 0 11-4 0v-.1A1.6 1.6 0 007 19.4a1.6 1.6 0 00-1.8.3l-.1.1a2 2 0 11-2.8-2.8l.1-.1a1.6 1.6 0 00.3-1.8 1.6 1.6 0 00-1.5-1H1a2 2 0 110-4h.1A1.6 1.6 0 003.6 7a1.6 1.6 0 00-.3-1.8l-.1-.1a2 2 0 112.8-2.8l.1.1a1.6 1.6 0 001.8.3H8a1.6 1.6 0 001-1.5V1a2 2 0 114 0v.1a1.6 1.6 0 001 1.5 1.6 1.6 0 001.8-.3l.1-.1a2 2 0 112.8 2.8l-.1.1a1.6 1.6 0 00-.3 1.8V8a1.6 1.6 0 001.5 1H23a2 2 0 110 4h-.1a1.6 1.6 0 00-1.5 1z',
    }),
  ],
}

const menuOptions = computed<MenuOption[]>(() => [
  {
    label: () => h(RouterLink, { to: '/' }, () => '概览'),
    key: '/',
    icon: renderIcon(iconPaths.overview!),
  },
  {
    label: () => h(RouterLink, { to: '/firewall' }, () => 'IPv6 防火墙'),
    key: '/firewall',
    icon: renderIcon(iconPaths.firewall!),
  },
  {
    label: () => h(RouterLink, { to: '/power' }, () => '电源与计划'),
    key: '/power',
    icon: renderIcon(iconPaths.power!),
  },
  {
    label: () => h(RouterLink, { to: '/services' }, () => '启动项'),
    key: '/services',
    icon: renderIcon(iconPaths.services!),
  },
  {
    label: () => h(RouterLink, { to: '/network' }, () => '网络诊断'),
    key: '/network',
    icon: renderIcon(iconPaths.network!),
  },
  {
    label: () => h(RouterLink, { to: '/orbstack' }, () => 'OrbStack'),
    key: '/orbstack',
    icon: renderIcon(iconPaths.orbstack!),
  },
  {
    label: () => h(RouterLink, { to: '/audit' }, () => '操作审计'),
    key: '/audit',
    icon: renderIcon(iconPaths.audit!),
  },
  {
    label: () => h(RouterLink, { to: '/settings' }, () => '设置'),
    key: '/settings',
    icon: renderIcon(iconPaths.settings!),
  },
])

const activeKey = computed(() => route.path)

// 移动端下拉菜单
const mobileMenuOpen = ref(false)

const accessLabel = computed(() => {
  const auth = store.state.auth
  if (!auth) return '状态未知'
  return auth.mode === 'local' ? '仅本机访问' : '远程访问'
})

const agentStatus = computed(() => {
  const meta = store.state.meta
  if (!meta) return { text: 'Agent 状态未知', type: 'default' as const }
  if (!meta.agentReachable) return { text: 'Agent 不可达', type: 'error' as const }
  return { text: 'Agent 正常', type: 'success' as const }
})

const themeIcon = computed(() => (mode.value === 'dark' ? '🌙' : '☀️'))

onMounted(() => {
  if (!store.state.auth) void store.loadAuth()
  if (!store.state.meta) void store.loadMeta()
})

async function handleLogout() {
  await store.logout()
  void router.push('/login')
}

function confirmLogout() {
  dialog.warning({
    title: '退出登录',
    content: '退出后需要重新输入密码才能访问远程面板。',
    positiveText: '退出',
    negativeText: '取消',
    onPositiveClick: () => {
      void handleLogout()
    },
  })
}

function cycleTheme() {
  toggleMode()
  message.success(`已切换为${mode.value === 'dark' ? '深色' : '浅色'}模式`, { duration: 1500 })
}
</script>

<template>
  <NLayout has-sider class="min-h-dvh">
    <!-- 侧边栏（仅桌面）。用外层 div 控制显隐：
         NLayoutSider 自身是 flex 元素，直接加 hidden/lg:block 会被组件样式覆盖。 -->
    <div class="hidden shrink-0 lg:block">
      <NLayoutSider
        bordered
        collapse-mode="width"
        :collapsed-width="56"
        :width="200"
        :collapsed="collapsed"
        show-trigger
        class="h-full"
        @collapse="collapsed = true"
        @expand="collapsed = false"
      >
      <div class="flex h-full flex-col">
        <!-- 品牌区 -->
        <div class="flex items-center gap-2 px-4 py-4">
          <div
            class="grid size-7 shrink-0 place-items-center rounded-md text-[11px] font-bold text-white"
            :style="{ background: 'var(--color-brand-mark)' }"
            aria-hidden="true"
          >
            HL
          </div>
          <div v-if="!collapsed" class="min-w-0 leading-tight">
            <div class="truncate text-[13px] font-semibold">MacPanel 管理</div>
            <div
              class="truncate text-[10.5px]"
              :style="{ color: 'var(--color-text-secondary)' }"
            >
              {{ accessLabel }}
            </div>
          </div>
        </div>

        <NMenu
          :value="activeKey"
          :options="menuOptions"
          :collapsed="collapsed"
          :collapsed-width="56"
          :collapsed-icon-size="18"
          class="flex-1"
        />

        <!-- 底部：主题切换 + 退出 -->
        <div class="border-t px-2 py-2" :style="{ borderColor: 'var(--color-border-subtle)' }">
          <NButton quaternary block :title="`切换主题（当前${mode === 'dark' ? '深色' : '浅色'}）`" @click="cycleTheme">
            <template #icon>{{ themeIcon }}</template>
            <template v-if="!collapsed">主题</template>
          </NButton>
          <NButton
            v-if="!collapsed && store.state.auth?.mode === 'remote' && store.state.auth.authenticated"
            quaternary
            block
            class="mt-1"
            @click="confirmLogout"
          >
            退出登录
          </NButton>
        </div>
        </div>
      </NLayoutSider>
    </div>

    <NLayout>
      <!-- 顶栏 -->
      <div
        class="flex items-center justify-between gap-3 border-b px-4 py-2.5"
        :style="{
          borderColor: 'var(--color-border-subtle)',
          background: 'var(--color-surface)',
        }"
      >
        <div class="flex min-w-0 items-center gap-2">
          <!-- 移动端：菜单下拉 -->
          <NDropdown
            v-model:show="mobileMenuOpen"
            :options="menuOptions"
            trigger="click"
            placement="bottom-start"
            class="lg:hidden"
          >
            <NButton quaternary size="small" aria-label="打开导航菜单">
              <template #icon>
                <NIcon :size="18">
                  <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2">
                    <path d="M3 6h18M3 12h18M3 18h18" stroke-linecap="round" />
                  </svg>
                </NIcon>
              </template>
            </NButton>
          </NDropdown>
          <h1 class="m-0 truncate text-[15px] font-semibold">{{ route.meta.title ?? '' }}</h1>
        </div>

        <div class="flex shrink-0 items-center gap-2">
          <NTag :type="agentStatus.type" size="small" :bordered="false" round>
            <template #icon>
              <span
                class="inline-block size-1.5 rounded-full"
                :style="{
                  background:
                    agentStatus.type === 'success'
                      ? 'var(--color-ok)'
                      : agentStatus.type === 'error'
                        ? 'var(--color-danger)'
                        : 'var(--color-text-tertiary)',
                }"
              />
            </template>
            {{ agentStatus.text }}
          </NTag>
          <NButton quaternary size="small" class="lg:hidden" aria-label="切换主题" @click="cycleTheme">
            {{ themeIcon }}
          </NButton>
        </div>
      </div>

      <!-- 内容区 -->
      <div class="p-4 lg:p-6">
        <slot />
      </div>

      <!-- 页脚 -->
      <div
        v-if="store.state.meta"
        class="px-4 pb-4 text-[11px] lg:px-6"
        :style="{ color: 'var(--color-text-tertiary)' }"
      >
        服务端 {{ store.state.meta.server.version }} · Go {{ store.state.meta.server.goVersion }} ·
        {{ store.state.meta.server.platform }} · 监听 {{ store.state.meta.access.listenAddr }}
        <template v-if="followingSystem"> · 主题跟随系统</template>
        <NButton
          v-if="!followingSystem"
          text
          size="tiny"
          class="ml-1"
          @click="setMode(null)"
        >
          恢复跟随系统
        </NButton>
      </div>
    </NLayout>
  </NLayout>
</template>
