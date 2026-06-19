# Runtime Plugin Design

## Status

Draft design for implementation planning.

## Goal

Make runtime providers config-first so adding or changing a runtime provider is mostly a manifest change instead of coordinated edits across adapters, runner projection, preflight, transcript parsing, and the React profile UI.

## Context

Runtime behavior is currently spread across provider-specific branches:

- internal/runtimeprofile validates a closed provider set.
- internal/adapters builds provider launch arguments.
- internal/runner projects provider-specific config files and process environment.
- internal/preflight reasons about profile, runner, and credentials.
- internal/transcript chooses provider-specific output parsing.
- web/src/pages/RuntimeProfilesPage.tsx hardcodes provider preview behavior.

This has worked for Codex, Claude Code, Pi, and the fake runtime, but each provider fix now touches multiple layers. The recent Pi credential and Claude transcript changes are examples: both were provider-specific config details that should be described in one runtime plugin manifest and consumed by shared code.

## Design Decision

Use declarative runtime plugin manifests as the source of truth for provider metadata, profile fields, launch templates, config projection primitives, environment requirements, preflight checks, and transcript parser selection.

Do not use Go plugin, arbitrary JavaScript, or downloaded executable plugins in v0. A runtime plugin may describe behavior, but execution remains inside existing daemon primitives and the selected runner boundary.

This keeps the system:

- Auditable: plugin behavior is plain JSON.
- Local-first: no marketplace or remote extension loading.
- Testable: manifest validation and projection are deterministic.
- Safe by default: manifests cannot execute host code.
- Compatible: existing runtime profiles continue to store their provider/plugin identifier in the current provider field.

## Runtime Plugin Manifest

Runtime plugins are JSON documents loaded into a registry. Built-ins are embedded first; external trusted manifests can be added after the registry contract is stable.

Example:

~~~json
{
  "schema_version": 1,
  "id": "claude_code",
  "name": "Claude Code",
  "description": "Claude Code CLI runtime provider.",
  "binary": {
    "default": "claude",
    "profile_field": "binary_path"
  },
  "capabilities": {
    "sandbox": true,
    "host": true,
    "mcp_config": true,
    "streaming_transcript": true,
    "resume": true
  },
  "profile_schema": {
    "fields": [
      { "name": "model", "type": "string", "label": "Model" },
      { "name": "endpoint", "type": "url", "label": "Endpoint" },
      { "name": "custom_args", "type": "string_list", "label": "Custom args" },
      { "name": "env", "type": "env_map", "label": "Environment" },
      { "name": "credential_refs", "type": "string_list", "label": "Credential refs" },
      { "name": "mcp_servers", "type": "mcp_servers", "label": "MCP servers" },
      { "name": "default_runner", "type": "runner", "label": "Default runner" },
      { "name": "sandbox_image", "type": "string", "label": "Sandbox image" }
    ]
  },
  "config_projection": {
    "primitive": "claude_settings",
    "config_path": "runtime-home/claude/settings.json",
    "mcp_config_path": "workdir/.mcp.json"
  },
  "launch": {
    "args": [
      "{{binary}}",
      "--model", "{{model}}",
      "--settings", "{{config_path}}",
      "{{mcp_args}}",
      "-p",
      "--output-format", "stream-json",
      "--verbose",
      "{{custom_args}}",
      "--",
      "{{goal}}"
    ]
  },
  "process_env": {
    "CLAUDE_HOME": "{{runtime_home}}/claude"
  },
  "credential_env": [
    "ANTHROPIC_AUTH_TOKEN",
    "ANTHROPIC_API_KEY"
  ],
  "transcript": {
    "parser": "claude_stream_json"
  }
}
~~~

## Template Variables

Launch and environment templates use a small fixed variable set:

- {{binary}}: resolved binary path or plugin default.
- {{goal}}: task goal or continuation prompt.
- {{model}}: profile model field.
- {{endpoint}}: profile endpoint field.
- {{config_path}}: runner-adjusted projected config path.
- {{mcp_config_path}}: runner-adjusted MCP config path.
- {{mcp_args}}: plugin-specific generated MCP argument segment.
- {{custom_args}}: profile custom args list.
- {{runtime_home}}: runner-adjusted runtime home.
- {{workdir}}: runner-adjusted task workdir.

Templates produce an argument vector, not a shell string. Empty variables are omitted unless the manifest marks them as required.

## Registry

The runtime plugin registry owns:

- Built-in plugin loading.
- Manifest validation.
- Lookup by plugin ID.
- Capability queries.
- Stable ordering for UI display.
- Optional external manifest loading from a trusted local directory.

