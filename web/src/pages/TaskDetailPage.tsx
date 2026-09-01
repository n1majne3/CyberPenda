import { useCallback, useEffect, useState, useRef, type KeyboardEvent, type ReactNode, type RefObject } from "react";
import { flushSync } from "react-dom";
import { useParams, useNavigate, useSearchParams } from "react-router-dom";
import { Square, Send, Terminal, Activity, GitBranch, MessageSquare, Play, ChevronRight, Wrench, User, ArrowDown, ArrowUp, CheckCircle2, Trash2, CircleX, KeyRound, ListPlus, Loader2, Maximize2, Minimize2, Flag, RefreshCcw, TriangleAlert, Archive, ArchiveRestore, Pencil, Paperclip, Brain, PlugZap } from "lucide-react";
import { apiGet, type FinishReadiness, type ModelProvider, type ProviderPermissionRequest, type RuntimeActivity, type RuntimePlugin, type RuntimeProfile, type TaskTranscriptEntry } from "@/lib/api";
import { Button, Badge, Input, Select, Textarea } from "@/components/ui";
import { ConfirmDialog } from "@/components/ConfirmDialog";
import { ProjectPageShell } from "@/components/ProjectPageShell";
import { ErrorState, LoadingState, PageContainer } from "@/components/shared";
import { AgentTranscriptView } from "@/components/task-transcript/AgentTranscriptView";
import { AttachmentFileRow } from "@/components/AttachmentPicker";
import { collapsedTranscriptTitle, toolCallFields } from "./taskDetailView";
import { displayReasoningEffort, REASONING_EFFORT_VALUES, selectableModelProviders } from "./runtimeProfileForm";
import { modelsForProvider } from "./taskLaunchForm";
import { formatDateTime } from "@/lib/format";
import { mergeTimelineItems, mergeTranscriptEntries, prependTimelineItems, prependTranscriptEntries } from "@/lib/ownerEvents";
import { useDocumentVisibility } from "@/lib/useDocumentVisibility";
import { useVirtualWindow } from "@/lib/virtualWindow";
import { taskRuntimeOwnerAdapter, sessionRuntimeOwnerAdapter, type RuntimeOwnerAdapter } from "@/lib/runtimeOwner/adapter";
import { canPiNativeCrossProvider, conversationModeText, conversationQueueUnavailable, conversationSendLabel, newBlackboardRetryID, newSteerRequestID, resolveConversationAction, resolveConversationSendMode, steerPendingState } from "@/lib/runtimeOwner/conversationKernel";
import { emptyHistory, type ConversationSendMode, type OwnerHistory, type RuntimeOwnerKind, type RuntimeOwnerView } from "@/lib/runtimeOwner/types";

const ACTIVE = new Set(["running", "paused"]);

// Uniform row-height estimates used by the virtualized Runtime Owner history
// lists; they match the contain-intrinsic-size hints on the rendered rows.
const TRANSCRIPT_ROW_ESTIMATE = 72;
const TIMELINE_ROW_ESTIMATE = 48;

export function TaskDetailPage() {
  return <RuntimeOwnerDetailPage ownerKind="task" />;
}

