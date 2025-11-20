# 服务端部署指南（面板）

> 更新提示：脚本与静态资源已迁移至静态镜像域名，建议优先使用以下命令：

一键脚本（推荐）
```bash
curl -fsSL https://panel-static.199028.xyz/network-panel/panel_install.sh -o panel_install.sh \
  && bash panel_install.sh
```

二进制一键脚本（systemd）
```bash
curl -fsSL https://panel-static.199028.xyz/network-panel/scripts/install_server.sh -o install_server.sh \
  && sudo bash install_server.sh
```

本文提供三种视角，推荐优先使用“一键脚本”：

- 一键脚本安装（推荐，默认 SQLite，可选 MySQL+Compose）
- 二进制一键脚本部署（systemd，Linux）
- Docker Compose 部署

在开始前，请准备：
- 一台 Linux 服务器（建议 Ubuntu 20.04+/Debian 11+/CentOS 8+）
- 已开放面板端口（默认 6365 提供 API；前端静态资源可由反代提供 HTTPS）
- MySQL 数据库（或在 Docker Compose 中随容器启动）

---
## 方式零：一键脚本安装（推荐）

脚本位置：仓库根 `panel_install.sh`

在线一键安装：

```bash
curl -fsSL https://panel-static.199028.xyz/network-panel/panel_install.sh -o panel_install.sh \
  && bash panel_install.sh
```

交互说明：
- 选择 1）二进制安装：默认 SQLite 模式；将向 `/etc/default/network-panel` 写入 `DB_DIALECT=sqlite`，可自定义 `DB_SQLITE_PATH`（默认 `/opt/network-panel/flux.db`）
- 选择 2）Docker Compose 安装：使用 MySQL 模式，会在当前目录创建 `network-panel/`，下载 `docker-compose-v4_mysql.yml` 重命名为 `docker-compose.yaml` 并启动

如需更细粒度控制，可参考下文“二进制部署”或“Docker Compose 部署”。

---
## 方式一：二进制一键脚本部署（Linux）

脚本位置：`scripts/install_server.sh`

步骤：
1）下载并执行安装脚本（root 权限）：

```bash
curl -fsSL https://panel-static.199028.xyz/network-panel/scripts/install_server.sh -o install_server.sh \
  && sudo bash install_server.sh
```

支持install、uninstall、daupte三种命令

2）按提示选择：
- 是否使用下载代理前缀（可为空）
- CPU 架构（默认自动识别）
- 选择从 GitHub Releases 下载预编译，或本地源码编译（需要已安装 Go）

3）服务与配置：
- systemd 服务名：`network-panel`
- 可执行文件：`/usr/local/bin/network-panel-server`
- 工作目录：`/opt/network-panel`
- 前端静态资源：安装脚本会自动从 GitHub Release 下载 `frontend-dist.zip` 并解压至 `/opt/network-panel/public/`；离线环境可在本地 `vite-frontend` 目录执行 `npm install && npm run build`，将 `dist/*` 手动复制到该目录
- 环境文件：二进制安装会创建 `/etc/default/network-panel` 与 `/opt/network-panel/.env`（systemd 同时读取），并默认写入：`DB_DIALECT=sqlite`、`DB_SQLITE_PATH=/opt/network-panel/panel.db`、`JWT_SECRET=flux-panel-secret`
- 环境配置：`/etc/default/network-panel`

环境变量说明：
```
PORT=6365               # 面板后端监听端口
DB_HOST=127.0.0.1
DB_PORT=3306
DB_NAME=flux_panel
DB_USER=flux
DB_PASSWORD=123456
```

4）常用命令：
```bash
sudo systemctl status network-panel
sudo systemctl restart network-panel
sudo journalctl -u network-panel -f
```

> 首次启动会自动创建数据库（如权限允许）与管理员账号（admin_user/admin_user），请尽快登录修改密码。

自升级说明：
- 期望的 Agent 版本与后端版本严格一致（后端升级到 1.0.1，则 Agent 期望版本为 1.0.1），不再支持通过环境变量自定义。
- Agent 连接后端 WS 时会携带自身版本；若与期望版本不一致，后端会下发 `UpgradeAgent` 指令。
- Agent 将从后端下载 `/flux-agent/flux-agent-linux-<arch>` 二进制（镜像/发布包已内置常见架构），替换本地 `/etc/gost/flux-agent` 并尝试重启自身（systemd/service/或进程内 Exec 替换）。

---
## 方式二：Docker Compose 部署

仓库内提供 `docker-compose-v4.yml` 与 `docker-compose-v4_mysql.yml`。

1）准备环境与变量
- 确保 Docker 与 Docker Compose 可用
- 准备 `.env` 文件，至少包括：
  - 面板访问域名/端口
  - 数据库相关变量（DB_HOST/DB_NAME/DB_USER/DB_PASSWORD）

2）启动服务（MySQL 版示例）
```bash
docker compose -f docker-compose-v4_mysql.yml up -d
```

3）反向代理（可选）
- 使用仓库内 `proxy.sh` 可快速配置 Caddy/Nginx 反代，或自行配置 HTTPS 证书与反代至后端端口（默认 6365）

4）升级/重启
```bash
docker compose -f docker-compose-v4.yml pull
docker compose -f docker-compose-v4.yml up -d
```

（提示：旧版中通过 `AGENT_VERSION` 控制目标版本的方式已废弃。）

---
## 端口与安全
- 后端默认监听 6365（可通过 `PORT` 修改）
- 建议将前端静态资源置于反代服务器并启用 HTTPS
- 不要在公开渠道泄露 `.env`、数据库密码、JWT 等敏感信息

---
## Agent 二进制分发与重启回退策略

- 镜像/发布包内置位置：`public/flux-agent/`
  - Docker 镜像已内置：`flux-agent-linux-amd64`、`flux-agent-linux-arm64`、`flux-agent-linux-armv7`
  - 如需更多平台，可使用 `scripts/build_flux_agent_all.sh` 生成并放入该目录（供后端 `/flux-agent/:file` 路由下发）

- 非 systemd 系统的回退重启：
  - Agent 升级后首先尝试 `systemctl restart flux-agent`；失败则尝试 `service flux-agent restart`；若仍失败，Agent 进程将使用 Exec 方式直接以新二进制替换当前进程（保持原参数与环境变量），确保无人工干预也能完成升级生效。
