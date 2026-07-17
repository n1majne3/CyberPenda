// Package blackboardv2 owns the Blackboard v2 semantic service. It is the
// durable service/store seam shared by later HTTP, MCP, CLI, and runtime
// projection adapters.
package blackboardv2

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"pentest/internal/blackboardv2grammar"
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
	db             *store.DB
	evidenceConfig EvidenceConfig
}

// NewService returns a Blackboard v2 semantic service backed by the Store.
func NewService(db *store.DB) *Service {
	return &Service{db: db}
}

// NewServiceWithEvidence configures the confined filesystem roots used by
// Runtime Evidence retention.
func NewServiceWithEvidence(db *store.DB, config EvidenceConfig) *Service {
	return &Service{db: db, evidenceConfig: config}
}

// ChangeBatch is the semantic-change-batch/v2 envelope.
type ChangeBatch struct {
	Schema         string   `json:"schema"`
	IdempotencyKey string   `json:"idempotency_key"`
	Changes        []Change `json:"changes"`
}

// UnmarshalJSON enforces the closed semantic-change-batch/v2 envelope before
// adapters can lose required-field presence or the nil/empty changes boundary.
func (batch *ChangeBatch) UnmarshalJSON(raw []byte) error {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return err
	}
	for field := range fields {
		switch field {
		case "schema", "idempotency_key", "changes":
		default:
			return fmt.Errorf("unknown ChangeBatch field %q", field)
		}
	}
	schema, err := decodeRequiredString(fields, "schema")
	if err != nil {
		return err
	}
	if schema != changeBatchSchema {
		return fmt.Errorf("schema must be %q", changeBatchSchema)
	}
	idempotencyKey, err := decodeRequiredString(fields, "idempotency_key")
	if err != nil {
		return err
	}
	if idempotencyKey == "" {
		return fmt.Errorf("idempotency_key must not be empty")
	}
	changesRaw, ok := fields["changes"]
	if !ok {
		return fmt.Errorf("changes is required")
	}
	if bytes.Equal(bytes.TrimSpace(changesRaw), []byte("null")) {
		return fmt.Errorf("changes must be an array")
	}
	var changes []Change
	if err := json.Unmarshal(changesRaw, &changes); err != nil {
		return fmt.Errorf("decode changes: %w", err)
	}
	*batch = ChangeBatch{Schema: schema, IdempotencyKey: idempotencyKey, Changes: changes}
	return nil
}

