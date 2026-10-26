# 系统架构

> 对应里程碑：M0 / M1
> 最后更新：2026-10-26

## 1. 总体结构

```text
浏览器（默认仅本机）
    │ HTTP + 会话鉴权（远程模式必须 HTTPS）
    ▼
┌─────────────────────────────────────────────┐
│ Go Server（非特权账户 _homelab）              │
│  ├── 内嵌前端资源（embed）                    │
│  ├── REST API /api/v1/*                      │
│  ├── 认证与会话（scrypt + cookie）            │
│  ├── SQLite（WAL）                           │
│  └── 审计日志                                 │
└──────────────────┬──────────────────────────┘
                   │ Unix Domain Socket
                   │ 长度前缀帧 + 共享令牌 + 对端 UID 校验
                   ▼
┌─────────────────────────────────────────────┐
│ Go Root Agent（root，launchd 系统服务）        │
│  ├── 固定 RPC 方法注册表                       │
│  ├── 系统信息采集（sysctl / vm_stat / statfs） │
│  ├── 网络接口采集（net.Interfaces）            │
│  └── PF 状态只读查询（pfctl -s）               │
└──────────────────┬──────────────────────────┘
                   ▼
        macOS：/dev/pf、sysctl、网络栈
```

单一一进制 `homelab-manager` 通过子命令切换角色：

| 子命令 | 身份 | 职责 |
| --- | --- | --- |
| `agent` | root | 特权只读采集（M1）、未来的 PF/launchd/重启写操作 |
| `server` | _homelab | HTTP 服务、认证、SQLite、审计、托管前端 |
| `status` | 任意授权用户 | CLI 查询 Agent 状态（无需浏览器，用于救援） |
| `token` | root | 生成 IPC 共享令牌 |
| `config` | 任意 | 打印生效配置，便于排查配置问题 |

## 2. 为什么不做桌面客户端

见 PLAN.md ADR-001。要点：本产品本质是后台管理服务，PF 规则、定时重启、launchd
服务必须在浏览器关闭、用户注销后继续工作。桌面壳（Wails/Tauri）只能提供窗口，
不能替代独立的 Root LaunchDaemon，反而增加构建、签名、安装与维护成本。

因此：**API 与业务逻辑不依赖任何 WebView 绑定**，未来若需要菜单栏应用，可以用
Wails 3 连接既有本地 API，且桌面进程不得接管 Agent 生命周期。

## 3. 依赖方向

```text
cmd/homelab-manager
    ├── internal/server ──► internal/app ──► internal/ipc ──► internal/model
    │        ├── internal/auth    ──► internal/database
    │        └── internal/audit   ──► internal/database
    ├── internal/agent ──► internal/platform（接口）
    │        └── internal/platform/darwin（实现）
    └── internal/config / logger / version
```

规则：

- `internal/model` 不依赖任何其它内部包（纯数据结构 + 错误码）。
- `internal/platform` 定义接口，`platform/darwin` 提供实现，`platform/fake` 提供模拟实现。
- HTTP handler 不直接执行系统命令，全部经由 `app.Service → ipc.Client → Agent`。
- 外部命令统一走 `internal/platform/execx`，强制白名单 + 超时 + 输出上限。

### 前端分层

```text
web/src
├── api/          HTTP 客户端与后端类型定义（与 internal/model 一一对应）
├── theme/        ★ 组件库主题覆盖、明暗模式、按需注册的组件表
├── stores/       全局状态（认证、元信息）
├── lib/          格式化工具、useResource（加载/错误/轮询三态）
├── components/   自有语义组件（StatBlock / Callout / PanelCard）
├── layouts/      AppShell（侧栏 + 顶栏 + 内容区）
└── pages/        5 个页面
```

分层约束：

- **`theme/` 是组件库的唯一入口**。业务页面不直接 import 主题变量，
  只使用 CSS 变量（`var(--color-*)`）或 Naive UI 组件的 `type` 属性。
  这样换组件库时只需重写 `theme/`，业务代码不动。
- **明暗模式只有一个真源**（`theme/mode.ts`）。它同时更新 `html.dark` 类
  和 Naive UI 的 `theme` 属性。如果两套各自判断 `prefers-color-scheme`，
  会出现「卡片深色、页面底色浅色」的撕裂。
