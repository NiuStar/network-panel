#!/usr/bin/env bash
set -euo pipefail

# One-click install and run the network‑panel server as a systemd service (Linux only).
# This is NOT the agent installer. It installs the backend server binary
# and configures DB env + auto‑start.

# -----------------------------------------------------------------------------
#  WARNING: This script has been modified to remember previous user choices
#  (such as proxy prefix and architecture) so that update operations do not
#  repeatedly prompt for the same information.  Configuration is stored in
#  $CONFIG_FILE and automatically reloaded on subsequent runs.  See
#  read_config() and save_config() for details.
# -----------------------------------------------------------------------------

log() { printf '%s\n' "$*" >&2; }

if [[ "$(uname -s)" != "Linux" ]]; then
  log "This installer supports Linux only."
  exit 1
fi

# Service and installation paths
SERVICE_NAME="network-panel"
INSTALL_DIR="/opt/network-panel"
BIN_PATH="/usr/local/bin/network-panel-server"
ENV_FILE="/etc/default/network-panel"
SERVICE_FILE="/etc/systemd/system/${SERVICE_NAME}.service"
ROOT_DIR="$(cd "$(dirname "$0")/.." && pwd)"
MAIN_PKG="${ROOT_DIR}/golang-backend/cmd/server"
FRONTEND_ASSET_NAME="frontend-dist.zip"
DOT_ENV_FILE="${INSTALL_DIR}/.env"

# -----------------------------------------------------------------------------
# Configuration persistence
#
# User choices (proxy prefix and architecture) are stored in this config file.
# On subsequent runs, values will be loaded automatically, bypassing prompts.
# -----------------------------------------------------------------------------
CONFIG_FILE="/etc/default/network-panel-install.conf"
STATIC_BASE="https://panel-static.199028.xyz/network-panel"

# Read configuration file if it exists.  Source only simple key=value pairs.
read_config() {
  if [[ -f "$CONFIG_FILE" ]]; then
    # shellcheck source=/dev/null
    source "$CONFIG_FILE"
  fi
}

# Write current configuration values to disk.  Only keys referenced here will
# persist.  Call this after a successful install to remember the user's
# selections for future runs.
save_config() {
  mkdir -p "$(dirname "$CONFIG_FILE")"
  {
    echo "PROXY_PREFIX=$PROXY_PREFIX"
    echo "ARCH=$ARCH"
  } > "$CONFIG_FILE"
}

# Detect CPU architecture; used for selecting a binary asset.
detect_arch() {
  local m
  m=$(uname -m)
  case "$m" in
    x86_64|amd64) printf 'amd64\n' ;;
    aarch64|arm64) printf 'arm64\n' ;;
    armv7l|armv7|armhf) printf 'armv7\n' ;;
    i386|i686) printf '386\n' ;;
    riscv64) printf 'riscv64\n' ;;
    s390x) printf 's390x\n' ;;
    loongarch64) printf 'loong64\n' ;;
    *) printf 'amd64\n' ;;
  esac
}

# Interactively confirm or override the detected architecture.  If the user
# answers yes (default), the detected value is used.  Otherwise they may
# specify a different architecture from the supported list.
prompt_arch() {
  local detected
  detected="$(detect_arch)"
  log "Detected arch: ${detected}"
  read -rp "Use detected arch ($detected)? [Y/n]: " yn
  yn=${yn:-Y}
  if [[ "$yn" =~ ^[Yy]$ ]]; then
    printf '%s\n' "$detected"
    return
  fi
  log "Available: amd64, amd64v3, arm64, armv7, 386, riscv64, s390x, loong64"
  read -rp "Enter arch: " a
  a=${a:-$detected}
  printf '%s\n' "$a"
}

