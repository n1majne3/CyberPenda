import { type ReactNode } from "react";
import { Link, useParams } from "react-router-dom";
import { ArrowDown, ArrowUp, CheckCircle2 } from "lucide-react";
import { type RuntimeActivity } from "@/lib/api";
import { Button, Chip } from "@/components/ui";
import { ProjectPageShell } from "@/components/ProjectPageShell";
import { PageContainer } from "@/components/shared";
import { statusWord } from "@/lib/runtimeOwner/status";
import { cn } from "@/lib/utils";
import { type RuntimeOwnerView } from "@/lib/runtimeOwner/types";
import { runtimeOwnerBlackboardMode } from "./runtimeOwnerView";

export type RuntimeOwnerShellProps = {
  projectChrome: boolean;
  children: ReactNode;
  hideChrome?: boolean;
  className?: string;
  bodyClassName?: string;
  contentClassName?: string;
  "data-testid"?: string;
};

export function RuntimeOwnerShell({ projectChrome, children, bodyClassName, contentClassName, ...props }: RuntimeOwnerShellProps) {
  if (projectChrome) {
    return <ProjectPageShell bodyClassName={bodyClassName} contentClassName={contentClassName} {...props}>{children}</ProjectPageShell>;
  }
  return (
    <PageContainer
      data-testid={props["data-testid"]}
      className={cn("mx-auto flex w-full max-w-6xl min-w-0 flex-col p-0 lg:p-0", props.className)}
    >
      <div className={cn("flex min-h-0 min-w-0 w-full flex-1 flex-col", contentClassName, bodyClassName)}>{children}</div>
    </PageContainer>
  );
}

export function FloatingScrollControls({
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

export function TimelineScrollControls({
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

export function StatusBadge({ status }: { status: string }) {
  const variant =
    status === "running" ? "success" :
    status === "failed" ? "danger" :
    status === "paused" || status === "interrupted" ? "warning" : "neutral";
  const label = status ? statusWord(status) : "Unknown";
  return <Chip variant={variant} dot className="shrink-0 whitespace-nowrap">{label}</Chip>;
}

export function RuntimeActivityBadge({ activity }: { activity?: RuntimeActivity }) {
  if (!activity?.liveness) return null;
  const label =
    activity.liveness === "live"
      ? activity.turn_activity === "busy" ? "Runtime busy" : "Runtime idle"
      : activity.liveness === "offline"
        ? "Runtime offline"
        : "Runtime state unknown";
  const variant =
    activity.liveness === "live"
      ? activity.turn_activity === "busy" ? "success" : "info"
      : activity.liveness === "offline" ? "danger" : "warning";
  return (
    <Chip
      variant={variant}
      dot
      data-testid="runtime-activity"
      title={activity.warning || label}
      className="shrink-0 whitespace-nowrap"
    >
      {label}
    </Chip>
  );
}

export function BlackboardConclusionBadge({ owner }: { owner: RuntimeOwnerView }) {
  const { projectId } = useParams();
  const mode = runtimeOwnerBlackboardMode(owner);
  if (mode === "disabled") {
    return (
      <Chip
        variant="neutral"
        dot
        data-testid="blackboard-conclusion-state"
        title="Blackboard: Disabled"
        className="min-w-0 shrink"
      >
        <span className="truncate whitespace-nowrap">Blackboard: Disabled</span>
      </Chip>
    );
  }
  if (owner.blackboardProtocol === "fgs") return <Link className="rounded border px-2 py-1 text-xs" to={owner.kind === "session" ? `/sessions/${owner.id}/blackboard` : `/projects/${projectId}/blackboard`}>Blackboard · FGS</Link>;
  const state = owner.blackboardConclusion?.state ?? "clean";
  const sourceTurn = owner.blackboardConclusion?.source_turn_id;
  const appliedRevision = owner.blackboardConclusion?.applied_revision;
  const stateLabel = state === "action_required" ? "action required" : state;
	const modeLabel = mode === "working_graph" ? "Working Graph" : "Interactive";
	const label = `Blackboard · ${modeLabel} · ${stateLabel}${appliedRevision !== undefined ? ` · applied revision ${appliedRevision}` : ""}`;
  const details = [label];
  if (sourceTurn) details.push(`source Turn ${sourceTurn}`);
  return (
    <Chip
      variant={state === "action_required" ? "danger" : state === "pending" || state === "concluding" ? "warning" : "signal"}
      dot
      data-testid="blackboard-conclusion-state"
      title={details.join(" · ")}
      className="min-w-0 shrink"
    >
      <span className="truncate whitespace-nowrap">{label}</span>
    </Chip>
  );
}
