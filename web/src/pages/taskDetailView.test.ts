import { describe, expect, it } from "vitest";
import type { TaskTranscriptEntry } from "@/lib/api";
import { collapsedTranscriptTitle, toolCallFields } from "./taskDetailView";

describe("collapsedTranscriptTitle", () => {
  it("includes tool names and useful input previews in collapsed titles", () => {
    const entry: TaskTranscriptEntry = {
      id: "x",
      seq: 1,
      continuation: 1,
      kind: "tool_call",
      role: "assistant",
      tool_name: "Bash",
      details: { input: { command: "curl http://localhost:3000\necho done" } },
      created_at: "",
    };
    expect(collapsedTranscriptTitle(entry)).toBe("Bash · curl http://localhost:3000");
  });

  it("previews a Hermes tool_describe name argument", () => {
    const entry: TaskTranscriptEntry = {
      id: "x",
      seq: 1,
      continuation: 1,
      kind: "tool_call",
      role: "assistant",
      tool_name: "tool_describe",
      details: { input: { name: "mcp__pentest__blackboard_change" } },
      created_at: "",
    };
    expect(collapsedTranscriptTitle(entry)).toBe("tool_describe · mcp__pentest__blackboard_change");
  });

  it("summarizes tool results without exposing transport call ids", () => {
    const entry: TaskTranscriptEntry = {
      id: "x",
      seq: 2,
      continuation: 1,
      kind: "tool_result",
      role: "tool",
      tool_call_id: "call-1",
      text: "ECONNREFUSED",
      created_at: "",
    };
    expect(collapsedTranscriptTitle(entry)).toBe("Result · ECONNREFUSED");
  });
});

describe("toolCallFields", () => {
  it("parses tool input into labeled fields instead of a raw JSON envelope", () => {
    const entry: TaskTranscriptEntry = {
      id: "x",
      seq: 1,
      continuation: 1,
      kind: "tool_call",
      role: "assistant",
      tool_name: "Bash",
      details: { input: { command: "curl http://localhost:3000", timeout: 30 } },
      created_at: "",
    };
    expect(toolCallFields(entry)).toEqual([
      { label: "Command", value: "curl http://localhost:3000", block: true },
      { label: "Timeout", value: "30", block: false },
    ]);
  });

  it("humanizes snake_case and camelCase argument keys", () => {
    const entry: TaskTranscriptEntry = {
      id: "x",
      seq: 1,
      continuation: 1,
      kind: "tool_call",
      role: "assistant",
      details: { input: { file_path: "/etc/hosts", maxResults: 5 } },
      created_at: "",
    };
    expect(toolCallFields(entry).map((field) => field.label)).toEqual(["File Path", "Max Results"]);
  });

  it("pretty-prints nested objects as expandable blocks", () => {
    const entry: TaskTranscriptEntry = {
      id: "x",
      seq: 1,
      continuation: 1,
      kind: "tool_call",
      role: "assistant",
      details: { input: { headers: { Accept: "application/json" } } },
      created_at: "",
    };
    const [field] = toolCallFields(entry);
    expect(field?.label).toBe("Headers");
    expect(field?.block).toBe(true);
    expect(field?.value).toContain('"Accept": "application/json"');
  });

  it("skips empty values and returns nothing when there is no input", () => {
    const entry: TaskTranscriptEntry = {
      id: "x",
      seq: 1,
      continuation: 1,
      kind: "tool_call",
      role: "assistant",
      details: { input: { command: "", note: null } },
      created_at: "",
    };
    expect(toolCallFields(entry)).toEqual([]);
    expect(toolCallFields({ ...entry, details: {} })).toEqual([]);
  });
});