# Download a prebuilt release tarball or binary for the given architecture.
download_prebuilt() {
  local arch="$1"
  local base="${STATIC_BASE}/server"
  local name
  for name in \
    "network-panel-server-linux-${arch}" \
    "server-linux-${arch}" \
    "network-panel_linux_${arch}.tar.gz" \
    "server_linux_${arch}.tar.gz"
  do
    log "Trying to download ${base}/${name}"
    if curl -fSL --retry 3 --retry-delay 1 "${base}/${name}" -o /tmp/network-panel.dl; then
      printf '/tmp/network-panel.dl\n'
      return 0
    fi
  done
  # fallback to GitHub latest
  base="https://github.com/NiuStar/network-panel/releases/latest/download"
  if [[ -n "$PROXY_PREFIX" ]]; then base="${PROXY_PREFIX}${base}"; fi
  for name in \
    "network-panel-server-linux-${arch}" \
    "server-linux-${arch}" \
    "network-panel_linux_${arch}.tar.gz" \
    "server_linux_${arch}.tar.gz"
  do
    log "Trying to download ${base}/${name}"
    if curl -fSL --retry 3 --retry-delay 1 "${base}/${name}" -o /tmp/network-panel.dl; then
      printf '/tmp/network-panel.dl\n'
      return 0
    fi
  done
  return 1
}

# Download the frontend distribution archive.
download_frontend_dist() {
  local url="${STATIC_BASE}/frontend/${FRONTEND_ASSET_NAME}"
  log "Trying to download frontend assets: $url"
  if curl -fSL --retry 3 --retry-delay 1 "$url" -o /tmp/frontend-dist.zip; then
    printf '/tmp/frontend-dist.zip\n'
    return 0
  fi
  local base="https://github.com/NiuStar/network-panel/releases/latest/download"
  if [[ -n "$PROXY_PREFIX" ]]; then base="${PROXY_PREFIX}${base}"; fi
  url="${base}/${FRONTEND_ASSET_NAME}"
  log "Trying to download frontend assets (fallback): $url"
  if curl -fSL --retry 3 --retry-delay 1 "$url" -o /tmp/frontend-dist.zip; then
    printf '/tmp/frontend-dist.zip\n'
    return 0
  fi
  return 1
}

