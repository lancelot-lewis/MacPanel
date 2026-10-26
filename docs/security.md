# 安全设计与威胁模型

> 对应里程碑：M0 / M1
> 最后更新：2026-10-26

## 1. 信任边界

```text
┌── 完全不可信 ──────────────────────────────┐
│ 任意网页（跨站请求）、局域网/公网主机        │
└────────────────────┬───────────────────────┘
                     │ 必须穿越：Host 校验、Origin 校验、
                     │ 认证（远程模式）、限速
┌────────────────────▼───────────────────────┐
│ 本机任意进程（与 Service 同 UID 或更低权限）  │
└────────────────────┬───────────────────────┘
                     │ 必须穿越：IPC 对端 UID 校验 + 共享令牌
                     │ + 时间窗 + 唯一 ID
┌────────────────────▼───────────────────────┐
│ 已授权：Server（_macpanel，持有令牌）         │
│        CLI（同一令牌文件可读）                │
└────────────────────┬───────────────────────┘
                     │ 固定方法白名单，无 exec / 任意文件读写
┌────────────────────▼───────────────────────┐
│ Root Agent —— 唯一可触达系统特权面            │
└────────────────────────────────────────────┘
```

**关键判断**：Socket 路径权限只能说明「谁可能连上来」，不能说明「谁真的连上来了」。
因此身份判定基于内核提供的对端凭据（`LOCAL_PEERCRED` / `SO_PEERCRED`），
再叠加不可猜测的共享令牌区分「同 UID 的其它进程」。

## 2. 资产与威胁

| 资产 | 泄露后果 | 篡改后果 |
| --- | --- | --- |
| Root Agent 的 RPC 接口 | — | **严重**：可修改防火墙/重启/操作 launchd |
| IPC 共享令牌 | 可冒充 Server 调用 Agent | 同左 |
| 管理密码 / 会话 cookie | 可远程管理面板 | 可执行高风险操作 |
| PF 规则文件 | 泄漏网络拓扑与开放端口 | **严重**：可导致失联或暴露服务 |
| 审计日志 | 泄漏操作历史 | 掩盖入侵痕迹 |

### 威胁与对策

| # | 威胁 | 对策 | 位置 |
| --- | --- | --- | --- |
| T1 | 同机低权限进程直连 Socket 调用 Agent | 对端 euid 白名单 + 共享令牌（0600/0640，仅 root 与 Server 组可读） | `ipc/server.go` `ipc/peer*.go` |
| T2 | 窃取令牌后重放旧请求 | 请求带 `issuedAt`（±60 秒窗口）+ 唯一 `id`，重复 ID 拒绝并定期 GC | `ipc/server.go:checkReplay` |
| T3 | 超长帧拖垮内存 | 先读 4 字节长度再按上限校验，绝不按声明长度预分配 | `ipc/frame.go:readFrame` |
| T4 | 协议字段拼写错误被静默忽略 | `DisallowUnknownFields` 严格解析 | `ipc/frame.go:decodeStrict` |
| T5 | 跨站请求伪造（CSRF）打本地面板 | 变更方法校验 Origin/Referer；远程模式额外强制自定义头 `X-HLM-Request` + `SameSite=Strict` | `server/middleware.go` `server/auth_middleware.go` |
| T6 | DNS rebinding 绕过同源 | 严格校验 Host 头，只允许配置的监听地址 / localhost / 回环 | `server/middleware.go:hostGuard` |
| T7 | 暴力破解管理密码 | 每来源地址 15 分钟 5 次失败上限；失败统一返回「密码错误」不区分原因 | `auth/auth.go:checkRate` |
| T8 | 数据库泄露导致密码外泄 | scrypt（N=2^15, r=8, p=1）+ 每用户 16 字节随机盐 | `auth/auth.go:hashPassword` |
| T9 | 会话令牌泄露 | 只存 SHA-256 哈希；`HttpOnly` + `SameSite=Strict` + 空闲 2 小时/绝对 8 小时过期 | `auth/auth.go` |
| T10 | 命令注入 | 全部外部命令以数组传参、不经 shell、仅白名单绝对路径 | `platform/execx` |
| T11 | 系统命令卡死拖垮 Agent | 每条命令 3 秒超时 + 输出上限 1 MiB | `platform/execx` |
| T12 | 攻击者篡改密码哈希降低强度（或抬高强度 DoS） | 校验 scrypt 参数范围：N ∈ [2^12, 2^18], r ∈ [1,32], p ∈ [1,4] | `auth/auth.go:verifyHash` |
| T13 | 会话被劫持后攻击者改密码 | 改密码必须提供当前密码；改密码后撤销所有会话 | `server/auth_handlers.go` |
| T14 | 误删非 Socket 文件 | 启动时若路径已存在且非 Socket，直接拒绝启动，绝不删除 | `ipc/server.go:removeStaleSocket` |
| T15 | 两个 Agent 实例并存导致状态混乱 | 检测到 Socket 已被监听时拒绝启动（先尝试连接探测） | `ipc/server.go` |
| T16 | 内部错误信息泄漏路径/凭据 | `model.CodeInternal` 一律对外替换为「服务器内部错误」，细节只进日志 | `server/respond.go` |
| T17 | 审计被窃取凭据 | 审计只记录 actor/action/target/result，不记录请求体、密码、令牌 | `audit/store.go` |

