import { useState, useEffect } from "react";
import { Input } from "@heroui/input";
import { Button } from "@heroui/button";
import { Card, CardBody } from "@heroui/card";
import { useNavigate } from "react-router-dom";
import toast from "react-hot-toast";

import { getVersionInfo } from "@/api";
import { reinitializeBaseURL } from "@/api/network";
import {
  getPanelAddresses,
  savePanelAddress,
  setCurrentPanelAddress,
  deletePanelAddress,
  validatePanelAddress,
} from "@/utils/panel";

interface PanelAddress {
  name: string;
  address: string;
  inx: boolean;
}

export const SettingsPage = () => {
  const navigate = useNavigate();
  const [panelAddresses, setPanelAddresses] = useState<PanelAddress[]>([]);
  const [newName, setNewName] = useState("");
  const [newAddress, setNewAddress] = useState("");
  const [serverVersion, setServerVersion] = useState<string>("-");
  const [agentVersion, setAgentVersion] = useState<string>("-");
  const frontendVersion = (import.meta as any).env?.VITE_APP_VERSION || "";

  const setPanelAddressesFunc = (newAddress: PanelAddress[]) => {
    setPanelAddresses(newAddress);
  };

  // 加载面板地址列表
  const loadPanelAddresses = async () => {
    (window as any).setPanelAddresses = setPanelAddressesFunc;
    getPanelAddresses();
  };

  // 添加新面板地址
  const addPanelAddress = async () => {
    if (!newName.trim() || !newAddress.trim()) {
      toast.error("请输入名称和地址");

      return;
    }

    // 验证地址格式
    if (!validatePanelAddress(newAddress.trim())) {
      toast.error(
        "地址格式不正确，请检查：\n• 必须是完整的URL格式\n• 必须以 http:// 或 https:// 开头\n• 支持域名、IPv4、IPv6 地址\n• 端口号范围：1-65535\n• 示例：http://192.168.1.100:3000",
      );

      return;
    }
    (window as any).setPanelAddresses = setPanelAddressesFunc;
    savePanelAddress(newName.trim(), newAddress.trim());
    setNewName("");
    setNewAddress("");
    toast.success("添加成功");
  };

  // 设置当前面板地址
  const setCurrentPanel = async (name: string) => {
    (window as any).setPanelAddresses = setPanelAddressesFunc;
    setCurrentPanelAddress(name);
    reinitializeBaseURL();
  };

  // 删除面板地址
  const handleDeletePanelAddress = async (name: string) => {
    (window as any).setPanelAddresses = setPanelAddressesFunc;
    deletePanelAddress(name);
    reinitializeBaseURL();
    toast.success("删除成功");
  };

  // 页面加载时获取数据
  useEffect(() => {
    loadPanelAddresses();
    getVersionInfo()
      .then((res: any) => {
        if (res.code === 0 && res.data) {
          setServerVersion(res.data.server || "-");
          setAgentVersion(res.data.agent || "-");
        }
      })
      .catch(() => {});
  }, []);

  return (
    <div className="min-h-screen bg-gray-50 dark:bg-black">
      {/* 顶部导航 */}
      <div className="bg-white dark:bg-black border-b border-gray-200 dark:border-gray-700">
        <div className="max-w-4xl mx-auto px-4 py-4">
          <div className="flex items-center gap-3">
            <Button
              isIconOnly
              className="text-gray-600 dark:text-gray-300"
              variant="light"
              onClick={() => navigate(-1)}
            >
              <svg
                className="w-5 h-5"
                fill="none"
                stroke="currentColor"
                viewBox="0 0 24 24"
              >
                <path
                  d="M15 19l-7-7 7-7"
                  strokeLinecap="round"
                  strokeLinejoin="round"
                  strokeWidth={2}
                />
              </svg>
            </Button>
            <h1 className="text-xl font-semibold text-gray-900 dark:text-white">
              面板设置
            </h1>
          </div>
        </div>
      </div>

      {/* 内容区域 */}
      <div className="max-w-4xl mx-auto px-4 py-6">
        <div className="space-y-6">
          {/* 添加新地址 */}
          <Card className="border border-gray-200 dark:border-gray-700">
            <CardBody className="p-6">
              <h2 className="text-lg font-medium text-gray-900 dark:text-white mb-4">
                添加新面板地址
              </h2>
              <div className="space-y-4">
                <div className="grid grid-cols-1 sm:grid-cols-2 gap-4">
                  <Input
                    label="名称"
                    placeholder="请输入面板名称"
                    value={newName}
                    onChange={(e) => setNewName(e.target.value)}
                  />
                  <Input
                    label="地址"
                    placeholder="http://192.168.1.100:3000"
                    value={newAddress}
                    onChange={(e) => setNewAddress(e.target.value)}
                  />
                </div>
                <Button color="primary" onClick={addPanelAddress}>
                  添加
                </Button>
              </div>
            </CardBody>
          </Card>

          {/* 地址列表 */}
          <Card className="border border-gray-200 dark:border-gray-700">
            <CardBody className="p-6">
              <h2 className="text-lg font-medium text-gray-900 dark:text-white mb-4">
                已保存的面板地址
              </h2>
              {panelAddresses.length === 0 ? (
                <p className="text-gray-500 dark:text-gray-400 text-center py-8">
                  暂无保存的面板地址
                </p>
              ) : (
                <div className="space-y-3">
                  {panelAddresses.map((panel, index) => (
                    <div
                      key={index}
                      className="border border-gray-200 dark:border-gray-600 rounded-lg p-4"
                    >
                      <div className="flex items-center justify-between">
                        <div className="flex-1">
                          <div className="flex items-center gap-2">
                            <span className="font-medium text-gray-900 dark:text-white">
                              {panel.name}
                            </span>
                            {panel.inx && (
                              <span className="px-2 py-1 bg-green-100 dark:bg-green-500/20 text-green-700 dark:text-green-300 text-xs rounded">
                                当前
                              </span>
                            )}
                          </div>
                          <p className="text-sm text-gray-500 dark:text-gray-400 mt-1">
                            {panel.address}
                          </p>
                        </div>
                        <div className="flex items-center gap-2">
                          {!panel.inx && (
                            <Button
                              color="primary"
                              size="sm"
                              variant="flat"
                              onClick={() => setCurrentPanel(panel.name)}
                            >
                              设为当前
                            </Button>
                          )}
                          <Button
                            color="danger"
                            size="sm"
                            variant="light"
                            onClick={() => handleDeletePanelAddress(panel.name)}
                          >
                            删除
                          </Button>
                        </div>
                      </div>
                    </div>
                  ))}
                </div>
              )}
            </CardBody>
          </Card>

          {/* 版本信息 */}
          <Card className="border border-gray-200 dark:border-gray-700">
            <CardBody className="p-6">
              <h2 className="text-lg font-medium text-gray-900 dark:text-white mb-4">
                版本信息
              </h2>
              <div className="grid grid-cols-1 sm:grid-cols-2 gap-4 text-sm">
                <div className="p-3 rounded border border-default-200 dark:border-default-100">
                  <div className="text-default-500">前端</div>
                  <div className="font-semibold">{frontendVersion}</div>
                </div>
                <div className="p-3 rounded border border-default-200 dark:border-default-100">
                  <div className="text-default-500">服务器</div>
                  <div className="font-semibold">{serverVersion}</div>
                </div>
                <div className="p-3 rounded border border-default-200 dark:border-default-100">
                  <div className="text-default-500">Agent（预期版本）</div>
                  <div className="font-semibold">{agentVersion}</div>
                </div>
              </div>
            </CardBody>
          </Card>
        </div>
      </div>
    </div>
  );
};
