#!/usr/bin/env bash
set -euo pipefail

APT_UPDATED=0

run_root() {
  if [ "$(id -u)" -eq 0 ]; then
    "$@"
    return
  fi
  if command -v sudo >/dev/null 2>&1; then
    sudo "$@"
    return
  fi
  echo "[install] need root privileges for: $*"
  return 1
}

install_pkg() {
  local pkg="$1"
  if command -v apt-get >/dev/null 2>&1; then
    if [ "$APT_UPDATED" -eq 0 ]; then
      run_root apt-get update
      APT_UPDATED=1
    fi
    run_root apt-get install -y "$pkg"
    return
  fi
  if command -v apt >/dev/null 2>&1; then
    if [ "$APT_UPDATED" -eq 0 ]; then
      run_root apt update
      APT_UPDATED=1
    fi
    run_root apt install -y "$pkg"
    return
  fi
  if command -v dnf >/dev/null 2>&1; then
    run_root dnf install -y "$pkg"
    return
  fi
  if command -v yum >/dev/null 2>&1; then
    run_root yum install -y "$pkg"
    return
  fi
  if command -v apk >/dev/null 2>&1; then
    run_root apk add --no-cache "$pkg"
    return
  fi
  if command -v pacman >/dev/null 2>&1; then
    run_root pacman -Sy --noconfirm "$pkg"
    return
  fi
  if command -v zypper >/dev/null 2>&1; then
    run_root zypper --non-interactive install "$pkg"
    return
  fi
  echo "[install] no supported package manager found to install ${pkg}"
  return 1
}

ensure_dep() {
  local bin="$1"
  if command -v "$bin" >/dev/null 2>&1; then
    return 0
  fi
  echo "[install] missing ${bin}, installing..."
  install_pkg "$bin"
  command -v "$bin" >/dev/null 2>&1
}

ensure_dep wget
ensure_dep curl
ensure_dep unzip

SERVER="{SERVER}"
if [ "$SERVER" = "/" ]; then
  SERVER=""
fi
INSTALL_URL=""
STATIC_URL="https://panel-static.199028.xyz/network-panel/easytier/install_easytier.sh"
ok=0
if [ -n "$SERVER" ]; then
  INSTALL_URL="${SERVER%/}/easytier/install_easytier.sh"
  echo "[install] fetching easytier install.sh from ${INSTALL_URL}"
  if wget -T 10 --tries=1 -O /tmp/easytier.sh "$INSTALL_URL"; then
    ok=1
  else
    echo "[install] panel host unavailable, trying static host"
    if wget -T 10 --tries=1 -O /tmp/easytier.sh "$STATIC_URL"; then
      ok=1
    fi
  fi
else
  echo "[install] panel host empty, skipping static host"
fi
if [ $ok -ne 1 ]; then
  echo "[install] install script unavailable, trying fallbacks..."
  FALLBACKS=(
    "https://raw.githubusercontent.com/NiuStar/network-panel/refs/heads/main/easytier/install_easytier.sh"
    "https://ghfast.top/https://raw.githubusercontent.com/NiuStar/network-panel/refs/heads/main/easytier/install_easytier.sh"
    "https://proxy.529851.xyz/https://raw.githubusercontent.com/NiuStar/network-panel/refs/heads/main/easytier/install_easytier.sh"
  )
  for u in "${FALLBACKS[@]}"; do
    if wget -T 10 --tries=1 -O /tmp/easytier.sh "$u"; then
      ok=1
      break
    fi
  done
  if [ $ok -ne 1 ]; then
    echo "[install] failed to fetch install script from all sources"
    exit 1
  fi
fi
chmod +x /tmp/easytier.sh
sudo bash /tmp/easytier.sh uninstall || true
sudo rm -rf /opt/easytier

attempt_install() {
  local label="$1"
  shift
  echo "[install] attempt: ${label}"
  sudo bash /tmp/easytier.sh install "$@"
  local rc=$?
  if [ $rc -eq 0 ]; then
    if [ ! -x /opt/easytier/easytier-core ] || [ ! -x /opt/easytier/easytier-cli ]; then
      rc=1
    fi
  fi
  return $rc
}

set +e
attempt_install "direct" --no-gh-proxy
rc=$?
if [ $rc -ne 0 ]; then
  echo "[install] direct install failed, retrying via ghfast"
  attempt_install "ghfast" --gh-proxy https://ghfast.top/
  rc=$?
fi
if [ $rc -ne 0 ]; then
  echo "[install] ghfast install failed, retrying via proxy"
  attempt_install "proxy" --gh-proxy https://proxy.529851.xyz/
  rc=$?
fi
set -e
if [ $rc -ne 0 ]; then
  exit $rc
fi
echo "[install] done"
