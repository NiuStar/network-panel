import { useEffect, useMemo, useState } from "react";
import { Button } from "@heroui/button";
import { Card, CardBody, CardHeader } from "@heroui/card";
import { Input } from "@heroui/input";
import { Select, SelectItem } from "@heroui/select";
import { toast } from "react-hot-toast";
import QRCode from "qrcode";

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
