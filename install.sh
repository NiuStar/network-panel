#!/bin/bash
# 下载地址
BASE_GOST_URL="https://github.com/bqlpfy/flux-panel/releases/download/gost-latest/gost"
DOWNLOAD_URL="$BASE_GOST_URL"
INSTALL_DIR="/etc/gost"
AGENT_BIN="/usr/local/bin/flux-agent"
COUNTRY=$(curl -s https://ipinfo.io/country)
if [ "$COUNTRY" = "CN" ]; then
    # 拼接 URL（默认国内加速，若提供 -p 则以 -p 优先）
    DOWNLOAD_URL="https://ghfast.top/${DOWNLOAD_URL}"
fi



# 显示菜单
show_menu() {
  echo "==============================================="
  echo "              管理脚本"
  echo "==============================================="
  echo "请选择操作："
  echo "1. 安装"
  echo "2. 更新"  
  echo "3. 卸载"
  echo "4. 退出"
  echo "==============================================="
}

# 删除脚本自身
delete_self() {
  echo ""
  echo "🗑️ 操作已完成，正在清理脚本文件..."
  SCRIPT_PATH="$(readlink -f "$0" 2>/dev/null || realpath "$0" 2>/dev/null || echo "$0")"
  sleep 1
  rm -f "$SCRIPT_PATH" && echo "✅ 脚本文件已删除" || echo "❌ 删除脚本文件失败"
}

# 检查并安装 tcpkill
check_and_install_tcpkill() {
  # 检查 tcpkill 是否已安装
  if command -v tcpkill &> /dev/null; then
    return 0
  fi
  
  # 检测操作系统类型
  OS_TYPE=$(uname -s)
  
  # 检查是否需要 sudo
  if [[ $EUID -ne 0 ]]; then
    SUDO_CMD="sudo"
  else
    SUDO_CMD=""
  fi
  
  if [[ "$OS_TYPE" == "Darwin" ]]; then
    if command -v brew &> /dev/null; then
      brew install dsniff &> /dev/null
    fi
    return 0
  fi
  
  # 检测 Linux 发行版并安装对应的包
  if [ -f /etc/os-release ]; then
    . /etc/os-release
    DISTRO=$ID
  elif [ -f /etc/redhat-release ]; then
    DISTRO="rhel"
  elif [ -f /etc/debian_version ]; then
    DISTRO="debian"
  else
    return 0
  fi
  
  case $DISTRO in
    ubuntu|debian)
      $SUDO_CMD apt update &> /dev/null
      $SUDO_CMD apt install -y dsniff &> /dev/null
      ;;
    centos|rhel|fedora)
      if command -v dnf &> /dev/null; then
        $SUDO_CMD dnf install -y dsniff &> /dev/null
      elif command -v yum &> /dev/null; then
        $SUDO_CMD yum install -y dsniff &> /dev/null
      fi
      ;;
    alpine)
      $SUDO_CMD apk add --no-cache dsniff &> /dev/null
      ;;
    arch|manjaro)
      $SUDO_CMD pacman -S --noconfirm dsniff &> /dev/null
      ;;
    opensuse*|sles)
      $SUDO_CMD zypper install -y dsniff &> /dev/null
      ;;
    gentoo)
      $SUDO_CMD emerge --ask=n net-analyzer/dsniff &> /dev/null
      ;;
    void)
      $SUDO_CMD xbps-install -Sy dsniff &> /dev/null
      ;;
  esac
  
  return 0
}

