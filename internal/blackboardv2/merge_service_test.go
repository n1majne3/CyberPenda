package blackboardv2_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"pentest/internal/blackboardv2"
	"pentest/internal/project"
	"pentest/internal/store"
	"pentest/internal/task"
)

func TestMergeRejectsInvalidOrUnapprovedRequestsWithoutPartialMutation(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(filepath.Join(t.TempDir(), "pentest.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	projects := project.NewService(db)
	createdProject, err := projects.Create("Merge guards", "", project.Scope{}, project.Defaults{})
	if err != nil {
		t.Fatalf("create project: %v", err)
	}
	foreignProject, err := projects.Create("Foreign merge guards", "", project.Scope{}, project.Defaults{})
	if err != nil {
		t.Fatalf("create foreign project: %v", err)
	}
	service := blackboardv2.NewService(db)
	seed, err := service.Apply(ctx, createdProject.ID, blackboardv2.ChangeBatch{
		Schema: "semantic-change-batch/v2", IdempotencyKey: "seed-merge-guards",
		Changes: []blackboardv2.Change{
			{Op: "create", Key: "entity:a", Type: "entity", Record: blackboardv2.EntityRecord{Status: "active", Kind: "host", Name: "A", Locator: "same.example", ScopeStatus: "in_scope"}},
			{Op: "create", Key: "entity:b", Type: "entity", Record: blackboardv2.EntityRecord{Status: "active", Kind: "host", Name: "B", Locator: "SAME.EXAMPLE.", ScopeStatus: "in_scope"}},
			{Op: "create", Key: "entity:different", Type: "entity", Record: blackboardv2.EntityRecord{Status: "active", Kind: "host", Name: "Different", Locator: "different.example", ScopeStatus: "in_scope"}},
			{Op: "create", Key: "fact:a", Type: "fact", Record: blackboardv2.FactRecord{Category: "service", Summary: "Same service", Confidence: "tentative", ScopeStatus: "in_scope"}},
			{Op: "create", Key: "objective:a", Type: "objective", Record: blackboardv2.ObjectiveRecord{Status: "open", Objective: "First objective"}},
			{Op: "create", Key: "objective:b", Type: "objective", Record: blackboardv2.ObjectiveRecord{Status: "open", Objective: "Second objective"}},
		},
	})
	if err != nil {
		t.Fatalf("seed guarded merge records: %v", err)
	}
	_, err = service.Apply(ctx, foreignProject.ID, blackboardv2.ChangeBatch{
		Schema: "semantic-change-batch/v2", IdempotencyKey: "seed-foreign-merge-record",
		Changes: []blackboardv2.Change{{Op: "create", Key: "entity:foreign", Type: "entity", Record: blackboardv2.EntityRecord{Status: "active", Kind: "host", Name: "Foreign", Locator: "same.example", ScopeStatus: "in_scope"}}},
	})
	if err != nil {
		t.Fatalf("seed foreign merge record: %v", err)
	}

	tests := []struct {
		name       string
		merge      blackboardv2.Change
		wantCode   string
		markerName string
	}{
		{name: "self merge", merge: blackboardv2.Change{Op: "merge", Source: "entity:a", SourceVersion: 1, Canonical: "entity:a", CanonicalVersion: 1}, wantCode: "semantic_validation", markerName: "self"},
		{name: "cross type", merge: blackboardv2.Change{Op: "merge", Source: "entity:a", SourceVersion: 1, Canonical: "fact:a", CanonicalVersion: 1}, wantCode: "semantic_validation", markerName: "cross-type"},
		{name: "Current Work", merge: blackboardv2.Change{Op: "merge", Source: "objective:a", SourceVersion: 1, Canonical: "objective:b", CanonicalVersion: 1}, wantCode: "semantic_validation", markerName: "current-work"},
		{name: "stale source", merge: blackboardv2.Change{Op: "merge", Source: "entity:a", SourceVersion: 2, Canonical: "entity:b", CanonicalVersion: 1}, wantCode: "version_conflict", markerName: "stale-source"},
		{name: "stale canonical", merge: blackboardv2.Change{Op: "merge", Source: "entity:a", SourceVersion: 1, Canonical: "entity:b", CanonicalVersion: 2}, wantCode: "version_conflict", markerName: "stale-canonical"},
		{name: "unapproved similarity", merge: blackboardv2.Change{Op: "merge", Source: "entity:a", SourceVersion: 1, Canonical: "entity:different", CanonicalVersion: 1}, wantCode: "semantic_validation", markerName: "unapproved"},
		{name: "cross Project", merge: blackboardv2.Change{Op: "merge", Source: "entity:a", SourceVersion: 1, Canonical: "entity:foreign", CanonicalVersion: 1}, wantCode: "not_found", markerName: "cross-project"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			marker := "entity:rollback-" + test.markerName
			_, err := service.Apply(ctx, createdProject.ID, blackboardv2.ChangeBatch{
				Schema: "semantic-change-batch/v2", IdempotencyKey: "reject-merge-" + test.markerName,
				Changes: []blackboardv2.Change{
					{Op: "create", Key: marker, Type: "entity", Record: blackboardv2.EntityRecord{Status: "active", Kind: "host", Name: "Must roll back", ScopeStatus: "unknown"}},
					test.merge,
				},
			})
			assertMergeErrorCode(t, err, test.wantCode)
			if _, err := service.ReadCurrent(ctx, createdProject.ID, marker); err == nil {
				t.Fatalf("failed merge retained partial marker %s", marker)
			}
			snapshot, err := service.RuntimeSnapshot(ctx, createdProject.ID)
			if err != nil {
				t.Fatalf("read snapshot after rejected merge: %v", err)
			}
			if snapshot.Revision != seed.Revision {
				t.Fatalf("rejected merge revision = %d, want %d", snapshot.Revision, seed.Revision)
			}
		})
	}

	_, err = service.Apply(ctx, createdProject.ID, blackboardv2.ChangeBatch{
		Schema: "semantic-change-batch/v2", IdempotencyKey: "merge-a-into-b",
		Changes: []blackboardv2.Change{{Op: "merge", Source: "entity:a", SourceVersion: 1, Canonical: "entity:b", CanonicalVersion: 1}},
	})
	if err != nil {
		t.Fatalf("create first redirect: %v", err)
	}
	_, err = service.Apply(ctx, createdProject.ID, blackboardv2.ChangeBatch{
		Schema: "semantic-change-batch/v2", IdempotencyKey: "seed-chain-target",
		Changes: []blackboardv2.Change{{Op: "create", Key: "entity:c", Type: "entity", Record: blackboardv2.EntityRecord{Status: "active", Kind: "host", Name: "C", Locator: "same.example", ScopeStatus: "in_scope"}}},
	})
	if err != nil {
		t.Fatalf("seed chain target: %v", err)
	}
	_, err = service.Apply(ctx, createdProject.ID, blackboardv2.ChangeBatch{
		Schema: "semantic-change-batch/v2", IdempotencyKey: "reject-redirect-chain",
		Changes: []blackboardv2.Change{{Op: "merge", Source: "entity:b", SourceVersion: 1, Canonical: "entity:c", CanonicalVersion: 1}},
	})
	assertMergeErrorCode(t, err, "key_conflict")
}

func TestRuntimeMergeRequiresGovernedOperatorApprovalBeforeTargetLookup(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(filepath.Join(t.TempDir(), "pentest.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	projects := project.NewService(db)
	createdProject, err := projects.Create("Runtime merge approval", "", project.Scope{}, project.Defaults{})
	if err != nil {
		t.Fatalf("create project: %v", err)
	}
	tasks := task.NewService(db, projects)
	createdTask, err := tasks.Create(task.CreateRequest{ProjectID: createdProject.ID, Goal: "Attempt unapproved merge", Runner: task.RunnerSandbox})
	if err != nil {
		t.Fatalf("create task: %v", err)
	}
	continuation, err := tasks.CreateContinuation(createdTask.ID, "profile-runtime-merge", "codex", task.RunnerSandbox)
	if err != nil {
		t.Fatalf("create continuation: %v", err)
	}
	service := blackboardv2.NewService(db)
	_, err = service.ApplyForContinuation(ctx, createdProject.ID, continuation.ID, blackboardv2.ChangeBatch{
		Schema: "semantic-change-batch/v2", IdempotencyKey: "runtime-merge-with-missing-targets",
		Changes: []blackboardv2.Change{{Op: "merge", Source: "evidence:missing-source", SourceVersion: 1, Canonical: "evidence:missing-canonical", CanonicalVersion: 1}},
	})
	var semanticErr *blackboardv2.Error
	if !errors.As(err, &semanticErr) || semanticErr.Code != "authority_denied" || semanticErr.Path != "changes[0].op" {
		t.Fatalf("runtime merge error = %#v, want authority_denied before target lookup", err)
	}
	snapshot, err := service.RuntimeSnapshot(ctx, createdProject.ID)
	if err != nil {
		t.Fatalf("read snapshot after rejected runtime merge: %v", err)
	}
	if snapshot.Revision != 0 {
		t.Fatalf("rejected runtime merge revision = %d, want 0", snapshot.Revision)
	}
}

func assertMergeErrorCode(t *testing.T, err error, code string) {
	t.Helper()
	var semanticErr *blackboardv2.Error
	if !errors.As(err, &semanticErr) || semanticErr.Code != code {
		t.Fatalf("merge error = %#v, want code %q", err, code)
	}
}

func TestMergeProjectKnowledgeAtomicallyRewritesHistoryAndRedirects(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(filepath.Join(t.TempDir(), "pentest.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	createdProject, err := project.NewService(db).Create("Governed merge", "", project.Scope{}, project.Defaults{})
	if err != nil {
		t.Fatalf("create project: %v", err)
	}
	service := blackboardv2.NewService(db)

	seed := blackboardv2.ChangeBatch{
		Schema:         "semantic-change-batch/v2",
		IdempotencyKey: "seed-governed-merge",
		Changes: []blackboardv2.Change{
			{Op: "create", Key: "entity:source", Type: "entity", Record: blackboardv2.EntityRecord{Status: "active", Kind: "host", Name: "Original admin host", Locator: "ADMIN.EXAMPLE.", ScopeStatus: "in_scope"}},
			{Op: "create", Key: "entity:canonical", Type: "entity", Record: blackboardv2.EntityRecord{Status: "active", Kind: "host", Name: "Admin host", Locator: "admin.example", ScopeStatus: "in_scope"}},
			{Op: "create", Key: "entity:parent", Type: "entity", Record: blackboardv2.EntityRecord{Status: "active", Kind: "host", Name: "Admin parent", Locator: "parent.example", ScopeStatus: "in_scope"}},
			{Op: "create", Key: "fact:source-context", Type: "fact", Record: blackboardv2.FactRecord{Category: "service", Summary: "Source context", Confidence: "tentative", ScopeStatus: "in_scope"}},
			{Op: "relate", From: "fact:source-context", Relation: "about", To: "entity:source"},
			{Op: "relate", From: "fact:source-context", Relation: "about", To: "entity:canonical"},
			{Op: "relate", From: "entity:source", Relation: "part_of", To: "entity:parent"},
		},
	}
	seedResult, err := service.Apply(ctx, createdProject.ID, seed)
	if err != nil {
		t.Fatalf("seed merge records: %v", err)
	}

	var mergeBatch blackboardv2.ChangeBatch
	if err := json.Unmarshal([]byte(`{
		"schema":"semantic-change-batch/v2",
		"idempotency_key":"merge-admin-host",
		"changes":[{
			"op":"merge",
			"source":"entity:source",
			"source_version":1,
			"canonical":"entity:canonical",
			"canonical_version":1,
			"canonical_record":{"name":"Canonical admin host","description":"Consolidated duplicate host"}
		}]
	}`), &mergeBatch); err != nil {
		t.Fatalf("decode merge change: %v", err)
	}
	merged, err := service.Apply(ctx, createdProject.ID, mergeBatch)
	if err != nil {
		t.Fatalf("merge Project Knowledge: %v", err)
	}
	if merged.Revision != seedResult.Revision+1 {
		t.Fatalf("merge revision = %d, want %d", merged.Revision, seedResult.Revision+1)
	}
	if len(merged.Records) != 1 || merged.Records[0][0] != "entity:canonical" || merged.Records[0][1] != 2 {
		t.Fatalf("merge record result = %#v", merged.Records)
	}

	detail, err := service.ReadCurrent(ctx, createdProject.ID, "entity:source")
	if err != nil {
		t.Fatalf("read through redirect: %v", err)
	}
	if detail.Key != "entity:canonical" || detail.Version != 2 || detail.Record.Name != "Canonical admin host" || detail.Record.Description != "Consolidated duplicate host" {
		t.Fatalf("redirected detail = %#v", detail)
	}
	if len(detail.Relationships) != 2 {
		t.Fatalf("canonical relationships = %#v, want deduplicated incoming and rewritten outgoing", detail.Relationships)
	}

	history, err := service.ReadHistory(ctx, createdProject.ID, "entity:source", blackboardv2.HistoryOptions{})
	if err != nil {
		t.Fatalf("read source Semantic History through redirect: %v", err)
	}
	if history.Key != "entity:canonical" {
		t.Fatalf("redirected source history = %#v", history)
	}
	if !historyContainsMergeSource(history.Items, "Original admin host", "entity:source") {
		t.Fatalf("source meaning and relationship context = %#v", history.Items)
	}
	canonicalHistory, err := service.ReadHistory(ctx, createdProject.ID, "entity:canonical", blackboardv2.HistoryOptions{})
	if err != nil {
		t.Fatalf("read canonical Semantic History: %v", err)
	}
	if canonicalHistory.Key != "entity:canonical" || !historyContainsMergeSource(canonicalHistory.Items, "Original admin host", "entity:source") {
		t.Fatalf("canonical history omitted merged source meaning: %#v", canonicalHistory)
	}

	snapshot, err := service.RuntimeSnapshot(ctx, createdProject.ID)
	if err != nil {
		t.Fatalf("read Runtime Snapshot: %v", err)
	}
	if snapshot.Knowledge.Entities["entity:source"].Name != "" {
		t.Fatalf("Runtime Snapshot exposes merged source: %#v", snapshot.Knowledge.Entities)
	}
	if snapshot.Knowledge.Entities["entity:canonical"].Name != "Canonical admin host" || len(snapshot.Relations) != 2 {
		t.Fatalf("Runtime Snapshot after merge = %#v", snapshot)
	}

	updatedName := "Updated through redirect"
	redirectedWrite, err := service.Apply(ctx, createdProject.ID, blackboardv2.ChangeBatch{
		Schema:         "semantic-change-batch/v2",
		IdempotencyKey: "update-through-redirect",
		Changes: []blackboardv2.Change{{
			Op: "update", Key: "entity:source", Version: 2, Type: "entity", Record: blackboardv2.EntityPatch{Name: &updatedName},
		}},
	})
	if err != nil {
		t.Fatalf("update through redirect: %v", err)
	}
	if len(redirectedWrite.Records) != 1 || redirectedWrite.Records[0][0] != "entity:canonical" || redirectedWrite.Records[0][1] != 3 {
		t.Fatalf("redirected write result = %#v", redirectedWrite.Records)
	}

	redirectedRelation, err := service.Apply(ctx, createdProject.ID, blackboardv2.ChangeBatch{
		Schema:         "semantic-change-batch/v2",
		IdempotencyKey: "relate-through-redirect",
		Changes:        []blackboardv2.Change{{Op: "relate", From: "entity:source", Relation: "part_of", To: "entity:parent"}},
	})
	if err != nil {
		t.Fatalf("relate through redirect: %v", err)
	}
	if len(redirectedRelation.Relations) != 0 {
		t.Fatalf("identical redirected relationship should be a no-op: %#v", redirectedRelation.Relations)
	}
	canonical, err := service.ReadCurrent(ctx, createdProject.ID, "entity:canonical")
	if err != nil {
		t.Fatalf("read canonical after redirected writes: %v", err)
	}
	if canonical.Key != "entity:canonical" || canonical.Version != 3 || canonical.Record.Name != updatedName {
		t.Fatalf("canonical after redirected writes = %#v", canonical)
	}
}

func TestMergeAcceptsClearOnlyCanonicalUpdate(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(filepath.Join(t.TempDir(), "pentest.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	createdProject, err := project.NewService(db).Create("Clear-only merge", "", project.Scope{}, project.Defaults{})
	if err != nil {
		t.Fatalf("create project: %v", err)
	}
	service := blackboardv2.NewService(db)
	_, err = service.Apply(ctx, createdProject.ID, blackboardv2.ChangeBatch{
		Schema: "semantic-change-batch/v2", IdempotencyKey: "seed-clear-only-merge",
		Changes: []blackboardv2.Change{
			{Op: "create", Key: "entity:clear-source", Type: "entity", Record: blackboardv2.EntityRecord{Status: "active", Kind: "host", Name: "Source", Locator: "clear.example", ScopeStatus: "in_scope"}},
			{Op: "create", Key: "entity:clear-canonical", Type: "entity", Record: blackboardv2.EntityRecord{Status: "active", Kind: "host", Name: "Canonical", Locator: "CLEAR.EXAMPLE.", Description: "Remove during merge", ScopeStatus: "in_scope"}},
		},
	})
	if err != nil {
		t.Fatalf("seed clear-only merge: %v", err)
	}
	merged, err := service.Apply(ctx, createdProject.ID, blackboardv2.ChangeBatch{
		Schema: "semantic-change-batch/v2", IdempotencyKey: "clear-only-merge",
		Changes: []blackboardv2.Change{{Op: "merge", Source: "entity:clear-source", SourceVersion: 1, Canonical: "entity:clear-canonical", CanonicalVersion: 1, Clear: []string{"description"}}},
	})
	if err != nil {
		t.Fatalf("apply clear-only merge: %v", err)
	}
	if len(merged.Records) != 1 || merged.Records[0][0] != "entity:clear-canonical" || merged.Records[0][1] != 2 {
		t.Fatalf("clear-only merge result = %#v", merged.Records)
	}
	detail, err := service.ReadCurrent(ctx, createdProject.ID, "entity:clear-source")
	if err != nil {
		t.Fatalf("read clear-only merge redirect: %v", err)
	}
	if detail.Record.Description != "" {
		t.Fatalf("clear-only merge retained description: %#v", detail.Record)
	}
}

func TestMergeWireRejectsNullOrDuplicateClearFields(t *testing.T) {
	for name, raw := range map[string]string{
		"null clear":      `{"schema":"semantic-change-batch/v2","idempotency_key":"merge-null-clear","changes":[{"op":"merge","source":"fact:source","source_version":1,"canonical":"fact:canonical","canonical_version":1,"clear":null}]}`,
		"duplicate clear": `{"schema":"semantic-change-batch/v2","idempotency_key":"merge-duplicate-clear","changes":[{"op":"merge","source":"fact:source","source_version":1,"canonical":"fact:canonical","canonical_version":1,"clear":["body","body"]}]}`,
	} {
		t.Run(name, func(t *testing.T) {
			var batch blackboardv2.ChangeBatch
			if err := json.Unmarshal([]byte(raw), &batch); err == nil {
				t.Fatalf("decoded invalid merge clear payload: %s", raw)
			}
		})
	}
}

func TestUnrelateThroughRedirectUsesCanonicalIdentityAndExactVersion(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(filepath.Join(t.TempDir(), "pentest.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	projects := project.NewService(db)
	createdProject, err := projects.Create("Redirected unrelate", "", project.Scope{}, project.Defaults{})
	if err != nil {
		t.Fatalf("create project: %v", err)
	}
	foreignProject, err := projects.Create("Foreign redirected unrelate", "", project.Scope{}, project.Defaults{})
	if err != nil {
		t.Fatalf("create foreign project: %v", err)
	}
	service := blackboardv2.NewService(db)
	_, err = service.Apply(ctx, createdProject.ID, blackboardv2.ChangeBatch{
		Schema: "semantic-change-batch/v2", IdempotencyKey: "seed-redirected-unrelate",
		Changes: []blackboardv2.Change{
			{Op: "create", Key: "entity:unrelate-source", Type: "entity", Record: blackboardv2.EntityRecord{Status: "active", Kind: "host", Name: "Source", Locator: "unrelate.example", ScopeStatus: "in_scope"}},
			{Op: "create", Key: "entity:unrelate-canonical", Type: "entity", Record: blackboardv2.EntityRecord{Status: "active", Kind: "host", Name: "Canonical", Locator: "UNRELATE.EXAMPLE.", ScopeStatus: "in_scope"}},
			{Op: "create", Key: "entity:unrelate-parent", Type: "entity", Record: blackboardv2.EntityRecord{Status: "active", Kind: "host", Name: "Parent", Locator: "parent-unrelate.example", ScopeStatus: "in_scope"}},
			{Op: "relate", From: "entity:unrelate-canonical", Relation: "part_of", To: "entity:unrelate-parent"},
		},
	})
	if err != nil {
		t.Fatalf("seed redirected unrelate: %v", err)
	}
	_, err = service.Apply(ctx, foreignProject.ID, blackboardv2.ChangeBatch{
		Schema: "semantic-change-batch/v2", IdempotencyKey: "seed-foreign-unrelate",
		Changes: []blackboardv2.Change{
			{Op: "create", Key: "entity:unrelate-source", Type: "entity", Record: blackboardv2.EntityRecord{Status: "active", Kind: "host", Name: "Foreign source", Locator: "foreign-unrelate.example", ScopeStatus: "in_scope"}},
			{Op: "create", Key: "entity:unrelate-parent", Type: "entity", Record: blackboardv2.EntityRecord{Status: "active", Kind: "host", Name: "Foreign parent", Locator: "foreign-parent-unrelate.example", ScopeStatus: "in_scope"}},
			{Op: "relate", From: "entity:unrelate-source", Relation: "part_of", To: "entity:unrelate-parent"},
		},
	})
	if err != nil {
		t.Fatalf("seed foreign unrelate relationship: %v", err)
	}
	merged, err := service.Apply(ctx, createdProject.ID, blackboardv2.ChangeBatch{
		Schema: "semantic-change-batch/v2", IdempotencyKey: "merge-before-unrelate",
		Changes: []blackboardv2.Change{{Op: "merge", Source: "entity:unrelate-source", SourceVersion: 1, Canonical: "entity:unrelate-canonical", CanonicalVersion: 1}},
	})
	if err != nil {
		t.Fatalf("merge before redirected unrelate: %v", err)
	}
	_, err = service.Apply(ctx, createdProject.ID, blackboardv2.ChangeBatch{
		Schema: "semantic-change-batch/v2", IdempotencyKey: "stale-redirected-unrelate",
		Changes: []blackboardv2.Change{
			{Op: "create", Key: "entity:unrelate-rollback", Type: "entity", Record: blackboardv2.EntityRecord{Status: "active", Kind: "host", Name: "Must roll back", ScopeStatus: "unknown"}},
			{Op: "unrelate", From: "entity:unrelate-source", Relation: "part_of", To: "entity:unrelate-parent", Version: 2},
		},
	})
	assertMergeErrorCode(t, err, "version_conflict")
	if _, err := service.ReadCurrent(ctx, createdProject.ID, "entity:unrelate-rollback"); err == nil {
		t.Fatal("stale redirected unrelate retained a partial record")
	}

	var batch blackboardv2.ChangeBatch
	if err := json.Unmarshal([]byte(`{"schema":"semantic-change-batch/v2","idempotency_key":"remove-through-redirect","changes":[{"op":"unrelate","from":"entity:unrelate-source","relation":"part_of","to":"entity:unrelate-parent","version":1}]}`), &batch); err != nil {
		t.Fatalf("decode redirected unrelate: %v", err)
	}
	removed, err := service.Apply(ctx, createdProject.ID, batch)
	if err != nil {
		t.Fatalf("unrelate through redirect: %v", err)
	}
	if removed.Revision != merged.Revision+1 || len(removed.Relations) != 1 || removed.Relations[0][0] != "entity:unrelate-canonical" || removed.Relations[0][1] != "part_of" || removed.Relations[0][2] != "entity:unrelate-parent" || removed.Relations[0][3] != 1 {
		t.Fatalf("redirected unrelate result = %#v", removed)
	}
	replay, err := service.Apply(ctx, createdProject.ID, batch)
	if err != nil {
		t.Fatalf("replay redirected unrelate: %v", err)
	}
	if !reflect.DeepEqual(replay, removed) {
		t.Fatalf("redirected unrelate replay = %#v, want %#v", replay, removed)
	}
	detail, err := service.ReadCurrent(ctx, createdProject.ID, "entity:unrelate-source")
	if err != nil {
		t.Fatalf("read canonical after redirected unrelate: %v", err)
	}
	for _, relationship := range detail.Relationships {
		if relationship[0] == "entity:unrelate-canonical" && relationship[1] == "part_of" && relationship[2] == "entity:unrelate-parent" {
			t.Fatalf("redirected unrelate left current relationship: %#v", detail.Relationships)
		}
	}
	foreign, err := service.ReadCurrent(ctx, foreignProject.ID, "entity:unrelate-source")
	if err != nil {
		t.Fatalf("read foreign relationship after redirected unrelate: %v", err)
	}
	if len(foreign.Relationships) != 1 || foreign.Relationships[0][0] != "entity:unrelate-source" || foreign.Relationships[0][1] != "part_of" || foreign.Relationships[0][2] != "entity:unrelate-parent" {
		t.Fatalf("redirected unrelate crossed Projects: %#v", foreign.Relationships)
	}
}

func historyContainsMergeSource(items []blackboardv2.HistoryItem, sourceName, sourceKey string) bool {
	hasMeaning, hasIncoming, hasOutgoing := false, false, false
	for _, item := range items {
		if item.Kind == "record" && item.Record != nil && item.Record.Name == sourceName {
			hasMeaning = true
		}
		if item.Kind == "relationship" && item.To == sourceKey {
			hasIncoming = true
		}
		if item.Kind == "relationship" && item.From == sourceKey {
			hasOutgoing = true
		}
	}
	return hasMeaning && hasIncoming && hasOutgoing
}

func TestMergeSupportsFactFindingAndSolutionTypes(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(filepath.Join(t.TempDir(), "pentest.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	projects := project.NewService(db)
	pentestProject, err := projects.Create("Knowledge type merge", "", project.Scope{}, project.Defaults{})
	if err != nil {
		t.Fatalf("create pentest project: %v", err)
	}
	ctfProject, err := projects.CreateWithKind("Solution type merge", "", project.KindCTFChallenge, project.Scope{}, project.Defaults{})
	if err != nil {
		t.Fatalf("create CTF project: %v", err)
	}
	service := blackboardv2.NewService(db)

	_, err = service.Apply(ctx, pentestProject.ID, blackboardv2.ChangeBatch{
		Schema: "semantic-change-batch/v2", IdempotencyKey: "seed-fact-finding-merges",
		Changes: []blackboardv2.Change{
			{Op: "create", Key: "fact:source", Type: "fact", Record: blackboardv2.FactRecord{Category: "service", Summary: "Admin---Portal", Body: "Source body", Confidence: "tentative", ScopeStatus: "in_scope"}},
			{Op: "create", Key: "fact:canonical", Type: "fact", Record: blackboardv2.FactRecord{Category: "service", Summary: "admin portal", Body: "Canonical body", Confidence: "tentative", ScopeStatus: "in_scope"}},
			{Op: "create", Key: "finding:source", Type: "finding", Record: blackboardv2.FindingRecord{Status: "unconfirmed", Title: "SQL Injection", Target: "POST /login", Description: "Source finding"}},
			{Op: "create", Key: "finding:canonical", Type: "finding", Record: blackboardv2.FindingRecord{Status: "unconfirmed", Title: "sql injection", Target: "POST /login", Description: "Canonical finding"}},
		},
	})
	if err != nil {
		t.Fatalf("seed Fact and Finding merges: %v", err)
	}
	factBody := "Approved consolidated Fact body"
	mergedFact, err := service.Apply(ctx, pentestProject.ID, blackboardv2.ChangeBatch{
		Schema: "semantic-change-batch/v2", IdempotencyKey: "merge-fact-type",
		Changes: []blackboardv2.Change{{Op: "merge", Source: "fact:source", SourceVersion: 1, Canonical: "fact:canonical", CanonicalVersion: 1, CanonicalRecord: blackboardv2.FactPatch{Body: &factBody}}},
	})
	if err != nil {
		t.Fatalf("merge Fact: %v", err)
	}
	if len(mergedFact.Records) != 1 || mergedFact.Records[0][0] != "fact:canonical" || mergedFact.Records[0][1] != 2 {
		t.Fatalf("Fact merge result = %#v", mergedFact.Records)
	}
	findingDescription := "Approved consolidated Finding"
	mergedFinding, err := service.Apply(ctx, pentestProject.ID, blackboardv2.ChangeBatch{
		Schema: "semantic-change-batch/v2", IdempotencyKey: "merge-finding-type",
		Changes: []blackboardv2.Change{{Op: "merge", Source: "finding:source", SourceVersion: 1, Canonical: "finding:canonical", CanonicalVersion: 1, CanonicalRecord: blackboardv2.FindingPatch{Description: &findingDescription}}},
	})
	if err != nil {
		t.Fatalf("merge Finding: %v", err)
	}
	if len(mergedFinding.Records) != 1 || mergedFinding.Records[0][0] != "finding:canonical" || mergedFinding.Records[0][1] != 2 {
		t.Fatalf("Finding merge result = %#v", mergedFinding.Records)
	}

	_, err = service.Apply(ctx, ctfProject.ID, blackboardv2.ChangeBatch{
		Schema: "semantic-change-batch/v2", IdempotencyKey: "seed-solution-merge",
		Changes: []blackboardv2.Change{
			{Op: "create", Key: "solution:source", Type: "solution", Record: blackboardv2.SolutionRecord{Status: "candidate", Kind: "flag", Summary: "Source candidate", Value: "FLAG{same}"}},
			{Op: "create", Key: "solution:canonical", Type: "solution", Record: blackboardv2.SolutionRecord{Status: "candidate", Kind: "flag", Summary: "Canonical candidate", Value: "FLAG{same}"}},
		},
	})
	if err != nil {
		t.Fatalf("seed Solution merge: %v", err)
	}
	solutionSummary := "Approved consolidated candidate"
	mergedSolution, err := service.Apply(ctx, ctfProject.ID, blackboardv2.ChangeBatch{
		Schema: "semantic-change-batch/v2", IdempotencyKey: "merge-solution-type",
		Changes: []blackboardv2.Change{{Op: "merge", Source: "solution:source", SourceVersion: 1, Canonical: "solution:canonical", CanonicalVersion: 1, CanonicalRecord: blackboardv2.SolutionPatch{Summary: &solutionSummary}}},
	})
	if err != nil {
		t.Fatalf("merge Solution: %v", err)
	}
	if len(mergedSolution.Records) != 1 || mergedSolution.Records[0][0] != "solution:canonical" || mergedSolution.Records[0][1] != 2 {
		t.Fatalf("Solution merge result = %#v", mergedSolution.Records)
	}
	for _, redirect := range []struct{ projectID, source, canonical string }{
		{pentestProject.ID, "fact:source", "fact:canonical"},
		{pentestProject.ID, "finding:source", "finding:canonical"},
		{ctfProject.ID, "solution:source", "solution:canonical"},
	} {
		detail, err := service.ReadCurrent(ctx, redirect.projectID, redirect.source)
		if err != nil {
			t.Fatalf("read %s redirect: %v", redirect.source, err)
		}
		if detail.Key != redirect.canonical {
			t.Fatalf("%s resolved to %q, want %q", redirect.source, detail.Key, redirect.canonical)
		}
	}
}

func TestMergeEvidenceAndRetainThroughRedirect(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	db, err := store.Open(filepath.Join(root, "pentest.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	projects := project.NewService(db)
	createdProject, err := projects.Create("Evidence merge", "", project.Scope{}, project.Defaults{})
	if err != nil {
		t.Fatalf("create project: %v", err)
	}
	tasks := task.NewService(db, projects)
	createdTask, err := tasks.Create(task.CreateRequest{ProjectID: createdProject.ID, Goal: "Merge duplicate Evidence", Runner: task.RunnerSandbox})
	if err != nil {
		t.Fatalf("create task: %v", err)
	}
	continuation, err := tasks.CreateContinuation(createdTask.ID, "profile-evidence-merge", "codex", task.RunnerSandbox)
	if err != nil {
		t.Fatalf("create continuation: %v", err)
	}
	runtimeRoot := filepath.Join(root, "runtime")
	service := blackboardv2.NewServiceWithEvidence(db, blackboardv2.EvidenceConfig{RuntimeRoot: runtimeRoot, ArtifactRoot: runtimeRoot})
	_, err = service.ApplyForContinuation(ctx, createdProject.ID, continuation.ID, blackboardv2.ChangeBatch{
		Schema: "semantic-change-batch/v2", IdempotencyKey: "prepare-evidence-merge",
		Changes: []blackboardv2.Change{
			{Op: "create", Key: "objective:evidence-merge", Type: "objective", Record: blackboardv2.ObjectiveRecord{Status: "open", Objective: "Merge duplicate Evidence"}},
			{Op: "create", Key: "attempt:evidence-merge", Type: "attempt", Record: blackboardv2.AttemptRecord{Status: "open", Summary: "Capture duplicate Evidence"}},
			{Op: "relate", From: "attempt:evidence-merge", Relation: "tests", To: "objective:evidence-merge"},
		},
	})
	if err != nil {
		t.Fatalf("prepare Evidence merge: %v", err)
	}
	workdir := filepath.Join(runtimeRoot, createdTask.ID, "workdir")
	if err := os.MkdirAll(workdir, 0o700); err != nil {
		t.Fatalf("create workdir: %v", err)
	}
	for _, name := range []string{"source.txt", "canonical.txt", "redirect.txt"} {
		if err := os.WriteFile(filepath.Join(workdir, name), []byte("identical retained proof"), 0o600); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
	for _, evidence := range []struct{ key, source, idempotency string }{
		{"evidence:source", "source.txt", "retain-source-evidence"},
		{"evidence:canonical", "canonical.txt", "retain-canonical-evidence"},
	} {
		_, err := service.RetainEvidenceForContinuation(ctx, createdProject.ID, continuation.ID, blackboardv2.RetainEvidenceRequest{
			IdempotencyKey: evidence.idempotency, Key: evidence.key, Attempt: "attempt:evidence-merge", SourcePath: evidence.source, ArtifactType: "text", Summary: "Retained duplicate proof",
		})
		if err != nil {
			t.Fatalf("retain %s: %v", evidence.key, err)
		}
	}
	merged, err := service.Apply(ctx, createdProject.ID, blackboardv2.ChangeBatch{
		Schema: "semantic-change-batch/v2", IdempotencyKey: "merge-evidence-type",
		Changes: []blackboardv2.Change{{Op: "merge", Source: "evidence:source", SourceVersion: 1, Canonical: "evidence:canonical", CanonicalVersion: 1}},
	})
	if err != nil {
		t.Fatalf("merge Evidence: %v", err)
	}
	if len(merged.Records) != 1 || merged.Records[0][0] != "evidence:canonical" || merged.Records[0][1] != 1 {
		t.Fatalf("Evidence merge result = %#v", merged.Records)
	}
	_, err = service.RetainEvidenceForContinuation(ctx, createdProject.ID, continuation.ID, blackboardv2.RetainEvidenceRequest{
		IdempotencyKey: "retain-source-evidence", Key: "evidence:source", Attempt: "attempt:evidence-merge", SourcePath: "source.txt", ArtifactType: "text", Summary: "Retained duplicate proof",
	})
	if err != nil {
		t.Fatalf("replay pre-merge Evidence retention after redirect: %v", err)
	}
	redirected, err := service.RetainEvidenceForContinuation(ctx, createdProject.ID, continuation.ID, blackboardv2.RetainEvidenceRequest{
		IdempotencyKey: "retain-through-evidence-redirect", Key: "evidence:source", Version: 1, Attempt: "attempt:evidence-merge", SourcePath: "redirect.txt", ArtifactType: "text", Summary: "Updated through Evidence redirect",
	})
	if err != nil {
		t.Fatalf("retain through Evidence redirect: %v", err)
	}
	if len(redirected.Records) != 1 || redirected.Records[0][0] != "evidence:canonical" || redirected.Records[0][1] != 2 {
		t.Fatalf("redirected Evidence result = %#v", redirected.Records)
	}
}

func TestMergePreservesRelationshipVersionsReasonsAndSelfLinkContext(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(filepath.Join(t.TempDir(), "pentest.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	createdProject, err := project.NewService(db).Create("Merge relationship context", "", project.Scope{}, project.Defaults{})
	if err != nil {
		t.Fatalf("create project: %v", err)
	}
	service := blackboardv2.NewService(db)
	_, err = service.Apply(ctx, createdProject.ID, blackboardv2.ChangeBatch{
		Schema: "semantic-change-batch/v2", IdempotencyKey: "seed-merge-relationship-context",
		Changes: []blackboardv2.Change{
			{Op: "create", Key: "fact:source", Type: "fact", Record: blackboardv2.FactRecord{Category: "service", Summary: "Admin portal", Confidence: "tentative", ScopeStatus: "in_scope"}},
			{Op: "create", Key: "fact:canonical", Type: "fact", Record: blackboardv2.FactRecord{Category: "service", Summary: "admin portal", Confidence: "tentative", ScopeStatus: "in_scope"}},
			{Op: "create", Key: "fact:target", Type: "fact", Record: blackboardv2.FactRecord{Category: "service", Summary: "Target service", Confidence: "tentative", ScopeStatus: "in_scope"}},
			{Op: "relate", From: "fact:source", Relation: "supports", To: "fact:target", Reason: "Source reason v1"},
			{Op: "relate", From: "fact:canonical", Relation: "supports", To: "fact:target", Reason: "Canonical reason wins"},
			{Op: "relate", From: "fact:target", Relation: "contradicts", To: "fact:source", Reason: "Incoming source context v1"},
			{Op: "relate", From: "fact:source", Relation: "contradicts", To: "fact:canonical", Reason: "Becomes a self-link"},
		},
	})
	if err != nil {
		t.Fatalf("seed merge relationships: %v", err)
	}
	_, err = service.Apply(ctx, createdProject.ID, blackboardv2.ChangeBatch{
		Schema: "semantic-change-batch/v2", IdempotencyKey: "version-source-merge-relationships",
		Changes: []blackboardv2.Change{
			{Op: "relate", From: "fact:source", Relation: "supports", To: "fact:target", Version: 1, Reason: "Source reason v2"},
			{Op: "relate", From: "fact:target", Relation: "contradicts", To: "fact:source", Version: 1, Reason: "Incoming source context v2"},
		},
	})
	if err != nil {
		t.Fatalf("version source relationships: %v", err)
	}
	merged, err := service.Apply(ctx, createdProject.ID, blackboardv2.ChangeBatch{
		Schema: "semantic-change-batch/v2", IdempotencyKey: "merge-versioned-relationships",
		Changes: []blackboardv2.Change{{Op: "merge", Source: "fact:source", SourceVersion: 1, Canonical: "fact:canonical", CanonicalVersion: 1}},
	})
	if err != nil {
		t.Fatalf("merge versioned relationships: %v", err)
	}
	if len(merged.Relations) != 1 || merged.Relations[0][0] != "fact:target" || merged.Relations[0][1] != "contradicts" || merged.Relations[0][2] != "fact:canonical" || merged.Relations[0][3] != 2 {
		t.Fatalf("rewritten relationship result = %#v", merged.Relations)
	}
	detail, err := service.ReadCurrent(ctx, createdProject.ID, "fact:canonical")
	if err != nil {
		t.Fatalf("read canonical relationships: %v", err)
	}
	if len(detail.Relationships) != 2 || detail.Relationships[0][3] != "Canonical reason wins" || detail.Relationships[1][3] != "Incoming source context v2" {
		t.Fatalf("canonical relationships after deterministic collision = %#v", detail.Relationships)
	}
	history, err := service.ReadHistory(ctx, createdProject.ID, "fact:source", blackboardv2.HistoryOptions{})
	if err != nil {
		t.Fatalf("read source relationship history: %v", err)
	}
	wantReasons := map[string]bool{
		"Source reason v1":           false,
		"Source reason v2":           false,
		"Incoming source context v1": false,
		"Incoming source context v2": false,
		"Becomes a self-link":        false,
	}
	for _, item := range history.Items {
		if _, ok := wantReasons[item.Reason]; ok {
			wantReasons[item.Reason] = true
		}
	}
	for reason, found := range wantReasons {
		if !found {
			t.Errorf("source Semantic History did not preserve %q: %#v", reason, history.Items)
		}
	}
}

func TestRelationlessMergedFactHistoryNamesOriginalSourceKey(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(filepath.Join(t.TempDir(), "pentest.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	createdProject, err := project.NewService(db).Create("Relationless merge history", "", project.Scope{}, project.Defaults{})
	if err != nil {
		t.Fatalf("create project: %v", err)
	}
	service := blackboardv2.NewService(db)
	_, err = service.Apply(ctx, createdProject.ID, blackboardv2.ChangeBatch{
		Schema: "semantic-change-batch/v2", IdempotencyKey: "seed-relationless-fact-merge",
		Changes: []blackboardv2.Change{
			{Op: "create", Key: "fact:relationless-source", Type: "fact", Record: blackboardv2.FactRecord{Category: "service", Summary: "Relationless duplicate", Body: "Original source meaning", Confidence: "tentative", ScopeStatus: "in_scope"}},
			{Op: "create", Key: "fact:relationless-canonical", Type: "fact", Record: blackboardv2.FactRecord{Category: "service", Summary: "relationless duplicate", Body: "Canonical meaning", Confidence: "tentative", ScopeStatus: "in_scope"}},
		},
	})
	if err != nil {
		t.Fatalf("seed relationless Fact merge: %v", err)
	}
	_, err = service.Apply(ctx, createdProject.ID, blackboardv2.ChangeBatch{
		Schema: "semantic-change-batch/v2", IdempotencyKey: "merge-relationless-fact",
		Changes: []blackboardv2.Change{{Op: "merge", Source: "fact:relationless-source", SourceVersion: 1, Canonical: "fact:relationless-canonical", CanonicalVersion: 1}},
	})
	if err != nil {
		t.Fatalf("merge relationless Fact: %v", err)
	}
	history, err := service.ReadHistory(ctx, createdProject.ID, "fact:relationless-source", blackboardv2.HistoryOptions{})
	if err != nil {
		t.Fatalf("read relationless merged Fact history: %v", err)
	}
	if history.Key != "fact:relationless-canonical" || len(history.Items) != 1 {
		t.Fatalf("relationless merged Fact history = %#v", history)
	}
	item := history.Items[0]
	if item.Kind != "record" || item.Key != "fact:relationless-source" || item.Record == nil || item.Record.Body != "Original source meaning" {
		t.Fatalf("relationless source identity was not preserved: %#v", item)
	}
}

func TestMergeRedirectPersistsAcrossReopenAndReplaysExactly(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "pentest.db")
	db, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	projects := project.NewService(db)
	createdProject, err := projects.Create("Durable merge redirect", "", project.Scope{}, project.Defaults{})
	if err != nil {
		t.Fatalf("create project: %v", err)
	}
	foreignProject, err := projects.Create("Project-local redirect", "", project.Scope{}, project.Defaults{})
	if err != nil {
		t.Fatalf("create foreign project: %v", err)
	}
	service := blackboardv2.NewService(db)
	_, err = service.Apply(ctx, createdProject.ID, blackboardv2.ChangeBatch{
		Schema: "semantic-change-batch/v2", IdempotencyKey: "seed-durable-merge",
		Changes: []blackboardv2.Change{
			{Op: "create", Key: "fact:old", Type: "fact", Record: blackboardv2.FactRecord{Category: "service", Summary: "Durable merge", Confidence: "tentative", ScopeStatus: "in_scope"}},
			{Op: "create", Key: "fact:canonical", Type: "fact", Record: blackboardv2.FactRecord{Category: "service", Summary: "durable merge", Confidence: "tentative", ScopeStatus: "in_scope"}},
		},
	})
	if err != nil {
		t.Fatalf("seed durable merge: %v", err)
	}
	_, err = service.Apply(ctx, foreignProject.ID, blackboardv2.ChangeBatch{
		Schema: "semantic-change-batch/v2", IdempotencyKey: "seed-project-local-key",
		Changes: []blackboardv2.Change{{Op: "create", Key: "fact:old", Type: "fact", Record: blackboardv2.FactRecord{Category: "service", Summary: "Independent Project meaning", Confidence: "tentative", ScopeStatus: "in_scope"}}},
	})
	if err != nil {
		t.Fatalf("seed foreign same key: %v", err)
	}
	mergeBatch := blackboardv2.ChangeBatch{
		Schema: "semantic-change-batch/v2", IdempotencyKey: "durable-merge",
		Changes: []blackboardv2.Change{{Op: "merge", Source: "fact:old", SourceVersion: 1, Canonical: "fact:canonical", CanonicalVersion: 1}},
	}
	first, err := service.Apply(ctx, createdProject.ID, mergeBatch)
	if err != nil {
		t.Fatalf("apply durable merge: %v", err)
	}
	replay, err := service.Apply(ctx, createdProject.ID, mergeBatch)
	if err != nil {
		t.Fatalf("replay durable merge: %v", err)
	}
	if !reflect.DeepEqual(replay, first) {
		t.Fatalf("merge replay = %#v, want %#v", replay, first)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close store before reopen: %v", err)
	}
	db, err = store.Open(dbPath)
	if err != nil {
		t.Fatalf("reopen store: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	service = blackboardv2.NewService(db)
	detail, err := service.ReadCurrent(ctx, createdProject.ID, "fact:old")
	if err != nil {
		t.Fatalf("read durable redirect after reopen: %v", err)
	}
	if detail.Key != "fact:canonical" || detail.Version != 1 {
		t.Fatalf("durable redirected detail = %#v", detail)
	}
	foreign, err := service.ReadCurrent(ctx, foreignProject.ID, "fact:old")
	if err != nil {
		t.Fatalf("read project-local same key: %v", err)
	}
	if foreign.Key != "fact:old" || foreign.Record.Summary != "Independent Project meaning" {
		t.Fatalf("redirect crossed Projects: %#v", foreign)
	}
	_, err = service.Apply(ctx, createdProject.ID, blackboardv2.ChangeBatch{
		Schema: "semantic-change-batch/v2", IdempotencyKey: "reject-shadow-redirect",
		Changes: []blackboardv2.Change{{Op: "create", Key: "fact:old", Type: "fact", Record: blackboardv2.FactRecord{Category: "service", Summary: "Must not shadow redirect", Confidence: "tentative", ScopeStatus: "in_scope"}}},
	})
	assertMergeErrorCode(t, err, "key_conflict")
}

func TestConcurrentMergeAttemptsProduceOneCanonicalRecord(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(filepath.Join(t.TempDir(), "pentest.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	createdProject, err := project.NewService(db).Create("Concurrent merge", "", project.Scope{}, project.Defaults{})
	if err != nil {
		t.Fatalf("create project: %v", err)
	}
	service := blackboardv2.NewService(db)
	seed, err := service.Apply(ctx, createdProject.ID, blackboardv2.ChangeBatch{
		Schema: "semantic-change-batch/v2", IdempotencyKey: "seed-concurrent-merge",
		Changes: []blackboardv2.Change{
			{Op: "create", Key: "entity:concurrent-source", Type: "entity", Record: blackboardv2.EntityRecord{Status: "active", Kind: "host", Name: "Source", Locator: "concurrent.example", ScopeStatus: "in_scope"}},
			{Op: "create", Key: "entity:concurrent-canonical", Type: "entity", Record: blackboardv2.EntityRecord{Status: "active", Kind: "host", Name: "Canonical", Locator: "CONCURRENT.EXAMPLE.", ScopeStatus: "in_scope"}},
		},
	})
	if err != nil {
		t.Fatalf("seed concurrent merge: %v", err)
	}
	start := make(chan struct{})
	results := make(chan error, 2)
	for _, key := range []string{"concurrent-merge-a", "concurrent-merge-b"} {
		go func(idempotencyKey string) {
			<-start
			_, err := service.Apply(ctx, createdProject.ID, blackboardv2.ChangeBatch{
				Schema: "semantic-change-batch/v2", IdempotencyKey: idempotencyKey,
				Changes: []blackboardv2.Change{{Op: "merge", Source: "entity:concurrent-source", SourceVersion: 1, Canonical: "entity:concurrent-canonical", CanonicalVersion: 1}},
			})
			results <- err
		}(key)
	}
	close(start)
	successes := 0
	for range 2 {
		if err := <-results; err == nil {
			successes++
		}
	}
	if successes != 1 {
		t.Fatalf("concurrent merge successes = %d, want exactly 1", successes)
	}
	snapshot, err := service.RuntimeSnapshot(ctx, createdProject.ID)
	if err != nil {
		t.Fatalf("read snapshot after concurrent merge: %v", err)
	}
	if snapshot.Revision != seed.Revision+1 || len(snapshot.Knowledge.Entities) != 1 || snapshot.Knowledge.Entities["entity:concurrent-source"].Name != "" {
		t.Fatalf("snapshot after concurrent merge = %#v", snapshot)
	}
	detail, err := service.ReadCurrent(ctx, createdProject.ID, "entity:concurrent-source")
	if err != nil || detail.Key != "entity:concurrent-canonical" {
		t.Fatalf("concurrent redirect detail = %#v, err=%v", detail, err)
	}
}
