package blackboard

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"unicode"

	"golang.org/x/text/unicode/norm"

	"pentest/internal/store"
)

const BlackboardReadProtocolVersion = 1

const (
	ReadKindRecordCollectionV1         ReadKind = "record_collection_v1"
	ReadKindRecordResolveV1            ReadKind = "record_resolve_v1"
	ReadKindRecordDetailV1             ReadKind = "record_detail_v1"
	ReadKindRecordHistoryV1            ReadKind = "record_history_v1"
	ReadKindRecordProvenanceV1         ReadKind = "record_provenance_v1"
	ReadKindGraphTraversalV1           ReadKind = "graph_traversal_v1"
	ReadKindBlackboardHealthV1         ReadKind = "blackboard_health_v1"
	ReadKindHealthRunV1                ReadKind = "health_run_v1"
	ReadKindHealthResultsV1            ReadKind = "health_results_v1"
	ReadKindGraphExplorerV1            ReadKind = "graph_explorer_v1"
	ReadKindCanonicalGraphV1           ReadKind = "canonical_main_graph_v1"
	ReadKindBlackboardWorkV1           ReadKind = "blackboard_work_v1"
	ReadKindProjectBlackboardSummaryV1 ReadKind = "project_blackboard_summary_v1"
	ReadKindCurrentTruthV1             ReadKind = "current_truth_v1"
	ReadKindExplorationFrontierV1      ReadKind = "exploration_frontier_v1"
	ReadKindEntityCollectionV1         ReadKind = "entity_collection_v1"
	ReadKindEntityDetailV1             ReadKind = "entity_detail_v1"
	ReadKindPentestReportV1            ReadKind = "pentest_report_v1"
	ReadKindCTFSolutionV1              ReadKind = "ctf_solution_v1"
	ReadKindLegacyFactIndexV1          ReadKind = "legacy_fact_index_v1"
	ReadKindLegacyFactDetailV1         ReadKind = "legacy_fact_detail_v1"
	ReadKindLegacyFactVersionsV1       ReadKind = "legacy_fact_versions_v1"
	ReadKindLegacyFactRelationsV1      ReadKind = "legacy_fact_relations_v1"
	ReadKindLegacyFindingCollectionV1  ReadKind = "legacy_finding_collection_v1"
	ReadKindLegacyFindingVersionsV1    ReadKind = "legacy_finding_versions_v1"
	ReadKindLegacyEvidenceCollectionV1 ReadKind = "legacy_evidence_collection_v1"
)

const (
	RecordSortAttention   = "attention"
	RecordSortUpdatedDesc = "updated_desc"
	RecordSortCreatedAsc  = "created_asc"
	RecordSortStableKey   = "stable_key"
	RecordSortSeverity    = "severity"
)

const recordSortRelevance = "relevance"

const readCursorDomain = "CyberPenda.Blackboard.ReadCursor.v1"

// ReadKind is the versioned Blackboard read shape for this projection.
type ReadKind string

// ReadRequest is the versioned Blackboard read shape for this projection.
type ReadRequest struct {
	ProtocolVersion          int
	ProjectID                string
	Kind                     ReadKind
	AtRevision               *int
	RecordCollection         *RecordCollectionRequest
	RecordResolve            *RecordResolveRequest
	RecordDetail             *RecordDetailRequest
	RecordHistory            *RecordHistoryRequest
	RecordProvenance         *RecordProvenanceRequest
	GraphTraversal           *GraphTraversalRequest
	BlackboardHealth         *BlackboardHealthRequest
	HealthRun                *HealthRunRequest
	HealthResults            *HealthResultsRequest
	GraphExplorer            *GraphExplorerRequest
	BlackboardWork           *BlackboardWorkRequest
	ProjectSummary           *ProjectBlackboardSummaryRequest
	CurrentTruth             *CurrentTruthRequest
	ExplorationFrontier      *ExplorationFrontierRequest
	EntityCollection         *EntityCollectionRequest
	EntityDetail             *EntityDetailRequest
	PentestReport            *PentestReportRequest
	CTFSolution              *CTFSolutionRequest
	LegacyFactIndex          *LegacyFactIndexRequest
	LegacyFactDetail         *LegacyFactDetailRequest
	LegacyFactVersions       *LegacyFactVersionsRequest
	LegacyFactRelations      *LegacyFactRelationsRequest
	LegacyFindingCollection  *LegacyFindingCollectionRequest
	LegacyFindingVersions    *LegacyFindingVersionsRequest
	LegacyEvidenceCollection *LegacyEvidenceCollectionRequest
}

// ReadEnvelope is the versioned Blackboard read shape for this projection.
type ReadEnvelope struct {
	ProtocolVersion       int            `json:"protocol_version"`
	Projection            string         `json:"projection"`
	ProjectID             string         `json:"project_id"`
	ProjectKind           string         `json:"project_kind"`
	ObservedGraphRevision int            `json:"observed_graph_revision"`
	ObservedStateHash     string         `json:"observed_state_hash"`
	SourcePins            map[string]any `json:"source_pins"`
	ProjectionHash        string         `json:"projection_hash"`
	Result                any            `json:"result"`
}

// RecordCollectionRequest is the versioned Blackboard read shape for this projection.
type RecordCollectionRequest struct {
	NodeTypes        []NodeType
	Dispositions     []Disposition
	Lifecycle        []string
	ScopeStatus      []string
	Severity         []string
	EntityKind       []string
	ActorType        []string
	TaskID           string
	ContinuationID   string
	RuntimeProfileID string
	Runner           string
	AboutEntityID    string
	EdgeType         EdgeType
	Direction        string
	HasEvidence      *bool
	HasContradiction *bool
	Frontier         *bool
	HealthSeverity   []string
	UpdatedBefore    string
	UpdatedAfter     string
	Query            string
	Sort             string
	Limit            int
	Cursor           string
}

// RecordResolveRequest is the versioned Blackboard read shape for this projection.
type RecordResolveRequest struct {
	NodeType  NodeType
	StableKey string
	NodeID    string
}

// RecordResolveV1 is the versioned Blackboard read shape for this projection.
type RecordResolveV1 struct {
	Requested            NodeRefV1 `json:"requested"`
	Resolved             NodeRefV1 `json:"resolved"`
	ResolvedFromAlias    *string   `json:"resolved_from_alias"`
	ResolvedFromMergedID *string   `json:"resolved_from_merged_id"`
}

// RecordHistoryRequest is the versioned Blackboard read shape for this projection.
type RecordHistoryRequest struct {
	NodeID        string
	Literal       bool
	BeforeVersion int
	Limit         int
	Cursor        string
}

// NodeRefV1 is the versioned Blackboard read shape for this projection.
type NodeRefV1 struct {
	ID        string   `json:"id"`
	NodeType  NodeType `json:"node_type"`
	StableKey string   `json:"stable_key"`
	Label     string   `json:"label"`
}

// LifecycleV1 is the versioned Blackboard read shape for this projection.
type LifecycleV1 struct {
	Field string `json:"field"`
	Value string `json:"value"`
}

// RelationshipCountsV1 is the versioned Blackboard read shape for this projection.
type RelationshipCountsV1 struct {
	AboutEntities  int `json:"about_entities"`
	Incoming       int `json:"incoming"`
	Outgoing       int `json:"outgoing"`
	Evidence       int `json:"evidence"`
	Contradictions int `json:"contradictions"`
}

// ProvenanceSummaryV1 is the versioned Blackboard read shape for this projection.
type ProvenanceSummaryV1 struct {
	ActorType        ActorType `json:"actor_type"`
	ActorID          string    `json:"actor_id"`
	TaskID           *string   `json:"task_id"`
	ContinuationID   *string   `json:"continuation_id"`
	RuntimeProfileID *string   `json:"runtime_profile_id"`
	Runner           *string   `json:"runner"`
	SourceEventCount int       `json:"source_event_count"`
	MigrationSource  any       `json:"migration_source"`
	RecordedAt       string    `json:"recorded_at"`
}

// NodeRowV1 is the versioned Blackboard read shape for this projection.
type NodeRowV1 struct {
	Ref                NodeRefV1            `json:"ref"`
	Version            int                  `json:"version"`
	Disposition        Disposition          `json:"disposition"`
	Lifecycle          *LifecycleV1         `json:"lifecycle"`
	ScopeStatus        *string              `json:"scope_status"`
	Severity           *string              `json:"severity"`
	Secondary          *string              `json:"secondary"`
	UpdatedAt          string               `json:"updated_at"`
	AboutEntities      []NodeRefV1          `json:"about_entities"`
	RelationshipCounts RelationshipCountsV1 `json:"relationship_counts"`
	UpdatedProvenance  ProvenanceSummaryV1  `json:"updated_provenance"`
	searchRank         int
	attentionRank      int
	entityKind         string
}

// PageV1 is the versioned Blackboard read shape for this projection.
type PageV1 struct {
	Limit      int    `json:"limit"`
	TotalItems int    `json:"total_items"`
	NextCursor string `json:"next_cursor,omitempty"`
}

// RecordCollectionV1 is the versioned Blackboard read shape for this projection.
type RecordCollectionV1 struct {
	Items      []NodeRowV1    `json:"items"`
	Facets     map[string]any `json:"facets"`
	Page       PageV1         `json:"page"`
	sourcePins map[string]any
}

