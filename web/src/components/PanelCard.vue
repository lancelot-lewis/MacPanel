<script setup lang="ts">
// 通用卡片容器。
withDefaults(defineProps<{ title?: string; subtitle?: string; padded?: boolean }>(), {
  title: '',
  subtitle: '',
  padded: true,
})
</script>

<template>
  <section class="card overflow-hidden">
    <header
      v-if="title || subtitle || $slots.actions"
      class="flex items-start justify-between gap-3 border-b border-[var(--color-border-subtle)] px-4 py-3"
    >
      <div class="min-w-0">
        <h2 v-if="title" class="card-title m-0">{{ title }}</h2>
        <p v-if="subtitle" class="mt-1 mb-0 text-xs text-[var(--color-text-secondary)]">
          {{ subtitle }}
        </p>
      </div>
      <div v-if="$slots.actions" class="shrink-0"><slot name="actions" /></div>
    </header>
    <div :class="padded ? 'p-4' : ''">
      <slot />
    </div>
  </section>
</template>
