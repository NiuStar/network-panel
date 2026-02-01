import Network from "./network";

// 登陆相关接口
export interface LoginData {
  username: string;
  password: string;
  captchaId: string;
}

export interface LoginResponse {
  token: string;
  role_id: number;
  name: string;
  requirePasswordChange?: boolean;
}

export const login = (data: LoginData) =>
  Network.post<LoginResponse>("/user/login", data);
export const register = (data: { username: string; password: string }) =>
  Network.post("/user/register", data);

// 用户CRUD操作 - 全部使用POST请求
export const createUser = (data: any) => Network.post("/user/create", data);
export const getAllUsers = (pageData: any = {}) =>
  Network.post("/user/list", pageData);
export const updateUser = (data: any) => Network.post("/user/update", data);
export const deleteUser = (id: number) => Network.post("/user/delete", { id });
export const getUserPackageInfo = () => Network.post("/user/package");

// 节点CRUD操作 - 全部使用POST请求
export const createNode = (data: any) => Network.post("/node/create", data);
export const getNodeList = () => Network.post("/node/list");
export const updateNode = (data: any) => Network.post("/node/update", data);
export const deleteNode = (id: number, uninstall?: boolean) =>
  Network.post("/node/delete", { id, uninstall });
export const getNodeInstallCommand = (id: number) =>
  Network.post("/node/install", { id });
export const checkNodeStatus = (nodeId?: number) => {
  const params = nodeId ? { nodeId } : {};

  return Network.post("/node/check-status", params);
};
// 设置出口节点（在节点上创建/更新 SS 服务）
export const setExitNode = (data: {
  nodeId: number;
  type?: string;
  port: number;
  password: string;
  exitIp?: string;
  allowFallback?: boolean;
  method?: string;
  observer?: string;
  limiter?: string;
  rlimiter?: string;
  metadata?: Record<string, any>;
}) => Network.post("/node/set-exit", data);
// 获取节点上次保存的出口设置
export const getExitNode = (nodeId: number, type?: string) =>
  Network.post("/node/get-exit", type ? { nodeId, type } : { nodeId });
// 查询节点上的服务
export const queryNodeServices = (data: { nodeId: number; filter?: string }) =>
  Network.post("/node/query-services", data);

// 隧道CRUD操作 - 全部使用POST请求
export const createTunnel = (data: any) => Network.post("/tunnel/create", data);
export const getTunnelList = () => Network.post("/tunnel/list");
export const getTunnelById = (id: number) =>
  Network.post("/tunnel/get", { id });
export const updateTunnel = (data: any) => Network.post("/tunnel/update", data);
export const deleteTunnel = (id: number) =>
  Network.post("/tunnel/delete", { id });
export const diagnoseTunnel = (tunnelId: number) =>
  Network.post("/tunnel/diagnose", { tunnelId });
export const diagnoseTunnelStep = (tunnelId: number, step: string) =>
  Network.post("/tunnel/diagnose-step", { tunnelId, step });
// 隧道多级路径配置
export const getTunnelPath = (tunnelId: number) =>
  Network.post("/tunnel/path/get", { tunnelId });
export const setTunnelPath = (
  tunnelId: number,
  path: number[],
  linkModes?: string[],
) => Network.post("/tunnel/path/set", { tunnelId, path, linkModes });
// 转发批量迁移（旧版转新版）
export const migrateForwardToRoute = (force?: boolean) =>
  Network.post(`/forward/migrate${force ? "?force=1" : ""}`, {});
export const checkTunnelPath = (tunnelId: number) =>
  Network.post("/tunnel/path-check", { tunnelId });
export const cleanupTunnelTemp = (tunnelId: number) =>
  Network.post("/tunnel/cleanup-temp", { tunnelId });
// 隧道每个节点出口IP（interface）设置
export const getTunnelIface = (tunnelId: number) =>
  Network.post("/tunnel/iface/get", { tunnelId });
export const setTunnelIface = (
  tunnelId: number,
  ifaces: Array<{ nodeId: number; ip: string }>,
) => Network.post("/tunnel/iface/set", { tunnelId, ifaces });
// 隧道每个节点入站绑定IP（listener 绑定 IP）
export const getTunnelBind = (tunnelId: number) =>
  Network.post("/tunnel/bind/get", { tunnelId });
export const setTunnelBind = (
  tunnelId: number,
  binds: Array<{ nodeId: number; ip: string }>,
) => Network.post("/tunnel/bind/set", { tunnelId, binds });

