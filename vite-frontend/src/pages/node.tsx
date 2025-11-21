import { useState, useEffect, useRef } from "react";
import { useNavigate } from "react-router-dom";
import { Card, CardBody, CardHeader } from "@heroui/card";
import { Button } from "@heroui/button";
import { Input } from "@heroui/input";
import { Select, SelectItem } from "@heroui/select";
import { Textarea } from "@heroui/input";
import { Modal, ModalContent, ModalHeader, ModalBody, ModalFooter } from "@heroui/modal";
import { Chip } from "@heroui/chip";
import { Spinner } from "@heroui/spinner";
import { Alert } from "@heroui/alert";
import { Progress } from "@heroui/progress";
import OpsLogModal from '@/components/OpsLogModal';
import { Divider } from "@heroui/divider";
import { queryNodeServices, getNodeNetworkStatsBatch, getVersionInfo } from "@/api";
import toast from 'react-hot-toast';
import { getCachedConfig } from '@/config/site';


import { 
  createNode, 
  getNodeList, 
  updateNode, 
  deleteNode,
  getNodeInstallCommand,
  setExitNode,
  getExitNode,
  restartGost,
  agentReconcileNode,
  enableGostApi,
  getGostConfig
} from "@/api";

interface Node {
  id: number;
  name: string;
  ip: string;
  serverIp: string;
  portSta: number;
  portEnd: number;
  version?: string;
  status: number; // 1: 在线, 0: 离线
  connectionStatus: 'online' | 'offline';
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

export default function NodePage() {
  const navigate = useNavigate();
  const [nodeList, setNodeList] = useState<Node[]>([]);
  const [loading, setLoading] = useState(false);
  const [dialogVisible, setDialogVisible] = useState(false);
  const [dialogTitle, setDialogTitle] = useState('');
  const [isEdit, setIsEdit] = useState(false);
  const [submitLoading, setSubmitLoading] = useState(false);
  const [deleteModalOpen, setDeleteModalOpen] = useState(false);
  const [deleteLoading, setDeleteLoading] = useState(false);
  const [nodeToDelete, setNodeToDelete] = useState<Node | null>(null);
  const [deleteAlsoUninstall, setDeleteAlsoUninstall] = useState(false);
  const [form, setForm] = useState<NodeForm>({
    id: null,
    name: '',
    ipString: '',
    serverIp: '',
    portSta: 1000,
    portEnd: 65535
  });
  const [priceCents, setPriceCents] = useState<number | undefined>(undefined);
  const [cycleMonths, setCycleMonths] = useState<number | undefined>(undefined);
  const [startDateMs, setStartDateMs] = useState<number | undefined>(undefined);
  const [errors, setErrors] = useState<Record<string, string>>({});
  const [probeStat, setProbeStat] = useState<Record<number, {avg:number; latest:number|null; target?: {id:number; name?:string; ip?:string}}>>({});
  const [showNetwork, setShowNetwork] = useState(false);
  useEffect(()=>{ (async()=>{ try{ const v = await getCachedConfig('show_network'); setShowNetwork(v==='true'); }catch{} })(); },[]);

  // 出口服务设置
  const [exitModalOpen, setExitModalOpen] = useState(false);
  const [exitNodeId, setExitNodeId] = useState<number | null>(null);
  const [exitPort, setExitPort] = useState<number>(10000);
  const [exitPassword, setExitPassword] = useState<string>("");
  const [exitMethod, setExitMethod] = useState<string>("AEAD_CHACHA20_POLY1305");
  const [exitSubmitting, setExitSubmitting] = useState(false);
  const [exitObserver, setExitObserver] = useState<string>("console");
  const [exitLimiter, setExitLimiter] = useState<string>("");
  const [exitRLimiter, setExitRLimiter] = useState<string>("");
  const [exitMetaItems, setExitMetaItems] = useState<Array<{id:number, key:string, value:string}>>([]);
  const [exitIfaces, setExitIfaces] = useState<string[]>([]);
  const [exitIfaceSel, setExitIfaceSel] = useState<string>('');
  
  // 安装命令相关状态
  const [installCommandModal, setInstallCommandModal] = useState(false);
  const [installCommand, setInstallCommand] = useState('');
  const [currentNodeName, setCurrentNodeName] = useState('');
  const [gostConfigModal, setGostConfigModal] = useState<{open:boolean; loading:boolean; content:string; title:string}>({open:false, loading:false, content:'', title:''});
  
  const websocketRef = useRef<WebSocket | null>(null);
  const reconnectTimerRef = useRef<NodeJS.Timeout | null>(null);
  const reconnectAttemptsRef = useRef(0);
  const maxReconnectAttempts = 5;
  const [wsStatus, setWsStatus] = useState<'connected'|'connecting'|'disconnected'>('connecting');
  const [wsUrlShown, setWsUrlShown] = useState<string>('');
  const [serverVersion, setServerVersion] = useState<string>('');
  const [agentVersion, setAgentVersion] = useState<string>('');
  const [opsOpen, setOpsOpen] = useState(false);
  const [rstLoading, setRstLoading] = useState<Record<number, boolean>>({});
  const [reapplyLoading, setReapplyLoading] = useState<Record<number, boolean>>({});

  useEffect(() => {
    loadNodes();
    initWebSocket();
    
    return () => {
      closeWebSocket();
    };
  }, []);

  // 加载版本信息
  useEffect(() => {
    getVersionInfo().then((res:any)=>{
      if (res.code===0 && res.data){
        setServerVersion(res.data.server||'');
        setAgentVersion(res.data.agent||'');
      }
    }).catch(()=>{});
  }, []);

  // 加载节点列表
  const loadNodes = async () => {
    setLoading(true);
    try {
      const res = await getNodeList();
      if (res.code === 0) {
        setNodeList(res.data.map((node: any) => {
          const online = node.status === 1 ? 'online' : 'offline';
          const base: any = {
            ...node,
            connectionStatus: online,
            copyLoading: false
          };
          if (typeof node.gostApi !== 'undefined' || typeof node.gostRunning !== 'undefined') {
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
              gostApiConfigured: undefined
            };
          } else {
            base.systemInfo = null;
          }
          return base;
        }));
        // 批量拉取最近1小时探针概览（按配置可隐藏）
        if (showNetwork) {
          try {
            const r = await getNodeNetworkStatsBatch('1h');
            if (r.code === 0 && r.data) {
              const mapped: any = {};
              Object.keys(r.data).forEach((nid) => {
                const item = r.data[nid];
                mapped[Number(nid)] = { avg: item.avg ?? 0, latest: item.latest ?? null, target: item.latestTarget };
              });
              setProbeStat(mapped);
            }
          } catch {}
        }
      } else {
        toast.error(res.msg || '加载节点列表失败');
      }
    } catch (error) {
      toast.error('网络错误，请重试');
    } finally {
      setLoading(false);
    }
  };

