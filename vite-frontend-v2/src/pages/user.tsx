import { useState, useEffect } from "react";
import { Button } from "@heroui/button";
import { Card, CardBody, CardHeader } from "@heroui/card";
import { Input } from "@heroui/input";
import {
  Table,
  TableHeader,
  TableColumn,
  TableBody,
  TableRow,
  TableCell,
} from "@heroui/table";
import {
  Modal,
  ModalContent,
  ModalHeader,
  ModalBody,
  ModalFooter,
  useDisclosure,
} from "@heroui/modal";
import { Chip } from "@heroui/chip";
import { Select, SelectItem } from "@heroui/select";
import { RadioGroup, Radio } from "@heroui/radio";
import { DatePicker } from "@heroui/date-picker";
import { Spinner } from "@heroui/spinner";
import { Progress } from "@heroui/progress";
import toast from "react-hot-toast";
import { parseDate } from "@internationalized/date";

import {
  User,
  UserForm,
  UserNode,
  UserNodeForm,
  Pagination as PaginationType,
} from "@/types";
import {
  getAllUsers,
  createUser,
  updateUser,
  deleteUser,
  getNodeList,
  assignUserNode,
  getUserNodeList,
  removeUserNode,
  updateUserNode,
  resetUserFlow,
} from "@/api";
import { getCachedConfig } from "@/config/site";
import { usePageVisibility } from "@/hooks/usePageVisibility";
import {
  SearchIcon,
  EditIcon,
  DeleteIcon,
  UserIcon,
  SettingsIcon,
} from "@/components/icons";
import VirtualGrid from "@/components/VirtualGrid";

// 工具函数
const formatFlow = (value: number, unit: string = "bytes"): string => {
  if (unit === "gb") {
    return `${value} GB`;
  } else {
    if (value === 0) return "0 B";
    if (value < 1024) return `${value} B`;
    if (value < 1024 * 1024) return `${(value / 1024).toFixed(2)} KB`;
    if (value < 1024 * 1024 * 1024)
      return `${(value / (1024 * 1024)).toFixed(2)} MB`;

    return `${(value / (1024 * 1024 * 1024)).toFixed(2)} GB`;
  }
};

const formatDate = (timestamp: number): string => {
  return new Date(timestamp).toLocaleString();
};

const getExpireStatus = (expTime: number) => {
  const now = Date.now();

  if (expTime < now) {
    return { color: "danger" as const, text: "已过期" };
  }
  const diffDays = Math.ceil((expTime - now) / (1000 * 60 * 60 * 24));

  if (diffDays <= 7) {
    return { color: "warning" as const, text: `${diffDays}天后过期` };
  }

  return { color: "success" as const, text: "正常" };
};

// 获取用户状态（根据status字段）
const getUserStatus = (user: User) => {
  if (user.status === 1) {
    return { color: "success" as const, text: "正常" };
  } else {
    return { color: "danger" as const, text: "禁用" };
  }
};

const calculateUserTotalUsedFlow = (user: User): number => {
  if (typeof (user as any).usedBilled === "number") {
    return (user as any).usedBilled as number;
  }

  return (user.inFlow || 0) + (user.outFlow || 0);
};

const calculateNodeUsedFlow = (nodePerm: UserNode): number => {
  const inFlow = nodePerm.inFlow || 0;
  const outFlow = nodePerm.outFlow || 0;

  // 后端已按计费类型处理流量，前端直接使用入站+出站总和
  return inFlow + outFlow;
};

const normalizePortRanges = (raw: string): string => {
  if (!raw) return "";
  return raw
    .replace(/[，；]/g, ",")
    .replace(/[~～]/g, "-")
    .replace(/\s+/g, "")
    .replace(/,+/g, ",")
    .replace(/^,|,$/g, "");
};

const isValidPortRanges = (raw: string): boolean => {
  const text = normalizePortRanges(raw);
  if (!text) return true;
  const parts = text.split(",").filter(Boolean);
  for (const p of parts) {
    if (p.includes("-")) {
      const [a, b] = p.split("-", 2);
      const start = Number(a);
      const end = Number(b);
      if (!Number.isInteger(start) || !Number.isInteger(end)) return false;
      if (start < 1 || start > 65535 || end < 1 || end > 65535) return false;
      continue;
    }
    const v = Number(p);
    if (!Number.isInteger(v) || v < 1 || v > 65535) return false;
  }
  return true;
};

type NodeItem = {
  id: number;
  name: string;
  ip?: string;
};

