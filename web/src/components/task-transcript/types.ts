export type TimelineItemType = "tool_use" | "tool_result" | "thinking" | "text" | "error";

export interface TimelineItem {
  seq: number;
  type: TimelineItemType;
  tool?: string;
  content?: string;
  input?: Record<string, unknown>;
  output?: string;
  created_at?: string;
}

export type TranscriptSortDirection = "chronological" | "newest_first";

export type EventColor = "agent" | "thinking" | "tool" | "result" | "error";