import { useEffect, useRef, useState } from "react";
import type { ChangeEvent } from "react";
import { Button } from "@heroui/button";
import { Card, CardBody, CardHeader } from "@heroui/card";
import {
  Table,
  TableHeader,
  TableColumn,
  TableBody,
  TableRow,
  TableCell,
} from "@heroui/table";
import {
  Modal,
  ModalContent,
  ModalHeader,
  ModalBody,
  ModalFooter,
  useDisclosure,
} from "@heroui/modal";
import { Input } from "@heroui/input";
import { Select, SelectItem } from "@heroui/select";
import { Switch } from "@heroui/switch";
import { Checkbox } from "@heroui/checkbox";
import { Chip } from "@heroui/chip";
import { Spinner } from "@heroui/spinner";
import toast from "react-hot-toast";

import {
  getExitNodes,
  cleanupExitNodes,
  createExitExternal,
  updateExitExternal,
  deleteExitExternal,
} from "@/api";

type ExitNodeItem = {
  source: "node" | "external";
  nodeId?: number;
  exitId?: number;
  name: string;
  host: string;
  online: boolean;
  ssPort?: number;
  anytlsPort?: number;
  protocol?: string;
  port?: number;
  config?: Record<string, any>;
};

type ExitExternalForm = {
  id?: number;
  name: string;
  host: string;
  port: string;
  protocol: string;
  params: Record<string, any>;
};

type InputChangeEvent = ChangeEvent<HTMLInputElement | HTMLTextAreaElement>;
type SelectionKeys = "all" | Set<string>;

const PROTOCOL_OPTIONS = [
  { key: "ss", label: "Shadowsocks" },
  { key: "vmess", label: "VMess" },
  { key: "vless", label: "VLESS" },
  { key: "trojan", label: "Trojan" },
  { key: "anytls", label: "AnyTLS" },
  { key: "tuic", label: "TUIC" },
  { key: "tuicv5", label: "TUIC v5" },
  { key: "hysteria2", label: "Hysteria 2" },
  { key: "hy2", label: "Hysteria 2 (HY2)" },
  { key: "hysteria", label: "Hysteria" },
  { key: "snell", label: "Snell" },
  { key: "wireguard", label: "WireGuard" },
  { key: "socks5", label: "SOCKS5" },
  { key: "http", label: "HTTP" },
  { key: "https", label: "HTTPS" },
  { key: "ssh", label: "SSH" },
  { key: "ssr", label: "ShadowsocksR" },
  { key: "shadowtls", label: "ShadowTLS" },
  { key: "naive", label: "Naive" },
  { key: "mieru", label: "Mieru" },
  { key: "juicity", label: "Juicity" },
  { key: "unknown", label: "未知协议" },
];

const SS_CIPHERS = [
  "2022-blake3-aes-128-gcm",
  "2022-blake3-aes-256-gcm",
  "aes-128-gcm",
  "aes-192-gcm",
  "aes-256-gcm",
  "chacha20-ietf-poly1305",
  "xchacha20-ietf-poly1305",
  "rc4",
  "rc4-md5",
  "aes-128-cfb",
  "aes-192-cfb",
  "aes-256-cfb",
  "aes-128-ctr",
  "aes-192-ctr",
  "aes-256-ctr",
  "bf-cfb",
  "camellia-128-cfb",
  "camellia-192-cfb",
  "camellia-256-cfb",
  "cast5-cfb",
  "des-cfb",
  "idea-cfb",
  "rc2-cfb",
  "seed-cfb",
  "salsa20",
  "chacha20",
  "chacha20-ietf",
  "none",
];
const DEFAULT_SS_CIPHER = "2022-blake3-aes-256-gcm";

const SEGMENT_OBFS = [
  { key: "off", label: "Off" },
  { key: "http", label: "HTTP" },
  { key: "tls", label: "TLS" },
];

const SEGMENT_QUIC = [
  { key: "auto", label: "自动" },
  { key: "on", label: "开启" },
  { key: "off", label: "关闭" },
];

const EMPTY_FORM: ExitExternalForm = {
  name: "",
  host: "",
  port: "",
  protocol: "ss",
  params: {},
};

