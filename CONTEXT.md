# Pentest Agent Context

The shared language for CyberPenda, a local-first system for authorized security testing. This single context covers Project work, Non-Project Sessions, and Hosted evaluation.

## Language

### Projects and work

**Pentest Agent**:
A local-first system that coordinates authorized security testing work for a defined **Project**.
_Avoid_: autonomous hacker, exploit bot

**Project**:
A bounded security-testing engagement with its own **Scope**, tasks, memory, evidence, and report.
_Avoid_: workspace, conversation, campaign

**Project Kind**:
The explicit classification of a **Project** as Pentest or CTF Challenge, which determines its conclusion and reporting semantics.
_Avoid_: hidden project default, task mode, runtime capability

**Project Kind Conversion**:
An explicit operator-confirmed change of one Project Kind after a preview proves that every Task is terminal and no incompatible current Finding or Solution exists. It changes classification only and never infers a cross-type Blackboard record conversion.
_Avoid_: automatic project repair, fact-to-solution migration, task mode switch

**Project Defaults**:
Project-level defaults for the **Runner**, with retained legacy **Task Policy** metadata. They do not include an implicit **Runtime Profile** selection.
_Avoid_: project-local runtime profile, copied profile, default profile, launch selection store

**Task Policy**:
Historical operator-defined limits from the retired **Challenge Workflow**, retained as metadata without enforcement.
_Avoid_: prompt advice, Skill rule, hidden timeout

**Task Policy Snapshot**:
The immutable Task-local copy of legacy Task Policy retained for historical inspection. Existing snapshots are not rewritten when Challenge Workflow retires.
_Avoid_: current project defaults, mutable runtime limit, prompt text

**Project Dashboard**:
The primary project view that surfaces scope status, task runs, blackboard growth, findings, and evidence health.
_Avoid_: chat home, task-only queue

**Task**:
A user-goal-driven project run executed from one **Runtime Configuration Snapshot** through one **Runner**.
_Avoid_: chat message, report section, shell command, plan step

**Task Type**:
The immutable Pentest or CTF Challenge classification captured for a **Task**, matching its **Project Kind** at launch.
_Avoid_: hidden Project Kind lookup, mutable task mode, prompt label

**Non-Project Mode**:
The product mode for work that has no **Project** and therefore no Project Scope, while retaining the same Runtime interaction capabilities as a **Task**.
_Avoid_: unrestricted Project, temporary Task, separate chat product

**Session**:
A durable Non-Project owner of one persistent Runtime conversation, owner-local events, attachments, and workdir, with a self-contained Blackboard unless its **Blackboard Mode** is disabled.
_Avoid_: Project, Task, disposable chat, UI-only conversation

**Runtime Owner Workspace**:
The shared operator workspace for a **Task** or **Session**, with its conversation, Runtime activity, attachments, and lifecycle controls.
_Avoid_: Session-specific chat UI, duplicated Task page, shared domain aggregate

**Runtime Owner History Window**:
A bounded view of one **Runtime Owner**'s recent Timeline and Transcript, with older history available on demand.
_Avoid_: complete initial transcript, cross-owner event cache

**Project Navigation Projection**:
The Project navigation view of recent Tasks, the selected Task, and Tasks with a live busy Runtime.
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
The shared launch controls for **Tasks** and **Sessions**, including Runtime, model, Runner, Skills, and **Preflight**.
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
A **Runtime Turn** initiated by the operator's Task Goal, Task Conversation input, or Session Conversation input. The **Runtime Harness** assigns this kind from request lineage; provider output cannot claim it.
_Avoid_: harness control turn, provider-classified turn, task

**Harness Control Turn**:
A **Runtime Turn** initiated by the **Runtime Harness** for a bounded control purpose. The Harness assigns this kind from request lineage; provider output cannot claim it.
_Avoid_: operator message, autonomous task, provider-classified turn

**Runtime Turn Selection**:
The **Model Provider**, model, and **Requested Reasoning Effort** resolved for one **Runtime Turn**, independently of adjacent turns and without editing the selected **Runtime Profile**.
_Avoid_: profile switch, session-wide setting, global default

