# Use eleven Blackboard relationship types

Blackboard v2 uses `about`, `part_of`, `tests`, `produced`, `evidences`, `supports`, `contradicts`, `derived_from`, `depends_on`, `satisfies`, and `supersedes`. Relationships are versioned by source Blackboard Key, type, and target Blackboard Key and have no internal identity in Runtime contracts. `blocks` is removed as the inverse duplicate of `depends_on`, and migration reverses those endpoints. `leads_to` is removed because it is vague and unused in live data; attack-chain sequence is presented through precise current relationships plus an Attack Chain Project Fact and report narrative. `derived_from` remains because semantic lineage cannot always be truthfully replaced by support, production, or supersession. Only `supports`, `contradicts`, and `depends_on` may carry a concise non-redundant reason.

Each relationship type has a closed source-and-target endpoint matrix. Unsupported type combinations are rejected instead of becoming generic graph edges, giving Runtime reasoning, validation, and graph visualization one stable semantic grammar.

## Endpoint matrix

| Relationship | Allowed direction |
| --- | --- |
| `about` | Exploration Objective, Attempt, Project Fact, Finding, Solution, or Evidence Artifact → Entity |
| `part_of` | Entity → Entity, or Exploration Objective → Exploration Objective; each hierarchy is acyclic and has no automatic lifecycle propagation |
| `tests` | Attempt → Exploration Objective, Entity, Project Fact, Finding, or Solution |
| `produced` | Attempt → Entity, Exploration Objective, Project Fact, Finding, Solution, or Evidence Artifact |
| `evidences` | Evidence Artifact → Project Fact, Finding, or Solution |
| `supports` | Project Fact → Project Fact, Finding, or Solution |
| `contradicts` | Project Fact → Project Fact, Finding, or Solution; no automatic lifecycle transition |
| `derived_from` | Exploration Objective → Project Fact, Finding, or Solution; Project Fact → Project Fact or Evidence Artifact; Evidence Artifact → Evidence Artifact |
| `depends_on` | Exploration Objective → prerequisite Exploration Objective; acyclic |
| `satisfies` | Project Fact, Finding, or Solution → Exploration Objective; required for Objective resolution |
| `supersedes` | Replacement → replaced record of the same type for Entity, Exploration Objective, Project Fact, Finding, Solution, or Evidence Artifact; acyclic; at most one current replacement |

All relationship self-links are invalid. The `part_of`, `derived_from`, `depends_on`, `supersedes`, and Project-Fact-to-Project-Fact `supports` subgraphs are independently acyclic. Reciprocal `contradicts` relationships remain valid; no global DAG rule is imposed across different relationship types.
