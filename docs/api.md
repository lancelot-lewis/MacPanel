# HTTP API 参考

> 版本：v1（M1 只读实现）
> 最后更新：2026-10-26

## 通用约定

### 响应外层

成功：

```json
{
  "ok": true,
  "data": { },
  "meta": {
    "requestId": "abc123",
    "degraded": false,
    "notes": []
  }
}
```

失败：

```json
{
  "ok": false,
  "error": {
    "code": "unavailable",
    "message": "系统概览暂时不可用：…",
    "details": { }
  },
  "meta": { "requestId": "abc123" }
}
```

- `meta.degraded=true` 表示数据可用但存在降级（部分指标不可用），
  此时 `meta.notes` 给出可读原因。
- `meta.requestId` 与服务端访问日志、`X-Request-Id` 响应头一致，便于串联排查。

### 错误码 → HTTP 状态

| code | HTTP | 含义 |
| --- | --- | --- |
| `invalid_argument` | 400 | 参数/请求体非法 |
| `unauthorized` | 401 | 未认证或凭据无效 |
| `forbidden` | 403 | 已认证但无权访问 / 来源被拒绝 |
| `not_found` | 404 | 资源或方法不存在 |
| `conflict` | 409 | 状态冲突 |
| `rate_limited` | 429 | 触发限速 |
| `unavailable` | 503 | 依赖不可用（如 Agent 未运行） |
| `not_implemented` | 501 | 功能尚未实现 |
| `internal` | 500 | 服务端内部错误（细节不对外暴露） |

Host 头不合法时返回 **421 Misdirected Request**（配置错误类，独立于 403）。

### 请求约定

- 变更方法必须 `Content-Type: application/json`，请求体上限 64 KiB。
- 请求体使用严格解析：**未知字段直接报错**（400），不会静默忽略。
- 所有 API 响应 `Cache-Control: no-store`。
- 远程模式下所有 `/api/v1/*`（除 `healthz`、`auth/status`、`auth/login`）都需要：
  1. 有效会话 cookie；
  2. 请求头 `X-HLM-Request: 1`。

---

## GET /api/v1/healthz

无需认证。仅返回存活信息，不含任何系统数据，可安全暴露给反向代理健康检查。

```json
{
  "ok": true,
  "data": { "status": "ok", "version": "0.1.0", "uptimeSec": 128 }
}
```

---

## GET /api/v1/auth/status

查询认证状态。该接口不返回任何系统信息。

```json
{
  "ok": true,
  "data": {
    "mode": "local",
    "authenticated": true,
    "passwordSet": false,
    "requiresPasswordSetup": false,
    "session": null
  }
}
```

| 字段 | 说明 |
| --- | --- |
| `mode` | `local`（仅本机）或 `remote` |
| `authenticated` | 当前请求是否已授权 |
| `passwordSet` | 是否已配置管理密码 |
| `session` | 远程模式已登录时返回会话信息 |

---

## POST /api/v1/auth/login

仅远程模式可用（本地模式返回 501）。

```json
{ "password": "……" }
```

成功：`Set-Cookie: hlm_session=…; HttpOnly; SameSite=Strict`，
返回 `{"session": {...}}`。

失败：

| 情况 | 状态 | code |
| --- | --- | --- |
| 密码错误 / 未设置密码 | 401 | `unauthorized`（统一提示「用户名或密码错误」） |
| 触发限速 | 429 | `rate_limited` |

详情不区分「无密码」与「密码错」，避免探测；真实原因只写入审计。

---

## POST /api/v1/auth/logout

撤销当前会话并清除 cookie。本地模式下也返回成功（幂等）。

```json
{ "ok": true, "data": { "ok": true } }
```

---

## POST /api/v1/auth/password

设置或修改管理密码。

```json
{ "currentPassword": "……", "newPassword": "至少 12 个字符" }
```

