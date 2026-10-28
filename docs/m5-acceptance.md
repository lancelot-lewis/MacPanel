# M5 验收记录与实机测试手册

> 记录日期：2026-10-26
> 结论：**打包链路已修复并可构建；结构经静态校验通过；真实安装 / 升级 / 卸载仍未在干净系统上执行（开发机无 root）。**
> 用途：本文既是验收记录，也是**给测试人员的逐条手册**。第 3 节可以直接照着执行。

---

## 1. 交付范围

| 内容 | 文件 |
| --- | --- |
| PKG 构建 | `packaging/macos/pkg/build-pkg.sh` |
| 升级前备份 | `packaging/macos/pkg/build/scripts/preinstall`（由构建脚本生成） |
| 安装后初始化 | `packaging/macos/pkg/build/scripts/postinstall` |
| 卸载脚本（随包安装） | `packaging/macos/uninstall.sh` → `/usr/local/bin/macpanel-uninstall` |
| DMG 构建 | `packaging/macos/dmg/build-dmg.sh` |
| 包结构静态校验 | `scripts/verify-pkg.sh`（`make verify-pkg`） |
| 防火墙本地救援 CLI | `cmd/macpanel/firewall_cli.go`（`status` / `rollback` / `unload`） |
| 救援文档 | `docs/recovery.md` 第 3.8 节 |

---

## 2. 已验证（本机实测）

### 2.1 构建链路

```text
make pkg   → MacPanel-<version>.pkg   5.9 MB
make dmg   → MacPanel-<version>.dmg   6.0 MB
make verify-pkg → 通过 30 项，警告 1 项，失败 0 项
```

`<version>` 按此优先级决定：`MACPANEL_PKG_VERSION` 环境变量（CI 传 GitHub run number）
> 最近的 git tag > `0.1.<commit count>`。

**版本号必须每次递增，这一点是硬要求，不是洁癖**：同版本的两次安装会触发 macOS 的
receipt-based obsoleting，实测会在安装完成后把 payload 里的文件删掉（详见第 4 节表格）。

`make verify-pkg` 覆盖的检查项：

| 类别 | 检查内容 |
| --- | --- |
| 脚本语法 | 4 个 shell 脚本 `bash -n` |
| plist | 2 个 launchd 模板 `plutil -lint` |
| 模板 | 占位符存在（未被硬编码路径顶替） |
| 包结构 | 可展开、`Bom` / `Payload` / `PackageInfo` / `Scripts` 齐备、含 `preinstall` 与 `postinstall` |
| 脚本内容 | 无占位符残留、preinstall 做备份、postinstall 提到卸载与救援命令 |
| 文件清单 | 二进制与卸载脚本 100755、plist 100644、`Payload` 不含数据目录 |
| 运行身份 | agent 以 `root`、server 以 `_macpanel`、anchor 名带 `manager` 前缀 |

### 2.2 防火墙救援 CLI

本机非 root，因此以下均在 dry-run 下执行（流程走到、不改系统）：

```text
$ bin/macpanel firewall status -dev
== IPv6 防火墙 ==
  模式       dry-run（不会改动系统）
  事务状态   /…/MacPanel/.dev/run/firewall-state.json
  PF         未知（不可读）
             读取受限: pfctl: /dev/pf: Permission denied
  Anchor     com.macpanel.ipv6（文件存在）
  规则文件   /…/.dev/pf.anchor（存在=true，主配置引用=false）
             大小 880 字节
  期望状态   not_configured

== 事务 ==
  阶段       空闲（本应用未加载规则）

== 提示 ==
  · 当前处于 dry-run：输出真实，但不会改动 PF
  · 规则文件存在但 /etc/pf.conf 未引用本 anchor，规则不会生效
```

