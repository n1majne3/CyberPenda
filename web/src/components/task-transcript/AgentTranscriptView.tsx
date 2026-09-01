import { useCallback, useEffect, useMemo, useRef, useState, type ReactNode, type RefObject } from "react";
import {
  AlertCircle,
  ArrowDown,
  ArrowDownNarrowWide,
  ArrowUpNarrowWide,
  Bot,
  Check,
  CheckCircle2,
  ChevronRight,
  Clock,
  Copy,
  Filter,
  Loader2,
  OctagonPause,
  User,
  XCircle,
} from "lucide-react";
import { cn } from "@/lib/utils";
import { Badge } from "@/components/ui";
import { formatClockTime, formatCompactDateTime, formatDateTime } from "@/lib/format";
import { useVirtualWindow } from "@/lib/virtualWindow";
import type { TimelineItem, TranscriptSortDirection } from "./types";
import {
  buildFilterOptions,
  colorClasses,
  formatDuration,
  formatElapsedMs,
  getEventColor,
  getEventLabel,
  getEventSummary,
  itemFilterKey,
} from "./timeline-utils";

// Uniform row-height estimate matching the contain-intrinsic-size hint on the
// rendered rows; the virtualized window uses it to bound DOM size while older
// pages accumulate (#202).
const TIMELINE_ROW_ESTIMATE = 48;
// Distance from the tail (in px) below which the operator is "at the tail".
const TAIL_THRESHOLD = 48;

interface AgentTranscriptViewProps {
  owner: RuntimeTimelineOwner;
  items: TimelineItem[];
  profileName?: string;
  isLive?: boolean;
  /** Forwarded scroll container so the page can shift the reading position. */
  scrollRef?: RefObject<HTMLDivElement | null>;
  /** Reports whether the operator is at the live tail (sort-aware). */
  onAtTailChange?: (atTail: boolean) => void;
  /** Reports the internal sort direction for anchor-preserving paging. */
  onSortDirectionChange?: (direction: TranscriptSortDirection) => void;
  /** Unseen live-event count shown as a jump-to-tail pill when away. */
  unseenCount?: number;
  /** Jumps to the live tail and dismisses the unseen pill. */
  onShowLatest?: () => void;
  /** Rendered at the older end of the list (paging affordance). */
  footer?: ReactNode;
}

export interface RuntimeTimelineOwner {
  status: string;
  runner: string;
  createdAt: string;
  updatedAt: string;
}

