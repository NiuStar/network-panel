import { useState, useEffect, useRef, useCallback, memo } from "react";
import { useNavigate } from "react-router-dom";
import { Card, CardBody, CardHeader } from "@heroui/card";
import { Button } from "@heroui/button";
import { Input } from "@heroui/input";
import { Select, SelectItem } from "@heroui/select";
import { Textarea } from "@heroui/input";
import {
  Modal,
  ModalContent,
  ModalHeader,
  ModalBody,
  ModalFooter,
} from "@heroui/modal";
import { Chip } from "@heroui/chip";
import { Spinner } from "@heroui/spinner";
import { Alert } from "@heroui/alert";
import { Progress } from "@heroui/progress";
import { Tooltip } from "@heroui/tooltip";
import { Terminal } from "xterm";

import "xterm/css/xterm.css";
import { Divider } from "@heroui/divider";
import toast from "react-hot-toast";

import OpsLogModal from "@/components/OpsLogModal";
import VirtualGrid from "@/components/VirtualGrid";
import {
  queryNodeServices,
  getNodeNetworkStatsBatch,
  getVersionInfo,
} from "@/api";
import { getCachedConfig } from "@/config/site";
import { usePageVisibility } from "@/hooks/usePageVisibility";
import {
  createNode,
  getNodeList,
  updateNode,
  deleteNode,
  getNodeInstallCommand,
  getNodeConnections,
  nodeSelfCheck,
  setExitNode,
  getExitNode,
  restartGost,
  agentReconcileNode,
  enableGostApi,
  getGostConfig,
  runNQTest,
  getNQResult,
} from "@/api";

interface Node {
  id: number;
  name: string;
  ip: string;
  serverIp: string;
  portSta: number;
  portEnd: number;
  version?: string;
  usedPorts?: number[];
  status: number; // 1: 在线, 0: 离线
  connectionStatus: "online" | "offline";
  priceCents?: number;
  cycleMonths?: number;
  startDateMs?: number;
  systemInfo?: {
    cpuUsage: number;
    memoryUsage: number;
    uploadTraffic: number;
    downloadTraffic: number;
    uploadSpeed: number;
    downloadSpeed: number;
    uptime: number;
    gostApi?: boolean;
    gostRunning?: boolean;
    gostApiConfigured?: boolean;
  } | null;
  copyLoading?: boolean;
  ssStatus?: string;
  ssLoading?: boolean;
}

interface NodeForm {
  id: number | null;
  name: string;
  ipString: string;
  serverIp: string;
  portSta: number;
  portEnd: number;
}

type InstallCommands = {
  static?: string;
  github?: string;
  local?: string;
};

const EXIT_METHODS = [
  "AEAD_CHACHA20_POLY1305",
  "chacha20-ietf-poly1305",
  "AEAD_AES_128_GCM",
  "AEAD_AES_256_GCM",
];
const EXIT_TYPES = [
  { key: "ss", label: "Shadowsocks (SS)" },
  { key: "anytls", label: "AnyTLS" },
];

const PERIOD_OPTIONS = [
  { key: "1", label: "月" },
  { key: "3", label: "季度" },
  { key: "6", label: "半年" },
  { key: "12", label: "年" },
];

const addMonths = (ts: number, months: number): number => {
  const d = new Date(ts);
  const day = d.getDate();
  const targetMonth = d.getMonth() + months;
  const y = d.getFullYear() + Math.floor(targetMonth / 12);
  const m = ((targetMonth % 12) + 12) % 12;
  const lastDay = new Date(y, m + 1, 0).getDate();
  const newDay = Math.min(day, lastDay);
  const nd = new Date(
    y,
    m,
    newDay,
    d.getHours(),
    d.getMinutes(),
    d.getSeconds(),
    d.getMilliseconds(),
  );

  return nd.getTime();
};

const computeNextExpire = (start?: number, cycle?: number): number | null => {
  if (!start || !cycle) return null;
  let months = 0;

  switch (cycle) {
    case 30:
      months = 1;
      break;
    case 90:
      months = 3;
      break;
    case 180:
      months = 6;
      break;
    case 365:
      months = 12;
      break;
    default:
      months = 0;
      break;
  }
  if (months > 0) {
    let exp = addMonths(start, months);
    const now = Date.now();

    while (exp <= now) exp = addMonths(exp, months);

    return exp;
  }
  const cycleMs = cycle * 24 * 3600 * 1000;
  const now = Date.now();

  if (now <= start) return start + cycleMs;
  const elapsed = now - start;
  const k = Math.ceil(elapsed / cycleMs);

  return start + k * cycleMs;
};

type NodeEditModalProps = {
  isOpen: boolean;
  onOpenChange: (open: boolean) => void;
  editNode: Node | null;
  onSaved: () => void;
};

const DEFAULT_NODE_FORM: NodeForm = {
  id: null,
  name: "",
  ipString: "",
  serverIp: "",
  portSta: 1000,
  portEnd: 65535,
};

const NodeEditModal = memo(
  ({ isOpen, onOpenChange, editNode, onSaved }: NodeEditModalProps) => {
    const isEdit = !!editNode;
    const [form, setForm] = useState<NodeForm>(DEFAULT_NODE_FORM);
    const [errors, setErrors] = useState<Record<string, string>>({});
    const [submitLoading, setSubmitLoading] = useState(false);
    const [priceCents, setPriceCents] = useState<number | undefined>(undefined);
    const [cycleMonths, setCycleMonths] = useState<number | undefined>(
      undefined,
    );
    const [startDateMs, setStartDateMs] = useState<number | undefined>(
      undefined,
    );

    useEffect(() => {
      if (!isOpen) return;
      setErrors({});
      if (editNode) {
        setForm({
          id: editNode.id,
          name: editNode.name,
          ipString: editNode.ip
            ? editNode.ip
                .split(",")
                .map((ip) => ip.trim())
                .join("\n")
            : "",
          serverIp: editNode.serverIp || "",
          portSta: editNode.portSta,
          portEnd: editNode.portEnd,
        });
        setPriceCents(editNode.priceCents);
        setCycleMonths(editNode.cycleMonths);
        setStartDateMs(editNode.startDateMs);
      } else {
        setForm(DEFAULT_NODE_FORM);
        setPriceCents(undefined);
        setCycleMonths(undefined);
        setStartDateMs(undefined);
      }
    }, [editNode, isOpen]);

    const validateIp = (ip: string): boolean => {
      if (!ip || !ip.trim()) return false;
      const trimmedIp = ip.trim();

      const ipv4Regex =
        /^(25[0-5]|2[0-4][0-9]|[01]?[0-9][0-9]?)\.(25[0-5]|2[0-4][0-9]|[01]?[0-9][0-9]?)\.(25[0-5]|2[0-4][0-9]|[01]?[0-9][0-9]?)\.(25[0-5]|2[0-4][0-9]|[01]?[0-9][0-9]?)$/;
      const ipv6Regex =
        /^(([0-9a-fA-F]{1,4}:){7,7}[0-9a-fA-F]{1,4}|([0-9a-fA-F]{1,4}:){1,7}:|([0-9a-fA-F]{1,4}:){1,6}:[0-9a-fA-F]{1,4}|([0-9a-fA-F]{1,4}:){1,5}(:[0-9a-fA-F]{1,4}){1,2}|([0-9a-fA-F]{1,4}:){1,4}(:[0-9a-fA-F]{1,4}){1,3}|([0-9a-fA-F]{1,4}:){1,3}(:[0-9a-fA-F]{1,4}){1,4}|([0-9a-fA-F]{1,4}:){1,2}(:[0-9a-fA-F]{1,4}){1,5}|[0-9a-fA-F]{1,4}:((:[0-9a-fA-F]{1,4}){1,6})|:((:[0-9a-fA-F]{1,4}){1,7}|:)|fe80:(:[0-9a-fA-F]{0,4}){0,4}%[0-9a-zA-Z]{1,}|::(ffff(:0{1,4}){0,1}:){0,1}((25[0-5]|(2[0-4]|1{0,1}[0-9]){0,1}[0-9])\.){3,3}(25[0-5]|(2[0-4]|1{0,1}[0-9]){0,1}[0-9])|([0-9a-fA-F]{1,4}:){1,4}:((25[0-5]|(2[0-4]|1{0,1}[0-9]){0,1}[0-9])\.){3,3}(25[0-5]|(2[0-4]|1{0,1}[0-9]){0,1}[0-9]))$/;

      if (
        ipv4Regex.test(trimmedIp) ||
        ipv6Regex.test(trimmedIp) ||
        trimmedIp === "localhost"
      ) {
        return true;
      }

      if (/^\d+$/.test(trimmedIp)) return false;

      const domainRegex =
        /^[a-zA-Z0-9]([a-zA-Z0-9-]{0,61}[a-zA-Z0-9])?(\.[a-zA-Z0-9]([a-zA-Z0-9-]{0,61}[a-zA-Z0-9])?)+$/;
      const singleLabelDomain = /^[a-zA-Z][a-zA-Z0-9-]{0,62}$/;

      return domainRegex.test(trimmedIp) || singleLabelDomain.test(trimmedIp);
    };

    const validateForm = (): boolean => {
      const newErrors: Record<string, string> = {};

      if (!form.name.trim()) {
        newErrors.name = "请输入节点名称";
      } else if (form.name.trim().length < 2) {
        newErrors.name = "节点名称长度至少2位";
      } else if (form.name.trim().length > 50) {
        newErrors.name = "节点名称长度不能超过50位";
      }

      if (!form.ipString.trim()) {
        newErrors.ipString = "请输入入口IP地址";
      } else {
        const ips = form.ipString
          .split("\n")
          .map((ip) => ip.trim())
          .filter((ip) => ip);

        if (ips.length === 0) {
          newErrors.ipString = "请输入至少一个有效IP地址";
        } else {
          for (let i = 0; i < ips.length; i++) {
            if (!validateIp(ips[i])) {
              newErrors.ipString = `第${i + 1}行IP地址格式错误: ${ips[i]}`;
              break;
            }
          }
        }
      }

      if (!form.serverIp.trim()) {
        newErrors.serverIp = "请输入服务器IP地址";
      } else if (!validateIp(form.serverIp.trim())) {
        newErrors.serverIp = "请输入有效的IPv4、IPv6地址或域名";
      }

      if (!form.portSta || form.portSta < 1 || form.portSta > 65535) {
        newErrors.portSta = "端口范围必须在1-65535之间";
      }

      if (!form.portEnd || form.portEnd < 1 || form.portEnd > 65535) {
        newErrors.portEnd = "端口范围必须在1-65535之间";
      } else if (form.portEnd < form.portSta) {
        newErrors.portEnd = "结束端口不能小于起始端口";
      }

      setErrors(newErrors);

      return Object.keys(newErrors).length === 0;
    };

    const handleSubmit = async () => {
      if (!validateForm()) return;

      setSubmitLoading(true);
      try {
        const ipString = form.ipString
          .split("\n")
          .map((ip) => ip.trim())
          .filter((ip) => ip)
          .join(",");

        const submitData: any = {
          ...form,
          ip: ipString,
        };

        delete (submitData as any).ipString;
        if (priceCents != null) submitData.priceCents = priceCents;
        if (cycleMonths != null) submitData.cycleMonths = cycleMonths;
        if (startDateMs != null) submitData.startDateMs = startDateMs;

        const apiCall = isEdit ? updateNode : createNode;
        const data: any = isEdit
          ? submitData
          : {
              name: form.name,
              ip: ipString,
              serverIp: form.serverIp,
              portSta: form.portSta,
              portEnd: form.portEnd,
            };

        if (!isEdit) {
          if (priceCents != null) data.priceCents = priceCents;
          if (cycleMonths != null) data.cycleMonths = cycleMonths;
          if (startDateMs != null) data.startDateMs = startDateMs;
        }

        const res = await apiCall(data);

        if (res.code === 0) {
          toast.success(isEdit ? "更新成功" : "创建成功");
          onOpenChange(false);
          onSaved();
        } else {
          toast.error(res.msg || (isEdit ? "更新失败" : "创建失败"));
        }
      } catch (error) {
        toast.error("网络错误，请重试");
      } finally {
        setSubmitLoading(false);
      }
    };

    return (
      <Modal
        backdrop="opaque"
        disableAnimation
        isOpen={isOpen}
        placement="center"
        scrollBehavior="outside"
        size="2xl"
        onClose={() => onOpenChange(false)}
      >
        <ModalContent>
          <ModalHeader>{isEdit ? "编辑节点" : "新增节点"}</ModalHeader>
          <ModalBody>
            <div className="space-y-4">
              <Input
                errorMessage={errors.name}
                isInvalid={!!errors.name}
                label="节点名称"
                placeholder="请输入节点名称"
                value={form.name}
                variant="bordered"
                onChange={(e) =>
                  setForm((prev) => ({ ...prev, name: e.target.value }))
                }
              />

              <Input
                errorMessage={errors.serverIp}
                isInvalid={!!errors.serverIp}
                label="服务器IP"
                placeholder="请输入服务器IP地址，如: 192.168.1.100 或 example.com"
                value={form.serverIp}
                variant="bordered"
                onChange={(e) =>
                  setForm((prev) => ({ ...prev, serverIp: e.target.value }))
                }
              />

              <Textarea
                description="支持多个IP，每行一个地址"
                errorMessage={errors.ipString}
                isInvalid={!!errors.ipString}
                label="入口IP"
                maxRows={5}
                minRows={3}
                placeholder="一行一个IP地址或域名，例如:&#10;192.168.1.100&#10;example.com"
                value={form.ipString}
                variant="bordered"
                onChange={(e) =>
                  setForm((prev) => ({ ...prev, ipString: e.target.value }))
                }
              />

              <div className="grid grid-cols-2 gap-4">
                <Input
                  errorMessage={errors.portSta}
                  isInvalid={!!errors.portSta}
                  label="起始端口"
                  max={65535}
                  min={1}
                  placeholder="1000"
                  type="number"
                  value={form.portSta.toString()}
                  variant="bordered"
                  onChange={(e) =>
                    setForm((prev) => ({
                      ...prev,
                      portSta: parseInt(e.target.value) || 1000,
                    }))
                  }
                />

                <Input
                  errorMessage={errors.portEnd}
                  isInvalid={!!errors.portEnd}
                  label="结束端口"
                  max={65535}
                  min={1}
                  placeholder="65535"
                  type="number"
                  value={form.portEnd.toString()}
                  variant="bordered"
                  onChange={(e) =>
                    setForm((prev) => ({
                      ...prev,
                      portEnd: parseInt(e.target.value) || 65535,
                    }))
                  }
                />
              </div>

              <div className="grid grid-cols-3 gap-4">
                <Input
                  label="价格(元)"
                  placeholder="可选"
                  type="number"
                  value={
                    priceCents != null ? (priceCents / 100).toString() : ""
                  }
                  variant="bordered"
                  onChange={(e) => {
                    const v = parseFloat((e.target as any).value);

                    setPriceCents(isNaN(v) ? undefined : Math.round(v * 100));
                  }}
                />
                <Select
                  label="周期"
                  selectedKeys={
                    cycleMonths ? new Set([String(cycleMonths)]) : new Set()
                  }
                  variant="bordered"
                  onChange={(e) => {
                    const v = parseInt((e.target as any).value);

                    setCycleMonths(isNaN(v) ? undefined : v);
                  }}
                >
                  {PERIOD_OPTIONS.map((opt) => (
                    <SelectItem key={opt.key}>{opt.label}</SelectItem>
                  ))}
                </Select>
                <Input
                  label="开始日期"
                  type="date"
                  value={
                    startDateMs
                      ? new Date(startDateMs).toISOString().slice(0, 10)
                      : ""
                  }
                  variant="bordered"
                  onChange={(e) => {
                    const s = (e.target as any).value;

                    setStartDateMs(
                      s ? new Date(s + "T00:00:00").getTime() : undefined,
                    );
                  }}
                />
              </div>

              <div className="text-xs text-default-600">
                {(() => {
                  const exp = computeNextExpire(startDateMs, cycleMonths);

                  if (!exp) return "到期时间：-";
                  const daysLeft = Math.max(
                    0,
                    Math.ceil((exp - Date.now()) / (24 * 3600 * 1000)),
                  );
                  const dt = new Date(exp);
                  const yyyy = dt.getFullYear();
                  const mm = String(dt.getMonth() + 1).padStart(2, "0");
                  const dd = String(dt.getDate()).padStart(2, "0");

                  return `到期时间：${yyyy}-${mm}-${dd}（剩余 ${daysLeft} 天）`;
                })()}
              </div>

              <Alert
                className="mt-4"
                color="primary"
                description="服务器ip是你要添加的服务器的ip地址，不是面板的ip地址。入口ip是用于展示在转发页面，面向用户的访问地址。实在理解不到说明你没这个需求，都填节点的服务器ip就行！"
                variant="flat"
              />
            </div>
          </ModalBody>
          <ModalFooter>
            <Button variant="flat" onPress={() => onOpenChange(false)}>
              取消
            </Button>
            <Button
              color="primary"
              isLoading={submitLoading}
              onPress={handleSubmit}
            >
              {submitLoading ? "提交中..." : "确定"}
            </Button>
          </ModalFooter>
        </ModalContent>
      </Modal>
    );
  },
);

