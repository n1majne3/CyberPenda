package blackboard

// GraphMutationSchemaVersion is the mutation batch schema version accepted by
// this binary (graph contract: schema version 1).
const GraphMutationSchemaVersion = 1

// NodeType names the controlled node types (graph contract §5). C02 exercises
// only project_fact; the remaining types are validated in C03+.
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

// nodeTypeOrdinal returns the storage-contract §12.1 ordering ordinal used in
// integrity hashes. Unknown types return -1.
func nodeTypeOrdinal(t NodeType) int {
	switch t {
	case NodeTypeGoal:
		return 0
	case NodeTypeEntity:
		return 1
	case NodeTypeExplorationObjective:
		return 2
	case NodeTypeAttempt:
		return 3
	case NodeTypeObservation:
		return 4
	case NodeTypeHypothesis:
		return 5
	case NodeTypeProjectFact:
		return 6
	case NodeTypeFinding:
		return 7
	case NodeTypeSolution:
		return 8
	case NodeTypeEvidenceArtifact:
		return 9
	case NodeTypeProjectDirective:
		return 10
	}
	return -1
}

// Disposition is the node lifecycle placement (graph contract §3.1).
type Disposition string

const (
	DispositionMain     Disposition = "main"
	DispositionArchived Disposition = "archived"
	DispositionMerged   Disposition = "merged"
)

// Confidence and ScopeStatus are shared with the legacy Fact model
// (facts.go). The graph layer adds ScopeStatusUnknown for graph-native records
// (graph contract §5.7).

// ActorType is the provenance actor classification (graph contract §3.3).
type ActorType string

const (
	ActorTypeRuntime   ActorType = "runtime"
	ActorTypeOperator  ActorType = "operator"
	ActorTypeSystem    ActorType = "system"
	ActorTypeMigration ActorType = "migration"
)

// MutationKind is the storage-contract §6.1 mutation_kind classification.
type MutationKind string

const (
	MutationKindNormal MutationKind = "normal"
)

// OperationKind names a mutation batch operation (graph contract §9).
type OperationKind string

const (
	OpCreateNode     OperationKind = "create_node"
	OpPatchNode      OperationKind = "patch_node"
	OpTransitionNode OperationKind = "transition_node"
	OpPutEdge        OperationKind = "put_edge"
	OpRetireEdge     OperationKind = "retire_edge"
	OpSetDisposition OperationKind = "set_disposition"
	OpMergeNodes     OperationKind = "merge_nodes"
)

// ProjectFactProperties is the closed property set for a project_fact node
// (graph contract §5.7).
type ProjectFactProperties struct {
	Category    string      `json:"category"`
	Summary     string      `json:"summary"`
	Body        string      `json:"body,omitempty"`
	Confidence  Confidence  `json:"confidence"`
	ScopeStatus ScopeStatus `json:"scope_status"`
}

// CreateNodeInput carries the typed properties for a create_node operation.
// ExtraProperties is nil for conforming calls; any key present there is
// rejected as unknown_property under the closed envelope.
type CreateNodeInput struct {
	Properties ProjectFactProperties
	// PropertyMap is the canonical closed property envelope for every graph
	// node type. ProjectFact callers may continue to use Properties; adapters
	// use this representation so one conformance corpus can exercise all types.
	PropertyMap     map[string]any
	ExtraProperties map[string]any
}

type EdgeType string

const (
	EdgeTypeAbout       EdgeType = "about"
	EdgeTypePartOf      EdgeType = "part_of"
	EdgeTypeTests       EdgeType = "tests"
	EdgeTypeProduced    EdgeType = "produced"
	EdgeTypeEvidences   EdgeType = "evidences"
	EdgeTypeSupports    EdgeType = "supports"
	EdgeTypeContradicts EdgeType = "contradicts"
	EdgeTypeDerivedFrom EdgeType = "derived_from"
	EdgeTypeDependsOn   EdgeType = "depends_on"
	EdgeTypeBlocks      EdgeType = "blocks"
	EdgeTypeLeadsTo     EdgeType = "leads_to"
	EdgeTypeSatisfies   EdgeType = "satisfies"
	EdgeTypeSupersedes  EdgeType = "supersedes"
)

type PatchNodeInput struct {
	ExpectedVersion int            `json:"expected_version"`
	Properties      map[string]any `json:"properties"`
}

// TransitionNodeInput carries a lifecycle transition requested through Apply.
// resolved_at remains system-managed and is derived from the batch timestamp.
type TransitionNodeInput struct {
	ExpectedVersion   int    `json:"expected_version"`
	Status            string `json:"status"`
	Summary           string `json:"summary,omitempty"`
	ResolutionSummary string `json:"resolution_summary,omitempty"`
}

type PutEdgeInput struct {
	EdgeType EdgeType `json:"edge_type"`
	From     NodeRef  `json:"from"`
	To       NodeRef  `json:"to"`
	Summary  string   `json:"summary,omitempty"`
}

