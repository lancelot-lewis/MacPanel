// M4 启动项管理的类型定义。
// 与 internal/model/service.go 一一对应。

export type ServiceKind = 'system_critical' | 'self' | 'third_party'
export type ServiceDomain = 'system' | 'gui'
export type ServiceAction = 'start' | 'stop' | 'restart'

export interface ServiceItem {
  label: string
  displayName?: string
  domain: ServiceDomain
  kind: ServiceKind
  pid: number
  running: boolean
  plistPath?: string
  managed: boolean
  controllable: boolean
  blockedReason?: string
  lastExit?: string
}

export interface ServiceCounts {
  total: number
  systemCritical: number
  self: number
  thirdParty: number
  managed: number
  running: number
}

export interface ServicesOverview {
  items: ServiceItem[]
  summary: ServiceCounts
  warnings?: string[]
}

export interface ServicePlistInfo {
  label: string
  program?: string
  programArguments?: string[]
  runAtLoad: boolean
  keepAlive: boolean
  disabled: boolean
  standardOutPath?: string
  standardErrorPath?: string
  workingDirectory?: string
}

export interface ManagedService {
  id: number
  label: string
  domain: string
  plistPath: string
  displayName?: string
  allowControl: boolean
  createdAt: string
  updatedAt: string
}

export interface ServiceDetail {
  label: string
  domain: ServiceDomain
  plistPath?: string
  info?: ServicePlistInfo
  logPaths?: string[]
  managed?: ManagedService
  journalHint?: string
}

export interface ServiceActionResult {
  label: string
  action: string
  message?: string
}

/** 分类的中文标签与展示类型 */
export const KIND_META: Record<ServiceKind, { label: string; type: 'default' | 'info' | 'warning' }> = {
  system_critical: { label: '系统关键', type: 'default' },
  self: { label: '本应用', type: 'info' },
  third_party: { label: '第三方', type: 'warning' },
}

/** 操作的中文名 */
export const ACTION_LABELS: Record<ServiceAction, string> = {
  start: '启动',
  stop: '停止',
  restart: '重启',
}
