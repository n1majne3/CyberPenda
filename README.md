# CyberPenda

CyberPenda is a **local-first pentest agent** for coordinating **authorized** security testing inside a scoped project.

It combines a Go daemon, React dashboard, agent runtimes (Codex, Claude Code, Pi, Hermes), project scope controls, a Goal/Step/Fact Blackboard, skills and runtime extensions, and Markdown result export.

The daemon is the control plane, memory plane, task lifecycle plane, and reporting plane. Pentest tools run inside the selected runtime environment — not as a tool proxy through the daemon.

> **Use only against systems you are authorized to test.** Scope, approvals, and host-runner activation are first-class product concepts for a reason.

## Live demo

[Open the CyberPenda read-only demo](https://cyberpenda-demo.vercel.app)

The demo uses the real React dashboard, routes, Project pages, Blackboard,
Findings, Evidence, and Report views from this repository. It uses fixed sample
Project data through a read-only API adapter. It does not run `pentestd`, a
Runtime, Docker or Podman, or security-testing tools. Run Controls and data
changes are disabled.

## Architecture

| Component | Role |
| --- | --- |
| `pentestd` | Local HTTP daemon: SQLite store, Runtime Harness, FGS receiver, embedded UI |
| React dashboard | Project dashboard, launch controls, Blackboard, results, settings |
| Sandbox runner | Default runner — isolates runtime home, workdir, and process env (Docker/Podman) |
| Host runner | Explicit opt-in; never an automatic fallback from sandbox |
| Runtime Outbox | Continuation-scoped FGS updates with durable Receipts |
| `pentestctl` | FGS publication and reads inside a projected Runtime; retained legacy commands |
| Runtime plugins | Declarative adapters (Codex, Claude Code, Pi, Hermes, fake) |
| Skills / extensions | Runtime-agnostic skill bundles + runtime-specific extension packs |

Data lives on the machine by default: SQLite (`pentest.db`), task run directories, and managed artifact roots.

## Quick start

### Prerequisites

- Go (see `go.mod`)
- Node.js 20.19+ or 22.12+ (UI build / `make dev`)
- GNU Make (source builds)
- A Linux container engine for the **Sandbox Runner**:
  - **macOS (default here):** [OrbStack](https://orbstack.dev/) with the Docker-compatible CLI
  - **Linux:** Docker Engine or Podman
  - **Windows:** native `pentestd.exe` + Docker Desktop / Podman Desktop (Linux containers in the Desktop WSL machine)

Native Windows `make build` uses Node, npm, GNU Make, and Go. It does not require Git Bash, WSL, `rsync`, or POSIX coreutils. The Desktop Linux machine is only for the Sandbox Runner; it is not part of the application build.

See [ADR 0025](docs/adr/0025-container-engine-support-matrix.md) and
[docs/platform-engines.md](docs/platform-engines.md) for the engine matrix
(OrbStack, Podman, Windows native daemon + Desktop WSL).

### Local development

```sh
# Backend on :8787 + Vite UI with /api proxy
make dev
```

`make dev` builds the embedded UI before starting the daemon, then starts Vite.
Both ports therefore start with current UI code. Vite updates UI edits immediately;
restart `make dev` to refresh the daemon’s embedded UI.

Open the Vite URL printed by the frontend (API and health proxy to `http://127.0.0.1:8787`).
When `PENTEST_AUTH_TOKEN` is not configured and the daemon binds to loopback,
the local UI obtains an HttpOnly browser session automatically. Direct Blackboard
links also work after a daemon restart. With configured authentication, open the
UI with `?token=...`; it stores the token in session storage and removes it from
the visible URL.

### Build a self-contained daemon

Linux and macOS:

```sh
make build      # builds UI into the local embed path, then pentestd
./pentestd
```

Windows Command Prompt or PowerShell:

```powershell
make build      # builds UI into the local embed path, then pentestd.exe
.\pentestd.exe
```

The React build under `internal/daemon/webfs/dist` is **not** committed. Docker and `make build` regenerate it. A tracked `dist/.gitkeep` only keeps `//go:embed` valid for bare Go tests.

Default listen address: `http://127.0.0.1:8787`. On loopback without a
configured auth token, open this address or a direct Blackboard link. The UI
obtains operator access through a same-origin browser session. Runtime clients
still use their Continuation Interface capability.

### Docker Compose

```sh
export PENTEST_AUTH_TOKEN="$(openssl rand -hex 24)"
docker compose up -d
# Open http://127.0.0.1:8787/?token=<token>
```

Images default to:

- App: `ghcr.io/n1majne3/cyberpenda:latest`
- Sandbox: `ghcr.io/n1majne3/cyberpenda-sandbox:latest`

Compose mounts Docker socket so the app container can launch sandbox task containers. Task data uses the named `cyberpenda-data` volume, and child sandboxes mount each task subpath from that same volume. Set `PENTEST_AUTH_TOKEN` before starting; non-loopback binds require auth. Override the volume name with `CYBERPENDA_DATA_VOLUME` when needed.

### Sandbox image (from source)

```sh
make build-sandbox-image   # tags ghcr.io/n1majne3/cyberpenda-sandbox:latest by default

# Opt into a local development tag.
SANDBOX_IMAGE=pentest-sandbox:dev make build-sandbox-image
```

Override the source-build tag with `SANDBOX_IMAGE=...`. The source-smoke Make targets use that tag; direct daemon and script invocations use the published GHCR image unless `PENTEST_SANDBOX_IMAGE=...` is set.

## TSecBench Hosted evaluation

CyberPenda also builds a self-contained `linux/amd64` image for [TSecBench Hosted Mode](https://tsecbench.zc.tencent.com). The image runs the isolated Hosted Controller (`pentest-tsecbench-hosted`) plus the challenge client and one of the bundled Codex / Claude Code / Pi runtimes. TSecBench supplies the VPN-isolated network, challenge lifecycle, and scorekeeping; the container only solves challenges and emits its JSONL transcript.

### Build the image

The build target always targets `linux/amd64`, regardless of the host architecture:

```sh
make build-tsecbench-hosted-image
```

The default tag is `cyberpenda-tsecbench-hosted:local`. Override it with `TSECBENCH_HOSTED_IMAGE=...`.

After a build you can verify the image and inspect its bundled runtimes:

```sh
make smoke-tsecbench-hosted-image
make tsecbench-hosted-runtime-inventory
```

For the TSecBench upload bundle, run the `Build TSecBench Hosted Bundle` GitHub Actions workflow (or `make build-tsecbench-hosted-bundle TSECBENCH_BUNDLE_VERSION=v1` after exporting the image). The bundle archives the image as one `.tar.gz` Docker file plus its checksum and component inventory. See [docs/tsecbench/README.md](docs/tsecbench/README.md) for upload, environment templates, and VPN-backed local validation.

### Hosted environment variables

TSecBench injects `BENCHMARK_BASE_URL` and the one-use `BENCHMARK_TOKEN` in Hosted Mode. The remaining `CYBERPENDA_*` values are entered on the TSecBench page (secrets are never stored in the repo):

| Variable | Meaning |
| --- | --- |
| `CYBERPENDA_RUNTIME` | `codex` (default), `claude_code`, or `pi` |
| `CYBERPENDA_MODEL_PROTOCOL` | `openai_responses` for Codex; `anthropic_messages` for Claude Code; `openai_chat_completions`, `openai_responses`, or `anthropic_messages` for Pi |
| `CYBERPENDA_MODEL_BASE_URL` | Gateway base URL ending in `.tsecbench.gw`; do not append an operation suffix |
| `CYBERPENDA_MODEL` | Model ID served by the gateway |
| `CYBERPENDA_MODEL_API_KEY` | Dedicated, revocable evaluation model API key |
| `CYBERPENDA_REASONING_EFFORT` | Optional; `low`, `medium`, `high`, `xhigh`, or `max` |
| `CYBERPENDA_TASK_GOAL_APPENDIX` | Optional text appended to the required Hosted Task Goal |
| `CYBERPENDA_AUTO_COMPACT_THRESHOLD` | Optional Claude Code compaction threshold (1-100) |
| `CYBERPENDA_AUTO_COMPACT_WINDOW` | Optional Claude Code compaction window (1-1048576) |
| `CYBERPENDA_MAX_OUTPUT_TOKENS` | Optional max output tokens (1-1048576); DeepSeek hosted runs use `393216` |
| `CYBERPENDA_CONTEXT_WINDOW` | Optional context window in tokens |
| `CYBERPENDA_CHALLENGE_ADAPTER` | Optional challenge adapter id; defaults to `tsecbench` |

## Typical workflow

1. Create a **Project**, choose its **Project Kind**, and define **Scope**.
2. Configure a global **Model Provider** and its API key environment variable.
3. Launch a **Task** with a goal, matching **Task Type**, and **Launch Selection**.
   Choose a **Runtime Profile** only when reusable advanced configuration is needed.
4. Use the **Sandbox Runner** by default. **Host Runner** requires explicit activation.
5. Continue or steer the same Task. Inspect its conversation and Runtime activity.
6. With Blackboard enabled, the Runtime publishes Goal, Step, and Fact updates
   through its Outbox. The Harness records accepted state and Receipts.
7. Inspect the Blackboard and export accepted FGS state as Markdown from Report.

New launch controls offer FGS or Disabled. Tasks default to FGS; Non-Project
Sessions default to Disabled. The enabled wire value remains `working_graph`;
new `interactive` inputs map to it without rewriting historical snapshots.
FGS handles both historical enabled mode values. Legacy Working Graph Intent
publication, compilation, and settlement are retired. Disabled Runtime
Owners receive no Blackboard context or authority. Historical Blackboard records
and Evidence files are preserved without conversion to FGS.

The FGS export describes accepted Goals, Steps, and Facts. It does not assert
CVSS scoring, a verified Finding, or Challenge Platform acceptance.

Skills support managed import. Runtime Profiles support local registry
extensions, explicit extension references, and external MCP configuration.
There is no remote plugin catalog browser or built-in Blackboard MCP server.

Normal Project Challenge Workflow is retired. Tasks with retained Attempts or
Operations show read-only Challenge history. Old write routes return HTTP 410;
`--challenge-platform-config` and `PENTEST_CHALLENGE_PLATFORM_CONFIG` are removed.
Task Policy limits are retained only as historical metadata and are no longer
enforced or shown at launch. Stored states and Evidence remain unchanged;
pending operations are not replayed. Check unfinished work on the original
Platform. These records do not block Task Finish, which does not confirm Platform
completion. Hosted evaluation keeps the separate Hosted Challenge Client.

Domain terms are defined in [CONTEXT.md](CONTEXT.md).

## Make targets

| Target | Description |
| --- | --- |
| `make dev` | Daemon + Vite frontend for local development |
| `make build-ui` | Build React UI into the local (gitignored) embed path |
| `make build` | `build-ui` + compile `pentestd` with embedded UI |
| `make build-sandbox-image` | Build local sandbox container image |
| `make build-tsecbench-hosted-image` | Build the local TSecBench Hosted image (`linux/amd64`) |
| `make smoke-tsecbench-hosted-image` | Run the no-capability smoke test against a built Hosted image |
| `make tsecbench-hosted-runtime-inventory` | Print runtime versions bundled in a built Hosted image |
| `make build-tsecbench-hosted-bundle TSECBENCH_BUNDLE_VERSION=v1` | Export a Hosted upload bundle from a built image |
| `make test` / `make test-backend` | Go unit and integration tests |
| `make test-ci` | CI-safe tests (no Docker, no LLM credentials) |
| `make test-concurrency` | Runtime lifecycle race checks with shuffled order and one/four CPUs |
| `make smoke-sandbox-fgs` | Live smoke: sandbox → Runtime Outbox → accepted FGS |
| `make smoke-runtime-tasks` | Live smoke for Codex / Claude / Pi (needs Docker + provider creds) |
| `make clean` | Remove built UI artifacts and `pentestd` binary |

## Daemon flags and environment

Common `pentestd` options (flags or env):

| Flag | Env | Default |
| --- | --- | --- |
| `-addr` | `PENTEST_LISTEN_ADDR` | `127.0.0.1:8787` |
| `-db` | `PENTEST_DB` | `pentest.db` |
| `-runtime-root` | `PENTEST_RUNTIME_ROOT` | (empty → daemon default) |
| `-sandbox-image` | `PENTEST_SANDBOX_IMAGE` | `ghcr.io/n1majne3/cyberpenda-sandbox:latest` |
| `-container-cli` | `PENTEST_CONTAINER_CLI` | `docker` (or `podman`) |
| `-task-volume` | `PENTEST_TASK_VOLUME` | (empty; Compose sets the named data volume) |
| `-task-volume-root` | `PENTEST_TASK_VOLUME_ROOT` | `/data` when `-task-volume` is set |
| `-auth-token` | `PENTEST_AUTH_TOKEN` | (required for non-loopback binds) |
| `-runtime-plugin-dirs` | `PENTEST_RUNTIME_PLUGIN_DIRS` | trusted plugin dirs |
| `-runtime-extension-dirs` | `PENTEST_RUNTIME_EXTENSION_DIRS` | trusted extension dirs |

Sandbox network notes:

- Default bridge works with OrbStack, Docker, and rootful Podman.
- Opt-in **Sandbox VPN TUN** (`run_controls.sandbox_vpn_tun`) mounts `/dev/net/tun` and grants `NET_ADMIN` for OpenVPN. It cannot combine with `host_proxy_only`, and **rootless Podman fails preflight** for that option.
- On Windows the daemon runs **natively on Windows**; only sandbox containers run in the Docker/Podman Desktop **WSL/Linux VM**. Task bind mounts use `--mount type=bind` with Windows paths normalized to `C:/...` form so drive letters do not break mount parsing. Share the runtime-root drive in Desktop File Sharing if create fails on mounts.

Auth (when configured): `Authorization: Bearer <token>` or `?token=` on API routes.

## Runtime CLI (`pentestctl`)

Inside an enabled Runtime, Config Projection supplies the Owner, Continuation,
API, interface credential, and working-directory environment:

```sh
pentestctl working-graph emit --input update.json
pentestctl working-graph read --limit 100
pentestctl working-graph status
pentestctl working-graph history --key goal:inspect
```

`emit` publishes a structured update to the Outbox. Check its Receipt before
assuming the Blackboard accepted it. `--input -` reads one JSON object from stdin.
Read and history commands use the trusted HTTP interface. Disabled Runtime
Owners cannot use these commands.

The older `blackboard` commands remain for retained legacy interfaces and
history. They are not the FGS write path. `/mcp` is retired and returns 404.

## Project layout

```
cmd/pentestd/          Daemon entrypoint
cmd/pentestctl/        CLI entrypoint
internal/              Domain services, adapters, daemon HTTP, runner, store
web/                   React + Vite dashboard
docker/                Daemon and sandbox Dockerfiles
skills/                Daemon-owned runtime extension library (untracked; built-in sources in internal/skill/builtins/assets)
docs/                  Product docs and ADRs
scripts/               Release builds and live smokes
```

## Documentation

- [Documentation index](docs/README.md) — current workflow and historical references
- [Domain glossary](CONTEXT.md) — shared product language
- [FGS Runtime protocol](docs/specs/fgs-runtime-outbox-protocol.md) — reporting principles, Outbox, and Receipts
- [ADRs](docs/adr/) — architecture decisions (skills default-on, model providers vs profiles)

## License / authorization

CyberPenda is intended for **authorized** security testing only. Operators are responsible for lawful scope, credentials, and engagement rules. Do not use this software against systems without permission.
