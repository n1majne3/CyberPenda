# Research: Cairn Fact–Intent–Hint graph protocol

**Wayfinder ticket:** [#47 Research: Cairn Fact-Intent-Hint graph protocol](https://github.com/n1majne3/CyberPenda/issues/47)

**Map:** [#46 Map: Leverage Cairn graph base into CyberPenda](https://github.com/n1majne3/CyberPenda/issues/46)

**Date:** 2026-07-09
**License note:** Cairn is AGPL-3.0. This document records **protocol ideas and structure** from primary sources for comparison only. Do not copy or vendor Cairn source into CyberPenda.

## Sources (primary)

| Source | Role |
|--------|------|
| [docs/specs/server-protocol.md](https://github.com/oritera/Cairn/blob/main/docs/specs/server-protocol.md) | Canonical collaboration / graph protocol |
| [cairn/src/cairn/server/models.py](https://github.com/oritera/Cairn/blob/main/cairn/src/cairn/server/models.py) | Pydantic API models |
| [cairn/src/cairn/server/db.py](https://github.com/oritera/Cairn/blob/main/cairn/src/cairn/server/db.py) | SQLite schema |
| [cairn/src/cairn/server/services.py](https://github.com/oritera/Cairn/blob/main/cairn/src/cairn/server/services.py) | Consistency helpers, claim expiry, validation |
| [cairn/src/cairn/server/routers/intents.py](https://github.com/oritera/Cairn/blob/main/cairn/src/cairn/server/routers/intents.py) | Intent create / heartbeat / release / conclude |
| [README.md](https://github.com/oritera/Cairn/blob/main/README.md) | Product framing (state-space search, three primitives) |

Dispatcher Bootstrap/Reason/Explore **scheduling** is intentionally out of this ticket’s scope (see [#48](https://github.com/n1majne3/CyberPenda/issues/48)); only graph-facing protocol pieces that the server owns are covered here. The protocol doc’s “consumer flow” is summarized only where it illuminates graph semantics.

---

## One-line essence

Cairn models goal-directed exploration as a **directed acyclic graph of Facts connected by Intents**, plus **Hints** outside the graph. The **server does not reason or schedule**; it only maintains graph consistency and a thin layer of **coordination leases** (`intent.worker`, `project.reason`).

Source: protocol “本质” section; README blackboard framing.

---

## Design lineage (as stated by Cairn)

From `server-protocol.md`:

| Idea | Role in protocol |
|------|------------------|
| **Blackboard architecture** (Hearsay-II) | Multiple consumers read shared state and contribute knowledge; no central “brain” in the server |
| **Stigmergy** | Agents coordinate by changing the shared environment (the graph), not by direct messaging |
| **OODA per consumer** | Observe full graph → Orient → Decide (declare Intent) → Act (explore) → write Fact |
| **Mission command** | Goal is fixed; units decide locally; Hints act as commander’s intent |

Classic blackboard is “current state only.” Cairn’s Intents keep a **causal audit trail** so the graph is both knowledge store and reasoning path log.

---

## Primitives

### Project

A problem instance with a full graph.

| Field | Meaning |
|-------|---------|
| `id` | e.g. `proj_001` |
| `title` | Display name |
| `status` | `active` \| `stopped` \| `completed` |
| `bootstrap_enabled` | Whether consumers *may* run initial bootstrap (default true); server does not run bootstrap |
| `created_at` | Timestamp |
| `reason` | Optional **project-level lease** (not part of the fact graph) |

**Status semantics:**

| Status | Exploration writes (intents, conclude, complete, reason lease) | Hint writes | Notes |
|--------|------------------------------------------------------------------|-------------|--------|
| `active` | Allowed | Allowed | Normal search |
| `stopped` | Rejected (403) | Allowed | Hard stop: server clears open intent workers + reason lease immediately |
| `completed` | Rejected (403) | Allowed | Finish state; only `reopen` restores `active` |

`PUT .../status` only toggles `active` ↔ `stopped`. Completing is via `POST .../complete`, not status.

### Fact (graph node)

| Field | Meaning |
|-------|---------|
| `id` | `origin`, `goal`, or generated `f001`… |
| `description` | Objective text only |

**Rules:**

- **Append-only:** no update, no confidence, no deprecate, no categories, no fact keys.
- **No status markers** on facts. World changes are **new facts** (e.g. “shell obtained” then later “shell disconnected”); consumers interpret temporal order themselves.
- Descriptions should be **conclusions**, not raw dumps; large raw output should be referenced by path inside the description.
- Special facts at create time:
  - **`origin`** — starting situation (from create payload)
  - **`goal`** — success condition (from create payload)

Schema (`facts`): `(id, project_id)` PK, `description` only.

### Intent (graph edge / hyperedge)

| Field | Meaning |
|-------|---------|
| `id` | Generated `i001`… |
| `from` | One or more **source fact ids** (hyperedge) |
| `to` | Conclusion fact id, or `null` if still open |
| `description` | Exploration direction text |
| `creator` | Who declared the direction (immutable) |
| `worker` | Current claim holder / concluder (see below) |
| `last_heartbeat_at` | Last claim heartbeat; kept after release; used for timeout |
| `created_at` / `concluded_at` | Lifecycle timestamps |

**Three states (derived, not an enum field):**

| State | Condition |
|-------|-----------|
| Unclaimed open | `to == null` and `worker == null` |
| Working open | `to == null` and `worker != null` |
| Concluded | `to != null` (immutable thereafter) |

**Hyperedge:** multiple `from` fact ids share one description/creator/worker and one `to` fact — “several known facts jointly justified this exploration.”

**Constraints (enforced by server):**

- All `from` fact ids must exist in the project.
- **`goal` must not appear in `from`** (cannot explore “from” the goal).
- On create: `worker` must be `null` or equal to `creator`.
- Concluded intents cannot be heartbeated/released/re-concluded (409).

**Sources storage:** junction table `intent_sources (intent_id, project_id, fact_id)` — multi-from is first-class.

**Server does not type intents.** Reserved consumer conventions only, e.g.:

- `from = ["origin"]`, `description = "bootstrap"` — initial direct-push attempt
- `description = "external_feedback"` — written by reopen (see below)

### Hint (outside the graph)

| Field | Meaning |
|-------|---------|
| `id` | Generated `h001`… |
| `content` | Strategy / situational note |
| `creator` | Author |
| `created_at` | Timestamp |

**Rules:**

- **Not edges, not facts.** Do not participate in causal reasoning as graph structure.
- Writable on `active`, `stopped`, and `completed` projects.
- Uses: human strategy, cross-consumer **situation assessments** (e.g. “SSH path dead; focus upload”) to reduce graph-reading cost as the board grows.

### Project.reason (coordination only)

| Field | Meaning |
|-------|---------|
| `worker` | Who holds the reason lease |
| `trigger` | Consumer-defined reason for this reason pass |
| `started_at` / `last_heartbeat_at` | Lease lifetime |

**Not part of the fact graph.** Not included in YAML export. At most one reason lease per project. Used so two consumers do not run “reason over whole project” concurrently. claim / heartbeat / release API mirror intent leases.

---

## Intent lifecycle (API)

All exploration writes require `project.status == active` (else 403). Timeouts run on read/claim paths.

```
declare Intent (from[], description, creator, worker?)
        │
        ├─ worker null  → unclaimed open edge
        └─ worker=creator → claimed open edge
        │
        ▼
   heartbeat (claim or renew) ──► release (worker → null; last_heartbeat kept)
        │
        ▼
   conclude { worker, description }
        │  atomic:
        │  • insert Fact (new fNNN)
        │  • set intent.to = that fact
        │  • set worker, concluded_at
        ▼
   closed edge  from* ──intent──► fact
```

| Operation | Effect |
|-----------|--------|
| `POST .../intents` | Create open intent; optional immediate claim |
| `POST .../intents/{id}/heartbeat` | Claim if free, or renew if same worker; 409 if other worker holds unexpired claim |
| `POST .../intents/{id}/release` | Holder clears worker; 409 if held by other |
| `POST .../intents/{id}/conclude` | Atomically mint Fact + close edge |

**Claim rules on conclude/heartbeat:** unclaimed open intent is claimable by anyone; holder may act; foreign unexpired claim → 409.

**Timeouts (settings):**

| Setting | Default (schema) | Effect |
|---------|------------------|--------|
| `intent_timeout` | 15s in `db.py` (protocol examples often show 5) | Stale open intent `worker` → null |
| `reason_timeout` | 15s | Stale `project.reason` cleared |

Stopped projects: server **immediately** nulls all open intent workers and reason lease (no wait for timeout). Known protocol gap: after stop, prior worker identity on open intents is lost (no `worker_history`).

---

## Completion and reopen

### Complete

`POST .../complete` with `{ from: [fact_ids], description, worker }`:

1. Validates facts exist; `goal` not in `from`.
2. Creates a **concluded** Intent: `from` → **`to = goal`**, creator and worker both set to request `worker`.
3. Sets project `status = completed`.
4. Clears `project.reason`.

Completion is an explicit graph edge into the special `goal` fact, not a free-floating flag.

### Reopen

`POST .../reopen` (management op; only if `completed`):

1. Finds the **unique** completion intent (`to_fact_id = goal`); errors if missing/multiple.
2. **Deletes** that completion edge (no retained “was completed once” edge).
3. Inserts a new Fact with the correction description.
4. Inserts a concluded Intent: same `from` as the old completion edge → new fact, `description = "external_feedback"`.
5. Project back to `active`; reason cleared.

Used for external feedback (wrong flag, etc.): correction becomes a **fact on the graph**, not a silent status flip.

---

## What the server does **not** do

Explicit from protocol + implementation:

| Non-responsibility | Detail |
|--------------------|--------|
| **No reasoning** | Server never decides next intent, goal satisfaction, or fact quality |
| **No scheduling** | No Bootstrap/Reason/Explore dispatch inside server (dispatcher is a separate consumer) |
| **No fact merge/update/confidence** | Only insert; no versions, keys, aliases, deprecate |
| **No finding / evidence / scope types** | Graph is fact+intent+hint only |
| **No intent “types”** | bootstrap / external_feedback are consumer conventions |
| **No validation of description semantics** | Free text |
| **No multi-hop graph queries** | Full project payload or export; consumers interpret structure |
| **No direct agent-to-agent messaging** | Stigmergy via board only |
| **Container lifecycle** | Deleting a project does not stop worker containers; dispatcher notices deletion later |

Server **does**: id generation, FK existence checks, goal-not-in-from, claim mutual exclusion + timeouts, atomic conclude/complete/reopen rewrites, status gates, export snapshots.

---

## Read / export surfaces

| Surface | Contents |
|---------|----------|
| `GET /projects` | Summaries + counts (facts, intents, working, unclaimed, hints); no full graph |
| `GET /projects/{id}` | Full project + facts + intents + hints + reason lease |
| `GET .../export?format=yaml` | Graph snapshot for agents: origin/goal, facts, intents, hints. **Omits** `last_heartbeat_at` and `project.reason`. Open intents included. Timeout cleanup runs on export. |
| `GET .../export?format=timeline` | Ordered events: PROJECT CREATED, HINT, INTENT DECLARED, INTENT CONCLUDED, PROJECT COMPLETED. After reopen, prior PROJECT COMPLETED disappears with the deleted goal edge. |

---

## Mental model diagram

```
                    ┌──────── Hints (off-graph) ────────┐
                    │  human strategy / situation notes  │
                    └────────────────┬───────────────────┘
                                     │ read by consumers
┌────────────────────────────────────▼────────────────────────────────────┐
│                         Fact–Intent graph                               │
│                                                                         │
│   [origin] ──intent──► [f001] ──intent──► [f002] ─┐                     │
│                         │                         │                     │
│                         └──intent──► [f003] ──────┼──intent──► [goal]  │
│                                                   │     (complete)      │
│   open: [f002] ──intent (to=null, worker?)──► ?   │                     │
│                                                                         │
│   hyperedge: [f002]+[f004] ──intent──► [f006]                           │
└─────────────────────────────────────────────────────────────────────────┘
  project.reason lease  = concurrent “who is reasoning over the whole board”
  intent.worker lease   = concurrent “who is exploring this open edge”
```

---

## Implications for CyberPenda comparison (facts only; no adopt/reject)

These are **protocol contrasts** useful for later gap analysis ([#50](https://github.com/n1majne3/CyberPenda/issues/50)), not decisions:

1. **Graph growth unit** is Intent→Fact edges; our Blackboard centers on keyed **Project Facts** + typed **Fact Relations** + **Findings**/**Evidence**, not open exploration edges.
2. **Append-only facts** vs our upsert-by-**Fact Key**, confidence, deprecate, versions, aliases.
3. **origin/goal as special facts** vs **Scope** + per-**Task Goal** + report completion.
4. **Hint off-graph** vs **Harness Steering** / conversation / durable facts (no first-class Hint).
5. **Server is dumb graph store + leases**; our daemon owns richer project interfaces, preflight, runners, scope, findings validation.
6. **Stigmergic multi-consumer claim model** is core to the protocol; our envelope may **defer** multi-worker claim/heartbeat while still learning from Fact/Intent/Hint shapes.
7. **Complete as edge into `goal`** is a crisp success encoding; we lack an equivalent single graph completion edge.

---

## Answer to the ticket question (compact)

Cairn’s graph base is:

1. **Fact** — append-only objective node (`origin`/`goal`/generated); no mutation/confidence.
2. **Intent** — hyperedge from ≥1 facts; open (`to=null`) or concluded (`to=fact`); claim via `worker` + heartbeat/timeout/release; conclude atomically creates a Fact.
3. **Hint** — off-graph notes writable even when stopped/completed.
4. **Project status** — active/stopped/completed with hard gates; complete/reopen rewrite the goal edge.
5. **Leases** — intent worker + project.reason; not causal graph content.
6. **Server** — consistency only; **no reasoning, no role assignment, no fact quality, no scheduling.**

---

## Asset path

`docs/research/cairn-fact-intent-hint-graph-protocol.md`
