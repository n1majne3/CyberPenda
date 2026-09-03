// Package workinggraph owns the filesystem Working Graph boundary and durable
// Runtime-to-Harness intent mailbox. Blackboard compilation and settlement are
// implemented behind the same module in later slices.
package workinggraph

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"

	"pentest/internal/owner"
	"pentest/internal/store"
)

const (
	IntentSchema  = "working-graph-intent/v1"
	MaxIntentSize = 1 << 20
)

type OwnerKind string

const (
	OwnerKindTask    OwnerKind = "task"
	OwnerKindSession OwnerKind = "session"
)

type IntentKind string

const (
	IntentSemanticChanges IntentKind = "semantic_changes"
	IntentAttemptResult   IntentKind = "attempt_result"
	IntentCheckpoint      IntentKind = "checkpoint_attempt"
	IntentRetainEvidence  IntentKind = "retain_evidence"
)

type OwnerContext struct {
	Owner          owner.Contract
	ContinuationID string
	Workdir        string
}

type Projection struct {
	Root     string `json:"root"`
	State    string `json:"state"`
	Facts    string `json:"facts"`
	Data     string `json:"data"`
	Steps    string `json:"steps"`
	Goals    string `json:"goals"`
	Outbox   string `json:"outbox"`
	Receipts string `json:"receipts"`
}

type ApplyFunc func(context.Context, owner.Contract, string, Intent) (ApplyOutcome, error)

type SettlementRequest struct {
	Owner          owner.Contract
	ContinuationID string
	Projection     Projection
	Apply          ApplyFunc
}

type ApplyOutcome struct {
	State  IntentState    `json:"state"`
	Result map[string]any `json:"result,omitempty"`
	Error  map[string]any `json:"error,omitempty"`
}

type Receipt struct {
	Schema         string         `json:"schema"`
	IntentID       string         `json:"intent_id"`
	ContinuationID string         `json:"continuation_id"`
	State          IntentState    `json:"state"`
	Result         map[string]any `json:"result,omitempty"`
	Error          map[string]any `json:"error,omitempty"`
	UpdatedAt      string         `json:"updated_at"`
}

type SettlementResult struct {
	Receipts []Receipt `json:"receipts"`
	Blocked  bool      `json:"blocked"`
}

type Status struct {
	Pending        int `json:"pending"`
	RetryPending   int `json:"retry_pending"`
	ActionRequired int `json:"action_required"`
}

type WorkingGraph interface {
	Prepare(context.Context, OwnerContext) (Projection, error)
	Settle(context.Context, SettlementRequest) (SettlementResult, error)
	Status(context.Context, owner.Contract) (Status, error)
}

type Service struct{ db *store.DB }

func NewService(databases ...*store.DB) *Service {
	service := &Service{}
	if len(databases) > 0 {
		service.db = databases[0]
	}
	return service
}

func (s *Service) Prepare(_ context.Context, request OwnerContext) (Projection, error) {
	if err := request.Owner.Validate(); err != nil {
		return Projection{}, fmt.Errorf("validate Working Graph owner: %w", err)
	}
	if strings.TrimSpace(request.ContinuationID) == "" {
		return Projection{}, errors.New("Working Graph continuation id is required")
	}
	root, err := filepath.Abs(strings.TrimSpace(request.Workdir))
	if err != nil || root == "" {
		return Projection{}, errors.New("Working Graph workdir is invalid")
	}
	if err := ensureDirectoryTree(root); err != nil {
		return Projection{}, err
	}
	graph := filepath.Join(root, "graph")
	projection := Projection{
		Root: root, State: filepath.Join(root, "state.md"),
		Facts: filepath.Join(graph, "facts"), Data: filepath.Join(graph, "data"),
		Steps: filepath.Join(graph, "steps.yaml"), Goals: filepath.Join(graph, "goals.yaml"),
		Outbox:   filepath.Join(graph, "outbox", request.ContinuationID),
		Receipts: filepath.Join(graph, "receipts", request.ContinuationID),
	}
	for _, directory := range []string{graph, projection.Facts, projection.Data, filepath.Dir(projection.Outbox), projection.Outbox, filepath.Dir(projection.Receipts), projection.Receipts} {
		if err := ensureDirectory(directory); err != nil {
			return Projection{}, err
		}
	}
	for path, body := range map[string][]byte{
		projection.State: []byte("# Working Graph state\n"),
		projection.Steps: []byte("steps: []\n"),
		projection.Goals: []byte("goals: []\n"),
	} {
		if err := writeFileIfAbsent(path, body); err != nil {
			return Projection{}, err
		}
	}
	return projection, nil
}

