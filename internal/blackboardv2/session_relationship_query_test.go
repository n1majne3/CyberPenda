package blackboardv2

import (
	"context"
	"reflect"
	"strings"
	"testing"

	"pentest/internal/session"
)

func TestSessionCurrentReadQueriesOnlyAttachedRelationships(t *testing.T) {
	for _, state := range []string{"attached", "empty", "query error"} {
		t.Run(state, func(t *testing.T) {
			db, recorder := newAuditQueryStore(t)
			ctx := context.Background()
			service := NewService(db)
			sessions := session.NewService(db, t.TempDir())
			var sessionID string
			for _, foreign := range []bool{false, true} {
				owner, err := sessions.Create(session.CreateRequest{Input: "Relationship owner"})
				if err != nil {
					t.Fatal(err)
				}
				if !foreign {
					sessionID = owner.ID
				}
				changes := make([]Change, 0)
				for _, key := range []string{"fact:a", "fact:chosen", "fact:m", "fact:z", "fact:unrelated"} {
					changes = append(changes, Change{Op: "create", Key: key, Type: "fact", Record: SessionFactRecord{Category: "note", Summary: key, Confidence: "tentative"}})
				}
				if foreign {
					changes = append(changes, Change{Op: "relate", From: "fact:chosen", Relation: "contradicts", To: "fact:z", Reason: "Foreign reason"})
				} else {
					changes = append(changes,
						Change{Op: "relate", From: "fact:chosen", Relation: "supports", To: "fact:z", Reason: "Support rationale"},
						Change{Op: "relate", From: "fact:chosen", Relation: "derived_from", To: "fact:m"},
						Change{Op: "relate", From: "fact:chosen", Relation: "contradicts", To: "fact:z", Reason: "Outcome differs"},
						Change{Op: "relate", From: "fact:a", Relation: "supports", To: "fact:chosen"},
						Change{Op: "relate", From: "fact:m", Relation: "supports", To: "fact:z", Reason: "Not attached"},
					)
				}
				if _, err := service.ApplyForSession(ctx, owner.ID, ChangeBatch{Schema: "semantic-change-batch/v2", IdempotencyKey: "seed", Changes: changes}); err != nil {
					t.Fatal(err)
				}
			}
			if state == "query error" {
				if _, err := db.Exec(`DROP TABLE blackboard_v2_session_relationships`); err != nil {
					t.Fatal(err)
				}
			}
			recorder.mu.Lock()
			recorder.queries = nil
			recorder.mu.Unlock()
			key := "fact:chosen"
			want := []RelationshipTuple{
				{"fact:a", "supports", "fact:chosen"},
				{"fact:chosen", "contradicts", "fact:z", "Outcome differs"},
				{"fact:chosen", "derived_from", "fact:m"},
				{"fact:chosen", "supports", "fact:z", "Support rationale"},
			}
			if state == "empty" {
				key = "fact:unrelated"
				want = []RelationshipTuple{}
			}
			got, err := service.ReadSessionCurrent(ctx, sessionID, key)
			if state == "query error" {
				if err == nil || !strings.Contains(err.Error(), "read Session Blackboard relationships") {
					t.Fatalf("relationship query error = %v", err)
				}
			} else if err != nil || !reflect.DeepEqual(got.Relationships, want) {
				t.Fatalf("relationships = %#v, %v; want %#v", got.Relationships, err, want)
			}
			recorder.mu.Lock()
			queries := append([]conclusionQuery(nil), recorder.queries...)
			recorder.mu.Unlock()
			reads := 0
			for _, query := range queries {
				flat := strings.Join(strings.Fields(query.sql), " ")
				if !strings.Contains(flat, "FROM blackboard_v2_session_relationships") {
					continue
				}
				reads++
				if !strings.Contains(flat, "WHERE session_id=? AND (from_key=? OR to_key=?)") || !reflect.DeepEqual(query.args, []any{sessionID, key, key}) {
					t.Errorf("single-record read queried unrelated relationships: %s, %#v", flat, query.args)
				}
				if !strings.Contains(flat, "ORDER BY from_key ASC, relation ASC, to_key ASC, reason ASC") {
					t.Errorf("relationship query lost deterministic order: %s", flat)
				}
			}
			if reads != 1 {
				t.Fatalf("relationship reads = %d; want 1", reads)
			}
		})
	}
}
