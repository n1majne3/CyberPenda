package blackboardv2_test

import (
	"context"
	"reflect"
	"testing"

	"pentest/internal/blackboardv2"
)

func TestEvidenceReplacementRollsBackWhenConcurrentMergeChangesIntendedVersion(t *testing.T) {
	ctx := context.Background()
	fixture := newEvidenceV2Fixture(t, "Evidence merge version mismatch")
	fixture.writeSource(t, "mismatch-source.txt", "shared mismatch proof")
	fixture.writeSource(t, "mismatch-canonical.txt", "shared mismatch proof")
	fixture.writeSource(t, "mismatch-replacement.txt", "replacement that must roll back")
	_, err := fixture.service.ApplyForContinuation(ctx, fixture.projectID, fixture.continuationID, blackboardv2.ChangeBatch{
		Schema: "semantic-change-batch/v2", IdempotencyKey: "prepare-evidence-merge-mismatch",
		Changes: []blackboardv2.Change{
			{Op: "create", Key: "attempt:evidence-merge-mismatch", Type: "attempt", Record: blackboardv2.AttemptRecord{Status: "open", Summary: "Exercise merge version mismatch"}},
			{Op: "relate", From: "attempt:evidence-merge-mismatch", Relation: "tests", To: "objective:evidence"},
		},
	})
	if err != nil {
		t.Fatalf("prepare Evidence merge mismatch: %v", err)
	}
	for _, evidence := range []struct{ key, source, idempotency string }{
		{"evidence:mismatch-source", "mismatch-source.txt", "retain-mismatch-source"},
		{"evidence:mismatch-canonical", "mismatch-canonical.txt", "retain-mismatch-canonical"},
	} {
		_, err := fixture.service.RetainEvidenceForContinuation(ctx, fixture.projectID, fixture.continuationID, blackboardv2.RetainEvidenceRequest{
			IdempotencyKey: evidence.idempotency, Key: evidence.key, Attempt: "attempt:evidence", SourcePath: evidence.source, ArtifactType: "text", Summary: "Pre-merge mismatch duplicate",
		})
		if err != nil {
			t.Fatalf("retain %s: %v", evidence.key, err)
		}
	}
	canonicalSummary := "Canonical version two before merge"
	_, err = fixture.service.ApplyForContinuation(ctx, fixture.projectID, fixture.continuationID, blackboardv2.ChangeBatch{
		Schema: "semantic-change-batch/v2", IdempotencyKey: "version-mismatch-canonical",
		Changes: []blackboardv2.Change{{Op: "update", Key: "evidence:mismatch-canonical", Version: 1, Type: "evidence", Record: blackboardv2.EvidencePatch{Summary: &canonicalSummary}}},
	})
	if err != nil {
		t.Fatalf("version canonical Evidence: %v", err)
	}
	before, err := fixture.service.RuntimeSnapshot(ctx, fixture.projectID)
	if err != nil {
		t.Fatalf("read snapshot before mismatch race: %v", err)
	}
	injector := &evidenceMergeRaceInjector{
		service: fixture.service, projectID: fixture.projectID, idempotencyKey: "merge-during-version-mismatch-publication",
		source: "evidence:mismatch-source", sourceVersion: 1, canonical: "evidence:mismatch-canonical", canonicalVersion: 2,
	}
	racingService := blackboardv2.NewServiceWithEvidence(fixture.db, blackboardv2.EvidenceConfig{RuntimeRoot: fixture.runtimeRoot, ArtifactRoot: fixture.runtimeRoot, Failures: injector})
	_, err = racingService.RetainEvidenceForContinuation(ctx, fixture.projectID, fixture.continuationID, blackboardv2.RetainEvidenceRequest{
		IdempotencyKey: "replace-during-version-mismatch", Key: "evidence:mismatch-source", Version: 1, Attempt: "attempt:evidence-merge-mismatch", SourcePath: "mismatch-replacement.txt", ArtifactType: "text", Summary: "Must not commit",
	})
	if !isSemanticCode(err, "version_conflict") {
		t.Fatalf("merge-raced replacement error = %#v, want version_conflict", err)
	}
	if injector.err != nil || !injector.fired {
		t.Fatalf("mismatch merge injector fired=%v err=%v", injector.fired, injector.err)
	}
	after, err := fixture.service.RuntimeSnapshot(ctx, fixture.projectID)
	if err != nil {
		t.Fatalf("read snapshot after mismatch race: %v", err)
	}
	if after.Revision != before.Revision+1 {
		t.Fatalf("mismatch race revision = %d, want merge-only revision %d", after.Revision, before.Revision+1)
	}
	detail, err := fixture.service.ReadCurrent(ctx, fixture.projectID, "evidence:mismatch-source")
	if err != nil {
		t.Fatalf("read canonical after mismatch race: %v", err)
	}
	if detail.Key != "evidence:mismatch-canonical" || detail.Version != 2 || detail.Record.Summary != canonicalSummary {
		t.Fatalf("mismatch replacement partially committed: %#v", detail)
	}
	for _, relationship := range detail.Relationships {
		if relationship[0] == "attempt:evidence-merge-mismatch" && relationship[1] == "produced" {
			t.Fatalf("mismatch replacement retained produced relationship: %#v", detail.Relationships)
		}
	}
}

