import { act, renderHook } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";
import type { RuntimeOwnerAdapter } from "@/lib/runtimeOwner/adapter";
import { useOwnerHistoryState, useRuntimeOwnerHistory } from "./useRuntimeOwnerHistory";

function deferred<T>() {
  let resolve!: (value: T) => void;
  const promise = new Promise<T>((r) => { resolve = r; });
  return { promise, resolve };
}

function renderHistory(
  loadWorkspace: (signal: AbortSignal) => Promise<unknown>,
  setError = vi.fn(),
) {
  const ownerAdapter = { loadWorkspace } as unknown as RuntimeOwnerAdapter;
  const rendered = renderHook(() => {
    const { historyRef, commitHistory } = useOwnerHistoryState();
    return useRuntimeOwnerHistory({
      ownerID: "task-1",
      isSession: false,
      projectId: "project-1",
      activeView: "conversation",
      ownerAdapter,
      autoFollowRef: { current: true },
      historyRef,
      commitHistory,
      setOwner: vi.fn(),
      setFinishReadiness: vi.fn(),
      setTitleDraft: vi.fn(),
      setError,
      setActionError: vi.fn(),
    });
  });
  return { ...rendered, setError };
}

describe("useRuntimeOwnerHistory", () => {
  it("does not abort a slow load when the next poll starts", async () => {
    const pending = deferred<unknown>();
    const signals: AbortSignal[] = [];
    const loadWorkspace = vi.fn((signal: AbortSignal) => {
      signals.push(signal);
      return pending.promise;
    });
    const { result } = renderHistory(loadWorkspace);

    act(() => { void result.current.loadAll("poll"); });
    act(() => { void result.current.loadAll("poll"); });
    act(() => { void result.current.loadAll("poll"); });

    expect(loadWorkspace).toHaveBeenCalledTimes(1);
    expect(signals[0].aborted).toBe(false);

    await act(async () => {
      pending.resolve({
        owner: { kind: "task", id: "task-1", status: "running" },
        timeline: [],
        transcript: [],
      });
      await pending.promise;
    });
    act(() => { void result.current.loadAll("poll"); });
    expect(loadWorkspace).toHaveBeenCalledTimes(2);
  });

  it("starts an initial load even while a poll is in flight", () => {
    const signals: AbortSignal[] = [];
    const loadWorkspace = vi.fn((signal: AbortSignal) => {
      signals.push(signal);
      return new Promise(() => {});
    });
    const { result } = renderHistory(loadWorkspace);

    act(() => { void result.current.loadAll("poll"); });
    act(() => { void result.current.loadAll("initial"); });

    expect(loadWorkspace).toHaveBeenCalledTimes(2);
    expect(signals[0].aborted).toBe(true);
  });

  it("records a failed poll as pollError without setting the full-page load error", async () => {
    const loadWorkspace = vi.fn()
      .mockRejectedValueOnce(new Error("daemon temporarily unavailable"))
      .mockResolvedValueOnce({
        owner: { kind: "task", id: "task-1", status: "running" },
        timeline: [],
        transcript: [],
      });
    const { result, setError } = renderHistory(loadWorkspace);

    await act(async () => { await result.current.loadAll("poll"); });

    expect(setError).not.toHaveBeenCalled();
    expect(result.current.pollError).toBe("daemon temporarily unavailable");

    await act(async () => { await result.current.loadAll("poll"); });

    expect(result.current.pollError).toBeNull();
    expect(setError).toHaveBeenLastCalledWith(null);
  });

  it("sets the full-page load error when an initial load fails and clears stale pollError", async () => {
    const loadWorkspace = vi.fn()
      .mockRejectedValueOnce(new Error("poll hiccup"))
      .mockRejectedValueOnce(new Error("task not found"));
    const { result, setError } = renderHistory(loadWorkspace);

    await act(async () => { await result.current.loadAll("poll"); });
    expect(result.current.pollError).toBe("poll hiccup");

    await act(async () => { await result.current.loadAll("initial"); });
    expect(setError).toHaveBeenCalledWith("task not found");
    expect(result.current.pollError).toBeNull();
  });
});
