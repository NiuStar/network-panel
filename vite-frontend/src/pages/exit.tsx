import { useEffect, useState } from "react";
import { Button } from "@heroui/button";
import { Card, CardBody, CardHeader } from "@heroui/card";
import {
  Table,
  TableHeader,
  TableColumn,
  TableBody,
  TableRow,
  TableCell,
} from "@heroui/table";
import {
  Modal,
  ModalContent,
  ModalHeader,
  ModalBody,
  ModalFooter,
  useDisclosure,
} from "@heroui/modal";
import { Input } from "@heroui/input";
import { Chip } from "@heroui/chip";
import { Spinner } from "@heroui/spinner";
import toast from "react-hot-toast";

import {
  getExitNodes,
  createExitExternal,
  updateExitExternal,
  deleteExitExternal,
} from "@/api";

type ExitNodeItem = {
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
};

type ExitExternalForm = {
  id?: number;
  name: string;
  host: string;
  port: string;
  protocol: string;
};

const EMPTY_FORM: ExitExternalForm = {
  name: "",
  host: "",
  port: "",
  protocol: "",
};

export default function ExitNodePage() {
  const { isOpen, onOpen, onOpenChange } = useDisclosure();
  const [items, setItems] = useState<ExitNodeItem[]>([]);
  const [loading, setLoading] = useState(false);
  const [saving, setSaving] = useState(false);
  const [form, setForm] = useState<ExitExternalForm>(EMPTY_FORM);

  const load = async () => {
    setLoading(true);
    try {
      const r: any = await getExitNodes();

      if (r.code === 0 && Array.isArray(r.data)) {
        setItems(r.data as ExitNodeItem[]);
      } else {
        toast.error(r.msg || "获取出口节点失败");
      }
    } catch (e: any) {
      toast.error(e?.message || "获取出口节点失败");
    } finally {
      setLoading(false);
    }
  };

  useEffect(() => {
    load();
  }, []);

  const resetForm = () => setForm(EMPTY_FORM);

  const openCreate = () => {
    resetForm();
    onOpen();
  };

  const openEdit = (item: ExitNodeItem) => {
    setForm({
      id: item.exitId,
      name: item.name || "",
      host: item.host || "",
      port: item.port ? String(item.port) : "",
      protocol: item.protocol || "",
    });
    onOpen();
  };

  const handleSave = async () => {
    const name = form.name.trim();
    const host = form.host.trim();
    const port = Number(form.port);

    if (!name || !host || !port || port < 1 || port > 65535) {
      toast.error("请填写正确的名称、地址和端口");
      return;
    }
    setSaving(true);
    try {
      const payload = {
        name,
        host,
        port,
        protocol: form.protocol.trim() || undefined,
      };
      const r: any = form.id
        ? await updateExitExternal({ id: form.id, ...payload })
        : await createExitExternal(payload);

      if (r.code === 0) {
        toast.success(form.id ? "已更新" : "已创建");
        onOpenChange();
        resetForm();
        load();
      } else {
        toast.error(r.msg || "保存失败");
      }
    } catch (e: any) {
      toast.error(e?.message || "保存失败");
    } finally {
      setSaving(false);
    }
  };

  const handleDelete = async (item: ExitNodeItem) => {
    if (!item.exitId) return;
    setSaving(true);
    try {
      const r: any = await deleteExitExternal(item.exitId);

      if (r.code === 0) {
        toast.success("已删除");
        load();
      } else {
        toast.error(r.msg || "删除失败");
      }
    } catch (e: any) {
      toast.error(e?.message || "删除失败");
    } finally {
      setSaving(false);
    }
  };

  const renderPortInfo = (item: ExitNodeItem) => {
    if (item.source === "external") {
      return item.port ? String(item.port) : "-";
    }
    const parts: string[] = [];

    if (item.ssPort) parts.push(`ss:${item.ssPort}`);
    if (item.anytlsPort) parts.push(`anytls:${item.anytlsPort}`);

    return parts.length > 0 ? parts.join(" / ") : "-";
  };

  const renderProtocol = (item: ExitNodeItem) => {
    if (item.source === "external") {
      return item.protocol || "—";
    }
    const parts: string[] = [];

    if (item.ssPort) parts.push("ss");
    if (item.anytlsPort) parts.push("anytls");

    return parts.length > 0 ? parts.join(" / ") : "—";
  };

  return (
    <div className="space-y-6">
      <Card className="shadow-sm border border-gray-200 dark:border-gray-700">
        <CardHeader className="flex justify-between items-center">
          <div>
            <h2 className="text-lg font-semibold">出口节点</h2>
            <p className="text-sm text-default-500">
              统一管理已配置出口与外部出口地址
            </p>
          </div>
          <Button color="primary" onPress={openCreate}>
            新增外部出口
          </Button>
        </CardHeader>
        <CardBody>
          {loading ? (
            <div className="py-12 flex items-center justify-center">
              <Spinner />
            </div>
          ) : (
            <Table
              aria-label="出口节点列表"
              className="min-h-[200px]"
              removeWrapper
            >
              <TableHeader>
                <TableColumn>来源</TableColumn>
                <TableColumn>名称</TableColumn>
                <TableColumn>地址</TableColumn>
                <TableColumn>协议</TableColumn>
                <TableColumn>端口</TableColumn>
                <TableColumn>状态</TableColumn>
                <TableColumn>操作</TableColumn>
              </TableHeader>
              <TableBody
                emptyContent="暂无出口节点"
                items={items}
              >
                {(item) => (
                  <TableRow key={`${item.source}-${item.exitId || item.nodeId}`}>
                    <TableCell>
                      <Chip size="sm" variant="flat" color={item.source === "node" ? "primary" : "warning"}>
                        {item.source === "node" ? "节点出口" : "外部出口"}
                      </Chip>
                    </TableCell>
                    <TableCell>{item.name || "-"}</TableCell>
                    <TableCell>{item.host || "-"}</TableCell>
                    <TableCell>{renderProtocol(item)}</TableCell>
                    <TableCell>{renderPortInfo(item)}</TableCell>
                    <TableCell>
                      {item.source === "node" ? (
                        <Chip
                          size="sm"
                          variant="flat"
                          color={item.online ? "success" : "danger"}
                        >
                          {item.online ? "在线" : "离线"}
                        </Chip>
                      ) : (
                        <Chip size="sm" variant="flat">
                          可用
                        </Chip>
                      )}
                    </TableCell>
                    <TableCell>
                      {item.source === "external" ? (
                        <div className="flex items-center gap-2">
                          <Button
                            size="sm"
                            variant="flat"
                            onPress={() => openEdit(item)}
                          >
                            编辑
                          </Button>
                          <Button
                            color="danger"
                            size="sm"
                            variant="flat"
                            onPress={() => handleDelete(item)}
                          >
                            删除
                          </Button>
                        </div>
                      ) : (
                        <span className="text-xs text-default-400">—</span>
                      )}
                    </TableCell>
                  </TableRow>
                )}
              </TableBody>
            </Table>
          )}
        </CardBody>
      </Card>

      <Modal
        backdrop="opaque"
        disableAnimation
        isOpen={isOpen}
        placement="center"
        onOpenChange={onOpenChange}
      >
        <ModalContent>
          {(onClose) => (
            <>
              <ModalHeader>
                {form.id ? "编辑外部出口" : "新增外部出口"}
              </ModalHeader>
              <ModalBody className="space-y-3">
                <Input
                  label="出口名称"
                  placeholder="例如：海外出口-1"
                  value={form.name}
                  onChange={(e) =>
                    setForm((prev) => ({ ...prev, name: e.target.value }))
                  }
                />
                <Input
                  label="出口地址"
                  placeholder="IP 或域名"
                  value={form.host}
                  onChange={(e) =>
                    setForm((prev) => ({ ...prev, host: e.target.value }))
                  }
                />
                <Input
                  label="出口端口"
                  placeholder="例如 443"
                  type="number"
                  value={form.port}
                  onChange={(e) =>
                    setForm((prev) => ({ ...prev, port: e.target.value }))
                  }
                />
                <Input
                  label="出口协议(可选)"
                  placeholder="如 ss / anytls / tcp"
                  value={form.protocol}
                  onChange={(e) =>
                    setForm((prev) => ({ ...prev, protocol: e.target.value }))
                  }
                />
              </ModalBody>
              <ModalFooter>
                <Button variant="light" onPress={onClose}>
                  取消
                </Button>
                <Button color="primary" isLoading={saving} onPress={handleSave}>
                  {form.id ? "保存" : "创建"}
                </Button>
              </ModalFooter>
            </>
          )}
        </ModalContent>
      </Modal>
    </div>
  );
}
