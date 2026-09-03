import { useCallback, useEffect, useLayoutEffect, useRef, useState } from "react";

/**
 * Virtualized row window (#202). The Runtime Owner Workspace keeps older
 * history pages in state, so rendered DOM rows must stay bounded while pages
 * accumulate. useVirtualWindow windows the list around the scroll position
 * and bounds the DOM with two spacer elements.
 *
 * Spacer sizes come from a prefix-offset table. Rows carried in the table by
 * stable key (`rowKey`, mirrored on the row element as `data-vw-key`) are
 * measured after every commit, so visited history uses real heights;
 * never-rendered rows fall back to the uniform estimate until first rendered.
 * Exact offsets are what keep scrolling stable: the spacers a sliding window
 * leaves behind match the rows that were really there, and when new
 * measurements change the layout above the viewport the hook shifts
 * `scrollTop` by the same delta so the reading position never moves.
 *
 * The window end also extends, monotonically, until the rendered rows cover
 * the viewport: collapsed rows sit far below the estimate, and an
 * estimate-derived window can otherwise end above the viewport bottom and
 * expose the tail spacer as a blank band once the operator leaves the live
 * tail. The extension never hands rows back — dropping a tall row would
 * re-uncover the viewport and ping-pong forever (React #185) — and is
 * bounded by the end of the list.
 *
 * When the viewport cannot be measured (clientHeight is 0, for example in
 * jsdom), every row renders so tests observe the full list; in real browsers
 * clientHeight is measurable and the DOM stays bounded.
 */

const OVERSCAN = 8;

export interface VirtualWindow {
  /** First rendered row index (inclusive). */
  startIndex: number;
  /** Last rendered row index (exclusive). */
  endIndex: number;
  /** Height in px of the spacer before the rendered window. */
  spacerBefore: number;
  /** Height in px of the spacer after the rendered window. */
  spacerAfter: number;
  /** True when the window is active (viewport measured). */
  virtualized: boolean;
}

/** Largest row index whose prefix offset is at or below the given pixel. */
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
  /** The scroll container element, passed as a state value. */
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
  // an entry, everything else uses the uniform estimate.
  const [rowHeights, setRowHeights] = useState<ReadonlyMap<string, number>>(() => new Map());
  // Extra rows rendered past the estimate-derived end so the rows actually
  // cover the viewport when they are shorter than the estimate.
  const [extension, setExtension] = useState(0);
  // The DOM reading anchor recorded on the previous pass: a stable row key
  // plus its content offset, used to cancel layout shifts above it.
  const anchorRef = useRef<{ key: string; contentOffset: number } | null>(null);
  // Change signal for re-measuring rows whose height changed without any
  // scroll (for example an expanded tool row).
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
  // owner starts again from the estimate-derived window.
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
  // A tall tail row (a long final output) can push the real bottom scrollTop
  // past the estimated total, so the derived start can reach the end of the
  // list; once it does, anchor the window to the final items instead.
  const anchorTail = anchorEnd || derivedStart + windowSize >= itemCount;
  const startIndex = anchorTail ? Math.max(0, itemCount - windowSize) : derivedStart;
  const baseEnd = Math.min(itemCount, indexAt(offsets, scrollTop + height) + 1 + OVERSCAN);
  const endIndex = anchorTail ? itemCount : Math.min(itemCount, baseEnd + extension);

  // Measurement, reading-position compensation, and coverage pass, after the
  // rows have committed and real layout exists. The state updates must live
  // in this effect: the measured layout only exists after commit, exactly
  // like the scroll-position sync above.
  /* eslint-disable react-hooks/set-state-in-effect */
  useLayoutEffect(() => {
    if (!measured || !viewport || itemCount === 0) return;
    const viewportTop = viewport.getBoundingClientRect().top;

    // 1) Keep the reading position with a DOM anchor: a row near the viewport
    // top recorded on the previous pass. The difference between its recorded
    // and current content offset is exactly the layout shift this commit
    // caused above it (window slide, spacer corrections, measurements), so
    // shifting the container by that difference holds the visible content
    // still. The offset table cannot be used here: in regions not yet
    // measured it disagrees with the real DOM layout. Auto-follow is skipped,
    // where the tail settlement owns the scroll position.
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

    // 2) Measure the rendered rows by their stable keys.
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

    // 3) Record the anchor for the next pass: the first row at or below the
    // viewport top. Rows keep stable keys, so the next pass can find this
    // same content again even after the window slides.
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
