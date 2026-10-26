# 测试与验收矩阵

> 对应里程碑：M0 / M1
> 最后更新：2026-10-26

## 1. 运行方式

```bash
# 全部单元与集成测试
make test

# 竞态检测（IPC 并发路径必须跑）
make test-race

# 覆盖率
make test-cover

# 提交前完整检查（fmt + vet + test + 前端类型检查）
make check
```

## 2. 自动化测试清单

### 2.1 IPC 与对端身份验证（`internal/ipc`，覆盖率 100%）

| 测试 | 验证内容 |
| --- | --- |
| `TestCallSuccess` | 正常调用链路：请求 → handler → 响应 |
| `TestUnknownMethodRejected` | 未注册方法返回 `not_found`（不暴露「方法是否存在」差异） |
| `TestWrongTokenRejected` | 错误令牌返回 `unauthorized` |
| `TestHandlerErrorPropagated` | 业务错误码透传；内部错误细节不泄漏 |
| `TestFrameTooLargeRejected` | 超长帧被拒绝，且不按声明长度预分配内存 |
| `TestReplayRejected` | 重复请求 ID 被拒绝 |
| `TestStaleTimestampRejected` | 超窗时间戳被拒绝 |
| `TestSocketPermissions` | Socket 0660、目录 0750 |
| `TestStaleSocketRefusedWhenLive` | 已有实例监听时拒绝重复启动 |
| `TestNonSocketPathNotRemoved` | 非 Socket 文件不被删除，内容不变 |
| `TestDecodeStrictRejectsUnknownFields` | 未知字段被拒绝 |
| `TestSocketPathTooLongRejected` | 超长 Socket 路径提前拒绝并给出可读错误 |
| `TestValidateSocketPathBounds` | 路径长度边界（103 通过 / 104 拒绝） |

### 2.1.1 由实机验收发现的三个缺陷（已修复，均有回归测试）

| 缺陷 | 根因 | 修复 | 回归测试 |
| --- | --- | --- | --- |
| 远程模式下无法设置初始密码 | `/auth/password` 在认证中间件外，`sessionFrom` 永远为空；登录又需要密码 → 死锁 | 按「是否已设密码」分流：未设置时只允许回环，已设置时才要求会话 | `TestInitialPasswordOnlyFromLocalhost` |
| 远程模式已登录也无法改密码 | 同根：handler 内拿不到会话 | 新增 `hasValidSession()` 手动校验 cookie | `TestChangePasswordInRemoteMode` |
| 测试报 `bind: invalid argument` | Socket 路径超 `sun_path` 104 字节上限 | 生产代码加 `validateSocketPath` 提前校验；测试改用短路径 | `TestSocketPathTooLongRejected` |

### 2.1.2 M2 防火墙：规则生成与校验（`internal/platform/darwin/pfrules`，19 个测试）

| 测试 | 验证内容 |
| --- | --- |
| `TestGenerateContainsMandatoryRules` | 4 类强制放行规则必定存在，且强制清单完整上报 |
| `TestGenerateNeverEmitsGlobalDirectives` | 绝不产出 `set`/`scrub`/`nat`/`rdr`/`include`/`load anchor` |
| `TestAllRulesAreInet6` | 每条 pass/block 都带 `inet6`（不变量 1） |
| `TestVerifyPassesOnGeneratedContent` | 4 种规则集自生成自校验均通过 |
| `TestVerifyDetectsForbiddenDirectives` | 5 类全局指令被识别 |
| `TestVerifyDetectsMissingLoopback` | 缺 lo0 放行 → 关联不变量 2 |
| `TestVerifyDetectsMissingICMPv6` | 缺 ICMPv6 → 关联不变量 4 |
| `TestVerifyDetectsNonInet6Rule` | 未限定 inet6 → 关联不变量 1 |
| `TestGenerateRejectsDangerousDefaultDeny` | `lo0`/`utun*`/`ipsec*`/`gif*` 不能用于默认拒绝 |
| `TestGenerateReportsSkippedRules` | 禁用规则被记录而非静默丢弃 |
| `TestCommentInjectionPrevented` | **规则名含换行时无法注入额外 PF 规则** |
| `TestPortRangeRendering` | 端口范围用 PF 的 `a:b` 语法 |
| `TestDefaultDenyOnlyWhenConfigured` | 未配置接口时不生成 block，并明确说明 |
| `TestGenerateValidatesInput` | 4 类非法输入在生成前被拒绝 |
| `TestTemplatesAreValid` | 5 个模板生成的规则都能通过校验 |
| `TestTemplatesHaveNote` | 每个模板都带「必须核实端口」的提示 |
| `TestTemplatesAvoidUnintendedWideRanges` | **模板不会意外放行大段端口** |
| `TestCaddyTemplateUsesExactPorts` | Caddy 必须是 80/443 两个单端口 |
| `TestFindTemplate` | 按 key 查找 |

