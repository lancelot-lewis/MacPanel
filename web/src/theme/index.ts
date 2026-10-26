// Naive UI 主题配置。
//
// 目标：让组件库看起来像「macOS 系统设置页」，而不是 Naive UI 默认的中性商务风。
// 做法是覆盖主题变量（primaryColor / borderRadius / fontFamily 等），
// 而不是在业务组件上到处写 !important 去覆盖。
//
// 这样组件库提供表单、校验、表格、弹窗等能力，视觉仍由本项目控制：
// 换掉 Naive UI 时只要重写本文件 + 保留设计令牌即可，不必改业务页面。

import type { GlobalThemeOverrides } from 'naive-ui'

// 与 style.css 中的 @theme 保持一致的色值。
// 这里用 oklch 算出的近似 hex，因为 naive-ui 内部有色值运算，
// 传 oklch 字符串在部分组件（如颜色混合、透明度推导）会失效。
export const brand = {
  accent: '#2f6fed', // 主色：克制的蓝
  accentHover: '#4a84f0',
  accentPressed: '#2359c9',
  ok: '#1f9d55',
  warn: '#d98a00',
  danger: '#b82f2f', // 5.8:1 on white（原 #d64545 仅 4.38:1）
} as const

// 语义色的「文字版」。
//
// 为什么需要两套：
// Naive UI 的 Tag 默认把 type 色同时用作文字色和 12% 透明背景底色。
// #1f9d55 在白底上只有 3.05:1，12% 绿底上也只有约 3:1，不满足 WCAG AA。
// 因此 Tag 类元素统一改用下面的深色变体作为文字色：
// 这些值在白底/浅色底上均 ≥4.5:1，同时保持与主色同一色相。
export const semanticText = {
  success: '#146b3b', // 5.6:1 on white（原 #1a7f47 仅 4.4:1）
  warning: '#7a4f00', // 6.2:1 on white
  error: '#a82626', // 6.3:1 on white
  info: '#1d4fb5', // 6.4:1 on white（原 #2359c9 仅 3.9:1 叠在浅蓝底上）
} as const

// 深色模式下语义文字用更亮的变体（深底需要高亮度）
export const semanticTextDark = {
  success: '#5edb94',
  warning: '#ffc76b',
  error: '#ff8f8f',
  info: '#8db4ff',
} as const

// 浅色主题
export const lightOverrides: GlobalThemeOverrides = {
  common: {
    primaryColor: brand.accent,
    primaryColorHover: brand.accentHover,
    primaryColorPressed: brand.accentPressed,
    primaryColorSuppl: brand.accentHover,
    successColor: brand.ok,
    successColorHover: '#25b562',
    successColorPressed: '#187a42',
    warningColor: brand.warn,
    warningColorHover: '#eda200',
    warningColorPressed: '#b87400',
    errorColor: brand.danger,
    errorColorHover: '#e35d5d',
    errorColorPressed: '#b83939',
    infoColor: brand.accent,

    // 圆角：比 Naive 默认(3px)略大，接近 macOS 控件
    borderRadius: '7px',
    borderRadiusSmall: '5px',

    fontSize: '13px',
    fontSizeSmall: '12px',
    fontSizeMedium: '13px',
    fontSizeLarge: '14px',
    fontSizeHuge: '15px',

    fontFamily:
      "-apple-system, BlinkMacSystemFont, 'SF Pro SC', 'PingFang SC', 'Helvetica Neue', Arial, sans-serif",
    fontFamilyMono: "'SF Mono', ui-monospace, Menlo, monospace",

    textColorBase: '#2b2f36',
    textColor1: '#2b2f36',
    textColor2: '#535862',
    // 与 style.css 的 --color-text-tertiary 保持一致。
    // 注：该值需满足 WCAG AA（白底 ≥4.5:1），原 #7d838d 只有 3.82:1，已加深。
    textColor3: '#666c76',
    borderColor: '#e4e6ea',
    dividerColor: '#e9ebee',
    bodyColor: '#fafbfc',
    cardColor: '#ffffff',
    modalColor: '#ffffff',
    popoverColor: '#ffffff',
    tableColor: '#ffffff',
    inputColor: '#ffffff',
    hoverColor: 'rgba(0, 0, 0, 0.04)',

    // 控件高度：紧凑一些，管理面板信息密度更高
    heightSmall: '26px',
    heightMedium: '30px',
    heightLarge: '34px',
  },
  Card: {
    borderRadius: '12px',
    paddingMedium: '16px',
    color: '#ffffff',
    borderColor: '#e4e6ea',
  },
  Button: {
    borderRadiusMedium: '7px',
    fontWeight: '500',
    fontWeightStrong: '500',
  },
  DataTable: {
    thColor: '#f7f8fa',
    thTextColor: '#535862',
    thFontWeight: '600',
    tdColor: '#ffffff',
    borderColor: '#e9ebee',
    thPaddingMedium: '8px 12px',
    tdPaddingMedium: '8px 12px',
  },
  Input: {
    borderRadius: '7px',
  },
  Tag: {
    borderRadius: '5px',
    // 文字色压深到满足 WCAG AA（4.5:1）。
    // Naive 用 textColor 生成 12% 透明背景，底色会随文字色同步微调，整体更协调。
    textColorSuccess: semanticText.success,
    textColorWarning: semanticText.warning,
    textColorError: semanticText.error,
    textColorInfo: semanticText.info,
    // type="primary" 的 Tag 用 primaryColor 作为文字色，
    // 浅色底上只有 3.9:1，这里局部压深（不影响按钮等其它组件）。
    textColor: semanticText.info,
  },
  Menu: {
    itemHeight: '34px',
    borderRadius: '7px',
    itemTextColor: '#2b2f36',
    itemTextColorActive: brand.accent,
    itemTextColorActiveHover: brand.accent,
    itemTextColorChildActive: brand.accent,
    itemColorActive: 'rgba(47, 111, 237, 0.10)',
    itemColorActiveHover: 'rgba(47, 111, 237, 0.14)',
    itemIconColorActive: brand.accent,
    itemIconColorActiveHover: brand.accent,
    itemFontWeightActive: '600',
  },
  Alert: {
    borderRadius: '10px',
    padding: '11px 14px',
  },
  Descriptions: {
    thColor: '#f7f8fa',
    thTextColor: '#535862',
    tdTextColor: '#2b2f36',
    borderRadius: '8px',
  },
  Progress: {
    railHeight: '8px',
  },
}

