export type TimelineItemType = "tool_use" | "tool_result" | "reasoning" | "text" | "error" | "lifecycle" | "steering" | "harness";

export interface TimelineItem {
  id?: string;
  seq: number;
  type: TimelineItemType;
  tool?: string;
  content?: string;
  input?: Record<string, unknown>;
  output?: string;
  status?: string;
  incremental?: boolean;
  created_at?: string;
}

export type TranscriptSortDirection = "chronological" | "newest_first";

export type EventColor = "agent" | "reasoning" | "tool" | "result" | "error";