export function RuntimeOwnerDetailPage({ ownerKind }: { ownerKind: RuntimeOwnerKind }) {
  const { projectId, taskId, sessionId } = useParams<{ projectId: string; taskId: string; sessionId: string }>();
  const isSession = ownerKind === "session";
  const ownerID = isSession ? sessionId : taskId;
  const navigate = useNavigate();
  const [searchParams, setSearchParams] = useSearchParams();
  const isVisible = useDocumentVisibility();
  const [owner, setOwner] = useState<RuntimeOwnerView | null>(null);
  const [finishReadiness, setFinishReadiness] = useState<FinishReadiness | null>(null);
  const [activeView, setActiveView] = useState<"conversation" | "timeline">(
    () => searchParams.get("view") === "timeline" ? "timeline" : "conversation",
  );
  const [autoFollow, setAutoFollow] = useState(true);
  const [error, setError] = useState<string | null>(null);
  const [actionError, setActionError] = useState<string | null>(null);
  const [sending, setSending] = useState(false);
  const [steering, setSteering] = useState("");
  const [forceReplaceNext, setForceReplaceNext] = useState(false);
  const [continuationModelProvider, setContinuationModelProvider] = useState("");
  const [continuationModelOverride, setContinuationModelOverride] = useState("");
  const [continuationReasoningEffort, setContinuationReasoningEffort] = useState("high");
  const [turnSelectionSeeded, setTurnSelectionSeeded] = useState(false);
  const [profiles, setProfiles] = useState<RuntimeProfile[]>([]);
  const [modelProviders, setModelProviders] = useState<ModelProvider[]>([]);
  const [runtimePlugins, setRuntimePlugins] = useState<RuntimePlugin[]>([]);
  const [permissionBusy, setPermissionBusy] = useState("");
  const [retryingConclusion, setRetryingConclusion] = useState(false);
  const [editingTitle, setEditingTitle] = useState(false);
  const [titleDraft, setTitleDraft] = useState("");
  const [attachments, setAttachments] = useState<File[]>([]);
  const [confirmAction, setConfirmAction] = useState<"stop" | "finish" | "delete" | null>(null);
  const attachmentInput = useRef<HTMLInputElement>(null);
  const conversationViewport = useRef<HTMLDivElement>(null);
  const conversationScrollTop = useRef(0);
  const timelineViewport = useRef<HTMLDivElement>(null);
  const conversationEnd = useRef<HTMLDivElement>(null);
  const autoFollowRef = useRef(true);
  const programmaticAutoFollow = useRef(false);
  // Monotonic generation so slower in-flight polls cannot overwrite newer data.
  const loadGeneration = useRef(0);
  const abortRef = useRef<AbortController | null>(null);
  // Owner identity at request time; a response can merge only when its owner
  // identity and request generation still match (#202).
  const ownerIDRef = useRef(ownerID);
  const activeViewRef = useRef(activeView);
  const timelineAtTailRef = useRef(true);
  // Mirror render values into refs after each render so poll and paging
  // closures read the latest values without stale captures.
  useEffect(() => {
    ownerIDRef.current = ownerID;
    activeViewRef.current = activeView;
  });

  // Owner-local Runtime Owner History Window state (#202). The whole record is
  // replaced atomically when the selected owner changes so Timeline and
  // Transcript content from the prior owner can never merge into the new
  // route; the ref mirror lets poll closures read the latest cursors.
  const historyRef = useRef<OwnerHistory>(emptyHistory());
  const [history, setHistoryState] = useState<OwnerHistory>(emptyHistory());
  const commitHistory = (next: OwnerHistory) => {
    historyRef.current = next;
    setHistoryState(next);
  };

  const cancelAutoFollowSettlement = useCallback(() => {
    programmaticAutoFollow.current = false;
  }, []);

  // Virtual-window replacement can change scrollHeight after the first jump.
  // Keep programmatic follow distinct from operator scrolling. Later scroll
  // events repin until explicit operator input ends programmatic follow.
  const settleConversationBottom = useCallback((container: HTMLDivElement) => {
    programmaticAutoFollow.current = true;
    container.scrollTo?.({ top: container.scrollHeight, behavior: "auto" });
    conversationScrollTop.current = container.scrollTop;
  }, []);

  const base = isSession ? `/api/sessions/${sessionId}` : `/api/projects/${projectId}/tasks/${taskId}`;
  // The behavioral owner adapter owns every endpoint and payload shape; the
  // workspace keeps only rendering and orchestration.
  const ownerAdapter: RuntimeOwnerAdapter = isSession ? sessionRuntimeOwnerAdapter(base) : taskRuntimeOwnerAdapter(base);

  async function loadAll(mode: "initial" | "poll") {
    if (!ownerID || (!isSession && !projectId)) return;
    abortRef.current?.abort();
    const controller = new AbortController();
    abortRef.current = controller;
    const generation = ++loadGeneration.current;
    const requestOwner = ownerID;
    const before = historyRef.current;
    try {
      const loaded = await ownerAdapter.loadWorkspace(controller.signal, mode, before);
      // A response can merge only when its owner identity and request
      // generation still match; a route change replaces owner-local state
      // before any merge can run.
      if (generation !== loadGeneration.current || requestOwner !== ownerIDRef.current || controller.signal.aborted) {
        return;
      }
      setOwner(loaded.owner);
      setFinishReadiness(loaded.finishReadiness ?? null);
      if (loaded.owner.kind === "session") setTitleDraft(loaded.owner.title);
      // Merge into the latest state at commit time, not the pre-request
      // snapshot, so a backward page (or any other concurrent history
      // change) committed while this request was in flight is preserved
      // instead of being wiped by the merge.
      const latest = historyRef.current;
      const next: OwnerHistory = {
        ...latest,
        timeline: mode === "initial" ? loaded.timeline : mergeTimelineItems(latest.timeline, loaded.timeline),
        transcript: mode === "initial" ? loaded.transcript : mergeTranscriptEntries(latest.transcript, loaded.transcript),
        timelineCursor: loaded.timelineCursor,
        transcriptCursor: loaded.transcriptCursor,
        ...(mode === "initial" ? {
          timelineHasOlder: loaded.timelineHasOlder,
          transcriptHasOlder: loaded.transcriptHasOlder,
        } : {}),
      };
      applyHistoryDelta(
        mode,
        next,
        latest,
        mode === "poll" ? next.timeline.length - latest.timeline.length : 0,
        mode === "poll" ? next.transcript.length - latest.transcript.length : 0,
      );
      setError(null);
    } catch (e) {
      if (controller.signal.aborted || generation !== loadGeneration.current || requestOwner !== ownerIDRef.current) {
        return;
      }
      setError((e as Error).message);
    }
  }

  // Applies one merged history page. Live-tail deltas never move an operator
  // who is reading older history: at the tail the container follows the new
  // rows (chronological sort shifts by the appended height; the newest-first
  // top stays put and the new rows render at the tail), and away from the
  // tail an unseen-event count accumulates instead of forcing scroll.
  function applyHistoryDelta(
    mode: "initial" | "poll",
    next: OwnerHistory,
    latest: OwnerHistory,
    timelineDelta: number,
    transcriptDelta: number,
  ) {
    if (mode === "initial") {
      commitHistory(next);
      return;
    }
    if (transcriptDelta > 0 && activeViewRef.current === "conversation" && !autoFollowRef.current) {
      next = { ...next, transcriptUnseen: latest.transcriptUnseen + transcriptDelta };
    }
    if (timelineDelta > 0 && activeViewRef.current === "timeline") {
      if (timelineAtTailRef.current) {
        const container = timelineViewport.current;
        // Newest-first tail: the appended rows render at the top and the
        // operator stays at the tail without any scroll movement.
        if (container && timelineSortRef.current === "chronological") {
          // Render the appended rows first, then shift the reading position
          // by the appended height so the previously visible content stays in
          // place and the new tail rows appear.
          flushSync(() => commitHistory(next));
          container.scrollTop += timelineDelta * TIMELINE_ROW_ESTIMATE;
          return;
        }
        commitHistory(next);
        return;
      }
      next = { ...next, timelineUnseen: latest.timelineUnseen + timelineDelta };
    }
    commitHistory(next);
  }

  // loadOlderConversation prepends one backward Transcript page and preserves
  // the visible anchor by shifting the container by the prepended height.
  async function loadOlderConversation() {
    const current = historyRef.current;
    if (current.loadingOlder !== null || current.transcript.length === 0 || !current.transcriptHasOlder) return;
    const before = current.transcript[0]!.seq;
    const anchor = conversationViewport.current?.scrollTop ?? 0;
    commitHistory({ ...current, loadingOlder: "transcript" });
    try {
      const page = await ownerAdapter.loadOlderTranscript(before);
      const latest = historyRef.current;
      const merged = prependTranscriptEntries(latest.transcript, page.entries ?? []);
      const prepended = merged.length - latest.transcript.length;
      const next: OwnerHistory = { ...latest, transcript: merged, transcriptHasOlder: page.has_older === true, loadingOlder: null };
      flushSync(() => commitHistory(next));
      const viewport = conversationViewport.current;
      if (viewport && prepended > 0) {
        viewport.scrollTop = anchor + prepended * TRANSCRIPT_ROW_ESTIMATE;
      }
    } catch (e) {
      commitHistory({ ...historyRef.current, loadingOlder: null });
      setActionError((e as Error).message);
    }
  }

  // loadOlderTimeline prepends one backward Timeline page. In the default
  // newest-first sort the older rows land at the bottom of the list, so the
  // visible anchor needs no adjustment; in chronological sort the page lands
  // above the operator and the container shifts by the prepended height.
  async function loadOlderTimeline() {
    const current = historyRef.current;
    if (current.loadingOlder !== null || current.timeline.length === 0 || !current.timelineHasOlder) return;
    const before = current.timeline[0]!.seq;
    const anchor = timelineViewport.current?.scrollTop ?? 0;
    commitHistory({ ...current, loadingOlder: "timeline" });
    try {
      const page = await ownerAdapter.loadOlderTimeline(before);
      const latest = historyRef.current;
      const merged = prependTimelineItems(latest.timeline, page.items ?? []);
      const prepended = merged.length - latest.timeline.length;
      const next: OwnerHistory = { ...latest, timeline: merged, timelineHasOlder: page.has_older === true, loadingOlder: null };
      flushSync(() => commitHistory(next));
      const viewport = timelineViewport.current;
      if (viewport && prepended > 0 && timelineSortRef.current === "chronological") {
        viewport.scrollTop = anchor + prepended * TIMELINE_ROW_ESTIMATE;
      }
    } catch (e) {
      commitHistory({ ...historyRef.current, loadingOlder: null });
      setActionError((e as Error).message);
    }
  }

  // Timeline sort direction, mirrored so the poll and paging closures can read
  // the latest value without stale captures.
  const timelineSortRef = useRef<"chronological" | "newest_first">("newest_first");
  function handleTimelineSortDirection(direction: "chronological" | "newest_first") {
    timelineSortRef.current = direction;
  }

  // Timeline tail state: at the tail (newest-first: the top; chronological:
  // the bottom) live deltas shift the container; away from the tail they
  // accumulate an unseen count instead.
  function handleTimelineAtTail(atTail: boolean) {
    timelineAtTailRef.current = atTail;
    if (atTail && historyRef.current.timelineUnseen > 0) {
      commitHistory({ ...historyRef.current, timelineUnseen: 0 });
    }
  }

  function jumpToTimelineLatest() {
    const container = timelineViewport.current;
    if (container) {
      // The tail is the newest end of the list: the top in the default
      // newest-first sort, the bottom in chronological sort.
      const behavior = prefersReducedMotion() ? "auto" : "smooth";
      if (timelineSortRef.current === "chronological") {
        container.scrollTo({ top: container.scrollHeight, behavior });
      } else {
        container.scrollTo({ top: 0, behavior });
      }
    }
    if (historyRef.current.timelineUnseen > 0) {
      commitHistory({ ...historyRef.current, timelineUnseen: 0 });
    }
  }

  function scrollTimelineToTop() {
    timelineViewport.current?.scrollTo?.({
      top: 0,
      behavior: prefersReducedMotion() ? "auto" : "smooth",
    });
  }

  function scrollTimelineToBottom() {
    const container = timelineViewport.current;
    container?.scrollTo?.({
      top: container.scrollHeight,
      behavior: prefersReducedMotion() ? "auto" : "smooth",
    });
  }

  /* eslint-disable react-hooks/set-state-in-effect, react-hooks/exhaustive-deps */
  useEffect(() => {
    // Initial load on mount/owner change. A route change aborts old requests
    // and atomically replaces owner-local history with an empty record before
    // any merge, so Timeline and Transcript content from the prior owner can
    // never appear on the new route (#202).
    setTurnSelectionSeeded(false);
    autoFollowRef.current = true;
    setAutoFollow(true);
    conversationScrollTop.current = 0;
    timelineAtTailRef.current = true;
    commitHistory(emptyHistory());
    loadAll("initial");
    Promise.all([
      apiGet<{ profiles: RuntimeProfile[] }>("/api/runtime-profiles").then((d) => setProfiles(d.profiles ?? [])),
      apiGet<{ providers: ModelProvider[] }>("/api/model-providers").then((d) => setModelProviders(d.providers ?? [])),
      apiGet<{ plugins: RuntimePlugin[] }>("/api/runtime-plugins").then((d) => setRuntimePlugins(d.plugins ?? [])),
    ]).catch(() => {});
    return () => {
      abortRef.current?.abort();
      cancelAutoFollowSettlement();
    };
  }, [isSession, ownerID, projectId]);
  /* eslint-enable react-hooks/set-state-in-effect, react-hooks/exhaustive-deps */

  // Seed the composer once from the preceding Runtime Turn Selection. Later
  // submits retain the operator's choice instead of resetting.
  /* eslint-disable react-hooks/set-state-in-effect */
  useEffect(() => {
    if (turnSelectionSeeded || !owner) return;
    const preceding = owner.runtimeControls?.turn_selection;
    if (!preceding) {
      setTurnSelectionSeeded(true);
      return;
    }
    setContinuationModelProvider(preceding.model_provider_id?.trim() ?? "");
    setContinuationModelOverride(preceding.model?.trim() ?? "");
    setContinuationReasoningEffort(displayReasoningEffort(preceding.reasoning_effort));
    setTurnSelectionSeeded(true);
  }, [owner, turnSelectionSeeded]);
  /* eslint-enable react-hooks/set-state-in-effect */

  /* eslint-disable react-hooks/exhaustive-deps */
  useEffect(() => {
    if (!owner || activeView !== "conversation") return;
    const container = conversationViewport.current;
    if (!container) return;

    function updateAutoFollow() {
      const previousScrollTop = conversationScrollTop.current;
      const nextScrollTop = container!.scrollTop;
      conversationScrollTop.current = nextScrollTop;
      // A small upward movement is an explicit reading action even when it
      // remains inside the near-bottom threshold. Disable follow immediately
      // so the next live Transcript delta cannot pull a page-long message back
      // to the tail.
      const movingAwayFromTail = nextScrollTop < previousScrollTop;
      const pinned = !movingAwayFromTail && isNearScrollBottom(container!);
      if (programmaticAutoFollow.current) {
        if (!pinned && autoFollowRef.current) {
          container!.scrollTo?.({ top: container!.scrollHeight, behavior: "auto" });
          conversationScrollTop.current = container!.scrollTop;
        }
        return;
      }
      autoFollowRef.current = pinned;
      setAutoFollow((current) => current === pinned ? current : pinned);
      // Returning to the tail dismisses any accumulated unseen count.
      if (pinned && historyRef.current.transcriptUnseen > 0) {
        commitHistory({ ...historyRef.current, transcriptUnseen: 0 });
      }
    }

    const acceptOperatorScroll = (event: Event) => {
      programmaticAutoFollow.current = false;
      // Browser wheel scrolling can update layout before the following scroll
      // event reaches this listener. Record the upward reading intent here so
      // an in-flight live Transcript update cannot win that race and repin.
      if (event instanceof WheelEvent && event.deltaY < 0) {
        autoFollowRef.current = false;
        setAutoFollow(false);
      }
    };

    container.addEventListener("scroll", updateAutoFollow, { passive: true });
    container.addEventListener("wheel", acceptOperatorScroll, { passive: true });
    container.addEventListener("touchstart", acceptOperatorScroll, { passive: true });
    container.addEventListener("pointerdown", acceptOperatorScroll, { passive: true });
    container.addEventListener("keydown", acceptOperatorScroll);
    return () => {
      container.removeEventListener("scroll", updateAutoFollow);
      container.removeEventListener("wheel", acceptOperatorScroll);
      container.removeEventListener("touchstart", acceptOperatorScroll);
      container.removeEventListener("pointerdown", acceptOperatorScroll);
      container.removeEventListener("keydown", acceptOperatorScroll);
    };
  }, [owner?.id, activeView]);
  /* eslint-enable react-hooks/exhaustive-deps */

  // Poll events while the runtime owner is active and the tab is visible.
  // Depends on status/visibility only so the interval is not reset every render;
  // loadAll/owner are intentionally omitted.
  /* eslint-disable react-hooks/exhaustive-deps */
  useEffect(() => {
    if (!isVisible || !owner || !ACTIVE.has(owner.status)) return;
    const id = setInterval(() => void loadAll("poll"), 1000);
    return () => clearInterval(id);
  }, [owner?.status, isVisible]);
  /* eslint-enable react-hooks/exhaustive-deps */

  useEffect(() => {
    if (activeView === "conversation" && autoFollowRef.current) {
      const container = conversationViewport.current;
      if (container) settleConversationBottom(container);
    }
    // autoFollow is a dependency because enabling follow re-renders with the
    // end-anchored virtual window; the synchronous settle in scrollToLatest
    // ran on the previous layout and must be repeated on the taller one.
  }, [activeView, autoFollow, history.transcript.length, settleConversationBottom]);

  // Virtualized rendering window for the conversation transcript (#202): DOM
  // size stays bounded while older history pages accumulate in state. The
  // container element is stored in state so the hook attaches its scroll
  // listener once the workspace renders after the owner loads.
  const [transcriptViewport, setTranscriptViewport] = useState<HTMLDivElement | null>(null);
  const transcriptWindow = useVirtualWindow({
    itemCount: history.transcript.length,
    viewport: transcriptViewport,
    estimateHeight: TRANSCRIPT_ROW_ESTIMATE,
    anchorEnd: autoFollow,
  });
  const bindTranscriptViewport = useCallback((node: HTMLDivElement | null) => {
    conversationViewport.current = node;
    if (node && !autoFollowRef.current) {
      node.scrollTop = conversationScrollTop.current;
    }
    setTranscriptViewport(node);
  }, []);

  const currentProfileRuntimeProvider = owner?.runtimeConfiguration?.runtime_plugin_id ?? profiles.find((profile) => profile.id === owner?.runtimeProfileID)?.provider;
  const currentRuntimeProvider =
    owner?.runtimeControls?.runtime_provider ??
    owner?.activeContinuation?.runtimeProvider ??
    owner?.latestContinuation?.runtimeProvider ??
    currentProfileRuntimeProvider;
  const runtimePlugin = runtimePlugins.find((plugin) => plugin.id === currentRuntimeProvider);
  const continuationModelProviders = selectableModelProviders(
    modelProviders,
    runtimePlugin,
    continuationModelProvider,
  );
  const selectedContinuationModelProvider =
    continuationModelProviders.find((provider) => provider.id === continuationModelProvider) ??
    modelProviders.find((provider) => provider.id === continuationModelProvider);
  // Compute inline instead of useMemo: the compiler cannot preserve memoization
  // over selectedContinuationModelProvider (it may be mutated later), and the
  // derivation is a cheap filter.
  const continuationModelOptions = modelsForProvider(selectedContinuationModelProvider);

  // Keep continuationModelOverride valid for the selected provider by adjusting
  // state during render (not in an effect), comparing against the previous
  // provider/option set so we only reset when the selection actually changes.
  const overrideKey = `${continuationModelProvider}:${continuationModelOptions.join(",")}`;
  const [lastOverrideKey, setLastOverrideKey] = useState("");
  if (lastOverrideKey !== overrideKey) {
    setLastOverrideKey(overrideKey);
    if (!continuationModelProvider) {
      if (continuationModelOverride) setContinuationModelOverride("");
    } else if (!continuationModelOverride || !continuationModelOptions.includes(continuationModelOverride)) {
      setContinuationModelOverride(continuationModelOptions[0] ?? "");
    }
  }

  function scrollToLatest() {
    const container = conversationViewport.current;
    if (!container) return;
    autoFollowRef.current = true;
    setAutoFollow(true);
    settleConversationBottom(container);
    if (historyRef.current.transcriptUnseen > 0) {
      commitHistory({ ...historyRef.current, transcriptUnseen: 0 });
    }
  }

  function scrollToTop() {
    const container = conversationViewport.current;
    if (!container) return;
    autoFollowRef.current = false;
    setAutoFollow(false);
    cancelAutoFollowSettlement();
    conversationScrollTop.current = 0;
    container.scrollTo({ top: 0, behavior: prefersReducedMotion() ? "auto" : "smooth" });
  }

  async function stop() {
    if (!owner) return;
    try {
      await ownerAdapter.stop();
      setActionError(null);
      loadAll("poll");
    } catch (e) {
      setActionError((e as Error).message);
    }
  }

  async function finishTask() {
    if (!owner?.capabilities.finish) return;
    try {
      await ownerAdapter.finish();
      setActionError(null);
      loadAll("poll");
    } catch (e) {
      setActionError((e as Error).message);
    }
  }

  async function retryBlackboardConclusion() {
    if (retryingConclusion) return;
    setRetryingConclusion(true);
    try {
      const retried = await ownerAdapter.retryConclusion(newBlackboardRetryID());
      setOwner(retried);
      setActionError(null);
    } catch (e) {
      setActionError((e as Error).message);
    } finally {
      setRetryingConclusion(false);
    }
  }

  async function deleteTask() {
    if (!owner?.capabilities.delete) return;
    try {
      await ownerAdapter.remove();
      navigate(owner.kind === "session" ? "/sessions" : `/projects/${projectId}/tasks`);
    } catch (e) {
      setActionError((e as Error).message);
    }
  }

  async function resumeNative() {
    if (!owner?.capabilities.resumeWithoutMessage) return;
    try {
      await ownerAdapter.resume(continuationModelPayload());
      setActionError(null);
      loadAll("poll");
    } catch (e) {
      setActionError((e as Error).message);
    }
  }

  async function queueSteer() {
    const directive = steering.trim();
    if (!directive || sending || !owner) return;
    if (conversationQueueUnavailable(owner, continuationModelProvider.trim())) {
      setActionError("Queue cannot switch a Session model provider; use Switch provider and resume.");
      return;
    }
    setSending(true);
    try {
      await ownerAdapter.queueSteer(directive, continuationModelPayload(), owner.capabilities.attachments ? attachments : []);
      if (owner.capabilities.attachments) {
        setAttachments([]);
        if (attachmentInput.current) attachmentInput.current.value = "";
      }
      setSteering("");
      setActionError(null);
      await loadAll("poll");
    } catch (e) {
      setActionError((e as Error).message);
    } finally {
      setSending(false);
    }
  }

  async function sendConversationMessage() {
    const message = steering.trim();
    if (!message || sending || !owner) return;
    setSending(true);
    const requestID = newSteerRequestID();
    try {
      const modelPayload = continuationModelPayload();
      const action = resolveConversationAction(owner, {
        running: ACTIVE.has(owner.status),
        nativeSteerAvailable: owner.runtimeControls?.native_steer_available ?? false,
        interruptSteerAvailable: owner.runtimeControls?.interrupt_steer_available ?? false,
        queueSteerAvailable: owner.runtimeControls?.queue_steer_available ?? true,
        resumeAvailable: owner.runtimeControls?.resume_available ?? !ACTIVE.has(owner.status),
        nativeResumeAvailable: owner.runtimeControls?.native_resume_available ?? false,
        precedingProviderID: owner.runtimeControls?.turn_selection?.model_provider_id?.trim() ?? "",
        selectedProviderID: continuationModelProvider.trim(),
        runtimeProvider: currentRuntimeProvider ?? "",
        nativeSteerMode: owner.runtimeControls?.native_steer_mode,
        forceReplace: forceReplaceNext,
      }, requestID);
      await action.run(message, modelPayload, ownerAdapter, owner.capabilities.attachments ? attachments : []);
      setSteering("");
      setForceReplaceNext(false);
      if (owner.capabilities.attachments) {
        setAttachments([]);
        if (attachmentInput.current) attachmentInput.current.value = "";
      }
      setActionError(null);
      await loadAll("poll");
    } catch (e) {
      setActionError((e as Error).message);
    } finally {
      setSending(false);
    }
  }

  function handleComposerKeyDown(event: KeyboardEvent<HTMLTextAreaElement>) {
    if (event.key !== "Enter" || event.shiftKey || event.nativeEvent.isComposing) return;
    event.preventDefault();
    void sendConversationMessage();
  }

  async function respondToPermission(permissionRequestID: string, decision: "allow" | "deny") {
    setPermissionBusy(permissionRequestID);
    try {
      await ownerAdapter.respondPermission(permissionRequestID, decision);
      setActionError(null);
      loadAll("poll");
    } catch (e) {
      setActionError((e as Error).message);
    } finally {
      setPermissionBusy("");
    }
  }

  async function saveSessionTitle() {
    if (owner?.kind !== "session" || !titleDraft.trim()) return;
    try {
      await ownerAdapter.rename(titleDraft.trim());
      setEditingTitle(false);
      setActionError(null);
      await loadAll("poll");
    } catch (e) {
      setActionError((e as Error).message);
    }
  }

  async function changeSessionLifecycle(action: "archive" | "restore") {
    if (owner?.kind !== "session") return;
    try {
      await ownerAdapter.changeLifecycle(action);
      setActionError(null);
      await loadAll("poll");
    } catch (e) {
      setActionError((e as Error).message);
    }
  }

  function continuationModelPayload() {
    const providerID = continuationModelProvider.trim();
    const model = continuationModelOverride.trim();
    const effort = displayReasoningEffort(continuationReasoningEffort);
    // Every Runtime Turn sends the complete resolved selection. `model` is the
    // canonical field; model_override is kept as a compatibility alias.
    const payload: {
      model_provider_id?: string;
      model?: string;
      model_override?: string;
      reasoning_effort: string;
    } = {
      reasoning_effort: effort,
    };
    if (providerID) payload.model_provider_id = providerID;
    if (model) {
      payload.model = model;
      payload.model_override = model;
    }
    return payload;
  }

  function selectView(view: "conversation" | "timeline") {
    setActiveView(view);
    const next = new URLSearchParams(searchParams);
    next.set("view", view);
    setSearchParams(next, { replace: true });
    // Unseen counts belong to the visible view only; switching resets them.
    if (historyRef.current.timelineUnseen > 0 || historyRef.current.transcriptUnseen > 0) {
      commitHistory({ ...historyRef.current, timelineUnseen: 0, transcriptUnseen: 0 });
    }
  }

  function selectFocus(focused: boolean) {
    const next = new URLSearchParams(searchParams);
    if (focused) {
      next.set("focus", "1");
    } else {
      next.delete("focus");
    }
    setSearchParams(next, { replace: true });
  }

  function selectContinuationModelProvider(providerID: string) {
    setContinuationModelProvider(providerID);
    const provider = continuationModelProviders.find((item) => item.id === providerID) ??
      modelProviders.find((item) => item.id === providerID);
    setContinuationModelOverride(modelsForProvider(provider)[0] ?? "");
  }

  if (error) {
    return (
      <RuntimeOwnerShell projectChrome={!isSession}>
        <ErrorState error={error} title="Couldn't load this conversation" className="m-6 max-w-2xl" />
      </RuntimeOwnerShell>
    );
  }
  if (!owner) {
    return (
      <RuntimeOwnerShell projectChrome={!isSession}>
        <LoadingState label="Loading conversation" className="m-6 max-w-2xl" />
      </RuntimeOwnerShell>
    );
  }
  const currentContinuation = owner.activeContinuation ?? owner.latestContinuation;
  const controls = owner.runtimeControls;
  const nativeResumeAvailable = controls?.native_resume_available ?? Boolean(currentContinuation?.nativeSessionID);
  const resumeAvailable = !ACTIVE.has(owner.status) || controls?.resume_available === true;
  const queueSteerAvailable = controls?.queue_steer_available ?? true;
  const interruptSteerAvailable = controls?.interrupt_steer_available ?? nativeResumeAvailable;
  const nativeSteerAvailable = controls?.native_steer_available ?? false;
  const nativeSteerMode = controls?.native_steer_mode;
  const providerPermissions = controls?.provider_permissions ?? [];
  const running = ACTIVE.has(owner.status);
  // Finish gate trusts server RuntimeControls only (live+idle already applied).
  const finishAvailable = controls?.finish_available === true;
  const sendMode: ConversationSendMode = owner.kind === "session" && owner.lifecycle !== "open"
    ? "unavailable"
    : resolveConversationSendMode({
      running,
      nativeSteerAvailable,
      interruptSteerAvailable,
      queueSteerAvailable,
      resumeAvailable,
    });
  const precedingProviderID = controls?.turn_selection?.model_provider_id?.trim() ?? "";
  const selectedProviderID = continuationModelProvider.trim();
  // Pi native cross-provider only with explicit projected set membership.
  // Codex/Claude and legacy Pi (missing ids) restart; empty preceding is a switch.
  const piNativeCrossProvider = owner.kind === "task" && canPiNativeCrossProvider({
    runtimeProvider: currentRuntimeProvider ?? "",
    nativeSteerAvailable: nativeSteerAvailable || interruptSteerAvailable,
    projectedModelProviderIDs: controls?.projected_model_provider_ids,
    targetProviderID: selectedProviderID,
  });
  const providerSwitchRequested =
    running &&
    selectedProviderID !== "" &&
    selectedProviderID !== precedingProviderID &&
    !piNativeCrossProvider;
  const providerSwitchAvailable = owner.kind === "session" || queueSteerAvailable;
  const queueActionAvailable = queueSteerAvailable;
  const currentTurnModel = controls?.turn_selection?.model?.trim() ?? "";
  const currentTurnEffort = controls?.turn_selection?.reasoning_effort?.trim() ?? "";
  const selectedTurnModel = continuationModelOverride.trim();
  const selectedTurnEffort = continuationReasoningEffort.trim();
  const turnSelectionChanged = !providerSwitchRequested && currentRuntimeProvider === "codex" && (
    (selectedTurnModel !== "" && selectedTurnModel !== currentTurnModel) ||
    (selectedTurnEffort !== "" && selectedTurnEffort !== currentTurnEffort)
  );
  const sendActionLabel = forceReplaceNext
    ? "Interrupt and replace"
    : providerSwitchRequested
      ? providerSwitchAvailable ? "Switch provider and resume" : "Provider switch unavailable"
      : conversationSendLabel(sendMode, nativeSteerMode, turnSelectionChanged);
  const steerActionRequired = controls?.native_steer_state === "action_required";
  const replacementRecoveryAvailable = steerActionRequired && interruptSteerAvailable && [
    "active_turn_not_steerable", "target_turn_changed", "target_turn_completed",
  ].includes(controls?.native_steer_error_code ?? "");
  const focusMode = searchParams.get("focus") === "1";
  const blackboardDisabled = runtimeOwnerBlackboardMode(owner) === "disabled";

  return (
    <RuntimeOwnerShell
      projectChrome={owner.capabilities.projectChrome}
      hideChrome={focusMode}
      data-testid="task-detail-shell"
      className={focusMode ? "h-[calc(100dvh-3.5rem)] max-w-none p-0 md:h-dvh lg:p-0" : "flex min-h-full flex-col"}
      bodyClassName={focusMode ? "flex h-full min-h-0 flex-col" : "flex min-h-[32rem] flex-1 flex-col pb-0 lg:min-h-0"}
    >
      <div data-testid="task-session-header" className="flex h-12 shrink-0 items-center gap-2 px-2 sm:px-3">
        <StatusBadge status={owner.status} />
        <RuntimeActivityBadge activity={owner.runtimeActivity} />
        <BlackboardConclusionBadge owner={owner} />
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
          <h1 className="min-w-0 flex-1 truncate text-sm font-medium" title={owner.title}>{owner.title}</h1>
        )}
        {currentContinuation && (
          <div
            data-testid="continuation-summary"
            title={`continuation #${currentContinuation.number} · runtime: ${currentContinuation.runtimeProvider} · runner: ${owner.runner} · status: ${currentContinuation.status}`}
            className="hidden min-w-0 flex-1 items-center gap-1 overflow-hidden whitespace-nowrap font-mono text-xs text-muted-foreground lg:flex"
          >
            <span>continuation #{currentContinuation.number}</span>
            <span aria-hidden="true">·</span>
            <span>runtime: {currentContinuation.runtimeProvider}</span>
            {owner.runtimeConfiguration && (
              <>
                <span aria-hidden="true">·</span>
                <span>model provider: {owner.runtimeConfiguration.model_provider_name || owner.runtimeConfiguration.model_provider_id}</span>
                <span aria-hidden="true">·</span>
                <span>model: {owner.runtimeConfiguration.model}</span>
                {owner.runtimeConfiguration.runtime_profile_name && (
                  <>
                    <span aria-hidden="true">·</span>
                    <span>profile: {owner.runtimeConfiguration.runtime_profile_name}</span>
                  </>
                )}
              </>
            )}
            <span aria-hidden="true">·</span>
            <span>runner: {owner.runner}</span>
            <span className="hidden xl:inline" aria-hidden="true">·</span>
            <span className="hidden xl:inline">continuation status: {currentContinuation.status}</span>
            {owner.activeContinuation &&
              owner.latestContinuation &&
              owner.latestContinuation.id !== owner.activeContinuation.id && (
                <>
                  <span aria-hidden="true">·</span>
                  <span className="hidden 2xl:inline" data-testid="prior-terminal-continuation">
                    prior terminal: #{owner.latestContinuation.number} ({owner.latestContinuation.status})
                  </span>
                </>
              )}
            {(controls?.native_session_captured || currentContinuation.nativeSessionID) && (
              <span className="hidden 2xl:inline">native session: captured</span>
            )}
            {controls?.same_runtime_provider_only && <span className="hidden 2xl:inline">same runtime only</span>}
          </div>
        )}
        <div className="flex shrink-0 items-center gap-1">
		  {owner.kind === "task" && projectId && (
			<Button size="sm" variant="ghost" onClick={() => navigate(`/projects/${projectId}/tasks/${owner.id}/challenges`)} aria-label="Challenge Workflow" title="Open Challenge Workflow">
			  <Wrench className="h-4 w-4" /> <span className="hidden sm:inline">Challenges</span>
			</Button>
		  )}
          {owner.capabilities.rename && !editingTitle && (
            <Button size="icon" variant="ghost" onClick={() => setEditingTitle(true)} aria-label={`Rename ${owner.title}`} title="Rename Session" className="h-8 w-8">
              <Pencil className="h-4 w-4" />
            </Button>
          )}
          {owner.capabilities.archive && (
            <Button size="icon" variant="ghost" onClick={() => void changeSessionLifecycle("archive")} aria-label="Archive" title="Archive Session" className="h-8 w-8">
              <Archive className="h-4 w-4" />
            </Button>
          )}
          {owner.capabilities.restore && (
            <Button size="icon" variant="ghost" onClick={() => void changeSessionLifecycle("restore")} aria-label="Restore" title="Restore Session" className="h-8 w-8">
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
            <Button size="icon" variant="ghost" onClick={() => setConfirmAction("delete")} aria-label={`Delete ${owner.kind}`} title={`Delete ${owner.kind === "session" ? "Session" : "Task"}`} className="h-8 w-8 text-destructive hover:text-destructive">
              <Trash2 className="h-4 w-4" />
            </Button>
          )}
          <Button
            size="icon"
            variant="ghost"
            onClick={() => selectFocus(!focusMode)}
            aria-label={focusMode ? "Exit focus view" : "Enter focus view"}
            title={focusMode ? "Exit focus view" : "Enter focus view"}
            className="h-10 w-10"
          >
            {focusMode ? <Minimize2 className="h-4 w-4" /> : <Maximize2 className="h-4 w-4" />}
          </Button>
        </div>
      </div>

      {owner.kind === "session" && !focusMode && (
        <div role="note" className="shrink-0 border-b border-info/20 bg-info/5 px-3 py-2 text-xs text-muted-foreground">
          Non-Project Mode · no Project Scope; Runtime execution is unrestricted by Project Scope.
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

      {!blackboardDisabled && owner.blackboardConclusion?.state === "action_required" && (
        <BlackboardConclusionRecovery
          errorCode={owner.blackboardConclusion.error_code}
          validationReason={owner.blackboardConclusion.validation_reason}
          validationFieldPath={owner.blackboardConclusion.validation_field_path}
          validationExpected={owner.blackboardConclusion.validation_expected}
          retryAvailable={owner.blackboardConclusion.retry_available === true}
          nextEligibleAt={owner.blackboardConclusion.next_eligible_at}
          retrying={retryingConclusion}
          onRetry={() => void retryBlackboardConclusion()}
        />
      )}

      <div className="flex h-10 shrink-0 items-center gap-1 px-2 sm:px-3">
        <button
          type="button"
          className={tabClass(activeView === "conversation")}
          aria-pressed={activeView === "conversation"}
          onClick={() => selectView("conversation")}
        >
          <MessageSquare className="h-4 w-4" /> Conversation
        </button>
        <button
          type="button"
          className={tabClass(activeView === "timeline")}
          aria-pressed={activeView === "timeline"}
          onClick={() => selectView("timeline")}
        >
          <Activity className="h-4 w-4" /> Timeline
        </button>
        <div className="ml-auto">
          {activeView === "conversation" ? (
            <FloatingScrollControls autoFollow={autoFollow} onTop={scrollToTop} onBottom={scrollToLatest} />
          ) : (
            <TimelineScrollControls onTop={scrollTimelineToTop} onBottom={scrollTimelineToBottom} />
          )}
        </div>
      </div>

      <div
        data-testid="task-workspace"
        className="flex min-h-[28rem] min-w-0 flex-1 flex-col overflow-visible bg-card/30 md:overflow-hidden lg:min-h-0"
      >
        {activeView === "timeline" ? (
          <div className="min-h-0 flex-1 overflow-hidden p-2 pb-44 sm:p-3 md:pb-5">
            <AgentTranscriptView
              owner={owner}
              items={history.timeline}
              profileName={profiles.find((p) => p.id === owner.runtimeProfileID)?.name}
              isLive={ACTIVE.has(owner.status)}
              scrollRef={timelineViewport}
              onAtTailChange={handleTimelineAtTail}
              onSortDirectionChange={handleTimelineSortDirection}
              unseenCount={history.timelineUnseen}
              onShowLatest={jumpToTimelineLatest}
              footer={
                history.timelineHasOlder ? (
                  <div className="flex justify-center p-3">
                    <Button
                      variant="outline"
                      size="sm"
                      data-testid="load-older-timeline"
                      onClick={() => void loadOlderTimeline()}
                      disabled={history.loadingOlder === "timeline"}
                    >
                      {history.loadingOlder === "timeline" ? (
                        <Loader2 className="h-3.5 w-3.5 animate-spin motion-reduce:animate-none" />
                      ) : (
                        <ArrowUp className="h-3.5 w-3.5" />
                      )}
                      Load older events
                    </Button>
                  </div>
                ) : undefined
              }
            />
            {providerPermissions.length > 0 && (
              <ProviderPermissionRequests
                permissions={providerPermissions}
                permissionBusy={permissionBusy}
                onRespond={respondToPermission}
              />
            )}
          </div>
        ) : (
          <div
            ref={bindTranscriptViewport}
            data-testid="conversation-workspace"
            className="relative min-h-0 flex-1 overflow-y-auto overscroll-contain bg-background px-3 py-5 pb-44 sm:px-6 md:pb-5"
          >
            <div className="mx-auto max-w-3xl">
              {history.transcriptHasOlder && (
                <div className="mb-2 flex justify-center">
                  <Button
                    variant="outline"
                    size="sm"
                    data-testid="load-older-transcript"
                    onClick={() => void loadOlderConversation()}
                    disabled={history.loadingOlder === "transcript"}
                  >
                    {history.loadingOlder === "transcript" ? (
                      <Loader2 className="h-3.5 w-3.5 animate-spin motion-reduce:animate-none" />
                    ) : (
                      <ArrowUp className="h-3.5 w-3.5" />
                    )}
                    Load older messages
                  </Button>
                </div>
              )}
              {transcriptWindow.spacerBefore > 0 && (
                <div aria-hidden="true" data-testid="transcript-spacer-before" style={{ height: transcriptWindow.spacerBefore }} />
              )}
              <TranscriptList
                entries={history.transcript.slice(transcriptWindow.startIndex, transcriptWindow.endIndex)}
                endRef={conversationEnd}
                liveReasoningID={liveReasoningEntryID(
                  history.transcript,
                  owner.runtimeActivity?.liveness === "live" && owner.runtimeActivity.turn_activity === "busy",
                )}
              />
              {transcriptWindow.spacerAfter > 0 && (
                <div aria-hidden="true" data-testid="transcript-spacer-after" style={{ height: transcriptWindow.spacerAfter }} />
              )}
              {providerPermissions.length > 0 && (
                <ProviderPermissionRequests
                  permissions={providerPermissions}
                  permissionBusy={permissionBusy}
                  onRespond={respondToPermission}
                />
              )}
            </div>
            {history.transcriptUnseen > 0 && (
              <button
                type="button"
                data-testid="unseen-transcript-indicator"
                onClick={scrollToLatest}
                className="absolute bottom-28 left-1/2 z-10 flex -translate-x-1/2 items-center gap-1.5 rounded-full border border-border bg-background/95 px-3 py-1.5 text-xs font-medium text-foreground shadow-md transition-colors hover:bg-accent focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
              >
                <ArrowDown className="h-3.5 w-3.5" />
                {history.transcriptUnseen} new {history.transcriptUnseen === 1 ? "message" : "messages"}
              </button>
            )}
          </div>
        )}

        <RuntimeOwnerComposer
          ownerLabel={owner.kind === "session" ? "Session" : "Task"}
          value={steering}
          onChange={setSteering}
          onKeyDown={handleComposerKeyDown}
          onSend={() => void sendConversationMessage()}
          onQueue={() => void queueSteer()}
          onStop={() => setConfirmAction("stop")}
          onFinish={() => setConfirmAction("finish")}
          finishAvailable={finishAvailable}
          sending={sending}
          running={running}
          queueAvailable={queueActionAvailable}
          queueDisabled={owner.kind === "session" && providerSwitchRequested}
          providerSwitchRequested={providerSwitchRequested}
          providerSwitchAvailable={providerSwitchAvailable}
          sendMode={sendMode}
          sendActionLabel={sendActionLabel}
          actionError={actionError}
          steerState={controls?.native_steer_state}
          steerError={controls?.native_steer_error}
          replacementRecoveryAvailable={replacementRecoveryAvailable}
          replacementSelected={forceReplaceNext}
          onUseReplacement={() => setForceReplaceNext(true)}
          continuationModelProviders={continuationModelProviders}
          continuationModelProvider={continuationModelProvider}
          continuationModelOverride={continuationModelOverride}
          continuationModelOptions={continuationModelOptions}
          continuationReasoningEffort={continuationReasoningEffort}
          onSelectProvider={selectContinuationModelProvider}
          onSelectModel={setContinuationModelOverride}
          onSelectReasoningEffort={setContinuationReasoningEffort}
          attachments={owner.capabilities.attachments ? attachments : undefined}
          attachmentInputRef={owner.capabilities.attachments ? attachmentInput : undefined}
          onAttachmentsChange={owner.capabilities.attachments ? setAttachments : undefined}
        />
      </div>

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
    </RuntimeOwnerShell>
  );
}

