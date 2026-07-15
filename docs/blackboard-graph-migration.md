# Graph Blackboard compatibility retirement

- **Status:** superseded by ADRs 0003–0014; historical Blackboard v1 runbook; do not execute

> **STOP:** The commands and release gates below target Blackboard v1. Implement and migrate with the [Blackboard v2 specification](./specs/blackboard-v2-spec.md) and [v2 TDD plan](./specs/blackboard-v2-tdd-plan.md).

Release C removes legacy Blackboard writes only after every local retirement
gate passes. Until then, legacy writes remain available with deprecation
metadata. Compatibility reads and existing browser redirects remain available
through the additional stable-release window.

Once writes retire, HTTP callers receive `410 Gone` with the stable error code
`compatibility_removed`. MCP and CLI callers receive the same code and message.
The error's `replacement_operation` detail identifies the supported operation:

| Removed compatibility write | Replacement operation |
| --- | --- |
| Fact, Finding, merge, deprecation, or relation write | `blackboard apply` / `blackboard_apply` |
| Evidence attachment | `blackboard evidence retain` / `blackboard_retain_evidence` |
| Runtime Task Summary submission | `blackboard continuation finish` / `blackboard_finish_continuation` |

Graph-native writes and Finish are unchanged. Operator Task Summary versioning
is separate from Runtime summary compatibility and remains available.

The local-use observation period is 30 days. An operator may explicitly waive
only that period by supplying both `--blackboard-write-waiver-operator` and
`--blackboard-write-waiver-reason` (or the corresponding
`PENTEST_BLACKBOARD_WRITE_WAIVER_OPERATOR` and
`PENTEST_BLACKBOARD_WRITE_WAIVER_REASON` environment variables). The operator
identity and reason are recorded with the durable retirement decision. A waiver
does not bypass stable-release, Runtime adoption, Continuation, verification,
Health, frozen-table guard, or documentation gates.

## Release D read retirement

Release D records a separate durable read-retirement decision. The daemon
first records an eligible Release C write-retirement decision even when no
caller attempts a legacy write. Read retirement then requires that decision to
be at least 30 days old, bundled Web and CLI clients to use canonical
projections, and no compatibility read use in the latest 30 days. The optional
`--blackboard-read-waiver-operator` and `--blackboard-read-waiver-reason`
flags (or matching `PENTEST_BLACKBOARD_READ_WAIVER_*` variables) waive only the
duration and read-use observation gate; their identity and reason are stored.

After the durable read decision, legacy HTTP reads return `410
compatibility_removed`, legacy MCP read/report tools are not registered, and
legacy CLI aliases return canonical read guidance. Cheap browser bookmarks
still redirect to the canonical Blackboard views.

## Explicit legacy-table finalization

Finalization is offline-only and never runs at daemon startup. First export the
migration summary and mapping digest, verify the committed cutover, and confirm
that the verified backup is retained or that rollback is intentionally being
surrendered. Then run:

```sh
pentestctl --db pentest.db blackboard migration finalize-legacy \
  --artifact-root /path/to/artifacts \
  --cutover-id CUTOVER_ID \
  --mapping-digest MAPPING_DIGEST \
  --acknowledge-backup \
  --migration-summary-exported
```

The command requires durable Release D read retirement and a fresh successful
verification. Its numbered `finalize_legacy_transaction_1` SQLite transaction
atomically drops only frozen legacy Blackboard tables and guards, changes the
store epoch to `graph_v1_finalized`, and writes the migration audit row. Any
failed gate, lost conditional state transition, or audit failure rolls back the
entire transaction. Task, Continuation, Event, Summary, Scope-bearing Task
configuration, graph ledger, Health, mapping, compatibility-history, and
migration-audit data remain.
