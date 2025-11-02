
# flux-panel 转发面板（哆啦A梦转发面板）

基于 go-gost/gost 与 go-gost/x 的轻量级转发/隧道管理面板，开箱即用，面向「节点-隧道-转发」的全链路配置、诊断与可视化。

本 README 面向一线使用者，覆盖安装部署、概念说明、典型场景配置（端口转发、gRPC 隧道）、常见问题与排障。建议先快速过一遍再实际操作。

---
## 功能特性

- 转发类型
  - 端口转发：入站监听 → 直连目标（forward）
  - 隧道转发：入站监听 → 通过 gRPC 隧道中转到出口，再由出口转发到真实目标
- 灵活链路编排
  - service 顶层 `addr` 监听
  - 入口 service.handler 支持 `http` + `chain` 引用；出口 service.handler 支持 `relay` + `auth`
  - 顶层 `chains` 描述 hop/node，支持 `dialer.grpc` 与 `connector.relay(auth)`
- 安全与兼容
  - 出口/入口认证使用稳定随机的用户名/密码（兼容重放与更新）
  - IPv6 统一使用 `[addr]:port` 格式
  - 仅管理由面板创建的服务（标记 `metadata.managedBy=flux-panel`），不会删除/覆盖外部手工配置
- 可观测 & 诊断
  - 在线状态、基础系统信息、链路诊断（ping/tcp/iperf3）
  - 节点侧服务查询（已部署/端口/监听状态）

---
## 快速开始

### 1) 部署面板（Docker）

```bash
curl -L https://raw.githubusercontent.com/bqlpfy/flux-panel/refs/heads/main/panel_install.sh -o panel_install.sh \
  && chmod +x panel_install.sh \
  && ./panel_install.sh
```

安装脚本包含：
- 面板后端 + 前端（内置 Nginx/Caddy 反代可选）
- 数据库初始化

默认管理员账号：
- 账号：admin_user
- 密码：admin_user

⚠️ 首次登录后请立即修改默认密码。

### 2) 新增节点并安装 agent

在面板「节点」页创建节点，配置：
- 服务器 IP（用于 agent 连接与展示）
- 入口监听端口范围（portSta~portEnd）

创建后，在节点卡片点击「安装」获取安装命令，到服务器执行即可。安装脚本会：
- 安装 gost，并创建 systemd 服务（工作路径：/etc/gost）
- 安装并启用诊断 Agent（go 版本）
- 写入 `/etc/gost/config.json`（面板连接参数）

Agent 与 gost 配置文件：
- gost 配置路径固定：`/etc/gost/gost.json`
- 面板与 Agent 交互：WebSocket `/system-info`（type=1）

### 3) 新增隧道

在「隧道」页创建：
- 选择入口节点（必选）
- 隧道类型：端口转发 或 隧道转发
- 其他选项：入口/出口网卡名、流量计费方向、倍率等

### 4) 新增转发

在「转发」页创建：
- 选择上述隧道
- 入口端口（留空自动分配）
- 目标地址（单个或逗号分隔多个）
- 策略（fifo/round/rand/hash）

保存后系统自动下发配置到对应节点：
- 端口转发：仅入口节点创建 `forward` 服务
- 隧道转发：同时下发入口+出口两端服务与链路（见下）

---
## 配置说明（落盘到 /etc/gost/gost.json）

### 端口转发（Type=1）

- 入口节点（示例）：
```json
{
  "name": "<serviceName>",
  "addr": ":<inPort>",
  "listener": {"type": "tcp"},
  "handler": {"type": "forward"},
  "forwarder": {"nodes": [{"name":"target","addr":"<remote>"}]},
  "metadata": {"managedBy":"flux-panel"}
}
```

### 隧道转发（Type=2，gRPC + relay + http）

- 出口节点（server）：
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

- 出口节点（server）顶层 chains：
```json
{
  "name": "chain_<serviceName>",
  "hops": [
    {
      "name": "hop_<serviceName>",
      "nodes": [
        {"name": "target", "addr": "<remote1>"},
        {"name": "target", "addr": "<remote2>"}
      ]
    }
  ]
}
```

- 入口节点（client）service：
```json
{
  "name": "<serviceName>",
  "addr": ":<inPort>",
  "listener": {"type": "tcp"},
  "handler": {"type": "http", "chain": "chain_<serviceName>"},
  "metadata": {"managedBy": "flux-panel"}
}
```

- 入口节点（client）顶层 chains：
```json
{
  "name": "chain_<serviceName>",
  "hops": [
    {
      "name": "hop_<serviceName>",
      "nodes": [
        {
          "name": "node_<serviceName>",
          "addr": "[<outIP>]:<outPort>",
          "connector": {
            "type": "relay",
            "auth": {"username": "u-<forwardID>", "password": "<stable-16char>"}
          },
          "dialer": {"type": "grpc"}
        }
      ]
    }
  ]
}
```

说明：
- `<stable-16char>` 为 `MD5("<forwardID>:<createdTime>")` 的前 16 位，稳定且可复用；用户名为 `u-<forwardID>`
- service 中的 `_chains` 只是传输载体；最终都会被 Agent 合并到顶层 `chains` 中
- IPv6 地址统一 `[ip]:port` 形式

---
## 使用建议与诊断

- 节点页支持“一键刷新服务状态”（查询已部署/监听端口/监听状态）
- 转发页支持分步诊断：入口⇄出口、节点⇄远端、iperf3 反向带宽（仅隧道模式）
- reconcile 策略：默认不删除；即便开启严格对齐，也只删除 `managedBy=flux-panel` 的冗余，不动外部手工服务

---
## 常见问题（FAQ）

- 链定义不在顶层？
  - 确认节点 Agent 版本 ≥ go-agent-1.0.1。Agent 会把 service.payload 中的 `_chains` 上移为顶层 `chains`
- 入口 service.handler.chain 指向的 chain 不存在？
  - 若 payload 未带 `_chains`，Agent 会尝试从 `forwarder.nodes[0].addr` 兜底合成一个最小 chain，保证引用不失配
- unknown_msg 日志
  - 现已支持双层 JSON 解析与自动裁剪；如仍报错，请对照面板 `ws_send` 的 payload 与 agent `unknown_msg.error` 并反馈
- 配置路径
  - 固定：`/etc/gost/gost.json`

---
## 免责声明

本项目仅供学习与研究使用，请务必在合法、合规前提下使用。作者不对使用本项目造成的任何直接或间接损失负责。

---
## ⭐ 喝杯咖啡！

| 网络       | 地址                                                                 |
|------------|----------------------------------------------------------------------|
| BNB(BEP20) | `0x755492c03728851bbf855daa28a1e089f9aca4d1`                          |
| TRC20      | `TYh2L3xxXpuJhAcBWnt3yiiADiCSJLgUm7`                                  |
| Aptos      | `0xf2f9fb14749457748506a8281628d556e8540d1eb586d202cd8b02b99d369ef8`  |

[![Star History Chart](https://api.star-history.com/svg?repos=bqlpfy/flux-panel&type=Date)](https://www.star-history.com/#bqlpfy/flux-panel&Date)
