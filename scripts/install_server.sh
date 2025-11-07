#!/usr/bin/env bash
set -euo pipefail

# One-click install and run the network-panel server as a systemd service (Linux only).
# This is NOT the agent installer. It installs the backend server binary
# and configures DB env + auto-start.

log() { printf '%s\n' "$*" >&2; }

if [[ "$(uname -s)" != "Linux" ]]; then
  log "This installer supports Linux only."
  exit 1
fi

SERVICE_NAME="network-panel"
INSTALL_DIR="/opt/network-panel"
BIN_PATH="/usr/local/bin/network-panel-server"
ENV_FILE="/etc/default/network-panel"
SERVICE_FILE="/etc/systemd/system/${SERVICE_NAME}.service"
PROXY_PREFIX=""
ROOT_DIR="$(cd "$(dirname "$0")/.." && pwd)"
MAIN_PKG="${ROOT_DIR}/golang-backend/cmd/server"
FRONTEND_ASSET_NAME="frontend-dist.zip"
DOT_ENV_FILE="/opt/network-panel/.env"

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

prompt_arch() {
  local detected
  detected="$(detect_arch)"
  log "Detected arch: ${detected}"
  read -rp "Use detected arch ($detected)? [Y/n]: " yn
  yn=${yn:-Y}
  if [[ "$yn" =~ ^[Yy]$ ]]; then
    printf '%s\n' "$detected"; return
  fi
  log "Available: amd64, amd64v3, arm64, armv7, 386, riscv64, s390x, loong64"
  read -rp "Enter arch: " a
  a=${a:-$detected}
  printf '%s\n' "$a"
}

choose_source() {
  # Not currently used; kept for future.
  log "How to obtain the server binary?"
  log "  1) Download prebuilt from GitHub releases (recommended)"
  log "  2) Build from source (requires Go toolchain)"
  read -rp "Choose [1/2]: " ch
  ch=${ch:-1}
  printf '%s\n' "$ch"
}

download_prebuilt() {
  local arch="$1"
  local base="https://github.com/NiuStar/network-panel/releases/latest/download"
  if [[ -n "$PROXY_PREFIX" ]]; then base="${PROXY_PREFIX}${base}"; fi
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
  return 1
}

download_frontend_dist() {
  local base="https://github.com/NiuStar/network-panel/releases/latest/download"
  if [[ -n "$PROXY_PREFIX" ]]; then base="${PROXY_PREFIX}${base}"; fi
  local url="${base}/${FRONTEND_ASSET_NAME}"
  log "Trying to download frontend assets: $url"
  if curl -fSL --retry 3 --retry-delay 1 "$url" -o /tmp/frontend-dist.zip; then
    printf '/tmp/frontend-dist.zip\n'
    return 0
  fi
  return 1
}

extract_or_install() {
  local file="$1"
  mkdir -p "$INSTALL_DIR"

  # Detect archive by extension
  if [[ "$file" =~ \.tar\.gz$|\.tgz$ ]]; then
    tar -xzf "$file" -C "$INSTALL_DIR"
  elif [[ "$file" =~ \.zip$ ]]; then
    if command -v unzip >/dev/null 2>&1; then
      unzip -o "$file" -d "$INSTALL_DIR"
    else
      log "unzip not found, please install unzip or provide a .tar.gz"
      return 1
    fi
  else
    # assume plain binary
    install -m 0755 "$file" "$BIN_PATH"
  fi

  # If binary exists inside INSTALL_DIR after extraction, move it to BIN_PATH
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

  # Ensure frontend assets exist; if missing, try to fetch the dist zip from GitHub release
  if [[ ! -d "$INSTALL_DIR/public" || -z "$(find "$INSTALL_DIR/public" -mindepth 1 -print -quit 2>/dev/null)" ]]; then
    log "Frontend assets missing, attempting to download ${FRONTEND_ASSET_NAME}..."
    local fzip
    if fzip=$(download_frontend_dist); then
      if command -v unzip >/dev/null 2>&1; then
        mkdir -p "$INSTALL_DIR/public"
        unzip -qo "$fzip" -d "$INSTALL_DIR/public"
        log "✅ Frontend assets installed to $INSTALL_DIR/public"
      else
        log "⚠️  'unzip' not found; cannot extract ${FRONTEND_ASSET_NAME}. Please install unzip and re-run."
      fi
    else
      log "⚠️  Failed to download ${FRONTEND_ASSET_NAME}. The web UI may be unavailable."
      log "   - You can build locally (vite-frontend) and copy dist/* to $INSTALL_DIR/public"
    fi
  fi
}