type RuntimeOwnerShellProps = {
  projectChrome: boolean;
  children: ReactNode;
  hideChrome?: boolean;
  className?: string;
  bodyClassName?: string;
  "data-testid"?: string;
};

function RuntimeOwnerShell({ projectChrome, children, bodyClassName, ...props }: RuntimeOwnerShellProps) {
  if (projectChrome) {
    return <ProjectPageShell bodyClassName={bodyClassName} {...props}>{children}</ProjectPageShell>;
  }
  return (
    <PageContainer
      data-testid={props["data-testid"]}
      className={`mx-auto w-full max-w-6xl ${props.className ?? ""}`}
    >
      <div className={bodyClassName}>{children}</div>
    </PageContainer>
  );
}

function tabClass(active: boolean) {
  return [
    "inline-flex items-center gap-1.5 rounded-t-md border-b-2 px-3 py-2 text-sm transition-colors focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring focus-visible:ring-offset-2 focus-visible:ring-offset-background",
    active ? "border-signal text-foreground" : "border-transparent text-muted-foreground hover:text-foreground",
  ].join(" ");
}

function isNearScrollBottom(container: HTMLElement, threshold = 160) {
  return container.scrollHeight - (container.scrollTop + container.clientHeight) <= threshold;
}


function FloatingScrollControls({
  autoFollow,
  onTop,
  onBottom,
}: {
  autoFollow: boolean;
  onTop: () => void;
  onBottom: () => void;
}) {
  const latestLabel = autoFollow
    ? "Scroll to latest (auto-follow on)"
    : "Scroll to latest (auto-follow off)";

  return (
    <div className="flex items-center gap-1">
      <Button size="sm" variant="outline" className="h-10 w-10 p-0 shadow-md transition-transform duration-150 active:scale-[0.96] motion-reduce:transform-none" onClick={onTop} aria-label="Scroll to top" title="Top">
        <ArrowUp className="h-4 w-4" />
      </Button>
      <Button
        size="sm"
        variant={autoFollow ? "secondary" : "outline"}
        className="relative h-10 w-10 p-0 shadow-md transition-transform duration-150 active:scale-[0.96] motion-reduce:transform-none"
        onClick={onBottom}
        aria-label={latestLabel}
        title={latestLabel}
      >
        <ArrowDown className="h-4 w-4" />
        {autoFollow && (
          <CheckCircle2 className="absolute right-0.5 top-0.5 h-3 w-3 text-signal" aria-hidden="true" />
        )}
      </Button>
    </div>
  );
}

