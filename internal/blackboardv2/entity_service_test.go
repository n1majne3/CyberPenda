package blackboardv2_test

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"pentest/internal/blackboardv2"
	"pentest/internal/project"
	"pentest/internal/store"
)

func TestEntityTopologyCreateRelateDetailSnapshotAndScopeMemoryEndToEnd(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "pentest.db")
	db, err := store.Open(path)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	projects := project.NewService(db)
	createdProject, err := projects.Create("Entity Topology", "", project.Scope{Domains: []string{"app.example"}, Excluded: []string{"legacy.example"}}, project.Defaults{})
	if err != nil {
		t.Fatalf("create project: %v", err)
	}
	otherProject, err := projects.Create("Other", "", project.Scope{}, project.Defaults{})
	if err != nil {
		t.Fatalf("create other project: %v", err)
	}
	service := blackboardv2.NewService(db)
	harness := mustHarness(t)

	create := blackboardv2.ChangeBatch{
		Schema:         "semantic-change-batch/v2",
		IdempotencyKey: "create-entity-topology",
		Changes: []blackboardv2.Change{
			{
				Op:   "create",
				Key:  "entity:app",
				Type: "entity",
				Record: blackboardv2.EntityRecord{
					Status:        "active",
					Kind:          "host",
					Name:          "app.example",
					Locator:       "https://app.example",
					Description:   "Primary application host",
					ScopeStatus:   "in_scope",
					CredentialRef: "cred:staging-user",
				},
			},
			{
				Op:   "create",
				Key:  "entity:login",
				Type: "entity",
				Record: blackboardv2.EntityRecord{
					Status:      "active",
					Kind:        "endpoint",
					Name:        "POST /login",
					Locator:     "https://app.example/login",
					ScopeStatus: "in_scope",
				},
			},
			{
				Op:   "create",
				Key:  "entity:legacy",
				Type: "entity",
				Record: blackboardv2.EntityRecord{
					Status:      "active",
					Kind:        "host",
					Name:        "legacy.example",
					Locator:     "legacy.example",
					ScopeStatus: "out_of_scope",
				},
			},
			{
				Op:   "create",
				Key:  "fact:login-json",
				Type: "fact",
				Record: blackboardv2.FactRecord{
					Category:    "authentication",
					Summary:     "The login endpoint accepts JSON requests",
					Body:        "Raw response body is retained outside the Snapshot.",
					Confidence:  "tentative",
					ScopeStatus: "in_scope",
				},
			},
			{Op: "relate", From: "fact:login-json", Relation: "about", To: "entity:login"},
			{Op: "relate", From: "entity:login", Relation: "part_of", To: "entity:app"},
		},
	}
	createResult, err := service.Apply(ctx, createdProject.ID, create)
	if err != nil {
		t.Fatalf("create entity topology: %v", err)
	}
	assertChangeRecords(t, createResult, 6, [][]any{{"entity:app", float64(1)}, {"entity:legacy", float64(1)}, {"entity:login", float64(1)}, {"fact:login-json", float64(1)}})
	assertRelationResult(t, createResult, [][]any{{"entity:login", "part_of", "entity:app", float64(1)}, {"fact:login-json", "about", "entity:login", float64(1)}})
	assertContractJSON(t, harness, "changeResult", createResult)

	if _, err := service.Apply(ctx, otherProject.ID, blackboardv2.ChangeBatch{
		Schema:         "semantic-change-batch/v2",
		IdempotencyKey: "cross-project-about",
		Changes: []blackboardv2.Change{
			{
				Op:   "create",
				Key:  "fact:other",
				Type: "fact",
				Record: blackboardv2.FactRecord{
					Category: "asset", Summary: "Other project fact", Confidence: "tentative", ScopeStatus: "unknown",
				},
			},
			{Op: "relate", From: "fact:other", Relation: "about", To: "entity:login"},
		},
	}); !isSemanticCode(err, "not_found") {
		t.Fatalf("cross-project relationship error = %#v, want not_found", err)
	}
	if _, err := service.ReadCurrent(ctx, otherProject.ID, "fact:other"); !isSemanticCode(err, "not_found") {
		t.Fatalf("cross-project failed batch created a record: %#v", err)
	}

	detail, err := service.ReadCurrent(ctx, createdProject.ID, "entity:login")
	if err != nil {
		t.Fatalf("read entity detail: %v", err)
	}
	if detail.Type != "entity" || detail.Record.Kind != "endpoint" || detail.Record.Name != "POST /login" || detail.Record.ScopeStatus != "in_scope" {
		t.Fatalf("entity detail = %#v", detail)
	}
	if got := mustTupleJSON(t, detail.Relationships); !reflect.DeepEqual(got, [][]any{{"entity:login", "part_of", "entity:app"}, {"fact:login-json", "about", "entity:login"}}) {
		t.Fatalf("entity detail relationships = %#v", got)
	}
	assertContractJSON(t, harness, "currentDetail", detail)

	snapshot, err := service.RuntimeSnapshot(ctx, createdProject.ID)
	if err != nil {
		t.Fatalf("runtime snapshot: %v", err)
	}
	assertContractJSON(t, harness, "runtimeSnapshot", snapshot)
	snapshotJSON := mustJSON(t, snapshot)
	wantSnapshot := `{"schema":"runtime-blackboard/v2","semantics":"work is active; knowledge is current; history and details are available by key","revision":6,"work":{},"knowledge":{"entities":{"entity:app":{"version":1,"status":"active","kind":"host","name":"app.example","locator":"https://app.example","description":"Primary application host","scope_status":"in_scope","credential_ref":"cred:staging-user"},"entity:legacy":{"version":1,"status":"active","kind":"host","name":"legacy.example","locator":"legacy.example","scope_status":"out_of_scope"},"entity:login":{"version":1,"status":"active","kind":"endpoint","name":"POST /login","locator":"https://app.example/login","scope_status":"in_scope"}},"facts":{"fact:login-json":{"version":1,"category":"authentication","summary":"The login endpoint accepts JSON requests","confidence":"tentative","scope_status":"in_scope"}}},"relations":[["entity:login","part_of","entity:app"],["fact:login-json","about","entity:login"]]}`
	if string(snapshotJSON) != wantSnapshot {
		t.Fatalf("snapshot JSON = %s, want %s", snapshotJSON, wantSnapshot)
	}
	for _, forbidden := range []string{"Raw response body", "body", "project_id", "trusted", "audit", "hash", "internal"} {
		if strings.Contains(string(snapshotJSON), forbidden) {
			t.Fatalf("snapshot leaked forbidden field/content %q: %s", forbidden, snapshotJSON)
		}
	}

	fetchedProject, err := projects.Get(createdProject.ID)
	if err != nil {
		t.Fatalf("reload project: %v", err)
	}
	if !reflect.DeepEqual(fetchedProject.Scope, createdProject.Scope) {
		t.Fatalf("Entity scope_status mutated authoritative Scope: %#v, want %#v", fetchedProject.Scope, createdProject.Scope)
	}
}

