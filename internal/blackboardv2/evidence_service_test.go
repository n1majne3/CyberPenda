package blackboardv2_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

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
	ownerTask, err := tasks.Create(task.CreateRequest{ProjectID: createdProject.ID, Goal: "Own Evidence", Runner: task.RunnerSandbox})
	if err != nil {
		t.Fatalf("create owner Task: %v", err)
	}
	owner, err := tasks.CreateContinuation(ownerTask.ID, "profile-owner", "codex", task.RunnerSandbox)
	if err != nil {
		t.Fatalf("create owner Continuation: %v", err)
	}
	peerTask, err := tasks.Create(task.CreateRequest{ProjectID: createdProject.ID, Goal: "Peer Evidence", Runner: task.RunnerSandbox})
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

func assertNoRetainedEvidenceFiles(t *testing.T, runtimeRoot string) {
	t.Helper()
	matches := retainedEvidenceFiles(t, runtimeRoot)
	if len(matches) != 0 {
		t.Fatalf("rejected retain published managed files: %v", matches)
	}
}

func retainedEvidenceFiles(t *testing.T, artifactRoot string) []string {
	t.Helper()
	patterns := []string{
		filepath.Join(artifactRoot, "projects", "*", "retained", "*", "*"),
		filepath.Join(artifactRoot, "*", "artifacts", "retained", "*", "*"),
	}
	matches := []string{}
	for _, pattern := range patterns {
		found, err := filepath.Glob(pattern)
		if err != nil {
			t.Fatalf("glob retained Evidence: %v", err)
		}
		matches = append(matches, found...)
	}
	return matches
}

func globEvidenceTemps(t *testing.T, directory, pattern string) []string {
	t.Helper()
	matches, err := filepath.Glob(filepath.Join(directory, pattern))
	if err != nil {
		t.Fatalf("glob publication temps: %v", err)
	}
	return matches
}

