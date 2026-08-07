import { useEffect, useState } from "react";

/**
 * Virtualized row window (#202). The Runtime Owner Workspace keeps older
 * history pages in state, so rendered DOM rows must stay bounded while pages
 * accumulate. useVirtualWindow windows the list around the scroll position
 * using a uniform per-row height estimate and two spacer elements.
 *
 * The caller passes the scroll container element as a value (`viewport`),
 * typically by storing the element in state through a ref callback on the
 * container. The container may mount after the hook (the workspace renders
 * only once the owner loads, and the Timeline view mounts on demand), so the
 * scroll listener attaches whenever the element actually appears.
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

export function useVirtualWindow(options: {
  itemCount: number;
  /** The scroll container element, passed as a state value. */
  viewport: HTMLElement | null;
  estimateHeight: number;
}): VirtualWindow {
  const { itemCount, viewport, estimateHeight } = options;
  const [scrollTop, setScrollTop] = useState(0);
  const [viewportHeight, setViewportHeight] = useState(0);
  const [measured, setMeasured] = useState(false);

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

  if (!measured || itemCount === 0) {
    return { startIndex: 0, endIndex: itemCount, spacerBefore: 0, spacerAfter: 0, virtualized: false };
  }
  const height = viewportHeight > 0 ? viewportHeight : estimateHeight * 10;
  const startIndex = Math.max(0, Math.floor(scrollTop / estimateHeight) - OVERSCAN);
  const endIndex = Math.min(itemCount, Math.ceil((scrollTop + height) / estimateHeight) + OVERSCAN);
  return {
    startIndex,
    endIndex,
    spacerBefore: startIndex * estimateHeight,
    spacerAfter: (itemCount - endIndex) * estimateHeight,
    virtualized: true,
  };
}
