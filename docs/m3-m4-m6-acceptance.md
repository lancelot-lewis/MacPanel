# M3/M4/M6 验收记录

> 记录日期：2026-10-26
> 结论：**功能实现完成，逻辑层经实机验证；涉及特权写操作的部分需在部署环境复验。**

本文件记录 M3（电源与计划任务）、M4（启动项管理）、M6（网络诊断 + OrbStack）的实现结果与验证状态。
与 `m1-acceptance.md`、`m2-acceptance.md` 的分工一致：本文件只讲这三个里程碑。

---

## 1. 交付范围

| 模块 | 内容 | 新增文件 |
| --- | --- | --- |
| M3 调度 | 结构化递归表达式、下次执行计算、执行决策、持久化、launchd 集成、崩溃恢复 | `internal/schedule`、`internal/taskstore`、`internal/tasksched`、`internal/model/schedule.go` |
| M4 启动项 | launchctl 输出解析、plist 解析、服务分类、受管白名单、受控启停 | `internal/platform/darwin/services`、`internal/servicestore`、`internal/model/service.go` |
| M6 诊断 | TCP 连通性检测、IPv6 路由与监听、OrbStack 只读容器列表 | `internal/platform/darwin/diag`、`internal/platform/orbstack`、`internal/model/diagnostics.go` |
| 前端 | 电源与计划任务页、启动项页、网络诊断增强、OrbStack 页 | `web/src/pages/PowerPage.vue`、`ServicesPage.vue`、`OrbStackPage.vue` |
| 基础设施 | launchctl / shutdown / plutil / netstat 的参数级守卫 | `internal/platform/execx` |

**Agent 方法数从 12 增至 30**：M3 加 10 个、M4 加 5 个、M6 加 3 个。

---

## 2. 已验证（本机实测）

以下项目在本机（macOS 27，非 root，dev 模式）真实执行过。

### 2.1 调度时间计算

| 项目 | 结果 |
| --- | --- |
| 每日 04:30，当前 12:00 → 次日 04:30 | 正确 |
| 恰好等于计划时刻时取次日（严格晚于 now） | 正确 |
| 每周一/周四 03:00，周一 05:00 → 周四 03:00 | 正确 |
| 跨年：12-31 23:30 → 次年 01-01 23:00 | 正确 |
| 固定间隔以上次执行时间为基准（非整点对齐） | 正确 |
| 关机 3 天后不返回过去时刻 | 正确 |
| DST 春季跳变时跳过不存在的墙钟时刻 | 正确（实测 2026-03-08 02:30 不存在 → 落到 03-09） |
| 同一绝对时刻在不同任务时区下的结果不同 | 正确 |

### 2.2 launchd plist 生成

实测生成内容（dev 模式，任务 ID=1）：

```xml
<key>Label</key><string>com.macpanel.task.1</string>
<key>ProgramArguments</key>
<array>
    <string>/.../bin/macpanel</string>
    <string>task-run</string>
    <string>--task</string>
    <string>1</string>
    <string>--dev</string>
</array>
<key>StartCalendarInterval</key><array><dict>
    <key>Hour</key><integer>4</integer>
    <key>Minute</key><integer>30</integer>
</dict></array>
<key>RunAtLoad</key><false/>
```

- `RunAtLoad` 为 `false`（否则每次安装都会立即执行一次重启）
- `ProgramArguments` 首个元素是本应用二进制的绝对路径
- 路径为绝对路径（相对路径会让 plist 校验失败）

### 2.3 执行与保护机制

用 `task-run --task 1 --dev` 模拟 launchd 触发：

```text
第一次：结果 success，已提交重启请求
第二次：结果 skipped，"距上次执行仅 0 秒，低于最短间隔 30 分钟（防循环重启）"
```

两次退出码均为 0（跳过不是失败）。

### 2.4 重启排定与取消

| 操作 | 结果 |
| --- | --- |
| 不带 `confirmsRisk` 排定 | HTTP 400，未触达 Agent |
| 带确认排定 10 分钟后重启 | 成功，返回 `pending.scheduledAt` |
| 重复排定 | HTTP 409（冲突），不会叠加 |
| 取消 | 成功，`pending` 变 null |
| 取消后再取消 | 成功（幂等） |

### 2.5 启动项扫描

实测结果（本机，含 GUI 域）：

```text
服务总数 902｜运行中 426｜系统关键 874｜第三方 28｜已受管 1
```

- `com.apple.*` 全部归类为系统关键（只读）
- `ddns-go`、`com.netease.uuremote.daemon`、`SparkleService` 等归类为第三方
- `com.homelab.ipv6`（非本项目的服务）也出现在第三方列表中

服务详情解析正确（实测 `ddns-go`）：

```text
启动命令 /Users/han/ddns-go -l :9876 -f 300 -cacheTimes 5 -c /Users/han/.ddns_go_config.yaml
RunAtLoad: false｜KeepAlive: true
```

### 2.6 受管登记的拦截