type ExitServiceModalProps = {
  isOpen: boolean;
  onOpenChange: (open: boolean) => void;
  node: Node | null;
};

const ExitServiceModal = memo(
  ({ isOpen, onOpenChange, node }: ExitServiceModalProps) => {
    const [exitType, setExitType] = useState<string>("ss");
    const [exitPort, setExitPort] = useState<number>(10000);
    const [exitPassword, setExitPassword] = useState<string>("");
    const [exitMethod, setExitMethod] = useState<string>(EXIT_METHODS[0]);
    const [exitSubmitting, setExitSubmitting] = useState(false);
    const [exitObserver, setExitObserver] = useState<string>("console");
    const [exitLimiter, setExitLimiter] = useState<string>("");
    const [exitRLimiter, setExitRLimiter] = useState<string>("");
    const [exitMetaItems, setExitMetaItems] = useState<
      Array<{ id: number; key: string; value: string }>
    >([]);
    const [exitIfaces, setExitIfaces] = useState<string[]>([]);
    const [exitIfaceSel, setExitIfaceSel] = useState<string>("");
    const lastLoadedExitTypeRef = useRef<string>("");

    const resetDefaults = useCallback(
      (type: string) => {
        setExitPort(node?.portSta || 10000);
        setExitPassword("");
        setExitMethod(EXIT_METHODS[0]);
        setExitObserver("console");
        setExitLimiter("");
        setExitRLimiter("");
        setExitMetaItems([]);
        setExitIfaceSel("");
        if (type === "anytls") {
          setExitIfaces([]);
        }
      },
      [node],
    );

    const loadExitConfig = useCallback(
      async (type: string) => {
        if (!node?.id) return;
        resetDefaults(type);
        lastLoadedExitTypeRef.current = type;

        let dPort = node.portSta || 10000;
        let dPwd = "";
        let dMethod = EXIT_METHODS[0];
        let dObserver = "console";
        let dLimiter = "";
        let dRLimiter = "";
        let dMetaItems: Array<{ id: number; key: string; value: string }> = [];
        let dIfaceSel = "";

        try {
          const res = await getExitNode(node.id, type);

          if (res.code === 0 && res.data) {
            const data = res.data as any;

            if (typeof data.port === "number") dPort = data.port;
            if (typeof data.password === "string") dPwd = data.password;
            if (type === "ss") {
              if (typeof data.method === "string" && data.method)
                dMethod = data.method;
              if (typeof data.observer === "string")
                dObserver = data.observer || dObserver;
              if (typeof data.limiter === "string") dLimiter = data.limiter || "";
              if (typeof data.rlimiter === "string")
                dRLimiter = data.rlimiter || "";
              if (data.metadata && typeof data.metadata === "object") {
                dMetaItems = Object.entries(data.metadata).map(([k, v]) => ({
                  id: Date.now() + Math.random(),
                  key: String(k),
                  value: String(v),
                }));
                if (typeof (data.metadata as any).interface === "string") {
                  dIfaceSel = String((data.metadata as any).interface);
                }
              }
            }
          }
        } catch {}

        setExitPort(dPort);
        setExitPassword(dPwd);
        setExitMethod(dMethod);
        setExitObserver(dObserver);
        setExitLimiter(dLimiter);
        setExitRLimiter(dRLimiter);
        setExitMetaItems(dMetaItems);
        setExitIfaceSel(dIfaceSel);
      },
      [node, resetDefaults],
    );

    useEffect(() => {
      if (!isOpen || !node) return;
      let active = true;

      setExitType("ss");
      lastLoadedExitTypeRef.current = "";
      void loadExitConfig("ss");

      (async () => {
        try {
          const { getNodeInterfaces } = await import("@/api");
          const rr: any = await getNodeInterfaces(node.id);
          const ips =
            rr && rr.code === 0 && Array.isArray(rr.data?.ips)
              ? (rr.data.ips as string[])
              : [];

          if (active) setExitIfaces(ips);
        } catch {
          if (active) setExitIfaces([]);
        }
      })();

      return () => {
        active = false;
      };
    }, [isOpen, node, loadExitConfig]);

    useEffect(() => {
      if (!isOpen || !node) return;
      if (lastLoadedExitTypeRef.current === exitType) return;
      void loadExitConfig(exitType);
    }, [exitType, isOpen, node, loadExitConfig]);

    const submitExit = async () => {
      if (!node?.id) {
        toast.error("无效的节点");

        return;
      }
      if (!exitPort || exitPort < 1 || exitPort > 65535) {
        toast.error("端口无效");

        return;
      }
      if (!exitPassword) {
        toast.error("请填写密码");

        return;
      }
      setExitSubmitting(true);
      try {
        let res;

        if (exitType === "anytls") {
          res = await setExitNode({
            nodeId: node.id,
            type: "anytls",
            port: exitPort,
            password: exitPassword,
          } as any);
        } else {
          const metadata: any = {};

          exitMetaItems.forEach((it: { key: string; value: string }) => {
            if (it.key && it.value) metadata[it.key] = it.value;
          });
          if (exitIfaceSel) {
            (metadata as any)["interface"] = exitIfaceSel;
          }
          res = await setExitNode({
            nodeId: node.id,
            type: "ss",
            port: exitPort,
            password: exitPassword,
            method: exitMethod,
            observer: exitObserver,
            limiter: exitLimiter,
            rlimiter: exitRLimiter,
            metadata,
          } as any);
        }

        if (res.code === 0) {
          toast.success(exitType === "anytls" ? "AnyTLS 已创建/更新" : "出口服务已创建/更新");
          onOpenChange(false);
        } else {
          toast.error(res.msg || "操作失败");
        }
      } catch (e) {
        toast.error("网络错误");
      } finally {
        setExitSubmitting(false);
      }
    };

    return (
      <Modal
        backdrop="opaque"
        disableAnimation
        isOpen={isOpen}
        size="md"
        onOpenChange={onOpenChange}
      >
        <ModalContent>
          {(onClose) => (
            <>
              <ModalHeader>
                设置出口节点服务{node?.name ? ` · ${node.name}` : ""}
              </ModalHeader>
              <ModalBody>
                <div className="space-y-3">
                  <Select
                    label="出口类型"
                    selectedKeys={[exitType]}
                    onSelectionChange={(keys) => {
                      const val = Array.from(keys as Set<string>)[0] || "ss";

                      setExitType(val);
                    }}
                  >
                    {EXIT_TYPES.map((t) => (
                      <SelectItem key={t.key} textValue={t.label}>
                        {t.label}
                      </SelectItem>
                    ))}
                  </Select>
                  {exitType === "anytls" && (
                    <Alert
                      color="primary"
                      description="AnyTLS 将自动生成自签证书，客户端默认不校验即可使用。"
                      variant="flat"
                    />
                  )}
                  <Input
                    label="端口"
                    type="number"
                    value={String(exitPort)}
                    onChange={(e: any) => setExitPort(Number(e.target.value))}
                  />
                  <Input
                    label="密码"
                    type="text"
                    value={exitPassword}
                    onChange={(e: any) => setExitPassword(e.target.value)}
                  />
                  {exitType === "ss" && (
                    <>
                      <Select
                        label="加密方法"
                        selectedKeys={[exitMethod]}
                        description="选择 Shadowsocks 加密方法"
                        onSelectionChange={(keys) => {
                          const val = Array.from(keys as Set<string>)[0] || "";

                          if (val) setExitMethod(val);
                        }}
                      >
                        {EXIT_METHODS.map((m) => (
                          <SelectItem key={m} textValue={m}>
                            {m}
                          </SelectItem>
                        ))}
                      </Select>
                      <div>
                        <div className="text-sm text-default-600 mb-1">
                          出口IP（metadata.interface，可选）
                        </div>
                        <Select
                          isDisabled={exitIfaces.length === 0}
                          label="请选择出口IP"
                          placeholder={
                            exitIfaces.length
                              ? "选择出口IP"
                              : "未获取到出口IP列表"
                          }
                          selectedKeys={exitIfaceSel ? [exitIfaceSel] : []}
                          onSelectionChange={(keys) => {
                            const val = Array.from(keys as Set<string>)[0] || "";

                            setExitIfaceSel(val);
                          }}
                        >
                          {exitIfaces.map((ip) => (
                            <SelectItem key={ip}>{ip}</SelectItem>
                          ))}
                        </Select>
                        {exitIfaceSel && (
                          <Button
                            className="mt-2"
                            size="sm"
                            variant="light"
                            onPress={() => setExitIfaceSel("")}
                          >
                            清除选择
                          </Button>
                        )}
                      </div>
                      <Divider />
                      <Input
                        description="默认 console，可留空"
                        label="观察器(observer)"
                        value={exitObserver}
                        onChange={(e: any) => setExitObserver(e.target.value)}
                      />
                      <Input
                        description="可选，需在节点注册对应限速器"
                        label="限速(limiter)"
                        value={exitLimiter}
                        onChange={(e: any) => setExitLimiter(e.target.value)}
                      />
                      <Input
                        description="可选，需在节点注册对应限速器"
                        label="连接限速(rlimiter)"
                        value={exitRLimiter}
                        onChange={(e: any) => setExitRLimiter(e.target.value)}
                      />
                      <Divider />
                      <div className="space-y-2">
                        <div className="flex items-center justify-between">
                          <span className="text-sm text-default-600">
                            handler.metadata
                          </span>
                          <Button
                            size="sm"
                            variant="flat"
                            onPress={() =>
                              setExitMetaItems((prev) => [
                                ...prev,
                                { id: Date.now(), key: "", value: "" },
                              ])
                            }
                          >
                            添加
                          </Button>
                        </div>
                        {exitMetaItems.map(
                          (it: { id: number; key: string; value: string }) => (
                            <div
                              key={it.id}
                              className="grid grid-cols-5 gap-2 items-center"
                            >
                              <Input
                                className="col-span-2"
                                placeholder="key"
                                value={it.key}
                                onChange={(e: any) =>
                                  setExitMetaItems(
                                    (
                                      prev: Array<{
                                        id: number;
                                        key: string;
                                        value: string;
                                      }>,
                                    ) =>
                                      prev.map((x: any) =>
                                        x.id === it.id
                                          ? { ...x, key: e.target.value }
                                          : x,
                                      ),
                                  )
                                }
                              />
                              <Input
                                className="col-span-3"
                                placeholder="value"
                                value={it.value}
                                onChange={(e: any) =>
                                  setExitMetaItems(
                                    (
                                      prev: Array<{
                                        id: number;
                                        key: string;
                                        value: string;
                                      }>,
                                    ) =>
                                      prev.map((x: any) =>
                                        x.id === it.id
                                          ? { ...x, value: e.target.value }
                                          : x,
                                      ),
                                  )
                                }
                              />
                              <Button
                                color="danger"
                                size="sm"
                                variant="light"
                                onPress={() =>
                                  setExitMetaItems(
                                    (
                                      prev: Array<{
                                        id: number;
                                        key: string;
                                        value: string;
                                      }>,
                                    ) => prev.filter((x: any) => x.id !== it.id),
                                  )
                                }
                              >
                                删除
                              </Button>
                            </div>
                          ),
                        )}
                      </div>
                    </>
                  )}
                </div>
              </ModalBody>
              <ModalFooter>
                <Button variant="light" onPress={onClose}>
                  关闭
                </Button>
                <Button
                  color="primary"
                  isLoading={exitSubmitting}
                  onPress={submitExit}
                >
                  保存
                </Button>
              </ModalFooter>
            </>
          )}
        </ModalContent>
      </Modal>
    );
  },
);

