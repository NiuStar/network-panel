export interface UpgradeStatus {
  id: string;
  state: string;
  tag?: string;
  logs?: string[];
  error?: string;
  errors?: string[];
}

export interface UpgradeReply {
  status: number;
  code: number;
  msg?: string;
  data?: UpgradeStatus;
}

export interface UpgradeRequest {
  method: "POST" | "GET";
  path: string;
  body?: { tag?: string; proxyPrefix?: string };
  signal: AbortSignal;
  timeoutMs: number;
}

interface UpgradeOptions {
  tag?: string;
  proxyPrefix?: string;
  request: (request: UpgradeRequest) => Promise<UpgradeReply>;
  onProgress: (status: UpgradeStatus) => void;
  intervalMs?: number;
  deadlineMs?: number;
  requestTimeoutMs?: number;
}

const terminalStates = new Set(["COMMITTED", "ROLLED_BACK", "FAILED", "ROLLBACK_FAILED", "RECOVERY_REQUIRED"]);
const pendingStates = new Set(["QUEUED", "PREPARING", "PREPARED", "ACTIVATING", "VERIFYING", "ROLLING_BACK"]);

// No POST retry: an interrupted acknowledgement may already have started a worker.
export function createUpgradeController() {
  let active: AbortController | undefined;
  return {
    cancel: () => active?.abort(),
    async run(options: UpgradeOptions): Promise<UpgradeStatus> {
      if (active) return { id: "", state: "BUSY" };
      const operation = new AbortController();
      active = operation;
      const deadline = Date.now() + (options.deadlineMs ?? 180000);
      let id = "";
      const unconfirmed = (): UpgradeStatus => ({
        id, state: "UNCONFIRMED",
        error: `升级结果未确认${id ? `，事务 ID: ${id}，可通过 upgrade-status 查询` : "，未收到事务 ID"}。请检查服务状态，勿重复提交升级。`,
      });
      const publish = (status: UpgradeStatus) => {
        if (!operation.signal.aborted) options.onProgress(status);
        return status;
      };
      // Bound even a transport that never settles; abort the underlying request too.
      const bounded = <T>(work: (signal: AbortSignal) => Promise<T>, ms: number): Promise<T> =>
        new Promise((resolve, reject) => {
          const request = new AbortController();
          let settled = false;
          const finish = (error: Error | undefined, value?: T) => {
            if (settled) return;
            settled = true;
            clearTimeout(timer);
            operation.signal.removeEventListener("abort", cancel);
            request.abort();
            if (error) reject(error);
            else resolve(value as T);
          };
          const cancel = () => finish(new Error("cancelled"));
          const timer = setTimeout(() => finish(new Error("timeout")), Math.max(0, ms));
          operation.signal.addEventListener("abort", cancel, { once: true });
          if (operation.signal.aborted) { cancel(); return; }
          Promise.resolve().then(() => {
            if (settled || request.signal.aborted) throw new Error("cancelled");
            return work(request.signal);
          }).then(
            value => finish(undefined, value),
            () => finish(new Error("request failed")),
          );
        });
      const request = (method: "POST" | "GET", path: string, body?: UpgradeRequest["body"]) => {
        const timeoutMs = Math.min(options.requestTimeoutMs ?? 10000, deadline - Date.now());
        return bounded(signal => options.request({ method, path, body, signal, timeoutMs }), timeoutMs);
      };
      try {
        let reply: UpgradeReply;
        try {
          reply = await request("POST", "version/upgrade", {
            ...(options.tag ? { tag: options.tag } : {}),
            ...(options.proxyPrefix ? { proxyPrefix: options.proxyPrefix } : {}),
          });
        } catch {
          return operation.signal.aborted ? { id, state: "CANCELLED" } : publish(unconfirmed());
        }
        if (operation.signal.aborted) return { id, state: "CANCELLED" };
        if (Date.now() >= deadline) return publish(unconfirmed());
        if (reply.data?.state === "IMAGE_UPGRADE_REQUIRED") {
          return publish({ id: reply.data.id || "", state: "IMAGE_UPGRADE_REQUIRED", error: [
            "Docker 部署不支持面板内替换二进制。请在宿主机拉取新版 Docker 镜像并重建容器，保留原有数据卷和配置。", reply.msg,
          ].filter(Boolean).join("\n") });
        }
        if (reply.status !== 202 || reply.code !== 0) {
          return publish({ id: "", state: "FAILED", error: reply.msg || `启动升级失败 (HTTP ${reply.status})` });
        }
        const accepted = reply.data;
        if (!accepted || typeof accepted.id !== "string" || !accepted.id || !["QUEUED", "PREPARING"].includes(accepted.state)) {
          return publish(unconfirmed());
        }
        id = accepted.id;
        publish(accepted);
        while (Date.now() < deadline && !operation.signal.aborted) {
          await bounded(signal => new Promise<void>(resolve => {
            const timer = setTimeout(resolve, Math.min(options.intervalMs ?? 1500, deadline - Date.now()));
            signal.addEventListener("abort", () => clearTimeout(timer), { once: true });
          }), deadline - Date.now());
          if (Date.now() >= deadline || operation.signal.aborted) break;
          try {
            const statusReply = await request("GET", `version/upgrade-status?id=${encodeURIComponent(id)}`);
            if (operation.signal.aborted) break;
            if (Date.now() >= deadline) break;
            const status = statusReply.data;
            if (statusReply.status >= 200 && statusReply.status < 300 && statusReply.code === 0 && status?.id === id &&
                (pendingStates.has(status.state) || terminalStates.has(status.state))) {
              publish(status);
              if (terminalStates.has(status.state)) return status;
              continue;
            }
          } catch { /* Restart disconnects and timeouts are not terminal evidence. */ }
          publish({ id, state: "VERIFYING", logs: ["连接或鉴权暂不可用，等待持久事务状态；尚未确认升级结果。"] });
        }
        return operation.signal.aborted ? { id, state: "CANCELLED" } : publish(unconfirmed());
      } catch {
        return operation.signal.aborted ? { id, state: "CANCELLED" } : publish(unconfirmed());
      } finally {
        if (active === operation) active = undefined;
      }
    },
  };
}
