<script setup lang="ts">
// 待确认横幅：显示倒计时并要求用户确认，超时自动回滚。
//
// 设计要点：
//  1. 倒计时基于后端返回的绝对 deadline 计算，而不是「收到响应时开始数 60 秒」。
//     后者会因网络延迟、页面切后台导致本地与后端不一致。
//  2. 剩余时间归零时不做乐观 UI——不自己把状态改成「已回滚」，
//     而是通知父组件去刷新真实状态。因为回滚可能失败，UI 不能替后端下结论。
//  3. 提供「立即回滚」按钮：用户发现服务不可达时可以马上恢复，不必等超时。

import { computed, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { NAlert, NButton, NProgress, NSpace, NTag } from 'naive-ui'
import type { TransactionState } from '../api/firewall'

const props = defineProps<{
  state: TransactionState
  /** 应用时的警告（例如强制加入的规则），在确认前必须让用户看到 */
  warnings?: string[] | null
  confirming?: boolean
  rollingBack?: boolean
}>()

const emit = defineEmits<{
  confirm: []
  rollback: []
  /** 倒计时归零，父组件应刷新真实状态 */
  expired: []
}>()

const now = ref(Date.now())
let timer: number | null = null

const deadlineMs = computed(() => {
  if (!props.state.deadline) return 0
  const t = new Date(props.state.deadline).getTime()
  return Number.isNaN(t) ? 0 : t
})

const remainingMs = computed(() => Math.max(0, deadlineMs.value - now.value))
const remainingSec = computed(() => Math.ceil(remainingMs.value / 1000))

// 总时长：用 appliedAt 与 deadline 之差，而不是硬编码 60 秒
const totalMs = computed(() => {
  const applied = props.state.appliedAt ? new Date(props.state.appliedAt).getTime() : 0
  if (!applied || !deadlineMs.value) return 60_000
  return Math.max(1, deadlineMs.value - applied)
})

const percent = computed(() => {
  if (totalMs.value <= 0) return 0
  return Math.min(100, Math.max(0, (remainingMs.value / totalMs.value) * 100))
})

const urgency = computed<'info' | 'warning' | 'error'>(() => {
  const s = remainingSec.value
  if (s <= 10) return 'error'
  if (s <= 30) return 'warning'
  return 'info'
})

const progressStatus = computed(() => {
  const s = remainingSec.value
  if (s <= 10) return 'error' as const
  if (s <= 30) return 'warning' as const
  return 'success' as const
})

let expiredFired = false

function tick() {
  now.value = Date.now()
  if (remainingMs.value <= 0 && !expiredFired) {
    expiredFired = true
    stopTimer()
    emit('expired')
  }
}

function startTimer() {
  stopTimer()
  expiredFired = false
  tick()
  timer = window.setInterval(tick, 1000)
}

function stopTimer() {
  if (timer !== null) {
    window.clearInterval(timer)
    timer = null
  }
}

onMounted(startTimer)
onBeforeUnmount(stopTimer)

// deadline 变化（重新应用）时重置
watch(
  () => props.state.deadline,
  () => startTimer(),
)
</script>

<template>
  <NAlert :type="urgency" :bordered="true" class="txn-banner">
    <template #header>
      <span class="inline-flex flex-wrap items-center gap-2">
        <NTag :type="urgency" size="small" :bordered="false" strong>待确认</NTag>
        <span>新规则已应用，请在倒计时结束前确认</span>
        <span class="mono text-[15px] font-semibold">{{ remainingSec }} 秒</span>
      </span>
    </template>

    <div class="flex flex-col gap-3">
      <NProgress
        type="line"
        :percentage="percent"
        :status="progressStatus"
        :show-indicator="false"
        :height="6"
        :border-radius="3"
      />

      <p class="m-0 text-[12.5px] leading-relaxed">
        超时未确认将
        <strong>自动回滚</strong>到应用前的状态。请立即验证你的服务是否仍可访问——
        如果远程连接已中断，回滚会让你恢复访问。
      </p>

      <!-- 强制加入的规则必须在确认前展示：用户要为自己没写过的规则负责 -->
      <ul
        v-if="warnings && warnings.length > 0"
        class="m-0 list-disc space-y-1 pl-4 text-[12px]"
      >
        <li v-for="w in warnings" :key="w">{{ w }}</li>
      </ul>

      <NSpace>
        <NButton type="primary" size="small" :loading="confirming" @click="emit('confirm')">
          连接正常，确认配置
        </NButton>
        <NButton size="small" :loading="rollingBack" @click="emit('rollback')">
          立即回滚
        </NButton>
      </NSpace>

      <p class="m-0 text-[11px]" style="opacity: 0.75">
        配置版本 #{{ state.revisionId }} · 截止
        {{ state.deadline ? new Date(state.deadline).toLocaleTimeString('zh-CN', { hour12: false }) : '—' }}
      </p>
    </div>
  </NAlert>
</template>

<style scoped>
.txn-banner :deep(.n-alert-body__content) {
  font-size: 12.5px;
}
</style>
