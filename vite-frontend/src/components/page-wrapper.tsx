import React, { useState, useEffect } from "react";

import AdminLayout from "@/layouts/admin";

interface PageWrapperProps {
  children: React.ReactNode;
  title: string;
  description?: string;
  className?: string;
}

export default function PageWrapper({
  children,
  title,
  description,
  className = "container mx-auto max-w-7xl px-3 lg:px-6 py-8",
}: PageWrapperProps) {
  const [isReady, setIsReady] = useState(false);

  useEffect(() => {
    // 使用短暂的延迟确保组件完全加载，避免闪烁
    const timer = setTimeout(() => {
      setIsReady(true);
    }, 50);

    return () => clearTimeout(timer);
  }, []);

  if (!isReady) {
    return (
      <AdminLayout>
        <div className="container mx-auto max-w-7xl px-3 lg:px-6 py-8">
          <div className="flex items-center justify-center h-64">
            <div className="flex items-center gap-3">
              <div className="animate-spin h-5 w-5 border-2 border-gray-200 dark:border-gray-700 border-t-gray-600 dark:border-t-gray-300 rounded-full" />
              <span className="text-default-600" />
            </div>
          </div>
        </div>
      </AdminLayout>
    );
  }

  return (
    <AdminLayout>
      <div className={className}>
        <div className="mb-6">
          <h1 className="text-2xl font-bold mb-2 text-foreground">{title}</h1>
          {description && <p className="text-default-600">{description}</p>}
        </div>
        {children}
      </div>
    </AdminLayout>
  );
}