# 安装 nc (netcat) 与 iperf3
check_and_install_diag_tools() {
  if [[ $EUID -ne 0 ]]; then SUDO_CMD="sudo"; else SUDO_CMD=""; fi
  if [ -f /etc/os-release ]; then . /etc/os-release; DISTRO=$ID; else DISTRO=""; fi
  case $DISTRO in
    ubuntu|debian)
      $SUDO_CMD apt update -y >/dev/null 2>&1 || true
      $SUDO_CMD apt install -y netcat-openbsd iperf3 jq >/dev/null 2>&1 || true
      ;;
    centos|rhel|fedora)
      if command -v dnf >/dev/null 2>&1; then
        $SUDO_CMD dnf install -y nmap-ncat iperf3 jq >/dev/null 2>&1 || true
      else
        $SUDO_CMD yum install -y nmap-ncat iperf3 jq >/dev/null 2>&1 || true
      fi
      ;;
    alpine)
      $SUDO_CMD apk add --no-cache netcat-openbsd iperf3 jq >/dev/null 2>&1 || true
      ;;
    arch|manjaro)
      $SUDO_CMD pacman -S --noconfirm gnu-netcat iperf3 jq >/dev/null 2>&1 || true
      ;;
    *)
      # best effort
      command -v nc >/dev/null 2>&1 || echo "⚠️ 请手动安装 netcat/iperf3/jq 以支持诊断"
      ;;
  esac
  # 禁用系统 iperf3 服务（如存在）
  if systemctl list-unit-files | grep -q '^iperf3\.service'; then
    $SUDO_CMD systemctl disable iperf3 >/dev/null 2>&1 || true
    $SUDO_CMD systemctl stop iperf3 >/dev/null 2>&1 || true
  fi

  # 如果 websocat 仍不可用，尝试从 GitHub 下载二进制
  if ! command -v websocat >/dev/null 2>&1; then
    install_websocat_from_github || true
  fi
}

# 从 GitHub 下载 websocat 二进制（按架构尝试多个候选）
install_websocat_from_github() {
  local arch="$(uname -m)"
  local base="https://github.com/vi/websocat/releases/latest/download"
  if [[ -n "$PROXY_PREFIX" ]]; then base="${PROXY_PREFIX}${base}"; fi
  local target="/usr/local/bin/websocat"
  local tried=()
  declare -a candidates
  case "$arch" in
    x86_64|amd64)
      candidates=(
        "websocat.x86_64-unknown-linux-musl"
        "websocat.x86_64-unknown-linux-gnu"
        "websocat_amd64-linux"
        "websocat_linux_amd64"
      ) ;;
    aarch64|arm64)
      candidates=(
        "websocat.aarch64-unknown-linux-musl"
        "websocat.aarch64-unknown-linux-gnu"
        "websocat_arm64-linux"
        "websocat_linux_arm64"
      ) ;;
    armv7l|armv7|armhf)
      candidates=(
        "websocat.armv7-unknown-linux-musleabihf"
      ) ;;
    *)
      echo "⚠️ 未识别架构 $arch，跳过 websocat 安装" >&2
      return 1 ;;
  esac
  for f in "${candidates[@]}"; do
    tried+=("$f")
    if curl -fsSL "$base/$f" -o "$target"; then
      chmod +x "$target"
      if "$target" -h >/dev/null 2>&1; then
        echo "✅ websocat 安装成功 ($f)"
        return 0
      fi
    fi
  done
  echo "❌ 尝试下载 websocat 失败: ${tried[*]}" >&2
  return 1
}


# 获取用户输入的配置参数
get_config_params() {
  if [[ -z "$SERVER_ADDR" || -z "$SECRET" ]]; then
    echo "请输入配置参数："
    
    if [[ -z "$SERVER_ADDR" ]]; then
      read -p "服务器地址: " SERVER_ADDR
    fi
    
    if [[ -z "$SECRET" ]]; then
      read -p "密钥: " SECRET
    fi
    
    if [[ -z "$SERVER_ADDR" || -z "$SECRET" ]]; then
      echo "❌ 参数不完整，操作取消。"
      exit 1
    fi
  fi
}

