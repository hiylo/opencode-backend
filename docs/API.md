# OpenCode Backend API 契约

> 供客户端（Android / Web）与后端并行开发的接口规范。Base URL 形如 `http://<host>:8080`。

## 鉴权模型

后端有两套凭据，**互不通用**：

| 凭据 | 用途 | 传递方式 |
|------|------|---------|
| **Web Session** | 浏览器/配置页登录管理 | Header `X-Web-Session: <sid>` |
| **APP Token** | Android/Web 客户端调编排 API | Header `Authorization: Bearer <token>` 或 WS 的 `?token=` |

- 首次启动生成默认管理密码（`--default-admin-password`，默认 `admin`），登录后可修改。
- Token 由 Web 配置页生成，带 `ocb_` 前缀，**仅显示一次**，按设备命名、可撤销。
- 错误统一返回 `{"error":"描述"}`，非 2xx 状态码。

## 公共端点

### GET /api/health（免鉴权）
```json
{ "status": "ok", "upstream": true, "upstreamError": "", "time": "..." }
```
- `upstream`：后端能否连到本机 OpenCode。

### GET /api/system（免鉴权）
```json
{ "backend": "opencode-backend", "opencodeURL": "http://127.0.0.1:4096", "opencodeVersion": "v1.2.3", "db": "sqlite" }
```

### GET /（免鉴权）
无头模式提示页；配置页资产嵌入时返回 index.html。

## Web 配置（Web Session）

### POST /api/web/session（免鉴权）
请求：`{"password":"admin"}`
- 200 → `{"session":"<sid>"}`（存 localStorage，24h 有效）
- 401 → `{"error":"invalid password"}`

### DELETE /api/web/session（需 X-Web-Session）
退出登录。→ `{"ok":true}`

### POST /api/web/password（需 X-Web-Session）
请求：`{"newPassword":"新密码"}`（≥4 位）
- 200 → `{"ok":true}`；400 → 密码太短

### GET /api/tokens（需 X-Web-Session）
```json
[{ "id":"...", "name":"我的手机", "tokenHash":"...", "createdAt":"...", "revokedAt":null, "lastUsed":"..." }]
```

### POST /api/tokens（需 X-Web-Session）
请求：`{"name":"设备名"}`
- 201 → `{"token":"ocb_..."}`（仅显示一次）

### DELETE /api/tokens/{id}（需 X-Web-Session）
撤销。→ `{"ok":true}`；404 → 不存在

## 编排 API（APP Token）

### GET /api/projects（需 Token）
本机 OpenCode 当前会话分组列表：
```json
{ "projects": [ { "id":"ses_...", "busy":true } ] }
```

### GET /api/projects/{dir}（需 Token）
单个项目会话详情（当前返回与 projects 相同聚合结构）。

### GET /api/tasks?status=queued（需 Token）
任务列表（最新在前，最多 50 条），`status` 可选过滤：
```json
{ "tasks": [ {
  "id":"task_...", "sessionId":"ses_...", "directory":"/path",
  "prompt":"...", "status":"queued|running|succeeded|failed|canceled",
  "error":"", "result":"", "progress":"", "attempts":0,
  "createdAt":"...", "updatedAt":"...", "startedAt":null, "finishedAt":null
} ] }
```

### POST /api/tasks（需 Token）
请求：
```json
{ "prompt":"给所有 controller 加日志", "sessionId":"ses_...(可选)", "directory":"/path(可选,新会话用)" }
```
- 201 → 完整 Task 对象（含新生成的 id）

### GET /api/tasks/{id}（需 Token）
单个任务详情。

### DELETE /api/tasks/{id}（需 Token）
取消任务（仅 queued/running）。→ `{"ok":true}`；409 → 已结束

### POST /api/batch（需 Token）
一条指令对多个 target 批量建任务：
```json
{ "prompt":"给所有模块加日志", "targets":[ {"directory":"/a"}, {"sessionId":"ses_..."} ] }
```
- 201 → `{"created":["task_..."],"count":2}`

