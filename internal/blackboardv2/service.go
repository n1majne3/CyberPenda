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
		if err := rejectUnknownFields(fields, map[string]bool{"op": true, "key": true, "version": true, "status": true, "resolution_summary": true}); err != nil {
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

// Record is the current-detail union for the implemented Project Knowledge
// records. Empty fields are omitted so each type still serializes to its closed
// contract allowlist.
type Record struct {
	Status        string `json:"status,omitempty"`
	Kind          string `json:"kind,omitempty"`
	Name          string `json:"name,omitempty"`
	Locator       string `json:"locator,omitempty"`
	Description   string `json:"description,omitempty"`
	ScopeStatus   string `json:"scope_status,omitempty"`
	CredentialRef string `json:"credential_ref,omitempty"`
	Category      string `json:"category,omitempty"`
	Summary       string `json:"summary,omitempty"`
	Body          string `json:"body,omitempty"`
	Confidence    string `json:"confidence,omitempty"`
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
	Kind    string `json:"kind"`
	Version int    `json:"version"`
	Type    string `json:"type"`
	Record  Record `json:"record"`
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
	changedRelations := make(map[string]RelationVersionTuple)
	now := time.Now().UTC().Format(time.RFC3339Nano)
	for index, change := range batch.Changes {
		if err := validateChangeShape(change, index); err != nil {
			return ChangeResult{}, err
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
			newRevision, key, version, tuple, changed, err := applySupersede(ctx, tx, projectID, revision, index, change, now)
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
		record, err := decodeStoredRecord(typ, raw)
		if err != nil {
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
		SELECT key, type, version, record_json
		FROM blackboard_v2_records
		WHERE project_id = ? AND type IN ('entity', 'fact')
		ORDER BY key ASC`, projectID,
	)
	if err != nil {
		return RuntimeSnapshot{}, fmt.Errorf("read Blackboard v2 snapshot records: %w", err)
	}
	defer rows.Close()

	entities := make(map[string]SnapshotEntity)
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
		Work:      SnapshotWork{},
		Knowledge: knowledge,
		Relations: relationships,
	}, nil
}

func applyCreateRecord(ctx context.Context, tx *sql.Tx, projectID string, revision, index int, change Change, now string) (int, string, int, bool, error) {
	switch change.Type {
	case "entity":
		return applyCreateEntity(ctx, tx, projectID, revision, index, change, now)
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
	case "fact":
		return applyUpdateFact(ctx, tx, projectID, revision, index, change, now)
	default:
		return revision, "", 0, false, semanticError("semantic_validation", "unsupported Blackboard v2 record type in this slice", fmt.Sprintf("changes[%d].type", index), nil)
	}
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
	if change.Reason != "" {
		return revision, RelationVersionTuple{}, false, semanticError("semantic_validation", "relationship reason is not allowed for this relation", path+".reason", nil)
	}
	if change.Version != 0 {
		return revision, RelationVersionTuple{}, false, semanticError("semantic_validation", "relationship version is not accepted for a new ordinary relation", path+".version", nil)
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
	if change.Relation == "part_of" {
		wouldCycle, err := relationshipWouldCycle(ctx, tx, projectID, change.From, change.To)
		if err != nil {
			return revision, RelationVersionTuple{}, false, err
		}
		if wouldCycle {
			return revision, RelationVersionTuple{}, false, semanticError("semantic_validation", "Entity part_of containment must be acyclic", path+".to", nil)
		}
	}

	var existingVersion int
	err = tx.QueryRowContext(ctx, `
		SELECT version
		FROM blackboard_v2_relationships
		WHERE project_id = ? AND from_key = ? AND relation = ? AND to_key = ?`,
		projectID, change.From, change.Relation, change.To,
	).Scan(&existingVersion)
	if err == nil {
		return revision, RelationVersionTuple{change.From, change.Relation, change.To, existingVersion}, false, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return revision, RelationVersionTuple{}, false, fmt.Errorf("read Blackboard v2 relationship: %w", err)
	}
	nextRevision, err := incrementRevision(ctx, tx, projectID, revision)
	if err != nil {
		return revision, RelationVersionTuple{}, false, err
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO blackboard_v2_relationships (project_id, from_key, relation, to_key, version, reason, created_at, updated_at)
		VALUES (?, ?, ?, ?, 1, '', ?, ?)`,
		projectID, change.From, change.Relation, change.To, now, now,
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
	if change.Status != "retired" {
		return revision, "", 0, false, semanticError("semantic_validation", "only Entity retirement is implemented in this slice", path+".status", nil)
	}
	if err := validateConciseText(change.ResolutionSummary, path+".resolution_summary"); err != nil {
		return revision, "", 0, false, err
	}
	existing, err := loadCurrentRecord(ctx, tx, projectID, change.Key)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return revision, "", 0, false, semanticError("not_found", fmt.Sprintf("%s was not found", change.Key), path+".key", map[string]any{"key": change.Key})
		}
		return revision, "", 0, false, err
	}
	if existing.typ != "entity" {
		return revision, "", 0, false, semanticError("semantic_validation", "only Entities can be retired in this slice", path+".key", map[string]any{"key": change.Key})
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
	activeJSON, err := json.Marshal(existing.record.entityRecord())
	if err != nil {
		return revision, "", 0, false, fmt.Errorf("encode active Entity record: %w", err)
	}
	retiredRecord := existing.record.entityRecord()
	retiredRecord.Status = "retired"
	retiredJSON, err := json.Marshal(retiredRecord)
	if err != nil {
		return revision, "", 0, false, fmt.Errorf("encode retired Entity record: %w", err)
	}
	nextVersion := existing.version + 1
	nextRevision, err := incrementRevision(ctx, tx, projectID, revision)
	if err != nil {
		return revision, "", 0, false, err
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO blackboard_v2_record_history (project_id, key, version, type, record_json, recorded_at)
		VALUES (?, ?, ?, 'entity', ?, ?)`,
		projectID, change.Key, existing.version, string(activeJSON), now,
	); err != nil {
		return revision, "", 0, false, fmt.Errorf("store active Entity history: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO blackboard_v2_record_history (project_id, key, version, type, record_json, recorded_at)
		VALUES (?, ?, ?, 'entity', ?, ?)`,
		projectID, change.Key, nextVersion, string(retiredJSON), now,
	); err != nil {
		return revision, "", 0, false, fmt.Errorf("store retired Entity history: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `
		DELETE FROM blackboard_v2_relationships
		WHERE project_id = ? AND (from_key = ? OR to_key = ?)`,
		projectID, change.Key, change.Key,
	); err != nil {
		return revision, "", 0, false, fmt.Errorf("remove retired Entity relationships: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `
		DELETE FROM blackboard_v2_records
		WHERE project_id = ? AND key = ?`,
		projectID, change.Key,
	); err != nil {
		return revision, "", 0, false, fmt.Errorf("remove retired Entity current record: %w", err)
	}
	return nextRevision, change.Key, nextVersion, true, nil
}

func applySupersede(ctx context.Context, tx *sql.Tx, projectID string, revision, index int, change Change, now string) (int, string, int, RelationVersionTuple, bool, error) {
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
	if replacement.typ != "entity" || replaced.typ != "entity" {
		return revision, "", 0, RelationVersionTuple{}, false, semanticError("semantic_validation", "Entity supersede requires two Entities", path, map[string]any{"replacement_type": replacement.typ, "replaced_type": replaced.typ})
	}
	if change.ReplacementVersion != replacement.version {
		return revision, "", 0, RelationVersionTuple{}, false, semanticError(
			"version_conflict",
			fmt.Sprintf("%s changed", change.Replacement),
			path+".replacement_version",
			map[string]any{"key": change.Replacement, "expected_version": float64(change.ReplacementVersion), "current_version": float64(replacement.version), "next_action": "read_current_record"},
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
	activeJSON, err := json.Marshal(replaced.record.entityRecord())
	if err != nil {
		return revision, "", 0, RelationVersionTuple{}, false, fmt.Errorf("encode active Entity record: %w", err)
	}
	supersededRecord := replaced.record.entityRecord()
	supersededRecord.Status = "superseded"
	supersededJSON, err := json.Marshal(supersededRecord)
	if err != nil {
		return revision, "", 0, RelationVersionTuple{}, false, fmt.Errorf("encode superseded Entity record: %w", err)
	}
	nextVersion := replaced.version + 1
	nextRevision, err := incrementRevision(ctx, tx, projectID, revision)
	if err != nil {
		return revision, "", 0, RelationVersionTuple{}, false, err
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO blackboard_v2_record_history (project_id, key, version, type, record_json, recorded_at)
		VALUES (?, ?, ?, 'entity', ?, ?)`,
		projectID, change.Replaced, replaced.version, string(activeJSON), now,
	); err != nil {
		return revision, "", 0, RelationVersionTuple{}, false, fmt.Errorf("store active Entity history: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO blackboard_v2_record_history (project_id, key, version, type, record_json, recorded_at)
		VALUES (?, ?, ?, 'entity', ?, ?)`,
		projectID, change.Replaced, nextVersion, string(supersededJSON), now,
	); err != nil {
		return revision, "", 0, RelationVersionTuple{}, false, fmt.Errorf("store superseded Entity history: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `
		DELETE FROM blackboard_v2_relationships
		WHERE project_id = ? AND (from_key = ? OR to_key = ?)`,
		projectID, change.Replaced, change.Replaced,
	); err != nil {
		return revision, "", 0, RelationVersionTuple{}, false, fmt.Errorf("remove superseded Entity relationships: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `
		DELETE FROM blackboard_v2_records
		WHERE project_id = ? AND key = ?`,
		projectID, change.Replaced,
	); err != nil {
		return revision, "", 0, RelationVersionTuple{}, false, fmt.Errorf("remove superseded Entity current record: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO blackboard_v2_relationships (project_id, from_key, relation, to_key, version, reason, created_at, updated_at)
		VALUES (?, ?, 'supersedes', ?, 1, '', ?, ?)`,
		projectID, change.Replacement, change.Replaced, now, now,
	); err != nil {
		return revision, "", 0, RelationVersionTuple{}, false, fmt.Errorf("store Entity supersedes relationship: %w", err)
	}
	tuple := RelationVersionTuple{change.Replacement, "supersedes", change.Replaced, 1}
	return nextRevision, change.Replaced, nextVersion, tuple, true, nil
}

func validateRelationshipEndpoint(relation, fromType, toType, path string) error {
	switch relation {
	case "about":
		if toType == "entity" && fromType == "fact" {
			return nil
		}
		return semanticError("semantic_validation", "about must connect an allowed record to an Entity", path, map[string]any{"from_type": fromType, "to_type": toType})
	case "part_of":
		if fromType == "entity" && toType == "entity" {
			return nil
		}
		return semanticError("semantic_validation", "Entity part_of must point from child Entity to parent Entity", path, map[string]any{"from_type": fromType, "to_type": toType})
	case "supersedes":
		return semanticError("semantic_validation", "supersedes is created only by the supersede operation", path, nil)
	default:
		return semanticError("semantic_validation", "unsupported relationship type in this slice", path, nil)
	}
}

func relationshipWouldCycle(ctx context.Context, tx *sql.Tx, projectID, fromKey, toKey string) (bool, error) {
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
			WHERE project_id = ? AND from_key = ? AND relation = 'part_of'
			ORDER BY to_key ASC`,
			projectID, key,
		)
		if err != nil {
			return false, fmt.Errorf("read Entity part_of containment: %w", err)
		}
		for rows.Next() {
			var parent string
			if err := rows.Scan(&parent); err != nil {
				rows.Close()
				return false, fmt.Errorf("scan Entity part_of containment: %w", err)
			}
			stack = append(stack, parent)
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return false, fmt.Errorf("iterate Entity part_of containment: %w", err)
		}
		rows.Close()
	}
	return false, nil
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

func decodeStoredRecord(typ, raw string) (Record, error) {
	switch typ {
	case "entity":
		var record EntityRecord
		if err := json.Unmarshal([]byte(raw), &record); err != nil {
			return Record{}, err
		}
		return recordFromEntity(record), nil
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
	var version int
	if raw, ok := fields["version"]; ok {
		if err := json.Unmarshal(raw, &version); err != nil {
			return Change{}, fmt.Errorf("decode relation version: %w", err)
		}
	}
	reason := ""
	if raw, ok := fields["reason"]; ok {
		if err := json.Unmarshal(raw, &reason); err != nil {
			return Change{}, fmt.Errorf("decode relation reason: %w", err)
		}
		if relation == "about" || relation == "part_of" {
			return Change{}, fmt.Errorf("relationship reason is forbidden for %s", relation)
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
	resolutionSummary, err := decodeRequiredString(fields, "resolution_summary")
	if err != nil {
		return Change{}, err
	}
	return Change{Op: "transition", Key: key, Version: version, Status: status, ResolutionSummary: resolutionSummary}, nil
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
	if raw, ok := fields["replacement_version"]; !ok {
		return Change{}, fmt.Errorf("supersede replacement_version is required")
	} else if err := json.Unmarshal(raw, &replacementVersion); err != nil {
		return Change{}, fmt.Errorf("decode supersede replacement_version: %w", err)
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
	return patch, nil
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
	case "supersede":
		if change.Replacement == "" {
			return semanticError("semantic_validation", "supersede replacement is required", fmt.Sprintf("changes[%d].replacement", index), nil)
		}
		if change.ReplacementVersion < 1 {
			return semanticError("semantic_validation", "supersede requires the current replacement version", fmt.Sprintf("changes[%d].replacement_version", index), nil)
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
		return patch, nil
	case *EntityPatch:
		if patch == nil {
			return EntityPatch{}, semanticError("semantic_validation", "Entity update requires an Entity partial record", path, nil)
		}
		return *patch, nil
	case EntityRecord:
		return EntityPatch{
			Kind:          stringPtr(patch.Kind),
			Name:          stringPtr(patch.Name),
			Locator:       stringPtr(patch.Locator),
			Description:   stringPtr(patch.Description),
			ScopeStatus:   stringPtr(patch.ScopeStatus),
			CredentialRef: stringPtr(patch.CredentialRef),
		}, nil
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
	if record.Locator != "" {
		if err := validateLocator(record.Locator, path+".locator"); err != nil {
			return err
		}
	}
	if record.Description != "" {
		if err := validateConciseText(record.Description, path+".description"); err != nil {
			return err
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
