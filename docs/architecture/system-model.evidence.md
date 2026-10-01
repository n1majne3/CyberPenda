# CyberPenda System Model — Evidence Index

Current-state model built by the `system-modeler` skill (diagram format via `c4model`).
Scope: repository `CyberPenda` (Go module `pentest`), current state only, as of 2026-09-30.
Diagram sources: `system-context.dsl` (L1) and `system-model.dsl` (L2 container + L3 daemon component).

## Nodes

| ID | Label | Type | Confidence | Source refs |
| --- | --- | --- | --- | --- |
| actor.operator | Operator | actor | high | README.md:23-28 (who it is for), README.md:257-266 (typical workflow) |
| system.cyberpenda | CyberPenda | system | high | README.md:5, go.mod:1 |
| container.spa | React Dashboard | container | high | web/package.json, web/src/App.tsx:225-253, internal/daemon/webfs/webfs.go |
| container.pentestd | pentestd Daemon | container | high | cmd/pentestd/main.go, internal/daemon/server.go:21-43, README.md:42 |
| container.db | SQLite Database | database | high | go.mod:8 (modernc.org/sqlite), internal/store/store.go:1-6 |
| container.runtime | Agent Runtime (Codex/Claude Code/Pi) | container | high | internal/runtime/runtime.go:1-5, internal/runtimeplugin/builtin.go:21,46,105,185 |
| container.bridges | Provider Bridges | container | high | cmd/pentest-provider-bridge/main.go:1-4, cmd/pentest-claude-sdk-bridge/ (main.mjs, bridge.mjs) |
| container.pentestctl | pentestctl CLI | container | high | cmd/pentestctl/main.go, internal/pentestctl/pentestctl.go:1,46, README.md:342-366 |
| system.hostedImage | TSecBench Hosted Image | system | high | docs/adr/0026-package-tsecbench-as-an-isolated-hosted-image.md, docker/tsecbench-hosted/Dockerfile |
| container.hostedController | Hosted Controller | container | high | cmd/pentest-tsecbench-hosted/main.go:12,29, internal/hostedcontroller/controller.go:1-2 |
| container.hostedDaemon | In-process pentestd | container | high | internal/hostedcontroller/controller.go:340 (daemon.NewServer) |
| container.challengeClient | Hosted Challenge Client | container | high | cmd/pentest-tsecbench-client/main.go:1-2,15-16, internal/tsecbenchclient/client.go:1 |
| container.hostedRuntime | Hosted Runtime + tool baseline | container | high | docker/tsecbench-hosted/Dockerfile, docs/adr/0037-offline-knowledge-baseline-in-hosted-image.md, docker/knowledge-baseline/install.sh |
| ext.modelProvider | Model Provider | external-system | high | internal/modelprovider/modelprovider.go:1, README.md:167-190 |
| ext.containerEngine | Container Engine | external-system | high | docs/adr/0025-container-engine-support-matrix.md, docs/platform-engines.md, docker-compose.yaml |
| ext.challengePlatform | Challenge Platform (TSecBench) | external-system | high | README.md:138-160, internal/challengeadapter/adapter.go:15 |
| ext.targetSystems | Authorized Target Systems | external-system | high | README.md:7 (authorization notice), CONTEXT.md (Scope) |
| ext.ghcr | GHCR Image Registry | external-system | medium | README.md:122-124 (image names); pull flow itself is standard registry behavior, not repo code |
| ext.vercelDemo | Vercel Demo Deployment | external-system | high | web/vercel.json, web/package.json (`build:demo`), web/src/demo/, README.md:9 |
| component.httpApi | HTTP API | component | high | internal/daemon/server.go:939-975+, internal/daemon/blackboard_v2_http.go:43-63, internal/daemon/fgs.go:20-23 |
| component.harness | Runtime Harness | component | high | internal/runtime/runtime.go:1-5 |
| component.runnerProjection | Runner + Config Projection | component | high | internal/runner/runner.go:1-3, internal/runner/prepared_projection.go (capture and paired rendering), internal/runner/prepared_projection_test.go (captured inputs and failure behavior), internal/preflight/preflight.go:1-4 |
| component.fgs | FGS Store | component | high | internal/fgs/fgs.go:1-2 |
| component.blackboard | Blackboard v2 Service | component | high | internal/blackboardv2/service.go:1-4 |
| component.domains | Domain Services | component | high | internal/{project,task,session,skill,modelprovider,credential,report,steering,finishreadiness} package docs |
| component.store | Store | component | high | internal/store/store.go:1-6 |

