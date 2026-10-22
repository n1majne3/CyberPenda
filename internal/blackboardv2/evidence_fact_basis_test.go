package blackboardv2_test

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"pentest/internal/blackboardv2"
	"pentest/internal/project"
	"pentest/internal/task"
)

func TestEvidenceCannotBecomeMissingWhileSoleBasisOfConfirmedFact(t *testing.T) {
	fixture := newEvidenceV2Fixture(t, "Dependent Confirmed Fact")
	_, err := fixture.service.ApplyForContinuation(context.Background(), fixture.projectID, fixture.continuationID, blackboardv2.ChangeBatch{
		Schema: "semantic-change-batch/v2", IdempotencyKey: "create-dependent-fact",
		Changes: []blackboardv2.Change{{Op: "create", Key: "fact:dependent", Type: "fact", Record: blackboardv2.FactRecord{
			Category: "authentication", Summary: "The retained response proves the dependent conclusion", Confidence: "tentative", ScopeStatus: "in_scope",
		}}},
	})
	if err != nil {
		t.Fatalf("create dependent Fact: %v", err)
	}
	fixture.writeSource(t, "sole-basis.txt", "sole basis\n")
	if _, err := fixture.service.RetainEvidenceForContinuation(context.Background(), fixture.projectID, fixture.continuationID, blackboardv2.RetainEvidenceRequest{
		IdempotencyKey: "retain-sole-basis", Key: "evidence:sole-basis", Attempt: "attempt:evidence",
		SourcePath: "sole-basis.txt", ArtifactType: "text", Summary: "Sole valid basis",
		Links: []blackboardv2.EvidenceLink{{"evidences", "fact:dependent"}},
	}); err != nil {
		t.Fatalf("retain sole-basis Evidence: %v", err)
	}
	if _, err := fixture.service.ApplyForContinuation(context.Background(), fixture.projectID, fixture.continuationID, blackboardv2.ChangeBatch{
		Schema: "semantic-change-batch/v2", IdempotencyKey: "confirm-dependent-fact",
		Changes: []blackboardv2.Change{{Op: "transition", Key: "fact:dependent", Version: 1, Status: "confirmed"}},
	}); err != nil {
		t.Fatalf("confirm dependent Fact: %v", err)
	}
	missingChange := blackboardv2.ChangeBatch{
		Schema: "semantic-change-batch/v2", IdempotencyKey: "reject-missing-sole-basis",
		Changes: []blackboardv2.Change{{Op: "transition", Key: "evidence:sole-basis", Version: 1, Status: "missing", Summary: "The sole payload disappeared"}},
	}
	if _, err := fixture.service.ApplyForContinuation(context.Background(), fixture.projectID, fixture.continuationID, missingChange); !isSemanticCode(err, "semantic_validation") {
		t.Fatalf("sole-basis missing transition error = %#v, want semantic_validation", err)
	}
	detail, err := fixture.service.ReadCurrent(context.Background(), fixture.projectID, "evidence:sole-basis")
	if err != nil || detail.Version != 1 || detail.Record.Status != "available" {
		t.Fatalf("rejected missing transition changed Evidence: %#v, err = %v", detail, err)
	}
	fixture.writeSource(t, "alternate-basis.txt", "alternate basis\n")
	if _, err := fixture.service.RetainEvidenceForContinuation(context.Background(), fixture.projectID, fixture.continuationID, blackboardv2.RetainEvidenceRequest{
		IdempotencyKey: "retain-alternate-basis", Key: "evidence:alternate-basis", Attempt: "attempt:evidence",
		SourcePath: "alternate-basis.txt", ArtifactType: "text", Summary: "Alternate valid basis",
		Links: []blackboardv2.EvidenceLink{{"evidences", "fact:dependent"}},
	}); err != nil {
		t.Fatalf("retain alternate Evidence basis: %v", err)
	}
	missingChange.IdempotencyKey = "allow-missing-with-alternate-basis"
	if _, err := fixture.service.ApplyForContinuation(context.Background(), fixture.projectID, fixture.continuationID, missingChange); err != nil {
		t.Fatalf("mark Evidence missing with alternate basis: %v", err)
	}
}

