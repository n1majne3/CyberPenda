# Runtime Plugin Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox syntax for tracking.

**Goal:** Add a manifest-first runtime plugin system so runtime providers are described by declarative configuration and consumed through a daemon registry.

**Architecture:** Introduce an internal runtimeplugin package that validates manifests, loads built-ins, renders launch templates, and exposes public plugin metadata. Wire the registry into daemon APIs, runtime profile validation, launch argument construction, config projection metadata, transcript parser selection, and the runtime profile UI without changing the existing database schema.

**Tech Stack:** Go services and HTTP handlers, SQLite-backed runtime profiles, React + TypeScript runtime profile UI, existing Vite build and Go test setup.

---

## File Structure

- Create internal/runtimeplugin/plugin.go: public manifest types, capabilities, profile schema, projection metadata, launch template fields.
- Create internal/runtimeplugin/registry.go: registry construction, lookup, stable listing, and validation.
- Create internal/runtimeplugin/builtin.go: embedded built-in manifests for fake, codex, claude_code, and pi.
- Create internal/runtimeplugin/template.go: argument and environment template rendering.
- Create internal/runtimeplugin/plugin_test.go: manifest validation and built-in registry tests.
- Create internal/runtimeplugin/template_test.go: launch template rendering tests.
- Modify internal/runtimeprofile/runtimeprofile.go: validate provider identifiers through supported plugin IDs instead of a hardcoded provider map.
- Modify internal/daemon/server.go: construct and expose the runtime plugin registry.
- Create or modify internal/daemon/runtime_plugin_handlers.go: GET /api/runtime-plugins and GET /api/runtime-plugins/{id}.
- Modify internal/adapters/adapters.go: render launch args from plugin templates while preserving provider-specific compatibility where needed.
- Modify internal/runner/projection.go: read projection primitive and paths from plugin metadata while keeping projection primitives in Go.
- Modify internal/transcript/transcript.go: route parser selection through plugin parser names when the adapter/provider is known.
- Modify web/src/lib/api.ts: add runtime plugin metadata types.
- Modify web/src/pages/RuntimeProfilesPage.tsx: load plugins, render provider options from plugin metadata, and use plugin schema/preview metadata.
- Rebuild internal/daemon/webfs/dist through make build-ui.

## Task 1: Runtime Plugin Manifest Types

**Files:**
- Create: internal/runtimeplugin/plugin.go
- Create: internal/runtimeplugin/plugin_test.go

- [ ] **Step 1: Write manifest validation tests**

Add tests that cover:

- A valid Claude Code manifest passes validation.
- Missing id fails.
- Unknown config projection primitive fails.
- Unknown transcript parser fails.
- Duplicate profile field names fail.
- A manifest that declares credential values instead of credential env names fails.

Run: env GOCACHE=/Users/benjamin/Documents/pentest/.cache/go-build go test ./internal/runtimeplugin -run TestValidateManifest -count=1

Expected: FAIL because the package does not exist.

- [ ] **Step 2: Add manifest structs**

Implement these types in internal/runtimeplugin/plugin.go:

~~~go
package runtimeplugin

type Plugin struct {
    SchemaVersion    int              json:"schema_version"
    ID               string           json:"id"
    Name             string           json:"name"
    Description      string           json:"description,omitempty"
    Binary           Binary           json:"binary"
    Capabilities     Capabilities     json:"capabilities"
    ProfileSchema    ProfileSchema    json:"profile_schema"
    ConfigProjection ConfigProjection json:"config_projection"
    Launch           LaunchTemplate   json:"launch"
    ProcessEnv       map[string]string json:"process_env,omitempty"
    CredentialEnv    []string         json:"credential_env,omitempty"
    Transcript       Transcript       json:"transcript"
}

type Binary struct {
    Default      string json:"default"
    ProfileField string json:"profile_field,omitempty"
}

type Capabilities struct {
    Sandbox             bool json:"sandbox"
    Host                bool json:"host"
    MCPConfig           bool json:"mcp_config"
    StreamingTranscript bool json:"streaming_transcript"
    Resume              bool json:"resume"
}

type ProfileField struct {
    Name  string json:"name"
    Type  string json:"type"
    Label string json:"label"
}
~~~

Use real struct tags in the implementation.

- [ ] **Step 3: Implement Validate**

Add Validate(plugin Plugin) error. Validation must reject:

- schema_version other than 1,
- blank id or name,
- unknown profile field type,
- duplicate field names,
- unknown projection primitive,
- unknown transcript parser,
- credential_env entries containing equal signs or obvious values.

- [ ] **Step 4: Run package tests**

Run: env GOCACHE=/Users/benjamin/Documents/pentest/.cache/go-build go test ./internal/runtimeplugin -count=1

Expected: PASS.

## Task 2: Built-In Registry

**Files:**
- Create: internal/runtimeplugin/registry.go
- Create: internal/runtimeplugin/builtin.go
- Modify: internal/runtimeplugin/plugin_test.go