// BlackboardWorkRequest is the versioned Blackboard read shape for this projection.
type BlackboardWorkRequest struct{}

// BlackboardWorkBudgetV1 is the versioned Blackboard read shape for this projection.
type BlackboardWorkBudgetV1 struct {
	State           BudgetState `json:"state"`
	ProjectionBytes int         `json:"projection_bytes"`
	EstimatedTokens int         `json:"estimated_tokens"`
	TargetTokens    int         `json:"target_tokens"`
	WarningTokens   int         `json:"warning_tokens"`
	RequiredTokens  int         `json:"required_tokens"`
}

// BlackboardWorkSummaryV1 is the versioned Blackboard read shape for this projection.
type BlackboardWorkSummaryV1 struct {
	GraphRevision       int                    `json:"graph_revision"`
	NodeCounts          map[string]int         `json:"node_counts"`
	EdgeCounts          map[string]int         `json:"edge_counts"`
	CurrentTruth        int                    `json:"current_truth"`
	Frontier            int                    `json:"frontier"`
	OpenAttempts        int                    `json:"open_attempts"`
	ConfirmedFindings   int                    `json:"confirmed_findings"`
	UnconfirmedFindings int                    `json:"unconfirmed_findings"`
	VerifiedSolutions   int                    `json:"verified_solutions"`
	EvidenceMissing     int                    `json:"evidence_missing"`
	Budget              BlackboardWorkBudgetV1 `json:"budget"`
	Health              DashboardHealthV1      `json:"health"`
}

// SemanticChangeV1 is the versioned Blackboard read shape for this projection.
type SemanticChangeV1 struct {
	Kind      string     `json:"kind"`
	Node      *NodeRowV1 `json:"node"`
	Edge      *EdgeRowV1 `json:"edge"`
	UpdatedAt string     `json:"updated_at"`
}

// SemanticChangeCollectionV1 is the versioned Blackboard read shape for this projection.
type SemanticChangeCollectionV1 struct {
	Items []SemanticChangeV1 `json:"items"`
	Page  PageV1             `json:"page"`
}

// BlackboardWorkViewV1 is the versioned Blackboard read shape for this projection.
type BlackboardWorkViewV1 struct {
	Summary        BlackboardWorkSummaryV1    `json:"summary"`
	Attention      RecordCollectionV1         `json:"attention"`
	Frontier       RecordCollectionV1         `json:"frontier"`
	ActiveAttempts RecordCollectionV1         `json:"active_attempts"`
	RecentChanges  SemanticChangeCollectionV1 `json:"recent_changes"`
	Facets         map[string]any             `json:"facets"`
}

// RecordHistoryV1 is the versioned Blackboard read shape for this projection.
type RecordHistoryV1 struct {
	Record     NodeRefV1            `json:"record"`
	Versions   []NodeHistoryVersion `json:"versions"`
	Page       PageV1               `json:"page"`
	KeyHistory []GraphKey           `json:"key_history"`
	Merge      *NodeRefV1           `json:"merge"`
}

// NodeHistoryVersion is the versioned Blackboard read shape for this projection.
type NodeHistoryVersion struct {
	Version      int            `json:"version"`
	Disposition  Disposition    `json:"disposition"`
	Properties   map[string]any `json:"properties"`
	UpdatedAt    string         `json:"updated_at"`
	SemanticHash string         `json:"semantic_hash"`
}

// BlackboardReadService is the versioned Blackboard read shape for this projection.
type BlackboardReadService struct {
	db           *store.DB
	cursorKey    []byte
	cursorKeyErr error
	artifactRoot string
}

func NewBlackboardReadService(db *store.DB) *BlackboardReadService {
	service := &BlackboardReadService{db: db}
	service.cursorKeyErr = db.QueryRow(`SELECT cursor_secret FROM blackboard_read_state WHERE id=1`).Scan(&service.cursorKey)
	if service.cursorKeyErr == nil && len(service.cursorKey) != 32 {
		service.cursorKeyErr = fmt.Errorf("Blackboard read cursor secret has invalid length %d", len(service.cursorKey))
	}
	return service
}

type readCursor struct {
	Version    int               `json:"version"`
	Projection ReadKind          `json:"projection"`
	ProjectID  string            `json:"project_id"`
	Revision   int               `json:"revision"`
	QueryHash  string            `json:"query_hash"`
	Sort       string            `json:"sort"`
	Limit      int               `json:"limit"`
	Last       []string          `json:"last"`
	SourcePins map[string]string `json:"source_pins,omitempty"`
}

// WithArtifactRoot confines Health staleness checks to the managed Artifact Root.
func (s *BlackboardReadService) WithArtifactRoot(root string) *BlackboardReadService {
	s.artifactRoot = root
	return s
}

