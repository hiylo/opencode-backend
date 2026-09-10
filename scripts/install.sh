#!/usr/bin/env bash
# opencode-backend 一键安装脚本
#
# 用法:
#   curl -fsSL https://<host>/install.sh | bash
# 或本地:
#   bash scripts/install.sh [--port 8080] [--db sqlite|postgres] [--pg-dsn "..."] [--admin-password "..."]
#
# 安装内容:
#   1. 下载 opencode-backend 单二进制到 /usr/local/bin
#   2. 写入 systemd 服务 (/etc/systemd/system/opencode-backend.service)
#   3. 生成默认配置 (/etc/opencode-backend/)
#   4. 启动并开机自启
#
# 通过环境变量/参数覆盖默认值:
#   OCB_PORT / --port
#   OCB_DB / --db (sqlite|postgres)
#   OCB_PG_DSN / --pg-dsn
#   OCB_ADMIN_PASSWORD / --admin-password
set -euo pipefail

# ---------- 权限与前置检查 ----------
if [[ "$(id -u)" -ne 0 ]]; then
  echo "!! 需要 root 权限（写入 /usr/local/bin、/etc/systemd/system 并执行 systemctl）" >&2
  echo "   请使用: sudo bash $0 $*" >&2
  exit 1
fi
if ! command -v systemctl >/dev/null 2>&1; then
  echo "!! 未找到 systemctl，本机可能不是 systemd 系统" >&2
  exit 1
fi
if ! command -v curl >/dev/null 2>&1; then
  echo "!! 未找到 curl，请先安装 (如 apt install curl)" >&2
  exit 1
fi

# ---------- 参数解析 ----------
PORT="${OCB_PORT:-8080}"
DB="${OCB_DB:-sqlite}"
PG_DSN="${OCB_PG_DSN:-}"
ADMIN_PASSWORD="${OCB_ADMIN_PASSWORD:-}"

while [[ $# -gt 0 ]]; do
  case "$1" in
    --port) PORT="$2"; shift 2 ;;
    --db) DB="$2"; shift 2 ;;
    --pg-dsn) PG_DSN="$2"; shift 2 ;;
    --admin-password) ADMIN_PASSWORD="$2"; shift 2 ;;
    -h|--help)
      echo "用法: $0 [--port 8080] [--db sqlite|postgres] [--pg-dsn dsn] [--admin-password pw]"
      exit 0 ;;
    *) echo "未知参数: $1" >&2; exit 1 ;;
  esac
done

# ---------- 参数合法性校验 ----------
if ! [[ "$PORT" =~ ^[0-9]+$ ]]; then
  echo "!! 端口必须是数字: $PORT" >&2
  exit 1
fi
if [[ "$DB" != "sqlite" && "$DB" != "postgres" ]]; then
  echo "!! --db 仅支持 sqlite 或 postgres: $DB" >&2
  exit 1
fi
if [[ "$DB" == "postgres" && -z "$PG_DSN" ]]; then
  echo "!! 选择 postgres 时必须提供 --pg-dsn" >&2
  exit 1
fi

# ---------- 二进制下载 ----------
# GitHub Release 产物命名: opencode-backend-{os}-{arch}
#   os ∈ linux|darwin，arch ∈ amd64|arm64|arm
# uname 输出与产物命名不同（x86_64→amd64、aarch64→arm64、armv7l→arm），需要显式映射
OS="$(uname -s | tr '[:upper:]' '[:lower:]')"
case "$OS" in
  linux|darwin) ;;
  *) echo "!! 本脚本仅支持 Linux/Darwin，当前平台: $OS" >&2; exit 1 ;;
esac

ARCH="$(uname -m)"
case "$ARCH" in
  x86_64|amd64) ARCH="amd64" ;;
  aarch64|arm64) ARCH="arm64" ;;
  armv7l|armv6l|arm) ARCH="arm" ;;
  *) echo "!! 不支持的架构: $ARCH" >&2; exit 1 ;;
