# Skills Management Implementation Plan

> For agentic workers: Use TDD. Execute this plan as vertical slices: write one behavior test, make it pass with the smallest implementation, then continue. Do not write all tests first.

**Goal:** Add global Skills management: users can upload/edit canonical Skill bundles, import package-backed Skills through a controlled importer, default-enable Skills for Runtime Profiles with per-profile opt-out, and project enabled Skills into task-local runtime boundaries.

**Architecture:** Add an internal skill domain service backed by SQLite metadata and daemon-managed file bundles. Keep Skills as the global, runtime-agnostic subtype of Runtime Extensions; keep runtime-specific plugins/packages on the existing runtime-specific extension path. Projection resolves enabled Skills from the Skill library and profile opt-outs, materializes them into a task-local skills root, then exposes that root through provider-specific skill discovery paths.

**Decision sources:** Follow CONTEXT.md and docs/adr/0001-global-default-on-skills.md.

**Tech Stack:** Go services and HTTP handlers, SQLite metadata, filesystem bundle storage, existing runner projection and preflight services, React + TypeScript UI, existing Vite build and Go test setup.

---

## File Structure

- Create internal/skill/skill.go: domain types for Skill, Skill ID, provenance, validation result, opt-out.
- Create internal/skill/service.go: SQLite-backed service for list/get/publish/delete/opt-out resolution.
- Create internal/skill/validation.go: bundle validation and path-safety checks.
- Create internal/skill/importer.go: controlled importer interface and package-ref request model.
- Create internal/skill/skill_test.go: domain/service/validation behavior tests.
- Modify internal/store/store.go: add skills and skill_profile_opt_outs tables.
- Modify internal/daemon/server.go: initialize skill service and register Skills routes.
- Create internal/daemon/skill_handlers.go: global Skills API handlers.
- Create internal/daemon/skill_test.go: HTTP behavior tests for publish, list, import, opt-out, delete.
- Modify internal/preflight/preflight.go: include enabled Skill resolution in preflight checks.
- Modify internal/preflight/preflight_test.go: preflight behavior tests for enabled Skill previews while credential checks stay profile/request-owned.
- Modify internal/runner/runner.go: add a task-local Skills root to task layout.
- Modify internal/runner/sandbox_skills.go: expose task-local Skills root instead of image-baked global skills.
- Modify internal/runner/projection.go: materialize enabled Skill bundles during runtime config projection.
- Create or modify internal/runner/skill_projection_test.go: task-local projection behavior tests.
- Modify internal/daemon/task_handlers.go and/or preflight handlers: include Skill Preflight Preview details.
- Modify web/src/lib/api.ts: add Skill API types.
- Create web/src/pages/SkillsPage.tsx: global Skills page.
- Modify web/src/App.tsx: add /skills route and sidebar entry.
- Modify web/src/pages/TaskLaunchPage.tsx: show Skill Preflight Preview.
- Modify web/src/pages/RuntimeProfilesPage.tsx: show default-on Skills and per-profile opt-outs if needed.
- Add or modify relevant web/src/pages/*.test.tsx tests.
- Rebuild internal/daemon/webfs/dist through make build-ui.

## Task 1: Skill Domain Validation Tracer Bullet

**Files:**
- Create: internal/skill/skill.go
- Create: internal/skill/validation.go
- Create: internal/skill/skill_test.go

- [ ] Step 1: Write one failing validation test

Behavior: a valid Skill bundle with a stable Skill ID and instruction document validates, while the validator rejects symlinks and path escape.

Run:

~~~sh
rtk env GOCACHE=/Users/benjamin/Documents/pentest/.cache/go-build go test ./internal/skill -run TestValidateSkillBundle -count=1
~~~

Expected: FAIL because internal/skill does not exist.

- [ ] Step 2: Implement minimal domain types and validator

Implement only enough for valid Skill ID, instruction document existence, path containment, and symlink rejection.

- [ ] Step 3: Run the validation test

Expected: PASS.

## Task 2: Skill Publication Is Atomic

**Files:**
- Modify: internal/store/store.go
- Modify: internal/skill/service.go
- Modify: internal/skill/skill_test.go

- [ ] Step 1: Write failing service test

Behavior: publishing a new Skill stores metadata and bundle content; a later failed publish for the same Skill ID leaves the live bundle unchanged.

Run:

~~~sh
rtk env GOCACHE=/Users/benjamin/Documents/pentest/.cache/go-build go test ./internal/skill -run TestPublishSkillIsAtomic -count=1
~~~

Expected: FAIL because the service/storage does not exist.

- [ ] Step 2: Add persistence and atomic publish

Add tables:

- skills: id, name, description, source_provenance_json, created_at, updated_at.
- skill_profile_opt_outs: profile_id, skill_id, created_at, unique profile_id plus skill_id.

Store bundle files under a daemon-managed library root outside host runtime homes. Publish through a staging directory, validate, then rename/swap into the live bundle path.

- [ ] Step 3: Run skill package tests

Expected: PASS.

## Task 3: Skill API Publish/List/Get

**Files:**
- Modify: internal/daemon/server.go
- Create: internal/daemon/skill_handlers.go
- Create: internal/daemon/skill_test.go
- Modify later: web/src/lib/api.ts

- [ ] Step 1: Write failing HTTP tracer test

Behavior: PUT /api/skills/{skill_id} publishes a Skill from structured metadata and file content; GET /api/skills lists it.

Run:

~~~sh
rtk env GOCACHE=/Users/benjamin/Documents/pentest/.cache/go-build go test ./internal/daemon -run TestSkillsPublishAndList -count=1
~~~

Expected: FAIL with missing route.

- [ ] Step 2: Add routes and handlers

Add routes:

- GET /api/skills
- GET /api/skills/{skill_id}
- PUT /api/skills/{skill_id}

For the first implementation, accept JSON file maps for tests and UI integration. Multipart upload can be added after the behavior path is proven.

- [ ] Step 3: Run daemon skill API test

Expected: PASS.

## Task 4: Default Skill Enablement and Profile Opt-Out

**Files:**
- Modify: internal/skill/service.go
- Modify: internal/skill/skill_test.go
- Modify: internal/daemon/skill_handlers.go
- Modify: internal/daemon/skill_test.go

- [ ] Step 1: Write failing service test

Behavior: a newly published Skill is enabled for existing Runtime Profiles by default; a profile opt-out disables it; updating the same Skill ID preserves the opt-out.

Run:

~~~sh
rtk env GOCACHE=/Users/benjamin/Documents/pentest/.cache/go-build go test ./internal/skill -run TestDefaultEnablementAndOptOut -count=1
~~~

Expected: FAIL.

- [ ] Step 2: Implement opt-out resolution

Do not enumerate default enablements into every profile. Resolve enabled Skills as all live Skills minus profile-specific Skill Opt-Out rows. Runtime-specific extensions remain outside this default-on mechanism.

- [ ] Step 3: Add HTTP opt-out route and test

Add:

- PUT /api/skills/{skill_id}/profiles/{profile_id}/opt-out
- DELETE /api/skills/{skill_id}/profiles/{profile_id}/opt-out

Expected behavior: Skills Page can modify profile opt-outs, but Runtime Profile remains the owner of the effective enablement state.

## Task 5: Skill Deletion and Delete-And-Disable

**Files:**
- Modify: internal/skill/service.go
- Modify: internal/skill/skill_test.go
- Modify: internal/daemon/skill_handlers.go
- Modify: internal/daemon/skill_test.go

- [ ] Step 1: Write failing deletion behavior test

Behavior: deleting a Skill is blocked while effective enablement exists; force_disable=true deletes the Skill and clears opt-out rows. Re-importing the same Skill ID follows Default Skill Enablement and does not restore old opt-outs.

Run:

~~~sh
rtk env GOCACHE=/Users/benjamin/Documents/pentest/.cache/go-build go test ./internal/skill -run TestSkillDeletionLifecycle -count=1
~~~

Expected: FAIL.

- [ ] Step 2: Implement guarded deletion

Deletion must not leave dangling profile references or stale opt-outs for a deleted Skill ID.

- [ ] Step 3: Add HTTP delete tests

Add DELETE /api/skills/{skill_id} with optional force_disable=true.

## Task 6: Skill Audit Events

**Files:**
- Modify: internal/daemon/skill_handlers.go
- Modify: internal/daemon/skill_test.go
- Possibly add global audit helper if needed.

- [ ] Step 1: Write failing audit test

Behavior: publish, edit, import, delete, and opt-out changes record Skill Audit Events in audit_logs with kind values like skill_published, skill_deleted, and skill_opt_out_changed.

Run:

~~~sh
rtk env GOCACHE=/Users/benjamin/Documents/pentest/.cache/go-build go test ./internal/daemon -run TestSkillManagementWritesAuditLog -count=1
~~~

Expected: FAIL.

- [ ] Step 2: Implement audit recording

Use existing audit storage. For global Skill events, use an empty project_id unless a stronger global-audit convention already exists by implementation time.

## Task 7: Controlled Skill Import

**Files:**
- Modify: internal/skill/importer.go
- Modify: internal/skill/service.go
- Modify: internal/daemon/skill_handlers.go
- Modify: internal/daemon/skill_test.go

- [ ] Step 1: Write failing controlled import test

Behavior: POST /api/skills/import accepts a structured package/ref request, uses an injected importer, publishes the returned bundle, and rejects raw command strings.

Run:

~~~sh
rtk env GOCACHE=/Users/benjamin/Documents/pentest/.cache/go-build go test ./internal/daemon -run TestControlledSkillImportPublishesBundle -count=1
~~~

Expected: FAIL.

- [ ] Step 2: Add importer interface and handler

Define an importer interface so tests can use a fake importer. Production command execution should be fixed-shape and structured, not arbitrary shell from the request.

- [ ] Step 3: Preserve source provenance

Record source kind, package/ref, source URL when present, last imported timestamp, and local modification status.

## Task 8: Task-Local Skill Projection

**Files:**
- Modify: internal/runner/runner.go
- Modify: internal/runner/sandbox_skills.go
- Modify: internal/runner/projection.go
- Create: internal/runner/skill_projection_test.go

- [ ] Step 1: Write failing projection test

Behavior: runtime config projection materializes enabled Skills into a task-local Skills root and links the selected runtime's skill discovery path to that root.

Run:

~~~sh
rtk env GOCACHE=/Users/benjamin/Documents/pentest/.cache/go-build go test ./internal/runner -run TestProjectRuntimeConfigProjectsEnabledSkills -count=1
~~~

Expected: FAIL.

- [ ] Step 2: Add Skills root to task layout

Extend task layout with SkillsRoot under the task root. In sandbox execution this should be visible through the existing task-root bind mount.

- [ ] Step 3: Replace image-baked-only skill links

Current PrepareSandboxSkills links to /opt/pentest/skills. Change user-managed Skills so they are materialized per task and linked into:

- workdir .agents/skills,
- provider home skills for Codex/Claude,
- Pi agent skills.

Keep compatibility with image-baked skills only if needed as a fallback; user-managed Skills should be task-local.

## Task 9: Skill Preflight Preview

**Files:**
- Modify: internal/preflight/preflight.go
- Modify: internal/preflight/preflight_test.go
- Modify: internal/daemon/preflight_test.go

- [ ] Step 1: Write failing preflight test

Behavior: preflight lists enabled Skills while credential checks stay owned by Runtime Profiles and launch requests.

Run:

~~~sh
rtk env GOCACHE=/Users/benjamin/Documents/pentest/.cache/go-build go test ./internal/preflight -run TestRunListsEnabledSkillsWithoutAddingCredentialRequirements -count=1
~~~

Expected: FAIL.

- [ ] Step 2: Add skill resolver to preflight

Add a small interface to avoid coupling preflight to the full skill service. Preflight should produce a skills check and include enough detail for the UI preview.

- [ ] Step 3: Add daemon preflight HTTP coverage

Ensure /api/projects/{id}/preflight returns skill preview data for Task launch.

## Task 10: Skills Page UI

**Files:**
- Modify: web/src/lib/api.ts
- Create: web/src/pages/SkillsPage.tsx
- Create: web/src/pages/SkillsPage.test.tsx
- Modify: web/src/App.tsx

- [ ] Step 1: Write failing UI test

Behavior: the global sidebar contains Skills, and the Skills page lists Skills with source provenance and profile opt-out controls.

Run:

~~~sh
rtk npm --prefix web test -- --run SkillsPage
~~~

Expected: FAIL.

- [ ] Step 2: Add API types and route

Add /skills route under global Settings navigation. Do not add Skills to ProjectNav.

- [ ] Step 3: Build page capabilities

Implement list Skills, manual publish path, controlled import form, edit instruction/metadata, profile opt-out toggles, delete and delete-and-disable flow, validation and warning display.

## Task 11: Task Launch Skill Preview UI

**Files:**
- Modify: web/src/pages/TaskLaunchPage.tsx
- Modify: web/src/lib/api.ts
- Add or modify page tests.

- [ ] Step 1: Write failing Task launch UI test

Behavior: after preflight, Task launch shows enabled Skill IDs and skill-related blockers.

Run:

~~~sh
rtk npm --prefix web test -- --run TaskLaunchPage
~~~

Expected: FAIL.

- [ ] Step 2: Render Skill Preflight Preview

Show enabled Skills, missing/deleted Skill errors, and pass/fail status.

## Task 12: Full Verification

- [ ] Run Go tests

~~~sh
rtk env GOCACHE=/Users/benjamin/Documents/pentest/.cache/go-build go test ./...
~~~

- [ ] Run web tests

~~~sh
rtk npm --prefix web test -- --run
~~~

- [ ] Build UI

~~~sh
rtk make build-ui
~~~

- [ ] Run final status check

~~~sh
rtk git status --short
~~~

Expected: all relevant tests pass, UI bundle is rebuilt, and only intended files are modified.
