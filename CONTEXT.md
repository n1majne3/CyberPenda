# Pentest Agent Context

This context defines the product and security-testing language for the local-first pentest agent. It is a glossary for shared domain terms, not an implementation spec.

## Language

**Pentest Agent**:
A local-first system that coordinates authorized security testing work for a defined **Project**.
_Avoid_: autonomous hacker, exploit bot

**Project**:
A bounded security-testing engagement with its own **Scope**, tasks, memory, evidence, approvals, and report.
_Avoid_: workspace, conversation, campaign

**Project Defaults**:
Project-level choices for default runtime, runner, and task policy that do not copy global **Runtime Profiles**.
_Avoid_: project-local runtime profile, copied profile

**Project Dashboard**:
The primary project view that surfaces scope status, task runs, blackboard growth, findings, and evidence health.
_Avoid_: chat home, task-only queue

**Task**:
A user-goal-driven project run executed by one **Runtime Profile** through one **Runner**.
_Avoid_: chat message, report section, shell command, plan step

**Task Goal**:
The user's natural-language objective for a **Task**.
_Avoid_: raw prompt only, plan step

**Run Controls**:
The structured task launch settings that choose runtime, runner, mode, scope preview, and artifact behavior.
_Avoid_: hidden prompt flags, runtime internals

**Task Event**:
A structured timeline entry for a **Task**, including runtime output, status changes, startup checks, and task-local workflow markers.
_Avoid_: audit log entry, transcript line, raw output dump

**Task Conversation**:
The user-runtime interaction that continues inside one **Task** after launch.
_Avoid_: new task per reply, detached chat

**Task Summary**:
A runtime-maintained compact handoff view of a **Task** used to continue work without replaying every task event or conversation message.
_Avoid_: full transcript, raw event dump

**Task Summary Version**:
A historical revision of a **Task Summary** submitted by a runtime.
_Avoid_: separate task summary, transcript version

**Mechanical Handoff Packet**:
A daemon-assembled structured handoff view built from task state when no accepted **Task Summary** is available.
_Avoid_: LLM summary, canonical task understanding

**Scope**:
The asset boundaries and testing limits that define what the **Pentest Agent** is authorized to do within a **Project**.
_Avoid_: target list, allowlist, permission note

**Scope Expansion**:
A proposed change that adds a newly discovered asset or testing permission to an existing **Scope**.
_Avoid_: auto-enrollment, target drift

**YOLO-Derived Scope**:
Scope content added during **YOLO Mode** under an explicit project policy rather than a human **Approval**.
_Avoid_: approved scope, normal scope

**Out-of-Scope Fact**:
A **Project Fact** about an asset or action outside current **Scope** that is retained for context but not authorization.
_Avoid_: hidden target, pending target

**Scope Snapshot**:
An immutable copy of **Scope** captured when a **Task** starts.
_Avoid_: current scope, cached target list

**Runtime**:
The local agent CLI or assistant process scheduled to perform one **Task**.
_Avoid_: pentest agent, model, provider, worker

**Runtime Harness**:
The daemon-managed control wrapper that launches, resumes, steers, and stops a **Runtime** for one **Task**.
_Avoid_: pentest tool executor, agent brain, sandbox

**Harness Steering**:
A task-local control action that changes how the **Runtime Harness** continues a **Task** without creating a new task.
_Avoid_: direct tool control, hidden prompt mutation, new task

**Runtime Continuation**:
The next unit of runtime progress after launch, user input, checkpoint, interrupt, or resume.
_Avoid_: live thought editing, new task

**Runtime Profile**:
A global user-editable configuration that chooses how a **Runtime** should run for a task without storing secret values.
_Avoid_: preset, account, credential bundle, secret store

**Runtime Plugin**:
A declarative provider definition that describes how a **Runtime Profile** launches, projects config, validates startup, and selects transcript parsing for a runtime family.
_Avoid_: executable extension, marketplace package, project-local runtime profile

**Runtime Plugin Manifest**:
The JSON configuration document that defines one **Runtime Plugin**.
_Avoid_: secret config, arbitrary code, shell script

**Runtime Plugin Registry**:
The daemon-owned catalog of built-in and explicitly trusted **Runtime Plugin Manifests**.
_Avoid_: remote plugin store, package manager

**Runtime Plugin Primitive**:
A built-in daemon implementation named by a **Runtime Plugin Manifest**, such as a config projection primitive or transcript parser.
_Avoid_: manifest code, user-provided hook

**Runtime Extension**:
A runtime-native plugin, skill, package, or configuration bundle that a selected **Runtime** consumes after **Config Projection** prepares it for a **Task**.
_Avoid_: runtime provider, daemon plugin, arbitrary host hook

**Runtime Extension Manifest**:
The declarative document that identifies a **Runtime Extension**, its compatible **Runtime Plugins**, source location, task-local projection target, and non-secret configuration.
_Avoid_: executable installer, credential file, remote marketplace listing

