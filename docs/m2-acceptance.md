# M2 验收记录：IPv6 防火墙核心

> 验收日期：2026-10-26
> 环境：macOS 27.0.1 (26A434) / Apple M4 / arm64 / Go 1.27.3
> 结论：**代码与逻辑层通过；PF 真实加载未验证（需 root，本机不可用）**

## 1. 交付范围

| 能力 | 状态 | 说明 |
| --- | --- | --- |
| 规则 CRUD | ✅ | 表单校验 + 模板套用 + 排序 + 启停 |
| PF 规则生成 | ✅ | 强制规则 + 用户规则 + 默认拒绝，全部 inet6 限定 |
| 语法/不变量校验 | ✅ | 纯 Go 双层校验，不依赖 root |
| `pfctl -n -f` 语法检查 | ⚠️ 代码就绪 | dry-run 时不执行；真实执行需 root |
| 应用事务（pending） | ✅ | 状态机 + 落盘 |
| 60 秒超时自动回滚 | ✅ | 后台定时器 + 读接口顺带检查 |
| Agent 崩溃后恢复 | ✅ | 启动时 `Recover()`，Deadline 为绝对时间不重置 |
| 确认 / 主动回滚 | ✅ | 幂等 |
| 规则版本记录 | ✅ | 候选 / 已应用 / 已回滚 / 失败 |
| 审计 | ✅ | 保存、应用、确认、回滚全部留痕，含被拒绝的尝试 |

## 2. 八条网络不变量的实现与验证

PLAN.md 5.2 要求的 8 条不变量，逐条对应到代码与测试：

| # | 不变量 | 实现方式 | 验证 |
| --- | --- | --- | --- |
| 1 | 自定义规则不限制 IPv4 | 所有 pass/block 语句强制带 `inet6` | `TestAllRulesAreInet6`、`TestVerifyDetectsNonInet6Rule` |
| 2 | `::1` 与本机公网 IPv6 可访问自己 | 强制 `pass quick on lo0 all` + `pass in quick inet6 from ::1 to ::1` | `TestGenerateContainsMandatoryRules`、`TestVerifyDetectsMissingLoopback` |
| 3 | 主动出站与返回流量正常 | 强制 `pass out quick inet6 all keep state` | 同上 |
| 4 | 保留 ND / RA / PMTU 等必需 ICMPv6 | 强制放行类型 1,2,128,129,133,134,135,136,137 | `TestVerifyDetectsMissingICMPv6` |
| 5 | 白名单端口可达、未授权端口受阻 | 用户规则 `pass quick` 在前，默认拒绝在后 | 生成顺序 + `TestVerifyPassesOnGeneratedContent` |
| 6 | 仅对选定物理接口默认拒绝 | 默认拒绝仅按 `DefaultDenyInterfaces` 生成；`utun*`/`bridge*` 被拒绝 | `TestGenerateRejectsDangerousDefaultDeny`、`TestVerifyDetectsNonInet6Rule` |
| 7 | 不使用 `pfctl -d` 作为暂停/卸载 | `execx` 参数白名单禁止 `-d`/`-F`/`-e`/`-k`；生成器不产出 | `TestPFctlGuardRejectsDangerousFlags`、`TestGenerateNeverEmitsGlobalDirectives` |
| 8 | 不干扰主规则集与系统 Anchor | 只操作本应用 anchor；绝不产出 `set`/`scrub`/`nat`/`rdr`/`include` | `TestGenerateNeverEmitsGlobalDirectives` |

### 关于不变量 8 的补充说明

`set skip on lo0` 属于主规则集的全局配置，**在 anchor 内无效**。
因此生成器改用 `pass quick on lo0 all` 达到同等效果，作用域限定在本 anchor。
这一条在 `docs/architecture.md` 与生成器注释中都有说明，避免后来者「优化」成 `set skip`。

`quick` 的语义是「首个匹配立即生效并终止后续评估」。因此：
- 强制放行必须在用户规则之前；
- 默认拒绝必须在最后；
- 本应用的 anchor 应在 `/etc/pf.conf` 中靠后调用（M5 安装时确认顺序）。

## 3. 事务与回滚设计

```text
Apply(content)
  1. 若已有 pending → 先回滚（不允许两个未确认事务叠加）
  2. 写 anchor 文件（原子：临时文件 → fsync → rename）
  3. pfctl -n -f 语法检查（失败即中止，不触碰运行状态）
  4. 快照当前 anchor 内容（回滚参考）
  5. 【落盘 pending 状态】  ← 先写盘再加载
  6. pfctl -a <anchor> -f <file> 加载
  7. 进入 pending，记录绝对 Deadline

Confirm()  → 校验未超时 → 置 confirmed，清除 Deadline
Rollback() → 置 rolled_back，加载 /dev/null 清空 anchor
Recover()  → 启动时：pending 且已超时 → 回滚；未超时 → 保留原 Deadline
```

### 为什么必须先落盘再加载

