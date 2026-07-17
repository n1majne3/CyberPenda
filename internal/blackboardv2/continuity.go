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

	"pentest/internal/store"
	"pentest/internal/task"
)

const snapshotEstimatorVersion = "utf8-bytes-div-4/v1"

var ErrLaunchPinIntegrity = errors.New("Launch Blackboard Pin integrity check failed")

type ContinuityFailurePoint string

const (
	ContinuityFailureBeforeCommit                     ContinuityFailurePoint = "before_commit"
	ContinuityFailureBeforeWorkingSnapshotPublication ContinuityFailurePoint = "before_working_snapshot_publication"
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
	Precommit        func(ContinuationLaunchProjection) error
}

type ContinuationLaunchProjection struct {
	Schema   string
	Revision int
}

type ContinuationLaunch struct {
	RuntimeConfig task.RuntimeConfigVersion
	Continuation  task.TaskContinuation
	Snapshot      []byte
	Schema        string
	Revision      int
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
	runtimeRoot string
	fail        ContinuityFailureInjector
}

func NewContinuityService(db *store.DB, board *Service, tasks *task.Service, runtimeRoot string) *ContinuityService {
	board.runtimeRoot = runtimeRoot
	return &ContinuityService{db: db, board: board, tasks: tasks, runtimeRoot: runtimeRoot}
}

func (s *ContinuityService) SetFailureInjector(injector ContinuityFailureInjector) {
	s.fail = injector
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
	if err := materializeWorkingSnapshot(s.runtimeRoot, req.TaskID, projection.Bytes); err != nil {
		return ContinuationLaunch{}, fmt.Errorf("publish initial Working Blackboard Snapshot: %w", err)
	}
	if err := tx.Commit(); err != nil {
		if previousExists {
			_ = materializeWorkingSnapshot(s.runtimeRoot, req.TaskID, previousBytes)
		} else {
			_ = os.Remove(workingPath)
		}
		return ContinuationLaunch{}, fmt.Errorf("commit atomic Blackboard v2 Continuation launch: %w", err)
	}
	return ContinuationLaunch{
		RuntimeConfig: config, Continuation: continuation,
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