**Task Deletion**:
Operator removal of a terminal **Task** from normal task surfaces and counts while retaining the minimum durable state required for historical **Blackboard** and **Trusted Origin** integrity.
_Avoid_: active task cancellation, provenance erasure, hard deletion

**Task Finish**:
An operator action confirming that a **Task** is complete, which closes its Runtime after required reconciliation and marks the Task completed; it is distinct from **Stop**.
_Avoid_: stop, auto-complete

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

### Runtime lifecycle

**Runtime Owner**:
A **Task** or **Session** that owns one Runtime conversation and its execution state.
_Avoid_: Project, Runtime Profile, daemon-global session

**Runtime**:
The local agent CLI or assistant process scheduled to perform work for one **Runtime Owner**.
_Avoid_: pentest agent, model, provider, worker

**Runtime Owner-Scoped Persistent Runtime**:
A **Runtime** that remains available across **Runtime Turns** for exactly one **Runtime Owner**.
_Avoid_: daemon-global session, infinite process, session-wide setting

**Runtime Harness**:
The daemon-managed, owner-neutral control wrapper that launches, resumes, steers, and stops a **Runtime** for one **Runtime Owner**.
_Avoid_: pentest tool executor, agent brain, sandbox

**Harness Steering**:
An operator control action that changes Runtime progress within the same **Runtime Owner**, through current-turn input or a later **Runtime Continuation**.
_Avoid_: direct tool control, hidden prompt mutation, new Runtime Owner

**Accepted Steering**:
A durable **Harness Steering** request whose ordered delivery and explicit settlement are the **Runtime Harness**'s responsibility.
_Avoid_: saved message, in-memory callback, permanent pending state

**Runtime Continuation**:
One writable unit of runtime progress after launch, user input, checkpoint, interrupt, or resume. Provider-native same-turn steering remains inside the current Runtime Continuation; interrupt-then-replace creates a replacement Runtime Continuation.
_Avoid_: rewriting completed Runtime items, new task

**Runtime Activity Indicator**:
The current Runtime liveness and turn activity shown to the operator, separate from durable **Task** lifecycle state.
_Avoid_: Task status, audit record, activity history

**Runtime Non-Interactive Defaults**:
The CyberPenda defaults for Runtime operation without provider approval prompts. They do not grant Scope authorization or Host Runner activation.
_Avoid_: permission grant, Scope authorization, **Host Runner Activation**, **Project Interface** authority, runner policy

**Runtime Profile**:
A global user-created reusable advanced configuration for a **Runtime**, including MCP, **Profile Skill Opt-Outs**, Extensions, binary paths, custom configuration, or Runner defaults without storing secret values.
_Avoid_: account, credential bundle, secret store, automatic profile, runtime profile preset

**Runtime Custom Arguments**:
Advanced **Runtime Profile** command arguments for provider-native options that have no structured CyberPenda field.
_Avoid_: Model Provider override, model override, reasoning-effort override, structured-field duplicate

**Launch-Resolved Runtime Profile**:
A legacy global **Runtime Profile** that an older **Launch Configuration Resolution** path created from **Launch Selection**. New launches never create, reuse, or implicitly promote one.
_Avoid_: runtime profile preset, runtime configuration snapshot, current launch output

### Models

**Model Provider**:
A global reusable non-secret configuration for a model service that a **Launch Selection** or **Runtime Profile** can use when a **Runtime** needs model access.
_Avoid_: runtime profile, runtime plugin, model only, credential value

**Launch-Ready Model Provider**:
A **Model Provider** that resolves a protocol endpoint, catalog model, and configured API key compatible with the selected **Runtime Plugin**. Readiness is runtime-specific and checked by **Preflight**.
_Avoid_: saved provider, valid draft, globally ready provider

**Model Provider ID**:
The immutable identifier for a **Model Provider**, used to derive its **Model API Key Environment Variable**.
_Avoid_: display name, editable label, secret name

**Model Providers Page**:
The global settings view for managing **Model Providers**.
_Avoid_: runtime profile subform, project settings panel, credential page

**Model Provider Migration**:
An explicit management action that moves legacy model-service fields from a **Runtime Profile** into a reusable **Model Provider**.
_Avoid_: silent automatic migration, runtime profile edit side effect

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
An explicit operator action that updates a **Model Catalog** from its **Model Provider**, while preserving manual model choices.
_Avoid_: background polling, task-launch discovery, runtime introspection

