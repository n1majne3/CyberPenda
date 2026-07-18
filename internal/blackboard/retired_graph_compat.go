package blackboard

// CompactionPlan remains only as the private field type in the user-owned
// graph_types.go while that concurrent change is in the shared workspace.
// Blackboard v2 has no graph compaction behavior.
type CompactionPlan struct{}

const attemptInterruptionReconcilerActor = "retired-graph-reconciler"

type migrationImportBatch struct{}
