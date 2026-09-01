// Package finishreadiness projects every durable condition that can block
// operator-controlled Task Finish.
package finishreadiness

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"pentest/internal/store"
	"pentest/internal/task"
)

const (
	BlockerPendingConclusions         = "pending_blackboard_conclusions"
	BlockerConclusionActionRequired   = "blackboard_conclusion_action_required"
	BlockerOpenAttempts               = "open_attempts"
	BlockerOpenChallengeAttempts      = "unfinalized_challenge_attempts"
	BlockerOpenChallengeObjectives    = "open_challenge_objectives"
	BlockerPendingChallengeOperations = "pending_challenge_operations"
	BlockerMissingChallengeEvidence   = "missing_challenge_evidence"
	BlockerReconciliation             = "continuation_reconciliation"
	BlockerInvalidFinishIntent        = "invalid_finish_intent"
)

type Blocker struct {
	Code    string   `json:"code"`
	Count   int      `json:"count"`
	Message string   `json:"message"`
	Links   []string `json:"links,omitempty"`
}

type Readiness struct {
	ReadyToFinish bool      `json:"ready_to_finish"`
	Blockers      []Blocker `json:"blockers"`
}

type NotReadyError struct{ Readiness Readiness }

func (err *NotReadyError) Error() string { return "Task is not ready to finish" }

type Service struct {
	db    *store.DB
	tasks *task.Service
}

func NewService(db *store.DB, tasks *task.Service) *Service { return &Service{db: db, tasks: tasks} }

func (service *Service) Evaluate(ctx context.Context, projectID, taskID string) (Readiness, error) {
	found, err := service.tasks.Get(taskID)
	if err != nil {
		return Readiness{}, err
	}
	if found.ProjectID != projectID {
		return Readiness{}, task.ErrNotFound
	}
	blackboardDisabled := found.RunControls.BlackboardMode == task.BlackboardModeDisabled
	readiness := Readiness{Blockers: []Blocker{}}
	queries := []struct {
		code, message, link, query string
		args                       []any
		blackboardOnly             bool
	}{
		{BlockerPendingConclusions, "Blackboard conclusions are not settled.", "blackboard", `SELECT COUNT(*) FROM pending_blackboard_conclusions WHERE task_id=? AND state NOT IN ('clean','applied','action_required')`, []any{taskID}, true},
		{BlockerConclusionActionRequired, "A Blackboard conclusion needs operator action.", "blackboard", `SELECT COUNT(*) FROM pending_blackboard_conclusions WHERE task_id=? AND state='action_required'`, []any{taskID}, true},
		{BlockerOpenAttempts, "Task-owned Blackboard Attempts are still open.", "blackboard", `SELECT COUNT(*) FROM blackboard_v2_attempt_origins origins JOIN task_continuations continuations ON continuations.id=origins.continuation_id JOIN blackboard_v2_records records ON records.project_id=origins.project_id AND records.key=origins.key WHERE continuations.task_id=? AND records.type='attempt' AND json_extract(records.record_json,'$.status')='open'`, []any{taskID}, true},
		{BlockerOpenChallengeAttempts, "Challenge Attempts are not finalized.", "challenge-workflow", `SELECT COUNT(*) FROM challenge_attempts WHERE task_id=? AND status<>'finalized'`, []any{taskID}, false},
		{BlockerOpenChallengeObjectives, "Challenge Objectives are still open.", "blackboard", `SELECT COUNT(*) FROM challenge_attempts attempts JOIN blackboard_v2_records records ON records.project_id=attempts.project_id AND records.key=attempts.objective_key WHERE attempts.task_id=? AND records.type='objective' AND json_extract(records.record_json,'$.status')='open'`, []any{taskID}, false},
		{BlockerPendingChallengeOperations, "Challenge Platform operations need recovery.", "challenge-workflow", `SELECT COUNT(*) FROM challenge_operations WHERE task_id=? AND state<>'completed'`, []any{taskID}, false},
		{BlockerMissingChallengeEvidence, "A Challenge Platform response has no retained Evidence.", "evidence", `SELECT COUNT(*) FROM challenge_operations operations WHERE operations.task_id=? AND operations.kind IN ('claim','submit','abandon') AND operations.state='completed' AND (operations.evidence_key='' OR NOT EXISTS (SELECT 1 FROM blackboard_v2_records records WHERE records.project_id=operations.project_id AND records.key=operations.evidence_key AND records.type='evidence'))`, []any{taskID}, false},
	}
	for _, item := range queries {
		if blackboardDisabled && item.blackboardOnly {
			continue
		}
		var count int
		if err := service.db.QueryRowContext(ctx, item.query, item.args...).Scan(&count); err != nil {
			return Readiness{}, fmt.Errorf("evaluate Finish Readiness %s: %w", item.code, err)
		}
		if count > 0 {
			readiness.Blockers = append(readiness.Blockers, Blocker{Code: item.code, Count: count, Message: item.message, Links: []string{readinessLink(item.link, projectID, taskID)}})
		}
	}
	continuation, err := service.tasks.LatestContinuation(taskID)
	if err != nil {
		return Readiness{}, err
	}
	if !blackboardDisabled && continuation != nil && continuation.EndedAt != nil && continuation.BlackboardReconciliationStatus != task.ReconciliationCompleted {
		readiness.Blockers = append(readiness.Blockers, Blocker{Code: BlockerReconciliation, Count: 1, Message: "The latest terminal Continuation is not reconciled.", Links: []string{readinessLink("continuation", projectID, taskID)}})
	}
	if !blackboardDisabled && continuation != nil {
		var invalidated int
		err := service.db.QueryRowContext(ctx, `SELECT invalidated FROM blackboard_v2_finish_intents WHERE continuation_id=?`, continuation.ID).Scan(&invalidated)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return Readiness{}, err
		}
		if err == nil && invalidated == 1 {
			readiness.Blockers = append(readiness.Blockers, Blocker{Code: BlockerInvalidFinishIntent, Count: 1, Message: "The Blackboard Finish Intent was invalidated. Record a new Finish Intent.", Links: []string{readinessLink("blackboard", projectID, taskID)}})
		}
	}
	readiness.ReadyToFinish = len(readiness.Blockers) == 0
	return readiness, nil
}

func readinessLink(surface, projectID, taskID string) string {
	switch surface {
	case "challenge-workflow":
		return "/projects/" + projectID + "/tasks/" + taskID + "/challenges"
	case "evidence":
		return "/projects/" + projectID + "/evidence"
	case "continuation":
		return "/projects/" + projectID + "/tasks/" + taskID
	default:
		return "/projects/" + projectID + "/blackboard"
	}
}

func (service *Service) Require(ctx context.Context, projectID, taskID string) (Readiness, error) {
	readiness, err := service.Evaluate(ctx, projectID, taskID)
	if err != nil {
		return readiness, err
	}
	if !readiness.ReadyToFinish {
		return readiness, &NotReadyError{Readiness: readiness}
	}
	return readiness, nil
}