# Extract downloaded archive or install binary directly into INSTALL_DIR.
extract_or_install() {
  local file="$1"
  mkdir -p "$INSTALL_DIR"
  if [[ "$file" =~ \.tar\.gz$|\.tgz$ ]]; then
    # Preserve existing files (especially SQLite DB) during extraction
    if tar --help 2>/dev/null | grep -q -- '--keep-old-files'; then
      tar --keep-old-files -xzf "$file" -C "$INSTALL_DIR"
    else
      # Fallback for non-GNU tar: extract to temp then copy without overwriting
      local tmpdir
      tmpdir=$(mktemp -d)
      tar -xzf "$file" -C "$tmpdir"
      if command -v rsync >/dev/null 2>&1; then
        rsync -a --ignore-existing "$tmpdir"/ "$INSTALL_DIR"/
      else
        # cp -an to avoid overwriting; fallback loop if -n unsupported
        if cp -an "$tmpdir"/* "$INSTALL_DIR"/ 2>/dev/null; then :; else
          find "$tmpdir" -type f | while read -r f; do
            rel="${f#${tmpdir}/}"
            dest="$INSTALL_DIR/$rel"
            mkdir -p "$(dirname "$dest")"
            [[ -e "$dest" ]] || cp -a "$f" "$dest"
          done
        fi
      fi
      rm -rf "$tmpdir"
    fi
  elif [[ "$file" =~ \.zip$ ]]; then
    if command -v unzip >/dev/null 2>&1; then
      # -n: never overwrite existing files
      unzip -n "$file" -d "$INSTALL_DIR"
    else
      log "unzip not found, please install unzip or provide a .tar.gz"
      return 1
    fi
  else
    # assume plain binary
    install -m 0755 "$file" "$BIN_PATH"
  fi
  # After extraction, locate the binary within INSTALL_DIR and move to BIN_PATH
  if [[ ! -x "$BIN_PATH" ]]; then
    local cand
    cand=$(find "$INSTALL_DIR" -maxdepth 2 -type f \( -name "server" -o -name "network-panel-server" \) | head -n1 || true)
    if [[ -n "$cand" ]]; then
      install -m 0755 "$cand" "$BIN_PATH"
    fi
  fi
  if [[ ! -x "$BIN_PATH" ]]; then
    log "Server binary not found after extraction."
    return 1
  fi
  # Ensure frontend assets exist; if missing, attempt to download them
  if [[ ! -d "$INSTALL_DIR/public" || -z "$(find "$INSTALL_DIR/public" -mindepth 1 -print -quit 2>/dev/null)" ]]; then
    log "Frontend assets missing, attempting to download ${FRONTEND_ASSET_NAME}..."
    local fzip
    if fzip=$(download_frontend_dist); then
      if command -v unzip >/dev/null 2>&1; then
        mkdir -p "$INSTALL_DIR/public"
        unzip -qo "$fzip" -d "$INSTALL_DIR/public"
        log "✅ Frontend assets installed to $INSTALL_DIR/public"
      else
        log "⚠️  'unzip' not found; cannot extract ${FRONTEND_ASSET_NAME}. Please install unzip and re‑run."
      fi
    else
      log "⚠️  Failed to download ${FRONTEND_ASSET_NAME}. The web UI may be unavailable."
      log "   - You can build locally (vite‑frontend) and copy dist/* to $INSTALL_DIR/public"
    fi
  fi
}

# Ensure install.sh is available for node bootstrap (served at GET /install.sh)
install_install_sh() {
  local dst="$INSTALL_DIR/install.sh"
  if [[ -f "$ROOT_DIR/install.sh" ]]; then
    install -m 0755 "$ROOT_DIR/install.sh" "$dst"
    log "✅ install.sh installed to $dst"
    return 0
  fi
  local base="https://raw.githubusercontent.com/NiuStar/network-panel/refs/heads/main/install.sh"
  if [[ -n "$PROXY_PREFIX" ]]; then base="${PROXY_PREFIX}${base}"; fi
  if curl -fSL --retry 3 --retry-delay 1 "$base" -o "$dst"; then
    chmod +x "$dst"
    log "✅ install.sh downloaded to $dst"
    return 0
  fi
  log "⚠️  Failed to obtain install.sh; /install.sh endpoint will return 404 until provided."
  return 1
}

# Helper to read a value from an env file
read_env_val() {
  local f="$1"; local k="$2"; local v
  [[ -f "$f" ]] || { return 1; }
  v=$(grep -E "^${k}=" "$f" 2>/dev/null | tail -n1 | sed -E "s/^${k}=\"?([^\"]*)\"?.*$/\1/")
  if [[ -n "$v" ]]; then printf '%s\n' "$v"; return 0; fi
  return 1
}

# Update or append a key=value pair in an env file
upsert_env_val() {
  local f="$1" k="$2" v="$3"
  mkdir -p "$(dirname "$f")"
  if [[ -f "$f" ]] && grep -qE "^${k}=" "$f"; then
    sed -i -E "s|^${k}=.*|${k}=${v}|" "$f"
  else
    printf '%s\n' "${k}=${v}" >> "$f"
  fi
}

# Check if a TCP port is in use using lsof/ss/netstat/nc
port_in_use() {
  local port="$1"
  if command -v lsof >/dev/null 2>&1; then
    lsof -nP -iTCP:"$port" -sTCP:LISTEN >/dev/null 2>&1 && return 0 || return 1
  elif command -v ss >/dev/null 2>&1; then
    ss -lnt "sport = :$port" 2>/dev/null | awk 'NR>1{exit 0} END{exit 1}' && return 0 || return 1
  elif command -v netstat >/dev/null 2>&1; then
    netstat -lnt 2>/dev/null | awk -v p=":$port" '$4 ~ p{found=1} END{exit found?0:1}' && return 0 || return 1
  else
    # Fallback: try connecting locally; if connection succeeds, assume in use
    nc -z localhost "$port" >/dev/null 2>&1 && return 0 || return 1
  fi
}

# Suggest the closest N unused ports greater than the given port
suggest_free_ports_above() {
  local base="$1" count="${2:-10}" max_scan=10000
  local p=$(( base + 1 ))
  local found=0 out=()
  while (( found < count && max_scan > 0 )); do
    # stop if exceeding valid TCP port range
    if (( p > 65535 )); then break; fi
    if ! port_in_use "$p"; then
      out+=("$p")
      found=$((found+1))
    fi
    p=$((p+1))
    max_scan=$((max_scan-1))
  done
  printf '%s\n' "${out[*]}"
}

# Ensure a usable port; if desired is busy, print 10 nearest higher free ports and pick the first
ensure_port_available() {
  local desired="$1"; local suggestions="" first=""
  if port_in_use "$desired"; then
    suggestions=$(suggest_free_ports_above "$desired" 10)
    log "Port $desired is in use. Suggested free ports (nearest higher): ${suggestions}"
    first=$(printf '%s' "$suggestions" | awk '{print $1}')
    if [[ -n "$first" ]]; then
      printf '%s\n' "$first"
      return 0
    fi
  fi
  printf '%s\n' "$desired"
}

# Clean the installation directory while preserving SQLite DB if configured
clean_install_dir_preserve_sqlite() {
  mkdir -p "$INSTALL_DIR"
  local dialect="" dbpath_config="" default_db="${INSTALL_DIR}/panel.db"

  # Try to read configured dialect and sqlite path
  dialect=$(read_env_val "$ENV_FILE" DB_DIALECT || true)
  if [[ -z "$dialect" ]]; then
    dialect=$(read_env_val "$DOT_ENV_FILE" DB_DIALECT || true)
  fi
  dbpath_config=$(read_env_val "$ENV_FILE" DB_SQLITE_PATH || true)
  if [[ -z "$dbpath_config" ]]; then
    dbpath_config=$(read_env_val "$DOT_ENV_FILE" DB_SQLITE_PATH || true)
  fi

  # Build list of paths to preserve:
  #  - If a sqlite path is configured and exists
  #  - Always also preserve default ${INSTALL_DIR}/panel.db if it exists, even when dialect isn't sqlite
  local preserve_paths=()
  if [[ -n "$dbpath_config" && -f "$dbpath_config" ]]; then
    preserve_paths+=("$dbpath_config")
  fi
  if [[ -f "$default_db" ]]; then
    # Avoid duplicate entries if it matches dbpath_config
    if [[ "${dbpath_config:-}" != "$default_db" ]]; then
      preserve_paths+=("$default_db")
    else
      preserve_paths+=("$default_db")
    fi
  fi

  if (( ${#preserve_paths[@]} > 0 )); then
    log "Cleaning $INSTALL_DIR (preserving sqlite DB files: ${preserve_paths[*]})"
    # Move out any DBs that are inside INSTALL_DIR, wipe, and move back.
    local tmpdir
    tmpdir=$(mktemp -d)
    # Track mapping of original path to temp path
    local i p rel tdest
    for ((i=0; i<${#preserve_paths[@]}; i++)); do
      p="${preserve_paths[$i]}"
      # Only need to move if it's inside INSTALL_DIR; if outside, cleaning won't affect it.
      case "$p" in
        "$INSTALL_DIR"/*)
          rel="${p#${INSTALL_DIR}/}"
          tdest="$tmpdir/$rel"
          mkdir -p "$(dirname "$tdest")" 2>/dev/null || true
          cp -a "$p" "$tdest" 2>/dev/null || true
          ;;
      esac
    done
    # Now wipe installation directory fully
    rm -rf "${INSTALL_DIR}/"* 2>/dev/null || true
    # Restore preserved files back
    for ((i=0; i<${#preserve_paths[@]}; i++)); do
      p="${preserve_paths[$i]}"
      case "$p" in
        "$INSTALL_DIR"/*)
          rel="${p#${INSTALL_DIR}/}"
          tdest="$tmpdir/$rel"
          if [[ -f "$tdest" ]]; then
            mkdir -p "$(dirname "$p")" 2>/dev/null || true
            mv -f "$tdest" "$p" 2>/dev/null || cp -a "$tdest" "$p" 2>/dev/null || true
          fi
          ;;
      esac
    done
    rm -rf "$tmpdir" 2>/dev/null || true
  else
    log "Cleaning $INSTALL_DIR (no sqlite DB to preserve)"
    rm -rf "${INSTALL_DIR}/"* 2>/dev/null || true
  fi
}

# Install flux-agent binaries for various architectures
install_flux_agents() {
  local outdir="$INSTALL_DIR/public/flux-agent"
  mkdir -p "$outdir"
  local localdir="$ROOT_DIR/golang-backend/public/flux-agent"
  local need=("flux-agent-linux-amd64" "flux-agent-linux-arm64" "flux-agent-linux-armv7")
  local have_any=0
  if [[ -d "$localdir" ]]; then
    for f in "${need[@]}"; do
      if [[ -f "$localdir/$f" ]]; then
        install -m 0755 "$localdir/$f" "$outdir/$f"
        have_any=1
        log "✅ copied local $f"
      fi
    done
  fi
  # Helper for download with retry
  try_dl() {
    local url="$1"; local dest="$2"; local code
    code=$(curl -fSL --retry 2 --retry-delay 1 --write-out '%{http_code}' --output "$dest" "$url" 2>/dev/null || true)
    if [[ -s "$dest" && ( "$code" == "200" || "$code" == "302" || "$code" == "000" ) ]]; then
      chmod +x "$dest" 2>/dev/null || true
      printf 'OK %s\n' "$code"
      return 0
    fi
    rm -f "$dest" 2>/dev/null || true
    printf 'ERR %s\n' "${code:-unknown}"
    return 1
  }
  # Download from GitHub if needed
  local api="https://api.github.com/repos/NiuStar/network-panel/releases/latest"
  local dlbase="https://github.com/NiuStar/network-panel/releases/latest/download"
  if [[ -n "$PROXY_PREFIX" ]]; then
    api="${PROXY_PREFIX}${api}"
    dlbase="${PROXY_PREFIX}${dlbase}"
  fi
  local api_body
  api_body=$(curl -fsSL "$api" || true)
  for f in "${need[@]}"; do
    if [[ -f "$outdir/$f" ]]; then continue; fi
    log "Downloading flux-agent: $f"
    local token url1 url2 url3 url4
    case "$f" in
      *amd64) token="amd64" ;;
      *arm64) token="arm64" ;;
      *armv7) token="armv7|armv7l" ;;
      *) token="" ;;
    esac
    if [[ -n "$api_body" ]]; then
      url1=$(printf '%s\n' "$api_body" | grep -oE '"browser_download_url"\s*:\s*"[^"]+"' | grep -E "flux-agent.*(linux-($token))" | head -n1 | sed -E 's/.*"(http[^"]+)".*/\1/')
    fi
    url2="${dlbase}/$f"
    url3="${STATIC_BASE}/flux-agent/$f"
    url4="https://raw.githubusercontent.com/NiuStar/network-panel/refs/heads/main/golang-backend/public/flux-agent/$f"
    if [[ -n "$PROXY_PREFIX" ]]; then url4="${PROXY_PREFIX}${url4}"; fi
    local dest="$outdir/$f"; local res
    if [[ -n "$url1" ]]; then
      res=$(try_dl "$url1" "$dest") || log "❌ failed $f from $url1 ($(printf '%s' "$res" | awk '{print $2}'))"
      if [[ "$res" == OK* ]]; then have_any=1; log "✅ downloaded $f via API asset"; continue; fi
    fi
    res=$(try_dl "$url2" "$dest") || log "❌ failed $f from $url2 ($(printf '%s' "$res" | awk '{print $2}'))"
    if [[ "$res" == OK* ]]; then have_any=1; log "✅ downloaded $f via latest/download"; continue; fi
    res=$(try_dl "$url3" "$dest") || log "❌ failed $f from $url3 ($(printf '%s' "$res" | awk '{print $2}'))"
    if [[ "$res" == OK* ]]; then have_any=1; log "✅ downloaded $f via static mirror"; continue; fi
    res=$(try_dl "$url4" "$dest") || log "❌ failed $f from $url4 ($(printf '%s' "$res" | awk '{print $2}'))"
    if [[ "$res" == OK* ]]; then have_any=1; log "✅ downloaded $f via raw repo"; continue; fi
  done
  if (( have_any == 1 )); then
    log "✅ flux-agent binaries ready in $outdir"
  else
    log "⚠️  No flux-agent binaries available; /flux-agent endpoint will be unavailable for nodes until provided."
  fi
}