**Runtime Extension Projection**:
The task-local materialization of enabled **Runtime Extensions** into the selected **Runtime**'s home, config, skill, plugin, or MCP-compatible directories.
_Avoid_: host runtime mutation, global plugin install, profile edit side effect

**Profile Selector**:
A user-facing control for choosing or quickly switching the **Runtime Profile** used by a **Task**.
_Avoid_: raw config picker, provider switch only

**Generated Runtime Config**:
A previewable task-local config output produced from structured profile fields during **Config Projection**.
_Avoid_: source of truth, raw profile

**MCP Configuration**:
Structured runtime interface configuration that defines available project-facing MCP servers for a **Runtime Profile**.
_Avoid_: raw JSON blob, unvalidated tool config

**Trusted MCP Server**:
An MCP server allowed to act as a **Project Interface** for project state, memory, approval, evidence, or reporting.
_Avoid_: arbitrary MCP server, external tool server

**External MCP Server**:
A user-added MCP server that is available to a **Runtime** but is not trusted as a **Project Interface** by default.
_Avoid_: trusted project interface, built-in server

**Profile Config Import**:
An advanced action that parses edited runtime config back into structured **Runtime Profile** fields.
_Avoid_: raw config save, opaque override

**Task Runtime Configuration**:
The task-specific copy of runtime settings captured from a **Runtime Profile** for launching a **Task**.
_Avoid_: live profile reference, mutable profile, embedded secret

**Task Runtime Configuration Version**:
A historical task-specific runtime configuration captured for a **Runtime Continuation**.
_Avoid_: new task, mutable profile edit

**Runner**:
The execution boundary selected for a task's **Runtime**, not a pentest tool executor.
_Avoid_: executor, tool runner

**Sandbox Runner**:
The default **Runner** that runs a **Runtime** inside a **Sandbox**.
_Avoid_: kali runner, container runner

**Host Runner**:
An explicit opt-in **Runner** that runs a **Runtime** in the host environment instead of a **Sandbox**.
_Avoid_: default runner, unsafe shortcut

**Host Runner Activation**:
A recorded boundary decision to run a **Task** through the **Host Runner**.
_Avoid_: silent host fallback, implicit host run

**Sandbox**:
An isolated runtime environment used to separate task filesystem state, dependencies, runtime homes, and process environment from the host.
_Avoid_: jail, proxy, enforcement boundary

**Credential Reference**:
A non-secret pointer that lets a task receive required credentials without storing the secret in a **Runtime Profile**.
_Avoid_: credential value, embedded secret

**Credential Binding**:
A project-level mapping from a **Credential Reference** to the credential source used for that **Project**.
_Avoid_: embedded secret, copied credential

**Global Credential Binding**:
A default credential mapping used when a **Project** does not override a **Credential Reference**.
_Avoid_: hidden credential, project credential

**Credential Binding Mode**:
The project setting that chooses whether a **Credential Reference** uses the global default binding or a project override.
_Avoid_: implicit credential behavior, hidden override

**Disabled Credential Binding**:
A project override that explicitly prevents a **Credential Reference** from using any credential source.
_Avoid_: missing binding, broken secret

**Config Projection**:
The task-local preparation of runtime configuration from a **Runtime Profile** and **Credential References**.
_Avoid_: host config edit, config sync

**Preflight**:
A recorded startup check phase that determines whether a **Task** can launch its **Runtime**.
_Avoid_: runtime execution, pentest work

**Project Interface**:
A supported channel that lets a **Runtime** read or write project state, memory, approvals, evidence, and reports.
_Avoid_: backdoor, low-level database access

**CLI Fallback**:
A command-line **Project Interface** used when the primary agent integration is unavailable or unreliable.
_Avoid_: bypass, debug-only path

**Blackboard**:
The project-local memory that stores durable facts, relationships, findings, and evidence for one **Project**.
_Avoid_: chat history, notes database

**Project Fact**:
A stable, project-scoped assertion that can be reused by later tasks without carrying raw proof content.
_Avoid_: observation, memory blob

**Fact Key**:
A stable project-local identifier used to update the same **Project Fact** over time.
_Avoid_: database ID, fact summary

**Fact Version**:
A historical revision of a **Project Fact** created when a **Fact Key** update changes its content or confidence.
_Avoid_: separate fact, duplicate fact

**Fact Merge**:
A governed cleanup action that consolidates duplicate or overly narrow **Project Facts** under a canonical **Fact Key**.
_Avoid_: silent deletion, overwrite

**Fact Key Alias**:
A historical **Fact Key** that redirects to the canonical **Fact Key** after a **Fact Merge**.
_Avoid_: duplicate key, deleted key

**Deprecated Fact**:
A **Project Fact** that remains historically available but should not be treated as current truth.
_Avoid_: deleted fact, stale note

**Current Truth**:
The default working set of non-deprecated **Project Facts** used by runtimes, UI views, and reports.
_Avoid_: absolute truth, all facts

**Fact Index**:
A compact view of **Current Truth** that exposes fact keys, categories, summaries, confidence, and scope status without full fact bodies.
_Avoid_: blackboard dump, full memory

