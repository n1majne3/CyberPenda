# Assisted Blackboard live validation

This acceptance run models an operator manually starting a normal CyberPenda
Task and then letting the Runtime perform one bounded Work Runtime Turn. The
Runtime may use browser, shell, or other testing tools, but it must not call a
Blackboard or trusted pentest persistence tool. The Runtime Harness detects the
semantic debt, runs a separate Conclude Runtime Turn, validates its closed
Attempt result, and applies it through Blackboard v2.

The existing `scripts/run-juice-shop-live.py` remains the interactive-write
smoke test: its Runtime is explicitly instructed to write Blackboard through
MCP. This document covers the complementary assisted path and does not change
the old script.

## Prerequisites

- `pentestd` is running and reachable through `PENTEST_DAEMON_URL` (default
  `http://127.0.0.1:8787`).
- A Runtime Profile whose Runtime Plugin advertises `assisted_conclusion` is
  already configured. Set its ID in `PENTEST_RUNTIME_PROFILE_ID`.
- Docker can start OWASP Juice Shop, or Juice Shop is already reachable through
  `PENTEST_JUICE_URL` (default `http://127.0.0.1:3000`).
- The selected runner can reach the Juice Shop URL. For a sandbox runner,
  configure the URL accordingly when localhost is not shared with the host.

Run the validator from the repository root:

```bash
PENTEST_RUNTIME_PROFILE_ID=<profile-id> \
PENTEST_RUNNER=sandbox \
python3 scripts/validate-juice-shop-assisted-live.py
```

Set `PENTEST_AUTH_TOKEN` to the configured daemon token or to the token from
the generated loopback operator access URL. The validator reads operator
Blackboard endpoints and does not use tokenless loopback authority. Optional
controls are `PENTEST_JUICE_TIMEOUT_SEC`, `PENTEST_JUICE_POLL_SEC`, and
`PENTEST_JUICE_URL`.

The script creates a fresh scoped Project and launches the Task with:

```json
{
  "run_controls": {
    "blackboard_conclusion_mode": "assisted"
  }
}
```

The Task Goal explicitly forbids Runtime Blackboard writes and Task Finish.
The validator then observes operator APIs until the conclusion applies or a
visible action-required receipt appears.

## Passing evidence

A successful manual assisted run requires all of the following:

- at least one non-Blackboard work event and one pending receipt for its Work
  Runtime Turn;
- a distinct Harness Conclude Runtime Turn in Timeline;
- a terminal Attempt at `attempt:juice-shop-assisted-score-board`;
- an applied Blackboard revision newer than the initial revision;
- Task status remains `running`;
- Runtime activity remains `live · idle` after reconciliation;
- no automatic Task Finish or Reason/Objective dispatch Timeline event;
- every observed completed Work Runtime Turn is covered by either an applied
  conclusion or a visible action-required receipt.

The JSON result deliberately contains no prompt, credential, raw tool output,
provider message, structured conclusion body, or reasoning. Coverage is derived
from Harness receipts, never a Juice Shop solved counter. A visible
action-required receipt counts as covered semantic debt but still fails this
happy-path run because no revision and terminal Attempt were applied.

`harness.conclusion_latency_ms` is derived separately from the pending and
applied/action-required Harness Timeline timestamps. Model usage is reported in
the portable `runtime_turns` unit, with operator Work Turns and automatic
control Turns in separate counters. The current Timeline API does not expose
provider token counts, so `provider_token_usage_available` remains false
instead of guessing or parsing raw provider output.

Run the deterministic validator tests without a daemon or Juice Shop:

```bash
python3 -m unittest scripts/validate_juice_shop_assisted_test.py
```

## Out of scope

- A CLI process started directly in a terminal, outside a CyberPenda
  Harness-owned Task and Task-scoped persistent Runtime, cannot be observed or
  made to write Blackboard by this feature. External manual CLI launch remains
  operator-managed.
- Assisted conclusion does not implement a full autonomous Cairn/Trellis-style
  Reason–Explore graph scheduler. It does not choose or dispatch the next
  Exploration Objective or another Work Runtime Turn.
- Assisted conclusion does not automatically finish a Task. Task Finish remains
  an explicit operator action.
- Interactive mode and Runtime Plugins without the complete assisted contract
  continue to use their existing behavior; the latter must reject or hide the
  assisted launch option rather than silently downgrade it.