// Change is the closed semantic operation union for a ChangeBatch.
type Change struct {
	Op                  string   `json:"op"`
	Key                 string   `json:"key,omitempty"`
	Version             int      `json:"version,omitempty"`
	Type                string   `json:"type,omitempty"`
	Record              any      `json:"record,omitempty"`
	Clear               []string `json:"clear,omitempty"`
	From                string   `json:"from,omitempty"`
	Relation            string   `json:"relation,omitempty"`
	To                  string   `json:"to,omitempty"`
	Reason              string   `json:"reason,omitempty"`
	Status              string   `json:"status,omitempty"`
	Summary             string   `json:"summary,omitempty"`
	ResolutionSummary   string   `json:"resolution_summary,omitempty"`
	VerificationSummary string   `json:"verification_summary,omitempty"`
	Replacement         string   `json:"replacement,omitempty"`
	ReplacementVersion  int      `json:"replacement_version,omitempty"`
	Replaced            string   `json:"replaced,omitempty"`
	ReplacedVersion     int      `json:"replaced_version,omitempty"`
	Source              string   `json:"source,omitempty"`
	SourceVersion       int      `json:"source_version,omitempty"`
	Canonical           string   `json:"canonical,omitempty"`
	CanonicalVersion    int      `json:"canonical_version,omitempty"`
	CanonicalRecord     any      `json:"canonical_record,omitempty"`
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
	case "unrelate":
		if err := rejectUnknownFields(fields, map[string]bool{"op": true, "from": true, "relation": true, "to": true, "version": true}); err != nil {
			return err
		}
		decoded, err := decodeUnrelateChange(fields)
		if err != nil {
			return err
		}
		*change = decoded
	case "transition":
		if err := rejectUnknownFields(fields, map[string]bool{"op": true, "key": true, "version": true, "status": true, "summary": true, "resolution_summary": true, "verification_summary": true}); err != nil {
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
	case "merge":
		if err := rejectUnknownFields(fields, map[string]bool{"op": true, "source": true, "source_version": true, "canonical": true, "canonical_version": true, "canonical_record": true, "clear": true}); err != nil {
			return err
		}
		decoded, err := decodeMergeChange(fields)
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

// FindingRecord is the complete caller-writable Finding DTO. Severity and
// CVSSPending are deliberately absent because the service derives them.
type FindingRecord struct {
	Status         string `json:"status"`
	Title          string `json:"title"`
	Target         string `json:"target,omitempty"`
	Description    string `json:"description,omitempty"`
	Proof          string `json:"proof,omitempty"`
	Impact         string `json:"impact,omitempty"`
	Recommendation string `json:"recommendation,omitempty"`
	CVSSVersion    string `json:"cvss_version,omitempty"`
	CVSSVector     string `json:"cvss_vector,omitempty"`
}

// FindingPatch is the closed partial update shape for Findings. Lifecycle
// state changes use transition; derived scoring fields are never writable.
type FindingPatch struct {
	Title          *string `json:"title,omitempty"`
	Target         *string `json:"target,omitempty"`
	Description    *string `json:"description,omitempty"`
	Proof          *string `json:"proof,omitempty"`
	Impact         *string `json:"impact,omitempty"`
	Recommendation *string `json:"recommendation,omitempty"`
	CVSSVersion    *string `json:"cvss_version,omitempty"`
	CVSSVector     *string `json:"cvss_vector,omitempty"`
}

// SolutionRecord is the complete semantic CTF Solution DTO.
type SolutionRecord struct {
	Status              string `json:"status"`
	Kind                string `json:"kind"`
	Summary             string `json:"summary"`
	Value               string `json:"value,omitempty"`
	VerificationSummary string `json:"verification_summary,omitempty"`
}

// SolutionPatch is the closed partial update shape for current Solutions.
type SolutionPatch struct {
	Kind                *string `json:"kind,omitempty"`
	Summary             *string `json:"summary,omitempty"`
	Value               *string `json:"value,omitempty"`
	VerificationSummary *string `json:"verification_summary,omitempty"`
}

// EvidenceRecord is the complete semantic Evidence detail DTO. Filesystem and
// integrity fields are server-derived by RetainEvidenceForContinuation.
type EvidenceRecord struct {
	Status       string `json:"status"`
	ArtifactType string `json:"artifact_type"`
	Summary      string `json:"summary"`
	MediaType    string `json:"media_type,omitempty"`
	SourcePath   string `json:"source_path,omitempty"`
	ManagedPath  string `json:"managed_path"`
	SHA256       string `json:"sha256"`
	Size         int64  `json:"size"`
	CapturedAt   string `json:"captured_at,omitempty"`
}

// EvidencePatch is the closed semantic-only update shape for Evidence.
type EvidencePatch struct {
	Summary    *string `json:"summary,omitempty"`
	MediaType  *string `json:"media_type,omitempty"`
	CapturedAt *string `json:"captured_at,omitempty"`
}

// Record is the current-detail union for the implemented Project Knowledge
// records. Empty fields are omitted so each type still serializes to its closed
// contract allowlist.
type Record struct {
	Status              string `json:"status,omitempty"`
	Objective           string `json:"objective,omitempty"`
	ResolutionSummary   string `json:"resolution_summary,omitempty"`
	Kind                string `json:"kind,omitempty"`
	Name                string `json:"name,omitempty"`
	Locator             string `json:"locator,omitempty"`
	Description         string `json:"description,omitempty"`
	ScopeStatus         string `json:"scope_status,omitempty"`
	CredentialRef       string `json:"credential_ref,omitempty"`
	Category            string `json:"category,omitempty"`
	Summary             string `json:"summary,omitempty"`
	Body                string `json:"body,omitempty"`
	Confidence          string `json:"confidence,omitempty"`
	Title               string `json:"title,omitempty"`
	Target              string `json:"target,omitempty"`
	Proof               string `json:"proof,omitempty"`
	Impact              string `json:"impact,omitempty"`
	Recommendation      string `json:"recommendation,omitempty"`
	CVSSVersion         string `json:"cvss_version,omitempty"`
	CVSSVector          string `json:"cvss_vector,omitempty"`
	Severity            string `json:"severity,omitempty"`
	CVSSPending         bool   `json:"cvss_pending,omitempty"`
	Value               string `json:"value,omitempty"`
	VerificationSummary string `json:"verification_summary,omitempty"`
	ArtifactType        string `json:"artifact_type,omitempty"`
	MediaType           string `json:"media_type,omitempty"`
	SourcePath          string `json:"source_path,omitempty"`
	ManagedPath         string `json:"managed_path,omitempty"`
	SHA256              string `json:"sha256,omitempty"`
	Size                int64  `json:"size,omitempty"`
	CapturedAt          string `json:"captured_at,omitempty"`
}

// MarshalJSON keeps Evidence's required zero size while preserving the
// existing closed DTO shape for every other record type.
func (record Record) MarshalJSON() ([]byte, error) {
	if isOneOf(record.Status, "candidate", "verified", "rejected", "superseded") && isOneOf(record.Kind, "answer", "flag", "procedure") {
		return json.Marshal(record.solutionRecord())
	}
	if record.Title != "" && isOneOf(record.Status, "unconfirmed", "confirmed", "false_positive", "superseded") {
		return json.Marshal(record.findingOutputRecord())
	}
	if record.ArtifactType != "" && record.ManagedPath != "" && record.SHA256 != "" {
		return json.Marshal(record.evidenceRecord())
	}
	type recordJSON Record
	return json.Marshal(recordJSON(record))
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
	Key      string  `json:"key,omitempty"`
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
	Entities  map[string]SnapshotEntity   `json:"entities,omitempty"`
	Facts     map[string]SnapshotFact     `json:"facts,omitempty"`
	Findings  map[string]SnapshotFinding  `json:"findings,omitempty"`
	Solutions map[string]SnapshotSolution `json:"solutions,omitempty"`
	Evidence  map[string]SnapshotEvidence `json:"evidence,omitempty"`
}

// SnapshotSolution is the Runtime Snapshot allowlist for current Solutions.
type SnapshotSolution struct {
	Version             int    `json:"version"`
	Status              string `json:"status"`
	Kind                string `json:"kind"`
	Summary             string `json:"summary"`
	Value               string `json:"value,omitempty"`
	VerificationSummary string `json:"verification_summary,omitempty"`
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

// SnapshotFinding is the Runtime Snapshot allowlist for current Findings.
type SnapshotFinding struct {
	Version     int    `json:"version"`
	Status      string `json:"status"`
	Title       string `json:"title"`
	Target      string `json:"target,omitempty"`
	Description string `json:"description,omitempty"`
	Severity    string `json:"severity,omitempty"`
	CVSSPending bool   `json:"cvss_pending"`
}

// SnapshotEvidence is the Runtime Snapshot allowlist for Evidence.
type SnapshotEvidence struct {
	Version      int    `json:"version"`
	Status       string `json:"status"`
	ArtifactType string `json:"artifact_type"`
	Summary      string `json:"summary"`
	MediaType    string `json:"media_type,omitempty"`
	CapturedAt   string `json:"captured_at,omitempty"`
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

type terminalAttemptValidation struct {
	status string
	path   string
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

	invalidAttempts := make([]string, 0)
	for _, existing := range owned {
		tested, err := currentOutgoingRelationshipCount(ctx, tx, projectID, existing.key, "tests")
		if err != nil {
			return ChangeResult{}, err
		}
		if tested == 0 {
			tested, err = historicalOutgoingRelationshipCount(ctx, tx, projectID, existing.key, "tests")
			if err != nil {
				return ChangeResult{}, err
			}
		}
		if tested == 0 {
			invalidAttempts = append(invalidAttempts, existing.key)
		}
	}
	if len(invalidAttempts) != 0 {
		return ChangeResult{}, semanticError("semantic_validation", "Attempt reconciliation found owned Attempts without tested targets", "", map[string]any{
			"reason":      "missing_tested_target",
			"attempts":    invalidAttempts,
			"next_action": "repair_invalid_attempt",
		})
	}

	changedRecords := make(map[string]int)
	now := time.Now().UTC().Format(time.RFC3339Nano)
	for _, existing := range owned {
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
	if batch.Changes == nil {
		return ChangeResult{}, semanticError("semantic_validation", "changes must be a non-null array", "changes", nil)
	}
	for index, change := range batch.Changes {
		if err := validateChangeDTOShape(change, index); err != nil {
			return ChangeResult{}, err
		}
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
	if continuationID != "" {
		for index, change := range batch.Changes {
			if change.Op == "merge" {
				return ChangeResult{}, semanticError("authority_denied", "governed Record Merge requires explicit operator approval", fmt.Sprintf("changes[%d].op", index), nil)
			}
		}
	}

	revision, err := currentRevision(ctx, tx, projectID)
	if err != nil {
		return ChangeResult{}, err
	}
	changedRecords := make(map[string]int)
	changedRelations := make(map[string]RelationVersionTuple)
	createdThisBatch := make(map[string]bool)
	runtimeConfirmedFacts := make(map[string]string)
	runtimeCreatedAttempts := make(map[string]string)
	terminalAttempts := make(map[string]terminalAttemptValidation)
	dependentConfirmedFacts := make(map[string]string)
	now := time.Now().UTC().Format(time.RFC3339Nano)
	for index, change := range batch.Changes {
		change, err = resolveChangeRedirects(ctx, tx, projectID, change)
		if err != nil {
			return ChangeResult{}, err
		}
		if change.Op == "transition" && isOneOf(change.Status, "verified", "rejected") {
			if err := ensureCTFProject(ctx, tx, projectID, fmt.Sprintf("changes[%d].status", index)); err != nil {
				return ChangeResult{}, err
			}
		}
		if continuationID != "" {
			if err := validateContinuationChangeOwnership(ctx, tx, projectID, continuationID, change, index); err != nil {
				return ChangeResult{}, err
			}
		}
		if change.Op == "transition" && change.Status == "missing" {
			if err := collectEvidenceDependentConfirmedFacts(ctx, tx, projectID, change.Key, fmt.Sprintf("changes[%d].status", index), dependentConfirmedFacts); err != nil {
				return ChangeResult{}, err
			}
		}
		if change.Op == "transition" && change.Status == "tentative" {
			if err := collectSupportingFactDependentConfirmedFacts(ctx, tx, projectID, change.Key, fmt.Sprintf("changes[%d].status", index), dependentConfirmedFacts); err != nil {
				return ChangeResult{}, err
			}
		}
		if change.Op == "supersede" {
			if err := collectEvidenceDependentConfirmedFacts(ctx, tx, projectID, change.Replaced, fmt.Sprintf("changes[%d].replaced", index), dependentConfirmedFacts); err != nil {
				return ChangeResult{}, err
			}
			if err := collectSupportingFactDependentConfirmedFacts(ctx, tx, projectID, change.Replaced, fmt.Sprintf("changes[%d].replaced", index), dependentConfirmedFacts); err != nil {
				return ChangeResult{}, err
			}
		}
		if change.Op == "merge" {
			if err := collectEvidenceDependentConfirmedFacts(ctx, tx, projectID, change.Source, fmt.Sprintf("changes[%d].source", index), dependentConfirmedFacts); err != nil {
				return ChangeResult{}, err
			}
			if err := collectSupportingFactDependentConfirmedFacts(ctx, tx, projectID, change.Source, fmt.Sprintf("changes[%d].source", index), dependentConfirmedFacts); err != nil {
				return ChangeResult{}, err
			}
		}
		if change.Op == "unrelate" && change.Relation == "evidences" {
			if err := collectEvidenceDependentConfirmedFacts(ctx, tx, projectID, change.From, fmt.Sprintf("changes[%d].relation", index), dependentConfirmedFacts); err != nil {
				return ChangeResult{}, err
			}
		}
		if change.Op == "unrelate" && change.Relation == "supports" {
			if err := collectSupportingFactDependentConfirmedFacts(ctx, tx, projectID, change.From, fmt.Sprintf("changes[%d].relation", index), dependentConfirmedFacts); err != nil {
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
				if continuationID != "" && change.Type == "fact" {
					created, err := loadCurrentRecord(ctx, tx, projectID, key)
					if err != nil {
						return ChangeResult{}, err
					}
					if created.record.factRecord().Confidence == "confirmed" {
						runtimeConfirmedFacts[key] = fmt.Sprintf("changes[%d].record.confidence", index)
					}
				}
				if continuationID != "" && change.Type == "attempt" {
					if err := bindAttemptOrigin(ctx, tx, projectID, key, continuationID, now); err != nil {
						return ChangeResult{}, err
					}
					runtimeCreatedAttempts[key] = fmt.Sprintf("changes[%d]", index)
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
		case "unrelate":
			newRevision, tuple, err := applyUnrelate(ctx, tx, projectID, revision, index, change, now)
			if err != nil {
				return ChangeResult{}, err
			}
			revision = newRevision
			changedRelations[relationKey(tuple)] = tuple
		case "transition":
			newRevision, key, version, changed, err := applyTransition(ctx, tx, projectID, revision, index, change, now)
			if err != nil {
				return ChangeResult{}, err
			}
			if changed {
				revision = newRevision
				changedRecords[key] = version
				if isOneOf(change.Status, "succeeded", "failed", "blocked", "inconclusive") {
					terminalAttempts[key] = terminalAttemptValidation{status: change.Status, path: fmt.Sprintf("changes[%d].status", index)}
				}
				if continuationID != "" && change.Status == "confirmed" {
					runtimeConfirmedFacts[key] = fmt.Sprintf("changes[%d].status", index)
				}
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
		case "merge":
			newRevision, key, version, tuples, err := applyMerge(ctx, tx, projectID, revision, index, change, now)
			if err != nil {
				return ChangeResult{}, err
			}
			revision = newRevision
			changedRecords[key] = version
			for _, tuple := range tuples {
				changedRelations[relationKey(tuple)] = tuple
			}
		default:
			return ChangeResult{}, semanticError("semantic_validation", "unsupported Blackboard v2 operation", fmt.Sprintf("changes[%d].op", index), nil)
		}
	}
	if err := validateFinalTerminalAttempts(ctx, tx, projectID, terminalAttempts); err != nil {
		return ChangeResult{}, err
	}
	if continuationID != "" {
		attemptKeys := make([]string, 0, len(runtimeCreatedAttempts))
		for key := range runtimeCreatedAttempts {
			attemptKeys = append(attemptKeys, key)
		}
		sort.Strings(attemptKeys)
		for _, key := range attemptKeys {
			current, err := loadCurrentRecord(ctx, tx, projectID, key)
			if errors.Is(err, sql.ErrNoRows) {
				continue
			}
			if err != nil {
				return ChangeResult{}, err
			}
			if current.typ != "attempt" {
				continue
			}
			tested, err := currentOutgoingRelationshipCount(ctx, tx, projectID, key, "tests")
			if err != nil {
				return ChangeResult{}, err
			}
			if tested == 0 {
				return ChangeResult{}, semanticError("semantic_validation", "Runtime-created Attempt requires a current tested target at batch end", runtimeCreatedAttempts[key], map[string]any{"key": key})
			}
		}

		keys := make([]string, 0, len(runtimeConfirmedFacts))
		for key := range runtimeConfirmedFacts {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			current, err := loadCurrentRecord(ctx, tx, projectID, key)
			if errors.Is(err, sql.ErrNoRows) {
				continue
			}
			if err != nil {
				return ChangeResult{}, err
			}
			if current.typ != "fact" || current.record.factRecord().Confidence != "confirmed" {
				continue
			}
			hasBasis, err := s.runtimeFactConfirmationHasBasis(ctx, tx, projectID, continuationID, key)
			if err != nil {
				return ChangeResult{}, err
			}
			if !hasBasis {
				return ChangeResult{}, semanticError("semantic_validation", "Runtime Fact confirmation requires an accepted semantic basis", runtimeConfirmedFacts[key], nil)
			}
		}
	}
	if err := s.validateDependentConfirmedFactBases(ctx, tx, projectID, dependentConfirmedFacts); err != nil {
		return ChangeResult{}, err
	}
	if err := validateAllConfirmedFindings(ctx, tx, projectID); err != nil {
		return ChangeResult{}, err
	}
	if err := validateAllVerifiedSolutions(ctx, tx, projectID); err != nil {
		return ChangeResult{}, err
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
	if err := validateKey(key, "key"); err != nil {
		return SemanticHistory{}, err
	}
	cursor, err := parseCursor(options.Cursor)
	if err != nil {
		return SemanticHistory{}, err
	}
	if options.Limit < 0 || options.Limit > 100 {
		return SemanticHistory{}, semanticError("semantic_validation", "history limit must be between 1 and 100", "limit", nil)
	}
	limit := options.Limit
	if cursor.present {
		if limit == 0 {
			limit = cursor.limit
		} else if limit != cursor.limit {
			return SemanticHistory{}, invalidHistoryCursorError("page_size_mismatch")
		}
	} else if limit == 0 {
		limit = 20
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
	canonicalKey, _, err := resolveKeyRedirect(ctx, tx, projectID, key)
	if err != nil {
		return SemanticHistory{}, err
	}
	key = canonicalKey
	if cursor.present && cursor.key != canonicalKey {
		return SemanticHistory{}, invalidHistoryCursorError("key_mismatch")
	}
	if cursor.present && cursor.revision != revision {
		return SemanticHistory{}, semanticError("semantic_validation", "history cursor is stale", "cursor", map[string]any{
			"reason":           "stale_cursor",
			"cursor_revision":  float64(cursor.revision),
			"current_revision": float64(revision),
			"next_action":      "restart_history_read",
		})
	}
	offset := cursor.offset
	hasCurrent := true
	if _, err := loadCurrentRecord(ctx, tx, projectID, canonicalKey); err != nil {
		if !errors.Is(err, sql.ErrNoRows) {
			return SemanticHistory{}, err
		}
		hasCurrent = false
	}
	if !hasCurrent {
		var recordHistoryCount int
		err := tx.QueryRowContext(ctx, `
			SELECT COUNT(*) FROM blackboard_v2_record_history
			WHERE project_id = ? AND (key = ? OR key IN (
				SELECT source_key FROM blackboard_v2_key_redirects WHERE project_id = ? AND canonical_key = ?
			))`, projectID, key, projectID, key).Scan(&recordHistoryCount)
		if err != nil {
			return SemanticHistory{}, fmt.Errorf("check Blackboard v2 history: %w", err)
		}
		if recordHistoryCount == 0 {
			return SemanticHistory{}, semanticError("not_found", fmt.Sprintf("%s was not found", key), "key", map[string]any{"key": key})
		}
	}
	total, err := countSemanticHistoryItems(ctx, tx, projectID, key)
	if err != nil {
		return SemanticHistory{}, err
	}
	if cursor.present && offset >= total {
		return SemanticHistory{}, invalidHistoryCursorError("offset_out_of_range")
	}

	rows, err := tx.QueryContext(ctx, `
		SELECT identity_key, kind, version, type, record_json, from_key, relation, to_key, reason
		FROM (
			SELECT 0 AS sort_group, recorded_at AS sort_time, key AS identity_key, 'record' AS kind, version, type, record_json,
			       '' AS from_key, '' AS relation, '' AS to_key, '' AS reason
			FROM blackboard_v2_record_history
			WHERE project_id = ? AND (
				key = ? OR key IN (
					SELECT source_key FROM blackboard_v2_key_redirects
					WHERE project_id = ? AND canonical_key = ?
				)
			)
			UNION ALL
			SELECT 1 AS sort_group, recorded_at AS sort_time, from_key || char(0) || relation || char(0) || to_key AS identity_key, 'relationship' AS kind, version, '' AS type, '' AS record_json,
			       from_key, relation, to_key, reason
			FROM blackboard_v2_relationship_history
			WHERE project_id = ? AND (
				from_key = ? OR to_key = ? OR
				from_key IN (SELECT source_key FROM blackboard_v2_key_redirects WHERE project_id = ? AND canonical_key = ?) OR
				to_key IN (SELECT source_key FROM blackboard_v2_key_redirects WHERE project_id = ? AND canonical_key = ?)
			)
		)
		ORDER BY sort_group ASC,
		         CASE WHEN sort_group = 0 THEN sort_time ELSE '' END ASC,
		         CASE WHEN sort_group = 0 THEN version ELSE 0 END ASC,
		         CASE WHEN sort_group = 0 THEN identity_key ELSE '' END ASC,
		         relation ASC, from_key ASC, to_key ASC, version ASC
		LIMIT ? OFFSET ?`,
		projectID, key, projectID, key,
		projectID, key, key, projectID, key, projectID, key,
		limit, offset,
	)
	if err != nil {
		return SemanticHistory{}, fmt.Errorf("read Blackboard v2 history: %w", err)
	}
	defer rows.Close()

	items := make([]HistoryItem, 0, limit)
	for rows.Next() {
		var version int
		var identityKey, kind, typ, raw, from, relation, to, reason string
		if err := rows.Scan(&identityKey, &kind, &version, &typ, &raw, &from, &relation, &to, &reason); err != nil {
			return SemanticHistory{}, fmt.Errorf("scan Blackboard v2 history: %w", err)
		}
		if len(items) < limit {
			if kind == "record" {
				record, err := decodeStoredRecord(typ, raw)
				if err != nil {
					return SemanticHistory{}, fmt.Errorf("decode Blackboard v2 history record: %w", err)
				}
				items = append(items, HistoryItem{Kind: kind, Key: identityKey, Version: version, Type: typ, Record: &record})
			} else {
				items = append(items, HistoryItem{Kind: kind, Version: version, From: from, Relation: relation, To: to, Reason: reason})
			}
		}
	}
	if err := rows.Err(); err != nil {
		return SemanticHistory{}, fmt.Errorf("iterate Blackboard v2 history: %w", err)
	}
	next := ""
	if offset+len(items) < total {
		next = makeCursor(revision, canonicalKey, limit, offset+len(items))
	}
	return SemanticHistory{Schema: historySchema, Revision: revision, Key: canonicalKey, Items: items, NextCursor: next}, nil
}

func countSemanticHistoryItems(ctx context.Context, tx *sql.Tx, projectID, key string) (int, error) {
	var total int
	err := tx.QueryRowContext(ctx, `
		SELECT
			(SELECT COUNT(*) FROM blackboard_v2_record_history WHERE project_id = ? AND (
				key = ? OR key IN (SELECT source_key FROM blackboard_v2_key_redirects WHERE project_id = ? AND canonical_key = ?)
			)) +
			(SELECT COUNT(*) FROM blackboard_v2_relationship_history WHERE project_id = ? AND (
				from_key = ? OR to_key = ? OR
				from_key IN (SELECT source_key FROM blackboard_v2_key_redirects WHERE project_id = ? AND canonical_key = ?) OR
				to_key IN (SELECT source_key FROM blackboard_v2_key_redirects WHERE project_id = ? AND canonical_key = ?)
			))`,
		projectID, key, projectID, key,
		projectID, key, key, projectID, key, projectID, key,
	).Scan(&total)
	if err != nil {
		return 0, fmt.Errorf("count Blackboard v2 history: %w", err)
	}
	return total, nil
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
		WHERE project_id = ? AND type IN ('entity', 'objective', 'attempt', 'fact', 'finding', 'solution', 'evidence')
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
	findings := make(map[string]SnapshotFinding)
	solutions := make(map[string]SnapshotSolution)
	evidence := make(map[string]SnapshotEvidence)
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
		case "finding":
			finding := record.findingOutputRecord()
			findings[key] = SnapshotFinding{
				Version: version, Status: finding.Status, Title: finding.Title,
				Target: finding.Target, Description: finding.Description,
				Severity: finding.Severity, CVSSPending: finding.CVSSPending,
			}
		case "solution":
			solution := record.solutionRecord()
			solutions[key] = SnapshotSolution{Version: version, Status: solution.Status, Kind: solution.Kind, Summary: solution.Summary, Value: solution.Value, VerificationSummary: solution.VerificationSummary}
		case "evidence":
			item := record.evidenceRecord()
			evidence[key] = SnapshotEvidence{
				Version:      version,
				Status:       item.Status,
				ArtifactType: item.ArtifactType,
				Summary:      item.Summary,
				MediaType:    item.MediaType,
				CapturedAt:   item.CapturedAt,
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
	if len(findings) != 0 {
		knowledge.Findings = findings
	}
	if len(solutions) != 0 {
		knowledge.Solutions = solutions
	}
	if len(evidence) != 0 {
		knowledge.Evidence = evidence
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
	case "finding":
		return applyCreateFinding(ctx, tx, projectID, revision, index, change, now)
	case "solution":
		return applyCreateSolution(ctx, tx, projectID, revision, index, change, now)
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
	case "finding":
		return applyUpdateFinding(ctx, tx, projectID, revision, index, change, now)
	case "solution":
		return applyUpdateSolution(ctx, tx, projectID, revision, index, change, now)
	case "evidence":
		return applyUpdateEvidence(ctx, tx, projectID, revision, index, change, now)
	default:
		return revision, "", 0, false, semanticError("semantic_validation", "unsupported Blackboard v2 record type in this slice", fmt.Sprintf("changes[%d].type", index), nil)
	}
}

func applyUpdateEvidence(ctx context.Context, tx *sql.Tx, projectID string, revision, index int, change Change, now string) (int, string, int, bool, error) {
	path := fmt.Sprintf("changes[%d]", index)
	existing, err := currentRecordForUpdate(ctx, tx, projectID, change, "evidence", path)
	if err != nil {
		return revision, "", 0, false, err
	}
	patch, err := partialEvidenceRecord(change.Record, path+".record")
	if err != nil {
		return revision, "", 0, false, err
	}
	next := existing.record.evidenceRecord()
	if patch.Summary != nil {
		next.Summary = *patch.Summary
	}
	if patch.MediaType != nil {
		next.MediaType = *patch.MediaType
	}
	if patch.CapturedAt != nil {
		next.CapturedAt = *patch.CapturedAt
	}
	for _, field := range change.Clear {
		switch field {
		case "media_type":
			next.MediaType = ""
		case "captured_at":
			next.CapturedAt = ""
		default:
			return revision, "", 0, false, semanticError("semantic_validation", "unsupported Evidence clear field", path+".clear", map[string]any{"field": field})
		}
	}
	if err := validateEvidenceRecord(next, path+".record"); err != nil {
		return revision, "", 0, false, err
	}
	if evidenceEqual(existing.record.evidenceRecord(), next) {
		return revision, change.Key, existing.version, false, nil
	}
	return replaceCurrentWorkRecord(ctx, tx, projectID, revision, existing, next, now)
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
	if err := validateRelationshipReason(change.Relation, change.Reason, path+".reason"); err != nil {
		return revision, RelationVersionTuple{}, false, err
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
	rule, _ := blackboardv2grammar.Lookup(change.Relation)
	if rule.CyclePolicy == "project_fact_to_project_fact_acyclic" && fromRecord.typ == "fact" && toRecord.typ == "fact" {
		wouldCycle, err := factSupportsWouldCycle(ctx, tx, projectID, change.From, change.To)
		if err != nil {
			return revision, RelationVersionTuple{}, false, err
		}
		if wouldCycle {
			return revision, RelationVersionTuple{}, false, semanticError("semantic_validation", "Fact supports relationships must be acyclic", path+".to", nil)
		}
	}
	if isOneOf(rule.CyclePolicy, "acyclic_per_endpoint_family", "acyclic") {
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
		if change.Version == 0 {
			return revision, RelationVersionTuple{}, false, semanticError("semantic_validation", "current relationship version is required when reason changes", path+".version", nil)
		}
		if change.Version != existingVersion {
			return revision, RelationVersionTuple{}, false, semanticError("version_conflict", "relationship changed", path+".version", map[string]any{
				"from": change.From, "relation": change.Relation, "to": change.To, "expected_version": float64(change.Version), "current_version": float64(existingVersion), "next_action": "read_current_record",
			})
		}
		nextRevision, err := incrementRevision(ctx, tx, projectID, revision)
		if err != nil {
			return revision, RelationVersionTuple{}, false, err
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO blackboard_v2_relationship_history (project_id, from_key, relation, to_key, version, reason, recorded_at)
			VALUES (?, ?, ?, ?, ?, ?, ?)`,
			projectID, change.From, change.Relation, change.To, existingVersion, existingReason, now,
		); err != nil {
			return revision, RelationVersionTuple{}, false, fmt.Errorf("store prior Blackboard v2 relationship: %w", err)
		}
		nextVersion := existingVersion + 1
		if _, err := tx.ExecContext(ctx, `
			UPDATE blackboard_v2_relationships
			SET version = ?, reason = ?, updated_at = ?
			WHERE project_id = ? AND from_key = ? AND relation = ? AND to_key = ?`,
			nextVersion, change.Reason, now, projectID, change.From, change.Relation, change.To,
		); err != nil {
			return revision, RelationVersionTuple{}, false, fmt.Errorf("update Blackboard v2 relationship reason: %w", err)
		}
		return nextRevision, RelationVersionTuple{change.From, change.Relation, change.To, nextVersion}, true, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return revision, RelationVersionTuple{}, false, fmt.Errorf("read Blackboard v2 relationship: %w", err)
	}
	maxVersion, err := maxRelationshipVersion(ctx, tx, projectID, change.From, change.Relation, change.To)
	if err != nil {
		return revision, RelationVersionTuple{}, false, err
	}
	if change.Version != 0 {
		if maxVersion != 0 {
			return revision, RelationVersionTuple{}, false, semanticError("version_conflict", "relationship changed", path+".version", map[string]any{
				"from": change.From, "relation": change.Relation, "to": change.To, "expected_version": float64(change.Version), "current_version": float64(maxVersion), "current_state": "removed", "next_action": "read_current_record",
			})
		}
		return revision, RelationVersionTuple{}, false, semanticError("semantic_validation", "relationship version is not accepted for a new relation", path+".version", nil)
	}
	nextVersion := maxVersion + 1
	nextRevision, err := incrementRevision(ctx, tx, projectID, revision)
	if err != nil {
		return revision, RelationVersionTuple{}, false, err
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO blackboard_v2_relationships (project_id, from_key, relation, to_key, version, reason, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		projectID, change.From, change.Relation, change.To, nextVersion, change.Reason, now, now,
	); err != nil {
		return revision, RelationVersionTuple{}, false, fmt.Errorf("store Blackboard v2 relationship: %w", err)
	}
	return nextRevision, RelationVersionTuple{change.From, change.Relation, change.To, nextVersion}, true, nil
}

func maxRelationshipVersion(ctx context.Context, tx *sql.Tx, projectID, from, relation, to string) (int, error) {
	var version int
	if err := tx.QueryRowContext(ctx, `
		SELECT COALESCE(MAX(version), 0)
		FROM (
			SELECT version FROM blackboard_v2_relationships WHERE project_id=? AND from_key=? AND relation=? AND to_key=?
			UNION ALL
			SELECT version FROM blackboard_v2_relationship_history WHERE project_id=? AND from_key=? AND relation=? AND to_key=?
		)`, projectID, from, relation, to, projectID, from, relation, to).Scan(&version); err != nil {
		return 0, fmt.Errorf("read Blackboard v2 relationship identity version: %w", err)
	}
	return version, nil
}

func validateRelationshipReason(relation, reason, path string) error {
	violation := blackboardv2grammar.ReasonViolation(relation, reason)
	if violation == "" {
		return nil
	}
	details := map[string]any{"relation": relation, "violation": violation}
	switch violation {
	case blackboardv2grammar.ReasonViolationForbidden:
		return semanticError("semantic_validation", "relationship reason is not allowed for this relation", path, details)
	case blackboardv2grammar.ReasonViolationRedundant:
		return semanticError("semantic_validation", "relationship reason must add semantic information beyond the relation type", path, details)
	default:
		return semanticError("semantic_validation", "concise semantic text is required and must fit the v2 limit", path, details)
	}
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
	case "finding":
		if change.Status == "false_positive" {
			if err := validateConciseText(change.ResolutionSummary, path+".resolution_summary"); err != nil {
				return revision, "", 0, false, err
			}
			hasMeaning, err := hasCurrentFindingInvalidationMeaning(ctx, tx, projectID, change.Key)
			if err != nil {
				return revision, "", 0, false, err
			}
			if !hasMeaning {
				return revision, "", 0, false, semanticError("semantic_validation", "false-positive Finding requires a current contradicting Fact that preserves reusable invalidation meaning", path+".status", nil)
			}
			terminal := existing.record.findingOutputRecord()
			terminal.Status = "false_positive"
			terminal.ResolutionSummary = change.ResolutionSummary
			return terminalizeRecord(ctx, tx, projectID, revision, existing, terminal, now)
		}
		if change.Status != "confirmed" {
			return revision, "", 0, false, semanticError("semantic_validation", "Finding transition status must be confirmed or false_positive", path+".status", nil)
		}
		current := existing.record.findingOutputRecord()
		if current.Status == "confirmed" {
			return revision, change.Key, existing.version, false, nil
		}
		if current.Status != "unconfirmed" {
			return revision, "", 0, false, semanticError("semantic_validation", "only an unconfirmed Finding can be confirmed", path+".status", nil)
		}
		next := current
		next.Status = "confirmed"
		if err := validateFindingOutputRecord(next, path+".status"); err != nil {
			return revision, "", 0, false, err
		}
		return replaceCurrentWorkRecord(ctx, tx, projectID, revision, existing, next, now)
	case "solution":
		return applySolutionTransition(ctx, tx, projectID, revision, path, existing, change, now)
	case "evidence":
		if change.Status != "missing" {
			return revision, "", 0, false, semanticError("semantic_validation", "Evidence lifecycle transition must be missing", path+".status", nil)
		}
		if err := validateSemanticText(change.Summary, path+".summary"); err != nil {
			return revision, "", 0, false, err
		}
		current := existing.record.evidenceRecord()
		next := current
		next.Status = "missing"
		next.Summary = change.Summary
		if current.Status == next.Status && current.Summary == next.Summary {
			return revision, change.Key, existing.version, false, nil
		}
		return replaceCurrentWorkRecord(ctx, tx, projectID, revision, existing, next, now)
	default:
		return revision, "", 0, false, semanticError("semantic_validation", "record type does not support this transition", path+".key", map[string]any{"key": change.Key, "type": existing.typ})
	}
}

func (s *Service) runtimeFactConfirmationHasBasis(ctx context.Context, tx *sql.Tx, projectID, continuationID, factKey string) (bool, error) {
	rows, err := tx.QueryContext(ctx, `
		SELECT source.record_json
		FROM blackboard_v2_relationships AS rel
		JOIN blackboard_v2_records AS source
		  ON source.project_id = rel.project_id AND source.key = rel.from_key
		WHERE rel.project_id = ? AND rel.relation = 'supports' AND rel.to_key = ? AND source.type = 'fact'
		ORDER BY source.key ASC`,
		projectID, factKey,
	)
	if err != nil {
		return false, fmt.Errorf("read supporting Facts for confirmation: %w", err)
	}
	for rows.Next() {
		var raw string
		if err := rows.Scan(&raw); err != nil {
			rows.Close()
			return false, fmt.Errorf("scan supporting Fact for confirmation: %w", err)
		}
		var source FactRecord
		if err := json.Unmarshal([]byte(raw), &source); err != nil {
			rows.Close()
			return false, fmt.Errorf("decode supporting Fact for confirmation: %w", err)
		}
		if source.Confidence == "confirmed" {
			rows.Close()
			return true, nil
		}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return false, fmt.Errorf("iterate supporting Facts for confirmation: %w", err)
	}
	if err := rows.Close(); err != nil {
		return false, fmt.Errorf("close supporting Facts for confirmation: %w", err)
	}

	rows, err = tx.QueryContext(ctx, `
		SELECT source.record_json
		FROM blackboard_v2_relationships AS rel
		JOIN blackboard_v2_records AS source
		  ON source.project_id = rel.project_id AND source.key = rel.from_key
		WHERE rel.project_id = ? AND rel.relation = 'evidences' AND rel.to_key = ? AND source.type = 'evidence'
		ORDER BY source.key ASC`,
		projectID, factKey,
	)
	if err != nil {
		return false, fmt.Errorf("read Evidence for confirmation: %w", err)
	}
	for rows.Next() {
		var raw string
		if err := rows.Scan(&raw); err != nil {
			rows.Close()
			return false, fmt.Errorf("scan Evidence for confirmation: %w", err)
		}
		var source EvidenceRecord
		if err := json.Unmarshal([]byte(raw), &source); err != nil {
			rows.Close()
			return false, fmt.Errorf("decode Evidence for confirmation: %w", err)
		}
		if source.Status == "available" {
			valid, err := s.evidenceIntegrityValid(projectID, source)
			if err != nil {
				rows.Close()
				return false, err
			}
			if valid {
				rows.Close()
				return true, nil
			}
		}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return false, fmt.Errorf("iterate Evidence for confirmation: %w", err)
	}
	if err := rows.Close(); err != nil {
		return false, fmt.Errorf("close Evidence for confirmation: %w", err)
	}

	rows, err = tx.QueryContext(ctx, `
		SELECT attempt.record_json, origin.continuation_id
		FROM blackboard_v2_relationship_history AS rel
		JOIN blackboard_v2_attempt_origins AS origin
		  ON origin.project_id = rel.project_id AND origin.key = rel.from_key
		JOIN blackboard_v2_record_history AS attempt
		  ON attempt.project_id = rel.project_id AND attempt.key = rel.from_key AND attempt.type = 'attempt'
		WHERE rel.project_id = ? AND rel.relation = 'produced' AND rel.to_key = ?
		ORDER BY rel.from_key ASC, attempt.version ASC`,
		projectID, factKey,
	)
	if err != nil {
		return false, fmt.Errorf("read producing Attempts for confirmation: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var raw, originContinuationID string
		if err := rows.Scan(&raw, &originContinuationID); err != nil {
			return false, fmt.Errorf("scan producing Attempt for confirmation: %w", err)
		}
		var attempt AttemptRecord
		if err := json.Unmarshal([]byte(raw), &attempt); err != nil {
			return false, fmt.Errorf("decode producing Attempt for confirmation: %w", err)
		}
		if originContinuationID == continuationID && attempt.Status == "succeeded" {
			return true, nil
		}
	}
	if err := rows.Err(); err != nil {
		return false, fmt.Errorf("iterate producing Attempts for confirmation: %w", err)
	}
	return false, nil
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

func historicalOutgoingRelationshipCount(ctx context.Context, tx *sql.Tx, projectID, fromKey, relation string) (int, error) {
	var count int
	if err := tx.QueryRowContext(ctx, `
		SELECT COUNT(*)
		FROM blackboard_v2_relationship_history
		WHERE project_id = ? AND from_key = ? AND relation = ?`,
		projectID, fromKey, relation,
	).Scan(&count); err != nil {
		return 0, fmt.Errorf("count historical %s relationships: %w", relation, err)
	}
	return count, nil
}

func validateFinalTerminalAttempts(ctx context.Context, tx *sql.Tx, projectID string, attempts map[string]terminalAttemptValidation) error {
	keys := make([]string, 0, len(attempts))
	for key := range attempts {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		validation := attempts[key]
		currentTests, err := currentOutgoingRelationshipCount(ctx, tx, projectID, key, "tests")
		if err != nil {
			return err
		}
		historicalTests, err := historicalOutgoingRelationshipCount(ctx, tx, projectID, key, "tests")
		if err != nil {
			return err
		}
		if currentTests+historicalTests == 0 {
			return semanticError("semantic_validation", "terminal Attempt requires at least one tested target", validation.path, map[string]any{"key": key})
		}
		if validation.status != "succeeded" {
			continue
		}
		hasReusableOutcome, err := succeededAttemptHasReusableProducedOutcome(ctx, tx, projectID, key)
		if err != nil {
			return err
		}
		if !hasReusableOutcome {
			return semanticError("semantic_validation", "succeeded Attempt requires a current produced outcome or current same-type replacement", validation.path, map[string]any{"key": key})
		}
	}
	return nil
}

func succeededAttemptHasReusableProducedOutcome(ctx context.Context, tx *sql.Tx, projectID, attemptKey string) (bool, error) {
	rows, err := tx.QueryContext(ctx, `
		SELECT to_key
		FROM blackboard_v2_relationships
		WHERE project_id = ? AND from_key = ? AND relation = 'produced'
		UNION
		SELECT to_key
		FROM blackboard_v2_relationship_history
		WHERE project_id = ? AND from_key = ? AND relation = 'produced'
		ORDER BY to_key ASC`,
		projectID, attemptKey, projectID, attemptKey,
	)
	if err != nil {
		return false, fmt.Errorf("read succeeded Attempt produced outcomes: %w", err)
	}
	defer rows.Close()
	targets := make([]string, 0)
	for rows.Next() {
		var target string
		if err := rows.Scan(&target); err != nil {
			return false, fmt.Errorf("scan succeeded Attempt produced outcome: %w", err)
		}
		targets = append(targets, target)
	}
	if err := rows.Err(); err != nil {
		return false, fmt.Errorf("iterate succeeded Attempt produced outcomes: %w", err)
	}
	if len(targets) == 0 {
		return false, nil
	}
	for _, target := range targets {
		if _, err := loadCurrentRecord(ctx, tx, projectID, target); err == nil {
			return true, nil
		} else if !errors.Is(err, sql.ErrNoRows) {
			return false, err
		}
		var replacementExists int
		if err := tx.QueryRowContext(ctx, `
			SELECT EXISTS(
				SELECT 1
				FROM blackboard_v2_relationship_history AS supersession
				JOIN blackboard_v2_records AS replacement
				  ON replacement.project_id = supersession.project_id AND replacement.key = supersession.from_key
				JOIN blackboard_v2_record_history AS replaced
				  ON replaced.project_id = supersession.project_id AND replaced.key = supersession.to_key
				WHERE supersession.project_id = ? AND supersession.relation = 'supersedes' AND supersession.to_key = ?
				  AND replacement.type = replaced.type
			)`,
			projectID, target,
		).Scan(&replacementExists); err != nil {
			return false, fmt.Errorf("check produced outcome replacement: %w", err)
		}
		if replacementExists != 0 {
			return true, nil
		}
	}
	return false, nil
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
	if err := moveCurrentRelationshipsToHistory(ctx, tx, projectID, existing.key, now); err != nil {
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
	supersedesRule, _ := blackboardv2grammar.Lookup("supersedes")
	if !supersedesRule.Allows(replacement.typ, replaced.typ) {
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
	case "evidence":
		record := replaced.record.evidenceRecord()
		record.Status = "superseded"
		terminal = record
	case "fact":
		record := replaced.record.factRecord()
		record.Confidence = "deprecated"
		terminal = record
	case "finding":
		record := replaced.record.findingOutputRecord()
		record.Status = "superseded"
		terminal = record
	case "solution":
		record := replaced.record.solutionRecord()
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
	rule, ok := blackboardv2grammar.Lookup(relation)
	if !ok {
		return semanticError("semantic_validation", "unsupported Blackboard v2 relationship type", path, map[string]any{"relation": relation})
	}
	if relation == "supersedes" {
		return semanticError("semantic_validation", "supersedes is created only by the supersede operation", path, nil)
	}
	if rule.Allows(fromType, toType) {
		return nil
	}
	return semanticError("semantic_validation", rule.EndpointError, path, map[string]any{"relation": relation, "from_type": fromType, "to_type": toType})
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

func factSupportsWouldCycle(ctx context.Context, tx *sql.Tx, projectID, fromKey, toKey string) (bool, error) {
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
			SELECT rel.to_key
			FROM blackboard_v2_relationships AS rel
			JOIN blackboard_v2_records AS source
			  ON source.project_id = rel.project_id AND source.key = rel.from_key AND source.type = 'fact'
			JOIN blackboard_v2_records AS target
			  ON target.project_id = rel.project_id AND target.key = rel.to_key AND target.type = 'fact'
			WHERE rel.project_id = ? AND rel.from_key = ? AND rel.relation = 'supports'
			ORDER BY rel.to_key ASC`,
			projectID, key,
		)
		if err != nil {
			return false, fmt.Errorf("read Fact supports relationships: %w", err)
		}
		for rows.Next() {
			var target string
			if err := rows.Scan(&target); err != nil {
				rows.Close()
				return false, fmt.Errorf("scan Fact supports relationship: %w", err)
			}
			stack = append(stack, target)
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return false, fmt.Errorf("iterate Fact supports relationships: %w", err)
		}
		if err := rows.Close(); err != nil {
			return false, fmt.Errorf("close Fact supports relationships: %w", err)
		}
	}
	return false, nil
}

func moveCurrentRelationshipsToHistory(ctx context.Context, tx *sql.Tx, projectID, key, recordedAt string) error {
	rows, err := tx.QueryContext(ctx, `
		SELECT from_key, relation, to_key, version, reason
		FROM blackboard_v2_relationships
		WHERE project_id = ? AND (from_key = ? OR to_key = ?)
		ORDER BY from_key ASC, relation ASC, to_key ASC`,
		projectID, key, key,
	)
	if err != nil {
		return fmt.Errorf("read terminal Entity relationships: %w", err)
	}
	type relationship struct {
		from, relation, to, reason string
		version                    int
	}
	relationships := make([]relationship, 0)
	for rows.Next() {
		var item relationship
		if err := rows.Scan(&item.from, &item.relation, &item.to, &item.version, &item.reason); err != nil {
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
			projectID, item.from, item.relation, item.to, item.version, item.reason, recordedAt,
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
	canonicalKey, _, err := resolveKeyRedirect(ctx, tx, projectID, key)
	if err != nil {
		return storedRecord{}, err
	}
	return loadCurrentRecordDirect(ctx, tx, projectID, canonicalKey)
}

func loadCurrentRecordDirect(ctx context.Context, tx *sql.Tx, projectID, key string) (storedRecord, error) {
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

func resolveKeyRedirect(ctx context.Context, tx *sql.Tx, projectID, key string) (string, bool, error) {
	var canonical string
	err := tx.QueryRowContext(ctx, `
		SELECT canonical_key
		FROM blackboard_v2_key_redirects
		WHERE project_id = ? AND source_key = ?`,
		projectID, key,
	).Scan(&canonical)
	if errors.Is(err, sql.ErrNoRows) {
		return key, false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("resolve Blackboard v2 key redirect: %w", err)
	}
	var chained int
	if err := tx.QueryRowContext(ctx, `
		SELECT EXISTS(
			SELECT 1 FROM blackboard_v2_key_redirects
			WHERE project_id = ? AND source_key = ?
		)`, projectID, canonical,
	).Scan(&chained); err != nil {
		return "", false, fmt.Errorf("validate Blackboard v2 key redirect: %w", err)
	}
	if chained != 0 {
		return "", false, fmt.Errorf("invalid Blackboard v2 key redirect chain for %q", key)
	}
	return canonical, true, nil
}

func resolveChangeRedirects(ctx context.Context, tx *sql.Tx, projectID string, change Change) (Change, error) {
	resolve := func(key string) (string, error) {
		canonical, _, err := resolveKeyRedirect(ctx, tx, projectID, key)
		return canonical, err
	}
	var err error
	switch change.Op {
	case "update", "transition":
		change.Key, err = resolve(change.Key)
	case "relate", "unrelate":
		if change.From, err = resolve(change.From); err == nil {
			change.To, err = resolve(change.To)
		}
	case "supersede":
		if change.Replacement, err = resolve(change.Replacement); err == nil {
			change.Replaced, err = resolve(change.Replaced)
		}
	}
	return change, err
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
	case "finding":
		var record findingOutputRecord
		if err := json.Unmarshal([]byte(raw), &record); err != nil {
			return Record{}, err
		}
		return recordFromFindingOutput(record), nil
	case "solution":
		var record SolutionRecord
		if err := json.Unmarshal([]byte(raw), &record); err != nil {
			return Record{}, err
		}
		return recordFromSolution(record), nil
	case "evidence":
		var record EvidenceRecord
		if err := json.Unmarshal([]byte(raw), &record); err != nil {
			return Record{}, err
		}
		return recordFromEvidence(record), nil
	default:
		return Record{}, fmt.Errorf("unsupported Blackboard v2 record type %q", typ)
	}
}

func loadCurrentRelationshipsForKey(ctx context.Context, tx *sql.Tx, projectID, key string) ([]RelationshipTuple, error) {
	all, err := loadAllCurrentRelationships(ctx, tx, projectID)
	if err != nil {
		return nil, err
	}
	relationships := make([]RelationshipTuple, 0)
	for _, relationship := range all {
		if relationship[0] == key || relationship[2] == key {
			relationships = append(relationships, relationship)
		}
	}
	return relationships, nil
}

func loadAllCurrentRelationships(ctx context.Context, tx *sql.Tx, projectID string) ([]RelationshipTuple, error) {
	rows, err := tx.QueryContext(ctx, `
		SELECT rel.from_key, rel.relation, rel.to_key, rel.reason, source.type, target.type
		FROM blackboard_v2_relationships AS rel
		LEFT JOIN blackboard_v2_records AS source
		  ON source.project_id=rel.project_id AND source.key=rel.from_key
		LEFT JOIN blackboard_v2_records AS target
		  ON target.project_id=rel.project_id AND target.key=rel.to_key
		WHERE rel.project_id = ?
		ORDER BY rel.from_key ASC, rel.relation ASC, rel.to_key ASC, rel.reason ASC`,
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
	stored := make([]persistedRelationship, 0)
	for rows.Next() {
		var from, relation, to, reason string
		var fromType, toType sql.NullString
		if err := rows.Scan(&from, &relation, &to, &reason, &fromType, &toType); err != nil {
			return nil, fmt.Errorf("scan Blackboard v2 relationship: %w", err)
		}
		rule, known := blackboardv2grammar.Lookup(relation)
		if !known || relation == "supersedes" {
			return nil, persistedRelationshipError(from, relation, to, "relation")
		}
		if from == to {
			return nil, persistedRelationshipError(from, relation, to, "self_link")
		}
		if !fromType.Valid || !toType.Valid || !rule.Allows(fromType.String, toType.String) {
			return nil, persistedRelationshipError(from, relation, to, "endpoint")
		}
		if violation := blackboardv2grammar.ReasonViolation(relation, reason); violation != "" {
			return nil, persistedRelationshipError(from, relation, to, violation)
		}
		stored = append(stored, persistedRelationship{from: from, relation: relation, to: to, fromType: fromType.String, toType: toType.String})
		if reason == "" {
			relationships = append(relationships, RelationshipTuple{from, relation, to})
		} else {
			relationships = append(relationships, RelationshipTuple{from, relation, to, reason})
		}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate Blackboard v2 relationships: %w", err)
	}
	if relation, cyclic := persistedRelationshipCycle(stored); cyclic {
		return nil, persistedRelationshipError("", relation, "", "cycle")
	}
	return relationships, nil
}

type persistedRelationship struct {
	from, relation, to string
	fromType, toType   string
}

func persistedRelationshipError(from, relation, to, violation string) error {
	details := map[string]any{"relation": relation, "violation": violation}
	if from != "" {
		details["from"] = from
	}
	if to != "" {
		details["to"] = to
	}
	return semanticError("semantic_validation", "persisted relationship is outside Blackboard v2 relationship grammar", "relations", details)
}

func persistedRelationshipCycle(relationships []persistedRelationship) (string, bool) {
	for _, rule := range blackboardv2grammar.Rules() {
		if !isOneOf(rule.CyclePolicy, "acyclic_per_endpoint_family", "acyclic", "project_fact_to_project_fact_acyclic") {
			continue
		}
		adjacency := make(map[string][]string)
		nodes := make(map[string]bool)
		for _, relationship := range relationships {
			if relationship.relation != rule.Relation {
				continue
			}
			if rule.CyclePolicy == "project_fact_to_project_fact_acyclic" && (relationship.fromType != "fact" || relationship.toType != "fact") {
				continue
			}
			adjacency[relationship.from] = append(adjacency[relationship.from], relationship.to)
			nodes[relationship.from], nodes[relationship.to] = true, true
		}
		keys := make([]string, 0, len(nodes))
		for key := range nodes {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for key := range adjacency {
			sort.Strings(adjacency[key])
		}
		state := make(map[string]uint8, len(nodes))
		var visit func(string) bool
		visit = func(key string) bool {
			if state[key] == 1 {
				return true
			}
			if state[key] == 2 {
				return false
			}
			state[key] = 1
			for _, target := range adjacency[key] {
				if visit(target) {
					return true
				}
			}
			state[key] = 2
			return false
		}
		for _, key := range keys {
			if visit(key) {
				return rule.Relation, true
			}
		}
	}
	return "", false
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
	case "relate", "unrelate":
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
		if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
			return Change{}, fmt.Errorf("relationship version must be a positive integer")
		}
		if err := json.Unmarshal(raw, &version); err != nil {
			return Change{}, fmt.Errorf("decode relation version: %w", err)
		}
		if version < 1 {
			return Change{}, fmt.Errorf("relationship version must be a positive integer")
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

func decodeUnrelateChange(fields map[string]json.RawMessage) (Change, error) {
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
		return Change{}, fmt.Errorf("unsupported mutable relationship type %q", relation)
	}
	var version int
	if raw, ok := fields["version"]; !ok {
		return Change{}, fmt.Errorf("unrelate version is required")
	} else if err := json.Unmarshal(raw, &version); err != nil {
		return Change{}, fmt.Errorf("decode unrelate version: %w", err)
	}
	if version < 1 {
		return Change{}, fmt.Errorf("unrelate version must be a positive integer")
	}
	return Change{Op: "unrelate", From: from, Relation: relation, To: to, Version: version}, nil
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
	case "verified", "rejected":
		if _, ok := fields["summary"]; ok {
			return Change{}, fmt.Errorf("transition summary is not allowed for status %s", status)
		}
		if _, ok := fields["resolution_summary"]; ok {
			return Change{}, fmt.Errorf("transition resolution_summary is not allowed for status %s", status)
		}
		verificationSummary, err := decodeRequiredString(fields, "verification_summary")
		if err != nil {
			return Change{}, err
		}
		change.VerificationSummary = verificationSummary
	case "resolved", "abandoned", "retired", "false_positive":
		if _, ok := fields["summary"]; ok {
			return Change{}, fmt.Errorf("transition summary is not allowed for status %s", status)
		}
		if _, ok := fields["verification_summary"]; ok {
			return Change{}, fmt.Errorf("transition verification_summary is not allowed for status %s", status)
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
		if _, ok := fields["verification_summary"]; ok {
			return Change{}, fmt.Errorf("transition verification_summary is not allowed for status %s", status)
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
		if _, ok := fields["verification_summary"]; ok {
			return Change{}, fmt.Errorf("transition verification_summary is not allowed for status %s", status)
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

func decodeMergeChange(fields map[string]json.RawMessage) (Change, error) {
	source, err := decodeRequiredString(fields, "source")
	if err != nil {
		return Change{}, err
	}
	canonical, err := decodeRequiredString(fields, "canonical")
	if err != nil {
		return Change{}, err
	}
	var sourceVersion, canonicalVersion int
	if raw, ok := fields["source_version"]; !ok {
		return Change{}, fmt.Errorf("merge source_version is required")
	} else if err := json.Unmarshal(raw, &sourceVersion); err != nil {
		return Change{}, fmt.Errorf("decode merge source_version: %w", err)
	}
	if raw, ok := fields["canonical_version"]; !ok {
		return Change{}, fmt.Errorf("merge canonical_version is required")
	} else if err := json.Unmarshal(raw, &canonicalVersion); err != nil {
		return Change{}, fmt.Errorf("decode merge canonical_version: %w", err)
	}
	var canonicalRecord any
	if raw, ok := fields["canonical_record"]; ok {
		canonicalRecord = json.RawMessage(append([]byte(nil), raw...))
	}
	var clear []string
	if raw, ok := fields["clear"]; ok {
		if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
			return Change{}, fmt.Errorf("merge clear must be an array")
		}
		if err := json.Unmarshal(raw, &clear); err != nil {
			return Change{}, fmt.Errorf("decode merge clear: %w", err)
		}
		seen := make(map[string]bool, len(clear))
		for _, field := range clear {
			if seen[field] {
				return Change{}, fmt.Errorf("merge clear fields must be unique")
			}
			seen[field] = true
		}
	}
	return Change{Op: "merge", Source: source, SourceVersion: sourceVersion, Canonical: canonical, CanonicalVersion: canonicalVersion, CanonicalRecord: canonicalRecord, Clear: clear}, nil
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
	case "finding":
		return decodeFindingRecord(raw)
	case "solution":
		return decodeSolutionRecord(raw)
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
	case "finding":
		return decodeFindingPatch(raw)
	case "solution":
		return decodeSolutionPatch(raw)
	case "evidence":
		return decodeEvidencePatch(raw)
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

func decodeFindingRecord(raw json.RawMessage) (FindingRecord, error) {
	var record FindingRecord
	if err := strictDecodeJSON(raw, &record); err != nil {
		return FindingRecord{}, fmt.Errorf("decode Finding record: %w", err)
	}
	return record, nil
}

func decodeFindingPatch(raw json.RawMessage) (FindingPatch, error) {
	var patch FindingPatch
	if err := strictDecodeJSON(raw, &patch); err != nil {
		return FindingPatch{}, fmt.Errorf("decode Finding patch: %w", err)
	}
	if findingPatchEmpty(patch) {
		return FindingPatch{}, fmt.Errorf("Finding partial record requires at least one property")
	}
	return patch, nil
}

func decodeEvidencePatch(raw json.RawMessage) (EvidencePatch, error) {
	var patch EvidencePatch
	if err := strictDecodeJSON(raw, &patch); err != nil {
		return EvidencePatch{}, fmt.Errorf("decode Evidence patch: %w", err)
	}
	if evidencePatchEmpty(patch) {
		return EvidencePatch{}, fmt.Errorf("Evidence partial record requires at least one property")
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
	case "unrelate":
		allowedFields = map[string]bool{"version": true, "from": true, "relation": true, "to": true}
	case "transition":
		allowedFields = map[string]bool{"key": true, "version": true, "status": true, "summary": true, "resolution_summary": true, "verification_summary": true}
	case "supersede":
		allowedFields = map[string]bool{"replacement": true, "replacement_version": true, "replaced": true, "replaced_version": true}
	case "merge":
		allowedFields = map[string]bool{"source": true, "source_version": true, "canonical": true, "canonical_version": true, "canonical_record": true, "clear": true}
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
		} else if isReasonRelation(change.Relation) {
			if change.Version < 0 {
				return semanticError("semantic_validation", "relationship version must be positive when provided", path+".version", nil)
			}
			if change.Version != 0 && change.Reason == "" {
				return semanticError("semantic_validation", "relationship reason is required when version is provided", path+".reason", nil)
			}
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
	case "unrelate":
		if change.From == "" {
			return semanticError("semantic_validation", "unrelate from is required", fmt.Sprintf("changes[%d].from", index), nil)
		}
		if change.Relation == "" {
			return semanticError("semantic_validation", "unrelate relation is required", fmt.Sprintf("changes[%d].relation", index), nil)
		}
		if change.To == "" {
			return semanticError("semantic_validation", "unrelate to is required", fmt.Sprintf("changes[%d].to", index), nil)
		}
		if change.Version < 1 {
			return semanticError("semantic_validation", "unrelate requires the current relationship version", fmt.Sprintf("changes[%d].version", index), nil)
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
			if change.Summary != "" || change.VerificationSummary != "" {
				return semanticError("semantic_validation", "transition accepts only resolution_summary for this status", fmt.Sprintf("changes[%d].status", index), nil)
			}
		case "succeeded", "failed", "blocked", "inconclusive", "interrupted", "deprecated", "missing":
			if change.ResolutionSummary != "" || change.VerificationSummary != "" {
				return semanticError("semantic_validation", "transition accepts only summary for this status", fmt.Sprintf("changes[%d].status", index), nil)
			}
		case "verified", "rejected":
			if change.Summary != "" || change.ResolutionSummary != "" {
				return semanticError("semantic_validation", "Solution transition accepts only verification_summary", fmt.Sprintf("changes[%d].status", index), nil)
			}
			if change.VerificationSummary == "" {
				return semanticError("semantic_validation", "Solution transition requires verification_summary", fmt.Sprintf("changes[%d].verification_summary", index), nil)
			}
		default:
			if change.Summary != "" || change.ResolutionSummary != "" || change.VerificationSummary != "" {
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
	case "merge":
		if change.Source == "" {
			return semanticError("semantic_validation", "merge source is required", fmt.Sprintf("changes[%d].source", index), nil)
		}
		if change.SourceVersion < 1 {
			return semanticError("semantic_validation", "merge requires the current source version", fmt.Sprintf("changes[%d].source_version", index), nil)
		}
		if change.Canonical == "" {
			return semanticError("semantic_validation", "merge canonical is required", fmt.Sprintf("changes[%d].canonical", index), nil)
		}
		if change.CanonicalVersion < 1 {
			return semanticError("semantic_validation", "merge requires the current canonical version", fmt.Sprintf("changes[%d].canonical_version", index), nil)
		}
	}
	return nil
}

func validateChangeDTOShape(change Change, index int) error {
	if err := validateChangeShape(change, index); err != nil {
		return err
	}
	path := fmt.Sprintf("changes[%d].record", index)
	if change.Op == "create" {
		switch change.Type {
		case "entity":
			_, err := completeEntityRecord(change.Record, path)
			return err
		case "objective":
			_, err := completeObjectiveRecord(change.Record, path)
			return err
		case "attempt":
			_, err := completeAttemptRecord(change.Record, path)
			return err
		case "fact":
			_, err := completeFactRecord(change.Record, path)
			return err
		case "finding":
			_, err := completeFindingRecord(change.Record, path)
			return err
		case "solution":
			_, err := completeSolutionRecord(change.Record, path)
			return err
		}
	}
	if change.Op == "update" {
		switch change.Type {
		case "entity":
			_, err := partialEntityRecord(change.Record, path)
			return err
		case "objective":
			_, err := partialObjectiveRecord(change.Record, path)
			return err
		case "attempt":
			_, err := partialAttemptRecord(change.Record, path)
			return err
		case "fact":
			_, err := partialFactRecord(change.Record, path)
			return err
		case "finding":
			_, err := partialFindingRecord(change.Record, path)
			return err
		case "solution":
			_, err := partialSolutionRecord(change.Record, path)
			return err
		case "evidence":
			_, err := partialEvidenceRecord(change.Record, path)
			return err
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
		{name: "verification_summary", populated: change.VerificationSummary != ""},
		{name: "replacement", populated: change.Replacement != ""},
		{name: "replacement_version", populated: change.ReplacementVersion != 0},
		{name: "replaced", populated: change.Replaced != ""},
		{name: "replaced_version", populated: change.ReplacedVersion != 0},
		{name: "source", populated: change.Source != ""},
		{name: "source_version", populated: change.SourceVersion != 0},
		{name: "canonical", populated: change.Canonical != ""},
		{name: "canonical_version", populated: change.CanonicalVersion != 0},
		{name: "canonical_record", populated: change.CanonicalRecord != nil},
	}
}

func isOrdinaryRelation(relation string) bool {
	rule, ok := blackboardv2grammar.Lookup(relation)
	return ok && relation != "supersedes" && rule.ReasonPolicy == blackboardv2grammar.ReasonForbidden
}

func isReasonRelation(relation string) bool {
	rule, ok := blackboardv2grammar.Lookup(relation)
	return ok && rule.ReasonPolicy == blackboardv2grammar.ReasonOptional
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

func completeFindingRecord(value any, path string) (FindingRecord, error) {
	switch record := value.(type) {
	case FindingRecord:
		return record, nil
	case *FindingRecord:
		if record == nil {
			return FindingRecord{}, semanticError("semantic_validation", "Finding record is required", path, nil)
		}
		return *record, nil
	case json.RawMessage:
		decoded, err := decodeFindingRecord(record)
		if err != nil {
			return FindingRecord{}, semanticError("semantic_validation", err.Error(), path, nil)
		}
		return decoded, nil
	default:
		return FindingRecord{}, semanticError("semantic_validation", "Finding create requires a complete closed Finding record", path, nil)
	}
}

func partialFindingRecord(value any, path string) (FindingPatch, error) {
	switch patch := value.(type) {
	case FindingPatch:
		if findingPatchEmpty(patch) {
			return FindingPatch{}, semanticError("semantic_validation", "Finding partial record requires at least one property", path, nil)
		}
		return patch, nil
	case *FindingPatch:
		if patch == nil || findingPatchEmpty(*patch) {
			return FindingPatch{}, semanticError("semantic_validation", "Finding update requires a non-empty Finding partial record", path, nil)
		}
		return *patch, nil
	case json.RawMessage:
		decoded, err := decodeFindingPatch(patch)
		if err != nil {
			return FindingPatch{}, semanticError("semantic_validation", err.Error(), path, nil)
		}
		return decoded, nil
	default:
		return FindingPatch{}, semanticError("semantic_validation", "Finding update requires a closed Finding partial record", path, nil)
	}
}

func partialEvidenceRecord(value any, path string) (EvidencePatch, error) {
	switch patch := value.(type) {
	case EvidencePatch:
		if evidencePatchEmpty(patch) {
			return EvidencePatch{}, semanticError("semantic_validation", "Evidence partial record requires at least one property", path, nil)
		}
		if err := validateEvidencePatch(patch, path); err != nil {
			return EvidencePatch{}, err
		}
		return patch, nil
	case *EvidencePatch:
		if patch == nil || evidencePatchEmpty(*patch) {
			return EvidencePatch{}, semanticError("semantic_validation", "Evidence update requires a non-empty Evidence partial record", path, nil)
		}
		if err := validateEvidencePatch(*patch, path); err != nil {
			return EvidencePatch{}, err
		}
		return *patch, nil
	case json.RawMessage:
		decoded, err := decodeEvidencePatch(patch)
		if err != nil {
			return EvidencePatch{}, semanticError("semantic_validation", err.Error(), path, nil)
		}
		if err := validateEvidencePatch(decoded, path); err != nil {
			return EvidencePatch{}, err
		}
		return decoded, nil
	default:
		return EvidencePatch{}, semanticError("semantic_validation", "Evidence update requires an Evidence partial record", path, nil)
	}
}

func validateEvidencePatch(patch EvidencePatch, path string) error {
	if patch.Summary != nil {
		if err := validateSemanticText(*patch.Summary, path+".summary"); err != nil {
			return err
		}
	}
	if patch.MediaType != nil {
		if err := validateConciseText(*patch.MediaType, path+".media_type"); err != nil {
			return err
		}
	}
	if patch.CapturedAt != nil {
		if *patch.CapturedAt == "" {
			return semanticError("semantic_validation", "captured_at must be cleared explicitly", path+".captured_at", nil)
		}
		if _, err := time.Parse(time.RFC3339, *patch.CapturedAt); err != nil {
			return semanticError("semantic_validation", "captured_at must be an RFC3339 timestamp", path+".captured_at", nil)
		}
	}
	return nil
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

func findingPatchEmpty(patch FindingPatch) bool {
	return patch.Title == nil && patch.Target == nil && patch.Description == nil && patch.Proof == nil &&
		patch.Impact == nil && patch.Recommendation == nil && patch.CVSSVersion == nil && patch.CVSSVector == nil
}

func evidencePatchEmpty(patch EvidencePatch) bool {
	return patch.Summary == nil && patch.MediaType == nil && patch.CapturedAt == nil
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

func validateEvidenceRecord(record EvidenceRecord, path string) error {
	if record.Status != "available" && record.Status != "missing" {
		return semanticError("semantic_validation", "Evidence status must be available or missing", path+".status", nil)
	}
	if err := validateConciseText(record.ArtifactType, path+".artifact_type"); err != nil {
		return err
	}
	if err := validateSemanticText(record.Summary, path+".summary"); err != nil {
		return err
	}
	if record.MediaType != "" {
		if err := validateConciseText(record.MediaType, path+".media_type"); err != nil {
			return err
		}
	}
	if record.ManagedPath == "" || len(record.SHA256) != 64 || record.Size < 0 {
		return semanticError("semantic_validation", "Evidence integrity details are invalid", path, nil)
	}
	if _, err := hex.DecodeString(record.SHA256); err != nil {
		return semanticError("semantic_validation", "Evidence sha256 must be lowercase hexadecimal", path+".sha256", nil)
	}
	if record.SHA256 != strings.ToLower(record.SHA256) {
		return semanticError("semantic_validation", "Evidence sha256 must be lowercase hexadecimal", path+".sha256", nil)
	}
	if record.CapturedAt != "" {
		if _, err := time.Parse(time.RFC3339, record.CapturedAt); err != nil {
			return semanticError("semantic_validation", "Evidence captured_at must be an RFC3339 timestamp", path+".captured_at", nil)
		}
	}
	return nil
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

func evidenceEqual(a, b EvidenceRecord) bool {
	return a.Status == b.Status && a.ArtifactType == b.ArtifactType && a.Summary == b.Summary &&
		a.MediaType == b.MediaType && a.SourcePath == b.SourcePath && a.ManagedPath == b.ManagedPath &&
		a.SHA256 == b.SHA256 && a.Size == b.Size && a.CapturedAt == b.CapturedAt
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

func recordFromFindingOutput(record findingOutputRecord) Record {
	return Record{
		Status: record.Status, ResolutionSummary: record.ResolutionSummary, Title: record.Title, Target: record.Target, Description: record.Description,
		Proof: record.Proof, Impact: record.Impact, Recommendation: record.Recommendation,
		CVSSVersion: record.CVSSVersion, CVSSVector: record.CVSSVector,
		Severity: record.Severity, CVSSPending: record.CVSSPending,
	}
}

func recordFromEvidence(record EvidenceRecord) Record {
	return Record{
		Status:       record.Status,
		ArtifactType: record.ArtifactType,
		Summary:      record.Summary,
		MediaType:    record.MediaType,
		SourcePath:   record.SourcePath,
		ManagedPath:  record.ManagedPath,
		SHA256:       record.SHA256,
		Size:         record.Size,
		CapturedAt:   record.CapturedAt,
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

func (record Record) findingOutputRecord() findingOutputRecord {
	return findingOutputRecord{
		Status: record.Status, ResolutionSummary: record.ResolutionSummary, Title: record.Title, Target: record.Target, Description: record.Description,
		Proof: record.Proof, Impact: record.Impact, Recommendation: record.Recommendation,
		CVSSVersion: record.CVSSVersion, CVSSVector: record.CVSSVector,
		Severity: record.Severity, CVSSPending: record.CVSSPending,
	}
}

func (record Record) evidenceRecord() EvidenceRecord {
	return EvidenceRecord{
		Status:       record.Status,
		ArtifactType: record.ArtifactType,
		Summary:      record.Summary,
		MediaType:    record.MediaType,
		SourcePath:   record.SourcePath,
		ManagedPath:  record.ManagedPath,
		SHA256:       record.SHA256,
		Size:         record.Size,
		CapturedAt:   record.CapturedAt,
	}
}

type historyCursor struct {
	revision int
	key      string
	limit    int
	offset   int
	present  bool
}

type historyCursorPayload struct {
	Schema   string `json:"schema"`
	Revision int    `json:"revision"`
	Key      string `json:"key"`
	Limit    int    `json:"limit"`
	Offset   int    `json:"offset"`
}

func parseCursor(cursor string) (historyCursor, error) {
	if cursor == "" {
		return historyCursor{}, nil
	}
	if !strings.HasPrefix(cursor, "opaque:") {
		return historyCursor{}, semanticError("semantic_validation", "history cursor is invalid", "cursor", nil)
	}
	payload, err := base64.RawURLEncoding.Strict().DecodeString(strings.TrimPrefix(cursor, "opaque:"))
	if err != nil {
		return historyCursor{}, invalidHistoryCursorError("malformed")
	}
	var decoded historyCursorPayload
	if err := strictDecodeJSON(payload, &decoded); err != nil {
		return historyCursor{}, invalidHistoryCursorError("malformed")
	}
	if decoded.Schema != "semantic-history-cursor/v2" || decoded.Revision < 0 || decoded.Limit < 1 || decoded.Limit > 100 || decoded.Offset < 1 {
		return historyCursor{}, invalidHistoryCursorError("malformed")
	}
	if err := validateKey(decoded.Key, "cursor"); err != nil {
		return historyCursor{}, invalidHistoryCursorError("malformed")
	}
	return historyCursor{revision: decoded.Revision, key: decoded.Key, limit: decoded.Limit, offset: decoded.Offset, present: true}, nil
}

func makeCursor(revision int, key string, limit, offset int) string {
	payload, err := json.Marshal(historyCursorPayload{Schema: "semantic-history-cursor/v2", Revision: revision, Key: key, Limit: limit, Offset: offset})
	if err != nil {
		panic(fmt.Sprintf("encode Semantic History cursor: %v", err))
	}
	return "opaque:" + base64.RawURLEncoding.EncodeToString(payload)
}

func invalidHistoryCursorError(reason string) error {
	return semanticError("semantic_validation", "history cursor is invalid", "cursor", map[string]any{"reason": reason, "next_action": "restart_history_read"})
}

func semanticError(code, message, path string, details map[string]any) *Error {
	return &Error{Code: code, Message: message, Path: path, Retryable: false, Details: details}
}
