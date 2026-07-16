// Package blackboardv2 owns the Blackboard v2 semantic service. It is the
// durable service/store seam shared by later HTTP, MCP, CLI, and runtime
// projection adapters.
package blackboardv2

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"pentest/internal/store"
)

const (
	changeBatchSchema  = "semantic-change-batch/v2"
	changeResultSchema = "semantic-change-result/v2"
	recordSchema       = "blackboard-record/v2"
	historySchema      = "semantic-history/v2"
	snapshotSchema     = "runtime-blackboard/v2"
	snapshotSemantics  = "work is active; knowledge is current; history and details are available by key"
	workingPath        = ".pentest/blackboard.json"
)

// Service applies and reads Blackboard v2 semantic state for one Project.
type Service struct {
	db *store.DB
}

// NewService returns a Blackboard v2 semantic service backed by the Store.
func NewService(db *store.DB) *Service {
	return &Service{db: db}
}

// ChangeBatch is the semantic-change-batch/v2 envelope.
type ChangeBatch struct {
	Schema         string   `json:"schema"`
	IdempotencyKey string   `json:"idempotency_key"`
	Changes        []Change `json:"changes"`
}

// Change is the closed operation shape subset owned by #100.
type Change struct {
	Op                 string   `json:"op"`
	Key                string   `json:"key,omitempty"`
	Version            int      `json:"version,omitempty"`
	Type               string   `json:"type,omitempty"`
	Record             any      `json:"record,omitempty"`
	Clear              []string `json:"clear,omitempty"`
	From               string   `json:"from,omitempty"`
	Relation           string   `json:"relation,omitempty"`
	To                 string   `json:"to,omitempty"`
	Reason             string   `json:"reason,omitempty"`
	Status             string   `json:"status,omitempty"`
	Summary            string   `json:"summary,omitempty"`
	ResolutionSummary  string   `json:"resolution_summary,omitempty"`
	Replacement        string   `json:"replacement,omitempty"`
	ReplacementVersion int      `json:"replacement_version,omitempty"`
	Replaced           string   `json:"replaced,omitempty"`
	ReplacedVersion    int      `json:"replaced_version,omitempty"`
}

// UnmarshalJSON enforces the closed semantic-change item shapes at the service
// DTO boundary so adapters cannot silently drop unknown fields before Apply.
func (change *Change) UnmarshalJSON(raw []byte) error {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return err
	}
	opRaw, ok := fields["op"]
	if !ok {
		return fmt.Errorf("change op is required")
	}
	var op string
	if err := json.Unmarshal(opRaw, &op); err != nil {
		return fmt.Errorf("decode change op: %w", err)
	}
	switch op {
	case "create":
		if err := rejectUnknownFields(fields, map[string]bool{"op": true, "key": true, "type": true, "record": true}); err != nil {
			return err
		}
		decoded, err := decodeCreateChange(fields)
		if err != nil {
			return err
		}
		*change = decoded
	case "update":
		if err := rejectUnknownFields(fields, map[string]bool{"op": true, "key": true, "version": true, "type": true, "record": true, "clear": true}); err != nil {
			return err
		}
		decoded, err := decodeUpdateChange(fields)
		if err != nil {
			return err
		}
		*change = decoded
	case "relate":
		if err := rejectUnknownFields(fields, map[string]bool{"op": true, "from": true, "relation": true, "to": true, "version": true, "reason": true}); err != nil {
			return err
		}
		decoded, err := decodeRelateChange(fields)
		if err != nil {
			return err
		}
		*change = decoded
	case "transition":
		if err := rejectUnknownFields(fields, map[string]bool{"op": true, "key": true, "version": true, "status": true, "summary": true, "resolution_summary": true}); err != nil {
			return err
		}
		decoded, err := decodeTransitionChange(fields)
		if err != nil {
			return err
		}
		*change = decoded
	case "supersede":
		if err := rejectUnknownFields(fields, map[string]bool{"op": true, "replacement": true, "replacement_version": true, "replaced": true, "replaced_version": true}); err != nil {
			return err
		}
		decoded, err := decodeSupersedeChange(fields)
		if err != nil {
			return err
		}
		*change = decoded
	default:
		*change = Change{Op: op}
	}
	return nil
}

// EntityRecord is the complete semantic Entity DTO.
type EntityRecord struct {
	Status        string `json:"status"`
	Kind          string `json:"kind"`
	Name          string `json:"name"`
	Locator       string `json:"locator,omitempty"`
	Description   string `json:"description,omitempty"`
	ScopeStatus   string `json:"scope_status"`
	CredentialRef string `json:"credential_ref,omitempty"`
}

// EntityPatch is the closed partial update shape for Entities.
type EntityPatch struct {
	Kind          *string `json:"kind,omitempty"`
	Name          *string `json:"name,omitempty"`
	Locator       *string `json:"locator,omitempty"`
	Description   *string `json:"description,omitempty"`
	ScopeStatus   *string `json:"scope_status,omitempty"`
	CredentialRef *string `json:"credential_ref,omitempty"`
}

// ObjectiveRecord is the complete semantic Exploration Objective DTO.
type ObjectiveRecord struct {
	Status            string `json:"status"`
	Objective         string `json:"objective"`
	ResolutionSummary string `json:"resolution_summary,omitempty"`
}

// ObjectivePatch is the closed partial update shape for open Objectives.
type ObjectivePatch struct {
	Objective *string `json:"objective,omitempty"`
}

// AttemptRecord is the complete semantic Attempt DTO.
type AttemptRecord struct {
	Status  string `json:"status"`
	Summary string `json:"summary"`
}

// AttemptPatch is the closed partial update shape for open Attempts.
type AttemptPatch struct {
	Summary *string `json:"summary,omitempty"`
}

// FactRecord is the complete semantic Project Fact DTO.
type FactRecord struct {
	Category    string `json:"category"`
	Summary     string `json:"summary"`
	Body        string `json:"body,omitempty"`
	Confidence  string `json:"confidence"`
	ScopeStatus string `json:"scope_status"`
}

// FactPatch is the closed partial update shape for Project Facts.
type FactPatch struct {
	Category    *string `json:"category,omitempty"`
	Summary     *string `json:"summary,omitempty"`
	Body        *string `json:"body,omitempty"`
	Confidence  *string `json:"-"`
	ScopeStatus *string `json:"scope_status,omitempty"`
}

// Record is the current-detail union for the implemented Project Knowledge
// records. Empty fields are omitted so each type still serializes to its closed
// contract allowlist.
type Record struct {
	Status            string `json:"status,omitempty"`
	Objective         string `json:"objective,omitempty"`
	ResolutionSummary string `json:"resolution_summary,omitempty"`
	Kind              string `json:"kind,omitempty"`
	Name              string `json:"name,omitempty"`
	Locator           string `json:"locator,omitempty"`
	Description       string `json:"description,omitempty"`
	ScopeStatus       string `json:"scope_status,omitempty"`
	CredentialRef     string `json:"credential_ref,omitempty"`
	Category          string `json:"category,omitempty"`
	Summary           string `json:"summary,omitempty"`
	Body              string `json:"body,omitempty"`
	Confidence        string `json:"confidence,omitempty"`
}

// ChangeResult is semantic-change-result/v2.
type ChangeResult struct {
	Schema          string                 `json:"schema"`
	Revision        int                    `json:"revision"`
	Records         []RecordVersionTuple   `json:"records"`
	Relations       []RelationVersionTuple `json:"relations"`
	WorkingSnapshot WorkingSnapshot        `json:"working_snapshot"`
}

// RecordVersionTuple serializes as [key, version].
type RecordVersionTuple [2]any

// UnmarshalJSON keeps replayed idempotency receipts structurally stable.
func (tuple *RecordVersionTuple) UnmarshalJSON(raw []byte) error {
	var values []json.RawMessage
	if err := json.Unmarshal(raw, &values); err != nil {
		return err
	}
	if len(values) != 2 {
		return fmt.Errorf("record version tuple must have 2 values")
	}
	var key string
	var version int
	if err := json.Unmarshal(values[0], &key); err != nil {
		return err
	}
	if err := json.Unmarshal(values[1], &version); err != nil {
		return err
	}
	*tuple = RecordVersionTuple{key, version}
	return nil
}

// RelationVersionTuple serializes as [from, relation, to, version].
type RelationVersionTuple [4]any

// UnmarshalJSON keeps replayed relationship tuples structurally stable.
func (tuple *RelationVersionTuple) UnmarshalJSON(raw []byte) error {
	var values []json.RawMessage
	if err := json.Unmarshal(raw, &values); err != nil {
		return err
	}
	if len(values) != 4 {
		return fmt.Errorf("relation version tuple must have 4 values")
	}
	var from, relation, to string
	var version int
	if err := json.Unmarshal(values[0], &from); err != nil {
		return err
	}
	if err := json.Unmarshal(values[1], &relation); err != nil {
		return err
	}
	if err := json.Unmarshal(values[2], &to); err != nil {
		return err
	}
	if err := json.Unmarshal(values[3], &version); err != nil {
		return err
	}
	*tuple = RelationVersionTuple{from, relation, to, version}
	return nil
}

// WorkingSnapshot points to the Runtime's working Blackboard snapshot.
type WorkingSnapshot struct {
	Path     string `json:"path"`
	Revision int    `json:"revision"`
}

// CurrentDetail is blackboard-record/v2 for one current semantic record.
type CurrentDetail struct {
	Schema        string              `json:"schema"`
	Revision      int                 `json:"revision"`
	Key           string              `json:"key"`
	Type          string              `json:"type"`
	Version       int                 `json:"version"`
	Record        Record              `json:"record"`
	Relationships []RelationshipTuple `json:"relationships"`
}

// RelationshipTuple serializes as a v2 relationship tuple.
type RelationshipTuple []any

// HistoryOptions controls explicit Semantic History pagination.
type HistoryOptions struct {
	Cursor string
	Limit  int
}

// SemanticHistory is semantic-history/v2.
type SemanticHistory struct {
	Schema     string        `json:"schema"`
	Revision   int           `json:"revision"`
	Key        string        `json:"key"`
	Items      []HistoryItem `json:"items"`
	NextCursor string        `json:"next_cursor,omitempty"`
}

// HistoryItem is a record Semantic History item.
type HistoryItem struct {
	Kind     string  `json:"kind"`
	Version  int     `json:"version"`
	Type     string  `json:"type,omitempty"`
	Record   *Record `json:"record,omitempty"`
	From     string  `json:"from,omitempty"`
	Relation string  `json:"relation,omitempty"`
	To       string  `json:"to,omitempty"`
	Reason   string  `json:"reason,omitempty"`
}

// RuntimeSnapshot is runtime-blackboard/v2.
type RuntimeSnapshot struct {
	Schema    string              `json:"schema"`
	Semantics string              `json:"semantics"`
	Revision  int                 `json:"revision"`
	Work      SnapshotWork        `json:"work"`
	Knowledge SnapshotKnowledge   `json:"knowledge"`
	Relations []RelationshipTuple `json:"relations"`
}

// SnapshotWork groups open Current Work records.
type SnapshotWork struct {
	Objectives map[string]SnapshotObjective `json:"objectives,omitempty"`
	Attempts   map[string]SnapshotAttempt   `json:"attempts,omitempty"`
}

// SnapshotObjective is the runtime allowlist for open Objectives.
type SnapshotObjective struct {
	Version   int    `json:"version"`
	Status    string `json:"status"`
	Objective string `json:"objective"`
}

// SnapshotAttempt is the runtime allowlist for open Attempts.
type SnapshotAttempt struct {
	Version int    `json:"version"`
	Status  string `json:"status"`
	Summary string `json:"summary"`
}

// SnapshotKnowledge groups current Project Knowledge records.
type SnapshotKnowledge struct {
	Entities map[string]SnapshotEntity `json:"entities,omitempty"`
	Facts    map[string]SnapshotFact   `json:"facts,omitempty"`
}

// SnapshotEntity is the runtime allowlist for Entities.
type SnapshotEntity struct {
	Version       int    `json:"version"`
	Status        string `json:"status"`
	Kind          string `json:"kind"`
	Name          string `json:"name"`
	Locator       string `json:"locator,omitempty"`
	Description   string `json:"description,omitempty"`
	ScopeStatus   string `json:"scope_status"`
	CredentialRef string `json:"credential_ref,omitempty"`
}

// SnapshotFact is the runtime allowlist for Project Facts.
type SnapshotFact struct {
	Version     int    `json:"version"`
	Category    string `json:"category"`
	Summary     string `json:"summary"`
	Confidence  string `json:"confidence"`
	ScopeStatus string `json:"scope_status"`
}

// Error is the stable semantic error envelope body surfaced by adapters.
type Error struct {
	Code      string         `json:"code"`
	Message   string         `json:"message"`
	Path      string         `json:"path,omitempty"`
	Retryable bool           `json:"retryable"`
	Details   map[string]any `json:"details,omitempty"`
}

func (e *Error) Error() string {
	if e == nil {
		return ""
	}
	return e.Message
}

type storedRecord struct {
	key     string
	typ     string
	version int
	record  Record
}

// Apply atomically applies a semantic-change-batch/v2 to one Project.
func (s *Service) Apply(ctx context.Context, projectID string, batch ChangeBatch) (ChangeResult, error) {
	return s.apply(ctx, projectID, "", batch)
}

// ApplyForContinuation applies a trusted Runtime batch using server-bound
// Continuation identity that never enters the semantic payload or result.
func (s *Service) ApplyForContinuation(ctx context.Context, projectID, continuationID string, batch ChangeBatch) (ChangeResult, error) {
	if continuationID == "" {
		return ChangeResult{}, semanticError("authority_denied", "trusted Continuation identity is required", "", nil)
	}
	return s.apply(ctx, projectID, continuationID, batch)
}

