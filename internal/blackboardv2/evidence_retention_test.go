package blackboardv2_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"pentest/internal/blackboardv2"
	"pentest/internal/project"
	"pentest/internal/store"
	"pentest/internal/task"
)

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
	createdTask, err := tasks.Create(task.CreateRequest{ProjectID: createdProject.ID, Type: task.TypePentest, Goal: "Retain proof", Runner: task.RunnerSandbox})
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
	createdTask, err := tasks.Create(task.CreateRequest{ProjectID: createdProject.ID, Type: task.TypePentest, Goal: "Confirm from proof", Runner: task.RunnerSandbox})
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
