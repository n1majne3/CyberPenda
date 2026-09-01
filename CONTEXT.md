# Pentest Agent Context

This context defines the product and security-testing language for the local-first pentest agent. It is a glossary for shared domain terms, not an implementation spec.

## Language

**Pentest Agent**:
A local-first system that coordinates authorized security testing work for a defined **Project**.
_Avoid_: autonomous hacker, exploit bot

**Project**:
A bounded security-testing engagement with its own **Scope**, tasks, memory, evidence, and report.
_Avoid_: workspace, conversation, campaign

**Project Kind**:
The required Project classification of `pentest` or `ctf_challenge` that selects valid Project Knowledge and reporting semantics. It is chosen explicitly when the Project is created and is not inferred from a Task Goal, Skill, or Runtime output.
_Avoid_: hidden project default, task mode, runtime capability

**Project Kind Conversion**:
An explicit operator-confirmed change of one Project Kind after a preview proves that every Task is terminal and no incompatible current Finding or Solution exists. It changes classification only and never infers a cross-type Blackboard record conversion.
_Avoid_: automatic project repair, fact-to-solution migration, task mode switch

**Project Defaults**:
Project-level choices for default runner and task policy. They never select, copy, or imply a **Runtime Profile**.
_Avoid_: project-local runtime profile, copied profile, default profile, launch selection store

**Task Policy**:
Structured operator-defined limits that the **Runtime Harness** enforces for one Task, such as maximum challenge Attempts, wrong submissions, wall time, consecutive failures, Rating drawdown, or no-progress duration.
_Avoid_: prompt advice, Skill rule, hidden timeout

**Task Policy Snapshot**:
The immutable Task-local copy of Task Policy captured by **Task Launch** and used for deterministic enforcement and historical inspection.
_Avoid_: current project defaults, mutable runtime limit, prompt text

**Project Dashboard**:
The primary project view that surfaces scope status, task runs, blackboard growth, findings, and evidence health.
_Avoid_: chat home, task-only queue

**Task**:
A user-goal-driven project run executed from one **Runtime Configuration Snapshot** through one **Runner**.
_Avoid_: chat message, report section, shell command, plan step

**Task Type**:
The required operator-visible `pentest` or `ctf_challenge` classification selected during **Task Launch** and stored as an immutable Task-local snapshot. It must match the current **Project Kind** at launch. A later Project Kind Conversion does not rewrite an existing Task Type.
_Avoid_: hidden Project Kind lookup, mutable task mode, prompt label

**Non-Project Mode**:
The product mode for work that has no **Project** and therefore no Project Scope, while retaining the same Runtime interaction capabilities as a **Task**.
_Avoid_: unrestricted Project, temporary Task, separate chat product

**Session**:
A durable Non-Project owner of one persistent Runtime conversation, owner-local events, attachments, and workdir, with a self-contained Blackboard unless its **Blackboard Mode** is disabled.
_Avoid_: Project, Task, disposable chat, UI-only conversation

**Runtime Owner Workspace**:
The shared interactive UI used by both a Project **Task** and a Non-Project **Session** for conversation, timeline, Runtime activity, permissions, Blackboard conclusion state, attachments, and per-turn model selection. Owner-specific lifecycle actions and data adapters may differ without creating a separate workspace UI.
_Avoid_: Session-specific chat UI, duplicated Task page, shared domain aggregate

**Runtime Owner History Window**:
A view of the selected Runtime Owner's recent Timeline and Transcript that is bounded by both item count and serialized size. It keeps older history available on demand.
_Avoid_: complete initial transcript, cross-owner event cache

**Project Navigation Projection**:
A bounded navigation view of every **Project**, with a fixed recent **Task** summary plus the selected Task and every Task with a live busy Runtime.
_Avoid_: full Task inventory, request-count-only optimization

**Task Goal**:
The user's natural-language objective for a **Task**.
_Avoid_: raw prompt only, plan step

**Task Launch**:
The creation or continuation of a **Task** from **Run Controls**, resolved runtime configuration, selected **Runner**, **Scope Snapshot**, and startup checks.
_Avoid_: runtime projection, task adapter build, launch plumbing

**Run Controls**:
The structured task launch settings that choose **Launch Selection** or an optional **Runtime Profile**, runner, mode, scope preview, and artifact behavior.
_Avoid_: hidden prompt flags, runtime internals

**Runtime Launch Controls**:
The shared launch UI and controller used by both Project **Task Launch** and Non-Project **Session** creation for **Launch Selection**, an optional **Runtime Profile**, runner, model override, reasoning effort, skills preview, and **Preflight**. Owner-specific input labels, endpoints, and Project Scope behavior are adapters around this shared surface.
_Avoid_: Session launch form, duplicated launch picker, runtime profile-only Session creation

**Launch Selection**:
The primary task-launch choice of one **Runtime Plugin** family, one **Model Provider**, and an optional model for that launch.
_Avoid_: runtime profile picker, MCP preset, profile name

**Launch Model Override**:
A task-only model choice applied at launch that may differ from the selected **Runtime Profile**'s **Model Override** without editing that profile.
_Avoid_: profile edit, model provider edit, catalog refresh

**Launch Reasoning Effort Override**:
A task-only **Reasoning Effort** choice applied at launch that may differ from the selected **Runtime Profile** default without editing that profile.
_Avoid_: profile edit, runtime flag, model override

**Launch Configuration Resolution**:
The **Preflight** step that turns **Run Controls** into a **Runtime Configuration Snapshot**, either directly from **Launch Selection** or from a **Runtime Profile** selected for that launch.
_Avoid_: launch profile resolution, global profile matching, profile creation

**Runtime Configuration Snapshot**:
The immutable Runtime Owner-local settings captured for one launch or **Runtime Continuation**. It records the resolved Runtime Plugin, Model Provider, model, Reasoning Effort, Runner, and applicable Runtime configuration without storing secret values.
_Avoid_: runtime profile, mutable launch selection, global configuration record

**Task Event**:
A structured timeline entry for a **Task**, including runtime output, status changes, startup checks, and task-local workflow markers.
_Avoid_: audit log entry, transcript line, raw output dump

**Task Conversation**:
The user-runtime interaction that continues inside one **Task** after launch.
_Avoid_: new task per reply, detached chat

**Session Conversation**:
The user-runtime interaction that continues inside one **Non-Project Session**.
_Avoid_: Project Task Conversation, disposable chat, detached runtime log

**Runtime Turn**:
A single provider response cycle initiated by operator input within a **Task Conversation** or **Session Conversation**, or by a bounded **Harness Control Turn**. It retains its **Runtime Owner** identity while using its own **Runtime Turn Selection**.
_Avoid_: task, continuation, internal reasoning step

**Work Runtime Turn**:
A **Runtime Turn** initiated by the operator's Task Goal, Task Conversation input, or Session Conversation input. The **Runtime Harness** assigns this kind from request lineage; provider output cannot claim it. In assisted Blackboard mode, bounded Tool and Turn observations from this kind may create a **Pending Blackboard Conclusion**.
_Avoid_: harness control turn, provider-classified turn, task

**Harness Control Turn**:
A **Runtime Turn** initiated by the **Runtime Harness** for a bounded control purpose such as Blackboard conclusion reconciliation. The Harness assigns this kind from request lineage so it cannot recursively trigger assisted conclusion detection.
_Avoid_: operator message, autonomous task, provider-classified turn

**Conclude Runtime Turn**:
A **Harness Control Turn** delivered by one **Conclusion Dispatch** for a **Pending Blackboard Conclusion**. It runs on that dispatch's bound **Runtime Continuation**, reuses the source **Runtime Turn Selection**, forbids further testing, and returns one closed semantic Attempt result for Harness validation and application.
_Avoid_: user message, new continuation, task finish turn, unrestricted agent turn

**Runtime Turn Selection**:
The **Model Provider**, model, and **Requested Reasoning Effort** resolved for one **Runtime Turn**, independently of adjacent turns and without editing the selected **Runtime Profile**.
_Avoid_: profile switch, session-wide setting, global default

**Task Deletion**:
Operator removal of a terminal **Task** from normal task surfaces and counts while retaining the minimum durable state required for historical **Blackboard** and **Trusted Origin** integrity.
_Avoid_: active task cancellation, provenance erasure, hard deletion

**Task Finish**:
An operator action confirming that a **Task** is complete, which closes its Runtime after required reconciliation and marks the Task completed; it is distinct from **Stop** and **Blackboard Finish**.
_Avoid_: stop, auto-complete, **Blackboard Finish**

**Scope**:
The asset boundaries and testing limits that define what the **Pentest Agent** is authorized to do within a **Project**.
_Avoid_: target list, allowlist, permission note

**Scope Expansion**:
A proposed change that adds a newly discovered asset or testing permission to an existing **Scope**.
_Avoid_: auto-enrollment, target drift

**Out-of-Scope Fact**:
A **Project Fact** about an asset or action outside current **Scope** that is retained for context but not authorization.
_Avoid_: hidden target, pending target

**Scope Snapshot**:
An immutable copy of **Scope** captured when a **Task** starts.
_Avoid_: current scope, cached target list

**Runtime**:
The local agent CLI or assistant process scheduled to perform work for one **Runtime Owner**.
_Avoid_: pentest agent, model, provider, worker

**Runtime Owner-Scoped Persistent Runtime**:
A **Runtime** process or native session that remains available across **Runtime Turns** within one **Runtime Owner**. The Runtime Owner is a **Task** or **Non-Project Session**. The Runtime closes only at explicit owner completion, stop, failure, archive, or a required **Config Projection** restart.
_Avoid_: daemon-global session, infinite process, session-wide setting

**Runtime Harness**:
The daemon-managed, owner-neutral control wrapper that launches, resumes, steers, and stops a **Runtime** for one **Runtime Owner**.
_Avoid_: pentest tool executor, agent brain, sandbox

**Harness Steering**:
An owner-local control action that changes how the **Runtime Harness** continues a **Task** or **Non-Project Session** without creating a new Runtime Owner. When the Runtime exposes provider-native same-turn steering, it may append operator input to the current active steerable **Work Runtime Turn**; otherwise it changes a later or replacement **Runtime Continuation**.
_Avoid_: direct tool control, hidden prompt mutation, new Runtime Owner

**Accepted Steering**:
A durable **Harness Steering** request for one **Runtime Owner** whose ordered dispatch and settlement responsibility belongs to the **Runtime Harness**. It must reach `applied`, `failed`, or `action_required`, including after daemon restart.
_Avoid_: saved message, in-memory callback, permanent pending state

**Runtime Continuation**:
One writable unit of runtime progress after launch, user input, checkpoint, interrupt, or resume. Provider-native same-turn steering remains inside the current Runtime Continuation; interrupt-then-replace creates a replacement Runtime Continuation.
_Avoid_: rewriting completed Runtime items, new task

**Runtime Activity Indicator**:
An operator-visible current-state view with Runtime liveness (`live`, `offline`, `orphaned`, or `unknown`) and, while live, turn activity (`busy` or `idle`), independently of the durable **Task** lifecycle.
_Avoid_: Task status, audit record, activity history

**Runtime Non-Interactive Defaults**:
Provider-native arguments required for a **Runtime** to operate without interactive approval or permission prompts. The **Runtime Harness** adds them to every launch and **Runtime Continuation**: Codex receives `--dangerously-bypass-approvals-and-sandbox`; Claude Code receives `--dangerously-skip-permissions` and `--permission-mode bypassPermissions`; Hermes receives `--yolo` and `HERMES_YOLO_MODE=1`. For persistent Codex App Server, the same default is projected as `approvalPolicy=never` and `sandbox=danger-full-access` on `thread/start`, `thread/resume`, and `turn/start`, plus `approval_policy` and `sandbox_mode` in the projected Codex config. These defaults apply to both **Sandbox Runner** and **Host Runner** execution, and are not duplicated when the **Runtime Profile** already supplies them.
_Avoid_: permission grant, Scope authorization, **Host Runner Activation**, **Project Interface** authority, runner policy

**Runtime Profile**:
A global user-created reusable advanced configuration for a **Runtime**, including MCP, Skill opt-outs, Extensions, binary paths, custom configuration, or Runner defaults without storing secret values.
_Avoid_: account, credential bundle, secret store, automatic profile, runtime profile preset

**Runtime Custom Arguments**:
Advanced **Runtime Profile** command arguments for provider-native options that have no structured CyberPenda field.
_Avoid_: Model Provider override, model override, reasoning-effort override, structured-field duplicate

**Launch-Resolved Runtime Profile**:
A legacy global **Runtime Profile** that an older **Launch Configuration Resolution** path created from **Launch Selection**. New launches never create, reuse, or implicitly promote one.
_Avoid_: runtime profile preset, runtime configuration snapshot, current launch output

**Launch-Resolved Runtime Profile Migration**:
The one-time conversion that copies every complete non-secret legacy Profile setting into each referencing Runtime Owner's self-contained **Runtime Configuration Snapshots** before removing all **Launch-Resolved Runtime Profiles**.
_Avoid_: profile cleanup, blind deletion, hidden tombstone

**Model Provider**:
A global reusable non-secret configuration for a model service that a **Launch Selection** or **Runtime Profile** can use when a **Runtime** needs model access.
_Avoid_: runtime profile, runtime plugin, model only, credential value

**Launch-Ready Model Provider**:
A **Model Provider** that resolves a protocol endpoint, catalog model, and configured API key compatible with the selected **Runtime Plugin**. Readiness is runtime-specific and checked by **Preflight**.
_Avoid_: saved provider, valid draft, globally ready provider

**Model Provider ID**:
The immutable identifier for a **Model Provider**, used to derive its **Model API Key Environment Variable**.
_Avoid_: display name, editable label, secret name

**Model Provider ID Generation**:
The creation-time derivation of a **Model Provider ID** from a **Model Provider** display name.
_Avoid_: user-entered identifier, editable id

**Model Providers Page**:
The global settings view for managing **Model Providers**.
_Avoid_: runtime profile subform, project settings panel, credential page

**Model Provider Migration**:
An explicit management action that moves legacy model-service fields from a **Runtime Profile** into a reusable **Model Provider**.
_Avoid_: silent automatic migration, runtime profile edit side effect

**Model Provider Endpoint Backfill**:
The automatic interpretation of an older **Model Provider** with provider-level `base_url` and `protocols` fields as a new **Model Provider** with backfilled **Model Provider Endpoints**, including the explicit Anthropic final-segment adaptation.
_Avoid_: user migration, runtime profile migration, endpoint guessing

**Model Provider Migration Preview**:
The non-secret review of proposed **Model Provider** fields before a **Model Provider Migration** is confirmed.
_Avoid_: automatic migration result, hidden protocol choice, credential value

**Model Provider Migration Match**:
A possible existing **Model Provider** shown during **Model Provider Migration** when legacy fields resemble an already configured provider.
_Avoid_: automatic reuse, forced merge, duplicate detection as truth

**Model Catalog**:
The model names and default model exposed by one **Model Provider**.
_Avoid_: endpoint-specific model list, runtime model list

**Manual Model Entry**:
A user-entered model identifier in a **Model Catalog** that is preserved across **Model Catalog Refresh**.
_Avoid_: refreshed model, provider metadata

**Model Catalog Refresh**:
An explicit user-triggered management action that fetches model names from a **Model Provider** using a `/v1/models` model-list path and updates the **Model Catalog**.
_Avoid_: background polling, task-launch discovery, runtime introspection

**Model Context Window**:
The total token capacity used for one catalog model after Catalog override, **Model Capability Cache**, or Runtime native fallback.
_Avoid_: Hosted Auto Compact Window, context file size

**Model Max Output Tokens**:
The maximum completion reservation used for one catalog model after Catalog override, **Model Capability Cache**, or Runtime native fallback.
_Avoid_: Hosted Max Output Tokens, compact window

**Model Capability Cache**:
A bundled, optionally refreshed, non-authoritative table of known model identifiers to **Model Context Window** and **Model Max Output Tokens**, sourced from models.dev.
_Avoid_: Model Catalog, Runtime Plugin table, family heuristic

**Model Catalog Limit Override**:
Optional per-identifier window and max-output numbers stored on a **Model Catalog**. They beat the **Model Capability Cache** and survive catalog name refresh and cache refresh.
_Avoid_: Runtime Profile field, inferred model family limit

**Model Capability Cache Refresh**:
An explicit user-triggered management action that replaces the daemon overlay of the **Model Capability Cache** from models.dev. A failed refresh preserves the previous cache.
_Avoid_: Model Catalog Refresh, Task Launch fetch, Preflight download

**Model Catalog Refresh URL**:
The derived non-secret URL used by **Model Catalog Refresh**. It is not user-configured; it is derived from an OpenAI-family **Model Provider Endpoint** origin and the model-list path is always `/v1/models`.
_Avoid_: custom catalog base URL, runtime endpoint, protocol endpoint

**Model Endpoint Origin**:
The scheme, host, and optional port shared by one or more **Model Provider Endpoints** for a **Model Provider**.
_Avoid_: protocol path prefix, operation URL, catalog refresh URL

**Model Protocol Path Prefix**:
The protocol-specific path portion of a **Model Protocol Base URL** before the **Runtime** appends its operation suffix, such as an empty path, `/v1`, `/api/anthropic`, or `/api/coding/paas/v4`.
_Avoid_: operation suffix, model-list path, full endpoint URL

**Model Operation Suffix**:
The protocol operation path appended by a **Runtime** after receiving a **Model Protocol Base URL**, such as `/v1/messages`, `/messages`, `/v1/responses`, `/responses`, `/v1/chat/completions`, or `/chat/completions`.
_Avoid_: protocol path prefix, model-list path, configured base URL

**Model Protocol Base URL**:
The non-secret base URL for one **Model Provider Protocol**, stored as `base_url` on a **Model Provider Endpoint**. It combines a **Model Endpoint Origin** with any **Model Protocol Path Prefix** and excludes the protocol operation suffix that the **Runtime** appends, such as `/v1/messages`, `/responses`, or `/chat/completions`.
_Avoid_: operation URL, catalog refresh URL, full request URL

**Model Catalog Refresh Format**:
The response shape accepted by **Model Catalog Refresh** when parsing a model-list response.
_Avoid_: protocol negotiation, provider-specific parser selection

**Model Provider Endpoint**:
A concrete non-secret entry within a **Model Provider**'s `endpoints` list that records one **Model Provider Protocol** and its **Model Protocol Base URL**. A **Model Provider** may have different **Model Provider Endpoints** for different protocols while sharing one **Model API Key Source**.
_Avoid_: protocol, runtime profile, credential value, custom header bundle

**Model Provider Endpoint Defaults**:
The **Model Providers Page** quick-setup behavior that derives common **Model Provider Endpoints** from one shared provider base URL, often including an API version path such as `/v1` or `/v2`, before saving composed endpoint `base_url` values.
_Avoid_: canonical storage fields, operation suffix generation, protocol support auto-detection

**Normalized Model Protocol Base URL**:
A **Model Protocol Base URL** stored after uniformly removing trailing slashes while preserving provider path prefixes such as `/v1`, `/api/anthropic`, or `/api/coding/paas/v4`.
_Avoid_: semantic URL repair, proxy route, operation suffix