// ReconcileContinuationAttempts is a server-only control path that records an
// unexpected Continuation end by moving its valid open Attempts to interrupted
// history. Clean completion is audited elsewhere and never guesses an outcome.
func (s *Service) ReconcileContinuationAttempts(ctx context.Context, projectID, continuationID string) (ChangeResult, error) {
	if continuationID == "" {
		return ChangeResult{}, semanticError("authority_denied", "trusted Continuation identity is required", "", nil)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return ChangeResult{}, fmt.Errorf("begin Attempt reconciliation: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if err := ensureProjectState(ctx, tx, projectID); err != nil {
		return ChangeResult{}, err
	}
	status, err := continuationProjectStatus(ctx, tx, projectID, continuationID)
	if err != nil {
		return ChangeResult{}, err
	}
	if !isTerminalContinuationStatus(status) {
		return ChangeResult{}, semanticError("continuation_not_closed", "Attempt reconciliation requires a terminal Continuation", "", nil)
	}
	revision, err := currentRevision(ctx, tx, projectID)
	if err != nil {
		return ChangeResult{}, err
	}
	if !isUnexpectedTerminalContinuationStatus(status) {
		result := makeChangeResult(revision, nil, nil)
		if err := tx.Commit(); err != nil {
			return ChangeResult{}, fmt.Errorf("commit clean Continuation reconciliation audit: %w", err)
		}
		return result, nil
	}
	rows, err := tx.QueryContext(ctx, `
		SELECT record.key, record.version, record.record_json
		FROM blackboard_v2_records AS record
		JOIN blackboard_v2_attempt_origins AS origin
		  ON origin.project_id = record.project_id AND origin.key = record.key
		WHERE record.project_id = ? AND record.type = 'attempt' AND origin.continuation_id = ?
		ORDER BY record.key ASC`,
		projectID, continuationID,
	)
	if err != nil {
		return ChangeResult{}, fmt.Errorf("read owned Attempts for reconciliation: %w", err)
	}
	owned := make([]storedRecord, 0)
	for rows.Next() {
		var key, raw string
		var version int
		if err := rows.Scan(&key, &version, &raw); err != nil {
			rows.Close()
			return ChangeResult{}, fmt.Errorf("scan owned Attempt for reconciliation: %w", err)
		}
		record, err := decodeStoredRecord("attempt", raw)
		if err != nil {
			rows.Close()
			return ChangeResult{}, fmt.Errorf("decode owned Attempt for reconciliation: %w", err)
		}
		owned = append(owned, storedRecord{key: key, typ: "attempt", version: version, record: record})
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return ChangeResult{}, fmt.Errorf("iterate owned Attempts for reconciliation: %w", err)
	}
	if err := rows.Close(); err != nil {
		return ChangeResult{}, fmt.Errorf("close owned Attempt reconciliation rows: %w", err)
	}

	changedRecords := make(map[string]int)
	now := time.Now().UTC().Format(time.RFC3339Nano)
	for _, existing := range owned {
		tested, err := currentOutgoingRelationshipCount(ctx, tx, projectID, existing.key, "tests")
		if err != nil {
			return ChangeResult{}, err
		}
		if tested == 0 {
			continue
		}
		terminal := existing.record.attemptRecord()
		terminal.Status = "interrupted"
		nextRevision, key, version, changed, err := terminalizeRecord(ctx, tx, projectID, revision, existing, terminal, now)
		if err != nil {
			return ChangeResult{}, err
		}
		if changed {
			revision = nextRevision
			changedRecords[key] = version
		}
	}
	result := makeChangeResult(revision, changedRecords, nil)
	if err := tx.Commit(); err != nil {
		return ChangeResult{}, fmt.Errorf("commit Attempt reconciliation: %w", err)
	}
	return result, nil
}

func (s *Service) apply(ctx context.Context, projectID, continuationID string, batch ChangeBatch) (ChangeResult, error) {
	if batch.Schema != changeBatchSchema {
		return ChangeResult{}, semanticError("invalid_schema", "unsupported semantic change schema", "", nil)
	}
	if batch.IdempotencyKey == "" {
		return ChangeResult{}, semanticError("semantic_validation", "idempotency_key is required", "idempotency_key", nil)
	}
	requestHash, err := canonicalRequestHash(batch)
	if err != nil {
		return ChangeResult{}, err
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return ChangeResult{}, fmt.Errorf("begin Blackboard v2 change: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	if err := ensureProjectState(ctx, tx, projectID); err != nil {
		return ChangeResult{}, err
	}
	continuationStatus := ""
	if continuationID != "" {
		continuationStatus, err = continuationProjectStatus(ctx, tx, projectID, continuationID)
		if err != nil {
			return ChangeResult{}, err
		}
	}
	if replay, ok, err := idempotencyReplay(ctx, tx, projectID, continuationID, batch.IdempotencyKey, requestHash); err != nil {
		return ChangeResult{}, err
	} else if ok {
		if err := tx.Commit(); err != nil {
			return ChangeResult{}, fmt.Errorf("commit Blackboard v2 replay: %w", err)
		}
		return replay, nil
	}
	if continuationID != "" && !continuationCanWrite(continuationStatus) {
		return ChangeResult{}, semanticError("closed_continuation", "trusted Continuation is closed for new Blackboard writes", "", nil)
	}

	revision, err := currentRevision(ctx, tx, projectID)
	if err != nil {
		return ChangeResult{}, err
	}
	changedRecords := make(map[string]int)
	changedRelations := make(map[string]RelationVersionTuple)
	createdThisBatch := make(map[string]bool)
	now := time.Now().UTC().Format(time.RFC3339Nano)
	for index, change := range batch.Changes {
		if err := validateChangeShape(change, index); err != nil {
			return ChangeResult{}, err
		}
		if continuationID != "" {
			if err := validateContinuationChangeOwnership(ctx, tx, projectID, continuationID, change, index); err != nil {
				return ChangeResult{}, err
			}
		}
		switch change.Op {
		case "create":
			newRevision, key, version, changed, err := applyCreateRecord(ctx, tx, projectID, revision, index, change, now)
			if err != nil {
				return ChangeResult{}, err
			}
			if changed {
				revision = newRevision
				changedRecords[key] = version
				createdThisBatch[key] = true
				if continuationID != "" && change.Type == "attempt" {
					if err := bindAttemptOrigin(ctx, tx, projectID, key, continuationID, now); err != nil {
						return ChangeResult{}, err
					}
				}
			}
		case "update":
			newRevision, key, version, changed, err := applyUpdateRecord(ctx, tx, projectID, revision, index, change, now)
			if err != nil {
				return ChangeResult{}, err
			}
			if changed {
				revision = newRevision
				changedRecords[key] = version
			}
		case "relate":
			newRevision, tuple, changed, err := applyRelate(ctx, tx, projectID, revision, index, change, now)
			if err != nil {
				return ChangeResult{}, err
			}
			if changed {
				revision = newRevision
				changedRelations[relationKey(tuple)] = tuple
			}
		case "transition":
			newRevision, key, version, changed, err := applyTransition(ctx, tx, projectID, revision, index, change, now)
			if err != nil {
				return ChangeResult{}, err
			}
			if changed {
				revision = newRevision
				changedRecords[key] = version
			}
		case "supersede":
			newRevision, key, version, tuple, changed, err := applySupersede(ctx, tx, projectID, revision, index, change, createdThisBatch, now)
			if err != nil {
				return ChangeResult{}, err
			}
			if changed {
				revision = newRevision
				changedRecords[key] = version
				changedRelations[relationKey(tuple)] = tuple
			}
		default:
			return ChangeResult{}, semanticError("semantic_validation", "unsupported Blackboard v2 operation", fmt.Sprintf("changes[%d].op", index), nil)
		}
	}

	result := makeChangeResult(revision, changedRecords, changedRelations)
	resultJSON, err := json.Marshal(result)
	if err != nil {
		return ChangeResult{}, fmt.Errorf("encode idempotency result: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO blackboard_v2_idempotency_receipts (project_id, idempotency_key, request_hash, result_json, created_at, continuation_id)
		VALUES (?, ?, ?, ?, ?, ?)`,
		projectID, batch.IdempotencyKey, requestHash, string(resultJSON), now, continuationID,
	); err != nil {
		return ChangeResult{}, fmt.Errorf("store idempotency receipt: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return ChangeResult{}, fmt.Errorf("commit Blackboard v2 change: %w", err)
	}
	return result, nil
}

// ReadCurrent reads one current semantic record by Blackboard Key.
func (s *Service) ReadCurrent(ctx context.Context, projectID, key string) (CurrentDetail, error) {
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return CurrentDetail{}, fmt.Errorf("begin Blackboard v2 read: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if err := ensureProjectExists(ctx, tx, projectID); err != nil {
		return CurrentDetail{}, err
	}
	revision, err := currentRevisionOrZero(ctx, tx, projectID)
	if err != nil {
		return CurrentDetail{}, err
	}
	found, err := loadCurrentRecord(ctx, tx, projectID, key)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return CurrentDetail{}, semanticError("not_found", fmt.Sprintf("%s was not found", key), "key", map[string]any{"key": key})
		}
		return CurrentDetail{}, err
	}
	relationships, err := loadCurrentRelationshipsForKey(ctx, tx, projectID, found.key)
	if err != nil {
		return CurrentDetail{}, err
	}
	return CurrentDetail{
		Schema:        recordSchema,
		Revision:      revision,
		Key:           found.key,
		Type:          found.typ,
		Version:       found.version,
		Record:        found.record,
		Relationships: relationships,
	}, nil
}

// ReadHistory reads prior semantic versions by key with cursor pagination.
func (s *Service) ReadHistory(ctx context.Context, projectID, key string, options HistoryOptions) (SemanticHistory, error) {
	limit := options.Limit
	if limit <= 0 {
		limit = 20
	}
	if limit > 100 {
		limit = 100
	}
	offset, err := parseCursor(options.Cursor)
	if err != nil {
		return SemanticHistory{}, err
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return SemanticHistory{}, fmt.Errorf("begin Blackboard v2 history read: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if err := ensureProjectExists(ctx, tx, projectID); err != nil {
		return SemanticHistory{}, err
	}
	revision, err := currentRevisionOrZero(ctx, tx, projectID)
	if err != nil {
		return SemanticHistory{}, err
	}
	hasCurrent := true
	if _, err := loadCurrentRecord(ctx, tx, projectID, key); err != nil {
		if !errors.Is(err, sql.ErrNoRows) {
			return SemanticHistory{}, err
		}
		hasCurrent = false
	}
	if !hasCurrent {
		var historyCount int
		err := tx.QueryRowContext(ctx, `
			SELECT COUNT(*)
			FROM blackboard_v2_record_history
			WHERE project_id = ? AND key = ?`, projectID, key,
		).Scan(&historyCount)
		if err != nil {
			return SemanticHistory{}, fmt.Errorf("check Blackboard v2 history: %w", err)
		}
		if historyCount == 0 {
			return SemanticHistory{}, semanticError("not_found", fmt.Sprintf("%s was not found", key), "key", map[string]any{"key": key})
		}
	}

	rows, err := tx.QueryContext(ctx, `
		SELECT kind, version, type, record_json, from_key, relation, to_key, reason
		FROM (
			SELECT 0 AS sort_group, recorded_at AS sort_time, 'record' AS kind, version, type, record_json,
			       '' AS from_key, '' AS relation, '' AS to_key, '' AS reason
			FROM blackboard_v2_record_history
			WHERE project_id = ? AND key = ?
			UNION ALL
			SELECT 1 AS sort_group, recorded_at AS sort_time, 'relationship' AS kind, version, '' AS type, '' AS record_json,
			       from_key, relation, to_key, reason
			FROM blackboard_v2_relationship_history
			WHERE project_id = ? AND (from_key = ? OR to_key = ?)
		)
		ORDER BY sort_group ASC, sort_time ASC, version ASC, from_key ASC, relation ASC, to_key ASC
		LIMIT ? OFFSET ?`,
		projectID, key, projectID, key, key, limit+1, offset,
	)
	if err != nil {
		return SemanticHistory{}, fmt.Errorf("read Blackboard v2 history: %w", err)
	}
	defer rows.Close()

	items := make([]HistoryItem, 0, limit)
	for rows.Next() {
		var version int
		var kind, typ, raw, from, relation, to, reason string
		if err := rows.Scan(&kind, &version, &typ, &raw, &from, &relation, &to, &reason); err != nil {
			return SemanticHistory{}, fmt.Errorf("scan Blackboard v2 history: %w", err)
		}
		if len(items) < limit {
			if kind == "record" {
				record, err := decodeStoredRecord(typ, raw)
				if err != nil {
					return SemanticHistory{}, fmt.Errorf("decode Blackboard v2 history record: %w", err)
				}
				items = append(items, HistoryItem{Kind: kind, Version: version, Type: typ, Record: &record})
			} else {
				items = append(items, HistoryItem{Kind: kind, Version: version, From: from, Relation: relation, To: to, Reason: reason})
			}
		}
	}
	if err := rows.Err(); err != nil {
		return SemanticHistory{}, fmt.Errorf("iterate Blackboard v2 history: %w", err)
	}
	next := ""
	if len(items) == limit {
		var extra int
		err := tx.QueryRowContext(ctx, `
			SELECT
				(SELECT COUNT(*) FROM blackboard_v2_record_history WHERE project_id = ? AND key = ?) +
				(SELECT COUNT(*) FROM blackboard_v2_relationship_history WHERE project_id = ? AND (from_key = ? OR to_key = ?))`,
			projectID, key, projectID, key, key,
		).Scan(&extra)
		if err != nil {
			return SemanticHistory{}, fmt.Errorf("count Blackboard v2 history: %w", err)
		}
		if offset+limit < extra {
			next = makeCursor(offset + limit)
		}
	}
	return SemanticHistory{Schema: historySchema, Revision: revision, Key: key, Items: items, NextCursor: next}, nil
}

// RuntimeSnapshot returns the complete current runtime-blackboard/v2 snapshot.
func (s *Service) RuntimeSnapshot(ctx context.Context, projectID string) (RuntimeSnapshot, error) {
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return RuntimeSnapshot{}, fmt.Errorf("begin Blackboard v2 snapshot: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if err := ensureProjectExists(ctx, tx, projectID); err != nil {
		return RuntimeSnapshot{}, err
	}
	revision, err := currentRevisionOrZero(ctx, tx, projectID)
	if err != nil {
		return RuntimeSnapshot{}, err
	}
	rows, err := tx.QueryContext(ctx, `
		SELECT key, type, version, record_json
		FROM blackboard_v2_records
		WHERE project_id = ? AND type IN ('entity', 'objective', 'attempt', 'fact')
		ORDER BY key ASC`, projectID,
	)
	if err != nil {
		return RuntimeSnapshot{}, fmt.Errorf("read Blackboard v2 snapshot records: %w", err)
	}
	defer rows.Close()

	entities := make(map[string]SnapshotEntity)
	objectives := make(map[string]SnapshotObjective)
	attempts := make(map[string]SnapshotAttempt)
	facts := make(map[string]SnapshotFact)
	for rows.Next() {
		var key, typ, raw string
		var version int
		if err := rows.Scan(&key, &typ, &version, &raw); err != nil {
			return RuntimeSnapshot{}, fmt.Errorf("scan Blackboard v2 snapshot record: %w", err)
		}
		record, err := decodeStoredRecord(typ, raw)
		if err != nil {
			return RuntimeSnapshot{}, fmt.Errorf("decode Blackboard v2 snapshot record: %w", err)
		}
		switch typ {
		case "entity":
			entity := record.entityRecord()
			entities[key] = SnapshotEntity{
				Version:       version,
				Status:        entity.Status,
				Kind:          entity.Kind,
				Name:          entity.Name,
				Locator:       entity.Locator,
				Description:   entity.Description,
				ScopeStatus:   entity.ScopeStatus,
				CredentialRef: entity.CredentialRef,
			}
		case "objective":
			objective := record.objectiveRecord()
			objectives[key] = SnapshotObjective{Version: version, Status: objective.Status, Objective: objective.Objective}
		case "attempt":
			attempt := record.attemptRecord()
			attempts[key] = SnapshotAttempt{Version: version, Status: attempt.Status, Summary: attempt.Summary}
		case "fact":
			fact := record.factRecord()
			facts[key] = SnapshotFact{
				Version:     version,
				Category:    fact.Category,
				Summary:     fact.Summary,
				Confidence:  fact.Confidence,
				ScopeStatus: fact.ScopeStatus,
			}
		}
	}
	if err := rows.Err(); err != nil {
		return RuntimeSnapshot{}, fmt.Errorf("iterate Blackboard v2 snapshot records: %w", err)
	}
	work := SnapshotWork{}
	if len(objectives) != 0 {
		work.Objectives = objectives
	}
	if len(attempts) != 0 {
		work.Attempts = attempts
	}
	knowledge := SnapshotKnowledge{}
	if len(entities) != 0 {
		knowledge.Entities = entities
	}
	if len(facts) != 0 {
		knowledge.Facts = facts
	}
	relationships, err := loadAllCurrentRelationships(ctx, tx, projectID)
	if err != nil {
		return RuntimeSnapshot{}, err
	}
	return RuntimeSnapshot{
		Schema:    snapshotSchema,
		Semantics: snapshotSemantics,
		Revision:  revision,
		Work:      work,
		Knowledge: knowledge,
		Relations: relationships,
	}, nil
}

func applyCreateRecord(ctx context.Context, tx *sql.Tx, projectID string, revision, index int, change Change, now string) (int, string, int, bool, error) {
	switch change.Type {
	case "entity":
		return applyCreateEntity(ctx, tx, projectID, revision, index, change, now)
	case "objective":
		return applyCreateObjective(ctx, tx, projectID, revision, index, change, now)
	case "attempt":
		return applyCreateAttempt(ctx, tx, projectID, revision, index, change, now)
	case "fact":
		return applyCreateFact(ctx, tx, projectID, revision, index, change, now)
	default:
		return revision, "", 0, false, semanticError("semantic_validation", "unsupported Blackboard v2 record type in this slice", fmt.Sprintf("changes[%d].type", index), nil)
	}
}

func applyUpdateRecord(ctx context.Context, tx *sql.Tx, projectID string, revision, index int, change Change, now string) (int, string, int, bool, error) {
	switch change.Type {
	case "entity":
		return applyUpdateEntity(ctx, tx, projectID, revision, index, change, now)
	case "objective":
		return applyUpdateObjective(ctx, tx, projectID, revision, index, change, now)
	case "attempt":
		return applyUpdateAttempt(ctx, tx, projectID, revision, index, change, now)
	case "fact":
		return applyUpdateFact(ctx, tx, projectID, revision, index, change, now)
	default:
		return revision, "", 0, false, semanticError("semantic_validation", "unsupported Blackboard v2 record type in this slice", fmt.Sprintf("changes[%d].type", index), nil)
	}
}

func applyCreateObjective(ctx context.Context, tx *sql.Tx, projectID string, revision, index int, change Change, now string) (int, string, int, bool, error) {
	path := fmt.Sprintf("changes[%d]", index)
	if err := validateKey(change.Key, path+".key"); err != nil {
		return revision, "", 0, false, err
	}
	record, err := completeObjectiveRecord(change.Record, path+".record")
	if err != nil {
		return revision, "", 0, false, err
	}
	if err := validateObjectiveRecord(record, path+".record"); err != nil {
		return revision, "", 0, false, err
	}
	existing, err := loadCurrentRecord(ctx, tx, projectID, change.Key)
	if err == nil {
		if existing.typ == "objective" && objectivesEqual(existing.record.objectiveRecord(), record) {
			return revision, change.Key, existing.version, false, nil
		}
		return revision, "", 0, false, semanticError("key_conflict", fmt.Sprintf("%s already exists", change.Key), path+".key", map[string]any{"key": change.Key})
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return revision, "", 0, false, err
	}
	if used, err := historicalKeyExists(ctx, tx, projectID, change.Key); err != nil {
		return revision, "", 0, false, err
	} else if used {
		return revision, "", 0, false, semanticError("key_conflict", fmt.Sprintf("%s already exists in Semantic History", change.Key), path+".key", map[string]any{"key": change.Key})
	}
	return insertCurrentWorkRecord(ctx, tx, projectID, revision, change.Key, "objective", record, now)
}

func applyCreateAttempt(ctx context.Context, tx *sql.Tx, projectID string, revision, index int, change Change, now string) (int, string, int, bool, error) {
	path := fmt.Sprintf("changes[%d]", index)
	if err := validateKey(change.Key, path+".key"); err != nil {
		return revision, "", 0, false, err
	}
	record, err := completeAttemptRecord(change.Record, path+".record")
	if err != nil {
		return revision, "", 0, false, err
	}
	if err := validateAttemptRecord(record, path+".record"); err != nil {
		return revision, "", 0, false, err
	}
	existing, err := loadCurrentRecord(ctx, tx, projectID, change.Key)
	if err == nil {
		if existing.typ == "attempt" && attemptsEqual(existing.record.attemptRecord(), record) {
			return revision, change.Key, existing.version, false, nil
		}
		return revision, "", 0, false, semanticError("key_conflict", fmt.Sprintf("%s already exists", change.Key), path+".key", map[string]any{"key": change.Key})
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return revision, "", 0, false, err
	}
	if used, err := historicalKeyExists(ctx, tx, projectID, change.Key); err != nil {
		return revision, "", 0, false, err
	} else if used {
		return revision, "", 0, false, semanticError("key_conflict", fmt.Sprintf("%s already exists in Semantic History", change.Key), path+".key", map[string]any{"key": change.Key})
	}
	return insertCurrentWorkRecord(ctx, tx, projectID, revision, change.Key, "attempt", record, now)
}

func insertCurrentWorkRecord(ctx context.Context, tx *sql.Tx, projectID string, revision int, key, typ string, record any, now string) (int, string, int, bool, error) {
	recordJSON, err := json.Marshal(record)
	if err != nil {
		return revision, "", 0, false, fmt.Errorf("encode %s record: %w", typ, err)
	}
	nextRevision, err := incrementRevision(ctx, tx, projectID, revision)
	if err != nil {
		return revision, "", 0, false, err
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO blackboard_v2_records (project_id, key, type, version, record_json, created_at, updated_at)
		VALUES (?, ?, ?, 1, ?, ?, ?)`,
		projectID, key, typ, string(recordJSON), now, now,
	); err != nil {
		return revision, "", 0, false, fmt.Errorf("store Blackboard v2 %s: %w", typ, err)
	}
	return nextRevision, key, 1, true, nil
}

func applyCreateEntity(ctx context.Context, tx *sql.Tx, projectID string, revision, index int, change Change, now string) (int, string, int, bool, error) {
	if err := validateKey(change.Key, fmt.Sprintf("changes[%d].key", index)); err != nil {
		return revision, "", 0, false, err
	}
	record, err := completeEntityRecord(change.Record, fmt.Sprintf("changes[%d].record", index))
	if err != nil {
		return revision, "", 0, false, err
	}
	if err := validateEntityRecord(record, fmt.Sprintf("changes[%d].record", index)); err != nil {
		return revision, "", 0, false, err
	}
	existing, err := loadCurrentRecord(ctx, tx, projectID, change.Key)
	if err == nil {
		if existing.typ == "entity" && entitiesEqual(existing.record.entityRecord(), record) {
			return revision, change.Key, existing.version, false, nil
		}
		return revision, "", 0, false, semanticError("key_conflict", fmt.Sprintf("%s already exists", change.Key), fmt.Sprintf("changes[%d].key", index), map[string]any{"key": change.Key})
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return revision, "", 0, false, err
	}
	if used, err := historicalKeyExists(ctx, tx, projectID, change.Key); err != nil {
		return revision, "", 0, false, err
	} else if used {
		return revision, "", 0, false, semanticError("key_conflict", fmt.Sprintf("%s already exists in Semantic History", change.Key), fmt.Sprintf("changes[%d].key", index), map[string]any{"key": change.Key})
	}
	recordJSON, err := json.Marshal(record)
	if err != nil {
		return revision, "", 0, false, fmt.Errorf("encode Entity record: %w", err)
	}
	nextRevision, err := incrementRevision(ctx, tx, projectID, revision)
	if err != nil {
		return revision, "", 0, false, err
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO blackboard_v2_records (project_id, key, type, version, record_json, created_at, updated_at)
		VALUES (?, ?, 'entity', 1, ?, ?, ?)`,
		projectID, change.Key, string(recordJSON), now, now,
	); err != nil {
		return revision, "", 0, false, fmt.Errorf("store Blackboard v2 Entity: %w", err)
	}
	return nextRevision, change.Key, 1, true, nil
}

func applyCreateFact(ctx context.Context, tx *sql.Tx, projectID string, revision, index int, change Change, now string) (int, string, int, bool, error) {
	if change.Type != "fact" {
		return revision, "", 0, false, semanticError("semantic_validation", "create supports only Project Facts in this slice", fmt.Sprintf("changes[%d].type", index), nil)
	}
	if err := validateKey(change.Key, fmt.Sprintf("changes[%d].key", index)); err != nil {
		return revision, "", 0, false, err
	}
	record, err := completeFactRecord(change.Record, fmt.Sprintf("changes[%d].record", index))
	if err != nil {
		return revision, "", 0, false, err
	}
	if err := validateFactRecord(record, fmt.Sprintf("changes[%d].record", index)); err != nil {
		return revision, "", 0, false, err
	}
	existing, err := loadCurrentRecord(ctx, tx, projectID, change.Key)
	if err == nil {
		if existing.typ == "fact" && factsEqual(existing.record.factRecord(), record) {
			return revision, change.Key, existing.version, false, nil
		}
		return revision, "", 0, false, semanticError("key_conflict", fmt.Sprintf("%s already exists", change.Key), fmt.Sprintf("changes[%d].key", index), map[string]any{"key": change.Key})
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return revision, "", 0, false, err
	}
	if used, err := historicalKeyExists(ctx, tx, projectID, change.Key); err != nil {
		return revision, "", 0, false, err
	} else if used {
		return revision, "", 0, false, semanticError("key_conflict", fmt.Sprintf("%s already exists in Semantic History", change.Key), fmt.Sprintf("changes[%d].key", index), map[string]any{"key": change.Key})
	}
	recordJSON, err := json.Marshal(record)
	if err != nil {
		return revision, "", 0, false, fmt.Errorf("encode Fact record: %w", err)
	}
	nextRevision, err := incrementRevision(ctx, tx, projectID, revision)
	if err != nil {
		return revision, "", 0, false, err
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO blackboard_v2_records (project_id, key, type, version, record_json, created_at, updated_at)
		VALUES (?, ?, 'fact', 1, ?, ?, ?)`,
		projectID, change.Key, string(recordJSON), now, now,
	); err != nil {
		return revision, "", 0, false, fmt.Errorf("store Blackboard v2 Fact: %w", err)
	}
	return nextRevision, change.Key, 1, true, nil
}

func applyUpdateEntity(ctx context.Context, tx *sql.Tx, projectID string, revision, index int, change Change, now string) (int, string, int, bool, error) {
	if err := validateKey(change.Key, fmt.Sprintf("changes[%d].key", index)); err != nil {
		return revision, "", 0, false, err
	}
	existing, err := loadCurrentRecord(ctx, tx, projectID, change.Key)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return revision, "", 0, false, semanticError("not_found", fmt.Sprintf("%s was not found", change.Key), fmt.Sprintf("changes[%d].key", index), map[string]any{"key": change.Key})
		}
		return revision, "", 0, false, err
	}
	if existing.typ != "entity" {
		return revision, "", 0, false, semanticError("semantic_validation", "record type mismatch", fmt.Sprintf("changes[%d].type", index), map[string]any{"key": change.Key})
	}
	if change.Version != existing.version {
		return revision, "", 0, false, semanticError(
			"version_conflict",
			fmt.Sprintf("%s changed", change.Key),
			fmt.Sprintf("changes[%d].version", index),
			map[string]any{
				"key":              change.Key,
				"expected_version": float64(change.Version),
				"current_version":  float64(existing.version),
				"next_action":      "read_current_record",
			},
		)
	}
	patch, err := partialEntityRecord(change.Record, fmt.Sprintf("changes[%d].record", index))
	if err != nil {
		return revision, "", 0, false, err
	}
	nextRecord, err := applyEntityPatch(existing.record.entityRecord(), patch, change.Clear, fmt.Sprintf("changes[%d].clear", index))
	if err != nil {
		return revision, "", 0, false, err
	}
	if err := validateEntityRecord(nextRecord, fmt.Sprintf("changes[%d].record", index)); err != nil {
		return revision, "", 0, false, err
	}
	if entitiesEqual(existing.record.entityRecord(), nextRecord) {
		return revision, change.Key, existing.version, false, nil
	}
	historyJSON, err := json.Marshal(existing.record.entityRecord())
	if err != nil {
		return revision, "", 0, false, fmt.Errorf("encode prior Entity record: %w", err)
	}
	nextJSON, err := json.Marshal(nextRecord)
	if err != nil {
		return revision, "", 0, false, fmt.Errorf("encode updated Entity record: %w", err)
	}
	nextRevision, err := incrementRevision(ctx, tx, projectID, revision)
	if err != nil {
		return revision, "", 0, false, err
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO blackboard_v2_record_history (project_id, key, version, type, record_json, recorded_at)
		VALUES (?, ?, ?, 'entity', ?, ?)`,
		projectID, change.Key, existing.version, string(historyJSON), now,
	); err != nil {
		return revision, "", 0, false, fmt.Errorf("store prior Blackboard v2 Entity: %w", err)
	}
	nextVersion := existing.version + 1
	if _, err := tx.ExecContext(ctx, `
		UPDATE blackboard_v2_records
		SET version = ?, record_json = ?, updated_at = ?
		WHERE project_id = ? AND key = ?`,
		nextVersion, string(nextJSON), now, projectID, change.Key,
	); err != nil {
		return revision, "", 0, false, fmt.Errorf("update Blackboard v2 Entity: %w", err)
	}
	return nextRevision, change.Key, nextVersion, true, nil
}

func applyUpdateObjective(ctx context.Context, tx *sql.Tx, projectID string, revision, index int, change Change, now string) (int, string, int, bool, error) {
	path := fmt.Sprintf("changes[%d]", index)
	existing, err := currentRecordForUpdate(ctx, tx, projectID, change, "objective", path)
	if err != nil {
		return revision, "", 0, false, err
	}
	if len(change.Clear) != 0 {
		return revision, "", 0, false, semanticError("semantic_validation", "Objective update does not accept clear", path+".clear", nil)
	}
	patch, err := partialObjectiveRecord(change.Record, path+".record")
	if err != nil {
		return revision, "", 0, false, err
	}
	nextRecord := existing.record.objectiveRecord()
	if patch.Objective != nil {
		nextRecord.Objective = *patch.Objective
	}
	if err := validateObjectiveRecord(nextRecord, path+".record"); err != nil {
		return revision, "", 0, false, err
	}
	if objectivesEqual(existing.record.objectiveRecord(), nextRecord) {
		return revision, change.Key, existing.version, false, nil
	}
	return replaceCurrentWorkRecord(ctx, tx, projectID, revision, existing, nextRecord, now)
}

func applyUpdateAttempt(ctx context.Context, tx *sql.Tx, projectID string, revision, index int, change Change, now string) (int, string, int, bool, error) {
	path := fmt.Sprintf("changes[%d]", index)
	existing, err := currentRecordForUpdate(ctx, tx, projectID, change, "attempt", path)
	if err != nil {
		return revision, "", 0, false, err
	}
	if len(change.Clear) != 0 {
		return revision, "", 0, false, semanticError("semantic_validation", "Attempt update does not accept clear", path+".clear", nil)
	}
	patch, err := partialAttemptRecord(change.Record, path+".record")
	if err != nil {
		return revision, "", 0, false, err
	}
	nextRecord := existing.record.attemptRecord()
	if patch.Summary != nil {
		nextRecord.Summary = *patch.Summary
	}
	if err := validateAttemptRecord(nextRecord, path+".record"); err != nil {
		return revision, "", 0, false, err
	}
	if attemptsEqual(existing.record.attemptRecord(), nextRecord) {
		return revision, change.Key, existing.version, false, nil
	}
	return replaceCurrentWorkRecord(ctx, tx, projectID, revision, existing, nextRecord, now)
}

func currentRecordForUpdate(ctx context.Context, tx *sql.Tx, projectID string, change Change, typ, path string) (storedRecord, error) {
	if err := validateKey(change.Key, path+".key"); err != nil {
		return storedRecord{}, err
	}
	existing, err := loadCurrentRecord(ctx, tx, projectID, change.Key)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return storedRecord{}, semanticError("not_found", fmt.Sprintf("%s was not found", change.Key), path+".key", map[string]any{"key": change.Key})
		}
		return storedRecord{}, err
	}
	if existing.typ != typ {
		return storedRecord{}, semanticError("semantic_validation", "record type mismatch", path+".type", map[string]any{"key": change.Key})
	}
	if change.Version != existing.version {
		return storedRecord{}, semanticError(
			"version_conflict",
			fmt.Sprintf("%s changed", change.Key),
			path+".version",
			map[string]any{"key": change.Key, "expected_version": float64(change.Version), "current_version": float64(existing.version), "next_action": "read_current_record"},
		)
	}
	return existing, nil
}

func replaceCurrentWorkRecord(ctx context.Context, tx *sql.Tx, projectID string, revision int, existing storedRecord, nextRecord any, now string) (int, string, int, bool, error) {
	historyJSON, err := json.Marshal(existing.record)
	if err != nil {
		return revision, "", 0, false, fmt.Errorf("encode prior %s record: %w", existing.typ, err)
	}
	nextJSON, err := json.Marshal(nextRecord)
	if err != nil {
		return revision, "", 0, false, fmt.Errorf("encode updated %s record: %w", existing.typ, err)
	}
	nextRevision, err := incrementRevision(ctx, tx, projectID, revision)
	if err != nil {
		return revision, "", 0, false, err
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO blackboard_v2_record_history (project_id, key, version, type, record_json, recorded_at)
		VALUES (?, ?, ?, ?, ?, ?)`,
		projectID, existing.key, existing.version, existing.typ, string(historyJSON), now,
	); err != nil {
		return revision, "", 0, false, fmt.Errorf("store prior Blackboard v2 %s: %w", existing.typ, err)
	}
	nextVersion := existing.version + 1
	if _, err := tx.ExecContext(ctx, `
		UPDATE blackboard_v2_records
		SET version = ?, record_json = ?, updated_at = ?
		WHERE project_id = ? AND key = ?`,
		nextVersion, string(nextJSON), now, projectID, existing.key,
	); err != nil {
		return revision, "", 0, false, fmt.Errorf("update Blackboard v2 %s: %w", existing.typ, err)
	}
	return nextRevision, existing.key, nextVersion, true, nil
}

func applyUpdateFact(ctx context.Context, tx *sql.Tx, projectID string, revision, index int, change Change, now string) (int, string, int, bool, error) {
	if change.Type != "fact" {
		return revision, "", 0, false, semanticError("semantic_validation", "update supports only Project Facts in this slice", fmt.Sprintf("changes[%d].type", index), nil)
	}
	if err := validateKey(change.Key, fmt.Sprintf("changes[%d].key", index)); err != nil {
		return revision, "", 0, false, err
	}
	existing, err := loadCurrentRecord(ctx, tx, projectID, change.Key)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return revision, "", 0, false, semanticError("not_found", fmt.Sprintf("%s was not found", change.Key), fmt.Sprintf("changes[%d].key", index), map[string]any{"key": change.Key})
		}
		return revision, "", 0, false, err
	}
	if existing.typ != "fact" {
		return revision, "", 0, false, semanticError("semantic_validation", "record type mismatch", fmt.Sprintf("changes[%d].type", index), map[string]any{"key": change.Key})
	}
	if change.Version != existing.version {
		return revision, "", 0, false, semanticError(
			"version_conflict",
			fmt.Sprintf("%s changed", change.Key),
			fmt.Sprintf("changes[%d].version", index),
			map[string]any{
				"key":              change.Key,
				"expected_version": float64(change.Version),
				"current_version":  float64(existing.version),
				"next_action":      "read_current_record",
			},
		)
	}
	patch, err := partialFactRecord(change.Record, fmt.Sprintf("changes[%d].record", index))
	if err != nil {
		return revision, "", 0, false, err
	}
	nextRecord, err := applyFactPatch(existing.record.factRecord(), patch, change.Clear, fmt.Sprintf("changes[%d].clear", index))
	if err != nil {
		return revision, "", 0, false, err
	}
	if err := validateFactRecord(nextRecord, fmt.Sprintf("changes[%d].record", index)); err != nil {
		return revision, "", 0, false, err
	}
	if factsEqual(existing.record.factRecord(), nextRecord) {
		return revision, change.Key, existing.version, false, nil
	}
	historyJSON, err := json.Marshal(existing.record.factRecord())
	if err != nil {
		return revision, "", 0, false, fmt.Errorf("encode prior Fact record: %w", err)
	}
	nextJSON, err := json.Marshal(nextRecord)
	if err != nil {
		return revision, "", 0, false, fmt.Errorf("encode updated Fact record: %w", err)
	}
	nextRevision, err := incrementRevision(ctx, tx, projectID, revision)
	if err != nil {
		return revision, "", 0, false, err
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO blackboard_v2_record_history (project_id, key, version, type, record_json, recorded_at)
		VALUES (?, ?, ?, 'fact', ?, ?)`,
		projectID, change.Key, existing.version, string(historyJSON), now,
	); err != nil {
		return revision, "", 0, false, fmt.Errorf("store prior Blackboard v2 Fact: %w", err)
	}
	nextVersion := existing.version + 1
	if _, err := tx.ExecContext(ctx, `
		UPDATE blackboard_v2_records
		SET version = ?, record_json = ?, updated_at = ?
		WHERE project_id = ? AND key = ?`,
		nextVersion, string(nextJSON), now, projectID, change.Key,
	); err != nil {
		return revision, "", 0, false, fmt.Errorf("update Blackboard v2 Fact: %w", err)
	}
	return nextRevision, change.Key, nextVersion, true, nil
}

func applyRelate(ctx context.Context, tx *sql.Tx, projectID string, revision, index int, change Change, now string) (int, RelationVersionTuple, bool, error) {
	path := fmt.Sprintf("changes[%d]", index)
	if err := validateKey(change.From, path+".from"); err != nil {
		return revision, RelationVersionTuple{}, false, err
	}
	if err := validateKey(change.To, path+".to"); err != nil {
		return revision, RelationVersionTuple{}, false, err
	}
	if change.From == change.To {
		return revision, RelationVersionTuple{}, false, semanticError("semantic_validation", "relationship self-links are invalid", path+".to", nil)
	}
	if isReasonRelation(change.Relation) && change.Reason != "" {
		if err := validateConciseText(change.Reason, path+".reason"); err != nil {
			return revision, RelationVersionTuple{}, false, err
		}
	} else if change.Reason != "" {
		return revision, RelationVersionTuple{}, false, semanticError("semantic_validation", "relationship reason is not allowed for this relation", path+".reason", nil)
	}
	fromRecord, err := loadCurrentRecord(ctx, tx, projectID, change.From)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return revision, RelationVersionTuple{}, false, semanticError("not_found", fmt.Sprintf("%s was not found", change.From), path+".from", map[string]any{"key": change.From})
		}
		return revision, RelationVersionTuple{}, false, err
	}
	toRecord, err := loadCurrentRecord(ctx, tx, projectID, change.To)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return revision, RelationVersionTuple{}, false, semanticError("not_found", fmt.Sprintf("%s was not found", change.To), path+".to", map[string]any{"key": change.To})
		}
		return revision, RelationVersionTuple{}, false, err
	}
	if err := validateRelationshipEndpoint(change.Relation, fromRecord.typ, toRecord.typ, path+".relation"); err != nil {
		return revision, RelationVersionTuple{}, false, err
	}
	if change.Relation == "part_of" || change.Relation == "derived_from" || change.Relation == "depends_on" {
		wouldCycle, err := relationshipWouldCycle(ctx, tx, projectID, change.Relation, change.From, change.To)
		if err != nil {
			return revision, RelationVersionTuple{}, false, err
		}
		if wouldCycle {
			return revision, RelationVersionTuple{}, false, semanticError("semantic_validation", fmt.Sprintf("%s relationships must be acyclic", change.Relation), path+".to", nil)
		}
	}

	var existingVersion int
	var existingReason string
	err = tx.QueryRowContext(ctx, `
		SELECT version, reason
		FROM blackboard_v2_relationships
		WHERE project_id = ? AND from_key = ? AND relation = ? AND to_key = ?`,
		projectID, change.From, change.Relation, change.To,
	).Scan(&existingVersion, &existingReason)
	if err == nil {
		if existingReason == change.Reason {
			return revision, RelationVersionTuple{change.From, change.Relation, change.To, existingVersion}, false, nil
		}
		if change.Version != existingVersion {
			return revision, RelationVersionTuple{}, false, semanticError("version_conflict", "relationship changed", path+".version", map[string]any{
				"from": change.From, "relation": change.Relation, "to": change.To, "expected_version": float64(change.Version), "current_version": float64(existingVersion), "next_action": "read_current_record",
			})
		}
		return revision, RelationVersionTuple{}, false, semanticError("semantic_validation", "relationship reason updates are implemented by the relation lifecycle slice", path+".reason", nil)
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return revision, RelationVersionTuple{}, false, fmt.Errorf("read Blackboard v2 relationship: %w", err)
	}
	if change.Version != 0 {
		return revision, RelationVersionTuple{}, false, semanticError("semantic_validation", "relationship version is not accepted for a new relation", path+".version", nil)
	}
	nextRevision, err := incrementRevision(ctx, tx, projectID, revision)
	if err != nil {
		return revision, RelationVersionTuple{}, false, err
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO blackboard_v2_relationships (project_id, from_key, relation, to_key, version, reason, created_at, updated_at)
		VALUES (?, ?, ?, ?, 1, ?, ?, ?)`,
		projectID, change.From, change.Relation, change.To, change.Reason, now, now,
	); err != nil {
		return revision, RelationVersionTuple{}, false, fmt.Errorf("store Blackboard v2 relationship: %w", err)
	}
	return nextRevision, RelationVersionTuple{change.From, change.Relation, change.To, 1}, true, nil
}