**Model Context Window**:
The total token capacity selected for a catalog model.
_Avoid_: Hosted Auto Compact Window, context file size

**Model Max Output Tokens**:
The maximum completion token reservation selected for a catalog model.
_Avoid_: Hosted Max Output Tokens, compact window

**Model Capability Cache**:
Non-authoritative reference limits for known models, subordinate to operator **Model Catalog Limit Overrides**.
_Avoid_: Model Catalog, Runtime Plugin table, family heuristic

**Model Catalog Limit Override**:
Optional per-identifier window and max-output numbers stored on a **Model Catalog**. They beat the **Model Capability Cache** and survive catalog name refresh and cache refresh.
_Avoid_: Runtime Profile field, inferred model family limit

**Model Capability Cache Refresh**:
An explicit operator update of the model-limit reference data in the **Model Capability Cache**.
_Avoid_: Model Catalog Refresh, Task Launch fetch, Preflight download

**Model Endpoint Origin**:
The scheme, host, and optional port shared by one or more **Model Provider Endpoints** for a **Model Provider**.
_Avoid_: protocol path prefix, operation URL, catalog refresh URL

**Model Protocol Path Prefix**:
The provider-specific route in a **Model Protocol Base URL** before the Runtime adds an operation path.
_Avoid_: operation suffix, model-list path, full endpoint URL

**Model Operation Suffix**:
The model-service operation path added by a **Runtime** to a **Model Protocol Base URL**.
_Avoid_: protocol path prefix, model-list path, configured base URL

**Model Protocol Base URL**:
The non-secret service base address for one **Model Provider Protocol**, including its provider route but excluding the **Model Operation Suffix**.
_Avoid_: operation URL, catalog refresh URL, full request URL

**Model Provider Endpoint**:
A **Model Provider** entry that pairs one supported protocol with its **Model Protocol Base URL**. Endpoints of the same Provider share one **Model API Key Source**.
_Avoid_: protocol, runtime profile, credential value, custom header bundle

**Model Override**:
A **Runtime Profile** field that replaces the selected **Model Provider**'s default model when that profile is used without a **Launch Model Override**.
_Avoid_: provider edit, endpoint fork, hidden model switch, launch-only override

**Reasoning Effort**:
The requested degree of model reasoning for a **Runtime Turn**, with an optional **Runtime Profile** default.
_Avoid_: thinking mode, token budget, custom runtime flag, auto effort

**Requested Reasoning Effort**:
The **Reasoning Effort** CyberPenda asks a **Runtime** to use for one **Runtime Turn**. It is a request, not proof of the level the model actually used.
_Avoid_: effective effort, supported effort, reasoning token count

**Effective Reasoning Effort**:
The reasoning level a **Runtime** reports that it actually applied after any native validation or downgrade. It remains unknown when the **Runtime** does not report it.
_Avoid_: requested effort, assumed effort, profile default

**Runtime Reasoning Entry**:
Retained reasoning text that a **Runtime** emits during a **Runtime Turn**, or its emitted summary when raw reasoning is absent. It is not inferred hidden reasoning or proof of **Effective Reasoning Effort**.
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
The complete set of global **Launch-Ready Model Providers** and their credentials made available to a Runtime that supports native provider switching.
_Avoid_: selected-provider-only projection, project allowlist, on-demand credential injection

**Model API Key Source**:
The required single source for the API key used by a **Model Provider**.
_Avoid_: credential reference, project override, runtime profile key

**Model API Key Environment Variable**:
The generated environment variable name used as the **Model API Key Source** for a **Model Provider**.
_Avoid_: user-entered env var, inline API key, secret value, credential reference

**Model Provider Snapshot**:
The non-secret resolved Provider, endpoint, protocol, model, and credential-source information captured for a launch or **Runtime Continuation**.
_Avoid_: live model provider reference, model catalog copy, credential value

**Model Provider Requirement**:
A **Runtime Plugin** declaration that says whether a **Runtime Profile** must, may, or must not resolve a compatible **Model Provider** and **Model Provider Protocol**.
_Avoid_: hidden preflight rule, runtime profile convention, provider guess