export default function ExitNodePage() {
  const { isOpen, onOpen, onOpenChange } = useDisclosure();
  const [items, setItems] = useState<ExitNodeItem[]>([]);
  const [loading, setLoading] = useState(false);
  const [saving, setSaving] = useState(false);
  const [form, setForm] = useState<ExitExternalForm>(EMPTY_FORM);
  const [extraPairs, setExtraPairs] = useState<Array<{ key: string; value: string }>>([]);
  const [pickerOpen, setPickerOpen] = useState(false);
  const [pickerTitle, setPickerTitle] = useState("");
  const [pickerOptions, setPickerOptions] = useState<
    Array<{ label: string; value: string }>
  >([]);
  const [pickerValue, setPickerValue] = useState("");
  const pickerOnSelectRef = useRef<(value: string) => void>(() => undefined);

  const inputClassNames = {
    label: "text-[11px] text-default-500 mb-1",
    inputWrapper:
      "h-8 min-h-8 rounded-[8px] bg-white border border-default-200 shadow-none data-[hover=true]:border-default-300 data-[focus=true]:border-warning-400",
    input: "text-[12px]",
  };

  const selectClassNames = {
    label: "text-[11px] text-default-500 mb-1",
    trigger:
      "h-8 min-h-8 rounded-[8px] bg-white border border-default-200 shadow-none data-[hover=true]:border-default-300 data-[focus=true]:border-warning-400 px-2",
    value: "text-[12px]",
    selectorIcon: "w-3 h-3",
    popoverContent: "rounded-[10px] p-1",
  };
  const checkboxClassNames = {
    base: "items-start",
    wrapper: "items-start",
    label: "text-[11px] font-normal text-default-700 leading-4",
  };

  const ExitInput = (props: any) => (
    <Input
      {...props}
      size="sm"
      labelPlacement="outside"
      classNames={inputClassNames}
    />
  );

  const ExitSelect = (props: any) => {
    const {
      listboxProps,
      popoverProps,
      scrollShadowProps,
      isVirtualized,
      maxListboxHeight,
      itemHeight,
      ...rest
    } = props;
    return (
      <Select
        {...rest}
        size="sm"
        labelPlacement="outside"
        classNames={selectClassNames}
        isVirtualized={isVirtualized}
        itemHeight={itemHeight ?? 26}
        maxListboxHeight={maxListboxHeight ?? 260}
        listboxProps={{
          disableAnimation: true,
          classNames: { list: "text-[12px]" },
          itemClasses: {
            base: "text-[12px] py-1",
            title: "text-[12px]",
          },
          ...listboxProps,
        }}
        scrollShadowProps={{
          hideScrollBar: false,
          ...scrollShadowProps,
        }}
        popoverProps={{
          offset: 6,
          disableAnimation: true,
          classNames: { content: "rounded-[10px] p-1" },
          ...popoverProps,
        }}
      />
    );
  };

  const openPicker = (params: {
    title: string;
    options: Array<{ label: string; value: string }>;
    value: string;
    onSelect: (value: string) => void;
  }) => {
    setPickerTitle(params.title);
    setPickerOptions(params.options);
    setPickerValue(params.value);
    pickerOnSelectRef.current = params.onSelect;
    setPickerOpen(true);
  };

  const PickerField = (props: {
    label: string;
    value: string;
    placeholder?: string;
    onClick: () => void;
  }) => {
    const { label, value, placeholder, onClick } = props;
    return (
      <div className="exit-picker-field">
        <div className="exit-picker-label">{label}</div>
        <button type="button" className="exit-picker-trigger" onClick={onClick}>
          <span className={value ? "exit-picker-value" : "exit-picker-placeholder"}>
            {value || placeholder || "请选择"}
          </span>
          <span className="exit-picker-caret">▾</span>
        </button>
      </div>
    );
  };

  const load = async () => {
    setLoading(true);
    try {
      const r: any = await getExitNodes();

      if (r.code === 0 && Array.isArray(r.data)) {
        setItems(r.data as ExitNodeItem[]);
      } else {
        toast.error(r.msg || "获取出口节点失败");
      }
    } catch (e: any) {
      toast.error(e?.message || "获取出口节点失败");
    } finally {
      setLoading(false);
    }
  };

  useEffect(() => {
    load();
  }, []);

  const resetForm = () => setForm(EMPTY_FORM);

  const openCreate = () => {
    resetForm();
    onOpen();
  };

  const openEdit = (item: ExitNodeItem) => {
    const params =
      item && item.config && typeof item.config === "object" ? item.config : {};
    setForm({
      id: item.exitId,
      name: item.name || "",
      host: item.host || "",
      port: item.port ? String(item.port) : "",
      protocol: item.protocol || "ss",
      params,
    });
    onOpen();
  };

  const handleSave = async () => {
    const name = form.name.trim();
    const host = form.host.trim();
    const port = Number(form.port);
    const protocol = form.protocol.trim();

    if (!name || !host || !port || port < 1 || port > 65535) {
      toast.error("请填写正确的名称、地址和端口");
      return;
    }
    setSaving(true);
    try {
      const params = { ...(form.params || {}) };
      if ((protocol === "ss" || protocol === "ssr") && !params.cipher) {
        params.cipher = DEFAULT_SS_CIPHER;
      }
      if (protocol === "unknown") {
        extraPairs.forEach((pair) => {
          const key = pair.key.trim();
          if (!key) return;
          params[key] = pair.value;
        });
      }
      const payload = {
        name,
        host,
        port,
        protocol: protocol || undefined,
        config: params,
      };
      const r: any = form.id
        ? await updateExitExternal({ id: form.id, ...payload })
        : await createExitExternal(payload);

      if (r.code === 0) {
        toast.success(form.id ? "已更新" : "已创建");
        onOpenChange();
        resetForm();
        load();
      } else {
        toast.error(r.msg || "保存失败");
      }
    } catch (e: any) {
      toast.error(e?.message || "保存失败");
    } finally {
      setSaving(false);
    }
  };

  const handleDelete = async (item: ExitNodeItem) => {
    if (!item.exitId) return;
    setSaving(true);
    try {
      const r: any = await deleteExitExternal(item.exitId);

      if (r.code === 0) {
        toast.success("已删除");
        load();
      } else {
        toast.error(r.msg || "删除失败");
      }
    } catch (e: any) {
      toast.error(e?.message || "删除失败");
    } finally {
      setSaving(false);
    }
  };

  const handleCleanup = async () => {
    setSaving(true);
    try {
      const r: any = await cleanupExitNodes();
      if (r.code === 0) {
        const deletedExit = r.data?.deletedExit ?? 0;
        const deletedAnyTLS = r.data?.deletedAnyTLS ?? 0;
        toast.success(`已清理：出口 ${deletedExit} / AnyTLS ${deletedAnyTLS}`);
        await load();
      } else {
        toast.error(r.msg || "清理失败");
      }
    } catch (e: any) {
      toast.error(e?.message || "清理失败");
    } finally {
      setSaving(false);
    }
  };

  const renderPortInfo = (item: ExitNodeItem) => {
    if (item.source === "external") {
      return item.port ? String(item.port) : "-";
    }
    const parts: string[] = [];

    if (item.ssPort) parts.push(`ss:${item.ssPort}`);
    if (item.anytlsPort) parts.push(`anytls:${item.anytlsPort}`);

    return parts.length > 0 ? parts.join(" / ") : "-";
  };

  const renderProtocol = (item: ExitNodeItem) => {
    if (item.source === "external") {
      return item.protocol || "—";
    }
    const parts: string[] = [];

    if (item.ssPort) parts.push("ss");
    if (item.anytlsPort) parts.push("anytls");

    return parts.length > 0 ? parts.join(" / ") : "—";
  };

  const protocol = form.protocol || "";
  const params = form.params || {};
  const updateParam = (key: string, value: any) =>
    setForm((prev) => ({
      ...prev,
      params: { ...(prev.params || {}), [key]: value },
    }));
  const updateNestedParam = (key: string, nestedKey: string, value: any) => {
    const current = (params && typeof params[key] === "object" ? params[key] : {}) as Record<
      string,
      any
    >;
    updateParam(key, { ...current, [nestedKey]: value });
  };
  const protocolLabel =
    PROTOCOL_OPTIONS.find((p) => p.key === protocol)?.label || "未选择";
  const alpnValue = Array.isArray(params.alpn)
    ? params.alpn.join(",")
    : typeof params.alpn === "string"
      ? params.alpn
      : "";

  const knownKeys = () => {
    const common = [
      "uuid",
      "password",
      "username",
      "cipher",
      "alterId",
      "flow",
      "network",
      "tls",
      "sni",
      "alpn",
      "udp",
      "skip-cert-verify",
      "obfs",
      "obfs-password",
      "obfs-host",
      "psk",
      "version",
      "private-key",
      "public-key",
      "preshared-key",
      "ip",
      "dns",
      "mtu",
      "type",
      "protocol",
      "protocol-param",
      "obfs-param",
      "ws-opts",
      "grpc-opts",
      "h2-opts",
      "reality-opts",
      "block-quic",
      "proxy-chain",
      "test-url",
      "tos",
      "ip-version",
      "interface",
    ];
    return new Set(common);
  };

  useEffect(() => {
    if (!isOpen) return;
    if (protocol !== "unknown") {
      setExtraPairs([]);
      return;
    }
    const known = knownKeys();
    const pairs = Object.entries(params || {})
      .filter(([key, value]) => !known.has(key) && typeof value !== "object")
      .map(([key, value]) => ({ key, value: String(value ?? "") }));
    setExtraPairs(pairs);
  }, [isOpen, protocol]);

  return (
    <div className="np-page">
      <div className="np-page-header">
        <div>
          <h2 className="np-page-title">出口节点</h2>
          <p className="np-page-desc">统一管理已配置出口与外部出口地址。</p>
        </div>
        <div className="flex items-center gap-2">
          <Button variant="flat" onPress={handleCleanup}>
            清理失效出口
          </Button>
          <Button color="primary" onPress={openCreate}>
            新增外部出口
          </Button>
        </div>
      </div>
      <Card className="np-card">
        <CardHeader className="pb-2">
          <div className="text-sm text-default-600">出口节点列表</div>
        </CardHeader>
        <CardBody>
          {loading ? (
            <div className="py-12 flex items-center justify-center">
              <Spinner />
            </div>
          ) : (
            <Table
              aria-label="出口节点列表"
              className="min-h-[200px]"
              removeWrapper
            >
              <TableHeader>
                <TableColumn>来源</TableColumn>
                <TableColumn>名称</TableColumn>
                <TableColumn>地址</TableColumn>
                <TableColumn>协议</TableColumn>
                <TableColumn>端口</TableColumn>
                <TableColumn>状态</TableColumn>
                <TableColumn>操作</TableColumn>
              </TableHeader>
              <TableBody emptyContent="暂无出口节点" items={items}>
                {(item) => (
                  <TableRow key={`${item.source}-${item.exitId || item.nodeId}`}>
                    <TableCell>
                      <Chip
                        size="sm"
                        variant="flat"
                        color={item.source === "node" ? "primary" : "warning"}
                      >
                        {item.source === "node" ? "节点出口" : "外部出口"}
                      </Chip>
                    </TableCell>
                    <TableCell>{item.name || "-"}</TableCell>
                    <TableCell>{item.host || "-"}</TableCell>
                    <TableCell>{renderProtocol(item)}</TableCell>
                    <TableCell>{renderPortInfo(item)}</TableCell>
                    <TableCell>
                      {item.source === "node" ? (
                        <Chip
                          size="sm"
                          variant="flat"
                          color={item.online ? "success" : "danger"}
                        >
                          {item.online ? "在线" : "离线"}
                        </Chip>
                      ) : (
                        <Chip size="sm" variant="flat">
                          可用
                        </Chip>
                      )}
                    </TableCell>
                    <TableCell>
                      {item.source === "external" ? (
                        <div className="flex items-center gap-2">
                          <Button size="sm" variant="flat" onPress={() => openEdit(item)}>
                            编辑
                          </Button>
                          <Button
                            color="danger"
                            size="sm"
                            variant="flat"
                            onPress={() => handleDelete(item)}
                          >
                            删除
                          </Button>
                        </div>
                      ) : (
                        <span className="text-xs text-default-400">—</span>
                      )}
                    </TableCell>
                  </TableRow>
                )}
              </TableBody>
            </Table>
          )}
        </CardBody>
      </Card>

      <Modal
        backdrop="opaque"
        disableAnimation
        isOpen={isOpen}
        placement="center"
        size="4xl"
        onOpenChange={onOpenChange}
      >
        <ModalContent className="exit-modal">
          {(onClose) => (
            <>
              <ModalHeader className="exit-header">
                <div className="flex flex-col gap-1">
                  <div className="exit-title">
                    {form.id ? "编辑代理" : "新增代理"}
                  </div>
                  <div className="exit-subtitle">配置代理协议与出站策略</div>
                </div>
              </ModalHeader>
              <ModalBody className="exit-body">
                <div className="grid grid-cols-1 xl:grid-cols-[1.2fr_0.8fr] gap-5">
                  <div className="space-y-4">
                    <div className="exit-section exit-top-row">
                      <div className="grid grid-cols-1 md:grid-cols-[0.9fr_1.1fr] gap-3 items-start">
                        <div className="exit-protocol-block">
                          <PickerField
                            label="协议"
                            value={protocol || "ss"}
                            placeholder="选择协议"
                            onClick={() =>
                              openPicker({
                                title: "协议",
                                options: PROTOCOL_OPTIONS.map((item) => ({
                                  label: item.label,
                                  value: item.key,
                                })),
                                value: protocol || "ss",
                                onSelect: (val) =>
                                  setForm((prev) => ({
                                    ...prev,
                                    protocol: val || "ss",
                                  })),
                              })
                            }
                          />
                        </div>
                        <ExitInput
                          size="sm"
                          label="名称"
                          placeholder="输入代理名称"
                          value={form.name}
                          classNames={inputClassNames}
                          onChange={(e: InputChangeEvent) =>
                            setForm((prev) => ({ ...prev, name: e.target.value }))
                          }
                        />
                      </div>
                    </div>

                    <div className="exit-section">
                      <div className="exit-section-title">服务器信息</div>
                      <div className="grid grid-cols-1 md:grid-cols-[1.6fr_0.4fr] gap-3">
                        <ExitInput
                          size="sm"
                          label="服务器地址"
                          placeholder="IP 或域名"
                          value={form.host}
                          classNames={inputClassNames}
                          onChange={(e: InputChangeEvent) =>
                            setForm((prev) => ({ ...prev, host: e.target.value }))
                          }
                        />
                        <ExitInput
                          size="sm"
                          label="端口"
                          placeholder="443"
                          type="number"
                          value={form.port}
                          classNames={inputClassNames}
                          onChange={(e: InputChangeEvent) =>
                            setForm((prev) => ({ ...prev, port: e.target.value }))
                          }
                        />
                      </div>
                    </div>

                    <div className="exit-section">
                      <div className="exit-section-title">
                        {protocolLabel === "未选择" ? "协议参数" : protocolLabel}
                      </div>
                      <div className="grid grid-cols-1 md:grid-cols-2 gap-3">
                        {protocol === "ss" ? (
                          <>
                            <PickerField
                              label="加密方式"
                              value={params.cipher || DEFAULT_SS_CIPHER}
                              onClick={() =>
                                openPicker({
                                  title: "加密方式",
                                  options: SS_CIPHERS.map((cipher) => ({
                                    label: cipher,
                                    value: cipher,
                                  })),
                                  value: params.cipher || DEFAULT_SS_CIPHER,
                                  onSelect: (val) => updateParam("cipher", val),
                                })
                              }
                            />
                            <ExitInput
                              size="sm"
                              label="密码"
                              placeholder="输入密码"
                              type="password"
                              value={params.password || ""}
                              classNames={inputClassNames}
                              onChange={(e: InputChangeEvent) => updateParam("password", e.target.value)}
                            />
                            <div className="col-span-full exit-subsection">
                              <div className="exit-subsection-title">混淆</div>
                              <div className="exit-segment-group">
                                {SEGMENT_OBFS.map((item) => (
                                  <Button
                                    key={item.key}
                                    size="sm"
                                    variant={params.obfs === item.key ? "solid" : "flat"}
                                    color={params.obfs === item.key ? "warning" : "default"}
                                    className="exit-segment-btn"
                                    onPress={() => updateParam("obfs", item.key)}
                                  >
                                    {item.label}
                                  </Button>
                                ))}
                              </div>
                              <ExitInput
                                size="sm"
                                label="混淆主机名"
                                placeholder="可选"
                                value={params["obfs-host"] || ""}
                                classNames={inputClassNames}
                                onChange={(e: InputChangeEvent) =>
                                  updateParam("obfs-host", e.target.value)
                                }
                              />
                            </div>
                            <div className="col-span-full exit-subsection">
                              <div className="exit-subsection-title">
                                阻止 QUIC (HTTP/3)
                              </div>
                              <div className="exit-segment-group">
                                {SEGMENT_QUIC.map((item) => (
                                  <Button
                                    key={item.key}
                                    size="sm"
                                    variant={
                                      (params["block-quic"] || "auto") === item.key
                                        ? "solid"
                                        : "flat"
                                    }
                                    color={
                                      (params["block-quic"] || "auto") === item.key
                                        ? "warning"
                                        : "default"
                                    }
                                    className="exit-segment-btn"
                                    onPress={() => updateParam("block-quic", item.key)}
                                  >
                                    {item.label}
                                  </Button>
                                ))}
                              </div>
                            </div>
                          </>
                        ) : null}

                        {protocol === "anytls" ? (
                          <ExitInput
                            size="sm"
                            label="密码"
                            placeholder="输入密码"
                            type="password"
                            value={params.password || ""}
                            classNames={inputClassNames}
                            onChange={(e: InputChangeEvent) => updateParam("password", e.target.value)}
                          />
                        ) : null}

                        {protocol === "vmess" || protocol === "vless" ? (
                          <>
                            <ExitInput
                              size="sm"
                              label="UUID"
                              placeholder="用户 UUID"
                              value={params.uuid || ""}
                              classNames={inputClassNames}
                              onChange={(e: InputChangeEvent) => updateParam("uuid", e.target.value)}
                            />
                            {protocol === "vmess" ? (
                              <ExitSelect
                                size="sm"
                                label="加密"
                                selectedKeys={
                                  params.cipher
                                    ? new Set([String(params.cipher)])
                                    : new Set()
                                }
                                classNames={selectClassNames}
                                onSelectionChange={(keys: SelectionKeys) =>
                                  updateParam(
                                    "cipher",
                                    Array.from(keys)[0] || "auto",
                                  )
                                }
                              >
                                {[
                                  "auto",
                                  "none",
                                  "aes-128-gcm",
                                  "chacha20-poly1305",
                                ].map((cipher) => (
                                  <SelectItem key={cipher}>{cipher}</SelectItem>
                                ))}
                              </ExitSelect>
                            ) : null}
                            {protocol === "vmess" ? (
                              <ExitInput
                                size="sm"
                                label="Alter ID (可选)"
                                type="number"
                                value={params.alterId ?? ""}
                                classNames={inputClassNames}
                                onChange={(e: InputChangeEvent) =>
                                  updateParam(
                                    "alterId",
                                    e.target.value ? Number(e.target.value) : "",
                                  )
                                }
                              />
                            ) : null}
                            {protocol === "vless" ? (
                              <ExitSelect
                                size="sm"
                                label="Flow (可选)"
                                selectedKeys={
                                  params.flow
                                    ? new Set([String(params.flow)])
                                    : new Set()
                                }
                                classNames={selectClassNames}
                                onSelectionChange={(keys: SelectionKeys) =>
                                  updateParam("flow", Array.from(keys)[0] || "")
                                }
                              >
                                {[
                                  "",
                                  "xtls-rprx-vision",
                                  "xtls-rprx-vision-udp443",
                                ].map((flow) => (
                                  <SelectItem key={flow || "none"}>
                                    {flow || "默认"}
                                  </SelectItem>
                                ))}
                              </ExitSelect>
                            ) : null}
                          </>
                        ) : null}

                        {protocol === "trojan" ? (
                          <ExitInput
                            size="sm"
                            label="密码"
                            placeholder="Trojan 密码"
                            type="password"
                            value={params.password || ""}
                            classNames={inputClassNames}
                            onChange={(e: InputChangeEvent) => updateParam("password", e.target.value)}
                          />
                        ) : null}

                        {protocol === "hysteria2" || protocol === "hy2" ? (
                          <>
                            <ExitInput
                              size="sm"
                              label="密码"
                              placeholder="Hysteria2 密码"
                              type="password"
                              value={params.password || ""}
                              classNames={inputClassNames}
                              onChange={(e: InputChangeEvent) => updateParam("password", e.target.value)}
                            />
                            <ExitInput
                              size="sm"
                              label="上行带宽(Mbps)"
                              type="number"
                              value={params.up ?? ""}
                              classNames={inputClassNames}
                              onChange={(e: InputChangeEvent) =>
                                updateParam(
                                  "up",
                                  e.target.value ? Number(e.target.value) : "",
                                )
                              }
                            />
                            <ExitInput
                              size="sm"
                              label="下行带宽(Mbps)"
                              type="number"
                              value={params.down ?? ""}
                              classNames={inputClassNames}
                              onChange={(e: InputChangeEvent) =>
                                updateParam(
                                  "down",
                                  e.target.value ? Number(e.target.value) : "",
                                )
                              }
                            />
                            <ExitSelect
                              size="sm"
                              label="混淆"
                              selectedKeys={
                                params.obfs
                                  ? new Set([String(params.obfs)])
                                  : new Set()
                              }
                              classNames={selectClassNames}
                              onSelectionChange={(keys: SelectionKeys) =>
                                updateParam("obfs", Array.from(keys)[0] || "")
                              }
                            >
                              {["", "salamander"].map((item) => (
                                <SelectItem key={item || "none"}>
                                  {item || "无"}
                                </SelectItem>
                              ))}
                            </ExitSelect>
                            <ExitInput
                              size="sm"
                              label="混淆密码(可选)"
                              value={params["obfs-password"] || ""}
                              classNames={inputClassNames}
                              onChange={(e: InputChangeEvent) =>
                                updateParam("obfs-password", e.target.value)
                              }
                            />
                          </>
                        ) : null}

                        {protocol === "hysteria" ? (
                          <>
                            <ExitInput
                              size="sm"
                              label="认证信息"
                              placeholder="auth 或 auth_str"
                              value={params.auth || ""}
                              classNames={inputClassNames}
                              onChange={(e: InputChangeEvent) => updateParam("auth", e.target.value)}
                            />
                            <ExitInput
                              size="sm"
                              label="上行带宽(Mbps)"
                              type="number"
                              value={params.up ?? ""}
                              classNames={inputClassNames}
                              onChange={(e: InputChangeEvent) =>
                                updateParam(
                                  "up",
                                  e.target.value ? Number(e.target.value) : "",
                                )
                              }
                            />
                            <ExitInput
                              size="sm"
                              label="下行带宽(Mbps)"
                              type="number"
                              value={params.down ?? ""}
                              classNames={inputClassNames}
                              onChange={(e: InputChangeEvent) =>
                                updateParam(
                                  "down",
                                  e.target.value ? Number(e.target.value) : "",
                                )
                              }
                            />
                            <ExitSelect
                              size="sm"
                              label="混淆"
                              selectedKeys={
                                params.obfs
                                  ? new Set([String(params.obfs)])
                                  : new Set()
                              }
                              classNames={selectClassNames}
                              onSelectionChange={(keys: SelectionKeys) =>
                                updateParam("obfs", Array.from(keys)[0] || "")
                              }
                            >
                              {["", "salamander"].map((item) => (
                                <SelectItem key={item || "none"}>
                                  {item || "无"}
                                </SelectItem>
                              ))}
                            </ExitSelect>
                            <ExitInput
                              size="sm"
                              label="混淆密码(可选)"
                              value={params["obfs-password"] || ""}
                              classNames={inputClassNames}
                              onChange={(e: InputChangeEvent) =>
                                updateParam("obfs-password", e.target.value)
                              }
                            />
                          </>
                        ) : null}

                        {protocol === "tuic" || protocol === "tuicv5" ? (
                          <>
                            <ExitInput
                              size="sm"
                              label="UUID"
                              value={params.uuid || ""}
                              classNames={inputClassNames}
                              onChange={(e: InputChangeEvent) => updateParam("uuid", e.target.value)}
                            />
                            <ExitInput
                              size="sm"
                              label="密码"
                              type="password"
                              value={params.password || ""}
                              classNames={inputClassNames}
                              onChange={(e: InputChangeEvent) => updateParam("password", e.target.value)}
                            />
                            <ExitSelect
                              size="sm"
                              label="拥塞控制"
                              selectedKeys={
                                params["congestion-controller"]
                                  ? new Set([String(params["congestion-controller"])])
                                  : new Set()
                              }
                              classNames={selectClassNames}
                              onSelectionChange={(keys: SelectionKeys) =>
                                updateParam(
                                  "congestion-controller",
                                  Array.from(keys)[0] || "bbr",
                                )
                              }
                            >
                              {["bbr", "cubic", "reno"].map((item) => (
                                <SelectItem key={item}>{item}</SelectItem>
                              ))}
                            </ExitSelect>
                            <ExitSelect
                              size="sm"
                              label="UDP Relay 模式"
                              selectedKeys={
                                params["udp-relay-mode"]
                                  ? new Set([String(params["udp-relay-mode"])])
                                  : new Set()
                              }
                              classNames={selectClassNames}
                              onSelectionChange={(keys: SelectionKeys) =>
                                updateParam(
                                  "udp-relay-mode",
                                  Array.from(keys)[0] || "native",
                                )
                              }
                            >
                              {["native", "quic"].map((item) => (
                                <SelectItem key={item}>{item}</SelectItem>
                              ))}
                            </ExitSelect>
                          </>
                        ) : null}

                        {protocol === "snell" ? (
                          <>
                            <ExitInput
                              size="sm"
                              label="PSK"
                              type="password"
                              value={params.psk || ""}
                              classNames={inputClassNames}
                              onChange={(e: InputChangeEvent) => updateParam("psk", e.target.value)}
                            />
                            <ExitSelect
                              size="sm"
                              label="版本"
                              selectedKeys={
                                params.version
                                  ? new Set([String(params.version)])
                                  : new Set()
                              }
                              classNames={selectClassNames}
                              onSelectionChange={(keys: SelectionKeys) =>
                                updateParam("version", Array.from(keys)[0] || "v2")
                              }
                            >
                              {["v1", "v2", "v3"].map((item) => (
                                <SelectItem key={item}>{item}</SelectItem>
                              ))}
                            </ExitSelect>
                            <ExitSelect
                              size="sm"
                              label="混淆"
                              selectedKeys={
                                params.obfs
                                  ? new Set([String(params.obfs)])
                                  : new Set()
                              }
                              classNames={selectClassNames}
                              onSelectionChange={(keys: SelectionKeys) =>
                                updateParam("obfs", Array.from(keys)[0] || "")
                              }
                            >
                              {["", "http", "tls"].map((item) => (
                                <SelectItem key={item || "none"}>
                                  {item || "无"}
                                </SelectItem>
                              ))}
                            </ExitSelect>
                            <ExitInput
                              size="sm"
                              label="混淆主机(可选)"
                              value={params["obfs-host"] || ""}
                              classNames={inputClassNames}
                              onChange={(e: InputChangeEvent) => updateParam("obfs-host", e.target.value)}
                            />
                          </>
                        ) : null}

                        {["socks5", "http", "https", "ssh", "naive"].includes(protocol) ? (
                          <>
                            <ExitInput
                              size="sm"
                              label="用户名(可选)"
                              value={params.username || ""}
                              classNames={inputClassNames}
                              onChange={(e: InputChangeEvent) => updateParam("username", e.target.value)}
                            />
                            <ExitInput
                              size="sm"
                              label="密码(可选)"
                              type="password"
                              value={params.password || ""}
                              classNames={inputClassNames}
                              onChange={(e: InputChangeEvent) => updateParam("password", e.target.value)}
                            />
                          </>
                        ) : null}

                        {protocol === "wireguard" ? (
                          <>
                            <ExitInput
                              size="sm"
                              label="Private Key"
                              value={params["private-key"] || ""}
                              classNames={inputClassNames}
                              onChange={(e: InputChangeEvent) => updateParam("private-key", e.target.value)}
                            />
                            <ExitInput
                              size="sm"
                              label="Public Key"
                              value={params["public-key"] || ""}
                              classNames={inputClassNames}
                              onChange={(e: InputChangeEvent) => updateParam("public-key", e.target.value)}
                            />
                            <ExitInput
                              size="sm"
                              label="Preshared Key (可选)"
                              value={params["preshared-key"] || ""}
                              classNames={inputClassNames}
                              onChange={(e: InputChangeEvent) => updateParam("preshared-key", e.target.value)}
                            />
                            <ExitInput
                              size="sm"
                              label="IP (可选)"
                              value={params.ip || ""}
                              classNames={inputClassNames}
                              onChange={(e: InputChangeEvent) => updateParam("ip", e.target.value)}
                            />
                            <ExitInput
                              size="sm"
                              label="DNS (可选)"
                              value={params.dns || ""}
                              classNames={inputClassNames}
                              onChange={(e: InputChangeEvent) => updateParam("dns", e.target.value)}
                            />
                            <ExitInput
                              size="sm"
                              label="MTU (可选)"
                              type="number"
                              value={params.mtu ?? ""}
                              classNames={inputClassNames}
                              onChange={(e: InputChangeEvent) =>
                                updateParam(
                                  "mtu",
                                  e.target.value ? Number(e.target.value) : "",
                                )
                              }
                            />
                          </>
                        ) : null}

                        {protocol === "ssr" ? (
                          <>
                            <PickerField
                              label="加密方式"
                              value={params.cipher || DEFAULT_SS_CIPHER}
                              onClick={() =>
                                openPicker({
                                  title: "加密方式",
                                  options: SS_CIPHERS.map((cipher) => ({
                                    label: cipher,
                                    value: cipher,
                                  })),
                                  value: params.cipher || DEFAULT_SS_CIPHER,
                                  onSelect: (val) => updateParam("cipher", val),
                                })
                              }
                            />
                            <ExitInput
                              size="sm"
                              label="密码"
                              type="password"
                              value={params.password || ""}
                              classNames={inputClassNames}
                              onChange={(e: InputChangeEvent) => updateParam("password", e.target.value)}
                            />
                            <ExitSelect
                              size="sm"
                              label="协议"
                              selectedKeys={
                                params.protocol
                                  ? new Set([String(params.protocol)])
                                  : new Set()
                              }
                              classNames={selectClassNames}
                              onSelectionChange={(keys: SelectionKeys) =>
                                updateParam("protocol", Array.from(keys)[0] || "origin")
                              }
                            >
                              {[
                                "origin",
                                "auth_sha1_v4",
                                "auth_aes128_md5",
                                "auth_aes128_sha1",
                              ].map((item) => (
                                <SelectItem key={item}>{item}</SelectItem>
                              ))}
                            </ExitSelect>
                            <ExitInput
                              size="sm"
                              label="协议参数(可选)"
                              value={params["protocol-param"] || ""}
                              classNames={inputClassNames}
                              onChange={(e: InputChangeEvent) => updateParam("protocol-param", e.target.value)}
                            />
                            <ExitSelect
                              size="sm"
                              label="混淆"
                              selectedKeys={
                                params.obfs
                                  ? new Set([String(params.obfs)])
                                  : new Set()
                              }
                              classNames={selectClassNames}
                              onSelectionChange={(keys: SelectionKeys) =>
                                updateParam("obfs", Array.from(keys)[0] || "plain")
                              }
                            >
                              {["plain", "http_simple", "tls1.2_ticket_auth"].map(
                                (item) => (
                                  <SelectItem key={item}>{item}</SelectItem>
                                ),
                              )}
                            </ExitSelect>
                            <ExitInput
                              size="sm"
                              label="混淆参数(可选)"
                              value={params["obfs-param"] || ""}
                              classNames={inputClassNames}
                              onChange={(e: InputChangeEvent) => updateParam("obfs-param", e.target.value)}
                            />
                          </>
                        ) : null}

                        {protocol === "shadowtls" ? (
                          <>
                            <ExitSelect
                              size="sm"
                              label="版本"
                              selectedKeys={
                                params.version
                                  ? new Set([String(params.version)])
                                  : new Set()
                              }
                              classNames={selectClassNames}
                              onSelectionChange={(keys: SelectionKeys) =>
                                updateParam("version", Array.from(keys)[0] || "v3")
                              }
                            >
                              {["v1", "v2", "v3"].map((item) => (
                                <SelectItem key={item}>{item}</SelectItem>
                              ))}
                            </ExitSelect>
                            <ExitInput
                              size="sm"
                              label="密码/密钥"
                              type="password"
                              value={params.password || ""}
                              classNames={inputClassNames}
                              onChange={(e: InputChangeEvent) => updateParam("password", e.target.value)}
                            />
                            <ExitInput
                              size="sm"
                              label="SNI (可选)"
                              value={params.sni || ""}
                              classNames={inputClassNames}
                              onChange={(e: InputChangeEvent) => updateParam("sni", e.target.value)}
                            />
                            <ExitInput
                              size="sm"
                              label="ALPN (可选)"
                              placeholder="h2,http/1.1"
                              value={alpnValue}
                              classNames={inputClassNames}
                              onChange={(e: InputChangeEvent) =>
                                updateParam(
                                  "alpn",
                                  e.target.value
                                    .split(",")
                                    .map((v: string) => v.trim())
                                    .filter((v: string) => v),
                                )
                              }
                            />
                          </>
                        ) : null}

                        {protocol === "mieru" ? (
                          <>
                            <ExitInput
                              size="sm"
                              label="用户名"
                              value={params.username || ""}
                              classNames={inputClassNames}
                              onChange={(e: InputChangeEvent) => updateParam("username", e.target.value)}
                            />
                            <ExitInput
                              size="sm"
                              label="密码"
                              type="password"
                              value={params.password || ""}
                              classNames={inputClassNames}
                              onChange={(e: InputChangeEvent) => updateParam("password", e.target.value)}
                            />
                            <ExitInput
                              size="sm"
                              label="SNI (可选)"
                              value={params.sni || ""}
                              classNames={inputClassNames}
                              onChange={(e: InputChangeEvent) => updateParam("sni", e.target.value)}
                            />
                          </>
                        ) : null}

                        {protocol === "juicity" ? (
                          <>
                            <ExitInput
                              size="sm"
                              label="UUID"
                              value={params.uuid || ""}
                              classNames={inputClassNames}
                              onChange={(e: InputChangeEvent) => updateParam("uuid", e.target.value)}
                            />
                            <ExitInput
                              size="sm"
                              label="密码"
                              type="password"
                              value={params.password || ""}
                              classNames={inputClassNames}
                              onChange={(e: InputChangeEvent) => updateParam("password", e.target.value)}
                            />
                            <ExitInput
                              size="sm"
                              label="SNI (可选)"
                              value={params.sni || ""}
                              classNames={inputClassNames}
                              onChange={(e: InputChangeEvent) => updateParam("sni", e.target.value)}
                            />
                          </>
                        ) : null}

                        {protocol === "unknown" ? (
                          <>
                            <ExitInput
                              size="sm"
                              label="自定义协议标识"
                              placeholder="例如 vmess / vless"
                              value={params.type || ""}
                              classNames={inputClassNames}
                              onChange={(e: InputChangeEvent) => updateParam("type", e.target.value)}
                            />
                            <ExitInput
                              size="sm"
                              label="用户名(可选)"
                              value={params.username || ""}
                              classNames={inputClassNames}
                              onChange={(e: InputChangeEvent) => updateParam("username", e.target.value)}
                            />
                            <ExitInput
                              size="sm"
                              label="密码(可选)"
                              type="password"
                              value={params.password || ""}
                              classNames={inputClassNames}
                              onChange={(e: InputChangeEvent) => updateParam("password", e.target.value)}
                            />
                            <ExitInput
                              size="sm"
                              label="UUID(可选)"
                              value={params.uuid || ""}
                              classNames={inputClassNames}
                              onChange={(e: InputChangeEvent) => updateParam("uuid", e.target.value)}
                            />
                            <ExitSelect
                              size="sm"
                              label="加密方式(可选)"
                              selectedKeys={
                                params.cipher
                                  ? new Set([String(params.cipher)])
                                  : new Set()
                              }
                              classNames={selectClassNames}
                              onSelectionChange={(keys: SelectionKeys) =>
                                updateParam("cipher", Array.from(keys)[0] || "")
                              }
                            >
                              {SS_CIPHERS.map((cipher) => (
                                <SelectItem key={cipher}>{cipher}</SelectItem>
                              ))}
                            </ExitSelect>
                            <ExitSelect
                              size="sm"
                              label="传输方式(可选)"
                              selectedKeys={
                                params.network
                                  ? new Set([String(params.network)])
                                  : new Set()
                              }
                              classNames={selectClassNames}
                              onSelectionChange={(keys: SelectionKeys) =>
                                updateParam("network", Array.from(keys)[0] || "tcp")
                              }
                            >
                              {["tcp", "ws", "grpc", "h2"].map((item) => (
                                <SelectItem key={item}>{item}</SelectItem>
                              ))}
                            </ExitSelect>
                            <Switch
                              className="w-full justify-between"
                              isSelected={!!params.tls}
                              onValueChange={(val) => updateParam("tls", val)}
                            >
                              TLS
                            </Switch>
                            <ExitInput
                              size="sm"
                              label="SNI (可选)"
                              value={params.sni || ""}
                              classNames={inputClassNames}
                              onChange={(e: InputChangeEvent) => updateParam("sni", e.target.value)}
                            />
                            <ExitInput
                              size="sm"
                              label="ALPN (可选)"
                              placeholder="h2,http/1.1"
                              value={alpnValue}
                              classNames={inputClassNames}
                              onChange={(e: InputChangeEvent) =>
                                updateParam(
                                  "alpn",
                                  e.target.value
                                    .split(",")
                                    .map((v: string) => v.trim())
                                    .filter((v: string) => v),
                                )
                              }
                            />
                            <div className="col-span-full exit-subsection">
                              <div className="exit-subsection-title">自定义参数</div>
                              <div className="space-y-2">
                                {extraPairs.map((pair, idx) => (
                                  <div
                                    key={`${pair.key}-${idx}`}
                                    className="grid grid-cols-[1fr_1fr_auto] gap-2"
                                  >
                                    <ExitInput
                                      size="sm"
                                      placeholder="参数名"
                                      value={pair.key}
                                      classNames={inputClassNames}
                                      onChange={(e: InputChangeEvent) => {
                                        const next = [...extraPairs];
                                        next[idx] = { ...next[idx], key: e.target.value };
                                        setExtraPairs(next);
                                      }}
                                    />
                                    <ExitInput
                                      size="sm"
                                      placeholder="参数值"
                                      value={pair.value}
                                      classNames={inputClassNames}
                                      onChange={(e: InputChangeEvent) => {
                                        const next = [...extraPairs];
                                        next[idx] = { ...next[idx], value: e.target.value };
                                        setExtraPairs(next);
                                      }}
                                    />
                                    <Button
                                      size="sm"
                                      variant="flat"
                                      color="danger"
                                      onPress={() => {
                                        const next = extraPairs.filter((_, i) => i !== idx);
                                        setExtraPairs(next);
                                      }}
                                    >
                                      删除
                                    </Button>
                                  </div>
                                ))}
                                <Button
                                  size="sm"
                                  variant="flat"
                                  onPress={() =>
                                    setExtraPairs((prev) => [
                                      ...prev,
                                      { key: "", value: "" },
                                    ])
                                  }
                                >
                                  添加参数
                                </Button>
                              </div>
                            </div>
                          </>
                        ) : null}
                      </div>
                    </div>

                    {(protocol === "vmess" || protocol === "vless" || protocol === "trojan") && (
                      <div className="exit-section">
                        <div className="exit-section-title">传输设置</div>
                        <div className="grid grid-cols-1 md:grid-cols-2 gap-3">
                          <ExitSelect
                            size="sm"
                            label="传输方式"
                            selectedKeys={
                              params.network ? new Set([String(params.network)]) : new Set()
                            }
                            classNames={selectClassNames}
                            onSelectionChange={(keys: SelectionKeys) =>
                              updateParam("network", Array.from(keys)[0] || "tcp")
                            }
                          >
                            {["tcp", "ws", "grpc", "h2"].map((item) => (
                              <SelectItem key={item}>{item}</SelectItem>
                            ))}
                          </ExitSelect>
                        </div>

                        {(params.network === "ws" ||
                          params.network === "grpc" ||
                          params.network === "h2") && (
                          <div className="mt-3 grid grid-cols-1 md:grid-cols-2 gap-3">
                            {params.network === "ws" ? (
                              <>
                                <ExitInput
                                  size="sm"
                                  label="WS Path"
                                  placeholder="/path"
                                  value={params["ws-opts"]?.path || ""}
                                  classNames={inputClassNames}
                                  onChange={(e: InputChangeEvent) =>
                                    updateNestedParam("ws-opts", "path", e.target.value)
                                  }
                                />
                                <ExitInput
                                  size="sm"
                                  label="WS Host (可选)"
                                  placeholder="example.com"
                                  value={params["ws-opts"]?.headers?.Host || ""}
                                  classNames={inputClassNames}
                                  onChange={(e: InputChangeEvent) => {
                                    const headers =
                                      (params["ws-opts"]?.headers as Record<string, any>) || {};
                                    updateNestedParam("ws-opts", "headers", {
                                      ...headers,
                                      Host: e.target.value,
                                    });
                                  }}
                                />
                              </>
                            ) : null}
                            {params.network === "grpc" ? (
                              <ExitInput
                                size="sm"
                                label="gRPC ServiceName"
                                value={params["grpc-opts"]?.["grpc-service-name"] || ""}
                                classNames={inputClassNames}
                                onChange={(e: InputChangeEvent) =>
                                  updateNestedParam(
                                    "grpc-opts",
                                    "grpc-service-name",
                                    e.target.value,
                                  )
                                }
                              />
                            ) : null}
                            {params.network === "h2" ? (
                              <>
                                <ExitInput
                                  size="sm"
                                  label="HTTP/2 Host"
                                  value={params["h2-opts"]?.host || ""}
                                  classNames={inputClassNames}
                                  onChange={(e: InputChangeEvent) =>
                                    updateNestedParam("h2-opts", "host", e.target.value)
                                  }
                                />
                                <ExitInput
                                  size="sm"
                                  label="HTTP/2 Path"
                                  value={params["h2-opts"]?.path || ""}
                                  classNames={inputClassNames}
                                  onChange={(e: InputChangeEvent) =>
                                    updateNestedParam("h2-opts", "path", e.target.value)
                                  }
                                />
                              </>
                            ) : null}
                          </div>
                        )}
                      </div>
                    )}
                  </div>

                  <div className="space-y-4">
                    <div className="exit-section">
                      <div className="exit-section-title">代理链</div>
                      <div className="grid grid-cols-1 gap-3">
                        <ExitInput
                          size="sm"
                          label="跳板代理"
                          placeholder="不使用"
                          value={params["proxy-chain"] || ""}
                          classNames={inputClassNames}
                          onChange={(e: InputChangeEvent) => updateParam("proxy-chain", e.target.value)}
                        />
                        <ExitInput
                          size="sm"
                          label="覆盖默认测试 URL"
                          placeholder="http://"
                          value={params["test-url"] || ""}
                          classNames={inputClassNames}
                          onChange={(e: InputChangeEvent) => updateParam("test-url", e.target.value)}
                        />
                      </div>
                    </div>

                    <div className="exit-section">
                      <div className="exit-section-title">出口设置</div>
                      <div className="grid grid-cols-1 gap-3">
                        <Button
                          size="sm"
                          variant="flat"
                          className="exit-pill-button"
                          onPress={() => updateParam("interface", params.interface || "")}
                        >
                          指定网络接口…
                        </Button>
                        <div className="grid grid-cols-1 md:grid-cols-2 gap-3">
                          <ExitInput
                            size="sm"
                            label="IP 包 TOS"
                            placeholder="默认"
                            value={params.tos || ""}
                            classNames={inputClassNames}
                            onChange={(e: InputChangeEvent) => updateParam("tos", e.target.value)}
                          />
                          <ExitSelect
                            size="sm"
                            label="IP 版本"
                            selectedKeys={
                              params["ip-version"]
                                ? new Set([String(params["ip-version"])])
                                : new Set(["dual"])
                            }
                            classNames={selectClassNames}
                            onSelectionChange={(keys: SelectionKeys) =>
                              updateParam("ip-version", Array.from(keys)[0] || "dual")
                            }
                          >
                            {[
                              { key: "dual", label: "双栈" },
                              { key: "ipv4", label: "IPv4" },
                              { key: "ipv6", label: "IPv6" },
                            ].map((item) => (
                              <SelectItem key={item.key}>{item.label}</SelectItem>
                            ))}
                          </ExitSelect>
                        </div>
                        {[
                          "vmess",
                          "vless",
                          "trojan",
                          "anytls",
                          "hysteria2",
                          "hy2",
                          "tuic",
                          "tuicv5",
                        ].includes(protocol) ? (
                          <ExitInput
                            size="sm"
                            label="SNI (可选)"
                            placeholder="example.com"
                            value={params.sni || ""}
                            classNames={inputClassNames}
                            onChange={(e: InputChangeEvent) => updateParam("sni", e.target.value)}
                          />
                        ) : null}
                        {[
                          "vmess",
                          "vless",
                          "trojan",
                          "anytls",
                        ].includes(protocol) ? (
                          <Switch
                            className="w-full justify-between"
                            isSelected={!!params.tls}
                            onValueChange={(val) => updateParam("tls", val)}
                          >
                            TLS
                          </Switch>
                        ) : null}
                        {protocol === "vless" ? (
                          <Switch
                            className="w-full justify-between"
                            isSelected={!!params["reality-opts"]}
                            onValueChange={(val) =>
                              updateParam("reality-opts", val ? {} : undefined)
                            }
                          >
                            启用 Reality
                          </Switch>
                        ) : null}
                        {protocol === "vless" && params["reality-opts"] ? (
                          <>
                            <ExitInput
                              size="sm"
                              label="Reality 公钥"
                              value={params["reality-opts"]?.["public-key"] || ""}
                              classNames={inputClassNames}
                              onChange={(e: InputChangeEvent) =>
                                updateNestedParam(
                                  "reality-opts",
                                  "public-key",
                                  e.target.value,
                                )
                              }
                            />
                            <ExitInput
                              size="sm"
                              label="Short ID"
                              value={params["reality-opts"]?.["short-id"] || ""}
                              classNames={inputClassNames}
                              onChange={(e: InputChangeEvent) =>
                                updateNestedParam(
                                  "reality-opts",
                                  "short-id",
                                  e.target.value,
                                )
                              }
                            />
                            <ExitSelect
                              size="sm"
                              label="指纹"
                              selectedKeys={
                                params["reality-opts"]?.["fingerprint"]
                                  ? new Set([
                                      String(params["reality-opts"]?.["fingerprint"]),
                                    ])
                                  : new Set()
                              }
                              classNames={selectClassNames}
                              onSelectionChange={(keys: SelectionKeys) =>
                                updateNestedParam(
                                  "reality-opts",
                                  "fingerprint",
                                  Array.from(keys)[0] || "chrome",
                                )
                              }
                            >
                              {["chrome", "firefox", "safari", "ios", "edge"].map(
                                (item) => (
                                  <SelectItem key={item}>{item}</SelectItem>
                                ),
                              )}
                            </ExitSelect>
                          </>
                        ) : null}
                      </div>
                    </div>

                    <div className="exit-section">
                      <div className="exit-section-title">选项</div>
                      <div className="grid grid-cols-1 gap-2">
                        {[
                          "vmess",
                          "vless",
                          "trojan",
                          "anytls",
                          "hysteria2",
                          "hy2",
                          "tuic",
                          "tuicv5",
                        ].includes(protocol) ? (
                          <ExitInput
                            size="sm"
                            label="ALPN (可选)"
                            placeholder="h2,http/1.1"
                            value={alpnValue}
                            classNames={inputClassNames}
                            onChange={(e: InputChangeEvent) =>
                              updateParam(
                                "alpn",
                                e.target.value
                                  .split(",")
                                  .map((v: string) => v.trim())
                                  .filter((v: string) => v),
                              )
                            }
                          />
                        ) : null}
                        <div className="exit-option">
                          <Checkbox
                            isSelected={!!params.udp}
                            onValueChange={(val) => updateParam("udp", val)}
                            classNames={checkboxClassNames}
                          >
                            允许 UDP 转发
                          </Checkbox>
                          <div className="exit-option-desc">将 UDP 数据包转发到代理服务器（仅当协议支持）</div>
                        </div>
                        <div className="exit-option">
                          <Checkbox
                            isSelected={!!params["tcp-fast-open"]}
                            onValueChange={(val) => updateParam("tcp-fast-open", val)}
                            classNames={checkboxClassNames}
                          >
                            TCP Fast Open（实验性）
                          </Checkbox>
                          <div className="exit-option-desc">减少握手延迟以提升连接速度</div>
                        </div>
                        <div className="exit-option">
                          <Checkbox
                            isSelected={!!params["skip-cert-verify"]}
                            onValueChange={(val) => updateParam("skip-cert-verify", val)}
                            classNames={checkboxClassNames}
                          >
                            不校验证书
                          </Checkbox>
                          <div className="exit-option-desc">当证书链无法验证时继续连接</div>
                        </div>
                        {protocol === "tuic" || protocol === "tuicv5" ? (
                          <>
                            <Checkbox
                              isSelected={!!params["reduce-rtt"]}
                              onValueChange={(val) => updateParam("reduce-rtt", val)}
                              className="exit-checkbox"
                            >
                              Reduce RTT
                            </Checkbox>
                            <Checkbox
                              isSelected={!!params["disable-sni"]}
                              onValueChange={(val) => updateParam("disable-sni", val)}
                              className="exit-checkbox"
                            >
                              Disable SNI
                            </Checkbox>
                          </>
                        ) : null}
                      </div>
                    </div>
                  </div>
                </div>
              </ModalBody>
              <ModalFooter className="exit-footer">
                <div className="flex items-center gap-2">
                  <Button size="sm" variant="flat" isDisabled>
                    More Settings...
                  </Button>
                  <Button size="sm" variant="flat" isDisabled>
                    测试代理
                  </Button>
                </div>
                <div className="flex items-center gap-2">
                  <Button size="sm" variant="light" onPress={onClose}>
                    取消
                  </Button>
                  <Button size="sm" color="primary" isLoading={saving} onPress={handleSave}>
                    {form.id ? "完成" : "创建"}
                  </Button>
                </div>
              </ModalFooter>
            </>
          )}
        </ModalContent>
      </Modal>

      <Modal
        backdrop="opaque"
        disableAnimation
        isOpen={pickerOpen}
        placement="center"
        size="md"
        onOpenChange={setPickerOpen}
      >
        <ModalContent className="exit-picker-modal">
          {(onClose) => (
            <>
              <ModalHeader className="exit-picker-header">
                选择{pickerTitle}
              </ModalHeader>
              <ModalBody className="exit-picker-body">
                <div className="exit-picker-list">
                  {pickerOptions.map((item) => (
                    <button
                      key={item.value}
                      type="button"
                      className={`exit-picker-item ${
                        item.value === pickerValue ? "is-active" : ""
                      }`}
                      onClick={() => {
                        pickerOnSelectRef.current?.(item.value);
                        onClose();
                      }}
                    >
                      {item.label}
                    </button>
                  ))}
                </div>
              </ModalBody>
            </>
          )}
        </ModalContent>
      </Modal>
    </div>
  );
}
