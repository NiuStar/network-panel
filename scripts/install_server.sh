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

  # Check for frontend assets when using archive; warn if missing for plain binary
  if [[ ! -d "$INSTALL_DIR/public" ]]; then
    log "⚠️  Frontend assets not found at $INSTALL_DIR/public"
    log "   - If you downloaded a single binary, the web UI won't be available."
    log "   - Recommended: use the Docker image or a release tarball that contains 'public/'."
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
}

write_env_file() {
  if [[ -f "$ENV_FILE" ]]; then return 0; fi
  log "Writing $ENV_FILE"
  cat > "$ENV_FILE" <<EOF
# Flux Panel server environment
# Bind port for HTTP API
PORT=6365
# Database settings (MySQL)
DB_HOST=127.0.0.1
DB_PORT=3306
DB_NAME=flux_panel
DB_USER=flux
DB_PASSWORD=123456
# Expected agent version for auto-upgrade.
# Agents connecting with a different version will receive an Upgrade command.
# Example: AGENT_VERSION=go-agent-1.0.7
# Leave empty to use server default.
AGENT_VERSION=
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

  write_env_file
  write_service
  systemctl restart "$SERVICE_NAME"
  systemctl status --no-pager "$SERVICE_NAME" || true
  printf '\n✅ Installed. Configure env in %s and restart via: systemctl restart %s\n' "$ENV_FILE" "$SERVICE_NAME"
}

main "$@"
