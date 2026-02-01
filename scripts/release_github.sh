#!/usr/bin/env bash
set -euo pipefail

# Release helper: builds agent + server binaries, creates a git tag, pushes it,
# and optionally creates a GitHub Release (if gh CLI is available).

ROOT_DIR="$(cd "$(dirname "$0")/.." && pwd)"
AGENT_BUILD="$ROOT_DIR/scripts/build_flux_agent_all.sh"
SERVER_BUILD="$ROOT_DIR/scripts/build_server_all.sh"
ASSETS_DIR_AGENT="$ROOT_DIR/golang-backend/public/flux-agent"
ASSETS_DIR_SERVER="$ROOT_DIR/golang-backend/public/server"
# Frontend
FRONTEND_DIR="$ROOT_DIR/vite-frontend-v2"
ASSETS_DIR_FRONTEND="$ROOT_DIR/golang-backend/public/frontend"
FRONTEND_ZIP="$ASSETS_DIR_FRONTEND/frontend-dist.zip"

usage() {
  cat <<EOF
Usage: $(basename "$0") [-v <tag>] [--force] [--no-build] [--no-release]

Options:
  -v <tag>       Tag name to use (e.g., v1.2.3). Defaults to vYYYYMMDD-HHMMSS.
  --force        Skip clean working tree check.
  --no-build     Do not run build scripts (reuse existing artifacts).
  --no-release   Do not create a GitHub Release even if gh is available.

Behavior:
  - Runs frontend (vite) build and zips dist to frontend-dist.zip.
  - Runs agent and server build scripts.
  - Creates an annotated tag and pushes it to origin.
  - If gh CLI is installed and not disabled, creates a GitHub release and uploads artifacts.
EOF
}

TAG=""
FORCE=0
DO_BUILD=1
DO_RELEASE=1

while [[ $# -gt 0 ]]; do
  case "$1" in
    -v)
      TAG="$2"; shift 2 ;;
    --force)
      FORCE=1; shift ;;
    --no-build)
      DO_BUILD=0; shift ;;
    --no-release)
      DO_RELEASE=0; shift ;;
    -h|--help)
      usage; exit 0 ;;
    *)
      echo "Unknown argument: $1" >&2; usage; exit 1 ;;
  esac
done

if [[ -z "$TAG" ]]; then
  TAG="v$(date +%Y%m%d-%H%M%S)"
fi

# Ensure git repo present
git -C "$ROOT_DIR" rev-parse --git-dir >/dev/null 2>&1 || { echo "Not a git repo: $ROOT_DIR" >&2; exit 1; }

# Ensure clean tree unless forced
if [[ "$FORCE" -eq 0 ]]; then
  if [[ -n "$(git -C "$ROOT_DIR" status --porcelain)" ]]; then
    echo "Working tree not clean. Commit/stash or use --force." >&2
    exit 1
  fi
fi

if [[ "$DO_BUILD" -eq 1 ]]; then
  echo "==> Building frontend (vite-frontend-v2)"
  (
    set -e
    cd "$FRONTEND_DIR"
    # Follow requested flow: npm install && npm run build
    npm install --legacy-peer-deps --no-audit --no-fund
    npm run build
  )
  echo "==> Packaging frontend dist -> $FRONTEND_ZIP"
  mkdir -p "$ASSETS_DIR_FRONTEND"
  rm -f "$FRONTEND_ZIP"
  if command -v zip >/dev/null 2>&1; then
    (
      cd "$FRONTEND_DIR/dist"
      # Zip the contents (not the dist folder itself)
      zip -qr "$FRONTEND_ZIP" .
    )
  else
    echo "zip command not found; please install zip or package dist manually." >&2
    exit 1
  fi
  echo "==> Building flux-agent for all targets"
  bash "$AGENT_BUILD"
  echo "==> Building server for all targets"
  bash "$SERVER_BUILD"
fi

echo "==> Ensuring git tag $TAG exists"

# 先确保本地有 tag
if git -C "$ROOT_DIR" rev-parse "refs/tags/$TAG" >/dev/null 2>&1; then
  echo "Tag $TAG already exists locally, skip creating."
else
  git -C "$ROOT_DIR" tag -a "$TAG" -m "Release $TAG"
fi

# 再确保远端 origin 上也有
if git -C "$ROOT_DIR" ls-remote --tags origin "$TAG" | grep -q "$TAG"; then
  echo "Tag $TAG already exists on origin, skip pushing."
else
  git -C "$ROOT_DIR" push origin "$TAG"
fi

if [[ "$DO_RELEASE" -eq 0 ]]; then
  echo "Skipping GitHub release as requested (--no-release)."
  exit 0
fi

echo "==> Preparing GitHub release $TAG"
DESC="Automated release $TAG"

# Collect assets
assets=()
if [[ -d "$ASSETS_DIR_AGENT" ]]; then
  while IFS= read -r -d '' f; do assets+=("$f"); done < <(find "$ASSETS_DIR_AGENT" -type f -print0)