| 模式 | 授权条件 |
| --- | --- |
| 本地 | 请求来自回环地址 |
| 远程 | 必须已登录；已设置密码时还需 `currentPassword` 正确 |

- 新密码最短 12 字符（`auth.MinPasswordLength`）。
- 修改密码会**撤销所有会话**；远程模式下立即为新密码发放新会话。
- 已存在密码时缺少 `currentPassword` → 400；密码错误 → 401。

---

## GET /api/v1/meta

返回版本、Agent 状态、访问模式与能力声明。前端据此禁用未实现的功能入口。

```json
{
  "ok": true,
  "data": {
    "server": {
      "version": "0.1.0",
      "commit": "abc1234",
      "buildTime": "2026-10-26T00:00:00Z",
      "goVersion": "go1.27.3",
      "platform": "darwin/arm64"
    },
    "agent": {
      "version": "0.1.0",
      "startedAt": "2026-10-26T00:00:00Z",
      "pid": 1234,
      "platform": "darwin/arm64",
      "isRoot": true,
      "methodCount": 4,
      "serverClock": "2026-10-26T00:00:10Z"
    },
    "agentReachable": true,
    "access": {
      "mode": "local",
      "listenAddr": "127.0.0.1:7788",
      "allowRemote": false
    },
    "features": {
      "firewallRead": true,
      "firewallWrite": false,
      "powerControl": false,
      "serviceControl": false,
      "scheduleControl": false
    }
  }
}
```

Agent 不可达时 `agentReachable=false`，`meta.degraded=true`，
`meta.notes` 给出原因。

---

## GET /api/v1/system/overview

系统概览。前端每 5 秒刷新。

```json
{
  "ok": true,
  "data": {
    "hostname": "mac-mini.local",
    "os": {
      "productVersion": "27.0.1",
      "build": "26A434",
      "kernel": "Darwin 27.0.0",
      "architecture": "arm64"
    },
    "cpu": {
      "model": "Apple M4",
      "physicalCores": 10,
      "logicalCores": 10,
      "loadAverage": { "one": 3.32, "five": 3.51, "fifteen": 3.5 }
    },
    "memory": {
      "totalBytes": 17179869184,
      "usedBytes": 12643008512,
      "freeBytes": 193003520,
      "availableBytes": 4536860672,
      "state": "ok"
    },
    "disks": [
      {
        "mountPoint": "/",
        "fileSystem": "apfs",
        "totalBytes": 245107195904,
        "freeBytes": 85464276992,
        "usedBytes": 159642918912,
        "usedRatio": 0.6513
      }
    ],
    "bootTime": "2026-10-25T01:34:11Z",
    "uptimeSeconds": 84163,
    "collectedAt": "2026-10-26T00:57:02Z",
    "missing": []
  },
  "meta": { "degraded": false }
}
```

### 数据质量语义

| 字段 | 缺失时表现 |
| --- | --- |
| `memory.state` | `unavailable`，`usedBytes`/`freeBytes` 省略 |
| `disks` | 空数组或省略，`missing` 中记录原因 |
| `bootTime` / `uptimeSeconds` | 省略，`missing` 中记录原因 |
| `missing` | `[{ "metric": "memory", "reason": "无法读取内存统计" }]` |

**内存口径**：`usedBytes = totalBytes − (free + speculative + purgeable + inactive) × pageSize`。
与「活动监视器 → 内存已用」接近；`availableBytes` 为上述可回收页总量。

**APFS 说明**：根卷与数据卷共享容器，容量数值可能相同，属系统真实语义。

---

## GET /api/v1/system/network

网络接口快照。前端每 10 秒刷新。

