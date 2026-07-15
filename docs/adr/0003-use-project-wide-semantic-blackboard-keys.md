# Use project-wide semantic Blackboard keys

Every Blackboard record uses one human-readable key that is unique across its Project, and Runtime snapshots and relationships refer to that key instead of database IDs or `(node type, key)` pairs. Keys do not embed internal Project, Task, Continuation, Runtime, generated-ID, or hash values; external identifiers remain valid when they are part of the record's domain meaning. This makes Runtime context smaller and unambiguous at the cost of migrating type-scoped key collisions and opaque legacy keys.

New Blackboard Keys are limited to 96 ASCII characters.

Fact- and Finding-specific merge/alias models are replaced by one same-type Project Knowledge `Record Merge`. It atomically rewrites relationships, moves the source into Semantic History, and creates a project-local Blackboard Key Redirect to the canonical key. Current Work is superseded or concluded rather than merged, redirects never enter Runtime snapshots, and v1 opaque-key migration does not create a long-lived compatibility redirect.
