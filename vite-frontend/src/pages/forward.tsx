import { useState, useEffect, useRef, useCallback, useMemo, memo } from "react";
import { Card, CardBody, CardHeader } from "@heroui/card";
import { Button } from "@heroui/button";
import { Input } from "@heroui/input";
import { Textarea } from "@heroui/input";
import { Select, SelectItem } from "@heroui/select";
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
import { Accordion, AccordionItem } from "@heroui/accordion";
import { Switch } from "@heroui/switch";
import toast from "react-hot-toast";
import {
  DndContext,
  closestCenter,
  KeyboardSensor,
  PointerSensor,
  useSensor,
  useSensors,
  DragEndEvent,
} from "@dnd-kit/core";
import {
  arrayMove,
  SortableContext,
  sortableKeyboardCoordinates,
  rectSortingStrategy,
} from "@dnd-kit/sortable";
import { useSortable } from "@dnd-kit/sortable";
import { CSS } from "@dnd-kit/utilities";

import OpsLogModal from "@/components/OpsLogModal";
import VirtualGrid from "@/components/VirtualGrid";
import {
  createForward,
  getForwardList,
  updateForward,
  deleteForward,
  forceDeleteForward,
  userTunnel,
  diagnoseForwardStep,
  diagnoseForward,
  updateForwardOrder,
  getForwardStatus,
  getForwardStatusDetail,
  getNodeInterfaces,
  getTunnelPath,
  getTunnelBind,
  getTunnelIface,
  getNodeList,
  getTunnelList,
} from "@/api";
import { JwtUtil } from "@/utils/jwt";
import { getCachedConfig } from "@/config/site";
import { usePageVisibility } from "@/hooks/usePageVisibility";

interface Forward {
  id: number;
  name: string;
  tunnelId: number;
  tunnelName: string;
  inIp: string;
  inPort: number;
  outPort?: number;
  remoteAddr: string;
  interfaceName?: string;
  strategy: string;
  status: number;
  inFlow: number;
  outFlow: number;
  serviceRunning: boolean;
  createdTime: string;
  userName?: string;
  userId?: number;
  inx?: number;
  configOk?: boolean;
}

const getStrategyDisplay = (strategy: string) => {
  switch (strategy) {
    case "fifo":
      return { color: "primary", text: "主备" };
    case "round":
      return { color: "success", text: "轮询" };
    case "rand":
      return { color: "warning", text: "随机" };
    default:
      return { color: "default", text: "未知" };
  }
};

const formatInAddress = (ipString: string, port: number): string => {
  if (!ipString || !port) return "";

  const ips = ipString
    .split(",")
    .map((ip) => ip.trim())
    .filter((ip) => ip);

  if (ips.length === 0) return "";

  if (ips.length === 1) {
    const ip = ips[0];

    if (ip.includes(":") && !ip.startsWith("[")) {
      return `[${ip}]:${port}`;
    } else {
      return `${ip}:${port}`;
    }
  }

  const firstIp = ips[0];
  let formattedFirstIp;

  if (firstIp.includes(":") && !firstIp.startsWith("[")) {
    formattedFirstIp = `[${firstIp}]`;
  } else {
    formattedFirstIp = firstIp;
  }

  return `${formattedFirstIp}:${port} (+${ips.length - 1})`;
};

const formatRemoteAddress = (addressString: string): string => {
  if (!addressString) return "";

  const addresses = addressString
    .split(",")
    .map((addr) => addr.trim())
    .filter((addr) => addr);

  if (addresses.length === 0) return "";
  if (addresses.length === 1) return addresses[0];

  return `${addresses[0]} (+${addresses.length - 1})`;
};

const hasMultipleAddresses = (addressString: string): boolean => {
  if (!addressString) return false;
  const addresses = addressString
    .split(",")
    .map((addr) => addr.trim())
    .filter((addr) => addr);

  return addresses.length > 1;
};

type SortableForwardCardProps = {
  forward: Forward;
  renderCard: (forward: Forward, listeners?: any) => JSX.Element;
};

const SortableForwardCard = memo(
  ({ forward, renderCard }: SortableForwardCardProps) => {
    if (!forward || !forward.id) return null;

    const {
      attributes,
      listeners,
      setNodeRef,
      transform,
      transition,
      isDragging,
    } = useSortable({ id: forward.id });

    const style = {
      transform: transform ? CSS.Transform.toString(transform) : undefined,
      transition: transition || undefined,
      opacity: isDragging ? 0.5 : 1,
    };

    return (
      <div ref={setNodeRef} style={style} {...attributes}>
        {renderCard(forward, listeners)}
      </div>
    );
  },
);
interface Tunnel {
  id: number;
  name: string;
  inNodePortSta?: number;
  inNodePortEnd?: number;
  // 以下字段用于只读预览/选择接口IP（若后端未返回则保持为可选）
  type?: number; // 1: 端口转发, 2: 隧道转发
  inNodeId?: number;
  outNodeId?: number;
}

interface ForwardForm {
  id?: number;
  userId?: number;
  name: string;
  tunnelId: number | null;
  inPort: number | null;
  remoteAddr: string;
  interfaceName?: string;
  strategy: string;
  // SS 参数移除，统一在节点信息“出口服务”里设置
}

type ForwardEditModalProps = {
  isOpen: boolean;
  onOpenChange: (open: boolean) => void;
  editForward: Forward | null;
  tunnels: Tunnel[];
  forwards: Forward[];
  previewTunnelMap: Record<number, any>;
  nodesCache: any[];
  ifaceCacheRef: React.MutableRefObject<Map<number, string[]>>;
  ifaceInflightRef: React.MutableRefObject<Set<number>>;
  onSaved: (payload: { isEdit: boolean; forwardId?: number }) => void;
  onOpsLogOpen: (requestId: string) => void;
};

const DEFAULT_FORWARD_FORM: ForwardForm = {
  name: "",
  tunnelId: null,
  inPort: null,
  remoteAddr: "",
  interfaceName: "",
  strategy: "fifo",
};

interface AddressItem {
  id: number;
  address: string;
  copying: boolean;
}

const ForwardIfacePicker = memo(
  ({
    selectedTunnel,
    entryNodeId,
    currentValue,
    onSelect,
    active,
    cacheRef,
    inflightRef,
  }: {
    selectedTunnel: Tunnel | null;
    entryNodeId: number | null;
    currentValue?: string;
    onSelect: (ip: string) => void;
    active: boolean;
    cacheRef: React.MutableRefObject<Map<number, string[]>>;
    inflightRef: React.MutableRefObject<Set<number>>;
  }) => {
    const [ips, setIps] = useState<string[]>([]);
    const [loadingIps, setLoadingIps] = useState<boolean>(false);
    const lastNodeIdRef = useRef<number | null>(null);

    const doRefresh = async (nodeId?: number) => {
      const t = selectedTunnel;
      const type = t?.type ?? 1;
      const nid =
        nodeId ??
        Number(
          t
            ? type === 2 && t.outNodeId
              ? t.outNodeId
              : t.inNodeId
            : entryNodeId || 0,
        );

      if (!nid) return;
      if (inflightRef.current.has(nid)) return;
      inflightRef.current.add(nid);
      // 仅在本地没有列表时显示 loading，避免 UI 抖动
      if (ips.length === 0) setLoadingIps(true);
      try {
        const res: any = await getNodeInterfaces(nid);
        const list =
          res && res.code === 0 && Array.isArray(res.data?.ips)
            ? (res.data.ips as string[])
            : [];

        cacheRef.current.set(nid, list);
        setIps(list);
      } catch {
        /* noop */
      } finally {
        inflightRef.current.delete(nid);
        setLoadingIps(false);
      }
    };

    // 自动：弹窗打开且切换到新隧道时，若无缓存则自动拉取一次
    useEffect(() => {
      if (!active) return;
      const t = selectedTunnel;
      const type = t?.type ?? 1;
      const nodeId = Number(
        t && type === 2 && t.outNodeId ? t.outNodeId : t?.inNodeId,
      );

      lastNodeIdRef.current = nodeId || null;
      if (!nodeId) {
        setIps([]);
        setLoadingIps(false);

        return;
      }
      if (cacheRef.current.has(nodeId)) {
        setIps(cacheRef.current.get(nodeId) || []);
        setLoadingIps(false);

        return;
      }
      // 无缓存则自动拉一次（有并发锁保护）
      void doRefresh(nodeId);
    }, [selectedTunnel?.id, active]);

    return (
      <Select
        description={
          loadingIps
            ? "正在获取接口IP…"
            : ips.length
              ? "请选择出口IP"
              : "未获取到接口IP"
        }
        label="出口IP"
        placeholder={"出口IP列表"}
        selectedKeys={currentValue ? [currentValue] : []}
        size="sm"
        variant="bordered"
        onSelectionChange={(keys) => {
          const k = Array.from(keys)[0] as string;

          if (k) onSelect(k);
        }}
      >
        {ips.map((ip) => (
          <SelectItem key={ip}>{ip}</SelectItem>
        ))}
      </Select>
    );
  },
);