### 2.1.3 M2 防火墙：事务与回滚（`internal/platform/darwin/firewalld`，17 个测试）

| 测试 | 验证内容 |
| --- | --- |
| `TestApplyEntersPending` | 应用后进入 pending，deadline = now + 超时 |
| `TestApplyAbortsOnSyntaxError` | **语法失败时不加载、不写 pending** |
| `TestConfirmMakesRulePermanent` | 确认后清除 deadline，不再自动回滚 |
| `TestConfirmIsIdempotent` | 重复确认返回 200 |
| `TestConfirmRejectedAfterDeadline` | **超时后确认被拒绝并触发回滚** |
| `TestPurgeExpiredRollsBack` | 59 秒不回滚、61 秒回滚 |
| `TestRollbackIsIdempotent` | 重复回滚不报错 |
| `TestRecoverRollsBackExpiredOnStartup` | **崩溃重启后回滚超时配置** |
| `TestRecoverKeepsUnexpiredDeadline` | **重启不重置倒计时**（关键） |
| `TestApplyRollsBackPendingFirst` | 应用新规则前先回滚未确认的旧配置 |
| `TestApplyRollsBackOnLoadFailure` | 加载失败自动回滚 |
| `TestApplyRejectsEmptyContent` | 空内容被拒绝 |
| `TestConfirmRejectedWhenIdle` / `TestRollbackRejectedWhenIdle` | 无事务时报 409 |
| `TestStatePersistedAcrossInstances` | 状态真正落盘，换实例仍可读 |
| `TestNewValidatesOptions` | 5 类无效构造参数 |
| `TestHashContentStable` | 哈希稳定且能区分大小差异 |

### 2.1.4 M2：pfctl 参数白名单（`internal/platform/execx`，+5 个测试）

| 测试 | 验证内容 |
| --- | --- |
| `TestPFctlGuardRejectsDangerousFlags` | `-d`/`-e`/`-F`/`-k`/`-x`/`-z`/`-m` 全拒绝，**含混在合法参数中的情况** |
| `TestPFctlGuardAllowsExpectedCalls` | 6 种合法调用全部通过 |
| `TestPFctlGuardRejectsUnknownCombinations` | 6 种非法组合被拒绝 |
| `TestRunEnforcesArgGuard` | `Run` 确实调用参数校验 |
| `TestArgGuardScopeLimited` | 只有 pfctl 有参数 guard（不误伤只读命令） |

### 2.1.5 M2：规则持久化（`internal/firewallstore`，8 个测试）

