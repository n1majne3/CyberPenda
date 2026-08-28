import { describe, expect, it } from "vitest";
import { mergeTimelineItems, mergeTranscriptEntries, prependTimelineItems, prependTranscriptEntries } from "./ownerEvents";
import type { TaskTimelineItem, TaskTranscriptEntry } from "./api";

function timelineItem(seq: number): TaskTimelineItem {
  return { seq, type: "text", content: `item-${seq}` };
}

function identifiedTimelineItem(id: string, seq: number): TaskTimelineItem {
  return { id, seq, type: "text", content: id };
}

function transcriptEntry(id: string, seq: number): TaskTranscriptEntry {
  return { id, seq, continuation: 1, kind: "message", role: "assistant", text: id };
}

describe("mergeTimelineItems", () => {
  it("returns the delta directly for an empty existing projection", () => {
    const delta = [timelineItem(1), timelineItem(2)];
    expect(mergeTimelineItems([], delta)).toEqual(delta);
  });

  it("keeps the existing projection on an empty poll", () => {
    const existing = [timelineItem(1), timelineItem(2)];
    expect(mergeTimelineItems(existing, [])).toBe(existing);
  });

  it("appends new items in server order and deduplicates by seq", () => {
    const existing = [timelineItem(1), timelineItem(2)];
    const delta = [timelineItem(2), timelineItem(3), timelineItem(3)];
    const merged = mergeTimelineItems(existing, delta);
    expect(merged.map((item) => item.seq)).toEqual([1, 2, 3]);
  });

  it("does not reorder existing items when a stale delta overlaps", () => {
    const existing = [timelineItem(2), timelineItem(3)];
    const delta = [timelineItem(1), timelineItem(2)];
    const merged = mergeTimelineItems(existing, delta);
    expect(merged.map((item) => item.seq)).toEqual([2, 3]);
  });

  it("updates a growing reasoning item with the same stable id in place", () => {
    const existing: TaskTimelineItem[] = [
      { id: "reasoning-1", seq: 2, type: "reasoning", content: "checking", created_at: "2026-01-01T00:00:00Z" },
      identifiedTimelineItem("tool-1", 3),
    ];
    const delta: TaskTimelineItem[] = [
      { id: "reasoning-1", seq: 4, type: "reasoning", content: "checking the auth flow", created_at: "2026-01-01T00:00:01Z" },
    ];
    const merged = mergeTimelineItems(existing, delta);
    expect(merged).toHaveLength(2);
    expect(merged[0]).toMatchObject({ id: "reasoning-1", seq: 4, content: "checking the auth flow", created_at: "2026-01-01T00:00:00Z" });
    expect(merged[1]?.id).toBe("tool-1");
  });

  it("keeps distinct Timeline items that share one source Event seq", () => {
    const existing = [identifiedTimelineItem("event-1-text", 1)];
    const delta = [identifiedTimelineItem("event-2-thinking", 2), identifiedTimelineItem("event-2-tool", 2)];
    const merged = mergeTimelineItems(existing, delta);
    expect(merged.map((item) => item.id)).toEqual(["event-1-text", "event-2-thinking", "event-2-tool"]);
  });
});