func TestEvidenceSupersedeRequiresReplacementBasisAndMovesReplacedEvidenceToHistory(t *testing.T) {
	fixture := newEvidenceV2Fixture(t, "Evidence Supersession")
	_, err := fixture.service.ApplyForContinuation(context.Background(), fixture.projectID, fixture.continuationID, blackboardv2.ChangeBatch{
		Schema: "semantic-change-batch/v2", IdempotencyKey: "create-superseded-fact",
		Changes: []blackboardv2.Change{{Op: "create", Key: "fact:superseded-evidence", Type: "fact", Record: blackboardv2.FactRecord{
			Category: "authentication", Summary: "The original payload proves the conclusion", Confidence: "tentative", ScopeStatus: "in_scope",
		}}},
	})
	if err != nil {
		t.Fatalf("create superseded-Evidence Fact: %v", err)
	}
	fixture.writeSource(t, "original.txt", "original proof\n")
	fixture.writeSource(t, "replacement.txt", "replacement proof\n")
	if _, err := fixture.service.RetainEvidenceForContinuation(context.Background(), fixture.projectID, fixture.continuationID, blackboardv2.RetainEvidenceRequest{
		IdempotencyKey: "retain-original", Key: "evidence:original", Attempt: "attempt:evidence",
		SourcePath: "original.txt", ArtifactType: "text", Summary: "Original proof",
		Links: []blackboardv2.EvidenceLink{{"evidences", "fact:superseded-evidence"}},
	}); err != nil {
		t.Fatalf("retain original Evidence: %v", err)
	}
	if _, err := fixture.service.RetainEvidenceForContinuation(context.Background(), fixture.projectID, fixture.continuationID, blackboardv2.RetainEvidenceRequest{
		IdempotencyKey: "retain-replacement", Key: "evidence:replacement", Attempt: "attempt:evidence",
		SourcePath: "replacement.txt", ArtifactType: "text", Summary: "Replacement proof",
	}); err != nil {
		t.Fatalf("retain replacement Evidence: %v", err)
	}
	if _, err := fixture.service.ApplyForContinuation(context.Background(), fixture.projectID, fixture.continuationID, blackboardv2.ChangeBatch{
		Schema: "semantic-change-batch/v2", IdempotencyKey: "confirm-before-supersede",
		Changes: []blackboardv2.Change{{Op: "transition", Key: "fact:superseded-evidence", Version: 1, Status: "confirmed"}},
	}); err != nil {
		t.Fatalf("confirm Fact before Evidence supersession: %v", err)
	}
	if _, err := fixture.service.ApplyForContinuation(context.Background(), fixture.projectID, fixture.continuationID, blackboardv2.ChangeBatch{
		Schema: "semantic-change-batch/v2", IdempotencyKey: "reject-unsupported-supersede",
		Changes: []blackboardv2.Change{{Op: "supersede", Replacement: "evidence:replacement", ReplacementVersion: 1, Replaced: "evidence:original", ReplacedVersion: 1}},
	}); !isSemanticCode(err, "semantic_validation") {
		t.Fatalf("unsupported Evidence supersede error = %#v, want semantic_validation", err)
	}
	result, err := fixture.service.ApplyForContinuation(context.Background(), fixture.projectID, fixture.continuationID, blackboardv2.ChangeBatch{
		Schema: "semantic-change-batch/v2", IdempotencyKey: "supersede-with-replacement-basis",
		Changes: []blackboardv2.Change{
			{Op: "relate", From: "evidence:replacement", Relation: "evidences", To: "fact:superseded-evidence"},
			{Op: "supersede", Replacement: "evidence:replacement", ReplacementVersion: 1, Replaced: "evidence:original", ReplacedVersion: 1},
		},
	})
	if err != nil {
		t.Fatalf("supersede Evidence with replacement basis: %v", err)
	}
	if got, want := mustTupleJSON(t, result.Records), [][]any{{"evidence:original", float64(2)}}; !reflect.DeepEqual(got, want) {
		t.Fatalf("Evidence supersede records = %#v, want %#v", got, want)
	}
	if _, err := fixture.service.ReadCurrent(context.Background(), fixture.projectID, "evidence:original"); !isSemanticCode(err, "not_found") {
		t.Fatalf("superseded Evidence current read error = %#v, want not_found", err)
	}
	history, err := fixture.service.ReadHistory(context.Background(), fixture.projectID, "evidence:original", blackboardv2.HistoryOptions{})
	if err != nil {
		t.Fatalf("read superseded Evidence history: %v", err)
	}
	if len(history.Items) < 2 || history.Items[1].Record == nil || history.Items[1].Record.Status != "superseded" {
		t.Fatalf("superseded Evidence history = %#v", history.Items)
	}
	snapshot, err := fixture.service.RuntimeSnapshot(context.Background(), fixture.projectID)
	if err != nil {
		t.Fatalf("read Snapshot after Evidence supersession: %v", err)
	}
	if _, exists := snapshot.Knowledge.Evidence["evidence:original"]; exists {
		t.Fatalf("superseded Evidence remained in Snapshot: %#v", snapshot.Knowledge.Evidence)
	}
	if _, exists := snapshot.Knowledge.Evidence["evidence:replacement"]; !exists {
		t.Fatalf("replacement Evidence missing from Snapshot: %#v", snapshot.Knowledge.Evidence)
	}
}