func TestRuntimeRetainsEvidenceFromOwnedOpenAttemptWithCompactSnapshotAndDetailedIntegrity(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	db, err := store.Open(filepath.Join(root, "pentest.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	projects := project.NewService(db)
	createdProject, err := projects.Create("Retained Evidence", "", project.Scope{}, project.Defaults{})
	if err != nil {
		t.Fatalf("create Project: %v", err)
	}
	tasks := task.NewService(db, projects)
	createdTask, err := tasks.Create(task.CreateRequest{ProjectID: createdProject.ID, Goal: "Retain proof", Runner: task.RunnerSandbox})
	if err != nil {
		t.Fatalf("create Task: %v", err)
	}
	continuation, err := tasks.CreateContinuation(createdTask.ID, "profile-evidence", "codex", task.RunnerSandbox)
	if err != nil {
		t.Fatalf("create Continuation: %v", err)
	}
	runtimeRoot := filepath.Join(root, "runtime")
	artifactRoot := runtimeRoot
	service := blackboardv2.NewServiceWithEvidence(db, blackboardv2.EvidenceConfig{
		RuntimeRoot:  runtimeRoot,
		ArtifactRoot: artifactRoot,
	})

	_, err = service.ApplyForContinuation(ctx, createdProject.ID, continuation.ID, blackboardv2.ChangeBatch{
		Schema:         "semantic-change-batch/v2",
		IdempotencyKey: "prepare-evidence-retention",
		Changes: []blackboardv2.Change{
			{Op: "create", Key: "entity:login", Type: "entity", Record: blackboardv2.EntityRecord{Status: "active", Kind: "endpoint", Name: "Login", ScopeStatus: "in_scope"}},
			{Op: "create", Key: "objective:login", Type: "objective", Record: blackboardv2.ObjectiveRecord{Status: "open", Objective: "Verify the login boundary"}},
			{Op: "create", Key: "attempt:login", Type: "attempt", Record: blackboardv2.AttemptRecord{Status: "open", Summary: "Capture the login response"}},
			{Op: "create", Key: "fact:login", Type: "fact", Record: blackboardv2.FactRecord{Category: "authentication", Summary: "The login boundary accepted the test request", Confidence: "tentative", ScopeStatus: "in_scope"}},
			{Op: "relate", From: "attempt:login", Relation: "tests", To: "objective:login"},
		},
	})
	if err != nil {
		t.Fatalf("prepare owned Attempt: %v", err)
	}
	workdir := filepath.Join(runtimeRoot, createdTask.ID, "workdir", "captures")
	if err := os.MkdirAll(workdir, 0o700); err != nil {
		t.Fatalf("create Task workdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(workdir, "login.txt"), []byte("proof\n"), 0o600); err != nil {
		t.Fatalf("write Evidence source: %v", err)
	}

	result, err := service.RetainEvidenceForContinuation(ctx, createdProject.ID, continuation.ID, blackboardv2.RetainEvidenceRequest{
		IdempotencyKey: "retain-login-proof",
		Key:            "evidence:login",
		Attempt:        "attempt:login",
		SourcePath:     "captures/login.txt",
		ArtifactType:   "http_exchange",
		Summary:        "Captured response proving the login behavior",
		MediaType:      "application/http",
		CapturedAt:     "2026-07-17T09:30:00Z",
		Links: []blackboardv2.EvidenceLink{
			{"evidences", "fact:login"},
			{"about", "entity:login"},
		},
	})
	if err != nil {
		t.Fatalf("retain Evidence: %v", err)
	}
	if got, want := mustTupleJSON(t, result.Records), [][]any{{"evidence:login", float64(1)}}; !reflect.DeepEqual(got, want) {
		t.Fatalf("retained records = %#v, want %#v", got, want)
	}
	if got, want := mustTupleJSON(t, result.Relations), [][]any{
		{"attempt:login", "produced", "evidence:login", float64(1)},
		{"evidence:login", "about", "entity:login", float64(1)},
		{"evidence:login", "evidences", "fact:login", float64(1)},
	}; !reflect.DeepEqual(got, want) {
		t.Fatalf("retained relations = %#v, want %#v", got, want)
	}
	assertContractJSON(t, mustHarness(t), "changeResult", result)

	detail, err := service.ReadCurrent(ctx, createdProject.ID, "evidence:login")
	if err != nil {
		t.Fatalf("read retained Evidence: %v", err)
	}
	if detail.Type != "evidence" || detail.Version != 1 || detail.Record.Status != "available" ||
		detail.Record.ArtifactType != "http_exchange" || detail.Record.SourcePath != "captures/login.txt" ||
		detail.Record.SHA256 != "f6ed42a9d765eeb230a069bbc3d5dc346b2669594bb0b83cc6d14d5d967b8961" || detail.Record.Size != 6 {
		t.Fatalf("retained Evidence detail = %#v", detail)
	}
	assertContractJSON(t, mustHarness(t), "currentDetail", detail)
	managedFiles := retainedEvidenceFiles(t, artifactRoot)
	if len(managedFiles) != 1 {
		t.Fatalf("managed Evidence files = %v, want one project-owned payload", managedFiles)
	}
	managedPayload, err := os.ReadFile(managedFiles[0])
	if err != nil || string(managedPayload) != "proof\n" {
		t.Fatalf("managed Evidence payload = %q, err = %v", managedPayload, err)
	}

	snapshot, err := service.RuntimeSnapshot(ctx, createdProject.ID)
	if err != nil {
		t.Fatalf("read Runtime Snapshot: %v", err)
	}
	rawSnapshot := mustJSON(t, snapshot)
	var decoded map[string]any
	if err := json.Unmarshal(rawSnapshot, &decoded); err != nil {
		t.Fatalf("decode Runtime Snapshot: %v", err)
	}
	evidence := decoded["knowledge"].(map[string]any)["evidence"].(map[string]any)["evidence:login"].(map[string]any)
	if got, want := evidence, map[string]any{
		"version": float64(1), "status": "available", "artifact_type": "http_exchange",
		"summary": "Captured response proving the login behavior", "media_type": "application/http", "captured_at": "2026-07-17T09:30:00Z",
	}; !reflect.DeepEqual(got, want) {
		t.Fatalf("Snapshot Evidence = %#v, want allowlist %#v", got, want)
	}
	assertContractJSON(t, mustHarness(t), "runtimeSnapshot", snapshot)
}

func TestAvailableEvidenceConfirmsProjectFactThroughRuntimeService(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	db, err := store.Open(filepath.Join(root, "pentest.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	projects := project.NewService(db)
	createdProject, err := projects.Create("Evidence-backed Fact", "", project.Scope{}, project.Defaults{})
	if err != nil {
		t.Fatalf("create Project: %v", err)
	}
	tasks := task.NewService(db, projects)
	createdTask, err := tasks.Create(task.CreateRequest{ProjectID: createdProject.ID, Goal: "Confirm from proof", Runner: task.RunnerSandbox})
	if err != nil {
		t.Fatalf("create Task: %v", err)
	}
	continuation, err := tasks.CreateContinuation(createdTask.ID, "profile-proof", "codex", task.RunnerSandbox)
	if err != nil {
		t.Fatalf("create Continuation: %v", err)
	}
	runtimeRoot := filepath.Join(root, "runtime")
	service := blackboardv2.NewServiceWithEvidence(db, blackboardv2.EvidenceConfig{RuntimeRoot: runtimeRoot, ArtifactRoot: runtimeRoot})
	_, err = service.ApplyForContinuation(ctx, createdProject.ID, continuation.ID, blackboardv2.ChangeBatch{
		Schema: "semantic-change-batch/v2", IdempotencyKey: "prepare-proof-backed-fact",
		Changes: []blackboardv2.Change{
			{Op: "create", Key: "objective:proof", Type: "objective", Record: blackboardv2.ObjectiveRecord{Status: "open", Objective: "Confirm the observed response"}},
			{Op: "create", Key: "attempt:proof", Type: "attempt", Record: blackboardv2.AttemptRecord{Status: "open", Summary: "Capture the observed response"}},
			{Op: "create", Key: "fact:proof", Type: "fact", Record: blackboardv2.FactRecord{Category: "authentication", Summary: "The response exposes the authenticated account", Confidence: "tentative", ScopeStatus: "in_scope"}},
			{Op: "relate", From: "attempt:proof", Relation: "tests", To: "objective:proof"},
		},
	})
	if err != nil {
		t.Fatalf("prepare proof-backed Fact: %v", err)
	}
	workdir := filepath.Join(runtimeRoot, createdTask.ID, "workdir")
	if err := os.MkdirAll(workdir, 0o700); err != nil {
		t.Fatalf("create workdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(workdir, "response.txt"), []byte("account=admin\n"), 0o600); err != nil {
		t.Fatalf("write proof: %v", err)
	}
	if _, err := service.RetainEvidenceForContinuation(ctx, createdProject.ID, continuation.ID, blackboardv2.RetainEvidenceRequest{
		IdempotencyKey: "retain-fact-proof", Key: "evidence:proof", Attempt: "attempt:proof",
		SourcePath: "response.txt", ArtifactType: "http_exchange", Summary: "Authenticated response body",
		Links: []blackboardv2.EvidenceLink{{"evidences", "fact:proof"}},
	}); err != nil {
		t.Fatalf("retain Fact Evidence: %v", err)
	}

	confirmed, err := service.ApplyForContinuation(ctx, createdProject.ID, continuation.ID, blackboardv2.ChangeBatch{
		Schema: "semantic-change-batch/v2", IdempotencyKey: "confirm-from-evidence",
		Changes: []blackboardv2.Change{{Op: "transition", Key: "fact:proof", Version: 1, Status: "confirmed"}},
	})
	if err != nil {
		t.Fatalf("confirm Fact from available Evidence: %v", err)
	}
	if got, want := mustTupleJSON(t, confirmed.Records), [][]any{{"fact:proof", float64(2)}}; !reflect.DeepEqual(got, want) {
		t.Fatalf("confirmed Fact records = %#v, want %#v", got, want)
	}
	if _, err := service.ApplyForContinuation(ctx, createdProject.ID, continuation.ID, blackboardv2.ChangeBatch{
		Schema: "semantic-change-batch/v2", IdempotencyKey: "stale-confirm-from-evidence",
		Changes: []blackboardv2.Change{{Op: "transition", Key: "fact:proof", Version: 1, Status: "tentative"}},
	}); !isSemanticCode(err, "version_conflict") {
		t.Fatalf("stale Evidence-backed transition error = %#v, want version_conflict", err)
	}
}

func TestFactConfirmationRequiresProjectOwnedImmutableEvidenceIntegrity(t *testing.T) {
	fixture := newEvidenceV2Fixture(t, "Evidence Integrity Confirmation")
	_, err := fixture.service.ApplyForContinuation(context.Background(), fixture.projectID, fixture.continuationID, blackboardv2.ChangeBatch{
		Schema: "semantic-change-batch/v2", IdempotencyKey: "create-integrity-fact",
		Changes: []blackboardv2.Change{{Op: "create", Key: "fact:integrity", Type: "fact", Record: blackboardv2.FactRecord{
			Category: "authentication", Summary: "The retained payload establishes the conclusion", Confidence: "tentative", ScopeStatus: "in_scope",
		}}},
	})
	if err != nil {
		t.Fatalf("create integrity Fact: %v", err)
	}
	fixture.writeSource(t, "integrity.txt", "trusted payload\n")
	if _, err := fixture.service.RetainEvidenceForContinuation(context.Background(), fixture.projectID, fixture.continuationID, blackboardv2.RetainEvidenceRequest{
		IdempotencyKey: "retain-integrity", Key: "evidence:integrity", Attempt: "attempt:evidence",
		SourcePath: "integrity.txt", ArtifactType: "text", Summary: "Integrity-checked retained payload",
		Links: []blackboardv2.EvidenceLink{{"evidences", "fact:integrity"}},
	}); err != nil {
		t.Fatalf("retain integrity Evidence: %v", err)
	}
	detail, err := fixture.service.ReadCurrent(context.Background(), fixture.projectID, "evidence:integrity")
	if err != nil {
		t.Fatalf("read integrity Evidence: %v", err)
	}
	if strings.Contains(detail.Record.ManagedPath, fixture.taskID) || strings.Contains(detail.Record.ManagedPath, fixture.projectID) || !strings.HasPrefix(detail.Record.ManagedPath, "artifacts/retained/") {
		t.Fatalf("managed detail path exposes authority identity or is not Project-relative: %q", detail.Record.ManagedPath)
	}
	managedFiles := retainedEvidenceFiles(t, fixture.runtimeRoot)
	if len(managedFiles) != 1 {
		t.Fatalf("project-owned managed files = %v", managedFiles)
	}
	if relative, ok := relativeWithinTestRoot(filepath.Join(fixture.runtimeRoot, fixture.taskID), managedFiles[0]); ok {
		t.Fatalf("managed Evidence remained Runtime-writable under Task mount as %q", relative)
	}
	info, err := os.Stat(managedFiles[0])
	if err != nil || info.Mode().Perm()&0o222 != 0 {
		t.Fatalf("managed Evidence mode = %v, err = %v, want no write bits", info, err)
	}
	if err := os.Chmod(managedFiles[0], 0o600); err != nil {
		t.Fatalf("make managed payload writable for tamper simulation: %v", err)
	}
	if err := os.WriteFile(managedFiles[0], []byte("tampered payload\n"), 0o600); err != nil {
		t.Fatalf("tamper managed payload: %v", err)
	}
	if _, err := fixture.service.ApplyForContinuation(context.Background(), fixture.projectID, fixture.continuationID, blackboardv2.ChangeBatch{
		Schema: "semantic-change-batch/v2", IdempotencyKey: "reject-tampered-confirmation",
		Changes: []blackboardv2.Change{{Op: "transition", Key: "fact:integrity", Version: 1, Status: "confirmed"}},
	}); !isSemanticCode(err, "semantic_validation") {
		t.Fatalf("tampered Evidence confirmation error = %#v, want semantic_validation", err)
	}
	fact, err := fixture.service.ReadCurrent(context.Background(), fixture.projectID, "fact:integrity")
	if err != nil || fact.Record.Confidence != "tentative" || fact.Version != 1 {
		t.Fatalf("tampered Evidence partially confirmed Fact: %#v, err = %v", fact, err)
	}
}

func relativeWithinTestRoot(root, path string) (string, bool) {
	relative, err := filepath.Rel(root, path)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) || filepath.IsAbs(relative) {
		return "", false
	}
	return relative, true
}

func TestMissingEvidenceCannotConfirmFactAndLifecycleUsesCurrentVersion(t *testing.T) {
	fixture := newEvidenceV2Fixture(t, "Missing Evidence")
	ctx := context.Background()
	_, err := fixture.service.ApplyForContinuation(ctx, fixture.projectID, fixture.continuationID, blackboardv2.ChangeBatch{
		Schema: "semantic-change-batch/v2", IdempotencyKey: "create-missing-proof-fact",
		Changes: []blackboardv2.Change{{Op: "create", Key: "fact:missing-proof", Type: "fact", Record: blackboardv2.FactRecord{
			Category: "authentication", Summary: "The unavailable response proves the conclusion", Confidence: "tentative", ScopeStatus: "in_scope",
		}}},
	})
	if err != nil {
		t.Fatalf("create tentative Fact: %v", err)
	}
	fixture.writeSource(t, "missing.txt", "temporary proof\n")
	if _, err := fixture.service.RetainEvidenceForContinuation(ctx, fixture.projectID, fixture.continuationID, blackboardv2.RetainEvidenceRequest{
		IdempotencyKey: "retain-missing-proof", Key: "evidence:missing-proof", Attempt: "attempt:evidence",
		SourcePath: "missing.txt", ArtifactType: "text", Summary: "Proof before its managed payload became unavailable",
		Links: []blackboardv2.EvidenceLink{{"evidences", "fact:missing-proof"}},
	}); err != nil {
		t.Fatalf("retain Evidence: %v", err)
	}

	missing, err := fixture.service.ApplyForContinuation(ctx, fixture.projectID, fixture.continuationID, blackboardv2.ChangeBatch{
		Schema: "semantic-change-batch/v2", IdempotencyKey: "mark-proof-missing",
		Changes: []blackboardv2.Change{{Op: "transition", Key: "evidence:missing-proof", Version: 1, Status: "missing", Summary: "The retained payload is no longer available"}},
	})
	if err != nil {
		t.Fatalf("mark Evidence missing: %v", err)
	}
	if got, want := mustTupleJSON(t, missing.Records), [][]any{{"evidence:missing-proof", float64(2)}}; !reflect.DeepEqual(got, want) {
		t.Fatalf("missing Evidence records = %#v, want %#v", got, want)
	}
	detail, err := fixture.service.ReadCurrent(ctx, fixture.projectID, "evidence:missing-proof")
	if err != nil {
		t.Fatalf("read missing Evidence: %v", err)
	}
	if detail.Record.Status != "missing" || detail.Record.Summary != "The retained payload is no longer available" || detail.Record.SHA256 == "" || detail.Record.ManagedPath == "" {
		t.Fatalf("missing Evidence detail = %#v", detail)
	}
	if _, err := fixture.service.ApplyForContinuation(ctx, fixture.projectID, fixture.continuationID, blackboardv2.ChangeBatch{
		Schema: "semantic-change-batch/v2", IdempotencyKey: "stale-missing-proof",
		Changes: []blackboardv2.Change{{Op: "transition", Key: "evidence:missing-proof", Version: 1, Status: "missing", Summary: "Stale lifecycle write"}},
	}); !isSemanticCode(err, "version_conflict") {
		t.Fatalf("stale Evidence lifecycle error = %#v, want version_conflict", err)
	}
	if _, err := fixture.service.ApplyForContinuation(ctx, fixture.projectID, fixture.continuationID, blackboardv2.ChangeBatch{
		Schema: "semantic-change-batch/v2", IdempotencyKey: "reject-missing-proof-confirmation",
		Changes: []blackboardv2.Change{{Op: "transition", Key: "fact:missing-proof", Version: 1, Status: "confirmed"}},
	}); !isSemanticCode(err, "semantic_validation") {
		t.Fatalf("missing Evidence confirmation error = %#v, want semantic_validation", err)
	}
	fact, err := fixture.service.ReadCurrent(ctx, fixture.projectID, "fact:missing-proof")
	if err != nil || fact.Record.Confidence != "tentative" || fact.Version != 1 {
		t.Fatalf("Fact changed after rejected missing-Evidence confirmation: %#v, err = %v", fact, err)
	}
}

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
	foreignTask, err := tasks.Create(task.CreateRequest{ProjectID: foreignProject.ID, Goal: "Foreign Evidence", Runner: task.RunnerSandbox})
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

func TestReservedAndPublishedRetainResumeAfterProducingAttemptTerminal(t *testing.T) {
	for _, point := range []blackboardv2.EvidenceFailurePoint{
		blackboardv2.EvidenceFailureBeforeFilePublish,
		blackboardv2.EvidenceFailureAfterFileRename,
		blackboardv2.EvidenceFailureBeforePublishStore,
		blackboardv2.EvidenceFailureAfterFilePublish,
	} {
		t.Run(string(point), func(t *testing.T) {
			fixture := newEvidenceV2Fixture(t, "Terminal Resume "+string(point))
			injector := &failEvidenceV2Once{point: point}
			fixture.service = blackboardv2.NewServiceWithEvidence(fixture.db, blackboardv2.EvidenceConfig{
				RuntimeRoot: fixture.runtimeRoot, ArtifactRoot: fixture.runtimeRoot, Failures: injector,
			})
			fixture.writeSource(t, "terminal-resume.txt", "durably reserved proof\n")
			request := blackboardv2.RetainEvidenceRequest{
				IdempotencyKey: "terminal-resume-" + string(point), Key: "evidence:terminal-resume-" + string(point), Attempt: "attempt:evidence",
				SourcePath: "terminal-resume.txt", ArtifactType: "text", Summary: "Proof reserved before the Attempt became terminal",
			}
			if _, err := fixture.service.RetainEvidenceForContinuation(context.Background(), fixture.projectID, fixture.continuationID, request); err == nil {
				t.Fatal("injected durable retention failure unexpectedly succeeded")
			}
			var journalStatus, internalPath string
			var payloadOwned int
			if err := fixture.db.QueryRow(`SELECT status,managed_internal_path,payload_owned FROM blackboard_v2_evidence_requests WHERE project_id=? AND continuation_id=? AND idempotency_key=?`, fixture.projectID, fixture.continuationID, request.IdempotencyKey).Scan(&journalStatus, &internalPath, &payloadOwned); err != nil {
				t.Fatalf("read durable Evidence journal: %v", err)
			}
			if internalPath == "" || strings.Contains(internalPath, fixture.taskID) {
				t.Fatalf("journal planned path = %q, want private Project-owned path without Task ID", internalPath)
			}
			if point == blackboardv2.EvidenceFailureBeforeFilePublish && (journalStatus != "reserved" || payloadOwned != 0) {
				t.Fatalf("pre-publish journal = status %q owned %d, want reserved/0", journalStatus, payloadOwned)
			}
			if (point == blackboardv2.EvidenceFailureAfterFileRename || point == blackboardv2.EvidenceFailureBeforePublishStore) && (journalStatus != "reserved" || payloadOwned != 1) {
				t.Fatalf("post-rename journal = status %q owned %d, want reserved/1", journalStatus, payloadOwned)
			}
			if point == blackboardv2.EvidenceFailureAfterFilePublish && (journalStatus != "published" || payloadOwned != 1) {
				t.Fatalf("published journal = status %q owned %d, want published/1", journalStatus, payloadOwned)
			}
			if _, err := fixture.service.ApplyForContinuation(context.Background(), fixture.projectID, fixture.continuationID, blackboardv2.ChangeBatch{
				Schema: "semantic-change-batch/v2", IdempotencyKey: "terminalize-" + string(point),
				Changes: []blackboardv2.Change{{Op: "transition", Key: "attempt:evidence", Version: 1, Status: "failed", Summary: "The producing Attempt ended after durable Evidence reservation"}},
			}); err != nil {
				t.Fatalf("terminalize producing Attempt: %v", err)
			}
			if point == blackboardv2.EvidenceFailureAfterFileRename || point == blackboardv2.EvidenceFailureBeforePublishStore || point == blackboardv2.EvidenceFailureAfterFilePublish {
				if err := os.Remove(filepath.Join(fixture.workdir, "terminal-resume.txt")); err != nil {
					t.Fatalf("remove source after durable publication: %v", err)
				}
			}
			result, err := fixture.service.RetainEvidenceForContinuation(context.Background(), fixture.projectID, fixture.continuationID, request)
			if err != nil {
				t.Fatalf("resume durable Retain Evidence after terminal Attempt: %v", err)
			}
			if got, want := mustTupleJSON(t, result.Records), [][]any{{request.Key, float64(1)}}; !reflect.DeepEqual(got, want) {
				t.Fatalf("resumed Evidence records = %#v, want %#v", got, want)
			}
			history, err := fixture.service.ReadHistory(context.Background(), fixture.projectID, request.Key, blackboardv2.HistoryOptions{})
			if err != nil {
				t.Fatalf("read resumed Evidence history: %v", err)
			}
			foundProduced := false
			for _, item := range history.Items {
				foundProduced = foundProduced || (item.Kind == "relationship" && item.From == "attempt:evidence" && item.Relation == "produced" && item.To == request.Key)
			}
			if !foundProduced {
				t.Fatalf("resumed Evidence history lacks terminal Attempt production: %#v", history.Items)
			}
		})
	}
}

func TestRetainEvidenceRestartRecoversDeterministicJournaledTempAtEveryPublicationBoundary(t *testing.T) {
	for _, point := range []blackboardv2.EvidenceFailurePoint{
		blackboardv2.EvidenceFailureBeforeTempCopy,
		blackboardv2.EvidenceFailureMidTempCopy,
		blackboardv2.EvidenceFailureAfterTempSync,
		blackboardv2.EvidenceFailureBeforeFileRename,
		blackboardv2.EvidenceFailureAfterFileRename,
	} {
		t.Run(string(point), func(t *testing.T) {
			fixture := newEvidenceV2Fixture(t, "Restart Temp Recovery "+string(point))
			injector := &failEvidenceV2Once{point: point}
			fixture.service = blackboardv2.NewServiceWithEvidence(fixture.db, blackboardv2.EvidenceConfig{
				RuntimeRoot: fixture.runtimeRoot, ArtifactRoot: fixture.runtimeRoot, Failures: injector,
			})
			payload := strings.Repeat("restart-safe-proof-", 8192)
			fixture.writeSource(t, "restart.txt", payload)
			request := blackboardv2.RetainEvidenceRequest{
				IdempotencyKey: "restart-" + string(point), Key: "evidence:restart-" + string(point), Attempt: "attempt:evidence",
				SourcePath: "restart.txt", ArtifactType: "text", Summary: "Proof recovered after a publication crash",
			}
			if _, err := fixture.service.RetainEvidenceForContinuation(context.Background(), fixture.projectID, fixture.continuationID, request); err == nil {
				t.Fatal("injected publication crash unexpectedly succeeded")
			}
			var requestHash, tempPath, finalPath, status string
			if err := fixture.db.QueryRow(`SELECT request_hash,temp_internal_path,managed_internal_path,status FROM blackboard_v2_evidence_requests WHERE project_id=? AND continuation_id=? AND idempotency_key=?`, fixture.projectID, fixture.continuationID, request.IdempotencyKey).Scan(&requestHash, &tempPath, &finalPath, &status); err != nil {
				t.Fatalf("read restart journal: %v", err)
			}
			wantTempPath := finalPath + ".stage-" + requestHash[:24]
			if tempPath != wantTempPath || filepath.Dir(tempPath) != filepath.Dir(finalPath) || status != "reserved" {
				t.Fatalf("restart journal temp=%q final=%q status=%q", tempPath, finalPath, status)
			}
			trackedTemp := filepath.Join(fixture.runtimeRoot, filepath.FromSlash(tempPath))
			temps := append(globEvidenceTemps(t, filepath.Dir(trackedTemp), ".retain-*"), globEvidenceTemps(t, filepath.Dir(trackedTemp), "*.stage-*")...)
			for _, candidate := range temps {
				if candidate != trackedTemp {
					t.Fatalf("raw unjournaled temp survived crash: tracked %q candidates %v", trackedTemp, temps)
				}
			}
			if err := fixture.db.Close(); err != nil {
				t.Fatalf("close store for restart: %v", err)
			}
			reopened, err := store.Open(fixture.dbPath)
			if err != nil {
				t.Fatalf("reopen store after publication crash: %v", err)
			}
			t.Cleanup(func() { _ = reopened.Close() })
			service := blackboardv2.NewServiceWithEvidence(reopened, blackboardv2.EvidenceConfig{RuntimeRoot: fixture.runtimeRoot, ArtifactRoot: fixture.runtimeRoot})
			if point == blackboardv2.EvidenceFailureAfterTempSync || point == blackboardv2.EvidenceFailureBeforeFileRename || point == blackboardv2.EvidenceFailureAfterFileRename {
				if err := os.Remove(filepath.Join(fixture.workdir, "restart.txt")); err != nil {
					t.Fatalf("remove source before restart recovery: %v", err)
				}
			}
			if _, err := service.RetainEvidenceForContinuation(context.Background(), fixture.projectID, fixture.continuationID, request); err != nil {
				t.Fatalf("recover Retain Evidence after restart: %v", err)
			}
			if _, err := os.Lstat(trackedTemp); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("journaled temp remained after recovery: err=%v path=%q", err, trackedTemp)
			}
			files := retainedEvidenceFiles(t, fixture.runtimeRoot)
			if len(files) != 1 {
				t.Fatalf("restart recovery final payloads = %v, want one", files)
			}
			got, err := os.ReadFile(files[0])
			if err != nil || string(got) != payload {
				t.Fatalf("recovered payload size=%d err=%v", len(got), err)
			}
		})
	}
}

func TestConcurrentExactRetainRetryCannotReplaceActivePublisherTemp(t *testing.T) {
	fixture := newEvidenceV2Fixture(t, "Concurrent Exact Evidence Publisher")
	payload := strings.Repeat("inode-bound-publication-proof-", 8192)
	fixture.writeSource(t, "overlap.txt", payload)
	barrier := &evidenceMidCopyBarrier{reached: make(chan struct{}), release: make(chan struct{})}
	service := blackboardv2.NewServiceWithEvidence(fixture.db, blackboardv2.EvidenceConfig{
		RuntimeRoot: fixture.runtimeRoot, ArtifactRoot: fixture.runtimeRoot, Failures: barrier,
	})
	request := blackboardv2.RetainEvidenceRequest{
		IdempotencyKey: "retain-overlapping-exact", Key: "evidence:overlapping-exact", Attempt: "attempt:evidence",
		SourcePath: "overlap.txt", ArtifactType: "text", Summary: "Concurrent exact retries share one publisher",
	}
	firstResult := make(chan error, 1)
	go func() {
		_, err := service.RetainEvidenceForContinuation(context.Background(), fixture.projectID, fixture.continuationID, request)
		firstResult <- err
	}()
	select {
	case <-barrier.reached:
	case <-time.After(5 * time.Second):
		t.Fatal("first publisher did not reach controlled mid-copy boundary")
	}
	secondResult := make(chan error, 1)
	go func() {
		_, err := service.RetainEvidenceForContinuation(context.Background(), fixture.projectID, fixture.continuationID, request)
		secondResult <- err
	}()
	var secondErr error
	select {
	case secondErr = <-secondResult:
	case <-time.After(5 * time.Second):
		close(barrier.release)
		t.Fatal("overlapping exact retry did not return while publisher was active")
	}
	var retryable *blackboardv2.Error
	if !errors.As(secondErr, &retryable) || retryable.Code != "evidence_publication_in_progress" || !retryable.Retryable {
		close(barrier.release)
		t.Fatalf("overlapping exact retry error = %#v, want retryable publisher claim", secondErr)
	}
	var tempPath, publisherToken, publisherIdentity string
	if err := fixture.db.QueryRow(`SELECT temp_internal_path,publisher_token,publisher_temp_identity FROM blackboard_v2_evidence_requests WHERE project_id=? AND continuation_id=? AND idempotency_key=?`, fixture.projectID, fixture.continuationID, request.IdempotencyKey).Scan(&tempPath, &publisherToken, &publisherIdentity); err != nil {
		close(barrier.release)
		t.Fatalf("read active publisher temp path: %v", err)
	}
	if publisherToken == "" || publisherIdentity == "" {
		close(barrier.release)
		t.Fatalf("active publisher claim = token %q identity %q, want durable owner", publisherToken, publisherIdentity)
	}
	partial, err := os.Stat(filepath.Join(fixture.runtimeRoot, filepath.FromSlash(tempPath)))
	if err != nil || partial.Size() == 0 || partial.Size() >= int64(len(payload)) {
		close(barrier.release)
		t.Fatalf("active publisher temp size=%v err=%v", func() int64 {
			if partial == nil {
				return -1
			}
			return partial.Size()
		}(), err)
	}
	close(barrier.release)
	if err := <-firstResult; err != nil {
		t.Fatalf("active publisher failed after overlapping retry: %v", err)
	}
	if err := fixture.db.QueryRow(`SELECT publisher_token,publisher_temp_identity FROM blackboard_v2_evidence_requests WHERE project_id=? AND continuation_id=? AND idempotency_key=?`, fixture.projectID, fixture.continuationID, request.IdempotencyKey).Scan(&publisherToken, &publisherIdentity); err != nil {
		t.Fatalf("read completed publisher claim: %v", err)
	}
	if publisherToken != "" || publisherIdentity != "" {
		t.Fatalf("completed publisher claim = token %q identity %q, want cleared", publisherToken, publisherIdentity)
	}
	if _, err := service.RetainEvidenceForContinuation(context.Background(), fixture.projectID, fixture.continuationID, request); err != nil {
		t.Fatalf("exact replay after active publisher completed: %v", err)
	}
	files := retainedEvidenceFiles(t, fixture.runtimeRoot)
	if len(files) != 1 {
		t.Fatalf("overlapping exact retry payloads = %v, want one final", files)
	}
	got, err := os.ReadFile(files[0])
	if err != nil || string(got) != payload {
		t.Fatalf("overlapping exact retry final size=%d err=%v", len(got), err)
	}
}

func TestV24UpgradeAdoptsSyncedLegacyTempForTerminalAttemptReplay(t *testing.T) {
	fixture := newEvidenceV2Fixture(t, "V24 Evidence Temp Upgrade")
	payload := strings.Repeat("legacy-v24-synced-proof-", 4096)
	fixture.writeSource(t, "legacy.txt", payload)
	request := blackboardv2.RetainEvidenceRequest{
		IdempotencyKey: "retain-v24-upgrade", Key: "evidence:v24-upgrade", Attempt: "attempt:evidence",
		SourcePath: "legacy.txt", ArtifactType: "text", Summary: "Legacy v24 crash payload",
	}
	failing := blackboardv2.NewServiceWithEvidence(fixture.db, blackboardv2.EvidenceConfig{
		RuntimeRoot: fixture.runtimeRoot, ArtifactRoot: fixture.runtimeRoot,
		Failures: &failEvidenceV2Once{point: blackboardv2.EvidenceFailureAfterTempSync},
	})
	if _, err := failing.RetainEvidenceForContinuation(context.Background(), fixture.projectID, fixture.continuationID, request); err == nil {
		t.Fatal("v24 fixture publication crash unexpectedly succeeded")
	}
	if _, err := fixture.service.ApplyForContinuation(context.Background(), fixture.projectID, fixture.continuationID, blackboardv2.ChangeBatch{
		Schema: "semantic-change-batch/v2", IdempotencyKey: "terminalize-v24-upgrade-attempt",
		Changes: []blackboardv2.Change{{
			Op: "transition", Key: "attempt:evidence", Version: 1, Status: "failed", Summary: "Attempt ended after v24 temp sync",
		}},
	}); err != nil {
		t.Fatalf("terminalize v24 producing Attempt: %v", err)
	}
	var tempPath, finalPath string
	if err := fixture.db.QueryRow(`SELECT temp_internal_path,managed_internal_path FROM blackboard_v2_evidence_requests WHERE project_id=? AND continuation_id=? AND idempotency_key=?`, fixture.projectID, fixture.continuationID, request.IdempotencyKey).Scan(&tempPath, &finalPath); err != nil {
		t.Fatalf("read v24 fixture publication paths: %v", err)
	}
	absoluteTemp := filepath.Join(fixture.runtimeRoot, filepath.FromSlash(tempPath))
	legacyTemp := filepath.Join(filepath.Dir(absoluteTemp), ".retain-v24-crash-state")
	if err := os.Rename(absoluteTemp, legacyTemp); err != nil {
		t.Fatalf("convert deterministic temp to v24 random temp: %v", err)
	}
	currentVersionTemp := filepath.Join(filepath.Dir(absoluteTemp), filepath.Base(finalPath)+".stage-active-current-version")
	if err := os.WriteFile(currentVersionTemp, []byte("active current-version publisher bytes"), 0o600); err != nil {
		t.Fatalf("write current-version temp sweep sentinel: %v", err)
	}
	directory, err := os.Open(filepath.Dir(legacyTemp))
	if err != nil {
		t.Fatalf("open v24 temp directory: %v", err)
	}
	if err := directory.Sync(); err != nil {
		_ = directory.Close()
		t.Fatalf("sync v24 temp rename: %v", err)
	}
	if err := directory.Close(); err != nil {
		t.Fatalf("close v24 temp directory: %v", err)
	}
	if _, err := fixture.db.Exec(`DROP TABLE blackboard_v2_evidence_payloads`); err != nil {
		t.Fatalf("remove post-v24 payload claims: %v", err)
	}
	if _, err := fixture.db.Exec(`UPDATE blackboard_v2_evidence_requests SET temp_internal_path='',publisher_token='',publisher_temp_identity='' WHERE project_id=? AND continuation_id=? AND idempotency_key=?`, fixture.projectID, fixture.continuationID, request.IdempotencyKey); err != nil {
		t.Fatalf("restore v24 request shape: %v", err)
	}
	if _, err := fixture.db.Exec(`DELETE FROM schema_migrations WHERE version>=25`); err != nil {
		t.Fatalf("restore v24 migration ledger: %v", err)
	}
	if err := fixture.db.Close(); err != nil {
		t.Fatalf("close v24 crash fixture: %v", err)
	}
	if err := os.Remove(filepath.Join(fixture.workdir, "legacy.txt")); err != nil {
		t.Fatalf("remove v24 source before terminal replay: %v", err)
	}
	reopened, err := store.Open(fixture.dbPath)
	if err != nil {
		t.Fatalf("upgrade v24 crash fixture: %v", err)
	}
	adopting := blackboardv2.NewServiceWithEvidence(reopened, blackboardv2.EvidenceConfig{
		RuntimeRoot: fixture.runtimeRoot, ArtifactRoot: fixture.runtimeRoot,
		Failures: &failEvidenceV2Once{point: blackboardv2.EvidenceFailureAfterTempSync},
	})
	if _, err := adopting.RetainEvidenceForContinuation(context.Background(), fixture.projectID, fixture.continuationID, request); err == nil {
		t.Fatal("adopted v24 temp fsync crash unexpectedly succeeded")
	}
	if _, err := os.Lstat(legacyTemp); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("v24 adoption left legacy temp after fsync: %v", err)
	}
	if info, err := os.Stat(filepath.Join(fixture.runtimeRoot, filepath.FromSlash(tempPath))); err != nil || info.Size() != int64(len(payload)) {
		t.Fatalf("adopted deterministic temp size=%v err=%v", func() int64 {
			if info == nil {
				return -1
			}
			return info.Size()
		}(), err)
	}
	if err := reopened.Close(); err != nil {
		t.Fatalf("close upgraded store after adopted temp fsync: %v", err)
	}
	reopened, err = store.Open(fixture.dbPath)
	if err != nil {
		t.Fatalf("restart after adopted v24 temp fsync: %v", err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	restarted := blackboardv2.NewServiceWithEvidence(reopened, blackboardv2.EvidenceConfig{RuntimeRoot: fixture.runtimeRoot, ArtifactRoot: fixture.runtimeRoot})
	result, err := restarted.RetainEvidenceForContinuation(context.Background(), fixture.projectID, fixture.continuationID, request)
	if err != nil {
		t.Fatalf("terminal replay from upgraded v24 temp: %v", err)
	}
	if got, want := mustTupleJSON(t, result.Records), [][]any{{request.Key, float64(1)}}; !reflect.DeepEqual(got, want) {
		t.Fatalf("upgraded v24 replay records = %#v, want %#v", got, want)
	}
	for _, path := range []string{legacyTemp, filepath.Join(fixture.runtimeRoot, filepath.FromSlash(tempPath))} {
		if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("upgraded v24 replay left temp %q: %v", path, err)
		}
	}
	currentBytes, err := os.ReadFile(currentVersionTemp)
	if err != nil || string(currentBytes) != "active current-version publisher bytes" {
		t.Fatalf("v24 sweep changed current-version temp = %q err=%v", currentBytes, err)
	}
	if err := os.Remove(currentVersionTemp); err != nil {
		t.Fatalf("remove current-version temp sweep sentinel: %v", err)
	}
	finalBytes, err := os.ReadFile(filepath.Join(fixture.runtimeRoot, filepath.FromSlash(finalPath)))
	if err != nil || string(finalBytes) != payload {
		t.Fatalf("upgraded v24 final size=%d err=%v", len(finalBytes), err)
	}
	history, err := restarted.ReadHistory(context.Background(), fixture.projectID, request.Key, blackboardv2.HistoryOptions{})
	if err != nil {
		t.Fatalf("read upgraded v24 Evidence history: %v", err)
	}
	foundProduced := false
	for _, item := range history.Items {
		foundProduced = foundProduced || (item.Kind == "relationship" && item.From == request.Attempt && item.Relation == "produced" && item.To == request.Key)
	}
	if !foundProduced {
		t.Fatalf("upgraded v24 Evidence lacks produced history: %#v", history.Items)
	}
}

type evidenceMidCopyBarrier struct {
	mu      sync.Mutex
	reached chan struct{}
	release chan struct{}
	blocked bool
}

func (barrier *evidenceMidCopyBarrier) FailAfter(point blackboardv2.EvidenceFailurePoint) error {
	if point != blackboardv2.EvidenceFailureMidTempCopy {
		return nil
	}
	barrier.mu.Lock()
	if barrier.blocked {
		barrier.mu.Unlock()
		return nil
	}
	barrier.blocked = true
	close(barrier.reached)
	barrier.mu.Unlock()
	<-barrier.release
	return nil
}

func TestConcurrentEvidenceReservationAtomicallyRejectsConflictingSemantics(t *testing.T) {
	fixture := newEvidenceV2Fixture(t, "Concurrent Evidence Reservation")
	fixture.writeSource(t, "race-a.txt", "reservation A\n")
	fixture.writeSource(t, "race-b.txt", "reservation B\n")
	barrier := newEvidenceReservationBarrier(2)
	serviceA := blackboardv2.NewServiceWithEvidence(fixture.db, blackboardv2.EvidenceConfig{RuntimeRoot: fixture.runtimeRoot, ArtifactRoot: fixture.runtimeRoot, Failures: barrier})
	serviceB := blackboardv2.NewServiceWithEvidence(fixture.db, blackboardv2.EvidenceConfig{RuntimeRoot: fixture.runtimeRoot, ArtifactRoot: fixture.runtimeRoot, Failures: barrier})
	requests := []blackboardv2.RetainEvidenceRequest{
		{IdempotencyKey: "reservation-race", Key: "evidence:race-a", Attempt: "attempt:evidence", SourcePath: "race-a.txt", ArtifactType: "text", Summary: "Reservation A"},
		{IdempotencyKey: "reservation-race", Key: "evidence:race-b", Attempt: "attempt:evidence", SourcePath: "race-b.txt", ArtifactType: "text", Summary: "Reservation B"},
	}
	services := []*blackboardv2.Service{serviceA, serviceB}
	type outcome struct {
		index  int
		result blackboardv2.ChangeResult
		err    error
	}
	outcomes := make(chan outcome, 2)
	for index := range requests {
		go func(index int) {
			result, err := services[index].RetainEvidenceForContinuation(context.Background(), fixture.projectID, fixture.continuationID, requests[index])
			outcomes <- outcome{index: index, result: result, err: err}
		}(index)
	}
	first, second := <-outcomes, <-outcomes
	results := []outcome{first, second}
	winner := -1
	for _, result := range results {
		if result.err == nil {
			if winner != -1 {
				t.Fatalf("both conflicting reservations succeeded: %#v", results)
			}
			winner = result.index
			continue
		}
		if !isSemanticCode(result.err, "idempotency_conflict") {
			t.Fatalf("reservation loser error = %#v, want idempotency_conflict", result.err)
		}
	}
	if winner == -1 {
		t.Fatalf("neither conflicting reservation succeeded: %#v", results)
	}
	if _, err := services[winner].RetainEvidenceForContinuation(context.Background(), fixture.projectID, fixture.continuationID, requests[winner]); err != nil {
		t.Fatalf("winner exact replay was poisoned: %v", err)
	}
	loser := 1 - winner
	if _, err := services[loser].RetainEvidenceForContinuation(context.Background(), fixture.projectID, fixture.continuationID, requests[loser]); !isSemanticCode(err, "idempotency_conflict") {
		t.Fatalf("loser replay error = %#v, want idempotency_conflict", err)
	}
}

type evidenceReservationBarrier struct {
	want    int
	mu      sync.Mutex
	arrived int
	release chan struct{}
}

func newEvidenceReservationBarrier(want int) *evidenceReservationBarrier {
	return &evidenceReservationBarrier{want: want, release: make(chan struct{})}
}

func (barrier *evidenceReservationBarrier) FailAfter(point blackboardv2.EvidenceFailurePoint) error {
	if point != blackboardv2.EvidenceFailureBeforeReservation {
		return nil
	}
	barrier.mu.Lock()
	barrier.arrived++
	if barrier.arrived == barrier.want {
		close(barrier.release)
	}
	barrier.mu.Unlock()
	<-barrier.release
	return nil
}

func TestRetainEvidenceConfinesSourcesAndRejectsSymlinkAndReplacementRaces(t *testing.T) {
	fixture := newEvidenceV2Fixture(t, "Evidence Source Confinement")
	outside := filepath.Join(filepath.Dir(fixture.runtimeRoot), "outside.txt")
	if err := os.WriteFile(outside, []byte("outside\n"), 0o600); err != nil {
		t.Fatalf("write outside source: %v", err)
	}
	fixture.writeSource(t, "inside.txt", "inside\n")
	if err := os.Symlink(outside, filepath.Join(fixture.workdir, "escape.txt")); err != nil {
		t.Fatalf("create escaping symlink: %v", err)
	}
	if err := os.Symlink("inside.txt", filepath.Join(fixture.workdir, "inside-link.txt")); err != nil {
		t.Fatalf("create in-root symlink: %v", err)
	}
	realDirectory := filepath.Join(fixture.workdir, "real-directory")
	if err := os.MkdirAll(realDirectory, 0o700); err != nil {
		t.Fatalf("create real source directory: %v", err)
	}
	if err := os.WriteFile(filepath.Join(realDirectory, "nested.txt"), []byte("nested\n"), 0o600); err != nil {
		t.Fatalf("write nested source: %v", err)
	}
	if err := os.Symlink("real-directory", filepath.Join(fixture.workdir, "linked-directory")); err != nil {
		t.Fatalf("create relative directory symlink: %v", err)
	}
	otherTaskSource := filepath.Join(fixture.runtimeRoot, "another-task", "workdir", "proof.txt")
	if err := os.MkdirAll(filepath.Dir(otherTaskSource), 0o700); err != nil {
		t.Fatalf("create other Task root: %v", err)
	}
	if err := os.WriteFile(otherTaskSource, []byte("other task\n"), 0o600); err != nil {
		t.Fatalf("write other Task source: %v", err)
	}
	for name, sourcePath := range map[string]string{
		"traversal":         "../../outside.txt",
		"escaping-symlink":  "escape.txt",
		"in-root-symlink":   "inside-link.txt",
		"directory-symlink": filepath.Join("linked-directory", "nested.txt"),
		"other-task":        otherTaskSource,
	} {
		t.Run(name, func(t *testing.T) {
			_, err := fixture.service.RetainEvidenceForContinuation(context.Background(), fixture.projectID, fixture.continuationID, blackboardv2.RetainEvidenceRequest{
				IdempotencyKey: "forbidden-" + name, Key: "evidence:forbidden-" + name, Attempt: "attempt:evidence",
				SourcePath: sourcePath, ArtifactType: "text", Summary: "Forbidden source must not be retained",
			})
			var semanticErr *blackboardv2.Error
			if !errors.As(err, &semanticErr) || semanticErr.Code != "evidence_source_forbidden" {
				t.Fatalf("forbidden source error = %#v, want evidence_source_forbidden", err)
			}
		})
	}
	assertNoRetainedEvidenceFiles(t, fixture.runtimeRoot)

	replacePath := filepath.Join(fixture.workdir, "replace.txt")
	if err := os.WriteFile(replacePath, []byte("original bytes\n"), 0o600); err != nil {
		t.Fatalf("write replacement-race source: %v", err)
	}
	fixture.service = blackboardv2.NewServiceWithEvidence(fixture.db, blackboardv2.EvidenceConfig{
		RuntimeRoot: fixture.runtimeRoot, ArtifactRoot: fixture.runtimeRoot, Failures: replaceEvidenceV2Source{path: replacePath},
	})
	_, err := fixture.service.RetainEvidenceForContinuation(context.Background(), fixture.projectID, fixture.continuationID, blackboardv2.RetainEvidenceRequest{
		IdempotencyKey: "replacement-race", Key: "evidence:replacement-race", Attempt: "attempt:evidence",
		SourcePath: "replace.txt", ArtifactType: "text", Summary: "Only original bytes may be retained",
	})
	var semanticErr *blackboardv2.Error
	if !errors.As(err, &semanticErr) || semanticErr.Code != "evidence_source_changed" {
		t.Fatalf("replacement race error = %#v, want evidence_source_changed", err)
	}
	if _, err := fixture.service.ReadCurrent(context.Background(), fixture.projectID, "evidence:replacement-race"); !isSemanticCode(err, "not_found") {
		t.Fatalf("replacement race Evidence read error = %#v, want not_found", err)
	}
	assertNoRetainedEvidenceFiles(t, fixture.runtimeRoot)
}

func TestRetainEvidenceConvergesAcrossPublicationSemanticAndLostResponseFailures(t *testing.T) {
	for _, point := range []blackboardv2.EvidenceFailurePoint{
		blackboardv2.EvidenceFailureBeforeFilePublish,
		blackboardv2.EvidenceFailureAfterFilePublish,
		blackboardv2.EvidenceFailureAfterGraphCommit,
		blackboardv2.EvidenceFailureAfterResultStore,
	} {
		t.Run(string(point), func(t *testing.T) {
			fixture := newEvidenceV2Fixture(t, "Evidence Failure "+string(point))
			injector := &failEvidenceV2Once{point: point}
			fixture.service = blackboardv2.NewServiceWithEvidence(fixture.db, blackboardv2.EvidenceConfig{
				RuntimeRoot: fixture.runtimeRoot, ArtifactRoot: fixture.runtimeRoot, Failures: injector,
			})
			fixture.writeSource(t, "failure.txt", "one durable payload\n")
			request := blackboardv2.RetainEvidenceRequest{
				IdempotencyKey: "failure-" + string(point), Key: "evidence:failure-" + string(point), Attempt: "attempt:evidence",
				SourcePath: "failure.txt", ArtifactType: "text", Summary: "Evidence retained across an uncertain boundary",
			}
			if _, err := fixture.service.RetainEvidenceForContinuation(context.Background(), fixture.projectID, fixture.continuationID, request); err == nil {
				t.Fatal("injected Evidence failure unexpectedly succeeded")
			}
			if point == blackboardv2.EvidenceFailureBeforeFilePublish {
				assertNoRetainedEvidenceFiles(t, fixture.runtimeRoot)
			}
			first, err := fixture.service.RetainEvidenceForContinuation(context.Background(), fixture.projectID, fixture.continuationID, request)
			if err != nil {
				t.Fatalf("retry Retain Evidence: %v", err)
			}
			replay, err := fixture.service.RetainEvidenceForContinuation(context.Background(), fixture.projectID, fixture.continuationID, request)
			if err != nil {
				t.Fatalf("replay Retain Evidence: %v", err)
			}
			if !reflect.DeepEqual(first, replay) {
				t.Fatalf("Retain Evidence replay drifted:\nfirst %#v\nreplay %#v", first, replay)
			}
			detail, err := fixture.service.ReadCurrent(context.Background(), fixture.projectID, request.Key)
			if err != nil || detail.Version != 1 || detail.Record.Status != "available" {
				t.Fatalf("converged Evidence = %#v, err = %v", detail, err)
			}
			matches := retainedEvidenceFiles(t, fixture.runtimeRoot)
			if len(matches) != 1 {
				t.Fatalf("managed Evidence files = %v", matches)
			}
			payload, err := os.ReadFile(matches[0])
			if err != nil || string(payload) != "one durable payload\n" {
				t.Fatalf("managed Evidence payload = %q, err = %v", payload, err)
			}
		})
	}
}

func TestRetainEvidenceReplacementUsesCurrentVersionWithoutPublishingStalePayload(t *testing.T) {
	fixture := newEvidenceV2Fixture(t, "Evidence Replacement")
	fixture.writeSource(t, "first.txt", "first payload\n")
	firstRequest := blackboardv2.RetainEvidenceRequest{
		IdempotencyKey: "retain-first", Key: "evidence:replace", Attempt: "attempt:evidence",
		SourcePath: "first.txt", ArtifactType: "text", Summary: "First retained payload",
	}
	if _, err := fixture.service.RetainEvidenceForContinuation(context.Background(), fixture.projectID, fixture.continuationID, firstRequest); err != nil {
		t.Fatalf("retain first Evidence version: %v", err)
	}
	fixture.writeSource(t, "second.txt", "second payload\n")
	secondRequest := blackboardv2.RetainEvidenceRequest{
		IdempotencyKey: "retain-second", Key: "evidence:replace", Version: 1, Attempt: "attempt:evidence",
		SourcePath: "second.txt", ArtifactType: "text", Summary: "Second retained payload",
	}
	second, err := fixture.service.RetainEvidenceForContinuation(context.Background(), fixture.projectID, fixture.continuationID, secondRequest)
	if err != nil {
		t.Fatalf("replace retained Evidence: %v", err)
	}
	if got, want := mustTupleJSON(t, second.Records), [][]any{{"evidence:replace", float64(2)}}; !reflect.DeepEqual(got, want) {
		t.Fatalf("replacement records = %#v, want %#v", got, want)
	}
	detail, err := fixture.service.ReadCurrent(context.Background(), fixture.projectID, "evidence:replace")
	if err != nil || detail.Version != 2 || detail.Record.Summary != "Second retained payload" || detail.Record.SourcePath != "second.txt" {
		t.Fatalf("replacement detail = %#v, err = %v", detail, err)
	}
	history, err := fixture.service.ReadHistory(context.Background(), fixture.projectID, "evidence:replace", blackboardv2.HistoryOptions{})
	if err != nil {
		t.Fatalf("read replacement history: %v", err)
	}
	if len(history.Items) != 1 || history.Items[0].Record == nil || history.Items[0].Record.SourcePath != "first.txt" || history.Items[0].Record.SHA256 == detail.Record.SHA256 {
		t.Fatalf("replacement history = %#v", history)
	}
	matchesBefore := retainedEvidenceFiles(t, fixture.runtimeRoot)
	if len(matchesBefore) != 2 {
		t.Fatalf("managed replacement files before stale write = %v", matchesBefore)
	}
	noOp, err := fixture.service.RetainEvidenceForContinuation(context.Background(), fixture.projectID, fixture.continuationID, blackboardv2.RetainEvidenceRequest{
		IdempotencyKey: "retain-second-no-op", Key: "evidence:replace", Version: 2, Attempt: "attempt:evidence",
		SourcePath: "second.txt", ArtifactType: "text", Summary: "Second retained payload",
	})
	if err != nil {
		t.Fatalf("repeat semantic replacement: %v", err)
	}
	if len(noOp.Records) != 0 || len(noOp.Relations) != 0 || noOp.Revision != second.Revision {
		t.Fatalf("semantic replacement no-op = %#v, want unchanged revision %d", noOp, second.Revision)
	}

	fixture.writeSource(t, "stale.txt", "stale payload must not publish\n")
	_, err = fixture.service.RetainEvidenceForContinuation(context.Background(), fixture.projectID, fixture.continuationID, blackboardv2.RetainEvidenceRequest{
		IdempotencyKey: "retain-stale", Key: "evidence:replace", Version: 1, Attempt: "attempt:evidence",
		SourcePath: "stale.txt", ArtifactType: "text", Summary: "Stale replacement payload",
	})
	if !isSemanticCode(err, "version_conflict") {
		t.Fatalf("stale Evidence replacement error = %#v, want version_conflict", err)
	}
	matchesAfter := retainedEvidenceFiles(t, fixture.runtimeRoot)
	if !reflect.DeepEqual(matchesAfter, matchesBefore) {
		t.Fatalf("stale replacement changed managed files:\nbefore %v\nafter %v", matchesBefore, matchesAfter)
	}
}

func TestDefinitiveSemanticFailureCleansOnlyRequestOwnedPayloadAndReservation(t *testing.T) {
	fixture := newEvidenceV2Fixture(t, "Evidence Definitive Cleanup")
	fixture.writeSource(t, "current.txt", "current payload\n")
	if _, err := fixture.service.RetainEvidenceForContinuation(context.Background(), fixture.projectID, fixture.continuationID, blackboardv2.RetainEvidenceRequest{
		IdempotencyKey: "retain-current-cleanup", Key: "evidence:cleanup", Attempt: "attempt:evidence",
		SourcePath: "current.txt", ArtifactType: "text", Summary: "Current retained payload",
	}); err != nil {
		t.Fatalf("retain current Evidence: %v", err)
	}
	fixture.writeSource(t, "raced.txt", "raced replacement payload\n")
	injector := &evidenceVersionRaceInjector{
		service: fixture.service, projectID: fixture.projectID, continuationID: fixture.continuationID,
	}
	tracingService := blackboardv2.NewServiceWithEvidence(fixture.db, blackboardv2.EvidenceConfig{
		RuntimeRoot: fixture.runtimeRoot, ArtifactRoot: fixture.runtimeRoot, Failures: injector,
	})
	request := blackboardv2.RetainEvidenceRequest{
		IdempotencyKey: "retain-raced-cleanup", Key: "evidence:cleanup", Version: 1, Attempt: "attempt:evidence",
		SourcePath: "raced.txt", ArtifactType: "text", Summary: "Raced replacement payload",
	}
	if _, err := tracingService.RetainEvidenceForContinuation(context.Background(), fixture.projectID, fixture.continuationID, request); !isSemanticCode(err, "version_conflict") {
		t.Fatalf("raced replacement error = %#v, want version_conflict", err)
	}
	files := retainedEvidenceFiles(t, fixture.runtimeRoot)
	if len(files) != 1 {
		t.Fatalf("definitive failure left an orphan or removed shared payload: %v", files)
	}
	detail, err := fixture.service.ReadCurrent(context.Background(), fixture.projectID, "evidence:cleanup")
	if err != nil || detail.Version != 2 || detail.Record.Summary != "Concurrent semantic update" {
		t.Fatalf("definitive failure left partial semantic state: %#v, err = %v", detail, err)
	}
	request.Version = 2
	result, err := tracingService.RetainEvidenceForContinuation(context.Background(), fixture.projectID, fixture.continuationID, request)
	if err != nil {
		t.Fatalf("reuse cleaned reservation against current version: %v", err)
	}
	if got, want := mustTupleJSON(t, result.Records), [][]any{{"evidence:cleanup", float64(3)}}; !reflect.DeepEqual(got, want) {
		t.Fatalf("replacement after cleanup records = %#v, want %#v", got, want)
	}
}

func TestDefinitiveEvidenceCleanupRecoversDurableGCCheckpointAfterRestart(t *testing.T) {
	for _, point := range []blackboardv2.EvidenceFailurePoint{
		blackboardv2.EvidenceFailureAfterPayloadGCClaim,
		blackboardv2.EvidenceFailureAfterPayloadUnlink,
	} {
		t.Run(string(point), func(t *testing.T) {
			fixture := newEvidenceV2Fixture(t, "Evidence GC Restart "+string(point))
			fixture.writeSource(t, "current.txt", "current payload\n")
			if _, err := fixture.service.RetainEvidenceForContinuation(context.Background(), fixture.projectID, fixture.continuationID, blackboardv2.RetainEvidenceRequest{
				IdempotencyKey: "retain-current-gc", Key: "evidence:cleanup", Attempt: "attempt:evidence",
				SourcePath: "current.txt", ArtifactType: "text", Summary: "Current retained payload",
			}); err != nil {
				t.Fatalf("retain current Evidence: %v", err)
			}
			fixture.writeSource(t, "raced.txt", "payload requiring durable cleanup\n")
			injector := &evidenceCleanupCrashInjector{
				service: fixture.service, projectID: fixture.projectID, continuationID: fixture.continuationID, crashPoint: point,
			}
			service := blackboardv2.NewServiceWithEvidence(fixture.db, blackboardv2.EvidenceConfig{
				RuntimeRoot: fixture.runtimeRoot, ArtifactRoot: fixture.runtimeRoot, Failures: injector,
			})
			request := blackboardv2.RetainEvidenceRequest{
				IdempotencyKey: "retain-raced-gc", Key: "evidence:cleanup", Version: 1, Attempt: "attempt:evidence",
				SourcePath: "raced.txt", ArtifactType: "text", Summary: "Raced replacement payload",
			}
			if _, err := service.RetainEvidenceForContinuation(context.Background(), fixture.projectID, fixture.continuationID, request); !isSemanticCode(err, "version_conflict") {
				t.Fatalf("raced replacement error = %#v, want version_conflict", err)
			}
			var state, gcContinuationID, gcKey, finalPath, tempPath string
			if err := fixture.db.QueryRow(`
				SELECT payload.state,payload.gc_continuation_id,payload.gc_idempotency_key,request.managed_internal_path,request.temp_internal_path
				FROM blackboard_v2_evidence_requests request
				JOIN blackboard_v2_evidence_payloads payload
				  ON payload.project_id=request.project_id AND payload.managed_internal_path=request.managed_internal_path
				WHERE request.project_id=? AND request.continuation_id=? AND request.idempotency_key=?`,
				fixture.projectID, fixture.continuationID, request.IdempotencyKey,
			).Scan(&state, &gcContinuationID, &gcKey, &finalPath, &tempPath); err != nil {
				t.Fatalf("read durable GC checkpoint: %v", err)
			}
			if state != "gc" || gcContinuationID != fixture.continuationID || gcKey != request.IdempotencyKey {
				t.Fatalf("durable GC checkpoint = %q/%q/%q", state, gcContinuationID, gcKey)
			}
			if err := fixture.db.Close(); err != nil {
				t.Fatalf("close store for GC restart: %v", err)
			}
			reopened, err := store.Open(fixture.dbPath)
			if err != nil {
				t.Fatalf("reopen store after GC crash: %v", err)
			}
			syncCrash := blackboardv2.NewServiceWithEvidence(reopened, blackboardv2.EvidenceConfig{
				RuntimeRoot: fixture.runtimeRoot, ArtifactRoot: fixture.runtimeRoot,
				Failures: &failEvidenceV2Once{point: blackboardv2.EvidenceFailureAfterPayloadUnlink},
			})
			if _, err := syncCrash.RetainEvidenceForContinuation(context.Background(), fixture.projectID, fixture.continuationID, request); err == nil || isSemanticCode(err, "version_conflict") {
				t.Fatalf("GC recovery sync crash = %#v, want injected durability failure", err)
			}
			var durableRequestCount, durablePayloadCount int
			if err := reopened.QueryRow(`SELECT COUNT(*) FROM blackboard_v2_evidence_requests WHERE project_id=? AND continuation_id=? AND idempotency_key=?`, fixture.projectID, fixture.continuationID, request.IdempotencyKey).Scan(&durableRequestCount); err != nil {
				t.Fatalf("count GC request after recovery sync crash: %v", err)
			}
			if err := reopened.QueryRow(`SELECT COUNT(*) FROM blackboard_v2_evidence_payloads WHERE project_id=? AND managed_internal_path=? AND state='gc'`, fixture.projectID, finalPath).Scan(&durablePayloadCount); err != nil {
				t.Fatalf("count GC claim after recovery sync crash: %v", err)
			}
			if durableRequestCount != 1 || durablePayloadCount != 1 {
				t.Fatalf("GC durability rows after sync crash = request %d payload %d, want one each", durableRequestCount, durablePayloadCount)
			}
			if err := reopened.Close(); err != nil {
				t.Fatalf("close store after GC recovery sync crash: %v", err)
			}
			reopened, err = store.Open(fixture.dbPath)
			if err != nil {
				t.Fatalf("restart after GC recovery sync crash: %v", err)
			}
			t.Cleanup(func() { _ = reopened.Close() })
			restarted := blackboardv2.NewServiceWithEvidence(reopened, blackboardv2.EvidenceConfig{RuntimeRoot: fixture.runtimeRoot, ArtifactRoot: fixture.runtimeRoot})
			if _, err := restarted.RetainEvidenceForContinuation(context.Background(), fixture.projectID, fixture.continuationID, request); !isSemanticCode(err, "version_conflict") {
				t.Fatalf("second restart after durable GC checkpoint = %#v, want version_conflict", err)
			}
			var requestCount, payloadCount int
			if err := reopened.QueryRow(`SELECT COUNT(*) FROM blackboard_v2_evidence_requests WHERE project_id=? AND continuation_id=? AND idempotency_key=?`, fixture.projectID, fixture.continuationID, request.IdempotencyKey).Scan(&requestCount); err != nil {
				t.Fatalf("count recovered GC request: %v", err)
			}
			if err := reopened.QueryRow(`SELECT COUNT(*) FROM blackboard_v2_evidence_payloads WHERE project_id=? AND managed_internal_path=?`, fixture.projectID, finalPath).Scan(&payloadCount); err != nil {
				t.Fatalf("count recovered GC payload claim: %v", err)
			}
			if requestCount != 0 || payloadCount != 0 {
				t.Fatalf("recovered GC rows = request %d payload %d, want zero", requestCount, payloadCount)
			}
			for _, path := range []string{finalPath, tempPath} {
				absolute := filepath.Join(fixture.runtimeRoot, filepath.FromSlash(path))
				if _, err := os.Lstat(absolute); !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("recovered GC left managed file %q: %v", absolute, err)
				}
			}
		})
	}
}

func TestConcurrentRetainCannotClaimSharedPathDuringDurableGC(t *testing.T) {
	fixture := newEvidenceV2Fixture(t, "Concurrent Evidence Payload GC")
	fixture.writeSource(t, "current.txt", "current payload\n")
	if _, err := fixture.service.RetainEvidenceForContinuation(context.Background(), fixture.projectID, fixture.continuationID, blackboardv2.RetainEvidenceRequest{
		IdempotencyKey: "retain-current-concurrent-gc", Key: "evidence:cleanup", Attempt: "attempt:evidence",
		SourcePath: "current.txt", ArtifactType: "text", Summary: "Current retained payload",
	}); err != nil {
		t.Fatalf("retain current Evidence: %v", err)
	}
	for _, directory := range []string{"first", "second"} {
		if err := os.Mkdir(filepath.Join(fixture.workdir, directory), 0o700); err != nil {
			t.Fatalf("create shared-path source directory: %v", err)
		}
		if err := os.WriteFile(filepath.Join(fixture.workdir, directory, "shared.txt"), []byte("shared content-addressed proof\n"), 0o600); err != nil {
			t.Fatalf("write shared-path source: %v", err)
		}
	}
	barrier := &evidenceGCClaimBarrier{
		service: fixture.service, projectID: fixture.projectID, continuationID: fixture.continuationID,
		claimed: make(chan struct{}), release: make(chan struct{}),
	}
	cleanupService := blackboardv2.NewServiceWithEvidence(fixture.db, blackboardv2.EvidenceConfig{
		RuntimeRoot: fixture.runtimeRoot, ArtifactRoot: fixture.runtimeRoot, Failures: barrier,
	})
	cleanupRequest := blackboardv2.RetainEvidenceRequest{
		IdempotencyKey: "retain-cleanup-gc-race", Key: "evidence:cleanup", Version: 1, Attempt: "attempt:evidence",
		SourcePath: filepath.Join("first", "shared.txt"), ArtifactType: "text", Summary: "Payload losing a semantic race",
	}
	cleanupResult := make(chan error, 1)
	go func() {
		_, err := cleanupService.RetainEvidenceForContinuation(context.Background(), fixture.projectID, fixture.continuationID, cleanupRequest)
		cleanupResult <- err
	}()
	select {
	case <-barrier.claimed:
	case <-time.After(5 * time.Second):
		t.Fatal("cleanup did not reach durable payload GC claim")
	}
	sharedRequest := blackboardv2.RetainEvidenceRequest{
		IdempotencyKey: "retain-shared-after-gc", Key: "evidence:shared", Attempt: "attempt:evidence",
		SourcePath: filepath.Join("second", "shared.txt"), ArtifactType: "text", Summary: "Concurrent shared payload",
	}
	_, concurrentErr := fixture.service.RetainEvidenceForContinuation(context.Background(), fixture.projectID, fixture.continuationID, sharedRequest)
	var retryable *blackboardv2.Error
	if !errors.As(concurrentErr, &retryable) || retryable.Code != "evidence_payload_gc_in_progress" || !retryable.Retryable {
		t.Fatalf("concurrent shared-path retain error = %#v, want retryable payload GC", concurrentErr)
	}
	close(barrier.release)
	if err := <-cleanupResult; !isSemanticCode(err, "version_conflict") {
		t.Fatalf("cleanup retain error = %#v, want version_conflict", err)
	}
	if _, err := fixture.service.RetainEvidenceForContinuation(context.Background(), fixture.projectID, fixture.continuationID, sharedRequest); err != nil {
		t.Fatalf("retry shared-path retain after GC: %v", err)
	}
	detail, err := fixture.service.ReadCurrent(context.Background(), fixture.projectID, sharedRequest.Key)
	if err != nil {
		t.Fatalf("read shared-path Evidence: %v", err)
	}
	var internalPath string
	if err := fixture.db.QueryRow(`SELECT managed_internal_path FROM blackboard_v2_evidence_requests WHERE project_id=? AND continuation_id=? AND idempotency_key=?`, fixture.projectID, fixture.continuationID, sharedRequest.IdempotencyKey).Scan(&internalPath); err != nil {
		t.Fatalf("read shared-path managed location: %v", err)
	}
	payload, err := os.ReadFile(filepath.Join(fixture.runtimeRoot, filepath.FromSlash(internalPath)))
	if err != nil || string(payload) != "shared content-addressed proof\n" || detail.Record.SHA256 == "" {
		t.Fatalf("committed shared-path Evidence payload = %q, detail=%#v, err=%v", payload, detail, err)
	}
}

func TestReservedSharedPathPreventsConcurrentCleanupUnlink(t *testing.T) {
	fixture := newEvidenceV2Fixture(t, "Reserved Shared Evidence Payload")
	fixture.writeSource(t, "current.txt", "current payload\n")
	if _, err := fixture.service.RetainEvidenceForContinuation(context.Background(), fixture.projectID, fixture.continuationID, blackboardv2.RetainEvidenceRequest{
		IdempotencyKey: "retain-current-shared-reservation", Key: "evidence:cleanup", Attempt: "attempt:evidence",
		SourcePath: "current.txt", ArtifactType: "text", Summary: "Current retained payload",
	}); err != nil {
		t.Fatalf("retain current Evidence: %v", err)
	}
	for _, directory := range []string{"owner", "waiter"} {
		if err := os.Mkdir(filepath.Join(fixture.workdir, directory), 0o700); err != nil {
			t.Fatalf("create reserved shared-path source directory: %v", err)
		}
		if err := os.WriteFile(filepath.Join(fixture.workdir, directory, "shared.txt"), []byte("reserved shared proof\n"), 0o600); err != nil {
			t.Fatalf("write reserved shared-path source: %v", err)
		}
	}
	waiterBarrier := &evidenceBeforePublishBarrier{reached: make(chan struct{}), release: make(chan struct{})}
	waiterService := blackboardv2.NewServiceWithEvidence(fixture.db, blackboardv2.EvidenceConfig{
		RuntimeRoot: fixture.runtimeRoot, ArtifactRoot: fixture.runtimeRoot, Failures: waiterBarrier,
	})
	waiterRequest := blackboardv2.RetainEvidenceRequest{
		IdempotencyKey: "retain-reserved-shared-waiter", Key: "evidence:waiter", Attempt: "attempt:evidence",
		SourcePath: filepath.Join("waiter", "shared.txt"), ArtifactType: "text", Summary: "Reserved shared payload waiter",
	}
	waiterResult := make(chan error, 1)
	go func() {
		_, err := waiterService.RetainEvidenceForContinuation(context.Background(), fixture.projectID, fixture.continuationID, waiterRequest)
		waiterResult <- err
	}()
	select {
	case <-waiterBarrier.reached:
	case <-time.After(5 * time.Second):
		t.Fatal("shared-path waiter did not reach its durable reservation")
	}
	ownerInjector := &evidenceVersionRaceInjector{service: fixture.service, projectID: fixture.projectID, continuationID: fixture.continuationID}
	ownerService := blackboardv2.NewServiceWithEvidence(fixture.db, blackboardv2.EvidenceConfig{
		RuntimeRoot: fixture.runtimeRoot, ArtifactRoot: fixture.runtimeRoot, Failures: ownerInjector,
	})
	ownerRequest := blackboardv2.RetainEvidenceRequest{
		IdempotencyKey: "retain-reserved-shared-owner", Key: "evidence:cleanup", Version: 1, Attempt: "attempt:evidence",
		SourcePath: filepath.Join("owner", "shared.txt"), ArtifactType: "text", Summary: "Shared payload owner losing semantic race",
	}
	if _, err := ownerService.RetainEvidenceForContinuation(context.Background(), fixture.projectID, fixture.continuationID, ownerRequest); !isSemanticCode(err, "version_conflict") {
		t.Fatalf("shared-path owner error = %#v, want version_conflict", err)
	}
	var sharedPath string
	if err := fixture.db.QueryRow(`SELECT managed_internal_path FROM blackboard_v2_evidence_requests WHERE project_id=? AND continuation_id=? AND idempotency_key=?`, fixture.projectID, fixture.continuationID, waiterRequest.IdempotencyKey).Scan(&sharedPath); err != nil {
		t.Fatalf("read reserved shared managed path: %v", err)
	}
	if _, err := os.Stat(filepath.Join(fixture.runtimeRoot, filepath.FromSlash(sharedPath))); err != nil {
		t.Fatalf("cleanup unlinked payload protected by reservation: %v", err)
	}
	close(waiterBarrier.release)
	if err := <-waiterResult; err != nil {
		t.Fatalf("reserved shared-path waiter commit: %v", err)
	}
	detail, err := fixture.service.ReadCurrent(context.Background(), fixture.projectID, waiterRequest.Key)
	if err != nil || detail.Record.SHA256 == "" {
		t.Fatalf("read committed reserved shared Evidence: %#v, err=%v", detail, err)
	}
}

type evidenceBeforePublishBarrier struct {
	reached chan struct{}
	release chan struct{}
}

func (barrier *evidenceBeforePublishBarrier) FailAfter(point blackboardv2.EvidenceFailurePoint) error {
	if point == blackboardv2.EvidenceFailureBeforeFilePublish {
		close(barrier.reached)
		<-barrier.release
	}
	return nil
}

type evidenceGCClaimBarrier struct {
	service        *blackboardv2.Service
	projectID      string
	continuationID string
	claimed        chan struct{}
	release        chan struct{}
	updated        bool
}

func (barrier *evidenceGCClaimBarrier) FailAfter(point blackboardv2.EvidenceFailurePoint) error {
	if point == blackboardv2.EvidenceFailureAfterFilePublish && !barrier.updated {
		barrier.updated = true
		_, err := barrier.service.ApplyForContinuation(context.Background(), barrier.projectID, barrier.continuationID, blackboardv2.ChangeBatch{
			Schema: "semantic-change-batch/v2", IdempotencyKey: "concurrent-update-before-payload-gc",
			Changes: []blackboardv2.Change{{
				Op: "update", Key: "evidence:cleanup", Version: 1, Type: "evidence",
				Record: blackboardv2.EvidencePatch{Summary: strPtr("Concurrent semantic update")},
			}},
		})
		return err
	}
	if point == blackboardv2.EvidenceFailureAfterPayloadGCClaim {
		close(barrier.claimed)
		<-barrier.release
	}
	return nil
}

type evidenceCleanupCrashInjector struct {
	service        *blackboardv2.Service
	projectID      string
	continuationID string
	crashPoint     blackboardv2.EvidenceFailurePoint
	updated        bool
	crashed        bool
}

func (injector *evidenceCleanupCrashInjector) FailAfter(point blackboardv2.EvidenceFailurePoint) error {
	if point == blackboardv2.EvidenceFailureAfterFilePublish && !injector.updated {
		injector.updated = true
		_, err := injector.service.ApplyForContinuation(context.Background(), injector.projectID, injector.continuationID, blackboardv2.ChangeBatch{
			Schema: "semantic-change-batch/v2", IdempotencyKey: "concurrent-evidence-update-" + string(injector.crashPoint),
			Changes: []blackboardv2.Change{{
				Op: "update", Key: "evidence:cleanup", Version: 1, Type: "evidence",
				Record: blackboardv2.EvidencePatch{Summary: strPtr("Concurrent semantic update")},
			}},
		})
		return err
	}
	if point == injector.crashPoint && !injector.crashed {
		injector.crashed = true
		return errors.New("injected Evidence cleanup crash")
	}
	return nil
}

type evidenceVersionRaceInjector struct {
	service        *blackboardv2.Service
	projectID      string
	continuationID string
	fired          bool
}

func (injector *evidenceVersionRaceInjector) FailAfter(point blackboardv2.EvidenceFailurePoint) error {
	if point != blackboardv2.EvidenceFailureAfterFilePublish || injector.fired {
		return nil
	}
	injector.fired = true
	_, err := injector.service.ApplyForContinuation(context.Background(), injector.projectID, injector.continuationID, blackboardv2.ChangeBatch{
		Schema: "semantic-change-batch/v2", IdempotencyKey: "concurrent-evidence-update",
		Changes: []blackboardv2.Change{{
			Op: "update", Key: "evidence:cleanup", Version: 1, Type: "evidence",
			Record: blackboardv2.EvidencePatch{Summary: strPtr("Concurrent semantic update")},
		}},
	})
	return err
}

func TestEmptyEvidenceDetailIncludesZeroSize(t *testing.T) {
	fixture := newEvidenceV2Fixture(t, "Empty Evidence")
	fixture.writeSource(t, "empty.txt", "")
	if _, err := fixture.service.RetainEvidenceForContinuation(context.Background(), fixture.projectID, fixture.continuationID, blackboardv2.RetainEvidenceRequest{
		IdempotencyKey: "retain-empty", Key: "evidence:empty", Attempt: "attempt:evidence",
		SourcePath: "empty.txt", ArtifactType: "text", Summary: "An intentionally empty retained artifact",
	}); err != nil {
		t.Fatalf("retain empty Evidence: %v", err)
	}
	detail, err := fixture.service.ReadCurrent(context.Background(), fixture.projectID, "evidence:empty")
	if err != nil {
		t.Fatalf("read empty Evidence: %v", err)
	}
	if detail.Record.Size != 0 || detail.Record.SHA256 != "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855" {
		t.Fatalf("empty Evidence integrity = %#v", detail.Record)
	}
	assertContractJSON(t, mustHarness(t), "currentDetail", detail)
}

func TestEvidenceSemanticUpdatePreservesServerDerivedIntegrityAndUsesCurrentVersion(t *testing.T) {
	fixture := newEvidenceV2Fixture(t, "Evidence Semantic Update")
	fixture.writeSource(t, "update.txt", "immutable payload\n")
	if _, err := fixture.service.RetainEvidenceForContinuation(context.Background(), fixture.projectID, fixture.continuationID, blackboardv2.RetainEvidenceRequest{
		IdempotencyKey: "retain-update", Key: "evidence:update", Attempt: "attempt:evidence",
		SourcePath: "update.txt", ArtifactType: "text", Summary: "Initial Evidence summary", MediaType: "text/plain", CapturedAt: "2026-07-17T10:00:00Z",
	}); err != nil {
		t.Fatalf("retain Evidence: %v", err)
	}
	before, err := fixture.service.ReadCurrent(context.Background(), fixture.projectID, "evidence:update")
	if err != nil {
		t.Fatalf("read retained Evidence: %v", err)
	}
	updated, err := fixture.service.ApplyForContinuation(context.Background(), fixture.projectID, fixture.continuationID, blackboardv2.ChangeBatch{
		Schema: "semantic-change-batch/v2", IdempotencyKey: "update-evidence-semantics",
		Changes: []blackboardv2.Change{{Op: "update", Key: "evidence:update", Version: 1, Type: "evidence", Record: blackboardv2.EvidencePatch{
			Summary: strPtr("Updated Evidence summary"), MediaType: strPtr("application/octet-stream"),
		}}},
	})
	if err != nil {
		t.Fatalf("update Evidence semantics: %v", err)
	}
	if got, want := mustTupleJSON(t, updated.Records), [][]any{{"evidence:update", float64(2)}}; !reflect.DeepEqual(got, want) {
		t.Fatalf("updated Evidence records = %#v, want %#v", got, want)
	}
	after, err := fixture.service.ReadCurrent(context.Background(), fixture.projectID, "evidence:update")
	if err != nil {
		t.Fatalf("read updated Evidence: %v", err)
	}
	if after.Record.Summary != "Updated Evidence summary" || after.Record.MediaType != "application/octet-stream" ||
		after.Record.ManagedPath != before.Record.ManagedPath || after.Record.SHA256 != before.Record.SHA256 || after.Record.Size != before.Record.Size ||
		after.Record.SourcePath != before.Record.SourcePath || after.Record.ArtifactType != before.Record.ArtifactType {
		t.Fatalf("updated Evidence detail = %#v, before = %#v", after, before)
	}
	if _, err := fixture.service.ApplyForContinuation(context.Background(), fixture.projectID, fixture.continuationID, blackboardv2.ChangeBatch{
		Schema: "semantic-change-batch/v2", IdempotencyKey: "stale-evidence-update",
		Changes: []blackboardv2.Change{{Op: "update", Key: "evidence:update", Version: 1, Type: "evidence", Record: blackboardv2.EvidencePatch{Summary: strPtr("Stale")}}},
	}); !isSemanticCode(err, "version_conflict") {
		t.Fatalf("stale Evidence update error = %#v, want version_conflict", err)
	}
}

func TestEvidenceOptionalFieldsRequireExplicitClear(t *testing.T) {
	fixture := newEvidenceV2Fixture(t, "Evidence Explicit Clear")
	fixture.writeSource(t, "clear.txt", "clear semantics\n")
	if _, err := fixture.service.RetainEvidenceForContinuation(context.Background(), fixture.projectID, fixture.continuationID, blackboardv2.RetainEvidenceRequest{
		IdempotencyKey: "retain-clear", Key: "evidence:clear", Attempt: "attempt:evidence",
		SourcePath: "clear.txt", ArtifactType: "text", Summary: "Evidence with optional fields",
		MediaType: "text/plain", CapturedAt: "2026-07-17T10:30:00Z",
	}); err != nil {
		t.Fatalf("retain Evidence with optional fields: %v", err)
	}
	for name, patch := range map[string]blackboardv2.EvidencePatch{
		"empty media_type":  {MediaType: strPtr("")},
		"empty captured_at": {CapturedAt: strPtr("")},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := fixture.service.ApplyForContinuation(context.Background(), fixture.projectID, fixture.continuationID, blackboardv2.ChangeBatch{
				Schema: "semantic-change-batch/v2", IdempotencyKey: "reject-" + name,
				Changes: []blackboardv2.Change{{Op: "update", Key: "evidence:clear", Version: 1, Type: "evidence", Record: patch}},
			}); !isSemanticCode(err, "semantic_validation") {
				t.Fatalf("empty optional Evidence patch error = %#v, want semantic_validation", err)
			}
		})
	}
	detail, err := fixture.service.ReadCurrent(context.Background(), fixture.projectID, "evidence:clear")
	if err != nil || detail.Version != 1 || detail.Record.MediaType != "text/plain" || detail.Record.CapturedAt != "2026-07-17T10:30:00Z" {
		t.Fatalf("empty patch silently cleared optional Evidence fields: %#v, err = %v", detail, err)
	}
	if _, err := fixture.service.ApplyForContinuation(context.Background(), fixture.projectID, fixture.continuationID, blackboardv2.ChangeBatch{
		Schema: "semantic-change-batch/v2", IdempotencyKey: "explicitly-clear-evidence-fields",
		Changes: []blackboardv2.Change{{
			Op: "update", Key: "evidence:clear", Version: 1, Type: "evidence",
			Record: blackboardv2.EvidencePatch{Summary: strPtr("Evidence after explicit clear")},
			Clear:  []string{"media_type", "captured_at"},
		}},
	}); err != nil {
		t.Fatalf("explicitly clear optional Evidence fields: %v", err)
	}
	detail, err = fixture.service.ReadCurrent(context.Background(), fixture.projectID, "evidence:clear")
	if err != nil || detail.Version != 2 || detail.Record.MediaType != "" || detail.Record.CapturedAt != "" {
		t.Fatalf("explicit Evidence clear result = %#v, err = %v", detail, err)
	}
}

type replaceEvidenceV2Source struct{ path string }

func (replacement replaceEvidenceV2Source) FailAfter(point blackboardv2.EvidenceFailurePoint) error {
	if point != blackboardv2.EvidenceFailureBeforeFilePublish {
		return nil
	}
	if err := os.Rename(replacement.path, replacement.path+".original"); err != nil {
		return err
	}
	return os.WriteFile(replacement.path, []byte("replacement bytes\n"), 0o600)
}

type failEvidenceV2Once struct {
	point  blackboardv2.EvidenceFailurePoint
	failed bool
}

func (failure *failEvidenceV2Once) FailAfter(point blackboardv2.EvidenceFailurePoint) error {
	if point != failure.point || failure.failed {
		return nil
	}
	failure.failed = true
	return errors.New("injected Evidence retention failure")
}

type evidenceV2Fixture struct {
	db             *store.DB
	dbPath         string
	service        *blackboardv2.Service
	projectID      string
	taskID         string
	continuationID string
	workdir        string
	runtimeRoot    string
}

func newEvidenceV2Fixture(t *testing.T, name string) evidenceV2Fixture {
	t.Helper()
	root := t.TempDir()
	dbPath := filepath.Join(root, "pentest.db")
	db, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	projects := project.NewService(db)
	createdProject, err := projects.Create(name, "", project.Scope{}, project.Defaults{})
	if err != nil {
		t.Fatalf("create Project: %v", err)
	}
	tasks := task.NewService(db, projects)
	createdTask, err := tasks.Create(task.CreateRequest{ProjectID: createdProject.ID, Goal: name, Runner: task.RunnerSandbox})
	if err != nil {
		t.Fatalf("create Task: %v", err)
	}
	continuation, err := tasks.CreateContinuation(createdTask.ID, "profile-evidence", "codex", task.RunnerSandbox)
	if err != nil {
		t.Fatalf("create Continuation: %v", err)
	}
	runtimeRoot := filepath.Join(root, "runtime")
	service := blackboardv2.NewServiceWithEvidence(db, blackboardv2.EvidenceConfig{RuntimeRoot: runtimeRoot, ArtifactRoot: runtimeRoot})
	_, err = service.ApplyForContinuation(context.Background(), createdProject.ID, continuation.ID, blackboardv2.ChangeBatch{
		Schema: "semantic-change-batch/v2", IdempotencyKey: "prepare-evidence-fixture",
		Changes: []blackboardv2.Change{
			{Op: "create", Key: "objective:evidence", Type: "objective", Record: blackboardv2.ObjectiveRecord{Status: "open", Objective: "Retain fixture Evidence"}},
			{Op: "create", Key: "attempt:evidence", Type: "attempt", Record: blackboardv2.AttemptRecord{Status: "open", Summary: "Retain fixture Evidence"}},
			{Op: "relate", From: "attempt:evidence", Relation: "tests", To: "objective:evidence"},
		},
	})
	if err != nil {
		t.Fatalf("prepare Evidence fixture: %v", err)
	}
	workdir := filepath.Join(runtimeRoot, createdTask.ID, "workdir")
	if err := os.MkdirAll(workdir, 0o700); err != nil {
		t.Fatalf("create Evidence workdir: %v", err)
	}
	return evidenceV2Fixture{db: db, dbPath: dbPath, service: service, projectID: createdProject.ID, taskID: createdTask.ID, continuationID: continuation.ID, workdir: workdir, runtimeRoot: runtimeRoot}
}

func (fixture evidenceV2Fixture) writeSource(t *testing.T, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(fixture.workdir, name), []byte(content), 0o600); err != nil {
		t.Fatalf("write Evidence source: %v", err)
	}
}
