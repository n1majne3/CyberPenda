package blackboardv2_test

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"pentest/internal/blackboardv2"
	"pentest/internal/project"
	"pentest/internal/session"
	"pentest/internal/store"
)

func TestSessionBlackboardOwnerAdapterSharesV2SemanticsWithoutProjectScope(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(filepath.Join(t.TempDir(), "session-blackboard.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	sessions := session.NewService(db, filepath.Join(t.TempDir(), "sessions"))
	first, err := sessions.Create(session.CreateRequest{Input: "first Session"})
	if err != nil {
		t.Fatalf("create first Session: %v", err)
	}
	second, err := sessions.Create(session.CreateRequest{Input: "second Session"})
	if err != nil {
		t.Fatalf("create second Session: %v", err)
	}
	projects := project.NewService(db)
	projectOwner, err := projects.Create("Project owner", "", project.Scope{}, project.Defaults{})
	if err != nil {
		t.Fatalf("create Project: %v", err)
	}
	board := blackboardv2.NewService(db)
	firstOwner := first.OwnerContract()
	if err := firstOwner.Validate(); err != nil {
		t.Fatalf("Session owner contract: %v", err)
	}

	seed := blackboardv2.ChangeBatch{
		Schema: "semantic-change-batch/v2", IdempotencyKey: "seed-session",
		Changes: []blackboardv2.Change{
			{Op: "create", Key: "entity:note", Type: "entity", Record: blackboardv2.EntityRecord{Status: "active", Kind: "host", Name: "session.example"}},
			{Op: "create", Key: "objective:remember", Type: "objective", Record: blackboardv2.ObjectiveRecord{Status: "open", Objective: "Remember Session state"}},
			{Op: "create", Key: "attempt:remember", Type: "attempt", Record: blackboardv2.AttemptRecord{Status: "open", Summary: "Capture Session state"}},
			{Op: "create", Key: "fact:note", Type: "fact", Record: blackboardv2.FactRecord{Category: "note", Summary: "A Session-local fact", Confidence: "tentative"}},
			{Op: "relate", From: "attempt:remember", Relation: "tests", To: "objective:remember"},
			{Op: "relate", From: "attempt:remember", Relation: "produced", To: "fact:note"},
		},
	}
	result, err := board.ApplyForOwner(ctx, firstOwner, seed)
	if err != nil {
		t.Fatalf("seed Session Blackboard: %v", err)
	}
	if result.Revision != 6 || len(result.Records) != 4 || len(result.Relations) != 2 {
		t.Fatalf("seed result = %#v", result)
	}
	replay, err := board.ApplyForOwner(ctx, firstOwner, seed)
	if err != nil || !reflect.DeepEqual(replay, result) {
		t.Fatalf("Session replay = %#v, %v; want %#v", replay, err, result)
	}

	snapshot, err := board.RuntimeSnapshotForOwner(ctx, firstOwner)
	if err != nil {
		t.Fatalf("Session snapshot: %v", err)
	}
	snapshotJSON, err := json.Marshal(snapshot)
	if err != nil {
		t.Fatalf("encode Session snapshot: %v", err)
	}
	for _, forbidden := range []string{"scope_status", "finding", "solution", "evidence", "project_id"} {
		if strings.Contains(string(snapshotJSON), `"`+forbidden+`"`) {
			t.Fatalf("Session snapshot contains Project-only field %q: %s", forbidden, snapshotJSON)
		}
	}
	health, err := board.SessionSemanticHealth(ctx, first.ID)
	if err != nil || health.Status != blackboardv2.HealthStatusHealthy || len(health.Anomalies) != 0 {
		t.Fatalf("healthy Session semantic health = %#v, %v", health, err)
	}
	if _, err := board.ApplyForOwner(ctx, firstOwner, blackboardv2.ChangeBatch{
		Schema: "semantic-change-batch/v2", IdempotencyKey: "orphan-objective",
		Changes: []blackboardv2.Change{{Op: "create", Key: "objective:orphan", Type: "objective", Record: blackboardv2.ObjectiveRecord{
			Status: "open", Objective: "This objective is intentionally not being tested",
		}}},
	}); err != nil {
		t.Fatalf("create orphan Session Objective: %v", err)
	}
	health, err = board.SessionSemanticHealth(ctx, first.ID)
	if err != nil || health.Status != blackboardv2.HealthStatusWarning || !hasSessionHealthAnomaly(health, "stranded_objective") {
		t.Fatalf("warning Session semantic health = %#v, %v", health, err)
	}

	secondResult, err := board.ApplyForOwner(ctx, second.OwnerContract(), blackboardv2.ChangeBatch{
		Schema: "semantic-change-batch/v2", IdempotencyKey: "same-local-key",
		Changes: []blackboardv2.Change{{Op: "create", Key: "entity:note", Type: "entity", Record: blackboardv2.EntityRecord{Status: "active", Kind: "host", Name: "other.example"}}},
	})
	if err != nil || secondResult.Revision != 1 {
		t.Fatalf("same Key in second Session = %#v, %v", secondResult, err)
	}
	if _, err := board.Apply(ctx, projectOwner.ID, blackboardv2.ChangeBatch{
		Schema: "semantic-change-batch/v2", IdempotencyKey: "same-project-key",
		Changes: []blackboardv2.Change{{Op: "create", Key: "entity:note", Type: "entity", Record: blackboardv2.EntityRecord{Status: "active", Kind: "host", Name: "project.example", ScopeStatus: "unknown"}}},
	}); err != nil {
		t.Fatalf("same Key in Project: %v", err)
	}

	_, err = board.ApplyForOwner(ctx, firstOwner, blackboardv2.ChangeBatch{
		Schema: "semantic-change-batch/v2", IdempotencyKey: "reject-finding",
		Changes: []blackboardv2.Change{{Op: "create", Key: "finding:nope", Type: "finding", Record: blackboardv2.FindingRecord{Status: "unconfirmed", Title: "Project finding"}}},
	})
	var capabilityErr *blackboardv2.Error
	if !errors.As(err, &capabilityErr) || capabilityErr.Code != "owner_capability_denied" {
		t.Fatalf("Project-only Session write = %#v, want owner_capability_denied", err)
	}
	_, err = board.ApplyForOwner(ctx, firstOwner, blackboardv2.ChangeBatch{
		Schema: "semantic-change-batch/v2", IdempotencyKey: "reject-scope-clear",
		Changes: []blackboardv2.Change{{Op: "update", Key: "entity:note", Version: 1, Type: "entity", Clear: []string{"scope_status"}, Record: blackboardv2.SessionEntityPatch{
			Name: stringPtr("session.example"),
		}}},
	})
	if !isSessionSemanticCode(err, "owner_capability_denied") {
		t.Fatalf("Session scope clear = %v, want owner_capability_denied", err)
	}

	if _, err := board.ApplyForOwner(ctx, firstOwner, blackboardv2.ChangeBatch{
		Schema: "semantic-change-batch/v2", IdempotencyKey: "update-session-fact",
		Changes: []blackboardv2.Change{{Op: "update", Key: "fact:note", Version: 1, Type: "fact", Record: blackboardv2.FactPatch{Summary: stringPtr("Updated Session fact")}}},
	}); err != nil {
		t.Fatalf("update Session Fact: %v", err)
	}
	history, err := board.ReadHistoryForOwner(ctx, firstOwner, "fact:note", blackboardv2.HistoryOptions{})
	if err != nil || len(history.Items) != 1 || history.Items[0].Record == nil || history.Items[0].Record.Summary != "A Session-local fact" {
		t.Fatalf("Session history = %#v, %v", history, err)
	}
	detail, err := board.ReadCurrentForOwner(ctx, firstOwner, "fact:note")
	if err != nil {
		t.Fatalf("Session current detail: %v", err)
	}
	for name, value := range map[string]any{"detail": detail, "history": history} {
		encoded, marshalErr := json.Marshal(value)
		if marshalErr != nil {
			t.Fatalf("encode Session %s: %v", name, marshalErr)
		}
		if strings.Contains(string(encoded), `"scope_status"`) {
			t.Fatalf("Session %s fabricated Project scope: %s", name, encoded)
		}
	}
	if _, err := board.ReadCurrentForOwner(ctx, second.OwnerContract(), "fact:note"); !isSessionSemanticCode(err, "not_found") {
		t.Fatalf("cross-Session current read = %v, want not_found", err)
	}

	if _, err := sessions.Archive(first.ID); err != nil {
		t.Fatalf("archive Session: %v", err)
	}
	if err := sessions.Delete(first.ID); err != nil {
		t.Fatalf("delete Session: %v", err)
	}
	if _, err := board.SessionRuntimeSnapshot(ctx, first.ID); !isSessionSemanticCode(err, "not_found") {
		t.Fatalf("deleted Session snapshot = %v, want not_found", err)
	}
}

func TestSessionBlackboardContinuationPinsWorkingSnapshotAndFinishesOnlyContinuation(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(filepath.Join(t.TempDir(), "session-continuity.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	sessions := session.NewService(db, filepath.Join(t.TempDir(), "sessions"))
	created, err := sessions.Create(session.CreateRequest{Input: "continuity Session"})
	if err != nil {
		t.Fatalf("create Session: %v", err)
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	_, err = db.Exec(`
		INSERT INTO session_continuations
		(id, session_id, number, runtime_profile_id, runtime_provider, runner, status,
		 container_id, native_session_id, native_session_path, runtime_config_version_id,
		 started_at, updated_at, ended_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		"continuation:session", created.ID, 1, "profile:session", "provider", "sandbox", "pending",
		"", "", "", "runtime-config:session", now, now, "")
	if err != nil {
		t.Fatalf("insert Session Continuation: %v", err)
	}
	board := blackboardv2.NewService(db)

	launch, err := board.BindSessionContinuation(ctx, created.ID, "continuation:session")
	if err != nil {
		t.Fatalf("bind Session Continuation: %v", err)
	}
	if launch.Launch.Revision != 0 || launch.Working.Revision != 0 || launch.Launch.SHA256 != launch.Working.SHA256 {
		t.Fatalf("initial Session Continuation pin = %#v", launch)
	}
	if _, err := db.Exec(`UPDATE blackboard_v2_session_continuation_pins SET launch_revision=99 WHERE continuation_id=?`, "continuation:session"); err == nil {
		t.Fatal("Session launch pin accepted an update")
	}
	var persistedRevision int
	if err := db.QueryRow(`SELECT revision FROM blackboard_v2_session_snapshots WHERE session_id=?`, created.ID).Scan(&persistedRevision); err != nil || persistedRevision != 0 {
		t.Fatalf("persisted Session snapshot at bind = %d, %v; want revision 0", persistedRevision, err)
	}

	seed := blackboardv2.ChangeBatch{
		Schema: "semantic-change-batch/v2", IdempotencyKey: "continuation-seed",
		Changes: []blackboardv2.Change{{Op: "create", Key: "fact:continuity", Type: "fact", Record: blackboardv2.FactRecord{
			Category: "note", Summary: "Pinned Session state", Confidence: "tentative",
		}}},
	}
	authority, err := board.AuthorizeSessionContinuation(ctx, created.ID, "continuation:session")
	if err != nil {
		t.Fatalf("authorize Session Continuation: %v", err)
	}
	accepted, err := board.ApplyForSessionAuthority(ctx, authority, seed)
	if err != nil {
		t.Fatalf("Session Continuation write: %v", err)
	}
	if accepted.Revision != 1 {
		t.Fatalf("Session Continuation write revision = %d, want 1", accepted.Revision)
	}
	updatedPin, err := board.ReadSessionContinuationPin(ctx, created.ID, "continuation:session")
	if err != nil {
		t.Fatalf("read updated Session pin: %v", err)
	}
	if updatedPin.Launch.Revision != 0 || updatedPin.Working.Revision != 1 || string(updatedPin.Launch.Bytes) == string(updatedPin.Working.Bytes) {
		t.Fatalf("Session pin after write = %#v", updatedPin)
	}

	baseOne := 1
	second, err := board.ApplyForSessionContinuationAtRevision(ctx, created.ID, "continuation:session", baseOne, blackboardv2.ChangeBatch{
		Schema: "semantic-change-batch/v2", IdempotencyKey: "continuation-update",
		Changes: []blackboardv2.Change{{Op: "update", Key: "fact:continuity", Type: "fact", Version: 1, Record: blackboardv2.FactPatch{
			Summary: stringPtr("Updated pinned Session state"),
		}}},
	})
	if err != nil || second.Revision != 2 {
		t.Fatalf("Session Continuation revisioned write = %#v, %v", second, err)
	}
	if _, err := board.ApplyForSessionContinuationAtRevision(ctx, created.ID, "continuation:session", 1, blackboardv2.ChangeBatch{
		Schema: "semantic-change-batch/v2", IdempotencyKey: "continuation-stale",
		Changes: []blackboardv2.Change{{Op: "update", Key: "fact:continuity", Type: "fact", Version: 2, Record: blackboardv2.FactPatch{
			Summary: stringPtr("stale"),
		}}},
	}); !isSessionSemanticCode(err, "version_conflict") {
		t.Fatalf("stale Session Continuation write = %v, want version_conflict", err)
	}

	finished, err := board.FinishSessionAuthority(ctx, authority, "continuation-finish")
	if err != nil || finished.Revision != 2 {
		t.Fatalf("finish Session Continuation = %#v, %v", finished, err)
	}
	replay, err := board.FinishSessionContinuation(ctx, created.ID, "continuation:session", "continuation-finish")
	if err != nil || !reflect.DeepEqual(replay, finished) {
		t.Fatalf("finish replay = %#v, %v; want %#v", replay, err, finished)
	}
	if _, err := board.FinishSessionContinuation(ctx, created.ID, "continuation:session", "different-finish"); !isSessionSemanticCode(err, "finish_conflict") {
		t.Fatalf("different Session finish retry = %v, want finish_conflict", err)
	}
	if _, err := board.ApplyForSessionContinuation(ctx, created.ID, "continuation:session", blackboardv2.ChangeBatch{
		Schema: "semantic-change-batch/v2", IdempotencyKey: "after-finish",
		Changes: []blackboardv2.Change{{Op: "update", Key: "fact:continuity", Version: 2, Type: "fact", Record: blackboardv2.FactPatch{
			Summary: stringPtr("must be rejected"),
		}}},
	}); !isSessionSemanticCode(err, "closed_continuation") {
		t.Fatalf("write after Session Continuation finish = %v, want closed_continuation", err)
	}
	_, err = db.Exec(`
		INSERT INTO session_continuations
		(id, session_id, number, runtime_profile_id, runtime_provider, runner, status,
		 container_id, native_session_id, native_session_path, runtime_config_version_id,
		 started_at, updated_at, ended_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		"continuation:replacement", created.ID, 2, "profile:session", "provider", "sandbox", "pending",
		"", "", "", "runtime-config:session", now, now, "")
	if err != nil {
		t.Fatalf("insert replacement Session Continuation: %v", err)
	}
	if err := board.RebindSessionContinuation(ctx, created.ID, "continuation:session", "continuation:replacement"); err != nil {
		t.Fatalf("rebind Session Continuation: %v", err)
	}
	rebound, err := board.ReadSessionContinuationPin(ctx, created.ID, "continuation:replacement")
	if err != nil || rebound.Launch.Revision != 0 || rebound.Working.Revision != 2 || string(rebound.Launch.Bytes) == string(rebound.Working.Bytes) {
		t.Fatalf("rebound Session pin = %#v, %v", rebound, err)
	}
	if _, err := board.ApplyForSessionContinuation(ctx, created.ID, "continuation:replacement", blackboardv2.ChangeBatch{
		Schema: "semantic-change-batch/v2", IdempotencyKey: "replacement-update",
		Changes: []blackboardv2.Change{{Op: "update", Key: "fact:continuity", Version: 2, Type: "fact", Record: blackboardv2.FactPatch{
			Summary: stringPtr("Updated by replacement Continuation"),
		}}},
	}); err != nil {
		t.Fatalf("replacement Session Continuation write: %v", err)
	}
	stillOpen, err := sessions.Get(created.ID)
	if err != nil || stillOpen.Lifecycle != session.LifecycleOpen {
		t.Fatalf("Session lifecycle after Continuation finish = %#v, %v", stillOpen, err)
	}
}

func isSessionSemanticCode(err error, code string) bool {
	var semantic *blackboardv2.Error
	return errors.As(err, &semantic) && semantic.Code == code
}

func stringPtr(value string) *string { return &value }

func hasSessionHealthAnomaly(health blackboardv2.SemanticHealth, code string) bool {
	for _, anomaly := range health.Anomalies {
		if anomaly.Code == code {
			return true
		}
	}
	return false
}
