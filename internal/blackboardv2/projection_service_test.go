package blackboardv2_test

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"pentest/internal/blackboardv2"
	"pentest/internal/project"
	"pentest/internal/store"
	"pentest/internal/task"
)

func TestRuntimeSnapshotProjectionMatchesCompleteGoldenBytesAcrossRepeatAndReopen(t *testing.T) {
	for _, test := range []struct {
		name        string
		kind        string
		golden      string
		seed        func(*testing.T, projectionFixture)
		wantRecords int
	}{
		{name: "Pentest", kind: project.KindPentest, golden: "runtime_snapshot.pentest_complete", seed: seedPentestProjection, wantRecords: 9},
		{name: "CTF", kind: project.KindCTFChallenge, golden: "runtime_snapshot.ctf_complete", seed: seedCTFProjection, wantRecords: 5},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newProjectionFixture(t, test.kind)
			test.seed(t, fixture)
			harness := mustHarness(t)
			want, err := harness.Fixture(test.golden)
			if err != nil {
				t.Fatalf("load %s golden: %v", test.name, err)
			}

			for repeat := 0; repeat < 8; repeat++ {
				projection, err := fixture.service.ProjectRuntimeSnapshot(context.Background(), fixture.projectID)
				if err != nil {
					t.Fatalf("project %s repeat %d: %v", test.name, repeat, err)
				}
				assertCompleteProjection(t, projection, want, test.wantRecords)
				if err := harness.Validate("runtimeSnapshot", projection.Bytes); err != nil {
					t.Fatalf("%s projection violates runtimeSnapshot: %v", test.name, err)
				}
			}

			if err := fixture.db.Close(); err != nil {
				t.Fatalf("close %s store: %v", test.name, err)
			}
			reopened, err := store.Open(fixture.dbPath)
			if err != nil {
				t.Fatalf("reopen %s store: %v", test.name, err)
			}
			t.Cleanup(func() { _ = reopened.Close() })
			reopenedService := blackboardv2.NewService(reopened)
			projection, err := reopenedService.ProjectRuntimeSnapshot(context.Background(), fixture.projectID)
			if err != nil {
				t.Fatalf("project reopened %s snapshot: %v", test.name, err)
			}
			assertCompleteProjection(t, projection, want, test.wantRecords)
		})
	}
}

