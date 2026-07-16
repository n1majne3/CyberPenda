package blackboardv2_test

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"pentest/internal/blackboardv2"
	"pentest/internal/project"
	"pentest/internal/store"
)

func TestChangeBatchEnvelopeMatchesFrozenContract(t *testing.T) {
	var empty blackboardv2.ChangeBatch
	if err := json.Unmarshal([]byte(`{"schema":"semantic-change-batch/v2","idempotency_key":"empty-batch","changes":[]}`), &empty); err != nil {
		t.Fatalf("decode valid empty ChangeBatch: %v", err)
	}
	if empty.Changes == nil || len(empty.Changes) != 0 {
		t.Fatalf("decoded changes = %#v, want non-nil empty slice", empty.Changes)
	}

	for _, tt := range []struct {
		name string
		raw  string
	}{
		{name: "unknown field", raw: `{"schema":"semantic-change-batch/v2","idempotency_key":"closed","changes":[],"project_id":"caller-owned"}`},
		{name: "missing schema", raw: `{"idempotency_key":"missing-schema","changes":[]}`},
		{name: "null schema", raw: `{"schema":null,"idempotency_key":"null-schema","changes":[]}`},
		{name: "wrong schema", raw: `{"schema":"semantic-change-batch/v1","idempotency_key":"wrong-schema","changes":[]}`},
		{name: "missing idempotency key", raw: `{"schema":"semantic-change-batch/v2","changes":[]}`},
		{name: "null idempotency key", raw: `{"schema":"semantic-change-batch/v2","idempotency_key":null,"changes":[]}`},
		{name: "empty idempotency key", raw: `{"schema":"semantic-change-batch/v2","idempotency_key":"","changes":[]}`},
		{name: "missing changes", raw: `{"schema":"semantic-change-batch/v2","idempotency_key":"missing-changes"}`},
		{name: "null changes", raw: `{"schema":"semantic-change-batch/v2","idempotency_key":"null-changes","changes":null}`},
		{name: "object changes", raw: `{"schema":"semantic-change-batch/v2","idempotency_key":"object-changes","changes":{}}`},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var batch blackboardv2.ChangeBatch
			if err := json.Unmarshal([]byte(tt.raw), &batch); err == nil {
				t.Fatalf("decoded invalid ChangeBatch: %s", tt.raw)
			}
		})
	}
}

