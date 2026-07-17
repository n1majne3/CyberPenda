package blackboardv2_test

import (
	"context"
	"encoding/json"
	"errors"
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

func TestCTFSolutionVerificationDerivesAndReversesSolvedState(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(filepath.Join(t.TempDir(), "pentest.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	createdProject, err := project.NewService(db).CreateWithKind("Challenge", "", project.KindCTFChallenge, project.Scope{}, project.Defaults{})
	if err != nil {
		t.Fatalf("create CTF Project: %v", err)
	}
	service := blackboardv2.NewService(db)

	_, err = service.Apply(ctx, createdProject.ID, blackboardv2.ChangeBatch{
		Schema: "semantic-change-batch/v2", IdempotencyKey: "solution-candidate",
		Changes: []blackboardv2.Change{
			{Op: "create", Key: "objective:recover-flag", Type: "objective", Record: blackboardv2.ObjectiveRecord{Status: "open", Objective: "Recover the accepted challenge flag"}},
			{Op: "create", Key: "solution:flag", Type: "solution", Record: blackboardv2.SolutionRecord{Status: "candidate", Kind: "flag", Summary: "Recovered a candidate flag", Value: "FLAG{accepted}"}},
			{Op: "relate", From: "solution:flag", Relation: "satisfies", To: "objective:recover-flag"},
		},
	})
	if err != nil {
		t.Fatalf("create candidate Solution: %v", err)
	}

	candidateState, err := service.CTFSolvedState(ctx, createdProject.ID)
	if err != nil {
		t.Fatalf("read candidate solved state: %v", err)
	}
	if candidateState.Solved || len(candidateState.VerifiedFlags) != 0 {
		t.Fatalf("candidate Solution solved state = %#v", candidateState)
	}

	_, err = service.Apply(ctx, createdProject.ID, blackboardv2.ChangeBatch{
		Schema: "semantic-change-batch/v2", IdempotencyKey: "solution-verified",
		Changes: []blackboardv2.Change{{Op: "transition", Key: "solution:flag", Version: 1, Status: "verified", VerificationSummary: "Accepted by the challenge service"}},
	})
	if err != nil {
		t.Fatalf("verify Solution: %v", err)
	}

	solved, err := service.CTFSolvedState(ctx, createdProject.ID)
	if err != nil {
		t.Fatalf("read solved state: %v", err)
	}
	if !solved.Solved || len(solved.VerifiedFlags) != 1 || solved.VerifiedFlags[0] != "solution:flag" {
		t.Fatalf("verified flag solved state = %#v", solved)
	}
	snapshot, err := service.RuntimeSnapshot(ctx, createdProject.ID)
	if err != nil {
		t.Fatalf("read verified Solution snapshot: %v", err)
	}
	if got := snapshot.Knowledge.Solutions["solution:flag"]; got.Status != "verified" || got.VerificationSummary != "Accepted by the challenge service" {
		t.Fatalf("verified Solution snapshot = %#v", got)
	}
	assertContractJSON(t, mustHarness(t), "runtimeSnapshot", snapshot)

	_, err = service.Apply(ctx, createdProject.ID, blackboardv2.ChangeBatch{
		Schema: "semantic-change-batch/v2", IdempotencyKey: "solution-rejected",
		Changes: []blackboardv2.Change{
			{Op: "create", Key: "fact:flag-rejection", Type: "fact", Record: blackboardv2.FactRecord{Category: "validation", Summary: "The challenge rejected the candidate flag", Confidence: "confirmed", ScopeStatus: "in_scope"}},
			{Op: "relate", From: "fact:flag-rejection", Relation: "contradicts", To: "solution:flag", Reason: "The authoritative challenge response rejected this value"},
			{Op: "transition", Key: "solution:flag", Version: 2, Status: "rejected", VerificationSummary: "Rejected by the challenge service"},
		},
	})
	if err != nil {
		t.Fatalf("reject Solution with reusable meaning: %v", err)
	}
	unsolved, err := service.CTFSolvedState(ctx, createdProject.ID)
	if err != nil {
		t.Fatalf("read reversed solved state: %v", err)
	}
	if unsolved.Solved || len(unsolved.VerifiedFlags) != 0 {
		t.Fatalf("rejected flag solved state = %#v", unsolved)
	}
	if _, err := service.ReadCurrent(ctx, createdProject.ID, "solution:flag"); !isSemanticCode(err, "not_found") {
		t.Fatalf("rejected Solution remained current: %#v", err)
	}
	history, err := service.ReadHistory(ctx, createdProject.ID, "solution:flag", blackboardv2.HistoryOptions{})
	if err != nil {
		t.Fatalf("read rejected Solution history: %v", err)
	}
	if len(history.Items) < 3 || history.Items[2].Record == nil || history.Items[2].Record.Status != "rejected" {
		t.Fatalf("rejected Solution history = %#v", history.Items)
	}
	assertContractJSON(t, mustHarness(t), "semanticHistory", history)
}

func TestSolutionValidationAndPentestKindGuardAreClosedAndAtomic(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(filepath.Join(t.TempDir(), "pentest.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	projects := project.NewService(db)
	pentestProject, err := projects.Create("Pentest", "", project.Scope{}, project.Defaults{})
	if err != nil {
		t.Fatalf("create Pentest Project: %v", err)
	}
	ctfProject, err := projects.CreateWithKind("Challenge", "", project.KindCTFChallenge, project.Scope{}, project.Defaults{})
	if err != nil {
		t.Fatalf("create CTF Project: %v", err)
	}
	service := blackboardv2.NewService(db)

	_, err = service.Apply(ctx, pentestProject.ID, blackboardv2.ChangeBatch{
		Schema: "semantic-change-batch/v2", IdempotencyKey: "pentest-solution",
		Changes: []blackboardv2.Change{
			{Op: "create", Key: "fact:must-rollback", Type: "fact", Record: blackboardv2.FactRecord{Category: "test", Summary: "must roll back", Confidence: "tentative", ScopeStatus: "unknown"}},
			{Op: "create", Key: "solution:forbidden", Type: "solution", Record: blackboardv2.SolutionRecord{Status: "candidate", Kind: "flag", Summary: "must be rejected"}},
		},
	})
	var semanticErr *blackboardv2.Error
	if !errors.As(err, &semanticErr) || semanticErr.Code != "project_kind_mismatch" || semanticErr.Message != "Solutions require a CTF Challenge Project" {
		t.Fatalf("Pentest Solution error = %#v", err)
	}
	if _, err := service.ReadCurrent(ctx, pentestProject.ID, "fact:must-rollback"); !isSemanticCode(err, "not_found") {
		t.Fatalf("Pentest rejection retained earlier batch write: %#v", err)
	}

	tests := []struct {
		name   string
		record any
	}{
		{name: "unknown kind", record: blackboardv2.SolutionRecord{Status: "candidate", Kind: "token", Summary: "Unknown kind"}},
		{name: "missing status", record: blackboardv2.SolutionRecord{Kind: "flag", Summary: "Missing status"}},
		{name: "missing summary", record: blackboardv2.SolutionRecord{Status: "candidate", Kind: "flag"}},
		{name: "verified flag missing value", record: blackboardv2.SolutionRecord{Status: "verified", Kind: "flag", Summary: "Missing value", VerificationSummary: "Accepted"}},
		{name: "verified answer missing value", record: blackboardv2.SolutionRecord{Status: "verified", Kind: "answer", Summary: "Missing value", VerificationSummary: "Accepted"}},
		{name: "verified missing summary", record: blackboardv2.SolutionRecord{Status: "verified", Kind: "procedure", Summary: "Procedure", VerificationSummary: ""}},
		{name: "oversized verification", record: blackboardv2.SolutionRecord{Status: "verified", Kind: "procedure", Summary: "Procedure", VerificationSummary: strings.Repeat("x", 513)}},
		{name: "unknown field", record: map[string]any{"status": "candidate", "kind": "flag", "summary": "Closed", "solved": true}},
	}
	for i, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := service.Apply(ctx, ctfProject.ID, blackboardv2.ChangeBatch{
				Schema: "semantic-change-batch/v2", IdempotencyKey: "invalid-solution-" + tt.name,
				Changes: []blackboardv2.Change{{Op: "create", Key: "solution:invalid-" + string(rune('a'+i)), Type: "solution", Record: tt.record}},
			})
			if !isSemanticCode(err, "semantic_validation") {
				t.Fatalf("invalid Solution error = %#v", err)
			}
		})
	}
	_, err = service.Apply(ctx, ctfProject.ID, blackboardv2.ChangeBatch{
		Schema: "semantic-change-batch/v2", IdempotencyKey: "candidate-without-value",
		Changes: []blackboardv2.Change{{Op: "create", Key: "solution:no-value", Type: "solution", Record: blackboardv2.SolutionRecord{Status: "candidate", Kind: "flag", Summary: "Candidate without a value"}}},
	})
	if err != nil {
		t.Fatalf("create value-less candidate: %v", err)
	}
	_, err = service.Apply(ctx, ctfProject.ID, blackboardv2.ChangeBatch{
		Schema: "semantic-change-batch/v2", IdempotencyKey: "verify-without-value",
		Changes: []blackboardv2.Change{
			{Op: "create", Key: "fact:verify-marker", Type: "fact", Record: blackboardv2.FactRecord{Category: "test", Summary: "must roll back", Confidence: "tentative", ScopeStatus: "unknown"}},
			{Op: "transition", Key: "solution:no-value", Version: 1, Status: "verified", VerificationSummary: "Accepted"},
		},
	})
	if !isSemanticCode(err, "semantic_validation") {
		t.Fatalf("value-less verification error = %#v", err)
	}
	if _, err := service.ReadCurrent(ctx, ctfProject.ID, "fact:verify-marker"); !isSemanticCode(err, "not_found") {
		t.Fatalf("failed verification retained marker: %#v", err)
	}
	_, err = service.Apply(ctx, ctfProject.ID, blackboardv2.ChangeBatch{
		Schema: "semantic-change-batch/v2", IdempotencyKey: "reject-without-reason",
		Changes: []blackboardv2.Change{{Op: "transition", Key: "solution:no-value", Version: 1, Status: "rejected", VerificationSummary: "Rejected"}},
	})
	if !isSemanticCode(err, "semantic_validation") {
		t.Fatalf("reasonless rejection error = %#v", err)
	}
	_, err = service.Apply(ctx, ctfProject.ID, blackboardv2.ChangeBatch{
		Schema: "semantic-change-batch/v2", IdempotencyKey: "invalid-transition",
		Changes: []blackboardv2.Change{{Op: "transition", Key: "solution:no-value", Version: 1, Status: "confirmed"}},
	})
	if !isSemanticCode(err, "semantic_validation") {
		t.Fatalf("invalid Solution transition error = %#v", err)
	}

	var decoded blackboardv2.ChangeBatch
	if err := json.Unmarshal([]byte(`{"schema":"semantic-change-batch/v2","idempotency_key":"closed-transition","changes":[{"op":"transition","key":"solution:a","version":1,"status":"verified","verification_summary":"accepted","solved":true}]}`), &decoded); err == nil {
		t.Fatal("transition accepted caller-writable solved field")
	}
	if err := json.Unmarshal([]byte(`{"schema":"semantic-change-batch/v2","idempotency_key":"closed-objective-transition","changes":[{"op":"transition","key":"objective:a","version":1,"status":"resolved","resolution_summary":"done","verification_summary":"not a Solution"}]}`), &decoded); err == nil {
		t.Fatal("non-Solution transition accepted verification_summary")
	}
}

func TestSolutionVersionsRelationsAndSolvedStateRemainDeterministicAcrossReload(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "pentest.db")
	db, err := store.Open(path)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	projects := project.NewService(db)
	createdProject, err := projects.CreateWithKind("Challenge", "", project.KindCTFChallenge, project.Scope{}, project.Defaults{})
	if err != nil {
		t.Fatalf("create CTF Project: %v", err)
	}
	service := blackboardv2.NewService(db)
	_, err = service.Apply(ctx, createdProject.ID, blackboardv2.ChangeBatch{
		Schema: "semantic-change-batch/v2", IdempotencyKey: "solution-topology",
		Changes: []blackboardv2.Change{
			{Op: "create", Key: "entity:challenge", Type: "entity", Record: blackboardv2.EntityRecord{Status: "active", Kind: "host", Name: "Challenge", ScopeStatus: "in_scope"}},
			{Op: "create", Key: "objective:solve", Type: "objective", Record: blackboardv2.ObjectiveRecord{Status: "open", Objective: "Solve the challenge"}},
			{Op: "create", Key: "fact:support", Type: "fact", Record: blackboardv2.FactRecord{Category: "validation", Summary: "The response confirms the solution", Confidence: "confirmed", ScopeStatus: "in_scope"}},
			{Op: "create", Key: "solution:z", Type: "solution", Record: blackboardv2.SolutionRecord{Status: "verified", Kind: "flag", Summary: "Second flag", Value: "FLAG{z}", VerificationSummary: "Accepted z"}},
			{Op: "create", Key: "solution:a", Type: "solution", Record: blackboardv2.SolutionRecord{Status: "verified", Kind: "flag", Summary: "First flag", Value: "FLAG{a}", VerificationSummary: "Accepted a"}},
			{Op: "create", Key: "solution:answer", Type: "solution", Record: blackboardv2.SolutionRecord{Status: "verified", Kind: "answer", Summary: "Challenge answer", Value: "42", VerificationSummary: "Accepted answer"}},
			{Op: "relate", From: "solution:a", Relation: "about", To: "entity:challenge"},
			{Op: "relate", From: "fact:support", Relation: "supports", To: "solution:a", Reason: "The response independently confirms it"},
			{Op: "relate", From: "solution:a", Relation: "satisfies", To: "objective:solve"},
		},
	})
	if err != nil {
		t.Fatalf("create Solution topology: %v", err)
	}
	state, err := service.CTFSolvedState(ctx, createdProject.ID)
	if err != nil {
		t.Fatalf("read solved state: %v", err)
	}
	if want := []string{"solution:a", "solution:z"}; !reflect.DeepEqual(state.VerifiedFlags, want) {
		t.Fatalf("verified flag order = %v, want %v", state.VerifiedFlags, want)
	}

	_, err = service.Apply(ctx, createdProject.ID, blackboardv2.ChangeBatch{
		Schema: "semantic-change-batch/v2", IdempotencyKey: "stale-solution-update",
		Changes: []blackboardv2.Change{
			{Op: "create", Key: "fact:stale-marker", Type: "fact", Record: blackboardv2.FactRecord{Category: "test", Summary: "must roll back", Confidence: "tentative", ScopeStatus: "unknown"}},
			{Op: "update", Key: "solution:a", Version: 2, Type: "solution", Record: blackboardv2.SolutionPatch{Summary: strPtr("stale")}},
		},
	})
	if !isSemanticCode(err, "version_conflict") {
		t.Fatalf("stale Solution update error = %#v", err)
	}
	if _, err := service.ReadCurrent(ctx, createdProject.ID, "fact:stale-marker"); !isSemanticCode(err, "not_found") {
		t.Fatalf("stale batch retained marker: %#v", err)
	}
	_, err = service.Apply(ctx, createdProject.ID, blackboardv2.ChangeBatch{
		Schema: "semantic-change-batch/v2", IdempotencyKey: "replacement-candidates",
		Changes: []blackboardv2.Change{
			{Op: "create", Key: "solution:a-next", Type: "solution", Record: blackboardv2.SolutionRecord{Status: "candidate", Kind: "flag", Summary: "Replacement for a", Value: "FLAG{a-next}"}},
			{Op: "create", Key: "solution:z-next", Type: "solution", Record: blackboardv2.SolutionRecord{Status: "candidate", Kind: "flag", Summary: "Replacement for z", Value: "FLAG{z-next}"}},
		},
	})
	if err != nil {
		t.Fatalf("create replacement candidates: %v", err)
	}
	_, err = service.Apply(ctx, createdProject.ID, blackboardv2.ChangeBatch{
		Schema: "semantic-change-batch/v2", IdempotencyKey: "supersede-first-verified-flag",
		Changes: []blackboardv2.Change{{Op: "supersede", Replacement: "solution:a-next", ReplacementVersion: 1, Replaced: "solution:a", ReplacedVersion: 1}},
	})
	if err != nil {
		t.Fatalf("supersede first verified flag: %v", err)
	}
	oneRemaining, err := service.CTFSolvedState(ctx, createdProject.ID)
	if err != nil || !oneRemaining.Solved || !reflect.DeepEqual(oneRemaining.VerifiedFlags, []string{"solution:z"}) {
		t.Fatalf("solved state with one verified flag = %#v, %v", oneRemaining, err)
	}
	_, err = service.Apply(ctx, createdProject.ID, blackboardv2.ChangeBatch{
		Schema: "semantic-change-batch/v2", IdempotencyKey: "supersede-last-verified-flag",
		Changes: []blackboardv2.Change{{Op: "supersede", Replacement: "solution:z-next", ReplacementVersion: 1, Replaced: "solution:z", ReplacedVersion: 1}},
	})
	if err != nil {
		t.Fatalf("supersede last verified flag: %v", err)
	}
	noFlags, err := service.CTFSolvedState(ctx, createdProject.ID)
	if err != nil || noFlags.Solved || len(noFlags.VerifiedFlags) != 0 {
		t.Fatalf("solved state without verified flags = %#v, %v", noFlags, err)
	}
	state = noFlags

	before, err := service.RuntimeSnapshot(ctx, createdProject.ID)
	if err != nil {
		t.Fatalf("read snapshot before reload: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close store: %v", err)
	}
	reopened, err := store.Open(path)
	if err != nil {
		t.Fatalf("reopen store: %v", err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	reloaded := blackboardv2.NewService(reopened)
	after, err := reloaded.RuntimeSnapshot(ctx, createdProject.ID)
	if err != nil {
		t.Fatalf("read snapshot after reload: %v", err)
	}
	if !reflect.DeepEqual(before, after) {
		t.Fatalf("snapshot changed across reload\nbefore=%#v\nafter=%#v", before, after)
	}
	reloadedState, err := reloaded.CTFSolvedState(ctx, createdProject.ID)
	if err != nil || !reflect.DeepEqual(reloadedState, state) {
		t.Fatalf("solved state after reload = %#v, %v; want %#v", reloadedState, err, state)
	}
}

func TestSolutionEvidenceEndpointAndSupersessionControlSolvedState(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	db, err := store.Open(filepath.Join(root, "pentest.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	projects := project.NewService(db)
	createdProject, err := projects.CreateWithKind("Challenge", "", project.KindCTFChallenge, project.Scope{}, project.Defaults{})
	if err != nil {
		t.Fatalf("create CTF Project: %v", err)
	}
	tasks := task.NewService(db, projects)
	createdTask, err := tasks.Create(task.CreateRequest{ProjectID: createdProject.ID, Goal: "Verify the flag", Runner: task.RunnerSandbox})
	if err != nil {
		t.Fatalf("create Task: %v", err)
	}
	continuation, err := tasks.CreateContinuation(createdTask.ID, "profile-solution", "codex", task.RunnerSandbox)
	if err != nil {
		t.Fatalf("create Continuation: %v", err)
	}
	runtimeRoot := filepath.Join(root, "runtime")
	service := blackboardv2.NewServiceWithEvidence(db, blackboardv2.EvidenceConfig{RuntimeRoot: runtimeRoot, ArtifactRoot: runtimeRoot})
	_, err = service.ApplyForContinuation(ctx, createdProject.ID, continuation.ID, blackboardv2.ChangeBatch{
		Schema: "semantic-change-batch/v2", IdempotencyKey: "solution-evidence-seed",
		Changes: []blackboardv2.Change{
			{Op: "create", Key: "objective:verify", Type: "objective", Record: blackboardv2.ObjectiveRecord{Status: "open", Objective: "Verify the candidate flag"}},
			{Op: "create", Key: "attempt:verify", Type: "attempt", Record: blackboardv2.AttemptRecord{Status: "open", Summary: "Submit the candidate flag"}},
			{Op: "relate", From: "attempt:verify", Relation: "tests", To: "objective:verify"},
			{Op: "create", Key: "solution:old", Type: "solution", Record: blackboardv2.SolutionRecord{Status: "verified", Kind: "flag", Summary: "Old accepted flag", Value: "FLAG{old}", VerificationSummary: "Accepted"}},
			{Op: "create", Key: "solution:replacement", Type: "solution", Record: blackboardv2.SolutionRecord{Status: "candidate", Kind: "flag", Summary: "Replacement candidate", Value: "FLAG{new}"}},
		},
	})
	if err != nil {
		t.Fatalf("seed Solution Evidence flow: %v", err)
	}
	workdir := filepath.Join(runtimeRoot, createdTask.ID, "workdir")
	if err := os.MkdirAll(workdir, 0o700); err != nil {
		t.Fatalf("create Task workdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(workdir, "accepted.txt"), []byte("accepted"), 0o600); err != nil {
		t.Fatalf("write Evidence source: %v", err)
	}
	_, err = service.RetainEvidenceForContinuation(ctx, createdProject.ID, continuation.ID, blackboardv2.RetainEvidenceRequest{
		IdempotencyKey: "retain-solution-evidence", Key: "evidence:accepted", Attempt: "attempt:verify",
		SourcePath: "accepted.txt", ArtifactType: "terminal_capture", Summary: "Challenge accepted the submitted flag",
		Links: []blackboardv2.EvidenceLink{{"evidences", "solution:old"}},
	})
	if err != nil {
		t.Fatalf("retain Evidence for Solution: %v", err)
	}
	detail, err := service.ReadCurrent(ctx, createdProject.ID, "solution:old")
	if err != nil {
		t.Fatalf("read evidenced Solution: %v", err)
	}
	assertContractJSON(t, mustHarness(t), "currentDetail", detail)

	_, err = service.Apply(ctx, createdProject.ID, blackboardv2.ChangeBatch{
		Schema: "semantic-change-batch/v2", IdempotencyKey: "invalid-solution-evidence-direction",
		Changes: []blackboardv2.Change{{Op: "relate", From: "solution:old", Relation: "evidences", To: "evidence:accepted"}},
	})
	if !isSemanticCode(err, "semantic_validation") {
		t.Fatalf("reversed evidences endpoint error = %#v", err)
	}

	_, err = service.Apply(ctx, createdProject.ID, blackboardv2.ChangeBatch{
		Schema: "semantic-change-batch/v2", IdempotencyKey: "supersede-verified-solution",
		Changes: []blackboardv2.Change{{Op: "supersede", Replacement: "solution:replacement", ReplacementVersion: 1, Replaced: "solution:old", ReplacedVersion: 1}},
	})
	if err != nil {
		t.Fatalf("supersede verified Solution: %v", err)
	}
	state, err := service.CTFSolvedState(ctx, createdProject.ID)
	if err != nil {
		t.Fatalf("read solved state after supersession: %v", err)
	}
	if state.Solved || len(state.VerifiedFlags) != 0 {
		t.Fatalf("candidate replacement left Project solved: %#v", state)
	}
	history, err := service.ReadHistory(ctx, createdProject.ID, "solution:old", blackboardv2.HistoryOptions{})
	if err != nil {
		t.Fatalf("read superseded Solution history: %v", err)
	}
	if len(history.Items) < 2 || history.Items[1].Record == nil || history.Items[1].Record.Status != "superseded" {
		t.Fatalf("superseded Solution history = %#v", history.Items)
	}
}