# 下载并安装 Go 版 flux-agent 二进制
install_flux_agent_go_bin() {
  local arch="$(uname -m)" os="linux"
  local file=""
  case "$arch" in
    x86_64|amd64) file="flux-agent-${os}-amd64" ;;
    aarch64|arm64) file="flux-agent-${os}-arm64" ;;
    armv7l|armv7|armhf) file="flux-agent-${os}-armv7" ;;
    *) file="flux-agent-${os}-amd64" ;;
  esac
  local target="$INSTALL_DIR/flux-agent"
  # 优先从面板下载（后端容器已内置 /flux-agent 路由）
  if curl -fsSL "http://$SERVER_ADDR/flux-agent/$file" -o "$target"; then
    chmod +x "$target"; return 0
  fi
  echo "http://$SERVER_ADDR/flux-agent/$file"
  return 1
}

# 写入并启用 Go 诊断 Agent 服务
install_flux_agent() {
  echo "🛠️ 安装 Go 诊断 Agent..."
  mkdir -p "$INSTALL_DIR"
  # 下载 agent 二进制到 /usr/local/bin 原子替换
  local arch="$(uname -m)" os="linux" file=""
  case "$arch" in
    x86_64|amd64) file="flux-agent-${os}-amd64" ;;
    aarch64|arm64) file="flux-agent-${os}-arm64" ;;
    armv7l|armv7|armhf) file="flux-agent-${os}-armv7" ;;
    *) file="flux-agent-${os}-amd64" ;;
  esac
  local tmpfile
  local AGENT_FILE="$INSTALL_DIR/flux-agent"
  tmpfile=$(mktemp -p /tmp flux-agent.XXXX || echo "/tmp/flux-agent.tmp")
  if curl -fSL --retry 3 --retry-delay 1 "http://$SERVER_ADDR/flux-agent/$file" -o "$tmpfile"; then
    install -m 0755 "$tmpfile" "$AGENT_FILE" && rm -f "$tmpfile"
  else
    echo "❌ 无法下载 flux-agent 二进制"
    return 1
  fi

  # 写入环境配置，便于后续修改
  local AGENT_ENV="/etc/default/flux-agent"
  if [[ ! -f "$AGENT_ENV" ]]; then
    cat > "$AGENT_ENV" <<EOF
# Flux Agent 环境配置
# 面板地址（含端口），为空则默认读取 /etc/gost/config.json 的 addr
ADDR=
# 节点密钥，为空则默认读取 /etc/gost/config.json 的 secret
SECRET=
# WebSocket 协议：ws 或 wss
SCHEME=wss
EOF
  fi

  # 写入 systemd 服务
  local AGENT_SERVICE="/etc/systemd/system/flux-agent.service"
  cat > "$AGENT_SERVICE" <<EOF
[Unit]
Description=Flux Diagnose Go Agent
After=network-online.target gost.service
Wants=network-online.target

[Service]
Type=simple
EnvironmentFile=-/etc/default/flux-agent
ExecStart=$AGENT_FILE
WorkingDirectory=$INSTALL_DIR
Restart=always
RestartSec=2
Environment=PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin

[Install]
WantedBy=multi-user.target
EOF

  systemctl daemon-reload
  systemctl enable flux-agent >/dev/null 2>&1 || true
  systemctl start flux-agent >/dev/null 2>&1 || true
  echo "✅ Go Agent 已安装并启用 (flux-agent.service)"
}
# 解析命令行参数
PROXY_MODE=""
PROXY_PREFIX=""
while getopts "a:s:p:" opt; do
  case $opt in
    a) SERVER_ADDR="$OPTARG" ;;
    s) SECRET="$OPTARG" ;;
    p) PROXY_MODE="$OPTARG" ;;
    *) echo "❌ 无效参数"; exit 1 ;;
  esac
done

# 设置代理前缀（用于 GitHub 下载加速）
if [[ "$PROXY_MODE" == "4" ]]; then
  PROXY_PREFIX="https://proxy.529851.xyz/"
