# 节点部署指南

> 更新提示：节点安装脚本已迁移至静态镜像域名，请使用以下最新命令：

在线执行（替换参数中的面板地址与节点密钥）：
```bash
curl -fsSL https://panel-static.199028.xyz/network-panel/install.sh -o install.sh \
  && sudo bash install.sh -a <面板地址:端口> -s <节点密钥>
# 若静态源不可用，可用 GitHub 备用源：
# curl -fsSL https://raw.githubusercontent.com/NiuStar/network-panel/refs/heads/main/install.sh -o install.sh && sudo bash install.sh -a <面板地址:端口> -s <节点密钥>
```

节点用于承载入口/出口服务，包含两部分：
- gost 主服务（由 systemd 管理）
- go 诊断 Agent（与面板通信，采集状态、下发/对齐配置）

部署方式推荐使用面板页面的“安装命令”，或手动执行安装脚本。

---
## 方式一：在面板复制“安装命令”

1）在“节点”页创建节点（填写服务器 IP、端口范围等）

2）点击该节点卡片的“安装”，复制弹窗内命令到节点服务器执行（root）

脚本会：
- 下载并安装 gost（systemd 服务 `gost`）
- 下载并安装 go 诊断 Agent（systemd 服务 `flux-agent`）
- 写入配置：
  - `/etc/gost/config.json`（面板地址、节点 secret）
  - `/etc/gost/gost.json`（gost 主配置，Agent/面板管理）

> 单 Agent 模式：从当前版本起，默认仅运行一个常驻 Agent（`flux-agent`）。
> 另一个辅助二进制（`flux-agent2`）仅用于升级/卸载等维护操作，不常驻运行。
> 可通过环境变量 `SINGLE_AGENT=1`（默认已开启）控制。

---
## 方式二：手动执行安装脚本（可参数化）

脚本位置：根目录 `install.sh`

在线执行：
```bash
curl -fsSL https://panel-static.199028.xyz/network-panel/install.sh -o install.sh \
  && sudo bash install.sh -a <面板地址:端口> -s <节点密钥>
# 若静态源不可用，可用 GitHub 备用源：
# curl -fsSL https://raw.githubusercontent.com/NiuStar/network-panel/refs/heads/main/install.sh -o install.sh && sudo bash install.sh -a <面板地址:端口> -s <节点密钥>
```

参数说明：
- `-a`：面板地址（含端口），例如 `panel.example.com:443`
- `-s`：节点密钥（在面板创建节点后生成）
- `-p`：可选，指定下载代理模式（支持内置若干加速前缀）

关键行为：
- 自动从 go-gost/gost Releases 获取最新版本并安装
- 安装 go 诊断 Agent 并写入 systemd 服务
- 配置文件覆盖策略：
  - `/etc/gost/config.json` 每次安装按传入参数重建
  - `/etc/gost/gost.json` 若已存在则保留（首次安装时创建空结构体）

---
## 服务管理与排障

查看/重启服务：
```bash
sudo systemctl status gost
sudo systemctl restart gost

sudo systemctl status flux-agent
sudo systemctl restart flux-agent

# 若看到系统中存在 `flux-agent2` 服务，单 Agent 模式下会自动停止并禁用该服务。
```

实时日志：
```bash
journalctl -u gost -f
journalctl -u flux-agent -f
```

常见问题：
- 无法连接面板：确认 `/etc/gost/config.json` 中 addr/secret 正确；网络出站策略允许到面板端口；面板后端端口对外开放
- 安装失败：再次执行脚本或使用 `-p` 指定代理前缀；确保系统已安装 `curl`、`tar`、`jq`（脚本会尽量自动安装）
