import {
  LineChart,
  Line,
  XAxis,
  YAxis,
  CartesianGrid,
  Tooltip,
  ResponsiveContainer,
} from "recharts";

type FlowPoint = {
  time: string;
  flow: number;
  gostFlow?: number;
  anytlsFlow?: number;
};

type DashboardFlowChartProps = {
  data: FlowPoint[];
  formatFlow: (value: number) => string;
};

const DashboardFlowChart = ({ data, formatFlow }: DashboardFlowChartProps) => (
  <div className="h-64 lg:h-80 w-full">
    <ResponsiveContainer height="100%" width="100%">
      <LineChart
        data={data}
        margin={{ top: 4, right: 12, bottom: 0, left: 8 }}
      >
        <CartesianGrid className="opacity-30" strokeDasharray="3 3" />
        <XAxis
          axisLine={{ stroke: "#e5e7eb", strokeWidth: 1 }}
          dataKey="time"
          tick={{ fontSize: 12 }}
          tickLine={false}
        />
        <YAxis
          axisLine={{ stroke: "#e5e7eb", strokeWidth: 1 }}
          tick={{ fontSize: 12 }}
          tickFormatter={(value) => {
            if (value === 0) return "0";
            if (value < 1024) return `${value}B`;
            if (value < 1024 * 1024) return `${(value / 1024).toFixed(1)}K`;
            if (value < 1024 * 1024 * 1024)
              return `${(value / (1024 * 1024)).toFixed(1)}M`;

            return `${(value / (1024 * 1024 * 1024)).toFixed(1)}G`;
          }}
          tickLine={false}
        />
        <Tooltip
          content={({ active, payload, label }) => {
            if (active && payload && payload.length) {
              const p = payload[0]?.payload as FlowPoint | undefined;
              const gost = p?.gostFlow ?? 0;
              const anytls = p?.anytlsFlow ?? 0;
              return (
                <div className="bg-white dark:bg-default-100 border border-default-200 rounded-lg shadow-lg p-3">
                  <p className="font-medium text-foreground">{`时间: ${label}`}</p>
                  <p className="text-primary">
                    {`流量: ${formatFlow((payload[0]?.value as number) || 0)}`}
                  </p>
                  {(gost > 0 || anytls > 0) && (
                    <div className="text-xs text-default-600 mt-1 space-y-0.5">
                      <div>{`GOST: ${formatFlow(gost)}`}</div>
                      <div>{`AnyTLS: ${formatFlow(anytls)}`}</div>
                    </div>
                  )}
                </div>
              );
            }

            return null;
          }}
        />
        <Line
          activeDot={{ r: 4, stroke: "#8b5cf6", strokeWidth: 2, fill: "#fff" }}
          dataKey="flow"
          dot={false}
          isAnimationActive={false}
          stroke="#8b5cf6"
          strokeWidth={3}
          type="monotone"
        />
      </LineChart>
    </ResponsiveContainer>
  </div>
);

export default DashboardFlowChart;
