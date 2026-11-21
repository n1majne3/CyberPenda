import { useMemo, useState } from "react";
import { AlertCircle, Brain, ChevronRight } from "lucide-react";
import { cn } from "@/lib/utils";
import { formatClockTime, formatDateTime } from "@/lib/format";
import type { TimelineItem } from "./types";
import { colorClasses, getEventColor, getEventLabel, getEventSummary, stripAnsi } from "./timeline-utils";

export function MetadataChip({ icon, children }: { icon?: React.ReactNode; children: React.ReactNode }) {
  return (
    <span className="inline-flex items-center gap-1 rounded-md border bg-muted/50 px-2 py-0.5 text-[11px] text-muted-foreground">
      {icon}
      {children}
    </span>
  );
}

export function TranscriptEventRow({
  ref,
  vwKey,
  item,
  isSelected,
}: {
  ref?: React.Ref<HTMLDivElement>;
  vwKey: string;
  item: TimelineItem;
  isSelected: boolean;
}) {
  const [expanded, setExpanded] = useState(item.type === "reasoning" && item.status === "streaming");
  const color = getEventColor(item);
  const label = getEventLabel(item);
  const summary = getEventSummary(item);
  const date = useMemo(() => (item.created_at ? new Date(item.created_at) : null), [item.created_at]);

  const hasDetail =
    (item.type === "tool_use" && item.input && Object.keys(item.input).length > 0) ||
    (item.type === "tool_result" && item.output && item.output.length > 0) ||
    (item.type === "reasoning" && item.content && item.content.length > 0) ||
    (item.type === "text" && item.content && item.content.length > 0) ||
    (item.type === "harness" && item.content && item.content.length > 0) ||
    (item.type === "error" && item.content && item.content.length > 0);

  return (
    <div
      ref={ref}
      data-vw-key={vwKey}
      data-testid="transcript-event-row"
      className={cn(
        "group transition-colors",
        isSelected && "bg-accent/50",
      )}
    >
      <div className="flex items-start gap-2 px-4 py-2">
        <span
          className={cn(
            "mt-0.5 inline-flex min-w-[60px] shrink-0 items-center justify-center rounded px-1.5 py-0.5 text-[11px] font-medium",
            colorClasses[color].label,
          )}
        >
          {item.type === "reasoning" && <Brain className="mr-1 h-3 w-3 shrink-0" />}
          {item.type === "error" && <AlertCircle className="mr-1 h-3 w-3 shrink-0" />}
          {label}
        </span>

        <button
          type="button"
          disabled={!hasDetail}
          onClick={() => hasDetail && setExpanded((open) => !open)}
          className={cn(
            "min-w-0 flex-1 rounded py-0.5 text-left text-xs transition-colors focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring/50",
            hasDetail ? "cursor-pointer hover:text-foreground" : "cursor-default",
            item.type === "error" ? "text-destructive" : "text-muted-foreground",
          )}
        >
          <div className="flex items-start gap-1.5">
            {hasDetail && (
              <ChevronRight
                className={cn(
                  "mt-0.5 h-3 w-3 shrink-0 text-muted-foreground/50 transition-transform",
                  expanded && "rotate-90",
                )}
              />
            )}
            <span className="truncate">{summary || "(empty)"}</span>
          </div>
        </button>

        <span className="mt-1 shrink-0 text-[10px] tabular-nums text-muted-foreground/50">#{item.seq}</span>

        {date && (
          <span
            className="mt-1 shrink-0 text-[10px] tabular-nums text-muted-foreground/50"
            title={formatDateTime(date)}
          >
            {formatClockTime(date)}
          </span>
        )}
      </div>

      {hasDetail && expanded && (
        <div data-testid="timeline-event-detail" className="px-4 pb-3">
          <div className="ml-[72px] border-l-2 border-border/60">
            <EventDetailContent item={item} />
          </div>
        </div>
      )}
    </div>
  );
}

export function EventDetailContent({ item }: { item: TimelineItem }) {
  switch (item.type) {
    case "tool_use":
      return (
        <pre className="max-h-60 overflow-auto whitespace-pre-wrap break-all p-3 text-[11px] text-muted-foreground">
          {item.input ? JSON.stringify(item.input, null, 2) : ""}
        </pre>
      );
    case "tool_result": {
      const output = item.output
        ? item.output.length > 4000
          ? item.output.slice(0, 4000) + "\n… (truncated)"
          : item.output
        : "";
      return (
        <pre className="max-h-60 overflow-auto whitespace-pre-wrap break-all p-3 text-[11px] text-muted-foreground">
          {output}
        </pre>
      );
    }
    case "reasoning":
      return (
        <pre className="max-h-60 overflow-auto whitespace-pre-wrap break-words p-3 text-[11px] text-muted-foreground">
          {stripAnsi(item.content ?? "")}
        </pre>
      );
    case "text":
    case "harness":
      return (
        <pre className="max-h-60 overflow-auto whitespace-pre-wrap break-words p-3 text-[11px] text-muted-foreground">
          {item.content ?? ""}
        </pre>
      );
    case "error":
      return (
        <pre className="max-h-60 overflow-auto whitespace-pre-wrap break-words p-3 text-[11px] text-destructive">
          {item.content ?? ""}
        </pre>
      );
    default:
      return null;
  }
}
