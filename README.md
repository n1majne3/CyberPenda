# CyberPenda

CyberPenda is a **local-first pentest agent** for coordinating **authorized** security testing inside a scoped project.

It combines a Go daemon, React dashboard, sandboxed agent runtimes (Codex, Claude Code, Pi), project scope controls, a project blackboard (facts / findings / evidence), skills and runtime extensions, and Markdown report generation.

The daemon is the control plane, memory plane, task lifecycle plane, and reporting plane. Pentest tools run inside the selected runtime environment — not as a tool proxy through the daemon.

> **Use only against systems you are authorized to test.** Scope, approvals, and host-runner activation are first-class product concepts for a reason.

## Architecture

| Component | Role |
| --- | --- |
| `pentestd` | Local HTTP daemon: SQLite store, task harness, MCP server, embedded UI |
| React dashboard | Project dashboard, launch controls, blackboard, findings, settings |
| Sandbox runner | Default runner — isolates runtime home, workdir, and process env (Docker/Podman) |
| Host runner | Explicit opt-in; never an automatic fallback from sandbox |
| Trusted MCP (`/mcp`) | Project interface for facts, findings, evidence, task summaries, reports |
| `pentestctl` | CLI fallback for the same trusted project interfaces |
| Runtime plugins | Declarative adapters (Codex, Claude Code, Pi, fake) |
| Skills / extensions | Runtime-agnostic skill bundles + runtime-specific extension packs |

Data lives on the machine by default: SQLite (`pentest.db`), task run directories, and managed artifact roots.

## Quick start

### Prerequisites

- Go (see `go.mod`)
- Node.js 20+ (UI build / `make dev`)
- Docker or Podman (sandbox runner)

### Local development

```sh
# One-time checkout setup: reject stale embedded UI before push.
make install-git-hooks

# Backend on :8787 + Vite UI with /api proxy
make dev
```

Open the Vite URL printed by the frontend (API and health proxy to `http://127.0.0.1:8787`).

### Build a self-contained daemon

```sh
make build-ui   # builds web/ and copies into internal/daemon/webfs/dist
make build      # builds UI then pentestd with embedded assets
./pentestd
```

Default listen address: `http://127.0.0.1:8787`.

### Docker Compose

```sh
export PENTEST_AUTH_TOKEN="$(openssl rand -hex 24)"
docker compose up -d
# Open http://127.0.0.1:8787/?token=<token>
```

Images default to:

- App: `ghcr.io/n1majne3/cyberpenda:latest`
- Sandbox: `ghcr.io/n1majne3/cyberpenda-sandbox:latest`

Compose mounts Docker socket so the app container can launch sandbox task containers. Set `PENTEST_AUTH_TOKEN` before starting; non-loopback binds require auth.

### Sandbox image (from source)

```sh
make build-sandbox-image   # tags pentest-sandbox:latest by default
```

Override with `SANDBOX_IMAGE=...` or `PENTEST_SANDBOX_IMAGE=...`.

## Typical workflow

1. Create a **Project** and define **Scope** (what is authorized).
2. Configure global **Model Providers** and API key env vars.
3. Optionally configure **Runtime Profile Presets**, credentials, MCP, and **Skills**.
4. Launch a **Task** with a natural-language goal via **Launch Selection** (runtime + model provider + model) or an advanced preset.
5. Default path uses the **Sandbox Runner**; steer / continue the same task as work progresses.
6. Runtimes write durable **Facts**, **Findings**, and **Evidence** through trusted project interfaces (MCP / CLI).
7. Generate a **Markdown report** from stored project state.

Domain terms are defined in [CONTEXT.md](CONTEXT.md).

## Make targets