| 命令 | 结果 |
| --- | --- |
| `firewall`（无参数） | 打印用法并报错退出 |
| `firewall status -dev` | 正常，上述输出 |
| `firewall status -dev -json` | 机器可读输出正常 |
| `firewall rollback -dev`（idle 时） | 报 conflict，并提示改用 `unload` |
| `firewall unload -dev` | 成功，输出「已清空本应用 anchor（dry-run：未真正改动 PF）」 |
| 非 root 时 | 自动进入 dry-run（`-dry-run` 可强制） |

单元测试：`TestUnloadClearsAnchorFromAnyPhase`（4 个阶段）、
`TestUnloadAlsoWorksOnPending`、`TestUnloadKeepsStateOnFailure`、
`TestRenderLogArgs`、`TestFirewallStatePathFollowsSocketDir` 等。

### 2.3 卸载脚本

| 场景 | 结果 |
| --- | --- |
| `--help` | 打印用法 |
| 非 root 执行 | 明确拒绝并提示 `sudo`，退出码 1 |
| `--yes` / `--purge` | 语法检查通过（实际删除需 root） |

---

## 3. 待测清单（需 root，请照此执行）

> 前置：一台可以重启、物理可达的 Mac。**全程保留一个不受 PF 影响的通道**（显示器 / 另一条网络）。
> 每步都要记录实际输出，不要因为「看起来没问题」就跳过。

### 3.1 干净系统安装

```bash
# 1. 记录安装前基线（便于事后对比有没有污染系统）
sudo pfctl -s info | head -5
sudo pfctl -s Anchors | wc -l
launchctl print system/com.macpanel.agent 2>&1 | head -3   # 期望：不存在
id _macpanel                                                        # 期望：不存在

# 2. 安装
sudo installer -pkg MacPanel-<version>.pkg -target /   # 文件名随版本号变化，见 2.1
```

预期结果（逐条确认）：

- [ ] 安装器退出码 0，未报 `preinstall/postinstall` 错误
- [ ] `id _macpanel` 存在且隐藏（`dscl . -read /Users/_macpanel IsHidden` = 1）
- [ ] `/usr/local/bin/macpanel` 与 `macpanel-uninstall` 均存在且 0755
- [ ] `sudo launchctl print system/com.macpanel.agent` 有输出，`state = running`
- [ ] `sudo launchctl print system/com.macpanel.server` 有输出，所属用户为 `_macpanel`
- [ ] `curl -s http://127.0.0.1:7788/api/v1/healthz` 返回 OK
- [ ] 浏览器打开 `http://127.0.0.1:7788`，概览页显示真实 CPU / 内存 / 磁盘
- [ ] `/Library/Application Support/MacPanel` 属主 root:wheel 0750，
      其中 `data` 属主 `_macpanel` 0700
- [ ] IPC 令牌 `root:_macpanel 0640`
- [ ] `/Library/Logs/MacPanel` 下出现 agent / server 日志且**没有报错堆栈**
- [ ] `sudo macpanel firewall status` 显示「空闲」，PF 状态为真实值（非「未知」）
- [ ] 安装**没有**修改 `/etc/pf.conf`、没有新增 anchor 文件

重启后复核：

- [ ] 不登录任何 GUI 账户，等待 60 秒，两个服务仍在运行（`launchctl print` 有输出）
- [ ] 面板仍可访问
- [ ] `data` 目录属主权限未被重置

### 3.2 升级（含备份）

```bash
# 1. 先制造一些数据：建一条防火墙规则、登记一个受管服务、设一个计划任务
# 2. 记录当前版本号
/usr/local/bin/macpanel version

# 3. 安装同包第二遍（模拟升级）
sudo installer -pkg MacPanel-<version>.pkg -target /   # 文件名随版本号变化，见 2.1
```

- [ ] `/Library/Application Support/MacPanel/backups/preupgrade-<时间戳>/` 生成
- [ ] 备份目录内含 `data`、`ipc.token`、`pf-info.txt`、`pf-anchors.txt`
- [ ] 原有防火墙规则 / 受管服务 / 计划任务**全部还在**
- [ ] 令牌未被重新生成（两个服务仍正常通信）
- [ ] 服务重启后数据可读性正常（无 SQLite 损坏）

