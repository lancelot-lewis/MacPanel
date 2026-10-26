<script setup lang="ts">
import { computed, onMounted, watch } from 'vue'
import { useRouter } from 'vue-router'
import { zhCN, dateZhCN } from 'naive-ui'
import { store } from './stores/app'
import { naiveOverrides, naiveTheme } from './theme/mode'
import AppShell from './layouts/AppShell.vue'

const router = useRouter()

onMounted(async () => {
  await store.loadAuth()
  // 远程模式下未登录时强制跳转登录页；
  // 本地模式下无需登录，保持当前路由。
  if (store.needsLogin.value) {
    void router.replace('/login')
  }
})

// 认证状态变化时再次校正路由，避免登录后仍停留在登录页。
watch(
  () => store.state.auth,
  (auth) => {
    if (!auth) return
    const onLogin = router.currentRoute.value.name === 'login'
    if (auth.mode === 'remote' && !auth.authenticated && !onLogin) {
      void router.replace('/login')
    }
    if (onLogin && (auth.mode === 'local' || auth.authenticated)) {
      void router.replace('/')
    }
  },
)

const isLogin = computed(() => router.currentRoute.value.name === 'login')
</script>

<template>
  <!-- 全局主题注入：组件库主题与 CSS 变量由同一响应式来源驱动，
       避免出现「卡片深色、底色浅色」的撕裂。 -->
  <NConfigProvider
    :theme="naiveTheme"
    :theme-overrides="naiveOverrides"
    :locale="zhCN"
    :date-locale="dateZhCN"
  >
    <NMessageProvider :max="3" placement="top">
      <NDialogProvider>
        <RouterView v-slot="{ Component, route }">
          <!-- 登录页不套管理布局 -->
          <component :is="Component" v-if="Component && isLogin" :key="route.path" />
          <AppShell v-else-if="Component">
            <component :is="Component" :key="route.path" />
          </AppShell>
        </RouterView>
      </NDialogProvider>
    </NMessageProvider>
  </NConfigProvider>
</template>
