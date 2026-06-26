import { describe, expect, it } from "vitest";
import type { TaskEvent, TaskTranscriptEntry } from "@/lib/api";
import { collapsedTranscriptTitle, summarizeTaskEvent } from "./taskDetailView";

describe("summarizeTaskEvent", () => {
  it("summarizes lifecycle phases", () => {
    expect(
      summarizeTaskEvent({
        id: "1",
        task_id: "t",
        seq: 1,
        kind: "lifecycle",
        payload: { phase: "started", adapter: "claude_code" },
        created_at: "",
      }),
    ).toBe("Started · claude_code");
  });

  it("summarizes Claude tool_use runtime output", () => {
    const event: TaskEvent = {
      id: "2",
      task_id: "t",
      seq: 2,
      kind: "runtime_output",
      payload: {
        stream: "stdout",
        text: JSON.stringify({
          type: "assistant",
          message: {
            content: [{ type: "tool_use", id: "call_1", name: "Bash", input: { command: "curl example.com" } }],
          },
        }),
      },
      created_at: "",
    };
    expect(summarizeTaskEvent(event)).toBe("stdout · tool_use Bash");
  });

  it("summarizes Claude assistant text", () => {
    const event: TaskEvent = {
      id: "3",
      task_id: "t",
      seq: 3,
      kind: "runtime_output",
      payload: {
        stream: "stdout",
        text: JSON.stringify({
          type: "assistant",
          message: { content: [{ type: "text", text: "Found the flag!" }] },
        }),
      },
      created_at: "",
    };
    expect(summarizeTaskEvent(event)).toBe("stdout · assistant: Found the flag!");
  });

  it("summarizes Claude init and result as metadata", () => {
    expect(
      summarizeTaskEvent({
        id: "4",
        task_id: "t",
        seq: 4,
        kind: "runtime_output",
        payload: { stream: "stdout", text: JSON.stringify({ type: "system", subtype: "init" }) },
        created_at: "",
      }),
    ).toBe("stdout · system init");

    expect(
      summarizeTaskEvent({
        id: "5",
        task_id: "t",
        seq: 5,
        kind: "runtime_output",
        payload: { stream: "stdout", text: JSON.stringify({ type: "result", subtype: "success", is_error: false }) },
        created_at: "",
      }),
    ).toBe("stdout · run finished (success)");
  });
});

describe("collapsedTranscriptTitle", () => {
  it("includes tool names in collapsed titles", () => {
    const entry: TaskTranscriptEntry = {
      id: "x",
      seq: 1,
      continuation: 1,
      kind: "tool_call",
      role: "assistant",
      tool_name: "Bash",
      created_at: "",
    };
    expect(collapsedTranscriptTitle(entry)).toBe("Tool call · Bash");
  });

  it("includes tool name for tool results", () => {
    const entry: TaskTranscriptEntry = {
      id: "x",
      seq: 2,
      continuation: 1,
      kind: "tool_result",
      role: "tool",
      tool_name: "Bash",
      text: "ECONNREFUSED",
      created_at: "",
    };
    expect(collapsedTranscriptTitle(entry)).toBe("Tool result · Bash: ECONNREFUSED");
  });
});