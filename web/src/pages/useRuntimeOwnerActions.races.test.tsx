import { act, renderHook } from "@testing-library/react";
import { useState } from "react";
import { describe, expect, it, vi } from "vitest";
import type { RuntimeOwnerAdapter } from "@/lib/runtimeOwner/adapter";
import type { RuntimeOwnerView } from "@/lib/runtimeOwner/types";
import { useRuntimeOwnerActions } from "./useRuntimeOwnerActions";

function deferred() {
  let resolve!: () => void;
  const promise = new Promise<void>((done) => { resolve = done; });
  return { promise, resolve };
}

function owner(id = "one"): RuntimeOwnerView {
  return {
    kind: "session", id, title: id, status: "running", lifecycle: "open", runner: "sandbox",
    runtimeProfileID: "pi", createdAt: "", updatedAt: "",
    capabilities: { projectChrome: false, rename: true, archive: true, restore: false,
      delete: false, finish: false, resumeWithoutMessage: false, attachments: true },
    runtimeControls: {
      native_resume_available: true, native_steer_available: true,
      native_steer_mode: "in_turn_steer", resume_available: false,
      queue_steer_available: true, interrupt_steer_available: true,
      native_session_captured: true, same_runtime_provider_only: true,
      turn_selection: { model_provider_id: "local" },
    },
  };
}

function harness(initialOwner = owner(), selectedProvider = "local") {
  const pending = deferred();
  const adapter = {
    kind: "session", base: "/api/sessions/one",
    steer: vi.fn(() => pending.promise), queueSteer: vi.fn(() => pending.promise),
    stop: vi.fn(async () => {}), resume: vi.fn(async () => {}),
    sendMessage: vi.fn(async () => initialOwner), finish: vi.fn(async () => {}),
  } as unknown as RuntimeOwnerAdapter;
  const loadAll = vi.fn(async () => {});
  const rendered = renderHook(({ currentOwner, currentAdapter }) => {
    const [steering, setSteering] = useState("first instruction");
    const [actionError, setActionError] = useState<string | null>(null);
    const [attachments, setAttachments] = useState<File[]>([]);
    const actions = useRuntimeOwnerActions({
      owner: currentOwner, ownerAdapter: currentAdapter, loadAll, setActionError,
      navigate: vi.fn(), projectId: undefined,
      continuationModelPayload: () => ({ model_provider_id: selectedProvider, reasoning_effort: "" }),
      steering, setSteering, continuationModelProvider: selectedProvider,
      attachments, setAttachments, forceReplaceNext: false,
      setForceReplaceNext: vi.fn(), currentRuntimeProvider: "pi", setPermissionBusy: vi.fn(),
      titleDraft: "", setEditingTitle: vi.fn(),
    });
    return { ...actions, steering, setSteering, actionError, attachments, setAttachments };
  }, { initialProps: { currentOwner: initialOwner, currentAdapter: adapter } });
  return { ...rendered, adapter, pending, loadAll };
}

