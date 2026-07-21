# Keep persistence inline in domain services

Domain services (`project`, `modelprovider`, `credential`, `skill`) keep their SQL, JSON encoding, and row scanning inline rather than behind a repository interface or a separate persistence type. The Service↔transport seam already exists — HTTP, MCP, and CLI handlers call Services and never touch SQLite — and Services are already testable against a real database (`store.Open` with a temp file, or `store.Open("")` for an isolated in-memory database). SQLite is the only backend, and the Blackboard's distinct store epochs are still all SQLite, so a repository interface would be a single-implementation seam that adds indirection without a second adapter to justify it.

## Consequences

- Business rules and persistence live together in each Service; this is accepted for readability cost, not treated as a defect.
- Introducing a persistence interface is deferred until a genuine second backing implementation exists (for example a non-SQLite store, a caching layer, or a remote store). One implementation is a hypothetical seam; two make it real.
- Architecture reviews should not re-suggest a generic "persistence seam / repository interface" refactor while SQLite remains the only backend.
- If the inline SQL repetition becomes a maintenance burden, the preferred first step is a concrete per-aggregate persistence helper (a struct, not an interface), which improves locality without adding a hypothetical seam.