esac

BIN_URL="${OCB_BIN_URL:-https://github.com/hiylo/opencode-backend/releases/latest/download/opencode-backend-${OS}-${ARCH}}"
INSTALL_DIR="/usr/local/bin"
BIN_PATH="$INSTALL_DIR/opencode-backend"
CONFIG_DIR="/etc/opencode-backend"
DATA_DIR="/var/lib/opencode-backend"
SERVICE_FILE="/etc/systemd/system/opencode-backend.service"

echo "==> 下载 $BIN_URL"
mkdir -p "$INSTALL_DIR"
if curl -fsSL -o "$BIN_PATH.tmp" "$BIN_URL"; then
  chmod +x "$BIN_PATH.tmp"
  mv "$BIN_PATH.tmp" "$BIN_PATH"
else
  echo "!! 下载失败。若在开发机本地运行，可先 go build -o $BIN_PATH ./cmd/opencode-backend 再重试" >&2
  exit 1
fi

echo "==> 写入配置 $CONFIG_DIR"
mkdir -p "$CONFIG_DIR" "$DATA_DIR"

# 生成启动参数。SQLite 数据放 /var/lib, Postgres 用连接串。
DB_ARGS=(--db "$DB")
if [[ "$DB" == "sqlite" ]]; then
  DB_ARGS+=(--sqlite-path "$DATA_DIR/opencode-backend.db")
else
  DB_ARGS+=(--pg-dsn "$PG_DSN")
fi
[[ -n "$ADMIN_PASSWORD" ]] && DB_ARGS+=(--default-admin-password "$ADMIN_PASSWORD")

# systemd 按空白切分 ExecStart 的参数，含空格的值（DSN、密码）必须用双引号包裹；
# 值内部的双引号/反斜杠也要转义，否则会被 systemd 错误解析。
EXEC_DB_ARGS=""
for arg in "${DB_ARGS[@]}"; do
  escaped="$(printf '%s' "$arg" | sed 's/[\\"]/\\&/g')"
  EXEC_DB_ARGS="${EXEC_DB_ARGS} \"${escaped}\""
done

# 用单引号定界符生成 unit 模板（不做任何 shell 展开），再通过参数替换注入动态值，
# 避免 DSN/登录密码中的 $、反引号、\ 在 heredoc 展开时被误解析。
UNIT=$(cat <<'UNIT_EOF'
[Unit]
Description=OpenCode Backend
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
ExecStart=@BIN_PATH@ --listen :@PORT@ @EXEC_DB_ARGS@
Restart=on-failure
RestartSec=5
User=root
Environment=OCB_OPENCODE_URL=@OPENCODE_URL@
WorkingDirectory=@DATA_DIR@

[Install]
WantedBy=multi-user.target
UNIT_EOF
)
UNIT="${UNIT//@BIN_PATH@/$BIN_PATH}"
UNIT="${UNIT//@PORT@/$PORT}"
UNIT="${UNIT//@EXEC_DB_ARGS@/$EXEC_DB_ARGS}"
UNIT="${UNIT//@OPENCODE_URL@/${OCB_OPENCODE_URL:-http://127.0.0.1:4096}}"
UNIT="${UNIT//@DATA_DIR@/$DATA_DIR}"

printf '%s\n' "$UNIT" > "$SERVICE_FILE"

echo "==> 启动服务"
systemctl daemon-reload
systemctl enable opencode-backend
systemctl restart opencode-backend

echo ""
echo "✔ 安装完成"
if [[ -n "$ADMIN_PASSWORD" ]]; then
  echo "  - 配置页: http://<本机IP>:$PORT/  (初始密码为你设置的 --admin-password)"
else
  echo "  - 配置页: http://<本机IP>:$PORT/  (默认密码 admin, 请首次登录后修改)"
fi
echo "  - 服务: opencode-backend (systemd, :$PORT)"
echo "  - 数据: $DATA_DIR"
echo "  - 日志: journalctl -u opencode-backend -f"