func TestEntitySemanticGuardsAreAtomic(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "pentest.db")
	db, err := store.Open(path)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	projects := project.NewService(db)
	createdProject, err := projects.Create("Entity Guards", "", project.Scope{}, project.Defaults{})
	if err != nil {
		t.Fatalf("create project: %v", err)
	}
	service := blackboardv2.NewService(db)

	seed := blackboardv2.ChangeBatch{
		Schema:         "semantic-change-batch/v2",
		IdempotencyKey: "seed-entity-guards",
		Changes: []blackboardv2.Change{
			{Op: "create", Key: "entity:app", Type: "entity", Record: blackboardv2.EntityRecord{Status: "active", Kind: "host", Name: "app.example", ScopeStatus: "in_scope"}},
			{Op: "create", Key: "entity:login", Type: "entity", Record: blackboardv2.EntityRecord{Status: "active", Kind: "endpoint", Name: "POST /login", ScopeStatus: "in_scope"}},
			{Op: "create", Key: "fact:login", Type: "fact", Record: blackboardv2.FactRecord{Category: "authentication", Summary: "Login accepts JSON", Confidence: "tentative", ScopeStatus: "in_scope"}},
		},
	}
	if _, err := service.Apply(ctx, createdProject.ID, seed); err != nil {
		t.Fatalf("seed topology: %v", err)
	}

	tests := []struct {
		name    string
		batch   blackboardv2.ChangeBatch
		code    string
		missing string
	}{
		{
			name: "cross-type key collision",
			batch: blackboardv2.ChangeBatch{Schema: "semantic-change-batch/v2", IdempotencyKey: "entity-collides-with-fact", Changes: []blackboardv2.Change{
				{Op: "create", Key: "entity:fresh", Type: "entity", Record: blackboardv2.EntityRecord{Status: "active", Kind: "host", Name: "fresh.example", ScopeStatus: "unknown"}},
				{Op: "create", Key: "fact:login", Type: "entity", Record: blackboardv2.EntityRecord{Status: "active", Kind: "host", Name: "collision.example", ScopeStatus: "unknown"}},
			}},
			code:    "key_conflict",
			missing: "entity:fresh",
		},
		{
			name: "about requires entity target",
			batch: blackboardv2.ChangeBatch{Schema: "semantic-change-batch/v2", IdempotencyKey: "about-wrong-target", Changes: []blackboardv2.Change{
				{Op: "create", Key: "entity:atomic-about", Type: "entity", Record: blackboardv2.EntityRecord{Status: "active", Kind: "host", Name: "atomic.example", ScopeStatus: "unknown"}},
				{Op: "relate", From: "entity:app", Relation: "about", To: "fact:login"},
			}},
			code:    "semantic_validation",
			missing: "entity:atomic-about",
		},
		{
			name: "part_of rejects wrong direction",
			batch: blackboardv2.ChangeBatch{Schema: "semantic-change-batch/v2", IdempotencyKey: "part-of-wrong-direction", Changes: []blackboardv2.Change{
				{Op: "create", Key: "entity:atomic-direction", Type: "entity", Record: blackboardv2.EntityRecord{Status: "active", Kind: "service", Name: "atomic service", ScopeStatus: "unknown"}},
				{Op: "relate", From: "fact:login", Relation: "part_of", To: "entity:app"},
			}},
			code:    "semantic_validation",
			missing: "entity:atomic-direction",
		},
		{
			name: "part_of rejects self-link",
			batch: blackboardv2.ChangeBatch{Schema: "semantic-change-batch/v2", IdempotencyKey: "part-of-self-link", Changes: []blackboardv2.Change{
				{Op: "create", Key: "entity:atomic-self", Type: "entity", Record: blackboardv2.EntityRecord{Status: "active", Kind: "service", Name: "atomic self", ScopeStatus: "unknown"}},
				{Op: "relate", From: "entity:app", Relation: "part_of", To: "entity:app"},
			}},
			code:    "semantic_validation",
			missing: "entity:atomic-self",
		},
		{
			name: "part_of rejects containment cycle",
			batch: blackboardv2.ChangeBatch{Schema: "semantic-change-batch/v2", IdempotencyKey: "part-of-cycle", Changes: []blackboardv2.Change{
				{Op: "relate", From: "entity:login", Relation: "part_of", To: "entity:app"},
				{Op: "relate", From: "entity:app", Relation: "part_of", To: "entity:login"},
			}},
			code:    "semantic_validation",
			missing: "",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := service.Apply(ctx, createdProject.ID, tt.batch)
			if !isSemanticCode(err, tt.code) {
				t.Fatalf("Apply error = %#v, want %s", err, tt.code)
			}
			if tt.missing != "" {
				if _, err := service.ReadCurrent(ctx, createdProject.ID, tt.missing); !isSemanticCode(err, "not_found") {
					t.Fatalf("failed batch created %s: %#v", tt.missing, err)
				}
			}
		})
	}
}

