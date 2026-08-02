import { useCallback, useEffect, useRef, useState } from "react";
import { Link, useNavigate, useParams } from "react-router-dom";
import {
  Archive,
  ArchiveRestore,
  ArrowLeft,
  LoaderCircle,
  MessageSquareText,
  Pencil,
  Send,
  Square,
  Trash2,
} from "lucide-react";
import {
  archiveSession,
  deleteSession,
  getSession,
  getSessionConversation,
  getSessionEvents,
  getSessionTimeline,
  queueSessionSteer,
  renameSession,
  respondSessionPermission,
  retrySessionBlackboardConclusion,
  restoreSession,
  sendSessionMessage,
  steerSession,
  stopSession,
  type Session,
  type SessionEvent,
} from "@/lib/api";
import { formatDateTime } from "@/lib/format";
import { PageContainer, SettingsAlert } from "@/components/shared";
import { Badge, Button, Card, CardDescription, CardTitle, Input, Label, Textarea } from "@/components/ui";

export function SessionDetailPage() {
  const { sessionId } = useParams<{ sessionId: string }>();
  const navigate = useNavigate();
  const fileInputRef = useRef<HTMLInputElement>(null);
  const [session, setSession] = useState<Session | null>(null);
  const [conversation, setConversation] = useState<SessionEvent[]>([]);
  const [timeline, setTimeline] = useState<SessionEvent[]>([]);
  const [draft, setDraft] = useState("");
  const [attachments, setAttachments] = useState<File[]>([]);
  const [renameValue, setRenameValue] = useState("");
  const [editing, setEditing] = useState(false);
  const [loading, setLoading] = useState(true);
  const [busy, setBusy] = useState(false);
  const [sending, setSending] = useState(false);
  const [retryingConclusion, setRetryingConclusion] = useState(false);
  const [error, setError] = useState<string | null>(null);

  const applySessionData = useCallback(
    (
      found: Session,
      allEvents: SessionEvent[],
      conversationEvents: SessionEvent[],
      timelineEvents: SessionEvent[],
    ) => {
      setSession(found);
      setConversation(conversationEvents.length > 0 ? conversationEvents : allEvents.filter((event) => event.kind === "conversation"));
      setTimeline(timelineEvents.length > 0 ? timelineEvents : allEvents.filter((event) => event.kind !== "conversation"));
      setRenameValue(found.title);
    },
    [],
  );

  const refresh = useCallback(async () => {
    if (!sessionId) return;
    const [found, allEvents, conversationResponse, timelineResponse] = await Promise.all([
      getSession(sessionId),
      getSessionEvents(sessionId),
      getSessionConversation(sessionId),
      getSessionTimeline(sessionId),
    ]);
    applySessionData(found, allEvents.events ?? [], conversationResponse.events ?? [], timelineResponse.events ?? []);
  }, [applySessionData, sessionId]);

  useEffect(() => {
    if (!sessionId) return;
    let cancelled = false;
    Promise.all([
      getSession(sessionId),
      getSessionEvents(sessionId),
      getSessionConversation(sessionId),
      getSessionTimeline(sessionId),
    ])
      .then(([found, allEvents, conversationResponse, timelineResponse]) => {
        if (cancelled) return;
        applySessionData(found, allEvents.events ?? [], conversationResponse.events ?? [], timelineResponse.events ?? []);
        setError(null);
      })
      .catch((reason: unknown) => {
        if (!cancelled) setError((reason as Error).message);
      })
      .finally(() => {
        if (!cancelled) setLoading(false);
      });
    return () => {
      cancelled = true;
    };
  }, [applySessionData, sessionId]);

  const activeContinuationID = session?.active_continuation?.id;
  const conclusionState = session?.blackboard_conclusion?.state;
  useEffect(() => {
    if (!sessionId || (!activeContinuationID && !["pending", "concluding"].includes(conclusionState ?? ""))) {
      return;
    }
    const timer = window.setInterval(() => {
      void refresh().catch((reason: unknown) => setError((reason as Error).message));
    }, 1000);
    return () => window.clearInterval(timer);
  }, [activeContinuationID, conclusionState, refresh, sessionId]);

  async function submitMessage(event: React.FormEvent<HTMLFormElement>) {
    event.preventDefault();
    if (!session || !draft.trim() || session.lifecycle !== "open") return;
    setSending(true);
    try {
      await sendSessionMessage(session.id, draft.trim(), attachments);
      setDraft("");
      setAttachments([]);
      if (fileInputRef.current) fileInputRef.current.value = "";
      await refresh();
      setError(null);
    } catch (reason) {
      setError((reason as Error).message);
    } finally {
      setSending(false);
    }
  }

  async function steer() {
    if (!session || !draft.trim() || session.lifecycle !== "open") return;
    setSending(true);
    try {
      await steerSession(session.id, draft.trim());
      setDraft("");
      await refresh();
      setError(null);
    } catch (reason) {
      setError((reason as Error).message);
    } finally {
      setSending(false);
    }
  }

  async function queueSteer() {
    if (!session || !draft.trim() || session.lifecycle !== "open") return;
    setSending(true);
    try {
      await queueSessionSteer(session.id, draft.trim());
      setDraft("");
      await refresh();
      setError(null);
    } catch (reason) {
      setError((reason as Error).message);
    } finally {
      setSending(false);
    }
  }

  async function respondPermission(permissionRequestId: string, decision: "allow" | "deny", requestId: string) {
    if (!session) return;
    setBusy(true);
    try {
      await respondSessionPermission(session.id, permissionRequestId, decision, requestId);
      await refresh();
      setError(null);
    } catch (reason) {
      setError((reason as Error).message);
    } finally {
      setBusy(false);
    }
  }

  async function stop() {
    if (!session) return;
    setBusy(true);
    try {
      await stopSession(session.id);
      await refresh();
      setError(null);
    } catch (reason) {
      setError((reason as Error).message);
    } finally {
      setBusy(false);
    }
  }

  async function retryBlackboardConclusion() {
    if (!session || retryingConclusion) return;
    setRetryingConclusion(true);
    try {
      const retried = await retrySessionBlackboardConclusion(session.id, newBlackboardRetryID());
      setSession(retried);
      setError(null);
    } catch (reason) {
      setError((reason as Error).message);
    } finally {
      setRetryingConclusion(false);
    }
  }

  async function changeLifecycle(action: "archive" | "restore") {
    if (!session) return;
    setBusy(true);
    try {
      if (action === "archive") await archiveSession(session.id);
      else await restoreSession(session.id);
      await refresh();
      setError(null);
    } catch (reason) {
      setError((reason as Error).message);
    } finally {
      setBusy(false);
    }
  }

  async function saveName() {
    if (!session || !renameValue.trim()) return;
    setBusy(true);
    try {
      await renameSession(session.id, renameValue);
      await refresh();
      setEditing(false);
      setError(null);
    } catch (reason) {
      setError((reason as Error).message);
    } finally {
      setBusy(false);
    }
  }

  async function remove() {
    if (!session || !window.confirm(`Delete archived session ${session.title}? This removes its Session Workdir and Events.`)) {
      return;
    }
    setBusy(true);
    try {
      await deleteSession(session.id);
      navigate("/sessions");
    } catch (reason) {
      setError((reason as Error).message);
      setBusy(false);
    }
  }

  return (
    <PageContainer className="mx-auto w-full max-w-5xl space-y-6">
      <Link
        to="/sessions"
        className="inline-flex items-center gap-1 text-sm text-muted-foreground transition-colors hover:text-foreground focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring focus-visible:ring-offset-2 focus-visible:ring-offset-background"
      >
        <ArrowLeft className="size-4" aria-hidden="true" />
        All sessions
      </Link>

      {loading || (Boolean(sessionId) && session?.id !== sessionId && !error) ? (
        <Card role="status" aria-label="Loading session" className="py-12 text-center text-sm text-muted-foreground">
          Loading session
        </Card>
      ) : error && !session ? (
        <SettingsAlert>{error}</SettingsAlert>
      ) : session ? (
        <>
          <header className="flex flex-col gap-4 sm:flex-row sm:items-start sm:justify-between">
            <div className="min-w-0">
              <p className="mb-1 font-mono text-xs uppercase tracking-[0.14em] text-muted-foreground">Session</p>
              {editing ? (
                <div className="flex flex-wrap items-center gap-2">
                  <Label htmlFor="session-detail-title" className="sr-only">
                    Rename {session.title}
                  </Label>
                  <Input
                    id="session-detail-title"
                    value={renameValue}
                    onChange={(event) => setRenameValue(event.target.value)}
                    className="min-w-60 sm:w-96"
                    autoFocus
                  />
                  <Button size="sm" onClick={saveName} disabled={busy || !renameValue.trim()}>
                    Save name
                  </Button>
                  <Button size="sm" variant="ghost" onClick={() => setEditing(false)} disabled={busy}>
                    Cancel
                  </Button>
                </div>
              ) : (
                <h1 className="flex items-center gap-2 break-words text-2xl font-semibold tracking-tight sm:text-3xl">
                  <MessageSquareText className="size-6 shrink-0 text-signal" aria-hidden="true" />
                  {session.title}
                </h1>
              )}
              <div className="mt-2 flex flex-wrap items-center gap-2 text-sm text-muted-foreground">
                <Badge variant={session.lifecycle === "open" ? "success" : "outline"}>{session.lifecycle}</Badge>
                <RuntimeActivityBadge session={session} />
                {session.runtime_controls?.runtime_provider && <span>{session.runtime_controls.runtime_provider}</span>}
                <span>Last activity {formatDateTime(session.last_activity_at)}</span>
              </div>
            </div>
            <div className="flex shrink-0 flex-wrap gap-1.5">
              <Button size="sm" variant="ghost" onClick={() => setEditing(true)} disabled={busy} aria-label={`Rename ${session.title}`}>
                <Pencil className="size-3.5" aria-hidden="true" />
                Rename
              </Button>
              {session.lifecycle === "open" ? (
                <Button size="sm" variant="outline" onClick={() => changeLifecycle("archive")} disabled={busy}>
                  <Archive className="size-3.5" aria-hidden="true" />
                  Archive
                </Button>
              ) : (
                <Button size="sm" variant="outline" onClick={() => changeLifecycle("restore")} disabled={busy}>
                  <ArchiveRestore className="size-3.5" aria-hidden="true" />
                  Restore
                </Button>
              )}
              {session.lifecycle === "archived" && (
                <Button size="sm" variant="destructive" onClick={remove} disabled={busy}>
                  <Trash2 className="size-3.5" aria-hidden="true" />
                  Delete
                </Button>
              )}
            </div>
          </header>

          <div role="note" className="rounded-lg border border-info/20 bg-info/5 px-4 py-3 text-sm text-muted-foreground">
            This is Non-Project Mode. This Session has no Project Scope; that is informational and does not restrict
            Runtime execution.
          </div>

          <BlackboardConclusionStatus
            session={session}
            retrying={retryingConclusion}
            onRetry={() => void retryBlackboardConclusion()}
          />

          {error && <SettingsAlert>{error}</SettingsAlert>}

          <Card as="section" aria-labelledby="session-conversation-heading" className="gap-4">
            <div className="flex flex-wrap items-start justify-between gap-3">
              <div>
                <CardTitle id="session-conversation-heading">Conversation</CardTitle>
                <CardDescription className="mt-1">A durable Session transcript backed by the same Runtime conversation boundary as Tasks.</CardDescription>
              </div>
              {session.active_continuation && <Badge variant="info">Turn {session.active_continuation.number} · {session.active_continuation.status}</Badge>}
            </div>
            {conversation.length === 0 ? (
              <p className="text-sm text-muted-foreground">No conversation messages yet.</p>
            ) : (
              <ol className="space-y-3" aria-label="Session conversation">
                {conversation.map((event) => (
                  <ConversationRow key={event.id} event={event} />
                ))}
              </ol>
            )}
            {session.lifecycle === "open" && (
              <form className="border-t border-border pt-4" onSubmit={submitMessage} aria-label="Session message composer">
                <Label htmlFor="session-message">Continue the conversation</Label>
                <Textarea
                  id="session-message"
                  value={draft}
                  onChange={(event) => setDraft(event.target.value)}
                  placeholder="Ask the persistent Runtime to continue…"
                  className="mt-1"
                  rows={3}
                  disabled={sending}
                />
                <div className="mt-3 flex flex-wrap items-center justify-between gap-2">
                  <div className="flex items-center gap-2">
                    <Input
                      ref={fileInputRef}
                      id="session-message-attachments"
                      type="file"
                      multiple
                      onChange={(event) => setAttachments(Array.from(event.target.files ?? []))}
                      className="h-auto max-w-56 py-1.5 text-xs"
                      aria-label="Attach files to Session message"
                      disabled={sending}
                    />
                    {attachments.length > 0 && <span className="text-xs text-muted-foreground">{attachments.length} attached</span>}
                  </div>
                  <div className="flex flex-wrap gap-2">
                    {session.runtime_controls?.native_steer_available && (
                      <Button type="button" variant="outline" onClick={steer} disabled={sending || !draft.trim()}>
                        <LoaderCircle className="size-3.5" aria-hidden="true" />
                        Steer Runtime
                      </Button>
                    )}
                    {session.active_continuation && session.runtime_controls?.queue_steer_available && (
                      <Button type="button" variant="outline" onClick={queueSteer} disabled={sending || !draft.trim()}>
                        Queue Turn
                      </Button>
                    )}
                    {session.active_continuation && (
                      <Button type="button" variant="warning" onClick={stop} disabled={busy || sending}>
                        <Square className="size-3.5" aria-hidden="true" />
                        Stop Runtime
                      </Button>
                    )}
                    <Button type="submit" disabled={sending || !draft.trim()}>
                      <Send className="size-3.5" aria-hidden="true" />
                      {sending ? "Sending…" : "Send"}
                    </Button>
                  </div>
                </div>
              </form>
            )}
          </Card>

          <Card as="section" aria-labelledby="session-events-heading" className="gap-4">
            <div>
              <CardTitle id="session-events-heading">Session Events</CardTitle>
              <CardDescription className="mt-1">Runtime Timeline, attachments, lifecycle, permission, and turn events kept separate from the transcript.</CardDescription>
            </div>
            {(session.runtime_controls?.provider_permissions?.length ?? 0) > 0 && (
              <div className="rounded-lg border border-warning/30 bg-warning/5 p-3" role="region" aria-label="Pending Runtime permissions">
                <p className="text-sm font-medium">Runtime permission requests</p>
                <ul className="mt-2 space-y-2">
                  {session.runtime_controls?.provider_permissions?.map((permission) => (
                    <li key={permission.permission_request_id} className="flex flex-wrap items-center justify-between gap-2 text-sm">
                      <span className="font-mono text-xs text-muted-foreground">
                        {permission.provider ?? "Runtime"} · {permission.permission_request_id}
                      </span>
                      <span className="flex gap-2">
                        <Button
                          size="sm"
                          variant="outline"
                          onClick={() => respondPermission(permission.permission_request_id, "deny", permission.request_id)}
                          disabled={busy}
                        >
                          Deny
                        </Button>
                        <Button
                          size="sm"
                          onClick={() => respondPermission(permission.permission_request_id, "allow", permission.request_id)}
                          disabled={busy}
                        >
                          Allow
                        </Button>
                      </span>
                    </li>
                  ))}
                </ul>
              </div>
            )}
            {timeline.length === 0 ? (
              <p className="text-sm text-muted-foreground">No Session Events yet.</p>
            ) : (
              <ol className="space-y-3">
                {timeline.map((event) => (
                  <EventRow key={event.id} event={event} />
                ))}
              </ol>
            )}
          </Card>
        </>
      ) : null}
    </PageContainer>
  );
}