func (s *BlackboardReadService) Read(ctx context.Context, request ReadRequest) (ReadEnvelope, error) {
	if s.cursorKeyErr != nil {
		return ReadEnvelope{}, readValidationError(ErrCodeSnapshotUnavailable, "Blackboard read cursor state is unavailable", "cursor")
	}
	if request.ProtocolVersion != BlackboardReadProtocolVersion {
		return ReadEnvelope{}, validationError(ErrCodeUnsupportedSchemaVersion, "unsupported Blackboard read protocol version", -1, "", "protocol_version")
	}
	if request.ProjectID == "" {
		return ReadEnvelope{}, readValidationError(ErrCodeInvalidQuery, "project_id is required", "project_id")
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return ReadEnvelope{}, graphStorageError("begin Blackboard read", err)
	}
	defer func() { _ = tx.Rollback() }()

	var projectKind string
	if err := tx.QueryRowContext(ctx, `SELECT kind FROM projects WHERE id=?`, request.ProjectID).Scan(&projectKind); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ReadEnvelope{}, validationError(ErrCodeProjectNotFound, "project does not exist", -1, "", "project_id")
		}
		return ReadEnvelope{}, fmt.Errorf("read project kind: %w", err)
	}

	revision, cursor, err := resolveReadRevision(ctx, tx, request, s.cursorKey)
	if err != nil {
		return ReadEnvelope{}, err
	}
	if err := verifyMutationChain(ctx, tx, request.ProjectID); err != nil {
		return ReadEnvelope{}, fmt.Errorf("verify graph ledger before read: %w", err)
	}
	snapshot, err := reconstructGraph(ctx, tx, request.ProjectID, revision)
	if err != nil {
		var validation *ValidationError
		if errors.As(err, &validation) && validation.Code == ErrCodeInvalidRequest {
			return ReadEnvelope{}, readValidationError(ErrCodeRevisionNotFound, validation.Message, "at_revision")
		}
		return ReadEnvelope{}, readValidationError(ErrCodeSnapshotUnavailable, err.Error(), "at_revision")
	}

	var result any
	switch request.Kind {
	case ReadKindRecordCollectionV1:
		if request.RecordCollection == nil {
			return ReadEnvelope{}, readValidationError(ErrCodeInvalidQuery, "record_collection request is required", "record_collection")
		}
		result, err = buildRecordCollection(ctx, tx, snapshot, *request.RecordCollection, cursor, s.cursorKey)
	case ReadKindRecordDetailV1:
		if request.RecordDetail == nil {
			return ReadEnvelope{}, readValidationError(ErrCodeInvalidQuery, "record_detail request is required", "record_detail")
		}
		result, err = buildRecordDetail(ctx, tx, snapshot, *request.RecordDetail)
	case ReadKindRecordResolveV1:
		if request.RecordResolve == nil {
			return ReadEnvelope{}, readValidationError(ErrCodeInvalidQuery, "record_resolve request is required", "record_resolve")
		}
		result, err = buildRecordResolve(snapshot, *request.RecordResolve)
	case ReadKindRecordHistoryV1:
		if request.RecordHistory == nil {
			return ReadEnvelope{}, readValidationError(ErrCodeInvalidQuery, "record_history request is required", "record_history")
		}
		result, err = buildRecordHistory(ctx, tx, snapshot, *request.RecordHistory, cursor, s.cursorKey)
	case ReadKindRecordProvenanceV1:
		if request.RecordProvenance == nil {
			return ReadEnvelope{}, readValidationError(ErrCodeInvalidQuery, "record_provenance request is required", "record_provenance")
		}
		result, err = buildRecordProvenance(ctx, tx, snapshot, *request.RecordProvenance)
	case ReadKindGraphTraversalV1:
		if request.GraphTraversal == nil {
			return ReadEnvelope{}, readValidationError(ErrCodeInvalidQuery, "graph_traversal request is required", "graph_traversal")
		}
		result, err = buildGraphTraversal(ctx, tx, snapshot, *request.GraphTraversal)
	case ReadKindBlackboardHealthV1:
		if request.BlackboardHealth == nil {
			return ReadEnvelope{}, readValidationError(ErrCodeInvalidQuery, "blackboard_health request is required", "blackboard_health")
		}
		result, err = buildBlackboardHealth(ctx, tx, snapshot, s.artifactRoot)
	case ReadKindHealthRunV1:
		if request.HealthRun == nil {
			return ReadEnvelope{}, readValidationError(ErrCodeInvalidQuery, "health_run request is required", "health_run")
		}
		result, err = buildHealthRun(ctx, tx, snapshot, *request.HealthRun, s.artifactRoot)
	case ReadKindHealthResultsV1:
		if request.HealthResults == nil {
			return ReadEnvelope{}, readValidationError(ErrCodeInvalidQuery, "health_results request is required", "health_results")
		}
		result, err = buildHealthResults(ctx, tx, snapshot, *request.HealthResults, cursor, s.cursorKey, s.artifactRoot)
	case ReadKindGraphExplorerV1:
		if request.GraphExplorer == nil {
			return ReadEnvelope{}, readValidationError(ErrCodeInvalidQuery, "graph_explorer request is required", "graph_explorer")
		}
		result, err = buildGraphExplorer(ctx, tx, snapshot, *request.GraphExplorer)
	case ReadKindCanonicalGraphV1:
		result, err = canonicalMainGraphDocumentAt(ctx, tx, request.ProjectID, revision)
	case ReadKindEntityCollectionV1:
		if request.EntityCollection == nil {
			return ReadEnvelope{}, readValidationError(ErrCodeInvalidQuery, "entity_collection request is required", "entity_collection")
		}
		result, err = buildEntityCollection(ctx, tx, snapshot, *request.EntityCollection, cursor, s.cursorKey)
	case ReadKindEntityDetailV1:
		if request.EntityDetail == nil {
			return ReadEnvelope{}, readValidationError(ErrCodeInvalidQuery, "entity_detail request is required", "entity_detail")
		}
		result, err = buildEntityDetail(ctx, tx, snapshot, *request.EntityDetail)
	case ReadKindExplorationFrontierV1:
		if request.ExplorationFrontier == nil {
			return ReadEnvelope{}, readValidationError(ErrCodeInvalidQuery, "exploration_frontier request is required", "exploration_frontier")
		}
		result, err = buildExplorationFrontier(snapshot, *request.ExplorationFrontier, cursor, s.cursorKey)
	case ReadKindCurrentTruthV1:
		if request.CurrentTruth == nil {
			return ReadEnvelope{}, readValidationError(ErrCodeInvalidQuery, "current_truth request is required", "current_truth")
		}
		result, err = buildCurrentTruth(snapshot, *request.CurrentTruth, cursor, s.cursorKey)
	case ReadKindProjectBlackboardSummaryV1:
		if request.ProjectSummary == nil {
			return ReadEnvelope{}, readValidationError(ErrCodeInvalidQuery, "project_summary request is required", "project_summary")
		}
		result, err = buildProjectBlackboardSummary(ctx, tx, snapshot, projectKind)
	case ReadKindBlackboardWorkV1:
		if request.BlackboardWork == nil {
			return ReadEnvelope{}, readValidationError(ErrCodeInvalidQuery, "blackboard_work request is required", "blackboard_work")
		}
		result, err = buildBlackboardWork(ctx, tx, snapshot)
	case ReadKindPentestReportV1:
		if request.PentestReport == nil {
			return ReadEnvelope{}, readValidationError(ErrCodeInvalidQuery, "pentest_report request is required", "pentest_report")
		}
		result, err = buildPentestReport(ctx, tx, snapshot, projectKind, *request.PentestReport)
	case ReadKindCTFSolutionV1:
		if request.CTFSolution == nil {
			return ReadEnvelope{}, readValidationError(ErrCodeInvalidQuery, "ctf_solution request is required", "ctf_solution")
		}
		result, err = buildCTFSolutionReport(ctx, tx, snapshot, projectKind, *request.CTFSolution)
	case ReadKindLegacyFactIndexV1:
		if request.LegacyFactIndex == nil {
			return ReadEnvelope{}, readValidationError(ErrCodeInvalidQuery, "legacy_fact_index request is required", "legacy_fact_index")
		}
		result, err = buildLegacyFactIndex(snapshot, *request.LegacyFactIndex, cursor, s.cursorKey)
	case ReadKindLegacyFactDetailV1:
		if request.LegacyFactDetail == nil {
			return ReadEnvelope{}, readValidationError(ErrCodeInvalidQuery, "legacy_fact_detail request is required", "legacy_fact_detail")
		}
		result, err = buildLegacyFactDetail(snapshot, *request.LegacyFactDetail)
	case ReadKindLegacyFactVersionsV1:
		if request.LegacyFactVersions == nil {
			return ReadEnvelope{}, readValidationError(ErrCodeInvalidQuery, "legacy_fact_versions request is required", "legacy_fact_versions")
		}
		result, err = buildLegacyFactVersions(ctx, tx, snapshot, *request.LegacyFactVersions)
	case ReadKindLegacyFactRelationsV1:
		if request.LegacyFactRelations == nil {
			return ReadEnvelope{}, readValidationError(ErrCodeInvalidQuery, "legacy_fact_relations request is required", "legacy_fact_relations")
		}
		result, err = buildLegacyFactRelations(ctx, tx, snapshot, *request.LegacyFactRelations)
	case ReadKindLegacyFindingCollectionV1:
		if request.LegacyFindingCollection == nil {
			return ReadEnvelope{}, readValidationError(ErrCodeInvalidQuery, "legacy_finding_collection request is required", "legacy_finding_collection")
		}
		result, err = buildLegacyFindingCollection(snapshot, *request.LegacyFindingCollection, cursor, s.cursorKey)
	case ReadKindLegacyFindingVersionsV1:
		if request.LegacyFindingVersions == nil {
			return ReadEnvelope{}, readValidationError(ErrCodeInvalidQuery, "legacy_finding_versions request is required", "legacy_finding_versions")
		}
		result, err = buildLegacyFindingVersions(ctx, tx, snapshot, *request.LegacyFindingVersions)
	case ReadKindLegacyEvidenceCollectionV1:
		if request.LegacyEvidenceCollection == nil {
			return ReadEnvelope{}, readValidationError(ErrCodeInvalidQuery, "legacy_evidence_collection request is required", "legacy_evidence_collection")
		}
		result, err = buildLegacyEvidenceCollection(snapshot, *request.LegacyEvidenceCollection, cursor, s.cursorKey)
	default:
		err = readValidationError(ErrCodeInvalidQuery, "unknown read projection", "kind")
	}
	if err != nil {
		return ReadEnvelope{}, err
	}

	sourcePins := map[string]any{}
	switch value := result.(type) {
	case RecordCollectionV1:
		for key, pin := range value.sourcePins {
			sourcePins[key] = pin
		}
	case EntityCollectionV1:
		for key, pin := range value.sourcePins {
			sourcePins[key] = pin
		}
	case RecordDetailV1:
		for key, pin := range value.sourcePins {
			sourcePins[key] = pin
		}
	case EntityDetailV1:
		for key, pin := range value.sourcePins {
			sourcePins[key] = pin
		}
	case BlackboardWorkViewV1:
		if value.Summary.Health.LatestRunID != "" {
			sourcePins["health_run_id"] = value.Summary.Health.LatestRunID
		}
	case ProjectBlackboardSummaryV1:
		if value.Health.LatestRunID != "" {
			sourcePins["health_run_id"] = value.Health.LatestRunID
		}
	case BlackboardHealthV1:
		if value.LatestRun != nil {
			sourcePins["health_run_id"] = value.LatestRun.RunID
		}
	case HealthRunV1:
		sourcePins["health_run_id"] = value.RunID
	case HealthResultCollectionV1:
		if request.HealthResults != nil {
			sourcePins["health_run_id"] = request.HealthResults.RunID
		}
	}
	envelope := ReadEnvelope{
		ProtocolVersion:       BlackboardReadProtocolVersion,
		Projection:            string(request.Kind),
		ProjectID:             request.ProjectID,
		ProjectKind:           projectKind,
		ObservedGraphRevision: snapshot.GraphRevision,
		ObservedStateHash:     snapshot.StateHash,
		SourcePins:            sourcePins,
		Result:                result,
	}
	envelope.ProjectionHash, err = hashReadEnvelope(envelope)
	if err != nil {
		return ReadEnvelope{}, err
	}
	if err := tx.Commit(); err != nil {
		return ReadEnvelope{}, graphStorageError("commit Blackboard read", err)
	}
	return envelope, nil
}

