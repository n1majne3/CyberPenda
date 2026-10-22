---
name: verify-accepted-steering
description: Checks that Accepted Steering is not replayed after a daemon crash. Use when changing Steering dispatch, the send-start fence, durable Steering transitions, startup recovery, or per-owner dispatch serialization.
---

# Verify Accepted Steering recovery

**Property:** The Harness sends each Accepted Steering request to the Runtime at most once across daemon crashes.
**Harm:** A replay can repeat a security-testing action that the operator accepted only once.

## Run the check

1. Read `GLOSSARY.md`, `docs/adr/0023-require-accepted-steering-to-settle-durably.md`, and `docs/agents/async-tests.md`.
2. Compare the covered code with the sync commit below. Include local changes. Review each changed function against the model actions before interpreting a pass:

   ```bash
   git diff b82d62365cbc6f41cf32dbb9285b2132b24c4e98 -- internal/daemon/steering.go internal/daemon/task_handlers.go internal/daemon/server.go internal/steering/steering.go internal/owner/steering.go internal/store/store.go internal/daemon/finish_task_resume_test.go
   ```

3. From the repository root, run:

   ```bash
   uv run --no-project python -I verification/accepted-steering/check.py
   ```

   Requirements: Go as specified by `go.mod`, Java 8 or later, UV, and Python 3.11 or later. The first run downloads the pinned TLC 1.7.4 release (TLC 2.19), checks its SHA-256, and caches it in the system temporary directory. Later runs use that cache. No Python packages are required. No Model Provider or Sandbox is used.
4. A pass requires both TLC and the real Go crash-cut tests to pass. Expect 159 distinct model states at the checked-in bounds and eight Go subtests. The normal run took about 40 seconds on the validated Windows machine, including Go build overhead; first-time compilation can take longer.
5. After changing the model, runner, test fixture, or covered ordering, run the sensitivity check:

   ```bash
   uv run --no-project python -I verification/accepted-steering/check.py --self-test
   ```

   It must find `Invariant NoReplay is violated.` in the model mutant and `NO_REPLAY:` in the real-code mutant, then pass the unmodified model and code. Expected mutant failures are printed; only the final exit status decides whether the sensitivity check passed.

## Scope

The model explores every reachable ordering for **one Runtime Owner, two distinct accepted requests, and at most two crashes**, including a crash during recovery. There is one dispatcher. SQLite commits are atomic and durable. A Runtime receives each dispatcher call once. Dispatch starts again only after startup recovery and a Runtime becomes available.

The Go test uses the real dispatcher, SQLite store, and startup recovery with fake delivery callbacks. It reopens the database at four crash cuts for each of two requests. It does not start a Runtime. A Go build overlay supplies the test and scratch mutation; existing workspace files are read-only.

This is bounded model checking, not a proof of all Go code. It does not prove eventual delivery, FIFO, simultaneous dispatcher exclusion, Runtime-internal retries, network delivery semantics, multi-daemon safety, SQLite power-loss behavior, or Session-specific lifecycle behavior. A post-fence request can have **zero** deliveries and still correctly require operator action.

## Coverage record

Sync commit: `b82d62365cbc6f41cf32dbb9285b2132b24c4e98`

| Code | Model action or assumption |
| --- | --- |
| `internal/steering/steering.go`: `Accept` | `Accept`: one committed record and queue order |
| Same file: `OldestPending` | `SelectPending`: select the oldest pending row |
| Same file: `MarkDispatchStarted`, `transition` | `Fence`: commit `dispatch_started` before delivery |
| `internal/daemon/steering.go`: `dispatchAcceptedSteering` | `SelectPending`, `Fence`, `Send`, `Settle` ordering |
| `internal/steering/steering.go`: `MarkApplied`, `MarkActionRequired`, `NonTerminal`, `transition` | `Settle`, `RecoverFenced`: separate durable transactions |
| `internal/daemon/steering.go`: `recoverAcceptedSteering`, `recoverTaskPendingSteering`, `triggerAcceptedSteeringDispatch` | `RecoverFenced`, `ResumeDispatch`; pending requests wait for a live Runtime |
| `internal/daemon/server.go`: `NewServer` | `Restart`: run recovery before returning the server |
| `internal/daemon/task_handlers.go`: `enqueueProviderTaskControlWithSettlement` | Assumed single dispatcher per owner; not proved by this model |
| `internal/owner/steering.go`: `SteeringState`, `Terminal` | Durable state vocabulary |
| `internal/store/store.go`: `Open` | SQLite transaction and single-daemon storage assumptions |

The test reuses `newFinishTaskFixtureAt` in `internal/daemon/finish_task_resume_test.go`. Review fixture changes too. The model and runner live in `verification/accepted-steering/`.

## Diagnose and maintain

- **TLC counterexample:** translate its `phase`, `durable`, and `deliveries` into code steps. Check every atomic boundary and assumption against the coverage record. See `verification/accepted-steering/README.md` for the expected replay trace and limits.
- **Go failure:** use the named crash cut and durable outcome. A compiler, download, checksum, or timeout error is a tool/setup failure, not a counterexample. The runner never counts such an error as a successful mutation check.
- **Model/code mismatch:** correct the mapping and rerun both checks. Do not treat a model pass as verification of changed code before this review.
- **Real defect:** preserve the counterexample and report it. Do not weaken the property or fix product code unless requested.
- **Intended protocol change:** update the model, crash-cut test, and mutation together. Keep the fence-after-send negative control capable of failure. Run the sensitivity check and affected product tests.
- Move the sync commit forward **only after** confirming that every covered function still matches the model and assumptions at that commit. Record new results in the verification README. A green command alone is not that confirmation.

Use `/pstack:maintain-formal-verification-skill` when a relevant change did not recheck this mapping.
