import { useEffect, useState } from 'react';
import { Modal, ModalBody, ModalContent, ModalFooter, ModalHeader } from "@heroui/modal";
import { Button } from "@heroui/button";
import { Select, SelectItem } from "@heroui/select";
import { getNodeList, listNodeOps } from '@/api';

interface NodeLite { id:number; name:string }

export default function OpsLogModal({ isOpen, onOpenChange, defaultNodeId, requestId }:{ isOpen:boolean; onOpenChange:(open:boolean)=>void; defaultNodeId?: number; requestId?: string }){
  const [nodes, setNodes] = useState<NodeLite[]>([]);
  const [nodeId, setNodeId] = useState<number|undefined>(defaultNodeId);
  const [logs, setLogs] = useState<Array<{timeMs:number;cmd:string;success:number;message:string;stdout?:string;stderr?:string; nodeName?:string;}>>([]);
  const [loading, setLoading] = useState(false);

  useEffect(()=>{ (async()=>{ try{ const r:any = await getNodeList(); if (Array.isArray(r?.data)) setNodes(r.data.map((x:any)=>({id:x.id, name:x.name}))); }catch{} })(); },[]);
  useEffect(()=>{ setNodeId(defaultNodeId); }, [defaultNodeId]);

  const load = async()=>{
    setLoading(true);
    try{
      if (requestId){
        const r:any = await listNodeOps({ requestId, limit: 1000 });
        setLogs(r.code===0 ? (r.data?.ops||[]) : []);
      } else if (nodeId){
        const r:any = await listNodeOps({ nodeId, limit: 200 });
        setLogs(r.code===0 ? (r.data?.ops||[]) : []);
      } else {
        setLogs([]);
      }
    }catch{ setLogs([]); } finally{ setLoading(false); }
  };

  useEffect(()=>{ if (isOpen) load(); }, [isOpen, nodeId, requestId]);

  return (
    <Modal isOpen={isOpen} onOpenChange={onOpenChange} scrollBehavior="outside">
      <ModalContent className="w-[80vw] max-w-[80vw] h-[80vh]">
        {(onClose)=> (
          <>
            <ModalHeader className="flex items-center justify-between">
              <div>操作日志{requestId? '（本次诊断）':''}</div>
              <div className="flex items-center gap-2">
                {!requestId && (
                  <Select aria-label="选择节点" placeholder="选择节点" className="min-w-[260px]" selectedKeys={nodeId? [String(nodeId)]: []} onSelectionChange={(keys)=>{ const k=Array.from(keys)[0] as string; setNodeId(k? parseInt(k): undefined); }}>
                    {nodes.map(n=> (<SelectItem key={String(n.id)}>{n.name}</SelectItem>))}
                  </Select>
                )}
                <Button size="sm" variant="flat" onPress={load} isDisabled={loading}>{loading? '刷新中...':'刷新'}</Button>
              </div>
            </ModalHeader>
            <ModalBody className="overflow-hidden">
              <pre className="h-[65vh] max-h-[65vh] overflow-auto whitespace-pre-wrap text-2xs bg-default-100 p-3 rounded">
{(!requestId && !nodeId) ? '请选择节点' : (logs.length===0 ? '暂无记录' : logs.map(o => {
  const t = new Date(o.timeMs).toLocaleString();
  const head = `[${t}] ${o.nodeName? o.nodeName+' ':''}${o.cmd}`;
  const body = (o.message||'').trim();
  const lines = [ `${head}  ${body}` ];
  if (o.stdout && o.stdout.trim()) lines.push(`${head}  stdout: ${o.stdout.trim()}`);
  if (o.stderr && o.stderr.trim()) lines.push(`${head}  stderr: ${o.stderr.trim()}`);
  return lines.join('\n');
}).join('\n'))}
              </pre>
            </ModalBody>
            <ModalFooter>
              <Button variant="light" onPress={onClose}>关闭</Button>
            </ModalFooter>
          </>
        )}
      </ModalContent>
    </Modal>
  );
}
