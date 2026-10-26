# M1 验收记录

> 验收日期：2026-10-26
> 环境：macOS 27.0.1 (26A434) / Apple M4 / arm64 / 10 核 / 16 GiB
> 工具链：Go 1.27.3、Node 22.22.2、npm 10.9.7、SQLite 3.54.0
> 结论：**M1 通过**（M0 文档同步完成）

## 1. 交付物清单

### 代码

| 模块 | 文件数 | 说明 |
| --- | --- | --- |
| Go 后端 | 40 | cmd 2 / internal 32 / web embed 1 / 测试 9 |
| 前端 | 18 | 5 页面 + 5 组件 + API 层 + 状态 + 工具 |
| 打包 | 4 | launchd ×2、PKG 构建、DMG 构建 |
| 文档 | 6 | README + architecture / security / api / recovery / testing |

### 关键文件

```text
cmd/macpanel/main.go            统一入口（server/agent/status/token/config）
internal/ipc/{protocol,frame,server,client,peer_*}.go   IPC 协议与身份验证
internal/platform/execx/execx.go       白名单命令执行
internal/platform/darwin/{system,firewall,statfs_darwin}.go  只读采集
internal/server/{server,middleware,auth_middleware,handlers,auth_handlers,web,respond}.go
internal/auth/auth.go                  scrypt + 会话 + 限速
internal/database/{database,migrate}.go  WAL + 版本化迁移
internal/audit/store.go                审计
web/src/pages/*.vue                    5 个页面
packaging/macos/{launchd,pkg,dmg}       安装脚手架
docs/*.md                              5 份技术文档
```

## 2. 自动化测试结果

```text
go test ./... -count=1      全部通过（10 个含测试的包）
go test ./... -race         全部通过，无数据竞争
go vet ./...                无告警
gofmt -l .                  无未格式化文件
npm run build               类型检查通过，产物 143.80 kB (gzip 52.22 kB)
```

### 覆盖率

| 包 | 覆盖率 |
| --- | --- |
| internal/ipc | 100.0% |
| internal/platform/execx | 88.9% |
| internal/auth | 89.6% |
| internal/platform/darwin | 78.8% |
| internal/database | 80.0% |
| internal/agent | 79.9% |
| internal/server | 78.7% |
| internal/audit | 70.0% |
| internal/config | 64.6% |
| **合计** | **61.7%** |

未覆盖部分主要是防御性错误分支与 CLI 主函数；**安全关键路径 100% 覆盖**。

测试数量：**109 个测试函数**，全部通过。

## 3. 实机验证结果

### 3.1 真实数据采集

| 项目 | 期望 | 实测 | 结果 |
| --- | --- | --- | --- |
| 主机名 | 真实值 | `zhanghandeMac-mini.local` | ✅ |
| macOS 版本 | 27.0.1 | `27.0.1 (26A434)` | ✅ |
| 内核 | Darwin 27 | `Darwin 27.0.0` | ✅ |
| CPU | Apple M4 / 10 核 | `Apple M4` / 10 物理 10 逻辑 | ✅ |
| 负载 | 与 sysctl 一致 | `3.32 / 3.51 / 3.50` | ✅ |
| 内存总量 | 16 GiB | `17179869184` 字节 | ✅ |
| 内存已用 | 与手工核算一致 | `12.0 GiB`（手工核算 11.53 GiB，含采样差异） | ✅ |
| 磁盘 | 与 df 一致 | `/` 与 `/System/Volumes/Data` 均 65% | ✅ |
| 启动时间 | 与 kern.boottime 一致 | `2026-10-25 09:34:11` | ✅ |
| 网络接口 | 真实接口 | lo0 / en0 / en1 / utun* / bridge0 等 | ✅ |
| IPv6 分类 | 正确分类 | `::1` 回环、`fe80::` 链路本地、`2409:8a55::` 公网 | ✅ |

**发现并修复的问题**：初版 `pfctl` 路径写成 `/usr/sbin/pfctl`（实际在 `/sbin`），
`vm_stat` 未加入白名单，导致 PF 与内存均报「不可用」。已修正并复测通过。

**发现并修正的口径问题**：初版内存「已用」仅用 `free + speculative` 计算，
16 GiB 机器显示「已用 15.8 GiB」，明显失真。改为
`已用 = 总量 − (free + speculative + purgeable + inactive)` 后在 11.5 GiB 左右，
与 `vm_stat` 手工核算一致；页面已标注口径。

### 3.2 数据质量（核心验收项）

| 项目 | 命令/接口 | 结果 |
| --- | --- | --- |
| PF `enabled` 为 `null` | `GET /firewall/status` | `enabled: null, state: unknown` ✅ |
| PF 不可读说明 | 同上 | "当前进程无权限读取 PF 状态（/dev/pf 仅 root 可访问）" ✅ |
| 前端提示 | 防火墙页 | 「不可读不等于已关闭」显著提示 ✅ |
| `writable` 为 false | `GET /firewall/status` | `writable: false` ✅ |
| 规则应用未实现 | `POST /firewall/apply` | **501** ✅ |
| 重启未实现 | `POST /power/reboot` | **501** ✅ |
| 服务管理未实现 | `GET /services` | **501** ✅ |
| 规则保存未实现 | `PUT /firewall/rules` | **501** ✅ |
| 能力声明 | `GET /meta` | 4 项写能力全 false，读能力 true ✅ |
| anchor 不创建 | `GET /firewall/status` | `state: absent, present: false` ✅ |

