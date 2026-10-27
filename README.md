# MacPanel

MacPanel：macOS 服务器管理面板。单个 Go 二进制 + 内嵌 Vue 3 前端，原生运行、不依赖 Docker，
用于统一管理系统状态、IPv6 防火墙、定时重启、启动项与网络诊断。

> **当前版本：M0–M4 + M6 功能完成；M5 打包链路已打通并完成静态校验，干净系统安装待验证**
>
> **已实现**
> - 系统概览、网络接口、操作审计、本地/远程认证
> - IPv6 防火墙：规则编辑 / 预览 / 应用 / 60 秒超时回滚 / 崩溃恢复
> - **本地终端救援**：`firewall status|rollback|unload`（不经 IPC，面板失联时可用）
> - 电源与计划任务：受控重启、每日/每周/固定间隔/单次、最短间隔保护、
>   失败熔断、错过执行策略、launchd 调度、崩溃恢复
> - 启动项：launchd 服务扫描与分类、受管白名单、受控启停
> - 网络诊断：TCP 连通性检测（区分拒绝/超时/不可达）、IPv6 路由与监听端口
> - OrbStack：容器只读列表（无任何写操作）
> - 打包：PKG / DMG 构建、升级前自动备份、随包安装的卸载脚本、包结构静态校验
>
> **未实现**：容器启停、PF 规则命中统计、Tailscale/EasyTier 状态、多用户 RBAC。
> **未签名**：分发前需 Developer ID 签名与公证。
>
> ⚠️ **以下行为未在本机实机验证**（开发机无 root）：
> - PKG 的真实安装 / 升级 / 卸载
> - PF 真实加载（见 [`docs/m2-acceptance.md`](docs/m2-acceptance.md)）
> - 真实重启、launchd 真实调度与触发（见 [`docs/m3-m4-m6-acceptance.md`](docs/m3-m4-m6-acceptance.md)）
>
> 首次部署前请按 [`docs/m5-acceptance.md`](docs/m5-acceptance.md) 第 3 节的清单逐项确认，
> 并**保留一个不受 PF 规则影响的本地通道**（显示器或另一条网络）。

## 技术路线

按 PLAN.md 的 ADR-001：**Go + Vue 3 Web-first，不引入 Wails / Tauri / Rust**。
本产品本质是后台管理服务——PF 规则、计划重启、launchd 服务必须在浏览器关闭、
用户注销后继续工作，桌面壳无法替代独立的 Root LaunchDaemon。

### 前端为何选 Naive UI

M2 的核心是「规则编辑器」——端口范围、CIDR、协议、多接口选择、动态校验，
以及「应用 → 60 秒倒计时 → 确认/回滚」的弹窗流程。手写这些表单与状态机
容易在细节上出错，因此引入组件库换取表单校验、数据表格、弹窗与键盘可达性。

代价与取舍：

- **体积**：按需注册后 gzip 约 295 kB（naive 212 + vue 42 + 业务 37 + CSS 4），
  是纯手写方案（52 kB）的 5.7 倍。这是引入组件库的固有成本。
  （数字随每次构建浮动，以 `npm run build` 输出为准）
- **不做全量注册**：改成 `app.use(naive)` 全量注册会让产物显著变大
  （历史测量：gzip 442 kB），因此用具名导入 + 显式注册表。
  因此改为具名导入 + 显式注册表（`web/src/theme/components.ts`）。
- **视觉不跟随默认风格**：通过 `themeOverrides` 对齐既有的极简灰阶，
  保留「macOS 系统设置页」的克制感。换库时只需重写 `web/src/theme/`。
- **数据语义仍由自有组件承担**：`StatBlock` / `Callout` 保留自研，
  因为组件库没有「不可用 ≠ 值为 0」的表达能力（见下节「数据质量原则」）。

## 架构一览

