package blackboardmigration

// NodeType is the legacy graph node vocabulary needed to read migration
// sources. It intentionally lives here so the migration does not depend on
// the retired runtime graph package.
type NodeType string

const (
	NodeTypeGoal                 NodeType = "goal"
	NodeTypeEntity               NodeType = "entity"
	NodeTypeExplorationObjective NodeType = "exploration_objective"
	NodeTypeAttempt              NodeType = "attempt"
	NodeTypeObservation          NodeType = "observation"
	NodeTypeHypothesis           NodeType = "hypothesis"
	NodeTypeProjectFact          NodeType = "project_fact"
	NodeTypeFinding              NodeType = "finding"
	NodeTypeSolution             NodeType = "solution"
	NodeTypeEvidenceArtifact     NodeType = "evidence_artifact"
	NodeTypeProjectDirective     NodeType = "project_directive"
)

// EdgeType is the subset of legacy graph edges used while rebuilding v2.
type EdgeType string

const (
	EdgeTypeProduced  EdgeType = "produced"
	EdgeTypeEvidences EdgeType = "evidences"
	EdgeTypeSupports  EdgeType = "supports"
)

// Disposition is the legacy node lifecycle placement used by migration reads.
type Disposition string

const DispositionMain Disposition = "main"
