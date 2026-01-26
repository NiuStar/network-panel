import { useEffect, useMemo, useState, type CSSProperties } from "react";
import { useParams, useNavigate } from "react-router-dom";
import { Button } from "@heroui/button";
import { Card, CardBody, CardHeader } from "@heroui/card";
import { useRef } from "react";
import toast from "react-hot-toast";

import {
  getNodeNetworkStats,
  getNodeNetworkStatsBatch,
  getNodeList,
  getNodeSysinfo,
} from "@/api";
import VirtualGrid from "@/components/VirtualGrid";

const ranges = [
  { key: "1h", label: "每小时" },
  { key: "12h", label: "每12小时" },
  { key: "1d", label: "每天" },
  { key: "7d", label: "每七天" },
  { key: "30d", label: "每月" },
];

const CARD_STYLE: CSSProperties = {
  contentVisibility: "auto",
  containIntrinsicSize: "260px 220px",
};

const formatUptime = (seconds: number) => {
  if (!seconds) return "-";
  const d = Math.floor(seconds / 86400);
  const h = Math.floor((seconds % 86400) / 3600);
  const m = Math.floor((seconds % 3600) / 60);

  return d > 0
    ? `${d}天${h}小时`
    : h > 0
      ? `${h}小时${m}分钟`
      : `${m}分钟`;
};


const addMonths = (ts: number, months: number) => {
  const d = new Date(ts);
  const day = d.getDate();
  const target = d.getMonth() + months;
  const y = d.getFullYear() + Math.floor(target / 12);
  const m = ((target % 12) + 12) % 12;
  const last = new Date(y, m + 1, 0).getDate();
  const nd = new Date(
    y,
    m,
    Math.min(day, last),
    d.getHours(),
    d.getMinutes(),
    d.getSeconds(),
    d.getMilliseconds(),
  );

  return nd.getTime();
};

const toMonths = (cd?: number) => {
  if (!cd) return undefined;
  switch (cd) {
    case 30:
      return 1;
    case 90:
      return 3;
    case 180:
      return 6;
    case 365:
      return 12;
    default:
      return undefined;
  }
};

