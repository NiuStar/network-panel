import { useEffect, useMemo, useRef, useState } from "react";
import { Card, CardBody, CardHeader } from "@heroui/card";
import { Button } from "@heroui/button";
import { Select, SelectItem } from "@heroui/select";
import { Switch } from "@heroui/switch";
import { Alert } from "@heroui/alert";
import {
  Modal,
  ModalBody,
  ModalContent,
  ModalFooter,
  ModalHeader,
} from "@heroui/modal";
import toast from "react-hot-toast";

import {
  etStatus,
  etEnable,
  etNodes,
  etJoin,
  getNodeInterfaces,
  etSuggestPort,
  etRemove,
  etAutoAssign,
  etRedeployMaster,
  etVersion,
  etOperate,
  etOperateBatch,
  etReapplyBatch,
  etLog,
  listNodeOps,
  getConfigByName,
} from "@/api";
import VirtualGrid from "@/components/VirtualGrid";

interface NodeLite {
  id: number;
  name: string;
  serverIp: string;
  configured?: boolean;
  joined?: boolean;
  ip?: string;
  port?: number;
  peerNodeId?: number;
  peerIp?: string;
  ipv4?: string;
  expectedIp?: string;
  online?: boolean;
  etStatus?: string;
  etOp?: string;
  etError?: string;
  etUpdatedTime?: number;
  etRequestId?: string;
  etVersion?: string;
}

