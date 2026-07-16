package blackboardv2_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"pentest/internal/blackboardv2"
	"pentest/internal/blackboardv2contract"
	"pentest/internal/project"
	"pentest/internal/store"
	"pentest/internal/task"
)

func TestProjectFactCreateUpdateDetailHistoryAndSnapshotEndToEnd(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "pentest.db")
	db, err := store.Open(path)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	projects := project.NewService(db)
	alpha, err := projects.Create("Alpha", "", project.Scope{}, project.Defaults{})
	if err != nil {
		t.Fatalf("create alpha project: %v", err)
	}
	beta, err := projects.Create("Beta", "", project.Scope{}, project.Defaults{})
	if err != nil {
		t.Fatalf("create beta project: %v", err)
	}
	service := blackboardv2.NewService(db)
	harness := mustHarness(t)

	emptySnapshot, err := service.RuntimeSnapshot(ctx, alpha.ID)
	if err != nil {
		t.Fatalf("runtime snapshot for empty project: %v", err)
	}
	assertContractJSON(t, harness, "runtimeSnapshot", emptySnapshot)
	emptySnapshotJSON := mustJSON(t, emptySnapshot)
	wantEmptySnapshot := `{"schema":"runtime-blackboard/v2","semantics":"work is active; knowledge is current; history and details are available by key","revision":0,"work":{},"knowledge":{},"relations":[]}`
	if string(emptySnapshotJSON) != wantEmptySnapshot {
		t.Fatalf("empty snapshot JSON = %s, want %s", emptySnapshotJSON, wantEmptySnapshot)
	}

	create := blackboardv2.ChangeBatch{
		Schema:         "semantic-change-batch/v2",
		IdempotencyKey: "create-login-json",
		Changes: []blackboardv2.Change{{
			Op:   "create",
			Key:  "fact:login-json",
			Type: "fact",
			Record: blackboardv2.FactRecord{
				Category:    "authentication",
				Summary:     "Login probably accepts JSON requests",
				Body:        "Observed Content-Type: application/json during login testing.",
				Confidence:  "tentative",
				ScopeStatus: "in_scope",
			},
		}},
	}
	createResult, err := service.Apply(ctx, alpha.ID, create)
	if err != nil {
		t.Fatalf("create fact: %v", err)
	}
	assertChangeResult(t, createResult, 1, [][]any{{"fact:login-json", float64(1)}})
	assertContractJSON(t, harness, "changeResult", createResult)

	replayResult, err := service.Apply(ctx, alpha.ID, create)
	if err != nil {
		t.Fatalf("replay create fact: %v", err)
	}
	if !reflect.DeepEqual(replayResult, createResult) {
		t.Fatalf("idempotent replay result = %#v, want original %#v", replayResult, createResult)
	}

	if _, err := service.Apply(ctx, beta.ID, create); err != nil {
		t.Fatalf("same fact key should be valid in another project: %v", err)
	}
	betaDetail, err := service.ReadCurrent(ctx, beta.ID, "fact:login-json")
	if err != nil {
		t.Fatalf("read beta fact: %v", err)
	}
	if betaDetail.Revision != 1 || betaDetail.Version != 1 || betaDetail.Record.Summary != "Login probably accepts JSON requests" {
		t.Fatalf("beta fact was not project-isolated: %#v", betaDetail)
	}

	update := blackboardv2.ChangeBatch{
		Schema:         "semantic-change-batch/v2",
		IdempotencyKey: "update-login-json-summary",
		Changes: []blackboardv2.Change{{
			Op:      "update",
			Key:     "fact:login-json",
			Version: 1,
			Type:    "fact",
			Record: blackboardv2.FactPatch{
				Summary: strPtr("The login endpoint accepts JSON requests"),
			},
		}},
	}
	updateResult, err := service.Apply(ctx, alpha.ID, update)
	if err != nil {
		t.Fatalf("partial update fact: %v", err)
	}
	assertChangeResult(t, updateResult, 2, [][]any{{"fact:login-json", float64(2)}})

	detailBeforeClear, err := service.ReadCurrent(ctx, alpha.ID, "fact:login-json")
	if err != nil {
		t.Fatalf("read current fact before clear: %v", err)
	}
	if detailBeforeClear.Record.Body == "" {
		t.Fatal("partial update cleared an omitted Fact body")
	}
	assertContractJSON(t, harness, "currentDetail", detailBeforeClear)

	clear := blackboardv2.ChangeBatch{
		Schema:         "semantic-change-batch/v2",
		IdempotencyKey: "clear-login-json-body",
		Changes: []blackboardv2.Change{{
			Op:      "update",
			Key:     "fact:login-json",
			Version: 2,
			Type:    "fact",
			Record:  blackboardv2.FactPatch{Summary: strPtr("The login endpoint accepts JSON requests")},
			Clear:   []string{"body"},
		}},
	}
	clearResult, err := service.Apply(ctx, alpha.ID, clear)
	if err != nil {
		t.Fatalf("clear fact body: %v", err)
	}
	assertChangeResult(t, clearResult, 3, [][]any{{"fact:login-json", float64(3)}})

	noop := blackboardv2.ChangeBatch{
		Schema:         "semantic-change-batch/v2",
		IdempotencyKey: "noop-login-json-summary",
		Changes: []blackboardv2.Change{{
			Op:      "update",
			Key:     "fact:login-json",
			Version: 3,
			Type:    "fact",
			Record:  blackboardv2.FactPatch{Summary: strPtr("The login endpoint accepts JSON requests")},
		}},
	}
	noopResult, err := service.Apply(ctx, alpha.ID, noop)
	if err != nil {
		t.Fatalf("exact no-op fact update: %v", err)
	}
	assertChangeResult(t, noopResult, 3, nil)

	stale := noop
	stale.IdempotencyKey = "stale-login-json-summary"
	stale.Changes[0].Version = 2
	_, err = service.Apply(ctx, alpha.ID, stale)
	var semanticErr *blackboardv2.Error
	if !errors.As(err, &semanticErr) || semanticErr.Code != "version_conflict" || semanticErr.Path != "changes[0].version" {
		t.Fatalf("stale version error = %#v, want version_conflict on changes[0].version", err)
	}
	if semanticErr.Details["key"] != "fact:login-json" ||
		semanticErr.Details["expected_version"] != float64(2) ||
		semanticErr.Details["current_version"] != float64(3) ||
		semanticErr.Details["next_action"] != "read_current_record" {
		t.Fatalf("version conflict details = %#v", semanticErr.Details)
	}

	conflictingReplay := clear
	conflictingReplay.Changes[0].Clear = nil
	conflictingReplay.Changes[0].Record = blackboardv2.FactPatch{Category: strPtr("changed")}
	_, err = service.Apply(ctx, alpha.ID, conflictingReplay)
	if !errors.As(err, &semanticErr) || semanticErr.Code != "idempotency_conflict" {
		t.Fatalf("changed-payload replay error = %#v, want idempotency_conflict", err)
	}

	detail, err := service.ReadCurrent(ctx, alpha.ID, "fact:login-json")
	if err != nil {
		t.Fatalf("read current fact: %v", err)
	}
	if detail.Revision != 3 || detail.Version != 3 || detail.Record.Body != "" || len(detail.Relationships) != 0 {
		t.Fatalf("current detail = %#v, want full body-cleared Fact without relationships", detail)
	}
	assertContractJSON(t, harness, "currentDetail", detail)

	firstHistoryPage, err := service.ReadHistory(ctx, alpha.ID, "fact:login-json", blackboardv2.HistoryOptions{Limit: 1})
	if err != nil {
		t.Fatalf("read first history page: %v", err)
	}
	if firstHistoryPage.Revision != 3 || firstHistoryPage.Key != "fact:login-json" || len(firstHistoryPage.Items) != 1 || firstHistoryPage.Items[0].Version != 1 || firstHistoryPage.NextCursor == "" {
		t.Fatalf("first history page = %#v", firstHistoryPage)
	}
	assertContractJSON(t, harness, "semanticHistory", firstHistoryPage)

	secondHistoryPage, err := service.ReadHistory(ctx, alpha.ID, "fact:login-json", blackboardv2.HistoryOptions{Cursor: firstHistoryPage.NextCursor, Limit: 1})
	if err != nil {
		t.Fatalf("read second history page: %v", err)
	}
	if len(secondHistoryPage.Items) != 1 || secondHistoryPage.Items[0].Version != 2 || secondHistoryPage.NextCursor != "" {
		t.Fatalf("second history page = %#v", secondHistoryPage)
	}
	assertContractJSON(t, harness, "semanticHistory", secondHistoryPage)

	snapshot, err := service.RuntimeSnapshot(ctx, alpha.ID)
	if err != nil {
		t.Fatalf("runtime snapshot: %v", err)
	}
	assertContractJSON(t, harness, "runtimeSnapshot", snapshot)
	snapshotJSON := mustJSON(t, snapshot)
	wantSnapshot := `{"schema":"runtime-blackboard/v2","semantics":"work is active; knowledge is current; history and details are available by key","revision":3,"work":{},"knowledge":{"facts":{"fact:login-json":{"version":3,"category":"authentication","summary":"The login endpoint accepts JSON requests","confidence":"tentative","scope_status":"in_scope"}}},"relations":[]}`
	if string(snapshotJSON) != wantSnapshot {
		t.Fatalf("snapshot JSON = %s, want %s", snapshotJSON, wantSnapshot)
	}
	for _, forbidden := range []string{"Observed Content-Type", "body", "project_id", "trusted", "audit", "hash", "diagnostic", "internal"} {
		if strings.Contains(string(snapshotJSON), forbidden) {
			t.Fatalf("snapshot leaked forbidden field/content %q: %s", forbidden, snapshotJSON)
		}
	}

	if err := db.Close(); err != nil {
		t.Fatalf("close first store: %v", err)
	}
	reopenedDB, err := store.Open(path)
	if err != nil {
		t.Fatalf("reopen store: %v", err)
	}
	t.Cleanup(func() { _ = reopenedDB.Close() })
	reopenedService := blackboardv2.NewService(reopenedDB)
	reopenedDetail, err := reopenedService.ReadCurrent(ctx, alpha.ID, "fact:login-json")
	if err != nil {
		t.Fatalf("read reopened fact: %v", err)
	}
	if reopenedDetail.Revision != detail.Revision || reopenedDetail.Version != detail.Version || reopenedDetail.Record.Summary != detail.Record.Summary {
		t.Fatalf("reopened detail = %#v, want %#v", reopenedDetail, detail)
	}
}