# Install easytier config files into INSTALL_DIR/easytier
install_easytier() {
  log "install_easytier"
  local outdir="$INSTALL_DIR/easytier"
  mkdir -p "$outdir"
  log "install_easytier 到 $outdir"
  if [[ ! -d "$outdir" ]]; then
    log "⚠️ 创建目标目录失败: $outdir"
    return 1
  fi
  local localdir="$ROOT_DIR/easytier"
  local need=("default.conf" "install.sh")
  local have_any=0
  if [[ -d "$localdir" ]]; then
    for f in "${need[@]}"; do
      if [[ -f "$localdir/$f" ]]; then
        install -m 0755 "$localdir/$f" "$outdir/$f"
        have_any=1
        log "✅ 本地复制 $f"
      else
        log "⚠️ 本地找不到 $f，尝试从 GitHub 下载"
      fi
    done
  fi
  try_dl() {
    local url="$1"; local dest="$2"; local code
    code=$(curl -fSL --retry 2 --retry-delay 1 --write-out '%{http_code}' --output "$dest" "$url" 2>/dev/null || true)
    if [[ -s "$dest" && ( "$code" == "200" || "$code" == "302" || "$code" == "000" ) ]]; then
      chmod +x "$dest" 2>/dev/null || true
      printf 'OK %s\n' "$code"
      return 0
    fi
    rm -f "$dest" 2>/dev/null || true
    printf 'ERR %s\n' "${code:-unknown}"
    return 1
  }
  local base_url="${STATIC_BASE}/easytier"
  for f in "${need[@]}"; do
    if [[ -f "$outdir/$f" ]]; then continue; fi
    log "下载 easytier: $f"
    local url="${base_url}/${f}"
    local dest="$outdir/$f"
    res=$(try_dl "$url" "$dest") || log "❌ 从 $url 下载失败 ($(printf '%s' "$res" | awk '{print $2}'))"
    if [[ "$res" == OK* ]]; then have_any=1; log "✅ 从静态镜像下载 $f"; continue; fi
    # fallback raw GitHub
    local ghraw="https://raw.githubusercontent.com/NiuStar/network-panel/refs/heads/main/easytier/${f}"
    if [[ -n "$PROXY_PREFIX" ]]; then ghraw="${PROXY_PREFIX}${ghraw}"; fi
    res=$(try_dl "$ghraw" "$dest") || log "❌ 从 $ghraw 下载失败 ($(printf '%s' "$res" | awk '{print $2}'))"
    if [[ "$res" == OK* ]]; then have_any=1; log "✅ 从 GitHub 仓库下载 $f"; fi
  done
  if (( have_any == 1 )); then
    log "✅ easytier 文件准备就绪，位于 $outdir"
  else
    log "⚠️  没有找到 easytier 文件；/easytier 端点在节点上将无法使用，直到文件提供完毕。"
  fi
}

