import { useState, useEffect } from "react";
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

import OpsLogModal from "@/components/OpsLogModal";
// import moved above; avoid duplicate react imports
import { getNodeInterfaces } from "@/api";
import {
  createTunnel,
  getTunnelList,
  updateTunnel,
  deleteTunnel,
  getNodeList,
  diagnoseTunnelStep,
  enableGostApi,
} from "@/api";

interface Tunnel {
  id: number;
  name: string;
  type: number; // 1: 端口转发, 2: 隧道转发
  inNodeId: number;
  outNodeId?: number;
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

export default function TunnelPage() {
  const [loading, setLoading] = useState(true);
  const [tunnels, setTunnels] = useState<Tunnel[]>([]);
  const [nodes, setNodes] = useState<Node[]>([]);
  // 操作日志弹窗（必须放在顶部，避免 Hooks 顺序变化）
  const [opsOpen, setOpsOpen] = useState(false);
  // 操作日志弹窗

  // 模态框状态
  const [modalOpen, setModalOpen] = useState(false);
  const [deleteModalOpen, setDeleteModalOpen] = useState(false);
  const [diagnosisModalOpen, setDiagnosisModalOpen] = useState(false);
  const [diagReqId, setDiagReqId] = useState<string>("");
  const [isEdit, setIsEdit] = useState(false);
  const [submitLoading, setSubmitLoading] = useState(false);
  const [deleteLoading, setDeleteLoading] = useState(false);
  const [diagnosisLoading, setDiagnosisLoading] = useState(false);
  const [tunnelToDelete, setTunnelToDelete] = useState<Tunnel | null>(null);
  const [currentDiagnosisTunnel, setCurrentDiagnosisTunnel] =
    useState<Tunnel | null>(null);
  const [diagnosisResult, setDiagnosisResult] =
    useState<DiagnosisResult | null>(null);
  // 多级路径（中间节点按顺序）
  const [midPath, setMidPath] = useState<number[]>([]);
  const [addMidNodeId, setAddMidNodeId] = useState<number | "">("");
  // 入口与中间节点的接口IP选择（出站接口IP）
  const [entryIface, setEntryIface] = useState<string>("");
  const [midIfaces, setMidIfaces] = useState<Record<number, string>>({});
  // 中间与出口节点的入站绑定IP（监听IP）
  const [midBindIps, setMidBindIps] = useState<Record<number, string>>({});
  const [exitBindIp, setExitBindIp] = useState<string>("");
  const [ifaceCache, setIfaceCache] = useState<Record<number, string[]>>({});

  // 表单状态
  const [form, setForm] = useState<TunnelForm>({
    name: "",
    type: 1,
    inNodeId: null,
    outNodeId: null,
    protocol: "tls",
    tcpListenAddr: "[::]",
    udpListenAddr: "[::]",
    interfaceName: "",
    flow: 1,
    trafficRatio: 1.0,
    status: 1,
  });

  // 表单验证错误
  const [errors, setErrors] = useState<{ [key: string]: string }>({});
  // 出口服务（SS）附加项
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
  // 入口节点 API 状态（用于弹窗内启用）
  const [entryApiOn, setEntryApiOn] = useState<boolean | null>(null);

  useEffect(() => {
    loadData();
  }, []);

  // 加载所有数据
  const loadData = async () => {
    setLoading(true);
    try {
      const [tunnelsRes, nodesRes] = await Promise.all([
        getTunnelList(),
        getNodeList(),
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
    } catch (error) {
      console.error("加载数据失败:", error);
      toast.error("加载数据失败");
    } finally {
      setLoading(false);
    }
  };

  // 拉取指定节点的接口IP并缓存
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

  // 表单验证
  const validateForm = (): boolean => {
    const newErrors: { [key: string]: string } = {};

    if (!form.name.trim()) {
      newErrors.name = "请输入隧道名称";
    } else if (form.name.length < 2 || form.name.length > 50) {
      newErrors.name = "隧道名称长度应在2-50个字符之间";
    }

    if (!form.inNodeId) {
      newErrors.inNodeId = "请选择入口节点";
    }

    if (!form.tcpListenAddr.trim()) {
      newErrors.tcpListenAddr = "请输入TCP监听地址";
    }

    if (!form.udpListenAddr.trim()) {
      newErrors.udpListenAddr = "请输入UDP监听地址";
    }

    if (form.trafficRatio < 0.0 || form.trafficRatio > 100.0) {
      newErrors.trafficRatio = "流量倍率必须在0.0-100.0之间";
    }

    // 隧道转发时的验证
    if (form.type === 2) {
      if (!form.outNodeId) {
        newErrors.outNodeId = "请选择出口节点";
      } else if (form.inNodeId === form.outNodeId) {
        newErrors.outNodeId = "隧道转发模式下，入口和出口不能是同一个节点";
      }

      if (!form.protocol) {
        newErrors.protocol = "请选择协议类型";
      }
    }

    setErrors(newErrors);

    return Object.keys(newErrors).length === 0;
  };

  // 新增隧道
  const handleAdd = () => {
    setIsEdit(false);
    setForm({
      name: "",
      type: 1,
      inNodeId: null,
      outNodeId: null,
      protocol: "tls",
      tcpListenAddr: "[::]",
      udpListenAddr: "[::]",
      interfaceName: "",
      flow: 1,
      trafficRatio: 1.0,
      status: 1,
    });
    setErrors({});
    setMidPath([]);
    setAddMidNodeId("");
    setEntryIface("");
    setMidIfaces({});
    setIfaceCache({});
    setExitPort(null);
    setExitPassword("");
    setExitMethod("AEAD_CHACHA20_POLY1305");
    setExitObserver("console");
    setExitLimiter("");
    setExitRLimiter("");
    setExitDeployed("");
    setExitMetaItems([]);
    setModalOpen(true);
    setEntryApiOn(null);
  };

  // 编辑隧道 - 只能修改部分字段
  const handleEdit = (tunnel: Tunnel) => {
    setIsEdit(true);
    setForm({
      id: tunnel.id,
      name: tunnel.name,
      type: tunnel.type,
      inNodeId: tunnel.inNodeId,
      outNodeId: tunnel.outNodeId || null,
      protocol: tunnel.protocol || "tls",
      tcpListenAddr: tunnel.tcpListenAddr || "[::]",
      udpListenAddr: tunnel.udpListenAddr || "[::]",
      interfaceName: tunnel.interfaceName || "",
      flow: tunnel.flow,
      trafficRatio: tunnel.trafficRatio,
      status: tunnel.status,
    });
    setErrors({});
    setMidPath([]);
    setAddMidNodeId("");
    // 拉取路径
    (async () => {
      try {
        const { getTunnelPath } = await import("@/api");
        const r: any = await getTunnelPath(tunnel.id);

        if (r.code === 0 && Array.isArray(r.data?.path))
          setMidPath(r.data.path);
      } catch {}
    })();
    setExitPort(null);
    setExitPassword("");
    setExitMethod("AEAD_CHACHA20_POLY1305");
    setExitObserver("console");
    setExitLimiter("");
    setExitRLimiter("");
    setExitDeployed("");
    setExitMetaItems([]);
    setModalOpen(true);
    // 更新入口节点 API 状态
    try {
      const n: any = nodes.find(
        (nn) => Number(nn.id) === Number(tunnel.inNodeId),
      );

      setEntryApiOn(
        typeof (n as any)?.gostApi !== "undefined"
          ? (n as any).gostApi === 1
          : null,
      );
    } catch {
      setEntryApiOn(null);
    }
    // 读取已保存的每节点接口IP
    (async () => {
      try {
        const { getTunnelIface } = await import("@/api");
        const r: any = await getTunnelIface(tunnel.id);

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
        const r: any = await getTunnelBind(tunnel.id);

        if (r.code === 0 && Array.isArray(r.data?.binds)) {
          const map: Record<number, string> = {};

          r.data.binds.forEach((x: any) => {
            if (x?.nodeId) map[Number(x.nodeId)] = String(x.ip || "");
          });
          setMidBindIps(map);
          if (tunnel.outNodeId && map[tunnel.outNodeId])
            setExitBindIp(map[tunnel.outNodeId]);
        }
      } catch {}
    })();
  };

  // 删除隧道
  const handleDelete = (tunnel: Tunnel) => {
    setTunnelToDelete(tunnel);
    setDeleteModalOpen(true);
  };

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

  // 隧道类型改变时的处理
  const handleTypeChange = (type: number) => {
    setForm((prev) => ({
      ...prev,
      type,
      outNodeId: type === 1 ? null : prev.outNodeId,
      protocol: type === 1 ? "tls" : prev.protocol,
    }));
    setExitDeployed("");
  };

  // 当入口节点变更时刷新 API 状态
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

  // 提交表单
  const handleSubmit = async () => {
    if (!validateForm()) return;

    setSubmitLoading(true);
    try {
      const data = { ...form };

      const response = isEdit
        ? await updateTunnel(data)
        : await createTunnel(data);

      if (response.code === 0) {
        // 保存多级路径、每节点出站接口IP、以及每节点监听IP（仅隧道转发）
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
                  ? candidates.sort((a, b) => (b.id || 0) - (a.id || 0))[0].id
                  : undefined;
            }
          }
          if (tid) {
            if (midPath.length > 0) await setTunnelPath(tid as number, midPath);
            // 出站接口IP（入口及中间）
            const ifaces: Array<{ nodeId: number; ip: string }> = [];

            if (form.inNodeId)
              ifaces.push({ nodeId: form.inNodeId, ip: entryIface || "" });
            midPath.forEach((nid) => {
              ifaces.push({ nodeId: nid, ip: midIfaces[nid] || "" });
            });
            if (ifaces.length > 0) await setTunnelIface(tid as number, ifaces);
            // 入站绑定IP（中间与出口）——仅隧道转发需要出口绑定，端口转发忽略出口
            const binds: Array<{ nodeId: number; ip: string }> = [];

            midPath.forEach((nid) => {
              binds.push({ nodeId: nid, ip: midBindIps[nid] || "" });
            });
            if (form.type === 2 && form.outNodeId)
              binds.push({ nodeId: form.outNodeId, ip: exitBindIp || "" });
            if (binds.length > 0) await setTunnelBind(tid as number, binds);
          }
        } catch {}
        toast.success(isEdit ? "更新成功" : "创建成功");
        // 入口/出口服务由转发创建时一并配置（forward.create/update 负责下发），此处不再直接创建SS
        setModalOpen(false);
        loadData();
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

  // 诊断隧道
  const handleDiagnose = async (tunnel: Tunnel) => {
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
  };

  // 获取显示的IP（处理多IP）
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

  // 获取节点名称
  const getNodeName = (nodeId?: number): string => {
    if (!nodeId) return "-";
    const node = nodes.find((n) => n.id === nodeId);

    return node ? node.name : `节点${nodeId}`;
  };

  // 获取状态显示
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

  // 获取类型显示
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

  // 获取流量计算显示
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
      <div className="flex items-center justify-center h-64">
        <div className="flex items-center gap-3">
          <Spinner size="sm" />
          <span className="text-default-600">正在加载...</span>
        </div>
      </div>
    );
  }

  return (
    <div className="px-3 lg:px-6 py-8">
      {/* 页面头部 */}
      <div className="flex items-center justify-between mb-6">
        <div className="flex-1" />
        <Button size="sm" variant="flat" onPress={() => setOpsOpen(true)}>
          操作日志
        </Button>
        <Button color="primary" size="sm" variant="flat" onPress={handleAdd}>
          新增
        </Button>
      </div>

      <OpsLogModal
        isOpen={opsOpen}
        requestId={diagReqId || undefined}
        onOpenChange={setOpsOpen}
      />
      {/* 隧道卡片网格 */}
      {tunnels.length > 0 ? (
        <div className="grid grid-cols-1 sm:grid-cols-2 lg:grid-cols-3 xl:grid-cols-2 2xl:grid-cols-3 gap-4">
          {tunnels.map((tunnel) => {
            const statusDisplay = getStatusDisplay(tunnel.status);
            const typeDisplay = getTypeDisplay(tunnel.type);

            return (
              <Card
                key={tunnel.id}
                className="shadow-sm border border-divider hover:shadow-md transition-shadow duration-200"
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
                    {/* 流程展示 */}
                    <div className="space-y-1.5">
                      <div className="p-2 bg-default-50 dark:bg-default-100/50 rounded border border-default-200 dark:border-default-300">
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

                      <div className="p-2 bg-default-50 dark:bg-default-100/50 rounded border border-default-200 dark:border-default-300">
                        <div className="flex items-center justify-between mb-1">
                          <span className="text-xs font-medium text-default-600">
                            {tunnel.type === 1
                              ? "出口节点（同入口）"
                              : "出口节点"}
                          </span>
                        </div>
                        <code className="text-xs font-mono text-foreground block truncate">
                          {tunnel.type === 1
                            ? getNodeName(tunnel.inNodeId)
                            : getNodeName(tunnel.outNodeId)}
                        </code>
                        <code className="text-xs font-mono text-default-500 block truncate">
                          {tunnel.type === 1
                            ? getDisplayIp(tunnel.inIp)
                            : getDisplayIp(tunnel.outIp)}
                        </code>
                      </div>
                    </div>

                    {/* 配置信息 */}
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
                      onPress={() => handleEdit(tunnel)}
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
                      onPress={() => handleDiagnose(tunnel)}
                    >
                      诊断
                    </Button>
                    {tunnel.type === 2 && (
                      <Button
                        className="flex-1 min-h-8"
                        color="secondary"
                        size="sm"
                        variant="flat"
                        onPress={async () => {
                          try {
                            const { checkTunnelPath } = await import("@/api");
                            const r: any = await checkTunnelPath(tunnel.id);

                            if (r.code === 0) {
                              const bad = (r.data?.hops || []).filter(
                                (h: any) =>
                                  !h.online ||
                                  (h.role === "mid" && !h.proposedPort),
                              ).length;

                              toast.success(
                                `路径检查完成：${(r.data?.hops || []).length} 跳，异常 ${bad} 处`,
                              );
                              setDiagnosisResult({
                                tunnelName: tunnel.name,
                                tunnelType: "隧道转发",
                                timestamp: Date.now(),
                                results: (r.data?.hops || []).map((h: any) => ({
                                  success:
                                    h.online &&
                                    (h.role !== "mid" || !!h.proposedPort),
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
                          }
                        }}
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
                      onPress={() => handleDelete(tunnel)}
                    >
                      删除
                    </Button>
                  </div>
                </CardBody>
              </Card>
            );
          })}
        </div>
      ) : (
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
      )}

      {/* 新增/编辑模态框 */}
      <Modal
        backdrop="blur"
        isOpen={modalOpen}
        placement="center"
        scrollBehavior="outside"
        size="2xl"
        onOpenChange={setModalOpen}
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

                  {/* 隧道(SS)参数已移除：统一在“节点信息 → 出口服务”配置 */}

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
                  <h3 className="text-lg font-semibold">入口配置</h3>

                  <Select
                    errorMessage={errors.inNodeId}
                    isDisabled={isEdit}
                    isInvalid={!!errors.inNodeId}
                    label="入口节点"
                    placeholder="请选择入口节点"
                    selectedKeys={
                      form.inNodeId ? [form.inNodeId.toString()] : []
                    }
                    variant="bordered"
                    onSelectionChange={(keys) => {
                      const selectedKey = Array.from(keys)[0] as string;

                      if (selectedKey) {
                        setForm((prev) => ({
                          ...prev,
                          inNodeId: parseInt(selectedKey),
                        }));
                      }
                    }}
                  >
                    {nodes.map((node) => (
                      <SelectItem
                        key={node.id}
                        textValue={`${node.name} (${node.status === 1 ? "在线" : "离线"})`}
                      >
                        <div className="flex items-center justify-between">
                          <span>{node.name}</span>
                          <Chip
                            color={node.status === 1 ? "success" : "danger"}
                            size="sm"
                            variant="flat"
                          >
                            {node.status === 1 ? "在线" : "离线"}
                          </Chip>
                        </div>
                      </SelectItem>
                    ))}
                  </Select>

                  {form.inNodeId ? (
                    <div className="p-3 border border-default-200 rounded-lg flex items-center justify-between">
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
                        value={exitPort ? String(exitPort) : ""}
                        onChange={(e) =>
                          setExitPort(Number((e.target as any).value))
                        }
                      />
                      <Input
                        label="出口密码(SS)"
                        placeholder="不少于6位"
                        value={exitPassword}
                        onChange={(e) =>
                          setExitPassword((e.target as any).value)
                        }
                      />
                      <Select
                        label="加密方法"
                        description="选择 Shadowsocks 加密方法"
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
                          <span className="text-default-400 text-small">
                            TCP
                          </span>
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
                          <span className="text-default-400 text-small">
                            UDP
                          </span>
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

                  {/* 多级路径（中间节点，按顺序，端口转发与隧道转发均支持） */}
                  {true && (
                    <div className="mt-2">
                      <h3 className="text-base font-semibold mb-1">多级路径</h3>
                      {/* 入口接口选择 */}
                      <div className="mb-2 text-sm">
                        <div className="flex items-center gap-2">
                          <span className="text-default-600">
                            入口出站IP(接口)
                          </span>
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
                            {(ifaceCache[form.inNodeId || 0] || []).map(
                              (ip) => (
                                <SelectItem key={ip}>{ip}</SelectItem>
                              ),
                            )}
                          </Select>
                        </div>
                      </div>
                      <div className="flex items-center gap-2 mb-2">
                        <Select
                          className="max-w-[260px]"
                          label="添加中间节点"
                          selectedKeys={
                            addMidNodeId ? [String(addMidNodeId)] : []
                          }
                          size="sm"
                          onSelectionChange={(keys) => {
                            const k = Array.from(keys)[0] as string;

                            if (!k) return;
                            const v = parseInt(k);

                            setAddMidNodeId(isNaN(v) ? "" : v);
                          }}
                        >
                          {nodes
                            .filter(
                              (n) =>
                                n.id !== form.inNodeId &&
                                n.id !== (form.outNodeId || 0) &&
                                !midPath.includes(n.id),
                            )
                            .map((n) => (
                              <SelectItem key={String(n.id)}>
                                {n.name}
                              </SelectItem>
                            ))}
                        </Select>
                        <Button
                          size="sm"
                          variant="flat"
                          onPress={() => {
                            if (addMidNodeId) {
                              const nid = Number(addMidNodeId);

                              if (!midPath.includes(nid))
                                setMidPath((prev) => [...prev, nid]);
                              setAddMidNodeId("");
                            }
                          }}
                        >
                          添加
                        </Button>
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
                                      size="sm"
                                      variant="flat"
                                      onPress={() => {
                                        setMidPath((prev) => {
                                          const i = prev.indexOf(nid);

                                          if (i <= 0) return prev.slice();
                                          const arr = prev.slice();
                                          const t = arr[i - 1];

                                          arr[i - 1] = arr[i];
                                          arr[i] = t;

                                          return arr;
                                        });
                                      }}
                                    >
                                      上移
                                    </Button>
                                    <Button
                                      size="sm"
                                      variant="flat"
                                      onPress={() => {
                                        setMidPath((prev) => {
                                          const i = prev.indexOf(nid);

                                          if (i < 0 || i >= prev.length - 1)
                                            return prev.slice();
                                          const arr = prev.slice();
                                          const t = arr[i + 1];

                                          arr[i + 1] = arr[i];
                                          arr[i] = t;

                                          return arr;
                                        });
                                      }}
                                    >
                                      下移
                                    </Button>
                                    <Button
                                      color="danger"
                                      size="sm"
                                      variant="flat"
                                      onPress={() =>
                                        setMidPath((prev) =>
                                          prev.filter((id) => id !== nid),
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
                        说明：入口→中间节点→出口
                        逐级直转；端口转发和隧道转发均可配置路径和每节点出站/入站IP。
                      </div>
                    </div>
                  )}

                  {/* 隧道转发时显示出口监听IP（下拉选择） */}
                  {form.type === 2 && (
                    <div className="space-y-2">
                      <Select
                        className="min-w-[320px] max-w-[380px]"
                        label="出口监听IP"
                        placeholder="请选择出口监听IP"
                        selectedKeys={exitBindIp ? [exitBindIp] : []}
                        variant="bordered"
                        onOpenChange={async () => {
                          if (form.outNodeId)
                            await fetchNodeIfaces(form.outNodeId);
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
                  )}

                  {/* 隧道转发时显示出口配置 */}
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

                      <Select
                        errorMessage={errors.outNodeId}
                        isDisabled={isEdit}
                        isInvalid={!!errors.outNodeId}
                        label="出口节点"
                        placeholder="请选择出口节点"
                        selectedKeys={
                          form.outNodeId ? [form.outNodeId.toString()] : []
                        }
                        variant="bordered"
                        onSelectionChange={(keys) => {
                          const selectedKey = Array.from(keys)[0] as string;

                          if (selectedKey) {
                            setForm((prev) => ({
                              ...prev,
                              outNodeId: parseInt(selectedKey),
                            }));
                            // 清空状态并尝试查询该节点是否已有SS服务
                            setExitDeployed("");
                            const nid = parseInt(selectedKey);

                            // 动态导入API以避免循环依赖（已顶层导入，此处直接使用也可）
                            import("@/api").then(({ queryNodeServices }) => {
                              queryNodeServices({ nodeId: nid, filter: "ss" })
                                .then((res: any) => {
                                  if (
                                    res.code === 0 &&
                                    Array.isArray(res.data)
                                  ) {
                                    const items = res.data as any[];
                                    const ss = items.find(
                                      (x) => x && x.handler === "ss",
                                    );

                                    if (ss) {
                                      const desc = `已部署: 端口 ${ss.port || ss.addr || "-"}，监听 ${ss.listening ? "是" : "否"}`;

                                      setExitDeployed(desc);
                                      if (!exitPort && ss.port)
                                        setExitPort(Number(ss.port));
                                    } else {
                                      setExitDeployed("未部署");
                                    }
                                  }
                                })
                                .catch(() => {});
                            });
                            // 获取该出口节点的接口IP列表（agent上报）
                            import("@/api").then(({ getNodeInterfaces }) => {
                              getNodeInterfaces(nid).catch(() => {});
                            });
                          }
                        }}
                      >
                        {nodes.map((node) => (
                          <SelectItem
                            key={node.id}
                            textValue={`${node.name} (${node.status === 1 ? "在线" : "离线"})`}
                          >
                            <div className="flex items-center justify-between">
                              <span>{node.name}</span>
                              <div className="flex items-center gap-2">
                                <Chip
                                  color={
                                    node.status === 1 ? "success" : "danger"
                                  }
                                  size="sm"
                                  variant="flat"
                                >
                                  {node.status === 1 ? "在线" : "离线"}
                                </Chip>
                                {form.inNodeId === node.id && (
                                  <Chip
                                    color="warning"
                                    size="sm"
                                    variant="flat"
                                  >
                                    已选为入口
                                  </Chip>
                                )}
                              </div>
                            </div>
                          </SelectItem>
                        ))}
                      </Select>

                      {/* 出口SS高级选项与状态 */}
                      <div className="grid grid-cols-1 md:grid-cols-3 gap-4">
                        <Input
                          description="默认 console，可留空"
                          label="观察器(observer)"
                          value={exitObserver}
                          onChange={(e) =>
                            setExitObserver((e.target as any).value)
                          }
                        />
                        <Input
                          description="可选，需在节点注册对应限速器"
                          label="限速(limiter)"
                          value={exitLimiter}
                          onChange={(e) =>
                            setExitLimiter((e.target as any).value)
                          }
                        />
                        <Input
                          description="可选，需在节点注册对应限速器"
                          label="连接限速(rlimiter)"
                          value={exitRLimiter}
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
                              setExitMetaItems(
                                (
                                  prev: Array<{
                                    id: number;
                                    key: string;
                                    value: string;
                                  }>,
                                ) => [
                                  ...prev,
                                  { id: Date.now(), key: "", value: "" },
                                ],
                              )
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
                                onChange={(e) =>
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
                                  setExitMetaItems(
                                    (
                                      prev: Array<{
                                        id: number;
                                        key: string;
                                        value: string;
                                      }>,
                                    ) =>
                                      prev.filter((x: any) => x.id !== it.id),
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
                {/* 诊断前的入口 API 提示 */}
                {currentDiagnosisTunnel && (
                  <div className="mb-3 p-3 border border-default-200 rounded-lg flex items-center justify-between">
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
        backdrop="blur"
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