func applyTransition(ctx context.Context, tx *sql.Tx, projectID string, revision, index int, change Change, now string) (int, string, int, bool, error) {
	path := fmt.Sprintf("changes[%d]", index)
	if err := validateKey(change.Key, path+".key"); err != nil {
		return revision, "", 0, false, err
	}
	existing, err := loadCurrentRecord(ctx, tx, projectID, change.Key)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return revision, "", 0, false, semanticError("not_found", fmt.Sprintf("%s was not found", change.Key), path+".key", map[string]any{"key": change.Key})
		}
		return revision, "", 0, false, err
	}
	if change.Version != existing.version {
		return revision, "", 0, false, semanticError(
			"version_conflict",
			fmt.Sprintf("%s changed", change.Key),
			path+".version",
			map[string]any{
				"key":              change.Key,
				"expected_version": float64(change.Version),
				"current_version":  float64(existing.version),
				"next_action":      "read_current_record",
			},
		)
	}

	switch existing.typ {
	case "entity":
		if change.Status != "retired" {
			return revision, "", 0, false, semanticError("semantic_validation", "Entity transition status must be retired", path+".status", nil)
		}
		if err := validateConciseText(change.ResolutionSummary, path+".resolution_summary"); err != nil {
			return revision, "", 0, false, err
		}
		terminal := existing.record.entityRecord()
		terminal.Status = "retired"
		return terminalizeRecord(ctx, tx, projectID, revision, existing, terminal, now)
	case "objective":
		if change.Status != "resolved" && change.Status != "abandoned" {
			return revision, "", 0, false, semanticError("semantic_validation", "Objective transition status must be resolved or abandoned", path+".status", nil)
		}
		if err := validateConciseText(change.ResolutionSummary, path+".resolution_summary"); err != nil {
			return revision, "", 0, false, err
		}
		if change.Status == "resolved" {
			satisfied, err := hasIncomingSatisfies(ctx, tx, projectID, change.Key)
			if err != nil {
				return revision, "", 0, false, err
			}
			if !satisfied {
				return revision, "", 0, false, semanticError("semantic_validation", "resolved Objective requires a current incoming satisfies relationship", path+".status", nil)
			}
		}
		// Abandonment has no relationship proof guard in this slice. The v2
		// grammar has no edge that means "invalidation basis", and the spec
		// explicitly permits meaningless invalidations without manufacturing a
		// Fact. Its only canonical meaning is the terminal resolution_summary
		// retained in Semantic History.
		terminal := existing.record.objectiveRecord()
		terminal.Status = change.Status
		terminal.ResolutionSummary = change.ResolutionSummary
		return terminalizeRecord(ctx, tx, projectID, revision, existing, terminal, now)
	case "attempt":
		if !isOneOf(change.Status, "succeeded", "failed", "blocked", "inconclusive") {
			if change.Status == "interrupted" {
				return revision, "", 0, false, semanticError("semantic_validation", "Runtime interruption is reconciled by the server", path+".status", nil)
			}
			return revision, "", 0, false, semanticError("semantic_validation", "Attempt transition status is not terminal", path+".status", nil)
		}
		if err := validateSemanticText(change.Summary, path+".summary"); err != nil {
			return revision, "", 0, false, err
		}
		tested, err := currentOutgoingRelationshipCount(ctx, tx, projectID, change.Key, "tests")
		if err != nil {
			return revision, "", 0, false, err
		}
		if tested == 0 {
			return revision, "", 0, false, semanticError("semantic_validation", "terminal Attempt requires at least one tested target", path+".status", nil)
		}
		if change.Status == "succeeded" {
			produced, err := currentOutgoingRelationshipCount(ctx, tx, projectID, change.Key, "produced")
			if err != nil {
				return revision, "", 0, false, err
			}
			if produced == 0 {
				return revision, "", 0, false, semanticError("semantic_validation", "succeeded Attempt requires at least one produced outcome", path+".status", nil)
			}
		}
		terminal := existing.record.attemptRecord()
		terminal.Status = change.Status
		terminal.Summary = change.Summary
		return terminalizeRecord(ctx, tx, projectID, revision, existing, terminal, now)
	case "fact":
		if !isOneOf(change.Status, "tentative", "confirmed") {
			return revision, "", 0, false, semanticError("semantic_validation", "Fact transition status must be tentative or confirmed", path+".status", nil)
		}
		current := existing.record.factRecord()
		if current.Confidence == change.Status {
			return revision, change.Key, existing.version, false, nil
		}
		next := current
		next.Confidence = change.Status
		if err := validateFactRecord(next, path+".status"); err != nil {
			return revision, "", 0, false, err
		}
		return replaceCurrentWorkRecord(ctx, tx, projectID, revision, existing, next, now)
	default:
		return revision, "", 0, false, semanticError("semantic_validation", "record type does not support this transition", path+".key", map[string]any{"key": change.Key, "type": existing.typ})
	}
}

