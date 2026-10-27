# MacPanel 项目长期记忆

## 命名（2026-10-27 完成全量统一）

产品名 **MacPanel**（展示名同样写 MacPanel，与 GitHub 仓库名一致）。
旧名 HomeLab Manager / homelab-manager 已全部清除，代码里不要再出现。

| 对象 | 值 |
| --- | --- |
| 二进制 | `macpanel`（`/usr/local/bin/macpanel`） |
| Go module / 仓库 | `github.com/lancelot-lewis/MacPanel` |
| 命令目录 | `cmd/macpanel/` |
| 环境变量前缀 | `MACPANEL_`（旧 `HLM_` 已废弃） |
| launchd label | `com.macpanel.agent` / `.server` / `.task.<id>` |
| 服务账户 | `_macpanel` |
| 安装目录 | `/Library/Application Support/MacPanel`、`/Library/Logs/MacPanel`、`/var/run/macpanel` |
| PF anchor | `com.macpanel.ipv6`（文件 `/etc/pf.anchors/com.macpanel.ipv6`） |
| 安装包 | `MacPanel-<version>.pkg` / `.dmg` |
| 卸载命令 | `macpanel-uninstall` |

**刻意保留的旧名**（别误当成遗漏）：`com.homelab.ipv6` 是本机第三方服务的真名，
文档里描述它时必须用原名；`homelab.example.com` 只是示例域名。
另外「家庭实验室（HomeLab）」作为**场景词**仍在 PLAN 里使用——它指使用场景，
不是产品名，别再替换成 MacPanel（替换会让句子语义失真）。

## 产品与路线

MacPanel：macOS 服务器管理面板。**ADR-001 已决策：Go + Vue 3 Web-first，
不引入 Wails / Tauri / Rust**。理由：PF 规则、计划重启、launchd 服务必须在浏览器
关闭、用户注销后继续工作，桌面壳无法替代独立的 Root LaunchDaemon。

边界：默认只监听 `127.0.0.1`；远程访问需显式开启并完成认证/HTTPS 验收。

## 前端：Naive UI（2026-10-26 用户决定）

用户主动选择引入 Naive UI，理由是 **M2 的重表单**（"长痛不如短痛"）。
不要退回纯手写方案，也不要换成别的库。

必须遵守的约定：

- **按需注册，不要 `app.use(naive)`**：全量注册产物 gzip 442 kB（历史测量），
  按需后约 295 kB（naive 212 + vue 42 + 业务 37 + CSS 4，随构建浮动）。
  注册表在
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
./bin/macpanel token -dev -force    # 生成 .dev 令牌
./bin/macpanel agent -dev           # 终端 1
./bin/macpanel server -dev          # 终端 2 → http://127.0.0.1:7788
```

## 里程碑状态

见文末「M5 收尾」一节。所有功能里程碑（M0–M4、M6）代码已完成，
M5 打包链路已打通并通过静态校验；**干净系统安装与 PF/重启的实机验证仍未做**。

## M2：PF 防火墙写入（已完成代码，未实机验证）

**最重要的未完成事项**：`pfctl` 真实调用需要 root，本机 `sudo` 不可用，
因此 M2 的防火墙加载**从未在真实环境执行过**。进入 M3 前必须完成
`docs/testing.md` 第 4.7 节的实机清单，尤其是：
「应用阻断 SSH 的规则 → 连接断开 → 60 秒后自动恢复」。

### 架构（改这些代码前必读）

```text
agent/firewall.go      流程编排：生成 → 校验 → 写文件 → 事务
  ├── pfrules/        规则集 → PF 文本；纯 Go 双层校验（不依赖 root）
  ├── firewalld/      状态机、超时回滚、崩溃恢复、pfctl 执行器
  └── firewallstore/  SQLite 持久化
