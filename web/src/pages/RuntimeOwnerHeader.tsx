import type { Dispatch, SetStateAction } from "react";
import type { NavigateFunction } from "react-router-dom";
import { Archive, ArchiveRestore, Flag, Info, Maximize2, Minimize2, Pencil, Play, Trash2, TriangleAlert, Wrench } from "lucide-react";
import type { FinishReadiness } from "@/lib/api";
import { Button, Input } from "@/components/ui";
import { ConfirmDialog } from "@/components/ConfirmDialog";
import { formatRelativeTime } from "@/lib/format";
import type { RuntimeOwnerView } from "@/lib/runtimeOwner/types";
import { BlackboardConclusionBadge, RuntimeActivityBadge, StatusBadge } from "./RuntimeOwnerChrome";

type ConfirmAction = "stop" | "finish" | "delete" | null;

export function RuntimeOwnerHeader({
  owner,
  projectId,
  currentContinuation,
  controls,
  editingTitle,
  setEditingTitle,
  titleDraft,
  setTitleDraft,
  saveSessionTitle,
  changeSessionLifecycle,
  running,
  finishAvailable,
  setConfirmAction,
  resumeNative,
  resumeAvailable,
  nativeResumeAvailable,
  focusMode,
  selectFocus,
  navigate,
}: {
  owner: RuntimeOwnerView;
  projectId: string | undefined;
  currentContinuation: RuntimeOwnerView["activeContinuation"];
  controls: RuntimeOwnerView["runtimeControls"];
  editingTitle: boolean;
  setEditingTitle: Dispatch<SetStateAction<boolean>>;
  titleDraft: string;
  setTitleDraft: Dispatch<SetStateAction<string>>;
  saveSessionTitle: () => Promise<void>;
  changeSessionLifecycle: (action: "archive" | "restore") => Promise<void>;
  running: boolean;
  finishAvailable: boolean;
  setConfirmAction: Dispatch<SetStateAction<ConfirmAction>>;
  resumeNative: () => Promise<void>;
  resumeAvailable: boolean;
  nativeResumeAvailable: boolean;
  focusMode: boolean;
  selectFocus: (focused: boolean) => void;
  navigate: NavigateFunction;
}) {
  const continuationTitle = currentContinuation
    ? [
        `continuation #${currentContinuation.number}`,
        `runtime: ${currentContinuation.runtimeProvider}`,
        `runner: ${owner.runner}`,
        `status: ${currentContinuation.status}`,
        (controls?.native_session_captured || currentContinuation.nativeSessionID) ? "native session: captured" : "",
        controls?.same_runtime_provider_only ? "same runtime only" : "",
        owner.activeContinuation && owner.latestContinuation && owner.latestContinuation.id !== owner.activeContinuation.id
          ? `prior terminal: #${owner.latestContinuation.number} (${owner.latestContinuation.status})`
          : "",
      ].filter(Boolean).join(" · ")
    : "";

  return (
    <header data-testid="task-session-header" className="shrink-0 rounded-t-xl border border-b-0 border-border bg-card px-2 py-2 sm:px-3">
      <div className="flex min-w-0 items-start gap-2">
        <div className="min-w-0 flex-1">
          <div className="flex min-w-0 flex-wrap items-center gap-2">
            {owner.capabilities.rename && editingTitle ? (
              <div className="flex min-w-0 flex-1 items-center gap-1.5">
                <Input
                  aria-label="Session title"
                  value={titleDraft}
                  onChange={(event) => setTitleDraft(event.target.value)}
                  className="h-8 min-w-0"
                  autoFocus
                />
                <Button size="sm" onClick={() => void saveSessionTitle()} disabled={!titleDraft.trim()}>Save</Button>
                <Button size="sm" variant="ghost" onClick={() => setEditingTitle(false)}>Cancel</Button>
              </div>
            ) : (
              <h1 className="min-w-0 truncate text-base font-semibold tracking-tight" title={owner.title}>{owner.title}</h1>
            )}
            <StatusBadge status={owner.status} />
            <RuntimeActivityBadge activity={owner.runtimeActivity} />
            <BlackboardConclusionBadge owner={owner} />
          </div>
          {currentContinuation && (
            <div
              data-testid="continuation-summary"
              title={continuationTitle}
              className="mt-1 hidden min-w-0 items-center gap-1 overflow-hidden whitespace-nowrap font-mono text-[11px] text-muted-foreground sm:flex"
            >
              <span>continuation #{currentContinuation.number}</span>
              <span aria-hidden="true">·</span>
              <span>runtime {currentContinuation.runtimeProvider}</span>
              <span aria-hidden="true">·</span>
              <span>runner {owner.runner}</span>
              <span aria-hidden="true">·</span>
              <span>Updated {formatRelativeTime(owner.updatedAt)}</span>
            </div>
          )}
        </div>
        <div className="flex shrink-0 items-center gap-1">
          {owner.kind === "task" && projectId && owner.challengeHistoryAvailable && (
            <Button size="sm" variant="ghost" onClick={() => navigate(`/projects/${projectId}/tasks/${owner.id}/challenges`)} aria-label="Challenge history" title="Open Challenge history">
              <Wrench className="h-4 w-4" /> <span className="hidden sm:inline">Challenge history</span>
            </Button>
          )}
          {owner.capabilities.rename && !editingTitle && (
            <Button size="icon" variant="outline" onClick={() => setEditingTitle(true)} aria-label={`Rename ${owner.title}`} title="Rename Session" className="h-8 w-8">
              <Pencil className="h-4 w-4" />
            </Button>
          )}
          {owner.capabilities.archive && (
            <Button size="icon" variant="outline" onClick={() => void changeSessionLifecycle("archive")} aria-label="Archive" title="Archive Session" className="h-8 w-8">
              <Archive className="h-4 w-4" />
            </Button>
          )}
          {owner.capabilities.restore && (
            <Button size="icon" variant="outline" onClick={() => void changeSessionLifecycle("restore")} aria-label="Restore" title="Restore Session" className="h-8 w-8">
              <ArchiveRestore className="h-4 w-4" />
            </Button>
          )}
          {running && finishAvailable && (
            <Button
              size="sm"
              variant="ghost"
              onClick={() => setConfirmAction("finish")}
              aria-label="Finish task"
              title="Finish Task: close Runtime and mark completed"
              data-testid="finish-task"
            >
              <Flag className="h-4 w-4" /> <span className="hidden sm:inline">Finish</span>
            </Button>
          )}
          {owner.capabilities.resumeWithoutMessage && !running && (
            <Button size="sm" variant="ghost" onClick={resumeNative} disabled={!resumeAvailable} aria-label="Resume" title={nativeResumeAvailable ? "Resume native session" : "Start a fresh continuation from the current Task state"}>
              <Play className="h-4 w-4" /> <span className="hidden sm:inline">Resume</span>
            </Button>
          )}
          {owner.capabilities.delete && (
            <Button size="icon" variant="outline" onClick={() => setConfirmAction("delete")} aria-label={`Delete ${owner.kind}`} title={`Delete ${owner.kind === "session" ? "Session" : "Task"}`} className="h-8 w-8 text-destructive hover:text-destructive">
              <Trash2 className="h-4 w-4" />
            </Button>
          )}
          <Button
            size="icon"
            variant="outline"
            onClick={() => selectFocus(!focusMode)}
            aria-label={focusMode ? "Exit focus view" : "Enter focus view"}
            title={focusMode ? "Exit focus view" : "Enter focus view"}
            className="h-8 w-8"
          >
            {focusMode ? <Minimize2 className="h-4 w-4" /> : <Maximize2 className="h-4 w-4" />}
          </Button>
        </div>
      </div>
    </header>
  );
}

