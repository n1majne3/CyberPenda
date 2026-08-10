# Defer Blackboard Finish until Work Runtime Turn settlement

## Status

Accepted

## Context

A Runtime can call Blackboard Finish and then continue source work in the same
Work Runtime Turn. Immediate closure makes later Blackboard writes fail and can
hide new semantic debt.

## Decision

`blackboard_finish` records a **Blackboard Finish Intent** during a Work Runtime
Turn. The intent closes the current Runtime Continuation's Blackboard write
protocol only when that Turn settles. Any later source work in the same Turn
invalidates the intent and continues to advance Semantic Debt Watermarks. The
tool reports `intent_recorded`, not `finished`. Invalidation produces a bounded
Runtime notice and requires a new finish intent.

## Consequences

Blackboard authority does not change in the middle of a Runtime Turn. A Runtime
must persist semantics after any work that invalidates its finish intent before
the Continuation can close cleanly. The tool response and Timeline state match
the actual write authority instead of reporting an early close.