```

生成顺序即语义（PF 首个匹配即生效 + quick）：
`lo0 → ::1 → 出站 → 必需 ICMPv6 → 用户规则 → 默认拒绝`。
顺序错了白名单会失效。

### 四条不可让步的约束

1. **先落盘再加载**。反过来会在「加载成功但状态没写盘」时崩溃 →
   机器停在未确认规则上且永不回滚。
2. **Deadline 存绝对时间**。存剩余秒数会在 Agent 重启后重置，
   等于无限延长未确认窗口。
3. **绝不用 `pfctl -d`**。全局关闭防火墙会影响所有服务。
   回滚 = 清空本应用 anchor（加载 `/dev/null`）。
4. **不用 `set skip on lo0`**。它是主规则集指令，anchor 内无效。
   用 `pass quick on lo0 all`。

### pfctl 三层限制（别简化）

`execx.allowedBinaries`（路径）→ `execx.argGuards`（参数）→
`PFExecutor`（anchor 名 + 文件路径白名单）。
初版只有路径白名单，意味着 `pfctl -d` 可被发起 —— 等于没限制。

### 本机开发模式

非 root 或 `-dev` 自动 **dry-run**：走通生成/校验/状态机/界面，不触碰系统。
真实模式需 `sudo ./bin/macpanel agent`。

Vite 开发需 `MACPANEL_PUBLIC_HOSTS=localhost:5199` + 代理 `changeOrigin: true`，
否则写请求被 OriginGuard 403（只有写请求校验 Origin，GET 不受影响）。

### M2 踩过的坑

| 现象 | 根因 |
| --- | --- |
| 端口填了仍报「请输入端口」 | naive-ui `required` 对 `NInputNumber` 判定不可靠，改用纯 validator |
| PF 文本全空、校验报缺全部规则 | `Generate` 忘了 `gen.Content = b.String()` |
| 规则注释编号全是 `[0]` | `Save` 不返回数据库 ID |
| Caddy 模板放行 81–442 端口 | 把「80 和 443」误写成 PF 范围 `80:443`（安全缺陷） |
| 生产代理下写请求全 403 | `PublicHosts` 是死代码，从未从配置注入 |
| 开发模式所有 API 报 Host 不允许 | Vite `changeOrigin: false` 未改写 Host |

## M3/M4/M6：功能已全部完成（2026-10-26 晚）

**范围**：M0–M4 + M6 代码完成。M5 打包链路已通（见文末），实机验证未做。
377 个测试通过，合计覆盖率 53.2%。

### 分层地图（找代码先看这里）

```text
M3  internal/schedule（纯逻辑，80%）→ taskstore → tasksched → launchd
    cmd/macpanel/task-run   launchd 触发的执行入口