export function RuntimeOwnerNotices({
  owner,
  focusMode,
  finishReadiness,
  pollError,
  onRetryPoll,
}: {
  owner: RuntimeOwnerView;
  focusMode: boolean;
  finishReadiness: FinishReadiness | null;
  pollError?: string | null;
  onRetryPoll?: () => void;
}) {
  return (
    <>
      {pollError && (
        <div
          role="alert"
          data-testid="poll-error-notice"
          className="flex shrink-0 items-center justify-between gap-2 border-b border-warning/30 bg-warning/5 px-3 py-2 text-xs text-warning"
        >
          <div className="flex min-w-0 items-center gap-2">
            <TriangleAlert className="h-4 w-4 shrink-0" aria-hidden="true" />
            <span className="min-w-0 break-words">Live refresh failed: {pollError}</span>
          </div>
          {onRetryPoll && (
            <Button size="sm" variant="ghost" onClick={onRetryPoll} className="h-6 shrink-0 px-2 text-xs">
              Retry
            </Button>
          )}
        </div>
      )}

      {owner.kind === "session" && !focusMode && (
        <div role="note" className="flex shrink-0 items-center gap-2 border-b border-info/20 bg-info/5 px-3 py-2 text-xs text-muted-foreground">
          <Info className="h-4 w-4 shrink-0 text-info" aria-hidden="true" />
          <span>Non-Project Mode — no Project Scope is applied, so Runtime execution is not Scope-constrained.</span>
        </div>
      )}

      {owner.kind === "task" && finishReadiness && !finishReadiness.ready_to_finish && (
        <section
          aria-label="Finish Readiness"
          className="shrink-0 space-y-2 border-b border-warning/30 bg-warning/5 px-3 py-2"
        >
          <div className="flex items-center gap-2 text-sm font-medium text-warning">
            <TriangleAlert className="h-4 w-4" />
            <span>Finish Readiness blockers</span>
          </div>
          <div className="grid gap-2 lg:grid-cols-2">
            {finishReadiness.blockers.map((blocker) => (
              <div key={blocker.code} className="rounded-md border border-warning/30 bg-background/70 p-2 text-xs">
                <p className="font-medium text-foreground">{blocker.message}</p>
                <p className="font-mono text-muted-foreground">{blocker.code} · {blocker.count}</p>
                {blocker.links?.map((link) => (
                  <a key={link} href={link} className="mt-1 inline-block font-medium text-signal hover:underline">
                    Open {blocker.code}
                  </a>
                ))}
              </div>
            ))}
          </div>
        </section>
      )}
    </>
  );
}

