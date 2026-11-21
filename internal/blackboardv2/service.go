// Package blackboardv2 owns the Blackboard v2 semantic service. It is the
// durable service/store seam shared by later HTTP, MCP, CLI, and runtime
// projection adapters.
package blackboardv2

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"sync"

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

type Service struct {
	db              *store.DB
	evidenceConfig  EvidenceConfig
	runtimeRoot     string
	writeMu         sync.Mutex
	snapshotMu      sync.Mutex
	finishFail      FinishFailureInjector
	publicationFail SnapshotPublicationInjector
}

type SnapshotPublicationPoint string

const SnapshotPublicationAfterCommit SnapshotPublicationPoint = "after_commit"

type SnapshotPublicationInjector func(SnapshotPublicationPoint, string) error

func (s *Service) SetSnapshotPublicationInjector(injector SnapshotPublicationInjector) {
	s.publicationFail = injector
}

func NewService(db *store.DB) *Service {
	return &Service{db: db}
}

// NewServiceWithEvidence configures the confined filesystem roots used by
// Runtime Evidence retention.
func NewServiceWithEvidence(db *store.DB, config EvidenceConfig) *Service {
	return &Service{db: db, evidenceConfig: config, runtimeRoot: config.RuntimeRoot}
}

type Error struct {
	Code      string         `json:"code"`
	Message   string         `json:"message"`
	Path      string         `json:"path"`
	Retryable bool           `json:"retryable"`
	Details   map[string]any `json:"details"`
}

// MarshalJSON keeps every semantic error inside the closed v2 Error Envelope.
// Empty details remain an object rather than null so adapters expose one stable
// shape for actionable and non-actionable failures.
func (e Error) MarshalJSON() ([]byte, error) {
	type errorBody struct {
		Code      string         `json:"code"`
		Message   string         `json:"message"`
		Path      string         `json:"path"`
		Retryable bool           `json:"retryable"`
		Details   map[string]any `json:"details"`
	}
	details := e.Details
	if details == nil {
		details = map[string]any{}
	}
	return json.Marshal(errorBody{
		Code: e.Code, Message: e.Message, Path: e.Path, Retryable: e.Retryable, Details: details,
	})
}

func (e *Error) Error() string {
	if e == nil {
		return ""
	}
	return e.Message
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
	if err := decodeJSON([]byte(raw), &result); err != nil {
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

func canonicalRequestHash(batch ChangeBatch) (string, error) {
	raw, err := json.Marshal(batch)
	if err != nil {
		return "", fmt.Errorf("encode idempotency request: %w", err)
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:]), nil
}

func semanticError(code, message, path string, details map[string]any) *Error {
	return &Error{Code: code, Message: message, Path: path, Retryable: false, Details: details}
}
