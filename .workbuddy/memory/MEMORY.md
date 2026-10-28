# MacPanel 项目长期记忆

（详细过程见 `.workbuddy/memory/YYYY-MM-DD.md` 日志；本文件只留长期约束。）

## 命名

产品名 **MacPanel**，旧名 HomeLab Manager / homelab-manager 已全部清除，代码里不要再出现。

| 对象 | 值 |
| --- | --- |
| 二进制 / 卸载 | `macpanel`、`macpanel-uninstall`（`/usr/local/bin`） |
| Go module / 仓库 | `github.com/lancelot-lewis/MacPanel` |
| 命令目录 | `cmd/macpanel/` |
| 环境变量前缀 | `MACPANEL_` |
| launchd label | `com.macpanel.agent` / `.server` / `com.macpanel.task.<id>` |
| 服务账户 | `_macpanel`（UID/GID 从 350 向下找空位） |
| 目录 | `/Library/Application Support/MacPanel`、`/Library/Logs/MacPanel`、`/var/run/macpanel` |
| PF anchor | `com.macpanel.ipv6` |
| 安装包 | `MacPanel-<version>.pkg` / `.dmg` |

**刻意保留的旧名**：`com.homelab.ipv6` 是本机第三方服务的真名，描述时用原名；
`homelab.example.com` 只是示例域名；「家庭实验室（HomeLab）」作为**场景词**仍在 PLAN 里用。

## 产品与路线

macOS 服务器管理面板。**ADR-001：Go + Vue 3 Web-first，不引入 Wails / Tauri / Rust**——
PF 规则、计划重启、launchd 服务必须在浏览器关闭、用户注销后继续工作，桌面壳替代不了
独立的 Root LaunchDaemon。边界：默认只监听 `127.0.0.1`，远程访问需显式开启并完成认证/HTTPS 验收。

**只支持 darwin/arm64（Apple Silicon）**，用户确认。`internal/platform/darwin` 在非 darwin
下根本编译不过，所以 CI 必须跑 macOS runner。

## 最强的产品约束：不伪造状态

1. **model**：布尔状态用 `*bool`，`null` = 无法确认，绝不等于 `false`；采集失败写 `missing: [{metric, reason}]`。
2. **API**：有缺失项 → `meta.degraded=true` + `meta.notes`；整体失败 → `503` 不返回部分数据；未实现 → `501`。
3. **前端**：「不可用」与「值为 0」分开渲染；失败时保留旧数据但标注「可能已过期」。

## 关键架构约定

- 依赖方向 `cmd → server → app → ipc → model`；`model` 不依赖任何内部包。
- HTTP handler **不直接执行系统命令**，一律 `app.Service → ipc.Client → Agent`。
- 外部命令只走 `internal/platform/execx`（5 个绝对路径白名单 + 数组传参 + 3s 超时 + 1MiB 上限）。
- Agent 只有固定方法注册表，**不存在** `exec` / 任意文件读写 / 任意 PF 语句入口。
- 读接口 `requireNoParams` 显式拒绝参数。
- **`LabelPrefix` 这类命名空间前缀同时覆盖「自身服务」和「用户任务」时，任何基于前缀的批量操作
  必须再收一层判据**（`isTaskLabel()` 只认 `com.macpanel.task.*`；`launchd.Installer.Remove`
  硬拒非任务 label）。这条是把自己 launchd 定义删掉的根因。

## IPC 身份验证（三层，缺一不可）

1. 内核对端凭据：darwin `LOCAL_PEERCRED`（`unix.GetsockoptXucred`；`solLocal=0x0`、
   `localPeerCred=0x001` 需自行声明，x/sys 未导出）/ linux `SO_PEERCRED`；
2. 32 字节共享令牌，`subtle.ConstantTimeCompare`；
3. ±60s 时间窗 + 唯一请求 ID 防重放。

平台读不到凭据时**一律拒绝**，不做「读不到就放行」。`UIDAllowed` 只放行 Agent 自身 euid
或 `MACPANEL_ALLOWED_PEER_UIDS` 显式列出的 UID。