func TestEmptyEvidenceDetailIncludesZeroSize(t *testing.T) {
	fixture := newEvidenceV2Fixture(t, "Empty Evidence")
	fixture.writeSource(t, "empty.txt", "")
	if _, err := fixture.service.RetainEvidenceForContinuation(context.Background(), fixture.projectID, fixture.continuationID, blackboardv2.RetainEvidenceRequest{
		IdempotencyKey: "retain-empty", Key: "evidence:empty", Attempt: "attempt:evidence",
		SourcePath: "empty.txt", ArtifactType: "text", Summary: "An intentionally empty retained artifact",
	}); err != nil {
		t.Fatalf("retain empty Evidence: %v", err)
	}
	detail, err := fixture.service.ReadCurrent(context.Background(), fixture.projectID, "evidence:empty")
	if err != nil {
		t.Fatalf("read empty Evidence: %v", err)
	}
	if detail.Record.Size != 0 || detail.Record.SHA256 != "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855" {
		t.Fatalf("empty Evidence integrity = %#v", detail.Record)
	}
	assertContractJSON(t, mustHarness(t), "currentDetail", detail)
}

func TestEvidenceSemanticUpdatePreservesServerDerivedIntegrityAndUsesCurrentVersion(t *testing.T) {
	fixture := newEvidenceV2Fixture(t, "Evidence Semantic Update")
	fixture.writeSource(t, "update.txt", "immutable payload\n")
	if _, err := fixture.service.RetainEvidenceForContinuation(context.Background(), fixture.projectID, fixture.continuationID, blackboardv2.RetainEvidenceRequest{
		IdempotencyKey: "retain-update", Key: "evidence:update", Attempt: "attempt:evidence",
		SourcePath: "update.txt", ArtifactType: "text", Summary: "Initial Evidence summary", MediaType: "text/plain", CapturedAt: "2026-07-17T10:00:00Z",
	}); err != nil {
		t.Fatalf("retain Evidence: %v", err)
	}
	before, err := fixture.service.ReadCurrent(context.Background(), fixture.projectID, "evidence:update")
	if err != nil {
		t.Fatalf("read retained Evidence: %v", err)
	}
	updated, err := fixture.service.ApplyForContinuation(context.Background(), fixture.projectID, fixture.continuationID, blackboardv2.ChangeBatch{
		Schema: "semantic-change-batch/v2", IdempotencyKey: "update-evidence-semantics",
		Changes: []blackboardv2.Change{{Op: "update", Key: "evidence:update", Version: 1, Type: "evidence", Record: blackboardv2.EvidencePatch{
			Summary: strPtr("Updated Evidence summary"), MediaType: strPtr("application/octet-stream"),
		}}},
	})
	if err != nil {
		t.Fatalf("update Evidence semantics: %v", err)
	}
	if got, want := mustTupleJSON(t, updated.Records), [][]any{{"evidence:update", float64(2)}}; !reflect.DeepEqual(got, want) {
		t.Fatalf("updated Evidence records = %#v, want %#v", got, want)
	}
	after, err := fixture.service.ReadCurrent(context.Background(), fixture.projectID, "evidence:update")
	if err != nil {
		t.Fatalf("read updated Evidence: %v", err)
	}
	if after.Record.Summary != "Updated Evidence summary" || after.Record.MediaType != "application/octet-stream" ||
		after.Record.ManagedPath != before.Record.ManagedPath || after.Record.SHA256 != before.Record.SHA256 || after.Record.Size != before.Record.Size ||
		after.Record.SourcePath != before.Record.SourcePath || after.Record.ArtifactType != before.Record.ArtifactType {
		t.Fatalf("updated Evidence detail = %#v, before = %#v", after, before)
	}
	if _, err := fixture.service.ApplyForContinuation(context.Background(), fixture.projectID, fixture.continuationID, blackboardv2.ChangeBatch{
		Schema: "semantic-change-batch/v2", IdempotencyKey: "stale-evidence-update",
		Changes: []blackboardv2.Change{{Op: "update", Key: "evidence:update", Version: 1, Type: "evidence", Record: blackboardv2.EvidencePatch{Summary: strPtr("Stale")}}},
	}); !isSemanticCode(err, "version_conflict") {
		t.Fatalf("stale Evidence update error = %#v, want version_conflict", err)
	}
}