func TestEvidenceDerivedFromIsVersionedAcyclicAndProjectIsolated(t *testing.T) {
	fixture := newEvidenceV2Fixture(t, "Evidence Lineage")
	ctx := context.Background()
	for index, key := range []string{"evidence:source", "evidence:derived"} {
		name := key[len("evidence:"):] + ".txt"
		fixture.writeSource(t, name, fmt.Sprintf("lineage payload %d\n", index))
		if _, err := fixture.service.RetainEvidenceForContinuation(ctx, fixture.projectID, fixture.continuationID, blackboardv2.RetainEvidenceRequest{
			IdempotencyKey: "retain-" + key, Key: key, Attempt: "attempt:evidence", SourcePath: name,
			ArtifactType: "text", Summary: "Evidence lineage payload " + key,
		}); err != nil {
			t.Fatalf("retain %s: %v", key, err)
		}
	}
	lineage, err := fixture.service.ApplyForContinuation(ctx, fixture.projectID, fixture.continuationID, blackboardv2.ChangeBatch{
		Schema: "semantic-change-batch/v2", IdempotencyKey: "derive-evidence",
		Changes: []blackboardv2.Change{{Op: "relate", From: "evidence:derived", Relation: "derived_from", To: "evidence:source"}},
	})
	if err != nil {
		t.Fatalf("relate derived Evidence: %v", err)
	}
	if got, want := mustTupleJSON(t, lineage.Relations), [][]any{{"evidence:derived", "derived_from", "evidence:source", float64(1)}}; !reflect.DeepEqual(got, want) {
		t.Fatalf("Evidence lineage relations = %#v, want %#v", got, want)
	}
	if _, err := fixture.service.ApplyForContinuation(ctx, fixture.projectID, fixture.continuationID, blackboardv2.ChangeBatch{
		Schema: "semantic-change-batch/v2", IdempotencyKey: "cycle-evidence-lineage",
		Changes: []blackboardv2.Change{{Op: "relate", From: "evidence:source", Relation: "derived_from", To: "evidence:derived"}},
	}); !isSemanticCode(err, "semantic_validation") {
		t.Fatalf("Evidence lineage cycle error = %#v, want semantic_validation", err)
	}
	if _, err := fixture.service.ApplyForContinuation(ctx, fixture.projectID, fixture.continuationID, blackboardv2.ChangeBatch{
		Schema: "semantic-change-batch/v2", IdempotencyKey: "reject-lineage-version",
		Changes: []blackboardv2.Change{{Op: "relate", From: "evidence:derived", Relation: "derived_from", To: "evidence:source", Version: 1}},
	}); !isSemanticCode(err, "semantic_validation") {
		t.Fatalf("versioned ordinary Evidence lineage error = %#v, want semantic_validation", err)
	}

	projects := project.NewService(fixture.db)
	foreignProject, err := projects.Create("Foreign Evidence Lineage", "", project.Scope{}, project.Defaults{})
	if err != nil {
		t.Fatalf("create foreign Project: %v", err)
	}
	tasks := task.NewService(fixture.db, projects)
	foreignTask, err := tasks.Create(task.CreateRequest{ProjectID: foreignProject.ID, Type: task.TypePentest, Goal: "Foreign Evidence", Runner: task.RunnerSandbox})
	if err != nil {
		t.Fatalf("create foreign Task: %v", err)
	}
	foreignContinuation, err := tasks.CreateContinuation(foreignTask.ID, "profile-foreign", "codex", task.RunnerSandbox)
	if err != nil {
		t.Fatalf("create foreign Continuation: %v", err)
	}
	_, err = fixture.service.ApplyForContinuation(ctx, foreignProject.ID, foreignContinuation.ID, blackboardv2.ChangeBatch{
		Schema: "semantic-change-batch/v2", IdempotencyKey: "prepare-foreign-evidence",
		Changes: []blackboardv2.Change{
			{Op: "create", Key: "objective:foreign-evidence", Type: "objective", Record: blackboardv2.ObjectiveRecord{Status: "open", Objective: "Retain foreign Evidence"}},
			{Op: "create", Key: "attempt:foreign-evidence", Type: "attempt", Record: blackboardv2.AttemptRecord{Status: "open", Summary: "Retain foreign Evidence"}},
			{Op: "relate", From: "attempt:foreign-evidence", Relation: "tests", To: "objective:foreign-evidence"},
		},
	})
	if err != nil {
		t.Fatalf("prepare foreign Evidence: %v", err)
	}
	foreignWorkdir := filepath.Join(fixture.runtimeRoot, foreignTask.ID, "workdir")
	if err := os.MkdirAll(foreignWorkdir, 0o700); err != nil {
		t.Fatalf("create foreign workdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(foreignWorkdir, "foreign.txt"), []byte("foreign\n"), 0o600); err != nil {
		t.Fatalf("write foreign source: %v", err)
	}
	if _, err := fixture.service.RetainEvidenceForContinuation(ctx, foreignProject.ID, foreignContinuation.ID, blackboardv2.RetainEvidenceRequest{
		IdempotencyKey: "retain-foreign", Key: "evidence:foreign", Attempt: "attempt:foreign-evidence",
		SourcePath: "foreign.txt", ArtifactType: "text", Summary: "Foreign Project Evidence",
	}); err != nil {
		t.Fatalf("retain foreign Evidence: %v", err)
	}
	if _, err := fixture.service.ApplyForContinuation(ctx, fixture.projectID, fixture.continuationID, blackboardv2.ChangeBatch{
		Schema: "semantic-change-batch/v2", IdempotencyKey: "reject-foreign-lineage",
		Changes: []blackboardv2.Change{{Op: "relate", From: "evidence:derived", Relation: "derived_from", To: "evidence:foreign"}},
	}); !isSemanticCode(err, "not_found") {
		t.Fatalf("foreign Evidence lineage error = %#v, want not_found", err)
	}
}

func TestExactRetainReplayRecoversAfterSemanticCommitAndProducingAttemptTerminal(t *testing.T) {
	fixture := newEvidenceV2Fixture(t, "Terminal Evidence Replay")
	injector := &failEvidenceV2Once{point: blackboardv2.EvidenceFailureAfterGraphCommit}
	fixture.service = blackboardv2.NewServiceWithEvidence(fixture.db, blackboardv2.EvidenceConfig{
		RuntimeRoot: fixture.runtimeRoot, ArtifactRoot: fixture.runtimeRoot, Failures: injector,
	})
	fixture.writeSource(t, "terminal-replay.txt", "terminal replay proof\n")
	request := blackboardv2.RetainEvidenceRequest{
		IdempotencyKey: "terminal-replay", Key: "evidence:terminal-replay", Attempt: "attempt:evidence",
		SourcePath: "terminal-replay.txt", ArtifactType: "text", Summary: "Proof committed before a lost response",
	}
	if _, err := fixture.service.RetainEvidenceForContinuation(context.Background(), fixture.projectID, fixture.continuationID, request); err == nil {
		t.Fatal("injected post-semantic-commit failure unexpectedly succeeded")
	}
	detail, err := fixture.service.ReadCurrent(context.Background(), fixture.projectID, request.Key)
	if err != nil || detail.Type != "evidence" || detail.Version != 1 {
		t.Fatalf("semantic commit did not retain Evidence: %#v, err = %v", detail, err)
	}
	if _, err := fixture.service.ApplyForContinuation(context.Background(), fixture.projectID, fixture.continuationID, blackboardv2.ChangeBatch{
		Schema: "semantic-change-batch/v2", IdempotencyKey: "finish-producing-attempt",
		Changes: []blackboardv2.Change{{Op: "transition", Key: "attempt:evidence", Version: 1, Status: "succeeded", Summary: "The retained proof established the outcome"}},
	}); err != nil {
		t.Fatalf("terminalize producing Attempt: %v", err)
	}
	if err := os.Remove(filepath.Join(fixture.workdir, "terminal-replay.txt")); err != nil {
		t.Fatalf("remove source before exact replay: %v", err)
	}

	recovered, err := fixture.service.RetainEvidenceForContinuation(context.Background(), fixture.projectID, fixture.continuationID, request)
	if err != nil {
		t.Fatalf("recover exact Retain Evidence after terminal Attempt: %v", err)
	}
	replay, err := fixture.service.RetainEvidenceForContinuation(context.Background(), fixture.projectID, fixture.continuationID, request)
	if err != nil {
		t.Fatalf("replay completed Retain Evidence: %v", err)
	}
	if !reflect.DeepEqual(recovered, replay) {
		t.Fatalf("recovered replay drifted:\nrecovered %#v\nreplay %#v", recovered, replay)
	}
	if _, err := fixture.service.RetainEvidenceForContinuation(context.Background(), fixture.projectID, fixture.continuationID, blackboardv2.RetainEvidenceRequest{
		IdempotencyKey: "new-after-terminal", Key: "evidence:new-after-terminal", Attempt: "attempt:evidence",
		SourcePath: "terminal-replay.txt", ArtifactType: "text", Summary: "New retain must not pass a terminal Attempt",
	}); err == nil {
		t.Fatal("new post-terminal Retain Evidence unexpectedly succeeded")
	}
}