## 认证授权条件（有过死锁教训）

`/auth/password` 在认证中间件之外，handler 内 `sessionFrom(ctx)` 永远为空，必须手动
`hasValidSession()`。按「是否已设密码」分流：未设置（初始化）→ **只允许回环**；
已设置 + 远程 → 须已登录 + `currentPassword` 正确；已设置 + 本地 → 须本机 + `currentPassword`。

## 安装后的权限与动态值（6 次实机 bug 换来的，改打包必读）

以 `_macpanel` 运行的 Server 要连上 root 的 Agent，**四处必须同时成立**，缺一处就是
「面板能打开、所有状态接口 500」：

| 对象 | 正确值 | 错了的表现 |
| --- | --- | --- |
| 数据目录 `/Library/Application Support/MacPanel` | `root:_macpanel 0750` | `创建数据目录失败: permission denied` |
| 令牌 `ipc.token` | `root:_macpanel 0640` | `读取 IPC 令牌失败: permission denied` |
| Socket `agent.sock` | `root:_macpanel 0660` | 连不上，日志 `Forbidden` |
| Agent plist `MACPANEL_ALLOWED_PEER_UIDS` | `_macpanel` 的真实 UID | `调用方身份不受信任` |

**三条铁律**：
1. **必须每次安装都强制校正，不能「已存在就跳过」**。`token` 子命令用 `os.CreateTemp` +
   `chmod 0600` 写文件，属组是 root 的默认组（wheel），它自己不设属组；只在新建时 chown
   会让旧令牌永久不可读。
2. **UI 上能改的都不能靠安装脚本补一次性**：socket 每次 Agent 启动重建，必须在代码里
   `os.Chown(path, -1, gid)`（`MACPANEL_SOCKET_GID`）；`os.Chmod(0660)` 只改权限位不动属组。
3. **UID/GID 是安装时动态分配的**，不能写死在模板里 —— plist 用 `__SERVER_UID__` /
   `__SERVER_GID__` 占位，postinstall 用 `dscl` 取真值替换，并校验「替换确实发生」。

判断是否真的通：`curl --noproxy '*' http://127.0.0.1:7788/api/v1/system/overview`，
返回真实数据才算通。`healthz` 只证明 Server 的 HTTP 栈活着，**不代表能连上 Agent**。

## 前端：Naive UI（用户决定，别退回手写）

- **按需注册，不要 `app.use(naive)`**：全量 gzip 442 kB，按需约 295 kB。注册表
  `web/src/theme/components.ts`，**新增组件必须补一行**。
- `theme/` 是组件库唯一入口：业务只用 CSS 变量或组件 `type` 属性。
- 明暗模式单真源 `theme/mode.ts`（同时驱动 `html.dark` 与 `NConfigProvider :theme`），
  两处各自判断 `prefers-color-scheme` 会撕裂。

### 踩坑

| 现象 | 原因 |
| --- | --- |
| 页面全白只 warn | `NCard.name === "Card"`，`N` 前缀只在导出名上，不能用 `c.name` 注册 |
| 深色底色撕裂 | CSS 变量与组件库各自判断系统偏好 |
| 对比度大面积不达标 | 库把语义色同时用作文字色和 12% 透明底 |
| `type="primary"` 的 Tag 覆盖无效 | 该类型走 `primaryColor`；改用 `type="info"` |
| 移动端侧栏不隐藏 | `NLayoutSider` 自身是 flex 元素；用外层 div |

对比度已按 WCAG AA 修正（绿 `#146b3b`、橙 `#7a4f00`、蓝 `#1d4fb5`、三级文字浅
`#666c76`/深 `#8b93a0`），`placeholder` 与必填星号在 `style.css` 用 `!important` 补。
**检测对比度必须逐层叠加半透明背景**，否则大量假阳性。

## M2 防火墙（代码完成，**从未实机验证**）

```text
agent/firewall.go   流程编排：生成 → 校验 → 写文件 → 事务
  ├── pfrules/      规则集 → PF 文本；纯 Go 双层校验（不依赖 root）
  ├── firewalld/    状态机、超时回滚、崩溃恢复、pfctl 执行器
  └── firewallstore/ SQLite 持久化
```

