package blackboardv2_test

import (
	"context"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"pentest/internal/blackboardv2"
	"pentest/internal/project"
	"pentest/internal/store"
)

func TestTerminalAttemptAcceptsHistoricalTestsAcrossBatchOrders(t *testing.T) {
	for _, tt := range []struct {
		name          string
		previousBatch bool
		attemptFirst  bool
	}{
		{name: "target terminalized previously", previousBatch: true},
		{name: "target first in final batch"},
		{name: "Attempt first in final batch", attemptFirst: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			ctx, service, projectID := seedTerminalAttemptFixture(t, false)
			targetTerminal := blackboardv2.Change{
				Op: "transition", Key: "objective:final-target", Version: 1, Status: "abandoned", ResolutionSummary: "The tested target no longer requires work",
			}
			attemptTerminal := blackboardv2.Change{
				Op: "transition", Key: "attempt:final-batch", Version: 1, Status: "failed", Summary: "The tested path did not produce a reusable outcome",
			}
			if tt.previousBatch {
				if _, err := service.Apply(ctx, projectID, blackboardv2.ChangeBatch{
					Schema: "semantic-change-batch/v2", IdempotencyKey: "terminalize-tested-target-first", Changes: []blackboardv2.Change{targetTerminal},
				}); err != nil {
					t.Fatalf("terminalize tested target in prior batch: %v", err)
				}
			}
			changes := []blackboardv2.Change{targetTerminal, attemptTerminal}
			if tt.previousBatch {
				changes = []blackboardv2.Change{attemptTerminal}
			} else if tt.attemptFirst {
				changes = []blackboardv2.Change{attemptTerminal, targetTerminal}
			}
			batch := blackboardv2.ChangeBatch{
				Schema: "semantic-change-batch/v2", IdempotencyKey: "terminalize-attempt-with-historical-tests", Changes: changes,
			}
			result, err := service.Apply(ctx, projectID, batch)
			if err != nil {
				t.Fatalf("terminalize Attempt with historical tests: %v", err)
			}
			replay, err := service.Apply(ctx, projectID, batch)
			if err != nil || !reflect.DeepEqual(replay, result) {
				t.Fatalf("terminal Attempt exact replay = %#v, %v; want %#v", replay, err, result)
			}
			history, err := service.ReadHistory(ctx, projectID, "attempt:final-batch", blackboardv2.HistoryOptions{})
			if err != nil {
				t.Fatalf("read terminal Attempt history: %v", err)
			}
			if len(history.Items) != 4 || history.Items[1].Record.Status != "failed" || history.Items[3].Relation != "tests" || history.Items[3].To != "objective:final-target" {
				t.Fatalf("terminal Attempt history = %#v", history.Items)
			}
			snapshot, err := service.RuntimeSnapshot(ctx, projectID)
			if err != nil {
				t.Fatalf("read final Runtime Snapshot: %v", err)
			}
			snapshotJSON := string(mustJSON(t, snapshot))
			if strings.Contains(snapshotJSON, "attempt:final-batch") || strings.Contains(snapshotJSON, "objective:final-target") {
				t.Fatalf("terminal work remained in Snapshot: %s", snapshotJSON)
			}
		})
	}
}

