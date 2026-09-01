package workinggraph_test

import (
	"context"
	"testing"

	"pentest/internal/blackboardv2"
	"pentest/internal/owner"
	"pentest/internal/workinggraph"
)

type compilerBoard struct {
	versions         map[string]int
	relationVersions map[string]int
	batch            blackboardv2.ChangeBatch
}

func (board *compilerBoard) ReadCurrentForOwner(_ context.Context, _ owner.Contract, key string) (blackboardv2.CurrentDetail, error) {
	version, ok := board.versions[key]
	if !ok {
		return blackboardv2.CurrentDetail{}, &blackboardv2.Error{Code: "not_found", Message: "missing", Details: map[string]any{}}
	}
	return blackboardv2.CurrentDetail{Key: key, Version: version}, nil
}

func (board *compilerBoard) CurrentRelationshipVersionForOwner(_ context.Context, _ owner.Contract, from, relation, to string) (int, error) {
	return board.relationVersions[from+"|"+relation+"|"+to], nil
}

func (board *compilerBoard) ApplyForOwnerContinuation(_ context.Context, _ owner.Contract, _ string, batch blackboardv2.ChangeBatch) (blackboardv2.ChangeResult, error) {
	board.batch = batch
	return blackboardv2.ChangeResult{Schema: "semantic-change-result/v2", Revision: 9}, nil
}

func TestSemanticCompilerResolvesVersionsAndOwnsIdempotency(t *testing.T) {
	board := &compilerBoard{
		versions: map[string]int{
			"fact:existing": 4, "attempt:login": 2, "objective:new": 3,
			"objective:old": 5, "entity:source": 6, "entity:canonical": 7,
		},
		relationVersions: map[string]int{"fact:existing|derived_from|entity:source": 8},
	}
	compiler := workinggraph.NewSemanticCompiler(board)
	contract := owner.NewTaskContract("task-1", "project-1", "/tmp/task-1")
	outcome, err := compiler.Apply(context.Background(), contract, "continuation-1", workinggraph.Intent{
		Schema: workinggraph.IntentSchema, ID: "intent_00000001", Kind: workinggraph.IntentSemanticChanges,
		Payload: map[string]any{"changes": []any{
			map[string]any{"op": "upsert", "key": "fact:existing", "type": "fact", "record": map[string]any{"summary": "new summary"}},
			map[string]any{"op": "upsert", "key": "entity:new", "type": "entity", "record": map[string]any{"status": "active", "kind": "host", "name": "new", "scope_status": "in_scope"}},
			map[string]any{"op": "transition", "key": "attempt:login", "status": "failed", "summary": "failed safely"},
			map[string]any{"op": "unrelate", "from": "fact:existing", "relation": "derived_from", "to": "entity:source"},
			map[string]any{"op": "supersede", "replacement": "objective:new", "replaced": "objective:old"},
			map[string]any{"op": "merge", "source": "entity:source", "canonical": "entity:canonical"},
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if outcome.State != workinggraph.IntentStateApplied || board.batch.IdempotencyKey != "working-graph:task:task-1:continuation-1:intent_00000001" {
		t.Fatalf("outcome=%#v batch=%#v", outcome, board.batch)
	}
	if len(board.batch.Changes) != 6 {
		t.Fatalf("changes = %#v", board.batch.Changes)
	}
	checks := []struct {
		op      string
		version int
	}{
		{op: "update", version: 4}, {op: "create", version: 0}, {op: "transition", version: 2}, {op: "unrelate", version: 8},
	}
	for index, check := range checks {
		if board.batch.Changes[index].Op != check.op || board.batch.Changes[index].Version != check.version {
			t.Fatalf("change[%d] = %#v", index, board.batch.Changes[index])
		}
	}
	if change := board.batch.Changes[4]; change.ReplacementVersion != 3 || change.ReplacedVersion != 5 {
		t.Fatalf("supersede = %#v", change)
	}
	if change := board.batch.Changes[5]; change.SourceVersion != 6 || change.CanonicalVersion != 7 {
		t.Fatalf("merge = %#v", change)
	}
}

func TestSemanticCompilerReturnsActionRequiredForInvalidRuntimePayload(t *testing.T) {
	compiler := workinggraph.NewSemanticCompiler(&compilerBoard{versions: map[string]int{}, relationVersions: map[string]int{}})
	outcome, err := compiler.Apply(context.Background(), owner.NewSessionContract("session-1", "/tmp/session-1"), "continuation-1", workinggraph.Intent{
		Schema: workinggraph.IntentSchema, ID: "intent_00000001", Kind: workinggraph.IntentSemanticChanges,
		Payload: map[string]any{"changes": []any{map[string]any{"op": "update", "key": "fact:x"}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if outcome.State != workinggraph.IntentStateActionRequired || outcome.Error["code"] != "invalid_intent_payload" {
		t.Fatalf("outcome = %#v", outcome)
	}
}
