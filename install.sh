#!/bin/bash
INSTALL_DIR="/etc/gost"
AGENT_BIN="/usr/local/bin/flux-agent"
# Static mirror for all downloadable artifacts (scripts/binaries/configs)
STATIC_BASE="https://panel-static.199028.xyz/network-panel"
GITHUB_DL_BASE="https://github.com/NiuStar/network-panel/releases/latest/download"
# GOST 最新版本 API（自动匹配资产）
BASE_GOST_REPO_API="https://api.github.com/repos/go-gost/gost/releases/latest"
DEFAULT_PROXY_PREFIX="https://proxy.yjkj02.com/"
PROXY_PREFIX=""
FORCE_PROXY_GITHUB=0
# 下载源模式：global(默认) | cn | static | github | auto(等价于 global)
SOURCE_MODE="global"
SOURCE_DESC=""
CURL_CONNECT_TIMEOUT=${CURL_CONNECT_TIMEOUT:-8}
CURL_MAX_TIME=${CURL_MAX_TIME:-45}
CURL_RETRY=${CURL_RETRY:-2}
PKG_INSTALL_TIMEOUT=${PKG_INSTALL_TIMEOUT:-120}

cmd_display() {
  printf '%q ' "$@"
}

run_with_timeout() {
  local seconds="$1"
  shift
  if command -v timeout >/dev/null 2>&1; then
    timeout "$seconds" "$@"
  else
    "$@"
  fi
}

curl_download() {
  local url="$1" target="$2"
  echo "🌐 请求下载: $url"
  curl -fSL \
    --connect-timeout "$CURL_CONNECT_TIMEOUT" \
    --max-time "$CURL_MAX_TIME" \
    --retry "$CURL_RETRY" \
    --retry-delay 1 \
    --retry-connrefused \
    "$url" -o "$target"
}

curl_text() {
  local url="$1"
  echo "🌐 请求接口: $url" >&2
  curl -fsSL \
    --connect-timeout "$CURL_CONNECT_TIMEOUT" \
    --max-time "$CURL_MAX_TIME" \
    --retry "$CURL_RETRY" \
    --retry-delay 1 \
    --retry-connrefused \
    "$url"
}

curl_head_ok() {
  local url="$1"
  curl -fsI \
    --connect-timeout "$CURL_CONNECT_TIMEOUT" \
    --max-time 12 \
    --retry 1 \
    --retry-delay 1 \
    "$url" >/dev/null 2>&1
}

pkg_run() {
  local rc
  echo "📦 执行: $(cmd_display "$@")"
  run_with_timeout "$PKG_INSTALL_TIMEOUT" "$@"
  rc=$?
  if [[ "$rc" -eq 0 ]]; then
    echo "✅ 命令完成: $(cmd_display "$@")"
  elif [[ "$rc" -eq 124 ]]; then
    echo "⚠️ 命令超时 ${PKG_INSTALL_TIMEOUT}s: $(cmd_display "$@")"
  else
    echo "⚠️ 命令失败($rc): $(cmd_display "$@")"
  fi
  return "$rc"
}

# 根据地域/参数决定下载源优先级
init_source_mode() {
  local mode="$SOURCE_MODE"
  if [[ "$mode" == "auto" ]]; then mode="global"; fi
  case "$mode" in
    cn)
      [[ -z "$PROXY_PREFIX" ]] && PROXY_PREFIX="$DEFAULT_PROXY_PREFIX"
      SOURCE_DESC="静态镜像 > GitHub(代理) > GitHub(直连) > 面板"
      ;;
    panel)
      SOURCE_DESC="面板 > GitHub(直连) > GitHub(代理) > 静态镜像"
      ;;
    static)
      SOURCE_DESC="静态镜像 > GitHub(直/代理) > 面板"
      ;;
    github)
      SOURCE_DESC="GitHub > 静态镜像 > 面板"
      ;;
    global)
      SOURCE_DESC="GitHub > 静态镜像 > 面板"
      ;;
    *)
      mode="global"
      SOURCE_DESC="GitHub > 静态镜像 > 面板"
      ;;
  esac
  SOURCE_MODE="$mode"
  echo "📡 下载源模式: $SOURCE_MODE${SOURCE_DESC:+ ($SOURCE_DESC)}"
}

# 按源优先级组装候选下载地址，自动去重去空
build_candidate_urls() {
  local kind="$1" file="$2"
  local urls=() static gh ghp panel
  case "$kind" in
    flux-agent)
      static="${STATIC_BASE}/flux-agent/${file}"
      gh="${GITHUB_DL_BASE}/${file}"
      [[ -n "$PROXY_PREFIX" ]] && ghp="${PROXY_PREFIX}${GITHUB_DL_BASE}/${file}"
      [[ -n "${SERVER_ADDR:-}" ]] && panel="http://${SERVER_ADDR}/flux-agent/${file}"
      ;;
    script)
      static="${STATIC_BASE}/${file}"
      gh="${GITHUB_DL_BASE}/${file}"
      [[ -n "$PROXY_PREFIX" ]] && ghp="${PROXY_PREFIX}${GITHUB_DL_BASE}/${file}"
      ;;
  esac
  case "$SOURCE_MODE" in
    cn) urls+=("$static" "$ghp" "$gh" "$panel") ;;
    panel) urls+=("$panel" "$gh" "$ghp" "$static") ;;
    static) urls+=("$static" "$gh" "$ghp" "$panel") ;;
    github) urls+=("$gh" "$ghp" "$static" "$panel") ;;
    global|*) 
      urls+=("$gh")
      [[ -n "$ghp" ]] && urls+=("$ghp")
      urls+=("$static" "$panel")
      ;;
  esac
  printf '%s\n' "${urls[@]}" | awk '!seen[$0]++ && NF {print}'
}

# 依次尝试下载至目标
download_from_urls() {
  local target="$1"; shift
  local url try_url
  for url in "$@"; do
    [[ -z "$url" ]] && continue
    while read -r try_url; do
      [[ -z "$try_url" ]] && continue
      echo "➡️ 尝试下载: $try_url"
      if curl_download "$try_url" "$target"; then
        echo "✅ 下载成功: $try_url"
        return 0
      else
        echo "⚠️ 下载失败，切换下一个源: $try_url"
      fi
    done < <(expand_github_urls "$url")
  done
  return 1
}

# 判断是否为 GitHub 相关地址（可走 -p 代理前缀）
is_github_related_url() {
  local u="$1"
  case "$u" in
    https://github.com/*|http://github.com/*|\
    https://api.github.com/*|http://api.github.com/*|\
    https://raw.githubusercontent.com/*|http://raw.githubusercontent.com/*|\
    https://objects.githubusercontent.com/*|http://objects.githubusercontent.com/*|\
    https://codeload.github.com/*|http://codeload.github.com/*|\
    https://github-releases.githubusercontent.com/*|http://github-releases.githubusercontent.com/*)
      return 0
      ;;
    *)
      return 1
      ;;
  esac
}

