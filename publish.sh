#!/usr/bin/env bash
set -euo pipefail

# Run on the Docker build host from a clean, exact Git checkout.
ROOT_DIR="$(cd "$(dirname "$0")" && pwd)"
cd "$ROOT_DIR"
VERSION=$(sed -n 's/^var serverVersion = "\([^"]*\)"/\1/p' golang-backend/internal/app/version/version.go)
IMAGE_REPO="${IMAGE_REPO:-24802117/network-panel}"
DOCKER_TARGET="${DOCKER_TARGET:-final}"
PLATFORMS="${PLATFORMS:-linux/amd64,linux/arm64}"
REVISION=$(git rev-parse HEAD)
SHORT_SHA=$(git rev-parse --short=12 HEAD)
if [[ -z "$VERSION" || -n "$(git status --porcelain --untracked-files=no)" ]]; then
  echo "Release requires a version and clean tracked source" >&2
  exit 1
fi
if [[ "$DOCKER_TARGET" == final-local && ! -f vite-frontend-v2/dist/index.html ]]; then
  echo "final-local requires a built vite-frontend-v2/dist/index.html" >&2
  exit 1
fi

docker buildx build --platform "$PLATFORMS" --target "$DOCKER_TARGET" \
  --label "org.opencontainers.image.version=$VERSION" \
  --label "org.opencontainers.image.revision=$REVISION" \
  --label "org.opencontainers.image.source=https://github.com/NiuStar/network-panel" \
  -t "$IMAGE_REPO:$VERSION" -t "$IMAGE_REPO:$SHORT_SHA" --push .
for tag in "$VERSION" "$SHORT_SHA"; do
  docker buildx imagetools inspect "$IMAGE_REPO:$tag"
done
# Promote only after the immutable image's runtime/content checks have passed.
if [[ "${PROMOTE_LATEST:-0}" == 1 ]]; then
  docker buildx imagetools create -t "$IMAGE_REPO:latest" "$IMAGE_REPO:$VERSION"
  docker buildx imagetools inspect "$IMAGE_REPO:latest"
fi