| 操作 | 结果 |
| --- | --- |
| 登记 `ddns-go`（allowControl=false） | 成功，列表中 `managed=true` / `controllable=false` |
| 未登记就尝试停止 | 被拒绝（非 root 时先报能力不可用） |
| 登记 `plistPath=/tmp/evil.plist` | 拒绝：「plist 路径必须位于 /Library/LaunchDaemons 或 /System/Library/LaunchDaemons 下」 |
| 登记 `com.apple.Finder` | 拒绝：「不允许登记系统关键服务」 |

### 2.7 网络诊断

| 项目 | 结果 |
| --- | --- |
| IPv6 路由表 | 89 条，目标/网关/接口/标志解析正确 |
| TCP 探测 `127.0.0.1:7788`（本机 Server） | `connected` |
| TCP 探测 `127.0.0.1:59999`（未监听） | `refused`（"端口没有服务在监听"） |
| TCP 探测 `::1:443`（IPv6） | `connected`（[::1]:443 格式正确） |
| 探测 `8.8.8.8:53` | 拒绝：「只能检测本机地址」 |
| 非法端口 0 / 65536 | 拒绝 |

### 2.8 OrbStack 只读

实测（本机 OrbStack 正在运行）：

```text
available: true｜version: API 1.56｜socket: /var/run/docker.sock
容器数: 31，其中 29 运行中
```

容器信息解析正确（名称去前导斜杠、ID 截断 12 位、端口区分映射与未映射）：

```text
dpanel      running  tcp/8080 → *:8080
dozzle      running  tcp/8080（未映射）
```

### 2.9 前端

| 项目 | 结果 |
| --- | --- |
| 电源与计划任务页 | 渲染正常，0 控制台警告 |
| 任务创建（每周一/周四 03:00） | 成功，列表显示「每周一、周四 03:00」 |
| 启动项页 | 902 个服务中默认展示 28 个第三方 |
| 网络诊断页 | TCP 检测点击后输出「连接成功 · [::1]:443」 |
| OrbStack 页 | 「29 / 31 运行中」，表格正常 |
| 浅色/深色对比度 | 4 个页面 × 2 主题 = **0 处不达标** |
| 横向溢出 | 1440px 与 390px 均为 0 |

---

## 3. 未验证（需在部署环境执行）

**这些项目的逻辑已实现并有测试覆盖，但真实系统交互未在本机执行过。**

### 3.1 M3：真实重启与真实 launchd 调度

本机 `sudo` 不可用，因此：

- [ ] `shutdown -r now` 真实执行（本地 `shutdownGuard` 允许，但**未真实调用**）
- [ ] `launchctl bootstrap system <plist>` 真实加载
- [ ] launchd 在计划时刻真实触发 `task-run` 子命令
- [ ] 执行完后 `launchctl bootout` 卸载
- [ ] 系统重启后 plist 仍被自动加载并继续调度
- [ ] 待执行重启在系统重启后：已生效的应被清除，未到期的应保留

**必须验证的关键场景**：

```text
1. 创建「5 分钟后重启」的任务并等它执行 → 机器应重启
2. 重启后检查：任务列表中 last_result=success，next_run_at 已推进
3. 创建「每天 04:30 重启」→ 检查 /Library/LaunchDaemons/ 下的 plist 内容
4. 手动 launchctl kickstart 触发一次 → 检查审计与执行记录
```

### 3.2 M4：真实服务启停

- [ ] `launchctl kickstart -k system/<label>` 真实重启第三方服务
- [ ] `launchctl bootout` 真实停止服务
- [ ] `launchctl bootstrap` 重新启动服务
- [ ] 确认停止服务后列表状态在下次刷新时变为「已停止」
- [ ] 确认对 `com.apple.*` 的操作请求在**三层**都被拒绝

**风险提示**：不要在远程连接上停止提供该连接的通道（如 VPN、远程桌面）。

### 3.3 M6：监听端口列表

本机受限环境下 `netstat -an` 只返回 multipath 段，未返回标准连接表，
因此：

- [ ] 在正常终端环境（非沙箱）确认监听端口列表能显示出来
- [ ] 确认 `tcp46` 双栈监听被正确识别（解析逻辑已用真实输出片段测试）
- [ ] 确认 LISTEN 与 ESTABLISHED 被正确区分

解析逻辑本身已用 `netstat` 真实输出片段做过单元测试（含 `tcp46`），
问题只在于**本机运行环境读不到那份输出**。因此代码现在会在读不到时
明确报告「不可用」，而不是显示「0 个监听」。

### 3.4 环境变量参考

| 变量 | 作用 | 默认 |
| --- | --- | --- |
| `MACPANEL_ALLOW_NONLOCAL_PROBE` | 设为 `1` 允许 TCP 探测外部地址 | 关闭 |
| `MACPANEL_PUBLIC_HOSTS` | 反向代理部署时额外允许的 Host | 空 |
| `MACPANEL_LISTEN_ADDR` / `MACPANEL_ALLOW_REMOTE` | 临时覆盖监听配置（优先于 `server.conf`） | 见 `server.conf` |

监听地址与 `allow_remote` 的常驻取值在
`/Library/Application Support/MacPanel/server.conf`（pkg 升级不覆盖），
用 `sudo macpanel config set listen 0.0.0.0:7788` 修改。

