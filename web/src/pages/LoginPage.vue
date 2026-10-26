<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { useRouter } from 'vue-router'
import { ApiException } from '../api/client'
import { store } from '../stores/app'

const router = useRouter()
const password = ref('')
const submitting = ref(false)
const error = ref<string | null>(null)

const auth = computed(() => store.state.auth)

// 远程模式但未设置密码时，提示先在本机设置密码——不要在远程端提供「免认证设置初始密码」的口子。
const needsLocalSetup = computed(
  () => auth.value?.mode === 'remote' && !auth.value.passwordSet,
)

onMounted(async () => {
  if (!store.state.auth) await store.loadAuth()
})

async function submit() {
  if (!password.value || submitting.value) return
  submitting.value = true
  error.value = null
  try {
    await store.login(password.value)
    void router.replace('/')
  } catch (e) {
    error.value = e instanceof ApiException ? e.message : String(e)
  } finally {
    submitting.value = false
  }
}
</script>

<template>
  <div class="grid min-h-dvh place-items-center px-4">
    <div class="w-full max-w-sm">
      <div class="mb-6 text-center">
        <div
          class="mx-auto mb-3 grid size-11 place-items-center rounded-xl text-sm font-bold text-white"
          style="background: var(--color-accent)"
          aria-hidden="true"
        >
          HL
        </div>
        <h1 class="m-0 text-lg font-semibold">HomeLab 管理面板</h1>
        <p class="mt-1 mb-0 text-[12px] text-[var(--color-text-secondary)]">
          远程访问需要登录
        </p>
      </div>

      <div class="card p-5">
        <div
          v-if="needsLocalSetup"
          class="rounded-lg px-3 py-2.5 text-[12px] leading-relaxed"
          style="
            background: color-mix(in oklab, var(--color-warn) 12%, transparent);
            border: 1px solid color-mix(in oklab, var(--color-warn) 38%, transparent);
          "
        >
          尚未设置管理密码。请先在运行本服务的 Mac 上打开
          <code class="mono">http://127.0.0.1:7788</code> 前往「设置」创建密码。
        </div>

        <form v-else class="flex flex-col gap-3" novalidate @submit.prevent="submit">
          <div>
            <label for="password" class="mb-1 block text-[12px] font-medium">密码</label>
            <input
              id="password"
              v-model="password"
              type="password"
              autocomplete="current-password"
              autofocus
              class="w-full rounded-md border border-[var(--color-border-subtle)] bg-[var(--color-surface)] px-2.5 py-2 text-[13px]"
            />
          </div>

          <button
            type="submit"
            :disabled="!password || submitting"
            class="rounded-md px-3 py-2 text-[13px] font-medium text-white disabled:cursor-not-allowed disabled:opacity-45"
            style="background: var(--color-accent)"
          >
            {{ submitting ? '登录中…' : '登录' }}
          </button>

          <p v-if="error" class="m-0 text-[12px] text-[var(--color-danger)]">{{ error }}</p>
        </form>
      </div>

      <p class="mt-4 mb-0 text-center text-[11px] leading-relaxed text-[var(--color-text-secondary)]">
        登录失败次数受限（每地址 15 分钟内最多 5 次）。
        <br />
        远程访问应置于 HTTPS 反向代理之后。
      </p>
    </div>
  </div>
</template>