export default function UserPage() {
  // 状态管理
  const [users, setUsers] = useState<User[]>([]);
  const [loading, setLoading] = useState(false);
  const [searchKeyword, setSearchKeyword] = useState("");
  const [pagination, setPagination] = useState<PaginationType>({
    current: 1,
    size: 10,
    total: 0,
  });

  // 用户表单相关状态
  const {
    isOpen: isUserModalOpen,
    onOpen: onUserModalOpen,
    onClose: onUserModalClose,
  } = useDisclosure();
  const [isEdit, setIsEdit] = useState(false);
  const [userForm, setUserForm] = useState<UserForm>({
    user: "",
    pwd: "",
    status: 1,
    flow: 100,
    num: 10,
    expTime: null,
    flowResetTime: 0,
  });
  const [userFormLoading, setUserFormLoading] = useState(false);

  // 节点权限管理相关状态
  const {
    isOpen: isNodeModalOpen,
    onOpen: onNodeModalOpen,
    onClose: onNodeModalClose,
  } = useDisclosure();
  const [currentUser, setCurrentUser] = useState<User | null>(null);
  const [userNodes, setUserNodes] = useState<UserNode[]>([]);
  const [nodeListLoading, setNodeListLoading] = useState(false);

  // 分配新节点权限相关状态
  const [nodeForm, setNodeForm] = useState<UserNodeForm>({
    nodeId: null,
    flow: 100,
    portRanges: "",
    expTime: null,
    flowResetTime: 0,
    speedMbps: null,
  });
  const [assignLoading, setAssignLoading] = useState(false);

  // 编辑节点权限相关状态
  const {
    isOpen: isEditNodeModalOpen,
    onOpen: onEditNodeModalOpen,
    onClose: onEditNodeModalClose,
  } = useDisclosure();
  const [editNodeForm, setEditNodeForm] = useState<UserNode | null>(null);
  const [editNodeLoading, setEditNodeLoading] = useState(false);

  // 删除确认相关状态
  const {
    isOpen: isDeleteModalOpen,
    onOpen: onDeleteModalOpen,
    onClose: onDeleteModalClose,
  } = useDisclosure();
  const [userToDelete, setUserToDelete] = useState<User | null>(null);

  // 删除节点权限确认相关状态
  const {
    isOpen: isDeleteNodeModalOpen,
    onOpen: onDeleteNodeModalOpen,
    onClose: onDeleteNodeModalClose,
  } = useDisclosure();
  const [nodeToDelete, setNodeToDelete] = useState<UserNode | null>(null);

  // 重置流量确认相关状态
  const {
    isOpen: isResetFlowModalOpen,
    onOpen: onResetFlowModalOpen,
    onClose: onResetFlowModalClose,
  } = useDisclosure();
  const [userToReset, setUserToReset] = useState<User | null>(null);
  const [resetFlowLoading, setResetFlowLoading] = useState(false);

  // 重置节点流量确认相关状态
  const {
    isOpen: isResetNodeFlowModalOpen,
    onOpen: onResetNodeFlowModalOpen,
    onClose: onResetNodeFlowModalClose,
  } = useDisclosure();
  const [nodeToReset, setNodeToReset] = useState<UserNode | null>(null);
  const [resetNodeFlowLoading, setResetNodeFlowLoading] = useState(false);

  // 其他数据
  const [nodes, setNodes] = useState<NodeItem[]>([]);
  const pageVisible = usePageVisibility();

  // 生命周期
  useEffect(() => {
    loadUsers();
    loadNodes();
  }, [pagination.current, pagination.size, searchKeyword]);

  // 轮询刷新用户列表与（可选）当前用户的节点用量，间隔从网站配置 poll_interval_sec 读取（默认3秒）
  const [pollMs, setPollMs] = useState<number>(3000);

  useEffect(() => {
    (async () => {
      try {
        const v = await getCachedConfig("poll_interval_sec");
        const n = Math.max(1, parseInt(String(v || "3"), 10));

        setPollMs(n * 1000);
      } catch {}
    })();
  }, []);
  useEffect(() => {
    let timer: any;
    const tick = async () => {
      if (!pageVisible) return;
      try {
        // 刷新用户列表（静默，不影响加载状态）
        const res: any = await getAllUsers({
          current: pagination.current,
          size: pagination.size,
          keyword: searchKeyword,
        });

        if (res && res.code === 0) {
          setUsers(res.data || []);
        }
        // 若正在查看某个用户的节点权限，则顺带刷新其用量
        if (isNodeModalOpen && currentUser?.id) {
          const r2: any = await getUserNodeList({ userId: currentUser.id });

          if (r2 && r2.code === 0) setUserNodes(r2.data || []);
        }
      } catch {
        /* ignore */
      }
    };

    tick();
    timer = setInterval(tick, pollMs);

    return () => {
      if (timer) clearInterval(timer);
    };
  }, [
    pollMs,
    pagination.current,
    pagination.size,
    searchKeyword,
    isNodeModalOpen,
    currentUser?.id,
    pageVisible,
  ]);

  // 数据加载函数
  const loadUsers = async () => {
    setLoading(true);
    try {
      const response = await getAllUsers({
        current: pagination.current,
        size: pagination.size,
        keyword: searchKeyword,
      });

      if (response.code === 0) {
        const data = response.data || {};

        setUsers(data || []);
      } else {
        toast.error(response.msg || "获取用户列表失败");
      }
    } catch (error) {
      toast.error("获取用户列表失败");
    } finally {
      setLoading(false);
    }
  };

  const loadNodes = async () => {
    try {
      const response = await getNodeList();

      if (response.code === 0) {
        setNodes(response.data || []);
      }
    } catch (error) {
      console.error("获取节点列表失败:", error);
    }
  };

  const loadUserNodes = async (userId: number) => {
    setNodeListLoading(true);
    try {
      const response = await getUserNodeList({ userId });

      if (response.code === 0) {
        setUserNodes(response.data || []);
      } else {
        toast.error(response.msg || "获取节点权限列表失败");
      }
    } catch (error) {
      toast.error("获取节点权限列表失败");
    } finally {
      setNodeListLoading(false);
    }
  };

  // 用户管理操作
  const handleSearch = () => {
    setPagination((prev) => ({ ...prev, current: 1 }));
    loadUsers();
  };

  const handleAdd = () => {
    setIsEdit(false);
    setUserForm({
      user: "",
      pwd: "",
      status: 1,
      flow: 100,
      num: 10,
      expTime: null,
      flowResetTime: 0,
    });
    onUserModalOpen();
  };

  const handleEdit = (user: User) => {
    setIsEdit(true);
    setUserForm({
      id: user.id,
      name: user.name,
      user: user.user,
      pwd: "",
      status: user.status,
      flow: user.flow,
      num: user.num,
      expTime: user.expTime ? new Date(user.expTime) : null,
      flowResetTime: user.flowResetTime ?? 0,
    });
    onUserModalOpen();
  };

  const handleDelete = (user: User) => {
    setUserToDelete(user);
    onDeleteModalOpen();
  };

  const handleConfirmDelete = async () => {
    if (!userToDelete) return;

    try {
      const response = await deleteUser(userToDelete.id);

      if (response.code === 0) {
        toast.success("删除成功");
        loadUsers();
        onDeleteModalClose();
        setUserToDelete(null);
      } else {
        toast.error(response.msg || "删除失败");
      }
    } catch (error) {
      toast.error("删除失败");
    }
  };

  const handleSubmitUser = async () => {
    if (!userForm.user || (!userForm.pwd && !isEdit) || !userForm.expTime) {
      toast.error("请填写完整信息");

      return;
    }

    setUserFormLoading(true);
    try {
      const submitData: any = {
        ...userForm,
        expTime: userForm.expTime.getTime(),
      };

      if (isEdit && !submitData.pwd) {
        delete submitData.pwd;
      }

      const response = isEdit
        ? await updateUser(submitData)
        : await createUser(submitData);

      if (response.code === 0) {
        toast.success(isEdit ? "更新成功" : "创建成功");
        onUserModalClose();
        loadUsers();
      } else {
        toast.error(response.msg || (isEdit ? "更新失败" : "创建失败"));
      }
    } catch (error) {
      toast.error(isEdit ? "更新失败" : "创建失败");
    } finally {
      setUserFormLoading(false);
    }
  };

  // 节点权限管理操作
  const handleManageNodes = (user: User) => {
    setCurrentUser(user);
    setNodeForm({
      nodeId: null,
      flow: 100,
      portRanges: "",
      expTime: null,
      flowResetTime: 0,
      speedMbps: null,
    });
    onNodeModalOpen();
    loadUserNodes(user.id);
  };

  const handleAssignNode = async () => {
    if (!nodeForm.nodeId || !nodeForm.expTime || !currentUser) {
      toast.error("请填写完整信息");

      return;
    }
    if (!isValidPortRanges(nodeForm.portRanges)) {
      toast.error("端口范围格式不正确");
      return;
    }

    setAssignLoading(true);
    try {
      const response = await assignUserNode({
        userId: currentUser.id,
        nodeId: nodeForm.nodeId,
        flow: nodeForm.flow,
        portRanges: normalizePortRanges(nodeForm.portRanges),
        expTime: nodeForm.expTime.getTime(),
        flowResetTime: nodeForm.flowResetTime,
        speedMbps: nodeForm.speedMbps ?? undefined,
      });

      if (response.code === 0) {
        toast.success("分配成功");
        setNodeForm({
          nodeId: null,
          flow: 100,
          portRanges: "",
          expTime: null,
          flowResetTime: 0,
          speedMbps: null,
        });
        loadUserNodes(currentUser.id);
      } else {
        toast.error(response.msg || "分配失败");
      }
    } catch (error) {
      toast.error("分配失败");
    } finally {
      setAssignLoading(false);
    }
  };

  const handleEditNode = (userNode: UserNode) => {
    setEditNodeForm({
      ...userNode,
      expTime: userNode.expTime,
    });
    onEditNodeModalOpen();
  };

  const handleUpdateNode = async () => {
    if (!editNodeForm) return;

    setEditNodeLoading(true);
    try {
      if (!isValidPortRanges(editNodeForm.portRanges || "")) {
        toast.error("端口范围格式不正确");
        return;
      }
      const response = await updateUserNode({
        id: editNodeForm.id,
        flow: editNodeForm.flow,
        portRanges: normalizePortRanges(editNodeForm.portRanges || ""),
        expTime: editNodeForm.expTime,
        flowResetTime: editNodeForm.flowResetTime,
        speedMbps:
          typeof editNodeForm.speedMbps === "number"
            ? editNodeForm.speedMbps
            : null,
        status: editNodeForm.status,
      });

      if (response.code === 0) {
        toast.success("更新成功");
        onEditNodeModalClose();
        if (currentUser) {
          loadUserNodes(currentUser.id);
        }
      } else {
        toast.error(response.msg || "更新失败");
      }
    } catch (error) {
      toast.error("更新失败");
    } finally {
      setEditNodeLoading(false);
    }
  };

  const handleRemoveNode = (userNode: UserNode) => {
    setNodeToDelete(userNode);
    onDeleteNodeModalOpen();
  };

  const handleConfirmRemoveNode = async () => {
    if (!nodeToDelete) return;

    try {
      const response = await removeUserNode({ id: nodeToDelete.id });

      if (response.code === 0) {
        toast.success("删除成功");
        if (currentUser) {
          loadUserNodes(currentUser.id);
        }
        onDeleteNodeModalClose();
        setNodeToDelete(null);
      } else {
        toast.error(response.msg || "删除失败");
      }
    } catch (error) {
      toast.error("删除失败");
    }
  };

  // 重置流量相关函数
  const handleResetFlow = (user: User) => {
    setUserToReset(user);
    onResetFlowModalOpen();
  };

  const handleConfirmResetFlow = async () => {
    if (!userToReset) return;

    setResetFlowLoading(true);
    try {
      const response = await resetUserFlow({
        id: userToReset.id,
        type: 1, // 1表示重置用户流量
      });

      if (response.code === 0) {
        toast.success("流量重置成功");
        onResetFlowModalClose();
        setUserToReset(null);
        loadUsers(); // 重新加载用户列表
      } else {
        toast.error(response.msg || "重置失败");
      }
    } catch (error) {
      toast.error("重置失败");
    } finally {
      setResetFlowLoading(false);
    }
  };

  // 节点流量重置相关函数
  const handleResetNodeFlow = (userNode: UserNode) => {
    setNodeToReset(userNode);
    onResetNodeFlowModalOpen();
  };

  const handleConfirmResetNodeFlow = async () => {
    if (!nodeToReset) return;

    setResetNodeFlowLoading(true);
    try {
      const response = await resetUserFlow({
        id: nodeToReset.id,
        type: 3, // 3表示重置节点流量
      });

      if (response.code === 0) {
        toast.success("节点流量重置成功");
        onResetNodeFlowModalClose();
        setNodeToReset(null);
        if (currentUser) {
          loadUserNodes(currentUser.id); // 重新加载节点权限列表
        }
      } else {
        toast.error(response.msg || "重置失败");
      }
    } catch (error) {
      toast.error("重置失败");
    } finally {
      setResetNodeFlowLoading(false);
    }
  };

  // 过滤数据
  const availableNodes = nodes.filter(
    (node) => !userNodes.some((un) => un.nodeId === node.id),
  );

  return (
    <div className="np-page">
      {/* 页面头部 */}
      <div className="np-page-header">
        <div>
          <h1 className="np-page-title">用户管理</h1>
          <p className="np-page-desc">管理账号、套餐与节点权限。</p>
        </div>
        <Button color="primary" variant="flat" onPress={handleAdd}>
          新增
        </Button>
      </div>

      <div className="flex flex-col sm:flex-row gap-3 items-stretch sm:items-center justify-between">
        <div className="flex items-center gap-3 flex-1 max-w-md">
          <Input
            className="flex-1"
            classNames={{
              base: "bg-default-100",
              input: "bg-transparent",
              inputWrapper:
                "bg-default-100 border-2 border-default-200 hover:border-default-300 focus-within:border-primary data-[hover=true]:border-default-300",
            }}
            placeholder="搜索用户名"
            startContent={<SearchIcon className="w-4 h-4 text-default-400" />}
            value={searchKeyword}
            onChange={(e) => setSearchKeyword(e.target.value)}
            onKeyDown={(e) => e.key === "Enter" && handleSearch()}
          />
          <Button
            isIconOnly
            className="min-h-10 w-10"
            color="primary"
            variant="solid"
            onClick={handleSearch}
          >
            <SearchIcon className="w-4 h-4" />
          </Button>
        </div>
      </div>

      {/* 用户列表 */}
      {loading ? (
        <div className="space-y-4">
          <div className="flex justify-end">
            <div className="skeleton-line w-28" />
          </div>
          <div className="grid gap-4 grid-cols-1 sm:grid-cols-2 xl:grid-cols-3 2xl:grid-cols-4">
            {Array.from({ length: 8 }).map((_, idx) => (
              <div key={`user-skel-${idx}`} className="skeleton-card" />
            ))}
          </div>
        </div>
      ) : users.length === 0 ? (
        <Card className="np-card">
          <CardBody className="text-center py-16">
            <div className="flex flex-col items-center gap-4">
              <div className="w-16 h-16 bg-default-100 rounded-full flex items-center justify-center">
                <UserIcon className="w-8 h-8 text-default-400" />
              </div>
              <div>
                <h3 className="text-lg font-semibold text-foreground">
                  暂无用户数据
                </h3>
                <p className="text-default-500 text-sm mt-1">
                  还没有创建任何用户，点击上方按钮开始创建
                </p>
              </div>
            </div>
          </CardBody>
        </Card>
      ) : (
        <VirtualGrid
          className="w-full"
          estimateRowHeight={340}
          items={users}
          maxColumns={5}
          minItemWidth={260}
          renderItem={(user) => {
            const userStatus = getUserStatus(user);
            const expStatus = user.expTime
              ? getExpireStatus(user.expTime)
              : null;
            const usedFlow = calculateUserTotalUsedFlow(user);
            const flowPercent =
              user.flow > 0
                ? Math.min(
                    (usedFlow / (user.flow * 1024 * 1024 * 1024)) * 100,
                    100,
                  )
                : 0;

            return (
              <Card
                key={user.id}
                className="list-card hover:shadow-md transition-shadow duration-200"
              >
                <CardHeader className="pb-2">
                  <div className="flex justify-between items-start w-full">
                    <div className="flex-1 min-w-0">
                      <h3 className="font-semibold text-foreground truncate text-sm">
                        {user.name || user.user}
                      </h3>
                      <p className="text-xs text-default-500 truncate">
                        @{user.user}
                      </p>
                    </div>
                    <div className="flex items-center gap-1.5 ml-2">
                      <Chip
                        className="text-xs"
                        color={userStatus.color}
                        size="sm"
                        variant="flat"
                      >
                        {userStatus.text}
                      </Chip>
                    </div>
                  </div>
                </CardHeader>

                <CardBody className="pt-0 pb-3">
                  <div className="space-y-2">
                    {/* 流量信息 */}
                    <div className="space-y-1.5">
                      <div className="flex justify-between text-sm">
                        <span className="text-default-600">流量限制</span>
                        <span className="font-medium text-xs">
                          {formatFlow(user.flow, "gb")}
                        </span>
                      </div>
                      <div className="flex justify-between text-sm">
                        <span className="text-default-600">已使用</span>
                        <span className="font-medium text-xs text-danger">
                          {formatFlow(usedFlow)}
                        </span>
                      </div>
                      <Progress
                        aria-label={`流量使用 ${flowPercent.toFixed(1)}%`}
                        className="mt-1"
                        color={
                          flowPercent > 90
                            ? "danger"
                            : flowPercent > 70
                              ? "warning"
                              : "success"
                        }
                        size="sm"
                        value={flowPercent}
                      />
                    </div>

                    {/* 其他信息 */}
                    <div className="space-y-1.5 pt-2 border-t border-divider">
                      <div className="flex justify-between text-sm">
                        <span className="text-default-600">转发数量</span>
                        <span className="font-medium text-xs">
                          {(user as any).forwardCount ?? "-"}
                        </span>
                      </div>
                      <div className="flex justify-between text-sm">
                        <span className="text-default-600">节点数量</span>
                        <span className="font-medium text-xs">
                          {(user as any).nodeCount ?? "-"}
                        </span>
                      </div>
                      <div className="flex justify-between text-sm">
                        <span className="text-default-600">重置日期</span>
                        <span className="text-xs">
                          {user.flowResetTime === 0
                            ? "不重置"
                            : `每月${user.flowResetTime}号`}
                        </span>
                      </div>
                      {user.expTime && (
                        <div className="flex justify-between text-sm">
                          <span className="text-default-600">过期时间</span>
                          <div className="text-right">
                            {expStatus && expStatus.color === "success" ? (
                              <div className="text-xs">
                                {formatDate(user.expTime)}
                              </div>
                            ) : (
                              <Chip
                                className="text-xs"
                                color={expStatus?.color || "default"}
                                size="sm"
                                variant="flat"
                              >
                                {expStatus?.text || "未知状态"}
                              </Chip>
                            )}
                          </div>
                        </div>
                      )}
                    </div>
                  </div>

                  <div className="space-y-1.5 mt-3">
                    {/* 第一行：编辑和重置 */}
                    <div className="flex gap-1.5">
                      <Button
                        className="flex-1 min-h-8"
                        color="primary"
                        size="sm"
                        startContent={<EditIcon className="w-3 h-3" />}
                        variant="flat"
                        onPress={() => handleEdit(user)}
                      >
                        编辑
                      </Button>
                      <Button
                        className="flex-1 min-h-8"
                        color="warning"
                        size="sm"
                        startContent={
                          <svg
                            className="w-3 h-3"
                            fill="currentColor"
                            viewBox="0 0 20 20"
                          >
                            <path
                              clipRule="evenodd"
                              d="M4 2a1 1 0 011 1v2.101a7.002 7.002 0 0111.601 2.566 1 1 0 11-1.885.666A5.002 5.002 0 005.999 7H9a1 1 0 010 2H4a1 1 0 01-1-1V3a1 1 0 011-1zm.008 9.057a1 1 0 011.276.61A5.002 5.002 0 0014.001 13H11a1 1 0 110-2h5a1 1 0 011 1v5a1 1 0 11-2 0v-2.101a7.002 7.002 0 01-11.601-2.566 1 1 0 01.61-1.276z"
                              fillRule="evenodd"
                            />
                          </svg>
                        }
                        variant="flat"
                        onPress={() => handleResetFlow(user)}
                      >
                        重置
                      </Button>
                    </div>

                    {/* 第二行：权限和删除 */}
                    <div className="flex gap-1.5">
                      <Button
                        className="flex-1 min-h-8"
                        color="success"
                        size="sm"
                        startContent={<SettingsIcon className="w-3 h-3" />}
                        variant="flat"
                        onPress={() => handleManageNodes(user)}
                      >
                        权限
                      </Button>
                      <Button
                        className="flex-1 min-h-8"
                        color="danger"
                        size="sm"
                        startContent={<DeleteIcon className="w-3 h-3" />}
                        variant="flat"
                        onPress={() => handleDelete(user)}
                      >
                        删除
                      </Button>
                    </div>
                  </div>
                </CardBody>
              </Card>
            );
          }}
        />
      )}

      {/* 用户表单模态框 */}
      <Modal
        backdrop="opaque"
        disableAnimation
        isOpen={isUserModalOpen}
        placement="center"
        scrollBehavior="outside"
        size="2xl"
        onClose={onUserModalClose}
      >
        <ModalContent>
          <ModalHeader>{isEdit ? "编辑用户" : "新增用户"}</ModalHeader>
          <ModalBody>
            <div className="grid grid-cols-1 md:grid-cols-2 gap-4">
              <Input
                isRequired
                label="用户名"
                value={userForm.user}
                onChange={(e) =>
                  setUserForm((prev) => ({ ...prev, user: e.target.value }))
                }
              />
              <Input
                isRequired={!isEdit}
                label="密码"
                placeholder={isEdit ? "留空则不修改密码" : "请输入密码"}
                type="password"
                value={userForm.pwd}
                onChange={(e) =>
                  setUserForm((prev) => ({ ...prev, pwd: e.target.value }))
                }
              />
              <Input
                isRequired
                label="流量限制(GB)"
                max="99999"
                min="1"
                type="number"
                value={userForm.flow.toString()}
                onChange={(e) => {
                  const value = Math.min(
                    Math.max(Number(e.target.value) || 0, 1),
                    99999,
                  );

                  setUserForm((prev) => ({ ...prev, flow: value }));
                }}
              />
              <Input
                isRequired
                label="转发数量"
                max="99999"
                min="1"
                type="number"
                value={userForm.num.toString()}
                onChange={(e) => {
                  const value = Math.min(
                    Math.max(Number(e.target.value) || 0, 1),
                    99999,
                  );

                  setUserForm((prev) => ({ ...prev, num: value }));
                }}
              />
              <Select
                label="流量重置日期"
                selectedKeys={[userForm.flowResetTime.toString()]}
                onSelectionChange={(keys) => {
                  const value = Array.from(keys)[0] as string;

                  setUserForm((prev) => ({
                    ...prev,
                    flowResetTime: Number(value),
                  }));
                }}
              >
                <>
                  <SelectItem key="0" textValue="不重置">
                    不重置
                  </SelectItem>
                  {Array.from({ length: 31 }, (_, i) => i + 1).map((day) => (
                    <SelectItem
                      key={day.toString()}
                      textValue={`每月${day}号（0点重置）`}
                    >
                      每月{day}号（0点重置）
                    </SelectItem>
                  ))}
                </>
              </Select>
              <DatePicker
                isRequired
                showMonthAndYearPickers
                className="cursor-pointer"
                label="过期时间"
                value={
                  userForm.expTime
                    ? (parseDate(
                        userForm.expTime.toISOString().split("T")[0],
                      ) as any)
                    : null
                }
                onChange={(date) => {
                  if (date) {
                    const jsDate = new Date(
                      date.year,
                      date.month - 1,
                      date.day,
                      23,
                      59,
                      59,
                    );

                    setUserForm((prev) => ({ ...prev, expTime: jsDate }));
                  } else {
                    setUserForm((prev) => ({ ...prev, expTime: null }));
                  }
                }}
              />
            </div>

            <RadioGroup
              label="状态"
              orientation="horizontal"
              value={userForm.status.toString()}
              onValueChange={(value: string) =>
                setUserForm((prev) => ({ ...prev, status: Number(value) }))
              }
            >
              <Radio value="1">正常</Radio>
              <Radio value="0">禁用</Radio>
            </RadioGroup>
          </ModalBody>
          <ModalFooter>
            <Button onPress={onUserModalClose}>取消</Button>
            <Button
              color="primary"
              isLoading={userFormLoading}
              onPress={handleSubmitUser}
            >
              确定
            </Button>
          </ModalFooter>
        </ModalContent>
      </Modal>

      {/* 节点权限管理模态框 */}
      <Modal
        backdrop="opaque"
        disableAnimation
        classNames={{
          base: "max-w-[95vw] sm:max-w-4xl",
        }}
        isDismissable={false}
        isOpen={isNodeModalOpen}
        placement="center"
        scrollBehavior="outside"
        size="2xl"
        onClose={onNodeModalClose}
      >
        <ModalContent>
          <ModalHeader>用户 {currentUser?.user} 的节点权限管理</ModalHeader>
          <ModalBody>
            <div className="space-y-6">
              {/* 分配新权限部分 */}
              <div>
                <h3 className="text-lg font-semibold mb-4">分配新权限</h3>
                <div className="space-y-4">
                  <div className="grid grid-cols-1 md:grid-cols-2 gap-4">
                    <Select
                      label="选择节点"
                      selectedKeys={
                        nodeForm.nodeId
                          ? [nodeForm.nodeId.toString()]
                          : []
                      }
                      onSelectionChange={(keys) => {
                        const value = Array.from(keys)[0] as string;

                        setNodeForm((prev) => ({
                          ...prev,
                          nodeId: Number(value) || null,
                          speedMbps: null,
                        }));
                      }}
                    >
                      {availableNodes.map((node) => (
                        <SelectItem
                          key={node.id.toString()}
                          textValue={node.name}
                        >
                          {node.name}
                        </SelectItem>
                      ))}
                    </Select>

                    <Input
                      isDisabled={!nodeForm.nodeId}
                      label="限速(Mbps)"
                      placeholder="不填或 0 表示不限速"
                      min="0"
                      type="number"
                      value={
                        typeof nodeForm.speedMbps === "number"
                          ? nodeForm.speedMbps.toString()
                          : ""
                      }
                      onChange={(e) => {
                        const raw = e.target.value;
                        if (raw === "") {
                          setNodeForm((prev) => ({ ...prev, speedMbps: null }));
                          return;
                        }
                        const v = Math.max(0, Number(raw) || 0);
                        setNodeForm((prev) => ({ ...prev, speedMbps: v }));
                      }}
                    />

                    <Input
                      label="流量限制(GB)"
                      max="99999"
                      min="1"
                      type="number"
                      value={nodeForm.flow.toString()}
                      onChange={(e) => {
                        const value = Math.min(
                          Math.max(Number(e.target.value) || 0, 1),
                          99999,
                        );

                        setNodeForm((prev) => ({ ...prev, flow: value }));
                      }}
                    />

                    <Input
                      label="端口范围"
                      placeholder="例如：10000-10010,10020,10030-10040"
                      value={nodeForm.portRanges}
                      isInvalid={!isValidPortRanges(nodeForm.portRanges)}
                      errorMessage={
                        !isValidPortRanges(nodeForm.portRanges)
                          ? "格式错误：使用 10000-10010,10020"
                          : undefined
                      }
                      onChange={(e) => {
                        setNodeForm((prev) => ({
                          ...prev,
                          portRanges: e.target.value,
                        }));
                      }}
                      onBlur={() => {
                        setNodeForm((prev) => ({
                          ...prev,
                          portRanges: normalizePortRanges(prev.portRanges),
                        }));
                      }}
                    />

                    <Select
                      label="流量重置日期"
                      selectedKeys={[nodeForm.flowResetTime.toString()]}
                      onSelectionChange={(keys) => {
                        const value = Array.from(keys)[0] as string;

                        setNodeForm((prev) => ({
                          ...prev,
                          flowResetTime: Number(value),
                        }));
                      }}
                    >
                      <>
                        <SelectItem key="0" textValue="不重置">
                          不重置
                        </SelectItem>
                        {Array.from({ length: 31 }, (_, i) => i + 1).map(
                          (day) => (
                            <SelectItem
                              key={day.toString()}
                              textValue={`每月${day}号（0点重置）`}
                            >
                              每月{day}号（0点重置）
                            </SelectItem>
                          ),
                        )}
                      </>
                    </Select>

                    <DatePicker
                      showMonthAndYearPickers
                      className="cursor-pointer"
                      label="到期时间"
                      value={
                        nodeForm.expTime
                          ? (parseDate(
                              nodeForm.expTime.toISOString().split("T")[0],
                            ) as any)
                          : null
                      }
                      onChange={(date) => {
                        if (date) {
                          const jsDate = new Date(
                            date.year,
                            date.month - 1,
                            date.day,
                            23,
                            59,
                            59,
                          );

                          setNodeForm((prev) => ({
                            ...prev,
                            expTime: jsDate,
                          }));
                        } else {
                          setNodeForm((prev) => ({ ...prev, expTime: null }));
                        }
                      }}
                    />
                  </div>

                  <Button
                    color="primary"
                    isLoading={assignLoading}
                    onPress={handleAssignNode}
                  >
                    分配权限
                  </Button>
                </div>
              </div>

              {/* 已有权限部分 */}
              <div>
                <h3 className="text-lg font-semibold mb-4">已有权限</h3>
                <Table
                  aria-label="用户节点权限列表"
                  classNames={{
                    wrapper: "shadow-none",
                    th: "bg-gray-50 dark:bg-gray-800 text-gray-700 dark:text-gray-300 font-medium",
                  }}
                  >
                  <TableHeader>
                    <TableColumn>节点名称</TableColumn>
                    <TableColumn>流量统计</TableColumn>
                    <TableColumn>端口范围</TableColumn>
                    <TableColumn>状态</TableColumn>
                    <TableColumn>限速(Mbps)</TableColumn>
                    <TableColumn>重置时间</TableColumn>
                    <TableColumn>到期时间</TableColumn>
                    <TableColumn>操作</TableColumn>
                  </TableHeader>
                  <TableBody
                    emptyContent="暂无节点权限"
                    isLoading={nodeListLoading}
                    items={userNodes}
                    loadingContent={<Spinner />}
                  >
                    {(userNode) => (
                      <TableRow key={userNode.id}>
                        <TableCell>{userNode.nodeName}</TableCell>
                        <TableCell>
                          <div className="flex flex-col gap-1">
                            <div className="flex justify-between text-small">
                              <span className="text-gray-600">限制:</span>
                              <span className="font-medium">
                                {formatFlow(userNode.flow, "gb")}
                              </span>
                            </div>
                            <div className="flex justify-between text-small">
                              <span className="text-gray-600">已用:</span>
                              <span className="font-medium text-danger">
                                {formatFlow(
                                  calculateNodeUsedFlow(userNode),
                                )}
                              </span>
                            </div>
                          </div>
                        </TableCell>
                        <TableCell>
                          <span className="text-xs font-mono text-default-600">
                            {userNode.portRanges || "-"}
                          </span>
                        </TableCell>
                        <TableCell>
                          <div className="flex flex-col gap-1">
                            <Chip
                              color={
                                userNode.status === 1 ? "success" : "danger"
                              }
                              size="sm"
                              variant="flat"
                            >
                              {userNode.status === 1 ? "正常" : "禁用"}
                            </Chip>
                            {userNode.status !== 1 &&
                              userNode.statusReason && (
                                <span className="text-2xs text-danger-600">
                                  {userNode.statusReason}
                                </span>
                              )}
                          </div>
                        </TableCell>
                        <TableCell>
                          <Chip
                            color={
                              userNode.speedMbps && userNode.speedMbps > 0
                                ? "warning"
                                : "success"
                            }
                            size="sm"
                            variant="flat"
                          >
                            {userNode.speedMbps && userNode.speedMbps > 0
                              ? `${userNode.speedMbps} Mbps`
                              : "不限速"}
                          </Chip>
                        </TableCell>
                        <TableCell>
                          {userNode.flowResetTime === 0
                            ? "不重置"
                            : `每月${userNode.flowResetTime}号`}
                        </TableCell>
                        <TableCell>{formatDate(userNode.expTime)}</TableCell>
                        <TableCell>
                          <div className="flex items-center gap-2">
                            <Button
                              isIconOnly
                              color="primary"
                              size="sm"
                              variant="flat"
                              onClick={() => handleEditNode(userNode)}
                            >
                              <EditIcon className="w-4 h-4" />
                            </Button>
                            <Button
                              isIconOnly
                              color="warning"
                              size="sm"
                              title="重置流量"
                              variant="flat"
                              onClick={() => handleResetNodeFlow(userNode)}
                            >
                              <svg
                                className="w-4 h-4"
                                fill="currentColor"
                                viewBox="0 0 20 20"
                              >
                                <path
                                  clipRule="evenodd"
                                  d="M4 2a1 1 0 011 1v2.101a7.002 7.002 0 0111.601 2.566 1 1 0 11-1.885.666A5.002 5.002 0 005.999 7H9a1 1 0 010 2H4a1 1 0 01-1-1V3a1 1 0 011-1zm.008 9.057a1 1 0 011.276.61A5.002 5.002 0 0014.001 13H11a1 1 0 110-2h5a1 1 0 011 1v5a1 1 0 11-2 0v-2.101a7.002 7.002 0 01-11.601-2.566 1 1 0 01.61-1.276z"
                                  fillRule="evenodd"
                                />
                              </svg>
                            </Button>
                            <Button
                              isIconOnly
                              color="danger"
                              size="sm"
                              variant="flat"
                              onClick={() => handleRemoveNode(userNode)}
                            >
                              <DeleteIcon className="w-4 h-4" />
                            </Button>
                          </div>
                        </TableCell>
                      </TableRow>
                    )}
                  </TableBody>
                </Table>
              </div>
            </div>
          </ModalBody>
          <ModalFooter>
            <Button onPress={onNodeModalClose}>关闭</Button>
          </ModalFooter>
        </ModalContent>
      </Modal>

      {/* 编辑节点权限模态框 */}
      <Modal
        backdrop="opaque"
        disableAnimation
        isDismissable={false}
        isOpen={isEditNodeModalOpen}
        placement="center"
        scrollBehavior="outside"
        size="2xl"
        onClose={onEditNodeModalClose}
      >
        <ModalContent>
          <ModalHeader>编辑节点权限 - {editNodeForm?.nodeName}</ModalHeader>
          <ModalBody>
            {editNodeForm && (
              <>
                <div className="grid grid-cols-1 md:grid-cols-2 gap-4">
                  <Input
                    label="流量限制(GB)"
                    max="99999"
                    min="1"
                    type="number"
                    value={editNodeForm.flow.toString()}
                    onChange={(e) => {
                      const value = Math.min(
                        Math.max(Number(e.target.value) || 0, 1),
                        99999,
                      );

                      setEditNodeForm((prev) =>
                        prev ? { ...prev, flow: value } : null,
                      );
                    }}
                  />

                  <Input
                    label="端口范围"
                    placeholder="例如：10000-10010,10020,10030-10040"
                    value={editNodeForm.portRanges || ""}
                    isInvalid={!isValidPortRanges(editNodeForm.portRanges || "")}
                    errorMessage={
                      !isValidPortRanges(editNodeForm.portRanges || "")
                        ? "格式错误：使用 10000-10010,10020"
                        : undefined
                    }
                    onChange={(e) => {
                      setEditNodeForm((prev) =>
                        prev
                          ? {
                              ...prev,
                              portRanges: e.target.value,
                            }
                          : null,
                      );
                    }}
                    onBlur={() => {
                      setEditNodeForm((prev) =>
                        prev
                          ? {
                              ...prev,
                              portRanges: normalizePortRanges(
                                prev.portRanges || "",
                              ),
                            }
                          : null,
                      );
                    }}
                  />

                  <Input
                    label="限速(Mbps)"
                    placeholder="不填或 0 表示不限速"
                    min="0"
                    type="number"
                    value={
                      typeof editNodeForm.speedMbps === "number"
                        ? editNodeForm.speedMbps.toString()
                        : ""
                    }
                    onChange={(e) => {
                      const raw = e.target.value;
                      setEditNodeForm((prev) => {
                        if (!prev) return null;
                        if (raw === "") {
                          const next = { ...prev };
                          delete (next as UserNode).speedMbps;
                          return next;
                        }
                        const v = Math.max(0, Number(raw) || 0);
                        return { ...prev, speedMbps: v };
                      });
                    }}
                  />

                  <Select
                    label="流量重置日期"
                    selectedKeys={[editNodeForm.flowResetTime.toString()]}
                    onSelectionChange={(keys) => {
                      const value = Array.from(keys)[0] as string;

                      setEditNodeForm((prev) =>
                        prev ? { ...prev, flowResetTime: Number(value) } : null,
                      );
                    }}
                  >
                    <>
                      <SelectItem key="0" textValue="不重置">
                        不重置
                      </SelectItem>
                      {Array.from({ length: 31 }, (_, i) => i + 1).map(
                        (day) => (
                          <SelectItem
                            key={day.toString()}
                            textValue={`每月${day}号（0点重置）`}
                          >
                            每月{day}号（0点重置）
                          </SelectItem>
                        ),
                      )}
                    </>
                  </Select>

                  <DatePicker
                    isRequired
                    showMonthAndYearPickers
                    className="cursor-pointer"
                    label="到期时间"
                    value={
                      editNodeForm.expTime
                        ? (parseDate(
                            new Date(editNodeForm.expTime)
                              .toISOString()
                              .split("T")[0],
                          ) as any)
                        : null
                    }
                    onChange={(date) => {
                      if (date) {
                        const jsDate = new Date(
                          date.year,
                          date.month - 1,
                          date.day,
                          23,
                          59,
                          59,
                        );

                        setEditNodeForm((prev) =>
                          prev ? { ...prev, expTime: jsDate.getTime() } : null,
                        );
                      } else {
                        setEditNodeForm((prev) =>
                          prev ? { ...prev, expTime: Date.now() } : null,
                        );
                      }
                    }}
                  />
                </div>

                <RadioGroup
                  label="状态"
                  orientation="horizontal"
                  value={editNodeForm.status.toString()}
                  onValueChange={(value: string) =>
                    setEditNodeForm((prev) =>
                      prev ? { ...prev, status: Number(value) } : null,
                    )
                  }
                >
                  <Radio value="1">正常</Radio>
                  <Radio value="0">禁用</Radio>
                </RadioGroup>
              </>
            )}
          </ModalBody>
          <ModalFooter>
            <Button onPress={onEditNodeModalClose}>取消</Button>
            <Button
              color="primary"
              isLoading={editNodeLoading}
              onPress={handleUpdateNode}
            >
              确定
            </Button>
          </ModalFooter>
        </ModalContent>
      </Modal>

      {/* 删除确认对话框 */}
      <Modal
        backdrop="opaque"
        disableAnimation
        isOpen={isDeleteModalOpen}
        placement="center"
        scrollBehavior="outside"
        size="2xl"
        onClose={onDeleteModalClose}
      >
        <ModalContent>
          <ModalHeader className="flex flex-col gap-1">
            确认删除用户
          </ModalHeader>
          <ModalBody>
            <div className="flex items-center gap-4">
              <div className="w-12 h-12 bg-danger-100 rounded-full flex items-center justify-center">
                <DeleteIcon className="w-6 h-6 text-danger" />
              </div>
              <div className="flex-1">
                <p className="text-foreground">
                  确定要删除用户{" "}
                  <span className="font-semibold text-danger">
                    "{userToDelete?.user}"
                  </span>{" "}
                  吗？
                </p>
                <p className="text-small text-default-500 mt-1">
                  此操作不可撤销，用户的所有数据将被永久删除。
                </p>
              </div>
            </div>
          </ModalBody>
          <ModalFooter>
            <Button variant="light" onPress={onDeleteModalClose}>
              取消
            </Button>
            <Button color="danger" onPress={handleConfirmDelete}>
              确认删除
            </Button>
          </ModalFooter>
        </ModalContent>
      </Modal>

      {/* 删除节点权限确认对话框 */}
      <Modal
        backdrop="opaque"
        disableAnimation
        isOpen={isDeleteNodeModalOpen}
        placement="center"
        scrollBehavior="outside"
        size="2xl"
        onClose={onDeleteNodeModalClose}
      >
        <ModalContent>
          <ModalHeader className="flex flex-col gap-1">
            确认删除节点权限
          </ModalHeader>
          <ModalBody>
            <div className="flex items-center gap-4">
              <div className="w-12 h-12 bg-danger-100 rounded-full flex items-center justify-center">
                <DeleteIcon className="w-6 h-6 text-danger" />
              </div>
              <div className="flex-1">
                <p className="text-foreground">
                  确定要删除用户{" "}
                  <span className="font-semibold">{currentUser?.user}</span>{" "}
                  对节点{" "}
                  <span className="font-semibold text-danger">
                    "{nodeToDelete?.nodeName}"
                  </span>{" "}
                  的权限吗？
                </p>
                <p className="text-small text-default-500 mt-1">
                  删除后该用户将无法使用此节点创建转发，此操作不可撤销。
                </p>
              </div>
            </div>
          </ModalBody>
          <ModalFooter>
            <Button variant="light" onPress={onDeleteNodeModalClose}>
              取消
            </Button>
            <Button color="danger" onPress={handleConfirmRemoveNode}>
              确认删除
            </Button>
          </ModalFooter>
        </ModalContent>
      </Modal>

      {/* 重置流量确认对话框 */}
      <Modal
        backdrop="opaque"
        disableAnimation
        isOpen={isResetFlowModalOpen}
        placement="center"
        scrollBehavior="outside"
        size="2xl"
        onClose={onResetFlowModalClose}
      >
        <ModalContent>
          <ModalHeader className="flex flex-col gap-1">
            确认重置流量
          </ModalHeader>
          <ModalBody>
            <div className="flex items-center gap-4">
              <div className="w-12 h-12 bg-warning-100 rounded-full flex items-center justify-center">
                <svg
                  className="w-6 h-6 text-warning"
                  fill="currentColor"
                  viewBox="0 0 20 20"
                >
                  <path
                    clipRule="evenodd"
                    d="M4 2a1 1 0 011 1v2.101a7.002 7.002 0 0111.601 2.566 1 1 0 11-1.885.666A5.002 5.002 0 005.999 7H9a1 1 0 010 2H4a1 1 0 01-1-1V3a1 1 0 011-1zm.008 9.057a1 1 0 011.276.61A5.002 5.002 0 0014.001 13H11a1 1 0 110-2h5a1 1 0 011 1v5a1 1 0 11-2 0v-2.101a7.002 7.002 0 01-11.601-2.566 1 1 0 01.61-1.276z"
                    fillRule="evenodd"
                  />
                </svg>
              </div>
              <div className="flex-1">
                <p className="text-foreground">
                  确定要重置用户{" "}
                  <span className="font-semibold text-warning">
                    "{userToReset?.user}"
                  </span>{" "}
                  的流量吗？
                </p>
                <p className="text-small text-default-500 mt-1">
                  该操作只会重置账号流量不会重置节点权限流量，重置后该用户的上下行流量将归零，此操作不可撤销。
                </p>
                <div className="mt-2 p-2 bg-warning-50 dark:bg-warning-100/10 rounded text-xs">
                  <div className="text-warning-700 dark:text-warning-300">
                    当前流量使用情况：
                  </div>
                  <div className="mt-1 space-y-1">
                    <div className="flex justify-between">
                      <span>上行流量：</span>
                      <span className="font-mono">
                        {userToReset
                          ? formatFlow(userToReset.inFlow || 0)
                          : "-"}
                      </span>
                    </div>
                    <div className="flex justify-between">
                      <span>下行流量：</span>
                      <span className="font-mono">
                        {userToReset
                          ? formatFlow(userToReset.outFlow || 0)
                          : "-"}
                      </span>
                    </div>
                    <div className="flex justify-between font-medium">
                      <span>总计：</span>
                      <span className="font-mono text-warning-700 dark:text-warning-300">
                        {userToReset
                          ? formatFlow(calculateUserTotalUsedFlow(userToReset))
                          : "-"}
                      </span>
                    </div>
                  </div>
                </div>
              </div>
            </div>
          </ModalBody>
          <ModalFooter>
            <Button variant="light" onPress={onResetFlowModalClose}>
              取消
            </Button>
            <Button
              color="warning"
              isLoading={resetFlowLoading}
              onPress={handleConfirmResetFlow}
            >
              确认重置
            </Button>
          </ModalFooter>
        </ModalContent>
      </Modal>

      {/* 重置节点流量确认对话框 */}
      <Modal
        backdrop="opaque"
        disableAnimation
        isOpen={isResetNodeFlowModalOpen}
        placement="center"
        scrollBehavior="outside"
        size="2xl"
        onClose={onResetNodeFlowModalClose}
      >
        <ModalContent>
          <ModalHeader className="flex flex-col gap-1">
            确认重置节点流量
          </ModalHeader>
          <ModalBody>
            <div className="flex items-center gap-4">
              <div className="w-12 h-12 bg-warning-100 rounded-full flex items-center justify-center">
                <svg
                  className="w-6 h-6 text-warning"
                  fill="currentColor"
                  viewBox="0 0 20 20"
                >
                  <path
                    clipRule="evenodd"
                    d="M4 2a1 1 0 011 1v2.101a7.002 7.002 0 0111.601 2.566 1 1 0 11-1.885.666A5.002 5.002 0 005.999 7H9a1 1 0 010 2H4a1 1 0 01-1-1V3a1 1 0 011-1zm.008 9.057a1 1 0 011.276.61A5.002 5.002 0 0014.001 13H11a1 1 0 110-2h5a1 1 0 011 1v5a1 1 0 11-2 0v-2.101a7.002 7.002 0 01-11.601-2.566 1 1 0 01.61-1.276z"
                    fillRule="evenodd"
                  />
                </svg>
              </div>
              <div className="flex-1">
                <p className="text-foreground">
                  确定要重置用户{" "}
                  <span className="font-semibold">{currentUser?.user}</span>{" "}
                  对节点{" "}
                  <span className="font-semibold text-warning">
                    "{nodeToReset?.nodeName}"
                  </span>{" "}
                  的流量吗？
                </p>
                <p className="text-small text-default-500 mt-1">
                  该操作只会重置节点权限流量不会重置账号流量，重置后该节点权限的上下行流量将归零，此操作不可撤销。
                </p>
                <div className="mt-2 p-2 bg-warning-50 dark:bg-warning-100/10 rounded text-xs">
                  <div className="text-warning-700 dark:text-warning-300">
                    当前流量使用情况：
                  </div>
                  <div className="mt-1 space-y-1">
                    <div className="flex justify-between">
                      <span>上行流量：</span>
                      <span className="font-mono">
                        {nodeToReset
                          ? formatFlow(nodeToReset.inFlow || 0)
                          : "-"}
                      </span>
                    </div>
                    <div className="flex justify-between">
                      <span>下行流量：</span>
                      <span className="font-mono">
                        {nodeToReset
                          ? formatFlow(nodeToReset.outFlow || 0)
                          : "-"}
                      </span>
                    </div>
                    <div className="flex justify-between font-medium">
                      <span>总计：</span>
                      <span className="font-mono text-warning-700 dark:text-warning-300">
                        {nodeToReset
                          ? formatFlow(calculateNodeUsedFlow(nodeToReset))
                          : "-"}
                      </span>
                    </div>
                  </div>
                </div>
              </div>
            </div>
          </ModalBody>
          <ModalFooter>
            <Button variant="light" onPress={onResetNodeFlowModalClose}>
              取消
            </Button>
            <Button
              color="warning"
              isLoading={resetNodeFlowLoading}
              onPress={handleConfirmResetNodeFlow}
            >
              确认重置
            </Button>
          </ModalFooter>
        </ModalContent>
      </Modal>
    </div>
  );
}
