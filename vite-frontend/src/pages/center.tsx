import { useEffect, useMemo, useState } from "react";
import { Card, CardBody, CardHeader } from "@heroui/card";
import { Spinner } from "@heroui/spinner";
import { Switch } from "@heroui/switch";
import dayjs from "dayjs";
import relativeTime from "dayjs/plugin/relativeTime";

import { getHeartbeatSummary } from "@/api";

dayjs.extend(relativeTime);

type HBItem = {
  uniqueId: string;
  version: string;
  installMode?: string;
  ip?: string;
  ipPrefix?: string;
  country?: string;
  city?: string;
  os: string;
  arch: string;
  createdAtMs: number;
  firstSeenMs: number;
  lastHeartbeatMs: number;
  uninstallAtMs?: number;
};

type HBSummary = {
  total: number;
  active: number;
  items: HBItem[];
};

type ResponseShape = {
  agents: HBSummary;
  controllers: HBSummary;
};

const fmt = (ms?: number) => {
  if (!ms) return "-";

  return `${dayjs(ms).format("YYYY-MM-DD HH:mm")} (${dayjs(ms).fromNow()})`;
};

const Section = ({
  title,
  data,
  showIP,
}: {
  title: string;
  data: HBSummary | null;
  showIP: boolean;
}) => {
  const items = useMemo(() => {
    if (!data) return [];

    return [...data.items].sort((a, b) => b.firstSeenMs - a.firstSeenMs);
  }, [data]);
  const versionStats = useMemo(() => {
    const m: Record<string, number> = {};

    items.forEach((it) => {
      const v = it.version || "未知";

      m[v] = (m[v] || 0) + 1;
    });

    return m;
  }, [items]);

  return (
    <Card className="w-full">
      <CardHeader className="flex flex-col items-start gap-1">
        <div className="text-base font-semibold">{title}</div>
        {data && (
          <div className="text-sm text-gray-500 dark:text-gray-400">
            总数 {data.total} · 活跃 {data.active} · 离线{" "}
            {Math.max(data.total - data.active, 0)}
          </div>
        )}
        {items.length > 0 && (
          <div className="text-xs text-gray-500 flex flex-wrap gap-3">
            {Object.entries(versionStats).map(([ver, cnt]) => (
              <span key={ver}>
                {ver}: {cnt}
              </span>
            ))}
          </div>
        )}
      </CardHeader>
      <CardBody className="overflow-x-auto">
        {!data ? (
          <div className="flex items-center gap-2 text-sm text-gray-500">
            <Spinner size="sm" /> 读取中...
          </div>
        ) : !items || items.length === 0 ? (
          <div className="text-sm text-gray-500">暂无数据</div>
        ) : (
          <table className="min-w-full text-sm">
            <thead>
              <tr className="text-left text-gray-500 dark:text-gray-400 border-b border-gray-200 dark:border-gray-800">
                <th className="py-2 pr-2 font-medium">序号</th>
                <th className="py-2 pr-2 font-medium">版本</th>
                <th className="py-2 pr-2 font-medium">安装方式</th>
                {showIP && <th className="py-2 pr-2 font-medium">IP</th>}
                <th className="py-2 pr-2 font-medium">系统</th>
                <th className="py-2 pr-2 font-medium">架构</th>
                <th className="py-2 pr-2 font-medium whitespace-nowrap">
                  创建时间
                </th>
                <th className="py-2 pr-2 font-medium whitespace-nowrap">
                  首次上报
                </th>
                <th className="py-2 pr-2 font-medium whitespace-nowrap">
                  最新心跳
                </th>
                <th className="py-2 pr-2 font-medium whitespace-nowrap">
                  判定卸载
                </th>
              </tr>
            </thead>
            <tbody>
              {items.map((item, idx) => (
                <tr
                  key={`${item.uniqueId || idx}`}
                  className="border-b border-gray-100 dark:border-gray-800"
                >
                  <td className="py-2 pr-2 font-mono text-xs max-w-[240px] break-all">
                    #{idx + 1}
                  </td>
                  <td className="py-2 pr-2">{item.version || "-"}</td>
                  <td className="py-2 pr-2">{item.installMode || "-"}</td>
                  {showIP && (
                    <td className="py-2 pr-2 font-mono text-xs">
                      {item.ip || item.ipPrefix || "-"}
                      {(item.country || item.city) && (
                        <span className="text-xs text-gray-500 ml-1">
                          ({[item.country, item.city].filter(Boolean).join("/")}
                          )
                        </span>
                      )}
                    </td>
                  )}
                  <td className="py-2 pr-2">{item.os || "-"}</td>
                  <td className="py-2 pr-2">{item.arch || "-"}</td>
                  <td className="py-2 pr-2 whitespace-nowrap">
                    {fmt(item.createdAtMs)}
                  </td>
                  <td className="py-2 pr-2 whitespace-nowrap">
                    {fmt(item.firstSeenMs)}
                  </td>
                  <td className="py-2 pr-2 whitespace-nowrap">
                    {fmt(item.lastHeartbeatMs)}
                  </td>
                  <td className="py-2 pr-2 whitespace-nowrap">
                    {item.uninstallAtMs ? fmt(item.uninstallAtMs) : "-"}
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        )}
      </CardBody>
    </Card>
  );
};

export default function CenterPage() {
  const [data, setData] = useState<ResponseShape | null>(null);
  const [loading, setLoading] = useState(false);
  const [showIP, setShowIP] = useState(() => {
    if (typeof window === "undefined") return false;
    const v = localStorage.getItem("center_show_ip");

    return v === "1";
  });
  const [tab, setTab] = useState<"agents" | "controllers">("agents");

  const fetchData = async () => {
    setLoading(true);
    try {
      const res: any = await getHeartbeatSummary();

      if (res.code === 0 && res.data) {
        setData(res.data as ResponseShape);
      }
    } finally {
      setLoading(false);
    }
  };

  useEffect(() => {
    fetchData();
  }, []);

  const header = useMemo(() => {
    if (!data) return "节点心跳中心";
    const cur = tab === "agents" ? data.agents : data.controllers;
    const label = tab === "agents" ? "Agent" : "控制器";

    return `节点心跳中心 · ${label} ${cur.active}/${cur.total}`;
  }, [data, tab]);

  return (
    <div className="p-6 space-y-6">
      <div className="flex flex-wrap items-center justify-between gap-4">
        <div>
          <h2 className="text-2xl font-semibold text-foreground">{header}</h2>
          <p className="text-sm text-gray-500">
            用于统计 agent / 中控程序的版本、系统、架构与存活状态
          </p>
        </div>
        <div className="flex items-center gap-3">
          <div className="flex rounded border border-gray-300 dark:border-gray-700 overflow-hidden">
            <button
              className={`px-3 py-1 text-sm ${tab === "agents" ? "bg-primary-600 text-white" : "bg-transparent text-foreground"}`}
              onClick={() => setTab("agents")}
            >
              Agent
            </button>
            <button
              className={`px-3 py-1 text-sm ${tab === "controllers" ? "bg-primary-600 text-white" : "bg-transparent text-foreground"}`}
              onClick={() => setTab("controllers")}
            >
              中控
            </button>
          </div>
          <Switch
            isSelected={showIP}
            size="sm"
            onChange={(v) => {
              setShowIP(v.target.checked);
              localStorage.setItem(
                "center_show_ip",
                v.target.checked ? "1" : "0",
              );
            }}
          >
            显示IP
          </Switch>
          <button
            className="px-3 py-2 text-sm rounded-lg bg-primary-600 text-white hover:bg-primary-700 disabled:opacity-60"
            disabled={loading}
            onClick={fetchData}
          >
            {loading ? "刷新中..." : "刷新数据"}
          </button>
        </div>
      </div>

      <div className="grid grid-cols-1 gap-4">
        {tab === "agents" && (
          <Section
            data={data?.agents || null}
            showIP={showIP}
            title="Agent 节点"
          />
        )}
        {tab === "controllers" && (
          <Section
            data={data?.controllers || null}
            showIP={showIP}
            title="中控程序"
          />
        )}
      </div>
    </div>
  );
}
