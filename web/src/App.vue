<script setup lang="ts">
import { onMounted, watch } from 'vue'
import { useRouter } from 'vue-router'
import { store } from './stores/app'
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
</script>

<template>
  <RouterView v-slot="{ Component, route }">
    <AppShell>
      <component :is="Component" v-if="Component" :key="route.path" />
    </AppShell>
  </RouterView>
</template>