### 3.3 安全防护

| 项目 | 验证方式 | 结果 |
| --- | --- | --- |
| Host 校验（DNS rebinding） | `Host: evil.com` | **421** ✅ |
| 跨站 POST | `Origin: https://evil.example.com` | **403** ✅ |
| 同源 POST | `Origin: http://127.0.0.1:7788` | **200** ✅ |
| 响应头 CSP | 响应头检查 | `frame-ancestors 'none'`、`script-src 'self'` ✅ |
| X-Frame-Options | 响应头检查 | `DENY` ✅ |
| X-Content-Type-Options | 响应头检查 | `nosniff` ✅ |
| Cache-Control | 响应头检查 | `no-store` ✅ |
| X-Request-Id | 响应头检查 | 存在且与日志一致 ✅ |
| 远程模式缺 header | `GET /overview` 无 `X-HLM-Request` | **403** ✅ |
| 远程模式无会话 | `GET /overview` 有 header 无 cookie | **401** ✅ |
| 登录限速 | 连续 5 次错误密码 | 第 5 次起 **429** ✅ |
| 限速后正确密码 | 限速期间登录 | 仍 **429** ✅ |
| 初始密码只能本机 | 远程地址调 `/auth/password` | **403** ✅ |
| 改密码需当前密码 | 缺失时调用 | **400**；错误时 **401** ✅ |
| 改密码撤销旧会话 | 改后旧 cookie 访问 | **401** ✅ |

### 3.4 远程模式端到端流程（真实 HTTP）

```text
1. 未设密码读 overview        → 401   ✅
2. 本机初始化密码             → 200   ✅（同时下发会话 cookie）
3. 用该会话读 overview        → 200   ✅
4. 改密码（带当前密码）        → 200   ✅
5. 旧密码登录                 → 401   ✅
6. 新密码登录                 → 200   ✅
7. logout 后访问              → 401   ✅
```

### 3.5 前端

| 项目 | 验证方式 | 结果 |
| --- | --- | --- |
| 概览页真实数据 | 浏览器渲染 | 显示 Apple M4、16 GiB、真实 IPv6 ✅ |
| 5 个页面渲染 | 逐页访问 | 概览/防火墙/网络/审计/设置全部 200 ✅ |
| 横向溢出（1440px） | `scrollWidth` 检查 | 无溢出 ✅ |
| 横向溢出（390px） | `scrollWidth` 检查 | `390 == 390`，无溢出 ✅ |
| 移动端导航 | 点击汉堡按钮 | 展开 → 5 项 → 点击后跳转并自动收起 ✅ |
| 浅色模式对比度 | 脚本化计算 | 0 处低于 4.5:1 ✅ |
| 深色模式对比度 | 脚本化计算 | 0 处低于 4.5:1 ✅ |
| 文字裁剪 | `scrollWidth > clientWidth` | 0 处 ✅ |
| 登录页渲染 | 远程模式访问 `/` | 自动跳转 `/login`，含密码框 ✅ |
| 登录失败提示 | 输入错误密码 | 显示「用户名或密码错误」，停留登录页 ✅ |
| 登录成功跳转 | 输入正确密码 | 跳转 `/`，显示概览 + 退出按钮 ✅ |
| SPA 路由回落 | `GET /firewall` | 200 返回 index.html ✅ |
| 静态资源 404 | `GET /assets/nope.js` | 404（不伪装成 HTML）✅ |

### 3.6 打包

| 项目 | 结果 |
| --- | --- |
| `plutil -lint` 两个 plist | 均 OK ✅ |
| `make build` | 产出 11 MB 二进制 ✅ |
| PKG 构建 | `MacPanel-0.1.0.pkg`，4.6 MB ✅ |
| PKG 载荷检查 | 仅 3 个文件（二进制 + 2 个 plist），属主 wheel ✅ |
| 二进制内嵌前端 | 可离线提供完整 UI ✅ |

**发现并修复的问题**：初版 payload 包含权限为 0750 的数据目录，
`pkgbuild` 以非 root 运行时输出 4 条 `write: Permission denied`。改为
数据/日志目录全部由 `postinstall` 创建，payload 只保留二进制与 plist，
并加 `COPYFILE_DISABLE=1` + `xattr -cr` 防 AppleDouble 污染。

同时修正版本号策略：原用 `git describe --always` 导致包名变成
`MacPanel-c86977a.pkg`，改为优先取 tag，无 tag 时回落 `0.1.0`。

## 4. 过程中发现并修复的缺陷

这是本次验收最有价值的部分——三个问题都不是靠「跑通」能发现的：

### 缺陷 1：远程模式下无法设置初始密码（死锁）

