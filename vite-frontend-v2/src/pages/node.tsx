import { useState, useEffect, useRef, useCallback, memo } from "react";
import { useNavigate, useSearchParams } from "react-router-dom";
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
import { Tooltip } from "@heroui/tooltip";
import { Switch } from "@heroui/switch";
import { Terminal } from "xterm";

import "xterm/css/xterm.css";
import { Divider } from "@heroui/divider";
import toast from "react-hot-toast";

import OpsLogModal from "@/components/OpsLogModal";
import VirtualGrid from "@/components/VirtualGrid";
import { getVersionInfo } from "@/api";
import { getCachedConfig } from "@/config/site";
import { getLocalCurrentPanelAddress } from "@/utils/panel";
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
  getAnyTLSCertPreview,
  checkAnyTLSCertChain,
  reissueAnyTLSCert,
  restartGost,
  agentReconcileNode,
  enableGostApi,
  getGostConfig,
  runNQTest,
  getNQResult,
  nodeDiagStart,
  nodeDiagResult,
  nodeIperf3Status,
  nodePprofControl,
  nodePprofFetch,
  listNodeOps,
  listNodeAnyTLSCertLogs,
  listNodeAnyTLSRuntimeLogs,
  getNodeUserUsage,
  upgradeNodeAgentsBatch,
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
  shared?: boolean;
  assignedPortRanges?: string;
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
    agentHeapAllocMb?: number;
    agentHeapInuseMb?: number;
    agentStackInuseMb?: number;
    agentSysMb?: number;
    agentRssMb?: number;
    agentNumGc?: number;
    agentLastGcPauseMs?: number;
    agentGcCpuPercent?: number;
    agentGoRoutines?: number;
    agentMemCollectedAtMs?: number;
  } | null;
  copyLoading?: boolean;
  ssStatus?: string;
  ssLoading?: boolean;
  anytlsPort?: number;
  anytlsPorts?: Array<{ port: number; exitIp?: string }>;
  anytlsCert?: {
    domain?: string;
    notBeforeMs?: number;
    notAfterMs?: number;
    daysLeft?: number;
    state?: "ok" | "expiring" | "expired" | string;
    updatedAtMs?: number;
    source?: "agent_log" | "controller_estimate" | string;
  } | null;
  anytlsRuntime?: {
    state?: "healthy" | "degraded" | "unknown" | string;
    windowSec?: number;
    recentCount?: number;
    starts?: number;
    acceptErr?: number;
    connReject?: number;
    handshakeTimeout?: number;
    tlsClientHello?: number;
    tlsHandshakeErr?: number;
    tlsConnResetByPeer?: number;
    tlsSniMismatch?: number;
    listenErr?: number;
    streamErr?: number;
    authFail?: number;
    readErr?: number;
    outboundErr?: number;
    egressDialErr?: number;
    lastLogMs?: number;
  } | null;
  gostApiBindLoopbackOnly?: boolean;
  gostApiBindDetail?: string;
  gostApiBindCheckedAtMs?: number;
}

type AnyTLSRuntimeLogItem = {
  id: number;
  timeMs: number;
  nodeId: number;
  cmd: string;
  requestId?: string;
  success: number;
  message: string;
  stdout?: string;
  stderr?: string;
};

type AnyTLSRuntimeStatus = {
  state: "healthy" | "degraded" | "unknown" | string;
  windowSec: number;
  windowFromMs: number;
  windowToMs: number;
  recentCount: number;
  totalLogs: number;
  starts: number;
  acceptErr: number;
  connReject: number;
  handshakeTimeout: number;
  tlsClientHello: number;
  tlsHandshakeErr: number;
  tlsConnResetByPeer: number;
  tlsSniMismatch: number;
  listenErr: number;
  streamErr: number;
  authFail: number;
  readErr: number;
  outboundErr: number;
  egressDialErr: number;
  lastLogMs: number;
  lastStartMs: number;
};

interface NodeForm {
  id: number | null;
  name: string;
  ipString: string;
  serverIp: string;
  portSta: number;
  portEnd: number;
}

const nodeHasAnyTLS = (node?: Node | null) => {
  if (!node) return false;
  if ((node.anytlsPort || 0) > 0) return true;
  return Array.isArray(node.anytlsPorts) && node.anytlsPorts.length > 0;
};

type PprofProfile =
  | "goroutine"
  | "heap"
  | "mutex"
  | "block"
  | "threadcreate";

type GoroutineSnapshot = {
  total: number;
  anytlsStacks: number;
  copyConn: number;
  copyPacketConn: number;
  copyStream: number;
  copyPacket: number;
  sessionRecv: number;
};

type HeapSnapshot = {
  heapAlloc: number;
  heapInuse: number;
  heapObjects: number;
  numGC: number;
  maxRSS: number;
};

const sleepMs = (ms: number) => new Promise((resolve) => setTimeout(resolve, ms));

const toMB = (v: number) => (v > 0 ? (v / 1024 / 1024).toFixed(2) : "0.00");

const parseFirstInt = (text: string, re: RegExp): number => {
  const m = text.match(re);

  return m?.[1] ? Number(m[1]) || 0 : 0;
};

const parseGoroutineSnapshot = (content: string): GoroutineSnapshot => {
  const lines = String(content || "").split(/\r?\n/);
  const out: GoroutineSnapshot = {
    total: parseFirstInt(content, /goroutine profile:\s*total\s+(\d+)/i),
    anytlsStacks: 0,
    copyConn: 0,
    copyPacketConn: 0,
    copyStream: 0,
    copyPacket: 0,
    sessionRecv: 0,
  };
  let curCount = 0;
  let curBody = "";
  const flush = () => {
    if (curCount <= 0 || !curBody) return;
    if (curBody.includes("cmd/flux-agent/anytls.go")) out.anytlsStacks += curCount;
    if (curBody.includes("copyConnWithLimiter")) out.copyConn += curCount;
    if (curBody.includes("copyPacketConnWithLimiter")) out.copyPacketConn += curCount;
    if (curBody.includes("copyStreamLimited")) out.copyStream += curCount;
    if (curBody.includes("copyPacketLimited")) out.copyPacket += curCount;
    if (curBody.includes("session.(*Session).recvLoop")) out.sessionRecv += curCount;
  };
  for (const line of lines) {
    const m = line.match(/^(\d+)\s+@/);
    if (m) {
      flush();
      curCount = Number(m[1]) || 0;
      curBody = "";
      continue;
    }
    if (line.startsWith("#")) curBody += line + "\n";
  }
  flush();
  return out;
};

