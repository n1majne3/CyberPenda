# Pentest Agent Context

This context defines the product and security-testing language for the local-first pentest agent. It is a glossary for shared domain terms, not an implementation spec.

## Language

**Pentest Agent**:
A local-first system that coordinates authorized security testing work for a defined **Project**.
_Avoid_: autonomous hacker, exploit bot

**Project**:
A bounded security-testing engagement with its own **Scope**, tasks, memory, evidence, and report.
_Avoid_: workspace, conversation, campaign

**Project Defaults**:
Project-level choices for default runner, optional **Default Runtime Profile Preset**, and task policy that do not copy global **Runtime Profiles**.
_Avoid_: project-local runtime profile, copied profile, launch selection store

**Project Dashboard**:
The primary project view that surfaces scope status, task runs, blackboard growth, findings, and evidence health.
_Avoid_: chat home, task-only queue

**Task**:
A user-goal-driven project run executed by one **Runtime Profile** through one **Runner**.
_Avoid_: chat message, report section, shell command, plan step

**Task Goal**:
The user's natural-language objective for a **Task**.
_Avoid_: raw prompt only, plan step

**Task Launch**:
The creation or continuation of a **Task** from **Run Controls**, resolved runtime configuration, selected **Runner**, **Scope Snapshot**, and startup checks.
_Avoid_: runtime projection, task adapter build, launch plumbing

**Run Controls**:
The structured task launch settings that choose **Launch Selection** or an optional **Runtime Profile Preset**, runner, mode, scope preview, and artifact behavior.
_Avoid_: hidden prompt flags, runtime internals

**Launch Selection**:
The primary task-launch choice of one **Runtime Plugin** family, one **Model Provider**, and an optional model for that launch.
_Avoid_: runtime profile picker, MCP preset, profile name

**Launch Model Override**:
A task-only model choice applied at launch that may differ from the selected **Runtime Profile**'s **Model Override** without editing that profile.
_Avoid_: profile edit, model provider edit, catalog refresh

**Launch Profile Resolution**:
The daemon step that turns **Run Controls** into the **Runtime Profile** used for a **Task**, either by reusing an explicitly selected **Runtime Profile Preset** or by finding or creating a minimal matching **Runtime Profile** from a **Launch Selection**.
_Avoid_: live profile mutation, project-local profile fork

**Default Runtime Profile Preset**:
An optional **Project Defaults** reference to a global **Runtime Profile Preset** that preselects advanced launch configuration for new **Tasks** in that **Project**.
_Avoid_: copied profile, project-local profile, required launch picker

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
_Avoid_: account, credential bundle, secret store

**Runtime Profile Preset**:
A **Runtime Profile** saved for reuse because it carries advanced launch configuration such as **MCP Configuration**, **Runtime Extension Enablement**, binary paths, or runner defaults beyond a minimal **Launch Selection**.
_Avoid_: model provider, launch picker default, project-local copy

**Launch-Resolved Runtime Profile**:
A minimal global **Runtime Profile** created or reused by **Launch Profile Resolution** when no **Runtime Profile Preset** is selected. It is marked `launch_resolve` in storage, grouped separately from user-authored presets, and may be promoted to a **Runtime Profile Preset** after the user adds MCP, skills, or extension configuration.
_Avoid_: preset, project-local profile, launch-time copy

**Model Provider**:
A global reusable non-secret configuration for a model service that a **Runtime Profile** can use when a **Runtime** needs model access.
_Avoid_: runtime profile, runtime plugin, model only, credential value

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
An explicit user-triggered management action that fetches model names from a **Model Provider Endpoint** by appending `/models` to its **Normalized Model Base URL** and updates the **Model Catalog**.
_Avoid_: background polling, task-launch discovery, runtime introspection

**Model Catalog Refresh Format**:
The response shape accepted by **Model Catalog Refresh** when parsing a model-list response.
_Avoid_: protocol negotiation, provider-specific parser selection

**Model Provider Endpoint**:
A concrete non-secret base URL within a **Model Provider** that may support one or more **Model Provider Protocols**.
_Avoid_: protocol, runtime profile, credential value, custom header bundle

**Normalized Model Base URL**:
A **Model Provider Endpoint** base URL stored without trailing slashes while preserving path prefixes such as `/v1`.
_Avoid_: semantic URL repair, proxy route