M4  services（扫描/分类/控制器）→ servicestore（受管白名单）
M6  darwin/diag（TCP+路由+监听） / orbstack（Docker Socket 只读）
```

### 必须知道的环境事实（实测）

| 事实 | 影响 |
| --- | --- |
| **`launchctl list` 在 macOS 27 返回空 + 退出码 1** | 服务列表必须用 `launchctl print system` 解析 services 段 |
| netstat 协议列有 `tcp6/tcp46/udp6/udp46` 四种 | 只匹配 tcp6 会漏掉双栈监听（最常见形态） |
| 某些受限环境 `netstat -an` 只返回 multipath 段 | 必须检查标志性标题行，否则会把「读不到」显示成「0 个」 |
| 本机 macOS 27 上 `pfctl` 在 `/sbin`，`vm_stat` 在 `/usr/bin` | 已修正，别再用 `/usr/sbin/pfctl` |
| 本机存在 `com.homelab.ipv6` launchd 服务 | **不是本项目创建的**，本项目用 `com.macpanel.*` |

### 四条设计约束（改动时别破坏）

1. **调度时间只有一个时钟源**。定时器用 `s.now()` 而不是 `time.Until`。
   混用会把「10 分钟后重启」变成「立即重启」（测试暴露过）。
2. **待执行重启必须落盘**，且恢复时先判断「系统是否已重启过」——
   已重启过则清除记录，否则会「重启后再重启」。
3. **服务启停三道拦截**：`model.Validate`（路径/前缀）→
   `ValidateLabelForControl`（拒绝关键/自身）→ `Controller`
   （只用登记过的 plist 路径）。任一道都不能省。
4. **诊断结论不合并**：`refused`（服务没起来）与 `timeout`
   （防火墙丢弃）必须分开，修复动作完全不同。

### 本阶段踩过的坑（都不是编译错误）

| 现象 | 根因 |
| --- | --- |
| 任务创建成功但 launchd 里没东西 | dev 用相对路径 + 同步失败只写日志 → 改为预校验 buildSpec |
| 「10 分钟后重启」立即执行 | 定时器混用真实时钟与注入时钟 |
| `ddns-go` 在服务列表消失 | 标签校验要求「必须含点」，而 launchd 允许无点 |
| 已登记服务显示成「未登记」 | `Enrich` 只有一个集合，未区分登记/授权 |
| 「每天重启都失败」与实际相反 | 重启杀掉进程留下的 running 记录被一律标失败 |
| OrbStack Status 报的 socket 与实际不符 | 构造函数创建了两个 Client 实例 |
| 任务表单填对却提交不了 | 前端回传了 `createdAt: ""`，Go 的 time 解析失败 |

### dev 模式的边界（别混用）

`-dev` 下：plist 写到 `.dev/launchd`（绝对路径）、`fake.Power` 只记录不执行、
Service Controller **仍装配**（但非 root 时能力受限）。
`task-run --dev` 强制用 fake power——这个开关是硬的，不依赖权限判断。

## M5 收尾：打包 + 防火墙本地救援（2026-10-26 晚）

### 防火墙救援 CLI（新增，别让它退化成「走 IPC」）

`macpanel firewall {status,rollback,unload}`（见 `cmd/.../firewall_cli.go`）
**刻意不连 Server 与 Unix Socket**：规则一旦挡住 SSH，面板那四跳本身就断了。
它直连 `firewalld.Manager`，所以 pfctl 三层限制一条不少。非 root 自动 dry-run。

`rollback` 与 `unload` 是两个语义，别合并：前者取消**当前事务**（idle 时报 conflict），
后者只回答「让本应用不再持有任何 PF 规则」。unload 必须存在的原因是
**倒计时定时器活在 Agent 进程里**——Agent 被杀后超时不会自动回滚。

### 打包命令

```bash
make pkg && make verify-pkg && make dmg     # 都不需要 root
sudo installer -pkg MacPanel-<v>.pkg -target /
sudo macpanel-uninstall [--yes|--purge]   # 随包安装
```

包的事实：二进制 + 卸载脚本在 `/usr/local/bin`（100755）、plist 在
`/Library/LaunchDaemons`（100644）；数据目录**不进 payload**（否则升级覆盖用户数据），
由 postinstall 建立 root:wheel 0750 / data 为 _macpanel 0700。

### 打包环境陷阱（本机实测）

| 陷阱 | 结论 |
| --- | --- |
| `com.apple.provenance` | **删不掉**（xattr -c/-d/removexattr 都返回成功但仍在）。pkgbuild 因此给每条记录加 `._xxx` AppleDouble；无害，卸载会清理 |
| macOS bash 是 3.2 | 不支持 `arr[-1]`，用 `tail -1` |
| `pkgutil --expand` | 目标必须不存在且不创建父目录；distribution 包展开一次就够，再 expand 会失败 |
| `lsbom -f` | 格式 `path<TAB>八进制mode<TAB>uid/gid<TAB>size`；`-pf` 只有路径 |
| `hdiutil` | macOS 27 起 deprecated，将来迁 `diskutil image create` |

### 待办（必须真机 root）

干净系统安装 → 升级与备份恢复 → 卸载 → PF 真实加载 → 真实重启与 launchd 触发。
清单见 `docs/m5-acceptance.md` 第 3 节。**no root 环境下的任何静态检查都不能替代它。**

## CI（2026-10-28 新增）

`.github/workflows/ci.yml`：单 job，`macos-latest`，原生 darwin/arm64（用户确认只要 Apple Silicon）。
所有触发跑 `make build` + `make check`；**仅 push 到 main** 才 `make pkg` + `make verify-pkg` + 上传制品。
刻意不上传裸二进制制品（会把人推进 dev 三连体验）。
单 job 而非 build/test 并行：两者都要跑 npm ci + 前端构建，macOS 分钟数按 Linux 倍数计费。

## 安装包版本号必须递增（实机踩出来的硬约束）

**同版本（如 `0.1.0`）覆盖安装会触发 macOS receipt-based obsoleting，在安装收尾时把 payload
里的文件删掉** —— 表现为 `pkgutil --files` 说装了、磁盘上没有、服务完全起不来，极难查。
而 `uninstall.sh` 若不 `pkgutil --forget` 收据，「卸载→重装」永远仍是同版本覆盖，必然复现。

优先级：`MACPANEL_PKG_VERSION`（CI 传 run_number）> 最近 git tag > `0.1.<提交计数>`。
卸载脚本已在**最开头** forget 收据（放最前：中断也能留下干净状态）。

## 装完 plist 消失的真凶：第三方清理工具，不是 installer（实机证实）

**installer 与 payload 都是清白的。** 排查链（照此复现，别再猜）：
手工 `cp` plist 进 `/Library/LaunchDaemons` → `ls` 可见 →
执行 `launchctl bootstrap system <plist>` → **文件消失**；
对照实验（对无关 plist 做 gui 域 bootstrap，失败退出 5）证明 **bootstrap 本身不删文件**。

结论：本机装的**腾讯柠檬清理**（`com.tencent.LemonDaemon`，KeepAlive 的 root 守护进程，
带启动项管理能力）在 launchd 注册任务后删除 plist。同类工具（各种「清理/优化」软件）同理。

**教训：看到「安装成功但文件不在」，先怀疑本机安全/清理类常驻进程，不要先怀疑打包。**
代码侧对应措施：plist 由 postinstall 从 scripts 目录再写一次（冗余保险），
并在 bootstrap 后自检——文件若消失就打印明确提示（见 build-pkg.sh 第 6 步）。

（此前我两轮归错了因：先猜打包缺文件、再猜同版本 obsoleting、再猜 installer 不写盘，
全部被证据推翻。改动前先做对照实验。）

## macOS shell 陷阱（补一条）

`head -n -N`（负行数）是 **GNU 专有**，macOS 的 BSD head 报 `illegal line count` 后整段静默失败。
保留最近 N 份要用：`sort -r | tail -n +"$((N + 1))"`。
此前同类：`arr[-1]`（bash 3.2 不支持）、`launchctl list`（macOS 27 返回空 + 退出码 1，要用 `launchctl print system`）。

## 排查 pkg 安装问题三件套

1. `pkgutil --files <id>` —— 收据**认为**装了什么
2. `ls <目标目录>` —— 磁盘上**实际**有什么
3. `lsbom -pf <展开 pkg>/component.pkg/Bom` —— **包里到底有没有**（区分「打包漏文件」与「装完被删」）

外加 `/var/log/install.log` 搜包 id，能看到 preinstall/postinstall 的逐行输出。
注意：**postinstall 有 `set -euo pipefail`，只要它打印到了结尾那几行，就说明中途没有命令失败。**

## root 的边界（用户已认可现状，暂不改）

PF 写规则 / 定时重启 / launchd 系统域服务**必须 root，且与「是否需要在注销后常驻」完全无关**。
不要因为用户说「我只在登录后管理」就以为能去掉 root。
若将来介意常驻，正解是 launchd socket activation，而不是降权。
