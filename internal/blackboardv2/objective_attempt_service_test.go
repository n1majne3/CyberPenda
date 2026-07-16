package blackboardv2_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"pentest/internal/blackboardv2"
	"pentest/internal/project"
	"pentest/internal/store"
	"pentest/internal/task"
)

func TestOpenObjectivesAndAttemptsAreVersionedCurrentWork(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(filepath.Join(t.TempDir(), "pentest.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	createdProject, err := project.NewService(db).Create("Current Work", "", project.Scope{}, project.Defaults{})
	if err != nil {
		t.Fatalf("create project: %v", err)
	}
	service := blackboardv2.NewService(db)
	harness := mustHarness(t)

	created, err := service.Apply(ctx, createdProject.ID, blackboardv2.ChangeBatch{
		Schema:         "semantic-change-batch/v2",
		IdempotencyKey: "create-current-work",
		Changes: []blackboardv2.Change{
			{Op: "create", Key: "objective:zeta", Type: "objective", Record: blackboardv2.ObjectiveRecord{Status: "open", Objective: "Map the administrative login surface"}},
			{Op: "create", Key: "objective:alpha", Type: "objective", Record: blackboardv2.ObjectiveRecord{Status: "open", Objective: "Identify authentication entry points"}},
			{Op: "create", Key: "attempt:zeta", Type: "attempt", Record: blackboardv2.AttemptRecord{Status: "open", Summary: "Testing administrative login behavior"}},
			{Op: "create", Key: "attempt:alpha", Type: "attempt", Record: blackboardv2.AttemptRecord{Status: "open", Summary: "Enumerating authentication endpoints"}},
		},
	})
	if err != nil {
		t.Fatalf("create current work: %v", err)
	}
	assertChangeRecords(t, created, 4, [][]any{
		{"attempt:alpha", float64(1)},
		{"attempt:zeta", float64(1)},
		{"objective:alpha", float64(1)},
		{"objective:zeta", float64(1)},
	})

	updated, err := service.Apply(ctx, createdProject.ID, blackboardv2.ChangeBatch{
		Schema:         "semantic-change-batch/v2",
		IdempotencyKey: "update-current-work",
		Changes: []blackboardv2.Change{
			{Op: "update", Key: "objective:alpha", Version: 1, Type: "objective", Record: blackboardv2.ObjectivePatch{Objective: strPtr("Identify and classify authentication entry points")}},
			{Op: "update", Key: "attempt:alpha", Version: 1, Type: "attempt", Record: blackboardv2.AttemptPatch{Summary: strPtr("Enumerating and classifying authentication endpoints")}},
		},
	})
	if err != nil {
		t.Fatalf("update current work: %v", err)
	}
	assertChangeRecords(t, updated, 6, [][]any{{"attempt:alpha", float64(2)}, {"objective:alpha", float64(2)}})

	objective, err := service.ReadCurrent(ctx, createdProject.ID, "objective:alpha")
	if err != nil {
		t.Fatalf("read objective: %v", err)
	}
	if objective.Type != "objective" || objective.Version != 2 || objective.Record.Status != "open" || objective.Record.Objective != "Identify and classify authentication entry points" {
		t.Fatalf("objective detail = %#v", objective)
	}
	assertContractJSON(t, harness, "currentDetail", objective)

	attempt, err := service.ReadCurrent(ctx, createdProject.ID, "attempt:alpha")
	if err != nil {
		t.Fatalf("read attempt: %v", err)
	}
	if attempt.Type != "attempt" || attempt.Version != 2 || attempt.Record.Status != "open" || attempt.Record.Summary != "Enumerating and classifying authentication endpoints" {
		t.Fatalf("attempt detail = %#v", attempt)
	}
	assertContractJSON(t, harness, "currentDetail", attempt)

	objectiveHistory, err := service.ReadHistory(ctx, createdProject.ID, "objective:alpha", blackboardv2.HistoryOptions{})
	if err != nil {
		t.Fatalf("read objective history: %v", err)
	}
	if len(objectiveHistory.Items) != 1 || objectiveHistory.Items[0].Version != 1 || objectiveHistory.Items[0].Record.Objective != "Identify authentication entry points" {
		t.Fatalf("objective history = %#v", objectiveHistory.Items)
	}
	assertContractJSON(t, harness, "semanticHistory", objectiveHistory)

	snapshot, err := service.RuntimeSnapshot(ctx, createdProject.ID)
	if err != nil {
		t.Fatalf("runtime snapshot: %v", err)
	}
	wantSnapshot := `{"schema":"runtime-blackboard/v2","semantics":"work is active; knowledge is current; history and details are available by key","revision":6,"work":{"objectives":{"objective:alpha":{"version":2,"status":"open","objective":"Identify and classify authentication entry points"},"objective:zeta":{"version":1,"status":"open","objective":"Map the administrative login surface"}},"attempts":{"attempt:alpha":{"version":2,"status":"open","summary":"Enumerating and classifying authentication endpoints"},"attempt:zeta":{"version":1,"status":"open","summary":"Testing administrative login behavior"}}},"knowledge":{},"relations":[]}`
	if got := string(mustJSON(t, snapshot)); got != wantSnapshot {
		t.Fatalf("snapshot JSON = %s, want %s", got, wantSnapshot)
	}
	assertContractJSON(t, harness, "runtimeSnapshot", snapshot)
}

func TestTrustedContinuationOwnsAttemptsWithoutLeakingTaskState(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(filepath.Join(t.TempDir(), "pentest.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	projects := project.NewService(db)
	createdProject, err := projects.Create("Trusted Ownership", "", project.Scope{}, project.Defaults{})
	if err != nil {
		t.Fatalf("create project: %v", err)
	}
	otherProject, err := projects.Create("Other Project", "", project.Scope{}, project.Defaults{})
	if err != nil {
		t.Fatalf("create other project: %v", err)
	}
	tasks := task.NewService(db, projects)
	ownerTask, err := tasks.Create(task.CreateRequest{ProjectID: createdProject.ID, Goal: "OWNER TASK GOAL MUST NOT LEAK", RuntimeProfileID: "profile-owner", Runner: task.RunnerSandbox})
	if err != nil {
		t.Fatalf("create owner Task: %v", err)
	}
	owner, err := tasks.CreateContinuation(ownerTask.ID, "profile-owner", "codex", task.RunnerSandbox)
	if err != nil {
		t.Fatalf("create owner Continuation: %v", err)
	}
	otherTask, err := tasks.Create(task.CreateRequest{ProjectID: createdProject.ID, Goal: "OTHER TASK GOAL MUST NOT LEAK", RuntimeProfileID: "profile-other", Runner: task.RunnerSandbox})
	if err != nil {
		t.Fatalf("create other Task: %v", err)
	}
	other, err := tasks.CreateContinuation(otherTask.ID, "profile-other", "codex", task.RunnerSandbox)
	if err != nil {
		t.Fatalf("create other Continuation: %v", err)
	}
	foreignTask, err := tasks.Create(task.CreateRequest{ProjectID: otherProject.ID, Goal: "FOREIGN TASK GOAL MUST NOT LEAK", RuntimeProfileID: "profile-foreign", Runner: task.RunnerSandbox})
	if err != nil {
		t.Fatalf("create foreign Task: %v", err)
	}
	foreign, err := tasks.CreateContinuation(foreignTask.ID, "profile-foreign", "codex", task.RunnerSandbox)
	if err != nil {
		t.Fatalf("create foreign Continuation: %v", err)
	}
	service := blackboardv2.NewService(db)

	ownerCreate := blackboardv2.ChangeBatch{
		Schema:         "semantic-change-batch/v2",
		IdempotencyKey: "owner-create-attempt",
		Changes: []blackboardv2.Change{
			{Op: "create", Key: "objective:login", Type: "objective", Record: blackboardv2.ObjectiveRecord{Status: "open", Objective: "Test administrative login"}},
			{Op: "create", Key: "attempt:login", Type: "attempt", Record: blackboardv2.AttemptRecord{Status: "open", Summary: "Testing administrative login"}},
			{Op: "create", Key: "fact:outcome", Type: "fact", Record: blackboardv2.FactRecord{Category: "authentication", Summary: "Administrative login rejected the payload", Confidence: "tentative", ScopeStatus: "in_scope"}},
			{Op: "relate", From: "attempt:login", Relation: "tests", To: "objective:login"},
			{Op: "relate", From: "attempt:login", Relation: "produced", To: "fact:outcome"},
		},
	}
	if _, err := service.ApplyForContinuation(ctx, createdProject.ID, owner.ID, ownerCreate); err != nil {
		t.Fatalf("owner creates Attempt: %v", err)
	}

	for _, tt := range []struct {
		name           string
		continuationID string
		batch          blackboardv2.ChangeBatch
	}{
		{name: "other continuation update", continuationID: other.ID, batch: blackboardv2.ChangeBatch{Schema: "semantic-change-batch/v2", IdempotencyKey: "other-update", Changes: []blackboardv2.Change{{Op: "update", Key: "attempt:login", Version: 1, Type: "attempt", Record: blackboardv2.AttemptPatch{Summary: strPtr("Other continuation changed the Attempt")}}}}},
		{name: "other continuation relation", continuationID: other.ID, batch: blackboardv2.ChangeBatch{Schema: "semantic-change-batch/v2", IdempotencyKey: "other-relation", Changes: []blackboardv2.Change{{Op: "relate", From: "attempt:login", Relation: "tests", To: "fact:outcome"}}}},
		{name: "other continuation transition", continuationID: other.ID, batch: blackboardv2.ChangeBatch{Schema: "semantic-change-batch/v2", IdempotencyKey: "other-transition", Changes: []blackboardv2.Change{{Op: "transition", Key: "attempt:login", Version: 1, Status: "failed", Summary: "Other continuation tried to finish the Attempt"}}}},
		{name: "other continuation replay", continuationID: other.ID, batch: ownerCreate},
		{name: "foreign project continuation", continuationID: foreign.ID, batch: blackboardv2.ChangeBatch{Schema: "semantic-change-batch/v2", IdempotencyKey: "foreign-create", Changes: []blackboardv2.Change{{Op: "create", Key: "objective:foreign", Type: "objective", Record: blackboardv2.ObjectiveRecord{Status: "open", Objective: "Must not cross projects"}}}}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			_, err := service.ApplyForContinuation(ctx, createdProject.ID, tt.continuationID, tt.batch)
			var semanticErr *blackboardv2.Error
			if !errors.As(err, &semanticErr) || semanticErr.Code != "authority_denied" {
				t.Fatalf("ApplyForContinuation error = %#v, want authority_denied", err)
			}
		})
	}

	if _, err := service.ApplyForContinuation(ctx, createdProject.ID, owner.ID, blackboardv2.ChangeBatch{
		Schema:         "semantic-change-batch/v2",
		IdempotencyKey: "owner-update-attempt",
		Changes: []blackboardv2.Change{{
			Op: "update", Key: "attempt:login", Version: 1, Type: "attempt", Record: blackboardv2.AttemptPatch{Summary: strPtr("Owner continued testing administrative login")},
		}},
	}); err != nil {
		t.Fatalf("owner updates Attempt: %v", err)
	}
	if _, err := service.ApplyForContinuation(ctx, createdProject.ID, owner.ID, blackboardv2.ChangeBatch{
		Schema:         "semantic-change-batch/v2",
		IdempotencyKey: "owner-finishes-attempt",
		Changes: []blackboardv2.Change{{
			Op: "transition", Key: "attempt:login", Version: 2, Status: "failed", Summary: "Administrative login rejected the tested payload",
		}},
	}); err != nil {
		t.Fatalf("owner finishes Attempt: %v", err)
	}

	history, err := service.ReadHistory(ctx, createdProject.ID, "attempt:login", blackboardv2.HistoryOptions{})
	if err != nil {
		t.Fatalf("read owned Attempt history: %v", err)
	}
	snapshot, err := service.RuntimeSnapshot(ctx, createdProject.ID)
	if err != nil {
		t.Fatalf("runtime snapshot: %v", err)
	}
	visible := string(mustJSON(t, history)) + string(mustJSON(t, snapshot))
	for _, forbidden := range []string{owner.ID, other.ID, foreign.ID, ownerTask.ID, otherTask.ID, "OWNER TASK GOAL MUST NOT LEAK", "OTHER TASK GOAL MUST NOT LEAK", "FOREIGN TASK GOAL MUST NOT LEAK", "profile-owner", "profile-other"} {
		if strings.Contains(visible, forbidden) {
			t.Fatalf("Blackboard DTO leaked trusted Task/Continuation state %q: %s", forbidden, visible)
		}
	}
}

func TestServerReconcilesOnlyClosedContinuationsOwnedAttempts(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(filepath.Join(t.TempDir(), "pentest.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	projects := project.NewService(db)
	createdProject, err := projects.Create("Attempt Reconciliation", "", project.Scope{}, project.Defaults{})
	if err != nil {
		t.Fatalf("create project: %v", err)
	}
	foreignProject, err := projects.Create("Foreign Reconciliation", "", project.Scope{}, project.Defaults{})
	if err != nil {
		t.Fatalf("create foreign project: %v", err)
	}
	tasks := task.NewService(db, projects)
	ownerTask, err := tasks.Create(task.CreateRequest{ProjectID: createdProject.ID, Goal: "Owner", Runner: task.RunnerSandbox})
	if err != nil {
		t.Fatalf("create owner Task: %v", err)
	}
	owner, err := tasks.CreateContinuation(ownerTask.ID, "profile-owner", "codex", task.RunnerSandbox)
	if err != nil {
		t.Fatalf("create owner Continuation: %v", err)
	}
	if _, err := tasks.UpdateContinuationStatus(owner.ID, task.StatusRunning); err != nil {
		t.Fatalf("run owner Continuation: %v", err)
	}
	peerTask, err := tasks.Create(task.CreateRequest{ProjectID: createdProject.ID, Goal: "Peer", Runner: task.RunnerSandbox})
	if err != nil {
		t.Fatalf("create peer Task: %v", err)
	}
	peer, err := tasks.CreateContinuation(peerTask.ID, "profile-peer", "codex", task.RunnerSandbox)
	if err != nil {
		t.Fatalf("create peer Continuation: %v", err)
	}
	if _, err := tasks.UpdateContinuationStatus(peer.ID, task.StatusRunning); err != nil {
		t.Fatalf("run peer Continuation: %v", err)
	}
	foreignTask, err := tasks.Create(task.CreateRequest{ProjectID: foreignProject.ID, Goal: "Foreign", Runner: task.RunnerSandbox})
	if err != nil {
		t.Fatalf("create foreign Task: %v", err)
	}
	foreign, err := tasks.CreateContinuation(foreignTask.ID, "profile-foreign", "codex", task.RunnerSandbox)
	if err != nil {
		t.Fatalf("create foreign Continuation: %v", err)
	}
	if _, err := tasks.UpdateContinuationStatus(foreign.ID, task.StatusInterrupted); err != nil {
		t.Fatalf("interrupt foreign Continuation: %v", err)
	}
	service := blackboardv2.NewService(db)

	ownerCreate := blackboardv2.ChangeBatch{
		Schema:         "semantic-change-batch/v2",
		IdempotencyKey: "owner-reconciliation-work",
		Changes: []blackboardv2.Change{
			{Op: "create", Key: "objective:owner", Type: "objective", Record: blackboardv2.ObjectiveRecord{Status: "open", Objective: "Exercise owner targets"}},
			{Op: "create", Key: "attempt:owner-a", Type: "attempt", Record: blackboardv2.AttemptRecord{Status: "open", Summary: "Owner attempt A checkpoint"}},
			{Op: "create", Key: "attempt:owner-b", Type: "attempt", Record: blackboardv2.AttemptRecord{Status: "open", Summary: "Owner attempt B checkpoint"}},
			{Op: "create", Key: "attempt:owner-unstarted", Type: "attempt", Record: blackboardv2.AttemptRecord{Status: "open", Summary: "Owner attempt created before its target was linked"}},
			{Op: "relate", From: "attempt:owner-a", Relation: "tests", To: "objective:owner"},
			{Op: "relate", From: "attempt:owner-b", Relation: "tests", To: "objective:owner"},
		},
	}
	if _, err := service.ApplyForContinuation(ctx, createdProject.ID, owner.ID, ownerCreate); err != nil {
		t.Fatalf("create owner reconciliation work: %v", err)
	}
	if _, err := service.ApplyForContinuation(ctx, createdProject.ID, peer.ID, blackboardv2.ChangeBatch{
		Schema:         "semantic-change-batch/v2",
		IdempotencyKey: "peer-reconciliation-work",
		Changes: []blackboardv2.Change{
			{Op: "create", Key: "attempt:peer", Type: "attempt", Record: blackboardv2.AttemptRecord{Status: "open", Summary: "Peer attempt checkpoint"}},
			{Op: "relate", From: "attempt:peer", Relation: "tests", To: "objective:owner"},
		},
	}); err != nil {
		t.Fatalf("create peer reconciliation work: %v", err)
	}

	_, err = service.ApplyForContinuation(ctx, createdProject.ID, owner.ID, blackboardv2.ChangeBatch{
		Schema:         "semantic-change-batch/v2",
		IdempotencyKey: "runtime-self-interrupt",
		Changes: []blackboardv2.Change{{
			Op: "transition", Key: "attempt:owner-a", Version: 1, Status: "interrupted", Summary: "Runtime cannot choose interrupted",
		}},
	})
	if !isSemanticCode(err, "semantic_validation") {
		t.Fatalf("Runtime self-interruption error = %#v, want semantic_validation", err)
	}

	_, err = service.ReconcileContinuationAttempts(ctx, createdProject.ID, owner.ID)
	if !isSemanticCode(err, "continuation_not_closed") {
		t.Fatalf("running reconciliation error = %#v, want continuation_not_closed", err)
	}
	if _, err := service.ReconcileContinuationAttempts(ctx, createdProject.ID, foreign.ID); !isSemanticCode(err, "authority_denied") {
		t.Fatalf("cross-project reconciliation error = %#v, want authority_denied", err)
	}

	if _, err := tasks.UpdateContinuationStatus(owner.ID, task.StatusInterrupted); err != nil {
		t.Fatalf("interrupt owner Continuation: %v", err)
	}
	_, err = service.ApplyForContinuation(ctx, createdProject.ID, owner.ID, blackboardv2.ChangeBatch{
		Schema:         "semantic-change-batch/v2",
		IdempotencyKey: "closed-owner-write",
		Changes: []blackboardv2.Change{{
			Op: "update", Key: "attempt:owner-a", Version: 1, Type: "attempt", Record: blackboardv2.AttemptPatch{Summary: strPtr("Closed owner write")},
		}},
	})
	if !isSemanticCode(err, "closed_continuation") {
		t.Fatalf("closed Continuation write error = %#v, want closed_continuation", err)
	}
	if replay, err := service.ApplyForContinuation(ctx, createdProject.ID, owner.ID, ownerCreate); err != nil || replay.Revision != 6 {
		t.Fatalf("closed Continuation exact replay = %#v, %v", replay, err)
	}

	reconciled, err := service.ReconcileContinuationAttempts(ctx, createdProject.ID, owner.ID)
	if err != nil {
		t.Fatalf("reconcile interrupted owner: %v", err)
	}
	assertChangeRecords(t, reconciled, 10, [][]any{{"attempt:owner-a", float64(2)}, {"attempt:owner-b", float64(2)}})
	for _, tt := range []struct {
		key         string
		historySize int
	}{
		{key: "attempt:owner-a", historySize: 3},
		{key: "attempt:owner-b", historySize: 3},
	} {
		key := tt.key
		if _, err := service.ReadCurrent(ctx, createdProject.ID, key); !isSemanticCode(err, "not_found") {
			t.Fatalf("reconciled Attempt %s current read = %#v, want not_found", key, err)
		}
		history, err := service.ReadHistory(ctx, createdProject.ID, key, blackboardv2.HistoryOptions{})
		if err != nil {
			t.Fatalf("read reconciled history %s: %v", key, err)
		}
		if len(history.Items) != tt.historySize || history.Items[1].Record.Status != "interrupted" || strings.TrimSpace(history.Items[1].Record.Summary) == "" {
			t.Fatalf("reconciled history %s = %#v", key, history.Items)
		}
	}
	if untested, err := service.ReadCurrent(ctx, createdProject.ID, "attempt:owner-unstarted"); err != nil || untested.Record.Status != "open" {
		t.Fatalf("reconciliation terminalized untested Attempt = %#v, %v", untested, err)
	}
	if _, err := service.ReadCurrent(ctx, createdProject.ID, "attempt:peer"); err != nil {
		t.Fatalf("peer-owned Attempt was reconciled: %v", err)
	}

	replayReconciliation, err := service.ReconcileContinuationAttempts(ctx, createdProject.ID, owner.ID)
	if err != nil {
		t.Fatalf("replay reconciliation: %v", err)
	}
	assertChangeRecords(t, replayReconciliation, 10, [][]any{})

	if _, err := tasks.UpdateContinuationStatus(peer.ID, task.StatusCompleted); err != nil {
		t.Fatalf("complete peer Continuation: %v", err)
	}
	completed, err := service.ReconcileContinuationAttempts(ctx, createdProject.ID, peer.ID)
	if err != nil {
		t.Fatalf("reconcile completed peer: %v", err)
	}
	assertChangeRecords(t, completed, 10, [][]any{})
	if peerAttempt, err := service.ReadCurrent(ctx, createdProject.ID, "attempt:peer"); err != nil || peerAttempt.Record.Status != "open" {
		t.Fatalf("clean completion rewrote peer Attempt = %#v, %v", peerAttempt, err)
	}
}

func TestTrustedAttemptWritesRejectEveryTerminalContinuationStatus(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(filepath.Join(t.TempDir(), "pentest.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	projects := project.NewService(db)
	createdProject, err := projects.Create("Closed Continuations", "", project.Scope{}, project.Defaults{})
	if err != nil {
		t.Fatalf("create project: %v", err)
	}
	tasks := task.NewService(db, projects)
	service := blackboardv2.NewService(db)

	for _, status := range []task.Status{task.StatusCompleted, task.StatusFailed, task.StatusStopped, task.StatusInterrupted} {
		t.Run(string(status), func(t *testing.T) {
			createdTask, err := tasks.Create(task.CreateRequest{ProjectID: createdProject.ID, Goal: "Closed " + string(status), Runner: task.RunnerSandbox})
			if err != nil {
				t.Fatalf("create Task: %v", err)
			}
			continuation, err := tasks.CreateContinuation(createdTask.ID, "profile-"+string(status), "codex", task.RunnerSandbox)
			if err != nil {
				t.Fatalf("create Continuation: %v", err)
			}
			if _, err := tasks.UpdateContinuationStatus(continuation.ID, status); err != nil {
				t.Fatalf("set Continuation status: %v", err)
			}
			_, err = service.ApplyForContinuation(ctx, createdProject.ID, continuation.ID, blackboardv2.ChangeBatch{
				Schema:         "semantic-change-batch/v2",
				IdempotencyKey: "closed-attempt-write-" + string(status),
				Changes: []blackboardv2.Change{{
					Op: "create", Key: "attempt:closed-" + string(status), Type: "attempt", Record: blackboardv2.AttemptRecord{Status: "open", Summary: "Must not be created by a closed Continuation"},
				}},
			})
			if !isSemanticCode(err, "closed_continuation") {
				t.Fatalf("closed %s write error = %#v, want closed_continuation", status, err)
			}
			if _, err := service.ReadCurrent(ctx, createdProject.ID, "attempt:closed-"+string(status)); !isSemanticCode(err, "not_found") {
				t.Fatalf("closed %s Continuation created Attempt: %#v", status, err)
			}
		})
	}
}

func TestObjectiveLifecycleAndWorkflowChangeShapesAreClosed(t *testing.T) {
	for _, raw := range []string{
		`{"op":"create","key":"objective:bad","type":"objective","record":{"status":"open","objective":"Inspect login","task_goal":"copied goal"}}`,
		`{"op":"create","key":"objective:bad","type":"objective","record":{"status":"open","objective":"Inspect login","resolution_summary":""}}`,
		`{"op":"create","key":"attempt:bad","type":"attempt","record":{"status":"open","summary":"Inspect login","task_status":"running"}}`,
		`{"op":"update","key":"objective:bad","version":1,"type":"objective","record":{"status":"resolved"}}`,
		`{"op":"update","key":"attempt:bad","version":1,"type":"attempt","record":{"status":"failed"}}`,
		`{"op":"update","key":"objective:bad","version":1,"type":"objective","record":{}}`,
		`{"op":"update","key":"attempt:bad","version":1,"type":"attempt","record":{}}`,
		`{"op":"transition","key":"attempt:bad","version":1,"status":"failed","resolution_summary":"wrong terminal field"}`,
		`{"op":"transition","key":"objective:bad","version":1,"status":"resolved","summary":"wrong terminal field"}`,
	} {
		var change blackboardv2.Change
		if err := json.Unmarshal([]byte(raw), &change); err == nil {
			t.Fatalf("decoded non-closed workflow change: %s", raw)
		}
	}

	ctx := context.Background()
	db, err := store.Open(filepath.Join(t.TempDir(), "pentest.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	createdProject, err := project.NewService(db).Create("Objective Lifecycle", "", project.Scope{}, project.Defaults{})
	if err != nil {
		t.Fatalf("create project: %v", err)
	}
	service := blackboardv2.NewService(db)
	harness := mustHarness(t)

	_, err = service.Apply(ctx, createdProject.ID, blackboardv2.ChangeBatch{
		Schema:         "semantic-change-batch/v2",
		IdempotencyKey: "seed-objective-lifecycle",
		Changes: []blackboardv2.Change{
			{Op: "create", Key: "objective:old", Type: "objective", Record: blackboardv2.ObjectiveRecord{Status: "open", Objective: "Test the legacy login path"}},
			{Op: "create", Key: "objective:new", Type: "objective", Record: blackboardv2.ObjectiveRecord{Status: "open", Objective: "Test the replacement login path"}},
			{Op: "create", Key: "objective:abandon", Type: "objective", Record: blackboardv2.ObjectiveRecord{Status: "open", Objective: "Test a removed login path"}},
			{Op: "create", Key: "attempt:not-supersedable", Type: "attempt", Record: blackboardv2.AttemptRecord{Status: "open", Summary: "Attempt records conclude instead of being superseded"}},
		},
	})
	if err != nil {
		t.Fatalf("seed Objective lifecycle: %v", err)
	}

	_, err = service.Apply(ctx, createdProject.ID, blackboardv2.ChangeBatch{
		Schema:         "semantic-change-batch/v2",
		IdempotencyKey: "reject-terminal-create",
		Changes: []blackboardv2.Change{
			{Op: "create", Key: "objective:resolved-create", Type: "objective", Record: blackboardv2.ObjectiveRecord{Status: "resolved", Objective: "Invalid terminal create", ResolutionSummary: "Must transition"}},
		},
	})
	if !isSemanticCode(err, "semantic_validation") {
		t.Fatalf("terminal Objective create error = %#v, want semantic_validation", err)
	}

	for _, tt := range []struct {
		name   string
		change blackboardv2.Change
	}{
		{name: "Objective", change: blackboardv2.Change{Op: "update", Key: "objective:old", Version: 1, Type: "objective", Record: blackboardv2.ObjectivePatch{}}},
		{name: "Attempt", change: blackboardv2.Change{Op: "update", Key: "attempt:not-supersedable", Version: 1, Type: "attempt", Record: blackboardv2.AttemptPatch{}}},
	} {
		t.Run("rejects empty programmatic "+tt.name+" partial", func(t *testing.T) {
			_, err := service.Apply(ctx, createdProject.ID, blackboardv2.ChangeBatch{
				Schema:         "semantic-change-batch/v2",
				IdempotencyKey: "reject-empty-" + strings.ToLower(tt.name) + "-partial",
				Changes:        []blackboardv2.Change{tt.change},
			})
			if !isSemanticCode(err, "semantic_validation") {
				t.Fatalf("empty %s partial error = %#v, want semantic_validation", tt.name, err)
			}
		})
	}

	_, err = service.Apply(ctx, createdProject.ID, blackboardv2.ChangeBatch{
		Schema:         "semantic-change-batch/v2",
		IdempotencyKey: "reject-mixed-terminal-fields",
		Changes: []blackboardv2.Change{{
			Op: "transition", Key: "objective:abandon", Version: 1, Status: "abandoned", Summary: "Wrong field", ResolutionSummary: "The endpoint was removed before testing",
		}},
	})
	if !isSemanticCode(err, "semantic_validation") {
		t.Fatalf("mixed terminal fields error = %#v, want semantic_validation", err)
	}

	abandoned, err := service.Apply(ctx, createdProject.ID, blackboardv2.ChangeBatch{
		Schema:         "semantic-change-batch/v2",
		IdempotencyKey: "abandon-objective",
		Changes: []blackboardv2.Change{{
			Op: "transition", Key: "objective:abandon", Version: 1, Status: "abandoned", ResolutionSummary: "The endpoint was removed before testing",
		}},
	})
	if err != nil {
		t.Fatalf("abandon Objective: %v", err)
	}
	assertChangeRecords(t, abandoned, 5, [][]any{{"objective:abandon", float64(2)}})
	abandonedHistory, err := service.ReadHistory(ctx, createdProject.ID, "objective:abandon", blackboardv2.HistoryOptions{})
	if err != nil {
		t.Fatalf("read abandoned Objective history: %v", err)
	}
	if len(abandonedHistory.Items) != 2 || abandonedHistory.Items[1].Record.Status != "abandoned" || abandonedHistory.Items[1].Record.ResolutionSummary != "The endpoint was removed before testing" {
		t.Fatalf("abandoned Objective history = %#v", abandonedHistory.Items)
	}
	assertContractJSON(t, harness, "semanticHistory", abandonedHistory)
	noMeaningSnapshot, err := service.RuntimeSnapshot(ctx, createdProject.ID)
	if err != nil {
		t.Fatalf("snapshot after no-meaning abandonment: %v", err)
	}
	if len(noMeaningSnapshot.Knowledge.Facts) != 0 {
		t.Fatalf("no-meaning abandonment manufactured Facts: %#v", noMeaningSnapshot.Knowledge.Facts)
	}

	meaningfulAbandonment, err := service.Apply(ctx, createdProject.ID, blackboardv2.ChangeBatch{
		Schema:         "semantic-change-batch/v2",
		IdempotencyKey: "abandon-objective-with-reusable-meaning",
		Changes: []blackboardv2.Change{
			{Op: "create", Key: "objective:meaningful-abandonment", Type: "objective", Record: blackboardv2.ObjectiveRecord{Status: "open", Objective: "Test the retired administrative login endpoint"}},
			{Op: "create", Key: "fact:retired-admin-login", Type: "fact", Record: blackboardv2.FactRecord{Category: "authentication", Summary: "The legacy administrative login endpoint was removed from the current deployment", Confidence: "tentative", ScopeStatus: "in_scope"}},
			{Op: "relate", From: "objective:meaningful-abandonment", Relation: "derived_from", To: "fact:retired-admin-login"},
			{Op: "transition", Key: "objective:meaningful-abandonment", Version: 1, Status: "abandoned", ResolutionSummary: "The endpoint retirement is reusable deployment knowledge"},
		},
	})
	if err != nil {
		t.Fatalf("abandon Objective with reusable meaning: %v", err)
	}
	assertChangeRecords(t, meaningfulAbandonment, 9, [][]any{{"fact:retired-admin-login", float64(1)}, {"objective:meaningful-abandonment", float64(2)}})
	assertRelationResult(t, meaningfulAbandonment, [][]any{{"objective:meaningful-abandonment", "derived_from", "fact:retired-admin-login", float64(1)}})
	if fact, err := service.ReadCurrent(ctx, createdProject.ID, "fact:retired-admin-login"); err != nil || fact.Record.Summary != "The legacy administrative login endpoint was removed from the current deployment" {
		t.Fatalf("reusable abandonment Fact is not current = %#v, %v", fact, err)
	}
	meaningfulHistory, err := service.ReadHistory(ctx, createdProject.ID, "objective:meaningful-abandonment", blackboardv2.HistoryOptions{})
	if err != nil {
		t.Fatalf("read meaningful abandoned Objective history: %v", err)
	}
	if len(meaningfulHistory.Items) != 3 || meaningfulHistory.Items[2].Relation != "derived_from" || meaningfulHistory.Items[2].To != "fact:retired-admin-login" {
		t.Fatalf("meaningful abandoned Objective history = %#v", meaningfulHistory.Items)
	}

	superseded, err := service.Apply(ctx, createdProject.ID, blackboardv2.ChangeBatch{
		Schema:         "semantic-change-batch/v2",
		IdempotencyKey: "supersede-objective",
		Changes: []blackboardv2.Change{{
			Op: "supersede", Replacement: "objective:new", ReplacementVersion: 1, Replaced: "objective:old", ReplacedVersion: 1,
		}},
	})
	if err != nil {
		t.Fatalf("supersede Objective: %v", err)
	}
	assertChangeRecords(t, superseded, 10, [][]any{{"objective:old", float64(2)}})
	assertRelationResult(t, superseded, [][]any{{"objective:new", "supersedes", "objective:old", float64(1)}})

	_, err = service.Apply(ctx, createdProject.ID, blackboardv2.ChangeBatch{
		Schema:         "semantic-change-batch/v2",
		IdempotencyKey: "reject-attempt-supersede",
		Changes: []blackboardv2.Change{{
			Op: "supersede", Replacement: "attempt:not-supersedable", ReplacementVersion: 1, Replaced: "objective:new", ReplacedVersion: 1,
		}},
	})
	if !isSemanticCode(err, "semantic_validation") {
		t.Fatalf("Attempt supersede error = %#v, want semantic_validation", err)
	}

	history, err := service.ReadHistory(ctx, createdProject.ID, "objective:old", blackboardv2.HistoryOptions{})
	if err != nil {
		t.Fatalf("read superseded Objective history: %v", err)
	}
	if len(history.Items) != 3 || history.Items[1].Record.Status != "superseded" || history.Items[2].Relation != "supersedes" {
		t.Fatalf("superseded Objective history = %#v", history.Items)
	}
	assertContractJSON(t, harness, "semanticHistory", history)
}

func TestObjectiveAndAttemptUpdatesRequirePartialDTOs(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(filepath.Join(t.TempDir(), "pentest.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	createdProject, err := project.NewService(db).Create("Closed Work Update DTOs", "", project.Scope{}, project.Defaults{})
	if err != nil {
		t.Fatalf("create project: %v", err)
	}
	service := blackboardv2.NewService(db)

	_, err = service.Apply(ctx, createdProject.ID, blackboardv2.ChangeBatch{
		Schema:         "semantic-change-batch/v2",
		IdempotencyKey: "seed-closed-work-update-dtos",
		Changes: []blackboardv2.Change{
			{Op: "create", Key: "objective:record-value", Type: "objective", Record: blackboardv2.ObjectiveRecord{Status: "open", Objective: "Reject a complete Objective value"}},
			{Op: "create", Key: "objective:record-pointer", Type: "objective", Record: blackboardv2.ObjectiveRecord{Status: "open", Objective: "Reject a complete Objective pointer"}},
			{Op: "create", Key: "attempt:record-value", Type: "attempt", Record: blackboardv2.AttemptRecord{Status: "open", Summary: "Reject a complete Attempt value"}},
			{Op: "create", Key: "attempt:record-pointer", Type: "attempt", Record: blackboardv2.AttemptRecord{Status: "open", Summary: "Reject a complete Attempt pointer"}},
			{Op: "create", Key: "objective:patch-value", Type: "objective", Record: blackboardv2.ObjectiveRecord{Status: "open", Objective: "Accept an Objective patch value"}},
			{Op: "create", Key: "objective:patch-pointer", Type: "objective", Record: blackboardv2.ObjectiveRecord{Status: "open", Objective: "Accept an Objective patch pointer"}},
			{Op: "create", Key: "attempt:patch-value", Type: "attempt", Record: blackboardv2.AttemptRecord{Status: "open", Summary: "Accept an Attempt patch value"}},
			{Op: "create", Key: "attempt:patch-pointer", Type: "attempt", Record: blackboardv2.AttemptRecord{Status: "open", Summary: "Accept an Attempt patch pointer"}},
		},
	})
	if err != nil {
		t.Fatalf("seed closed work update DTOs: %v", err)
	}

	objectivePointer := &blackboardv2.ObjectiveRecord{Status: "open", Objective: "Complete Objective pointer must be rejected"}
	attemptPointer := &blackboardv2.AttemptRecord{Status: "open", Summary: "Complete Attempt pointer must be rejected"}
	for index, tt := range []struct {
		name   string
		change blackboardv2.Change
	}{
		{name: "Objective record value", change: blackboardv2.Change{Op: "update", Key: "objective:record-value", Version: 1, Type: "objective", Record: blackboardv2.ObjectiveRecord{Status: "open", Objective: "Complete Objective value must be rejected"}}},
		{name: "Objective record pointer", change: blackboardv2.Change{Op: "update", Key: "objective:record-pointer", Version: 1, Type: "objective", Record: objectivePointer}},
		{name: "Attempt record value", change: blackboardv2.Change{Op: "update", Key: "attempt:record-value", Version: 1, Type: "attempt", Record: blackboardv2.AttemptRecord{Status: "open", Summary: "Complete Attempt value must be rejected"}}},
		{name: "Attempt record pointer", change: blackboardv2.Change{Op: "update", Key: "attempt:record-pointer", Version: 1, Type: "attempt", Record: attemptPointer}},
	} {
		t.Run("rejects "+tt.name, func(t *testing.T) {
			_, err := service.Apply(ctx, createdProject.ID, blackboardv2.ChangeBatch{
				Schema:         "semantic-change-batch/v2",
				IdempotencyKey: fmt.Sprintf("reject-complete-work-update-%d", index),
				Changes:        []blackboardv2.Change{tt.change},
			})
			if !isSemanticCode(err, "semantic_validation") {
				t.Fatalf("complete update DTO error = %#v, want semantic_validation", err)
			}
		})
	}

	objectiveText := "Updated through an Objective patch pointer"
	attemptSummary := "Updated through an Attempt patch pointer"
	updated, err := service.Apply(ctx, createdProject.ID, blackboardv2.ChangeBatch{
		Schema:         "semantic-change-batch/v2",
		IdempotencyKey: "accept-work-partial-dtos",
		Changes: []blackboardv2.Change{
			{Op: "update", Key: "objective:patch-value", Version: 1, Type: "objective", Record: blackboardv2.ObjectivePatch{Objective: strPtr("Updated through an Objective patch value")}},
			{Op: "update", Key: "objective:patch-pointer", Version: 1, Type: "objective", Record: &blackboardv2.ObjectivePatch{Objective: &objectiveText}},
			{Op: "update", Key: "attempt:patch-value", Version: 1, Type: "attempt", Record: blackboardv2.AttemptPatch{Summary: strPtr("Updated through an Attempt patch value")}},
			{Op: "update", Key: "attempt:patch-pointer", Version: 1, Type: "attempt", Record: &blackboardv2.AttemptPatch{Summary: &attemptSummary}},
		},
	})
	if err != nil {
		t.Fatalf("apply valid work partial DTOs: %v", err)
	}
	assertChangeRecords(t, updated, 12, [][]any{
		{"attempt:patch-pointer", float64(2)},
		{"attempt:patch-value", float64(2)},
		{"objective:patch-pointer", float64(2)},
		{"objective:patch-value", float64(2)},
	})
}

func TestProgrammaticChangeShapesRejectEveryUnrelatedField(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(filepath.Join(t.TempDir(), "pentest.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	createdProject, err := project.NewService(db).Create("Closed Programmatic Changes", "", project.Scope{}, project.Defaults{})
	if err != nil {
		t.Fatalf("create project: %v", err)
	}
	service := blackboardv2.NewService(db)
	_, err = service.Apply(ctx, createdProject.ID, blackboardv2.ChangeBatch{
		Schema:         "semantic-change-batch/v2",
		IdempotencyKey: "seed-programmatic-change-shapes",
		Changes: []blackboardv2.Change{
			{Op: "create", Key: "objective:shape-target", Type: "objective", Record: blackboardv2.ObjectiveRecord{Status: "open", Objective: "Exercise closed programmatic operation shapes"}},
			{Op: "create", Key: "objective:shape-update", Type: "objective", Record: blackboardv2.ObjectiveRecord{Status: "open", Objective: "Remain unchanged when an unrelated field is ignored"}},
			{Op: "create", Key: "attempt:shape-transition", Type: "attempt", Record: blackboardv2.AttemptRecord{Status: "open", Summary: "Remain open behind a stale transition version"}},
			{Op: "create", Key: "objective:shape-replacement", Type: "objective", Record: blackboardv2.ObjectiveRecord{Status: "open", Objective: "Replacement Objective"}},
			{Op: "create", Key: "objective:shape-replaced", Type: "objective", Record: blackboardv2.ObjectiveRecord{Status: "open", Objective: "Replaced Objective"}},
		},
	})
	if err != nil {
		t.Fatalf("seed programmatic change shapes: %v", err)
	}

	type populatedField struct {
		name string
		set  func(*blackboardv2.Change)
	}
	fields := []populatedField{
		{name: "key", set: func(change *blackboardv2.Change) { change.Key = "objective:unrelated" }},
		{name: "version", set: func(change *blackboardv2.Change) { change.Version = 1 }},
		{name: "type", set: func(change *blackboardv2.Change) { change.Type = "objective" }},
		{name: "record", set: func(change *blackboardv2.Change) {
			change.Record = blackboardv2.ObjectivePatch{Objective: strPtr("Unrelated record")}
		}},
		{name: "clear", set: func(change *blackboardv2.Change) { change.Clear = []string{"body"} }},
		{name: "from", set: func(change *blackboardv2.Change) { change.From = "attempt:shape-transition" }},
		{name: "relation", set: func(change *blackboardv2.Change) { change.Relation = "tests" }},
		{name: "to", set: func(change *blackboardv2.Change) { change.To = "objective:shape-target" }},
		{name: "reason", set: func(change *blackboardv2.Change) { change.Reason = "Unrelated relationship reason" }},
		{name: "status", set: func(change *blackboardv2.Change) { change.Status = "failed" }},
		{name: "summary", set: func(change *blackboardv2.Change) { change.Summary = "Unrelated terminal summary" }},
		{name: "resolution_summary", set: func(change *blackboardv2.Change) { change.ResolutionSummary = "Unrelated resolution summary" }},
		{name: "replacement", set: func(change *blackboardv2.Change) { change.Replacement = "objective:shape-replacement" }},
		{name: "replacement_version", set: func(change *blackboardv2.Change) { change.ReplacementVersion = 1 }},
		{name: "replaced", set: func(change *blackboardv2.Change) { change.Replaced = "objective:shape-replaced" }},
		{name: "replaced_version", set: func(change *blackboardv2.Change) { change.ReplacedVersion = 1 }},
	}
	type operationShape struct {
		name    string
		allowed map[string]bool
		base    func(string) blackboardv2.Change
	}
	operations := []operationShape{
		{
			name:    "create",
			allowed: map[string]bool{"key": true, "type": true, "record": true},
			base: func(field string) blackboardv2.Change {
				return blackboardv2.Change{Op: "create", Key: "objective:shape-create-" + field, Type: "objective", Record: blackboardv2.ObjectiveRecord{Status: "open", Objective: "Must reject the unrelated create field"}}
			},
		},
		{
			name:    "update",
			allowed: map[string]bool{"key": true, "version": true, "type": true, "record": true, "clear": true},
			base: func(string) blackboardv2.Change {
				return blackboardv2.Change{Op: "update", Key: "objective:shape-update", Version: 1, Type: "objective", Record: blackboardv2.ObjectivePatch{Objective: strPtr("Remain unchanged when an unrelated field is ignored")}}
			},
		},
		{
			name:    "relate",
			allowed: map[string]bool{"version": true, "from": true, "relation": true, "to": true, "reason": true},
			base: func(string) blackboardv2.Change {
				return blackboardv2.Change{Op: "relate", From: "attempt:shape-transition", Relation: "tests", To: "objective:shape-target", Version: 1}
			},
		},
		{
			name:    "transition",
			allowed: map[string]bool{"key": true, "version": true, "status": true, "summary": true, "resolution_summary": true},
			base: func(string) blackboardv2.Change {
				return blackboardv2.Change{Op: "transition", Key: "attempt:shape-transition", Version: 99, Status: "failed", Summary: "Stale transition must not execute"}
			},
		},
		{
			name:    "supersede",
			allowed: map[string]bool{"replacement": true, "replacement_version": true, "replaced": true, "replaced_version": true},
			base: func(string) blackboardv2.Change {
				return blackboardv2.Change{Op: "supersede", Replacement: "objective:shape-replacement", ReplacementVersion: 1, Replaced: "objective:shape-replaced", ReplacedVersion: 99}
			},
		},
	}

	for _, operation := range operations {
		for _, field := range fields {
			if operation.allowed[field.name] {
				continue
			}
			t.Run(operation.name+" rejects "+field.name, func(t *testing.T) {
				change := operation.base(field.name)
				field.set(&change)
				_, err := service.Apply(ctx, createdProject.ID, blackboardv2.ChangeBatch{
					Schema:         "semantic-change-batch/v2",
					IdempotencyKey: "reject-programmatic-" + operation.name + "-" + field.name,
					Changes:        []blackboardv2.Change{change},
				})
				var semanticErr *blackboardv2.Error
				if !errors.As(err, &semanticErr) || semanticErr.Code != "semantic_validation" || semanticErr.Path != "changes[0]."+field.name {
					t.Fatalf("unrelated %s field error = %#v, want semantic_validation on changes[0].%s", field.name, err, field.name)
				}
			})
		}
	}
}

func TestSameBatchCreateAndSupersedeInfersReplacementVersion(t *testing.T) {
	var jsonSupersede blackboardv2.Change
	if err := json.Unmarshal([]byte(`{"op":"supersede","replacement":"objective:replacement-json","replaced":"objective:old-json","replaced_version":1}`), &jsonSupersede); err != nil {
		t.Fatalf("decode same-batch supersede without replacement_version: %v", err)
	}
	if jsonSupersede.ReplacementVersion != 0 {
		t.Fatalf("decoded replacement_version = %d, want omitted zero", jsonSupersede.ReplacementVersion)
	}

	ctx := context.Background()
	db, err := store.Open(filepath.Join(t.TempDir(), "pentest.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	createdProject, err := project.NewService(db).Create("Same Batch Supersede", "", project.Scope{}, project.Defaults{})
	if err != nil {
		t.Fatalf("create project: %v", err)
	}
	service := blackboardv2.NewService(db)
	harness := mustHarness(t)
	_, err = service.Apply(ctx, createdProject.ID, blackboardv2.ChangeBatch{
		Schema:         "semantic-change-batch/v2",
		IdempotencyKey: "seed-same-batch-supersede",
		Changes: []blackboardv2.Change{
			{Op: "create", Key: "objective:old-json", Type: "objective", Record: blackboardv2.ObjectiveRecord{Status: "open", Objective: "Old JSON Objective"}},
			{Op: "create", Key: "objective:old-programmatic", Type: "objective", Record: blackboardv2.ObjectiveRecord{Status: "open", Objective: "Old programmatic Objective"}},
			{Op: "create", Key: "objective:existing-replacement", Type: "objective", Record: blackboardv2.ObjectiveRecord{Status: "open", Objective: "Existing replacement"}},
			{Op: "create", Key: "objective:old-existing", Type: "objective", Record: blackboardv2.ObjectiveRecord{Status: "open", Objective: "Old existing Objective"}},
			{Op: "create", Key: "objective:old-stale", Type: "objective", Record: blackboardv2.ObjectiveRecord{Status: "open", Objective: "Old stale Objective"}},
		},
	})
	if err != nil {
		t.Fatalf("seed supersede records: %v", err)
	}

	jsonBatch := blackboardv2.ChangeBatch{
		Schema:         "semantic-change-batch/v2",
		IdempotencyKey: "same-batch-json-supersede",
		Changes: []blackboardv2.Change{
			{Op: "create", Key: "objective:replacement-json", Type: "objective", Record: blackboardv2.ObjectiveRecord{Status: "open", Objective: "Replacement JSON Objective"}},
			jsonSupersede,
		},
	}
	assertContractJSON(t, harness, "changeBatch", jsonBatch)
	jsonResult, err := service.Apply(ctx, createdProject.ID, jsonBatch)
	if err != nil {
		t.Fatalf("apply JSON same-batch supersede: %v", err)
	}
	assertChangeRecords(t, jsonResult, 7, [][]any{{"objective:old-json", float64(2)}, {"objective:replacement-json", float64(1)}})
	assertRelationResult(t, jsonResult, [][]any{{"objective:replacement-json", "supersedes", "objective:old-json", float64(1)}})

	programmaticResult, err := service.Apply(ctx, createdProject.ID, blackboardv2.ChangeBatch{
		Schema:         "semantic-change-batch/v2",
		IdempotencyKey: "same-batch-programmatic-supersede",
		Changes: []blackboardv2.Change{
			{Op: "create", Key: "objective:replacement-programmatic", Type: "objective", Record: blackboardv2.ObjectiveRecord{Status: "open", Objective: "Replacement programmatic Objective"}},
			{Op: "supersede", Replacement: "objective:replacement-programmatic", Replaced: "objective:old-programmatic", ReplacedVersion: 1},
		},
	})
	if err != nil {
		t.Fatalf("apply programmatic same-batch supersede: %v", err)
	}
	assertChangeRecords(t, programmaticResult, 9, [][]any{{"objective:old-programmatic", float64(2)}, {"objective:replacement-programmatic", float64(1)}})

	_, err = service.Apply(ctx, createdProject.ID, blackboardv2.ChangeBatch{
		Schema:         "semantic-change-batch/v2",
		IdempotencyKey: "reject-omitted-preexisting-replacement-version",
		Changes: []blackboardv2.Change{
			{Op: "create", Key: "objective:atomic-marker", Type: "objective", Record: blackboardv2.ObjectiveRecord{Status: "open", Objective: "Must roll back"}},
			{Op: "supersede", Replacement: "objective:existing-replacement", Replaced: "objective:old-existing", ReplacedVersion: 1},
		},
	})
	if !isSemanticCode(err, "semantic_validation") {
		t.Fatalf("pre-existing omitted replacement_version error = %#v, want semantic_validation", err)
	}
	if _, err := service.ReadCurrent(ctx, createdProject.ID, "objective:atomic-marker"); !isSemanticCode(err, "not_found") {
		t.Fatalf("failed omitted-version batch retained atomic marker: %#v", err)
	}

	_, err = service.Apply(ctx, createdProject.ID, blackboardv2.ChangeBatch{
		Schema:         "semantic-change-batch/v2",
		IdempotencyKey: "rollback-created-replacement-on-stale-replaced",
		Changes: []blackboardv2.Change{
			{Op: "create", Key: "objective:replacement-stale", Type: "objective", Record: blackboardv2.ObjectiveRecord{Status: "open", Objective: "Must roll back after stale replaced version"}},
			{Op: "supersede", Replacement: "objective:replacement-stale", Replaced: "objective:old-stale", ReplacedVersion: 99},
		},
	})
	if !isSemanticCode(err, "version_conflict") {
		t.Fatalf("stale same-batch supersede error = %#v, want version_conflict", err)
	}
	if _, err := service.ReadCurrent(ctx, createdProject.ID, "objective:replacement-stale"); !isSemanticCode(err, "not_found") {
		t.Fatalf("stale same-batch supersede retained replacement: %#v", err)
	}
}

func TestObjectiveAndAttemptRelationshipsEnforceEndpointsAndCycles(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(filepath.Join(t.TempDir(), "pentest.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	createdProject, err := project.NewService(db).Create("Work Relationships", "", project.Scope{}, project.Defaults{})
	if err != nil {
		t.Fatalf("create project: %v", err)
	}
	service := blackboardv2.NewService(db)

	_, err = service.Apply(ctx, createdProject.ID, blackboardv2.ChangeBatch{
		Schema:         "semantic-change-batch/v2",
		IdempotencyKey: "seed-work-relationships",
		Changes: []blackboardv2.Change{
			{Op: "create", Key: "objective:parent", Type: "objective", Record: blackboardv2.ObjectiveRecord{Status: "open", Objective: "Understand the authentication boundary"}},
			{Op: "create", Key: "objective:child", Type: "objective", Record: blackboardv2.ObjectiveRecord{Status: "open", Objective: "Test the administrative login"}},
			{Op: "create", Key: "attempt:login", Type: "attempt", Record: blackboardv2.AttemptRecord{Status: "open", Summary: "Testing administrative login behavior"}},
			{Op: "create", Key: "entity:login", Type: "entity", Record: blackboardv2.EntityRecord{Status: "active", Kind: "endpoint", Name: "POST /admin/login", ScopeStatus: "in_scope"}},
			{Op: "create", Key: "fact:login", Type: "fact", Record: blackboardv2.FactRecord{Category: "authentication", Summary: "Administrative login accepts JSON", Confidence: "tentative", ScopeStatus: "in_scope"}},
		},
	})
	if err != nil {
		t.Fatalf("seed work relationships: %v", err)
	}

	related, err := service.Apply(ctx, createdProject.ID, blackboardv2.ChangeBatch{
		Schema:         "semantic-change-batch/v2",
		IdempotencyKey: "relate-work",
		Changes: []blackboardv2.Change{
			{Op: "relate", From: "objective:child", Relation: "part_of", To: "objective:parent"},
			{Op: "relate", From: "objective:child", Relation: "derived_from", To: "fact:login"},
			{Op: "relate", From: "objective:child", Relation: "depends_on", To: "objective:parent", Reason: "The boundary must be understood first"},
			{Op: "relate", From: "attempt:login", Relation: "tests", To: "objective:child"},
			{Op: "relate", From: "attempt:login", Relation: "tests", To: "entity:login"},
			{Op: "relate", From: "attempt:login", Relation: "produced", To: "fact:login"},
		},
	})
	if err != nil {
		t.Fatalf("relate work: %v", err)
	}
	assertRelationResult(t, related, [][]any{
		{"attempt:login", "produced", "fact:login", float64(1)},
		{"attempt:login", "tests", "entity:login", float64(1)},
		{"attempt:login", "tests", "objective:child", float64(1)},
		{"objective:child", "depends_on", "objective:parent", float64(1)},
		{"objective:child", "derived_from", "fact:login", float64(1)},
		{"objective:child", "part_of", "objective:parent", float64(1)},
	})

	for _, tt := range []struct {
		name     string
		relation blackboardv2.Change
	}{
		{name: "objective part_of requires objective target", relation: blackboardv2.Change{Op: "relate", From: "objective:parent", Relation: "part_of", To: "entity:login"}},
		{name: "objective derived_from requires knowledge target", relation: blackboardv2.Change{Op: "relate", From: "objective:parent", Relation: "derived_from", To: "entity:login"}},
		{name: "attempt tests rejects attempt target", relation: blackboardv2.Change{Op: "relate", From: "attempt:login", Relation: "tests", To: "attempt:login"}},
		{name: "attempt produced rejects attempt target", relation: blackboardv2.Change{Op: "relate", From: "attempt:login", Relation: "produced", To: "attempt:login"}},
		{name: "part_of cycle", relation: blackboardv2.Change{Op: "relate", From: "objective:parent", Relation: "part_of", To: "objective:child"}},
		{name: "depends_on cycle", relation: blackboardv2.Change{Op: "relate", From: "objective:parent", Relation: "depends_on", To: "objective:child"}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			_, err := service.Apply(ctx, createdProject.ID, blackboardv2.ChangeBatch{
				Schema:         "semantic-change-batch/v2",
				IdempotencyKey: "reject-" + tt.name,
				Changes:        []blackboardv2.Change{tt.relation},
			})
			var semanticErr *blackboardv2.Error
			if !errors.As(err, &semanticErr) || semanticErr.Code != "semantic_validation" {
				t.Fatalf("Apply error = %#v, want semantic_validation", err)
			}
		})
	}
}

func TestAttemptAndObjectiveTerminalGuardsPreserveSemanticHistory(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(filepath.Join(t.TempDir(), "pentest.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	createdProject, err := project.NewService(db).Create("Terminal Work", "", project.Scope{}, project.Defaults{})
	if err != nil {
		t.Fatalf("create project: %v", err)
	}
	service := blackboardv2.NewService(db)
	harness := mustHarness(t)

	_, err = service.Apply(ctx, createdProject.ID, blackboardv2.ChangeBatch{
		Schema:         "semantic-change-batch/v2",
		IdempotencyKey: "seed-terminal-work-valid",
		Changes: []blackboardv2.Change{
			{Op: "create", Key: "objective:login", Type: "objective", Record: blackboardv2.ObjectiveRecord{Status: "open", Objective: "Determine whether administrative login can be bypassed"}},
			{Op: "create", Key: "objective:follow-up", Type: "objective", Record: blackboardv2.ObjectiveRecord{Status: "open", Objective: "Assess post-authentication administrative access"}},
			{Op: "create", Key: "objective:unsatisfied", Type: "objective", Record: blackboardv2.ObjectiveRecord{Status: "open", Objective: "Resolve only with semantic support"}},
			{Op: "create", Key: "attempt:login", Type: "attempt", Record: blackboardv2.AttemptRecord{Status: "open", Summary: "Testing administrative login bypasses"}},
			{Op: "create", Key: "attempt:no-tests", Type: "attempt", Record: blackboardv2.AttemptRecord{Status: "open", Summary: "Collecting an outcome without a tested target"}},
			{Op: "create", Key: "attempt:no-outcome", Type: "attempt", Record: blackboardv2.AttemptRecord{Status: "open", Summary: "Testing without retaining a reusable outcome"}},
			{Op: "create", Key: "entity:login", Type: "entity", Record: blackboardv2.EntityRecord{Status: "active", Kind: "endpoint", Name: "POST /admin/login", ScopeStatus: "in_scope"}},
			{Op: "create", Key: "fact:login-outcome", Type: "fact", Record: blackboardv2.FactRecord{Category: "authentication", Summary: "Administrative login rejected the tested bypass payloads", Confidence: "tentative", ScopeStatus: "in_scope"}},
			{Op: "relate", From: "objective:follow-up", Relation: "part_of", To: "objective:login"},
			{Op: "relate", From: "objective:follow-up", Relation: "depends_on", To: "objective:login"},
			{Op: "relate", From: "attempt:login", Relation: "tests", To: "objective:login"},
			{Op: "relate", From: "attempt:login", Relation: "tests", To: "entity:login"},
			{Op: "relate", From: "attempt:login", Relation: "produced", To: "fact:login-outcome"},
			{Op: "relate", From: "attempt:no-tests", Relation: "produced", To: "fact:login-outcome"},
			{Op: "relate", From: "attempt:no-outcome", Relation: "tests", To: "entity:login"},
			{Op: "relate", From: "fact:login-outcome", Relation: "satisfies", To: "objective:login"},
		},
	})
	if err != nil {
		t.Fatalf("seed terminal work: %v", err)
	}

	for _, tt := range []struct {
		key     string
		version int
		status  string
		summary string
	}{
		{key: "attempt:no-tests", version: 1, status: "failed", summary: "No tested target was recorded"},
		{key: "attempt:login", version: 1, status: "interrupted", summary: "Runtime stopped unexpectedly"},
	} {
		_, err := service.Apply(ctx, createdProject.ID, blackboardv2.ChangeBatch{
			Schema:         "semantic-change-batch/v2",
			IdempotencyKey: "reject-terminal-" + tt.key + "-" + tt.status,
			Changes: []blackboardv2.Change{{
				Op: "transition", Key: tt.key, Version: tt.version, Status: tt.status, Summary: tt.summary,
			}},
		})
		var semanticErr *blackboardv2.Error
		if !errors.As(err, &semanticErr) || semanticErr.Code != "semantic_validation" {
			t.Fatalf("terminal guard for %s = %#v, want semantic_validation", tt.key, err)
		}
	}

	_, err = service.Apply(ctx, createdProject.ID, blackboardv2.ChangeBatch{
		Schema:         "semantic-change-batch/v2",
		IdempotencyKey: "reject-unsatisfied-objective",
		Changes: []blackboardv2.Change{{
			Op: "transition", Key: "objective:unsatisfied", Version: 1, Status: "resolved", ResolutionSummary: "No current satisfying knowledge exists",
		}},
	})
	var semanticErr *blackboardv2.Error
	if !errors.As(err, &semanticErr) || semanticErr.Code != "semantic_validation" {
		t.Fatalf("unsatisfied resolution error = %#v, want semantic_validation", err)
	}

	terminalAttempt, err := service.Apply(ctx, createdProject.ID, blackboardv2.ChangeBatch{
		Schema:         "semantic-change-batch/v2",
		IdempotencyKey: "finish-login-attempt",
		Changes: []blackboardv2.Change{{
			Op: "transition", Key: "attempt:login", Version: 1, Status: "failed", Summary: "The tested bypass payloads were rejected by administrative login",
		}},
	})
	if err != nil {
		t.Fatalf("finish attempt: %v", err)
	}
	assertChangeRecords(t, terminalAttempt, 17, [][]any{{"attempt:login", float64(2)}})
	if _, err := service.ReadCurrent(ctx, createdProject.ID, "attempt:login"); !isSemanticCode(err, "not_found") {
		t.Fatalf("terminal Attempt current read = %#v, want not_found", err)
	}
	attemptHistory, err := service.ReadHistory(ctx, createdProject.ID, "attempt:login", blackboardv2.HistoryOptions{})
	if err != nil {
		t.Fatalf("read Attempt history: %v", err)
	}
	if len(attemptHistory.Items) != 5 || attemptHistory.Items[1].Version != 2 || attemptHistory.Items[1].Record.Status != "failed" || attemptHistory.Items[1].Record.Summary != "The tested bypass payloads were rejected by administrative login" {
		t.Fatalf("Attempt history = %#v", attemptHistory.Items)
	}
	if attemptHistory.Items[2].Relation != "produced" || attemptHistory.Items[2].To != "fact:login-outcome" {
		t.Fatalf("Attempt reusable outcome history = %#v", attemptHistory.Items[2])
	}
	assertContractJSON(t, harness, "semanticHistory", attemptHistory)

	terminalObjective, err := service.Apply(ctx, createdProject.ID, blackboardv2.ChangeBatch{
		Schema:         "semantic-change-batch/v2",
		IdempotencyKey: "resolve-login-objective",
		Changes: []blackboardv2.Change{{
			Op: "transition", Key: "objective:login", Version: 1, Status: "resolved", ResolutionSummary: "The tested bypasses failed and the retained Fact records the result",
		}},
	})
	if err != nil {
		t.Fatalf("resolve Objective: %v", err)
	}
	assertChangeRecords(t, terminalObjective, 18, [][]any{{"objective:login", float64(2)}})
	if _, err := service.ReadCurrent(ctx, createdProject.ID, "objective:login"); !isSemanticCode(err, "not_found") {
		t.Fatalf("terminal Objective current read = %#v, want not_found", err)
	}
	followUp, err := service.ReadCurrent(ctx, createdProject.ID, "objective:follow-up")
	if err != nil {
		t.Fatalf("read child Objective: %v", err)
	}
	if followUp.Record.Status != "open" {
		t.Fatalf("child Objective status = %q, want open", followUp.Record.Status)
	}

	objectiveHistory, err := service.ReadHistory(ctx, createdProject.ID, "objective:login", blackboardv2.HistoryOptions{})
	if err != nil {
		t.Fatalf("read Objective history: %v", err)
	}
	if len(objectiveHistory.Items) != 6 || objectiveHistory.Items[1].Version != 2 || objectiveHistory.Items[1].Record.Status != "resolved" || objectiveHistory.Items[1].Record.ResolutionSummary == "" {
		t.Fatalf("Objective history = %#v", objectiveHistory.Items)
	}
	assertContractJSON(t, harness, "semanticHistory", objectiveHistory)

	snapshot, err := service.RuntimeSnapshot(ctx, createdProject.ID)
	if err != nil {
		t.Fatalf("runtime snapshot: %v", err)
	}
	snapshotJSON := string(mustJSON(t, snapshot))
	for _, terminalKey := range []string{"attempt:login", "objective:login"} {
		if contains := strings.Contains(snapshotJSON, terminalKey); contains {
			t.Fatalf("snapshot retained terminal key %q: %s", terminalKey, snapshotJSON)
		}
	}
	for _, openKey := range []string{"attempt:no-outcome", "attempt:no-tests", "objective:follow-up", "objective:unsatisfied"} {
		if !strings.Contains(snapshotJSON, openKey) {
			t.Fatalf("snapshot omitted open key %q: %s", openKey, snapshotJSON)
		}
	}
	assertContractJSON(t, harness, "runtimeSnapshot", snapshot)
}

func TestTerminalAttemptDoesNotRequireProducedOutcome(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(filepath.Join(t.TempDir(), "pentest.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	createdProject, err := project.NewService(db).Create("No Reusable Attempt Outcome", "", project.Scope{}, project.Defaults{})
	if err != nil {
		t.Fatalf("create project: %v", err)
	}
	service := blackboardv2.NewService(db)

	_, err = service.Apply(ctx, createdProject.ID, blackboardv2.ChangeBatch{
		Schema:         "semantic-change-batch/v2",
		IdempotencyKey: "seed-no-outcome-attempts",
		Changes: []blackboardv2.Change{
			{Op: "create", Key: "objective:target", Type: "objective", Record: blackboardv2.ObjectiveRecord{Status: "open", Objective: "Exercise a target with no reusable result"}},
			{Op: "create", Key: "attempt:blocked", Type: "attempt", Record: blackboardv2.AttemptRecord{Status: "open", Summary: "Testing stopped at an authorization boundary"}},
			{Op: "create", Key: "attempt:inconclusive", Type: "attempt", Record: blackboardv2.AttemptRecord{Status: "open", Summary: "Testing could not distinguish the observed behavior"}},
			{Op: "create", Key: "attempt:failed", Type: "attempt", Record: blackboardv2.AttemptRecord{Status: "open", Summary: "Testing did not reproduce the suspected behavior"}},
			{Op: "relate", From: "attempt:blocked", Relation: "tests", To: "objective:target"},
			{Op: "relate", From: "attempt:inconclusive", Relation: "tests", To: "objective:target"},
			{Op: "relate", From: "attempt:failed", Relation: "tests", To: "objective:target"},
		},
	})
	if err != nil {
		t.Fatalf("seed Attempts without outcomes: %v", err)
	}

	for index, tt := range []struct {
		key     string
		status  string
		summary string
	}{
		{key: "attempt:blocked", status: "blocked", summary: "Authorization prevented the tested action and no reusable result was produced"},
		{key: "attempt:inconclusive", status: "inconclusive", summary: "The tested behavior remained ambiguous and yielded no reusable result"},
		{key: "attempt:failed", status: "failed", summary: "The tested behavior was not reproduced and yielded no reusable result"},
	} {
		result, err := service.Apply(ctx, createdProject.ID, blackboardv2.ChangeBatch{
			Schema:         "semantic-change-batch/v2",
			IdempotencyKey: "terminal-no-outcome-" + tt.status,
			Changes: []blackboardv2.Change{{
				Op: "transition", Key: tt.key, Version: 1, Status: tt.status, Summary: tt.summary,
			}},
		})
		if err != nil {
			t.Fatalf("terminal %s Attempt without outcome: %v", tt.status, err)
		}
		assertChangeRecords(t, result, 8+index, [][]any{{tt.key, float64(2)}})
		history, err := service.ReadHistory(ctx, createdProject.ID, tt.key, blackboardv2.HistoryOptions{})
		if err != nil {
			t.Fatalf("read %s Attempt history: %v", tt.status, err)
		}
		if len(history.Items) != 3 || history.Items[1].Record.Status != tt.status || history.Items[1].Record.Summary != tt.summary || history.Items[2].Relation != "tests" {
			t.Fatalf("%s Attempt history = %#v", tt.status, history.Items)
		}
	}
}

func TestSucceededAttemptRequiresProducedOutcomeInFinalBatch(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(filepath.Join(t.TempDir(), "pentest.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	createdProject, err := project.NewService(db).Create("Successful Attempt Outcome", "", project.Scope{}, project.Defaults{})
	if err != nil {
		t.Fatalf("create project: %v", err)
	}
	service := blackboardv2.NewService(db)

	_, err = service.Apply(ctx, createdProject.ID, blackboardv2.ChangeBatch{
		Schema:         "semantic-change-batch/v2",
		IdempotencyKey: "seed-success-outcome-guard",
		Changes: []blackboardv2.Change{
			{Op: "create", Key: "objective:target", Type: "objective", Record: blackboardv2.ObjectiveRecord{Status: "open", Objective: "Establish a reusable authentication conclusion"}},
			{Op: "create", Key: "attempt:missing-outcome", Type: "attempt", Record: blackboardv2.AttemptRecord{Status: "open", Summary: "Testing authentication behavior"}},
			{Op: "relate", From: "attempt:missing-outcome", Relation: "tests", To: "objective:target"},
		},
	})
	if err != nil {
		t.Fatalf("seed success outcome guard: %v", err)
	}

	_, err = service.Apply(ctx, createdProject.ID, blackboardv2.ChangeBatch{
		Schema:         "semantic-change-batch/v2",
		IdempotencyKey: "reject-success-without-outcome",
		Changes: []blackboardv2.Change{
			{Op: "create", Key: "fact:must-roll-back", Type: "fact", Record: blackboardv2.FactRecord{Category: "authentication", Summary: "This Fact must roll back with the invalid success", Confidence: "tentative", ScopeStatus: "in_scope"}},
			{Op: "transition", Key: "attempt:missing-outcome", Version: 1, Status: "succeeded", Summary: "Authentication testing succeeded without a reusable outcome"},
		},
	})
	if !isSemanticCode(err, "semantic_validation") {
		t.Fatalf("succeeded Attempt without produced error = %#v, want semantic_validation", err)
	}
	if _, err := service.ReadCurrent(ctx, createdProject.ID, "fact:must-roll-back"); !isSemanticCode(err, "not_found") {
		t.Fatalf("invalid success batch retained Fact: %#v", err)
	}
	if attempt, err := service.ReadCurrent(ctx, createdProject.ID, "attempt:missing-outcome"); err != nil || attempt.Version != 1 || attempt.Record.Status != "open" {
		t.Fatalf("invalid success batch changed Attempt = %#v, %v", attempt, err)
	}

	succeeded, err := service.Apply(ctx, createdProject.ID, blackboardv2.ChangeBatch{
		Schema:         "semantic-change-batch/v2",
		IdempotencyKey: "succeed-with-final-batch-outcome",
		Changes: []blackboardv2.Change{
			{Op: "create", Key: "attempt:with-outcome", Type: "attempt", Record: blackboardv2.AttemptRecord{Status: "open", Summary: "Testing administrative authentication"}},
			{Op: "create", Key: "fact:admin-auth", Type: "fact", Record: blackboardv2.FactRecord{Category: "authentication", Summary: "Administrative authentication rejects invalid credentials", Confidence: "tentative", ScopeStatus: "in_scope"}},
			{Op: "relate", From: "attempt:with-outcome", Relation: "tests", To: "objective:target"},
			{Op: "relate", From: "attempt:with-outcome", Relation: "produced", To: "fact:admin-auth"},
			{Op: "transition", Key: "attempt:with-outcome", Version: 1, Status: "succeeded", Summary: "Administrative authentication rejected the tested invalid credentials"},
		},
	})
	if err != nil {
		t.Fatalf("succeed Attempt with same-batch outcome: %v", err)
	}
	assertChangeRecords(t, succeeded, 8, [][]any{{"attempt:with-outcome", float64(2)}, {"fact:admin-auth", float64(1)}})
	assertRelationResult(t, succeeded, [][]any{
		{"attempt:with-outcome", "produced", "fact:admin-auth", float64(1)},
		{"attempt:with-outcome", "tests", "objective:target", float64(1)},
	})
	if fact, err := service.ReadCurrent(ctx, createdProject.ID, "fact:admin-auth"); err != nil || fact.Record.Summary != "Administrative authentication rejects invalid credentials" {
		t.Fatalf("successful Attempt outcome is not current = %#v, %v", fact, err)
	}
	history, err := service.ReadHistory(ctx, createdProject.ID, "attempt:with-outcome", blackboardv2.HistoryOptions{})
	if err != nil {
		t.Fatalf("read succeeded Attempt history: %v", err)
	}
	if len(history.Items) != 4 || history.Items[1].Record.Status != "succeeded" || history.Items[2].Relation != "produced" || history.Items[2].To != "fact:admin-auth" {
		t.Fatalf("succeeded Attempt history = %#v", history.Items)
	}
}