| 测试 | 验证内容 |
| --- | --- |
| `TestSaveAndLoadRoundTrip` | 全字段往返一致 |
| `TestSaveReplacesExisting` | 整体替换，旧接口配置被清空 |
| `TestSaveRejectsInvalid` | 非法规则集不写入 |
| `TestSaveIsAtomic` | **失败时原有数据完好** |
| `TestSaveBackfillsIDs` | 保存后返回数据库分配的 ID（曾漏过） |
| `TestEmptyRuleSet` | 空规则集可读写 |
| `TestRevisionLifecycle` | 候选 → 已应用 → 已回滚，时间戳与原因保留 |
| `TestLastKnownGood` | 「上一份可用配置」查找与排除逻辑 |

### 2.2 配置与令牌（`internal/config`）

| 测试 | 验证内容 |
| --- | --- |
| `TestDefaultsAreSafe` | 默认只监听回环、禁止远程、路径全为绝对路径 |
| `TestValidateRejectsRemoteWithoutOptIn` | 5 种非回环地址在未开启远程时全部拒绝 |
| `TestValidateAllowsRemoteWhenOptedIn` | 显式开启后可监听所有网卡 |
| `TestValidateListenAddrFormats` | 缺端口 / 非数字端口 / 越界端口 / 域名均拒绝 |
| `TestValidateRejectsBadMode` | 未知运行模式被拒绝 |
| `TestValidateRejectsRelativePaths` | 相对路径被拒绝 |
| `TestLoadFromEnv` | 环境变量覆盖与 UID 列表解析 |
| `TestLoadFromEnvRejectsBadBool` | 非法布尔值报错（不静默降级） |
| `TestTokenRoundTrip` | 令牌生成 → 写入 → 读取 → 比较；文件 0600 |
| `TestTokenMismatch` | 随机令牌互不相等 |
| `TestParseTokenRejectsWeak` | 空 / 短 / 非十六进制 / 长度不符全部拒绝 |
| `TestIPCTimeoutBounds` | 超时 0 与 >1 分钟被拒绝 |
| `TestNormalizedListenAddr` | localhost → 127.0.0.1，IPv6 回环保留 |
| `TestIsLoopbackListen` | 回环判定 5 个用例 |

### 2.3 数据库与迁移（`internal/database`，覆盖率 80%）

| 测试 | 验证内容 |
| --- | --- |
| `TestMigrateCreatesSchema` | 11 张表全部创建，版本为 1 |
| `TestMigrateIdempotent` | 重复打开不重复执行迁移，记录数等于版本号 |
| `TestWALEnabled` | `journal_mode=wal`、`foreign_keys=1` |
| `TestFirewallRulesConstraints` | 端口越界、倒置范围、非法 action/direction 被约束拒绝 |
| `TestForeignKeyCascade` | 删除任务级联删除执行记录 |
| `TestTxRollback` | 事务失败后数据回滚 |
| `TestAuditLogsResultConstraint` | 非法 result 被约束拒绝 |

### 2.4 认证与会话（`internal/auth`）

| 测试 | 验证内容 |
| --- | --- |
| `TestPasswordTooShort` | 短密码被拒绝并提示长度要求 |
| `TestSetAndVerifyPassword` | 设置 → 校验 → 错误密码拒绝 |
| `TestVerifyWithoutPassword` | 未设密码返回 `ErrNoPassword` |
| `TestPasswordHashNotPlaintext` | DB 中无明文，格式为 `scrypt$…` |
| `TestRateLimitAfterFailures` | 5 次失败后限速；其它地址不受影响 |
| `TestSuccessfulLoginResetsFailures` | 成功登录清零失败计数 |
| `TestSessionLifecycle` | 创建 → 校验 → 撤销 → 失效 |
| `TestValidateSessionRejectsGarbage` | 空 / 随机 / 超长令牌被拒绝 |
| `TestChangePasswordRevokesSessions` | 改密码撤销旧会话 |
| `TestSessionTokenNotStoredPlaintext` | 只存 SHA-256 |
| `TestSessionIdleTimeout` | 空闲超时判定失效 |
| `TestVerifyHashRejectsTamperedParams` | 篡改 scrypt 参数被拒绝（防 DoS） |
| `TestPurgeExpired` | 过期会话被清理 |

