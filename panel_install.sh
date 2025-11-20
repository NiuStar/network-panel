#!/usr/bin/env bash
set -euo pipefail

# 日志函数
log() {
  printf '%s\n' "$*" >&2
}

# 轻量安装脚本：
# - 默认使用 SQLite 模式；若选择/配置 MySQL 则使用 MySQL 模式
# - 支持二进制安装 或 Docker Compose 安装
#
# 二进制安装：下载并执行 scripts/install_server.sh，随后根据选择写入 DB 配置
# Docker Compose 安装：创建 network-panel 目录，下载 docker-compose-v4_mysql.yml
#   重命名为 docker-compose.yaml，并启动

export LANG=en_US.UTF-8
export LC_ALL=C

# 定义安装目录
INSTALL_DIR="/opt/network-panel"
BIN_PATH="/usr/local/bin/network-panel-server"
SERVICE_FILE="/etc/systemd/system/network-panel.service"
ENV_FILE="/etc/default/network-panel"
DOT_ENV_FILE="/opt/network-panel/.env"

STATIC_BASE="https://panel-static.199028.xyz/network-panel"
INSTALL_SERVER_RAW="$STATIC_BASE/scripts/install_server.sh"
COMPOSE_MYSQL_RAW="$STATIC_BASE/docker-compose-v4_mysql.yml"

