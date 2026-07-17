package blackboardv2

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"pentest/internal/projectinterface"
	"pentest/internal/store"
	"pentest/internal/task"
)

const snapshotEstimatorVersion = "utf8-bytes-div-4/v1"

var ErrLaunchPinIntegrity = errors.New("Launch Blackboard Pin integrity check failed")

type ContinuityFailurePoint string

const (
	ContinuityFailureBeforeCommit                     ContinuityFailurePoint = "before_commit"
	ContinuityFailureBeforeWorkingSnapshotPublication ContinuityFailurePoint = "before_working_snapshot_publication"
	// ContinuityFailureAfterBindGrant runs after BindGrant succeeds and before
	// the launch transaction commits. Injectors use it to prove grant-bearing
	// projection rolls back atomically with durable Continuation state.
	ContinuityFailureAfterBindGrant ContinuityFailurePoint = "after_bind_grant"
)

type ContinuityFailureInjector func(ContinuityFailurePoint) error

type ContinuationLaunchRequest struct {
	ProjectID        string
	TaskID           string
	RuntimeProfileID string
	RuntimeProvider  string
	Runner           task.Runner
	RuntimeConfig    map[string]any
	SteeringEventIDs []string
	// Precommit runs before the launch transaction with staged Snapshot
	// metadata only (no Continuation grant). Codex uses this for full
	// projection; Claude/Pi use it for grant-less layout projection.
	Precommit func(ContinuationLaunchProjection) error
	// BindGrant runs after the Continuation grant plaintext is available
	// (empty when grants are disabled), after the Working Snapshot is
	// published, and still before the launch transaction commits. Failure
	// aborts the launch: durable rows roll back and the prior Working
	// Snapshot is restored. Callers that write grant-bearing config must
	// scrub it before returning an error from BindGrant, or via UnbindGrant
	// when the service aborts after a successful BindGrant.
	BindGrant func(plaintextGrant string) error
	// UnbindGrant is invoked when BindGrant returned nil but the launch still
	// aborts before success (failure injection or commit failure). Optional.
	UnbindGrant func()
}

type ContinuationLaunchProjection struct {
	Schema   string
	Revision int
}

type ContinuationLaunch struct {
	RuntimeConfig task.RuntimeConfigVersion
	Continuation  task.TaskContinuation
	// Token is the one-time opaque Continuation Interface capability bound to
	// this Project/Task/Continuation. Callers must treat it as a secret.
	Token    string
	Snapshot []byte
	Schema   string
	Revision int
}

type LaunchPin struct {
	Schema   string
	Revision int
	Bytes    []byte
}

type WorkingSnapshotState struct {
	LastAcknowledgedRevision int
	Bytes                    []byte
}

// ContinuationSynchronizationState is the authenticated revision state an
// adapter observes before serving one trusted Continuation response.
type ContinuationSynchronizationState struct {
	FromRevision int
	Revision     int
	Pending      bool
}

// SynchronizationAttachment is the common optional trusted-response sibling.
type SynchronizationAttachment struct {
	Reason       string          `json:"reason"`
	FromRevision int             `json:"from_revision"`
	Revision     int             `json:"revision"`
	Snapshot     RuntimeSnapshot `json:"snapshot"`
}

type ActiveSnapshot struct {
	ContinuationID  string
	TaskID          string
	RuntimeProvider string
	Runner          task.Runner
	Schema          string
	Revision        int
}

type ContinuityService struct {
	db          *store.DB
	board       *Service
	tasks       *task.Service
	grants      *projectinterface.GrantStore
	runtimeRoot string
	fail        ContinuityFailureInjector
}

func NewContinuityService(db *store.DB, board *Service, tasks *task.Service, runtimeRoot string) *ContinuityService {
	board.runtimeRoot = runtimeRoot
	return &ContinuityService{
		db: db, board: board, tasks: tasks, runtimeRoot: runtimeRoot,
		grants: projectinterface.NewGrantStore(db, projectinterface.SystemClock{}, projectinterface.RandomIDSource{}, projectinterface.RandomTokenSource{}),
	}
}

func (s *ContinuityService) SetFailureInjector(injector ContinuityFailureInjector) {
	s.fail = injector
}