# 若传入 -p，则对 GitHub 相关地址自动给出“直连 + 代理”候选
expand_github_urls() {
  local u="$1"
  local p=""
  if [[ -n "$PROXY_PREFIX" ]] && is_github_related_url "$u"; then
    p="${PROXY_PREFIX}${u}"
    if [[ "$FORCE_PROXY_GITHUB" == "1" ]]; then
      printf '%s\n' "$p"
    elif [[ "$SOURCE_MODE" == "cn" || "$SOURCE_MODE" == "static" ]]; then
      printf '%s\n%s\n' "$p" "$u"
    else
      printf '%s\n%s\n' "$u" "$p"
    fi
  else
    printf '%s\n' "$u"
  fi
}

# 探测 URL 可达，返回首个可达候选（支持 GitHub 代理展开）
pick_reachable_url() {
  local u="$1"
  local t=""
  while read -r t; do
    [[ -z "$t" ]] && continue
    echo "  - HEAD 探测: $t" >&2
    if curl_head_ok "$t"; then
      echo "  - 可用: $t" >&2
      echo "$t"
      return 0
    else
      echo "  - 不可用/超时: $t" >&2
    fi
  done < <(expand_github_urls "$u")
  return 1
}

# 安装二进制（兼容 busybox 无 install 命令）
install_bin() {
  local src="$1"
  local dest="$2"
  mkdir -p "$(dirname "$dest")"
  if command -v install >/dev/null 2>&1; then
    install -m 0755 "$src" "$dest"
  else
    cp -f "$src" "$dest"
    chmod 0755 "$dest"
  fi
}

# 兼容 OpenWrt/busybox 的 mktemp
safe_mktemp_dir() {
  local d
  d=$(mktemp -d 2>/dev/null) || true
  if [[ -z "$d" || ! -d "$d" ]]; then
    d="/tmp/np.$$.$RANDOM"
    mkdir -p "$d"
  fi
  echo "$d"
}

safe_mktemp_file() {
  local name="$1"
  local f
  f=$(mktemp "/tmp/${name}.XXXXXX" 2>/dev/null) || true
  if [[ -z "$f" ]]; then
    f="/tmp/${name}.$$"
    : > "$f"
  fi
  echo "$f"
}

# 写入 cron 任务：每天 03:00 删除 24h 之前的 syslog 轮转文件，避免 syslog.* 撑爆磁盘
setup_syslog_cleanup_cron() {
  local cron_file="/etc/cron.d/cleanup-syslog"
  local line="0 3 * * * root find /var/log -maxdepth 1 -type f -name 'syslog.*' -mmin +1440 -delete"
  if is_openwrt; then
    cron_file="/etc/crontabs/root"
    line="0 3 * * * find /var/log -maxdepth 1 -type f -name 'syslog.*' -mmin +1440 -delete"
    mkdir -p /etc/crontabs >/dev/null 2>&1 || true
  fi
  if [[ -f "$cron_file" ]] && grep -Fq "$line" "$cron_file"; then
    return 0
  fi
  echo "🧹 配置 syslog 清理计划任务 (每日 03:00 清理 24h 前的 syslog.*)"
  if [[ $EUID -ne 0 ]]; then
    printf '%s\n' "$line" | sudo tee "$cron_file" >/dev/null
    sudo chmod 0644 "$cron_file" >/dev/null 2>&1 || true
  else
    printf '%s\n' "$line" > "$cron_file"
    chmod 0644 "$cron_file" >/dev/null 2>&1 || true
  fi
}



# 显示菜单
show_menu() {
  echo "==============================================="
  echo "              管理脚本"
  echo "==============================================="
  echo "请选择操作："
  echo "1. 安装"
  echo "2. 更新 (自动识别二进制/Docker)"  
  echo "3. 卸载 (自动识别二进制/Docker)"
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

  echo "🔧 tcpkill 未安装，尝试安装 dsniff（最多 ${PKG_INSTALL_TIMEOUT}s，失败不影响主流程）..."
  
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
      export DEBIAN_FRONTEND=noninteractive
      pkg_run $SUDO_CMD apt-get update
      pkg_run $SUDO_CMD apt-get install -y --no-install-recommends dsniff
      ;;
    centos|rhel|fedora)
      if command -v dnf &> /dev/null; then
        pkg_run $SUDO_CMD dnf install -y dsniff
      elif command -v yum &> /dev/null; then
        pkg_run $SUDO_CMD yum install -y dsniff
      fi
      ;;
    alpine)
      pkg_run $SUDO_CMD apk add --no-cache dsniff
      ;;
    arch|manjaro)
      pkg_run $SUDO_CMD pacman -S --noconfirm dsniff
      ;;
    opensuse*|sles)
      pkg_run $SUDO_CMD zypper --non-interactive install dsniff
      ;;
    gentoo)
      pkg_run $SUDO_CMD emerge --ask=n net-analyzer/dsniff
      ;;
    void)
      pkg_run $SUDO_CMD xbps-install -Sy dsniff
      ;;
  esac

  if command -v tcpkill >/dev/null 2>&1; then
    echo "✅ tcpkill 已可用"
  else
    echo "⚠️ tcpkill 未安装成功，继续安装 GOST"
  fi
  
  return 0
}

# 安装 nc (netcat) 与 iperf3
check_and_install_diag_tools() {
  if [[ $EUID -ne 0 ]]; then SUDO_CMD="sudo"; else SUDO_CMD=""; fi
  if [ -f /etc/os-release ]; then . /etc/os-release; DISTRO=$ID; else DISTRO=""; fi
  echo "🔧 检查诊断工具 nc/iperf3/jq（最多 ${PKG_INSTALL_TIMEOUT}s，失败不影响主流程）..."
  case $DISTRO in
    ubuntu|debian)
      export DEBIAN_FRONTEND=noninteractive
      pkg_run $SUDO_CMD apt-get update || true
      pkg_run $SUDO_CMD apt-get install -y --no-install-recommends netcat-openbsd iperf3 jq || true
      ;;
    centos|rhel|fedora)
      if command -v dnf >/dev/null 2>&1; then
        pkg_run $SUDO_CMD dnf install -y nmap-ncat iperf3 jq || true
      else
        pkg_run $SUDO_CMD yum install -y nmap-ncat iperf3 jq || true
      fi
      ;;
    alpine)
      pkg_run $SUDO_CMD apk add --no-cache netcat-openbsd iperf3 jq || true
      ;;
    arch|manjaro)
      pkg_run $SUDO_CMD pacman -S --noconfirm gnu-netcat iperf3 jq || true
      ;;
    *)
      # best effort
      command -v nc >/dev/null 2>&1 || echo "⚠️ 请手动安装 netcat/iperf3/jq 以支持诊断"
      ;;
  esac
  echo "✅ 诊断工具检查完成"
  # 禁用系统 iperf3 服务（如存在）
  if is_systemd && systemctl list-unit-files | grep -q '^iperf3\.service'; then
    $SUDO_CMD systemctl disable iperf3 >/dev/null 2>&1 || true
    $SUDO_CMD systemctl stop iperf3 >/dev/null 2>&1 || true
  fi

  # websocat 仅用于旧版 shell agent，当前默认使用 Go 版 flux-agent，无需安装 websocat
}

