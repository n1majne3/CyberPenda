import { describe, expect, it } from "vitest";
import type { TaskEvent, TaskTranscriptEntry } from "@/lib/api";
import { collapsedTranscriptTitle, shouldShowInTimeline, summarizeTaskEvent } from "./taskDetailView";

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

  it("hides tool-only runtime output from the timeline", () => {
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
    expect(shouldShowInTimeline(event)).toBe(false);
  });

  it("summarizes mixed assistant text without tool labels in the timeline", () => {
    const event: TaskEvent = {
      id: "2b",
      task_id: "t",
      seq: 2,
      kind: "runtime_output",
      payload: {
        stream: "stdout",
        text: JSON.stringify({
          type: "assistant",
          message: {
            content: [
              { type: "text", text: "Checking the login page." },
              { type: "tool_use", id: "call_1", name: "Bash", input: { command: "curl example.com" } },
            ],
          },
        }),
      },
      created_at: "",
    };
    expect(shouldShowInTimeline(event)).toBe(true);
    expect(summarizeTaskEvent(event)).toBe("stdout · assistant: Checking the login page.");
  });

  it("hides task_started and task_failed runtime output from the timeline", () => {
    for (const [label, subtype] of [["task_started", "task_started"], ["task_failed", "task_failed"]] as const) {
      const event: TaskEvent = {
        id: label,
        task_id: "t",
        seq: 4,
        kind: "runtime_output",
        payload: {
          stream: "stdout",
          text: JSON.stringify({
            type: "system",
            subtype,
            task_id: "bbr05bd75",
            summary: "Explore FTP directory",
            status: subtype === "task_failed" ? "failed" : undefined,
          }),
        },
        created_at: "",
      };
      expect(shouldShowInTimeline(event), label).toBe(false);
    }
  });

  it("hides task_progress runtime output from the timeline", () => {
    const event: TaskEvent = {
      id: "2d",
      task_id: "t",
      seq: 4,
      kind: "runtime_output",
      payload: {
        stream: "stdout",
        text: JSON.stringify({
          type: "system",
          subtype: "task_progress",
          description: "Exploit: privacy-policy",
          workflow_progress: [{ label: "privacy-policy" }],
        }),
      },
      created_at: "",
    };
    expect(shouldShowInTimeline(event)).toBe(false);
  });

  it("hides tool_result runtime output from the timeline", () => {
    const event: TaskEvent = {
      id: "2c",
      task_id: "t",
      seq: 3,
      kind: "runtime_output",
      payload: {
        stream: "stdout",
        text: JSON.stringify({
          type: "user",
          message: {
            content: [{ type: "tool_result", tool_use_id: "call_1", content: "200 OK" }],
          },
        }),
      },
      created_at: "",
    };
    expect(shouldShowInTimeline(event)).toBe(false);
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

  it("hides Claude init and task_progress from the timeline", () => {
    expect(
      shouldShowInTimeline({
        id: "4",
        task_id: "t",
        seq: 4,
        kind: "runtime_output",
        payload: { stream: "stdout", text: JSON.stringify({ type: "system", subtype: "init" }) },
        created_at: "",
      }),
    ).toBe(false);

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