// EasyTier组网
export const etStatus = () => Network.get("/easytier/status");
export const etEnable = (data: {
  enable: boolean;
  masterNodeId: number;
  ip: string;
  port: number;
  autoJoin?: boolean;
}) => Network.post("/easytier/enable", data);
export const etNodes = () => Network.post("/easytier/nodes", {});
export const etJoin = (data: {
  nodeId: number;
  ip: string;
  port: number;
  peerNodeId?: number;
  peerIp?: string;
}) => Network.post("/easytier/join", data);
export const etSuggestPort = (nodeId: number) =>
  Network.post("/easytier/suggest-port", { nodeId });
export const etRemove = (nodeId: number) =>
  Network.post("/easytier/remove", { nodeId });
export const etChangePeer = (data: { nodeId: number; peerNodeId: number }) =>
  Network.post("/easytier/change-peer", data);
export const etAutoAssign = (mode: string = "chain") =>
  Network.post("/easytier/auto-assign", { mode });
export const etRedeployMaster = () =>
  Network.post("/easytier/redeploy-master", {});
export const etVersion = () => Network.get("/easytier/version");
export const etUpdateAll = () => Network.post("/easytier/update-all", {});
export const etReapplyBatch = (nodeIds: number[]) =>
  Network.post("/easytier/reapply", { nodeIds });
export const etOperate = (nodeId: number, action: string) =>
  Network.post("/easytier/operate", { nodeId, action });
export const etOperateBatch = (nodeIds: number[], action: string) =>
  Network.post("/easytier/operate-batch", { nodeIds, action });
export const etLog = (requestId: string) =>
  Network.post("/easytier/log", { requestId });
export const listNodeOps = (params: {
  nodeId?: number;
  limit?: number;
  requestId?: string;
}) => Network.post("/node/ops", params);

// 用户隧道权限管理操作 - 全部使用POST请求
export const assignUserTunnel = (data: any) =>
  Network.post("/tunnel/user/assign", data);
export const getUserTunnelList = (queryData: any = {}) =>
  Network.post("/tunnel/user/list", queryData);
export const removeUserTunnel = (params: any) =>
  Network.post("/tunnel/user/remove", params);
export const updateUserTunnel = (data: any) =>
  Network.post("/tunnel/user/update", data);
export const userTunnel = () => Network.post("/tunnel/user/tunnel");

// 用户节点权限管理
export const assignUserNode = (data: any) =>
  Network.post("/node/user/assign", data);
export const getUserNodeList = (queryData: any = {}) =>
  Network.post("/node/user/list", queryData);
export const removeUserNode = (params: any) =>
  Network.post("/node/user/remove", params);
export const updateUserNode = (data: any) =>
  Network.post("/node/user/update", data);
export const getNodeUserUsage = (data: any) =>
  Network.post("/node/user/usage", data);

// 转发CRUD操作 - 全部使用POST请求
export const createForward = (data: any) =>
  Network.post("/forward/create", data);
export const getForwardList = () => Network.post("/forward/list");
export const updateForward = (data: any) =>
  Network.post("/forward/update", data);
export const deleteForward = (id: number) =>
  Network.post("/forward/delete", { id });
export const forceDeleteForward = (id: number) =>
  Network.post("/forward/force-delete", { id });
export const batchDeleteForwards = (ids: number[]) =>
  Network.post("/forward/batch-delete", { ids });

// 转发服务控制操作 - 通过Java后端接口
export const pauseForwardService = (forwardId: number) =>
  Network.post("/forward/pause", { id: forwardId });
export const resumeForwardService = (forwardId: number) =>
  Network.post("/forward/resume", { id: forwardId });

// 转发诊断操作
export const diagnoseForward = (forwardId: number) =>
  Network.post("/forward/diagnose", { forwardId });
export const diagnoseForwardStep = (forwardId: number, step: string) =>
  Network.post("/forward/diagnose-step", { forwardId, step });
export const singboxTestForward = (forwardId: number, nodeId: number, mode: string) =>
  Network.post("/forward/singbox-test", { forwardId, nodeId, mode });
export const restartGost = (nodeId: number) =>
  Network.post("/node/restart-gost", { nodeId });
export const enableGostApi = (nodeId: number) =>
  Network.post("/node/enable-gost-api", { nodeId });

// 出口节点（含外部）
export const getExitNodes = () => Network.post("/exit/list");
export const cleanupExitNodes = () => Network.post("/exit/cleanup");
export const createExitExternal = (data: {
  name: string;
  host: string;
  port: number;
  protocol?: string;
  config?: Record<string, any>;
}) => Network.post("/exit/external/create", data);
export const updateExitExternal = (data: {
  id: number;
  name: string;
  host: string;
  port: number;
  protocol?: string;
  config?: Record<string, any>;
}) => Network.post("/exit/external/update", data);
export const deleteExitExternal = (id: number) =>
  Network.post("/exit/external/delete", { id });