生成顺序即语义（首匹配 + quick）：`lo0 → ::1 → 出站 → 必需 ICMPv6 → 用户规则 → 默认拒绝`。

**四条不可让步**：
1. **先落盘再加载**（反过来会在「加载成功但状态没写盘」时崩溃 → 永不回滚）。
2. **Deadline 存绝对时间**（存剩余秒数会在 Agent 重启后重置）。
3. **绝不用 `pfctl -d`**（全局关闭影响所有服务）；回滚 = 清空本应用 anchor。
4. **不用 `set skip on lo0`**（主规则集指令，anchor 内无效），用 `pass quick on lo0 all`。

pfctl 三层限制别简化：`execx.allowedBinaries`（路径）→ `execx.argGuards`（参数）→
`PFExecutor`（anchor 名 + 文件路径白名单）。初版只有路径白名单时 `pfctl -d` 可被发起。

非 root 或 `-dev` 自动 dry-run。Vite 开发需 `MACPANEL_PUBLIC_HOSTS=localhost:5199` +
代理 `changeOrigin: true`，否则写请求被 OriginGuard 403。

## M3/M4/M6（代码完成）

```text
M3  internal/schedule（纯逻辑）→ taskstore → tasksched → launchd
    cmd/macpanel/task-run   launchd 触发的执行入口
M4  services（扫描/分类/控制器）→ servicestore（受管白名单）
M6  darwin/diag（TCP+路由+监听） / orbstack（Docker Socket 只读）
```

**四条设计约束**：
1. **调度时间只有一个时钟源**（定时器用 `s.now()`，别混 `time.Until`，否则「10 分钟后」变立即）。
2. **待执行重启必须落盘**，恢复时先判断系统是否已重启过（否则「重启后再重启」）。
3. **服务启停三道拦截**：`model.Validate` → `ValidateLabelForControl`（拒关键/自身）→
   `Controller`（只用登记过的 plist 路径）。
4. **诊断结论不合并**：`refused`（服务没起来）与 `timeout`（防火墙丢弃）修复动作完全不同。

`-dev` 下 plist 写 `.dev/launchd`、`fake.Power` 只记录、Service Controller 仍装配；
`task-run --dev` 强制 fake power（硬开关，不看权限）。

## M5 打包与防火墙救援

`macpanel firewall {status,rollback,unload}` **刻意不连 Server / Unix Socket**——规则挡住
SSH 时面板那四跳本身就断了，它直连 `firewalld.Manager`。`rollback`（取消当前事务，idle 报
conflict）与 `unload`（不再持有任何 PF 规则）是两个语义，别合并；unload 存在的理由：
倒计时定时器活在 Agent 进程里，Agent 被杀后超时不会自动回滚。

```bash
make pkg && make verify-pkg && make dmg        # 都不需要 root
sudo installer -pkg MacPanel-<v>.pkg -target /
sudo macpanel-uninstall [--yes|--purge]
```

数据目录**不进 payload**（否则升级覆盖用户数据），由 postinstall 建立。

### 版本号必须每次递增（实机踩出来的硬约束）

同版本覆盖安装触发 macOS **receipt-based obsoleting**，安装收尾时把 payload 文件删掉——
表现为 `pkgutil --files` 说装了、磁盘上没有、服务起不来，极难查；而 `uninstall.sh` 不
`pkgutil --forget` 收据的话，「卸载→重装」仍是同版本覆盖，必然复现。
优先级：`MACPANEL_PKG_VERSION`（CI 传 run_number）> 最近 git tag > `0.1.<提交计数>`；
卸载脚本在**最开头** forget 收据。

### 打包环境陷阱

`com.apple.provenance` 删不掉（各种 xattr 手段都返回成功但仍在），pkgbuild 因此给每条记录
加 `._xxx` AppleDouble（无害）；macOS bash 是 3.2（不支持 `arr[-1]`）；`pkgutil --expand`
目标必须不存在且不创建父目录；`lsbom -f` 格式 `path<TAB>八进制mode<TAB>uid/gid<TAB>size`；
`hdiutil` macOS 27 起 deprecated。

