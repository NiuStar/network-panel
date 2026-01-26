import { useEffect, useState } from "react";
import { Button } from "@heroui/button";
import { Card, CardBody, CardHeader } from "@heroui/card";
import { Input } from "@heroui/input";
import {
  Modal,
  ModalBody,
  ModalContent,
  ModalFooter,
  ModalHeader,
} from "@heroui/modal";
import toast from "react-hot-toast";

import {
  listProbeTargets,
  createProbeTarget,
  updateProbeTarget,
  deleteProbeTarget,
} from "@/api";
import VirtualGrid from "@/components/VirtualGrid";

export default function ProbePage() {
  const [list, setList] = useState<any[]>([]);
  const [modalOpen, setModalOpen] = useState(false);
  const [isEdit, setIsEdit] = useState(false);
  const [form, setForm] = useState<{ id?: number; name: string; ip: string }>({
    name: "",
    ip: "",
  });

  const load = async () => {
    try {
      const res = await listProbeTargets();

      if (res.code === 0) setList(res.data || []);
      else toast.error(res.msg || "加载失败");
    } catch {
      toast.error("网络错误");
    }
  };

  useEffect(() => {
    load();
  }, []);

  const openCreate = () => {
    setIsEdit(false);
    setForm({ name: "", ip: "" });
    setModalOpen(true);
  };
  const openEdit = (it: any) => {
    setIsEdit(true);
    setForm({ id: it.id, name: it.name, ip: it.ip });
    setModalOpen(true);
  };
  const submit = async () => {
    try {
      if (!form.name || !form.ip) {
        toast.error("请填写名称与IP");

        return;
      }
      const fn = isEdit ? updateProbeTarget : createProbeTarget;
      const payload: any = isEdit
        ? { id: form.id, name: form.name, ip: form.ip }
        : { name: form.name, ip: form.ip };
      const res = await fn(payload);

      if (res.code === 0) {
        toast.success("已保存");
        setModalOpen(false);
        load();
      } else toast.error(res.msg || "保存失败");
    } catch {
      toast.error("网络错误");
    }
  };
  const del = async (it: any) => {
    try {
      const res = await deleteProbeTarget(it.id);

      if (res.code === 0) {
        toast.success("已删除");
        load();
      } else toast.error(res.msg || "删除失败");
    } catch {
      toast.error("网络错误");
    }
  };

  return (
    <div className="px-4 py-6">
      <div className="flex items-center justify-between mb-4">
        <h2 className="text-xl font-semibold">探针目标</h2>
        <Button color="primary" onPress={openCreate}>
          新增目标
        </Button>
      </div>
      <VirtualGrid
        className="w-full"
        estimateRowHeight={180}
        items={list}
        maxColumns={3}
        minItemWidth={280}
        renderItem={(it) => (
          <Card key={it.id} className="list-card">
            <CardHeader className="justify-between">
              <div>
                <div className="font-semibold">{it.name}</div>
                <div className="text-sm text-default-500">{it.ip}</div>
              </div>
              <div className="flex gap-2">
                <Button size="sm" variant="flat" onPress={() => openEdit(it)}>
                  编辑
                </Button>
                <Button
                  color="danger"
                  size="sm"
                  variant="flat"
                  onPress={() => del(it)}
                >
                  删除
                </Button>
              </div>
            </CardHeader>
            <CardBody className="pt-0 text-sm text-default-500">
              ID: {it.id}
            </CardBody>
          </Card>
        )}
      />

      <Modal isOpen={modalOpen} onOpenChange={setModalOpen}>
        <ModalContent>
          {(onClose) => (
            <>
              <ModalHeader>{isEdit ? "编辑目标" : "新增目标"}</ModalHeader>
              <ModalBody>
                <Input
                  label="名称"
                  value={form.name}
                  onChange={(e: any) =>
                    setForm((prev) => ({ ...prev, name: e.target.value }))
                  }
                />
                <Input
                  label="IP"
                  value={form.ip}
                  onChange={(e: any) =>
                    setForm((prev) => ({ ...prev, ip: e.target.value }))
                  }
                />
              </ModalBody>
              <ModalFooter>
                <Button variant="light" onPress={onClose}>
                  取消
                </Button>
                <Button color="primary" onPress={submit}>
                  保存
                </Button>
              </ModalFooter>
            </>
          )}
        </ModalContent>
      </Modal>
    </div>
  );
}