func TestEvidenceOptionalFieldsRequireExplicitClear(t *testing.T) {
	fixture := newEvidenceV2Fixture(t, "Evidence Explicit Clear")
	fixture.writeSource(t, "clear.txt", "clear semantics\n")
	if _, err := fixture.service.RetainEvidenceForContinuation(context.Background(), fixture.projectID, fixture.continuationID, blackboardv2.RetainEvidenceRequest{
		IdempotencyKey: "retain-clear", Key: "evidence:clear", Attempt: "attempt:evidence",
		SourcePath: "clear.txt", ArtifactType: "text", Summary: "Evidence with optional fields",
		MediaType: "text/plain", CapturedAt: "2026-07-17T10:30:00Z",
	}); err != nil {
		t.Fatalf("retain Evidence with optional fields: %v", err)
	}
	for name, patch := range map[string]blackboardv2.EvidencePatch{
		"empty media_type":  {MediaType: strPtr("")},
		"empty captured_at": {CapturedAt: strPtr("")},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := fixture.service.ApplyForContinuation(context.Background(), fixture.projectID, fixture.continuationID, blackboardv2.ChangeBatch{
				Schema: "semantic-change-batch/v2", IdempotencyKey: "reject-" + name,
				Changes: []blackboardv2.Change{{Op: "update", Key: "evidence:clear", Version: 1, Type: "evidence", Record: patch}},
			}); !isSemanticCode(err, "semantic_validation") {
				t.Fatalf("empty optional Evidence patch error = %#v, want semantic_validation", err)
			}
		})
	}
	detail, err := fixture.service.ReadCurrent(context.Background(), fixture.projectID, "evidence:clear")
	if err != nil || detail.Version != 1 || detail.Record.MediaType != "text/plain" || detail.Record.CapturedAt != "2026-07-17T10:30:00Z" {
		t.Fatalf("empty patch silently cleared optional Evidence fields: %#v, err = %v", detail, err)
	}
	if _, err := fixture.service.ApplyForContinuation(context.Background(), fixture.projectID, fixture.continuationID, blackboardv2.ChangeBatch{
		Schema: "semantic-change-batch/v2", IdempotencyKey: "explicitly-clear-evidence-fields",
		Changes: []blackboardv2.Change{{
			Op: "update", Key: "evidence:clear", Version: 1, Type: "evidence",
			Record: blackboardv2.EvidencePatch{Summary: strPtr("Evidence after explicit clear")},
			Clear:  []string{"media_type", "captured_at"},
		}},
	}); err != nil {
		t.Fatalf("explicitly clear optional Evidence fields: %v", err)
	}
	detail, err = fixture.service.ReadCurrent(context.Background(), fixture.projectID, "evidence:clear")
	if err != nil || detail.Version != 2 || detail.Record.MediaType != "" || detail.Record.CapturedAt != "" {
		t.Fatalf("explicit Evidence clear result = %#v, err = %v", detail, err)
	}
}