func TestRetiredEntityLeavesCurrentContextAndRelationships(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "pentest.db")
	db, err := store.Open(path)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	projects := project.NewService(db)
	createdProject, err := projects.Create("Entity Retirement", "", project.Scope{}, project.Defaults{})
	if err != nil {
		t.Fatalf("create project: %v", err)
	}
	service := blackboardv2.NewService(db)
	harness := mustHarness(t)

	_, err = service.Apply(ctx, createdProject.ID, blackboardv2.ChangeBatch{
		Schema:         "semantic-change-batch/v2",
		IdempotencyKey: "seed-retired-entity",
		Changes: []blackboardv2.Change{
			{Op: "create", Key: "entity:login", Type: "entity", Record: blackboardv2.EntityRecord{Status: "active", Kind: "endpoint", Name: "POST /login", ScopeStatus: "in_scope"}},
			{Op: "create", Key: "fact:login", Type: "fact", Record: blackboardv2.FactRecord{Category: "authentication", Summary: "Login accepts JSON", Confidence: "tentative", ScopeStatus: "in_scope"}},
			{Op: "relate", From: "fact:login", Relation: "about", To: "entity:login"},
		},
	})
	if err != nil {
		t.Fatalf("seed entity: %v", err)
	}

	retired, err := service.Apply(ctx, createdProject.ID, blackboardv2.ChangeBatch{
		Schema:         "semantic-change-batch/v2",
		IdempotencyKey: "retire-login-entity",
		Changes: []blackboardv2.Change{{
			Op:                "transition",
			Key:               "entity:login",
			Version:           1,
			Status:            "retired",
			ResolutionSummary: "The login endpoint was removed from the current deployment",
		}},
	})
	if err != nil {
		t.Fatalf("retire entity: %v", err)
	}
	assertChangeRecords(t, retired, 4, [][]any{{"entity:login", float64(2)}})
	if len(retired.Relations) != 0 {
		t.Fatalf("retirement relation result = %#v, want no current relations", retired.Relations)
	}

	if _, err := service.ReadCurrent(ctx, createdProject.ID, "entity:login"); !isSemanticCode(err, "not_found") {
		t.Fatalf("retired entity current detail error = %#v, want not_found", err)
	}
	factDetail, err := service.ReadCurrent(ctx, createdProject.ID, "fact:login")
	if err != nil {
		t.Fatalf("read remaining fact: %v", err)
	}
	if got := mustTupleJSON(t, factDetail.Relationships); len(got) != 0 {
		t.Fatalf("fact relationships after Entity retirement = %#v, want none", got)
	}

	history, err := service.ReadHistory(ctx, createdProject.ID, "entity:login", blackboardv2.HistoryOptions{})
	if err != nil {
		t.Fatalf("read retired entity history: %v", err)
	}
	if len(history.Items) != 3 || history.Items[0].Version != 1 || history.Items[0].Record.Status != "active" || history.Items[1].Version != 2 || history.Items[1].Record.Status != "retired" {
		t.Fatalf("retired entity history = %#v", history.Items)
	}
	if history.Items[2].Kind != "relationship" || history.Items[2].From != "fact:login" || history.Items[2].Relation != "about" || history.Items[2].To != "entity:login" {
		t.Fatalf("retired Entity relationship history = %#v", history.Items[2])
	}
	assertContractJSON(t, harness, "semanticHistory", history)

	snapshot, err := service.RuntimeSnapshot(ctx, createdProject.ID)
	if err != nil {
		t.Fatalf("runtime snapshot: %v", err)
	}
	snapshotJSON := string(mustJSON(t, snapshot))
	wantSnapshot := `{"schema":"runtime-blackboard/v2","semantics":"work is active; knowledge is current; history and details are available by key","revision":4,"work":{},"knowledge":{"facts":{"fact:login":{"version":1,"category":"authentication","summary":"Login accepts JSON","confidence":"tentative","scope_status":"in_scope"}}},"relations":[]}`
	if snapshotJSON != wantSnapshot {
		t.Fatalf("snapshot after Entity retirement = %s, want %s", snapshotJSON, wantSnapshot)
	}

	_, err = service.Apply(ctx, createdProject.ID, blackboardv2.ChangeBatch{
		Schema:         "semantic-change-batch/v2",
		IdempotencyKey: "reject-retired-key-reuse",
		Changes: []blackboardv2.Change{
			{Op: "create", Key: "entity:fresh", Type: "entity", Record: blackboardv2.EntityRecord{Status: "active", Kind: "host", Name: "fresh.example", ScopeStatus: "unknown"}},
			{Op: "create", Key: "entity:login", Type: "fact", Record: blackboardv2.FactRecord{Category: "asset", Summary: "Reused terminal Entity key", Confidence: "tentative", ScopeStatus: "unknown"}},
		},
	})
	if !isSemanticCode(err, "key_conflict") {
		t.Fatalf("retired key reuse error = %#v, want key_conflict", err)
	}
	if _, err := service.ReadCurrent(ctx, createdProject.ID, "entity:fresh"); !isSemanticCode(err, "not_found") {
		t.Fatalf("failed key-reuse batch created entity:fresh: %#v", err)
	}
}

