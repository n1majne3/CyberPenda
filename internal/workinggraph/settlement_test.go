package workinggraph_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"pentest/internal/owner"
	"pentest/internal/store"
	"pentest/internal/workinggraph"
)

func TestSettleClaimsCurrentContinuationInSequenceAndWritesReceipts(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "pentest.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	service := workinggraph.NewService(db)
	contract := owner.NewTaskContract("task-1", "project-1", t.TempDir())
	projection, err := service.Prepare(context.Background(), workinggraph.OwnerContext{
		Owner: contract, ContinuationID: "continuation-1", Workdir: contract.Workdir,
	})
	if err != nil {
		t.Fatal(err)
	}
	writeIntentFile(t, projection.Outbox, workinggraph.Intent{Schema: workinggraph.IntentSchema, ID: "intent_00000002", Kind: workinggraph.IntentAttemptResult, Payload: map[string]any{"key": "attempt:2"}})
	writeIntentFile(t, projection.Outbox, workinggraph.Intent{Schema: workinggraph.IntentSchema, ID: "intent_00000001", Kind: workinggraph.IntentSemanticChanges, Payload: map[string]any{"changes": []any{}}})
	if err := os.WriteFile(filepath.Join(projection.Outbox, "intent_00000003.123.tmp"), []byte("partial"), 0o600); err != nil {
		t.Fatal(err)
	}

	var applied []string
	result, err := service.Settle(context.Background(), workinggraph.SettlementRequest{
		Owner: contract, ContinuationID: "continuation-1", Projection: projection,
		Apply: func(_ context.Context, _ owner.Contract, _ string, intent workinggraph.Intent) (workinggraph.ApplyOutcome, error) {
			applied = append(applied, intent.ID)
			return workinggraph.ApplyOutcome{State: workinggraph.IntentStateApplied, Result: map[string]any{"accepted": intent.ID}}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(applied, []string{"intent_00000001", "intent_00000002"}) {
		t.Fatalf("apply order = %#v", applied)
	}
	if len(result.Receipts) != 2 || result.Blocked {
		t.Fatalf("settlement result = %#v", result)
	}
	for _, intentID := range applied {
		raw, readErr := os.ReadFile(filepath.Join(projection.Receipts, intentID+".json"))
		if readErr != nil {
			t.Fatal(readErr)
		}
		var receipt workinggraph.Receipt
		if err := json.Unmarshal(raw, &receipt); err != nil {
			t.Fatal(err)
		}
		if receipt.IntentID != intentID || receipt.State != workinggraph.IntentStateApplied {
			t.Fatalf("receipt = %#v", receipt)
		}
	}
}

func TestSettleStopsAtActionRequiredAndDoesNotClaimLaterIntent(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "pentest.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	service := workinggraph.NewService(db)
	contract := owner.NewTaskContract("task-1", "project-1", t.TempDir())
	projection, err := service.Prepare(context.Background(), workinggraph.OwnerContext{
		Owner: contract, ContinuationID: "continuation-1", Workdir: contract.Workdir,
	})
	if err != nil {
		t.Fatal(err)
	}
	for sequence := 1; sequence <= 3; sequence++ {
		id := "intent_0000000" + string(rune('0'+sequence))
		writeIntentFile(t, projection.Outbox, workinggraph.Intent{Schema: workinggraph.IntentSchema, ID: id, Kind: workinggraph.IntentSemanticChanges, Payload: map[string]any{"changes": []any{}}})
	}

	var applied []string
	result, err := service.Settle(context.Background(), workinggraph.SettlementRequest{
		Owner: contract, ContinuationID: "continuation-1", Projection: projection,
		Apply: func(_ context.Context, _ owner.Contract, _ string, intent workinggraph.Intent) (workinggraph.ApplyOutcome, error) {
			applied = append(applied, intent.ID)
			if intent.ID == "intent_00000002" {
				return workinggraph.ApplyOutcome{State: workinggraph.IntentStateActionRequired, Error: map[string]any{"code": "operator_required"}}, nil
			}
			return workinggraph.ApplyOutcome{State: workinggraph.IntentStateApplied}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Blocked || !reflect.DeepEqual(applied, []string{"intent_00000001", "intent_00000002"}) {
		t.Fatalf("settlement result = %#v, applied=%#v", result, applied)
	}
	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM working_graph_intents WHERE owner_kind='task' AND owner_id='task-1'`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 2 {
		t.Fatalf("claimed intent count = %d", count)
	}
}

func TestSettleRejectsSymlinkedIntent(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "pentest.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	service := workinggraph.NewService(db)
	contract := owner.NewTaskContract("task-1", "project-1", t.TempDir())
	projection, err := service.Prepare(context.Background(), workinggraph.OwnerContext{
		Owner: contract, ContinuationID: "continuation-1", Workdir: contract.Workdir,
	})
	if err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(t.TempDir(), "intent.json")
	writeIntentFile(t, filepath.Dir(target), workinggraph.Intent{Schema: workinggraph.IntentSchema, ID: "intent_00000001", Kind: workinggraph.IntentSemanticChanges, Payload: map[string]any{"changes": []any{}}})
	if err := os.Rename(filepath.Join(filepath.Dir(target), "intent_00000001.json"), target); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(projection.Outbox, "intent_00000001.json")); err != nil {
		t.Fatal(err)
	}
	_, err = service.Settle(context.Background(), workinggraph.SettlementRequest{
		Owner: contract, ContinuationID: "continuation-1", Projection: projection,
		Apply: func(context.Context, owner.Contract, string, workinggraph.Intent) (workinggraph.ApplyOutcome, error) {
			return workinggraph.ApplyOutcome{State: workinggraph.IntentStateApplied}, nil
		},
	})
	if err == nil {
		t.Fatal("symlinked intent was accepted")
	}
}

func writeIntentFile(t *testing.T, directory string, intent workinggraph.Intent) {
	t.Helper()
	raw, err := json.Marshal(intent)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, intent.ID+".json"), raw, 0o600); err != nil {
		t.Fatal(err)
	}
}