func resolveReadRevision(ctx context.Context, tx *sql.Tx, request ReadRequest, cursorKey []byte) (int, *readCursor, error) {
	var latest int
	if err := tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(result_graph_revision),0) FROM blackboard_graph_mutations WHERE project_id=?`, request.ProjectID).Scan(&latest); err != nil {
		return 0, nil, fmt.Errorf("read current graph revision: %w", err)
	}
	if request.RecordCollection != nil && request.RecordCollection.Cursor != "" {
		cursor, err := decodeReadCursor(request.RecordCollection.Cursor, cursorKey)
		if err != nil {
			return 0, nil, err
		}
		if cursor.ProjectID != request.ProjectID || cursor.Projection != request.Kind {
			return 0, nil, readValidationError(ErrCodeInvalidCursor, "The cursor does not match this query.", "cursor")
		}
		return cursor.Revision, &cursor, nil
	}
	if request.HealthResults != nil && request.HealthResults.Cursor != "" {
		cursor, err := decodeReadCursor(request.HealthResults.Cursor, cursorKey)
		if err != nil {
			return 0, nil, err
		}
		if cursor.ProjectID != request.ProjectID || cursor.Projection != request.Kind {
			return 0, nil, readValidationError(ErrCodeInvalidCursor, "The cursor does not match this query.", "cursor")
		}
		return cursor.Revision, &cursor, nil
	}
	if request.RecordHistory != nil && request.RecordHistory.Cursor != "" {
		cursor, err := decodeReadCursor(request.RecordHistory.Cursor, cursorKey)
		if err != nil {
			return 0, nil, err
		}
		if cursor.ProjectID != request.ProjectID || cursor.Projection != request.Kind {
			return 0, nil, readValidationError(ErrCodeInvalidCursor, "The cursor does not match this query.", "cursor")
		}
		return cursor.Revision, &cursor, nil
	}
	collectionCursor := ""
	switch {
	case request.CurrentTruth != nil:
		collectionCursor = request.CurrentTruth.Cursor
	case request.ExplorationFrontier != nil:
		collectionCursor = request.ExplorationFrontier.Cursor
	case request.EntityCollection != nil:
		collectionCursor = request.EntityCollection.Cursor
	case request.LegacyFactIndex != nil:
		collectionCursor = request.LegacyFactIndex.Cursor
	case request.LegacyFindingCollection != nil:
		collectionCursor = request.LegacyFindingCollection.Cursor
	case request.LegacyEvidenceCollection != nil:
		collectionCursor = request.LegacyEvidenceCollection.Cursor
	}
	if collectionCursor != "" {
		cursor, err := decodeReadCursor(collectionCursor, cursorKey)
		if err != nil {
			return 0, nil, err
		}
		if cursor.ProjectID != request.ProjectID || cursor.Projection != request.Kind {
			return 0, nil, readValidationError(ErrCodeInvalidCursor, "The cursor does not match this query.", "cursor")
		}
		return cursor.Revision, &cursor, nil
	}
	if request.AtRevision != nil {
		if *request.AtRevision < 0 || *request.AtRevision > latest {
			return 0, nil, readValidationError(ErrCodeRevisionNotFound, "requested graph revision does not exist", "at_revision")
		}
		return *request.AtRevision, nil, nil
	}
	return latest, nil, nil
}

func buildRecordCollection(ctx context.Context, tx *sql.Tx, snapshot GraphSnapshot, request RecordCollectionRequest, cursor *readCursor, cursorKey []byte) (RecordCollectionV1, error) {
	normalized, err := normalizeRecordCollectionRequest(request)
	if err != nil {
		return RecordCollectionV1{}, err
	}
	queryHash, err := normalizedRecordQueryHash(normalized)
	if err != nil {
		return RecordCollectionV1{}, err
	}
	if cursor != nil && (cursor.QueryHash != queryHash || cursor.Sort != normalized.Sort || cursor.Limit != normalized.Limit) {
		return RecordCollectionV1{}, readValidationError(ErrCodeInvalidCursor, "The cursor does not match this query.", "cursor")
	}

	rows := make([]NodeRowV1, 0, len(snapshot.Nodes))
	frontierSet := make(map[string]bool)
	for _, id := range historicalFrontierNodeIDs(snapshot) {
		frontierSet[id] = true
	}
	pinnedHealthRun := ""
	if cursor != nil && cursor.SourcePins != nil {
		pinnedHealthRun = cursor.SourcePins["health_run_id"]
	}
	healthRanks, healthRunID, err := healthSubjectRanks(ctx, tx, snapshot.ProjectID, snapshot.GraphRevision, pinnedHealthRun)
	if err != nil {
		return RecordCollectionV1{}, err
	}
	for _, node := range snapshot.Nodes {
		if !recordMatches(node, normalized) {
			continue
		}
		row, err := nodeRowAt(ctx, tx, snapshot, node)
		if err != nil {
			return RecordCollectionV1{}, err
		}
		row.searchRank = lexicalSearchRank(node, normalized.Query)
		row.attentionRank = workAttentionRank(node, frontierSet[node.ID], healthRanks[node.ID])
		if !recordRowMatches(snapshot, node, row, normalized, frontierSet[node.ID], healthRanks[node.ID]) {
			continue
		}
		rows = append(rows, row)
	}
	sortRecordRows(rows, normalized.Sort)
	total := len(rows)
	start := 0
	if cursor != nil {
		start = sort.Search(len(rows), func(i int) bool { return compareRowTuple(rowTuple(rows[i], normalized.Sort), cursor.Last) > 0 })
	}
	end := start + normalized.Limit
	if end > len(rows) {
		end = len(rows)
	}
	pageRows := append([]NodeRowV1(nil), rows[start:end]...)
	next := ""
	if end < len(rows) && len(pageRows) > 0 {
		next, err = encodeReadCursor(readCursor{
			Version: BlackboardReadProtocolVersion, Projection: ReadKindRecordCollectionV1,
			ProjectID: snapshot.ProjectID, Revision: snapshot.GraphRevision, QueryHash: queryHash,
			Sort: normalized.Sort, Limit: normalized.Limit, Last: rowTuple(pageRows[len(pageRows)-1], normalized.Sort), SourcePins: stringSourcePins("health_run_id", healthRunID),
		}, cursorKey)
		if err != nil {
			return RecordCollectionV1{}, err
		}
	}
	return RecordCollectionV1{Items: pageRows, Facets: recordFacets(rows), Page: PageV1{Limit: normalized.Limit, TotalItems: total, NextCursor: next}, sourcePins: anySourcePins("health_run_id", healthRunID)}, nil
}

func normalizeRecordCollectionRequest(request RecordCollectionRequest) (RecordCollectionRequest, error) {
	if request.Limit == 0 {
		request.Limit = 50
	}
	if request.Limit < 1 || request.Limit > 200 {
		return RecordCollectionRequest{}, readValidationError(ErrCodeInvalidQuery, "limit must be between 1 and 200", "limit")
	}
	if request.Sort == "" {
		if normalizeSearchText(request.Query) != "" {
			request.Sort = recordSortRelevance
		} else {
			request.Sort = RecordSortAttention
		}
	}
	switch request.Sort {
	case RecordSortAttention, RecordSortUpdatedDesc, RecordSortCreatedAsc, RecordSortStableKey, RecordSortSeverity, recordSortRelevance:
	default:
		return RecordCollectionRequest{}, readValidationError(ErrCodeInvalidQuery, "unsupported sort", "sort")
	}
	if len([]rune(request.Query)) > 500 {
		return RecordCollectionRequest{}, readValidationError(ErrCodeInvalidQuery, "query exceeds 500 Unicode scalar values", "query")
	}
	if len(request.NodeTypes) > 50 || len(request.Dispositions) > 50 || len(request.Lifecycle) > 50 || len(request.ScopeStatus) > 50 || len(request.Severity) > 50 || len(request.EntityKind) > 50 || len(request.ActorType) > 50 || len(request.HealthSeverity) > 50 {
		return RecordCollectionRequest{}, readValidationError(ErrCodeInvalidQuery, "repeated filter exceeds 50 values", "filters")
	}
	request.Query = normalizeSearchText(request.Query)
	request.NodeTypes = sortedUniqueNodeTypes(request.NodeTypes)
	for _, nodeType := range request.NodeTypes {
		if nodeTypeOrdinal(nodeType) < 0 {
			return RecordCollectionRequest{}, readValidationError(ErrCodeInvalidQuery, "unknown node_type filter", "node_type")
		}
	}
	request.Dispositions = sortedUniqueDispositions(request.Dispositions)
	for _, disposition := range request.Dispositions {
		if disposition != DispositionMain && disposition != DispositionArchived && disposition != DispositionMerged {
			return RecordCollectionRequest{}, readValidationError(ErrCodeInvalidQuery, "unknown disposition filter", "disposition")
		}
	}
	request.Lifecycle = sortedUniqueStrings(request.Lifecycle)
	request.ScopeStatus = sortedUniqueStrings(request.ScopeStatus)
	request.Severity = sortedUniqueStrings(request.Severity)
	request.EntityKind = sortedUniqueStrings(request.EntityKind)
	request.ActorType = sortedUniqueStrings(request.ActorType)
	request.HealthSeverity = sortedUniqueStrings(request.HealthSeverity)
	if request.Direction == "" {
		request.Direction = "either"
	}
	if request.Direction != "incoming" && request.Direction != "outgoing" && request.Direction != "either" {
		return RecordCollectionRequest{}, readValidationError(ErrCodeInvalidQuery, "unsupported direction", "direction")
	}
	if request.EdgeType != "" && edgeTypeOrdinal(request.EdgeType) < 0 {
		return RecordCollectionRequest{}, readValidationError(ErrCodeInvalidQuery, "unsupported edge_type", "edge_type")
	}
	if err := validateEnumFilters("scope_status", request.ScopeStatus, "in_scope", "unknown", "out_of_scope"); err != nil {
		return RecordCollectionRequest{}, err
	}
	if err := validateEnumFilters("actor_type", request.ActorType, "runtime", "operator", "system", "migration"); err != nil {
		return RecordCollectionRequest{}, err
	}
	if err := validateEnumFilters("health_severity", request.HealthSeverity, "critical", "warning", "info"); err != nil {
		return RecordCollectionRequest{}, err
	}
	if len(request.Dispositions) == 0 {
		request.Dispositions = []Disposition{DispositionMain}
	}
	return request, nil
}

func recordMatches(node NodeRecord, request RecordCollectionRequest) bool {
	if len(request.NodeTypes) > 0 && !containsNodeType(request.NodeTypes, node.NodeType) {
		return false
	}
	if !containsDisposition(request.Dispositions, node.Disposition) {
		return false
	}
	_, lifecycle := lifecycleForNode(node)
	if len(request.Lifecycle) > 0 && !containsString(request.Lifecycle, lifecycle) {
		return false
	}
	if request.Query != "" && !matchesLexicalQuery(node, request.Query) {
		return false
	}
	return true
}

func nodeRowAt(ctx context.Context, tx *sql.Tx, snapshot GraphSnapshot, node NodeRecord) (NodeRowV1, error) {
	field, value := lifecycleForNode(node)
	var lifecycle *LifecycleV1
	if field != "" {
		lifecycle = &LifecycleV1{Field: field, Value: value}
	}
	var scopeStatus, severity, secondary *string
	if value, ok := stringProperty(node.PropertyMap, "scope_status"); ok {
		scopeStatus = &value
	}
	if value, ok := stringProperty(node.PropertyMap, "severity"); ok {
		severity = &value
	}
	if value := secondaryForNode(node); value != "" {
		secondary = &value
	}
	about, counts := relationshipsForNode(snapshot, node.ID)
	provenance, err := provenanceSummaryForNodeVersion(ctx, tx, snapshot.ProjectID, node.ID, node.Version)
	if err != nil {
		return NodeRowV1{}, err
	}
	row := NodeRowV1{
		Ref: nodeRefForNode(node), Version: node.Version, Disposition: node.Disposition,
		Lifecycle: lifecycle, ScopeStatus: scopeStatus, Severity: severity, Secondary: secondary,
		UpdatedAt: node.UpdatedAt, AboutEntities: about, RelationshipCounts: counts, UpdatedProvenance: provenance,
	}
	if node.NodeType == NodeTypeEntity {
		row.entityKind = propertyString(node, "kind")
	}
	return row, nil
}

func provenanceSummaryForNodeVersion(ctx context.Context, tx *sql.Tx, projectID, nodeID string, version int) (ProvenanceSummaryV1, error) {
	var p ProvenanceSummaryV1
	var actorType string
	var taskID, continuationID, profileID, runner, migration sql.NullString
	var provenanceID string
	err := tx.QueryRowContext(ctx, `SELECT p.id,p.actor_type,p.actor_id,p.task_id,p.continuation_id,p.runtime_profile_id,p.runner,p.migration_source_json,p.recorded_at
		FROM blackboard_node_versions v
		JOIN blackboard_graph_operations o ON o.project_id=v.project_id AND o.mutation_seq=v.mutation_seq AND o.operation_index=v.operation_index
		JOIN blackboard_graph_provenance p ON p.project_id=o.project_id AND p.id=o.provenance_id
		WHERE v.project_id=? AND v.node_id=? AND v.version=?`, projectID, nodeID, version).
		Scan(&provenanceID, &actorType, &p.ActorID, &taskID, &continuationID, &profileID, &runner, &migration, &p.RecordedAt)
	if err != nil {
		return ProvenanceSummaryV1{}, fmt.Errorf("read node provenance summary: %w", err)
	}
	p.ActorType = ActorType(actorType)
	p.TaskID, p.ContinuationID, p.RuntimeProfileID, p.Runner = nullStringPointer(taskID), nullStringPointer(continuationID), nullStringPointer(profileID), nullStringPointer(runner)
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM blackboard_graph_provenance_events WHERE project_id=? AND provenance_id=?`, projectID, provenanceID).Scan(&p.SourceEventCount); err != nil {
		return ProvenanceSummaryV1{}, fmt.Errorf("count provenance events: %w", err)
	}
	if migration.Valid {
		if err := json.Unmarshal([]byte(migration.String), &p.MigrationSource); err != nil {
			return ProvenanceSummaryV1{}, fmt.Errorf("decode migration source: %w", err)
		}
	}
	return p, nil
}