function TimelineScrollControls({
  onTop,
  onBottom,
}: {
  onTop: () => void;
  onBottom: () => void;
}) {
  return (
    <div className="flex items-center gap-1">
      <Button
        size="sm"
        variant="outline"
        className="h-10 w-10 p-0 shadow-md transition-transform duration-150 active:scale-[0.96] motion-reduce:transform-none"
        onClick={onTop}
        aria-label="Scroll Timeline to top"
        title="Timeline top"
      >
        <ArrowUp className="h-4 w-4" />
      </Button>
      <Button
        size="sm"
        variant="outline"
        className="h-10 w-10 p-0 shadow-md transition-transform duration-150 active:scale-[0.96] motion-reduce:transform-none"
        onClick={onBottom}
        aria-label="Scroll Timeline to bottom"
        title="Timeline bottom"
      >
        <ArrowDown className="h-4 w-4" />
      </Button>
    </div>
  );
}

function ProviderPermissionRequests({
  permissions,
  permissionBusy,
  onRespond,
}: {
  permissions: ProviderPermissionRequest[];
  permissionBusy: string;
  onRespond: (permissionRequestID: string, decision: "allow" | "deny") => void;
}) {
  return (
    <section className="mt-5 rounded-lg border border-warning/25 bg-warning/5 p-3" aria-label="Provider permission requests">
      <div className="space-y-2">
        <div className="flex items-center gap-2 text-xs font-medium text-warning-foreground">
          <KeyRound className="h-3.5 w-3.5" /> Provider permission
        </div>
        {permissions.map((permission) => (
          <div key={permission.permission_request_id} className="flex flex-wrap items-center justify-between gap-3 rounded-md border border-warning/20 bg-background/70 px-3 py-2">
            <div className="min-w-0">
              <div className="text-sm">Permission request</div>
              <code className="block max-w-full truncate text-xs text-muted-foreground">{permission.permission_request_id}</code>
            </div>
            <div className="flex gap-2">
              <Button
                size="sm"
                onClick={() => onRespond(permission.permission_request_id, "allow")}
                disabled={permissionBusy !== ""}
                aria-label={`Allow provider permission ${permission.permission_request_id}`}
              >
                <CheckCircle2 className="h-4 w-4" /> Allow
              </Button>
              <Button
                size="sm"
                variant="outline"
                onClick={() => onRespond(permission.permission_request_id, "deny")}
                disabled={permissionBusy !== ""}
                aria-label={`Deny provider permission ${permission.permission_request_id}`}
              >
                <CircleX className="h-4 w-4" /> Deny
              </Button>
            </div>
          </div>
        ))}
      </div>
    </section>
  );
}

