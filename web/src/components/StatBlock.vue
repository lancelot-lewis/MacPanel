<script setup lang="ts">
// 值状态块：把「不可用」与「值为 0」严格区分。
//
// 为什么不用 NStatistic：它只有数值与标签，没有「数据缺失」这一语义。
// 本项目最硬的约束是不伪造状态，因此这里保留自有组件，
// 只在视觉上使用 Naive UI 的排版基调（字号、颜色变量）。

withDefaults(
  defineProps<{
    label: string
    /** 已格式化好的展示值。空字符串表示「无值可显示」。 */
    value?: string | null
    /** 补充说明，例如「10 逻辑 / 10 物理」 */
    hint?: string
    /** true 时渲染为「不可用」标签，与 value 互斥 */
    unavailable?: boolean
    /** 不可用的原因，作为 title 提示 */
    unavailableReason?: string
    /** 数值等宽显示（字节、负载、百分比等） */
    mono?: boolean
    /** 次要展示：字号更小、颜色更淡，用于非关键指标 */
    subtle?: boolean
  }>(),
  {
    value: null,
    hint: '',
    unavailable: false,
    unavailableReason: '',
    mono: true,
    subtle: false,
  },
)
</script>

<template>
  <div class="min-w-0">
    <div class="text-xs" :style="{ color: 'var(--color-text-secondary)' }">{{ label }}</div>

    <div v-if="unavailable" class="mt-1">
      <NTag size="small" :bordered="false" type="default" :title="unavailableReason">
        不可用
      </NTag>
    </div>

    <div
      v-else
      class="mt-1 truncate font-semibold"
      :class="[mono ? 'mono' : '', subtle ? 'text-[13px]' : 'text-[17px]']"
      :style="{ color: subtle ? 'var(--color-text-secondary)' : 'var(--color-text-primary)' }"
      :title="value ?? ''"
    >
      {{ value || '—' }}
    </div>

    <div
      v-if="hint"
      class="mt-0.5 truncate text-[11px]"
      :style="{ color: 'var(--color-text-tertiary)' }"
      :title="hint"
    >
      {{ hint }}
    </div>
  </div>
</template>
