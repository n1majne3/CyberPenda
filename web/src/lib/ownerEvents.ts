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

/** mergeTimelineItems appends new items and updates growing stable items in place. */
export function mergeTimelineItems(existing: TaskTimelineItem[], delta: TaskTimelineItem[]): TaskTimelineItem[] {
  if (existing.length === 0) return delta;
  if (delta.length === 0) return existing;
  const maxSeq = Math.max(...existing.map((item) => item.seq));
  const updated = [...existing];
  const existingByIdentity = new Map(updated.map((item, index) => [timelineItemIdentity(item), index]));
  const appended: TaskTimelineItem[] = [];
  const appendedByIdentity = new Map<string, number>();
  let changed = false;

  for (const item of delta) {
    const identity = timelineItemIdentity(item);
    const existingIndex = existingByIdentity.get(identity);
    if (existingIndex !== undefined) {
      if (item.seq > updated[existingIndex]!.seq) {
        // Keep the item at its first visible position while replacing its
        // cumulative reasoning text and latest Seq.
        const previous = updated[existingIndex]!;
        updated[existingIndex] = {
          ...item,
          content: item.incremental ? `${previous.content ?? ""}${item.content ?? ""}` : item.content,
          created_at: previous.created_at ?? item.created_at,
        };
        changed = true;
      }
      continue;
    }
    const appendedIndex = appendedByIdentity.get(identity);
    if (appendedIndex !== undefined) {
      if (item.seq > appended[appendedIndex]!.seq) appended[appendedIndex] = item;
      continue;
    }
    if (item.seq <= maxSeq) continue;
    appendedByIdentity.set(identity, appended.length);
    appended.push(item);
  }
  if (!changed && appended.length === 0) return existing;
  return [...updated, ...appended];
}

/** mergeTranscriptEntries appends new rows and updates growing stable rows in place. */
export function mergeTranscriptEntries(existing: TaskTranscriptEntry[], delta: TaskTranscriptEntry[]): TaskTranscriptEntry[] {
  if (existing.length === 0) return coalesceAssistantChunks(delta);
  if (delta.length === 0) return existing;
  const maxSeq = Math.max(...existing.map((entry) => entry.seq));
  const updated = [...existing];
  const existingByID = new Map(updated.map((entry, index) => [entry.id, index]));
  const appended: TaskTranscriptEntry[] = [];
  const appendedByID = new Map<string, number>();
  let changed = false;

  for (const entry of delta) {
    const existingIndex = existingByID.get(entry.id);
    if (existingIndex !== undefined) {
      if (entry.seq > updated[existingIndex]!.seq) {
        // A cumulative reasoning batch or completed provider item replaces the
        // same stable row without changing its visible list position.
        const previous = updated[existingIndex]!;
        updated[existingIndex] = {
          ...entry,
          continuation: previous.continuation,
          text: entry.incremental ? `${previous.text ?? ""}${entry.text ?? ""}` : entry.text,
          created_at: previous.created_at ?? entry.created_at,
        };
        changed = true;
      }
      continue;
    }
    const appendedIndex = appendedByID.get(entry.id);
    if (appendedIndex !== undefined) {
      if (entry.seq > appended[appendedIndex]!.seq) appended[appendedIndex] = entry;
      continue;
    }
    if (entry.seq <= maxSeq) continue;
    appendedByID.set(entry.id, appended.length);
    appended.push(entry);
  }
  if (!changed && appended.length === 0) return existing;
  return appendCoalescedTranscript(updated, appended);
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
