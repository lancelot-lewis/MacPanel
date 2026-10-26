// 格式化工具。所有函数对缺失值返回「—」而不是 0，
// 以免把「读不到」误呈现为「是 0」。

export function formatBytes(bytes?: number | null, digits = 1): string {
  if (bytes == null || Number.isNaN(bytes)) return '—'
  if (bytes < 0) return '—'
  const units = ['B', 'KiB', 'MiB', 'GiB', 'TiB', 'PiB']
  let value = bytes
  let unit = 0
  while (value >= 1024 && unit < units.length - 1) {
    value /= 1024
    unit++
  }
  const precision = unit === 0 ? 0 : digits
  return `${value.toFixed(precision)} ${units[unit]}`
}

export function formatPercent(ratio?: number | null, digits = 0): string {
  if (ratio == null || Number.isNaN(ratio)) return '—'
  return `${(ratio * 100).toFixed(digits)}%`
}

export function formatDuration(seconds?: number | null): string {
  if (seconds == null || Number.isNaN(seconds) || seconds < 0) return '—'
  const d = Math.floor(seconds / 86400)
  const h = Math.floor((seconds % 86400) / 3600)
  const m = Math.floor((seconds % 3600) / 60)
  if (d > 0) return `${d} 天 ${h} 小时`
  if (h > 0) return `${h} 小时 ${m} 分`
  if (m > 0) return `${m} 分`
  return `${Math.floor(seconds)} 秒`
}

export function formatTime(iso?: string | null): string {
  if (!iso) return '—'
  const d = new Date(iso)
  if (Number.isNaN(d.getTime())) return '—'
  // 本地面板优先显示本地时间，便于对照系统行为。
  return d.toLocaleString('zh-CN', { hour12: false })
}

export function formatClock(iso?: string | null): string {
  if (!iso) return '—'
  const d = new Date(iso)
  if (Number.isNaN(d.getTime())) return '—'
  return d.toLocaleTimeString('zh-CN', { hour12: false })
}

export function relativeTime(iso?: string | null): string {
  if (!iso) return '—'
  const d = new Date(iso).getTime()
  if (Number.isNaN(d)) return '—'
  const diff = Date.now() - d
  const abs = Math.abs(diff)
  const suffix = diff >= 0 ? '前' : '后'
  if (abs < 60_000) return `${Math.floor(abs / 1000)} 秒${suffix}`
  if (abs < 3_600_000) return `${Math.floor(abs / 60_000)} 分钟${suffix}`
  if (abs < 86_400_000) return `${Math.floor(abs / 3_600_000)} 小时${suffix}`
  return `${Math.floor(abs / 86_400_000)} 天${suffix}`
}

export function formatNumber(value?: number | null, digits = 2): string {
  if (value == null || Number.isNaN(value)) return '—'
  return value.toFixed(digits)
}

// 接口类型的中文标签，集中维护避免各页面各写一套。
export const kindLabels: Record<string, string> = {
  loopback: '回环',
  ethernet: '以太网',
  wifi: 'Wi-Fi',
  tunnel: '隧道',
  bridge: '网桥',
  virtual: '虚拟',
  other: '其它',
}

export const scopeLabels: Record<string, string> = {
  loopback: '回环',
  'link-local': '链路本地',
  private: '私有',
  global: '公网',
}

export const anchorStateLabels: Record<string, string> = {
  unknown: '未知',
  absent: '未创建',
  present: '文件存在',
  loaded: '已加载',
}

export const managedStateLabels: Record<string, string> = {
  not_configured: '未配置',
  configured: '已配置',
  drift: '与期望不一致',
}