func relationshipsForNode(snapshot GraphSnapshot, nodeID string) ([]NodeRefV1, RelationshipCountsV1) {
	byID := make(map[string]NodeRecord, len(snapshot.Nodes))
	for _, node := range snapshot.Nodes {
		byID[node.ID] = node
	}
	about := []NodeRefV1{}
	seenAbout := map[string]bool{}
	var counts RelationshipCountsV1
	for _, edge := range snapshot.Edges {
		if edge.State != "active" {
			continue
		}
		if edge.ToNodeID == nodeID {
			counts.Incoming++
		}
		if edge.FromNodeID == nodeID {
			counts.Outgoing++
		}
		if edge.EdgeType == EdgeTypeAbout && edge.FromNodeID == nodeID {
			counts.AboutEntities++
			if entity, ok := byID[edge.ToNodeID]; ok && len(about) < 3 && !seenAbout[entity.ID] {
				about = append(about, nodeRefForNode(entity))
				seenAbout[entity.ID] = true
			}
		}
		if edge.EdgeType == EdgeTypeEvidences && edge.ToNodeID == nodeID {
			counts.Evidence++
		}
		if edge.EdgeType == EdgeTypeContradicts && (edge.FromNodeID == nodeID || edge.ToNodeID == nodeID) {
			counts.Contradictions++
		}
	}
	sort.Slice(about, func(i, j int) bool {
		return compareStrings(about[i].StableKey, about[j].StableKey, about[i].ID, about[j].ID) < 0
	})
	return about, counts
}

func buildRecordResolve(snapshot GraphSnapshot, request RecordResolveRequest) (RecordResolveV1, error) {
	byID := make(map[string]NodeRecord, len(snapshot.Nodes))
	for _, node := range snapshot.Nodes {
		byID[node.ID] = node
	}
	requested := NodeRefV1{ID: request.NodeID, NodeType: request.NodeType, StableKey: request.StableKey}
	var canonicalID string
	var alias *string
	var merged *string
	if request.NodeID != "" {
		node, ok := byID[request.NodeID]
		if !ok {
			return RecordResolveV1{}, readValidationError(ErrCodeRecordNotFound, "record does not exist in this Project", "node_id")
		}
		canonicalID = node.ID
		for node.MergeTargetID != "" {
			from := canonicalID
			merged = &from
			canonicalID = node.MergeTargetID
			var exists bool
			node, exists = byID[canonicalID]
			if !exists {
				return RecordResolveV1{}, readValidationError(ErrCodeSnapshotUnavailable, "merged target cannot be reconstructed", "node_id")
			}
		}
	} else {
		if request.NodeType == "" || request.StableKey == "" {
			return RecordResolveV1{}, readValidationError(ErrCodeInvalidQuery, "node_type and stable_key are required", "stable_key")
		}
		for _, key := range snapshot.Keys {
			if key.NodeType == request.NodeType && key.Key == request.StableKey {
				canonicalID = key.CanonicalNodeID
				if key.Role == "alias" {
					value := request.StableKey
					alias = &value
				}
				break
			}
		}
	}
	resolved, ok := byID[canonicalID]
	if !ok {
		return RecordResolveV1{}, readValidationError(ErrCodeRecordNotFound, "record does not exist in this Project", "stable_key")
	}
	return RecordResolveV1{Requested: requested, Resolved: nodeRefForNode(resolved), ResolvedFromAlias: alias, ResolvedFromMergedID: merged}, nil
}