export default function NodePage() {
  const navigate = useNavigate();
  const [nodeList, setNodeList] = useState<Node[]>([]);
  const [loading, setLoading] = useState(false);
  const gridWrapRef = useRef<HTMLDivElement | null>(null);
  const [nodeRowHeight, setNodeRowHeight] = useState(520);
  const [gridReady, setGridReady] = useState(false);
  const [dialogVisible, setDialogVisible] = useState(false);
  const [editNode, setEditNode] = useState<Node | null>(null);
  const [deleteModalOpen, setDeleteModalOpen] = useState(false);
  const [deleteLoading, setDeleteLoading] = useState(false);
  const [nodeToDelete, setNodeToDelete] = useState<Node | null>(null);
  const [deleteAlsoUninstall, setDeleteAlsoUninstall] = useState(false);
  const [exitNode, setExitNode] = useState<Node | null>(null);
  const [probeStat, setProbeStat] = useState<
    Record<
      number,
      {
        avg: number;
        latest: number | null;
        target?: { id: number; name?: string; ip?: string };
      }
    >
  >({});
  const [showNetwork, setShowNetwork] = useState(false);

  useEffect(() => {
    (async () => {
      try {
        const v = await getCachedConfig("show_network");

        setShowNetwork(v === "true");
      } catch {}
    })();
  }, []);

  // 出口服务设置
  const [exitModalOpen, setExitModalOpen] = useState(false);

  // 安装命令相关状态
  const [installCommandModal, setInstallCommandModal] = useState(false);
  const [installCommands, setInstallCommands] =
    useState<InstallCommands | null>(null);
  const [currentNodeName, setCurrentNodeName] = useState("");
  const [gostConfigModal, setGostConfigModal] = useState<{
    open: boolean;
    loading: boolean;
    content: string;
    title: string;
  }>({ open: false, loading: false, content: "", title: "" });
  const [nqLoading, setNqLoading] = useState<Record<number, boolean>>({});
  const [nqResultCache, setNqResultCache] = useState<
    Record<number, { content: string; timeMs: number | null }>
  >({});
  const [nqModal, setNqModal] = useState<{
    open: boolean;
    title: string;
    content: string;
    timeMs: number | null;
    loading: boolean;
    nodeId: number | null;
    done?: boolean;
  }>({
    open: false,
    title: "",
    content: "",
    timeMs: null,
    loading: false,
    nodeId: null,
    done: false,
  });
  const [nqHasResult, setNqHasResult] = useState<Record<number, boolean>>({});
  const [usedPortsModal, setUsedPortsModal] = useState<{
    open: boolean;
    nodeName: string;
    ports: number[];
  }>({
    open: false,
    nodeName: "",
    ports: [],
  });
  const [connModal, setConnModal] = useState<{
    open: boolean;
    nodeName: string;
    loading: boolean;
    versions: string[];
  }>({
    open: false,
    nodeName: "",
    loading: false,
    versions: [],
  });
  const [selfCheckModal, setSelfCheckModal] = useState<{
    open: boolean;
    nodeName: string;
    loading: boolean;
    result: any | null;
  }>({
    open: false,
    nodeName: "",
    loading: false,
    result: null,
  });
  const logScrollRef = useRef<HTMLDivElement | null>(null);
  const termContainerRef = useRef<HTMLDivElement | null>(null);
  const termWSRef = useRef<WebSocket | null>(null);
  const termRef = useRef<Terminal | null>(null);
  const suspendRealtimeRef = useRef(false);
  const [termModal, setTermModal] = useState<{
    open: boolean;
    nodeId: number | null;
    nodeName: string;
    content: string;
    running: boolean;
    connecting: boolean;
  }>({
    open: false,
    nodeId: null,
    nodeName: "",
    content: "",
    running: false,
    connecting: false,
  });
  const isAdmin = (() => {
    const rid =
      localStorage.getItem("role_id") || localStorage.getItem("roleId");
    const adminFlag = localStorage.getItem("admin");

    return adminFlag === "true" || rid === "0";
  })();

  const anyModalOpen =
    dialogVisible ||
    exitModalOpen ||
    deleteModalOpen ||
    termModal.open ||
    gostConfigModal.open ||
    nqModal.open ||
    installCommandModal ||
    usedPortsModal.open;
  const scrollPosRef = useRef<number | null>(null);

  const getScrollEl = () => {
    if (typeof document === "undefined") return null;
    const main = document.querySelector("main");
    if (main) return main as HTMLElement;
    return (document.scrollingElement as HTMLElement) || null;
  };

  useEffect(() => {
    suspendRealtimeRef.current = anyModalOpen;
  }, [
    anyModalOpen,
  ]);

  useEffect(() => {
    if (loading || nodeList.length === 0) {
      setGridReady(true);
      return;
    }
    let active = true;
    const measure = () => {
      if (!active) return;
      const el = gridWrapRef.current?.querySelector<HTMLElement>(".node-card");
      if (el) {
        const h = el.getBoundingClientRect().height;
        if (h > 0) {
          const next = Math.ceil(h + 24);
          setNodeRowHeight((prev) => (next > prev ? next : prev));
        }
      }
      setGridReady(true);
    };
    const raf1 = requestAnimationFrame(() => {
      const raf2 = requestAnimationFrame(measure);
      if (!active) cancelAnimationFrame(raf2);
    });
    const t1 = setTimeout(measure, 220);
    return () => {
      active = false;
      cancelAnimationFrame(raf1);
      clearTimeout(t1);
    };
  }, [loading, nodeList.length]);

  useEffect(() => {
    const el = getScrollEl();
    if (!el) return;
    if (anyModalOpen) {
      scrollPosRef.current = el.scrollTop;
      return;
    }
    if (scrollPosRef.current == null) return;
    const pos = scrollPosRef.current;
    const raf = requestAnimationFrame(() => {
      el.scrollTop = pos;
    });
    return () => cancelAnimationFrame(raf);
  }, [anyModalOpen]);

  const websocketRef = useRef<WebSocket | null>(null);
  const reconnectTimerRef = useRef<NodeJS.Timeout | null>(null);
  const reconnectAttemptsRef = useRef(0);
  const maxReconnectAttempts = 5;
  const [wsStatus, setWsStatus] = useState<
    "connected" | "connecting" | "disconnected"
  >("connecting");
  const [wsUrlShown, setWsUrlShown] = useState<string>("");
  const [serverVersion, setServerVersion] = useState<string>("");
  const [agentVersion, setAgentVersion] = useState<string>("");
  const [opsOpen, setOpsOpen] = useState(false);
  const [rstLoading, setRstLoading] = useState<Record<number, boolean>>({});
  const [reapplyLoading, setReapplyLoading] = useState<Record<number, boolean>>(
    {},
  );
  const pageVisible = usePageVisibility();
  const [pollMs, setPollMs] = useState<number>(5000);

  useEffect(() => {
    loadNodes();
    initWebSocket();

    return () => {
      closeWebSocket();
      closeTermWS();
    };
  }, []);

  useEffect(() => {
    (async () => {
      try {
        const v = await getCachedConfig("poll_interval_sec");
        const n = Math.max(3, parseInt(String(v || "5"), 10));

        setPollMs(n * 1000);
      } catch {}
    })();
  }, []);

  useEffect(() => {
    let timer: any;
    const tick = async () => {
      if (!pageVisible) return;
      await loadNodes(true, false);
    };

    tick();
    timer = setInterval(tick, pollMs);

    return () => {
      if (timer) clearInterval(timer);
    };
  }, [pollMs, pageVisible]);

  // 模拟终端：处理 \r 覆盖、保留 ANSI 颜色
  // 终端相关
  const closeTermWS = () => {
    if (termWSRef.current) {
      termWSRef.current.close();
      termWSRef.current = null;
    }
  };

  const openTerminalWindow = (node: Node) => {
    const token = localStorage.getItem("token") || "";
    const proto = window.location.protocol === "https:" ? "wss" : "ws";
    const wsUrl = `${proto}://${window.location.host}/api/v1/node/${node.id}/terminal?token=${encodeURIComponent(token)}`;
    const html = `
<!DOCTYPE html>
<html>
<head>
  <meta charset="utf-8" />
  <meta name="viewport" content="width=device-width, initial-scale=1.0" />
  <title>终端 - ${node.name}</title>
  <link rel="stylesheet" href="https://cdn.jsdelivr.net/npm/xterm@5.3.0/css/xterm.css" />
  <style>
    html, body { margin:0; padding:0; width:100%; height:100%; background:#000; color:#d1d5db; font-family: monospace; }
    #layout { display:flex; width:100%; height:100%; padding:0; box-sizing:border-box; gap:6px; }
    #term-wrap { flex: 1 1 90%; background: ${localStorage.getItem("term_bg") || "#151729"}; border-radius:6px; overflow:hidden; }
    #term, .xterm { width:100% !important; height:100% !important; padding:4px; box-sizing:border-box; }
    #side { flex: 0 0 10%; min-width:200px; background:#111; color:#d1d5db; padding:10px; box-sizing:border-box; border-left:1px solid #222; font-size:12px; display:flex; flex-direction:column; gap:10px; transition: width 0.2s ease; }
    #side.hidden { display:none; }
    #side-toggle { position:absolute; top:8px; right:8px; z-index:10; padding:4px 6px; font-size:12px; }
    #log { margin-top:4px; max-height:60%; overflow:auto; white-space:pre-wrap; }
    .status-ok { color:#22c55e; }
    .status-bad { color:#ef4444; }
    .btn { background:#1e293b; color:#e5e7eb; border:1px solid #334155; padding:6px 10px; border-radius:4px; cursor:pointer; }
    .btn:hover { background:#0f172a; }
    .key-btn { background:#1f2937; color:#e5e7eb; border:1px solid #374151; padding:4px 6px; border-radius:4px; cursor:pointer; font-size:11px; }
    .key-btn:hover { background:#0f172a; }
    @media (max-width: 768px) {
      #layout { width:98%; height:98%; padding:1%; gap:6px; }
      #side { min-width:120px; font-size:11px; }
    }
  </style>
</head>
  <body>
  <div id="layout">
    <button id="side-toggle" class="btn" style="position:absolute; top:10px; right:10px;">》</button>
    <div id="term-wrap"><div id="term"></div></div>
    <div id="side">
      <div>
        <div><strong>连接状态</strong></div>
        <div id="status" class="status-bad">连接中...</div>
        <div style="margin-top:6px; display:flex; gap:6px; flex-wrap:wrap;">
          <button id="btn-reconnect" class="btn">重连 WS</button>
          <button id="btn-restart" class="btn">重开会话</button>
        </div>
      </div>
      <div>
        <div><strong>节点</strong></div>
        <div>${node.name}</div>
      </div>
      <div>
        <div><strong>资源</strong></div>
        <div id="stats">CPU -- | 内存 -- | 上行 -- | 下行 --</div>
      </div>
      <div style="flex:1 1 auto; display:flex; flex-direction:column;">
        <div><strong>日志</strong></div>
        <div id="log"></div>
      </div>
      <div>
        <div><strong>快捷键</strong></div>
        <div id="hotkeys" style="display:flex; flex-wrap:wrap; gap:6px; margin-top:6px;">
          <button class="key-btn" data-key="ctrl+c">Ctrl+C</button>
          <button class="key-btn" data-key="ctrl+v">Ctrl+V</button>
          <button class="key-btn" data-key="ctrl+d">Ctrl+D</button>
          <button class="key-btn" data-key="ctrl+z">Ctrl+Z</button>
          <button class="key-btn" data-key="ctrl+alt+a">Ctrl+Alt+A</button>
          <button class="key-btn" data-key="ctrl+l">Ctrl+L</button>
          <button class="key-btn" data-key="esc">Esc</button>
          <button class="key-btn" data-key="tab">Tab</button>
        </div>
      </div>
      <div>
        <div><strong>字号</strong></div>
        <div style="display:flex; gap:6px; margin-top:6px;">
          <button id="font-dec" class="btn" title="减小字体">A-</button>
          <button id="font-inc" class="btn" title="增大字体">A+</button>
        </div>
        <div id="font-info" style="margin-top:4px;">--</div>
      </div>
    </div>
  </div>
  <script type="module">
    import { Terminal } from "https://cdn.jsdelivr.net/npm/xterm@5.3.0/+esm";
    import { FitAddon } from "https://cdn.jsdelivr.net/npm/xterm-addon-fit@0.7.0/+esm";
    const logEl = document.getElementById("log");
    const statusEl = document.getElementById("status");
    const sideEl = document.getElementById("side");
    const statsEl = document.getElementById("stats");
    const fontInfoEl = document.getElementById("font-info");
    const setStatus = (msg, ok=false) => {
      statusEl.textContent = msg;
      statusEl.className = ok ? "status-ok" : "status-bad";
    };
    const addLog = (msg) => {
      const line = document.createElement("div");
      line.textContent = msg;
      logEl.appendChild(line);
      logEl.scrollTop = logEl.scrollHeight;
    };
    const fitAddon = new FitAddon();
    const isMobile = window.innerWidth <= 768;
    let fontSize = isMobile ? 16 : 13;
    let lineHeight = isMobile ? 2.2 : 1.2;
    const term = new Terminal({
      convertEol:true,
      cursorBlink:true,
      fontSize: fontSize,
      lineHeight: lineHeight,
      rendererType: isMobile ? "dom" : "canvas",
      fontFamily: 'Menlo, Consolas, "Courier New", monospace',
      theme:{
        background: "${localStorage.getItem("term_bg") || "#151729"}",
        foreground: "${localStorage.getItem("term_fg") || "#209d5f"}"
      },
      scrollback:2000
    });
    term.loadAddon(fitAddon);
    const termEl = document.getElementById("term");
    termEl.style.padding = "6px 6px";
    term.open(termEl);
    fitAddon.fit();
    term.focus();
    const applyFont = () => {
      term.options.fontSize = fontSize;
      term.options.lineHeight = lineHeight;
      fontInfoEl.textContent = fontSize + "px / " + lineHeight.toFixed(2);
      fitAddon.fit();
      if (ws && ws.readyState === WebSocket.OPEN) {
        ws.send(JSON.stringify({type:"resize", rows: term.rows, cols: term.cols}));
      }
      // persist config
      fetch("/api/v1/config/update", {
        method:"POST",
        headers: {"Content-Type":"application/json","Authorization": ${JSON.stringify(token)}},
        body: JSON.stringify({
          "term_font_size": String(fontSize),
          "term_line_height": String(lineHeight)
        })
      }).catch(()=>{});
    };
    const fetchConfigVal = async (name, defVal) => {
      try{
        const resp = await fetch("/api/v1/config/get", {
          method:"POST",
          headers: {"Content-Type":"application/json","Authorization": ${JSON.stringify(token)}},
          body: JSON.stringify({name})
        });
        const data = await resp.json();
        if (data.code === 0 && data.data) return data.data;
      }catch(e){}
      return defVal;
    };
    const loadFontConfig = async () => {
      const fv = await fetchConfigVal("term_font_size", fontSize);
      const lv = await fetchConfigVal("term_line_height", lineHeight);
      const fNum = parseInt(fv,10);
      const lNum = parseFloat(lv);
      if (!isNaN(fNum) && fNum>0) fontSize = fNum;
      if (!isNaN(lNum) && lNum>0) lineHeight = lNum;
      applyFont();
    };
    let ws = null;
    let onDataDispose = null;
    const sendResize = () => {
      fitAddon.fit();
      if (ws && ws.readyState === WebSocket.OPEN) {
        ws.send(JSON.stringify({type:"resize", rows: term.rows, cols: term.cols}));
      }
    };
    new ResizeObserver(()=>sendResize()).observe(document.getElementById("term-wrap"));
    const openWS = () => {
      if (ws) { try{ ws.close(); }catch(e){} }
      if (onDataDispose) { try{ onDataDispose.dispose(); }catch(e){} onDataDispose = null; }
      term.reset();
      setStatus("连接中...", false);
      ws = new WebSocket(${JSON.stringify(wsUrl)});
      ws.addEventListener("open", ()=>{ 
        setStatus("已连接", true); addLog("WS 已连接"); 
        sendResize(); 
        ws.send(JSON.stringify({type:"start", rows: term.rows, cols: term.cols})); 
      });
      let restartPending = false;
      let dataReceived = false;
      let timerCleared = false;
      const stopAndMaybeClose = () => {
        restartPending = true;
        try{ ws.send(JSON.stringify({type:"stop"})); }catch(e){}
        setTimeout(()=>{ if (restartPending && ws && ws.readyState === WebSocket.OPEN) { ws.close(); } }, 500);
      };
      const clearTimer = () => {
        if (!timerCleared) {
          clearTimeout(timer);
          timerCleared = true;
        }
      };
      const timer = setTimeout(()=>{
        if (!dataReceived) {
          stopAndMaybeClose();
        }
      }, 3000);
      // heartbeat ping to keep WS alive
      const hb = setInterval(()=>{
        if (ws && ws.readyState === WebSocket.OPEN) {
          try{ ws.send(JSON.stringify({type:"ping"})); }catch(e){}
        }
      }, 20000);
      ws.addEventListener("message", (ev)=>{
        try{
          const msg = JSON.parse(ev.data);
          if (msg.type === "history") { term.reset(); term.write(msg.data || ""); }
          else if (msg.type === "data") { dataReceived = true; clearTimer(); term.write(msg.data || ""); }
          else if (msg.type === "ShellExit") { 
            term.write("\\r\\n[会话结束 code="+(msg.code??"") +"]"); 
            addLog("会话结束 code="+(msg.code??"")); 
            setStatus("已断开", false); 
            if (restartPending) {
              // close and let close handler reopen
              ws.close();
              return;
            }
          }
        }catch(e){}
      });
      onDataDispose = term.onData((d)=>{ if (ws.readyState === WebSocket.OPEN) ws.send(JSON.stringify({type:"input", data:d})); });
      ws.addEventListener("close", ()=>{ 
        clearTimer(); clearInterval(hb); 
        addLog("WS 已关闭"); setStatus("已断开", false); 
        if (restartPending) { restartPending = false; setTimeout(()=>openWS(), 200); }
      });
      ws.addEventListener("error", ()=>{ clearTimer(); clearInterval(hb); addLog("WS 错误"); setStatus("连接错误", false); });
    };
    loadFontConfig().then(()=>openWS());
    window.addEventListener("resize", ()=>{ sendResize(); });
    window.addEventListener("beforeunload", ()=>{ try{ ws.close(); }catch(e){} });
    document.getElementById("btn-reconnect").addEventListener("click", ()=>{ addLog("手动重连"); openWS(); });
    document.getElementById("btn-restart").addEventListener("click", ()=>{ addLog("重开会话"); if (ws && ws.readyState===WebSocket.OPEN){ ws.send(JSON.stringify({type:"stop"})); } openWS(); });
    // toggle side
    const toggleSide = ()=> {
      if (sideEl.style.display === "none") {
        sideEl.style.display = "flex";
        document.getElementById("side-toggle").textContent = "》";
      } else {
        sideEl.style.display = "none";
        document.getElementById("side-toggle").textContent = "《";
      }
    };
    document.getElementById("side-toggle").addEventListener("click", toggleSide);
    document.addEventListener("keydown", (e)=>{ if (e.key === "F2") toggleSide(); });
    document.getElementById("font-inc").addEventListener("click", ()=>{
      fontSize = Math.min(32, fontSize + 1);
      if (isMobile) { lineHeight = Math.max(1.4, lineHeight + 0.05); }
      applyFont();
    });
    document.getElementById("font-dec").addEventListener("click", ()=>{
      fontSize = Math.max(10, fontSize - 1);
      if (isMobile) { lineHeight = Math.max(1.4, lineHeight - 0.05); }
      applyFont();
    });
    // hotkeys
    const keyMap = {
      "ctrl+c": "\\u0003",
      "ctrl+v": "\\u0016",
      "ctrl+d": "\\u0004",
      "ctrl+z": "\\u001a",
      "ctrl+alt+a": "\\u001b\\u0001",
      "ctrl+l": "\\u000c",
      "esc": "\\u001b",
      "tab": "\\t"
    };
    const sendRaw = (s) => {
      if (ws && ws.readyState === WebSocket.OPEN) {
        const raw = s.replace(/\\\\u([0-9a-fA-F]{4})/g, (_, hex)=>String.fromCharCode(parseInt(hex,16)));
        ws.send(JSON.stringify({type:"input", data: raw}));
      }
    };
    document.getElementById("hotkeys").querySelectorAll("button").forEach(btn=>{
      btn.addEventListener("click", ()=>{
        const k = btn.getAttribute("data-key");
        if (k && keyMap[k]) {
          addLog("发送快捷键 "+k);
          sendRaw(keyMap[k]);
        }
      });
    });
    // stats poll
    let lastRx = null, lastTx = null, lastTime = null;
    const fetchStats = async () => {
      try{
        const resp = await fetch("/api/v1/node/sysinfo", {
          method:"POST",
          headers: {
            "Content-Type":"application/json",
            "Authorization": ${JSON.stringify(token)}
          },
          body: JSON.stringify({ nodeId: ${node.id}, limit: 1 })
        });
        const data = await resp.json();
        if (data.code === 0 && Array.isArray(data.data) && data.data.length > 0) {
          const s = data.data[data.data.length-1];
          let cpu = (s.cpu ?? 0).toFixed(1) + "%";
          let mem = (s.mem ?? 0).toFixed(1) + "%";
          let up = "--", down = "--";
          if (lastRx !== null && lastTx !== null && lastTime !== null) {
            const dt = (s.timeMs - lastTime)/1000;
            if (dt > 0) {
              const rxBps = (s.bytesRx - lastRx)/dt;
              const txBps = (s.bytesTx - lastTx)/dt;
              const fmt = (v)=> {
                if (v >= 1e9) return (v/1e9).toFixed(2)+" GB/s";
                if (v >= 1e6) return (v/1e6).toFixed(2)+" MB/s";
                if (v >= 1e3) return (v/1e3).toFixed(2)+" KB/s";
                return v.toFixed(0)+" B/s";
              };
              down = fmt(rxBps);
              up = fmt(txBps);
            }
          }
          lastRx = s.bytesRx; lastTx = s.bytesTx; lastTime = s.timeMs;
          statsEl.textContent = "CPU " + cpu + " | 内存 " + mem + " | 上行 " + up + " | 下行 " + down;
        }
      }catch(e){}
    };
    fetchStats();
    setInterval(fetchStats, 5000);
  </script>
</body>
</html>`;
    const w = window.open("", "_blank");

    if (w) {
      w.document.write(html);
      w.document.close();
    } else {
      toast.error("请允许弹窗以打开终端");
    }
  };

  const openTerminal = (node: Node) => {
    if (!isAdmin) return;
    // 默认改为新标签页
    openTerminalWindow(node);
  };

  const sendTermInput = (data: string) => {
    if (!termWSRef.current || termWSRef.current.readyState !== WebSocket.OPEN)
      return;
    termWSRef.current.send(JSON.stringify({ type: "input", data }));
  };

  useEffect(() => {
    if (!termModal.open) {
      return;
    }
    if (termContainerRef.current && !termRef.current) {
      const term = new Terminal({
        convertEol: true,
        cursorBlink: true,
        disableStdin: false,
        fontSize: 13,
        theme: { background: "#000000", foreground: "#d1d5db" },
        scrollback: 2000,
      });

      term.open(termContainerRef.current);
      term.focus();
      term.onData((d: string) => sendTermInput(d));
      termRef.current = term;
      if (termModal.content) {
        term.write(termModal.content);
      }
    } else if (termRef.current) {
      termRef.current.focus();
    }
  }, [termModal.open]);

  useEffect(() => {
    if (termRef.current) {
      // ensure latest content visible
      termRef.current.scrollToBottom();
    }
  }, [termModal.content, termModal.running]);

  // 加载版本信息
  useEffect(() => {
    getVersionInfo()
      .then((res: any) => {
        if (res.code === 0 && res.data) {
          setServerVersion(res.data.server || "");
          setAgentVersion(res.data.agent || "");
        }
      })
      .catch(() => {});
  }, []);

  // 加载节点列表
  const loadNodes = async (silent = false, withExtras = true) => {
    if (!silent) setLoading(true);
    try {
      const res = await getNodeList();

      if (res.code === 0) {
        const mappedNodes = res.data.map((node: any) => {
          const online = node.status === 1 ? "online" : "offline";
          const base: any = {
            ...node,
            connectionStatus: online,
            copyLoading: false,
          };

          if (
            typeof node.gostApi !== "undefined" ||
            typeof node.gostRunning !== "undefined"
          ) {
            base.systemInfo = {
              cpuUsage: 0,
              memoryUsage: 0,
              uploadTraffic: 0,
              downloadTraffic: 0,
              uploadSpeed: 0,
              downloadSpeed: 0,
              uptime: 0,
              gostApi: node.gostApi === 1,
              gostRunning: node.gostRunning === 1,
              gostApiConfigured: node.gostApi === 1 ? true : undefined,
            };
          } else {
            base.systemInfo = null;
          }

          return base;
        });

        setNodeList((prev) => {
          const prevMap = new Map(prev.map((n) => [n.id, n]));
          return mappedNodes.map((node: any) => {
            const old = prevMap.get(node.id);
            if (old?.systemInfo) {
              const curUptime = node.systemInfo?.uptime || 0;
              if (!node.systemInfo || curUptime === 0) {
                node.systemInfo = old.systemInfo;
              }
            }
            if (old?.copyLoading) {
              node.copyLoading = old.copyLoading;
            }
            return node;
          });
        });
        if (withExtras) {
          // 预拉取各节点的 NQ 结果存在性
          mappedNodes.forEach(async (node: any) => {
            try {
              const r: any = await getNQResult(node.id);

              if (r.code === 0 && r.data && (r.data.content || r.data.done)) {
                setNqHasResult((prev: Record<number, boolean>) => ({
                  ...prev,
                  [node.id]: true,
                }));
                const content = (r.data.content as string) || "";
                const timeMs = r.data.timeMs || null;

                setNqResultCache((prev) => ({
                  ...prev,
                  [node.id]: { content, timeMs },
                }));
              }
            } catch {}
          });
          // 批量拉取最近1小时探针概览（按配置可隐藏）
          if (showNetwork) {
            try {
              const r = await getNodeNetworkStatsBatch("1h");

              if (r.code === 0 && r.data) {
                const mapped: any = {};

                Object.keys(r.data).forEach((nid) => {
                  const item = r.data[nid];

                  mapped[Number(nid)] = {
                    avg: item.avg ?? 0,
                    latest: item.latest ?? null,
                    target: item.latestTarget,
                  };
                });
                setProbeStat(mapped);
              }
            } catch {}
          }
        }
      } else {
        toast.error(res.msg || "加载节点列表失败");
      }
    } catch (error) {
      toast.error("网络错误，请重试");
    } finally {
      if (!silent) setLoading(false);
    }
  };

  const openExitModal = useCallback((node: Node) => {
    setExitNode(node);
    setExitModalOpen(true);
  }, []);

  const formatRemainDays = (node: Node) => {
    if (!node.cycleMonths || !node.startDateMs) return "";
    let months = node.cycleMonths;
    let exp: number | null = null;

    if (months > 0) {
      exp = addMonths(node.startDateMs, months);
      const now = Date.now();

      while (exp <= now) exp = addMonths(exp, months);
    } else {
      return "";
    }
    if (!exp) return "";
    const days = Math.max(
      0,
      Math.ceil((exp - Date.now()) / (24 * 3600 * 1000)),
    );

    return `${days} 天`;
  };

  const goNetwork = (node: Node) => {
    navigate(`/network/${node.id}`);
  };


  // 刷新节点服务状态（仅查询 ss）
  const refreshServices = async (node: Node) => {
    setNodeList((prev) =>
      prev.map((n) => (n.id === node.id ? { ...n, ssLoading: true } : n)),
    );
    try {
      const res = await queryNodeServices({ nodeId: node.id, filter: "ss" });

      if (res.code === 0 && Array.isArray(res.data)) {
        const items = res.data as any[];
        const ss = items.find((x) => x && x.handler === "ss");
        const desc = ss
          ? `SS: 端口 ${ss.port || ss.addr || "-"}，监听 ${ss.listening ? "是" : "否"}`
          : "SS: 未部署";

        setNodeList((prev) =>
          prev.map((n) =>
            n.id === node.id ? { ...n, ssStatus: desc, ssLoading: false } : n,
          ),
        );
      } else {
        setNodeList((prev) =>
          prev.map((n) =>
            n.id === node.id
              ? { ...n, ssStatus: "SS: 查询失败", ssLoading: false }
              : n,
          ),
        );
      }
    } catch {
      setNodeList((prev) =>
        prev.map((n) =>
          n.id === node.id
            ? { ...n, ssStatus: "SS: 查询失败", ssLoading: false }
            : n,
        ),
      );
    }
  };


  // 初始化WebSocket连接
  const initWebSocket = () => {
    setWsStatus("connecting");
    if (
      websocketRef.current &&
      (websocketRef.current.readyState === WebSocket.OPEN ||
        websocketRef.current.readyState === WebSocket.CONNECTING)
    ) {
      return;
    }

    if (websocketRef.current) {
      closeWebSocket();
    }

    // 构建 WebSocket URL：固定基于浏览器当前 origin，避免 baseURL 末尾斜杠/环境差异导致的不稳定
    const loc = window.location;
    const wsScheme = loc.protocol === "https:" ? "wss" : "ws";
    const wsUrl = `${wsScheme}://${loc.host}/system-info?type=0`;

    setWsUrlShown(wsUrl);

    try {
      websocketRef.current = new WebSocket(wsUrl);

      websocketRef.current.onopen = () => {
        reconnectAttemptsRef.current = 0;
        setWsStatus("connected");
      };

      websocketRef.current.onmessage = (event) => {
        try {
          const data = JSON.parse(event.data);

          handleWebSocketMessage(data);
        } catch (error) {
          // 解析失败时不输出错误信息
        }
      };

      websocketRef.current.onerror = () => {
        // WebSocket错误时不输出错误信息
        setWsStatus("disconnected");
      };

      websocketRef.current.onclose = () => {
        websocketRef.current = null;
        setWsStatus("disconnected");
        attemptReconnect();
      };
    } catch (error) {
      setWsStatus("disconnected");
      attemptReconnect();
    }
  };

  // 处理WebSocket消息
  const handleWebSocketMessage = (data: any) => {
    if (suspendRealtimeRef.current) return;
    const { id, type, data: messageData } = data;

    if (type === "status") {
      setNodeList((prev) =>
        prev.map((node) => {
          if (node.id == id) {
            return {
              ...node,
              connectionStatus: messageData === 1 ? "online" : "offline",
              systemInfo: node.systemInfo,
            };
          }

          return node;
        }),
      );
    } else if (type === "info") {
      setNodeList((prev) =>
        prev.map((node) => {
          if (node.id == id) {
            try {
              let systemInfo;

              if (typeof messageData === "string") {
                systemInfo = JSON.parse(messageData);
              } else {
                systemInfo = messageData;
              }
              if (!systemInfo || Object.keys(systemInfo).length === 0) {
                return node;
              }

              const currentUpload = parseInt(systemInfo.bytes_transmitted) || 0;
              const currentDownload = parseInt(systemInfo.bytes_received) || 0;
              const currentUptime = parseInt(systemInfo.uptime) || 0;

              if (!currentUptime && node.systemInfo) {
                return node;
              }

              let uploadSpeed = 0;
              let downloadSpeed = 0;

              if (node.systemInfo && node.systemInfo.uptime) {
                const timeDiff = currentUptime - node.systemInfo.uptime;

                if (timeDiff > 0 && timeDiff <= 10) {
                  const lastUpload = node.systemInfo.uploadTraffic || 0;
                  const lastDownload = node.systemInfo.downloadTraffic || 0;

                  const uploadDiff = currentUpload - lastUpload;
                  const downloadDiff = currentDownload - lastDownload;

                  const uploadReset = currentUpload < lastUpload;
                  const downloadReset = currentDownload < lastDownload;

                  if (!uploadReset && uploadDiff >= 0) {
                    uploadSpeed = uploadDiff / timeDiff;
                  }

                  if (!downloadReset && downloadDiff >= 0) {
                    downloadSpeed = downloadDiff / timeDiff;
                  }
                }
              }

              return {
                ...node,
                connectionStatus: "online",
                systemInfo: {
                  cpuUsage: parseFloat(systemInfo.cpu_usage) || 0,
                  memoryUsage: parseFloat(systemInfo.memory_usage) || 0,
                  uploadTraffic: currentUpload,
                  downloadTraffic: currentDownload,
                  uploadSpeed: uploadSpeed,
                  downloadSpeed: downloadSpeed,
                  uptime: currentUptime,
                  gostApi: !!systemInfo.gost_api,
                  gostRunning: !!systemInfo.gost_running,
                  // Prefer explicit configured flag; fallback to api reachable for agents未上报configured的旧版
                  gostApiConfigured:
                    systemInfo.gost_api_configured !== undefined
                      ? !!systemInfo.gost_api_configured
                      : !!systemInfo.gost_api,
                },
              };
            } catch (error) {
              return node;
            }
          }

          return node;
        }),
      );
    }
  };

  // 尝试重新连接
  const attemptReconnect = () => {
    if (reconnectAttemptsRef.current < maxReconnectAttempts) {
      reconnectAttemptsRef.current++;

      reconnectTimerRef.current = setTimeout(() => {
        setWsStatus("connecting");
        initWebSocket();
      }, 3000 * reconnectAttemptsRef.current);
    }
  };

  const doRestartGost = async (nodeId: number) => {
    setRstLoading((prev) => ({ ...prev, [nodeId]: true }));
    try {
      const r: any = await restartGost(nodeId);

      if (r && r.code === 0) {
        const ok = !!r.data?.success;

        toast[ok ? "success" : "error"](
          r.data?.message || (ok ? "已下发重启" : "重启失败"),
        );
      } else {
        toast.error(r?.msg || "重启失败");
      }
    } catch {
      toast.error("重启失败");
    } finally {
      setRstLoading((prev) => ({ ...prev, [nodeId]: false }));
    }
  };

  const doReapply = async (nodeId: number) => {
    setReapplyLoading((prev) => ({ ...prev, [nodeId]: true }));
    try {
      const r: any = await agentReconcileNode(nodeId);

      if (r && r.code === 0) {
        toast.success(`已触发重新应用，推送数量: ${r.data?.pushed ?? 0}`);
      } else {
        toast.error(r?.msg || "触发失败");
      }
    } catch {
      toast.error("触发失败");
    } finally {
      setReapplyLoading((prev) => ({ ...prev, [nodeId]: false }));
    }
  };

  const showGostConfig = async (node: Node) => {
    setGostConfigModal({
      open: true,
      loading: true,
      content: "",
      title: `${node.name} - GOST 配置`,
    });
    try {
      const res: any = await getGostConfig(node.id);

      if (res.code === 0) {
        const content = (res.data?.content as string) || "无返回内容";

        setGostConfigModal({
          open: true,
          loading: false,
          content,
          title: `${node.name} - GOST 配置`,
        });
      } else {
        toast.error(res.msg || "获取配置失败");
        setGostConfigModal((prev) => ({ ...prev, loading: false }));
      }
    } catch (e: any) {
      toast.error(e?.message || "获取配置失败");
      setGostConfigModal((prev) => ({ ...prev, loading: false }));
    }
  };

  const runNQ = async (node: Node) => {
    setNqLoading((prev) => ({ ...prev, [node.id]: true }));
    try {
      const res: any = await runNQTest(node.id);

      if (res.code === 0) {
        const content = (res.data?.content as string) || "无返回内容";

        setNqResultCache((prev) => ({
          ...prev,
          [node.id]: { content, timeMs: Date.now() },
        }));
        toast.success("已开始 NQ 测试，实时结果将自动更新");
        // 开始轮询结果
        const poll = async (attempt = 0) => {
          if (attempt > 80) return; // ~4分钟
          try {
            const r: any = await getNQResult(node.id);

            if (r.code === 0 && r.data) {
              const ct = (r.data.content as string) || "";
              const tms = r.data.timeMs || null;

              setNqResultCache((prev) => ({
                ...prev,
                [node.id]: { content: ct, timeMs: tms },
              }));
              if (r.data.done) {
                return;
              }
            }
          } catch {}
          setTimeout(() => poll(attempt + 1), 3000);
        };

        poll();
      } else {
        toast.error(res.msg || "测试失败");
      }
    } catch (e: any) {
      toast.error(e?.message || "测试失败");
    } finally {
      setNqLoading((prev) => ({ ...prev, [node.id]: false }));
    }
  };

  const viewNQ = async (node: Node) => {
    const cached = nqResultCache[node.id];

    if (cached && cached.content) {
      setNqModal({
        open: true,
        title: `${node.name} - NQ 测试结果`,
        content: cached.content,
        timeMs: cached.timeMs,
        loading: false,
        nodeId: node.id,
        done: false,
      });
    } else {
      setNqModal({
        open: true,
        title: `${node.name} - NQ 测试结果`,
        content: "",
        timeMs: null,
        loading: true,
        nodeId: node.id,
        done: false,
      });
    }
    try {
      const res: any = await getNQResult(node.id);

      if (res.code === 0 && res.data) {
        const content = (res.data.content as string) || "无返回内容";
        const timeMs = res.data.timeMs || null;
        const done = !!res.data.done;

        setNqResultCache((prev) => ({
          ...prev,
          [node.id]: { content, timeMs },
        }));
        setNqModal({
          open: true,
          title: `${node.name} - NQ 测试结果`,
          content,
          timeMs,
          loading: false,
          nodeId: node.id,
          done,
        });
      } else {
        toast.error(res.msg || "暂无结果");
        setNqModal((prev) => ({ ...prev, open: false, loading: false }));
      }
    } catch (e: any) {
      toast.error(e?.message || "获取结果失败");
      setNqModal((prev) => ({ ...prev, open: false, loading: false }));
    }
  };

  // 自动刷新 NQ 弹窗内容
  useEffect(() => {
    if (!nqModal.open || !nqModal.nodeId) return;
    const timer = setInterval(async () => {
      const nodeId = nqModal.nodeId!;

      try {
        const res: any = await getNQResult(nodeId);

        if (res.code === 0 && res.data) {
          const content = (res.data.content as string) || "";
          const timeMs = res.data.timeMs || null;
          const done = !!res.data.done;

          // 增量合并：如果新内容以旧内容开头，只追加差异
          setNqResultCache((prev) => {
            const prevContent = prev[nodeId]?.content || "";
            let merged = content;

            if (content.startsWith(prevContent)) {
              merged = prevContent + content.slice(prevContent.length);
            }

            return { ...prev, [nodeId]: { content: merged, timeMs } };
          });
          setNqModal((prev) => {
            const prevContent = prev.content || "";
            let merged = content;

            if (content.startsWith(prevContent)) {
              merged = prevContent + content.slice(prevContent.length);
            }

            return { ...prev, content: merged, timeMs, loading: false, done };
          });
          // 滚动到底部
          requestAnimationFrame(() => {
            if (logScrollRef.current) {
              logScrollRef.current.scrollTop =
                logScrollRef.current.scrollHeight;
            }
          });
          if (done) {
            clearInterval(timer);
          }
        }
      } catch {}
    }, 2000);

    return () => clearInterval(timer);
  }, [nqModal.open, nqModal.nodeId]);

  useEffect(() => {
    if (nqModal.open && logScrollRef.current) {
      logScrollRef.current.scrollTop = logScrollRef.current.scrollHeight;
    }
  }, [nqModal.content, nqModal.open]);

  const getProgress = (txt: string) => {
    const match = txt.match(/(\d{1,3})%/);

    if (!match) return null;
    const val = parseInt(match[1], 10);

    if (isNaN(val)) return null;

    return Math.min(Math.max(val, 0), 100);
  };

  // 关闭WebSocket连接
  const closeWebSocket = () => {
    if (reconnectTimerRef.current) {
      clearTimeout(reconnectTimerRef.current);
      reconnectTimerRef.current = null;
    }

    reconnectAttemptsRef.current = 0;

    if (websocketRef.current) {
      websocketRef.current.onopen = null;
      websocketRef.current.onmessage = null;
      websocketRef.current.onerror = null;
      websocketRef.current.onclose = null;

      if (
        websocketRef.current.readyState === WebSocket.OPEN ||
        websocketRef.current.readyState === WebSocket.CONNECTING
      ) {
        websocketRef.current.close();
      }

      websocketRef.current = null;
    }

    setNodeList((prev) =>
      prev.map((node) => ({
        ...node,
        connectionStatus: "offline",
        systemInfo: null,
      })),
    );
  };

  // 格式化速度
  const formatSpeed = (bytesPerSecond: number): string => {
    if (bytesPerSecond === 0) return "0 B/s";

    const k = 1024;
    const sizes = ["B/s", "KB/s", "MB/s", "GB/s", "TB/s"];
    const i = Math.floor(Math.log(bytesPerSecond) / Math.log(k));

    return (
      parseFloat((bytesPerSecond / Math.pow(k, i)).toFixed(2)) + " " + sizes[i]
    );
  };

  // 格式化开机时间
  const formatUptime = (seconds: number): string => {
    if (seconds === 0) return "-";

    const days = Math.floor(seconds / 86400);
    const hours = Math.floor((seconds % 86400) / 3600);
    const minutes = Math.floor((seconds % 3600) / 60);

    if (days > 0) {
      return `${days}天${hours}小时`;
    } else if (hours > 0) {
      return `${hours}小时${minutes}分钟`;
    } else {
      return `${minutes}分钟`;
    }
  };

  // 格式化流量
  const formatTraffic = (bytes: number): string => {
    if (bytes === 0) return "0 B";

    const k = 1024;
    const sizes = ["B", "KB", "MB", "GB", "TB"];
    const i = Math.floor(Math.log(bytes) / Math.log(k));

    return parseFloat((bytes / Math.pow(k, i)).toFixed(2)) + " " + sizes[i];
  };

  // 获取进度条颜色
  const getProgressColor = (
    value: number,
    offline = false,
  ): "default" | "primary" | "secondary" | "success" | "warning" | "danger" => {
    if (offline) return "default";
    if (value <= 50) return "success";
    if (value <= 80) return "warning";

    return "danger";
  };

  // 新增节点
  const handleAdd = () => {
    setEditNode(null);
    setDialogVisible(true);
  };

  // 编辑节点
  const handleEdit = (node: Node) => {
    setEditNode(node);
    setDialogVisible(true);
  };

  // 删除节点
  const handleDelete = (node: Node) => {
    setNodeToDelete(node);
    // 默认勾选：删除节点时同时卸载 Agent
    setDeleteAlsoUninstall(true);
    setDeleteModalOpen(true);
  };

  const confirmDelete = async () => {
    if (!nodeToDelete) return;

    setDeleteLoading(true);
    try {
      const res = await deleteNode(nodeToDelete.id, deleteAlsoUninstall);

      if (res.code === 0) {
        toast.success("删除成功");
        setNodeList((prev) => prev.filter((n) => n.id !== nodeToDelete.id));
        setDeleteModalOpen(false);
        setNodeToDelete(null);
      } else {
        toast.error(res.msg || "删除失败");
      }
    } catch (error) {
      toast.error("网络错误，请重试");
    } finally {
      setDeleteLoading(false);
    }
  };

  // 复制安装命令
  const handleCopyInstallCommand = async (node: Node) => {
    setNodeList((prev) =>
      prev.map((n) => (n.id === node.id ? { ...n, copyLoading: true } : n)),
    );

    try {
      const res = await getNodeInstallCommand(node.id);

      if (res.code === 0 && res.data) {
        const cmds: InstallCommands =
          typeof res.data === "string" ? { static: res.data } : res.data;
        setInstallCommands(cmds);
        setCurrentNodeName(node.name);
        setInstallCommandModal(true);
        const toCopy = cmds.static || cmds.github || cmds.local;
        if (toCopy) {
          try {
            await navigator.clipboard.writeText(toCopy);
            toast.success("已复制静态源安装命令到剪贴板");
          } catch (copyError) {
            toast.success("已生成安装命令，请手动复制");
          }
        }
      } else {
        toast.error(res.msg || "获取安装命令失败");
      }
    } catch (error) {
      toast.error("获取安装命令失败");
    } finally {
      setNodeList((prev) =>
        prev.map((n) => (n.id === node.id ? { ...n, copyLoading: false } : n)),
      );
    }
  };

  // 手动复制安装命令
  const handleManualCopy = async (cmd: string) => {
    if (!cmd) return;
    try {
      await navigator.clipboard.writeText(cmd);
      toast.success("安装命令已复制到剪贴板");
      setInstallCommandModal(false);
    } catch (error) {
      toast.error("复制失败，请手动选择文本复制");
    }
  };

  const openConnections = async (node: Node) => {
    setConnModal({
      open: true,
      nodeName: node.name,
      loading: true,
      versions: [],
    });
    try {
      const res: any = await getNodeConnections();

      if (res && res.code === 0 && Array.isArray(res.data)) {
        const item = res.data.find(
          (it: any) => Number(it.nodeId) === Number(node.id),
        );
        const versions = Array.isArray(item?.conns)
          ? item.conns.map((c: any) => String(c?.version || "unknown"))
          : [];

        setConnModal({
          open: true,
          nodeName: node.name,
          loading: false,
          versions,
        });
      } else {
        setConnModal((prev) => ({ ...prev, loading: false }));
        toast.error(res?.msg || "获取连接信息失败");
      }
    } catch {
      setConnModal((prev) => ({ ...prev, loading: false }));
      toast.error("获取连接信息失败");
    }
  };

  const runSelfCheck = async (node: Node) => {
    setSelfCheckModal({
      open: true,
      nodeName: node.name,
      loading: true,
      result: null,
    });
    try {
      const res: any = await nodeSelfCheck(node.id);

      if (res && res.code === 0) {
        setSelfCheckModal({
          open: true,
          nodeName: node.name,
          loading: false,
          result: res.data || null,
        });
      } else {
        setSelfCheckModal((prev) => ({ ...prev, loading: false }));
        toast.error(res?.msg || "自检失败");
      }
    } catch {
      setSelfCheckModal((prev) => ({ ...prev, loading: false }));
      toast.error("自检失败");
    }
  };

  const handleNodeSaved = () => {
    loadNodes();
  };

  const handleNodeModalChange = useCallback((open: boolean) => {
    setDialogVisible(open);
    if (!open) setEditNode(null);
  }, []);

  const handleExitModalChange = useCallback((open: boolean) => {
    setExitModalOpen(open);
    if (!open) setExitNode(null);
  }, []);

  return (
    <div className="px-3 lg:px-6 py-8">
      {/* 页面头部 */}
      <div className="flex items-center justify-between mb-6">
        <div className="flex items-center gap-4">
          <div className="flex items-center gap-2 text-sm">
            <span
              className={`inline-block w-2 h-2 rounded-full ${wsStatus === "connected" ? "bg-green-500" : wsStatus === "connecting" ? "bg-yellow-500" : "bg-red-500"}`}
            />
            <span className="text-default-600">
              {wsStatus === "connected"
                ? "WS 已连接"
                : wsStatus === "connecting"
                  ? "WS 连接中…"
                  : "WS 未连接（自动重试）"}
            </span>
          </div>
          <div
            className="hidden md:block text-xs text-default-500 truncate max-w-[420px]"
            title={wsUrlShown}
          >
            {" "}
            {wsUrlShown || "-"}
          </div>
          <div className="text-xs text-default-500">
            后端: {serverVersion || "-"} · Agent: {agentVersion || "-"}
          </div>
        </div>

        <Button color="primary" size="sm" variant="flat" onPress={handleAdd}>
          新增
        </Button>
      </div>

      {/* 节点列表 */}
      {loading ? (
        <div className="space-y-4">
          <div className="flex items-center justify-center h-24">
            <div className="flex items-center gap-3">
              <Spinner size="sm" />
              <span className="text-default-600 skeleton-text">
                正在加载...
              </span>
            </div>
          </div>
          <div className="grid gap-4 grid-cols-1 sm:grid-cols-2 xl:grid-cols-3 2xl:grid-cols-4">
            {Array.from({ length: 8 }).map((_, idx) => (
              <div key={`node-skel-${idx}`} className="skeleton-card" />
            ))}
          </div>
        </div>
      ) : nodeList.length === 0 ? (
        <Card className="shadow-sm border border-gray-200 dark:border-gray-700">
          <CardBody className="text-center py-16">
            <div className="flex flex-col items-center gap-4">
              <div className="w-16 h-16 bg-default-100 rounded-full flex items-center justify-center">
                <svg
                  className="w-8 h-8 text-default-400"
                  fill="none"
                  stroke="currentColor"
                  viewBox="0 0 24 24"
                >
                  <path
                    d="M5 12h14M5 12l4-4m-4 4l4 4"
                    strokeLinecap="round"
                    strokeLinejoin="round"
                    strokeWidth={1.5}
                  />
                </svg>
              </div>
              <div>
                <h3 className="text-lg font-semibold text-foreground">
                  暂无节点配置
                </h3>
                <p className="text-default-500 text-sm mt-1">
                  还没有创建任何节点配置，点击上方按钮开始创建
                </p>
              </div>
            </div>
          </CardBody>
        </Card>
      ) : nodeList.length > 0 ? (
        <>
          <div className="flex justify-end mb-2 gap-2">
            <Button size="sm" variant="flat" onPress={() => setOpsOpen(true)}>
              操作日志
            </Button>
            <Button
              color="primary"
              size="sm"
              variant="flat"
              onPress={async () => {
                try {
                  const r: any = await getNodeList();

                  if (r && r.code === 0 && Array.isArray(r.data)) {
                    let ok = 0;
                    let total = r.data.length;

                    for (const n of r.data) {
                      try {
                        const rr: any = await agentReconcileNode(n.id);

                        if (rr && rr.code === 0) ok++;
                      } catch {}
                    }
                    toast.success(`已触发重新应用：${ok}/${total}`);
                  } else {
                    toast.error(r?.msg || "获取节点列表失败");
                  }
                } catch {
                  toast.error("操作失败");
                }
              }}
            >
              批量重新应用
            </Button>
          </div>
          <div
            ref={gridWrapRef}
            style={{ visibility: gridReady ? "visible" : "hidden" }}
          >
            <VirtualGrid
              className="w-full"
              estimateRowHeight={nodeRowHeight}
              items={nodeList}
              minItemWidth={320}
              renderItem={(node) => (
                <Card
                  key={node.id}
                  className="list-card node-card shadow-sm border border-divider hover:shadow-md transition-shadow duration-200"
                >
                <CardHeader className="pb-2">
                  <div className="flex justify-between items-start w-full">
                    <div className="flex-1 min-w-0">
                      <h3 className="font-semibold text-foreground truncate text-sm">
                        {node.name}
                      </h3>
                      <p className="text-xs text-default-500 truncate">
                        {node.serverIp}
                      </p>
                    </div>
                    <div className="flex items-center gap-1.5 ml-2">
                      {isAdmin && (
                        <Tooltip content="终端">
                          <Button
                            isIconOnly
                            isDisabled={node.connectionStatus !== "online"}
                            size="sm"
                            variant="light"
                            onPress={() => openTerminal(node)}
                          >
                            <svg
                              className="w-4 h-4"
                              fill="none"
                              stroke="currentColor"
                              strokeWidth="2"
                              viewBox="0 0 24 24"
                            >
                              <path d="M4 17l6-6-6-6" />
                              <path d="M12 19h8" />
                            </svg>
                          </Button>
                        </Tooltip>
                      )}
                      <Tooltip content="连接详情">
                        <Button
                          isIconOnly
                          size="sm"
                          variant="light"
                          onPress={() => openConnections(node)}
                        >
                          <svg
                            className="w-4 h-4"
                            fill="none"
                            stroke="currentColor"
                            strokeWidth="2"
                            viewBox="0 0 24 24"
                          >
                            <path
                              d="M8 12a4 4 0 014-4h4m-4 8h4a4 4 0 000-8"
                              strokeLinecap="round"
                              strokeLinejoin="round"
                            />
                          </svg>
                        </Button>
                      </Tooltip>
                      <Chip
                        className="text-xs"
                        color={
                          node.connectionStatus === "online"
                            ? "success"
                            : "danger"
                        }
                        size="sm"
                        variant="flat"
                      >
                        {node.connectionStatus === "online" ? "在线" : "离线"}
                      </Chip>
                    </div>
                  </div>
                </CardHeader>

                <CardBody className="pt-0 pb-3">
                  {/* 基础信息 */}
                  <div
                    className="space-y-2 mb-4"
                    style={{ cursor: "pointer" }}
                    onClick={() => goNetwork(node)}
                  >
                    <div className="flex justify-between items-center text-sm min-w-0">
                      <span className="text-default-600 flex-shrink-0">
                        入口IP
                      </span>
                      <div className="text-right text-xs min-w-0 flex-1 ml-2">
                        {node.ip ? (
                          node.ip.split(",").length > 1 ? (
                            <span
                              className="font-mono truncate block"
                              title={node.ip.split(",")[0].trim()}
                            >
                              {node.ip.split(",")[0].trim()} +
                              {node.ip.split(",").length - 1}个
                            </span>
                          ) : (
                            <span
                              className="font-mono truncate block"
                              title={node.ip.trim()}
                            >
                              {node.ip.trim()}
                            </span>
                          )
                        ) : (
                          "-"
                        )}
                      </div>
                    </div>
                    <div className="flex justify-between text-sm">
                      <span className="text-default-600">端口</span>
                      <span className="text-xs">
                        {node.portSta}-{node.portEnd}
                      </span>
                    </div>
                    {showNetwork && (
                      <div className="flex justify-between text-sm">
                        <span className="text-default-600">网络</span>
                        <span className="text-xs">
                          {probeStat[node.id]?.latest != null
                            ? `${probeStat[node.id]?.latest} ms`
                            : "-"}
                          {probeStat[node.id]?.avg
                            ? ` · 平均 ${probeStat[node.id]?.avg} ms`
                            : ""}
                          {probeStat[node.id]?.target?.name
                            ? ` · ${probeStat[node.id]?.target?.name}(${probeStat[node.id]?.target?.ip || ""})`
                            : ""}
                        </span>
                      </div>
                    )}
                    {(node.priceCents || node.cycleMonths) && (
                      <div className="flex justify-between text-sm">
                        <span className="text-default-600">计费</span>
                        <span className="text-xs">
                          {node.priceCents
                            ? `¥${(node.priceCents / 100).toFixed(2)}`
                            : ""}
                          {node.cycleMonths
                            ? ` / ${node.cycleMonths === 1 ? "月" : node.cycleMonths === 3 ? "季度" : node.cycleMonths === 6 ? "半年" : node.cycleMonths === 12 ? "年" : node.cycleMonths + "月"}`
                            : ""}
                          {node.startDateMs
                            ? ` · 剩余${formatRemainDays(node)}`
                            : ""}
                        </span>
                      </div>
                    )}
                    <div className="flex justify-between text-sm">
                      <span className="text-default-600">版本</span>
                      <span className="text-xs">{node.version || "未知"}</span>
                    </div>
                    <div className="flex justify-between text-sm">
                      <span className="text-default-600">GOST 服务</span>
                      <span className="text-xs">
                        {node.connectionStatus === "online" && node.systemInfo
                          ? node.systemInfo.gostRunning
                            ? "运行中"
                            : "未运行"
                          : "-"}
                      </span>
                    </div>
                    <div className="flex justify-between text-sm items-center">
                      <span className="text-default-600">NQ 测试</span>
                      <span className="text-xs flex items-center gap-2">
                        <Button
                          color="primary"
                          isLoading={!!nqLoading[node.id]}
                          size="sm"
                          variant="flat"
                          onPress={() => runNQ(node)}
                        >
                          {nqResultCache[node.id] ? "重新测试" : "NQ 测试"}
                        </Button>
                        {(nqResultCache[node.id] || nqHasResult[node.id]) && (
                          <Button
                            size="sm"
                            variant="flat"
                            onPress={() => viewNQ(node)}
                          >
                            查看结果
                          </Button>
                        )}
                      </span>
                    </div>
                    <div className="flex justify-between text-sm items-center">
                      <span className="text-default-600">API 配置</span>
                      <span className="text-xs flex items-center gap-2">
                        {node.connectionStatus === "online" &&
                        node.systemInfo ? (
                          (node.systemInfo as any).gostApiConfigured ===
                          false ? (
                            <Button
                              color="primary"
                              size="sm"
                              variant="flat"
                              onPress={async () => {
                                try {
                                  await enableGostApi(node.id);
                                  toast.success(
                                    "已发送启用 GOST API 指令，稍候刷新",
                                  );
                                } catch (e: any) {
                                  toast.error(e?.message || "指令发送失败");
                                }
                              }}
                            >
                              开启 GOST API
                            </Button>
                          ) : (node.systemInfo as any).gostApiConfigured ===
                            true ? (
                            <Button
                              color="secondary"
                              size="sm"
                              variant="flat"
                              onPress={() => showGostConfig(node)}
                            >
                              查看 GOST 配置
                            </Button>
                          ) : (
                            "检测中…"
                          )
                        ) : (
                          "-"
                        )}
                      </span>
                    </div>
                    <div className="flex justify-between items-center text-sm">
                      <span className="text-default-600">服务</span>
                      <span className="text-xs flex items-center gap-2">
                        {node.ssStatus ? node.ssStatus : "-"}
                        <Button
                          isLoading={node.ssLoading}
                          size="sm"
                          variant="light"
                          onPress={() => refreshServices(node)}
                        >
                          刷新
                        </Button>
                        <Button
                          color="warning"
                          isLoading={!!rstLoading[node.id]}
                          size="sm"
                          variant="flat"
                          onPress={() => doRestartGost(node.id)}
                        >
                          重启 GOST
                        </Button>
                        <Button
                          color="primary"
                          isLoading={!!reapplyLoading[node.id]}
                          size="sm"
                          variant="flat"
                          onPress={() => doReapply(node.id)}
                        >
                          重新应用服务
                        </Button>
                      </span>
                    </div>
                    <div className="flex justify-between text-sm">
                      <span className="text-default-600">开机时间</span>
                        <span
                          className={`text-xs ${node.connectionStatus === "online" ? "" : "text-default-400"}`}
                        >
                          {node.systemInfo
                            ? formatUptime(node.systemInfo.uptime)
                            : "-"}
                        </span>
                    </div>
                  </div>

                  {/* 系统监控 */}
                  <div className="space-y-3 mb-4">
                    <div className="grid grid-cols-2 gap-3">
                      <div>
                        <div className="flex justify-between text-xs mb-1">
                          <span>CPU</span>
                          <span
                            className={`font-mono ${node.connectionStatus === "online" ? "" : "text-default-400"}`}
                          >
                            {node.systemInfo
                              ? `${node.systemInfo.cpuUsage.toFixed(1)}%`
                              : "-"}
                          </span>
                        </div>
                        <Progress
                          aria-label="CPU使用率"
                          color={getProgressColor(
                            node.connectionStatus === "online" &&
                              node.systemInfo
                              ? node.systemInfo.cpuUsage
                              : 0,
                            node.connectionStatus !== "online",
                          )}
                          size="sm"
                          value={node.systemInfo ? node.systemInfo.cpuUsage : 0}
                        />
                      </div>
                      <div>
                        <div className="flex justify-between text-xs mb-1">
                          <span>内存</span>
                          <span
                            className={`font-mono ${node.connectionStatus === "online" ? "" : "text-default-400"}`}
                          >
                            {node.systemInfo
                              ? `${node.systemInfo.memoryUsage.toFixed(1)}%`
                              : "-"}
                          </span>
                        </div>
                        <Progress
                          aria-label="内存使用率"
                          color={getProgressColor(
                            node.connectionStatus === "online" &&
                              node.systemInfo
                              ? node.systemInfo.memoryUsage
                              : 0,
                            node.connectionStatus !== "online",
                          )}
                          size="sm"
                          value={
                            node.systemInfo ? node.systemInfo.memoryUsage : 0
                          }
                        />
                      </div>
                    </div>

                    <div className="grid grid-cols-2 gap-2 text-xs">
                      <div className="text-center p-2 bg-default-50 dark:bg-default-100 rounded">
                        <div className="text-default-600 mb-0.5">上传</div>
                        <div
                          className={`font-mono ${node.connectionStatus === "online" ? "" : "text-default-400"}`}
                        >
                          {node.systemInfo
                            ? formatSpeed(node.systemInfo.uploadSpeed)
                            : "-"}
                        </div>
                      </div>
                      <div className="text-center p-2 bg-default-50 dark:bg-default-100 rounded">
                        <div className="text-default-600 mb-0.5">下载</div>
                        <div
                          className={`font-mono ${node.connectionStatus === "online" ? "" : "text-default-400"}`}
                        >
                          {node.systemInfo
                            ? formatSpeed(node.systemInfo.downloadSpeed)
                            : "-"}
                        </div>
                      </div>
                    </div>

                    {/* 流量统计 */}
                    <div className="grid grid-cols-2 gap-2 text-xs">
                      <div className="text-center p-2 bg-primary-50 dark:bg-primary-100/20 rounded border border-primary-200 dark:border-primary-300/20">
                        <div className="text-primary-600 dark:text-primary-400 mb-0.5">
                          ↑ 上行流量
                        </div>
                        <div
                          className={`font-mono ${node.connectionStatus === "online" ? "text-primary-700 dark:text-primary-300" : "text-default-400"}`}
                        >
                          {node.systemInfo
                            ? formatTraffic(node.systemInfo.uploadTraffic)
                            : "-"}
                        </div>
                      </div>
                      <div className="text-center p-2 bg-success-50 dark:bg-success-100/20 rounded border border-success-200 dark:border-success-300/20">
                        <div className="text-success-600 dark:text-success-400 mb-0.5">
                          ↓ 下行流量
                        </div>
                        <div
                          className={`font-mono ${node.connectionStatus === "online" ? "text-success-700 dark:text-success-300" : "text-default-400"}`}
                        >
                          {node.systemInfo
                            ? formatTraffic(node.systemInfo.downloadTraffic)
                            : "-"}
                        </div>
                      </div>
                    </div>
                  </div>

                  {/* 操作按钮 */}
                  <div className="space-y-1.5">
                    <div className="grid grid-cols-3 gap-1.5">
                      <Button
                        className="w-full min-h-8"
                        color="success"
                        isLoading={node.copyLoading}
                        size="sm"
                        variant="flat"
                        onPress={() => handleCopyInstallCommand(node)}
                      >
                        安装
                      </Button>
                      <Button
                        className="w-full min-h-8"
                        color="warning"
                        size="sm"
                        variant="flat"
                        onPress={() => openExitModal(node)}
                      >
                        出口
                      </Button>
                      <Button
                        className="w-full min-h-8"
                        color="default"
                        size="sm"
                        variant="flat"
                        onPress={() => runSelfCheck(node)}
                      >
                        自检
                      </Button>
                      <Button
                        className="w-full min-h-8"
                        color="default"
                        size="sm"
                        variant="flat"
                        onPress={() =>
                          setUsedPortsModal({
                            open: true,
                            nodeName: node.name,
                            ports: node.usedPorts || [],
                          })
                        }
                      >
                        占用端口
                      </Button>
                      <Button
                        className="w-full min-h-8"
                        color="primary"
                        size="sm"
                        variant="flat"
                        onPress={() => handleEdit(node)}
                      >
                        编辑
                      </Button>
                      <Button
                        className="w-full min-h-8"
                        color="danger"
                        size="sm"
                        variant="flat"
                        onPress={() => handleDelete(node)}
                      >
                        删除
                      </Button>
                    </div>
                  </div>
                </CardBody>
                </Card>
              )}
            />
          </div>
        </>
      ) : null}

      <OpsLogModal isOpen={opsOpen} onOpenChange={setOpsOpen} />
      <Modal
        isOpen={termModal.open}
        placement="center"
        scrollBehavior="inside"
        size="5xl"
        onOpenChange={(open) => {
          if (!open) {
            closeTermWS();
            setTermModal({
              open: false,
              nodeId: null,
              nodeName: "",
              content: "",
              running: false,
              connecting: false,
            });
          }
        }}
      >
        <ModalContent>
          <ModalHeader>
            终端 · {termModal.nodeName}
            <span className="text-xs text-default-500 ml-2">
              {termModal.connecting
                ? "连接中..."
                : termModal.running
                  ? "运行中"
                  : "已断开"}
            </span>
          </ModalHeader>
          <ModalBody>
            <div className="bg-black rounded-md h-[60vh] min-h-[300px] overflow-hidden">
              <div ref={termContainerRef} className="w-full h-full" />
            </div>
            <div className="text-xs text-default-500">
              按键直接发送到节点 /bin/bash，关闭弹窗不会终止会话。
            </div>
          </ModalBody>
          <ModalFooter>
            <Button
              variant="light"
              onPress={() => {
                closeTermWS();
                setTermModal({
                  open: false,
                  nodeId: null,
                  nodeName: "",
                  content: "",
                  running: false,
                  connecting: false,
                });
              }}
            >
              关闭
            </Button>
          </ModalFooter>
        </ModalContent>
      </Modal>

      {/* 新增/编辑节点对话框 */}
      <NodeEditModal
        editNode={editNode}
        isOpen={dialogVisible}
        onOpenChange={handleNodeModalChange}
        onSaved={handleNodeSaved}
      />

      {/* 出口服务设置弹窗 */}
      <ExitServiceModal
        isOpen={exitModalOpen}
        node={exitNode}
        onOpenChange={handleExitModalChange}
      />

      {/* 已占用端口弹窗 */}
      <Modal
        backdrop="opaque"
        disableAnimation
        isOpen={usedPortsModal.open}
        placement="center"
        scrollBehavior="outside"
        size="2xl"
        onOpenChange={(open) =>
          setUsedPortsModal((prev) => ({ ...prev, open }))
        }
      >
        <ModalContent>
          {(onClose) => (
            <>
              <ModalHeader>
                已占用端口 · {usedPortsModal.nodeName}
              </ModalHeader>
              <ModalBody>
                {usedPortsModal.ports.length > 0 ? (
                  <Textarea
                    readOnly
                    className="font-mono text-xs"
                    minRows={6}
                    value={usedPortsModal.ports.join(", ")}
                  />
                ) : (
                  <div className="text-sm text-default-500">
                    暂无上报或无占用端口
                  </div>
                )}
              </ModalBody>
              <ModalFooter>
                <Button variant="light" onPress={onClose}>
                  关闭
                </Button>
              </ModalFooter>
            </>
          )}
        </ModalContent>
      </Modal>

      {/* 连接详情弹窗 */}
      <Modal
        backdrop="opaque"
        disableAnimation
        isOpen={connModal.open}
        placement="center"
        scrollBehavior="outside"
        size="2xl"
        onOpenChange={(open) => setConnModal((prev) => ({ ...prev, open }))}
      >
        <ModalContent>
          {(onClose) => (
            <>
              <ModalHeader>连接详情 · {connModal.nodeName}</ModalHeader>
              <ModalBody>
                {connModal.loading ? (
                  <div className="text-sm text-default-500">加载中...</div>
                ) : connModal.versions.length > 0 ? (
                  <div className="space-y-2 text-sm">
                    <div className="text-default-500">
                      连接数：{connModal.versions.length}
                    </div>
                    <Textarea
                      readOnly
                      className="font-mono text-xs"
                      minRows={4}
                      value={connModal.versions.join("\n")}
                    />
                  </div>
                ) : (
                  <div className="text-sm text-default-500">暂无连接</div>
                )}
              </ModalBody>
              <ModalFooter>
                <Button variant="light" onPress={onClose}>
                  关闭
                </Button>
              </ModalFooter>
            </>
          )}
        </ModalContent>
      </Modal>

      {/* 节点自检弹窗 */}
      <Modal
        backdrop="opaque"
        disableAnimation
        isOpen={selfCheckModal.open}
        placement="center"
        scrollBehavior="outside"
        size="2xl"
        onOpenChange={(open) =>
          setSelfCheckModal((prev) => ({ ...prev, open }))
        }
      >
        <ModalContent>
          {(onClose) => (
            <>
              <ModalHeader>节点自检 · {selfCheckModal.nodeName}</ModalHeader>
              <ModalBody>
                {selfCheckModal.loading ? (
                  <div className="text-sm text-default-500">检测中...</div>
                ) : selfCheckModal.result ? (
                  <div className="space-y-3 text-sm">
                    {["ping", "tcp"].map((key) => {
                      const item = selfCheckModal.result?.[key];
                      if (!item) return null;
                      return (
                        <div
                          key={key}
                          className="border border-default-200 rounded-lg p-3"
                        >
                          <div className="flex items-center justify-between mb-1">
                            <span className="font-medium">
                              {key === "ping" ? "ICMP Ping" : "TCP 测试"}
                            </span>
                            <Chip
                              color={item.success ? "success" : "danger"}
                              size="sm"
                              variant="flat"
                            >
                              {item.success ? "正常" : "失败"}
                            </Chip>
                          </div>
                          <div className="text-xs text-default-500">
                            目标: {item.target || "-"} · 平均延迟:{" "}
                            {item.averageTime ?? "-"} ms · 丢包:{" "}
                            {item.packetLoss ?? "-"}%
                          </div>
                          <div className="text-xs text-default-500 mt-1">
                            {item.message || "-"}
                          </div>
                        </div>
                      );
                    })}
                  </div>
                ) : (
                  <div className="text-sm text-default-500">暂无结果</div>
                )}
              </ModalBody>
              <ModalFooter>
                <Button variant="light" onPress={onClose}>
                  关闭
                </Button>
              </ModalFooter>
            </>
          )}
        </ModalContent>
      </Modal>

      {/* 删除确认模态框 */}
      <Modal
        backdrop="blur"
        isOpen={deleteModalOpen}
        placement="center"
        scrollBehavior="outside"
        size="2xl"
        onOpenChange={setDeleteModalOpen}
      >
        <ModalContent>
          {(onClose) => (
            <>
              <ModalHeader className="flex flex-col gap-1">
                <h2 className="text-xl font-bold">确认删除</h2>
              </ModalHeader>
              <ModalBody>
                <p>
                  确定要删除节点 <strong>"{nodeToDelete?.name}"</strong> 吗？
                </p>
                <p className="text-small text-default-500">
                  此操作不可恢复，请谨慎操作。
                </p>
                <label className="flex items-center gap-2 text-sm mt-2">
                  <input
                    checked={deleteAlsoUninstall}
                    type="checkbox"
                    onChange={(e) =>
                      setDeleteAlsoUninstall((e.target as any).checked)
                    }
                  />
                  同步卸载节点上的 Agent（自我卸载）
                </label>
              </ModalBody>
              <ModalFooter>
                <Button variant="light" onPress={onClose}>
                  取消
                </Button>
                <Button
                  color="danger"
                  isLoading={deleteLoading}
                  onPress={confirmDelete}
                >
                  {deleteLoading ? "删除中..." : "确认删除"}
                </Button>
              </ModalFooter>
            </>
          )}
        </ModalContent>
      </Modal>

      {/* GOST 配置查看 */}
      <Modal
        backdrop="blur"
        isOpen={gostConfigModal.open}
        placement="center"
        scrollBehavior="outside"
        size="3xl"
        onClose={() => setGostConfigModal((prev) => ({ ...prev, open: false }))}
      >
        <ModalContent>
          <ModalHeader>{gostConfigModal.title}</ModalHeader>
          <ModalBody>
            {gostConfigModal.loading ? (
              <div className="text-sm text-default-500">读取中...</div>
            ) : (
              <pre className="bg-default-50 dark:bg-default-100/10 rounded-lg p-4 text-xs whitespace-pre-wrap break-all">
                {gostConfigModal.content || "无配置内容"}
              </pre>
            )}
          </ModalBody>
          <ModalFooter>
            <Button
              variant="light"
              onPress={() =>
                setGostConfigModal((prev) => ({ ...prev, open: false }))
              }
            >
              关闭
            </Button>
          </ModalFooter>
        </ModalContent>
      </Modal>

      {/* NQ 测试结果 */}
      <Modal
        backdrop="blur"
        isOpen={nqModal.open}
        placement="center"
        scrollBehavior="outside"
        size="5xl"
        onClose={() => setNqModal((prev) => ({ ...prev, open: false }))}
      >
        <ModalContent className="w-full max-w-[95vw] md:max-w-[95vw] lg:max-w-[60vw] h-[80vh]">
          <ModalHeader>{nqModal.title}</ModalHeader>
          <ModalBody>
            {nqModal.loading ? (
              <div className="text-sm text-default-500">读取中...</div>
            ) : (
              <>
                {!nqModal.done && (
                  <div className="flex items-center gap-3 text-xs text-default-500 mb-2">
                    <Spinner size="sm" />
                    <span>实时更新中...</span>
                    {getProgress(nqModal.content) !== null && (
                      <span className="text-default-600 font-mono">
                        {getProgress(nqModal.content)}%
                      </span>
                    )}
                  </div>
                )}
                <div ref={logScrollRef} className="h-[60vh] overflow-y-auto">
                  <pre
                    dangerouslySetInnerHTML={{
                      __html: nqModal.content || "暂无结果",
                    }}
                    className="bg-black text-green-100 rounded-lg p-4 text-xs font-mono leading-relaxed min-h-[200px] whitespace-pre-wrap break-words"
                  />
                </div>
              </>
            )}
            {nqModal.timeMs && (
              <div className="text-xs text-default-500">
                时间：{new Date(nqModal.timeMs).toLocaleString()}
              </div>
            )}
          </ModalBody>
          <ModalFooter>
            <Button
              variant="light"
              onPress={() => setNqModal((prev) => ({ ...prev, open: false }))}
            >
              关闭
            </Button>
          </ModalFooter>
        </ModalContent>
      </Modal>

      {/* 安装命令模态框 */}
      <Modal
        backdrop="blur"
        isOpen={installCommandModal}
        placement="center"
        scrollBehavior="outside"
        size="2xl"
        onClose={() => setInstallCommandModal(false)}
      >
        <ModalContent>
          <ModalHeader>安装命令 - {currentNodeName}</ModalHeader>
          <ModalBody>
            <div className="space-y-4">
              <p className="text-sm text-default-600">
                提供两种源（静态/ GitHub），任选其一执行：
              </p>
              {[
                {
                  label: "静态源（panel-static.199028.xyz）",
                  value: installCommands?.static,
                },
                {
                  label: "GitHub 源（raw.githubusercontent.com）",
                  value: installCommands?.github,
                },
                {
                  label: "本地源（面板直链，可选）",
                  value: installCommands?.local,
                },
              ]
                .filter((item) => !!item.value)
                .map((item) => (
                  <div
                    key={item.label}
                    className="relative rounded-lg border border-default-200/70 bg-default-50/30 p-3"
                  >
                    <div className="mb-2 flex items-center justify-between gap-2 text-sm font-medium text-default-700">
                      <span>{item.label}</span>
                      <Button
                        size="sm"
                        variant="flat"
                        color="primary"
                        onPress={() => handleManualCopy(item.value as string)}
                      >
                        复制
                      </Button>
                    </div>
                    <Textarea
                      readOnly
                      className="font-mono text-sm"
                      classNames={{
                        input: "font-mono text-sm",
                      }}
                      maxRows={8}
                      minRows={4}
                      value={item.value || ""}
                      variant="bordered"
                    />
                  </div>
                ))}
              <div className="text-xs text-default-500">
                💡 如复制失败，可手动选择对应命令文本进行复制。
              </div>
            </div>
          </ModalBody>
          <ModalFooter>
            <Button
              variant="flat"
              onPress={() => setInstallCommandModal(false)}
            >
              关闭
            </Button>
          </ModalFooter>
        </ModalContent>
      </Modal>
    </div>
  );
}