- **`components/` 里的三个自有组件不替换为组件库组件**：
  `StatBlock` 表达「不可用 ≠ 值为 0」，`Callout` 承载阶段/能力边界标注，
  这些语义组件库没有。它们内部仍用 Naive UI 原语（NTag/NAlert）保证视觉一致。

#### 组件库踩坑记录（换库或升级时必读）

| 问题 | 原因 | 处理 |
| --- | --- | --- |
| 页面全白且不报错 | `NCard.name === "Card"`，用 `c.name` 注册成 `<Card>`，模板写的是 `<NCard>` | 显式维护 `[注册名, 组件]` 映射表 |
| 产物 1584 kB | `app.use(naive)` 全量注册，27 个组件之外全部进包 | 具名导入 + 显式注册表，降到 243 kB(gzip) |
| 深色模式底色撕裂 | CSS 变量与组件库各自判断系统偏好 | 单一响应式来源同时驱动两者 |
| 大量对比度不达标 | 组件库把语义色同时用作文字色和 12% 透明底 | 在 `themeOverrides` 里为 Tag 指定压深后的 `textColor*` |
| `type="primary"` 的 Tag 覆盖无效 | 该类型用 `primaryColor`，不受 `Tag.textColor` 影响 | 改用 `type="info"`（对应 `textColorInfo`） |
| 移动端侧栏不隐藏 | `NLayoutSider` 自身是 flex 元素，`hidden/lg:block` 被组件样式覆盖 | 用外层 `div` 控制显隐 |

## 4. IPC 协议

### 帧格式

```text
[4 字节大端长度][JSON 负载]
```

| 项 | 值 | 说明 |
| --- | --- | --- |
| 请求帧上限 | 64 KiB | 请求本就很小，防止内存放大 |
| 响应帧上限 | 4 MiB | 列表类数据留余量 |
| 最大并发连接 | 16 | 防本地 DoS |
| 单连接最多请求 | 8 | 限制长连接占用 |
| 超时 | 默认 5 秒 | 可由 `HLM_IPC_TIMEOUT` 调整，上限 1 分钟 |

### 请求结构

```json
{
  "protocolVersion": 1,
  "id": "16 字节随机十六进制",
  "method": "system.overview",
  "token": "64 位十六进制共享令牌",
  "issuedAt": 1792892051,
  "params": null
}
```

解析使用 `json.Decoder.DisallowUnknownFields()`：未知字段直接报错，
避免字段名拼写错误被静默忽略。

### 已实现方法（M1，全部只读）

| 方法 | 参数 | 返回 |
| --- | --- | --- |
| `agent.health` | 无 | Agent 版本、启动时间、PID、是否 root、方法数 |
| `system.overview` | 无 | 主机名、OS、CPU、内存、磁盘、启动时间 |
| `system.network` | 无 | 网络接口列表与 IPv4/IPv6 地址 |
| `firewall.status` | 无 | PF 状态、本应用 anchor 状态、诊断警告 |

方法名刻意做成固定白名单（`agent.RegisterAll`）。**不存在**
`exec`、`read_file`、`write_file`、`pf.apply_rule` 之类的通用接口。

## 5. 数据模型

SQLite（WAL 模式，`synchronous=NORMAL`，`foreign_keys=ON`，连接池上限 1）。

| 表 | 状态 | 说明 |
| --- | --- | --- |
| `schema_migrations` | 已用 | 版本化迁移记录，按 version 升序应用 |
| `settings` | 已用 | 键值配置（含 scrypt 密码哈希） |
| `firewall_rules` | 建表完成 | M2 写入；已带 action/direction/protocol/端口范围 CHECK 约束 |
| `firewall_revisions` | 建表完成 | M2 使用；candidate / applied / rolled_back / failed |
| `scheduled_tasks` | 建表完成 | M3 使用 |
| `task_executions` | 建表完成 | 外键级联删除 |
| `managed_services` | 建表完成 | M4 使用；`allow_control` 默认 0，只读优先 |
| `audit_logs` | 已用 | 高风险操作与读接口调用留痕 |
| `metric_samples` | 建表完成 | M3 使用，历史指标 |
| `auth_sessions` | 已用 | 只存 SHA-256 令牌哈希 |
| `auth_attempts` | 已用 | 登录失败计数（按来源地址限速） |

约束示例（`firewall_rules`）：

