import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import {
  AlertCircle,
  ArrowDownNarrowWide,
  ArrowUpNarrowWide,
  Bot,
  Brain,
  Check,
  CheckCircle2,
  ChevronRight,
  Clock,
  Copy,
  Filter,
  Loader2,
  XCircle,
} from "lucide-react";
import { cn } from "@/lib/utils";
import type { Task } from "@/lib/api";
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

interface AgentTranscriptViewProps {
  task: Task;
  items: TimelineItem[];
  profileName?: string;
  isLive?: boolean;
}

export function AgentTranscriptView({ task, items, profileName, isLive = false }: AgentTranscriptViewProps) {
  const [selectedSeq, setSelectedSeq] = useState<number | null>(null);
  const [elapsed, setElapsed] = useState("");
  const [copied, setCopied] = useState(false);
  const [selectedTools, setSelectedTools] = useState<Set<string>>(new Set());
  const [sortDirection, setSortDirection] = useState<TranscriptSortDirection>("chronological");
  const [filterOpen, setFilterOpen] = useState(false);
  const eventRefs = useRef<Map<number, HTMLDivElement>>(new Map());
  const scrollContainerRef = useRef<HTMLDivElement>(null);
  const filterRef = useRef<HTMLDivElement>(null);

  const filterOptions = useMemo(() => buildFilterOptions(items), [items]);

  const filteredItems = useMemo(() => {
    if (selectedTools.size === 0) return items;
    return items.filter((item) => selectedTools.has(itemFilterKey(item)));
  }, [items, selectedTools]);

  const displayItems = useMemo(
    () => (sortDirection === "newest_first" ? [...filteredItems].reverse() : filteredItems),
    [filteredItems, sortDirection],
  );

  useEffect(() => {
    if (!isLive) return;
    const startRef = task.created_at;
    const update = () => setElapsed(formatElapsedMs(Date.now() - new Date(startRef).getTime()));
    update();
    const interval = setInterval(update, 1000);
    return () => clearInterval(interval);
  }, [isLive, task.created_at]);

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
      scrollContainerRef.current?.scrollTo({ top: 0 });
    },
    [sortDirection],
  );

  const handleSegmentClick = useCallback((seq: number) => {
    setSelectedSeq(seq);
    eventRefs.current.get(seq)?.scrollIntoView({ behavior: "smooth", block: "center" });
  }, []);

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
    task.updated_at && !isLive && task.status !== "running"
      ? formatDuration(task.created_at, task.updated_at)
      : isLive
        ? elapsed
        : null;

  const toolCount = items.filter((item) => item.type === "tool_use").length;

  const statusBadge = isLive ? (
    <span className="inline-flex items-center gap-1 rounded-full bg-info/15 px-2 py-0.5 text-xs font-medium text-info">
      <Loader2 className="h-3 w-3 animate-spin" />
      Running
    </span>
  ) : task.status === "completed" ? (
    <span className="inline-flex items-center gap-1 rounded-full bg-success/15 px-2 py-0.5 text-xs font-medium text-success">
      <CheckCircle2 className="h-3 w-3" />
      Completed
    </span>
  ) : task.status === "failed" ? (
    <span className="inline-flex items-center gap-1 rounded-full bg-destructive/15 px-2 py-0.5 text-xs font-medium text-destructive">
      <XCircle className="h-3 w-3" />
      Failed
    </span>
  ) : (
    <span className="inline-flex items-center gap-1 rounded-full bg-muted px-2 py-0.5 text-xs font-medium text-muted-foreground capitalize">
      {task.status}
    </span>
  );

  return (
    <div className="flex min-h-[32rem] flex-col overflow-hidden rounded-xl border border-border bg-card">
      <div className="shrink-0 space-y-2 border-b px-4 py-3">
        <div className="flex items-center gap-3">
          <div className="flex items-center gap-2">
            <div className="flex h-6 w-6 items-center justify-center rounded-full bg-info/10 text-info">
              <Bot className="h-3.5 w-3.5" />
            </div>
            <span className="text-sm font-medium">{profileName ?? "Agent"}</span>
          </div>
          {statusBadge}
          <div className="ml-auto flex items-center gap-1">
            {items.length > 1 && (
              <SortDirectionToggle value={sortDirection} onChange={handleSortDirectionChange} />
            )}
            {filterOptions.length > 0 && (
              <div className="relative" ref={filterRef}>
                <button
                  type="button"
                  onClick={() => setFilterOpen((open) => !open)}
                  className={cn(
                    "flex items-center gap-1 rounded px-2 py-1 text-xs transition-colors",
                    selectedTools.size > 0
                      ? "bg-blue-500/10 text-blue-600 hover:bg-blue-500/20 dark:text-blue-400"
                      : "text-muted-foreground hover:bg-accent hover:text-foreground",
                  )}
                >
                  <Filter className="h-3 w-3" />
                  Filter
                  {selectedTools.size > 0 && (
                    <span className="ml-0.5 rounded-full bg-blue-500/20 px-1.5 py-0 text-[10px] font-medium">
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
                        className="mt-1 w-full rounded px-2 py-1.5 text-left text-muted-foreground hover:bg-accent"
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
              className="flex items-center gap-1 rounded px-2 py-1 text-xs text-muted-foreground transition-colors hover:bg-accent hover:text-foreground"
            >
              {copied ? <Check className="h-3 w-3" /> : <Copy className="h-3 w-3" />}
              {copied ? "Copied" : selectedTools.size > 0 ? "Copy filtered" : "Copy all"}
            </button>
          </div>
        </div>

        <div className="flex flex-wrap items-center gap-2 text-xs">
          <MetadataChip>runner: {task.runner}</MetadataChip>
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
          {task.created_at && (
            <MetadataChip>
              {new Date(task.created_at).toLocaleString(undefined, {
                month: "short",
                day: "numeric",
                hour: "2-digit",
                minute: "2-digit",
              })}
            </MetadataChip>
          )}
        </div>
      </div>

      {displayItems.length > 0 && (
        <div className="shrink-0 border-b px-4 py-2.5">
          <TimelineBar items={displayItems} selectedSeq={selectedSeq} onSegmentClick={handleSegmentClick} />
        </div>
      )}

      <div ref={scrollContainerRef} className="min-h-0 flex-1 overflow-y-auto">
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
          <div className="divide-y">
            {displayItems.map((item) => (
              <TranscriptEventRow
                key={item.seq}
                ref={(el) => {
                  if (el) eventRefs.current.set(item.seq, el);
                  else eventRefs.current.delete(item.seq);
                }}
                item={item}
                isSelected={selectedSeq === item.seq}
              />
            ))}
          </div>
        )}
      </div>
    </div>
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
        title="Chronological"
        onClick={() => onChange("chronological")}
        className={cn(
          "flex items-center gap-1 rounded px-1.5 py-0.5 transition-colors",
          value === "chronological"
            ? "bg-background text-foreground shadow-sm"
            : "text-muted-foreground hover:text-foreground",
        )}
      >
        <ArrowDownNarrowWide className="h-3 w-3" />
        <span className="hidden sm:inline">Oldest</span>
      </button>
      <button
        type="button"
        aria-pressed={value === "newest_first"}
        title="Newest first"
        onClick={() => onChange("newest_first")}
        className={cn(
          "flex items-center gap-1 rounded px-1.5 py-0.5 transition-colors",
          value === "newest_first"
            ? "bg-background text-foreground shadow-sm"
            : "text-muted-foreground hover:text-foreground",
        )}
      >
        <ArrowUpNarrowWide className="h-3 w-3" />
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
              "group relative h-full min-w-[4px] transition-all duration-150 hover:opacity-80",
              isSelected ? color.bgActive : color.bg,
            )}
            style={{ width: `${Math.max(widthPercent, 0.5)}%` }}
            onClick={() => onSegmentClick(items[seg.startIdx]!.seq)}
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

function TranscriptEventRow({
  ref,
  item,
  isSelected,
}: {
  ref?: React.Ref<HTMLDivElement>;
  item: TimelineItem;
  isSelected: boolean;
}) {
  const [expanded, setExpanded] = useState(false);
  const color = getEventColor(item);
  const label = getEventLabel(item);
  const summary = getEventSummary(item);
  const date = useMemo(() => (item.created_at ? new Date(item.created_at) : null), [item.created_at]);

  const hasDetail =
    (item.type === "tool_use" && item.input && Object.keys(item.input).length > 0) ||
    (item.type === "tool_result" && item.output && item.output.length > 0) ||
    (item.type === "thinking" && item.content && item.content.length > 0) ||
    (item.type === "text" && item.content && item.content.length > 0) ||
    (item.type === "error" && item.content && item.content.length > 0);

  return (
    <div ref={ref} className={cn("group transition-colors", isSelected && "bg-accent/50")}>
      <div className="flex items-start gap-2 px-4 py-2">
        <span
          className={cn(
            "mt-0.5 inline-flex min-w-[60px] shrink-0 items-center justify-center rounded px-1.5 py-0.5 text-[11px] font-medium",
            colorClasses[color].label,
          )}
        >
          {item.type === "thinking" && <Brain className="mr-1 h-3 w-3 shrink-0" />}
          {item.type === "error" && <AlertCircle className="mr-1 h-3 w-3 shrink-0" />}
          {label}
        </span>

        <button
          type="button"
          disabled={!hasDetail}
          onClick={() => hasDetail && setExpanded((open) => !open)}
          className={cn(
            "min-w-0 flex-1 py-0.5 text-left text-xs transition-colors",
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
            title={date.toLocaleString()}
          >
            {date.toLocaleTimeString(undefined, {
              hour: "2-digit",
              minute: "2-digit",
              second: "2-digit",
            })}
          </span>
        )}
      </div>

      {hasDetail && expanded && (
        <div className="px-4 pb-3">
          <div className="ml-[72px] rounded border bg-muted/40">
            <EventDetailContent item={item} />
          </div>
        </div>
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
    case "tool_result":
      return (
        <pre className="max-h-60 overflow-auto whitespace-pre-wrap break-all p-3 text-[11px] text-muted-foreground">
          {item.output
            ? item.output.length > 4000
              ? item.output.slice(0, 4000) + "\n... (truncated)"
              : item.output
            : ""}
        </pre>
      );
    case "thinking":
    case "text":
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