// Status returns owner-scoped unsettled intent counts from the durable Store.
func (s *Service) Status(ctx context.Context, contract owner.Contract) (Status, error) {
	if s.db == nil {
		return Status{}, errors.New("Working Graph Store is unavailable")
	}
	if err := contract.Validate(); err != nil {
		return Status{}, fmt.Errorf("validate Working Graph owner: %w", err)
	}
	rows, err := s.db.QueryContext(ctx, `SELECT state,COUNT(*) FROM working_graph_intents
		WHERE owner_kind=? AND owner_id=? AND state IN (?,?,?) GROUP BY state`,
		OwnerKind(contract.Kind), contract.ID,
		IntentStatePending, IntentStateRetryPending, IntentStateActionRequired,
	)
	if err != nil {
		return Status{}, fmt.Errorf("read Working Graph status: %w", err)
	}
	defer rows.Close()
	var status Status
	for rows.Next() {
		var state IntentState
		var count int
		if err := rows.Scan(&state, &count); err != nil {
			return Status{}, fmt.Errorf("scan Working Graph status: %w", err)
		}
		switch state {
		case IntentStatePending:
			status.Pending = count
		case IntentStateRetryPending:
			status.RetryPending = count
		case IntentStateActionRequired:
			status.ActionRequired = count
		}
	}
	if err := rows.Err(); err != nil {
		return Status{}, fmt.Errorf("iterate Working Graph status: %w", err)
	}
	return status, nil
}

type IntentInput struct {
	Kind        IntentKind     `json:"kind"`
	SourceFacts []string       `json:"source_facts,omitempty"`
	Replaces    string         `json:"replaces,omitempty"`
	Payload     map[string]any `json:"payload"`
}

type Intent struct {
	Schema      string         `json:"schema"`
	ID          string         `json:"id"`
	Kind        IntentKind     `json:"kind"`
	SourceFacts []string       `json:"source_facts,omitempty"`
	Replaces    string         `json:"replaces,omitempty"`
	Payload     map[string]any `json:"payload"`
}

type IntentState string

const (
	IntentStatePending        IntentState = "pending"
	IntentStateApplying       IntentState = "applying"
	IntentStateRetryPending   IntentState = "retry_pending"
	IntentStateApplied        IntentState = "applied"
	IntentStateNoop           IntentState = "noop"
	IntentStateActionRequired IntentState = "action_required"
	IntentStateSuperseded     IntentState = "superseded"
)

type IntentRecord struct {
	ID             string
	OwnerKind      OwnerKind
	OwnerID        string
	ContinuationID string
	IntentID       string
	Sequence       int
	RequestHash    string
	Kind           IntentKind
	State          IntentState
	ReplacementOf  string
	SourceFacts    []string
	Result         map[string]any
	Error          map[string]any
	RetryCount     int
	CreatedAt      string
	UpdatedAt      string
}

const ReceiptSchema = "working-graph-receipt/v1"

var factIDPattern = regexp.MustCompile(`^fact_[0-9]+$`)
var intentIDPattern = regexp.MustCompile(`^intent_[0-9]{8}$`)