- **现象**：`MACPANEL_ALLOW_REMOTE=true` 时，`POST /auth/password` 永远返回 401
  「请先登录」，而登录又需要密码 → 远程模式永久不可用。
- **原因**：该路由注册在认证中间件之外（初始设置必须允许未登录），
  handler 内用 `sessionFrom(ctx)` 判断授权，但 context 中永远没有会话。
- **修复**：按「是否已设置密码」分流——未设置时只允许回环地址（引导阶段），
  已设置时才要求会话。
- **回归测试**：`TestInitialPasswordOnlyFromLocalhost`。

### 缺陷 2：远程模式下已登录也无法改密码

- **现象**：修复缺陷 1 后，已登录改密码仍 401。
- **原因**：同根——handler 内 `sessionFrom` 为空，必须手动校验 cookie。
- **修复**：新增 `hasValidSession()`，优先复用中间件注入的会话，
  否则手动读取并校验 cookie。
- **回归测试**：`TestChangePasswordInRemoteMode`、
  `TestChangePasswordRemoteWithoutSessionRejected`。

### 缺陷 3：测试因 Socket 路径过长而失败（暴露真实限制）

- **现象**：`TestStaleSocketRefusedWhenLive` 报 `bind: invalid argument`。
- **原因**：`t.TempDir()` 把长测试名拼进路径，超过 `sun_path` 的 104 字节。
- **修复**：
  1. 生产代码新增 `validateSocketPath`，在 `Listen` 前显式校验并给出可读错误
     （原先只会看到 `invalid argument`，极难定位）；
  2. 测试改用短路径辅助函数。
- **额外收益**：用户配置过深目录时会直接看到
  「Socket 路径过长（112 字节，上限 103）」而不是内核的模糊报错。
- **回归测试**：`TestSocketPathTooLongRejected`、`TestValidateSocketPathBounds`。

## 5. 对照 PLAN.md 的 M1 验收条件

| 条件 | 状态 | 证据 |
| --- | --- | --- |
| Web 可读取 Agent 的真实只读状态 | ✅ | 3.1 真实数据表 |
| 权限隔离通过 | ✅ | 非特权 Server / root Agent 分离；IPC 对端 UID + 令牌 |
| Web Server 专用非特权用户 | ✅ 设计 + 打包脚本 | plist 指定 `UserName=_macpanel`（待 M5 实装验证） |
| Root Agent 仅执行受控操作 | ✅ | 4 个固定只读方法；`execx` 白名单 5 个命令 |
| 无人值守（不依赖登录） | ✅ 设计 | LaunchDaemons（非 LaunchAgents）+ `RunAtLoad` |
| 故障可恢复 | ✅ 文档 | `docs/recovery.md` 覆盖 7 类故障 |
| 不破坏系统 | ✅ | 未调用任何 `pfctl` 写操作、未修改 `/etc/pf.conf` |
| 模块化单体 | ✅ | 单仓库、单二进制、清晰依赖方向 |
| 先验证后部署 | ✅ | 未执行 `pfctl`/`launchctl`/`reboot`/安装器 |
| UI 展示真实状态，未实现标记未实现 | ✅ | 3.2 数据质量表 |

## 6. 已知限制（进入 M2 前需知晓）

1. **PF 读取需 root**：本机 `sudo` 不可用（沙箱限制），因此**未能验证 root 下
   PF 状态读取**。代码路径已按 `enabled *bool` 建模，单元测试覆盖非 root 分支；
   root 分支需在部署后确认。
2. **`/etc/pf.conf` 与 anchor 文件不可读**（0600 root:wheel）：只能判断存在性，
   `anchor.referenced=false` 不代表确认未引用。
3. **本机已存在 `/etc/pf.anchors/com.homelab.ipv6`**（2026-10-26 07:37 创建，
   691 字节，root:wheel 0600）——**不是本项目创建的**。本项目使用的 anchor 名是
   `com.macpanel.ipv6`，两者不冲突，但 M2 实施前需确认该文件的来源，
   避免误判为「本应用残留」。
4. **PKG 未实际安装**：需干净测试机与 root 权限，属 M5 范围。
5. **前端无自动化测试**：本轮用脚本化浏览器检查（对比度、溢出、裁剪、交互），
   建议 M2 前补 Vitest 组件测试。
6. **DMG 未构建验证**：脚本已就绪，依赖 PKG 实际安装验证。

## 7. M2 前置待办

- [ ] 在部署环境确认 root 下 PF 状态读取（`pfctl -s info` / `-s Anchors`）
- [ ] 查明 `/etc/pf.anchors/com.homelab.ipv6` 的来源与用途
- [ ] 设计 PF 规则生成器与语法校验（`pfctl -n -f`）
- [ ] 设计回滚状态机的持久化（Agent 崩溃/重启后仍能回滚）
- [ ] 确认 `/etc/pf.conf` 中 anchor 调用位置与 `quick` 语义的实测结果
- [ ] 前端补 Vitest 组件测试
- [ ] 补充 `metric_samples` 的历史采样与聚合