### Runtime Plugins and Skills

**Runtime Plugin**:
A declarative definition of a Runtime family's launch, configuration, startup requirements, and conversation interpretation.
_Avoid_: executable extension, marketplace package, project-local runtime profile

**Runtime Plugin Manifest**:
The non-secret declaration of one **Runtime Plugin**.
_Avoid_: secret config, arbitrary code, shell script

**Runtime Plugin Registry**:
The local collection of built-in and explicitly trusted **Runtime Plugin Manifests**.
_Avoid_: remote plugin store, package manager

**Runtime Extension**:
A runtime-native plugin, skill, package, or configuration bundle that a selected **Runtime** consumes after **Config Projection** prepares it for a **Task**.
_Avoid_: runtime provider, daemon plugin, arbitrary host hook

**Runtime Extension Bundle**:
The file-backed content of a **Runtime Extension**, including its instructions, scripts, assets, and structured non-secret metadata.
_Avoid_: manifest-only skill, external path pointer, raw JSON config

**Skill**:
A runtime-agnostic **Runtime Extension Bundle** managed through the **Skills Page** and projected for any supported **Runtime** when allowed by **Default Skill Enablement**, **Global Skill Opt-Out**, and any selected **Profile Skill Opt-Out**.
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

**Execute Agent Type**:
CyberPenda 为 Pi 与 Claude Code **Runtime** 经 **Config Projection** 投影的自定义子代理类型(`execute`),两个 Runtime 使用同一份 CyberPenda 定义(分别写入 Pi agent 目录与 workdir `.claude/agents/`),内置 ctf-orchestrator Execute 进程的身份与收束纪律,使派发不必使用通用 general-purpose 克隆。不绑定任何 Challenge Platform。
_Avoid_: hosted-only agent, per-runtime duplicate definitions, general-purpose specialization, per-dispatch discipline text

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
The effective choice that allows a compatible **Runtime Extension** from the **Runtime Extension Library** to be projected for a new Runtime Owner. For **Skills**, it combines **Default Skill Enablement**, **Global Skill Opt-Out**, and any selected **Profile Skill Opt-Out**.
_Avoid_: library membership, automatic global mount, project-wide default

**Default Skill Enablement**:
The default-on policy that enables newly uploaded or imported **Skills** for direct launches and all current and future **Runtime Profiles**, unless a **Global Skill Opt-Out** or selected **Profile Skill Opt-Out** disables the Skill.
_Avoid_: runtime-specific plugin default, live task mutation, project-local default

**Skill Opt-Out**:
An enablement override that disables a default-enabled **Skill** by **Skill ID**. It is either a **Global Skill Opt-Out** or a **Profile Skill Opt-Out**.
_Avoid_: Skill Deletion, Runtime-Specific Extension disablement, temporary task skip

**Global Skill Opt-Out**:
A **Skills Page** library-level override that disables one default-enabled **Skill** for direct launches and every current or future **Runtime Profile**. It overrides, but does not erase, **Profile Skill Opt-Outs**.
_Avoid_: Skill Deletion, bulk Profile action, started Runtime Owner mutation

**Profile Skill Opt-Out**:
A **Runtime Profile** override that disables one default-enabled **Skill** when that Profile is selected for a new Runtime Owner.
_Avoid_: Global Skill Opt-Out, direct launch default, temporary task skip

**Skills Page**:
The top-level product view named Skills for managing **Skills** in the **Runtime Extension Library**.
_Avoid_: runtime profile subform, project settings panel, provider-specific plugin manager

**Runtime Extension Manifest**:
The declarative document that identifies a **Runtime Extension**, its compatible **Runtime Plugins**, source location, task-local projection target, and non-secret configuration.
_Avoid_: executable installer, credential file, remote marketplace listing

**Runtime Extension Projection**:
The task-local materialization of enabled **Runtime Extensions** into the selected **Runtime**'s home, config, skill, plugin, or MCP-compatible directories.
_Avoid_: host runtime mutation, global plugin install, profile edit side effect

### Configuration and execution

