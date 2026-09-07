# CyberPenda Docs

CyberPenda coordinates authorized security testing in a local Project. Current
Projects and Sessions use Goal, Step, and Fact (FGS) for enabled Blackboard work.
Disabled Blackboard Mode remains available. Historical Blackboard records and
Evidence files remain stored without conversion to FGS nodes.

## Current product

- [Quick start and workflow](../README.md): build, configure, launch, and inspect results.
- [Domain glossary](../CONTEXT.md): canonical terms and definitions only.
- [Current product boundary](./product/current-boundary.md): supported behavior, retired features, and links to detailed constraints.
- [FGS Runtime protocol](./specs/fgs-runtime-outbox-protocol.md): confirmed principles, protocol design, and implementation status.
- [FGS decision](./adr/0035-use-fgs-as-the-blackboard-working-model.md): model replacement and historical-data boundary.
- [Platform engines](./platform-engines.md): Sandbox Runner support.
- [TSecBench Hosted delivery](./tsecbench/README.md): image, configuration, and acceptance.

The Runtime publishes FGS updates through its Outbox. The Harness validates and
stores accepted updates and Receipts. The UI reads accepted graph state and can
export Markdown. CyberPenda does not expose a built-in Blackboard MCP server.
External MCP servers remain explicit Runtime Profile configuration.

Runtime extensions can be selected from the local registry or entered as explicit
references. Skills retain their managed import flow. The product does not fetch
or browse remote plugin catalogs.

Normal Project Challenge Workflow is retired. Tasks with retained Attempts or
Operations show read-only history. Pending operations are not replayed and need
review on the original Platform. The separate Hosted Challenge Client remains
active.

## Historical references

These records explain prior decisions. Do not use them as new-feature or current
launch instructions.

- [Retired Challenge Project Workflow](./specs/challenge-project-workflow/design.md).
- [Original PRD](./product/prd.md), [MVP scope](./product/mvp.md), and [implementation plan](./product/implementation-plan.md).
- [Legacy Blackboard v2 specification](./specs/blackboard-v2-spec.md) and [replacement plan](./specs/blackboard-v2-tdd-plan.md).
- [Original design](./superpowers/specs/2026-06-17-pentest-agent-design.md).
- [Architecture decisions](./adr/): check each document's status and later decisions.
- [Pre-compression domain snapshot](./history/2026-09-08-context-before-compression.md): prior definitions, relationships, and resolved questions; mixed current and superseded material for decision tracing only.

The Assisted-versus-Interactive experiment launchers are retired. Their saved
results remain historical data. Use `make smoke-sandbox-fgs`,
`make smoke-runtime-tasks`, and the Hosted acceptance workflow for current checks.