### 2.5 HTTP API 与中间件（`internal/server`）

| 测试 | 验证内容 |
| --- | --- |
| `TestHealthzPublic` | 无需认证，且不含敏感字段 |
| `TestOverviewLocalMode` | 本机请求可读 |
| `TestOverviewRejectsNonLocalInLocalMode` | 本地模式外部地址 → 403 |
| `TestRemoteModeRequiresAuth` | 远程未登录 → 401 |
| `TestRemoteModeRequiresCustomHeader` | 缺 `X-HLM-Request` → 403 |
| `TestRemoteModeWithSession` | 已登录 + 自定义头 → 200 |
| `TestHostGuardRejectsForeignHost` | 非法 Host → 421 |
| `TestOriginGuardRejectsCrossSite` | 跨站 Origin 写请求 → 403 |
| `TestOriginGuardAllowsSameOrigin` | 同源 → 200 |
| `TestNotImplementedReturns501` | 4 个未实现接口均返回 501 且 `data` 为空 |
| `TestFirewallStatusDegraded` | PF 不可读时 `meta.degraded=true` + notes |
| `TestFirewallStatusNotWritable` | `writable` 必须为 false |
| `TestAgentErrorReturns503` | Agent 不可用 → 503，不返回伪造数据 |
| `TestDecodeJSONRejectsUnknownField` | 未知字段 → 400 |
| `TestPasswordTooShortViaAPI` | 短密码 → 400 |
| `TestSetPasswordRequiresCurrentPassword` | 缺当前密码 → 400；错误 → 401；正确 → 200 |
| `TestInitialPasswordOnlyFromLocalhost` | 初始密码只能本机设置（打破远程模式死锁） |
| `TestChangePasswordInRemoteMode` | 远程模式已登录取可改密码（回归缺陷 2） |
| `TestChangePasswordRemoteWithoutSessionRejected` | 未登录 / 伪造会话改密码 → 401 |
| `TestLoginDisabledInLocalMode` | 本地模式登录 → 501 |
| `TestLoginRateLimited` | 连续失败 → 429 |
| `TestAuthStatusReportsMode` | local / remote 模式正确上报 |
| `TestAuditListLimitValidation` | 4 种非法 limit → 400 |
| `TestApplyRequiresRiskConfirmation` | 应用必须带 `confirmsRisk`，否则 400 且不触达 Agent |
| `TestApplyAcceptsConfirmationHeader` | 可用 `X-HLM-Confirm-Risk` 头确认 |
| `TestApplyRejectsFalseConfirmation` | `confirmsRisk=false` 不视为确认 |
| `TestApplyDeniedIsAudited` | 被拒绝的尝试写入审计（denied） |
| `TestApplyFailureIsAudited` | 应用失败写入审计（failure） |
| `TestRulesSaveIsAudited` | 保存规则留审计 |
| `TestConfirmAndRollbackAreAudited` | 确认与回滚留审计 |
| `TestRollbackPassesReasonToAgent` | 回滚原因传递到 Agent |
| `TestFirewallWriteRequiresAuth` | 未登录写请求 401 且不触达 Agent |
| `TestAgentNotImplementedMapsTo501` | Agent 未实现 → 501，不包装成 500 |
| `TestSecurityHeaders` | 6 项安全头 + X-Request-Id + no-store |
| `TestMetaReportsFeatureFlags` | 写能力全部 false，读能力 true |
| `TestAuditRecorded` | 读接口写入审计 |
| `TestAuditDoesNotLogSecrets` | 审计不含密码 |
| `TestMethodNotAllowedOnReadEndpoint` | 只读端点拒绝 POST |

### 2.6 Agent 方法注册（`internal/agent`）

