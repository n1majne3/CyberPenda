package blackboardv2_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"testing"

	"pentest/internal/blackboardv2"
	"pentest/internal/project"
	"pentest/internal/store"
)

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

func TestRelationChangeShapesMatchFrozenReasonAndVersionRules(t *testing.T) {
	ordinary := []string{"about", "part_of", "tests", "produced", "evidences", "derived_from", "satisfies"}
	for _, relation := range ordinary {
		for _, extra := range []string{`,"version":99`, `,"reason":"not allowed"`} {
			raw := fmt.Sprintf(`{"op":"relate","from":"fact:source","relation":%q,"to":"entity:target"%s}`, relation, extra)
			var change blackboardv2.Change
			if err := json.Unmarshal([]byte(raw), &change); err == nil {
				t.Fatalf("decoded ordinary %s relation with forbidden field: %s", relation, raw)
			}
		}
	}
	for _, relation := range []string{"supports", "contradicts", "depends_on"} {
		for _, raw := range []string{
			fmt.Sprintf(`{"op":"relate","from":"fact:source","relation":%q,"to":"fact:target","version":1}`, relation),
			fmt.Sprintf(`{"op":"relate","from":"fact:source","relation":%q,"to":"fact:target","reason":""}`, relation),
		} {
			var change blackboardv2.Change
			if err := json.Unmarshal([]byte(raw), &change); err == nil {
				t.Fatalf("decoded invalid reason-capable %s relation: %s", relation, raw)
			}
		}
		for _, raw := range []string{
			fmt.Sprintf(`{"op":"relate","from":"fact:source","relation":%q,"to":"fact:target"}`, relation),
			fmt.Sprintf(`{"op":"relate","from":"fact:source","relation":%q,"to":"fact:target","reason":"semantic basis"}`, relation),
			fmt.Sprintf(`{"op":"relate","from":"fact:source","relation":%q,"to":"fact:target","version":1,"reason":"semantic basis"}`, relation),
		} {
			var change blackboardv2.Change
			if err := json.Unmarshal([]byte(raw), &change); err != nil {
				t.Fatalf("decode valid reason-capable %s relation %s: %v", relation, raw, err)
			}
		}
		for _, version := range []string{"null", "0", "-1"} {
			raw := fmt.Sprintf(`{"op":"relate","from":"fact:source","relation":%q,"to":"fact:target","version":%s,"reason":"semantic basis"}`, relation, version)
			var change blackboardv2.Change
			if err := json.Unmarshal([]byte(raw), &change); err == nil {
				t.Fatalf("decoded reason-capable %s relation with invalid version %s", relation, version)
			}
		}
	}

	ctx := context.Background()
	db, err := store.Open(filepath.Join(t.TempDir(), "pentest.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	createdProject, err := project.NewService(db).Create("Closed Relation Changes", "", project.Scope{}, project.Defaults{})
	if err != nil {
		t.Fatalf("create project: %v", err)
	}
	service := blackboardv2.NewService(db)
	_, err = service.Apply(ctx, createdProject.ID, blackboardv2.ChangeBatch{
		Schema:         "semantic-change-batch/v2",
		IdempotencyKey: "seed-closed-relation-changes",
		Changes: []blackboardv2.Change{
			{Op: "create", Key: "entity:target", Type: "entity", Record: blackboardv2.EntityRecord{Status: "active", Kind: "endpoint", Name: "POST /login", ScopeStatus: "in_scope"}},
			{Op: "create", Key: "fact:source", Type: "fact", Record: blackboardv2.FactRecord{Category: "authentication", Summary: "Login accepts JSON", Confidence: "tentative", ScopeStatus: "in_scope"}},
			{Op: "create", Key: "objective:dependent", Type: "objective", Record: blackboardv2.ObjectiveRecord{Status: "open", Objective: "Test login behavior"}},
			{Op: "create", Key: "objective:prerequisite", Type: "objective", Record: blackboardv2.ObjectiveRecord{Status: "open", Objective: "Map the login endpoint"}},
			{Op: "relate", From: "fact:source", Relation: "about", To: "entity:target"},
		},
	})
	if err != nil {
		t.Fatalf("seed closed relation changes: %v", err)
	}

	for _, relation := range ordinary {
		for _, tt := range []struct {
			name   string
			change blackboardv2.Change
			path   string
		}{
			{name: "version", change: blackboardv2.Change{Op: "relate", From: "fact:source", Relation: relation, To: "entity:target", Version: 99}, path: "changes[0].version"},
			{name: "reason", change: blackboardv2.Change{Op: "relate", From: "fact:source", Relation: relation, To: "entity:target", Reason: "not allowed"}, path: "changes[0].reason"},
		} {
			t.Run(relation+" rejects "+tt.name, func(t *testing.T) {
				_, err := service.Apply(ctx, createdProject.ID, blackboardv2.ChangeBatch{
					Schema:         "semantic-change-batch/v2",
					IdempotencyKey: "reject-ordinary-" + relation + "-" + tt.name,
					Changes:        []blackboardv2.Change{tt.change},
				})
				var semanticErr *blackboardv2.Error
				if !errors.As(err, &semanticErr) || semanticErr.Code != "semantic_validation" || semanticErr.Path != tt.path {
					t.Fatalf("ordinary %s %s error = %#v, want semantic_validation on %s", relation, tt.name, err, tt.path)
				}
			})
		}
	}
	for _, relation := range []string{"supports", "contradicts", "depends_on"} {
		t.Run(relation+" version requires reason", func(t *testing.T) {
			_, err := service.Apply(ctx, createdProject.ID, blackboardv2.ChangeBatch{
				Schema:         "semantic-change-batch/v2",
				IdempotencyKey: "reject-reason-relation-version-without-reason-" + relation,
				Changes: []blackboardv2.Change{{
					Op: "relate", From: "objective:dependent", Relation: relation, To: "objective:prerequisite", Version: 1,
				}},
			})
			var semanticErr *blackboardv2.Error
			if !errors.As(err, &semanticErr) || semanticErr.Code != "semantic_validation" || semanticErr.Path != "changes[0].reason" {
				t.Fatalf("reason-capable %s dependentRequired error = %#v", relation, err)
			}
		})
	}

	noOp, err := service.Apply(ctx, createdProject.ID, blackboardv2.ChangeBatch{
		Schema:         "semantic-change-batch/v2",
		IdempotencyKey: "ordinary-existing-edge-noop",
		Changes:        []blackboardv2.Change{{Op: "relate", From: "fact:source", Relation: "about", To: "entity:target"}},
	})
	if err != nil {
		t.Fatalf("valid ordinary existing-edge no-op: %v", err)
	}
	assertRelationResult(t, noOp, [][]any{})
	dependsOn, err := service.Apply(ctx, createdProject.ID, blackboardv2.ChangeBatch{
		Schema:         "semantic-change-batch/v2",
		IdempotencyKey: "reason-capable-new-relation",
		Changes: []blackboardv2.Change{{
			Op: "relate", From: "objective:dependent", Relation: "depends_on", To: "objective:prerequisite", Reason: "Map the endpoint first",
		}},
	})
	if err != nil {
		t.Fatalf("valid reason-capable new relation: %v", err)
	}
	assertRelationResult(t, dependsOn, [][]any{{"objective:dependent", "depends_on", "objective:prerequisite", float64(1)}})
}

func TestReasonedRelationshipUpdatesAreVersioned(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(filepath.Join(t.TempDir(), "pentest.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	createdProject, err := project.NewService(db).Create("Reasoned Relationship Updates", "", project.Scope{}, project.Defaults{})
	if err != nil {
		t.Fatalf("create project: %v", err)
	}
	service := blackboardv2.NewService(db)
	_, err = service.Apply(ctx, createdProject.ID, blackboardv2.ChangeBatch{
		Schema:         "semantic-change-batch/v2",
		IdempotencyKey: "seed-reasoned-relationship-updates",
		Changes: []blackboardv2.Change{
			{Op: "create", Key: "fact:support-source", Type: "fact", Record: blackboardv2.FactRecord{Category: "authentication", Summary: "Independent reproduction supports the conclusion", Confidence: "confirmed", ScopeStatus: "in_scope"}},
			{Op: "create", Key: "fact:support-target", Type: "fact", Record: blackboardv2.FactRecord{Category: "authentication", Summary: "Login accepts JSON", Confidence: "tentative", ScopeStatus: "in_scope"}},
			{Op: "create", Key: "fact:contradict-source", Type: "fact", Record: blackboardv2.FactRecord{Category: "authentication", Summary: "Independent reproduction contradicts the conclusion", Confidence: "confirmed", ScopeStatus: "in_scope"}},
			{Op: "create", Key: "fact:contradict-target", Type: "fact", Record: blackboardv2.FactRecord{Category: "authentication", Summary: "Login rejects JSON", Confidence: "tentative", ScopeStatus: "in_scope"}},
			{Op: "create", Key: "objective:dependent-update", Type: "objective", Record: blackboardv2.ObjectiveRecord{Status: "open", Objective: "Test administrative login"}},
			{Op: "create", Key: "objective:prerequisite-update", Type: "objective", Record: blackboardv2.ObjectiveRecord{Status: "open", Objective: "Map authentication endpoints"}},
			{Op: "relate", From: "fact:support-source", Relation: "supports", To: "fact:support-target", Reason: "Initial support reason"},
			{Op: "relate", From: "fact:contradict-source", Relation: "contradicts", To: "fact:contradict-target", Reason: "Initial contradiction reason"},
			{Op: "relate", From: "objective:dependent-update", Relation: "depends_on", To: "objective:prerequisite-update", Reason: "Initial dependency reason"},
		},
	})
	if err != nil {
		t.Fatalf("seed reasoned relationship updates: %v", err)
	}

	for index, tt := range []struct {
		relation  string
		from      string
		to        string
		oldReason string
		newReason string
	}{
		{relation: "supports", from: "fact:support-source", to: "fact:support-target", oldReason: "Initial support reason", newReason: "Updated support reason"},
		{relation: "contradicts", from: "fact:contradict-source", to: "fact:contradict-target", oldReason: "Initial contradiction reason", newReason: "Updated contradiction reason"},
		{relation: "depends_on", from: "objective:dependent-update", to: "objective:prerequisite-update", oldReason: "Initial dependency reason", newReason: "Updated dependency reason"},
	} {
		t.Run(tt.relation, func(t *testing.T) {
			for _, invalid := range []struct {
				name    string
				version int
				reason  string
				code    string
			}{
				{name: "negative version before no-op", version: -1, reason: tt.oldReason, code: "semantic_validation"},
				{name: "changed reason requires version", version: 0, reason: tt.newReason, code: "semantic_validation"},
				{name: "stale version", version: 99, reason: tt.newReason, code: "version_conflict"},
			} {
				_, err := service.Apply(ctx, createdProject.ID, blackboardv2.ChangeBatch{
					Schema:         "semantic-change-batch/v2",
					IdempotencyKey: fmt.Sprintf("reject-%s-%s", tt.relation, invalid.name),
					Changes: []blackboardv2.Change{{
						Op: "relate", From: tt.from, Relation: tt.relation, To: tt.to, Version: invalid.version, Reason: invalid.reason,
					}},
				})
				if !isSemanticCode(err, invalid.code) {
					t.Fatalf("%s error = %#v, want %s", invalid.name, err, invalid.code)
				}
			}

			updated, err := service.Apply(ctx, createdProject.ID, blackboardv2.ChangeBatch{
				Schema:         "semantic-change-batch/v2",
				IdempotencyKey: "update-reason-" + tt.relation,
				Changes: []blackboardv2.Change{{
					Op: "relate", From: tt.from, Relation: tt.relation, To: tt.to, Version: 1, Reason: tt.newReason,
				}},
			})
			if err != nil {
				t.Fatalf("update %s reason: %v", tt.relation, err)
			}
			assertChangeRecords(t, updated, 10+index, [][]any{})
			assertRelationResult(t, updated, [][]any{{tt.from, tt.relation, tt.to, float64(2)}})

			noOp, err := service.Apply(ctx, createdProject.ID, blackboardv2.ChangeBatch{
				Schema:         "semantic-change-batch/v2",
				IdempotencyKey: "noop-updated-reason-" + tt.relation,
				Changes: []blackboardv2.Change{{
					Op: "relate", From: tt.from, Relation: tt.relation, To: tt.to, Reason: tt.newReason,
				}},
			})
			if err != nil {
				t.Fatalf("exact %s reason no-op: %v", tt.relation, err)
			}
			assertChangeRecords(t, noOp, 10+index, [][]any{})
			assertRelationResult(t, noOp, [][]any{})

			history, err := service.ReadHistory(ctx, createdProject.ID, tt.from, blackboardv2.HistoryOptions{})
			if err != nil {
				t.Fatalf("read %s relationship history: %v", tt.relation, err)
			}
			if len(history.Items) != 1 || history.Items[0].Version != 1 || history.Items[0].Relation != tt.relation || history.Items[0].Reason != tt.oldReason {
				t.Fatalf("%s relationship history = %#v", tt.relation, history.Items)
			}
			detail, err := service.ReadCurrent(ctx, createdProject.ID, tt.from)
			if err != nil {
				t.Fatalf("read current %s relationship: %v", tt.relation, err)
			}
			if len(detail.Relationships) != 1 || len(detail.Relationships[0]) != 4 || detail.Relationships[0][1] != tt.relation || detail.Relationships[0][3] != tt.newReason {
				t.Fatalf("current %s relationship = %#v", tt.relation, detail.Relationships)
			}
		})
	}
}
