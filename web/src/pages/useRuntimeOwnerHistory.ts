import { useEffect, useRef, useState, type Dispatch, type RefObject, type SetStateAction } from "react";
import { flushSync } from "react-dom";
import type { FinishReadiness } from "@/lib/api";
import { mergeTimelineItems, mergeTranscriptEntries, prependTimelineItems, prependTranscriptEntries } from "@/lib/ownerEvents";
import type { RuntimeOwnerAdapter } from "@/lib/runtimeOwner/adapter";
import { emptyHistory, type OwnerHistory, type RuntimeOwnerView } from "@/lib/runtimeOwner/types";
import { prefersReducedMotion } from "./runtimeOwnerView";

const TIMELINE_ROW_ESTIMATE = 48;

export function useOwnerHistoryState() {
  const historyRef = useRef<OwnerHistory>(emptyHistory());
  const [history, setHistoryState] = useState<OwnerHistory>(emptyHistory());
  const commitHistory = (next: OwnerHistory) => {
    historyRef.current = next;
    setHistoryState(next);
  };

  return { historyRef, history, commitHistory };
}

export function useRuntimeOwnerHistory({
  ownerID,
  isSession,
  projectId,
  activeView,
  ownerAdapter,
  autoFollowRef,
  historyRef,
  commitHistory,
  setOwner,
  setFinishReadiness,
  setError,
  setActionError,
}: {
  ownerID: string | undefined;
  isSession: boolean;
  projectId: string | undefined;
  activeView: "conversation" | "timeline" | "blackboard";
  ownerAdapter: RuntimeOwnerAdapter;
  autoFollowRef: RefObject<boolean>;
  historyRef: RefObject<OwnerHistory>;
  commitHistory: (next: OwnerHistory) => void;
  setOwner: Dispatch<SetStateAction<RuntimeOwnerView | null>>;
  setFinishReadiness: Dispatch<SetStateAction<FinishReadiness | null>>;
  setError: Dispatch<SetStateAction<string | null>>;
  setActionError: Dispatch<SetStateAction<string | null>>;
}) {
  // Monotonic generation so slower in-flight polls cannot overwrite newer data.
  const loadGeneration = useRef(0);
  const abortRef = useRef<AbortController | null>(null);
  const ownerIDRef = useRef(ownerID);
  const activeViewRef = useRef(activeView);
  const timelineAtTailRef = useRef(true);
  const timelineViewport = useRef<HTMLDivElement>(null);
  const [pollError, setPollError] = useState<string | null>(null);
  useEffect(() => {
    ownerIDRef.current = ownerID;
    activeViewRef.current = activeView;
  });


  async function loadAll(mode: "initial" | "poll") {
    if (!ownerID || (!isSession && !projectId)) return;
    if (mode === "poll" && abortRef.current) return;
    abortRef.current?.abort();
    const controller = new AbortController();
    abortRef.current = controller;
    const generation = ++loadGeneration.current;
    const requestOwner = ownerID;
    const before = historyRef.current;
    if (mode === "initial") setPollError(null);
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
          transcriptBefore: loaded.transcriptBefore,
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
      setPollError(null);
    } catch (e) {
      if (controller.signal.aborted || generation !== loadGeneration.current || requestOwner !== ownerIDRef.current) {
        return;
      }
      if (mode === "poll") {
        setPollError((e as Error).message);
      } else {
        setError((e as Error).message);
        setPollError(null);
      }
    } finally {
      if (abortRef.current === controller) abortRef.current = null;
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

  // loadOlderConversation prepends one backward Transcript page. The virtual
  // window measures the new rows and compensates scrollTop, so the reading
  // position survives without a manual estimate-based shift.
  async function loadOlderConversation() {
    const current = historyRef.current;
    if (current.loadingOlder !== null || current.transcript.length === 0 || !current.transcriptHasOlder) return;
    const before = current.transcriptBefore ?? current.transcript[0]!.seq;
    commitHistory({ ...current, loadingOlder: "transcript" });
    try {
      const page = await ownerAdapter.loadOlderTranscript(before);
      const latest = historyRef.current;
      const merged = prependTranscriptEntries(latest.transcript, page.entries ?? []);
      commitHistory({ ...latest, transcript: merged, transcriptHasOlder: page.has_older === true, transcriptBefore: page.before, loadingOlder: null });
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

  function resetTimelineTail() {
    timelineAtTailRef.current = true;
  }

  return {
    abortRef,
    pollError,
    timelineViewport,
    resetTimelineTail,
    loadAll,
    loadOlderConversation,
    loadOlderTimeline,
    handleTimelineSortDirection,
    handleTimelineAtTail,
    jumpToTimelineLatest,
    scrollTimelineToTop,
    scrollTimelineToBottom,
  };
}