| 测试 | 验证内容 |
| --- | --- |
| `TestHealth` | 版本、方法数、PID |
| `TestMethodsAreWhitelisted` | 方法集合精确匹配；显式确认 15 个危险方法名不存在 |
| `TestSystemOverview` / `TestFirewallStatus` | 数据透传；`writable=false` |
| `TestCollectorErrorBecomesUnavailable` | 采集失败 → `unavailable` |
| `TestGenericErrorNotLeaked` | 普通 error → `unavailable` 而非 `internal` |
| `TestRequireNoParams` | 7 种参数形态（含注入尝试）的接受/拒绝 |
| `TestNewRequiresCollector` | 缺少采集器报错 |

### 2.7 命令执行（`internal/platform/execx`）

| 测试 | 验证内容 |
| --- | --- |
| `TestRejectsUnlistedBinary` | 8 个白名单外命令（含 `sh`、`./evil.sh`）全部拒绝 |
| `TestAllowedBinaryRuns` | 白名单命令可执行 |
| `TestNonZeroExitReturnsError` | 非零退出 → `CommandError` 带退出码 |
| `TestTimeout` | context 取消生效 |
| `TestAllowedBinariesSnapshot` | 快照为副本，修改不影响内部状态 |
| `TestLimitedWriterTruncates` | 输出截断仍报告全部写入（避免 short write） |

### 2.8 macOS 采集器（`internal/platform/darwin`，实机运行）

| 测试 | 验证内容 |
| --- | --- |
| `TestSystemOverviewRealReads` | 主机名 / 版本 / 核心数 / 内存 / 磁盘 / 启动时间均为真实值且合理 |
| `TestNetworkSnapshotRealReads` | 至少一个接口；回环被正确识别；地址族标记正确 |
| `TestFirewallStatusWithoutRoot` | **非 root 时 `pf.enabled` 必须为 `nil`**，状态 `unknown`，有原因与警告 |
| `TestAnchorAbsent` | anchor 不存在 → `absent` + `not_configured` |
| `TestAnchorPresentButNotReferenced` | 文件存在但未引用 → 警告 |
| `TestAnchorReferencedByPFConf` | 正确识别 `/etc/pf.conf` 引用 |
| `TestPFConfCommentIgnored` | 注释行不算引用 |
| `TestParseAnchorList` | 列表解析与去空白 |
| `TestLoadAverageParsing` | 负载解析 |

### 2.9 审计（`internal/audit`）

| 测试 | 验证内容 |
| --- | --- |
| `TestWriteAndList` | 写入 → 查询，字段与明细完整 |
| `TestListFiltersByAction` | 按动作过滤 |
| `TestListLimitClamped` | limit ≤0 回落默认；超限不报错 |
| `TestResultConstraintEnforced` | 非法 result 被拒绝 |
| `TestBestEffortDoesNotPanic` | 数据库关闭时不 panic |
| `TestNoSecretsInDetail` | 审计内容不含 password/token/cookie/secret |

### 2.10 模拟采集器（`internal/platform/fake`）

| 测试 | 验证内容 |
| --- | --- |
| `TestProvidesAllFields` | 字段完整，可用于前端开发 |
| `TestFakeDataIsMarked` | **模拟数据必须带明确标识**，避免误认为真实状态 |
| `TestInjectError` | 错误注入可用 |

## 3. 覆盖率（M1 实测）

M2 完成后的实测覆盖率（`go test ./... -cover`）：

```text
internal/platform/darwin/pfrules    88.2%   ← M2 规则生成与校验
internal/platform/darwin/firewalld  43.2%   ← 未覆盖部分主要是 pfctl 真实调用（需 root）
internal/firewallstore              81.4%   ← M2 规则持久化
internal/platform/execx             87.0%
internal/audit                      89.6%
internal/platform/fake              88.9%
internal/config                     80.0%
internal/auth                       79.9%
internal/platform/darwin            78.7%
internal/database                   70.7%
internal/ipc                        68.1%
internal/server                     67.8%
internal/agent                      26.9%   ← 大量 handler 需装配依赖后才可达
──────────────────────────────────────────
合计                                 56.8%
```

