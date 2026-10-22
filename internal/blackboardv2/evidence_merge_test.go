package blackboardv2_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"pentest/internal/blackboardv2"
	"pentest/internal/project"
	"pentest/internal/store"
	"pentest/internal/task"
)

func TestRetainEvidenceRejectsUnknownFieldsAndRequiresOwnedOpenAttemptBeforePublication(t *testing.T) {
	for name, raw := range map[string]string{
		"caller-controlled managed path": `{"idempotency_key":"closed","key":"evidence:closed","attempt":"attempt:closed","source_path":"proof.txt","artifact_type":"text","summary":"proof","managed_path":"caller-controlled"}`,
		"null version":                   `{"idempotency_key":"closed","key":"evidence:closed","version":null,"attempt":"attempt:closed","source_path":"proof.txt","artifact_type":"text","summary":"proof"}`,
		"zero version":                   `{"idempotency_key":"closed","key":"evidence:closed","version":0,"attempt":"attempt:closed","source_path":"proof.txt","artifact_type":"text","summary":"proof"}`,
		"empty media type":               `{"idempotency_key":"closed","key":"evidence:closed","attempt":"attempt:closed","source_path":"proof.txt","artifact_type":"text","summary":"proof","media_type":""}`,
		"null links":                     `{"idempotency_key":"closed","key":"evidence:closed","attempt":"attempt:closed","source_path":"proof.txt","artifact_type":"text","summary":"proof","links":null}`,
	} {
		t.Run(name, func(t *testing.T) {
			var request blackboardv2.RetainEvidenceRequest
			if err := json.Unmarshal([]byte(raw), &request); err == nil {
				t.Fatalf("Retain Evidence accepted invalid closed request %s", raw)
			}
		})
	}

	ctx := context.Background()
	root := t.TempDir()
	db, err := store.Open(filepath.Join(root, "pentest.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	projects := project.NewService(db)
	createdProject, err := projects.Create("Evidence Attempt Authority", "", project.Scope{}, project.Defaults{})
	if err != nil {
		t.Fatalf("create Project: %v", err)
	}
	tasks := task.NewService(db, projects)
	ownerTask, err := tasks.Create(task.CreateRequest{ProjectID: createdProject.ID, Type: task.TypePentest, Goal: "Own Evidence", Runner: task.RunnerSandbox})
	if err != nil {
		t.Fatalf("create owner Task: %v", err)
	}
	owner, err := tasks.CreateContinuation(ownerTask.ID, "profile-owner", "codex", task.RunnerSandbox)
	if err != nil {
		t.Fatalf("create owner Continuation: %v", err)
	}
	peerTask, err := tasks.Create(task.CreateRequest{ProjectID: createdProject.ID, Type: task.TypePentest, Goal: "Peer Evidence", Runner: task.RunnerSandbox})
	if err != nil {
		t.Fatalf("create peer Task: %v", err)
	}
	peer, err := tasks.CreateContinuation(peerTask.ID, "profile-peer", "codex", task.RunnerSandbox)
	if err != nil {
		t.Fatalf("create peer Continuation: %v", err)
	}
	runtimeRoot := filepath.Join(root, "runtime")
	service := blackboardv2.NewServiceWithEvidence(db, blackboardv2.EvidenceConfig{RuntimeRoot: runtimeRoot, ArtifactRoot: runtimeRoot})
	_, err = service.ApplyForContinuation(ctx, createdProject.ID, owner.ID, blackboardv2.ChangeBatch{
		Schema: "semantic-change-batch/v2", IdempotencyKey: "prepare-owned-attempt",
		Changes: []blackboardv2.Change{
			{Op: "create", Key: "objective:owned", Type: "objective", Record: blackboardv2.ObjectiveRecord{Status: "open", Objective: "Capture owned proof"}},
			{Op: "create", Key: "attempt:owned", Type: "attempt", Record: blackboardv2.AttemptRecord{Status: "open", Summary: "Capture owned proof"}},
			{Op: "relate", From: "attempt:owned", Relation: "tests", To: "objective:owned"},
		},
	})
	if err != nil {
		t.Fatalf("prepare owned Attempt: %v", err)
	}
	for _, taskID := range []string{ownerTask.ID, peerTask.ID} {
		workdir := filepath.Join(runtimeRoot, taskID, "workdir")
		if err := os.MkdirAll(workdir, 0o700); err != nil {
			t.Fatalf("create Task workdir: %v", err)
		}
		if err := os.WriteFile(filepath.Join(workdir, "proof.txt"), []byte(taskID), 0o600); err != nil {
			t.Fatalf("write Task proof: %v", err)
		}
	}

	_, err = service.RetainEvidenceForContinuation(ctx, createdProject.ID, peer.ID, blackboardv2.RetainEvidenceRequest{
		IdempotencyKey: "peer-owned-attempt", Key: "evidence:peer", Attempt: "attempt:owned",
		SourcePath: "proof.txt", ArtifactType: "text", Summary: "Peer must not retain from owner Attempt",
	})
	var semanticErr *blackboardv2.Error
	if !errors.As(err, &semanticErr) || semanticErr.Code != "authority_denied" {
		t.Fatalf("peer Retain Evidence error = %#v, want authority_denied", err)
	}
	assertNoRetainedEvidenceFiles(t, runtimeRoot)

	_, err = service.ApplyForContinuation(ctx, createdProject.ID, owner.ID, blackboardv2.ChangeBatch{
		Schema: "semantic-change-batch/v2", IdempotencyKey: "terminal-owned-attempt",
		Changes: []blackboardv2.Change{{Op: "transition", Key: "attempt:owned", Version: 1, Status: "failed", Summary: "The capture attempt failed"}},
	})
	if err != nil {
		t.Fatalf("terminalize owned Attempt: %v", err)
	}
	_, err = service.RetainEvidenceForContinuation(ctx, createdProject.ID, owner.ID, blackboardv2.RetainEvidenceRequest{
		IdempotencyKey: "terminal-owned-attempt", Key: "evidence:terminal", Attempt: "attempt:owned",
		SourcePath: "proof.txt", ArtifactType: "text", Summary: "Terminal Attempt must reject new retain",
	})
	if err == nil {
		t.Fatal("new post-terminal Retain Evidence unexpectedly succeeded")
	}
	assertNoRetainedEvidenceFiles(t, runtimeRoot)
}

func TestEvidenceMergeRollsBackWhenMissingCanonicalWouldRemoveSoleConfirmedFactBasis(t *testing.T) {
	ctx := context.Background()
	fixture := newEvidenceV2Fixture(t, "Evidence merge basis")
	fixture.writeSource(t, "merge-source.txt", "same merge proof")
	fixture.writeSource(t, "merge-canonical.txt", "same merge proof")

	_, err := fixture.service.ApplyForContinuation(ctx, fixture.projectID, fixture.continuationID, blackboardv2.ChangeBatch{
		Schema: "semantic-change-batch/v2", IdempotencyKey: "create-merge-dependent-fact",
		Changes: []blackboardv2.Change{{Op: "create", Key: "fact:merge-dependent", Type: "fact", Record: blackboardv2.FactRecord{Category: "service", Summary: "Merge-dependent conclusion", Confidence: "tentative", ScopeStatus: "in_scope"}}},
	})
	if err != nil {
		t.Fatalf("create merge-dependent Fact: %v", err)
	}
	_, err = fixture.service.RetainEvidenceForContinuation(ctx, fixture.projectID, fixture.continuationID, blackboardv2.RetainEvidenceRequest{
		IdempotencyKey: "retain-merge-basis-source", Key: "evidence:merge-basis-source", Attempt: "attempt:evidence", SourcePath: "merge-source.txt", ArtifactType: "text", Summary: "Available sole basis",
		Links: []blackboardv2.EvidenceLink{{"evidences", "fact:merge-dependent"}},
	})
	if err != nil {
		t.Fatalf("retain source Evidence basis: %v", err)
	}
	_, err = fixture.service.RetainEvidenceForContinuation(ctx, fixture.projectID, fixture.continuationID, blackboardv2.RetainEvidenceRequest{
		IdempotencyKey: "retain-merge-basis-canonical", Key: "evidence:merge-basis-canonical", Attempt: "attempt:evidence", SourcePath: "merge-canonical.txt", ArtifactType: "text", Summary: "Canonical duplicate",
	})
	if err != nil {
		t.Fatalf("retain canonical Evidence: %v", err)
	}
	_, err = fixture.service.ApplyForContinuation(ctx, fixture.projectID, fixture.continuationID, blackboardv2.ChangeBatch{
		Schema: "semantic-change-batch/v2", IdempotencyKey: "confirm-merge-dependent-fact",
		Changes: []blackboardv2.Change{{Op: "transition", Key: "fact:merge-dependent", Version: 1, Status: "confirmed"}},
	})
	if err != nil {
		t.Fatalf("confirm merge-dependent Fact: %v", err)
	}
	_, err = fixture.service.ApplyForContinuation(ctx, fixture.projectID, fixture.continuationID, blackboardv2.ChangeBatch{
		Schema: "semantic-change-batch/v2", IdempotencyKey: "mark-merge-canonical-missing",
		Changes: []blackboardv2.Change{{Op: "transition", Key: "evidence:merge-basis-canonical", Version: 1, Status: "missing", Summary: "Canonical payload is unavailable"}},
	})
	if err != nil {
		t.Fatalf("mark canonical Evidence missing: %v", err)
	}
	before, err := fixture.service.RuntimeSnapshot(ctx, fixture.projectID)
	if err != nil {
		t.Fatalf("read snapshot before guarded Evidence merge: %v", err)
	}
	_, err = fixture.service.Apply(ctx, fixture.projectID, blackboardv2.ChangeBatch{
		Schema: "semantic-change-batch/v2", IdempotencyKey: "reject-missing-canonical-evidence-merge",
		Changes: []blackboardv2.Change{{Op: "merge", Source: "evidence:merge-basis-source", SourceVersion: 1, Canonical: "evidence:merge-basis-canonical", CanonicalVersion: 2}},
	})
	var semanticErr *blackboardv2.Error
	if !errors.As(err, &semanticErr) || semanticErr.Code != "semantic_validation" {
		t.Fatalf("guarded Evidence merge error = %#v, want semantic_validation", err)
	}
	after, err := fixture.service.RuntimeSnapshot(ctx, fixture.projectID)
	if err != nil {
		t.Fatalf("read snapshot after guarded Evidence merge: %v", err)
	}
	if after.Revision != before.Revision {
		t.Fatalf("failed Evidence merge revision = %d, want %d", after.Revision, before.Revision)
	}
	source, err := fixture.service.ReadCurrent(ctx, fixture.projectID, "evidence:merge-basis-source")
	if err != nil {
		t.Fatalf("read source after guarded merge: %v", err)
	}
	canonical, err := fixture.service.ReadCurrent(ctx, fixture.projectID, "evidence:merge-basis-canonical")
	if err != nil {
		t.Fatalf("read canonical after guarded merge: %v", err)
	}
	if source.Record.Status != "available" || canonical.Record.Status != "missing" {
		t.Fatalf("guarded Evidence merge mutated records: source=%#v canonical=%#v", source.Record, canonical.Record)
	}
}

func TestEvidenceReplacementReResolvesConcurrentMergeInsideSemanticCommit(t *testing.T) {
	ctx := context.Background()
	fixture := newEvidenceV2Fixture(t, "Evidence merge publication race")
	fixture.writeSource(t, "race-source.txt", "shared pre-merge proof")
	fixture.writeSource(t, "race-canonical.txt", "shared pre-merge proof")
	fixture.writeSource(t, "race-replacement.txt", "replacement proof after merge")

	_, err := fixture.service.ApplyForContinuation(ctx, fixture.projectID, fixture.continuationID, blackboardv2.ChangeBatch{
		Schema: "semantic-change-batch/v2", IdempotencyKey: "prepare-evidence-merge-race",
		Changes: []blackboardv2.Change{
			{Op: "create", Key: "attempt:evidence-merge-race", Type: "attempt", Record: blackboardv2.AttemptRecord{Status: "open", Summary: "Replace Evidence during merge"}},
			{Op: "relate", From: "attempt:evidence-merge-race", Relation: "tests", To: "objective:evidence"},
			{Op: "create", Key: "fact:evidence-merge-race", Type: "fact", Record: blackboardv2.FactRecord{Category: "service", Summary: "Replacement race target", Confidence: "tentative", ScopeStatus: "in_scope"}},
		},
	})
	if err != nil {
		t.Fatalf("prepare Evidence merge race: %v", err)
	}
	for _, evidence := range []struct{ key, source, idempotency string }{
		{"evidence:race-source", "race-source.txt", "retain-race-source"},
		{"evidence:race-canonical", "race-canonical.txt", "retain-race-canonical"},
	} {
		_, err := fixture.service.RetainEvidenceForContinuation(ctx, fixture.projectID, fixture.continuationID, blackboardv2.RetainEvidenceRequest{
			IdempotencyKey: evidence.idempotency, Key: evidence.key, Attempt: "attempt:evidence", SourcePath: evidence.source, ArtifactType: "text", Summary: "Pre-merge duplicate Evidence",
		})
		if err != nil {
			t.Fatalf("retain %s: %v", evidence.key, err)
		}
	}
	injector := &evidenceMergeRaceInjector{
		service: fixture.service, projectID: fixture.projectID, idempotencyKey: "merge-during-evidence-publication",
		source: "evidence:race-source", sourceVersion: 1, canonical: "evidence:race-canonical", canonicalVersion: 1,
	}
	racingService := blackboardv2.NewServiceWithEvidence(fixture.db, blackboardv2.EvidenceConfig{RuntimeRoot: fixture.runtimeRoot, ArtifactRoot: fixture.runtimeRoot, Failures: injector})
	result, err := racingService.RetainEvidenceForContinuation(ctx, fixture.projectID, fixture.continuationID, blackboardv2.RetainEvidenceRequest{
		IdempotencyKey: "replace-during-evidence-merge", Key: "evidence:race-source", Version: 1, Attempt: "attempt:evidence-merge-race", SourcePath: "race-replacement.txt", ArtifactType: "text", Summary: "Replacement committed to canonical Evidence",
		Links: []blackboardv2.EvidenceLink{{"evidences", "fact:evidence-merge-race"}},
	})
	if err != nil {
		t.Fatalf("replace Evidence across concurrent merge: %v", err)
	}
	if injector.err != nil || !injector.fired {
		t.Fatalf("merge race injector fired=%v err=%v", injector.fired, injector.err)
	}
	if len(result.Records) != 1 || result.Records[0][0] != "evidence:race-canonical" || result.Records[0][1] != 2 {
		t.Fatalf("merge-raced Evidence record result = %#v", result.Records)
	}
	if len(result.Relations) != 2 {
		t.Fatalf("merge-raced Evidence relationship result = %#v", result.Relations)
	}
	for _, relationship := range result.Relations {
		if relationship[0] == "evidence:race-source" || relationship[2] == "evidence:race-source" {
			t.Fatalf("merge-raced result exposed alias endpoint: %#v", result.Relations)
		}
	}
	detail, err := fixture.service.ReadCurrent(ctx, fixture.projectID, "evidence:race-source")
	if err != nil {
		t.Fatalf("read merge-raced Evidence through redirect: %v", err)
	}
	if detail.Key != "evidence:race-canonical" || detail.Version != 2 || detail.Record.Summary != "Replacement committed to canonical Evidence" {
		t.Fatalf("merge-raced canonical Evidence = %#v", detail)
	}
	snapshot, err := fixture.service.RuntimeSnapshot(ctx, fixture.projectID)
	if err != nil {
		t.Fatalf("read snapshot after Evidence merge race: %v", err)
	}
	for _, relationship := range snapshot.Relations {
		if relationship[0] == "evidence:race-source" || relationship[2] == "evidence:race-source" {
			t.Fatalf("snapshot retained alias relationship endpoint: %#v", snapshot.Relations)
		}
	}
}