export function AgentTranscriptView({ owner, items, profileName, isLive = false, scrollRef, onAtTailChange, onSortDirectionChange, unseenCount = 0, onShowLatest, footer }: AgentTranscriptViewProps) {
  const [selectedSeq, setSelectedSeq] = useState<number | null>(null);
  const [elapsed, setElapsed] = useState("");
  const [copied, setCopied] = useState(false);
  const [selectedTools, setSelectedTools] = useState<Set<string>>(new Set());
  const [sortDirection, setSortDirection] = useState<TranscriptSortDirection>("newest_first");
  const [filterOpen, setFilterOpen] = useState(false);
  const [scrollContainer, setScrollContainer] = useState<HTMLDivElement | null>(null);
  const eventRefs = useRef<Map<number, HTMLDivElement>>(new Map());
  const filterRef = useRef<HTMLDivElement>(null);
  const atTailRef = useRef(true);
  const sortRef = useRef<TranscriptSortDirection>("newest_first");
  const onAtTailChangeRef = useRef(onAtTailChange);
  // Mirror the latest callback so effect closures never capture a stale one.
  useEffect(() => {
    onAtTailChangeRef.current = onAtTailChange;
  });

  const filterOptions = useMemo(() => buildFilterOptions(items), [items]);

  const filteredItems = useMemo(() => {
    if (selectedTools.size === 0) return items;
    return items.filter((item) => selectedTools.has(itemFilterKey(item)));
  }, [items, selectedTools]);

  const displayItems = useMemo(
    () => (sortDirection === "newest_first" ? [...filteredItems].reverse() : filteredItems),
    [filteredItems, sortDirection],
  );

  // Pair each tool call with the immediately following result for the same
  // tool so the pair renders as one compressed tool row (Direction A). The
  // source order is chronological, so the result always trails its call.
  const toolPairs = useMemo(() => {
    const resultForCall = new Map<TimelineItem, TimelineItem>();
    const absorbedResults = new Set<TimelineItem>();
    for (let index = 0; index + 1 < filteredItems.length; index += 1) {
      const item = filteredItems[index]!;
      if (item.type !== "tool_use") continue;
      const next = filteredItems[index + 1]!;
      if (next.type === "tool_result" && (!next.tool || !item.tool || next.tool === item.tool)) {
        resultForCall.set(item, next);
        absorbedResults.add(next);
      }
    }
    return { resultForCall, absorbedResults };
  }, [filteredItems]);

  // Timeline lane per displayed row; a lane change starts a new turn-group
  // marker on the rail.
  const lanes = useMemo(() => displayItems.map(turnLane), [displayItems]);

  // Virtualized rendering window: DOM size stays bounded while loaded older
  // pages accumulate in state (#202). The container element is stored in
  // state so the scroll listener attaches once the container mounts.
  const displayWindow = useVirtualWindow({
    itemCount: displayItems.length,
    viewport: scrollContainer,
    estimateHeight: TIMELINE_ROW_ESTIMATE,
  });
  const visibleItems = displayWindow.virtualized
    ? displayItems.slice(displayWindow.startIndex, displayWindow.endIndex)
    : displayItems;

  // The live tail is the newest end of the list: the top in the default
  // newest-first sort, the bottom in chronological sort. Report whether the
  // operator is there so the page can preserve the reading position on live
  // deltas instead of forcing scroll.
  const computeAtTail = useCallback((): boolean => {
    if (!scrollContainer) return true;
    if (sortRef.current === "newest_first") {
      return scrollContainer.scrollTop <= TAIL_THRESHOLD;
    }
    return scrollContainer.scrollHeight - scrollContainer.scrollTop - scrollContainer.clientHeight <= TAIL_THRESHOLD;
  }, [scrollContainer]);

  useEffect(() => {
    if (!scrollContainer) return;
    const update = () => {
      const tail = computeAtTail();
      if (tail !== atTailRef.current) {
        atTailRef.current = tail;
        onAtTailChangeRef.current?.(tail);
      }
    };
    update();
    scrollContainer.addEventListener("scroll", update, { passive: true });
    return () => scrollContainer.removeEventListener("scroll", update);
  }, [computeAtTail, scrollContainer]);

  // Content changes can move the tail (appended rows at the tail end), so
  // recompute the reported state whenever the displayed list changes.
  useEffect(() => {
    const tail = computeAtTail();
    if (tail !== atTailRef.current) {
      atTailRef.current = tail;
      onAtTailChangeRef.current?.(tail);
    }
  }, [computeAtTail, displayItems.length]);

  // Forward the internal scroll container to the page for anchor shifts.
  useEffect(() => {
    if (scrollRef) {
      scrollRef.current = scrollContainer;
    }
  }, [scrollRef, scrollContainer]);

  useEffect(() => {
    if (!isLive) return;
    const startRef = owner.createdAt;
    const update = () => setElapsed(formatElapsedMs(Date.now() - new Date(startRef).getTime()));
    update();
    const interval = setInterval(update, 1000);
    return () => clearInterval(interval);
  }, [isLive, owner.createdAt]);

  useEffect(() => {
    if (!filterOpen) return;
    function handleClick(event: MouseEvent) {
      if (filterRef.current && !filterRef.current.contains(event.target as Node)) {
        setFilterOpen(false);
      }
    }
    document.addEventListener("mousedown", handleClick);
    return () => document.removeEventListener("mousedown", handleClick);
  }, [filterOpen]);

  const handleSortDirectionChange = useCallback(
    (dir: TranscriptSortDirection) => {
      if (dir === sortDirection) return;
      setSortDirection(dir);
      sortRef.current = dir;
      onSortDirectionChange?.(dir);
      scrollContainer?.scrollTo({ top: 0 });
    },
    [sortDirection, onSortDirectionChange, scrollContainer],
  );

  const handleSegmentClick = useCallback((seq: number) => {
    setSelectedSeq(seq);
    const row = eventRefs.current.get(seq);
    if (row) {
      row.scrollIntoView({
        behavior: prefersReducedMotion() ? "auto" : "smooth",
        block: "center",
      });
      return;
    }
    // The target row is outside the rendered window: jump to its estimated
    // position so the virtualized window follows.
    const index = displayItems.findIndex((item) => item.seq === seq);
    if (scrollContainer && index >= 0) {
      scrollContainer.scrollTo({
        top: Math.max(0, index * TIMELINE_ROW_ESTIMATE - (scrollContainer.clientHeight || 0) / 2),
        behavior: prefersReducedMotion() ? "auto" : "smooth",
      });
    }
  }, [displayItems, scrollContainer]);

  const handleCopyAll = useCallback(() => {
    const text = displayItems
      .map((item) => `[${getEventLabel(item)}] ${getEventSummary(item)}`)
      .join("\n");
    void navigator.clipboard.writeText(text).then(() => {
      setCopied(true);
      setTimeout(() => setCopied(false), 2000);
    });
  }, [displayItems]);

  const toggleTool = useCallback((tool: string) => {
    setSelectedTools((prev) => {
      const next = new Set(prev);
      if (next.has(tool)) next.delete(tool);
      else next.add(tool);
      return next;
    });
  }, []);

  const clearFilters = useCallback(() => setSelectedTools(new Set()), []);

  const duration =
    owner.updatedAt && !isLive && owner.status !== "running"
      ? formatDuration(owner.createdAt, owner.updatedAt)
      : isLive
        ? elapsed
        : null;

  const toolCount = items.filter((item) => item.type === "tool_use").length;

  const statusBadge = isLive ? (
    <Badge size="sm" variant="info">
      <Loader2 className="h-3 w-3 animate-spin motion-reduce:animate-none" />
      Running
    </Badge>
  ) : owner.status === "completed" ? (
    <Badge size="sm" variant="success">
      <CheckCircle2 className="h-3 w-3" />
      Completed
    </Badge>
  ) : owner.status === "failed" ? (
    <Badge size="sm" variant="destructive">
      <XCircle className="h-3 w-3" />
      Failed
    </Badge>
  ) : (
    <Badge size="sm" variant="outline" className="capitalize">
      {owner.status}
    </Badge>
  );

  return (
    <div
      ref={setScrollContainer}
      data-testid="timeline-workspace"
      className="flex h-full min-h-0 flex-col overflow-y-auto overscroll-contain rounded-lg border border-border bg-card"
    >
      <div className="shrink-0 space-y-2 border-b px-4 py-3">
        <div className="flex flex-col gap-2 sm:flex-row sm:items-center sm:gap-3">
          <div className="flex items-center gap-2">
            <div className="flex h-6 w-6 items-center justify-center rounded-full bg-info/10 text-info">
              <Bot className="h-3.5 w-3.5" />
            </div>
            <span className="text-sm font-medium">{profileName ?? "Agent"}</span>
          </div>
          {statusBadge}
          <div className="flex flex-wrap items-center gap-1 sm:ml-auto">
            {items.length > 1 && (
              <SortDirectionToggle value={sortDirection} onChange={handleSortDirectionChange} />
            )}
            {filterOptions.length > 0 && (
              <div className="relative" ref={filterRef}>
                <button
                  type="button"
                  aria-expanded={filterOpen}
                  onClick={() => setFilterOpen((open) => !open)}
                  className={cn(
                    "flex items-center gap-1 rounded px-2 py-1 text-xs transition-colors focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring/50",
                    selectedTools.size > 0
                      ? "bg-info/10 text-info hover:bg-info/15"
                      : "text-muted-foreground hover:bg-accent hover:text-foreground",
                  )}
                >
                  <Filter className="h-3 w-3" />
                  Filter
                  {selectedTools.size > 0 && (
                    <span className="ml-0.5 rounded-full bg-info/15 px-1.5 py-0 text-[10px] font-medium">
                      {selectedTools.size}
                    </span>
                  )}
                </button>
                {filterOpen && (
                  <div className="absolute right-0 z-20 mt-1 min-w-[10rem] rounded-md border bg-popover p-1 text-xs shadow-md">
                    {filterOptions.map(([value, label]) => (
                      <label key={value} className="flex cursor-pointer items-center gap-2 rounded px-2 py-1.5 hover:bg-accent">
                        <input
                          type="checkbox"
                          aria-label={label.replace(/^tool:/i, "")}
                          checked={selectedTools.has(value)}
                          onChange={() => toggleTool(value)}
                          className="rounded border-input"
                        />
                        {label}
                      </label>
                    ))}
                    {selectedTools.size > 0 && (
                      <button
                        type="button"
                        onClick={clearFilters}
                        className="mt-1 w-full rounded px-2 py-1.5 text-left text-muted-foreground hover:bg-accent focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring/50"
                      >
                        Clear filters
                      </button>
                    )}
                  </div>
                )}
              </div>
            )}
            <button
              type="button"
              onClick={handleCopyAll}
              className="flex items-center gap-1 rounded px-2 py-1 text-xs text-muted-foreground transition-colors hover:bg-accent hover:text-foreground focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring/50"
            >
              {copied ? <Check className="h-3 w-3" /> : <Copy className="h-3 w-3" />}
              {copied ? "Copied" : selectedTools.size > 0 ? "Copy filtered" : "Copy all"}
            </button>
          </div>
        </div>

        <div className="flex flex-wrap items-center gap-2 text-xs">
          <MetadataChip>runner: {owner.runner}</MetadataChip>
          {profileName && <MetadataChip>{profileName}</MetadataChip>}
          {duration && (
            <MetadataChip icon={<Clock className="h-3 w-3" />}>
              {duration}
            </MetadataChip>
          )}
          {toolCount > 0 && <MetadataChip>{toolCount} tool calls</MetadataChip>}
          <MetadataChip>
            {selectedTools.size > 0
              ? `${filteredItems.length} / ${items.length} events`
              : `${items.length} events`}
          </MetadataChip>
          {owner.createdAt && (
            <MetadataChip>
              {formatCompactDateTime(owner.createdAt)}
            </MetadataChip>
          )}
        </div>
      </div>

      {displayItems.length > 0 && (
        <div className="shrink-0 border-b px-4 py-2.5">
          <TimelineBar items={displayItems} selectedSeq={selectedSeq} onSegmentClick={handleSegmentClick} />
        </div>
      )}

      <div className="min-h-0 flex-1">
        {unseenCount > 0 && sortDirection === "newest_first" && (
          <UnseenTailPill count={unseenCount} onShowLatest={onShowLatest} className="top-2" />
        )}
        {displayItems.length === 0 ? (
          <div className="flex h-full min-h-[12rem] items-center justify-center text-sm text-muted-foreground">
            {isLive ? (
              <div className="flex items-center gap-2">
                <Loader2 className="h-4 w-4 animate-spin" />
                Waiting for events…
              </div>
            ) : (
              "No timeline data"
            )}
          </div>
        ) : (
          <div>
            {sortDirection === "chronological" && footer}
            {displayWindow.spacerBefore > 0 && (
              <div aria-hidden="true" style={{ height: displayWindow.spacerBefore }} />
            )}
            {visibleItems.map((item, visibleIndex) => {
              if (toolPairs.absorbedResults.has(item)) return null;
              const absoluteIndex = (displayWindow.virtualized ? displayWindow.startIndex : 0) + visibleIndex;
              const lane = lanes[absoluteIndex] ?? "runtime";
              return (
                <TranscriptEventRow
                  key={`${item.id ?? `${item.seq}-${visibleIndex}`}-${item.type === "reasoning" ? item.status ?? "" : ""}`}
                  ref={(el) => {
                    if (el) eventRefs.current.set(item.seq, el);
                    else eventRefs.current.delete(item.seq);
                  }}
                  item={item}
                  isSelected={selectedSeq === item.seq}
                  pairedResult={toolPairs.resultForCall.get(item)}
                  lane={lane}
                  laneStart={absoluteIndex === 0 || lanes[absoluteIndex - 1] !== lane}
                />
              );
            })}
            {displayWindow.spacerAfter > 0 && (
              <div aria-hidden="true" style={{ height: displayWindow.spacerAfter }} />
            )}
            {sortDirection !== "chronological" && footer}
          </div>
        )}
        {unseenCount > 0 && sortDirection === "chronological" && (
          <UnseenTailPill count={unseenCount} onShowLatest={onShowLatest} className="bottom-2" />
        )}
      </div>
    </div>
  );
}