**Model Override**:
A **Runtime Profile** field that replaces the selected **Model Provider**'s default model when that profile is used without a **Launch Model Override**.
_Avoid_: provider edit, endpoint fork, hidden model switch, launch-only override

**Reasoning Effort**:
An optional **Runtime Profile** default for how much model reasoning a **Runtime** should request for a **Runtime Turn**, using `low`, `medium`, `high`, `xhigh`, or `max`; when absent, CyberPenda defaults to `high`.
_Avoid_: thinking mode, token budget, custom runtime flag, auto effort

**Requested Reasoning Effort**:
The **Reasoning Effort** CyberPenda asks a **Runtime** to use for one **Runtime Turn**. It is a request, not proof of the level the model actually used.
_Avoid_: effective effort, supported effort, reasoning token count

**Effective Reasoning Effort**:
The reasoning level a **Runtime** reports that it actually applied after any native validation or downgrade. It remains unknown when the **Runtime** does not report it.
_Avoid_: requested effort, assumed effort, profile default

**Runtime Reasoning Entry**:
A durable, streamable `reasoning` row in one **Runtime Owner** Transcript that projects reasoning text the **Runtime** emitted during one **Runtime Turn**. Partial deltas coalesce by stable provider-item identity, remain interleaved with messages and Tool activity, expand while live, and remain replayable after the Turn settles. Raw reasoning emitted by a provider is retained through the normal runtime-output redaction and history-window controls; a provider summary is only a fallback when raw reasoning is absent.
_Avoid_: Reasoning Effort, Runtime Activity Indicator, inferred hidden reasoning, temporary typing indicator, Task Event summary

**Model Provider Protocol**:
The model-service API contract a **Model Provider Endpoint** supports and a **Runtime Plugin** knows how to project for a **Runtime**.
_Avoid_: runtime provider, endpoint URL, model name

**Model Protocol Preference**:
A **Runtime Plugin** ordering that chooses a compatible **Model Provider Protocol** when a **Runtime Profile** does not pin one.
_Avoid_: hidden provider switch, model ranking, runtime profile default

**Model Credential Projection**:
The **Runtime Plugin** mapping that injects a **Model Provider** API key into the environment, config, or argument shape required by a **Runtime**.
_Avoid_: separate credential, runtime profile credential, endpoint secret

**Model Runtime Projection**:
The **Config Projection** step that derives and passes the runtime-specific model URL, protocol, model, and credential to a **Runtime** without proxying model traffic.
_Avoid_: LLM proxy, gateway request, daemon model call

**Global Model Projection**:
The **Config Projection** that makes every global **Launch-Ready Model Provider**, its model configuration, and its configured model API credential available to a **Runtime** that can switch providers natively. Pi and Hermes use it. Codex and Claude Code do not.
_Avoid_: selected-provider-only projection, project allowlist, on-demand credential injection, Pi-only projection

**Model API Key Source**:
The required single source for the API key used by a **Model Provider**.
_Avoid_: credential reference, project override, runtime profile key

**Model API Key Environment Variable**:
The generated environment variable name used as the **Model API Key Source** for a **Model Provider**.
_Avoid_: user-entered env var, inline API key, secret value, credential reference

**Model Provider Snapshot**:
The non-secret resolved model provider values captured in a **Task Runtime Configuration** for one launch or continuation, including `endpoint_base_url`, protocol, model, and API key source provenance.
_Avoid_: live model provider reference, model catalog copy, credential value

**Model Provider Requirement**:
A **Runtime Plugin** declaration that says whether a **Runtime Profile** must, may, or must not resolve a compatible **Model Provider** and **Model Provider Protocol**.
_Avoid_: hidden preflight rule, runtime profile convention, provider guess

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

**Runtime Extension Bundle**:
The file-backed content of a **Runtime Extension**, including its instructions, scripts, assets, and structured non-secret metadata.
_Avoid_: manifest-only skill, external path pointer, raw JSON config

**Skill**:
A runtime-agnostic **Runtime Extension Bundle** managed through the **Skills Page** and projected for any supported **Runtime** when enabled by a **Runtime Profile**.
_Avoid_: runtime plugin, provider-specific extension, MCP server

**Skill ID**:
The stable identifier for one **Skill** in the **Runtime Extension Library**, used by **Runtime Extension Enablement** and repeated imports to refer to the same skill.
_Avoid_: display name, package source, duplicate copy

**Skill Source Provenance**:
The non-authoritative record of where a **Skill** came from and how it was last imported or edited.
_Avoid_: Skill ID, trust proof, enablement source of truth

**Built-in Skill**:
A packaged **Skill** seeded by the daemon into the **Runtime Extension Library** from reviewed upstream sources.
_Avoid_: remote runtime download, hardcoded prompt fragment, uneditable system-only skill

**Skill Bundle Format**:
The canonical file layout for a **Skill**, centered on a skill instruction document with optional scripts and assets.
_Avoid_: provider-native plugin format, manifest-only format

**Skill Bundle Edit**:
A bounded change to a **Skill**'s instruction document, structured metadata, scripts, or assets within its **Runtime Extension Bundle**.
_Avoid_: raw manifest editing, host filesystem edit, path escape

**Skill Execution Boundary**:
The existing **Task**, **Scope**, **Runner**, credential, and **Project Interface** constraints that govern actions influenced by a **Skill**.
_Avoid_: skill-granted permission, scope expansion

**Skill Deletion**:
Removal of a **Skill** from the **Runtime Extension Library**, guarded so it does not silently leave broken **Runtime Extension Enablement**.
_Avoid_: dangling profile reference, live task mutation, silent launch breakage

**Skill Preflight Preview**:
The **Run Controls** and **Preflight** view of enabled **Skills** and their projection readiness before **Task** launch.
_Avoid_: hidden runtime context, raw bundle dump

**Task Skills Root**:
The task-local directory containing enabled **Skills** for one **Task**, exposed to the selected **Runtime** through that runtime's skill discovery path.
_Avoid_: global skills directory, host runtime home, package install location

**Runtime-Specific Extension**:
A provider-native plugin, package, or configuration bundle represented as a **Runtime Extension** but scoped to a specific **Runtime Plugin** family.
_Avoid_: Skill, Runtime Plugin

**Runtime Extension Library**:
The global user-facing collection where reusable **Skills** and other **Runtime Extensions** are discovered, uploaded, edited, and made available to **Runtime Profiles**.
_Avoid_: project skill store, profile-local skill editor, runtime provider list

**Runtime Extension Import**:
The management-time intake of an external skill or package into the **Runtime Extension Library** so it can be reused and projected by later **Tasks**.
_Avoid_: task launch install, transient package reference, runtime-side package manager execution

**Controlled Skill Import**:
A **Runtime Extension Import** that accepts a package, source, or reference and runs a fixed import primitive rather than user-supplied shell.
_Avoid_: arbitrary command execution, task launch install, shell-scripted management

**Skill Publication**:
The atomic promotion of a validated **Runtime Extension Bundle** into the live **Runtime Extension Library**.
_Avoid_: partial live update, versioning system, failed-save mutation

**Skill Validation**:
The checks that gate **Skill Publication** for identity, bundle shape, path safety, non-secret metadata, credential references, size limits, and update intent.
_Avoid_: runtime execution test, trust proof, full code audit

**Runtime Extension Enablement**:
A **Runtime Profile** choice that allows a compatible **Runtime Extension** from the **Runtime Extension Library** to be projected for tasks using that profile.
_Avoid_: library membership, automatic global mount, project-wide default

**Default Skill Enablement**:
The default-on policy that enables newly uploaded or imported **Skills** for direct launches and all current and future **Runtime Profiles**, unless a selected Profile opts out.
_Avoid_: runtime-specific plugin default, live task mutation, project-local default

**Skill Opt-Out**:
A **Runtime Profile** override that disables a default-enabled **Skill** by **Skill ID**.
_Avoid_: Skill Deletion, Runtime-Specific Extension disablement, temporary task skip

**Skills Page**:
The top-level product view named Skills for managing **Skills** in the **Runtime Extension Library**.
_Avoid_: runtime profile subform, project settings panel, provider-specific plugin manager

**Runtime Extension Manifest**:
The declarative document that identifies a **Runtime Extension**, its compatible **Runtime Plugins**, source location, task-local projection target, and non-secret configuration.
_Avoid_: executable installer, credential file, remote marketplace listing

**Runtime Extension Projection**:
The task-local materialization of enabled **Runtime Extensions** into the selected **Runtime**'s home, config, skill, plugin, or MCP-compatible directories.
_Avoid_: host runtime mutation, global plugin install, profile edit side effect

**Launch Profile Selector**:
An advanced task-launch control for explicitly choosing an optional **Runtime Profile** filtered to the selected **Runtime Plugin** family.
_Avoid_: primary launch picker, default profile, model provider switch, raw config editor

**Save as Runtime Profile**:
An explicit operator-confirmed action that creates a named **Runtime Profile** from a direct **Runtime Configuration Snapshot** for later advanced editing and reuse.
_Avoid_: automatic promotion, generated profile, launch side effect

**Profile Selector**:
The settings-page control for choosing which **Runtime Profile** to edit.
_Avoid_: task launch default, launch selection picker

**Protocol Pin Selector**:
The **Runtime Profile** control for choosing Auto or a compatible **Model Provider Protocol**.
_Avoid_: all-protocol list, runtime plugin editor

**Generated Runtime Config**:
A previewable task-local config output produced from structured profile fields during **Config Projection**.
_Avoid_: source of truth, raw profile

**Custom Config File**:
The per-**Runtime Profile** provider-native raw configuration that holds only keys structured fields cannot express. **Config Projection** deep-merges it over the **Generated Runtime Config**, and structured fields always win conflicts.
_Avoid_: full config replacement, opaque override, host config edit

**Managed Config Key**:
A provider-native config key that CyberPenda re-derives at every **Config Projection**, declared per **Runtime Plugin Manifest**. **Profile Config Import** refuses to change it and points to the structured field that owns it.
_Avoid_: locked key, forbidden setting

**MCP Configuration**:
Structured runtime interface configuration that defines available project-facing MCP servers for a **Runtime Profile**.
_Avoid_: raw JSON blob, unvalidated tool config

**External MCP Server**:
A user-added MCP server that is available to a **Runtime**. CyberPenda does not inject a built-in Blackboard MCP server and does not grant Blackboard authority through MCP configuration.
_Avoid_: built-in Blackboard server, implicit Project Interface authority

**Profile Config Import**:
An advanced action that parses an edited runtime config back into structured **Runtime Profile** fields, mapping what structured fields express and storing the remainder in the **Custom Config File**. It refuses **Managed Config Key** changes and secret-shaped values.
_Avoid_: raw config save, opaque override, host config edit

**Task Runtime Configuration**:
The **Runtime Configuration Snapshot** owned by a **Task**, captured directly from **Launch Selection** or from a **Runtime Profile** selected for that launch.
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

**Global Environment Variable**:
A non-disabled **Global Credential Binding** that **Config Projection** injects into every **Runtime** as a process environment variable, without a **Runtime Profile** **Credential Reference**. Its name is the binding **Destination Environment Variable**.
_Avoid_: automatic global mount, profile-local env var, runtime-only env

**Destination Environment Variable**:
The process environment variable name under which a materialized **Credential Binding** is made available to a **Runtime**. For env sources it defaults to the source variable name; for literal sources it is required.
_Avoid_: credential reference, source value, inline env var

**Credential Binding Mode**:
The project setting that chooses whether a **Credential Reference** uses the global default binding or a project override.
_Avoid_: implicit credential behavior, hidden override

**Disabled Credential Binding**:
A project override that explicitly prevents a **Credential Reference** from using any credential source.
_Avoid_: missing binding, broken secret

**Config Projection**:
The Runtime Owner-local preparation of runtime configuration from a **Runtime Configuration Snapshot**, **Model Provider**, and **Credential References**.
_Avoid_: host config edit, config sync

**Preflight**:
A read-only startup check phase that resolves a non-persistent configuration preview and determines whether a **Task** or **Session** can launch its **Runtime**.
_Avoid_: runtime execution, pentest work

**Runtime Extension Requirement**:
A non-authorizing declaration that a Runtime Extension needs a compatible Project Kind or Scope capability before Task Launch. **Preflight** validates it, but it never changes Project Kind or expands Scope.
_Avoid_: Skill authorization, automatic Scope Expansion, runtime preference

**Model Preflight Preview**:
The **Preflight** view of resolved non-secret model provider projection and generated API key environment variable readiness.
_Avoid_: API key display, LLM connectivity test

**Project Interface**:
A supported channel that lets a **Runtime** read or write project state, memory, evidence, and reports.
_Avoid_: backdoor, low-level database access

**CLI Fallback**:
A command-line **Project Interface** used when the primary agent integration is unavailable or unreliable.
_Avoid_: bypass, debug-only path

**Blackboard**:
The project-local memory that stores durable semantic records and relationships for one **Project**, including **Entities**, **Exploration Objectives**, **Attempts**, **Project Facts**, **Findings**, **Solutions**, and **Evidence Artifacts**.
_Avoid_: chat history, notes database

**Blackboard Mode**:
A **Run Controls** choice of `interactive`, `working_graph`, or `disabled`. Interactive mode gives the Runtime a full CLI Blackboard grant. Working Graph mode gives the Runtime read-only CLI Blackboard access and a continuation-scoped file graph for write intents. Disabled mode gives the Runtime no Blackboard context or authority.
_Avoid_: Blackboard Conclusion Mode, autonomous Task completion, transcript parsing mode, Blackboard write permission

**Mode Skill**:
The single system Skill projected for one Blackboard Mode. It is separate from user-selected Skills. Exactly one of the interactive, Working Graph, or disabled Mode Skills is present in a Runtime launch.
_Avoid_: ordinary Skill toggle, mixed-mode instructions, Runtime Profile capability

**Working Graph**:
The Harness-defined file graph used in `working_graph` mode. It contains `state.md`, fact and data directories, goals, steps, a continuation-scoped Outbox, and continuation-scoped Receipts. It is Runtime working state, while the Blackboard remains the durable semantic source of truth.
_Avoid_: Assisted Blackboard, transcript parser, direct Blackboard storage

**Working Graph Intent**:
A bounded Runtime request written atomically to the current Continuation Outbox. It contains a semantic operation without Blackboard versions or idempotency keys. The Harness claims it, resolves authority and versions, applies it through the Blackboard service, and writes a Receipt.
_Avoid_: direct database command, provider tool result, lifecycle instruction

**Working Graph Receipt**:
The continuation-scoped file projection of a durable Working Graph Intent state. States include `pending`, `applying`, `retry_pending`, `applied`, `noop`, `action_required`, and `superseded`. An `action_required` Receipt blocks later Intents in sequence order.
_Avoid_: model acknowledgement, transient stdout, Blackboard record

**Agent-Managed Trace**:
Optional files that a Runtime chooses and maintains in its ordinary workdir when **Blackboard Mode** is disabled. CyberPenda may remind the Runtime that file tracing is available, but it does not define, parse, or migrate a trace file.
_Avoid_: file-backed Blackboard, fixed state file, Working Blackboard Snapshot, cross-Runtime handoff

**Conclusion Dispatch**:
A durable, idempotent attempt to deliver one **Conclude Runtime Turn** for a **Pending Blackboard Conclusion**. It is bound immutably to one **Runtime Continuation**, source Runtime session, and source **Runtime Turn Selection**; safe recovery creates a new dispatch instead of rewriting an earlier one.
_Avoid_: semantic obligation, mutable receipt, continuation migration

**Blackboard Finish Intent**:
A Runtime request within a **Work Runtime Turn** to close the current **Runtime Continuation**'s Blackboard write protocol when that Turn settles. Later source work in the same Turn invalidates the intent.
_Avoid_: Task Finish, immediate continuation close, coverage of later work

**Semantic Debt Watermarks**:
The ordered counts persisted for a completed **Work Runtime Turn**: source work advances for each terminal non-Blackboard Tool Result, while semantic persistence advances only when a later successful semantic Blackboard change, Attempt checkpoint, or Blackboard Finish covers the work observed so far. The conclusion is pending only while source work is ahead of semantic persistence.
_Avoid_: raw Tool output, transcript offset, proof that a read or Evidence retention persisted semantics

**Blackboard Key**:
A stable, human-readable semantic identifier that is unique across every record in one **Blackboard** and resolves only within its **Project**. It identifies a record without requiring its type or a database ID and does not embed internal Project, Task, Continuation, Runtime, generated-ID, or hash values.
_Avoid_: database ID, globally unique ID, type-scoped key

**Record Merge**:
A governed consolidation of duplicate same-type **Project Knowledge** into one canonical record, with relationships rewritten and the source moved to **Semantic History**.
_Avoid_: silent deletion, Current Work merge, cross-type conversion

**Blackboard Key Redirect**:
A project-local redirect from a merged record's former **Blackboard Key** to the canonical Blackboard Key.
_Avoid_: current record, duplicate identity, migration compatibility alias

**Entity**:
A durable Blackboard identity for what project knowledge or exploration work is about, such as a host, service, endpoint, identity, file, or function. Its scope status describes memory and never grants authorization.
_Avoid_: asset authorization, project fact, finding

**Project Fact**:
A stable, project-scoped assertion that can be reused by later tasks without carrying raw proof content.
_Avoid_: raw command result, task event, memory blob

**Fact Key**:
A **Blackboard Key** used to update the same **Project Fact** over time.
_Avoid_: database ID, fact summary

**Fact Version**:
A historical revision of a **Project Fact** created when a **Fact Key** update changes its content or confidence.
_Avoid_: separate fact, duplicate fact

**Deprecated Fact**:
A **Project Fact** that remains historically available but should not be treated as current truth.
_Avoid_: deleted fact, stale note

**Current Truth**:
The default working set of non-deprecated **Project Facts** used by runtimes, UI views, and reports.
_Avoid_: absolute truth, all facts

**Tentative Fact**:
A reusable **Project Fact** that is plausible but not yet confirmed.
_Avoid_: task noise, confirmed fact

**Confirmed Fact**:
A **Project Fact** supported by evidence, reproduction, human confirmation, or independent corroboration.
_Avoid_: model assumption, unverified claim

**Blackboard Relationship**:
A typed, versioned semantic link between two current Blackboard records, identified by its source **Blackboard Key**, relationship type, and target Blackboard Key.
_Avoid_: edge ID, audit lineage, untyped relation

**Exploration Objective**:
A durable project-scoped investigation direction that may be derived from existing **Project Facts**, **Findings**, or **Solutions** and points toward an unknown future conclusion. It may inform a **Task Goal** and later resolve through **Project Facts**, **Findings**, or **Solutions**, but it is not **Current Truth** by itself.
_Avoid_: intent, open relationship, task, attack graph edge

**Attempt**:
A durable Blackboard record of one exploration episode that tests an **Exploration Objective**, **Entity**, **Project Fact**, **Finding**, or **Solution** and concludes with a distilled outcome.
_Avoid_: Task, command, tool call, raw output

**Challenge Platform**:
An external system that issues challenge Attempts and accepts candidate submissions or abandonment through a platform-specific **Platform Adapter**.
_Avoid_: Project, Scope authority, Runtime Extension

