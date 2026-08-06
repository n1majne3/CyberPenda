import { describe, expect, it } from "vitest";
import { mergeTimelineItems, mergeTranscriptEntries } from "./ownerEvents";
import type { TaskTimelineItem, TaskTranscriptEntry } from "./api";

function timelineItem(seq: number): TaskTimelineItem {
  return { seq, type: "text", content: `item-${seq}` };
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