# Write a default environment file if it does not exist
write_env_file() {
  if [[ -f "$ENV_FILE" ]]; then return 0; fi
  log "Writing $ENV_FILE"
  cat > "$ENV_FILE" <<EOF
# Flux Panel server environment
# Bind port for HTTP API
PORT=${PORT_CHOSEN:-6365}
# Database settings
# Default to SQLite for simpler out-of-the-box usage. To switch to MySQL,
# clear DB_DIALECT and set DB_HOST/DB_PORT/DB_NAME/DB_USER/DB_PASSWORD.
DB_DIALECT=sqlite
DB_SQLITE_PATH=${INSTALL_DIR}/panel.db
# MySQL settings (used only if DB_DIALECT is empty)
DB_HOST=127.0.0.1
DB_PORT=3306
DB_NAME=flux_panel
DB_USER=flux
DB_PASSWORD=123456
# JWT secret for API authentication
JWT_SECRET=flux-panel-secret
EOF
}

# Write the systemd service file
write_service() {
  cat > "$SERVICE_FILE" <<EOF
[Unit]
Description=Flux Panel Server
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
EnvironmentFile=-${ENV_FILE}
EnvironmentFile=-${DOT_ENV_FILE}
WorkingDirectory=${INSTALL_DIR}
ExecStart=${BIN_PATH}
Restart=always
RestartSec=3
LimitNOFILE=65535

[Install]
WantedBy=multi-user.target
EOF
  systemctl daemon-reload
  systemctl enable "$SERVICE_NAME" >/dev/null 2>&1 || true
}

