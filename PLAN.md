# MacPanel — macOS 服务器管理面板开发计划

> 版本：v1.1（Go + Vue 3 Web-first 实施蓝图；2026-10-26 更新）  
> 目标平台：macOS（优先 Apple Silicon Mac mini，当前测试环境 macOS 27）  
> 产品形态：**第一阶段仅 Go + Vue 3 Web UI**；原生运行，不依赖 Docker；独立 DMG 分发（内含 PKG 安装器）  
> 使用范围：个人家庭实验室（HomeLab）场景；默认仅本机访问，远程访问可选开启  
> 实施方式：按里程碑迭代，每阶段须通过验收再继续

## 1. 产品目标与边界

开发一款轻量级 macOS 服务器管理面板（MacPanel），用于统一管理系统状态、IPv6 防火墙、定时重启、启动项与服务、网络诊断，后续支持 OrbStack 容器管理。

### 核心原则

1. **最小权限**：Web Server 以专用非特权用户运行；Root Agent 仅执行受控操作。
2. **无人值守**：系统服务由 launchd 管理，不依赖用户登录或浏览器打开。
3. **故障可恢复**：高风险网络修改具备持久化超时回滚、备份与 CLI 救援。
4. **不破坏系统**：不直接覆盖系统默认 PF 规则；不全局关闭 PF；不任意修改第三方 launchd 服务。
5. **模块化单体**：一个代码仓库、共享业务模型，避免过早引入微服务与插件系统。
6. **先验证后部署**：所有 PF、权限与安装行为必须在真实 macOS 测试环境验证。

### 非目标（首版不做）

- 多用户/RBAC、云端同步、开放公网管理、应用级联网控制、任意 Shell 执行器。
- 完整 Docker Compose 编辑器、实时流量拓扑、复杂告警平台。
- **Wails 3 / Tauri 2 桌面客户端**：当前均不开发。未来确有菜单栏或独立窗口需求时，优先评估 Wails 3，但不预先锁定框架。
- 不为将来的桌面客户端引入 Rust、Wails 运行时、Sidecar 或专用桌面通信协议。

## 2. 最新架构决策记录（ADR-001）

**决策日期：2026-10-26。决策：Web-first，Go + Vue 3；暂不采用 Wails 3 或 Tauri 2。**

### 决策依据

1. 本项目本质是 **macOS 后台管理服务**，不是以桌面窗口为核心的消费级应用。PF 防火墙、计划重启、launchd 服务必须在用户注销、窗口关闭、Web 页面关闭后持续工作。
2. Mac mini 主要承担服务器职责；浏览器可以在 macOS、Windows、手机上访问，适合低频系统管理。
3. Go 适合实现 HTTP API、系统信息采集、调度和特权 Agent；Vue 3 提供统一响应式管理界面。
4. Wails 3 与 Tauri 2 都只能解决桌面客户端外壳，不能代替独立的 Root LaunchDaemon。当前引入会增加构建、签名、安装与维护成本。
5. 保留后续增加桌面客户端的能力，但**不为尚未确定的桌面需求过度抽象**。API 与业务逻辑不依赖 WebView 或 Wails/Tauri 特有绑定即可。

### 当前明确选择

- **必须开发**：Go Web Server、Vue 3 Web UI、Root Agent、CLI 救援工具、launchd 服务、PKG/DMG 安装。
- **暂不开发**：Wails 3、Tauri 2、原生菜单栏、桌面通知桥接、Sidecar。
- **未来可选**：Wails 3 + Vue 3 作为轻量桌面外壳，连接既有本地 API；若 Wails 3 的稳定性或 macOS 兼容性不满足要求，再评估 Tauri。**不得让桌面进程接管 Agent 生命周期。**
- **访问策略**：默认 `127.0.0.1`；远程访问功能需经过独立的认证、HTTPS 与网络访问控制验收后才能启用。

### 决策变更触发条件

只有当用户明确提出“需要独立 macOS 窗口、菜单栏、Dock 集成、系统通知”等桌面体验时，才启动桌面框架 PoC。桌面客户端不得成为 v1.0 的发布阻塞项。

## 3. 技术栈