const parseHeapSnapshot = (content: string): HeapSnapshot => ({
  heapAlloc: parseFirstInt(content, /#\s*HeapAlloc\s*=\s*(\d+)/),
  heapInuse: parseFirstInt(content, /#\s*HeapInuse\s*=\s*(\d+)/),
  heapObjects: parseFirstInt(content, /#\s*HeapObjects\s*=\s*(\d+)/),
  numGC: parseFirstInt(content, /#\s*NumGC\s*=\s*(\d+)/),
  maxRSS: parseFirstInt(content, /#\s*MaxRSS\s*=\s*(\d+)/),
});

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

const clampPercent = (v: number) => Math.max(0, Math.min(100, v));

const parsePortRanges = (raw: string): Array<{ start: number; end: number }> => {
  const out: Array<{ start: number; end: number }> = [];
  const cleaned = (raw || "")
    .split(/[,，\s]+/)
    .map((s) => s.trim())
    .filter(Boolean);
  for (const part of cleaned) {
    if (!part) continue;
    const seg = part.split("-");
    if (seg.length === 1) {
      const v = Number(seg[0]);
      if (Number.isFinite(v) && v > 0 && v <= 65535) {
        out.push({ start: v, end: v });
      }
      continue;
    }
    const a = Number(seg[0]);
    const b = Number(seg[1]);
    if (
      Number.isFinite(a) &&
      Number.isFinite(b) &&
      a > 0 &&
      b > 0 &&
      a <= 65535 &&
      b <= 65535
    ) {
      out.push({ start: Math.min(a, b), end: Math.max(a, b) });
    }
  }
  return out;
};

const portInRanges = (port: number, ranges: string): boolean => {
  if (!port || !ranges) return false;
  const list = parsePortRanges(ranges);
  return list.some((r) => port >= r.start && port <= r.end);
};

const recommendPortFromRanges = (ranges: string): number | null => {
  const list = parsePortRanges(ranges);
  if (list.length === 0) return null;
  let min = list[0].start;
  for (const r of list) {
    if (r.start < min) min = r.start;
  }
  return min;
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
  isAdmin: boolean;
  anytlsCertEnabled: boolean;
  onChanged?: () => void;
};

const ExitServiceModal = memo(
  ({
    isOpen,
    onOpenChange,
    node,
    isAdmin,
    anytlsCertEnabled,
    onChanged,
  }: ExitServiceModalProps) => {
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
    const [anytlsPortItems, setAnytlsPortItems] = useState<
      Array<{ id: number; port: string; exitIp: string }>
    >([{ id: Date.now(), port: "", exitIp: "" }]);
    const [anytlsAllowFallback, setAnytlsAllowFallback] = useState(false);
    const [anytlsCertDomain, setAnytlsCertDomain] = useState("");
    const [anytlsReissuing, setAnytlsReissuing] = useState(false);
    type AnyTLSCertLogItem = {
      id: number;
      timeMs: number;
      nodeId: number;
      step: string;
      message: string;
      data?: string;
    };
    const [anytlsCertLogOpen, setAnytlsCertLogOpen] = useState(false);
    const [anytlsCertLogLoading, setAnytlsCertLogLoading] = useState(false);
    const [anytlsCertLogs, setAnytlsCertLogs] = useState<AnyTLSCertLogItem[]>(
      [],
    );
    const [loadedExitOwner, setLoadedExitOwner] = useState("");
    const [exitSession, setExitSession] = useState(0);
    const exitSessionRef = useRef(0);
    const exitWritePendingRef = useRef(false);
    const exitLoadGenerationRef = useRef(0);
    const exitOwnerRef = useRef({ open: false, nodeId: node?.id, type: "ss" });
    if (exitOwnerRef.current.open !== isOpen || exitOwnerRef.current.nodeId !== node?.id) {
      exitSessionRef.current += 1;
    }
    exitOwnerRef.current = { open: isOpen, nodeId: node?.id, type: exitType };
    const currentExitOwner = `${exitSessionRef.current}:${node?.id}:${exitType}`;
    const canSaveExit = isOpen && !!node?.id && !exitWritePendingRef.current && loadedExitOwner === currentExitOwner;

    const assignedRanges = node?.assignedPortRanges || "";
    const isShared = !isAdmin && !!node?.shared;

    const resetDefaults = useCallback(() => {
      const recommended =
        isShared && assignedRanges
          ? recommendPortFromRanges(assignedRanges)
          : null;
      setExitPort(recommended || node?.portSta || 10000);
      setExitPassword("");
      setExitMethod(EXIT_METHODS[0]);
      setExitObserver("console");
      setExitLimiter("");
      setExitRLimiter("");
      setExitMetaItems([]);
      setExitIfaceSel("");
      setAnytlsPortItems([{ id: Date.now(), port: "", exitIp: "" }]);
      setAnytlsAllowFallback(false);
      setAnytlsCertDomain("");
    }, [node?.portSta, assignedRanges, isShared]);

    const loadExitConfig = useCallback(
      async (type: string) => {
        if (!node?.id || !exitOwnerRef.current.open ||
            exitOwnerRef.current.nodeId !== node.id ||
            exitOwnerRef.current.type !== type || exitSessionRef.current !== exitSession) return;
        const session = exitSession;
        const generation = ++exitLoadGenerationRef.current;
        const owner = `${session}:${node.id}:${type}`;
        const isCurrent = () => exitSessionRef.current === session &&
          exitLoadGenerationRef.current === generation && exitOwnerRef.current.open &&
          exitOwnerRef.current.nodeId === node.id && exitOwnerRef.current.type === type;
        setLoadedExitOwner("");
        resetDefaults();

        let dPort = node.portSta || 10000;
        let dPwd = "";
        let dMethod = EXIT_METHODS[0];
        let dObserver = "console";
        let dLimiter = "";
        let dRLimiter = "";
        let dMetaItems: Array<{ id: number; key: string; value: string }> = [];
        let dIfaceSel = "";
        let dExitIp = "";
        let dAnyTLSPortItems: Array<{ id: number; port: string; exitIp: string }> = [
          { id: Date.now(), port: "", exitIp: "" },
        ];
        let dAllowFallback = false;
        let dCertDomain = node?.anytlsCert?.domain || "";

        try {
          const res = await getExitNode(node.id, type);
          if (!isCurrent()) return;
          if (res.code !== 0) return;

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
            } else if (type === "anytls") {
              if (typeof data.exitIp === "string") dExitIp = data.exitIp;
              if (typeof data.allowFallback === "boolean")
                dAllowFallback = data.allowFallback;
              if (typeof data.certDomain === "string")
                dCertDomain = data.certDomain;
              if (Array.isArray(data.anytlsPorts) && data.anytlsPorts.length > 0) {
                dAnyTLSPortItems = data.anytlsPorts
                  .map((it: any) => ({
                    id: Date.now() + Math.random(),
                    port:
                      it && Number(it.port) > 0 ? String(Number(it.port)) : "",
                    exitIp: typeof it?.exitIp === "string" ? it.exitIp : "",
                  }))
                  .filter((it: any) => it.port);
              } else if (typeof data.port === "number" && data.port > 0) {
                dAnyTLSPortItems = [
                  {
                    id: Date.now(),
                    port: String(data.port),
                    exitIp: dExitIp,
                  },
                ];
              }
            }
          }
        } catch { return; }

        if (!isCurrent()) return;
        if (type === "anytls" && anytlsCertEnabled && !dCertDomain) {
          try {
            const preview: any = await getAnyTLSCertPreview(node.id);
            if (preview?.code === 0 && preview?.data?.domain) {
              dCertDomain = String(preview.data.domain);
            }
          } catch {}
        }

        if (!isCurrent()) return;
        if (isShared && assignedRanges) {
          if (!portInRanges(dPort, assignedRanges)) {
            const rec = recommendPortFromRanges(assignedRanges);
            if (rec) dPort = rec;
          }
        }
        setExitPort(dPort);
        setExitPassword(dPwd);
        setExitMethod(dMethod);
        setExitObserver(dObserver);
        setExitLimiter(dLimiter);
        setExitRLimiter(dRLimiter);
        setExitMetaItems(dMetaItems);
        setExitIfaceSel(dIfaceSel);
        if (type === "anytls") {
          setAnytlsPortItems(
            dAnyTLSPortItems.length > 0
              ? dAnyTLSPortItems
              : [{ id: Date.now(), port: "", exitIp: "" }],
          );
          setExitIfaceSel(dExitIp);
          setAnytlsAllowFallback(dAllowFallback);
          setAnytlsCertDomain(dCertDomain);
        }
        setLoadedExitOwner(owner);
      },
      [node?.id, node?.portSta, node?.anytlsCert?.domain, resetDefaults, assignedRanges, isShared, anytlsCertEnabled, exitSession],
    );

    useEffect(() => {
      if (!isOpen || !node) return;
      let active = true;

      setExitType("ss");
      setExitSession(exitSessionRef.current);
      setLoadedExitOwner("");

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
    }, [isOpen, node?.id]);

    useEffect(() => {
      if (!isOpen || !node?.id || exitSession !== exitSessionRef.current) return;
      void loadExitConfig(exitType);
      return () => { exitLoadGenerationRef.current += 1; };
    }, [exitType, isOpen, node?.id, loadExitConfig, exitSession]);

    const submitExit = async () => {
      if (!canSaveExit || exitSubmitting || exitWritePendingRef.current) return;
      const submitNodeId = node?.id;
      const submitType = exitType;
      const submitSession = exitSessionRef.current;
      const submitGeneration = exitLoadGenerationRef.current;
      const ownsSubmit = () => exitOwnerRef.current.open &&
        exitOwnerRef.current.nodeId === submitNodeId && exitOwnerRef.current.type === submitType &&
        exitSessionRef.current === submitSession && exitLoadGenerationRef.current === submitGeneration;
      if (!node?.id) {
        toast.error("无效的节点");

        return;
      }
      if (!exitPort || exitPort < 1 || exitPort > 65535) {
        if (exitType !== "anytls") {
          toast.error("端口无效");
          return;
        }
      }
      if (exitType === "anytls") {
        const rows = anytlsPortItems
          .map((it) => ({
            port: Number(String(it.port || "").trim()),
            exitIp: String(it.exitIp || "").trim(),
          }))
          .filter((it) => it.port > 0);
        if (rows.length === 0) {
          toast.error("请至少配置一个 AnyTLS 端口");
          return;
        }
        if (isShared && assignedRanges) {
          const invalid = rows.find((it) => !portInRanges(it.port, assignedRanges));
          if (invalid) {
            toast.error(`端口 ${invalid.port} 不在授权范围`);
            return;
          }
        }
        if (!exitPassword) {
          toast.error("请填写密码");
          return;
        }
        exitWritePendingRef.current = true;
        setExitSubmitting(true);
        try {
          const res = await setExitNode({
            nodeId: submitNodeId,
            type: "anytls",
            port: rows[0].port,
            anytlsPorts: rows,
            password: exitPassword,
            allowFallback: anytlsAllowFallback,
            certDomain: anytlsCertEnabled ? anytlsCertDomain.trim() : "",
          } as any);
          if (!ownsSubmit()) return;
          if (res.code === 0) {
            toast.success("AnyTLS 已创建/更新");
            onChanged?.();
            onOpenChange(false);
          } else {
            toast.error(res.msg || "操作失败");
          }
        } catch (e) {
          if (ownsSubmit()) toast.error("网络错误");
        } finally {
          exitWritePendingRef.current = false;
          setExitSubmitting(false);
        }

        return;
      }
      if (!exitPassword) {
        toast.error("请填写密码");

        return;
      }
      if (isShared && assignedRanges && !portInRanges(exitPort, assignedRanges)) {
        toast.error("端口不在授权范围");
        return;
      }
      exitWritePendingRef.current = true;
      setExitSubmitting(true);
      try {
        let res;
        const metadata: any = {};

        exitMetaItems.forEach((it: { key: string; value: string }) => {
          if (it.key && it.value) metadata[it.key] = it.value;
        });
        if (exitIfaceSel) {
          (metadata as any)["interface"] = exitIfaceSel;
        }
        res = await setExitNode({
          nodeId: submitNodeId,
          type: "ss",
          port: exitPort,
          password: exitPassword,
          method: exitMethod,
          observer: exitObserver,
          limiter: exitLimiter,
          rlimiter: exitRLimiter,
          metadata,
        } as any);

        if (!ownsSubmit()) return;
        if (res.code === 0) {
          toast.success("出口服务已创建/更新");
          onChanged?.();
          onOpenChange(false);
        } else {
          toast.error(res.msg || "操作失败");
        }
      } catch (e) {
        if (ownsSubmit()) toast.error("网络错误");
      } finally {
        exitWritePendingRef.current = false;
        setExitSubmitting(false);
      }
    };

    const forceReissueAnyTLSCert = async () => {
      if (!node?.id) return;
      if (
        !window.confirm(
          "确认强制重新颁发 AnyTLS 证书并立即下发到该节点吗？",
        )
      ) {
        return;
      }
      setAnytlsReissuing(true);
      try {
        const res: any = await reissueAnyTLSCert(
          node.id,
          anytlsCertDomain.trim() || undefined,
        );
        if (res?.code === 0) {
          toast.success("AnyTLS 证书已重新颁发并下发");
          const domain =
            String(res?.data?.certDomain || "") ||
            String(res?.data?.certStatus?.domain || "");
          if (domain) setAnytlsCertDomain(domain);
          await loadExitConfig("anytls");
          onChanged?.();
        } else {
          toast.error(res?.msg || "证书重签失败");
        }
      } catch (e: any) {
        toast.error(e?.message || "证书重签失败");
      } finally {
        setAnytlsReissuing(false);
      }
    };

    const loadAnyTLSCertLogs = useCallback(async () => {
      if (!node?.id) return;
      setAnytlsCertLogLoading(true);
      try {
        const res: any = await listNodeAnyTLSCertLogs({
          nodeId: node.id,
          limit: 200,
        });
        const list = Array.isArray(res?.data?.logs) ? res.data.logs : [];
        setAnytlsCertLogs(list);
      } catch {
        setAnytlsCertLogs([]);
      } finally {
        setAnytlsCertLogLoading(false);
      }
    }, [node?.id]);

    return (
      <>
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
                  {exitType === "anytls" && anytlsCertEnabled && (
                    <Alert
                      color="primary"
                      description="AnyTLS 使用控制器签发证书。客户端需开启证书校验，不可跳过。"
                      variant="flat"
                    />
                  )}
                  <Input
                    label="端口"
                    type="number"
                    value={String(exitPort)}
                    onChange={(e: any) => setExitPort(Number(e.target.value))}
                  />
                  {isShared && assignedRanges ? (
                    <div className="text-xs text-warning-600">
                      授权端口范围：{assignedRanges}
                    </div>
                  ) : null}
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
                  {exitType === "anytls" && (
                    <div>
                      {anytlsCertEnabled ? (
                        <>
                          <Input
                            description="可输入完整域名；或输入前缀（自动生成 节点IDjs.前缀.docker.com）"
                            label="AnyTLS 证书域名"
                            placeholder="例如: abcd 或 n1.example.com"
                            value={anytlsCertDomain}
                            onChange={(e: any) => setAnytlsCertDomain(e.target.value)}
                          />
                          <Button
                            className="mt-2"
                            size="sm"
                            variant="flat"
                            onPress={() => {
                              const chars = "abcdefghijklmnopqrstuvwxyz0123456789";
                              let s = "";
                              for (let i = 0; i < 8; i++) {
                                s += chars[Math.floor(Math.random() * chars.length)];
                              }
                              setAnytlsCertDomain(s);
                            }}
                          >
                            随机前缀（docker.com）
                          </Button>
                          <Button
                            className="mt-2 ml-2"
                            color="warning"
                            isLoading={anytlsReissuing}
                            size="sm"
                            variant="flat"
                            onPress={() => {
                              void forceReissueAnyTLSCert();
                            }}
                          >
                            强制重新颁发证书
                          </Button>
                          <Button
                            className="mt-2 ml-2"
                            size="sm"
                            variant="flat"
                            onPress={() => {
                              setAnytlsCertLogOpen(true);
                              void loadAnyTLSCertLogs();
                            }}
                          >
                            查看证书安装日志
                          </Button>
                        </>
                      ) : (
                        <Alert
                          className="mb-2"
                          color="default"
                          description="证书功能已关闭：当前 AnyTLS 使用隐匿模式，证书域名与重签入口已隐藏。"
                          variant="flat"
                        />
                      )}
                      <div className="text-sm text-default-600 mb-1">
                        AnyTLS 端口与出口IP（出口IP可选）
                      </div>
                      <div className="space-y-2">
                        {anytlsPortItems.map((row, idx) => (
                          <div key={row.id} className="grid grid-cols-12 gap-2 items-end">
                            <Input
                              className="col-span-4"
                              label={idx === 0 ? "端口" : `端口 #${idx + 1}`}
                              type="number"
                              value={row.port}
                              onChange={(e: any) =>
                                setAnytlsPortItems((prev) =>
                                  prev.map((it) =>
                                    it.id === row.id ? { ...it, port: e.target.value } : it,
                                  ),
                                )
                              }
                            />
                            <Select
                              className="col-span-7"
                              isDisabled={exitIfaces.length === 0}
                              label="出口IP"
                              placeholder={exitIfaces.length ? "选择出口IP" : "未获取到出口IP列表"}
                              selectedKeys={row.exitIp ? [row.exitIp] : []}
                              onSelectionChange={(keys) => {
                                const val = Array.from(keys as Set<string>)[0] || "";
                                setAnytlsPortItems((prev) =>
                                  prev.map((it) =>
                                    it.id === row.id ? { ...it, exitIp: val } : it,
                                  ),
                                );
                              }}
                            >
                              {exitIfaces.map((ip) => (
                                <SelectItem key={ip}>{ip}</SelectItem>
                              ))}
                            </Select>
                            <Button
                              className="col-span-1"
                              color="danger"
                              isDisabled={anytlsPortItems.length <= 1}
                              size="sm"
                              variant="light"
                              onPress={() =>
                                setAnytlsPortItems((prev) => prev.filter((it) => it.id !== row.id))
                              }
                            >
                              删
                            </Button>
                          </div>
                        ))}
                        <Button
                          size="sm"
                          variant="flat"
                          onPress={() =>
                            setAnytlsPortItems((prev) => [
                              ...prev,
                              { id: Date.now() + Math.random(), port: "", exitIp: "" },
                            ])
                          }
                        >
                          新增端口
                        </Button>
                      </div>
                      <div className="mt-3">
                        <Switch
                          isSelected={anytlsAllowFallback}
                          onValueChange={setAnytlsAllowFallback}
                        >
                          允许 IPv4/IPv6 回退
                        </Switch>
                        <div className="text-xs text-default-500 mt-1">
                          当出口 IP 为 IPv6 且目标无 AAAA 记录时，允许自动回退到 IPv4
                        </div>
                      </div>
                    </div>
                  )}
                </div>
              </ModalBody>
              <ModalFooter>
                <Button variant="light" onPress={onClose}>
                  关闭
                </Button>
                <Button
                  color="primary"
                  isDisabled={!canSaveExit || exitSubmitting}
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
        <Modal
          backdrop="opaque"
          disableAnimation
          isOpen={anytlsCertLogOpen}
          size="2xl"
          onOpenChange={setAnytlsCertLogOpen}
        >
          <ModalContent>
            {(onClose) => (
              <>
                <ModalHeader>
                  AnyTLS 证书安装日志{node?.name ? ` · ${node.name}` : ""}
                </ModalHeader>
                <ModalBody>
                  <div className="flex items-center justify-between mb-2">
                    <div className="text-xs text-default-500">
                      仅显示该节点 agent 上报的证书安装/更新/重载日志
                    </div>
                    <Button
                      isLoading={anytlsCertLogLoading}
                      size="sm"
                      variant="flat"
                      onPress={() => {
                        void loadAnyTLSCertLogs();
                      }}
                    >
                      刷新
                    </Button>
                  </div>
                  {anytlsCertLogLoading ? (
                    <div className="py-8 flex justify-center">
                      <Spinner size="sm" />
                    </div>
                  ) : anytlsCertLogs.length === 0 ? (
                    <div className="text-sm text-default-500 py-6 text-center">
                      暂无证书日志
                    </div>
                  ) : (
                    <div className="space-y-2 max-h-[55vh] overflow-y-auto">
                      {anytlsCertLogs.map((it) => (
                        <div
                          key={it.id}
                          className="rounded-lg border border-default-200 p-2"
                        >
                          <div className="text-xs text-default-500 mb-1">
                            {it.timeMs
                              ? new Date(it.timeMs).toLocaleString()
                              : "-"}
                            {it.step ? ` · ${it.step}` : ""}
                          </div>
                          <div className="text-sm">{it.message || "-"}</div>
                          {it.data ? (
                            <pre className="mt-2 text-xs bg-default-100 rounded p-2 overflow-x-auto whitespace-pre-wrap break-all">
                              {it.data}
                            </pre>
                          ) : null}
                        </div>
                      ))}
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
      </>
    );
  },
);

export default function NodePage() {
  const navigate = useNavigate();
  const [searchParams, setSearchParams] = useSearchParams();
  const initialKeyword = searchParams.get("q") || "";
  const initialConn =
    searchParams.get("conn") === "online" || searchParams.get("conn") === "offline"
      ? (searchParams.get("conn") as "online" | "offline")
      : "all";
  const initialAnyTLS =
    searchParams.get("anytls") === "healthy" ||
    searchParams.get("anytls") === "degraded" ||
    searchParams.get("anytls") === "unknown"
      ? (searchParams.get("anytls") as "healthy" | "degraded" | "unknown")
      : "all";
  const [nodeList, setNodeList] = useState<Node[]>([]);
  const [nodeKeyword, setNodeKeyword] = useState(initialKeyword);
  const [nodeConnFilter, setNodeConnFilter] = useState<
    "all" | "online" | "offline"
  >(initialConn);
  const [nodeAnyTLSFilter, setNodeAnyTLSFilter] = useState<
    "all" | "healthy" | "degraded" | "unknown"
  >(initialAnyTLS);
  const [loading, setLoading] = useState(false);
  const gridWrapRef = useRef<HTMLDivElement | null>(null);
  const [nodeRowHeight, setNodeRowHeight] = useState(360);
  const [gridReady, setGridReady] = useState(false);
  const [dialogVisible, setDialogVisible] = useState(false);
  const [editNode, setEditNode] = useState<Node | null>(null);
  const [deleteModalOpen, setDeleteModalOpen] = useState(false);
  const [deleteLoading, setDeleteLoading] = useState(false);
  const [nodeToDelete, setNodeToDelete] = useState<Node | null>(null);
  const [deleteAlsoUninstall, setDeleteAlsoUninstall] = useState(false);
  const [exitNode, setExitNode] = useState<Node | null>(null);

  // 出口服务设置
  const [exitModalOpen, setExitModalOpen] = useState(false);
  const [upgradeModalOpen, setUpgradeModalOpen] = useState(false);
  const [upgradeSummary, setUpgradeSummary] = useState<
    Record<
      number,
      {
        status: "success" | "failed" | "running" | "unknown";
        timeMs: number;
        message: string;
        step: string;
      }
    >
  >({});
  const [upgradeLogs, setUpgradeLogs] = useState<
    Array<{ timeMs: number; cmd: string; message: string }>
  >([]);
  const [upgradeLoading, setUpgradeLoading] = useState(false);
  const [upgradeAllLoading, setUpgradeAllLoading] = useState(false);
  const [upgradeNodeLoading, setUpgradeNodeLoading] = useState<
    Record<number, boolean>
  >({});
  const [upgradeNodeId, setUpgradeNodeId] = useState<number | null>(null);

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
  const [usageModal, setUsageModal] = useState<{
    open: boolean;
    nodeId: number | null;
    nodeName: string;
    loading: boolean;
    items: Array<{
      userId: number;
      userName: string;
      inFlow: number;
      outFlow: number;
      flow: number;
    }>;
  }>({
    open: false,
    nodeId: null,
    nodeName: "",
    loading: false,
    items: [],
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
    nodeId: number | null;
    loading: boolean;
    result: any | null;
  }>({
    open: false,
    nodeName: "",
    nodeId: null,
    loading: false,
    result: null,
  });
  const [iperf3Status, setIperf3Status] = useState<{
    status: string;
    port: string;
    pid: string;
    loading: boolean;
  }>({
    status: "unknown",
    port: "",
    pid: "",
    loading: false,
  });
  const [iperf3Map, setIperf3Map] = useState<
    Record<number, { status: string; port: string; loading: boolean }>
  >({});
  const [diagState, setDiagState] = useState<{
    nodeId: number | null;
    nodeName: string;
    kind: string;
    loading: boolean;
    content: string;
    done: boolean;
    requestId: string;
  }>({
    nodeId: null,
    nodeName: "",
    kind: "",
    loading: false,
    content: "",
    done: false,
    requestId: "",
  });
  const [pprofState, setPprofState] = useState<{
    enabled: boolean;
    addr: string;
    loading: boolean;
    fetchLoading: boolean;
    quickTesting: boolean;
    content: string;
    profile: PprofProfile;
  }>({
    enabled: false,
    addr: "",
    loading: false,
    fetchLoading: false,
    quickTesting: false,
    content: "",
    profile: "goroutine",
  });
  const logScrollRef = useRef<HTMLDivElement | null>(null);
  const diagPollRef = useRef<number | null>(null);
  const iperfPollRef = useRef<number | null>(null);
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
  const [certChainModal, setCertChainModal] = useState<{
    open: boolean;
    title: string;
    data: any | null;
  }>({
    open: false,
    title: "",
    data: null,
  });
  const [anytlsLogModal, setAnytlsLogModal] = useState<{
    open: boolean;
    nodeId: number | null;
    nodeName: string;
    loading: boolean;
    logs: AnyTLSRuntimeLogItem[];
    status: AnyTLSRuntimeStatus | null;
  }>({
    open: false,
    nodeId: null,
    nodeName: "",
    loading: false,
    logs: [],
    status: null,
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
    usedPortsModal.open ||
    usageModal.open ||
    certChainModal.open ||
    anytlsLogModal.open;
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
  const [certRefreshLoading, setCertRefreshLoading] = useState<
    Record<number, boolean>
  >({});
  const [certChainLoading, setCertChainLoading] = useState<Record<number, boolean>>(
    {},
  );
  const [anytlsCertEnabled, setAnytlsCertEnabled] = useState(false);
  const pageVisible = usePageVisibility();
  const [pollMs, setPollMs] = useState<number>(5000);
  const pendingWsStatusRef = useRef<Map<number, "online" | "offline">>(
    new Map(),
  );
  const pendingWsInfoRef = useRef<Map<number, any>>(new Map());
  const pendingIperf3Ref = useRef<
    Map<number, { status: string; port: string; loading: boolean }>
  >(new Map());
  const wsFlushTimerRef = useRef<number | null>(null);

  useEffect(() => {
    loadNodes();
    initWebSocket();

    return () => {
      if (wsFlushTimerRef.current != null) {
        window.clearTimeout(wsFlushTimerRef.current);
        wsFlushTimerRef.current = null;
      }
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
      try {
        const v = await getCachedConfig("anytls_cert_enabled");
        const s = String(v || "")
          .trim()
          .toLowerCase();
        setAnytlsCertEnabled(
          s === "1" || s === "true" || s === "yes" || s === "on" || s === "enabled",
        );
      } catch {}
    })();
  }, []);

  useEffect(() => {
    const handler = async (ev: any) => {
      const changed = Array.isArray(ev?.detail?.changedKeys)
        ? (ev.detail.changedKeys as string[])
        : [];
      if (changed.length > 0 && !changed.includes("anytls_cert_enabled")) return;
      try {
        const v = await getCachedConfig("anytls_cert_enabled");
        const s = String(v || "")
          .trim()
          .toLowerCase();
        setAnytlsCertEnabled(
          s === "1" || s === "true" || s === "yes" || s === "on" || s === "enabled",
        );
      } catch {}
    };
    window.addEventListener("configUpdated", handler);
    return () => window.removeEventListener("configUpdated", handler);
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

  const escapeTerminalHtml = (value: unknown) =>
    String(value ?? "").replace(/[&<>"']/g, (ch) => {
      const escapes: Record<string, string> = {
        "&": "&amp;",
        "<": "&lt;",
        ">": "&gt;",
        '"': "&quot;",
        "'": "&#39;",
      };

      return escapes[ch] || ch;
    });

  const sanitizeTerminalColor = (value: string | null, fallback: string) => {
    const raw = String(value || "").trim();

    return /^#(?:[0-9a-fA-F]{3}|[0-9a-fA-F]{6}|[0-9a-fA-F]{8})$/.test(raw)
      ? raw
      : fallback;
  };

  const openTerminalWindow = (node: Node) => {
    const token = localStorage.getItem("token") || "";
    const proto = window.location.protocol === "https:" ? "wss" : "ws";
    const wsUrl = `${proto}://${window.location.host}/api/v1/node/${node.id}/terminal?token=${encodeURIComponent(token)}`;
    const safeNodeName = escapeTerminalHtml(node.name);
    const termBg = sanitizeTerminalColor(localStorage.getItem("term_bg"), "#151729");
    const termFg = sanitizeTerminalColor(localStorage.getItem("term_fg"), "#209d5f");
    const html = `
<!DOCTYPE html>
<html>
<head>
  <meta charset="utf-8" />
  <meta name="viewport" content="width=device-width, initial-scale=1.0" />
  <title>终端 - ${safeNodeName}</title>
  <link rel="stylesheet" href="https://cdn.jsdelivr.net/npm/xterm@5.3.0/css/xterm.css" />
  <style>
    html, body { margin:0; padding:0; width:100%; height:100%; background:#000; color:#d1d5db; font-family: monospace; }
    #layout { display:flex; width:100%; height:100%; padding:0; box-sizing:border-box; gap:6px; }
    #term-wrap { flex: 1 1 90%; background: ${termBg}; border-radius:6px; overflow:hidden; }
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
        <div>${safeNodeName}</div>
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
        background: ${JSON.stringify(termBg)},
        foreground: ${JSON.stringify(termFg)}
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
          mappedNodes.forEach(async (node: any) => {
            try {
              const r: any = await nodeIperf3Status(node.id);
              if (r.code === 0 && r.data) {
                setIperf3Map((prev) => ({
                  ...prev,
                  [node.id]: {
                    status: r.data.status || "unknown",
                    port: r.data.port || "",
                    loading: false,
                  },
                }));
              }
            } catch {}
          });
        }
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

  const openUsageModal = useCallback(async (node: Node) => {
    setUsageModal({
      open: true,
      nodeId: node.id,
      nodeName: node.name,
      loading: true,
      items: [],
    });
    try {
      const res: any = await getNodeUserUsage({ nodeId: node.id });
      if (res && res.code === 0) {
        const items = (res.data || []).map((it: any) => ({
          userId: Number(it.userId || it.user_id || 0),
          userName: it.userName || it.user || it.user_name || `用户${it.userId}`,
          inFlow: Number(it.inFlow || it.in_flow || 0),
          outFlow: Number(it.outFlow || it.out_flow || 0),
          flow:
            Number(it.inFlow || it.in_flow || 0) +
            Number(it.outFlow || it.out_flow || 0),
        }));
        setUsageModal((prev) => ({
          ...prev,
          loading: false,
          items,
        }));
      } else {
        toast.error(res?.msg || "获取节点用量失败");
        setUsageModal((prev) => ({ ...prev, loading: false }));
      }
    } catch (e: any) {
      toast.error(e?.message || "获取节点用量失败");
      setUsageModal((prev) => ({ ...prev, loading: false }));
    }
  }, []);

  const goNetwork = (node: Node) => {
    navigate(`/network/${node.id}`);
  };

  const parseWsSystemInfo = (messageData: any) => {
    try {
      if (typeof messageData === "string") {
        return JSON.parse(messageData);
      }

      return messageData;
    } catch {
      return null;
    }
  };

  const applyWsSystemInfo = (node: Node, systemInfo: any): Node => {
    if (!systemInfo || Object.keys(systemInfo).length === 0) {
      return node;
    }

    const toNum = (v: any): number | undefined => {
      if (v === null || typeof v === "undefined" || v === "") return undefined;
      const n = Number(v);

      return Number.isFinite(n) ? n : undefined;
    };
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

        if (currentUpload >= lastUpload && uploadDiff >= 0) {
          uploadSpeed = uploadDiff / timeDiff;
        }

        if (currentDownload >= lastDownload && downloadDiff >= 0) {
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
        uploadSpeed,
        downloadSpeed,
        uptime: currentUptime,
        gostApi: !!systemInfo.gost_api,
        gostRunning: !!systemInfo.gost_running,
        gostApiConfigured:
          systemInfo.gost_api_configured !== undefined
            ? !!systemInfo.gost_api_configured
            : !!systemInfo.gost_api,
        agentHeapAllocMb: toNum(systemInfo.agent_heap_alloc_mb),
        agentHeapInuseMb: toNum(systemInfo.agent_heap_inuse_mb),
        agentStackInuseMb: toNum(systemInfo.agent_stack_inuse_mb),
        agentSysMb: toNum(systemInfo.agent_sys_mb),
        agentRssMb: toNum(systemInfo.agent_rss_mb),
        agentNumGc: toNum(systemInfo.agent_num_gc),
        agentLastGcPauseMs: toNum(systemInfo.agent_last_gc_pause_ms),
        agentGcCpuPercent: toNum(systemInfo.agent_gc_cpu_percent),
        agentGoRoutines: toNum(systemInfo.agent_go_routines),
        agentMemCollectedAtMs: toNum(systemInfo.agent_mem_collected_at_ms),
      },
    };
  };

  const flushWsNodeUpdates = () => {
    wsFlushTimerRef.current = null;
    const statusMap = new Map(pendingWsStatusRef.current);
    const infoMap = new Map(pendingWsInfoRef.current);
    const iperfMap = new Map(pendingIperf3Ref.current);

    pendingWsStatusRef.current.clear();
    pendingWsInfoRef.current.clear();
    pendingIperf3Ref.current.clear();

    if (statusMap.size > 0 || infoMap.size > 0) {
      setNodeList((prev) => {
        let changed = false;
        const next = prev.map((node) => {
          let out = node;
          const status = statusMap.get(node.id);

          if (status && status !== out.connectionStatus) {
            out = {
              ...out,
              connectionStatus: status,
              systemInfo: out.systemInfo,
            };
          }
          if (infoMap.has(node.id)) {
            const updated = applyWsSystemInfo(out, infoMap.get(node.id));

            if (updated !== out) {
              out = updated;
            }
          }
          if (out !== node) {
            changed = true;
          }

          return out;
        });

        return changed ? next : prev;
      });
    }

    if (iperfMap.size > 0) {
      setIperf3Map((prev) => {
        let changed = false;
        const next = { ...prev };

        iperfMap.forEach((value, id) => {
          const old = prev[id];

          if (
            !old ||
            old.status !== value.status ||
            old.port !== value.port ||
            old.loading !== value.loading
          ) {
            next[id] = value;
            changed = true;
          }
        });

        return changed ? next : prev;
      });
    }
  };

  const scheduleWsNodeFlush = () => {
    if (wsFlushTimerRef.current != null) return;
    wsFlushTimerRef.current = window.setTimeout(flushWsNodeUpdates, 200);
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

    // 构建 WebSocket URL：优先跟随设置里的后端地址，其次 VITE_API_BASE
    const localPanel = getLocalCurrentPanelAddress();
    const apiBase = localPanel?.address || import.meta.env.VITE_API_BASE || "";
    let wsUrl = "";
    if (apiBase) {
      try {
        const u = new URL(apiBase);
        const wsScheme = u.protocol === "https:" ? "wss" : "ws";
        wsUrl = `${wsScheme}://${u.host}/system-info?type=0`;
      } catch {
        wsUrl = "";
      }
    }
    if (!wsUrl) {
      const loc = window.location;
      const wsScheme = loc.protocol === "https:" ? "wss" : "ws";
      wsUrl = `${wsScheme}://${loc.host}/system-info?type=0`;
    }

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
    const nodeID = Number(id);

    if (!Number.isFinite(nodeID) || nodeID <= 0) return;

    if (type === "status") {
      pendingWsStatusRef.current.set(
        nodeID,
        messageData === 1 ? "online" : "offline",
      );
      scheduleWsNodeFlush();
    } else if (type === "info") {
      const systemInfo = parseWsSystemInfo(messageData);

      if (!systemInfo || Object.keys(systemInfo).length === 0) return;

      pendingWsInfoRef.current.set(nodeID, systemInfo);

      try {
        const iperfStatus = systemInfo?.iperf3_status;
        const iperfPort = systemInfo?.iperf3_port;

        if (iperfStatus !== undefined) {
          const nextIperf = {
            status: String(iperfStatus),
            port: iperfPort ? String(iperfPort) : "",
            loading: false,
          };

          pendingIperf3Ref.current.set(nodeID, nextIperf);
          if (selfCheckModal.open && selfCheckModal.nodeId === nodeID) {
            setIperf3Status((prev) => ({
              ...prev,
              ...nextIperf,
            }));
          }
        }
      } catch {}
      scheduleWsNodeFlush();
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

  const doForceRefreshAnyTLSCert = async (node: Node) => {
    if (!node?.id) return;
    if (!anytlsCertEnabled) {
      toast.error("证书功能已关闭");
      return;
    }
    if (!nodeHasAnyTLS(node)) {
      toast.error("该节点未启用 AnyTLS 出口");
      return;
    }
    if (!window.confirm(`确认强制刷新 ${node.name} 的 AnyTLS 证书吗？`)) {
      return;
    }
    setCertRefreshLoading((prev) => ({ ...prev, [node.id]: true }));
    try {
      const res: any = await reissueAnyTLSCert(
        node.id,
        node.anytlsCert?.domain || undefined,
      );
      if (res?.code === 0) {
        toast.success("证书已重签并下发");
        await loadNodes(true, true);
      } else {
        toast.error(res?.msg || "证书刷新失败");
      }
    } catch (e: any) {
      toast.error(e?.message || "证书刷新失败");
    } finally {
      setCertRefreshLoading((prev) => ({ ...prev, [node.id]: false }));
    }
  };

  const doCheckAnyTLSCertChain = async (node: Node) => {
    if (!node?.id) return;
    if (!anytlsCertEnabled) {
      toast.error("证书功能已关闭");
      return;
    }
    if (!nodeHasAnyTLS(node)) {
      toast.error("该节点未启用 AnyTLS 出口");
      return;
    }
    setCertChainLoading((prev) => ({ ...prev, [node.id]: true }));
    try {
      const res: any = await checkAnyTLSCertChain(node.id);
      if (res?.code === 0) {
        const data = res?.data || {};
        setCertChainModal({
          open: true,
          title: `AnyTLS 链校验 · ${node.name}`,
          data,
        });
        if (data?.verifyOK) {
          toast.success("证书链校验通过");
        } else {
          toast.error(data?.verifyErr || "证书链校验失败");
        }
      } else {
        toast.error(res?.msg || "链校验失败");
      }
    } catch (e: any) {
      toast.error(e?.message || "链校验失败");
    } finally {
      setCertChainLoading((prev) => ({ ...prev, [node.id]: false }));
    }
  };

  const loadAnyTLSRuntimeLogs = useCallback(
    async (nodeId: number) => {
      if (!nodeId) return;
      setAnytlsLogModal((prev) => ({ ...prev, loading: true }));
      try {
        const res: any = await listNodeAnyTLSRuntimeLogs({
          nodeId,
          limit: 200,
          windowSec: 900,
        });
        const logs = Array.isArray(res?.data?.logs) ? res.data.logs : [];
        const status = res?.data?.status || null;
        setAnytlsLogModal((prev) => ({
          ...prev,
          logs,
          status,
          loading: false,
        }));
      } catch {
        setAnytlsLogModal((prev) => ({
          ...prev,
          logs: [],
          status: null,
          loading: false,
        }));
      }
    },
    [],
  );

  const openAnyTLSLogModal = useCallback(
    (node: Node) => {
      setAnytlsLogModal({
        open: true,
        nodeId: node.id,
        nodeName: node.name,
        loading: true,
        logs: [],
        status: null,
      });
      void loadAnyTLSRuntimeLogs(node.id);
    },
    [loadAnyTLSRuntimeLogs],
  );

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

  // 格式化流量
  const formatTraffic = (bytes: number): string => {
    if (bytes === 0) return "0 B";

    const k = 1024;
    const sizes = ["B", "KB", "MB", "GB", "TB"];
    const i = Math.floor(Math.log(bytes) / Math.log(k));

    return parseFloat((bytes / Math.pow(k, i)).toFixed(2)) + " " + sizes[i];
  };

  const formatMB = (val?: number): string =>
    typeof val === "number" && Number.isFinite(val) ? `${val.toFixed(1)} MB` : "-";

  const formatFlowBytes = (bytes: number): string => {
    if (!bytes || bytes <= 0) return "0 B";
    const k = 1024;
    const sizes = ["B", "KB", "MB", "GB", "TB"];
    const i = Math.floor(Math.log(bytes) / Math.log(k));
    return parseFloat((bytes / Math.pow(k, i)).toFixed(2)) + " " + sizes[i];
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
    setDiagState({
      nodeId: node.id,
      nodeName: node.name,
      kind: "",
      loading: false,
      content: "",
      done: false,
      requestId: "",
    });
    stopIperfPolling();
    setIperf3Status({ status: "unknown", port: "", pid: "", loading: true });
    setPprofState({
      enabled: false,
      addr: "",
      loading: isAdmin,
      fetchLoading: false,
      quickTesting: false,
      content: "",
      profile: "goroutine",
    });
    setIperf3Map((prev) => ({
      ...prev,
      [node.id]: {
        status: prev[node.id]?.status || "unknown",
        port: prev[node.id]?.port || "",
        loading: true,
      },
    }));
    pollIperfStatus(node.id);
    setSelfCheckModal({
      open: true,
      nodeName: node.name,
      nodeId: node.id,
      loading: true,
      result: null,
    });
    if (isAdmin) {
      try {
        const rs: any = await nodePprofControl(node.id, "status");
        if (rs?.code === 0 && rs?.data) {
          setPprofState((prev) => ({
            ...prev,
            enabled: !!rs.data.enabled,
            addr: String(rs.data.addr || ""),
            loading: false,
          }));
        } else {
          setPprofState((prev) => ({ ...prev, loading: false }));
        }
      } catch {
        setPprofState((prev) => ({ ...prev, loading: false }));
      }
    }
    try {
      const res: any = await nodeSelfCheck(node.id);

      if (res && res.code === 0) {
        setSelfCheckModal({
          open: true,
          nodeName: node.name,
          nodeId: node.id,
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

  const stopDiagPolling = () => {
    if (diagPollRef.current) {
      window.clearTimeout(diagPollRef.current);
      diagPollRef.current = null;
    }
  };

  const stopIperfPolling = () => {
    if (iperfPollRef.current) {
      window.clearTimeout(iperfPollRef.current);
      iperfPollRef.current = null;
    }
  };

  const refreshPprofStatus = useCallback(async (nodeId: number) => {
    setPprofState((prev) => ({ ...prev, loading: true }));
    try {
      const rs: any = await nodePprofControl(nodeId, "status");
      if (rs?.code === 0 && rs?.data) {
        setPprofState((prev) => ({
          ...prev,
          enabled: !!rs.data.enabled,
          addr: String(rs.data.addr || ""),
          loading: false,
        }));
      } else {
        setPprofState((prev) => ({ ...prev, loading: false }));
        toast.error(rs?.msg || "获取 pprof 状态失败");
      }
    } catch (e: any) {
      setPprofState((prev) => ({ ...prev, loading: false }));
      toast.error(e?.message || "获取 pprof 状态失败");
    }
  }, []);

  const handlePprofControl = useCallback(
    async (action: "enable" | "disable") => {
      const nodeId = selfCheckModal.nodeId;
      if (!nodeId) return;
      setPprofState((prev) => ({ ...prev, loading: true }));
      try {
        const rs: any = await nodePprofControl(nodeId, action);
        if (rs?.code === 0 && rs?.data) {
          setPprofState((prev) => ({
            ...prev,
            enabled: !!rs.data.enabled,
            addr: String(rs.data.addr || ""),
            loading: false,
          }));
          toast.success(action === "enable" ? "pprof 已开启" : "pprof 已关闭");
        } else {
          setPprofState((prev) => ({ ...prev, loading: false }));
          toast.error(rs?.msg || "pprof 操作失败");
        }
      } catch (e: any) {
        setPprofState((prev) => ({ ...prev, loading: false }));
        toast.error(e?.message || "pprof 操作失败");
      }
    },
    [selfCheckModal.nodeId],
  );

  const handlePprofFetch = useCallback(
    async (profile: PprofProfile) => {
      const nodeId = selfCheckModal.nodeId;
      if (!nodeId) return;
      setPprofState((prev) => ({
        ...prev,
        fetchLoading: true,
        quickTesting: false,
        profile,
      }));
      try {
        const rs: any = await nodePprofFetch(nodeId, profile, 1);
        if (rs?.code === 0 && rs?.data) {
          setPprofState((prev) => ({
            ...prev,
            fetchLoading: false,
            content: String(rs.data.content || ""),
            addr: String(rs.data.addr || prev.addr),
          }));
        } else {
          setPprofState((prev) => ({ ...prev, fetchLoading: false }));
          toast.error(rs?.msg || "拉取 pprof 失败");
        }
      } catch (e: any) {
        setPprofState((prev) => ({ ...prev, fetchLoading: false }));
        toast.error(e?.message || "拉取 pprof 失败");
      }
    },
    [selfCheckModal.nodeId],
  );

  const handlePprofQuickTest = useCallback(async () => {
    const nodeId = selfCheckModal.nodeId;
    if (!nodeId) return;
    const startAt = Date.now();
    setPprofState((prev) => ({
      ...prev,
      fetchLoading: true,
      quickTesting: true,
      profile: "goroutine",
      content: "[quick-test] 正在准备...",
    }));
    try {
      let pprofAddr = String(pprofState.addr || "");
      if (!pprofState.enabled) {
        const en: any = await nodePprofControl(nodeId, "enable");
        if (!(en?.code === 0 && en?.data?.enabled)) {
          throw new Error(en?.msg || "pprof 开启失败");
        }
        pprofAddr = String(en.data.addr || "");
        setPprofState((prev) => ({
          ...prev,
          enabled: true,
          addr: pprofAddr,
          loading: false,
        }));
      }

      const baselineRs: any = await nodePprofFetch(nodeId, "goroutine", 1);
      if (!(baselineRs?.code === 0 && baselineRs?.data?.content)) {
        throw new Error(baselineRs?.msg || "获取基线 goroutine 失败");
      }
      const baseContent = String(baselineRs.data.content || "");
      const base = parseGoroutineSnapshot(baseContent);
      pprofAddr = String(baselineRs?.data?.addr || pprofAddr);
      setPprofState((prev) => ({
        ...prev,
        content: `[quick-test] 基线 goroutine=${base.total}，正在触发短负载...`,
        addr: pprofAddr || prev.addr,
      }));

      let selfCheckOk = 0;
      let selfCheckErr = 0;
      for (let i = 0; i < 2; i++) {
        try {
          const rs: any = await nodeSelfCheck(nodeId);
          if (rs?.code === 0) selfCheckOk++;
          else selfCheckErr++;
        } catch {
          selfCheckErr++;
        }
        await sleepMs(800);
      }

      setPprofState((prev) => ({
        ...prev,
        content: "[quick-test] 负载完成，等待 8 秒观察回落...",
      }));
      await sleepMs(8000);

      const afterRs: any = await nodePprofFetch(nodeId, "goroutine", 1);
      if (!(afterRs?.code === 0 && afterRs?.data?.content)) {
        throw new Error(afterRs?.msg || "获取结束 goroutine 失败");
      }
      const afterContent = String(afterRs.data.content || "");
      const after = parseGoroutineSnapshot(afterContent);
      pprofAddr = String(afterRs?.data?.addr || pprofAddr);

      const heapRs: any = await nodePprofFetch(nodeId, "heap", 1);
      const heapContent =
        heapRs?.code === 0 && heapRs?.data?.content
          ? String(heapRs.data.content || "")
          : "";
      const heap = parseHeapSnapshot(heapContent);

      const fmt = (n: number) => n.toLocaleString("en-US");
      const sign = (n: number) => (n >= 0 ? `+${n}` : `${n}`);
      const report = [
        "[Agent pprof quick-test report]",
        `node=${selfCheckModal.nodeName} id=${nodeId}`,
        `time=${new Date().toISOString()}`,
        `pprof_addr=${pprofAddr || "-"}`,
        `window_seconds=${Math.max(1, Math.round((Date.now() - startAt) / 1000))}`,
        `self_check_trigger=ok:${selfCheckOk},err:${selfCheckErr}`,
        "",
        "[goroutine baseline]",
        `total=${fmt(base.total)}`,
        `anytls_stacks=${fmt(base.anytlsStacks)}`,
        `copyConnWithLimiter=${fmt(base.copyConn)}`,
        `copyPacketConnWithLimiter=${fmt(base.copyPacketConn)}`,
        `copyStreamLimited=${fmt(base.copyStream)}`,
        `copyPacketLimited=${fmt(base.copyPacket)}`,
        `session.recvLoop=${fmt(base.sessionRecv)}`,
        "",
        "[goroutine after]",
        `total=${fmt(after.total)} (${sign(after.total - base.total)})`,
        `anytls_stacks=${fmt(after.anytlsStacks)} (${sign(after.anytlsStacks - base.anytlsStacks)})`,
        `copyConnWithLimiter=${fmt(after.copyConn)} (${sign(after.copyConn - base.copyConn)})`,
        `copyPacketConnWithLimiter=${fmt(after.copyPacketConn)} (${sign(after.copyPacketConn - base.copyPacketConn)})`,
        `copyStreamLimited=${fmt(after.copyStream)} (${sign(after.copyStream - base.copyStream)})`,
        `copyPacketLimited=${fmt(after.copyPacket)} (${sign(after.copyPacket - base.copyPacket)})`,
        `session.recvLoop=${fmt(after.sessionRecv)} (${sign(after.sessionRecv - base.sessionRecv)})`,
        "",
        "[heap snapshot]",
        `heap_alloc_mb=${toMB(heap.heapAlloc)}`,
        `heap_inuse_mb=${toMB(heap.heapInuse)}`,
        `heap_objects=${fmt(heap.heapObjects)}`,
        `num_gc=${fmt(heap.numGC)}`,
        `max_rss_mb=${toMB(heap.maxRSS)}`,
      ].join("\n");

      setPprofState((prev) => ({
        ...prev,
        fetchLoading: false,
        quickTesting: false,
        profile: "goroutine",
        addr: pprofAddr || prev.addr,
        content: report,
      }));
      toast.success("一键回归测试完成，可复制报告");
    } catch (e: any) {
      setPprofState((prev) => ({
        ...prev,
        fetchLoading: false,
        quickTesting: false,
        content: `[quick-test] 失败：${String(e?.message || "未知错误")}`,
      }));
      toast.error(e?.message || "一键回归测试失败");
    }
  }, [selfCheckModal.nodeId, selfCheckModal.nodeName, pprofState.addr, pprofState.enabled]);

  const copyPprofReport = useCallback(async () => {
    const text = String(pprofState.content || "").trim();
    if (!text) {
      toast.error("暂无报告可复制");
      return;
    }
    try {
      await navigator.clipboard.writeText(text);
      toast.success("报告已复制");
    } catch {
      toast.error("复制失败");
    }
  }, [pprofState.content]);

  const pollIperfStatus = async (nodeId: number) => {
    try {
      const res: any = await nodeIperf3Status(nodeId);
      if (res?.code === 0 && res.data) {
        setIperf3Status((prev) => ({
          ...prev,
          status: res.data.status || "unknown",
          port: res.data.port || "",
          pid: res.data.pid || "",
          loading: false,
        }));
        setIperf3Map((prev) => ({
          ...prev,
          [nodeId]: {
            status: res.data.status || "unknown",
            port: res.data.port || "",
            loading: false,
          },
        }));
      }
    } catch {}
    iperfPollRef.current = window.setTimeout(
      () => pollIperfStatus(nodeId),
      4000,
    );
  };

  const pollDiag = async (
    nodeId: number,
    kind: string,
    requestId: string,
    attempt: number = 0,
  ) => {
    if (attempt > 200) return;
    try {
      const res: any = await nodeDiagResult(nodeId, kind, requestId);
      if (res?.code === 0 && res.data) {
        setDiagState((prev) => ({
          ...prev,
          content: res.data.content || "",
          done: !!res.data.done,
          requestId: res.data.requestId || requestId,
        }));
        if (res.data.done) {
          return;
        }
      }
    } catch {}
    diagPollRef.current = window.setTimeout(
      () => pollDiag(nodeId, kind, requestId, attempt + 1),
      1500,
    );
  };

  const startDiag = async (node: Node, kind: string) => {
    stopDiagPolling();
    setDiagState({
      nodeId: node.id,
      nodeName: node.name,
      kind,
      loading: true,
      content: "",
      done: false,
      requestId: "",
    });
    if (kind === "iperf3-start" || kind === "iperf3-stop") {
      setIperf3Map((prev) => ({
        ...prev,
        [node.id]: {
          status: prev[node.id]?.status || "unknown",
          port: prev[node.id]?.port || "",
          loading: true,
        },
      }));
    }
    try {
      const res: any = await nodeDiagStart(node.id, kind);
      if (res?.code === 0) {
        const requestId = res.data?.requestId || "";
        setDiagState((prev) => ({
          ...prev,
          loading: false,
          requestId,
        }));
        if (requestId) {
          pollDiag(node.id, kind, requestId, 0);
        }
        if (kind === "iperf3-start" || kind === "iperf3-stop") {
          setIperf3Status((prev) => ({ ...prev, loading: true }));
          stopIperfPolling();
          pollIperfStatus(node.id);
        }
      } else {
        setDiagState((prev) => ({ ...prev, loading: false }));
        toast.error(res?.msg || "诊断失败");
      }
    } catch (e: any) {
      setDiagState((prev) => ({ ...prev, loading: false }));
      toast.error(e?.message || "诊断失败");
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

  const parseUpgradeStatus = useCallback((cmd: string) => {
    const step = cmd.replace("OpLog:", "");
    if (step.includes("error")) return "failed";
    if (step.includes("done")) return "success";
    if (
      step.includes("start") ||
      step.includes("download") ||
      step.includes("validate") ||
      step.includes("restart")
    ) {
      return "running";
    }
    return "unknown";
  }, []);

  const loadUpgradeSummary = useCallback(async () => {
    setUpgradeLoading(true);
    try {
      const res: any = await listNodeOps({ limit: 1000 });
      const ops =
        res && res.code === 0 && Array.isArray(res.data?.ops)
          ? res.data.ops
          : [];
      const filtered = ops.filter(
        (o: any) =>
          typeof o?.cmd === "string" && o.cmd.startsWith("OpLog:agent_upgrade"),
      );
      const summary: Record<
        number,
        {
          status: "success" | "failed" | "running" | "unknown";
          timeMs: number;
          message: string;
          step: string;
        }
      > = {};
      filtered.forEach((o: any) => {
        const nodeId = Number(o.nodeId || 0);
        if (!nodeId) return;
        if (!summary[nodeId] || o.timeMs > summary[nodeId].timeMs) {
          summary[nodeId] = {
            status: parseUpgradeStatus(o.cmd),
            timeMs: o.timeMs || 0,
            message: o.message || "",
            step: o.cmd.replace("OpLog:", ""),
          };
        }
      });
      setUpgradeSummary(summary);
      if (!upgradeNodeId && nodeList.length > 0) {
        setUpgradeNodeId(nodeList[0].id);
      }
    } catch {
      setUpgradeSummary({});
    } finally {
      setUpgradeLoading(false);
    }
  }, [nodeList, parseUpgradeStatus, upgradeNodeId]);

  const loadUpgradeLogs = useCallback(async (nodeId?: number | null) => {
    if (!nodeId) {
      setUpgradeLogs([]);
      return;
    }
    try {
      const res: any = await listNodeOps({ nodeId, limit: 200 });
      const ops =
        res && res.code === 0 && Array.isArray(res.data?.ops)
          ? res.data.ops
          : [];
      const filtered = ops
        .filter(
          (o: any) =>
            typeof o?.cmd === "string" &&
            o.cmd.startsWith("OpLog:agent_upgrade"),
        )
        .sort((a: any, b: any) => (a.timeMs || 0) - (b.timeMs || 0));
      setUpgradeLogs(
        filtered.map((o: any) => ({
          timeMs: o.timeMs || 0,
          cmd: o.cmd,
          message: o.message || "",
        })),
      );
    } catch {
      setUpgradeLogs([]);
    }
  }, []);

  const handleUpgradeOneAgent = async (node: Node) => {
    if (node.connectionStatus !== "online") {
      toast.error("节点离线，无法触发升级");
      return;
    }
    setUpgradeNodeLoading((prev) => ({ ...prev, [node.id]: true }));
    try {
      const res: any = await upgradeNodeAgentsBatch([node.id]);
      if (res?.code === 0) {
        const item = Array.isArray(res?.data?.results)
          ? res.data.results.find((r: any) => Number(r?.nodeId) === node.id)
          : null;
        if (item?.ok === false) {
          toast.error(item?.error || "触发升级失败");
        } else {
          toast.success(`已触发 ${node.name} 升级`);
        }
        setUpgradeModalOpen(true);
        setUpgradeNodeId(node.id);
        void loadUpgradeSummary();
        void loadUpgradeLogs(node.id);
      } else {
        toast.error(res?.msg || "触发升级失败");
      }
    } catch (e: any) {
      toast.error(e?.message || "触发升级失败");
    } finally {
      setUpgradeNodeLoading((prev) => ({ ...prev, [node.id]: false }));
    }
  };

  const handleUpgradeAllAgents = async () => {
    if (nodeList.length === 0) {
      toast.error("暂无节点可升级");
      return;
    }
    if (
      !window.confirm(
        `确认手动触发全部节点 Agent 升级吗？共 ${nodeList.length} 个节点。`,
      )
    ) {
      return;
    }
    setUpgradeAllLoading(true);
    try {
      const res: any = await upgradeNodeAgentsBatch(nodeList.map((n) => n.id));
      if (res?.code === 0) {
        const ok = Number(res?.data?.ok || 0);
        const total = Number(res?.data?.total || nodeList.length);
        const failed = Number(res?.data?.failed || 0);
        if (failed > 0) {
          toast.error(`已触发升级 ${ok}/${total}，失败 ${failed}`);
        } else {
          toast.success(`已触发升级 ${ok}/${total}`);
        }
        setUpgradeModalOpen(true);
        void loadUpgradeSummary();
      } else {
        toast.error(res?.msg || "触发全部升级失败");
      }
    } catch (e: any) {
      toast.error(e?.message || "触发全部升级失败");
    } finally {
      setUpgradeAllLoading(false);
    }
  };

  useEffect(() => {
    if (!upgradeModalOpen) return;
    loadUpgradeSummary();
  }, [upgradeModalOpen, loadUpgradeSummary]);

  useEffect(() => {
    if (!upgradeModalOpen) return;
    loadUpgradeLogs(upgradeNodeId);
  }, [upgradeModalOpen, upgradeNodeId, loadUpgradeLogs]);

  useEffect(() => {
    return () => {
      stopDiagPolling();
      stopIperfPolling();
    };
  }, []);

  useEffect(() => {
    const next = new URLSearchParams();
    const kw = nodeKeyword.trim();

    if (kw) next.set("q", kw);
    if (nodeConnFilter !== "all") next.set("conn", nodeConnFilter);
    if (nodeAnyTLSFilter !== "all") next.set("anytls", nodeAnyTLSFilter);
    if (next.toString() !== searchParams.toString()) {
      setSearchParams(next, { replace: true });
    }
  }, [
    nodeKeyword,
    nodeConnFilter,
    nodeAnyTLSFilter,
    searchParams,
    setSearchParams,
  ]);

  const anytlsDegradedCount = nodeList.filter(
    (n) => nodeHasAnyTLS(n) && n.anytlsRuntime?.state === "degraded",
  ).length;
  const filteredNodeList = nodeList.filter((node) => {
    if (nodeConnFilter !== "all" && node.connectionStatus !== nodeConnFilter) {
      return false;
    }
    if (nodeAnyTLSFilter !== "all") {
      if (!nodeHasAnyTLS(node)) {
        return false;
      }
      const state = (node.anytlsRuntime?.state || "unknown").toLowerCase();
      if (state !== nodeAnyTLSFilter) {
        return false;
      }
    }
    if (nodeKeyword.trim()) {
      const kw = nodeKeyword.trim().toLowerCase();
      const hay = [
        node.name || "",
        node.ip || "",
        node.serverIp || "",
        node.anytlsCert?.domain || "",
      ]
        .join(" ")
        .toLowerCase();
      if (!hay.includes(kw)) {
        return false;
      }
    }
    return true;
  });

  return (
    <div className="np-page">
      {/* 页面头部 */}
      <div className="np-page-header">
        <div className="space-y-2">
          <div>
            <h1 className="np-page-title">节点监控</h1>
            <p className="np-page-desc">实时观察节点健康度、端口和服务状态。</p>
          </div>
          <div className="flex flex-wrap items-center gap-3 text-xs text-default-500">
            <div className="flex items-center gap-2">
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
              className="hidden md:block truncate max-w-[420px]"
              title={wsUrlShown}
            >
              {wsUrlShown || "-"}
            </div>
            <div>
              后端: {serverVersion || "-"} · Agent: {agentVersion || "-"}
            </div>
            <Chip
              color={anytlsDegradedCount > 0 ? "warning" : "success"}
              size="sm"
              variant="flat"
            >
              AnyTLS异常 {anytlsDegradedCount}
            </Chip>
          </div>
        </div>

        {isAdmin ? (
          <>
            <Button color="primary" size="sm" variant="flat" onPress={handleAdd}>
              新增
            </Button>
            <Button
              color="default"
              size="sm"
              variant="flat"
              isLoading={upgradeAllLoading}
              onPress={handleUpgradeAllAgents}
            >
              全部升级
            </Button>
            <Button
              color="default"
              size="sm"
              variant="flat"
              onPress={() => setUpgradeModalOpen(true)}
            >
              升级状态
            </Button>
          </>
        ) : null}
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
        <Card className="np-card">
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
          <div className="flex flex-wrap items-end gap-2 mb-2">
            <Input
              className="w-full sm:w-64"
              placeholder="搜索 名称/IP/证书域名"
              size="sm"
              value={nodeKeyword}
              variant="bordered"
              onChange={(e) => setNodeKeyword(e.target.value)}
            />
            <Select
              className="w-full sm:w-40"
              labelPlacement="outside"
              placeholder="在线状态"
              selectedKeys={[nodeConnFilter]}
              size="sm"
              variant="bordered"
              onChange={(e) =>
                setNodeConnFilter(
                  ((e.target as any).value as "all" | "online" | "offline") ||
                    "all",
                )
              }
            >
              <SelectItem key="all">全部状态</SelectItem>
              <SelectItem key="online">仅在线</SelectItem>
              <SelectItem key="offline">仅离线</SelectItem>
            </Select>
            <Select
              className="w-full sm:w-48"
              labelPlacement="outside"
              placeholder="AnyTLS状态"
              selectedKeys={[nodeAnyTLSFilter]}
              size="sm"
              variant="bordered"
              onChange={(e) =>
                setNodeAnyTLSFilter(
                  ((e.target as any).value as
                    | "all"
                    | "healthy"
                    | "degraded"
                    | "unknown") || "all",
                )
              }
            >
              <SelectItem key="all">AnyTLS全部</SelectItem>
              <SelectItem key="degraded">仅异常</SelectItem>
              <SelectItem key="healthy">仅健康</SelectItem>
              <SelectItem key="unknown">仅未知</SelectItem>
            </Select>
            <Button
              size="sm"
              variant="flat"
              onPress={() => {
                setNodeKeyword("");
                setNodeConnFilter("all");
                setNodeAnyTLSFilter("all");
              }}
            >
              重置筛选
            </Button>
            <Chip size="sm" variant="flat">
              命中 {filteredNodeList.length}/{nodeList.length}
            </Chip>
          </div>
          {isAdmin ? (
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
          ) : null}
          {filteredNodeList.length === 0 ? (
            <Card className="np-card">
              <CardBody className="text-center py-12 text-default-500">
                当前筛选条件下暂无节点
              </CardBody>
            </Card>
          ) : (
            <div
              ref={gridWrapRef}
              style={{ visibility: gridReady ? "visible" : "hidden" }}
            >
              <VirtualGrid
                className="w-full"
                estimateRowHeight={nodeRowHeight}
                items={filteredNodeList}
                minItemWidth={280}
                renderItem={(node) => {
                  const readOnly = !isAdmin && !!node.shared;
                  return (
                    <Card
                      key={node.id}
                      className={`list-card node-card hover:shadow-md transition-shadow duration-200 ${node.connectionStatus === "offline" ? "node-card-offline" : ""}`}
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
                          {isAdmin && !readOnly && (
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
                          {isAdmin ? (
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
                          ) : null}
                          <Chip
                            className="text-xs np-pill"
                            color={
                              node.connectionStatus === "online"
                                ? "success"
                                : "danger"
                            }
                            size="sm"
                            variant="flat"
                          >
                            <span className="inline-flex items-center gap-1">
                              <span
                                className={`inline-block w-1.5 h-1.5 rounded-full ${
                                  node.connectionStatus === "online"
                                    ? "bg-green-500"
                                    : "bg-red-500"
                                }`}
                              />
                              <span>
                                {node.connectionStatus === "online"
                                  ? "在线"
                                  : "离线"}
                              </span>
                            </span>
                          </Chip>
                        </div>
                      </div>
                    </CardHeader>

                    <CardBody className="pt-0 pb-3 space-y-3">
                  <div
                    className="text-xs text-default-500 flex flex-wrap items-center gap-2"
                    style={{ cursor: "pointer" }}
                    onClick={() => goNetwork(node)}
                  >
                    <span className="font-mono">
                      {node.ip
                        ? node.ip.split(",")[0].trim()
                        : node.serverIp || "-"}
                    </span>
                    <span>·</span>
                    {readOnly && node.assignedPortRanges ? (
                      <span>授权端口 {node.assignedPortRanges}</span>
                    ) : (
                      <span>
                        Port {node.portSta}-{node.portEnd}
                      </span>
                    )}
                    <span>·</span>
                    <span>v{node.version || "-"}</span>
                    {!readOnly && node.usedPorts && node.usedPorts.length > 0 ? (
                      <>
                        <span>·</span>
                        <span className="text-warning-600">
                          端口冲突 {node.usedPorts.length}
                        </span>
                      </>
                    ) : null}
                  </div>

                  {anytlsCertEnabled && node.anytlsCert?.domain ? (
                    <div className="flex flex-wrap items-center gap-2 text-xs">
                      <Chip
                        size="sm"
                        variant="flat"
                        color={
                          node.anytlsCert?.state === "expired"
                            ? "danger"
                            : node.anytlsCert?.state === "expiring"
                              ? "warning"
                              : "success"
                        }
                      >
                        AnyTLS 证书
                      </Chip>
                      <span className="font-mono text-default-700 break-all">
                        {node.anytlsCert.domain}
                      </span>
                      <span className="text-default-500">
                        到期{" "}
                        {node.anytlsCert.notAfterMs
                          ? new Date(node.anytlsCert.notAfterMs).toLocaleString()
                          : "-"}
                      </span>
                      <span className="text-default-500">
                        剩余{" "}
                        {typeof node.anytlsCert.daysLeft === "number"
                          ? `${node.anytlsCert.daysLeft} 天`
                          : "-"}
                      </span>
                      <span className="text-default-500">
                        来源{" "}
                        {node.anytlsCert.source === "agent_log"
                          ? "Agent已安装"
                          : node.anytlsCert.source === "controller_estimate"
                            ? "控制器估算"
                            : "-"}
                      </span>
                      <span className="text-default-500">
                        更新{" "}
                        {node.anytlsCert.updatedAtMs
                          ? new Date(node.anytlsCert.updatedAtMs).toLocaleString()
                          : "-"}
                      </span>
                    </div>
                  ) : null}

                  {nodeHasAnyTLS(node) ? (
                    <div className="flex flex-wrap items-center gap-2 text-xs">
                      <Chip
                        size="sm"
                        variant="flat"
                        color={
                          node.anytlsRuntime?.state === "healthy"
                            ? "success"
                            : node.anytlsRuntime?.state === "degraded"
                              ? "warning"
                              : "default"
                        }
                      >
                        AnyTLS状态 {node.anytlsRuntime?.state || "unknown"}
                      </Chip>
                      <span className="text-default-500">
                        窗口 {node.anytlsRuntime?.windowSec || 900}s
                      </span>
                      <span className="text-default-500">
                        日志 {node.anytlsRuntime?.recentCount || 0}
                      </span>
                      {(node.anytlsRuntime?.connReject || 0) > 0 ? (
                        <span className="text-warning-600">
                          拒绝 {node.anytlsRuntime?.connReject || 0}
                        </span>
                      ) : null}
                      {(node.anytlsRuntime?.acceptErr || 0) > 0 ? (
                        <span className="text-warning-600">
                          accept错 {node.anytlsRuntime?.acceptErr || 0}
                        </span>
                      ) : null}
                      {(node.anytlsRuntime?.handshakeTimeout || 0) > 0 ? (
                        <span className="text-warning-600">
                          握手超时 {node.anytlsRuntime?.handshakeTimeout || 0}
                        </span>
                      ) : null}
                      {(node.anytlsRuntime?.tlsHandshakeErr || 0) > 0 ? (
                        <span className="text-danger-600">
                          TLS握手错 {node.anytlsRuntime?.tlsHandshakeErr || 0}
                          {(node.anytlsRuntime?.tlsConnResetByPeer || 0) > 0
                            ? ` (重置 ${
                                node.anytlsRuntime?.tlsConnResetByPeer || 0
                              }/${node.anytlsRuntime?.tlsHandshakeErr || 0}, ${(
                                (((node.anytlsRuntime?.tlsConnResetByPeer || 0) *
                                  100) /
                                  Math.max(
                                    1,
                                    node.anytlsRuntime?.tlsHandshakeErr || 0,
                                  )) || 0
                              ).toFixed(1)}%)`
                            : ""}
                        </span>
                      ) : null}
                      {(node.anytlsRuntime?.tlsClientHello || 0) > 0 ? (
                        <span className="text-default-500">
                          SNI失配 {node.anytlsRuntime?.tlsSniMismatch || 0}/
                          {node.anytlsRuntime?.tlsClientHello || 0} ({(
                            (((node.anytlsRuntime?.tlsSniMismatch || 0) * 100) /
                              Math.max(1, node.anytlsRuntime?.tlsClientHello || 0)) ||
                            0
                          ).toFixed(1)}%)
                        </span>
                      ) : null}
                      {(node.anytlsRuntime?.listenErr || 0) > 0 ? (
                        <span className="text-danger-600">
                          监听错 {node.anytlsRuntime?.listenErr || 0}
                        </span>
                      ) : null}
                      {(node.anytlsRuntime?.streamErr || 0) > 0 ? (
                        <span className="text-danger-600">
                          转发错 {node.anytlsRuntime?.streamErr || 0}
                        </span>
                      ) : null}
                      {(node.anytlsRuntime?.authFail || 0) > 0 ? (
                        <span className="text-warning-600">
                          鉴权失败 {node.anytlsRuntime?.authFail || 0}
                        </span>
                      ) : null}
                      {(node.anytlsRuntime?.readErr || 0) > 0 ? (
                        <span className="text-warning-600">
                          读包错 {node.anytlsRuntime?.readErr || 0}
                        </span>
                      ) : null}
                    </div>
                  ) : null}

                  {!readOnly ? (() => {
                    const hasSystemInfo =
                      !!node.systemInfo &&
                      ((node.systemInfo.cpuUsage || 0) > 0 ||
                        (node.systemInfo.memoryUsage || 0) > 0 ||
                        (node.systemInfo.uploadTraffic || 0) > 0 ||
                        (node.systemInfo.downloadTraffic || 0) > 0);
                    const cpuPct = hasSystemInfo
                      ? clampPercent(node.systemInfo!.cpuUsage)
                      : 0;
                    const memPct = hasSystemInfo
                      ? clampPercent(node.systemInfo!.memoryUsage)
                      : 0;
                    const trafficMax = hasSystemInfo
                      ? Math.max(
                          node.systemInfo!.uploadTraffic || 0,
                          node.systemInfo!.downloadTraffic || 0,
                          1,
                        )
                      : 1;
                    const upPct = hasSystemInfo
                      ? clampPercent(
                          Math.round(
                            ((node.systemInfo!.uploadTraffic || 0) /
                              trafficMax) *
                              100,
                          ),
                        )
                      : 0;
                    const downPct = hasSystemInfo
                      ? clampPercent(
                          Math.round(
                            ((node.systemInfo!.downloadTraffic || 0) /
                              trafficMax) *
                              100,
                          ),
                        )
                      : 0;
                    const agentRssMb = node.systemInfo?.agentRssMb;
                    const agentHeapAllocMb = node.systemInfo?.agentHeapAllocMb;
                    const agentGoRoutines = node.systemInfo?.agentGoRoutines;
                    const agentNumGc = node.systemInfo?.agentNumGc;
                    const hasAgentRuntime =
                      typeof agentRssMb === "number" ||
                      typeof agentHeapAllocMb === "number" ||
                      typeof agentGoRoutines === "number";
                    const memHealth =
                      typeof agentRssMb === "number"
                        ? agentRssMb <= 96
                          ? "low"
                          : agentRssMb <= 192
                            ? "mid"
                            : "high"
                        : "unknown";
                    const memHealthColor =
                      memHealth === "low"
                        ? "success"
                        : memHealth === "mid"
                          ? "warning"
                          : memHealth === "high"
                            ? "danger"
                            : "default";
                    const memHealthText =
                      memHealth === "low"
                        ? "正常"
                        : memHealth === "mid"
                          ? "偏高"
                          : memHealth === "high"
                            ? "高"
                            : "-";

                    return (
                      <div className="grid grid-cols-2 gap-2 text-xs">
                        <div className="np-soft p-2">
                          <div className="flex justify-between mb-1 text-default-600">
                            <span>CPU</span>
                            <span className="font-mono">
                              {hasSystemInfo
                                ? `${node.systemInfo!.cpuUsage.toFixed(1)}%`
                                : "-"}
                            </span>
                          </div>
                          <div className="h-1.5 rounded-full bg-orange-100">
                            <div
                              className="h-full rounded-full bg-orange-400"
                              style={{ width: `${cpuPct}%` }}
                            />
                          </div>
                        </div>
                        <div className="np-soft p-2">
                          <div className="flex justify-between mb-1 text-default-600">
                            <span>内存</span>
                            <span className="font-mono">
                              {hasSystemInfo
                                ? `${node.systemInfo!.memoryUsage.toFixed(1)}%`
                                : "-"}
                            </span>
                          </div>
                          <div className="h-1.5 rounded-full bg-orange-100">
                            <div
                              className="h-full rounded-full bg-orange-400"
                              style={{ width: `${memPct}%` }}
                            />
                          </div>
                        </div>
                        <div className="np-soft p-2">
                          <div className="flex justify-between mb-1 text-default-600">
                            <span>上行流量</span>
                            <span className="font-mono">
                              {hasSystemInfo
                                ? formatTraffic(node.systemInfo!.uploadTraffic)
                                : "-"}
                            </span>
                          </div>
                          <div className="h-1.5 rounded-full bg-orange-100">
                            <div
                              className="h-full rounded-full bg-orange-400"
                              style={{ width: `${upPct}%` }}
                            />
                          </div>
                        </div>
                        <div className="np-soft p-2">
                          <div className="flex justify-between mb-1 text-default-600">
                            <span>下行流量</span>
                            <span className="font-mono">
                              {hasSystemInfo
                                ? formatTraffic(
                                    node.systemInfo!.downloadTraffic,
                                  )
                                : "-"}
                            </span>
                          </div>
                          <div className="h-1.5 rounded-full bg-orange-100">
                            <div
                              className="h-full rounded-full bg-orange-400"
                              style={{ width: `${downPct}%` }}
                            />
                          </div>
                        </div>
                        <div className="np-soft p-2 col-span-2">
                          <div className="flex items-center justify-between mb-1 text-default-600">
                            <span>Agent 内存</span>
                            <Chip color={memHealthColor as any} size="sm" variant="flat">
                              {memHealthText}
                            </Chip>
                          </div>
                          <div className="font-mono text-default-700 flex flex-wrap items-center gap-x-3 gap-y-1">
                            <span>RSS: {formatMB(agentRssMb)}</span>
                            <span>Heap: {formatMB(agentHeapAllocMb)}</span>
                            <span>
                              Goroutines:{" "}
                              {typeof agentGoRoutines === "number"
                                ? Math.round(agentGoRoutines)
                                : "-"}
                            </span>
                            <span>
                              GC: {typeof agentNumGc === "number" ? Math.round(agentNumGc) : "-"}
                            </span>
                          </div>
                          {!hasAgentRuntime ? (
                            <div className="text-default-400 mt-1">
                              等待 Agent 上报进程内存指标
                            </div>
                          ) : null}
                        </div>
                      </div>
                    );
                  })() : null}

                  {!readOnly ? (
                    <div className="flex flex-wrap items-center gap-2 text-xs">
                      <div className="flex flex-wrap items-center gap-2 rounded-full border border-orange-200 bg-orange-50/70 px-2 py-1">
                        <span className="np-tag">
                          {node.connectionStatus === "online" && node.systemInfo
                            ? node.systemInfo.gostRunning
                              ? "Gost ON"
                              : "Gost OFF"
                            : "Gost -"}
                        </span>
                        {iperf3Map[node.id] && (
                          <Chip
                            size="sm"
                            variant="flat"
                            color={
                              iperf3Map[node.id].loading
                                ? "warning"
                                : iperf3Map[node.id].status === "running"
                                  ? "success"
                                  : iperf3Map[node.id].status === "stopped"
                                    ? "default"
                                    : "danger"
                            }
                          >
                            <span className="inline-flex items-center gap-1">
                              <span
                                className={`inline-block w-1.5 h-1.5 rounded-full ${
                                  iperf3Map[node.id].loading
                                    ? "bg-yellow-400"
                                    : iperf3Map[node.id].status === "running"
                                      ? "bg-green-500"
                                      : iperf3Map[node.id].status === "stopped"
                                        ? "bg-slate-400"
                                        : "bg-red-500"
                                }`}
                              />
                              iperf3
                            </span>
                          </Chip>
                        )}
                        {iperf3Map[node.id]?.port && (
                          <Chip size="sm" variant="flat">
                            端口 {iperf3Map[node.id].port}
                          </Chip>
                        )}
                      {node.connectionStatus === "online" &&
                      node.systemInfo &&
                      node.systemInfo.gostRunning ? (
                        <Button
                          className="h-6 min-h-6 px-2 text-xs"
                          color="warning"
                          isLoading={!!rstLoading[node.id]}
                          size="sm"
                          variant="flat"
                          onPress={() => doRestartGost(node.id)}
                        >
                          重启
                        </Button>
                      ) : null}
                      {node.connectionStatus === "online" && node.systemInfo ? (
                        (node.systemInfo as any).gostApiConfigured === false ? (
                          <Button
                            className="h-6 min-h-6 px-2 text-xs"
                            color="primary"
                            size="sm"
                            variant="flat"
                            onPress={async () => {
                              try {
                                const input = window.prompt(
                                  "请输入 GOST API 端口（默认 18080）",
                                  "18080",
                                );
                                if (input === null) return;
                                const trimmed = String(input).trim();
                                let port: number | undefined = undefined;
                                if (trimmed !== "") {
                                  const n = Number(trimmed);
                                  if (
                                    !Number.isInteger(n) ||
                                    n <= 0 ||
                                    n > 65535
                                  ) {
                                    toast.error("端口需为 1-65535 的整数");
                                    return;
                                  }
                                  port = n;
                                }
                                await enableGostApi(node.id, port);
                                toast.success(
                                  "已发送启用 GOST API 指令（仅本机127.0.0.1监听），稍候刷新",
                                );
                              } catch (e: any) {
                                toast.error(e?.message || "指令发送失败");
                              }
                            }}
                          >
                            GOST API
                          </Button>
                        ) : (node.systemInfo as any).gostApiConfigured === true ? (
                          <>
                            <Button
                              className="h-6 min-h-6 px-2 text-xs"
                              color="primary"
                              size="sm"
                              variant="flat"
                              onPress={async () => {
                                try {
                                  const input = window.prompt(
                                    "请输入 GOST API 端口（默认 18080）",
                                    "18080",
                                  );
                                  if (input === null) return;
                                  const trimmed = String(input).trim();
                                  let port: number | undefined = undefined;
                                  if (trimmed !== "") {
                                    const n = Number(trimmed);
                                    if (
                                      !Number.isInteger(n) ||
                                      n <= 0 ||
                                      n > 65535
                                    ) {
                                      toast.error("端口需为 1-65535 的整数");
                                      return;
                                    }
                                    port = n;
                                  }
                                  await enableGostApi(node.id, port);
                                  toast.success(
                                    "已发送重配 GOST API 指令（仅本机127.0.0.1监听），稍候刷新",
                                  );
                                } catch (e: any) {
                                  toast.error(e?.message || "指令发送失败");
                                }
                              }}
                            >
                              API端口
                            </Button>
                            <Button
                              className="h-6 min-h-6 px-2 text-xs"
                              color="secondary"
                              size="sm"
                              variant="flat"
                              onPress={() => showGostConfig(node)}
                            >
                              配置
                            </Button>
                          </>
                        ) : null
                      ) : null}
                      </div>
                      <Tooltip
                        content={
                          <div className="text-xs">
                            <div>
                              {node.gostApiBindDetail || "GOST API 监听校验"}
                            </div>
                            <div className="text-default-500">
                              {node.gostApiBindCheckedAtMs
                                ? `校验时间 ${new Date(
                                    node.gostApiBindCheckedAtMs,
                                  ).toLocaleString()}`
                                : "校验时间 -"}
                            </div>
                          </div>
                        }
                      >
                        <Chip
                          size="sm"
                          variant="flat"
                          color={
                            node.gostApiBindLoopbackOnly === true
                              ? "success"
                              : node.gostApiBindLoopbackOnly === false
                                ? "danger"
                                : "default"
                          }
                        >
                          API本地监听
                          {node.gostApiBindLoopbackOnly === true
                            ? "已校验"
                            : node.gostApiBindLoopbackOnly === false
                              ? "异常"
                              : "未知"}
                        </Chip>
                      </Tooltip>
                      {(node.priceCents || node.cycleMonths) && (
                        <span className="np-tag">
                          ¥{(node.priceCents || 0) / 100}
                          {node.cycleMonths ? `/${node.cycleMonths}月` : ""}
                        </span>
                      )}
                    </div>
                  ) : null}

                  {/* 操作按钮 */}
                  <div className="space-y-1.5">
                    <div className={`grid gap-2 ${readOnly ? "grid-cols-1" : "grid-cols-2 sm:grid-cols-3"}`}>
                      {!readOnly ? (
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
                      ) : null}
                      {!readOnly && isAdmin ? (
                        <Button
                          className="w-full min-h-8"
                          color="default"
                          size="sm"
                          variant="flat"
                          isLoading={!!upgradeNodeLoading[node.id]}
                          isDisabled={node.connectionStatus !== "online"}
                          onPress={() => handleUpgradeOneAgent(node)}
                        >
                          升级
                        </Button>
                      ) : null}
                      <Button
                        className="w-full min-h-8"
                        color="warning"
                        size="sm"
                        variant="flat"
                        onPress={() => openExitModal(node)}
                      >
                        出口
                      </Button>
                      {!readOnly && anytlsCertEnabled ? (
                        <Button
                          className="w-full min-h-8"
                          color="secondary"
                          size="sm"
                          variant="flat"
                          isLoading={!!certRefreshLoading[node.id]}
                          isDisabled={!nodeHasAnyTLS(node)}
                          onPress={() => doForceRefreshAnyTLSCert(node)}
                        >
                          刷新证书
                        </Button>
                      ) : null}
                      {!readOnly && anytlsCertEnabled ? (
                        <Button
                          className="w-full min-h-8"
                          color="primary"
                          size="sm"
                          variant="flat"
                          isLoading={!!certChainLoading[node.id]}
                          isDisabled={!nodeHasAnyTLS(node)}
                          onPress={() => doCheckAnyTLSCertChain(node)}
                        >
                          链校验
                        </Button>
                      ) : null}
                      {!readOnly ? (
                        <Button
                          className="w-full min-h-8"
                          color="secondary"
                          size="sm"
                          variant="flat"
                          isDisabled={!nodeHasAnyTLS(node)}
                          onPress={() => openAnyTLSLogModal(node)}
                        >
                          AnyTLS日志
                        </Button>
                      ) : null}
                      {!readOnly ? (
                        <>
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
                            端口
                          </Button>
                          {isAdmin ? (
                            <Button
                              className="w-full min-h-8"
                              color="default"
                              size="sm"
                              variant="flat"
                              onPress={() => openUsageModal(node)}
                            >
                              用量
                            </Button>
                          ) : null}
                          <Button
                            className="w-full min-h-8"
                            color="primary"
                            isLoading={!!nqLoading[node.id]}
                            size="sm"
                            variant="flat"
                            onPress={() => runNQ(node)}
                          >
                            NQ
                          </Button>
                          {(nqResultCache[node.id] || nqHasResult[node.id]) && (
                            <Button
                              className="w-full min-h-8"
                              color="default"
                              size="sm"
                              variant="flat"
                              onPress={() => viewNQ(node)}
                            >
                              NQ 结果
                            </Button>
                          )}
                          <Button
                            className="w-full min-h-8"
                            color="primary"
                            isLoading={!!reapplyLoading[node.id]}
                            size="sm"
                            variant="flat"
                            onPress={() => doReapply(node.id)}
                          >
                            重应用
                          </Button>
                          <Button
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
                        </>
                      ) : null}
                    </div>
                  </div>
                    </CardBody>
                    </Card>
                  );
                }}
              />
            </div>
          )}
        </>
      ) : null}

      <OpsLogModal isOpen={opsOpen} onOpenChange={setOpsOpen} />
      <Modal
        backdrop="opaque"
        disableAnimation
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
        isAdmin={isAdmin}
        anytlsCertEnabled={anytlsCertEnabled}
        onChanged={handleNodeSaved}
        onOpenChange={handleExitModalChange}
      />

      <Modal
        backdrop="opaque"
        disableAnimation
        isOpen={upgradeModalOpen}
        scrollBehavior="outside"
        onOpenChange={setUpgradeModalOpen}
      >
        <ModalContent className="w-[88vw] max-w-[88vw] h-[80vh]">
          {(onClose) => (
            <>
              <ModalHeader className="flex items-center justify-between">
                <div>节点升级状态</div>
                <Button
                  isDisabled={upgradeLoading}
                  size="sm"
                  variant="flat"
                  onPress={loadUpgradeSummary}
                >
                  {upgradeLoading ? "刷新中..." : "刷新"}
                </Button>
              </ModalHeader>
              <ModalBody className="overflow-hidden">
                <div className="grid grid-cols-1 md:grid-cols-[280px,1fr] gap-4 h-[62vh]">
                  <div className="border border-divider rounded-lg p-3 overflow-auto">
                    {nodeList.length === 0 ? (
                      <div className="text-default-500 text-sm">暂无节点</div>
                    ) : (
                      <div className="space-y-2">
                        {nodeList.map((n) => {
                          const info = upgradeSummary[n.id];
                          const status = info?.status || "unknown";
                          const chipColor =
                            status === "success"
                              ? "success"
                              : status === "failed"
                                ? "danger"
                                : status === "running"
                                  ? "warning"
                                  : "default";
                          return (
                            <button
                              key={`upgrade-node-${n.id}`}
                              className={`w-full text-left rounded-md px-3 py-2 border ${
                                upgradeNodeId === n.id
                                  ? "border-primary/60 bg-primary/10"
                                  : "border-divider hover:bg-default-100"
                              }`}
                              onClick={() => setUpgradeNodeId(n.id)}
                            >
                              <div className="flex items-center justify-between gap-2">
                                <div className="truncate text-sm font-medium">
                                  {n.name}
                                </div>
                                <Chip size="sm" variant="flat" color={chipColor as any}>
                                  {status === "success"
                                    ? "成功"
                                    : status === "failed"
                                      ? "失败"
                                      : status === "running"
                                        ? "进行中"
                                        : "未知"}
                                </Chip>
                              </div>
                              <div className="text-2xs text-default-500 mt-1 truncate">
                                {info?.message || "暂无升级日志"}
                              </div>
                            </button>
                          );
                        })}
                      </div>
                    )}
                  </div>
                  <div className="border border-divider rounded-lg p-3 overflow-auto">
                    {upgradeNodeId == null ? (
                      <div className="text-default-500 text-sm">请选择节点</div>
                    ) : upgradeLogs.length === 0 ? (
                      <div className="text-default-500 text-sm">暂无升级日志</div>
                    ) : (
                      <pre className="whitespace-pre-wrap text-2xs">
                        {upgradeLogs
                          .map((o) => {
                            const t = o.timeMs
                              ? new Date(o.timeMs).toLocaleString()
                              : "";
                            const step = o.cmd.replace("OpLog:", "");
                            return `[${t}] ${step}  ${o.message || ""}`;
                          })
                          .join("\n")}
                      </pre>
                    )}
                  </div>
                </div>
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

      <Modal
        backdrop="opaque"
        disableAnimation
        isOpen={certChainModal.open}
        placement="center"
        scrollBehavior="outside"
        size="3xl"
        onOpenChange={(open) =>
          setCertChainModal((prev) => ({ ...prev, open }))
        }
      >
        <ModalContent>
          {(onClose) => (
            <>
              <ModalHeader>{certChainModal.title || "AnyTLS 链校验"}</ModalHeader>
              <ModalBody>
                <div className="text-sm space-y-2">
                  <div>
                    校验结果：
                    {certChainModal.data?.verifyOK ? (
                      <span className="text-success font-medium"> 通过</span>
                    ) : (
                      <span className="text-danger font-medium"> 失败</span>
                    )}
                  </div>
                  <div>
                    目标地址：{certChainModal.data?.selectedAddr || "-"} · SNI{" "}
                    {certChainModal.data?.domain || "-"}
                  </div>
                  {!certChainModal.data?.verifyOK && certChainModal.data?.verifyErr ? (
                    <div className="text-danger break-all">
                      错误：{String(certChainModal.data.verifyErr)}
                    </div>
                  ) : null}
                </div>
                <Textarea
                  readOnly
                  className="font-mono text-xs"
                  minRows={14}
                  value={JSON.stringify(certChainModal.data || {}, null, 2)}
                />
              </ModalBody>
              <ModalFooter>
                <Button
                  color="primary"
                  variant="flat"
                  onPress={async () => {
                    try {
                      await navigator.clipboard.writeText(
                        JSON.stringify(certChainModal.data || {}, null, 2),
                      );
                      toast.success("已复制链校验结果");
                    } catch {
                      toast.error("复制失败");
                    }
                  }}
                >
                  复制结果
                </Button>
                <Button variant="light" onPress={onClose}>
                  关闭
                </Button>
              </ModalFooter>
            </>
          )}
        </ModalContent>
      </Modal>

      <Modal
        backdrop="opaque"
        disableAnimation
        isOpen={anytlsLogModal.open}
        placement="center"
        scrollBehavior="outside"
        size="3xl"
        onOpenChange={(open) => {
          if (!open) {
            setAnytlsLogModal({
              open: false,
              nodeId: null,
              nodeName: "",
              loading: false,
              logs: [],
              status: null,
            });
            return;
          }
          setAnytlsLogModal((prev) => ({ ...prev, open }));
        }}
      >
        <ModalContent>
          {(onClose) => (
            <>
              <ModalHeader>
                AnyTLS 运行日志
                {anytlsLogModal.nodeName ? ` · ${anytlsLogModal.nodeName}` : ""}
              </ModalHeader>
              <ModalBody>
                <div className="flex items-center justify-between gap-3 mb-2">
                  <div className="flex flex-wrap items-center gap-2">
                    <Chip size="sm" variant="flat" color={
                      anytlsLogModal.status?.state === "healthy"
                        ? "success"
                        : anytlsLogModal.status?.state === "degraded"
                          ? "warning"
                          : "default"
                    }>
                      状态 {anytlsLogModal.status?.state || "unknown"}
                    </Chip>
                    <Chip size="sm" variant="flat">
                      窗口 {anytlsLogModal.status?.windowSec || 0}s
                    </Chip>
                    <Chip size="sm" variant="flat">
                      最近日志 {anytlsLogModal.status?.recentCount || 0}
                    </Chip>
                    <Chip size="sm" variant="flat">
                      拒绝 {anytlsLogModal.status?.connReject || 0}
                    </Chip>
                    <Chip size="sm" variant="flat">
                      accept错 {anytlsLogModal.status?.acceptErr || 0}
                    </Chip>
                    <Chip size="sm" variant="flat">
                      握手超时 {anytlsLogModal.status?.handshakeTimeout || 0}
                    </Chip>
                    <Chip size="sm" variant="flat">
                      TLS握手错 {anytlsLogModal.status?.tlsHandshakeErr || 0}
                    </Chip>
                    <Chip size="sm" variant="flat">
                      握手重置 {anytlsLogModal.status?.tlsConnResetByPeer || 0}/
                      {anytlsLogModal.status?.tlsHandshakeErr || 0} ({(
                        (((anytlsLogModal.status?.tlsConnResetByPeer || 0) * 100) /
                          Math.max(1, anytlsLogModal.status?.tlsHandshakeErr || 0)) ||
                        0
                      ).toFixed(1)}%)
                    </Chip>
                    <Chip size="sm" variant="flat">
                      SNI失配 {anytlsLogModal.status?.tlsSniMismatch || 0}/
                      {anytlsLogModal.status?.tlsClientHello || 0} ({(
                        (((anytlsLogModal.status?.tlsSniMismatch || 0) * 100) /
                          Math.max(1, anytlsLogModal.status?.tlsClientHello || 0)) ||
                        0
                      ).toFixed(1)}%)
                    </Chip>
                    <Chip size="sm" variant="flat">
                      转发错 {anytlsLogModal.status?.streamErr || 0}
                    </Chip>
                    <Chip size="sm" variant="flat">
                      鉴权失败 {anytlsLogModal.status?.authFail || 0}
                    </Chip>
                    <Chip size="sm" variant="flat">
                      读包错 {anytlsLogModal.status?.readErr || 0}
                    </Chip>
                    <Chip size="sm" variant="flat">
                      出口错 {anytlsLogModal.status?.outboundErr || 0}
                    </Chip>
                  </div>
                  <Button
                    isLoading={anytlsLogModal.loading}
                    size="sm"
                    variant="flat"
                    onPress={() => {
                      if (!anytlsLogModal.nodeId) return;
                      void loadAnyTLSRuntimeLogs(anytlsLogModal.nodeId);
                    }}
                  >
                    刷新
                  </Button>
                </div>
                {anytlsLogModal.loading ? (
                  <div className="py-8 flex justify-center">
                    <Spinner size="sm" />
                  </div>
                ) : anytlsLogModal.logs.length === 0 ? (
                  <div className="text-sm text-default-500 py-6 text-center">
                    暂无 AnyTLS 运行日志
                  </div>
	                ) : (
	                  <div className="space-y-2 max-h-[58vh] overflow-y-auto">
	                    {anytlsLogModal.logs.map((it) => {
	                      const step = String(it.cmd || "").replace(/^OpLog:/, "");
	                      let parsed: any = null;
	                      if (it.stdout) {
	                        try {
	                          parsed = JSON.parse(String(it.stdout));
	                        } catch {
	                          parsed = null;
	                        }
	                      }
	                      const detailLine =
	                        parsed && typeof parsed === "object"
	                          ? [
	                              parsed.stage ? `stage=${parsed.stage}` : "",
	                              parsed.kind ? `kind=${parsed.kind}` : "",
	                              parsed.errorType ? `type=${parsed.errorType}` : "",
	                              parsed.network ? `network=${parsed.network}` : "",
	                              parsed.op ? `op=${parsed.op}` : "",
	                              parsed.local ? `local=${parsed.local}` : "",
	                              parsed.remote ? `remote=${parsed.remote}` : "",
	                              parsed.destination
	                                ? `destination=${parsed.destination}`
	                                : "",
	                              typeof parsed.timeout === "boolean"
	                                ? `timeout=${String(parsed.timeout)}`
	                                : "",
	                            ]
	                              .filter(Boolean)
	                              .join(" · ")
	                          : "";
	                      return (
	                        <div
	                          key={`anytls-log-${it.id}`}
	                          className="rounded-lg border border-default-200 p-2"
	                        >
                          <div className="text-xs text-default-500 mb-1">
                            {it.timeMs
                              ? new Date(it.timeMs).toLocaleString()
                              : "-"}
                            {step ? ` · ${step}` : ""}
                          </div>
	                          <div className="text-sm break-all">
	                            {it.message || "-"}
	                          </div>
	                          {parsed?.error ? (
	                            <div className="mt-1 text-xs text-danger break-all">
	                              错误：{String(parsed.error)}
	                            </div>
	                          ) : null}
	                          {detailLine ? (
	                            <div className="mt-1 text-[11px] text-default-500 break-all">
	                              {detailLine}
	                            </div>
	                          ) : null}
	                          {it.stderr ? (
	                            <pre className="mt-2 text-xs bg-danger-50 rounded p-2 overflow-x-auto whitespace-pre-wrap break-all">
	                              {it.stderr}
	                            </pre>
	                          ) : null}
                          {it.stdout ? (
                            <pre className="mt-2 text-xs bg-default-100 rounded p-2 overflow-x-auto whitespace-pre-wrap break-all">
                              {it.stdout}
                            </pre>
                          ) : null}
                        </div>
                      );
                    })}
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

      {/* 节点用量弹窗（管理员） */}
      <Modal
        backdrop="opaque"
        disableAnimation
        isOpen={usageModal.open}
        placement="center"
        scrollBehavior="outside"
        size="2xl"
        onOpenChange={(open) =>
          setUsageModal((prev) => ({ ...prev, open }))
        }
      >
        <ModalContent>
          {(onClose) => (
            <>
              <ModalHeader>节点用量 · {usageModal.nodeName}</ModalHeader>
              <ModalBody>
                {usageModal.loading ? (
                  <div className="text-sm text-default-500">加载中...</div>
                ) : usageModal.items.length === 0 ? (
                  <div className="text-sm text-default-500">暂无用量数据</div>
                ) : (
                  <div className="space-y-2">
                    {usageModal.items.map((it) => (
                      <div
                        key={`usage-${it.userId}`}
                        className="flex items-center justify-between gap-3 rounded-md border border-divider px-3 py-2"
                      >
                        <div className="text-sm font-medium text-foreground">
                          {it.userName || `用户${it.userId}`}
                        </div>
                        <div className="flex items-center gap-2 text-xs">
                          <Chip size="sm" variant="flat" color="primary">
                            ↑{formatFlowBytes(it.inFlow || 0)}
                          </Chip>
                          <Chip size="sm" variant="flat" color="success">
                            ↓{formatFlowBytes(it.outFlow || 0)}
                          </Chip>
                          <Chip size="sm" variant="flat">
                            总 {formatFlowBytes(it.flow || 0)}
                          </Chip>
                        </div>
                      </div>
                    ))}
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
        scrollBehavior="inside"
        size="3xl"
        onOpenChange={(open) => {
          if (!open) {
            stopDiagPolling();
            stopIperfPolling();
            setDiagState((prev) => ({
              ...prev,
              loading: false,
              content: "",
              done: false,
              requestId: "",
            }));
            setIperf3Status({
              status: "unknown",
              port: "",
              pid: "",
              loading: false,
            });
            setPprofState({
              enabled: false,
              addr: "",
              loading: false,
              fetchLoading: false,
              quickTesting: false,
              content: "",
              profile: "goroutine",
            });
          }
          setSelfCheckModal((prev) => ({ ...prev, open }));
        }}
      >
        <ModalContent>
          {(onClose) => (
            <>
              <ModalHeader>节点自检 · {selfCheckModal.nodeName}</ModalHeader>
              <ModalBody className="space-y-4">
                <div className="space-y-2">
                  <div className="text-sm font-medium">扩展诊断</div>
                  <div className="flex items-center gap-2 text-xs text-default-500">
                    <span>iperf3 状态：</span>
                    <Chip
                      size="sm"
                      variant="flat"
                      color={
                        iperf3Status.loading
                          ? "warning"
                          : iperf3Status.status === "running"
                            ? "success"
                            : iperf3Status.status === "stopped"
                              ? "default"
                              : "danger"
                      }
                    >
                      <span className="flex items-center gap-1">
                        <span
                          className={`inline-block w-2 h-2 rounded-full ${
                            iperf3Status.loading
                              ? "bg-yellow-400"
                              : iperf3Status.status === "running"
                                ? "bg-green-500"
                                : iperf3Status.status === "stopped"
                                  ? "bg-slate-400"
                                  : "bg-red-500"
                          }`}
                        />
                        {iperf3Status.loading
                          ? "查询中"
                          : iperf3Status.status === "running"
                            ? "运行中"
                            : iperf3Status.status === "stopped"
                              ? "已停止"
                              : "未知"}
                      </span>
                    </Chip>
                    {iperf3Status.port && (
                      <Chip size="sm" variant="flat">
                        端口 {iperf3Status.port}
                      </Chip>
                    )}
                  </div>
                  <div className="flex flex-wrap gap-2">
                    <Button
                      size="sm"
                      variant="flat"
                      color="primary"
                      isLoading={diagState.loading && diagState.kind === "backtrace"}
                      isDisabled={
                        (diagState.loading && diagState.kind === "backtrace") ||
                        (diagState.kind === "backtrace" &&
                          !diagState.done &&
                          !!diagState.requestId)
                      }
                      onPress={() => {
                        if (selfCheckModal.nodeId) {
                          startDiag(
                            { id: selfCheckModal.nodeId, name: selfCheckModal.nodeName } as Node,
                            "backtrace",
                          );
                        }
                      }}
                    >
                      三网回程测试
                    </Button>
                    {iperf3Status.status === "running" ? (
                      <Button
                        size="sm"
                        variant="flat"
                        color="default"
                        isLoading={diagState.loading && diagState.kind === "iperf3-stop"}
                        onPress={() => {
                          if (selfCheckModal.nodeId) {
                            startDiag(
                              { id: selfCheckModal.nodeId, name: selfCheckModal.nodeName } as Node,
                              "iperf3-stop",
                            );
                          }
                        }}
                      >
                        停止 iperf3
                      </Button>
                    ) : (
                      <Button
                        size="sm"
                        variant="flat"
                        color="warning"
                        isLoading={diagState.loading && diagState.kind === "iperf3-start"}
                        onPress={() => {
                          if (selfCheckModal.nodeId) {
                            startDiag(
                              { id: selfCheckModal.nodeId, name: selfCheckModal.nodeName } as Node,
                              "iperf3-start",
                            );
                          }
                        }}
                      >
                        启动 iperf3
                      </Button>
                    )}
                  </div>
                  {isAdmin && (
                    <div className="space-y-2 pt-1">
                      <div className="flex items-center gap-2 text-xs text-default-500">
                        <span>Agent pprof：</span>
                        <Chip
                          size="sm"
                          variant="flat"
                          color={
                            pprofState.loading
                              ? "warning"
                              : pprofState.enabled
                                ? "success"
                                : "default"
                          }
                        >
                          {pprofState.loading
                            ? "查询中"
                            : pprofState.enabled
                              ? "已开启"
                              : "未开启"}
                        </Chip>
                        {pprofState.addr ? (
                          <Chip size="sm" variant="flat">
                            {pprofState.addr}
                          </Chip>
                        ) : null}
                      </div>
                      <div className="flex flex-wrap gap-2">
                        <Button
                          size="sm"
                          variant="flat"
                          color="success"
                          isDisabled={
                            pprofState.loading ||
                            pprofState.quickTesting ||
                            pprofState.enabled
                          }
                          onPress={() => handlePprofControl("enable")}
                        >
                          开启 pprof
                        </Button>
                        <Button
                          size="sm"
                          variant="flat"
                          color="default"
                          isDisabled={
                            pprofState.loading ||
                            pprofState.quickTesting ||
                            !pprofState.enabled
                          }
                          onPress={() => handlePprofControl("disable")}
                        >
                          关闭 pprof
                        </Button>
                        <Button
                          size="sm"
                          variant="flat"
                          color="primary"
                          isLoading={
                            pprofState.fetchLoading &&
                            pprofState.profile === "goroutine"
                          }
                          isDisabled={
                            !pprofState.enabled ||
                            pprofState.loading ||
                            pprofState.quickTesting
                          }
                          onPress={() => handlePprofFetch("goroutine")}
                        >
                          查看 Goroutine
                        </Button>
                        <Button
                          size="sm"
                          variant="flat"
                          color="secondary"
                          isLoading={
                            pprofState.fetchLoading && pprofState.profile === "heap"
                          }
                          isDisabled={
                            !pprofState.enabled ||
                            pprofState.loading ||
                            pprofState.quickTesting
                          }
                          onPress={() => handlePprofFetch("heap")}
                        >
                          查看 Heap
                        </Button>
                        <Button
                          size="sm"
                          variant="flat"
                          color="warning"
                          isLoading={
                            pprofState.fetchLoading && pprofState.profile === "mutex"
                          }
                          isDisabled={
                            !pprofState.enabled ||
                            pprofState.loading ||
                            pprofState.quickTesting
                          }
                          onPress={() => handlePprofFetch("mutex")}
                        >
                          查看 Mutex
                        </Button>
                        <Button
                          size="sm"
                          variant="flat"
                          color="warning"
                          isLoading={
                            pprofState.fetchLoading && pprofState.profile === "block"
                          }
                          isDisabled={
                            !pprofState.enabled ||
                            pprofState.loading ||
                            pprofState.quickTesting
                          }
                          onPress={() => handlePprofFetch("block")}
                        >
                          查看 Block
                        </Button>
                        <Button
                          size="sm"
                          variant="flat"
                          color="warning"
                          isLoading={
                            pprofState.fetchLoading &&
                            pprofState.profile === "threadcreate"
                          }
                          isDisabled={
                            !pprofState.enabled ||
                            pprofState.loading ||
                            pprofState.quickTesting
                          }
                          onPress={() => handlePprofFetch("threadcreate")}
                        >
                          查看 ThreadCreate
                        </Button>
                        <Button
                          size="sm"
                          variant="flat"
                          color="danger"
                          isLoading={pprofState.quickTesting}
                          isDisabled={pprofState.loading || pprofState.fetchLoading}
                          onPress={handlePprofQuickTest}
                        >
                          一键回归测试
                        </Button>
                        <Button
                          size="sm"
                          variant="flat"
                          color="primary"
                          isDisabled={!pprofState.content || pprofState.fetchLoading}
                          onPress={copyPprofReport}
                        >
                          复制报告
                        </Button>
                        <Button
                          size="sm"
                          variant="light"
                          isDisabled={pprofState.loading || pprofState.quickTesting}
                          onPress={() => {
                            if (selfCheckModal.nodeId) {
                              refreshPprofStatus(selfCheckModal.nodeId);
                            }
                          }}
                        >
                          刷新状态
                        </Button>
                      </div>
                      <Textarea
                        readOnly
                        minRows={6}
                        className="font-mono text-xs"
                        value={
                          pprofState.content ||
                          (pprofState.fetchLoading ? "拉取中..." : "暂无 pprof 输出")
                        }
                      />
                    </div>
                  )}
                  <Textarea
                    readOnly
                    minRows={6}
                    className="font-mono text-xs"
                    value={
                      diagState.content ||
                      (diagState.loading ? "执行中..." : "暂无日志")
                    }
                  />
                </div>

                {selfCheckModal.loading ? (
                  <div className="text-sm text-default-500">检测中...</div>
                ) : selfCheckModal.result ? (
                  <div className="space-y-3 text-sm">
                    {["ping", "tcp"].map((key) => {
                      const item = selfCheckModal.result?.[key];
                      if (!item) return null;
                      return (
                        <div key={key} className="np-soft p-3">
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
        backdrop="opaque"
        disableAnimation
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
        backdrop="opaque"
        disableAnimation
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
        backdrop="opaque"
        disableAnimation
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
        backdrop="opaque"
        disableAnimation
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
                  <div key={item.label} className="relative np-soft p-3">
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
                    <pre className="np-soft bg-default-50/60 text-xs font-mono leading-relaxed whitespace-pre-wrap break-words p-3">
                      {item.value || ""}
                    </pre>
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