// 转发排序操作
export const updateForwardOrder = (data: {
  forwards: Array<{ id: number; inx: number }>;
}) => Network.post("/forward/update-order", data);
// 转发状态聚合
export const getForwardStatus = (forwardIds?: number[]) =>
  Network.post(
    "/forward/status",
    forwardIds && forwardIds.length > 0 ? { forwardIds } : {},
  );
export const getForwardStatusDetail = (forwardId: number) =>
  Network.post("/forward/status-detail", { forwardId });
// 最近告警
export const getRecentAlerts = (limit = 50) =>
  Network.post("/alerts/recent", { limit });

// 限速规则CRUD操作 - 全部使用POST请求
export const createSpeedLimit = (data: any) =>
  Network.post("/speed-limit/create", data);
export const getSpeedLimitList = () => Network.post("/speed-limit/list");
export const updateSpeedLimit = (data: any) =>
  Network.post("/speed-limit/update", data);
export const deleteSpeedLimit = (id: number) =>
  Network.post("/speed-limit/delete", { id });

// 修改密码接口
export const updatePassword = (data: any) =>
  Network.post("/user/updatePassword", data);

// 重置流量接口
export const resetUserFlow = (data: { id: number; type: number }) =>
  Network.post("/user/reset", data);

// 网站配置相关接口
export const getConfigs = () => Network.post("/config/list");
export const getConfigByName = (name: string) =>
  Network.post("/config/get", { name });
export const updateConfigs = (configMap: Record<string, string>) =>
  Network.post("/config/update", configMap);
export const updateConfig = (name: string, value: string) =>
  Network.post("/config/update-single", { name, value });

// 验证码相关接口
export const checkCaptcha = () => Network.post("/captcha/check");
export const generateCaptcha = () => Network.post(`/captcha/generate`);
export const verifyCaptcha = (data: { captchaId: string; trackData: string }) =>
  Network.post("/captcha/verify", data);

// Agent & Node diagnostics utilities
export const agentReconcileNode = (nodeId: number) =>
  Network.post("/agent/reconcile-node", { nodeId });
export const getNodeConnections = () => Network.get("/node/connections");
export const nodeSelfCheck = (nodeId: number) =>
  Network.post("/node/self-check", { nodeId });
export const nodeDiagStart = (nodeId: number, kind: string) =>
  Network.post("/node/diag/start", { nodeId, kind });
export const nodeDiagResult = (
  nodeId: number,
  kind: string,
  requestId?: string,
) => Network.post("/node/diag/result", { nodeId, kind, requestId });
export const nodeIperf3Status = (nodeId: number) =>
  Network.post("/node/diag/iperf3-status", { nodeId });

// 探针目标管理（管理员）
export const listProbeTargets = () => Network.post("/probe/list");
export const createProbeTarget = (data: { name: string; ip: string }) =>
  Network.post("/probe/create", data);
export const updateProbeTarget = (data: {
  id: number;
  name?: string;
  ip?: string;
  status?: number;
}) => Network.post("/probe/update", data);
export const deleteProbeTarget = (id: number) =>
  Network.post("/probe/delete", { id });

// 节点网络统计
export const getNodeNetworkStats = (nodeId: number, range: string) =>
  Network.post("/node/network-stats", { nodeId, range });
export const getNodeNetworkStatsBatch = (range: string) =>
  Network.post("/node/network-stats-batch", { range });
export const getGostConfig = (nodeId: number) =>
  Network.post("/node/gost-config", { nodeId });
export const runNQTest = (nodeId: number) =>
  Network.post("/node/nq-test", { nodeId });
export const getNQResult = (nodeId: number) =>
  Network.post("/node/nq-result", { nodeId });
// 版本信息
export const getVersionInfo = () => Network.get("/version");
export const getLatestVersionInfo = () => Network.get("/version/latest");
export const upgradeToLatest = (proxyPrefix?: string) =>
  Network.post("/version/upgrade", { proxyPrefix });
// 节点接口(IP)列表（agent上报）
export const getNodeInterfaces = (nodeId: number) =>
  Network.post("/node/interfaces", { nodeId });
// 节点系统信息（时间序列）
export const getNodeSysinfo = (
  nodeId: number,
  range: string = "1h",
  limit?: number,
) => Network.post("/node/sysinfo", { nodeId, range, limit });

// Share (public, read-only)
export const shareNetworkList = (range: string = "1h") =>
  Network.post("/share/network-list", { range });
export const shareNetworkStats = (nodeId: number, range: string = "1h") =>
  Network.post("/share/network-stats", { nodeId, range });

// Heartbeat inventory
export const reportHeartbeat = (data: {
  kind: "agent" | "controller";
  uniqueId: string;
  version?: string;
  createdAtMs?: number;
  os?: string;
  arch?: string;
}) => Network.post("/stats/heartbeat", data);
export const getHeartbeatSummary = () =>
  Network.get("/stats/heartbeat/summary");