func currentOutgoingRelationshipCount(ctx context.Context, tx *sql.Tx, projectID, fromKey, relation string) (int, error) {
	var count int
	if err := tx.QueryRowContext(ctx, `
		SELECT COUNT(*)
		FROM blackboard_v2_relationships
		WHERE project_id = ? AND from_key = ? AND relation = ?`,
		projectID, fromKey, relation,
	).Scan(&count); err != nil {
		return 0, fmt.Errorf("count current %s relationships: %w", relation, err)
	}
	return count, nil
}

func hasIncomingSatisfies(ctx context.Context, tx *sql.Tx, projectID, objectiveKey string) (bool, error) {
	var exists int
	if err := tx.QueryRowContext(ctx, `
		SELECT EXISTS(
			SELECT 1
			FROM blackboard_v2_relationships AS rel
			JOIN blackboard_v2_records AS source
			  ON source.project_id = rel.project_id AND source.key = rel.from_key
			WHERE rel.project_id = ? AND rel.relation = 'satisfies' AND rel.to_key = ?
			  AND source.type IN ('fact', 'finding', 'solution')
		)`,
		projectID, objectiveKey,
	).Scan(&exists); err != nil {
		return false, fmt.Errorf("check Objective satisfaction: %w", err)
	}
	return exists == 1, nil
}

