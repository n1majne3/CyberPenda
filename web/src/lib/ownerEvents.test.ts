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

  it("keeps distinct Timeline items that share one source Event seq", () => {
    const existing = [identifiedTimelineItem("event-1-text", 1)];
    const delta = [identifiedTimelineItem("event-2-thinking", 2), identifiedTimelineItem("event-2-tool", 2)];
    const merged = mergeTimelineItems(existing, delta);
    expect(merged.map((item) => item.id)).toEqual(["event-1-text", "event-2-thinking", "event-2-tool"]);
  });
});

describe("mergeTranscriptEntries", () => {
  it("returns the delta directly for an empty existing transcript", () => {
    const delta = [transcriptEntry("a", 1), transcriptEntry("b", 2)];
    expect(mergeTranscriptEntries([], delta)).toEqual(delta);
  });

  it("keeps the existing transcript on an empty poll", () => {
    const existing = [transcriptEntry("a", 1)];
    expect(mergeTranscriptEntries(existing, [])).toBe(existing);
  });

  it("appends new entries and deduplicates by stable id", () => {
    const existing = [transcriptEntry("a", 1)];
    const delta = [transcriptEntry("a", 1), transcriptEntry("b", 2)];
    const merged = mergeTranscriptEntries(existing, delta);
    expect(merged.map((entry) => entry.id)).toEqual(["a", "b"]);
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
