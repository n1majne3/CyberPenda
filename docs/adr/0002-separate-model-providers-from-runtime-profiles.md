# Separate model providers from runtime profiles

Runtime profiles choose how a runtime CLI is launched and projected, while model-service configuration is reusable across multiple runtime profiles. We will introduce global model providers with one or more protocol-specific base URLs, let runtime plugins declare supported protocols and protocol preference, and capture the resolved endpoint base URL as `endpoint_base_url` alongside protocol, credentials, and model into each task runtime configuration at launch. This avoids duplicating the same model service account across Codex, Claude Code, and Pi profiles while keeping runtime-specific projection logic in runtime plugins.

## Considered Options

- Keep model, endpoint, and model API credentials directly on runtime profiles. This is simpler in the short term but duplicates shared model-service configuration and mixes runtime CLI concerns with model-service concerns.
- Allow runtime profiles to select reusable model providers. This adds an explicit resolution step but keeps model-service configuration reusable and makes compatibility/preflight deterministic.

## Consequences

- Canonical protocol identifiers are concrete API contracts: `openai_chat_completions`, `openai_responses`, and `anthropic_messages`.
- Model provider protocol support is manually configured through endpoint records and not auto-detected.
- Model providers may be saved without configured protocol support as draft configurations, but dependent task launches fail preflight until a compatible protocol is available.
- Removing endpoint records, and therefore protocol support, from a model provider is allowed even when existing runtime profiles become invalid; validation and preflight surface affected strict pins.
- Runtime plugin manifests define each runtime family's supported protocols and protocol preference; users do not configure runtime protocol preference manually.
- Runtime profile protocol pin UI shows Auto plus the intersection of protocols supported by the selected runtime plugin and model provider.
- Generated runtime config previews resolved non-secret model projection details, including endpoint base URL, protocol, model, generated API key environment variable name, and runtime-specific projection target, but not API key values.
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
- Legacy migration uses the same Anthropic final-segment adaptation as endpoint backfill: when deriving an `anthropic_messages` endpoint from a legacy `base_url`, remove the final non-empty URL path segment when one exists.
- Legacy migration may show possible existing-provider matches, but users choose whether to reuse an existing provider or create a new one; matches are not auto-merged.
- Successful legacy migration writes the model provider reference back to the runtime profile and clears migrated legacy model-service fields to avoid dual sources of truth.
- A model provider may have protocol-specific base URLs under one shared non-secret provider configuration and API key source.
- Multiple Model Provider Endpoints for one provider commonly share the same URL origin while differing only by Model Protocol Path Prefix, such as `/v1`, `/api/anthropic`, or `/api/coding/paas/v4`.
- Model Provider Endpoint records are stored as an `endpoints` list of `{protocol, base_url}` records rather than a map keyed by protocol.
- Endpoint storage does not split origin and path prefix into separate canonical fields; management UI may expose that split as an editing convenience and save the composed `base_url`.
- The Model Providers Page offers quick setup for the common case: enter one shared provider base URL, commonly up to the provider API version path such as `/v1` or `/v2`, then derive Model Provider Endpoint `base_url` values from it.
- Quick setup uses the shared provider base URL as the default `openai_chat_completions` and `openai_responses` `base_url`; it does not hardcode appending `/v1`.
- Quick setup derives the default `anthropic_messages` `base_url` by removing the final non-empty URL path segment from the shared provider base URL when one exists.
- Users can override individual endpoint `base_url` values when protocol paths differ; overrides save into the same composed `endpoints[]` shape.
- Provider-level `protocols` disappears from the new API/storage shape; provider protocol support is derived from `endpoints[].protocol`. Old `protocols` is read only for compatibility backfill.
- Example base URLs include `https://hub.example.com` for `anthropic_messages`, `https://hub.example.com/v1` for `openai_responses`, and `https://hub.example.com/v1` for `openai_chat_completions`; the selected runtime appends its own operation suffix.
- Runtime plugins pass the selected endpoint base URL to the runtime rather than appending operation suffixes in the daemon.
- Model provider management does not auto-fill, append, or store operation suffixes in endpoint `base_url` values.
- Model provider base URLs are normalized by removing trailing slashes before validation, storage, migration, and backfill derivation while preserving path prefixes such as `/v1`, `/api/anthropic`, or `/api/coding/paas/v4`.
- Model provider management rejects endpoint `base_url` values that already end with any known operation suffix after trailing slash normalization, including `/v1/messages`, `/messages`, `/v1/responses`, `/responses`, `/v1/chat/completions`, or `/chat/completions`, rather than silently stripping that suffix.
- Model catalog refresh uses a derived URL ending in `/v1/models`.
- Model catalog refresh derives its URL from an OpenAI-family endpoint origin, preferring `openai_chat_completions` over `openai_responses`, then appends `/v1/models`. It does not use arbitrary protocol URI paths such as `/api/anthropic` or `/api/coding/paas/v4` for the model-list URL.
- For example, an OpenAI-family Model Provider Endpoint with `base_url` `https://open.bigmodel.cn/api/coding/paas/v4` refreshes from `https://open.bigmodel.cn/v1/models`, not from that endpoint record's Model Protocol Path Prefix.
- There is no custom `catalog_base_url` field; the model-list path is always `/v1/models`.
- Existing providers with provider-level `base_url` and `protocols` are automatically backfilled by treating the normalized old `base_url` as legacy runtime base URL source data. Backfill creates one endpoint per old protocol.
- Backfill derives `anthropic_messages` endpoint `base_url` by removing the final non-empty URL path segment from the old `base_url` when one exists, because Claude Code appends its own versioned messages operation path.
- Backfill copies the normalized old `base_url` into non-Anthropic endpoint `base_url` values.
- Base URLs are not otherwise semantically repaired; the daemon does not infer arbitrary provider path changes beyond the explicit Anthropic final-segment adaptation.
- A provider's model catalog may be manually edited or explicitly refreshed from the model-list API; refresh is not automatic during preflight or task launch.
- Model catalog refresh uses the same generated API key environment variable as runtime launch.
- In MVP, model catalog refresh parses only OpenAI-style `/v1/models` responses.
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
- Task runtime configuration captures a non-secret model provider snapshot for the launch or continuation: `endpoint_base_url`, protocol, model, and API key source provenance, but not the full model catalog or key value.
- During transition, model provider snapshots may expose `base_url` as a compatibility alias for `endpoint_base_url`; new code should use `endpoint_base_url`.
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