func TestRuntimeSnapshotTextLimitsUseUTF8BytesAndRejectAtomically(t *testing.T) {
	fixture := newProjectionFixture(t, project.KindPentest)
	ctx := context.Background()
	semanticAtLimit := strings.Repeat("界", 341) + "a"
	semanticOverLimit := semanticAtLimit + "b"
	conciseAtLimit := strings.Repeat("界", 170) + "ab"
	conciseOverLimit := conciseAtLimit + "c"
	keyAtLimit := strings.Repeat("k", 96)

	created, err := fixture.service.Apply(ctx, fixture.projectID, blackboardv2.ChangeBatch{
		Schema: "semantic-change-batch/v2", IdempotencyKey: "limit-boundaries",
		Changes: []blackboardv2.Change{
			{Op: "create", Key: keyAtLimit, Type: "objective", Record: blackboardv2.ObjectiveRecord{Status: "open", Objective: semanticAtLimit}},
			{Op: "create", Key: "entity:limit", Type: "entity", Record: blackboardv2.EntityRecord{Status: "active", Kind: "service", Name: "Limit service", Description: conciseAtLimit, ScopeStatus: "in_scope"}},
			{Op: "create", Key: "fact:limit-a", Type: "fact", Record: blackboardv2.FactRecord{Category: "limit", Summary: semanticAtLimit, Confidence: "tentative", ScopeStatus: "in_scope"}},
			{Op: "create", Key: "fact:limit-b", Type: "fact", Record: blackboardv2.FactRecord{Category: "limit", Summary: "Target", Confidence: "tentative", ScopeStatus: "in_scope"}},
			{Op: "relate", From: "fact:limit-a", Relation: "supports", To: "fact:limit-b", Reason: conciseAtLimit},
		},
	})
	if err != nil {
		t.Fatalf("accept exact UTF-8 byte limits: %v", err)
	}
	before := created.Revision

	_, err = fixture.service.Apply(ctx, fixture.projectID, blackboardv2.ChangeBatch{
		Schema: "semantic-change-batch/v2", IdempotencyKey: "reject-over-limit-atomically",
		Changes: []blackboardv2.Change{
			{Op: "create", Key: "fact:must-rollback", Type: "fact", Record: blackboardv2.FactRecord{Category: "limit", Summary: "Must roll back", Confidence: "tentative", ScopeStatus: "in_scope"}},
			{Op: "create", Key: strings.Repeat("x", 97), Type: "objective", Record: blackboardv2.ObjectiveRecord{Status: "open", Objective: "Never stored"}},
		},
	})
	assertSemanticPath(t, err, "semantic_validation", "changes[1].key")
	assertAbsentAtRevision(t, fixture, "fact:must-rollback", before)

	_, err = fixture.service.Apply(ctx, fixture.projectID, blackboardv2.ChangeBatch{
		Schema: "semantic-change-batch/v2", IdempotencyKey: "reject-primary-update",
		Changes: []blackboardv2.Change{{Op: "update", Key: keyAtLimit, Version: 1, Type: "objective", Record: blackboardv2.ObjectivePatch{Objective: strPtr(semanticOverLimit)}}},
	})
	assertSemanticPath(t, err, "semantic_validation", "changes[0].record.objective")

	_, err = fixture.service.Apply(ctx, fixture.projectID, blackboardv2.ChangeBatch{
		Schema: "semantic-change-batch/v2", IdempotencyKey: "reject-description-create",
		Changes: []blackboardv2.Change{{Op: "create", Key: "entity:over-description", Type: "entity", Record: blackboardv2.EntityRecord{Status: "active", Kind: "service", Name: "Over", Description: conciseOverLimit, ScopeStatus: "in_scope"}}},
	})
	assertSemanticPath(t, err, "semantic_validation", "changes[0].record.description")

	_, err = fixture.service.Apply(ctx, fixture.projectID, blackboardv2.ChangeBatch{
		Schema: "semantic-change-batch/v2", IdempotencyKey: "reject-finding-description",
		Changes: []blackboardv2.Change{{Op: "create", Key: "finding:over-description", Type: "finding", Record: blackboardv2.FindingRecord{Status: "unconfirmed", Title: "Overlong description", Description: semanticOverLimit}}},
	})
	assertSemanticPath(t, err, "semantic_validation", "changes[0].record.description")

	_, err = fixture.service.Apply(ctx, fixture.projectID, blackboardv2.ChangeBatch{
		Schema: "semantic-change-batch/v2", IdempotencyKey: "reject-reason-update",
		Changes: []blackboardv2.Change{{Op: "relate", From: "fact:limit-a", Relation: "supports", To: "fact:limit-b", Version: 1, Reason: conciseOverLimit}},
	})
	assertSemanticPath(t, err, "semantic_validation", "changes[0].reason")

	_, err = fixture.service.Apply(ctx, fixture.projectID, blackboardv2.ChangeBatch{
		Schema: "semantic-change-batch/v2", IdempotencyKey: "seed-merge-limit",
		Changes: []blackboardv2.Change{
			{Op: "create", Key: "fact:merge-source-limit", Type: "fact", Record: blackboardv2.FactRecord{Category: "duplicate", Summary: "Same duplicate", Confidence: "tentative", ScopeStatus: "unknown"}},
			{Op: "create", Key: "fact:merge-canonical-limit", Type: "fact", Record: blackboardv2.FactRecord{Category: "duplicate", Summary: "Same duplicate", Confidence: "tentative", ScopeStatus: "unknown"}},
		},
	})
	if err != nil {
		t.Fatalf("seed merge text limit: %v", err)
	}
	_, err = fixture.service.Apply(ctx, fixture.projectID, blackboardv2.ChangeBatch{
		Schema: "semantic-change-batch/v2", IdempotencyKey: "reject-merge-primary",
		Changes: []blackboardv2.Change{{Op: "merge", Source: "fact:merge-source-limit", SourceVersion: 1, Canonical: "fact:merge-canonical-limit", CanonicalVersion: 1, CanonicalRecord: blackboardv2.FactPatch{Summary: strPtr(semanticOverLimit)}}},
	})
	assertSemanticPath(t, err, "semantic_validation", "changes[0].record.summary")
	if detail, readErr := fixture.service.ReadCurrent(ctx, fixture.projectID, "fact:merge-source-limit"); readErr != nil || detail.Key != "fact:merge-source-limit" {
		t.Fatalf("over-limit merge partially redirected source: %#v, %v", detail, readErr)
	}

	prepareEvidenceAttempt(t, fixture)
	if err := os.WriteFile(filepath.Join(fixture.workdir, "limit.txt"), []byte("proof"), 0o600); err != nil {
		t.Fatalf("write limit Evidence source: %v", err)
	}
	_, err = fixture.service.RetainEvidenceForContinuation(ctx, fixture.projectID, fixture.continuationID, blackboardv2.RetainEvidenceRequest{
		IdempotencyKey: "reject-retained-summary", Key: "evidence:over-limit", Attempt: "attempt:evidence-limit", SourcePath: "limit.txt", ArtifactType: "text", Summary: semanticOverLimit,
	})
	assertSemanticPath(t, err, "semantic_validation", "summary")
	if _, readErr := fixture.service.ReadCurrent(ctx, fixture.projectID, "evidence:over-limit"); !isSemanticCode(readErr, "not_found") {
		t.Fatalf("over-limit Evidence retention partially committed: %v", readErr)
	}
}

