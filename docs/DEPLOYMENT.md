# 部署指南（Deployment）

本文档描述面板与节点的部署方式、目录结构与常用命令。

---
## 1. 环境要求

- 一台用于运行面板的服务器（推荐 Linux，支持 Docker）
- 节点服务器若干（Linux），支持 systemd
- 开放面板 HTTP/HTTPS 端口与节点到面板的 WebSocket 端口

---
## 2. 面板部署（Docker 一键安装）

```bash
curl -L https://raw.githubusercontent.com/bqlpfy/flux-panel/refs/heads/main/panel_install.sh -o panel_install.sh \
  && chmod +x panel_install.sh \
  && ./panel_install.sh
```

安装过程包含：
- 后端（Go）与前端（Vite）打包
- 反向代理（可选 Caddy）
- 数据库初始化

配置文件与环境变量：
- 面板使用 `.env`（如果使用 compose/反代脚本）存放站点域名、端口等参数；建议使用占位值或在部署主机本地维护，不要将实际 IP/域名/密码提交到仓库。

默认管理员账号：
- 账号：admin_user
- 密码：admin_user

> 强烈建议首次登录后立即修改默认密码！

---
## 3. 节点安装 & Agent 启动

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

> 安全提示：请勿在公开渠道粘贴/分享包含实际 IP、密码、JWT 的 `.env` 或配置内容。排障时建议脱敏（使用 `example.com`、`<password>` 等占位）。

服务管理（systemd）：
```bash
systemctl status gost
systemctl restart gost

systemctl status flux-agent
systemctl restart flux-agent
```

---
## 4. 典型网络拓扑

- 端口转发：
  - 客户端 → 入口节点（forward）→ 目标主机
- 隧道转发（gRPC + relay + http）：
  - 客户端 → 入口节点（http + chain）→ gRPC 隧道（relay/auth）→ 出口节点（relay + chain）→ 目标主机

---
## 5. 安全与维护

- 面板仅管理带 `metadata.managedBy=flux-panel` 的服务
- Agent reconcile 默认不删除任何服务；如需严格对齐，可显式开启严格模式，但删除范围仍仅限 `managedBy=flux-panel` 的冗余项
- IPv6 地址统一 `[ip]:port` 形式以避免解析问题

---
## 6. 常见问题

- 入口 service.handler.chain 指向的 chain 不存在？
  - Agent 写入时会将 service.payload 携带的 `_chains` 上移到顶层 `chains`；如果确实缺失，Agent 会根据 `forwarder.nodes[0].addr` 兜底合成最小链
- Agent 日志 unknown_msg？
  - 现已支持双层 JSON 与自动裁剪解析；请对照后端 `ws_send` 与 Agent 的 `unknown_msg.error`，排查中间网关是否重写了帧