```json
{
  "ok": true,
  "data": {
    "interfaces": [
      {
        "name": "lo0",
        "index": 1,
        "kind": "loopback",
        "up": true,
        "loopback": true,
        "mtu": 16384,
        "hardwareAddr": "",
        "flags": ["up", "loopback", "running"],
        "ipv4": [{ "address": "127.0.0.1", "prefixLen": 8, "family": "ipv4", "scope": "loopback", "isLoopback": true }],
        "ipv6": [{ "address": "::1", "prefixLen": 128, "family": "ipv6", "scope": "loopback", "isLoopback": true }]
      },
      {
        "name": "en0",
        "index": 4,
        "kind": "ethernet",
        "up": true,
        "loopback": false,
        "mtu": 1500,
        "hardwareAddr": "d0:11:e5:c0:5b:d7",
        "flags": ["up", "broadcast", "running", "multicast"],
        "ipv4": [{ "address": "192.168.1.2", "prefixLen": 24, "family": "ipv4", "scope": "private", "isPrivate": true }],
        "ipv6": [
          { "address": "fe80::1cd8:c61e:9555:94c2", "prefixLen": 64, "family": "ipv6", "scope": "link-local", "isLinkLocal": true },
          { "address": "2409:8a55:d6d5:7200:8b2:aae6:8d42:734f", "prefixLen": 64, "family": "ipv6", "scope": "global" }
        ]
      }
    ],
    "hasIPv6": true,
    "hasGlobalIPv6": true,
    "collectedAt": "2026-10-26T00:57:02Z"
  }
}
```

### `kind` 取值

| 值 | 判定依据 |
| --- | --- |
| `loopback` | `net.FlagLoopback` 或名称 `lo0` |
| `ethernet` | 硬件端口名含 "Ethernet"，或名称前缀 `en` |
| `wifi` | 硬件端口名含 "Wi-Fi" |
| `tunnel` | 名称前缀 `utun` / `ipsec` / `gif` / `stf` |
| `bridge` | 名称前缀 `bridge` / `vmenet` |
| `virtual` | 名称前缀 `awdl` / `llw` / `anpi` / `ap` |
| `other` | 其余 |

### `scope` 取值

`loopback` / `link-local` / `private` / `global`

---

## GET /api/v1/firewall/status

IPv6 防火墙只读状态。前端每 10 秒刷新。

```json
{
  "ok": true,
  "data": {
    "managed": "not_configured",
    "pf": {
      "enabled": null,
      "state": "unknown",
      "detail": "当前进程无权限读取 PF 状态（/dev/pf 仅 root 可访问）",
      "readAvailable": false,
      "readError": "pfctl: /dev/pf: Permission denied"
    },
    "anchor": {
      "name": "com.homelab.manager.ipv6",
      "rulePath": "/etc/pf.anchors/com.homelab.manager.ipv6",
      "state": "absent",
      "present": false,
      "referenced": false,
      "notes": ["规则文件不存在（M1 不创建任何 anchor 文件）"]
    },
    "warnings": [
      "无法读取 PF 运行状态（需要 root 权限访问 /dev/pf）；页面显示的 PF 状态为「未知」而不是「已关闭」"
    ],
    "collectedAt": "2026-10-26T00:57:34Z",
    "writable": false
  },
  "meta": { "degraded": true, "notes": ["…"] }
}
```

### 关键语义

| 字段 | 语义 | 禁止的理解 |
| --- | --- | --- |
| `pf.enabled` | `null` = 无法确认；`true`/`false` = 确认状态 | `null` 不等于 `false` |
| `pf.state` | `enabled` / `disabled` / `unknown` | `unknown` 不等于 `disabled` |
| `pf.readAvailable` | 是否成功读取 `/dev/pf` | — |
| `anchor.present` | 规则文件是否存在（Lstat，不需读权限） | 存在 ≠ 已生效 |
| `anchor.referenced` | `/etc/pf.conf` 是否含无注释的引用 | 无权限读取时为 `false`，**不代表**确认未引用 |
| `anchor.state` | `unknown` / `absent` / `present` / `loaded` | `present` ≠ 已加载 |
| `managed` | `not_configured` / `configured` / `drift` | — |
| `writable` | M1 恒为 `false` | — |