function RuntimeOwnerComposer({
  ownerLabel,
  value,
  onChange,
  onKeyDown,
  onSend,
  onQueue,
  onStop,
  onFinish,
  finishAvailable,
  sending,
  running,
  queueAvailable,
  queueDisabled,
  providerSwitchRequested,
  providerSwitchAvailable,
  sendMode,
  sendActionLabel,
  actionError,
  steerState,
  steerError,
  replacementRecoveryAvailable,
  replacementSelected,
  onUseReplacement,
  continuationModelProviders,
  continuationModelProvider,
  continuationModelOverride,
  continuationModelOptions,
  continuationReasoningEffort,
  onSelectProvider,
  onSelectModel,
  onSelectReasoningEffort,
  attachments,
  attachmentInputRef,
  onAttachmentsChange,
}: {
  ownerLabel: "Task" | "Session";
  value: string;
  onChange: (value: string) => void;
  onKeyDown: (event: KeyboardEvent<HTMLTextAreaElement>) => void;
  onSend: () => void;
  onQueue: () => void;
  onStop: () => void;
  onFinish: () => void;
  finishAvailable: boolean;
  sending: boolean;
  running: boolean;
  queueAvailable: boolean;
  queueDisabled: boolean;
  providerSwitchRequested: boolean;
  providerSwitchAvailable: boolean;
  sendMode: ConversationSendMode;
  sendActionLabel: string;
  actionError: string | null;
  steerState?: string;
  steerError?: string;
  replacementRecoveryAvailable: boolean;
  replacementSelected: boolean;
  onUseReplacement: () => void;
  continuationModelProviders: ModelProvider[];
  continuationModelProvider: string;
  continuationModelOverride: string;
  continuationModelOptions: string[];
  continuationReasoningEffort: string;
  onSelectProvider: (providerID: string) => void;
  onSelectModel: (model: string) => void;
  onSelectReasoningEffort: (effort: string) => void;
  attachments?: File[];
  attachmentInputRef?: RefObject<HTMLInputElement | null>;
  onAttachmentsChange?: (files: File[]) => void;
}) {
  // The runtime is offline but resumable when the only admitted send mode is
  // resume: explain the state and its consequence instead of a bare badge.
  const runtimeOffline = sendMode === "resume";
  return (
    <div data-testid="task-composer" className="fixed inset-x-0 bottom-0 z-30 shrink-0 bg-background/95 px-3 py-2 shadow-[0_-8px_24px] shadow-black/15 backdrop-blur-sm sm:px-4 md:static md:z-10 md:shadow-none">
      <div className="mx-auto max-w-3xl space-y-2">
        {actionError && <p role="alert" className="text-xs text-destructive">{actionError}</p>}
        {runtimeOffline && (
          <div className="flex items-center gap-2 rounded-md border border-warning/30 bg-warning/10 px-3 py-2 text-xs text-[hsl(28_90%_32%)]">
            <PlugZap className="h-3.5 w-3.5 flex-none" />
            <span>Runtime 当前离线。发送消息将恢复此 Session 并启动新的 Runtime。</span>
          </div>
        )}
        {steerState === "action_required" && (
          <div role="alert" className="flex flex-wrap items-center justify-between gap-2 rounded-md border border-warning/40 bg-warning/10 px-3 py-2 text-xs">
            <span>{steerError || "Steering needs operator action."}</span>
            {replacementRecoveryAvailable && (
              <Button type="button" size="sm" variant="outline" onClick={onUseReplacement} disabled={replacementSelected}>
                {replacementSelected ? "Replacement selected" : "Use interrupt and replace"}
              </Button>
            )}
          </div>
        )}
        <div className="overflow-hidden rounded-lg border border-input bg-card shadow-sm focus-within:border-ring">
          <Textarea
            aria-label={`${ownerLabel} message`}
            name="task_message"
            value={value}
            onChange={(event) => onChange(event.target.value)}
            onKeyDown={onKeyDown}
            placeholder="Focus on admin.example.com next…"
            rows={2}
            autoComplete="off"
            className="max-h-40 min-h-[60px] resize-y rounded-none border-0 bg-transparent px-3 py-2.5 shadow-none focus-visible:border-transparent focus-visible:ring-0"
          />
          <div className="flex flex-wrap items-center gap-1.5 px-2 py-1.5">
            <div className="flex min-w-0 flex-1 flex-wrap items-center gap-2">
              {onAttachmentsChange && (
                <label className="inline-flex h-7 cursor-pointer items-center gap-1 rounded-md border-0 bg-transparent px-1.5 text-xs text-muted-foreground transition-colors hover:text-foreground focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring">
                  <Paperclip className="h-3.5 w-3.5" />
                  <span>{attachments?.length ? `${attachments.length} attached` : "Attach"}</span>
                  <input
                    ref={attachmentInputRef}
                    type="file"
                    multiple
                    className="sr-only"
                    aria-label="Attach files to Session message"
                    onChange={(event) => onAttachmentsChange(Array.from(event.target.files ?? []))}
                  />
                </label>
              )}
              {onAttachmentsChange && <span className="mx-1 h-4 w-px bg-border" />}
              <Select
                size="sm"
                className="h-7 min-w-0 w-auto max-w-full border-0 bg-transparent px-1.5 text-xs shadow-none focus-visible:ring-2 sm:max-w-[13rem]"
                name="continuation_model_provider"
                value={continuationModelProvider}
                onChange={(event) => onSelectProvider(event.target.value)}
                aria-label="Continuation model provider"
              >
                <option value="">Select model provider</option>
                {continuationModelProviders.map((provider) => (
                  <option key={provider.id} value={provider.id}>{provider.name}</option>
                ))}
              </Select>
              <Select
                size="sm"
                className="h-7 min-w-0 w-auto max-w-full border-0 bg-transparent px-1.5 text-xs shadow-none focus-visible:ring-2 sm:max-w-[13rem]"
                name="continuation_model"
                value={continuationModelOverride}
                onChange={(event) => onSelectModel(event.target.value)}
                aria-label="Continuation model"
                disabled={!continuationModelProvider || continuationModelOptions.length === 0}
              >
                {continuationModelOptions.length === 0 ? (
                  <option value="">Default model</option>
                ) : continuationModelOptions.map((model) => (
                  <option key={model} value={model}>{model}</option>
                ))}
              </Select>
              <Select
                size="sm"
                className="h-7 min-w-0 w-auto max-w-full border-0 bg-transparent px-1.5 text-xs shadow-none focus-visible:ring-2 sm:max-w-[9rem]"
                name="continuation_reasoning_effort"
                value={displayReasoningEffort(continuationReasoningEffort)}
                onChange={(event) => onSelectReasoningEffort(event.target.value)}
                aria-label="Continuation reasoning effort"
              >
                {REASONING_EFFORT_VALUES.map((effort) => (
                  <option key={effort} value={effort}>{effort}</option>
                ))}
              </Select>
              {!runtimeOffline && (
                <Badge variant={sendMode === "unavailable" ? "warning" : "outline"} size="sm">
                  {providerSwitchRequested ? "switch provider" : conversationModeText(sendMode)}
                </Badge>
              )}
              {steerPendingState(steerState) && (
                <Badge variant="warning" size="sm" data-testid="steer-pending-badge">
                  steering pending…
                </Badge>
              )}
              {steerState === "failed" && (
                <Badge variant="destructive" size="sm" data-testid="steer-failed-badge">
                  steering failed
                </Badge>
              )}
            </div>
            <div className="ml-auto flex shrink-0 items-center gap-1">
              <span className="mr-1 hidden text-xs text-muted-foreground sm:inline">Enter 发送 · Shift+Enter 换行</span>
              {running && queueAvailable && sendMode !== "queue" && (
                <Button
                  size="icon-xl"
                  variant="ghost"
                  onClick={onQueue}
                  disabled={!value.trim() || sending || queueDisabled}
                  aria-label="Queue message"
                  title={queueDisabled ? "Queue cannot switch a Session model provider" : "Queue message"}
                >
                  <ListPlus className="h-4 w-4" />
                </Button>
              )}
              {running && finishAvailable && (
                <Button
                  size="icon-xl"
                  variant="ghost"
                  onClick={onFinish}
                  disabled={sending}
                  aria-label={`Finish ${ownerLabel.toLowerCase()}`}
                  title="Finish Task: close Runtime and mark completed"
                  data-testid="finish-task-composer"
                >
                  <Flag className="h-4 w-4" />
                </Button>
              )}
              {running && (
                <Button size="icon-xl" variant="destructive" onClick={onStop} disabled={sending} aria-label={`Stop ${ownerLabel.toLowerCase()}`} title={`Stop ${ownerLabel}`}>
                  <Square className="h-4 w-4" />
                </Button>
              )}
              <Button
                size="icon-xl"
                onClick={onSend}
                disabled={!value.trim() || sending || sendMode === "unavailable" || (providerSwitchRequested && !providerSwitchAvailable)}
                aria-label={sendActionLabel}
                title={sendActionLabel}
              >
                {sending ? <Loader2 className="h-4 w-4 animate-spin motion-reduce:animate-none" /> : sendMode === "resume" ? <Play className="h-4 w-4" /> : <Send className="h-4 w-4" />}
              </Button>
            </div>
          </div>
        </div>
      </div>
    </div>
  );
}