func buildRecordHistory(ctx context.Context, tx *sql.Tx, snapshot GraphSnapshot, request RecordHistoryRequest, cursor *readCursor, cursorKey []byte) (RecordHistoryV1, error) {
	if request.NodeID == "" {
		return RecordHistoryV1{}, readValidationError(ErrCodeInvalidQuery, "node_id is required", "node_id")
	}
	limit := request.Limit
	if limit == 0 {
		limit = 50
	}
	if limit < 1 || limit > 200 {
		return RecordHistoryV1{}, readValidationError(ErrCodeInvalidQuery, "limit must be between 1 and 200", "limit")
	}
	queryIdentity, err := canonicalJSON(struct {
		NodeID        string `json:"node_id"`
		Literal       bool   `json:"literal"`
		BeforeVersion int    `json:"before_version"`
	}{request.NodeID, request.Literal, request.BeforeVersion})
	if err != nil {
		return RecordHistoryV1{}, fmt.Errorf("encode history query: %w", err)
	}
	queryHash := hex.EncodeToString(framedHash("CyberPenda.Blackboard.ReadHistoryQuery.v1", queryIdentity))
	pageBefore := request.BeforeVersion
	if cursor != nil {
		if cursor.QueryHash != queryHash || cursor.Sort != "version_desc" || cursor.Limit != limit || len(cursor.Last) != 1 {
			return RecordHistoryV1{}, readValidationError(ErrCodeInvalidCursor, "The cursor does not match this query.", "cursor")
		}
		if _, err := fmt.Sscan(cursor.Last[0], &pageBefore); err != nil || pageBefore < 1 {
			return RecordHistoryV1{}, readValidationError(ErrCodeInvalidCursor, "The cursor is malformed.", "cursor")
		}
	}

	var current *NodeRecord
	for i := range snapshot.Nodes {
		if snapshot.Nodes[i].ID == request.NodeID {
			current = &snapshot.Nodes[i]
			break
		}
	}
	if current == nil {
		return RecordHistoryV1{}, readValidationError(ErrCodeRecordNotFound, "record does not exist in this Project", "node_id")
	}
	if current.MergeTargetID != "" && !request.Literal {
		return RecordHistoryV1{}, readValidationError(ErrCodeLiteralIdentityRequired, "literal=true is required for merged source history", "literal")
	}

	countQuery := `SELECT COUNT(*) FROM blackboard_node_versions WHERE project_id=? AND node_id=? AND result_graph_revision<=?`
	countArgs := []any{snapshot.ProjectID, request.NodeID, snapshot.GraphRevision}
	if request.BeforeVersion > 0 {
		countQuery += ` AND version<?`
		countArgs = append(countArgs, request.BeforeVersion)
	}
	var total int
	if err := tx.QueryRowContext(ctx, countQuery, countArgs...).Scan(&total); err != nil {
		return RecordHistoryV1{}, fmt.Errorf("count node history: %w", err)
	}

	query := `SELECT version,disposition,properties_json,updated_at,semantic_hash FROM blackboard_node_versions WHERE project_id=? AND node_id=? AND result_graph_revision<=?`
	args := []any{snapshot.ProjectID, request.NodeID, snapshot.GraphRevision}
	if pageBefore > 0 {
		query += ` AND version<?`
		args = append(args, pageBefore)
	}
	query += ` ORDER BY version DESC LIMIT ?`
	args = append(args, limit+1)
	rows, err := tx.QueryContext(ctx, query, args...)
	if err != nil {
		return RecordHistoryV1{}, fmt.Errorf("read node history: %w", err)
	}
	defer rows.Close()
	versions := []NodeHistoryVersion{}
	for rows.Next() {
		var version NodeHistoryVersion
		var properties string
		if err := rows.Scan(&version.Version, &version.Disposition, &properties, &version.UpdatedAt, &version.SemanticHash); err != nil {
			return RecordHistoryV1{}, fmt.Errorf("scan node history: %w", err)
		}
		if err := json.Unmarshal([]byte(properties), &version.Properties); err != nil {
			return RecordHistoryV1{}, fmt.Errorf("decode node history: %w", err)
		}
		versions = append(versions, version)
	}
	if err := rows.Err(); err != nil {
		return RecordHistoryV1{}, fmt.Errorf("iterate node history: %w", err)
	}
	next := ""
	if len(versions) > limit {
		versions = versions[:limit]
		next, err = encodeReadCursor(readCursor{Version: 1, Projection: ReadKindRecordHistoryV1, ProjectID: snapshot.ProjectID, Revision: snapshot.GraphRevision, QueryHash: queryHash, Sort: "version_desc", Limit: limit, Last: []string{fmt.Sprint(versions[len(versions)-1].Version)}}, cursorKey)
		if err != nil {
			return RecordHistoryV1{}, err
		}
	}
	keys := []GraphKey{}
	for _, key := range snapshot.Keys {
		if key.SourceNodeID == request.NodeID || key.CanonicalNodeID == request.NodeID {
			keys = append(keys, key)
		}
	}
	var merge *NodeRefV1
	if current.MergeTargetID != "" {
		for _, node := range snapshot.Nodes {
			if node.ID == current.MergeTargetID {
				ref := nodeRefForNode(node)
				merge = &ref
				break
			}
		}
	}
	return RecordHistoryV1{Record: nodeRefForNode(*current), Versions: versions, Page: PageV1{Limit: limit, TotalItems: total, NextCursor: next}, KeyHistory: keys, Merge: merge}, nil
}

func normalizedRecordQueryHash(request RecordCollectionRequest) (string, error) {
	value := struct {
		NodeTypes                                                               []NodeType    `json:"node_types"`
		Dispositions                                                            []Disposition `json:"dispositions"`
		Lifecycle, ScopeStatus, Severity, EntityKind, ActorType, HealthSeverity []string
		TaskID, ContinuationID, RuntimeProfileID, Runner, AboutEntityID         string
		EdgeType                                                                EdgeType
		Direction                                                               string
		HasEvidence, HasContradiction, Frontier                                 *bool
		UpdatedBefore, UpdatedAfter, Query                                      string
	}{request.NodeTypes, request.Dispositions, request.Lifecycle, request.ScopeStatus, request.Severity, request.EntityKind, request.ActorType, request.HealthSeverity, request.TaskID, request.ContinuationID, request.RuntimeProfileID, request.Runner, request.AboutEntityID, request.EdgeType, request.Direction, request.HasEvidence, request.HasContradiction, request.Frontier, request.UpdatedBefore, request.UpdatedAfter, request.Query}
	data, err := canonicalJSON(value)
	if err != nil {
		return "", fmt.Errorf("encode normalized record query: %w", err)
	}
	hash := framedHash("CyberPenda.Blackboard.ReadQuery.v1", data)
	return hex.EncodeToString(hash), nil
}

func encodeReadCursor(cursor readCursor, cursorKey []byte) (string, error) {
	payload, err := canonicalJSON(cursor)
	if err != nil {
		return "", fmt.Errorf("encode read cursor: %w", err)
	}
	mac := hmac.New(sha256.New, cursorKey)
	_, _ = mac.Write([]byte(readCursorDomain))
	_, _ = mac.Write(payload)
	return base64.RawURLEncoding.EncodeToString(payload) + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil)), nil
}

func decodeReadCursor(encoded string, cursorKey []byte) (readCursor, error) {
	parts := strings.Split(encoded, ".")
	if len(parts) != 2 {
		return readCursor{}, readValidationError(ErrCodeInvalidCursor, "The cursor is malformed.", "cursor")
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return readCursor{}, readValidationError(ErrCodeInvalidCursor, "The cursor is malformed.", "cursor")
	}
	signature, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return readCursor{}, readValidationError(ErrCodeInvalidCursor, "The cursor is malformed.", "cursor")
	}
	mac := hmac.New(sha256.New, cursorKey)
	_, _ = mac.Write([]byte(readCursorDomain))
	_, _ = mac.Write(payload)
	if !hmac.Equal(signature, mac.Sum(nil)) {
		return readCursor{}, readValidationError(ErrCodeInvalidCursor, "The cursor signature is invalid.", "cursor")
	}
	var cursor readCursor
	if err := json.Unmarshal(payload, &cursor); err != nil || cursor.Version != BlackboardReadProtocolVersion {
		return readCursor{}, readValidationError(ErrCodeInvalidCursor, "The cursor version is unsupported.", "cursor")
	}
	return cursor, nil
}

func hashReadEnvelope(envelope ReadEnvelope) (string, error) {
	value := struct {
		ProtocolVersion       int            `json:"protocol_version"`
		Projection            string         `json:"projection"`
		ProjectID             string         `json:"project_id"`
		ProjectKind           string         `json:"project_kind"`
		ObservedGraphRevision int            `json:"observed_graph_revision"`
		ObservedStateHash     string         `json:"observed_state_hash"`
		SourcePins            map[string]any `json:"source_pins"`
		Result                any            `json:"result"`
	}{envelope.ProtocolVersion, envelope.Projection, envelope.ProjectID, envelope.ProjectKind, envelope.ObservedGraphRevision, envelope.ObservedStateHash, envelope.SourcePins, envelope.Result}
	data, err := canonicalJSON(value)
	if err != nil {
		return "", fmt.Errorf("encode read projection: %w", err)
	}
	return hex.EncodeToString(framedHash("CyberPenda.Blackboard.ReadProjection.v1", data)), nil
}

func normalizeSearchText(value string) string {
	value = norm.NFKC.String(value)
	value = strings.ToLower(value)
	return strings.Join(strings.FieldsFunc(value, unicode.IsSpace), " ")
}

func matchesLexicalQuery(node NodeRecord, query string) bool {
	return lexicalSearchRank(node, query) < 6
}

func lexicalSearchRank(node NodeRecord, query string) int {
	if query == "" {
		return 0
	}
	key := normalizeSearchText(node.StableKey)
	locator, _ := stringProperty(node.PropertyMap, "locator")
	locator = normalizeSearchText(locator)
	label := normalizeSearchText(labelForNode(node))
	if key == query || (locator != "" && locator == query) {
		return 0
	}
	if strings.HasPrefix(key, query) || (locator != "" && strings.HasPrefix(locator, query)) {
		return 1
	}
	if label == query {
		return 2
	}
	if strings.HasPrefix(label, query) {
		return 3
	}
	joined := normalizeSearchText(strings.Join(searchableFields(node), " "))
	tokens := strings.Fields(query)
	all := true
	any := false
	for _, token := range tokens {
		matched := strings.Contains(joined, token)
		all = all && matched
		any = any || matched
	}
	if all {
		return 4
	}
	if any {
		return 5
	}
	return 6
}

func searchableFields(node NodeRecord) []string {
	fields := []string{node.StableKey}
	for _, key := range []string{"name", "locator", "objective", "summary", "detail", "statement", "rationale", "category", "body", "title", "target", "description", "proof", "impact", "recommendation", "value", "media_type", "digest", "directive"} {
		if value, ok := stringProperty(node.PropertyMap, key); ok {
			fields = append(fields, value)
		}
	}
	if node.NodeType == NodeTypeEvidenceArtifact {
		if value, ok := stringProperty(node.PropertyMap, "managed_path"); ok {
			if index := strings.LastIndexAny(value, `/\\`); index >= 0 {
				value = value[index+1:]
			}
			fields = append(fields, value)
		}
	}
	return fields
}