`managed` 判定逻辑：

1. `anchor.present && anchor.referenced` → `configured`；
2. 若同时 PF 可读但 anchor 未加载 → `drift` + 警告；
3. 否则 `not_configured`。

---

## GET /api/v1/audit

审计日志，按时间倒序。

| 查询参数 | 约束 | 默认 |
| --- | --- | --- |
| `limit` | 1–500 整数 | 50 |
| `action` | 字符串，最长 100 | 不过滤 |

```json
{
  "ok": true,
  "data": {
    "items": [
      {
        "id": 42,
        "at": "2026-10-26T00:57:34Z",
        "actor": "local:127.0.0.1",
        "action": "firewall.status",
        "target": "com.homelab.manager.ipv6",
        "result": "success",
        "remoteAddr": "127.0.0.1",
        "requestId": "8790dfbe9e214732"
      }
    ],
    "limit": 50
  }
}
```

`result` 取值：`success` / `failure` / `denied`。
`actor` 形式：`local:<ip>` / `remote:<ip>` / `session:<id>`。

审计**不记录**密码、令牌、会话 cookie、请求体全文。

---

## M2：防火墙规则接口

### GET /api/v1/firewall/rules

返回规则集与当前事务状态。

```json
{
  "ok": true,
  "data": {
    "rules": [
      {
        "id": 4, "name": "Caddy HTTPS", "description": "HTTPS 入口",
        "position": 1, "action": "pass", "direction": "in", "protocol": "tcp",
        "portFrom": 443, "portTo": 443,
        "sourceCidr": "any", "interface": "any", "enabled": true,
        "builtinTemplate": "caddy"
      }
    ],
    "defaultDenyInterfaces": ["en0"],
    "transaction": {
      "phase": "pending", "revisionId": 7,
      "contentHash": "…", "appliedAt": "…", "deadline": "…"
    }
  }
}
```

`transaction.phase` 取值：`idle` / `pending` / `confirmed` / `rolled_back` / `failed`。

### PUT /api/v1/firewall/rules

整体替换规则集，返回新生成的预览。不触碰系统防火墙。

请求体：`{ "rules": [...], "defaultDenyInterfaces": ["en0"] }`

规则字段校验（不通过返回 400，并逐条列出问题）：

| 字段 | 约束 |
| --- | --- |
| `name` | 非空，≤64 字符 |
| `action` | `pass` / `block` |
| `direction` | 仅 `in`（本应用只控制入站） |
| `protocol` | `tcp` / `udp` / `icmp6` / `any` |
| `portFrom`/`portTo` | 1–65535；`tcp`/`udp` 必填；`icmp6` 禁止填；两者都有时 from ≤ to |
| `sourceCidr` | `any` 或 IPv6 CIDR（前缀 ≥ /16；拒绝 IPv4） |
| `interface` | `any` 或接口名（小写字母+数字，≤15 字符） |

`defaultDenyInterfaces` 不允许包含 `lo0`、`utun*`、`ipsec*`、`gif*`、`stf*`
（会切断回环或隧道流量）。

### POST /api/v1/firewall/preview

生成 PF 文本但不写文件、不调 pfctl。

请求体可选：`{ "ruleSet": { ... } }`（不传则用已保存的规则）。

返回：

```json
{
  "content": "# 由 HomeLab Manager 自动生成…
pass quick on lo0 all
…",
  "ruleCount": 2,
  "enforced": [
    { "key": "loopback", "reason": "回环接口必须放行…", "rule": "pass quick on lo0 all" }
  ],
  "skipped": [{ "ruleId": 3, "name": "旧规则", "reason": "规则已禁用" }],
  "verification": {
    "ok": true,
    "syntaxErrors": [],
    "invariantViolations": [],
    "stats": { "ruleLines": 8, "passRules": 7, "blockRules": 1, "inet6Rules": 7 }
  }
}
```

