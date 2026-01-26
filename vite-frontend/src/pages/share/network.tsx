import { useEffect, useMemo, useRef, useState, type CSSProperties } from "react";
import { useNavigate, useParams } from "react-router-dom";
import { Button } from "@heroui/button";
import { Card, CardBody, CardHeader } from "@heroui/card";

import { shareNetworkList, shareNetworkStats } from "@/api";
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


export default function ShareNetworkPage() {
  const params = useParams();
  const navigate = useNavigate();
  const [range, setRange] = useState("1h");
  const [, setLoading] = useState(false);
  const [nodes, setNodes] = useState<any[]>([]);
  const [stats, setStats] = useState<Record<string, any>>({});
  const [sys, setSys] = useState<Record<string, any>>({});
  const [detail, setDetail] = useState<any>({
    results: [],
    targets: {},
    disconnects: [],
    sla: 0,
  });
  const [nodeName, setNodeName] = useState("");
  const chartRef = useRef<HTMLDivElement>(null);
  const chartInstanceRef = useRef<any>(null);

  const nodeId = params.id ? Number(params.id) : undefined;

  const load = async () => {
    setLoading(true);
    try {
      if (nodeId) {
        const r: any = await shareNetworkStats(nodeId, range);

        if (r.code === 0) setDetail(r.data);
      } else {
        const r: any = await shareNetworkList(range);

        if (r.code === 0) {
          setNodes(r.data.nodes || []);
          setStats(r.data.stats || {});
          setSys(r.data.sys || {});
        }
      }
    } finally {
      setLoading(false);
    }
  };

  useEffect(() => {
    load();
  }, [range, params.id]);

  useEffect(() => {
    if (!nodeId) return;
    const n = (nodes || []).find((x: any) => x.id === nodeId);

    setNodeName(n ? n.name : `节点 ${nodeId}`);
  }, [nodes, nodeId]);

  const grouped = useMemo(() => {
    const g: Record<string, any[]> = {};

    for (const r of detail.results || []) {
      const k = String(r.targetId);

      (g[k] ||= []).push(r);
    }

    return g;
  }, [detail]);

  useEffect(() => {
    if (!nodeId) return;
    const render = async () => {
      if (!chartRef.current) return;
      const echarts = await import("echarts");

      if (!chartInstanceRef.current)
        chartInstanceRef.current = echarts.init(chartRef.current);
      const series: any[] = [];

      Object.keys(grouped).forEach((tid) => {
        const arr = grouped[tid];
        const label = detail.targets?.[tid]?.name || `目标${tid}`;

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
    };

    render();
  }, [grouped]);

  const formatUptime = (seconds: number) => {
    if (!seconds) return "-";
    const d = Math.floor(seconds / 86400);
    const h = Math.floor((seconds % 86400) / 3600);
    const m = Math.floor((seconds % 3600) / 60);

    return d > 0 ? `${d}天${h}小时` : h > 0 ? `${h}小时${m}分钟` : `${m}分钟`;
  };

  return (
    <div className="px-4 py-6 space-y-4">
      <div className="flex items-center justify-between">
        {nodeId ? (
          <>
            <h2 className="text-xl font-semibold">{nodeName} 网络详情</h2>
            <div className="text-sm text-default-500">
              SLA：{(detail.sla * 100).toFixed(2)}%
            </div>
          </>
        ) : (
          <h2 className="text-xl font-semibold">节点网络概览（共享）</h2>
        )}
      </div>

      <div className="flex gap-2">
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
      </div>

      {nodeId ? (
        <Card>
          <CardHeader className="justify-between">
            <div className="font-semibold">Ping 统计（按目标）</div>
          </CardHeader>
          <CardBody>
            <div ref={chartRef} className="h-[360px]" />
          </CardBody>
        </Card>
      ) : (
        <Card>
          <CardHeader className="justify-between">
            <div className="font-semibold">节点网络概览（{range}）</div>
          </CardHeader>
          <CardBody>
            <VirtualGrid
              className="w-full"
              estimateRowHeight={220}
              items={nodes}
              maxColumns={4}
              minItemWidth={260}
              renderItem={(n: any) => {
                const s = stats?.[n.id] || {};
                const avg = s.avg ?? null;
                const latest = s.latest ?? null;
                const ss = sys?.[n.id];
                const online = n.status === 1;

                return (
                  <div
                    key={n.id}
                    className="p-3 rounded border border-divider hover:shadow-sm transition cursor-pointer"
                    onClick={() => navigate(`/share/network/${n.id}`)}
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
                          {online && ss
                            ? `${ss.cpu.toFixed?.(1) || ss.cpu}%`
                            : "-"}
                        </div>
                      </div>
                      <div>
                        <div className="text-default-600 mb-0.5">内存</div>
                        <div className="font-mono">
                          {online && ss
                            ? `${ss.mem.toFixed?.(1) || ss.mem}%`
                            : "-"}
                        </div>
                      </div>
                      <div>
                        <div className="text-default-600 mb-0.5">开机时间</div>
                        <div className="font-mono">
                          {online && ss ? formatUptime(ss.uptime) : "-"}
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
                    </div>
                  </div>
                );
              }}
            />
          </CardBody>
        </Card>
      )}

      {nodeId && (
        <Card>
          <CardHeader className="font-semibold">断联记录</CardHeader>
          <CardBody>
            <div className="space-y-2 text-sm">
              {(detail.disconnects || []).map((it: any) => {
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
              {(!detail.disconnects || detail.disconnects.length === 0) && (
                <div className="text-default-500">暂无记录</div>
              )}
            </div>
          </CardBody>
        </Card>
      )}
    </div>
  );
}
