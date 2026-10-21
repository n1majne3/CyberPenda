# Accepted Steering: no replay after a crash

The Harness must send each Accepted Steering request to the Runtime at most once across daemon crashes. A duplicate can repeat a security-testing action that the operator accepted only once.

The live contract is in [ADR 0023](../../docs/adr/0023-require-accepted-steering-to-settle-durably.md). This target was selected over Task Finish authority and Skill Publication: replay can repeat an external action, not only lose internal state, and the durable send-start fence is a precise boundary between SQLite and a Runtime.

## Run

From the repository root:

```bash
uv run --no-project python -I verification/accepted-steering/check.py
uv run --no-project python -I verification/accepted-steering/check.py --self-test
```

Use the [verification skill](../../.claude/skills/verify-accepted-steering/SKILL.md) to review code/model alignment before interpreting a pass. It holds the authoritative coverage record and sync commit.

The runner requires Go from `go.mod`, Java 8+, UV, and Python 3.11+. It uses only the Python standard library. It downloads the public `tlaplus/tlaplus` release `v1.7.4` (`tla2tools.jar`, TLC 2.19), with SHA-256:

```text
936a262061c914694dfd669a543be24573c45d5aa0ff20a8b96b23d01e050e88
```

The download is verified before execution. It is cached at `cyberpenda-tlc-1.7.4-936a262061c9/tla2tools.jar` under the system temporary directory. `--tlc-jar` accepts a local copy with the same checksum for offline use. Go dependencies must also be available offline. Nothing from this repository is uploaded.

Each invocation creates an isolated temporary directory for models, TLC modules/state, Go build overlays, and the scratch source. The runner removes only that directory when it exits. An existing cache entry with the wrong checksum causes an error; it is not overwritten. The new virtual Go test path must not already exist in the workspace.

## What TLC checks

`AcceptedSteering.tla` is a hand-written abstraction of the dispatch and recovery protocol, not a translation of Go. `AcceptedSteering.cfg` sets the bounds: one Runtime Owner, two request identities, and at most two crashes. TLC explores all reachable states within those bounds, including crashes before acceptance, between durable operations, and during recovery. It uses one worker and fixed fingerprint/seed settings.

`NoReplay` requires each entry in `deliveries` to be at most one. `deliveries` is external history: a daemon crash cannot erase a request already received by a Runtime. `durable` is the committed SQLite state. `phase` and `current` are volatile dispatch state and are lost on a crash.

Each `Accept`, `Fence`, `Settle`, and `RecoverFenced` action represents **one** committed transaction. A crash inside a transaction is represented by its before-commit or after-commit state. Recovery is per record, not one transaction over the whole queue. `Send` is a separate external event. It is never atomic with `Fence` or `Settle`.

The single dispatcher selects the oldest pending row. Continuation resolution is assumed to succeed and not change delivery authority in this scope. Startup recovery changes `dispatch_started` to `action_required`. It leaves `pending` eligible for later dispatch when a Runtime becomes available. Terminal outcomes are not replayed.

### Limits

- This is a safety check, not a liveness or exactly-once guarantee. A crash after fencing but before delivery leaves zero deliveries and `action_required`.
- The model assumes one daemon and one active dispatcher per owner. It does not prove the control mutex implementation.
- The model covers successful provider operations plus crashes, not Stop, Task Finish, Runtime errors, cancellation, HTTP acceptance/replay identity, or automatic mode fallback.
- One delivery callback represents one receipt at the Runtime boundary. Retries inside an SDK or Runtime, network ambiguity, and external action execution are not modeled.
- SQLite transaction atomicity and durable commit are assumptions, not a power-loss test. WAL/filesystem failure is outside scope.
- Bounds do not imply a theorem for arbitrary request counts or restart counts. TLC uses state fingerprints; its reported collision estimate is part of the run output.
- The Go bridge exercises a Task owner through the shared dispatcher. Session-specific lifecycle and real Runtime transport are outside its scope.

## Real-code bridge

`dispatch_test.go.txt` is installed as `internal/daemon/formal_accepted_steering_test.go` through Go's `-overlay` flag. It is not part of the normal Go suite. The test uses real `dispatchAcceptedSteering`, Steering service transactions, and `NewServer` startup recovery. Only Continuation resolution and Runtime delivery/projection callbacks are fake. No Model Provider, external command, or Sandbox is started.

For each of two accepted requests, the test forces these cuts:

| Cut | Expected resumed outcome |
| --- | --- |
| Before the fence | Request remains pending, then is delivered once |
| After the fence, before receipt | `action_required`, zero receipts |
| After receipt, before outcome commit | `action_required`, one receipt |
| After outcome commit | `applied`, one receipt |

A sentinel panic drops the dispatch stack at the cut. With no live Runtime or control goroutine, closing the server only releases services and SQLite; the test then reopens the same database through `NewServer`. This is a deterministic persistence-boundary simulation, **not** an OS process-kill or power-loss test. No sleeps select the disputed ordering. The final assertions check delivery counts, durable state, and the ambiguity reason; merely counting a dispatched call is not treated as durable completion.

## Negative control and counterexample

`--self-test` first tests a broken variant, then the unchanged baseline. It changes `FenceBeforeSend` to `FALSE` in a temporary model configuration. Separately, it compiles a temporary copy of the real `internal/daemon/steering.go` with the `MarkDispatchStarted` block moved after `adapter.execute`. The repository source is never edited. This is a real compiled source mutation, not a source-text check substituted for behavior.

Both variants must expose this sequence:

1. Accept request 1. Its durable state is `pending`.
2. Select request 1 and deliver it before recording the fence.
3. Crash before `MarkDispatchStarted` commits. The Runtime has one receipt; SQLite still says `pending`.
4. Restart. Recovery has no durable evidence of delivery.
5. Select the pending request again. The Runtime receives it a second time.

The model must report `Invariant NoReplay is violated.` The Go test must report `NO_REPLAY: request-1 delivered 2 times across after_send` (and likewise for request 2). Syntax errors, build failures, and timeouts do not count. The final unmodified checks must pass again.

## Validated result

On 2026-10-21 at the skill's sync commit, Windows 11 with Go 1.27.2, Python 3.13, and Java 8:

- Baseline TLC: **219 generated states, 159 distinct states, depth 17; no violation**.
- Baseline Go: **all eight crash-cut cases passed**.
- Broken model: **NoReplay violated**, with a nine-state counterexample.
- Broken Go source: **both after-send cases delivered the same request twice** and failed.
- Unmodified baseline passed after the scratch checks.

No current-code defect was found within this scope. This does not clear the unmodeled paths listed above. The full backend suite and race detector are separate checks; this command does not run them.