### POST /api/v1/firewall/apply

**高风险**。必须带 `confirmsRisk: true`（或请求头 `X-HLM-Confirm-Risk: 1`），
否则返回 400 且不触达 Agent。

流程：生成 → 自校验不变量 → 写 anchor 文件 → `pfctl -n -f` 语法检查
→ 快照当前 → 落盘 pending → 加载 → 返回 deadline。

返回：

```json
{
  "ok": true,
  "data": {
    "revisionId": 7,
    "contentHash": "…",
    "appliedAt": "2026-10-26T02:16:26Z",
    "deadline": "2026-10-26T02:17:26Z",
    "confirmTimeoutSeconds": 60,
    "warnings": ["无法读取当前生效规则快照，回滚能力受限"]
  }
}
```

### POST /api/v1/firewall/confirm

确认当前 pending 配置。幂等。

| 情况 | 状态 | code |
| --- | --- | --- |
| 成功 | 200 | — |
| 无待确认配置 | 409 | `conflict` |
| 已超过 deadline | 409 | `conflict`（并已自动回滚） |

### POST /api/v1/firewall/rollback

主动回滚。请求体可选 `{ "reason": "…" }`（≤200 字符）。

回滚语义：清空本应用 anchor（加载 `/dev/null`），不影响其它程序。

### GET /api/v1/firewall/transaction

返回事务状态。**读操作会顺带检查超时**，因此即使后台定时器未运行，
也能读到正确的 `rolled_back` 状态。

### GET /api/v1/firewall/templates

返回 5 个内置模板（Caddy / sish / Tailscale / EasyTier / SSH 限局域网）。
每个模板带 `note` 字段，提示用户必须核实的点。

## M3：电源与计划任务接口

### GET /api/v1/power/status

```json
{
  "ok": true,
  "data": {
    "pending": {
      "scheduledAt": "2026-10-26T12:32:51Z",
      "reason": "系统更新后重启",
      "requestedBy": "local:127.0.0.1",
      "createdAt": "2026-10-26T12:22:51Z",
      "taskId": 0
    },
    "canReboot": true,
    "rebootUnavailableReason": "",
    "lastRebootAt": "2026-10-24T04:20:31Z",
    "uptimeSeconds": 172846,
    "timezone": "Asia/Shanghai",
    "scheduleDaemonInstalled": true
  }
}
```

`pending` 缺失表示没有待执行的重启。`canReboot` 为 false 时
`rebootUnavailableReason` 说明原因（如「当前进程不是 root」）。

### POST /api/v1/power/reboot

**高风险**。必须带 `confirmsRisk: true`（或请求头 `X-HLM-Confirm-Risk: 1`）。

请求：

```json
{ "delaySeconds": 600, "reason": "系统更新后重启", "confirmsRisk": true }
```

| 字段 | 约束 |
| --- | --- |
| `delaySeconds` | 0-3600（0 表示尽快）。上限 60 分钟与命令层守卫一致 |
| `reason` | ≤200 字符 |

错误：

| 情况 | 状态 | code |
| --- | --- | --- |
| 缺少风险确认 | 400 | `invalid_argument` |
| 已有待执行的重启 | 409 | `conflict` |
| 不具备重启能力（非 root） | 503 | `unavailable` |

### POST /api/v1/power/cancel

取消待执行的重启。幂等：没有待执行重启时也返回成功。

### 计划任务

| 方法 | 路径 | 说明 |
| --- | --- | --- |
| GET | `/api/v1/schedules` | 列表 |
| POST | `/api/v1/schedules` | 创建 |
| PUT | `/api/v1/schedules/{id}` | 更新（路径 ID 是唯一权威，忽略 body 里的 id） |
| DELETE | `/api/v1/schedules/{id}` | 删除（同时移除 launchd 配置） |
| GET | `/api/v1/schedules/executions?taskId=&limit=` | 执行记录 |
| POST | `/api/v1/schedules/{id}/run` | 立即执行（需风险确认） |
| POST | `/api/v1/schedules/sync` | 把任务同步到 launchd |