func TestSupersededEntityLeavesCurrentContextWithReplacementMeaning(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "pentest.db")
	db, err := store.Open(path)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	projects := project.NewService(db)
	createdProject, err := projects.Create("Entity Supersession", "", project.Scope{}, project.Defaults{})
	if err != nil {
		t.Fatalf("create project: %v", err)
	}
	service := blackboardv2.NewService(db)
	harness := mustHarness(t)

	_, err = service.Apply(ctx, createdProject.ID, blackboardv2.ChangeBatch{
		Schema:         "semantic-change-batch/v2",
		IdempotencyKey: "seed-superseded-entity",
		Changes: []blackboardv2.Change{
			{Op: "create", Key: "entity:login-v1", Type: "entity", Record: blackboardv2.EntityRecord{Status: "active", Kind: "endpoint", Name: "POST /login", ScopeStatus: "in_scope"}},
			{Op: "create", Key: "entity:login-v2", Type: "entity", Record: blackboardv2.EntityRecord{Status: "active", Kind: "endpoint", Name: "POST /v2/login", ScopeStatus: "in_scope"}},
			{Op: "create", Key: "fact:login", Type: "fact", Record: blackboardv2.FactRecord{Category: "authentication", Summary: "Login accepts JSON", Confidence: "tentative", ScopeStatus: "in_scope"}},
			{Op: "relate", From: "fact:login", Relation: "about", To: "entity:login-v1"},
		},
	})
	if err != nil {
		t.Fatalf("seed entities: %v", err)
	}

	superseded, err := service.Apply(ctx, createdProject.ID, blackboardv2.ChangeBatch{
		Schema:         "semantic-change-batch/v2",
		IdempotencyKey: "supersede-login-entity",
		Changes: []blackboardv2.Change{{
			Op:                 "supersede",
			Replacement:        "entity:login-v2",
			ReplacementVersion: 1,
			Replaced:           "entity:login-v1",
			ReplacedVersion:    1,
		}},
	})
	if err != nil {
		t.Fatalf("supersede entity: %v", err)
	}
	assertChangeRecords(t, superseded, 5, [][]any{{"entity:login-v1", float64(2)}})
	assertRelationResult(t, superseded, [][]any{{"entity:login-v2", "supersedes", "entity:login-v1", float64(1)}})
	assertContractJSON(t, harness, "changeResult", superseded)

	if _, err := service.ReadCurrent(ctx, createdProject.ID, "entity:login-v1"); !isSemanticCode(err, "not_found") {
		t.Fatalf("superseded entity current detail error = %#v, want not_found", err)
	}
	replacement, err := service.ReadCurrent(ctx, createdProject.ID, "entity:login-v2")
	if err != nil {
		t.Fatalf("read replacement entity: %v", err)
	}
	if got := mustTupleJSON(t, replacement.Relationships); len(got) != 0 {
		t.Fatalf("replacement relationships = %#v", got)
	}
	assertContractJSON(t, harness, "currentDetail", replacement)

	factDetail, err := service.ReadCurrent(ctx, createdProject.ID, "fact:login")
	if err != nil {
		t.Fatalf("read remaining fact: %v", err)
	}
	if got := mustTupleJSON(t, factDetail.Relationships); len(got) != 0 {
		t.Fatalf("fact relationships after Entity supersession = %#v, want none", got)
	}

	history, err := service.ReadHistory(ctx, createdProject.ID, "entity:login-v1", blackboardv2.HistoryOptions{})
	if err != nil {
		t.Fatalf("read superseded entity history: %v", err)
	}
	if len(history.Items) != 4 || history.Items[0].Version != 1 || history.Items[0].Record.Status != "active" || history.Items[1].Version != 2 || history.Items[1].Record.Status != "superseded" {
		t.Fatalf("superseded entity history = %#v", history.Items)
	}
	if history.Items[2].Kind != "relationship" || history.Items[2].From != "fact:login" || history.Items[2].Relation != "about" || history.Items[2].To != "entity:login-v1" {
		t.Fatalf("retired about relationship history = %#v", history.Items[2])
	}
	if history.Items[3].Kind != "relationship" || history.Items[3].From != "entity:login-v2" || history.Items[3].Relation != "supersedes" || history.Items[3].To != "entity:login-v1" {
		t.Fatalf("supersedes relationship history = %#v", history.Items[3])
	}
	assertContractJSON(t, harness, "semanticHistory", history)

	snapshot, err := service.RuntimeSnapshot(ctx, createdProject.ID)
	if err != nil {
		t.Fatalf("runtime snapshot: %v", err)
	}
	snapshotJSON := string(mustJSON(t, snapshot))
	wantSnapshot := `{"schema":"runtime-blackboard/v2","semantics":"work is active; knowledge is current; history and details are available by key","revision":5,"work":{},"knowledge":{"entities":{"entity:login-v2":{"version":1,"status":"active","kind":"endpoint","name":"POST /v2/login","scope_status":"in_scope"}},"facts":{"fact:login":{"version":1,"category":"authentication","summary":"Login accepts JSON","confidence":"tentative","scope_status":"in_scope"}}},"relations":[]}`
	if snapshotJSON != wantSnapshot {
		t.Fatalf("snapshot after Entity supersession = %s, want %s", snapshotJSON, wantSnapshot)
	}
	assertContractJSON(t, harness, "runtimeSnapshot", snapshot)
}