describe("Runtime Owner conversation races", () => {
  it("accepts only one send before React has rendered the busy state", async () => {
    const { result, adapter, pending } = harness();
    let sends: Promise<void>[];
    act(() => { sends = [result.current.sendConversationMessage(), result.current.sendConversationMessage()]; });
    expect(adapter.steer).toHaveBeenCalledTimes(1);
    await act(async () => { pending.resolve(); await Promise.all(sends); });
  });

  it("does not erase the next instruction or new attachments when an earlier send completes", async () => {
    const { result, pending } = harness();
    let send!: Promise<void>;
    act(() => { send = result.current.sendConversationMessage(); });
    const nextAttachment = new File(["next"], "next.txt");
    act(() => {
      result.current.setSteering("second instruction");
      result.current.setAttachments([nextAttachment]);
    });
    await act(async () => { pending.resolve(); await send; });
    expect(result.current.steering).toBe("second instruction");
    expect(result.current.attachments).toEqual([nextAttachment]);
  });

  it("uses the same unavailable guard for Enter and the disabled send button", async () => {
    const archived = { ...owner(), lifecycle: "archived" as const };
    const { result, adapter, pending } = harness(archived);
    let send!: Promise<void>;
    act(() => { send = result.current.sendConversationMessage(); });
    await act(async () => { pending.resolve(); await send; });
    expect(adapter.steer).not.toHaveBeenCalled();
    expect(adapter.sendMessage).not.toHaveBeenCalled();
  });

  it("ignores an old owner's acknowledgement after navigating to another owner", async () => {
    const { result, adapter, pending, rerender, loadAll } = harness();
    let send!: Promise<void>;
    act(() => { send = result.current.sendConversationMessage(); });
    rerender({ currentOwner: owner("two"), currentAdapter: { ...adapter, base: "/api/sessions/two" } });
    act(() => { result.current.setSteering("instruction for owner two"); });
    await act(async () => { pending.resolve(); await send; });
    expect(result.current.steering).toBe("instruction for owner two");
    expect(loadAll).not.toHaveBeenCalled();
  });

  it("keeps Stop available during an in-flight send and ignores its late acknowledgement", async () => {
    const { result, adapter, pending, loadAll } = harness();
    let send!: Promise<void>;
    act(() => { send = result.current.sendConversationMessage(); });
    expect(result.current.sending).toBe(true);
    let stop!: Promise<void>;
    act(() => { stop = result.current.stop(); });
    expect(adapter.stop).toHaveBeenCalledTimes(1);
    act(() => { result.current.setSteering("new draft after Stop"); });
    await act(async () => { pending.resolve(); await Promise.all([send, stop]); });
    expect(adapter.stop).toHaveBeenCalledTimes(2);
    expect(result.current.steering).toBe("new draft after Stop");
    expect(result.current.sending).toBe(false);
    expect(loadAll).toHaveBeenCalledTimes(1);
  });

  it("does not restart a Task provider switch after the operator presses Stop", async () => {
    const task = { ...owner(), kind: "task" as const };
    const { result, adapter, pending } = harness(task, "other-provider");
    let send!: Promise<void>;
    act(() => { send = result.current.sendConversationMessage(); });
    expect(adapter.queueSteer).toHaveBeenCalledTimes(1);
    let stop!: Promise<void>;
    act(() => { stop = result.current.stop(); });
    await act(async () => { pending.resolve(); await Promise.all([send, stop]); });
    expect(adapter.stop).toHaveBeenCalledTimes(2);
    expect(adapter.resume).not.toHaveBeenCalled();
    expect(result.current.steering).toBe("first instruction");
  });

  it("does not send a Session provider-switch message after Stop cancels its restart", async () => {
    const { result, adapter, pending } = harness(owner(), "other-provider");
    vi.mocked(adapter.stop).mockImplementationOnce(() => pending.promise);
    let send!: Promise<void>;
    act(() => { send = result.current.sendConversationMessage(); });
    let stop!: Promise<void>;
    act(() => { stop = result.current.stop(); });
    await act(async () => { pending.resolve(); await Promise.all([send, stop]); });
    expect(adapter.stop).toHaveBeenCalledTimes(3);
    expect(adapter.sendMessage).not.toHaveBeenCalled();
    expect(result.current.steering).toBe("first instruction");
  });

  it("retains the failed message and attachments for an explicit retry", async () => {
    const { result, adapter } = harness();
    const attachment = new File(["context"], "context.txt");
    act(() => { result.current.setAttachments([attachment]); });
    vi.mocked(adapter.steer).mockRejectedValueOnce(new Error("Provider refused steering"));
    await act(async () => { await result.current.sendConversationMessage(); });
    expect(result.current.steering).toBe("first instruction");
    expect(result.current.attachments).toEqual([attachment]);
    expect(result.current.actionError).toBe("Provider refused steering");
    expect(result.current.sending).toBe(false);
    expect(result.current.conversationState.phase).toBe("failed");
  });

  it("does not finish Stop before an uploading Session switch can admit a new Runtime", async () => {
    const { result, adapter } = harness(owner(), "other-provider");
    const upload = deferred();
    let running = true;
    vi.mocked(adapter.stop).mockImplementation(async () => { running = false; });
    vi.mocked(adapter.sendMessage).mockImplementation(async () => {
      await upload.promise;
      running = true;
      return owner();
    });
    let send!: Promise<void>;
    await act(async () => { send = result.current.sendConversationMessage(); });
    expect(adapter.sendMessage).toHaveBeenCalledOnce();
    let stopped = false;
    let stop!: Promise<void>;
    await act(async () => {
      stop = result.current.stop().then(() => { stopped = true; });
    });
    expect(running).toBe(false);
    expect(stopped).toBe(false);
    expect(result.current.conversationState.phase).toBe("stopping");
    await act(async () => { upload.resolve(); await Promise.all([send, stop]); });
    expect(stopped).toBe(true);
    expect(running).toBe(false);
    expect(result.current.sending).toBe(false);
    expect(adapter.stop).toHaveBeenCalledTimes(3);
  });
});