function StatusBadge({ status }: { status: string }) {
  const variant =
    status === "completed" ? "success" :
    status === "running" ? "primary" :
    status === "failed" ? "destructive" :
    status === "stopped" ? "warning" :
    status === "interrupted" ? "warning" : "outline";
  return <Badge variant={variant} className="shrink-0 whitespace-nowrap">{status}</Badge>;
}

function RuntimeActivityBadge({ activity }: { activity?: RuntimeActivity }) {
  if (!activity?.liveness) return null;
  const liveness = activity.liveness;
  const turn = activity.turn_activity;
  const label =
    liveness === "live" && turn
      ? `runtime ${liveness} · ${turn}`
      : `runtime ${liveness}`;
  const variant =
    liveness === "live" ? "primary" :
    liveness === "offline" ? "outline" :
    liveness === "orphaned" ? "warning" :
    liveness === "unknown" ? "warning" : "outline";
  return (
    <Badge
      variant={variant}
      data-testid="runtime-activity"
      title={activity.warning || label}
      className="shrink-0 whitespace-nowrap"
    >
      {label}
    </Badge>
  );
}

function BlackboardConclusionBadge({ owner }: { owner: RuntimeOwnerView }) {
  const mode = runtimeOwnerBlackboardMode(owner);
  if (mode === "disabled") {
    return (
      <Badge
        variant="outline"
        data-testid="blackboard-conclusion-state"
        title="Blackboard: Disabled"
        className="min-w-0 shrink"
      >
        <span className="truncate whitespace-nowrap">Blackboard: Disabled</span>
      </Badge>
    );
  }
  const state = owner.blackboardConclusion?.state ?? "clean";
  const sourceTurn = owner.blackboardConclusion?.source_turn_id;
  const appliedRevision = owner.blackboardConclusion?.applied_revision;
  const stateLabel = state === "action_required" ? "action required" : state;
  const label = `Blackboard · ${mode} · ${stateLabel}${appliedRevision !== undefined ? ` · applied revision ${appliedRevision}` : ""}`;
  const details = [label];
  if (sourceTurn) details.push(`source Turn ${sourceTurn}`);
  if (appliedRevision !== undefined) details.push(`applied revision ${appliedRevision}`);
  return (
    <Badge
      variant={state === "action_required" ? "destructive" : state === "pending" || state === "concluding" ? "warning" : "outline"}
      data-testid="blackboard-conclusion-state"
      title={details.join(" · ")}
      className="min-w-0 shrink"
    >
      <span className="truncate whitespace-nowrap">{label}</span>
    </Badge>
  );
}

