import { useEffect, useMemo, useState } from "react";
import { Button } from "@heroui/button";
import { Card, CardBody, CardHeader } from "@heroui/card";
import { Input, Textarea } from "@heroui/input";
import { Select, SelectItem } from "@heroui/select";
import { toast } from "react-hot-toast";
import QRCode from "qrcode";
import {
  generateAnyTLSCA,
  getAnyTLSCACheck,
  getAnyTLSCAStatus,
  getConfigByName,
  updateConfig,
  type AnyTLSCACheckResult,
  type AnyTLSCAStatus,
} from "@/api";
import { isAdmin } from "@/utils/auth";

const shadowrocketRulesUrl =
  "https://raw.githubusercontent.com/LingJingMaster/Shadowrocket-Rules/refs/heads/main/Shadowrocket.conf";

const buildBaseUrl = () => {
  const raw =
    (import.meta as any).env?.VITE_API_BASE || window.location.origin;

  return raw.replace(/\/+$/, "");
};

const copyText = async (text: string, label: string) => {
  if (!text) {
    toast.error("内容为空");
    return;
  }

  try {
    await navigator.clipboard.writeText(text);
    toast.success(`${label}已复制`);
  } catch {
    const input = document.createElement("input");
    input.value = text;
    document.body.appendChild(input);
    input.select();
    document.execCommand("copy");
    document.body.removeChild(input);
    toast.success(`${label}已复制`);
  }
};

