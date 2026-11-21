import { type TaskTranscriptEntry } from "@/lib/api";
import { anchorSubagentBlocks } from "@/lib/ownerEvents";
import { type RuntimeOwnerView } from "@/lib/runtimeOwner/types";

export function tabClass(active: boolean) {
  return [
    "inline-flex h-full items-center border-b-2 px-3 text-sm transition-colors focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring focus-visible:ring-offset-2 focus-visible:ring-offset-background",
    active ? "border-primary font-medium text-foreground" : "border-transparent text-muted-foreground hover:text-foreground",
  ].join(" ");
}

export function isNearScrollBottom(container: HTMLElement, threshold = 160) {
  return container.scrollHeight - (container.scrollTop + container.clientHeight) <= threshold;
}

export function runtimeOwnerBlackboardMode(owner: RuntimeOwnerView) {
  return owner.blackboardMode ?? owner.blackboardConclusion?.mode ?? "interactive";
}

export function liveReasoningEntryID(entries: TaskTranscriptEntry[], runtimeBusy: boolean): string | undefined {
  if (!runtimeBusy || entries.length === 0) return undefined;
  const maxSeq = Math.max(...entries.map((entry) => entry.seq));
  for (let index = entries.length - 1; index >= 0; index -= 1) {
    const entry = entries[index];
    if (entry?.kind === "reasoning" && entry.status === "streaming" && entry.seq === maxSeq) return entry.id;
  }
  return undefined;
}

export type TranscriptLane = "user" | "runtime" | "system" | "divider";

export type TranscriptDisplayRow = {
  entry: TaskTranscriptEntry;
  result?: TaskTranscriptEntry;
  lane: TranscriptLane;
  laneStart: boolean;
};

export function buildTranscriptRows(entries: TaskTranscriptEntry[]): TranscriptDisplayRow[] {
  entries = anchorSubagentBlocks(entries);
  const resultByCallID = new Map<string, TaskTranscriptEntry>();
  for (const entry of entries) {
    if (entry.kind === "tool_result" && entry.tool_call_id) resultByCallID.set(entry.tool_call_id, entry);
  }
  const absorbedResults = new Set<string>();
  const rows: TranscriptDisplayRow[] = [];
  for (const entry of entries) {
    if (absorbedResults.has(entry.id)) continue;
    const result = entry.kind === "tool_call" && entry.tool_call_id ? resultByCallID.get(entry.tool_call_id) : undefined;
    if (result) absorbedResults.add(result.id);
    const lane = transcriptLane(entry);
    rows.push({
      entry,
      result,
      lane,
      laneStart: lane === "divider" || rows.length === 0 || rows[rows.length - 1]!.lane !== lane,
    });
  }
  return rows;
}

export function transcriptLane(entry: TaskTranscriptEntry): TranscriptLane {
  if (entry.kind === "continuation") return "divider";
  if (entry.role === "user") return "user";
  if (entry.role === "system") return "system";
  return "runtime";
}

export function formatTranscriptDuration(start: string, end: string): string {
  const milliseconds = Date.parse(end) - Date.parse(start);
  if (!Number.isFinite(milliseconds) || milliseconds < 0) return "";
  if (milliseconds < 1000) return `${milliseconds}ms`;
  return `${(milliseconds / 1000).toFixed(milliseconds < 10_000 ? 1 : 0)}s`;
}

export function collapsedBody(entry: TaskTranscriptEntry) {
  if (entry.kind === "tool_result") return entry.text || "(empty)";

  const parts: string[] = [];
  if (entry.text) parts.push(entry.text);
  if (entry.tool_call_id) parts.push(`tool_call_id: ${entry.tool_call_id}`);
  if (entry.tool_name) parts.push(`tool_name: ${entry.tool_name}`);
  if (entry.details && Object.keys(entry.details).length > 0) {
    parts.push(JSON.stringify(entry.details, null, 2));
  }
  return parts.join("\n\n") || "(empty)";
}

export function prefersReducedMotion(): boolean {
  return window.matchMedia?.("(prefers-reduced-motion: reduce)").matches ?? false;
}
