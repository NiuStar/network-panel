#!/usr/bin/env bash
set -euo pipefail

# Cross-compile flux-agent for a list of OS/ARCH targets.
# Outputs to golang-backend/public/flux-agent (created if missing).

ROOT_DIR="$(cd "$(dirname "$0")/.." && pwd)"
MAIN_PKG="${ROOT_DIR}/golang-backend/cmd/flux-agent"
OUT_DIR="${ROOT_DIR}/golang-backend/public/flux-agent"
mkdir -p "$OUT_DIR"

if ! command -v go >/dev/null 2>&1; then
  echo "Go toolchain not found in PATH" >&2
  exit 1
fi

# Requested targets
TARGETS=(
  darwin_arm64
  freebsd_386
  freebsd_amd64
  linux_386
  linux_amd64
  linux_amd64v3
  linux_arm64
  linux_armv5
  linux_armv6
  linux_armv7
  linux_loong64
  linux_mips64le_hardfloat
  linux_mips64_hardfloat
  linux_mipsle_hardfloat
  linux_mipsle_softfloat
  linux_mips_hardfloat
  linux_mips_softfloat
  linux_riscv64
  linux_s390x
  windows_386
  windows_amd64
  windows_amd64v3
  windows_arm64
)

build_one() {
  local target="$1"
  local os arch extra envs=()
  os="${target%%_*}"
  arch="${target#*_}"

  case "$arch" in
    amd64v3) envs+=("GOARCH=amd64" "GOAMD64=v3"); out_arch="amd64v3" ;;
    amd64)   envs+=("GOARCH=amd64"); out_arch="amd64" ;;
    386)     envs+=("GOARCH=386");   out_arch="386" ;;
    arm64)   envs+=("GOARCH=arm64"); out_arch="arm64" ;;
    armv5)   envs+=("GOARCH=arm" "GOARM=5"); out_arch="armv5" ;;
    armv6)   envs+=("GOARCH=arm" "GOARM=6"); out_arch="armv6" ;;
    armv7)   envs+=("GOARCH=arm" "GOARM=7"); out_arch="armv7" ;;
    loong64) envs+=("GOARCH=loong64"); out_arch="loong64" ;;
    riscv64) envs+=("GOARCH=riscv64"); out_arch="riscv64" ;;
    s390x)   envs+=("GOARCH=s390x"); out_arch="s390x" ;;
    mips64le_hardfloat) envs+=("GOARCH=mips64le" "GOMIPS64=hardfloat"); out_arch="mips64le-hardfloat" ;;
    mips64_hardfloat)   envs+=("GOARCH=mips64"  "GOMIPS64=hardfloat"); out_arch="mips64-hardfloat" ;;
    mipsle_hardfloat)   envs+=("GOARCH=mipsle"  "GOMIPS=hardfloat");  out_arch="mipsle-hardfloat" ;;
    mipsle_softfloat)   envs+=("GOARCH=mipsle"  "GOMIPS=softfloat");  out_arch="mipsle-softfloat" ;;
    mips_hardfloat)     envs+=("GOARCH=mips"    "GOMIPS=hardfloat");  out_arch="mips-hardfloat" ;;
    mips_softfloat)     envs+=("GOARCH=mips"    "GOMIPS=softfloat");  out_arch="mips-softfloat" ;;
    *) echo "Unknown arch token: $arch" >&2; return 1 ;;
  esac

  local out_name="flux-agent-${os}-${out_arch}"
  local ext=""
  if [[ "$os" == "windows" ]]; then ext=".exe"; fi

  echo "==> Building $target -> $out_name$ext"
  local ldflags=("-s" "-w")
  # Inject version if available
  if git -C "$ROOT_DIR" rev-parse --git-dir >/dev/null 2>&1; then
    ver=$(git -C "$ROOT_DIR" describe --tags --always 2>/dev/null || true)
    if [[ -n "$ver" ]]; then
      ldflags+=("-X" "main.version=$ver")
    fi
  fi
  (cd "$ROOT_DIR" && \
    env CGO_ENABLED=0 GOOS="$os" ${envs[@]} \
    go build -trimpath -buildvcs=false -ldflags "${ldflags[*]}" -o "$OUT_DIR/$out_name$ext" "$MAIN_PKG")

  # Also produce agent2 variant (same binary, different name triggers agent2 role by argv0)
  local out_name2="flux-agent2-${os}-${out_arch}"
  cp "$OUT_DIR/$out_name$ext" "$OUT_DIR/$out_name2$ext"
  chmod +x "$OUT_DIR/$out_name2$ext" || true
}

main() {
  for t in "${TARGETS[@]}"; do
    build_one "$t"
  done
  echo "\nAll binaries are in: $OUT_DIR"
}

main "$@"
