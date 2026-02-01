import { useState, useEffect, useCallback, useMemo, useRef, memo } from "react";
import { Card, CardBody, CardHeader } from "@heroui/card";
import { Button } from "@heroui/button";
import { Input } from "@heroui/input";
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
import { Divider } from "@heroui/divider";
import { Alert } from "@heroui/alert";
import toast from "react-hot-toast";
import {
  DndContext,
  DragEndEvent,
  PointerSensor,
  closestCenter,
  useSensor,
  useSensors,
} from "@dnd-kit/core";
import {
  SortableContext,
  arrayMove,
  useSortable,
  verticalListSortingStrategy,
} from "@dnd-kit/sortable";
import { CSS } from "@dnd-kit/utilities";

import OpsLogModal from "@/components/OpsLogModal";
import VirtualGrid from "@/components/VirtualGrid";
// import moved above; avoid duplicate react imports
import { getNodeInterfaces } from "@/api";
import {
  createTunnel,
  getTunnelList,
  updateTunnel,
  deleteTunnel,
  getNodeList,
  diagnoseTunnelStep,
  getExitNodes,
  enableGostApi,
} from "@/api";

interface Tunnel {
  id: number;
  name: string;
  type: number; // 1: 端口转发, 2: 隧道转发
  inNodeId: number;
  outNodeId?: number;
  outExitId?: number;
  inIp: string;
  outIp?: string;
  protocol?: string;
  tcpListenAddr: string;
  udpListenAddr: string;
  interfaceName?: string;
  flow: number; // 1: 单向, 2: 双向
  trafficRatio: number;
  status: number;
  createdTime: string;
}

interface Node {
  id: number;
  name: string;
  status: number; // 1: 在线, 0: 离线
}

interface TunnelForm {
  id?: number;
  name: string;
  type: number;
  inNodeId: number | null;
  outNodeId?: number | null;
  outExitId?: number | null;
  protocol: string;
  tcpListenAddr: string;
  udpListenAddr: string;
  interfaceName?: string;
  flow: number;
  trafficRatio: number;
  status: number;
}

interface DiagnosisResult {
  tunnelName: string;
  tunnelType: string;
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
    bandwidthMbps?: number; // 添加此属性
  }>;
}

interface ExitNodeItem {
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
}

interface RouteItem {
  key: string;
  type: "node" | "external";
  id: number;
  name: string;
  host?: string;
  isExit: boolean;
}

type TunnelEditModalProps = {
  isOpen: boolean;
  onOpenChange: (open: boolean) => void;
  nodes: Node[];
  exitNodes: ExitNodeItem[];
  editTunnel: Tunnel | null;
  onSaved: () => void;
};

const DEFAULT_FORM: TunnelForm = {
  name: "",
  type: 1,
  inNodeId: null,
  outNodeId: null,
  outExitId: null,
  protocol: "tls",
  tcpListenAddr: "[::]",
  udpListenAddr: "[::]",
  interfaceName: "",
  flow: 1,
  trafficRatio: 1.0,
  status: 1,
};

type SortableRouteItemProps = {
  item: RouteItem;
  onRemove: (key: string) => void;
};

const SortableRouteItem = memo(({ item, onRemove }: SortableRouteItemProps) => {
  const { attributes, listeners, setNodeRef, transform, transition, isDragging } =
    useSortable({ id: item.key });
  const style = {
    transform: CSS.Transform.toString(transform),
    transition,
    opacity: isDragging ? 0.6 : 1,
  };

  return (
    <div
      ref={setNodeRef}
      className="flex items-center gap-3 np-soft p-2"
      style={style}
    >
      <button
        className="cursor-grab text-default-500"
        type="button"
        {...attributes}
        {...listeners}
      >
        <svg className="w-4 h-4" fill="none" stroke="currentColor" viewBox="0 0 24 24">
          <path
            d="M8 6h.01M8 12h.01M8 18h.01M16 6h.01M16 12h.01M16 18h.01"
            strokeLinecap="round"
            strokeLinejoin="round"
            strokeWidth={2}
          />
        </svg>
      </button>
      <div className="flex-1">
        <div className="font-medium">{item.name || "-"}</div>
        {item.type === "external" && item.host ? (
          <div className="text-xs text-default-500">{item.host}</div>
        ) : null}
      </div>
      {item.type === "external" ? (
        <Chip size="sm" variant="flat" color="warning">
          外部
        </Chip>
      ) : null}
      {item.isExit ? (
        <Chip size="sm" variant="flat" color="primary">
          出口
        </Chip>
      ) : null}
      <Button size="sm" variant="flat" onPress={() => onRemove(item.key)}>
        移除
      </Button>
    </div>
  );
});