- [ ] **Step 1: Write registry tests**

Add tests:

- BuiltinRegistry contains fake, codex, claude_code, and pi.
- List returns stable sorted plugin IDs.
- Get returns a copy, not mutable shared state.
- Duplicate plugin IDs fail registry construction.

- [ ] **Step 2: Implement Registry**

Implement:

~~~go
type Registry struct {
    plugins map[string]Plugin
    order   []string
}

func NewRegistry(plugins []Plugin) (*Registry, error)
func BuiltinRegistry() (*Registry, error)
func (r *Registry) Get(id string) (Plugin, bool)
func (r *Registry) List() []Plugin
func (r *Registry) Has(id string) bool
~~~

- [ ] **Step 3: Add built-in manifests**

Add built-in manifests for:

- fake: no projection, plain runtime output parser.
- codex: codex_home projection, codex_json parser.
- claude_code: claude_settings projection, claude_stream_json parser, stream JSON launch defaults.
- pi: pi_agent projection, pi_json_session parser.

- [ ] **Step 4: Run registry tests**

Run: env GOCACHE=/Users/benjamin/Documents/pentest/.cache/go-build go test ./internal/runtimeplugin -count=1

Expected: PASS.

## Task 3: Runtime Plugin API

**Files:**
- Modify: internal/daemon/server.go
- Create: internal/daemon/runtime_plugin_handlers.go
- Test: internal/daemon/runtime_plugin_test.go

- [ ] **Step 1: Write API tests**

Add tests:

- GET /api/runtime-plugins returns built-ins without credential values.
- GET /api/runtime-plugins/claude_code returns one plugin.
- GET /api/runtime-plugins/missing returns 404.

- [ ] **Step 2: Add registry to Server**

Add a runtimePlugins field to Server and initialize it in NewServer with runtimeplugin.BuiltinRegistry().

- [ ] **Step 3: Register routes**

Register:

- GET /api/runtime-plugins
- GET /api/runtime-plugins/{plugin_id}

- [ ] **Step 4: Implement handlers**

Handlers return public plugin metadata. Do not include any resolved credential values.

- [ ] **Step 5: Run daemon API tests**

Run: env GOCACHE=/Users/benjamin/Documents/pentest/.cache/go-build go test ./internal/daemon -run TestRuntimePlugin -count=1

Expected: PASS.

## Task 4: Runtime Profile Validation Through Registry

**Files:**
- Modify: internal/runtimeprofile/runtimeprofile.go
- Modify: internal/runtimeprofile/runtimeprofile_test.go
- Modify: internal/daemon/server.go

- [ ] **Step 1: Write validation tests**

Add tests:

- Service accepts plugin IDs supplied by a registry.
- Unknown plugin IDs are rejected.
- Existing built-in provider IDs remain accepted.

- [ ] **Step 2: Replace hardcoded provider map dependency**

Change runtimeprofile.Service so supported provider IDs can be injected. Keep existing constructor behavior by defaulting to built-ins for tests and callers that do not pass a registry.

- [ ] **Step 3: Wire daemon service to registry**

In NewServer, create the plugin registry before runtime profile service construction and pass supported plugin IDs into runtimeprofile.NewService.

- [ ] **Step 4: Run profile tests**

Run: env GOCACHE=/Users/benjamin/Documents/pentest/.cache/go-build go test ./internal/runtimeprofile ./internal/daemon -count=1

Expected: PASS.

## Task 5: Launch Template Rendering

**Files:**
- Create: internal/runtimeplugin/template.go
- Create: internal/runtimeplugin/template_test.go
- Modify: internal/adapters/adapters.go
- Modify: internal/adapters/adapters_test.go

- [ ] **Step 1: Write template tests**

Add tests:

- Claude launch renders binary, model, settings path, MCP args, stream JSON args, custom args, and goal.
- Codex launch renders subcommand, model, custom args, and goal.
- Pi launch renders provider override, model, custom args, and goal.
- Explicit singleton options in custom args suppress manifest defaults.
- Secret-looking values are never introduced by template rendering.

- [ ] **Step 2: Implement RenderLaunch**

Render launch templates into argument arrays. Requirements:

- Replace scalar placeholders.
- Splice list placeholders.
- Omit empty optional values.
- Preserve argument order.
- Avoid shell joining.

- [ ] **Step 3: Update BuildLaunchArgs**

BuildLaunchArgs should look up the plugin manifest and render launch args from the manifest. Keep narrow compatibility fallbacks only where a built-in primitive has not yet moved into the manifest.

- [ ] **Step 4: Run adapter tests**

Run: env GOCACHE=/Users/benjamin/Documents/pentest/.cache/go-build go test ./internal/runtimeplugin ./internal/adapters -count=1

Expected: PASS.

## Task 6: Projection Metadata Bridge