// ContinuationAuthority is the race-safe Project/Task/Continuation binding an
// offline or transport adapter must establish before service access.
type ContinuationAuthority struct {
	ProjectID      string
	TaskID         string
	ContinuationID string
	Status         string
	// Live is true when the Continuation is open and still owns the Task's
	// current Working Snapshot (active/latest). Closed or superseded
	// Continuations lose offline read, history, and synchronization authority.
	Live bool
	Sync ContinuationSynchronizationState
}

// AuthorizeContinuationBinding validates Project, Task, and Continuation
// identity before any offline Runtime mutation or read. When requireLive is
// true, closed or superseded Continuations are rejected before service access.
func (s *Service) AuthorizeContinuationBinding(ctx context.Context, projectID, taskID, continuationID string, requireLive bool) (ContinuationAuthority, error) {
	if strings.TrimSpace(projectID) == "" || strings.TrimSpace(continuationID) == "" {
		return ContinuationAuthority{}, semanticError("authority_denied", "trusted Continuation identity is required", "", nil)
	}
	var boundTaskID, status string
	var number, acknowledged, revision, newer int
	err := s.db.QueryRowContext(ctx, `
		SELECT continuation.task_id,continuation.status,continuation.number,
		       state.last_acknowledged_revision,COALESCE(project_state.revision,0),
		       (SELECT COUNT(*) FROM task_continuations AS newer
		         WHERE newer.task_id=continuation.task_id AND newer.number>continuation.number)
		FROM task_continuations AS continuation
		JOIN tasks AS task ON task.id=continuation.task_id
		JOIN blackboard_v2_continuation_pins AS pin ON pin.continuation_id=continuation.id
		JOIN blackboard_v2_continuation_state AS state ON state.continuation_id=continuation.id
		LEFT JOIN blackboard_v2_project_state AS project_state ON project_state.project_id=task.project_id
		WHERE continuation.id=? AND task.project_id=?`, continuationID, projectID,
	).Scan(&boundTaskID, &status, &number, &acknowledged, &revision, &newer)
	if errors.Is(err, sql.ErrNoRows) {
		return ContinuationAuthority{}, semanticError("authority_denied", "trusted Continuation does not own this Project interface", "", nil)
	}
	if err != nil {
		return ContinuationAuthority{}, fmt.Errorf("authorize trusted Continuation binding: %w", err)
	}
	if taskID != "" && taskID != boundTaskID {
		return ContinuationAuthority{}, semanticError("authority_denied", "trusted Continuation does not own this Task interface", "", nil)
	}
	live := continuationCanWrite(status) && newer == 0
	if requireLive && !live {
		return ContinuationAuthority{}, semanticError("closed_continuation", "trusted Continuation is closed for offline Blackboard access", "", nil)
	}
	return ContinuationAuthority{
		ProjectID: projectID, TaskID: boundTaskID, ContinuationID: continuationID, Status: status, Live: live,
		Sync: ContinuationSynchronizationState{FromRevision: acknowledged, Revision: revision, Pending: live && revision > acknowledged},
	}, nil
}

// InspectContinuationSynchronization validates the Project, Task, and
// Continuation binding before an adapter decodes or executes semantic input.
// An empty taskID is accepted for adapters whose authenticated principal has
// already bound the Task outside this service call. Closed or superseded
// Continuations retain identity checks but report no pending live sync.
func (s *Service) InspectContinuationSynchronization(ctx context.Context, projectID, taskID, continuationID string) (ContinuationSynchronizationState, error) {
	authority, err := s.AuthorizeContinuationBinding(ctx, projectID, taskID, continuationID, false)
	if err != nil {
		return ContinuationSynchronizationState{}, err
	}
	return authority.Sync, nil
}