## 排查 pkg 安装问题三件套

1. `pkgutil --files <id>` —— 收据**认为**装了什么
2. `ls <目标目录>` —— 磁盘上**实际**有什么
3. `lsbom -pf <展开 pkg>/component.pkg/Bom` —— **包里到底有没有**

外加 `/var/log/install.log`（能看 pre/postinstall 逐行输出；postinstall 有 `set -euo pipefail`，
打印到结尾说明中途没失败）。**要指认「谁删了文件」就直接上
`sudo fs_usage -w -f filesystem | grep <名字>`**，它会打出进程名——别靠推理。

## macOS 环境事实与 shell 陷阱

| 事实 | 影响 |
| --- | --- |
| **`launchctl list` 在 macOS 27 返回空 + 退出码 1** | 用 `launchctl print system` |
| `head -n -N` 是 GNU 专有 | BSD head 报 `illegal line count` 后整段静默失败；用 `sort -r \| tail -n +"$((N+1))"` |
| netstat 有 `tcp6/tcp46/udp6/udp46` | 只匹配 tcp6 会漏双栈监听 |
| 某些环境 `netstat -an` 只返回 multipath 段 | 必须检查标题行，否则「读不到」被显示成「0 个」 |
| `pfctl` 在 `/sbin`，`vm_stat` 在 `/usr/bin` | 别用 `/usr/sbin/pfctl` |
| `sysctl vm.page_inactive_count` 不存在 | 内存统计走 `vm_stat` |
| 本机 `com.homelab.ipv6` 服务 | **不是本项目创建的** |

本机开发：Go 由 mise 管理（`~/.local/share/mise/shims`）；`sudo` 在沙箱不可用；有
`HTTP_PROXY`，curl 测本机端口要 `--noproxy '*'`。

```bash
export PATH="$HOME/.local/share/mise/shims:$PATH"
make build | build-go | test | test-race | check
./bin/macpanel token -dev -force   # 仅一次
./bin/macpanel agent -dev & ./bin/macpanel server -dev   # → http://127.0.0.1:7788
```

**两条路径别混**：源码开发 = 手工 token + 两个终端 + 功能受限；PKG 安装 = postinstall 全自动
（令牌、plist、bootstrap），**零命令**。装完千万别跑 `token -dev -force`（`-dev` 把路径改到
当前目录 `.dev/`，生成一坨无关文件）。

## CI

`.github/workflows/ci.yml`：单 job，`macos-latest`，darwin/arm64。所有触发跑
`make build` + `make check`；**仅 push 到 main** 才 `make pkg` + `verify-pkg` + 上传制品
（名字带短 SHA）。**刻意不上传裸二进制**——会把人推进 dev 三连。
单 job 不拆：两者都要 npm ci + 前端构建，macOS 分钟数按 Linux 倍数计费。
`web` 包用 `//go:embed all:dist`，**任何 go vet/test 之前必须先生成 `web/dist`**，否则
`pattern all:dist: no matching files found`。

## 待办：必须真机 root 完成的验证

干净系统安装 → 升级与备份恢复 → 卸载 → PF 真实加载 → 真实重启与 launchd 触发。
清单见 `docs/m5-acceptance.md` 第 3 节。**no root 环境下的任何静态检查都不能替代它。**

首次真机安装（2026-10-28）共暴露 6 个 bug，全部是无 root 静态检查不可能发现的：
`head -n -5`、tasksched 自删 plist、数据目录父目录属组、UID 白名单、socket 属组、令牌权限。
**后续任何「看起来只是配置问题」的改动，都要假设还有同类的权限/属组问题。**

## root 的边界（用户已认可，暂不改）

PF 写规则 / 定时重启 / launchd 系统域服务**必须 root，且与「是否需要注销后常驻」无关**。
别因为用户说「我只在登录后管理」就以为能去掉 root。若将来介意常驻，正解是 launchd
socket activation，而不是降权。
