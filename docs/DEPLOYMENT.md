# 部署指南（Deployment）

> 更新提示：从本版本起，所有安装脚本与静态资源已统一迁移到静态镜像域名，建议使用以下最新命令：

面板一键脚本（推荐）
```bash
curl -fsSL https://panel-static.199028.xyz/network-panel/panel_install.sh -o panel_install.sh \
  && bash panel_install.sh
```

本文档为总览，分别提供服务端部署与节点部署的独立指南：

- 服务端部署（面板）：见 docs/SERVER_DEPLOY.md
- 节点部署：见 docs/NODE_DEPLOY.md

---
## 1. 环境要求

- 一台用于运行面板的服务器（推荐 Linux，支持 Docker）
- 节点服务器若干（Linux），支持 systemd
- 开放面板 HTTP/HTTPS 端口与节点到面板的 WebSocket 端口

---
## 2. 面板部署（一键脚本）

```bash
curl -fsSL https://panel-static.199028.xyz/network-panel/panel_install.sh -o panel_install.sh \
  && bash panel_install.sh
```

安装过程包含：
- 交互选择安装方式：二进制（默认 SQLite）或 Docker Compose（MySQL）
- 二进制：安装 systemd 服务，环境配置位于 `/etc/default/network-panel`，并自动从 Release 下载前端静态资源包到 `/opt/network-panel/public`
- Docker Compose：在 `network-panel/` 下下载并启动 `docker-compose.yaml`

配置与环境变量：
- 二进制：`/etc/default/network-panel`（SQLite：`DB_DIALECT=sqlite`，可选 `DB_SQLITE_PATH`；MySQL：`DB_HOST/DB_PORT/DB_NAME/DB_USER/DB_PASSWORD`）
- Docker Compose：如使用 `docker-compose-v4_mysql.yml`，可直接修改 compose 环境段或 `.env` 文件

默认管理员账号：
- 账号：admin_user
- 密码：admin_user

> 强烈建议首次登录后立即修改默认密码！

---
## 3. 节点安装 & Agent 启动（快速指引）

1）在面板“节点”页添加节点（填写 ServerIP、入口端口范围等）。

2）节点卡片点击“安装”，复制安装命令到节点服务器执行：

安装脚本会：
- 下载并安装 gost（systemd 服务）
- 下载并安装 go 诊断 Agent（systemd 服务）
- 创建配置：
  - `/etc/gost/config.json`（面板地址、节点 secret）
  - `/etc/gost/gost.json`（gost 服务配置文件，Agent 读写）

目录与文件：
- `/etc/gost/config.json`  面板接入配置
- `/etc/gost/gost.json`   gost 主配置（由面板/Agent 写入）

> 安全提示：请勿在公开渠道粘贴/分享包含实际 IP、密码、JWT 的环境文件或配置内容。排障时建议脱敏（使用 `example.com`、`<password>` 等占位）。

服务管理（systemd）：
```bash
systemctl status gost
systemctl restart gost

systemctl status flux-agent
systemctl restart flux-agent

单 Agent 模式（默认开启）：
- 环境变量 `SINGLE_AGENT=1` 时，仅运行 `flux-agent` 常驻；如检测到 `flux-agent2` 服务，将自动停止并禁用。
- 升级时仍会下发/阶段 `flux-agent2` 二进制用于维护，但不会常驻运行。
```

---
## 4. 典型网络拓扑

- 端口转发：
  - 客户端 → 入口节点（forward）→ 目标主机
- 隧道转发（gRPC + relay + http）：
  - 客户端 → 入口节点（http + chain）→ gRPC 隧道（relay/auth）→ 出口节点（relay + chain）→ 目标主机

---
## 5. 安全与维护

- 面板仅管理带 `metadata.managedBy=network-panel` 的服务
- Agent 在每次连接/重连面板后会执行一次对齐：若发现 `desired` 中的服务不在本地 `gost.json`，会自动补齐（仅新增）。
- Agent reconcile 默认不删除任何服务；如需严格对齐，可显式开启严格模式（`STRICT_RECONCILE=1`），但删除范围仍仅限 `managedBy=network-panel` 的冗余项。
- 可通过 `RECONCILE_INTERVAL`（秒）开启周期性对齐（默认 300s，<=0 关闭）。
- Agent 每 `AGENT_SVC_REPORT_SEC` 秒（默认 5s）上报本地服务清单及配置哈希；后端聚合 `/forward/status` 时使用哈希进行严格校验。
- 后端判定“上报是否新鲜”的阈值可配置：`FORWARD_STATUS_STALE_MS`（默认 15000 毫秒）。
- IPv6 地址统一 `[ip]:port` 形式以避免解析问题

---
## 6. 常见问题

- 入口 service.handler.chain 指向的 chain 不存在？
  - Agent 写入时会将 service.payload 携带的 `_chains` 上移到顶层 `chains`；如果确实缺失，Agent 会根据 `forwarder.nodes[0].addr` 兜底合成最小链
- Agent 日志 unknown_msg？
  - 现已支持双层 JSON 与自动裁剪解析；请对照后端 `ws_send` 与 Agent 的 `unknown_msg.error`，排查中间网关是否重写了帧