**TSecBench Hosted Image**:
A separate, self-starting distribution of the **Pentest Agent** for one TSecBench hosted evaluation. It preserves the Pentest Agent's Project, Task, Runtime, Skill, Blackboard, and policy semantics without becoming a normal product mode.
_Avoid_: CyberPenda deployment mode, replacement application image, specialized competition agent

**Container Host Runner**:
The Host Runner execution of a Runtime as a direct process inside the **TSecBench Hosted Image** container. It runs as container root with the platform's normal default capabilities. It does not mean access to the Challenge Platform's physical host and does not require privileged mode, TUN, `NET_ADMIN`, a Docker Socket, or a nested container engine.
_Avoid_: Sandbox Runner, physical host access, Docker-in-Docker, privileged container

**Hosted Tool Baseline**:
The bounded set of general-purpose tools preinstalled in the **TSecBench Hosted Image** on top of Kali Rolling: packaged Runtimes, shell and source utilities, tmux, Python/Go/C toolchains, compact binary and network tools, pwntools, Chromium, agent-browser, ffuf, gobuster, sqlmap, hydra, john, common protocol clients, compact forensic utilities, and compact reverse-engineering tools (gdb-multiarch, radare2, capstone, unicorn, pefile, ropper, ROPgadget, qemu-user, yara, upx, nasm, smali, dex2jar, Volatility 3, uncompyle6, xdis, PyInstaller archive viewer, pyinstxtractor-ng). The Runtime implements missing challenge-specific capability instead of relying on the full CyberPenda Sandbox tool inventory or large reverse-engineering suites.
_Avoid_: full Sandbox image, runtime package installation, per-challenge image

**Hosted Model Configuration**:
The operator-supplied Runtime family, model protocol, converted gateway base URL, model identifier, model API key, optional **Reasoning Effort**, optional **Hosted Auto Compact Threshold**, optional **Hosted Auto Compact Window**, optional **Hosted Max Output Tokens**, and optional **Model Context Window** used by one **Hosted Evaluation Run**. These values enter through stable `CYBERPENDA_*` environment names and are translated into normal Model Provider, Credential Binding, and Runtime Profile inputs during bootstrap. Compatibility is strict: Codex accepts only `openai_responses`, and Claude Code accepts only `anthropic_messages`. Pi and Hermes remain in the **Hosted Tool Baseline** but are not valid Hosted Runtime selections.
_Avoid_: vendor-specific environment contract, model discovery, persisted hosted profile

**Hosted Auto Compact Threshold**:
An optional operator-supplied percent, from 1 to 100, that tells the hosted Claude Code Runtime when to compact conversation context. A lower value starts compact earlier. An omitted value keeps the Claude Code default.
_Avoid_: Claude-native page variable, context window size, required compact setting

**Hosted Auto Compact Window**:
An optional operator-supplied absolute token count that tells the hosted Claude Code Runtime the compact window size. DeepSeek documents 786432. With DeepSeek 384K max output, hosted evaluation should use 524288 so the window plus 393216 stays under 1048576. An omitted value keeps the Claude Code default.
_Avoid_: Claude-native page variable, compact percent, required compact setting

**Hosted Max Output Tokens**:
An optional operator-supplied maximum completion token reservation for one hosted Claude Code request. DeepSeek hosted evaluation should use 393216 (384K). An omitted value keeps the Claude Code default of 32000.
_Avoid_: context window size, Claude-native page variable, required output cap

**Hosted Task Goal Appendix**:
Optional operator-supplied text appended to the required hosted **Task Goal**. It does not replace the required Skill completion sentence and does not change Hosted Controller behavior.
_Avoid_: replacement Task Goal, Skill rewrite, vendor prompt file

**Hosted Acceptance Configuration**:
The reference Runtime and model configuration used for hosted bootstrap, model-call, and fake-platform validation: Codex with `openai_responses`, plus the deployer-supplied gateway base URL, model identifier, and dedicated evaluation API key. Real TSecBench local-mode acceptance validates the platform API separately and does not require challenge solving.
_Avoid_: only supported hosted configuration, build-time model selection, production credential

**Hosted Delivery Bundle**:
The versioned `linux/amd64` TSecBench Docker archive with its checksum, Secret-free page environment-variable template, local-mode runner, resolved component inventory, and integration guidance.
_Avoid_: Docker archive alone, source checkout, multi-architecture image index

**Hosted Evaluation Run**:
One automatic, non-restartable, container-bounded execution of the **TSecBench Hosted Image** from configuration validation through all eligible challenge work and final platform termination.
_Avoid_: Project, Task, interactive session

**Hosted Transcript Stream**:
The sequence-ordered JSONL projection of every retained Task Transcript entry to container standard output. The Hosted Controller polls the uniform Transcript API, resolves truncated entry details, and masks exact `BENCHMARK_TOKEN` and hosted model API key values before writing each record.
_Avoid_: provider-native byte stream, daemon log tail, lossless live pipe, internal record redaction

**Hosted Operational Log**:
Non-Transcript Hosted Controller and daemon diagnostics written to container standard error so standard output remains a valid **Hosted Transcript Stream**.
_Avoid_: Transcript entry, mixed stdout log, formal evaluation result

**Hosted Evaluation Result**:
The score and completion state recorded by TSecBench for one **Hosted Evaluation Run**. Internal Project, Blackboard, and Evidence data support execution and diagnosis but are not part of this formal result.
_Avoid_: CyberPenda report, container database, runtime log

**Benchmark Challenge**:
One independently started and answered item within a Challenge Platform evaluation set. Multiple Benchmark Challenges may be handled inside one CTF Challenge Project and one Task when the Runtime owns the full evaluation loop.
_Avoid_: Project, Task, Hosted Evaluation Run

**Hosted Controller**:
The TSecBench-specific bootstrap process that validates hosted configuration, starts the daemon, creates one CTF Challenge Project and one CTF Challenge Task, projects the **Hosted Transcript Stream**, and observes the Runtime until TSecBench forcibly terminates the container. It is fail-fast and does not resume an interrupted process. It does not list, schedule, solve, submit, close, formally finish, stop, gracefully terminate, or recover Benchmark Challenges, Tasks, or the Hosted Evaluation Run.
_Avoid_: Runtime, Challenge Workflow, challenge scheduler

**Subagent Activity**:
A provider-neutral Timeline projection of one child agent that a **Runtime** spawned inside a **Work Runtime Turn**, such as a Codex child thread from the in-turn multi-agent tools or a Claude Code Task-tool subagent. It carries durable child identity, a coarse started-to-settled activity state, and its provider, and is a Timeline entry rather than a conversation message. The **Runtime Harness** only observes it and never gains a spawn RPC or a Harness-owned subagent scheduling surface.
_Avoid_: Harness-owned subagent, Runtime Continuation per child, raw provider JSON dump, Task

**Hosted Challenge Client**:
A bounded, one-command process inside the TSecBench Hosted Image that performs one list, start, hint, submit, or guarded close operation for the Runtime. It has no background lifecycle, never controls the Hosted Controller or daemon, and derives safety decisions from current platform state so its failure cannot terminate the host process. It loads one Challenge Platform adapter by `CYBERPENDA_CHALLENGE_ADAPTER`. Overlay manifests under `/data/adapters` replace baked adapters without rebuilding the image.
_Avoid_: Hosted Controller, background sidecar, challenge scheduler, direct curl procedure

**Challenge Pass Clock**:
The Hosted Challenge Client file under the Runtime Workdir that stores each active Benchmark Challenge `started_at`, first-pass `budget_min`, and `attempt_n`. `list` projects `elapsed_min`, `budget_min`, `over_budget`, and `attempt_n` on stdout. It is not Blackboard knowledge and does not abandon a challenge.
_Avoid_: appendix timer, Lead memory, Blackboard timestamp, Hosted Controller scheduler

**Hosted FGS**:
The `ctf-orchestrator` file graph state under the Runtime Workdir for one Hosted Evaluation Run. It is the Runtime's sole semantic working state in disabled Blackboard Mode and remains an **Agent-Managed Trace**, not a CyberPenda Blackboard or Project Knowledge source.
_Avoid_: Hosted Blackboard, Working Blackboard Snapshot, Project Interface memory

**Platform-Issued Scope**:
An operator authorization statement in Scope notes that permits testing only the ephemeral target addresses returned by the Challenge Platform for the current evaluation credential. It guides the Runtime but is not a structured dynamic target selector.
_Avoid_: automatic Scope Expansion, structured target list, unrestricted platform network

**Platform Adapter**:
An implementation behind the internal Challenge Platform seam that maps claim, submit, abandon, and recovery operations to one external platform. Production and in-memory test Adapters satisfy the same interface.
_Avoid_: generic fetcher, Project Interface, Skill

**Challenge Workflow**:
The deep module whose small interface claims, submits, abandons, and finalizes challenge Attempts while owning stable identity, Task Policy enforcement, Platform Adapter calls, Evidence retention, Blackboard settlement, and recovery.
_Avoid_: raw platform client, prompt procedure, collection of Blackboard tool calls

**Runtime-Managed Challenge Execution**:
A challenge execution path in which the Runtime obtains challenge information and submits candidate answers through a platform interface available inside its execution boundary. It does not depend on the optional **Challenge Workflow** control surface.
_Avoid_: Challenge Workflow, operator-managed submission, hosted controller solving

**Challenge Operation**:
A durable idempotent claim, submit, abandon, or finalize request owned by one Task and one external Attempt. It moves through `pending` and `recording` to `completed`. If automatic daemon-restart recovery fails, it settles as `action_required` and is not retried automatically.
_Avoid_: tool call, Task Event, remote response only

**Finish Readiness**:
A read-only Task projection that reports whether **Task Finish** can proceed and lists every typed **Finish Blocker** across reconciliation, Current Work, Finish Intent state, and required challenge Evidence.
_Avoid_: Task status, Runtime Activity Indicator, automatic completion

**Finish Blocker**:
A stable typed reason that prevents Task Finish, such as a Pending Blackboard Conclusion, open Attempt, unfinalized challenge Attempt, open Exploration Objective, unsettled reconciliation, invalid Finish Intent, or missing required Evidence.
_Avoid_: warning text, latest conclusion status, runtime error

**Runtime Blackboard Snapshot**:
A topology-complete semantic view of the current main **Blackboard** graph at one revision. It includes every current reusable semantic record and relationship in compact form while excluding auxiliary record bodies, **Trusted Origin** data, audit history, and audit-only metadata.
_Avoid_: audit export, storage dump, relevance-selected subset

**Launch Blackboard Pin**:
The immutable **Runtime Blackboard Snapshot** captured when a **Runtime Continuation** starts and retained internally for deterministic recovery.
_Avoid_: live Blackboard, working file, refreshed snapshot

**Working Blackboard Snapshot**:
The task-local Runtime-readable Blackboard file initialized from the **Launch Blackboard Pin** and advanced after acknowledged semantic writes or synchronization.
_Avoid_: source of truth, immutable launch pin, automatic external refresh

**Blackboard Change Notice**:
A coalesced control signal telling an active **Runtime Continuation** that another Task advanced the current Blackboard beyond its last acknowledged revision and that the latest Snapshot will be delivered at the next trusted synchronization.
_Avoid_: automatic snapshot injection, Task completion transcript, change payload

**Semantic Change Batch**:
An atomic, replay-safe set of typed Blackboard changes expressed with semantic verbs and **Blackboard Keys**.
_Avoid_: graph operation envelope, arbitrary property map, storage mutation

**Semantic History**:
The prior semantic versions and terminal workflow records retained for explicit on-demand understanding without preserving an operation-by-operation audit ledger.
_Avoid_: event replay log, Provenance history, full historical graph

**Current Work**:
The active **Exploration Objectives** and **Attempts** that still require project work.
_Avoid_: task history, terminal workflow records, project knowledge

**Project Knowledge**:
The current, reusable **Entities**, **Project Facts**, **Findings**, **Solutions**, and **Evidence Artifact** references retained across Tasks. It is broader than **Current Truth**, which contains only non-deprecated Project Facts.
_Avoid_: current work, task history, absolute truth

**Attack Chain**:
A narrative path that connects **Project Facts** and **Findings** into an explainable security-testing story.
_Avoid_: attack graph, exploit graph

**Finding**:
A reportable security issue with severity, proof, impact, recommendation, and status.
_Avoid_: vulnerability, vulnerability record, bug

**Finding Key**:
A **Blackboard Key** used to update the same **Finding** over time.
_Avoid_: fact key, database ID, finding title

**Finding Version**:
A historical revision of a **Finding** created when a **Finding Key** update changes its content, status, severity, or confidence.
_Avoid_: separate finding, duplicate finding

**Finding Group**:
A report or UI grouping of related **Findings** that keeps each **Finding** identity separate.
_Avoid_: finding merge, shared finding

**Confirmed Finding**:
A **Finding** supported strongly enough by confirmed facts or evidence to report as verified.
_Avoid_: suspected finding, tentative issue

**Solution**:
A CTF Challenge conclusion represented as a candidate, verified, rejected, or superseded answer, flag, or procedure. It is not valid in a Pentest Project.
_Avoid_: Finding, Task completion, project solved flag

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

**Trusted Origin**:
The server-owned Project and execution binding used to validate who or what was authorized to create a Blackboard mutation or **Evidence Artifact**. It is internal integrity data, not Blackboard knowledge or user-facing audit content.
_Avoid_: Provenance, audit trail, metadata blob

**High-Risk Action**:
A testing action that may cause disruption, privileged data access, authenticated impact, exploit validation, or other impact beyond ordinary enumeration.
_Avoid_: dangerous command, scary action

**Intended Action**:
A pre-action record of what a runtime plans to do and why before a high-risk step.
_Avoid_: result log, after-the-fact note

**Policy Violation**:
A recorded workflow breach where a runtime performs or attempts an action outside the required scope or declaration process.
_Avoid_: runtime error

**Reconciliation**:
A governed review action that accepts, rejects, or reclassifies state discovered outside normal **Project Interface** writes.
_Avoid_: silent import, automatic trust

**Reconciliation Candidate**:
Untrusted discovered state proposed for **Reconciliation**.
_Avoid_: accepted fact, imported evidence

**Report**:
A deliverable generated from **Findings**, **Project Facts**, **Blackboard Relationships**, and **Evidence Artifacts**.
_Avoid_: transcript, export, source of truth

## Relationships

