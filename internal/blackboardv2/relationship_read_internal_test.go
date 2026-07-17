package blackboardv2

import (
	"context"
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
