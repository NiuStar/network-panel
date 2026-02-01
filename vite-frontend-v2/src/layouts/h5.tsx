import React, { useState, useEffect } from "react";
import { useNavigate, useLocation } from "react-router-dom";

import { Logo } from "@/components/icons";
import { siteConfig, getCachedConfig } from "@/config/site";

interface TabItem {
  path: string;
  label: string;
  icon: React.ReactNode;
  adminOnly?: boolean;
}

export default function H5Layout({ children }: { children: React.ReactNode }) {
  const navigate = useNavigate();
  const location = useLocation();
  const [isAdmin, setIsAdmin] = useState(false);
  const [roleId, setRoleId] = useState<number>(1);
  const [showProbe, setShowProbe] = useState(false);

  // Tabbar配置
  const tabItems: TabItem[] = [
    {
      path: "/dashboard",
      label: "首页",
      icon: (
        <svg className="w-6 h-6" fill="currentColor" viewBox="0 0 20 20">
          <path d="M10.707 2.293a1 1 0 00-1.414 0l-7 7a1 1 0 001.414 1.414L4 10.414V17a1 1 0 001 1h2a1 1 0 001-1v-2a1 1 0 011-1h2a1 1 0 011 1v2a1 1 0 001 1h2a1 1 0 001-1v-6.586l.293.293a1 1 0 001.414-1.414l-7-7z" />
        </svg>
      ),
    },
    {
      path: "/probe",
      label: "探针",
      icon: (
        <svg className="w-6 h-6" fill="currentColor" viewBox="0 0 20 20">
          <path d="M2 11a1 1 0 011-1h2.586l2-2H8a1 1 0 110-2h1.586l2-2H14a1 1 0 110 2h-.586l-2 2H12a1 1 0 110 2h-.586l-2 2H11a1 1 0 110 2H7a1 1 0 01-1-1v-.586l-2 2V17a1 1 0 11-2 0v-4z" />
        </svg>
      ),
      adminOnly: true,
    },
    {
      path: "/forward",
      label: "转发",
      icon: (
        <svg className="w-6 h-6" fill="currentColor" viewBox="0 0 20 20">
          <path
            clipRule="evenodd"
            d="M3 17a1 1 0 011-1h12a1 1 0 110 2H4a1 1 0 01-1-1zm3.293-7.707a1 1 0 011.414 0L9 10.586V3a1 1 0 112 0v7.586l1.293-1.293a1 1 0 111.414 1.414l-3 3a1 1 0 01-1.414 0l-3-3a1 1 0 010-1.414z"
            fillRule="evenodd"
          />
        </svg>
      ),
    },
    {
      path: "/node",
      label: "节点",
      icon: (
        <svg className="w-6 h-6" fill="currentColor" viewBox="0 0 20 20">
          <path
            clipRule="evenodd"
            d="M3 3a1 1 0 000 2v8a2 2 0 002 2h2.586l-1.293 1.293a1 1 0 101.414 1.414L10 15.414l2.293 2.293a1 1 0 001.414-1.414L12.414 15H15a2 2 0 002-2V5a1 1 0 100-2H3zm11.707 4.707a1 1 0 00-1.414-1.414L10 9.586 8.707 8.293a1 1 0 00-1.414 0l-2 2a1 1 0 101.414 1.414L8 10.414l1.293 1.293a1 1 0 001.414 0l4-4z"
            fillRule="evenodd"
          />
        </svg>
      ),
    },
    {
      path: "/profile",
      label: "我的",
      icon: (
        <svg className="w-6 h-6" fill="currentColor" viewBox="0 0 20 20">
          <path d="M9 6a3 3 0 11-6 0 3 3 0 016 0zM17 6a3 3 0 11-6 0 3 3 0 016 0zM12.93 17c.046-.327.07-.66.07-1a6.97 6.97 0 00-1.5-4.33A5 5 0 0119 16v1h-6.07zM6 11a5 5 0 015 5v1H1v-1a5 5 0 015-5z" />
        </svg>
      ),
    },
  ];

  useEffect(() => {
    // 兼容处理：如果没有admin字段，根据role_id判断（0为管理员）
    let adminFlag = localStorage.getItem("admin") === "true";

    if (localStorage.getItem("admin") === null) {
      const roleId = parseInt(localStorage.getItem("role_id") || "1", 10);

      adminFlag = roleId === 0;
      // 补充设置admin字段，避免下次再次判断
      localStorage.setItem("admin", adminFlag.toString());
    }

    setIsAdmin(adminFlag);
    setRoleId(parseInt(localStorage.getItem("role_id") || "1", 10));
    (async () => {
      try {
        const sp = await getCachedConfig("show_probe");

        setShowProbe(sp === "true");
      } catch {}
    })();
  }, []);

  // Tab点击处理
  const handleTabClick = (path: string) => {
    navigate(path);
  };

  // 过滤tab项（根据权限）
  const filteredTabItems = tabItems.filter((item) => {
    if (item.path === "/probe" && !showProbe) return false;
    if (roleId === 2 && item.path === "/node")
      return false;

    return !item.adminOnly || isAdmin;
  });

  // 路由切换时回到页面顶部，避免上一页的滚动位置遗留
  useEffect(() => {
    try {
      window.scrollTo({ top: 0, left: 0, behavior: "auto" });
    } catch (e) {
      window.scrollTo(0, 0);
    }
    document.body.scrollTop = 0;
    document.documentElement.scrollTop = 0;
  }, [location.pathname]);

  return (
    <div className="flex flex-col min-h-screen bg-gray-100 dark:bg-black">
      {/* 顶部导航栏 */}
      <header className="bg-white dark:bg-black shadow-sm border-b border-gray-200 dark:border-gray-600 h-14 safe-top flex-shrink-0 flex items-center justify-between px-4 relative z-10">
        <div className="flex items-center gap-2">
          <Logo size={20} />
          <h1 className="text-sm font-bold text-foreground">
            {siteConfig.name}
          </h1>
        </div>

        <div className="flex items-center gap-2" />
      </header>

      {/* 主内容区域 */}
      <main className="flex-1 bg-gray-100 dark:bg-black">
        {children}
        {/* Sponsor block above tabbar */}
        <div className="py-2 text-center">
          <a
            aria-label="Sponsor"
            className="inline-block mb-1"
            href="https://vps.town"
            rel="noopener noreferrer"
            target="_blank"
          >
            <img
              alt="Sponsor"
              className="h-8 mx-auto object-contain"
              loading="lazy"
              src="https://vps.town/static/images/sponsor.png"
            />
          </a>
          <p className="text-xs text-gray-400 dark:text-gray-500">
            感谢vps.town提供的服务器赞助
          </p>
        </div>
      </main>

      {/* 用于给固定 Tabbar 腾出空间的占位元素 */}
      <div aria-hidden className="h-16 safe-bottom" />

      {/* 底部Tabbar */}
      <nav className="bg-white dark:bg-black border-t border-gray-200 dark:border-gray-600 h-16 safe-bottom flex-shrink-0 flex items-center justify-around px-2 fixed bottom-0 left-0 right-0 z-30">
        {filteredTabItems.map((item) => {
          const isActive = location.pathname === item.path;

          return (
            <button
              key={item.path}
              className={`
                flex flex-col items-center justify-center flex-1 h-full
                transition-colors duration-200 min-h-[44px]
                ${
                  isActive
                    ? "text-primary-600 dark:text-primary-400"
                    : "text-gray-500 dark:text-gray-400 hover:text-gray-700 dark:hover:text-gray-200"
                }
              `}
              onClick={() => handleTabClick(item.path)}
            >
              <div className="flex-shrink-0 mb-1">{item.icon}</div>
              <span className="text-xs font-medium">{item.label}</span>
            </button>
          );
        })}
      </nav>
    </div>
  );
}