func nodeRefForNode(node NodeRecord) NodeRefV1 {
	return NodeRefV1{ID: node.ID, NodeType: node.NodeType, StableKey: node.StableKey, Label: labelForNode(node)}
}

func labelForNode(node NodeRecord) string {
	keys := map[NodeType][]string{
		NodeTypeGoal: {"text"}, NodeTypeEntity: {"name"}, NodeTypeExplorationObjective: {"objective"},
		NodeTypeAttempt: {"summary"}, NodeTypeObservation: {"summary"}, NodeTypeHypothesis: {"statement"},
		NodeTypeProjectFact: {"summary"}, NodeTypeFinding: {"title"}, NodeTypeSolution: {"summary"},
		NodeTypeEvidenceArtifact: {"summary"}, NodeTypeProjectDirective: {"directive"},
	}
	for _, key := range keys[node.NodeType] {
		if value, ok := stringProperty(node.PropertyMap, key); ok && value != "" {
			if node.NodeType == NodeTypeEntity {
				if locator, ok := stringProperty(node.PropertyMap, "locator"); ok && locator != "" && locator != value {
					return value + " — " + locator
				}
			}
			return value
		}
	}
	return node.StableKey
}

func lifecycleForNode(node NodeRecord) (string, string) {
	var field string
	switch node.NodeType {
	case NodeTypeGoal:
		field = "task_status"
	case NodeTypeExplorationObjective, NodeTypeAttempt, NodeTypeHypothesis, NodeTypeFinding, NodeTypeSolution, NodeTypeEvidenceArtifact, NodeTypeProjectDirective:
		field = "status"
	case NodeTypeProjectFact:
		field = "confidence"
	default:
		if node.Disposition != DispositionMain {
			return "disposition", string(node.Disposition)
		}
	}
	value, _ := stringProperty(node.PropertyMap, field)
	return field, value
}

func secondaryForNode(node NodeRecord) string {
	for _, key := range []string{"locator", "target", "category", "media_type", "kind"} {
		if value, ok := stringProperty(node.PropertyMap, key); ok {
			return value
		}
	}
	return ""
}

func stringProperty(properties map[string]any, key string) (string, bool) {
	value, ok := properties[key].(string)
	return value, ok
}

func sortRecordRows(rows []NodeRowV1, mode string) {
	sort.SliceStable(rows, func(i, j int) bool { return compareRowTuple(rowTuple(rows[i], mode), rowTuple(rows[j], mode)) < 0 })
}

func rowTuple(row NodeRowV1, mode string) []string {
	switch mode {
	case recordSortRelevance:
		return []string{fmt.Sprintf("%02d", row.searchRank), string(row.Ref.NodeType), row.Ref.StableKey, row.Ref.ID}
	case RecordSortCreatedAsc:
		return []string{row.UpdatedAt, string(row.Ref.NodeType), row.Ref.StableKey, row.Ref.ID}
	case RecordSortUpdatedDesc:
		return []string{invertLexical(row.UpdatedAt), string(row.Ref.NodeType), row.Ref.StableKey, row.Ref.ID}
	case RecordSortAttention:
		return []string{fmt.Sprintf("%02d", row.attentionRank), invertLexical(row.UpdatedAt), fmt.Sprintf("%02d", nodeTypeOrdinal(row.Ref.NodeType)), row.Ref.StableKey, row.Ref.ID}
	case RecordSortSeverity:
		severity := ""
		if row.Severity != nil {
			severity = severityRank(*row.Severity)
		}
		return []string{severity, invertLexical(row.UpdatedAt), string(row.Ref.NodeType), row.Ref.StableKey, row.Ref.ID}
	default:
		return []string{row.Ref.StableKey, string(row.Ref.NodeType), row.Ref.ID}
	}
}

func invertLexical(value string) string {
	out := make([]byte, len(value))
	for i := range value {
		out[i] = 127 - value[i]
	}
	return string(out)
}

func severityRank(value string) string {
	ranks := map[string]string{"critical": "0", "high": "1", "medium": "2", "low": "3", "info": "4", "none": "5"}
	if rank, ok := ranks[value]; ok {
		return rank
	}
	return "9"
}

func compareRowTuple(left, right []string) int {
	length := len(left)
	if len(right) < length {
		length = len(right)
	}
	for i := 0; i < length; i++ {
		if left[i] < right[i] {
			return -1
		}
		if left[i] > right[i] {
			return 1
		}
	}
	if len(left) < len(right) {
		return -1
	}
	if len(left) > len(right) {
		return 1
	}
	return 0
}

func compareStrings(values ...string) int {
	for i := 0; i+1 < len(values); i += 2 {
		if values[i] < values[i+1] {
			return -1
		}
		if values[i] > values[i+1] {
			return 1
		}
	}
	return 0
}

func recordFacets(rows []NodeRowV1) map[string]any {
	nodeTypes, dispositions, lifecycle, scopeStatus, severity, entityKind, actorType := map[string]int{}, map[string]int{}, map[string]int{}, map[string]int{}, map[string]int{}, map[string]int{}, map[string]int{}
	for _, row := range rows {
		nodeTypes[string(row.Ref.NodeType)]++
		dispositions[string(row.Disposition)]++
		if row.Lifecycle != nil {
			lifecycle[row.Lifecycle.Value]++
		}
		if row.ScopeStatus != nil {
			scopeStatus[*row.ScopeStatus]++
		}
		if row.Severity != nil {
			severity[*row.Severity]++
		}
		if row.entityKind != "" {
			entityKind[row.entityKind]++
		}
		actorType[string(row.UpdatedProvenance.ActorType)]++
	}
	return map[string]any{"node_type": nodeTypes, "disposition": dispositions, "lifecycle": lifecycle, "scope_status": scopeStatus, "severity": severity, "entity_kind": entityKind, "actor_type": actorType}
}