export default function NetworkPage() {
  const params = useParams();
  const navigate = useNavigate();
  const nodeId = Number(params.id);
  const [range, setRange] = useState("1h");
  const [listKey, setListKey] = useState(0);
  const [data, setData] = useState<any>({
    results: [],
    targets: {},
    disconnects: [],
    sla: 0,
  });
  const [nodes, setNodes] = useState<any[]>([]);
  const [batch, setBatch] = useState<any>({});
  const [sysMap, setSysMap] = useState<Record<number, any>>({});
  const cycleOverride: Record<number, number> = {};
  const [nodeName, setNodeName] = useState<string>("");
  const [loading, setLoading] = useState(false);
  const chartRef = useRef<HTMLDivElement>(null);
  const chartInstanceRef = useRef<any>(null);

  // Ensure chart is disposed when leaving detail view
  useEffect(() => {
    if (!params.id && chartInstanceRef.current) {
      try {
        chartInstanceRef.current.dispose();
      } catch {}
      chartInstanceRef.current = null;
    }
  }, [params.id]);

  const load = async () => {
    setLoading(true);
    try {
      if (params.id) {
        const res = await getNodeNetworkStats(nodeId, range);

        if (res.code === 0)
          setData(res.data || { results: [], disconnects: [], sla: 0 });
        else toast.error(res.msg || "加载失败");
      } else {
        const [l, b] = await Promise.all([
          getNodeList(),
          getNodeNetworkStatsBatch(range),
        ]);

        if (l.code === 0) {
          const arr = (l.data || []) as any[];

          setNodes(arr);
          // fetch latest sysinfo per node (limit 1)
          const entries = await Promise.all(
            arr.map(async (n: any) => {
              try {
                const r: any = await getNodeSysinfo(n.id, "1h", 1);

                if (r.code === 0 && Array.isArray(r.data) && r.data.length > 0)
                  return [n.id, r.data[r.data.length - 1]];
              } catch {}

              return [n.id, null];
            }),
          );
          const m: Record<number, any> = {};

          entries.forEach(([id, s]: any) => {
            if (s) m[id] = s;
          });
          setSysMap(m);
        }
        if (b.code === 0) setBatch(b.data || {});
      }
    } catch {
      toast.error("网络错误");
    } finally {
      setLoading(false);
    }
  };

  useEffect(() => {
    load();
  }, [params.id, range]);
  useEffect(() => {
    if (!params.id) setListKey((k) => k + 1);
  }, [params.id]);

  // fetch node name for detail page
  useEffect(() => {
    if (params.id) {
      getNodeList()
        .then((res: any) => {
          if (res.code === 0 && Array.isArray(res.data)) {
            const n = res.data.find((x: any) => x.id === Number(params.id));

            if (n) setNodeName(n.name || `节点 ${params.id}`);
          }
        })
        .catch(() => {});
    } else {
      setNodeName("");
    }
  }, [params.id]);

  const grouped = useMemo(() => {
    const g: Record<string, any[]> = {};

    for (const r of data.results || []) {
      const k = String(r.targetId);

      (g[k] ||= []).push(r);
    }

    return g;
  }, [data]);

  useEffect(() => {
    const render = async () => {
      if (!chartRef.current) return;
      const echarts = await import("echarts");

      if (chartInstanceRef.current) {
        try {
          chartInstanceRef.current.dispose();
        } catch {}
        chartInstanceRef.current = null;
      }
      chartInstanceRef.current = echarts.init(chartRef.current);
      const series: any[] = [];

      Object.keys(grouped).forEach((tid) => {
        const arr = grouped[tid];
        const label = data.targets?.[tid]?.name || `目标${tid}`;

        series.push({
          type: "line",
          sampling: "lttb",
          name: `${label} RTT`,
          showSymbol: false,
          yAxisIndex: 0,
          data: arr.map((it: any) => [it.timeMs, it.ok ? it.rttMs : null]),
        });
        series.push({
          type: "line",
          sampling: "lttb",
          name: `${label} 丢包%`,
          showSymbol: false,
          yAxisIndex: 1,
          data: arr.map((it: any) => [it.timeMs, it.ok ? 0 : 100]),
        });
      });
      chartInstanceRef.current.setOption({
        tooltip: { trigger: "axis" },
        legend: { type: "scroll" },
        dataZoom: [
          { type: "inside", throttle: 50 },
          { type: "slider", height: 20 },
        ],
        xAxis: { type: "time" },
        yAxis: [
          { type: "value", name: "RTT (ms)" },
          {
            type: "value",
            name: "丢包(%)",
            min: 0,
            max: 100,
            axisLabel: { formatter: "{value}%" },
          },
        ],
        series,
        grid: { left: 40, right: 20, top: 40, bottom: 30 },
      });
      window.addEventListener("resize", handleResize);
    };
    const handleResize = () => {
      try {
        chartInstanceRef.current?.resize();
      } catch {}
    };

    render();

    return () => {
      window.removeEventListener("resize", handleResize);
      if (chartInstanceRef.current) {
        try {
          chartInstanceRef.current.dispose();
        } catch {}
        chartInstanceRef.current = null;
      }
    };
  }, [grouped, data.targets]);

  return (
    <div className="px-4 py-6 space-y-4">
      <div className="flex items-center justify-between">
        {params.id ? (
          <>
            <h2 className="text-xl font-semibold">
              {nodeName || `节点 ${params.id}`} 网络详情
            </h2>
            <div className="text-sm text-default-500">
              SLA：{(data.sla * 100).toFixed(2)}%
            </div>
          </>
        ) : (
          <h2 className="text-xl font-semibold">节点网络概览</h2>
        )}
      </div>

      <div className="flex gap-2 items-center">
        {ranges.map((r) => (
          <Button
            key={r.key}
            color={range === r.key ? "primary" : "default"}
            size="sm"
            variant={range === r.key ? "solid" : "flat"}
            onPress={() => setRange(r.key)}
          >
            {r.label}
          </Button>
        ))}
        {!params.id && (
          <Button
            size="sm"
            variant="flat"
            onPress={async () => {
              const url =
                (window.location?.origin || "") + "/app/share/network";

              try {
                await navigator.clipboard.writeText(url);
                toast.success("分享链接已复制");
              } catch {
                toast.error("复制失败：" + url);
              }
            }}
          >
            分享
          </Button>
        )}
      </div>

      {params.id ? (
        <Card key={listKey}>
          <CardHeader className="justify-between">
            <div className="font-semibold">Ping 统计（按目标）</div>
            <Button isLoading={loading} size="sm" variant="flat" onPress={load}>
              刷新
            </Button>
          </CardHeader>
          <CardBody>
            <div ref={chartRef} className="h-[360px]" />
          </CardBody>
        </Card>
      ) : (
        <Card>
          <CardHeader className="justify-between">
            <div className="font-semibold">节点网络概览（{range}）</div>
            <Button isLoading={loading} size="sm" variant="flat" onPress={load}>
              刷新
            </Button>
          </CardHeader>
          <CardBody>
            <VirtualGrid
              className="w-full"
              estimateRowHeight={220}
              items={nodes}
              maxColumns={4}
              minItemWidth={260}
              renderItem={(n: any) => {
                const s = batch?.[n.id] || {};
                const avg = s.avg ?? null;
                const latest = s.latest ?? null;
                const sys = sysMap[n.id];
                const online = n.status === 1;
                const remainDays = () => {
                  const cm =
                    cycleOverride[n.id] ||
                    n.cycleMonths ||
                    toMonths(n.cycleDays);

                  if (!cm || !n.startDateMs) return "";
                  let months = cm;
                  let exp: number;

                  exp = addMonths(n.startDateMs, months);
                  const now = Date.now();

                  while (exp <= now) exp = addMonths(exp, months);
                  const days = Math.max(
                    0,
                    Math.ceil((exp - Date.now()) / (24 * 3600 * 1000)),
                  );

                  return `${days} 天`;
                };

                return (
                  <div
                    key={n.id}
                    className="p-3 rounded border border-divider hover:shadow-sm transition cursor-pointer"
                    onClick={() => navigate(`/network/${n.id}`)}
                    style={CARD_STYLE}
                  >
                    <div className="flex items-start justify-between mb-2">
                      <div className="font-semibold truncate">{n.name}</div>
                      <span
                        className={`text-2xs px-2 py-0.5 rounded ${online ? "bg-success-100 text-success-700" : "bg-danger-100 text-danger-700"}`}
                      >
                        {online ? "在线" : "离线"}
                      </span>
                    </div>
                    <div className="grid grid-cols-2 gap-3 text-xs">
                      <div>
                        <div className="text-default-600 mb-0.5">CPU</div>
                        <div className="font-mono">
                          {online && sys
                            ? `${sys.cpu.toFixed?.(1) || sys.cpu}%`
                            : "-"}
                        </div>
                      </div>
                      <div>
                        <div className="text-default-600 mb-0.5">内存</div>
                        <div className="font-mono">
                          {online && sys
                            ? `${sys.mem.toFixed?.(1) || sys.mem}%`
                            : "-"}
                        </div>
                      </div>
                      <div>
                        <div className="text-default-600 mb-0.5">开机时间</div>
                        <div className="font-mono">
                          {online && sys ? formatUptime(sys.uptime) : "-"}
                        </div>
                      </div>
                      <div>
                        <div className="text-default-600 mb-0.5">网络</div>
                        <div className="font-mono">
                          {avg != null ? `${avg.toFixed(1)} ms` : "-"}
                        </div>
                      </div>
                    </div>

                    <div className="flex justify-between text-xs text-default-500 mt-3">
                      <span>最近: {latest != null ? `${latest}ms` : "-"}</span>
                      <span>剩余: {remainDays() || "-"}</span>
                    </div>
                  </div>
                );
              }}
            />
          </CardBody>
        </Card>
      )}

      {params.id && (
        <Card>
          <CardHeader className="font-semibold">断联记录</CardHeader>
          <CardBody>
            <div className="space-y-2 text-sm">
              {(data.disconnects || []).map((it: any) => {
                const dur = it.durationS
                  ? it.durationS
                  : it.upAtMs
                    ? Math.round((it.upAtMs - it.downAtMs) / 1000)
                    : null;

                return (
                  <div
                    key={it.id}
                    className="flex justify-between p-2 rounded bg-default-50"
                  >
                    <div>开始：{new Date(it.downAtMs).toLocaleString()}</div>
                    <div>
                      恢复：
                      {it.upAtMs ? new Date(it.upAtMs).toLocaleString() : "-"}
                    </div>
                    <div>时长：{dur !== null ? `${dur}s` : "-"}</div>
                  </div>
                );
              })}
              {(!data.disconnects || data.disconnects.length === 0) && (
                <div className="text-default-500">暂无记录</div>
              )}
            </div>
          </CardBody>
        </Card>
      )}
    </div>
  );
}
