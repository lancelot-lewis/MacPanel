# 故障恢复与 CLI 救援

> 对应里程碑：M0 / M1，防火墙救援命令随 M2 补齐
> 最后更新：2026-10-26

## 1. 设计前提

远程管理面板最大的风险是**把自己锁在门外**。因此本项目的救援能力必须满足：

1. **不依赖 Web 服务**：Agent 与 CLI 都能独立工作。
2. **不依赖网络**：本机终端即可完成诊断与恢复。
3. **不依赖用户登录**：launchd 系统服务在登录界面也在运行。
4. **有明确的恢复路径**：每类故障都有可执行的具体命令，而不是「重装试试」。

## 2. 诊断的第一站：CLI

```bash
# 完整状态（Agent + 系统 + 防火墙）
macpanel status

# 机器可读输出（便于脚本与粘贴到问题报告）
macpanel status --json

# 查看生效配置（不启动任何服务，只打印并校验）
macpanel config -mode server
macpanel config -mode agent

# 防火墙状态与本地回滚（不经 IPC，面板打不开时也能用）
sudo macpanel firewall status
sudo macpanel firewall rollback
sudo macpanel firewall unload
```

`status` 的行为：

- 读取令牌文件 → 连接 Unix Socket → 调用 `agent.health` / `system.overview` /
  `firewall.status`。
- Agent 不可达时**不报错退出**，而是逐项打印「不可达原因」，便于定位是
  Agent 没起来、令牌不匹配，还是 Socket 权限问题。