fi
if [[ -d "$ASSETS_DIR_SERVER" ]]; then
  while IFS= read -r -d '' f; do assets+=("$f"); done < <(find "$ASSETS_DIR_SERVER" -type f -print0)
fi
if [[ -d "$ASSETS_DIR_FRONTEND" ]]; then
  while IFS= read -r -d '' f; do assets+=("$f"); done < <(find "$ASSETS_DIR_FRONTEND" -type f -print0)
fi

# Determine owner/repo from origin for both gh and REST paths
origin_url=$(git -C "$ROOT_DIR" config --get remote.origin.url || true)
owner=""; repo=""
if [[ "$origin_url" =~ github.com ]]; then
  origin_url=${origin_url%.git}
  if [[ "$origin_url" =~ ^git@github.com:(.*)$ ]]; then
    path="${BASH_REMATCH[1]}"
  else
    path="${origin_url#*github.com/}"
  fi
  owner="${path%%/*}"; repo="${path#*/}"
fi

# Try gh CLI first (explicit repo to avoid git context issues)
if command -v gh >/dev/null 2>&1 && [[ -n "$owner" && -n "$repo" ]]; then
  if gh release view -R "$owner/$repo" "$TAG" >/dev/null 2>&1; then
    echo "Release $TAG already exists; uploading assets (if any)."
    if [[ ${#assets[@]} -gt 0 ]]; then
      gh release upload -R "$owner/$repo" "$TAG" "${assets[@]}" --clobber
    fi
  else
    if [[ ${#assets[@]} -gt 0 ]]; then
      gh release create -R "$owner/$repo" "$TAG" "${assets[@]}" -t "Flux Panel $TAG" -n "$DESC"
    else
      gh release create -R "$owner/$repo" "$TAG" -t "Flux Panel $TAG" -n "$DESC"
    fi
  fi
  echo "✅ Release $TAG completed via gh CLI."
  exit 0
fi

# Fallback to GitHub REST API if gh is not available
GHTOKEN="${GITHUB_TOKEN:-${GH_TOKEN:-}}"
if [[ -z "$GHTOKEN" ]]; then
  echo "No gh CLI and no GITHUB_TOKEN/GH_TOKEN provided; tag pushed but cannot create Release automatically." >&2
  exit 0
fi

if [[ -z "$owner" || -z "$repo" ]]; then
  echo "Remote origin does not appear to be GitHub ($origin_url)." >&2
  exit 0
fi

api="https://api.github.com/repos/${owner}/${repo}"
hdr=( -H "Authorization: token $GHTOKEN" -H "Accept: application/vnd.github+json" )

# Check if release exists
status=$(curl -sS -o /tmp/release.json -w "%{http_code}" "${api}/releases/tags/${TAG}" "${hdr[@]}" || true)
if [[ "$status" == "200" ]]; then
  rel_id=$(jq -r .id /tmp/release.json)
else
  # create release
  payload=$(jq -n --arg tag "$TAG" --arg name "Flux Panel $TAG" --arg body "$DESC" '{tag_name:$tag, name:$name, body:$body, draft:false, prerelease:false}')
  curl -sS "${api}/releases" "${hdr[@]}" -d "$payload" > /tmp/release.json
  rel_id=$(jq -r .id /tmp/release.json)
  if [[ "$rel_id" == "null" || -z "$rel_id" ]]; then
    echo "Failed to create release: $(cat /tmp/release.json)" >&2
    exit 1
  fi
fi

upload_asset() {
  local file="$1"
  local name
  name="$(basename "$file")"

  local max_tries=3
  local attempt=1

  while (( attempt <= max_tries )); do
    echo "Uploading asset: $name (attempt ${attempt}/${max_tries})"

    # 放在 if 条件里，失败时不会触发 set -e 直接退出
    if curl -sS -X POST \
      -H "Authorization: token $GHTOKEN" \
      -H "Content-Type: application/octet-stream" \
      --data-binary @"$file" \
      "https://uploads.github.com/repos/${owner}/${repo}/releases/${rel_id}/assets?name=${name}" >/dev/null
    then
      echo "✓ Uploaded: $name"
      return 0
    fi

    echo "⚠️  Upload failed for $name, retrying..." >&2
    attempt=$((attempt + 1))
    sleep 3
  done

  echo "❌ Failed to upload $name after ${max_tries} tries, skip." >&2
  return 1
}


if [[ ${#assets[@]} -gt 0 ]]; then
  for f in "${assets[@]}"; do
    upload_asset "$f" || continue
  done
fi

echo "✅ Release $TAG completed via REST API."

# After GitHub release, also upload static bundle to the external static host
UPLOAD_SCRIPT="$ROOT_DIR/scripts/upload_static_to_server.sh"
if [[ -x "$UPLOAD_SCRIPT" ]]; then
  echo "==> Uploading static assets to external static host"
  bash "$UPLOAD_SCRIPT" || echo "⚠️  Static host upload failed (continuing)"
else
  echo "(skip) $UPLOAD_SCRIPT not executable"
fi
