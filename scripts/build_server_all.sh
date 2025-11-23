#!/usr/bin/env bash
set -euo pipefail

# Cross-compile network-panel server for common Linux targets.
# Outputs to golang-backend/public/server as network-panel-server-linux-<arch>

ROOT_DIR="$(cd "$(dirname "$0")/.." && pwd)"
MAIN_PKG="${ROOT_DIR}/golang-backend/cmd/server"
OUT_DIR="${ROOT_DIR}/golang-backend/public/server"
mkdir -p "$OUT_DIR"

if ! command -v go >/dev/null 2>&1; then
  echo "Go toolchain not found in PATH" >&2
  exit 1
fi

TARGETS=(
  linux_amd64
  linux_amd64v3
  linux_arm64
  linux_armv7
  linux_386
  linux_riscv64
  linux_s390x
  linux_loong64
)

build_one() {
  local target="$1"
  local os arch envs=()
  os="${target%%_*}"
  arch="${target#*_}"

  case "$arch" in
    amd64v3) envs+=("GOARCH=amd64" "GOAMD64=v3"); out_arch="amd64v3" ;;
    amd64)   envs+=("GOARCH=amd64"); out_arch="amd64" ;;
    386)     envs+=("GOARCH=386");   out_arch="386" ;;
    arm64)   envs+=("GOARCH=arm64"); out_arch="arm64" ;;
    armv7)   envs+=("GOARCH=arm" "GOARM=7"); out_arch="armv7" ;;
    loong64) envs+=("GOARCH=loong64"); out_arch="loong64" ;;
    riscv64) envs+=("GOARCH=riscv64"); out_arch="riscv64" ;;
    s390x)   envs+=("GOARCH=s390x"); out_arch="s390x" ;;
    *) echo "Unknown arch token: $arch" >&2; return 1 ;;
  esac

  local out_name="network-panel-server-${os}-${out_arch}"
  echo "==> Building $target -> $out_name"

  local ldflags=("-s" "-w")
  if git -C "$ROOT_DIR" rev-parse --git-dir >/dev/null 2>&1; then
    ver=$(git -C "$ROOT_DIR" describe --tags --always 2>/dev/null || true)
    if [[ -n "$ver" ]]; then
      ldflags+=("-X" "main.version=$ver")
    fi
  fi

  (cd "$ROOT_DIR" && \
    env CGO_ENABLED=0 GOOS="$os" ${envs[@]} \
    go build -trimpath -buildvcs=false -ldflags "${ldflags[*]}" -o "$OUT_DIR/$out_name" "$MAIN_PKG")
}

main() {
  for t in "${TARGETS[@]}"; do
    build_one "$t"
  done
  echo "\nAll server binaries are in: $OUT_DIR"
}

main "$@"