```text
浏览器（默认仅 127.0.0.1）
    │ HTTP + 严格 Host/Origin 校验（远程模式需登录）
    ▼
Go Server（非特权账户 _macpanel）
    │ REST API + 内嵌前端 + SQLite(WAL) + 审计
    ▼ Unix Socket（长度前缀帧 + 共享令牌 + 对端 UID 校验 + 防重放）
Go Root Agent（root，launchd 系统服务）
    │ 固定方法白名单，无 exec / 无任意文件读写
    ▼
macOS：/dev/pf、sysctl、网络栈
```

详见 [`docs/architecture.md`](docs/architecture.md)。

## 快速开始（开发环境）

### 前置

- macOS（Apple Silicon 已验证）
- Go 1.24+（本项目用 1.27.3 验证）
- Node.js 20+ 与 npm

```bash
# 安装依赖（Go 模块 + 前端包）
make deps
```

### 构建与运行

```bash
# 构建二进制（含前端）
make build

# 生成开发用 IPC 令牌（一次即可）
make dev-token

# 终端 1：启动 Agent（开发模式下以当前用户运行，非 root）
make dev-agent

# 终端 2：启动 Server
make dev-server

# 打开 http://127.0.0.1:7788
```

开发产物全部落在 `.dev/`（数据、日志、Socket、令牌），不会污染系统目录。

### 命令行诊断

```bash
make dev-status                              # 人类可读
./bin/macpanel status -dev --json     # 机器可读
./bin/macpanel config -dev            # 打印生效配置
```

`status` 在 Agent 不可达时不报错退出，而是逐项打印不可达原因。

### 防火墙本地救援（不经 IPC）

`firewall` 子命令刻意**不连接 Web Server 与 Unix Socket**：
规则一旦把 SSH 挡在外面，面板那条链路本身就断了，而这两条命令还能用。

```bash
sudo /usr/local/bin/macpanel firewall status     # 纯只读：PF 状态 + 事务 + 倒计时
sudo /usr/local/bin/macpanel firewall rollback   # 回滚当前事务（幂等）
sudo /usr/local/bin/macpanel firewall unload     # 无条件清空本应用 anchor

# 非 root 自动进入 dry-run，可用于预演
./bin/macpanel firewall status -dev
```

`rollback` 与 `unload` 的区别是刻意保留的两种语义：

- `rollback` 取消**当前事务**，没有事务时报错（不掩盖「状态文件已失效」这个事实）；
- `unload` 只回答「让本应用不再持有任何 PF 规则」，与当前阶段无关。

后者存在的原因是：倒计时定时器活**在 Agent 进程里**，Agent 若在规则加载后被杀，
超时不会自动触发任何回滚。详见 [`docs/recovery.md`](docs/recovery.md) 第 3.8 节。

### 前端热重载开发

```bash
make dev-server      # 终端 1：后端 7788
make web-dev         # 终端 2：Vite 5173，/api 自动代理到 7788
```

## 持续集成 (CI)

仓库内置 GitHub Actions（`.github/workflows/ci.yml`），在每次推送到 `main` 和所有 PR 上运行：

- **Build**：在 `macos-latest` 上原生编译 **darwin/arm64**（Apple Silicon）二进制，并上传为制品 `macpanel-darwin-arm64`。
  本项目是 macOS 专属（`internal/platform/darwin` 仅在 darwin 下编译），且二进制内嵌前端产物，故必须在 macOS runner 上原生构建。
- **Lint & Test**：运行 `make check`（go vet + go test + 前端类型检查）。

> 仅编译、不需要测试时，删除 `test` job 即可；`macos-latest` 需要 GitHub 的 macOS 运行器（公开仓库默认可用）。

## 打包与安装

```bash
make pkg           # 构建 PKG（不安装）
make verify-pkg    # 静态校验包结构（不需要 root，不会安装任何东西）
make dmg           # 构建 DMG 分发容器
```

`verify-pkg` 检查脚本语法、plist 合法性、占位符是否残留、Payload 文件清单与权限、
launchd 运行身份等 30 项。它**不能替代**真机安装，但能把「卸载脚本忘了进包」
这类错误从「装一次才发现」压缩到一秒。

安装与卸载：