- A **Project** has exactly one current **Scope**.
- A **Project** has exactly one explicit **Project Kind**.
- Project creation does not silently select a Project Kind when the caller omits it.
- A **Project Kind Conversion** requires a blocker-free preview and explicit operator confirmation.
- **Task Launch** requires an explicit **Task Type** that matches the current **Project Kind** and stores it as an immutable snapshot.
- **Scope Expansion** is part of **Scope** but retains a distinct internal **Trusted Origin** from human-approved scope.
- An **Out-of-Scope Fact** does not change **Scope** and does not authorize testing.
- A **Project** may define **Project Defaults** for new **Tasks**, including a default **Runner** and Task Policy.
- **Task Launch** captures one immutable **Task Policy Snapshot**.
- The **Challenge Workflow** enforces Task Policy before each governed external operation, independently of prompt compliance.
- The **Hosted Challenge Client** is process-isolated from the **Hosted Controller**, daemon, and Runtime session; one client command failure affects only that command.
- The **Hosted Challenge Client** records the **Challenge Pass Clock** on successful start and clears it on successful close or abandon. `list` annotates `over_budget` from that clock. The Runtime still decides abandon or close.
- The `ctf-orchestrator` reads per-challenge timing from the **Challenge Pass Clock** projection and does not duplicate that timing state in the **Hosted FGS**.
- The Hosted Task uses disabled **Blackboard Mode**, receives no built-in **Project Interface**, and uses the **Hosted FGS** as its only Runtime-managed semantic state.
- The `ctf-orchestrator` Decide process is the sole caller of Hosted Challenge Client `list`, `start`, `hint`, `close`, and `abandon`, so every Challenge lifecycle, hint decision, and Clock write is serialized from the Runtime Workdir.
- An Execute agent may call Hosted Challenge Client `submit` directly with the candidate on standard input. Submit does not change the **Challenge Pass Clock**, and an Execute agent never starts, closes, or abandons a Benchmark Challenge.
- A clock file error does not fail the client command and does not terminate the Hosted Controller.
- Overlay adapter files under `/data/adapters` replace baked adapters without rebuilding the **TSecBench Hosted Image**.
- The **Hosted Challenge Client** never combines submit, close, and start in one operation.
- The **Hosted Challenge Client** rejects a normal close unless current TSecBench state proves the Benchmark Challenge complete; explicit abandonment requires a non-empty reason.
- The Runtime, not the **Hosted Challenge Client**, selects, schedules, solves, hints, submits, or abandons Benchmark Challenges.
- **Project Defaults** never preselect a **Runtime Profile**; each launch must select a Profile explicitly or use **Launch Selection** directly.
- **Launch Configuration Resolution** creates a Runtime Owner-local **Runtime Configuration Snapshot** and never finds, creates, or reuses a global **Runtime Profile** unless that launch explicitly selects one.
- A **Project Dashboard** is the primary UI entry point for a **Project**.
- **Runtime Profiles** are global and reusable across **Projects**.
- A **Runtime Profile** selects one **Runtime Plugin** by plugin identifier.
- **Preflight** validates every enabled **Runtime Extension Requirement** before Runtime execution.
- A **Runtime Extension Requirement** neither changes Project Kind nor expands Scope.
- **Model Providers** are global and reusable across **Runtime Profiles**.
- A **Model Provider** has an immutable **Model Provider ID**.
- A **Model Provider ID** is created through **Model Provider ID Generation**.
- **Model Provider ID Generation** appends a numeric suffix when the derived ID already exists.
- Editing a **Model Provider** display name does not change its **Model Provider ID** or **Model API Key Environment Variable**.
- **Model Providers** are managed through the **Model Providers Page**.
- A **Runtime Profile** may select one **Model Provider**.
- A **Runtime Profile** for a runtime with required **Model Provider Requirement** may be saved without a selected **Model Provider**, but it is not launch-ready.
- Legacy model-service fields on a **Runtime Profile** may be preserved for compatibility until a **Model Provider Migration** is explicitly run.
- A **Model Provider Migration** presents a **Model Provider Migration Preview** before creating or updating a **Model Provider**.
- A **Model Provider Migration Preview** may suggest a protocol from the source **Runtime Plugin**, but the user must confirm it.
- A **Model Provider Migration** uses the same explicit Anthropic final-segment adaptation as **Model Provider Endpoint Backfill** when deriving an `anthropic_messages` endpoint from a legacy `base_url`.
- A **Model Provider Migration Preview** may show **Model Provider Migration Matches**, but the user chooses whether to reuse an existing **Model Provider** or create a new one.
- A successful **Model Provider Migration** removes migrated legacy model-service fields from the source **Runtime Profile**.
- A **Model Provider** may define a **Model Catalog**.
- A **Model Catalog** stores model identifiers, not full provider response objects.
- A **Model Catalog** may store optional **Model Catalog Limit Overrides** keyed by model identifier.
- **Config Projection** resolves **Model Context Window** and **Model Max Output Tokens** as Catalog override, then **Model Capability Cache**, then Runtime native fallback.
- Missing window or max-output values do not fail **Preflight**.
- **Model Catalog Refresh** does not invent window or max-output values and preserves existing **Model Catalog Limit Overrides** for identifiers that remain.
- **Model Capability Cache Refresh** does not change Catalog names or operator overrides.
- **Model Capability Cache** lookup uses an exact model identifier, then a unique case-insensitive final-segment match; an ambiguous suffix with different limits is a miss.
- A **Model Catalog** may be edited manually or updated through **Model Catalog Refresh**.
- A **Model Catalog** may include manually entered model identifiers that were not returned by **Model Catalog Refresh**.
- **Manual Model Entries** are preserved when **Model Catalog Refresh** updates refreshed model identifiers.
- A **Manual Model Entry** with the same identifier as a refreshed model is merged into the refreshed model entry.
- A **Manual Model Entry** may be deleted unless it has been merged into a refreshed model entry.
- Refreshed model identifiers in a **Model Catalog** are not manually deleted or hidden.
- Any model identifier in the **Model Catalog**, whether manual or refreshed, may be used as a provider default or **Model Override**.
- A **Model Catalog Refresh** is a user-triggered management action, not part of **Preflight** or task launch.
- A **Model Provider** does not store a custom `catalog_base_url`.
- A **Model Catalog Refresh URL** is derived, not user-configured.
- A **Model Catalog Refresh URL** always uses the `/v1/models` path.
- A **Model Catalog Refresh URL** is derived from an OpenAI-family endpoint when one exists, preferring `openai_chat_completions` over `openai_responses`.
- A **Model Catalog Refresh URL** uses the selected OpenAI-family endpoint origin plus `/v1/models`, not the endpoint URI path.
- A **Model Catalog Refresh URL** ignores non-standard OpenAI runtime path prefixes such as `/api/coding/paas/v4` and still uses the selected endpoint origin plus `/v1/models`.
- A **Model Catalog Refresh** is unavailable when a **Model Provider** has no OpenAI-family endpoint; users may maintain the **Model Catalog** manually.
- A **Model Catalog Refresh** uses the **Model API Key Environment Variable** for the selected **Model Provider**.
- **Model Catalog Refresh Format** is OpenAI-style `/v1/models` in MVP.
- A failed **Model Catalog Refresh** preserves the existing **Model Catalog**.
- A successful **Model Catalog Refresh** stores the returned **Model Catalog** even if existing defaults or overrides become invalid.
- A **Model Provider** may be saved without a **Model Catalog**, but tasks that require model access cannot launch from it.
- A **Model Catalog** drives model dropdown choices in **Runtime Profile** editing and in **Launch Selection**.
- A **Model Provider** must include exactly one **Model API Key Source**.
- A **Model API Key Source** is a **Model API Key Environment Variable** in MVP.
- A **Model API Key Environment Variable** is generated from the **Model Provider** identifier as `<PROVIDER_ID>_API_KEY`.
- A **Model Provider** may contain one or more **Model Provider Endpoints**.
- A **Model Provider** stores **Model Provider Endpoints** as an `endpoints` list rather than a map keyed by protocol.
- A **Model Provider** does not store provider-level `protocols` in the new provider shape.
- A **Model Provider** derives protocol support from `endpoints[].protocol`.
- A **Model Provider Endpoint** stores exactly one **Model Provider Protocol** as `protocol`.
- A **Model Provider Endpoint** stores a **Normalized Model Protocol Base URL** as `base_url`.
- Multiple **Model Provider Endpoints** for one **Model Provider** commonly share a **Model Endpoint Origin** while differing by **Model Protocol Path Prefix**.
- A **Model Protocol Base URL** remains the canonical stored endpoint value; editing interfaces may split it into **Model Endpoint Origin** and **Model Protocol Path Prefix** controls before saving.
- **Model Provider Endpoint Defaults** let most users enter one shared provider base URL, commonly up to the provider API version path such as `/v1` or `/v2`, and derive common endpoint `base_url` values before save.
- **Model Provider Endpoint Defaults** use the shared provider base URL as the default `openai_chat_completions` and `openai_responses` `base_url`; they do not hardcode appending `/v1`.
- **Model Provider Endpoint Defaults** derive the default `anthropic_messages` `base_url` by removing the final non-empty path segment from the shared provider base URL after splitting the URL path on `/`.
- **Model Provider Endpoint Defaults** leave the shared provider base URL unchanged for `anthropic_messages` when it has no path segment to remove.
- A user may override individual **Model Provider Endpoint** `base_url` values when a provider exposes different protocol paths.
- **Model Provider Endpoint Defaults** are editing helpers only; saved **Model Provider** records still contain composed `endpoints[]` values rather than a separate shared provider base URL field.
- Each **Model Provider Protocol** may appear in at most one **Model Provider Endpoint** for a **Model Provider**.
- **Model Provider** management removes trailing slashes from endpoint `base_url` values before validation and storage.
- A **Model Provider Endpoint** `base_url` excludes any known **Model Operation Suffix** the selected **Runtime** appends.
- **Model Provider** management does not auto-fill, append, or store protocol operation suffixes in endpoint `base_url` values.
- A **Model Provider Endpoint** `base_url` that already ends with any known **Model Operation Suffix** after trailing slash normalization is invalid, even when that suffix belongs to a different **Model Provider Protocol**.
- **Model Provider** management rejects operation-suffixed endpoint `base_url` values instead of silently stripping the suffix.
- A **Model Runtime Projection** passes the selected endpoint `base_url` to the **Runtime** without appending the operation suffix in the daemon.
- A **Runtime** appends its own protocol operation suffix, such as `/v1/messages`, `/responses`, or `/chat/completions`.
- A **Model Provider Endpoint Backfill** treats the normalized old `base_url` as legacy runtime base URL source data for deriving endpoint `base_url` values.
- A **Model Provider Endpoint Backfill** creates one endpoint record for each old protocol.
- A **Model Provider Endpoint Backfill** derives the `anthropic_messages` endpoint by removing the final non-empty path segment from the old `base_url` after splitting the URL path on `/`, because Claude Code appends its own versioned messages operation path.
- A **Model Provider Endpoint Backfill** leaves the old `base_url` unchanged for `anthropic_messages` when it has no path segment to remove.
- A **Model Provider Endpoint Backfill** copies the normalized old `base_url` into non-Anthropic endpoint `base_url` values.
- A **Model Provider Endpoint Backfill** does not infer arbitrary provider-specific path repairs beyond the explicit Anthropic final-segment adaptation.
- **Model Provider Protocol** support is configured by editing **Model Provider Endpoints** on the **Model Providers Page**.
- A **Model Provider** may be saved without configured **Model Provider Protocol** support, but tasks that require model access cannot launch from it.
- Removing a **Model Provider Endpoint** removes that **Model Provider Protocol** support from a **Model Provider** and is allowed even if existing **Runtime Profiles** become invalid.
- A **Model Provider Endpoint** does not contain custom headers or proxy request configuration.
- A **Runtime Plugin** may support one or more **Model Provider Protocols**.
- A **Runtime Plugin Manifest** declares supported **Model Provider Protocols** and **Model Protocol Preference**.
- A **Runtime Profile** may pin a **Model Provider Protocol**; otherwise the selected **Runtime Plugin** applies its **Model Protocol Preference**.
- An empty **Model Provider Protocol** pin means use the selected **Runtime Plugin**'s **Model Protocol Preference**.
- A **Protocol Pin Selector** shows Auto and the intersection of protocols supported by the selected **Runtime Plugin** and **Model Provider**.
- A pinned **Model Provider Protocol** must remain compatible with the selected **Model Provider Endpoint** or **Preflight** fails.
- **Model Protocol Preference** selects the first compatible protocol and fails **Preflight** when none is compatible.
- The resolved model from a **Model Override** or **Model Catalog** default must exist in the **Model Catalog** or **Preflight** fails.
- A **Runtime Profile** may define a **Model Override** instead of editing the selected **Model Provider**.
- A **Runtime Plugin** owns **Model Credential Projection** for supported **Model Provider Protocols**.
- A **Runtime Plugin** owns **Model Runtime Projection** for supported **Model Provider Protocols**.
- A **Runtime Plugin** declares a **Model Provider Requirement**.
- A **Runtime Profile** may include runtime-specific **Credential References** but not model API key configuration by default.
- A **Runtime Plugin Manifest** may declare credential environment names but must not contain credential values.
- A **Runtime Plugin Manifest** is declarative and may reference only known **Runtime Plugin Primitives**.
- A **Runtime Plugin Registry** is the source of supported runtime provider identifiers.
- A **Runtime Extension** belongs to a selected **Runtime Plugin** and does not define a new runtime provider identifier.
- A **Runtime Extension Bundle** is the editable and projectable content managed by the **Runtime Extension Library**.
- A **Skill ID** identifies a **Skill** independently of display name or import source.
- **Skill Source Provenance** records manual upload, package import, source URL, update, or local modification context without replacing **Skill ID**.
- A **Built-in Skill** uses **Skill Source Provenance** kind `builtin` without exposing upstream repository details in user-facing payloads.
- A **Skill** is compatible with all supported **Runtime Plugins**.
- A **Skill** uses the **Skill Bundle Format** rather than a provider-native plugin format.
- A **Skill Bundle Edit** may modify the full **Runtime Extension Bundle** but must stay within that bundle.
- A **Skill** must not contain credential values or declare credential resolution requirements; credentials and environment variables belong to **Runtime Profiles** and **Credential Bindings**.
- A **Skill** follows the **Skill Execution Boundary** and does not grant permissions by itself.
- **Skill Deletion** is blocked while the **Skill** is enabled unless the user explicitly removes that enablement everywhere.
- **Skill Events** record import, upload, edit, deletion, provenance, and enablement changes.
- **Skill Preflight Preview** makes enabled **Skills** and related launch blockers visible before a **Task** starts.
- **Runtime Extension Projection** materializes enabled **Skills** into a **Task Skills Root**.
- A **Runtime-Specific Extension** narrows compatibility to the relevant **Runtime Plugin** family.
- The **Runtime Extension Library** is global and reusable across **Projects**.
- **Runtime Extension Import** happens before **Task** launch and produces or updates a reusable **Runtime Extension** in the **Runtime Extension Library**.
- **Controlled Skill Import** is the import path for package-backed skills.
- **Skill Validation** must pass before **Skill Publication**.
- **Skill Publication** makes successful imports or edits visible to future **Tasks**.
- A **Runtime Extension Import** with an existing **Skill ID** updates that **Skill** rather than creating a duplicate.
- **Built-in Skills** are seeded on daemon startup when missing, but startup seeding does not overwrite an existing **Skill ID** so user edits survive restart.
- The **Skills Page** is the top-level management view for **Skills** in the **Runtime Extension Library**.
- The **Skills Page** lives in global navigation rather than project navigation.
- **Runtime-Specific Extensions** are managed through their owning runtime-specific surfaces rather than treated as universal **Skills**.
- A **Runtime Extension Manifest** may declare compatibility, source paths, projection targets, and non-secret defaults but must not contain credential values.
- A **Runtime Profile** manages **Runtime Extensions** through structured controls rather than raw manifest JSON.
- **Runtime Extension Enablement** belongs to a **Runtime Profile** and is limited to compatible **Runtime Plugins**.
- **Default Skill Enablement** applies to **Skills** but not **Runtime-Specific Extensions**.
- A **Runtime Profile** may opt out of a **Skill** enabled by **Default Skill Enablement**.
- A **Skill Opt-Out** is tied to **Skill ID** and survives ordinary imports or edits that update the same **Skill**.
- The **Skills Page** bulk enablement actions apply to the selected **Runtime Profile** and the current **Skill** library: Disable all atomically creates a **Skill Opt-Out** for every current Skill, while Enable all atomically removes every Skill Opt-Out for that profile. Neither action changes started **Tasks**, and later imports still follow **Default Skill Enablement**.
- **Skill Deletion** ends the enablement lifecycle for that **Skill ID**; re-importing the same **Skill ID** follows **Default Skill Enablement** instead of restoring old opt-outs.
- The **Skills Page** may change **Runtime Extension Enablement**, but the enablement state still belongs to the affected **Runtime Profile**.
- A **Runtime Profile** may reference a manually entered **Runtime Extension** identifier, but task launch still requires the daemon **Runtime Extension Registry** to resolve it.
- A new **Task** loads the current **Runtime Extensions** from the **Runtime Extension Library** when its runtime configuration is projected.
- **Preflight** previews enabled **Skills** but resolves credentials only from **Runtime Profiles**, **Model Providers**, and launch requests.
- **Preflight** includes a **Model Preflight Preview** when model access is used.
- A started **Task** keeps the **Runtime Extensions** already materialized into its task-local runtime boundary.
- A Runtime Owner Resume uses its latest **Runtime Configuration Snapshot**, including the captured Skills and advanced configuration; later global defaults or Runtime Profile edits do not change that Snapshot.
- **Runtime Extension Projection** happens during **Config Projection** and must not mutate host runtime plugin directories.
- A **Credential Reference** resolves first through **Credential Bindings**, then through **Global Credential Bindings**.
- A **Global Environment Variable** injects into every **Runtime** during **Config Projection** without a **Runtime Profile** **Credential Reference**.
- A **Destination Environment Variable** names the process environment variable a materialized **Credential Binding** makes available to a **Runtime**.
- A **Global Environment Variable** uses a non-disabled **Global Credential Binding** as its source.
- A **Runtime Profile** **Credential Reference** that resolves to the same **Destination Environment Variable** as a **Global Environment Variable** overrides the global value.
- **Preflight** validates every **Global Environment Variable**, even when the **Runtime Profile** has no **Credential References**.
- A literal **Credential Binding** must declare a **Destination Environment Variable** or **Preflight** fails.
- A **Project** may define **Credential Bindings** that override **Global Credential Bindings** for **Credential References** used by global **Runtime Profiles**.
- **Credential Binding Mode** defaults to using **Global Credential Bindings** unless the user explicitly chooses a project override.
- A **Disabled Credential Binding** blocks fallback to **Global Credential Bindings**.
- A **Runtime Profile** may define a default **Runner** for new **Tasks**.
- A **Profile Selector** chooses which **Runtime Profile** to edit on the settings page.
- A **Launch Profile Selector** is an advanced task-launch control and is not the primary launch path.
- A **Launch Profile Selector** lists only **Runtime Profiles** compatible with the selected **Runtime Plugin** family.
- A **Launch Profile Selector** defaults to no Profile and labels that state as direct configuration.
- Selecting a **Runtime Profile** locks the **Launch Selection** runtime and **Model Provider** to that Profile's values.
- A **Launch Model Override** may still be chosen at launch when a **Runtime Profile** is selected.
- A selected **Runtime Profile** keeps its identity for the **Task** even when a **Launch Model Override** is used.
- A **Launch Model Override** affects only the launching **Task** and its captured **Task Runtime Configuration**; it does not edit the selected **Runtime Profile**.
- A **Launch Reasoning Effort Override** may be chosen with or without a **Runtime Profile** and affects only the launching **Task**.
- **Requested Reasoning Effort** resolves in this order: the current **Runtime Turn Selection**, **Launch Reasoning Effort Override** for the initial turn, the **Runtime Profile** default, then CyberPenda's `high` default.
- **Requested Reasoning Effort** belongs to its **Runtime Turn** and does not edit the selected **Runtime Profile**.
- Changing the selected **Runtime Plugin** family during launch clears an incompatible **Runtime Profile** selection.
- **Launch Configuration Resolution** applies a **Runtime Profile** only when the launch explicitly selects that Profile.
- **Launch Configuration Resolution** otherwise builds the **Runtime Configuration Snapshot** directly from the selected **Runtime Plugin**, **Model Provider**, model, **Reasoning Effort**, **Runner**, Runtime Plugin standard configuration, and globally default-enabled **Skills**.
- A direct **Launch Selection** does not receive **MCP Configuration**, **Custom Config File**, **Runtime Extension Enablement**, or **Skill Opt-Out** from any matching or default **Runtime Profile**.
- A direct Runtime Owner is displayed by its **Runtime Plugin**, **Model Provider**, and model rather than a synthetic Profile name; an explicit Profile name is shown only when that Owner selected a **Runtime Profile** at creation.
- **Save as Runtime Profile** requires a user-supplied name and explicit confirmation; CyberPenda never suggests, triggers, or completes it automatically.
- Task, Session, Resume, Steering, and Runtime Turn model-selection paths never create or reuse a **Launch-Resolved Runtime Profile**.
- **Launch-Resolved Runtime Profile Migration** preserves every referenced legacy setting, including Runtime Plugin, Model Provider, model, Reasoning Effort, Custom Arguments, Sandbox image, Runner, and other structured configuration.
- **Launch-Resolved Runtime Profile Migration** removes every legacy Profile only after migrated history, Resume, and replacement Runtime Continuation behavior pass integrity checks; it does not retain hidden Profile tombstones.
- A **Runtime Profile** uses structured fields as source of truth for **Generated Runtime Config**.
- **Generated Runtime Config** previews the resolved non-secret **Model Runtime Projection**, including the runtime-specific model URL, protocol, model, generated API key environment variable name, and runtime-specific projection target.
- A **Runtime Plugin** describes which structured fields a **Runtime Profile** exposes.
- A **Runtime Profile** manages **MCP Configuration** as structured entries with raw preview or import as compatibility support.
- **MCP Configuration** contains user-configured **External MCP Servers** only; CyberPenda does not inject a built-in Blackboard MCP server.
- An **External MCP Server** does not receive project write authority by default.
- An **External MCP Server** follows the same **Runner** and **Sandbox** environment as its **Runtime**.
- **External MCP Server** output enters the **Blackboard** only when a **Runtime** writes it through a trusted **Project Interface**.
- A **Profile Config Import** updates a **Runtime Profile** only when the edited config can be parsed into structured fields.
- A **Project** has zero or more **Tasks**.
- A **Task** starts from one **Task Goal** plus **Run Controls**.
- Project **Task** and Non-Project **Session** Run Controls support the same `interactive`, `working_graph`, and `disabled` **Blackboard Mode** values.
- New Tasks default to `working_graph`. New Sessions default to `disabled`.
- A Runtime Owner captures one immutable Blackboard Mode at creation, and every later Resume and Runtime Continuation inherits it.
- A Runtime Owner in `disabled` Blackboard Mode receives no Blackboard context or authority and creates no Blackboard conclusion or reconciliation obligations.
- A disabled Runtime receives a concise reminder to use a state file at its initial launch and at each replacement Runtime launch, but not on ordinary Runtime Turns; the reminder does not explain file lifecycle or handoff limits.
- A disabled Runtime receives no built-in **Project Interface**; its Task Goal, Scope Snapshot, and Task Policy Snapshot remain launch context, and it asks the operator in conversation for Scope Expansion or file-retention actions.
- A disabled Runtime receives no Blackboard grant or credential. Blackboard APIs add no mode-specific rejection; an ungranted Runtime request fails normal authorization, while an operator-authorized request remains valid.
- Disabled Blackboard does not remove the **Task Goal**, **Scope Snapshot**, **Runner**, **Task Policy Snapshot**, Transcript, or ordinary Runtime Owner attachments.
- Disabled Blackboard cannot create **Entities**, **Exploration Objectives**, **Attempts**, **Project Facts**, **Findings**, **Solutions**, **Evidence Artifacts**, or **Blackboard Relationships**.
- The Runtime Owner Workspace labels disabled Blackboard explicitly, hides Blackboard Snapshot, conclusion, reconciliation, and mutation surfaces, and retains the project-level Blackboard on the Project Dashboard.
- The operator may explicitly retain a disabled Runtime's file or attachment as an **Evidence Artifact** or submit it for **Reconciliation** without exposing the resulting Blackboard state to that Runtime.
- A **Report** does not infer conclusions from a disabled Runtime's Transcript or workdir files; only Blackboard state created through explicit operator retention or Reconciliation becomes Report input.
- **Finish Readiness** for a Runtime Owner in disabled Blackboard Mode ignores Blackboard conclusion, Current Work, Finish Intent, and reconciliation blockers while retaining non-Blackboard lifecycle and policy checks.
- A **Task** may pursue one primary **Exploration Objective** while producing multiple **Project Facts**, **Findings**, or **Evidence Artifacts**.
- A **Task** receives one **Runtime Configuration Snapshot** through **Launch Configuration Resolution** and chooses one **Runner**.
- A **Task** has one **Runtime Harness** that controls runtime lifecycle for that task.
- A **Task** launches from its **Task Runtime Configuration**, not a live mutable **Runtime Profile**.
- A **Task Runtime Configuration** captures the selected **Runtime Plugin** identifier.
- A **Task Runtime Configuration** captures a **Model Provider Snapshot** when model access is used.
- A **Model Provider Snapshot** includes `endpoint_base_url`, protocol, model, and non-secret **Model API Key Source** provenance.
- A **Model Provider Snapshot** may expose `base_url` as a transition alias for `endpoint_base_url`, but new code uses `endpoint_base_url`.
- A **Model Provider Snapshot** uses the model selected by **Launch Selection** or a **Launch Model Override** when supplied; a launch with an explicit **Runtime Profile** may otherwise use that Profile's **Model Override** or the **Model Catalog** default.
- A **Model Provider Snapshot** does not include the full **Model Catalog** or any credential value.
- A **Task Runtime Configuration** may include **Credential References** but not credential values.
- Editing a **Runtime Profile** does not change existing **Task Runtime Configurations**.
- **Runtime Custom Arguments** cannot declare a **Model Provider**, model, or **Reasoning Effort** through any runtime-native alias; Profile validation and **Preflight** reject such conflicts with a clear offending-argument error and diagnostic log instead of relying on CLI argument order.
- CyberPenda does not migrate, remove, reinterpret, or otherwise fall back around conflicting **Runtime Custom Arguments**.
- Structured Profile, launch, and **Runtime Turn Selection** fields are authoritative for **Model Provider**, model, and **Reasoning Effort**.
- Non-conflicting **Runtime Custom Arguments** continue to apply to both **Sandbox Runner** and **Host Runner** launches, including **Runtime Owner-Scoped Persistent Runtime** bridges.
- Editing a **Model Provider** does not change existing **Task Runtime Configurations** or an active **Runtime Continuation**.
- A **Model Provider** cannot be deleted while any **Runtime Profile** still references it, unless the operator explicitly confirms a deletion that clears the **Model Provider** reference and its pinned **Model Provider Protocol** from every referencing **Runtime Profile**.
- Historical task views read captured **Task Runtime Configurations** and **Model Provider Snapshots**, not live **Runtime Profiles** or live **Model Providers**.
- A **Runtime Profile** may be selected only when its Task or Session is created and cannot be switched for that Runtime Owner.
- A later **Model Provider** change belongs to **Runtime Turn Selection**, not Runtime Profile selection; when it requires new **Config Projection**, it creates a new Runtime Owner-local configuration version and captures a new **Model Provider Snapshot**.
- A **Task** may contain internal steps, but those steps are not separate **Tasks**.
- A **Task** has zero or more **Task Events**.
- A terminal **Task** may undergo **Task Deletion**.
- **Task Deletion** excludes the **Task** from normal task lists, detail routes, and dashboard counts while retaining only the durable state required for historical **Blackboard** and **Trusted Origin** integrity.
- A pending, running, or paused **Task** cannot undergo **Task Deletion**.
- A **Task Conversation** belongs to exactly one **Task**.
- Each user message in a **Task Conversation** initiates one **Runtime Turn**.
- Every **Runtime Turn** may select its **Model Provider**, model, and **Reasoning Effort** independently of the preceding turn.
- When preparing a subsequent **Runtime Turn**, its selection starts from the preceding **Runtime Turn Selection**; the initial turn starts from the launch-resolved selection.
- A **Runtime Turn Selection** applies at a provider-defined turn boundary and never changes an already-running internal reasoning step.
- A **Runtime Turn Selection** does not edit the selected **Runtime Profile** or another **Runtime Turn**.
- A **Runtime Turn Selection** applied through native Runtime controls is part of its **Runtime Turn** request and does not create a separate **Task Event**, audit record, or **Task Runtime Configuration Version**.
- A **Model Provider** and **Model Catalog** do not store model effort capability metadata; effort capability is treated as unknown before a **Runtime Turn**.
- CyberPenda passes **Requested Reasoning Effort** to the **Runtime** without pre-validating whether the selected model supports it.
- A **Runtime** rejection of **Requested Reasoning Effort** fails the affected **Runtime Turn** with an explicit error.
- A runtime-native effort downgrade is accepted; CyberPenda records **Effective Reasoning Effort** only when the **Runtime** reports it, otherwise the effective value remains unknown.
- **Reasoning Effort** has exactly five user-selectable values: `low`, `medium`, `high`, `xhigh`, and `max`; there is no Auto or Runtime Default choice.
- A missing stored **Reasoning Effort** resolves to `high` without requiring existing **Runtime Profiles** to be rewritten.
- Every **Runtime Turn** sends its resolved **Requested Reasoning Effort** explicitly instead of inheriting sticky effort state from the **Runtime**.
- A **Runtime Reasoning Entry** belongs to one **Runtime Turn** and does not prove the **Effective Reasoning Effort** used by that Turn.
- Every emitted raw reasoning delta is retained through a Runtime Reasoning Entry; when the provider emits no raw reasoning, an emitted provider summary may supply the entry text instead.
- Runtime Reasoning Entries retain provider order relative to messages and Tool activity, coalesce repeated projections of one stable provider item, and participate in the same bounded history and detail retrieval as other Transcript entries.
- A retained Runtime Reasoning Entry is included in the **Hosted Transcript Stream** and does not change the **Runtime Activity Indicator**.
- A **Task** keeps one **Runtime Plugin** family; changing from Codex, Claude Code, Pi, or Hermes requires a different **Task**.
- Codex and Claude Code apply model and **Reasoning Effort** changes natively when the **Model Provider** is unchanged.
- Native **Runtime Turn Selection** changes do not create a **Task Runtime Configuration Version**.
- A **Task Runtime Configuration Version** is created only when a later turn requires new **Config Projection**, such as a Codex or Claude Code **Model Provider** change.
- Changing the **Model Provider** for a Codex or Claude Code **Runtime Turn** creates a new **Task Runtime Configuration Version**, re-resolves credentials, repeats **Config Projection**, and restarts the **Runtime** before resuming the **Task Conversation**.
- Every Pi or Hermes task uses **Global Model Projection** rather than limiting model configuration or credentials to its initially selected **Model Provider**.
- A Pi or Hermes **Runtime Turn** switches **Model Provider**, model, and **Reasoning Effort** through runtime-native controls without restarting the **Runtime** when those controls exist; otherwise the Harness restarts the **Runtime**.
- Every Pi or Hermes **Runtime** can access every global **Launch-Ready Model Provider** API credential; **Project**, **Task**, and **Runtime Profile** boundaries do not reduce that credential set.
- **Global Model Projection** excludes Model Provider drafts and other providers that are not launch-ready for that **Runtime Plugin**; those exclusions do not fail **Preflight** unless the excluded provider is the task's initial selection.
- **Global Model Projection** is resolved when the Runtime starts and does not hot-reload later Model Provider, catalog, or credential changes; the next required projection and Runtime restart refreshes that set.
- User messages and runtime replies in a **Task Conversation** are represented as **Task Events**.
- **Harness Steering** actions are represented as **Task Events**.
- An **Accepted Steering** is durably dispatchable before acceptance is returned and eventually settles as `applied`, `failed`, or `action_required`.
- Daemon restart does not leave an **Accepted Steering** permanently pending; recovery resumes dispatch or records a terminal operator-actionable result.
- **Accepted Steering** is dispatched in first-in, first-out order for each Runtime Owner, with at most one active Steering dispatch for that owner.
- The Runtime Harness records a durable send-start fence before provider Steering. A pre-fence request may be sent after recovery; a post-fence request with no result becomes `action_required` and is not sent again automatically.
- Stop, **Task Finish**, or permanent Runtime loss settles queued **Accepted Steering** explicitly instead of discarding it.
- An `action_required` **Accepted Steering** exposes only reason-specific safe actions; an ambiguous post-fence request never offers generic Retry.
- A **Runtime Continuation** resumes from its **Task Goal**, **Scope**, current **Working Blackboard Snapshot**, open **Attempt** checkpoints, and any unconsumed **Harness Steering** without a separate summary or mechanical handoff packet.
- A Task conclusion is represented by current semantic outcomes and relationships in the **Blackboard**, not by a duplicate task-level conclusion record.
- A **Task Event** may summarize runtime output but should not store complete raw output dumps.
- **Harness Steering** may request **Run Controls** changes, but those changes apply only at a **Runtime Continuation** boundary.
- A **Task** has its own **Runtime Workdir**.
- **Tasks** do not share **Runtime Workdirs** by default.
- A replacement **Runtime Continuation** after a required **Config Projection** restart does not inherit the prior runtime's **Runtime Workdir** by default.
- A **Task** may override a selected **Runtime Profile** or **Project Defaults** Runner through explicit **Run Controls**, and the actual Runner is recorded as a task event.
- A **Task** uses **Config Projection** to prepare runtime configuration without mutating host runtime configuration.
- Every **Runtime** operates non-interactively; the **Runtime Harness** applies **Runtime Non-Interactive Defaults** to every Codex, Claude Code, and Hermes launch and continuation, regardless of whether the selected **Runner** is a **Sandbox Runner** or **Host Runner**.
- **Runtime Non-Interactive Defaults** control provider CLI interaction only; they do not expand **Scope**, replace **Host Runner Activation**, grant **Project Interface** authority, or bypass **Credential Reference** and **Preflight** checks.
- A **Config Projection** failure belongs to the affected Runtime Owner unless an explicitly selected **Runtime Profile** is itself invalid.
- A **Task** passes **Preflight** before its **Runtime** starts.
- **Preflight** does not create a **Runtime Profile**, **Runtime Configuration Snapshot**, Task, Session, or Continuation record.
- After successful **Preflight**, the Runtime Owner or Continuation and its **Runtime Configuration Snapshot** are stored atomically.
- A **Credential Reference** that cannot be resolved during **Preflight** prevents **Runtime** launch.
- A missing **Model API Key Environment Variable** value prevents **Runtime** launch during **Preflight**.
- A required **Model Provider Requirement** that cannot resolve a compatible **Model Provider Protocol** prevents **Runtime** launch during **Preflight**.
- A **Preflight** failure prevents **Runtime** execution.
- A **Task** runs under exactly one **Scope Snapshot**.
- A **Scope Snapshot** records historical authorization and does not change when current **Scope** later changes.
- A **Runtime** performs a **Task** but is not the whole **Pentest Agent**.
- A **Runtime Harness** launches, resumes, steers, and stops a **Runtime** without executing pentest tools itself.
- Built-in Codex, Claude Code, Pi, and Hermes **Runtimes** use a **Runtime Owner-Scoped Persistent Runtime** on both **Sandbox Runner** and **Host Runner** when their native session bridge is available.
- **Runtime Owner-Scoped Persistent Runtime** does not remove **Host Runner Activation** or weaken the separate Sandbox and Host execution boundaries.
- A plugin without a usable native session bridge may retain one-shot Runtime execution; normal process exit completes that one-shot **Task**.
- A **Runtime Owner-Scoped Persistent Runtime** remains active until the operator finishes, stops, archives, or otherwise closes its **Runtime Owner**, a required **Config Projection** restart replaces it, it fails, or daemon shutdown closes it.
- A **Runtime** cannot autonomously complete its **Task** through a **Project Interface**; a valid **Blackboard Finish Intent** closes only the current Continuation's Blackboard write protocol when its **Work Runtime Turn** settles.
- Source work after a **Blackboard Finish Intent** invalidates that intent and continues to advance **Semantic Debt Watermarks**.
- `blackboard_finish` reports that a **Blackboard Finish Intent** was recorded, not that the Runtime Continuation is already closed.
- Invalidating a **Blackboard Finish Intent** produces a bounded Runtime notice and requires a new finish intent before the Blackboard write protocol can close.
- A **Runtime Activity Indicator** reflects current Runtime liveness without creating a separate **Task Event**, audit record, or historical status.
- Runtime liveness has exactly four states: `live`, `offline`, `orphaned`, and `unknown`; a live Runtime separately reports turn activity as `busy` or `idle`.
- `orphaned` means the durable **Task** appears active but the current daemon cannot prove ownership of a live Runtime; `unknown` means liveness cannot currently be determined.
- **Runtime Activity Indicator** state comes from the daemon's current Runtime process or session ownership and health, not from durable Task status, native session identity, historical **Task Events**, or elapsed time.
- The operator interface displays durable **Task** lifecycle and the **Runtime Activity Indicator** as separate states.
- **Task Finish** marks the **Task** completed after Runtime shutdown and required Continuation reconciliation; **Stop** marks it stopped and remains resumable.
- **Task Finish** is available only when the **Runtime Activity Indicator** reports `live` with turn activity `idle`; an operator uses **Stop** to interrupt a `busy` Runtime.
- A durable active **Task** whose Runtime is confirmed `offline` becomes failed; one whose Runtime is `orphaned` becomes interrupted; `unknown` liveness warns the operator without changing Task lifecycle.
- Sending a new user message to a completed, failed, interrupted, or stopped **Task** resumes that Task and launches a replacement **Runtime Owner-Scoped Persistent Runtime**, preferring provider-native session recovery and otherwise rebuilding a fresh **Runtime Continuation** from Task-owned continuity state.
- **Task Finish** releases Runtime resources and records a completed lifecycle state without sealing the **Task Conversation**; a later user message may resume the same **Task**.
- An `orphaned` Runtime must be closed or proven absent before a replacement Runtime starts, preventing two live Runtimes from owning one **Task**.
- **Harness Steering** never changes the **Runtime Owner** identity. Provider-native same-turn steering appends operator input to the current active steerable **Work Runtime Turn** and keeps its Runtime Continuation; interrupt-then-replace creates a replacement **Work Runtime Turn** and changes the writable Runtime Continuation when the provider contract requires it.
- **Harness Steering** never rewrites completed reasoning, messages, or tool items. A provider-native same-turn steer may enqueue additional operator input while the active Runtime Turn is still running.
- A **Pending Blackboard Conclusion** may survive replacement of its source **Runtime Continuation**.
- Each **Conclusion Dispatch** belongs to exactly one **Pending Blackboard Conclusion** and is bound immutably to exactly one Runtime Continuation and source Runtime session.
- Replacing a Runtime Continuation never rewrites an earlier **Conclusion Dispatch**; recovery creates a new dispatch and retains the earlier dispatch outcome.
- Only one **Conclusion Dispatch** for a **Pending Blackboard Conclusion** may be active at a time.
- A new **Conclusion Dispatch** requires proven ownership of the current Runtime Owner-scoped Runtime and a writable replacement Runtime Continuation.
- A recovered **Conclusion Dispatch** reuses the source **Runtime Turn Selection**; if that selection cannot be projected safely, the **Pending Blackboard Conclusion** becomes `action_required`.
- Automatic conclusion and repair budgets belong to the **Pending Blackboard Conclusion** and do not reset when a new **Conclusion Dispatch** is created.
- Only the active **Conclusion Dispatch** may validate or apply a result. A late result from an earlier dispatch is retained as a terminal delivery outcome and cannot change Blackboard state.
- An `action_required` **Pending Blackboard Conclusion** exposes only recovery actions that are safe for its recorded delivery boundary; an acceptance-ambiguous dispatch never offers generic Retry.
- Legacy conclusion receipts become one **Pending Blackboard Conclusion** and one immutable historical **Conclusion Dispatch**. A new dispatch is created only when current Runtime ownership and writable authority are proven; otherwise the obligation becomes `action_required`.
- A **Project Navigation Projection** reads a fixed number of ordinary Tasks per Project plus the selected Task and every Task with a live busy Runtime; its Task work does not grow with total historical Task count.
- An unchanged Project navigation refresh does not resend the complete **Project Navigation Projection**.
- A **Runtime Owner Workspace** replaces its **Runtime Owner History Window** when the selected owner changes and never merges events from different owners.
- Initial Runtime Owner history uses a recent window bounded by item count and serialized size. Older Timeline and Transcript content uses backward paging, live tail updates do not move an operator who is reading older history, and rendered history remains bounded.
- A **Runtime** uses **Project Interfaces** to read or write **Project** state.
- A **CLI Fallback** has the same project semantics as any other **Project Interface**.
- Direct storage changes outside **Project Interfaces** require **Reconciliation** before they can affect **Current Truth** or a **Report**.
- A **Runtime** may propose a **Reconciliation Candidate** but must not automatically complete **Reconciliation**.
- A **Sandbox Runner** runs a **Runtime** inside a **Sandbox** and is the default **Runner**.
- A **Runner** may place a **Runtime** inside a **Sandbox**.
- A **Host Runner** runs outside a **Sandbox** and must be visible in the **Report**.
- A **Host Runner Activation** requires explicit operator activation before launch.
- A **Sandbox Runner** failure must not automatically fall back to the **Host Runner**.
- A **Sandbox** isolates runtime environment state but does not imply full network or command enforcement.
- A **Blackboard** belongs to exactly one **Project**.
- A **Blackboard** contains zero or more **Entities**, **Exploration Objectives**, **Attempts**, **Project Facts**, **Blackboard Relationships**, **Findings**, **Solutions**, and **Evidence Artifacts**.
- A **Task Goal** belongs only to its **Task** and is never projected as a Blackboard record.
- **Blackboard** contents are not shared across **Projects** by default.
- All **Runtimes** in the same **Project** share the same **Blackboard**.
- A **Runtime** writes important **Project Facts** during a **Task**, not only at task completion.
- An **Entity** identifies what Blackboard knowledge or work is about; its scope status does not grant testing authorization.
- A **Project Fact** has exactly one **Fact Key** within its **Project**.
- A **Fact Key** identifies the same **Project Fact** across updates.
- A conflicting write to an existing **Fact Key** automatically updates that **Project Fact**.
- A **Fact Key** update may change a fact's confidence, including downgrading a **Confirmed Fact** to a **Tentative Fact**.
- A **Fact Key** update preserves prior content and confidence as **Fact Versions**.
- A **Fact Key** update with an empty body preserves the existing **Project Fact** body unless body clearing is explicit.
- A **Record Merge** applies only to same-type **Project Knowledge** in one **Project**; **Current Work** is superseded or concluded instead of merged.
- A **Record Merge** atomically rewrites relationships, moves the source to **Semantic History**, and creates a **Blackboard Key Redirect**.
- A **Blackboard Key Redirect** is excluded from the **Runtime Blackboard Snapshot** and does not create separate **Project Knowledge** from its canonical record.
- Reads or writes through a **Blackboard Key Redirect** resolve to and report the canonical **Blackboard Key**.
- A **Project** has one project-level **Artifact Root**.
- A **Task** may have one **Task Artifact Root** under the project-level **Artifact Root**.
- A **Runtime Workdir** is task-local scratch space, while a **Task Artifact Root** stores retained task outputs.
- A **Deprecated Fact** remains in the **Blackboard** but is excluded from default current-truth views.
- **Current Truth** is derived from non-deprecated **Project Facts** and does not claim absolute certainty.
- An **Out-of-Scope Fact** may be part of **Current Truth** only with explicit scope status.
- A **Tentative Fact** may be part of **Current Truth** when its uncertainty is explicit.
- A **Tentative Fact** becomes a **Confirmed Fact** by updating the same **Fact Key** when adequate support exists.
- A **Runtime** sees current **Project Facts** in the **Runtime Blackboard Snapshot** and fetches full Project Fact bodies on demand by **Blackboard Key**.
- A **Blackboard Relationship** connects two existing records in the same **Project** and is versioned by its source key, type, and target key rather than an internal ID.
- The relationship vocabulary is `about`, `part_of`, `tests`, `produced`, `evidences`, `supports`, `contradicts`, `derived_from`, `depends_on`, `satisfies`, and `supersedes`.
- Every relationship type has a closed source-and-target endpoint matrix; combinations outside that matrix are invalid rather than accepted as generic graph edges.
- `about` connects an **Exploration Objective**, **Attempt**, **Project Fact**, **Finding**, **Solution**, or **Evidence Artifact** to the **Entity** it concerns.
- `part_of` connects an **Entity** child to an Entity parent or an **Exploration Objective** child to an Exploration Objective parent; each hierarchy is acyclic and does not propagate lifecycle state.
- `tests` connects an **Attempt** to the **Exploration Objective**, **Entity**, **Project Fact**, **Finding**, or **Solution** it directly evaluates.
- `produced` connects an **Attempt** to an **Entity**, **Exploration Objective**, **Project Fact**, **Finding**, **Solution**, or **Evidence Artifact** that it directly produced.
- `evidences` connects an **Evidence Artifact** to the **Project Fact**, **Finding**, or **Solution** that it directly proves.
- `supports` connects a **Project Fact** to another **Project Fact**, **Finding**, or **Solution** whose conclusion it strengthens.
- `contradicts` connects a **Project Fact** to another **Project Fact**, **Finding**, or **Solution** whose conclusion it weakens; it never changes lifecycle state by itself.
- `derived_from` connects an **Exploration Objective** to a source **Project Fact**, **Finding**, or **Solution**; a Project Fact to a source Project Fact or **Evidence Artifact**; or an Evidence Artifact to a source Evidence Artifact.
- `depends_on` connects one **Exploration Objective** to another prerequisite Exploration Objective, with the dependent as source; the Objective dependency graph is acyclic.
- `satisfies` connects a **Project Fact**, **Finding**, or **Solution** to the **Exploration Objective** it resolves; an Objective cannot resolve without an incoming satisfies relationship.
- `supersedes` connects a replacement **Entity**, **Exploration Objective**, **Project Fact**, **Finding**, **Solution**, or **Evidence Artifact** to a replaced record of the same type; it is acyclic and each replaced record has at most one current replacement.
- Every **Blackboard Relationship** forbids a self-link. The `part_of`, `derived_from`, `depends_on`, `supersedes`, and Project-Fact-to-Project-Fact `supports` subgraphs are each acyclic; reciprocal `contradicts` relationships remain valid.
- `blocks` is expressed by reversing `depends_on`; `leads_to` is expressed through current relationships plus an **Attack Chain** Project Fact and report narrative.
- A `contradicts` relationship does not automatically turn a **Project Fact** into a **Deprecated Fact**.
- An **Exploration Objective** belongs to exactly one **Project**.
- An **Exploration Objective** may be derived from one or more source **Project Facts**, **Findings**, or **Solutions**.
- An **Exploration Objective** is not a **Project Fact**, **Blackboard Relationship**, **Finding**, **Task**, or **Attack Chain**.
- An **Exploration Objective** may become or inform a **Task Goal**, but the **Task Goal** is the launch objective for one **Task**.
- An **Exploration Objective** does not link to a copied **Task Goal** in the Blackboard.
- Resolving an **Exploration Objective** may produce multiple **Project Facts**, **Findings**, **Solutions**, or **Evidence Artifacts**.
- A **Runtime Blackboard Snapshot** includes open **Exploration Objectives** but excludes resolved, abandoned, or superseded Objectives.
- Before a terminal **Exploration Objective** leaves Runtime context, every reusable conclusion or abandonment reason is represented by a linked semantic record; a superseded Objective points to its active replacement.
- An **Attempt** may produce **Entities**, **Project Facts**, **Findings**, **Solutions**, **Evidence Artifacts**, or new **Exploration Objectives**.
- A **Runtime Blackboard Snapshot** includes open **Attempts** but excludes terminal Attempts.
- Before a terminal **Attempt** leaves Runtime context, every reusable positive, negative, blocked, or inconclusive outcome is represented by a linked **Project Fact**, **Finding**, **Solution**, **Evidence Artifact**, or **Exploration Objective**.
- **Current Work** includes only active work records; terminal or superseded work records leave Runtime context after their reusable outcomes are represented.
- A **Current Work** record exposes only its current version, explicit status, primary semantic text, and an optional concise rationale that adds new meaning.
- **Project Knowledge** persists across Tasks while current and leaves Runtime context when deprecated, superseded, false-positive, or otherwise explicitly invalidated.
- Before invalidated **Project Knowledge** leaves Runtime context, every reusable invalidation reason is represented by a concise **Project Fact** or current replacement record; a superseded record identifies its replacement, while an invalidation with no reusable meaning need not manufacture an empty Fact.
- An **Attack Chain** uses **Project Facts**, **Blackboard Relationships**, and **Findings** without becoming a separate graph source of truth.
- A stable **Attack Chain** summary is stored as a **Project Fact**.
- A **Runtime Blackboard Snapshot** preserves every current reusable semantic record and relationship without relevance filtering or truncation.
- Completeness of a **Runtime Blackboard Snapshot** means every current reusable record and relationship is represented, not that every auxiliary text field or proof payload is inlined.
- A **Runtime Blackboard Snapshot** consists of **Current Work**, **Project Knowledge**, and the current semantic relationships among them.
- A **Runtime Blackboard Snapshot** groups **Current Work** and **Project Knowledge** by record type, using each **Blackboard Key** as the record's map key; it does not repeat record keys or types inside records.
- A **Runtime Blackboard Snapshot** does not carry separate Frontier or Current Truth key lists because its work and knowledge sections, record types, and semantic state already express those classifications.
- A **Runtime Blackboard Snapshot** states every lifecycle or validation status that affects reasoning or a legal semantic transition explicitly; membership in **Current Work** or **Project Knowledge** never substitutes for that status.
- A **Runtime Blackboard Snapshot** is self-describing: it identifies its schema and graph revision and states that work is active, knowledge is current, and excluded history or details remain available through **Blackboard Keys**.
- A **Launch Blackboard Pin** is stored exactly for its **Runtime Continuation**; recovery reads that pin rather than replaying a historical graph ledger.
- A **Working Blackboard Snapshot** starts from the **Launch Blackboard Pin**, advances after the Runtime's own successful semantic writes, and advances to current shared state when an external change is synchronized.
- A **Blackboard Change Notice** carries last-acknowledged and current graph revisions without Task identities or changed record content, and remains pending until the latest **Runtime Blackboard Snapshot** is delivered.
- The first trusted tool or checkpoint response after a pending **Blackboard Change Notice** includes the complete current **Runtime Blackboard Snapshot**, updates the **Working Blackboard Snapshot**, and gives one concise explanation that another Task changed shared project knowledge.
- A normal response with no unseen external change returns only its semantic delta and the updated Working Snapshot path/revision rather than repeating the complete Snapshot.
- A **Project Interface** accepts Blackboard writes as a **Semantic Change Batch** using create, update, transition, relate, unrelate, merge, or supersede changes addressed by Blackboard Key.
- A **Semantic Change Batch** carries one idempotency key and current semantic versions where required, while Project and **Trusted Origin** bindings remain server-owned.
- A **Runtime Blackboard Snapshot** excludes **Trusted Origin** data, audit history, and audit-only metadata from Runtime context.
- **Task Launch** supplies the current **Task Goal** separately from the **Runtime Blackboard Snapshot**.
- A **Runtime Blackboard Snapshot** belongs to exactly one **Project**; all record and relationship references resolve only within that Project, and cross-Project references are invalid.
- A **Runtime Blackboard Snapshot** identifies records and relationship endpoints with **Blackboard Keys** rather than database IDs.
- A **Runtime Blackboard Snapshot** uses an explicit allowlist of fields required for Runtime reasoning or semantic mutation; new Blackboard or storage fields are excluded until deliberately admitted.
- A **Runtime Blackboard Snapshot** includes time only when it changes the security meaning of a record, such as observation, expiry, capture, or authorization validity; record creation, update, recording, resolution, and lifecycle-transition timestamps are excluded.
- Every record in a **Runtime Blackboard Snapshot** has a compact summary that stands on its own together with its type-specific semantic state; auxiliary body, detail, reproduction, and evidence content is fetched on demand through its **Blackboard Key**.
- Compact primary text and optional explanation fields have hard size limits; a write that exceeds them must move supporting detail to on-demand record content or an **Evidence Artifact** rather than expanding Runtime context.
- A relationship in a **Runtime Blackboard Snapshot** is represented by its source **Blackboard Key**, relation type, and target Blackboard Key without a restating summary.
- Only `supports`, `contradicts`, and `depends_on` may add a concise non-redundant reason.
- Every record in a **Blackboard** has one **Blackboard Key**, and no two records in that Blackboard share the same key even when their types differ.
- A **Blackboard Key** may contain an external domain identifier when that identifier is part of the record's meaning, but never an internal infrastructure identifier used only to manufacture uniqueness.
- **Semantic History** is organized by record or relationship identity and semantic version, not by storage mutation, operation, actor, or source event.
- **Semantic History** is retained until an explicit safe prune; active **Launch Blackboard Pins** and records required by **Blackboard Key Redirects** protect their referenced history from pruning.
- A **Finding** has exactly one **Finding Key** within its **Project**.
- A **Finding Key** identifies the same reportable issue across updates.
- A conflicting write to an existing **Finding Key** automatically updates that **Finding**.
- A **Finding Key** update preserves prior finding state as **Finding Versions**.
- **Findings** on different assets or entry points remain separate and may appear in a **Finding Group** instead of a **Record Merge**.
- A **Finding Group** may have aggregate severity without changing the severity of individual **Findings**.
- A **Finding** may be supported by zero or more **Project Facts** and **Evidence Artifacts**.
- A **Solution** belongs only to a CTF Challenge Project; verified flag **Solutions** determine current solved state without replacing Task status.
- A **Challenge Workflow** owns challenge Attempt identity and uses one **Platform Adapter** per configured Challenge Platform.
- A **Challenge Operation** is replay-safe within one Task and external Attempt.
- Challenge claim, submit, and abandon responses become **Evidence Artifacts** through system retention, not through Task Event text.
- A successful or abandoned challenge Attempt remains a **Finish Blocker** until the **Challenge Workflow** finalizes it.
- **Finish Readiness** does not perform Task Finish and does not collapse Task lifecycle with Runtime activity.
- **Task Finish** is rejected while any **Finish Blocker** exists.
- A **Finding** uses a **CVSS Vector** to derive severity.
- A **CVSS Vector** records its **CVSS Version**.
- A **Finding** without a complete **CVSS Vector** is **CVSS Pending**.
- A **Confirmed Finding** must be supported by **Confirmed Facts** or **Evidence Artifacts**.
- A **Confirmed Finding** must have a complete **CVSS Vector**.
- A **Confirmed Finding** must include target, proof, impact, and recommendation.
- A **Finding Update** preserves unspecified fields while allowing the finding to be completed over time.
- A suspected issue becomes a **Finding** only when it has a target, entry point, impact hypothesis, and validation path.
- Marking a **Finding** as false-positive does not automatically turn supporting **Project Facts** into **Deprecated Facts**.
- The server retains minimal **Trusted Origin** bindings for **Project Facts**, **Findings**, and **Evidence Artifacts**, but Blackboard records and **Reports** do not carry or present those bindings as content.
- An **Evidence Artifact** supports interpretation but is not itself a **Project Fact**.
- **Fact Key** updates preserve existing **Evidence Artifact** links unless a later action explicitly changes them.
- An **Evidence Artifact** references content under an **Artifact Root**.
- An **Evidence Artifact** may reference a **Task Artifact Root** to preserve task provenance.
- **Runtime Workdir** files become **Evidence Artifacts** only when explicitly attached or retained.
- Complete raw runtime or tool output is stored as logs or **Evidence Artifacts**, not as **Task Events**.
- A rejected **Scope Expansion** may leave an **Out-of-Scope Fact** for context and audit.
- A **Policy Violation** marks the affected **Task** but does not automatically pause the whole **Project**.
- Direct runtime writes to storage outside **Project Interfaces** are recorded as **Policy Violations** when detected.
- **Task Events** explain what happened inside one **Task**.
- A **Report** presents project conclusions but is not itself the source of truth for **Findings** or **Project Facts**.
- A **Report** does not expose **Trusted Origin** or expanded task-execution metadata by default.
- A **Report** distinguishes **Tentative Facts** from confirmed conclusions.
- A **Report** may include unconfirmed **Findings** separately from **Confirmed Findings**.

