# CyberPenda

CyberPenda is a **local-first pentest agent** for coordinating **authorized** security testing inside a scoped project.

It combines a Go daemon, React dashboard, sandboxed agent runtimes (Codex, Claude Code, Pi), project scope controls, a semantic Blackboard v2, skills and runtime extensions, and Markdown report generation.

The daemon is the control plane, memory plane, task lifecycle plane, and reporting plane. Pentest tools run inside the selected runtime environment — not as a tool proxy through the daemon.

> **Use only against systems you are authorized to test.** Scope, approvals, and host-runner activation are first-class product concepts for a reason.

## Architecture

| Component | Role |
| --- | --- |
| `pentestd` | Local HTTP daemon: SQLite store, task harness, MCP server, embedded UI |
| React dashboard | Project dashboard, launch controls, blackboard, findings, settings |
| Sandbox runner | Default runner — isolates runtime home, workdir, and process env (Docker/Podman) |
| Host runner | Explicit opt-in; never an automatic fallback from sandbox |
| Trusted MCP (`/mcp`) | Exactly seven Blackboard v2 semantic tools bound to a trusted Continuation |
| `pentestctl` | CLI access to the same Blackboard v2 semantic operations |
| Runtime plugins | Declarative adapters (Codex, Claude Code, Pi, fake) |
| Skills / extensions | Runtime-agnostic skill bundles + runtime-specific extension packs |

Data lives on the machine by default: SQLite (`pentest.db`), task run directories, and managed artifact roots.

## Quick start

### Prerequisites

- Go (see `go.mod`)
- Node.js 20+ (UI build / `make dev`)
- A Linux container engine for the **Sandbox Runner**:
  - **macOS (default here):** [OrbStack](https://orbstack.dev/) with the Docker-compatible CLI
  - **Linux:** Docker Engine or Podman
  - **Windows:** native `pentestd` + Docker Desktop / Podman Desktop (Linux containers in the Desktop WSL machine)

See [ADR 0025](docs/adr/0025-container-engine-support-matrix.md) and
[docs/platform-engines.md](docs/platform-engines.md) for the engine matrix
(OrbStack, Podman, Windows native daemon + Desktop WSL).

### Local development

```sh
# Backend on :8787 + Vite UI with /api proxy
make dev
```

Open the Vite URL printed by the frontend (API and health proxy to `http://127.0.0.1:8787`).

### Build a self-contained daemon

```sh
make build      # builds UI into the local embed path, then pentestd
./pentestd
```

The React build under `internal/daemon/webfs/dist` is **not** committed. Docker and `make build` regenerate it. A tracked `dist/.gitkeep` only keeps `//go:embed` valid for bare Go tests.

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

Compose mounts Docker socket so the app container can launch sandbox task containers. Task data uses the named `cyberpenda-data` volume, and child sandboxes mount each task subpath from that same volume. Set `PENTEST_AUTH_TOKEN` before starting; non-loopback binds require auth. Override the volume name with `CYBERPENDA_DATA_VOLUME` when needed.

### Sandbox image (from source)

```sh
make build-sandbox-image   # tags ghcr.io/n1majne3/cyberpenda-sandbox:latest by default

# Opt into a local development tag.
SANDBOX_IMAGE=pentest-sandbox:dev make build-sandbox-image
```

Override the source-build tag with `SANDBOX_IMAGE=...`. The source-smoke Make targets use that tag; direct daemon and script invocations use the published GHCR image unless `PENTEST_SANDBOX_IMAGE=...` is set.

## Typical workflow

1. Create a **Project** and define **Scope** (what is authorized).
2. Configure global **Model Providers** and API key env vars.
3. Optionally configure **Runtime Profile Presets**, credentials, MCP, and **Skills**.
4. Launch a **Task** with a natural-language goal via **Launch Selection** (runtime + model provider + model) or an advanced preset.
5. Default path uses the **Sandbox Runner**; steer / continue the same task as work progresses.
6. Runtimes record durable semantic Blackboard knowledge and retain evidence through trusted interfaces (MCP / CLI).
7. Generate a **Markdown report** from the semantic Blackboard snapshot.

Domain terms are defined in [CONTEXT.md](CONTEXT.md).

## Make targets

| Target | Description |
| --- | --- |
| `make dev` | Daemon + Vite frontend for local development |
| `make build-ui` | Build React UI into the local (gitignored) embed path |
| `make build` | `build-ui` + compile `pentestd` with embedded UI |
| `make build-sandbox-image` | Build local sandbox container image |
| `make test` / `make test-backend` | Go unit and integration tests |
| `make test-ci` | CI-safe tests (no Docker, no LLM credentials) |
| `make smoke-sandbox-mcp` | Live smoke: sandbox → daemon Blackboard v2 MCP change |
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

Sandbox network notes:

- Default bridge works with OrbStack, Docker, and rootful Podman.
- Opt-in **Sandbox VPN TUN** (`run_controls.sandbox_vpn_tun`) mounts `/dev/net/tun` and grants `NET_ADMIN` for OpenVPN. It cannot combine with `host_proxy_only`, and **rootless Podman fails preflight** for that option.
- On Windows the daemon runs **natively on Windows**; only sandbox containers run in the Docker/Podman Desktop **WSL/Linux VM**. Task bind mounts use `--mount type=bind` with Windows paths normalized to `C:/...` form so drive letters do not break mount parsing. Share the runtime-root drive in Desktop File Sharing if create fails on mounts.
| `-task-volume` | `PENTEST_TASK_VOLUME` | (empty; Compose sets the named data volume) |
| `-task-volume-root` | `PENTEST_TASK_VOLUME_ROOT` | `/data` when `-task-volume` is set |
| `-auth-token` | `PENTEST_AUTH_TOKEN` | (required for non-loopback binds) |
| `-runtime-plugin-dirs` | `PENTEST_RUNTIME_PLUGIN_DIRS` | trusted plugin dirs |
| `-runtime-extension-dirs` | `PENTEST_RUNTIME_EXTENSION_DIRS` | trusted extension dirs |

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
- [Blackboard v2 specification](docs/specs/blackboard-v2-spec.md) — semantic records, trusted tools, and public routes
- [ADRs](docs/adr/) — architecture decisions (skills default-on, model providers vs profiles)

## License / authorization

CyberPenda is intended for **authorized** security testing only. Operators are responsible for lawful scope, credentials, and engagement rules. Do not use this software against systems without permission.
