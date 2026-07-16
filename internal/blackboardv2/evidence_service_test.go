package blackboardv2_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
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
	matches, err := filepath.Glob(filepath.Join(runtimeRoot, "*", "artifacts", "retained", "*", "*"))
	if err != nil {
		t.Fatalf("glob retained Evidence: %v", err)
	}
	if len(matches) != 0 {
		t.Fatalf("rejected retain published managed files: %v", matches)
	}
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
	managedPayload, err := os.ReadFile(filepath.Join(artifactRoot, filepath.FromSlash(detail.Record.ManagedPath)))
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
	if err := os.Symlink(filepath.Join(fixture.workdir, "inside.txt"), filepath.Join(fixture.workdir, "inside-link.txt")); err != nil {
		t.Fatalf("create in-root symlink: %v", err)
	}
	otherTaskSource := filepath.Join(fixture.runtimeRoot, "another-task", "workdir", "proof.txt")
	if err := os.MkdirAll(filepath.Dir(otherTaskSource), 0o700); err != nil {
		t.Fatalf("create other Task root: %v", err)
	}
	if err := os.WriteFile(otherTaskSource, []byte("other task\n"), 0o600); err != nil {
		t.Fatalf("write other Task source: %v", err)
	}
	for name, sourcePath := range map[string]string{
		"traversal":        "../../outside.txt",
		"escaping-symlink": "escape.txt",
		"in-root-symlink":  "inside-link.txt",
		"other-task":       otherTaskSource,
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
			matches, err := filepath.Glob(filepath.Join(fixture.runtimeRoot, "*", "artifacts", "retained", "*", "*"))
			if err != nil || len(matches) != 1 {
				t.Fatalf("managed Evidence files = %v, err = %v", matches, err)
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
	matchesBefore, err := filepath.Glob(filepath.Join(fixture.runtimeRoot, "*", "artifacts", "retained", "*", "*"))
	if err != nil || len(matchesBefore) != 2 {
		t.Fatalf("managed replacement files before stale write = %v, err = %v", matchesBefore, err)
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
	matchesAfter, err := filepath.Glob(filepath.Join(fixture.runtimeRoot, "*", "artifacts", "retained", "*", "*"))
	if err != nil || !reflect.DeepEqual(matchesAfter, matchesBefore) {
		t.Fatalf("stale replacement changed managed files:\nbefore %v\nafter %v\nerr %v", matchesBefore, matchesAfter, err)
	}
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
	db, err := store.Open(filepath.Join(root, "pentest.db"))
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
	return evidenceV2Fixture{db: db, service: service, projectID: createdProject.ID, taskID: createdTask.ID, continuationID: continuation.ID, workdir: workdir, runtimeRoot: runtimeRoot}
}

func (fixture evidenceV2Fixture) writeSource(t *testing.T, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(fixture.workdir, name), []byte(content), 0o600); err != nil {
		t.Fatalf("write Evidence source: %v", err)
	}
}