**Model Override**:
A **Runtime Profile** field that replaces the selected **Model Provider**'s default model when that profile is used without a **Launch Model Override**.
_Avoid_: provider edit, endpoint fork, hidden model switch, launch-only override

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
The **Config Projection** step that passes the resolved model endpoint URL, protocol, model, and credential to a **Runtime** without proxying model traffic.
_Avoid_: LLM proxy, gateway request, daemon model call

**Model API Key Source**:
The required single source for the API key used by a **Model Provider**.
_Avoid_: credential reference, project override, runtime profile key

**Model API Key Environment Variable**:
The generated environment variable name used as the **Model API Key Source** for a **Model Provider**.
_Avoid_: user-entered env var, inline API key, secret value, credential reference

**Model Provider Snapshot**:
The non-secret resolved model provider values captured in a **Task Runtime Configuration** for one launch or continuation.
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
The default-on policy that enables newly uploaded or imported **Skills** for all current and future **Runtime Profiles** unless a profile opts out.
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

**Preset Selector**:
An advanced task-launch control for choosing an optional **Runtime Profile Preset** filtered to the selected **Runtime Plugin** family.
_Avoid_: primary launch picker, model provider switch, raw config editor

**Profile Selector**:
The settings-page control for choosing which **Runtime Profile** or **Runtime Profile Preset** to edit.
_Avoid_: task launch default, launch selection picker

**Protocol Pin Selector**:
The **Runtime Profile** control for choosing Auto or a compatible **Model Provider Protocol**.
_Avoid_: all-protocol list, runtime plugin editor

**Generated Runtime Config**:
A previewable task-local config output produced from structured profile fields during **Config Projection**.
_Avoid_: source of truth, raw profile

**MCP Configuration**:
Structured runtime interface configuration that defines available project-facing MCP servers for a **Runtime Profile**.
_Avoid_: raw JSON blob, unvalidated tool config

**Trusted MCP Server**:
An MCP server allowed to act as a **Project Interface** for project state, memory, evidence, or reporting.
_Avoid_: arbitrary MCP server, external tool server

**External MCP Server**:
A user-added MCP server that is available to a **Runtime** but is not trusted as a **Project Interface** by default.
_Avoid_: trusted project interface, built-in server

**Profile Config Import**:
An advanced action that parses edited runtime config back into structured **Runtime Profile** fields.
_Avoid_: raw config save, opaque override

**Task Runtime Configuration**:
The task-specific copy of runtime settings captured from a **Runtime Profile** for launching a **Task**, including any **Launch Model Override** applied at launch.
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
The task-local preparation of runtime configuration from a **Runtime Profile**, **Model Provider**, and **Credential References**.
_Avoid_: host config edit, config sync

**Preflight**:
A recorded startup check phase that determines whether a **Task** can launch its **Runtime**.
_Avoid_: runtime execution, pentest work

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
_Avoid_: metadata blob

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
A deliverable generated from **Findings**, **Project Facts**, **Fact Relations**, and **Evidence Artifacts**.
_Avoid_: transcript, export, source of truth

## Relationships

