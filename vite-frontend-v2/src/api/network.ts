import axios, { AxiosResponse } from "axios";

import {
  getPanelAddresses,
  getLocalCurrentPanelAddress,
  isWebViewFunc,
} from "@/utils/panel";

interface PanelAddress {
  name: string;
  address: string;
  inx: boolean;
}

const setPanelAddressesFunc = (newAddress: PanelAddress[]) => {
  newAddress.forEach((item) => {
    if (item.inx) {
      baseURL = `${item.address}/api/v1/`;
      axios.defaults.baseURL = baseURL;
    }
  });
};

function getWebViewPanelAddress() {
  (window as any).setAddresses = setPanelAddressesFunc;
  getPanelAddresses("setAddresses");
}

let baseURL: string = "";
let notifiedMissing = false;
const REQUIRE_FLAG = "panel_address_required";

const notifyMissingBaseURL = () => {
  if (notifiedMissing) return;
  notifiedMissing = true;
  try {
    window.localStorage.setItem(REQUIRE_FLAG, "1");
  } catch {}
  window.dispatchEvent(new CustomEvent("panel-address-required"));
};

const normalizeBase = (addr: string) => {
  const trimmed = addr.replace(/\/+$/, "");
  if (trimmed.endsWith("/api/v1")) return trimmed + "/";
  return `${trimmed}/api/v1/`;
};

export const reinitializeBaseURL = () => {
  if (isWebViewFunc()) {
    getWebViewPanelAddress();
  } else {
    const local = getLocalCurrentPanelAddress();
    const localBase = local?.address || "";
    const envBase = import.meta.env.VITE_API_BASE || "";
    if (localBase) {
      baseURL = normalizeBase(localBase);
    } else if (envBase) {
      baseURL = normalizeBase(envBase);
    } else {
      const host = window.location.hostname;
      if (
        host === "localhost" ||
        host === "127.0.0.1" ||
        host === "0.0.0.0"
      ) {
        baseURL = "";
        notifyMissingBaseURL();
      } else {
        baseURL = "/api/v1/";
      }
    }
    if (baseURL) {
      notifiedMissing = false;
      try {
        window.localStorage.removeItem(REQUIRE_FLAG);
      } catch {}
    }
    axios.defaults.baseURL = baseURL;
  }
};

reinitializeBaseURL();

interface ApiResponse<T = any> {
  code: number;
  msg: string;
  data: T;
}

// 处理token失效的逻辑
function handleTokenExpired() {
  // 清除localStorage中的token
  window.localStorage.removeItem("token");
  window.localStorage.removeItem("role_id");
  window.localStorage.removeItem("name");

  // 跳转到登录页面
  if (window.location.pathname !== "/") {
    window.location.href = "/";
  }
}

// 检查响应是否为token失效
function isTokenExpired(response: ApiResponse) {
  return (
    response &&
    response.code === 401 &&
    (response.msg === "未登录或token已过期" ||
      response.msg === "无效的token或token已过期" ||
      response.msg === "无法获取用户权限信息")
  );
}

const Network = {
  get: function <T = any>(
    path: string = "",
    data: any = {},
  ): Promise<ApiResponse<T>> {
    return new Promise(function (resolve) {
      // 如果baseURL是默认值且是WebView环境，说明没有设置面板地址
      if (baseURL === "") {
        notifyMissingBaseURL();
        resolve({ code: -1, msg: " - 请先设置面板地址", data: null as T });

        return;
      }

      axios
        .get(path, {
          params: data,
          timeout: 30000,
          headers: {
            Authorization: window.localStorage.getItem("token"),
          },
        })
        .then(function (response: AxiosResponse<ApiResponse<T>>) {
          // 检查是否token失效
          if (isTokenExpired(response.data)) {
            handleTokenExpired();

            return;
          }
          resolve(response.data);
        })
        .catch(function (error: any) {
          console.error("GET请求错误:", error);

          // 检查是否是401错误（token失效）
          if (error.response && error.response.status === 401) {
            handleTokenExpired();

            return;
          }

          resolve({
            code: -1,
            msg: error.message || "网络请求失败",
            data: null as T,
          });
        });
    });
  },

  post: function <T = any>(
    path: string = "",
    data: any = {},
  ): Promise<ApiResponse<T>> {
    return new Promise(function (resolve) {
      // 如果baseURL是默认值且是WebView环境，说明没有设置面板地址
      if (baseURL === "") {
        notifyMissingBaseURL();
        resolve({ code: -1, msg: " - 请先设置面板地址", data: null as T });

        return;
      }

      axios
        .post(path, data, {
          timeout: 30000,
          headers: {
            Authorization: window.localStorage.getItem("token"),
            "Content-Type": "application/json",
          },
        })
        .then(function (response: AxiosResponse<ApiResponse<T>>) {
          // 检查是否token失效
          if (isTokenExpired(response.data)) {
            handleTokenExpired();

            return;
          }
          resolve(response.data);
        })
        .catch(function (error: any) {
          console.error("POST请求错误:", error);

          // 检查是否是401错误（token失效）
          if (error.response && error.response.status === 401) {
            handleTokenExpired();

            return;
          }

          resolve({
            code: -1,
            msg: error.message || "网络请求失败",
            data: null as T,
          });
        });
    });
  },
};

export default Network;