function runtimeOwnerBlackboardMode(owner: RuntimeOwnerView) {
  return owner.blackboardConclusionMode ?? owner.blackboardConclusion?.mode ?? "interactive";
}

const blackboardConclusionErrorCopy: Record<string, string> = {
  semantic_conclusion_invalid_result: "The runtime returned an invalid Blackboard conclusion.",
  semantic_conclusion_repair_exhausted: "The runtime could not produce a valid Blackboard conclusion after one repair attempt.",
  conclude_tool_use_forbidden: "The runtime attempted to use a tool while concluding Blackboard state.",
};

function BlackboardConclusionRecovery({
  errorCode,
  validationReason,
  validationFieldPath,
  validationExpected,
  retryAvailable,
  nextEligibleAt,
  retrying,
  onRetry,
}: {
  errorCode?: string;
  validationReason?: string;
  validationFieldPath?: string;
  validationExpected?: string;
  retryAvailable: boolean;
  nextEligibleAt?: string;
  retrying: boolean;
  onRetry: () => void;
}) {
  const code = errorCode?.trim() || "conclusion_action_required";
  const message = blackboardConclusionErrorCopy[code] ?? "Blackboard conclusion requires operator attention.";
  const validationDetail = [validationReason, validationFieldPath, validationExpected].filter(Boolean).join(" · ");
  return (
    <div
      role="alert"
      aria-label="Blackboard conclusion requires attention"
      className="flex shrink-0 flex-col gap-2 border-b border-destructive/30 bg-destructive/5 px-3 py-2 text-sm sm:flex-row sm:items-center"
    >
      <TriangleAlert className="h-4 w-4 shrink-0 text-destructive" aria-hidden="true" />
      <div className="min-w-0 flex-1">
        <p className="text-foreground">{message}</p>
        <p className="break-all font-mono text-xs text-muted-foreground">{code}</p>
        {validationDetail && (
          <p data-testid="blackboard-conclusion-validation" className="break-all font-mono text-xs text-muted-foreground">
            {validationDetail}
          </p>
        )}
      </div>
      <Button
        type="button"
        size="sm"
        variant="outline"
        onClick={onRetry}
        disabled={retrying || !retryAvailable}
        aria-label="Retry Blackboard conclusion"
        title={!retryAvailable && nextEligibleAt ? `Retry available after ${formatDateTime(nextEligibleAt)}` : "Retry Blackboard conclusion"}
      >
        <RefreshCcw className={`h-4 w-4 ${retrying ? "animate-spin motion-reduce:animate-none" : ""}`} />
        Retry
      </Button>
    </div>
  );
}

