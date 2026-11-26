package blackboardv2_test

import (
	"context"
	"testing"

	"encoding/json"
	"path/filepath"
	"pentest/internal/blackboardv2"
	"pentest/internal/project"
	"pentest/internal/store"
)

func TestSameBatchCreateAndSupersedeInfersReplacementVersion(t *testing.T) {
	var jsonSupersede blackboardv2.Change
	if err := json.Unmarshal([]byte(`{"op":"supersede","replacement":"objective:replacement-json","replaced":"objective:old-json","replaced_version":1}`), &jsonSupersede); err != nil {
		t.Fatalf("decode same-batch supersede without replacement_version: %v", err)
	}
	if jsonSupersede.ReplacementVersion != 0 {
		t.Fatalf("decoded replacement_version = %d, want omitted zero", jsonSupersede.ReplacementVersion)
	}

	ctx := context.Background()
	db, err := store.Open(filepath.Join(t.TempDir(), "pentest.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	createdProject, err := project.NewService(db).Create("Same Batch Supersede", "", project.Scope{}, project.Defaults{})
	if err != nil {
		t.Fatalf("create project: %v", err)
	}
	service := blackboardv2.NewService(db)
	harness := mustHarness(t)
	_, err = service.Apply(ctx, createdProject.ID, blackboardv2.ChangeBatch{
		Schema:         "semantic-change-batch/v2",
		IdempotencyKey: "seed-same-batch-supersede",
		Changes: []blackboardv2.Change{
			{Op: "create", Key: "objective:old-json", Type: "objective", Record: blackboardv2.ObjectiveRecord{Status: "open", Objective: "Old JSON Objective"}},
			{Op: "create", Key: "objective:old-programmatic", Type: "objective", Record: blackboardv2.ObjectiveRecord{Status: "open", Objective: "Old programmatic Objective"}},
			{Op: "create", Key: "objective:existing-replacement", Type: "objective", Record: blackboardv2.ObjectiveRecord{Status: "open", Objective: "Existing replacement"}},
			{Op: "create", Key: "objective:old-existing", Type: "objective", Record: blackboardv2.ObjectiveRecord{Status: "open", Objective: "Old existing Objective"}},
			{Op: "create", Key: "objective:old-stale", Type: "objective", Record: blackboardv2.ObjectiveRecord{Status: "open", Objective: "Old stale Objective"}},
		},
	})
	if err != nil {
		t.Fatalf("seed supersede records: %v", err)
	}

	jsonBatch := blackboardv2.ChangeBatch{
		Schema:         "semantic-change-batch/v2",
		IdempotencyKey: "same-batch-json-supersede",
		Changes: []blackboardv2.Change{
			{Op: "create", Key: "objective:replacement-json", Type: "objective", Record: blackboardv2.ObjectiveRecord{Status: "open", Objective: "Replacement JSON Objective"}},
			jsonSupersede,
		},
	}
	assertContractJSON(t, harness, "changeBatch", jsonBatch)
	jsonResult, err := service.Apply(ctx, createdProject.ID, jsonBatch)
	if err != nil {
		t.Fatalf("apply JSON same-batch supersede: %v", err)
	}
	assertChangeRecords(t, jsonResult, 7, [][]any{{"objective:old-json", float64(2)}, {"objective:replacement-json", float64(1)}})
	assertRelationResult(t, jsonResult, [][]any{{"objective:replacement-json", "supersedes", "objective:old-json", float64(1)}})

	programmaticResult, err := service.Apply(ctx, createdProject.ID, blackboardv2.ChangeBatch{
		Schema:         "semantic-change-batch/v2",
		IdempotencyKey: "same-batch-programmatic-supersede",
		Changes: []blackboardv2.Change{
			{Op: "create", Key: "objective:replacement-programmatic", Type: "objective", Record: blackboardv2.ObjectiveRecord{Status: "open", Objective: "Replacement programmatic Objective"}},
			{Op: "supersede", Replacement: "objective:replacement-programmatic", Replaced: "objective:old-programmatic", ReplacedVersion: 1},
		},
	})
	if err != nil {
		t.Fatalf("apply programmatic same-batch supersede: %v", err)
	}
	assertChangeRecords(t, programmaticResult, 9, [][]any{{"objective:old-programmatic", float64(2)}, {"objective:replacement-programmatic", float64(1)}})

	_, err = service.Apply(ctx, createdProject.ID, blackboardv2.ChangeBatch{
		Schema:         "semantic-change-batch/v2",
		IdempotencyKey: "reject-omitted-preexisting-replacement-version",
		Changes: []blackboardv2.Change{
			{Op: "create", Key: "objective:atomic-marker", Type: "objective", Record: blackboardv2.ObjectiveRecord{Status: "open", Objective: "Must roll back"}},
			{Op: "supersede", Replacement: "objective:existing-replacement", Replaced: "objective:old-existing", ReplacedVersion: 1},
		},
	})
	if !isSemanticCode(err, "semantic_validation") {
		t.Fatalf("pre-existing omitted replacement_version error = %#v, want semantic_validation", err)
	}
	if _, err := service.ReadCurrent(ctx, createdProject.ID, "objective:atomic-marker"); !isSemanticCode(err, "not_found") {
		t.Fatalf("failed omitted-version batch retained atomic marker: %#v", err)
	}

	_, err = service.Apply(ctx, createdProject.ID, blackboardv2.ChangeBatch{
		Schema:         "semantic-change-batch/v2",
		IdempotencyKey: "rollback-created-replacement-on-stale-replaced",
		Changes: []blackboardv2.Change{
			{Op: "create", Key: "objective:replacement-stale", Type: "objective", Record: blackboardv2.ObjectiveRecord{Status: "open", Objective: "Must roll back after stale replaced version"}},
			{Op: "supersede", Replacement: "objective:replacement-stale", Replaced: "objective:old-stale", ReplacedVersion: 99},
		},
	})
	if !isSemanticCode(err, "version_conflict") {
		t.Fatalf("stale same-batch supersede error = %#v, want version_conflict", err)
	}
	if _, err := service.ReadCurrent(ctx, createdProject.ID, "objective:replacement-stale"); !isSemanticCode(err, "not_found") {
		t.Fatalf("stale same-batch supersede retained replacement: %#v", err)
	}
}
