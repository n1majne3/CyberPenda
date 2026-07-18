package blackboard

import (
	"context"
	"time"
)

type BudgetState string

const (
	BudgetWithinTarget BudgetState = "within_target"
	BudgetAboveTarget              = "above_target"
	BudgetWarning                  = "warning"
	BudgetRequired                 = "required"
	BudgetUnknown                  = "unknown"

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
	_ = blocked
	_ = compacted
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
	_ = blocked
	_ = compacted
	return nil
}

// BudgetStateForEstimatedTokens classifies the provider-neutral canonical budget estimate.
func BudgetStateForEstimatedTokens(tokens int) BudgetState {
	return budgetStateForEstimatedTokens(tokens)
}
