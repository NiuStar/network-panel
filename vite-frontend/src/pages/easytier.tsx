import { useEffect, useMemo, useState } from 'react';
import { Card, CardBody, CardHeader } from "@heroui/card";
import { Button } from "@heroui/button";
import { Select, SelectItem } from "@heroui/select";
import { Modal, ModalBody, ModalContent, ModalFooter, ModalHeader } from "@heroui/modal";
import { Spinner } from "@heroui/spinner";
import { etStatus, etEnable, etNodes, etJoin, getNodeInterfaces, etSuggestPort, etRemove, etAutoAssign, etRedeployMaster, listNodeOps } from '@/api';
import toast from 'react-hot-toast';

interface NodeLite { id:number; name:string; serverIp:string; joined?:boolean; ip?:string; port?:number; peerNodeId?:number; peerIp?:string; ipv4?:string; }

export default function EasyTierPage(){
  const [loading, setLoading] = useState(true);
  const [enabled, setEnabled] = useState(false);
  const [secret, setSecret] = useState('');
  const [masterNodeId, setMasterNodeId] = useState<number|undefined>(undefined);
  const [masterIp, setMasterIp] = useState('');
  const [masterPort, setMasterPort] = useState<number>(0);
  const [nodes, setNodes] = useState<NodeLite[]>([]);
  const [ifaceCache, setIfaceCache] = useState<Record<number,string[]>>({});
  const [editOpen, setEditOpen] = useState(false);
  const [editNode, setEditNode] = useState<NodeLite|null>(null);
  const [editIp, setEditIp] = useState('');
  const [editPort, setEditPort] = useState<number>(0);
  const [editPeer, setEditPeer] = useState<number|undefined>(undefined);
  const [editPeerIp, setEditPeerIp] = useState<string>('');
  const [opsOpen, setOpsOpen] = useState(false);
  const [opsNodeId, setOpsNodeId] = useState<number|undefined>(undefined);
  const [ops, setOps] = useState<Array<{timeMs:number;cmd:string;success:number;message:string;stdout?:string;stderr?:string;}>>([]);
  const [opsLoading, setOpsLoading] = useState(false);
  const reloadOps = async()=>{
    if (!opsNodeId) return; setOpsLoading(true);
    try{ const r:any = await listNodeOps({ nodeId: opsNodeId, limit: 50 }); if (r.code===0) setOps(r.data.ops||[]); }catch{} finally{ setOpsLoading(false); }
  };

  const joined = useMemo(()=> nodes.filter(n=>n.joined), [nodes]);
  const pending = useMemo(()=> nodes.filter(n=>!n.joined), [nodes]);

  const load = async ()=>{
    setLoading(true);
    try{
      const s:any = await etStatus();
      if (s.code===0){
        setEnabled(!!s.data?.enabled); setSecret(s.data?.secret||'');
        const m = s.data?.master||{}; setMasterNodeId(m.nodeId||undefined); setMasterIp(m.ip||''); setMasterPort(m.port||0);
      }
      const r:any = await etNodes();
      if (r.code===0 && Array.isArray(r.data?.nodes)){
        setNodes(r.data.nodes.map((x:any)=>({ id:x.nodeId, name:x.nodeName, serverIp:x.serverIp, joined:!!x.joined, ip:x.ip, port:x.port, peerNodeId:x.peerNodeId, peerIp:x.peerIp, ipv4:x.ipv4 })));
      }
    }catch{ toast.error('加载失败'); } finally{ setLoading(false); }
  };
  useEffect(()=>{ load(); },[]);

  // 当选择主控节点时，自动填充默认入口IP与随机端口
  useEffect(()=>{
    (async()=>{
      if (!masterNodeId) return;
      if (!masterIp){
        const nn = nodes.find(n=>n.id===masterNodeId);
        if (nn) setMasterIp(nn.serverIp);
      }
      if (!masterPort){
        try{ const s:any = await etSuggestPort(masterNodeId); if (s.code===0) setMasterPort(s.data?.port||0); }catch{}
      }
    })();
  }, [masterNodeId]);

  const fetchIfaces = async (nodeId:number)=>{
    if (!nodeId) return [] as string[];
    if (ifaceCache[nodeId]) return ifaceCache[nodeId];
    try{ const r:any = await getNodeInterfaces(nodeId); const ips = (r.code===0 && Array.isArray(r.data?.ips)) ? r.data.ips as string[] : []; setIfaceCache(prev=>({...prev, [nodeId]: ips})); return ips; }catch{return []}
  };

  const addIfaceToCache = (nodeId:number, ip?:string)=>{
    if (!ip) return;
    setIfaceCache(prev=>{
      const list = prev[nodeId] || [];
      if (list.includes(ip)) return prev;
      return { ...prev, [nodeId]: [...list, ip] };
    });
  };

  const enable = async ()=>{
    if (!masterNodeId){ toast.error('请选择主控节点'); return; }
    // 补全默认入口IP与端口
    let ip = masterIp; let port = masterPort;
    if (!ip){ const nn = nodes.find(n=>n.id===masterNodeId); if (nn) ip = nn.serverIp; }
    if (!port){ try{ const s:any = await etSuggestPort(masterNodeId!); if (s.code===0) port = s.data?.port||0; }catch{} }
    setMasterIp(ip); setMasterPort(port);
    try{
      const r:any = await etEnable({ enable: true, masterNodeId: masterNodeId!, ip, port: port||0 });
      if (r.code===0){ toast.success('已启用组网'); await load(); } else toast.error(r.msg||'失败');
    }catch{ toast.error('失败'); }
  };

  const openEdit = async (n:NodeLite)=>{
    if (!enabled || !masterNodeId){ toast.error('请先设置主控节点并启用组网'); return; }
    setEditNode(n); setEditIp(n.serverIp); setEditPort(0); setEditPeer(n.peerNodeId || joined[0]?.id); setEditPeerIp(n.peerIp || '');
    await fetchIfaces(n.id);
    addIfaceToCache(n.id, n.serverIp);
    try{ const s:any = await etSuggestPort(n.id); if (s.code===0) setEditPort(s.data?.port||0); }catch{}
    setEditOpen(true);
  };
  const doJoin = async ()=>{
    if (!editNode) return; if (!editIp || !editPort){ toast.error('请选择IP与端口'); return; }
    // 自动弹出操作日志
    setOpsNodeId(editNode.id); setOpsOpen(true);
    try{ const r:any = await etJoin({ nodeId: editNode.id, ip: editIp, port: editPort, peerNodeId: editPeer, peerIp: editPeerIp||undefined }); if (r.code===0){ toast.success('已下发安装与配置'); setEditOpen(false); load(); } else toast.error(r.msg||'失败'); }catch{ toast.error('失败'); }
  };

  if (loading) return <div className="p-6"><Spinner size="sm" /> <span className="ml-2 text-default-600">加载中...</span></div>;

  return (
    <div className="p-4 space-y-4">
      <Card>
        <CardHeader className="flex justify-between items-center">
          <div className="font-semibold">组网功能（EasyTier）</div>
          {!enabled ? (
            <div className="flex items-center gap-2">
              <Select label="主控节点" className="min-w-[320px] max-w-[380px]" selectedKeys={masterNodeId? [String(masterNodeId)]: []} onSelectionChange={(keys)=>{ const k=Array.from(keys)[0] as string; if (k) setMasterNodeId(parseInt(k)); }}>
                {nodes.map(n=> (<SelectItem key={String(n.id)}>{n.name}</SelectItem>))}
              </Select>
              <Select label="入口IP" className="min-w-[320px] max-w-[380px]" selectedKeys={masterIp? [masterIp]: []} onOpenChange={async()=>{ if (masterNodeId) await fetchIfaces(masterNodeId); }} onSelectionChange={(keys)=>{ const k=Array.from(keys)[0] as string; setMasterIp(k||''); }}>
                {Array.from(new Set([...(ifaceCache[masterNodeId||0]||[]), masterIp || (nodes.find(n=>n.id===masterNodeId)?.serverIp||'')].filter(Boolean))).map(ip=> (<SelectItem key={ip as string}>{ip as string}</SelectItem>))}
              </Select>
              <InputSmallNumber label="端口" value={masterPort} onChange={setMasterPort} />
              <Button color="primary" onPress={enable}>启用组网</Button>
            </div>
          ) : (
            <div className="flex items-center gap-2 text-sm text-default-500">
              <div>已启用 · secret: <span className="font-mono">{secret||'-'}</span></div>
              <Button size="sm" variant="flat" onPress={async()=>{ setOpsNodeId(masterNodeId); setOpsOpen(true); try{ const r:any = await etRedeployMaster(); if (r.code===0){ toast.success('已在主控重装/重配'); } else toast.error(r.msg||'失败'); }catch{ toast.error('失败'); } }}>主控重装/重配</Button>
            </div>
          )}
        </CardHeader>
      </Card>

      <div className="grid grid-cols-1 md:grid-cols-2 gap-4">
        <Card>
          <CardHeader>待加入</CardHeader>
          <CardBody>
            <div className="space-y-2">
              {pending.map(n=> (
                <div key={n.id} className="border border-dashed rounded p-3 cursor-pointer" onDoubleClick={()=>openEdit(n)}>
                  <div className="font-medium">{n.name}</div>
                  <div className="text-xs text-default-500">公网IP: {n.serverIp}</div>
                </div>
              ))}
              {pending.length===0 && <div className="text-xs text-default-500">暂无</div>}
            </div>
          </CardBody>
        </Card>
        <Card>
          <CardHeader>已加入</CardHeader>
          <CardBody>
            <div className="space-y-2">
              {joined.map(n=> {
                const peerPort = nodes.find(x=>x.id===n.peerNodeId)?.port;
                return (
                <div key={n.id} className="border border-dashed rounded p-3">
                  <div className="font-medium">{n.name}</div>
                  <div className="text-xs text-default-500">内网IP: {n.ipv4? `10.126.126.${n.ipv4}`: '-'}</div>
                  <div className="text-xs text-default-500">对外 {n.ip||'-'}:{n.port||0}</div>
                  <div className="text-xs text-default-500">对端 {n.peerIp||'-'}:{peerPort||'-'}</div>
                  <div className="mt-2 flex gap-2">
                    <Button size="sm" onPress={()=>openEdit(n)}>变更对端</Button>
                    <Button size="sm" variant="flat" onPress={async()=>{ setOpsNodeId(n.id); try{ const r:any = await listNodeOps({ nodeId: n.id, limit: 50 }); if (r.code===0) setOps(r.data.ops||[]); else setOps([]);}catch{ setOps([])}; setOpsOpen(true); }}>操作日志</Button>
                    <Button 
                      size="sm" color="danger" variant="flat"
                      isDisabled={masterNodeId===n.id}
                      onPress={async()=>{ 
                        if (masterNodeId===n.id){ toast.error('主控节点不可移除'); return; }
                        try{ const r:any = await etRemove(n.id); if (r.code===0){ toast.success('已移除'); load(); } else toast.error(r.msg||'失败'); }catch{ toast.error('失败'); } 
                      }}
                    >移除</Button>
                  </div>
                </div>
              )})}
              {joined.length===0 && <div className="text-xs text-default-500">暂无</div>}
            </div>
          </CardBody>
        </Card>
      </div>

      {enabled && (
        <div className="flex justify-end">
          <Button color="primary" variant="flat" onPress={async()=>{ try{ const r:any = await etAutoAssign('chain'); if (r.code===0){ toast.success('已一键分配链路'); load(); } else toast.error(r.msg||'失败'); }catch{ toast.error('失败'); } }}>一键分配对端链路</Button>
        </div>
      )}

      <Modal isOpen={editOpen} onOpenChange={setEditOpen}>
        <ModalContent className="w-[80vw] max-w-[80vw] h-[60vh]">
          {(onClose)=> (
            <>
              <ModalHeader className="flex flex-col gap-1">加入组网：{editNode?.name}</ModalHeader>
              <ModalBody className="overflow-auto">
                <div className="grid grid-cols-1 md:grid-cols-3 gap-4">
                  <div className="md:col-span-1 space-y-3">
                    <Select label="对外IP" className="min-w-[320px] max-w-[420px]" selectedKeys={editIp? [editIp]: []} onOpenChange={async()=>{ if (editNode) await fetchIfaces(editNode.id); }} onSelectionChange={(keys)=>{ const k=Array.from(keys)[0] as string; setEditIp(k||''); }}>
                      {Array.from(new Set([...(ifaceCache[editNode?.id||0]||[]), editNode?.serverIp || '', editIp || ''].filter(Boolean))).map(ip=> (<SelectItem key={ip as string}>{ip as string}</SelectItem>))}
                    </Select>
                    <InputSmallNumber label="开放端口" value={editPort} onChange={setEditPort} />
                  </div>
                  <div className="md:col-span-2" />
                  <Select label="连接到对端" className="md:col-span-3 min-w-[320px] max-w-[720px]" selectedKeys={editPeer? [String(editPeer)]: []} onSelectionChange={async (keys)=>{ const k=Array.from(keys)[0] as string; const v = k? parseInt(k): undefined; setEditPeer(v); setEditPeerIp(''); if (v) await fetchIfaces(v); }}>
                    {joined.map(n=> (<SelectItem key={String(n.id)}>{n.name}</SelectItem>))}
                  </Select>
                  {editPeer && (
                    <Select label="对端IP" className="md:col-span-3 min-w-[320px] max-w-[720px]" selectedKeys={editPeerIp? [editPeerIp]: []} onOpenChange={async()=>{ if (editPeer) await fetchIfaces(editPeer); }} onSelectionChange={(keys)=>{ const k=Array.from(keys)[0] as string; setEditPeerIp(k||''); }}>
                      {Array.from(new Set([...(ifaceCache[editPeer||0]||[]), (nodes.find(x=>x.id===editPeer)?.serverIp||'')].filter(Boolean))).map(ip=> (<SelectItem key={ip as string}>{ip as string}</SelectItem>))}
                    </Select>
                  )}
                </div>
              </ModalBody>
              <ModalFooter>
                <Button variant="light" onPress={onClose}>取消</Button>
                <Button color="primary" onPress={doJoin}>加入</Button>
              </ModalFooter>
            </>
          )}
        </ModalContent>
      </Modal>

      <Modal isOpen={opsOpen} onOpenChange={setOpsOpen}>
        <ModalContent className="w-[80vw] max-w-[80vw] h-[80vh]">
          {(onClose)=> (
            <>
              <ModalHeader className="flex items-center justify-between">
                <div>操作日志 · 节点 {opsNodeId||'-'}</div>
                <div>
                  <Button size="sm" variant="flat" onPress={reloadOps} isDisabled={!opsNodeId || opsLoading}>{opsLoading? '刷新中...':'刷新'}</Button>
                </div>
              </ModalHeader>
              <ModalBody className="overflow-hidden">
                <pre className="h-[65vh] max-h-[65vh] overflow-auto whitespace-pre-wrap text-2xs bg-default-100 p-3 rounded">
{ops.length===0 ? '暂无记录' : ops.map(o => {
  const t = new Date(o.timeMs).toLocaleString();
  const head = `[${t}] ${o.cmd}`;
  const body = (o.message||'').trim();
  const lines = [ `${head}  ${body}` ];
  if (o.stdout && o.stdout.trim()) lines.push(`${head}  stdout: ${o.stdout.trim()}`);
  if (o.stderr && o.stderr.trim()) lines.push(`${head}  stderr: ${o.stderr.trim()}`);
  return lines.join('\n');
}).join('\n')}
                </pre>
              </ModalBody>
              <ModalFooter>
                <Button variant="light" onPress={onClose}>关闭</Button>
              </ModalFooter>
            </>
          )}
        </ModalContent>
      </Modal>
    </div>
  );
}

function InputSmallNumber({label, value, onChange}:{label:string; value:number; onChange:(v:number)=>void}){
  return (
    <div className="flex flex-col min-w-[220px]">
      <label className="text-xs text-default-600 mb-1">{label}</label>
      <input className="px-3 py-2 rounded border border-default-300 bg-transparent text-sm w-56" type="number" min={1} step={1} placeholder="系统分配或手动填写" value={value||''} onChange={(e)=> onChange(parseInt(e.target.value||'0'))} />
    </div>
  );
}