`firewalld` 的 43.2% 与 `agent` 的 26.9% 需要说明：
- `firewalld` 未覆盖部分集中在 `pfexecutor_darwin.go`（真实 pfctl 调用、
  dry-run 分支），这些路径只能在 root 环境下执行；
- `agent` 的 M2 handler 需要完整装配 `FirewallDeps` 才可达，
  当前测试只覆盖「依赖未就绪 → 返回未实现」的降级路径。

这两处是 M2 实机验证阶段必须补齐的部分，已记入 `docs/m2-acceptance.md`。

`internal/server` 与 `internal/auth` 的未覆盖部分主要是防御性分支
（数据库错误路径、cookie 属性分支）与 CLI 主函数；安全关键路径已全部覆盖。

## 4. 实机验收清单（M1）

以下项目需在真实 macOS 上手工执行并记录。**勾选前必须实际运行过命令。**

### 4.1 构建与启动

- [x] `make build` 成功，产出 `bin/homelab-manager`（约 11 MB）
- [x] `make dev-token` 生成令牌，文件权限 0600
- [x] `make dev-agent` 启动成功，日志显示 4 个方法已注册
- [x] `make dev-server` 启动成功，监听 `127.0.0.1:7788`
- [x] `curl http://127.0.0.1:7788/api/v1/healthz` 返回 `ok:true`

### 4.2 真实数据采集（macOS 27.0.1 / Apple M4 / arm64）

- [x] 主机名、macOS 版本（27.0.1 / 26A434）、内核（Darwin 27.0.0）正确
- [x] CPU 型号 Apple M4、10 物理 / 10 逻辑核心正确
- [x] 负载均值可读（与 `sysctl -n vm.loadavg` 一致）
- [x] 内存已用 / 可用与手工核算一致（口径已在页面标注）
- [x] 磁盘容量与 `df -k` 一致（APFS 容器语义已在页面标注）
- [x] 启动时间与 `sysctl -n kern.boottime` 一致
- [x] 网络接口识别正确：lo0 为回环、en0 为以太网、utun* 为隧道
- [x] IPv6 地址正确分类：`::1` 回环、`fe80::` 链路本地、`2409:...` 公网
- [x] `hasIPv6` / `hasGlobalIPv6` 标记正确

### 4.3 数据质量（最关键的验收项）

- [x] 非 root 运行时 PF 状态为 `enabled: null` + `state: unknown`，**不是** `false`
- [x] 页面显示「PF 状态：不可读」，并有明确说明「不可读不等于已关闭」
- [x] anchor 状态为 `absent`（M1 不创建任何文件）
- [x] `writable: false`，页面标注「当前为只读模式」
- [x] `/api/v1/firewall/apply` 返回 501 `not_implemented`
- [x] `/api/v1/power/reboot` 返回 501
- [x] `/api/v1/services` 返回 501
- [x] `meta.features` 中 4 项写能力全为 `false`

### 4.4 安全防护

- [x] `curl -H "Host: evil.com" .../healthz` → 421
- [x] 跨站 Origin 的 POST 请求 → 403
- [x] 错误令牌调用 Agent → `unauthorized`（测试覆盖）
- [x] 重复请求 ID → 拒绝（测试覆盖）
- [x] Socket 文件权限 0660、目录 0750（测试覆盖）
- [x] 响应头包含 CSP / X-Frame-Options / nosniff / no-store
- [x] 生产构建产物无 sourcemap

### 4.5 前端

- [x] 概览页显示真实系统数据（非模拟）
- [x] 5 个页面（概览 / 防火墙 / 网络 / 审计 / 设置）均正常渲染
- [x] 无横向滚动溢出（1440px 与 390px 视口均验证）
- [x] 移动端（390×844）导航收起，下拉菜单可展开并正确跳转
- [x] 浅色与深色模式文字对比度全部 ≥ 4.5:1（脚本化检测，0 处不达标）
- [x] 无文字被裁剪（`scrollWidth > clientWidth` 检查，0 处）
- [x] 审计页显示真实操作记录
- [x] 设置页正确显示访问模式与能力清单

