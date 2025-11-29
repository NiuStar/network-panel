// 获取面板地址列表
export async function getPanelAddresses(
  callback: string = "setPanelAddresses",
) {
  if (
    (window as any).JsInterface &&
    (window as any).JsInterface.getPanelAddresses
  ) {
    (window as any).JsInterface.getPanelAddresses(callback);
  } else if ((window as any).webkit && (window as any).webkit.messageHandlers) {
    (window as any).webkit.messageHandlers.getPanelAddresses.postMessage(
      callback,
    );
  }
}

// 保存面板地址
export async function savePanelAddress(name: string, address: string) {
  if ((window as any).JsInterface) {
    (window as any).JsInterface.savePanelAddress(name, address);
  } else if ((window as any).webkit && (window as any).webkit.messageHandlers) {
    (window as any).webkit.messageHandlers.savePanelAddress.postMessage({
      name,
      address,
    });
  }
}

// 设置当前面板地址
export async function setCurrentPanelAddress(name: string) {
  if ((window as any).JsInterface) {
    (window as any).JsInterface.setCurrentPanelAddress(name);
  } else if ((window as any).webkit && (window as any).webkit.messageHandlers) {
    (window as any).webkit.messageHandlers.setCurrentPanelAddress.postMessage({
      name,
    });
  }
}

// 删除面板地址
export async function deletePanelAddress(name: string) {
  if ((window as any).JsInterface) {
    (window as any).JsInterface.deletePanelAddress(name);
  } else if ((window as any).webkit && (window as any).webkit.messageHandlers) {
    (window as any).webkit.messageHandlers.deletePanelAddress.postMessage({
      name,
    });
  }
}

export function isWebViewFunc() {
  if (
    (window as any).JsInterface !== undefined &&
    (window as any).JsInterface.getPanelAddresses !== undefined
  ) {
    return true;
  } else if (
    (window as any).webkit &&
    (window as any).webkit.messageHandlers &&
    (window as any).webkit.messageHandlers.getPanelAddresses !== undefined
  ) {
    return true;
  } else {
    return false;
  }
}

// 验证面板地址格式
export function validatePanelAddress(address: string): boolean {
  try {
    // 基本格式检查：必须以 http:// 或 https:// 开头
    if (!address.startsWith("http://") && !address.startsWith("https://")) {
      return false;
    }

    // 使用URL构造函数验证完整URL格式
    const url = new URL(address);

    // 检查主机名不能为空
    if (!url.hostname || url.hostname.trim() === "") {
      return false;
    }

    // 检查主机名
    const hostname = url.hostname;

    // 支持 localhost
    if (hostname === "localhost") {
      return true;
    }

    // 支持 IPv4 地址
    const ipv4Pattern = /^(\d{1,3}\.){3}\d{1,3}$/;

    if (ipv4Pattern.test(hostname)) {
      const parts = hostname.split(".");

      return parts.every((part) => {
        const num = parseInt(part);

        return num >= 0 && num <= 255;
      });
    }

    // 支持 IPv6 地址
    const ipv6Pattern =
      /^\[([0-9a-fA-F]{1,4}:){7}[0-9a-fA-F]{1,4}\]$|^\[([0-9a-fA-F]{1,4}:)*:([0-9a-fA-F]{1,4}:)*[0-9a-fA-F]{1,4}\]$/;

    if (ipv6Pattern.test(hostname)) {
      return true;
    }

    // 支持域名
    const domainPattern =
      /^[a-zA-Z0-9]([a-zA-Z0-9-]{0,61}[a-zA-Z0-9])?(\.[a-zA-Z0-9]([a-zA-Z0-9-]{0,61}[a-zA-Z0-9])?)*\.[a-zA-Z]{2,}$/;

    if (domainPattern.test(hostname)) {
      return true;
    }

    return false;
  } catch (error) {
    // URL构造函数失败说明格式不正确
    return false;
  }
}
