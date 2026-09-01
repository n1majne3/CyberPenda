package workinggraph_test

import (
	"context"
	"path/filepath"
	"testing"

	"pentest/internal/owner"
	"pentest/internal/store"
	"pentest/internal/workinggraph"
)

func TestClaimIntentIsOwnerScopedAndIdempotent(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "pentest.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	service := workinggraph.NewService(db)
	contract := owner.NewTaskContract("task-1", "project-1", "/tmp/task-1")
	intent := workinggraph.Intent{Schema: workinggraph.IntentSchema, ID: "intent_00000001", Kind: workinggraph.IntentSemanticChanges, Payload: map[string]any{"changes": []any{}}}
	first, created, err := service.Claim(context.Background(), contract, "continuation-1", 1, intent)
	if err != nil || !created || first.State != workinggraph.IntentStatePending {
		t.Fatalf("first claim = %#v created=%v err=%v", first, created, err)
	}
	replay, created, err := service.Claim(context.Background(), contract, "continuation-1", 1, intent)
	if err != nil || created || replay.ID != first.ID || replay.RequestHash != first.RequestHash {
		t.Fatalf("replay claim = %#v created=%v err=%v", replay, created, err)
	}
	intent.Payload = map[string]any{"changes": []any{"different"}}
	if _, _, err := service.Claim(context.Background(), contract, "continuation-1", 1, intent); err == nil {
		t.Fatal("same intent id with different content was accepted")
	}
}
