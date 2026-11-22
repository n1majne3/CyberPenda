# Record decisions from code comments

Status: Accepted

## Context

Code comments recorded important decisions, but nobody reviewed them as decisions. They became stale, and they let shortcuts stay in the code. The `/delete-comment` process (`.claude/skills/delete-comment/SKILL.md`) removes comments. This ADR keeps the decisions that those comments recorded. A lint ratchet now rejects new comments.

## Decisions

### LLM output is not redacted (#288)

- The Redactor masks secrets only on non-content surfaces: launch and lifecycle diagnostics, configuration previews, error text, and daemon logs.
- LLM output is persisted byte-for-byte. This includes assistant messages, reasoning, tool-call content, runtime stdout and stderr, Pi session tail lines, subagent transcript lines, and provider-session notifications.
- The Redactor always applies the shape-based patterns. When it gets known secret values (resolved runtime env values, the operator token), it also masks them by exact match. It ignores values shorter than `minRedactableSecretLen`, so that ordinary text is not changed.
- `persistProviderSessionEvent` is the only entry point for unsolicited provider notifications. It copies a fixed correlation allowlist and selects the current Continuation at receipt time. Raw protocol payload is never persisted.
- Raw reasoning content is durable transcript content. It is preferred over a provider summary, because a third-party summary is not the real reasoning. The summary is the fallback.

### Local security boundaries