## 3. 认证模式

### 本地模式（默认）

- 监听 `127.0.0.1`（配置非回环地址会在校验阶段直接报错）。
- 来自回环地址的请求视为已授权；非回环请求一律 403。
- 不做登录，避免为本机场景引入不必要的摩擦。
- **仍然**执行 Host 与 Origin 校验，因此恶意网页无法通过浏览器驱动本机 API。

### 远程模式（显式开启）

必须同时满足：

1. `MACPANEL_ALLOW_REMOTE=true`（否则非回环监听校验失败）；
2. 请求携带 `X-HLM-Request: 1` 自定义头（跨站无法在不触发预检的情况下附加）；
3. 携带有效会话 cookie，通过 `ValidateSession`；
4. 建议置于 HTTPS 反向代理后（cookie 的 `Secure` 由 `r.TLS` 或 `X-Forwarded-Proto` 决定）。

启用远程前**必须**先设置管理密码（≥12 字符）。未设置时登录接口一律失败，且登录页
引导用户到本机设置密码——不在远程端开放「免认证设置初始密码」的入口。

## 4. Agent 权限最小化

### 4.1 白名单命令与参数级限制

Agent 可执行的系统命令只有 8 个，每个都有明确用途：

| 命令 | 用途 | 参数级守卫 |
| --- | --- | --- |
| `/usr/sbin/sysctl` | 读取内核与硬件参数 | 无（只读命令） |
| `/usr/bin/vm_stat` | 虚拟内存统计 | 无 |
| `/usr/bin/sw_vers` | macOS 版本 | 无 |
| `/usr/sbin/networksetup` | 网络硬件端口列表 | 无 |
| `/sbin/pfctl` | PF 查询与 anchor 加载 | `pfctlGuard` |
| `/bin/launchctl` | 服务查询与受管启停 | `launchctlGuard` |
| `/sbin/shutdown` | 受控重启 | `shutdownGuard` |
| `/usr/bin/plutil` | plist 语法校验 | `plutilGuard` |
| `/usr/sbin/netstat` | 路由与连接查询 | `netstatGuard` |

**只有路径白名单是不够的**——`pfctl` 既能 `-a <anchor> -f <file>`（安全），
也能 `-d`（全局关闭防火墙）。因此危险命令都有参数级守卫。

### 4.2 各守卫的明确禁止项

`pfctlGuard`：禁 `-d`（关闭 PF）、`-e`、`-F`（清空规则表）、`-k`（终止全部状态）、
`-x`、`-z`、`-m`、`-N`、`-O`。只允许 `-s info/Anchors`、`-n -f`、
`-a <anchor> -f`、`-a <anchor> -sr`。

`launchctlGuard`：**永久关闭** `submit`（以 root 执行任意命令）、
`load`/`unload`（旧式接口）、`reboot`/`shutdown`/`halt`、
`setenv`/`unsetenv`（影响所有服务）、`config`、`attach`。
改变状态的子命令（`bootstrap`/`bootout`/`kickstart`/`enable`/`disable`）
**只允许 system 域**；只读的 `print` 可查任意域（诊断需要）。

`shutdownGuard`：只允许 `-r now` 与 `-r +N`（1-60 分钟）。
**关机（-h）与断电（-p）永久缺席**——若机器不在物理可及范围，
误关机等于彻底失联，而本应用不提供服务器的远程开机能力。

`netstatGuard`：只允许 `-rn`/`-an` 配合 `-f inet/inet6`，禁 `-d`（删除路由）。

`plutilGuard`：只允许 `-lint`（语法校验），不允许 `-replace`/`-insert` 改写文件。

### 4.3 明确不做