func TestSucceededAttemptAcceptsCurrentReplacementForProducedOutcomeInEitherOrder(t *testing.T) {
	for _, tt := range []struct {
		name         string
		attemptFirst bool
	}{
		{name: "replacement first"},
		{name: "Attempt first", attemptFirst: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			ctx, service, projectID := seedTerminalAttemptFixture(t, true)
			supersede := blackboardv2.Change{
				Op: "supersede", Replacement: "entity:outcome-new", ReplacementVersion: 1, Replaced: "entity:outcome-old", ReplacedVersion: 1,
			}
			terminal := blackboardv2.Change{
				Op: "transition", Key: "attempt:final-batch", Version: 1, Status: "succeeded", Summary: "The tested path produced reusable replacement knowledge",
			}
			changes := []blackboardv2.Change{supersede, terminal}
			if tt.attemptFirst {
				changes = []blackboardv2.Change{terminal, supersede}
			}
			batch := blackboardv2.ChangeBatch{
				Schema: "semantic-change-batch/v2", IdempotencyKey: "succeed-with-current-produced-replacement", Changes: changes,
			}
			result, err := service.Apply(ctx, projectID, batch)
			if err != nil {
				t.Fatalf("succeed Attempt with produced replacement: %v", err)
			}
			replay, err := service.Apply(ctx, projectID, batch)
			if err != nil || !reflect.DeepEqual(replay, result) {
				t.Fatalf("replacement success exact replay = %#v, %v; want %#v", replay, err, result)
			}
			if replacement, err := service.ReadCurrent(ctx, projectID, "entity:outcome-new"); err != nil || replacement.Record.Status != "active" {
				t.Fatalf("current produced replacement = %#v, %v", replacement, err)
			}
			for _, key := range []string{"attempt:final-batch", "entity:outcome-old"} {
				if _, err := service.ReadCurrent(ctx, projectID, key); !isSemanticCode(err, "not_found") {
					t.Fatalf("terminal record %s remained current: %#v", key, err)
				}
			}
			history, err := service.ReadHistory(ctx, projectID, "attempt:final-batch", blackboardv2.HistoryOptions{})
			if err != nil {
				t.Fatalf("read succeeded Attempt history: %v", err)
			}
			if len(history.Items) != 4 || history.Items[1].Record.Status != "succeeded" || history.Items[2].Relation != "produced" || history.Items[2].To != "entity:outcome-old" || history.Items[3].Relation != "tests" {
				t.Fatalf("succeeded Attempt history = %#v", history.Items)
			}
			snapshot, err := service.RuntimeSnapshot(ctx, projectID)
			if err != nil {
				t.Fatalf("read replacement Runtime Snapshot: %v", err)
			}
			snapshotJSON := string(mustJSON(t, snapshot))
			if !strings.Contains(snapshotJSON, "entity:outcome-new") || strings.Contains(snapshotJSON, "entity:outcome-old") || strings.Contains(snapshotJSON, "attempt:final-batch") {
				t.Fatalf("replacement Snapshot is inconsistent: %s", snapshotJSON)
			}
		})
	}
}

func TestSucceededAttemptRollsBackWhenOnlyProducedOutcomeIsRetiredInEitherOrder(t *testing.T) {
	for _, tt := range []struct {
		name         string
		attemptFirst bool
	}{
		{name: "outcome first"},
		{name: "Attempt first", attemptFirst: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			ctx, service, projectID := seedTerminalAttemptFixture(t, false)
			retireOutcome := blackboardv2.Change{
				Op: "transition", Key: "entity:outcome-old", Version: 1, Status: "retired", ResolutionSummary: "The produced outcome has no reusable current meaning",
			}
			terminal := blackboardv2.Change{
				Op: "transition", Key: "attempt:final-batch", Version: 1, Status: "succeeded", Summary: "The Attempt cannot succeed after losing its only reusable outcome",
			}
			changes := []blackboardv2.Change{retireOutcome, terminal}
			if tt.attemptFirst {
				changes = []blackboardv2.Change{terminal, retireOutcome}
			}
			_, err := service.Apply(ctx, projectID, blackboardv2.ChangeBatch{
				Schema: "semantic-change-batch/v2", IdempotencyKey: "reject-success-with-retired-produced-outcome", Changes: changes,
			})
			if !isSemanticCode(err, "semantic_validation") {
				t.Fatalf("retired produced outcome error = %#v, want semantic_validation", err)
			}
			attempt, err := service.ReadCurrent(ctx, projectID, "attempt:final-batch")
			if err != nil || attempt.Record.Status != "open" || len(attempt.Relationships) != 2 {
				t.Fatalf("failed batch changed Attempt = %#v, %v", attempt, err)
			}
			if outcome, err := service.ReadCurrent(ctx, projectID, "entity:outcome-old"); err != nil || outcome.Record.Status != "active" {
				t.Fatalf("failed batch changed produced outcome = %#v, %v", outcome, err)
			}
			history, err := service.ReadHistory(ctx, projectID, "attempt:final-batch", blackboardv2.HistoryOptions{})
			if err != nil {
				t.Fatalf("read rolled-back Attempt history: %v", err)
			}
			if len(history.Items) != 0 {
				t.Fatalf("failed batch retained Attempt history: %#v", history.Items)
			}
			snapshot, err := service.RuntimeSnapshot(ctx, projectID)
			if err != nil {
				t.Fatalf("read rolled-back Runtime Snapshot: %v", err)
			}
			snapshotJSON := string(mustJSON(t, snapshot))
			for _, currentKey := range []string{"attempt:final-batch", "entity:outcome-old"} {
				if !strings.Contains(snapshotJSON, currentKey) {
					t.Fatalf("rolled-back Snapshot lost %s: %s", currentKey, snapshotJSON)
				}
			}
		})
	}
}

