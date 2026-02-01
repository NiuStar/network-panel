import { SVGProps } from "react";

export type IconSvgProps = SVGProps<SVGSVGElement> & {
  size?: number;
};

// 用户管理相关类型
export interface User {
  id: number;
  name?: string;
  user: string;
  pwd?: string;
  status: number; // 1-正常, 0-禁用
  flow: number; // 流量限制(GB)
  num: number; // 转发数量
  expTime?: number; // 过期时间戳
  flowResetTime?: number; // 流量重置日期(1-31号)
  createdTime?: number; // 创建时间戳
  inFlow?: number; // 下载流量(字节)
  outFlow?: number; // 上传流量(字节)
  usedBilled?: number; // 计费口径用量（单向取大，双向相加）
  // 统计（后端返回）
  forwardCount?: number;
  tunnelCount?: number;
  nodeCount?: number;
}

export interface UserForm {
  id?: number;
  name?: string;
  user: string;
  pwd?: string;
  status: number;
  flow: number;
  num: number;
  expTime: Date | null;
  flowResetTime: number;
}

export interface UserTunnel {
  id: number;
  userId: number;
  tunnelId: number;
  tunnelName: string;
  status: number; // 1-正常, 0-禁用
  flow: number; // 流量限制(GB)
  num: number; // 转发数量
  expTime: number; // 过期时间戳
  flowResetTime: number; // 流量重置日期
  speedId?: number | null; // 限速规则ID
  speedLimitName?: string; // 限速规则名称
  inFlow?: number; // 下载流量(字节)
  outFlow?: number; // 上传流量(字节)
  tunnelFlow?: number; // 隧道流量计算类型(1-单向, 2-双向)
}

export interface UserTunnelForm {
  tunnelId: number | null;
  flow: number;
  num: number;
  expTime: Date | null;
  flowResetTime: number;
  speedId: number | null;
}

export interface UserNode {
  id: number;
  userId: number;
  nodeId: number;
  nodeName: string;
  status: number; // 1-正常, 0-禁用
  statusReason?: string; // 禁用原因
  flow: number; // 流量限制(GB)
  num: number; // 旧字段：转发数量（不再使用）
  portRanges?: string; // 端口范围
  expTime: number; // 过期时间戳
  flowResetTime: number; // 流量重置日期
  speedMbps?: number; // 限速（Mbps）
  inFlow?: number; // 下载流量(字节)
  outFlow?: number; // 上传流量(字节)
}

export interface UserNodeForm {
  nodeId: number | null;
  flow: number;
  portRanges: string;
  expTime: Date | null;
  flowResetTime: number;
  speedMbps: number | null;
}

export interface Tunnel {
  id: number;
  name: string;
  entryNodeId: number;
  exitNodeId: number;
  entryNodeName?: string;
  exitNodeName?: string;
  status?: number;
  flow?: number; // 流量计算类型
}

export interface Pagination {
  current: number;
  size: number;
  total: number;
}
