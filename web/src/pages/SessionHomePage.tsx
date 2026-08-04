import { useCallback, useEffect, useState } from "react";
import { Link, useLocation, useNavigate } from "react-router-dom";
import { Archive, ArchiveRestore, FilePlus2, MessageSquareText, Pencil, Plus, Trash2 } from "lucide-react";
import { RuntimeLaunchControls, useRuntimeLaunchControls } from "@/components/RuntimeLaunchControls";
import { AttachmentPicker } from "@/components/AttachmentPicker";
import {
  archiveSession,
  createSession,
  deleteSession,
  listSessions,
  renameSession,
  restoreSession,
  type Session,
} from "@/lib/api";
import { formatCompactDateTime } from "@/lib/format";
import { LoadingState, PageContainer, RichEmptyState, SettingsAlert } from "@/components/shared";
import { Badge, Button, Card, CardDescription, CardTitle, Input, Label, Textarea } from "@/components/ui";

export function SessionHomePage({ view = "open" }: { view?: "open" | "archived" }) {
  const archivedView = view === "archived";
  const { hash } = useLocation();
  const navigate = useNavigate();
  const [openSessions, setOpenSessions] = useState<Session[]>([]);
  const [archivedSessions, setArchivedSessions] = useState<Session[]>([]);
  const [draft, setDraft] = useState("");
  const [attachments, setAttachments] = useState<File[]>([]);
  const launchControls = useRuntimeLaunchControls();
  const [loading, setLoading] = useState(true);
  const [creating, setCreating] = useState(false);
  const [busyId, setBusyId] = useState<string | null>(null);
  const [renamingId, setRenamingId] = useState<string | null>(null);
  const [renameValue, setRenameValue] = useState("");
  const [error, setError] = useState<string | null>(null);

  const loadSessions = useCallback(async () => {
    setLoading(true);
    try {
      if (archivedView) {
        const archived = await listSessions("archived");
        setArchivedSessions(archived.sessions ?? []);
      } else {
        const open = await listSessions();
        setOpenSessions(open.sessions ?? []);
      }
      setError(null);
    } catch (e) {
      setError((e as Error).message);
    } finally {
      setLoading(false);
    }
  }, [archivedView]);

  /* eslint-disable react-hooks/set-state-in-effect */
  useEffect(() => {
    // The initial fetch is intentionally shared with the mutation refresh path.
    loadSessions();
  }, [loadSessions]);
  /* eslint-enable react-hooks/set-state-in-effect */

  useEffect(() => {
    if (hash !== "#new-session") return;
    const creationSurface = document.getElementById("new-session");
    creationSurface?.scrollIntoView?.({ block: "start" });
    document.getElementById("session-initial-input")?.focus();
  }, [hash]);

  async function submit(event: React.FormEvent<HTMLFormElement>) {
    event.preventDefault();
    if (!draft.trim()) return;
    setCreating(true);
    launchControls.setError(null);
    try {
      const profileId = await launchControls.resolveRuntimeProfileId();
      const checked = await launchControls.runPreflight("/api/sessions/preflight", profileId);
      if (!checked.pass) {
        launchControls.setError("preflight failed");
        return;
      }
      const created = await createSession(draft, attachments, launchControls.launchPayload(profileId));
      setDraft("");
      setAttachments([]);
      navigate(`/sessions/${encodeURIComponent(created.id)}`);
    } catch (cause) {
      launchControls.setError((cause as Error).message);
    } finally {
      setCreating(false);
    }
  }

  async function changeLifecycle(session: Session, action: "archive" | "restore") {
    setBusyId(session.id);
    try {
      if (action === "archive") await archiveSession(session.id);
      else await restoreSession(session.id);
      await loadSessions();
    } catch (e) {
      setError((e as Error).message);
    } finally {
      setBusyId(null);
    }
  }

  async function remove(session: Session) {
    if (!window.confirm(`Delete archived session ${session.title}? This removes its Session Workdir and Events.`)) return;
    setBusyId(session.id);
    try {
      await deleteSession(session.id);
      await loadSessions();
    } catch (e) {
      setError((e as Error).message);
    } finally {
      setBusyId(null);
    }
  }

  function beginRename(session: Session) {
    setRenamingId(session.id);
    setRenameValue(session.title);
  }

  async function saveRename(session: Session) {
    if (!renameValue.trim()) return;
    setBusyId(session.id);
    try {
      await renameSession(session.id, renameValue);
      setRenamingId(null);
      await loadSessions();
    } catch (e) {
      setError((e as Error).message);
    } finally {
      setBusyId(null);
    }
  }

  return (
    <PageContainer className="mx-auto max-w-6xl space-y-8">
      <header className="max-w-3xl">
        <p className="mb-1 font-mono text-xs uppercase tracking-[0.14em] text-muted-foreground">Workspace</p>
        <h1 className="flex items-center gap-2 text-2xl font-semibold tracking-tight sm:text-3xl">
          <MessageSquareText className="size-6 text-signal" aria-hidden="true" />
          {archivedView ? "Archived Sessions" : "Non-Project Sessions"}
        </h1>
        <p className="mt-2 text-sm leading-6 text-muted-foreground">
          Durable exploratory conversations with their own Events and managed Workdir.
        </p>
      </header>

      <Link
        to={archivedView ? "/sessions" : "/sessions/archived"}
        className="inline-flex w-fit items-center gap-2 text-sm text-muted-foreground hover:text-foreground"
      >
        <Archive className="size-4" aria-hidden="true" />
        {archivedView ? "Back to open Sessions" : "Archived Sessions"}
      </Link>

      <div role="note" className="rounded-lg border border-info/20 bg-info/5 px-4 py-3 text-sm text-foreground">
        <p className="font-medium">Non-Project Mode</p>
        <p className="mt-1 text-muted-foreground">
          This is Non-Project Mode. Sessions have no Project Scope; this statement is informational and does not
          restrict Runtime execution.
        </p>
      </div>

      {!archivedView && <Card as="section" id="new-session" aria-labelledby="new-session-heading" className="gap-5">
        <div>
          <CardTitle id="new-session-heading" className="flex items-center gap-2">
            <Plus className="size-4 text-signal" aria-hidden="true" />
            New session
          </CardTitle>
          <CardDescription className="mt-1">
            The first non-empty line becomes the initial title. You can rename it later without changing the input.
          </CardDescription>
        </div>
        <form className="grid gap-4" onSubmit={submit}>
          <div>
            <Label htmlFor="session-initial-input">Initial input</Label>
            <Textarea
              id="session-initial-input"
              name="input"
              value={draft}
              onChange={(event) => setDraft(event.target.value)}
              placeholder="Describe what you want to explore…"
              className="mt-1"
              rows={4}
            />
          </div>
          <AttachmentPicker
            id="session-attachments"
            files={attachments}
            onFilesChange={setAttachments}
            onError={launchControls.setError}
            ownerLabel="Session"
          />
          <RuntimeLaunchControls controller={launchControls} ownerLabel="session" initialInput={draft} />
          <div className="flex justify-end">
            <Button
              type="submit"
              disabled={creating || !launchControls.launchReady(draft) || (launchControls.form.runner === "host" && !launchControls.hostActivated)}
            >
              <FilePlus2 className="size-4" aria-hidden="true" />
              {creating ? "Creating…" : "Create session"}
            </Button>
          </div>
        </form>
      </Card>}

      {error && <SettingsAlert>{error}</SettingsAlert>}

      {!archivedView && <SessionSection
        id="open-sessions"
        title="Open sessions"
        sessions={openSessions}
        lifecycle="open"
        loading={loading}
        busyId={busyId}
        renamingId={renamingId}
        renameValue={renameValue}
        onRenameValueChange={setRenameValue}
        onBeginRename={beginRename}
        onCancelRename={() => setRenamingId(null)}
        onSaveRename={saveRename}
        onArchive={(session) => changeLifecycle(session, "archive")}
      />}
      {archivedView && <SessionSection
        id="archived-sessions"
        title="Archived sessions"
        sessions={archivedSessions}
        lifecycle="archived"
        loading={loading}
        busyId={busyId}
        renamingId={renamingId}
        renameValue={renameValue}
        onRenameValueChange={setRenameValue}
        onBeginRename={beginRename}
        onCancelRename={() => setRenamingId(null)}
        onSaveRename={saveRename}
        onRestore={(session) => changeLifecycle(session, "restore")}
        onDelete={remove}
      />}
    </PageContainer>
  );
}

