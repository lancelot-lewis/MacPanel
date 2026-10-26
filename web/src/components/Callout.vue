<script setup lang="ts">
// 提示块：基于 NAlert 封装，统一种子与文案风格。
//
// 相比直接用 NAlert，这里多做三件事：
// 1. 固定 tone → type 的映射，避免各页面各写一套；
// 2. 支持 badge（如「只读」「M1」）——用来标注阶段与能力边界；
// 3. 默认不留关闭按钮：这些提示是状态说明而非可忽略的通知，
//    用户关掉后就看不到「PF 不可读不等于已关闭」这种关键信息了。

import { computed } from 'vue'

type Tone = 'info' | 'warn' | 'danger' | 'ok'

const props = withDefaults(
  defineProps<{
    tone?: Tone
    title?: string
    badge?: string
    /** 允许关闭时传 true，仅用于真正的临时通知 */
    closable?: boolean
  }>(),
  { tone: 'info', title: '', badge: '', closable: false },
)

const alertType = computed(() => {
  switch (props.tone) {
    case 'warn':
      return 'warning' as const
    case 'danger':
      return 'error' as const
    case 'ok':
      return 'success' as const
    default:
      return 'info' as const
  }
})
</script>

<template>
  <NAlert :type="alertType" :bordered="true" :closable="closable" class="hlm-alert">
    <template #header>
      <span class="inline-flex items-center gap-2">
        <NTag v-if="badge" size="small" :bordered="false" :type="alertType" strong>
          {{ badge }}
        </NTag>
        <span v-if="title">{{ title }}</span>
      </span>
    </template>
    <slot />
  </NAlert>
</template>

<style scoped>
/* 内容区行高放宽：技术说明文字比一般通知长，紧凑排版会难读 */
.hlm-alert :deep(.n-alert-body__content) {
  font-size: 12.5px;
  line-height: 1.65;
}
</style>
