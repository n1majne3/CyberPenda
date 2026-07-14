package task

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

// OpenAttemptRef identifies one canonical main Attempt that prevents a
// Continuation from finishing.
type OpenAttemptRef struct {
	ID        string `json:"id"`
	NodeType  string `json:"node_type"`
	StableKey string `json:"stable_key"`
}

// OpenAttemptsError reports the sorted Attempts that are still open for the
// Continuation whose Runtime provenance created them.
type OpenAttemptsError struct {
	Attempts []OpenAttemptRef
}

func (e *OpenAttemptsError) Error() string { return "continuation has open Attempts" }

var (
	// ErrContinuationFinishConflict is returned when a Finish idempotency key
	// is reused with a changed summary or Objective Outcome.
	ErrContinuationFinishConflict = errors.New("continuation Finish request conflicts with the stored request")
	// ErrContinuationWriteClosed is returned when the bound grant was already
	// finished, revoked, or marked terminal by reconciliation.
	ErrContinuationWriteClosed = errors.New("continuation grant is closed to new writes")
	// ErrContinuationFinishMarkerMismatch is returned when the durable Finish
	// marker no longer matches its Summary or recorded graph position.
	ErrContinuationFinishMarkerMismatch = errors.New("continuation Finish marker does not match its recorded graph position")
)

// FinishContinuationRequest is the trusted input to the Task-domain Finish
// transaction. IDs, graph position, actor, and time are supplied by the
// project-interface orchestration layer, never by the Runtime request body.
type FinishContinuationRequest struct {
	ProjectID                  string
	TaskID                     string
	ContinuationID             string
	GrantID                    string
	SummaryVersionID           string
	Summary                    string
	ObjectiveOutcomeJSON       []byte
	ObjectiveOutcome           *FinishObjectiveOutcome
	SubmittedBy                string
	IdempotencyKey             string
	RequestHash                string
	BlackboardGraphRevision    int
	BlackboardMutationSequence int
	FinishedAt                 time.Time
}

// FinishNodeRef is the Task domain's graph reference used to validate a
// Finish Objective Outcome inside the same writer transaction.
type FinishNodeRef struct {
	ID        string
	NodeType  string
	StableKey string
}

type FinishObjectiveOutcome struct {
	Objective          FinishNodeRef
	Status             string
	SupportingNodeRefs []FinishNodeRef
}

// ObjectiveOutcomeError reports a semantic Objective Outcome violation.
type ObjectiveOutcomeError struct {
	Path    string
	Message string
}

func (e *ObjectiveOutcomeError) Error() string { return e.Message }

// FinishContinuationResult is the durable Finish marker. Replayed is true
// when the exact stored request was returned without another write.
type FinishContinuationResult struct {
	SummaryVersion SummaryVersion
	Replayed       bool
}