**Tentative Fact**:
A reusable **Project Fact** that is plausible but not yet confirmed.
_Avoid_: task noise, confirmed fact

**Confirmed Fact**:
A **Project Fact** supported by evidence, reproduction, human confirmation, or independent corroboration.
_Avoid_: model assumption, unverified claim

**Fact Relation**:
A typed link that explains how one **Project Fact** relates to another.
_Avoid_: finding relation, edge, attack graph link

**Attack Chain**:
A narrative path that connects **Project Facts** and **Findings** into an explainable security-testing story.
_Avoid_: attack graph, exploit graph

**Finding**:
A reportable security issue with severity, proof, impact, recommendation, and status.
_Avoid_: vulnerability, vulnerability record, bug

**Finding Key**:
A stable project-local identifier used to update the same **Finding** over time.
_Avoid_: fact key, database ID, finding title

**Finding Version**:
A historical revision of a **Finding** created when a **Finding Key** update changes its content, status, severity, or confidence.
_Avoid_: separate finding, duplicate finding

**Finding Merge**:
A governed cleanup action that consolidates duplicate **Findings** under a canonical **Finding Key**.
_Avoid_: silent deletion, overwrite

**Finding Group**:
A report or UI grouping of related **Findings** that keeps each **Finding** identity separate.
_Avoid_: finding merge, shared finding

**Finding Key Alias**:
A historical **Finding Key** that redirects to the canonical **Finding Key** after a **Finding Merge**.
_Avoid_: duplicate finding key, deleted finding key

**Confirmed Finding**:
A **Finding** supported strongly enough by confirmed facts or evidence to report as verified.
_Avoid_: suspected finding, tentative issue

**CVSS Vector**:
A structured vulnerability scoring vector used to derive a **Finding** severity.
_Avoid_: freeform severity note, gut-feel score

**CVSS Version**:
The scoring standard version used by a **CVSS Vector**, with v4.0 as the canonical version for new findings.
_Avoid_: implicit CVSS version, mixed scoring scale

**CVSS Pending**:
A **Finding** scoring state used when the issue shape is known but the complete **CVSS Vector** is not yet available.
_Avoid_: guessed severity, unscored confirmed finding

**Finding Update**:
A partial change to an existing **Finding** that preserves unspecified fields.
_Avoid_: full replacement, duplicate finding

**Evidence Artifact**:
A durable reference to raw or derived proof that supports a **Project Fact** or **Finding**.
_Avoid_: attachment, log dump, fact body

**Artifact Root**:
The managed local storage root for project or task evidence, logs, and generated files.
_Avoid_: arbitrary host path, download folder

**Task Artifact Root**:
A task-specific **Artifact Root** that preserves where a task's evidence, logs, and generated files came from.
_Avoid_: temporary folder, runtime workdir

**Runtime Workdir**:
The task-local working directory used by a **Runtime** while executing one **Task**.
_Avoid_: shared project workspace, artifact root

**Provenance**:
The source context that explains which task, runtime, runner, scope, mode, and evidence produced a project conclusion.
_Avoid_: metadata blob, audit log

**Approval**:
A recorded human decision for scope changes or high-risk security-testing actions.
_Avoid_: permission popup, confirmation

**High-Risk Action**:
A testing action that may cause disruption, privileged data access, authenticated impact, exploit validation, or other impact beyond ordinary enumeration.
_Avoid_: dangerous command, scary action

**Intended Action**:
A pre-action record of what a runtime plans to do and why before a high-risk or approval-relevant step.
_Avoid_: result log, after-the-fact note

**Policy Violation**:
A recorded workflow breach where a runtime performs or attempts an action outside the required scope, approval, or declaration process.
_Avoid_: approval, YOLO action, runtime error

**Reconciliation**:
A governed review action that accepts, rejects, or reclassifies state discovered outside normal **Project Interface** writes.
_Avoid_: silent import, automatic trust

**Reconciliation Candidate**:
Untrusted discovered state proposed for **Reconciliation**.
_Avoid_: accepted fact, imported evidence

**YOLO Mode**:
A task mode that skips high-risk approval waits while preserving audit records and scope boundaries.
_Avoid_: unrestricted mode, unsafe mode

**Audit Log**:
A chronological, append-only record of security-relevant project, task, approval, and report events.
_Avoid_: debug log, transcript

**Report**:
A deliverable generated from **Findings**, **Project Facts**, **Fact Relations**, **Evidence Artifacts**, and the **Audit Log**.
_Avoid_: transcript, export, source of truth

## Relationships