// Claim stores one Runtime intent as the authoritative settlement record.
// Replaying the same owner/intention bytes returns the existing record;
// reusing an intent id with different bytes fails closed.
func (s *Service) Claim(ctx context.Context, contract owner.Contract, continuationID string, sequence int, intent Intent) (IntentRecord, bool, error) {
	if s.db == nil {
		return IntentRecord{}, false, errors.New("Working Graph Store is unavailable")
	}
	if err := contract.Validate(); err != nil {
		return IntentRecord{}, false, err
	}
	continuationID = strings.TrimSpace(continuationID)
	if continuationID == "" || sequence < 1 {
		return IntentRecord{}, false, errors.New("Working Graph claim requires continuation and positive sequence")
	}
	ownerKind := OwnerKind(contract.Kind)
	if intent.Schema != IntentSchema || !intentIDPattern.MatchString(intent.ID) {
		return IntentRecord{}, false, errors.New("Working Graph intent envelope is invalid")
	}
	if err := validateIntentInput(ownerKind, IntentInput{Kind: intent.Kind, SourceFacts: intent.SourceFacts, Replaces: intent.Replaces, Payload: intent.Payload}); err != nil {
		return IntentRecord{}, false, err
	}
	raw, err := json.Marshal(intent)
	if err != nil {
		return IntentRecord{}, false, fmt.Errorf("encode Working Graph claim: %w", err)
	}
	digest := sha256.Sum256(raw)
	requestHash := hex.EncodeToString(digest[:])

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return IntentRecord{}, false, fmt.Errorf("begin Working Graph claim: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	existing, err := queryIntentRecord(ctx, tx, ownerKind, contract.ID, intent.ID)
	if err == nil {
		if existing.RequestHash != requestHash {
			return IntentRecord{}, false, errors.New("Working Graph intent id was reused with different content")
		}
		return existing, false, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return IntentRecord{}, false, fmt.Errorf("read Working Graph claim: %w", err)
	}
	sourceFacts, err := json.Marshal(intent.SourceFacts)
	if err != nil {
		return IntentRecord{}, false, err
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	record := IntentRecord{
		ID:        fmt.Sprintf("working-graph-%s-%s-%s", ownerKind, contract.ID, intent.ID),
		OwnerKind: ownerKind, OwnerID: contract.ID, ContinuationID: continuationID,
		IntentID: intent.ID, Sequence: sequence, RequestHash: requestHash, Kind: intent.Kind,
		State: IntentStatePending, ReplacementOf: intent.Replaces, SourceFacts: append([]string(nil), intent.SourceFacts...),
		CreatedAt: now, UpdatedAt: now,
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO working_graph_intents
		(id,owner_kind,owner_id,continuation_id,intent_id,sequence,request_hash,kind,state,replacement_of,source_facts_json,result_json,error_json,retry_count,created_at,updated_at)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		record.ID, record.OwnerKind, record.OwnerID, record.ContinuationID, record.IntentID, record.Sequence,
		record.RequestHash, record.Kind, record.State, record.ReplacementOf, string(sourceFacts), "{}", "{}", 0, now, now,
	); err != nil {
		return IntentRecord{}, false, fmt.Errorf("insert Working Graph claim: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return IntentRecord{}, false, fmt.Errorf("commit Working Graph claim: %w", err)
	}
	return record, true, nil
}

type rowQueryer interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func queryIntentRecord(ctx context.Context, queryer rowQueryer, ownerKind OwnerKind, ownerID, intentID string) (IntentRecord, error) {
	var record IntentRecord
	var sourceFactsJSON, resultJSON, errorJSON string
	err := queryer.QueryRowContext(ctx, `SELECT id,owner_kind,owner_id,continuation_id,intent_id,sequence,request_hash,kind,state,replacement_of,source_facts_json,result_json,error_json,retry_count,created_at,updated_at
		FROM working_graph_intents WHERE owner_kind=? AND owner_id=? AND intent_id=?`, ownerKind, ownerID, intentID).Scan(
		&record.ID, &record.OwnerKind, &record.OwnerID, &record.ContinuationID, &record.IntentID, &record.Sequence,
		&record.RequestHash, &record.Kind, &record.State, &record.ReplacementOf, &sourceFactsJSON, &resultJSON, &errorJSON,
		&record.RetryCount, &record.CreatedAt, &record.UpdatedAt,
	)
	if err != nil {
		return IntentRecord{}, err
	}
	if err := json.Unmarshal([]byte(sourceFactsJSON), &record.SourceFacts); err != nil {
		return IntentRecord{}, fmt.Errorf("decode Working Graph source facts: %w", err)
	}
	if err := json.Unmarshal([]byte(resultJSON), &record.Result); err != nil {
		return IntentRecord{}, fmt.Errorf("decode Working Graph result: %w", err)
	}
	if err := json.Unmarshal([]byte(errorJSON), &record.Error); err != nil {
		return IntentRecord{}, fmt.Errorf("decode Working Graph error: %w", err)
	}
	return record, nil
}

type scannedIntent struct {
	sequence int
	intent   Intent
}

// Settle consumes only the current Continuation mailbox. It claims and applies
// intents in sequence order, writes one durable receipt per claimed intent,
// and stops before later work when operator action is required.
func (s *Service) Settle(ctx context.Context, request SettlementRequest) (SettlementResult, error) {
	if s.db == nil {
		return SettlementResult{}, errors.New("Working Graph Store is unavailable")
	}
	if err := validateSettlementRequest(request); err != nil {
		return SettlementResult{}, err
	}
	intents, err := scanOutbox(request.Projection.Outbox, OwnerKind(request.Owner.Kind))
	if err != nil {
		return SettlementResult{}, err
	}
	result := SettlementResult{Receipts: make([]Receipt, 0, len(intents))}
	for _, item := range intents {
		record, _, claimErr := s.Claim(ctx, request.Owner, request.ContinuationID, item.sequence, item.intent)
		if claimErr != nil {
			return result, claimErr
		}
		if isTerminalIntentState(record.State) {
			receipt := receiptFromRecord(record)
			if err := writeReceipt(request.Projection.Receipts, receipt); err != nil {
				return result, err
			}
			result.Receipts = append(result.Receipts, receipt)
			if record.State == IntentStateActionRequired {
				result.Blocked = true
				break
			}
			continue
		}
		if err := s.markApplying(ctx, record); err != nil {
			return result, err
		}
		outcome, applyErr := request.Apply(ctx, request.Owner, request.ContinuationID, item.intent)
		if applyErr != nil {
			outcome = ApplyOutcome{State: IntentStateRetryPending, Error: map[string]any{"message": applyErr.Error()}}
		}
		if err := validateApplyOutcome(outcome); err != nil {
			outcome = ApplyOutcome{State: IntentStateActionRequired, Error: map[string]any{"code": "invalid_apply_outcome", "message": err.Error()}}
		}
		record, err = s.completeApply(ctx, record, outcome)
		if err != nil {
			return result, err
		}
		receipt := receiptFromRecord(record)
		if err := writeReceipt(request.Projection.Receipts, receipt); err != nil {
			return result, err
		}
		result.Receipts = append(result.Receipts, receipt)
		if record.State == IntentStateActionRequired {
			result.Blocked = true
			break
		}
		if applyErr != nil {
			return result, fmt.Errorf("apply Working Graph intent %s: %w", item.intent.ID, applyErr)
		}
	}
	return result, nil
}

func validateSettlementRequest(request SettlementRequest) error {
	if err := request.Owner.Validate(); err != nil {
		return fmt.Errorf("validate Working Graph settlement owner: %w", err)
	}
	if strings.TrimSpace(request.ContinuationID) == "" || request.Apply == nil {
		return errors.New("Working Graph settlement requires continuation and applier")
	}
	root, err := filepath.Abs(strings.TrimSpace(request.Owner.Workdir))
	if err != nil || root == "" {
		return errors.New("Working Graph settlement workdir is invalid")
	}
	expectedOutbox := filepath.Join(root, "graph", "outbox", request.ContinuationID)
	expectedReceipts := filepath.Join(root, "graph", "receipts", request.ContinuationID)
	actualOutbox, outboxErr := filepath.Abs(request.Projection.Outbox)
	actualReceipts, receiptsErr := filepath.Abs(request.Projection.Receipts)
	if outboxErr != nil || receiptsErr != nil || actualOutbox != expectedOutbox || actualReceipts != expectedReceipts {
		return errors.New("Working Graph settlement projection does not match its owner and Continuation")
	}
	if err := ensureDirectory(actualOutbox); err != nil {
		return err
	}
	return ensureDirectory(actualReceipts)
}

func scanOutbox(outbox string, ownerKind OwnerKind) ([]scannedIntent, error) {
	entries, err := os.ReadDir(outbox)
	if err != nil {
		return nil, fmt.Errorf("read Working Graph outbox: %w", err)
	}
	intents := make([]scannedIntent, 0, len(entries))
	for _, entry := range entries {
		name := entry.Name()
		if name == ".sequence" || name == ".sequence.lock" || strings.HasSuffix(name, ".tmp") {
			continue
		}
		if entry.Type()&os.ModeSymlink != 0 || !entry.Type().IsRegular() {
			return nil, fmt.Errorf("Working Graph outbox entry is not a regular file: %s", name)
		}
		if !strings.HasSuffix(name, ".json") {
			return nil, fmt.Errorf("unexpected Working Graph outbox entry: %s", name)
		}
		intentID := strings.TrimSuffix(name, ".json")
		if !intentIDPattern.MatchString(intentID) {
			return nil, fmt.Errorf("invalid Working Graph intent filename: %s", name)
		}
		info, err := entry.Info()
		if err != nil {
			return nil, err
		}
		if info.Size() > MaxIntentSize {
			return nil, fmt.Errorf("Working Graph intent exceeds %d bytes: %s", MaxIntentSize, name)
		}
		raw, err := os.ReadFile(filepath.Join(outbox, name))
		if err != nil {
			return nil, err
		}
		decoder := json.NewDecoder(bytes.NewReader(raw))
		decoder.DisallowUnknownFields()
		var intent Intent
		if err := decoder.Decode(&intent); err != nil {
			return nil, fmt.Errorf("decode Working Graph intent %s: %w", name, err)
		}
		if intent.ID != intentID || intent.Schema != IntentSchema {
			return nil, fmt.Errorf("Working Graph intent envelope does not match filename: %s", name)
		}
		if err := validateIntentInput(ownerKind, IntentInput{Kind: intent.Kind, SourceFacts: intent.SourceFacts, Replaces: intent.Replaces, Payload: intent.Payload}); err != nil {
			return nil, fmt.Errorf("validate Working Graph intent %s: %w", name, err)
		}
		sequence, err := strconv.Atoi(strings.TrimPrefix(intent.ID, "intent_"))
		if err != nil || sequence < 1 {
			return nil, fmt.Errorf("invalid Working Graph intent sequence: %s", name)
		}
		intents = append(intents, scannedIntent{sequence: sequence, intent: intent})
	}
	sort.Slice(intents, func(i, j int) bool { return intents[i].sequence < intents[j].sequence })
	return intents, nil
}

func (s *Service) markApplying(ctx context.Context, record IntentRecord) error {
	now := time.Now().UTC().Format(time.RFC3339Nano)
	result, err := s.db.ExecContext(ctx, `UPDATE working_graph_intents SET state=?,retry_count=retry_count+CASE WHEN state=? THEN 1 ELSE 0 END,updated_at=?
		WHERE id=? AND state IN (?,?,?)`, IntentStateApplying, IntentStateApplying, now, record.ID,
		IntentStatePending, IntentStateRetryPending, IntentStateApplying)
	if err != nil {
		return fmt.Errorf("mark Working Graph intent applying: %w", err)
	}
	changed, err := result.RowsAffected()
	if err != nil || changed != 1 {
		return errors.New("Working Graph intent could not enter applying state")
	}
	return nil
}

func (s *Service) completeApply(ctx context.Context, record IntentRecord, outcome ApplyOutcome) (IntentRecord, error) {
	resultJSON, err := json.Marshal(nonNilMap(outcome.Result))
	if err != nil {
		return IntentRecord{}, err
	}
	errorJSON, err := json.Marshal(nonNilMap(outcome.Error))
	if err != nil {
		return IntentRecord{}, err
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	result, err := s.db.ExecContext(ctx, `UPDATE working_graph_intents SET state=?,result_json=?,error_json=?,updated_at=? WHERE id=? AND state=?`,
		outcome.State, string(resultJSON), string(errorJSON), now, record.ID, IntentStateApplying)
	if err != nil {
		return IntentRecord{}, fmt.Errorf("complete Working Graph intent: %w", err)
	}
	changed, err := result.RowsAffected()
	if err != nil || changed != 1 {
		return IntentRecord{}, errors.New("Working Graph intent completion lost ownership")
	}
	return queryIntentRecord(ctx, s.db, record.OwnerKind, record.OwnerID, record.IntentID)
}

func validateApplyOutcome(outcome ApplyOutcome) error {
	switch outcome.State {
	case IntentStateApplied, IntentStateNoop, IntentStateActionRequired, IntentStateSuperseded, IntentStateRetryPending:
		return nil
	default:
		return fmt.Errorf("unsupported terminal intent state %q", outcome.State)
	}
}

func isTerminalIntentState(state IntentState) bool {
	switch state {
	case IntentStateApplied, IntentStateNoop, IntentStateActionRequired, IntentStateSuperseded:
		return true
	default:
		return false
	}
}

func receiptFromRecord(record IntentRecord) Receipt {
	return Receipt{Schema: ReceiptSchema, IntentID: record.IntentID, ContinuationID: record.ContinuationID,
		State: record.State, Result: nonNilMap(record.Result), Error: nonNilMap(record.Error), UpdatedAt: record.UpdatedAt}
}

func writeReceipt(directory string, receipt Receipt) error {
	raw, err := json.Marshal(receipt)
	if err != nil {
		return err
	}
	return atomicWrite(filepath.Join(directory, receipt.IntentID+".json"), append(raw, '\n'))
}

func nonNilMap(value map[string]any) map[string]any {
	if value == nil {
		return map[string]any{}
	}
	return value
}

func Emit(outbox string, ownerKind OwnerKind, input IntentInput) (Intent, error) {
	if err := validateIntentInput(ownerKind, input); err != nil {
		return Intent{}, err
	}
	outbox, err := filepath.Abs(strings.TrimSpace(outbox))
	if err != nil || outbox == "" {
		return Intent{}, errors.New("Working Graph outbox is invalid")
	}
	if err := ensureDirectory(outbox); err != nil {
		return Intent{}, err
	}
	release, err := acquireSequenceLock(outbox)
	if err != nil {
		return Intent{}, err
	}
	defer release()
	sequence, err := nextSequence(outbox)
	if err != nil {
		return Intent{}, err
	}
	intent := Intent{
		Schema: IntentSchema, ID: fmt.Sprintf("intent_%08d", sequence), Kind: input.Kind,
		SourceFacts: append([]string(nil), input.SourceFacts...), Replaces: input.Replaces, Payload: input.Payload,
	}
	raw, err := json.Marshal(intent)
	if err != nil {
		return Intent{}, fmt.Errorf("encode Working Graph intent: %w", err)
	}
	if len(raw) > MaxIntentSize {
		return Intent{}, fmt.Errorf("Working Graph intent exceeds %d bytes", MaxIntentSize)
	}
	if err := atomicWrite(filepath.Join(outbox, intent.ID+".json"), append(raw, '\n')); err != nil {
		return Intent{}, err
	}
	return intent, nil
}

func validateIntentInput(ownerKind OwnerKind, input IntentInput) error {
	if ownerKind != OwnerKindTask && ownerKind != OwnerKindSession {
		return errors.New("Working Graph owner kind must be task or session")
	}
	switch input.Kind {
	case IntentSemanticChanges, IntentAttemptResult, IntentCheckpoint, IntentRetainEvidence:
	default:
		return fmt.Errorf("unsupported Working Graph intent kind %q", input.Kind)
	}
	if ownerKind == OwnerKindSession && input.Kind == IntentRetainEvidence {
		return errors.New("Session Working Graph does not support retain_evidence")
	}
	if input.Payload == nil {
		return errors.New("Working Graph intent payload is required")
	}
	for _, factID := range input.SourceFacts {
		if !factIDPattern.MatchString(strings.TrimSpace(factID)) {
			return fmt.Errorf("invalid source fact %q", factID)
		}
	}
	if input.Replaces != "" && !intentIDPattern.MatchString(input.Replaces) {
		return fmt.Errorf("invalid replacement intent %q", input.Replaces)
	}
	return nil
}

func ensureDirectory(path string) error {
	info, err := os.Lstat(path)
	if err == nil {
		if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			return fmt.Errorf("Working Graph path is not a regular directory: %s", path)
		}
		return nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := os.Mkdir(path, 0o700); err != nil {
		return fmt.Errorf("create Working Graph directory %s: %w", path, err)
	}
	return nil
}

func ensureDirectoryTree(path string) error {
	path = filepath.Clean(path)
	missing := make([]string, 0)
	for current := path; ; current = filepath.Dir(current) {
		info, err := os.Lstat(current)
		if err == nil {
			if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
				return fmt.Errorf("Working Graph path is not a regular directory: %s", current)
			}
			break
		}
		if !errors.Is(err, os.ErrNotExist) {
			return err
		}
		missing = append(missing, current)
		parent := filepath.Dir(current)
		if parent == current {
			return fmt.Errorf("Working Graph directory has no existing ancestor: %s", path)
		}
	}
	for index := len(missing) - 1; index >= 0; index-- {
		if err := ensureDirectory(missing[index]); err != nil {
			return err
		}
	}
	return nil
}

func writeFileIfAbsent(path string, body []byte) error {
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if errors.Is(err, os.ErrExist) {
		info, inspectErr := os.Lstat(path)
		if inspectErr != nil || info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
			return fmt.Errorf("Working Graph path is not a regular file: %s", path)
		}
		return nil
	}
	if err != nil {
		return err
	}
	defer file.Close()
	if _, err := file.Write(body); err != nil {
		return err
	}
	return file.Sync()
}

func acquireSequenceLock(outbox string) (func(), error) {
	path := filepath.Join(outbox, ".sequence.lock")
	for attempt := 0; attempt < 100; attempt++ {
		file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if err == nil {
			_ = file.Close()
			return func() { _ = os.Remove(path) }, nil
		}
		if !errors.Is(err, os.ErrExist) {
			return nil, err
		}
		time.Sleep(10 * time.Millisecond)
	}
	return nil, errors.New("Working Graph outbox sequence is busy")
}

func nextSequence(outbox string) (int, error) {
	path := filepath.Join(outbox, ".sequence")
	current := 0
	if raw, err := os.ReadFile(path); err == nil {
		parsed, parseErr := strconv.Atoi(strings.TrimSpace(string(raw)))
		if parseErr != nil || parsed < 0 {
			return 0, errors.New("Working Graph outbox sequence is invalid")
		}
		current = parsed
	} else if !errors.Is(err, os.ErrNotExist) {
		return 0, err
	}
	next := current + 1
	if err := atomicWrite(path, []byte(strconv.Itoa(next)+"\n")); err != nil {
		return 0, err
	}
	return next, nil
}

func atomicWrite(target string, body []byte) error {
	temporary := target + "." + strconv.Itoa(os.Getpid()) + ".tmp"
	file, err := os.OpenFile(temporary, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return fmt.Errorf("stage Working Graph file: %w", err)
	}
	cleanup := true
	defer func() {
		_ = file.Close()
		if cleanup {
			_ = os.Remove(temporary)
		}
	}()
	if _, err := file.Write(body); err != nil {
		return err
	}
	if err := file.Sync(); err != nil {
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	if err := os.Rename(temporary, target); err != nil {
		return fmt.Errorf("publish Working Graph file: %w", err)
	}
	cleanup = false
	return syncDirectory(filepath.Dir(target))
}

func syncDirectory(path string) error {
	directory, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("open Working Graph directory for fsync: %w", err)
	}
	defer directory.Close()
	// Windows cannot fsync a directory handle (golang/go#47366). The staged
	// file was synced before rename, so follow the migration backup behavior
	// and skip the unsupported directory durability sync.
	if runtime.GOOS == "windows" {
		return nil
	}
	if err := directory.Sync(); err != nil {
		return fmt.Errorf("fsync Working Graph directory: %w", err)
	}
	return nil
}
