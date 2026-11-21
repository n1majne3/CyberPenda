import { ArrowDown, ArrowDownNarrowWide, ArrowUpNarrowWide } from "lucide-react";
import { cn } from "@/lib/utils";
import type { TimelineItem, TranscriptSortDirection } from "./types";
import { colorClasses, getEventColor, getEventLabel } from "./timeline-utils";

export function UnseenTailPill({
  count,
  onShowLatest,
  className,
}: {
  count: number;
  onShowLatest?: () => void;
  className: string;
}) {
  return (
    <button
      type="button"
      data-testid="unseen-timeline-indicator"
      onClick={onShowLatest}
      className={cn(
        "sticky z-10 mx-auto flex items-center gap-1.5 rounded-full border border-border bg-background/95 px-3 py-1 text-xs font-medium text-foreground shadow-md transition-colors hover:bg-accent focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring",
        className,
      )}
    >
      <ArrowDown className="h-3.5 w-3.5" />
      {count} new {count === 1 ? "event" : "events"}
    </button>
  );
}

export function SortDirectionToggle({
  value,
  onChange,
}: {
  value: TranscriptSortDirection;
  onChange: (dir: TranscriptSortDirection) => void;
}) {
  return (
    <div
      role="group"
      aria-label="Sort direction"
      className="inline-flex items-center rounded border bg-muted/40 p-0.5 text-xs"
    >
      <button
        type="button"
        aria-pressed={value === "chronological"}
        aria-label="Sort oldest first"
        title="Chronological"
        onClick={() => onChange("chronological")}
        className={cn(
          "flex h-8 items-center gap-1 rounded px-2 py-0 transition-colors focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring/50",
          value === "chronological"
            ? "bg-background text-foreground shadow-sm"
            : "text-muted-foreground hover:text-foreground",
        )}
      >
        <ArrowDownNarrowWide className="h-3.5 w-3.5" />
        <span className="hidden sm:inline">Oldest</span>
      </button>
      <button
        type="button"
        aria-pressed={value === "newest_first"}
        aria-label="Sort newest first"
        title="Newest first"
        onClick={() => onChange("newest_first")}
        className={cn(
          "flex h-8 items-center gap-1 rounded px-2 py-0 transition-colors focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring/50",
          value === "newest_first"
            ? "bg-background text-foreground shadow-sm"
            : "text-muted-foreground hover:text-foreground",
        )}
      >
        <ArrowUpNarrowWide className="h-3.5 w-3.5" />
        <span className="hidden sm:inline">Newest</span>
      </button>
    </div>
  );
}

export function TimelineBar({
  items,
  selectedSeq,
  onSegmentClick,
}: {
  items: TimelineItem[];
  selectedSeq: number | null;
  onSegmentClick: (seq: number) => void;
}) {
  const segments: { startIdx: number; endIdx: number; color: ReturnType<typeof getEventColor>; count: number }[] = [];
  let currentColor: ReturnType<typeof getEventColor> | null = null;
  let currentStart = 0;

  for (let i = 0; i < items.length; i++) {
    const item = items[i]!;
    const color = getEventColor(item);
    if (color !== currentColor) {
      if (currentColor !== null) {
        segments.push({ startIdx: currentStart, endIdx: i - 1, color: currentColor, count: i - currentStart });
      }
      currentColor = color;
      currentStart = i;
    }
  }
  if (currentColor !== null) {
    segments.push({ startIdx: currentStart, endIdx: items.length - 1, color: currentColor, count: items.length - currentStart });
  }

  return (
    <div className="flex h-5 gap-0.5 overflow-hidden rounded" role="navigation" aria-label="Timeline">
      {segments.map((seg) => {
        const isSelected =
          selectedSeq !== null && items.slice(seg.startIdx, seg.endIdx + 1).some((item) => item.seq === selectedSeq);
        const color = colorClasses[seg.color];
        const widthPercent = (seg.count / items.length) * 100;

        return (
          <button
            type="button"
            key={seg.startIdx}
            className={cn(
              "group relative h-full min-w-[4px] transition-[background-color,opacity] duration-150 hover:opacity-80 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring/50",
              isSelected ? color.bgActive : color.bg,
            )}
            style={{ width: `${Math.max(widthPercent, 0.5)}%` }}
            onClick={() => onSegmentClick(items[seg.startIdx]!.seq)}
            aria-label={`Jump to ${getEventLabel(items[seg.startIdx]!)} event${seg.count > 1 ? ` with ${seg.count} entries` : ""}`}
            title={`${getEventLabel(items[seg.startIdx]!)}${seg.count > 1 ? ` (+${seg.count - 1} more)` : ""}`}
          >
            <div className="pointer-events-none absolute bottom-full left-1/2 z-10 mb-1 hidden -translate-x-1/2 group-hover:block">
              <div className="whitespace-nowrap rounded border bg-popover px-2 py-1 text-[10px] text-popover-foreground shadow-md">
                {getEventLabel(items[seg.startIdx]!)}
                {seg.count > 1 && <span className="ml-1 text-muted-foreground">+{seg.count - 1}</span>}
              </div>
            </div>
          </button>
        );
      })}
    </div>
  );
}
