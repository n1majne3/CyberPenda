import { describe, expect, it } from "vitest";
import { getEventColor, getEventLabel, getEventSummary, itemFilterKey } from "./timeline-utils";
import type { TimelineItem } from "./types";

describe("timeline-utils", () => {
  it("maps item types to multica colors", () => {
    expect(getEventColor({ seq: 1, type: "thinking" })).toBe("thinking");
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
});