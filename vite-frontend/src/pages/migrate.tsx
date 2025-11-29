import { useEffect, useRef, useState } from "react";
import { Card, CardBody, CardHeader } from "@heroui/card";
import { Input } from "@heroui/input";
import { Button } from "@heroui/button";
import { Progress as UiProgress } from "@heroui/progress";
import toast from "react-hot-toast";

import api from "@/api/network";

type MigrateForm = {
  host: string;
  port: string;
  user: string;
  password: string;
  db: string;
};

type Counts = Record<string, number>;

type TableProg = { table: string; inserted?: number; srcCount?: number };

type Progress = {
  current: number;
  total: number;
  status: "pending" | "running" | "done" | "error";
  tables?: TableProg[];
  error?: string;
};

export default function MigratePage() {
  const [form, setForm] = useState<MigrateForm>({
    host: "",
    port: "3306",
    user: "root",
    password: "",
    db: "",
  });

  const [loading, setLoading] = useState(false);
  const [testing, setTesting] = useState(false);
  const [counts, setCounts] = useState<Counts | null>(null);
  const [progress, setProgress] = useState<Progress | null>(null);

  // 用 ref 管理轮询定时器
  const pollRef = useRef<ReturnType<typeof setInterval> | null>(null);

  useEffect(() => {
    return () => {
      if (pollRef.current) {
        clearInterval(pollRef.current);
        pollRef.current = null;
      }
    };
  }, []);

  const validate = () => {
    if (!form.host || !form.port || !form.user || !form.db) {
      toast.error("请填写完整信息");

      return false;
    }

    return true;
  };

  const submit = async () => {
    if (!validate()) return;
    setLoading(true);
    try {
      const res: any = await api.post("/migrate/start", form);

      if (res.code === 0 && res.data?.jobId) {
        toast.success("已开始迁移");
        // 开始轮询进度
        pollRef.current = setInterval(async () => {
          try {
            const st: any = await fetch(
              `/api/v1/migrate/status?jobId=${res.data.jobId}`,
              {
                headers: { Authorization: localStorage.getItem("token") || "" },
              },
            ).then((r) => r.json());

            if (!st || st.code !== 0) return;

            setProgress(st.data as Progress);

            if (st.data?.status === "done" || st.data?.status === "error") {
              if (pollRef.current) {
                clearInterval(pollRef.current);
                pollRef.current = null;
              }
              setLoading(false);
              if (st.data.status === "done") toast.success("迁移完成");
              else toast.error(st.data.error || "迁移失败");
            }
          } catch {
            // 单次轮询错误忽略
          }
        }, 1000);
      } else {
        toast.error(res.msg || "启动迁移失败");
        setLoading(false);
      }
    } catch {
      toast.error("网络错误");
      setLoading(false);
    }
  };

  const testConn = async () => {
    if (!validate()) return;
    setTesting(true);
    try {
      const res: any = await api.post("/migrate/test", form);

      if (res.code === 0) {
        setCounts((res.data?.counts as Counts) || {});
        toast.success("连接正常");
      } else {
        toast.error(res.msg || "测试失败");
      }
    } catch {
      toast.error("网络错误");
    } finally {
      setTesting(false);
    }
  };

  return (
    <div className="px-4 py-6">
      <Card>
        <CardHeader>数据迁移（从旧 MySQL 导入）</CardHeader>
        <CardBody className="space-y-3">
          <div className="grid grid-cols-1 md:grid-cols-2 gap-3">
            <Input
              label="Host"
              value={form.host}
              onChange={(e) =>
                setForm({ ...form, host: (e.target as any).value })
              }
            />
            <Input
              label="Port"
              value={form.port}
              onChange={(e) =>
                setForm({ ...form, port: (e.target as any).value })
              }
            />
            <Input
              label="User"
              value={form.user}
              onChange={(e) =>
                setForm({ ...form, user: (e.target as any).value })
              }
            />
            <Input
              label="Password"
              type="password"
              value={form.password}
              onChange={(e) =>
                setForm({ ...form, password: (e.target as any).value })
              }
            />
            <Input
              label="Database"
              value={form.db}
              onChange={(e) =>
                setForm({ ...form, db: (e.target as any).value })
              }
            />
          </div>

          <div className="flex gap-2">
            <Button isLoading={testing} variant="flat" onPress={testConn}>
              测试连接
            </Button>
            <Button color="primary" isLoading={loading} onPress={submit}>
              开始迁移
            </Button>
          </div>

          {counts && (
            <div className="text-xs text-default-600">
              源库记录数：
              {Object.keys(counts)
                .map((k) => `${k}:${counts[k]}`)
                .join("，")}
            </div>
          )}

          {progress && (
            <div className="space-y-2">
              <div className="flex items-center gap-3">
                <div className="min-w-[120px] text-xs text-default-600">
                  进度：{progress.current}/{Math.max(progress.total || 0, 0)}
                </div>
                <div className="flex-1">
                  <UiProgress
                    showValueLabel
                    aria-label="迁移进度"
                    color={
                      progress.status === "error"
                        ? "danger"
                        : progress.status === "done"
                          ? "success"
                          : "primary"
                    }
                    size="sm"
                    value={(() => {
                      const t = Math.max(progress.total || 0, 0);
                      const c = Math.min(
                        progress.current || 0,
                        t || Number.MAX_SAFE_INTEGER,
                      );

                      return t > 0 ? Math.floor((c / t) * 100) : 0;
                    })()}
                  />
                </div>
                <span
                  className={`text-2xs ${progress.status === "error" ? "text-danger" : "text-default-500"}`}
                >
                  {progress.status}
                </span>
              </div>
              {Array.isArray(progress.tables) && progress.tables.length > 0 && (
                <div className="text-xs text-default-600">
                  {progress.tables
                    .map(
                      (t) => `${t.table}:${t.inserted || 0}/${t.srcCount || 0}`,
                    )
                    .join("，")}
                </div>
              )}
              {progress.status === "error" && (
                <div className="text-xs text-danger">
                  错误：{progress.error}
                </div>
              )}
            </div>
          )}

          <div className="text-xs text-default-500">
            提示：迁移完成后建议重新安装/升级 Agent 使节点配置生效。
          </div>
        </CardBody>
      </Card>
    </div>
  );
}
