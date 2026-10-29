#!/usr/bin/env bash
set -euo pipefail

# Build from the published tag's tracked bytes, never local ignored dist/vendor.
ROOT_DIR="$(cd "$(dirname "$0")" && pwd)"
cd "$ROOT_DIR"
VERSION=$(sed -n 's/^var serverVersion = "\([^"]*\)"/\1/p' golang-backend/internal/app/version/version.go)
IMAGE_REPO="${IMAGE_REPO:-24802117/network-panel}"
PLATFORMS="${PLATFORMS:-linux/amd64,linux/arm64}"
REVISION=$(git rev-parse HEAD)
SHORT_SHA=$(git rev-parse --short=12 HEAD)
TAG="v$VERSION"
if [[ -z "$VERSION" || -n "$(git status --porcelain --untracked-files=no)" ]]; then
  echo "Release requires a version and clean tracked source" >&2
  exit 1
fi
if [[ "$(git rev-parse "$TAG^{commit}")" != "$REVISION" ]] ||
   ! git show "$REVISION:golang-backend/cmd/flux-agent/main.go" | grep -Fx "var versionBase = \"$VERSION\"" >/dev/null; then
  echo "Release tag, backend and agent versions must match HEAD" >&2
  exit 1
fi
REMOTE_TAG=$(git ls-remote origin "refs/tags/$TAG" "refs/tags/$TAG^{}")
if ! printf '%s\n' "$REMOTE_TAG" | grep -Eq "^$REVISION[[:space:]]"; then
  echo "Release tag must already exist on origin at HEAD" >&2
  exit 1
fi

# Docker receives only Git archive bytes, so ignored/untracked outputs cannot leak in.
git archive "$REVISION" | docker buildx build --platform "$PLATFORMS" --target final \
  --label "org.opencontainers.image.version=$VERSION" \
  --label "org.opencontainers.image.revision=$REVISION" \
  --label "org.opencontainers.image.source=https://github.com/NiuStar/network-panel" \
  -t "$IMAGE_REPO:$VERSION" -t "$IMAGE_REPO:$SHORT_SHA" --push -
for tag in "$VERSION" "$SHORT_SHA"; do
  docker buildx imagetools inspect "$IMAGE_REPO:$tag"
done
# Promote only after the immutable image's runtime/content checks have passed.
if [[ "${PROMOTE_LATEST:-0}" == 1 ]]; then
  docker buildx imagetools create -t "$IMAGE_REPO:latest" "$IMAGE_REPO:$VERSION"
  docker buildx imagetools inspect "$IMAGE_REPO:latest"
fi
