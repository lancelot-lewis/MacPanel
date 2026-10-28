<script setup lang="ts">
import { computed, reactive, ref, watch } from 'vue'
import { useMessage } from 'naive-ui'
import type { FormInst, FormItemRule, FormRules } from 'naive-ui'
import { api, ApiException } from '../api/client'
import { store } from '../stores/app'
import { formatDuration } from '../lib/format'
import { followingSystem, mode, setMode, type ThemeMode } from '../theme/mode'
import PanelCard from '../components/PanelCard.vue'
import Callout from '../components/Callout.vue'

const message = useMessage()
const meta = computed(() => store.state.meta)
const auth = computed(() => store.state.auth)

// ---- 密码表单（M2 的重表单会用同一套模式：NForm + rules + FormInst.validate） ----
const formRef = ref<FormInst | null>(null)
const submitting = ref(false)

const form = reactive({
  currentPassword: '',
  newPassword: '',
  confirmPassword: '',
})

const rules = computed<FormRules>(() => {
  const r: FormRules = {
    newPassword: [
      { required: true, message: '请输入新密码', trigger: ['blur', 'input'] },
      {
        validator: (_rule: FormItemRule, value: string) => {
          if (!value || value.length < 12) {
            return new Error('密码长度至少 12 个字符')
          }
          return true
        },
        trigger: ['blur', 'input'],
      },
    ],
    confirmPassword: [
      { required: true, message: '请再次输入新密码', trigger: ['blur', 'input'] },
      {
        validator: (_rule: FormItemRule, value: string) => {
          if (value !== form.newPassword) {
            return new Error('两次输入的密码不一致')
          }
          return true
        },
        trigger: ['blur', 'input'],
      },
    ],
  }

  // 仅当已存在密码时才要求填当前密码。
  // 用条件赋值而非把 undefined 塞进 rules（后者类型不允许，也容易让人误解）。
  if (auth.value?.passwordSet) {
    r.currentPassword = [
      { required: true, message: '请输入当前密码', trigger: ['blur', 'input'] },
    ]
  }

  return r
})

const canSubmit = computed(
  () =>
    !submitting.value &&
    form.newPassword.length >= 12 &&
    form.newPassword === form.confirmPassword &&
    (!auth.value?.passwordSet || form.currentPassword.length > 0),
)

// 改密码成功后撤销了所有会话，远程模式下会下发新 cookie，需要刷新认证状态
async function submitPassword() {
  if (!canSubmit.value) return
  try {
    await formRef.value?.validate()
  } catch {
    return // 校验失败的提示由表单自身展示
  }

  submitting.value = true
  try {
    await api.setPassword(form.currentPassword, form.newPassword)
    message.success('密码已更新，其它会话已被撤销')
    form.currentPassword = ''
    form.newPassword = ''
    form.confirmPassword = ''
    await store.loadAuth()
  } catch (e) {
    message.error(e instanceof ApiException ? e.message : String(e))
  } finally {
    submitting.value = false
  }
}

// ---- 能力清单 ----
const featureRows = computed(() => {
  const f = meta.value?.features
  if (!f) return []
  return [
    { key: 'firewallRead', label: '防火墙状态读取', value: f.firewallRead, milestone: 'M1' },
    { key: 'firewallWrite', label: '防火墙规则写入', value: f.firewallWrite, milestone: 'M2' },
    { key: 'powerControl', label: '电源与重启控制', value: f.powerControl, milestone: 'M3' },
    { key: 'serviceControl', label: '启动项启停', value: f.serviceControl, milestone: 'M4' },
    { key: 'scheduleControl', label: '定时任务管理', value: f.scheduleControl, milestone: 'M3' },
  ]
})

// ---- 主题 ----
const themeOptions = computed(() => [
  { label: '跟随系统', value: 'auto' },
  { label: '浅色', value: 'light' },
  { label: '深色', value: 'dark' },
])