升级失败恢复演练：

- [ ] 手工把备份目录 `data` 拷回 `data` 的位置 → 服务起来后旧数据恢复

### 3.3 防火墙救援路径（最重要）

```bash
# 1. 在面板里应用一条会阻断自己的规则（例如拒绝全部入站 TCP）
#    故意不在 60 秒内确认
sudo macpanel firewall status      # 期望：阶段=待确认 + 剩余秒数
```

- [ ] `status` 显示的剩余秒数与面板倒计时一致（±2 秒）
- [ ] 不确认，等到倒计时归零 → anchor 被自动清空，网络恢复
- [ ] Agent 模拟崩溃（`sudo launchctl kill SIGKILL system/com.macpanel.agent`）
      后在倒计时内 → launchd 自动拉起，倒计时**继续**（不重置）
- [ ] Agent 崩溃且**已过**截止时间 → 重启后 `status` 显示已回滚
- [ ] 规则挡住 SSH 时，在物理终端执行 `sudo macpanel firewall rollback` 立刻恢复
- [ ] 手工把状态文件删掉后，`rollback` 报 conflict，`unload` 仍能清空 anchor
- [ ] 全程 `pfctl -s info` 的 PF 全局状态**始终没有变成 Disabled**（不得 kill 全局 PF）
- [ ] 其它程序的 anchor 未被影响（`pfctl -s Anchors` 前后一致，只少了本应用那一条）

### 3.4 卸载

```bash
sudo macpanel-uninstall
```

- [ ] 两个 launchd 服务被 bootout 且 plist 被删除
- [ ] `/usr/local/bin/macpanel` 与卸载脚本本身被删除
- [ ] `/var/run/macpanel` 被删除
- [ ] 数据目录**默认保留**（提示了 `backups/` 可找回）
- [ ] 从未执行过 `pfctl -d`（`sudo pfctl -s info` 仍显示 Enabled）
- [ ] `/etc/pf.conf` 未被修改，第三方 anchor 完好
- [ ] 用 `--purge` 再跑一遍（先重装），数据与日志被删除
- [ ] AppleDouble 残留（`._macpanel` 等）被清掉
- [ ] 卸载后重装可以正常回到 3.1 的状态

### 3.5 与前几个里程碑的交叉项

`docs/m2-acceptance.md` 与 `docs/m3-m4-m6-acceptance.md` 第 3 节的清单仍然有效，
其中**优先级最高**的三条：

1. 应用阻断自身 SSH 的规则 → 连接断开 → 60 秒后自动恢复（或本地 `rollback` 恢复）
2. 创建「5 分钟后重启」的任务 → 机器真的重启 → 重启后 `last_result=success`、`next_run_at` 已推进
3. 停止 / 启动一个受管第三方服务 → 状态确实改变且不误伤 `com.apple.*`

---

## 4. 本阶段发现并修复的缺陷

