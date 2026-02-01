import React, { useEffect, useMemo, useRef, useState } from "react";
import { useVirtualizer } from "@tanstack/react-virtual";

type VirtualGridProps<T> = {
  items: T[];
  renderItem: (item: T, index: number) => React.ReactNode;
  minItemWidth?: number;
  minColumns?: number;
  maxColumns?: number;
  gap?: number;
  overscan?: number;
  estimateRowHeight?: number;
  className?: string;
};

function pickScrollElement(): HTMLElement | null {
  if (typeof document === "undefined") return null;
  const main = document.querySelector("main");
  if (main) {
    const style = window.getComputedStyle(main);
    if (style.overflowY === "auto" || style.overflowY === "scroll") {
      return main as HTMLElement;
    }
  }
  return (document.scrollingElement as HTMLElement) || null;
}

export default function VirtualGrid<T>({
  items,
  renderItem,
  minItemWidth = 300,
  minColumns = 1,
  maxColumns,
  gap = 16,
  overscan = 4,
  estimateRowHeight = 260,
  className,
}: VirtualGridProps<T>) {
  const containerRef = useRef<HTMLDivElement | null>(null);
  const [containerWidth, setContainerWidth] = useState(0);
  const [scrollEl, setScrollEl] = useState<HTMLElement | null>(null);

  useEffect(() => {
    setScrollEl(pickScrollElement());
  }, []);

  useEffect(() => {
    if (!containerRef.current) return;
    const ro = new ResizeObserver((entries) => {
      const entry = entries[0];
      if (entry) setContainerWidth(entry.contentRect.width);
    });
    ro.observe(containerRef.current);
    return () => ro.disconnect();
  }, []);

  const columns = useMemo(() => {
    if (containerWidth <= 0) return minColumns;
    let cols = Math.max(
      minColumns,
      Math.floor((containerWidth + gap) / (minItemWidth + gap)),
    );
    if (maxColumns && maxColumns > 0) {
      cols = Math.min(cols, maxColumns);
    }
    return cols;
  }, [containerWidth, gap, minItemWidth, minColumns, maxColumns]);

  const rowCount = useMemo(
    () => Math.ceil(items.length / columns),
    [items.length, columns],
  );

  const rowVirtualizer = useVirtualizer({
    count: rowCount,
    getScrollElement: () => scrollEl,
    estimateSize: () => estimateRowHeight,
    overscan,
  });

  useEffect(() => {
    if (!scrollEl) return;
    let alive = true;
    const run = () => {
      if (!alive) return;
      rowVirtualizer.measure();
    };
    const raf1 = requestAnimationFrame(run);
    const raf2 = requestAnimationFrame(run);
    const t1 = setTimeout(run, 120);
    if (typeof document !== "undefined" && (document as any).fonts?.ready) {
      (document as any).fonts.ready.then(run);
    }
    return () => {
      alive = false;
      cancelAnimationFrame(raf1);
      cancelAnimationFrame(raf2);
      clearTimeout(t1);
    };
  }, [columns, items.length, scrollEl, containerWidth, rowVirtualizer]);

  const virtualRows = rowVirtualizer.getVirtualItems();

  return (
    <div ref={containerRef} className={className}>
      <div
        style={{
          height: rowVirtualizer.getTotalSize(),
          position: "relative",
        }}
      >
        {virtualRows.map((row) => {
          const startIndex = row.index * columns;
          const rowItems = items.slice(startIndex, startIndex + columns);
          return (
            <div
              key={row.key}
              data-index={row.index}
              ref={rowVirtualizer.measureElement}
              style={{
                position: "absolute",
                top: 0,
                left: 0,
                width: "100%",
                transform: `translateY(${row.start}px)`,
                paddingBottom: gap,
              }}
            >
              <div
                style={{
                  display: "grid",
                  gridTemplateColumns: `repeat(${columns}, minmax(0, 1fr))`,
                  gap: `${gap}px`,
                }}
              >
                {rowItems.map((item, idx) =>
                  renderItem(item, startIndex + idx),
                )}
              </div>
            </div>
          );
        })}
      </div>
    </div>
  );
}
