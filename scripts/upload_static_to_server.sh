#!/usr/bin/env bash
set -euo pipefail

# Sync built static assets (scripts/binaries/configs) to a remote static host.
# Default destination: root@154.19.43.125:/root/docker/nginx/static/network-panel
# Requires: sshpass (for password-based auth) or configured SSH keys.

ROOT_DIR="$(cd "$(dirname "$0")/.." && pwd)"
STAGE_DIR="${ROOT_DIR}/.release-static"
BASE_NAME="network-panel"

# Remote configuration (override with env vars if needed)
REMOTE_HOST="${REMOTE_HOST:-154.19.43.125}"
REMOTE_PORT="${REMOTE_PORT:-22}"
REMOTE_USER="${REMOTE_USER:-root}"
REMOTE_PASS="${REMOTE_PASS:-Wangzai007..@@}"
REMOTE_BASE="${REMOTE_BASE:-/root/docker/nginx/static}"

DEST_DIR_REMOTE="${REMOTE_BASE}/${BASE_NAME}"

log(){ printf '%s\n' "$*" >&2; }

prepare_stage(){
  rm -rf "$STAGE_DIR" && mkdir -p "$STAGE_DIR"
  local dst="$STAGE_DIR/${BASE_NAME}"
  mkdir -p "$dst"

  # Top-level scripts/configs
  cp -a "$ROOT_DIR/install.sh" "$dst/" 2>/dev/null || true
  cp -a "$ROOT_DIR/panel_install.sh" "$dst/" 2>/dev/null || true
  cp -a "$ROOT_DIR/docker-compose-v4.yml" "$dst/" 2>/dev/null || true
  cp -a "$ROOT_DIR/docker-compose-v4_mysql.yml" "$dst/" 2>/dev/null || true

  # scripts
  mkdir -p "$dst/scripts"
  cp -a "$ROOT_DIR/scripts/install_server.sh" "$dst/scripts/" 2>/dev/null || true

  # easytier
  mkdir -p "$dst/easytier"
  cp -a "$ROOT_DIR/easytier/default.conf" "$dst/easytier/" 2>/dev/null || true
  cp -a "$ROOT_DIR/easytier/install.sh" "$dst/easytier/" 2>/dev/null || true

  # flux-agent binaries
  if [[ -d "$ROOT_DIR/golang-backend/public/flux-agent" ]]; then
    mkdir -p "$dst/flux-agent"
    cp -a "$ROOT_DIR/golang-backend/public/flux-agent/"* "$dst/flux-agent/" 2>/dev/null || true
  fi

  # server binaries (multi-arch) if present
  if [[ -d "$ROOT_DIR/golang-backend/public/server" ]]; then
    mkdir -p "$dst/server"
    cp -a "$ROOT_DIR/golang-backend/public/server/"* "$dst/server/" 2>/dev/null || true
  fi

  # frontend dist archive if present
  if [[ -f "$ROOT_DIR/golang-backend/public/frontend/frontend-dist.zip" ]]; then
    mkdir -p "$dst/frontend"
    cp -a "$ROOT_DIR/golang-backend/public/frontend/frontend-dist.zip" "$dst/frontend/" 2>/dev/null || true
  fi

  echo "$dst"
}

_do_ssh(){
  local cmd="$1"
  if command -v sshpass >/dev/null 2>&1; then
    sshpass -p "$REMOTE_PASS" ssh -p "$REMOTE_PORT" -o StrictHostKeyChecking=no "${REMOTE_USER}@${REMOTE_HOST}" "$cmd"
    return $?
  fi
  if command -v expect >/dev/null 2>&1; then
    expect <<EOF
set timeout -1
spawn ssh -p ${REMOTE_PORT} -o StrictHostKeyChecking=no ${REMOTE_USER}@${REMOTE_HOST} "$cmd"
expect {
  -re ".*assword:.*" {send "${REMOTE_PASS}\r"; exp_continue}
  eof
}
EOF
    return 0
  fi
  # Fallback: use SSH_ASKPASS trick (no TTY); requires setsid
  if command -v setsid >/dev/null 2>&1; then
    local ap; ap="$(mktemp)"; chmod 700 "$ap"; printf '#!/bin/sh\necho %s\n' "$REMOTE_PASS" > "$ap"
    # Force askpass, run without TTY; ignore host key prompt
    DISPLAY=none SSH_ASKPASS_REQUIRE=force SSH_ASKPASS="$ap" \
      setsid -w ssh -p "$REMOTE_PORT" -o StrictHostKeyChecking=no -o NumberOfPasswordPrompts=1 \
      "${REMOTE_USER}@${REMOTE_HOST}" "$cmd" </dev/null
    local rc=$?; rm -f "$ap"; return $rc
  fi
  # Last resort: interactive
  log "sshpass/expect/setsid not found; running interactive ssh"
  ssh -p "$REMOTE_PORT" -o StrictHostKeyChecking=no "${REMOTE_USER}@${REMOTE_HOST}" "$cmd"
}

_do_scp(){
  local src="$1" dest="$2"
  if command -v sshpass >/dev/null 2>&1; then
    sshpass -p "$REMOTE_PASS" scp -P "$REMOTE_PORT" -o StrictHostKeyChecking=no -r $src "$dest"
    return $?
  fi
  if command -v expect >/dev/null 2>&1; then
    expect <<EOF
set timeout -1
spawn scp -P ${REMOTE_PORT} -o StrictHostKeyChecking=no -r $src ${dest}
expect {
  -re ".*assword:.*" {send "${REMOTE_PASS}\r"; exp_continue}
  eof
}
EOF
    return 0
  fi
  if command -v setsid >/dev/null 2>&1; then
    local ap; ap="$(mktemp)"; chmod 700 "$ap"; printf '#!/bin/sh\necho %s\n' "$REMOTE_PASS" > "$ap"
    DISPLAY=none SSH_ASKPASS_REQUIRE=force SSH_ASKPASS="$ap" \
      setsid -w scp -P "$REMOTE_PORT" -o StrictHostKeyChecking=no -r $src "$dest" </dev/null
    local rc=$?; rm -f "$ap"; return $rc
  fi
  log "sshpass/expect/setsid not found; running interactive scp"
  scp -P "$REMOTE_PORT" -o StrictHostKeyChecking=no -r $src "$dest"
}

sync_to_remote(){
  local src_dir="$1"
  log "Uploading to ${REMOTE_USER}@${REMOTE_HOST}:${DEST_DIR_REMOTE}"
  _do_ssh "mkdir -p '${DEST_DIR_REMOTE}'"
  # Pack as tar.gz to ensure complete, recursive upload in one shot
  local tmp
  tmp="$(mktemp -t np-static.XXXXXX).tgz"
  (cd "$src_dir" && tar -czf "$tmp" .)
  local remote_pkg="/tmp/np-static.$$.$RANDOM.tgz"
  _do_scp "$tmp" "${REMOTE_USER}@${REMOTE_HOST}:${remote_pkg}"
  rm -f "$tmp" 2>/dev/null || true
  _do_ssh "mkdir -p '${DEST_DIR_REMOTE}' && tar -xzf '${remote_pkg}' -C '${DEST_DIR_REMOTE}' && rm -f '${remote_pkg}'"
  log "✅ Uploaded static assets to ${REMOTE_HOST}:${DEST_DIR_REMOTE}"
}

main(){
  local bundle
  bundle=$(prepare_stage)
  sync_to_remote "$bundle"
}

main "$@"