# --- 安装方式检测与 Docker 辅助 ---

# --- 服务管理（systemd / OpenRC） ---
is_systemd() {
  command -v systemctl >/dev/null 2>&1 && [[ -d /run/systemd/system ]]
}

is_openrc() {
  command -v rc-service >/dev/null 2>&1
}

is_openwrt() {
  [ -f /etc/openwrt_release ] || { [ -f /etc/os-release ] && grep -qi '^ID=.*openwrt' /etc/os-release; }
}

has_jq() {
  command -v jq >/dev/null 2>&1
}

start_flux_agent_service() {
  if is_systemd; then
    echo "🔄 启动 flux-agent(systemd)..."
    systemctl start flux-agent || true
  elif is_openrc; then
    echo "🔄 启动 flux-agent(OpenRC)..."
    rc-service flux-agent start || true
  elif is_openwrt; then
    echo "🔄 启动 flux-agent(OpenWrt)..."
    /etc/init.d/flux-agent start || true
  fi
}

restart_flux_agent_service() {
  if is_systemd; then
    echo "🔄 重启 flux-agent(systemd)..."
    systemctl restart flux-agent || systemctl start flux-agent || true
  elif is_openrc; then
    echo "🔄 重启 flux-agent(OpenRC)..."
    rc-service flux-agent restart || rc-service flux-agent start || true
  elif is_openwrt; then
    echo "🔄 重启 flux-agent(OpenWrt)..."
    /etc/init.d/flux-agent restart || /etc/init.d/flux-agent start || true
  fi
}

enable_flux_agent_service() {
  if is_systemd; then
    echo "⚙️ 设置 flux-agent 开机自启(systemd)..."
    systemctl enable flux-agent || true
  elif is_openrc; then
    echo "⚙️ 设置 flux-agent 开机自启(OpenRC)..."
    rc-update add flux-agent default || true
  elif is_openwrt; then
    echo "⚙️ 设置 flux-agent 开机自启(OpenWrt)..."
    /etc/init.d/flux-agent enable || true
  fi
}

disable_flux_agent_service() {
  if is_systemd; then
    systemctl disable flux-agent >/dev/null 2>&1 || true
  elif is_openrc; then
    rc-update del flux-agent default >/dev/null 2>&1 || true
  elif is_openwrt; then
    /etc/init.d/flux-agent disable >/dev/null 2>&1 || true
  fi
}

start_gost_service() {
  if is_systemd; then
    echo "🔄 启动 gost(systemd)..."
    systemctl start gost || true
  elif is_openrc; then
    echo "🔄 启动 gost(OpenRC)..."
    rc-service gost start || true
  elif is_openwrt; then
    echo "🔄 启动 gost(OpenWrt)..."
    /etc/init.d/gost start || true
  fi
}

restart_gost_service() {
  if is_systemd; then
    echo "🔄 重启 gost(systemd)..."
    systemctl restart gost || systemctl start gost || true
  elif is_openrc; then
    echo "🔄 重启 gost(OpenRC)..."
    rc-service gost restart || rc-service gost start || true
  elif is_openwrt; then
    echo "🔄 重启 gost(OpenWrt)..."
    /etc/init.d/gost restart || /etc/init.d/gost start || true
  fi
}

enable_gost_service() {
  if is_systemd; then
    echo "⚙️ 设置 gost 开机自启(systemd)..."
    systemctl enable gost || true
  elif is_openrc; then
    echo "⚙️ 设置 gost 开机自启(OpenRC)..."
    rc-update add gost default || true
  elif is_openwrt; then
    echo "⚙️ 设置 gost 开机自启(OpenWrt)..."
    /etc/init.d/gost enable || true
  fi
}

disable_gost_service() {
  if is_systemd; then
    systemctl disable gost >/dev/null 2>&1 || true
  elif is_openrc; then
    rc-update del gost default >/dev/null 2>&1 || true
  elif is_openwrt; then
    /etc/init.d/gost disable >/dev/null 2>&1 || true
  fi
}
# 返回值：
#   echo "binary" | "docker" | "none"
detect_install_mode() {
  # binary 判定：systemd 存在或二进制存在
  if (is_systemd && systemctl list-units --full -all 2>/dev/null | grep -Fq "gost.service") || [ -x "$INSTALL_DIR/gost" ]; then
    echo "binary"; return
  fi
  # docker 判定：存在包含 gost 的容器（名称或镜像）
  if command -v docker >/dev/null 2>&1; then
    if docker ps -a --format '{{.ID}} {{.Image}} {{.Names}}' 2>/dev/null | grep -Ei '\bgost\b|go-gost' >/dev/null 2>&1; then
      echo "docker"; return
    fi
  fi
  echo "none"
}

# 选择一个 gost 容器（当存在多个时）
pick_gost_container() {
  docker ps -a --format '{{.ID}} {{.Image}} {{.Names}}' | grep -Ei '\bgost\b|go-gost' | head -n1 | awk '{print $3}'
}

# 使用 docker compose 方式更新（依据容器标签定位 compose 工程）
docker_compose_update() {
  local cn="$1"
  local proj dir files svc
  proj=$(docker inspect -f '{{ index .Config.Labels "com.docker.compose.project"}}' "$cn" 2>/dev/null)
  dir=$(docker inspect -f '{{ index .Config.Labels "com.docker.compose.project.working_dir"}}' "$cn" 2>/dev/null)
  files=$(docker inspect -f '{{ index .Config.Labels "com.docker.compose.project.config_files"}}' "$cn" 2>/dev/null)
  svc=$(docker inspect -f '{{ index .Config.Labels "com.docker.compose.service"}}' "$cn" 2>/dev/null)
  if [[ -n "$proj" && -n "$dir" && -n "$files" && -n "$svc" ]]; then
    ( cd "$dir" 2>/dev/null && \
      docker compose -p "$proj" -f "$files" pull "$svc" && \
      docker compose -p "$proj" -f "$files" up -d "$svc" )
    return $?
  fi
  return 2
}

