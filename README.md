# flux-panel 转发面板（哆啦A梦转发面板）

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

---
## 文档目录

- 部署指南：[
  docs/DEPLOYMENT.md
](docs/DEPLOYMENT.md)
- API 文档：[
  docs/API.md
](docs/API.md)
- 典型链路与配置：见下文“隧道转发配置（JSON）”

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