```sql
action    TEXT NOT NULL CHECK (action IN ('pass','block'))
direction TEXT NOT NULL CHECK (direction IN ('in','out'))
protocol  TEXT NOT NULL CHECK (protocol IN ('tcp','udp','icmp6','any'))
CHECK (port_from IS NULL OR (port_from BETWEEN 1 AND 65535))
CHECK (port_from IS NULL OR port_to IS NULL OR port_from <= port_to)
```

这些约束在数据库层拒绝非法值，避免「前端校验漏了 → 脏数据落库」。
测试 `TestFirewallRulesConstraints` 逐条验证约束真实生效。

## 6. 「不伪造数据」的实现机制

这是本项目最强的产品约束，落在三层：

1. **采集层**：无法读取的指标写入 `missing: [{metric, reason}]`，
   布尔字段用 `*bool` 而非 `bool`（`null` 表示未知，`false` 表示确认关闭）。
2. **API 层**：存在缺失项时 `meta.degraded = true` 并附带可读说明；
   采集整体失败返回 `503 unavailable`，不返回部分数据。
3. **前端层**：`unavailable` 状态独立渲染为「不可用」标签并降低视觉权重，
   与「值确实是 0」严格区分；失败时保留旧数据但显著标注「可能已过期」。

未实现的功能一律返回 `501 not_implemented`，能力声明 `features.*` 中对应项为 `false`。

## 7. 关键路径时序：读取防火墙状态

```text
浏览器 GET /api/v1/firewall/status
  → withRequestID / withAccessLog / withRecover / withSecurityHeaders
  → withHostGuard（校验 Host，防 DNS rebinding）
  → withOriginGuard（GET 放行）
  → authorizer.requireAuth（本地模式：校验来源为回环）
  → handleFirewallStatus
      → app.Service.FirewallStatus
          → ipc.Client.Call("firewall.status")
              → dial unix socket → writeFrame → readFrame
                  → agent.handleFirewallStatus
                      → darwin.Collector.FirewallStatus
                          ├── pfctl -s info（失败则 enabled=null + readError）
                          ├── os.Lstat(anchor 路径)
                          └── 读取 /etc/pf.conf 判断引用
  → 写入 audit_logs
  → 响应 {ok, data, meta:{degraded, notes}}
```

## 7.1 M2：PF 规则生成与应用事务

### 分层

```text
agent/firewall.go          方法注册与流程编排（生成 → 校验 → 写文件 → 事务）
    ├── pfrules/           规则集 → PF anchor 文本；文法与不变量校验
    ├── firewalld/         应用事务状态机、超时回滚、崩溃恢复、pfctl 执行器
    └── firewallstore/     规则与版本的持久化（SQLite）
```

`pfrules` 与 `firewalld` 都不依赖数据库，因此可以纯逻辑测试；
`pfctl` 调用被 `PFApplier` 接口隔离，测试用 fake 替换。

### 生成顺序（PF 是首个匹配即生效）

```text
pass quick on lo0 all                       ← 回环，必须最先
pass in quick inet6 from ::1 to ::1         ← IPv6 回环
pass out quick inet6 all keep state         ← 出站
pass in quick inet6 proto ipv6-icmp ...     ← 必需 ICMPv6
<用户规则，按 position 升序>
block in quick inet6 on <iface> all         ← 默认拒绝，必须最后
```

顺序错了会怎样：若默认拒绝在前，用户的白名单规则永远匹配不到（`quick` 已终止评估）。

### 为什么不用 `set skip on lo0`

`set` 是主规则集的全局指令，**在 anchor 内无效**。用它会产生「看起来放行了 lo0，
实际没生效」的假象。改用 `pass quick on lo0 all`，作用域限定在本 anchor 且效果等价。

### 应用事务的状态机

```text
        Apply()                      Confirm()
idle ──────────► pending ─────────────────────► confirmed
                    │
                    │ 超时 / 主动回滚 / 加载失败 / 重启后发现超时
                    ▼
               rolled_back  ◄── 清空 anchor（加载 /dev/null）
                    ▲
                    │ 加载失败且回滚也失败
                 failed（需人工介入）
```

关键约束（每条都有测试）：

