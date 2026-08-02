package blackboardv2_test

import (
	"context"
	"encoding/json"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"pentest/internal/blackboardv2"
	"pentest/internal/owner"
	"pentest/internal/project"
	"pentest/internal/session"
	"pentest/internal/store"
)

func TestOwnerAdaptersRunTheCommonBlackboardV2ConformanceCorpus(t *testing.T) {
	for _, kind := range []string{"project", "session"} {
		t.Run(kind, func(t *testing.T) {
			ctx := context.Background()
			db, err := store.Open(filepath.Join(t.TempDir(), "owner-conformance.db"))
			if err != nil {
				t.Fatalf("open store: %v", err)
			}
			t.Cleanup(func() { _ = db.Close() })

			board := blackboardv2.NewService(db)
			var contract owner.Contract
			var entity any
			var patch any
			if kind == "project" {
				projects := project.NewService(db)
				created, createErr := projects.Create("owner conformance", "", project.Scope{}, project.Defaults{})
				if createErr != nil {
					t.Fatalf("create Project: %v", createErr)
				}
				contract = owner.NewTaskContract("task:owner-conformance", created.ID, "")
				entity = blackboardv2.EntityRecord{Status: "active", Kind: "host", Name: "conformance.example", ScopeStatus: "unknown"}
				name := "updated.conformance.example"
				patch = blackboardv2.EntityPatch{Name: &name}
			} else {
				sessions := session.NewService(db, filepath.Join(t.TempDir(), "sessions"))
				created, createErr := sessions.Create(session.CreateRequest{Input: "owner conformance"})
				if createErr != nil {
					t.Fatalf("create Session: %v", createErr)
				}
				contract = created.OwnerContract()
				entity = blackboardv2.SessionEntityRecord{Status: "active", Kind: "host", Name: "conformance.example"}
				name := "updated.conformance.example"
				patch = blackboardv2.SessionEntityPatch{Name: &name}
			}
			if err := contract.Validate(); err != nil {
				t.Fatalf("owner contract: %v", err)
			}

			seed := blackboardv2.ChangeBatch{
				Schema: "semantic-change-batch/v2", IdempotencyKey: "conformance-seed",
				Changes: []blackboardv2.Change{
					{Op: "create", Key: "entity:target", Type: "entity", Record: entity},
					{Op: "create", Key: "objective:goal", Type: "objective", Record: blackboardv2.ObjectiveRecord{Status: "open", Objective: "Exercise the common owner adapter"}},
					{Op: "create", Key: "attempt:run", Type: "attempt", Record: blackboardv2.AttemptRecord{Status: "open", Summary: "Run common owner conformance"}},
					{Op: "relate", From: "attempt:run", Relation: "tests", To: "objective:goal"},
					{Op: "relate", From: "attempt:run", Relation: "about", To: "entity:target"},
				},
			}
			seedResult, err := board.ApplyForOwner(ctx, contract, seed)
			if err != nil {
				t.Fatalf("seed: %v", err)
			}
			if seedResult.Revision != 5 || len(seedResult.Records) != 3 || len(seedResult.Relations) != 2 {
				t.Fatalf("seed result = %#v", seedResult)
			}
			replay, err := board.ApplyForOwner(ctx, contract, seed)
			if err != nil || !reflect.DeepEqual(replay, seedResult) {
				t.Fatalf("seed replay = %#v, %v; want %#v", replay, err, seedResult)
			}

			updated, err := board.ApplyForOwnerAtRevision(ctx, contract, 5, blackboardv2.ChangeBatch{
				Schema: "semantic-change-batch/v2", IdempotencyKey: "conformance-update",
				Changes: []blackboardv2.Change{{Op: "update", Key: "entity:target", Type: "entity", Version: 1, Record: patch}},
			})
			if err != nil || updated.Revision != 6 {
				t.Fatalf("revisioned update = %#v, %v", updated, err)
			}
			detail, err := board.ReadCurrentForOwner(ctx, contract, "entity:target")
			if err != nil || detail.Revision != 6 || detail.Version != 2 || detail.Record.Name != "updated.conformance.example" {
				t.Fatalf("current detail = %#v, %v", detail, err)
			}
			history, err := board.ReadHistoryForOwner(ctx, contract, "entity:target", blackboardv2.HistoryOptions{})
			if err != nil || len(history.Items) != 1 || history.Items[0].Record == nil || history.Items[0].Record.Name != "conformance.example" {
				t.Fatalf("history = %#v, %v", history, err)
			}
			snapshot, err := board.RuntimeSnapshotForOwner(ctx, contract)
			if err != nil || snapshot.Revision != 6 || len(snapshot.Work.Objectives) != 1 || len(snapshot.Work.Attempts) != 1 || len(snapshot.Relations) != 2 {
				t.Fatalf("snapshot = %#v, %v", snapshot, err)
			}
			if kind == "session" {
				encoded, marshalErr := json.Marshal(snapshot)
				if marshalErr != nil {
					t.Fatalf("encode Session snapshot: %v", marshalErr)
				}
				if strings.Contains(string(encoded), `"scope_status"`) {
					t.Fatalf("Session conformance snapshot fabricated Project scope: %s", encoded)
				}
			}
		})
	}
}
