# Multi-stage build: frontend + backend into a single minimal image

# --- Frontend (two options) ---
# Option A: use prebuilt assets from local workspace (vite-frontend-v2/dist)
FROM scratch AS fe_local
COPY vite-frontend-v2/dist /fe/dist

# Option B: build inside Docker
FROM --platform=$BUILDPLATFORM node:22-alpine AS fe_build
WORKDIR /fe
COPY vite-frontend-v2/package*.json ./
RUN npm install --legacy-peer-deps --no-audit --no-fund
COPY vite-frontend-v2/ .
RUN npm run build

# --- Go source/vendor base ---
# Build on the native builder platform, then cross-compile pure-Go binaries for
# TARGETPLATFORM. This avoids slow qemu builds when publishing multi-arch images.
FROM --platform=$BUILDPLATFORM golang:1.25-bookworm AS deps
WORKDIR /app
# 关键：禁用 CGO、禁用远程 toolchain、使用 vendor，避免外网与编译链依赖
ENV CGO_ENABLED=0 \
    GOTOOLCHAIN=local

# 复制所有源码（包含 vendor）
COPY . ./

# Sync vendor metadata before using -mod=vendor. The repository vendor tree can
# lag behind go.mod in release archives, which otherwise makes Docker builds
# fail with "inconsistent vendoring" before compilation starts.
RUN go mod vendor

# --- Backend build for the requested image platform ---
FROM deps AS app_build
ARG TARGETOS=linux
ARG TARGETARCH=amd64
RUN CGO_ENABLED=0 GOOS=${TARGETOS:-linux} GOARCH=${TARGETARCH:-amd64} \
    go build -trimpath -mod=vendor -buildvcs=false -ldflags "-w -s -buildid=" \
    -o /app/server ./golang-backend/cmd/server
RUN CGO_ENABLED=0 GOOS=${TARGETOS:-linux} GOARCH=${TARGETARCH:-amd64} \
    go build -trimpath -mod=vendor -buildvcs=false -ldflags "-w -s -buildid=" \
    -o /app/launcher ./golang-backend/cmd/launcher

# --- Agent download binaries ---
# These artifacts are platform downloads served by the web app; build them once
# on BUILDPLATFORM instead of repeating them inside every TARGETPLATFORM stage.
FROM deps AS agent_build
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -mod=vendor -buildvcs=false -ldflags "-w -s -buildid=" -o /app/flux-agent-linux-amd64   ./golang-backend/cmd/flux-agent && \
    CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -trimpath -mod=vendor -buildvcs=false -ldflags "-w -s -buildid=" -o /app/flux-agent-linux-arm64   ./golang-backend/cmd/flux-agent && \
    CGO_ENABLED=0 GOOS=linux GOARCH=arm   GOARM=7 go build -trimpath -mod=vendor -buildvcs=false -ldflags "-w -s -buildid=" -o /app/flux-agent-linux-armv7   ./golang-backend/cmd/flux-agent && \
    CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -mod=vendor -buildvcs=false -ldflags "-w -s -buildid=" -o /app/flux-agent2-linux-amd64 ./golang-backend/cmd/flux-agent && \
    CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -trimpath -mod=vendor -buildvcs=false -ldflags "-w -s -buildid=" -o /app/flux-agent2-linux-arm64 ./golang-backend/cmd/flux-agent && \
    CGO_ENABLED=0 GOOS=linux GOARCH=arm   GOARM=7 go build -trimpath -mod=vendor -buildvcs=false -ldflags "-w -s -buildid=" -o /app/flux-agent2-linux-armv7 ./golang-backend/cmd/flux-agent

# --- Final runtime ---
FROM debian:12-slim AS final
WORKDIR /app
ENV PORT=6365 \
    ANYTLS_CERT_DIR=/app/data/anytls
COPY --from=app_build /etc/ssl/certs/ /etc/ssl/certs/

COPY --from=app_build /app/server /app/server
COPY --from=app_build /app/launcher /app/launcher
COPY --from=fe_build /fe/dist /app/public

# 发布多架构 agent
COPY --from=agent_build /app/flux-agent-linux-amd64   /app/public/flux-agent/flux-agent-linux-amd64
COPY --from=agent_build /app/flux-agent-linux-arm64   /app/public/flux-agent/flux-agent-linux-arm64
COPY --from=agent_build /app/flux-agent-linux-armv7   /app/public/flux-agent/flux-agent-linux-armv7
COPY --from=agent_build /app/flux-agent2-linux-amd64  /app/public/flux-agent/flux-agent2-linux-amd64
COPY --from=agent_build /app/flux-agent2-linux-arm64  /app/public/flux-agent/flux-agent2-linux-arm64
COPY --from=agent_build /app/flux-agent2-linux-armv7  /app/public/flux-agent/flux-agent2-linux-armv7

# serve install.sh from the backend container
COPY install.sh /app/install.sh
# ship easytier assets for download by agents
COPY easytier/ /app/easytier/

EXPOSE 6365
CMD ["/app/launcher"]

# --- Final runtime (use local prebuilt frontend) ---
FROM debian:12-slim AS final-local
WORKDIR /app
ENV PORT=6365 \
    ANYTLS_CERT_DIR=/app/data/anytls
COPY --from=app_build /etc/ssl/certs/ /etc/ssl/certs/

COPY --from=app_build /app/server /app/server
COPY --from=app_build /app/launcher /app/launcher
COPY --from=fe_local /fe/dist /app/public

# 发布多架构 agent
COPY --from=agent_build /app/flux-agent-linux-amd64   /app/public/flux-agent/flux-agent-linux-amd64
COPY --from=agent_build /app/flux-agent-linux-arm64   /app/public/flux-agent/flux-agent-linux-arm64
COPY --from=agent_build /app/flux-agent-linux-armv7   /app/public/flux-agent/flux-agent-linux-armv7
COPY --from=agent_build /app/flux-agent2-linux-amd64  /app/public/flux-agent/flux-agent2-linux-amd64
COPY --from=agent_build /app/flux-agent2-linux-arm64  /app/public/flux-agent/flux-agent2-linux-arm64
COPY --from=agent_build /app/flux-agent2-linux-armv7  /app/public/flux-agent/flux-agent2-linux-armv7

COPY install.sh /app/install.sh
# ship easytier assets for download by agents
COPY easytier/ /app/easytier/

EXPOSE 6365
CMD ["/app/launcher"]
