# flux-panel 转发面板（哆啦A梦转发面板）

## 更新记录
- 2025-11-05 迁移页新增“真·进度条”，实时显示表级进度
- 2025-11-05 增加探针功能，现在可以看网络延迟和掉线情况了

基于 go-gost/gost 与 go-gost/x 的轻量转发/隧道面板，专注“节点—隧道—转发”全流程编排、诊断与可视化。

---
## 功能概览

- 转发模式
    - 端口转发：入口监听 → 直连目标（forward）
    - 隧道转发：入口监听 → gRPC 隧道（relay/auth）→ 出口 → 真实目标
- 链路编排
    - service 顶层 `addr` 监听
    - 入口 handler：`http + chain`
    - 出口 handler：`relay + auth (+ chain)`
    - 顶层 chains：hop/node，支持 `dialer.grpc` 与 `connector.relay(auth)`
- 安全与可靠
    - 出入口认证使用稳定随机（用户名 u-ForwardID；密码 MD5 前 16 位）
    - IPv6 统一 `[addr]:port`
    - 仅管理 `metadata.managedBy=flux-panel` 服务，不删除外部服务
- 诊断与可观测
    - 节点在线/系统信息、链路诊断（ping/tcp/iperf3）
    - 节点服务状态查询（已部署/监听/端口）
    - 数据迁移实时进度：支持后台任务 current/total 与逐表统计
    - Agent 自升级：Agent 连接后端时自动校验版本，若与后端期望版本不一致则触发在线升级（无需人工登录节点）

---
## 文档目录

- 服务端部署（面板）：[
  docs/SERVER_DEPLOY.md
  ](docs/SERVER_DEPLOY.md) — 二进制一键脚本与 Docker Compose 两种方式
- 节点部署（Agent/节点）：[
  docs/NODE_DEPLOY.md
  ](docs/NODE_DEPLOY.md)
- 部署总览（可选）：[
  docs/DEPLOYMENT.md
  ](docs/DEPLOYMENT.md)
- API 文档：[
  docs/API.md
  ](docs/API.md)
- 典型链路与配置：见下文“隧道转发配置（JSON）”

---
## 数据迁移（带进度）

- 入口：面板后台 “数据迁移” 页面。
- 行为：提交旧库连接后后台异步迁移，前端每秒轮询 `/api/v1/migrate/status?jobId=...`，
  以真实 current/total 渲染进度条，并显示每张表的插入/源计数。
- 失败：展示错误原因并停止轮询；成功：进度 100% 并提示完成。

注意：若以单文件后端二进制部署而没有前端静态资源，UI 将不可用。
建议使用 Docker 镜像部署，或使用 `scripts/install_server.sh` 从源码构建（服务器有 Node/npm 时将自动构建前端并安装到 `public/`）。

---
## Agent 自升级

- 行为：Agent 建立 WS 连接 `/system-info` 时会携带自身版本（如 `go-agent-1.0.7`）。后端读取环境变量 `AGENT_VERSION`（为空则用内置默认）作为期望版本；若不一致则通过 WS 下发 `UpgradeAgent` 指令。
- 升级流程：Agent 根据自身 CPU 架构从后端下载对应二进制 `/flux-agent/flux-agent-linux-<arch>`（容器内置 amd64/arm64/armv7），替换本地 `/etc/gost/flux-agent` 并 `systemctl restart flux-agent`。
- 配置：
  - Docker：在运行容器时设置环境变量 `AGENT_VERSION` 即可控制目标版本。
  - Systemd：使用 `scripts/install_server.sh` 安装时，`/etc/default/flux-panel` 中包含 `AGENT_VERSION` 项，可手动填写目标版本。
  - 产物：Docker 镜像内置 `flux-agent-linux-amd64/arm64/armv7`，发布脚本 `scripts/build_flux_agent_all.sh` 会生成更多平台二进制以供手动替换到 `golang-backend/public/flux-agent/`。

---
## 隧道转发配置（JSON 参考）

出口（server）：listener.grpc + handler.relay(auth) + chain（转发到真实目标）
```json
{
  "name": "<serviceName>",
  "addr": ":<outPort>",
  "listener": {"type": "grpc"},
  "handler": {
    "type": "relay",
    "auth": {"username": "u-<forwardID>", "password": "<stable-16char>"},
    "chain": "chain_<serviceName>"
  },
  "metadata": {"managedBy": "flux-panel"}
}
```

出口 chains（真实目标）：
```json
{
  "name": "chain_<serviceName>",
  "hops": [
    {"name": "hop_<serviceName>", "nodes": [
      {"name": "target", "addr": "<remote1>"},
      {"name": "target", "addr": "<remote2>"}
    ]}
  ]
}
```

入口（client）：listener.tcp + handler.http(chain) + chain（dialer.grpc + connector.relay(auth)）
```json
{
  "name": "<serviceName>",
  "addr": ":<inPort>",
  "listener": {"type": "tcp"},
  "handler": {"type": "http", "chain": "chain_<serviceName>"},
  "metadata": {"managedBy": "flux-panel"}
}
```

入口 chains（到出口隧道）：
```json
{
  "name": "chain_<serviceName>",
  "hops": [
    {"name": "hop_<serviceName>", "nodes": [
      {
        "name": "node_<serviceName>",
        "addr": "[<outIP>]:<outPort>",
        "connector": {"type": "relay", "auth": {"username": "u-<forwardID>", "password": "<stable-16char>"}},
        "dialer": {"type": "grpc"}
      }
    ]}
  ]
}
```

> 说明：`<stable-16char>` = `MD5("<forwardID>:<createdTime>")[:16]`。service 内的 `_chains` 仅作传输载体，Agent 落盘时会上移为顶层 `chains`。

---
## FAQ

- chains 出现在 service 里？
    - 请更新 Agent 至 ≥ go-agent-1.0.1；Agent 会将 `_chains` 上移合并到顶层 `chains`
- 入口 chain 引用缺失？
    - 未携带 `_chains` 时 Agent 会从 `forwarder.nodes[0].addr` 兜底合成最小链
- 配置路径
    - 固定 `/etc/gost/gost.json`

---
## 免责声明

本项目仅供学习与研究使用，请在合法、合规前提下使用。作者不对使用本项目造成的任何直接或间接损失负责。

## 感谢
- 来源：https://github.com/bqlpfy/flux-panel