func TestSucceededAttemptAcceptsAnyReusableProducedOutcomeInEitherOrder(t *testing.T) {
	for _, tt := range []struct {
		name         string
		attemptFirst bool
	}{
		{name: "outcome first"},
		{name: "Attempt first", attemptFirst: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			ctx, service, projectID := seedTerminalAttemptFixture(t, false)
			if _, err := service.Apply(ctx, projectID, blackboardv2.ChangeBatch{
				Schema: "semantic-change-batch/v2", IdempotencyKey: "seed-second-produced-outcome", Changes: []blackboardv2.Change{
					{Op: "create", Key: "entity:outcome-current", Type: "entity", Record: blackboardv2.EntityRecord{Status: "active", Kind: "endpoint", Name: "Current reusable outcome", ScopeStatus: "in_scope"}},
					{Op: "relate", From: "attempt:final-batch", Relation: "produced", To: "entity:outcome-current"},
				},
			}); err != nil {
				t.Fatalf("seed second produced outcome: %v", err)
			}

			retireOutcome := blackboardv2.Change{
				Op: "transition", Key: "entity:outcome-old", Version: 1, Status: "retired", ResolutionSummary: "A second produced outcome remains reusable",
			}
			terminal := blackboardv2.Change{
				Op: "transition", Key: "attempt:final-batch", Version: 1, Status: "succeeded", Summary: "The Attempt retained one reusable produced outcome",
			}
			changes := []blackboardv2.Change{retireOutcome, terminal}
			if tt.attemptFirst {
				changes = []blackboardv2.Change{terminal, retireOutcome}
			}
			batch := blackboardv2.ChangeBatch{
				Schema: "semantic-change-batch/v2", IdempotencyKey: "succeed-with-one-current-produced-outcome", Changes: changes,
			}
			result, err := service.Apply(ctx, projectID, batch)
			if err != nil {
				t.Fatalf("succeed Attempt with one reusable produced outcome: %v", err)
			}
			replay, err := service.Apply(ctx, projectID, batch)
			if err != nil || !reflect.DeepEqual(replay, result) {
				t.Fatalf("reusable outcome exact replay = %#v, %v; want %#v", replay, err, result)
			}
			if outcome, err := service.ReadCurrent(ctx, projectID, "entity:outcome-current"); err != nil || outcome.Record.Status != "active" {
				t.Fatalf("reusable produced outcome = %#v, %v", outcome, err)
			}
		})
	}
}

func seedTerminalAttemptFixture(t *testing.T, withReplacement bool) (context.Context, *blackboardv2.Service, string) {
	t.Helper()
	ctx := context.Background()
	db, err := store.Open(filepath.Join(t.TempDir(), "pentest.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	createdProject, err := project.NewService(db).Create("Attempt Final Batch", "", project.Scope{}, project.Defaults{})
	if err != nil {
		t.Fatalf("create project: %v", err)
	}
	service := blackboardv2.NewService(db)
	changes := []blackboardv2.Change{
		{Op: "create", Key: "objective:final-target", Type: "objective", Record: blackboardv2.ObjectiveRecord{Status: "open", Objective: "Provide the tested final-batch target"}},
		{Op: "create", Key: "attempt:final-batch", Type: "attempt", Record: blackboardv2.AttemptRecord{Status: "open", Summary: "Exercise final-batch Attempt invariants"}},
		{Op: "create", Key: "entity:outcome-old", Type: "entity", Record: blackboardv2.EntityRecord{Status: "active", Kind: "endpoint", Name: "Original reusable outcome", ScopeStatus: "in_scope"}},
		{Op: "relate", From: "attempt:final-batch", Relation: "tests", To: "objective:final-target"},
		{Op: "relate", From: "attempt:final-batch", Relation: "produced", To: "entity:outcome-old"},
	}
	if withReplacement {
		changes = append(changes[:3], append([]blackboardv2.Change{{
			Op: "create", Key: "entity:outcome-new", Type: "entity", Record: blackboardv2.EntityRecord{Status: "active", Kind: "endpoint", Name: "Replacement reusable outcome", ScopeStatus: "in_scope"},
		}}, changes[3:]...)...)
	}
	if _, err := service.Apply(ctx, createdProject.ID, blackboardv2.ChangeBatch{
		Schema: "semantic-change-batch/v2", IdempotencyKey: "seed-attempt-final-batch", Changes: changes,
	}); err != nil {
		t.Fatalf("seed Attempt final-batch fixture: %v", err)
	}
	return ctx, service, createdProject.ID
}