func terminalizeRecord(ctx context.Context, tx *sql.Tx, projectID string, revision int, existing storedRecord, terminal any, now string) (int, string, int, bool, error) {
	currentJSON, err := json.Marshal(existing.record)
	if err != nil {
		return revision, "", 0, false, fmt.Errorf("encode current %s record: %w", existing.typ, err)
	}
	terminalJSON, err := json.Marshal(terminal)
	if err != nil {
		return revision, "", 0, false, fmt.Errorf("encode terminal %s record: %w", existing.typ, err)
	}
	nextVersion := existing.version + 1
	nextRevision, err := incrementRevision(ctx, tx, projectID, revision)
	if err != nil {
		return revision, "", 0, false, err
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO blackboard_v2_record_history (project_id, key, version, type, record_json, recorded_at)
		VALUES (?, ?, ?, ?, ?, ?)`,
		projectID, existing.key, existing.version, existing.typ, string(currentJSON), now,
	); err != nil {
		return revision, "", 0, false, fmt.Errorf("store current %s history: %w", existing.typ, err)
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO blackboard_v2_record_history (project_id, key, version, type, record_json, recorded_at)
		VALUES (?, ?, ?, ?, ?, ?)`,
		projectID, existing.key, nextVersion, existing.typ, string(terminalJSON), now,
	); err != nil {
		return revision, "", 0, false, fmt.Errorf("store terminal %s history: %w", existing.typ, err)
	}
	if err := moveCurrentRelationshipsToHistory(ctx, tx, projectID, existing.key); err != nil {
		return revision, "", 0, false, err
	}
	if _, err := tx.ExecContext(ctx, `
		DELETE FROM blackboard_v2_records
		WHERE project_id = ? AND key = ?`,
		projectID, existing.key,
	); err != nil {
		return revision, "", 0, false, fmt.Errorf("remove terminal %s current record: %w", existing.typ, err)
	}
	return nextRevision, existing.key, nextVersion, true, nil
}

func applySupersede(ctx context.Context, tx *sql.Tx, projectID string, revision, index int, change Change, createdThisBatch map[string]bool, now string) (int, string, int, RelationVersionTuple, bool, error) {
	path := fmt.Sprintf("changes[%d]", index)
	if err := validateKey(change.Replacement, path+".replacement"); err != nil {
		return revision, "", 0, RelationVersionTuple{}, false, err
	}
	if err := validateKey(change.Replaced, path+".replaced"); err != nil {
		return revision, "", 0, RelationVersionTuple{}, false, err
	}
	if change.Replacement == change.Replaced {
		return revision, "", 0, RelationVersionTuple{}, false, semanticError("semantic_validation", "supersede requires different replacement and replaced records", path+".replaced", nil)
	}
	replacement, err := loadCurrentRecord(ctx, tx, projectID, change.Replacement)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return revision, "", 0, RelationVersionTuple{}, false, semanticError("not_found", fmt.Sprintf("%s was not found", change.Replacement), path+".replacement", map[string]any{"key": change.Replacement})
		}
		return revision, "", 0, RelationVersionTuple{}, false, err
	}
	replaced, err := loadCurrentRecord(ctx, tx, projectID, change.Replaced)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return revision, "", 0, RelationVersionTuple{}, false, semanticError("not_found", fmt.Sprintf("%s was not found", change.Replaced), path+".replaced", map[string]any{"key": change.Replaced})
		}
		return revision, "", 0, RelationVersionTuple{}, false, err
	}
	if replacement.typ != replaced.typ || !isOneOf(replacement.typ, "entity", "objective") {
		return revision, "", 0, RelationVersionTuple{}, false, semanticError("semantic_validation", "supersede requires two current records of the same supersedable type", path, map[string]any{"replacement_type": replacement.typ, "replaced_type": replaced.typ})
	}
	replacementVersion := change.ReplacementVersion
	if replacementVersion == 0 {
		if !createdThisBatch[change.Replacement] || replacement.version != 1 {
			return revision, "", 0, RelationVersionTuple{}, false, semanticError("semantic_validation", "replacement_version may be omitted only for a version 1 replacement created earlier in the same batch", path+".replacement_version", nil)
		}
		replacementVersion = 1
	}
	if replacementVersion != replacement.version {
		return revision, "", 0, RelationVersionTuple{}, false, semanticError(
			"version_conflict",
			fmt.Sprintf("%s changed", change.Replacement),
			path+".replacement_version",
			map[string]any{"key": change.Replacement, "expected_version": float64(replacementVersion), "current_version": float64(replacement.version), "next_action": "read_current_record"},
		)
	}
	if change.ReplacedVersion != replaced.version {
		return revision, "", 0, RelationVersionTuple{}, false, semanticError(
			"version_conflict",
			fmt.Sprintf("%s changed", change.Replaced),
			path+".replaced_version",
			map[string]any{"key": change.Replaced, "expected_version": float64(change.ReplacedVersion), "current_version": float64(replaced.version), "next_action": "read_current_record"},
		)
	}
	var terminal any
	switch replaced.typ {
	case "entity":
		record := replaced.record.entityRecord()
		record.Status = "superseded"
		terminal = record
	case "objective":
		record := replaced.record.objectiveRecord()
		record.Status = "superseded"
		terminal = record
	}
	nextRevision, key, nextVersion, changed, err := terminalizeRecord(ctx, tx, projectID, revision, replaced, terminal, now)
	if err != nil {
		return revision, "", 0, RelationVersionTuple{}, false, err
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO blackboard_v2_relationship_history (project_id, from_key, relation, to_key, version, reason, recorded_at)
		VALUES (?, ?, 'supersedes', ?, 1, '', ?)`,
		projectID, change.Replacement, change.Replaced, now,
	); err != nil {
		return revision, "", 0, RelationVersionTuple{}, false, fmt.Errorf("store supersedes relationship history: %w", err)
	}
	tuple := RelationVersionTuple{change.Replacement, "supersedes", change.Replaced, 1}
	return nextRevision, key, nextVersion, tuple, changed, nil
}

func validateRelationshipEndpoint(relation, fromType, toType, path string) error {
	// This semantic kernel admits only endpoint types whose records are fully
	// decodable in the current slice. Finding, Solution, and Evidence endpoints
	// are added by their owning tickets alongside their complete record schemas.
	switch relation {
	case "about":
		if toType == "entity" && isOneOf(fromType, "objective", "attempt", "fact") {
			return nil
		}
		return semanticError("semantic_validation", "about must connect an allowed record to an Entity", path, map[string]any{"from_type": fromType, "to_type": toType})
	case "part_of":
		if (fromType == "entity" && toType == "entity") || (fromType == "objective" && toType == "objective") {
			return nil
		}
		return semanticError("semantic_validation", "part_of must stay within the Entity or Objective endpoint family", path, map[string]any{"from_type": fromType, "to_type": toType})
	case "tests":
		if fromType == "attempt" && isOneOf(toType, "objective", "entity", "fact") {
			return nil
		}
		return semanticError("semantic_validation", "tests must point from an Attempt to an approved tested target", path, map[string]any{"from_type": fromType, "to_type": toType})
	case "produced":
		if fromType == "attempt" && isOneOf(toType, "entity", "objective", "fact") {
			return nil
		}
		return semanticError("semantic_validation", "produced must point from an Attempt to a reusable outcome", path, map[string]any{"from_type": fromType, "to_type": toType})
	case "derived_from":
		if (fromType == "objective" && toType == "fact") || (fromType == "fact" && toType == "fact") {
			return nil
		}
		return semanticError("semantic_validation", "derived_from endpoint types are not allowed", path, map[string]any{"from_type": fromType, "to_type": toType})
	case "depends_on":
		if fromType == "objective" && toType == "objective" {
			return nil
		}
		return semanticError("semantic_validation", "depends_on must point from an Objective to a prerequisite Objective", path, map[string]any{"from_type": fromType, "to_type": toType})
	case "satisfies":
		if fromType == "fact" && toType == "objective" {
			return nil
		}
		return semanticError("semantic_validation", "satisfies must point from current knowledge to an Objective", path, map[string]any{"from_type": fromType, "to_type": toType})
	case "supersedes":
		return semanticError("semantic_validation", "supersedes is created only by the supersede operation", path, nil)
	default:
		return semanticError("semantic_validation", "unsupported relationship type in this slice", path, nil)
	}
}

func isOneOf(value string, allowed ...string) bool {
	for _, candidate := range allowed {
		if value == candidate {
			return true
		}
	}
	return false
}

func relationshipWouldCycle(ctx context.Context, tx *sql.Tx, projectID, relation, fromKey, toKey string) (bool, error) {
	if fromKey == toKey {
		return true, nil
	}
	visited := map[string]bool{}
	stack := []string{toKey}
	for len(stack) != 0 {
		key := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		if key == fromKey {
			return true, nil
		}
		if visited[key] {
			continue
		}
		visited[key] = true
		rows, err := tx.QueryContext(ctx, `
			SELECT to_key
			FROM blackboard_v2_relationships
			WHERE project_id = ? AND from_key = ? AND relation = ?
			ORDER BY to_key ASC`,
			projectID, key, relation,
		)
		if err != nil {
			return false, fmt.Errorf("read %s relationships: %w", relation, err)
		}
		for rows.Next() {
			var parent string
			if err := rows.Scan(&parent); err != nil {
				rows.Close()
				return false, fmt.Errorf("scan %s relationships: %w", relation, err)
			}
			stack = append(stack, parent)
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return false, fmt.Errorf("iterate %s relationships: %w", relation, err)
		}
		rows.Close()
	}
	return false, nil
}

func moveCurrentRelationshipsToHistory(ctx context.Context, tx *sql.Tx, projectID, key string) error {
	rows, err := tx.QueryContext(ctx, `
		SELECT from_key, relation, to_key, version, reason, created_at
		FROM blackboard_v2_relationships
		WHERE project_id = ? AND (from_key = ? OR to_key = ?)
		ORDER BY from_key ASC, relation ASC, to_key ASC`,
		projectID, key, key,
	)
	if err != nil {
		return fmt.Errorf("read terminal Entity relationships: %w", err)
	}
	type relationship struct {
		from, relation, to, reason, createdAt string
		version                               int
	}
	relationships := make([]relationship, 0)
	for rows.Next() {
		var item relationship
		if err := rows.Scan(&item.from, &item.relation, &item.to, &item.version, &item.reason, &item.createdAt); err != nil {
			rows.Close()
			return fmt.Errorf("scan terminal Entity relationship: %w", err)
		}
		relationships = append(relationships, item)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return fmt.Errorf("iterate terminal Entity relationships: %w", err)
	}
	rows.Close()
	for _, item := range relationships {
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO blackboard_v2_relationship_history (project_id, from_key, relation, to_key, version, reason, recorded_at)
			VALUES (?, ?, ?, ?, ?, ?, ?)`,
			projectID, item.from, item.relation, item.to, item.version, item.reason, item.createdAt,
		); err != nil {
			return fmt.Errorf("store terminal Entity relationship history: %w", err)
		}
	}
	if _, err := tx.ExecContext(ctx, `
		DELETE FROM blackboard_v2_relationships
		WHERE project_id = ? AND (from_key = ? OR to_key = ?)`,
		projectID, key, key,
	); err != nil {
		return fmt.Errorf("remove terminal Entity relationships: %w", err)
	}
	return nil
}

func ensureProjectState(ctx context.Context, tx *sql.Tx, projectID string) error {
	if err := ensureProjectExists(ctx, tx, projectID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO blackboard_v2_project_state (project_id, revision)
		VALUES (?, 0)
		ON CONFLICT(project_id) DO NOTHING`, projectID,
	); err != nil {
		return fmt.Errorf("ensure Blackboard v2 Project state: %w", err)
	}
	return nil
}

func ensureProjectExists(ctx context.Context, tx *sql.Tx, projectID string) error {
	var exists int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM projects WHERE id = ?`, projectID).Scan(&exists); err != nil {
		return fmt.Errorf("check Project for Blackboard v2 state: %w", err)
	}
	if exists == 0 {
		return semanticError("not_found", "project not found", "project", map[string]any{"project": projectID})
	}
	return nil
}

func currentRevision(ctx context.Context, tx *sql.Tx, projectID string) (int, error) {
	var revision int
	if err := tx.QueryRowContext(ctx, `SELECT revision FROM blackboard_v2_project_state WHERE project_id = ?`, projectID).Scan(&revision); err != nil {
		return 0, fmt.Errorf("read Blackboard v2 revision: %w", err)
	}
	return revision, nil
}

func currentRevisionOrZero(ctx context.Context, tx *sql.Tx, projectID string) (int, error) {
	revision, err := currentRevision(ctx, tx, projectID)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	return revision, err
}

