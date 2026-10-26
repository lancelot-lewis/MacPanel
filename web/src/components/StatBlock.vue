<script setup lang="ts">
// 数值 + 说明的统计块。
// 当 value 为 null/undefined 时显示「不可用」并降低视觉权重，
// 让用户一眼区分「确实是 0」与「读不到」。
withDefaults(
  defineProps<{
    label: string
    value?: string | null
    hint?: string
    unavailable?: boolean
    unavailableReason?: string
  }>(),
  { value: '', hint: '', unavailable: false, unavailableReason: '' },
)
</script>

<template>
  <div class="min-w-0">
    <div class="text-xs text-[var(--color-text-secondary)]">{{ label }}</div>
    <div v-if="unavailable" class="mt-1">
      <span
        class="inline-flex items-center gap-1 rounded px-1.5 py-0.5 text-xs font-medium"
        style="
          background: color-mix(in oklab, var(--color-text-secondary) 12%, transparent);
          color: var(--color-text-secondary);
        "
        :title="unavailableReason"
      >
        不可用
      </span>
    </div>
    <div v-else class="stat-value mt-1 truncate" :title="value ?? ''">{{ value || '—' }}</div>
    <div v-if="hint" class="mt-0.5 truncate text-[11px] text-[var(--color-text-secondary)]">
      {{ hint }}
    </div>
  </div>
</template>