describe("mergeTranscriptEntries", () => {
  it("returns the delta directly for an empty existing transcript", () => {
    const delta = [
      { ...transcriptEntry("a", 1), role: "user" as const, text: "hi" },
      transcriptEntry("b", 2),
    ];
    expect(mergeTranscriptEntries([], delta)).toEqual(delta);
  });

  it("keeps the existing transcript on an empty poll", () => {
    const existing = [transcriptEntry("a", 1)];
    expect(mergeTranscriptEntries(existing, [])).toBe(existing);
  });

  it("appends new entries and deduplicates by stable id", () => {
    const existing = [{ ...transcriptEntry("a", 1), role: "user" as const, text: "hi" }];
    const delta = [
      { ...transcriptEntry("a", 1), role: "user" as const, text: "hi" },
      transcriptEntry("b", 2),
    ];
    const merged = mergeTranscriptEntries(existing, delta);
    expect(merged.map((entry) => entry.id)).toEqual(["a", "b"]);
  });

  it("appends incremental reasoning text for the same stable id", () => {
    const existing: TaskTranscriptEntry[] = [
      { id: "reasoning-cli", seq: 2, continuation: 1, kind: "reasoning", role: "assistant", text: "checking ", status: "streaming", incremental: true, created_at: "2026-01-01T00:00:00Z" },
    ];
    const delta: TaskTranscriptEntry[] = [
      { id: "reasoning-cli", seq: 3, continuation: 1, kind: "reasoning", role: "assistant", text: "auth", status: "streaming", incremental: true, created_at: "2026-01-01T00:00:01Z" },
    ];
    expect(mergeTranscriptEntries(existing, delta)[0]).toMatchObject({ text: "checking auth", seq: 3 });
  });

  it("updates a growing reasoning entry with the same stable id in place", () => {
    const existing: TaskTranscriptEntry[] = [
      { id: "reasoning-1", seq: 2, continuation: 1, kind: "reasoning", role: "assistant", text: "checking", created_at: "2026-01-01T00:00:00Z" },
      transcriptEntry("tool-1", 3),
    ];
    const delta: TaskTranscriptEntry[] = [
      { id: "reasoning-1", seq: 4, continuation: 1, kind: "reasoning", role: "assistant", text: "checking the auth flow", created_at: "2026-01-01T00:00:01Z" },
    ];
    const merged = mergeTranscriptEntries(existing, delta);
    expect(merged).toHaveLength(2);
    expect(merged[0]).toMatchObject({ id: "reasoning-1", seq: 4, text: "checking the auth flow", created_at: "2026-01-01T00:00:00Z" });
    expect(merged[1]?.id).toBe("tool-1");
  });

  it("joins later Hermes ACP assistant chunks onto the previous sentence", () => {
    const existing = [{ ...transcriptEntry("ev-5-message", 5), text: "Hi", stream: "hermes_acp" }];
    const delta = [
      { ...transcriptEntry("ev-6-message", 6), text: "!", stream: "hermes_acp" },
      { ...transcriptEntry("ev-7-message", 7), text: " ", stream: "hermes_acp" },
      { ...transcriptEntry("ev-8-message", 8), text: "👋", stream: "hermes_acp" },
    ];
    const merged = mergeTranscriptEntries(existing, delta);
    expect(merged).toHaveLength(1);
    expect(merged[0]).toMatchObject({ id: "ev-5-message", seq: 8, text: "Hi! 👋" });
  });

  it("appends incremental reasoning content for the same Timeline id", () => {
    const existing: TaskTimelineItem[] = [
      { id: "reasoning-cli", seq: 2, type: "reasoning", content: "checking ", status: "streaming", incremental: true },
    ];
    const delta: TaskTimelineItem[] = [
      { id: "reasoning-cli", seq: 3, type: "reasoning", content: "auth", status: "streaming", incremental: true },
    ];
    expect(mergeTimelineItems(existing, delta)[0]).toMatchObject({ content: "checking auth", seq: 3 });
  });

  it("keeps complete adjacent assistant messages as separate rows", () => {
    const existing = [{ ...transcriptEntry("a", 1), text: "Message 1" }];
    const delta = [{ ...transcriptEntry("b", 2), text: "Message 2" }];
    const merged = mergeTranscriptEntries(existing, delta);
    expect(merged.map((entry) => entry.text)).toEqual(["Message 1", "Message 2"]);
  });
});

describe("prependTimelineItems", () => {
  it("places a strictly older backward page ahead of the loaded items", () => {
    const existing = [timelineItem(51), timelineItem(52)];
    const page = [timelineItem(1), timelineItem(2)];
    const merged = prependTimelineItems(existing, page);
    expect(merged.map((item) => item.seq)).toEqual([1, 2, 51, 52]);
  });

  it("keeps the loaded items when the page is empty", () => {
    const existing = [timelineItem(51)];
    expect(prependTimelineItems(existing, [])).toBe(existing);
  });

  it("drops stale overlap at the page boundary", () => {
    const existing = [timelineItem(51), timelineItem(52)];
    const page = [timelineItem(50), timelineItem(51)];
    const merged = prependTimelineItems(existing, page);
    expect(merged.map((item) => item.seq)).toEqual([50, 51, 52]);
  });
});

describe("prependTranscriptEntries", () => {
  it("places a strictly older backward page ahead of the loaded entries", () => {
    const existing = [transcriptEntry("c", 51), transcriptEntry("d", 52)];
    const page = [transcriptEntry("a", 1), transcriptEntry("b", 2)];
    const merged = prependTranscriptEntries(existing, page);
    expect(merged.map((entry) => entry.id)).toEqual(["a", "b", "c", "d"]);
  });

  it("keeps the loaded entries when the page is empty", () => {
    const existing = [transcriptEntry("a", 51)];
    expect(prependTranscriptEntries(existing, [])).toBe(existing);
  });

  it("deduplicates by stable id across the page boundary", () => {
    const existing = [transcriptEntry("a", 50), transcriptEntry("b", 51)];
    const page = [transcriptEntry("a", 50), transcriptEntry("z", 49)];
    const merged = prependTranscriptEntries(existing, page);
    expect(merged.map((entry) => entry.id)).toEqual(["z", "a", "b"]);
  });
});