| 现象 | 根因 | 修复 |
| --- | --- | --- |
| `make dmg` 必然失败 | DMG 用 `git describe` 推版本号（`ef79e4e`），PKG 用固化版本（`0.1.0`），两者不一致 | DMG 改为从**实际产物文件名**推导版本 |
| DMG 脚本用 `arr[-1]` | macOS 自带 bash 3.2 不支持 negative subscript | 改用 `tail -1` |
| 升级会覆盖现有数据且无退路 | 没有 preinstall | 新增 preinstall：停服务 → 备份 data / 令牌 / PF 现状 → 只留最近 5 份 |
| 硬编码 UID/GID 350 | 与其它软件撞号时 `dscl -create` 失败会让**整个安装失败** | 从 350 起向下找空位；用户与组分别判定；最后对齐 PrimaryGroupID |
| 卸载脚本只存在于 build 目录，出事时找不到 | 它没进包 | 作为 `packaging/macos/uninstall.sh` 入库，并装到 `/usr/local/bin/macpanel-uninstall` |
| 卸载只能交互执行 | `read -r -p` 阻塞自动化 | 支持 `--yes` / `--purge`；非 root 明确拒绝 |
| 卸载未处理加载到一半的 anchor | 只提示，不清空 | 先 `pfctl -a <anchor> -f /dev/null` 清空，再谈删文件 |
| 文档与安装说明写着「M1 完全只读」 | M2 之后未更新 | 改为当前真实行为 + 救援命令 |
| 没有本地终端的防火墙回滚手段 | 面板回滚依赖 Server/IPC/Agent 四跳，失联时这条链正好断了 | 新增 `firewall status/rollback/unload`，刻意不经过 IPC |
| Agent 崩溃后倒计时失效却无兜底 | `Rollback` 在 idle 时报错 | Manager 新增 `Unload`：与阶段无关地清空 anchor |
| CLI 日志输出 `%!(EXTRA string=reason…)` | Logger 是 slog 风格 key/value，却用 `fmt.Sprintf` 展开 | 新增 `renderLogArgs` 渲染 `k=v`（附回归测试） |
| **干净卸载后再重装，面板依旧起不来：`/Library/LaunchDaemons/` 里没有任何 plist** | 两次安装的版本号完全相同，macOS 触发 **receipt-based obsoleting**，会在安装完成后把 payload 里的文件删掉；而 `macpanel-uninstall` 从不清除收据，于是「卸载→重装」永远还是同版本覆盖 | 版本号强制递增（`MACPANEL_PKG_VERSION` > tag > 提交计数兜底），且卸载时 `pkgutil --forget com.macpanel` |
| **Agent 正常、Server 起不来；stderr 持续刷「创建数据目录失败: permission denied」** | postinstall 把 `/Library/Application Support/MacPanel` 设为 `root:wheel 0750`。`_macpanel` **不在 wheel 组**，others 位是 `---`，连进入（traverse）父目录都做不到，于是既建不了 `data` 也读不到同目录下的 `ipc.token` | 父目录改为 `root:_macpanel 0750`：组给 `r-x`（能进入、能读令牌），但**不给写**，避免在父目录里乱建东西；`data` 保持 `_macpanel` 0700 |
| **升级前备份只增不减，磁盘慢慢被吃光** | `preinstall` 用 `head -n -"${KEEP}"` 保留最近 KEEP 份，负行数是 GNU 专有语法，macOS 的 BSD head 报 `illegal line count -- -5` 后整段静默失败 | 改为 `sort -r \| tail -n +"$((KEEP + 1))"`，BSD / GNU 都成立 |
| **装完后面板打不开：`/Library/LaunchDaemons/` 下没有任何 plist，且安装「看起来完全成功」** | **本应用自杀**：`tasksched` 的「清理孤儿 plist」用 `ListManaged()` 列出所有 `com.macpanel.*`，把不在任务表里的删掉；而 `LabelPrefix = "com.macpanel."` 同时覆盖本应用自身服务，于是 `com.macpanel.agent` / `com.macpanel.server` 被当成残留任务 unlink。链路：bootstrap → 启动 agent → agent 对账 → 删掉自己的 plist。installer、payload、第三方软件全部清白 | `isTaskLabel()` 只认 `com.macpanel.task.*`：对账循环跳过非任务 label；`launchd.Installer.Remove` 再硬拒一次非任务 label。定位手法：`sudo fs_usage -w -f filesystem \| grep com.macpanel` 直接打出做 unlink 的进程（当时是 `macpanel` 自己） |
| **面板所有状态接口 500，日志 `调用方身份不受信任` / `Forbidden`** | Agent 的 IPC 对端校验 `ipc.UIDAllowed` 只放行「与 Agent 同 euid（root=0）」或 `MACPANEL_ALLOWED_PEER_UIDS` 显式列出的 UID。Server 以 `_macpanel`(350) 运行，而生产 plist 从未设置该变量 | agent plist 模板加 `MACPANEL_ALLOWED_PEER_UIDS = __SERVER_UID__`，postinstall 用 `dscl` 取真实 UID 替换并校验；UID 安装时动态分配，不能写死 |
| **面板 500，`ls /var/run/macpanel` 显示 socket 为 `root:wheel 0660`** | Socket 由 root 创建，`os.Chmod(0660)` 只改权限位、不动属组，组默认继承 root 的 `wheel`。Server 是 `_macpanel`，不在 wheel 组，others 位 `---`，连接被拒。目录权限改对也没用，卡在 socket 文件本身 | 新增 `MACPANEL_SOCKET_GID`：`ipc.ServerOptions.SocketGID`，`Listen` 后 `os.Chown(path, -1, gid)`。socket 每次 Agent 启动都重建，只能在代码里改，装完补一次没用 |
| **面板 500，Server 日志 `读取 IPC 令牌失败: open …/ipc.token: permission denied`** | `token` 子命令用 `os.CreateTemp` + `chmod 0600` 写文件，属组是 root 的默认组（wheel），它不设属组。postinstall 只在新令牌时 `chown root:_macpanel` + `chmod 0640`，`else` 分支仅打印「保留原值」——旧安装留下的 `root:wheel 0600` 令牌**永远修不回来** | chown/chmod 移出 `if`，每次安装强制校正，并 `stat` 校验 `root:_macpanel 640`，不符即中止安装 |
| BOM 里有 9 条 `._xxx` AppleDouble | macOS 给本地文件写 `com.apple.provenance`（内核合成，删不掉），pkgbuild 为其生成 AppleDouble | 尽力清除 xattr；卸载时顺手清理；`verify-pkg` 将其列为**警告**并说明原因 |