function UnseenTailPill({
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

function SortDirectionToggle({
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

function MetadataChip({ icon, children }: { icon?: React.ReactNode; children: React.ReactNode }) {
  return (
    <span className="inline-flex items-center gap-1 rounded-md border bg-muted/50 px-2 py-0.5 text-[11px] text-muted-foreground">
      {icon}
      {children}
    </span>
  );
}

function TimelineBar({
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

type TurnLane = "user" | "runtime" | "system" | "divider";

/** Maps a timeline item onto its Direction A lane: operator input, runtime
 * work, system workflow markers, or a full-width lifecycle divider. */
function turnLane(item: TimelineItem): TurnLane {
  if (item.type === "lifecycle") return "divider";
  if (item.type === "steering") return "user";
  if (item.type === "harness") return "system";
  return "runtime";
}

/** Timeline rail grouping one runtime turn: a circular lane marker on the
 * left rail ties the turn's rows into a single visual unit. */
function TurnGroup({
  children,
  marker,
  showMarker = true,
}: {
  children: ReactNode;
  marker: Exclude<TurnLane, "divider">;
  showMarker?: boolean;
}) {
  return (
    <div className="relative pl-6">
      {showMarker && (
        <span
          className={cn(
            "absolute left-0 top-1 flex h-4 w-4 items-center justify-center rounded-full border",
            marker === "user" && "border-border bg-card",
            marker === "runtime" && "border-signal/40 bg-signal/10 text-signal",
            marker === "system" && "border-border bg-muted",
          )}
        >
          {marker === "user" && <User className="h-2.5 w-2.5" />}
          {marker === "runtime" && <Bot className="h-2.5 w-2.5" />}
          {marker === "system" && <OctagonPause className="h-2.5 w-2.5" />}
        </span>
      )}
      <div className="border-l border-border pl-4">{children}</div>
    </div>
  );
}

/** Lifecycle events degrade to centered dividers instead of posing as rows. */
function SystemEventDivider({ item }: { item: TimelineItem }) {
  return (
    <div className="flex items-center gap-3 py-1">
      <span className="h-px flex-1 bg-border" />
      <span className="flex items-center gap-1.5 text-xs text-muted-foreground">
        <OctagonPause className="h-3 w-3" />
        {getEventSummary(item) || getEventLabel(item)}
        <span className="text-muted-foreground/50">· #{item.seq}</span>
      </span>
      <span className="h-px flex-1 bg-border" />
    </div>
  );
}

/** One compressed row per tool call: status icon, tool name, one-line command
 * summary, optional failure status, and call→result duration. The call input
 * and the paired result output stay folded behind the row until expanded. */
function ToolCallRow({
  call,
  result,
  expanded,
  onToggle,
}: {
  call?: TimelineItem;
  result?: TimelineItem;
  expanded: boolean;
  onToggle: () => void;
}) {
  const item = call ?? result;
  if (!item) return null;
  const hasInput = Boolean(call?.input && Object.keys(call.input).length > 0);
  const hasOutput = Boolean(result?.output && result.output.length > 0);
  const hasDetail = hasInput || hasOutput;
  const failed = Boolean(result?.status && result.status !== "completed" && result.status !== "success");
  const duration =
    call?.created_at && result?.created_at ? formatDuration(call.created_at, result.created_at) : null;
  return (
    <div className="py-0.5">
      <button
        type="button"
        disabled={!hasDetail}
        onClick={onToggle}
        className={cn(
          "flex w-full items-center gap-2 rounded-md px-2 py-1.5 text-left transition-colors focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring/50",
          hasDetail ? "hover:bg-muted/50" : "cursor-default",
        )}
      >
        {failed ? (
          <XCircle className="h-3.5 w-3.5 flex-none text-destructive" />
        ) : result ? (
          <CheckCircle2 className="h-3.5 w-3.5 flex-none text-success" />
        ) : (
          <Clock className="h-3.5 w-3.5 flex-none text-muted-foreground" />
        )}
        <span className="flex-none font-mono text-xs text-muted-foreground">
          {item.tool ?? (call ? "Tool" : "Result")}
        </span>
        <span className="min-w-0 flex-1 truncate font-mono text-xs">{getEventSummary(item) || "(empty)"}</span>
        {failed && (
          <span className="flex-none rounded-sm bg-destructive/10 px-1 text-[10px] text-destructive">
            {result?.status}
          </span>
        )}
        {duration && <span className="flex-none text-xs text-muted-foreground">{duration}</span>}
        {hasDetail && (
          <ChevronRight
            className={cn("h-3.5 w-3.5 flex-none text-muted-foreground transition-transform", expanded && "rotate-90")}
          />
        )}
      </button>
      {expanded && hasDetail && (
        <div
          data-testid="timeline-event-detail"
          className="ml-5 rounded-md border border-border bg-muted/40 font-mono text-xs leading-relaxed text-muted-foreground"
        >
          {hasInput && call && <EventDetailContent item={call} />}
          {hasOutput && result && <EventDetailContent item={result} />}
        </div>
      )}
    </div>
  );
}

/** Reasoning entries fold behind a single italic summary line by default. */
function ReasoningRow({
  item,
  expanded,
  onToggle,
}: {
  item: TimelineItem;
  expanded: boolean;
  onToggle: () => void;
}) {
  const hasDetail = Boolean(item.content && item.content.length > 0);
  const summary = getEventSummary(item);
  return (
    <div className="py-1.5">
      <button
        type="button"
        disabled={!hasDetail}
        onClick={onToggle}
        className={cn(
          "flex w-full items-center gap-1.5 rounded text-left text-xs italic text-muted-foreground transition-colors focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring/50",
          hasDetail ? "hover:text-foreground" : "cursor-default",
        )}
      >
        {hasDetail && (
          <ChevronRight className={cn("h-3.5 w-3.5 flex-none transition-transform", expanded && "rotate-90")} />
        )}
        <span className="min-w-0 flex-1 truncate">
          {getEventLabel(item)} · {summary || "(empty)"}
        </span>
      </button>
      {expanded && hasDetail && (
        <div data-testid="timeline-event-detail" className="mt-1 pl-5 text-xs text-muted-foreground">
          <EventDetailContent item={item} />
        </div>
      )}
    </div>
  );
}

/** Assistant messages drop the card chrome for a signal-colored left accent. */
function AssistantMessageRow({
  item,
  expanded,
  onToggle,
}: {
  item: TimelineItem;
  expanded: boolean;
  onToggle: () => void;
}) {
  const hasDetail = Boolean(item.content && item.content.length > 0);
  const summary = getEventSummary(item);
  return (
    <div className="py-1.5">
      <div className="border-l-2 border-signal/40 pl-4 text-sm leading-relaxed">
        <button
          type="button"
          disabled={!hasDetail}
          onClick={onToggle}
          className={cn(
            "w-full rounded text-left transition-colors focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring/50",
            hasDetail ? "cursor-pointer hover:text-foreground" : "cursor-default",
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
        {expanded && hasDetail && (
          <div data-testid="timeline-event-detail" className="mt-1">
            <EventDetailContent item={item} />
          </div>
        )}
      </div>
    </div>
  );
}

/** Rows the Direction A redesign leaves on the classic layout: errors,
 * Harness workflow markers, subagent activity, and steering directives. */
function GenericEventRow({
  item,
  expanded,
  onToggle,
  date,
}: {
  item: TimelineItem;
  expanded: boolean;
  onToggle: () => void;
  date: Date | null;
}) {
  const color = getEventColor(item);
  const label = getEventLabel(item);
  const summary = getEventSummary(item);
  const hasDetail = Boolean(
    (item.type === "harness" || item.type === "error") && item.content && item.content.length > 0,
  );

  return (
    <>
      <div className="flex items-start gap-2 py-2">
        <span
          className={cn(
            "mt-0.5 inline-flex min-w-[60px] shrink-0 items-center justify-center rounded px-1.5 py-0.5 text-[11px] font-medium",
            colorClasses[color].label,
          )}
        >
          {item.type === "error" && <AlertCircle className="mr-1 h-3 w-3 shrink-0" />}
          {label}
        </span>

        <button
          type="button"
          disabled={!hasDetail}
          onClick={onToggle}
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
        <div data-testid="timeline-event-detail" className="pb-3">
          <div className="ml-[72px] border-l-2 border-border/60">
            <EventDetailContent item={item} />
          </div>
        </div>
      )}
    </>
  );
}

function TranscriptEventRow({
  ref,
  item,
  isSelected,
  pairedResult,
  lane,
  laneStart,
}: {
  ref?: React.Ref<HTMLDivElement>;
  item: TimelineItem;
  isSelected: boolean;
  pairedResult?: TimelineItem;
  lane: TurnLane;
  laneStart: boolean;
}) {
  const [expanded, setExpanded] = useState(item.type === "reasoning" && item.status === "streaming");
  const toggle = useCallback(() => setExpanded((open) => !open), []);
  const date = useMemo(() => (item.created_at ? new Date(item.created_at) : null), [item.created_at]);

  return (
    <div
      ref={ref}
      data-testid="transcript-event-row"
      className={cn(
        "group px-4 [contain-intrinsic-size:48px] [content-visibility:auto] transition-colors",
        isSelected && "bg-accent/50",
      )}
    >
      {lane === "divider" ? (
        <SystemEventDivider item={item} />
      ) : (
        <TurnGroup marker={lane} showMarker={laneStart}>
          {item.type === "tool_use" || item.type === "tool_result" ? (
            <ToolCallRow
              call={item.type === "tool_use" ? item : undefined}
              result={item.type === "tool_use" ? pairedResult : item}
              expanded={expanded}
              onToggle={toggle}
            />
          ) : item.type === "reasoning" ? (
            <ReasoningRow item={item} expanded={expanded} onToggle={toggle} />
          ) : item.type === "text" ? (
            <AssistantMessageRow item={item} expanded={expanded} onToggle={toggle} />
          ) : (
            <GenericEventRow item={item} expanded={expanded} onToggle={toggle} date={date} />
          )}
        </TurnGroup>
      )}
    </div>
  );
}

function EventDetailContent({ item }: { item: TimelineItem }) {
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

function prefersReducedMotion(): boolean {
  return window.matchMedia?.("(prefers-reduced-motion: reduce)").matches ?? false;
}