function SessionSection({
  id,
  title,
  sessions,
  lifecycle,
  loading,
  busyId,
  renamingId,
  renameValue,
  onRenameValueChange,
  onBeginRename,
  onCancelRename,
  onSaveRename,
  onArchive,
  onRestore,
  onDelete,
}: {
  id: string;
  title: string;
  sessions: Session[];
  lifecycle: "open" | "archived";
  loading: boolean;
  busyId: string | null;
  renamingId: string | null;
  renameValue: string;
  onRenameValueChange: (value: string) => void;
  onBeginRename: (session: Session) => void;
  onCancelRename: () => void;
  onSaveRename: (session: Session) => void;
  onArchive?: (session: Session) => void;
  onRestore?: (session: Session) => void;
  onDelete?: (session: Session) => void;
}) {
  return (
    <section aria-labelledby={id} className="space-y-3">
      <div className="flex items-center justify-between gap-3">
        <h2 id={id} className="text-lg font-semibold tracking-tight">
          {title}
        </h2>
        <Badge variant={lifecycle === "open" ? "success" : "outline"}>{sessions.length}</Badge>
      </div>
      {loading ? (
        <LoadingState label={`Loading ${title.toLowerCase()}`} minHeight="min-h-24" />
      ) : sessions.length === 0 ? (
        <RichEmptyState
          icon={<MessageSquareText className="h-6 w-6" aria-hidden="true" />}
          title={`No ${lifecycle} sessions`}
          description={
            lifecycle === "open"
              ? "Start a session above for durable exploratory conversations."
              : "Archived sessions will appear here."
          }
        />
      ) : (
        <div className="grid gap-3">
          {sessions.map((session) => (
            <SessionRow
              key={session.id}
              session={session}
              busy={busyId === session.id}
              renaming={renamingId === session.id}
              renameValue={renameValue}
              onRenameValueChange={onRenameValueChange}
              onBeginRename={onBeginRename}
              onCancelRename={onCancelRename}
              onSaveRename={onSaveRename}
              onArchive={onArchive}
              onRestore={onRestore}
              onDelete={onDelete}
            />
          ))}
        </div>
      )}
    </section>
  );
}