## Example dialogue

> **Dev:** "The Runtime discovered a new subdomain during a task. Should it write that straight into Scope?"
> **Domain expert:** "No. It should create a Scope Expansion request. If accepted, the Project Scope changes and the decision is recorded."

> **Dev:** "The Runtime confirmed SQL injection and saved the HTTP exchange. Is that a Project Fact or a Finding?"
> **Domain expert:** "Both can be involved: the reproducible issue is a Finding, and the reproduction context can be stored as Project Facts with Evidence Artifacts attached."

> **Dev:** "Task launch no longer asks for a Runtime Profile. Where do MCP and skills come from?"
> **Domain expert:** "Most launches only need Launch Selection: runtime, model provider, and model. Preflight resolves that selection directly into a Runtime Owner-local Runtime Configuration Snapshot. If the user explicitly chooses a Runtime Profile, that Profile's MCP and Skill configuration apply. Runtime and model provider lock to the Profile at launch, but the user may still set a Launch Model Override for just that task."

## Flagged Ambiguities

- Disabled Blackboard is not Interactive Blackboard or read-only Blackboard; resolved: it fully disconnects the Runtime Owner from Blackboard context, authority, conclusion handling, and reconciliation while the Runtime may keep an **Agent-Managed Trace**.
- **Agent-Managed Trace** is not a file-backed Blackboard protocol; resolved: startup may remind the Runtime to use files, but CyberPenda defines no filename, format, parser, or cross-Runtime migration.
- An **Agent-Managed Trace** reminder is not repeated conversation context or a lifecycle warning; resolved: issue a short state-file reminder at initial and replacement Runtime launch, not on ordinary Runtime Turns, without explaining handoff limits.
- Disabled Blackboard is not a reduced Project Interface; resolved: no built-in Project Interface is injected, while Task Goal, Scope Snapshot, Task Policy, Transcript, and ordinary attachments remain available through their normal owner boundaries.
- Disabled Blackboard is not a new API authorization layer; resolved: the Runtime receives no Blackboard grant or credential, ordinary authorization rejects its ungranted calls, and operator-authorized Blackboard actions remain valid without a `blackboard_disabled` error.
- A disabled Blackboard state is not `clean` or `empty`; resolved: label it `Disabled`, hide inapplicable Blackboard controls in the Runtime Owner Workspace, and retain the Project Dashboard's project-level Blackboard.
- Disabled Runtime output is not permanently barred from Project Knowledge; resolved: the operator may explicitly retain a file as Evidence or start Reconciliation outside the Runtime without feeding Blackboard state back to it.
- Disabled Runtime output is not an automatic Report source; resolved: Reports remain Blackboard-based, and Transcript or workdir content enters that source only through explicit operator retention or Reconciliation.
- Disabled Blackboard is not the new default or a Task-only option; resolved: `interactive` remains the default, and both Project Tasks and Non-Project Sessions may explicitly select `disabled`.
- **Blackboard Mode** is not a per-Continuation toggle; resolved: the Runtime Owner captures it at creation and every Resume or Continuation inherits it.
- Disabled Task Finish is not blocked by state the Runtime is forbidden to settle; resolved: **Finish Readiness** skips Blackboard-specific blockers and retains ordinary lifecycle and policy checks.
- A **Pending Blackboard Conclusion** is not one mutable delivery receipt; resolved: it is the stable semantic obligation, while each **Conclusion Dispatch** keeps immutable Runtime Continuation and source-session identity.
- **Blackboard Finish Intent** is not an immediate close inside a busy **Work Runtime Turn**; resolved: it closes the Blackboard write protocol only at Turn settlement and later source work invalidates it.
- **Accepted Steering** is not durable message storage alone; resolved: the Runtime Harness owns eventual settlement as `applied`, `failed`, or `action_required`, including after daemon restart.
- A bounded **Project Navigation Projection** is not request-count-only optimization; resolved: Task query work and response content are bounded per Project independently of total Task history, with current and busy exceptions retained.
- Initial **Runtime Owner Workspace** history is not the complete Timeline and Transcript; resolved: load and render a bounded recent **Runtime Owner History Window**, with older history available on demand.
- Runtime replacement is not a new automatic-conclusion budget; resolved: every **Conclusion Dispatch** for one **Pending Blackboard Conclusion** shares the obligation's bounded budget and source **Runtime Turn Selection**.
- Ambiguous provider Steering is not replayed after restart; resolved: a durable send-start fence separates safe pre-send recovery from post-send `action_required` settlement.
- Legacy stuck conclusion receipts are not silently rebound or left on the old model; resolved: preserve the historical dispatch and create a safe replacement dispatch only with proven current Runtime ownership.
- A Non-Project **Session** is not presented through a separate chat UI; resolved: **Session** and **Task** reuse the same **Runtime Owner Workspace**, with owner-specific API and lifecycle adapters at the boundary.