# 依据当前容器配置重建并更新镜像
docker_update_recreate() {
  local cn="$1"
  local img opts="" envs ports binds net rp priv cmd ep
  img=$(docker inspect -f '{{ .Config.Image }}' "$cn") || return 1
  # 拉取最新镜像
  docker pull "$img" || true
  # 环境变量
  envs=$(docker inspect "$cn" | jq -r '.[0].Config.Env[]? | "-e \(. )"')
  # 端口映射（仅处理 HostPort 存在的 TCP/UDP 一般情况）
  ports=$(docker inspect "$cn" | jq -r '
    .[0].HostConfig.PortBindings // {} | to_entries[]? as $e |
    ($e.key | split("/") | .[0]) as $cport |
    $e.value[]? | "-p \((.HostIp // "") as $ip | if $ip != "" then "\($ip):" else "" end)\(.HostPort):\($cport)"')
  if [[ -z "$ports" ]]; then
    # fallback 简化：根据 .NetworkSettings.Ports 构建
    ports=$(docker inspect "$cn" | jq -r '.[0].NetworkSettings.Ports // {} | to_entries[]? | select(.value!=null) | .value[]? | select(.HostPort) | "-p \(.HostPort):\(.key | split("/")[0])"')
  fi
  # volume 绑定
  binds=$(docker inspect "$cn" | jq -r '.[0].HostConfig.Binds[]? | "-v \(.)"')
  # 网络与重启策略
  net=$(docker inspect -f '{{ .HostConfig.NetworkMode }}' "$cn" 2>/dev/null)
  [[ -n "$net" && "$net" != "default" ]] && opts+=" --network $net"
  rp=$(docker inspect -f '{{ .HostConfig.RestartPolicy.Name }}' "$cn" 2>/dev/null)
  [[ -n "$rp" && "$rp" != "no" ]] && opts+=" --restart $rp"
  priv=$(docker inspect -f '{{ .HostConfig.Privileged }}' "$cn" 2>/dev/null)
  [[ "$priv" == "true" ]] && opts+=" --privileged"
  # entrypoint & cmd
  ep=$(docker inspect "$cn" | jq -r '.[0].Config.Entrypoint? | if type=="array" then ("--entrypoint \(.[0])") elif type=="string" then ("--entrypoint \(.)") else empty end')
  cmd=$(docker inspect "$cn" | jq -r '.[0].Config.Cmd? | @sh' | sed "s/^'//;s/'$//")
  # 停止并删除旧容器
  docker stop "$cn" >/dev/null 2>&1 || true
  docker rm "$cn" >/dev/null 2>&1 || true
  # 运行新容器
  # shellcheck disable=SC2086
  docker run -d --name "$cn" $opts $envs $binds $ports ${ep:-} "$img" ${cmd:-} || return 1
  return 0
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
  case "$(uname -s)" in
    Darwin) os="darwin" ;;
    Linux) os="linux" ;;
    FreeBSD) os="freebsd" ;;
  esac
  local file=""
  case "$arch" in
    x86_64|amd64) file="flux-agent-${os}-amd64" ;;
    aarch64|arm64) file="flux-agent-${os}-arm64" ;;
    armv7l|armv7|armhf) file="flux-agent-${os}-armv7" ;;
    *) file="flux-agent-${os}-amd64" ;;
  esac
  local target="$INSTALL_DIR/flux-agent"
  local urls=()
  while read -r u; do urls+=("$u"); done < <(build_candidate_urls "flux-agent" "$file")
  if download_from_urls "$target" "${urls[@]}"; then
    chmod +x "$target"; return 0
  fi
  echo "❌ 无法下载 flux-agent 二进制"
  return 1
}

# 写入并启用 Go 诊断 Agent 服务
install_flux_agent() {
  echo "🛠️ 安装 Go 诊断 Agent..."
  mkdir -p "$INSTALL_DIR"
  # 下载 agent 二进制到 /usr/local/bin 原子替换
  local arch="$(uname -m)" os="linux" file=""
  case "$(uname -s)" in
    Darwin) os="darwin" ;;
    Linux) os="linux" ;;
    FreeBSD) os="freebsd" ;;
  esac
  case "$arch" in
    x86_64|amd64) file="flux-agent-${os}-amd64" ;;
    aarch64|arm64) file="flux-agent-${os}-arm64" ;;
    armv7l|armv7|armhf) file="flux-agent-${os}-armv7" ;;
    *) file="flux-agent-${os}-amd64" ;;
  esac
  local tmpfile
  local AGENT_FILE="$INSTALL_DIR/flux-agent"
  tmpfile=$(safe_mktemp_file "flux-agent")
  local urls=()
  while read -r u; do urls+=("$u"); done < <(build_candidate_urls "flux-agent" "$file")
  if download_from_urls "$tmpfile" "${urls[@]}"; then
    install_bin "$tmpfile" "$AGENT_FILE" && rm -f "$tmpfile"
  else
    echo "❌ 无法下载 flux-agent 二进制"
    return 1
  fi

  # 写入环境配置，便于后续修改
  local AGENT_ENV="/etc/default/flux-agent"
  local AGENT_ENV_RC="/etc/conf.d/flux-agent"
  mkdir -p "$(dirname "$AGENT_ENV")"
  # 自动推断 SCHEME（可通过环境变量 SCHEME 覆盖）
  if [[ -z "${SCHEME:-}" ]]; then
    SCHEME="ws"
    if [[ "$SERVER_ADDR" =~ :443$ ]]; then
      SCHEME="wss"
    fi
  fi
  # 始终写入（若提供了参数则写具体值，否则写空）
  cat > "$AGENT_ENV" <<EOF
# Flux Agent 环境配置
# 面板地址（含端口），为空则默认读取 /etc/gost/config.json 的 addr
ADDR=${SERVER_ADDR:-}
# 节点密钥，为空则默认读取 /etc/gost/config.json 的 secret
SECRET=${SECRET:-}
# WebSocket 协议：ws 或 wss
SCHEME=${SCHEME:-ws}
# Agent 自动升级：1 开启，0 关闭
AGENT_AUTO_UPGRADE=${AGENT_AUTO_UPGRADE:-1}
# GOST Web API 本地端口（Agent 将固定绑定 127.0.0.1）
GOST_API_PORT=${GOST_API_PORT:-18080}
EOF

  if is_openwrt && ! is_systemd; then
    local AGENT_INIT="/etc/init.d/flux-agent"
    cat > "$AGENT_INIT" <<EOF
#!/bin/sh /etc/rc.common

USE_PROCD=1
START=95
STOP=10

AGENT_BIN="$AGENT_FILE"
AGENT_ENV="/etc/default/flux-agent"