func sortedUniqueNodeTypes(values []NodeType) []NodeType {
	set := map[NodeType]bool{}
	for _, value := range values {
		set[value] = true
	}
	out := make([]NodeType, 0, len(set))
	for value := range set {
		out = append(out, value)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

func sortedUniqueDispositions(values []Disposition) []Disposition {
	set := map[Disposition]bool{}
	for _, value := range values {
		set[value] = true
	}
	out := make([]Disposition, 0, len(set))
	for value := range set {
		out = append(out, value)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

func sortedUniqueStrings(values []string) []string {
	set := map[string]bool{}
	for _, value := range values {
		set[value] = true
	}
	out := make([]string, 0, len(set))
	for value := range set {
		out = append(out, value)
	}
	sort.Strings(out)
	return out
}

func containsNodeType(values []NodeType, target NodeType) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}
func containsDisposition(values []Disposition, target Disposition) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}
func containsString(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

const (
	ErrCodeInvalidQuery            = "invalid_query"
	ErrCodeInvalidCursor           = "invalid_cursor"
	ErrCodeRevisionNotFound        = "revision_not_found"
	ErrCodeRecordNotFound          = "record_not_found"
	ErrCodeLiteralIdentityRequired = "literal_identity_required"
	ErrCodeProjectionTooLarge      = "projection_too_large"
	ErrCodeProjectKindMismatch     = "project_kind_mismatch"
	ErrCodeHealthRunNotFound       = "health_run_not_found"
	ErrCodeHealthRunInProgress     = "health_run_in_progress"
	ErrCodeSnapshotUnavailable     = "snapshot_unavailable"
)

func readValidationError(code, message, path string) *ValidationError {
	return validationError(code, message, -1, "", path)
}

func buildBlackboardWork(ctx context.Context, tx *sql.Tx, snapshot GraphSnapshot) (BlackboardWorkViewV1, error) {
	rows := make([]NodeRowV1, 0, len(snapshot.Nodes))
	frontierIDs := make(map[string]bool)
	for _, id := range historicalFrontierNodeIDs(snapshot) {
		frontierIDs[id] = true
	}
	healthRanks, _, err := healthSubjectRanks(ctx, tx, snapshot.ProjectID, snapshot.GraphRevision, "")
	if err != nil {
		return BlackboardWorkViewV1{}, err
	}
	summary := BlackboardWorkSummaryV1{GraphRevision: snapshot.GraphRevision, NodeCounts: map[string]int{}, EdgeCounts: map[string]int{}}
	for _, edge := range snapshot.Edges {
		if edge.State == "active" {
			summary.EdgeCounts[string(edge.EdgeType)]++
		}
	}
	for _, node := range snapshot.Nodes {
		if node.Disposition != DispositionMain {
			continue
		}
		summary.NodeCounts[string(node.NodeType)]++
		row, err := nodeRowAt(ctx, tx, snapshot, node)
		if err != nil {
			return BlackboardWorkViewV1{}, err
		}
		row.attentionRank = workAttentionRank(node, frontierIDs[node.ID], healthRanks[node.ID])
		rows = append(rows, row)
		switch node.NodeType {
		case NodeTypeProjectFact:
			if node.PropertyMap["confidence"] == "confirmed" || node.PropertyMap["confidence"] == "tentative" {
				summary.CurrentTruth++
			}
		case NodeTypeAttempt:
			if node.PropertyMap["status"] == "open" {
				summary.OpenAttempts++
			}
		case NodeTypeFinding:
			if node.PropertyMap["status"] == "confirmed" {
				summary.ConfirmedFindings++
			}
			if node.PropertyMap["status"] == "unconfirmed" {
				summary.UnconfirmedFindings++
			}
		case NodeTypeSolution:
			if node.PropertyMap["status"] == "verified" {
				summary.VerifiedSolutions++
			}
		case NodeTypeEvidenceArtifact:
			if node.PropertyMap["status"] == "missing" {
				summary.EvidenceMissing++
			}
		}
	}
	summary.Frontier = len(frontierIDs)
	doc, err := canonicalMainGraphDocumentAt(ctx, tx, snapshot.ProjectID, snapshot.GraphRevision)
	if err != nil {
		return BlackboardWorkViewV1{}, err
	}
	projection, err := measureCanonicalMainGraphDocument(snapshot.ProjectID, snapshot.GraphRevision, doc)
	if err != nil {
		return BlackboardWorkViewV1{}, err
	}
	summary.Budget = BlackboardWorkBudgetV1{State: budgetStateForEstimatedTokens(projection.EstimatedTokens), ProjectionBytes: projection.ByteCount, EstimatedTokens: projection.EstimatedTokens, TargetTokens: budgetTargetTokens, WarningTokens: budgetWarningTokens, RequiredTokens: budgetRequiredTokens}
	summary.Health, err = dashboardHealth(ctx, tx, snapshot.ProjectID, snapshot.GraphRevision)
	if err != nil {
		return BlackboardWorkViewV1{}, err
	}
	attention := append([]NodeRowV1(nil), rows...)
	sortRecordRows(attention, RecordSortAttention)
	frontier := filterRows(rows, func(row NodeRowV1) bool { return frontierIDs[row.Ref.ID] })
	active := filterRows(rows, func(row NodeRowV1) bool {
		return row.Ref.NodeType == NodeTypeAttempt && row.Lifecycle != nil && row.Lifecycle.Value == "open"
	})
	recent, err := semanticChangesAt(ctx, tx, snapshot, rows, 20)
	if err != nil {
		return BlackboardWorkViewV1{}, err
	}
	return BlackboardWorkViewV1{Summary: summary, Attention: previewCollection(attention, 20), Frontier: previewCollection(frontier, 20), ActiveAttempts: previewCollection(active, 20), RecentChanges: recent, Facets: recordFacets(rows)}, nil
}

func healthSubjectRanks(ctx context.Context, tx *sql.Tx, projectID string, revision int, pinnedRunID string) (map[string]int, string, error) {
	runID := pinnedRunID
	if runID != "" {
		var checked int
		if err := tx.QueryRowContext(ctx, `SELECT checked_graph_revision FROM blackboard_health_runs WHERE project_id=? AND run_id=? AND completed_at IS NOT NULL`, projectID, runID).Scan(&checked); errors.Is(err, sql.ErrNoRows) {
			return nil, "", readValidationError(ErrCodeSnapshotUnavailable, "The pinned Health run is unavailable.", "cursor")
		} else if err != nil {
			return nil, "", err
		} else if checked > revision {
			return nil, "", readValidationError(ErrCodeInvalidCursor, "The cursor Health pin is newer than its graph revision.", "cursor")
		}
	} else {
		err := tx.QueryRowContext(ctx, `SELECT run_id FROM blackboard_health_runs WHERE project_id=? AND checked_graph_revision<=? AND completed_at IS NOT NULL ORDER BY started_at DESC,rowid DESC LIMIT 1`, projectID, revision).Scan(&runID)
		if errors.Is(err, sql.ErrNoRows) {
			return map[string]int{}, "", nil
		}
		if err != nil {
			return nil, "", fmt.Errorf("read latest Health run for Work: %w", err)
		}
	}
	rows, err := tx.QueryContext(ctx, `SELECT subject_id,severity FROM blackboard_health_results WHERE project_id=? AND run_id=? ORDER BY fingerprint`, projectID, runID)
	if err != nil {
		return nil, "", fmt.Errorf("read Health subjects for Work: %w", err)
	}
	defer rows.Close()
	ranks := map[string]int{}
	for rows.Next() {
		var id, severity string
		if err := rows.Scan(&id, &severity); err != nil {
			return nil, "", err
		}
		rank := 99
		if severity == "critical" {
			rank = 1
		} else if severity == "warning" {
			rank = 2
		} else if severity == "info" {
			rank = 3
		}
		if old, ok := ranks[id]; !ok || rank < old {
			ranks[id] = rank
		}
	}
	return ranks, runID, rows.Err()
}
func stringSourcePins(key, value string) map[string]string {
	if value == "" {
		return nil
	}
	return map[string]string{key: value}
}
func anySourcePins(key, value string) map[string]any {
	if value == "" {
		return nil
	}
	return map[string]any{key: value}
}

func workAttentionRank(node NodeRecord, frontier bool, healthRank int) int {
	if healthRank == 1 || healthRank == 2 {
		return healthRank
	}
	if frontier {
		return 3
	}
	switch node.NodeType {
	case NodeTypeAttempt:
		if node.PropertyMap["status"] == "open" {
			return 4
		}
	case NodeTypeFinding:
		if node.PropertyMap["status"] == "confirmed" {
			return 5
		}
	case NodeTypeSolution:
		if node.PropertyMap["status"] == "verified" || node.PropertyMap["status"] == "candidate" {
			return 6
		}
	case NodeTypeProjectFact:
		if node.PropertyMap["confidence"] == "confirmed" {
			return 7
		}
		if node.PropertyMap["confidence"] == "tentative" {
			return 8
		}
	case NodeTypeHypothesis:
		return 9
	case NodeTypeObservation:
		return 10
	case NodeTypeEvidenceArtifact:
		return 11
	case NodeTypeEntity:
		return 12
	case NodeTypeProjectDirective:
		return 13
	}
	return 14
}

func filterRows(rows []NodeRowV1, keep func(NodeRowV1) bool) []NodeRowV1 {
	out := []NodeRowV1{}
	for _, row := range rows {
		if keep(row) {
			out = append(out, row)
		}
	}
	return out
}

func previewCollection(rows []NodeRowV1, limit int) RecordCollectionV1 {
	total := len(rows)
	if len(rows) > limit {
		rows = rows[:limit]
	}
	return RecordCollectionV1{Items: rows, Facets: recordFacets(rows), Page: PageV1{Limit: limit, TotalItems: total}}
}

func recordRowMatches(snapshot GraphSnapshot, node NodeRecord, row NodeRowV1, request RecordCollectionRequest, frontier bool, healthRank int) bool {
	if len(request.ScopeStatus) > 0 {
		if row.ScopeStatus == nil || !containsString(request.ScopeStatus, *row.ScopeStatus) {
			return false
		}
	}
	if len(request.Severity) > 0 {
		if row.Severity == nil || !containsString(request.Severity, *row.Severity) {
			return false
		}
	}
	if len(request.EntityKind) > 0 && (node.NodeType != NodeTypeEntity || !containsString(request.EntityKind, propertyString(node, "kind"))) {
		return false
	}
	if len(request.ActorType) > 0 && !containsString(request.ActorType, string(row.UpdatedProvenance.ActorType)) {
		return false
	}
	if request.TaskID != "" && (row.UpdatedProvenance.TaskID == nil || *row.UpdatedProvenance.TaskID != request.TaskID) {
		return false
	}
	if request.ContinuationID != "" && (row.UpdatedProvenance.ContinuationID == nil || *row.UpdatedProvenance.ContinuationID != request.ContinuationID) {
		return false
	}
	if request.RuntimeProfileID != "" && (row.UpdatedProvenance.RuntimeProfileID == nil || *row.UpdatedProvenance.RuntimeProfileID != request.RuntimeProfileID) {
		return false
	}
	if request.Runner != "" && (row.UpdatedProvenance.Runner == nil || *row.UpdatedProvenance.Runner != request.Runner) {
		return false
	}
	if request.AboutEntityID != "" && !hasActiveEdge(snapshot, EdgeTypeAbout, node.ID, request.AboutEntityID) {
		return false
	}
	if request.EdgeType != "" {
		matched := false
		for _, edge := range snapshot.Edges {
			if edge.State != "active" || edge.EdgeType != request.EdgeType {
				continue
			}
			if (request.Direction == "incoming" || request.Direction == "either") && edge.ToNodeID == node.ID {
				matched = true
			}
			if (request.Direction == "outgoing" || request.Direction == "either") && edge.FromNodeID == node.ID {
				matched = true
			}
		}
		if !matched {
			return false
		}
	}
	if request.HasEvidence != nil && (row.RelationshipCounts.Evidence > 0) != *request.HasEvidence {
		return false
	}
	if request.HasContradiction != nil && (row.RelationshipCounts.Contradictions > 0) != *request.HasContradiction {
		return false
	}
	if request.Frontier != nil && frontier != *request.Frontier {
		return false
	}
	if len(request.HealthSeverity) > 0 {
		severity := ""
		if healthRank == 1 {
			severity = "critical"
		} else if healthRank == 2 {
			severity = "warning"
		} else if healthRank == 3 {
			severity = "info"
		}
		if !containsString(request.HealthSeverity, severity) {
			return false
		}
	}
	if request.UpdatedBefore != "" && row.UpdatedAt >= request.UpdatedBefore {
		return false
	}
	if request.UpdatedAfter != "" && row.UpdatedAt <= request.UpdatedAfter {
		return false
	}
	return true
}
