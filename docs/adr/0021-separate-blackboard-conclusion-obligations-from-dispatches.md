# Separate Blackboard conclusion obligations from dispatches

## Status

Accepted

## Context

A Pending Blackboard Conclusion can outlive the Runtime Continuation that first
created it. Rewriting one receipt to point at a replacement Continuation loses
the original delivery identity, but failing every such conclusion prevents
safe recovery.

## Decision

A **Pending Blackboard Conclusion** is the stable semantic reconciliation
obligation. A **Conclusion Dispatch** is one immutable delivery attempt bound to
one Runtime Continuation, source Runtime session, and source Runtime Turn
Selection. Runtime replacement or recovery creates a new Conclusion Dispatch
instead of changing an earlier dispatch. Only one dispatch for the obligation
may be active at a time. A new dispatch requires proven ownership of the current
Task-scoped Runtime and a writable replacement Continuation. It reuses the
source Runtime Turn Selection; failure to project that selection makes the
obligation operator-actionable. Automatic conclusion and repair budgets belong
to the obligation and do not reset for a new dispatch. Only the active dispatch
may validate or apply a result. A late result from an earlier dispatch remains
a terminal delivery outcome and cannot change Blackboard state.

## Consequences

The system retains true delivery lineage while the semantic obligation can
survive Runtime replacement. Dispatch recovery needs explicit terminal and
supersession states; code must not update the Continuation or source session of
an existing dispatch. Migration converts a legacy receipt into one obligation
and one immutable historical dispatch. It creates a safe replacement dispatch
only when current Runtime ownership and writable authority are proven;
otherwise it marks the obligation `action_required`. Operator actions are
reason-specific; an acceptance-ambiguous dispatch never offers generic Retry.
