import { act, renderHook } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";
import type { RuntimeOwnerAdapter } from "@/lib/runtimeOwner/adapter";
import { useOwnerHistoryState, useRuntimeOwnerHistory } from "./useRuntimeOwnerHistory";

function deferred<T>() {
  let resolve!: (value: T) => void;
  const promise = new Promise<T>((r) => { resolve = r; });
  return { promise, resolve };
}

function renderHistory(loadWorkspace: (signal: AbortSignal) => Promise<unknown>) {
  const ownerAdapter = { loadWorkspace } as unknown as RuntimeOwnerAdapter;
  return renderHook(() => {
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
      setError: vi.fn(),
      setActionError: vi.fn(),
    });
  });
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
});