func TestProgrammaticNilChangesFailBeforeIdempotencyReplay(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(filepath.Join(t.TempDir(), "pentest.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	createdProject, err := project.NewService(db).Create("Closed ChangeBatch", "", project.Scope{}, project.Defaults{})
	if err != nil {
		t.Fatalf("create project: %v", err)
	}
	service := blackboardv2.NewService(db)
	valid := blackboardv2.ChangeBatch{
		Schema:         "semantic-change-batch/v2",
		IdempotencyKey: "empty-programmatic-batch",
		Changes:        []blackboardv2.Change{},
	}
	first, err := service.Apply(ctx, createdProject.ID, valid)
	if err != nil {
		t.Fatalf("apply empty non-nil ChangeBatch: %v", err)
	}
	replay, err := service.Apply(ctx, createdProject.ID, valid)
	if err != nil {
		t.Fatalf("replay empty non-nil ChangeBatch: %v", err)
	}
	if !reflect.DeepEqual(replay, first) {
		t.Fatalf("empty ChangeBatch replay = %#v, want %#v", replay, first)
	}

	_, err = service.Apply(ctx, createdProject.ID, blackboardv2.ChangeBatch{
		Schema:         "semantic-change-batch/v2",
		IdempotencyKey: valid.IdempotencyKey,
		Changes:        nil,
	})
	var semanticErr *blackboardv2.Error
	if !errors.As(err, &semanticErr) || semanticErr.Code != "semantic_validation" || semanticErr.Path != "changes" {
		t.Fatalf("nil changes replay error = %#v, want semantic_validation on changes before replay", err)
	}
}

func TestRelationshipHistoryUsesSemanticVersionOrderAcrossPages(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(filepath.Join(t.TempDir(), "pentest.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	createdProject, err := project.NewService(db).Create("Relationship History", "", project.Scope{}, project.Defaults{})
	if err != nil {
		t.Fatalf("create project: %v", err)
	}
	service := blackboardv2.NewService(db)

	_, err = service.Apply(ctx, createdProject.ID, blackboardv2.ChangeBatch{
		Schema:         "semantic-change-batch/v2",
		IdempotencyKey: "create-history-objectives",
		Changes: []blackboardv2.Change{
			{Op: "create", Key: "objective:dependent-history", Type: "objective", Record: blackboardv2.ObjectiveRecord{Status: "open", Objective: "Exercise the dependent path"}},
			{Op: "create", Key: "objective:prerequisite-history", Type: "objective", Record: blackboardv2.ObjectiveRecord{Status: "open", Objective: "Map the prerequisite path"}},
			{Op: "relate", From: "objective:dependent-history", Relation: "depends_on", To: "objective:prerequisite-history", Reason: "The prerequisite must be mapped first"},
		},
	})
	if err != nil {
		t.Fatalf("create reason relationship v1: %v", err)
	}
	_, err = service.Apply(ctx, createdProject.ID, blackboardv2.ChangeBatch{
		Schema:         "semantic-change-batch/v2",
		IdempotencyKey: "update-history-reason",
		Changes: []blackboardv2.Change{{
			Op: "relate", From: "objective:dependent-history", Relation: "depends_on", To: "objective:prerequisite-history", Version: 1, Reason: "The prerequisite boundary must be mapped first",
		}},
	})
	if err != nil {
		t.Fatalf("update reason relationship to v2: %v", err)
	}
	_, err = service.Apply(ctx, createdProject.ID, blackboardv2.ChangeBatch{
		Schema:         "semantic-change-batch/v2",
		IdempotencyKey: "terminalize-history-endpoint",
		Changes: []blackboardv2.Change{{
			Op: "transition", Key: "objective:dependent-history", Version: 1, Status: "abandoned", ResolutionSummary: "The dependent path is no longer relevant",
		}},
	})
	if err != nil {
		t.Fatalf("terminalize relationship endpoint: %v", err)
	}

	var v1RecordedAt, v2RecordedAt string
	for version, destination := range map[int]*string{1: &v1RecordedAt, 2: &v2RecordedAt} {
		if err := db.QueryRowContext(ctx, `
			SELECT recorded_at
			FROM blackboard_v2_relationship_history
			WHERE project_id = ? AND from_key = ? AND relation = ? AND to_key = ? AND version = ?`,
			createdProject.ID, "objective:dependent-history", "depends_on", "objective:prerequisite-history", version,
		).Scan(destination); err != nil {
			t.Fatalf("read relationship v%d archive time: %v", version, err)
		}
	}
	v1Time, err := time.Parse(time.RFC3339Nano, v1RecordedAt)
	if err != nil {
		t.Fatalf("parse relationship v1 archive time %q: %v", v1RecordedAt, err)
	}
	v2Time, err := time.Parse(time.RFC3339Nano, v2RecordedAt)
	if err != nil {
		t.Fatalf("parse relationship v2 archive time %q: %v", v2RecordedAt, err)
	}
	if !v2Time.After(v1Time) {
		t.Fatalf("relationship archive chronology v1=%q v2=%q, want v2 archived at terminal time", v1RecordedAt, v2RecordedAt)
	}

	var items []blackboardv2.HistoryItem
	cursor := ""
	for {
		page, err := service.ReadHistory(ctx, createdProject.ID, "objective:dependent-history", blackboardv2.HistoryOptions{Cursor: cursor, Limit: 1})
		if err != nil {
			t.Fatalf("read relationship history page after %q: %v", cursor, err)
		}
		if len(page.Items) != 1 {
			t.Fatalf("history page after %q has %d items, want 1", cursor, len(page.Items))
		}
		items = append(items, page.Items[0])
		if page.NextCursor == "" {
			break
		}
		cursor = page.NextCursor
	}
	if len(items) != 4 {
		t.Fatalf("history items = %#v, want two record and two relationship versions", items)
	}
	if items[0].Kind != "record" || items[0].Version != 1 || items[1].Kind != "record" || items[1].Version != 2 {
		t.Fatalf("record history order = %#v", items[:2])
	}
	if items[2].Kind != "relationship" || items[2].Version != 1 || items[2].Reason != "The prerequisite must be mapped first" ||
		items[3].Kind != "relationship" || items[3].Version != 2 || items[3].Reason != "The prerequisite boundary must be mapped first" {
		t.Fatalf("relationship history order = %#v", items[2:])
	}
}