const TunnelEditModal = memo(
  ({
    isOpen,
    onOpenChange,
    nodes,
    exitNodes,
    editTunnel,
    onSaved,
  }: TunnelEditModalProps) => {
    const isEdit = !!editTunnel;
    const [form, setForm] = useState<TunnelForm>(DEFAULT_FORM);
    const [errors, setErrors] = useState<{ [key: string]: string }>({});
    const [submitLoading, setSubmitLoading] = useState(false);
    const [midPath, setMidPath] = useState<number[]>([]);
    const [midPathReady, setMidPathReady] = useState(false);
    const [entryIface, setEntryIface] = useState<string>("");
    const [midIfaces, setMidIfaces] = useState<Record<number, string>>({});
    const [midBindIps, setMidBindIps] = useState<Record<number, string>>({});
    const [exitBindIp, setExitBindIp] = useState<string>("");
    const [ifaceCache, setIfaceCache] = useState<Record<number, string[]>>({});
    const [exitPort, setExitPort] = useState<number | null>(null);
    const EXIT_METHODS = [
      "AEAD_CHACHA20_POLY1305",
      "chacha20-ietf-poly1305",
      "AEAD_AES_128_GCM",
      "AEAD_AES_256_GCM",
    ];
    const [exitPassword, setExitPassword] = useState<string>("");
    const [exitMethod, setExitMethod] = useState<string>(EXIT_METHODS[0]);
    const [exitObserver, setExitObserver] = useState<string>("console");
    const [exitLimiter, setExitLimiter] = useState<string>("");
    const [exitRLimiter, setExitRLimiter] = useState<string>("");
    const [exitDeployed, setExitDeployed] = useState<string>("");
    const [exitMetaItems, setExitMetaItems] = useState<
      Array<{ id: number; key: string; value: string }>
    >([]);
    const [entryApiOn, setEntryApiOn] = useState<boolean | null>(null);
    const [routeItems, setRouteItems] = useState<RouteItem[]>([]);
    const initRouteRef = useRef(false);

    const exitNodeIdSet = useMemo(() => {
      const set = new Set<number>();

      exitNodes.forEach((n) => {
        if (n.source === "node" && n.nodeId) set.add(n.nodeId);
      });

      return set;
    }, [exitNodes]);

    const externalExitNodes = useMemo(
      () => exitNodes.filter((n) => n.source === "external"),
      [exitNodes],
    );

    const selectedExitInfo = useMemo(() => {
      if (form.outExitId) {
        const ext = externalExitNodes.find(
          (n) => n.exitId === form.outExitId,
        );

        if (ext) {
          return { name: ext.name, host: ext.host, type: "external" as const };
        }
      }
      if (form.outNodeId) {
        const outNode = nodes.find((n) => n.id === form.outNodeId);
        const exitNode = exitNodes.find(
          (n) => n.source === "node" && n.nodeId === form.outNodeId,
        );

        if (outNode) {
          return {
            name: outNode.name,
            host: exitNode?.host || "",
            type: "node" as const,
          };
        }
      }
      return null;
    }, [exitNodes, externalExitNodes, form.outExitId, form.outNodeId, nodes]);

    const sensors = useSensors(
      useSensor(PointerSensor, { activationConstraint: { distance: 4 } }),
    );

    const buildRouteFromIds = useCallback(
      (
        entryId: number | null,
        mids: number[],
        outNodeId?: number | null,
        outExitId?: number | null,
      ) => {
        const items: RouteItem[] = [];

        if (entryId) {
          const entryNode = nodes.find((n) => n.id === entryId);
          if (entryNode) {
            items.push({
              key: `node-${entryNode.id}`,
              type: "node",
              id: entryNode.id,
              name: entryNode.name,
              isExit: exitNodeIdSet.has(entryNode.id),
            });
          }
        }
        mids.forEach((nid) => {
          const midNode = nodes.find((n) => n.id === nid);
          if (midNode) {
            items.push({
              key: `node-${midNode.id}`,
              type: "node",
              id: midNode.id,
              name: midNode.name,
              isExit: exitNodeIdSet.has(midNode.id),
            });
          }
        });
        if (outExitId) {
          const ext = externalExitNodes.find((n) => n.exitId === outExitId);
          if (ext) {
            items.push({
              key: `exit-${ext.exitId}`,
              type: "external",
              id: ext.exitId || 0,
              name: ext.name,
              host: ext.host,
              isExit: true,
            });
          }
        } else if (outNodeId) {
          const outNode = nodes.find((n) => n.id === outNodeId);
          if (outNode) {
            items.push({
              key: `node-${outNode.id}`,
              type: "node",
              id: outNode.id,
              name: outNode.name,
              isExit: exitNodeIdSet.has(outNode.id),
            });
          }
        }

        return items;
      },
      [externalExitNodes, exitNodeIdSet, nodes],
    );

    const syncRouteToForm = useCallback(
      (items: RouteItem[]) => {
        if (items.length === 0) {
          setForm((prev) => ({
            ...prev,
            inNodeId: null,
            outNodeId: null,
            outExitId: null,
          }));
          setMidPath([]);
          return;
        }
        const entry = items[0];
        const last = items[items.length - 1];
        const midNodes = items
          .slice(1, items.length - 1)
          .filter((it) => it.type === "node")
          .map((it) => it.id);

        setForm((prev) => ({
          ...prev,
          inNodeId: entry.type === "node" ? entry.id : null,
          outNodeId:
            prev.type === 2 && last?.type === "node" ? last.id : null,
          outExitId:
            prev.type === 2 && last?.type === "external" ? last.id : null,
        }));
        setMidPath(midNodes);
      },
      [setForm, setMidPath],
    );

    useEffect(() => {
      if (!isOpen) return;
      initRouteRef.current = false;
      if (!editTunnel) {
        setForm(DEFAULT_FORM);
        setErrors({});
        setMidPath([]);
        setMidPathReady(true);
        setRouteItems([]);
        setEntryIface("");
        setMidIfaces({});
        setMidBindIps({});
        setExitBindIp("");
        setIfaceCache({});
        setExitPort(null);
        setExitPassword("");
        setExitMethod(EXIT_METHODS[0]);
        setExitObserver("console");
        setExitLimiter("");
        setExitRLimiter("");
        setExitDeployed("");
        setExitMetaItems([]);
        setEntryApiOn(null);
        return;
      }
      setForm({
        id: editTunnel.id,
        name: editTunnel.name,
        type: editTunnel.type,
        inNodeId: editTunnel.inNodeId,
        outNodeId: editTunnel.outNodeId || null,
        outExitId: editTunnel.outExitId || null,
        protocol: editTunnel.protocol || "tls",
        tcpListenAddr: editTunnel.tcpListenAddr || "[::]",
        udpListenAddr: editTunnel.udpListenAddr || "[::]",
        interfaceName: editTunnel.interfaceName || "",
        flow: editTunnel.flow,
        trafficRatio: editTunnel.trafficRatio,
        status: editTunnel.status,
      });
      setErrors({});
      setMidPath([]);
      setRouteItems([]);
      setMidPathReady(false);
      setExitPort(null);
      setExitPassword("");
      setExitMethod(EXIT_METHODS[0]);
      setExitObserver("console");
      setExitLimiter("");
      setExitRLimiter("");
      setExitDeployed("");
      setExitMetaItems([]);
      try {
        const n: any = nodes.find(
          (nn) => Number(nn.id) === Number(editTunnel.inNodeId),
        );
        setEntryApiOn(
          typeof (n as any)?.gostApi !== "undefined"
            ? (n as any).gostApi === 1
            : null,
        );
      } catch {
        setEntryApiOn(null);
      }
      (async () => {
        try {
          const { getTunnelPath } = await import("@/api");
          const r: any = await getTunnelPath(editTunnel.id);

          if (r.code === 0 && Array.isArray(r.data?.path))
            setMidPath(r.data.path);
        } catch {}
        setMidPathReady(true);
        try {
          const { getTunnelIface } = await import("@/api");
          const r: any = await getTunnelIface(editTunnel.id);

          if (r.code === 0 && Array.isArray(r.data?.ifaces)) {
            const map: Record<number, string> = {};

            r.data.ifaces.forEach((x: any) => {
              if (x?.nodeId) map[Number(x.nodeId)] = String(x.ip || "");
            });
            setMidIfaces(map);
          }
        } catch {}
        try {
          const { getTunnelBind } = await import("@/api");
          const r: any = await getTunnelBind(editTunnel.id);

          if (r.code === 0 && Array.isArray(r.data?.binds)) {
            const map: Record<number, string> = {};

            r.data.binds.forEach((x: any) => {
              if (x?.nodeId) map[Number(x.nodeId)] = String(x.ip || "");
            });
            setMidBindIps(map);
            if (editTunnel.outNodeId && map[editTunnel.outNodeId])
              setExitBindIp(map[editTunnel.outNodeId]);
          }
        } catch {}
      })();
    }, [editTunnel, isOpen, nodes]);

    useEffect(() => {
      if (!isOpen) return;
      if (!midPathReady) return;
      if (initRouteRef.current) return;
      const items = buildRouteFromIds(
        form.inNodeId,
        midPath,
        form.outNodeId,
        form.outExitId,
      );

      if (items.length > 0) {
        setRouteItems(items);
        initRouteRef.current = true;
      }
    }, [
      buildRouteFromIds,
      form.inNodeId,
      form.outExitId,
      form.outNodeId,
      isOpen,
      midPath,
      midPathReady,
    ]);

    useEffect(() => {
      if (!isOpen) return;
      setRouteItems((prev) =>
        prev.map((item) =>
          item.type === "node"
            ? { ...item, isExit: exitNodeIdSet.has(item.id) }
            : item,
        ),
      );
    }, [exitNodeIdSet, isOpen]);

    useEffect(() => {
      if (!isOpen) return;
      syncRouteToForm(routeItems);
    }, [isOpen, routeItems, syncRouteToForm]);

    useEffect(() => {
      if (form.inNodeId) {
        try {
          const n: any = nodes.find(
            (nn) => Number(nn.id) === Number(form.inNodeId),
          );
          setEntryApiOn(
            typeof (n as any)?.gostApi !== "undefined"
              ? (n as any).gostApi === 1
              : null,
          );
        } catch {
          setEntryApiOn(null);
        }
      } else {
        setEntryApiOn(null);
      }
    }, [form.inNodeId, nodes]);

    useEffect(() => {
      if (!isOpen || form.type !== 2) return;
      if (!form.outNodeId) {
        setExitDeployed("");
        return;
      }
      const nid = form.outNodeId;

      setExitDeployed("");
      import("@/api")
        .then(({ queryNodeServices }) =>
          queryNodeServices({ nodeId: nid, filter: "ss" }),
        )
        .then((res: any) => {
          if (res.code === 0 && Array.isArray(res.data)) {
            const items = res.data as any[];
            const ss = items.find((x) => x && x.handler === "ss");

            if (ss) {
              const desc = `已部署: 端口 ${ss.port || ss.addr || "-"}，监听 ${ss.listening ? "是" : "否"}`;

              setExitDeployed(desc);
              if (!exitPort && ss.port) setExitPort(Number(ss.port));
            } else {
              setExitDeployed("未部署");
            }
          }
        })
        .catch(() => {});
      import("@/api").then(({ getNodeInterfaces }) => {
        getNodeInterfaces(nid).catch(() => {});
      });
    }, [exitPort, form.outNodeId, form.type, isOpen]);

    const toggleNodeRoute = (node: Node) => {
      setRouteItems((prev) => {
        const idx = prev.findIndex(
          (item) => item.type === "node" && item.id === node.id,
        );
        if (idx >= 0) {
          return prev.filter((_, i) => i !== idx);
        }
        const newItem: RouteItem = {
          key: `node-${node.id}`,
          type: "node",
          id: node.id,
          name: node.name,
          isExit: exitNodeIdSet.has(node.id),
        };
        const extIndex = prev.findIndex((item) => item.type === "external");
        if (extIndex >= 0) {
          const next = prev.slice();
          next.splice(extIndex, 0, newItem);
          return next;
        }
        return [...prev, newItem];
      });
    };

    const toggleExternalExit = (ext: ExitNodeItem) => {
      const exitId = ext.exitId;
      if (!exitId) return;
      setRouteItems((prev) => {
        const hasExt = prev.some(
          (item) => item.type === "external" && item.id === exitId,
        );
        const next = prev.filter((item) => item.type !== "external");
        if (hasExt) return next;
        next.push({
          key: `exit-${exitId}`,
          type: "external",
          id: exitId,
          name: ext.name,
          host: ext.host,
          isExit: true,
        });
        return next;
      });
    };

    const handleRouteDragEnd = (event: DragEndEvent) => {
      const { active, over } = event;

      if (!over || active.id === over.id) return;
      setRouteItems((prev) => {
        const activeKey = String(active.id);
        const overKey = String(over.id);
        const oldIndex = prev.findIndex((item) => item.key === activeKey);
        const newIndex = prev.findIndex((item) => item.key === overKey);

        if (oldIndex < 0 || newIndex < 0) return prev;
        const next = arrayMove(prev, oldIndex, newIndex);
        if (form.type === 2) {
          const last = next[next.length - 1];
          const externalMid = next.some(
            (item, idx) => item.type === "external" && idx !== next.length - 1,
          );

          if (!last || !last.isExit) {
            toast.error("末端必须是出口节点");
            return prev;
          }
          if (externalMid) {
            toast.error("外部出口只能放在末端");
            return prev;
          }
        }
        return next;
      });
    };

    const validateForm = (): boolean => {
      const newErrors: { [key: string]: string } = {};

      if (!form.name.trim()) {
        newErrors.name = "请输入隧道名称";
      } else if (form.name.length < 2 || form.name.length > 50) {
        newErrors.name = "隧道名称长度应在2-50个字符之间";
      }

      if (routeItems.length === 0) {
        newErrors.route = "请选择线路节点";
      } else {
        const entry = routeItems[0];
        if (!entry || entry.type !== "node") {
          newErrors.route = "入口必须是面板节点";
        }
      }

      if (!form.tcpListenAddr.trim()) {
        newErrors.tcpListenAddr = "请输入TCP监听地址";
      }
      if (!form.udpListenAddr.trim()) {
        newErrors.udpListenAddr = "请输入UDP监听地址";
      }
      if (form.trafficRatio < 0.0 || form.trafficRatio > 100.0) {
        newErrors.trafficRatio = "流量倍率应在0-100之间";
      }

      if (form.type === 2) {
        if (routeItems.length < 2) {
          newErrors.route = "隧道转发需要至少入口与出口";
        } else {
          const entry = routeItems[0];
          const last = routeItems[routeItems.length - 1];
          const externalMid = routeItems.some(
            (item, idx) => item.type === "external" && idx !== routeItems.length - 1,
          );

          if (!last || !last.isExit) {
            newErrors.route = "最后一个节点必须是出口节点";
          } else if (
            entry &&
            last &&
            entry.type === "node" &&
            last.type === "node" &&
            entry.id === last.id
          ) {
            newErrors.route = "隧道转发模式下，入口和出口不能是同一个节点";
          } else if (externalMid) {
            newErrors.route = "外部出口只能放在末端";
          }
        }

        if (!form.protocol) {
          newErrors.protocol = "请选择协议类型";
        }
      }

      setErrors(newErrors);

      return Object.keys(newErrors).length === 0;
    };

    const fetchNodeIfaces = async (nodeId: number) => {
      if (!nodeId) return [] as string[];
      if (ifaceCache[nodeId]) return ifaceCache[nodeId];
      try {
        const r: any = await getNodeInterfaces(nodeId);
        const ips =
          r.code === 0 && Array.isArray(r.data?.ips)
            ? (r.data.ips as string[])
            : [];

        setIfaceCache((prev) => ({ ...prev, [nodeId]: ips }));

        return ips;
      } catch {
        return [];
      }
    };

    const handleTypeChange = (type: number) => {
      setForm((prev) => ({
        ...prev,
        type,
        outNodeId: type === 1 ? null : prev.outNodeId,
        outExitId: type === 1 ? null : prev.outExitId,
        protocol: type === 1 ? "tls" : prev.protocol,
      }));
      if (type === 1) {
        setRouteItems((prev) => prev.slice(0, 1));
      }
      setExitDeployed("");
    };

    const handleSubmit = async () => {
      if (!validateForm()) return;

      setSubmitLoading(true);
      try {
        const data = { ...form };
        const response = isEdit
          ? await updateTunnel(data)
          : await createTunnel(data);

        if (response.code === 0) {
          try {
            const {
              setTunnelPath,
              getTunnelList,
              setTunnelIface,
              setTunnelBind,
            } = await import("@/api");
            let tid = isEdit ? form.id : undefined;

            if (!tid) {
              const lr: any = await getTunnelList();

              if (lr && lr.code === 0 && Array.isArray(lr.data)) {
                const candidates = (lr.data as any[]).filter(
                  (x) => x.name === form.name && x.inNodeId === form.inNodeId,
                );

                tid =
                  candidates.length > 0
                    ? candidates.sort((a, b) => (b.id || 0) - (a.id || 0))[0]
                        .id
                    : undefined;
              }
            }
            if (tid) {
              await setTunnelPath(tid as number, midPath);
              const ifaces: Array<{ nodeId: number; ip: string }> = [];

              if (form.inNodeId)
                ifaces.push({ nodeId: form.inNodeId, ip: entryIface || "" });
              midPath.forEach((nid) => {
                ifaces.push({ nodeId: nid, ip: midIfaces[nid] || "" });
              });
              if (ifaces.length > 0)
                await setTunnelIface(tid as number, ifaces);
              const binds: Array<{ nodeId: number; ip: string }> = [];

              midPath.forEach((nid) => {
                binds.push({ nodeId: nid, ip: midBindIps[nid] || "" });
              });
              if (form.type === 2 && form.outNodeId)
                binds.push({ nodeId: form.outNodeId, ip: exitBindIp || "" });
              if (binds.length > 0)
                await setTunnelBind(tid as number, binds);
            }
          } catch {}
          toast.success(isEdit ? "更新成功" : "创建成功");
          onOpenChange(false);
          onSaved();
        } else {
          toast.error(response.msg || (isEdit ? "更新失败" : "创建失败"));
        }
      } catch (error) {
        console.error("提交失败:", error);
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
        onOpenChange={onOpenChange}
      >
        <ModalContent>
          {(onClose) => (
            <>
              <ModalHeader className="flex flex-col gap-1">
                <h2 className="text-xl font-bold">
                  {isEdit ? "编辑隧道" : "新增隧道"}
                </h2>
                <p className="text-small text-default-500">
                  {isEdit ? "修改现有隧道配置的信息" : "创建新的隧道配置"}
                </p>
              </ModalHeader>
              <ModalBody>
                <div className="space-y-4">
                  <Input
                    errorMessage={errors.name}
                    isInvalid={!!errors.name}
                    label="隧道名称"
                    placeholder="请输入隧道名称"
                    value={form.name}
                    variant="bordered"
                    onChange={(e) =>
                      setForm((prev) => ({ ...prev, name: e.target.value }))
                    }
                  />

                  <Select
                    errorMessage={errors.type}
                    isDisabled={isEdit}
                    isInvalid={!!errors.type}
                    label="隧道类型"
                    placeholder="请选择隧道类型"
                    selectedKeys={[form.type.toString()]}
                    variant="bordered"
                    onSelectionChange={(keys) => {
                      const selectedKey = Array.from(keys)[0] as string;

                      if (selectedKey) {
                        handleTypeChange(parseInt(selectedKey));
                      }
                    }}
                  >
                    <SelectItem key="1">端口转发</SelectItem>
                    <SelectItem key="2">隧道转发</SelectItem>
                  </Select>

                  <div className="grid grid-cols-1 md:grid-cols-2 gap-4">
                    <Select
                      errorMessage={errors.flow}
                      isInvalid={!!errors.flow}
                      label="流量计算"
                      placeholder="请选择流量计算方式"
                      selectedKeys={[form.flow.toString()]}
                      variant="bordered"
                      onSelectionChange={(keys) => {
                        const selectedKey = Array.from(keys)[0] as string;

                        if (selectedKey) {
                          setForm((prev) => ({
                            ...prev,
                            flow: parseInt(selectedKey),
                          }));
                        }
                      }}
                    >
                      <SelectItem key="1">单向计算（仅上传）</SelectItem>
                      <SelectItem key="2">双向计算（上传+下载）</SelectItem>
                    </Select>

                    <Input
                      endContent={
                        <div className="pointer-events-none flex items-center">
                          <span className="text-default-400 text-small">x</span>
                        </div>
                      }
                      errorMessage={errors.trafficRatio}
                      isInvalid={!!errors.trafficRatio}
                      label="流量倍率"
                      placeholder="请输入流量倍率"
                      type="number"
                      value={form.trafficRatio.toString()}
                      variant="bordered"
                      onChange={(e) =>
                        setForm((prev) => ({
                          ...prev,
                          trafficRatio: parseFloat(e.target.value) || 0,
                        }))
                      }
                    />
                  </div>

                  <Divider />
                  <h3 className="text-lg font-semibold">线路编排</h3>
                  <div className="text-sm text-default-500">
                    点击节点卡片加入线路，拖动下方顺序。末端必须为出口节点。
                  </div>
                  {errors.route ? (
                    <div className="text-xs text-danger-500">{errors.route}</div>
                  ) : null}
                  <div className="grid grid-cols-1 md:grid-cols-2 xl:grid-cols-3 gap-3">
                    {nodes.map((node) => {
                      const selected = routeItems.some(
                        (item) => item.type === "node" && item.id === node.id,
                      );
                      const isExit = exitNodeIdSet.has(node.id);

                      return (
                        <button
                          key={node.id}
                          className={`w-full text-left rounded-lg border p-3 transition ${
                            selected
                              ? "border-primary-500 bg-primary-50"
                              : "border-default-200"
                          }`}
                          type="button"
                          onClick={() => toggleNodeRoute(node)}
                        >
                          <div className="flex items-center justify-between">
                            <div className="font-medium">{node.name}</div>
                            <Chip
                              size="sm"
                              variant="flat"
                              color={node.status === 1 ? "success" : "danger"}
                            >
                              {node.status === 1 ? "在线" : "离线"}
                            </Chip>
                          </div>
                          <div className="text-xs text-default-500 mt-1">
                            节点 ID：{node.id}
                          </div>
                          <div className="flex items-center gap-2 mt-2">
                            {isExit ? (
                              <Chip size="sm" variant="flat" color="primary">
                                出口
                              </Chip>
                            ) : null}
                            {selected ? (
                              <Chip size="sm" variant="flat" color="success">
                                已选
                              </Chip>
                            ) : null}
                          </div>
                        </button>
                      );
                    })}
                  </div>

                  {externalExitNodes.length > 0 ? (
                    <div className="space-y-2">
                      <div className="text-sm font-medium">外部出口节点</div>
                      <div className="flex flex-wrap gap-2">
                        {externalExitNodes.map((ext) => {
                          const active = routeItems.some(
                            (item) =>
                              item.type === "external" &&
                              item.id === ext.exitId,
                          );

                          return (
                            <Button
                              key={ext.exitId}
                              size="sm"
                              variant={active ? "solid" : "flat"}
                              color="warning"
                              onPress={() => toggleExternalExit(ext)}
                            >
                              {ext.name}
                            </Button>
                          );
                        })}
                      </div>
                    </div>
                  ) : null}

                  <div className="space-y-2">
                    <div className="text-sm font-medium">已选线路</div>
                    {routeItems.length === 0 ? (
                      <div className="text-xs text-default-500">
                        尚未选择节点
                      </div>
                    ) : (
                      <DndContext
                        collisionDetection={closestCenter}
                        sensors={sensors}
                        onDragEnd={handleRouteDragEnd}
                      >
                        <SortableContext
                          items={routeItems.map((item) => item.key)}
                          strategy={verticalListSortingStrategy}
                        >
                          <div className="space-y-2">
                            {routeItems.map((item) => (
                              <SortableRouteItem
                                key={item.key}
                                item={item}
                                onRemove={(key) =>
                                  setRouteItems((prev) =>
                                    prev.filter((it) => it.key !== key),
                                  )
                                }
                              />
                            ))}
                          </div>
                        </SortableContext>
                      </DndContext>
                    )}
                  </div>

                  {form.inNodeId ? (
                    <div className="p-3 np-soft flex items-center justify-between">
                      <div className="text-sm">
                        <div className="text-default-600">入口节点 API</div>
                        <div className="text-xs text-default-500 mt-1">
                          {entryApiOn === null
                            ? "检测中…"
                            : entryApiOn
                              ? "已启用，可直接下发服务"
                              : "未启用，需先开启后再保存/诊断"}
                        </div>
                      </div>
                      {entryApiOn === false && (
                        <Button
                          color="primary"
                          size="sm"
                          variant="flat"
                          onPress={async () => {
                            try {
                              await enableGostApi(form.inNodeId as number);
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

                  {form.type === 2 && (
                    <div className="grid grid-cols-1 md:grid-cols-3 gap-4">
                      <Input
                        label="出口端口(SS)"
                        placeholder="例如 10086"
                        type="number"
                        isDisabled={!!form.outExitId}
                        value={exitPort ? String(exitPort) : ""}
                        onChange={(e) =>
                          setExitPort(Number((e.target as any).value))
                        }
                      />
                      <Input
                        label="出口密码(SS)"
                        placeholder="不少于6位"
                        isDisabled={!!form.outExitId}
                        value={exitPassword}
                        onChange={(e) =>
                          setExitPassword((e.target as any).value)
                        }
                      />
                      <Select
                        label="加密方法"
                        description="选择 Shadowsocks 加密方法"
                        isDisabled={!!form.outExitId}
                        selectedKeys={[exitMethod]}
                        onSelectionChange={(keys) => {
                          const val = Array.from(keys as Set<string>)[0] as string;

                          if (val) setExitMethod(val);
                        }}
                      >
                        {EXIT_METHODS.map((m) => (
                          <SelectItem key={m} textValue={m}>
                            {m}
                          </SelectItem>
                        ))}
                      </Select>
                    </div>
                  )}

                  <div className="grid grid-cols-1 md:grid-cols-2 gap-4">
                    <Input
                      errorMessage={errors.tcpListenAddr}
                      isInvalid={!!errors.tcpListenAddr}
                      label="TCP监听地址"
                      placeholder="请输入TCP监听地址"
                      startContent={
                        <div className="pointer-events-none flex items-center">
                          <span className="text-default-400 text-small">TCP</span>
                        </div>
                      }
                      value={form.tcpListenAddr}
                      variant="bordered"
                      onChange={(e) =>
                        setForm((prev) => ({
                          ...prev,
                          tcpListenAddr: e.target.value,
                        }))
                      }
                    />

                    <Input
                      errorMessage={errors.udpListenAddr}
                      isInvalid={!!errors.udpListenAddr}
                      label="UDP监听地址"
                      placeholder="请输入UDP监听地址"
                      startContent={
                        <div className="pointer-events-none flex items-center">
                          <span className="text-default-400 text-small">UDP</span>
                        </div>
                      }
                      value={form.udpListenAddr}
                      variant="bordered"
                      onChange={(e) =>
                        setForm((prev) => ({
                          ...prev,
                          udpListenAddr: e.target.value,
                        }))
                      }
                    />
                  </div>

                  <div className="mt-2">
                    <h3 className="text-base font-semibold mb-1">多级路径</h3>
                    <div className="mb-2 text-sm">
                      <div className="flex items-center gap-2">
                        <span className="text-default-600">入口出站IP(接口)</span>
                        <Select
                          className="min-w-[320px] max-w-[380px]"
                          selectedKeys={entryIface ? [entryIface] : []}
                          size="sm"
                          onOpenChange={async () => {
                            await fetchNodeIfaces(form.inNodeId || 0);
                          }}
                          onSelectionChange={(keys) => {
                            const k = Array.from(keys)[0] as string;

                            setEntryIface(k || "");
                          }}
                        >
                          {(ifaceCache[form.inNodeId || 0] || []).map((ip) => (
                            <SelectItem key={ip}>{ip}</SelectItem>
                          ))}
                        </Select>
                      </div>
                    </div>
                    {midPath.length === 0 ? (
                      <div className="text-xs text-default-500">
                        未配置中间节点
                      </div>
                    ) : (
                      <div className="space-y-3">
                        {midPath.map((nid, idx) => {
                          const n = nodes.find((x) => x.id === nid);

                          return (
                            <div
                              key={nid}
                              className="w-full border border-dashed rounded-md p-3"
                            >
                              <div className="flex items-center justify-between mb-2">
                                <div className="font-medium">
                                  {idx + 1}. {n?.name || `节点${nid}`}
                                </div>
                                <div className="flex items-center gap-1">
                                  <Button
                                    color="danger"
                                    size="sm"
                                    variant="flat"
                                    onPress={() =>
                                      setRouteItems((prev) =>
                                        prev.filter(
                                          (item) =>
                                            !(
                                              item.type === "node" &&
                                              item.id === nid
                                            ),
                                        ),
                                      )
                                    }
                                  >
                                    移除
                                  </Button>
                                </div>
                              </div>
                              <div className="grid grid-cols-1 md:grid-cols-2 gap-3">
                                <Select
                                  aria-label="选择出站IP(接口)"
                                  className="min-w-[320px] max-w-[380px]"
                                  label="出站IP(接口)"
                                  selectedKeys={
                                    midIfaces[nid] ? [midIfaces[nid]] : []
                                  }
                                  size="sm"
                                  onOpenChange={async () => {
                                    await fetchNodeIfaces(nid);
                                  }}
                                  onSelectionChange={(keys) => {
                                    const k = Array.from(keys)[0] as string;

                                    setMidIfaces((prev) => ({
                                      ...prev,
                                      [nid]: k || "",
                                    }));
                                  }}
                                >
                                  {(ifaceCache[nid] || []).map((ip) => (
                                    <SelectItem key={ip}>{ip}</SelectItem>
                                  ))}
                                </Select>
                                <Select
                                  aria-label="选择监听IP(入站)"
                                  className="min-w-[320px] max-w-[380px]"
                                  label="监听IP(入站)"
                                  selectedKeys={
                                    midBindIps[nid] ? [midBindIps[nid]] : []
                                  }
                                  size="sm"
                                  onOpenChange={async () => {
                                    await fetchNodeIfaces(nid);
                                  }}
                                  onSelectionChange={(keys) => {
                                    const k = Array.from(keys)[0] as string;

                                    setMidBindIps((prev) => ({
                                      ...prev,
                                      [nid]: k || "",
                                    }));
                                  }}
                                >
                                  {(ifaceCache[nid] || []).map((ip) => (
                                    <SelectItem key={ip}>{ip}</SelectItem>
                                  ))}
                                </Select>
                              </div>
                            </div>
                          );
                        })}
                      </div>
                    )}
                    <div className="text-2xs text-default-400 mt-1">
                      说明：入口→中间节点→出口 逐级直转；端口转发和隧道转发均可配置路径和每节点出站/入站IP。
                    </div>
                  </div>

                  {form.type === 2 && form.outNodeId ? (
                    <div className="space-y-2">
                      <Select
                        className="min-w-[320px] max-w-[380px]"
                        label="出口监听IP"
                        placeholder="请选择出口监听IP"
                        selectedKeys={exitBindIp ? [exitBindIp] : []}
                        variant="bordered"
                        onOpenChange={async () => {
                          if (form.outNodeId) await fetchNodeIfaces(form.outNodeId);
                        }}
                        onSelectionChange={(keys) => {
                          const k = Array.from(keys)[0] as string;

                          setExitBindIp(k || "");
                        }}
                      >
                        {(ifaceCache[form.outNodeId || 0] || []).map((ip) => (
                          <SelectItem key={ip}>{ip}</SelectItem>
                        ))}
                      </Select>
                    </div>
                  ) : null}

                  {form.type === 2 && (
                    <>
                      <Divider />
                      <h3 className="text-lg font-semibold">出口配置</h3>

                      <Select
                        errorMessage={errors.protocol}
                        isInvalid={!!errors.protocol}
                        label="协议类型"
                        placeholder="请选择协议类型"
                        selectedKeys={[form.protocol]}
                        variant="bordered"
                        onSelectionChange={(keys) => {
                          const selectedKey = Array.from(keys)[0] as string;

                          if (selectedKey) {
                            setForm((prev) => ({
                              ...prev,
                              protocol: selectedKey,
                            }));
                          }
                        }}
                      >
                        <SelectItem key="tls">TLS</SelectItem>
                        <SelectItem key="wss">WSS</SelectItem>
                        <SelectItem key="tcp">TCP</SelectItem>
                        <SelectItem key="mtls">MTLS</SelectItem>
                        <SelectItem key="mwss">MWSS</SelectItem>
                      <SelectItem key="mtcp">MTCP</SelectItem>
                      </Select>

                      <div className="p-3 np-soft">
                        <div className="text-sm text-default-600">出口节点</div>
                        <div className="font-medium mt-1">
                          {selectedExitInfo?.name || "未选择"}
                        </div>
                        {selectedExitInfo?.host ? (
                          <div className="text-xs text-default-500 mt-1">
                            {selectedExitInfo.host}
                          </div>
                        ) : null}
                        {form.outExitId ? (
                          <div className="text-xs text-warning-600 mt-1">
                            外部出口仅用于连接，不支持下发配置
                          </div>
                        ) : null}
                      </div>

                      <div className="grid grid-cols-1 md:grid-cols-3 gap-4">
                        <Input
                          description="默认 console，可留空"
                          label="观察器(observer)"
                          value={exitObserver}
                          isDisabled={!!form.outExitId}
                          onChange={(e) =>
                            setExitObserver((e.target as any).value)
                          }
                        />
                        <Input
                          description="可选，需在节点注册对应限速器"
                          label="限速(limiter)"
                          value={exitLimiter}
                          isDisabled={!!form.outExitId}
                          onChange={(e) =>
                            setExitLimiter((e.target as any).value)
                          }
                        />
                        <Input
                          description="可选，需在节点注册对应限速器"
                          label="连接限速(rlimiter)"
                          value={exitRLimiter}
                          isDisabled={!!form.outExitId}
                          onChange={(e) =>
                            setExitRLimiter((e.target as any).value)
                          }
                        />
                      </div>
                      {exitDeployed && (
                        <Alert
                          color="success"
                          description={exitDeployed}
                          title="出口SS状态"
                          variant="flat"
                        />
                      )}
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
                        {exitMetaItems.map((it) => (
                          <div
                            key={it.id}
                            className="grid grid-cols-5 gap-2 items-center"
                          >
                            <Input
                              className="col-span-2"
                              placeholder="key"
                              value={it.key}
                              onChange={(e) =>
                                setExitMetaItems((prev) =>
                                  prev.map((x) =>
                                    x.id === it.id
                                      ? {
                                          ...x,
                                          key: (e.target as any).value,
                                        }
                                      : x,
                                  ),
                                )
                              }
                            />
                            <Input
                              className="col-span-3"
                              placeholder="value"
                              value={it.value}
                              onChange={(e) =>
                                setExitMetaItems((prev) =>
                                  prev.map((x) =>
                                    x.id === it.id
                                      ? {
                                          ...x,
                                          value: (e.target as any).value,
                                        }
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
                                setExitMetaItems((prev) =>
                                  prev.filter((x) => x.id !== it.id),
                                )
                              }
                            >
                              删除
                            </Button>
                          </div>
                        ))}
                      </div>
                    </>
                  )}

                  <Alert
                    className="mt-4"
                    color="primary"
                    description="V6或者双栈填写[::],V4填写0.0.0.0。不懂的就去看文档网站内的说明"
                    title="TCP,UDP监听地址"
                    variant="flat"
                  />
                  <Alert
                    className="mt-4"
                    color="primary"
                    description="用于多IP服务器指定使用那个IP和出口服务器通讯，不懂的默认为空就行"
                    title="出口网卡名或IP"
                    variant="flat"
                  />
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
                  {submitLoading
                    ? isEdit
                      ? "更新中..."
                      : "创建中..."
                    : isEdit
                      ? "更新"
                      : "创建"}
                </Button>
              </ModalFooter>
            </>
          )}
        </ModalContent>
      </Modal>
    );
  },
);

type TunnelCardGridProps = {
  tunnels: Tunnel[];
  nodes: Node[];
  exitNodes: ExitNodeItem[];
  onEdit: (tunnel: Tunnel) => void;
  onDiagnose: (tunnel: Tunnel) => void;
  onDelete: (tunnel: Tunnel) => void;
  onCheckPath: (tunnel: Tunnel) => void;
};

const TunnelCardGrid = memo(({
  tunnels,
  nodes,
  onEdit,
  onDiagnose,
  onDelete,
  onCheckPath,
  exitNodes,
}: TunnelCardGridProps) => {
  const nodeMap = useMemo(() => {
    const map = new Map<number, string>();

    nodes.forEach((n) => map.set(n.id, n.name));

    return map;
  }, [nodes]);

  const exitMap = useMemo(() => {
    const map = new Map<number, { name: string }>();

    exitNodes.forEach((n) => {
      if (n.source === "external" && n.exitId) {
        map.set(n.exitId, { name: n.name || `外部出口${n.exitId}` });
      }
    });

    return map;
  }, [exitNodes]);

  const getDisplayIp = (ipString?: string): string => {
    if (!ipString) return "-";

    const ips = ipString
      .split(",")
      .map((ip) => ip.trim())
      .filter((ip) => ip);

    if (ips.length === 0) return "-";
    if (ips.length === 1) return ips[0];

    return `${ips[0]} 等${ips.length}个`;
  };

  const getNodeName = (nodeId?: number): string => {
    if (!nodeId) return "-";

    return nodeMap.get(nodeId) || `节点${nodeId}`;
  };

  const getExitName = (tunnel: Tunnel): string => {
    if (tunnel.type === 1) return getNodeName(tunnel.inNodeId);
    if (tunnel.outExitId) {
      return exitMap.get(tunnel.outExitId)?.name || `外部出口${tunnel.outExitId}`;
    }
    return getNodeName(tunnel.outNodeId);
  };

  const getStatusDisplay = (status: number) => {
    switch (status) {
      case 1:
        return { text: "启用", color: "success" };
      case 0:
        return { text: "禁用", color: "default" };
      default:
        return { text: "未知", color: "warning" };
    }
  };

  const getTypeDisplay = (type: number) => {
    switch (type) {
      case 1:
        return { text: "端口转发", color: "primary" };
      case 2:
        return { text: "隧道转发", color: "secondary" };
      default:
        return { text: "未知", color: "default" };
    }
  };

  const getFlowDisplay = (flow: number) => {
    switch (flow) {
      case 1:
        return "单向计算";
      case 2:
        return "双向计算";
      default:
        return "未知";
    }
  };

  return (
    <VirtualGrid
      className="w-full"
      estimateRowHeight={360}
      items={tunnels}
      minItemWidth={320}
      renderItem={(tunnel) => {
        const statusDisplay = getStatusDisplay(tunnel.status);
        const typeDisplay = getTypeDisplay(tunnel.type);

        return (
          <Card
            key={tunnel.id}
            className="list-card hover:shadow-md transition-shadow duration-200"
          >
            <CardHeader className="pb-2">
              <div className="flex justify-between items-start w-full">
                <div className="flex-1 min-w-0">
                  <h3 className="font-semibold text-foreground truncate text-sm">
                    {tunnel.name}
                  </h3>
                  <div className="flex items-center gap-1.5 mt-1">
                    <Chip
                      className="text-xs"
                      color={typeDisplay.color as any}
                      size="sm"
                      variant="flat"
                    >
                      {typeDisplay.text}
                    </Chip>
                    <Chip
                      className="text-xs"
                      color={statusDisplay.color as any}
                      size="sm"
                      variant="flat"
                    >
                      {statusDisplay.text}
                    </Chip>
                  </div>
                </div>
              </div>
            </CardHeader>

            <CardBody className="pt-0 pb-3">
              <div className="space-y-2">
                <div className="space-y-1.5">
                  <div className="p-2 np-soft">
                    <div className="flex items-center justify-between mb-1">
                      <span className="text-xs font-medium text-default-600">
                        入口节点
                      </span>
                    </div>
                    <code className="text-xs font-mono text-foreground block truncate">
                      {getNodeName(tunnel.inNodeId)}
                    </code>
                    <code className="text-xs font-mono text-default-500 block truncate">
                      {getDisplayIp(tunnel.inIp)}
                    </code>
                  </div>

                  <div className="text-center py-0.5">
                    <svg
                      className="w-3 h-3 text-default-400 mx-auto"
                      fill="none"
                      stroke="currentColor"
                      viewBox="0 0 24 24"
                    >
                      <path
                        d="M19 14l-7 7m0 0l-7-7m7 7V3"
                        strokeLinecap="round"
                        strokeLinejoin="round"
                        strokeWidth={2}
                      />
                    </svg>
                  </div>

                  <div className="p-2 np-soft">
                    <div className="flex items-center justify-between mb-1">
                      <span className="text-xs font-medium text-default-600">
                        {tunnel.type === 1 ? "出口节点（同入口）" : "出口节点"}
                      </span>
                    </div>
                    <code className="text-xs font-mono text-foreground block truncate">
                      {getExitName(tunnel)}
                    </code>
                    <code className="text-xs font-mono text-default-500 block truncate">
                      {tunnel.type === 1
                        ? getDisplayIp(tunnel.inIp)
                        : getDisplayIp(tunnel.outIp)}
                    </code>
                  </div>
                </div>

                <div className="flex justify-between items-center pt-2 border-t border-divider">
                  <div className="text-left">
                    <div className="text-xs font-medium text-foreground">
                      {getFlowDisplay(tunnel.flow)}
                    </div>
                  </div>
                  <div className="text-right">
                    <div className="text-xs font-medium text-foreground">
                      {tunnel.trafficRatio}x
                    </div>
                  </div>
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
                  onPress={() => onEdit(tunnel)}
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
                  onPress={() => onDiagnose(tunnel)}
                >
                  诊断
                </Button>
                {tunnel.type === 2 && (
                  <Button
                    className="flex-1 min-h-8"
                    color="secondary"
                    size="sm"
                    variant="flat"
                    onPress={() => onCheckPath(tunnel)}
                  >
                    检查路径
                  </Button>
                )}
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
                  onPress={() => onDelete(tunnel)}
                >
                  删除
                </Button>
              </div>
            </CardBody>
          </Card>
        );
      }}
    />
  );
});

export default function TunnelPage() {
  const [loading, setLoading] = useState(true);
  const [tunnels, setTunnels] = useState<Tunnel[]>([]);
  const [nodes, setNodes] = useState<Node[]>([]);
  const [exitNodes, setExitNodes] = useState<ExitNodeItem[]>([]);
  // 操作日志弹窗（必须放在顶部，避免 Hooks 顺序变化）
  const [opsOpen, setOpsOpen] = useState(false);
  // 操作日志弹窗

  // 模态框状态
  const [modalOpen, setModalOpen] = useState(false);
  const [editTunnel, setEditTunnel] = useState<Tunnel | null>(null);
  const [deleteModalOpen, setDeleteModalOpen] = useState(false);
  const [diagnosisModalOpen, setDiagnosisModalOpen] = useState(false);
  const [diagReqId, setDiagReqId] = useState<string>("");
  const [deleteLoading, setDeleteLoading] = useState(false);
  const [diagnosisLoading, setDiagnosisLoading] = useState(false);
  const [tunnelToDelete, setTunnelToDelete] = useState<Tunnel | null>(null);
  const [currentDiagnosisTunnel, setCurrentDiagnosisTunnel] =
    useState<Tunnel | null>(null);
  const [diagnosisResult, setDiagnosisResult] =
    useState<DiagnosisResult | null>(null);

  useEffect(() => {
    loadData();
  }, []);

  // 加载所有数据
  const loadData = async () => {
    setLoading(true);
    try {
      const [tunnelsRes, nodesRes, exitRes] = await Promise.all([
        getTunnelList(),
        getNodeList(),
        getExitNodes(),
      ]);

      if (tunnelsRes.code === 0) {
        setTunnels(tunnelsRes.data || []);
      } else {
        toast.error(tunnelsRes.msg || "获取隧道列表失败");
      }

      if (nodesRes.code === 0) {
        setNodes(nodesRes.data || []);
      } else {
        console.warn("获取节点列表失败:", nodesRes.msg);
      }

      if (exitRes.code === 0) {
        setExitNodes(exitRes.data || []);
      }
    } catch (error) {
      console.error("加载数据失败:", error);
      toast.error("加载数据失败");
    } finally {
      setLoading(false);
    }
  };

  const handleAdd = () => {
    setEditTunnel(null);
    setModalOpen(true);
  };

  const handleEdit = useCallback((tunnel: Tunnel) => {
    setEditTunnel(tunnel);
    setModalOpen(true);
  }, []);

  const handleDelete = useCallback((tunnel: Tunnel) => {
    setTunnelToDelete(tunnel);
    setDeleteModalOpen(true);
  }, []);

  const confirmDelete = async () => {
    if (!tunnelToDelete) return;

    setDeleteLoading(true);
    try {
      const response = await deleteTunnel(tunnelToDelete.id);

      if (response.code === 0) {
        toast.success("删除成功");
        setDeleteModalOpen(false);
        setTunnelToDelete(null);
        loadData();
      } else {
        toast.error(response.msg || "删除失败");
      }
    } catch (error) {
      console.error("删除失败:", error);
      toast.error("删除失败");
    } finally {
      setDeleteLoading(false);
    }
  };

  // 诊断隧道
  const handleDiagnose = useCallback(async (tunnel: Tunnel) => {
    setCurrentDiagnosisTunnel(tunnel);
    setDiagnosisModalOpen(true);
    setDiagnosisLoading(true);
    setDiagReqId("");
    setDiagnosisResult({
      tunnelName: tunnel.name,
      tunnelType: tunnel.type === 1 ? "端口转发" : "隧道转发",
      timestamp: Date.now(),
      results: [],
    });

    // 流式增量：依次请求三个步骤
    const append = (item: any) => {
      setDiagnosisResult((prev) =>
        prev
          ? {
              ...prev,
              results: [...prev.results, item],
            }
          : prev,
      );
    };

    try {
      // 0) 入口到 1.1.1.1（ICMP）仅端口转发执行
      if (tunnel.type === 1) {
        const r1 = await diagnoseTunnelStep(tunnel.id, "entry");

        if (r1.code === 0) append(r1.data);
        else {
          append({
            success: false,
            description: "入口外网连通性 (ICMP 1.1.1.1)",
            nodeName: "-",
            nodeId: "-",
            targetIp: "1.1.1.1",
            message: r1.msg || "失败",
          });
        }
      }

      // 1) 逐跳ICMP（仅隧道转发）
      if (tunnel.type === 2) {
        const rp = await diagnoseTunnelStep(tunnel.id, "path");

        if (rp.code === 0) {
          if (rp.data && Array.isArray(rp.data.results))
            rp.data.results.forEach((it: any) => append(it));
          else
            append({
              success: false,
              description: "路径连通性(逐跳)",
              nodeName: "-",
              nodeId: "-",
              targetIp: "-",
              message: "无数据",
            });
        } else {
          append({
            success: false,
            description: "路径连通性(逐跳)",
            nodeName: "-",
            nodeId: "-",
            targetIp: "-",
            message: rp.msg || "失败",
          });
        }
      }

      // 2) 出口到 1.1.1.1（ICMP）仅隧道转发
      if (tunnel.type === 2) {
        const r3 = await diagnoseTunnelStep(tunnel.id, "exitPublic");

        if (r3.code === 0) append(r3.data);
        else {
          append({
            success: false,
            description: "出口外网连通性",
            nodeName: "-",
            nodeId: "-",
            targetIp: "1.1.1.1",
            message: r3.msg || "失败",
          });
        }
      }

      // 3) iperf3 反向带宽测试（仅隧道转发）
      if (tunnel.type === 2) {
        const r4 = await diagnoseTunnelStep(tunnel.id, "iperf3");

        if (r4.code === 0) {
          append(r4.data);
          const did =
            r4.data && (r4.data as any).diagId
              ? String((r4.data as any).diagId)
              : "";

          if (did) setDiagReqId(did);
        } else {
          // 若后端在失败时也返回了 diagId，则也记录以便聚合查看本次日志
          const did =
            r4.data && (r4.data as any).diagId
              ? String((r4.data as any).diagId)
              : "";

          if (did) setDiagReqId(did);
          append({
            success: false,
            description: "iperf3 反向带宽测试",
            nodeName: "-",
            nodeId: "-",
            targetIp: "-",
            message: r4.msg || "未支持或失败",
            ...(did ? { diagId: did } : {}),
          });
          if (did) setOpsOpen(true);
        }
      }
    } catch (e) {
      toast.error("诊断失败");
    } finally {
      setDiagnosisLoading(false);
    }
  }, []);

  const handleCheckPath = useCallback(async (tunnel: Tunnel) => {
    setDiagnosisLoading(true);
    try {
      const { checkTunnelPath } = await import("@/api");
      const r: any = await checkTunnelPath(tunnel.id);

      if (r.code === 0) {
        const bad = (r.data?.hops || []).filter(
          (h: any) => !h.online || (h.role === "mid" && !h.proposedPort),
        ).length;

        toast.success(
          `路径检查完成：${(r.data?.hops || []).length} 跳，异常 ${bad} 处`,
        );
        setDiagnosisResult({
          tunnelName: tunnel.name,
          tunnelType: "隧道转发",
          timestamp: Date.now(),
          results: (r.data?.hops || []).map((h: any) => ({
            success: h.online && (h.role !== "mid" || !!h.proposedPort),
            description: `节点(${h.role}) ${h.nodeName}`,
            nodeName: h.nodeName,
            nodeId: String(h.nodeId),
            targetIp: "-",
            message: `${h.online ? "在线" : "离线"}${h.relayGrpc ? " · 有relay(grpc)" : ""}${h.proposedPort ? ` · 建议端口 ${h.proposedPort}` : ""}`,
          })),
        });
        setCurrentDiagnosisTunnel(tunnel);
        setDiagnosisModalOpen(true);
        setDiagnosisLoading(false);
      } else {
        toast.error(r.msg || "检查失败");
      }
    } catch {
      toast.error("检查失败");
    } finally {
      setDiagnosisLoading(false);
    }
  }, []);

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

  if (loading) {
    return (
      <div className="np-page">
        <div className="flex justify-end">
          <div className="skeleton-line w-20" />
        </div>
        <div className="grid gap-4 grid-cols-1 sm:grid-cols-2 xl:grid-cols-3 2xl:grid-cols-4">
          {Array.from({ length: 8 }).map((_, idx) => (
            <div key={`tunnel-skel-${idx}`} className="skeleton-card" />
          ))}
        </div>
      </div>
    );
  }

  const suppressBackground =
    modalOpen || deleteModalOpen || diagnosisModalOpen || opsOpen;

  return (
    <div className="np-page">
      {/* 页面头部 */}
      <div className="np-page-header">
        <div>
          <h1 className="np-page-title">隧道管理</h1>
          <p className="np-page-desc">快速编排入口、出口与中继线路。</p>
        </div>
        <div className="flex items-center gap-2">
          <Button size="sm" variant="flat" onPress={() => setOpsOpen(true)}>
            操作日志
          </Button>
          <Button color="primary" size="sm" variant="flat" onPress={handleAdd}>
            新增
          </Button>
        </div>
      </div>

      {opsOpen ? (
        <OpsLogModal
          isOpen={opsOpen}
          requestId={diagReqId || undefined}
          onOpenChange={setOpsOpen}
        />
      ) : null}
      {/* 隧道卡片网格 */}
      {!suppressBackground && tunnels.length > 0 ? (
        <TunnelCardGrid
          exitNodes={exitNodes}
          nodes={nodes}
          onCheckPath={handleCheckPath}
          onDelete={handleDelete}
          onDiagnose={handleDiagnose}
          onEdit={handleEdit}
          tunnels={tunnels}
        />
      ) : !suppressBackground ? (
        /* 空状态 */
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
                    d="M8.111 16.404a5.5 5.5 0 017.778 0M12 20h.01m-7.08-7.071c3.904-3.905 10.236-3.905 14.141 0M1.394 9.393c5.857-5.857 15.355-5.857 21.213 0"
                    strokeLinecap="round"
                    strokeLinejoin="round"
                    strokeWidth={1.5}
                  />
                </svg>
              </div>
              <div>
                <h3 className="text-lg font-semibold text-foreground">
                  暂无隧道配置
                </h3>
                <p className="text-default-500 text-sm mt-1">
                  还没有创建任何隧道配置，点击上方按钮开始创建
                </p>
              </div>
            </div>
          </CardBody>
        </Card>
      ) : null}

      {/* 新增/编辑模态框 */}
      <TunnelEditModal
        editTunnel={editTunnel}
        isOpen={modalOpen}
        nodes={nodes}
        exitNodes={exitNodes}
        onOpenChange={setModalOpen}
        onSaved={loadData}
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
                <h2 className="text-xl font-bold">确认删除</h2>
              </ModalHeader>
              <ModalBody>
                {/* 诊断前的入口 API 提示 */}
                {currentDiagnosisTunnel && (
                  <div className="mb-3 p-3 np-soft flex items-center justify-between">
                    <div className="text-sm">
                      <div className="text-default-600">入口节点 API</div>
                      <div className="text-xs text-default-500 mt-1">
                        {(() => {
                          const n: any = nodes.find(
                            (nn) =>
                              Number(nn.id) ===
                              Number(currentDiagnosisTunnel.inNodeId),
                          );
                          const on =
                            typeof (n as any)?.gostApi !== "undefined"
                              ? (n as any).gostApi === 1
                              : null;

                          return on === null
                            ? "检测中…"
                            : on
                              ? "已启用，可直接进行诊断"
                              : "未启用，可能无法下发临时服务";
                        })()}
                      </div>
                    </div>
                    {(() => {
                      const n: any = nodes.find(
                        (nn) =>
                          Number(nn.id) ===
                          Number(currentDiagnosisTunnel.inNodeId),
                      );
                      const on =
                        typeof (n as any)?.gostApi !== "undefined"
                          ? (n as any).gostApi === 1
                          : null;

                      return on === false ? (
                        <Button
                          color="primary"
                          size="sm"
                          variant="flat"
                          onPress={async () => {
                            try {
                              await enableGostApi(
                                currentDiagnosisTunnel.inNodeId,
                              );
                              toast.success("已发送开启 GOST API 指令");
                            } catch (e: any) {
                              toast.error(e?.message || "发送失败");
                            }
                          }}
                        >
                          开启 GOST API
                        </Button>
                      ) : null;
                    })()}
                  </div>
                )}
                <p>
                  确定要删除隧道 <strong>"{tunnelToDelete?.name}"</strong> 吗？
                </p>
                <p className="text-small text-default-500">
                  此操作不可恢复，请谨慎操作。
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
                  {deleteLoading ? "删除中..." : "确认删除"}
                </Button>
              </ModalFooter>
            </>
          )}
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
                <h2 className="text-xl font-bold">隧道诊断结果</h2>
                {currentDiagnosisTunnel && (
                  <div className="flex items-center gap-2">
                    <span className="text-small text-default-500">
                      {currentDiagnosisTunnel.name}
                    </span>
                    <Chip
                      color={
                        currentDiagnosisTunnel.type === 1
                          ? "primary"
                          : "secondary"
                      }
                      size="sm"
                      variant="flat"
                    >
                      {currentDiagnosisTunnel.type === 1
                        ? "端口转发"
                        : "隧道转发"}
                    </Chip>
                  </div>
                )}
              </ModalHeader>
              <ModalBody>
                {diagnosisLoading ? (
                  <div className="flex items-center justify-center py-16">
                    <div className="flex items-center gap-3">
                      <Spinner size="sm" />
                      <span className="text-default-600">正在诊断...</span>
                    </div>
                  </div>
                ) : diagnosisResult ? (
                  <div className="space-y-4">
                    {diagnosisResult.results.map((result, index) => {
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
                              <div className="flex items-center gap-3">
                                <div
                                  className={`w-8 h-8 rounded-full flex items-center justify-center ${
                                    result.success
                                      ? "bg-success text-white"
                                      : "bg-danger text-white"
                                  }`}
                                >
                                  {result.success ? "✓" : "✗"}
                                </div>
                                <div>
                                  <h4 className="font-semibold">
                                    {result.description}
                                  </h4>
                                  <p className="text-small text-default-500">
                                    {result.nodeName}
                                  </p>
                                </div>
                              </div>
                              <Chip
                                color={result.success ? "success" : "danger"}
                                variant="flat"
                              >
                                {result.success ? "成功" : "失败"}
                              </Chip>
                            </div>
                          </CardHeader>
                          <CardBody className="pt-0">
                            {result.success ? (
                              typeof result.bandwidthMbps === "number" ? (
                                <div className="space-y-3">
                                  <div className="grid grid-cols-3 gap-4">
                                    <div className="text-center">
                                      <div className="text-2xl font-bold text-primary">
                                        {Number(result.bandwidthMbps).toFixed(
                                          2,
                                        )}
                                      </div>
                                      <div className="text-small text-default-500">
                                        带宽(Mbps)
                                      </div>
                                    </div>
                                  </div>
                                  <div className="text-small text-default-500">
                                    目标地址:{" "}
                                    <code className="font-mono">
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
                                  {(result as any).diagId && (
                                    <div className="text-small text-default-400">
                                      diagId:{" "}
                                      <code className="font-mono">
                                        {(result as any).diagId}
                                      </code>
                                    </div>
                                  )}
                                </div>
                              ) : (
                                <div className="space-y-3">
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
                                  <div className="text-small text-default-500">
                                    目标地址:{" "}
                                    <code className="font-mono">
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
                                  {(result as any).diagId && (
                                    <div className="text-small text-default-400">
                                      diagId:{" "}
                                      <code className="font-mono">
                                        {(result as any).diagId}
                                      </code>
                                    </div>
                                  )}
                                </div>
                              )
                            ) : (
                              <div className="space-y-2">
                                <div className="text-small text-default-500">
                                  目标地址:{" "}
                                  <code className="font-mono">
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
                    })}
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
                <Button variant="flat" onPress={() => setOpsOpen(true)}>
                  诊断日志
                </Button>
                {currentDiagnosisTunnel && (
                  <Button
                    color="primary"
                    isLoading={diagnosisLoading}
                    onPress={() => handleDiagnose(currentDiagnosisTunnel)}
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

// (exit IP picker now unified as Select in form; standalone IfacePicker removed)