export default function SubscriptionPage() {
  const admin = useMemo(() => isAdmin(), []);
  const token = useMemo(() => localStorage.getItem("token") || "", []);
  const baseUrl = useMemo(buildBaseUrl, []);
  const encodedToken = encodeURIComponent(token);
  const [qrKey, setQrKey] = useState("clash");
  const [qrDataUrl, setQrDataUrl] = useState<string>("");
  const [clashTemplate, setClashTemplate] = useState("");
  const [surgeTemplate, setSurgeTemplate] = useState("");
  const [clashFileName, setClashFileName] = useState("");
  const [surgeFileName, setSurgeFileName] = useState("");
  const [saving, setSaving] = useState(false);
  const [caStatus, setCaStatus] = useState<AnyTLSCAStatus | null>(null);
  const [caCheck, setCaCheck] = useState<AnyTLSCACheckResult | null>(null);
  const [caLoading, setCaLoading] = useState(false);
  const [caGenerating, setCaGenerating] = useState(false);
  const [caChecking, setCaChecking] = useState(false);
  const [anytlsCertEnabled, setAnytlsCertEnabled] = useState(false);
  const templateKeyClash = "subscription_clash_template";
  const templateKeySurge = "subscription_surge_template";

  const links = useMemo(
    () => [
      {
        key: "clash",
        title: "Clash 订阅",
        desc: "使用转发出口协议生成订阅",
        url: `${baseUrl}/api/v1/subscription/clash?token=${encodedToken}`,
      },
      {
        key: "clash-meta",
        title: "Clash Meta 订阅",
        desc: "兼容 Clash Meta 协议与出站参数",
        url: `${baseUrl}/api/v1/subscription/clash-meta?token=${encodedToken}`,
      },
      {
        key: "shadowrocket",
        title: "Shadowrocket 分流配置",
        desc: "含当前账户节点与 LingJingMaster 分流规则；在 Shadowrocket 的「配置」中导入并设为使用中",
        url: `${baseUrl}/api/v1/subscription/shadowrocket?rules=1&token=${encodedToken}`,
      },
      {
        key: "shadowrocket-nodes",
        title: "Shadowrocket 纯节点订阅（兼容旧版）",
        desc: "仅包含节点，不含分流规则",
        url: `${baseUrl}/api/v1/subscription/shadowrocket?token=${encodedToken}`,
      },
      {
        key: "surge5",
        title: "Surge 5 配置",
        desc: "兼容 Surge 5 格式",
        url: `${baseUrl}/api/v1/subscription/surge?ver=5&token=${encodedToken}`,
      },
      {
        key: "surge6",
        title: "Surge 6 配置",
        desc: "兼容 Surge 6 格式",
        url: `${baseUrl}/api/v1/subscription/surge?ver=6&token=${encodedToken}`,
      },
      {
        key: "singbox",
        title: "Sing-box 订阅",
        desc: "生成 sing-box JSON 配置",
        url: `${baseUrl}/api/v1/subscription/singbox?token=${encodedToken}`,
      },
      {
        key: "v2ray",
        title: "V2Ray 订阅",
        desc: "输出 V2RayN 通用订阅格式",
        url: `${baseUrl}/api/v1/subscription/v2ray?token=${encodedToken}`,
      },
      {
        key: "qx",
        title: "Quantumult X 订阅",
        desc: "输出 Quantumult X 节点列表",
        url: `${baseUrl}/api/v1/subscription/qx?token=${encodedToken}`,
      },
    ],
    [baseUrl, encodedToken],
  );

  const qrLink = useMemo(
    () => links.find((item) => item.key === qrKey)?.url || "",
    [links, qrKey],
  );

  useEffect(() => {
    if (!qrLink) {
      setQrDataUrl("");
      return;
    }
    QRCode.toDataURL(qrLink, { width: 200, margin: 1 })
      .then((url) => setQrDataUrl(url))
      .catch(() => setQrDataUrl(""));
  }, [qrLink]);

  useEffect(() => {
    const parseEnabled = (v: unknown) => {
      const s = String(v ?? "")
        .trim()
        .toLowerCase();
      return (
        s === "1" ||
        s === "true" ||
        s === "yes" ||
        s === "on" ||
        s === "enabled"
      );
    };
    (async () => {
      const [clashResp, surgeResp, certEnabledResp] = await Promise.all([
        getConfigByName(templateKeyClash),
        getConfigByName(templateKeySurge),
        getConfigByName("anytls_cert_enabled"),
      ]);
      if (clashResp.code === 0 && typeof clashResp.data === "string") {
        setClashTemplate(clashResp.data || "");
      }
      if (surgeResp.code === 0 && typeof surgeResp.data === "string") {
        setSurgeTemplate(surgeResp.data || "");
      }
      const enabled = certEnabledResp.code === 0 && parseEnabled(certEnabledResp.data);
      setAnytlsCertEnabled(enabled);
      if (admin && enabled) {
        await refreshCAStatus();
      }
    })();
  }, []);

  const fmtTime = (ms?: number) => {
    if (!ms || ms <= 0) return "-";
    return new Date(ms).toLocaleString();
  };

  const refreshCAStatus = async () => {
    setCaLoading(true);
    try {
      const resp = await getAnyTLSCAStatus();
      if (resp.code === 0 && resp.data) {
        setCaStatus(resp.data);
      } else {
        toast.error(resp.msg || "获取 CA 状态失败");
      }
    } finally {
      setCaLoading(false);
    }
  };

  const handleGenerateCA = async () => {
    if (!window.confirm("将重新生成/轮换站点 CA，确认继续吗？")) {
      return;
    }
    setCaGenerating(true);
    try {
      const resp = await generateAnyTLSCA(true);
      if (resp.code === 0 && resp.data) {
        setCaStatus(resp.data);
        toast.success("站点 CA 已生成");
      } else {
        toast.error(resp.msg || "生成失败");
      }
    } finally {
      setCaGenerating(false);
    }
  };

  const handleCheckCA = async () => {
    setCaChecking(true);
    try {
      const resp = await getAnyTLSCACheck();
      if (resp.code === 0 && resp.data) {
        setCaCheck(resp.data);
        if (resp.data.downloadReady) {
          toast.success("CA 内容检查通过，可下载导入");
        } else {
          toast.error(resp.data.error || "CA 内容检查未通过");
        }
      } else {
        toast.error(resp.msg || "CA 自检失败");
      }
    } finally {
      setCaChecking(false);
    }
  };

  const readTemplateFile = (
    file: File,
    setter: (v: string) => void,
    nameSetter: (v: string) => void,
  ) => {
    const reader = new FileReader();
    reader.onload = () => {
      const content = typeof reader.result === "string" ? reader.result : "";
      setter(content);
      nameSetter(file.name);
    };
    reader.onerror = () => {
      toast.error("读取模板失败");
    };
    reader.readAsText(file);
  };

  const handleSaveTemplate = async (key: string, value: string) => {
    setSaving(true);
    try {
      const resp = await updateConfig(key, value);
      if (resp.code === 0) {
        toast.success("模板已保存");
      } else {
        toast.error(resp.msg || "保存失败");
      }
    } finally {
      setSaving(false);
    }
  };

  const handleClearTemplate = async (key: string, setter: (v: string) => void) => {
    setSaving(true);
    try {
      const resp = await updateConfig(key, "");
      if (resp.code === 0) {
        setter("");
        toast.success("模板已清空");
      } else {
        toast.error(resp.msg || "清空失败");
      }
    } finally {
      setSaving(false);
    }
  };

  return (
    <div className="np-page">
      <div className="np-page-header">
        <div>
          <h1 className="np-page-title">订阅中心</h1>
          <p className="np-page-desc">统一生成 Clash / Surge / Shadowrocket 订阅。</p>
        </div>
      </div>
      <Card className="np-card">
        <CardHeader className="flex flex-col items-start gap-1">
          <h2 className="text-lg font-semibold">订阅中心</h2>
          <p className="text-sm text-default-500">
            订阅链接以当前登录 Token 鉴权，转发分组会同步到配置的 group
          </p>
        </CardHeader>
        <CardBody className="space-y-4">
          <div className="flex flex-col gap-3">
            <label className="text-sm text-default-500">Token</label>
            <div className="flex flex-col lg:flex-row gap-3">
              <Input
                readOnly
                aria-label="Token"
                value={token || "未检测到 Token"}
              />
              <Button
                color="primary"
                onPress={() => copyText(token, "Token")}
              >
                复制 Token
              </Button>
            </div>
          </div>
        </CardBody>
      </Card>

      <div className="grid grid-cols-1 xl:grid-cols-3 gap-6">
        <Card className="np-card xl:col-span-2">
          <CardHeader className="flex flex-col items-start gap-1">
            <h3 className="text-base font-semibold">订阅链接</h3>
            <p className="text-sm text-default-500">
              入口取自转发管理配置，出口协议由出口设置决定
            </p>
          </CardHeader>
          <CardBody className="space-y-4">
            {links.map((item) => (
              <div key={item.key} className="np-soft p-4 space-y-2">
                <div className="flex flex-col lg:flex-row lg:items-center lg:justify-between gap-2">
                  <div>
                    <div className="text-sm font-semibold">{item.title}</div>
                    <div className="text-xs text-default-500">{item.desc}</div>
                  </div>
                  <div className="flex items-center gap-2">
                    <Button
                      size="sm"
                      variant="bordered"
                      onPress={() => copyText(item.url, item.title)}
                    >
                      复制
                    </Button>
                    <Button
                      size="sm"
                      variant="flat"
                      onPress={() => window.open(item.url, "_blank")}
                    >
                      打开
                    </Button>
                  </div>
                </div>
                <Input readOnly aria-label={item.title} value={item.url} />
              </div>
            ))}
            <div className="np-soft p-4 space-y-2">
              <div className="text-sm font-semibold">Shadowrocket 分流使用说明</div>
              <p className="text-xs text-default-500">
                将上方「Shadowrocket 分流配置」链接复制到 Shadowrocket 底部「配置」→ 右上角「+」下载，点已下载文件选择「使用配置」；首页「全局路由」请选择「配置」，不要将此链接作为首页 Subscribe 节点订阅。打开该配置的「ⓘ」→「代理分组」，在 AI、谷歌、油管等业务组中分别选择具体节点。规则通过 LingJingMaster 规则集匹配；如需节点出现在首页，请另导入「纯节点订阅」。节点变更后在「配置」页重新下载，旧的纯节点订阅不会自动启用规则。
              </p>
              <div className="flex gap-2">
                <Button size="sm" variant="bordered" onPress={() => copyText(shadowrocketRulesUrl, "上游规则原址")}>
                  复制上游规则原址（参考）
                </Button>
                <Button size="sm" variant="flat" onPress={() => window.open(shadowrocketRulesUrl, "_blank", "noopener,noreferrer")}>
                  查看上游规则（不含节点）
                </Button>
              </div>
              <Input readOnly aria-label="上游 Shadowrocket 规则原址（不含节点）" value={shadowrocketRulesUrl} />
            </div>
          </CardBody>
        </Card>

        <div className="flex flex-col gap-6">
          {admin && anytlsCertEnabled && (
            <Card className="np-card">
              <CardHeader className="flex items-center justify-between">
                <h3 className="text-base font-semibold">站点 CA 管理</h3>
                <div className="text-xs text-default-400">
                  {caStatus?.caExists ? "已生成" : "未生成"}
                </div>
              </CardHeader>
              <CardBody className="space-y-3">
                <div className="text-xs text-default-500">
                  证书目录：{caStatus?.certDir || "-"}
                </div>
                <div className="text-xs text-default-500 break-all">
                  CA 文件：{caStatus?.caCertPath || "-"}
                </div>
                <div className="text-xs text-default-500 break-all">
                  密钥文件：{caStatus?.caKeyPath || "-"}
                </div>
                <div className="grid grid-cols-1 gap-2 text-xs text-default-500">
                  <div>组织：{caStatus?.subjectOrganization || caStatus?.certOrganization || "-"}</div>
                  <div>通用名：{caStatus?.subjectCommonName || caStatus?.certCommonName || "-"}</div>
                  <div>签发时间：{fmtTime(caStatus?.notBeforeMs)}</div>
                  <div>到期时间：{fmtTime(caStatus?.notAfterMs)}</div>
                  <div>最近更新时间：{fmtTime(caStatus?.caUpdatedAtMs)}</div>
                  <div>剩余天数：{typeof caStatus?.daysRemaining === "number" ? caStatus.daysRemaining : "-"}</div>
                  <div>CA 证书周期：5 年</div>
                  <div>节点证书周期：90 天（Agent 每 24 小时自动拉取）</div>
                  {caStatus?.parseError && (
                    <div className="text-danger">证书解析异常：{caStatus.parseError}</div>
                  )}
                </div>
                <div className="flex flex-wrap items-center gap-2">
                  <Button
                    size="sm"
                    variant="bordered"
                    isLoading={caLoading}
                    onPress={() => void refreshCAStatus()}
                  >
                    刷新状态
                  </Button>
                  <Button
                    size="sm"
                    color="warning"
                    isLoading={caGenerating}
                    onPress={() => void handleGenerateCA()}
                  >
                    生成/轮换 CA
                  </Button>
                  <Button
                    size="sm"
                    variant="bordered"
                    isLoading={caChecking}
                    onPress={() => void handleCheckCA()}
                  >
                    下载前自检
                  </Button>
                  <Button
                    size="sm"
                    color="primary"
                    variant="flat"
                    onPress={() =>
                      window.open(
                        `${baseUrl}/api/v1/subscription/docker-ca?token=${encodedToken}&format=pem`,
                        "_blank",
                      )
                    }
                  >
                    下载 CA (PEM)
                  </Button>
                  <Button
                    size="sm"
                    color="secondary"
                    variant="flat"
                    onPress={() =>
                      window.open(
                        `${baseUrl}/api/v1/subscription/docker-ca?token=${encodedToken}&format=cer`,
                        "_blank",
                      )
                    }
                  >
                    下载 CA (CER, macOS/Surge)
                  </Button>
                </div>
                {caCheck && (
                  <div className="rounded-large border border-default-200 bg-default-50 p-3 text-xs text-default-600 space-y-1">
                    <div>PEM 首行：{caCheck.pemFirstLine || "-"}</div>
                    <div>PEM 头格式：{caCheck.pemHeaderOK ? "正常" : "异常"}</div>
                    <div>X509 解析：{caCheck.parseOK ? "成功" : "失败"}</div>
                    <div>公钥算法：{caCheck.publicKeyAlgorithm || "-"}</div>
                    <div>签名算法：{caCheck.signatureAlgorithm || "-"}</div>
                    <div>可直接下载导入：{caCheck.downloadReady ? "是" : "否"}</div>
                    {caCheck.error && <div className="text-danger">错误：{caCheck.error}</div>}
                  </div>
                )}
              </CardBody>
            </Card>
          )}
          {admin && !anytlsCertEnabled && (
            <Card className="np-card">
              <CardHeader className="flex items-center justify-between">
                <h3 className="text-base font-semibold">站点 CA 管理</h3>
                <div className="text-xs text-default-400">已关闭</div>
              </CardHeader>
              <CardBody className="text-xs text-default-500">
                当前「网站配置 -&gt; 启用 AnyTLS 证书管理」为关闭状态，证书签发与下载入口已隐藏。
              </CardBody>
            </Card>
          )}

          <Card className="np-card">
            <CardHeader>
              <h3 className="text-base font-semibold">订阅模板</h3>
            </CardHeader>
            <CardBody className="space-y-6">
              <div className="space-y-3">
                <div className="flex items-center justify-between">
                  <div>
                    <div className="text-sm font-semibold">Clash 模板</div>
                    <div className="text-xs text-default-500">
                      支持 {"{{PROXIES}}"} / {"{{EXTRA_GROUPS}}"} 占位符，不填则自动替换 proxies / proxy-groups
                    </div>
                  </div>
                  <div className="text-xs text-default-400">
                    {clashTemplate ? "已启用" : "未设置"}
                  </div>
                </div>
                <div className="flex items-center gap-2">
                  <Input
                    readOnly
                    value={clashFileName || (clashTemplate ? "已加载模板" : "")}
                    placeholder="上传 .yaml 模板"
                  />
                  <Button
                    size="sm"
                    variant="bordered"
                    onPress={() => {
                      const input = document.createElement("input");
                      input.type = "file";
                      input.accept = ".yaml,.yml,.txt";
                      input.onchange = (e) => {
                        const file = (e.target as HTMLInputElement)?.files?.[0];
                        if (file) readTemplateFile(file, setClashTemplate, setClashFileName);
                      };
                      input.click();
                    }}
                  >
                    上传
                  </Button>
                </div>
                <Textarea
                  minRows={6}
                  value={clashTemplate}
                  onValueChange={setClashTemplate}
                  placeholder="粘贴 Clash 模板内容"
                />
                <div className="flex items-center gap-2">
                  <Button
                    size="sm"
                    color="primary"
                    isDisabled={saving}
                    onPress={() => handleSaveTemplate(templateKeyClash, clashTemplate)}
                  >
                    保存 Clash 模板
                  </Button>
                  <Button
                    size="sm"
                    variant="bordered"
                    isDisabled={saving || !clashTemplate}
                    onPress={() => handleClearTemplate(templateKeyClash, setClashTemplate)}
                  >
                    清空
                  </Button>
                </div>
              </div>

              <div className="space-y-3">
                <div className="flex items-center justify-between">
                  <div>
                    <div className="text-sm font-semibold">Surge 模板</div>
                    <div className="text-xs text-default-500">
                      支持 {"{{PROXIES}}"} / {"{{EXTRA_GROUPS}}"} 占位符，不填则自动替换 [Proxy] / [Proxy Group]
                    </div>
                  </div>
                  <div className="text-xs text-default-400">
                    {surgeTemplate ? "已启用" : "未设置"}
                  </div>
                </div>
                <div className="flex items-center gap-2">
                  <Input
                    readOnly
                    value={surgeFileName || (surgeTemplate ? "已加载模板" : "")}
                    placeholder="上传 .conf 模板"
                  />
                  <Button
                    size="sm"
                    variant="bordered"
                    onPress={() => {
                      const input = document.createElement("input");
                      input.type = "file";
                      input.accept = ".conf,.txt";
                      input.onchange = (e) => {
                        const file = (e.target as HTMLInputElement)?.files?.[0];
                        if (file) readTemplateFile(file, setSurgeTemplate, setSurgeFileName);
                      };
                      input.click();
                    }}
                  >
                    上传
                  </Button>
                </div>
                <Textarea
                  minRows={6}
                  value={surgeTemplate}
                  onValueChange={setSurgeTemplate}
                  placeholder="粘贴 Surge 模板内容"
                />
                <div className="flex items-center gap-2">
                  <Button
                    size="sm"
                    color="primary"
                    isDisabled={saving}
                    onPress={() => handleSaveTemplate(templateKeySurge, surgeTemplate)}
                  >
                    保存 Surge 模板
                  </Button>
                  <Button
                    size="sm"
                    variant="bordered"
                    isDisabled={saving || !surgeTemplate}
                    onPress={() => handleClearTemplate(templateKeySurge, setSurgeTemplate)}
                  >
                    清空
                  </Button>
                </div>
              </div>
            </CardBody>
          </Card>

          <Card className="np-card">
            <CardHeader>
              <h3 className="text-base font-semibold">订阅二维码</h3>
            </CardHeader>
            <CardBody className="flex flex-col items-center gap-4">
              <Select
                label="二维码类型"
                selectedKeys={[qrKey]}
                size="sm"
                variant="bordered"
                onSelectionChange={(keys) =>
                  setQrKey((Array.from(keys)[0] as string) || "clash")
                }
              >
                {links.map((link) => (
                  <SelectItem key={link.key}>{link.title}</SelectItem>
                ))}
              </Select>
              <div className="h-48 w-48 rounded-xl border border-dashed border-default-300 flex items-center justify-center text-default-400 overflow-hidden bg-white/70">
                {qrDataUrl ? (
                  <img
                    src={qrDataUrl}
                    alt="订阅二维码"
                    className="h-full w-full object-contain"
                  />
                ) : (
                  "暂无二维码"
                )}
              </div>
            </CardBody>
          </Card>

          <Card className="np-card">
            <CardHeader>
              <h3 className="text-base font-semibold">配置下载</h3>
            </CardHeader>
            <CardBody className="space-y-3">
              <Button
                variant="bordered"
                onPress={() => window.open(links.find((l) => l.key === "surge5")?.url || "", "_blank")}
              >
                下载 Surge 5 配置
              </Button>
              <Button
                variant="bordered"
                onPress={() => window.open(links.find((l) => l.key === "surge6")?.url || "", "_blank")}
              >
                下载 Surge 6 配置
              </Button>
              <Button
                variant="bordered"
                onPress={() => window.open(links.find((l) => l.key === "clash")?.url || "", "_blank")}
              >
                下载 Clash 配置
              </Button>
              <Button
                variant="bordered"
                onPress={() =>
                  window.open(
                    links.find((l) => l.key === "clash-meta")?.url || "",
                    "_blank",
                  )
                }
              >
                下载 Clash Meta 配置
              </Button>
              <Button
                variant="bordered"
                onPress={() =>
                  window.open(
                    links.find((l) => l.key === "singbox")?.url || "",
                    "_blank",
                  )
                }
              >
                下载 Sing-box 配置
              </Button>
            </CardBody>
          </Card>
        </div>
      </div>
    </div>
  );
}