**Launch Profile Selector**:
An advanced task-launch control for explicitly choosing an optional **Runtime Profile** from every launchable Profile, unfiltered by the current **Runtime** selection. Choosing a Profile switches the launch Runtime to that Profile's **Runtime Plugin** family.
_Avoid_: primary launch picker, default profile, model provider switch, raw config editor, runtime-filtered profile list

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

**Runtime Profile Config View**:
The on-demand, redacted native configuration view of a saved **Runtime Profile**, shared by advanced editing and **Profile Config Import**.
_Avoid_: live draft preview, complete Task launch snapshot, frontend config generator

**Custom Config File**:
The native configuration in a **Runtime Profile** for options that structured fields cannot express. Structured fields remain authoritative for their own options.
_Avoid_: full config replacement, opaque override, host config edit

**Managed Config Key**:
A native Runtime option owned by a CyberPenda structured field and therefore excluded from raw **Profile Config Import** changes.
_Avoid_: locked key, forbidden setting

**MCP Configuration**:
A **Runtime Profile**'s explicit configuration of **External MCP Servers**.
_Avoid_: raw JSON blob, unvalidated tool config

**External MCP Server**:
A user-added Runtime tool service without implicit Blackboard authority.
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
The name under which a **Credential Binding** makes a credential available to a **Runtime**.
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

### Blackboard and FGS

**Blackboard**:
The durable semantic memory shared by a Project's Tasks, or owned separately by a Session. Its current working model is **Goal**, **Step**, and **Fact**; retained legacy records are distinct from those nodes.
_Avoid_: chat history, notes database

**Blackboard Mode**:
The Runtime Owner choice of FGS enabled or Disabled. Disabled gives the Runtime no Blackboard context or authority.
_Avoid_: Blackboard Conclusion Mode, autonomous Task completion, transcript parsing mode, Blackboard write permission

**Mode Skill**:
The system Skill associated with a legacy Blackboard Mode, distinct from ordinary Skills and current **FGS Runtime Instructions**.
_Avoid_: ordinary Skill toggle, mixed-mode instructions, Runtime Profile capability, Disabled Mode Skill

**Working Graph**:
The Runtime's local FGS working state, distinct from accepted durable **Blackboard** state.
_Avoid_: Assisted Blackboard, transcript parser, direct Blackboard storage

**Goal**:
An FGS node that states an intended outcome and its success criteria.
_Avoid_: legacy Exploration Objective record

**Step**:
An FGS node that describes work toward a **Goal**, linked to input and output **Facts**.
_Avoid_: legacy Attempt record, Runtime Turn

**Fact**:
An immutable FGS observation or result for use by later **Steps**. A correction is a new Fact, distinct from an update to a legacy **Project Fact**.
_Avoid_: legacy Project Fact record, Transcript dump

**FGS Runtime Instructions**:
The core guidance for Runtime FGS reporting, Decide and Execute responsibilities, and delivery recovery. It guides behavior without proving compliance or granting scheduling control to the Harness.
_Avoid_: mandatory FGS Skill invocation, system-role message, enforced scheduler

**Working Graph Intent**:
A retained request from the retired legacy Working Graph publication and settlement protocol, distinct from a current FGS update.
_Avoid_: direct database command, provider tool result, lifecycle instruction

**Working Graph Receipt**:
A retained delivery-state record for a legacy **Working Graph Intent**, distinct from current FGS delivery.
_Avoid_: model acknowledgement, transient stdout, Blackboard record

**Agent-Managed Trace**:
Optional Runtime-maintained work notes in Disabled **Blackboard Mode**, outside CyberPenda Blackboard knowledge.
_Avoid_: file-backed Blackboard, fixed state file, Working Blackboard Snapshot, cross-Runtime handoff

**Blackboard Key**:
A stable, readable semantic identity unique within one **Blackboard**, independent of storage and execution identifiers.
_Avoid_: database ID, globally unique ID, type-scoped key

### Retained legacy knowledge

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
A typed, versioned semantic link between retained legacy Blackboard records, identified by their **Blackboard Keys** and relationship type.
_Avoid_: edge ID, audit lineage, untyped relation

