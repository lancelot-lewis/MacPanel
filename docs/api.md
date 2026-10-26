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

## 未实现接口（返回 501）

以下接口在路由中显式注册，统一返回 `not_implemented`，
用于让前端尽早暴露「功能未就绪」而不是静默失败：

| 方法 | 路径 | 计划里程碑 |
| --- | --- | --- |
| GET | `/api/v1/firewall/rules` | M2 |
| PUT | `/api/v1/firewall/rules` | M2 |
| POST | `/api/v1/firewall/apply` | M2 |
| POST | `/api/v1/firewall/confirm` | M2 |
| POST | `/api/v1/firewall/rollback` | M2 |
| GET | `/api/v1/schedules` | M3 |
| POST | `/api/v1/schedules` | M3 |
| POST | `/api/v1/power/reboot` | M3 |
| GET | `/api/v1/services` | M4 |

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