如果顺序反过来（先加载再落盘），在第 6 步与第 7 步之间崩溃，磁盘上仍是「已确认」状态，
而系统已经加载了未确认的规则 → 永远不会回滚，机器可能停在失联状态。

### 为什么 Deadline 是绝对时间而非「剩余秒数」

若存剩余秒数，Agent 重启后倒计时会被重置，等于无限延长未确认窗口。
存绝对时间后，`Recover()` 能正确判断「已过期」，测试
`TestRecoverKeepsUnexpiredDeadline` 专门锁定了这一点。

### 回滚的语义

回滚 = 清空本应用的 anchor（加载 `/dev/null`），而不是恢复上一份内容。

原因：
- 本应用只持有历史内容的哈希，不持有完整副本（若持有，等于把规则文件复制到数据库，
  扩大了敏感面）；
- 清空 anchor 等价于「本应用不加任何规则」，这是最安全的已知状态；
- 不会影响其它程序的 PF 配置。

## 4. 自动化测试

```text
go test ./... -count=1      180 个测试全部通过（13 个包）
go test ./... -race        通过
go vet ./...               无告警
```

新增测试分布：

| 包 | 测试数 | 重点 |
| --- | --- | --- |
| `platform/darwin/pfrules` | 19 | 生成器、校验器、模板、注释注入防护 |
| `platform/darwin/firewalld` | 17 | 状态机、崩溃恢复、超时、幂等 |
| `platform/execx` | +5 | pfctl 参数白名单 |
| `firewallstore` | 8 | 事务性保存、ID 回填、版本生命周期 |
| `server` | +10 | 风险确认、审计、认证 |
| `agent` | +5 | 未实现降级、方法清单单一来源 |
| `config` | +2 | PublicHosts |

### 覆盖的关键安全场景

| 测试 | 验证内容 |
| --- | --- |
| `TestCommentInjectionPrevented` | 规则名含 `\n` 时无法注入额外 PF 规则 |
| `TestPFctlGuardRejectsDangerousFlags` | `-d`/`-e`/`-F`/`-k`/`-x` 全部拒绝，含混在合法参数中的情况 |
| `TestApplyAbortsOnSyntaxError` | 语法失败时不加载、不写 pending |
| `TestConfirmRejectedAfterDeadline` | 超时后确认被拒绝并触发回滚 |
| `TestApplyRollsBackOnLoadFailure` | 加载失败自动回滚 |
| `TestRecoverRollsBackExpiredOnStartup` | 崩溃重启后回滚超时配置 |
| `TestTemplatesAvoidUnintendedWideRanges` | 模板不会意外放行大段端口 |
| `TestFirewallWriteRequiresAuth` | 远程模式未登录的写请求 401 且不触达 Agent |

## 5. 实机验证（dry-run 模式）

本机 `sudo` 不可用，**无法执行真实 pfctl**。因此以 dry-run 模式（`dev` 或非 root 自动启用）
验证了除「真实加载」之外的全部链路。

### 已验证

| 项目 | 结果 |
| --- | --- |
| 模板接口 | 5 个模板，Caddy 已拆为 80/443/HTTP3 三条 ✅ |
| 保存规则 → 生成 PF 文本 | 2 条用户规则 + 5 条强制规则，校验通过 ✅ |
| 规则 ID 回填 | 生成的注释编号为真实 ID（如 `# [4]`）✅ |
| 应用（无风险确认） | HTTP 400，且不触达 Agent ✅ |
| 应用（带确认） | 成功，返回 revisionId 与 deadline ✅ |
| 事务状态 | `pending`，deadline 为绝对时间 ✅ |
| 确认 | `confirmed`，清除 deadline ✅ |
| 重复确认 | 幂等，返回 200 ✅ |
| 回滚 | `rolled_back`，原因被记录 ✅ |
| 审计 | 保存/应用/确认/回滚全部留痕，含 `denied` 记录 ✅ |
| UI 倒计时横幅 | 显示剩余秒数 + 进度条 + 确认/回滚按钮 ✅ |
| UI 确认操作 | 点击后横幅消失，状态变「已确认」✅ |
| 对比度（浅色/深色） | 0 处不达标 ✅ |
| 横向溢出（1440px / 390px） | 无 ✅ |

### 未验证（需 root 实机）

| 项目 | 原因 |
| --- | --- |
| `pfctl -n -f` 真实语法检查 | 需 root |
| `pfctl -a <anchor> -f` 真实加载 | 需 root |
| `pfctl -a <anchor> -sr` 快照读取 | 需 root |
| 本机 `::1` 与实际公网 IPv6 连通性 | 需规则真正生效 |
| 同 LAN Windows 访问白名单/非白名单端口 | 需真实网络环境 |
| IPv6 前缀变化、网卡切换、睡眠唤醒 | 需真实网络事件 |
| anchor 在 `/etc/pf.conf` 中的调用顺序 | 需检查主配置 |

