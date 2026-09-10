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
    *) echo "未知参数: $1"; exit 1 ;;
  esac
done

# ---------- 二进制下载 ----------
BIN_URL="${OCB_BIN_URL:-https://github.com/hiylo/opencode-backend/releases/latest/download/opencode-backend-$({ uname -s | tr '[:upper:]' '[:lower:]'; })-$({ uname -m; })}"
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
  echo "!! 下载失败。如果你在开发本机运行，可先 go build -o $BIN_PATH ./cmd/opencode-backend"
  exit 1
fi

echo "==> 写入配置 $CONFIG_DIR"
mkdir -p "$CONFIG_DIR" "$DATA_DIR"

# 生成启动参数。SQLite 数据放 /var/lib, Postgres 用连接串。
DB_ARGS=(--db "$DB")
if [[ "$DB" == "sqlite" ]]; then
  DB_ARGS+=(--sqlite-path "$DATA_DIR/opencode-backend.db")
else
  if [[ -z "$PG_DSN" ]]; then
    echo "!! 选择 postgres 时必须提供 --pg-dsn" >&2
    exit 1
  fi
  DB_ARGS+=(--pg-dsn "$PG_DSN")
fi
[[ -n "$ADMIN_PASSWORD" ]] && DB_ARGS+=(--default-admin-password "$ADMIN_PASSWORD")

cat > "$SERVICE_FILE" <<EOF
[Unit]
Description=OpenCode Backend
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
ExecStart=$BIN_PATH --listen :$PORT ${DB_ARGS[*]}
Restart=on-failure
RestartSec=5
User=root
Environment=OCB_OPENCODE_URL=${OCB_OPENCODE_URL:-http://127.0.0.1:4096}
WorkingDirectory=$DATA_DIR

[Install]
WantedBy=multi-user.target
EOF

echo "==> 启动服务"
systemctl daemon-reload
systemctl enable opencode-backend
systemctl restart opencode-backend

echo ""
echo "✔ 安装完成"
echo "  - 服务: opencode-backend (systemd, :$PORT)"
echo "  - 数据: $DATA_DIR"
echo "  - 配置页: http://<本机IP>:$PORT/  (默认密码 admin, 请首次登录后修改)"
echo "  - 日志: journalctl -u opencode-backend -f"