function liveReasoningEntryID(entries: TaskTranscriptEntry[], runtimeBusy: boolean): string | undefined {
  if (!runtimeBusy || entries.length === 0) return undefined;
  const maxSeq = Math.max(...entries.map((entry) => entry.seq));
  for (let index = entries.length - 1; index >= 0; index -= 1) {
    const entry = entries[index];
    if (entry?.kind === "reasoning" && entry.status === "streaming" && entry.seq === maxSeq) return entry.id;
  }
  return undefined;
}

function TranscriptList({
  entries,
  endRef,
  liveReasoningID,
}: {
  entries: TaskTranscriptEntry[];
  endRef: RefObject<HTMLDivElement | null>;
  liveReasoningID?: string;
}) {
  return (
    <div>
      {entries.map((entry, index) => {
        // A user message starts a new turn and gets breathing room above it.
        // Everything inside an agent turn — assistant messages, tool calls,
        // tool results — stays tight so the agent's reasoning reads as one block.
        const spacing = index === 0 ? "" : isUserTurnStart(entry) ? "mt-4" : "mt-1";
        return (
          <div
            key={entry.id}
            data-testid="transcript-row"
            className={`[contain-intrinsic-size:72px] [content-visibility:auto] ${spacing}`}
          >
            <TranscriptRow entry={entry} autoExpandReasoning={entry.id === liveReasoningID} />
          </div>
        );
      })}
      {entries.length === 0 && (
        <div className="flex flex-col items-center justify-center gap-2 py-12 text-center text-sm text-muted-foreground">
          <MessageSquare className="size-5 opacity-50" aria-hidden="true" />
          <p>No transcript yet. Send a message below to start.</p>
        </div>
      )}
      <div ref={endRef} />
    </div>
  );
}

// isUserTurnStart identifies a row that begins a new operator turn. Only these
// get the roomier spacing; agent reasoning (assistant text, tool calls, tool
// results) stays tight so a single agent turn reads as one cohesive block.
function isUserTurnStart(entry: TaskTranscriptEntry): boolean {
  if (entry.kind === "continuation" || entry.kind === "tool_call" || entry.kind === "tool_result") {
    return false;
  }
  return entry.role === "user";
}

function TranscriptRow({ entry, autoExpandReasoning = false }: { entry: TaskTranscriptEntry; autoExpandReasoning?: boolean }) {
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

  if (isCollapsedTranscriptEntry(entry)) {
    return <CollapsedTranscriptRow entry={entry} autoExpand={autoExpandReasoning && entry.kind === "reasoning"} />;
  }

  const isUser = entry.role === "user";
  const isAssistant = entry.role === "assistant";
  const Icon = isUser ? User : MessageSquare;
  const roleLabel = isUser ? "You" : isAssistant ? "Agent" : entry.role;
  return (
    <div className={`flex gap-3 text-sm ${isUser ? "justify-end pl-8 sm:pl-16" : "justify-start pr-2 sm:pr-8"}`}>
      {!isUser && !isAssistant && (
        <span className="mt-1 flex h-7 w-7 shrink-0 items-center justify-center rounded-md border border-border bg-muted/60 text-muted-foreground">
          <Icon className="h-4 w-4" />
        </span>
      )}
      <div
        data-testid="transcript-message-bubble"
        className={`min-w-0 max-w-[88%] rounded-lg px-3 py-2 sm:px-3.5 ${isUser ? "bg-primary/10 dark:bg-primary/15" : "bg-transparent px-0"}`}
      >
        {!isAssistant && (
          <div className="mb-1 text-[11px] font-medium text-muted-foreground">
            {roleLabel}
            {entry.created_at && (
              <span className="font-normal text-muted-foreground/70"> · {formatDateTime(entry.created_at)}</span>
            )}
          </div>
        )}
        <div className="whitespace-pre-wrap break-words leading-6 text-foreground">{entry.text}</div>
      </div>
      {isUser && (
        <span className="mt-1 flex h-7 w-7 shrink-0 items-center justify-center rounded-md bg-primary/10 text-foreground/70 dark:bg-primary/15">
          <Icon className="h-4 w-4" />
        </span>
      )}
    </div>
  );
}

function CollapsedTranscriptRow({ entry, autoExpand = false }: { entry: TaskTranscriptEntry; autoExpand?: boolean }) {
  const [manuallyOpen, setManuallyOpen] = useState(false);
  const wasAutoExpanded = useRef(false);
  useEffect(() => {
    // A live reasoning row opens automatically. When streaming stops, close
    // it once; completed rows remain manually expandable across later polls.
    if (wasAutoExpanded.current && !autoExpand) setManuallyOpen(false);
    wasAutoExpanded.current = autoExpand;
  }, [autoExpand]);
  const isError = entry.kind === "tool_result" && (entry.details as { is_error?: boolean } | undefined)?.is_error === true;
  const Icon =
    entry.kind === "reasoning"
      ? Brain
      : entry.kind === "runtime_output"
        ? Terminal
      : entry.kind === "tool_result"
        ? isError
          ? CircleX
          : CheckCircle2
        : Wrench;
  return (
    <details
      data-testid={entry.kind === "reasoning" ? "transcript-reasoning-row" : "transcript-tool-row"}
      open={autoExpand || manuallyOpen}
      onToggle={(event) => {
        if (!autoExpand) setManuallyOpen(event.currentTarget.open);
      }}
      className="group border-b border-border/50 last:border-b-0"
    >
      <summary className="-mx-1 flex min-h-9 cursor-pointer list-none items-center gap-2 rounded-sm px-1 py-1.5 text-sm transition-colors hover:bg-muted/30 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring [&::-webkit-details-marker]:hidden">
        <ChevronRight className="h-3.5 w-3.5 shrink-0 text-muted-foreground transition-transform group-open:rotate-90" />
        <Icon className={`h-4 w-4 shrink-0 ${isError ? "text-destructive" : "text-muted-foreground"}`} />
        <span className="min-w-0 flex-1 truncate text-[13px] text-muted-foreground transition-colors group-hover:text-foreground group-open:text-foreground">{collapsedTranscriptTitle(entry)}</span>
        {entry.created_at && <span className="ml-auto shrink-0 text-[11px] tabular-nums text-muted-foreground/70">{formatDateTime(entry.created_at)}</span>}
      </summary>
      <div className="ml-[1.625rem] border-l border-border/60 pb-3 pl-4 pr-2 pt-2">
        {entry.kind === "tool_call" ? (
          <ToolCallDetails entry={entry} />
        ) : (
          <pre className="overflow-x-auto whitespace-pre-wrap break-words text-xs leading-5 text-foreground/80">{collapsedBody(entry)}</pre>
        )}
      </div>
    </details>
  );
}

function ToolCallDetails({ entry }: { entry: TaskTranscriptEntry }) {
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

function isCollapsedTranscriptEntry(entry: TaskTranscriptEntry) {
  return entry.kind === "reasoning" || entry.kind === "tool_call" || entry.kind === "tool_result" || entry.kind === "runtime_output";
}

function collapsedBody(entry: TaskTranscriptEntry) {
  if (entry.kind === "tool_result") return entry.text || "(empty)";

  const parts: string[] = [];
  if (entry.text) parts.push(entry.text);
  if (entry.tool_call_id) parts.push(`tool_call_id: ${entry.tool_call_id}`);
  if (entry.tool_name) parts.push(`tool_name: ${entry.tool_name}`);
  if (entry.details && Object.keys(entry.details).length > 0) {
    parts.push(JSON.stringify(entry.details, null, 2));
  }
  return parts.join("\n\n") || "(empty)";
}

function prefersReducedMotion(): boolean {
  return window.matchMedia?.("(prefers-reduced-motion: reduce)").matches ?? false;
}
