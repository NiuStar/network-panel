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

type TableProg = {
  table: string;
  inserted?: number;
  srcCount?: number;
  status?: string;
  etaSec?: number;
};

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

  const tableOrder = [
    "user",
    "node",
    "user_tunnel",
    "user_node",
    "speed_limit",
    "vite_config",
    "tunnel",
    "forward",
    "forward_mid_port",
    "exit_setting",
    "anytls_setting",
    "exit_node_external",
    "probe_target",
    "node_sysinfo",
    "node_runtime",
    "easytier_result",
    "statistics_flow",
    "flow_timeseries",
    "nq_result",
  ] as const;

  const tableLabels: Record<string, string> = {
    user: "用户",
    node: "节点",
    user_tunnel: "用户-隧道权限",
    user_node: "用户-节点权限",
    speed_limit: "限速规则",
    vite_config: "系统配置",
    tunnel: "线路",
    forward: "转发",
    forward_mid_port: "中继端口",
    exit_setting: "出口(SS)",
    anytls_setting: "出口(AnyTLS)",
    exit_node_external: "外部出口",
    probe_target: "探测目标",
    node_sysinfo: "节点信息",
    node_runtime: "节点运行态",
    easytier_result: "EasyTier 状态",
    statistics_flow: "流量统计",
    flow_timeseries: "流量时序",
    nq_result: "NQ 结果",
  };

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

  const tableRows = (() => {
    const progMap: Record<string, TableProg> = {};
    if (progress?.tables) {
      progress.tables.forEach((t) => {
        progMap[t.table] = t;
      });
    }
    return tableOrder.map((key) => {
      const p = progMap[key] || {};
      const srcCount =
        p.srcCount ?? (counts && typeof counts[key] === "number" ? counts[key] : 0);
      const inserted = p.inserted ?? 0;
      const status = p.status || (progress ? "pending" : "idle");
      const percent =
        srcCount > 0 ? Math.min(100, Math.floor((inserted / srcCount) * 100)) : 0;
      const etaSec = typeof p.etaSec === "number" ? p.etaSec : null;
      return {
        key,
        name: tableLabels[key] || key,
        srcCount,
        inserted,
        status,
        percent,
        etaSec,
      };
    });
  })();
  const currentRow = tableRows.find((row) => row.status === "running") || null;

  return (
    <div className="np-page">
      <div className="np-page-header">
        <div>
          <h1 className="np-page-title">数据迁移</h1>
          <p className="np-page-desc">从旧 MySQL 导入数据。</p>
        </div>
      </div>
      <Card className="np-card">
        <CardHeader className="text-sm text-default-600">
          数据迁移配置
        </CardHeader>
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
          {currentRow && (
            <div className="mt-2 rounded-medium border border-warning-300/70 bg-warning-50/60 px-3 py-2 text-xs">
              <div className="flex items-center justify-between">
                <div className="text-warning-700">
                  当前迁移中：{currentRow.name}
                </div>
                <div className="text-default-500">
                  {currentRow.inserted}/{currentRow.srcCount}（{currentRow.percent}%）
                </div>
              </div>
            </div>
          )}

          <div className="mt-2 border rounded-medium overflow-hidden">
            <div className="grid grid-cols-12 gap-2 bg-default-100 text-2xs text-default-600 px-3 py-2">
              <div className="col-span-3">数据表</div>
              <div className="col-span-2">源数量</div>
              <div className="col-span-2">已迁移</div>
              <div className="col-span-2">进度</div>
              <div className="col-span-2">预计剩余</div>
              <div className="col-span-1">状态</div>
            </div>
            <div className="divide-y">
              {tableRows.map((row) => (
                <div
                  key={row.key}
                  className={`grid grid-cols-12 gap-2 px-3 py-2 text-xs items-center ${
                    row.status === "running"
                      ? "bg-warning-50/50"
                      : row.status === "error"
                        ? "bg-danger-50/40"
                        : ""
                  }`}
                >
                  <div className="col-span-3 text-default-700">{row.name}</div>
                  <div className="col-span-2 text-default-500">{row.srcCount}</div>
                  <div className="col-span-2 text-default-500">{row.inserted}</div>
                  <div className="col-span-2">
                    <div className="flex items-center gap-2">
                      <UiProgress
                        aria-label="表进度"
                        size="sm"
                        value={row.percent}
                        className="flex-1"
                      />
                      <span className="text-2xs text-default-500 w-10 text-right">
                        {row.percent}%
                      </span>
                    </div>
                  </div>
                  <div className="col-span-2 text-default-500">
                    {row.etaSec != null && row.etaSec > 0 ? `${row.etaSec}s` : "--"}
                  </div>
                  <div className="col-span-1 text-2xs">
                    <span
                      className={`${
                        row.status === "running"
                          ? "text-warning"
                          : row.status === "done"
                            ? "text-success"
                            : row.status === "error"
                              ? "text-danger"
                              : "text-default-400"
                      }`}
                    >
                      {row.status}
                    </span>
                  </div>
                </div>
              ))}
            </div>
          </div>

          <div className="text-xs text-default-500">
            提示：迁移完成后建议重新安装/升级 Agent 使节点配置生效。
          </div>
        </CardBody>
      </Card>
    </div>
  );
}