- A **Project** has exactly one current **Scope**.
- **Scope Expansion** is part of **Scope** but carries distinct **Provenance** from human-approved scope.
- An **Out-of-Scope Fact** does not change **Scope** and does not authorize testing.
- A **Project** may define **Project Defaults** for new **Tasks**, including an optional **Default Runtime Profile Preset** and default **Runner**.
- A **Project Defaults** reference to a **Default Runtime Profile Preset** preselects that preset on the task launch page but does not copy the **Runtime Profile**.
- When no **Default Runtime Profile Preset** is configured, task launch starts from **Launch Selection** and uses **Launch Profile Resolution** to find or create a minimal **Runtime Profile**.
- A **Project Dashboard** is the primary UI entry point for a **Project**.
- **Runtime Profiles** are global and reusable across **Projects**.
- A **Runtime Profile** selects one **Runtime Plugin** by plugin identifier.
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
- A **Model Provider Migration Preview** may show **Model Provider Migration Matches**, but the user chooses whether to reuse an existing **Model Provider** or create a new one.
- A successful **Model Provider Migration** removes migrated legacy model-service fields from the source **Runtime Profile**.
- A **Model Provider** may define a **Model Catalog**.
- A **Model Catalog** stores model identifiers, not full provider response objects.
- A **Model Catalog** may be edited manually or updated through **Model Catalog Refresh**.
- A **Model Catalog** may include manually entered model identifiers that were not returned by **Model Catalog Refresh**.
- **Manual Model Entries** are preserved when **Model Catalog Refresh** updates refreshed model identifiers.
- A **Manual Model Entry** with the same identifier as a refreshed model is merged into the refreshed model entry.
- A **Manual Model Entry** may be deleted unless it has been merged into a refreshed model entry.
- Refreshed model identifiers in a **Model Catalog** are not manually deleted or hidden.
- Any model identifier in the **Model Catalog**, whether manual or refreshed, may be used as a provider default or **Model Override**.
- A **Model Catalog Refresh** is a user-triggered management action, not part of **Preflight** or task launch.
- A **Model Catalog Refresh** uses `{normalized_model_base_url}/models` rather than a protocol-specific model list.
- A **Model Catalog Refresh** uses the **Model API Key Environment Variable** for the selected **Model Provider**.
- **Model Catalog Refresh Format** is OpenAI-style `/models` in MVP.
- A failed **Model Catalog Refresh** preserves the existing **Model Catalog**.
- A successful **Model Catalog Refresh** stores the returned **Model Catalog** even if existing defaults or overrides become invalid.
- A **Model Provider** may be saved without a **Model Catalog**, but tasks that require model access cannot launch from it.
- A **Model Catalog** drives model dropdown choices in **Runtime Profile** editing and in **Launch Selection**.
- A **Model Provider** must include exactly one **Model API Key Source**.
- A **Model API Key Source** is a **Model API Key Environment Variable** in MVP.
- A **Model API Key Environment Variable** is generated from the **Model Provider** identifier as `<PROVIDER_ID>_API_KEY`.
- A **Model Provider** contains exactly one **Model Provider Endpoint**.
- A **Model Provider Endpoint** stores a **Normalized Model Base URL**.
- A **Model Provider Endpoint** may support one or more **Model Provider Protocols**, such as `openai_chat_completions`, `openai_responses`, or `anthropic_messages`.
- **Model Provider Protocol** support is manually configured on the **Model Providers Page**.
- A **Model Provider** may be saved without configured **Model Provider Protocol** support, but tasks that require model access cannot launch from it.
- Removing **Model Provider Protocol** support from a **Model Provider** is allowed even if existing **Runtime Profiles** become invalid.
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
- **Skill Deletion** ends the enablement lifecycle for that **Skill ID**; re-importing the same **Skill ID** follows **Default Skill Enablement** instead of restoring old opt-outs.
- The **Skills Page** may change **Runtime Extension Enablement**, but the enablement state still belongs to the affected **Runtime Profile**.
- A **Runtime Profile** may reference a manually entered **Runtime Extension** identifier, but task launch still requires the daemon **Runtime Extension Registry** to resolve it.
- A new **Task** loads the current **Runtime Extensions** from the **Runtime Extension Library** when its runtime configuration is projected.
- **Preflight** previews enabled **Skills** but resolves credentials only from **Runtime Profiles**, **Model Providers**, and launch requests.
- **Preflight** includes a **Model Preflight Preview** when model access is used.
- A started **Task** keeps the **Runtime Extensions** already materialized into its task-local runtime boundary.
- **Runtime Extension Projection** happens during **Config Projection** and must not mutate host runtime plugin directories.
- A **Credential Reference** resolves first through **Credential Bindings**, then through **Global Credential Bindings**.
- A **Project** may define **Credential Bindings** that override **Global Credential Bindings** for **Credential References** used by global **Runtime Profiles**.
- **Credential Binding Mode** defaults to using **Global Credential Bindings** unless the user explicitly chooses a project override.
- A **Disabled Credential Binding** blocks fallback to **Global Credential Bindings**.
- A **Runtime Profile** may define a default **Runner** for new **Tasks**.
- A **Profile Selector** chooses which **Runtime Profile** or **Runtime Profile Preset** to edit on the settings page.
- A **Preset Selector** is an advanced task-launch control and is not the primary launch path.
- A **Preset Selector** lists only **Runtime Profile Presets** compatible with the selected **Runtime Plugin** family.
- Selecting a **Runtime Profile Preset** locks the **Launch Selection** runtime and **Model Provider** to that preset's values.
- A **Launch Model Override** may still be chosen at launch when a **Runtime Profile Preset** is selected.
- A selected **Runtime Profile Preset** keeps its **Runtime Profile** identity for the **Task** even when a **Launch Model Override** is used.
- A **Launch Model Override** affects only the launching **Task** and its captured **Task Runtime Configuration**; it does not edit the selected **Runtime Profile**.
- Changing the selected **Runtime Plugin** family during launch clears an incompatible **Runtime Profile Preset** selection.
- **Launch Profile Resolution** reuses an explicitly selected **Runtime Profile Preset** when one is chosen.
- **Launch Profile Resolution** otherwise finds or creates a minimal **Runtime Profile** that matches the **Launch Selection** runtime, **Model Provider**, and model choice.
- A minimal **Runtime Profile** created by **Launch Profile Resolution** is stored as a **Launch-Resolved Runtime Profile** (`launch_resolve`) and may later gain MCP, skills, or extension configuration without breaking reuse for the same **Launch Selection**.
- A **Launch-Resolved Runtime Profile** may be promoted to a **Runtime Profile Preset** (`manual`) without changing its identity or launch-matching behavior.
- **Skill Opt-Out** changes on a **Launch-Resolved Runtime Profile** apply to future launches that resolve to the same **Launch Selection**.
- A **Runtime Profile** uses structured fields as source of truth for **Generated Runtime Config**.
- **Generated Runtime Config** previews the resolved non-secret **Model Runtime Projection**, including base URL, protocol, model, generated API key environment variable name, and runtime-specific projection target.
- A **Runtime Plugin** describes which structured fields a **Runtime Profile** exposes.
- A **Runtime Profile** manages **MCP Configuration** as structured entries with raw preview or import as compatibility support.
- **MCP Configuration** may include **Trusted MCP Servers** and **External MCP Servers**.
- An **External MCP Server** does not receive project write authority by default.
- An **External MCP Server** follows the same **Runner** and **Sandbox** environment as its **Runtime**.
- **External MCP Server** output enters the **Blackboard** only when a **Runtime** writes it through a trusted **Project Interface**.
- A **Profile Config Import** updates a **Runtime Profile** only when the edited config can be parsed into structured fields.
- A **Project** has zero or more **Tasks**.
- A **Task** starts from one **Task Goal** plus **Run Controls**.
- A **Task** resolves to one **Runtime Profile** through **Launch Profile Resolution** and chooses one **Runner**.
- A **Task** has one **Runtime Harness** that controls runtime lifecycle for that task.
- A **Task** launches from its **Task Runtime Configuration**, not a live mutable **Runtime Profile**.
- A **Task Runtime Configuration** captures the selected **Runtime Plugin** identifier.
- A **Task Runtime Configuration** captures a **Model Provider Snapshot** when model access is used.
- A **Model Provider Snapshot** includes the resolved base URL, protocol, model, and non-secret **Model API Key Source** provenance.
- A **Model Provider Snapshot** uses a **Launch Model Override** when one was supplied at launch; otherwise it uses the selected **Runtime Profile**'s **Model Override** or **Model Catalog** default.
- A **Model Provider Snapshot** does not include the full **Model Catalog** or any credential value.
- A **Task Runtime Configuration** may include **Credential References** but not credential values.
- Editing a **Runtime Profile** does not change existing **Task Runtime Configurations**.
- Editing a **Model Provider** does not change existing **Task Runtime Configurations** or an active **Runtime Continuation**.
- A **Model Provider** cannot be deleted while any **Runtime Profile** still references it.
- Historical task views read captured **Task Runtime Configurations** and **Model Provider Snapshots**, not live **Runtime Profiles** or live **Model Providers**.
- A runtime-profile switch inside a **Task** creates a new **Task Runtime Configuration Version** for the next **Runtime Continuation**.
- A runtime-profile switch re-resolves the selected **Model Provider** and captures a new **Model Provider Snapshot** for the new **Task Runtime Configuration Version**.
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
- A **Task** may override its **Runtime Profile**'s default **Runner**, and that override is recorded as a task event.
- A **Task** uses **Config Projection** to prepare runtime configuration without mutating host runtime configuration.
- A **Config Projection** failure belongs to the affected **Task** unless the **Runtime Profile** itself is explicitly invalid.
- A **Task** passes **Preflight** before its **Runtime** starts.
- A **Credential Reference** that cannot be resolved during **Preflight** prevents **Runtime** launch.
- A missing **Model API Key Environment Variable** value prevents **Runtime** launch during **Preflight**.
- A required **Model Provider Requirement** that cannot resolve a compatible **Model Provider Protocol** prevents **Runtime** launch during **Preflight**.
- A **Preflight** failure prevents **Runtime** execution.
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
- A **Host Runner** runs outside a **Sandbox** and must be visible in the **Report**.
- A **Host Runner Activation** requires explicit operator activation before launch.
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
- A rejected **Scope Expansion** may leave an **Out-of-Scope Fact** for context and audit.
- A **Policy Violation** marks the affected **Task** but does not automatically pause the whole **Project**.
- Direct runtime writes to storage outside **Project Interfaces** are recorded as **Policy Violations** when detected.
- **Task Events** explain what happened inside one **Task**.
- A **Report** presents project conclusions but is not itself the source of truth for **Findings** or **Project Facts**.
- A **Report** presents high-signal **Provenance** without expanding every **Task Event**.
- A **Report** distinguishes **Tentative Facts** from confirmed conclusions.
- A **Report** may include unconfirmed **Findings** separately from **Confirmed Findings**.

