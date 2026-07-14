package blackboard

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
)

// ContinuationAttemptRef is one canonical main open Attempt created by a
// specific Task and Continuation.
type ContinuationAttemptRef struct {
	ID        string
	StableKey string
	Version   int
	Summary   string
}

// ContinuationReconciliationResult is the graph-owned result of unexpected
// end recovery. MutationID is also recovered after a committed Apply whose
// Task-domain marker update was lost.
type ContinuationReconciliationResult struct {
	MutationID string
	Attempts   []ContinuationAttemptRef
}

type reconciliationEvent struct {
	id, attemptNodeID, kind string
	seq                     int
	payload                 map[string]any
}

// OpenAttemptsForContinuation returns canonical main open Attempts by their
// immutable creation provenance, ordered by stable key then ID.
func (s *GraphService) OpenAttemptsForContinuation(ctx context.Context, projectID, taskID, continuationID string) ([]ContinuationAttemptRef, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT h.node_id,n.original_stable_key,h.version,COALESCE(json_extract(v.properties_json,'$.summary'),'')
		FROM blackboard_node_heads h
		JOIN blackboard_node_versions v ON v.project_id=h.project_id AND v.node_id=h.node_id AND v.version=h.version
		JOIN blackboard_nodes n ON n.project_id=h.project_id AND n.id=h.node_id
		JOIN blackboard_graph_operations o ON o.project_id=n.project_id
		 AND o.mutation_seq=n.created_mutation_seq AND o.operation_index=n.created_operation_index
		JOIN blackboard_graph_provenance p ON p.project_id=o.project_id AND p.id=o.provenance_id
		WHERE h.project_id=? AND h.node_type='attempt' AND h.disposition='main'
		  AND json_extract(v.properties_json,'$.status')='open'
		  AND p.task_id=? AND p.continuation_id=?
		ORDER BY n.original_stable_key,h.node_id`, projectID, taskID, continuationID)
	if err != nil {
		return nil, fmt.Errorf("query Continuation open Attempts: %w", err)
	}
	defer rows.Close()
	var attempts []ContinuationAttemptRef
	for rows.Next() {
		var attempt ContinuationAttemptRef
		if err := rows.Scan(&attempt.ID, &attempt.StableKey, &attempt.Version, &attempt.Summary); err != nil {
			return nil, fmt.Errorf("scan Continuation open Attempt: %w", err)
		}
		attempts = append(attempts, attempt)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate Continuation open Attempts: %w", err)
	}
	return attempts, nil
}

// ReconcileUnexpectedContinuation interrupts only open Attempts created by
// the matching Task and Continuation. All graph changes pass through Apply.
func (s *GraphService) ReconcileUnexpectedContinuation(ctx context.Context, projectID, taskID, continuationID, reason string) (ContinuationReconciliationResult, error) {
	return s.reconcileUnexpectedContinuation(ctx, projectID, taskID, continuationID, reason, 0)
}

func (s *GraphService) reconcileUnexpectedContinuation(ctx context.Context, projectID, taskID, continuationID, reason string, retries int) (ContinuationReconciliationResult, error) {
	attempts, err := s.OpenAttemptsForContinuation(ctx, projectID, taskID, continuationID)
	if err != nil {
		return ContinuationReconciliationResult{}, err
	}
	if len(attempts) == 0 {
		mutationID, err := s.reconciliationMutationID(ctx, projectID, continuationID)
		return ContinuationReconciliationResult{MutationID: mutationID}, err
	}

	events, err := s.reconciliationEvents(ctx, taskID, continuationID)
	if err != nil {
		return ContinuationReconciliationResult{}, err
	}
	operations := make([]Operation, 0, len(attempts))
	sourceEvents := make(map[string][]string, len(attempts))
	for index, attempt := range attempts {
		opID := fmt.Sprintf("interrupt-%03d", index+1)
		summary := attempt.Summary
		if strings.TrimSpace(summary) == "" {
			summary = interruptionSummary(events, attempt.ID)
		}
		if summary == "" {
			summary = fmt.Sprintf("Continuation %s ended before this Attempt was concluded (%s).", continuationID, reason)
		}
		operations = append(operations, Operation{
			OpID: opID, Kind: OpTransitionNode, Node: NodeRef{ID: attempt.ID},
			Transition: TransitionNodeInput{ExpectedVersion: attempt.Version, Status: "interrupted", Summary: summary},
		})
		if ids := interruptionEventIDs(events, attempt.ID); len(ids) > 0 {
			sourceEvents[opID] = ids
		}
	}

	var projectKind string
	if err := s.db.QueryRowContext(ctx, `SELECT kind FROM projects WHERE id=?`, projectID).Scan(&projectKind); err != nil {
		return ContinuationReconciliationResult{}, fmt.Errorf("read reconciliation Project kind: %w", err)
	}
	ids := make([]string, len(attempts))
	for index := range attempts {
		ids[index] = attempts[index].ID
	}
	sort.Strings(ids)
	hash := sha256.Sum256([]byte(strings.Join(ids, "\n")))
	result, err := s.Apply(ctx, MutationBatch{
		SchemaVersion:  GraphMutationSchemaVersion,
		IdempotencyKey: "reconcile:" + continuationID + ":" + hex.EncodeToString(hash[:]),
		Context:        SystemReconciliationExecutionContext(projectID, projectKind, taskID, continuationID, reason),
		Operations:     operations, SourceEventIDsByOp: sourceEvents,
	})
	if err != nil {
		var validation *ValidationError
		if retries < 8 && errors.As(err, &validation) && validation.Code == ErrCodeVersionConflict {
			return s.reconcileUnexpectedContinuation(ctx, projectID, taskID, continuationID, reason, retries+1)
		}
		return ContinuationReconciliationResult{}, err
	}
	return ContinuationReconciliationResult{MutationID: result.MutationID, Attempts: attempts}, nil
}

func (s *GraphService) reconciliationMutationID(ctx context.Context, projectID, continuationID string) (string, error) {
	var mutationID string
	err := s.db.QueryRowContext(ctx, `
		SELECT mutation_id FROM blackboard_graph_mutations
		WHERE project_id=? AND mutation_kind='reconciliation' AND maintenance_subject_id=?
		ORDER BY mutation_seq DESC LIMIT 1`, projectID, continuationID).Scan(&mutationID)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("discover committed reconciliation mutation: %w", err)
	}
	return mutationID, nil
}

func (s *GraphService) reconciliationEvents(ctx context.Context, taskID, continuationID string) ([]reconciliationEvent, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id,COALESCE(attempt_node_id,''),seq,kind,payload_json
		FROM task_events WHERE task_id=? AND continuation_id=? ORDER BY seq,id`, taskID, continuationID)
	if err != nil {
		return nil, fmt.Errorf("query reconciliation Task Events: %w", err)
	}
	defer rows.Close()
	var events []reconciliationEvent
	for rows.Next() {
		var event reconciliationEvent
		var payloadJSON string
		if err := rows.Scan(&event.id, &event.attemptNodeID, &event.seq, &event.kind, &payloadJSON); err != nil {
			return nil, fmt.Errorf("scan reconciliation Task Event: %w", err)
		}
		if err := json.Unmarshal([]byte(payloadJSON), &event.payload); err != nil {
			return nil, fmt.Errorf("decode reconciliation Task Event: %w", err)
		}
		events = append(events, event)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate reconciliation Task Events: %w", err)
	}
	return events, nil
}

