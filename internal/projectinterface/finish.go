package projectinterface

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"pentest/internal/blackboard"
	"pentest/internal/task"
)

// FinishContinuationRequest is the Runtime-supplied Finish envelope. Trusted
// Project, Task, Continuation, actor, graph position, and timestamp fields are
// deliberately absent and are bound by the Continuation Interface Grant.
type FinishContinuationRequest struct {
	ProtocolVersion  int               `json:"protocol_version"`
	IdempotencyKey   string            `json:"idempotency_key"`
	Summary          string            `json:"summary"`
	ObjectiveOutcome *ObjectiveOutcome `json:"objective_outcome,omitempty"`
}

// ObjectiveOutcome is an optional Task Summary conclusion. It records a
// judgment and supporting graph references without transitioning the
// Exploration Objective itself.
type ObjectiveOutcome struct {
	Objective          blackboard.NodeRef   `json:"objective"`
	Status             string               `json:"status"`
	SupportingNodeRefs []blackboard.NodeRef `json:"supporting_node_refs,omitempty"`
}

// FinishContinuationResult is the Task-domain marker committed atomically
// with the grant write-close.
type FinishContinuationResult struct {
	SummaryVersion   task.SummaryVersion `json:"summary_version"`
	GraphRevision    int                 `json:"graph_revision"`
	MutationSequence int                 `json:"mutation_sequence"`
	FinishedAt       string              `json:"finished_at"`
}

// FinishContinuationResponse is the canonical transport-neutral Finish
// success envelope.
type FinishContinuationResponse struct {
	ProtocolVersion       int                      `json:"protocol_version"`
	RequestKind           string                   `json:"request_kind"`
	ProjectID             string                   `json:"project_id"`
	ObservedGraphRevision int                      `json:"observed_graph_revision"`
	Result                FinishContinuationResult `json:"result"`
}

