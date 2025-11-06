# 服务端部署指南（面板）

本文介绍两种部署方式：

- 二进制一键脚本部署（systemd，Linux）
- Docker Compose 部署

在开始前，请准备：
- 一台 Linux 服务器（建议 Ubuntu 20.04+/Debian 11+/CentOS 8+）
- 已开放面板端口（默认 6365 提供 API；前端静态资源可由反代提供 HTTPS）
- MySQL 数据库（或在 Docker Compose 中随容器启动）

---
## 方式一：二进制一键脚本部署（Linux）

脚本位置：`scripts/install_server.sh`

步骤：
1）下载并执行安装脚本（root 权限）：

```bash
curl -fsSL https://raw.githubusercontent.com/NiuStar/flux-panel/refs/heads/main/scripts/install_server.sh -o install_server.sh \
  && sudo bash install_server.sh
```

2）按提示选择：
- 是否使用下载代理前缀（可为空）
- CPU 架构（默认自动识别）
- 选择从 GitHub Releases 下载预编译，或本地源码编译（需要已安装 Go）

3）服务与配置：
- systemd 服务名：`flux-panel`
- 可执行文件：`/usr/local/bin/flux-panel-server`
- 工作目录：`/opt/flux-panel`
- 环境配置：`/etc/default/flux-panel`

环境变量说明：
```
PORT=6365               # 面板后端监听端口
DB_HOST=127.0.0.1
DB_PORT=3306
DB_NAME=flux_panel
DB_USER=flux
DB_PASSWORD=123456
AGENT_VERSION=go-agent-1.0.7  # (可选) 期望的 Agent 版本，用于触发自升级
```

4）常用命令：
```bash
sudo systemctl status flux-panel
sudo systemctl restart flux-panel
sudo journalctl -u flux-panel -f
```

> 首次启动会自动创建数据库（如权限允许）与管理员账号（admin_user/admin_user），请尽快登录修改密码。

自升级说明：
- 后端通过环境变量 `AGENT_VERSION` 指定期望的 Agent 版本；若为空则使用后端内置默认值。
- Agent 连接后端 WS 时会携带自身版本；若与期望版本不一致，后端会下发 `UpgradeAgent` 指令。
- Agent 将从后端下载 `/flux-agent/flux-agent-linux-<arch>` 二进制（镜像/发布包已内置常见架构），替换本地 `/etc/gost/flux-agent` 并尝试重启自身（systemd/service/或进程内 Exec 替换）。

---
## 方式二：Docker Compose 部署

仓库内提供 `docker-compose-v4.yml`，可与 `panel_install.sh` 搭配使用或手动部署。

1）准备环境与变量
- 确保 Docker 与 Docker Compose 可用
- 准备 `.env` 文件（样例变量参考 `panel_install.sh` 交互生成），至少包括：
  - 面板访问域名/端口
  - 数据库相关变量（DB_HOST/DB_NAME/DB_USER/DB_PASSWORD）

2）启动服务
```bash
docker compose -f docker-compose-v4.yml up -d
```

3）反向代理（可选）
- 使用仓库内 `proxy.sh` 可快速配置 Caddy/Nginx 反代，或自行配置 HTTPS 证书与反代至后端端口（默认 6365）

4）升级/重启
```bash
docker compose -f docker-compose-v4.yml pull
docker compose -f docker-compose-v4.yml up -d
```

如需控制 Agent 自升级目标版本，在 docker-compose 配置或容器运行参数中加入：
```
-e AGENT_VERSION=go-agent-1.0.7
```

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