function SessionRow({
  session,
  busy,
  renaming,
  renameValue,
  onRenameValueChange,
  onBeginRename,
  onCancelRename,
  onSaveRename,
  onArchive,
  onRestore,
  onDelete,
}: {
  session: Session;
  busy: boolean;
  renaming: boolean;
  renameValue: string;
  onRenameValueChange: (value: string) => void;
  onBeginRename: (session: Session) => void;
  onCancelRename: () => void;
  onSaveRename: (session: Session) => void;
  onArchive?: (session: Session) => void;
  onRestore?: (session: Session) => void;
  onDelete?: (session: Session) => void;
}) {
  return (
    <Card
      as="article"
      className="gap-3 transition-[border-color,box-shadow,background-color,transform] duration-150 ease-geist hover:-translate-y-0.5 hover:border-signal/40 hover:shadow-md motion-reduce:translate-y-0 motion-reduce:transition-none sm:flex-row sm:items-center sm:justify-between"
    >
      <div className="min-w-0">
        {renaming ? (
          <div className="flex flex-wrap items-center gap-2">
            <Label htmlFor={`rename-session-${session.id}`} className="sr-only">
              Rename {session.title}
            </Label>
            <Input
              id={`rename-session-${session.id}`}
              value={renameValue}
              onChange={(event) => onRenameValueChange(event.target.value)}
              className="min-w-52 sm:w-80"
              autoFocus
            />
            <Button size="sm" onClick={() => onSaveRename(session)} disabled={busy || !renameValue.trim()}>
              Save name
            </Button>
            <Button size="sm" variant="ghost" onClick={onCancelRename} disabled={busy}>
              Cancel
            </Button>
          </div>
        ) : (
          <Link
            to={`/sessions/${session.id}`}
            aria-label={`Open ${session.title} session`}
            className="group inline-flex max-w-full items-start gap-3 rounded-md focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring focus-visible:ring-offset-2 focus-visible:ring-offset-background"
          >
            <span className="mt-0.5 flex h-8 w-8 shrink-0 items-center justify-center rounded-md border bg-muted text-muted-foreground transition-colors group-hover:border-signal/40 group-hover:text-signal">
              <MessageSquareText className="h-4 w-4" aria-hidden="true" />
            </span>
            <span className="min-w-0 flex-col">
              <span className="block truncate font-medium group-hover:text-signal">{session.title}</span>
              <span className="mt-1 flex flex-wrap items-center gap-2 text-xs text-muted-foreground">
                <Badge variant={session.lifecycle === "open" ? "success" : "outline"}>{session.lifecycle}</Badge>
                <span>Activity {formatCompactDateTime(session.last_activity_at)}</span>
              </span>
            </span>
          </Link>
        )}
      </div>
      {!renaming && (
        <div className="flex shrink-0 flex-wrap items-center gap-1.5 sm:justify-end">
          {session.lifecycle === "open" && onArchive && (
            <Button
              size="sm"
              variant="outline"
              onClick={() => onArchive(session)}
              disabled={busy}
              aria-label={`Archive ${session.title}`}
            >
              <Archive className="size-3.5" aria-hidden="true" />
              <span className="sr-only">Archive {session.title}</span>
            </Button>
          )}
          <Button size="sm" variant="ghost" onClick={() => onBeginRename(session)} disabled={busy} aria-label={`Rename ${session.title}`}>
            <Pencil className="size-3.5" aria-hidden="true" />
            <span className="sr-only">Rename {session.title}</span>
          </Button>
          {session.lifecycle === "archived" && onRestore && (
            <Button
              size="sm"
              variant="outline"
              onClick={() => onRestore(session)}
              disabled={busy}
              aria-label={`Restore ${session.title}`}
            >
              <ArchiveRestore className="size-3.5" aria-hidden="true" />
              <span className="sr-only">Restore {session.title}</span>
            </Button>
          )}
          {session.lifecycle === "archived" && onDelete && (
            <Button
              size="sm"
              variant="destructive"
              onClick={() => onDelete(session)}
              disabled={busy}
              aria-label={`Delete ${session.title}`}
            >
              <Trash2 className="size-3.5" aria-hidden="true" />
              <span className="sr-only">Delete {session.title}</span>
            </Button>
          )}
        </div>
      )}
    </Card>
  );
}
