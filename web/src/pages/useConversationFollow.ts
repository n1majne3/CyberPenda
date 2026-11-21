import { useCallback, useEffect, useRef, useState, type RefObject } from "react";
import { useVirtualWindow } from "@/lib/virtualWindow";
import type { OwnerHistory, RuntimeOwnerView } from "@/lib/runtimeOwner/types";
import { buildTranscriptRows, isNearScrollBottom, prefersReducedMotion } from "./runtimeOwnerView";

// needs real row layout, so rows carry no content-visibility size hints that
const TRANSCRIPT_ROW_ESTIMATE = 72;

export function useConversationFollow({
  owner,
  activeView,
  history,
  historyRef,
  commitHistory,
}: {
  owner: RuntimeOwnerView | null;
  activeView: "conversation" | "timeline" | "blackboard";
  history: OwnerHistory;
  historyRef: RefObject<OwnerHistory>;
  commitHistory: (next: OwnerHistory) => void;
}) {
  const [autoFollow, setAutoFollow] = useState(true);
  const conversationViewport = useRef<HTMLDivElement>(null);
  const conversationScrollTop = useRef(0);
  const conversationEnd = useRef<HTMLDivElement>(null);
  const autoFollowRef = useRef(true);
  const programmaticAutoFollow = useRef(false);

  const cancelAutoFollowSettlement = useCallback(() => {
    programmaticAutoFollow.current = false;
  }, []);

  // Keep programmatic follow distinct from operator scrolling. Later scroll
  // events repin until explicit operator input ends programmatic follow.
  const settleConversationBottom = useCallback((container: HTMLDivElement) => {
    programmaticAutoFollow.current = true;
    container.scrollTo?.({ top: container.scrollHeight, behavior: "auto" });
    conversationScrollTop.current = container.scrollTop;
  }, []);


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
  // The element wrapping exactly the rendered transcript rows; the virtual
  // window measures it so the rows always cover the viewport (#blank band).
  const [transcriptRowsElement, setTranscriptRowsElement] = useState<HTMLDivElement | null>(null);
  // The window slices display rows (tool results already paired with their
  // calls), so row keys and measured heights match the rendered DOM 1:1.
  const transcriptDisplayRows = buildTranscriptRows(history.transcript);
  const transcriptWindow = useVirtualWindow({
    itemCount: transcriptDisplayRows.length,
    viewport: transcriptViewport,
    estimateHeight: TRANSCRIPT_ROW_ESTIMATE,
    anchorEnd: autoFollow,
    windowElement: transcriptRowsElement,
    rowKey: (index) => transcriptDisplayRows[index]!.entry.id,
  });
  const bindTranscriptViewport = useCallback((node: HTMLDivElement | null) => {
    conversationViewport.current = node;
    if (node && !autoFollowRef.current) {
      node.scrollTop = conversationScrollTop.current;
    }
    setTranscriptViewport(node);
  }, []);
  const bindTranscriptRows = useCallback((node: HTMLDivElement | null) => {
    setTranscriptRowsElement(node);
  }, []);

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

  function resetConversationFollow() {
    autoFollowRef.current = true;
    setAutoFollow(true);
    conversationScrollTop.current = 0;
  }

  return {
    autoFollow,
    autoFollowRef,
    conversationEnd,
    cancelAutoFollowSettlement,
    resetConversationFollow,
    transcriptDisplayRows,
    transcriptWindow,
    bindTranscriptViewport,
    bindTranscriptRows,
    scrollToLatest,
    scrollToTop,
  };
}
