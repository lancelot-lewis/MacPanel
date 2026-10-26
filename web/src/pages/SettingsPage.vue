<script setup lang="ts">
import { computed, ref } from 'vue'
import { api, ApiException } from '../api/client'
import { store } from '../stores/app'
import { formatDuration } from '../lib/format'
import PanelCard from '../components/PanelCard.vue'
import Callout from '../components/Callout.vue'

const meta = computed(() => store.state.meta)
const auth = computed(() => store.state.auth)

// 修改密码表单
const currentPassword = ref('')
const newPassword = ref('')
const confirmPassword = ref('')
const submitting = ref(false)
const formError = ref<string | null>(null)
const formSuccess = ref<string | null>(null)

const canSubmit = computed(() => {
  if (submitting.value) return false
  if (newPassword.value.length < 12) return false
  if (newPassword.value !== confirmPassword.value) return false
  // 已设置密码时必须填当前密码
  if (auth.value?.passwordSet && !currentPassword.value) return false
  return true
})

const passwordMismatch = computed(
  () => confirmPassword.value.length > 0 && newPassword.value !== confirmPassword.value,
)
const passwordTooShort = computed(
  () => newPassword.value.length > 0 && newPassword.value.length < 12,
)

const featureRows = computed(() => {
  const f = meta.value?.features
  if (!f) return []
  return [
    { key: 'firewallRead', label: '防火墙状态读取', value: f.firewallRead },
    { key: 'firewallWrite', label: '防火墙规则写入', value: f.firewallWrite },
    { key: 'powerControl', label: '电源与重启控制', value: f.powerControl },
    { key: 'serviceControl', label: '启动项启停', value: f.serviceControl },
    { key: 'scheduleControl', label: '定时任务管理', value: f.scheduleControl },
  ]
})

async function submitPassword() {
  if (!canSubmit.value) return
  submitting.value = true
  formError.value = null
  formSuccess.value = null
  try {
    await api.setPassword(currentPassword.value, newPassword.value)
    formSuccess.value = '密码已更新。修改密码会撤销所有其它会话。'
    currentPassword.value = ''
    newPassword.value = ''
    confirmPassword.value = ''
    await store.loadAuth()
  } catch (e) {
    formError.value = e instanceof ApiException ? e.message : String(e)
  } finally {
    submitting.value = false
  }
}

function startPasswordSetup() {
  void store.loadAuth()
}
</script>

