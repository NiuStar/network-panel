import { useEffect, useMemo, useState } from "react";
import { Button } from "@heroui/button";
import { Card, CardBody, CardHeader } from "@heroui/card";
import { Input, Textarea } from "@heroui/input";
import { Select, SelectItem } from "@heroui/select";
import { toast } from "react-hot-toast";
import QRCode from "qrcode";
import { getConfigByName, updateConfig } from "@/api";

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
        title: "Shadowrocket 订阅",
        desc: "iOS 可直接导入订阅链接",
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
    getConfigByName(templateKeyClash).then((resp) => {
      if (resp.code === 0 && typeof resp.data === "string") {
        setClashTemplate(resp.data || "");
      }
    });
    getConfigByName(templateKeySurge).then((resp) => {
      if (resp.code === 0 && typeof resp.data === "string") {
        setSurgeTemplate(resp.data || "");
      }
    });
  }, []);

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
          </CardBody>
        </Card>

        <div className="flex flex-col gap-6">
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
