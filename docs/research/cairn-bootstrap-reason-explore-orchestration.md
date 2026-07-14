# Research: Cairn Bootstrap–Reason–Explore orchestration

**Wayfinder ticket:** [#48 Research: Cairn Bootstrap-Reason-Explore orchestration](https://github.com/n1majne3/CyberPenda/issues/48)
**Map:** [#46 Map: Leverage Cairn graph base into CyberPenda](https://github.com/n1majne3/CyberPenda/issues/46)
**Date:** 2026-07-09
**License note:** Cairn is AGPL-3.0. This document records **architecture and protocol ideas** from primary sources for comparison only. Do not copy or vendor Cairn source into CyberPenda.

This ticket covers **how Cairn turns graph state into work** — the dispatcher's three task types, stigmergic coordination, the dispatcher/server split, claim-and-write mechanics, and YAML export as agent context. It deliberately stops short of adopt/reject; the graph-protocol primitives themselves were resolved in [#47](https://github.com/n1majne3/CyberPenda/issues/47), and gap analysis is [#50](https://github.com/n1majne3/CyberPenda/issues/50).

## Sources (primary)

| Source | Role |
|--------|------|
| [docs/specs/dispatcher-design.md](https://github.com/oritera/Cairn/blob/main/docs/specs/dispatcher-design.md) | Canonical dispatcher spec (本質, task model, scheduling, concurrency, workers, prompts) |
| [docs/specs/server-protocol.md](https://github.com/oritera/Cairn/blob/main/docs/specs/server-protocol.md) | Server-side graph + lease API the dispatcher drives (via [#47](https://github.com/n1majne3/CyberPenda/issues/47)) |
| [README.md](https://github.com/oritera/Cairn/blob/main/README.md) | Product framing: three task types, OODA worker loop, system architecture diagram |

All agent-facing prompt bodies (`bootstrap.md`, `reason.md`, `explore.md`, `explore_conclude.md`, `bootstrap_conclude.md`) are reproduced in the dispatcher spec's appendix and summarized below.

---

## One-line essence

The **Dispatcher** is Cairn's sole control plane and sole protocol writer. It reads graph state, decides which of three task types (`bootstrap` / `reason` / `explore`) to run, picks a **Worker** (an LLM CLI), renders a prompt, claims any lease needed, runs the Worker in a per-project container, parses its structured JSON, and writes the result back to the graph. **Workers never touch the Cairn API** — they only receive a prompt and return JSON.

Source: dispatcher-design.md "本質"; README "How It Works".

---

## Dispatcher vs Server (the split)

| | Cairn Server | Dispatcher |
|---|---|---|
| **Truth** | Source of truth for Project / Fact / Intent / Hint | A *client* of the server; holds no graph truth |
| **Responsibilities** | Store graph; maintain Intent claim/heartbeat/conclude; maintain project-level `reason` lease; status gates; atomic complete/reopen; export snapshots | Pull graph; choose task type + worker; manage containers + worker processes; sessions, timeouts, health checks, conclude fallback; **all** protocol writes |
| **Reasoning?** | No — consistency only | No — it *schedules*, the **Worker** reasons. Dispatcher parses JSON and maps outputs to protocol calls, but does not judge goal satisfaction or fact quality itself. |
| **Concurrency coordination** | Enforces lease mutual exclusion (intent `worker`, `project.reason`) + timeouts on read/claim | Enforces *local* admission caps, worker health windows, per-project task caps, single-dispatcher assumption |
| **Writes protocol?** | Exposes the API; applies validation | **Sole writer.** Agents do not claim, heartbeat, or call the API. |

Key quote (本質): "Agent 不直接认领 Intent，不直接 heartbeat，不直接调用 Cairn API。Agent 只接收 Dispatcher 下发的任务，返回结构化结果；Dispatcher 再决定如何请求 Server。"

This is a **hard three-tier split**: Server (graph truth) → Dispatcher (control plane, sole writer) → Worker (stateless reasoner, prompt in / JSON out).

---

## The three task types

All three are executed by the **same** Worker; the task type only changes the prompt, the input slice, the output contract, and which lease (if any) is claimed. From README "How It Works":

| Task | Trigger (summary) | Input | Output | Lease |
|------|-------------------|-------|--------|-------|
| **Bootstrap** | Project `active`; `bootstrap_enabled`; facts are only `origin`+`goal`; no normal intents | `{origin}`, `{goal}`, `{hints}` | `fact` + `complete` (if solved), or just `fact` (conclude phase) | Reserved `bootstrap` Intent |
| **Reason** | `active`; no unclaimed intents in project; no other `reason` running; first run or "new situation" re-trigger | `{graph_yaml}`, `{fact_ids}`, `{open_intents}` | `complete` **or** new `intent` **or** empty (no-op) | Project-level `reason` lease |
| **Explore** | `active`; a claimable unconcluded Intent exists | `{graph_yaml}`, `{intent_id}`, `{intent_description}` | One Fact description (the conclusion of that intent) | The target Intent's `worker` claim |

### Bootstrap — "try to solve it directly at the start"

- **When:** only at project initial state (facts == origin + goal; intents empty or only the reserved bootstrap intent).
- **Reserved bootstrap intent convention:** `description = "bootstrap"`, `creator = "dispatcher.bootstrap"`, `from = ["origin"]`. Dispatcher creates it if missing, then claims it via heartbeat.
- **Two-phase (like explore):**
  - **Phase 1** (`bootstrap`, `timeout`): the Worker tries to solve the whole problem. On success it must return **both** `fact` and `complete`.
  - **Phase 2** (`bootstrap_conclude`, `conclude_timeout`): entered only on timeout / parse-fail / contract-violation, on Workers that support session/conclude. Prompt explicitly says: *don't push further, don't wait for in-flight tasks, only summarize the key facts so far.* Returns only `fact` (never `complete`).
- **Writeback ordering:** if phase 1 returns valid `fact + complete` → Dispatcher `conclude`s the bootstrap intent (writes the Fact) **then immediately** calls `complete`. If phase-1 `complete` writeback fails, the already-written Fact survives and a later `reason` can still complete the project.
- **Failure:** both phases fail (or conclude writeback fails) → `release` the bootstrap intent, write nothing; project is retried as a fresh project next round.
- **Reads no graph YAML** — only origin/goal/hints. It is the only task that does not consume the graph snapshot.

### Reason — "read the whole graph; are we done? what's next?"

- **When:** `active`; **no unclaimed intents** in the project; **no other `reason`** running (enforced by the server's per-project `reason` lease); and either first run, or a **"new situation"** re-trigger.
- **"New situation" re-trigger (deliberately narrow):**
  - Fact count increased, **or**
  - Hint count increased, **or**
  - Project transitioned from "has open intents" → "no open intents".
  - **Not** a re-trigger: a single `explore` failure / dropped heartbeat / release while the intent stays open; nor "total intent count increased" (that is just last reason's new intent appearing).
- **De-dup checkpoint:** Dispatcher records (fact count, hint count, had-open-intents) at each successful reason; a baseline checkpoint is established for already-running projects on startup so the first new fact/hint isn't swallowed.
- **Output contract — exactly one of:**
  - `data.complete` `{from, description}` → Dispatcher calls `POST /complete` (with `worker` = the Worker's name).
  - `data.intent` `{from, description}` → Dispatcher calls `POST /intents` with `creator` = Worker name, `worker = null` (declared but unclaimed).
  - `data: {}` → write nothing this round.
  - Rule: if `{open_intents}` is empty and there is no `complete`, an `intent` is **required** (the graph must not stall with nothing in flight and no new direction).
- **Concurrency:** even with an in-flight `explore` in the same project, a `reason` may run **if** a new situation occurred — but not merely because the last reason just created a new intent. `reason` counts toward `max_project_workers`.
- **Failure:** timeout-only; any failure (timeout, `accepted:false`, non-zero exit, bad JSON, missing fields) → discard, write nothing, no immediate retry; `reason` lease is always released at task end if still held.

### Explore — "claim one intent, execute it, report one Fact"

- **When:** `active` and a claimable unconcluded Intent exists.
- **Fixed dispatch sequence:**
  1. Dispatcher selects a claimable intent.
  2. `POST /intents/{id}/heartbeat` as the **claim** — only after success…
  3. …does the Worker process actually start.
- **Output:** one objective fact conclusion (`data.description`). Even a null result must be an objective conclusion ("tested common SQLi payloads on /search, no exploitable injection"), never a refusal or empty.
- **Two-phase conclude fallback (Workers that support session):**
  - Phase 1 (`explore`, `timeout`): execute.
  - On success → `POST /conclude` (atomically mints Fact + closes the intent). No separate release.
  - On **timeout** or **parse/contract failure** (and only those) → enter `explore_conclude`: kill the process, **keep the same session id**, keep heartbeating, run the conclude prompt ("don't keep exploring, only summarize what's been done"). On valid output → `conclude`. On any second failure → discard, write nothing, `release` the intent.
  - Single-phase Workers (no session/conclude) go straight to failure on phase-1 timeout/parse-fail: kill, discard, release, log.
- **Hard-fail (never enters conclude):** `accepted:false`, non-zero exit, no readable output at all.
- **Healthcheck note:** the conclude fallback does **not** re-run the worker healthcheck — it goes straight to conclude and relies on JSON/schema validation.

---

## How workers claim intents and write facts (the precise mechanics)

The defining rule: **the Worker never calls the Cairn API.** All claims, heartbeats, concludes, completes, releases, and intent declarations are done by the Dispatcher, using the Worker's `name` as `creator`/`worker`.

| Worker action (logical) | Who actually calls the API | Call |
|-------------------------|----------------------------|------|
| "I will explore this intent" | Dispatcher, before launching the process | `POST /intents/{id}/heartbeat` (the claim) |
| "keep my claim alive" | Dispatcher, on `runtime.interval` tick | `POST /intents/{id}/heartbeat` |
| "here is my exploration conclusion" | Dispatcher, after parsing JSON | `POST /intents/{id}/conclude` (atomic Fact insert + edge close) |
| "I give up this attempt" | Dispatcher, on failure path | `POST /intents/{id}/release` |
| "I will reason over the project" | Dispatcher, before launching | `POST /reason/claim` |
| "keep my reason lease" | Dispatcher, on interval | `POST /reason/heartbeat` |
| "done reasoning" | Dispatcher, at task end | `POST /reason/release` |
| "goal is met" (from reason/bootstrap output) | Dispatcher | `POST /complete` |
| "propose a new direction" (from reason output) | Dispatcher | `POST /intents` |

**Why this matters for CyberPenda:** the Worker is a stateless prompt→JSON function. All graph-side state (claims, leases, atomic writes, dedup, ordering) lives in the Dispatcher + Server. A Worker crash loses nothing structural — the Dispatcher notices, releases the lease (or lets it time out), and the intent becomes claimable again.

**Heartbeat cadence is deliberately shared:** `runtime.interval` is both the main scheduler loop tick **and** the heartbeat period for claimed tasks (`bootstrap`, `explore`) — an explicit decision to avoid a second timing parameter.

---

## Stigmergy in this orchestration

Cairn's coordination is **stigmergic**: Workers/Dispatchers coordinate by *changing the shared graph*, not by messaging each other.

- Two Dispatchers are **not** supported against one server (local admission/health/dedup state doesn't cross processes) — but *conceptually*, the model is multi-consumer.
- The unit of coordination is the **Intent claim** (mutual-excluded by the server) and the **project `reason` lease** (at most one per project).
- A Worker "signals" by writing a Fact or declaring an Intent; the next reader sees it on the next graph read. There is no Worker→Worker channel.
- "New situation" detection in scheduling is stigmergy-aware: re-reasoning is triggered by *graph change* (new Fact/Hint, or open-intents→none), not by completion signals from peer tasks.

For CyberPenda this is the sharpest contrast: our envelope (map Notes) explicitly parks "full concurrent multi-worker claim/heartbeat dispatcher" as **defer/out-of-scope**, so the stigmergic *multi-agent* layer is the part we are least likely to adopt — even if we borrow the three-task-type *shape*.

---

## YAML export as agent context

`GET /projects/{id}/export?format=yaml` is **the** agent-context surface. The dispatcher spec is explicit that the dispatcher reads **two** kinds of data:

1. **Structured API** (`GET /projects/{id}`) — for scheduling, status, intent selection, and protocol writeback.
2. **`export?format=yaml`** — *only* for building the prompt's graph snapshot. It is **not** used for scheduling decisions.

What the YAML contains (per [#47](https://github.com/n1majne3/CyberPenda/issues/47), confirmed by the spec): origin/goal, facts, intents, hints. It **omits** `last_heartbeat_at` and `project.reason` (coordination leases are not agent context). Open intents are included. Timeout cleanup runs on export so stale claims don't leak into the prompt.

Prompt placeholders that consume it:
- `reason`: `{graph_yaml}`, plus `{fact_ids}` (JSON array) and `{open_intents}` (JSON array) as explicit navigational aids so the model doesn't have to re-derive valid fact ids.
- `explore` / `explore_conclude`: `{graph_yaml}`, `{intent_id}`, `{intent_description}`.
- `bootstrap` / `bootstrap_conclude`: **no** `{graph_yaml}` — only `{origin}`, `{goal}`, `{hints}` (JSON array).

So the YAML export is a **deliberately pruned, agent-safe projection**: causal graph content in, coordination leases and heartbeat noise out.

---

## Scheduling strategy

### Global (round-robin over running projects)

1. Running projects first, but only tasks that are *immediately dispatchable*.
2. If a running project is in initial state and can `bootstrap` → continue that.
3. Else if a running project has a dispatchable `explore` → continue that.
4. Else if `max_running_projects` has room → start one new project.

### Per-project (read full state, then order)

1. Initial state → choose `bootstrap` vs `reason` by `bootstrap_enabled` + worker capability (or continue an existing reserved bootstrap intent).
2. Else if "new situation" → `reason`.
3. Else if an unclaimed intent exists → `explore`.
4. Else idle this round.

### Concurrency caps

| Cap | Scope |
|-----|-------|
| `runtime.max_workers` | Total running tasks across the dispatcher |
| `runtime.max_running_projects` | Active projects admitted by this dispatcher (held while `active`, even if idle) |
| `runtime.max_project_workers` | Per-project running tasks — **counts bootstrap + reason + explore together** |
| `workers[].max_running` | Per-Worker (per LLM concurrency-quota unit) |
| Server-enforced | At most one `reason` per project; at most one claim per intent; bootstrap "one reserved intent + one bootstrap task" per project |

**Single-dispatcher assumption** is explicit and tested; multi-dispatcher against one server is **not** supported.

### Worker selection

Filter by task type → drop at-`max_running` → drop in local `retry_after` health window → min `priority` → fewest running → random. For `explore` and `bootstrap`, **claim succeeds before** the process starts; for `startup_and_task` healthcheck mode, a final healthcheck runs immediately before start and a failure benches the worker briefly.

---

## Worker / driver model

A **Worker** = one independent LLM concurrency-quota unit (one key = one Worker; you don't split one key across Workers). Drivers:

| Driver | Env | Two-phase conclude? | Session handling |
|--------|-----|---------------------|------------------|
| `claudecode` | `ANTHROPIC_MODEL/BASE_URL/AUTH_TOKEN` | Yes | Pre-generates session id; phase 2 uses `claude -r {session}` |
| `codex` | `CODEX_MODEL/CODEX_BASE_URL` + `OPENAI_API_KEY` | Yes | Session id printed on stderr, extracted via regex; phase 2 uses `codex exec resume {session}` |
| `mock` | `MOCK_*` JSON per phase | Yes | Structured JSON prompt (no NL); phase chosen from prompt field |

Dispatcher always takes **stdout全文** as the model output and JSON-parses it; it never inspects response bodies for healthchecks (exit code 0 only). Healthcheck = "is *this worker's* LLM config actually callable" (base URL reachable, key valid, model callable), not "is the container alive."

---

## Hard stop, reopen, delete

- Project → `stopped`: **hard stop.** No new tasks; cancel in-flight bootstrap/explore/reason immediately (no conclude fallback for cancelled tasks); subsequent rounds stop + kill the container.
- Project → `completed` then server `reopen`: Dispatcher treats it as a normal active project again (graph just has one more fact from reopen). If the container was queued for cleanup, it waits for that cleanup first to avoid racing.
- Project **deleted**: like stopped for task cancellation, but the orphan container is **removed**, not just stopped.

---

## What the dispatcher does **not** do

| Non-responsibility | Detail |
|--------------------|--------|
| **No reasoning** | It maps Worker JSON to protocol calls; it does not judge goal satisfaction, fact quality, or whether an intent is worth exploring. |
| **No multi-dispatcher** | Single instance assumed; local admission/health/dedup not cross-process. |
| **No Worker→Worker messaging** | Stigmergy only. |
| **No intent worker history** | Known limitation: after `stopped`, the prior claimant of an open intent is lost (server clears `worker`). A future `worker_history` field is the suggested fix, not a change to claim semantics. |
| **No in-band retry** | Writeback failures (403 on stopped project, etc.) are discarded with a log; no immediate retry. |
| **No prompt logic in the dispatcher** | Prompts are markdown shipped with the code, selected by `runtime.prompt_group`; dispatcher only renders placeholders. |

---

## Mental model diagram

```
        ┌──────────────── Cairn Server (graph truth, leases) ───────────────┐
        │  Facts · Intents (worker lease) · Hints · project.reason lease     │
        └────────▲──────────────────────────────────────────▲───────────────┘
                 │ structured API (scheduling + writeback)   │ YAML export (prompt context only)
                 │                                          │
        ┌────────┴──────────────── Dispatcher ──────────────┴──────────────┐
        │  sole protocol writer · picks task type · picks worker            │
        │  claims leases · renders prompt · parses JSON · manages lifecycle │
        │  sessions · timeouts · healthchecks · conclude fallback · caps    │
        └──────▲────────────────────────▲─────────────────────▲─────────────┘
               │ prompt + placeholder   │ prompt              │ prompt
        ┌──────┴────────┐      ┌────────┴─────────┐   ┌───────┴────────┐
        │ bootstrap     │      │ reason           │   │ explore        │
        │ Worker (CLI)  │      │ Worker (CLI)     │   │ Worker (CLI)   │
        │ JSON out      │      │ JSON out         │   │ JSON out       │
        └───────────────┘      └──────────────────┘   └────────────────┘
                  all inside one per-project container (toolchain + network + worker procs)
```

The Worker box is **stateless**: prompt in, JSON out. Everything structural (claims, atomic Fact-on-conclude, dedup, ordering) is Dispatcher + Server.

---

## Candidate analogies to CyberPenda (NOT decisions)

> The ticket asks for analogies only. Each row is a *candidate* mapping for [#50](https://github.com/n1majne3/CyberPenda/issues/50) gap analysis and the [#51](https://github.com/n1majne3/CyberPenda/issues/51)–[#53](https://github.com/n1majne3/CyberPenda/issues/53) grilling tickets to accept, reshape, or reject. CyberPenda terms per `CONTEXT.md`.

| Cairn concept | Candidate CyberPenda analogy | Why it's only a candidate |
|---------------|-------------------------------|---------------------------|
| **Dispatcher** (control plane, sole writer) | Our **daemon** (Preflight, Config Projection, Runtime Harness, Project Interfaces) | Daemon is richer (scope, runners, findings, credentials) and is not "sole writer" — runtimes write through **Project Interfaces**. |
| **Worker** (stateless prompt→JSON in a container) | Our **Runtime** (a CLI/assistant process under a **Runtime Harness**) | Runtimes are interactive and conversation-capable, not single-shot JSON; they own a **Runtime Workdir** and **Task Conversation**. |
| **Project container** (toolchain + network per project) | Our **Sandbox** under a **Runner** | We distinguish **Sandbox Runner** vs **Host Runner** and tie isolation to scope; Cairn's container is just the exec environment. |
| **Bootstrap** (try to solve directly at start) | A **Task** launched from a **Task Goal** with a fresh **Scope Snapshot** | We have no special "first task" role; every Task is goal-driven. Bootstrap's "fact+complete or conclude-only fact" contract has no direct equivalent. |
| **Reason** (read graph; done? / next intent?) | No first-class concept today. Closest: operator + runtime deciding next **Task**, or a graph-informed planning step | This is the most contested analogy — see [#53](https://github.com/n1majne3/CyberPenda/issues/53) and the map's "exact placement of any reason step" fog. |
| **Explore** (claim one intent, execute, one Fact) | A **Task** that pursues one objective and writes **Project Facts** / **Findings** / **Evidence** to the **Blackboard** | Our Task writes richer, keyed, versioned, confidence-bearing facts, plus findings/evidence; Cairn writes one append-only fact per conclude. |
| **Intent** (claimable exploration edge) | No direct equivalent. Closest: a **Task Goal** or a graph-informed "open objective" — possibly **Attack Chain** edges | Whether CyberPenda needs claimable, durable exploration edges is itself the open question ([#51](https://github.com/n1majne3/CyberPenda/issues/51)). |
| **Hint** (off-graph strategy note) | **Harness Steering**, operator notes, or a non-durable **Project Fact** | Already flagged as a grilling question ([#52](https://github.com/n1majne3/CyberPenda/issues/52)): Hint vs Harness Steering vs Project Fact. |
| **Stigmergy / multi-worker claims** | All **Runtimes** in a **Project** share one **Blackboard** | We share the blackboard, but have no claim/heartbeat/lease layer and the envelope defers one. |
| **YAML export** (agent-safe graph projection) | **Fact Index** + on-demand **Project Fact** bodies (what a Runtime sees by default) | We already expose an index-first view; whether we also export a graph-shaped snapshot is open. |
| **Complete = edge into `goal`** | A **Report** / project completion — but we have no single graph completion edge | [#47](https://github.com/n1majne3/CyberPenda/issues/47) flagged this crisp success encoding as something we lack. |
| **`bootstrap_enabled` / reserved bootstrap intent** | A **Project Defaults** flag? | Speculative; no current equivalent. |
| **Two-phase conclude fallback** | **Task Summary** / **Mechanical Handoff Packet** for **Runtime Continuation** | Loose: both are "salvage a compact conclusion from an interrupted run," but Cairn's is same-session conclude-on-timeout; ours is a handoff for the *next* continuation. |

These analogies intentionally over-map to make the contrasts visible; most will **not** survive gap analysis as adoptions.

---

## Implications for CyberPenda (facts only; no adopt/reject)

Protocol/orchestration contrasts useful for [#50](https://github.com/n1majne3/CyberPenda/issues/50), not decisions:

1. **Three-tier split (Server/Dispatcher/Worker)** vs our **daemon/runtime** two-tier with Project Interfaces. The "sole writer" dispatcher role has no clean CyberPenda counterpart — our daemon mediates but runtimes write through trusted interfaces.
2. **Worker = stateless prompt→JSON** vs our **Runtime = interactive, conversation-bearing, workdir-owning** agent. This is a deep architectural difference that shapes every analogy below it.
3. **Three task types as prompt+contract variants of one Worker** is a cheap, composable pattern. Our **Task** is a heavier, goal-driven unit; whether a Bootstrap/Reason/Explore-like *decomposition within or across Tasks* fits is open ([#53](https://github.com/n1majne3/CyberPenda/issues/53)).
4. **Reason as a graph-driven "what's next" step** is the concept with the weakest current CyberPenda home — and the one the map's fog explicitly calls out ("exact placement of any reason step").
5. **Explore's claim→execute→conclude lifecycle** maps loosely to Task launch→run→write-facts, but our lack of a claimable, durable, structured "intent" edge is the largest structural gap.
6. **Two-phase conclude fallback** is an interesting reliability pattern (salvage a fact from a timed-out run via same-session resume) that rhymes with our Task Summary / Mechanical Handoff Packet but serves a different purpose.
7. **YAML export as a pruned, agent-safe projection** rhymes with our Fact Index (index-first, bodies-on-demand) but is graph-shaped rather than keyed-fact-shaped.
8. **Stigmergic multi-worker coordination** is core to Cairn's identity and explicitly **deferred** by our envelope — so any adoption here is the "ideas, not the scheduler" slice.

---

## Answer to the ticket question (compact)

How Cairn turns graph state into work:

1. **Dispatcher vs Server** — Server is the dumb graph-truth + lease store; Dispatcher is the **sole** control plane and sole protocol writer; Workers are stateless prompt→JSON functions.
2. **Bootstrap** — initial-state direct-solve attempt; reserved intent; two-phase (solve→fact+complete, or conclude→fact only); reads only origin/goal/hints.
3. **Reason** — reads full graph; returns `complete`, or a new `intent`, or no-op; triggered only by "new situation" (new Fact/Hint, or open-intents→none); one per project via `reason` lease.
4. **Explore** — claims one intent via heartbeat, executes, concludes one Fact; two-phase conclude fallback on timeout/parse-fail for session-capable Workers.
5. **Stigmergy** — coordination through graph writes only; claim/lease mutual exclusion is the coordination primitive; no Worker→Worker messaging; single-dispatcher assumed.
6. **Claim & write** — Workers never call the API; Dispatcher claims (`heartbeat`/`reason/claim`), concludes (atomic Fact+edge), completes, releases, and declares intents, using the Worker's name as `creator`/`worker`.
7. **YAML export** — `export?format=yaml` is the agent-context projection (origin/goal/facts/intents/hints; omits leases + heartbeat); used **only** to build prompts, not for scheduling.

Candidate CyberPenda analogies are sketched above for gap analysis; **no adopt/reject decisions made in this ticket**.

---

## Asset path

`docs/research/cairn-bootstrap-reason-explore-orchestration.md`