func incrementRevision(ctx context.Context, tx *sql.Tx, projectID string, current int) (int, error) {
	next := current + 1
	if _, err := tx.ExecContext(ctx, `UPDATE blackboard_v2_project_state SET revision = ? WHERE project_id = ?`, next, projectID); err != nil {
		return 0, fmt.Errorf("advance Blackboard v2 revision: %w", err)
	}
	return next, nil
}

func loadCurrentRecord(ctx context.Context, tx *sql.Tx, projectID, key string) (storedRecord, error) {
	var found storedRecord
	var raw string
	err := tx.QueryRowContext(ctx, `
		SELECT key, type, version, record_json
		FROM blackboard_v2_records
		WHERE project_id = ? AND key = ?`,
		projectID, key,
	).Scan(&found.key, &found.typ, &found.version, &raw)
	if err != nil {
		return storedRecord{}, err
	}
	record, err := decodeStoredRecord(found.typ, raw)
	if err != nil {
		return storedRecord{}, fmt.Errorf("decode Blackboard v2 record: %w", err)
	}
	found.record = record
	return found, nil
}

func historicalKeyExists(ctx context.Context, tx *sql.Tx, projectID, key string) (bool, error) {
	var exists int
	err := tx.QueryRowContext(ctx, `
		SELECT EXISTS(
			SELECT 1 FROM blackboard_v2_record_history WHERE project_id = ? AND key = ?
		)`, projectID, key,
	).Scan(&exists)
	if err != nil {
		return false, fmt.Errorf("check Blackboard v2 historical key: %w", err)
	}
	return exists == 1, nil
}

func decodeStoredRecord(typ, raw string) (Record, error) {
	switch typ {
	case "entity":
		var record EntityRecord
		if err := json.Unmarshal([]byte(raw), &record); err != nil {
			return Record{}, err
		}
		return recordFromEntity(record), nil
	case "objective":
		var record ObjectiveRecord
		if err := json.Unmarshal([]byte(raw), &record); err != nil {
			return Record{}, err
		}
		return recordFromObjective(record), nil
	case "attempt":
		var record AttemptRecord
		if err := json.Unmarshal([]byte(raw), &record); err != nil {
			return Record{}, err
		}
		return recordFromAttempt(record), nil
	case "fact":
		var record FactRecord
		if err := json.Unmarshal([]byte(raw), &record); err != nil {
			return Record{}, err
		}
		return recordFromFact(record), nil
	default:
		return Record{}, fmt.Errorf("unsupported Blackboard v2 record type %q", typ)
	}
}

func loadCurrentRelationshipsForKey(ctx context.Context, tx *sql.Tx, projectID, key string) ([]RelationshipTuple, error) {
	rows, err := tx.QueryContext(ctx, `
		SELECT from_key, relation, to_key, reason
		FROM blackboard_v2_relationships
		WHERE project_id = ? AND (from_key = ? OR to_key = ?)
		ORDER BY from_key ASC, relation ASC, to_key ASC, reason ASC`,
		projectID, key, key,
	)
	if err != nil {
		return nil, fmt.Errorf("read Blackboard v2 record relationships: %w", err)
	}
	defer rows.Close()
	return scanRelationshipTuples(rows)
}

func loadAllCurrentRelationships(ctx context.Context, tx *sql.Tx, projectID string) ([]RelationshipTuple, error) {
	rows, err := tx.QueryContext(ctx, `
		SELECT from_key, relation, to_key, reason
		FROM blackboard_v2_relationships
		WHERE project_id = ?
		ORDER BY from_key ASC, relation ASC, to_key ASC, reason ASC`,
		projectID,
	)
	if err != nil {
		return nil, fmt.Errorf("read Blackboard v2 snapshot relationships: %w", err)
	}
	defer rows.Close()
	return scanRelationshipTuples(rows)
}

type relationshipRows interface {
	Next() bool
	Scan(dest ...any) error
	Err() error
}

func scanRelationshipTuples(rows relationshipRows) ([]RelationshipTuple, error) {
	relationships := []RelationshipTuple{}
	for rows.Next() {
		var from, relation, to, reason string
		if err := rows.Scan(&from, &relation, &to, &reason); err != nil {
			return nil, fmt.Errorf("scan Blackboard v2 relationship: %w", err)
		}
		if reason == "" {
			relationships = append(relationships, RelationshipTuple{from, relation, to})
		} else {
			relationships = append(relationships, RelationshipTuple{from, relation, to, reason})
		}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate Blackboard v2 relationships: %w", err)
	}
	return relationships, nil
}

func continuationProjectStatus(ctx context.Context, tx *sql.Tx, projectID, continuationID string) (string, error) {
	var status string
	err := tx.QueryRowContext(ctx, `
		SELECT continuation.status
		FROM task_continuations AS continuation
		JOIN tasks AS task ON task.id = continuation.task_id
		WHERE continuation.id = ? AND task.project_id = ?`,
		continuationID, projectID,
	).Scan(&status)
	if errors.Is(err, sql.ErrNoRows) {
		return "", semanticError("authority_denied", "trusted Continuation does not own this Project interface", "", nil)
	}
	if err != nil {
		return "", fmt.Errorf("validate trusted Continuation Project: %w", err)
	}
	return status, nil
}

func continuationCanWrite(status string) bool {
	return isOneOf(status, "pending", "running", "paused")
}

func isTerminalContinuationStatus(status string) bool {
	return isOneOf(status, "completed", "failed", "stopped", "interrupted")
}

func isUnexpectedTerminalContinuationStatus(status string) bool {
	return isOneOf(status, "failed", "stopped", "interrupted")
}

func validateContinuationChangeOwnership(ctx context.Context, tx *sql.Tx, projectID, continuationID string, change Change, index int) error {
	keys := make([]string, 0, 2)
	switch change.Op {
	case "update", "transition":
		keys = append(keys, change.Key)
	case "relate":
		keys = append(keys, change.From)
	case "supersede":
		keys = append(keys, change.Replacement, change.Replaced)
	}
	for _, key := range keys {
		record, err := loadCurrentRecord(ctx, tx, projectID, key)
		if errors.Is(err, sql.ErrNoRows) {
			continue
		}
		if err != nil {
			return err
		}
		if record.typ == "attempt" {
			if err := requireAttemptOwner(ctx, tx, projectID, key, continuationID, fmt.Sprintf("changes[%d]", index)); err != nil {
				return err
			}
		}
	}
	return nil
}