**以上必须在真实 Mac mini 上按 `docs/testing.md` 的清单逐项验证后才能视为 M2 完成。**

## 6. 本次发现并修复的缺陷

### 缺陷 1：Caddy 模板过度授权（安全）

- **现象**：生成的规则为 `port 80:443`。
- **根因**：把「80 和 443 两个端口」误写成 PF 的端口范围 `80:443`，
  而 PF 的 `a:b` 会放行区间内**全部**端口（含 81–442）。
- **修复**：拆成两条独立规则。
- **回归测试**：`TestTemplatesAvoidUnintendedWideRanges`、`TestCaddyTemplateUsesExactPorts`。
- **教训**：模板里的「看起来对」的端口写法，实际是安全漏洞。

### 缺陷 2：生成器的 `Content` 从未赋值

- **现象**：所有生成结果的 PF 文本为空，校验器报「缺少全部强制规则」。
- **根因**：`Generate` 把内容写进 `strings.Builder` 却忘了 `gen.Content = b.String()`。
- **修复**：补上赋值并加注释说明该陷阱。
- **回归测试**：`TestGenerateContainsMandatoryRules`（症状看起来像生成逻辑错，
  实际只是漏赋值）。

### 缺陷 3：检查脚本的 pfctl 参数白名单缺失（安全）

- **现象**：`pfctl` 在二进制白名单内，意味 `pfctl -d`（全局关闭防火墙）可被发起。
- **根因**：白名单只校验路径，不校验参数。
- **修复**：新增 `argGuards` 参数级白名单，只允许
  `-s info|Anchors`、`-n -f <file>`、`-a <anchor> -f <file>`、`-a <anchor> -sr`。
- **回归测试**：`TestPFctlGuardRejectsDangerousFlags`（含「危险参数混在合法参数中」）。

### 缺陷 4：`PublicHosts` 是死代码，反向代理下写请求全部 403

- **现象**：经代理访问时所有 POST/PUT 返回 403「请求来源不被允许」，
  但 GET 正常——因为只有写请求校验 Origin。
- **根因**：`Server.Options.PublicHosts` 存在但**从未从配置注入**，
  导致 OriginGuard 只认后端自己的监听地址。
- **影响**：任何反向代理部署（生产环境常见）都会无法保存/应用规则。
- **修复**：新增 `MACPANEL_PUBLIC_HOSTS` 配置项并注入；开发代理也用同一机制。
- **回归测试**：`TestPublicHostsParsing`。

### 缺陷 5：`dirty` 状态竞态导致保存按钮时好时坏

- **现象**：套用模板后，保存按钮有时可用、有时禁用。
- **处理**：改为**保存按钮始终可用**（重复保存幂等），只在有改动时显示提示文案。
- 这不是掩盖问题：用户在任何时候都应能强制保存，而「因状态竞态而禁用按钮」
  会让用户以为界面卡死。

### 缺陷 6：Naive UI `required` 规则对 `NInputNumber` 判定错误

- **现象**：端口填了 8080，校验仍报「请输入端口」，无法提交。
- **根因**：`required: true` 对数值字段的「空值」判定不可靠（实测 value=8080 判为空）。
- **修复**：移除 `required`，只用自定义 validator（错误信息也更精确：
  能区分「没填」与「超出范围」）。
- **验证**：移除后表单可正常提交，规则加入表格。

### 缺陷 7：Vite 开发代理未改写 Host

- **现象**：`npm run dev` 下所有 API 返回「Host 头不被允许」。
- **根因**：`changeOrigin: false` 保留了浏览器侧 Host（`localhost:5199`），
  被 HostGuard 拒绝。
- **修复**：改为 `changeOrigin: true`。
- **说明**：这是开发环境问题，生产构建（直接访问 7788）不受影响。

## 7. M2 完成度判定

**代码层面完成，实机验证未完成。**

按 PLAN.md 的发布阻断条件，M2 不能仅凭单元测试宣布完成。进入 M3 之前必须：

1. 在真实 Mac mini 上以 root 运行 Agent；
2. 按 `docs/testing.md` 第 6 节逐项验证 PF 真实加载；
3. 确认 `/etc/pf.conf` 中 anchor 的调用位置；
4. 验证远程失联场景下的超时回滚确实生效（这是最重要的一条）。

在此之前，M2 的真实防火墙能力应视为 **beta，仅可在本地终端可及的环境试用**。

## 8. 下一步（M3 前置）

- [ ] 实机验证 PF 加载与回滚（root 环境）
- [ ] 确认 anchor 调用顺序：
      `grep -n anchor /etc/pf.conf`
- [ ] 查明既有 `/etc/pf.anchors/com.homelab.ipv6` 的来源（非本项目创建）
- [ ] 规则版本历史界面（当前 API 占位）
- [ ] 补充「回滚到上一份可用配置」而不仅是清空 anchor
- [ ] 前端补 Vitest 组件测试（当前仅有脚本化浏览器验证）