`pfctl -d`、`pfctl -f /etc/pf.conf`（全量刷新主规则集）、任意文件写入、
任意命令执行、关机/断电、容器写操作、加载任意 plist。

### 4.4 服务启停的三层拦截

见 `docs/architecture.md` 7.3。核心是「不以路径信任调用方」：
`bootstrap` 只接受**登记时记录的** plist 路径，不接受请求传入——
否则等于开放「以 root 加载任意 plist」。

### 4.5 为什么容器能力只读

Docker Socket 的权限等价于 root：能创建特权容器、挂载宿主机任意目录。
本应用只发 `GET /containers/json`，且不提供 `inspect`（会返回环境变量，
常含数据库密码、API Key）。

容器的启停需要在后续版本单独设计授权与审计，不能顺手加上——
那会让「面板的实际权限」悄悄超过「设计时承诺的权限」。

## 5. 拒绝服务防护

| 维度 | 限制 | 位置 |
| --- | --- | --- |
| IPC 并发连接 | 16 | `ipc.MaxConns` |
| IPC 请求帧 | 64 KiB | `ipc.MaxRequestBytes` |
| IPC 响应帧 | 4 MiB | `ipc.MaxResponseBytes` |
| 单连接请求数 | 8 | `ipc/server.go` |
| HTTP 请求体 | 64 KiB | `server.maxJSONBody` |
| HTTP 请求头 | 16 KiB | `http.Server.MaxHeaderBytes` |
| HTTP 读头/读/写/空闲超时 | 10s / 30s / 60s / 120s | `cmd/macpanel` |
| 系统命令超时 | 3 秒 | `darwin.cmdTimeout` |
| 系统命令输出 | 1 MiB | `execx.MaxOutputBytes` |
| 登录失败 | 5 次 / 15 分钟 / 地址 | `auth.MaxLoginFailures` |
| 审计查询条数 | 1–500 | `server/handlers.go` |

## 6. 凭据存储

| 凭据 | 存储位置 | 权限 | 形式 |
| --- | --- | --- | --- |
| 管理密码 | SQLite `settings` | 随数据库文件 | scrypt 哈希（`scrypt$N$r$p$salt$hash`） |
| 会话令牌 | SQLite `auth_sessions` | 随数据库文件 | 客户端持有明文，DB 只存 SHA-256 |
| IPC 共享令牌 | `ipc.token` 文件 | `0600`（安装器设 `0640 root:_macpanel`） | 32 字节随机，十六进制表示 |

令牌文件由 `config.WriteTokenFile` 以「创建临时文件 → chmod 0600 → 写入 → fsync → rename」
的原子方式写入，避免生成半截文件。

## 7. 安全响应头

所有响应统一附加：

```text
X-Content-Type-Options: nosniff
X-Frame-Options: DENY
Referrer-Policy: no-referrer
Cross-Origin-Opener-Policy: same-origin
Cross-Origin-Resource-Policy: same-origin
Permissions-Policy: geolocation=(), microphone=(), camera=()
Content-Security-Policy: default-src 'self'; script-src 'self';
    style-src 'self' 'unsafe-inline'; img-src 'self' data:;
    font-src 'self'; connect-src 'self'; frame-ancestors 'none';
    base-uri 'self'; form-action 'self'
```

`style-src` 允许 `'unsafe-inline'` 是因为 Vue 单文件组件与动态 `:style` 绑定需要；
`script-src` 严格只允许同源打包产物，不含 `'unsafe-inline'` 或 `'unsafe-eval'`。
API 响应一律 `Cache-Control: no-store`。

## 8. 尚未覆盖的风险

1. **远程模式的 TLS 终止**：当前假设由反向代理提供 HTTPS。若用户直接暴露
   HTTP 端口，凭据会明文传输。M5 需提供内置 TLS 或强制校验机制。
2. **密钥轮换**：IPC 令牌目前需手动重新生成（`token -force`），
   会导致 Server 与 Agent 短暂不一致，需要协同重启。
3. **审计日志防篡改**：当前为普通表，root 或被授权进程可修改。
   未来可考虑追加哈希链或外部日志转发。
4. **签名与公证**（M5）：当前 PKG 未签名，Gatekeeper 会拦截。
5. **多用户**：明确不支持 RBAC，仅区分「已授权/未授权」。
6. **服务启停的「关键通道」识别**：本应用不做「这个服务是否是远程通道」
   的智能判断，而是**统一要求风险确认**。误判的代价是失联，
   而统一确认的代价只是多点一次。