func bindAttemptOrigin(ctx context.Context, tx *sql.Tx, projectID, key, continuationID, now string) error {
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO blackboard_v2_attempt_origins (project_id, key, continuation_id, created_at)
		VALUES (?, ?, ?, ?)`,
		projectID, key, continuationID, now,
	); err != nil {
		return fmt.Errorf("bind Attempt trusted origin: %w", err)
	}
	return nil
}

func requireAttemptOwner(ctx context.Context, tx *sql.Tx, projectID, key, continuationID, path string) error {
	var owner string
	err := tx.QueryRowContext(ctx, `
		SELECT continuation_id
		FROM blackboard_v2_attempt_origins
		WHERE project_id = ? AND key = ?`,
		projectID, key,
	).Scan(&owner)
	if errors.Is(err, sql.ErrNoRows) || (err == nil && owner != continuationID) {
		return semanticError("authority_denied", "Attempt is owned by another trusted origin", path, map[string]any{"key": key})
	}
	if err != nil {
		return fmt.Errorf("read Attempt trusted origin: %w", err)
	}
	return nil
}

func idempotencyReplay(ctx context.Context, tx *sql.Tx, projectID, continuationID, key, requestHash string) (ChangeResult, bool, error) {
	var storedHash, raw, storedContinuationID string
	err := tx.QueryRowContext(ctx, `
		SELECT request_hash, result_json, continuation_id
		FROM blackboard_v2_idempotency_receipts
		WHERE project_id = ? AND idempotency_key = ?`,
		projectID, key,
	).Scan(&storedHash, &raw, &storedContinuationID)
	if errors.Is(err, sql.ErrNoRows) {
		return ChangeResult{}, false, nil
	}
	if err != nil {
		return ChangeResult{}, false, fmt.Errorf("read idempotency receipt: %w", err)
	}
	if storedContinuationID != continuationID {
		return ChangeResult{}, false, semanticError("authority_denied", "idempotency receipt belongs to another trusted origin", "idempotency_key", nil)
	}
	if storedHash != requestHash {
		return ChangeResult{}, false, semanticError("idempotency_conflict", "idempotency key was already used with different semantics", "idempotency_key", map[string]any{"idempotency_key": key})
	}
	var result ChangeResult
	if err := json.Unmarshal([]byte(raw), &result); err != nil {
		return ChangeResult{}, false, fmt.Errorf("decode idempotency receipt: %w", err)
	}
	return result, true, nil
}

func makeChangeResult(revision int, changedRecords map[string]int, changedRelations map[string]RelationVersionTuple) ChangeResult {
	keys := make([]string, 0, len(changedRecords))
	for key := range changedRecords {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	records := make([]RecordVersionTuple, 0, len(keys))
	for _, key := range keys {
		records = append(records, RecordVersionTuple{key, changedRecords[key]})
	}
	relationKeys := make([]string, 0, len(changedRelations))
	for key := range changedRelations {
		relationKeys = append(relationKeys, key)
	}
	sort.Strings(relationKeys)
	relations := make([]RelationVersionTuple, 0, len(relationKeys))
	for _, key := range relationKeys {
		relations = append(relations, changedRelations[key])
	}
	return ChangeResult{
		Schema:    changeResultSchema,
		Revision:  revision,
		Records:   records,
		Relations: relations,
		WorkingSnapshot: WorkingSnapshot{
			Path:     workingPath,
			Revision: revision,
		},
	}
}

func relationKey(tuple RelationVersionTuple) string {
	return fmt.Sprintf("%s\x00%s\x00%s", tuple[0], tuple[1], tuple[2])
}

func canonicalRequestHash(batch ChangeBatch) (string, error) {
	raw, err := json.Marshal(batch)
	if err != nil {
		return "", fmt.Errorf("encode idempotency request: %w", err)
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:]), nil
}

func rejectUnknownFields(fields map[string]json.RawMessage, allowed map[string]bool) error {
	for field := range fields {
		if !allowed[field] {
			return fmt.Errorf("unknown change field %q", field)
		}
	}
	return nil
}

func decodeCreateChange(fields map[string]json.RawMessage) (Change, error) {
	key, err := decodeRequiredString(fields, "key")
	if err != nil {
		return Change{}, err
	}
	typ, err := decodeRequiredString(fields, "type")
	if err != nil {
		return Change{}, err
	}
	recordRaw, ok := fields["record"]
	if !ok {
		return Change{}, fmt.Errorf("create record is required")
	}
	record, err := decodeCompleteRecord(typ, recordRaw)
	if err != nil {
		return Change{}, err
	}
	return Change{Op: "create", Key: key, Type: typ, Record: record}, nil
}

func decodeUpdateChange(fields map[string]json.RawMessage) (Change, error) {
	key, err := decodeRequiredString(fields, "key")
	if err != nil {
		return Change{}, err
	}
	typ, err := decodeRequiredString(fields, "type")
	if err != nil {
		return Change{}, err
	}
	var version int
	if raw, ok := fields["version"]; !ok {
		return Change{}, fmt.Errorf("update version is required")
	} else if err := json.Unmarshal(raw, &version); err != nil {
		return Change{}, fmt.Errorf("decode update version: %w", err)
	}
	recordRaw, ok := fields["record"]
	if !ok {
		return Change{}, fmt.Errorf("update record is required")
	}
	record, err := decodePartialRecord(typ, recordRaw)
	if err != nil {
		return Change{}, err
	}
	var clear []string
	if raw, ok := fields["clear"]; ok {
		if err := json.Unmarshal(raw, &clear); err != nil {
			return Change{}, fmt.Errorf("decode update clear: %w", err)
		}
	}
	return Change{Op: "update", Key: key, Version: version, Type: typ, Record: record, Clear: clear}, nil
}

func decodeRelateChange(fields map[string]json.RawMessage) (Change, error) {
	from, err := decodeRequiredString(fields, "from")
	if err != nil {
		return Change{}, err
	}
	relation, err := decodeRequiredString(fields, "relation")
	if err != nil {
		return Change{}, err
	}
	to, err := decodeRequiredString(fields, "to")
	if err != nil {
		return Change{}, err
	}
	if !isOrdinaryRelation(relation) && !isReasonRelation(relation) {
		return Change{}, fmt.Errorf("unsupported relationship type %q", relation)
	}
	if isOrdinaryRelation(relation) {
		if _, ok := fields["version"]; ok {
			return Change{}, fmt.Errorf("relationship version is forbidden for %s", relation)
		}
		if _, ok := fields["reason"]; ok {
			return Change{}, fmt.Errorf("relationship reason is forbidden for %s", relation)
		}
		return Change{Op: "relate", From: from, Relation: relation, To: to}, nil
	}
	_, hasVersion := fields["version"]
	_, hasReason := fields["reason"]
	if hasVersion && !hasReason {
		return Change{}, fmt.Errorf("relationship reason is required when version is provided")
	}
	var version int
	if raw, ok := fields["version"]; ok {
		if err := json.Unmarshal(raw, &version); err != nil {
			return Change{}, fmt.Errorf("decode relation version: %w", err)
		}
	}
	reason := ""
	if hasReason {
		reason, err = decodeRequiredString(fields, "reason")
		if err != nil {
			return Change{}, err
		}
		if reason == "" {
			return Change{}, fmt.Errorf("relationship reason must not be empty")
		}
	}
	return Change{Op: "relate", From: from, Relation: relation, To: to, Version: version, Reason: reason}, nil
}

func decodeTransitionChange(fields map[string]json.RawMessage) (Change, error) {
	key, err := decodeRequiredString(fields, "key")
	if err != nil {
		return Change{}, err
	}
	var version int
	if raw, ok := fields["version"]; !ok {
		return Change{}, fmt.Errorf("transition version is required")
	} else if err := json.Unmarshal(raw, &version); err != nil {
		return Change{}, fmt.Errorf("decode transition version: %w", err)
	}
	status, err := decodeRequiredString(fields, "status")
	if err != nil {
		return Change{}, err
	}
	change := Change{Op: "transition", Key: key, Version: version, Status: status}
	switch status {
	case "resolved", "abandoned", "retired", "false_positive":
		if _, ok := fields["summary"]; ok {
			return Change{}, fmt.Errorf("transition summary is not allowed for status %s", status)
		}
		resolutionSummary, err := decodeRequiredString(fields, "resolution_summary")
		if err != nil {
			return Change{}, err
		}
		change.ResolutionSummary = resolutionSummary
	case "succeeded", "failed", "blocked", "inconclusive", "interrupted", "deprecated", "missing":
		if _, ok := fields["resolution_summary"]; ok {
			return Change{}, fmt.Errorf("transition resolution_summary is not allowed for status %s", status)
		}
		summary, err := decodeRequiredString(fields, "summary")
		if err != nil {
			return Change{}, err
		}
		change.Summary = summary
	default:
		if _, ok := fields["summary"]; ok {
			return Change{}, fmt.Errorf("transition summary is not allowed for status %s", status)
		}
		if _, ok := fields["resolution_summary"]; ok {
			return Change{}, fmt.Errorf("transition resolution_summary is not allowed for status %s", status)
		}
	}
	return change, nil
}

func decodeSupersedeChange(fields map[string]json.RawMessage) (Change, error) {
	replacement, err := decodeRequiredString(fields, "replacement")
	if err != nil {
		return Change{}, err
	}
	replaced, err := decodeRequiredString(fields, "replaced")
	if err != nil {
		return Change{}, err
	}
	var replacementVersion int
	if raw, ok := fields["replacement_version"]; ok {
		if err := json.Unmarshal(raw, &replacementVersion); err != nil {
			return Change{}, fmt.Errorf("decode supersede replacement_version: %w", err)
		}
	}
	var replacedVersion int
	if raw, ok := fields["replaced_version"]; !ok {
		return Change{}, fmt.Errorf("supersede replaced_version is required")
	} else if err := json.Unmarshal(raw, &replacedVersion); err != nil {
		return Change{}, fmt.Errorf("decode supersede replaced_version: %w", err)
	}
	return Change{Op: "supersede", Replacement: replacement, ReplacementVersion: replacementVersion, Replaced: replaced, ReplacedVersion: replacedVersion}, nil
}

func decodeRequiredString(fields map[string]json.RawMessage, field string) (string, error) {
	raw, ok := fields[field]
	if !ok {
		return "", fmt.Errorf("%s is required", field)
	}
	var value string
	if err := json.Unmarshal(raw, &value); err != nil {
		return "", fmt.Errorf("decode %s: %w", field, err)
	}
	return value, nil
}

func decodeCompleteRecord(typ string, raw json.RawMessage) (any, error) {
	switch typ {
	case "entity":
		return decodeEntityRecord(raw)
	case "objective":
		return decodeObjectiveRecord(raw)
	case "attempt":
		return decodeAttemptRecord(raw)
	case "fact":
		return decodeFactRecord(raw)
	default:
		return nil, fmt.Errorf("unsupported Blackboard v2 record type %q in this slice", typ)
	}
}

func decodePartialRecord(typ string, raw json.RawMessage) (any, error) {
	switch typ {
	case "entity":
		return decodeEntityPatch(raw)
	case "objective":
		return decodeObjectivePatch(raw)
	case "attempt":
		return decodeAttemptPatch(raw)
	case "fact":
		return decodeFactPatch(raw)
	default:
		return nil, fmt.Errorf("unsupported Blackboard v2 record type %q in this slice", typ)
	}
}

func decodeEntityRecord(raw json.RawMessage) (EntityRecord, error) {
	var record EntityRecord
	if err := strictDecodeJSON(raw, &record); err != nil {
		return EntityRecord{}, fmt.Errorf("decode Entity record: %w", err)
	}
	return record, nil
}

func decodeEntityPatch(raw json.RawMessage) (EntityPatch, error) {
	var patch EntityPatch
	if err := strictDecodeJSON(raw, &patch); err != nil {
		return EntityPatch{}, fmt.Errorf("decode Entity patch: %w", err)
	}
	if entityPatchEmpty(patch) {
		return EntityPatch{}, fmt.Errorf("Entity partial record requires at least one property")
	}
	return patch, nil
}

func decodeObjectiveRecord(raw json.RawMessage) (ObjectiveRecord, error) {
	var record ObjectiveRecord
	if err := strictDecodeJSON(raw, &record); err != nil {
		return ObjectiveRecord{}, fmt.Errorf("decode Objective record: %w", err)
	}
	if jsonFieldPresent(raw, "resolution_summary") {
		return ObjectiveRecord{}, fmt.Errorf("Objective create does not accept resolution_summary")
	}
	return record, nil
}

func decodeObjectivePatch(raw json.RawMessage) (ObjectivePatch, error) {
	var patch ObjectivePatch
	if err := strictDecodeJSON(raw, &patch); err != nil {
		return ObjectivePatch{}, fmt.Errorf("decode Objective patch: %w", err)
	}
	if patch.Objective == nil {
		return ObjectivePatch{}, fmt.Errorf("Objective partial record requires at least one property")
	}
	return patch, nil
}

func decodeAttemptRecord(raw json.RawMessage) (AttemptRecord, error) {
	var record AttemptRecord
	if err := strictDecodeJSON(raw, &record); err != nil {
		return AttemptRecord{}, fmt.Errorf("decode Attempt record: %w", err)
	}
	return record, nil
}

func decodeAttemptPatch(raw json.RawMessage) (AttemptPatch, error) {
	var patch AttemptPatch
	if err := strictDecodeJSON(raw, &patch); err != nil {
		return AttemptPatch{}, fmt.Errorf("decode Attempt patch: %w", err)
	}
	if patch.Summary == nil {
		return AttemptPatch{}, fmt.Errorf("Attempt partial record requires at least one property")
	}
	return patch, nil
}

func jsonFieldPresent(raw json.RawMessage, name string) bool {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return false
	}
	_, ok := fields[name]
	return ok
}

func decodeFactRecord(raw json.RawMessage) (FactRecord, error) {
	var record FactRecord
	if err := strictDecodeJSON(raw, &record); err != nil {
		return FactRecord{}, fmt.Errorf("decode Fact record: %w", err)
	}
	return record, nil
}

func decodeFactPatch(raw json.RawMessage) (FactPatch, error) {
	var patch FactPatch
	if err := strictDecodeJSON(raw, &patch); err != nil {
		return FactPatch{}, fmt.Errorf("decode Fact patch: %w", err)
	}
	if factPatchEmpty(patch) {
		return FactPatch{}, fmt.Errorf("Fact partial record requires at least one property")
	}
	return patch, nil
}

func strictDecodeJSON(raw []byte, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return fmt.Errorf("unexpected trailing JSON")
	}
	return nil
}

func validateChangeShape(change Change, index int) error {
	allowedFields := map[string]bool{}
	switch change.Op {
	case "create":
		allowedFields = map[string]bool{"key": true, "type": true, "record": true}
	case "update":
		allowedFields = map[string]bool{"key": true, "version": true, "type": true, "record": true, "clear": true}
	case "relate":
		allowedFields = map[string]bool{"version": true, "from": true, "relation": true, "to": true, "reason": true}
	case "transition":
		allowedFields = map[string]bool{"key": true, "version": true, "status": true, "summary": true, "resolution_summary": true}
	case "supersede":
		allowedFields = map[string]bool{"replacement": true, "replacement_version": true, "replaced": true, "replaced_version": true}
	}
	if len(allowedFields) != 0 {
		for _, field := range populatedChangeFields(change) {
			if field.populated && !allowedFields[field.name] {
				return semanticError("semantic_validation", fmt.Sprintf("%s does not accept %s", change.Op, field.name), fmt.Sprintf("changes[%d].%s", index, field.name), nil)
			}
		}
	}
	if change.Op == "relate" {
		path := fmt.Sprintf("changes[%d]", index)
		if isOrdinaryRelation(change.Relation) {
			if change.Version != 0 {
				return semanticError("semantic_validation", "ordinary relationship does not accept version", path+".version", nil)
			}
			if change.Reason != "" {
				return semanticError("semantic_validation", "ordinary relationship does not accept reason", path+".reason", nil)
			}
		} else if isReasonRelation(change.Relation) && change.Version != 0 && change.Reason == "" {
			return semanticError("semantic_validation", "relationship reason is required when version is provided", path+".reason", nil)
		}
	}

	switch change.Op {
	case "create":
		if change.Version != 0 {
			return semanticError("semantic_validation", "create does not accept version", fmt.Sprintf("changes[%d].version", index), nil)
		}
		if len(change.Clear) != 0 {
			return semanticError("semantic_validation", "create does not accept clear", fmt.Sprintf("changes[%d].clear", index), nil)
		}
		if change.Record == nil {
			return semanticError("semantic_validation", "create record is required", fmt.Sprintf("changes[%d].record", index), nil)
		}
	case "update":
		if change.Version < 1 {
			return semanticError("semantic_validation", "update requires the current version", fmt.Sprintf("changes[%d].version", index), nil)
		}
		if change.Record == nil {
			return semanticError("semantic_validation", "update record is required", fmt.Sprintf("changes[%d].record", index), nil)
		}
	case "relate":
		if change.From == "" {
			return semanticError("semantic_validation", "relate from is required", fmt.Sprintf("changes[%d].from", index), nil)
		}
		if change.Relation == "" {
			return semanticError("semantic_validation", "relate relation is required", fmt.Sprintf("changes[%d].relation", index), nil)
		}
		if change.To == "" {
			return semanticError("semantic_validation", "relate to is required", fmt.Sprintf("changes[%d].to", index), nil)
		}
	case "transition":
		if change.Key == "" {
			return semanticError("semantic_validation", "transition key is required", fmt.Sprintf("changes[%d].key", index), nil)
		}
		if change.Version < 1 {
			return semanticError("semantic_validation", "transition requires the current version", fmt.Sprintf("changes[%d].version", index), nil)
		}
		if change.Status == "" {
			return semanticError("semantic_validation", "transition status is required", fmt.Sprintf("changes[%d].status", index), nil)
		}
		switch change.Status {
		case "resolved", "abandoned", "retired", "false_positive":
			if change.Summary != "" {
				return semanticError("semantic_validation", "transition summary is not allowed for this status", fmt.Sprintf("changes[%d].summary", index), nil)
			}
		case "succeeded", "failed", "blocked", "inconclusive", "interrupted", "deprecated", "missing":
			if change.ResolutionSummary != "" {
				return semanticError("semantic_validation", "transition resolution_summary is not allowed for this status", fmt.Sprintf("changes[%d].resolution_summary", index), nil)
			}
		default:
			if change.Summary != "" || change.ResolutionSummary != "" {
				return semanticError("semantic_validation", "transition summary field is not allowed for this status", fmt.Sprintf("changes[%d].status", index), nil)
			}
		}
	case "supersede":
		if change.Replacement == "" {
			return semanticError("semantic_validation", "supersede replacement is required", fmt.Sprintf("changes[%d].replacement", index), nil)
		}
		if change.ReplacementVersion < 0 {
			return semanticError("semantic_validation", "supersede replacement_version must be positive when provided", fmt.Sprintf("changes[%d].replacement_version", index), nil)
		}
		if change.Replaced == "" {
			return semanticError("semantic_validation", "supersede replaced is required", fmt.Sprintf("changes[%d].replaced", index), nil)
		}
		if change.ReplacedVersion < 1 {
			return semanticError("semantic_validation", "supersede requires the current replaced version", fmt.Sprintf("changes[%d].replaced_version", index), nil)
		}
	}
	return nil
}

type populatedChangeField struct {
	name      string
	populated bool
}

func populatedChangeFields(change Change) []populatedChangeField {
	return []populatedChangeField{
		{name: "key", populated: change.Key != ""},
		{name: "version", populated: change.Version != 0},
		{name: "type", populated: change.Type != ""},
		{name: "record", populated: change.Record != nil},
		{name: "clear", populated: len(change.Clear) != 0},
		{name: "from", populated: change.From != ""},
		{name: "relation", populated: change.Relation != ""},
		{name: "to", populated: change.To != ""},
		{name: "reason", populated: change.Reason != ""},
		{name: "status", populated: change.Status != ""},
		{name: "summary", populated: change.Summary != ""},
		{name: "resolution_summary", populated: change.ResolutionSummary != ""},
		{name: "replacement", populated: change.Replacement != ""},
		{name: "replacement_version", populated: change.ReplacementVersion != 0},
		{name: "replaced", populated: change.Replaced != ""},
		{name: "replaced_version", populated: change.ReplacedVersion != 0},
	}
}

func isOrdinaryRelation(relation string) bool {
	return isOneOf(relation, "about", "part_of", "tests", "produced", "evidences", "derived_from", "satisfies")
}

func isReasonRelation(relation string) bool {
	return isOneOf(relation, "supports", "contradicts", "depends_on")
}

func completeEntityRecord(value any, path string) (EntityRecord, error) {
	switch record := value.(type) {
	case EntityRecord:
		return record, nil
	case *EntityRecord:
		if record == nil {
			return EntityRecord{}, semanticError("semantic_validation", "Entity record is required", path, nil)
		}
		return *record, nil
	case json.RawMessage:
		decoded, err := decodeEntityRecord(record)
		if err != nil {
			return EntityRecord{}, semanticError("semantic_validation", err.Error(), path, nil)
		}
		return decoded, nil
	default:
		return EntityRecord{}, semanticError("semantic_validation", "Entity create requires a complete Entity record", path, nil)
	}
}

func partialEntityRecord(value any, path string) (EntityPatch, error) {
	switch patch := value.(type) {
	case EntityPatch:
		if entityPatchEmpty(patch) {
			return EntityPatch{}, semanticError("semantic_validation", "Entity partial record requires at least one property", path, nil)
		}
		return patch, nil
	case *EntityPatch:
		if patch == nil {
			return EntityPatch{}, semanticError("semantic_validation", "Entity update requires an Entity partial record", path, nil)
		}
		if entityPatchEmpty(*patch) {
			return EntityPatch{}, semanticError("semantic_validation", "Entity partial record requires at least one property", path, nil)
		}
		return *patch, nil
	case json.RawMessage:
		decoded, err := decodeEntityPatch(patch)
		if err != nil {
			return EntityPatch{}, semanticError("semantic_validation", err.Error(), path, nil)
		}
		return decoded, nil
	default:
		return EntityPatch{}, semanticError("semantic_validation", "Entity update requires an Entity partial record", path, nil)
	}
}

func completeObjectiveRecord(value any, path string) (ObjectiveRecord, error) {
	switch record := value.(type) {
	case ObjectiveRecord:
		return record, nil
	case *ObjectiveRecord:
		if record == nil {
			return ObjectiveRecord{}, semanticError("semantic_validation", "Objective record is required", path, nil)
		}
		return *record, nil
	case json.RawMessage:
		decoded, err := decodeObjectiveRecord(record)
		if err != nil {
			return ObjectiveRecord{}, semanticError("semantic_validation", err.Error(), path, nil)
		}
		return decoded, nil
	default:
		return ObjectiveRecord{}, semanticError("semantic_validation", "Objective create requires a complete Objective record", path, nil)
	}
}

func partialObjectiveRecord(value any, path string) (ObjectivePatch, error) {
	switch patch := value.(type) {
	case ObjectivePatch:
		if patch.Objective == nil {
			return ObjectivePatch{}, semanticError("semantic_validation", "Objective partial record requires at least one property", path, nil)
		}
		return patch, nil
	case *ObjectivePatch:
		if patch == nil {
			return ObjectivePatch{}, semanticError("semantic_validation", "Objective update requires an Objective partial record", path, nil)
		}
		if patch.Objective == nil {
			return ObjectivePatch{}, semanticError("semantic_validation", "Objective partial record requires at least one property", path, nil)
		}
		return *patch, nil
	case json.RawMessage:
		decoded, err := decodeObjectivePatch(patch)
		if err != nil {
			return ObjectivePatch{}, semanticError("semantic_validation", err.Error(), path, nil)
		}
		return decoded, nil
	default:
		return ObjectivePatch{}, semanticError("semantic_validation", "Objective update requires an Objective partial record", path, nil)
	}
}

func completeAttemptRecord(value any, path string) (AttemptRecord, error) {
	switch record := value.(type) {
	case AttemptRecord:
		return record, nil
	case *AttemptRecord:
		if record == nil {
			return AttemptRecord{}, semanticError("semantic_validation", "Attempt record is required", path, nil)
		}
		return *record, nil
	case json.RawMessage:
		decoded, err := decodeAttemptRecord(record)
		if err != nil {
			return AttemptRecord{}, semanticError("semantic_validation", err.Error(), path, nil)
		}
		return decoded, nil
	default:
		return AttemptRecord{}, semanticError("semantic_validation", "Attempt create requires a complete Attempt record", path, nil)
	}
}

func partialAttemptRecord(value any, path string) (AttemptPatch, error) {
	switch patch := value.(type) {
	case AttemptPatch:
		if patch.Summary == nil {
			return AttemptPatch{}, semanticError("semantic_validation", "Attempt partial record requires at least one property", path, nil)
		}
		return patch, nil
	case *AttemptPatch:
		if patch == nil {
			return AttemptPatch{}, semanticError("semantic_validation", "Attempt update requires an Attempt partial record", path, nil)
		}
		if patch.Summary == nil {
			return AttemptPatch{}, semanticError("semantic_validation", "Attempt partial record requires at least one property", path, nil)
		}
		return *patch, nil
	case json.RawMessage:
		decoded, err := decodeAttemptPatch(patch)
		if err != nil {
			return AttemptPatch{}, semanticError("semantic_validation", err.Error(), path, nil)
		}
		return decoded, nil
	default:
		return AttemptPatch{}, semanticError("semantic_validation", "Attempt update requires an Attempt partial record", path, nil)
	}
}

func completeFactRecord(value any, path string) (FactRecord, error) {
	switch record := value.(type) {
	case FactRecord:
		return record, nil
	case *FactRecord:
		if record == nil {
			return FactRecord{}, semanticError("semantic_validation", "Fact record is required", path, nil)
		}
		return *record, nil
	case json.RawMessage:
		decoded, err := decodeFactRecord(record)
		if err != nil {
			return FactRecord{}, semanticError("semantic_validation", err.Error(), path, nil)
		}
		return decoded, nil
	default:
		return FactRecord{}, semanticError("semantic_validation", "Fact create requires a complete Fact record", path, nil)
	}
}

func partialFactRecord(value any, path string) (FactPatch, error) {
	switch patch := value.(type) {
	case FactPatch:
		if patch.Confidence != nil {
			return FactPatch{}, semanticError("semantic_validation", "Fact confidence changes require transition", path+".confidence", nil)
		}
		if factPatchEmpty(patch) {
			return FactPatch{}, semanticError("semantic_validation", "Fact partial record requires at least one property", path, nil)
		}
		return patch, nil
	case *FactPatch:
		if patch == nil {
			return FactPatch{}, semanticError("semantic_validation", "Fact update requires a Fact partial record", path, nil)
		}
		if patch.Confidence != nil {
			return FactPatch{}, semanticError("semantic_validation", "Fact confidence changes require transition", path+".confidence", nil)
		}
		if factPatchEmpty(*patch) {
			return FactPatch{}, semanticError("semantic_validation", "Fact partial record requires at least one property", path, nil)
		}
		return *patch, nil
	case json.RawMessage:
		decoded, err := decodeFactPatch(patch)
		if err != nil {
			return FactPatch{}, semanticError("semantic_validation", err.Error(), path, nil)
		}
		return decoded, nil
	default:
		return FactPatch{}, semanticError("semantic_validation", "Fact update requires a Fact partial record", path, nil)
	}
}

func stringPtr(value string) *string {
	return &value
}

func entityPatchEmpty(patch EntityPatch) bool {
	return patch.Kind == nil && patch.Name == nil && patch.Locator == nil && patch.Description == nil && patch.ScopeStatus == nil && patch.CredentialRef == nil
}

func factPatchEmpty(patch FactPatch) bool {
	return patch.Category == nil && patch.Summary == nil && patch.Body == nil && patch.ScopeStatus == nil
}

func applyEntityPatch(existing EntityRecord, patch EntityPatch, clear []string, path string) (EntityRecord, error) {
	next := existing
	if patch.Kind != nil {
		next.Kind = *patch.Kind
	}
	if patch.Name != nil {
		next.Name = *patch.Name
	}
	if patch.Locator != nil {
		next.Locator = *patch.Locator
	}
	if patch.Description != nil {
		next.Description = *patch.Description
	}
	if patch.ScopeStatus != nil {
		next.ScopeStatus = *patch.ScopeStatus
	}
	if patch.CredentialRef != nil {
		next.CredentialRef = *patch.CredentialRef
	}
	for _, field := range clear {
		switch field {
		case "locator":
			next.Locator = ""
		case "description":
			next.Description = ""
		case "credential_ref":
			next.CredentialRef = ""
		default:
			return EntityRecord{}, semanticError("semantic_validation", "unsupported Entity clear field", path, map[string]any{"field": field})
		}
	}
	return next, nil
}

func applyFactPatch(existing FactRecord, patch FactPatch, clear []string, path string) (FactRecord, error) {
	next := existing
	if patch.Category != nil {
		next.Category = *patch.Category
	}
	if patch.Summary != nil {
		next.Summary = *patch.Summary
	}
	if patch.Body != nil {
		next.Body = *patch.Body
	}
	if patch.ScopeStatus != nil {
		next.ScopeStatus = *patch.ScopeStatus
	}
	for _, field := range clear {
		switch field {
		case "body":
			next.Body = ""
		default:
			return FactRecord{}, semanticError("semantic_validation", "unsupported Fact clear field", path, map[string]any{"field": field})
		}
	}
	return next, nil
}

func validateEntityRecord(record EntityRecord, path string) error {
	if record.Status != "active" {
		return semanticError("semantic_validation", "Entity status must be active", path+".status", nil)
	}
	if !isAcceptedEntityKind(record.Kind) {
		return semanticError("semantic_validation", "Entity kind is not accepted", path+".kind", nil)
	}
	if err := validateConciseText(record.Name, path+".name"); err != nil {
		return err
	}
	if containsSecretMarker(record.Name) {
		return semanticError("semantic_validation", "Entity name must not contain secrets", path+".name", nil)
	}
	if record.Locator != "" {
		if err := validateLocator(record.Locator, path+".locator"); err != nil {
			return err
		}
	}
	if record.Description != "" {
		if err := validateConciseText(record.Description, path+".description"); err != nil {
			return err
		}
		if containsSecretMarker(record.Description) {
			return semanticError("semantic_validation", "Entity description must not contain secrets", path+".description", nil)
		}
	}
	switch record.ScopeStatus {
	case "in_scope", "unknown", "out_of_scope":
	default:
		return semanticError("semantic_validation", "Entity scope_status must be in_scope, unknown, or out_of_scope", path+".scope_status", nil)
	}
	if record.CredentialRef != "" {
		if err := validateCredentialRef(record.CredentialRef, path+".credential_ref"); err != nil {
			return err
		}
	}
	return nil
}

func isAcceptedEntityKind(kind string) bool {
	switch kind {
	case "host", "service", "endpoint", "identity", "file", "function":
		return true
	default:
		return false
	}
}

func validateLocator(locator, path string) error {
	if err := validateConciseText(locator, path); err != nil {
		return err
	}
	if strings.ContainsAny(locator, " \t\r\n") {
		return semanticError("semantic_validation", "Entity locator must not contain whitespace", path, nil)
	}
	if containsSecretMarker(locator) {
		return semanticError("semantic_validation", "Entity locator must not contain secrets", path, nil)
	}
	parsed, err := url.Parse(locator)
	if err != nil {
		return semanticError("semantic_validation", "Entity locator must be a valid locator", path, nil)
	}
	if parsed.User != nil {
		return semanticError("semantic_validation", "Entity locator must not contain credentials", path, nil)
	}
	if strings.Contains(locator, "://") && (parsed.Scheme == "" || parsed.Host == "") {
		return semanticError("semantic_validation", "Entity locator URL must include scheme and host", path, nil)
	}
	if (parsed.Scheme == "http" || parsed.Scheme == "https") && parsed.Host == "" {
		return semanticError("semantic_validation", "Entity HTTP locator must include a host", path, nil)
	}
	return nil
}

func validateCredentialRef(value, path string) error {
	if err := validateConciseText(value, path); err != nil {
		return err
	}
	if containsSecretMarker(value) {
		return semanticError("semantic_validation", "Entity credential_ref must be a non-secret reference", path, nil)
	}
	return nil
}

func containsSecretMarker(value string) bool {
	lower := strings.ToLower(value)
	for _, marker := range []string{"password=", "passwd=", "token=", "secret=", "api_key=", "apikey=", "sk-"} {
		if strings.Contains(lower, marker) {
			return true
		}
	}
	return false
}

func validateObjectiveRecord(record ObjectiveRecord, path string) error {
	if record.Status != "open" {
		return semanticError("semantic_validation", "Objective status must be open", path+".status", nil)
	}
	if record.ResolutionSummary != "" {
		return semanticError("semantic_validation", "open Objective must not include resolution_summary", path+".resolution_summary", nil)
	}
	return validateSemanticText(record.Objective, path+".objective")
}

func validateAttemptRecord(record AttemptRecord, path string) error {
	if record.Status != "open" {
		return semanticError("semantic_validation", "Attempt status must be open", path+".status", nil)
	}
	return validateSemanticText(record.Summary, path+".summary")
}

func validateFactRecord(record FactRecord, path string) error {
	if err := validateConciseText(record.Category, path+".category"); err != nil {
		return err
	}
	if err := validateSemanticText(record.Summary, path+".summary"); err != nil {
		return err
	}
	if record.Body != "" && !utf8.ValidString(record.Body) {
		return semanticError("semantic_validation", "Fact body must be valid UTF-8", path+".body", nil)
	}
	if record.Confidence != "tentative" && record.Confidence != "confirmed" {
		return semanticError("semantic_validation", "Fact confidence must be tentative or confirmed", path+".confidence", nil)
	}
	switch record.ScopeStatus {
	case "in_scope", "unknown", "out_of_scope":
		return nil
	default:
		return semanticError("semantic_validation", "Fact scope_status must be in_scope, unknown, or out_of_scope", path+".scope_status", nil)
	}
}

func validateConciseText(value, path string) error {
	if strings.TrimSpace(value) == "" || !utf8.ValidString(value) || len([]byte(value)) > 512 {
		return semanticError("semantic_validation", "concise semantic text is required and must fit the v2 limit", path, nil)
	}
	return nil
}

func validateSemanticText(value, path string) error {
	if strings.TrimSpace(value) == "" || !utf8.ValidString(value) || len([]byte(value)) > 1024 {
		return semanticError("semantic_validation", "primary semantic text is required and must fit the v2 limit", path, nil)
	}
	return nil
}

func validateKey(key, path string) error {
	if key == "" || len(key) > 96 {
		return semanticError("semantic_validation", "Blackboard Key must be non-empty and at most 96 ASCII characters", path, nil)
	}
	for _, r := range key {
		if r < 0x20 || r > 0x7e {
			return semanticError("semantic_validation", "Blackboard Key must be readable ASCII", path, nil)
		}
	}
	return nil
}

func factsEqual(a, b FactRecord) bool {
	return a.Category == b.Category &&
		a.Summary == b.Summary &&
		a.Body == b.Body &&
		a.Confidence == b.Confidence &&
		a.ScopeStatus == b.ScopeStatus
}

func objectivesEqual(a, b ObjectiveRecord) bool {
	return a.Status == b.Status && a.Objective == b.Objective && a.ResolutionSummary == b.ResolutionSummary
}

func attemptsEqual(a, b AttemptRecord) bool {
	return a.Status == b.Status && a.Summary == b.Summary
}

func entitiesEqual(a, b EntityRecord) bool {
	return a.Status == b.Status &&
		a.Kind == b.Kind &&
		a.Name == b.Name &&
		a.Locator == b.Locator &&
		a.Description == b.Description &&
		a.ScopeStatus == b.ScopeStatus &&
		a.CredentialRef == b.CredentialRef
}

func recordFromEntity(record EntityRecord) Record {
	return Record{
		Status:        record.Status,
		Kind:          record.Kind,
		Name:          record.Name,
		Locator:       record.Locator,
		Description:   record.Description,
		ScopeStatus:   record.ScopeStatus,
		CredentialRef: record.CredentialRef,
	}
}

func recordFromObjective(record ObjectiveRecord) Record {
	return Record{Status: record.Status, Objective: record.Objective, ResolutionSummary: record.ResolutionSummary}
}

func recordFromAttempt(record AttemptRecord) Record {
	return Record{Status: record.Status, Summary: record.Summary}
}

func recordFromFact(record FactRecord) Record {
	return Record{
		Category:    record.Category,
		Summary:     record.Summary,
		Body:        record.Body,
		Confidence:  record.Confidence,
		ScopeStatus: record.ScopeStatus,
	}
}

func (record Record) entityRecord() EntityRecord {
	return EntityRecord{
		Status:        record.Status,
		Kind:          record.Kind,
		Name:          record.Name,
		Locator:       record.Locator,
		Description:   record.Description,
		ScopeStatus:   record.ScopeStatus,
		CredentialRef: record.CredentialRef,
	}
}

func (record Record) objectiveRecord() ObjectiveRecord {
	return ObjectiveRecord{Status: record.Status, Objective: record.Objective, ResolutionSummary: record.ResolutionSummary}
}

func (record Record) attemptRecord() AttemptRecord {
	return AttemptRecord{Status: record.Status, Summary: record.Summary}
}

func (record Record) factRecord() FactRecord {
	return FactRecord{
		Category:    record.Category,
		Summary:     record.Summary,
		Body:        record.Body,
		Confidence:  record.Confidence,
		ScopeStatus: record.ScopeStatus,
	}
}

func parseCursor(cursor string) (int, error) {
	if cursor == "" {
		return 0, nil
	}
	if !strings.HasPrefix(cursor, "opaque:") {
		return 0, semanticError("semantic_validation", "history cursor is invalid", "cursor", nil)
	}
	offset, err := strconv.Atoi(strings.TrimPrefix(cursor, "opaque:"))
	if err != nil || offset < 0 {
		return 0, semanticError("semantic_validation", "history cursor is invalid", "cursor", nil)
	}
	return offset, nil
}

func makeCursor(offset int) string {
	return "opaque:" + strconv.Itoa(offset)
}

func semanticError(code, message, path string, details map[string]any) *Error {
	return &Error{Code: code, Message: message, Path: path, Retryable: false, Details: details}
}
