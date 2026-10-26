// M6 网络诊断与容器相关的类型定义。
// 与 internal/model/diagnostics.go 一一对应。

export type ProbeOutcome = 'connected' | 'refused' | 'timeout' | 'unreachable' | 'error'

export interface TCPProbeResult {
  target: string
  host: string
  port: number
  reachable: boolean
  outcome: ProbeOutcome
  detail?: string
  latencyMs?: number
  probedAt: string
}

export interface Route {
  destination: string
  gateway: string
  flags: string
  interface: string
  expire?: string
}

export interface Listener {
  protocol: string
  address: string
  port: number
}

export interface DiagnosticsSnapshot {
  ipv6Routes: Route[] | null
  ipv6Listeners: Listener[] | null
  missing?: { metric: string; reason: string }[]
  collectedAt: string
}

export interface OrbStackStatus {
  available: boolean
  socketPath?: string
  version?: string
  reason?: string
}

export interface Container {
  id: string
  name: string
  image: string
  state: string
  status: string
  ports?: string[]
}

export interface ContainersOverview {
  status: OrbStackStatus
  items: Container[] | null
}

/**
 * 探测结论的展示信息。
 *
 * 关键点：refused 与 timeout 用不同的颜色与说明。
 * 前者是「服务没起来」，后者是「可能被防火墙丢弃」——
 * 排查方向完全不同，不能都显示成红色的「失败」。
 */
export const OUTCOME_META: Record<
  ProbeOutcome,
  { label: string; type: 'success' | 'error' | 'warning' | 'default'; hint: string }
> = {
  connected: {
    label: '连接成功',
    type: 'success',
    hint: '端口在监听，TCP 三次握手完成',
  },
  refused: {
    label: '连接被拒绝',
    type: 'error',
    hint: '目标端口没有服务在监听。检查服务是否启动、监听地址是否正确。',
  },
  timeout: {
    label: '连接超时',
    type: 'warning',
    hint: '没有收到任何响应。常见原因是防火墙丢弃了数据包（PF 规则），或目标主机不在线。',
  },
  unreachable: {
    label: '网络不可达',
    type: 'warning',
    hint: '没有到目标的路径。检查地址拼写、接口状态与路由表。',
  },
  error: {
    label: '检测失败',
    type: 'default',
    hint: '发生了非预期错误，详见说明。',
  },
}

/** 容器状态的中文标签 */
export const CONTAINER_STATE_META: Record<string, { label: string; type: 'success' | 'warning' | 'default' | 'error' }> = {
  running: { label: '运行中', type: 'success' },
  exited: { label: '已退出', type: 'default' },
  paused: { label: '已暂停', type: 'warning' },
  restarting: { label: '重启中', type: 'warning' },
  created: { label: '已创建', type: 'default' },
  dead: { label: '异常', type: 'error' },
}

/** 生成监听端口的去重展示（同一端口可能既 tcp 又 udp） */
export function groupListeners(list: Listener[]): { port: number; protocols: string[]; addresses: string[] }[] {
  const map = new Map<number, { protocols: Set<string>; addresses: Set<string> }>()
  for (const l of list) {
    const entry = map.get(l.port) ?? { protocols: new Set(), addresses: new Set() }
    entry.protocols.add(l.protocol)
    entry.addresses.add(l.address)
    map.set(l.port, entry)
  }
  return [...map.entries()]
    .map(([port, v]) => ({
      port,
      protocols: [...v.protocols].sort(),
      addresses: [...v.addresses].sort(),
    }))
    .sort((a, b) => a.port - b.port)
}
