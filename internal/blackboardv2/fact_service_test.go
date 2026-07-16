package blackboardv2_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"pentest/internal/blackboardv2"
	"pentest/internal/blackboardv2contract"
	"pentest/internal/project"
	"pentest/internal/store"
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
			Record:  blackboardv2.FactPatch{},
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
