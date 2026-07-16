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
	Op      string   `json:"op"`
	Key     string   `json:"key,omitempty"`
	Version int      `json:"version,omitempty"`
	Type    string   `json:"type,omitempty"`
	Record  any      `json:"record,omitempty"`
	Clear   []string `json:"clear,omitempty"`
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
	default:
		*change = Change{Op: op}
	}
	return nil
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
	Confidence  *string `json:"confidence,omitempty"`
	ScopeStatus *string `json:"scope_status,omitempty"`
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
	Record        FactRecord          `json:"record"`
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
	Kind    string     `json:"kind"`
	Version int        `json:"version"`
	Type    string     `json:"type"`
	Record  FactRecord `json:"record"`
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

// SnapshotWork is intentionally empty for #100; later tickets add Current Work.
type SnapshotWork struct{}

// SnapshotKnowledge groups current Project Knowledge records.
type SnapshotKnowledge struct {
	Facts map[string]SnapshotFact `json:"facts,omitempty"`
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
	record  FactRecord
}

// Apply atomically applies a semantic-change-batch/v2 to one Project.
func (s *Service) Apply(ctx context.Context, projectID string, batch ChangeBatch) (ChangeResult, error) {
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
	if replay, ok, err := idempotencyReplay(ctx, tx, projectID, batch.IdempotencyKey, requestHash); err != nil {
		return ChangeResult{}, err
	} else if ok {
		if err := tx.Commit(); err != nil {
			return ChangeResult{}, fmt.Errorf("commit Blackboard v2 replay: %w", err)
		}
		return replay, nil
	}

	revision, err := currentRevision(ctx, tx, projectID)
	if err != nil {
		return ChangeResult{}, err
	}
	changedRecords := make(map[string]int)
	now := time.Now().UTC().Format(time.RFC3339Nano)
	for index, change := range batch.Changes {
		if err := validateChangeShape(change, index); err != nil {
			return ChangeResult{}, err
		}
		switch change.Op {
		case "create":
			newRevision, key, version, changed, err := applyCreateFact(ctx, tx, projectID, revision, index, change, now)
			if err != nil {
				return ChangeResult{}, err
			}
			if changed {
				revision = newRevision
				changedRecords[key] = version
			}
		case "update":
			newRevision, key, version, changed, err := applyUpdateFact(ctx, tx, projectID, revision, index, change, now)
			if err != nil {
				return ChangeResult{}, err
			}
			if changed {
				revision = newRevision
				changedRecords[key] = version
			}
		default:
			return ChangeResult{}, semanticError("semantic_validation", "unsupported Blackboard v2 operation", fmt.Sprintf("changes[%d].op", index), nil)
		}
	}

	result := makeChangeResult(revision, changedRecords)
	resultJSON, err := json.Marshal(result)
	if err != nil {
		return ChangeResult{}, fmt.Errorf("encode idempotency result: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO blackboard_v2_idempotency_receipts (project_id, idempotency_key, request_hash, result_json, created_at)
		VALUES (?, ?, ?, ?, ?)`,
		projectID, batch.IdempotencyKey, requestHash, string(resultJSON), now,
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
		return CurrentDetail{}, err
	}
	return CurrentDetail{
		Schema:        recordSchema,
		Revision:      revision,
		Key:           found.key,
		Type:          found.typ,
		Version:       found.version,
		Record:        found.record,
		Relationships: []RelationshipTuple{},
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
	if _, err := loadCurrentRecord(ctx, tx, projectID, key); err != nil {
		return SemanticHistory{}, err
	}

	rows, err := tx.QueryContext(ctx, `
		SELECT version, type, record_json
		FROM blackboard_v2_record_history
		WHERE project_id = ? AND key = ?
		ORDER BY version ASC
		LIMIT ? OFFSET ?`,
		projectID, key, limit+1, offset,
	)
	if err != nil {
		return SemanticHistory{}, fmt.Errorf("read Blackboard v2 history: %w", err)
	}
	defer rows.Close()

	items := make([]HistoryItem, 0, limit)
	for rows.Next() {
		var version int
		var typ, raw string
		if err := rows.Scan(&version, &typ, &raw); err != nil {
			return SemanticHistory{}, fmt.Errorf("scan Blackboard v2 history: %w", err)
		}
		var record FactRecord
		if err := json.Unmarshal([]byte(raw), &record); err != nil {
			return SemanticHistory{}, fmt.Errorf("decode Blackboard v2 history record: %w", err)
		}
		if len(items) < limit {
			items = append(items, HistoryItem{Kind: "record", Version: version, Type: typ, Record: record})
		}
	}
	if err := rows.Err(); err != nil {
		return SemanticHistory{}, fmt.Errorf("iterate Blackboard v2 history: %w", err)
	}
	next := ""
	if len(items) == limit {
		var extra int
		err := tx.QueryRowContext(ctx, `
			SELECT COUNT(*)
			FROM blackboard_v2_record_history
			WHERE project_id = ? AND key = ?`, projectID, key,
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

// RuntimeSnapshot returns the complete current runtime-blackboard/v2 snapshot
// for the #100 Fact-only semantic path.
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
		SELECT key, version, record_json
		FROM blackboard_v2_records
		WHERE project_id = ? AND type = 'fact'
		ORDER BY key ASC`, projectID,
	)
	if err != nil {
		return RuntimeSnapshot{}, fmt.Errorf("read Blackboard v2 snapshot records: %w", err)
	}
	defer rows.Close()

	facts := make(map[string]SnapshotFact)
	for rows.Next() {
		var key, raw string
		var version int
		if err := rows.Scan(&key, &version, &raw); err != nil {
			return RuntimeSnapshot{}, fmt.Errorf("scan Blackboard v2 snapshot record: %w", err)
		}
		var record FactRecord
		if err := json.Unmarshal([]byte(raw), &record); err != nil {
			return RuntimeSnapshot{}, fmt.Errorf("decode Blackboard v2 snapshot record: %w", err)
		}
		facts[key] = SnapshotFact{
			Version:     version,
			Category:    record.Category,
			Summary:     record.Summary,
			Confidence:  record.Confidence,
			ScopeStatus: record.ScopeStatus,
		}
	}
	if err := rows.Err(); err != nil {
		return RuntimeSnapshot{}, fmt.Errorf("iterate Blackboard v2 snapshot records: %w", err)
	}
	knowledge := SnapshotKnowledge{}
	if len(facts) != 0 {
		knowledge.Facts = facts
	}
	return RuntimeSnapshot{
		Schema:    snapshotSchema,
		Semantics: snapshotSemantics,
		Revision:  revision,
		Work:      SnapshotWork{},
		Knowledge: knowledge,
		Relations: []RelationshipTuple{},
	}, nil
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
		if existing.typ == "fact" && factsEqual(existing.record, record) {
			return revision, change.Key, existing.version, false, nil
		}
		return revision, "", 0, false, semanticError("key_conflict", fmt.Sprintf("%s already exists", change.Key), fmt.Sprintf("changes[%d].key", index), map[string]any{"key": change.Key})
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return revision, "", 0, false, err
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
	nextRecord, err := applyFactPatch(existing.record, patch, change.Clear, fmt.Sprintf("changes[%d].clear", index))
	if err != nil {
		return revision, "", 0, false, err
	}
	if err := validateFactRecord(nextRecord, fmt.Sprintf("changes[%d].record", index)); err != nil {
		return revision, "", 0, false, err
	}
	if factsEqual(existing.record, nextRecord) {
		return revision, change.Key, existing.version, false, nil
	}
	historyJSON, err := json.Marshal(existing.record)
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
	if found.typ != "fact" {
		return storedRecord{}, semanticError("semantic_validation", "only Project Facts are implemented in this slice", "key", map[string]any{"key": key})
	}
	if err := json.Unmarshal([]byte(raw), &found.record); err != nil {
		return storedRecord{}, fmt.Errorf("decode Blackboard v2 record: %w", err)
	}
	return found, nil
}

func idempotencyReplay(ctx context.Context, tx *sql.Tx, projectID, key, requestHash string) (ChangeResult, bool, error) {
	var storedHash, raw string
	err := tx.QueryRowContext(ctx, `
		SELECT request_hash, result_json
		FROM blackboard_v2_idempotency_receipts
		WHERE project_id = ? AND idempotency_key = ?`,
		projectID, key,
	).Scan(&storedHash, &raw)
	if errors.Is(err, sql.ErrNoRows) {
		return ChangeResult{}, false, nil
	}
	if err != nil {
		return ChangeResult{}, false, fmt.Errorf("read idempotency receipt: %w", err)
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

func makeChangeResult(revision int, changedRecords map[string]int) ChangeResult {
	keys := make([]string, 0, len(changedRecords))
	for key := range changedRecords {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	records := make([]RecordVersionTuple, 0, len(keys))
	for _, key := range keys {
		records = append(records, RecordVersionTuple{key, changedRecords[key]})
	}
	return ChangeResult{
		Schema:    changeResultSchema,
		Revision:  revision,
		Records:   records,
		Relations: []RelationVersionTuple{},
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
	record, err := decodeFactRecord(typ, recordRaw)
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
	record, err := decodeFactPatch(typ, recordRaw)
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

func decodeFactRecord(typ string, raw json.RawMessage) (FactRecord, error) {
	if typ != "fact" {
		return FactRecord{}, fmt.Errorf("only Fact records are implemented in this slice")
	}
	var record FactRecord
	if err := strictDecodeJSON(raw, &record); err != nil {
		return FactRecord{}, fmt.Errorf("decode Fact record: %w", err)
	}
	return record, nil
}

func decodeFactPatch(typ string, raw json.RawMessage) (FactPatch, error) {
	if typ != "fact" {
		return FactPatch{}, fmt.Errorf("only Fact records are implemented in this slice")
	}
	var patch FactPatch
	if err := strictDecodeJSON(raw, &patch); err != nil {
		return FactPatch{}, fmt.Errorf("decode Fact patch: %w", err)
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
	}
	return nil
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
		decoded, err := decodeFactRecord("fact", record)
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
		return patch, nil
	case *FactPatch:
		if patch == nil {
			return FactPatch{}, semanticError("semantic_validation", "Fact update requires a Fact partial record", path, nil)
		}
		return *patch, nil
	case FactRecord:
		return FactPatch{
			Category:    stringPtr(patch.Category),
			Summary:     stringPtr(patch.Summary),
			Body:        stringPtr(patch.Body),
			Confidence:  stringPtr(patch.Confidence),
			ScopeStatus: stringPtr(patch.ScopeStatus),
		}, nil
	case json.RawMessage:
		decoded, err := decodeFactPatch("fact", patch)
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
	if patch.Confidence != nil {
		next.Confidence = *patch.Confidence
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
