package workinggraph_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"pentest/internal/owner"
	"pentest/internal/workinggraph"
)

func TestPrepareCreatesOwnerAndContinuationScopedLayout(t *testing.T) {
	workdir := t.TempDir()
	service := workinggraph.NewService()
	projection, err := service.Prepare(context.Background(), workinggraph.OwnerContext{
		Owner: owner.NewTaskContract("task-1", "project-1", workdir), ContinuationID: "continuation-2", Workdir: workdir,
	})
	if err != nil {
		t.Fatal(err)
	}
	if projection.Root != workdir || projection.Outbox != filepath.Join(workdir, "graph", "outbox", "continuation-2") ||
		projection.Receipts != filepath.Join(workdir, "graph", "receipts", "continuation-2") {
		t.Fatalf("projection = %#v", projection)
	}
	for _, path := range []string{
		filepath.Join(workdir, "state.md"), filepath.Join(workdir, "graph", "steps.yaml"), filepath.Join(workdir, "graph", "goals.yaml"),
		filepath.Join(workdir, "graph", "facts"), filepath.Join(workdir, "graph", "data"), projection.Outbox, projection.Receipts,
	} {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("missing Working Graph path %s: %v", path, err)
		}
	}
}

func TestEmitWritesMonotonicAtomicIntent(t *testing.T) {
	outbox := filepath.Join(t.TempDir(), "outbox")
	if err := os.MkdirAll(outbox, 0o700); err != nil {
		t.Fatal(err)
	}
	request := workinggraph.IntentInput{Kind: workinggraph.IntentSemanticChanges, SourceFacts: []string{"fact_0007"}, Payload: map[string]any{"changes": []any{}}}
	first, err := workinggraph.Emit(outbox, workinggraph.OwnerKindTask, request)
	if err != nil {
		t.Fatal(err)
	}
	second, err := workinggraph.Emit(outbox, workinggraph.OwnerKindTask, request)
	if err != nil {
		t.Fatal(err)
	}
	if first.ID != "intent_00000001" || second.ID != "intent_00000002" {
		t.Fatalf("intent ids = %q, %q", first.ID, second.ID)
	}
	raw, err := os.ReadFile(filepath.Join(outbox, first.ID+".json"))
	if err != nil {
		t.Fatal(err)
	}
	var stored workinggraph.Intent
	if err := json.Unmarshal(raw, &stored); err != nil {
		t.Fatal(err)
	}
	if stored.Schema != "working-graph-intent/v1" || stored.ID != first.ID || stored.Kind != workinggraph.IntentSemanticChanges {
		t.Fatalf("stored intent = %#v", stored)
	}
	matches, err := filepath.Glob(filepath.Join(outbox, "*.tmp"))
	if err != nil || len(matches) != 0 {
		t.Fatalf("temporary files = %#v, err=%v", matches, err)
	}
}

func TestEmitRejectsSessionEvidenceRetention(t *testing.T) {
	outbox := filepath.Join(t.TempDir(), "outbox")
	if err := os.MkdirAll(outbox, 0o700); err != nil {
		t.Fatal(err)
	}
	_, err := workinggraph.Emit(outbox, workinggraph.OwnerKindSession, workinggraph.IntentInput{
		Kind: workinggraph.IntentRetainEvidence, Payload: map[string]any{"source_path": "proof.txt"},
	})
	if err == nil {
		t.Fatal("Session retain_evidence intent was accepted")
	}
}