---

## 5. 已知限制

1. **AppleDouble 残留**：本机 macOS 环境下 BOM 会带 `._xxx` 条目，安装后系统中多出几个隐藏文件。
   功能不受影响，卸载脚本会清理。正式发版建议在 CI / 干净构建机上重新打包，
   那时 `make verify-pkg` 的这一条警告应当消失。
2. **未签名、未公证**：Gatekeeper 会拦截，需在「隐私与安全性」手动放行。
   分发前必须做 Developer ID 签名 + 公证。
3. **`macpanel` 的二进制出入口在 `/usr/local/bin`**：Apple Silicon 上该路径存在且可用，
   但如果目标机器启用了某些 MDM 策略限制写入，需要改用 `/opt/homebrew/bin` 之类路径。
4. **升级尚未实机演练**：preinstall 的备份逻辑已写好并通过静态检查，但没有在真机上跑过一次。
5. **`ThrottleInterval=10` + `KeepAlive`**：Agent 崩溃循环最快每 10 秒一次；
   若某个环境依赖会导致持续崩溃，请在近 10 秒内用 `launchctl bootout` 停掉。

---

## 6. 与 Definition of Done 的对照

PLAN.md 第 14 节的清单里，与 M5 相关的各项目前状态：

| 条目 | 状态 |
| --- | --- |
| 安装、升级、卸载与救援流程可重复执行 | ⏳ 脚本与校验齐备，**待干净系统实机执行** |
| 关键异常场景有自动恢复或明确的本地救援路径 | ✅ CLI 救援已落地并有单元测试覆盖 |
| macOS 重启后无需用户登录即可恢复服务 | ⏳ 依赖 LaunchDaemons 配置，待实机验证 |
| IPv6 本机回环 / 主动出站 / 合法入站 / 未授权入站全部实测通过 | ⏳ 见 `docs/m2-acceptance.md` |
| 定时重启可恢复、可取消，不重复执行 | ⏳ 见 `docs/m3-m4-m6-acceptance.md` |
| 启动项管理只操作明确受管对象 | ✅ 三层拦截已测试，真实 `launchctl` 调用待验证 |

**发布阻断条件**（任一条成立即不得发布）：不可逆的远程失联、未授权 Root 操作、
IPv4 意外受影响、回滚失效、循环重启。这些只能靠第 3 节的实机测试排除。