func interruptionSummary(events []reconciliationEvent, attemptID string) string {
	for index := len(events) - 1; index >= 0; index-- {
		event := events[index]
		if event.attemptNodeID != attemptID {
			continue
		}
		if summary, _ := event.payload["summary"].(string); strings.TrimSpace(summary) != "" {
			return strings.TrimSpace(summary)
		}
	}
	for index := len(events) - 1; index >= 0; index-- {
		event := events[index]
		if event.kind == "lifecycle" {
			if !isContinuationTerminalEvent(event.payload) {
				continue
			}
		} else if event.kind == "error" {
			if event.attemptNodeID != "" && event.attemptNodeID != attemptID {
				continue
			}
		} else {
			continue
		}
		for _, field := range []string{"message", "error", "reason"} {
			if message, _ := event.payload[field].(string); strings.TrimSpace(message) != "" {
				return strings.TrimSpace(message)
			}
		}
	}
	return ""
}

func interruptionEventIDs(events []reconciliationEvent, attemptID string) []string {
	eligible := make([]reconciliationEvent, 0, len(events))
	seen := map[string]struct{}{}
	for _, event := range events {
		sharedLifecycle := event.kind == "lifecycle" && isContinuationBoundaryEvent(event.payload)
		matching := event.attemptNodeID == attemptID && (event.kind == "blackboard_checkpoint" || event.kind == "status" || event.kind == "error")
		if !sharedLifecycle && !matching {
			continue
		}
		if _, exists := seen[event.id]; exists {
			continue
		}
		seen[event.id] = struct{}{}
		eligible = append(eligible, event)
	}
	if len(eligible) > 8 {
		selected := map[string]reconciliationEvent{}
		for _, event := range eligible {
			if isContinuationStartEvent(event.payload) {
				selected[event.id] = event
				break
			}
		}
		for index := len(eligible) - 1; index >= 0; index-- {
			event := eligible[index]
			if isContinuationTerminalEvent(event.payload) {
				selected[event.id] = event
				break
			}
		}
		for index := len(eligible) - 1; index >= 0 && len(selected) < 8; index-- {
			selected[eligible[index].id] = eligible[index]
		}
		eligible = eligible[:0]
		for _, event := range selected {
			eligible = append(eligible, event)
		}
		sort.Slice(eligible, func(i, j int) bool {
			if eligible[i].seq != eligible[j].seq {
				return eligible[i].seq < eligible[j].seq
			}
			return eligible[i].id < eligible[j].id
		})
	}
	ids := make([]string, len(eligible))
	for index := range eligible {
		ids[index] = eligible[index].id
	}
	return ids
}

func isContinuationBoundaryEvent(payload map[string]any) bool {
	return isContinuationStartEvent(payload) || isContinuationTerminalEvent(payload)
}

func isContinuationStartEvent(payload map[string]any) bool {
	phase, _ := payload["phase"].(string)
	switch phase {
	case "started", "running":
		return true
	default:
		return false
	}
}

func isContinuationTerminalEvent(payload map[string]any) bool {
	phase, _ := payload["phase"].(string)
	switch phase {
	case "completed", "failed", "stopped", "interrupted", "timed_out":
		return true
	default:
		return false
	}
}
