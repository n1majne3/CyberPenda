package blackboard

import (
	"context"
	"time"
)

type BudgetState string
type HealthStatus string
type HealthSeverity string

const (
	BudgetWithinTarget BudgetState = "within_target"
	BudgetAboveTarget              = "above_target"
	BudgetWarning                  = "warning"
	BudgetRequired                 = "required"
	BudgetUnknown                  = "unknown"

	HealthStatusHealthy   HealthStatus = "healthy"
	HealthStatusAttention              = "attention"
	HealthStatusDegraded               = "degraded"
	HealthStatusCritical               = "critical"
	HealthStatusUnknown                = "unknown"

	blackboardHealthCheckerV1  = "blackboard_health_v1"
	blackboardCompactorActorID = "blackboard-compactor"
	budgetTargetTokens         = 12_000
	budgetAboveTargetTokens    = 12_001
	budgetWarningTokens        = 16_000
	budgetRequiredTokens       = 20_000
)

type CompactionOptions struct {
	OverrideRestoreHold bool `json:"override_restore_hold"`
}

type CompactionPlan struct {
	ProjectID                  string         `json:"project_id"`
	BaseGraphRevision          int            `json:"base_graph_revision"`
	BeforeHash                 string         `json:"before_hash"`
	BeforeBytes                int            `json:"before_bytes"`
	BeforeTokens               int            `json:"before_tokens"`
	ExpectedNodeVersions       map[string]int `json:"expected_node_versions"`
	ExpectedEdgeVersions       map[string]int `json:"expected_edge_versions"`
	ArchiveNodeIDs             []string       `json:"archive_node_ids"`
	RetireEdgeIDs              []string       `json:"retire_edge_ids"`
	PreservedAnchorIDs         []string       `json:"preserved_anchor_ids"`
	SimulatedAfterHash         string         `json:"simulated_after_hash"`
	SimulatedAfterTokens       int            `json:"simulated_after_tokens"`
	Rationale                  []string       `json:"rationale"`
	EligibleComponentCount     int            `json:"eligible_component_count"`
	ProtectedEstimatedTokens   int            `json:"protected_estimated_tokens"`
	ReclaimableEstimatedTokens int            `json:"reclaimable_estimated_tokens"`
}

type CompactionManifest struct {
	ID                  string   `json:"id"`
	BaseGraphRevision   int      `json:"base_graph_revision"`
	ResultGraphRevision int      `json:"result_graph_revision"`
	BeforeHash          string   `json:"before_hash"`
	AfterHash           string   `json:"after_hash"`
	BeforeBytes         int      `json:"before_bytes"`
	AfterBytes          int      `json:"after_bytes"`
	BeforeTokens        int      `json:"before_tokens"`
	AfterTokens         int      `json:"after_tokens"`
	ArchivedNodeIDs     []string `json:"archived_node_ids"`
	RetiredEdgeIDs      []string `json:"retired_edge_ids"`
	PreservedAnchorIDs  []string `json:"preserved_anchor_ids"`
	MutationID          string   `json:"mutation_id"`
	CreatedAt           string   `json:"created_at"`
}

type HealthMetrics struct {
	ProjectionBytes                  int            `json:"projection_bytes"`
	EstimatedTokens                  int            `json:"estimated_tokens"`
	BudgetState                      BudgetState    `json:"budget_state"`
	ProtectedNodeCount               int            `json:"protected_node_count"`
	EligibleComponentCount           int            `json:"eligible_component_count"`
	NodeCounts                       map[string]int `json:"node_counts"`
	ActiveEdgeCount                  int            `json:"active_edge_count"`
	RetiredEdgeCount                 int            `json:"retired_edge_count"`
	CurrentTruthCount                int            `json:"current_truth_count"`
	FrontierCount                    int            `json:"frontier_count"`
	LastCompactionRevision           int            `json:"last_compaction_revision"`
	ProtectedEstimatedTokens         int            `json:"protected_estimated_tokens"`
	ReclaimableEstimatedTokens       int            `json:"reclaimable_estimated_tokens"`
	OrphanCount                      int            `json:"orphan_count"`
	MissingEvidenceCount             int            `json:"missing_evidence_count"`
	DuplicateCandidateCount          int            `json:"duplicate_candidate_count"`
	UnresolvedContradictionCount     int            `json:"unresolved_contradiction_count"`
	StrandedObjectiveCount           int            `json:"stranded_objective_count"`
	HistoryHash                      string         `json:"history_hash"`
	StateHash                        string         `json:"state_hash"`
	ProjectionHash                   string         `json:"projection_hash"`
	OpenAttemptsOnEndedContinuations int            `json:"open_attempts_on_ended_continuations"`
	MaxReconciliationAgeSeconds      int            `json:"max_reconciliation_age_seconds"`
	ArtifactScanFingerprint          string         `json:"artifact_scan_fingerprint"`
}

