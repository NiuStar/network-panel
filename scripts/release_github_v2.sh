#!/usr/bin/env bash
set -euo pipefail

# Release helper: builds binaries, creates a git tag, pushes it,
# and creates/publishes a GitHub Release (if possible).

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
Usage: $(basename "$0") [-v <tag>] [--force] [--no-build] [--no-release] [--upload-only]

Options:
  -v <tag>       Tag name to use (e.g., v1.2.3). Defaults to vYYYYMMDD-HHMMSS.
  --force        Skip clean working tree check.
  --no-build     Do not run build scripts (reuse existing artifacts).
  --no-release   Do not create a GitHub Release even if gh is available.
  --upload-only  Only upload assets to an existing release (skip build/tag/push).

Behavior:
  - Builds frontend (vite) and zips to frontend-dist.zip.
  - Builds flux-agent and server for all targets.
  - Creates an annotated git tag and pushes it to origin.
  - If GitHub CLI is installed (and not disabled), creates a draft GitHub Release, uploads assets, then publishes it.
  - Otherwise, uses GitHub API to create a draft Release, upload assets, then publish it.
EOF
}

TAG=""
FORCE=0
DO_BUILD=1
DO_RELEASE=1
DO_TAG=1
UPLOAD_ONLY=0

# Parse arguments
while [[ $# -gt 0 ]]; do
  case "$1" in
    -v) TAG="$2"; shift 2 ;;
    --force) FORCE=1; shift ;;
    --no-build) DO_BUILD=0; shift ;;
    --no-release) DO_RELEASE=0; shift ;;
    --upload-only) DO_BUILD=0; DO_TAG=0; DO_RELEASE=1; UPLOAD_ONLY=1; shift ;;
    -h|--help) usage; exit 0 ;;
    *) echo "Unknown argument: $1" >&2; usage; exit 1 ;;
  esac
done

# Default tag if not provided
if [[ -z "$TAG" ]]; then
  TAG="v$(date +%Y%m%d-%H%M%S)"
fi

# Ensure we're in a git repo
git -C "$ROOT_DIR" rev-parse --git-dir >/dev/null 2>&1 || {
  echo "Not a git repository: $ROOT_DIR" >&2
  exit 1
}

# Ensure working tree is clean unless --force
if [[ "$FORCE" -eq 0 && "$UPLOAD_ONLY" -eq 0 ]]; then
  if [[ -n "$(git -C "$ROOT_DIR" status --porcelain)" ]]; then
    echo "Working tree not clean. Commit/stash changes or use --force." >&2
    exit 1
  fi
fi

# Build artifacts (unless skipped)
if [[ "$DO_BUILD" -eq 1 ]]; then
  echo "==> Building frontend (vite-frontend-v2)"
  (
    set -e
    cd "$FRONTEND_DIR"
    npm install --legacy-peer-deps --no-audit --no-fund
    npm run build
  )
  echo "==> Packaging frontend dist -> $FRONTEND_ZIP"
  mkdir -p "$ASSETS_DIR_FRONTEND"
  rm -f "$FRONTEND_ZIP"
  if command -v zip >/dev/null 2>&1; then
    ( cd "$FRONTEND_DIR/dist" && zip -qr "$FRONTEND_ZIP" . )
  else
    echo "zip command not found; please install zip or package dist manually." >&2
    exit 1
  fi

  echo "==> Building flux-agent for all targets"
  bash "$AGENT_BUILD"
  echo "==> Building server for all targets"
  bash "$SERVER_BUILD"
fi

if [[ "$DO_TAG" -eq 1 ]]; then
  echo "==> Ensuring git tag $TAG exists"
  # Create local tag if not present
  if git -C "$ROOT_DIR" rev-parse "refs/tags/$TAG" >/dev/null 2>&1; then
    echo "Tag $TAG already exists locally, skipping tag creation."
  else
    git -C "$ROOT_DIR" tag -a "$TAG" -m "Release $TAG"
  fi
  # Push tag to origin if not already there
  if git -C "$ROOT_DIR" ls-remote --tags origin "$TAG" | grep -q "$TAG"; then
    echo "Tag $TAG already exists on origin, not pushing."
  else
    git -C "$ROOT_DIR" push origin "$TAG"
  fi
else
  echo "==> Skipping tag creation/push (--upload-only)"
fi

if [[ "$DO_RELEASE" -eq 0 ]]; then
  echo "Skipping GitHub release creation (--no-release specified)."
  exit 0
fi

echo "==> Preparing GitHub release $TAG"
DESC="Automated release $TAG"

# Collect release asset file paths
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

# Determine GitHub repo (owner/repo) from git origin URL
origin_url=$(git -C "$ROOT_DIR" config --get remote.origin.url || true)
owner=""; repo=""
if [[ "$origin_url" =~ github.com[:/]+([^/]+)/([^/]+) ]]; then
  owner="${BASH_REMATCH[1]}"
  repo="${BASH_REMATCH[2]%.git}"
fi

