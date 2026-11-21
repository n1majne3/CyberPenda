package blackboardv2_test

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"pentest/internal/blackboardv2"
)

func TestEvidenceSemanticReceiptRepairsWorkingSnapshotBeforeCompletion(t *testing.T) {
	ctx := context.Background()
	fixture := newContinuityFixture(t)
	t.Cleanup(func() { _ = fixture.db.Close() })
	fixture.board = blackboardv2.NewServiceWithEvidence(fixture.db, blackboardv2.EvidenceConfig{
		RuntimeRoot: fixture.runtimeRoot, ArtifactRoot: fixture.runtimeRoot,
	})
	fixture.continuity = blackboardv2.NewContinuityService(fixture.db, fixture.board, fixture.tasks, fixture.runtimeRoot)
	launch := fixture.launch(t)
	if _, err := fixture.board.ApplyForContinuation(ctx, fixture.project.ID, launch.Continuation.ID, blackboardv2.ChangeBatch{
		Schema: "semantic-change-batch/v2", IdempotencyKey: "prepare-snapshot-recovery",
		Changes: []blackboardv2.Change{
			{Op: "create", Key: "objective:proof", Type: "objective", Record: blackboardv2.ObjectiveRecord{Status: "open", Objective: "Retain proof"}},
			{Op: "create", Key: "attempt:proof", Type: "attempt", Record: blackboardv2.AttemptRecord{Status: "open", Summary: "Capture proof"}},
			{Op: "relate", From: "attempt:proof", Relation: "tests", To: "objective:proof"},
		},
	}); err != nil {
		t.Fatal(err)
	}
	workdir := filepath.Join(fixture.runtimeRoot, fixture.task.ID, "workdir")
	if err := os.WriteFile(filepath.Join(workdir, "proof.txt"), []byte("proof\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	workingPath := filepath.Join(workdir, ".pentest", "blackboard.json")
	if err := os.Remove(workingPath); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(workingPath, 0o700); err != nil {
		t.Fatal(err)
	}
	request := blackboardv2.RetainEvidenceRequest{
		IdempotencyKey: "recover-snapshot", Key: "evidence:proof", Attempt: "attempt:proof",
		SourcePath: "proof.txt", ArtifactType: "text", Summary: "Proof with a failed Snapshot publication",
	}
	retain := func() (blackboardv2.ChangeResult, error) {
		return fixture.board.RetainEvidenceForContinuation(ctx, fixture.project.ID, launch.Continuation.ID, request)
	}
	if _, err := retain(); err == nil {
		t.Fatal("retention succeeded while Snapshot publication was blocked")
	}
	if detail, err := fixture.board.ReadCurrent(ctx, fixture.project.ID, request.Key); err != nil || detail.Version != 1 {
		t.Fatalf("committed Evidence = %#v, %v", detail, err)
	}
	if _, err := retain(); err == nil {
		t.Fatal("semantic receipt recovery succeeded without repairing the Working Snapshot")
	}
	var status string
	if err := fixture.db.QueryRow(`SELECT status FROM blackboard_v2_evidence_requests WHERE project_id=? AND continuation_id=? AND idempotency_key=?`, fixture.project.ID, launch.Continuation.ID, request.IdempotencyKey).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status == "completed" {
		t.Fatal("Evidence request completed before Working Snapshot repair")
	}
	if err := os.Remove(workingPath); err != nil {
		t.Fatal(err)
	}
	recovered, err := retain()
	if err != nil {
		t.Fatalf("recover Working Snapshot: %v", err)
	}
	want, err := fixture.continuity.ReadWorkingSnapshot(ctx, launch.Continuation.ID)
	if err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(workingPath)
	if err != nil || !bytes.Equal(got, want.Bytes) || recovered.Revision != want.LastAcknowledgedRevision {
		t.Fatalf("recovered Snapshot = %s, %v; want %s at revision %d", got, err, want.Bytes, recovered.Revision)
	}
	replay, err := retain()
	if err != nil || !reflect.DeepEqual(replay, recovered) {
		t.Fatalf("completed replay = %#v, %v; want %#v", replay, err, recovered)
	}
}