type HealthResult struct {
	Fingerprint string         `json:"fingerprint"`
	Code        string         `json:"code"`
	Severity    HealthSeverity `json:"severity"`
	SubjectKind string         `json:"subject_kind"`
	SubjectID   string         `json:"subject_id"`
	Details     map[string]any `json:"details"`
}

type HealthRun struct {
	RunID                   string         `json:"run_id"`
	ProjectID               string         `json:"project_id"`
	CheckedGraphRevision    int            `json:"checked_graph_revision"`
	CheckedStateHash        string         `json:"checked_state_hash"`
	CheckedProjectionHash   string         `json:"checked_projection_hash"`
	Status                  HealthStatus   `json:"status"`
	StartedAt               string         `json:"started_at"`
	CompletedAt             string         `json:"completed_at"`
	Metrics                 HealthMetrics  `json:"metrics"`
	Results                 []HealthResult `json:"results"`
	Stale                   bool           `json:"stale"`
	RunStatus               string         `json:"run_status,omitempty"`
	ArtifactScanStatus      string         `json:"artifact_scan_status,omitempty"`
	ArtifactScanFingerprint string         `json:"artifact_scan_fingerprint,omitempty"`
}

func budgetStateForEstimatedTokens(tokens int) BudgetState {
	switch {
	case tokens >= budgetRequiredTokens:
		return BudgetRequired
	case tokens >= budgetWarningTokens:
		return BudgetWarning
	case tokens >= budgetAboveTargetTokens:
		return BudgetAboveTarget
	default:
		return BudgetWithinTarget
	}
}

func (s *GraphService) postCommitMaintenance(ctx context.Context, batch MutationBatch, result MutationResult) {
	if batch.Context.ActorID == blackboardCompactorActorID {
		return
	}
	projection, err := s.remeasureCanonicalMainGraphAt(ctx, batch.Context.ProjectID, result.RecordedAt)
	if err != nil {
		_ = s.persistUnknownHealth(context.Background(), batch.Context.ProjectID, result.GraphRevision, result.RecordedAt, err)
		return
	}
	blocked, compacted := false, false
	if projection.EstimatedTokens >= budgetRequiredTokens && mutationKindForBatch(batch) != "restore" {
		plan, planErr := s.PlanCompaction(ctx, batch.Context.ProjectID)
		if planErr == nil && len(plan.ArchiveNodeIDs) > 0 {
			_, planErr = s.ApplyCompaction(ctx, plan)
			if planErr == nil {
				compacted = true
				projection, planErr = s.remeasureCanonicalMainGraphAt(ctx, batch.Context.ProjectID, result.RecordedAt)
			}
		}
		if planErr != nil || projection.EstimatedTokens >= budgetRequiredTokens {
			blocked = true
		}
	}
	if _, healthErr := s.runHealth(ctx, batch.Context.ProjectID, projection, blocked, false, compacted || mutationKindForBatch(batch) == "restore" || blocked, result.RecordedAt); healthErr != nil {
		_ = s.persistUnknownHealth(context.Background(), batch.Context.ProjectID, result.GraphRevision, result.RecordedAt, healthErr)
	}
}

// PrepareContinuationSnapshot reruns required budget maintenance immediately
// before a Continuation pin. Health/compaction failures are diagnosed but do
// not substitute a partial graph or block launch solely because of Health.
func (s *GraphService) PrepareContinuationSnapshot(ctx context.Context, projectID string) error {
	checkedAt := s.clock.Now().UTC().Format(time.RFC3339Nano)
	projection, err := s.remeasureCanonicalMainGraphAt(ctx, projectID, checkedAt)
	if err != nil {
		return err
	}
	blocked, compacted := false, false
	if projection.EstimatedTokens >= budgetRequiredTokens {
		plan, planErr := s.PlanCompaction(ctx, projectID)
		if planErr == nil && len(plan.ArchiveNodeIDs) > 0 {
			_, planErr = s.ApplyCompaction(ctx, plan)
			if planErr == nil {
				compacted = true
				projection, planErr = s.remeasureCanonicalMainGraphAt(ctx, projectID, checkedAt)
			}
		}
		if planErr != nil || projection.EstimatedTokens >= budgetRequiredTokens {
			blocked = true
		}
	}
	if _, healthErr := s.runHealth(ctx, projectID, projection, blocked, false, compacted || blocked, checkedAt); healthErr != nil {
		_ = s.persistUnknownHealth(context.Background(), projectID, projection.GraphRevision, checkedAt, healthErr)
	}
	return nil
}

// BudgetStateForEstimatedTokens classifies the provider-neutral canonical budget estimate.
func BudgetStateForEstimatedTokens(tokens int) BudgetState {
	return budgetStateForEstimatedTokens(tokens)
}