function newBlackboardRetryID() {
  if (typeof crypto !== "undefined" && typeof crypto.randomUUID === "function") {
    return `blackboard-retry-${crypto.randomUUID()}`;
  }
  return `blackboard-retry-${Math.random().toString(36).slice(2)}-${Date.now()}`;
}

function BlackboardConclusionStatus({
  session,
  retrying,
  onRetry,
}: {
  session: Session;
  retrying: boolean;
  onRetry: () => void;
}) {
  const mode = session.blackboard_conclusion?.mode ?? session.run_controls?.blackboard_conclusion_mode ?? "interactive";
  const state = session.blackboard_conclusion?.state ?? "clean";
  const sourceTurn = session.blackboard_conclusion?.source_turn_id;
  const appliedRevision = session.blackboard_conclusion?.applied_revision;
  const stateLabel = state === "action_required" ? "action required" : state;
  const label = `Blackboard · ${mode} · ${stateLabel}${appliedRevision !== undefined ? ` · applied revision ${appliedRevision}` : ""}`;
  return (
    <Card as="section" aria-labelledby="session-blackboard-conclusion-heading" className="gap-3">
      <div className="flex flex-wrap items-center justify-between gap-2">
        <div>
          <CardTitle id="session-blackboard-conclusion-heading" className="text-base">Session Blackboard conclusions</CardTitle>
          <CardDescription className="mt-1">
            Owner-local semantic persistence status; this state never writes Project Blackboard records.
          </CardDescription>
        </div>
        <Badge
          variant={state === "action_required" ? "destructive" : state === "pending" || state === "concluding" ? "warning" : "outline"}
          data-testid="session-blackboard-conclusion-state"
          title={[label, sourceTurn ? `source Turn ${sourceTurn}` : ""].filter(Boolean).join(" · ")}
        >
          {label}
        </Badge>
      </div>
      {state === "action_required" && (
        <div role="alert" className="flex flex-wrap items-center justify-between gap-2 rounded-md border border-destructive/30 bg-destructive/5 px-3 py-2 text-sm">
          <div>
            <p className="text-foreground">Session Blackboard conclusion requires attention.</p>
            {session.blackboard_conclusion?.error_code && (
              <p className="font-mono text-xs text-muted-foreground">{session.blackboard_conclusion.error_code}</p>
            )}
          </div>
          <Button
            type="button"
            size="sm"
            variant="outline"
            onClick={onRetry}
            disabled={retrying || session.blackboard_conclusion?.retry_available !== true}
            aria-label="Retry Session Blackboard conclusion"
          >
            {retrying ? "Retrying…" : "Retry conclusion"}
          </Button>
        </div>
      )}
    </Card>
  );
}