<template>
  <div class="mx-auto flex max-w-4xl flex-col gap-4">
    <!-- 访问模式 -->
    <PanelCard title="访问模式">
      <div v-if="!auth" class="text-[13px] text-[var(--color-text-secondary)]">正在读取…</div>
      <template v-else>
        <div class="flex flex-wrap items-center gap-2">
          <span
            class="rounded-full px-2.5 py-1 text-[12px] font-medium"
            :style="{
              background: `color-mix(in oklab, var(--color-${auth.mode === 'local' ? 'ok' : 'warn'}) 14%, transparent)`,
              color: auth.mode === 'local' ? 'var(--color-ok)' : 'var(--color-warn)',
            }"
          >
            {{ auth.mode === 'local' ? '仅本机访问' : '远程访问已启用' }}
          </span>
          <span class="mono text-[12px] text-[var(--color-text-secondary)]">
            {{ meta?.access.listenAddr ?? '—' }}
          </span>
        </div>

        <p class="mt-3 mb-0 text-[12px] leading-relaxed text-[var(--color-text-secondary)]">
          <template v-if="auth.mode === 'local'">
            服务只监听回环地址，来自本机的请求直接视为已授权。跨站请求与 DNS rebinding 仍被
            Host / Origin 校验阻断。
          </template>
          <template v-else>
            远程访问要求先登录；所有请求必须携带会话 cookie 与自定义请求头，且建议置于 HTTPS
            反向代理之后，否则凭据会以明文传输。
          </template>
        </p>
      </template>
    </PanelCard>

    <!-- 密码管理 -->
    <PanelCard
      title="管理密码"
      :subtitle="auth?.mode === 'local' ? '仅本机模式下可设置；远程模式启用前必须先设置' : '远程登录凭据'"
    >
      <Callout
        v-if="auth && !auth.passwordSet"
        tone="warn"
        badge="未设置"
        title="尚未设置管理密码"
      >
        启用远程访问前必须设置密码，否则无法登录。密码使用 scrypt 加盐哈希存储，永不明文保存。
      </Callout>

      <form class="mt-3 flex flex-col gap-3" novalidate @submit.prevent="submitPassword">
        <div v-if="auth?.passwordSet">
          <label for="current-password" class="mb-1 block text-[12px] font-medium">
            当前密码
          </label>
          <input
            id="current-password"
            v-model="currentPassword"
            type="password"
            autocomplete="current-password"
            class="w-full max-w-sm rounded-md border border-[var(--color-border-subtle)] bg-[var(--color-surface)] px-2.5 py-1.5 text-[13px]"
          />
        </div>

        <div>
          <label for="new-password" class="mb-1 block text-[12px] font-medium">
            {{ auth?.passwordSet ? '新密码' : '密码' }}
          </label>
          <input
            id="new-password"
            v-model="newPassword"
            type="password"
            autocomplete="new-password"
            placeholder="至少 12 个字符"
            class="w-full max-w-sm rounded-md border border-[var(--color-border-subtle)] bg-[var(--color-surface)] px-2.5 py-1.5 text-[13px]"
          />
          <p v-if="passwordTooShort" class="mt-1 mb-0 text-[11px] text-[var(--color-warn)]">
            密码长度至少 12 个字符
          </p>
        </div>

        <div>
          <label for="confirm-password" class="mb-1 block text-[12px] font-medium">
            确认密码
          </label>
          <input
            id="confirm-password"
            v-model="confirmPassword"
            type="password"
            autocomplete="new-password"
            class="w-full max-w-sm rounded-md border border-[var(--color-border-subtle)] bg-[var(--color-surface)] px-2.5 py-1.5 text-[13px]"
          />
          <p v-if="passwordMismatch" class="mt-1 mb-0 text-[11px] text-[var(--color-warn)]">
            两次输入的密码不一致
          </p>
        </div>

        <div class="flex items-center gap-3">
          <button
            type="submit"
            :disabled="!canSubmit"
            class="rounded-md px-3 py-1.5 text-[13px] font-medium text-white disabled:cursor-not-allowed disabled:opacity-45"
            style="background: var(--color-accent)"
          >
            {{ submitting ? '保存中…' : auth?.passwordSet ? '修改密码' : '设置密码' }}
          </button>
          <button
            v-if="!auth?.passwordSet"
            type="button"
            class="text-[12px] text-[var(--color-text-secondary)] underline-offset-2 hover:underline"
            @click="startPasswordSetup"
          >
            刷新状态
          </button>
        </div>

        <p v-if="formError" class="m-0 text-[12px] text-[var(--color-danger)]">{{ formError }}</p>
        <p v-if="formSuccess" class="m-0 text-[12px]" style="color: var(--color-ok)">
          {{ formSuccess }}
        </p>
      </form>
    </PanelCard>

    <!-- 能力清单 -->
    <PanelCard title="功能可用性" subtitle="明确声明当前版本已实现与未实现的能力">
      <div v-if="meta" class="flex flex-col gap-1.5">
        <div
          v-for="row in featureRows"
          :key="row.key"
          class="flex items-center justify-between border-b border-[var(--color-border-subtle)] py-1.5 last:border-0"
        >
          <span class="text-[13px]">{{ row.label }}</span>
          <span
            class="rounded px-1.5 py-0.5 text-[10px] font-medium"
            :style="{
              background: `color-mix(in oklab, var(--color-${row.value ? 'ok' : 'text-secondary'}) 14%, transparent)`,
              color: row.value ? 'var(--color-ok)' : 'var(--color-text-secondary)',
            }"
          >
            {{ row.value ? '已实现' : '未实现' }}
          </span>
        </div>
      </div>
      <div v-else class="text-[13px] text-[var(--color-text-secondary)]">正在读取…</div>
    </PanelCard>

    <!-- 版本与环境 -->
    <PanelCard title="版本与环境">
      <dl v-if="meta" class="grid grid-cols-[auto_1fr] gap-x-5 gap-y-1.5 text-[12px]">
        <dt class="text-[var(--color-text-secondary)]">服务端版本</dt>
        <dd class="mono m-0">{{ meta.server.version }}</dd>
        <dt class="text-[var(--color-text-secondary)]">提交</dt>
        <dd class="mono m-0">{{ meta.server.commit }}</dd>
        <dt class="text-[var(--color-text-secondary)]">构建时间</dt>
        <dd class="mono m-0">{{ meta.server.buildTime }}</dd>
        <dt class="text-[var(--color-text-secondary)]">Go 版本</dt>
        <dd class="mono m-0">{{ meta.server.goVersion }}</dd>
        <dt class="text-[var(--color-text-secondary)]">平台</dt>
        <dd class="mono m-0">{{ meta.server.platform }}</dd>
        <dt class="text-[var(--color-text-secondary)]">Agent</dt>
        <dd class="m-0">
          <template v-if="meta.agentReachable">
            正常 · 运行 {{ formatDuration((Date.now() - new Date(meta.agent.startedAt).getTime()) / 1000) }}
            · root={{ meta.agent.isRoot ? '是' : '否' }}
          </template>
          <template v-else>
            <span style="color: var(--color-danger)">不可达</span>
          </template>
        </dd>
      </dl>
      <div v-else class="text-[13px] text-[var(--color-text-secondary)]">
        正在读取… <span v-if="store.state.metaError">{{ store.state.metaError }}</span>
      </div>
    </PanelCard>

    <Callout tone="info" badge="安全" title="凭据存储">
      密码以 scrypt（N=32768, r=8, p=1）加盐哈希存储于本机 SQLite；会话令牌仅存 SHA-256
      哈希。IPC 共享令牌文件权限 0600，仅 root 可读。
    </Callout>
  </div>
</template>
