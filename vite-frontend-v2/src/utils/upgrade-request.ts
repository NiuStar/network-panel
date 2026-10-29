import axios from "axios";

// Initialize the selected panel base URL exactly as regular API requests do.
import "@/api/network";
import type { UpgradeReply, UpgradeRequest } from "./upgrade-transaction";

export function createUpgradeRequest() {
  // Network.get/post cannot be used here: their 401 handling clears credentials
  // and can leave the promise pending, and non-2xx responses lose Docker details.
  // Snapshot its configured base URL and token so a panel switch cannot reroute a transaction.
  const baseURL = axios.defaults.baseURL;
  const token = window.localStorage.getItem("token") || "";
  const client = axios.create({ baseURL });

  return async ({ method, path, body, signal, timeoutMs }: UpgradeRequest): Promise<UpgradeReply> => {
    if (!baseURL) return { status: 0, code: -1, msg: "请先设置面板地址" };
    const response = await client.request({
      method, url: path, data: body, signal, timeout: Math.max(1, timeoutMs),
      headers: { Authorization: token, "Content-Type": "application/json" },
      validateStatus: () => true,
    });
    return { ...response.data, status: response.status };
  };
}
