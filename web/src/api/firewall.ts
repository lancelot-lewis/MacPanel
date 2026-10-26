// M2 防火墙规则相关的类型定义。
// 与 internal/model/firewall_rule.go 一一对应。

export type RuleAction = 'pass' | 'block'
export type RuleDirection = 'in'
export type RuleProtocol = 'tcp' | 'udp' | 'icmp6' | 'any'

export interface FirewallRule {
  id: number
  name: string
  description: string
  position: number
  action: RuleAction
  direction: RuleDirection
  protocol: RuleProtocol
  portFrom: number | null
  portTo: number | null
  sourceCidr: string
  interface: string
  enabled: boolean
  builtinTemplate?: string
}

export interface RuleSet {
  rules: FirewallRule[]
  defaultDenyInterfaces: string[]
}

/** 事务阶段，与 firewalld.Phase 对应 */
export type TransactionPhase =
  | 'idle'
  | 'pending'
  | 'confirmed'
  | 'rolled_back'
  | 'failed'

export interface TransactionState {
  phase: TransactionPhase
  revisionId: number
  contentHash: string
  appliedAt?: string
  deadline?: string
  previousHash?: string
  reason?: string
  updatedAt?: string
}

/** 生成器强制添加的规则 */
export interface EnforcedRule {
  key: string
  reason: string
  rule: string
}

/** 被跳过的规则 */
export interface SkippedRule {
  ruleId: number
  name: string
  reason: string
}

/** 不变量违反 */
export interface Violation {
  invariant?: number
  key: string
  message: string
  line?: number
}

export interface Verification {
  ok: boolean
  syntaxErrors: Violation[] | null
  invariantViolations: Violation[] | null
  stats: {
    totalLines: number
    ruleLines: number
    passRules: number
    blockRules: number
    inet6Rules: number
    nonInet6Rules: number
    commentLines: number
  }
}

export interface PreviewResult {
  content: string
  ruleCount: number
  enforced: EnforcedRule[] | null
  skipped: SkippedRule[] | null
  verification: Verification
}

export interface RulesGetResult extends RuleSet {
  transaction: TransactionState
}

export interface ApplyResult {
  revisionId: number
  contentHash: string
  appliedAt: string
  deadline: string
  confirmTimeoutSeconds: number
  warnings?: string[] | null
  verification: Verification
}

/** 内置模板 */
export interface FirewallTemplate {
  key: string
  displayName: string
  description: string
  note?: string
  rules: FirewallRule[]
}