创建请求：

```json
{
  "name": "每日凌晨重启",
  "kind": "reboot",
  "timezone": "Asia/Shanghai",
  "recurrence": { "kind": "daily", "timeOfDay": "04:30" },
  "enabled": true,
  "catchUp": "never",
  "catchUpWindowSeconds": 0,
  "minIntervalSeconds": 1800
}
```

`recurrence.kind` 取值与必填字段：

| kind | 必填 | 说明 |
| --- | --- | --- |
| `daily` | `timeOfDay`（"HH:MM"） | 每天固定时刻 |
| `weekly` | `timeOfDay` + `weekdays`（0=周日） | 每周指定几天 |
| `interval_hours` | `intervalHours`（1-8760） | 以上次执行时间为基准 |
| `once` | `onceAt`（RFC3339，未来时刻） | 单次；走 Agent 持久化倒计时而不是 launchd |

约束（不通过返回 400）：

- `minIntervalSeconds` **不得低于 1800**（30 分钟）——防循环重启的硬性下限
- `catchUp` 为 `within_window` 时必须设 `catchUpWindowSeconds`，且 ≤7 天
- `timezone` 必须是有效的 IANA 名

任务对象的只读字段（`createdAt` / `updatedAt` / `lastRunAt` / `failCount`
/ `launcherLabel`）由服务端维护，**客户端回传会导致解析失败**（time 字段空串）。
提交时只发上面列出的可编辑字段。

## M4：启动项接口

### GET /api/v1/services?includeGui=1&guiUid=

```json
{
  "ok": true,
  "data": {
    "items": [
      {
        "label": "ddns-go",
        "domain": "system",
        "kind": "third_party",
        "pid": 329,
        "running": true,
        "plistPath": "/Library/LaunchDaemons/ddns-go.plist",
        "managed": true,
        "controllable": false,
        "blockedReason": "已登记但未授权启停（可在此授权）"
      }
    ],
    "summary": {
      "total": 902, "systemCritical": 874, "self": 0,
      "thirdParty": 28, "managed": 1, "running": 426
    },
    "warnings": []
  }
}
```

`kind` 三类：`system_critical`（永久只读）、`self`（本应用，永久只读）、
`third_party`（默认只读，需登记授权）。

`warnings` 非空表示部分域读取失败（能读到的部分仍会返回）。

### GET /api/v1/services/detail?label=&domain=

返回 plist 路径、启动命令、RunAtLoad/KeepAlive、日志路径
（未声明时给 `log show` 查询命令）。

### POST /api/v1/services/register

```json
{
  "label": "ddns-go",
  "domain": "system",
  "plistPath": "/Library/LaunchDaemons/ddns-go.plist",
  "displayName": "DDNS 客户端",
  "allowControl": false
}
```

校验（不通过返回 400）：

- `label` 不得是 `com.apple.*` 或 `com.homelab.manager.*`
- `domain` 只接受 `system`
- `plistPath` 必须位于 `/Library/LaunchDaemons/` 或 `/System/Library/LaunchDaemons/`，
  不得含 `..`，必须是 `.plist`，且文件名必须等于 `<label>.plist`
- 同一 label 重复登记是幂等覆盖

`allowControl=false` 表示「只登记观察」，列表中显示 `managed=true`
但 `controllable=false`。

### DELETE /api/v1/services/register?label=

取消登记。服务本身不受影响。

### POST /api/v1/services/control

**高风险**。必须带 `confirmsRisk: true`。

```json
{ "label": "ddns-go", "action": "restart", "confirmsRisk": true }
```

`action`：`start` / `stop` / `restart`。

三层拦截（顺序即在代码中的检查顺序）：