## Edges

| From | To | Type / protocol | Confidence | Source refs |
| --- | --- | --- | --- | --- |
| operator | spa | calls / HTTPS | high | README.md:107-110 |
| spa | pentestd | calls / JSON HTTP /api | high | web/src/lib/api.ts, web/vite.config.ts (/api proxy) |
| pentestd | db | reads+writes / SQL | high | internal/store/store.go, ADR 0017 |
| pentestd | containerEngine | controls / Docker-Podman CLI | high | README.md:60-69, cmd/pentestd/main.go (-container-cli) |
| pentestd | runtime | launches-steers-stops / stdio | high | internal/runtime/runtime.go:1-5, internal/daemon/production_provider_session_factory.go |
| pentestd | bridges | owns / stdio | high | cmd/pentest-provider-bridge/main.go:1-4 |
| bridges | runtime | frames / JSON-RPC, JSONL | high | cmd/pentest-provider-bridge/main.go:142,402 |
| runtime | pentestctl | invokes / CLI | high | README.md:342-366 |
| pentestctl | pentestd | calls / HTTP Continuation Interface | high | README.md:344-363, internal/projectinterface/grant.go:1-4 |
| runtime | modelProvider | calls / HTTPS | high | CONTEXT.md (Model Runtime Projection: no proxy), README.md:167-190 |
| runtime | targetSystems | tests / tool-specific | high | README.md:5,44-45 (tools in sandbox, not proxied) |
| challengePlatform | hostedController | config + transcript / stdout | high | README.md:163-164 (BENCHMARK_BASE_URL, BENCHMARK_TOKEN) |
| hostedController | hostedDaemon | boots / in-process | high | internal/hostedcontroller/controller.go:340 |
| hostedController | challengeClient | exec / one op per process | high | cmd/pentest-tsecbench-client/main.go:1-2 |
| challengeClient | challengePlatform | calls / HTTPS | high | internal/tsecbenchclient/client.go:1, internal/challengeadapter |
| hostedDaemon | hostedRuntime | launches / process | high | internal/hostedcontroller/controller.go:48-56 |
| hostedRuntime | modelProvider | calls / HTTPS via gateway | high | README.md:169-171 (.tsecbench.gw) |
| pentestd -> ghcr | pulls images / HTTPS | medium | README.md:122-124 (image coordinates; pull mechanics not in repo code) |
| cyberpenda -> vercelDemo | demo build / npm build:demo | high | web/package.json, web/vercel.json |

## Retained legacy (current but read-only / retired — excluded from diagrams)

| Element | State | Evidence |
| --- | --- | --- |
| internal/challengeworkflow | Retired; read-only history handlers still served; write routes return HTTP 410 | internal/challengeworkflow/service.go:1, README.md:287-294 |
| internal/workinggraph | Legacy Intent publication/compilation/settlement retired; keeps protected filesystem layout for FGS | internal/workinggraph/working_graph.go:1-2 |
| internal/blackboardmigration | Offline v1 decoder, one-way migration; only consumer is pentestctl | internal/blackboardmigration/service.go:1 |
| Daemon MCP endpoint | Retired: POST /mcp returns 404; go-sdk still in go.mod but no Go file imports it | internal/daemon/mcp_test.go:27-35, internal/daemon/server.go:1904 |
| Task Policy | Retained as historical metadata, not enforced | CONTEXT.md (Task Policy), README.md:290 |

## Assumptions and unknowns

- **GHCR pull path (medium)**: image coordinates are documented; the actual pull is container-engine behavior. Validate with a live `make dev` + sandbox launch if this edge matters.
- **Sandbox as container boundary**: the Sandbox Runner container (Kali image, docker/pentest-sandbox/Dockerfile) hosts the Runtime, bridges, pentestctl, and tool baseline at runtime. It is drawn as descriptions on those nodes, not as a C4 container, because it is a deployment environment. Route to `deployment-topology-analyzer` for a runtime topology view.
- **Provider session wire details** (Codex app-server, Pi RPC, Claude Agent SDK framing) are implemented in internal/runtime provider assemblers and the bridges; not modeled below container level.
- **CyberGym scripts** (scripts/cybergym-download-level1-30.py) are dataset tooling, not product architecture; excluded.
- **Vercel demo** is a static build of the same web source with fixture data (web/src/demo/); it shares no backend with the daemon.