func TestEntityClosedShapeRejectsSecretsUnknownFieldsAndInvalidLocators(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "pentest.db")
	db, err := store.Open(path)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	projects := project.NewService(db)
	createdProject, err := projects.Create("Entity Shape", "", project.Scope{}, project.Defaults{})
	if err != nil {
		t.Fatalf("create project: %v", err)
	}
	service := blackboardv2.NewService(db)

	for _, tt := range []struct {
		name   string
		record blackboardv2.EntityRecord
		path   string
	}{
		{
			name:   "unknown kind",
			record: blackboardv2.EntityRecord{Status: "active", Kind: "container", Name: "web", ScopeStatus: "unknown"},
			path:   "changes[0].record.kind",
		},
		{
			name:   "secret locator",
			record: blackboardv2.EntityRecord{Status: "active", Kind: "host", Name: "app", Locator: "https://user:pass@app.example", ScopeStatus: "unknown"},
			path:   "changes[0].record.locator",
		},
		{
			name:   "invalid url locator",
			record: blackboardv2.EntityRecord{Status: "active", Kind: "endpoint", Name: "bad", Locator: "https:// app.example/login", ScopeStatus: "unknown"},
			path:   "changes[0].record.locator",
		},
		{
			name:   "secret credential ref",
			record: blackboardv2.EntityRecord{Status: "active", Kind: "identity", Name: "admin", ScopeStatus: "unknown", CredentialRef: "sk-live-secret"},
			path:   "changes[0].record.credential_ref",
		},
		{
			name:   "secret name",
			record: blackboardv2.EntityRecord{Status: "active", Kind: "identity", Name: "password=hunter2", ScopeStatus: "unknown"},
			path:   "changes[0].record.name",
		},
		{
			name:   "secret description",
			record: blackboardv2.EntityRecord{Status: "active", Kind: "service", Name: "Admin API", Description: "token=live-value", ScopeStatus: "unknown"},
			path:   "changes[0].record.description",
		},
		{
			name:   "http locator without host",
			record: blackboardv2.EntityRecord{Status: "active", Kind: "endpoint", Name: "bad", Locator: "https:app.example/login", ScopeStatus: "unknown"},
			path:   "changes[0].record.locator",
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			_, err := service.Apply(ctx, createdProject.ID, blackboardv2.ChangeBatch{
				Schema:         "semantic-change-batch/v2",
				IdempotencyKey: "reject-" + strings.ReplaceAll(tt.name, " ", "-"),
				Changes: []blackboardv2.Change{{
					Op:     "create",
					Key:    "entity:" + strings.ReplaceAll(tt.name, " ", "-"),
					Type:   "entity",
					Record: tt.record,
				}},
			})
			var semanticErr *blackboardv2.Error
			if !errors.As(err, &semanticErr) || semanticErr.Code != "semantic_validation" || semanticErr.Path != tt.path {
				t.Fatalf("Apply error = %#v, want semantic_validation on %s", err, tt.path)
			}
		})
	}

	for _, raw := range []string{
		`{"op":"create","key":"entity:json","type":"entity","record":{"status":"active","kind":"host","name":"Unknown field","scope_status":"unknown","secret":"should fail"}}`,
		`{"op":"relate","from":"fact:a","relation":"about","to":"entity:a","reason":"forbidden"}`,
	} {
		var change blackboardv2.Change
		if err := json.Unmarshal([]byte(raw), &change); err == nil {
			t.Fatalf("decoded non-closed Entity topology change: %s", raw)
		}
	}
}

func assertRelationResult(t *testing.T, got blackboardv2.ChangeResult, wantRelations [][]any) {
	t.Helper()
	gotRelations := mustTupleJSON(t, got.Relations)
	if !reflect.DeepEqual(gotRelations, wantRelations) {
		t.Fatalf("relations = %#v, want %#v", gotRelations, wantRelations)
	}
}

func assertChangeRecords(t *testing.T, got blackboardv2.ChangeResult, wantRevision int, wantRecords [][]any) {
	t.Helper()
	if got.Schema != "semantic-change-result/v2" || got.Revision != wantRevision || got.WorkingSnapshot.Path != ".pentest/blackboard.json" || got.WorkingSnapshot.Revision != wantRevision {
		t.Fatalf("change result = %#v, want revision %d and working snapshot", got, wantRevision)
	}
	gotRecords := mustTupleJSON(t, got.Records)
	if !reflect.DeepEqual(gotRecords, wantRecords) {
		t.Fatalf("records = %#v, want %#v", gotRecords, wantRecords)
	}
}

func isSemanticCode(err error, code string) bool {
	var semanticErr *blackboardv2.Error
	return errors.As(err, &semanticErr) && semanticErr.Code == code
}
