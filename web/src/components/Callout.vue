<script setup lang="ts">
// 统一的状态提示块。用于「不可用 / 未实现 / 模拟数据」等场景，
// 强调「不伪造状态」这一产品原则。
type Tone = 'info' | 'warn' | 'danger' | 'ok'

const props = withDefaults(
  defineProps<{
    tone?: Tone
    title?: string
    // 明确标记数据质量问题时使用，例如 unavailable
    badge?: string
  }>(),
  { tone: 'info', title: '', badge: '' },
)

const toneClass = {
  info: 'bg-[color-mix(in_oklab,var(--color-accent)_8%,transparent)] border-[color-mix(in_oklab,var(--color-accent)_30%,transparent)]',
  warn: 'bg-[color-mix(in_oklab,var(--color-warn)_12%,transparent)] border-[color-mix(in_oklab,var(--color-warn)_38%,transparent)]',
  danger:
    'bg-[color-mix(in_oklab,var(--color-danger)_10%,transparent)] border-[color-mix(in_oklab,var(--color-danger)_35%,transparent)]',
  ok: 'bg-[color-mix(in_oklab,var(--color-ok)_10%,transparent)] border-[color-mix(in_oklab,var(--color-ok)_32%,transparent)]',
} as const

const badgeClass = {
  info: 'bg-[var(--color-accent)] text-white',
  warn: 'bg-[var(--color-warn)] text-black',
  danger: 'bg-[var(--color-danger)] text-white',
  ok: 'bg-[var(--color-ok)] text-white',
} as const
</script>

<template>
  <div class="rounded-lg border px-3.5 py-3 text-[13px] leading-relaxed" :class="toneClass[props.tone]">
    <div class="flex items-start gap-2">
      <span
        v-if="badge"
        class="mt-0.5 shrink-0 rounded px-1.5 py-0.5 text-[10px] font-semibold tracking-wide uppercase"
        :class="badgeClass[props.tone]"
      >
        {{ badge }}
      </span>
      <div class="min-w-0 flex-1">
        <p v-if="title" class="m-0 font-semibold">{{ title }}</p>
        <div :class="title ? 'mt-1 opacity-85' : ''">
          <slot />
        </div>
      </div>
    </div>
  </div>
</template>
