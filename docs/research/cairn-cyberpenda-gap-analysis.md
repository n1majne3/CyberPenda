# Research: Gap analysis Cairn graph vs CyberPenda

**Wayfinder ticket:** [#50 Research: Gap analysis Cairn graph vs CyberPenda](https://github.com/n1majne3/CyberPenda/issues/50)

**Map:** [#46 Map: Leverage Cairn graph base into CyberPenda](https://github.com/n1majne3/CyberPenda/issues/46)

**Date:** 2026-07-09
**License note:** Cairn is AGPL-3.0. This document compares architecture ideas and domain shapes only. Do not copy, vendor, link, or transliterate Cairn implementation code into CyberPenda unless a later license decision explicitly permits it.

This ticket resolves a gap analysis only. It does **not** make final adopt, adapt, reject, or defer decisions. The next grilling tickets decide where the high-leverage candidates belong in CyberPenda's domain model.

## Sources

| Source | Role |
|--------|------|
| [Research: Cairn Fact-Intent-Hint graph protocol](./cairn-fact-intent-hint-graph-protocol.md) | Cairn graph protocol: Fact, Intent, Hint, goal edge, leases, server non-responsibilities |
| [Research: Cairn Bootstrap-Reason-Explore orchestration](./cairn-bootstrap-reason-explore-orchestration.md) | Cairn orchestration: Dispatcher, Worker, Bootstrap, Reason, Explore, YAML export, claim/write mechanics |
| [Research: CyberPenda Blackboard and Task surfaces today](./cyberpenda-blackboard-and-task-surfaces-today.md) | Current CyberPenda surfaces: Blackboard, Project Facts, Fact Relations, Findings, Evidence, Task lifecycle, steering, trusted interfaces |
| [Local-First Pentest Agent PRD](../product/prd.md) | Product boundary and non-goals, including no full typed attack graph in the first release |
| [MVP Scope](../product/mvp.md) | MVP scope, current implementation status, deferred capabilities |
| [Pentest Agent Context](../../CONTEXT.md) | Domain language for Project, Scope, Task, Blackboard, Project Fact, Finding, Evidence, Harness Steering, Attack Chain |

## One-line answer

Cairn's useful pressure on CyberPenda is not "copy the graph system." The leverage is a smaller set of planning shapes: an open exploration objective, a graph-informed context projection, a reason-like planning pass, and a clearer boundary between durable facts, reportable findings, task steering, and project strategy notes.

## Scope guardrails

- The envelope is **Blackboard** and **Task orchestration** only.
- Full concurrent multi-worker claim/heartbeat scheduling is outside the current adopt target. It can be named as a deferred option, not adopted here.
- CyberPenda remains a local-first **Pentest Agent** with Project Scope, Tasks, Findings, Evidence, and Reports. It should not become a general state-space search product.
- The PRD and MVP explicitly avoid a full typed attack graph in the first release. Candidates below must either fit that ceiling or be marked as deferred.
- Cairn terms are analysis inputs. CyberPenda decisions should prefer CyberPenda domain terms.

## Comparative matrix

| Axis | Cairn shape | CyberPenda today | Gap | Candidate leverage |
|------|-------------|------------------|-----|--------------------|
| Knowledge node | Append-only `Fact` nodes, including `origin` and `goal`; no confidence, key, deprecation, or versions | Keyed **Project Facts** with category, summary, body, confidence, scope status, versions, aliases, and merge/deprecate behavior | Cairn facts are causal graph atoms; CyberPenda facts are reusable project assertions with lifecycle | Keep Project Fact lifecycle. Borrow only the discipline that task output should become concise reusable assertions, not raw transcript |
| Closed causal relation | `Intent` concludes by atomically minting one new Fact and closing an edge from source Facts | **Fact Relations** connect existing Project Facts; no version table; no direct Finding links | CyberPenda can express "fact A supports/leads to fact B," but not "this open objective will produce a future fact" | Consider stronger relation vocabulary or provenance for derived facts, while keeping Fact Relation closed-world rather than open work |
| Open exploration work | Open `Intent` is a claimable frontier edge from one or more source Facts to unknown future Fact | **Task Goal** is a user goal for a Task; **Fact Relation** is between existing facts; **Attack Chain** is narrative | No first-class "open exploration objective" with source facts, status, and eventual conclusion | Candidate new domain concept for [#51](https://github.com/n1majne3/CyberPenda/issues/51): an Intent-like **Exploration Objective** or equivalent, not necessarily with leases |
| Frontier | Unclaimed open Intents form the takeable frontier | Task list exists, but tasks are user-goal-driven runs, not graph-frontier items | No graph-derived queue of unresolved investigation directions | A lightweight frontier view could be generated from open exploration objectives or high-signal progress facts without becoming a scheduler |
| Strategy note | `Hint` is off-graph, writable even when stopped/completed, and guides future reasoning | **Harness Steering** is task-local continuation control; **Project Fact** is durable reusable assertion; conversation/task notes are not a project-level primitive | Strategy notes risk polluting Current Truth if stored as facts, or losing project scope if kept only as task steering | Candidate for [#52](https://github.com/n1majne3/CyberPenda/issues/52): project strategy notes separate from facts, or a constrained fact category with explicit non-truth semantics |
| Reasoning pass | `Reason` reads the graph, decides complete vs next Intent vs no-op, and uses a project-level reason lease | Operator/runtime can launch or continue Tasks; daemon does not summarize semantically; no reason lease | No first-class graph-informed "what next?" pass | Candidate for [#53](https://github.com/n1majne3/CyberPenda/issues/53): a Task pattern or operator action, not daemon-as-LLM-summarizer |
| Exploration execution | `Explore` claims one Intent, runs a Worker, writes one Fact conclusion | A **Task** can write many facts/findings/evidence and maintain a conversation, workdir, events, continuations, summaries | One-Cairn-Fact conclusion does not fit CyberPenda's richer output model | Borrow "one objective, explicit conclusion" as a task contract, but allow multiple Project Facts, Findings, Evidence Artifacts, and a Task Summary |
| Completion | Completion is a graph edge into special `goal` Fact | Task status, reports, confirmed findings, and attack-chain summaries exist; no single project completion edge | No compact graph proof that a project goal is satisfied | Candidate: a project outcome / attack-chain summary Project Fact, not necessarily a goal edge |
| Context projection | YAML export projects facts, intents, hints to the Worker; coordination leases are omitted | Runtimes receive compact Fact Index and task context; full fact bodies fetched on demand; handoff resume injects progress facts and findings | CyberPenda has compact context, but not a graph-shaped snapshot combining facts, relations, findings, and candidate objectives | High-fit candidate: a **Blackboard Snapshot** projection for planning/resume prompts |
| Coordination leases | Server enforces Intent worker claim and project reason lease | Runtime Harness controls a Task; no shared open-objective claim/heartbeat | No multi-runtime claim layer | Defer. Useful only if later map adopts multi-worker frontier execution |
| Two-phase salvage | Bootstrap/Explore can enter conclude fallback after timeout/parse failure and write a compact fact | Task Summary, Mechanical Handoff Packet, continuations, and native resume exist | Similar reliability need, different mechanism | Candidate: strengthen interruption/timeout contracts so runtimes submit task summaries or progress facts before/after interruption |
| Fact vs Finding | Cairn `Fact` can carry any objective conclusion; no Finding primitive | **Finding** is a reportable issue with target, proof, impact, recommendation, CVSS state, versions, aliases, evidence | Cairn's "fact conclusion" can accidentally collapse vulnerability evidence, assertion, and reportable issue | Do not map Cairn Fact directly to Finding. Exploration may produce facts, findings, and evidence separately |
| Attack Chain | Graph path is the reasoning artifact | **Attack Chain** is narrative built from Project Facts and Findings; stable summaries are Project Facts; not a separate graph source of truth | Intent-like edges could tempt a typed attack graph | Keep Attack Chain narrative-first unless a later decision deliberately revises the PRD ceiling |

## Sharp gap list

### Missing primitives

1. **Open Exploration Objective**

   CyberPenda has Task Goals and Fact Relations, but neither is an open, durable, source-fact-linked investigation direction. Cairn's Intent fills that gap. The CyberPenda-shaped question is whether an open objective should be its own domain concept, a Task planning affordance, or a constrained use of existing Project Facts.

2. **Frontier**

   Cairn can ask "what open Intents are unclaimed?" CyberPenda can list Tasks and facts, but it has no canonical "unresolved directions" view. A frontier does not have to imply a scheduler; it could be an operator-facing planning surface.

3. **Reason step**

   Cairn has a graph-driven Reason pass with a project lease. CyberPenda has runtime continuations and operator steering, but no named reason step that reads Blackboard state and proposes completion or next work. The key placement question is daemon, runtime, skill, or operator-only.

4. **Project strategy note**

   Cairn Hint is explicitly outside causal graph truth. CyberPenda's closest concepts each carry different semantics: Harness Steering changes one Task continuation, Project Fact joins Current Truth, and Task Conversation is local history. A project-wide strategy cue needs a home if we want Cairn-like Hints.

5. **Graph-shaped planning projection**

   CyberPenda has Fact Index and resume prompts, but no single projection that presents facts, fact relations, findings, evidence pointers, task progress, and open objectives as a compact planning board. This is likely the highest-leverage low-disruption gap.

6. **Explicit task-to-blackboard provenance edge**

   Cairn's concluded Intent gives a causal audit trail from source facts to the new fact. CyberPenda has Task Events, Fact Versions, Evidence Artifacts, and provenance language, but the current comparison does not show a first-class join from a Task/Continuation to each fact/finding/evidence write. That may matter if future planning depends on "what produced this assertion?"

7. **Completion proof**

   Cairn records complete as an edge into `goal`. CyberPenda has report generation and task status, but no single project-level outcome proof. The closest existing fit is a stable Attack Chain summary stored as a Project Fact.

8. **Claim/heartbeat coordination**

   Cairn's worker and reason leases are missing. The map already locks full multi-worker claim/heartbeat scheduling outside the near-term adopt target, so this should stay a deferred gap unless a later decision changes the envelope.

### Overloaded concepts to protect

1. **Project Fact vs Cairn Fact**

   Cairn Fact is append-only graph content. CyberPenda Project Fact is a keyed, reusable assertion with confidence, scope status, body, versions, aliases, and deprecation. Do not import Cairn's append-only model wholesale; it would discard CyberPenda's current truth and correction semantics.

2. **Project Fact vs Finding**

   A Finding is a reportable issue with target, proof, impact, recommendation, CVSS state, severity, versions, aliases, and evidence support. A Cairn exploration conclusion may become supporting Project Facts, a Finding, or Evidence Artifacts, but those are separate CyberPenda writes.

3. **Fact Relation vs Intent**

   A Fact Relation links existing facts. A Cairn Intent is open work that points to a not-yet-known conclusion. Encoding open work as ordinary Fact Relations would blur Current Truth with "to investigate."

4. **Task Goal vs Intent**

   A Task Goal is the user's natural-language objective for a Task. An Intent-like objective is a graph-informed direction that might become a Task Goal, but it is not the Task itself.

5. **Harness Steering vs Hint**

   Harness Steering affects a runtime continuation inside one Task. A Hint-like note is project-level strategy that may inform future work without changing current task controls.

6. **Task Summary vs Reason output**

   A Task Summary preserves context for continuation. A Reason output proposes completion or next directions after reading the board. They can feed each other, but they are not the same artifact.

7. **Attack Chain vs typed graph**

   Attack Chain is narrative and report-facing in the current domain model. Intent-like edges should not quietly turn it into a full typed attack graph.

## Candidate leverage points ranked by fit

Fit ranks measure how naturally the candidate fits CyberPenda's current domain model and first-release boundary. They are not adoption decisions.

| Rank | Candidate | Fit | Why it fits | Main risk / decision still needed |
|------|-----------|-----|-------------|-----------------------------------|
| 1 | **Blackboard Snapshot**: a graph-shaped planning projection over Fact Index, selected full facts, Fact Relations, Findings, Evidence pointers, progress facts, Task Summary, and maybe open objectives | High | Extends Compact Context First and existing resume prompts without changing truth storage | Decide exact contents and whether it is only prompt context or also UI/API |
| 2 | **Exploration Objective**: Intent-like open work item linked to source Fact Keys and eventual conclusion writes | High, if kept lightweight | Names the biggest structural gap without adopting the Cairn scheduler | [#51](https://github.com/n1majne3/CyberPenda/issues/51) must decide whether it is new concept, Task metadata, or encoded as facts |
| 3 | **Graph-informed planning Task / Reason step**: operator or runtime asks "are we done, what next?" from the Blackboard Snapshot | Medium-high | Fits Task orchestration and avoids daemon-as-LLM-summarizer if run as a runtime/operator action | [#53](https://github.com/n1majne3/CyberPenda/issues/53) must decide placement and output contract |
| 4 | **Stronger causal provenance** from Task/Continuation and source Fact Keys to blackboard writes | Medium-high | Reinforces evidence, audit, and resume quality; complements facts-before-reports | Needs schema/API decision; could become too graph-like if overloaded |
| 5 | **Project strategy notes** as Hint-like non-truth context | Medium | Solves the Hint vs Steering vs Fact confusion directly | [#52](https://github.com/n1majne3/CyberPenda/issues/52) must decide semantics, visibility, and whether notes survive reports |
| 6 | **Explicit exploration conclusion contract** for Tasks | Medium | Borrows Explore's "finish with an objective conclusion" without reducing CyberPenda output to one fact | Must not constrain normal Tasks that legitimately produce many facts/findings/evidence items |
| 7 | **Two-phase salvage on timeout/interruption** | Medium | Rhymes with Task Summary, Mechanical Handoff Packet, and native resume | Runtime adapter support varies; may be better as a best-effort convention |
| 8 | **Project outcome / completion proof fact** | Medium-low | Could strengthen Attack Chain and report readiness | Risk of duplicating report/task status; needs clear difference from report generation |
| 9 | **Bootstrap as special first-task mode** | Low-medium | Useful pattern for "try direct solve from origin/goal" | CyberPenda Tasks are already user-goal-driven; special bootstrap may add ceremony |
| 10 | **Claim/heartbeat frontier scheduler** | Low for this map | Cairn's strongest concurrency primitive, but outside the envelope | Defer unless future product direction adopts multi-runtime graph execution |

## How this should shape the next tickets

- [Grill: Where does exploration Intent live in our domain?](https://github.com/n1majne3/CyberPenda/issues/51) should start from the **Exploration Objective** gap and decide whether CyberPenda needs a new concept or can represent open directions with existing Task/Blackboard objects.
- [Grill: Hint vs Harness Steering vs Project Fact](https://github.com/n1majne3/CyberPenda/issues/52) should protect the truth boundary: strategy notes must not accidentally become Current Truth, and steering must remain task-local.
- [Grill: Bootstrap Reason Explore on our Task model](https://github.com/n1majne3/CyberPenda/issues/53) should decide whether Bootstrap/Reason/Explore are Task types, Task modes, prompt contracts, skills, or simply patterns for future UX.
- [Grill: Final adopt-adapt-reject-defer matrix and leverage spec shape](https://github.com/n1majne3/CyberPenda/issues/54) should use the ranked candidate list above as the input menu, not as pre-decided recommendations.

## Compact answer to the ticket question

CyberPenda's sharp gaps against Cairn are: no open exploration objective, no frontier, no reason step, no Hint-like strategy note, no graph-shaped planning snapshot, no explicit completion proof, no first-class task-to-blackboard causal edge, and no claim/heartbeat layer. The overloaded concepts to avoid are Project Fact vs Finding, Fact Relation vs Intent, Task Goal vs Intent, Harness Steering vs Hint, Task Summary vs Reason output, and Attack Chain vs typed graph. The highest-fit candidate leverage points are a Blackboard Snapshot, a lightweight Exploration Objective, a graph-informed planning Task/Reason step, and stronger causal provenance. Full claim/heartbeat scheduling remains a defer/out-of-scope candidate.

## Asset path

`docs/research/cairn-cyberpenda-gap-analysis.md`