### 4.5.1 Naive UI 替换后的复验（2026-10-26）

| 项目 | 方法 | 结果 |
| --- | --- | --- |
| 组件渲染 | 5 页 × 2 主题，检查 `Failed to resolve component` | 零警告 ✅ |
| 未注册组件陷阱 | 校验 `NCard.name === "Card"` | 已改为显式注册表 ✅ |
| 整体积 | `vite build` | gzip 243 kB（naive 187 / vue 42 / 业务 19）✅ |
| 全量注册对比 | `app.use(naive)` 前后对比 | 442 → 243 kB（省 45%）✅ |
| 明暗切换 | 点击主题按钮后读 `html.dark` 与计算色值 | 双向正常，且持久化 ✅ |
| 明暗一致性 | 校验卡片背景与 body 背景 | `#1a1e25` / `#12151a`，无撕裂 ✅ |
| 对比度（正确算法） | 逐层叠加半透明背景后计算 WCAG 比值 | 10 组合 0 处不达标 ✅ |
| 表单校验 | 输入短密码 / 不一致密码 | 两条提示均正确 ✅ |
| 数据表格 | 检查列数、行数、分页控件 | 5 列 / 20 行 / 分页正常 ✅ |
| 移动端布局 | 390px 视口 | 无横向溢出，侧栏隐藏 ✅ |
| 移动端导航 | 点击菜单 → 检查下拉项 → 点击跳转 | 5 项齐全，跳转正确 ✅ |
| 远程登录流程 | 错误密码 → 正确密码 | 错误停留并提示；正确跳转概览 ✅ |
| 后端回归 | `go test ./...` | 109 个测试全部通过 ✅ |

**对比度修正明细**（组件库默认值不满足 WCAG AA）：

| 元素 | 原值 | 原对比度 | 修正值 | 新对比度 |
| --- | --- | --- | --- | --- |
| 绿色 Tag 文字 | `#1f9d55` | 3.05 | `#146b3b` | 5.6 |
| 橙色 Tag 文字 | `#d98a00` | 2.25 | `#7a4f00` | 6.2 |
| 红色 Tag 文字 | `#d64545` | 4.38 | `#a82626` | 6.3 |
| 蓝色 Tag 文字 | `#2f6fed` | 3.89 | `#1d4fb5` | 6.4 |
| 三级文字（浅色） | `#7d838d` | 3.82 | `#666c76` | 4.9 |
| 三级文字（深色） | `#7b828d` | 4.31 | `#8b93a0` | 5.2 |
| placeholder（深色） | 白 38% | 3.57 | tertiary 实色 | 5.6 |
| 品牌块白字（深色） | accent 底 | 2.71 | 固定 `#2359c9` 底 | 6.0+ |

### 4.6 打包

- [x] `plutil -lint` 校验两个 launchd plist 均通过
- [x] `./packaging/macos/pkg/build-pkg.sh` 成功产出 `HomeLabManager-0.1.0.pkg`（4.6 MB）
- [x] `pkgutil --expand-full` 确认载荷仅 3 个文件，属主 wheel
- [ ] PKG 在干净系统安装验证（M5）
- [ ] DMG 构建与挂载验证（M5）
- [ ] Developer ID 签名与公证（M5）

## 4.7 M2 防火墙实机验收清单（需 root，未验证）

以下项目**必须**在真实 Mac mini 上以 root 运行 Agent 后逐项验证。
未全部通过前，M2 的防火墙写入能力应视为 beta。

### 前置检查

