# AnyTLS 强制证书指纹：候选合同

## 范围与原始输入

本候选基于 network-panel 45da7376e68c441783b2ae2f6b65541796a7fe9d 加本机已存在的未提交改动；原字节清单及差分在 evidence/inherited-source-pins.json、original-dirty.patch。原工作树不改写，不包含 mysql_data、生产数据或凭据。新修改只在 /workspace/network-panel-pin-candidate 开发，不代表远端已经更新或线上已经部署。

## 信任合同

- 本次“证书指纹”指实际 TLS 叶证书 DER 的 SHA-256（64位hex），与 anytls-go URI 的 cert-sha256 一致，不是 PEM 文本摘要、CA摘要或 Chrome/uTLS client-fingerprint。
- 外部 AnyTLS 节点创建、编辑、运行配置分发和订阅输出须使用同一严格校验：缺失、畸形、相互冲突的指纹别名拒绝；任何跳验别名开启、畸形跳验值或可能覆盖信任的原始配置拒绝。节点指纹必须由操作人经可信渠道交付，不能自动从未认证服务器抓取并信任。
- 新建控制器管理节点不能要求提前手填尚未签发的指纹。由已持久化证书生成，并绑定当前 agent 实际加载证书的确认；缺证书、部署未确认、过期确认或故障时拒绝发布，不以其他证书或新随机证书替代。
- 自签名、公网数字 IP、无域名的 anytls-go 叶证书可经 pin-only 身份验证使用，不能把管理端 CA/SNI 强制为所有外部节点的额外前提。pin-only 不自动验证 CA、主机名和有效期，必须准确描述。
- 重签同一公钥会改变整证书指纹；不能将节点 SPKI SHA256/Base64 或订阅bootstrap整叶DER SHA256/hex当作可互换值。

## 客户端格式与拒绝

按各客户端真正支持的字段输出。没有可验证指纹能力时，不能用 CA-only 或不可识别字段冒充指纹；明确跳过/拒绝该 AnyTLS 格式并提示原因，保持组/规则无悬空引用。raw URI/Surge/Clash/Sing-box overrides 不能绕过强制指纹。非 AnyTLS 协议不因本项需求被要求提供伪造指纹。

固定 sing-box v1.12.17 的 option.OutboundTLSOptions、NewSTDClient 已从精确公开源码核对：仅CA certificate/path，没有叶或SPKI指纹字段。当前范围不全局升级SDK；其AnyTLS配置导出必须失败关闭，不能声称写入 CA 等价为强制指纹。

## 验证门

1. 统一校验器和实际创建／编辑 API 的正确 pin 正控与空、错、冲突／跳验／raw 覆盖负控，负例数据库不写入。
2. 管理节点证书签发→部署报告→订阅指纹来源绑定，避免本机证书文件替代真实加载身份。
3. URI、Clash、Surge、Shadowrocket、QX、Sing-box 分别检查；原格式不支持时明确拒绝，不输出空对象或静默非pin节点。
4. 真实 anytls-go 对端证书正确／错误指纹的 TLS 负例零目标业务证据；配置字段存在不是客户端真正执行pin的证明。
5. 新候选 targeted/full/vet、前端校验及适用的构建。测试环境与生产分别报告；不因本次需求擅自切换入口、发布镜像、重启 agent 或迁移生产数据库。