| 领域 | 选型 | 说明 |
| --- | --- | --- |
| 后端 | Go（开发时选定稳定版本） | `net/http`，`embed` 前端资源；不依赖桌面框架 |
| 前端 | Vue 3 + TypeScript + Vite | Naive UI（按需注册）+ Tailwind CSS v4；Web-first 响应式布局 |
| 存储 | SQLite（WAL） | 数据迁移、备份、定期清理 |
| 特权边界 | Root Agent + Unix Domain Socket | 固定 RPC 操作，核验对端身份和请求参数 |
| 服务管理 | launchd | Server 与 Agent 分别运行，分别配置身份和权限 |
| 防火墙 | macOS PF + 专用 Anchor | 对 macOS 版本差异进行实机验证 |
| 日志 | Go 结构化日志 + macOS 日志机制 | 不记录密钥和会话凭据 |
| 安装 | PKG 安装器 + DMG 分发容器 | **不打包 Wails/Tauri `.app`**；先验证本地签名安装，长期分发考虑 Developer ID 与公证 |
| 测试 | Go 单元/集成测试、前端测试、macOS 实机测试 | 关键网络用例必须实测 |

## 4. 系统架构

```text
浏览器（macOS 本机；远程浏览器需单独启用）
默认 http://127.0.0.1:<配置端口>
        │ HTTPS/HTTP + 会话鉴权（远程访问必须 HTTPS）
        ▼
Go Server（专用非特权账户）
  ├── Web UI / REST API / 认证
  ├── SQLite / 审计 / 指标采集
  └── 业务模块（非特权部分）
        │ Unix Domain Socket（文件权限 + 对端凭据验证）
        ▼
Go Root Agent（launchd 系统服务）
  ├── 固定特权操作接口与参数校验
  ├── PF 配置事务、状态核验与回滚
  ├── 受控重启和系统任务
  └── 受管 launchd 服务操作
        │
        ▼
macOS PF / launchctl / 系统 API
```

### 进程设计

同一 Go 二进制可支持不同子命令：

```bash
macpanel server
macpanel agent
macpanel status
macpanel firewall status
macpanel firewall rollback
```

由 launchd 将 `server` 作为非特权用户运行、`agent` 作为 Root 运行。CLI 救援需要本地授权与明确的操作边界。**无桌面主进程，不依赖用户登录或 WebView。**

### 安全要求

- Agent 不提供 `exec(command)`、任意文件写入、任意 plist 路径或任意 PF 语句执行接口。
- IPC 校验 Unix Socket 权限、对端凭据与允许的操作；请求结构严格校验，限制长度、频率及超时。
- Web 默认仅监听 `127.0.0.1`；远程模式须显式启用认证、HTTPS、CSRF/Origin/Host 校验、会话过期和登录限速。
- 不因前端按钮隐藏而跳过后端鉴权；所有高风险操作均记录审计日志。
- Agent 使用 Root 独占的配置快照与回滚状态，不信任可被非特权进程任意改写的规则文件。

## 5. 项目目录

```text
macpanel/
├── cmd/macpanel/main.go
├── internal/
│   ├── app/
│   ├── api/{handlers,middleware}/
│   ├── auth/
│   ├── config/
│   ├── database/
│   ├── ipc/
│   ├── modules/
│   │   ├── dashboard/
│   │   ├── firewall/
│   │   ├── scheduler/
│   │   ├── power/
│   │   ├── services/
│   │   ├── network/
│   │   └── orbstack/
│   └── platform/darwin/{pf,launchd,power,system}/
├── web/src/{api,components,layouts,pages,router,stores}/
├── packaging/macos/{launchd,pkg,dmg}/
├── tests/{integration,fixtures}/
├── docs/{architecture,security,api,recovery}.md
├── go.mod
└── Makefile
```

模块依赖原则：业务模块依赖抽象接口，`platform/darwin` 负责具体系统调用；不要让 HTTP Handler 直接执行系统命令。前端仅通过版本化 HTTP API 调用后端，不依赖 Wails/Tauri 的 JS bridge。

## 6. 功能规格

### 5.1 系统概览（v0.1）

- CPU、内存、磁盘、系统版本、开机时间、系统负载。
- 网络接口、IPv4/IPv6 地址与状态；区分物理接口、回环与虚拟接口。
- 前端按 5–10 秒刷新实时指标；历史采样默认 60 秒，定期聚合与清理。
- 指标读取失败应展示“未知/不可用”，不能伪造为正常值。