  // 打开设置出口服务对话框
  const openExitModal = async (node: Node) => {
    setExitNodeId(node.id);
    // default values
    let dPort = node.portSta || 10000;
    let dPwd = "";
    let dMethod = "AEAD_CHACHA20_POLY1305";
    let dObserver = "console";
    let dLimiter = "";
    let dRLimiter = "";
    let dMetaItems: Array<{id:number, key:string, value:string}> = [];

    try {
      const res = await getExitNode(node.id);
      if (res.code === 0 && res.data) {
        const data = res.data as any;
        if (typeof data.port === 'number') dPort = data.port;
        if (typeof data.password === 'string') dPwd = data.password;
        if (typeof data.method === 'string' && data.method) dMethod = data.method;
        if (typeof data.observer === 'string') dObserver = data.observer || dObserver;
        if (typeof data.limiter === 'string') dLimiter = data.limiter || '';
        if (typeof data.rlimiter === 'string') dRLimiter = data.rlimiter || '';
        if (data.metadata && typeof data.metadata === 'object') {
          dMetaItems = Object.entries(data.metadata).map(([k,v])=>({id: Date.now()+Math.random(), key: String(k), value: String(v)}));
        }
      }
    } catch {}

    // 拉取该节点的接口IP列表（agent上报的全局地址）
    try {
      const { getNodeInterfaces } = await import('@/api');
      const rr: any = await getNodeInterfaces(node.id);
      const ips = (rr && rr.code === 0 && Array.isArray(rr.data?.ips)) ? rr.data.ips as string[] : [];
      setExitIfaces(ips);
    } catch { setExitIfaces([]); }
    setExitIfaceSel('');

    setExitPort(dPort);
    setExitPassword(dPwd);
    setExitMethod(dMethod);
    setExitObserver(dObserver);
    setExitLimiter(dLimiter);
    setExitRLimiter(dRLimiter);
    setExitMetaItems(dMetaItems);
    setExitModalOpen(true);
  };

  const addMonths = (ts: number, months: number): number => {
    const d = new Date(ts);
    const day = d.getDate();
    const targetMonth = d.getMonth() + months;
    const y = d.getFullYear() + Math.floor(targetMonth / 12);
    const m = ((targetMonth % 12) + 12) % 12;
    const lastDay = new Date(y, m + 1, 0).getDate();
    const newDay = Math.min(day, lastDay);
    const nd = new Date(y, m, newDay, d.getHours(), d.getMinutes(), d.getSeconds(), d.getMilliseconds());
    return nd.getTime();
  };

  const formatRemainDays = (node: Node) => {
    if (!node.cycleMonths || !node.startDateMs) return '';
    let months = node.cycleMonths;
    let exp: number | null = null;
    if (months > 0) {
      exp = addMonths(node.startDateMs, months);
      const now = Date.now();
      while (exp <= now) exp = addMonths(exp, months);
    } else {
      return '';
    }
    if (!exp) return '';
    const days = Math.max(0, Math.ceil((exp - Date.now()) / (24*3600*1000)));
    return `${days} 天`;
  };

  const goNetwork = (node: Node) => {
    navigate(`/network/${node.id}`);
  };

  const periodOptions = [
    { key: '1', label: '月' },
    { key: '3', label: '季度' },
    { key: '6', label: '半年' },
    { key: '12', label: '年' },
  ];

  const computeNextExpire = (start?: number, cycle?: number): number | null => {
    if (!start || !cycle) return null;
    let months = 0;
    switch (cycle) {
      case 30: months = 1; break;
      case 90: months = 3; break;
      case 180: months = 6; break;
      case 365: months = 12; break;
      default: months = 0; break;
    }
    if (months > 0) {
      let exp = addMonths(start, months);
      const now = Date.now();
      while (exp <= now) exp = addMonths(exp, months);
      return exp;
    }
    // fallback by days
    const cycleMs = cycle * 24 * 3600 * 1000;
    const now = Date.now();
    if (now <= start) return start + cycleMs;
    const elapsed = now - start;
    const k = Math.ceil(elapsed / cycleMs);
    return start + k * cycleMs;
  };

  // 刷新节点服务状态（仅查询 ss）
  const refreshServices = async (node: Node) => {
    setNodeList(prev => prev.map(n => n.id === node.id ? { ...n, ssLoading: true } : n));
    try {
      const res = await queryNodeServices({ nodeId: node.id, filter: 'ss' });
      if (res.code === 0 && Array.isArray(res.data)) {
        const items = res.data as any[];
        const ss = items.find(x => x && x.handler === 'ss');
        const desc = ss ? `SS: 端口 ${ss.port || ss.addr || '-'}，监听 ${ss.listening ? '是' : '否'}` : 'SS: 未部署';
        setNodeList(prev => prev.map(n => n.id === node.id ? { ...n, ssStatus: desc, ssLoading: false } : n));
      } else {
        setNodeList(prev => prev.map(n => n.id === node.id ? { ...n, ssStatus: 'SS: 查询失败', ssLoading: false } : n));
      }
    } catch {
      setNodeList(prev => prev.map(n => n.id === node.id ? { ...n, ssStatus: 'SS: 查询失败', ssLoading: false } : n));
    }
  };

  // 提交出口服务设置
  const submitExit = async () => {
    if (!exitNodeId) { toast.error('无效的节点'); return; }
    if (!exitPort || exitPort < 1 || exitPort > 65535) { toast.error('端口无效'); return; }
    if (!exitPassword) { toast.error('请填写密码'); return; }
    setExitSubmitting(true);
    try {
      const metadata: any = {};
      exitMetaItems.forEach((it: {key:string; value:string}) => { if (it.key && it.value) metadata[it.key] = it.value });
      if (exitIfaceSel) { (metadata as any)['interface'] = exitIfaceSel }
      const res = await setExitNode({ nodeId: exitNodeId, port: exitPort, password: exitPassword, method: exitMethod, 
        observer: exitObserver, limiter: exitLimiter, rlimiter: exitRLimiter, metadata } as any);
      if (res.code === 0) { toast.success('出口服务已创建/更新'); setExitModalOpen(false); }
      else { toast.error(res.msg || '操作失败'); }
    } catch (e) {
      toast.error('网络错误');
    } finally {
      setExitSubmitting(false);
    }
  };

