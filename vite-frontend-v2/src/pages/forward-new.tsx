import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { useNavigate, useSearchParams } from "react-router-dom";
import { Card, CardBody, CardHeader } from "@heroui/card";
import { Button } from "@heroui/button";
import { Chip } from "@heroui/chip";
import { Spinner } from "@heroui/spinner";
import { Input } from "@heroui/input";
import { Select, SelectItem } from "@heroui/select";
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

import {
  createForward,
  createTunnel,
  getExitNode,
  getExitNodes,
  getForwardList,
  getForwardStatusDetail,
  getNodeInterfaces,
  getNodeList,
  setExitNode,
  getTunnelBind,
  getTunnelById,
  getTunnelPath,
  getTunnelList,
  setTunnelBind,
  setTunnelPath,
  updateTunnel,
  updateForward,
} from "@/api";

type RouteItem = {
  id: string;
  type: "node" | "external";
  nodeId?: number;
  exitId?: number;
  protocol?: string;
  bindIp?: string;
  exitIp?: string;
  port?: number | null;
  ssPort?: number;
  anytlsPort?: number;
  exitPort?: number;
  label: string;
  subLabel?: string;
};
type LinkMode = "direct" | "tunnel";

const SortableRouteItem = ({
  item,
  index,
  isExit,
  extraLines,
  onRemove,
}: {
  item: RouteItem;
  index: number;
  isExit: boolean;
  extraLines?: string[];
  onRemove: (id: string) => void;
}) => {
  const { attributes, listeners, setNodeRef, transform, transition, isDragging } =
    useSortable({ id: item.id });
  const style = {
    transform: transform ? CSS.Transform.toString(transform) : undefined,
    transition: transition || undefined,
    opacity: isDragging ? 0.6 : 1,
  };

  return (
    <div
      ref={setNodeRef}
      className="np-soft px-3 py-2 flex items-center justify-between gap-3"
      style={style}
      {...attributes}
    >
      <div className="flex items-center gap-2 min-w-0">
        <span className="text-xs text-default-400 w-5 text-right">
          {index + 1}
        </span>
        <span className="text-default-400 cursor-grab select-none" {...listeners}>
          ⋮⋮
        </span>
        <div className="min-w-0">
          <div className="text-sm font-medium break-words">{item.label}</div>
          {item.subLabel ? (
            <div className="text-2xs text-default-400 break-words">
              {item.subLabel}
            </div>
          ) : null}
          {extraLines && extraLines.length ? (
            <div className="mt-1 space-y-0.5">
              {extraLines.map((line, idx) => (
                <div key={idx} className="text-2xs text-default-600">
                  {line}
                </div>
              ))}
            </div>
          ) : null}
        </div>
        <Chip
          className="ml-1"
          color={isExit ? "success" : "default"}
          size="sm"
          variant="flat"
        >
          {item.type === "external" ? "外部出口" : isExit ? "出口" : "节点"}
        </Chip>
        {item.protocol ? (
          <Chip color="warning" size="sm" variant="flat">
            {item.protocol.toUpperCase()}
          </Chip>
        ) : null}
      </div>
      <Button size="sm" variant="light" onPress={() => onRemove(item.id)}>
        移除
      </Button>
    </div>
  );
};