# Ensure install.sh is available for node bootstrap (served at GET /install.sh)
install_install_sh() {
  local dst="$INSTALL_DIR/install.sh"
  # Prefer repo copy if available (running from source tree)
  if [[ -f "$ROOT_DIR/install.sh" ]]; then
    install -m 0755 "$ROOT_DIR/install.sh" "$dst"
    log "✅ install.sh installed to $dst"
    return 0
  fi
  # Otherwise download from GitHub main branch
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

install_flux_agents() {
  # Ensure a minimal set of flux-agent binaries are available via /flux-agent/:file
  # Preferred source order: local repo copies -> GitHub Releases (latest)
  local outdir="$INSTALL_DIR/public/flux-agent"
  mkdir -p "$outdir"

  local localdir="$ROOT_DIR/golang-backend/public/flux-agent"
  local need=("flux-agent-linux-amd64" "flux-agent-linux-arm64" "flux-agent-linux-armv7")
  local have_any=0

  # 1) Try local copies first
  if [[ -d "$localdir" ]]; then
    for f in "${need[@]}"; do
      if [[ -f "$localdir/$f" ]]; then
        install -m 0755 "$localdir/$f" "$outdir/$f"
        have_any=1
        log "✅ copied local $f"
      fi
    done
  fi

  # 2) Fetch missing from GitHub Releases latest
  local base="https://github.com/NiuStar/network-panel/releases/latest/download"
  if [[ -n "$PROXY_PREFIX" ]]; then base="${PROXY_PREFIX}${base}"; fi
  for f in "${need[@]}"; do
    if [[ ! -f "$outdir/$f" ]]; then
      local url="$base/$f"
      log "Downloading flux-agent: $f"
      # Capture HTTP code for better error messages
      local http_code
      http_code=$(curl -fSL --retry 3 --retry-delay 1 --write-out '%{http_code}' --output "$outdir/$f" "$url" 2>/dev/null || true)
      if [[ -s "$outdir/$f" && ( "$http_code" == "200" || "$http_code" == "302" || "$http_code" == "000" ) ]]; then
        chmod +x "$outdir/$f"
        have_any=1
        log "✅ downloaded $f to $outdir/$f"
      else
        rm -f "$outdir/$f" 2>/dev/null || true
        log "❌ failed to download $f from $url (HTTP ${http_code:-unknown})"
      fi
    fi
  done

  if (( have_any == 1 )); then
    log "✅ flux-agent binaries ready in $outdir"
  else
    log "⚠️  No flux-agent binaries available; /flux-agent endpoint will be unavailable for nodes until provided."
  fi
}

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
  [[ -x "$BIN_PATH" ]]

  # Try to build/copy frontend assets for the web UI
  mkdir -p "$INSTALL_DIR"
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
    # Fallback: copy existing dist if present
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
  # Always install install.sh alongside WorkingDirectory for /install.sh serving
  install_install_sh || true
  # Provide flux-agent binaries for node auto-upgrade/download
  install_flux_agents || true
}

write_env_file() {
  if [[ -f "$ENV_FILE" ]]; then return 0; fi
  log "Writing $ENV_FILE"
  cat > "$ENV_FILE" <<EOF
# Flux Panel server environment
# Bind port for HTTP API
PORT=6365
# Database settings
# Default to SQLite for simpler out-of-the-box usage. To switch to MySQL,
# clear DB_DIALECT and set DB_HOST/DB_PORT/DB_NAME/DB_USER/DB_PASSWORD.
DB_DIALECT=sqlite
DB_SQLITE_PATH=/opt/network-panel/panel.db
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

main() {
  log "Optional: set a proxy prefix for GitHub downloads (empty to skip)"
  read -rp "Proxy prefix (e.g. https://ghfast.top/): " PROXY_PREFIX

  local arch
  arch=$(prompt_arch)

  mkdir -p "$INSTALL_DIR"

  log "Downloading prebuilt server binary..."
  local file
  if file=$(download_prebuilt "$arch"); then
     extract_or_install "$file" || exit 1
  else
     log "Download failed; trying to build from source..."
     build_from_source || { log "Build failed"; exit 1; }
  fi

  # Ensure install.sh present for node bootstrap
  install_install_sh || true
  # Ensure flux-agent binaries are available
  install_flux_agents || true

  write_env_file
  # Also create a project-local .env with sane defaults
  if [[ ! -f "$DOT_ENV_FILE" ]]; then
    cat > "$DOT_ENV_FILE" <<EOF
PORT=6365
DB_DIALECT=sqlite
DB_SQLITE_PATH=/opt/network-panel/panel.db
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
}

main "$@"