| Target | Description |
| --- | --- |
| `make dev` | Daemon + Vite frontend for local development |
| `make build-ui` | Build React UI into the daemon embed path |
| `make check-ui-sync` | Rebuild UI and require the committed embed to match |
| `make install-git-hooks` | Enable the repository pre-push checks for this checkout |
| `make build` | `build-ui` + compile `pentestd` |
| `make build-sandbox-image` | Build local sandbox container image |
| `make test` / `make test-backend` | Go unit and integration tests |
| `make test-ci` | CI-safe tests (no Docker, no LLM credentials) |
| `make smoke-sandbox-mcp` | Live smoke: sandbox → daemon MCP fact write |
| `make smoke-runtime-tasks` | Live smoke for Codex / Claude / Pi (needs Docker + provider creds) |
| `make clean` | Remove built UI artifacts and `pentestd` binary |

## Daemon flags and environment

Common `pentestd` options (flags or env):

| Flag | Env | Default |
| --- | --- | --- |
| `-addr` | `PENTEST_LISTEN_ADDR` | `127.0.0.1:8787` |
| `-db` | `PENTEST_DB` | `pentest.db` |
| `-runtime-root` | `PENTEST_RUNTIME_ROOT` | (empty → daemon default) |
| `-sandbox-image` | `PENTEST_SANDBOX_IMAGE` | `pentest-sandbox:latest` |
| `-container-cli` | `PENTEST_CONTAINER_CLI` | `docker` |
| `-auth-token` | `PENTEST_AUTH_TOKEN` | (required for non-loopback binds) |
| `-runtime-plugin-dirs` | `PENTEST_RUNTIME_PLUGIN_DIRS` | trusted plugin dirs |
| `-runtime-extension-dirs` | `PENTEST_RUNTIME_EXTENSION_DIRS` | trusted extension dirs |
| `-blackboard-write-waiver-operator` | `PENTEST_BLACKBOARD_WRITE_WAIVER_OPERATOR` | (empty) |
| `-blackboard-write-waiver-reason` | `PENTEST_BLACKBOARD_WRITE_WAIVER_REASON` | (empty) |

Auth (when configured): `Authorization: Bearer <token>` or `?token=` on API/MCP routes.

## CLI fallback (`pentestctl`)

Blackboard v2 exposes the same closed semantic requests in offline Store mode
or through the daemon with `--api` and `--token`:

```sh
pentestctl --db pentest.db blackboard change --project <id> --actor-id <actor> --input change.json
pentestctl --db pentest.db blackboard read --project <id> --actor-id <actor> --key entity:example
pentestctl --db pentest.db blackboard history --project <id> --actor-id <actor> --key entity:example --limit 20
pentestctl blackboard evidence retain --input evidence.json
pentestctl blackboard attempt checkpoint --input checkpoint.json
pentestctl blackboard continuation finish --input finish.json
```

`--input -` reads one UTF-8 JSON request from stdin. Operator Project and
actor selection stay in flags, outside semantic JSON. Evidence retention,
Attempt checkpoint, and Continuation Finish require task context from the
`PENTEST_PROJECT_ID`, `PENTEST_TASK_ID`, and `PENTEST_CONTINUATION_ID`
environment. Daemon-backed task calls additionally use
`PENTEST_INTERFACE_TOKEN`; credentials are sent only in the Authorization
header. Run `pentestctl blackboard --help` for the compact command catalog.

## Project layout

```
cmd/pentestd/          Daemon entrypoint
cmd/pentestctl/        CLI entrypoint
internal/              Domain services, adapters, daemon HTTP, runner, store
web/                   React + Vite dashboard
docker/                Daemon and sandbox Dockerfiles
skills/bundles/        Built-in skill content
runtime-extensions/    Runtime-specific extension packs
docs/                  Product docs and ADRs
scripts/               Release builds and live smokes
```

## Documentation

- [Product docs index](docs/README.md) — PRD, MVP scope, implementation plan
- [Domain glossary](CONTEXT.md) — shared product language
- [Graph Blackboard compatibility retirement](docs/blackboard-graph-migration.md) — Release C gates, replacements, and stable 410 guidance
- [ADRs](docs/adr/) — architecture decisions (skills default-on, model providers vs profiles)

## License / authorization

CyberPenda is intended for **authorized** security testing only. Operators are responsible for lawful scope, credentials, and engagement rules. Do not use this software against systems without permission.