7. **TCP 探测可被用作内网探测**：默认限本机地址，但若开启
   `MACPANEL_ALLOW_NONLOCAL_PROBE`，面板就具备端口扫描能力。
   该开关必须在理解这个后果的前提下开启。
8. **OrbStack 容器信息暴露**：容器名与镜像名可能透露架构信息。
   接口只对已认证用户开放，但若账号泄露，这些信息会一并暴露。

## 9. 安全测试覆盖

自动化测试中与安全直接相关的用例：

### 命令层

| 测试 | 验证 |
| --- | --- |
| `TestPFctlGuardRejectsDangerousFlags` | `-d/-e/-F/-k/-x` 全拒绝，含混在合法参数中的情况 |
| `TestLaunchctlGuardRejectsDangerousSubcommands` | `submit`/`load`/`unload`/`reboot`/`shutdown`/`halt`/`setenv` 全拒绝 |
| `TestLaunchctlGuardRejectsNonSystemDomainForWrites` | bootstrap/bootout/kickstart/enable/disable 只允许 system 域 |
| `TestShutdownGuardOnlyAllowsReboot` | `-h`/`-p`/`-s` 全拒绝 |
| `TestRunRejectsGuardViolation` | 守卫确实被 `Run` 调用（不只是函数存在） |

### 服务操作

| 测试 | 验证 |
| --- | --- |
| `TestControllerRejectsCriticalServices` | 系统关键与本应用自身在控制器层也被拒绝 |
| `TestUpsertRejectsInvalid` | 非法 plist 路径（`/tmp/`、含 `..`、文件名不一致）全拒绝 |
| `TestValidateLabelForControlRejectsCritical` | 形态校验独立生效 |

### 调度与电源

| 测试 | 验证 |
| --- | --- |
| `TestScheduleRebootRejectsWithoutCapability` | 非 root 明确拒绝而不是「已排定但不生效」 |
| `TestDecideEnforcesMinInterval` | 最短间隔阻止循环重启 |
| `TestRebootRequiresRiskConfirmation` | 缺确认 400 且不触达 Agent |
| `TestPowerWriteRequiresAuth` | 远程模式未登录 → 401 |

### 诊断

| 测试 | 验证 |
| --- | --- |
| `TestValidateProbeTarget` | 限制模式拒绝外部地址 |
| `TestNoWriteOperations` | OrbStack 客户端只暴露 2 个只读方法（编译期断言） |

其余用例：

| 测试 | 验证内容 |
| --- | --- |
| `TestUnknownMethodRejected` | 未注册方法（如 `system.exec`）被拒绝 |
| `TestWrongTokenRejected` | 错误令牌 → `unauthorized` |
| `TestReplayRejected` | 重复请求 ID 被拒绝 |
| `TestStaleTimestampRejected` | 超窗时间戳被拒绝 |
| `TestFrameTooLargeRejected` | 超长帧被拒绝且不预分配内存 |
| `TestSocketPermissions` | Socket 0660 / 目录 0750 |
| `TestStaleSocketRefusedWhenLive` | 已有实例时拒绝重复启动 |
| `TestNonSocketPathNotRemoved` | 非 Socket 文件不被删除 |
| `TestDecodeStrictRejectsUnknownFields` | 未知字段被拒绝 |
| `TestRejectsUnlistedBinary` | 白名单外命令一律拒绝 |
| `TestHandlerErrorPropagated` | 内部错误细节不泄漏 |
| `TestHostGuardRejectsForeignHost` | DNS rebinding 防护（421） |
| `TestOriginGuardRejectsCrossSite` | 跨站写请求被拒绝（403） |
| `TestRemoteModeRequiresAuth` | 远程未登录 → 401 |
| `TestRemoteModeRequiresCustomHeader` | 缺 `X-HLM-Request` → 403 |
| `TestLoginRateLimited` | 连续失败触发 429 |
| `TestPasswordHashNotPlaintext` | DB 中无明文密码 |
| `TestSessionTokenNotStoredPlaintext` | DB 中无明文会话令牌 |
| `TestChangePasswordRevokesSessions` | 改密码撤销旧会话 |
| `TestSetPasswordRequiresCurrentPassword` | 改密码需当前密码 |
| `TestVerifyHashRejectsTamperedParams` | 篡改哈希参数被拒绝（防 DoS） |
| `TestAuditDoesNotLogSecrets` | 审计不含密码 |
| `TestConfig*`（多个） | 默认只监听回环、非回环需显式开启 |
