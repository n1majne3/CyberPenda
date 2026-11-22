import { useEffect, useState, useRef, type ReactNode, type RefObject } from "react";
import { Terminal, GitBranch, MessageSquare, ChevronRight, User, Bot, CheckCircle2, CircleX, Brain } from "lucide-react";
import { type TaskTranscriptEntry } from "@/lib/api";
import { Chip } from "@/components/ui";
import { SubagentHistory } from "@/components/task-transcript/SubagentHistory";
import { AttachmentFileRow } from "@/components/AttachmentPicker";
import { collapsedTranscriptTitle, toolCallFields } from "./taskDetailView";
import { formatDateTime, formatRelativeTime } from "@/lib/format";
import { cn } from "@/lib/utils";
import { type TranscriptLane, type TranscriptDisplayRow, buildTranscriptRows, formatTranscriptDuration, collapsedBody } from "./runtimeOwnerView";

export function TranscriptList({
  rows,
  rowsRef,
  endRef,
  liveReasoningID,
  runtimeLabel,
}: {
  rows: TranscriptDisplayRow[];
  rowsRef: (node: HTMLDivElement | null) => void;
  endRef: RefObject<HTMLDivElement | null>;
  liveReasoningID?: string;
  runtimeLabel: string;
}) {
  return (
    <div ref={rowsRef} data-testid="transcript-rows">
      {rows.map((row, index) => {
        const spacing = index === 0 ? "" : row.lane === "user" && row.laneStart ? "mt-4" : "mt-1";
        return (
          <div
            key={row.entry.id}
            data-vw-key={row.entry.id}
            data-testid="transcript-row"
            className={`${spacing}`}
          >
            {row.lane === "divider" ? (
              <TranscriptRow entry={row.entry} />
            ) : (
              <TranscriptTurnGroup
                lane={row.lane}
                showMarker={row.laneStart}
                entry={row.entry}
                runtimeLabel={runtimeLabel}
              >
                <TranscriptRow
                  entry={row.entry}
                  pairedResult={row.result}
                  autoExpandReasoning={row.entry.id === liveReasoningID}
                />
              </TranscriptTurnGroup>
            )}
          </div>
        );
      })}
      {rows.length === 0 && (
        <div className="flex flex-col items-center justify-center gap-2 py-12 text-center text-sm text-muted-foreground">
          <MessageSquare className="size-5 opacity-50" aria-hidden="true" />
          <p>No transcript yet. Send a message below to start.</p>
        </div>
      )}
      <div ref={endRef} />
    </div>
  );
}

export function TranscriptTurnGroup({
  lane,
  showMarker,
  entry,
  runtimeLabel,
  children,
}: {
  lane: Exclude<TranscriptLane, "divider">;
  showMarker: boolean;
  entry: TaskTranscriptEntry;
  runtimeLabel: string;
  children: ReactNode;
}) {
  const label = lane === "user" ? "You" : lane === "runtime" ? runtimeLabel : "System";
  return (
    <div className="relative pl-6">
      {showMarker && (
        <span
          className={cn(
            "absolute left-0 top-1 flex h-4 w-4 items-center justify-center rounded-full border",
            lane === "user" && "border-border bg-card",
            lane === "runtime" && "border-signal/40 bg-signal/10 text-signal",
            lane === "system" && "border-border bg-muted",
          )}
        >
          {lane === "user" && <User className="h-2.5 w-2.5" aria-hidden="true" />}
          {lane === "runtime" && <Bot className="h-2.5 w-2.5" aria-hidden="true" />}
          {lane === "system" && <GitBranch className="h-2.5 w-2.5" aria-hidden="true" />}
        </span>
      )}
      <div className="pl-2">
        {showMarker && (
          <div data-testid="transcript-turn-header" className="mb-1 flex items-center gap-2 text-xs">
            <span className={cn("font-medium", lane === "runtime" && "text-signal")}>{label}</span>
            {entry.created_at && (
              <span className="text-muted-foreground" title={formatDateTime(entry.created_at)}>
                {formatRelativeTime(entry.created_at)}
              </span>
            )}
          </div>
        )}
        {children}
      </div>
    </div>
  );
}

