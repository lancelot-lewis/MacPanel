<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { useRouter } from 'vue-router'
import { useMessage } from 'naive-ui'
import { ApiException } from '../api/client'
import { store } from '../stores/app'

const router = useRouter()
const message = useMessage()
const password = ref('')
const submitting = ref(false)
const error = ref<string | null>(null)

const auth = computed(() => store.state.auth)

// 远程模式但未设置密码时，提示先在本机设置密码——
// 不要在远程端提供「免认证设置初始密码」的口子。
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
    message.success('登录成功')
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
          :style="{ background: 'var(--color-brand-mark)' }"
          aria-hidden="true"
        >
          HL
        </div>
        <h1 class="m-0 text-lg font-semibold">HomeLab 管理面板</h1>
        <p class="mt-1 mb-0 text-[12px]" :style="{ color: 'var(--color-text-secondary)' }">
          远程访问需要登录
        </p>
      </div>

      <NCard size="small">
        <NAlert v-if="needsLocalSetup" type="warning" :bordered="true">
          尚未设置管理密码。请先在运行本服务的 Mac 上打开
          <code class="mono">http://127.0.0.1:7788</code> 前往「设置」创建密码。
        </NAlert>

        <NForm v-else size="small" @submit.prevent="submit">
          <NFormItem label="密码" :show-feedback="false">
            <NInput
              v-model:value="password"
              type="password"
              show-password-on="click"
              placeholder="请输入管理密码"
              autocomplete="current-password"
              autofocus
              @keyup.enter="submit"
            />
          </NFormItem>

          <NButton
            type="primary"
            block
            class="mt-3"
            :loading="submitting"
            :disabled="!password || submitting"
            attr-type="submit"
            @click="submit"
          >
            登录
          </NButton>

          <NAlert v-if="error" type="error" :bordered="false" class="mt-3">
            {{ error }}
          </NAlert>
        </NForm>
      </NCard>

      <p
        class="mt-4 mb-0 text-center text-[11px] leading-relaxed"
        :style="{ color: 'var(--color-text-tertiary)' }"
      >
        登录失败次数受限（每地址 15 分钟内最多 5 次）。
        <br />
        远程访问应置于 HTTPS 反向代理之后。
      </p>
    </div>
  </div>
</template>