---

## 4. 测试与覆盖率

**368 个测试全部通过**（含 `-race`），`go vet` 与 `gofmt` 干净。

按包覆盖率（`go test ./... -cover`）：

```text
internal/platform/darwin/pfrules    88.2%
internal/audit                      89.6%
internal/firewallstore              81.4%
internal/schedule                   80.1%   ← M3
internal/config                     80.0%
internal/platform/darwin/services   79.9%   ← M4
internal/auth                       79.9%
internal/taskstore                  78.0%   ← M3
internal/platform/darwin            77.7%
internal/platform/orbstack          75.3%   ← M6
internal/servicestore               72.6%   ← M4
internal/database                   71.2%
internal/tasksched                  71.0%   ← M3
internal/platform/darwin/diag       71.0%   ← M6
internal/platform/execx             69.6%
internal/ipc                        68.1%
internal/platform/darwin/launchd    65.8%   ← M3
internal/server                     57.1%
internal/platform/darwin/firewalld  43.2%
internal/agent                      12.8%   ← 见下方说明
──────────────────────────────────────────
合计                                 54.5%
```

**两处低覆盖需要说明**（不是遗漏，是环境限制）：

- `firewalld` 43.2%：未覆盖部分是真实 `pfctl` 调用，需 root。
- `agent` 12.8%：M2~M6 的 handler 需要完整装配依赖才可达。
  当前只覆盖「依赖未就绪 → 返回未实现」的降级路径。
  真正的行为验证在 API 层（`internal/server`）用桩实现完成，
  以及本文第 2 节的实机验证。

---

## 5. 本阶段发现并修复的缺陷

这些都是实现过程中真实踩到的问题，多数是「跑通了但结论错」的类型。

### 5.1 M3

| 现象 | 根因 | 修复 |
| --- | --- | --- |
| 任务创建成功但 launchd 里什么都没有 | dev 模式的 `.dev/logs` 是相对路径，`StandardOutPath` 校验失败；且同步失败只写日志 | 路径改绝对；Create/Update 前先预校验能否生成 plist，不能就报错 |
| 「10 分钟后重启」变成立即重启 | 定时器用 `time.Until`（真实时钟）算延迟，但目标时刻来自注入时钟 | 统一用调度器自己的时钟 |
| 已登记的任务在列表里显示成「未登记」 | `Enrich` 只接受一个集合，把「已登记」与「已授权」混为一谈 | 接受两个集合，分别表达 |
| 「每天重启都失败」与实际相反 | 重启任务执行后进程被杀会留下 running 记录，一律标失败 | 传入系统启动时刻：执行时刻早于本次启动即判定成功 |
| 关闭括号外的 `push` 后任务列表为空 | 空切片序列化为 `null` 而不是 `[]` | 前端统一按 `?? []` 处理 |

### 5.2 M4

| 现象 | 根因 | 修复 |
| --- | --- | --- |
| `ddns-go` 在服务列表中凭空消失 | 标签校验要求「必须含点」，而 launchd 允许无点标签 | 放宽为「字符集 + 至少一个字母」，加回归测试 |
| 启动项页面填入 8888 仍报「必填」 | Naive UI 的 `required` 对 `NInputNumber` 判定不可靠 | 只用自定义 validator（M2 已踩过，此处复用经验） |

### 5.3 M6

| 现象 | 根因 | 修复 |
| --- | --- | --- |
| 「没有 IPv6 监听端口」而实际有 28 个 | 协议列有 `tcp46`（双栈），只匹配了 `tcp6` | 匹配 `tcp6/tcp46/udp6/udp46` |
| 读不到连接表时显示「0 个监听」 | 受限环境下 `netstat -an` 只返回 multipath 段 | 检查标志性标题行，缺失则报告「不可用」 |
| `Status` 报的 Socket 路径与实际连接的不一致 | 构造函数创建了两个 `Client` 实例 | 复用同一实例 |
| 采集失败时同时显示「不可用」和「没有监听」 | 空态文案未考虑缺失状态 | 有缺失时不显示空态 |

---

## 6. 与 M2 的一处交叉发现

`com.homelab.ipv6` launchd 服务确实存在于本机（`/Library/LaunchDaemons/com.homelab.ipv6.plist`），
这解释了 M2 阶段那个来源不明的 `/etc/pf.anchors/com.homelab.ipv6` anchor。

**它不是本项目创建的**，本项目使用 `com.macpanel.ipv6`，两者互不干扰。
M4 的服务列表会把它显示为一个普通第三方服务，可正常观察但需显式登记才可操作。

---

## 7. 下一阶段待办

功能层面 M0–M4、M6 已完成。剩余工作：

1. **M5 安装与测试**（PKG/DMG 已有脚手架，未做干净系统验证）
2. **实机验证**：本文第 3 节的全部项目
3. **M2 收尾**：`docs/testing.md` 4.7 节的防火墙实机清单
4. 可选增强：
   - 容器启停（需单独的授权与审计设计）
   - PF 规则命中统计
   - Tailscale / EasyTier 状态查看