### 5.2 IPv6 防火墙（v0.1–v0.2，最高优先级）

#### 业务功能

- 规则分组、启用/禁用、TCP/UDP、端口或端口范围、来源 IPv6 CIDR、接口范围。
- 内置可编辑服务模板：Caddy（TCP 80/443、UDP 443）、sish（原配置 TCP/UDP 2222）、Tailscale（原配置 TCP/UDP 3478/40000）、EasyTier（原配置 TCP/UDP 11010–11013）。模板只是起点，实际端口须与监听配置核对。
- 显示配置期望状态、PF 实际状态、Anchor 加载状态、最近应用时间及错误原因。
- 规则预览、语法校验、备份、热更新、确认、超时回滚、卸载。

#### 必须满足的网络不变量

1. 自定义规则不限制 IPv4。
2. Mac mini 能通过 `::1` 和本机配置的公网 IPv6 访问自己；`lo0` 处理不得被破坏。
3. 主动发起的 IPv6 出站连接及合法返回流量正常。
4. 保留邻居发现、路由通告、路径 MTU 等必需 ICMPv6 行为；不能盲目全部封禁。
5. Windows 在同一局域网可访问白名单端口，未授权端口受阻。
6. 仅对选定的物理网络接口实施默认拒绝入站，不无条件放行全部 `utun*`/`bridge*`。
7. 不使用 `pfctl -d` 作为本应用的“暂停”或“卸载”手段；不得干扰其他应用 PF 状态。
8. 需验证 PF 主规则中的 Anchor 调用顺序、`quick`、状态表与系统 Anchor 的相互作用；`set skip on lo0` 属于全局配置，不能误认为可在任意过滤 Anchor 内生效。

#### 安全应用流程

```text
编辑候选规则 → 生成规则文件 → 语法检查
→ 保存上一份已知可用配置和 PF 相关状态
→ 应用候选配置 → 验证实际加载状态
→ 开始由 Root Agent 持久管理的回滚倒计时（默认 60 秒）
→ 用户确认且连通性验证通过：提交
→ 超时/失败/Agent 重启：恢复已知可用配置
```

必须考虑 Agent 崩溃、重启、部分加载成功、GUI 关闭、远程连接中断。首次部署需在本地终端或具备独立救援通道的环境进行。不要把“本机网页可点击确认”等同于“远程连接仍可用”。

### 5.3 电源与定时任务（v0.2）

- 立即重启、延迟重启、周期重启、取消待执行任务。
- 计划任务包含：启用状态、时区、日历表达式、下一次执行、上次结果、失败原因。
- 使用 launchd 或经过验证的系统级调度机制执行关键任务，不完全依赖 Web Server 存活。
- 明确睡眠错过任务、时区变更、开机补执行、重复执行的语义。
- 设最短重启间隔（默认建议 30 分钟），避免循环重启。
- 执行重启前写入审计记录；高风险操作需要二次确认。

### 5.4 启动项管理（v0.3）

- 分别展示 LaunchDaemons、LaunchAgents、登录项/后台项目（按系统可用接口）。
- 识别系统域与用户域；只对明确受管的服务提供启停。
- 系统关键服务只读；第三方服务默认只读；禁止任意路径操作。
- 展示服务状态、最近错误、配置来源和日志入口。
- 防止停止本软件 Agent/Server 或当前远程管理关键依赖导致失联。

### 5.5 网络诊断（v0.2–v0.3）

- IPv6 地址、接口与路由诊断。
- 本机 IPv6 回环检测、目标端口 TCP 连通性检测。
- PF 状态、Anchor、规则加载错误和服务监听检测。
- 可选查看 Tailscale/EasyTier 状态；只读优先。

### 5.6 OrbStack（v0.4，非首版阻塞项）

- 发现 Docker Endpoint；只读列出容器、状态、资源使用。
- 后续加入容器启停、重启；操作前进行授权与确认。
- Docker Socket 具备高权限，必须单独建模和授权，不能直接向 Web 用户暴露。
- 不在首版实现完整 Compose 编辑器。

## 7. Web API 草案

