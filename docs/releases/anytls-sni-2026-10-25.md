# AnyTLS 公网 IP SNI 修复发布（2026-10-25）

## 不可变身份

- 已验业务源码：[`4248fff4e94c69ff4fbab1959449332ba7985057`](https://github.com/NiuStar/network-panel/commit/4248fff4e94c69ff4fbab1959449332ba7985057)。后继发布说明提交不改变该镜像构建身份。
- 平台：`linux/amd64`。
- 镜像：

```text
24802117/network-panel:sni-4248fff4e94c@sha256:ff2819c100f87692c4660d29db771b6a399d16d5346aa0f6329976a9931dd92e
```

- 平台 manifest：`sha256:cfab4aa3f8ecad55707f58b75c4687744a252fa86e5e80b69e2d22f5d246e386`。
- `/app/server` SHA256：`0c3cfe2295b8e3d1aab061e452b1594a20be203388d7e4e7cb19542048555460`。
- 继承基镜像 `pin-9c3ca06eb3cb@sha256:0f142f83a8e90a2a9697a1b2827d0573d9f423ada3029d717676e8fbc20d36c2`；全 rootfs 只有 `/app/server` 改变，其余4616 entries一致；launcher、前端和六份 agent 程序未替换。下载 agent 没有更新，已经运行的 agent 也不会因镜像发布自动改变。
- registry tag/index/platform/config 已独立 GET、重算 SHA 并对构建产物读回；未更新 `latest`。

## 修复范围

对**托管节点、当前进程已确认安装的有效严格整叶证书 pin、最终实际连接地址为保守认定的公网字面 IP、无用户显式 SNI**，订阅不再仅因为证书签发域名而自动填入该域名 SNI。probe 先完成 hairpin 地址替换，再判断最终连接端点。

DNS、私网、保留、回环、未知地址继续原名称默认值；外部节点的显式 `sni`／`server_name`、原始密码、整叶 pin 和证书签发身份保留。缺失、畸形、未确认或 scope 排除的 pin 仍失败关闭，不以 CA-only 或跳验替代。公网 IP 分类不进行 DNS 解析或可达性探测。

客户端配套：[anytls-core v2026-10-25-01](https://github.com/NiuStar/anytls-core/releases/tag/v2026-10-25-01) 提供显式 `disable-sni: true`。它只省略 ClientHello SNI，保留严格身份认证；既有配置不会因发行自动迁移。整叶 pin 是叶 DER SHA-256，不是 SPKI 或 ClientHello 指纹；pin-only 不承诺 PKIX 链、主机名或有效期验证。

## 验证

- 独立精确提交复审关闭 `SNI-SCOPE-001`；旧候选 `1c80fc07d7fb` 因范围扩大未发布，其镜像仅保留为审计材料。
- 同字节真实订阅/probe回归：旧候选20个反向 scope 子例失败，新候选4顶层／40子例通过；不将编译错误算业务 RED。
- Mac Studio Go1.27.3 darwin/arm64：后端115顶层／155子测试通过，14无测试包单列，vet通过；CGO=0 Linux/amd64 交叉构建为精确提交且 `vcs.modified=false`。
- 最终镜像使用独立 internal network、全新 SQLite、真实 seed 登录：149项 HTTP／安全／收尾检查通过。69份前端资产与6份 agent HTTP响应摘要匹配；9项创建、9项更新拒绝均完整比较持久行不变；严格 pin 正控、外部显式 SNI、SS无pin正控和不支持leaf-pin的sing-box导出拒绝通过。
- 原有14容器身份和完整配置合同保持；新增2个未启动 verifier 与1个已停止 QA容器保留，无新增运行容器。构建收尾曾因Docker Mounts对象顺序不稳定误报；原FAIL保留，逐对象排列重构及只读核验关闭该工具问题，没有重建镜像或缩减保护集合。
- 既有 GitHub workflow 不匹配 `golang-backend/**`，本次不声称有面板 GitHub CI 绿灯；验收来自上述冻结源码的真实原生回归、独立复审与最终镜像 QA。

## 尚未完成／不包含

固定版本第三方客户端的 native parser、实际 ClientHello 及正确／错误 pin 握手未测。尤其 QX 的空 `tls-host=`、Shadowrocket rules 的空 `sni=`尚无原生兼容 PASS；生成字符串与 HTTP QA 不能替代该门。源实现审查通过不等于所有客户端均可用。

本次仅发布源码及不可变镜像，**未切换生产面板、未替换节点 agent、未重启客户端 WebUI／助手／VPN**。生产数据库兼容演练、当前 installed-agent／证书／认证订阅链与长期公网稳定性仍需单独验收。原 SNI 为可重复但非每次必败的触发候选；不声称已识别网络干扰设备或根治全部掉线。部署前保持既有认证、端口和部署方证书 scope／自动升级控制，不因本说明自动执行生产更新。