func TestProjectFactChangesRejectNonClosedShapes(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "pentest.db")
	db, err := store.Open(path)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	projects := project.NewService(db)
	createdProject, err := projects.Create("Closed Shape", "", project.Scope{}, project.Defaults{})
	if err != nil {
		t.Fatalf("create project: %v", err)
	}
	service := blackboardv2.NewService(db)

	t.Run("create rejects fields outside create shape", func(t *testing.T) {
		_, err := service.Apply(ctx, createdProject.ID, blackboardv2.ChangeBatch{
			Schema:         "semantic-change-batch/v2",
			IdempotencyKey: "create-with-version",
			Changes: []blackboardv2.Change{{
				Op:      "create",
				Key:     "fact:shape-create",
				Version: 1,
				Type:    "fact",
				Record: blackboardv2.FactRecord{
					Category: "asset", Summary: "Create must be closed", Confidence: "tentative", ScopeStatus: "in_scope",
				},
			}},
		})
		var semanticErr *blackboardv2.Error
		if !errors.As(err, &semanticErr) || semanticErr.Code != "semantic_validation" || semanticErr.Path != "changes[0].version" {
			t.Fatalf("create with forbidden version error = %#v, want semantic_validation on changes[0].version", err)
		}
	})

	t.Run("update rejects missing partial record", func(t *testing.T) {
		created, err := service.Apply(ctx, createdProject.ID, blackboardv2.ChangeBatch{
			Schema:         "semantic-change-batch/v2",
			IdempotencyKey: "create-shape-update-target",
			Changes: []blackboardv2.Change{{
				Op:   "create",
				Key:  "fact:shape-update",
				Type: "fact",
				Record: blackboardv2.FactRecord{
					Category: "asset", Summary: "Update target", Confidence: "tentative", ScopeStatus: "in_scope",
				},
			}},
		})
		if err != nil {
			t.Fatalf("create update target: %v", err)
		}
		if created.Revision != 1 {
			t.Fatalf("created revision = %d, want 1", created.Revision)
		}
		_, err = service.Apply(ctx, createdProject.ID, blackboardv2.ChangeBatch{
			Schema:         "semantic-change-batch/v2",
			IdempotencyKey: "update-without-record",
			Changes: []blackboardv2.Change{{
				Op:      "update",
				Key:     "fact:shape-update",
				Version: 1,
				Type:    "fact",
			}},
		})
		var semanticErr *blackboardv2.Error
		if !errors.As(err, &semanticErr) || semanticErr.Code != "semantic_validation" || semanticErr.Path != "changes[0].record" {
			t.Fatalf("update without record error = %#v, want semantic_validation on changes[0].record", err)
		}
	})

	t.Run("json decode rejects unknown top-level and record fields", func(t *testing.T) {
		for _, raw := range []string{
			`{"op":"create","key":"fact:json","type":"fact","record":{"category":"asset","summary":"Unknown top-level","confidence":"tentative","scope_status":"in_scope"},"unexpected":true}`,
			`{"op":"create","key":"fact:json","type":"fact","record":{"category":"asset","summary":"Unknown record","confidence":"tentative","scope_status":"in_scope","internal_id":"leak"}}`,
		} {
			var change blackboardv2.Change
			if err := json.Unmarshal([]byte(raw), &change); err == nil {
				t.Fatalf("decoded non-closed change: %s", raw)
			}
		}
	})
}

