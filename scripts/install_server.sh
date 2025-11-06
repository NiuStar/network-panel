#!/usr/bin/env bash
set -euo pipefail

# One-click install and run the flux-panel server as a systemd service (Linux only).
# This is NOT the agent installer. It installs the backend server binary
# and configures DB env + auto-start.

if [[ "$(uname -s)" != "Linux" ]]; then
  echo "This installer supports Linux only." >&2
  exit 1
fi

SERVICE_NAME="flux-panel"
INSTALL_DIR="/opt/flux-panel"
BIN_PATH="/usr/local/bin/flux-panel-server"
ENV_FILE="/etc/default/flux-panel"
SERVICE_FILE="/etc/systemd/system/${SERVICE_NAME}.service"
PROXY_PREFIX=""
ROOT_DIR="$(cd "$(dirname "$0")/.." && pwd)"
MAIN_PKG="${ROOT_DIR}/golang-backend/cmd/server"

detect_arch() {
  local m=$(uname -m)
  case "$m" in
    x86_64|amd64) echo amd64 ;;
    aarch64|arm64) echo arm64 ;;
    armv7l|armv7|armhf) echo armv7 ;;
    i386|i686) echo 386 ;;
    riscv64) echo riscv64 ;;
    s390x) echo s390x ;;
    loongarch64) echo loong64 ;;
    *) echo amd64 ;;
  esac
}

prompt_arch() {
  local detected="$(detect_arch)"
  echo "Detected arch: $detected"
  read -rp "Use detected arch ($detected)? [Y/n]: " yn
  yn=${yn:-Y}
  if [[ "$yn" =~ ^[Yy]$ ]]; then
    echo "$detected"; return
  fi
  echo "Available: amd64, amd64v3, arm64, armv7, 386, riscv64, s390x, loong64"
  read -rp "Enter arch: " a
  a=${a:-$detected}
  echo "$a"
}

choose_source() {
  echo "How to obtain the server binary?"
  echo "  1) Download prebuilt from GitHub releases (recommended)"
  echo "  2) Build from source (requires Go toolchain)"
  read -rp "Choose [1/2]: " ch
  ch=${ch:-1}
  echo "$ch"
}

download_prebuilt() {
  local arch="$1"
  local base="https://github.com/NiuStar/flux-panel/releases/latest/download"
  if [[ -n "$PROXY_PREFIX" ]]; then base="${PROXY_PREFIX}${base}"; fi
  # Try a few common asset names; adjust to your release naming
  local tried=()
  for name in \
    "flux-panel-server-linux-${arch}" \
    "server-linux-${arch}" \
    "flux-panel_linux_${arch}.tar.gz" \
    "server_linux_${arch}.tar.gz"; do
    tried+=("$name")
    if curl -fSL --retry 3 --retry-delay 1 "$base/$name" -o /tmp/flux-panel.dl 2>/dev/null; then
      echo "/tmp/flux-panel.dl"; return 0
    fi
  done
  echo ""; return 1
}

extract_or_install() {
  local file="$1"
  mkdir -p "$INSTALL_DIR"
  # Try to detect archive by extension
  if [[ "$file" =~ \.tar\.gz$|\.tgz$ ]]; then
    tar -xzf "$file" -C "$INSTALL_DIR"
  elif [[ "$file" =~ \.zip$ ]]; then
    if command -v unzip >/dev/null 2>&1; then
      unzip -o "$file" -d "$INSTALL_DIR"
    else
      echo "unzip not found, please install unzip or provide a .tar.gz" >&2
      return 1
    fi
  else
    # assume plain binary
    install -m 0755 "$file" "$BIN_PATH"
  fi

  # If binary exists inside INSTALL_DIR after extraction, move it to BIN_PATH
  if [[ ! -x "$BIN_PATH" ]]; then
    # search for server binary
    local cand
    cand=$(find "$INSTALL_DIR" -maxdepth 2 -type f -name "server" -o -name "flux-panel-server" | head -n1 || true)
    if [[ -n "$cand" ]]; then
      install -m 0755 "$cand" "$BIN_PATH"
    fi
  fi
  if [[ ! -x "$BIN_PATH" ]]; then
    echo "Server binary not found after extraction." >&2
    return 1
  fi

  # Check for frontend assets when using archive; warn if missing for plain binary
  if [[ ! -d "$INSTALL_DIR/public" ]]; then
    echo "⚠️  Frontend assets not found at $INSTALL_DIR/public"
    echo "   - If you downloaded a single binary, the web UI won't be available."
    echo "   - Recommended: use the Docker image or a release tarball that contains 'public/'."
  fi
}

build_from_source() {
  if ! command -v go >/dev/null 2>&1; then
    echo "Go toolchain not installed; cannot build from source." >&2
    return 1
  fi
  # Build using the repository where this script lives
  local ldflags=("-s" "-w")
  if git -C "$ROOT_DIR" rev-parse --git-dir >/dev/null 2>&1; then
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
    echo "Building frontend assets..."
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
      echo "✅ Frontend assets installed to $INSTALL_DIR/public"
    else
      echo "⚠️  Frontend build did not produce dist/; UI may be unavailable" >&2
    fi
  else
    # Fallback: copy existing dist if present
    if [[ -d "$ROOT_DIR/vite-frontend/dist" ]]; then
      rm -rf "$INSTALL_DIR/public"
      mkdir -p "$INSTALL_DIR/public"
      cp -r "$ROOT_DIR/vite-frontend/dist"/* "$INSTALL_DIR/public/"
      echo "✅ Frontend assets installed to $INSTALL_DIR/public"
    else
      echo "⚠️  'node' or 'npm' not found; skipping frontend build." >&2
      echo "   - The API will run, but the web UI requires assets in $INSTALL_DIR/public" >&2
      echo "   - Use Docker image or prebuilt release tarball for a ready UI." >&2
    fi
  fi
}

write_env_file() {
  if [[ -f "$ENV_FILE" ]]; then return 0; fi
  echo "Writing $ENV_FILE"
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
  echo "Optional: set a proxy prefix for GitHub downloads (empty to skip)"
  read -rp "Proxy prefix (e.g. https://ghfast.top/): " PROXY_PREFIX

  local arch
  arch=$(prompt_arch)

  mkdir -p "$INSTALL_DIR"

  local mode
  mode=$(choose_source)
  if [[ "$mode" == "1" ]]; then
    echo "Downloading prebuilt server binary..."
    if file=$(download_prebuilt "$arch"); then
      extract_or_install "$file" || exit 1
    else
      echo "Download failed; trying to build from source..."
      build_from_source || { echo "Build failed" >&2; exit 1; }
    fi
  else
    echo "Building from source..."
    build_from_source || { echo "Build failed" >&2; exit 1; }
  fi

  write_env_file
  write_service
  systemctl restart "$SERVICE_NAME"
  systemctl status --no-pager "$SERVICE_NAME" || true
  echo "\n✅ Installed. Configure env in ${ENV_FILE} and restart via: systemctl restart ${SERVICE_NAME}"
}

main "$@"