| 方法 | 路径 | 用途 |
| --- | --- | --- |
| GET | `/api/v1/system/overview` | 系统概览 |
| GET | `/api/v1/system/network` | 网络接口 |
| GET | `/api/v1/firewall/status` | 防火墙实际状态 |
| GET | `/api/v1/firewall/rules` | 获取规则 |
| PUT | `/api/v1/firewall/rules` | 保存候选规则 |
| POST | `/api/v1/firewall/apply` | 应用候选规则 |
| POST | `/api/v1/firewall/confirm` | 确认配置 |
| POST | `/api/v1/firewall/rollback` | 回滚配置 |
| GET/POST | `/api/v1/schedules` | 查询/创建任务 |
| GET | `/api/v1/services` | 服务列表 |
| POST | `/api/v1/power/reboot` | 受控重启 |
| GET | `/api/v1/audit` | 操作审计 |

要求：统一错误响应、参数校验、API 版本化；变更操作具有审计、权限检查和适当的幂等机制。

## 8. 数据模型

SQLite 表（可根据实际需求演进）：

- `settings`：应用设置。
- `firewall_rules`：用户编辑的规则。
- `firewall_revisions`：候选/生效配置及版本。
- `scheduled_tasks`：定时任务。
- `task_executions`：执行记录。
- `managed_services`：允许操作的受管服务。
- `audit_logs`：高风险操作审计。
- `metric_samples`：历史指标。
- `auth_sessions`：会话管理。

敏感凭据使用适当的系统受保护存储或严格权限机制；不要将明文密码放入 JSON/SQLite。Root Agent 的恢复快照应位于 Root 独占位置。

## 9. 前端页面

1. **概览**：系统指标、网络地址、关键服务、近期任务。
2. **IPv6 防火墙**：状态、规则表、模板、应用预览、回滚提示。
3. **电源与计划任务**：重启控制、计划列表、历史。
4. **启动项**：服务分类、状态、受控启停、日志。
5. **网络诊断**：接口、路由、连通性、PF 检测。
6. **设置**：监听地址、认证、安全选项、备份与版本。
7. **OrbStack（后续）**：容器列表、详情和操作。

界面要求：简体中文优先、响应式适配桌面/手机、操作反馈明确；禁止用前端模拟状态替代真实后端状态。**首版只通过浏览器访问，不创建桌面客户端。**

## 10. 安装、升级与卸载

### 安装

目标发布物：

```text
MacPanel-x.y.z.dmg
└── MacPanel.pkg
```

PKG 安装二进制、创建受限账户与目录、部署 launchd 服务、初始化数据库和配置。Root 权限通过明确的安装授权获取。

### 升级

- 安装前备份数据库、配置与已知可用 PF 快照。
- 数据库迁移可检测版本并支持失败恢复。
- 更新 Agent/Server 顺序须防止 IPC 协议不兼容。
- 升级失败不应留下失控的 PF 规则或损坏的 launchd 服务。

### 卸载

- 停止本应用服务，移除本应用的 launchd 项、Anchor 引用及独占文件。
- 不全局关闭 PF，不删除其他程序配置。
- 询问是否保留数据库和备份。
- 提供 CLI 救援和恢复文档。

### 签名

个人开发可先本地签名验证；Root 安装流程必须实测。若希望稳定跨机器安装、减少 Gatekeeper 阻碍，后续采用 Developer ID 签名与公证。

## 11. 开发里程碑

| 阶段 | 预计 | 交付物 | 验收条件 |
| --- | --- | --- | --- |
| M0 需求与测试环境 | 第 1 周 | 架构文档、威胁模型、测试清单 | 明确 PF 和 Root 权限边界，具备本地救援路径 |
| M1 基础框架 | 第 2 周 | Go Server、Agent、IPC、Vue、SQLite、认证 | Web 可读取 Agent 的真实只读状态；权限隔离通过 |
| M2 PF 核心 | 第 3–4 周 | 规则生成、验证、应用、回滚、状态检测 | IPv6 回环/出站/白名单/拒绝规则实测通过 |
| M3 系统与计划任务 | 第 5–6 周 | Dashboard、定时重启、执行历史 | 重启后任务保留，不重复执行或陷入重启循环 |
| M4 启动项管理 | 第 7–8 周 | 服务查询、受管启停、日志 | 不影响系统关键服务和管理通道 |
| M5 安装与测试 | 第 9–10 周 | PKG/DMG、升级、卸载、恢复文档 | 干净系统安装/升级/卸载和异常恢复通过 |
| M6 完善与 v1.0 | 第 11–12 周 | 移动适配、网络诊断、OrbStack 基础只读 | 满足长期无人值守稳定运行要求 |