const ForwardEditModal = memo(
  ({
    isOpen,
    onOpenChange,
    editForward,
    tunnels,
    forwards,
    previewTunnelMap,
    nodesCache,
    ifaceCacheRef,
    ifaceInflightRef,
    onSaved,
    onOpsLogOpen,
  }: ForwardEditModalProps) => {
    const isEdit = !!editForward;
    const [form, setForm] = useState<ForwardForm>(DEFAULT_FORWARD_FORM);
    const [errors, setErrors] = useState<{ [key: string]: string }>({});
    const [submitLoading, setSubmitLoading] = useState(false);
    const [selectedTunnel, setSelectedTunnel] = useState<Tunnel | null>(null);
    const [entryNodeId, setEntryNodeId] = useState<number | null>(null);
    const [entryApiOn, setEntryApiOn] = useState<boolean | null>(null);
    const [previewType, setPreviewType] = useState<number | undefined>(
      undefined,
    );
    const [previewInNodeId, setPreviewInNodeId] = useState<
      number | undefined
    >(undefined);
    const [previewOutNodeId, setPreviewOutNodeId] = useState<
      number | undefined
    >(undefined);
    const [previewPath, setPreviewPath] = useState<number[]>([]);
    const [previewBind, setPreviewBind] = useState<Record<number, string>>({});
    const [previewIface, setPreviewIface] = useState<Record<number, string>>({});
    const [previewExitBind, setPreviewExitBind] = useState<string>("");
    const [inPortAuto, setInPortAuto] = useState(true);
    const [outPort, setOutPort] = useState<number | null>(null);
    const [outPortTouched, setOutPortTouched] = useState(false);
    const [midPorts, setMidPorts] = useState<Record<number, number | null>>({});
    const [midPortsTouched, setMidPortsTouched] = useState<Set<number>>(
      new Set(),
    );
    const [portPrefsLoading, setPortPrefsLoading] = useState(false);
    const portPrefsKeyRef = useRef<string>("");

    const nodeNameMap = useMemo(() => {
      const nMap: Record<number, string> = {};

      (nodesCache || []).forEach((n: any) => {
        if (n && (n as any).id != null)
          nMap[Number((n as any).id)] = String(
            (n as any).name || "节点" + (n as any).id,
          );
      });

      return nMap;
    }, [nodesCache]);

    const getNodePortRange = useCallback(
      (nodeId?: number) => {
        if (!nodeId) return { min: 1, max: 65535, label: "" };
        const node = (nodesCache || []).find(
          (n: any) => Number(n?.id || 0) === Number(nodeId),
        );
        const min = Number(node?.portSta || 1);
        const max = Number(node?.portEnd || 65535);
        const label =
          node?.portSta && node?.portEnd ? `${min}-${max}` : "";

        return { min, max, label };
      },
      [nodesCache],
    );

    const getAddressCount = (addressString: string): number => {
      if (!addressString) return 0;
      const addresses = addressString
        .split("\n")
        .map((addr) => addr.trim())
        .filter((addr) => addr);

      return addresses.length;
    };

    const tunnelOptions = useMemo(
      () =>
        tunnels.map((tunnel) => (
          <SelectItem key={tunnel.id} textValue={tunnel.name}>
            {tunnel.name}
          </SelectItem>
        )),
      [tunnels],
    );
    const nodeOptions = useMemo(
      () =>
        (nodesCache || []).map((node: any) => (
          <SelectItem key={node.id} textValue={node.name}>
            {node.name || `节点${node.id}`}
          </SelectItem>
        )),
      [nodesCache],
    );

    const usedPortsByNode = useMemo(() => {
      const map = new Map<number, Set<number>>();
      const add = (nodeId: number, port: number) => {
        if (!nodeId || !port) return;
        if (!map.has(nodeId)) map.set(nodeId, new Set<number>());
        map.get(nodeId)!.add(port);
      };

      (nodesCache || []).forEach((node: any) => {
        const nodeId = Number(node?.id || 0);
        if (!nodeId) return;
        const used = Array.isArray(node?.usedPorts) ? node.usedPorts : [];
        used.forEach((p: any) => {
          const port = Number(p);
          if (port > 0) add(nodeId, port);
        });
      });

      (forwards || []).forEach((f) => {
        if (!f?.inPort) return;
        if (editForward && f.id === editForward.id) return;
        const tInfo =
          previewTunnelMap?.[f.tunnelId] ||
          tunnels.find((t) => t.id === f.tunnelId);
        const nodeId = Number(tInfo?.inNodeId || 0);
        if (nodeId) add(nodeId, Number(f.inPort));
      });

      return map;
    }, [nodesCache, forwards, previewTunnelMap, tunnels, editForward]);

    const getSuggestedInPort = useCallback(
      (nodeId: number, minPort: number, maxPort: number) => {
        if (!nodeId) return null;
        const used = usedPortsByNode.get(nodeId) || new Set<number>();

        for (let port = minPort; port <= maxPort; port += 1) {
          if (!used.has(port)) return port;
        }

        return null;
      },
      [usedPortsByNode],
    );

    const suggestedInPort = useMemo(() => {
      let nodeId = 0;
      let minPort = 10000;
      let maxPort = 65535;

      if (selectedTunnel?.id) {
        const tInfo =
          previewTunnelMap?.[selectedTunnel.id] ||
          tunnels.find((t) => t.id === selectedTunnel.id);
        nodeId = Number(tInfo?.inNodeId || 0);
        const node = (nodesCache || []).find(
          (n: any) => Number(n?.id || 0) === nodeId,
        );
        minPort = Number(tInfo?.inNodePortSta || node?.portSta || minPort);
        maxPort = Number(tInfo?.inNodePortEnd || node?.portEnd || maxPort);
      } else if (entryNodeId) {
        nodeId = Number(entryNodeId);
        const node = (nodesCache || []).find(
          (n: any) => Number(n?.id || 0) === nodeId,
        );
        minPort = Number(node?.portSta || minPort);
        maxPort = Number(node?.portEnd || maxPort);
      }

      if (!nodeId) return null;
      return getSuggestedInPort(nodeId, minPort, maxPort);
    }, [
      selectedTunnel,
      previewTunnelMap,
      tunnels,
      nodesCache,
      entryNodeId,
      getSuggestedInPort,
    ]);

    const loadTunnelPreview = useCallback(
      async (tunnelId: number) => {
        const tInfo = previewTunnelMap[tunnelId];

        if (tInfo) {
          setPreviewType(tInfo.type);
          setPreviewInNodeId(tInfo.inNodeId);
          setPreviewOutNodeId(tInfo.outNodeId || undefined);
          setEntryNodeId(tInfo.inNodeId || null);
        } else {
          setPreviewType(undefined);
          setPreviewInNodeId(undefined);
          setPreviewOutNodeId(undefined);
          setEntryNodeId(null);
        }

        try {
          const [rp, rb, ri] = await Promise.all([
            getTunnelPath(tunnelId),
            getTunnelBind(tunnelId),
            getTunnelIface(tunnelId),
          ]);

          if (rp.code === 0 && Array.isArray(rp.data?.path))
            setPreviewPath(rp.data.path as number[]);
          else setPreviewPath([]);
          const bMap: Record<number, string> = {};

          if (rb.code === 0 && Array.isArray(rb.data?.binds)) {
            rb.data.binds.forEach((x: any) => {
              if (x?.nodeId) bMap[Number(x.nodeId)] = String(x.ip || "");
            });
          }
          setPreviewBind(bMap);
          const iMap: Record<number, string> = {};

          if (ri.code === 0 && Array.isArray(ri.data?.ifaces)) {
            ri.data.ifaces.forEach((x: any) => {
              if (x?.nodeId) iMap[Number(x.nodeId)] = String(x.ip || "");
            });
          }
          setPreviewIface(iMap);
          const outId = previewTunnelMap[tunnelId]?.outNodeId || undefined;

          if (outId && bMap[outId]) setPreviewExitBind(bMap[outId]);
          else setPreviewExitBind("");
        } catch {
          setPreviewPath([]);
          setPreviewBind({});
          setPreviewIface({});
          setPreviewExitBind("");
        }
      },
      [previewTunnelMap],
    );

    const loadForwardPortPrefs = useCallback(
      async (forwardId: number, pathLen: number) => {
        setPortPrefsLoading(true);
        try {
          const r: any = await getForwardStatusDetail(forwardId);

          if (r && r.code === 0) {
            const nodes: any[] = Array.isArray(r.data?.nodes)
              ? r.data.nodes
              : [];
            const exitNode = nodes.find((n) => n?.role === "exit");
            const exitPortRaw =
              exitNode?.expectedPort ?? exitNode?.actualPort ?? null;
            const nextOutPort =
              exitPortRaw != null ? Number(exitPortRaw) : null;
            const mids = nodes.filter((n) => n?.role === "mid");
            const mp: Record<number, number | null> = {};

            for (let i = 0; i < pathLen; i++) {
              const node = mids[i];
              const raw = node?.expectedPort ?? node?.actualPort ?? null;
              mp[i] = raw != null ? Number(raw) : null;
            }
            setOutPort(nextOutPort && nextOutPort > 0 ? nextOutPort : null);
            setMidPorts(mp);
            setOutPortTouched(false);
            setMidPortsTouched(new Set());
          } else {
            setOutPort(null);
            setMidPorts({});
            setOutPortTouched(false);
            setMidPortsTouched(new Set());
          }
        } catch {
          setOutPort(null);
          setMidPorts({});
          setOutPortTouched(false);
          setMidPortsTouched(new Set());
        } finally {
          setPortPrefsLoading(false);
        }
      },
      [],
    );

    const handleTunnelChange = useCallback(
      (tunnelId: string) => {
        const tid = parseInt(tunnelId);
        const tunnel = tunnels.find((t) => t.id === tid);

        setSelectedTunnel(tunnel || null);
        setForm((prev) => ({ ...prev, tunnelId: tid }));
        void loadTunnelPreview(tid);
      },
      [tunnels, loadTunnelPreview],
    );

    useEffect(() => {
      if (!isOpen) return;
      setErrors({});
      if (editForward) {
        setForm({
          id: editForward.id,
          userId: editForward.userId,
          name: editForward.name,
          tunnelId: editForward.tunnelId,
          inPort: editForward.inPort,
          remoteAddr: editForward.remoteAddr.split(",").join("\n"),
          interfaceName: editForward.interfaceName || "",
          strategy: editForward.strategy || "fifo",
        });
        const tunnel = tunnels.find((t) => t.id === editForward.tunnelId);

        setSelectedTunnel(tunnel || null);
        setInPortAuto(false);
        setOutPort(null);
        setMidPorts({});
        setOutPortTouched(false);
        setMidPortsTouched(new Set());
        portPrefsKeyRef.current = "";
        void loadTunnelPreview(editForward.tunnelId);
      } else {
        setForm(DEFAULT_FORWARD_FORM);
        setSelectedTunnel(null);
        setEntryNodeId(null);
        setEntryApiOn(null);
        setPreviewType(undefined);
        setPreviewInNodeId(undefined);
        setPreviewOutNodeId(undefined);
        setPreviewPath([]);
        setPreviewBind({});
        setPreviewIface({});
        setPreviewExitBind("");
        setInPortAuto(true);
        setOutPort(null);
        setMidPorts({});
        setOutPortTouched(false);
        setMidPortsTouched(new Set());
        portPrefsKeyRef.current = "";
      }
    }, [editForward, isOpen, loadTunnelPreview, tunnels]);

    useEffect(() => {
      if (!isOpen) return;
      if (isEdit) return;
      if (!selectedTunnel?.id && !entryNodeId) return;
      if (!inPortAuto) return;
      if (form.inPort !== null) return;

      const suggested = suggestedInPort;

      if (suggested) {
        setForm((prev) => ({ ...prev, inPort: suggested }));
      }
    }, [
      isOpen,
      isEdit,
      selectedTunnel,
      entryNodeId,
      inPortAuto,
      form.inPort,
      suggestedInPort,
    ]);

    useEffect(() => {
      if (!entryNodeId) {
        setEntryApiOn(null);

        return;
      }
      const node: any = (nodesCache || []).find(
        (n: any) => Number(n.id) === Number(entryNodeId),
      );

      setEntryApiOn(
        typeof node?.gostApi !== "undefined" ? node.gostApi === 1 : null,
      );
    }, [entryNodeId, nodesCache]);

    useEffect(() => {
      if (!isOpen || !isEdit || !editForward) return;
      if (previewType !== 2) {
        setOutPort(null);
        setMidPorts({});
        setOutPortTouched(false);
        setMidPortsTouched(new Set());
        portPrefsKeyRef.current = "";
        return;
      }
      const key = `${editForward.id || 0}:${previewPath.length}`;
      if (!editForward.id || portPrefsKeyRef.current === key) return;
      portPrefsKeyRef.current = key;
      void loadForwardPortPrefs(editForward.id, previewPath.length);
    }, [
      isOpen,
      isEdit,
      editForward,
      previewType,
      previewPath.length,
      loadForwardPortPrefs,
    ]);

    const validateForm = (): boolean => {
      const newErrors: { [key: string]: string } = {};

      if (!form.name.trim()) {
        newErrors.name = "请输入转发名称";
      } else if (form.name.length < 2 || form.name.length > 50) {
        newErrors.name = "转发名称长度应在2-50个字符之间";
      }

      if (!form.tunnelId && !entryNodeId) {
        newErrors.tunnelId = "请选择隧道或入口节点";
      }

      if (!form.remoteAddr.trim()) {
        newErrors.remoteAddr = "请输入远程地址";
      } else {
        const addresses = form.remoteAddr
          .split("\n")
          .map((addr) => addr.trim())
          .filter((addr) => addr);
        const ipv4Pattern =
          /^(25[0-5]|2[0-4][0-9]|[01]?[0-9][0-9]?)\.(25[0-5]|2[0-4][0-9]|[01]?[0-9][0-9]?)\.(25[0-5]|2[0-4][0-9]|[01]?[0-9][0-9]?)\.(25[0-5]|2[0-4][0-9]|[01]?[0-9][0-9]?):\d+$/;
        const ipv6FullPattern =
          /^\[((([0-9a-fA-F]{1,4}:){7}([0-9a-fA-F]{1,4}|:))|(([0-9a-fA-F]{1,4}:){6}(:[0-9a-fA-F]{1,4}|((25[0-5]|2[0-4]\d|1\d\d|[1-9]?\d)(\.(25[0-5]|2[0-4]\d|1\d\d|[1-9]?\d)){3})|:))|(([0-9a-fA-F]{1,4}:){5}(((:[0-9a-fA-F]{1,4}){1,2})|:((25[0-5]|2[0-4]\d|1\d\d|[1-9]?\d)(\.(25[0-5]|2[0-4]\d|1\d\d|[1-9]?\d)){3})|:))|(([0-9a-fA-F]{1,4}:){4}(((:[0-9a-fA-F]{1,4}){1,3})|((:[0-9a-fA-F]{1,4})?:((25[0-5]|2[0-4]\d|1\d\d|[1-9]?\d)(\.(25[0-5]|2[0-4]\d|1\d\d|[1-9]?\d)){3}))|:))|(([0-9a-fA-F]{1,4}:){3}(((:[0-9a-fA-F]{1,4}){1,4})|((:[0-9a-fA-F]{1,4}){0,2}:((25[0-5]|2[0-4]\d|1\d\d|[1-9]?\d)(\.(25[0-5]|2[0-4]\d|1\d\d|[1-9]?\d)){3}))|:))|(([0-9a-fA-F]{1,4}:){2}(((:[0-9a-fA-F]{1,4}){1,5})|((:[0-9a-fA-F]{1,4}){0,3}:((25[0-5]|2[0-4]\d|1\d\d|[1-9]?\d)(\.(25[0-5]|2[0-4]\d|1\d\d|[1-9]?\d)){3}))|:))|(([0-9a-fA-F]{1,4}:){1}(((:[0-9a-fA-F]{1,4}){1,6})|((:[0-9a-fA-F]{1,4}){0,4}:((25[0-5]|2[0-4]\d|1\d\d|[1-9]?\d)(\.(25[0-5]|2[0-4]\d|1\d\d|[1-9]?\d)){3}))|:))|(:(((:[0-9a-fA-F]{1,4}){1,7})|((:[0-9a-fA-F]{1,4}){0,5}:((25[0-5]|2[0-4]\d|1\d\d|[1-9]?\d)(\.(25[0-5]|2[0-4]\d|1\d\d|[1-9]?\d)){3}))|:)))\]:\d+$/;
        const domainPattern =
          /^[a-zA-Z0-9]([a-zA-Z0-9-]{0,61}[a-zA-Z0-9])?(\.[a-zA-Z0-9]([a-zA-Z0-9-]{0,61}[a-zA-Z0-9])?)*:\d+$/;

        for (let i = 0; i < addresses.length; i++) {
          const addr = addresses[i];

          if (
            !ipv4Pattern.test(addr) &&
            !ipv6FullPattern.test(addr) &&
            !domainPattern.test(addr)
          ) {
            newErrors.remoteAddr = `第${i + 1}行地址格式错误`;
            break;
          }
        }
      }

      if (form.inPort !== null && (form.inPort < 1 || form.inPort > 65535)) {
        newErrors.inPort = "端口号必须在1-65535之间";
      }

      if (form.inPort) {
        if (
          selectedTunnel &&
          selectedTunnel.inNodePortSta &&
          selectedTunnel.inNodePortEnd
        ) {
          if (
            form.inPort < selectedTunnel.inNodePortSta ||
            form.inPort > selectedTunnel.inNodePortEnd
          ) {
            newErrors.inPort = `端口号必须在${selectedTunnel.inNodePortSta}-${selectedTunnel.inNodePortEnd}范围内`;
          }
        } else if (entryNodeId) {
          const node = (nodesCache || []).find(
            (n: any) => Number(n?.id || 0) === Number(entryNodeId),
          );
          if (node?.portSta && node?.portEnd) {
            if (form.inPort < node.portSta || form.inPort > node.portEnd) {
              newErrors.inPort = `端口号必须在${node.portSta}-${node.portEnd}范围内`;
            }
          }
        }
      }

      if (isEdit && previewType === 2) {
        if (outPortTouched || outPort !== null) {
          if (outPort !== null) {
            if (outPort < 1 || outPort > 65535) {
              newErrors.outPort = "端口号必须在1-65535之间";
            } else {
              const range = getNodePortRange(previewOutNodeId || undefined);
              if (
                range.label &&
                (outPort < range.min || outPort > range.max)
              ) {
                newErrors.outPort = `端口号必须在${range.label}范围内`;
              }
            }
          }
        }
        previewPath.forEach((nid, idx) => {
          const touched = midPortsTouched.has(idx);
          const value = midPorts[idx];

          if (!touched && value == null) return;
          if (value == null) return;
          if (value < 1 || value > 65535) {
            newErrors[`midPort_${idx}`] = "端口号必须在1-65535之间";
            return;
          }
          const range = getNodePortRange(nid);
          if (range.label && (value < range.min || value > range.max)) {
            newErrors[`midPort_${idx}`] = `端口号必须在${range.label}范围内`;
          }
        });
      }

      setErrors(newErrors);

      return Object.keys(newErrors).length === 0;
    };

    const handleSubmit = async () => {
      if (!validateForm()) return;

      setSubmitLoading(true);
      try {
        const processedRemoteAddr = form.remoteAddr
          .split("\n")
          .map((addr) => addr.trim())
          .filter((addr) => addr)
          .join(",");

        const addressCount = processedRemoteAddr.split(",").length;

        let res;

        if (isEdit) {
          const updateData: any = {
            id: form.id,
            userId: form.userId,
            name: form.name,
            tunnelId: form.tunnelId,
            inPort: form.inPort,
            remoteAddr: processedRemoteAddr,
            interfaceName: form.interfaceName,
            strategy: addressCount > 1 ? form.strategy : "fifo",
          };

          if (previewType === 2) {
            if (outPortTouched) {
              updateData.outPort = outPort ? outPort : 0;
            }
            if (midPortsTouched.size > 0) {
              updateData.midPorts = Array.from(midPortsTouched)
                .sort((a, b) => a - b)
                .map((idx) => ({
                  idx,
                  port: midPorts[idx] ? midPorts[idx] : 0,
                }));
            }
          }

          res = await updateForward(updateData);
        } else {
          const createData = {
            name: form.name,
            tunnelId: form.tunnelId || 0,
            entryNodeId: form.tunnelId ? undefined : entryNodeId,
            inPort: form.inPort,
            remoteAddr: processedRemoteAddr,
            interfaceName: form.interfaceName,
            strategy: addressCount > 1 ? form.strategy : "fifo",
          };

          res = await createForward(createData);
        }

        if (res.code === 0) {
          toast.success(isEdit ? "修改成功" : "创建成功");
          try {
            const rid =
              res.data && (res.data as any).requestId
                ? String((res.data as any).requestId)
                : "";

            if (rid) {
              onOpsLogOpen(rid);
              toast.custom(
                (t) => (
                  <div className="px-4 py-3 bg-content1 rounded shadow border border-default-200 flex items-center gap-3">
                    <span>{isEdit ? "修改成功" : "创建成功"}</span>
                    <button
                      className="text-primary underline"
                      onClick={() => {
                        onOpsLogOpen(rid);
                        toast.dismiss(t.id);
                      }}
                    >
                      查看日志
                    </button>
                  </div>
                ),
                { duration: 5000 },
              );
            }
          } catch {}
          try {
            const { enableGostApi } = await import("@/api");
            const tid = form.tunnelId as number;
            const tInfo = previewTunnelMap[tid];
            const inNodeId = tInfo?.inNodeId as number | undefined;

            if (inNodeId) {
              const node: any = nodesCache.find(
                (n: any) => Number(n.id) === Number(inNodeId),
              );
              const apiOn = !!(node && node.gostApi === 1);

              if (!apiOn) {
                toast.custom(
                  (t) => (
                    <div className="px-4 py-3 bg-warning-50 rounded shadow border border-warning-200 flex items-center gap-3">
                      <span>该入口节点未启用 GOST API，无法下发服务。</span>
                      <button
                        className="text-primary underline"
                        onClick={async () => {
                          try {
                            await enableGostApi(inNodeId);
                            toast.success("已发送开启 GOST API 指令");
                          } catch (e: any) {
                            toast.error(e?.message || "发送失败");
                          } finally {
                            toast.dismiss(t.id);
                          }
                        }}
                      >
                        开启 GOST API
                      </button>
                    </div>
                  ),
                  { duration: 8000 },
                );
              }
            }
          } catch {}
          onOpenChange(false);
          onSaved({ isEdit, forwardId: form.id });
        } else {
          toast.error(res.msg || "操作失败");
        }
      } catch (error) {
        console.error("提交失败:", error);
        toast.error("操作失败");
      } finally {
        setSubmitLoading(false);
      }
    };

    return (
      <Modal
        backdrop="opaque"
        disableAnimation
        isOpen={isOpen}
        placement="top-center"
        scrollBehavior="inside"
        size="2xl"
        onOpenChange={onOpenChange}
      >
        <ModalContent>
          {(onClose) => (
            <>
              <ModalHeader className="flex flex-col gap-1">
                <h2 className="text-xl font-bold">
                  {isEdit ? "编辑转发" : "新增转发"}
                </h2>
                <p className="text-small text-default-500">
                  {isEdit ? "修改现有转发配置的信息" : "创建新的转发配置"}
                </p>
              </ModalHeader>
              <ModalBody>
                <div className="space-y-4 pb-4">
                  <Input
                    errorMessage={errors.name}
                    isInvalid={!!errors.name}
                    label="转发名称"
                    placeholder="请输入转发名称"
                    value={form.name}
                    variant="bordered"
                    onChange={(e) =>
                      setForm((prev) => ({ ...prev, name: e.target.value }))
                    }
                  />

                  <Select
                    errorMessage={errors.tunnelId}
                    isInvalid={!!errors.tunnelId}
                    label="选择隧道（可选）"
                    placeholder="可不选，直接转发将自动创建线路"
                    selectedKeys={
                      form.tunnelId ? [form.tunnelId.toString()] : []
                    }
                    variant="bordered"
                    onSelectionChange={(keys) => {
                      const selectedKey = Array.from(keys)[0] as string;

                      if (selectedKey) {
                        handleTunnelChange(selectedKey);
                      }
                    }}
                  >
                    {tunnelOptions}
                  </Select>

                  {!form.tunnelId && (
                    <Select
                      errorMessage={errors.tunnelId}
                      isInvalid={!!errors.tunnelId}
                      label="入口节点（直连）"
                      placeholder="请选择入口节点"
                      selectedKeys={
                        entryNodeId ? [entryNodeId.toString()] : []
                      }
                      variant="bordered"
                      onSelectionChange={(keys) => {
                        const selectedKey = Array.from(keys)[0] as string;
                        const next = selectedKey ? Number(selectedKey) : null;

                        setEntryNodeId(next);
                      }}
                    >
                      {nodeOptions}
                    </Select>
                  )}

                  {entryNodeId ? (
                    <div className="p-3 border border-default-200 rounded-lg flex items-center justify-between">
                      <div className="text-sm">
                        <div className="text-default-600">入口节点 API</div>
                        <div className="text-xs text-default-500 mt-1">
                          {entryApiOn === null
                            ? "检测中…"
                            : entryApiOn
                              ? "已启用，可直接下发服务"
                              : "未启用，需先开启后再保存"}
                        </div>
                      </div>
                      {entryApiOn === false && (
                        <Button
                          color="primary"
                          size="sm"
                          variant="flat"
                          onPress={async () => {
                            try {
                              const { enableGostApi } = await import("@/api");

                              await enableGostApi(entryNodeId);
                              toast.success(
                                "已发送开启 GOST API 指令，请稍候刷新",
                              );
                            } catch (e: any) {
                              toast.error(e?.message || "发送失败");
                            }
                          }}
                        >
                          开启 GOST API
                        </Button>
                      )}
                    </div>
                  ) : null}

                  <Input
                    description={
                      selectedTunnel &&
                      selectedTunnel.inNodePortSta &&
                      selectedTunnel.inNodePortEnd
                        ? `允许范围: ${selectedTunnel.inNodePortSta}-${selectedTunnel.inNodePortEnd}（默认建议端口可修改）`
                        : entryNodeId
                          ? (() => {
                              const node = (nodesCache || []).find(
                                (n: any) =>
                                  Number(n?.id || 0) === Number(entryNodeId),
                              );
                              if (node?.portSta && node?.portEnd) {
                                return `允许范围: ${node.portSta}-${node.portEnd}（默认建议端口可修改）`;
                              }
                              return "默认建议端口可修改，清空将自动分配";
                            })()
                          : "默认建议端口可修改，清空将自动分配"
                    }
                    errorMessage={errors.inPort}
                    isInvalid={!!errors.inPort}
                    label="入口端口"
                    placeholder="默认建议端口，可修改"
                    type="number"
                    value={form.inPort?.toString() || ""}
                    variant="bordered"
                    onChange={(e) => {
                      const next = e.target.value
                        ? parseInt(e.target.value)
                        : null;

                      setInPortAuto(false);
                      setForm((prev) => ({
                        ...prev,
                        inPort: next,
                      }));
                    }}
                  />
                  <div className="flex items-center justify-between text-xs text-default-500">
                    <span>
                      建议端口:{" "}
                      {suggestedInPort ? suggestedInPort : "暂无可用"}
                    </span>
                    {suggestedInPort ? (
                      <Button
                        size="sm"
                        variant="flat"
                        onPress={() => {
                          setInPortAuto(false);
                          setForm((prev) => ({
                            ...prev,
                            inPort: suggestedInPort,
                          }));
                        }}
                        isDisabled={form.inPort === suggestedInPort}
                      >
                        使用建议端口
                      </Button>
                    ) : null}
                  </div>

                  <Textarea
                    description="格式: IP:端口 或 域名:端口，支持多个地址（每行一个）"
                    errorMessage={errors.remoteAddr}
                    isInvalid={!!errors.remoteAddr}
                    label="远程地址"
                    maxRows={6}
                    minRows={3}
                    placeholder="请输入远程地址，多个地址用换行分隔&#10;例如:&#10;192.168.1.100:8080&#10;example.com:3000"
                    value={form.remoteAddr}
                    variant="bordered"
                    onChange={(e) =>
                      setForm((prev) => ({
                        ...prev,
                        remoteAddr: e.target.value,
                      }))
                    }
                  />

                  <ForwardIfacePicker
                    active={isOpen}
                    cacheRef={ifaceCacheRef}
                    currentValue={form.interfaceName || ""}
                    inflightRef={ifaceInflightRef}
                    entryNodeId={entryNodeId}
                    selectedTunnel={selectedTunnel}
                    onSelect={(ip) =>
                      setForm((prev) => ({ ...prev, interfaceName: ip }))
                    }
                  />

                  {selectedTunnel && (
                    <Card className="border border-default-200">
                      <CardHeader>
                        <div className="font-semibold">
                          隧道多级路径（只读）
                        </div>
                      </CardHeader>
                      <CardBody>
                        {previewInNodeId ? (
                          <div className="space-y-2 text-sm">
                            <div>
                              <span className="text-default-600">入口</span>：
                              <code className="ml-1">
                                {nodeNameMap[previewInNodeId] ||
                                  `#${previewInNodeId}`}
                              </code>
                              {previewIface[previewInNodeId] && (
                                <span className="ml-2 text-default-500">
                                  出站IP:{" "}
                                  <code>{previewIface[previewInNodeId]}</code>
                                </span>
                              )}
                            </div>
                            {previewPath.length > 0 ? (
                              previewPath.map((nid, idx) => (
                                <div key={nid} className="pl-4">
                                  <span className="text-default-600">
                                    中继{idx + 1}
                                  </span>
                                  ：
                                  <code className="ml-1">
                                    {nodeNameMap[nid] || `#${nid}`}
                                  </code>
                                  {previewBind[nid] && (
                                    <span className="ml-2 text-default-500">
                                      监听IP: <code>{previewBind[nid]}</code>
                                    </span>
                                  )}
                                  {previewIface[nid] && (
                                    <span className="ml-2 text-default-500">
                                      出站IP: <code>{previewIface[nid]}</code>
                                    </span>
                                  )}
                                </div>
                              ))
                            ) : (
                              <div className="pl-4 text-default-400">
                                未配置中继节点
                              </div>
                            )}
                            {previewType === 2 && previewOutNodeId ? (
                              <div className="pl-4">
                                <span className="text-default-600">出口</span>：
                                <code className="ml-1">
                                  {nodeNameMap[previewOutNodeId] ||
                                    `#${previewOutNodeId}`}
                                </code>
                                {previewExitBind && (
                                  <span className="ml-2 text-default-500">
                                    监听IP: <code>{previewExitBind}</code>
                                  </span>
                                )}
                              </div>
                            ) : null}
                            <div className="text-2xs text-default-400 mt-1">
                              说明：路径与节点 IP 请在“隧道管理”页维护。
                            </div>
                          </div>
                        ) : (
                          <div className="text-default-400 text-sm">
                            未加载到隧道信息
                          </div>
                        )}
                      </CardBody>
                    </Card>
                  )}

                  {isEdit && previewType === 2 && (
                    <Card className="border border-default-200">
                      <CardHeader>
                        <div className="font-semibold">隧道监听端口</div>
                      </CardHeader>
                      <CardBody className="space-y-3">
                        {portPrefsLoading && (
                          <div className="flex items-center gap-2 text-xs text-default-500">
                            <Spinner size="sm" />
                            <span>正在读取已有端口配置…</span>
                          </div>
                        )}
                        {previewOutNodeId ? (
                          <Input
                            description={(() => {
                              const range = getNodePortRange(
                                previewOutNodeId,
                              );

                              return range.label
                                ? `允许范围: ${range.label}，留空自动分配`
                                : "留空自动分配";
                            })()}
                            errorMessage={errors.outPort}
                            isInvalid={!!errors.outPort}
                            label={`出口端口（${nodeNameMap[previewOutNodeId] || `#${previewOutNodeId}`})`}
                            placeholder="留空自动分配"
                            type="number"
                            value={outPort?.toString() || ""}
                            variant="bordered"
                            onChange={(e) => {
                              const next = e.target.value
                                ? parseInt(e.target.value)
                                : null;

                              setOutPortTouched(true);
                              setOutPort(next);
                            }}
                          />
                        ) : null}
                        {previewPath.map((nid, idx) => (
                          <Input
                            key={`${nid}-${idx}`}
                            description={(() => {
                              const range = getNodePortRange(nid);

                              return range.label
                                ? `允许范围: ${range.label}，留空自动分配`
                                : "留空自动分配";
                            })()}
                            errorMessage={errors[`midPort_${idx}`]}
                            isInvalid={!!errors[`midPort_${idx}`]}
                            label={`中继${idx + 1}端口（${nodeNameMap[nid] || `#${nid}`})`}
                            placeholder="留空自动分配"
                            type="number"
                            value={midPorts[idx]?.toString() || ""}
                            variant="bordered"
                            onChange={(e) => {
                              const next = e.target.value
                                ? parseInt(e.target.value)
                                : null;

                              setMidPorts((prev) => ({
                                ...prev,
                                [idx]: next,
                              }));
                              setMidPortsTouched((prev) => {
                                const nextSet = new Set(prev);
                                nextSet.add(idx);
                                return nextSet;
                              });
                            }}
                          />
                        ))}
                        <div className="text-2xs text-default-400">
                          留空表示自动分配可用端口
                        </div>
                      </CardBody>
                    </Card>
                  )}

                  {getAddressCount(form.remoteAddr) > 1 && (
                    <Select
                      description="多个目标地址的负载均衡策略"
                      label="负载策略"
                      placeholder="请选择负载均衡策略"
                      selectedKeys={[form.strategy]}
                      variant="bordered"
                      onSelectionChange={(keys) => {
                        const selectedKey = Array.from(keys)[0] as string;

                        setForm((prev) => ({ ...prev, strategy: selectedKey }));
                      }}
                    >
                      <SelectItem key="fifo">主备模式 - 自上而下</SelectItem>
                      <SelectItem key="round">轮询模式 - 依次轮换</SelectItem>
                      <SelectItem key="rand">随机模式 - 随机选择</SelectItem>
                      <SelectItem key="hash">哈希模式 - IP哈希</SelectItem>
                    </Select>
                  )}
                </div>
              </ModalBody>
              <ModalFooter>
                <Button variant="light" onPress={onClose}>
                  取消
                </Button>
                <Button
                  color="primary"
                  isLoading={submitLoading}
                  onPress={handleSubmit}
                >
                  {isEdit ? "保存修改" : "创建转发"}
                </Button>
              </ModalFooter>
            </>
          )}
        </ModalContent>
      </Modal>
    );
  },
);

interface DiagnosisResult {
  forwardName: string;
  timestamp: number;
  results: Array<{
    success: boolean;
    description: string;
    nodeName: string;
    nodeId: string;
    targetIp: string;
    targetPort?: number;
    message?: string;
    averageTime?: number;
    packetLoss?: number;
    reqId?: string;
    // iperf3 bandwidth Mbps, if present
    bandwidthMbps?: number;
  }>;
}

// 添加分组接口
interface UserGroup {
  userId: number | null;
  userName: string;
  tunnelGroups: TunnelGroup[];
}

interface TunnelGroup {
  tunnelId: number;
  tunnelName: string;
  forwards: Forward[];
}

export default function ForwardPage() {
  const [loading, setLoading] = useState(true);
  const [forwards, setForwards] = useState<Forward[]>([]);
  const [tunnels, setTunnels] = useState<Tunnel[]>([]);

  // 检测是否为移动端
const [isMobile, setIsMobile] = useState(false);

  useEffect(() => {
    const checkMobile = () => {
      setIsMobile(window.innerWidth < 768);
    };

    checkMobile();
    window.addEventListener("resize", checkMobile);

    return () => window.removeEventListener("resize", checkMobile);
  }, []);

  // 显示模式状态 - 从localStorage读取，默认为平铺显示
  const [viewMode, setViewMode] = useState<"grouped" | "direct">(() => {
    try {
      const savedMode = localStorage.getItem("forward-view-mode");

      return (savedMode as "grouped" | "direct") || "direct";
    } catch {
      return "direct";
    }
  });
  const [useWindowing, setUseWindowing] = useState(() => {
    try {
      const saved = localStorage.getItem("forward-windowing");

      return saved !== "0";
    } catch {
      return true;
    }
  });

  useEffect(() => {
    try {
      localStorage.setItem("forward-windowing", useWindowing ? "1" : "0");
    } catch {}
  }, [useWindowing]);

  // 拖拽排序相关状态
  const [forwardOrder, setForwardOrder] = useState<number[]>([]);

  // 模态框状态
  const [modalOpen, setModalOpen] = useState(false);
  const [editForward, setEditForward] = useState<Forward | null>(null);
  const [deleteModalOpen, setDeleteModalOpen] = useState(false);
  const [addressModalOpen, setAddressModalOpen] = useState(false);
  const [diagnosisModalOpen, setDiagnosisModalOpen] = useState(false);
  const [deleteLoading, setDeleteLoading] = useState(false);
  const [diagnosisLoading, setDiagnosisLoading] = useState(false);
  const [forwardToDelete, setForwardToDelete] = useState<Forward | null>(null);
  const [currentDiagnosisForward, setCurrentDiagnosisForward] =
    useState<Forward | null>(null);
  const [diagnosisResult, setDiagnosisResult] =
    useState<DiagnosisResult | null>(null);
  // 配置详情
  const [cfgDetailOpen, setCfgDetailOpen] = useState(false);
  const [cfgDetailLoading, setCfgDetailLoading] = useState(false);
  const [cfgDetail, setCfgDetail] = useState<any | null>(null);
  const [addressModalTitle, setAddressModalTitle] = useState("");
  const [addressList, setAddressList] = useState<AddressItem[]>([]);

  // 导出相关状态
  const [exportModalOpen, setExportModalOpen] = useState(false);
  const [exportData, setExportData] = useState("");
  const [exportLoading, setExportLoading] = useState(false);
  const [selectedTunnelForExport, setSelectedTunnelForExport] = useState<
    number | null
  >(null);

  // 导入相关状态
  const [importModalOpen, setImportModalOpen] = useState(false);
  const [importData, setImportData] = useState("");
  const [importLoading, setImportLoading] = useState(false);
  const [selectedTunnelForImport, setSelectedTunnelForImport] = useState<
    number | null
  >(null);
  const [importResults, setImportResults] = useState<
    Array<{
      line: string;
      success: boolean;
      message: string;
      forwardName?: string;
    }>
  >([]);
  const [opsOpen, setOpsOpen] = useState(false);
  const [opReqId, setOpReqId] = useState<string>("");
  const [restartingNodeId, setRestartingNodeId] = useState<number | null>(null);
  const pageVisible = usePageVisibility();

  const tunnelOptions = useMemo(
    () =>
      tunnels.map((tunnel) => (
        <SelectItem key={tunnel.id} textValue={tunnel.name}>
          {tunnel.name}
        </SelectItem>
      )),
    [tunnels],
  );

  const [previewTunnelMap, setPreviewTunnelMap] = useState<Record<number, any>>(
    {},
  );
  // 节点列表缓存（进入页面时获取一次，避免重复调用）
  const [nodesCache, setNodesCache] = useState<any[]>([]);
  // 出口接口IP缓存与并发锁（页面级，跨弹窗渲染保持）
  const ifaceCacheRef = useRef<Map<number, string[]>>(new Map());
  const ifaceInflightRef = useRef<Set<number>>(new Set());
  // 已校验配置的转发ID集合（进入页面批量校验一次，新增/编辑单独校验）
  const checkedForwardIdsRef = useRef<Set<number>>(new Set());

  const fetchStatusForIds = useCallback(
    async (ids: number[]) => {
      if (!ids.length) return;
      try {
        const sres: any = await getForwardStatus(ids);

        if (sres && sres.code === 0 && Array.isArray(sres.data?.list)) {
          const okMap = new Map<number, boolean>();

          (sres.data.list as any[]).forEach((it) => {
            if (typeof it?.forwardId === "number") {
              okMap.set(it.forwardId, !!it.ok);
            }
          });
          setForwards((prev) =>
            prev.map((f) => ({
              ...f,
              configOk: okMap.has(f.id) ? !!okMap.get(f.id) : f.configOk,
            })),
          );
          const next = new Set(checkedForwardIdsRef.current);
          ids.forEach((id) => next.add(id));
          checkedForwardIdsRef.current = next;
        }
      } catch {
        // ignore status fetch errors
      }
    },
    [setForwards],
  );

  useEffect(() => {
    loadData();
  }, []);

  // 进入页面获取一次节点列表用于名称映射与入口API状态判断
  useEffect(() => {
    (async () => {
      try {
        const nl: any = await getNodeList();

        if (nl && nl.code === 0 && Array.isArray(nl.data))
          setNodesCache(nl.data);
      } catch {}
    })();
  }, []);

  // 从网站配置读取轮询间隔（默认 3s）
  const [pollMs, setPollMs] = useState<number>(3000);

  useEffect(() => {
    (async () => {
      try {
        // 支持秒为单位的配置项：poll_interval_sec
        const v = await getCachedConfig("poll_interval_sec");
        const n = Math.max(1, parseInt(String(v || "3"), 10));

        setPollMs(n * 1000);
      } catch {}
    })();
  }, []);

  // 轮询刷新每条转发的进/出流量（当任一弹窗打开时暂停，避免编辑时界面抖动）
  const anyModalOpen =
    modalOpen ||
    deleteModalOpen ||
    diagnosisModalOpen ||
    cfgDetailOpen ||
    addressModalOpen ||
    exportModalOpen ||
    importModalOpen;

  useEffect(() => {
    let timer: any;
    const tick = async () => {
      if (anyModalOpen || !pageVisible) return; // 暂停轮询，避免干扰弹窗中的交互
      try {
        const res: any = await getForwardList();

        if (res && res.code === 0 && Array.isArray(res.data)) {
          const flowMap = new Map<
            number,
            { inFlow: number; outFlow: number }
          >();

          (res.data as any[]).forEach((it: any) => {
            if (typeof it?.id === "number") {
              flowMap.set(it.id, {
                inFlow: Number(it.inFlow || 0),
                outFlow: Number(it.outFlow || 0),
              });
            }
          });
          setForwards((prev) =>
            prev.map((f) => {
              const m = flowMap.get(f.id);

              if (!m) return f;
              if (m.inFlow === f.inFlow && m.outFlow === f.outFlow) return f;

              return { ...f, inFlow: m.inFlow, outFlow: m.outFlow };
            }),
          );
          // 配置校验：仅校验未校验过的ID（进入页面一次；新增/编辑单独触发）
          const ids = (res.data as any[])
            .map((it: any) => Number(it.id))
            .filter((x: number) => x > 0);
          // 清理已校验但已删除的ID
          const currentIds = new Set(ids);
          const cleaned = new Set<number>();
          checkedForwardIdsRef.current.forEach((id) => {
            if (currentIds.has(id)) cleaned.add(id);
          });
          checkedForwardIdsRef.current = cleaned;
          const unchecked = ids.filter(
            (id: number) => !checkedForwardIdsRef.current.has(id),
          );
          void fetchStatusForIds(unchecked);
        }
      } catch (_) {
        // 忽略错误，下一次轮询继续
      }
    };

    // 立即跑一次，随后按配置轮询
    tick();
    timer = setInterval(tick, pollMs);

    return () => {
      if (timer) clearInterval(timer);
    };
  }, [pollMs, anyModalOpen, pageVisible, fetchStatusForIds]);

  // 切换显示模式并保存到localStorage
  const handleViewModeChange = () => {
    const newMode = viewMode === "grouped" ? "direct" : "grouped";

    setViewMode(newMode);
    try {
      localStorage.setItem("forward-view-mode", newMode);

      // 切换到直接显示模式时，初始化拖拽排序顺序
      if (newMode === "direct") {
        // 在平铺模式下，只对当前用户的转发进行排序
        const currentUserId = JwtUtil.getUserIdFromToken();
        let userForwards = forwards;

        if (currentUserId !== null) {
          userForwards = forwards.filter(
            (f: Forward) => f.userId === currentUserId,
          );
        }

        // 检查数据库中是否有排序信息
        const hasDbOrdering = userForwards.some(
          (f: Forward) => f.inx !== undefined && f.inx !== 0,
        );

        if (hasDbOrdering) {
          // 使用数据库中的排序信息
          const dbOrder = userForwards
            .sort((a: Forward, b: Forward) => (a.inx ?? 0) - (b.inx ?? 0))
            .map((f: Forward) => f.id);

          setForwardOrder(dbOrder);

          // 同步到localStorage
          try {
            localStorage.setItem("forward-order", JSON.stringify(dbOrder));
          } catch (error) {
            console.warn("无法保存排序到localStorage:", error);
          }
        } else {
          // 使用本地存储的顺序
          const savedOrder = localStorage.getItem("forward-order");

          if (savedOrder) {
            try {
              const orderIds = JSON.parse(savedOrder);
              const validOrder = orderIds.filter((id: number) =>
                userForwards.some((f: Forward) => f.id === id),
              );

              userForwards.forEach((forward: Forward) => {
                if (!validOrder.includes(forward.id)) {
                  validOrder.push(forward.id);
                }
              });
              setForwardOrder(validOrder);
            } catch {
              setForwardOrder(userForwards.map((f: Forward) => f.id));
            }
          } else {
            setForwardOrder(userForwards.map((f: Forward) => f.id));
          }
        }
      }
    } catch (error) {
      console.warn("无法保存显示模式到localStorage:", error);
    }
  };

  // 加载所有数据
  const loadData = async (lod = true) => {
    setLoading(lod);
    try {
      const [forwardsRes, tunnelsRes, allTunnelsRes] = await Promise.all([
        getForwardList(),
        userTunnel(),
        getTunnelList().catch(() => ({ code: -1 })),
      ]);

      if (forwardsRes.code === 0) {
        const forwardsData =
          forwardsRes.data?.map((forward: any) => ({
            ...forward,
            serviceRunning: forward.status === 1,
          })) || [];

        setForwards((prev) => {
          const okMap = new Map<number, boolean | undefined>();
          prev.forEach((p) => okMap.set(p.id, p.configOk));
          return forwardsData.map((f: any) => ({
            ...f,
            configOk: okMap.has(f.id) ? okMap.get(f.id) : f.configOk,
          }));
        });
        // 清理已校验集合中不存在的ID
        const currentIds = new Set(forwardsData.map((f: Forward) => f.id));
        const cleaned = new Set<number>();
        checkedForwardIdsRef.current.forEach((id) => {
          if (currentIds.has(id)) cleaned.add(id);
        });
        checkedForwardIdsRef.current = cleaned;
        // 首次/新增时仅校验未校验过的转发
        const unchecked = forwardsData
          .map((f: Forward) => f.id)
          .filter((id: number) => !checkedForwardIdsRef.current.has(id));
        void fetchStatusForIds(unchecked);

        // 初始化拖拽排序顺序
        if (viewMode === "direct") {
          // 在平铺模式下，只对当前用户的转发进行排序
          const currentUserId = JwtUtil.getUserIdFromToken();
          let userForwards = forwardsData;

          if (currentUserId !== null) {
            userForwards = forwardsData.filter(
              (f: Forward) => f.userId === currentUserId,
            );
          }

          // 检查数据库中是否有排序信息
          const hasDbOrdering = userForwards.some(
            (f: Forward) => f.inx !== undefined && f.inx !== 0,
          );

          if (hasDbOrdering) {
            // 使用数据库中的排序信息
            const dbOrder = userForwards
              .sort((a: Forward, b: Forward) => (a.inx ?? 0) - (b.inx ?? 0))
              .map((f: Forward) => f.id);

            setForwardOrder(dbOrder);

            // 同步到localStorage
            try {
              localStorage.setItem("forward-order", JSON.stringify(dbOrder));
            } catch (error) {
              console.warn("无法保存排序到localStorage:", error);
            }
          } else {
            // 使用本地存储的顺序
            const savedOrder = localStorage.getItem("forward-order");

            if (savedOrder) {
              try {
                const orderIds = JSON.parse(savedOrder);
                // 验证保存的顺序是否仍然有效（只包含当前用户的转发）
                const validOrder = orderIds.filter((id: number) =>
                  userForwards.some((f: Forward) => f.id === id),
                );

                // 添加新的转发ID（如果存在）
                userForwards.forEach((forward: Forward) => {
                  if (!validOrder.includes(forward.id)) {
                    validOrder.push(forward.id);
                  }
                });
                setForwardOrder(validOrder);
              } catch {
                setForwardOrder(userForwards.map((f: Forward) => f.id));
              }
            } else {
              setForwardOrder(userForwards.map((f: Forward) => f.id));
            }
          }
        }
      } else {
        toast.error(forwardsRes.msg || "获取转发列表失败");
      }

      if (tunnelsRes.code === 0) {
        setTunnels(tunnelsRes.data || []);
      } else {
        console.warn("获取隧道列表失败:", tunnelsRes.msg);
      }
      // 预览用的完整隧道信息（包含 type/inNodeId/outNodeId）
      {
        const resp: any = allTunnelsRes as any;
        const arr: any[] =
          resp && resp.code === 0 && Array.isArray(resp.data)
            ? (resp.data as any[])
            : [];

        if (arr.length > 0) {
          const map: Record<number, any> = {};

          arr.forEach((t) => {
            if (t?.id) map[Number(t.id)] = t;
          });
          setPreviewTunnelMap(map);
        } else {
          setPreviewTunnelMap({});
        }
      }
    } catch (error) {
      console.error("加载数据失败:", error);
      toast.error("加载数据失败");
    } finally {
      setLoading(false);
    }
  };

  const handleAdd = () => {
    setEditForward(null);
    setModalOpen(true);
  };

  const handleEdit = useCallback((forward: Forward) => {
    setEditForward(forward);
    setModalOpen(true);
  }, []);

  const handleDelete = (forward: Forward) => {
    setForwardToDelete(forward);
    setDeleteModalOpen(true);
  };

  const confirmDelete = async () => {
    if (!forwardToDelete) return;

    setDeleteLoading(true);
    try {
      const res = await deleteForward(forwardToDelete.id);

      if (res.code === 0) {
        toast.success("删除成功");
        setDeleteModalOpen(false);
        loadData();
      } else {
        const confirmed = window.confirm(
          `常规删除失败：${res.msg || "删除失败"}\n\n是否需要强制删除？\n\n⚠️ 注意：强制删除不会去验证节点端是否已经删除对应的转发服务。`,
        );

        if (confirmed) {
          const forceRes = await forceDeleteForward(forwardToDelete.id);

          if (forceRes.code === 0) {
            toast.success("强制删除成功");
            setDeleteModalOpen(false);
            loadData();
          } else {
            toast.error(forceRes.msg || "强制删除失败");
          }
        }
      }
    } catch (error) {
      console.error("删除失败:", error);
      toast.error("删除失败");
    } finally {
      setDeleteLoading(false);
    }
  };

  const handleSaved = useCallback(
    ({ isEdit, forwardId }: { isEdit: boolean; forwardId?: number }) => {
      loadData().then(() => {
        if (isEdit && forwardId) {
          checkedForwardIdsRef.current.delete(forwardId);
          void fetchStatusForIds([forwardId]);
        }
      });
    },
    [fetchStatusForIds, loadData],
  );

  const handleOpsLogOpen = useCallback((requestId: string) => {
    setOpReqId(requestId);
    setOpsOpen(true);
  }, []);

  // 按用户和隧道分组转发数据
  const groupForwardsByUserAndTunnel = (): UserGroup[] => {
    const userMap = new Map<string, UserGroup>();

    sortedForwards.forEach((forward) => {
      const userKey = forward.userId ? forward.userId.toString() : "unknown";
      const userName = forward.userName || "未知用户";

      if (!userMap.has(userKey)) {
        userMap.set(userKey, {
          userId: forward.userId || null,
          userName,
          tunnelGroups: [],
        });
      }

      const userGroup = userMap.get(userKey)!;
      let tunnelGroup = userGroup.tunnelGroups.find(
        (tg) => tg.tunnelId === forward.tunnelId,
      );

      if (!tunnelGroup) {
        tunnelGroup = {
          tunnelId: forward.tunnelId,
          tunnelName: forward.tunnelName,
          forwards: [],
        };
        userGroup.tunnelGroups.push(tunnelGroup);
      }

      tunnelGroup.forwards.push(forward);
    });

    // 排序：先按用户名，再按隧道名
    const result = Array.from(userMap.values());

    result.sort((a, b) => a.userName.localeCompare(b.userName));
    result.forEach((userGroup) => {
      userGroup.tunnelGroups.sort((a, b) =>
        a.tunnelName.localeCompare(b.tunnelName),
      );
    });

    return result;
  };

  // 诊断转发
  const handleDiagnose = async (forward: Forward) => {
    setCurrentDiagnosisForward(forward);
    setDiagnosisModalOpen(true);
    setDiagnosisLoading(true);
    setDiagnosisResult(null);

    // 流式增量：优先逐跳路径，再到远端（与隧道诊断保持一致）
    setDiagnosisResult({
      forwardName: forward.name,
      timestamp: Date.now(),
      results: [],
    });
    const append = (item: any) => {
      setDiagnosisResult((prev) =>
        prev ? { ...prev, results: [...prev.results, item] } : prev,
      );
    };

    try {
      // 1) 逐跳路径（端口转发：入口->中间，最后到远端；隧道转发：入口->中间->出口，最后出口->远端）
      const rPath = await diagnoseForwardStep(forward.id, "path");

      if (rPath.code === 0) {
        const arr = Array.isArray(rPath.data?.results)
          ? rPath.data.results
          : rPath.data
            ? [rPath.data]
            : [];

        arr.forEach((it: any) => append(it));
      } else {
        append({
          success: false,
          description: "路径连通性",
          nodeName: "-",
          nodeId: "-",
          targetIp: "-",
          message: rPath.msg || "失败",
        });
      }

      // 2) 节点服务清单（逐跳）：从 /forward/diagnose 抽取“节点服务清单”一项，用于展示各节点的服务配置与状态
      try {
        const rFull = await diagnoseForward(forward.id);

        if (rFull && rFull.code === 0) {
          const list = Array.isArray(rFull.data?.results)
            ? rFull.data.results
            : Array.isArray(rFull.data)
              ? rFull.data
              : [];
          const hopItem = (list as any[]).find(
            (it: any) =>
              it &&
              (it.description === "节点服务清单" || Array.isArray(it.hops)),
          );

          if (hopItem) append(hopItem);
        }
      } catch {}
      // 3) iperf3 反向带宽（仅隧道转发）
      //const r3 = await diagnoseForwardStep(forward.id, 'iperf3');
      // if (r3.code === 0) append(r3.data); else append({ success: false, description: 'iperf3 反向带宽测试', nodeName: '-', nodeId: '-', targetIp: '-', message: r3.msg || '未支持或失败' });
    } catch (e) {
      toast.error("诊断失败");
    } finally {
      setDiagnosisLoading(false);
    }
  };

  const handleRestartGost = async (nodeId: number) => {
    if (!nodeId) return;
    try {
      setRestartingNodeId(nodeId);
      const api = await import("@/api");
      const res: any = await api.restartGost(nodeId);

      if (res.code === 0) {
        const ok = !!(res.data && res.data.success);
        const msg =
          res.data && res.data.message
            ? res.data.message
            : ok
              ? "重启成功"
              : "重启已下发";

        if (ok) toast.success(msg);
        else toast.success(msg);
        // 若重启成功或已下发，针对当前节点：
        // 1) 重新刷新该节点的服务清单（监听状态等）
        if (currentDiagnosisForward) {
          try {
            const rFull: any = await api.diagnoseForward(
              currentDiagnosisForward.id,
            );

            if (rFull && rFull.code === 0) {
              const list = Array.isArray(rFull.data?.results)
                ? rFull.data.results
                : Array.isArray(rFull.data)
                  ? rFull.data
                  : [];
              const hopItem = (list as any[]).find(
                (it: any) =>
                  it &&
                  (it.description === "节点服务清单" || Array.isArray(it.hops)),
              );

              if (hopItem && Array.isArray(hopItem.hops)) {
                setDiagnosisResult((prev) => {
                  if (!prev) return prev;
                  const newResults = prev.results.map((it: any) => {
                    if (Array.isArray(it.hops)) {
                      const newHops = it.hops.map((h: any) =>
                        h && h.nodeId === nodeId
                          ? {
                              ...h,
                              services:
                                hopItem.hops.find(
                                  (nh: any) => nh.nodeId === nodeId,
                                )?.services || h.services,
                            }
                          : h,
                      );

                      return { ...it, hops: newHops };
                    }

                    return it;
                  });

                  return { ...prev, results: newResults } as any;
                });
              }
            }
          } catch {}
          // 2) 仅重新运行“逐跳连通性 (ICMP)”等与该节点相关的路径诊断，并合并该节点对应项
          try {
            const rPath: any = await api.diagnoseForwardStep(
              currentDiagnosisForward.id,
              "path",
            );

            if (rPath && rPath.code === 0) {
              const items: any[] = Array.isArray(rPath.data?.results)
                ? rPath.data.results
                : [];
              const replaceForNode = items.filter(
                (x) => x && Number(x.nodeId) === Number(nodeId),
              );

              if (replaceForNode.length > 0) {
                setDiagnosisResult((prev) => {
                  if (!prev) return prev;
                  const newResults = prev.results.map((it: any) => {
                    // 仅替换“逐跳连通性 (ICMP)”或同类分项中的该节点记录
                    if (
                      typeof it?.description === "string" &&
                      it.description.indexOf("逐跳连通性") >= 0
                    ) {
                      // 在 path 结果中找同 nodeId 的新项
                      const fresh = replaceForNode.find(
                        (n) => Number(n.nodeId) === Number(nodeId),
                      );

                      return fresh ? fresh : it;
                    }

                    return it;
                  });

                  return { ...prev, results: newResults } as any;
                });
              }
            }
          } catch {}
          // 3) 轮询几次（短间隔）以等待 gost 完全启动后端口监听再更新（最多3次）
          const sleep = (ms: number) =>
            new Promise((res) => setTimeout(res, ms));

          for (let i = 0; i < 3; i++) {
            await sleep(1000);
            try {
              const rFull2: any = await api.diagnoseForward(
                currentDiagnosisForward.id,
              );

              if (rFull2 && rFull2.code === 0) {
                const list2 = Array.isArray(rFull2.data?.results)
                  ? rFull2.data.results
                  : Array.isArray(rFull2.data)
                    ? rFull2.data
                    : [];
                const hopItem2 = (list2 as any[]).find(
                  (it: any) =>
                    it &&
                    (it.description === "节点服务清单" ||
                      Array.isArray(it.hops)),
                );

                if (hopItem2 && Array.isArray(hopItem2.hops)) {
                  const targetHop = (hopItem2.hops as any[]).find(
                    (h: any) => h && Number(h.nodeId) === Number(nodeId),
                  );

                  if (targetHop) {
                    setDiagnosisResult((prev) => {
                      if (!prev) return prev;
                      const newResults = prev.results.map((it: any) => {
                        if (Array.isArray(it.hops)) {
                          const newHops = it.hops.map((h: any) =>
                            h && h.nodeId === nodeId
                              ? { ...h, services: targetHop.services }
                              : h,
                          );

                          return { ...it, hops: newHops };
                        }

                        return it;
                      });

                      return { ...prev, results: newResults } as any;
                    });
                    // 若任一服务已监听，提前结束轮询
                    if (
                      Array.isArray(targetHop.services) &&
                      targetHop.services.some((s: any) => !!s?.listening)
                    )
                      break;
                  }
                }
              }
            } catch {}
          }
        }
      } else {
        toast.error(res.msg || "重启失败");
      }
    } catch (e: any) {
      toast.error("重启失败");
    } finally {
      setRestartingNodeId(null);
    }
  };

  // 获取连接质量
  const getQualityDisplay = (averageTime?: number, packetLoss?: number) => {
    if (averageTime === undefined || packetLoss === undefined) return null;

    if (averageTime < 30 && packetLoss === 0)
      return { text: "🚀 优秀", color: "success" };
    if (averageTime < 50 && packetLoss === 0)
      return { text: "✨ 很好", color: "success" };
    if (averageTime < 100 && packetLoss < 1)
      return { text: "👍 良好", color: "primary" };
    if (averageTime < 150 && packetLoss < 2)
      return { text: "😐 一般", color: "warning" };
    if (averageTime < 200 && packetLoss < 5)
      return { text: "😟 较差", color: "warning" };

    return { text: "😵 很差", color: "danger" };
  };

  // 格式化流量
  const formatFlow = (value: number): string => {
    if (value === 0) return "0 B";
    if (value < 1024) return value + " B";
    if (value < 1024 * 1024) return (value / 1024).toFixed(2) + " KB";
    if (value < 1024 * 1024 * 1024)
      return (value / (1024 * 1024)).toFixed(2) + " MB";

    return (value / (1024 * 1024 * 1024)).toFixed(2) + " GB";
  };

  // 复制到剪贴板
  const copyToClipboard = useCallback(
    async (text: string, label: string = "内容") => {
      try {
        await navigator.clipboard.writeText(text);
        toast.success(`已复制${label}`);
      } catch (error) {
        toast.error("复制失败");
      }
    },
    [],
  );

  // 显示地址列表弹窗
  const showAddressModal = useCallback(
    (addressString: string, port: number | null, title: string) => {
      if (!addressString) return;

      let addresses: string[];

      if (port !== null) {
        const ips = addressString
          .split(",")
          .map((ip) => ip.trim())
          .filter((ip) => ip);

        if (ips.length <= 1) {
          copyToClipboard(formatInAddress(addressString, port), title);

          return;
        }
        addresses = ips.map((ip) => {
          if (ip.includes(":") && !ip.startsWith("[")) {
            return `[${ip}]:${port}`;
          } else {
            return `${ip}:${port}`;
          }
        });
      } else {
        addresses = addressString
          .split(",")
          .map((addr) => addr.trim())
          .filter((addr) => addr);
        if (addresses.length <= 1) {
          copyToClipboard(addressString, title);

          return;
        }
      }

      setAddressList(
        addresses.map((address, index) => ({
          id: index,
          address,
          copying: false,
        })),
      );
      setAddressModalTitle(`${title} (${addresses.length}个)`);
      setAddressModalOpen(true);
    },
    [copyToClipboard],
  );

  // 复制地址
  const copyAddress = async (addressItem: AddressItem) => {
    try {
      setAddressList((prev) =>
        prev.map((item) =>
          item.id === addressItem.id ? { ...item, copying: true } : item,
        ),
      );
      await copyToClipboard(addressItem.address, "地址");
    } catch (error) {
      toast.error("复制失败");
    } finally {
      setAddressList((prev) =>
        prev.map((item) =>
          item.id === addressItem.id ? { ...item, copying: false } : item,
        ),
      );
    }
  };

  // 复制所有地址
  const copyAllAddresses = async () => {
    if (addressList.length === 0) return;
    const allAddresses = addressList.map((item) => item.address).join("\n");

    await copyToClipboard(allAddresses, "所有地址");
  };

  // 导出转发数据
  const handleExport = () => {
    setSelectedTunnelForExport(null);
    setExportData("");
    setExportModalOpen(true);
  };

  // 执行导出
  const executeExport = () => {
    if (!selectedTunnelForExport) {
      toast.error("请选择要导出的隧道");

      return;
    }

    setExportLoading(true);

    try {
      // 根据当前显示模式获取要导出的转发列表
      let forwardsToExport: Forward[] = [];

      if (viewMode === "grouped") {
        // 分组模式下，获取指定隧道的转发
        const userGroups = userGroupsMemo;

        forwardsToExport = userGroups.flatMap((userGroup) =>
          userGroup.tunnelGroups
            .filter(
              (tunnelGroup) => tunnelGroup.tunnelId === selectedTunnelForExport,
            )
            .flatMap((tunnelGroup) => tunnelGroup.forwards),
        );
      } else {
        // 直接显示模式下，过滤指定隧道的转发
        forwardsToExport = sortedForwards.filter(
          (forward) => forward.tunnelId === selectedTunnelForExport,
        );
      }

      if (forwardsToExport.length === 0) {
        toast.error("所选隧道没有转发数据");
        setExportLoading(false);

        return;
      }

      // 格式化导出数据：remoteAddr|name|inPort|interface（interface 可为空）
      const exportLines = forwardsToExport.map((forward) => {
        const iface = forward.interfaceName || "";

        return `${forward.remoteAddr}|${forward.name}|${forward.inPort || ""}|${iface}`;
      });

      const exportText = exportLines.join("\n");

      setExportData(exportText);
    } catch (error) {
      console.error("导出失败:", error);
      toast.error("导出失败");
    } finally {
      setExportLoading(false);
    }
  };

  // 复制导出数据
  const copyExportData = async () => {
    await copyToClipboard(exportData, "转发数据");
  };

  // 导入转发数据
  const handleImport = () => {
    setImportData("");
    setImportResults([]);
    setSelectedTunnelForImport(null);
    setImportModalOpen(true);
  };

  // 执行导入
  const executeImport = async () => {
    if (!importData.trim()) {
      toast.error("请输入要导入的数据");

      return;
    }

    if (!selectedTunnelForImport) {
      toast.error("请选择要导入的隧道");

      return;
    }

    setImportLoading(true);
    setImportResults([]); // 清空之前的结果

    try {
      const lines = importData
        .trim()
        .split("\n")
        .filter((line) => line.trim());

      for (let i = 0; i < lines.length; i++) {
        const line = lines[i].trim();
        const parts = line.split("|");

        if (parts.length < 2) {
          setImportResults((prev) => [
            {
              line,
              success: false,
              message: "格式错误：需要至少包含目标地址和转发名称",
            },
            ...prev,
          ]);
          continue;
        }

        const [remoteAddr, name, inPort, iface] = parts;

        if (!remoteAddr.trim() || !name.trim()) {
          setImportResults((prev) => [
            {
              line,
              success: false,
              message: "目标地址和转发名称不能为空",
            },
            ...prev,
          ]);
          continue;
        }

        // 验证远程地址格式 - 支持单个地址或多个地址用逗号分隔
        const addresses = remoteAddr.trim().split(",");
        const addressPattern = /^[^:]+:\d+$/;
        const isValidFormat = addresses.every((addr) =>
          addressPattern.test(addr.trim()),
        );

        if (!isValidFormat) {
          setImportResults((prev) => [
            {
              line,
              success: false,
              message:
                "目标地址格式错误，应为 地址:端口 格式，多个地址用逗号分隔",
            },
            ...prev,
          ]);
          continue;
        }

        try {
          // 处理入口端口
          let portNumber: number | null = null;

          if (inPort && inPort.trim()) {
            const port = parseInt(inPort.trim());

            if (isNaN(port) || port < 1 || port > 65535) {
              setImportResults((prev) => [
                {
                  line,
                  success: false,
                  message: "入口端口格式错误，应为1-65535之间的数字",
                },
                ...prev,
              ]);
              continue;
            }
            portNumber = port;
          }

          // 调用创建转发接口
          const response = await createForward({
            name: name.trim(),
            tunnelId: selectedTunnelForImport, // 使用用户选择的隧道
            inPort: portNumber, // 使用指定端口或自动分配
            remoteAddr: remoteAddr.trim(),
            strategy: "fifo",
            interfaceName: iface && iface.trim() ? iface.trim() : undefined,
          });

          if (response.code === 0) {
            setImportResults((prev) => [
              {
                line,
                success: true,
                message: "创建成功",
                forwardName: name.trim(),
              },
              ...prev,
            ]);
          } else {
            setImportResults((prev) => [
              {
                line,
                success: false,
                message: response.msg || "创建失败",
              },
              ...prev,
            ]);
          }
        } catch (error) {
          setImportResults((prev) => [
            {
              line,
              success: false,
              message: "网络错误，创建失败",
            },
            ...prev,
          ]);
        }
      }

      toast.success(`导入执行完成`);

      // 导入完成后刷新转发列表
      await loadData(false);
    } catch (error) {
      console.error("导入失败:", error);
      toast.error("导入过程中发生错误");
    } finally {
      setImportLoading(false);
    }
  };

  // 处理拖拽结束
  const handleDragEnd = async (event: DragEndEvent) => {
    const { active, over } = event;

    if (!active || !over || active.id === over.id) return;

    // 确保 forwardOrder 存在且有效
    if (!forwardOrder || forwardOrder.length === 0) return;

    const activeId = Number(active.id);
    const overId = Number(over.id);

    // 检查 ID 是否有效
    if (isNaN(activeId) || isNaN(overId)) return;

    const oldIndex = forwardOrder.indexOf(activeId);
    const newIndex = forwardOrder.indexOf(overId);

    if (oldIndex !== -1 && newIndex !== -1 && oldIndex !== newIndex) {
      const newOrder = arrayMove(forwardOrder, oldIndex, newIndex);

      setForwardOrder(newOrder);

      // 保存到localStorage
      try {
        localStorage.setItem("forward-order", JSON.stringify(newOrder));
      } catch (error) {
        console.warn("无法保存排序到localStorage:", error);
      }

      // 持久化到数据库
      try {
        const forwardsToUpdate = newOrder.map((id, index) => ({
          id,
          inx: index,
        }));

        const response = await updateForwardOrder({
          forwards: forwardsToUpdate,
        });

        if (response.code === 0) {
          // 更新本地数据中的 inx 字段
          setForwards((prev) =>
            prev.map((forward) => {
              const updatedForward = forwardsToUpdate.find(
                (f) => f.id === forward.id,
              );

              if (updatedForward) {
                return { ...forward, inx: updatedForward.inx };
              }

              return forward;
            }),
          );
        } else {
          toast.error("保存排序失败：" + (response.msg || "未知错误"));
        }
      } catch (error) {
        console.error("保存排序到数据库失败:", error);
        toast.error("保存排序失败，请重试");
      }
    }
  };

  // 传感器配置 - 使用默认配置避免错误
  const sensors = useSensors(
    useSensor(PointerSensor),
    useSensor(KeyboardSensor, {
      coordinateGetter: sortableKeyboardCoordinates,
    }),
  );

  const sortedForwards = useMemo((): Forward[] => {
    if (!forwards || forwards.length === 0) {
      return [];
    }

    let filteredForwards = forwards;

    if (viewMode === "direct") {
      const currentUserId = JwtUtil.getUserIdFromToken();

      if (currentUserId !== null) {
        filteredForwards = forwards.filter(
          (forward) => forward.userId === currentUserId,
        );
      }
    }

    if (!filteredForwards || filteredForwards.length === 0) {
      return [];
    }

    const sorted = [...filteredForwards].sort((a, b) => {
      const aInx = a.inx ?? 0;
      const bInx = b.inx ?? 0;

      return aInx - bInx;
    });

    if (
      forwardOrder &&
      forwardOrder.length > 0 &&
      sorted.every((f) => f.inx === undefined || f.inx === 0)
    ) {
      const forwardMap = new Map(filteredForwards.map((f) => [f.id, f]));
      const localSortedForwards: Forward[] = [];

      forwardOrder.forEach((id) => {
        const forward = forwardMap.get(id);

        if (forward) {
          localSortedForwards.push(forward);
        }
      });

      filteredForwards.forEach((forward) => {
        if (!forwardOrder.includes(forward.id)) {
          localSortedForwards.push(forward);
        }
      });

      return localSortedForwards;
    }

    return sorted;
  }, [forwards, forwardOrder, viewMode]);

  const sortableIds = useMemo(
    () =>
      sortedForwards
        .map((f) => f.id || 0)
        .filter((id) => id > 0),
    [sortedForwards],
  );

  // 渲染转发卡片
  const renderForwardCard = useCallback(
    (forward: Forward, listeners?: any) => {
    //const statusDisplay = getStatusDisplay(forward.status);
    const strategyDisplay = getStrategyDisplay(forward.strategy);

    return (
      <Card
        key={forward.id}
        className="group list-card shadow-sm border border-divider hover:shadow-md transition-shadow duration-200"
      >
        <CardHeader className="pb-2">
          <div className="flex justify-between items-start w-full">
            <div className="flex-1 min-w-0">
              <h3 className="font-semibold text-foreground truncate text-sm">
                {forward.name}
              </h3>
              <p className="text-xs text-default-500 truncate">
                {forward.tunnelName}
              </p>
            </div>
            <div className="flex items-center gap-1.5 ml-2">
              {viewMode === "direct" && !useWindowing && (
                <div
                  className={`cursor-grab active:cursor-grabbing p-2 text-default-400 hover:text-default-600 transition-colors touch-manipulation ${
                    isMobile
                      ? "opacity-100" // 移动端始终显示
                      : "opacity-0 group-hover:opacity-100 sm:opacity-0 sm:group-hover:opacity-100"
                  }`}
                  {...listeners}
                  style={{ touchAction: "none" }}
                  title={isMobile ? "长按拖拽排序" : "拖拽排序"}
                >
                  <svg
                    className="w-4 h-4"
                    fill="currentColor"
                    viewBox="0 0 20 20"
                  >
                    <path d="M7 2a2 2 0 1 1 .001 4.001A2 2 0 0 1 7 2zm0 6a2 2 0 1 1 .001 4.001A2 2 0 0 1 7 8zm0 6a2 2 0 1 1 .001 4.001A2 2 0 0 1 7 14zm6-8a2 2 0 1 1-.001-4.001A2 2 0 0 1 13 6zm0 2a2 2 0 1 1 .001 4.001A2 2 0 0 1 13 8zm0 6a2 2 0 1 1 .001 4.001A2 2 0 0 1 13 14z" />
                  </svg>
                </div>
              )}
              <Chip
                className="text-xs"
                color={
                  forward.configOk === undefined
                    ? ("default" as any)
                    : forward.configOk
                      ? ("success" as any)
                      : ("danger" as any)
                }
                size="sm"
                title={forward.configOk ? "配置正常" : "点击查看缺失详情"}
                variant="flat"
                onClick={async () => {
                  if (forward.configOk) return;
                  setCfgDetailOpen(true);
                  setCfgDetail(null);
                  setCfgDetailLoading(true);
                  try {
                    const r: any = await getForwardStatusDetail(forward.id);

                    if (r && r.code === 0) {
                      setCfgDetail(r.data);
                      const nodes: any[] = Array.isArray(r.data?.nodes)
                        ? r.data.nodes
                        : [];
                      const cfgOk =
                        nodes.length > 0
                          ? nodes.every((n) => !!n.ok)
                          : false;
                      // 细节校验后按实际结果更新状态
                      setForwards((prev) =>
                        prev.map((f) =>
                          f.id === forward.id ? { ...f, configOk: cfgOk } : f,
                        ),
                      );
                      const next = new Set(checkedForwardIdsRef.current);
                      next.add(forward.id);
                      checkedForwardIdsRef.current = next;
                    }
                  } catch {
                  } finally {
                    setCfgDetailLoading(false);
                  }
                }}
              >
                {forward.configOk === undefined
                  ? "未知"
                  : forward.configOk
                    ? "配置正常"
                    : "配置缺失"}
              </Chip>
            </div>
          </div>
        </CardHeader>

        <CardBody className="pt-0 pb-3">
          <div className="space-y-2">
            {/* 地址信息 */}
            <div className="space-y-1">
            <div
                className={`cursor-pointer px-2 py-1 bg-default-50 dark:bg-default-100/50 rounded border border-default-200 dark:border-default-300 transition-colors duration-200 ${
                  hasMultipleAddresses(forward.inIp)
                    ? "hover:bg-default-100 dark:hover:bg-default-200/50"
                    : ""
                }`}
                title={formatInAddress(forward.inIp, forward.inPort)}
                onClick={() =>
                  showAddressModal(forward.inIp, forward.inPort, "入口端口")
                }
              >
                <div className="flex items-center justify-between">
                  <div className="flex items-center gap-1.5 min-w-0 flex-1">
                    <span className="text-xs font-medium text-default-600 flex-shrink-0">
                      入口:
                    </span>
                    <code className="text-xs font-mono text-foreground truncate min-w-0">
                      {formatInAddress(forward.inIp, forward.inPort)}
                    </code>
                  </div>
                  {hasMultipleAddresses(forward.inIp) && (
                    <svg
                      className="w-3 h-3 text-default-400 flex-shrink-0"
                      fill="none"
                      stroke="currentColor"
                      viewBox="0 0 24 24"
                    >
                      <path
                        d="M8 16H6a2 2 0 01-2-2V6a2 2 0 012-2h8a2 2 0 012 2v2m-6 12h8a2 2 0 002-2v-8a2 2 0 00-2-2h-8a2 2 0 00-2 2v8a2 2 0 002 2z"
                        strokeLinecap="round"
                        strokeLinejoin="round"
                        strokeWidth={2}
                      />
                    </svg>
                  )}
                </div>
              </div>

              <div
                className={`cursor-pointer px-2 py-1 bg-default-50 dark:bg-default-100/50 rounded border border-default-200 dark:border-default-300 transition-colors duration-200 ${
                  hasMultipleAddresses(forward.remoteAddr)
                    ? "hover:bg-default-100 dark:hover:bg-default-200/50"
                    : ""
                }`}
                title={formatRemoteAddress(forward.remoteAddr)}
                onClick={() =>
                  showAddressModal(forward.remoteAddr, null, "目标地址")
                }
              >
                <div className="flex items-center justify-between">
                  <div className="flex items-center gap-1.5 min-w-0 flex-1">
                    <span className="text-xs font-medium text-default-600 flex-shrink-0">
                      目标:
                    </span>
                    <code className="text-xs font-mono text-foreground truncate min-w-0">
                      {formatRemoteAddress(forward.remoteAddr)}
                    </code>
                  </div>
                  {hasMultipleAddresses(forward.remoteAddr) && (
                    <svg
                      className="w-3 h-3 text-default-400 flex-shrink-0"
                      fill="none"
                      stroke="currentColor"
                      viewBox="0 0 24 24"
                    >
                      <path
                        d="M8 16H6a2 2 0 01-2-2V6a2 2 0 012-2h8a2 2 0 012 2v2m-6 12h8a2 2 0 002-2v-8a2 2 0 00-2-2h-8a2 2 0 00-2 2v8a2 2 0 002 2z"
                        strokeLinecap="round"
                        strokeLinejoin="round"
                        strokeWidth={2}
                      />
                    </svg>
                  )}
                </div>
              </div>
            </div>

            {/* 统计信息 */}
            <div className="flex items-center justify-between pt-2 border-t border-divider">
              <Chip
                className="text-xs"
                color={strategyDisplay.color as any}
                size="sm"
                variant="flat"
              >
                {strategyDisplay.text}
              </Chip>
              <div className="flex items-center gap-1">
                <Chip
                  className="text-xs"
                  color="primary"
                  size="sm"
                  variant="flat"
                >
                  ↑{formatFlow(forward.inFlow || 0)}
                </Chip>
              </div>
              <Chip
                className="text-xs"
                color="success"
                size="sm"
                variant="flat"
              >
                ↓{formatFlow(forward.outFlow || 0)}
              </Chip>
            </div>
          </div>

          <div className="flex gap-1.5 mt-3">
            <Button
              className="flex-1 min-h-8"
              color="primary"
              size="sm"
              startContent={
                <svg
                  className="w-3 h-3"
                  fill="currentColor"
                  viewBox="0 0 20 20"
                >
                  <path d="M13.586 3.586a2 2 0 112.828 2.828l-.793.793-2.828-2.828.793-.793zM11.379 5.793L3 14.172V17h2.828l8.38-8.379-2.83-2.828z" />
                </svg>
              }
              variant="flat"
              onPress={() => handleEdit(forward)}
            >
              编辑
            </Button>
            <Button
              className="flex-1 min-h-8"
              color="warning"
              size="sm"
              startContent={
                <svg
                  className="w-3 h-3"
                  fill="currentColor"
                  viewBox="0 0 20 20"
                >
                  <path
                    clipRule="evenodd"
                    d="M8.257 3.099c.765-1.36 2.722-1.36 3.486 0l5.58 9.92c.75 1.334-.213 2.98-1.742 2.98H4.42c-1.53 0-2.493-1.646-1.743-2.98l5.58-9.92zM11 13a1 1 0 11-2 0 1 1 0 012 0zm-1-8a1 1 0 00-1 1v3a1 1 0 002 0V6a1 1 0 00-1-1z"
                    fillRule="evenodd"
                  />
                </svg>
              }
              variant="flat"
              onPress={() => handleDiagnose(forward)}
            >
              诊断
            </Button>
            <Button
              className="flex-1 min-h-8"
              color="danger"
              size="sm"
              startContent={
                <svg
                  className="w-3 h-3"
                  fill="currentColor"
                  viewBox="0 0 20 20"
                >
                  <path
                    clipRule="evenodd"
                    d="M9 2a1 1 0 000 2h2a1 1 0 100-2H9z"
                    fillRule="evenodd"
                  />
                  <path
                    clipRule="evenodd"
                    d="M10 18a8 8 0 100-16 8 8 0 000 16zM8 7a1 1 0 012 0v4a1 1 0 11-2 0V7zM12 7a1 1 0 012 0v4a1 1 0 11-2 0V7z"
                    fillRule="evenodd"
                  />
                </svg>
              }
              variant="flat"
              onPress={() => handleDelete(forward)}
            >
              删除
            </Button>
          </div>
        </CardBody>
      </Card>
    );
  },
    [isMobile, showAddressModal, useWindowing, viewMode],
  );

  // 渲染配置详情模态框
  const renderCfgDetailModal = () => (
    <Modal
      isOpen={cfgDetailOpen}
      scrollBehavior="outside"
      size="3xl"
      onOpenChange={setCfgDetailOpen}
    >
      <ModalContent>
        {(onClose) => (
          <>
            <ModalHeader className="flex flex-col gap-1">
              <h3 className="text-lg font-semibold">转发配置校验详情</h3>
            </ModalHeader>
            <ModalBody>
              {cfgDetailLoading ? (
                <div className="py-6 flex items-center justify-center">
                  <Spinner size="sm" />{" "}
                  <span className="ml-2 text-sm text-default-500">
                    加载中...
                  </span>
                </div>
              ) : cfgDetail && Array.isArray(cfgDetail.nodes) ? (
                <div className="space-y-4">
                  {cfgDetail.nodes.map((n: any) => (
                    <div
                      key={`${n.nodeId}-${n.role}`}
                      className="border rounded-md p-3"
                    >
                      <div className="flex items-center justify-between">
                        <div className="font-medium">
                          {n.nodeName}{" "}
                          <span className="text-xs text-default-500">
                            ({n.role})
                          </span>
                        </div>
                        <Chip
                          color={n.ok ? "success" : "danger"}
                          size="sm"
                          variant="flat"
                        >
                          {n.ok ? "OK" : "缺失/不一致"}
                        </Chip>
                      </div>
                      <div className="mt-2">
                        <div className="text-xs text-default-600 mb-2">
                          <span className="mr-4">
                            期望端口:{" "}
                            <code className="font-mono">
                              {n.expectedPort ?? "-"}
                            </code>
                          </span>
                          <span className="mr-4">
                            实际端口:{" "}
                            <code className="font-mono">
                              {n.actualPort ?? "-"}
                            </code>
                          </span>
                          <span>
                            在监听:{" "}
                            <code className="font-mono">
                              {n.listening === undefined
                                ? "-"
                                : n.listening
                                  ? "是"
                                  : "否"}
                            </code>
                          </span>
                        </div>
                      </div>
                      <div className="grid grid-cols-1 md:grid-cols-2 gap-3">
                        <div>
                          <div className="text-xs text-default-500 mb-1">
                            期望
                          </div>
                          <Textarea
                            readOnly
                            className="font-mono text-xs"
                            minRows={6}
                            value={JSON.stringify(n.expected || {}, null, 2)}
                          />
                        </div>
                        <div>
                          <div className="text-xs text-default-500 mb-1">
                            实际
                          </div>
                          <Textarea
                            readOnly
                            className="font-mono text-xs"
                            minRows={6}
                            value={
                              n.actual
                                ? JSON.stringify(n.actual, null, 2)
                                : "未查询到对应服务（可能未上报或未创建）"
                            }
                          />
                        </div>
                      </div>
                    </div>
                  ))}
                </div>
              ) : (
                <Alert color="warning" title="暂无数据" />
              )}
            </ModalBody>
            <ModalFooter>
              <Button onPress={onClose}>关闭</Button>
            </ModalFooter>
          </>
        )}
      </ModalContent>
    </Modal>
  );

  const userGroupsMemo = useMemo(
    () => groupForwardsByUserAndTunnel(),
    [sortedForwards],
  );

  if (loading) {
    return (
      <div className="px-3 lg:px-6 py-8 space-y-4">
        <div className="flex justify-end gap-2">
          <div className="skeleton-line w-20" />
          <div className="skeleton-line w-20" />
        </div>
        <div className="grid gap-4 grid-cols-1 sm:grid-cols-2 xl:grid-cols-3 2xl:grid-cols-4">
          {Array.from({ length: 8 }).map((_, idx) => (
            <div key={`forward-skel-${idx}`} className="skeleton-card" />
          ))}
        </div>
      </div>
    );
  }

  return (
    <div className="px-3 lg:px-6 py-8">
      {cfgDetailOpen ? renderCfgDetailModal() : null}
      {/* 页面头部 */}
      <div className="flex items-center justify-between mb-6">
        <div className="flex-1" />
        <div className="flex items-center gap-3">
          <Button size="sm" variant="flat" onPress={() => setOpsOpen(true)}>
            操作日志
          </Button>
          {/* 显示模式切换按钮 */}
          <Button
            isIconOnly
            className="text-sm"
            color="default"
            size="sm"
            title={viewMode === "grouped" ? "切换到直接显示" : "切换到分类显示"}
            variant="flat"
            onPress={handleViewModeChange}
          >
            {viewMode === "grouped" ? (
              <svg className="w-4 h-4" fill="currentColor" viewBox="0 0 20 20">
                <path
                  clipRule="evenodd"
                  d="M3 4a1 1 0 011-1h12a1 1 0 011 1v2a1 1 0 01-1 1H4a1 1 0 01-1-1V4zM3 10a1 1 0 011-1h12a1 1 0 011 1v2a1 1 0 01-1 1H4a1 1 0 01-1-1v-2zM3 16a1 1 0 011-1h12a1 1 0 011 1v2a1 1 0 01-1 1H4a1 1 0 01-1-1v-2z"
                  fillRule="evenodd"
                />
              </svg>
            ) : (
              <svg className="w-4 h-4" fill="currentColor" viewBox="0 0 20 20">
                <path d="M3 4a1 1 0 011-1h12a1 1 0 011 1v2a1 1 0 01-1 1H4a1 1 0 01-1-1V4zM3 10a1 1 0 011-1h6a1 1 0 011 1v6a1 1 0 01-1 1H4a1 1 0 01-1-1v-6zM14 9a1 1 0 00-1 1v6a1 1 0 001 1h2a1 1 0 001-1v-6a1 1 0 00-1-1h-2z" />
              </svg>
            )}
          </Button>
          {viewMode === "direct" && (
            <Switch isSelected={useWindowing} onValueChange={setUseWindowing}>
              虚拟列表
            </Switch>
          )}

          {/* 导入按钮 */}
          <Button
            color="warning"
            size="sm"
            variant="flat"
            onPress={handleImport}
          >
            导入
          </Button>

          {/* 导出按钮 */}
          <Button
            color="success"
            isLoading={exportLoading}
            size="sm"
            variant="flat"
            onPress={handleExport}
          >
            导出
          </Button>

          <Button color="primary" size="sm" variant="flat" onPress={handleAdd}>
            新增
          </Button>
        </div>
      </div>
      {opsOpen ? (
        <OpsLogModal
          isOpen={opsOpen}
          requestId={opReqId || undefined}
          onOpenChange={setOpsOpen}
        />
      ) : null}
      {/* 根据显示模式渲染不同内容 */}
      {viewMode === "grouped" ? (
        /* 按用户和隧道分组的转发列表 */
        !anyModalOpen && userGroupsMemo.length > 0 ? (
          <div className="space-y-6">
            {userGroupsMemo.map((userGroup) => (
              <Card
                key={userGroup.userId || "unknown"}
                className="list-card shadow-sm border border-divider w-full overflow-hidden"
              >
                <CardHeader className="pb-3">
                  <div className="flex items-center justify-between w-full min-w-0">
                    <div className="flex items-center gap-3 min-w-0 flex-1">
                      <div className="w-10 h-10 bg-primary-100 dark:bg-primary-900/30 rounded-full flex items-center justify-center flex-shrink-0">
                        <svg
                          className="w-5 h-5 text-primary"
                          fill="currentColor"
                          viewBox="0 0 20 20"
                        >
                          <path
                            clipRule="evenodd"
                            d="M10 9a3 3 0 100-6 3 3 0 000 6zm-7 9a7 7 0 1114 0H3z"
                            fillRule="evenodd"
                          />
                        </svg>
                      </div>
                      <div className="min-w-0 flex-1">
                        <h2 className="text-base font-medium text-foreground truncate max-w-[150px] sm:max-w-[250px] md:max-w-[350px] lg:max-w-[450px]">
                          {userGroup.userName}
                        </h2>
                        <p className="text-xs text-default-500 truncate max-w-[150px] sm:max-w-[250px] md:max-w-[350px] lg:max-w-[450px]">
                          {userGroup.tunnelGroups.length} 个隧道，
                          {userGroup.tunnelGroups.reduce(
                            (total, tg) => total + tg.forwards.length,
                            0,
                          )}{" "}
                          个转发
                        </p>
                      </div>
                    </div>
                    <Chip
                      className="text-xs flex-shrink-0 ml-2"
                      color="primary"
                      size="sm"
                      variant="flat"
                    >
                      用户
                    </Chip>
                  </div>
                </CardHeader>

                <CardBody className="pt-0">
                  <Accordion className="px-0" variant="splitted">
                    {userGroup.tunnelGroups.map((tunnelGroup) => (
                      <AccordionItem
                        key={tunnelGroup.tunnelId}
                        aria-label={tunnelGroup.tunnelName}
                        className="shadow-none border border-divider"
                        title={
                          <div className="flex items-center justify-between w-full min-w-0 pr-4">
                            <div className="flex items-center gap-3 min-w-0 flex-1">
                              <div className="w-8 h-8 bg-success-100 dark:bg-success-900/30 rounded-lg flex items-center justify-center flex-shrink-0">
                                <svg
                                  className="w-4 h-4 text-success"
                                  fill="none"
                                  stroke="currentColor"
                                  viewBox="0 0 24 24"
                                >
                                  <path
                                    d="M13 10V3L4 14h7v7l9-11h-7z"
                                    strokeLinecap="round"
                                    strokeLinejoin="round"
                                    strokeWidth={2}
                                  />
                                </svg>
                              </div>
                              <div className="min-w-0 flex-1">
                                <h3 className="text-sm font-medium text-foreground truncate max-w-[120px] sm:max-w-[200px] md:max-w-[300px] lg:max-w-[400px]">
                                  {tunnelGroup.tunnelName}
                                </h3>
                              </div>
                            </div>
                            <div className="flex items-center gap-2 flex-shrink-0 ml-2">
                              <Chip
                                className="text-xs"
                                size="sm"
                                variant="flat"
                              >
                                {
                                  tunnelGroup.forwards.filter(
                                    (f) => f.serviceRunning,
                                  ).length
                                }
                                /{tunnelGroup.forwards.length}
                              </Chip>
                            </div>
                          </div>
                        }
                      >
                        <div className="grid grid-cols-1 sm:grid-cols-2 lg:grid-cols-3 xl:grid-cols-4 2xl:grid-cols-5 gap-4 p-4">
                          {tunnelGroup.forwards.map((forward) =>
                            renderForwardCard(forward, undefined),
                          )}
                        </div>
                      </AccordionItem>
                    ))}
                  </Accordion>
                </CardBody>
              </Card>
            ))}
          </div>
        ) : !anyModalOpen ? (
          /* 空状态 */
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
                      d="M8 9l4-4 4 4m0 6l-4 4-4-4"
                      strokeLinecap="round"
                      strokeLinejoin="round"
                      strokeWidth={1.5}
                    />
                  </svg>
                </div>
                <div>
                  <h3 className="text-lg font-semibold text-foreground">
                    暂无转发配置
                  </h3>
                  <p className="text-default-500 text-sm mt-1">
                    还没有创建任何转发配置，点击上方按钮开始创建
                  </p>
                </div>
              </div>
            </CardBody>
          </Card>
        ) : null
      ) : /* 直接显示模式 */
      !anyModalOpen && forwards.length > 0 ? (
        useWindowing ? (
          <VirtualGrid
            className="w-full"
            estimateRowHeight={260}
            items={sortedForwards}
            maxColumns={5}
            minItemWidth={260}
            renderItem={(forward) =>
              forward && forward.id
                ? renderForwardCard(forward, undefined)
                : null
            }
          />
        ) : (
          <DndContext
            collisionDetection={closestCenter}
            sensors={sensors}
            onDragEnd={handleDragEnd}
            onDragStart={() => {}} // 添加空的 onDragStart 处理器
          >
            <SortableContext items={sortableIds} strategy={rectSortingStrategy}>
              <div className="grid grid-cols-1 sm:grid-cols-2 lg:grid-cols-3 xl:grid-cols-4 2xl:grid-cols-5 gap-4">
                {sortedForwards.map((forward) =>
                  forward && forward.id ? (
                    <SortableForwardCard
                      key={forward.id}
                      forward={forward}
                      renderCard={renderForwardCard}
                    />
                  ) : null,
                )}
              </div>
            </SortableContext>
          </DndContext>
        )
      ) : !anyModalOpen ? (
        /* 空状态 */
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
                    d="M8 9l4-4 4 4m0 6l-4 4-4-4"
                    strokeLinecap="round"
                    strokeLinejoin="round"
                    strokeWidth={1.5}
                  />
                </svg>
              </div>
              <div>
                <h3 className="text-lg font-semibold text-foreground">
                  暂无转发配置
                </h3>
                <p className="text-default-500 text-sm mt-1">
                  还没有创建任何转发配置，点击上方按钮开始创建
                </p>
              </div>
            </div>
          </CardBody>
        </Card>
      ) : null}

      {/* 新增/编辑模态框 */}
      <ForwardEditModal
        editForward={editForward}
        forwards={forwards}
        ifaceCacheRef={ifaceCacheRef}
        ifaceInflightRef={ifaceInflightRef}
        isOpen={modalOpen}
        nodesCache={nodesCache}
        previewTunnelMap={previewTunnelMap}
        tunnels={tunnels}
        onOpenChange={setModalOpen}
        onOpsLogOpen={handleOpsLogOpen}
        onSaved={handleSaved}
      />

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
                <h2 className="text-lg font-bold text-danger">确认删除</h2>
              </ModalHeader>
              <ModalBody>
                <p className="text-default-600">
                  确定要删除转发{" "}
                  <span className="font-semibold text-foreground">
                    "{forwardToDelete?.name}"
                  </span>{" "}
                  吗？
                </p>
                <p className="text-small text-default-500 mt-2">
                  此操作无法撤销，删除后该转发将永久消失。
                </p>
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
                  确认删除
                </Button>
              </ModalFooter>
            </>
          )}
        </ModalContent>
      </Modal>

      {/* 地址列表弹窗 */}
      <Modal
        disableAnimation
        isOpen={addressModalOpen}
        scrollBehavior="outside"
        size="lg"
        onClose={() => setAddressModalOpen(false)}
      >
        <ModalContent>
          <ModalHeader className="text-base">{addressModalTitle}</ModalHeader>
          <ModalBody className="pb-6">
            <div className="mb-4 text-right">
              <Button size="sm" onClick={copyAllAddresses}>
                复制
              </Button>
            </div>

            <div className="space-y-2 max-h-60 overflow-y-auto">
              {addressList.map((item) => (
                <div
                  key={item.id}
                  className="flex justify-between items-center p-3 border border-default-200 dark:border-default-100 rounded-lg"
                >
                  <code className="text-sm flex-1 mr-3 text-foreground">
                    {item.address}
                  </code>
                  <Button
                    isLoading={item.copying}
                    size="sm"
                    variant="light"
                    onClick={() => copyAddress(item)}
                  >
                    复制
                  </Button>
                </div>
              ))}
            </div>
          </ModalBody>
        </ModalContent>
      </Modal>

      {/* 导出数据模态框 */}
      <Modal
        backdrop="opaque"
        disableAnimation
        isOpen={exportModalOpen}
        placement="center"
        scrollBehavior="outside"
        size="2xl"
        onClose={() => {
          setExportModalOpen(false);
          setSelectedTunnelForExport(null);
          setExportData("");
        }}
      >
        <ModalContent>
          <ModalHeader className="flex flex-col gap-1">
            <h2 className="text-xl font-bold">导出转发数据</h2>
            <p className="text-small text-default-500">
              格式：目标地址|转发名称|入口端口
            </p>
          </ModalHeader>
          <ModalBody className="pb-6">
            <div className="space-y-4">
              {/* 隧道选择 */}
              <div>
                <Select
                  isRequired
                  label="选择导出隧道"
                  placeholder="请选择要导出的隧道"
                  selectedKeys={
                    selectedTunnelForExport
                      ? [selectedTunnelForExport.toString()]
                      : []
                  }
                  variant="bordered"
                  onSelectionChange={(keys) => {
                    const selectedKey = Array.from(keys)[0] as string;

                    setSelectedTunnelForExport(
                      selectedKey ? parseInt(selectedKey) : null,
                    );
                  }}
                >
                  {tunnelOptions}
                </Select>
              </div>

              {/* 导出按钮和数据 */}
              {exportData && (
                <div className="flex justify-between items-center">
                  <Button
                    color="primary"
                    isDisabled={!selectedTunnelForExport}
                    isLoading={exportLoading}
                    size="sm"
                    startContent={
                      <svg
                        className="w-4 h-4"
                        fill="currentColor"
                        viewBox="0 0 20 20"
                      >
                        <path
                          clipRule="evenodd"
                          d="M3 17a1 1 0 011-1h12a1 1 0 110 2H4a1 1 0 01-1-1zM6.293 6.707a1 1 0 010-1.414l3-3a1 1 0 011.414 0l3 3a1 1 0 01-1.414 1.414L11 5.414V13a1 1 0 11-2 0V5.414L7.707 6.707a1 1 0 01-1.414 0z"
                          fillRule="evenodd"
                        />
                      </svg>
                    }
                    onPress={executeExport}
                  >
                    重新生成
                  </Button>
                  <Button
                    color="secondary"
                    size="sm"
                    startContent={
                      <svg
                        className="w-4 h-4"
                        fill="currentColor"
                        viewBox="0 0 20 20"
                      >
                        <path d="M8 3a1 1 0 011-1h2a1 1 0 110 2H9a1 1 0 01-1-1z" />
                        <path d="M6 3a2 2 0 00-2 2v11a2 2 0 002 2h8a2 2 0 002-2V5a2 2 0 00-2-2 3 3 0 01-3 3H9a3 3 0 01-3-3z" />
                      </svg>
                    }
                    onPress={copyExportData}
                  >
                    复制
                  </Button>
                </div>
              )}

              {/* 初始导出按钮 */}
              {!exportData && (
                <div className="text-right">
                  <Button
                    color="primary"
                    isDisabled={!selectedTunnelForExport}
                    isLoading={exportLoading}
                    size="sm"
                    startContent={
                      <svg
                        className="w-4 h-4"
                        fill="currentColor"
                        viewBox="0 0 20 20"
                      >
                        <path
                          clipRule="evenodd"
                          d="M3 17a1 1 0 011-1h12a1 1 0 110 2H4a1 1 0 01-1-1zM6.293 6.707a1 1 0 010-1.414l3-3a1 1 0 011.414 0l3 3a1 1 0 01-1.414 1.414L11 5.414V13a1 1 0 11-2 0V5.414L7.707 6.707a1 1 0 01-1.414 0z"
                          fillRule="evenodd"
                        />
                      </svg>
                    }
                    onPress={executeExport}
                  >
                    生成导出数据
                  </Button>
                </div>
              )}

              {/* 导出数据显示 */}
              {exportData && (
                <div className="relative">
                  <Textarea
                    readOnly
                    className="font-mono text-sm"
                    classNames={{
                      input: "font-mono text-sm",
                    }}
                    maxRows={20}
                    minRows={10}
                    placeholder="暂无数据"
                    value={exportData}
                    variant="bordered"
                  />
                </div>
              )}
            </div>
          </ModalBody>
          <ModalFooter>
            <Button variant="light" onPress={() => setExportModalOpen(false)}>
              关闭
            </Button>
          </ModalFooter>
        </ModalContent>
      </Modal>

      {/* 导入数据模态框 */}
      <Modal
        backdrop="opaque"
        disableAnimation
        isOpen={importModalOpen}
        placement="center"
        scrollBehavior="outside"
        size="2xl"
        onClose={() => setImportModalOpen(false)}
      >
        <ModalContent>
          <ModalHeader className="flex flex-col gap-1">
            <h2 className="text-xl font-bold">导入转发数据</h2>
            <p className="text-small text-default-500">
              格式：目标地址|转发名称|入口端口，每行一个，入口端口留空将自动分配可用端口
            </p>
            <p className="text-small text-default-400">
              目标地址支持单个地址(如：example.com:8080)或多个地址用逗号分隔(如：3.3.3.3:3,4.4.4.4:4)
            </p>
          </ModalHeader>
          <ModalBody className="pb-6">
            <div className="space-y-4">
              {/* 隧道选择 */}
              <div>
                <Select
                  isRequired
                  label="选择导入隧道"
                  placeholder="请选择要导入的隧道"
                  selectedKeys={
                    selectedTunnelForImport
                      ? [selectedTunnelForImport.toString()]
                      : []
                  }
                  variant="bordered"
                  onSelectionChange={(keys) => {
                    const selectedKey = Array.from(keys)[0] as string;

                    setSelectedTunnelForImport(
                      selectedKey ? parseInt(selectedKey) : null,
                    );
                  }}
                >
                  {tunnelOptions}
                </Select>
              </div>

              {/* 输入区域 */}
              <div>
                <Textarea
                  classNames={{
                    input: "font-mono text-sm",
                  }}
                  label="导入数据"
                  maxRows={12}
                  minRows={8}
                  placeholder="请输入要导入的转发数据，格式：目标地址|转发名称|入口端口|出口IP(可选)"
                  value={importData}
                  variant="flat"
                  onChange={(e) => setImportData(e.target.value)}
                />
              </div>

              {/* 导入结果 */}
              {importResults.length > 0 && (
                <div>
                  <div className="flex items-center justify-between mb-2">
                    <h3 className="text-base font-semibold">导入结果</h3>
                    <div className="flex items-center gap-2">
                      <span className="text-xs text-default-500">
                        成功：{importResults.filter((r) => r.success).length} /
                        总计：{importResults.length}
                      </span>
                    </div>
                  </div>

                  <div
                    className="max-h-40 overflow-y-auto space-y-1"
                    style={{
                      scrollbarWidth: "thin",
                      scrollbarColor: "rgb(156 163 175) transparent",
                    }}
                  >
                    {importResults.map((result, index) => (
                      <div
                        key={index}
                        className={`p-2 rounded border ${
                          result.success
                            ? "bg-success-50 dark:bg-success-100/10 border-success-200 dark:border-success-300/20"
                            : "bg-danger-50 dark:bg-danger-100/10 border-danger-200 dark:border-danger-300/20"
                        }`}
                      >
                        <div className="flex items-center gap-2">
                          {result.success ? (
                            <svg
                              className="w-3 h-3 text-success-600 flex-shrink-0"
                              fill="currentColor"
                              viewBox="0 0 20 20"
                            >
                              <path
                                clipRule="evenodd"
                                d="M16.707 5.293a1 1 0 010 1.414l-8 8a1 1 0 01-1.414 0l-4-4a1 1 0 011.414-1.414L8 12.586l7.293-7.293a1 1 0 011.414 0z"
                                fillRule="evenodd"
                              />
                            </svg>
                          ) : (
                            <svg
                              className="w-3 h-3 text-danger-600 flex-shrink-0"
                              fill="currentColor"
                              viewBox="0 0 20 20"
                            >
                              <path
                                clipRule="evenodd"
                                d="M4.293 4.293a1 1 0 011.414 0L10 8.586l4.293-4.293a1 1 0 111.414 1.414L11.414 10l4.293 4.293a1 1 0 01-1.414 1.414L10 11.414l-4.293 4.293a1 1 0 01-1.414-1.414L8.586 10 4.293 5.707a1 1 0 010-1.414z"
                                fillRule="evenodd"
                              />
                            </svg>
                          )}
                          <div className="flex-1 min-w-0">
                            <div className="flex items-center gap-2 mb-0.5">
                              <span
                                className={`text-xs font-medium ${
                                  result.success
                                    ? "text-success-700 dark:text-success-300"
                                    : "text-danger-700 dark:text-danger-300"
                                }`}
                              >
                                {result.success ? "成功" : "失败"}
                              </span>
                              <span className="text-xs text-default-500">
                                |
                              </span>
                              <code className="text-xs font-mono text-default-600 truncate">
                                {result.line}
                              </code>
                            </div>
                            <div
                              className={`text-xs ${
                                result.success
                                  ? "text-success-600 dark:text-success-400"
                                  : "text-danger-600 dark:text-danger-400"
                              }`}
                            >
                              {result.message}
                            </div>
                          </div>
                        </div>
                      </div>
                    ))}
                  </div>
                </div>
              )}
            </div>
          </ModalBody>
          <ModalFooter>
            <Button variant="light" onPress={() => setImportModalOpen(false)}>
              关闭
            </Button>
            <Button
              color="warning"
              isDisabled={!importData.trim() || !selectedTunnelForImport}
              isLoading={importLoading}
              onPress={executeImport}
            >
              开始导入
            </Button>
          </ModalFooter>
        </ModalContent>
      </Modal>

      {/* 诊断结果模态框 */}
      <Modal
        backdrop="opaque"
        disableAnimation
        isOpen={diagnosisModalOpen}
        placement="center"
        scrollBehavior="outside"
        size="2xl"
        onOpenChange={setDiagnosisModalOpen}
      >
        <ModalContent>
          {(onClose) => (
            <>
              <ModalHeader className="flex flex-col gap-1">
                <h2 className="text-xl font-bold">转发诊断结果</h2>
                {currentDiagnosisForward && (
                  <div className="flex items-center gap-2 min-w-0">
                    <span className="text-small text-default-500 truncate flex-1 min-w-0">
                      {currentDiagnosisForward.name}
                    </span>
                    <Chip
                      className="flex-shrink-0"
                      color="primary"
                      size="sm"
                      variant="flat"
                    >
                      转发服务
                    </Chip>
                  </div>
                )}
              </ModalHeader>
              <ModalBody>
                {diagnosisLoading ? (
                  <div className="flex items-center justify-center py-16">
                    <div className="flex items-center gap-3">
                      <Spinner size="sm" />
                      <span className="text-default-600">
                        正在诊断转发连接...
                      </span>
                    </div>
                  </div>
                ) : diagnosisResult ? (
                  <div className="space-y-4">
                    {diagnosisResult.results.map(
                      (result: any, index: number) => {
                        const quality = getQualityDisplay(
                          result.averageTime,
                          result.packetLoss,
                        );

                        return (
                          <Card
                            key={index}
                            className={`shadow-sm border ${result.success ? "border-success" : "border-danger"}`}
                          >
                            <CardHeader className="pb-2">
                              <div className="flex items-center justify-between w-full">
                                <div>
                                  <h3 className="text-lg font-semibold text-foreground">
                                    {result.description}
                                  </h3>
                                  <div className="flex items-center gap-2 mt-1 flex-wrap">
                                    <span className="text-small text-default-500">
                                      节点: {result.nodeName}
                                    </span>
                                    <Chip
                                      color={
                                        result.success ? "success" : "danger"
                                      }
                                      size="sm"
                                      variant="flat"
                                    >
                                      {result.success ? "连接成功" : "连接失败"}
                                    </Chip>
                                    {(result.targetIp || result.targetPort) && (
                                      <Chip
                                        color="secondary"
                                        size="sm"
                                        variant="flat"
                                      >
                                        目标 {result.targetIp}
                                        {result.targetPort
                                          ? ":" + result.targetPort
                                          : ""}
                                      </Chip>
                                    )}
                                  </div>
                                </div>
                              </div>
                            </CardHeader>

                            <CardBody className="pt-0">
                              {/* 特殊渲染：节点服务清单（逐跳） */}
                              {Array.isArray(result.hops) ? (
                                <div className="space-y-4">
                                  {result.hops.map((hop: any, i: number) => (
                                    <div
                                      key={i}
                                      className="border border-default-200 rounded-lg p-3"
                                    >
                                      <div className="flex items-center justify-between">
                                        <div className="font-medium text-foreground">
                                          {hop.nodeName}{" "}
                                          <span className="text-default-500">
                                            ({hop.role || "-"})
                                          </span>
                                        </div>
                                        <div className="text-small text-default-500">
                                          ID: {hop.nodeId}
                                        </div>
                                      </div>
                                      <div className="mt-3 space-y-2">
                                        {Array.isArray(hop.services) &&
                                        hop.services.length > 0 ? (
                                          hop.services.map(
                                            (svc: any, j: number) => (
                                              <div
                                                key={j}
                                                className="rounded-md bg-content1 p-3 border border-default-200"
                                              >
                                                <div className="flex items-center justify-between gap-3">
                                                  <div
                                                    className="font-mono text-sm truncate"
                                                    title={svc.name}
                                                  >
                                                    {svc.name}
                                                  </div>
                                                  <div className="flex items-center gap-2">
                                                    {svc.listener && (
                                                      <Chip
                                                        color="default"
                                                        size="sm"
                                                        variant="flat"
                                                      >
                                                        L:{svc.listener}
                                                      </Chip>
                                                    )}
                                                    {svc.handler && (
                                                      <Chip
                                                        color="default"
                                                        size="sm"
                                                        variant="flat"
                                                      >
                                                        H:{svc.handler}
                                                      </Chip>
                                                    )}
                                                    <Chip
                                                      color={
                                                        svc.listening
                                                          ? "success"
                                                          : "danger"
                                                      }
                                                      size="sm"
                                                      variant="flat"
                                                    >
                                                      {svc.listening
                                                        ? "监听中"
                                                        : "未监听"}
                                                    </Chip>
                                                    {typeof svc.inRange ===
                                                      "boolean" && (
                                                      <Chip
                                                        color={
                                                          svc.inRange
                                                            ? "success"
                                                            : "warning"
                                                        }
                                                        size="sm"
                                                        variant="flat"
                                                      >
                                                        {svc.inRange
                                                          ? "端口在范围内"
                                                          : "超出范围"}
                                                        {svc.range
                                                          ? ` (${svc.range})`
                                                          : ""}
                                                      </Chip>
                                                    )}
                                                    {!svc.listening && (
                                                      <Button
                                                        color="warning"
                                                        isLoading={
                                                          restartingNodeId ===
                                                          hop.nodeId
                                                        }
                                                        size="sm"
                                                        variant="flat"
                                                        onPress={() =>
                                                          handleRestartGost(
                                                            hop.nodeId,
                                                          )
                                                        }
                                                      >
                                                        重启gost
                                                      </Button>
                                                    )}
                                                  </div>
                                                </div>
                                                <div className="mt-2 text-small text-default-500 flex items-center gap-1">
                                                  <span>地址:</span>
                                                  <code
                                                    className="font-mono truncate"
                                                    title={svc.addr || ""}
                                                  >
                                                    {svc.addr || "-"}
                                                  </code>
                                                  {svc.port ? (
                                                    <span className="ml-1">
                                                      (端口 {svc.port})
                                                    </span>
                                                  ) : null}
                                                </div>
                                                {svc.message && (
                                                  <div className="mt-2 text-small text-danger-500">
                                                    {svc.message}
                                                  </div>
                                                )}
                                              </div>
                                            ),
                                          )
                                        ) : (
                                          <div className="text-small text-default-400">
                                            未找到相关服务
                                          </div>
                                        )}
                                      </div>
                                    </div>
                                  ))}
                                </div>
                              ) : result.success ? (
                                <div className="space-y-3">
                                  {(() => {
                                    const isIperf3 =
                                      typeof result.description === "string" &&
                                      result.description
                                        .toLowerCase()
                                        .includes("iperf3");

                                    if (isIperf3) {
                                      const bw = ((): number | undefined => {
                                        const v: any = (result as any)
                                          .bandwidthMbps;
                                        const n =
                                          typeof v === "string" ? Number(v) : v;

                                        return Number.isFinite(n)
                                          ? Number(n)
                                          : undefined;
                                      })();

                                      return (
                                        <div className="grid grid-cols-1 gap-4">
                                          <div className="text-center">
                                            <div className="text-2xl font-bold text-success">
                                              {bw !== undefined
                                                ? bw.toFixed(2)
                                                : "-"}
                                            </div>
                                            <div className="text-small text-default-500">
                                              带宽(Mbps)
                                            </div>
                                          </div>
                                        </div>
                                      );
                                    }

                                    return (
                                      <div className="grid grid-cols-3 gap-4">
                                        <div className="text-center">
                                          <div className="text-2xl font-bold text-primary">
                                            {result.averageTime?.toFixed(0)}
                                          </div>
                                          <div className="text-small text-default-500">
                                            平均延迟(ms)
                                          </div>
                                        </div>
                                        <div className="text-center">
                                          <div className="text-2xl font-bold text-warning">
                                            {result.packetLoss?.toFixed(1)}
                                          </div>
                                          <div className="text-small text-default-500">
                                            丢包率(%)
                                          </div>
                                        </div>
                                        <div className="text-center">
                                          {quality && (
                                            <>
                                              <Chip
                                                color={quality.color as any}
                                                size="lg"
                                                variant="flat"
                                              >
                                                {quality.text}
                                              </Chip>
                                              <div className="text-small text-default-500 mt-1">
                                                连接质量
                                              </div>
                                            </>
                                          )}
                                        </div>
                                      </div>
                                    );
                                  })()}
                                  <div className="text-small text-default-500 flex items-center gap-1">
                                    <span className="flex-shrink-0">
                                      目标地址:
                                    </span>
                                    <code
                                      className="font-mono truncate min-w-0"
                                      title={`${result.targetIp}${result.targetPort ? ":" + result.targetPort : ""}`}
                                    >
                                      {result.targetIp}
                                      {result.targetPort
                                        ? ":" + result.targetPort
                                        : ""}
                                    </code>
                                  </div>
                                  {result.reqId && (
                                    <div className="text-small text-default-400">
                                      reqId:{" "}
                                      <code className="font-mono">
                                        {result.reqId}
                                      </code>
                                    </div>
                                  )}
                                </div>
                              ) : (
                                <div className="space-y-2">
                                  <div className="text-small text-default-500 flex items-center gap-1">
                                    <span className="flex-shrink-0">
                                      目标地址:
                                    </span>
                                    <code
                                      className="font-mono truncate min-w-0"
                                      title={`${result.targetIp}${result.targetPort ? ":" + result.targetPort : ""}`}
                                    >
                                      {result.targetIp}
                                      {result.targetPort
                                        ? ":" + result.targetPort
                                        : ""}
                                    </code>
                                  </div>
                                  {result.reqId && (
                                    <div className="text-small text-default-400">
                                      reqId:{" "}
                                      <code className="font-mono">
                                        {result.reqId}
                                      </code>
                                    </div>
                                  )}
                                  <Alert
                                    color="danger"
                                    description={result.message}
                                    title="错误详情"
                                    variant="flat"
                                  />
                                </div>
                              )}
                            </CardBody>
                          </Card>
                        );
                      },
                    )}
                  </div>
                ) : (
                  <div className="text-center py-16">
                    <div className="w-16 h-16 bg-default-100 rounded-full flex items-center justify-center mx-auto mb-4">
                      <svg
                        className="w-8 h-8 text-default-400"
                        fill="none"
                        stroke="currentColor"
                        viewBox="0 0 24 24"
                      >
                        <path
                          d="M9.75 9.75l4.5 4.5m0-4.5l-4.5 4.5M21 12a9 9 0 11-18 0 9 9 0 0118 0z"
                          strokeLinecap="round"
                          strokeLinejoin="round"
                          strokeWidth={1.5}
                        />
                      </svg>
                    </div>
                    <h3 className="text-lg font-semibold text-foreground">
                      暂无诊断数据
                    </h3>
                  </div>
                )}
              </ModalBody>
              <ModalFooter>
                <Button variant="light" onPress={onClose}>
                  关闭
                </Button>
                {currentDiagnosisForward && (
                  <Button
                    color="primary"
                    isLoading={diagnosisLoading}
                    onPress={() => handleDiagnose(currentDiagnosisForward)}
                  >
                    重新诊断
                  </Button>
                )}
              </ModalFooter>
            </>
          )}
        </ModalContent>
      </Modal>
    </div>
  );
}