`firewall` 系列与之相反：**刻意不连 IPC**。它是面板失联时的兜底，
因此自己直接读 root 独占的事务状态文件、直接调用 PF executor。
详见 [3.8](#38-规则导致失联最紧急)。

## 3. 常见故障与处置

> **如果你的机器刚刚还能连上、应用/修改了 IPv6 防火墙规则之后才断连，
> 不要在本节逐项排查，直接跳到 [3.8 规则导致失联](#38-规则导致失联最紧急)。**
> 那一节的三条命令不需要面板、不需要 Agent、不需要网络。

### 3.1 Web 面板打不开

```bash
# 1. Server 进程是否在跑？
launchctl print system/com.macpanel.server | head -30

# 2. 端口是否在监听？
lsof -nP -iTCP:7788 -sTCP:LISTEN

# 3. 直接探测（绕过浏览器）
curl -s http://127.0.0.1:7788/api/v1/healthz

# 4. 看日志
tail -50 /Library/Logs/MacPanel/server.stderr.log
tail -50 /Library/Logs/MacPanel/server.log
```

若 `healthz` 有响应但页面空白：前端资源未正确嵌入，检查安装包是否为
`make build`（含 `web-build`）产物。

### 3.2 Agent 不可达（面板显示「Agent 不可达」）

```bash
# 1. Agent 服务状态
launchctl print system/com.macpanel.agent | head -30

# 2. Socket 文件是否存在、权限是否正确
ls -l /var/run/macpanel/agent.sock
# 期望：srw-rw---- root:<组> 

# 3. 令牌文件是否存在、权限是否正确
ls -l "/Library/Application Support/MacPanel/ipc.token"
# 期望：-rw-r----- root:<组>

# 4. Agent 日志
tail -50 /Library/Logs/MacPanel/agent.stderr.log
```

常见原因与处置：

| 现象 | 原因 | 处置 |
| --- | --- | --- |
| Socket 不存在 | Agent 未启动 / 启动即退出 | 看 stderr 日志；`launchctl kickstart -k system/com.macpanel.agent` |
| Socket 权限不是 0660 | 目录或文件被手工改动 | 恢复 `chmod 0660`，目录 `0750` |
| 报「已有实例在监听」 | 重复启动 | 先 `launchctl bootout` 旧实例 |
| 报「路径已存在且不是 Socket」 | 该路径被普通文件占用 | 确认内容后再手工移走（程序不会自行删除） |
| `unauthorized` | Server 与 Agent 令牌不一致 | 重新生成令牌并同时重启两者（见 3.3） |

### 3.3 令牌不一致

```bash
# 重新生成（会覆盖旧令牌，必须同时重启两个服务）
sudo /usr/local/bin/macpanel token -force
sudo chown root:_macpanel "/Library/Application Support/MacPanel/ipc.token"
sudo chmod 0640 "/Library/Application Support/MacPanel/ipc.token"

sudo launchctl kickstart -k system/com.macpanel.agent
sudo launchctl kickstart -k system/com.macpanel.server
```

注意：`token -force` 会立即让旧令牌失效，期间 Server 调用会失败。

### 3.4 忘记管理密码

当前没有「找回」机制（不发送邮件、不存明文）。恢复方式：

```bash
# 直接清除密码哈希，然后用本机浏览器重新设置
sudo sqlite3 "/Library/Application Support/MacPanel/data/macpanel.db" \
  "DELETE FROM settings WHERE key = 'auth.password_hash';"

# 同时撤销所有会话，避免残留登录态
sudo sqlite3 "/Library/Application Support/MacPanel/data/macpanel.db" \
  "UPDATE auth_sessions SET revoked_at = datetime('now') WHERE revoked_at IS NULL;"

sudo launchctl kickstart -k system/com.macpanel.server
```

之后在**本机**打开 `http://127.0.0.1:7788` → 设置 → 设置密码。
必须在物理可及的本机操作，这是刻意的设计：不提供远程重置入口。

### 3.5 数据库损坏

```bash
# 1. 先停服务，避免写入竞争
sudo launchctl bootout system/com.macpanel.server

# 2. 检查完整性
sqlite3 "/Library/Application Support/MacPanel/data/macpanel.db" "PRAGMA integrity_check;"

# 3. 备份现场
sudo cp "/Library/Application Support/MacPanel/data/macpanel.db" \
        "/Library/Application Support/MacPanel/data/macpanel.db.corrupt.$(date +%s)"

# 4. 尝试导出可读数据
sqlite3 macpanel.db.corrupt.* ".dump" > /tmp/recover.sql 2>/dev/null

# 5. 最坏情况：删除后重建（会丢失审计与设置，防火墙规则不受影响）
sudo rm -f "/Library/Application Support/MacPanel/data/macpanel.db"*
sudo launchctl bootstrap system /Library/LaunchDaemons/com.macpanel.server.plist
```

数据库只存配置、审计与指标；**删除它不会改变任何系统防火墙状态**。

### 3.6 PF 状态「不可读」而不是「已关闭」

这是 M1 的正常行为，不是故障。原因：`/dev/pf` 仅 root 可访问。

```bash
# 以 root 确认真实状态
sudo pfctl -s info | head -20
sudo pfctl -s Anchors
```

若 Agent 以 root 运行仍显示不可读，检查 launchd plist 中的 `UserName` 是否为 `root`，
以及 `launchctl print system/com.macpanel.agent` 输出中的运行身份。

### 3.7 端口 7788 被占用

```bash
lsof -nP -iTCP:7788 -sTCP:LISTEN
```

改端口：编辑 `/Library/LaunchDaemons/com.macpanel.server.plist` 中的
`MACPANEL_LISTEN_ADDR`，然后：

```bash
sudo launchctl bootout system /Library/LaunchDaemons/com.macpanel.server.plist
sudo launchctl bootstrap system /Library/LaunchDaemons/com.macpanel.server.plist
```

注意：非回环地址必须同时设置 `MACPANEL_ALLOW_REMOTE=true`，否则启动时会直接报错退出
（这是安全不变量，不是缺陷）。

### 3.8 规则导致失联（最紧急）

**症状**：应用规则后 SSH / 面板连不上；或 rules → apply 之后确认倒计时走完也没恢复。

面板里的「回滚」按钮要走「浏览器 → Web Server → IPC → Agent」四跳，
而挡住连接的恰恰是刚应用的规则，这条链路本身可能已经断了。
因此这种情况**必须用本地终端**（物理接入 / 带外控制台 / iKVM），命令如下。

```bash
# 1. 先看发生了什么（纯只读，永远不会改动系统）
sudo /usr/local/bin/macpanel firewall status

# 2. 存在待确认配置时用回滚（幂等，重复执行无害）
sudo /usr/local/bin/macpanel firewall rollback

# 3. 若 2 提示「当前没有已应用的配置可回滚」，说明状态文件已失效或从未建立，
#    但 anchor 里可能仍有本应用的规则残留 —— 用 unload 无条件清空
sudo /usr/local/bin/macpanel firewall unload
```

三条命令的边界（这也是它们能安全使用的原因）：

| 行为 | 说明 |
| --- | --- |
| 不经过 IPC 与 Web Server | Agent 未运行、端口被挡住时依然可用 |
| 只操作本应用 anchor | `pfctl -a com.macpanel.ipv6 -f /dev/null` |
| 绝不执行 `pfctl -d` | 不会全局关闭 PF，其它程序的规则不受影响 |
| 不删除规则文件 | 内容保留，可事后检查当初写了什么 |
| 非 root 自动 dry-run | 输出与真实流程一致但不改系统，可用于预演 |

`status` 输出里的关键字段：

| 字段 | 怎么读 |
| --- | --- |
| `PF` | 「未知（不可读）」表示当前不是 root，不是「已关闭」 |
| `Anchor` | 「文件存在」≠ 已生效；需同时看「主配置引用」 |
| `事务/阶段` | `待确认` → 用 `rollback`；`已确认` → 规则正在生效，`rollback` 可撤回 |
| `截止时间` | 剩余秒数归零后，只要 Agent 存活就会自动回滚 |
| `提示` | 已按当前状态算出下一步该执行什么 |

**为什么还需要 Agent 之外的手段**：倒计时由 Agent 进程内的定时器驱动。
如果 Agent 在规则加载后被杀掉或机器重启，那个定时器就没了——
此时 `firewall status` 会显示「已超时」，而自动回滚不会发生，
必须有人在终端里执行 `rollback` 或 `unload`。

恢复后建议做的三件事：

```bash
# 1. 确认回环与本机的 IPv6 出口仍然通
ping6 -c 2 ::1
nc -6 -z -w 2 <本机公网 IPv6> 22 && echo "外部可达" || echo "仍不可达"

# 2. 先看这版规则做了什么（规则文件不会被删除）
sudo pfctl -a com.macpanel.ipv6 -sr

# 3. 在面板里改好再应用；先加一条允许自己 SSH 来源的规则
```

> 纪律：**首次在生产 Mac 上启用规则时，务必保留一个不受该规则影响的通道**
> （物理接入、另一条 VLAN、Tailscale）。
> 「网页上能点确认」不等于「远程连接还能用」。

## 4. 完全卸载

```bash
sudo /path/to/uninstall.sh
```

该脚本会：

1. `launchctl bootout` 两个服务并删除 plist；
2. 删除 `/usr/local/bin/macpanel`；
3. 删除 `/var/run/macpanel`；
4. **保留** anchor 文件，并明确提示「需先移除 /etc/pf.conf 引用」；
5. 询问是否删除数据与日志（默认保留）。

**绝不会做的事**：`pfctl -d`、修改 `/etc/pf.conf`、删除其它程序的 anchor 或 plist。

若曾启用 PF 规则（M2 之后），卸载前应先让本应用退出 PF：

```bash
sudo macpanel firewall unload     # 清空本应用 anchor
sudo pfctl -a com.macpanel.ipv6 -sr  # 确认为空
```

再移除 `/etc/pf.conf` 中的引用并 `sudo pfctl -f /etc/pf.conf`，最后执行卸载脚本。

## 5. 灾难恢复清单

在修改任何网络配置前，先确认这四项：

```bash
# 1. 本机能访问自己（IPv6 回环）
ping6 -c 2 ::1

# 2. 能通过自己的公网 IPv6 访问自己
IPV6=$(ifconfig en0 | awk '/inet6 2/{print $2; exit}')
ping6 -c 2 "$IPV6"

# 3. PF 当前状态（记录基线）
sudo pfctl -s info

# 4. 当前 anchor 列表（记录基线）
sudo pfctl -s Anchors
```

若第 1 或 2 项失败，**不要**修改防火墙规则——先修好基础网络。

保留一份可用的终端会话（SSH 或物理接入），并知道如何使用它。
「网页上能点确认」不等于「远程连接仍可用」。

## 6. 恢复相关的设计决策

| 决策 | 原因 |
| --- | --- |
| 不使用 `pfctl -d` 作为「暂停」手段 | 会全局关闭防火墙，影响所有服务，且可能让机器暴露 |
| 卸载不删除 anchor 文件 | 删除后若 `/etc/pf.conf` 仍引用它，PF 重载会报错 |
| 非 Socket 路径直接拒绝启动 | 防止误删用户数据（已有测试 `TestNonSocketPathNotRemoved`） |
| Agent 崩溃后由 launchd 重启 | `KeepAlive.SuccessfulExit=false` + `ThrottleInterval=10` |
| M2 的超时回滚必须由 Agent 持久管理 | Server 可能崩溃或浏览器关闭，回滚不能依赖它们 |
| 回滚之外再提供 `firewall unload` | 倒计时由 Agent 进程内的定时器驱动；Agent 不在时它不会触发，需要一个不挑状态的兜底手段 |
| CLI 救援不经过 IPC | 一旦规则挡住网络，面板那条链路本身可能已经断了 |
| 非 root 时 CLI 自动 dry-run | 宁可让用户看到「什么都没发生」，也不能让非特权进程悄悄改 PF |