start_service() {
  [ -f "\$AGENT_ENV" ] && . "\$AGENT_ENV"
  if [ -z "\$ADDR" ] || [ -z "\$SECRET" ]; then
    if [ -f /etc/gost/config.json ]; then
      ADDR=\$(sed -n 's/.*"addr":[[:space:]]*"\\([^"]*\\)".*/\\1/p' /etc/gost/config.json | head -n1)
      SECRET=\$(sed -n 's/.*"secret":[[:space:]]*"\\([^"]*\\)".*/\\1/p' /etc/gost/config.json | head -n1)
    fi
  fi
  [ -z "\$SCHEME" ] && SCHEME="ws"
  procd_open_instance
  procd_set_param command "\$AGENT_BIN"
  procd_set_param respawn 5 5 0
  procd_set_param env ADDR="\$ADDR" SECRET="\$SECRET" SCHEME="\$SCHEME" AGENT_AUTO_UPGRADE="\${AGENT_AUTO_UPGRADE:-1}" GOST_API_PORT="\${GOST_API_PORT:-18080}"
  procd_set_param stdout 1
  procd_set_param stderr 1
  procd_close_instance
}
EOF
    chmod +x "$AGENT_INIT"
    enable_flux_agent_service
    start_flux_agent_service
    echo "✅ Go Agent 已安装并启用 (OpenWrt: flux-agent)"
    return 0
  fi

  if is_openrc && ! is_systemd; then
    if [[ ! -f "$AGENT_ENV_RC" ]]; then
      mkdir -p "$(dirname "$AGENT_ENV_RC")"
      cat > "$AGENT_ENV_RC" <<EOF
# Flux Agent OpenRC 配置
ADDR=""
SECRET=""
SCHEME="ws"
EOF
    fi
    local AGENT_RC="/etc/init.d/flux-agent"
    cat > "$AGENT_RC" <<EOF
#!/sbin/openrc-run

name="flux-agent"
description="Flux Diagnose Go Agent"
command="$AGENT_FILE"
pidfile="/run/flux-agent.pid"
directory="$INSTALL_DIR"
supervisor="supervise-daemon"
respawn_delay=5
respawn_max=0
retry="SIGTERM/5 SIGKILL/5"

[ -f /etc/default/flux-agent ] && . /etc/default/flux-agent
export ADDR SECRET SCHEME GOST_API_PORT
export AGENT_AUTO_UPGRADE

depend() {
  need net
  after gost
}
EOF
    chmod +x "$AGENT_RC"
    enable_flux_agent_service
    start_flux_agent_service
    echo "✅ Go Agent 已安装并启用 (OpenRC: flux-agent)"
    return 0
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

  if is_systemd; then systemctl daemon-reload; fi
  enable_flux_agent_service
  start_flux_agent_service
  echo "✅ Go Agent 已安装并启用 (flux-agent.service)"
}
# 解析命令行参数（getopts 保留现有短参数及其紧凑写法）
usage() {
  cat <<EOF
用法: bash install.sh [-a 面板地址] [-s 密钥] [-p 4|6] [-m 下载源模式] [--proxy URL]
  -p 4, -4       使用默认 GitHub 代理: $DEFAULT_PROXY_PREFIX
  -p 6, -6       使用内置 IPv6 GitHub 代理
  --proxy URL    自定义 GitHub 代理前缀，优先于 -p；自动补齐末尾 /
                 也支持 --proxy=URL；GitHub 请求只走指定代理
  -m MODE        global（默认）| cn | static | github | panel | auto
  -h, --help     显示帮助
EOF
}

PROXY_MODE=""
CUSTOM_PROXY=""
while getopts ":a:s:p:m:46h-:" opt; do
  case $opt in
    a) SERVER_ADDR="$OPTARG" ;;
    s) SECRET="$OPTARG" ;;
    p) PROXY_MODE="$OPTARG" ;;
    m) SOURCE_MODE="$OPTARG" ;;
    4|6) PROXY_MODE="$opt" ;;
    h) usage; exit 0 ;;
    -)
      case "$OPTARG" in
        proxy)
          CUSTOM_PROXY="${!OPTIND}"
          if [[ -z "$CUSTOM_PROXY" || "$CUSTOM_PROXY" == -* ]]; then
            echo "❌ --proxy 需要提供 HTTP(S) 代理地址" >&2
            exit 1
          fi
          OPTIND=$((OPTIND + 1))
          ;;
        proxy=*) CUSTOM_PROXY="${OPTARG#proxy=}" ;;
        help) usage; exit 0 ;;
        *) echo "❌ 无效参数: --$OPTARG" >&2; exit 1 ;;
      esac
      if [[ "$OPTARG" == proxy || "$OPTARG" == proxy=* ]]; then
        if [[ ! "$CUSTOM_PROXY" =~ ^https?://[^/?#[:space:]]+(/[^?#[:space:]]*)?$ ]]; then
          echo "❌ --proxy 必须是有效的 HTTP(S) URL 前缀（不含空白、查询串或片段）" >&2
          exit 1
        fi
        CUSTOM_PROXY="${CUSTOM_PROXY%/}/"
      fi
      ;;
    :) echo "❌ 参数 -$OPTARG 缺少值" >&2; exit 1 ;;
    *) echo "❌ 无效参数: -$OPTARG" >&2; exit 1 ;;
  esac
done
shift $((OPTIND - 1))
if (( $# > 0 )); then
  echo "❌ 无效参数: $1" >&2
  exit 1
fi

# 设置代理前缀（用于 GitHub 下载加速）
case "$PROXY_MODE" in
  4) PROXY_PREFIX="$DEFAULT_PROXY_PREFIX" ;;
  6) PROXY_PREFIX="http://[240b:4000:93:de01:ffff:c725:3c65:47ff]:5000/" ;;
  "") ;;
  *) echo "❌ 无效 -p 参数: $PROXY_MODE (仅支持 4 或 6)" >&2; exit 1 ;;
esac
# 自定义地址始终优先，不受参数顺序影响
if [[ -n "$CUSTOM_PROXY" ]]; then
  PROXY_PREFIX="$CUSTOM_PROXY"
fi
# 带 -p / -4 / -6 / --proxy 时，GitHub 请求只走代理，不做直连回退
if [[ -n "$PROXY_PREFIX" ]]; then
  FORCE_PROXY_GITHUB=1
fi
init_source_mode

