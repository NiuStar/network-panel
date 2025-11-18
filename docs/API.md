# API 文档（v1）

统一前缀：`/api/v1`

鉴权：
- 绝大部分接口需要登录（请求头 `Authorization: <token>`）
- 管理接口需要管理员角色

约定：
- 响应统一 `{ code, msg, data }`，`code=0` 表示成功

---
## 认证 Auth

POST `/user/login`
- body: `{ username, password, captchaId, captchaData }`
- resp: `{ code:0, data: { token, role_id, name } }`

POST `/user/updatePassword`
- body: `{ newUsername, currentPassword, newPassword, confirmPassword }`

---
## 节点 Node

POST `/node/create`
POST `/node/list`
POST `/node/update`
POST `/node/delete`
- body: `{ id, uninstall? }` `uninstall=true` 时会通过 WebSocket 向该节点下发 `UninstallAgent` 指令，触发 Agent 自我卸载（停止并移除 `flux-agent/flux-agent2` 服务及二进制）。

POST `/node/install`  获取节点安装命令
GET  `/node/connections` 当前连接概览（管理员）

POST `/node/set-exit` 创建/更新出口 SS 服务（可选）
- body: `{ nodeId, port, password, method?, observer?, limiter?, rlimiter?, metadata? }`

POST `/node/query-services` 查询节点服务（由 Agent 返回 gost.json 汇总）
- body: `{ nodeId, filter? }`
- resp: `data = [ { name, addr, handler, port, listening, limiter, rlimiter, metadata } ]`

POST `/agent/report-services` Agent 上报本地 GOST 服务清单（每 5s）
- body: `{ secret, services: [name...], hashes: { [name]: md5Subset }, timeMs? }`

POST `/forward/status` 获取转发配置状态汇总（支持过滤）
- body: `{ forwardIds?: number[], userId?: number }`
- resp: `{ list: [ { forwardId, ok } ] }`
- 说明：`ok=true` 表示该转发涉及到的节点在最近一次上报中都存在对应服务，且服务配置（子集：name/addr/listener.type/handler.type/forwarder.addrs/metadata.interface）哈希一致。

---
## 隧道 Tunnel

POST `/tunnel/create`
POST `/tunnel/list`
POST `/tunnel/update`
POST `/tunnel/delete`

诊断：
POST `/tunnel/diagnose`
POST `/tunnel/diagnose-step`
- step: `entryExit | exitPublic | iperf3`

---
## 用户隧道权限 User-Tunnel

POST `/tunnel/user/assign`
POST `/tunnel/user/list`
POST `/tunnel/user/remove`
POST `/tunnel/user/update`
POST `/tunnel/user/tunnel`（当前用户可用隧道）

---
## 转发 Forward

POST `/forward/create`
- body: `{ name, tunnelId, inPort?, remoteAddr, interfaceName?, strategy?, ssPort?, ssPassword?, ssMethod? }`
  - 端口转发：仅入口 forward
  - 隧道转发：入口 http+chain（dialer.grpc+connector.relay(auth)），出口 relay+chain（目标 remote）

POST `/forward/list`
POST `/forward/update`
- body 同 create，可选择更新 ss* 字段

POST `/forward/delete`
POST `/forward/force-delete`
POST `/forward/pause`
POST `/forward/resume`
POST `/forward/diagnose`
POST `/forward/diagnose-step`（`entryExit | nodeRemote | iperf3`）
POST `/forward/update-order`

---
## 限速 Speed-Limit

POST `/speed-limit/create`
POST `/speed-limit/list`
POST `/speed-limit/update`
POST `/speed-limit/delete`

---
## 配置 Config

POST `/config/list`
POST `/config/get`
POST `/config/update`
POST `/config/update-single`

---
## 验证码 Captcha（默认简化）

POST `/captcha/check`
POST `/captcha/generate`
POST `/captcha/verify`

---
## Agent 内部接口（面板 ↔ Agent）

POST `/agent/desired-services` 按节点 secret 返回期望服务（agent 拉取）
POST `/agent/push-services`    推送服务（AddService）
POST `/agent/reconcile`        简单对齐（仅新增）
POST `/agent/remove-services`  删除服务（仅 managedBy=network-panel）
POST `/agent/reconcile-node`   管理员手动触发对齐

Agent WebSocket：`/system-info`（type=1 节点、type=0 管理端）
- 命令：Diagnose、AddService、UpdateService、DeleteService、PauseService、ResumeService、QueryServices、UninstallAgent
- 结果：DiagnoseResult、QueryServicesResult
