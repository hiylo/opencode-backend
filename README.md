# OpenCode Backend

OpenCode 客户端的轻量独立后端（Go 单二进制）。运行在开发机上连接本机 OpenCode，为 APP 提供**编排与推送**能力。APP 可直连 OpenCode，也可先连本后端再访问 OpenCode。

## 特性（第一期）

- **单二进制无头服务**：一个二进制一个进程，`--listen` 指定端口即可
- **Web 配置页**：默认密码登录（可修改），Token 生成/撤销、系统状态、日志
- **双数据库**：SQLite 默认（纯 Go 驱动），PostgreSQL 可选（同一迁移文件）
- **双凭据**：Web Session 管配置页，APP Token 管编排 API，互不通用
- **会话编排**：`/api/projects` 聚合本机 OpenCode 所有会话
- **异步任务队列**：提交即返回，后台调度 agent 执行，进度/结果/重试，WS 推送状态
- **实时推送**：WebSocket 长连（`/api/ws?token=`），任务状态、上游健康心跳

## 构建

```bash
go build -o opencode-backend ./cmd/opencode-backend
```

## 运行

```bash
# SQLite（默认）
./opencode-backend --listen :8080 --default-admin-password admin

# PostgreSQL
./opencode-backend --listen :8080 --db postgres --pg-dsn "postgres://user:pass@host/db"
```

配置项也支持环境变量：`OCB_LISTEN`、`OCB_DB`、`OCB_SQLITE_PATH`、`OCB_PG_DSN`、`OCB_OPENCODE_URL`、`OCB_ADMIN_PASSWORD`。

## 一键安装

```bash
curl -fsSL https://<host>/install.sh | bash
```

或本地 `bash scripts/install.sh`。安装为 systemd 服务。

## API

见 [docs/API.md](docs/API.md)。配置页为 `/`（`index.html`）。

## 目录结构

```
cmd/opencode-backend  入口
internal/
  config     flag/env 配置
  store      SQLite/Postgres 存储抽象 + 迁移
  auth       Web 密码 + APP Token
  server     HTTP/WS 路由与 handler
  opencode   本机 OpenCode 客户端
  tasks      异步任务调度器
  push       WS 推送 Hub
  webui      嵌入的配置页
scripts/install.sh
docs/API.md
```