# Build the server from source if no prebuilt binary is available
build_from_source() {
  if ! command -v go >/dev/null 2>&1; then
    log "Go toolchain not installed; cannot build from source."
    return 1
  fi
  local ldflags=("-s" "-w")
  if git -C "$ROOT_DIR" rev-parse --git-dir >/dev/null 2>&1; then
    local ver
    ver=$(git -C "$ROOT_DIR" describe --tags --always 2>/dev/null || true)
    if [[ -n "$ver" ]]; then
      ldflags+=("-X" "main.version=$ver")
    fi
  fi
  env CGO_ENABLED=0 go build -trimpath -buildvcs=false -ldflags "${ldflags[*]}" -o "$BIN_PATH" "$MAIN_PKG"
  [[ -x "$BIN_PATH" ]] || return 1
  mkdir -p "$INSTALL_DIR"
  # Attempt to build/copy frontend assets
  if command -v node >/dev/null 2>&1 && command -v npm >/dev/null 2>&1; then
    log "Building frontend assets..."
    (
      set -e
      cd "$ROOT_DIR/vite-frontend"
      npm install --legacy-peer-deps --no-audit --no-fund
      npm run build
    )
    if [[ -d "$ROOT_DIR/vite-frontend/dist" ]]; then
      rm -rf "$INSTALL_DIR/public"
      mkdir -p "$INSTALL_DIR/public"
      cp -r "$ROOT_DIR/vite-frontend/dist"/* "$INSTALL_DIR/public/"
      log "✅ Frontend assets installed to $INSTALL_DIR/public"
    else
      log "⚠️  Frontend build did not produce dist/; UI may be unavailable"
    fi
  else
    if [[ -d "$ROOT_DIR/vite-frontend/dist" ]]; then
      rm -rf "$INSTALL_DIR/public"
      mkdir -p "$INSTALL_DIR/public"
      cp -r "$ROOT_DIR/vite-frontend/dist"/* "$INSTALL_DIR/public/"
      log "✅ Frontend assets installed to $INSTALL_DIR/public"
    else
      log "⚠️  'node' or 'npm' not found; skipping frontend build."
      log "   - The API will run, but the web UI requires assets in $INSTALL_DIR/public"
      log "   - Use Docker image or prebuilt release tarball for a ready UI."
    fi
  fi
  install_install_sh || true
  install_flux_agents || true
  install_easytier || true
}

# Remove service and installation directories
uninstall() {
  log "Uninstalling $SERVICE_NAME..."
  systemctl stop "$SERVICE_NAME" >/dev/null 2>&1 || true
  systemctl disable "$SERVICE_NAME" >/dev/null 2>&1 || true
  rm -rf "$INSTALL_DIR"
  rm -f "$SERVICE_FILE"
  rm -f "$ENV_FILE"
  rm -f "$DOT_ENV_FILE"
  rm -f "$BIN_PATH"
  systemctl daemon-reload >/dev/null 2>&1 || true
  log "Uninstallation complete."
}

# Update the installation by uninstalling then reinstalling with preserved config
update() {
  # Update the service while preserving the existing SQLite database and configuration.
  # Do not uninstall completely; instead stop the service and perform a normal install.
  log "Updating $SERVICE_NAME..."
  # Stop the running service but do not remove installation files; this preserves the existing DB.
  systemctl stop "$SERVICE_NAME" >/dev/null 2>&1 || true
  # Call main without subcommand.  main() will reuse the saved configuration, clean
  # the install directory (preserving SQLite DB) and reinstall the binary and assets.
  main
}

# Main entry point for install / uninstall / update
main() {
  # Handle subcommands first
  if [[ "${1:-}" == "uninstall" ]]; then
    uninstall
    return 0
  fi
  if [[ "${1:-}" == "update" ]]; then
    update
    return 0
  fi
  # Load existing configuration (if any)
  read_config
  # Prompt for proxy prefix only if the variable has not been defined previously.
  # If PROXY_PREFIX is defined (even as an empty string) in the config file, do not prompt again.
  if ! [[ -v PROXY_PREFIX ]]; then
    log "Optional: set a proxy prefix for GitHub downloads (empty to skip)"
    read -rp "Proxy prefix (e.g. https://ghfast.top/): " PROXY_PREFIX
  fi
  # Prompt for architecture only if the variable has not been defined previously.
  if ! [[ -v ARCH ]]; then
    ARCH=$(prompt_arch)
  fi
  local arch="$ARCH"
  # Ensure installation directory exists and preserve sqlite DB if needed
  mkdir -p "$INSTALL_DIR"
  clean_install_dir_preserve_sqlite
  # Determine desired port and ensure it is available (or suggest nearest alternatives)
  local desired_port
  desired_port=$(read_env_val "$ENV_FILE" PORT || true)
  if [[ -z "$desired_port" ]]; then desired_port=$(read_env_val "$DOT_ENV_FILE" PORT || true); fi
  if [[ -z "$desired_port" ]]; then desired_port=6365; fi
  PORT_CHOSEN=$(ensure_port_available "$desired_port")
  if [[ "$PORT_CHOSEN" != "$desired_port" ]]; then
    # If env files already exist, update them in-place; otherwise write_env_file will write defaults with PORT_CHOSEN
    if [[ -f "$ENV_FILE" || -f "$DOT_ENV_FILE" ]]; then
      upsert_env_val "$ENV_FILE" PORT "$PORT_CHOSEN"
      upsert_env_val "$DOT_ENV_FILE" PORT "$PORT_CHOSEN"
    fi
  fi
  log "Downloading prebuilt server binary..."
  local file
  if file=$(download_prebuilt "$arch"); then
    extract_or_install "$file" || exit 1
  else
    log "Download failed; trying to build from source..."
    build_from_source || { log "Build failed"; exit 1; }
  fi
  install_install_sh || true
  install_flux_agents || true
  install_easytier || true
  write_env_file
  if [[ ! -f "$DOT_ENV_FILE" ]]; then
    cat > "$DOT_ENV_FILE" <<EOF
PORT=${PORT_CHOSEN:-6365}
DB_DIALECT=sqlite
DB_SQLITE_PATH=${INSTALL_DIR}/panel.db
DB_HOST=127.0.0.1
DB_PORT=3306
DB_NAME=flux_panel
DB_USER=flux
DB_PASSWORD=123456
JWT_SECRET=flux-panel-secret
EOF
  fi
  write_service
  systemctl restart "$SERVICE_NAME"
  systemctl status --no-pager "$SERVICE_NAME" || true
  printf '\n✅ Installed. Configure env in %s and restart via: systemctl restart %s\n' "$ENV_FILE" "$SERVICE_NAME"
  # Save current configuration for future runs
  save_config
}

# Invoke main with provided arguments
main "$@"