时间为业余开发估算，按验收而非日期推进。OrbStack 可延期，不阻塞核心稳定版发布。

## 12. 测试与验收矩阵

### 单元测试

- PF 规则生成、IPv6 CIDR/端口范围验证、配置版本化。
- 调度计算（时区、夏令时、错过执行、重试保护）。
- API 参数校验、授权、错误响应。

### 集成测试

- Server 与 Agent IPC 对端验证、拒绝越权请求。
- SQLite 迁移、备份恢复、进程重启。
- Agent 崩溃/系统重启时回滚恢复。

### macOS 实机测试

- Mac mini 访问 `::1`、自身公网 IPv6、OrbStack 端口。
- 同 LAN Windows 访问允许端口；拒绝端口不可访问。
- IPv4 行为与启用前一致。
- Tailscale 网络扩展开/关，OrbStack 重启，EasyTier 使用。
- IPv6 前缀变化、网卡切换、睡眠唤醒、系统重启。
- 远程修改规则后失联，超时自动恢复。
- Server/Agent 停止、日志写满、配置损坏、PF 加载失败。
- 首次安装、版本升级、完整卸载。

**发布阻断条件**：任何导致无法恢复的远程失联、未授权 Root 操作、IPv4 意外受影响、回滚失效、循环重启问题，均阻止发布。

## 13. WorkBuddy 执行规范

WorkBuddy 按以下方式逐阶段实施：

1. **严格遵守 ADR-001：先做 Go + Vue 3 Web，不得擅自创建 Wails/Tauri 项目。**每个里程碑开始前，先输出实现设计、涉及文件、风险和测试计划。
2. 先实现可测试的最小闭环，不一次性生成全部模块。
3. 修改系统配置前，必须先提供预览、备份、验证和恢复命令。
4. 不在生产 Mac mini 上自动执行 `pfctl`、`launchctl`、`reboot` 或安装器；必须经用户明确确认。
5. 所有特权操作使用固定接口、参数白名单和调用者身份验证。
6. 每阶段交付代码、README、测试结果、已知限制和下一阶段待办。
7. 不能声称“已测试 macOS/PF”除非确实执行了相应环境的测试。
8. UI 必须展示真实状态；未实现的功能标记为未实现，不伪造成功。
9. 默认简体中文，代码标识符使用规范英文，注释按必要程度使用中文。
10. 先完成 M0、M1，再进入 M2；未经实机验证的 PF 策略不可设为默认开机强制启用。

### 第一条执行任务（建议直接交给 WorkBuddy）

> 阅读本计划，特别是 ADR-001，**只采用 Go + Vue 3 Web-first 技术路线，不引入 Wails 3、Tauri 或 Rust**。先完成 M0 和 M1 的技术设计与项目脚手架：建立 Go 模块、Vue 3 + TypeScript 前端、SQLite 迁移、非特权 Server、Root Agent 的接口定义、Unix Socket 通信和只读系统/PF 状态查询。优先采用可测试的接口与模拟实现；不得自动修改生产机器的 PF、launchd 或重启配置。完成后给出目录树、启动方法、测试命令、风险清单和 M1 验收结果。对于 macOS 特权服务部署和对端身份验证，先给出可验证设计与测试，不要使用仅凭 Socket 路径就信任调用方的简化实现。

## 14. 完成定义（Definition of Done）

- [ ] 仅使用 Go + Vue 3 Web-first 架构，无 Wails/Tauri 运行时依赖。
- [ ] Web UI、Go Server、Root Agent 责任边界清晰。
- [ ] 安全认证与 IPC 授权经过测试。
- [ ] PF 规则具备备份、语法校验、实际状态检查和独立回滚。
- [ ] IPv6 本机回环、主动出站、合法入站及未授权入站全部实测通过。
- [ ] 定时重启可恢复、可取消，不出现重复执行。
- [ ] 启动项管理只操作明确受管对象。
- [ ] macOS 重启后无需用户登录即可恢复服务。
- [ ] 安装、升级、卸载与救援流程可重复执行。
- [ ] 关键异常场景有自动恢复或明确的本地救援路径。
- [ ] 文档、测试结果、版本变更记录完整。

