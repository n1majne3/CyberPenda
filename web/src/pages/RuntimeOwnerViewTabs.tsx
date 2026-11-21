import { FloatingScrollControls, TimelineScrollControls } from "./RuntimeOwnerChrome";
import { tabClass } from "./runtimeOwnerView";

type RuntimeOwnerWorkspaceView = "conversation" | "timeline" | "blackboard";

export function RuntimeOwnerViewTabs({
  activeView,
  hasSessionBlackboard,
  selectView,
  autoFollow,
  scrollToTop,
  scrollToLatest,
  scrollTimelineToTop,
  scrollTimelineToBottom,
}: {
  activeView: RuntimeOwnerWorkspaceView;
  hasSessionBlackboard: boolean;
  selectView: (view: RuntimeOwnerWorkspaceView) => void;
  autoFollow: boolean;
  scrollToTop: () => void;
  scrollToLatest: () => void;
  scrollTimelineToTop: () => void;
  scrollTimelineToBottom: () => void;
}) {
  return (
    <div className="mb-2 flex shrink-0 items-center gap-1 border-b border-border pb-3">
      <div className="flex items-center rounded-md bg-muted/60 p-0.5">
      <button
        type="button"
        className={tabClass(activeView === "conversation")}
        aria-pressed={activeView === "conversation"}
        onClick={() => selectView("conversation")}
      >
        Conversation
      </button>
      <button
        type="button"
        className={tabClass(activeView === "timeline")}
        aria-pressed={activeView === "timeline"}
        onClick={() => selectView("timeline")}
      >
        Timeline
      </button>
      {hasSessionBlackboard && <button
        type="button"
        className={tabClass(activeView === "blackboard")}
        aria-pressed={activeView === "blackboard"}
        onClick={() => selectView("blackboard")}
      >
        Blackboard
      </button>}
      </div>
      <div className="ml-auto">
        {activeView === "conversation" ? (
          <FloatingScrollControls autoFollow={autoFollow} onTop={scrollToTop} onBottom={scrollToLatest} />
        ) : activeView === "timeline" ? (
          <TimelineScrollControls onTop={scrollTimelineToTop} onBottom={scrollTimelineToBottom} />
        ) : null}
      </div>
    </div>
  );
}
