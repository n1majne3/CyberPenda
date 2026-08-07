# Require Accepted Steering to settle durably

## Status

Accepted

## Context

Saving a Steering Conversation Event does not preserve the in-memory provider
control operation. After daemon restart, such a request can remain pending
forever even though the API already returned `202 Accepted`.

## Decision

The Runtime Harness accepts Steering only after it has durable dispatch state.
Every **Accepted Steering** must eventually settle as `applied`, `failed`, or
`action_required`. Daemon restart resumes dispatch when safe or records a
terminal operator-actionable result; it never leaves Accepted Steering
permanently pending. Steering is first-in, first-out for each Runtime Owner, and
only one Steering dispatch for that owner may be active. The Harness records a
durable send-start fence before provider control: pre-fence work may be sent
after recovery, while a post-fence request with no result becomes
`action_required` and is not replayed automatically.

## Consequences

Accepted Steering needs a durable state machine and restart reconciliation.
Conversation Events remain projections of the request and outcome, not the
dispatch source of truth. Stop, Task Finish, and permanent Runtime loss settle
queued Steering explicitly instead of dropping it or leaving it pending. An
ambiguous post-fence request offers reason-specific operator actions, never a
generic Retry that could send the same control twice.
