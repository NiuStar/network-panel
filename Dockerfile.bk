# Multi-stage build: frontend + backend into a single minimal image

# --- Frontend (two options) ---
# Option A: use prebuilt assets from local workspace (vite-frontend/dist)
FROM scratch AS fe_local
COPY vite-frontend/dist /fe/dist

# Option B: build inside Docker (default)
FROM node:22-alpine AS fe_build
WORKDIR /fe
COPY vite-frontend/package*.json ./
RUN npm install --legacy-peer-deps --no-audit --no-fund
COPY vite-frontend/ .
RUN npm run build

# --- Backend build ---
FROM golang:1.25-alpine AS be
WORKDIR /app
# 关键：禁用 CGO、禁用远程 toolchain、使用 vendor，避免外网与编译链依赖
ENV CGO_ENABLED=0 \
    GOTOOLCHAIN=local

# 如果你确实需要 go build 缓存，可以开启 BuildKit 的缓存挂载（可选）
# 复制所有源码（包含 vendor）
COPY . ./

# 不再执行 apk add（避免拉取索引失败）
# 构建 server（纯 Go）
RUN go build -trimpath -mod=vendor -ldflags "-w -s" -o /app/server ./golang-backend/cmd/server

# 多架构构建 flux-agent 与 flux-agent2（纯 Go 跨平台）
RUN GOOS=linux GOARCH=amd64 go build -trimpath -mod=vendor -ldflags "-w -s" -o /app/flux-agent-linux-amd64   ./golang-backend/cmd/flux-agent && \
    GOOS=linux GOARCH=arm64 go build -trimpath -mod=vendor -ldflags "-w -s" -o /app/flux-agent-linux-arm64   ./golang-backend/cmd/flux-agent && \
    GOOS=linux GOARCH=arm   GOARM=7 go build -trimpath -mod=vendor -ldflags "-w -s" -o /app/flux-agent-linux-armv7   ./golang-backend/cmd/flux-agent && \
    GOOS=linux GOARCH=amd64 go build -trimpath -mod=vendor -ldflags "-w -s" -o /app/flux-agent2-linux-amd64 ./golang-backend/cmd/flux-agent && \
    GOOS=linux GOARCH=arm64 go build -trimpath -mod=vendor -ldflags "-w -s" -o /app/flux-agent2-linux-arm64 ./golang-backend/cmd/flux-agent && \
    GOOS=linux GOARCH=arm   GOARM=7 go build -trimpath -mod=vendor -ldflags "-w -s" -o /app/flux-agent2-linux-armv7 ./golang-backend/cmd/flux-agent

# --- Final runtime ---
FROM alpine:3.19 AS final
WORKDIR /app
ENV PORT=6365

# 不在运行期做 apk add，避免同类问题（若应用需要 HTTPS 出站，建议单独准备带 CA 的基础镜像或手动拷贝证书）
COPY --from=be /app/server /app/server
COPY --from=fe_build /fe/dist /app/public

# 发布多架构 agent
RUN mkdir -p /app/public/flux-agent
COPY --from=be /app/flux-agent-linux-amd64   /app/public/flux-agent/flux-agent-linux-amd64
COPY --from=be /app/flux-agent-linux-arm64   /app/public/flux-agent/flux-agent-linux-arm64
COPY --from=be /app/flux-agent-linux-armv7   /app/public/flux-agent/flux-agent-linux-armv7
COPY --from=be /app/flux-agent2-linux-amd64  /app/public/flux-agent/flux-agent2-linux-amd64
COPY --from=be /app/flux-agent2-linux-arm64  /app/public/flux-agent/flux-agent2-linux-arm64
COPY --from=be /app/flux-agent2-linux-armv7  /app/public/flux-agent/flux-agent2-linux-armv7

# serve install.sh from the backend container
COPY install.sh /app/install.sh
# ship easytier assets for download by agents
RUN mkdir -p /app/easytier
COPY easytier/ /app/easytier/

EXPOSE 6365
CMD ["/app/server"]

# --- Final runtime (use local prebuilt frontend) ---
FROM alpine:3.19 AS final-local
WORKDIR /app
ENV PORT=6365

COPY --from=be /app/server /app/server
COPY --from=fe_local /fe/dist /app/public

# 发布多架构 agent
RUN mkdir -p /app/public/flux-agent
COPY --from=be /app/flux-agent-linux-amd64   /app/public/flux-agent/flux-agent-linux-amd64
COPY --from=be /app/flux-agent-linux-arm64   /app/public/flux-agent/flux-agent-linux-arm64
COPY --from=be /app/flux-agent-linux-armv7   /app/public/flux-agent/flux-agent-linux-armv7
COPY --from=be /app/flux-agent2-linux-amd64  /app/public/flux-agent/flux-agent2-linux-amd64
COPY --from=be /app/flux-agent2-linux-arm64  /app/public/flux-agent/flux-agent2-linux-arm64
COPY --from=be /app/flux-agent2-linux-armv7  /app/public/flux-agent/flux-agent2-linux-armv7

COPY install.sh /app/install.sh
# ship easytier assets for download by agents
RUN mkdir -p /app/easytier
COPY easytier/ /app/easytier/

EXPOSE 6365
CMD ["/app/server"]