- **先落盘再加载**：否则「加载成功但状态没写盘」时崩溃，机器会停在未确认规则上且永不回滚。
- **Deadline 用绝对时间**：用剩余秒数会在重启后重置，等于无限延长未确认窗口。
- **确认前检查超时**：超时后仍能确认的话，倒计时形同虚设。
- **应用新规则前先回滚 pending**：避免两份未确认状态叠加。

### pfctl 的三层限制

| 层 | 限制 |
| --- | --- |
| `execx.allowedBinaries` | 只允许 `/sbin/pfctl` 等 5 个绝对路径 |
| `execx.argGuards` | 只允许 `-s info/Anchors`、`-n -f`、`-a <anchor> -f`、`-a <anchor> -sr`；拒绝 `-d`/`-e`/`-F`/`-k`/`-x` |
| `firewalld.PFExecutor` | anchor 名与文件路径在构造时校验；只允许本应用 anchor 与 `/dev/null` |

为什么需要参数级：`pfctl` 既能 `-a <anchor> -f <file>`（安全），
也能 `-d`（全局关闭防火墙）。只校验路径等于没限制。

## 7.2 M3：调度与电源的分层

```text
internal/schedule      纯逻辑：递归表达式、下次执行、执行决策、错过判定
internal/taskstore     SQLite 持久化（含待执行重启）
internal/platform/darwin/launchd   plist 生成、安装、卸载
internal/tasksched     编排：同步、状态机、崩溃恢复、审计
cmd/.../task-run       launchd 触发的执行入口（独立子进程）
```

**为什么要拆这么细**：调度计算是最容易出错的地方（时区、DST、
错过执行、时钟回拨），而它恰好可以完全脱离系统测试。
把它单独成包后，`schedule` 有 80% 覆盖率且全部是确定性测试。

### 为什么 task-run 是独立子进程

launchd 按 `StartCalendarInterval` 启动 `homelab-manager task-run --task <id>`。
不让常驻 Agent 自己计时，原因有三：

1. **Agent 停掉时任务仍能执行**——这是 PLAN 5.3 的硬要求；
2. 每次执行都是全新进程，不会因 Agent 长期运行而累积状态；
3. 退出码直接反映结果，launchd 的日志可读性更好。

### 待执行重启为什么落盘

延迟重启的倒计时必须在 Agent 崩溃或系统重启后恢复。放内存里会让
「30 分钟后重启」在 Agent 重启后静默消失——用户以为已排定，实际什么都没有。

恢复时先判断「系统是否在排定之后重启过」：

- 已重启过 → 意图已达成，清除记录（否则会**重启后再重启**）
- 未到期的 → 用**绝对时刻**重新定时，不重置倒计时
- 已过期的 → 立即执行（这是用户明确的一次性意图，没有「错过就跳过」的余地）

### 时钟必须单一来源

定时器用调度器自己的时钟算延迟，不是 `time.Until`（真实时钟）。
混用在测试中暴露过：目标时刻来自注入时钟而延迟取真实时钟，
会把「10 分钟后重启」变成「立即重启」。

生产环境两者一致，但这类「只有测试才暴露」的分歧说明混用本身是错的。

## 7.3 M4：启动项的三层操作边界

```text
model.ManagedService.Validate      数据层：标签前缀、路径白名单、文件名一致
services.ValidateLabelForControl   执行层：拒绝系统关键与自身
services.Controller.Execute        命令层：只接受登记过的 plist 路径
```

三层独立实现、面向不同层。任何一层被绕过，另外两层仍有兜底。
这不是冗余——「停掉系统关键服务」的代价是机器可能失联或崩溃，
值得用三道检查来防。

### 数据来源的实测结论

`launchctl list` 在 macOS 27 上对普通用户返回**空且退出码 1**，
不能作为数据源。改用 `launchctl print system`，解析其 services 段。

段内每行形如 `<PID> <标志> <label>`，只依赖首尾两列
（中间的标志列各版本会变，如 `-`、`(pe)`、`0`）。

### 为什么系统服务与本应用永久只读

- 停止 `com.apple.*` 的后果不可预测：可能是网络栈、文件系统守护进程；
- 停止本应用自身 → 面板立刻失去数据来源，而用户很可能正通过它访问这台机器。

用户要停它们应该走 CLI 或 launchctl，不在「可能因此失联」的界面里操作。

## 7.4 M6：诊断的结论分类

TCP 探测不返回「成功/失败」，而是四类结论：