// NodeRef references a node by id, (node_type, stable_key), or same-batch op_id
// (graph contract §4).
type NodeRef struct {
	ID        string   `json:"id,omitempty"`
	NodeType  NodeType `json:"node_type,omitempty"`
	StableKey string   `json:"stable_key,omitempty"`
	OpID      string   `json:"op_id,omitempty"`
}

// Operation is one mutation batch operation in the closed graph mutation envelope.
type Operation struct {
	OpID       string              `json:"op_id"`
	Kind       OperationKind       `json:"kind"`
	Node       NodeRef             `json:"node"`
	Create     CreateNodeInput     `json:"create,omitempty"`
	Patch      PatchNodeInput      `json:"patch,omitempty"`
	Transition TransitionNodeInput `json:"transition,omitempty"`
	PutEdge    PutEdgeInput        `json:"put_edge,omitempty"`
}

// ExecutionContext is the server-side trusted context bound to a mutation
// batch (storage contract §2, graph contract §3.3). The graph service treats
// these fields as authoritative; caller-supplied Project/provenance is never
// trusted. Runtime Task/Continuation/profile/runner binding is revalidated
// transactionally before Apply accepts a Runtime-authored mutation.
type ExecutionContext struct {
	ProjectID        string    `json:"project_id"`
	ProjectKind      string    `json:"project_kind"`
	ActorType        ActorType `json:"actor_type"`
	ActorID          string    `json:"actor_id"`
	TaskID           string    `json:"task_id,omitempty"`
	ContinuationID   string    `json:"continuation_id,omitempty"`
	RuntimeProfileID string    `json:"runtime_profile_id,omitempty"`
	Runner           string    `json:"runner,omitempty"`
}

// SystemExecutionContext builds a trusted context for a system actor. This is
// the construction path used while the store epoch is legacy_v1 (graph data is
// exercised only in tests and migration transactions). Runtime-bound contexts
// are constructed by the project-interface module.
func SystemExecutionContext(projectID, projectKind, systemActorID string) ExecutionContext {
	return ExecutionContext{
		ProjectID:   projectID,
		ProjectKind: projectKind,
		ActorType:   ActorTypeSystem,
		ActorID:     systemActorID,
	}
}

// idempotencyScope derives the idempotency scope for this context (graph
// contract §10): runtime uses continuation:<continuation_id>,
// operator uses operator:<actor_id>, system uses system:<actor_id>, and
// migration uses migration:<actor_id>.
func (c ExecutionContext) idempotencyScope() string {
	switch c.ActorType {
	case ActorTypeRuntime:
		return "continuation:" + c.ContinuationID
	case ActorTypeSystem:
		return "system:" + c.ActorID
	case ActorTypeOperator:
		return "operator:" + c.ActorID
	case ActorTypeMigration:
		return "migration:" + c.ActorID
	}
	return ""
}

// MutationBatch is a graph contract §9 batch. ProjectID is the caller-declared
// Project; it MUST match Context.ProjectID or project_mismatch is raised.
type MutationBatch struct {
	SchemaVersion  int              `json:"schema_version"`
	IdempotencyKey string           `json:"idempotency_key"`
	ProjectID      string           `json:"project_id,omitempty"`
	Context        ExecutionContext `json:"context"`
	Operations     []Operation      `json:"operations"`
}

// OperationResult is the per-operation outcome within a MutationResult.
type OperationResult struct {
	OpID         string   `json:"op_id"`
	NodeID       string   `json:"node_id,omitempty"`
	NodeType     NodeType `json:"node_type,omitempty"`
	StableKey    string   `json:"stable_key,omitempty"`
	NodeVersion  int      `json:"node_version,omitempty"`
	EdgeID       string   `json:"edge_id,omitempty"`
	EdgeType     EdgeType `json:"edge_type,omitempty"`
	EdgeVersion  int      `json:"edge_version,omitempty"`
	SemanticHash string   `json:"semantic_hash,omitempty"`
	Changed      bool     `json:"changed"`
}

// MutationResult is the observable outcome of Apply (graph contract §13,
// storage §9). ResultHash/ResultBytes carry the exact replay-comparable
// canonical result.
type MutationResult struct {
	GraphRevision      int               `json:"graph_revision"`
	RequestHash        string            `json:"request_hash"`
	ResultHash         string            `json:"result_hash"`
	ResultingStateHash string            `json:"resulting_state_hash"`
	Operations         []OperationResult `json:"operations"`
}

// ReadNodeRequest selects a node by key for the alias-resolving read.
type ReadNodeRequest struct {
	ProjectID string
	NodeType  NodeType
	Key       string
}