get_latest_gost_version() {
  local api tag
  echo "🔎 获取最新 GOST 版本..." >&2
  while read -r api; do
    [[ -z "$api" ]] && continue
    echo "  - 查询: $api" >&2
    if has_jq; then
      tag=$(curl_text "$api" | jq -r '.tag_name' 2>/dev/null | head -n1 || true)
    else
      tag=$(curl_text "$api" 2>/dev/null | sed -n 's/.*"tag_name"[[:space:]]*:[[:space:]]*"\\([^"]*\\)".*/\\1/p' | head -n1 || true)
    fi
    tag=$(echo "$tag" | tr -d '\r\n ')
    if [[ -n "$tag" ]]; then
      echo "✅ 最新 GOST 版本: $tag" >&2
      echo "$tag"
      return 0
    fi
    echo "  - 查询失败或无 tag，尝试下一个源" >&2
  done < <(expand_github_urls "$BASE_GOST_REPO_API")
  echo "⚠️ 获取最新版本失败，使用保底版本 v3.2.6" >&2
  echo "v3.2.6"
  return 0
}

# 解析 go-gost/gost 最新版本下载链接（匹配 Linux + 当前架构）
resolve_latest_gost_url() {
  local arch="$(uname -m)" token=""
  case "$arch" in
    x86_64|amd64) token="amd64" ;;
    aarch64|arm64) token="arm64" ;;
    armv7l|armv7|armhf) token="armv7" ;;
    i386|i686) token="386" ;;
    mips64el) token="mips64le" ;;
    mipsel) token="mipsle" ;;
    mips) token="mips" ;;
    loongarch64) token="loong64" ;;
    riscv64) token="riscv64" ;;
    s390x) token="s390x" ;;
    *) token="amd64" ;;
  esac
  local prefer_static=1
  if [[ "$SOURCE_MODE" == "github" || "$SOURCE_MODE" == "global" || "$SOURCE_MODE" == "panel" ]]; then
    prefer_static=0
  fi
  local ver ver_no_v
  ver=$(get_latest_gost_version)
  ver_no_v="${ver#v}"
  echo "🔎 匹配 GOST Linux/${token} 下载包..." >&2
  # 1) 静态镜像（按模式决定是否优先）
  local static_base="${STATIC_BASE}/gost"
  local name url ok_url
  if (( prefer_static )); then
    for name in \
      "gost_${ver_no_v}_linux_${token}.tar.gz" \
      "gost_${ver_no_v}_linux_${token}.tgz" \
      "gost_${ver_no_v}_linux_${token}.gz" \
      "gost_${ver_no_v}_linux_${token}.zip" \
      "gost-linux-${token}.tar.gz" \
      "gost-linux-${token}.tgz" \
      "gost-linux-${token}.gz" \
      "gost-linux-${token}.zip"
    do
      url="${static_base}/${name}"
      echo "  - 探测静态镜像: $url" >&2
      if ok_url=$(pick_reachable_url "$url"); then
        echo "$ok_url"; return 0
      fi
    done
  fi
  # 2) GitHub API（自动支持 -p 代理）

  # 无论是否有 jq，都优先尝试版本号固定文件名
  for name in \
    "gost_${ver_no_v}_linux_${token}.tar.gz" \
    "gost_${ver_no_v}_linux_${token}.tgz" \
    "gost_${ver_no_v}_linux_${token}.gz" \
    "gost_${ver_no_v}_linux_${token}.zip"
  do
    url="https://github.com/go-gost/gost/releases/download/${ver}/${name}"
    echo "  - 探测 GitHub 固定文件名: $url" >&2
    if ok_url=$(pick_reachable_url "$url"); then
      echo "$ok_url"; return 0
    fi
  done

  local api urls cand
  while read -r api; do
    [[ -z "$api" ]] && continue
    if ! has_jq; then
      continue
    fi
    echo "  - 读取 release assets: $api" >&2
    urls=$(curl_text "$api" | jq -r '.assets[].browser_download_url' 2>/dev/null || true)
    if [[ -z "$urls" ]]; then continue; fi
    for cand in $urls; do
      if [[ "$cand" == *linux* && "$cand" == *$token* && ( "$cand" == *.tar.gz || "$cand" == *.tgz || "$cand" == *.gz || "$cand" == *.zip ) ]]; then
        if ok_url=$(pick_reachable_url "$cand"); then
          echo "$ok_url"
          return 0
        fi
      fi
    done
  done < <(expand_github_urls "$BASE_GOST_REPO_API")
  # 3) 如果 GitHub 失败且未尝试静态源，再尝试静态源
  if (( ! prefer_static )); then
    for name in \
      "gost_${ver_no_v}_linux_${token}.tar.gz" \
      "gost_${ver_no_v}_linux_${token}.tgz" \
      "gost_${ver_no_v}_linux_${token}.gz" \
      "gost_${ver_no_v}_linux_${token}.zip" \
      "gost-linux-${token}.tar.gz" \
      "gost-linux-${token}.tgz" \
      "gost-linux-${token}.gz" \
      "gost-linux-${token}.zip"
    do
      url="${static_base}/${name}"
      echo "  - 探测静态镜像: $url" >&2
      if ok_url=$(pick_reachable_url "$url"); then
        echo "$ok_url"; return 0
      fi
    done
  fi
  return 1
}

# 下载并安装 GOST（支持 tar.gz/zip/gz/单文件）
download_and_install_gost() {
  local url="$1"
  local tmpdir; tmpdir=$(safe_mktemp_dir)
  echo "⬇️ 下载: $url"
  if ! download_from_urls "$tmpdir/pkg" "$url"; then
    echo "❌ 下载失败: $url"; rm -rf "$tmpdir"; return 1
  fi
  mkdir -p "$INSTALL_DIR"
  if [[ "$url" =~ \.tar\.gz$|\.tgz$ ]]; then
    if ! tar -xzf "$tmpdir/pkg" -C "$tmpdir"; then
      echo "❌ 解压 tar.gz 失败"; rm -rf "$tmpdir"; return 1
    fi
    local bin
    bin=$(find "$tmpdir" -type f -name gost -perm -111 | head -n1 || true)
    if [[ -z "$bin" ]]; then bin=$(find "$tmpdir" -type f -name gost | head -n1 || true); fi
    if [[ -z "$bin" ]]; then echo "❌ 未在压缩包内找到 gost"; rm -rf "$tmpdir"; return 1; fi
    install_bin "$bin" "$INSTALL_DIR/gost"
  elif [[ "$url" =~ \.zip$ ]]; then
    if command -v unzip >/dev/null 2>&1; then
      if ! unzip -o "$tmpdir/pkg" -d "$tmpdir" >/dev/null; then
        echo "❌ 解压 zip 失败"; rm -rf "$tmpdir"; return 1
      fi
      local bin
      bin=$(find "$tmpdir" -type f -name gost -perm -111 | head -n1 || true)
      if [[ -z "$bin" ]]; then bin=$(find "$tmpdir" -type f -name gost | head -n1 || true); fi
      if [[ -z "$bin" ]]; then echo "❌ 未在压缩包内找到 gost"; rm -rf "$tmpdir"; return 1; fi
      install_bin "$bin" "$INSTALL_DIR/gost"
    else
      echo "⚠️ 未安装 unzip，无法解压 .zip 包"; rm -rf "$tmpdir"; return 1
    fi
  elif [[ "$url" =~ \.gz$ ]]; then
    if ! gunzip -c "$tmpdir/pkg" > "$INSTALL_DIR/gost"; then
      echo "❌ 解压 gz 失败"; rm -rf "$tmpdir"; return 1
    fi
    chmod +x "$INSTALL_DIR/gost"
  else
    install_bin "$tmpdir/pkg" "$INSTALL_DIR/gost"
  fi
  rm -rf "$tmpdir"
  echo "🔎 版本：$($INSTALL_DIR/gost -V || true)"
}

