import { describe, expect, it } from "vitest";
import { getEventColor, getEventLabel, getEventSummary, itemFilterKey } from "./timeline-utils";
import type { TimelineItem } from "./types";

describe("timeline-utils", () => {
  it("maps item types to timeline color roles", () => {
    expect(getEventColor({ seq: 1, type: "reasoning" })).toBe("reasoning");
    expect(getEventColor({ seq: 2, type: "tool_use", tool: "Bash" })).toBe("tool");
    expect(getEventColor({ seq: 3, type: "text", content: "hi" })).toBe("agent");
  });

  it("summarizes bash commands from tool input", () => {
    const item: TimelineItem = {
      seq: 1,
      type: "tool_use",
      tool: "Bash",
      input: { command: "curl https://example.com" },
    };
    expect(getEventLabel(item)).toBe("Bash");
    expect(getEventSummary(item)).toBe("curl https://example.com");
  });

  it("builds stable filter keys for tool rows", () => {
    const item: TimelineItem = { seq: 1, type: "tool_use", tool: "Bash" };
    expect(itemFilterKey(item)).toBe("tool:Bash");
  });

  it("renders subagent activity with a label, state, and provider tag", () => {
    const item: TimelineItem = {
      seq: 1,
      type: "subagent_activity",
      tool: "codex",
      content: "security/recon",
      status: "started",
    };
    expect(getEventLabel(item)).toBe("Subagent");
    expect(getEventSummary(item)).toBe("security/recon");
    // Provider stays visible so operators can tell where the child ran.
    expect(itemFilterKey(item)).toBe("subagent:codex");
  });

  it("maps subagent activity to a distinct color role", () => {
    expect(getEventColor({ seq: 1, type: "subagent_activity", tool: "codex" })).toBe("subagent");
  });

});