- "vulnerability" and **Finding** were used for the same reportable issue concept; resolved: use **Finding** as the product/domain term and reserve "vulnerability" for type names, schemas, or imported source terminology.
- **Policy Violation** is not an approval state; resolved: it records a workflow breach that may be detected after the fact.
- **Policy Violation** is not automatic project suspension; resolved: flag the affected task strongly and leave project-level pause decisions to a human.
- Direct storage mutation is not trusted project state; resolved: use **Reconciliation** before such content affects **Current Truth** or reports.
- **Reconciliation** is not runtime self-approval; resolved: runtime-discovered candidates stay untrusted until accepted by a human or explicit project policy.
- **Host Runner Activation** is not implicit host fallback; resolved: host execution requires explicit activation and must be visible in report output.
- **Sandbox Runner** failure is not permission to use **Host Runner**; resolved: host execution requires explicit **Host Runner Activation**.
- **Deprecated Fact** is not deleted history; resolved: deprecated facts remain in **Semantic History**, while reusable invalidation meaning stays current through a replacement or separate Project Fact.
- **Current Truth** is not the whole **Blackboard**; resolved: it is the default working set that excludes **Deprecated Facts**.
- A current **Project Fact** in the **Runtime Blackboard Snapshot** is compact context, not full proof content; resolved: runtimes fetch full bodies only when needed.
- **Project Fact** does not mean confirmed fact; resolved: reusable but uncertain observations are **Tentative Facts**, while non-reusable noise stays in **Task Events** or logs.
- **Fact Version** is not separate current truth; resolved: current views use the latest fact state while history remains inspectable.
- **Confirmed Fact** is not a model assertion; resolved: confirmation requires evidence, reproduction, human confirmation, or independent corroboration.
- **Fact Key** is not a database ID or summary; resolved: it is the stable project-local identity used for fact updates and deduplication.
- **Fact Key** conflict handling is automatic overwrite; resolved: a new write to the same key updates the existing **Project Fact** rather than creating a review queue.
- **Confirmed Fact** status is not permanent; resolved: later writes to the same **Fact Key** may change confidence when they do so explicitly.
- Empty fact body updates do not erase detail; resolved: preserve the existing body unless the write explicitly clears it.
- **Fact Key** generation is not fully automatic in MVP; resolved: runtimes may propose keys, while naming rules and **Record Merge** handle cleanup.
- **Record Merge** is not silent overwrite; resolved: it preserves **Semantic History** and relationship context while consolidating same-type Project Knowledge.
- A **Blackboard Key Redirect** is not an independent record identity; resolved: a merged source key resolves to its canonical key without producing separate current knowledge.
- `contradicts` in a **Blackboard Relationship** does not decide truth by itself; resolved: deprecating a fact requires an explicit judgment.
- A **Blackboard Relationship** is not a separate finding graph; resolved: Findings participate through supporting **Project Facts** and **Evidence Artifacts**.
- Cairn-style Intent is not imported as an attack-graph edge; resolved: use **Exploration Objective** for durable open investigation directions.
- **Exploration Objective** is not a **Blackboard Relationship**; resolved: relationships connect existing records, while objectives represent work toward an unknown conclusion.
- **Exploration Objective** is not a **Task Goal**; resolved: objectives may inform task launch, but a **Task Goal** belongs to one launched **Task**.
- **Exploration Objective** is not **Current Truth**; resolved: it is planning state linked to facts, not a reusable assertion.
- **Finding Key** is not a **Fact Key**; resolved: facts and reportable issues have separate stable identities.
- **Finding Version** is not a duplicate finding; resolved: current finding views use the latest state while history remains inspectable.
- **Finding Key** generation is not fully automatic in MVP; resolved: runtimes may propose keys, while naming rules and **Record Merge** handle cleanup.
- **Record Merge** is not cross-asset finding grouping; resolved: related findings on different assets or entry points stay separate and can be grouped for presentation.
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
- **Blackboard** is not separated by **Runtime Profile**; resolved: runtime-specific source context remains internal **Trusted Origin** data rather than Blackboard knowledge.
- **Blackboard** writing is not only end-of-task summarization; resolved: runtimes should write durable facts during task execution, with final summaries used only as cleanup or gap filling.
- **Audit Log** history is append-only; resolved: corrections and reversals are represented as later events.
- **Scope Snapshot** is distinct from current **Scope**; resolved: task history uses the snapshot captured at task start.
- **Runtime Profile** default **Runner** is not final task truth; resolved: the **Task** records the actual **Runner** used.
- **Runtime Profile** edits are not retroactive task edits; resolved: existing tasks use captured **Task Runtime Configuration** unless explicitly refreshed with audit history.
- **Runtime Profile** is not project-local; resolved: profiles are global, while each **Task** captures the runtime configuration it actually used.
- **Runtime Profile** is not the primary task-launch picker; resolved: most launches use **Launch Selection**, while an explicitly selected Runtime Profile is an optional advanced choice.
- **Profile Selector** is not the default launch control; resolved: task launch uses **Launch Selection** plus an optional **Launch Profile Selector**, and settings-page editing keeps **Profile Selector**.
- **Launch Configuration Resolution** is not global profile matching; resolved: it creates a Runtime Owner-local **Runtime Configuration Snapshot**, and only an explicitly selected **Runtime Profile** contributes Profile configuration.
- A **Launch Model Override** is not a **Runtime Profile** edit; resolved: it changes only the launching task's captured model choice and snapshot.
- **Project Defaults** are not implicit Profile selection; resolved: each launch explicitly selects a **Runtime Profile** or uses direct **Launch Selection**.
- **Model Provider** is not a **Runtime Profile**; resolved: model-service configuration is globally reusable across runtime profiles and does not store credential values.
- **Model Provider ID** is not a display name; resolved: display names may change, while provider IDs stay stable to preserve generated environment variable names.
- **Model Provider ID Generation** is not user-controlled ID entry; resolved: IDs are generated from display names at creation time and then locked.
- **Model Provider ID Generation** collision handling is automatic; resolved: duplicate display names receive numeric suffixes and environment variables derive from the final ID.
- **Model API Key Environment Variable** is not regenerated on display-name edits; resolved: it follows the immutable provider ID.
- **Model Providers Page** is not a runtime profile editor section; resolved: model providers are managed as global settings alongside runtime profiles and credentials.
- A missing **Model Provider** on a model-required **Runtime Profile** is allowed as a draft configuration; resolved: validation surfaces it and **Preflight** blocks launch.
- **Model Provider Migration** is not silent schema guessing; resolved: users explicitly confirm migration from legacy runtime-profile model fields into model providers.
- **Model Provider Migration Preview** is not a secret display; resolved: it shows proposed provider name, base URL, model, protocol, and API key source provenance but not key values.
- **Model Provider Migration Match** is not automatic provider reuse; resolved: possible matches are shown for user choice rather than merged silently.
- **Model Provider Migration** does not leave dual model-service sources of truth; resolved: migrated legacy fields are cleared from the runtime profile after successful migration.
- **Model Provider Protocol** names are not marketing compatibility labels; resolved: use concrete API contracts such as `openai_chat_completions`, `openai_responses`, and `anthropic_messages`.
- **Model Provider Protocol** is not a runtime family; resolved: protocol compatibility connects reusable model-service configuration to runtime-specific projection.
- **Model Protocol Preference** is not user-configured; resolved: runtime plugin manifests define each runtime family's protocol support and ordering.
- **Model Provider Protocol** support is not auto-detected or stored separately; resolved: users explicitly configure supported protocols through **Model Provider Endpoints**.
- Provider-level `protocols` is not part of the new **Model Provider** API shape; resolved: derive provider protocol support from `endpoints[].protocol` and read old `protocols` only for **Model Provider Endpoint Backfill**.
- Empty **Model Provider Protocol** support is allowed for provider configuration; resolved: it is a management draft state, not launch-ready task configuration.
- Removing **Model Provider Protocol** support is not blocked by affected runtime profiles; resolved: save the provider change and surface invalid strict pins through validation and preflight.
- **Model Provider Endpoint** is not a protocol-only marker; resolved: each endpoint binds one **Model Provider Protocol** to one **Model Protocol Base URL**.
- **Model Provider Endpoint** is not a full operation URL; resolved: it records the base URL the runtime consumes before adding its operation suffix.
- **Model Provider Endpoint** normalization is not optional; resolved: remove trailing slashes before endpoint validation, storage, migration, and backfill derivation.
- **Model Provider Endpoint** validation is not protocol-local suffix checking; resolved: reject every known **Model Operation Suffix**, including versioned and unversioned forms, instead of stripping it.
- **Model Provider Endpoint** validation is not semantic URL repair; resolved: report operation-suffixed values rather than silently rewriting them.
- **Model Provider Endpoint** is not forced to be a provider-wide singleton; resolved: one model provider may have protocol-specific endpoints under one shared model-service configuration.
- **Model Protocol Base URL** is not necessarily only a host-level URL; resolved: it may include a protocol path prefix when providers expose different API families under one origin.
- **Model Endpoint Origin** and **Model Protocol Path Prefix** are not separate canonical storage fields; resolved: they may be used by the **Model Providers Page** as editing helpers, while the saved endpoint value remains `base_url`.
- **Model Provider Endpoint Defaults** are not stored provider configuration; resolved: quick setup composes endpoint `base_url` values from one shared provider base URL, derives Anthropic by removing the final path segment, then per-protocol overrides edit the composed endpoint list.
- **Model Provider Endpoint** storage is not keyed by **Model Provider Protocol**; resolved: store an `endpoints` list of `{protocol, base_url}` records.
- **Model Provider Endpoint Backfill** is not a user-confirmed **Model Provider Migration**; resolved: it is compatibility interpretation of existing provider records.
- **Model Provider Endpoint Backfill** is not regex-based version detection; resolved: `anthropic_messages` removes the final non-empty path segment by splitting the normalized URL path on `/`.
- **Model Provider Endpoint Backfill** is not arbitrary URL repair; resolved: only the explicit Anthropic final-segment adaptation changes the old `base_url`.
- **Model Provider Endpoint** is not proxy configuration; resolved: endpoints carry base URLs and protocols, not custom headers or arbitrary request-shaping settings.
- **Normalized Model Protocol Base URL** is not the model-list URL; resolved: preserve provider path prefixes for **Model Runtime Projection**, while **Model Catalog Refresh** derives a **Model Catalog Refresh URL** from an OpenAI-family endpoint origin plus `/v1/models`.
- **Normalized Model Protocol Base URL** is not semantically repaired during normal provider editing; resolved: outside explicit legacy migration/backfill rules, do not detect, reject, or trim provider path prefixes.
- **Model Provider Endpoint** selection is not guessed at task runtime; resolved: runtime plugins resolve a compatible protocol, then use the endpoint base URL configured for that protocol.
- **Model Protocol Preference** is ordered fallback, not a single default; resolved: the runtime plugin chooses the first compatible protocol.
- Empty **Model Provider Protocol** pin is not an invalid profile; resolved: it means runtime-plugin preference should resolve the protocol.
- **Protocol Pin Selector** is not a list of every known protocol; resolved: it shows only compatible choices plus Auto.
- A pinned **Model Provider Protocol** is not a preference; resolved: incompatible or deleted pins fail validation instead of falling back silently.
- **Model Catalog** is not endpoint-specific; resolved: the model provider exposes one shared model catalog.
- **Model Catalog** is not raw provider metadata; resolved: store model identifiers only and discard unrelated `/models` response fields.
- **Model Catalog** is not limited to refreshed models; resolved: users may manually add model identifiers when `/models` is unavailable or incomplete.
- **Manual Model Entry** is not duplicated after refresh; resolved: if refresh returns the same model identifier, treat it as a refreshed entry.
- **Manual Model Entry** deletion applies only while the entry remains manual; resolved: entries returned by refresh become refreshed entries and are not manually deleted.
- Refreshed model entries are not user-curated; resolved: keep provider-returned model identifiers as returned by refresh.
- Model selection does not depend on catalog entry source; resolved: manual and refreshed model identifiers are both selectable.
- **Model Catalog Refresh** is not automatic model discovery during task startup; resolved: refresh happens only through an explicit management action that fetches a derived **Model Catalog Refresh URL** ending in `/v1/models`.
- **Model Catalog Refresh URL** is not derived from an arbitrary protocol URI path; resolved: prefer the `openai_chat_completions` endpoint origin, then `openai_responses` endpoint origin, and append `/v1/models`.
- **Model Catalog Refresh URL** is not derived from OpenAI-family runtime path prefixes; resolved: an endpoint such as `https://open.bigmodel.cn/api/coding/paas/v4` refreshes from `https://open.bigmodel.cn/v1/models`.
- **Model Catalog Refresh URL** is not custom provider configuration; resolved: there is no `catalog_base_url` field and the model-list path is always `/v1/models`.
- **Model Catalog Refresh** does not use a separate credential path; resolved: it reads the same generated API key environment variable as runtime launch.
- **Model Catalog Refresh Format** is not provider-specific in MVP; resolved: parse only OpenAI-style `/v1/models` responses.
- A failed **Model Catalog Refresh** does not clear model choices; resolved: keep the previous catalog and surface the refresh error.
- A successful **Model Catalog Refresh** is not blocked by stale selections; resolved: save the provider's returned list and let validation surface invalid defaults or model overrides.
- An empty **Model Catalog** is allowed for provider configuration; resolved: it is a management draft state, not launch-ready task configuration.
- **Model Override** is not a **Model Provider** edit; resolved: provider defaults stay reusable while runtime profiles may choose a different model for their tasks.
- Invalid model selection is not auto-healed; resolved: missing provider defaults or stale **Model Overrides** fail validation instead of selecting another model.
- Model API key configuration does not belong to **Runtime Profiles**; resolved: model-service API key source is provider-level while runtime profiles keep only runtime-specific credential needs.
- A **Model Provider** does not have zero or multiple model API keys; resolved: each provider has exactly one **Model API Key Source**.
- **Model API Key Source** is not project-overridable in the model-provider flow; resolved: keep one provider-level API key source and let runtime plugins project it.
- **Model API Key Source** is not an inline secret store in MVP; resolved: model providers use a generated environment variable name, not an API key value.
- **Model API Key Environment Variable** is not user-named in MVP; resolved: derive it from the provider identifier, such as `MIMO_API_KEY`.
- **Model Credential Projection** is not a separate credential; resolved: the same provider API key may be injected differently for different runtime plugins.
- **Model Provider Snapshot** is not a live provider reference; resolved: task runtime configuration captures only the non-secret values used for that launch or continuation.
- **Model Provider Snapshot** `base_url` is not the canonical new snapshot field; resolved: `endpoint_base_url` names the selected endpoint base URL, with `base_url` only as a compatibility alias during transition.
- **Model Runtime Projection** is not LLM proxying; resolved: the daemon derives and passes a runtime-specific URL, protocol, model, and credential into the runtime, and the runtime calls the model service directly.
- **Model Provider Requirement** is not universal; resolved: runtime plugins declare whether model-provider resolution is required, optional, or unsupported.
- Runtime Profile selection is not a Steering control; resolved: a **Runtime Profile** is fixed when the Runtime Owner is created, while **Runtime Turn Selection** may change Model Provider, model, or Reasoning Effort.
- A Runtime Turn Model Provider change does not reuse the prior **Model Provider Snapshot** when new **Config Projection** is required; resolved: the new Runtime Owner-local configuration version captures its own resolved provider values.
- **Model Provider** edits are not live task mutation; resolved: active continuations keep the **Model Provider Snapshot** captured at launch or continuation start.
- **Model Provider** deletion is not silent profile breakage; resolved: deletion is blocked by default and names the referencing runtime profiles; an explicit operator-confirmed deletion clears the provider reference and its pinned protocol from every referencing runtime profile in the same action.
- Historical task inspection does not require live profile or provider records; resolved: task history uses captured runtime configuration snapshots.
- **Project Defaults** are not copied **Runtime Profiles**; resolved: they select defaults while profiles remain global.
- **Project Dashboard** is not a chat-first view; resolved: the project home prioritizes scope, task runs, blackboard state, findings, and evidence.
- **Task Goal** is not the whole task configuration; resolved: natural-language goals are paired with visible **Run Controls**.
- **Harness Steering** is not direct pentest tool control; resolved: it controls one **Runtime Owner** through the **Runtime Harness**, and provider-native same-turn steering may append operator input only to the current active steerable **Work Runtime Turn**.
- **Runtime Continuation** is not live thought editing; resolved: provider-native same-turn steering may enqueue new operator input into an active steerable Runtime Turn without rewriting completed items, while interrupt-then-replace applies at a replacement boundary.
- **Harness Steering** is not silent run-control mutation; resolved: runner, profile, or other run-control changes apply through explicit task events and only at continuation boundaries.
- **Profile Selector** is not raw configuration editing; resolved: switching profiles is fast, while editing profiles remains structured.
- **Generated Runtime Config** is not the editable source of truth; resolved: raw config preview and diff are derived from structured profile fields.
- **Generated Runtime Config** is not a secret preview; resolved: show generated API key environment variable names and projection targets, not API key values.
- **Profile Config Import** is not raw passthrough; resolved: edited config must round-trip into structured profile fields before saving.
- **MCP Configuration** is not raw JSON as source of truth; resolved: manage entries structurally and use raw JSON only for preview or import.
- **External MCP Server** is not a Blackboard interface; resolved: it remains ordinary Runtime configuration and never receives Blackboard authority automatically.
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
- **Runtime Extension Bundle** is not a manifest-only pointer; resolved: uploaded, edited, and imported skills keep file-backed content that can be projected into task-local runtime boundaries.
- **Skill ID** is not a display name or source URL; resolved: it is the stable identity used for enablement and repeated imports.
- **Skill Source Provenance** is not skill identity; resolved: provenance supports display, update, and audit while **Skill ID** controls identity.
- **Runtime Extension Library** is not project-local skill storage; resolved: uploaded, edited, and discovered runtime extensions are globally reusable.
- Duplicate **Runtime Extension Import** is not copy-by-default; resolved: an existing **Skill ID** is updated unless the user chooses a different identity.
- **Skill** compatibility is not runtime-specific; resolved: skills are compatible with all supported runtimes, while runtime-specific plugins belong to their runtime family.
- **Skill Bundle Format** is not provider-native plugin format; resolved: provider-native plugins are **Runtime-Specific Extensions**.
- **Skill Bundle Edit** is not raw manifest editing; resolved: users edit bounded bundle content and structured metadata.
- **Built-in Skill** is not a live remote install; resolved: packaged content is bundled with the daemon and seeded into the normal editable **Runtime Extension Library**.
- Skills do not define credential needs; resolved: credential and environment resolution stays with **Runtime Profiles**, launch requests, and **Credential Bindings**.
- **Skill Execution Boundary** is not expanded by enabling a skill; resolved: skills do not bypass scope, runner, credential, or project-interface controls.
- **Skill Deletion** is not silent profile breakage; resolved: deletion is blocked while enabled unless the user explicitly deletes and disables everywhere.
- **Skill Event** is not a **Task Event**; resolved: skill management changes are project-level records, not a task-local runtime timeline.
- **Skill Preflight Preview** is not hidden runtime context; resolved: enabled skills and their blockers are visible during task launch checks.
- **Task Skills Root** is not global skill installation; resolved: each task receives its own materialized enabled skills.
- **Skills Page** is not provider-specific plugin management; resolved: runtime-specific plugins belong to their own runtime family.
- **Skills Page** is not a project tab; resolved: it belongs in global navigation alongside runtime profile and credential management.
- **Default Skill Enablement** is not runtime-specific plugin injection; resolved: skills are default-on for runtime profiles with per-profile opt-out, while runtime-specific extensions remain explicit.
- **Skill Opt-Out** is not reset by a skill update; resolved: profile opt-outs follow the stable **Skill ID**.
- Re-import after **Skill Deletion** is not a skill update; resolved: old opt-outs are not restored after deletion and recreation.
- **Skills Page** enablement controls are not a second source of truth; resolved: they update **Runtime Profile** enablement.
- **Runtime Extension Import** is not task startup installation; resolved: package-backed skills are imported or updated through management, while task launch projects already-managed extensions.
- **Controlled Skill Import** is not arbitrary shell execution; resolved: package-backed skill import uses a fixed importer from structured input.
- **Skill Publication** is not partial live update; resolved: failed imports or edits leave the current live skill unchanged.
- **Skill Validation** is not a full trust proof; resolved: it blocks malformed or unsafe bundle structure and warns on suspect free-form content.
- **Runtime Extension Library** edits are not live task mutations; resolved: started tasks keep already-projected skills, and new tasks load the current library contents.
- **Runtime Extension Projection** is not a global install; resolved: enabled extensions are materialized into the task-local runtime boundary.
- **Config Projection** failure is not automatically **Runtime Profile** invalidity; resolved: treat it as a **Task** startup failure unless validation proves the profile itself is invalid.
- **Preflight** failure is not **Runtime** failure; resolved: startup checks fail before the runtime performs task work.
- **Model Preflight Preview** is not a secret display; resolved: show endpoint base URL, protocol, model, generated API key environment variable name, and configured/missing status without showing key values.
- **CLI Fallback** is not a bypass; resolved: CLI writes carry the same validation, **Trusted Origin**, and Blackboard semantics as other project interfaces.
- **Task Event** and project-level history are distinct; resolved: task events are task-local timeline entries, while project-level records are security history.
- **Task Event** is not raw output storage; resolved: preserve full output through logs or **Evidence Artifacts** and keep the task timeline structured.
- **Task Deletion** is not runtime cancellation or **Trusted Origin** erasure; resolved: only terminal Tasks may be removed from normal surfaces, and minimum integrity bindings remain available internally.
- Blackboard consolidation is not a second model workflow; resolved: the operator reviews Working Graph health and the Harness settles explicit Intents through the normal Blackboard service.
- Cairn-style graph export is not a new graph store or relevance-selected planning view; resolved: use the complete **Runtime Blackboard Snapshot**.
- A Task conclusion is not automatic objective closure; resolved: an **Exploration Objective** closes only through its semantic transition and supporting `satisfies` relationship.
- **Trusted Origin** is not project history or Blackboard knowledge; resolved: it is internal integrity binding, while project history is chronological security records.
- **Report** is not an audit view; resolved: reports show current conclusions, scope context, and key evidence without expanded execution-origin metadata.
- **Tentative Fact** is visible current context, not confirmed conclusion; resolved: current views may include it with confidence while reports mark it separately from confirmed findings.
- Unconfirmed **Findings** are not confirmed report conclusions; resolved: reports may show them as needing validation outside the confirmed findings summary.
- **Runtime Workdir** is not shared memory; resolved: cross-task knowledge flows through **Blackboard** and retained artifacts.
- **Runtime Workdir** is not cross-runtime handoff state; resolved: replacement Runtime Continuations pass context through the **Working Blackboard Snapshot**, open Attempt checkpoints, and retained artifacts.
- **Runtime Workdir** is not automatic evidence capture; resolved: files become **Evidence Artifacts** only through explicit attach or retain actions.
- CTF Challenge Project support is not backend-only or selected by an implicit Pentest default; resolved: Project creation requires an explicit Project Kind and the creation interface exposes both supported kinds.
- Task classification is not hidden in the owning Project; resolved: Task Launch exposes an explicit **Task Type**, stores the immutable selection, rejects a mismatch with the current Project Kind, and keeps historical Task Type unchanged after Project Kind Conversion.
- Runtime Extension compatibility is not authorization; resolved: Preflight validates **Runtime Extension Requirements**, while Project Kind and Scope remain explicit operator-owned state.
- Challenge execution is not a loose sequence of platform and Blackboard tool calls; resolved: the **Challenge Workflow** owns claim, submit, abandon, finalize, Evidence retention, and restart-safe settlement behind one small interface.
- Task completion readiness is not the latest Blackboard conclusion label; resolved: **Finish Readiness** aggregates every current Finish Blocker and Task Finish enforces that projection.
- TSecBench hosted evaluation is not a new CyberPenda product mode or a specialized competition Agent; resolved: the **TSecBench Hosted Image** preserves normal Pentest Agent semantics while keeping all normal product behavior unchanged.
- A **Hosted Evaluation Run** is not interactive or externally started after container launch; resolved: it validates its environment and starts the complete eligible evaluation automatically, then remains alive until TSecBench terminates the container.
- Hosted model selection is not fixed at build time or discovered opportunistically; resolved: select one of a small set of verified model configurations through runtime environment values and fail before challenge work when it is invalid.
- Hosted integration code is not forbidden from extending shared packages; resolved: shared additive interfaces are allowed when regression tests prove that existing product entrypoints and default behavior remain unchanged.
- A container-local Project, Blackboard, Evidence set, database, or report is not the formal **Hosted Evaluation Result**; resolved: TSecBench owns the formal score and completion state.
- The **Challenge Workflow** is not the mandatory challenge execution path; resolved: TSecBench keeps **Runtime-Managed Challenge Execution** and may use a Hosted-only structured client without making the Hosted Controller a scheduler or coupling hosted execution to Challenge Workflow.
- A TSecBench **Benchmark Challenge** is not mapped to its own Project or Task; resolved: one **Hosted Evaluation Run** uses one CTF Challenge Project and one CTF Challenge Task whose Runtime owns the complete evaluation loop.
- The **Hosted Controller** is not a challenge orchestrator or lifecycle finisher; resolved: it owns bootstrap and Task observation while the Runtime uses the process-isolated **Hosted Challenge Client** for challenge operations, and TSecBench owns container termination.
- TSecBench integration knowledge is not embedded as one oversized Task Goal or a new MCP server; resolved: the **TSecBench Hosted Image** supplies one Hosted-adapted `ctf-orchestrator` Skill and the Task Goal requires its use.
- TSecBench target addresses are not individually approved Scope Expansions or structured dynamic targets; resolved: a **Platform-Issued Scope** statement in Scope notes authorizes only the ephemeral addresses returned for the current evaluation credential.
- The **TSecBench Hosted Image** is not limited to one Runtime family; resolved: it packages Pi, Codex, Claude Code, and Hermes.
- A TSecBench hint is not forbidden or automatically requested; resolved: the Runtime may decide to request it after the hosted Skill explains its score cost and requires sufficient prior effort.
- The Hosted Controller does not prescribe serial Benchmark Challenge execution; resolved: the Runtime may manage between one and three active platform instances and remains responsible for closing them.
- Hosted Runtime completion is not converted into normal **Task Finish** or container exit; resolved: the Runtime remains available and the **Hosted Controller** waits until TSecBench terminates the container.
- The one-use `BENCHMARK_TOKEN` is not guaranteed to be redacted from persistent Runtime output; resolved: direct Runtime access is retained and this evaluation-time disclosure risk is accepted without a new redaction mechanism.
- Runtime family selection is not inferred only from model protocol; resolved: Codex is the default, Hosted bootstrap accepts Codex or Claude Code, and startup validation rejects Pi, Hermes, or an incompatible model protocol. All four Runtime CLIs remain in the **Hosted Tool Baseline**.
- A hosted Model Protocol Base URL is not transformed by the **Hosted Controller**; resolved: the operator enters the already converted `.tsecbench.gw` HTTP URL on the TSecBench environment-variable page.
- A missing hosted model protocol is not inferred or defaulted; resolved: startup fails unless the operator supplies it explicitly.
- The **TSecBench Hosted Image** does not depend on launching a nested Sandbox Runner; resolved: selected Runtimes and tools execute through the **Container Host Runner** and the image assumes no Docker Socket, privileged mode, or nested container engine.
- The hosted daemon does not expose its Web UI to the Challenge Platform network; resolved: retain embedded UI resources but bind the daemon only to container loopback.
- The **TSecBench Hosted Image** does not copy the existing full Sandbox tool inventory; resolved: build on Kali Rolling with a **Hosted Tool Baseline**, and let the Runtime implement missing challenge-specific capability from the available environment.
- Hosted model input does not use Runtime-specific or vendor-specific environment contracts; resolved: the TSecBench page supplies `CYBERPENDA_RUNTIME`, `CYBERPENDA_MODEL_PROTOCOL`, `CYBERPENDA_MODEL_BASE_URL`, `CYBERPENDA_MODEL`, `CYBERPENDA_MODEL_API_KEY`, optional `CYBERPENDA_REASONING_EFFORT`, optional `CYBERPENDA_TASK_GOAL_APPENDIX`, optional `CYBERPENDA_AUTO_COMPACT_THRESHOLD`, optional `CYBERPENDA_AUTO_COMPACT_WINDOW`, optional `CYBERPENDA_MAX_OUTPUT_TOKENS`, and optional `CYBERPENDA_CONTEXT_WINDOW` as the hosted page contract.
- Hosted Claude compact is not missing when a 1M-context request returns HTTP 400; resolved: compact exists, but the default threshold plus the reserved completion can exceed 1048576 after a large tool result. Optional `CYBERPENDA_AUTO_COMPACT_THRESHOLD` (1-100) becomes `CLAUDE_AUTOCOMPACT_PCT_OVERRIDE`. Optional `CYBERPENDA_AUTO_COMPACT_WINDOW` becomes `CLAUDE_CODE_AUTO_COMPACT_WINDOW`. DeepSeek documents 786432 for that window.
- Hosted max completion is not inferred from the model family; resolved: optional `CYBERPENDA_MAX_OUTPUT_TOKENS` becomes `CLAUDE_CODE_MAX_OUTPUT_TOKENS` and a **Model Catalog Limit Override**. Optional `CYBERPENDA_CONTEXT_WINDOW` becomes a **Model Catalog Limit Override**. DeepSeek hosted runs should set 393216 (384K). 786432 plus 393216 exceeds 1048576, so those runs should set the **Hosted Auto Compact Window** to 524288.
- Hosted **Reasoning Effort** is not read from `CLAUDE_CODE_EFFORT_LEVEL` or Runtime Custom Arguments; resolved: optional `CYBERPENDA_REASONING_EFFORT` becomes the hosted Runtime Profile **Reasoning Effort**, and an omitted value resolves to `high`.
- A hosted extra prompt is not a replacement Task Goal; resolved: optional `CYBERPENDA_TASK_GOAL_APPENDIX` is appended after the required hosted Task Goal.
- Hosted image size is not judged from its expanded layer size; resolved: the exported and gzip-compressed Docker archive must remain below 3 GB, and the build fails when it does not.
- Invalid **Hosted Model Configuration** is not delegated to the Runtime for repair; resolved: bootstrap reports a non-sensitive error and exits nonzero before Project creation or model use.
- A failed initial hosted Runtime Turn is not retried, replaced, or left running until the platform deadline; resolved: the controller reports a redacted failure and exits the container nonzero.
- A live Hosted Evaluation Run is not ended by Transcript drain, stdout write, Task observe, or later Task status errors; resolved: those errors are Hosted Operational Log lines, the Runtime stays live, and the Controller waits for TSecBench to terminate the container.
- The **Hosted Controller** does not send Task Conversation input or continuation Turns; resolved: it creates only the initial Task, then observes it until Runtime failure or TSecBench termination.
- Hosted Skill selection is a Hosted-only runtime reduction that does not change normal product defaults; resolved: disable Built-in Skill seeding for the Hosted daemon and publish only the Hosted-adapted `ctf-orchestrator`, so the Hosted Runtime Extension Library and Task Skills Root each contain exactly that one Skill. Packaged Built-in Skill bytes may remain embedded in the Hosted binary.
- The Hosted Task does not use CyberPenda Blackboard as orchestration memory; resolved: launch it in disabled **Blackboard Mode** and use the **Hosted FGS** as its sole Runtime-managed semantic state.
- Per-challenge timing is not copied into the **Hosted FGS**; resolved: the `ctf-orchestrator` reads `elapsed_min`, `budget_min`, `over_budget`, and `attempt_n` from the **Challenge Pass Clock** projection while retaining only its scheduling decisions in the Hosted FGS.
- The initial hosted Work Runtime Turn is not complete after one traversal or a subjective no-progress judgment; resolved: it returns only when the platform reports every Benchmark Challenge complete or the evaluation enters `invalid_state`.
- Hosted delivery is not accepted by unit tests alone; resolved: require fake-platform contract and failure tests with the **Hosted Acceptance Configuration**, a real TSecBench local-mode API validation after the deployer connects the host VPN, and the compressed-image size gate before upload.
- Hosted Runtime dependencies are not pinned at source level; resolved: builds install current Pi, Codex, Claude Code, and Hermes releases, and each produced archive records the exact resolved versions for traceability.
- The first **Hosted Delivery Bundle** is not multi-architecture; resolved: deliver and validate `linux/amd64` first.
- A Docker archive alone is not a complete hosted delivery; resolved: the **Hosted Delivery Bundle** also includes a SHA-256 checksum, page environment-variable template, local-mode runner, resolved component inventory, and integration and troubleshooting guidance.
- The hosted container does not establish a TSecBench VPN; resolved: hosted execution uses the isolated network supplied by TSecBench, while the real local-mode acceptance run requires the deployer to connect the TSecBench VPN on the host before starting the container.
- Pi unattended execution does not need a synthetic YOLO mode; resolved: Pi's built-in tools have no permission popups and execute with the Pi process permissions, while bootstrap deterministically trusts the projected CyberPenda project resources required by the hosted run.
- Container root is not permission to require elevated platform capabilities; resolved: the **Container Host Runner** uses only normal default container capabilities, does not request TUN, `NET_ADMIN`, privileged mode, or a Docker Socket, and challenge tools must fall back to normal TCP or HTTP methods when a capability is unavailable.
- Hosted Runtime and model protocol compatibility is not approximate OpenAI compatibility; resolved: validate Codex with `openai_responses` and Claude Code with `anthropic_messages`, and reject Pi or Hermes as Hosted Runtime selections.
- The **Hosted Acceptance Configuration** is not a requirement to solve a real local-mode challenge; resolved: use Codex with `openai_responses` for hosted bootstrap, model-call, and fake-platform validation, while real TSecBench local mode validates only the platform API.
- TSecBench challenge access is not performed through direct Runtime-built curl chains; resolved: the **Hosted Challenge Client** provides separate structured list, start, hint, submit, and guarded close commands while preserving Runtime-owned scheduling and process isolation from the host lifecycle.
- Hosted Challenge Client access is not all-or-nothing for Execute agents; resolved: Execute agents may submit candidates directly through `submit` on standard input, while the `ctf-orchestrator` Decide process exclusively serializes `list`, `start`, `hint`, `close`, and `abandon` and retains Challenge lifecycle and hint authority.
- An active Benchmark Challenge is not kept open after success or explicit abandonment; resolved: the hosted Skill requires immediate close in those cases, preserves the platform limit of three active challenges, and leaves other scheduling choices to the Runtime.
- The hosted model API key is not a reusable production credential; resolved: the deployer supplies a dedicated, revocable evaluation key and accepts that persistent Runtime records or complete standard output may disclose it.
- Hosted standard output is not a bounded operational summary or provider-native stream; resolved: emit every retained Runtime conversation and tool-result Transcript entry as sequence-ordered JSONL, fetch complete detail for truncated entries, and mask exact known evaluation credential values before output. Flags, targets, attack data, and other sensitive content remain visible.
- A **Model Protocol Base URL** is not a complete model operation URL; resolved: hosted bootstrap rejects URLs ending in known operation paths such as `/chat/completions`, `/responses`, or `/messages`, and the selected Runtime appends its own operation path.
- TSecBench transient-error recovery is not allowed to endanger the host process; resolved: the **Hosted Challenge Client** returns bounded structured failures without automatic mutation retries, and the Runtime decides whether to retry or switch challenges.
- A **Hosted Evaluation Run** is not restart-resumable; resolved: Project and Task creation remain non-idempotent, every hosted container process is one fresh run, and any unexpected process exit fails that run without bootstrap recovery.
- Hosted stdout masking is not persistent Runtime redaction; resolved: the **Hosted Transcript Stream** masks exact `BENCHMARK_TOKEN` and model API key values only when it writes stdout, while internal Task Events, Transcript source records, and diagnostic state retain the accepted disclosure risk.
- Runtime reasoning is not temporary UI-only liveness text or summary-only output; resolved: every raw reasoning delta a provider emits becomes a durable **Runtime Reasoning Entry**, including Codex raw content from third-party models, with provider summary used only when raw reasoning is absent.
- TSecBench container termination is not graceful CyberPenda shutdown; resolved: the hosted PID catches and ignores termination signals, does not stop the Task or close the daemon, and stays alive until the platform forcibly terminates the container.
- Hosted operational diagnostics are not mixed with the **Hosted Transcript Stream**; resolved: standard output contains JSONL Transcript records only, while Hosted Controller and daemon logs use standard error.
- Real TSecBench local-mode acceptance is not an autonomous solving benchmark; resolved: after the deployer connects the host VPN, validate list, start, submit, and close against the real platform without requiring the Runtime to solve a challenge.
- Local acceptance Secrets are not part of the **Hosted Delivery Bundle** or command-line arguments; resolved: the bundle contains only a Secret-free environment template, and the deployer creates a separate permission-restricted environment file.
- The **Hosted Delivery Bundle** is not built on the deployer's workstation; resolved: a dedicated GitHub Actions workflow builds and validates it on a native `linux/amd64` runner and uploads the complete Bundle as one workflow artifact.
- A failed hosted Runtime does not leave the controller waiting for platform termination; resolved: drain every already-retained Transcript entry to the **Hosted Transcript Stream**, then exit nonzero.
- The hosted TSecBench Skill is not a strict API-version compatibility gate; resolved: document the known `/openapi/v1` contract, but allow the Runtime to inspect unexpected platform responses and try a compatible request shape at its own discretion.
- Persistent Codex App Server is not covered by the exec-only `--dangerously-bypass-approvals-and-sandbox` flag; resolved: the **Runtime Harness** also projects **Runtime Non-Interactive Defaults** as `approvalPolicy=never` and `sandbox=danger-full-access` so hosted tool commands can use platform DNS and network.
- The Codex multi-agent control default is not an explicit off projection; resolved: an unset control projects no multi-agent keys so Codex's own feature default applies without CyberPenda suppression, an explicit on projects the feature flag and agent caps, and an explicit off projects the off keys.
