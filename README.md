# network-panel 组网面板（叮当猫组网面板）

## 更新记录
- 2025-11-09 增加了隧道转发每个节点出入口的选择，用以支撑下一个版本的功能
             修复了一些小 bug（端口越界等问题）
             新增测试功能：组网
- 2025-11-08 增加了转发链隧道功能，支持多跳隧道与诊断检测
- 2025-11-07 增加探针分享功能，地址为：```http://ip:port/app/share/network```
- 2025-11-06 迁移页新增“真·进度条”，实时显示表级进度  \
             增加了SQLLite支持，需设置DB_DIALECT环境变量为sqlite，默认为mysql  \
             Agent 自升级功能上线
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
    - 仅管理 `metadata.managedBy=network-panel` 服务，不删除外部服务
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
## 从哆啦A梦面板迁移

支持两种方式将“哆啦A梦面板”的数据迁移到本面板：

- 方式一（指向原库）：复用哆啦A梦的 MySQL 数据库，直接把本面板的数据库配置改为哆啦A梦的数据库连接信息。
- 方式二（数据拷贝）：使用面板内置“数据迁移”功能，填写哆啦A梦数据库信息，自动迁移数据到当前面板所用数据库（SQLite 或 MySQL）。

— 方式一：复用哆啦A梦数据库（直接改配置）
- 适合想零拷贝、快速切换到新面板的场景。
- 前置：已掌握哆啦A梦库的连接参数（Host/Port/User/Password/Database）。
- 步骤：
  1) 确认使用 MySQL 模式：确保未设置或清空 `DB_DIALECT`（为空即用 MySQL，默认也是 MySQL）。
  2) 设置以下变量为哆啦A梦数据库：`DB_HOST`、`DB_PORT`、`DB_NAME`、`DB_USER`、`DB_PASSWORD`。
  3) 重启面板服务。

  示例（Docker Compose）
  - 修改 `docker-compose-v4_mysql.yml:30` 或 `network-panel/docker-compose.yaml:30` 中的环境变量：
    - `DB_HOST=你的MySQL地址`
    - `DB_PORT=3306`
    - `DB_NAME=哆啦A梦的数据库名`
    - `DB_USER=用户名`
    - `DB_PASSWORD=密码`
  - 启动/重启：`docker compose -f docker-compose-v4_mysql.yml up -d`

  示例（本机/二进制/systemd）
  - 环境文件：`/etc/default/network-panel` 或项目根目录 `.env`
  - 写入（示例）：
    - `DB_HOST=127.0.0.1`
    - `DB_PORT=3306`
    - `DB_NAME=flux_panel`
    - `DB_USER=flux`
    - `DB_PASSWORD=123456`
    - 确保未设置或清空 `DB_DIALECT`
  - 重启：`systemctl restart network-panel`

  验证
  - 登录新面板，应直接看到原有用户/节点/隧道/转发等数据。
  - 若账号权限受限，请为该 MySQL 账号授予相应表的读写权限。

— 方式二：使用面板内置迁移（自动拷贝）
- 适合想将数据迁移到全新库（如 SQLite 或新建 MySQL）的场景。
- 思路：新面板按照你的目标数据库运行；进入“数据迁移”页面，填入哆啦A梦库信息，系统会逐表拷贝并显示实时进度。
- 步骤：
  1) 选择目标库并启动面板：
     - SQLite：设置 `DB_DIALECT=sqlite`，可选 `DB_SQLITE_PATH`；重启生效。
     - MySQL：清空 `DB_DIALECT`，设置 `DB_HOST/DB_PORT/DB_NAME/DB_USER/DB_PASSWORD`；重启生效。
  2) 打开后台“数据迁移”页面。
  3) 填入哆啦A梦库的 Host、Port(默认3306)、User、Password、Database。
  4) 点“测试连接”，确认源库各表记录数。
  5) 点“开始迁移”：前端显示总进度与逐表插入/源计数；完成后提示“迁移完成”，失败则展示错误原因。
  6) 迁移后建议重新安装/升级 Agent，使节点配置在各节点落盘并生效。

  说明
  - 已适配的核心表：`user`、`node`、`tunnel`、`forward`、`user_tunnel`、`speed_limit`、`vite_config`、`statistics_flow`。
  - 大数据量请耐心等待；中断后可通过状态接口再次查看进度。

---
## 数据迁移（带进度）

- 入口：面板后台 “数据迁移” 页面。
- 行为：提交旧库连接后后台异步迁移，前端每秒轮询 `/api/v1/migrate/status?jobId=...`，
  以真实 current/total 渲染进度条，并显示每张表的插入/源计数。
- 失败：展示错误原因并停止轮询；成功：进度 100% 并提示完成。

注意：若以单文件后端二进制部署而没有前端静态资源，UI 将不可用。
推荐一键安装脚本（默认 SQLite）：

```
curl -fsSL https://raw.githubusercontent.com/NiuStar/network-panel/refs/heads/main/panel_install.sh -o panel_install.sh \
  && bash panel_install.sh
```

说明：
- 选择“二进制安装”时默认使用 SQLite（可自定义 `DB_SQLITE_PATH`）
- 选择“Docker Compose 安装”时使用 MySQL（会创建 `network-panel/` 并启动 compose）
- 如需手动安装，可使用 `scripts/install_server.sh`（服务器有 Node/npm 时将自动构建前端并安装到 `public/`）

---
## Agent 自升级

- 行为：Agent 建立 WS 连接 `/system-info` 时会携带自身版本（如 `go-agent-1.0.1`）。后端的期望 Agent 版本与后端版本完全一致（不再支持自定义环境变量覆盖）。若不一致会下发 `UpgradeAgent` 指令触发在线升级。
- 升级流程：Agent 根据自身 CPU 架构从后端下载对应二进制 `/flux-agent/flux-agent-linux-<arch>`（镜像/发布包内置 amd64/arm64/armv7），替换本地 `/etc/gost/flux-agent` 并 `systemctl restart flux-agent`。
- 产物：Docker 镜像内置 `flux-agent-linux-amd64/arm64/armv7`；如需更多平台，可使用 `scripts/build_flux_agent_all.sh` 生成并放入 `golang-backend/public/flux-agent/`。

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
  "metadata": {"managedBy": "network-panel"}
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
  "metadata": {"managedBy": "network-panel"}
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
- 面板前端访问：```http://ip:port/app/```

---
## 免责声明

本项目仅供学习与研究使用，请在合法、合规前提下使用。作者不对使用本项目造成的任何直接或间接损失负责。

## 感谢
- 来源：https://github.com/bqlpfy/flux-panel
