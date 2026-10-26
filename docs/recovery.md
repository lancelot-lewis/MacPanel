# 故障恢复与 CLI 救援

> 对应里程碑：M0 / M1
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
homelab-manager status

# 机器可读输出（便于脚本与粘贴到问题报告）
homelab-manager status --json

# 查看生效配置（不启动任何服务，只打印并校验）
homelab-manager config -mode server
homelab-manager config -mode agent
```

`status` 的行为：

- 读取令牌文件 → 连接 Unix Socket → 调用 `agent.health` / `system.overview` /
  `firewall.status`。
- Agent 不可达时**不报错退出**，而是逐项打印「不可达原因」，便于定位是
  Agent 没起来、令牌不匹配，还是 Socket 权限问题。

## 3. 常见故障与处置

### 3.1 Web 面板打不开

```bash
# 1. Server 进程是否在跑？
launchctl print system/com.homelab.manager.server | head -30

# 2. 端口是否在监听？
lsof -nP -iTCP:7788 -sTCP:LISTEN

# 3. 直接探测（绕过浏览器）
curl -s http://127.0.0.1:7788/api/v1/healthz

# 4. 看日志
tail -50 /Library/Logs/HomeLabManager/server.stderr.log
tail -50 /Library/Logs/HomeLabManager/server.log
```

若 `healthz` 有响应但页面空白：前端资源未正确嵌入，检查安装包是否为
`make build`（含 `web-build`）产物。

### 3.2 Agent 不可达（面板显示「Agent 不可达」）

```bash
# 1. Agent 服务状态
launchctl print system/com.homelab.manager.agent | head -30

# 2. Socket 文件是否存在、权限是否正确
ls -l /var/run/homelab-manager/agent.sock
# 期望：srw-rw---- root:<组> 

# 3. 令牌文件是否存在、权限是否正确
ls -l "/Library/Application Support/HomeLabManager/ipc.token"
# 期望：-rw-r----- root:<组>

# 4. Agent 日志
tail -50 /Library/Logs/HomeLabManager/agent.stderr.log
```

常见原因与处置：

| 现象 | 原因 | 处置 |
| --- | --- | --- |
| Socket 不存在 | Agent 未启动 / 启动即退出 | 看 stderr 日志；`launchctl kickstart -k system/com.homelab.manager.agent` |
| Socket 权限不是 0660 | 目录或文件被手工改动 | 恢复 `chmod 0660`，目录 `0750` |
| 报「已有实例在监听」 | 重复启动 | 先 `launchctl bootout` 旧实例 |
| 报「路径已存在且不是 Socket」 | 该路径被普通文件占用 | 确认内容后再手工移走（程序不会自行删除） |
| `unauthorized` | Server 与 Agent 令牌不一致 | 重新生成令牌并同时重启两者（见 3.3） |

### 3.3 令牌不一致

```bash
# 重新生成（会覆盖旧令牌，必须同时重启两个服务）
sudo /usr/local/bin/homelab-manager token -force
sudo chown root:_homelab "/Library/Application Support/HomeLabManager/ipc.token"
sudo chmod 0640 "/Library/Application Support/HomeLabManager/ipc.token"

sudo launchctl kickstart -k system/com.homelab.manager.agent
sudo launchctl kickstart -k system/com.homelab.manager.server
```

注意：`token -force` 会立即让旧令牌失效，期间 Server 调用会失败。

### 3.4 忘记管理密码

当前没有「找回」机制（不发送邮件、不存明文）。恢复方式：

```bash
# 直接清除密码哈希，然后用本机浏览器重新设置
sudo sqlite3 "/Library/Application Support/HomeLabManager/data/homelab.db" \
  "DELETE FROM settings WHERE key = 'auth.password_hash';"

# 同时撤销所有会话，避免残留登录态
sudo sqlite3 "/Library/Application Support/HomeLabManager/data/homelab.db" \
  "UPDATE auth_sessions SET revoked_at = datetime('now') WHERE revoked_at IS NULL;"

sudo launchctl kickstart -k system/com.homelab.manager.server
```

之后在**本机**打开 `http://127.0.0.1:7788` → 设置 → 设置密码。
必须在物理可及的本机操作，这是刻意的设计：不提供远程重置入口。

### 3.5 数据库损坏

```bash
# 1. 先停服务，避免写入竞争
sudo launchctl bootout system/com.homelab.manager.server

# 2. 检查完整性
sqlite3 "/Library/Application Support/HomeLabManager/data/homelab.db" "PRAGMA integrity_check;"

# 3. 备份现场
sudo cp "/Library/Application Support/HomeLabManager/data/homelab.db" \
        "/Library/Application Support/HomeLabManager/data/homelab.db.corrupt.$(date +%s)"

# 4. 尝试导出可读数据
sqlite3 homelab.db.corrupt.* ".dump" > /tmp/recover.sql 2>/dev/null

# 5. 最坏情况：删除后重建（会丢失审计与设置，防火墙规则不受影响）
sudo rm -f "/Library/Application Support/HomeLabManager/data/homelab.db"*
sudo launchctl bootstrap system /Library/LaunchDaemons/com.homelab.manager.server.plist
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
以及 `launchctl print system/com.homelab.manager.agent` 输出中的运行身份。

### 3.7 端口 7788 被占用

```bash
lsof -nP -iTCP:7788 -sTCP:LISTEN
```

改端口：编辑 `/Library/LaunchDaemons/com.homelab.manager.server.plist` 中的
`HLM_LISTEN_ADDR`，然后：

```bash
sudo launchctl bootout system /Library/LaunchDaemons/com.homelab.manager.server.plist
sudo launchctl bootstrap system /Library/LaunchDaemons/com.homelab.manager.server.plist
```

注意：非回环地址必须同时设置 `HLM_ALLOW_REMOTE=true`，否则启动时会直接报错退出
（这是安全不变量，不是缺陷）。

## 4. 完全卸载

```bash
sudo /path/to/uninstall.sh
```

该脚本会：

1. `launchctl bootout` 两个服务并删除 plist；
2. 删除 `/usr/local/bin/homelab-manager`；
3. 删除 `/var/run/homelab-manager`；
4. **保留** anchor 文件，并明确提示「需先移除 /etc/pf.conf 引用」；
5. 询问是否删除数据与日志（默认保留）。

**绝不会做的事**：`pfctl -d`、修改 `/etc/pf.conf`、删除其它程序的 anchor 或 plist。

若曾启用 PF 规则（M2 之后），卸载前应先通过面板「停用规则」回到无规则状态，
再移除 `/etc/pf.conf` 中的引用并 `sudo pfctl -f /etc/pf.conf`。

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
