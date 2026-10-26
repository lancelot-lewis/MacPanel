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

## 8. 已知限制（M1）

- PF 状态需要 root：非 root 运行 Agent 时只能报告「不可读」。
- `/etc/pf.conf` 与 anchor 文件通常 0600 root:wheel，非 root 无法读取内容，
  只能判断存在性。
- 内存「已用」采用 `总量 − (free + speculative + purgeable + inactive)`，
  与「活动监视器」接近但不等价；页面已标注口径。
- APFS 上根卷与数据卷共享容器，容量数值可能相同，属系统真实语义。
- 无指标历史聚合（`metric_samples` 已建表，M3 启用）。
- 前端无自动化测试（仅类型检查 + 手工/脚本化浏览器验证）。

## 9. 后续里程碑的架构预留

| 里程碑 | 需要新增 | 已有预留 |
| --- | --- | --- |
| M2 PF 核心 | `platform/darwin/pf` 写实现、规则生成器、事务与回滚状态机 | `firewall_rules` / `firewall_revisions` 表、`execx` 白名单、Agent 方法注册表 |
| M3 定时任务 | launchd 或系统调度封装、时区计算 | `scheduled_tasks` / `task_executions` 表 |
| M4 启动项 | launchd 域枚举（只读）、受管白名单 | `managed_services.allow_control` |
| M5 安装 | PKG/DMG 签名与公证 | `packaging/macos/*` 脚手架、launchd 模板 |
| M6 OrbStack | Docker socket 客户端（独立授权建模） | 无（刻意不提前抽象） |