export function TranscriptRow({
  entry,
  pairedResult,
  autoExpandReasoning = false,
}: {
  entry: TaskTranscriptEntry;
  pairedResult?: TaskTranscriptEntry;
  autoExpandReasoning?: boolean;
}) {
  if (entry.kind === "continuation") {
    return (
      <div className="flex items-center justify-center gap-2 py-1 text-xs text-muted-foreground">
        <span className="h-px flex-1 bg-border" />
        <GitBranch className="h-3.5 w-3.5 shrink-0" />
        <span className="min-w-0 break-words text-center">#{entry.seq} {entry.text}</span>
        <span className="h-px flex-1 bg-border" />
      </div>
    );
  }

  if (entry.kind === "attachment") {
    const filename = typeof entry.details?.filename === "string" ? entry.details.filename : "attachment";
    const size = typeof entry.details?.size === "number" ? entry.details.size : undefined;
    return <AttachmentFileRow name={filename} size={size} prefix="Attached" />;
  }

  if (entry.kind === "tool_call" || entry.kind === "tool_result") {
    return (
      <ToolTranscriptRow
        call={entry.kind === "tool_call" ? entry : undefined}
        result={entry.kind === "tool_call" ? pairedResult : entry}
      />
    );
  }

  if (entry.kind === "subagent_block") {
    return <SubagentBlockRow entry={entry} />;
  }

  if (entry.kind === "reasoning" || entry.kind === "runtime_output") {
    return <CollapsedTranscriptRow entry={entry} autoExpand={autoExpandReasoning && entry.kind === "reasoning"} />;
  }

  if (entry.kind === "error") {
    return (
      <div
        role="alert"
        data-testid="transcript-error-row"
        className="flex min-w-0 items-start gap-2 rounded-lg border border-destructive/40 bg-destructive/5 px-3.5 py-2.5 text-sm leading-6 text-destructive"
      >
        <CircleX className="mt-1 h-4 w-4 shrink-0" aria-hidden="true" />
        <div className="min-w-0 whitespace-pre-wrap break-words">{entry.text || "Runtime turn failed"}</div>
      </div>
    );
  }

  const isUser = entry.role === "user";
  const isAssistant = entry.role === "assistant" || entry.role === "runtime";
  return (
    <div
      data-testid="transcript-message-bubble"
      className={cn(
        "min-w-0 whitespace-pre-wrap break-words text-sm leading-6 text-foreground",
        isUser && "rounded-lg bg-muted/40 px-4 py-4",
        isAssistant && "py-1",
        !isUser && !isAssistant && "rounded-md border border-border bg-muted/30 px-3 py-2",
      )}
    >
      {entry.text}
    </div>
  );
}

export function ToolTranscriptRow({ call, result }: { call?: TaskTranscriptEntry; result?: TaskTranscriptEntry }) {
  const [manuallyOpen, setManuallyOpen] = useState(false);
  const isError = Boolean((result?.details as { is_error?: boolean } | undefined)?.is_error) || result?.status === "failed";
  const title = call ? collapsedTranscriptTitle(call) : result ? collapsedTranscriptTitle(result) : "Tool";
  const duration = call?.created_at && result?.created_at ? formatTranscriptDuration(call.created_at, result.created_at) : "";
  return (
    <details
      data-testid="transcript-tool-row"
      open={manuallyOpen}
      onToggle={(event) => setManuallyOpen(event.currentTarget.open)}
      className="group"
    >
      <summary className="-mx-1 flex min-h-9 cursor-pointer list-none items-center gap-2 rounded-sm px-1 py-1.5 text-sm transition-colors hover:bg-muted/30 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring [&::-webkit-details-marker]:hidden">
        <ChevronRight className="h-3.5 w-3.5 shrink-0 text-muted-foreground transition-transform group-open:rotate-90" />
        {isError ? <CircleX className="h-4 w-4 shrink-0 text-destructive" /> : <CheckCircle2 className="h-4 w-4 shrink-0 text-success" />}
        <span className="min-w-0 flex-1 truncate font-mono text-[13px] text-muted-foreground transition-colors group-hover:text-foreground group-open:text-foreground">{title}</span>
        {result?.text && <span className="max-w-[40%] truncate text-xs text-muted-foreground">{collapsedTranscriptTitle(result)}</span>}
        {isError && <Chip variant="danger" className="h-4 px-1 text-[9px]">failed</Chip>}
        {duration && <span className="shrink-0 text-[11px] tabular-nums text-muted-foreground/70">{duration}</span>}
      </summary>
      <div className="ml-[1.625rem] space-y-3 border-l border-border/60 pb-3 pl-4 pr-2 pt-2">
        {call && <ToolCallDetails entry={call} />}
        {result && <pre className="overflow-x-auto whitespace-pre-wrap break-words text-xs leading-5 text-foreground/80">{result.text || "(empty)"}</pre>}
      </div>
    </details>
  );
}

