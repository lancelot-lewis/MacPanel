// 与后端 model 包一一对应的类型定义。
// 修改后端结构时同步更新本文件；字段名必须与 JSON tag 完全一致。

export type MetricState = 'ok' | 'unavailable'

export interface MissingMetric {
  metric: string
  reason: string
}

export interface LoadAvg {
  one: number
  five: number
  fifteen: number
}

export interface OSInfo {
  productVersion: string
  build: string
  kernel: string
  architecture: string
}

export interface CPUInfo {
  model: string
  physicalCores: number
  logicalCores: number
  loadAverage?: LoadAvg
  usagePercent?: number
}

export interface MemoryInfo {
  totalBytes: number
  usedBytes?: number
  freeBytes?: number
  availableBytes?: number
  state: MetricState
}

export interface DiskUsage {
  mountPoint: string
  fileSystem: string
  totalBytes: number
  freeBytes: number
  usedBytes: number
  usedRatio: number
}

export interface SystemOverview {
  hostname: string
  os: OSInfo
  cpu: CPUInfo
  memory: MemoryInfo
  disks: DiskUsage[] | null
  bootTime?: string
  uptimeSeconds?: number
  collectedAt: string
  missing?: MissingMetric[]
}

export type InterfaceKind =
  | 'loopback'
  | 'ethernet'
  | 'wifi'
  | 'tunnel'
  | 'bridge'
  | 'virtual'
  | 'other'

export interface NetAddress {
  address: string
  prefixLen: number
  family: 'ipv4' | 'ipv6'
  scope: string
  isLoopback?: boolean
  isLinkLocal?: boolean
  isPrivate?: boolean
}

export interface NetworkInterface {
  name: string
  index: number
  kind: InterfaceKind
  up: boolean
  loopback: boolean
  mtu: number
  hardwareAddr?: string
  flags: string[] | null
  ipv4: NetAddress[] | null
  ipv6: NetAddress[] | null
}

export interface NetworkSnapshot {
  interfaces: NetworkInterface[] | null
  hasIPv6: boolean
  hasGlobalIPv6: boolean
  collectedAt: string
  missing?: MissingMetric[]
}

export type AnchorState = 'unknown' | 'absent' | 'present' | 'loaded'
export type ManagedState = 'not_configured' | 'configured' | 'drift'

export interface PFInfo {
  enabled?: boolean
  enforceState?: boolean
  state?: string
  detail?: string
  readAvailable: boolean
  readError?: string
}

export interface AnchorInfo {
  name: string
  rulePath: string
  state: AnchorState
  present: boolean
  referenced: boolean
  sizeBytes?: number
  lastModified?: string
  notes?: string[] | null
}

export interface FirewallStatus {
  managed: ManagedState
  pf: PFInfo
  anchor: AnchorInfo
  loadedAnchors?: string[] | null
  warnings?: string[] | null
  collectedAt: string
  writable: boolean
}

export interface AgentHealth {
  version: string
  startedAt: string
  pid: number
  platform: string
  isRoot: boolean
  methodCount: number
  serverClock: string
}

export interface AuthStatus {
  mode: 'local' | 'remote'
  authenticated: boolean
  passwordSet: boolean
  requiresPasswordSetup: boolean
  session?: {
    id: string
    createdAt: string
    lastSeenAt: string
    expiresAt: string
    remoteAddr: string
  }
}

export interface FeatureFlags {
  firewallRead: boolean
  firewallWrite: boolean
  powerControl: boolean
  serviceControl: boolean
  scheduleControl: boolean
}

export interface MetaInfo {
  server: {
    version: string
    commit: string
    buildTime: string
    goVersion: string
    platform: string
  }
  agent: AgentHealth
  agentReachable: boolean
  access: {
    mode: 'local' | 'remote'
    listenAddr: string
    allowRemote: boolean
  }
  features: FeatureFlags
}

export interface AuditEntry {
  id: number
  at: string
  actor: string
  action: string
  target?: string
  result: 'success' | 'failure' | 'denied'
  remoteAddr?: string
  requestId?: string
  detail?: Record<string, unknown>
}

export interface ApiError {
  code: string
  message: string
  details?: Record<string, unknown>
}

export interface EnvelopeMeta {
  requestId?: string
  degraded?: boolean
  notes?: string[]
}

export interface Envelope<T> {
  ok: boolean
  data?: T
  error?: ApiError
  meta?: EnvelopeMeta
}
