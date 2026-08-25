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
    // (100*72=7200). The estimated-ratio window must not detach from the tail.
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
});
