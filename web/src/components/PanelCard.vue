<script setup lang="ts">
// 内容卡片：基于 NCard 封装。
//
// 相比裸 NCard，这里统一：标题字号、header 与内容的间距、右上角操作区。
// 用 NCard 而不是手写 div 的好处是：拿到 Naive UI 的边框、圆角、
// 深色模式适配与 #header-extra 插槽，且后续换皮肤时只需改主题变量。

withDefaults(
  defineProps<{
    title?: string
    subtitle?: string
    /** false 时内容区不加内边距，用于表格、列表这类需要贴边的场景 */
    padded?: boolean
  }>(),
  { title: '', subtitle: '', padded: true },
)
</script>

<template>
  <NCard size="small" :bordered="true" class="hlm-card">
    <template v-if="title || subtitle || $slots.actions" #header>
      <div class="min-w-0">
        <div class="text-[13px] font-semibold" :style="{ color: 'var(--color-text-primary)' }">
          {{ title }}
        </div>
        <div
          v-if="subtitle"
          class="mt-0.5 text-[11.5px] font-normal"
          :style="{ color: 'var(--color-text-tertiary)' }"
        >
          {{ subtitle }}
        </div>
      </div>
    </template>
    <template v-if="$slots.actions" #header-extra>
      <slot name="actions" />
    </template>

    <div :class="padded ? '' : '-mx-4 -my-3'">
      <slot />
    </div>
  </NCard>
</template>

<style scoped>
/* NCard 默认 header 上下留白偏大，收紧一档让信息密度更高 */
.hlm-card :deep(.n-card-header) {
  padding: 12px 16px;
}
.hlm-card :deep(.n-card__content) {
  padding: 14px 16px;
  font-size: 13px;
}
</style>
