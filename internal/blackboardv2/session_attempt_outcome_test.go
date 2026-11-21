package blackboardv2_test

import (
	"context"
	"path/filepath"
	"reflect"
	"testing"

	"pentest/internal/blackboardv2"
	"pentest/internal/session"
	"pentest/internal/store"
)

func TestSessionSucceededAttemptAcceptsCurrentReplacementInEitherOrder(t *testing.T) {
	for _, attemptFirst := range []bool{false, true} {
		t.Run(map[bool]string{false: "replacement first", true: "success first"}[attemptFirst], func(t *testing.T) {
			_, board, owner := newSessionOutcomeFixture(t)
			ctx := context.Background()
			supersede := blackboardv2.Change{Op: "supersede", Replaced: "entity:old", ReplacedVersion: 1, Replacement: "entity:new", ReplacementVersion: 1}
			terminal := blackboardv2.Change{Op: "transition", Key: "attempt:outcome", Version: 1, Status: "succeeded", Summary: "The outcome remains reusable"}
			changes := []blackboardv2.Change{supersede, terminal}
			if attemptFirst {
				changes = []blackboardv2.Change{terminal, supersede}
			}
			batch := blackboardv2.ChangeBatch{Schema: "semantic-change-batch/v2", IdempotencyKey: "replacement-success", Changes: changes}
			result, err := board.ApplyForSession(ctx, owner.ID, batch)
			if err != nil {
				t.Fatalf("succeed Session Attempt with replacement: %v", err)
			}
			replay, err := board.ApplyForSession(ctx, owner.ID, batch)
			if err != nil || !reflect.DeepEqual(replay, result) {
				t.Fatalf("replay = %#v, %v; want %#v", replay, err, result)
			}
			for _, key := range []string{"attempt:outcome", "entity:old"} {
				if _, err := board.ReadSessionCurrent(ctx, owner.ID, key); !isSessionSemanticCode(err, "not_found") {
					t.Fatalf("terminal record %s remains current: %v", key, err)
				}
			}
			if current, err := board.ReadSessionCurrent(ctx, owner.ID, "entity:new"); err != nil || current.Record.Status != "active" {
				t.Fatalf("current replacement = %#v, %v", current, err)
			}
			history, err := board.ReadSessionHistory(ctx, owner.ID, "attempt:outcome", blackboardv2.HistoryOptions{})
			if err != nil || len(history.Items) != 4 || history.Items[1].Record.Status != "succeeded" || history.Items[2].Relation != "produced" || history.Items[2].To != "entity:old" {
				t.Fatalf("Session Attempt history = %#v, %v", history, err)
			}
		})
	}
}

func TestSessionSucceededAttemptRejectsUnavailableReplacement(t *testing.T) {
	for _, state := range []string{"no produced outcome", "missing replacement", "wrong type", "foreign Session"} {
		t.Run(state, func(t *testing.T) {
			db, board, owner := newSessionOutcomeFixture(t)
			ctx := context.Background()
			if state == "no produced outcome" {
				if _, err := db.Exec(`DELETE FROM blackboard_v2_session_relationships WHERE session_id=? AND relation='produced'`, owner.ID); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := board.ApplyForSession(ctx, owner.ID, blackboardv2.ChangeBatch{
				Schema: "semantic-change-batch/v2", IdempotencyKey: "replace-outcome",
				Changes: []blackboardv2.Change{{Op: "supersede", Replaced: "entity:old", ReplacedVersion: 1, Replacement: "entity:new", ReplacementVersion: 1}},
			}); err != nil {
				t.Fatal(err)
			}
			if state == "wrong type" {
				if _, err := db.Exec(`UPDATE blackboard_v2_session_records SET type='fact',record_json='{"category":"test","summary":"Other type","confidence":"tentative"}' WHERE session_id=? AND key='entity:new'`, owner.ID); err != nil {
					t.Fatal(err)
				}
			} else if state != "no produced outcome" {
				if state == "foreign Session" {
					other, err := session.NewService(db, t.TempDir()).Create(session.CreateRequest{Input: "Other owner"})
					if err != nil {
						t.Fatal(err)
					}
					if _, err := db.Exec(`UPDATE blackboard_v2_session_records SET session_id=? WHERE session_id=? AND key='entity:new'`, other.ID, owner.ID); err != nil {
						t.Fatal(err)
					}
				} else if _, err := db.Exec(`DELETE FROM blackboard_v2_session_records WHERE session_id=? AND key='entity:new'`, owner.ID); err != nil {
					t.Fatal(err)
				}
			}
			_, err := board.ApplyForSession(ctx, owner.ID, blackboardv2.ChangeBatch{
				Schema: "semantic-change-batch/v2", IdempotencyKey: "invalid-success",
				Changes: []blackboardv2.Change{{Op: "transition", Key: "attempt:outcome", Version: 1, Status: "succeeded", Summary: "No current reusable outcome"}},
			})
			if !isSessionSemanticCode(err, "semantic_validation") {
				t.Fatalf("invalid success error = %v", err)
			}
			if current, err := board.ReadSessionCurrent(ctx, owner.ID, "attempt:outcome"); err != nil || current.Record.Status != "open" || current.Version != 1 {
				t.Fatalf("rejected transition changed Attempt: %#v, %v", current, err)
			}
		})
	}
}

func newSessionOutcomeFixture(t *testing.T) (*store.DB, *blackboardv2.Service, session.Session) {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "outcome.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	owner, err := session.NewService(db, t.TempDir()).Create(session.CreateRequest{Input: "Outcome owner"})
	if err != nil {
		t.Fatal(err)
	}
	board := blackboardv2.NewService(db)
	_, err = board.ApplyForSession(context.Background(), owner.ID, blackboardv2.ChangeBatch{
		Schema: "semantic-change-batch/v2", IdempotencyKey: "seed-outcome",
		Changes: []blackboardv2.Change{
			{Op: "create", Key: "entity:old", Type: "entity", Record: blackboardv2.SessionEntityRecord{Status: "active", Kind: "host", Name: "Old outcome"}},
			{Op: "create", Key: "entity:new", Type: "entity", Record: blackboardv2.SessionEntityRecord{Status: "active", Kind: "host", Name: "New outcome"}},
			{Op: "create", Key: "objective:test", Type: "objective", Record: blackboardv2.ObjectiveRecord{Status: "open", Objective: "Test a target"}},
			{Op: "create", Key: "attempt:outcome", Type: "attempt", Record: blackboardv2.AttemptRecord{Status: "open", Summary: "Test for an outcome"}},
			{Op: "relate", From: "attempt:outcome", Relation: "tests", To: "objective:test"},
			{Op: "relate", From: "attempt:outcome", Relation: "produced", To: "entity:old"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	return db, board, owner
}
