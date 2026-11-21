import { useCallback, useEffect, useLayoutEffect, useRef, useState } from "react";

const OVERSCAN = 8;

export interface VirtualWindow {
  startIndex: number;
  endIndex: number;
  spacerBefore: number;
  spacerAfter: number;
  virtualized: boolean;
}

function indexAt(offsets: number[], pixel: number): number {
  let low = 0;
  let high = offsets.length - 1;
  while (low < high) {
    const mid = (low + high + 1) >> 1;
    if (offsets[mid]! <= pixel) low = mid;
    else high = mid - 1;
  }
  return low;
}

export function useVirtualWindow(options: {
  itemCount: number;
  viewport: HTMLElement | null;
  estimateHeight: number;
  /** Keep the rendered window on the final items while the view follows its live tail. */
  anchorEnd?: boolean;
  /** Element wrapping exactly the rendered rows, measured to keep offsets exact. */
  windowElement?: HTMLElement | null;
  /** Stable per-row identity; must match the rows' data-vw-key attribute. */
  rowKey?: (index: number) => string;
}): VirtualWindow {
  const { itemCount, viewport, estimateHeight, anchorEnd = false, windowElement, rowKey } = options;
  const [scrollTop, setScrollTop] = useState(0);
  const [viewportHeight, setViewportHeight] = useState(0);
  const [measured, setMeasured] = useState(false);
  // Measured height per stable row key; only rows rendered at least once have
  const [rowHeights, setRowHeights] = useState<ReadonlyMap<string, number>>(() => new Map());
  // Extra rows rendered past the estimate-derived end so the rows actually
  const [extension, setExtension] = useState(0);
  // The DOM reading anchor recorded on the previous pass: a stable row key
  const anchorRef = useRef<{ key: string; contentOffset: number } | null>(null);
  const [contentVersion, setContentVersion] = useState(0);

  useEffect(() => {
    if (!viewport) return;
    const measure = () => {
      const height = viewport.clientHeight;
      setViewportHeight(height);
      if (height > 0) {
        setMeasured(true);
      }
    };
    const handleScroll = () => {
      setScrollTop(viewport.scrollTop);
      measure();
    };
    measure();
    viewport.addEventListener("scroll", handleScroll, { passive: true });
    if (typeof ResizeObserver !== "undefined") {
      const observer = new ResizeObserver(measure);
      observer.observe(viewport);
      return () => {
        observer.disconnect();
        viewport.removeEventListener("scroll", handleScroll);
      };
    }
    return () => viewport.removeEventListener("scroll", handleScroll);
  }, [viewport]);

  // Row heights also change without a scroll event whenever a rendered row
  // expands or collapses, so the measurement pass below re-runs then too.
  useEffect(() => {
    if (!windowElement || typeof ResizeObserver === "undefined") return;
    const observer = new ResizeObserver(() => setContentVersion((version) => version + 1));
    observer.observe(windowElement);
    return () => observer.disconnect();
  }, [windowElement]);

  // Reset the extension during render while the list is empty so a reloaded
  if (itemCount === 0 && extension !== 0) {
    setExtension(0);
  }

  const buildOffsets = useCallback((heights: ReadonlyMap<string, number>) => {
    const offsets = new Array<number>(itemCount + 1);
    offsets[0] = 0;
    for (let i = 0; i < itemCount; i += 1) {
      const key = rowKey?.(i);
      const measuredHeight = key === undefined ? undefined : heights.get(key);
      offsets[i + 1] = offsets[i]! + (measuredHeight !== undefined && measuredHeight > 0 ? measuredHeight : estimateHeight);
    }
    return offsets;
  }, [itemCount, rowKey, estimateHeight]);
  const offsets = buildOffsets(rowHeights);
  const totalHeight = offsets[itemCount]!;

  const height = viewportHeight > 0 ? viewportHeight : estimateHeight * 10;
  const topIndex = indexAt(offsets, scrollTop);
  const derivedStart = Math.max(0, topIndex - OVERSCAN);
  const windowSize = Math.ceil(height / estimateHeight) + OVERSCAN * 2;
  // past the estimated total, so the derived start can reach the end of the
  // list; once it does, anchor the window to the final items instead.
  const anchorTail = anchorEnd || derivedStart + windowSize >= itemCount;
  const startIndex = anchorTail ? Math.max(0, itemCount - windowSize) : derivedStart;
  const baseEnd = Math.min(itemCount, indexAt(offsets, scrollTop + height) + 1 + OVERSCAN);
  const endIndex = anchorTail ? itemCount : Math.min(itemCount, baseEnd + extension);

  // rows have committed and real layout exists. The state updates must live
  // in this effect: the measured layout only exists after commit, exactly
  /* eslint-disable react-hooks/set-state-in-effect */
  useLayoutEffect(() => {
    if (!measured || !viewport || itemCount === 0) return;
    const viewportTop = viewport.getBoundingClientRect().top;

    // 1) Keep the reading position with a DOM anchor: a row near the viewport
    // top recorded on the previous pass. The difference between its recorded
    // caused above it (window slide, spacer corrections, measurements), so
    if (!anchorEnd && anchorRef.current && windowElement) {
      const anchorElement = windowElement.querySelector(
        `[data-vw-key="${CSS.escape(anchorRef.current.key)}"]`,
      ) as HTMLElement | null;
      if (anchorElement) {
        const anchorRect = anchorElement.getBoundingClientRect();
        const contentOffset = anchorRect.top - viewportTop + viewport.scrollTop;
        const shift = contentOffset - anchorRef.current.contentOffset;
        if (Math.abs(shift) >= 1) {
          viewport.scrollTop += shift;
        }
      }
    }

    let nextHeights: Map<string, number> | null = null;
    if (windowElement && rowKey) {
      for (const child of windowElement.children) {
        const element = child as HTMLElement;
        const key = element.getAttribute("data-vw-key");
        const rowHeight = element.offsetHeight;
        if (!key || rowHeight <= 0) continue;
        if (rowHeights.get(key) !== rowHeight) {
          (nextHeights ??= new Map(rowHeights)).set(key, rowHeight);
        }
      }
    }
    if (nextHeights) {
      setRowHeights(nextHeights);
    }

    // viewport top. Rows keep stable keys, so the next pass can find this
    if (windowElement) {
      for (const child of windowElement.children) {
        const element = child as HTMLElement;
        const key = element.getAttribute("data-vw-key");
        if (!key) continue;
        const rect = element.getBoundingClientRect();
        if (rect.bottom > viewportTop) {
          anchorRef.current = {
            key,
            contentOffset: rect.top - viewportTop + viewport.scrollTop,
          };
          break;
        }
      }
    }

    // 4) Coverage: extend the window end, monotonically, until the rendered
    // rows reach the content-box bottom of the viewport. Rows can never cover
    // the container's bottom padding, so the target is not the padding edge.
    if (!anchorTail && endIndex < itemCount) {
      const paddingBottom = parseFloat(getComputedStyle(viewport).paddingBottom) || 0;
      const viewportBottom = viewport.scrollTop + viewport.clientHeight - paddingBottom;
      const deficit = viewportBottom - (offsets[endIndex] ?? totalHeight);
      if (deficit > 0) {
        const coveredEnd = Math.min(itemCount, indexAt(offsets, viewportBottom) + 1);
        const nextExtension = coveredEnd - endIndex + extension;
        if (nextExtension > extension) setExtension(nextExtension);
      }
    }
  }, [
    measured, viewport, windowElement, rowKey, itemCount, anchorEnd, anchorTail,
    endIndex, scrollTop, viewportHeight, contentVersion, extension,
    estimateHeight, totalHeight, rowHeights, offsets, buildOffsets,
  ]);
  /* eslint-enable react-hooks/set-state-in-effect */

  if (!measured || itemCount === 0) {
    return { startIndex: 0, endIndex: itemCount, spacerBefore: 0, spacerAfter: 0, virtualized: false };
  }
  return {
    startIndex,
    endIndex,
    spacerBefore: offsets[startIndex]!,
    spacerAfter: totalHeight - offsets[endIndex]!,
    virtualized: true,
  };
}
