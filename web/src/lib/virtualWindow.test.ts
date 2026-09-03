import { describe, expect, it } from "vitest";
import { act, renderHook } from "@testing-library/react";
import { useVirtualWindow } from "@/lib/virtualWindow";

// jsdom reports clientHeight 0 on real elements, so each test defines the
// measurable viewport the hook needs on a detached div and drives its
// scrollTop directly.
function fakeViewport(clientHeight: number) {
  const el = document.createElement("div");
  Object.defineProperty(el, "clientHeight", { value: clientHeight, configurable: true });
  return el;
}

function scrollViewportTo(el: HTMLDivElement, scrollTop: number) {
  el.scrollTop = scrollTop;
  el.dispatchEvent(new Event("scroll"));
}

function rect(top: number, bottom: number) {
  const height = bottom - top;
  return { top, bottom, height, width: 100, left: 0, right: 100, x: 0, y: top, toJSON: () => ({}) };
}

// Populates the rows wrapper with one row per list index whose measured
// height and viewport-relative rect follow the given content offsets, so the
// hook's measurement and DOM-anchor passes see deterministic layout in jsdom.
function attachRows(wrapper: HTMLElement, viewport: HTMLElement, offsets: number[], heights: number[]) {
  for (let i = 0; i < offsets.length; i += 1) {
    const row = document.createElement("div");
    const key = `k${i}`;
    row.setAttribute("data-vw-key", key);
    let height = heights[i] ?? 72;
    const extra = 0;
    Object.defineProperty(row, "offsetHeight", {
      get: () => height,
      configurable: true,
    });
    Object.defineProperty(row, "getBoundingClientRect", {
      value: () => {
        const top = offsets[i]! + extra - viewport.scrollTop;
        return rect(top, top + height);
      },
      configurable: true,
    });
    wrapper.appendChild(row);
    // Handle for tests to grow a row's measured height like real layout.
    (row as unknown as { vwHeight: (heightPx: number) => void }).vwHeight = (heightPx: number) => {
      height = heightPx;
    };
  }
}