```bash
sudo installer -pkg MacPanel-<version>.pkg -target /
sudo macpanel-uninstall            # 交互式，数据与日志默认保留
sudo macpanel-uninstall --purge    # 连数据与日志一起删除
```

`preinstall` 会把数据库、令牌与 PF 现状备份到
`/Library/Application Support/MacPanel/backups/preupgrade-<时间戳>/`，
只保留最近 5 份。

## 常用命令

```bash
make test            # 全部 Go 测试
make test-race       # 竞态检测
make test-cover      # 覆盖率
make check           # fmt + vet + test + 前端类型检查
make pkg             # 构建 PKG（不安装）
make verify-pkg      # 静态校验包结构
make dmg             # 构建 DMG（不挂载）
```

## 启用防火墙规则写入

Agent 以 root 运行时才会真正调用 pfctl。开发环境（`-dev` 或非 root）自动进入
**dry-run 模式**：完整走通生成、校验、状态机与界面，但不触碰系统防火墙。

```bash
# 开发模式（dry-run，安全）
make dev-agent

# 真实模式（需 root，会实际修改 PF）
sudo ./bin/macpanel agent
```

## 目录结构

```text
.
├── cmd/macpanel/          统一入口（server / agent / status / token / config）
├── internal/
│   ├── model/                    数据结构与错误码（不依赖其它内部包）
│   ├── config/                   配置加载、校验、IPC 令牌
│   ├── database/                 SQLite 连接与版本化迁移
│   ├── ipc/                      Unix Socket 协议、对端身份验证、客户端
│   ├── agent/                    Root Agent 的方法注册表与实现
│   ├── app/                      Server 侧应用服务层（IPC 的类型化封装）
│   ├── server/                   HTTP 路由、中间件、处理器
│   ├── auth/                     密码哈希、会话、登录限速
│   ├── audit/                    审计写入与查询
│   ├── logger/                   结构化日志
│   ├── platform/                 系统交互接口
│   │   ├── execx/                受控命令执行（白名单 + 超时 + 输出上限）
│   │   ├── darwin/               macOS 只读采集实现
│   │   └── fake/                 模拟实现（开发/测试）
│   └── version/                  构建期注入的版本信息
├── web/                          Vue 3 + TypeScript + Vite + Naive UI + Tailwind v4
│   ├── src/{api,components,layouts,lib,pages,stores}
│   ├── src/theme/                组件库主题覆盖、明暗模式、按需注册的组件表
│   ├── dist/                     构建产物（被 go:embed 打包）
│   └── embed.go                  前端资源嵌入
├── packaging/macos/              uninstall.sh、launchd 模板、PKG/DMG 构建脚本
├── scripts/                      verify-pkg.sh（包结构静态校验）
├── docs/                         架构、安全、API、恢复、测试与各里程碑验收
├── Makefile
└── PLAN.md                       产品计划与 ADR
```

## 安全要点（摘要）

| 项 | 实现 |
| --- | --- |
| 默认监听 | `127.0.0.1:7788`；非回环地址需显式 `MACPANEL_ALLOW_REMOTE=true` |
| IPC 身份 | `LOCAL_PEERCRED` 对端 uid 校验 + 32 字节共享令牌（常量时间比较） |
| IPC 防重放 | ±60 秒时间窗 + 唯一请求 ID |
| 命令执行 | 仅 5 个白名单绝对路径，不经 shell，3 秒超时，输出上限 1 MiB |
| 密码 | scrypt（N=2^15, r=8, p=1）+ 每用户 16 字节盐，最短 12 字符 |
| 会话 | 只存 SHA-256 哈希，`HttpOnly` + `SameSite=Strict`，空闲 2h / 绝对 8h |
| CSRF | Origin/Referer 校验 + 远程模式强制 `X-HLM-Request` 头 |
| DNS rebinding | Host 头白名单校验，不匹配返回 421 |
| 审计 | 记录 actor/action/target/result，不含密码、令牌、请求体 |

