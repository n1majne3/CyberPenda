import { afterEach, describe, expect, it, vi } from "vitest";
import { act, renderHook } from "@testing-library/react";
import { useDocumentVisibility } from "@/lib/useDocumentVisibility";

// jsdom defaults document.visibilityState to "visible". The Page Visibility API
// has no programmatic setter (the property is defined on the prototype with a
// getter only), so each test overrides it via defineProperty and dispatches the
// real event the hook listens to.
function setVisibility(value: DocumentVisibilityState) {
  Object.defineProperty(document, "visibilityState", { value, configurable: true });
  document.dispatchEvent(new Event("visibilitychange"));
}

describe("useDocumentVisibility", () => {
  afterEach(() => {
    vi.restoreAllMocks();
    Object.defineProperty(document, "visibilityState", { value: "visible", configurable: true });
  });

  it("is visible by default in jsdom", () => {
    const { result } = renderHook(() => useDocumentVisibility());
    expect(result.current).toBe(true);
  });

  it("tracks transitions between visible and hidden", () => {
    const { result } = renderHook(() => useDocumentVisibility());
    expect(result.current).toBe(true);

    act(() => setVisibility("hidden"));
    expect(result.current).toBe(false);

    act(() => setVisibility("visible"));
    expect(result.current).toBe(true);
  });

  it("stops listening on unmount", () => {
    const removeSpy = vi.spyOn(document, "removeEventListener");
    const { unmount } = renderHook(() => useDocumentVisibility());
    unmount();
    expect(removeSpy).toHaveBeenCalledWith("visibilitychange", expect.any(Function));
  });
});
