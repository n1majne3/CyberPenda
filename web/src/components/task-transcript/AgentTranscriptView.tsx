import { useCallback, useEffect, useMemo, useRef, useState, type ReactNode, type RefObject } from "react";
import { Bot, Check, CheckCircle2, Clock, Copy, Filter, Loader2, XCircle } from "lucide-react";
import { cn } from "@/lib/utils";
import { Badge } from "@/components/ui";
import { formatCompactDateTime } from "@/lib/format";
import { useVirtualWindow } from "@/lib/virtualWindow";
import type { TimelineItem, TranscriptSortDirection } from "./types";
import { buildFilterOptions, formatDuration, formatElapsedMs, getEventLabel, getEventSummary, itemFilterKey } from "./timeline-utils";
import { UnseenTailPill, SortDirectionToggle, TimelineBar } from "./TimelineControls";
import { TranscriptEventRow, MetadataChip } from "./TimelineEventRow";

const TIMELINE_ROW_ESTIMATE = 48;
const TAIL_THRESHOLD = 48;

interface AgentTranscriptViewProps {
  owner: RuntimeTimelineOwner;
  items: TimelineItem[];
  profileName?: string;
  isLive?: boolean;
  /** Forwarded scroll container so the page can shift the reading position. */
  scrollRef?: RefObject<HTMLDivElement | null>;
  onAtTailChange?: (atTail: boolean) => void;
  onSortDirectionChange?: (direction: TranscriptSortDirection) => void;
  unseenCount?: number;
  onShowLatest?: () => void;
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
  // measures it so the rows always cover the viewport (#blank band).
  const [rowsElement, setRowsElement] = useState<HTMLDivElement | null>(null);
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

  // state so the scroll listener attaches once the container mounts.
  const displayWindow = useVirtualWindow({
    itemCount: displayItems.length,
    viewport: scrollContainer,
    estimateHeight: TIMELINE_ROW_ESTIMATE,
    windowElement: rowsElement,
    rowKey: (index) => {
      const item = displayItems[index];
      return item === undefined ? "" : item.id ?? `seq-${item.seq}`;
    },
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
      className="flex h-full min-h-0 flex-col overflow-y-auto overscroll-contain rounded-lg border border-border bg-card [overflow-anchor:none]"
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
            <div ref={setRowsElement} data-testid="timeline-rows" className="divide-y">
              {visibleItems.map((item, visibleIndex) => (
                <TranscriptEventRow
                  key={`${item.id ?? `${item.seq}-${visibleIndex}`}-${item.type === "reasoning" ? item.status ?? "" : ""}`}
                  vwKey={item.id ?? `seq-${item.seq}`}
                  ref={(el) => {
                    if (el) eventRefs.current.set(item.seq, el);
                    else eventRefs.current.delete(item.seq);
                  }}
                  item={item}
                  isSelected={selectedSeq === item.seq}
                />
              ))}
            </div>
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

function prefersReducedMotion(): boolean {
  return window.matchMedia?.("(prefers-reduced-motion: reduce)").matches ?? false;
}