## Example dialogue

> **Dev:** "The Runtime discovered a new subdomain during a task. Should it write that straight into Scope?"
> **Domain expert:** "No. It should create a Scope Expansion request. If accepted, the Project Scope changes and the decision is recorded."

> **Dev:** "The Runtime confirmed SQL injection and saved the HTTP exchange. Is that a Project Fact or a Finding?"
> **Domain expert:** "Both can be involved: the reproducible issue is a Finding, and the reproduction context can be stored as Project Facts with Evidence Artifacts attached."

> **Dev:** "Task launch no longer asks for a Runtime Profile. Where do MCP and skills come from?"
> **Domain expert:** "Most launches only need Launch Selection: runtime, model provider, and model. The daemon resolves that to a minimal Runtime Profile automatically. If the user expands the advanced preset picker and chooses a saved Runtime Profile Preset, that preset's MCP and skill enablement apply. Runtime and model provider lock to the preset, but the user may still set a Launch Model Override for just that task."

## Flagged Ambiguities

- "vulnerability" and **Finding** were used for the same reportable issue concept; resolved: use **Finding** as the product/domain term and reserve "vulnerability" for type names, schemas, or imported source terminology.
- **Policy Violation** is not an approval state; resolved: it records a workflow breach that may be detected after the fact.
- **Policy Violation** is not automatic project suspension; resolved: flag the affected task strongly and leave project-level pause decisions to a human.
- Direct storage mutation is not trusted project state; resolved: use **Reconciliation** before such content affects **Current Truth** or reports.
- **Reconciliation** is not runtime self-approval; resolved: runtime-discovered candidates stay untrusted until accepted by a human or explicit project policy.
- **Host Runner Activation** is not implicit host fallback; resolved: host execution requires explicit activation and must be visible in report output.
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
- **Runtime Profile** is not the primary task-launch picker; resolved: most launches use **Launch Selection**, while **Runtime Profile Presets** are optional advanced choices.
- **Profile Selector** is not the default launch control; resolved: task launch uses **Launch Selection** plus an optional **Preset Selector**, and settings-page editing keeps **Profile Selector**.
- **Launch Profile Resolution** is not a live profile edit; resolved: it selects or creates the **Runtime Profile** record used for the task without mutating preset fields for a one-off model change.
- A **Launch Model Override** is not a **Runtime Profile** edit; resolved: it changes only the launching task's captured model choice and snapshot.
- **Default Runtime Profile Preset** is not a copied profile; resolved: project defaults may reference a global preset without creating project-local runtime configuration.
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
- **Model Provider Protocol** support is not auto-detected; resolved: users explicitly configure supported protocols for a model provider.
- Empty **Model Provider Protocol** support is allowed for provider configuration; resolved: it is a management draft state, not launch-ready task configuration.
- Removing **Model Provider Protocol** support is not blocked by affected runtime profiles; resolved: save the provider change and surface invalid strict pins through validation and preflight.
- **Model Provider Endpoint** is not a protocol; resolved: one endpoint may support multiple model-provider protocols.
- **Model Provider Endpoint** is not a plural endpoint set; resolved: each model provider has exactly one endpoint/base URL.
- **Model Provider Endpoint** is not proxy configuration; resolved: endpoints carry base URLs and supported protocols, not custom headers or arbitrary request-shaping settings.
- **Normalized Model Base URL** is not the model-list URL; resolved: preserve path prefixes and append `/models` only for **Model Catalog Refresh**.
- **Normalized Model Base URL** is not semantically repaired; resolved: do not detect, reject, or trim model-list paths such as `/models`.
- **Model Provider Endpoint** selection is not guessed at task runtime; resolved: each model provider has one endpoint and runtime plugins only resolve protocol preference.
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
- **Model Catalog Refresh** is not automatic model discovery during task startup; resolved: refresh happens only through an explicit management action that fetches `{normalized_model_base_url}/models`.
- **Model Catalog Refresh** does not use a separate credential path; resolved: it reads the same generated API key environment variable as runtime launch.
- **Model Catalog Refresh Format** is not provider-specific in MVP; resolved: parse only OpenAI-style `/models` responses.
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
- **Model Runtime Projection** is not LLM proxying; resolved: the daemon passes URL, protocol, model, and credential into the runtime, and the runtime calls the model service directly.
- **Model Provider Requirement** is not universal; resolved: runtime plugins declare whether model-provider resolution is required, optional, or unsupported.
- Runtime-profile switching does not create a new **Task**; resolved: it creates a new **Runtime Continuation** with a new **Task Runtime Configuration Version**.
- Runtime-profile switching does not reuse the prior **Model Provider Snapshot**; resolved: each new task runtime configuration version captures its own resolved model provider values.
- **Model Provider** edits are not live task mutation; resolved: active continuations keep the **Model Provider Snapshot** captured at launch or continuation start.
- **Model Provider** deletion is not silent profile breakage; resolved: deletion is blocked while any runtime profile references the provider.
- Historical task inspection does not require live profile or provider records; resolved: task history uses captured runtime configuration snapshots.
- **Project Defaults** are not copied **Runtime Profiles**; resolved: they select defaults while profiles remain global.
- **Project Dashboard** is not a chat-first view; resolved: the project home prioritizes scope, task runs, blackboard state, findings, and evidence.
- **Task Goal** is not the whole task configuration; resolved: natural-language goals are paired with visible **Run Controls**.
- **Harness Steering** is not direct pentest tool control; resolved: it controls runtime continuation inside the same **Task** through the **Runtime Harness**.
- **Runtime Continuation** is not live thought editing; resolved: steering applies at input, checkpoint, interrupt, or resume boundaries.
- **Harness Steering** is not silent run-control mutation; resolved: runner, profile, or other run-control changes apply through explicit task events and only at continuation boundaries.
- **Profile Selector** is not raw configuration editing; resolved: switching profiles is fast, while editing profiles remains structured.
- **Generated Runtime Config** is not the editable source of truth; resolved: raw config preview and diff are derived from structured profile fields.
- **Generated Runtime Config** is not a secret preview; resolved: show generated API key environment variable names and projection targets, not API key values.
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
- **Model Preflight Preview** is not a secret display; resolved: show base URL, protocol, model, generated API key environment variable name, and configured/missing status without showing key values.
- **CLI Fallback** is not a bypass; resolved: CLI writes carry the same validation, provenance, audit, and blackboard semantics as other project interfaces.
- **Task Event** and project-level history are distinct; resolved: task events are task-local timeline entries, while project-level records are security history.
- **Task Event** is not raw output storage; resolved: preserve full output through logs or **Evidence Artifacts** and keep the task timeline structured.
- **Task Summary** is not daemon-authored intelligence; resolved: runtimes maintain summary candidates, while the daemon stores and injects accepted summaries.
- **Task Summary** acceptance is automatic; resolved: the latest runtime-submitted summary is accepted while prior versions remain inspectable.
- **Mechanical Handoff Packet** is not an LLM summary; resolved: it is structured fallback context assembled without semantic summarization.
- **Provenance** is not chronological history; resolved: provenance is the source context attached to conclusions, while project history is chronological security records.
- **Report** provenance is summarized, not exhaustive; resolved: reports show the runner, scope context, and key evidence rather than every task event.
- **Tentative Fact** is visible current context, not confirmed conclusion; resolved: current views may include it with confidence while reports mark it separately from confirmed findings.
- Unconfirmed **Findings** are not confirmed report conclusions; resolved: reports may show them as needing validation outside the confirmed findings summary.
- **Runtime Workdir** is not shared memory; resolved: cross-task knowledge flows through **Blackboard** and retained artifacts.
- **Runtime Workdir** is not cross-runtime handoff state; resolved: runtime-profile switches pass context through **Blackboard**, summaries, and retained artifacts.
- **Runtime Workdir** is not automatic evidence capture; resolved: files become **Evidence Artifacts** only through explicit attach or retain actions.