  // 初始化WebSocket连接
  const initWebSocket = () => {
    setWsStatus('connecting');
    if (websocketRef.current && 
        (websocketRef.current.readyState === WebSocket.OPEN || 
         websocketRef.current.readyState === WebSocket.CONNECTING)) {
      return;
    }
    
    if (websocketRef.current) {
      closeWebSocket();
    }
    
    // 构建 WebSocket URL：固定基于浏览器当前 origin，避免 baseURL 末尾斜杠/环境差异导致的不稳定
    const loc = window.location;
    const wsScheme = loc.protocol === 'https:' ? 'wss' : 'ws';
    const wsUrl = `${wsScheme}://${loc.host}/system-info?type=0`;
    setWsUrlShown(wsUrl);
    
    try {
      websocketRef.current = new WebSocket(wsUrl);
      
      websocketRef.current.onopen = () => {
        reconnectAttemptsRef.current = 0;
        setWsStatus('connected');
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
        setWsStatus('disconnected');
      };
      
      websocketRef.current.onclose = () => {
        websocketRef.current = null;
        setWsStatus('disconnected');
        attemptReconnect();
      };
    } catch (error) {
      setWsStatus('disconnected');
      attemptReconnect();
    }
  };

  // 处理WebSocket消息
  const handleWebSocketMessage = (data: any) => {
    const { id, type, data: messageData } = data;
    
    if (type === 'status') {
      setNodeList(prev => prev.map(node => {
        if (node.id == id) {
          return {
            ...node,
            connectionStatus: messageData === 1 ? 'online' : 'offline',
            systemInfo: messageData === 0 ? null : node.systemInfo
          };
        }
        return node;
      }));
    } else if (type === 'info') {
      setNodeList(prev => prev.map(node => {
        if (node.id == id) {
          try {
            let systemInfo;
            if (typeof messageData === 'string') {
              systemInfo = JSON.parse(messageData);
            } else {
              systemInfo = messageData;
            }
            
            const currentUpload = parseInt(systemInfo.bytes_transmitted) || 0;
            const currentDownload = parseInt(systemInfo.bytes_received) || 0;
            const currentUptime = parseInt(systemInfo.uptime) || 0;
            
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
              connectionStatus: 'online',
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
                gostApiConfigured: !!systemInfo.gost_api_configured
              }
            };
          } catch (error) {
            return node;
          }
        }
        return node;
      }));
    }
  };

  // 尝试重新连接
  const attemptReconnect = () => {
    if (reconnectAttemptsRef.current < maxReconnectAttempts) {
      reconnectAttemptsRef.current++;
      
      reconnectTimerRef.current = setTimeout(() => {
        setWsStatus('connecting');
        initWebSocket();
      }, 3000 * reconnectAttemptsRef.current);
    }
  };

  const doRestartGost = async (nodeId:number) => {
    setRstLoading(prev=>({ ...prev, [nodeId]: true }));
    try {
      const r:any = await restartGost(nodeId);
      if (r && r.code === 0) {
        const ok = !!r.data?.success;
        toast[ok? 'success':'error'](r.data?.message || (ok? '已下发重启':'重启失败'));
      } else {
        toast.error(r?.msg || '重启失败');
      }
    } catch {
      toast.error('重启失败');
    } finally {
      setRstLoading(prev=>({ ...prev, [nodeId]: false }));
    }
  };

  const doReapply = async (nodeId:number) => {
    setReapplyLoading(prev=>({ ...prev, [nodeId]: true }));
    try {
      const r:any = await agentReconcileNode(nodeId);
      if (r && r.code === 0) {
        toast.success(`已触发重新应用，推送数量: ${r.data?.pushed ?? 0}`);
      } else {
        toast.error(r?.msg || '触发失败');
      }
    } catch {
      toast.error('触发失败');
    } finally {
      setReapplyLoading(prev=>({ ...prev, [nodeId]: false }));
    }
  };

  const showGostConfig = async (node: Node) => {
    setGostConfigModal({open:true, loading:true, content:'', title:`${node.name} - GOST 配置`});
    try{
      const res:any = await getGostConfig(node.id);
      if(res.code===0){
        const content = (res.data?.content as string) || '无返回内容';
        setGostConfigModal({open:true, loading:false, content, title:`${node.name} - GOST 配置`});
      }else{
        toast.error(res.msg || '获取配置失败');
        setGostConfigModal(prev => ({...prev, loading:false}));
      }
    }catch(e:any){
      toast.error(e?.message || '获取配置失败');
      setGostConfigModal(prev => ({...prev, loading:false}));
    }
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
      
      if (websocketRef.current.readyState === WebSocket.OPEN || 
          websocketRef.current.readyState === WebSocket.CONNECTING) {
        websocketRef.current.close();
      }
      
      websocketRef.current = null;
    }
    
    setNodeList(prev => prev.map(node => ({
      ...node,
      connectionStatus: 'offline',
      systemInfo: null
    })));
  };


  
  // 格式化速度
  const formatSpeed = (bytesPerSecond: number): string => {
    if (bytesPerSecond === 0) return '0 B/s';
    
    const k = 1024;
    const sizes = ['B/s', 'KB/s', 'MB/s', 'GB/s', 'TB/s'];
    const i = Math.floor(Math.log(bytesPerSecond) / Math.log(k));
    
    return parseFloat((bytesPerSecond / Math.pow(k, i)).toFixed(2)) + ' ' + sizes[i];
  };

  // 格式化开机时间
  const formatUptime = (seconds: number): string => {
    if (seconds === 0) return '-';
    
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
    if (bytes === 0) return '0 B';
    
    const k = 1024;
    const sizes = ['B', 'KB', 'MB', 'GB', 'TB'];
    const i = Math.floor(Math.log(bytes) / Math.log(k));
    
    return parseFloat((bytes / Math.pow(k, i)).toFixed(2)) + ' ' + sizes[i];
  };

  // 获取进度条颜色
  const getProgressColor = (value: number, offline = false): "default" | "primary" | "secondary" | "success" | "warning" | "danger" => {
    if (offline) return "default";
    if (value <= 50) return "success";
    if (value <= 80) return "warning";
    return "danger";
  };

  // 验证IP地址格式
  const validateIp = (ip: string): boolean => {
    if (!ip || !ip.trim()) return false;
    
    const trimmedIp = ip.trim();
    
    // IPv4格式验证
    const ipv4Regex = /^(25[0-5]|2[0-4][0-9]|[01]?[0-9][0-9]?)\.(25[0-5]|2[0-4][0-9]|[01]?[0-9][0-9]?)\.(25[0-5]|2[0-4][0-9]|[01]?[0-9][0-9]?)\.(25[0-5]|2[0-4][0-9]|[01]?[0-9][0-9]?)$/;
    
    // IPv6格式验证
    const ipv6Regex = /^(([0-9a-fA-F]{1,4}:){7,7}[0-9a-fA-F]{1,4}|([0-9a-fA-F]{1,4}:){1,7}:|([0-9a-fA-F]{1,4}:){1,6}:[0-9a-fA-F]{1,4}|([0-9a-fA-F]{1,4}:){1,5}(:[0-9a-fA-F]{1,4}){1,2}|([0-9a-fA-F]{1,4}:){1,4}(:[0-9a-fA-F]{1,4}){1,3}|([0-9a-fA-F]{1,4}:){1,3}(:[0-9a-fA-F]{1,4}){1,4}|([0-9a-fA-F]{1,4}:){1,2}(:[0-9a-fA-F]{1,4}){1,5}|[0-9a-fA-F]{1,4}:((:[0-9a-fA-F]{1,4}){1,6})|:((:[0-9a-fA-F]{1,4}){1,7}|:)|fe80:(:[0-9a-fA-F]{0,4}){0,4}%[0-9a-zA-Z]{1,}|::(ffff(:0{1,4}){0,1}:){0,1}((25[0-5]|(2[0-4]|1{0,1}[0-9]){0,1}[0-9])\.){3,3}(25[0-5]|(2[0-4]|1{0,1}[0-9]){0,1}[0-9])|([0-9a-fA-F]{1,4}:){1,4}:((25[0-5]|(2[0-4]|1{0,1}[0-9]){0,1}[0-9])\.){3,3}(25[0-5]|(2[0-4]|1{0,1}[0-9]){0,1}[0-9]))$/;
    
    if (ipv4Regex.test(trimmedIp) || ipv6Regex.test(trimmedIp) || trimmedIp === 'localhost') {
      return true;
    }
    
    // 验证域名格式
    if (/^\d+$/.test(trimmedIp)) return false;
    
    const domainRegex = /^[a-zA-Z0-9]([a-zA-Z0-9\-]{0,61}[a-zA-Z0-9])?(\.[a-zA-Z0-9]([a-zA-Z0-9\-]{0,61}[a-zA-Z0-9])?)+$/;
    const singleLabelDomain = /^[a-zA-Z][a-zA-Z0-9\-]{0,62}$/;
    
    return domainRegex.test(trimmedIp) || singleLabelDomain.test(trimmedIp);
  };

  // 表单验证
  const validateForm = (): boolean => {
    const newErrors: Record<string, string> = {};
    
    if (!form.name.trim()) {
      newErrors.name = '请输入节点名称';
    } else if (form.name.trim().length < 2) {
      newErrors.name = '节点名称长度至少2位';
    } else if (form.name.trim().length > 50) {
      newErrors.name = '节点名称长度不能超过50位';
    }
    
    if (!form.ipString.trim()) {
      newErrors.ipString = '请输入入口IP地址';
    } else {
      const ips = form.ipString.split('\n').map(ip => ip.trim()).filter(ip => ip);
      if (ips.length === 0) {
        newErrors.ipString = '请输入至少一个有效IP地址';
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
      newErrors.serverIp = '请输入服务器IP地址';
    } else if (!validateIp(form.serverIp.trim())) {
      newErrors.serverIp = '请输入有效的IPv4、IPv6地址或域名';
    }
    
    if (!form.portSta || form.portSta < 1 || form.portSta > 65535) {
      newErrors.portSta = '端口范围必须在1-65535之间';
    }
    
    if (!form.portEnd || form.portEnd < 1 || form.portEnd > 65535) {
      newErrors.portEnd = '端口范围必须在1-65535之间';
    } else if (form.portEnd < form.portSta) {
      newErrors.portEnd = '结束端口不能小于起始端口';
    }
    
    setErrors(newErrors);
    return Object.keys(newErrors).length === 0;
  };

  // 新增节点
  const handleAdd = () => {
    setDialogTitle('新增节点');
    setIsEdit(false);
    setDialogVisible(true);
    resetForm();
  };

  // 编辑节点
  const handleEdit = (node: Node) => {
    setDialogTitle('编辑节点');
    setIsEdit(true);
    setForm({
      id: node.id,
      name: node.name,
      ipString: node.ip ? node.ip.split(',').map(ip => ip.trim()).join('\n') : '',
      serverIp: node.serverIp || '',
      portSta: node.portSta,
      portEnd: node.portEnd
    });
    setPriceCents(node.priceCents);
    setCycleMonths(node.cycleMonths);
    setStartDateMs(node.startDateMs);
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
        toast.success('删除成功');
        setNodeList(prev => prev.filter(n => n.id !== nodeToDelete.id));
        setDeleteModalOpen(false);
        setNodeToDelete(null);
      } else {
        toast.error(res.msg || '删除失败');
      }
    } catch (error) {
      toast.error('网络错误，请重试');
    } finally {
      setDeleteLoading(false);
    }
  };

  // 复制安装命令
  const handleCopyInstallCommand = async (node: Node) => {
    setNodeList(prev => prev.map(n => 
      n.id === node.id ? { ...n, copyLoading: true } : n
    ));
    
    try {
      const res = await getNodeInstallCommand(node.id);
      if (res.code === 0 && res.data) {
        try {
          await navigator.clipboard.writeText(res.data);
          toast.success('安装命令已复制到剪贴板');
        } catch (copyError) {
          // 复制失败，显示安装命令模态框
          setInstallCommand(res.data);
          setCurrentNodeName(node.name);
          setInstallCommandModal(true);
        }
      } else {
        toast.error(res.msg || '获取安装命令失败');
      }
    } catch (error) {
      toast.error('获取安装命令失败');
    } finally {
      setNodeList(prev => prev.map(n => 
        n.id === node.id ? { ...n, copyLoading: false } : n
      ));
    }
  };

  // 手动复制安装命令
  const handleManualCopy = async () => {
    try {
      await navigator.clipboard.writeText(installCommand);
      toast.success('安装命令已复制到剪贴板');
      setInstallCommandModal(false);
    } catch (error) {
      toast.error('复制失败，请手动选择文本复制');
    }
  };

  // 提交表单
  const handleSubmit = async () => {
    if (!validateForm()) return;
    
    setSubmitLoading(true);
    
    try {
      const ipString = form.ipString
        .split('\n')
        .map(ip => ip.trim())
        .filter(ip => ip)
        .join(',');
        
      const submitData: any = {
        ...form,
        ip: ipString
      };
      delete (submitData as any).ipString;
      if (priceCents != null) submitData.priceCents = priceCents;
      if (cycleMonths != null) submitData.cycleMonths = cycleMonths;
      if (startDateMs != null) submitData.startDateMs = startDateMs;
      
      const apiCall = isEdit ? updateNode : createNode;
      const data: any = isEdit ? submitData : { 
        name: form.name, 
        ip: ipString,
        serverIp: form.serverIp,
        portSta: form.portSta,
        portEnd: form.portEnd
      };
      if (!isEdit) {
        if (priceCents != null) data.priceCents = priceCents;
        if (cycleMonths != null) data.cycleMonths = cycleMonths;
        if (startDateMs != null) data.startDateMs = startDateMs;
      }
      
      const res = await apiCall(data);
      if (res.code === 0) {
        toast.success(isEdit ? '更新成功' : '创建成功');
        setDialogVisible(false);
        
        if (isEdit) {
          setNodeList(prev => prev.map(n => 
            n.id === form.id ? {
              ...n,
              name: form.name,
              ip: ipString,
              serverIp: form.serverIp,
              portSta: form.portSta,
              portEnd: form.portEnd
            } : n
          ));
        } else {
          loadNodes();
        }
      } else {
        toast.error(res.msg || (isEdit ? '更新失败' : '创建失败'));
      }
    } catch (error) {
      toast.error('网络错误，请重试');
    } finally {
      setSubmitLoading(false);
    }
  };

  // 重置表单
  const resetForm = () => {
    setForm({
      id: null,
      name: '',
      ipString: '',
      serverIp: '',
      portSta: 1000,
      portEnd: 65535
    });
    setErrors({});
  };

  return (
    
      <div className="px-3 lg:px-6 py-8">
        {/* 页面头部 */}
        <div className="flex items-center justify-between mb-6">
        <div className="flex items-center gap-4">
          <div className="flex items-center gap-2 text-sm">
            <span className={`inline-block w-2 h-2 rounded-full ${wsStatus==='connected' ? 'bg-green-500' : (wsStatus==='connecting' ? 'bg-yellow-500' : 'bg-red-500')}`}></span>
            <span className="text-default-600">
              {wsStatus==='connected' ? 'WS 已连接' : wsStatus==='connecting' ? 'WS 连接中…' : 'WS 未连接（自动重试）'}
            </span>
          </div>
          <div className="hidden md:block text-xs text-default-500 truncate max-w-[420px]" title={wsUrlShown}> {wsUrlShown || '-'}</div>
          <div className="text-xs text-default-500">后端: {serverVersion||'-'} · Agent: {agentVersion||'-'}</div>
        </div>

        <Button
              size="sm"
              variant="flat"
              color="primary"
              onPress={handleAdd}
             
            >
              新增
            </Button>
     
        </div>

        {/* 节点列表 */}
        {loading ? (
          <div className="flex items-center justify-center h-64">
            <div className="flex items-center gap-3">
              <Spinner size="sm" />
              <span className="text-default-600">正在加载...</span>
            </div>
          </div>
        ) : nodeList.length === 0 ? (
          <Card className="shadow-sm border border-gray-200 dark:border-gray-700">
            <CardBody className="text-center py-16">
              <div className="flex flex-col items-center gap-4">
                <div className="w-16 h-16 bg-default-100 rounded-full flex items-center justify-center">
                  <svg className="w-8 h-8 text-default-400" fill="none" stroke="currentColor" viewBox="0 0 24 24">
                    <path strokeLinecap="round" strokeLinejoin="round" strokeWidth={1.5} d="M5 12h14M5 12l4-4m-4 4l4 4" />
                  </svg>
                </div>
                <div>
                  <h3 className="text-lg font-semibold text-foreground">暂无节点配置</h3>
                  <p className="text-default-500 text-sm mt-1">还没有创建任何节点配置，点击上方按钮开始创建</p>
                </div>
              </div>
            </CardBody>
          </Card>
        ) : (
          <>
          <div className="flex justify-end mb-2 gap-2">
            <Button size="sm" variant="flat" onPress={()=> setOpsOpen(true)}>操作日志</Button>
            <Button size="sm" color="primary" variant="flat" onPress={async()=>{
              try {
                const r:any = await getNodeList();
                if (r && r.code === 0 && Array.isArray(r.data)) {
                  let ok = 0; let total = r.data.length;
                  for (const n of r.data) { try { const rr:any = await agentReconcileNode(n.id); if (rr && rr.code===0) ok++; } catch {} }
                  toast.success(`已触发重新应用：${ok}/${total}`);
                } else { toast.error(r?.msg || '获取节点列表失败'); }
              } catch { toast.error('操作失败'); }
            }}>批量重新应用</Button>
          </div>
          <div className="grid grid-cols-1 sm:grid-cols-2 lg:grid-cols-3 xl:grid-cols-2 2xl:grid-cols-3 gap-4">
            {nodeList.map((node) => (
              <Card 
                key={node.id} 
                className="shadow-sm border border-divider hover:shadow-md transition-shadow duration-200"
              >
                <CardHeader className="pb-2">
                  <div className="flex justify-between items-start w-full">
                    <div className="flex-1 min-w-0">
                      <h3 className="font-semibold text-foreground truncate text-sm">{node.name}</h3>
                      <p className="text-xs text-default-500 truncate">{node.serverIp}</p>
                    </div>
                    <div className="flex items-center gap-1.5 ml-2">
                      <Chip 
                        color={node.connectionStatus === 'online' ? 'success' : 'danger'} 
                        variant="flat" 
                        size="sm"
                        className="text-xs"
                      >
                        {node.connectionStatus === 'online' ? '在线' : '离线'}
                      </Chip>
                    </div>
                  </div>
                </CardHeader>

                <CardBody className="pt-0 pb-3">
                  {/* 基础信息 */}
                  <div className="space-y-2 mb-4" onClick={() => goNetwork(node)} style={{ cursor: 'pointer' }}>
                    <div className="flex justify-between items-center text-sm min-w-0">
                      <span className="text-default-600 flex-shrink-0">入口IP</span>
                      <div className="text-right text-xs min-w-0 flex-1 ml-2">
                        {node.ip ? (
                          node.ip.split(',').length > 1 ? (
                            <span className="font-mono truncate block" title={node.ip.split(',')[0].trim()}>
                              {node.ip.split(',')[0].trim()} +{node.ip.split(',').length - 1}个
                            </span>
                          ) : (
                            <span className="font-mono truncate block" title={node.ip.trim()}>
                              {node.ip.trim()}
                            </span>
                          )
                        ) : '-'}
                      </div>
                    </div>
                    <div className="flex justify-between text-sm">
                      <span className="text-default-600">端口</span>
                      <span className="text-xs">{node.portSta}-{node.portEnd}</span>
                    </div>
                    {showNetwork && (
                      <div className="flex justify-between text-sm">
                        <span className="text-default-600">网络</span>
                        <span className="text-xs">
                          {probeStat[node.id]?.latest!=null ? `${probeStat[node.id]?.latest} ms` : '-'}
                          {probeStat[node.id]?.avg? ` · 平均 ${probeStat[node.id]?.avg} ms` : ''}
                          {probeStat[node.id]?.target?.name ? ` · ${probeStat[node.id]?.target?.name}(${probeStat[node.id]?.target?.ip || ''})` : ''}
                        </span>
                      </div>
                    )}
                    {(node.priceCents || node.cycleMonths) && (
                      <div className="flex justify-between text-sm">
                        <span className="text-default-600">计费</span>
                        <span className="text-xs">
                          {node.priceCents ? `¥${(node.priceCents/100).toFixed(2)}` : ''}
                          {node.cycleMonths ? ` / ${node.cycleMonths===1?'月':node.cycleMonths===3?'季度':node.cycleMonths===6?'半年':node.cycleMonths===12?'年':node.cycleMonths+'月'}` : ''}
                          {node.startDateMs ? ` · 剩余${formatRemainDays(node)}` : ''}
                        </span>
                      </div>
                    )}
                  <div className="flex justify-between text-sm">
                    <span className="text-default-600">版本</span>
                    <span className="text-xs">{node.version || '未知'}</span>
                  </div>
                  <div className="flex justify-between text-sm">
                    <span className="text-default-600">GOST 服务</span>
                    <span className="text-xs">
                      {node.connectionStatus === 'online' && node.systemInfo
                        ? (node.systemInfo.gostRunning ? '运行中' : '未运行')
                        : '-'}
                    </span>
                  </div>
                  <div className="flex justify-between text-sm items-center">
                    <span className="text-default-600">API 配置</span>
                    <span className="text-xs flex items-center gap-2">
                      {node.connectionStatus === 'online' && node.systemInfo ? (
                        (node.systemInfo as any).gostApiConfigured === false ? (
                          <Button size="sm" color="primary" variant="flat" onPress={async()=>{
                            try {
                              await enableGostApi(node.id);
                              toast.success('已发送启用 GOST API 指令，稍候刷新');
                            } catch(e:any){
                              toast.error(e?.message || '指令发送失败');
                            }
                          }}>开启 GOST API</Button>
                        ) : (node.systemInfo as any).gostApiConfigured === true ? (
                          <Button size="sm" color="secondary" variant="flat" onPress={()=>showGostConfig(node)}>查看 GOST 配置</Button>
                        ) : (
                          '检测中…'
                        )
                      ) : (
                        '-'
                      )}
                    </span>
                  </div>
                    <div className="flex justify-between items-center text-sm">
                      <span className="text-default-600">服务</span>
                      <span className="text-xs flex items-center gap-2">
                        {node.ssStatus ? node.ssStatus : '-'}
                        <Button size="sm" variant="light" onPress={() => refreshServices(node)} isLoading={node.ssLoading}>
                          刷新
                        </Button>
                        <Button size="sm" color="warning" variant="flat" onPress={()=>doRestartGost(node.id)} isLoading={!!rstLoading[node.id]}>
                          重启 GOST
                        </Button>
                        <Button size="sm" color="primary" variant="flat" onPress={()=>doReapply(node.id)} isLoading={!!reapplyLoading[node.id]}>
                          重新应用服务
                        </Button>
                      </span>
                    </div>
                    <div className="flex justify-between text-sm">
                      <span className="text-default-600">开机时间</span>
                      <span className="text-xs">
                        {node.connectionStatus === 'online' && node.systemInfo 
                          ? formatUptime(node.systemInfo.uptime)
                          : '-'
                        }
                      </span>
                    </div>
                  </div>

                  {/* 系统监控 */}
                  <div className="space-y-3 mb-4">
                    <div className="grid grid-cols-2 gap-3">
                      <div>
                        <div className="flex justify-between text-xs mb-1">
                          <span>CPU</span>
                          <span className="font-mono">
                            {node.connectionStatus === 'online' && node.systemInfo 
                              ? `${node.systemInfo.cpuUsage.toFixed(1)}%` 
                              : '-'
                            }
                          </span>
                        </div>
                        <Progress
                          value={node.connectionStatus === 'online' && node.systemInfo ? node.systemInfo.cpuUsage : 0}
                          color={getProgressColor(
                            node.connectionStatus === 'online' && node.systemInfo ? node.systemInfo.cpuUsage : 0,
                            node.connectionStatus !== 'online'
                          )}
                          size="sm"
                          aria-label="CPU使用率"
                        />
                      </div>
                      <div>
                        <div className="flex justify-between text-xs mb-1">
                          <span>内存</span>
                          <span className="font-mono">
                            {node.connectionStatus === 'online' && node.systemInfo 
                              ? `${node.systemInfo.memoryUsage.toFixed(1)}%` 
                              : '-'
                            }
                          </span>
                        </div>
                        <Progress
                          value={node.connectionStatus === 'online' && node.systemInfo ? node.systemInfo.memoryUsage : 0}
                          color={getProgressColor(
                            node.connectionStatus === 'online' && node.systemInfo ? node.systemInfo.memoryUsage : 0,
                            node.connectionStatus !== 'online'
                          )}
                          size="sm"
                          aria-label="内存使用率"
                        />
                      </div>
                    </div>

                    <div className="grid grid-cols-2 gap-2 text-xs">
                      <div className="text-center p-2 bg-default-50 dark:bg-default-100 rounded">
                        <div className="text-default-600 mb-0.5">上传</div>
                        <div className="font-mono">
                          {node.connectionStatus === 'online' && node.systemInfo 
                            ? formatSpeed(node.systemInfo.uploadSpeed) 
                            : '-'
                          }
                        </div>
                      </div>
                      <div className="text-center p-2 bg-default-50 dark:bg-default-100 rounded">
                        <div className="text-default-600 mb-0.5">下载</div>
                        <div className="font-mono">
                          {node.connectionStatus === 'online' && node.systemInfo 
                            ? formatSpeed(node.systemInfo.downloadSpeed) 
                            : '-'
                          }
                        </div>
                      </div>
                    </div>

                    {/* 流量统计 */}
                    <div className="grid grid-cols-2 gap-2 text-xs">
                      <div className="text-center p-2 bg-primary-50 dark:bg-primary-100/20 rounded border border-primary-200 dark:border-primary-300/20">
                        <div className="text-primary-600 dark:text-primary-400 mb-0.5">↑ 上行流量</div>
                        <div className="font-mono text-primary-700 dark:text-primary-300">
                          {node.connectionStatus === 'online' && node.systemInfo 
                            ? formatTraffic(node.systemInfo.uploadTraffic) 
                            : '-'
                          }
                        </div>
                      </div>
                      <div className="text-center p-2 bg-success-50 dark:bg-success-100/20 rounded border border-success-200 dark:border-success-300/20">
                        <div className="text-success-600 dark:text-success-400 mb-0.5">↓ 下行流量</div>
                        <div className="font-mono text-success-700 dark:text-success-300">
                          {node.connectionStatus === 'online' && node.systemInfo 
                            ? formatTraffic(node.systemInfo.downloadTraffic) 
                            : '-'
                          }
                        </div>
                      </div>
                    </div>
                  </div>

                  {/* 操作按钮 */}
                  <div className="space-y-1.5">
                    <div className="flex gap-1.5">
                      <Button
                        size="sm"
                        variant="flat"
                        color="success"
                        onPress={() => handleCopyInstallCommand(node)}
                        isLoading={node.copyLoading}
                        className="flex-1 min-h-8"
                      >
                        安装
                      </Button>
                      <Button
                        size="sm"
                        variant="flat"
                        color="warning"
                        onPress={() => openExitModal(node)}
                        className="flex-1 min-h-8"
                      >
                        出口
                      </Button>
                      <Button
                        size="sm"
                        variant="flat"
                        color="primary"
                        onPress={() => handleEdit(node)}
                        className="flex-1 min-h-8"
                      >
                        编辑
                      </Button>
                      <Button
                        size="sm"
                        variant="flat"
                        color="danger"
                        onPress={() => handleDelete(node)}
                        className="flex-1 min-h-8"
                      >
                        删除
                      </Button>
                    </div>
                  </div>
                </CardBody>
              </Card>
            ))}
          </div>
          <OpsLogModal isOpen={opsOpen} onOpenChange={setOpsOpen} />
          </>
        )}

        {/* 新增/编辑节点对话框 */}
        <Modal 
          isOpen={dialogVisible} 
          onClose={() => setDialogVisible(false)}
          size="2xl"
          scrollBehavior="outside"
          backdrop="blur"
          placement="center"
        >
          <ModalContent>
            <ModalHeader>{dialogTitle}</ModalHeader>
            <ModalBody>
              <div className="space-y-4">
                <Input
                  label="节点名称"
                  placeholder="请输入节点名称"
                  value={form.name}
                  onChange={(e) => setForm(prev => ({ ...prev, name: e.target.value }))}
                  isInvalid={!!errors.name}
                  errorMessage={errors.name}
                  variant="bordered"
                />

                <Input
                  label="服务器IP"
                  placeholder="请输入服务器IP地址，如: 192.168.1.100 或 example.com"
                  value={form.serverIp}
                  onChange={(e) => setForm(prev => ({ ...prev, serverIp: e.target.value }))}
                  isInvalid={!!errors.serverIp}
                  errorMessage={errors.serverIp}
                  variant="bordered"
                />

                <Textarea
                  label="入口IP"
                  placeholder="一行一个IP地址或域名，例如:&#10;192.168.1.100&#10;example.com"
                  value={form.ipString}
                  onChange={(e) => setForm(prev => ({ ...prev, ipString: e.target.value }))}
                  isInvalid={!!errors.ipString}
                  errorMessage={errors.ipString}
                  variant="bordered"
                  minRows={3}
                  maxRows={5}
                  description="支持多个IP，每行一个地址"
                />

                <div className="grid grid-cols-2 gap-4">
                  <Input
                    label="起始端口"
                    type="number"
                    placeholder="1000"
                    value={form.portSta.toString()}
                    onChange={(e) => setForm(prev => ({ ...prev, portSta: parseInt(e.target.value) || 1000 }))}
                    isInvalid={!!errors.portSta}
                    errorMessage={errors.portSta}
                    variant="bordered"
                    min={1}
                    max={65535}
                  />

                  <Input
                    label="结束端口"
                    type="number"
                    placeholder="65535"
                    value={form.portEnd.toString()}
                    onChange={(e) => setForm(prev => ({ ...prev, portEnd: parseInt(e.target.value) || 65535 }))}
                    isInvalid={!!errors.portEnd}
                    errorMessage={errors.portEnd}
                    variant="bordered"
                    min={1}
                    max={65535}
                  />
                </div>

                <div className="grid grid-cols-3 gap-4">
                  <Input label="价格(元)" type="number" placeholder="可选" value={priceCents!=null? (priceCents/100).toString():''} onChange={(e)=>{
                    const v = parseFloat((e.target as any).value); setPriceCents(isNaN(v)? undefined : Math.round(v*100));
                  }} variant="bordered" />
                  <Select 
                    label="周期"
                    selectedKeys={cycleMonths? new Set([String(cycleMonths)]): new Set()}
                    onChange={(e)=>{
                      const v = parseInt((e.target as any).value);
                      setCycleMonths(isNaN(v)? undefined : v);
                    }}
                    variant="bordered"
                  >
                    {periodOptions.map(opt => (
                      <SelectItem key={opt.key}>{opt.label}</SelectItem>
                    ))}
                  </Select>
                  <Input label="开始日期" type="date" value={startDateMs? new Date(startDateMs).toISOString().slice(0,10):''} onChange={(e)=>{
                    const s = (e.target as any).value; setStartDateMs(s? new Date(s+ 'T00:00:00').getTime(): undefined);
                  }} variant="bordered" />
                </div>

                {/* 到期时间预览（根据周期与开始日期计算），显示“剩余天数” */}
                <div className="text-xs text-default-600">
                  {(() => {
                    const exp = computeNextExpire(startDateMs, cycleMonths);
                    if (!exp) return '到期时间：-';
                    const daysLeft = Math.max(0, Math.ceil((exp - Date.now()) / (24*3600*1000)));
                    const dt = new Date(exp);
                    const yyyy = dt.getFullYear(); const mm = String(dt.getMonth()+1).padStart(2,'0'); const dd = String(dt.getDate()).padStart(2,'0');
                    return `到期时间：${yyyy}-${mm}-${dd}（剩余 ${daysLeft} 天）`;
                  })()}
                </div>



                
                <Alert
                        color="primary"
                        variant="flat"
                        description="服务器ip是你要添加的服务器的ip地址，不是面板的ip地址。入口ip是用于展示在转发页面，面向用户的访问地址。实在理解不到说明你没这个需求，都填节点的服务器ip就行！"
                        className="mt-4"
                      />
              </div>
            </ModalBody>
            <ModalFooter>
              <Button
                variant="flat"
                onPress={() => setDialogVisible(false)}
              >
                取消
              </Button>
              <Button
                color="primary"
                onPress={handleSubmit}
                isLoading={submitLoading}
              >
                {submitLoading ? '提交中...' : '确定'}
              </Button>
            </ModalFooter>
          </ModalContent>
        </Modal>

        {/* 出口服务设置弹窗 */}
        <Modal isOpen={exitModalOpen} onOpenChange={setExitModalOpen} size="md">
          <ModalContent>
            {(onClose) => (
              <>
                <ModalHeader>设置出口节点服务</ModalHeader>
                <ModalBody>
                  <div className="space-y-3">
                    <Input label="端口" type="number" value={String(exitPort)} onChange={(e:any)=>setExitPort(Number(e.target.value))} />
                    <Input label="密码" type="text" value={exitPassword} onChange={(e:any)=>setExitPassword(e.target.value)} />
                    <Input label="加密方法" value={exitMethod} onChange={(e:any)=>setExitMethod(e.target.value)} description="默认 AEAD_CHACHA20_POLY1305" />
                    <div>
                      <div className="text-sm text-default-600 mb-1">出口IP（metadata.interface，可选）</div>
                      <div className="flex gap-2 flex-wrap">
                        {exitIfaces.map((ip) => (
                          <Button key={ip} size="sm" variant={exitIfaceSel===ip? 'solid':'flat'} color={exitIfaceSel===ip? 'primary':'default'} onPress={()=>setExitIfaceSel(ip)}>{ip}</Button>
                        ))}
                        {exitIfaces.length===0 && <div className="text-xs text-default-500">未获取到出口IP列表</div>}
                        {exitIfaceSel && <Button size="sm" variant="light" onPress={()=>setExitIfaceSel('')}>清除选择</Button>}
                      </div>
                    </div>
                    <Divider />
                    <Input label="观察器(observer)" value={exitObserver} onChange={(e:any)=>setExitObserver(e.target.value)} description="默认 console，可留空" />
                    <Input label="限速(limiter)" value={exitLimiter} onChange={(e:any)=>setExitLimiter(e.target.value)} description="可选，需在节点注册对应限速器" />
                    <Input label="连接限速(rlimiter)" value={exitRLimiter} onChange={(e:any)=>setExitRLimiter(e.target.value)} description="可选，需在节点注册对应限速器" />
                    <Divider />
                    <div className="space-y-2">
                      <div className="flex items-center justify-between">
                        <span className="text-sm text-default-600">handler.metadata</span>
                        <Button size="sm" variant="flat" onPress={()=>setExitMetaItems(prev=>[...prev,{id:Date.now(), key:'', value:''}])}>添加</Button>
                      </div>
                      {exitMetaItems.map((it: {id:number; key:string; value:string}) => (
                        <div key={it.id} className="grid grid-cols-5 gap-2 items-center">
                          <Input className="col-span-2" placeholder="key" value={it.key} onChange={(e:any)=>setExitMetaItems((prev: Array<{id:number; key:string; value:string}>)=>prev.map((x:any)=>x.id===it.id?{...x,key:e.target.value}:x))} />
                          <Input className="col-span-3" placeholder="value" value={it.value} onChange={(e:any)=>setExitMetaItems((prev: Array<{id:number; key:string; value:string}>)=>prev.map((x:any)=>x.id===it.id?{...x,value:e.target.value}:x))} />
                          <Button size="sm" variant="light" color="danger" onPress={()=>setExitMetaItems((prev: Array<{id:number; key:string; value:string}>)=>prev.filter((x:any)=>x.id!==it.id))}>删除</Button>
                        </div>
                      ))}
                    </div>
                  </div>
                </ModalBody>
                <ModalFooter>
                  <Button variant="light" onPress={onClose}>关闭</Button>
                  <Button color="primary" isLoading={exitSubmitting} onPress={submitExit}>保存</Button>
                </ModalFooter>
              </>
            )}
          </ModalContent>
        </Modal>

        {/* 删除确认模态框 */}
        <Modal 
          isOpen={deleteModalOpen}
          onOpenChange={setDeleteModalOpen}
          size="2xl"
        scrollBehavior="outside"
        backdrop="blur"
        placement="center"
        >
          <ModalContent>
            {(onClose) => (
              <>
                <ModalHeader className="flex flex-col gap-1">
                  <h2 className="text-xl font-bold">确认删除</h2>
                </ModalHeader>
                <ModalBody>
                  <p>确定要删除节点 <strong>"{nodeToDelete?.name}"</strong> 吗？</p>
                  <p className="text-small text-default-500">此操作不可恢复，请谨慎操作。</p>
                  <label className="flex items-center gap-2 text-sm mt-2">
                    <input type="checkbox" checked={deleteAlsoUninstall} onChange={(e)=>setDeleteAlsoUninstall((e.target as any).checked)} />
                    同步卸载节点上的 Agent（自我卸载）
                  </label>
                </ModalBody>
                <ModalFooter>
                  <Button variant="light" onPress={onClose}>
                    取消
                  </Button>
                  <Button 
                    color="danger" 
                    onPress={confirmDelete}
                    isLoading={deleteLoading}
                  >
                    {deleteLoading ? '删除中...' : '确认删除'}
                  </Button>
                </ModalFooter>
              </>
            )}
          </ModalContent>
        </Modal>

        {/* GOST 配置查看 */}
        <Modal
          isOpen={gostConfigModal.open}
          onClose={()=>setGostConfigModal(prev=>({...prev, open:false}))}
          size="3xl"
          scrollBehavior="outside"
          backdrop="blur"
          placement="center"
        >
          <ModalContent>
            <ModalHeader>{gostConfigModal.title}</ModalHeader>
            <ModalBody>
              {gostConfigModal.loading ? (
                <div className="text-sm text-default-500">读取中...</div>
              ) : (
                <pre className="bg-default-50 dark:bg-default-100/10 rounded-lg p-4 text-xs whitespace-pre-wrap break-all">
{gostConfigModal.content || '无配置内容'}
                </pre>
              )}
            </ModalBody>
            <ModalFooter>
              <Button variant="light" onPress={()=>setGostConfigModal(prev=>({...prev, open:false}))}>关闭</Button>
            </ModalFooter>
          </ModalContent>
        </Modal>

        {/* 安装命令模态框 */}
        <Modal 
          isOpen={installCommandModal} 
          onClose={() => setInstallCommandModal(false)}
          size="2xl"
        scrollBehavior="outside"
        backdrop="blur"
        placement="center"
        >
          <ModalContent>
            <ModalHeader>安装命令 - {currentNodeName}</ModalHeader>
            <ModalBody>
              <div className="space-y-4">
                <p className="text-sm text-default-600">
                  请复制以下安装命令到服务器上执行：
                </p>
                <div className="relative">
                  <Textarea
                    value={installCommand}
                    readOnly
                    variant="bordered"
                    minRows={6}
                    maxRows={10}
                    className="font-mono text-sm"
                    classNames={{
                      input: "font-mono text-sm"
                    }}
                  />
                  <Button
                    size="sm"
                    color="primary"
                    variant="flat"
                    className="absolute top-2 right-2"
                    onPress={handleManualCopy}
                  >
                    复制
                  </Button>
                </div>
                <div className="text-xs text-default-500">
                  💡 提示：如果复制按钮失效，请手动选择上方文本进行复制
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