// FinishContinuation is the final Runtime project-interface write for one
// Continuation. Exact replay is resolved inside the Task transaction before
// the grant's new-write state is checked.
func (s *Service) FinishContinuation(ctx context.Context, principal Principal, req FinishContinuationRequest) (FinishContinuationResponse, error) {
	if !principal.isRuntime() {
		return FinishContinuationResponse{}, ValidationError(
			ErrCodeContinuationContextRequired,
			"Finish requires a Continuation Interface Grant",
			"authorization",
		)
	}
	if s.tasks == nil {
		return FinishContinuationResponse{}, InternalError("Task service is unavailable")
	}
	if req.ProtocolVersion != RuntimeProtocolVersion {
		return FinishContinuationResponse{}, ValidationError(
			ErrCodeInvalidRequest,
			fmt.Sprintf("unsupported protocol version %d", req.ProtocolVersion),
			"protocol_version",
		)
	}
	req.IdempotencyKey = strings.TrimSpace(req.IdempotencyKey)
	req.Summary = strings.TrimSpace(req.Summary)
	if !projectInterfaceIdempotencyKeyPattern.MatchString(req.IdempotencyKey) {
		return FinishContinuationResponse{}, ValidationError(ErrCodeInvalidRequest, "idempotency_key does not match the required grammar", "idempotency_key")
	}
	if req.Summary == "" {
		return FinishContinuationResponse{}, ValidationError(ErrCodeInvalidRequest, "summary is required", "summary")
	}
	if err := validateObjectiveOutcomeEnvelope(req.ObjectiveOutcome); err != nil {
		return FinishContinuationResponse{}, err
	}
	requestHash, err := finishRequestHash(req)
	if err != nil {
		return FinishContinuationResponse{}, InternalError("encode Finish request: " + err.Error())
	}
	var objectiveOutcomeJSON []byte
	var taskOutcome *task.FinishObjectiveOutcome
	if req.ObjectiveOutcome != nil {
		objectiveOutcomeJSON, err = json.Marshal(req.ObjectiveOutcome)
		if err != nil {
			return FinishContinuationResponse{}, InternalError("encode Objective Outcome: " + err.Error())
		}
		taskOutcome = &task.FinishObjectiveOutcome{
			Objective:          finishNodeRef(req.ObjectiveOutcome.Objective),
			Status:             req.ObjectiveOutcome.Status,
			SupportingNodeRefs: make([]task.FinishNodeRef, len(req.ObjectiveOutcome.SupportingNodeRefs)),
		}
		for i, ref := range req.ObjectiveOutcome.SupportingNodeRefs {
			taskOutcome.SupportingNodeRefs[i] = finishNodeRef(ref)
		}
	}
	finishedAt := s.clock.Now().UTC()
	finished, err := s.tasks.FinishContinuation(ctx, task.FinishContinuationRequest{
		ProjectID: principal.projectID(), TaskID: principal.Grant.TaskID,
		ContinuationID: principal.Grant.ContinuationID, GrantID: principal.Grant.ID,
		SummaryVersionID: s.ids.NextID(), Summary: req.Summary,
		ObjectiveOutcomeJSON: objectiveOutcomeJSON, ObjectiveOutcome: taskOutcome,
		SubmittedBy: principal.ActorID, IdempotencyKey: req.IdempotencyKey,
		RequestHash: requestHash, FinishedAt: finishedAt,
	})
	if err != nil {
		var openAttempts *task.OpenAttemptsError
		switch {
		case errors.As(err, &openAttempts):
			return FinishContinuationResponse{}, &Error{
				ProtocolVersion: RuntimeProtocolVersion,
				Code:            ErrCodeContinuationOpenAttempts,
				Message:         "Finish requires every current-Continuation Attempt to be terminal",
				Path:            "attempts",
				Details:         map[string]any{"open_attempts": openAttempts.Attempts},
			}
		case errors.Is(err, task.ErrContinuationFinishConflict):
			return FinishContinuationResponse{}, ValidationError(
				ErrCodeContinuationFinishConflict,
				"Finish idempotency key was reused with a different summary or Objective Outcome",
				"idempotency_key",
			)
		case errors.Is(err, task.ErrContinuationWriteClosed):
			current, readErr := s.currentGrant(ctx, principal)
			if readErr != nil {
				return FinishContinuationResponse{}, readErr
			}
			return FinishContinuationResponse{}, continuationClosedError(current.Status())
		case errors.Is(err, task.ErrNotFound):
			return FinishContinuationResponse{}, ValidationError(ErrCodeProjectNotFound, "bound Task or Continuation does not exist", "authorization")
		default:
			var outcomeErr *task.ObjectiveOutcomeError
			if errors.As(err, &outcomeErr) {
				return FinishContinuationResponse{}, ValidationError(ErrCodeInvalidRequest, outcomeErr.Message, outcomeErr.Path)
			}
			return FinishContinuationResponse{}, persistenceError("Finish Continuation", err)
		}
	}
	summary := finished.SummaryVersion
	return FinishContinuationResponse{
		ProtocolVersion:       RuntimeProtocolVersion,
		RequestKind:           "finish_continuation",
		ProjectID:             principal.projectID(),
		ObservedGraphRevision: summary.BlackboardGraphRevision,
		Result: FinishContinuationResult{
			SummaryVersion:   summary,
			GraphRevision:    summary.BlackboardGraphRevision,
			MutationSequence: summary.BlackboardMutationSequence,
			FinishedAt:       summary.CreatedAt.Format(timeFormat),
		},
	}, nil
}

func validateObjectiveOutcomeEnvelope(outcome *ObjectiveOutcome) *Error {
	if outcome == nil {
		return nil
	}
	if outcome.Objective.ID == "" && (outcome.Objective.NodeType != blackboard.NodeTypeExplorationObjective || strings.TrimSpace(outcome.Objective.StableKey) == "") {
		return ValidationError(ErrCodeInvalidRequest, "objective must identify an Exploration Objective", "objective_outcome.objective")
	}
	switch outcome.Status {
	case "supported", "contradicted", "inconclusive", "blocked":
	default:
		return ValidationError(ErrCodeInvalidRequest, "Objective Outcome status must be supported, contradicted, inconclusive, or blocked", "objective_outcome.status")
	}
	for i, ref := range outcome.SupportingNodeRefs {
		if ref.ID == "" && (ref.NodeType == "" || strings.TrimSpace(ref.StableKey) == "") {
			return ValidationError(ErrCodeInvalidRequest, "supporting reference must supply id or (node_type, stable_key)", fmt.Sprintf("objective_outcome.supporting_node_refs[%d]", i))
		}
	}
	return nil
}

func finishNodeRef(ref blackboard.NodeRef) task.FinishNodeRef {
	return task.FinishNodeRef{ID: ref.ID, NodeType: string(ref.NodeType), StableKey: ref.StableKey}
}

const timeFormat = "2006-01-02T15:04:05.999999999Z07:00"

func finishRequestHash(req FinishContinuationRequest) (string, error) {
	raw, err := json.Marshal(req)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:]), nil
}