**Files:**
- Modify: internal/runner/projection.go
- Modify: internal/runner/projection_*_test.go
- Modify: internal/runner/runner.go

- [ ] **Step 1: Write projection metadata tests**

Add tests proving each built-in plugin selects the same projection primitive and paths currently used by provider branches.

- [ ] **Step 2: Use plugin metadata for primitive selection**

ProjectRuntimeConfig should read the plugin's config_projection.primitive and dispatch to the existing Go primitive implementation.

- [ ] **Step 3: Use plugin metadata for runtime paths**

LaunchConfigPath, ProcessEnv, and preview config should use manifest paths where possible.

- [ ] **Step 4: Run runner tests**

Run: env GOCACHE=/Users/benjamin/Documents/pentest/.cache/go-build go test ./internal/runner -count=1

Expected: PASS.

## Task 7: Transcript Parser Selection

**Files:**
- Modify: internal/transcript/transcript.go
- Modify: internal/transcript/transcript_test.go

- [ ] **Step 1: Write parser selection tests**

Add tests:

- claude_code selects claude_stream_json.
- codex selects codex_json.
- pi selects pi_json_session.
- unknown parser falls back to runtime_output at validation time, not during task rendering.

- [ ] **Step 2: Route parser names through plugin metadata**

Keep parser implementations in Go. The manifest only names the parser.

- [ ] **Step 3: Run transcript tests**

Run: env GOCACHE=/Users/benjamin/Documents/pentest/.cache/go-build go test ./internal/transcript -count=1

Expected: PASS.

## Task 8: Runtime Profile UI Uses Plugin API

**Files:**
- Modify: web/src/lib/api.ts
- Modify: web/src/pages/RuntimeProfilesPage.tsx
- Rebuild: internal/daemon/webfs/dist

- [ ] **Step 1: Add TypeScript plugin types**

Add RuntimePlugin, RuntimePluginCapabilities, RuntimePluginProfileSchema, and RuntimePluginProfileField interfaces.

- [ ] **Step 2: Load plugin metadata**

RuntimeProfilesPage should load /api/runtime-plugins and use the result to populate provider/plugin choices.

- [ ] **Step 3: Render fields from profile schema**

Keep existing field controls, but show or hide them based on the plugin schema. Built-ins should render the same fields as today.

- [ ] **Step 4: Update generated preview**

Generated preview should use plugin metadata for binary defaults, paths, capabilities, and launch args.

- [ ] **Step 5: Build UI**

Run: cd web && npm run build

Expected: PASS.

- [ ] **Step 6: Rebuild embedded assets**

Run: make build-ui

Expected: PASS and internal/daemon/webfs/dist updates.

## Task 9: Trusted External Manifest Loading

**Files:**
- Modify: internal/runtimeplugin/registry.go
- Test: internal/runtimeplugin/plugin_test.go
- Modify: internal/daemon/server.go

- [ ] **Step 1: Write external loading tests**

Add tests:

- Loading a valid manifest from a configured directory succeeds.
- Invalid JSON fails with a useful registry error.
- Duplicate ID with built-in fails.
- External loading is disabled when no directory is configured.

- [ ] **Step 2: Add loader**

Add LoadDirectory(path string) ([]Plugin, []error). It should read only .json files from the top-level directory.

- [ ] **Step 3: Add daemon config**

Add RuntimePluginDirs []string to daemon Config or equivalent CLI wiring. Leave empty by default.

- [ ] **Step 4: Run runtimeplugin tests**

Run: env GOCACHE=/Users/benjamin/Documents/pentest/.cache/go-build go test ./internal/runtimeplugin -count=1

Expected: PASS.

## Task 10: Full Verification

**Files:**
- All modified files.

- [ ] **Step 1: Run focused Go tests**

Run: env GOCACHE=/Users/benjamin/Documents/pentest/.cache/go-build go test ./internal/runtimeplugin ./internal/runtimeprofile ./internal/adapters ./internal/runner ./internal/transcript ./internal/daemon -count=1

Expected: PASS.

- [ ] **Step 2: Run frontend build**

Run: cd web && npm run build

Expected: PASS.

- [ ] **Step 3: Rebuild embedded UI**

Run: make build-ui

Expected: PASS.

- [ ] **Step 4: Run full Go test suite**

Run: env GOCACHE=/Users/benjamin/Documents/pentest/.cache/go-build go test ./...

Expected: PASS.

- [ ] **Step 5: Review final diff**

Run: git diff --check

Expected: no whitespace errors.

Run: git status --short

Expected: only runtime plugin implementation files and rebuilt assets are dirty before commit.

## Self-Review

- Spec coverage: registry, manifests, built-ins, compatibility, API, UI, projection, launch, transcript, external manifests, security, and verification are covered.
- Placeholder scan: no TBD or fill-in-later steps are present.
- Type consistency: plugin, manifest, registry, primitive, and parser names match the design spec and CONTEXT.md vocabulary.