const themeValue = computed(() => (followingSystem.value ? 'auto' : mode.value))

function onThemeChange(v: string) {
  setMode(v === 'auto' ? null : (v as ThemeMode))
}

// 监听认证状态变化：远程模式下改密码后会话已轮换，密码框应清空
watch(
  () => auth.value?.passwordSet,
  () => {
    form.currentPassword = ''
  },
)
</script>

<template>
  <div class="page-stack page-narrow">
    <!-- 访问模式 -->
    <PanelCard title="访问模式">
      <div v-if="!auth" class="text-[13px]" :style="{ color: 'var(--color-text-secondary)' }">
        正在读取…
      </div>
      <template v-else>
        <div class="flex flex-wrap items-center gap-2">
          <NTag
            :type="auth.mode === 'local' ? 'success' : 'warning'"
            :bordered="false"
            size="medium"
            round
          >
            {{ auth.mode === 'local' ? '仅本机访问' : '远程访问已启用' }}
          </NTag>
          <span class="mono text-[12px]" :style="{ color: 'var(--color-text-secondary)' }">
            {{ meta?.access.listenAddr ?? '—' }}
          </span>
        </div>

        <p
          class="mt-3 mb-0 text-[12px] leading-relaxed"
          :style="{ color: 'var(--color-text-secondary)' }"
        >
          <template v-if="auth.mode === 'local'">
            服务只监听回环地址，来自本机的请求直接视为已授权。跨站请求与 DNS rebinding 仍被
            Host / Origin 校验阻断。
          </template>
          <template v-else>
            远程访问要求先登录；所有请求必须携带会话 cookie 与自定义请求头，且建议置于 HTTPS
            反向代理之后，否则凭据会以明文传输。
          </template>
        </p>

        <Callout
          v-if="auth.mode === 'local'"
          tone="info"
          badge="局域网"
          title="需要别的设备访问？"
          class="mt-3"
        >
          在运行本服务的 Mac 上执行
          <code>sudo macpanel config set listen 0.0.0.0:7788</code>，再
          <code>sudo launchctl kickstart -k system/com.macpanel.server</code> 生效。
          开启后所有访问（含本机）都需要登录，请先在本页设置管理密码。
        </Callout>
      </template>
    </PanelCard>

    <!-- 外观 -->
    <PanelCard title="外观" subtitle="主题与系统偏好保持同步，也可手动固定">
      <div class="flex flex-wrap items-center gap-3">
        <NRadioGroup :value="themeValue" size="small" @update:value="onThemeChange">
          <NRadioButton
            v-for="opt in themeOptions"
            :key="opt.value"
            :value="opt.value"
            :label="opt.label"
          />
        </NRadioGroup>
        <span class="text-[11px]" :style="{ color: 'var(--color-text-tertiary)' }">
          {{
            followingSystem
              ? '当前跟随系统偏好，系统切换外观时面板会同步变化'
              : '已手动固定，不再跟随系统'
          }}
        </span>
      </div>
    </PanelCard>

    <!-- 密码 -->
    <PanelCard
      title="管理密码"
      :subtitle="
        auth?.mode === 'local'
          ? '仅本机模式下可设置；远程模式启用前必须先设置'
          : '远程登录凭据'
      "
    >
      <Callout
        v-if="auth && !auth.passwordSet"
        tone="warn"
        badge="未设置"
        title="尚未设置管理密码"
        class="mb-3"
      >
        启用远程访问前必须设置密码，否则无法登录。密码使用 scrypt 加盐哈希存储，永不明文保存。
      </Callout>

      <NForm
        ref="formRef"
        :model="form"
        :rules="rules"
        label-placement="top"
        size="small"
        class="max-w-sm"
        @submit.prevent="submitPassword"
      >
        <NFormItem v-if="auth?.passwordSet" label="当前密码" path="currentPassword">
          <NInput
            v-model:value="form.currentPassword"
            type="password"
            show-password-on="click"
            placeholder="请输入当前密码"
            autocomplete="current-password"
          />
        </NFormItem>

        <NFormItem :label="auth?.passwordSet ? '新密码' : '密码'" path="newPassword">
          <NInput
            v-model:value="form.newPassword"
            type="password"
            show-password-on="click"
            placeholder="至少 12 个字符"
            autocomplete="new-password"
          />
        </NFormItem>

        <NFormItem label="确认密码" path="confirmPassword">
          <NInput
            v-model:value="form.confirmPassword"
            type="password"
            show-password-on="click"
            placeholder="再次输入"
            autocomplete="new-password"
            @keyup.enter="submitPassword"
          />
        </NFormItem>

        <NButton
          type="primary"
          size="small"
          :loading="submitting"
          :disabled="!canSubmit"
          @click="submitPassword"
        >
          {{ auth?.passwordSet ? '修改密码' : '设置密码' }}
        </NButton>
      </NForm>
    </PanelCard>

    <!-- 能力清单 -->
    <PanelCard title="功能可用性" subtitle="明确声明当前版本已实现与未实现的能力" :padded="false">
      <NTable v-if="meta" :single-line="false" size="small">
        <thead>
          <tr>
            <th>功能</th>
            <th style="width: 90px">里程碑</th>
            <th style="width: 110px">状态</th>
          </tr>
        </thead>
        <tbody>
          <tr v-for="row in featureRows" :key="row.key">
            <td>{{ row.label }}</td>
            <td class="mono text-[12px]" :style="{ color: 'var(--color-text-tertiary)' }">
              {{ row.milestone }}
            </td>
            <td>
              <NTag size="small" :bordered="false" :type="row.value ? 'success' : 'default'">
                {{ row.value ? '已实现' : '未实现' }}
              </NTag>
            </td>
          </tr>
        </tbody>
      </NTable>
      <div v-else class="p-4 text-[13px]" :style="{ color: 'var(--color-text-secondary)' }">
        正在读取…
      </div>
    </PanelCard>

    <!-- 版本 -->
    <PanelCard title="版本与环境">
      <NDescriptions v-if="meta" :column="1" label-placement="left" size="small" :bordered="false">
        <NDescriptionsItem label="服务端版本">
          <span class="mono">{{ meta.server.version }}</span>
        </NDescriptionsItem>
        <NDescriptionsItem label="提交">
          <span class="mono">{{ meta.server.commit }}</span>
        </NDescriptionsItem>
        <NDescriptionsItem label="构建时间">
          <span class="mono">{{ meta.server.buildTime }}</span>
        </NDescriptionsItem>
        <NDescriptionsItem label="Go 版本">
          <span class="mono">{{ meta.server.goVersion }}</span>
        </NDescriptionsItem>
        <NDescriptionsItem label="平台">
          <span class="mono">{{ meta.server.platform }}</span>
        </NDescriptionsItem>
        <NDescriptionsItem label="Agent">
          <template v-if="meta.agentReachable">
            正常 · 运行
            {{ formatDuration((Date.now() - new Date(meta.agent.startedAt).getTime()) / 1000) }} ·
            root={{ meta.agent.isRoot ? '是' : '否' }}
          </template>
          <NTag v-else size="small" type="error" :bordered="false">不可达</NTag>
        </NDescriptionsItem>
      </NDescriptions>
      <div v-else class="text-[13px]" :style="{ color: 'var(--color-text-secondary)' }">
        正在读取…
        <span v-if="store.state.metaError">{{ store.state.metaError }}</span>
      </div>
    </PanelCard>

    <Callout tone="info" badge="安全" title="凭据存储">
      密码以 scrypt（N=32768, r=8, p=1）加盐哈希存储于本机 SQLite；会话令牌仅存 SHA-256
      哈希。IPC 共享令牌文件权限 0600，仅 root 可读。
    </Callout>
  </div>
</template>
