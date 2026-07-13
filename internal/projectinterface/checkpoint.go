package projectinterface

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"pentest/internal/blackboard"
	"pentest/internal/task"
)

// CheckpointFailurePoint names a stable persistence boundary used to prove
// recovery without mocking CyberPenda's graph or Task modules.
type CheckpointFailurePoint string

const (
	CheckpointFailureAfterEventCommit CheckpointFailurePoint = "event_commit"
)

var projectInterfaceIdempotencyKeyPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:/-]{0,127}$`)

// CheckpointFailureInjector injects a failure after a named durable boundary.
type CheckpointFailureInjector interface {
	FailAfter(CheckpointFailurePoint) error
}

// CheckpointAttemptRequest is the Runtime-supplied checkpoint envelope.
type CheckpointAttemptRequest struct {
	ProtocolVersion int                `json:"protocol_version"`
	IdempotencyKey  string             `json:"idempotency_key"`
	Attempt         blackboard.NodeRef `json:"attempt"`
	ExpectedVersion int                `json:"expected_version"`
	Summary         string             `json:"summary"`
}

type CheckpointAttemptResult struct {
	Event    task.Event                `json:"event"`
	Mutation blackboard.MutationResult `json:"mutation"`
}

type CheckpointAttemptResponse struct {
	ProtocolVersion       int                     `json:"protocol_version"`
	RequestKind           string                  `json:"request_kind"`
	ProjectID             string                  `json:"project_id"`
	ObservedGraphRevision int                     `json:"observed_graph_revision"`
	Result                CheckpointAttemptResult `json:"result"`
}

type checkpointRequestRow struct {
	requestHash, eventID, attemptNodeID, resultJSON string
}

// CheckpointAttempt appends one compact, idempotent Task Event before applying
// the Attempt summary patch with that Event as provenance. Retry reuses the
// durable Event and resumes or replays graph Apply.
func (s *Service) CheckpointAttempt(ctx context.Context, principal Principal, req CheckpointAttemptRequest) (CheckpointAttemptResponse, error) {
	if !principal.isRuntime() {
		return CheckpointAttemptResponse{}, ValidationError(
			ErrCodeContinuationContextRequired,
			"Attempt checkpoint requires a Continuation Interface Grant",
			"authorization",
		)
	}
	if err := requireGraph(s.graph); err != nil {
		return CheckpointAttemptResponse{}, err
	}
	if req.ProtocolVersion != RuntimeProtocolVersion {
		return CheckpointAttemptResponse{}, ValidationError(ErrCodeInvalidRequest,
			fmt.Sprintf("unsupported protocol version %d", req.ProtocolVersion), "protocol_version")
	}
	req.IdempotencyKey = strings.TrimSpace(req.IdempotencyKey)
	req.Summary = strings.TrimSpace(req.Summary)
	if !projectInterfaceIdempotencyKeyPattern.MatchString(req.IdempotencyKey) {
		return CheckpointAttemptResponse{}, ValidationError(ErrCodeInvalidRequest, "idempotency_key does not match the required grammar", "idempotency_key")
	}
	if req.ExpectedVersion <= 0 {
		return CheckpointAttemptResponse{}, ValidationError(ErrCodeInvalidRequest, "expected_version must be positive", "expected_version")
	}
	if req.Summary == "" {
		return CheckpointAttemptResponse{}, ValidationError(ErrCodeInvalidRequest, "summary is required", "summary")
	}
	if req.Attempt.ID == "" && (req.Attempt.NodeType != blackboard.NodeTypeAttempt || req.Attempt.StableKey == "") {
		return CheckpointAttemptResponse{}, ValidationError(ErrCodeInvalidRequest, "attempt must identify an Attempt by id or stable key", "attempt")
	}
	requestHash, err := checkpointRequestHash(req)
	if err != nil {
		return CheckpointAttemptResponse{}, InternalError("encode Attempt checkpoint request: " + err.Error())
	}
	currentGrant, err := s.currentGrant(ctx, principal)
	if err != nil {
		return CheckpointAttemptResponse{}, err
	}
	if !currentGrant.Status().IsReadable() {
		return CheckpointAttemptResponse{}, continuationClosedError(currentGrant.Status())
	}

	row, found, err := s.readCheckpointRequest(ctx, principal, req.IdempotencyKey)
	if err != nil {
		return CheckpointAttemptResponse{}, err
	}
	if found && row.requestHash != requestHash {
		return CheckpointAttemptResponse{}, ValidationError(blackboard.ErrCodeIdempotencyConflict,
			"Attempt checkpoint idempotency key was reused with a different request", "idempotency_key")
	}
	if found && row.resultJSON != "" {
		var stored CheckpointAttemptResponse
		if err := json.Unmarshal([]byte(row.resultJSON), &stored); err != nil {
			return CheckpointAttemptResponse{}, InternalError("decode stored Attempt checkpoint result: " + err.Error())
		}
		return stored, nil
	}

	var event task.Event
	if !found {
		if !currentGrant.Status().IsWriteable() {
			return CheckpointAttemptResponse{}, continuationClosedError(currentGrant.Status())
		}
		attempt, err := s.resolveCheckpointAttempt(ctx, principal, req)
		if err != nil {
			return CheckpointAttemptResponse{}, err
		}
		row, event, err = s.reserveCheckpointEvent(ctx, principal, req, requestHash, attempt.ID)
		if err != nil {
			return CheckpointAttemptResponse{}, err
		}
	} else {
		event, err = s.readCheckpointEvent(ctx, row.eventID)
		if err != nil {
			return CheckpointAttemptResponse{}, err
		}
	}
	if s.checkpointFailures != nil {
		if err := s.checkpointFailures.FailAfter(CheckpointFailureAfterEventCommit); err != nil {
			return CheckpointAttemptResponse{}, checkpointEventError(err, event.ID)
		}
	}

	apply, err := s.Apply(ctx, principal, ApplyMutationRequest{
		ProtocolVersion: RuntimeProtocolVersion,
		Batch: RequestBatch{
			SchemaVersion:  blackboard.GraphMutationSchemaVersion,
			IdempotencyKey: "checkpoint:" + requestHash[:32],
			Operations: []blackboard.Operation{{
				OpID: "checkpoint", Kind: blackboard.OpPatchNode,
				Node: blackboard.NodeRef{ID: row.attemptNodeID},
				Patch: blackboard.PatchNodeInput{
					ExpectedVersion: req.ExpectedVersion,
					Properties:      map[string]any{"summary": req.Summary},
				},
			}},
		},
		SourceEventIDsByOp: map[string][]string{"checkpoint": {event.ID}},
		attemptCheckpoint:  true,
	})
	if err != nil {
		return CheckpointAttemptResponse{}, withCheckpointEvent(err, event.ID)
	}
	response := CheckpointAttemptResponse{
		ProtocolVersion: RuntimeProtocolVersion, RequestKind: "checkpoint_attempt",
		ProjectID: principal.projectID(), ObservedGraphRevision: apply.ObservedGraphRevision,
		Result: CheckpointAttemptResult{Event: event, Mutation: apply.Result},
	}
	resultJSON, err := json.Marshal(response)
	if err != nil {
		return CheckpointAttemptResponse{}, InternalError("encode Attempt checkpoint result: " + err.Error())
	}
	if _, err := s.db.ExecContext(ctx, `
		UPDATE blackboard_attempt_checkpoint_requests SET result_json=?,updated_at=?
		WHERE project_id=? AND continuation_id=? AND idempotency_key=? AND request_hash=?`,
		string(resultJSON), s.clock.Now().UTC().Format(time.RFC3339Nano), principal.projectID(),
		principal.Grant.ContinuationID, req.IdempotencyKey, requestHash,
	); err != nil {
		return CheckpointAttemptResponse{}, checkpointEventError(fmt.Errorf("store checkpoint result: %w", err), event.ID)
	}
	return response, nil
}

func (s *Service) resolveCheckpointAttempt(ctx context.Context, principal Principal, req CheckpointAttemptRequest) (blackboard.NodeRecord, error) {
	var node blackboard.NodeRecord
	if req.Attempt.ID != "" {
		resolved, err := s.graph.ReadLiteralNode(ctx, blackboard.ReadLiteralNodeRequest{
			ProjectID: principal.projectID(), NodeID: req.Attempt.ID,
		})
		if err != nil {
			return blackboard.NodeRecord{}, mapGraphError(err)
		}
		node = resolved.Node
	} else {
		resolved, err := s.graph.ReadNode(ctx, blackboard.ReadNodeRequest{
			ProjectID: principal.projectID(), NodeType: blackboard.NodeTypeAttempt, Key: req.Attempt.StableKey,
		})
		if err != nil {
			return blackboard.NodeRecord{}, mapGraphError(err)
		}
		node = resolved.Node
	}
	if node.NodeType != blackboard.NodeTypeAttempt || node.Disposition != blackboard.DispositionMain || node.MergeTargetID != "" {
		return blackboard.NodeRecord{}, ValidationError(blackboard.ErrCodeNodeNotFound, "canonical main Attempt does not exist", "attempt")
	}
	if node.PropertyMap["status"] != "open" {
		return blackboard.NodeRecord{}, ValidationError(blackboard.ErrCodeInvalidTransition, "Attempt checkpoint requires status=open", "attempt.status")
	}
	if node.Version != req.ExpectedVersion {
		return blackboard.NodeRecord{}, ValidationError(blackboard.ErrCodeVersionConflict,
			fmt.Sprintf("expected version %d does not match current version %d", req.ExpectedVersion, node.Version),
			"expected_version")
	}
	var taskID, continuationID sql.NullString
	err := s.db.QueryRowContext(ctx, `
		SELECT p.task_id,p.continuation_id
		FROM blackboard_nodes n
		JOIN blackboard_graph_operations o ON o.project_id=n.project_id
		 AND o.mutation_seq=n.created_mutation_seq AND o.operation_index=n.created_operation_index
		JOIN blackboard_graph_provenance p ON p.project_id=o.project_id AND p.id=o.provenance_id
		WHERE n.project_id=? AND n.id=?`, principal.projectID(), node.ID).Scan(&taskID, &continuationID)
	if err != nil {
		return blackboard.NodeRecord{}, persistenceError("read Attempt provenance", err)
	}
	if taskID.String != principal.Grant.TaskID || continuationID.String != principal.Grant.ContinuationID {
		return blackboard.NodeRecord{}, ValidationError(ErrCodeSourceEventMismatch,
			"Attempt provenance does not match the bound Task and Continuation", "attempt")
	}
	return node, nil
}

func checkpointRequestHash(req CheckpointAttemptRequest) (string, error) {
	raw, err := json.Marshal(req)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:]), nil
}

func (s *Service) readCheckpointRequest(ctx context.Context, principal Principal, key string) (checkpointRequestRow, bool, error) {
	var row checkpointRequestRow
	err := s.db.QueryRowContext(ctx, `
		SELECT request_hash,event_id,attempt_node_id,result_json
		FROM blackboard_attempt_checkpoint_requests
		WHERE project_id=? AND continuation_id=? AND idempotency_key=?`,
		principal.projectID(), principal.Grant.ContinuationID, key,
	).Scan(&row.requestHash, &row.eventID, &row.attemptNodeID, &row.resultJSON)
	if errors.Is(err, sql.ErrNoRows) {
		return checkpointRequestRow{}, false, nil
	}
	if err != nil {
		return checkpointRequestRow{}, false, persistenceError("read Attempt checkpoint request", err)
	}
	return row, true, nil
}

func (s *Service) reserveCheckpointEvent(ctx context.Context, principal Principal, req CheckpointAttemptRequest, requestHash, attemptNodeID string) (checkpointRequestRow, task.Event, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return checkpointRequestRow{}, task.Event{}, persistenceError("begin checkpoint Event", err)
	}
	defer func() { _ = tx.Rollback() }()
	now := s.clock.Now().UTC()
	event := task.Event{
		ID: s.ids.NextID(), TaskID: principal.Grant.TaskID,
		ContinuationID: principal.Grant.ContinuationID,
		AttemptNodeID:  attemptNodeID,
		Kind:           task.EventKindBlackboardCheckpoint,
		Payload: task.EventPayload{
			"attempt_node_id": attemptNodeID,
			"summary":         req.Summary,
			"idempotency_key": req.IdempotencyKey,
		},
		CreatedAt: now,
	}
	var maxSeq sql.NullInt64
	if err := tx.QueryRowContext(ctx, `SELECT MAX(seq) FROM task_events WHERE task_id=?`, event.TaskID).Scan(&maxSeq); err != nil {
		return checkpointRequestRow{}, task.Event{}, persistenceError("read checkpoint Event sequence", err)
	}
	event.Seq = int(maxSeq.Int64) + 1
	payloadJSON, err := json.Marshal(event.Payload)
	if err != nil {
		return checkpointRequestRow{}, task.Event{}, InternalError("encode checkpoint Event: " + err.Error())
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO task_events(id,task_id,continuation_id,attempt_node_id,seq,kind,payload_json,created_at)
		VALUES(?,?,?,?,?,?,?,?)`, event.ID, event.TaskID, event.ContinuationID,
		attemptNodeID, event.Seq, string(event.Kind), string(payloadJSON), now.Format(time.RFC3339Nano),
	); err != nil {
		return checkpointRequestRow{}, task.Event{}, persistenceError("store checkpoint Event", err)
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO blackboard_attempt_checkpoint_requests(
		 project_id,continuation_id,idempotency_key,request_hash,event_id,attempt_node_id,created_at,updated_at
		) VALUES(?,?,?,?,?,?,?,?)`, principal.projectID(), principal.Grant.ContinuationID,
		req.IdempotencyKey, requestHash, event.ID, attemptNodeID,
		now.Format(time.RFC3339Nano), now.Format(time.RFC3339Nano),
	); err != nil {
		return checkpointRequestRow{}, task.Event{}, persistenceError("reserve checkpoint request", err)
	}
	if err := tx.Commit(); err != nil {
		return checkpointRequestRow{}, task.Event{}, persistenceError("commit checkpoint Event", err)
	}
	return checkpointRequestRow{requestHash: requestHash, eventID: event.ID, attemptNodeID: attemptNodeID}, event, nil
}

func (s *Service) readCheckpointEvent(ctx context.Context, eventID string) (task.Event, error) {
	var event task.Event
	var continuationID sql.NullString
	var kind, payloadJSON, createdAt string
	err := s.db.QueryRowContext(ctx, `
		SELECT id,task_id,continuation_id,attempt_node_id,seq,kind,payload_json,created_at
		FROM task_events WHERE id=?`, eventID).Scan(
		&event.ID, &event.TaskID, &continuationID, &event.AttemptNodeID, &event.Seq, &kind, &payloadJSON, &createdAt,
	)
	if err != nil {
		return task.Event{}, persistenceError("read durable checkpoint Event", err)
	}
	event.ContinuationID = continuationID.String
	event.Kind = task.EventKind(kind)
	if err := json.Unmarshal([]byte(payloadJSON), &event.Payload); err != nil {
		return task.Event{}, InternalError("decode durable checkpoint Event: " + err.Error())
	}
	if event.CreatedAt, err = time.Parse(time.RFC3339Nano, createdAt); err != nil {
		return task.Event{}, InternalError("parse durable checkpoint Event time: " + err.Error())
	}
	return event, nil
}

func checkpointEventError(err error, eventID string) *Error {
	mapped := persistenceError("Attempt checkpoint", err)
	if mapped.Code == ErrCodeInternal {
		mapped.Path = "checkpoint"
	}
	mapped.Details = map[string]any{"event_id": eventID}
	return mapped
}

func withCheckpointEvent(err error, eventID string) error {
	if interfaceErr := AsError(err); interfaceErr != nil {
		if interfaceErr.Details == nil {
			interfaceErr.Details = map[string]any{}
		}
		interfaceErr.Details["event_id"] = eventID
		return interfaceErr
	}
	return checkpointEventError(err, eventID)
}