- A **Project** has exactly one current **Scope**.
- **YOLO-Derived Scope** is part of **Scope** but carries distinct **Provenance** from human-approved scope.
- An **Out-of-Scope Fact** does not change **Scope** and does not authorize testing.
- A **Project** may define **Project Defaults** for new **Tasks**.
- A **Project Dashboard** is the primary UI entry point for a **Project**.
- **Runtime Profiles** are global and reusable across **Projects**.
- A **Runtime Profile** selects one **Runtime Plugin** by plugin identifier.
- A **Runtime Profile** may include **Credential References** but not credential values.
- A **Runtime Plugin Manifest** may declare credential environment names but must not contain credential values.
- A **Runtime Plugin Manifest** is declarative and may reference only known **Runtime Plugin Primitives**.
- A **Runtime Plugin Registry** is the source of supported runtime provider identifiers.
- A **Runtime Extension** belongs to a selected **Runtime Plugin** and does not define a new runtime provider identifier.
- A **Runtime Extension Manifest** may declare compatibility, source paths, projection targets, and non-secret defaults but must not contain credential values.
- A **Runtime Profile** manages **Runtime Extensions** through structured controls rather than raw manifest JSON.
- A **Runtime Profile** may reference a manually entered **Runtime Extension** identifier, but task launch still requires the daemon **Runtime Extension Registry** to resolve it.
- **Runtime Extension Projection** happens during **Config Projection** and must not mutate host runtime plugin directories.
- A **Credential Reference** resolves first through **Credential Bindings**, then through **Global Credential Bindings**.
- A **Project** may define **Credential Bindings** that override **Global Credential Bindings** for **Credential References** used by global **Runtime Profiles**.
- **Credential Binding Mode** defaults to using **Global Credential Bindings** unless the user explicitly chooses a project override.
- A **Disabled Credential Binding** blocks fallback to **Global Credential Bindings**.
- A **Runtime Profile** may define a default **Runner** for new **Tasks**.
- A **Profile Selector** chooses a **Runtime Profile** while detailed edits happen through structured profile fields.
- A **Runtime Profile** uses structured fields as source of truth for **Generated Runtime Config**.
- A **Runtime Plugin** describes which structured fields a **Runtime Profile** exposes.
- A **Runtime Profile** manages **MCP Configuration** as structured entries with raw preview or import as compatibility support.
- **MCP Configuration** may include **Trusted MCP Servers** and **External MCP Servers**.
- An **External MCP Server** does not receive project write authority by default.
- An **External MCP Server** follows the same **Runner** and **Sandbox** environment as its **Runtime**.
- **External MCP Server** output enters the **Blackboard** only when a **Runtime** writes it through a trusted **Project Interface**.
- A **Profile Config Import** updates a **Runtime Profile** only when the edited config can be parsed into structured fields.
- A **Project** has zero or more **Tasks**.
- A **Task** starts from one **Task Goal** plus **Run Controls**.
- A **Task** chooses one **Runtime Profile** and one **Runner**.
- A **Task** has one **Runtime Harness** that controls runtime lifecycle for that task.
- A **Task** launches from its **Task Runtime Configuration**, not a live mutable **Runtime Profile**.
- A **Task Runtime Configuration** captures the selected **Runtime Plugin** identifier.
- A **Task Runtime Configuration** may include **Credential References** but not credential values.
- Editing a **Runtime Profile** does not change existing **Task Runtime Configurations**.
- A runtime-profile switch inside a **Task** creates a new **Task Runtime Configuration Version** for the next **Runtime Continuation**.
- A **Task** may contain internal steps, but those steps are not separate **Tasks**.
- A **Task** has zero or more **Task Events**.
- A **Task Conversation** belongs to exactly one **Task**.
- User messages and runtime replies in a **Task Conversation** are represented as **Task Events**.
- **Harness Steering** actions are represented as **Task Events**.
- A **Runtime Continuation** receives a **Task Summary** instead of a full task transcript by default.
- A **Runtime** may submit **Task Summary** updates through a trusted **Project Interface**.
- A **Task Summary** update is automatically accepted and preserved as a **Task Summary Version**.
- A **Mechanical Handoff Packet** is used for **Runtime Continuation** when no accepted **Task Summary** exists.
- A **Task Event** may summarize runtime output but should not store complete raw output dumps.
- **Harness Steering** may request **Run Controls** changes, but those changes apply only at a **Runtime Continuation** boundary.
- A **Task** has its own **Runtime Workdir**.
- **Tasks** do not share **Runtime Workdirs** by default.
- A **Runtime Continuation** after a runtime-profile switch does not inherit the prior runtime's **Runtime Workdir** by default.
- A **Task** may override its **Runtime Profile**'s default **Runner**, and that override is recorded in the **Audit Log**.
- A **Task** uses **Config Projection** to prepare runtime configuration without mutating host runtime configuration.
- A **Config Projection** failure belongs to the affected **Task** unless the **Runtime Profile** itself is explicitly invalid.
- A **Task** passes **Preflight** before its **Runtime** starts.
- A **Credential Reference** that cannot be resolved during **Preflight** prevents **Runtime** launch.
- A **Preflight** failure prevents **Runtime** execution and is recorded in the **Audit Log**.
- A **Task** runs under exactly one **Scope Snapshot**.
- A **Scope Snapshot** records historical authorization and does not change when current **Scope** later changes.
- A **Runtime** performs a **Task** but is not the whole **Pentest Agent**.
- A **Runtime Harness** launches, resumes, steers, and stops a **Runtime** without executing pentest tools itself.
- **Harness Steering** changes the next runtime continuation, not the **Task** identity.
- **Harness Steering** affects a **Runtime Continuation**, not an already-running internal reasoning step.
- A **Runtime** uses **Project Interfaces** to read or write **Project** state.
- A **CLI Fallback** has the same project semantics as any other **Project Interface**.
- Direct storage changes outside **Project Interfaces** require **Reconciliation** before they can affect **Current Truth** or a **Report**.
- A **Runtime** may propose a **Reconciliation Candidate** but must not automatically complete **Reconciliation**.
- A **Sandbox Runner** runs a **Runtime** inside a **Sandbox** and is the default **Runner**.
- A **Runner** may place a **Runtime** inside a **Sandbox**.
- A **Host Runner** runs outside a **Sandbox** and must be visible in the **Audit Log** and **Report**.
- A **Host Runner Activation** requires an **Approval** unless **YOLO Mode** is active for the **Task**.
- A **Host Runner Activation** in **YOLO Mode** still creates an **Intended Action** before launch.
- A **Sandbox Runner** failure must not automatically fall back to the **Host Runner**.
- A **Sandbox** isolates runtime environment state but does not imply full network or command enforcement.
- A **Blackboard** belongs to exactly one **Project**.
- A **Blackboard** contains zero or more **Project Facts**, **Fact Relations**, **Findings**, and **Evidence Artifacts**.
- **Blackboard** contents are not shared across **Projects** by default.
- All **Runtimes** in the same **Project** share the same **Blackboard**.
- A **Runtime** writes important **Project Facts** during a **Task**, not only at task completion.
- A **Project Fact** has exactly one **Fact Key** within its **Project**.
- A **Fact Key** identifies the same **Project Fact** across updates.
- A conflicting write to an existing **Fact Key** automatically updates that **Project Fact**.
- A **Fact Key** update may change a fact's confidence, including downgrading a **Confirmed Fact** to a **Tentative Fact**.
- A **Fact Key** update preserves prior content and confidence as **Fact Versions**.
- A **Fact Key** update with an empty body preserves the existing **Project Fact** body unless body clearing is explicit.
- A **Fact Merge** preserves history while moving duplicate meaning toward a canonical **Fact Key**.
- A **Fact Key Alias** does not create separate **Current Truth** from its canonical **Fact Key**.
- Reads or writes through a **Fact Key Alias** resolve to the canonical **Fact Key**.
- A **Project** has one project-level **Artifact Root**.
- A **Task** may have one **Task Artifact Root** under the project-level **Artifact Root**.
- A **Runtime Workdir** is task-local scratch space, while a **Task Artifact Root** stores retained task outputs.
- A **Deprecated Fact** remains in the **Blackboard** but is excluded from default current-truth views.
- **Current Truth** is derived from non-deprecated **Project Facts** and does not claim absolute certainty.
- An **Out-of-Scope Fact** may be part of **Current Truth** only with explicit scope status.
- A **Fact Index** may include **Out-of-Scope Facts** only with explicit non-actionable scope status.
- A **Tentative Fact** may be part of **Current Truth** when its uncertainty is explicit.
- A **Tentative Fact** becomes a **Confirmed Fact** by updating the same **Fact Key** when adequate support exists.
- A **Runtime** sees the **Fact Index** by default and fetches full **Project Fact** bodies on demand.
- A **Fact Relation** connects exactly two **Project Facts**.
- A **Fact Relation** does not directly connect **Findings**.
- A contradictory **Fact Relation** does not automatically turn either **Project Fact** into a **Deprecated Fact**.
- An **Attack Chain** uses **Project Facts**, **Fact Relations**, and **Findings** without becoming a separate graph source of truth.
- A stable **Attack Chain** summary is stored as a **Project Fact**.
- A **Finding** has exactly one **Finding Key** within its **Project**.
- A **Finding Key** identifies the same reportable issue across updates.
- A conflicting write to an existing **Finding Key** automatically updates that **Finding**.
- A **Finding Key** update preserves prior finding state as **Finding Versions**.
- A **Finding Merge** preserves history while moving duplicate issue meaning toward a canonical **Finding Key**.
- A **Finding Key Alias** does not create a separate current **Finding** from its canonical **Finding Key**.
- **Findings** on different assets or entry points remain separate and may appear in a **Finding Group** instead of a **Finding Merge**.
- A **Finding Group** may have aggregate severity without changing the severity of individual **Findings**.
- A **Finding** may be supported by zero or more **Project Facts** and **Evidence Artifacts**.
- A **Finding** uses a **CVSS Vector** to derive severity.
- A **CVSS Vector** records its **CVSS Version**.
- A **Finding** without a complete **CVSS Vector** is **CVSS Pending**.
- A **Confirmed Finding** must be supported by **Confirmed Facts** or **Evidence Artifacts**.
- A **Confirmed Finding** must have a complete **CVSS Vector**.
- A **Confirmed Finding** must include target, proof, impact, and recommendation.
- A **Finding Update** preserves unspecified fields while allowing the finding to be completed over time.
- A suspected issue becomes a **Finding** only when it has a target, entry point, impact hypothesis, and validation path.
- Marking a **Finding** as false-positive does not automatically turn supporting **Project Facts** into **Deprecated Facts**.
- **Project Facts**, **Findings**, **Evidence Artifacts**, and **Reports** carry or present **Provenance**.
- An **Evidence Artifact** supports interpretation but is not itself a **Project Fact**.
- **Fact Key** updates preserve existing **Evidence Artifact** links unless a later action explicitly changes them.
- An **Evidence Artifact** references content under an **Artifact Root**.
- An **Evidence Artifact** may reference a **Task Artifact Root** to preserve task provenance.
- **Runtime Workdir** files become **Evidence Artifacts** only when explicitly attached or retained.
- Complete raw runtime or tool output is stored as logs or **Evidence Artifacts**, not as **Task Events**.
- A **Scope Expansion** requires an **Approval** unless the **Project** explicitly allows that behavior for the active task mode.
- A rejected **Scope Expansion** may leave an **Out-of-Scope Fact** for context and audit.
- An **Approval** for a **High-Risk Action** includes an **Intended Action**.
- A **High-Risk Action** requires an **Approval** unless **YOLO Mode** is active for the **Task**.
- A **High-Risk Action** in **YOLO Mode** still creates an **Intended Action** before execution.
- A **High-Risk Action** without the required **Approval**, **YOLO Mode**, or **Intended Action** is recorded as a **Policy Violation**.
- A **Policy Violation** marks the affected **Task** but does not automatically pause the whole **Project**.
- Direct runtime writes to storage outside **Project Interfaces** are recorded as **Policy Violations** when detected.
- **YOLO Mode** changes approval waiting behavior but does not by itself change **Scope**.
- **YOLO Mode** does not bypass **Scope Expansion** approval unless the **Project** explicitly allows YOLO scope expansion.
- **YOLO-Derived Scope** must be visible in the **Audit Log** and **Report**.
- **YOLO Mode** records skipped high-risk approval waits in the **Audit Log** but does not create **Approvals**.
- An **Audit Log** records **Task** launches, approvals, YOLO use, blackboard writes, evidence changes, and report generation.
- **Task Events** explain what happened inside one **Task**, while the **Audit Log** records security-relevant history across the **Project**.
- An **Audit Log** is corrected by appending new events, not rewriting existing events.
- A **Report** presents project conclusions but is not itself the source of truth for **Findings** or **Project Facts**.
- A **Report** presents high-signal **Provenance** without expanding every **Task Event**.
- A **Report** distinguishes **Tentative Facts** from confirmed conclusions.
- A **Report** may include unconfirmed **Findings** separately from **Confirmed Findings**.