export function RuntimeOwnerConfirmDialogs({
  owner,
  confirmAction,
  setConfirmAction,
  stop,
  finishTask,
  deleteTask,
}: {
  owner: RuntimeOwnerView | null;
  confirmAction: ConfirmAction;
  setConfirmAction: Dispatch<SetStateAction<ConfirmAction>>;
  stop: () => Promise<void>;
  finishTask: () => Promise<void>;
  deleteTask: () => Promise<void>;
}) {
  return (
    <>
      <ConfirmDialog
        open={confirmAction === "stop"}
        // Keep the dialog title short; put the long Task Goal only in the
        // body so the confirm actions stay on-screen.
        title={owner ? `Stop ${owner.kind}?` : "Stop?"}
        description={
          owner
            ? `Stopping “${owner.title}” closes its Runtime without deleting its work.`
            : undefined
        }
        confirmLabel="Stop"
        destructive
        onConfirm={() => { setConfirmAction(null); void stop(); }}
        onCancel={() => setConfirmAction(null)}
      />
      <ConfirmDialog
        open={confirmAction === "finish"}
        title="Finish task?"
        description={
          owner
            ? `This marks “${owner.title}” completed after closing the Runtime.`
            : "This marks the Task completed after closing the Runtime."
        }
        confirmLabel="Finish"
        onConfirm={() => { setConfirmAction(null); void finishTask(); }}
        onCancel={() => setConfirmAction(null)}
      />
      <ConfirmDialog
        open={confirmAction === "delete"}
        title={owner?.kind === "session" ? "Delete archived session?" : "Delete task?"}
        description={
          owner?.kind === "session"
            ? `This removes “${owner.title}” and its Session Workdir and Events.`
            : owner
              ? `This removes “${owner.title}” and its Workdir.`
              : "This removes the Task and its Workdir."
        }
        confirmLabel="Delete"
        destructive
        onConfirm={() => { setConfirmAction(null); void deleteTask(); }}
        onCancel={() => setConfirmAction(null)}
      />
    </>
  );
}