describe("useVirtualWindow", () => {
  it("renders every row while the viewport cannot be measured", () => {
    const viewport = fakeViewport(0);
    const { result } = renderHook(() =>
      useVirtualWindow({ itemCount: 5, viewport, estimateHeight: 72 }),
    );
    expect(result.current).toEqual({ startIndex: 0, endIndex: 5, spacerBefore: 0, spacerAfter: 0, virtualized: false });
  });

  it("windows mid-list scroll positions around the scroll offset", () => {
    const viewport = fakeViewport(600);
    const { result } = renderHook(() =>
      useVirtualWindow({ itemCount: 100, viewport, estimateHeight: 72 }),
    );
    act(() => scrollViewportTo(viewport, 2880));
    expect(result.current.startIndex).toBe(32);
    expect(result.current.endIndex).toBe(57);
    expect(result.current.spacerBefore).toBe(32 * 72);
    expect(result.current.spacerAfter).toBe((100 - 57) * 72);
  });

  it("keeps the final rows rendered when tall tail rows push scrollTop beyond the estimated total", () => {
    const viewport = fakeViewport(600);
    const { result } = renderHook(() =>
      useVirtualWindow({ itemCount: 100, viewport, estimateHeight: 72 }),
    );
    // A long final transcript entry is far taller than the 72px estimate, so
    // the real bottom scrollTop exceeds the estimated total height
    // (100*72=7200). The window must not detach from the tail.
    act(() => scrollViewportTo(viewport, 10328));
    const window = result.current;
    expect(window.startIndex).toBeLessThan(100);
    expect(window.endIndex).toBe(100);
    expect(window.endIndex - window.startIndex).toBeGreaterThan(0);
  });

  it("anchors the window to the final items while anchorEnd is set", () => {
    const viewport = fakeViewport(600);
    const { result } = renderHook(() =>
      useVirtualWindow({ itemCount: 100, viewport, estimateHeight: 72, anchorEnd: true }),
    );
    expect(result.current.endIndex).toBe(100);
    expect(result.current.startIndex).toBeLessThan(100);
    expect(result.current.spacerAfter).toBe(0);
  });

  it("sizes spacers from measured row heights instead of the uniform estimate", () => {
    const viewport = fakeViewport(600);
    const rowsWrapper = document.createElement("div");
    attachRows(
      rowsWrapper,
      viewport,
      Array.from({ length: 100 }, (_, i) => i * 24),
      Array.from({ length: 100 }, () => 24),
    );
    const { result } = renderHook(() =>
      useVirtualWindow({
        itemCount: 100,
        viewport,
        estimateHeight: 72,
        windowElement: rowsWrapper,
        rowKey: (index) => `k${index}`,
      }),
    );
    act(() => scrollViewportTo(viewport, 1200));

    // offsets[42] = 42 * 24 measured, not the 42 * 72 estimate grid.
    expect(result.current.startIndex).toBe(42);
    expect(result.current.spacerBefore).toBe(42 * 24);
    expect(result.current.spacerAfter).toBe((100 - result.current.endIndex) * 24);
  });

  it("keeps the reading position when a layout shift above the viewport changes", () => {
    const viewport = fakeViewport(600);
    const rowsWrapper = document.createElement("div");
    const offsets = Array.from({ length: 100 }, (_, i) => i * 72);
    const heights = Array.from({ length: 100 }, () => 72);
    attachRows(rowsWrapper, viewport, offsets, heights);
    const { result } = renderHook(() =>
      useVirtualWindow({
        itemCount: 100,
        viewport,
        estimateHeight: 72,
        windowElement: rowsWrapper,
        rowKey: (index) => `k${index}`,
      }),
    );
    act(() => scrollViewportTo(viewport, 3672));
    expect(viewport.scrollTop).toBe(3672);

    // Row 45 sits above the viewport top (row 51 at scrollTop 3672). It grows
    // by 128px, so every row at or below it shifts down 128px in real layout.
    (rowsWrapper.children[45] as HTMLElement & { vwHeight: (h: number) => void }).vwHeight(200);
    for (let i = 45; i < 100; i += 1) {
      offsets[i] += 128;
    }
    act(() => scrollViewportTo(viewport, 3680));

    // The reading position is held: the container follows the +128px shift.
    expect(viewport.scrollTop).toBe(3680 + 128);
    expect(result.current.startIndex).toBeGreaterThan(0);
  });

  it("does not move the reading position when auto-follow owns the scroll", () => {
    const viewport = fakeViewport(600);
    const rowsWrapper = document.createElement("div");
    const offsets = Array.from({ length: 100 }, (_, i) => i * 72);
    const heights = Array.from({ length: 100 }, () => 72);
    attachRows(rowsWrapper, viewport, offsets, heights);
    const { result } = renderHook(() =>
      useVirtualWindow({
        itemCount: 100,
        viewport,
        estimateHeight: 72,
        anchorEnd: true,
        windowElement: rowsWrapper,
        rowKey: (index) => `k${index}`,
      }),
    );
    act(() => scrollViewportTo(viewport, 3600));

    (rowsWrapper.children[10] as HTMLElement & { vwHeight: (h: number) => void }).vwHeight(200);
    for (let i = 10; i < 100; i += 1) {
      offsets[i] += 128;
    }
    act(() => scrollViewportTo(viewport, 3620));

    expect(viewport.scrollTop).toBe(3620);
    expect(result.current.endIndex).toBe(100);
  });

  // A tall row near the window end makes the measured coverage flip between
  // "uncovered without it" and "over-covered with it". The extension must
  // converge instead of ping-ponging between the two states (React #185).
  it("does not oscillate when covering the viewport repeatedly swallows a tall row", () => {
    const viewport = fakeViewport(700);
    const rowsWrapper = document.createElement("div");
    const offsets: number[] = [];
    const heights = Array.from({ length: 200 }, (_, i) => (i === 120 ? 800 : 24));
    let acc = 0;
    for (let i = 0; i < 200; i += 1) {
      offsets.push(acc);
      acc += heights[i]!;
    }
    attachRows(rowsWrapper, viewport, offsets, heights);
    const { result } = renderHook(() =>
      useVirtualWindow({
        itemCount: 200,
        viewport,
        estimateHeight: 72,
        windowElement: rowsWrapper,
        rowKey: (index) => `k${index}`,
      }),
    );
    act(() => scrollViewportTo(viewport, 7056));
    const first = result.current.endIndex - result.current.startIndex;
    act(() => scrollViewportTo(viewport, 7200));
    const second = result.current.endIndex - result.current.startIndex;

    expect(first).toBeGreaterThan(0);
    expect(second).toBeGreaterThan(0);
    expect(second).toBeLessThan(200);
  });
});