**Exploration Objective**:
A durable project-scoped investigation direction that may be derived from existing **Project Facts**, **Findings**, or **Solutions** and points toward an unknown future conclusion. It may inform a **Task Goal** and later resolve through **Project Facts**, **Findings**, or **Solutions**, but it is not **Current Truth** by itself.
_Avoid_: intent, open relationship, task, attack graph edge

**Attempt**:
A durable Blackboard record of one exploration episode that tests an **Exploration Objective**, **Entity**, **Project Fact**, **Finding**, or **Solution** and concludes with a distilled outcome.
_Avoid_: Task, command, tool call, raw output

### Challenges and Hosted evaluation

**Challenge Platform**:
An external system that issues challenge Attempts and accepts candidate submissions or abandonment through a platform-specific **Platform Adapter**.
_Avoid_: Project, Scope authority, Runtime Extension

**TSecBench Hosted Image**:
The separate, self-starting distribution of the **Pentest Agent** for one TSecBench hosted evaluation.
_Avoid_: CyberPenda deployment mode, replacement application image, specialized competition agent

**Container Host Runner**:
A **Host Runner** execution inside the **TSecBench Hosted Image** container, with its normal container capabilities rather than physical host access.
_Avoid_: Sandbox Runner, physical host access, Docker-in-Docker, privileged container

**Hosted Tool Baseline**:
The bounded set of general-purpose tools included in the **TSecBench Hosted Image** for Runtime challenge work.
_Avoid_: full Sandbox image, runtime package installation, per-challenge image

**Hosted Model Configuration**:
The operator-supplied Runtime, model service, credential, and model limits for one **Hosted Evaluation Run**.
_Avoid_: vendor-specific environment contract, model discovery, persisted hosted profile

**Hosted Auto Compact Threshold**:
The optional percentage at which the hosted Claude Code Runtime starts conversation compaction.
_Avoid_: Claude-native page variable, context window size, required compact setting

**Hosted Auto Compact Window**:
The optional absolute token window used for conversation compaction by the hosted Claude Code Runtime.
_Avoid_: Claude-native page variable, compact percent, required compact setting

**Hosted Max Output Tokens**:
Hosted 请求的可选最大输出 token 数。至少支持 Claude Code 和 Pi；写入 **Model Catalog Limit Override**，再通过 **Model Runtime Projection** 传入 Runtime。
_Avoid_: context window size, Claude-native page variable, required output cap

**Hosted Context Window**:
Hosted 模型的可选总 token 容量。至少支持 Claude Code 和 Pi；写入 **Model Catalog Limit Override**，与 **Hosted Auto Compact Window** 独立。
_Avoid_: 自动压缩窗口、输入 token 上限、服务端容量扩展

**Hosted Task Goal Appendix**:
Optional operator-supplied text appended to the required hosted **Task Goal**. It does not replace the required Skill completion sentence and does not change Hosted Controller behavior.
_Avoid_: replacement Task Goal, Skill rewrite, vendor prompt file

**Hosted Pi Additional Model**:
An optional Pi-only numbered slot (1-3) that projects one extra model id, with optional protocol, base URL, and API key overrides, into the same Hosted Pi model registry as the parent `CYBERPENDA_MODEL`. An unset override inherits the parent Hosted Model Configuration. A present-but-empty value, an override without its model id, or any slot on a non-Pi Runtime fails configuration validation. One model id repeated identically across slots is projected once; slots that disagree on protocol, base URL, or API key fail bootstrap. The parent session model stays `CYBERPENDA_MODEL`.
_Avoid_: second parent model, subagent model switch, automatic model selection, per-model reasoning effort

**Hosted Acceptance Configuration**:
The reference Runtime and model configuration for Hosted bootstrap, model-call, and simulated Platform validation. It is distinct from real Platform API acceptance.
_Avoid_: only supported hosted configuration, build-time model selection, production credential

**Hosted Delivery Bundle**:
The versioned Hosted image archive with its checksum, non-secret configuration template, acceptance runner, component inventory, and delivery guidance.
_Avoid_: Docker archive alone, source checkout, multi-architecture image index

**Hosted Evaluation Run**:
One automatic, non-restartable, container-bounded execution of the **TSecBench Hosted Image** from configuration validation through all eligible challenge work and final platform termination.
_Avoid_: Project, Task, interactive session