## Example dialogue

> **Dev:** "The Runtime discovered a new subdomain during a task. Should it write that straight into Scope?"
> **Domain expert:** "No. It should create a Scope Expansion request. If approved, the Project Scope changes and the Audit Log records the decision."

> **Dev:** "The Runtime confirmed SQL injection and saved the HTTP exchange. Is that a Project Fact or a Finding?"
> **Domain expert:** "Both can be involved: the reproducible issue is a Finding, and the reproduction context can be stored as Project Facts with Evidence Artifacts attached."

## Flagged Ambiguities

- "vulnerability" and **Finding** were used for the same reportable issue concept; resolved: use **Finding** as the product/domain term and reserve "vulnerability" for type names, schemas, or imported source terminology.
- **Approval** and **YOLO Mode** both touch high-risk actions; resolved: **Approval** means a human decision, while **YOLO Mode** records skipped waits in the **Audit Log**.
- **YOLO Mode** is not scope authorization; resolved: it does not bypass **Scope Expansion** approval unless the **Project** explicitly allows YOLO scope expansion.
- **YOLO-Derived Scope** is not human-approved scope; resolved: keep distinct provenance in audit and report output.
- **YOLO Mode** is not after-the-fact-only audit; resolved: record an **Intended Action** before each skipped high-risk approval wait.
- **Intended Action** is not YOLO-only; resolved: approval requests and YOLO-skipped approvals share the same planned-action record shape.
- **Policy Violation** is not an approval state; resolved: it records a workflow breach that may be detected after the fact.
- **Policy Violation** is not automatic project suspension; resolved: flag the affected task strongly and leave project-level pause decisions to a human.
- Direct storage mutation is not trusted project state; resolved: use **Reconciliation** before such content affects **Current Truth** or reports.
- **Reconciliation** is not runtime self-approval; resolved: runtime-discovered candidates stay untrusted until accepted by a human or explicit project policy.
- **Host Runner Activation** is not implicit host fallback; resolved: host execution requires approval or YOLO-scoped declaration and must be visible in audit and report output.
- **Sandbox Runner** failure is not permission to use **Host Runner**; resolved: host execution requires explicit **Host Runner Activation**.
- **Deprecated Fact** is not deleted history; resolved: deprecated facts remain available for audit, contradiction handling, and report context.
- **Current Truth** is not the whole **Blackboard**; resolved: it is the default working set that excludes **Deprecated Facts**.
- **Fact Index** is not a full blackboard dump; resolved: runtimes receive compact current context first and fetch full bodies only when needed.
- **Fact Index** visibility is not authorization; resolved: out-of-scope entries may be visible only when clearly marked non-actionable.
- **Project Fact** does not mean confirmed fact; resolved: reusable but uncertain observations are **Tentative Facts**, while non-reusable noise stays in **Task Events** or logs.
- **Fact Version** is not separate current truth; resolved: current views use the latest fact state while history remains inspectable.
- **Confirmed Fact** is not a model assertion; resolved: confirmation requires evidence, reproduction, human confirmation, or independent corroboration.
- **Fact Key** is not a database ID or summary; resolved: it is the stable project-local identity used for fact updates and deduplication.
- **Fact Key** conflict handling is automatic overwrite; resolved: a new write to the same key updates the existing **Project Fact** rather than creating a review queue.
- **Confirmed Fact** status is not permanent; resolved: later writes to the same **Fact Key** may change confidence when they do so explicitly.
- Empty fact body updates do not erase detail; resolved: preserve the existing body unless the write explicitly clears it.
- **Fact Key** generation is not fully automatic in MVP; resolved: runtimes may propose keys, while naming rules and merge workflows handle cleanup.
- **Fact Merge** is not silent overwrite; resolved: consolidation preserves history, provenance, and relation context.
- **Fact Key Alias** is not an independent fact identity; resolved: old keys redirect to canonical keys after merge and stop producing separate current truth.
- `contradicts` in a **Fact Relation** does not decide truth by itself; resolved: deprecating a fact requires an explicit judgment.
- **Fact Relation** is not a finding graph; resolved: relate **Findings** through supporting **Project Facts** and **Evidence Artifacts**.
- **Finding Key** is not a **Fact Key**; resolved: facts and reportable issues have separate stable identities.
- **Finding Version** is not a duplicate finding; resolved: current finding views use the latest state while history remains inspectable.
- **Finding Key** generation is not fully automatic in MVP; resolved: runtimes may propose keys, while naming rules and merge workflows handle cleanup.
- **Finding Merge** is not silent deletion; resolved: duplicate finding keys become aliases that preserve history and references.
- **Finding Merge** is not cross-asset grouping; resolved: related findings on different assets or entry points stay separate and can be grouped for presentation.
- **Finding Group** severity is presentation metadata; resolved: it does not overwrite individual finding severity.
- **Finding** severity is CVSS-derived; resolved: use **CVSS Vector** rather than freeform severity judgment.
- **CVSS Version** is explicit; resolved: use CVSS v4.0 as canonical for new findings while allowing v3.1 compatibility for import and export.
- **CVSS Pending** is not confirmed severity; resolved: findings can exist before scoring is complete, but confirmed findings need a complete **CVSS Vector**.
- **Finding** false-positive status is not automatic fact deprecation; resolved: deprecate supporting facts only through an explicit judgment.
- **Finding** does not always mean verified; resolved: use **Confirmed Finding** for findings supported strongly enough to report as verified.
- **Finding Update** is not full replacement; resolved: missing fields are preserved, but confirmed findings still require complete core fields.
- Weak issue suspicion is not automatically a **Finding**; resolved: keep vague leads as **Tentative Facts** until the issue shape is clear.
- Fact overwrite does not erase evidence history; resolved: existing **Evidence Artifact** links are preserved unless explicitly changed.
- Rejected **Scope Expansion** is not erased; resolved: retain context as an **Out-of-Scope Fact** without adding authorization.
- **Out-of-Scope Fact** can be current but not actionable; resolved: it may appear in **Current Truth** only with explicit scope status.
- **Attack Chain** is not a typed attack graph; resolved: it is a reportable narrative assembled from existing blackboard concepts.
- **Attack Chain** is not report-only inference; resolved: stable chain summaries are stored as **Project Facts** and reports assemble them into narrative form.
- **Sandbox** is not a complete enforcement boundary; resolved: use it for runtime environment isolation, not per-command or per-network authorization.
- **Blackboard** is project-local; resolved: cross-project reuse requires explicit import, template, or report reference behavior.
- **Blackboard** is not separated by **Runtime Profile**; resolved: runtime-specific source context is represented through **Provenance**.
- **Blackboard** writing is not only end-of-task summarization; resolved: runtimes should write durable facts during task execution, with final summaries used only as cleanup or gap filling.
- **Audit Log** history is append-only; resolved: corrections and reversals are represented as later events.
- **Scope Snapshot** is distinct from current **Scope**; resolved: task history uses the snapshot captured at task start.
- **Runtime Profile** default **Runner** is not final task truth; resolved: the **Task** records the actual **Runner** used.
- **Runtime Profile** edits are not retroactive task edits; resolved: existing tasks use captured **Task Runtime Configuration** unless explicitly refreshed with audit history.
- **Runtime Profile** is not project-local; resolved: profiles are global, while each **Task** captures the runtime configuration it actually used.
- Runtime-profile switching does not create a new **Task**; resolved: it creates a new **Runtime Continuation** with a new **Task Runtime Configuration Version**.
- **Project Defaults** are not copied **Runtime Profiles**; resolved: they select defaults while profiles remain global.
- **Project Dashboard** is not a chat-first view; resolved: the project home prioritizes scope, task runs, blackboard state, findings, and evidence.
- **Task Goal** is not the whole task configuration; resolved: natural-language goals are paired with visible **Run Controls**.
- **Harness Steering** is not direct pentest tool control; resolved: it controls runtime continuation inside the same **Task** through the **Runtime Harness**.
- **Runtime Continuation** is not live thought editing; resolved: steering applies at input, checkpoint, interrupt, or resume boundaries.
- **Harness Steering** is not silent run-control mutation; resolved: runner, profile, YOLO, or other run-control changes apply through explicit task events and only at continuation boundaries.
- **Profile Selector** is not raw configuration editing; resolved: switching profiles is fast, while editing profiles remains structured.
- **Generated Runtime Config** is not the editable source of truth; resolved: raw config preview and diff are derived from structured profile fields.
- **Profile Config Import** is not raw passthrough; resolved: edited config must round-trip into structured profile fields before saving.
- **MCP Configuration** is not raw JSON as source of truth; resolved: manage entries structurally and use raw JSON only for preview or import.
- **External MCP Server** is not a trusted project interface by default; resolved: project write authority belongs only to **Trusted MCP Servers** or explicitly granted interfaces.
- **External MCP Server** execution is not separately gated by the daemon; resolved: it follows the runtime's runner environment while project write authority remains controlled.
- **External MCP Server** output is not automatic memory; resolved: runtimes interpret and write useful results through trusted project interfaces.
- **Task Runtime Configuration** is not secret storage; resolved: task launch uses credential references and injection rather than persisted secret values.
- **Credential Reference** is not the credential itself; resolved: project bindings override global bindings, and either may supply the credential source.
- **Credential Binding Mode** is visible by default; resolved: projects default to the checked global binding path unless explicitly overridden.
- **Disabled Credential Binding** is not a missing credential; resolved: it is an explicit project decision that prevents global fallback.
- **Credential Reference** resolution failure is not runtime-discoverable; resolved: fail **Preflight** only when neither project nor global binding can resolve it.
- **Config Projection** is not host configuration management; resolved: it prepares task-local runtime configuration and does not edit host runtime configuration.
- **Runtime Plugin** is not arbitrary code; resolved: v0 runtime plugins are declarative manifests that reference built-in daemon primitives.
- **Runtime Plugin Manifest** is not secret storage; resolved: manifests declare credential names and requirements while credential values resolve through bindings.
- **Runtime Plugin Registry** is not a remote marketplace; resolved: built-ins load first, and external manifests require explicit local trust.
- **Runtime Extension** is not a **Runtime Plugin**; resolved: extensions are consumed by a runtime selected through a runtime plugin, while runtime plugins define the provider family itself.
- **Runtime Extension Projection** is not a global install; resolved: enabled extensions are materialized into the task-local runtime boundary.
- **Config Projection** failure is not automatically **Runtime Profile** invalidity; resolved: treat it as a **Task** startup failure unless validation proves the profile itself is invalid.
- **Preflight** failure is not **Runtime** failure; resolved: startup checks fail before the runtime performs task work.
- **CLI Fallback** is not a bypass; resolved: CLI writes carry the same validation, provenance, audit, and blackboard semantics as other project interfaces.
- **Task Event** and **Audit Log** are distinct; resolved: task events are task-local timeline entries, while audit entries are project-level security history.
- **Task Event** is not raw output storage; resolved: preserve full output through logs or **Evidence Artifacts** and keep the task timeline structured.
- **Task Summary** is not daemon-authored intelligence; resolved: runtimes maintain summary candidates, while the daemon stores and injects accepted summaries.
- **Task Summary** acceptance is automatic; resolved: the latest runtime-submitted summary is accepted while prior versions remain inspectable.
- **Mechanical Handoff Packet** is not an LLM summary; resolved: it is structured fallback context assembled without semantic summarization.
- **Provenance** is not the **Audit Log** itself; resolved: provenance is the source context attached to conclusions, while audit is chronological security history.
- **Report** provenance is summarized, not exhaustive; resolved: reports show the runner, scope context, approval or YOLO origin, and key evidence rather than every task event.
- **Tentative Fact** is visible current context, not confirmed conclusion; resolved: current views may include it with confidence while reports mark it separately from confirmed findings.
- Unconfirmed **Findings** are not confirmed report conclusions; resolved: reports may show them as needing validation outside the confirmed findings summary.
- **Runtime Workdir** is not shared memory; resolved: cross-task knowledge flows through **Blackboard** and retained artifacts.
- **Runtime Workdir** is not cross-runtime handoff state; resolved: runtime-profile switches pass context through **Blackboard**, summaries, and retained artifacts.
- **Runtime Workdir** is not automatic evidence capture; resolved: files become **Evidence Artifacts** only through explicit attach or retain actions.