elif [[ "$PROXY_MODE" == "6" ]]; then
  PROXY_PREFIX="http://[240b:4000:93:de01:ffff:c725:3c65:47ff]:5000/"
fi

# 安装功能
install_gost() {
  echo "🚀 开始安装 GOST..."
  get_config_params

    # 检查并安装 tcpkill
  check_and_install_tcpkill
  # 安装 netcat 与 iperf3（诊断工具）
  check_and_install_diag_tools
  

  mkdir -p "$INSTALL_DIR"

  # 停止并禁用已有服务
  if systemctl list-units --full -all | grep -Fq "gost.service"; then
    echo "🔍 检测到已存在的gost服务"
    systemctl stop gost 2>/dev/null && echo "🛑 停止服务"
    systemctl disable gost 2>/dev/null && echo "🚫 禁用自启"
  fi

  # 删除旧文件
  [[ -f "$INSTALL_DIR/gost" ]] && echo "🧹 删除旧文件 gost" && rm -f "$INSTALL_DIR/gost"

  # 下载 gost
  echo "⬇️ 下载 gost 中..."
  # 基于代理与地区选择最终下载地址
  local DL_URL="$BASE_GOST_URL"
  if [ "$COUNTRY" = "CN" ] && [ -z "$PROXY_PREFIX" ]; then
    DL_URL="https://ghfast.top/${DL_URL}"
  fi
  if [[ -n "$PROXY_PREFIX" ]]; then
    DL_URL="${PROXY_PREFIX}${DL_URL}"
  fi
  curl -L "$DL_URL" -o "$INSTALL_DIR/gost"
  if [[ ! -f "$INSTALL_DIR/gost" || ! -s "$INSTALL_DIR/gost" ]]; then
    echo "❌ 下载失败，请检查网络或下载链接。"
    exit 1
  fi
  chmod +x "$INSTALL_DIR/gost"
  echo "✅ 下载完成"

  # 打印版本
  echo "🔎 gost 版本：$($INSTALL_DIR/gost -V)"

  # 写入 config.json (安装时总是创建新的)
  CONFIG_FILE="$INSTALL_DIR/config.json"
  echo "📄 创建新配置: config.json"
  cat > "$CONFIG_FILE" <<EOF
{
  "addr": "$SERVER_ADDR",
  "secret": "$SECRET"
}
EOF

  # 写入 gost.json
  GOST_CONFIG="$INSTALL_DIR/gost.json"
  if [[ -f "$GOST_CONFIG" ]]; then
    echo "⏭️ 跳过配置文件: gost.json (已存在)"
  else
    echo "📄 创建新配置: gost.json"
    cat > "$GOST_CONFIG" <<EOF
{}
EOF
  fi

  # 加强权限
  chmod 600 "$INSTALL_DIR"/*.json

  # 创建 systemd 服务
  SERVICE_FILE="/etc/systemd/system/gost.service"
  cat > "$SERVICE_FILE" <<EOF
[Unit]
Description=Gost Proxy Service
After=network.target

[Service]
WorkingDirectory=$INSTALL_DIR
ExecStart=$INSTALL_DIR/gost -C /etc/gost/gost.json
Restart=on-failure

[Install]
WantedBy=multi-user.target
EOF

  # 启动服务
  systemctl daemon-reload
  systemctl enable gost
  systemctl start gost

  # 检查状态
  echo "🔄 检查服务状态..."
  if systemctl is-active --quiet gost; then
    echo "✅ 安装完成，gost服务已启动并设置为开机启动。"
    echo "📁 配置目录: $INSTALL_DIR"
    echo "🔧 服务状态: $(systemctl is-active gost)"
  else
    echo "❌ gost服务启动失败，请执行以下命令查看日志："
    echo "journalctl -u gost -f"
  fi

  # 安装并启用 Bash 诊断 Agent
  install_flux_agent
}

# 更新功能
update_gost() {
  echo "🔄 开始更新 GOST..."
  
  if [[ ! -d "$INSTALL_DIR" ]]; then
    echo "❌ GOST 未安装，请先选择安装。"
    return 1
  fi
  
  echo "📥 使用下载地址: $DOWNLOAD_URL"
  
  # 检查并安装 tcpkill
  check_and_install_tcpkill
  
  # 先下载新版本
  echo "⬇️ 下载最新版本..."
  curl -L "$DOWNLOAD_URL" -o "$INSTALL_DIR/gost.new"
  if [[ ! -f "$INSTALL_DIR/gost.new" || ! -s "$INSTALL_DIR/gost.new" ]]; then
    echo "❌ 下载失败。"
    return 1
  fi

  # 停止服务
  if systemctl list-units --full -all | grep -Fq "gost.service"; then
    echo "🛑 停止 gost 服务..."
    systemctl stop gost
  fi

  # 替换文件
  mv "$INSTALL_DIR/gost.new" "$INSTALL_DIR/gost"
  chmod +x "$INSTALL_DIR/gost"
  
  # 打印版本
  echo "🔎 新版本：$($INSTALL_DIR/gost -V)"

  # 重启服务
  echo "🔄 重启服务..."
  systemctl start gost
  
  echo "✅ 更新完成，服务已重新启动。"
}

# 卸载功能
uninstall_gost() {
  echo "🗑️ 开始卸载 GOST..."
  
  read -p "确认卸载 GOST 吗？此操作将删除所有相关文件 (y/N): " confirm
  if [[ "$confirm" != "y" && "$confirm" != "Y" ]]; then
    echo "❌ 取消卸载"
    return 0
  fi

  # 停止并禁用服务
  if systemctl list-units --full -all | grep -Fq "gost.service"; then
    echo "🛑 停止并禁用服务..."
    systemctl stop gost 2>/dev/null
    systemctl disable gost 2>/dev/null
  fi

  # 删除服务文件
  if [[ -f "/etc/systemd/system/gost.service" ]]; then
    rm -f "/etc/systemd/system/gost.service"
    echo "🧹 删除服务文件"
  fi

  # 停止并卸载 flux-agent 服务
  if systemctl list-units --full -all | grep -Fq "flux-agent.service"; then
    echo "🛑 停止并禁用 flux-agent 服务..."
    systemctl stop flux-agent 2>/dev/null
    systemctl disable flux-agent 2>/dev/null
    rm -f "/etc/systemd/system/flux-agent.service"
  fi
  if [[ -f "$INSTALL_DIR/flux-agent" ]]; then
    rm -f "$INSTALL_DIR/flux-agent"
    echo "🧹 删除 flux-agent 二进制"
  fi

  # 删除安装目录
  if [[ -d "$INSTALL_DIR" ]]; then
    rm -rf "$INSTALL_DIR"
    echo "🧹 删除安装目录: $INSTALL_DIR"
  fi

  # 重载 systemd
  systemctl daemon-reload

  echo "✅ 卸载完成"
}

# 主逻辑
main() {
  # 如果提供了命令行参数，直接执行安装
  if [[ -n "$SERVER_ADDR" && -n "$SECRET" ]]; then
    install_gost
    delete_self
    exit 0
  fi

  # 显示交互式菜单
  while true; do
    show_menu
    read -p "请输入选项 (1-5): " choice
    
    case $choice in
      1)
        install_gost
        delete_self
        exit 0
        ;;
      2)
        update_gost
        delete_self
        exit 0
        ;;
      3)
        uninstall_gost
        delete_self
        exit 0
        ;;
      4)
        block_protocol
        delete_self
        exit 0
        ;;
      5)
        echo "👋 退出脚本"
        delete_self
        exit 0
        ;;
      *)
        echo "❌ 无效选项，请输入 1-5"
        echo ""
        ;;
    esac
  done
}

# 执行主函数
main