**Hosted Transcript Stream**:
The ordered projection of retained Task conversation and tool results to Hosted standard output, with exact known evaluation credentials masked. It does not redact persistent source records.
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
The Hosted bootstrap and observation component for one evaluation Project and Task. Challenge scheduling and execution belong to the Runtime.
_Avoid_: Runtime, Challenge Workflow, challenge scheduler

**Subagent Activity**:
The observed identity and activity of a child agent spawned by a **Runtime**, which may outlive its spawning **Work Runtime Turn**. The **Runtime Harness** observes rather than schedules it.
_Avoid_: Harness-owned subagent, Runtime Continuation per child, raw provider JSON dump, Task, provider-specific display rule

**Subagent Conversation Block**:
An attributed child-agent conversation summary within a **Runtime Owner**, with its retained history available independently of the main conversation.
_Avoid_: window-local child history, client-only aggregation, complete inline child Transcript, Runtime Turn per child

**Hosted Challenge Client**:
The isolated, bounded Platform-operation interface used by the Hosted Runtime, separate from the **Hosted Controller** and challenge scheduling.
_Avoid_: Hosted Controller, background sidecar, challenge scheduler, direct curl procedure

**Challenge Pass Clock**:
The Hosted record of an active **Benchmark Challenge**'s start, pass budget, and attempt number, used to inform Runtime scheduling decisions.
_Avoid_: appendix timer, Lead memory, Blackboard timestamp, Hosted Controller scheduler

**Hosted FGS**:
The Hosted Runtime's sole semantic working trace in Disabled **Blackboard Mode**, separate from CyberPenda Blackboard and Project Knowledge.
_Avoid_: Hosted Blackboard, Working Blackboard Snapshot, Project Interface memory

**Platform-Issued Scope**:
An operator authorization statement in Scope notes that permits testing only the ephemeral target addresses returned by the Challenge Platform for the current evaluation credential. It guides the Runtime but is not a structured dynamic target selector.
_Avoid_: automatic Scope Expansion, structured target list, unrestricted platform network

**Platform Adapter**:
A Platform-specific integration behind the **Hosted Challenge Client**; the normal Project Challenge Workflow integration is retired.
_Avoid_: generic fetcher, Project Interface, Skill

**Challenge Workflow**:
The retired normal Project workflow for external challenge operations, now represented only by retained read-only history.
_Avoid_: raw platform client, prompt procedure, collection of Blackboard tool calls

**Runtime-Managed Challenge Execution**:
A challenge execution path in which the Runtime obtains challenge information and submits candidate answers through a platform interface available inside its execution boundary. It does not depend on the retired **Challenge Workflow** control surface.
_Avoid_: Challenge Workflow, operator-managed submission, hosted controller solving

**Challenge Operation**:
A retained external challenge request belonging to a Task and external Attempt. Its historical state does not imply replay or Platform completion.
_Avoid_: tool call, Task Event, remote response only

### Completion and retained results

**Finish Readiness**:
The read-only assessment of whether **Task Finish** can proceed, with its current **Finish Blockers**. Retired Challenge records are excluded.
_Avoid_: Task status, Runtime Activity Indicator, automatic completion

**Finish Blocker**:
A stable typed reason that prevents Task Finish, such as an unresolved FGS update, ordinary open Attempt, unsettled Blackboard conclusion, unsettled reconciliation, or invalidated Finish Intent.
_Avoid_: warning text, latest conclusion status, runtime error

**Runtime Blackboard Snapshot**:
A complete semantic view of the retained legacy Blackboard graph at one revision, separate from execution-origin and audit details.
_Avoid_: audit export, storage dump, relevance-selected subset

**Launch Blackboard Pin**:
The immutable **Runtime Blackboard Snapshot** captured when a **Runtime Continuation** starts and retained internally for deterministic recovery.
_Avoid_: live Blackboard, working file, refreshed snapshot

**Working Blackboard Snapshot**:
The Runtime-readable legacy Blackboard view for a **Runtime Continuation**, distinct from its immutable **Launch Blackboard Pin**.
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
The scoring standard version used by a **CVSS Vector** for a **Finding**.
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
The local working area used by a **Runtime** for one **Runtime Owner**, distinct from retained Evidence storage.
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