// NodeRecord is the read view of a node envelope (graph contract §3.1) plus
// the type-specific properties. It is the smallest view needed to observe a
// committed record at a graph revision (C02 minimal green path).
type NodeRecord struct {
	ID           string                `json:"id"`
	ProjectID    string                `json:"project_id"`
	NodeType     NodeType              `json:"node_type"`
	StableKey    string                `json:"stable_key"`
	Version      int                   `json:"version"`
	Disposition  Disposition           `json:"disposition"`
	ProjectFact  ProjectFactProperties `json:"project_fact_properties,omitempty"`
	PropertyMap  map[string]any        `json:"properties"`
	CreatedAt    string                `json:"created_at"`
	UpdatedAt    string                `json:"updated_at"`
	SemanticHash string                `json:"semantic_hash"`
	StateHash    string                `json:"state_hash"`
}

// ReadNodeResult wraps a NodeRecord with the observed graph revision and alias
// resolution metadata (graph contract §4).
type ReadNodeResult struct {
	Node                  NodeRecord
	ObservedGraphRevision int
	ResolvedFromAlias     string // empty if the key was the canonical stable key
}

type ReadEdgeRequest struct{ ProjectID, EdgeID string }
type EdgeRecord struct {
	ID, ProjectID                string
	EdgeType                     EdgeType
	FromNodeID, ToNodeID         string
	Version                      int
	State, Summary, SemanticHash string
}

// operationResultLedgerForm is the canonical JSON shape used both when writing
// result_json to the ledger and when decoding it back for replay/reads. Keeping
// one definition ensures the stored and decoded forms are byte-compatible.
type operationResultLedgerForm struct {
	OpID         string   `json:"op_id"`
	NodeID       string   `json:"node_id"`
	NodeType     NodeType `json:"node_type"`
	StableKey    string   `json:"stable_key"`
	NodeVersion  int      `json:"node_version"`
	EdgeID       string   `json:"edge_id,omitempty"`
	EdgeType     EdgeType `json:"edge_type,omitempty"`
	EdgeVersion  int      `json:"edge_version,omitempty"`
	SemanticHash string   `json:"semantic_hash"`
	Changed      bool     `json:"changed"`
}

// resultLedgerForm is the canonical JSON shape stored in
// blackboard_graph_mutations.result_json.
type resultLedgerForm struct {
	GraphRevision      int                         `json:"graph_revision"`
	RequestHash        string                      `json:"request_hash"`
	ResultHash         string                      `json:"result_hash"`
	ResultingStateHash string                      `json:"resulting_state_hash"`
	Operations         []operationResultLedgerForm `json:"operations"`
}

// ValidationError is the stable machine-readable validation error shape (graph
// contract §12). errors.As matches by type so callers can inspect Code.
type ValidationError struct {
	Code           string
	Message        string
	OperationIndex int
	OpID           string
	Path           string
	Retryable      bool
	Details        map[string]any
}

func (e *ValidationError) Error() string {
	if e.OpID != "" {
		return e.Code + " (" + e.OpID + "): " + e.Message
	}
	return e.Code + ": " + e.Message
}

// Validation error codes (graph contract §12).
const (
	ErrCodeUnsupportedSchemaVersion = "unsupported_schema_version"
	ErrCodeInvalidRequest           = "invalid_request"
	ErrCodeUnknownNodeType          = "unknown_node_type"
	ErrCodeUnknownEdgeType          = "unknown_edge_type"
	ErrCodeUnknownProperty          = "unknown_property"
	ErrCodeMissingProperty          = "missing_property"
	ErrCodeInvalidProperty          = "invalid_property"
	ErrCodeProjectNotFound          = "project_not_found"
	ErrCodeProjectMismatch          = "project_mismatch"
	ErrCodeProjectKindViolation     = "project_kind_violation"
	ErrCodeNodeKeyConflict          = "node_key_conflict"
	ErrCodeNodeNotFound             = "node_not_found"
	ErrCodeEdgeEndpointNotFound     = "edge_endpoint_not_found"
	ErrCodeEdgeEndpointType         = "edge_endpoint_type"
	ErrCodeSelfEdgeForbidden        = "self_edge_forbidden"
	ErrCodeGraphCycle               = "graph_cycle"
	ErrCodeTransitionGuardFailed    = "transition_guard_failed"
	ErrCodeImmutableField           = "immutable_field"
	ErrCodeInvalidTransition        = "invalid_transition"
	ErrCodeInvariantViolation       = "invariant_violation"
	ErrCodeVersionConflict          = "version_conflict"
	ErrCodeProvenanceSpoofed        = "provenance_spoofed"
	ErrCodeIdempotencyConflict      = "idempotency_conflict"
	ErrCodeProvenanceRequired       = "provenance_required"
)

// validationError builds a ValidationError at the given operation index.
func validationError(code, message string, opIndex int, opID, path string) *ValidationError {
	return &ValidationError{
		Code:           code,
		Message:        message,
		OperationIndex: opIndex,
		OpID:           opID,
		Path:           path,
	}
}