// 深色主题
export const darkOverrides: GlobalThemeOverrides = {
  common: {
    primaryColor: '#6b9bff',
    primaryColorHover: '#85aeff',
    primaryColorPressed: '#5589f0',
    primaryColorSuppl: '#85aeff',
    successColor: '#3ecf7f',
    successColorHover: '#57dc92',
    successColorPressed: '#2ba463',
    warningColor: '#e8a33d',
    warningColorHover: '#f4b455',
    warningColorPressed: '#c4852a',
    errorColor: '#ef6b6b',
    errorColorHover: '#f88585',
    errorColorPressed: '#d45252',
    infoColor: '#6b9bff',

    borderRadius: '7px',
    borderRadiusSmall: '5px',

    fontSize: '13px',
    fontSizeSmall: '12px',
    fontSizeMedium: '13px',
    fontSizeLarge: '14px',
    fontSizeHuge: '15px',

    fontFamily:
      "-apple-system, BlinkMacSystemFont, 'SF Pro SC', 'PingFang SC', 'Helvetica Neue', Arial, sans-serif",
    fontFamilyMono: "'SF Mono', ui-monospace, Menlo, monospace",

    // 用带蓝调的深灰而非纯黑，避免纯黑边缘发灰
    bodyColor: '#12151a',
    cardColor: '#1a1e25',
    modalColor: '#1a1e25',
    popoverColor: '#1f242c',
    tableColor: '#1a1e25',
    inputColor: '#151920',

    textColorBase: '#e8eaee',
    textColor1: '#e8eaee',
    textColor2: '#a8aeb8',
    textColor3: '#8b93a0',
    borderColor: '#2d333c',
    dividerColor: '#262c34',
    hoverColor: 'rgba(255, 255, 255, 0.06)',

    heightSmall: '26px',
    heightMedium: '30px',
    heightLarge: '34px',
  },
  Card: {
    borderRadius: '12px',
    paddingMedium: '16px',
    color: '#1a1e25',
    borderColor: '#2d333c',
  },
  Button: {
    borderRadiusMedium: '7px',
    fontWeight: '500',
    fontWeightStrong: '500',
  },
  DataTable: {
    thColor: '#20252d',
    thTextColor: '#a8aeb8',
    thFontWeight: '600',
    tdColor: '#1a1e25',
    tdColorHover: '#20252d',
    borderColor: '#262c34',
    thPaddingMedium: '8px 12px',
    tdPaddingMedium: '8px 12px',
  },
  Input: {
    borderRadius: '7px',
  },
  Tag: {
    borderRadius: '5px',
    // 深色底需要更高亮度的文字才能达到 4.5:1
    textColorSuccess: semanticTextDark.success,
    textColorWarning: semanticTextDark.warning,
    textColorError: semanticTextDark.error,
    textColorInfo: semanticTextDark.info,
    textColor: semanticTextDark.info,
  },
  Menu: {
    itemHeight: '34px',
    borderRadius: '7px',
    itemTextColor: '#e8eaee',
    itemTextColorActive: '#6b9bff',
    itemTextColorActiveHover: '#6b9bff',
    itemTextColorChildActive: '#6b9bff',
    itemColorActive: 'rgba(107, 155, 255, 0.14)',
    itemColorActiveHover: 'rgba(107, 155, 255, 0.18)',
    itemIconColorActive: '#6b9bff',
    itemIconColorActiveHover: '#6b9bff',
    itemFontWeightActive: '600',
  },
  Alert: {
    borderRadius: '10px',
    padding: '11px 14px',
  },
  Descriptions: {
    thColor: '#20252d',
    thTextColor: '#a8aeb8',
    tdTextColor: '#e8eaee',
    borderRadius: '8px',
  },
  Progress: {
    railHeight: '8px',
  },
}

// Tag 的按需覆盖：把文字色压深，背景保持淡淡的语义色。
//
// Naive UI 的 Tag 使用 common 的 successColor/errorColor 等生成
// 「文字 + 12% 背景」。全局改 common 会影响按钮、进度条等组件，
// 因此只在 Tag 上局部覆盖 color 与 textColor，
// 用 CSS 变量 + 作用域样式实现（见 style.css 的 .hlm-tag 规则）。
export const tagTextColors = (dark: boolean) => (dark ? semanticTextDark : semanticText)