- The daemon refuses any request with a non-loopback `Origin` before routing. This stops DNS-rebinding attacks against the loopback daemon (#158).
- Only the SPA fallback is public. Registered service routes require authentication.
- The database file and its WAL and SHM sidecars have mode `0600`, because the database holds literal credential secrets (#160).
- Attachment upload limits bound memory and the operator data that the daemon writes into a task workdir. Multipart parsing spills files to temporary storage.
- Semantic-debt accounting trusts a Blackboard v2 operation only by its exact registered `mcp__pentest__<operation>` identity, never by a display name (#192).

### Windows file-system constraints

- Windows cannot fsync a directory handle (golang/go#47366). After a rename, the directory sync is a Linux and macOS ordering guarantee only.
- On Windows, the exclusive byte-range lock follows the file across a rename. The Evidence publisher writes through the locked handle on all platforms, and releases the lock before it opens a verification handle. The database publisher claim guards the rest of the checkpoint.
- A read that a Windows lock blocks is a temporary state, not an integrity failure. Linux `flock` does not block the I/O of other handles.
- A stale temp file from an interrupted publisher can be read-only (`0400`). The publisher clears that mode before it opens the file.
- Pi session tail state keeps the on-disk path, because a Windows file identity is not a path that you can open. Tails drain in identity-key order, so emission is deterministic.

### Historical data stays readable

- Historical Graph v1 decoding stays in the offline migration package. The daemon, runtime, transport, and CLI paths are v2-only (#126, #127).
- A Runtime Configuration Snapshot keeps its captured Skill IDs. Preflight and Config Projection skip captured IDs that no longer resolve, such as retired Built-in Skills, and Preflight shows the skip in the check detail.
- A Task whose Runtime Profile was deleted stays readable. The captured Task Runtime Configuration is self-contained (#205).
- Task Delete keeps the durable row for Blackboard provenance and historical joins. It advances `updated_at`, so the navigation revision changes.
- The daemon does not inject a built-in Blackboard MCP server. Every configured MCP entry is user-owned configuration.
- A Disabled Blackboard Mode launch has no Mode Skill. It gets only the state-file reminder (#248).

### Model selection fails closed

- A strict Runtime Profile protocol pin never falls back to a different protocol. Resolution and Preflight fail when the provider has no compatible endpoint.
- Pi native cross-provider selection uses only the projected provider set (ADR 0015). A Task without `projected_model_provider_ids` fails closed in the daemon and in the Task Conversation UI.
- `@tintinweb/pi-subagents` matches bare model names loosely (case and `.`/`-` are equal). Only exact `providerID/modelID` selectors from the projected registry select the intended model.

### Steering and turns

- Accepted Steering commits the operator message and the dispatch record atomically before the 202 response (#194, #200). The owner-neutral FIFO queue dispatches it. The Conversation Event is a projection, not the dispatch source of truth.
- After a steer replaces the session, an in-flight assisted-conclusion obligation gets a new Conclusion Dispatch for the replacement Continuation. Historical dispatch identity is not changed (ADR 0021).
- A same-turn steer is Harness control. Its fallback starts a replacement Turn, which is Work (ADR 0018). The successful fallback replaces the cached method-unavailable outcome, so a retry of the same request stays idempotent.
- A same-turn steer does not take ownership of the Turn identity. The Turn stays fenced on the prompt request id that started it.
- Adapters wait on `FirstSignal` of the bridge `Closed` and `Terminated` signals, so explicit cleanup and an unexpected exit both end the Task-scoped run.

### Navigation and history bounds (ADR 0024)

- The Sidebar navigation inlines at most `workspaceNavigationTaskLimit` ordinary Tasks for each Project. Busy-runtime Tasks and the selected Task are added on top of this limit (#201).
- The navigation revision folds the Project and Task `updated_at` maxima (soft-deleted rows included), the busy Task set, and the selected Task. An unchanged refresh returns `changed=false` without the projection.
- Runtime Owner history reads return the newest window first. Older content pages back with a `before` cursor. One oversized item becomes a bounded preview with a detail reference (#202).

### Provider bridge

- `pentest-provider-bridge` keeps stderr for diagnostics. Diagnostics never enter the protocol stream.
- `piWire` translates the CyberPenda JSON-RPC envelope to the Pi `{type,id,...}` RPC protocol. A Pi permission or UI response is a separate frame without its own response; the translator acknowledges the outer request after it writes the frame.
- The Pi RPC stream has no child-agent content. The bridge tails the pi-subagents transcript files and forwards each line as `pi/subagent_output`. It watches every candidate temp directory, because Node and Go select the temp directory from different environment variables.
- At start, the bridge primes each existing transcript to its end, because an earlier bridge lifetime forwarded it. When the provider exits, one final pass drains the last lines. A session id is never used as a glob pattern.
- A line longer than `maxSubagentOutputLineBytes` is dropped, because the daemon bridge scanner (4 MiB) cannot read it and one large frame stops the provider session. `maxSubagentOutputReadBytes` bounds the memory of one pass.
- A provider exit closes daemon stdin forwarding, so the daemon does not wait forever.

### Daemon startup

- `pentestd` seeds the Redactor with the operator token, because the token has no recognizable shape. Bridge and container diagnostics go through the Redactor.
- An explicit `-container-cli` or `PENTEST_CONTAINER_CLI` always wins. `auto` tries `docker`, then `podman`, then falls back to `docker`, so a missing engine shows its error at the first Sandbox use.
- `pentest-tsecbench-client` exits after one bounded platform operation. Its exit never controls the Hosted Controller or the daemon lifecycle.

### Sandbox and Hosted Images

- Kali `nmap` has file capabilities (`cap_net_admin`) outside the Docker default bounding set, so `execve` fails with `EPERM` (#286). The build guard runs the real binary, so a bad future package fails the build, not a task.
- Kali `qemu-user` 11 ships `qemu-<arch>`. The image provides both `qemu-<arch>` and `qemu-<arch>-static`, and the build fails when a pinned emulator is missing.
- `pwntools` and `frida-tools` install with pip because Kali has no package. `cmake` is in the apt layer for the `unicorn` source build. `--ignore-installed` stops pip from removing the apt-owned `python3-websockets`, which has no RECORD file.
- The Sandbox image and the TSecBench Hosted Image share one offline knowledge installer (ADR 0037).
- The Hosted Image enables only the async-capable `@tintinweb/pi-subagents` extension. The unscoped `pi-subagents` package registers a conflicting Agent tool.
- The Dockerfile always injects a fresh web build. It does not use a `dist` from git.
- Repository scans read only git-tracked files, because `runs/` holds local challenge state.

### CI workflow

- `main` uses one concurrency group for each commit and does not cancel runs, so every `main` commit is validated. Pull requests cancel superseded runs.
- `ci-result` is the only branch-protection gate. Every CI job feeds it.
- Every daemon test lands in exactly one CI shard.

### verify-go-moves

`scripts/verify-go-moves.go` proves that package directories hold the same Go declarations as at a base commit. Declarations can move between files of a package. It ignores imports per file, file names, positions, comments, and formatting. Additions, removals, content changes, build tag and directive changes, package name changes, changed grouping, and relocated `init` functions or side-effectful package variables are failures. Use it with package tests:

```sh
go run ./scripts/verify-go-moves.go -base <commit> ./internal/<package>
go run ./scripts/verify-go-moves.go --self-test
```

Exit code 0 is pass, 1 is a verification failure, and 2 is a git, read, or parse error.

### Web regression fixtures

- Web test modules group one behavior and share setup through named support modules. Route controls stay in a component-only module so Fast Refresh does not mix components and setup functions.
- `mockApi` matches routes in insertion order. Put a detail or query route before its broader list route, or the list response can hide the requested detail.
- jsdom does not measure layout. `mockConversationViewport` supplies geometry for history and auto-follow tests. Browser tests separately check measured layout and reading position.
- History replacement tests hold the next owner's response until the old rows are gone. Fake timers remain inside the polling tests; unrelated tests use real timers.
- The Runtime Profile save-feedback test holds its response until the Saving state is asserted. It does not depend on a short response timer.
- Provider endpoint composition keeps destination environment references with its credential binding. Provider-native config import follows ADR 0028; projected Pi selection follows ADR 0015. These are not visual layout changes.

## Consequences

- Code has no comments that record decisions. Read this ADR and the linked ADRs instead.
- New decisions go into a new ADR, not into a comment.
