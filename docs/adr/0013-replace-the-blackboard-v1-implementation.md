# Replace the Blackboard v1 implementation

Blackboard v2 is a clean replacement of the implemented graph-v1 subsystem rather than an in-place refactor. The new design rejects v1's graph event ledger, audit-heavy records, copied Goal nodes, Observation/Hypothesis/Project Directive types, compatibility projections, and Runtime protocol, so adapting that 56K-line implementation would preserve the complexity being removed. We retain shared Project, Task, Scope, Evidence-file, Report, Runner, and trusted-MCP integration seams, then replace Blackboard-specific storage, services, protocols, UI, migration, and tests through vertical TDD slices. V1 and v2 do not become long-lived dual read/write paths or a compatibility layer; cutover follows ADR 0007.

The rewrite starts with v2 behavioral tests at public seams. Each slice removes the displaced v1 behavior only after its failing v2 acceptance test exists, keeping the repository buildable while avoiding architecture-by-compatibility.