// FinishContinuation stores a Continuation-bound Task Summary Version, records
// the current graph position, and closes the grant in one immediate SQLite
// writer transaction. It checks an exact replay before any new-write gate.
func (s *Service) FinishContinuation(ctx context.Context, req FinishContinuationRequest) (FinishContinuationResult, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return FinishContinuationResult{}, fmt.Errorf("begin Continuation Finish: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	stored, found, err := readFinishSummary(ctx, tx, req.ContinuationID, req.IdempotencyKey)
	if err != nil {
		return FinishContinuationResult{}, err
	}
	if found {
		if stored.FinishRequestHash != req.RequestHash {
			return FinishContinuationResult{}, ErrContinuationFinishConflict
		}
		return FinishContinuationResult{SummaryVersion: stored, Replayed: true}, nil
	}

	var durableProjectID, durableTaskID string
	if err := tx.QueryRowContext(ctx, `
		SELECT t.project_id,c.task_id
		FROM task_continuations c JOIN tasks t ON t.id=c.task_id
		WHERE c.id=?`, req.ContinuationID).Scan(&durableProjectID, &durableTaskID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return FinishContinuationResult{}, ErrNotFound
		}
		return FinishContinuationResult{}, fmt.Errorf("load Finish Continuation: %w", err)
	}
	if durableProjectID != req.ProjectID || durableTaskID != req.TaskID {
		return FinishContinuationResult{}, ErrNotFound
	}

	openAttempts, err := continuationOpenAttempts(ctx, tx, req.ProjectID, req.TaskID, req.ContinuationID)
	if err != nil {
		return FinishContinuationResult{}, err
	}
	if len(openAttempts) > 0 {
		return FinishContinuationResult{}, &OpenAttemptsError{Attempts: openAttempts}
	}
	if req.ObjectiveOutcome != nil {
		if err := validateFinishObjectiveOutcome(ctx, tx, req.ProjectID, req.TaskID, *req.ObjectiveOutcome); err != nil {
			return FinishContinuationResult{}, err
		}
	}
	if err := tx.QueryRowContext(ctx, `
		SELECT current_graph_revision
		FROM blackboard_graph_state WHERE project_id=?`, req.ProjectID).Scan(
		&req.BlackboardGraphRevision,
	); errors.Is(err, sql.ErrNoRows) {
		req.BlackboardGraphRevision = 0
	} else if err != nil {
		return FinishContinuationResult{}, fmt.Errorf("read Finish graph position: %w", err)
	}
	if err := tx.QueryRowContext(ctx, `
		SELECT COALESCE(MAX(o.mutation_seq),0)
		FROM blackboard_graph_operations o
		JOIN blackboard_graph_provenance p ON p.project_id=o.project_id AND p.id=o.provenance_id
		WHERE o.project_id=? AND p.task_id=? AND p.continuation_id=?`,
		req.ProjectID, req.TaskID, req.ContinuationID,
	).Scan(&req.BlackboardMutationSequence); err != nil {
		return FinishContinuationResult{}, fmt.Errorf("read latest Runtime mutation sequence: %w", err)
	}

	var maxVersion sql.NullInt64
	if err := tx.QueryRowContext(ctx, `SELECT MAX(version) FROM task_summary_versions WHERE task_id=?`, req.TaskID).Scan(&maxVersion); err != nil {
		return FinishContinuationResult{}, fmt.Errorf("read max Task Summary Version: %w", err)
	}
	finishedAt := req.FinishedAt.UTC()
	objectiveOutcome := string(req.ObjectiveOutcomeJSON)
	summary := SummaryVersion{
		ID: req.SummaryVersionID, TaskID: req.TaskID, ContinuationID: req.ContinuationID,
		Version: int(maxVersion.Int64) + 1, Summary: req.Summary,
		BlackboardGraphRevision:    req.BlackboardGraphRevision,
		BlackboardMutationSequence: req.BlackboardMutationSequence,
		FinishIdempotencyKey:       req.IdempotencyKey, FinishRequestHash: req.RequestHash,
		SubmittedBy: req.SubmittedBy, CreatedAt: finishedAt,
	}
	if objectiveOutcome != "" {
		summary.ObjectiveOutcome = json.RawMessage(objectiveOutcome)
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO task_summary_versions (
			id,task_id,continuation_id,version,summary,objective_outcome_json,
			blackboard_graph_revision,blackboard_mutation_sequence,
			finish_idempotency_key,finish_request_hash,submitted_by,created_at
		) VALUES (?,?,?,?,?,?,?,?,?,?,?,?)`,
		summary.ID, summary.TaskID, summary.ContinuationID, summary.Version,
		summary.Summary, objectiveOutcome, summary.BlackboardGraphRevision,
		summary.BlackboardMutationSequence, summary.FinishIdempotencyKey,
		summary.FinishRequestHash, summary.SubmittedBy, finishedAt.Format(time.RFC3339Nano),
	); err != nil {
		return FinishContinuationResult{}, fmt.Errorf("store Finish Task Summary Version: %w", err)
	}

	finishedAtText := finishedAt.Format(time.RFC3339Nano)
	result, err := tx.ExecContext(ctx, `
		UPDATE blackboard_continuation_grants SET finished_at=?
		WHERE grant_id=? AND project_id=? AND task_id=? AND continuation_id=?
		  AND finished_at='' AND revoked_at='' AND terminal_at=''`,
		finishedAtText, req.GrantID, req.ProjectID, req.TaskID, req.ContinuationID,
	)
	if err != nil {
		return FinishContinuationResult{}, fmt.Errorf("close Continuation Interface Grant: %w", err)
	}
	if changed, _ := result.RowsAffected(); changed != 1 {
		return FinishContinuationResult{}, ErrContinuationWriteClosed
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE task_continuations
		SET blackboard_finish_summary_version_id=?,blackboard_finish_graph_revision=?,
		    blackboard_finish_mutation_sequence=?,blackboard_finished_at=?,updated_at=?
		WHERE id=?`,
		summary.ID, summary.BlackboardGraphRevision, summary.BlackboardMutationSequence,
		finishedAtText, finishedAtText, req.ContinuationID,
	); err != nil {
		return FinishContinuationResult{}, fmt.Errorf("store Continuation Finish marker: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return FinishContinuationResult{}, fmt.Errorf("commit Continuation Finish: %w", err)
	}
	return FinishContinuationResult{SummaryVersion: summary}, nil
}

// VerifyContinuationFinishMarker audits the graph revision and latest Runtime
// mutation sequence recorded by a valid Finish before clean completion is
// acknowledged.
func (s *Service) VerifyContinuationFinishMarker(ctx context.Context, continuationID string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin Continuation Finish audit: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	var taskID string
	var markerGraphRevision, markerMutationSequence int
	var summaryGraphRevision, summaryMutationSequence int
	err = tx.QueryRowContext(ctx, `
		SELECT c.task_id,c.blackboard_finish_graph_revision,c.blackboard_finish_mutation_sequence,
		       s.blackboard_graph_revision,s.blackboard_mutation_sequence
		FROM task_continuations c
		JOIN task_summary_versions s
		  ON s.id=c.blackboard_finish_summary_version_id
		 AND s.task_id=c.task_id AND s.continuation_id=c.id
		WHERE c.id=? AND c.blackboard_finish_summary_version_id<>''`, continuationID).Scan(
		&taskID, &markerGraphRevision, &markerMutationSequence,
		&summaryGraphRevision, &summaryMutationSequence,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrContinuationFinishMarkerMismatch
	}
	if err != nil {
		return fmt.Errorf("read Continuation Finish audit marker: %w", err)
	}
	if markerGraphRevision != summaryGraphRevision || markerMutationSequence != summaryMutationSequence {
		return ErrContinuationFinishMarkerMismatch
	}

	var projectID string
	if err := tx.QueryRowContext(ctx, `SELECT project_id FROM tasks WHERE id=?`, taskID).Scan(&projectID); err != nil {
		return fmt.Errorf("read Continuation Finish audit Project: %w", err)
	}
	var currentGraphRevision int
	err = tx.QueryRowContext(ctx, `SELECT current_graph_revision FROM blackboard_graph_state WHERE project_id=?`, projectID).Scan(&currentGraphRevision)
	if errors.Is(err, sql.ErrNoRows) {
		currentGraphRevision = 0
	} else if err != nil {
		return fmt.Errorf("read current graph revision for Finish audit: %w", err)
	}
	if markerGraphRevision > currentGraphRevision {
		return ErrContinuationFinishMarkerMismatch
	}

	var latestRuntimeMutationSequence int
	if err := tx.QueryRowContext(ctx, `
		SELECT COALESCE(MAX(o.mutation_seq),0)
		FROM blackboard_graph_operations o
		JOIN blackboard_graph_provenance p ON p.project_id=o.project_id AND p.id=o.provenance_id
		WHERE o.project_id=? AND p.task_id=? AND p.continuation_id=?`,
		projectID, taskID, continuationID,
	).Scan(&latestRuntimeMutationSequence); err != nil {
		return fmt.Errorf("read latest Runtime mutation sequence for Finish audit: %w", err)
	}
	if markerMutationSequence != latestRuntimeMutationSequence {
		return ErrContinuationFinishMarkerMismatch
	}
	return nil
}

func validateFinishObjectiveOutcome(ctx context.Context, tx *sql.Tx, projectID, taskID string, outcome FinishObjectiveOutcome) error {
	switch outcome.Status {
	case "supported", "contradicted", "inconclusive", "blocked":
	default:
		return &ObjectiveOutcomeError{Path: "objective_outcome.status", Message: "Objective Outcome status is invalid"}
	}
	objectiveID, objectiveType, err := resolveFinishNodeRef(ctx, tx, projectID, outcome.Objective)
	if err != nil {
		return &ObjectiveOutcomeError{Path: "objective_outcome.objective", Message: "Objective Outcome reference does not resolve in the bound Project"}
	}
	if objectiveType != "exploration_objective" {
		return &ObjectiveOutcomeError{Path: "objective_outcome.objective", Message: "Objective Outcome must reference an Exploration Objective"}
	}
	var primaryCount int
	if err := tx.QueryRowContext(ctx, `
		SELECT COUNT(*)
		FROM blackboard_edge_heads e
		JOIN blackboard_node_heads h ON h.project_id=e.project_id AND h.node_id=e.to_node_id
		JOIN blackboard_node_versions v ON v.project_id=h.project_id AND v.node_id=h.node_id AND v.version=h.version
		WHERE e.project_id=? AND e.edge_type='part_of' AND e.state='active'
		  AND e.from_node_id=? AND h.node_type='goal' AND h.disposition='main'
		  AND json_extract(v.properties_json,'$.task_id')=?`, projectID, objectiveID, taskID).Scan(&primaryCount); err != nil {
		return fmt.Errorf("validate primary Finish Objective: %w", err)
	}
	if primaryCount == 0 {
		return &ObjectiveOutcomeError{Path: "objective_outcome.objective", Message: "Task is not pursuing the referenced primary Exploration Objective"}
	}
	for i, ref := range outcome.SupportingNodeRefs {
		_, nodeType, err := resolveFinishNodeRef(ctx, tx, projectID, ref)
		if err != nil {
			return &ObjectiveOutcomeError{
				Path:    fmt.Sprintf("objective_outcome.supporting_node_refs[%d]", i),
				Message: "supporting reference does not resolve in the bound Project",
			}
		}
		switch nodeType {
		case "project_fact", "finding", "evidence_artifact":
		default:
			return &ObjectiveOutcomeError{
				Path:    fmt.Sprintf("objective_outcome.supporting_node_refs[%d]", i),
				Message: "supporting reference must be a Project Fact, Finding, or Evidence Artifact",
			}
		}
	}
	return nil
}

func resolveFinishNodeRef(ctx context.Context, tx *sql.Tx, projectID string, ref FinishNodeRef) (nodeID, nodeType string, err error) {
	if ref.ID != "" {
		err = tx.QueryRowContext(ctx, `
			SELECT h.node_id,h.node_type FROM blackboard_node_heads h
			WHERE h.project_id=? AND h.node_id=? AND h.disposition='main'`, projectID, ref.ID).Scan(&nodeID, &nodeType)
	} else if ref.NodeType != "" && ref.StableKey != "" {
		err = tx.QueryRowContext(ctx, `
			SELECT h.node_id,h.node_type
			FROM blackboard_key_registry k
			JOIN blackboard_node_heads h ON h.project_id=k.project_id AND h.node_id=k.canonical_node_id
			WHERE k.project_id=? AND k.node_type=? AND k.key=? AND h.disposition='main'`,
			projectID, ref.NodeType, ref.StableKey).Scan(&nodeID, &nodeType)
	} else {
		return "", "", errors.New("node reference is incomplete")
	}
	if err != nil {
		return "", "", err
	}
	if ref.NodeType != "" && ref.NodeType != nodeType {
		return "", "", errors.New("node type does not match resolved record")
	}
	return nodeID, nodeType, nil
}

func continuationOpenAttempts(ctx context.Context, tx *sql.Tx, projectID, taskID, continuationID string) ([]OpenAttemptRef, error) {
	rows, err := tx.QueryContext(ctx, `
		SELECT n.id,n.original_stable_key
		FROM blackboard_node_heads h
		JOIN blackboard_nodes n ON n.project_id=h.project_id AND n.id=h.node_id
		JOIN blackboard_graph_operations o ON o.project_id=n.project_id
		 AND o.mutation_seq=n.created_mutation_seq AND o.operation_index=n.created_operation_index
		JOIN blackboard_graph_provenance p ON p.project_id=o.project_id AND p.id=o.provenance_id
		WHERE h.project_id=? AND h.node_type='attempt' AND h.disposition='main'
		  AND h.lifecycle_state='open' AND p.task_id=? AND p.continuation_id=?
		ORDER BY n.original_stable_key,n.id`, projectID, taskID, continuationID)
	if err != nil {
		return nil, fmt.Errorf("read Continuation open Attempts: %w", err)
	}
	defer rows.Close()
	var attempts []OpenAttemptRef
	for rows.Next() {
		var attempt OpenAttemptRef
		attempt.NodeType = "attempt"
		if err := rows.Scan(&attempt.ID, &attempt.StableKey); err != nil {
			return nil, fmt.Errorf("scan Continuation open Attempt: %w", err)
		}
		attempts = append(attempts, attempt)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate Continuation open Attempts: %w", err)
	}
	return attempts, nil
}

func readFinishSummary(ctx context.Context, tx *sql.Tx, continuationID, key string) (SummaryVersion, bool, error) {
	var summary SummaryVersion
	var storedContinuationID sql.NullString
	var objectiveOutcome, createdAt string
	err := tx.QueryRowContext(ctx, `
		SELECT id,task_id,continuation_id,version,summary,objective_outcome_json,
		       blackboard_graph_revision,blackboard_mutation_sequence,
		       finish_idempotency_key,finish_request_hash,submitted_by,created_at
		FROM task_summary_versions
		WHERE continuation_id=? AND finish_idempotency_key=?`, continuationID, key).Scan(
		&summary.ID, &summary.TaskID, &storedContinuationID, &summary.Version,
		&summary.Summary, &objectiveOutcome, &summary.BlackboardGraphRevision,
		&summary.BlackboardMutationSequence, &summary.FinishIdempotencyKey,
		&summary.FinishRequestHash, &summary.SubmittedBy, &createdAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return SummaryVersion{}, false, nil
	}
	if err != nil {
		return SummaryVersion{}, false, fmt.Errorf("read stored Continuation Finish: %w", err)
	}
	summary.ContinuationID = storedContinuationID.String
	if objectiveOutcome != "" {
		summary.ObjectiveOutcome = json.RawMessage(objectiveOutcome)
	}
	if summary.CreatedAt, err = time.Parse(time.RFC3339Nano, createdAt); err != nil {
		return SummaryVersion{}, false, fmt.Errorf("parse stored Continuation Finish time: %w", err)
	}
	return summary, true, nil
}