详见 [`docs/security.md`](docs/security.md)。威胁模型列出 17 类威胁与对应控制措施。

## 数据质量原则

本项目最硬的一条约束：**不伪造状态**。

- 读不到的指标标记 `unavailable` + 原因，不填 0；
- PF 状态用 `*bool`，`null` 表示「无法确认」，绝不等于 `false`（已关闭）；
- 采集整体失败返回 `503`，不返回部分数据；
- 前端把「不可用」与「值为 0」分开渲染；
- 未实现功能返回 `501`，能力声明 `features.*` 为 `false`。

## 已知限制

1. **PF 状态需 root**：以普通用户运行 Agent 时，PF 状态显示「不可读」而非「已关闭」。
2. **`/etc/pf.conf` 与 anchor 文件通常不可读**（0600 root:wheel）：只能判断存在性，
   无法确认内容。因此 `anchor.referenced=false` 不代表「确认未引用」。
3. **内存口径**：`已用 = 总量 − (free + speculative + purgeable + inactive)`，
   接近「活动监视器」但不等价，页面已标注。
4. **APFS 容量**：根卷与数据卷共享容器，数值可能相同，属系统真实语义。
5. **安装包未签名**：Gatekeeper 会拦截，需手动放行或自行签名。
6. **AppleDouble 残留**：本机构建环境会给 payload 打上删不掉的 `com.apple.provenance`，
   导致 BOM 里多出 `._xxx` 条目。安装只是多几个隐藏文件，卸载脚本会清理；
   换到 CI / 干净构建机打包则不再出现（`make verify-pkg` 会提示）。
7. **前端无自动化测试**：仅有 TypeScript 类型检查 + 脚本化浏览器验证
   （对比度、溢出、交互流程均已脚本化检查，见 `docs/testing.md`）。
8. **PF 写入未实机验证**：M2 的 `pfctl` 调用路径已完整实现并有 180 个测试覆盖逻辑层，
   但**真实加载未在本机执行**（无 root）。首次使用前请按
   `docs/m2-acceptance.md` 第 5 节逐项验证，并保留可用的本地终端。
9. **回滚是「清空 anchor」**：不恢复上一份规则内容（本应用只持有历史哈希，
   不复制规则文件以缩小敏感面）。如需恢复历史规则，需重新保存并应用。
10. **组件库体积**：Naive UI 按需注册后仍占 gzip 约 212 kB，是整包的主要体积来源。
11. **组件库需手动登记**：新增 Naive UI 组件必须在 `web/src/theme/components.ts`
    补一行，否则模板中会解析失败（控制台 `Failed to resolve component`）。

## 文档

| 文档 | 内容 |
| --- | --- |
| [docs/architecture.md](docs/architecture.md) | 进程结构、依赖方向、IPC 协议、数据模型、时序 |
| [docs/security.md](docs/security.md) | 信任边界、17 类威胁与对策、认证模式、DoS 防护 |
| [docs/api.md](docs/api.md) | 全部 HTTP 接口、错误码、数据质量语义 |
| [docs/recovery.md](docs/recovery.md) | 故障诊断、CLI 救援、密码重置、卸载 |
| [docs/testing.md](docs/testing.md) | 测试矩阵与实机验收清单 |
| [docs/m1-acceptance.md](docs/m1-acceptance.md) | M1 验收记录 |
| [docs/m2-acceptance.md](docs/m2-acceptance.md) | M2 验收记录（含必须实机验证的清单） |
| [docs/m3-m4-m6-acceptance.md](docs/m3-m4-m6-acceptance.md) | M3/M4/M6 验收记录（已验证 / 未验证清单） |
| [docs/m5-acceptance.md](docs/m5-acceptance.md) | M5 验收记录与逐条实机测试手册 |

准备首次部署时，按 `docs/m5-acceptance.md` 第 3 节从头执行一遍即可，
它已经把 `m2-acceptance.md` 与 `m3-m4-m6-acceptance.md` 里优先级最高的
交叉项合并到了同一份清单。

## 许可

见 [LICENSE](LICENSE)。
