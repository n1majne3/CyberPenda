package blackboardv2

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// Issue #117 — publish-before-commit must restore exact prior Working Snapshot
// file state when materialization or commit fails after the filesystem advances.
// Existing Continuity/Finish failure injectors cannot force SynchronizeContinuation
// commit failure, so this pins the restore helper used on those error paths.
func TestRestorePriorWorkingSnapshotFilePreservesExactBytesAndAbsence(t *testing.T) {
	root := t.TempDir()
	taskID := "task-restore-prior"
	workingPath := filepath.Join(root, taskID, "workdir", ".pentest", "blackboard.json")
	prior := []byte(`{"schema":"runtime-blackboard/v2","revision":1,"records":{"prior":true}}`)
	advanced := []byte(`{"schema":"runtime-blackboard/v2","revision":2,"records":{"advanced":true}}`)

	if err := materializeWorkingSnapshot(root, taskID, prior); err != nil {
		t.Fatalf("materialize prior Working Snapshot: %v", err)
	}
	previousBytes, err := os.ReadFile(workingPath)
	if err != nil {
		t.Fatalf("capture prior Working Snapshot: %v", err)
	}
	if !bytes.Equal(previousBytes, prior) {
		t.Fatalf("captured prior bytes drifted\ngot=%s\nwant=%s", previousBytes, prior)
	}

	// Filesystem advanced as publish-before-commit does; commit/publication then fails.
	if err := materializeWorkingSnapshot(root, taskID, advanced); err != nil {
		t.Fatalf("materialize advanced Working Snapshot: %v", err)
	}
	advancedOnDisk, err := os.ReadFile(workingPath)
	if err != nil || !bytes.Equal(advancedOnDisk, advanced) {
		t.Fatalf("advanced disk state = %s, %v", advancedOnDisk, err)
	}

	restorePriorWorkingSnapshotFile(root, taskID, workingPath, previousBytes, true)
	restored, err := os.ReadFile(workingPath)
	if err != nil {
		t.Fatalf("read restored Working Snapshot: %v", err)
	}
	if !bytes.Equal(restored, prior) {
		t.Fatalf("restore did not preserve exact prior bytes\ngot=%s\nwant=%s", restored, prior)
	}

	// Absence path: no prior file means restore removes the advanced publication.
	if err := materializeWorkingSnapshot(root, taskID, advanced); err != nil {
		t.Fatalf("materialize advanced again: %v", err)
	}
	restorePriorWorkingSnapshotFile(root, taskID, workingPath, nil, false)
	if _, err := os.Stat(workingPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("restore of absent prior should remove Working Snapshot: %v", err)
	}
}