proxy_prefix=""
detect_cn() {
  local c
  c=$(curl -fsSL --max-time 2 https://ipinfo.io/country 2>/dev/null || true)
  if [[ "$c" == "CN" ]]; then
    proxy_prefix="https://ghfast.top/"
  fi
}

docker_cmd=""
detect_docker_cmd() {
  if command -v docker-compose >/dev/null 2>&1; then
    docker_cmd="docker-compose"
  elif command -v docker >/dev/null 2>&1 && docker compose version >/dev/null 2>&1; then
    docker_cmd="docker compose"
  else
    echo "未检测到 docker/docker-compose，请先安装 Docker 环境。" >&2
    exit 1
  fi
}

confirm() {
  local msg="$1"; shift || true
  read -rp "$msg [Y/n]: " yn
  yn=${yn:-Y}
  [[ "$yn" =~ ^[Yy]$ ]]
}

download() {
  # download <url> <out>
  local url="$1"; local out="$2"
  local u1 u2
  u1="$url"; u2="${proxy_prefix}${url}"
  if curl -fsSL --retry 2 "$u1" -o "$out"; then return 0; fi
  if [[ -n "$proxy_prefix" ]]; then
    curl -fsSL --retry 2 "$u2" -o "$out"
  else
    return 1
  fi
}

edit_env_sqlite() {
  # 将服务配置改为 SQLite 模式
  local envf="/etc/default/network-panel"
  if [[ $EUID -ne 0 ]]; then sudo sh -c "echo DB_DIALECT=sqlite >> '$envf'"; else echo DB_DIALECT=sqlite >> "$envf"; fi
  if confirm "是否自定义 SQLite 路径(默认 /opt/network-panel/panel.db)?"; then
    read -rp "输入 DB_SQLITE_PATH: " p
    p=${p:-/opt/network-panel/panel.db}
    if [[ $EUID -ne 0 ]]; then sudo sh -c "echo DB_SQLITE_PATH='$p' >> '$envf'"; else echo "DB_SQLITE_PATH=$p" >> "$envf"; fi
  fi
  echo "已设置为 SQLite 模式，执行: sudo systemctl restart network-panel"
}

edit_env_mysql() {
  local envf="/etc/default/network-panel"
  read -rp "DB_HOST (默认 127.0.0.1): " DB_HOST; DB_HOST=${DB_HOST:-127.0.0.1}
  read -rp "DB_PORT (默认 3306): " DB_PORT; DB_PORT=${DB_PORT:-3306}
  read -rp "DB_NAME (默认 flux_panel): " DB_NAME; DB_NAME=${DB_NAME:-flux_panel}
  read -rp "DB_USER (默认 flux): " DB_USER; DB_USER=${DB_USER:-flux}
  read -rp "DB_PASSWORD (默认 123456): " DB_PASSWORD; DB_PASSWORD=${DB_PASSWORD:-123456}

  {
    echo "DB_HOST=$DB_HOST"
    echo "DB_PORT=$DB_PORT"
    echo "DB_NAME=$DB_NAME"
    echo "DB_USER=$DB_USER"
    echo "DB_PASSWORD=$DB_PASSWORD"
    # 确保覆盖 SQLite 模式
    echo "DB_DIALECT="
  } | if [[ $EUID -ne 0 ]]; then sudo tee -a "$envf" >/dev/null; else tee -a "$envf" >/dev/null; fi
  echo "已写入 MySQL 配置，执行: sudo systemctl restart network-panel"
}

# 自动判断当前安装方式（Docker 或 二进制）
detect_install_method() {
  if [[ -f "$BIN_PATH" ]]; then
    echo "binary"
  elif [[ -f "$SERVICE_FILE" ]]; then
    echo "docker"
  else
    echo "none"
  fi
}

install_binary() {
  detect_cn
  echo "下载并执行服务端安装脚本..."
  local tmp=install_server.sh
  if ! download "$INSTALL_SERVER_RAW" "$tmp"; then
    echo "下载失败：$INSTALL_SERVER_RAW" >&2
    exit 1
  fi
  chmod +x "$tmp"
  if [[ $EUID -ne 0 ]]; then sudo bash "$tmp"; else bash "$tmp"; fi

  # 写入 DB 配置
  if [[ "$prev_dbsel" == "mysql" ]]; then
    edit_env_mysql
  else
    edit_env_sqlite
  fi

  if confirm "现在重启服务以生效配置吗?"; then
    if command -v systemctl >/dev/null 2>&1; then
      if [[ $EUID -ne 0 ]]; then sudo systemctl restart network-panel; else systemctl restart network-panel; fi
      systemctl status --no-pager network-panel || true
    else
      echo "未检测到 systemd，请手动重启服务进程。"
    fi
  fi
  echo "✅ 二进制安装完成"
}

install_compose() {
  detect_docker_cmd
  detect_cn
  local dir="network-panel"
  mkdir -p "$dir"
  pushd "$dir" >/dev/null
  echo "下载 docker-compose 配置 (MySQL 版)..."
  if ! download "$COMPOSE_MYSQL_RAW" docker-compose.yaml; then
    # 退化：如本地仓库存在同名文件则复制
    if [[ -f "../docker-compose-v4_mysql.yml" ]]; then
      cp ../docker-compose-v4_mysql.yml docker-compose.yaml
    else
      echo "下载失败，且未找到本地 docker-compose-v4_mysql.yml" >&2
      popd >/dev/null
      exit 1
    fi
  fi
  echo "启动容器..."
  $docker_cmd up -d
  echo "✅ Docker Compose 启动完成 (目录: $(pwd))"
  popd >/dev/null
}

# 增加 Docker 更新的特殊处理：先拉取最新镜像，再停止并删除容器，最后重新启动
update_docker() {
  log "正在更新 Docker 容器..."
  
  # 拉取最新镜像
  $docker_cmd pull
  
  # 停止并删除现有容器
  $docker_cmd down
  
  # 重新启动容器
  $docker_cmd up -d
  echo "✅ Docker 更新完成"
}

uninstall() {
  log "卸载中..."
  
  # 停止服务
  systemctl stop network-panel || true
  systemctl disable network-panel || true
  
  # 删除文件
  rm -rf "$INSTALL_DIR"
  rm -f "$SERVICE_FILE"
  rm -f "$ENV_FILE"
  rm -f "$DOT_ENV_FILE"
  rm -f "$BIN_PATH"
  
  # 删除服务文件
  systemctl daemon-reload || true
  log "卸载完成。"
}

update() {
  log "更新中..."
  
  # 自动判断当前安装方式，避免用户选择
  local install_method
  install_method=$(detect_install_method)

  if [[ "$install_method" == "binary" ]]; then
    uninstall
    install_binary
  elif [[ "$install_method" == "docker" ]]; then
    uninstall
    install_compose
  else
    log "未检测到有效安装方式，无法更新"
    exit 1
  fi
}

main() {
  # 读取之前的配置，跳过重新选择
  local prev_dbsel="sqlite"
  if [[ -f "$DOT_ENV_FILE" ]]; then
    prev_dbsel=$(grep "DB_DIALECT=" "$DOT_ENV_FILE" | cut -d'=' -f2)
  fi

  echo "==============================================="
  echo "           面板安装脚本"
  echo "==============================================="
  echo "请选择操作："
  echo "  1) 安装"
  echo "  2) 卸载"
  echo "  3) 更新"
  read -rp "输入选项 [1/2/3]: " sel
  sel=${sel:-1}

  case "$sel" in
    2) uninstall ;;
    3) 
      log "正在更新面板..."
      update
    ;;
    1|*) 
      # 安装时根据旧配置跳过数据库选择
      if [[ -n "$prev_dbsel" && "$prev_dbsel" == "mysql" ]]; then
        install_binary
      else
        echo "请选择安装方式："
        echo "  1) 二进制安装 (默认，SQLite 优先)"
        echo "  2) Docker Compose 安装 (MySQL)"
        read -rp "输入选项 [1/2]: " install_sel
        install_sel=${install_sel:-1}
        case "$install_sel" in
          2) install_compose ;;
          1|*) install_binary ;;
        esac
      fi
    ;;
  esac
}

main "$@"