1. 标签形态：`com.apple.*` 与本应用自身一律拒绝（403）
2. 受管白名单：未登记或未授权 → 403，且给出具体原因
3. 路径来源：只用登记时记录的 plist 路径，不接受请求传入

`start` / `stop` 幂等：已加载时 `start` 是空操作，未加载时 `stop` 是空操作。

## M6：网络诊断与容器接口

### GET /api/v1/diagnostics

```json
{
  "ok": true,
  "data": {
    "ipv6Routes": [
      { "destination": "default", "gateway": "fe80::1%en0",
        "flags": "UGcg", "interface": "en0" }
    ],
    "ipv6Listeners": [
      { "protocol": "tcp46", "address": "*", "port": 443 }
    ],
    "missing": [
      { "metric": "ipv6Listeners",
        "reason": "netstat 未返回标准连接表（可能受权限或运行环境限制）" }
    ],
    "collectedAt": "2026-10-26T04:22:51Z"
  }
}
```

**`missing` 非空表示对应项不可用，而不是「值为 0」**。
例如 `ipv6Listeners` 缺失时不能理解为「没有监听端口」。

### POST /api/v1/diagnostics/tcp

```json
{ "host": "::1", "port": 443, "timeoutMs": 3000 }
```

返回：

```json
{
  "target": "[::1]:443", "host": "::1", "port": 443,
  "reachable": false,
  "outcome": "refused",
  "detail": "连接被拒绝：目标端口没有服务在监听",
  "probedAt": "2026-10-26T04:22:51Z"
}
```

`outcome` 的语义（**不要合并成一个「失败」**）：

| outcome | 含义 | 排查方向 |
| --- | --- | --- |
| `connected` | 握手成功 | — |
| `refused` | 端口没有服务在监听 | 服务是否启动、监听地址是否正确 |
| `timeout` | 无响应 | 防火墙（PF 规则）是否丢弃了包、主机是否在线 |
| `unreachable` | 无路由 | 地址拼写、接口状态、路由表 |

目标默认限本机（含本机已配置的接口地址）。探测外部地址需
`HLM_ALLOW_NONLOCAL_PROBE=1`。

### GET /api/v1/orbstack/containers

```json
{
  "ok": true,
  "data": {
    "status": {
      "available": true,
      "socketPath": "/var/run/docker.sock",
      "version": "API 1.56"
    },
    "items": [
      { "id": "abc123def456", "name": "caddy", "image": "caddy:2-alpine",
        "state": "running", "status": "Up 2 hours (healthy)",
        "ports": ["tcp/443 → *:443", "tcp/80（未映射）"] }
    ]
  }
}
```

`status.available=false` 时 `items` 为空数组，`status.reason` 说明原因
（未安装 OrbStack / Socket 不可访问）。**这是正常状态，不是错误。**

本接口**只读**，没有任何写操作。原因见 `docs/security.md`：
Docker Socket 权限等价于 root。

## 未实现接口

路由表中已没有 501 占位。以下能力尚未实现，接口不存在（而非返回 501）：

| 能力 | 说明 |
| --- | --- |
| 容器启停 | 需单独的授权与审计设计 |
| PF 规则命中统计 | 需要解析 `pfctl -sr -vv` 的字节/包计数 |
| Tailscale / EasyTier 状态 | 需读取各自的本地状态文件或 CLI |

调用这些接口会写入审计（`result=denied`，`reason=not_implemented`），
便于确认「有人试图使用未实现功能」。

---

## 前端调用示例

```ts
// 所有请求都带 X-HLM-Request，远程模式下后端强制要求
const res = await fetch('/api/v1/firewall/status', {
  headers: { 'X-HLM-Request': '1', Accept: 'application/json' },
  credentials: 'same-origin',
  cache: 'no-store',
})
const body = await res.json()
if (!body.ok) throw new Error(body.error.message)
if (body.meta?.degraded) console.warn('降级：', body.meta.notes)
```