### POST /api/archives（需 Token）
把远端会话归档到后端存储。请求：`{"sessionId":"...", "format":"markdown"|"json"}`（format 默认 markdown）
- 201 → `{"id":"arch_...","size":123,"format":"markdown"}`

### GET /api/archives（需 Token）
归档元数据列表（不含内容），`?limit=`。→ `{"archives":[...]}`

### GET /api/archives/{id}（需 Token）
完整归档（含 `content`）。

### DELETE /api/archives/{id}（需 Token）
删除归档。→ `{"ok":true}`；404 → 不存在

## 自动化规则（Web Session）

### GET /api/rules（需 X-Web-Session）
规则列表。→ `{"rules":[...]}`

### POST /api/rules（需 X-Web-Session）
```json
{ "name":"每晚测试", "kind":"cron|git|http", "schedule":"5m 或 cron 或 target", "directory":"/path", "prompt":"指令", "enabled":true }
```
- 201 → 完整 Rule 对象（含新生成的 id）

### DELETE /api/rules/{id}（需 X-Web-Session）
删除规则。→ `{"ok":true}`；404 → 不存在

### POST /api/webhook?target=xxx
触发匹配的 http 规则（无需鉴权，由调用方如 git webhook 使用）。→ `{"fired":true}`；404 → 无匹配规则

## 审计与统计（Web Session）

### GET /api/audit?tokenId=&limit=（需 X-Web-Session）
最近审计记录（token 认证的 API 调用）。→ `{"audit":[...]}`

### GET /api/stats（需 X-Web-Session）
用量统计：
```json
{ "tasks":{"queued":0,"running":0,"succeeded":5,"failed":1,"canceled":0,"retried":1,"total":6},
  "tokenUsage":[{"tokenId":"...","tokenName":"我的手机","calls":12}],
  "archives":3 }
```

## 推送通道（WebSocket）

### GET /api/ws?token=xxx（需 Token，query 参数）
升级 WebSocket 长连，后端向客户端实时推送事件。连接建立后先收到 `subscribed`。

事件帧（JSON）：
```json
{ "type":"task.event", "payload":{"id":"task_...","status":"running|succeeded|failed"}, "severity":"info" }
{ "type":"upstream.health", "payload":{"healthy":true,"time":"..."} }
```

| type | payload | 说明 | severity |
|------|---------|------|----------|
| `subscribed` | — | 订阅成功 | info |
| `task.event` | `{id,status}` | 任务状态变更 | running/succeeded=info, retrying=warning, failed=critical |
| `upstream.health` | `{healthy,time}` | 本机 OpenCode 可达性心跳（30s） | info |

## 流式对话（SSE 中继）

### GET /api/stream（需 Token，Authorization: Bearer）
把上游 OpenCode 的**全局 SSE 事件流**（`/global/event`）原样中继给客户端。APP 通过它维持单一稳定连接即可实时收到对话流式输出，无需直连 OpenCode。

响应 `Content-Type: text/event-stream`，先发握手再逐事件转发：
```
event: connected
data: {}

data: {"directory":"/workspaces/opencode","payload":{"id":"evt_...","type":"message.part.delta","properties":{...}}}
data: {"payload":{"type":"message.part.updated","properties":{...}}}
```

- 上游断连时后端**自动重连**（指数退避，客户端无需感知）
- 事件逐条 `data:` 原样透传，字段结构与直连一致
- 主要事件类型：`server.connected`、`message.part.delta`、`message.part.updated`、`session.*`、`turn.completed`

## 错误码汇总

| 状态码 | 含义 |
|--------|------|
| 400 | 参数错误 / 密码太短 |
| 401 | 未授权（Web Session 或 Token 无效/已撤销） |
| 404 | 资源不存在 |
| 409 | 任务已结束无法取消 |
| 500 | 服务内部错误 |

## 状态码表（任务）

`queued` → `running` → `succeeded` / `failed`；`queued`/`running` → `canceled`；`retrying` = 失败后排队重试（带退避）