```bash
# 1. anchor 是否已在主配置中引用（调用顺序会影响 quick 的行为）
sudo grep -n "anchor" /etc/pf.conf

# 2. 当前 PF 状态基线
sudo pfctl -s info
sudo pfctl -s Anchors

# 3. 记录本机 IPv6 地址
ifconfig en0 | grep inet6
```

- [ ] `/etc/pf.conf` 中存在本应用 anchor 的 `anchor` 与 `load anchor` 行
- [ ] anchor 调用位置在系统 anchor 之后（避免 `quick` 截断系统规则）
- [ ] 已记录 PF 基线状态，便于对照

### 基础连通性（不变量 1–4）

- [ ] `pfctl -n -f` 对生成的规则文件返回 0（语法通过）
- [ ] 应用后 `sudo pfctl -a com.homelab.manager.ipv6 -sr` 能看到全部规则
- [ ] `ping6 ::1` 正常（不变量 2）
- [ ] `ping6 <本机公网 IPv6>` 正常（不变量 2）
- [ ] 主动 `curl -6 https://example.com` 正常（不变量 3）
- [ ] 邻居发现正常：`ndp -a` 能解析同 LAN 设备（不变量 4）
- [ ] `ping6` 大包（`-s 1400`）不失败（路径 MTU 正常，不变量 4）
- [ ] **IPv4 行为与启用前一致**（不变量 1）：`ping`、`curl -4`、
      同 LAN Windows 用 IPv4 访问均不受影响

### 白名单端口（不变量 5、6）

- [ ] 同 LAN Windows 访问白名单端口（如 443）成功
- [ ] 同 LAN Windows 访问未在白名单的端口被拒绝（`Test-NetConnection` 超时）
- [ ] 默认拒绝只影响 `en0`：其他接口（如 `utun*`）的入站不受影响
- [ ] 尝试把 `lo0` 加入默认拒绝 → 被后端拒绝（400）

### 超时回滚（最关键）

- [ ] 应用一份「故意阻断 SSH 端口」的规则
- [ ] **远程 SSH 连接立即断开**
- [ ] 60 秒后不确认，连接自动恢复（超时回滚生效）
- [ ] 检查状态：`curl /api/v1/firewall/transaction` 返回 `rolled_back`
- [ ] 应用同样的规则并立即确认 → SSH 保持断开（证明确认确实固化了配置）
- [ ] 手动回滚后 SSH 恢复

### 崩溃恢复

- [ ] 应用规则后（不确认）立即 `sudo launchctl kickstart -k system/com.homelab.manager.agent`
- [ ] Agent 重启后：若已超时则自动回滚；若未超时则保留原 deadline
- [ ] 检查 `/var/run/homelab-manager/firewall-state.json` 的 `deadline` 未被重置

### 环境变化

- [ ] IPv6 前缀变化后，访问本机新地址仍正常
- [ ] 网卡切换（en0 ↔ en1）后规则仍正确生效
- [ ] 睡眠唤醒后规则仍生效
- [ ] 系统重启后 anchor 按配置重新加载（若已写入主配置）

### 例外场景

- [ ] 系统开启「应用防火墙」时，两者规则互不破坏
- [ ] 其他程序的 anchor 仍被正常加载（`pfctl -s Anchors` 对照基线）

## 5. 未覆盖 / 待补

| 项 | 原因 | 计划 |
| --- | --- | --- |
| PKG 实际安装 | 需干净测试机，且需 root 安装 | M5 |
| PF 写路径（规则应用/回滚） | 功能未实现 | M2 |
| root 下 PF 状态读取 | 本机 sudo 不可用 | 部署后验证 |
| 前端单元测试 | 优先保证后端安全路径 | M2 前补 Vitest |
| 长时间稳定性（72h+） | 需持续运行环境 | M3 前 |
| IPv6 前缀变化 / 网卡切换 / 睡眠唤醒 | 需真实网络事件 | M2 |
| 同 LAN Windows 访问白名单端口 | 需 M2 规则功能 | M2 |
