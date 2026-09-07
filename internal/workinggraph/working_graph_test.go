package workinggraph_test

import (
	"context"
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