# 获取已安装 gost 版本（形如 v3.2.6），不存在则返回空
get_installed_gost_version() {
  local ver out
  if [[ -x "$INSTALL_DIR/gost" ]]; then
    out=$("$INSTALL_DIR/gost" -V 2>/dev/null || true)
    for ver in $out; do
      case "$ver" in
        v[0-9]*) echo "$ver"; return 0 ;;
      esac
    done
  fi
  echo ""
}

# 安装功能
install_gost() {
  echo "🚀 开始安装 GOST..."
  get_config_params

    # 检查并安装 tcpkill
  check_and_install_tcpkill
  # 安装 netcat 与 iperf3（诊断工具）
  check_and_install_diag_tools
  

  mkdir -p "$INSTALL_DIR"

  # 如已安装且版本一致，跳过下载与安装
  local latest_ver current_ver skip_download=0
  current_ver=$(get_installed_gost_version)
  latest_ver=$(get_latest_gost_version)
  # 始终输出版本信息，便于定位是否进入此逻辑
  if [[ -n "$current_ver" ]]; then
    echo "🔎 当前 gost 版本：$current_ver"
  else
    local raw_ver
    raw_ver=$("$INSTALL_DIR/gost" -V 2>/dev/null || true)
    echo "🔎 当前 gost 版本：<unknown>${raw_ver:+ ($raw_ver)}"
  fi
  if [[ -n "$latest_ver" ]]; then
    echo "🔎 最新 gost 版本：$latest_ver"
  else
    echo "🔎 最新 gost 版本：<unknown>"
  fi
  if [[ -n "$current_ver" && -n "$latest_ver" && "$current_ver" == "$latest_ver" ]]; then
    echo "✅ gost 已是最新版 ($current_ver)，跳过下载与安装。"
    skip_download=1
  elif [[ -n "$current_ver" && -z "$latest_ver" ]]; then
    echo "⚠️ 未能获取最新 gost 版本，继续执行安装流程。"
  fi

  # 停止并禁用已有服务（仅在需要重新安装时）
  if [[ "$skip_download" -eq 0 ]]; then
    if is_systemd && systemctl list-units --full -all 2>/dev/null | grep -Fq "gost.service"; then
      echo "🔍 检测到已存在的gost服务"
      systemctl stop gost 2>/dev/null && echo "🛑 停止服务"
      systemctl disable gost 2>/dev/null && echo "🚫 禁用自启"
    fi
    if is_openrc && [[ -f "/etc/init.d/gost" ]]; then
      echo "🔍 检测到已存在的gost(OpenRC)服务"
      rc-service gost stop 2>/dev/null && echo "🛑 停止服务"
      disable_gost_service && echo "🚫 禁用自启"
    fi
    if is_openwrt && [[ -f "/etc/init.d/gost" ]]; then
      echo "🔍 检测到已存在的gost(OpenWrt)服务"
      /etc/init.d/gost stop 2>/dev/null && echo "🛑 停止服务"
      disable_gost_service && echo "🚫 禁用自启"
    fi
  fi

  if [[ "$skip_download" -eq 0 ]]; then
    # 删除旧文件
    [[ -f "$INSTALL_DIR/gost" ]] && echo "🧹 删除旧文件 gost" && rm -f "$INSTALL_DIR/gost"
    # 下载并安装 GOST（自动解析最新版本与资产）
    echo "⬇️ 解析最新 GOST 下载地址..."
    local GOST_URL
    if ! GOST_URL=$(resolve_latest_gost_url); then
      echo "❌ 无法解析最新 GOST 下载地址"; exit 1
    fi
    if ! download_and_install_gost "$GOST_URL"; then
      echo "❌ GOST 下载或安装失败"; exit 1
    fi
  fi

  # 打印版本
  if [[ ! -x "$INSTALL_DIR/gost" ]]; then
    echo "❌ GOST 二进制不存在或不可执行: $INSTALL_DIR/gost"
    exit 1
  fi
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

  if is_openwrt && ! is_systemd; then
    local GOST_INIT="/etc/init.d/gost"
    cat > "$GOST_INIT" <<EOF
#!/bin/sh /etc/rc.common

USE_PROCD=1
START=90
STOP=10

GOST_BIN="$INSTALL_DIR/gost"
GOST_CFG="/etc/gost/gost.json"
LOG_OUT="/var/log/gost.log"
LOG_ERR="/var/log/gost.err"

start_service() {
  mkdir -p /var/log
  touch "\$LOG_OUT" "\$LOG_ERR"
  procd_open_instance
  procd_set_param command /bin/sh
  procd_set_param args -c "exec \"\$GOST_BIN\" -C \"\$GOST_CFG\" >>\"\$LOG_OUT\" 2>>\"\$LOG_ERR\""
  procd_set_param respawn 5 5 0
  procd_close_instance
}
EOF
    chmod +x "$GOST_INIT"
    enable_gost_service
    start_gost_service
    echo "✅ 安装完成，gost(OpenWrt) 已启动并设置为开机启动。"
    echo "📁 配置目录: $INSTALL_DIR"
  elif is_openrc && ! is_systemd; then
    local GOST_RC="/etc/init.d/gost"
    cat > "$GOST_RC" <<EOF
#!/sbin/openrc-run

name="gost"
description="Gost Proxy Service"
command="$INSTALL_DIR/gost"
command_args="-C /etc/gost/gost.json"
supervisor="supervise-daemon"
pidfile="/run/gost.pid"
respawn_delay=5
respawn_max=0
retry="SIGTERM/5 SIGKILL/5"
directory="$INSTALL_DIR"
output_log="/var/log/gost.log"
error_log="/var/log/gost.err"
command_user="root:root"

start_pre() {
  mkdir -p /var/log
  touch "\$output_log" "\$error_log"
  chown "\$command_user" "\$output_log" "\$error_log" 2>/dev/null || true
}

depend() {
  need net
}
EOF
    chmod +x "$GOST_RC"
    enable_gost_service
    start_gost_service
    echo "✅ 安装完成，gost(OpenRC) 已启动并设置为开机启动。"
    echo "📁 配置目录: $INSTALL_DIR"
  else
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
    enable_gost_service
    start_gost_service

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
  fi

  # 安装并启用 Go 诊断 Agent，并确保服务已重启生效
  install_flux_agent
  if is_systemd; then systemctl daemon-reload; fi
  restart_flux_agent_service
  setup_syslog_cleanup_cron
}