function RuntimeActivityBadge({ session }: { session: Session }) {
  const activity = session.runtime_activity;
  if (!activity?.liveness) return null;
  const label = activity.turn_activity ? `runtime ${activity.liveness} · ${activity.turn_activity}` : `runtime ${activity.liveness}`;
  const variant = activity.liveness === "live" ? "primary" : activity.liveness === "orphaned" || activity.liveness === "unknown" ? "warning" : "outline";
  return <Badge variant={variant} title={activity.warning || label}>{label}</Badge>;
}

function ConversationRow({ event }: { event: SessionEvent }) {
  const payload = event.payload ?? {};
  const role = typeof payload.role === "string" ? payload.role : "runtime";
  const text = typeof payload.text === "string" ? payload.text : eventDescription(event);
  return (
    <li className={`rounded-lg border px-3 py-2 ${role === "user" ? "border-signal/20 bg-signal/5" : "border-border bg-muted/20"}`}>
      <div className="flex items-center justify-between gap-2">
        <span className="font-mono text-xs uppercase tracking-wide text-muted-foreground">{role}</span>
        <time dateTime={event.created_at} className="text-xs text-muted-foreground">{formatDateTime(event.created_at)}</time>
      </div>
      <p className="mt-1 whitespace-pre-wrap break-words text-sm">{text}</p>
    </li>
  );
}

function EventRow({ event }: { event: SessionEvent }) {
  const description = eventDescription(event);
  return (
    <li className="rounded-md border border-border bg-muted/20 px-3 py-2">
      <div className="flex flex-wrap items-center justify-between gap-2">
        <span className="font-mono text-xs uppercase tracking-wide text-muted-foreground">
          #{event.seq} · {event.kind}
        </span>
        <time dateTime={event.created_at} className="text-xs text-muted-foreground">
          {formatDateTime(event.created_at)}
        </time>
      </div>
      <p className="mt-1 whitespace-pre-wrap break-words text-sm">{description}</p>
    </li>
  );
}

function eventDescription(event: SessionEvent): string {
  const payload = event.payload ?? {};
  if (event.kind === "conversation" && typeof payload.text === "string") return payload.text;
  if (event.kind === "attachment") {
    const filename = typeof payload.filename === "string" ? payload.filename : "attachment";
    const size = typeof payload.size === "number" ? ` (${payload.size} bytes)` : "";
    return `Attached ${filename}${size}`;
  }
  if (typeof payload.text === "string") return payload.text;
  if (typeof payload.phase === "string") return payload.phase;
  return JSON.stringify(payload);
}
