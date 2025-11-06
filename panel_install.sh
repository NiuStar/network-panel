#!/usr/bin/env bash
set -euo pipefail

# 轻量安装脚本：
# - 默认使用 SQLite 模式；若选择/配置 MySQL 则使用 MySQL 模式
# - 支持二进制安装 或 Docker Compose 安装
#
# 二进制安装：下载并执行 scripts/install_server.sh，随后根据选择写入 DB 配置
# Docker Compose 安装：创建 network-panel 目录，下载 docker-compose-v4_mysql.yml
#   重命名为 docker-compose.yaml，并启动

export LANG=en_US.UTF-8
export LC_ALL=C

INSTALL_SERVER_RAW="https://raw.githubusercontent.com/NiuStar/network-panel/refs/heads/main/scripts/install_server.sh"
COMPOSE_MYSQL_RAW="https://raw.githubusercontent.com/NiuStar/network-panel/refs/heads/main/docker-compose-v4_mysql.yml"

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
  if confirm "是否自定义 SQLite 路径(默认 /opt/network-panel/flux.db)?"; then
    read -rp "输入 DB_SQLITE_PATH: " p
    p=${p:-/opt/network-panel/flux.db}
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

install_binary() {
  echo "选择数据库模式："
  echo "  1) SQLite (默认)"
  echo "  2) MySQL"
  read -rp "输入选项 [1/2]: " dbsel
  dbsel=${dbsel:-1}

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
  if [[ "$dbsel" == "2" ]]; then
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

main() {
  echo "==============================================="
  echo "           面板安装脚本"
  echo "==============================================="
  echo "请选择安装方式："
  echo "  1) 二进制安装 (默认，SQLite 优先)"
  echo "  2) Docker Compose 安装 (MySQL)"
  read -rp "输入选项 [1/2]: " sel
  sel=${sel:-1}

  case "$sel" in
    2) install_compose ;;
    1|*) install_binary ;;
  esac
}

main "$@"

