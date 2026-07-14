# Graph Blackboard compatibility retirement

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