| outcome | 含义 | 排查方向 |
| --- | --- | --- |
| `connected` | 握手成功 | — |
| `refused` | 端口没有服务在监听 | 服务没起来 |
| `timeout` | 无响应 | 防火墙丢弃（PF 规则写错） |
| `unreachable` | 无路由 | 地址/接口问题 |

把三者合并成「失败」会让用户无法判断问题在哪一层——而这三层的
修复动作完全不同。

### 探测目标默认限本机

诊断目标是「我配的服务起来了吗」，不是网络扫描。开放任意目标会让
面板变成探测工具，需要 `HLM_ALLOW_NONLOCAL_PROBE=1` 显式开启。

### 容器能力为什么只读

Docker Socket 的权限等价于 root：通过它可以创建特权容器、挂载宿主机
任意目录。首版只发 `GET /containers/json`，且不提供 `inspect`
（会返回环境变量，常含凭据）。

容器的启停需要在后续版本中单独设计授权与审计，不能顺手加上——
那会让「面板的权限」悄悄超过「设计时承诺的权限」。

## 8. 已知限制（M1–M4/M6）

- PF 状态需要 root：非 root 运行 Agent 时只能报告「不可读」。
- `/etc/pf.conf` 与 anchor 文件通常 0600 root:wheel，非 root 无法读取内容，
  只能判断存在性。
- 内存「已用」采用 `总量 − (free + speculative + purgeable + inactive)`，
  与「活动监视器」接近但不等价；页面已标注口径。
- APFS 上根卷与数据卷共享容器，容量数值可能相同，属系统真实语义。
- 无指标历史聚合（`metric_samples` 已建表，M3 启用）。
- 前端无自动化测试（仅类型检查 + 脚本化浏览器验证；覆盖对比度、溢出、交互流程）。
- 前端包体积以组件库为主：gzip 约 260 kB，其中 Naive UI 占 193 kB。
- **PF 写入未实机验证**：`pfctl` 真实调用需 root，开发机不可用；
  dry-run 模式覆盖了除真实加载外的全部链路。
- **回滚 = 清空 anchor**，不恢复历史规则内容（只存哈希，不复制规则文件）。
- **规则版本历史接口为占位**：`GET /firewall/revisions` 返回空列表。
- **真实重启与 launchd 调度未实机验证**：`shutdown -r`、`launchctl bootstrap`
  的真实调用需 root，开发机不可用；dry-run 覆盖了除真实执行外的全部链路。
- **服务启停未实机验证**：同上。非 root 时 Controller 不装配，
  界面会显式说明「服务列表可查看但无法启停」。
- **监听端口列表依赖 netstat 输出**：某些受限环境下 `netstat -an`
  只返回 multipath 段。此时明确报告「不可用」而不是「0 个监听」。
- **单次任务用持久化倒计时而不是 launchd**：`StartCalendarInterval`
  没有年份字段，无法表达「仅在某个日期执行一次」。
- **容器能力只读**：Docker Socket 权限等价于 root，启停需单独的授权设计。

## 9. 后续里程碑的架构预留

| 里程碑 | 状态 | 关键实现 |
| --- | --- | --- |
| M2 PF 核心 | 代码完成，实机未验证 | `pfrules` / `firewalld` / `firewallstore` |
| M3 定时任务 | 代码完成，真实重启未验证 | `schedule` / `taskstore` / `tasksched` / `launchd` |
| M4 启动项 | 代码完成，真实启停未验证 | `services` / `servicestore` |
| M5 安装 | **未做** | `packaging/macos/*` 脚手架已有，未做干净系统验证 |
| M6 诊断与 OrbStack | 代码完成，OrbStack 只读已实测 | `diag` / `orbstack` |

后续可扩展项（需要新增设计，不能顺手加）：

| 能力 | 需要 | 为什么不能直接加 |
| --- | --- | --- |
| 容器启停 | 单独的授权模型 + 审计 | Docker Socket 权限等价 root |
| PF 命中统计 | 解析 `pfctl -sr -vv` | 需要新的参数守卫与输出解析 |
| Tailscale/EasyTier 状态 | 读取各自状态文件或 CLI | 需评估是否为稳定接口 |
| 多用户 RBAC | 用户表 + 会话隔离 | ADR-001 明确首版不做 |
