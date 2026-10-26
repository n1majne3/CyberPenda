package blackboardv2_test

import (
	"context"
	"os"
	"path/filepath"
	"pentest/internal/blackboardv2"
	"pentest/internal/project"
	"reflect"
	"testing"
)

// Warm health reads must retain the same integrity contract as the first read.
func TestSemanticHealthWarmEvidenceCacheDetectsPayloadChanges(t *testing.T) {
	for _, mutation := range []string{"same_size_restored_mtime", "replacement", "missing"} {
		t.Run(mutation, func(t *testing.T) {
			fixture := newHealthFixture(t, project.KindPentest)
			ctx := context.Background()
			_, err := fixture.service.ApplyForContinuation(ctx, fixture.projectID, fixture.continuationID, blackboardv2.ChangeBatch{
				Schema: "semantic-change-batch/v2", IdempotencyKey: "seed-health-cache",
				Changes: []blackboardv2.Change{
					{Op: "create", Key: "objective:cache", Type: "objective", Record: blackboardv2.ObjectiveRecord{Status: "open", Objective: "Check retained Evidence"}},
					{Op: "create", Key: "attempt:cache", Type: "attempt", Record: blackboardv2.AttemptRecord{Status: "open", Summary: "Collect retained Evidence"}},
					{Op: "relate", From: "attempt:cache", Relation: "tests", To: "objective:cache"},
				},
			})
			if err != nil {
				t.Fatal(err)
			}
			source := filepath.Join(fixture.workdir, "health-cache.txt")
			if err := os.WriteFile(source, []byte("trusted\n"), 0600); err != nil {
				t.Fatal(err)
			}
			_, err = fixture.service.RetainEvidenceForContinuation(ctx, fixture.projectID, fixture.continuationID, blackboardv2.RetainEvidenceRequest{
				IdempotencyKey: "retain-health-cache", Key: "evidence:cache", Attempt: "attempt:cache", SourcePath: "health-cache.txt", ArtifactType: "text", Summary: "Health cache payload",
			})
			if err != nil {
				t.Fatal(err)
			}

			{
				// Simulate host-side corruption of a file that remains writable.
				files := retainedEvidenceFiles(t, fixture.runtimeRoot)
				if len(files) != 1 {
					t.Fatalf("managed payload files = %v", files)
				}
				if err := os.Chmod(files[0], 0600); err != nil {
					t.Fatal(err)
				}
			}
			first, err := fixture.service.ProjectSemanticHealth(ctx, fixture.projectID)
			if err != nil {
				t.Fatal(err)
			}
			assertNoAnomalyCode(t, first, "evidence_integrity")
			for i := 0; i < 3; i++ {
				next, err := fixture.service.ProjectSemanticHealth(ctx, fixture.projectID)
				if err != nil {
					t.Fatal(err)
				}
				if !reflect.DeepEqual(first, next) {
					t.Fatal("unchanged Evidence changed the health projection")
				}
			}
			files := retainedEvidenceFiles(t, fixture.runtimeRoot)
			if len(files) != 1 {
				t.Fatalf("managed payload files = %v", files)
			}
			path := files[0]
			before, err := os.Stat(path)
			if err != nil {
				t.Fatal(err)
			}
			switch mutation {
			case "same_size_restored_mtime":
				if err := os.WriteFile(path, []byte("changed\n"), 0600); err != nil {
					t.Fatal(err)
				}
				if err := os.Chtimes(path, before.ModTime(), before.ModTime()); err != nil {
					t.Fatal(err)
				}
			case "replacement":
				replacement := filepath.Join(t.TempDir(), "replacement")
				if err := os.WriteFile(replacement, []byte("changed\n"), 0600); err != nil {
					t.Fatal(err)
				}
				if err := os.Rename(replacement, path); err != nil {
					t.Fatal(err)
				}
			case "missing":
				if err := os.Remove(path); err != nil {
					t.Fatal(err)
				}
			}
			health, err := fixture.service.ProjectSemanticHealth(ctx, fixture.projectID)
			if err != nil {
				t.Fatal(err)
			}
			anomaly := anomalyByCode(t, health, "evidence_integrity")
			if anomaly.SubjectKey != "evidence:cache" {
				t.Fatalf("integrity subject = %s", anomaly.SubjectKey)
			}
		})
	}
}