The registry is a daemon dependency used by runtime profiles, preflight, config projection, launch argument construction, transcript projection, and profile UI APIs.

## Runtime Profile Compatibility

The existing runtime_profiles.provider column remains the persisted plugin identifier for v0. Existing values map directly:

- codex
- claude_code
- pi
- fake

The runtimeprofile.Provider Go type may remain a string alias, but validation should move from a hardcoded package map to the runtime plugin registry.

No database migration is required for v0.

## Config Projection

Config projection remains implemented by built-in primitives in v0. The manifest chooses the primitive and supplies paths and template inputs.

Initial primitives:

- codex_home
- claude_settings
- pi_agent
- none for fake/runtime tests

This avoids making JSON manifests responsible for complex file formats while still moving provider selection, paths, and preview metadata into the manifest.

## Launch Arguments

Launch argument construction should become template-driven.

Rules:

- Templates produce argument arrays.
- List variables splice into the argument vector.
- Optional groups such as MCP args are omitted when not applicable.
- User custom args are preserved.
- Manifest defaults may be suppressed by explicit custom args when the plugin declares an option as singleton, such as --output-format.
- Secrets must never be rendered into launch args.

## Credentials

Runtime plugins may declare credential environment names they can consume, but they do not store secret values.

Credential values continue to resolve through credential references and bindings during preflight and launch. Redaction remains centralized in runtime event emission.

## MCP

Plugins declare whether they support MCP config projection and how the runtime receives MCP config:

- command-line arguments,
- config file path,
- environment variable,
- or unsupported.

Trusted and external MCP server semantics do not change. A plugin cannot grant project write authority by declaring an MCP server.

## Transcript Parsing

Plugins select a built-in transcript parser by name:

- codex_json
- claude_stream_json
- pi_json_session
- plain_runtime_output

The parser implementation remains Go code in v0. Unknown parser names fail manifest validation.

## HTTP API

Add:

~~~text
GET /api/runtime-plugins
GET /api/runtime-plugins/{id}
~~~

List response:

~~~json
{
  "plugins": [
    {
      "id": "claude_code",
      "name": "Claude Code",
      "description": "Claude Code CLI runtime provider.",
      "capabilities": {
        "sandbox": true,
        "host": true,
        "mcp_config": true,
        "streaming_transcript": true
      },
      "profile_schema": {
        "fields": []
      }
    }
  ]
}
~~~

The API returns public metadata only. It does not return resolved credential values or host-specific secret state.

## User Experience

Runtime profile management changes from a hardcoded provider switch to a plugin-driven profile editor:

- The provider selector lists runtime plugins from /api/runtime-plugins.
- The form renders supported profile fields from the plugin schema.
- Generated preview uses plugin manifest metadata and shared projection code.
- Unsupported or invalid external plugin manifests are shown as registry errors outside task launch.

Existing profiles remain editable without migration.

## External Plugins

External plugins should be a later slice after built-ins are registry-backed.

Rules for external manifests:

- Loaded only from configured trusted local directories.
- Disabled by default unless the daemon config opts in.
- Manifest validation errors prevent that plugin from appearing as launchable.
- External manifests cannot define new executable hooks in v0.
- External manifests may only reference known projection primitives and transcript parsers.

## Security

- Runtime plugin manifests are not trusted code.
- A plugin cannot execute host code except by launching its declared runtime through the selected Runner.
- Host runner activation rules remain unchanged.
- Sandbox remains the default runner boundary.
- Credential values remain outside manifests and runtime profiles.
- Redaction applies after env and arg projection.
- A malicious external manifest can still point at a risky binary, so external manifests require explicit local trust configuration.

## Rollout

1. Add registry and built-in manifests without changing task behavior.
2. Expose plugin API and update profile UI to read plugin metadata.
3. Move provider validation to the registry.
4. Move launch args to manifest templates.
5. Move projection path/parser selection to manifest metadata while keeping projection primitives in Go.
6. Add trusted external manifest loading.

Each step should keep existing Codex, Claude Code, Pi, and fake profiles working.

## Non-Goals

- Remote plugin marketplace.
- Arbitrary Go, JavaScript, or shell hooks in plugin manifests.
- Plugin-specific database migrations.
- Per-pentest-tool plugins.
- Replacing MCP configuration.
- Changing project trust or blackboard write semantics.

## Open Questions

- Should external plugin loading be daemon config only, or should the UI manage trusted plugin directories?
- Should plugin manifests support version constraints against the daemon version in v0?
- Should profile schemas support conditional fields in v0, or only fixed field lists?
