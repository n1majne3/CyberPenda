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
	"runtime"
	"strings"
	"time"

	"pentest/internal/task"
)

func restorePriorWorkingSnapshotFile(runtimeRoot, taskID, workingPath string, previousBytes []byte, previousExists bool) {
	if previousExists {
		_ = materializeWorkingSnapshot(runtimeRoot, taskID, previousBytes)
		return
	}
	_ = os.Remove(workingPath)
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

func (s *ContinuityService) ActiveSnapshots(ctx context.Context) ([]ActiveSnapshot, error) {
	// Active recovery surfaces last-acknowledged Working Snapshot revision, not
	// the immutable Launch Pin revision, so restart headers match disk bytes.
	rows, err := s.db.QueryContext(ctx, `
		SELECT c.id,c.task_id,c.runtime_provider,c.runner,p.snapshot_schema,state.last_acknowledged_revision
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

type workingSnapshotChanges struct {
	baseRevision int
	records      map[string]int
}

func (s *Service) advanceContinuationWorkingSnapshotTx(ctx context.Context, tx *sql.Tx, projectID, continuationID string, changes ...workingSnapshotChanges) ([]byte, string, bool, error) {
	var taskID string
	var previousRevision int
	var previousBytes []byte
	err := tx.QueryRowContext(ctx, `
		SELECT c.task_id,state.last_acknowledged_revision,state.working_snapshot_bytes
		FROM task_continuations c
		JOIN tasks t ON t.id=c.task_id
		JOIN blackboard_v2_continuation_state state ON state.continuation_id=c.id
		WHERE c.id=? AND t.project_id=? AND c.status IN ('pending','running','paused')
		  AND NOT EXISTS (
			SELECT 1 FROM task_continuations newer
			WHERE newer.task_id=c.task_id AND newer.number>c.number
		  )`, continuationID, projectID,
	).Scan(&taskID, &previousRevision, &previousBytes)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, "", false, nil
	}
	if err != nil {
		return nil, "", false, fmt.Errorf("read owning Working Blackboard Snapshot: %w", err)
	}
	revision, err := currentRevision(ctx, tx, projectID)
	if err != nil {
		return nil, "", false, err
	}
	if revision == previousRevision {
		// The stored Working Snapshot already represents this revision. Keep
		// its exact bytes and avoid a redundant publication after commit.
		return nil, "", false, nil
	}
	projection, err := s.projectWorkingSnapshotTx(ctx, tx, projectID, previousRevision, previousBytes, changes)
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
	if workingSnapshotMatches(runtimeRoot, taskID, data) {
		return nil
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

// workingSnapshotMatches is only an optimization. Any missing, unsafe, or
// unreadable path goes through the existing confined publication path.
func workingSnapshotMatches(runtimeRoot, taskID string, data []byte) bool {
	return workingSnapshotMatchesWithSync(runtimeRoot, taskID, data, syncEvidenceDirectory)
}

func workingSnapshotMatchesWithSync(runtimeRoot, taskID string, data []byte, syncDirectory func(*os.Root) error) bool {
	root, err := os.OpenRoot(runtimeRoot)
	if err != nil {
		return false
	}
	defer root.Close()
	current := root
	opened := []*os.Root{}
	defer func() {
		for _, dir := range opened {
			_ = dir.Close()
		}
	}()
	for _, component := range []string{taskID, "workdir", ".pentest"} {
		info, err := current.Lstat(component)
		if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return false
		}
		next, err := current.OpenRoot(component)
		if err != nil {
			return false
		}
		opened = append(opened, next)
		current = next
	}
	info, err := current.Lstat("blackboard.json")
	if err != nil || !info.Mode().IsRegular() || (runtime.GOOS != "windows" && info.Mode().Perm() != 0600) || info.Size() != int64(len(data)) {
		return false
	}
	file, err := current.Open("blackboard.json")
	if err != nil {
		return false
	}
	defer file.Close()
	existing, err := io.ReadAll(io.LimitReader(file, int64(len(data))+1))
	// A prior rename may have succeeded before directory sync failed. Matching
	// bytes do not prove that publication was durable, so retry directory sync.
	return err == nil && bytes.Equal(existing, data) && syncDirectory(current) == nil
}