func TestSemanticDetailAndHistoryAreDistinctClosedCursorBoundDTOs(t *testing.T) {
	fixture := newProjectionFixture(t, project.KindPentest)
	ctx := context.Background()
	_, err := fixture.service.Apply(ctx, fixture.projectID, blackboardv2.ChangeBatch{
		Schema: "semantic-change-batch/v2", IdempotencyKey: "seed-detail-history",
		Changes: []blackboardv2.Change{{Op: "create", Key: "fact:history", Type: "fact", Record: blackboardv2.FactRecord{Category: "detail", Summary: "Version one", Body: "Full detail body", Confidence: "tentative", ScopeStatus: "in_scope"}}},
	})
	if err != nil {
		t.Fatalf("seed detail/history: %v", err)
	}
	for version, summary := range []string{"Version two", "Version three", "Version four"} {
		_, err = fixture.service.Apply(ctx, fixture.projectID, blackboardv2.ChangeBatch{
			Schema: "semantic-change-batch/v2", IdempotencyKey: "update-detail-history-" + summary,
			Changes: []blackboardv2.Change{{Op: "update", Key: "fact:history", Version: version + 1, Type: "fact", Record: blackboardv2.FactPatch{Summary: strPtr(summary)}}},
		})
		if err != nil {
			t.Fatalf("update detail/history to %q: %v", summary, err)
		}
	}

	detail, err := fixture.service.ReadCurrent(ctx, fixture.projectID, "fact:history")
	if err != nil {
		t.Fatalf("read current detail: %v", err)
	}
	detailBytes, err := json.Marshal(detail)
	if err != nil {
		t.Fatalf("marshal current detail: %v", err)
	}
	if !bytes.Contains(detailBytes, []byte(`"schema":"blackboard-record/v2"`)) || !bytes.Contains(detailBytes, []byte(`"body":"Full detail body"`)) || bytes.Contains(detailBytes, []byte("items")) {
		t.Fatalf("current detail DTO is not closed and distinct: %s", detailBytes)
	}

	first, err := fixture.service.ReadHistory(ctx, fixture.projectID, "fact:history", blackboardv2.HistoryOptions{Limit: 1})
	if err != nil {
		t.Fatalf("read first history page: %v", err)
	}
	firstBytes, _ := json.Marshal(first)
	if !bytes.Contains(firstBytes, []byte(`"schema":"semantic-history/v2"`)) || bytes.Contains(firstBytes, []byte(`"relationships"`)) || first.NextCursor == "" {
		t.Fatalf("Semantic History DTO is not closed and distinct: %s", firstBytes)
	}

	_, err = fixture.service.ReadHistory(ctx, fixture.projectID, "fact:other", blackboardv2.HistoryOptions{Cursor: first.NextCursor, Limit: 1})
	assertSemanticPath(t, err, "semantic_validation", "cursor")
	_, err = fixture.service.ReadHistory(ctx, fixture.projectID, "fact:history", blackboardv2.HistoryOptions{Cursor: first.NextCursor, Limit: 2})
	assertSemanticPath(t, err, "semantic_validation", "cursor")
	_, err = fixture.service.ReadHistory(ctx, fixture.projectID, "fact:history", blackboardv2.HistoryOptions{Limit: 101})
	assertSemanticPath(t, err, "semantic_validation", "limit")

	second, err := fixture.service.ReadHistory(ctx, fixture.projectID, "fact:history", blackboardv2.HistoryOptions{Cursor: first.NextCursor})
	if err != nil {
		t.Fatalf("cursor-carried page size rejected: %v", err)
	}
	if len(second.Items) != 1 || second.Items[0].Version != 2 || second.NextCursor == "" {
		t.Fatalf("second stable history page = %#v", second)
	}

	if err := fixture.db.Close(); err != nil {
		t.Fatalf("close history store: %v", err)
	}
	reopened, err := store.Open(fixture.dbPath)
	if err != nil {
		t.Fatalf("reopen history store: %v", err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	reopenedPage, err := blackboardv2.NewService(reopened).ReadHistory(ctx, fixture.projectID, "fact:history", blackboardv2.HistoryOptions{Cursor: second.NextCursor})
	if err != nil {
		t.Fatalf("continue history cursor after reopen: %v", err)
	}
	if len(reopenedPage.Items) != 1 || reopenedPage.Items[0].Version != 3 || reopenedPage.NextCursor != "" {
		t.Fatalf("reopened stable history page = %#v", reopenedPage)
	}
}

func TestSemanticHistoryCursorsAreProjectBoundTamperEvidentAndStableAcrossReopen(t *testing.T) {
	ctx := context.Background()
	projectA := newProjectionFixture(t, project.KindPentest)
	secondProject, err := project.NewService(projectA.db).Create("Cursor isolation peer", "", project.Scope{}, project.Defaults{})
	if err != nil {
		t.Fatalf("create cursor isolation Project: %v", err)
	}
	projectB := projectionFixture{db: projectA.db, service: projectA.service, projectID: secondProject.ID}
	seedCursorHistory := func(t *testing.T, fixture projectionFixture) {
		t.Helper()
		if _, err := fixture.service.Apply(ctx, fixture.projectID, blackboardv2.ChangeBatch{
			Schema: "semantic-change-batch/v2", IdempotencyKey: "seed-cursor-history",
			Changes: []blackboardv2.Change{{Op: "create", Key: "fact:cursor", Type: "fact", Record: blackboardv2.FactRecord{Category: "cursor", Summary: "Cursor version one", Confidence: "tentative", ScopeStatus: "unknown"}}},
		}); err != nil {
			t.Fatalf("seed cursor Fact: %v", err)
		}
		for version := 1; version <= 3; version++ {
			summary := fmt.Sprintf("Cursor version %d", version+1)
			if _, err := fixture.service.Apply(ctx, fixture.projectID, blackboardv2.ChangeBatch{
				Schema: "semantic-change-batch/v2", IdempotencyKey: fmt.Sprintf("update-cursor-%d", version),
				Changes: []blackboardv2.Change{{Op: "update", Key: "fact:cursor", Version: version, Type: "fact", Record: blackboardv2.FactPatch{Summary: strPtr(summary)}}},
			}); err != nil {
				t.Fatalf("update cursor Fact: %v", err)
			}
		}
	}
	seedCursorHistory(t, projectA)
	seedCursorHistory(t, projectB)

	first, err := projectA.service.ReadHistory(ctx, projectA.projectID, "fact:cursor", blackboardv2.HistoryOptions{Limit: 1})
	if err != nil {
		t.Fatalf("read Project A first page: %v", err)
	}
	if len(first.Items) != 1 || first.Items[0].Version != 1 || first.NextCursor == "" {
		t.Fatalf("Project A first page = %#v", first)
	}
	assertOpaqueCursorPayload(t, first.NextCursor, projectA.projectID)

	_, err = projectB.service.ReadHistory(ctx, projectB.projectID, "fact:cursor", blackboardv2.HistoryOptions{Cursor: first.NextCursor})
	assertSemanticPath(t, err, "semantic_validation", "cursor")

	for _, tampered := range tamperedHistoryCursors(t, first.NextCursor) {
		_, err := projectA.service.ReadHistory(ctx, projectA.projectID, "fact:cursor", blackboardv2.HistoryOptions{Cursor: tampered})
		assertSemanticPath(t, err, "semantic_validation", "cursor")
	}

	if err := projectA.db.Close(); err != nil {
		t.Fatalf("close Project A store: %v", err)
	}
	reopened, err := store.Open(projectA.dbPath)
	if err != nil {
		t.Fatalf("reopen Project A store: %v", err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	reopenedService := blackboardv2.NewService(reopened)
	reissued, err := reopenedService.ReadHistory(ctx, projectA.projectID, "fact:cursor", blackboardv2.HistoryOptions{Limit: 1})
	if err != nil {
		t.Fatalf("reissue first page after reopen: %v", err)
	}
	if reissued.NextCursor != first.NextCursor {
		t.Fatalf("cursor bytes changed across reopen:\nfirst=%s\nreissued=%s", first.NextCursor, reissued.NextCursor)
	}
	second, err := reopenedService.ReadHistory(ctx, projectA.projectID, "fact:cursor", blackboardv2.HistoryOptions{Cursor: first.NextCursor})
	if err != nil {
		t.Fatalf("continue cursor after reopen: %v", err)
	}
	third, err := reopenedService.ReadHistory(ctx, projectA.projectID, "fact:cursor", blackboardv2.HistoryOptions{Cursor: second.NextCursor})
	if err != nil {
		t.Fatalf("continue second cursor after reopen: %v", err)
	}
	if len(second.Items) != 1 || second.Items[0].Version != 2 || len(third.Items) != 1 || third.Items[0].Version != 3 || third.NextCursor != "" {
		t.Fatalf("stable pagination duplicated or skipped history: second=%#v third=%#v", second, third)
	}
}

func TestAttentionBudgetUsesExactSnapshotByteBoundariesAndNeverBlocksLaunch(t *testing.T) {
	const bytesPerToken = 4
	for _, test := range []struct {
		name      string
		byteCount int
		want      blackboardv2.AttentionBudgetState
	}{
		{name: "16K minus one", byteCount: 16_000*bytesPerToken - 1, want: blackboardv2.AttentionWithinTarget},
		{name: "16K", byteCount: 16_000 * bytesPerToken, want: blackboardv2.AttentionWithinTarget},
		{name: "16K plus one", byteCount: 16_000*bytesPerToken + 1, want: blackboardv2.AttentionAboveTarget},
		{name: "32K minus one", byteCount: 32_000*bytesPerToken - 1, want: blackboardv2.AttentionWarning},
		{name: "32K", byteCount: 32_000 * bytesPerToken, want: blackboardv2.AttentionWarning},
		{name: "32K plus one", byteCount: 32_000*bytesPerToken + 1, want: blackboardv2.AttentionWarning},
		{name: "64K minus one", byteCount: 64_000*bytesPerToken - 1, want: blackboardv2.AttentionRequired},
		{name: "64K", byteCount: 64_000 * bytesPerToken, want: blackboardv2.AttentionRequired},
		{name: "64K plus one", byteCount: 64_000*bytesPerToken + 1, want: blackboardv2.AttentionRequired},
	} {
		t.Run(test.name, func(t *testing.T) {
			measurement := blackboardv2.MeasureRuntimeSnapshot(bytes.Repeat([]byte{'x'}, test.byteCount))
			if measurement.Bytes != test.byteCount || measurement.EstimatedTokens != (test.byteCount+3)/4 || measurement.State != test.want {
				t.Fatalf("measurement = %#v, want bytes=%d state=%s", measurement, test.byteCount, test.want)
			}
			if !measurement.Complete || !measurement.Launchable {
				t.Fatalf("attention state %s made a complete Snapshot unlaunchable: %#v", test.want, measurement)
			}
		})
	}
}

func tamperedHistoryCursors(t *testing.T, cursor string) []string {
	t.Helper()
	encoded := strings.TrimPrefix(cursor, "opaque:")
	parts := strings.Split(encoded, ".")
	if len(parts) != 2 {
		t.Fatalf("cursor is not a signed opaque envelope: %s", cursor)
	}
	payload, err := base64.RawURLEncoding.Strict().DecodeString(parts[0])
	if err != nil {
		t.Fatalf("decode cursor payload: %v", err)
	}
	var fields map[string]any
	if err := json.Unmarshal(payload, &fields); err != nil {
		t.Fatalf("decode cursor fields: %v", err)
	}
	mutations := []func(map[string]any){
		func(value map[string]any) { value["offset"] = value["offset"].(float64) + 1 },
		func(value map[string]any) { value["limit"] = value["limit"].(float64) + 1 },
		func(value map[string]any) { value["key"] = "fact:other" },
		func(value map[string]any) { value["revision"] = value["revision"].(float64) + 1 },
	}
	result := make([]string, 0, len(mutations)+2)
	for _, mutate := range mutations {
		copyFields := make(map[string]any, len(fields))
		for key, value := range fields {
			copyFields[key] = value
		}
		mutate(copyFields)
		changed, err := json.Marshal(copyFields)
		if err != nil {
			t.Fatalf("encode tampered cursor: %v", err)
		}
		result = append(result, "opaque:"+base64.RawURLEncoding.EncodeToString(changed)+"."+parts[1])
	}
	tamperedPayload := append([]byte(nil), payload...)
	tamperedPayload[len(tamperedPayload)/2] ^= 1
	result = append(result, "opaque:"+base64.RawURLEncoding.EncodeToString(tamperedPayload)+"."+parts[1])
	signature, err := base64.RawURLEncoding.Strict().DecodeString(parts[1])
	if err != nil {
		t.Fatalf("decode cursor signature: %v", err)
	}
	signature[0] ^= 1
	result = append(result, "opaque:"+parts[0]+"."+base64.RawURLEncoding.EncodeToString(signature))
	return result
}

func assertOpaqueCursorPayload(t *testing.T, cursor, projectID string) {
	t.Helper()
	parts := strings.Split(strings.TrimPrefix(cursor, "opaque:"), ".")
	if len(parts) != 2 {
		t.Fatalf("cursor is not opaque and signed: %s", cursor)
	}
	payload, err := base64.RawURLEncoding.Strict().DecodeString(parts[0])
	if err != nil {
		t.Fatalf("decode cursor payload: %v", err)
	}
	if bytes.Contains(payload, []byte(projectID)) || bytes.Contains(payload, []byte("project_id")) {
		t.Fatalf("cursor payload exposed internal Project identity: %s", payload)
	}
	var fields map[string]any
	if err := json.Unmarshal(payload, &fields); err != nil {
		t.Fatalf("decode cursor payload JSON: %v", err)
	}
	if len(fields) != 5 || fields["schema"] != "semantic-history-cursor/v2" || fields["key"] != "fact:cursor" {
		t.Fatalf("cursor payload is not closed: %#v", fields)
	}
}

func TestRuntimeSnapshotProjectionIsIndependentOfSemanticInsertionOrder(t *testing.T) {
	ctx := context.Background()
	forward := newProjectionFixture(t, project.KindPentest)
	reverse := newProjectionFixture(t, project.KindPentest)
	records := []blackboardv2.Change{
		{Op: "create", Key: "entity:order-a", Type: "entity", Record: blackboardv2.EntityRecord{Status: "active", Kind: "service", Name: "Order A", ScopeStatus: "in_scope"}},
		{Op: "create", Key: "entity:order-b", Type: "entity", Record: blackboardv2.EntityRecord{Status: "active", Kind: "endpoint", Name: "Order B", ScopeStatus: "unknown"}},
		{Op: "create", Key: "objective:order", Type: "objective", Record: blackboardv2.ObjectiveRecord{Status: "open", Objective: "Verify canonical insertion ordering"}},
		{Op: "create", Key: "fact:order-a", Type: "fact", Record: blackboardv2.FactRecord{Category: "ordering", Summary: "First lexical Fact", Confidence: "tentative", ScopeStatus: "in_scope"}},
		{Op: "create", Key: "fact:order-b", Type: "fact", Record: blackboardv2.FactRecord{Category: "ordering", Summary: "Second lexical Fact", Confidence: "tentative", ScopeStatus: "unknown"}},
	}
	relations := []blackboardv2.Change{
		{Op: "relate", From: "fact:order-b", Relation: "about", To: "entity:order-b"},
		{Op: "relate", From: "fact:order-a", Relation: "supports", To: "fact:order-b", Reason: "The first observation explains the second"},
		{Op: "relate", From: "fact:order-a", Relation: "satisfies", To: "objective:order"},
		{Op: "relate", From: "entity:order-b", Relation: "part_of", To: "entity:order-a"},
	}
	applyOrdered := func(t *testing.T, fixture projectionFixture, reverseOrder bool) []byte {
		t.Helper()
		changes := append([]blackboardv2.Change(nil), records...)
		orderedRelations := append([]blackboardv2.Change(nil), relations...)
		if reverseOrder {
			reverseChanges(changes)
			reverseChanges(orderedRelations)
		}
		changes = append(changes, orderedRelations...)
		if _, err := fixture.service.Apply(ctx, fixture.projectID, blackboardv2.ChangeBatch{Schema: "semantic-change-batch/v2", IdempotencyKey: "seed-order", Changes: changes}); err != nil {
			t.Fatalf("seed insertion order: %v", err)
		}
		projection, err := fixture.service.ProjectRuntimeSnapshot(ctx, fixture.projectID)
		if err != nil {
			t.Fatalf("project insertion order: %v", err)
		}
		return projection.Bytes
	}
	forwardBytes := applyOrdered(t, forward, false)
	reverseBytes := applyOrdered(t, reverse, true)
	if !bytes.Equal(forwardBytes, reverseBytes) {
		t.Fatalf("insertion order changed canonical bytes:\nforward=%s\nreverse=%s", forwardBytes, reverseBytes)
	}
}

func TestRealRuntimeSnapshotsRemainCompleteAndLaunchableInEveryAttentionState(t *testing.T) {
	for _, test := range []struct {
		name      string
		factCount int
		want      blackboardv2.AttentionBudgetState
	}{
		{name: "within target", factCount: 1, want: blackboardv2.AttentionWithinTarget},
		{name: "above target", factCount: 60, want: blackboardv2.AttentionAboveTarget},
		{name: "warning", factCount: 120, want: blackboardv2.AttentionWarning},
		{name: "required", factCount: 230, want: blackboardv2.AttentionRequired},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newProjectionFixture(t, project.KindPentest)
			changes := make([]blackboardv2.Change, 0, test.factCount)
			for index := 0; index < test.factCount; index++ {
				prefix := fmt.Sprintf("Fact %03d ", index)
				changes = append(changes, blackboardv2.Change{
					Op: "create", Key: fmt.Sprintf("fact:budget:%03d", index), Type: "fact",
					Record: blackboardv2.FactRecord{Category: "budget", Summary: prefix + strings.Repeat("x", 1024-len(prefix)), Confidence: "tentative", ScopeStatus: "unknown"},
				})
			}
			if _, err := fixture.service.Apply(context.Background(), fixture.projectID, blackboardv2.ChangeBatch{Schema: "semantic-change-batch/v2", IdempotencyKey: "seed-budget", Changes: changes}); err != nil {
				t.Fatalf("seed %s Snapshot: %v", test.name, err)
			}
			projection, err := fixture.service.ProjectRuntimeSnapshot(context.Background(), fixture.projectID)
			if err != nil {
				t.Fatalf("project %s Snapshot: %v", test.name, err)
			}
			if projection.AttentionState != test.want || !projection.Complete || !projection.Launchable {
				t.Fatalf("%s projection state = %s complete=%t launchable=%t", test.name, projection.AttentionState, projection.Complete, projection.Launchable)
			}
			if len(projection.Snapshot.Knowledge.Facts) != test.factCount || projection.Snapshot.Revision != test.factCount {
				t.Fatalf("%s projection filtered records: facts=%d revision=%d, want %d", test.name, len(projection.Snapshot.Knowledge.Facts), projection.Snapshot.Revision, test.factCount)
			}
		})
	}
}

func reverseChanges(changes []blackboardv2.Change) {
	for left, right := 0, len(changes)-1; left < right; left, right = left+1, right-1 {
		changes[left], changes[right] = changes[right], changes[left]
	}
}

type projectionFixture struct {
	db             *store.DB
	dbPath         string
	service        *blackboardv2.Service
	projectID      string
	continuationID string
	workdir        string
	runtimeRoot    string
}

func newProjectionFixture(t *testing.T, kind string) projectionFixture {
	t.Helper()
	root := t.TempDir()
	dbPath := filepath.Join(root, "pentest.db")
	db, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("open projection store: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	projects := project.NewService(db)
	createdProject, err := projects.CreateWithKind("Projection fixture", "", kind, project.Scope{}, project.Defaults{})
	if err != nil {
		t.Fatalf("create projection Project: %v", err)
	}
	tasks := task.NewService(db, projects)
	createdTask, err := tasks.Create(task.CreateRequest{ProjectID: createdProject.ID, Goal: "Project exact Runtime Snapshot", Runner: task.RunnerSandbox})
	if err != nil {
		t.Fatalf("create projection Task: %v", err)
	}
	continuation, err := tasks.CreateContinuation(createdTask.ID, "profile-projection", "codex", task.RunnerSandbox)
	if err != nil {
		t.Fatalf("create projection Continuation: %v", err)
	}
	runtimeRoot := filepath.Join(root, "runtime")
	workdir := filepath.Join(runtimeRoot, createdTask.ID, "workdir")
	if err := os.MkdirAll(workdir, 0o700); err != nil {
		t.Fatalf("create projection workdir: %v", err)
	}
	return projectionFixture{
		db: db, dbPath: dbPath,
		service:   blackboardv2.NewServiceWithEvidence(db, blackboardv2.EvidenceConfig{RuntimeRoot: runtimeRoot, ArtifactRoot: runtimeRoot}),
		projectID: createdProject.ID, continuationID: continuation.ID, workdir: workdir, runtimeRoot: runtimeRoot,
	}
}

func seedPentestProjection(t *testing.T, fixture projectionFixture) {
	t.Helper()
	ctx := context.Background()
	_, err := fixture.service.ApplyForContinuation(ctx, fixture.projectID, fixture.continuationID, blackboardv2.ChangeBatch{
		Schema: "semantic-change-batch/v2", IdempotencyKey: "seed-pentest-projection",
		Changes: []blackboardv2.Change{
			{Op: "create", Key: "entity:web", Type: "entity", Record: blackboardv2.EntityRecord{Status: "active", Kind: "service", Name: "Web application", Description: "Primary application boundary", ScopeStatus: "in_scope", CredentialRef: "credential:web-test"}},
			{Op: "create", Key: "entity:admin", Type: "entity", Record: blackboardv2.EntityRecord{Status: "active", Kind: "endpoint", Name: "Admin endpoint", Locator: "https://example.test/admin", ScopeStatus: "in_scope"}},
			{Op: "create", Key: "objective:prerequisite", Type: "objective", Record: blackboardv2.ObjectiveRecord{Status: "open", Objective: "Establish the admin authorization boundary"}},
			{Op: "create", Key: "objective:admin", Type: "objective", Record: blackboardv2.ObjectiveRecord{Status: "open", Objective: "Determine whether admin access can be bypassed"}},
			{Op: "create", Key: "attempt:admin", Type: "attempt", Record: blackboardv2.AttemptRecord{Status: "open", Summary: "Testing the admin endpoint authorization checks"}},
			{Op: "create", Key: "fact:boundary", Type: "fact", Record: blackboardv2.FactRecord{Category: "authorization", Summary: "The admin route is intended for privileged identities", Body: "Application code checks an administrator role before rendering the route.", Confidence: "tentative", ScopeStatus: "in_scope"}},
			{Op: "create", Key: "fact:admin", Type: "fact", Record: blackboardv2.FactRecord{Category: "authorization", Summary: "The admin route responds without a privileged session", Body: "A retained response contains the administrative page without an authenticated cookie.", Confidence: "tentative", ScopeStatus: "in_scope"}},
			{Op: "create", Key: "finding:admin", Type: "finding", Record: blackboardv2.FindingRecord{Status: "unconfirmed", Title: "Admin access control bypass", Target: "https://example.test/admin", Description: "The admin route may be reachable without a privileged session", Proof: "Full response is retained as Evidence", Impact: "Administrative functions may be exposed", Recommendation: "Enforce authorization before route handling", CVSSVersion: "4.0", CVSSVector: "CVSS:4.0/AV:N/AC:L/AT:N/PR:N/UI:N/VC:H/VI:H/VA:N/SC:N/SI:N/SA:N"}},
			{Op: "relate", From: "entity:admin", Relation: "part_of", To: "entity:web"},
			{Op: "relate", From: "objective:admin", Relation: "part_of", To: "objective:prerequisite"},
			{Op: "relate", From: "objective:admin", Relation: "derived_from", To: "fact:boundary"},
			{Op: "relate", From: "objective:admin", Relation: "depends_on", To: "objective:prerequisite", Reason: "The intended authorization boundary must be established first"},
			{Op: "relate", From: "attempt:admin", Relation: "tests", To: "objective:admin"},
			{Op: "relate", From: "attempt:admin", Relation: "about", To: "entity:admin"},
			{Op: "relate", From: "attempt:admin", Relation: "produced", To: "fact:admin"},
			{Op: "relate", From: "fact:admin", Relation: "about", To: "entity:admin"},
			{Op: "relate", From: "fact:admin", Relation: "supports", To: "finding:admin", Reason: "The unauthenticated response supports the access-control concern"},
			{Op: "relate", From: "fact:boundary", Relation: "contradicts", To: "fact:admin", Reason: "The intended role check conflicts with the observed response"},
			{Op: "relate", From: "fact:admin", Relation: "satisfies", To: "objective:prerequisite"},
			{Op: "relate", From: "finding:admin", Relation: "about", To: "entity:admin"},
		},
	})
	if err != nil {
		t.Fatalf("seed Pentest projection: %v", err)
	}
	if err := os.WriteFile(filepath.Join(fixture.workdir, "admin.http"), []byte("HTTP/1.1 200 OK\n\nadmin\n"), 0o600); err != nil {
		t.Fatalf("write Pentest Evidence source: %v", err)
	}
	_, err = fixture.service.RetainEvidenceForContinuation(ctx, fixture.projectID, fixture.continuationID, blackboardv2.RetainEvidenceRequest{
		IdempotencyKey: "retain-pentest-projection", Key: "evidence:admin", Attempt: "attempt:admin", SourcePath: "admin.http",
		ArtifactType: "http_exchange", Summary: "Captured unauthenticated admin response", MediaType: "message/http", CapturedAt: "2026-07-17T12:00:00Z",
		Links: []blackboardv2.EvidenceLink{{"evidences", "finding:admin"}, {"about", "entity:admin"}},
	})
	if err != nil {
		t.Fatalf("retain Pentest projection Evidence: %v", err)
	}
}

func seedCTFProjection(t *testing.T, fixture projectionFixture) {
	t.Helper()
	_, err := fixture.service.ApplyForContinuation(context.Background(), fixture.projectID, fixture.continuationID, blackboardv2.ChangeBatch{
		Schema: "semantic-change-batch/v2", IdempotencyKey: "seed-ctf-projection",
		Changes: []blackboardv2.Change{
			{Op: "create", Key: "entity:challenge", Type: "entity", Record: blackboardv2.EntityRecord{Status: "active", Kind: "service", Name: "Challenge service", Locator: "https://ctf.example.test", ScopeStatus: "in_scope"}},
			{Op: "create", Key: "objective:solve", Type: "objective", Record: blackboardv2.ObjectiveRecord{Status: "open", Objective: "Recover and verify the challenge flag"}},
			{Op: "create", Key: "attempt:solve", Type: "attempt", Record: blackboardv2.AttemptRecord{Status: "open", Summary: "Testing the challenge input parser"}},
			{Op: "create", Key: "fact:clue", Type: "fact", Record: blackboardv2.FactRecord{Category: "challenge", Summary: "The parser decodes a reversed hexadecimal token", Body: "The decompiled parser reverses bytes after hexadecimal decoding.", Confidence: "tentative", ScopeStatus: "in_scope"}},
			{Op: "create", Key: "solution:flag", Type: "solution", Record: blackboardv2.SolutionRecord{Status: "verified", Kind: "flag", Summary: "Recovered the challenge flag", Value: "FLAG{deterministic}", VerificationSummary: "Accepted by the local challenge verifier"}},
			{Op: "relate", From: "attempt:solve", Relation: "about", To: "entity:challenge"},
			{Op: "relate", From: "attempt:solve", Relation: "tests", To: "objective:solve"},
			{Op: "relate", From: "attempt:solve", Relation: "produced", To: "solution:flag"},
			{Op: "relate", From: "fact:clue", Relation: "about", To: "entity:challenge"},
			{Op: "relate", From: "fact:clue", Relation: "supports", To: "solution:flag", Reason: "The parser behavior explains the recovered flag"},
			{Op: "relate", From: "solution:flag", Relation: "satisfies", To: "objective:solve"},
		},
	})
	if err != nil {
		t.Fatalf("seed CTF projection: %v", err)
	}
}

func prepareEvidenceAttempt(t *testing.T, fixture projectionFixture) {
	t.Helper()
	_, err := fixture.service.ApplyForContinuation(context.Background(), fixture.projectID, fixture.continuationID, blackboardv2.ChangeBatch{
		Schema: "semantic-change-batch/v2", IdempotencyKey: "prepare-limit-evidence",
		Changes: []blackboardv2.Change{
			{Op: "create", Key: "objective:evidence-limit", Type: "objective", Record: blackboardv2.ObjectiveRecord{Status: "open", Objective: "Retain bounded Evidence"}},
			{Op: "create", Key: "attempt:evidence-limit", Type: "attempt", Record: blackboardv2.AttemptRecord{Status: "open", Summary: "Retain bounded Evidence"}},
			{Op: "relate", From: "attempt:evidence-limit", Relation: "tests", To: "objective:evidence-limit"},
		},
	})
	if err != nil {
		t.Fatalf("prepare Evidence limit Attempt: %v", err)
	}
}

func assertCompleteProjection(t *testing.T, projection blackboardv2.RuntimeSnapshotProjection, want []byte, wantRecords int) {
	t.Helper()
	if !bytes.Equal(projection.Bytes, want) {
		t.Fatalf("projection bytes =\n%s\nwant =\n%s", projection.Bytes, want)
	}
	if projection.ByteCount != len(want) || projection.EstimatedTokens != (len(want)+3)/4 {
		t.Fatalf("projection measurement = bytes %d tokens %d, want %d/%d", projection.ByteCount, projection.EstimatedTokens, len(want), (len(want)+3)/4)
	}
	if !projection.Complete || !projection.Launchable {
		t.Fatalf("projection is incomplete or unlaunchable: %#v", projection)
	}
	var document struct {
		Work struct {
			Objectives map[string]any `json:"objectives"`
			Attempts   map[string]any `json:"attempts"`
		} `json:"work"`
		Knowledge struct {
			Entities  map[string]any `json:"entities"`
			Facts     map[string]any `json:"facts"`
			Findings  map[string]any `json:"findings"`
			Solutions map[string]any `json:"solutions"`
			Evidence  map[string]any `json:"evidence"`
		} `json:"knowledge"`
	}
	if err := json.Unmarshal(projection.Bytes, &document); err != nil {
		t.Fatalf("decode complete projection: %v", err)
	}
	gotRecords := len(document.Work.Objectives) + len(document.Work.Attempts) + len(document.Knowledge.Entities) + len(document.Knowledge.Facts) + len(document.Knowledge.Findings) + len(document.Knowledge.Solutions) + len(document.Knowledge.Evidence)
	if gotRecords != wantRecords {
		t.Fatalf("projected records = %d, want %d", gotRecords, wantRecords)
	}
	for _, forbidden := range []string{"Full detail body", "Application code checks", "retained response contains", "proof", "impact", "recommendation", "managed_path", "source_path", "sha256", "project_id", "continuation", "trusted_origin", "created_at", "updated_at", "diagnostic", "redirect"} {
		if bytes.Contains(bytes.ToLower(projection.Bytes), []byte(strings.ToLower(forbidden))) {
			t.Fatalf("projection leaked forbidden content %q: %s", forbidden, projection.Bytes)
		}
	}
}

func assertSemanticPath(t *testing.T, err error, code, path string) {
	t.Helper()
	var semanticErr *blackboardv2.Error
	if !errors.As(err, &semanticErr) || semanticErr.Code != code || semanticErr.Path != path {
		t.Fatalf("semantic error = %#v, want %s on %s", err, code, path)
	}
}

func assertAbsentAtRevision(t *testing.T, fixture projectionFixture, key string, wantRevision int) {
	t.Helper()
	if _, err := fixture.service.ReadCurrent(context.Background(), fixture.projectID, key); !isSemanticCode(err, "not_found") {
		t.Fatalf("atomic rejection retained %s: %v", key, err)
	}
	snapshot, err := fixture.service.RuntimeSnapshot(context.Background(), fixture.projectID)
	if err != nil {
		t.Fatalf("read snapshot after atomic rejection: %v", err)
	}
	if snapshot.Revision != wantRevision {
		t.Fatalf("atomic rejection revision = %d, want %d", snapshot.Revision, wantRevision)
	}
}