# 更新功能
update_gost() {
  echo "🔄 开始更新 GOST..."
  local mode
  mode=$(detect_install_mode)
  if [[ "$mode" == "docker" ]]; then
    if ! command -v docker >/dev/null 2>&1; then echo "❌ 未检测到 docker"; return 1; fi
    # 需要 jq 解析容器配置
    check_and_install_diag_tools
    local cn
    cn=$(pick_gost_container)
    if [[ -z "$cn" ]]; then echo "❌ 未找到 gost 容器"; return 1; fi
    echo "🐳 检测到 Docker 安装，容器: $cn"
    # 优先使用 docker compose 重建
    if docker_compose_update "$cn"; then
      echo "✅ Docker Compose 更新完成"
      return 0
    fi
    # 退化为重建容器
    if docker_update_recreate "$cn"; then
      echo "✅ Docker 容器已使用最新镜像重建并启动"
      return 0
    else
      echo "❌ Docker 容器更新失败"
      return 1
    fi
  elif [[ "$mode" == "binary" ]]; then
    if [[ ! -d "$INSTALL_DIR" ]]; then
      echo "❌ GOST 未安装，请先选择安装。"; return 1
    fi
    # 检查并安装工具
    check_and_install_tcpkill
    check_and_install_diag_tools
    # 停止服务
    if is_systemd && systemctl list-units --full -all 2>/dev/null | grep -Fq "gost.service"; then
      echo "🛑 停止 gost 服务..."; systemctl stop gost || true
    elif is_openrc; then
      echo "🛑 停止 gost(OpenRC) 服务..."; rc-service gost stop >/dev/null 2>&1 || true
    elif is_openwrt; then
      echo "🛑 停止 gost(OpenWrt) 服务..."; /etc/init.d/gost stop >/dev/null 2>&1 || true
    fi
    # 下载并安装最新版
    echo "⬇️ 解析最新 GOST 下载地址..."
    local GOST_URL
    if ! GOST_URL=$(resolve_latest_gost_url); then echo "❌ 无法解析最新 GOST 下载地址"; return 1; fi
    if ! download_and_install_gost "$GOST_URL"; then
      echo "❌ GOST 下载或安装失败"
      return 1
    fi
    echo "🔎 新版本：$($INSTALL_DIR/gost -V || true)"
    echo "🔄 重启服务..."; start_gost_service
    if is_systemd; then systemctl daemon-reload; fi
    restart_flux_agent_service
    echo "✅ 更新完成，gost 与 flux-agent 均已重新启动。"
    return 0
  else
    echo "ℹ️ 未检测到已安装的 GOST。"
    return 1
  fi
}

# 卸载功能
uninstall_gost() {
  echo "🗑️ 开始卸载 GOST..."
  read -p "确认卸载 GOST 吗？此操作将删除所有相关文件 (y/N): " confirm
  if [[ "$confirm" != "y" && "$confirm" != "Y" ]]; then echo "❌ 取消卸载"; return 0; fi
  local mode; mode=$(detect_install_mode)
  if [[ "$mode" == "docker" ]]; then
    if ! command -v docker >/dev/null 2>&1; then echo "❌ 未检测到 docker"; return 1; fi
    # 批量处理所有匹配 gost 的容器
    local lines; lines=$(docker ps -a --format '{{.Names}}' | grep -Ei '\bgost\b|go-gost' || true)
    if [[ -z "$lines" ]]; then echo "ℹ️ 未找到 gost 容器"; else
      echo "$lines" | while read -r cn; do
        echo "🛑 停止容器: $cn"; docker stop "$cn" >/dev/null 2>&1 || true
        echo "🧹 删除容器: $cn"; docker rm "$cn" >/dev/null 2>&1 || true
      done
    fi
    echo "✅ Docker 卸载完成"
    return 0
  fi
  # binary 卸载
  if is_systemd && systemctl list-units --full -all 2>/dev/null | grep -Fq "gost.service"; then
    echo "🛑 停止并禁用服务..."; systemctl stop gost 2>/dev/null; systemctl disable gost 2>/dev/null
  fi
  if [[ -f "/etc/systemd/system/gost.service" ]]; then rm -f "/etc/systemd/system/gost.service"; echo "🧹 删除服务文件"; fi
  if is_openrc && [[ -f "/etc/init.d/gost" ]]; then
    echo "🛑 停止并禁用 gost(OpenRC)..."; rc-service gost stop 2>/dev/null || true; disable_gost_service; rm -f "/etc/init.d/gost"
  fi
  if is_openwrt && [[ -f "/etc/init.d/gost" ]]; then
    echo "🛑 停止并禁用 gost(OpenWrt)..."; /etc/init.d/gost stop 2>/dev/null || true; disable_gost_service; rm -f "/etc/init.d/gost"
  fi
  if is_systemd && systemctl list-units --full -all | grep -Fq "flux-agent.service"; then
    echo "🛑 停止并禁用 flux-agent 服务..."; systemctl stop flux-agent 2>/dev/null; systemctl disable flux-agent 2>/dev/null; rm -f "/etc/systemd/system/flux-agent.service"
  fi
  if is_openrc && [[ -f "/etc/init.d/flux-agent" ]]; then
    echo "🛑 停止并禁用 flux-agent(OpenRC)..."; rc-service flux-agent stop 2>/dev/null || true; disable_flux_agent_service; rm -f "/etc/init.d/flux-agent" "/etc/conf.d/flux-agent"
  fi
  if is_openwrt && [[ -f "/etc/init.d/flux-agent" ]]; then
    echo "🛑 停止并禁用 flux-agent(OpenWrt)..."; /etc/init.d/flux-agent stop 2>/dev/null || true; disable_flux_agent_service; rm -f "/etc/init.d/flux-agent"
  fi
  if [[ -f "$INSTALL_DIR/flux-agent" ]]; then rm -f "$INSTALL_DIR/flux-agent"; echo "🧹 删除 flux-agent 二进制"; fi
  if [[ -d "$INSTALL_DIR" ]]; then rm -rf "$INSTALL_DIR"; echo "🧹 删除安装目录: $INSTALL_DIR"; fi
  if is_systemd; then systemctl daemon-reload; fi
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
    read -p "请输入选项 (1-4): " choice
    
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
