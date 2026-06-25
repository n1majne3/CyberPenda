# Separate model providers from runtime profiles

Runtime profiles choose how a runtime CLI is launched and projected, while model-service configuration is reusable across multiple runtime profiles. We will introduce global model providers with one endpoint/base URL that may support multiple model protocols, let runtime plugins declare supported protocols and protocol preference, and capture the endpoint, resolved protocol, credentials, and model into each task runtime configuration at launch. This avoids duplicating the same model service account across Codex, Claude Code, and Pi profiles while keeping runtime-specific projection logic in runtime plugins.

## Considered Options

- Keep model, endpoint, and model API credentials directly on runtime profiles. This is simpler in the short term but duplicates shared model-service configuration and mixes runtime CLI concerns with model-service concerns.
- Allow runtime profiles to select reusable model providers. This adds an explicit resolution step but keeps model-service configuration reusable and makes compatibility/preflight deterministic.

## Consequences

- Canonical protocol identifiers are concrete API contracts: `openai_chat_completions`, `openai_responses`, and `anthropic_messages`.
- Model provider protocol support is manually configured and not auto-detected.
- Model providers may be saved without configured protocol support as draft configurations, but dependent task launches fail preflight until a compatible protocol is available.
- Removing protocol support from a model provider is allowed even when existing runtime profiles become invalid; validation and preflight surface affected strict pins.
- Runtime plugin manifests define each runtime family's supported protocols and protocol preference; users do not configure runtime protocol preference manually.
- Runtime profile protocol pin UI shows Auto plus the intersection of protocols supported by the selected runtime plugin and model provider.
- Generated runtime config previews resolved non-secret model projection details, including base URL, protocol, model, generated API key environment variable name, and runtime-specific projection target, but not API key values.
- Preflight shows the same non-secret model projection details and whether the generated API key environment variable is configured or missing, without showing the value.
- Codex supports `openai_responses`.
- Claude Code supports `anthropic_messages`.
- Pi supports `openai_chat_completions`, `openai_responses`, and `anthropic_messages`, preferring them in that order when a profile does not pin a model provider protocol.
- Runtime profile protocol pins are optional; an empty pin uses the selected runtime plugin's protocol preference.
- A pinned protocol is strict; if it is deleted or becomes incompatible, profile validation and preflight fail rather than silently falling back.
- Model providers are managed through a global Model Providers settings page, not embedded as runtime profile subforms.
- Runtime profiles that require model access may be saved without a selected model provider as draft configurations, but validation and preflight block launch.
- Legacy runtime-profile model fields remain readable for compatibility, and migration into model providers is an explicit user-confirmed management action rather than silent guessing.
- Legacy migration previews suggest protocol from the source runtime plugin, show provider name/base URL/model/protocol/API key source provenance, and require user confirmation before creating a model provider.
- Legacy migration may show possible existing-provider matches, but users choose whether to reuse an existing provider or create a new one; matches are not auto-merged.
- Successful legacy migration writes the model provider reference back to the runtime profile and clears migrated legacy model-service fields to avoid dual sources of truth.
- A model provider has exactly one endpoint/base URL; protocols affect API compatibility, not model availability.
- Model provider endpoint base URLs are normalized by removing trailing slashes while preserving path prefixes such as `/v1`; model catalog refresh fetches `{base_url}/models`.
- Endpoint base URLs are not semantically repaired; the daemon does not detect, reject, or trim model-list paths such as `/models`.
- A provider's model catalog may be manually edited or explicitly refreshed from the model-list API; refresh is not automatic during preflight or task launch.
- Model catalog refresh uses the same generated API key environment variable as runtime launch.
- In MVP, model catalog refresh parses only OpenAI-style `/models` responses.
- Model catalogs store model identifiers only, not full provider response objects.
- Model catalogs may include manually entered model identifiers that were not returned by refresh, because some endpoints cannot list models.
- Model catalog refresh preserves manually entered model identifiers that are not returned by refresh; if refresh returns the same identifier, it becomes a refreshed entry rather than a duplicate.
- Refreshed model identifiers are not manually deleted or hidden.
- Manual model identifiers may be deleted while they remain manual; once returned by refresh they are treated as refreshed entries.
- Any model identifier in the catalog, manual or refreshed, can be used as the provider default or runtime-profile model override.
- Failed model catalog refreshes preserve the previous catalog and surface the refresh error.
- Successful model catalog refreshes save the returned list even when existing defaults or runtime-profile overrides become invalid; validation and preflight surface the stale selections.
- Model providers may be saved with an empty model catalog as a management draft state, but dependent task launches fail preflight until a valid model is available.
- Invalid model defaults or stale runtime-profile model overrides fail validation and preflight rather than silently selecting another model.
- Each model provider has exactly one model API key source; runtime profiles retain only runtime-specific credential needs by default.
- In MVP, model API key sources are generated environment variable names derived from the model provider identifier, such as `MIMO_API_KEY`, not inline stored secret values.
- Model provider IDs are immutable after creation because they determine generated API key environment variable names; display names remain editable.
- Model provider IDs are generated from display names at creation time rather than entered manually.
- Model provider ID collisions are resolved by appending numeric suffixes, and generated API key environment variables derive from the final ID.
- Renaming a model provider changes only its display name; the immutable ID and generated API key environment variable remain unchanged.
- Model provider API key sources are not project-overridable in the model-provider flow.
- Task runtime configuration captures a non-secret model provider snapshot for the launch or continuation: base URL, protocol, model, and API key source provenance, but not the full model catalog or key value.
- Runtime profile switches re-resolve the selected model provider and capture a new model provider snapshot in the new task runtime configuration version.
- Editing a model provider does not affect existing task runtime configurations or active continuations.
- Model provider deletion is blocked while any runtime profile references the provider; historical task snapshots do not count as live references.
- Historical task views use captured task runtime configuration snapshots and do not require live runtime profile or model provider records.
- The daemon is not an LLM proxy; runtime plugins project URL, API protocol, model, and credential into the selected runtime, and the runtime calls the model service directly.

## Launch UX

Task launch separates everyday model/runtime choices from advanced runtime-profile presets.

- The primary launch path is **Launch Selection**: runtime family, model provider, and model. The daemon uses **Launch Profile Resolution** to find or create a minimal matching runtime profile when no preset is selected.
- An advanced collapsed **Preset Selector** lets operators choose an optional **Runtime Profile Preset** that carries MCP, skills, extension, binary, and runner defaults beyond the minimal launch selection.
- The preset list is filtered to the selected runtime family. Changing runtime clears an incompatible preset selection.
- Selecting a preset locks runtime and model provider to the preset values. Model remains editable at launch.
- A selected preset launches with that preset's `runtime_profile_id` directly; it does not run `resolve-launch` and therefore keeps the preset's MCP/skills/extensions intact.
- **Launch Model Override** is an optional task-only model choice sent with preflight and task creation alongside `runtime_profile_id`. It affects preflight preview, projection, and the captured **Model Provider Snapshot** for that task only. It does not edit the selected runtime profile.
- **Project Defaults** may optionally designate a **Default Runtime Profile Preset** through `defaults.runtime_profile`. When present, launch opens with that preset selected; when absent, launch starts from Launch Selection and auto-resolution.
