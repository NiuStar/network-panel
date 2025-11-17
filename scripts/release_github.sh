#!/usr/bin/env bash
set -euo pipefail

# Release helper: package assets and publish a GitHub release
# Requirements:
#  - gh (GitHub CLI) authenticated (gh auth login)
#  - node/npm if you want to build frontend (optional; otherwise uses existing dist)
#  - go toolchain if you want to build extra binaries (optional)

ROOT_DIR=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
cd "$ROOT_DIR"

version_from_file() {
  # parse version from golang-backend/internal/app/version/version.go
  local f="golang-backend/internal/app/version/version.go"
  if [[ -f "$f" ]]; then
    # extract digits like 1.2.3 from the assigned var
    sed -n 's/^var[[:space:]]\+serverVersion[[:space:]]*=\s*"\s*\(.*\)\s*"/\1/p' "$f" | head -n1 | tr -d ' ' | sed 's/^server-//'
  fi
}

VERSION="${1:-}"
if [[ -z "$VERSION" ]]; then
  VERSION=$(version_from_file || true)
fi
if [[ -z "$VERSION" ]]; then
  echo "ERROR: VERSION not provided and cannot be derived" >&2
  exit 1
fi

TAG="v$VERSION"
RELEASE_DIR=".release/$TAG"
mkdir -p "$RELEASE_DIR"

echo "==> Version: $VERSION (tag: $TAG)"

# 1) Pack frontend-dist.zip (use existing dist if present; else try to build)
if [[ -d vite-frontend/dist ]]; then
  (cd vite-frontend && zip -qr "../$RELEASE_DIR/frontend-dist.zip" dist)
else
  echo "==> vite-frontend/dist not found; trying to build"
  if command -v npm >/dev/null 2>&1; then
    (cd vite-frontend && npm ci --quiet && npm run build)
    (cd vite-frontend && zip -qr "../$RELEASE_DIR/frontend-dist.zip" dist)
  else
    echo "WARN: npm not available; skipping frontend-dist.zip" >&2
  fi
fi

# 2) Always include install.sh as asset
if [[ -f install.sh ]]; then
  cp install.sh "$RELEASE_DIR/install.sh"
  chmod +x "$RELEASE_DIR/install.sh"
else
  echo "ERROR: install.sh not found at repo root" >&2
  exit 1
fi

# 3) Include easytier templates (optional)
if [[ -d easytier ]]; then
  mkdir -p "$RELEASE_DIR/easytier"
  cp -a easytier/default.conf easytier/install.sh "$RELEASE_DIR/easytier/" 2>/dev/null || true
fi

# 4) Include prebuilt agents/servers if present
if [[ -d public/flux-agent ]]; then
  mkdir -p "$RELEASE_DIR/flux-agent"
  cp -a public/flux-agent/* "$RELEASE_DIR/flux-agent/" || true
fi
if [[ -d public/server ]]; then
  mkdir -p "$RELEASE_DIR/server"
  cp -a public/server/* "$RELEASE_DIR/server/" || true
fi

echo "==> Collected assets:"
find "$RELEASE_DIR" -maxdepth 2 -type f -print | sed 's/^/    /'

# 5) Create or update GitHub release
if ! command -v gh >/dev/null 2>&1; then
  echo "ERROR: gh CLI not found. Please install GitHub CLI (https://cli.github.com/) and run 'gh auth login'" >&2
  exit 1
fi

set +e
gh release view "$TAG" >/dev/null 2>&1
exists=$?
set -e

ASSETS=( )
while IFS= read -r -d '' f; do ASSETS+=("$f"); done < <(find "$RELEASE_DIR" -type f -print0)

if [[ $exists -ne 0 ]]; then
  echo "==> Creating release $TAG"
  gh release create "$TAG" \
    --title "$TAG" \
    --notes "network-panel $VERSION release" \
    "${ASSETS[@]}"
else
  echo "==> Updating release $TAG (uploading assets)"
  for f in "${ASSETS[@]}"; do
    gh release upload "$TAG" "$f" --clobber
  done
fi

echo "==> Done."