func TestEntityAndFactUpdatesRequireNonEmptyPartialDTOs(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(filepath.Join(t.TempDir(), "pentest.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	createdProject, err := project.NewService(db).Create("Closed Knowledge Update DTOs", "", project.Scope{}, project.Defaults{})
	if err != nil {
		t.Fatalf("create project: %v", err)
	}
	service := blackboardv2.NewService(db)
	_, err = service.Apply(ctx, createdProject.ID, blackboardv2.ChangeBatch{
		Schema:         "semantic-change-batch/v2",
		IdempotencyKey: "seed-closed-knowledge-update-dtos",
		Changes: []blackboardv2.Change{
			{Op: "create", Key: "entity:record-value", Type: "entity", Record: blackboardv2.EntityRecord{Status: "active", Kind: "host", Name: "value.example", ScopeStatus: "in_scope"}},
			{Op: "create", Key: "entity:record-pointer", Type: "entity", Record: blackboardv2.EntityRecord{Status: "active", Kind: "host", Name: "pointer.example", ScopeStatus: "in_scope"}},
			{Op: "create", Key: "entity:empty-value", Type: "entity", Record: blackboardv2.EntityRecord{Status: "active", Kind: "host", Name: "empty-value.example", ScopeStatus: "in_scope"}},
			{Op: "create", Key: "entity:empty-pointer", Type: "entity", Record: blackboardv2.EntityRecord{Status: "active", Kind: "host", Name: "empty-pointer.example", ScopeStatus: "in_scope"}},
			{Op: "create", Key: "fact:record-value", Type: "fact", Record: blackboardv2.FactRecord{Category: "asset", Summary: "Reject complete Fact value", Confidence: "tentative", ScopeStatus: "in_scope"}},
			{Op: "create", Key: "fact:record-pointer", Type: "fact", Record: blackboardv2.FactRecord{Category: "asset", Summary: "Reject complete Fact pointer", Confidence: "tentative", ScopeStatus: "in_scope"}},
			{Op: "create", Key: "fact:empty-value", Type: "fact", Record: blackboardv2.FactRecord{Category: "asset", Summary: "Reject empty Fact value", Confidence: "tentative", ScopeStatus: "in_scope"}},
			{Op: "create", Key: "fact:empty-pointer", Type: "fact", Record: blackboardv2.FactRecord{Category: "asset", Summary: "Reject empty Fact pointer", Confidence: "tentative", ScopeStatus: "in_scope"}},
			{Op: "create", Key: "fact:confidence-patch", Type: "fact", Record: blackboardv2.FactRecord{Category: "asset", Summary: "Reject confidence Fact patch", Confidence: "tentative", ScopeStatus: "in_scope"}},
			{Op: "create", Key: "entity:valid-patch", Type: "entity", Record: blackboardv2.EntityRecord{Status: "active", Kind: "host", Name: "valid.example", ScopeStatus: "unknown"}},
			{Op: "create", Key: "fact:valid-patch", Type: "fact", Record: blackboardv2.FactRecord{Category: "asset", Summary: "Accept valid Fact patch", Confidence: "tentative", ScopeStatus: "unknown"}},
		},
	})
	if err != nil {
		t.Fatalf("seed closed knowledge update DTOs: %v", err)
	}

	entityRecordPointer := &blackboardv2.EntityRecord{Status: "active", Kind: "host", Name: "rejected.example", ScopeStatus: "in_scope"}
	factRecordPointer := &blackboardv2.FactRecord{Category: "asset", Summary: "Rejected complete Fact pointer", Confidence: "tentative", ScopeStatus: "in_scope"}
	emptyEntityPatch := &blackboardv2.EntityPatch{}
	emptyFactPatch := &blackboardv2.FactPatch{}
	for index, tt := range []struct {
		name   string
		change blackboardv2.Change
	}{
		{name: "Entity record value", change: blackboardv2.Change{Op: "update", Key: "entity:record-value", Version: 1, Type: "entity", Record: blackboardv2.EntityRecord{Status: "active", Kind: "host", Name: "rejected-value.example", ScopeStatus: "in_scope"}}},
		{name: "Entity record pointer", change: blackboardv2.Change{Op: "update", Key: "entity:record-pointer", Version: 1, Type: "entity", Record: entityRecordPointer}},
		{name: "empty Entity patch value", change: blackboardv2.Change{Op: "update", Key: "entity:empty-value", Version: 1, Type: "entity", Record: blackboardv2.EntityPatch{}}},
		{name: "empty Entity patch pointer", change: blackboardv2.Change{Op: "update", Key: "entity:empty-pointer", Version: 1, Type: "entity", Record: emptyEntityPatch}},
		{name: "Fact record value", change: blackboardv2.Change{Op: "update", Key: "fact:record-value", Version: 1, Type: "fact", Record: blackboardv2.FactRecord{Category: "asset", Summary: "Rejected complete Fact value", Confidence: "tentative", ScopeStatus: "in_scope"}}},
		{name: "Fact record pointer", change: blackboardv2.Change{Op: "update", Key: "fact:record-pointer", Version: 1, Type: "fact", Record: factRecordPointer}},
		{name: "empty Fact patch value", change: blackboardv2.Change{Op: "update", Key: "fact:empty-value", Version: 1, Type: "fact", Record: blackboardv2.FactPatch{}}},
		{name: "empty Fact patch pointer", change: blackboardv2.Change{Op: "update", Key: "fact:empty-pointer", Version: 1, Type: "fact", Record: emptyFactPatch}},
		{name: "Fact confidence patch", change: blackboardv2.Change{Op: "update", Key: "fact:confidence-patch", Version: 1, Type: "fact", Record: blackboardv2.FactPatch{Confidence: strPtr("confirmed")}}},
	} {
		t.Run("rejects "+tt.name, func(t *testing.T) {
			_, err := service.Apply(ctx, createdProject.ID, blackboardv2.ChangeBatch{
				Schema:         "semantic-change-batch/v2",
				IdempotencyKey: "reject-knowledge-update-shape-" + strconv.Itoa(index),
				Changes:        []blackboardv2.Change{tt.change},
			})
			if !isSemanticCode(err, "semantic_validation") {
				t.Fatalf("closed knowledge update error = %#v, want semantic_validation", err)
			}
		})
	}
	for _, raw := range []string{
		`{"op":"update","key":"entity:empty-value","version":1,"type":"entity","record":{}}`,
		`{"op":"update","key":"fact:empty-value","version":1,"type":"fact","record":{}}`,
		`{"op":"update","key":"fact:confidence-patch","version":1,"type":"fact","record":{"confidence":"confirmed"}}`,
	} {
		var change blackboardv2.Change
		if err := json.Unmarshal([]byte(raw), &change); err == nil {
			t.Fatalf("decoded non-closed knowledge partial: %s", raw)
		}
	}

	valid, err := service.Apply(ctx, createdProject.ID, blackboardv2.ChangeBatch{
		Schema:         "semantic-change-batch/v2",
		IdempotencyKey: "accept-knowledge-partial-dtos",
		Changes: []blackboardv2.Change{
			{Op: "update", Key: "entity:valid-patch", Version: 1, Type: "entity", Record: &blackboardv2.EntityPatch{ScopeStatus: strPtr("in_scope")}},
			{Op: "update", Key: "fact:valid-patch", Version: 1, Type: "fact", Record: &blackboardv2.FactPatch{ScopeStatus: strPtr("in_scope")}},
		},
	})
	if err != nil {
		t.Fatalf("apply valid knowledge partial DTOs: %v", err)
	}
	assertChangeResult(t, valid, 13, [][]any{{"entity:valid-patch", float64(2)}, {"fact:valid-patch", float64(2)}})
}

func TestFactConfidenceChangesUseCanonicalTransitions(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(filepath.Join(t.TempDir(), "pentest.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	createdProject, err := project.NewService(db).Create("Fact Confidence Transitions", "", project.Scope{}, project.Defaults{})
	if err != nil {
		t.Fatalf("create project: %v", err)
	}
	service := blackboardv2.NewService(db)
	harness := mustHarness(t)
	_, err = service.Apply(ctx, createdProject.ID, blackboardv2.ChangeBatch{
		Schema:         "semantic-change-batch/v2",
		IdempotencyKey: "create-transitioned-fact",
		Changes: []blackboardv2.Change{{
			Op: "create", Key: "fact:login-confidence", Type: "fact", Record: blackboardv2.FactRecord{Category: "authentication", Summary: "The login endpoint accepts JSON", Confidence: "tentative", ScopeStatus: "in_scope"},
		}},
	})
	if err != nil {
		t.Fatalf("create transitioned Fact: %v", err)
	}

	confirmedBatch := blackboardv2.ChangeBatch{
		Schema:         "semantic-change-batch/v2",
		IdempotencyKey: "confirm-fact",
		Changes:        []blackboardv2.Change{{Op: "transition", Key: "fact:login-confidence", Version: 1, Status: "confirmed"}},
	}
	assertContractJSON(t, harness, "changeBatch", confirmedBatch)
	confirmed, err := service.Apply(ctx, createdProject.ID, confirmedBatch)
	if err != nil {
		t.Fatalf("confirm Fact: %v", err)
	}
	assertChangeResult(t, confirmed, 2, [][]any{{"fact:login-confidence", float64(2)}})
	tentativeBatch := blackboardv2.ChangeBatch{
		Schema:         "semantic-change-batch/v2",
		IdempotencyKey: "downgrade-fact",
		Changes:        []blackboardv2.Change{{Op: "transition", Key: "fact:login-confidence", Version: 2, Status: "tentative"}},
	}
	assertContractJSON(t, harness, "changeBatch", tentativeBatch)
	tentative, err := service.Apply(ctx, createdProject.ID, tentativeBatch)
	if err != nil {
		t.Fatalf("downgrade Fact: %v", err)
	}
	assertChangeResult(t, tentative, 3, [][]any{{"fact:login-confidence", float64(3)}})
	detail, err := service.ReadCurrent(ctx, createdProject.ID, "fact:login-confidence")
	if err != nil {
		t.Fatalf("read transitioned Fact: %v", err)
	}
	if detail.Version != 3 || detail.Record.Confidence != "tentative" {
		t.Fatalf("transitioned Fact detail = %#v", detail)
	}
	history, err := service.ReadHistory(ctx, createdProject.ID, "fact:login-confidence", blackboardv2.HistoryOptions{})
	if err != nil {
		t.Fatalf("read transitioned Fact history: %v", err)
	}
	if len(history.Items) != 2 || history.Items[0].Version != 1 || history.Items[0].Record.Confidence != "tentative" || history.Items[1].Version != 2 || history.Items[1].Record.Confidence != "confirmed" {
		t.Fatalf("transitioned Fact history = %#v", history.Items)
	}
	assertContractJSON(t, harness, "semanticHistory", history)
}

func TestClosedProgrammaticShapeValidationPrecedesIdempotencyReplay(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(filepath.Join(t.TempDir(), "pentest.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	createdProject, err := project.NewService(db).Create("Shape Before Replay", "", project.Scope{}, project.Defaults{})
	if err != nil {
		t.Fatalf("create project: %v", err)
	}
	service := blackboardv2.NewService(db)
	_, err = service.Apply(ctx, createdProject.ID, blackboardv2.ChangeBatch{
		Schema:         "semantic-change-batch/v2",
		IdempotencyKey: "create-shape-replay-fact",
		Changes: []blackboardv2.Change{{
			Op: "create", Key: "fact:shape-replay", Type: "fact", Record: blackboardv2.FactRecord{Category: "asset", Summary: "Original summary", Confidence: "tentative", ScopeStatus: "in_scope"},
		}},
	})
	if err != nil {
		t.Fatalf("create shape replay Fact: %v", err)
	}

	valid := blackboardv2.ChangeBatch{
		Schema:         "semantic-change-batch/v2",
		IdempotencyKey: "shape-replay-update",
		Changes: []blackboardv2.Change{{
			Op: "update", Key: "fact:shape-replay", Version: 1, Type: "fact", Record: blackboardv2.FactPatch{Summary: strPtr("Updated summary")},
		}},
	}
	first, err := service.Apply(ctx, createdProject.ID, valid)
	if err != nil {
		t.Fatalf("apply valid shape receipt: %v", err)
	}
	replay, err := service.Apply(ctx, createdProject.ID, valid)
	if err != nil || !reflect.DeepEqual(replay, first) {
		t.Fatalf("exact valid replay = %#v, %v, want %#v", replay, err, first)
	}

	forbidden := valid
	forbidden.Changes = append([]blackboardv2.Change(nil), valid.Changes...)
	forbidden.Changes[0].Record = blackboardv2.FactPatch{Summary: strPtr("Updated summary"), Confidence: strPtr("confirmed")}
	_, err = service.Apply(ctx, createdProject.ID, forbidden)
	var semanticErr *blackboardv2.Error
	if !errors.As(err, &semanticErr) || semanticErr.Code != "semantic_validation" || semanticErr.Path != "changes[0].record.confidence" {
		t.Fatalf("forbidden shape replay error = %#v, want semantic_validation before replay", err)
	}
}

func TestRuntimeFactConfirmationRequiresAcceptedImplementedBasis(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(filepath.Join(t.TempDir(), "pentest.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	projects := project.NewService(db)
	alpha, err := projects.Create("Runtime Fact Confirmation", "", project.Scope{}, project.Defaults{})
	if err != nil {
		t.Fatalf("create alpha project: %v", err)
	}
	beta, err := projects.Create("Foreign Fact Confirmation", "", project.Scope{}, project.Defaults{})
	if err != nil {
		t.Fatalf("create beta project: %v", err)
	}
	tasks := task.NewService(db, projects)
	ownerTask, err := tasks.Create(task.CreateRequest{ProjectID: alpha.ID, Goal: "Owner confirmation", Runner: task.RunnerSandbox})
	if err != nil {
		t.Fatalf("create owner Task: %v", err)
	}
	owner, err := tasks.CreateContinuation(ownerTask.ID, "profile-owner-confirm", "codex", task.RunnerSandbox)
	if err != nil {
		t.Fatalf("create owner Continuation: %v", err)
	}
	peerTask, err := tasks.Create(task.CreateRequest{ProjectID: alpha.ID, Goal: "Peer confirmation", Runner: task.RunnerSandbox})
	if err != nil {
		t.Fatalf("create peer Task: %v", err)
	}
	peer, err := tasks.CreateContinuation(peerTask.ID, "profile-peer-confirm", "codex", task.RunnerSandbox)
	if err != nil {
		t.Fatalf("create peer Continuation: %v", err)
	}
	foreignTask, err := tasks.Create(task.CreateRequest{ProjectID: beta.ID, Goal: "Foreign confirmation", Runner: task.RunnerSandbox})
	if err != nil {
		t.Fatalf("create foreign Task: %v", err)
	}
	foreign, err := tasks.CreateContinuation(foreignTask.ID, "profile-foreign-confirm", "codex", task.RunnerSandbox)
	if err != nil {
		t.Fatalf("create foreign Continuation: %v", err)
	}
	service := blackboardv2.NewService(db)

	_, err = service.ApplyForContinuation(ctx, alpha.ID, owner.ID, blackboardv2.ChangeBatch{
		Schema:         "semantic-change-batch/v2",
		IdempotencyKey: "create-unsupported-runtime-fact",
		Changes: []blackboardv2.Change{{
			Op: "create", Key: "fact:unsupported-runtime", Type: "fact", Record: blackboardv2.FactRecord{Category: "authentication", Summary: "Unsupported Runtime conclusion", Confidence: "tentative", ScopeStatus: "in_scope"},
		}},
	})
	if err != nil {
		t.Fatalf("create unsupported Runtime Fact: %v", err)
	}
	_, err = service.ApplyForContinuation(ctx, alpha.ID, owner.ID, blackboardv2.ChangeBatch{
		Schema:         "semantic-change-batch/v2",
		IdempotencyKey: "reject-unsupported-runtime-confirmation",
		Changes:        []blackboardv2.Change{{Op: "transition", Key: "fact:unsupported-runtime", Version: 1, Status: "confirmed"}},
	})
	if !isSemanticCode(err, "semantic_validation") {
		t.Fatalf("unsupported Runtime confirmation error = %#v, want semantic_validation", err)
	}

	_, err = service.Apply(ctx, alpha.ID, blackboardv2.ChangeBatch{
		Schema:         "semantic-change-batch/v2",
		IdempotencyKey: "trusted-operator-confirmation-basis",
		Changes: []blackboardv2.Change{
			{Op: "create", Key: "fact:operator-confirmed", Type: "fact", Record: blackboardv2.FactRecord{Category: "asset", Summary: "Operator-confirmed asset conclusion", Confidence: "tentative", ScopeStatus: "in_scope"}},
			{Op: "transition", Key: "fact:operator-confirmed", Version: 1, Status: "confirmed"},
		},
	})
	if err != nil {
		t.Fatalf("trusted operator confirmation: %v", err)
	}

	_, err = service.Apply(ctx, alpha.ID, blackboardv2.ChangeBatch{
		Schema:         "semantic-change-batch/v2",
		IdempotencyKey: "create-confirmed-support",
		Changes: []blackboardv2.Change{{
			Op: "create", Key: "fact:confirmed-support", Type: "fact", Record: blackboardv2.FactRecord{Category: "authentication", Summary: "Independent confirmed reproduction", Confidence: "confirmed", ScopeStatus: "in_scope"},
		}},
	})
	if err != nil {
		t.Fatalf("create confirmed supporting Fact: %v", err)
	}
	_, err = service.ApplyForContinuation(ctx, alpha.ID, owner.ID, blackboardv2.ChangeBatch{
		Schema:         "semantic-change-batch/v2",
		IdempotencyKey: "confirm-with-supporting-fact",
		Changes: []blackboardv2.Change{
			{Op: "create", Key: "fact:supported-runtime", Type: "fact", Record: blackboardv2.FactRecord{Category: "authentication", Summary: "Runtime conclusion with confirmed support", Confidence: "tentative", ScopeStatus: "in_scope"}},
			{Op: "relate", From: "fact:confirmed-support", Relation: "supports", To: "fact:supported-runtime", Reason: "Independent confirmed reproduction"},
			{Op: "transition", Key: "fact:supported-runtime", Version: 1, Status: "confirmed"},
		},
	})
	if err != nil {
		t.Fatalf("Runtime confirmation with supporting Fact: %v", err)
	}

	_, err = service.Apply(ctx, beta.ID, blackboardv2.ChangeBatch{
		Schema:         "semantic-change-batch/v2",
		IdempotencyKey: "create-foreign-confirmed-support",
		Changes: []blackboardv2.Change{{
			Op: "create", Key: "fact:foreign-support", Type: "fact", Record: blackboardv2.FactRecord{Category: "asset", Summary: "Foreign confirmed support", Confidence: "confirmed", ScopeStatus: "in_scope"},
		}},
	})
	if err != nil {
		t.Fatalf("create foreign confirmed support: %v", err)
	}
	_, err = service.ApplyForContinuation(ctx, alpha.ID, owner.ID, blackboardv2.ChangeBatch{
		Schema:         "semantic-change-batch/v2",
		IdempotencyKey: "create-project-isolated-fact",
		Changes: []blackboardv2.Change{{
			Op: "create", Key: "fact:project-isolated", Type: "fact", Record: blackboardv2.FactRecord{Category: "asset", Summary: "Project-isolated conclusion", Confidence: "tentative", ScopeStatus: "in_scope"},
		}},
	})
	if err != nil {
		t.Fatalf("create project-isolated Fact: %v", err)
	}
	_, err = service.ApplyForContinuation(ctx, alpha.ID, owner.ID, blackboardv2.ChangeBatch{
		Schema:         "semantic-change-batch/v2",
		IdempotencyKey: "reject-foreign-support-confirmation",
		Changes:        []blackboardv2.Change{{Op: "transition", Key: "fact:project-isolated", Version: 1, Status: "confirmed"}},
	})
	if !isSemanticCode(err, "semantic_validation") {
		t.Fatalf("foreign support confirmation error = %#v, want semantic_validation", err)
	}
	if _, err := service.ApplyForContinuation(ctx, alpha.ID, foreign.ID, blackboardv2.ChangeBatch{
		Schema:         "semantic-change-batch/v2",
		IdempotencyKey: "reject-foreign-continuation-confirmation",
		Changes:        []blackboardv2.Change{{Op: "transition", Key: "fact:project-isolated", Version: 1, Status: "confirmed"}},
	}); !isSemanticCode(err, "authority_denied") {
		t.Fatalf("foreign Continuation confirmation error = %#v, want authority_denied", err)
	}

	_, err = service.ApplyForContinuation(ctx, alpha.ID, owner.ID, blackboardv2.ChangeBatch{
		Schema:         "semantic-change-batch/v2",
		IdempotencyKey: "owner-succeeded-producing-attempt",
		Changes: []blackboardv2.Change{
			{Op: "create", Key: "objective:owner-produced", Type: "objective", Record: blackboardv2.ObjectiveRecord{Status: "open", Objective: "Establish an owner-produced conclusion"}},
			{Op: "create", Key: "attempt:owner-produced", Type: "attempt", Record: blackboardv2.AttemptRecord{Status: "open", Summary: "Testing owner-produced conclusion"}},
			{Op: "create", Key: "fact:owner-produced", Type: "fact", Record: blackboardv2.FactRecord{Category: "authentication", Summary: "Owner-produced tentative conclusion", Confidence: "tentative", ScopeStatus: "in_scope"}},
			{Op: "relate", From: "attempt:owner-produced", Relation: "tests", To: "objective:owner-produced"},
			{Op: "relate", From: "attempt:owner-produced", Relation: "produced", To: "fact:owner-produced"},
			{Op: "transition", Key: "attempt:owner-produced", Version: 1, Status: "succeeded", Summary: "Owner established the produced conclusion"},
		},
	})
	if err != nil {
		t.Fatalf("create owner succeeded producing Attempt: %v", err)
	}
	_, err = service.ApplyForContinuation(ctx, alpha.ID, owner.ID, blackboardv2.ChangeBatch{
		Schema:         "semantic-change-batch/v2",
		IdempotencyKey: "confirm-owner-produced-fact",
		Changes:        []blackboardv2.Change{{Op: "transition", Key: "fact:owner-produced", Version: 1, Status: "confirmed"}},
	})
	if err != nil {
		t.Fatalf("confirm owner-produced Fact: %v", err)
	}

	_, err = service.ApplyForContinuation(ctx, alpha.ID, peer.ID, blackboardv2.ChangeBatch{
		Schema:         "semantic-change-batch/v2",
		IdempotencyKey: "peer-succeeded-producing-attempt",
		Changes: []blackboardv2.Change{
			{Op: "create", Key: "objective:peer-produced", Type: "objective", Record: blackboardv2.ObjectiveRecord{Status: "open", Objective: "Establish a peer-produced conclusion"}},
			{Op: "create", Key: "attempt:peer-produced", Type: "attempt", Record: blackboardv2.AttemptRecord{Status: "open", Summary: "Testing peer-produced conclusion"}},
			{Op: "create", Key: "fact:peer-produced", Type: "fact", Record: blackboardv2.FactRecord{Category: "authentication", Summary: "Peer-produced tentative conclusion", Confidence: "tentative", ScopeStatus: "in_scope"}},
			{Op: "relate", From: "attempt:peer-produced", Relation: "tests", To: "objective:peer-produced"},
			{Op: "relate", From: "attempt:peer-produced", Relation: "produced", To: "fact:peer-produced"},
			{Op: "transition", Key: "attempt:peer-produced", Version: 1, Status: "succeeded", Summary: "Peer established the produced conclusion"},
		},
	})
	if err != nil {
		t.Fatalf("create peer succeeded producing Attempt: %v", err)
	}
	_, err = service.ApplyForContinuation(ctx, alpha.ID, owner.ID, blackboardv2.ChangeBatch{
		Schema:         "semantic-change-batch/v2",
		IdempotencyKey: "reject-peer-produced-owner-confirmation",
		Changes:        []blackboardv2.Change{{Op: "transition", Key: "fact:peer-produced", Version: 1, Status: "confirmed"}},
	})
	if !isSemanticCode(err, "semantic_validation") {
		t.Fatalf("peer-produced owner confirmation error = %#v, want semantic_validation", err)
	}
	_, err = service.ApplyForContinuation(ctx, alpha.ID, peer.ID, blackboardv2.ChangeBatch{
		Schema:         "semantic-change-batch/v2",
		IdempotencyKey: "confirm-peer-produced-fact",
		Changes:        []blackboardv2.Change{{Op: "transition", Key: "fact:peer-produced", Version: 1, Status: "confirmed"}},
	})
	if err != nil {
		t.Fatalf("confirm peer-produced Fact by peer: %v", err)
	}
}

func TestRuntimeConfirmedFactCreateValidatesFinalBatchBasis(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(filepath.Join(t.TempDir(), "pentest.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	projects := project.NewService(db)
	alpha, err := projects.Create("Runtime Confirmed Fact Create", "", project.Scope{}, project.Defaults{})
	if err != nil {
		t.Fatalf("create alpha project: %v", err)
	}
	beta, err := projects.Create("Foreign Confirmed Fact Basis", "", project.Scope{}, project.Defaults{})
	if err != nil {
		t.Fatalf("create beta project: %v", err)
	}
	tasks := task.NewService(db, projects)
	createdTask, err := tasks.Create(task.CreateRequest{ProjectID: alpha.ID, Goal: "Create confirmed Facts", Runner: task.RunnerSandbox})
	if err != nil {
		t.Fatalf("create Task: %v", err)
	}
	continuation, err := tasks.CreateContinuation(createdTask.ID, "profile-confirm-create", "codex", task.RunnerSandbox)
	if err != nil {
		t.Fatalf("create Continuation: %v", err)
	}
	service := blackboardv2.NewService(db)

	_, err = service.ApplyForContinuation(ctx, alpha.ID, continuation.ID, blackboardv2.ChangeBatch{
		Schema:         "semantic-change-batch/v2",
		IdempotencyKey: "reject-unsupported-confirmed-create",
		Changes: []blackboardv2.Change{
			{Op: "create", Key: "entity:must-roll-back", Type: "entity", Record: blackboardv2.EntityRecord{Status: "active", Kind: "host", Name: "rollback.example", ScopeStatus: "in_scope"}},
			{Op: "create", Key: "fact:unsupported-confirmed-create", Type: "fact", Record: blackboardv2.FactRecord{Category: "asset", Summary: "Unsupported confirmed Runtime Fact", Confidence: "confirmed", ScopeStatus: "in_scope"}},
		},
	})
	if !isSemanticCode(err, "semantic_validation") {
		t.Fatalf("unsupported confirmed create error = %#v, want semantic_validation", err)
	}
	for _, key := range []string{"entity:must-roll-back", "fact:unsupported-confirmed-create"} {
		if _, err := service.ReadCurrent(ctx, alpha.ID, key); !isSemanticCode(err, "not_found") {
			t.Fatalf("unsupported confirmed create retained %s: %#v", key, err)
		}
	}

	if _, err := service.Apply(ctx, alpha.ID, blackboardv2.ChangeBatch{
		Schema:         "semantic-change-batch/v2",
		IdempotencyKey: "operator-confirmed-create",
		Changes: []blackboardv2.Change{{
			Op: "create", Key: "fact:operator-confirmed-create", Type: "fact", Record: blackboardv2.FactRecord{Category: "asset", Summary: "Operator-confirmed Fact", Confidence: "confirmed", ScopeStatus: "in_scope"},
		}},
	}); err != nil {
		t.Fatalf("trusted operator confirmed create: %v", err)
	}
	if _, err := service.Apply(ctx, alpha.ID, blackboardv2.ChangeBatch{
		Schema:         "semantic-change-batch/v2",
		IdempotencyKey: "create-final-batch-support-source",
		Changes: []blackboardv2.Change{{
			Op: "create", Key: "fact:create-support-source", Type: "fact", Record: blackboardv2.FactRecord{Category: "authentication", Summary: "Confirmed supporting conclusion", Confidence: "confirmed", ScopeStatus: "in_scope"},
		}},
	}); err != nil {
		t.Fatalf("create final-batch support source: %v", err)
	}

	_, err = service.ApplyForContinuation(ctx, alpha.ID, continuation.ID, blackboardv2.ChangeBatch{
		Schema:         "semantic-change-batch/v2",
		IdempotencyKey: "confirmed-create-with-later-support",
		Changes: []blackboardv2.Change{
			{Op: "create", Key: "fact:confirmed-create-supported", Type: "fact", Record: blackboardv2.FactRecord{Category: "authentication", Summary: "Confirmed Runtime Fact with support", Confidence: "confirmed", ScopeStatus: "in_scope"}},
			{Op: "relate", From: "fact:create-support-source", Relation: "supports", To: "fact:confirmed-create-supported", Reason: "Support added after confirmed create"},
		},
	})
	if err != nil {
		t.Fatalf("confirmed create with later support: %v", err)
	}

	_, err = service.ApplyForContinuation(ctx, alpha.ID, continuation.ID, blackboardv2.ChangeBatch{
		Schema:         "semantic-change-batch/v2",
		IdempotencyKey: "confirmed-create-with-producing-attempt",
		Changes: []blackboardv2.Change{
			{Op: "create", Key: "objective:confirmed-create", Type: "objective", Record: blackboardv2.ObjectiveRecord{Status: "open", Objective: "Establish a directly confirmed Fact"}},
			{Op: "create", Key: "attempt:confirmed-create", Type: "attempt", Record: blackboardv2.AttemptRecord{Status: "open", Summary: "Testing the directly confirmed conclusion"}},
			{Op: "create", Key: "fact:confirmed-create-produced", Type: "fact", Record: blackboardv2.FactRecord{Category: "authentication", Summary: "Directly confirmed produced conclusion", Confidence: "confirmed", ScopeStatus: "in_scope"}},
			{Op: "relate", From: "attempt:confirmed-create", Relation: "tests", To: "objective:confirmed-create"},
			{Op: "relate", From: "attempt:confirmed-create", Relation: "produced", To: "fact:confirmed-create-produced"},
			{Op: "transition", Key: "attempt:confirmed-create", Version: 1, Status: "succeeded", Summary: "The producing Attempt established the conclusion"},
		},
	})
	if err != nil {
		t.Fatalf("confirmed create with same-batch producing Attempt: %v", err)
	}

	if _, err := service.Apply(ctx, beta.ID, blackboardv2.ChangeBatch{
		Schema:         "semantic-change-batch/v2",
		IdempotencyKey: "foreign-confirmed-create-support",
		Changes: []blackboardv2.Change{{
			Op: "create", Key: "fact:foreign-create-support", Type: "fact", Record: blackboardv2.FactRecord{Category: "asset", Summary: "Foreign confirmed support", Confidence: "confirmed", ScopeStatus: "in_scope"},
		}},
	}); err != nil {
		t.Fatalf("create foreign confirmed support: %v", err)
	}
	_, err = service.ApplyForContinuation(ctx, alpha.ID, continuation.ID, blackboardv2.ChangeBatch{
		Schema:         "semantic-change-batch/v2",
		IdempotencyKey: "reject-cross-project-confirmed-create-basis",
		Changes: []blackboardv2.Change{{
			Op: "create", Key: "fact:cross-project-confirmed-create", Type: "fact", Record: blackboardv2.FactRecord{Category: "asset", Summary: "Cross-Project unsupported confirmed Fact", Confidence: "confirmed", ScopeStatus: "in_scope"},
		}},
	})
	if !isSemanticCode(err, "semantic_validation") {
		t.Fatalf("cross-Project confirmed create error = %#v, want semantic_validation", err)
	}
}

func TestFactSupportsSubgraphIsAcyclic(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(filepath.Join(t.TempDir(), "pentest.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	createdProject, err := project.NewService(db).Create("Acyclic Fact Support", "", project.Scope{}, project.Defaults{})
	if err != nil {
		t.Fatalf("create project: %v", err)
	}
	service := blackboardv2.NewService(db)
	_, err = service.Apply(ctx, createdProject.ID, blackboardv2.ChangeBatch{
		Schema:         "semantic-change-batch/v2",
		IdempotencyKey: "seed-fact-support-dag",
		Changes: []blackboardv2.Change{
			{Op: "create", Key: "fact:support-a", Type: "fact", Record: blackboardv2.FactRecord{Category: "authentication", Summary: "Support fact A", Confidence: "confirmed", ScopeStatus: "in_scope"}},
			{Op: "create", Key: "fact:support-b", Type: "fact", Record: blackboardv2.FactRecord{Category: "authentication", Summary: "Support fact B", Confidence: "confirmed", ScopeStatus: "in_scope"}},
			{Op: "create", Key: "fact:support-c", Type: "fact", Record: blackboardv2.FactRecord{Category: "authentication", Summary: "Support fact C", Confidence: "tentative", ScopeStatus: "in_scope"}},
			{Op: "relate", From: "fact:support-a", Relation: "supports", To: "fact:support-b", Reason: "A supports B"},
		},
	})
	if err != nil {
		t.Fatalf("seed Fact support DAG: %v", err)
	}

	noOp, err := service.Apply(ctx, createdProject.ID, blackboardv2.ChangeBatch{
		Schema:         "semantic-change-batch/v2",
		IdempotencyKey: "exact-support-edge-noop",
		Changes: []blackboardv2.Change{{
			Op: "relate", From: "fact:support-a", Relation: "supports", To: "fact:support-b", Reason: "A supports B",
		}},
	})
	if err != nil {
		t.Fatalf("exact existing supports no-op: %v", err)
	}
	assertChangeResult(t, noOp, 4, nil)

	_, err = service.Apply(ctx, createdProject.ID, blackboardv2.ChangeBatch{
		Schema:         "semantic-change-batch/v2",
		IdempotencyKey: "reject-reciprocal-support-cycle",
		Changes: []blackboardv2.Change{{
			Op: "relate", From: "fact:support-b", Relation: "supports", To: "fact:support-a", Reason: "B must not support A",
		}},
	})
	if !isSemanticCode(err, "semantic_validation") {
		t.Fatalf("reciprocal supports cycle error = %#v, want semantic_validation", err)
	}
	if _, err := service.Apply(ctx, createdProject.ID, blackboardv2.ChangeBatch{
		Schema:         "semantic-change-batch/v2",
		IdempotencyKey: "extend-support-dag",
		Changes: []blackboardv2.Change{{
			Op: "relate", From: "fact:support-b", Relation: "supports", To: "fact:support-c", Reason: "B supports C",
		}},
	}); err != nil {
		t.Fatalf("extend Fact support DAG: %v", err)
	}
	_, err = service.Apply(ctx, createdProject.ID, blackboardv2.ChangeBatch{
		Schema:         "semantic-change-batch/v2",
		IdempotencyKey: "reject-long-support-cycle",
		Changes: []blackboardv2.Change{{
			Op: "relate", From: "fact:support-c", Relation: "supports", To: "fact:support-a", Reason: "C must not close the cycle",
		}},
	})
	if !isSemanticCode(err, "semantic_validation") {
		t.Fatalf("long supports cycle error = %#v, want semantic_validation", err)
	}

	_, err = service.Apply(ctx, createdProject.ID, blackboardv2.ChangeBatch{
		Schema:         "semantic-change-batch/v2",
		IdempotencyKey: "reject-same-batch-support-cycle",
		Changes: []blackboardv2.Change{
			{Op: "create", Key: "entity:support-cycle-marker", Type: "entity", Record: blackboardv2.EntityRecord{Status: "active", Kind: "host", Name: "must-roll-back.example", ScopeStatus: "in_scope"}},
			{Op: "create", Key: "fact:support-x", Type: "fact", Record: blackboardv2.FactRecord{Category: "asset", Summary: "Support fact X", Confidence: "tentative", ScopeStatus: "in_scope"}},
			{Op: "create", Key: "fact:support-y", Type: "fact", Record: blackboardv2.FactRecord{Category: "asset", Summary: "Support fact Y", Confidence: "tentative", ScopeStatus: "in_scope"}},
			{Op: "relate", From: "fact:support-x", Relation: "supports", To: "fact:support-y", Reason: "X supports Y"},
			{Op: "relate", From: "fact:support-y", Relation: "supports", To: "fact:support-x", Reason: "Y must not support X"},
		},
	})
	if !isSemanticCode(err, "semantic_validation") {
		t.Fatalf("same-batch supports cycle error = %#v, want semantic_validation", err)
	}
	for _, key := range []string{"entity:support-cycle-marker", "fact:support-x", "fact:support-y"} {
		if _, err := service.ReadCurrent(ctx, createdProject.ID, key); !isSemanticCode(err, "not_found") {
			t.Fatalf("same-batch supports cycle retained %s: %#v", key, err)
		}
	}

	updated, err := service.Apply(ctx, createdProject.ID, blackboardv2.ChangeBatch{
		Schema:         "semantic-change-batch/v2",
		IdempotencyKey: "update-acyclic-support-reason",
		Changes: []blackboardv2.Change{{
			Op: "relate", From: "fact:support-a", Relation: "supports", To: "fact:support-b", Version: 1, Reason: "A independently supports B",
		}},
	})
	if err != nil {
		t.Fatalf("update acyclic supports reason: %v", err)
	}
	assertChangeRecords(t, updated, 6, [][]any{})
	if len(updated.Relations) != 1 || updated.Relations[0][3] != 2 {
		t.Fatalf("updated supports result = %#v", updated.Relations)
	}
}

func mustHarness(t *testing.T) *blackboardv2contract.Harness {
	t.Helper()
	harness, err := blackboardv2contract.NewHarness()
	if err != nil {
		t.Fatalf("load v2 contract harness: %v", err)
	}
	return harness
}

func assertChangeResult(t *testing.T, got blackboardv2.ChangeResult, wantRevision int, wantRecords [][]any) {
	t.Helper()
	if got.Schema != "semantic-change-result/v2" || got.Revision != wantRevision || got.WorkingSnapshot.Path != ".pentest/blackboard.json" || got.WorkingSnapshot.Revision != wantRevision {
		t.Fatalf("change result = %#v, want revision %d and working snapshot", got, wantRevision)
	}
	gotRecords := mustTupleJSON(t, got.Records)
	if wantRecords == nil {
		wantRecords = [][]any{}
	}
	if !reflect.DeepEqual(gotRecords, wantRecords) {
		t.Fatalf("records = %#v, want %#v", gotRecords, wantRecords)
	}
	if got.Relations == nil || len(got.Relations) != 0 {
		t.Fatalf("relations = %#v, want empty array", got.Relations)
	}
}

func assertContractJSON(t *testing.T, harness *blackboardv2contract.Harness, schema string, value any) {
	t.Helper()
	raw := mustJSON(t, value)
	if err := harness.Validate(schema, raw); err != nil {
		t.Fatalf("%s contract validation failed: %v\n%s", schema, err, raw)
	}
}

func mustJSON(t *testing.T, value any) []byte {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("marshal JSON: %v", err)
	}
	var compact bytes.Buffer
	if err := json.Compact(&compact, raw); err != nil {
		t.Fatalf("compact JSON: %v", err)
	}
	return compact.Bytes()
}

func mustTupleJSON(t *testing.T, value any) [][]any {
	t.Helper()
	raw := mustJSON(t, value)
	var tuples [][]any
	if err := json.Unmarshal(raw, &tuples); err != nil {
		t.Fatalf("decode tuple JSON %s: %v", raw, err)
	}
	return tuples
}

func strPtr(value string) *string {
	return &value
}
