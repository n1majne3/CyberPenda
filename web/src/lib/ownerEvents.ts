// Shared Runtime Owner Workspace event kernel (#191). Task and Session views
// load the complete Timeline and Transcript once, then refresh only the
// events after the last committed cursor. These pure reducers keep both
// projections ordered and deduplicated across retry, reconnect, and daemon
// restart; the caller's monotonic generation guard rejects stale responses.
import type { TaskTimelineItem, TaskTranscriptEntry } from "./api";

/** CursorLoad is the delta a projection refresh returns. */
export interface ProjectionDelta<T> {
  /** New items strictly after the committed cursor (empty when idle). */
  items: T[];
  /** Maximum Seq of the full server projection; never regresses. */
  cursor: number;
}

/** mergeTimelineItems appends an ordered timeline delta without duplicates. */
export function mergeTimelineItems(existing: TaskTimelineItem[], delta: TaskTimelineItem[]): TaskTimelineItem[] {
  if (existing.length === 0) return delta;
  if (delta.length === 0) return existing;
  // A refresh only ever returns items strictly after the committed cursor.
  // Drop stale overlap (Seq <= existing max) and in-delta duplicates alike.
  const maxSeq = Math.max(...existing.map((item) => item.seq));
  const appended: TaskTimelineItem[] = [];
  const seen = new Set(existing.map(timelineItemIdentity));
  for (const item of delta) {
    const identity = timelineItemIdentity(item);
    if (item.seq <= maxSeq || seen.has(identity)) continue;
    seen.add(identity);
    appended.push(item);
  }
  if (appended.length === 0) return existing;
  return [...existing, ...appended];
}

/** mergeTranscriptEntries appends an ordered transcript delta without duplicates. */
export function mergeTranscriptEntries(existing: TaskTranscriptEntry[], delta: TaskTranscriptEntry[]): TaskTranscriptEntry[] {
  if (existing.length === 0) return coalesceAssistantChunks(delta);
  if (delta.length === 0) return existing;
  const maxSeq = Math.max(...existing.map((entry) => entry.seq));
  const appended: TaskTranscriptEntry[] = [];
  const seen = new Set<string>();
  for (const entry of delta) {
    if (entry.seq <= maxSeq || seen.has(entry.id)) continue;
    seen.add(entry.id);
    appended.push(entry);
  }
  if (appended.length === 0) return existing;
  return appendCoalescedTranscript(existing, appended);
}

function appendCoalescedTranscript(existing: TaskTranscriptEntry[], delta: TaskTranscriptEntry[]): TaskTranscriptEntry[] {
  const next = coalesceAssistantChunks(delta);
  if (next.length === 0) return existing;
  const last = existing[existing.length - 1]!;
  if (canMergeAssistantChunk(last, next[0]!)) {
    const joined: TaskTranscriptEntry = {
      ...last,
      text: `${last.text ?? ""}${next[0]!.text ?? ""}`,
      seq: next[0]!.seq,
      created_at: next[0]!.created_at ?? last.created_at,
    };
    return [...existing.slice(0, -1), joined, ...next.slice(1)];
  }
  return [...existing, ...next];
}

function coalesceAssistantChunks(entries: TaskTranscriptEntry[]): TaskTranscriptEntry[] {
  const out: TaskTranscriptEntry[] = [];
  for (const entry of entries) {
    const prev = out[out.length - 1];
    if (prev && canMergeAssistantChunk(prev, entry)) {
      out[out.length - 1] = {
        ...prev,
        text: `${prev.text ?? ""}${entry.text ?? ""}`,
        seq: entry.seq,
        created_at: entry.created_at ?? prev.created_at,
      };
      continue;
    }
    out.push(entry);
  }
  return out;
}

function canMergeAssistantChunk(prev: TaskTranscriptEntry, next: TaskTranscriptEntry): boolean {
  return (
    prev.kind === "message" &&
    next.kind === "message" &&
    prev.role === "assistant" &&
    next.role === "assistant" &&
    prev.continuation === next.continuation &&
    prev.stream === "hermes_acp" &&
    next.stream === "hermes_acp"
  );
}

/**
 * prependTimelineItems places a backward history page ahead of the loaded
 * items. The server guarantees the page is strictly older than the loaded
 * head, so no duplicate Seq can cross the boundary; the filter keeps the
 * merge safe against any stale overlap anyway.
 */
export function prependTimelineItems(existing: TaskTimelineItem[], page: TaskTimelineItem[]): TaskTimelineItem[] {
  if (page.length === 0) return existing;
  if (existing.length === 0) return page;
  const headSeq = existing[0]!.seq;
  const seen = new Set(existing.map(timelineItemIdentity));
  const older = page.filter((item) => item.seq < headSeq && !seen.has(timelineItemIdentity(item)));
  if (older.length === 0) return existing;
  return [...older, ...existing];
}

export function timelineItemIdentity(item: TaskTimelineItem): string {
  return item.id ?? `legacy-seq-${item.seq}`;
}

/**
 * prependTranscriptEntries places a backward history page ahead of the loaded
 * entries. Transcript entries can share one Seq (several rows projected from
 * one provider record), so deduplication is by stable ID; the server keeps
 * same-Seq groups atomic at page boundaries.
 */
export function prependTranscriptEntries(existing: TaskTranscriptEntry[], page: TaskTranscriptEntry[]): TaskTranscriptEntry[] {
  if (page.length === 0) return existing;
  if (existing.length === 0) return page;
  const headSeq = existing[0]!.seq;
  const older = page.filter((entry) => entry.seq < headSeq);
  if (older.length === 0) return existing;
  const seen = new Set(existing.map((entry) => entry.id));
  return [...older.filter((entry) => !seen.has(entry.id)), ...existing];
}