export default function EasyTierPage() {
  const [loading, setLoading] = useState(true);
  const [enabled, setEnabled] = useState(false);
  const [secret, setSecret] = useState("");
  const [masterNodeId, setMasterNodeId] = useState<number | undefined>(
    undefined,
  );
  const [masterIp, setMasterIp] = useState("");
  const [masterPort, setMasterPort] = useState<number>(0);
  const [autoJoin, setAutoJoin] = useState(false);
  const [nodes, setNodes] = useState<NodeLite[]>([]);
  const [ifaceCache, setIfaceCache] = useState<Record<number, string[]>>({});
  const [editOpen, setEditOpen] = useState(false);
  const [editNode, setEditNode] = useState<NodeLite | null>(null);
  const [editIp, setEditIp] = useState("");
  const [editPort, setEditPort] = useState<number>(0);
  const [editPeer, setEditPeer] = useState<number | undefined>(undefined);
  const [editPeerIp, setEditPeerIp] = useState<string>("");
  const [opsOpen, setOpsOpen] = useState(false);
  const [opsNodeId, setOpsNodeId] = useState<number | undefined>(undefined);
  const [ops, setOps] = useState<
    Array<{
      timeMs: number;
      cmd: string;
      success: number;
      message: string;
      stdout?: string;
      stderr?: string;
    }>
  >([]);
  const [opsLoading, setOpsLoading] = useState(false);
  const [versionLoading, setVersionLoading] = useState(false);
  const [currentVersion, setCurrentVersion] = useState("");
  const [latestVersion, setLatestVersion] = useState("");
  const [updateLoading, setUpdateLoading] = useState(false);
  const [selectedNodeIds, setSelectedNodeIds] = useState<number[]>([]);
  const [logOpen, setLogOpen] = useState(false);
  const [logRequestId, setLogRequestId] = useState<string>("");
  const [logLines, setLogLines] = useState<string[]>([]);
  const [logLoading, setLogLoading] = useState(false);
  const [logDone, setLogDone] = useState(false);
  const [panelHostConfig, setPanelHostConfig] = useState("");
  const [panelHostLoaded, setPanelHostLoaded] = useState(false);
  const logAbortRef = useRef<AbortController | null>(null);
  const allNodeIds = useMemo(() => nodes.map((n) => n.id), [nodes]);
  const allSelected =
    allNodeIds.length > 0 &&
    allNodeIds.every((id) => selectedNodeIds.includes(id));
  const toggleSelectAll = () => {
    setSelectedNodeIds(allSelected ? [] : allNodeIds);
  };
  const toggleSelectNode = (id: number, checked: boolean) => {
    setSelectedNodeIds((prev) => {
      if (checked) {
        if (prev.includes(id)) return prev;
        return [...prev, id];
      }
      return prev.filter((x) => x !== id);
    });
  };
  const reloadOps = async () => {
    if (!opsNodeId) return;
    setOpsLoading(true);
    try {
      const r: any = await listNodeOps({ nodeId: opsNodeId, limit: 50 });

      if (r.code === 0) setOps(r.data.ops || []);
    } catch {
    } finally {
      setOpsLoading(false);
    }
  };

  const configuredNodes = useMemo(
    () => nodes.filter((n) => n.configured),
    [nodes],
  );

  const statusMeta = (node: NodeLite) => {
    if (node.online === false) {
      return { label: "无法获取状态", color: "default" };
    }
    switch (node.etStatus) {
      case "downloading":
        return { label: "下载中", color: "warning" };
      case "installing":
        if (node.etOp === "upgrade") {
          return { label: "升级中", color: "warning" };
        }
        if (node.etOp === "reinstall") {
          return { label: "重装中", color: "warning" };
        }
        if (node.etOp === "uninstall") {
          return { label: "卸载中", color: "warning" };
        }
        return { label: "安装中", color: "warning" };
      case "installed":
        return { label: "已安装", color: "success" };
      case "failed":
        return { label: "失败", color: "danger" };
      case "not_installed":
      default:
        return { label: "未安装", color: "default" };
    }
  };

  const statusPillClass = (color: string) => {
    switch (color) {
      case "success":
        return "bg-success-100 text-success-700";
      case "warning":
        return "bg-warning-100 text-warning-700";
      case "danger":
        return "bg-danger-100 text-danger-700";
      default:
        return "bg-default-100 text-default-600";
    }
  };

  const openLog = (requestId: string) => {
    if (!requestId) return;
    setLogLines([]);
    setLogDone(false);
    setLogRequestId(requestId);
    setLogOpen(true);
  };

  const startLogStream = async (requestId: string) => {
    if (!requestId) return;
    logAbortRef.current?.abort();
    const aborter = new AbortController();
    logAbortRef.current = aborter;
    setLogLoading(true);
    setLogDone(false);
    try {
      const token = localStorage.getItem("token") || "";
      const headers: Record<string, string> = { Accept: "text/event-stream" };
      if (token) headers["Authorization"] = token;
      const res = await fetch(
        `/api/v1/easytier/log/stream?requestId=${encodeURIComponent(requestId)}`,
        { method: "GET", headers, signal: aborter.signal, credentials: "include" },
      );
      if (!res.ok || !res.body) return;
      const reader = res.body.getReader();
      const decoder = new TextDecoder("utf-8");
      const appendLog = (line: string) =>
        setLogLines((prev) => (line ? [...prev, line] : prev));
      while (true) {
        const { value, done } = await reader.read();
        if (done) break;
        const chunk = decoder.decode(value, { stream: true });
        chunk.split("\n\n").forEach((blk) => {
          const line = blk.trim();
          if (!line.startsWith("data:")) return;
          const payload = line.slice(5).trim();
          try {
            const evt = JSON.parse(payload);
            if (evt.event === "log") {
              appendLog(evt.data);
              if (evt.done) setLogDone(true);
            }
          } catch {
            appendLog(payload);
          }
        });
      }
    } catch {
    } finally {
      setLogLoading(false);
    }
  };

  const loadLogSnapshot = async (requestId: string): Promise<boolean> => {
    if (!requestId) return false;
    setLogLines([]);
    try {
      const r: any = await etLog(requestId);
      if (r.code === 0) {
        const content = String(r.data?.content || "");
        setLogDone(!!r.data?.done);
        setLogLines(content ? content.split("\n") : []);
        return !!r.data?.done;
      }
    } catch {
      setLogLines([]);
    }
    return false;
  };

  const handleOperate = async (nodeId: number, action: string) => {
    try {
      const r: any = await etOperate(nodeId, action);
      if (r.code === 0) {
        toast.success("操作已触发");
        const reqId = r.data?.requestId ? String(r.data.requestId) : "";
        if (reqId) {
          openLog(reqId);
        }
        load();
      } else {
        toast.error(r.msg || "操作失败");
      }
    } catch {
      toast.error("操作失败");
    }
  };

  const handleBatchOperate = async (action: string) => {
    if (!selectedNodeIds.length) {
      toast.error("请先选择节点");
      return;
    }
    try {
      const r: any = await etOperateBatch(selectedNodeIds, action);
      if (r.code === 0) {
        toast.success("批量操作已触发");
        load();
      } else {
        toast.error(r.msg || "批量操作失败");
      }
    } catch {
      toast.error("批量操作失败");
    }
  };

  const handleBatchReapply = async () => {
    if (!selectedNodeIds.length) {
      toast.error("请先选择节点");
      return;
    }
    try {
      const r: any = await etReapplyBatch(selectedNodeIds);
      if (r.code === 0) {
        const ok = r.data?.success ?? 0;
        const fail = r.data?.failed ?? 0;
        if (fail > 0) {
          toast.error(`重发完成：成功 ${ok}，失败 ${fail}`);
        } else {
          toast.success(`重发完成：成功 ${ok}`);
        }
        load();
      } else {
        toast.error(r.msg || "批量重发失败");
      }
    } catch {
      toast.error("批量重发失败");
    }
  };

  const load = async () => {
    setLoading(true);
    try {
      const s: any = await etStatus();

      if (s.code === 0) {
        setEnabled(!!s.data?.enabled);
        setSecret(s.data?.secret || "");
        setAutoJoin(!!s.data?.autoJoin);
        const m = s.data?.master || {};

        setMasterNodeId(m.nodeId || undefined);
        setMasterIp(m.ip || "");
        setMasterPort(m.port || 0);
      }
      try {
        const cfg: any = await getConfigByName("ip");

        if (cfg.code === 0) {
          setPanelHostConfig((cfg.data || "").toString());
        }
      } catch {
      } finally {
        setPanelHostLoaded(true);
      }
      setVersionLoading(true);
      try {
        const v: any = await etVersion();

        if (v.code === 0) {
          setCurrentVersion(v.data?.current || "");
          setLatestVersion(v.data?.latest || "");
        }
      } catch {}
      setVersionLoading(false);
      const r: any = await etNodes();

      if (r.code === 0 && Array.isArray(r.data?.nodes)) {
        setNodes(
          r.data.nodes.map((x: any) => ({
            id: x.nodeId,
            name: x.nodeName,
            serverIp: x.serverIp,
            configured: !!x.configured,
            joined: !!x.joined,
            ip: x.ip,
            port: x.port,
            peerNodeId: x.peerNodeId,
            peerIp: x.peerIp,
            ipv4: x.ipv4,
            expectedIp: x.expectedIp,
            online: x.online,
            etStatus: x.etStatus,
            etOp: x.etOp,
            etError: x.etError,
            etUpdatedTime: x.etUpdatedTime,
            etRequestId: x.etRequestId,
            etVersion: x.etVersion,
          })),
        );
      }
    } catch {
      toast.error("加载失败");
    } finally {
      setLoading(false);
    }
  };

  useEffect(() => {
    load();
  }, []);

  useEffect(() => {
    setSelectedNodeIds((prev) => prev.filter((id) => allNodeIds.includes(id)));
  }, [allNodeIds]);

  useEffect(() => {
    if (!logOpen || !logRequestId) return;
    (async () => {
      const done = await loadLogSnapshot(logRequestId);
      if (!done) startLogStream(logRequestId);
    })();
    return () => {
      logAbortRef.current?.abort();
      logAbortRef.current = null;
    };
  }, [logOpen, logRequestId]);

  // 当选择主控节点时，自动填充默认入口IP与随机端口
  useEffect(() => {
    (async () => {
      if (!masterNodeId) return;
      if (!masterIp) {
        const nn = nodes.find((n) => n.id === masterNodeId);

        if (nn) setMasterIp(nn.serverIp);
      }
      if (!masterPort) {
        try {
          const s: any = await etSuggestPort(masterNodeId);

          if (s.code === 0) setMasterPort(s.data?.port || 0);
        } catch {}
      }
    })();
  }, [masterNodeId]);

  const fetchIfaces = async (nodeId: number) => {
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

  const addIfaceToCache = (nodeId: number, ip?: string) => {
    if (!ip) return;
    setIfaceCache((prev) => {
      const list = prev[nodeId] || [];

      if (list.includes(ip)) return prev;

      return { ...prev, [nodeId]: [...list, ip] };
    });
  };

  const enable = async () => {
    // 可选主控，留空时后端自动选择
    let ip = masterIp;
    let port = masterPort;

    if (masterNodeId) {
      if (!ip) {
        const nn = nodes.find((n) => n.id === masterNodeId);

        if (nn) ip = nn.serverIp;
      }
      if (!port) {
        try {
          const s: any = await etSuggestPort(masterNodeId);

          if (s.code === 0) port = s.data?.port || 0;
        } catch {}
      }
      setMasterIp(ip);
      setMasterPort(port);
    }
    try {
      const r: any = await etEnable({
        enable: true,
        masterNodeId: masterNodeId || 0,
        ip,
        port: port || 0,
        autoJoin,
      });

      if (r.code === 0) {
        toast.success("已启用组网");
        await load();
      } else toast.error(r.msg || "失败");
    } catch {
      toast.error("失败");
    }
  };

  const openEdit = async (n: NodeLite) => {
    if (!enabled || !masterNodeId) {
      toast.error("请先设置主控节点并启用组网");

      return;
    }
    setEditNode(n);
    setEditIp(n.serverIp);
    setEditPort(n.port || 0);
    setEditPeer(n.peerNodeId || masterNodeId || configuredNodes[0]?.id);
    setEditPeerIp(n.peerIp || "");
    await fetchIfaces(n.id);
    addIfaceToCache(n.id, n.serverIp);
    if (!n.port) {
      try {
        const s: any = await etSuggestPort(n.id);

        if (s.code === 0) setEditPort(s.data?.port || 0);
      } catch {}
    }
    setEditOpen(true);
  };

  const updateAutoJoin = async (next: boolean) => {
    setAutoJoin(next);
    try {
      const r: any = await etEnable({
        enable: true,
        masterNodeId: masterNodeId || 0,
        ip: masterIp || "",
        port: masterPort || 0,
        autoJoin: next,
      });

      if (r.code !== 0) {
        toast.error(r.msg || "更新失败");
      }
    } catch {
      toast.error("更新失败");
    }
  };

  const doUpdateAll = async () => {
    setUpdateLoading(true);
    try {
      const ids = nodes.map((n) => n.id);
      const r: any = await etOperateBatch(ids, "upgrade");

      if (r.code === 0) {
        toast.success(`已触发更新：${ids.length} 节点`);
        const v: any = await etVersion();

        if (v.code === 0) {
          setCurrentVersion(v.data?.current || "");
          setLatestVersion(v.data?.latest || "");
        }
      } else {
        toast.error(r.msg || "更新失败");
      }
    } catch {
      toast.error("更新失败");
    } finally {
      setUpdateLoading(false);
    }
  };
  const doJoin = async () => {
    if (!editNode) return;
    if (!editIp || !editPort) {
      toast.error("请选择IP与端口");

      return;
    }
    // 自动弹出操作日志
    setOpsNodeId(editNode.id);
    setOpsOpen(true);
    try {
      const r: any = await etJoin({
        nodeId: editNode.id,
        ip: editIp,
        port: editPort,
        peerNodeId: editPeer,
        peerIp: editPeerIp || undefined,
      });

      if (r.code === 0) {
        toast.success("已下发安装与配置");
        setEditOpen(false);
        load();
      } else toast.error(r.msg || "失败");
    } catch {
      toast.error("失败");
    }
  };

  if (loading)
    return (
      <div className="p-6 space-y-4">
        <div className="skeleton-block min-h-[120px]" />
        <div className="grid gap-4 grid-cols-1 md:grid-cols-2 xl:grid-cols-3">
          {Array.from({ length: 6 }).map((_, idx) => (
            <div key={`et-skel-${idx}`} className="skeleton-card" />
          ))}
        </div>
      </div>
    );

  return (
    <div className="p-4 space-y-4">
      {panelHostLoaded && !panelHostConfig ? (
        <Alert
          color="warning"
          title="提示"
          description="面板后端地址未配置，将使用当前访问域名下发安装脚本；如果你是通过内网/localhost访问，外网节点可能无法安装。建议在“系统配置”里填写公网域名或可访问的后端地址。"
        />
      ) : null}
      <Card>
        <CardHeader className="flex justify-between items-center">
          <div className="font-semibold">组网功能（EasyTier）</div>
          {!enabled ? (
            <div className="flex items-center gap-2">
              <Select
                className="min-w-[320px] max-w-[380px]"
                label="主控节点"
                placeholder="留空自动选择"
                selectedKeys={masterNodeId ? [String(masterNodeId)] : []}
                onSelectionChange={(keys) => {
                  const k = Array.from(keys)[0] as string;

                  if (k) setMasterNodeId(parseInt(k));
                }}
              >
                {nodes.map((n) => (
                  <SelectItem key={String(n.id)}>{n.name}</SelectItem>
                ))}
              </Select>
              <Select
                className="min-w-[320px] max-w-[380px]"
                label="入口IP"
                placeholder="留空自动选择"
                selectedKeys={masterIp ? [masterIp] : []}
                onOpenChange={async () => {
                  if (masterNodeId) await fetchIfaces(masterNodeId);
                }}
                onSelectionChange={(keys) => {
                  const k = Array.from(keys)[0] as string;

                  setMasterIp(k || "");
                }}
              >
                {Array.from(
                  new Set(
                    [
                      ...(ifaceCache[masterNodeId || 0] || []),
                      masterIp ||
                        nodes.find((n) => n.id === masterNodeId)?.serverIp ||
                        "",
                    ].filter(Boolean),
                  ),
                ).map((ip) => (
                  <SelectItem key={ip as string}>{ip as string}</SelectItem>
                ))}
              </Select>
              <InputSmallNumber
                label="端口"
                value={masterPort}
                onChange={setMasterPort}
              />
              <Switch isSelected={autoJoin} onValueChange={setAutoJoin}>
                自动组网
              </Switch>
              <Button color="primary" onPress={enable}>
                启用组网
              </Button>
            </div>
          ) : (
            <div className="flex items-center gap-2 text-sm text-default-500">
              <div>
                已启用 · secret:{" "}
                <span className="font-mono">{secret || "-"}</span>
              </div>
              <div className="text-xs text-default-500">
                版本：{versionLoading ? "加载中..." : currentVersion || "-"}{" "}
                · 最新：{versionLoading ? "加载中..." : latestVersion || "-"}
              </div>
              <Switch
                isSelected={autoJoin}
                onValueChange={updateAutoJoin}
              >
                自动组网
              </Switch>
              <Button
                size="sm"
                variant="flat"
                isLoading={updateLoading}
                isDisabled={
                  !latestVersion ||
                  !currentVersion ||
                  currentVersion === latestVersion
                }
                onPress={doUpdateAll}
              >
                更新所有节点
              </Button>
              <Button
                size="sm"
                variant="flat"
                onPress={async () => {
                  setOpsNodeId(masterNodeId);
                  setOpsOpen(true);
                  try {
                    const r: any = await etRedeployMaster();

                    if (r.code === 0) {
                      toast.success("已在主控重装/重配");
                    } else toast.error(r.msg || "失败");
                  } catch {
                    toast.error("失败");
                  }
                }}
              >
                主控重装/重配
              </Button>
            </div>
          )}
        </CardHeader>
      </Card>

      <Card>
        <CardHeader className="flex justify-between items-center">
          <div className="font-semibold">安装状态与操作</div>
          <div className="text-xs text-default-500">
            节点离线时无法触发或获取状态
          </div>
        </CardHeader>
        <CardBody>
          <div className="flex flex-col gap-3">
            <div className="flex flex-wrap items-center gap-2">
              <Button
                size="sm"
                variant="flat"
                onPress={toggleSelectAll}
                isDisabled={nodes.length === 0}
              >
                {allSelected ? "取消全选" : "全选"}
              </Button>
              <div className="text-xs text-default-500">
                已选择 {selectedNodeIds.length} / {nodes.length}
              </div>
            </div>
            <div className="flex flex-wrap gap-2">
              <Button size="sm" onPress={handleBatchReapply}>
                批量重发
              </Button>
              <Button size="sm" onPress={() => handleBatchOperate("install")}>
                批量安装
              </Button>
              <Button
                size="sm"
                variant="flat"
                onPress={() => handleBatchOperate("reinstall")}
              >
                批量重装
              </Button>
              <Button
                size="sm"
                variant="flat"
                onPress={() => handleBatchOperate("upgrade")}
              >
                批量升级
              </Button>
              <Button
                color="danger"
                size="sm"
                variant="flat"
                onPress={() => handleBatchOperate("uninstall")}
              >
                批量卸载
              </Button>
            </div>
          </div>
        </CardBody>
      </Card>

      <Card>
        <CardHeader className="flex items-center justify-between">
          <div className="font-semibold">节点列表</div>
          <div className="text-xs text-default-500">
            分配IP生效后才算已加入
          </div>
        </CardHeader>
        <CardBody>
          {nodes.length === 0 ? (
            <div className="text-xs text-default-500">暂无</div>
          ) : (
            <VirtualGrid
              className="w-full"
              estimateRowHeight={300}
              gap={8}
              items={nodes}
              maxColumns={1}
              minItemWidth={320}
              renderItem={(n) => {
                const meta = statusMeta(n);
                const joinedOk = !!n.joined;
                const configured = !!n.configured;
                const peerPort = nodes.find((x) => x.id === n.peerNodeId)?.port;

                return (
                  <div
                    key={n.id}
                    className="list-card border border-dashed rounded p-3 cursor-pointer"
                    onDoubleClick={() => openEdit(n)}
                  >
                    <div className="flex items-center justify-between gap-2">
                      <div className="flex items-center gap-2">
                        <input
                          type="checkbox"
                          className="h-4 w-4 cursor-pointer"
                          checked={selectedNodeIds.includes(n.id)}
                          onChange={(e) => toggleSelectNode(n.id, e.target.checked)}
                          onClick={(e) => e.stopPropagation()}
                          onDoubleClick={(e) => e.stopPropagation()}
                        />
                        <div className="font-medium">{n.name}</div>
                      </div>
                      <div className="flex items-center gap-2">
                        <span
                          className={`text-xs px-2 py-0.5 rounded-full ${
                            joinedOk
                              ? "bg-success-100 text-success-700"
                              : "bg-default-100 text-default-600"
                          }`}
                        >
                          {joinedOk ? "已加入" : configured ? "未生效" : "未加入"}
                        </span>
                        <span
                          className={`text-xs px-2 py-0.5 rounded-full ${statusPillClass(
                            meta.color,
                          )}`}
                        >
                          {meta.label}
                        </span>
                      </div>
                    </div>
                    <div className="text-xs text-default-500">
                      公网IP: {n.serverIp}
                    </div>
                    {configured && (
                      <>
                        <div className="text-xs text-default-500">
                          内网IP:{" "}
                          {n.expectedIp ||
                            (n.ipv4 ? `10.126.126.${n.ipv4}` : "-")}
                        </div>
                        <div className="text-xs text-default-500">
                          对外 {n.ip || "-"}:{n.port || 0}
                        </div>
                        <div className="text-xs text-default-500">
                          对端 {n.peerIp || "-"}:{peerPort || "-"}
                        </div>
                      </>
                    )}
                    <div className="text-xs text-default-500">
                      版本: {n.etVersion || "-"}
                    </div>
                    {n.etError && (
                      <div className="text-xs text-danger-600 mt-1">
                        失败原因: {n.etError}
                      </div>
                    )}
                    <div className="mt-2 flex flex-wrap gap-2">
                      {joinedOk ? (
                        <>
                          <Button size="sm" onPress={() => openEdit(n)}>
                            变更对端
                          </Button>
                          <Button
                            color="danger"
                            isDisabled={masterNodeId === n.id}
                            size="sm"
                            variant="flat"
                            onPress={async () => {
                              if (masterNodeId === n.id) {
                                toast.error("主控节点不可移除");

                                return;
                              }
                              try {
                                const r: any = await etRemove(n.id);

                                if (r.code === 0) {
                                  toast.success("已移除");
                                  load();
                                } else toast.error(r.msg || "失败");
                              } catch {
                                toast.error("失败");
                              }
                            }}
                          >
                            移除
                          </Button>
                        </>
                      ) : (
                        <>
                          <Button
                            size="sm"
                            onPress={() => openEdit(n)}
                            isDisabled={!enabled || n.online === false}
                          >
                            {configured ? "重新下发" : "加入"}
                          </Button>
                          {configured && (
                            <Button
                              color="danger"
                              isDisabled={masterNodeId === n.id}
                              size="sm"
                              variant="flat"
                              onPress={async () => {
                                if (masterNodeId === n.id) {
                                  toast.error("主控节点不可移除");

                                  return;
                                }
                                try {
                                  const r: any = await etRemove(n.id);

                                  if (r.code === 0) {
                                    toast.success("已移除");
                                    load();
                                  } else toast.error(r.msg || "失败");
                                } catch {
                                  toast.error("失败");
                                }
                              }}
                            >
                              移除
                            </Button>
                          )}
                        </>
                      )}
                      <Button
                        size="sm"
                        variant="flat"
                        onPress={() => handleOperate(n.id, "install")}
                        isDisabled={n.online === false}
                      >
                        安装
                      </Button>
                      <Button
                        size="sm"
                        variant="flat"
                        onPress={() => handleOperate(n.id, "check")}
                        isDisabled={n.online === false}
                      >
                        检测
                      </Button>
                      <Button
                        size="sm"
                        variant="flat"
                        onPress={() => handleOperate(n.id, "reinstall")}
                        isDisabled={n.online === false}
                      >
                        重装
                      </Button>
                      <Button
                        size="sm"
                        variant="flat"
                        onPress={() => handleOperate(n.id, "upgrade")}
                        isDisabled={n.online === false}
                      >
                        升级
                      </Button>
                      <Button
                        color="danger"
                        size="sm"
                        variant="flat"
                        onPress={() => handleOperate(n.id, "uninstall")}
                        isDisabled={n.online === false}
                      >
                        卸载
                      </Button>
                      {n.etRequestId && (
                        <Button
                          size="sm"
                          variant="flat"
                          onPress={() => openLog(n.etRequestId || "")}
                        >
                          安装日志
                        </Button>
                      )}
                      <Button
                        size="sm"
                        variant="flat"
                        onPress={async () => {
                          setOpsNodeId(n.id);
                          try {
                            const r: any = await listNodeOps({
                              nodeId: n.id,
                              limit: 50,
                            });

                            if (r.code === 0) setOps(r.data.ops || []);
                            else setOps([]);
                          } catch {
                            setOps([]);
                          }
                          setOpsOpen(true);
                        }}
                      >
                        节点操作日志
                      </Button>
                    </div>
                  </div>
                );
              }}
            />
          )}
        </CardBody>
      </Card>

      {enabled && (
        <div className="flex justify-end">
          <Button
            color="primary"
            variant="flat"
            onPress={async () => {
              try {
                const r: any = await etAutoAssign("chain");

                if (r.code === 0) {
                  toast.success("已一键分配链路");
                  load();
                } else toast.error(r.msg || "失败");
              } catch {
                toast.error("失败");
              }
            }}
          >
            一键分配对端链路
          </Button>
        </div>
      )}

      <Modal isOpen={editOpen} onOpenChange={setEditOpen}>
        <ModalContent className="w-[80vw] max-w-[80vw] h-[60vh]">
          {(onClose) => (
            <>
              <ModalHeader className="flex flex-col gap-1">
                编辑节点：{editNode?.name}
              </ModalHeader>
              <ModalBody className="overflow-auto">
                <div className="grid grid-cols-1 md:grid-cols-3 gap-4">
                  <div className="md:col-span-1 space-y-3">
                    <Select
                      className="min-w-[320px] max-w-[420px]"
                      label="对外IP"
                      selectedKeys={editIp ? [editIp] : []}
                      onOpenChange={async () => {
                        if (editNode) await fetchIfaces(editNode.id);
                      }}
                      onSelectionChange={(keys) => {
                        const k = Array.from(keys)[0] as string;

                        setEditIp(k || "");
                      }}
                    >
                      {Array.from(
                        new Set(
                          [
                            ...(ifaceCache[editNode?.id || 0] || []),
                            editNode?.serverIp || "",
                            editIp || "",
                          ].filter(Boolean),
                        ),
                      ).map((ip) => (
                        <SelectItem key={ip as string}>
                          {ip as string}
                        </SelectItem>
                      ))}
                    </Select>
                    <InputSmallNumber
                      label="开放端口"
                      value={editPort}
                      onChange={setEditPort}
                    />
                  </div>
                  <div className="md:col-span-2" />
                  <Select
                    className="md:col-span-3 min-w-[320px] max-w-[720px]"
                    label="连接到对端"
                    selectedKeys={editPeer ? [String(editPeer)] : []}
                    onSelectionChange={async (keys) => {
                      const k = Array.from(keys)[0] as string;
                      const v = k ? parseInt(k) : undefined;

                      setEditPeer(v);
                      setEditPeerIp("");
                      if (v) await fetchIfaces(v);
                    }}
                  >
                    {configuredNodes.map((n) => (
                      <SelectItem key={String(n.id)}>{n.name}</SelectItem>
                    ))}
                  </Select>
                  {editPeer && (
                    <Select
                      className="md:col-span-3 min-w-[320px] max-w-[720px]"
                      label="对端IP"
                      selectedKeys={editPeerIp ? [editPeerIp] : []}
                      onOpenChange={async () => {
                        if (editPeer) await fetchIfaces(editPeer);
                      }}
                      onSelectionChange={(keys) => {
                        const k = Array.from(keys)[0] as string;

                        setEditPeerIp(k || "");
                      }}
                    >
                      {Array.from(
                        new Set(
                          [
                            ...(ifaceCache[editPeer || 0] || []),
                            nodes.find((x) => x.id === editPeer)?.serverIp ||
                              "",
                          ].filter(Boolean),
                        ),
                      ).map((ip) => (
                        <SelectItem key={ip as string}>
                          {ip as string}
                        </SelectItem>
                      ))}
                    </Select>
                  )}
                </div>
              </ModalBody>
              <ModalFooter>
                <Button variant="light" onPress={onClose}>
                  取消
                </Button>
                <Button color="primary" onPress={doJoin}>
                  保存
                </Button>
              </ModalFooter>
            </>
          )}
        </ModalContent>
      </Modal>

      <Modal
        isOpen={logOpen}
        onOpenChange={(open) => {
          setLogOpen(open);
          if (!open) {
            logAbortRef.current?.abort();
            logAbortRef.current = null;
          }
        }}
      >
        <ModalContent className="w-[80vw] max-w-[80vw] h-[80vh]">
          {(onClose) => (
            <>
              <ModalHeader className="flex items-center justify-between">
                <div>
                  EasyTier 安装日志{" "}
                  <span className="text-xs text-default-500">
                    {logRequestId || "-"}
                  </span>
                </div>
                <div className="flex items-center gap-2">
                  {logDone ? (
                    <span className="text-xs text-success-600">已完成</span>
                  ) : (
                    <span className="text-xs text-default-500">
                      {logLoading ? "加载中..." : "流式中"}
                    </span>
                  )}
                </div>
              </ModalHeader>
              <ModalBody className="overflow-hidden">
                <pre className="h-[65vh] max-h-[65vh] overflow-auto whitespace-pre-wrap text-2xs bg-default-100 p-3 rounded">
                  {logLines.length ? logLines.join("\n") : "暂无日志"}
                </pre>
              </ModalBody>
              <ModalFooter>
                <Button
                  variant="light"
                  onPress={() => {
                    onClose();
                    setLogLines([]);
                    setLogRequestId("");
                  }}
                >
                  关闭
                </Button>
              </ModalFooter>
            </>
          )}
        </ModalContent>
      </Modal>

      <Modal isOpen={opsOpen} onOpenChange={setOpsOpen}>
        <ModalContent className="w-[80vw] max-w-[80vw] h-[80vh]">
          {(onClose) => (
            <>
              <ModalHeader className="flex items-center justify-between">
                <div>节点操作日志 · 节点 {opsNodeId || "-"}</div>
                <div>
                  <Button
                    isDisabled={!opsNodeId || opsLoading}
                    size="sm"
                    variant="flat"
                    onPress={reloadOps}
                  >
                    {opsLoading ? "刷新中..." : "刷新"}
                  </Button>
                </div>
              </ModalHeader>
              <ModalBody className="overflow-hidden">
                <pre className="h-[65vh] max-h-[65vh] overflow-auto whitespace-pre-wrap text-2xs bg-default-100 p-3 rounded">
                  {ops.length === 0
                    ? "暂无记录"
                    : ops
                        .map((o) => {
                          const t = new Date(o.timeMs).toLocaleString();
                          const head = `[${t}] ${o.cmd}`;
                          const body = (o.message || "").trim();
                          const lines = [`${head}  ${body}`];

                          if (o.stdout && o.stdout.trim())
                            lines.push(`${head}  stdout: ${o.stdout.trim()}`);
                          if (o.stderr && o.stderr.trim())
                            lines.push(`${head}  stderr: ${o.stderr.trim()}`);

                          return lines.join("\n");
                        })
                        .join("\n")}
                </pre>
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
    </div>
  );
}

function InputSmallNumber({
  label,
  value,
  onChange,
}: {
  label: string;
  value: number;
  onChange: (v: number) => void;
}) {
  return (
    <div className="flex flex-col min-w-[220px]">
      <label className="text-xs text-default-600 mb-1">{label}</label>
      <input
        className="px-3 py-2 rounded border border-default-300 bg-transparent text-sm w-56"
        min={1}
        placeholder="系统分配或手动填写"
        step={1}
        type="number"
        value={value || ""}
        onChange={(e) => onChange(parseInt(e.target.value || "0"))}
      />
    </div>
  );
}