export default function ForwardNewPage() {
  const navigate = useNavigate();
  const [searchParams] = useSearchParams();
  const editId = Number(searchParams.get("id") || 0);
  const isEdit = editId > 0;
  const editLoadedRef = useRef(false);
  const [loading, setLoading] = useState(true);
  const [nodes, setNodes] = useState<any[]>([]);
  const [exitNodes, setExitNodes] = useState<any[]>([]);
  const [routeItems, setRouteItems] = useState<RouteItem[]>([]);
  const [linkModes, setLinkModes] = useState<LinkMode[]>([]);
  const [forwardName, setForwardName] = useState("");
  const [forwardGroups, setForwardGroups] = useState<string[]>([]);
  const [forwardGroupInput, setForwardGroupInput] = useState("");
  const [groupOptions, setGroupOptions] = useState<string[]>([]);
  const [groupDropdownOpen, setGroupDropdownOpen] = useState(false);
  const groupInputRef = useRef<HTMLInputElement | null>(null);
  const groupBoxRef = useRef<HTMLDivElement | null>(null);
  const [submitting, setSubmitting] = useState(false);
  const [editForward, setEditForward] = useState<any | null>(null);
  const [editTunnel, setEditTunnel] = useState<any | null>(null);
  const [editLoading, setEditLoading] = useState(false);
  const [ifaceMap, setIfaceMap] = useState<Record<number, string[]>>({});
  const [ifaceLoading, setIfaceLoading] = useState<Record<number, boolean>>({});
  const [portErrors, setPortErrors] = useState<Record<string, string>>({});
  const prevRouteRef = useRef<RouteItem[]>([]);
  const prevLinkModesRef = useRef<LinkMode[]>([]);

  const filteredGroupOptions = useMemo(() => {
    const input = forwardGroupInput.trim().toLowerCase();
    return groupOptions.filter((g) => {
      if (forwardGroups.includes(g)) return false;
      if (!input) return true;
      return g.toLowerCase().includes(input);
    });
  }, [groupOptions, forwardGroupInput, forwardGroups]);

  useEffect(() => {
    if (!groupDropdownOpen) return;
    const handler = (e: MouseEvent) => {
      if (!groupBoxRef.current?.contains(e.target as Node)) {
        setGroupDropdownOpen(false);
      }
    };
    document.addEventListener("mousedown", handler);
    return () => document.removeEventListener("mousedown", handler);
  }, [groupDropdownOpen]);

  const usedPortsMap = useMemo(() => {
    const map = new Map<number, Set<number>>();
    (nodes || []).forEach((node: any) => {
      const nodeId = Number(node?.id || 0);
      if (!nodeId) return;
      const used = new Set<number>();
      if (Array.isArray(node?.usedPorts)) {
        node.usedPorts.forEach((p: any) => {
          const n = Number(p);
          if (n > 0) used.add(n);
        });
      }
      map.set(nodeId, used);
    });
    return map;
  }, [nodes]);

  const getNodePortRange = useCallback(
    (nodeId?: number) => {
      const node = nodes.find(
        (n: any) => Number(n?.id || 0) === Number(nodeId || 0),
      );
      const minP = Number(node?.portSta || 10000);
      const maxP = Number(node?.portEnd || 65535);
      return {
        min: minP > 0 ? minP : 10000,
        max: maxP > 0 ? maxP : 65535,
      };
    },
    [nodes],
  );

  const setLinkModeAt = useCallback(
    (index: number, mode: LinkMode) => {
      setLinkModes((prev) => {
        const next = [...prev];
        if (index >= 0 && index < next.length) {
          next[index] = mode;
        }
        prevLinkModesRef.current = next;
        prevRouteRef.current = routeItems;
        return next;
      });
    },
    [routeItems],
  );

  useEffect(() => {
    const prev = prevRouteRef.current;
    const prevModes = prevLinkModesRef.current;
    const nextLen = Math.max(routeItems.length - 1, 0);
    if (nextLen === 0) {
      if (linkModes.length !== 0) {
        setLinkModes([]);
      }
      prevRouteRef.current = routeItems;
      prevLinkModesRef.current = [];
      return;
    }
    const map = new Map<string, LinkMode>();
    prev.forEach((item, idx) => {
      if (idx < prev.length - 1) {
        const key = `${item.id}__${prev[idx + 1].id}`;
        map.set(key, prevModes[idx] || "direct");
      }
    });
    const nextModes: LinkMode[] = [];
    for (let i = 0; i < nextLen; i += 1) {
      const key = `${routeItems[i].id}__${routeItems[i + 1].id}`;
      nextModes.push(map.get(key) || "direct");
    }
    setLinkModes(nextModes);
    prevRouteRef.current = routeItems;
    prevLinkModesRef.current = nextModes;
  }, [routeItems]);

  const exitNodeIdSet = useMemo(() => {
    const set = new Set<number>();
    (exitNodes || []).forEach((item: any) => {
      if (item?.source === "node" && item?.nodeId != null) {
        set.add(Number(item.nodeId));
      }
    });
    return set;
  }, [exitNodes]);

  const exitNodeList = useMemo(
    () =>
      (exitNodes || []).filter(
        (item: any) => item?.source === "node" && item?.nodeId != null,
      ),
    [exitNodes],
  );

  const exitExternalList = useMemo(
    () =>
      (exitNodes || []).filter(
        (item: any) => item?.source === "external" && item?.exitId != null,
      ),
    [exitNodes],
  );

  useEffect(() => {
    (async () => {
      const scanExitNodes = async (list: any[]) => {
        const results: any[] = [];
        for (const node of list) {
          const nodeId = Number(node?.id || 0);
          if (!nodeId) continue;
          const [ssRes, anyRes]: any[] = await Promise.all([
            getExitNode(nodeId, "ss").catch(() => null),
            getExitNode(nodeId, "anytls").catch(() => null),
          ]);
          const ssData = ssRes && ssRes.code === 0 ? ssRes.data : null;
          const anyData = anyRes && anyRes.code === 0 ? anyRes.data : null;
          if (!ssData && !anyData) continue;
          results.push({
            source: "node",
            nodeId,
            name: node?.name || `节点${nodeId}`,
            host: node?.serverIp || node?.ip || "",
            online: node?.status === 1,
            ssPort: ssData?.port,
            anytlsPort: anyData?.port,
            anytlsPassword: anyData?.password,
            anytlsExitIp: anyData?.exitIp,
          });
        }
        return results;
      };

      try {
        const [nRes, eRes, fRes]: any = await Promise.all([
          getNodeList(),
          getExitNodes().catch(() => null),
          getForwardList().catch(() => null),
        ]);
        if (nRes?.code === 0 && Array.isArray(nRes.data)) {
          setNodes(nRes.data);
        }
        if (eRes?.code === 0 && Array.isArray(eRes.data)) {
          setExitNodes(eRes.data);
        } else if (nRes?.code === 0 && Array.isArray(nRes.data)) {
          const fallback = await scanExitNodes(nRes.data);
          setExitNodes(fallback);
        }
        if (fRes?.code === 0 && Array.isArray(fRes.data)) {
          const opts = new Set<string>();
          (fRes.data as any[]).forEach((f) => {
            const raw = String(f?.group || "");
            raw
              .split(/[，,;/|]+/)
              .map((g) => g.trim())
              .filter((g) => g)
              .forEach((g) => opts.add(g));
          });
          setGroupOptions(Array.from(opts).sort());
        }
      } catch {
        toast.error("加载节点失败");
      } finally {
        setLoading(false);
      }
    })();
  }, []);

  const isExitItem = useCallback(
    (item?: RouteItem | null) => {
      if (!item) return false;
      if (item.type === "external") return true;
      return !!item.nodeId && exitNodeIdSet.has(item.nodeId);
    },
    [exitNodeIdSet],
  );

  const getRecommendedPort = useCallback(
    (nodeId?: number) => {
      if (!nodeId) return null;
      const node = nodes.find((n: any) => Number(n?.id || 0) === Number(nodeId));
      const minP = Number(node?.portSta || 10000);
      const maxP = Number(node?.portEnd || 65535);
      const used = usedPortsMap.get(Number(nodeId)) || new Set<number>();
      for (let port = minP; port <= maxP; port += 1) {
        if (!used.has(port)) return port;
      }
      return minP > 0 ? minP : 10000;
    },
    [nodes, usedPortsMap],
  );

  const getNodeName = useCallback(
    (nodeId?: number | null) => {
      if (!nodeId) return "-";
      const node = nodes.find(
        (n: any) => Number(n?.id || 0) === Number(nodeId),
      );
      return node?.name || `节点${nodeId}`;
    },
    [nodes],
  );

  const buildNodeRouteItem = useCallback((node: any, protocol?: string, port?: number | null, exitIp?: string) => {
    const nodeId = Number(node?.id || 0);
    return {
      id: `node-${nodeId}`,
      type: "node",
      nodeId,
      protocol,
      port: port ?? null,
      exitIp,
      label: node?.name || `节点${nodeId}`,
      subLabel: node?.serverIp || node?.ip || "",
    } as RouteItem;
  }, []);

  const buildExternalRouteItem = useCallback((item: any, protocol?: string) => {
    const exitId = Number(item?.exitId || item?.id || 0);
    const host = item?.host ? String(item.host) : "";
    const port = item?.port ? String(item.port) : "";
    return {
      id: `exit-${exitId}`,
      type: "external",
      exitId,
      protocol,
      exitPort: item?.port ? Number(item.port) : undefined,
      label: item?.name || `外部出口${exitId}`,
      subLabel: host && port ? `${host}:${port}` : host || "",
    } as RouteItem;
  }, []);

  const loadEditData = useCallback(async () => {
    if (!isEdit || editLoadedRef.current) return;
    setEditLoading(true);
    try {
      const fr: any = await getForwardList();
      const forward =
        fr && fr.code === 0 && Array.isArray(fr.data)
          ? (fr.data as any[]).find((f) => Number(f?.id || 0) === editId)
          : null;
      if (!forward) {
        toast.error("未找到需要编辑的转发");
        navigate("/forward");
        return;
      }
      setEditForward(forward);
      setForwardName(String(forward?.name || ""));
      {
        const rawGroups = String(forward?.group || "");
        const parsed = rawGroups
          .split(/[，,;/|]+/)
          .map((g) => g.trim())
          .filter((g) => g);
        setForwardGroups(Array.from(new Set(parsed)));
        setForwardGroupInput("");
      }

      const tr: any = await getTunnelById(Number(forward?.tunnelId || 0));
      if (!tr || tr.code !== 0 || !tr.data) {
        toast.error(tr?.msg || "未找到关联的线路");
        navigate("/forward");
        return;
      }
      const tunnel = tr.data;
      setEditTunnel(tunnel);

      const [pathRes, bindRes, detailRes] = await Promise.all([
        getTunnelPath(Number(tunnel.id)).catch(() => null),
        getTunnelBind(Number(tunnel.id)).catch(() => null),
        getForwardStatusDetail(editId).catch(() => null),
      ]);
      const path =
        pathRes && pathRes.code === 0 && Array.isArray(pathRes.data?.path)
          ? (pathRes.data.path as number[])
          : [];
      const linkModesFromApi =
        pathRes && pathRes.code === 0 && Array.isArray(pathRes.data?.linkModes)
          ? (pathRes.data.linkModes as string[])
          : [];
      const bindMap: Record<number, string> = {};
      if (bindRes && bindRes.code === 0 && Array.isArray(bindRes.data?.binds)) {
        bindRes.data.binds.forEach((x: any) => {
          if (x?.nodeId) bindMap[Number(x.nodeId)] = String(x.ip || "");
        });
      }
      const midPorts: Record<number, number | null> = {};
      if (detailRes && detailRes.code === 0 && Array.isArray(detailRes.data?.nodes)) {
        const mids = (detailRes.data.nodes as any[]).filter(
          (n) => n?.role === "mid",
        );
        mids.forEach((n: any, idx: number) => {
          const raw = n?.expectedPort ?? n?.actualPort ?? null;
          midPorts[idx] = raw != null ? Number(raw) : null;
        });
      }

      const entryNode = nodes.find(
        (n: any) => Number(n?.id || 0) === Number(tunnel?.inNodeId || 0),
      );
      if (!entryNode) {
        toast.error("入口节点不存在");
        navigate("/forward");
        return;
      }
      const entryItem = buildNodeRouteItem(
        entryNode,
        undefined,
        Number(forward?.inPort || 0) || null,
      );

      const exitProtocol = tunnel?.protocol
        ? String(tunnel.protocol).toLowerCase()
        : undefined;
      let exitItem: RouteItem | null = null;
      if (tunnel?.outExitId) {
        const ext = (exitNodes || []).find(
          (x: any) =>
            x?.source === "external" &&
            Number(x?.exitId || 0) === Number(tunnel.outExitId),
        );
        if (ext) {
          exitItem = buildExternalRouteItem(ext, exitProtocol || ext?.protocol);
        }
      } else if (tunnel?.outNodeId) {
        const outNode = nodes.find(
          (n: any) => Number(n?.id || 0) === Number(tunnel.outNodeId),
        );
        const exitInfo = (exitNodes || []).find(
          (x: any) =>
            x?.source === "node" &&
            Number(x?.nodeId || 0) === Number(tunnel.outNodeId),
        );
        if (outNode) {
          exitItem = {
            ...buildNodeRouteItem(
              outNode,
              exitProtocol,
              undefined,
              exitProtocol === "anytls" ? exitInfo?.anytlsExitIp : undefined,
            ),
            ssPort: exitInfo?.ssPort,
            anytlsPort: exitInfo?.anytlsPort,
          };
          const exitPort =
            exitProtocol === "anytls" ? exitItem.anytlsPort : exitItem.ssPort;
          exitItem.port = exitPort != null ? Number(exitPort) : null;
        }
      }

      const route: RouteItem[] = [];
      const sameEntryExit =
        tunnel?.outNodeId &&
        Number(tunnel.outNodeId) === Number(tunnel.inNodeId) &&
        !tunnel?.outExitId &&
        path.length === 0;
      if (sameEntryExit && exitItem && exitItem.type === "node") {
        exitItem.port =
          Number(forward?.inPort || 0) || getRecommendedPort(exitItem.nodeId);
        route.push(exitItem);
      } else {
        route.push(entryItem);
        path.forEach((nid, idx) => {
          const midNode = nodes.find(
            (n: any) => Number(n?.id || 0) === Number(nid),
          );
          if (!midNode) return;
          const item = buildNodeRouteItem(
            midNode,
            undefined,
            midPorts[idx] ?? null,
          );
          if (bindMap[nid]) item.bindIp = bindMap[nid];
          route.push(item);
        });
        if (exitItem) {
          if (exitItem.type === "node" && bindMap[Number(exitItem.nodeId || 0)]) {
            exitItem.bindIp = bindMap[Number(exitItem.nodeId || 0)];
          }
          route.push(exitItem);
        }
      }
      setRouteItems(route);
      const expectedLen = Math.max(route.length - 1, 0);
      const initMode: LinkMode =
        Number(tunnel?.type || 1) === 2 ? "tunnel" : "direct";
      const initModes = Array.from({ length: expectedLen }, (_, idx) => {
        const v = String(linkModesFromApi[idx] || "").toLowerCase();
        if (v === "tunnel" || v === "direct") return v as LinkMode;
        return initMode;
      });
      setLinkModes(initModes);
      prevRouteRef.current = route;
      prevLinkModesRef.current = initModes;
      editLoadedRef.current = true;
    } catch (e: any) {
      toast.error(e?.message || "加载编辑数据失败");
    } finally {
      setEditLoading(false);
    }
  }, [
    isEdit,
    editId,
    nodes,
    exitNodes,
    navigate,
    buildNodeRouteItem,
    buildExternalRouteItem,
    getRecommendedPort,
  ]);

  const addGroupsFromInput = useCallback((raw: string) => {
    const tokens = raw
      .split(/[，,;/|]+/)
      .map((g) => g.trim())
      .filter((g) => g);
    if (tokens.length === 0) return;
    setForwardGroups((prev) => {
      const next = [...prev];
      tokens.forEach((t) => {
        if (!next.includes(t)) next.push(t);
      });
      return next;
    });
    setForwardGroupInput("");
  }, []);

  const removeGroup = useCallback((name: string) => {
    setForwardGroups((prev) => prev.filter((g) => g !== name));
  }, []);

  const collectFinalGroups = useCallback(() => {
    const tokens = forwardGroupInput
      .split(/[，,;/|]+/)
      .map((g) => g.trim())
      .filter((g) => g);
    const next = [...forwardGroups];
    tokens.forEach((t) => {
      if (!next.includes(t)) next.push(t);
    });
    return next;
  }, [forwardGroups, forwardGroupInput]);

  useEffect(() => {
    if (!isEdit || loading) return;
    if (editLoadedRef.current) return;
    void loadEditData();
  }, [isEdit, loading, loadEditData]);

  const toggleNodeInRoute = useCallback(
    (node: any) => {
      const nodeId = Number(node?.id || 0);
      if (!nodeId) return;
      setRouteItems((prev) => {
        const id = `node-${nodeId}`;
        const exists = prev.find((item) => item.id === id);
        if (exists) return prev.filter((item) => item.id !== id);
        const next = [...prev];
        const last = next[next.length - 1];
        const recommended = getRecommendedPort(nodeId);
        const item = buildNodeRouteItem(node, undefined, next.length === 0 ? recommended : null);
        if (last && isExitItem(last)) {
          next.splice(next.length - 1, 0, item);
          return next;
        }
        return [...next, item];
      });
    },
    [buildNodeRouteItem, getRecommendedPort, isExitItem],
  );

  const addExitToRoute = useCallback(
    (item: any, protocol?: string) => {
      if (!item) return;
      const isExternal = item?.source === "external";
      const nodeId = Number(item?.nodeId || item?.id || 0);
      const nodeRef =
        nodes.find((n: any) => Number(n?.id || 0) === nodeId) || {
          id: nodeId,
          name: item?.name,
          serverIp: item?.host,
        };
      setRouteItems((prev) => {
        const exitPort =
          protocol === "anytls" ? item?.anytlsPort : item?.ssPort;
        const baseItem = isExternal
          ? buildExternalRouteItem(item, protocol || item?.protocol)
          : {
              ...buildNodeRouteItem(
                nodeRef,
                protocol,
                undefined,
                protocol === "anytls" ? item?.anytlsExitIp : undefined,
              ),
              ssPort: item?.ssPort,
              anytlsPort: item?.anytlsPort,
            };
        if (!baseItem.id) return prev;
        let next = prev.filter((i) => i.id !== baseItem.id);
        if (isExternal) {
          next = next.filter((i) => i.type !== "external");
        }
        const isFirst = next.length === 0;
        const routeItem = isExternal
          ? baseItem
          : {
              ...baseItem,
              port: isFirst
                ? exitPort
                  ? Number(exitPort)
                  : getRecommendedPort(baseItem.nodeId)
                : exitPort
                  ? Number(exitPort)
                  : null,
            };
        return [...next, routeItem as RouteItem];
      });
    },
    [buildExternalRouteItem, buildNodeRouteItem, getRecommendedPort, nodes],
  );

  const updateRouteItem = useCallback(
    (id: string, patch: Partial<RouteItem>) => {
      setRouteItems((prev) =>
        prev.map((item) => (item.id === id ? { ...item, ...patch } : item)),
      );
    },
    [],
  );

  useEffect(() => {
    if (routeItems.length === 0) return;
    setRouteItems((prev) => {
      let changed = false;
      const lastIndex = prev.length - 1;
      const next = prev.map((item, idx) => {
        if (item.type !== "node") return item;
        const isEntry = idx === 0;
        if (idx === lastIndex && isExitItem(item) && (!isEntry || prev.length === 1)) {
          const desired =
            item.protocol === "anytls" ? item.anytlsPort : item.ssPort;
          if (desired && item.port !== desired) {
            changed = true;
            return { ...item, port: desired };
          }
          return item;
        }
        if (item.port == null) {
          const recommended = getRecommendedPort(item.nodeId);
          if (recommended) {
            changed = true;
            return { ...item, port: recommended };
          }
        }
        return item;
      });
      return changed ? next : prev;
    });
  }, [routeItems, getRecommendedPort, isExitItem]);

  const loadIfaces = useCallback(
    async (nodeId?: number) => {
      const nid = Number(nodeId || 0);
      if (!nid) return;
      if (ifaceMap[nid] || ifaceLoading[nid]) return;
      setIfaceLoading((prev) => ({ ...prev, [nid]: true }));
      try {
        const res: any = await getNodeInterfaces(nid);
        const list =
          res && res.code === 0 && Array.isArray(res.data?.ips)
            ? (res.data.ips as string[])
            : [];
        const node = nodes.find((n: any) => Number(n?.id || 0) === nid);
        const extras: string[] = [];
        if (node?.serverIp) extras.push(String(node.serverIp));
        if (node?.ip) {
          String(node.ip)
            .split(",")
            .map((v) => v.trim())
            .filter(Boolean)
            .forEach((v) => extras.push(v));
        }
        const merged = Array.from(new Set([...extras, ...list])).filter(
          (v) => v && v.trim(),
        );
        setIfaceMap((prev) => ({ ...prev, [nid]: merged }));
      } catch {
        /* noop */
      } finally {
        setIfaceLoading((prev) => ({ ...prev, [nid]: false }));
      }
    },
    [ifaceLoading, ifaceMap, nodes],
  );


  const removeRouteItem = useCallback((id: string) => {
    setRouteItems((prev) => prev.filter((item) => item.id !== id));
  }, []);

  const routeSensors = useSensors(
    useSensor(PointerSensor),
    useSensor(KeyboardSensor, {
      coordinateGetter: sortableKeyboardCoordinates,
    }),
  );

  const handleRouteDragEnd = useCallback((event: DragEndEvent) => {
    const { active, over } = event;
    if (!over || active.id === over.id) return;
    setRouteItems((items) => {
      const oldIndex = items.findIndex((i) => i.id === active.id);
      const newIndex = items.findIndex((i) => i.id === over.id);
      if (oldIndex < 0 || newIndex < 0) return items;
      return arrayMove(items, oldIndex, newIndex);
    });
  }, []);

  const validationHint = useMemo(() => {
    if (routeItems.length === 0) return "请选择节点组成线路，最后一跳需为出口节点";
    if (routeItems.length === 1) {
      const only = routeItems[0];
      if (only.type !== "node" || !isExitItem(only)) {
        return "单出口线路仅支持面板节点出口";
      }
      return "单出口线路有效，点击下一步继续";
    }
    if (routeItems.length < 2) return "线路至少包含入口与出口节点";
    if (routeItems[0].type !== "node") return "入口必须是节点";
    const last = routeItems[routeItems.length - 1];
    if (!isExitItem(last)) return "最后一个节点必须为出口节点";
    const externalIndex = routeItems.findIndex(
      (item) => item.type === "external",
    );
    if (externalIndex >= 0 && externalIndex !== routeItems.length - 1) {
      return "外部出口只能作为最后一跳";
    }
    const lastLinkMode =
      linkModes.length > 0 ? linkModes[linkModes.length - 1] : "direct";
    if (lastLinkMode === "tunnel" && last.type === "external") {
      return "外部出口不支持隧道链路";
    }
    return "线路有效，点击下一步继续";
  }, [routeItems, linkModes, isExitItem]);

  const hintIsError = useMemo(() => {
    return (
      validationHint.includes("必须") ||
      validationHint.includes("至少") ||
      validationHint.includes("只能") ||
      validationHint.includes("仅支持") ||
      validationHint.includes("不支持")
    );
  }, [validationHint]);

  const handleCreate = async () => {
    const allowSingleExit =
      routeItems.length === 1 &&
      routeItems[0]?.type === "node" &&
      isExitItem(routeItems[0]);
    if ((routeItems.length < 2 && !allowSingleExit) || hintIsError) {
      toast.error(validationHint);
      return;
    }
    const linkModesToSave =
      routeItems.length > 1
        ? linkModes.slice(0, routeItems.length - 1)
        : [];
    const useTunnelMode =
      linkModesToSave.length > 0 &&
      linkModesToSave.every((m) => m === "tunnel");
    const nextTunnelType = useTunnelMode ? 2 : 1;
    if (!forwardName.trim()) {
      toast.error("请填写转发名称");
      return;
    }
    const last = routeItems[routeItems.length - 1];
    let processedRemoteAddr = "";
    if (last?.type === "external") {
      const host =
        last.subLabel?.split(":")[0] ||
        (exitNodes.find((n: any) => n.exitId === last.exitId)?.host || "");
      const port = last.exitPort || last.port;
      if (!host || !port) {
        toast.error("外部出口地址或端口无效");
        return;
      }
      processedRemoteAddr = `${host}:${port}`;
    } else if (last?.type === "node") {
      const node = nodes.find(
        (n: any) => Number(n?.id || 0) === Number(last.nodeId || 0),
      );
      const host =
        node?.serverIp ||
        (node?.ip ? String(node.ip).split(",")[0].trim() : "");
      const port =
        last.protocol === "anytls" ? last.anytlsPort : last.ssPort;
      if (!host || !port) {
        toast.error("出口节点地址或协议端口无效");
        return;
      }
      processedRemoteAddr = `${host}:${port}`;
    }
    if (!processedRemoteAddr) {
      toast.error("请先选择有效的出口节点");
      return;
    }
    setPortErrors({});
    if (
      last?.type === "node" &&
        last?.protocol === "anytls" &&
        last?.exitIp !== undefined
    ) {
      const exitNodeId = Number(last.nodeId || 0);
      const cached = exitNodes.find(
        (x: any) =>
          x?.source === "node" && Number(x?.nodeId || 0) === exitNodeId,
      );
      let anyPort = cached?.anytlsPort;
      let anyPass = cached?.anytlsPassword;
      if (!anyPort || !anyPass) {
        const anyRes: any = await getExitNode(exitNodeId, "anytls").catch(
          () => null,
        );
        if (anyRes && anyRes.code === 0 && anyRes.data) {
          anyPort = anyRes.data?.port;
          anyPass = anyRes.data?.password;
        }
      }
      if (anyPort && anyPass) {
        const exitRes: any = await setExitNode({
          nodeId: exitNodeId,
          type: "anytls",
          port: Number(anyPort),
          password: String(anyPass),
          exitIp: last.exitIp == null ? "" : String(last.exitIp),
        }).catch(() => null);
        if (!exitRes || exitRes.code !== 0) {
          toast.error(exitRes?.msg || "AnyTLS 出口IP设置失败");
          return;
        }
      }
    }
    const entryPort =
      routeItems[0]?.port ?? getRecommendedPort(routeItems[0]?.nodeId);
    const directExitPort =
      routeItems.length === 1 &&
      routeItems[0] &&
      isExitItem(routeItems[0]) &&
      routeItems[0].type === "node"
        ? routeItems[0].protocol === "anytls"
          ? routeItems[0].anytlsPort
          : routeItems[0].ssPort
        : null;
    const isDirectExitSingle =
      directExitPort != null &&
      entryPort != null &&
      Number(directExitPort) === Number(entryPort);
    if (entryPort != null && (entryPort < 1 || entryPort > 65535)) {
      toast.error("入口端口必须在1-65535之间");
      setPortErrors({ [routeItems[0]?.id || "entry"]: "端口无效" });
      return;
    }
    if (entryPort != null) {
      if (!isDirectExitSingle) {
        const range = getNodePortRange(routeItems[0]?.nodeId);
        if (entryPort < range.min || entryPort > range.max) {
          toast.error(`入口端口必须在${range.min}-${range.max}范围内`);
          setPortErrors({
            [routeItems[0]?.id || "entry"]: `范围 ${range.min}-${range.max}`,
          });
          return;
        }
        const used = usedPortsMap.get(Number(routeItems[0]?.nodeId || 0));
        const canIgnoreUsed =
          isEdit &&
          editForward &&
          editTunnel &&
          Number(routeItems[0]?.nodeId || 0) ===
            Number(editTunnel?.inNodeId || 0) &&
          entryPort === Number(editForward?.inPort || 0);
        if (used && used.has(entryPort) && !canIgnoreUsed) {
          toast.error("入口端口已被占用");
          setPortErrors({
            [routeItems[0]?.id || "entry"]: "已被占用",
          });
          return;
        }
      }
    }
    for (let i = 1; i < routeItems.length - 1; i++) {
      const port = routeItems[i]?.port;
      if (port != null && (port < 1 || port > 65535)) {
        toast.error("中继端口必须在1-65535之间");
        setPortErrors({
          [routeItems[i]?.id || `mid-${i}`]: "端口无效",
        });
        return;
      }
      if (port != null) {
        const nid = routeItems[i]?.nodeId;
        const range = getNodePortRange(nid);
        if (port < range.min || port > range.max) {
          toast.error(`中继端口必须在${range.min}-${range.max}范围内`);
          setPortErrors({
            [routeItems[i]?.id || `mid-${i}`]: `范围 ${range.min}-${range.max}`,
          });
          return;
        }
        const used = usedPortsMap.get(Number(nid || 0));
        if (used && used.has(port)) {
          const nodeName = getNodeName(nid);
          toast.error(`中继端口已被占用：${nodeName} ${port}`);
          setPortErrors({
            [routeItems[i]?.id || `mid-${i}`]: "已被占用",
          });
          return;
        }
      }
    }

    const groupValue = collectFinalGroups().join(",");
    setSubmitting(true);
    try {
      const first = routeItems[0];
      const midNodes = routeItems
        .slice(1, -1)
        .filter((item) => item.type === "node" && item.nodeId)
        .map((item) => Number(item.nodeId));
      const entryBind = first?.bindIp ? String(first.bindIp) : undefined;
      let tunnelId: number | undefined;

      if (isEdit) {
      if (!editForward || !editTunnel) {
        toast.error("编辑信息不完整，请刷新重试");
        return;
      }
      const entryChanged =
        Number(editTunnel?.inNodeId || 0) !== Number(first?.nodeId || 0);
      const typeChanged =
        Number(editTunnel?.type || 1) !== Number(nextTunnelType);
      const nextOutExitId =
        last?.type === "external" ? Number(last.exitId || 0) : 0;
      const nextOutNodeId =
        last?.type === "node" ? Number(last.nodeId || 0) : 0;
        const exitChanged =
          Number(editTunnel?.outExitId || 0) !== nextOutExitId ||
          Number(editTunnel?.outNodeId || 0) !== nextOutNodeId;
        const protoChanged =
          String(editTunnel?.protocol || "").toLowerCase() !==
          String(last?.protocol || "").toLowerCase();

        if (entryChanged || typeChanged) {
          const tunnelName = `线路-${forwardName}-${Date.now()
            .toString()
            .slice(-6)}`;
          const tunnelPayload: any = {
            name: tunnelName,
            inNodeId: Number(first.nodeId),
            type: nextTunnelType,
            flow: 1,
            trafficRatio: 1,
          };
          if (last?.type === "external" && last.exitId) {
            tunnelPayload.outExitId = Number(last.exitId);
          } else if (last?.type === "node" && last.nodeId) {
            tunnelPayload.outNodeId = Number(last.nodeId);
          }
          if (last?.protocol) {
            tunnelPayload.protocol = last.protocol;
          }
          const tunnelRes: any = await createTunnel(tunnelPayload);
          if (!tunnelRes || tunnelRes.code !== 0) {
            toast.error(tunnelRes?.msg || "线路创建失败");
            return;
          }
          const tr: any = await getTunnelList();
          if (tr && tr.code === 0 && Array.isArray(tr.data)) {
            const matches = (tr.data as any[]).filter(
              (t) => t?.name === tunnelName,
            );
            if (matches.length) {
              tunnelId = matches.sort(
                (a, b) => (b.id || 0) - (a.id || 0),
              )[0].id;
            }
          }
          if (!tunnelId) {
            toast.error("线路创建成功但未获取到ID");
            return;
          }
        } else {
          tunnelId = Number(editTunnel.id);
          if (exitChanged || protoChanged) {
            const updatePayload: any = {
              id: Number(editTunnel.id),
              name: String(editTunnel.name || ""),
              flow: Number(editTunnel.flow || 1),
              trafficRatio:
                typeof editTunnel.trafficRatio === "number"
                  ? editTunnel.trafficRatio
                  : 1,
              tcpListenAddr: editTunnel.tcpListenAddr,
              udpListenAddr: editTunnel.udpListenAddr,
              interfaceName: editTunnel.interfaceName,
              protocol: last?.protocol,
            };
            if (last?.type === "external") {
              updatePayload.outExitId = nextOutExitId || null;
            } else if (last?.type === "node") {
              updatePayload.outNodeId = nextOutNodeId || null;
            }
            await updateTunnel(updatePayload);
          }
        }

        if (!tunnelId) {
          toast.error("无法获取线路ID");
          return;
        }

        try {
          await setTunnelPath(tunnelId, midNodes, linkModesToSave);
        } catch {}

        const bindList = routeItems
          .slice(1)
          .filter((item) => item.type === "node" && item.nodeId && item.bindIp)
          .map((item) => ({
            nodeId: Number(item.nodeId),
            ip: String(item.bindIp),
          }));
        try {
          await setTunnelBind(tunnelId, bindList);
        } catch {}

        const midPorts = routeItems
          .slice(1, -1)
          .map((item, idx) =>
            item.port ? { idx, port: Number(item.port) } : null,
          )
          .filter(Boolean) as Array<{ idx: number; port: number }>;

        const updatePayload: any = {
          id: Number(editForward.id),
          name: forwardName.trim(),
          group: groupValue,
          tunnelId,
          inPort: entryPort != null ? entryPort : undefined,
          remoteAddr: processedRemoteAddr,
          interfaceName: editForward.interfaceName || entryBind,
          strategy: "fifo",
        };
        if (midPorts.length > 0) {
          updatePayload.midPorts = midPorts;
        }
        const updateRes: any = await updateForward(updatePayload);
        if (!updateRes || updateRes.code !== 0) {
          toast.error(updateRes?.msg || "转发更新失败");
          return;
        }
        toast.success("转发已更新并下发");
        navigate("/forward");
        return;
      }

      const tunnelName = `线路-${forwardName}-${Date.now()
        .toString()
        .slice(-6)}`;
      const tunnelPayload: any = {
        name: tunnelName,
        inNodeId: Number(first.nodeId),
        type: nextTunnelType,
        flow: 1,
        trafficRatio: 1,
      };
      if (last?.type === "external" && last.exitId) {
        tunnelPayload.outExitId = Number(last.exitId);
      } else if (last?.type === "node" && last.nodeId) {
        tunnelPayload.outNodeId = Number(last.nodeId);
      }
      if (last?.protocol) {
        tunnelPayload.protocol = last.protocol;
      }

      const tunnelRes: any = await createTunnel(tunnelPayload);
      if (!tunnelRes || tunnelRes.code !== 0) {
        toast.error(tunnelRes?.msg || "线路创建失败");
        return;
      }

      try {
        const tr: any = await getTunnelList();
        if (tr && tr.code === 0 && Array.isArray(tr.data)) {
          const matches = (tr.data as any[]).filter(
            (t) => t?.name === tunnelName,
          );
          if (matches.length) {
            tunnelId = matches.sort(
              (a, b) => (b.id || 0) - (a.id || 0),
            )[0].id;
          }
        }
      } catch {}

      if (!tunnelId) {
        toast.error("线路创建成功但未获取到ID");
        return;
      }

      if (midNodes.length > 0 || linkModesToSave.length > 0) {
        try {
          await setTunnelPath(tunnelId, midNodes, linkModesToSave);
        } catch {}
      }

      const bindList = routeItems
        .slice(1)
        .filter((item) => item.type === "node" && item.nodeId && item.bindIp)
        .map((item) => ({
          nodeId: Number(item.nodeId),
          ip: String(item.bindIp),
        }));
      if (bindList.length > 0) {
        try {
          await setTunnelBind(tunnelId, bindList);
        } catch {}
      }

      const createRes: any = await createForward({
        name: forwardName.trim(),
        group: groupValue,
        tunnelId,
        inPort: entryPort != null ? entryPort : undefined,
        remoteAddr: processedRemoteAddr,
        interfaceName: entryBind,
        strategy: "fifo",
      });
      if (!createRes || createRes.code !== 0) {
        toast.error(createRes?.msg || "转发创建失败");
        return;
      }

      const needUpdate = routeItems
        .slice(1, -1)
        .some((item) => item.port);

      if (needUpdate) {
        const fr: any = await getForwardList();
        if (fr && fr.code === 0 && Array.isArray(fr.data)) {
          const matches = (fr.data as any[]).filter(
            (f) =>
              f?.name === forwardName.trim() &&
              Number(f?.tunnelId) === Number(tunnelId),
          );
          const target =
            matches.length > 0
              ? matches.sort(
                  (a, b) => (b.createdTime || 0) - (a.createdTime || 0),
                )[0]
              : null;
          if (target?.id) {
            const midPorts = routeItems
              .slice(1, -1)
              .map((item, idx) =>
                item.port ? { idx, port: Number(item.port) } : null,
              )
              .filter(Boolean) as Array<{ idx: number; port: number }>;
            const updatePayload: any = {
              id: Number(target.id),
              name: forwardName.trim(),
              group: groupValue,
              tunnelId,
              inPort: entryPort != null ? entryPort : undefined,
              remoteAddr: processedRemoteAddr,
              interfaceName: entryBind,
              strategy: "fifo",
            };
            if (midPorts.length > 0) {
              updatePayload.midPorts = midPorts;
            }
            await updateForward(updatePayload);
          }
        }
      }

      toast.success("转发已创建并下发");
      setRouteItems([]);
      navigate("/forward");
    } catch (e: any) {
      toast.error(e?.message || "操作失败");
    } finally {
      setSubmitting(false);
    }
  };

  return (
    <div className="np-page">
      <div className="np-page-header">
        <div>
          <h1 className="np-page-title">
            {isEdit ? "编辑转发 · 线路调整" : "新建转发 · 线路选择"}
          </h1>
          <p className="np-page-desc">
            左侧选择入口/中间节点，右侧选择出口节点或外部出口，拖拽排序生成线路
          </p>
        </div>
        <div className="flex items-center gap-2">
          <Button variant="light" onPress={() => navigate("/forward")}>
            返回列表
          </Button>
          <Button
            color="primary"
            isDisabled={hintIsError || submitting || editLoading}
            isLoading={submitting || editLoading}
            onPress={handleCreate}
          >
            {isEdit ? "保存修改" : "创建转发"}
          </Button>
        </div>
      </div>

      {loading ? (
        <Card className="np-card">
          <CardBody className="flex items-center gap-2 text-sm text-default-500">
            <Spinner size="sm" /> 正在加载节点列表...
          </CardBody>
        </Card>
      ) : (
        <>
          <div className="flex gap-4">
            <div className="w-[260px] shrink-0">
              <Card className="np-card">
                <CardHeader>
                  <div className="font-semibold">入口/中继节点</div>
                </CardHeader>
                <CardBody className="space-y-2">
                  {nodes.length === 0 ? (
                    <div className="text-sm text-default-500">暂无节点</div>
                  ) : (
                    nodes.map((node: any) => {
                      const nodeId = Number(node?.id || 0);
                      const isOnline = node?.status === 1;
                      const selected = routeItems.some(
                        (item) => item.type === "node" && item.nodeId === nodeId,
                      );
                      return (
                        <div
                          key={nodeId}
                          className={`np-soft p-2 space-y-1 ${
                            isOnline ? "" : "opacity-60 grayscale"
                          }`}
                        >
                          <div className="text-sm font-medium break-words">
                            {node?.name || `节点${nodeId}`}
                          </div>
                          <div className="text-2xs text-default-400 break-words">
                            {node?.serverIp || node?.ip || "-"}
                          </div>
                          <div className="flex justify-end">
                            <Button
                              color={selected ? "primary" : "default"}
                              isDisabled={!isOnline}
                              size="sm"
                              variant={selected ? "solid" : "flat"}
                              onPress={() => toggleNodeInRoute(node)}
                            >
                              {selected ? "移除" : "加入"}
                            </Button>
                          </div>
                        </div>
                      );
                    })
                  )}
                </CardBody>
              </Card>
            </div>

            <div className="flex-1 min-w-0 space-y-3">
              <Card className="np-card">
                <CardHeader>
                  <div className="font-semibold">转发配置</div>
                </CardHeader>
                <CardBody className="space-y-3">
                  <Input
                    label="转发名称"
                    placeholder="请输入转发名称"
                    value={forwardName}
                    variant="bordered"
                    onChange={(e) => setForwardName(e.target.value)}
                  />
                  <div className="space-y-2">
                    <div className="space-y-1">
                      <label className="text-sm text-default-600">
                        分组（可选）
                      </label>
                      <div
                        ref={groupBoxRef}
                        className="np-tag-input"
                        onClick={() => {
                          groupInputRef.current?.focus();
                          setGroupDropdownOpen(true);
                        }}
                      >
                        {forwardGroups.map((g) => (
                          <span key={g} className="np-tag-pill">
                            <span className="np-tag-text">{g}</span>
                            <button
                              type="button"
                              className="np-tag-remove"
                              onClick={(e) => {
                                e.stopPropagation();
                                removeGroup(g);
                              }}
                            >
                              ×
                            </button>
                          </span>
                        ))}
                        <input
                          ref={groupInputRef}
                          className="np-tag-input-field"
                          placeholder={
                            forwardGroups.length === 0
                              ? "输入或选择分组，回车确认"
                              : ""
                          }
                          value={forwardGroupInput}
                          onFocus={() => setGroupDropdownOpen(true)}
                          onChange={(e) => setForwardGroupInput(e.target.value)}
                          onKeyDown={(e) => {
                            if (
                              e.key === "Enter" ||
                              e.key === "," ||
                              e.key === "，"
                            ) {
                              e.preventDefault();
                              addGroupsFromInput(
                                (e.currentTarget as HTMLInputElement).value,
                              );
                              return;
                            }
                            if (
                              (e.key === "Backspace" || e.key === "Delete") &&
                              !(e.currentTarget as HTMLInputElement).value
                            ) {
                              if (forwardGroups.length > 0) {
                                const last = forwardGroups[forwardGroups.length - 1];
                                removeGroup(last);
                              }
                            }
                          }}
                        />
                        <button
                          type="button"
                          className="np-tag-caret"
                          onClick={(e) => {
                            e.stopPropagation();
                            setGroupDropdownOpen((prev) => !prev);
                            groupInputRef.current?.focus();
                          }}
                        >
                          ▾
                        </button>
                        {groupDropdownOpen ? (
                          <div className="np-tag-dropdown">
                            {filteredGroupOptions.length === 0 ? (
                              <div className="np-tag-empty">
                                {groupOptions.length === 0
                                  ? "暂无已有分组"
                                  : "未找到匹配分组"}
                              </div>
                            ) : (
                              filteredGroupOptions.map((g) => (
                                <button
                                  type="button"
                                  key={`group-opt-${g}`}
                                  className="np-tag-option"
                                  onMouseDown={(e) => {
                                    e.preventDefault();
                                    addGroupsFromInput(g);
                                    setForwardGroupInput("");
                                    setGroupDropdownOpen(false);
                                  }}
                                >
                                  {g}
                                </button>
                              ))
                            )}
                          </div>
                        ) : null}
                      </div>
                    </div>
                  </div>
                  <div className="text-xs text-default-500 np-soft p-3">
                    远程地址将自动使用所选出口节点的协议端口，无需手动填写。
                  </div>
                </CardBody>
              </Card>
              <Card className="np-card">
                <CardHeader>
                  <div className="flex items-center justify-between w-full">
                    <div className="font-semibold">已选线路（拖拽排序）</div>
                    <Button
                      size="sm"
                      variant="light"
                      onPress={() => setRouteItems([])}
                    >
                      清空线路
                    </Button>
                  </div>
                </CardHeader>
                <CardBody>
                  {routeItems.length === 0 ? (
                    <div className="text-sm text-default-400">
                      {validationHint}
                    </div>
                  ) : (
                    <DndContext
                      collisionDetection={closestCenter}
                      sensors={routeSensors}
                      onDragEnd={handleRouteDragEnd}
                    >
                      <SortableContext
                        items={routeItems.map((i) => i.id)}
                        strategy={rectSortingStrategy}
                      >
                        <div className="space-y-2">
                          {routeItems.map((item, index) => {
                            const isLast = index === routeItems.length - 1;
                            const isExitCandidate = isExitItem(item);
                            const isExit = isLast && isExitCandidate;
                            const isEntry = index === 0;
                            const lockExitPort = isExit && !isEntry;
                            const portLabel =
                              isEntry || isExit ? "入口端口" : "中继端口";
                            const extraLines: string[] = [];
                            if (isEntry && item.type === "node") {
                              extraLines.push(
                                `入口IP：${item.subLabel || "-"}`,
                              );
                            }
                            if (isExitCandidate && item.type === "node") {
                              const selectedExitPort =
                                item.protocol === "anytls"
                                  ? item.anytlsPort
                                  : item.ssPort;
                              extraLines.push(
                                `SS出口：${item.ssPort != null ? item.ssPort : "-"}`,
                              );
                              extraLines.push(
                                `AnyTLS出口：${item.anytlsPort != null ? item.anytlsPort : "-"}`,
                              );
                              if (item.protocol === "anytls") {
                                extraLines.push(
                                  `AnyTLS出口IP：${item.exitIp || "-"}`,
                                );
                              }
                              if (!isEntry && isExit) {
                                extraLines.push(
                                  `出口入口端口：${
                                    selectedExitPort != null
                                      ? selectedExitPort
                                      : "-"
                                  }`,
                                );
                              }
                            }
                            if (item.type === "external") {
                              extraLines.push(
                                `出口端口：${item.exitPort != null ? item.exitPort : "-"}`,
                              );
                            }
                            const recommendedEntryPort = isEntry
                              ? getRecommendedPort(item.nodeId)
                              : null;
                            return (
                              <div key={item.id} className="space-y-2">
                                <SortableRouteItem
                                  index={index}
                                  isExit={isExit}
                                  item={item}
                                  extraLines={extraLines}
                                  onRemove={removeRouteItem}
                                />
                                {item.type === "node" ? (
                                  <div className="np-soft p-2 grid grid-cols-2 gap-2">
                                    {isEntry ? (
                                      <div className="text-2xs text-default-500 flex items-center">
                                        入口IP：自动使用节点入口
                                      </div>
                                    ) : (
                                      (() => {
                                      const opts = [
                                        { key: "__auto__", label: "自动选择" },
                                        ...(
                                          ifaceMap[item.nodeId || 0] || []
                                        ).map((ip) => ({
                                          key: ip,
                                          label: ip,
                                        })),
                                      ];
                                      return (
                                        <Select
                                          label="入口IP"
                                          placeholder="请选择入口IP"
                                          selectedKeys={
                                            item.bindIp ? [item.bindIp] : []
                                          }
                                          size="sm"
                                          variant="bordered"
                                          onOpenChange={(open) => {
                                            if (open) loadIfaces(item.nodeId);
                                          }}
                                          onSelectionChange={(keys) => {
                                            const k = Array.from(keys)[0] as string;
                                            updateRouteItem(item.id, {
                                              bindIp: k === "__auto__" ? undefined : k,
                                            });
                                          }}
                                          items={opts}
                                        >
                                          {(opt) => (
                                            <SelectItem key={opt.key}>
                                              {opt.label}
                                            </SelectItem>
                                          )}
                                        </Select>
                                      );
                                    })()
                                    )}
                                    <Input
                                      label={portLabel}
                                      placeholder={
                                        lockExitPort
                                          ? item.protocol === "anytls"
                                            ? item.anytlsPort
                                              ? "出口端口已锁定"
                                              : "出口端口未配置"
                                            : item.ssPort
                                              ? "出口端口已锁定"
                                              : "出口端口未配置"
                                          : recommendedEntryPort
                                            ? `推荐 ${recommendedEntryPort}`
                                            : "留空自动分配"
                                      }
                                      size="sm"
                                      type="number"
                                      value={
                                        lockExitPort
                                          ? String(
                                              item.protocol === "anytls"
                                                ? item.anytlsPort || ""
                                                : item.ssPort || "",
                                            )
                                          : item.port != null
                                            ? String(item.port)
                                            : recommendedEntryPort != null
                                              ? String(recommendedEntryPort)
                                              : ""
                                      }
                                      variant="bordered"
                                      isInvalid={!!portErrors[item.id]}
                                      errorMessage={portErrors[item.id]}
                                      isDisabled={lockExitPort}
                                      onChange={(e) => {
                                        if (lockExitPort) return;
                                        const v = e.target.value;
                                        updateRouteItem(item.id, {
                                          port: v ? Number(v) : null,
                                        });
                                      }}
                                    />
                                    {isExit && item.protocol === "anytls" ? (
                                      (() => {
                                        const opts = [
                                          { key: "__auto__", label: "自动选择" },
                                          ...(
                                            ifaceMap[item.nodeId || 0] || []
                                          ).map((ip) => ({
                                            key: ip,
                                            label: ip,
                                          })),
                                        ];
                                        return (
                                          <Select
                                            label="AnyTLS出口IP"
                                            placeholder="请选择出口IP"
                                            selectedKeys={
                                              item.exitIp ? [item.exitIp] : []
                                            }
                                            size="sm"
                                            variant="bordered"
                                            onOpenChange={(open) => {
                                              if (open) loadIfaces(item.nodeId);
                                            }}
                                            onSelectionChange={(keys) => {
                                              const k = Array.from(keys)[0] as string;
                                              updateRouteItem(item.id, {
                                                exitIp: k === "__auto__" ? "" : k,
                                              });
                                            }}
                                            items={opts}
                                          >
                                            {(opt) => (
                                              <SelectItem key={opt.key}>
                                                {opt.label}
                                              </SelectItem>
                                            )}
                                          </Select>
                                        );
                                      })()
                                    ) : null}
                                    {ifaceLoading[item.nodeId || 0] ? (
                                      <div className="text-2xs text-default-400">
                                        正在加载接口IP...
                                      </div>
                                    ) : null}
                                  </div>
                                ) : null}
                                {index < routeItems.length - 1 ? (
                                  <div className="px-1">
                                    <div className="flex items-center gap-3">
                                      <div className="flex-1 border-t border-dashed border-default-300" />
                                      <div className="np-soft rounded-full px-1 py-1 flex items-center gap-1">
                                        <Button
                                          size="sm"
                                          variant={
                                            linkModes[index] === "direct"
                                              ? "solid"
                                              : "light"
                                          }
                                          color={
                                            linkModes[index] === "direct"
                                              ? "primary"
                                              : "default"
                                          }
                                          onPress={() =>
                                            setLinkModeAt(index, "direct")
                                          }
                                        >
                                          转发
                                        </Button>
                                        <Button
                                          size="sm"
                                          variant={
                                            linkModes[index] === "tunnel"
                                              ? "solid"
                                              : "light"
                                          }
                                          color={
                                            linkModes[index] === "tunnel"
                                              ? "warning"
                                              : "default"
                                          }
                                          onPress={() =>
                                            setLinkModeAt(index, "tunnel")
                                          }
                                        >
                                          隧道
                                        </Button>
                                      </div>
                                      <div className="flex-1 border-t border-dashed border-default-300" />
                                    </div>
                                  </div>
                                ) : null}
                              </div>
                            );
                          })}
                        </div>
                      </SortableContext>
                    </DndContext>
                  )}
                  <div
                    className={`mt-2 text-2xs ${
                      hintIsError ? "text-danger" : "text-success-600"
                    }`}
                  >
                    {validationHint}
                  </div>
                </CardBody>
              </Card>
            </div>

            <div className="w-[260px] shrink-0">
              <Card className="np-card">
                <CardHeader>
                  <div className="font-semibold">出口节点</div>
                </CardHeader>
                <CardBody className="space-y-2">
                  {exitNodeList.length === 0 && exitExternalList.length === 0 ? (
                    <div className="text-sm text-default-500">暂无出口节点</div>
                  ) : (
                    <>
                      {exitNodeList.map((item: any) => {
                        const hasSS = !!item?.ssPort;
                        const hasAnyTLS = !!item?.anytlsPort;
                        const isOnline = item?.online !== false;
                        return (
                        <div
                          key={`exit-node-${item.nodeId}`}
                          className={`np-soft p-2 space-y-1 ${isOnline ? "" : "opacity-60 grayscale"}`}
                        >
                          <div className="text-sm font-medium break-words">
                            {item?.name || `节点${item.nodeId}`}
                          </div>
                          <div className="text-2xs text-default-400 break-words">
                            {item?.host || "-"}
                          </div>
                          <div className="text-2xs text-default-600">
                            SS出口：{item?.ssPort ? item.ssPort : "-"}
                          </div>
                          <div className="text-2xs text-default-600">
                            AnyTLS出口：{item?.anytlsPort ? item.anytlsPort : "-"}
                          </div>
                          <div className="flex flex-wrap gap-2 pt-1">
                            {hasSS && hasAnyTLS ? (
                              <>
                                <Button
                                  color="primary"
                                  isDisabled={!isOnline}
                                  size="sm"
                                  variant="flat"
                                  onPress={() => addExitToRoute(item, "ss")}
                                >
                                  SS出口
                                </Button>
                                <Button
                                  color="secondary"
                                  isDisabled={!isOnline}
                                  size="sm"
                                  variant="flat"
                                  onPress={() => addExitToRoute(item, "anytls")}
                                >
                                  AnyTLS出口
                                </Button>
                              </>
                            ) : (
                              <Button
                                color="success"
                                isDisabled={!isOnline}
                                size="sm"
                                variant="flat"
                                onPress={() =>
                                  addExitToRoute(
                                    item,
                                    hasSS ? "ss" : hasAnyTLS ? "anytls" : undefined,
                                  )
                                }
                              >
                                设为出口
                              </Button>
                            )}
                          </div>
                        </div>
                      );
                      })}
                      {exitExternalList.map((item: any) => (
                        <div
                          key={`exit-ext-${item.exitId}`}
                          className="np-soft p-2 space-y-1"
                        >
                          <div className="text-sm font-medium break-words">
                            {item?.name || `外部出口${item.exitId}`}
                          </div>
                          <div className="text-2xs text-default-400 break-words">
                            {item?.host || "-"}:{item?.port || "-"}
                          </div>
                          <div className="text-2xs text-default-600">
                            协议：{(item?.protocol || "出口").toUpperCase()}
                          </div>
                          <div className="flex justify-end pt-1">
                            <Button
                              color="warning"
                              size="sm"
                              variant="flat"
                              onPress={() => addExitToRoute(item, item?.protocol)}
                            >
                              设为出口
                            </Button>
                          </div>
                        </div>
                      ))}
                    </>
                  )}
                </CardBody>
              </Card>
            </div>
          </div>
        </>
      )}
    </div>
  );
}