export function SubagentBlockRow({ entry }: { entry: TaskTranscriptEntry }) {
  const [open, setOpen] = useState(false);
  const history = typeof entry.details?.history === "string" ? entry.details.history : "";
  const items = Array.isArray(entry.details?.items) ? (entry.details?.items as TaskTranscriptEntry[]) : [];
  // The child's items render with the same row machinery as the main thread:
  // tool calls pair with their results and collapse like every other tool row.
  const rows = buildTranscriptRows(items);
  return (
    <details data-testid="transcript-subagent-row" className="group" onToggle={(event) => setOpen(event.currentTarget.open)}>
      <summary className={cn(
        "-mx-1 flex min-h-9 cursor-pointer list-none items-center gap-2 rounded-sm px-1 py-1.5 text-sm transition-colors hover:bg-muted/30 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring [&::-webkit-details-marker]:hidden",
      )}>
        <ChevronRight className="h-3.5 w-3.5 shrink-0 text-muted-foreground transition-transform group-open:rotate-90" />
        <Bot className="h-4 w-4 shrink-0 text-muted-foreground" />
        <span className="min-w-0 flex-1 truncate text-[13px] transition-colors group-hover:text-foreground group-open:text-foreground">
          {collapsedTranscriptTitle(entry)}
        </span>
      </summary>
      {history && open && Array.isArray(entry.details?.legacy_items) && buildTranscriptRows(entry.details.legacy_items as TaskTranscriptEntry[]).map((row) => <TranscriptRow key={`legacy-${row.entry.id}`} entry={row.entry} pairedResult={row.result} />)}
      {history && open && <div className="ml-[1.625rem] border-l border-border/60 pb-3 pl-4 pr-2 pt-2">
        <SubagentHistory key={history} history={history} renderItems={(items) => buildTranscriptRows(items).map((row) => (
          <TranscriptRow key={row.entry.id} entry={row.entry} pairedResult={row.result} />
        ))} />
      </div>}
      {!history && rows.length > 0 && (
        <div className="ml-[1.625rem] space-y-1 border-l border-border/60 pb-3 pl-4 pr-2 pt-2">
          {rows.map((row) => (
            <TranscriptRow key={`${entry.id}-${row.entry.id}`} entry={row.entry} pairedResult={row.result} />
          ))}
        </div>
      )}
    </details>
  );
}

export function CollapsedTranscriptRow({ entry, autoExpand = false }: { entry: TaskTranscriptEntry; autoExpand?: boolean }) {
  const [manuallyOpen, setManuallyOpen] = useState(false);
  const wasAutoExpanded = useRef(false);
  useEffect(() => {
    if (wasAutoExpanded.current && !autoExpand) setManuallyOpen(false);
    wasAutoExpanded.current = autoExpand;
  }, [autoExpand]);
  const isReasoning = entry.kind === "reasoning";
  const Icon = isReasoning ? Brain : Terminal;
  return (
    <details
      data-testid={isReasoning ? "transcript-reasoning-row" : "transcript-tool-row"}
      open={autoExpand || manuallyOpen}
      onToggle={(event) => {
        if (!autoExpand) setManuallyOpen(event.currentTarget.open);
      }}
      className="group"
    >
      <summary className={cn(
        "-mx-1 flex min-h-9 cursor-pointer list-none items-center gap-2 rounded-sm px-1 py-1.5 text-sm transition-colors hover:bg-muted/30 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring [&::-webkit-details-marker]:hidden",
        isReasoning && "italic text-muted-foreground",
      )}>
        <ChevronRight className="h-3.5 w-3.5 shrink-0 text-muted-foreground transition-transform group-open:rotate-90" />
        <Icon className="h-4 w-4 shrink-0 text-muted-foreground" />
        <span className="min-w-0 flex-1 truncate text-[13px] transition-colors group-hover:text-foreground group-open:text-foreground">{collapsedTranscriptTitle(entry)}</span>
      </summary>
      <div className="ml-[1.625rem] border-l border-border/60 pb-3 pl-4 pr-2 pt-2">
        <pre className="overflow-x-auto whitespace-pre-wrap break-words text-xs leading-5 text-foreground/80">{collapsedBody(entry)}</pre>
      </div>
    </details>
  );
}

export function ToolCallDetails({ entry }: { entry: TaskTranscriptEntry }) {
  const fields = toolCallFields(entry);
  if (fields.length === 0) {
    return <p className="text-xs text-muted-foreground">No arguments.</p>;
  }
  return (
    <dl className="space-y-1">
      {fields.map((field) => (
        <div
          key={field.label}
          className={field.block ? "space-y-0.5" : "flex flex-wrap items-baseline gap-x-2 gap-y-0.5"}
        >
          <dt className="shrink-0 text-[11px] font-medium uppercase tracking-wide text-muted-foreground/80">{field.label}</dt>
          {field.block ? (
            <dd>
              <pre className="overflow-x-auto whitespace-pre-wrap break-words py-0.5 font-mono text-xs leading-5 text-foreground/85">{field.value}</pre>
            </dd>
          ) : (
            <dd className="min-w-0 break-words font-mono text-xs text-foreground/90">{field.value}</dd>
          )}
        </div>
      ))}
    </dl>
  );
}
