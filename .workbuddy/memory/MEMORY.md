# MacPanel（HomeLab Manager）项目长期记忆

## 产品与路线

macOS HomeLab 管理面板。**ADR-001 已决策：Go + Vue 3 Web-first，
不引入 Wails / Tauri / Rust**。理由：PF 规则、计划重启、launchd 服务必须在浏览器
关闭、用户注销后继续工作，桌面壳无法替代独立的 Root LaunchDaemon。

边界：默认只监听 `127.0.0.1`；远程访问需显式开启并完成认证/HTTPS 验收。

## 前端：Naive UI（2026-10-26 用户决定）

用户主动选择引入 Naive UI，理由是 **M2 的重表单**（"长痛不如短痛"）。
不要退回纯手写方案，也不要换成别的库。

必须遵守的约定：

- **按需注册，不要 `app.use(naive)`**：全量注册产物 gzip 442 kB，
  按需后 243 kB（naive 187 + vue 42 + 业务 19）。注册表在
  `web/src/theme/components.ts`，**新增组件必须补一行**。
- **`theme/` 是组件库唯一入口**：业务页面只用 CSS 变量
  （`var(--color-*)`）或组件的 `type` 属性，不直接 import 主题变量。
- **明暗模式只有一个真源** `theme/mode.ts`：同时驱动 `html.dark` 与
  `NConfigProvider :theme`。两套各自判断 `prefers-color-scheme` 会撕裂
  （卡片深色、底色浅色）。

### 组件库踩坑（都实际发生过）

| 现象 | 原因 |
| --- | --- |
| 页面全白且只 warn 不报错 | `NCard.name === "Card"`，`N` 前缀只在导出名上。不能用 `c.name` 注册 |
| 产物 1584 kB | `app.use(naive)` 全量注册 |
| 深色模式底色撕裂 | CSS 变量与组件库各自判断系统偏好 |
| 大量对比度不达标 | 库把语义色同时用作文字色和 12% 透明底 |
| `type="primary"` 的 Tag 覆盖无效 | 该类型走 `primaryColor`，不受 `Tag.textColor` 影响；改用 `type="info"` |
| 移动端侧栏不隐藏 | `NLayoutSider` 自身是 flex 元素，`hidden/lg:block` 被组件样式覆盖；用外层 div |

### 对比度（WCAG AA 4.5:1，别信默认值）

组件库默认值实测不达标，已在 `themeOverrides` 修正：
绿 `#1f9d55`(3.05)→`#146b3b`(5.6)、橙 `#d98a00`(2.25)→`#7a4f00`(6.2)、
蓝 `#2f6fed`(3.89)→`#1d4fb5`(6.4)、三级文字浅色 `#666c76`(4.9)/深色 `#8b93a0`(5.2)。
`placeholder` 与必填星号需在 `style.css` 用 `!important` 修补。
品牌标识白字用固定 `--color-brand-mark: #2359c9`，不能用会随主题变亮的 accent。

**检测对比度必须逐层叠加半透明背景**，否则半透明底会被当成实色，
产生大量假阳性（第一版脚本就报了 20+ 个假问题）。

## 最强的产品约束：不伪造状态

这条约束贯穿三层，改动任何一层都要保持一致：

1. **model 层**：布尔状态用 `*bool`，`null` = 无法确认，**绝不等于 `false`**。
   采集失败写 `missing: [{metric, reason}]`。
2. **API 层**：存在缺失项 → `meta.degraded=true` + `meta.notes`；
   采集整体失败 → `503`，不返回部分数据；未实现 → `501 not_implemented`。
3. **前端层**：「不可用」与「值为 0」分开渲染；失败时保留旧数据但显著标注
   「可能已过期」。

`FirewallStatus.Writable` 在 M1 恒为 `false`，前端据此隐藏写操作入口。

## 关键架构约定

- 依赖方向：`cmd → server → app → ipc → model`；`model` 不依赖任何内部包。
- HTTP handler **不直接执行系统命令**，一律 `app.Service → ipc.Client → Agent`。
- 外部命令只走 `internal/platform/execx`（白名单 5 个绝对路径 + 数组传参
  + 3s 超时 + 1MiB 输出上限）。
- Agent 只有固定方法注册表，**不存在** `exec` / 任意文件读写 / 任意 PF 语句入口。
- 读接口显式拒绝任何参数（`requireNoParams`），避免将来加参数时静默兼容。

## IPC 身份验证（不要简化成「Socket 路径信任」）

三层叠加，缺一不可：
1. 内核对端凭据：darwin `LOCAL_PEERCRED`（`unix.GetsockoptXucred`，注意
   `solLocal=0x0` / `localPeerCred=0x001` 需自行声明，x/sys 未导出）/
   linux `SO_PEERCRED`；
2. 32 字节共享令牌，`subtle.ConstantTimeCompare` 比较；
3. ±60s 时间窗 + 唯一请求 ID 防重放。

平台不支持读凭据时**一律拒绝**，不做「读不到就放行」的降级。

## 认证授权条件（有过死锁教训）

`/auth/password` 注册在认证中间件之外，因此 handler 内 `sessionFrom(ctx)`
**永远为空**，必须用 `hasValidSession()` 手动校验 cookie。

按「是否已设置密码」分流：
- 未设置（初始化）→ **只允许回环地址**（否则远程模式死锁：登录要密码、设密码要登录）
- 已设置 + 远程模式 → 必须已登录 + `currentPassword` 正确
- 已设置 + 本地模式 → 必须是本机 + `currentPassword` 正确

## 本机环境注意

- Go 由 mise 管理，PATH 里没有时加 `~/.local/share/mise/shims`
- `sudo` 在沙箱不可用；有 `HTTP_PROXY`，curl 测本机端口要 `--noproxy '*'`
- 命令路径：`pfctl` 在 `/sbin`，`vm_stat` 在 `/usr/bin`
- `sysctl -n vm.page_inactive_count` 在 macOS 27 不存在，内存统计走 `vm_stat`

## 常用命令

```bash
export PATH="$HOME/.local/share/mise/shims:$PATH"
make build          # 含前端
make build-go       # 仅后端（迭代时用）
make test / test-race / test-cover / check
./bin/homelab-manager token -dev -force    # 生成 .dev 令牌
./bin/homelab-manager agent -dev           # 终端 1
./bin/homelab-manager server -dev          # 终端 2 → http://127.0.0.1:7788
```

## 里程碑状态

- M0 文档、M1 只读面板：**已完成**（见 `docs/m1-acceptance.md`）
- M2 PF 核心（规则生成/校验/应用/回滚）：待开始
- 前置待办：确认 root 下 PF 读取、查明既有 `/etc/pf.anchors/com.homelab.ipv6`
  的来源（**不是本项目创建**）、设计回滚状态机持久化