// SynchronizeContinuation advances the authenticated Working Snapshot to the
// exact current Runtime Snapshot and returns the common response attachment.
// Delivery is crash/retry safe: the Working Snapshot is published before the
// acknowledgement commits when advancing, and an already-acknowledged retry
// republishes the exact committed Working Snapshot bytes so a lost response or
// interrupted materialization can recover without leaving a live pending notice.
// If publication or commit fails after the filesystem advances, the prior
// Working Snapshot file bytes (or absence) are restored so durable state and
// disk stay aligned for the next retry.
func (s *Service) SynchronizeContinuation(ctx context.Context, projectID, taskID, continuationID string, fromRevision int) (SynchronizationAttachment, error) {
	if fromRevision < 0 {
		return SynchronizationAttachment{}, semanticError("semantic_validation", "synchronization revision must not be negative", "from_revision", nil)
	}
	s.snapshotMu.Lock()
	defer s.snapshotMu.Unlock()
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return SynchronizationAttachment{}, fmt.Errorf("begin trusted Continuation synchronization: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	var boundTaskID string
	var acknowledged int
	var status string
	var newer int
	var workingBytes []byte
	err = tx.QueryRowContext(ctx, `
		SELECT continuation.task_id,continuation.status,state.last_acknowledged_revision,state.working_snapshot_bytes,
		       (SELECT COUNT(*) FROM task_continuations AS newer
		         WHERE newer.task_id=continuation.task_id AND newer.number>continuation.number)
		FROM task_continuations AS continuation
		JOIN tasks AS task ON task.id=continuation.task_id
		JOIN blackboard_v2_continuation_pins AS pin ON pin.continuation_id=continuation.id
		JOIN blackboard_v2_continuation_state AS state ON state.continuation_id=continuation.id
		WHERE continuation.id=? AND task.project_id=?`, continuationID, projectID,
	).Scan(&boundTaskID, &status, &acknowledged, &workingBytes, &newer)
	if errors.Is(err, sql.ErrNoRows) {
		return SynchronizationAttachment{}, semanticError("authority_denied", "trusted Continuation does not own this Project interface", "", nil)
	}
	if err != nil {
		return SynchronizationAttachment{}, fmt.Errorf("validate trusted Continuation synchronization: %w", err)
	}
	if taskID != "" && taskID != boundTaskID {
		return SynchronizationAttachment{}, semanticError("authority_denied", "trusted Continuation does not own this Task interface", "", nil)
	}
	if !continuationCanWrite(status) || newer != 0 {
		return SynchronizationAttachment{}, semanticError("closed_continuation", "trusted Continuation is closed for synchronization", "", nil)
	}
	projection, err := s.ProjectRuntimeSnapshotTx(ctx, tx, projectID)
	if err != nil {
		return SynchronizationAttachment{}, err
	}
	if projection.Snapshot.Revision < acknowledged {
		return SynchronizationAttachment{}, fmt.Errorf("acknowledged Blackboard revision moved beyond current Project state")
	}
	// from_revision is the last acknowledged revision the adapter observed. Prefer
	// the caller's observed from when it still describes this notice; otherwise
	// report the server-side acknowledgement observed in this transaction.
	deliveredFrom := acknowledged
	if fromRevision >= 0 && fromRevision <= acknowledged {
		deliveredFrom = fromRevision
	}
	if projection.Snapshot.Revision > acknowledged {
		result, err := tx.ExecContext(ctx, `
			UPDATE blackboard_v2_continuation_state
			SET last_acknowledged_revision=?,working_snapshot_bytes=?,updated_at=?
			WHERE continuation_id=? AND last_acknowledged_revision=?`,
			projection.Snapshot.Revision, projection.Bytes, time.Now().UTC().Format(time.RFC3339Nano), continuationID, acknowledged,
		)
		if err != nil {
			return SynchronizationAttachment{}, fmt.Errorf("advance synchronized Working Blackboard Snapshot: %w", err)
		}
		if changed, err := result.RowsAffected(); err != nil {
			return SynchronizationAttachment{}, err
		} else if changed != 1 {
			return SynchronizationAttachment{}, fmt.Errorf("stale Working Blackboard Snapshot synchronization")
		}
		workingBytes = append([]byte(nil), projection.Bytes...)
	}
	// Capture prior on-disk Working Snapshot before publication so a failed
	// materialization or commit can restore exact prior bytes (or absence).
	// Publish-before-commit remains crash-safe when the process dies after disk
	// replace: Pending stays true when advancing, and retries republish.
	workingPath := filepath.Join(s.runtimeRoot, boundTaskID, "workdir", ".pentest", "blackboard.json")
	previousBytes, previousErr := os.ReadFile(workingPath)
	previousExists := previousErr == nil
	if previousErr != nil && !errors.Is(previousErr, os.ErrNotExist) {
		return SynchronizationAttachment{}, fmt.Errorf("read prior Working Snapshot before synchronized publication: %w", previousErr)
	}
	restoreWorkingSnapshot := func() {
		restorePriorWorkingSnapshotFile(s.runtimeRoot, boundTaskID, workingPath, previousBytes, previousExists)
	}
	// Publish exact Snapshot bytes before commit so a crash after disk replace
	// and before commit leaves Pending true (when advancing) and retries safely.
	// Already-acknowledged retries republish committed Working Snapshot bytes so
	// lost materialization or a deleted file recovers without reopening Pending.
	if err := materializeWorkingSnapshot(s.runtimeRoot, boundTaskID, workingBytes); err != nil {
		restoreWorkingSnapshot()
		return SynchronizationAttachment{}, fmt.Errorf("publish synchronized Working Blackboard Snapshot: %w", err)
	}
	if err := tx.Commit(); err != nil {
		restoreWorkingSnapshot()
		return SynchronizationAttachment{}, fmt.Errorf("commit trusted Continuation synchronization: %w", err)
	}
	var snapshot RuntimeSnapshot
	if err := json.Unmarshal(workingBytes, &snapshot); err != nil {
		return SynchronizationAttachment{}, fmt.Errorf("decode synchronized Working Blackboard Snapshot: %w", err)
	}
	return SynchronizationAttachment{
		Reason: "another_task_changed_shared_project_knowledge", FromRevision: deliveredFrom,
		Revision: snapshot.Revision, Snapshot: snapshot,
	}, nil
}

// restorePriorWorkingSnapshotFile restores exact prior Working Snapshot file
// state after a failed publish-before-commit attempt. Errors from restore are
// ignored so callers can preserve and return the primary failure.
func restorePriorWorkingSnapshotFile(runtimeRoot, taskID, workingPath string, previousBytes []byte, previousExists bool) {
	if previousExists {
		_ = materializeWorkingSnapshot(runtimeRoot, taskID, previousBytes)
		return
	}
	_ = os.Remove(workingPath)
}

func (s *ContinuityService) CreateContinuation(ctx context.Context, req ContinuationLaunchRequest) (ContinuationLaunch, error) {
	if s.db == nil || s.board == nil || s.tasks == nil {
		return ContinuationLaunch{}, fmt.Errorf("Blackboard v2 Continuation launch is unavailable")
	}
	s.board.snapshotMu.Lock()
	defer s.board.snapshotMu.Unlock()
	s.board.writeMu.Lock()
	defer s.board.writeMu.Unlock()
	var stagedProjection RuntimeSnapshotProjection
	if req.Precommit != nil {
		var err error
		stagedProjection, err = s.board.ProjectRuntimeSnapshot(ctx, req.ProjectID)
		if err != nil {
			return ContinuationLaunch{}, fmt.Errorf("stage current Runtime Blackboard Snapshot: %w", err)
		}
		if err := req.Precommit(ContinuationLaunchProjection{Schema: snapshotSchema, Revision: stagedProjection.Snapshot.Revision}); err != nil {
			return ContinuationLaunch{}, err
		}
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return ContinuationLaunch{}, fmt.Errorf("begin atomic Blackboard v2 Continuation launch: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	projection, err := s.board.ProjectRuntimeSnapshotTx(ctx, tx, req.ProjectID)
	if err != nil {
		return ContinuationLaunch{}, fmt.Errorf("project current Runtime Blackboard Snapshot: %w", err)
	}
	if req.Precommit != nil && !bytes.Equal(stagedProjection.Bytes, projection.Bytes) {
		return ContinuationLaunch{}, fmt.Errorf("current Runtime Blackboard Snapshot changed during launch projection")
	}
	digest := sha256.Sum256(projection.Bytes)
	config, continuation, err := s.tasks.CreateContinuationLaunchTx(ctx, tx, task.ContinuationLaunchRequest{
		ProjectID: req.ProjectID, TaskID: req.TaskID, RuntimeProfileID: req.RuntimeProfileID,
		RuntimeProvider: req.RuntimeProvider, Runner: req.Runner, RuntimeConfig: req.RuntimeConfig,
		SteeringEventIDs: req.SteeringEventIDs,
		SnapshotPin: task.ContinuationSnapshotPin{
			BlackboardGraphRevision:             projection.Snapshot.Revision,
			BlackboardRendererVersion:           snapshotSchema,
			BlackboardEstimatorVersion:          snapshotEstimatorVersion,
			BlackboardProjectionHash:            hex.EncodeToString(digest[:]),
			BlackboardProjectionBytes:           len(projection.Bytes),
			BlackboardProjectionEstimatedTokens: projection.EstimatedTokens,
		},
	})
	if err != nil {
		return ContinuationLaunch{}, err
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO blackboard_v2_continuation_pins
		(continuation_id,snapshot_schema,snapshot_revision,snapshot_bytes,integrity_sha256,created_at)
		VALUES (?,?,?,?,?,?)`,
		continuation.ID, snapshotSchema, projection.Snapshot.Revision, projection.Bytes, hex.EncodeToString(digest[:]), now,
	); err != nil {
		return ContinuationLaunch{}, fmt.Errorf("store immutable Launch Blackboard Pin: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO blackboard_v2_continuation_state
		(continuation_id,last_acknowledged_revision,working_snapshot_bytes,updated_at)
		VALUES (?,?,?,?)`, continuation.ID, projection.Snapshot.Revision, projection.Bytes, now,
	); err != nil {
		return ContinuationLaunch{}, fmt.Errorf("store initial Working Blackboard Snapshot: %w", err)
	}
	token := ""
	if s.grants != nil {
		plaintext, _, grantErr := s.grants.IssueInTx(ctx, tx, projectinterface.IssueGrantRequest{
			ProjectID: req.ProjectID, TaskID: req.TaskID, ContinuationID: continuation.ID,
			RuntimeConfigVersionID: config.ID, RuntimeProfileID: req.RuntimeProfileID,
			RuntimePluginID: req.RuntimeProvider, Runner: string(req.Runner),
		})
		if grantErr != nil {
			return ContinuationLaunch{}, fmt.Errorf("issue Continuation Interface capability: %w", grantErr)
		}
		token = plaintext
	}
	if s.fail != nil {
		if err := s.fail(ContinuityFailureBeforeCommit); err != nil {
			return ContinuationLaunch{}, err
		}
		if err := s.fail(ContinuityFailureBeforeWorkingSnapshotPublication); err != nil {
			return ContinuationLaunch{}, err
		}
	}
	workingPath := filepath.Join(s.runtimeRoot, req.TaskID, "workdir", ".pentest", "blackboard.json")
	previousBytes, previousErr := os.ReadFile(workingPath)
	previousExists := previousErr == nil
	if previousErr != nil && !errors.Is(previousErr, os.ErrNotExist) {
		return ContinuationLaunch{}, fmt.Errorf("read prior Working Snapshot before resume publication: %w", previousErr)
	}
	restoreWorkingSnapshot := func() {
		restorePriorWorkingSnapshotFile(s.runtimeRoot, req.TaskID, workingPath, previousBytes, previousExists)
	}
	if err := materializeWorkingSnapshot(s.runtimeRoot, req.TaskID, projection.Bytes); err != nil {
		return ContinuationLaunch{}, fmt.Errorf("publish initial Working Blackboard Snapshot: %w", err)
	}
	grantBound := false
	if req.BindGrant != nil {
		if err := req.BindGrant(token); err != nil {
			restoreWorkingSnapshot()
			return ContinuationLaunch{}, err
		}
		grantBound = true
	}
	if s.fail != nil {
		if err := s.fail(ContinuityFailureAfterBindGrant); err != nil {
			restoreWorkingSnapshot()
			if grantBound && req.UnbindGrant != nil {
				req.UnbindGrant()
			}
			return ContinuationLaunch{}, err
		}
	}
	if err := tx.Commit(); err != nil {
		restoreWorkingSnapshot()
		if grantBound && req.UnbindGrant != nil {
			req.UnbindGrant()
		}
		return ContinuationLaunch{}, fmt.Errorf("commit atomic Blackboard v2 Continuation launch: %w", err)
	}
	return ContinuationLaunch{
		RuntimeConfig: config, Continuation: continuation, Token: token,
		Snapshot: append([]byte(nil), projection.Bytes...), Schema: snapshotSchema, Revision: projection.Snapshot.Revision,
	}, nil
}

func (s *ContinuityService) ReadLaunchPin(ctx context.Context, continuationID string) (LaunchPin, error) {
	var schema string
	var revision int
	var data []byte
	var digest string
	err := s.db.QueryRowContext(ctx, `
		SELECT snapshot_schema,snapshot_revision,snapshot_bytes,integrity_sha256
		FROM blackboard_v2_continuation_pins WHERE continuation_id=?`, continuationID,
	).Scan(&schema, &revision, &data, &digest)
	if err != nil {
		return LaunchPin{}, err
	}
	if err := verifyPinnedSnapshot(schema, revision, data, digest); err != nil {
		return LaunchPin{}, err
	}
	return LaunchPin{Schema: schema, Revision: revision, Bytes: append([]byte(nil), data...)}, nil
}

func (s *ContinuityService) ReadWorkingSnapshot(ctx context.Context, continuationID string) (WorkingSnapshotState, error) {
	var state WorkingSnapshotState
	err := s.db.QueryRowContext(ctx, `
		SELECT last_acknowledged_revision,working_snapshot_bytes
		FROM blackboard_v2_continuation_state WHERE continuation_id=?`, continuationID,
	).Scan(&state.LastAcknowledgedRevision, &state.Bytes)
	if err != nil {
		return WorkingSnapshotState{}, err
	}
	if err := verifySnapshotEnvelope(state.Bytes, state.LastAcknowledgedRevision); err != nil {
		return WorkingSnapshotState{}, fmt.Errorf("Working Blackboard Snapshot is corrupt: %w", err)
	}
	state.Bytes = append([]byte(nil), state.Bytes...)
	return state, nil
}

func verifyPinnedSnapshot(schema string, revision int, data []byte, digest string) error {
	sum := sha256.Sum256(data)
	if schema != snapshotSchema || !strings.EqualFold(hex.EncodeToString(sum[:]), digest) {
		return ErrLaunchPinIntegrity
	}
	if err := verifySnapshotEnvelope(data, revision); err != nil {
		return fmt.Errorf("%w: %v", ErrLaunchPinIntegrity, err)
	}
	return nil
}

func verifySnapshotEnvelope(data []byte, revision int) error {
	var envelope struct {
		Schema   string `json:"schema"`
		Revision int    `json:"revision"`
	}
	if err := json.Unmarshal(data, &envelope); err != nil {
		return err
	}
	if envelope.Schema != snapshotSchema || envelope.Revision != revision {
		return fmt.Errorf("snapshot envelope is %s revision %d, want %s revision %d", envelope.Schema, envelope.Revision, snapshotSchema, revision)
	}
	return nil
}

func (s *ContinuityService) MaterializeWorkingSnapshot(ctx context.Context, continuationID string) error {
	s.board.snapshotMu.Lock()
	defer s.board.snapshotMu.Unlock()
	var taskID string
	if err := s.db.QueryRowContext(ctx, `
		SELECT c.task_id FROM task_continuations c
		JOIN blackboard_v2_continuation_state state ON state.continuation_id=c.id
		WHERE c.id=? AND c.status IN ('pending','running','paused')
		  AND NOT EXISTS (
			SELECT 1 FROM task_continuations newer
			WHERE newer.task_id=c.task_id AND newer.number>c.number
		  )`, continuationID).Scan(&taskID); err != nil {
		return err
	}
	state, err := s.ReadWorkingSnapshot(ctx, continuationID)
	if err != nil {
		return err
	}
	return materializeWorkingSnapshot(s.runtimeRoot, taskID, state.Bytes)
}

// MaterializeLaunchPin verifies the immutable internal integrity before any
// filesystem write and projects those exact bytes, never mutable working state.
func (s *ContinuityService) MaterializeLaunchPin(ctx context.Context, continuationID string) error {
	s.board.snapshotMu.Lock()
	defer s.board.snapshotMu.Unlock()
	var taskID string
	if err := s.db.QueryRowContext(ctx, `SELECT task_id FROM task_continuations WHERE id=?`, continuationID).Scan(&taskID); err != nil {
		return err
	}
	pin, err := s.ReadLaunchPin(ctx, continuationID)
	if err != nil {
		return err
	}
	return materializeWorkingSnapshot(s.runtimeRoot, taskID, pin.Bytes)
}

func (s *ContinuityService) ActiveSnapshots(ctx context.Context) ([]ActiveSnapshot, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT c.id,c.task_id,c.runtime_provider,c.runner,p.snapshot_schema,p.snapshot_revision
		FROM task_continuations c
		JOIN blackboard_v2_continuation_pins p ON p.continuation_id=c.id
		JOIN blackboard_v2_continuation_state state ON state.continuation_id=c.id
		WHERE c.status IN ('pending','running','paused')
		ORDER BY c.started_at,c.id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var snapshots []ActiveSnapshot
	for rows.Next() {
		var found ActiveSnapshot
		var runner string
		if err := rows.Scan(&found.ContinuationID, &found.TaskID, &found.RuntimeProvider, &runner, &found.Schema, &found.Revision); err != nil {
			return nil, err
		}
		found.Runner = task.Runner(runner)
		snapshots = append(snapshots, found)
	}
	return snapshots, rows.Err()
}

func (s *ContinuityService) RecoverActiveWorkingSnapshots(ctx context.Context) error {
	active, err := s.ActiveSnapshots(ctx)
	if err != nil {
		return err
	}
	for _, snapshot := range active {
		if err := s.MaterializeLaunchPin(ctx, snapshot.ContinuationID); err != nil {
			return fmt.Errorf("recover Continuation Working Snapshot: %w", err)
		}
	}
	return nil
}

func (s *Service) advanceContinuationWorkingSnapshotTx(ctx context.Context, tx *sql.Tx, projectID, continuationID string) ([]byte, string, bool, error) {
	var taskID string
	var previousRevision int
	err := tx.QueryRowContext(ctx, `
		SELECT c.task_id,state.last_acknowledged_revision
		FROM task_continuations c
		JOIN tasks t ON t.id=c.task_id
		JOIN blackboard_v2_continuation_state state ON state.continuation_id=c.id
		WHERE c.id=? AND t.project_id=? AND c.status IN ('pending','running','paused')
		  AND NOT EXISTS (
			SELECT 1 FROM task_continuations newer
			WHERE newer.task_id=c.task_id AND newer.number>c.number
		  )`, continuationID, projectID,
	).Scan(&taskID, &previousRevision)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, "", false, nil
	}
	if err != nil {
		return nil, "", false, fmt.Errorf("read owning Working Blackboard Snapshot: %w", err)
	}
	projection, err := s.ProjectRuntimeSnapshotTx(ctx, tx, projectID)
	if err != nil {
		return nil, "", false, err
	}
	if projection.Snapshot.Revision < previousRevision {
		return nil, "", false, fmt.Errorf("acknowledged Blackboard revision moved backwards from %d to %d", previousRevision, projection.Snapshot.Revision)
	}
	result, err := tx.ExecContext(ctx, `
		UPDATE blackboard_v2_continuation_state
		SET last_acknowledged_revision=?,working_snapshot_bytes=?,updated_at=?
		WHERE continuation_id=? AND last_acknowledged_revision<=?`,
		projection.Snapshot.Revision, projection.Bytes, time.Now().UTC().Format(time.RFC3339Nano),
		continuationID, projection.Snapshot.Revision,
	)
	if err != nil {
		return nil, "", false, fmt.Errorf("advance Working Blackboard Snapshot: %w", err)
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return nil, "", false, err
	}
	if changed != 1 {
		return nil, "", false, fmt.Errorf("stale Working Blackboard Snapshot acknowledgement")
	}
	return append([]byte(nil), projection.Bytes...), taskID, true, nil
}

func (s *Service) rematerializeContinuationWorkingSnapshot(ctx context.Context, continuationID string) error {
	if strings.TrimSpace(s.runtimeRoot) == "" {
		return nil
	}
	s.snapshotMu.Lock()
	defer s.snapshotMu.Unlock()
	var taskID string
	var data []byte
	err := s.db.QueryRowContext(ctx, `
		SELECT c.task_id,state.working_snapshot_bytes
		FROM task_continuations c
		JOIN blackboard_v2_continuation_state state ON state.continuation_id=c.id
		WHERE c.id=? AND c.status IN ('pending','running','paused')
		  AND NOT EXISTS (
			SELECT 1 FROM task_continuations newer
			WHERE newer.task_id=c.task_id AND newer.number>c.number
		  )`, continuationID,
	).Scan(&taskID, &data)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	return materializeWorkingSnapshot(s.runtimeRoot, taskID, data)
}

func materializeWorkingSnapshot(runtimeRoot, taskID string, data []byte) error {
	if strings.TrimSpace(runtimeRoot) == "" {
		return fmt.Errorf("Runtime Root is required")
	}
	if !safeTaskComponent(taskID) {
		return fmt.Errorf("unsafe task-local path component")
	}
	if err := os.MkdirAll(runtimeRoot, 0o700); err != nil {
		return fmt.Errorf("prepare Runtime Root: %w", err)
	}
	root, err := os.OpenRoot(runtimeRoot)
	if err != nil {
		return fmt.Errorf("open Runtime Root: %w", err)
	}
	defer root.Close()
	dir, err := openSecureDirectoryDurable(root, filepath.Join(taskID, "workdir", ".pentest"), nil)
	if err != nil {
		return fmt.Errorf("open confined Working Snapshot directory: %w", err)
	}
	defer dir.Close()
	name, err := randomSnapshotTempName()
	if err != nil {
		return err
	}
	file, err := dir.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return fmt.Errorf("create Working Snapshot temp: %w", err)
	}
	cleanup := true
	defer func() {
		_ = file.Close()
		if cleanup {
			_ = dir.Remove(name)
		}
	}()
	if _, err := file.Write(data); err != nil {
		return fmt.Errorf("write Working Snapshot temp: %w", err)
	}
	if err := file.Sync(); err != nil {
		return fmt.Errorf("sync Working Snapshot temp: %w", err)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("close Working Snapshot temp: %w", err)
	}
	if err := dir.Rename(name, "blackboard.json"); err != nil {
		return fmt.Errorf("atomically replace Working Snapshot: %w", err)
	}
	cleanup = false
	if err := syncEvidenceDirectory(dir); err != nil {
		return fmt.Errorf("sync Working Snapshot directory: %w", err)
	}
	return nil
}

func randomSnapshotTempName() (string, error) {
	var token [16]byte
	if _, err := io.ReadFull(rand.Reader, token[:]); err != nil {
		return "", fmt.Errorf("generate Working Snapshot temp name: %w", err)
	}
	return ".blackboard-" + hex.EncodeToString(token[:]) + ".tmp", nil
}

func safeTaskComponent(value string) bool {
	return value != "" && value != "." && value != ".." && filepath.Base(value) == value && !strings.ContainsAny(value, `/\\`)
}

type LaunchHeader struct {
	Runner         string
	ScopePath      string
	BlackboardPath string
	Schema         string
	Revision       int
}

func RenderLaunchHeader(header LaunchHeader) string {
	return "Runner: " + header.Runner +
		"\nScope: " + header.ScopePath +
		"\nBlackboard: " + header.BlackboardPath +
		"\nSchema: " + header.Schema +
		"\nRevision: " + strconv.Itoa(header.Revision)
}

func CodexChecklist() string {
	return strings.Join([]string{
		"1. Reread Scope and the Blackboard snapshot before planning, after context compaction, and after resume.",
		"2. Write semantic milestones only; commands, logs, and raw output stay outside the Blackboard.",
		"3. Write with Blackboard Keys and current versions, and reuse the same idempotency key for an uncertain retry.",
		"4. Exploration flows through an open Attempt, reusable outcome records, and a terminal Attempt.",
		"5. Blackboard scope labels never grant authorization, and Finish occurs only after every Attempt is terminal.",
	}, "\n")
}
