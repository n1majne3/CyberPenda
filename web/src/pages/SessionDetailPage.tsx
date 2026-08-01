import { useEffect, useState } from "react";
import { Link, useNavigate, useParams } from "react-router-dom";
import { Archive, ArchiveRestore, ArrowLeft, MessageSquareText, Pencil, Trash2 } from "lucide-react";
import {
  archiveSession,
  deleteSession,
  getSession,
  getSessionEvents,
  renameSession,
  restoreSession,
  type Session,
  type SessionEvent,
} from "@/lib/api";
import { formatDateTime } from "@/lib/format";
import { PageContainer, SettingsAlert } from "@/components/shared";
import { Badge, Button, Card, CardDescription, CardTitle, Input, Label } from "@/components/ui";

export function SessionDetailPage() {
  const { sessionId } = useParams<{ sessionId: string }>();
  const navigate = useNavigate();
  const [session, setSession] = useState<Session | null>(null);
  const [events, setEvents] = useState<SessionEvent[]>([]);
  const [renameValue, setRenameValue] = useState("");
  const [editing, setEditing] = useState(false);
  const [loading, setLoading] = useState(true);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);

  useEffect(() => {
    if (!sessionId) return;
    let cancelled = false;
    Promise.all([getSession(sessionId), getSessionEvents(sessionId)])
      .then(([found, timeline]) => {
        if (cancelled) return;
        setSession(found);
        setEvents(timeline.events ?? []);
        setRenameValue(found.title);
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
  }, [sessionId]);

  async function refresh() {
    if (!sessionId) return;
    const [found, timeline] = await Promise.all([getSession(sessionId), getSessionEvents(sessionId)]);
    setSession(found);
    setEvents(timeline.events ?? []);
    setRenameValue(found.title);
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

          {error && <SettingsAlert>{error}</SettingsAlert>}

          <Card as="section" aria-labelledby="session-events-heading" className="gap-4">
            <div>
              <CardTitle id="session-events-heading">Session Events</CardTitle>
              <CardDescription className="mt-1">Owner-local activity retained with this Session.</CardDescription>
            </div>
            {events.length === 0 ? (
              <p className="text-sm text-muted-foreground">No Session Events yet.</p>
            ) : (
              <ol className="space-y-3">
                {events.map((event) => (
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
  if (event.kind === "lifecycle" && typeof payload.phase === "string") return payload.phase;
  return JSON.stringify(payload);
}
