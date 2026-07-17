package blackboardv2

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"pentest/internal/project"
	"pentest/internal/store"
)

func TestSnapshotAndDetailRejectPersistedRelationshipsOutsideSharedGrammar(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "pentest.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	createdProject, err := project.NewService(db).Create("Malformed relationship read", "", project.Scope{}, project.Defaults{})
	if err != nil {
		t.Fatalf("create Project: %v", err)
	}
	service := NewService(db)
	_, err = service.Apply(context.Background(), createdProject.ID, ChangeBatch{
		Schema: "semantic-change-batch/v2", IdempotencyKey: "seed-malformed-relationship-read",
		Changes: []Change{
			{Op: "create", Key: "entity:invalid-source", Type: "entity", Record: EntityRecord{Status: "active", Kind: "service", Name: "Invalid source", ScopeStatus: "in_scope"}},
			{Op: "create", Key: "fact:invalid-target", Type: "fact", Record: FactRecord{Category: "test", Summary: "Invalid target", Confidence: "tentative", ScopeStatus: "unknown"}},
		},
	})
	if err != nil {
		t.Fatalf("seed malformed relationship endpoints: %v", err)
	}
	if _, err := db.Exec(`
		INSERT INTO blackboard_v2_relationships(project_id,from_key,relation,to_key,version,reason,created_at,updated_at)
		VALUES(?, 'entity:invalid-source', 'about', 'fact:invalid-target', 1, '', '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z')`, createdProject.ID); err != nil {
		t.Fatalf("inject malformed persisted relationship: %v", err)
	}

	if _, err := service.RuntimeSnapshot(context.Background(), createdProject.ID); err == nil || !strings.Contains(err.Error(), "outside Blackboard v2 relationship grammar") {
		t.Fatalf("Snapshot malformed relationship error = %#v", err)
	}
	if _, err := service.ReadCurrent(context.Background(), createdProject.ID, "entity:invalid-source"); err == nil || !strings.Contains(err.Error(), "outside Blackboard v2 relationship grammar") {
		t.Fatalf("detail malformed relationship error = %#v", err)
	}
}

func TestSnapshotDetailAndReloadRejectPersistedReasonAndCycleViolations(t *testing.T) {
	for _, testCase := range []struct {
		name      string
		seed      []Change
		persist   func(t *testing.T, db *store.DB, projectID string)
		violation string
	}{
		{
			name: "513-byte reason",
			seed: []Change{
				{Op: "create", Key: "fact:persisted-a", Type: "fact", Record: FactRecord{Category: "test", Summary: "Persisted A", Confidence: "tentative", ScopeStatus: "unknown"}},
				{Op: "create", Key: "fact:persisted-b", Type: "fact", Record: FactRecord{Category: "test", Summary: "Persisted B", Confidence: "tentative", ScopeStatus: "unknown"}},
			},
			persist: func(t *testing.T, db *store.DB, projectID string) {
				t.Helper()
				if _, err := db.Exec(`INSERT INTO blackboard_v2_relationships(project_id,from_key,relation,to_key,version,reason,created_at,updated_at) VALUES(?, 'fact:persisted-a', 'contradicts', 'fact:persisted-b', 1, ?, '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z')`, projectID, strings.Repeat("r", 513)); err != nil {
					t.Fatalf("persist oversized reason: %v", err)
				}
			},
			violation: "reason_too_long",
		},
		{
			name: "cyclic part_of",
			seed: []Change{
				{Op: "create", Key: "entity:persisted-a", Type: "entity", Record: EntityRecord{Status: "active", Kind: "host", Name: "Persisted A", ScopeStatus: "unknown"}},
				{Op: "create", Key: "entity:persisted-b", Type: "entity", Record: EntityRecord{Status: "active", Kind: "host", Name: "Persisted B", ScopeStatus: "unknown"}},
			},
			persist: func(t *testing.T, db *store.DB, projectID string) {
				t.Helper()
				for _, pair := range [][2]string{{"entity:persisted-a", "entity:persisted-b"}, {"entity:persisted-b", "entity:persisted-a"}} {
					if _, err := db.Exec(`INSERT INTO blackboard_v2_relationships(project_id,from_key,relation,to_key,version,reason,created_at,updated_at) VALUES(?, ?, 'part_of', ?, 1, '', '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z')`, projectID, pair[0], pair[1]); err != nil {
						t.Fatalf("persist part_of cycle: %v", err)
					}
				}
			},
			violation: "cycle",
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "pentest.db")
			db, err := store.Open(path)
			if err != nil {
				t.Fatalf("open store: %v", err)
			}
			createdProject, err := project.NewService(db).Create("Persisted relationship violation", "", project.Scope{}, project.Defaults{})
			if err != nil {
				t.Fatalf("create Project: %v", err)
			}
			service := NewService(db)
			if _, err := service.Apply(context.Background(), createdProject.ID, ChangeBatch{Schema: "semantic-change-batch/v2", IdempotencyKey: "seed-" + testCase.name, Changes: testCase.seed}); err != nil {
				t.Fatalf("seed persisted violation: %v", err)
			}
			testCase.persist(t, db, createdProject.ID)
			assertPersistedRelationshipContractError(t, service, createdProject.ID, testCase.seed[0].Key, testCase.violation)
			if err := db.Close(); err != nil {
				t.Fatalf("close persisted violation store: %v", err)
			}
			reopened, err := store.Open(path)
			if err != nil {
				t.Fatalf("reopen persisted violation store: %v", err)
			}
			t.Cleanup(func() { _ = reopened.Close() })
			assertPersistedRelationshipContractError(t, NewService(reopened), createdProject.ID, testCase.seed[0].Key, testCase.violation)
		})
	}
}

func assertPersistedRelationshipContractError(t *testing.T, service *Service, projectID, key, violation string) {
	t.Helper()
	for name, read := range map[string]func() error{
		"snapshot": func() error { _, err := service.RuntimeSnapshot(context.Background(), projectID); return err },
		"detail":   func() error { _, err := service.ReadCurrent(context.Background(), projectID, key); return err },
	} {
		err := read()
		var semanticErr *Error
		if !errors.As(err, &semanticErr) || semanticErr.Code != "semantic_validation" || semanticErr.Path != "relations" || semanticErr.Details["violation"] != violation {
			t.Fatalf("%s persisted relationship error = %#v", name, err)
		}
	}
}
