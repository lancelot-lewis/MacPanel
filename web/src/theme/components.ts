// Naive UI 组件按需注册。
//
// 为什么不用 app.use(naive)（全量注册）：
// 实测全量注册后产物 1584 kB(gzip 442 kB)，而实际只用了 27 个组件。
// 改为具名导入后 Vite 可 tree-shake 掉未使用的组件，总量降到 243 kB(gzip)。
//
// 关键陷阱（踩过一次）：naive-ui 组件的 .name 是 "Card" 而不是 "NCard"，
// 前缀 N 只存在于导出名上。因此不能用 c.name 作为注册名——那会注册成
// <Card>，而模板里写的是 <NCard>，结果是页面空白且不报错（Vue 只给 warn）。
// 这里显式给出 [注册名, 组件] 映射。
//
// 新增组件时必须在这里补一行；漏注册会在控制台看到
// "Failed to resolve component: Nxxx"，属于立即可见的错误。

import type { App, Component } from 'vue'
import {
  NAlert,
  NButton,
  NCard,
  NCheckbox,
  NCheckboxGroup,
  NDrawer,
  NDrawerContent,
  NPopconfirm,
  NConfigProvider,
  NDataTable,
  NDescriptions,
  NDescriptionsItem,
  NDialogProvider,
  NDivider,
  NDropdown,
  NEmpty,
  NForm,
  NFormItem,
  NIcon,
  NInput,
  NInputNumber,
  NLayout,
  NLayoutSider,
  NMenu,
  NMessageProvider,
  NProgress,
  NRadio,
  NRadioButton,
  NRadioGroup,
  NSelect,
  NSkeleton,
  NSpin,
  NSwitch,
  NTable,
  NTag,
  NTooltip,
} from 'naive-ui'

// 注册名 → 组件。按功能分组，便于新增时定位。
const registry: Record<string, Component> = {
  // 布局与容器
  NConfigProvider,
  NLayout,
  NLayoutSider,
  NCard,
  NDivider,
  NDrawer,
  NDrawerContent,

  // 导航
  NMenu,
  NDropdown,

  // 数据展示
  NTable,
  NDataTable,
  NDescriptions,
  NDescriptionsItem,
  NTag,
  NProgress,
  NSkeleton,
  NEmpty,
  NSpin,
  NTooltip,

  // 表单（M2 规则编辑会用到的先一并注册）
  NForm,
  NFormItem,
  NInput,
  NInputNumber,
  NSelect,
  NRadioGroup,
  NRadioButton,
  NRadio,
  NCheckbox,
  NCheckboxGroup,
  NSwitch,

  // 反馈
  NAlert,
  NButton,
  NIcon,
  NPopconfirm,
  NMessageProvider,
  NDialogProvider,
}

export function installNaive(app: App): void {
  for (const [name, component] of Object.entries(registry)) {
    app.component(name, component)
  }
}