# If GitHub CLI is available, prefer it for creating/uploading release
if command -v gh >/dev/null 2>&1 && [[ -n "$owner" && -n "$repo" ]]; then
  release_exists=0
  is_draft="false"
  if gh release view -R "$owner/$repo" "$TAG" >/dev/null 2>&1; then
    release_exists=1
    # Check draft status of existing release (JSON output)
    is_draft=$(gh release view -R "$owner/$repo" "$TAG" --json isDraft -q '.isDraft' || echo "false")
    echo "Release $TAG already exists (draft=$is_draft); uploading assets (if any)."
    if [[ ${#assets[@]} -gt 0 ]]; then
      gh release upload -R "$owner/$repo" "$TAG" "${assets[@]}" --clobber
    fi
  else
    if [[ "$UPLOAD_ONLY" -eq 1 ]]; then
      echo "Release $TAG does not exist; --upload-only requires an existing release." >&2
      exit 1
    fi
    echo "Release $TAG does not exist; creating draft release."
    if [[ ${#assets[@]} -gt 0 ]]; then
      # Create release as draft and attach assets
      gh release create -R "$owner/$repo" "$TAG" "${assets[@]}" -d -t "Flux Panel $TAG" -n "$DESC"
    else
      gh release create -R "$owner/$repo" "$TAG" -d -t "Flux Panel $TAG" -n "$DESC"
    fi
    release_exists=1
    is_draft="true"
    echo "Draft release $TAG created."
  fi

  # If the release is in draft state, publish it
  if [[ "$is_draft" == "true" ]]; then
    echo "Publishing release $TAG (was draft)..."
    gh release edit -R "$owner/$repo" "$TAG" --draft=false
  fi

  echo "✅ Release $TAG completed via gh CLI."
  exit 0
fi

# Fallback to REST API if gh CLI is not available
GHTOKEN="${GITHUB_TOKEN:-${GH_TOKEN:-}}"
if [[ -z "$GHTOKEN" ]]; then
  echo "No gh CLI and no GITHUB_TOKEN provided; tag pushed but cannot create Release." >&2
  exit 0
fi
if [[ -z "$owner" || -z "$repo" ]]; then
  echo "Remote origin is not a GitHub repository (origin_url=$origin_url)." >&2
  exit 1
fi

api="https://api.github.com/repos/${owner}/${repo}"
hdr=(-H "Authorization: token $GHTOKEN" -H "Accept: application/vnd.github+json")

# Check if release already exists (by tag)
release_json="/tmp/release.json"
status=$(curl -sS -o "$release_json" -w "%{http_code}" "${api}/releases/tags/${TAG}" "${hdr[@]}" || true)
if [[ "$status" == "200" ]]; then
  # Release exists; get its ID and draft status
  rel_id=$(jq -r .id "$release_json")
  is_draft=$(jq -r .draft "$release_json")
  echo "Release $TAG already exists (draft=$is_draft); using existing release ID=$rel_id."
else
  if [[ "$UPLOAD_ONLY" -eq 1 ]]; then
    echo "Release $TAG does not exist; --upload-only requires an existing release." >&2
    exit 1
  fi
  echo "Release $TAG not found; creating a new draft release."
  payload=$(jq -n --arg tag "$TAG" --arg name "Flux Panel $TAG" --arg body "$DESC" \
               '{tag_name:$tag, name:$name, body:$body, draft:true, prerelease:false}')
  curl -sS -o "$release_json" "${api}/releases" "${hdr[@]}" -d "$payload"
  rel_id=$(jq -r .id "$release_json")
  if [[ "$rel_id" == "null" || -z "$rel_id" ]]; then
    echo "Failed to create release: $(cat "$release_json")" >&2
    exit 1
  fi
  is_draft=$(jq -r .draft "$release_json")
  echo "Draft release $TAG created with ID=$rel_id."
fi

# Function to upload a single asset file to the release
upload_asset() {
  local file="$1"
  local name; name="$(basename "$file")"
  local max_tries=3
  local attempt=1
  while (( attempt <= max_tries )); do
    echo "Uploading asset: $name (attempt ${attempt}/${max_tries})"
    if curl -sS -X POST \
           -H "Authorization: token $GHTOKEN" \
           -H "Content-Type: application/octet-stream" \
           --data-binary @"$file" \
           "${api}/releases/${rel_id}/assets?name=${name}" >/dev/null; then
      echo "✓ Uploaded: $name"
      return 0
    fi
    echo "⚠️  Upload failed for $name (attempt $attempt)." >&2
    attempt=$((attempt + 1))
    sleep 3
  done
  echo "❌ Giving up on $name after ${max_tries} failed attempts." >&2
  return 1
}

# Upload all assets (if any), continuing on failure to attempt all
if [[ ${#assets[@]} -gt 0 ]]; then
  echo "==> Uploading release assets..."
  for f in "${assets[@]}"; do
    upload_asset "$f" || true  # keep going even if one fails
  done
fi

# Publish the release if it is still a draft
if [[ "$is_draft" == "true" ]]; then
  echo "Publishing release $TAG (currently draft)..."
  # Patch the release to set draft=false (publish it)
  publish_payload=$(jq -n '{draft:false}')
  pub_status=$(curl -sS -o /tmp/release_publish.json -w "%{http_code}" \
                -X PATCH "${api}/releases/${rel_id}" "${hdr[@]}" -d "$publish_payload" || true)
  if [[ "$pub_status" != "200" ]]; then
    echo "⚠️ Failed to publish release (status $pub_status): $(cat /tmp/release_publish.json)" >&2
    # Not exiting here, as assets upload completed; the release remains draft for manual handling or next run
  else
    echo "Release $TAG is now published."
  fi
fi

echo "✅ Release $TAG completed via REST API."
