package blackboardcompat_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"

	"pentest/internal/blackboard"
	"pentest/internal/blackboardcompat"
	"pentest/internal/project"
	"pentest/internal/projectinterface"
	"pentest/internal/store"
	"pentest/internal/task"
)

func TestEquivalentLegacyHTTPMCPAndCLIWritesTranslateToOneGraphMutation(t *testing.T) {
	db, compatibility, graph, projectRow, principal, _ := newCompatibilityFixture(t)

	call := blackboardcompat.LegacyCall{
		Kind:           blackboardcompat.CallUpsertFact,
		ProjectID:      projectRow.ID,
		Principal:      principal,
		IdempotencyKey: "legacy-fact:admin-panel",
		Fact: &blackboardcompat.FactWrite{
			FactKey: "fact:admin-panel", Category: "service", Summary: "Admin panel exposed",
			Body: "Observed on the in-scope application.", Confidence: "tentative", ScopeStatus: "in_scope",
		},
	}

	var payload []byte
	var mutationSequence int
	for _, transport := range []blackboardcompat.Transport{
		blackboardcompat.TransportHTTP,
		blackboardcompat.TransportMCP,
		blackboardcompat.TransportCLI,
	} {
		call.Transport = transport
		result, err := compatibility.Call(context.Background(), call)
		if err != nil {
			t.Fatalf("%s compatibility write: %v", transport, err)
		}
		encoded, err := json.Marshal(result.Payload)
		if err != nil {
			t.Fatalf("encode %s payload: %v", transport, err)
		}
		if payload == nil {
			payload = encoded
			mutationSequence = result.Mutation.MutationSequence
		} else {
			if string(encoded) != string(payload) {
				t.Fatalf("%s payload = %s, want %s", transport, encoded, payload)
			}
			if result.Mutation.MutationSequence != mutationSequence {
				t.Fatalf("%s mutation sequence = %d, want replay of %d", transport, result.Mutation.MutationSequence, mutationSequence)
			}
		}
	}

	stored, err := graph.ReadNode(context.Background(), blackboard.ReadNodeRequest{
		ProjectID: projectRow.ID, NodeType: blackboard.NodeTypeProjectFact, Key: "fact:admin-panel",
	})
	if err != nil {
		t.Fatalf("read translated Project Fact: %v", err)
	}
	if stored.Node.Version != 1 || stored.ObservedGraphRevision != 1 {
		t.Fatalf("translated Project Fact = version %d at revision %d, want version 1 at revision 1", stored.Node.Version, stored.ObservedGraphRevision)
	}
	legacyFacts, err := blackboard.NewService(db).FactIndex(projectRow.ID, blackboard.FactIndexOptions{IncludeDeprecated: true})
	if err != nil {
		t.Fatalf("read frozen legacy service: %v", err)
	}
	if len(legacyFacts) != 0 {
		t.Fatalf("legacy Fact service was mutated: %+v", legacyFacts)
	}
}

func TestFactUpsertObservesVersionOnceAndPreservesEmptyBody(t *testing.T) {
	_, compatibility, _, projectRow, principal, _ := newCompatibilityFixture(t)
	ctx := context.Background()
	create := blackboardcompat.LegacyCall{
		Kind: blackboardcompat.CallUpsertFact, Transport: blackboardcompat.TransportHTTP,
		ProjectID: projectRow.ID, Principal: principal, IdempotencyKey: "fact:create",
		Fact: &blackboardcompat.FactWrite{
			FactKey: "fact:service", Category: "service", Summary: "HTTPS exposed",
			Body: "TLS 1.3 on port 443", Confidence: "tentative", ScopeStatus: "in_scope",
		},
	}
	if _, err := compatibility.Call(ctx, create); err != nil {
		t.Fatalf("create Fact: %v", err)
	}

	update := create
	update.IdempotencyKey = "fact:update"
	update.Fact.Summary = "HTTPS service confirmed"
	update.Fact.Body = ""
	result, err := compatibility.Call(ctx, update)
	if err != nil {
		t.Fatalf("update Fact without expected_version: %v", err)
	}
	fact := result.Payload.(blackboard.LegacyFactDetailV1)
	if fact.Version != 2 || fact.Summary != "HTTPS service confirmed" || fact.Body != "TLS 1.3 on port 443" {
		t.Fatalf("updated Fact = %+v", fact)
	}
	replay, err := compatibility.Call(ctx, update)
	if err != nil {
		t.Fatalf("replay update: %v", err)
	}
	if replay.Mutation.MutationSequence != result.Mutation.MutationSequence {
		t.Fatalf("replay mutation sequence = %d, want %d", replay.Mutation.MutationSequence, result.Mutation.MutationSequence)
	}
	emptySummary := update
	emptySummary.IdempotencyKey = "fact:empty-summary"
	emptySummary.Fact = &blackboardcompat.FactWrite{FactKey: "fact:service", Summary: ""}
	if _, err := compatibility.Call(ctx, emptySummary); err == nil {
		t.Fatal("empty Fact summary update unexpectedly succeeded")
	}
	normalized := update
	normalized.IdempotencyKey = "fact:empty-normalization"
	normalized.Fact = &blackboardcompat.FactWrite{FactKey: "fact:service", Summary: "Normalized empties"}
	normalizedResult, err := compatibility.Call(ctx, normalized)
	if err != nil {
		t.Fatalf("normalize empty legacy Fact fields: %v", err)
	}
	normalizedFact := normalizedResult.Payload.(blackboard.LegacyFactDetailV1)
	if normalizedFact.Category != "uncategorized" || normalizedFact.Confidence != "tentative" || normalizedFact.ScopeStatus != "unknown" {
		t.Fatalf("normalized Fact = %+v", normalizedFact)
	}

	staleVersion := 1
	stale := update
	stale.IdempotencyKey = "fact:stale"
	stale.ExpectedVersion = &staleVersion
	stale.Fact.Summary = "stale overwrite"
	_, err = compatibility.Call(ctx, stale)
	var interfaceErr *projectinterface.Error
	if !errors.As(err, &interfaceErr) || interfaceErr.Code != blackboard.ErrCodeVersionConflict {
		t.Fatalf("stale update error = %v, want version_conflict", err)
	}
}

func TestKeyedCompatibilityReplayReturnsOriginalPayloadAfterLaterMutation(t *testing.T) {
	db, compatibility, _, projectRow, principal, _ := newCompatibilityFixture(t)
	ctx := context.Background()
	original := blackboardcompat.LegacyCall{
		Kind: blackboardcompat.CallUpsertFact, Transport: blackboardcompat.TransportHTTP,
		ProjectID: projectRow.ID, Principal: principal, IdempotencyKey: "fact:original",
		Fact: &blackboardcompat.FactWrite{FactKey: "fact:replay", Category: "service", Summary: "Original", Confidence: "tentative", ScopeStatus: "in_scope"},
	}
	first, err := compatibility.Call(ctx, original)
	if err != nil {
		t.Fatalf("first compatibility call: %v", err)
	}
	if _, err := db.Exec(`DELETE FROM blackboard_compatibility_results WHERE project_id=? AND idempotency_key=?`, projectRow.ID, original.IdempotencyKey); err != nil {
		t.Fatalf("simulate result-persistence loss: %v", err)
	}
	updated := original
	updated.IdempotencyKey = "fact:later-update"
	updated.Fact = &blackboardcompat.FactWrite{FactKey: "fact:replay", Summary: "Later", Confidence: "tentative"}
	if _, err := compatibility.Call(ctx, updated); err != nil {
		t.Fatalf("later compatibility update: %v", err)
	}
	replay, err := compatibility.Call(ctx, original)
	if err != nil {
		t.Fatalf("replay original call: %v", err)
	}
	firstJSON, _ := json.Marshal(first.Payload)
	replayJSON, _ := json.Marshal(replay.Payload)
	if string(replayJSON) != string(firstJSON) {
		t.Fatalf("replay payload = %s, want original %s", replayJSON, firstJSON)
	}
}

func TestConcurrentKeyedOperatorSummaryCreatesOneVersion(t *testing.T) {
	db, _, graph, projectRow, operator, _ := newCompatibilityFixture(t)
	tasks := task.NewService(db)
	createdTask, err := tasks.Create(task.CreateRequest{ProjectID: projectRow.ID, Goal: "Concurrent summary", RuntimeProfileID: "profile", Runner: task.RunnerSandbox})
	if err != nil {
		t.Fatalf("create Task: %v", err)
	}
	var misses atomic.Int32
	release := make(chan struct{})
	compatibility := blackboardcompat.NewService(blackboardcompat.Deps{
		DB: db, Graph: graph, Reads: blackboard.NewBlackboardReadService(db),
		ProjectInterface: projectinterface.NewService(projectinterface.Deps{DB: db, Graph: graph, Tasks: tasks}), Tasks: tasks,
		AfterResultMiss: func() {
			if misses.Add(1) == 2 {
				close(release)
			}
			<-release
		},
	})
	call := blackboardcompat.LegacyCall{
		Kind: blackboardcompat.CallPutTaskSummary, Transport: blackboardcompat.TransportHTTP,
		ProjectID: projectRow.ID, Principal: operator, IdempotencyKey: "summary:concurrent",
		TaskSummary: &blackboardcompat.TaskSummaryWrite{TaskID: createdTask.ID, Summary: "One durable summary", SubmittedBy: "operator:test"},
	}
	results := make([]blackboardcompat.LegacyResult, 2)
	errs := make([]error, 2)
	var wg sync.WaitGroup
	for i := range results {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			results[index], errs[index] = compatibility.Call(context.Background(), call)
		}(i)
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Fatalf("call %d: %v", i, err)
		}
	}
	first := results[0].Payload.(task.SummaryVersion)
	second := results[1].Payload.(task.SummaryVersion)
	if first.ID != second.ID {
		t.Fatalf("concurrent replay IDs = %s and %s", first.ID, second.ID)
	}
	versions, err := tasks.SummaryVersions(createdTask.ID)
	if err != nil || len(versions) != 1 {
		t.Fatalf("Summary versions = %+v err=%v, want one", versions, err)
	}
}

func TestLegacyRelationsAndMergesUseGraphSemantics(t *testing.T) {
	_, compatibility, graph, projectRow, principal, _ := newCompatibilityFixture(t)
	ctx := context.Background()
	for _, key := range []string{"fact:source", "fact:canonical"} {
		_, err := compatibility.Call(ctx, blackboardcompat.LegacyCall{
			Kind: blackboardcompat.CallUpsertFact, Transport: blackboardcompat.TransportHTTP,
			ProjectID: projectRow.ID, Principal: principal, IdempotencyKey: "create:" + key,
			Fact: &blackboardcompat.FactWrite{FactKey: key, Category: "service", Summary: key, Confidence: "tentative", ScopeStatus: "in_scope"},
		})
		if err != nil {
			t.Fatalf("create %s: %v", key, err)
		}
	}

	relationResult, err := compatibility.Call(ctx, blackboardcompat.LegacyCall{
		Kind: blackboardcompat.CallPutFactRelation, Transport: blackboardcompat.TransportMCP,
		ProjectID: projectRow.ID, Principal: principal, IdempotencyKey: "relation:leads-to",
		Relation: &blackboardcompat.FactRelationWrite{
			SourceFactKey: "fact:source", TargetFactKey: "fact:canonical", Relation: "leads-to", Summary: "next step",
		},
	})
	if err != nil {
		t.Fatalf("put representable relation: %v", err)
	}
	relation := relationResult.Payload.(blackboard.LegacyFactRelationRow)
	if relation.Relation != "leads_to" || relation.SourceFactKey != "fact:source" || relation.TargetFactKey != "fact:canonical" {
		t.Fatalf("translated relation = %+v", relation)
	}
	relationUpdate := blackboardcompat.LegacyCall{
		Kind: blackboardcompat.CallPutFactRelation, Transport: blackboardcompat.TransportHTTP,
		ProjectID: projectRow.ID, Principal: principal, IdempotencyKey: "relation:update",
		Relation: &blackboardcompat.FactRelationWrite{SourceFactKey: "fact:source", TargetFactKey: "fact:canonical", Relation: "leads_to", Summary: "updated"},
	}
	updatedRelation, err := compatibility.Call(ctx, relationUpdate)
	if err != nil {
		t.Fatalf("update relation without expected_version: %v", err)
	}
	if updatedRelation.Payload.(blackboard.LegacyFactRelationRow).Summary != "updated" {
		t.Fatalf("updated relation = %+v", updatedRelation.Payload)
	}

	_, err = compatibility.Call(ctx, blackboardcompat.LegacyCall{
		Kind: blackboardcompat.CallPutFactRelation, Transport: blackboardcompat.TransportHTTP,
		ProjectID: projectRow.ID, Principal: principal, IdempotencyKey: "relation:depends-on",
		Relation: &blackboardcompat.FactRelationWrite{
			SourceFactKey: "fact:source", TargetFactKey: "fact:canonical", Relation: "depends_on",
		},
	})
	var interfaceErr *projectinterface.Error
	if !errors.As(err, &interfaceErr) || interfaceErr.Code != blackboardcompat.ErrCodeLegacyRelationNotGraphRepresentable {
		t.Fatalf("depends_on error = %v, want %s", err, blackboardcompat.ErrCodeLegacyRelationNotGraphRepresentable)
	}

	mergeResult, err := compatibility.Call(ctx, blackboardcompat.LegacyCall{
		Kind: blackboardcompat.CallMergeFacts, Transport: blackboardcompat.TransportCLI,
		ProjectID: projectRow.ID, Principal: principal, IdempotencyKey: "merge:facts",
		FactMerge: &blackboardcompat.MergeWrite{SourceKey: "fact:source", CanonicalKey: "fact:canonical"},
	})
	if err != nil {
		t.Fatalf("merge Facts: %v", err)
	}
	if mergeResult.Payload != (blackboardcompat.MergeResult{Merged: true}) {
		t.Fatalf("merge payload = %+v", mergeResult.Payload)
	}
	resolved, err := graph.ReadNode(ctx, blackboard.ReadNodeRequest{ProjectID: projectRow.ID, NodeType: blackboard.NodeTypeProjectFact, Key: "fact:source"})
	if err != nil {
		t.Fatalf("resolve merged source key: %v", err)
	}
	if resolved.Node.StableKey != "fact:canonical" || resolved.ResolvedFromAlias != "fact:source" {
		t.Fatalf("merged source resolution = %+v", resolved)
	}
}

func TestLegacyFindingUpdatesRequireGraphSupportAndMergeNonDestructively(t *testing.T) {
	_, compatibility, graph, projectRow, principal, _ := newCompatibilityFixture(t)
	ctx := context.Background()
	create := blackboardcompat.LegacyCall{
		Kind: blackboardcompat.CallUpsertFinding, Transport: blackboardcompat.TransportHTTP,
		ProjectID: projectRow.ID, Principal: principal, IdempotencyKey: "finding:create",
		Finding: &blackboardcompat.FindingWrite{
			FindingKey: "finding:source", Title: "Exposed admin panel", Description: "Initial detail",
			Status: "unconfirmed", Target: "https://example.test/admin", Proof: "HTTP 200",
			Impact: "Administrative surface exposed", Recommendation: "Restrict access",
			CVSSVersion: "4.0", CVSSVector: "CVSS:4.0/AV:N/AC:L/AT:N/PR:N/UI:N/VC:L/VI:L/VA:N/SC:N/SI:N/SA:N",
		},
	}
	created, err := compatibility.Call(ctx, create)
	if err != nil {
		t.Fatalf("create Finding: %v", err)
	}
	if got := created.Payload.(blackboard.LegacyFindingV1); got.Title != create.Finding.Title || got.Version != 1 {
		t.Fatalf("created Finding = %+v", got)
	}

	update := create
	update.IdempotencyKey = "finding:update"
	update.Finding = &blackboardcompat.FindingWrite{FindingKey: "finding:source", Description: "Expanded detail"}
	updated, err := compatibility.Call(ctx, update)
	if err != nil {
		t.Fatalf("partially update Finding: %v", err)
	}
	if got := updated.Payload.(blackboard.LegacyFindingV1); got.Title != "Exposed admin panel" || got.Description != "Expanded detail" || got.Version != 2 {
		t.Fatalf("updated Finding = %+v", got)
	}

	confirm := create
	confirm.IdempotencyKey = "finding:confirm-without-support"
	confirm.Finding = &blackboardcompat.FindingWrite{FindingKey: "finding:source", Status: "confirmed"}
	_, err = compatibility.Call(ctx, confirm)
	var interfaceErr *projectinterface.Error
	if !errors.As(err, &interfaceErr) || interfaceErr.Code != blackboard.ErrCodeTransitionGuardFailed {
		t.Fatalf("unsupported confirmation error = %v, want transition_guard_failed", err)
	}

	canonical := create
	canonical.IdempotencyKey = "finding:canonical"
	canonical.Finding = &blackboardcompat.FindingWrite{FindingKey: "finding:canonical", Title: "Canonical issue", Status: "unconfirmed"}
	if _, err := compatibility.Call(ctx, canonical); err != nil {
		t.Fatalf("create canonical Finding: %v", err)
	}
	merged, err := compatibility.Call(ctx, blackboardcompat.LegacyCall{
		Kind: blackboardcompat.CallMergeFindings, Transport: blackboardcompat.TransportMCP,
		ProjectID: projectRow.ID, Principal: principal, IdempotencyKey: "finding:merge",
		FindingMerge: &blackboardcompat.MergeWrite{SourceKey: "finding:source", CanonicalKey: "finding:canonical"},
	})
	if err != nil || merged.Payload != (blackboardcompat.MergeResult{Merged: true}) {
		t.Fatalf("merge Findings: result=%+v err=%v", merged, err)
	}
	resolved, err := graph.ReadNode(ctx, blackboard.ReadNodeRequest{ProjectID: projectRow.ID, NodeType: blackboard.NodeTypeFinding, Key: "finding:source"})
	if err != nil || resolved.Node.StableKey != "finding:canonical" || resolved.ResolvedFromAlias != "finding:source" {
		t.Fatalf("merged Finding resolution = %+v err=%v", resolved, err)
	}
}

func TestLegacyEvidenceUsesRetainedEvidenceAndRequiresRuntimeAttempt(t *testing.T) {
	_, compatibility, _, projectRow, principal, root := newCompatibilityFixture(t)
	ctx := context.Background()
	_, err := compatibility.Call(ctx, blackboardcompat.LegacyCall{
		Kind: blackboardcompat.CallUpsertFact, Transport: blackboardcompat.TransportHTTP,
		ProjectID: projectRow.ID, Principal: principal, IdempotencyKey: "evidence-target",
		Fact: &blackboardcompat.FactWrite{FactKey: "fact:target", Category: "service", Summary: "Target", Confidence: "tentative", ScopeStatus: "in_scope"},
	})
	if err != nil {
		t.Fatalf("create Evidence target: %v", err)
	}
	sourcePath := filepath.Join(root, "response.txt")
	if err := os.WriteFile(sourcePath, []byte("HTTP/1.1 200 OK\n"), 0o600); err != nil {
		t.Fatalf("write Evidence source: %v", err)
	}

	result, err := compatibility.Call(ctx, blackboardcompat.LegacyCall{
		Kind: blackboardcompat.CallAttachEvidence, Transport: blackboardcompat.TransportCLI,
		ProjectID: projectRow.ID, Principal: principal, IdempotencyKey: "evidence:retain",
		Evidence: &blackboardcompat.EvidenceWrite{
			EvidenceKey: "evidence:response", AttachToType: "fact", AttachToKey: "fact:target",
			ArtifactType: "http_exchange", SourcePath: sourcePath, Summary: "Captured response",
		},
	})
	if err != nil {
		t.Fatalf("retain operator Evidence: %v", err)
	}
	artifact := result.Payload.(blackboard.LegacyEvidenceArtifactV1)
	if artifact.EvidenceKey != "evidence:response" || artifact.AttachToKey != "fact:target" || artifact.SHA256 == "" || len(artifact.Attachments) != 1 {
		t.Fatalf("retained Evidence payload = %+v", artifact)
	}

	runtimePrincipal := projectinterface.Principal{
		ActorType: blackboard.ActorTypeRuntime, ActorID: "runtime:test", ProjectID: projectRow.ID,
		Grant: projectinterface.Grant{ProjectID: projectRow.ID, TaskID: "task", ContinuationID: "continuation"},
	}
	_, err = compatibility.Call(ctx, blackboardcompat.LegacyCall{
		Kind: blackboardcompat.CallAttachEvidence, Transport: blackboardcompat.TransportMCP,
		ProjectID: projectRow.ID, Principal: runtimePrincipal, IdempotencyKey: "evidence:runtime",
		Evidence: &blackboardcompat.EvidenceWrite{
			EvidenceKey: "evidence:runtime", AttachToType: "fact", AttachToKey: "fact:target",
			ArtifactType: "http_exchange", SourcePath: sourcePath, Summary: "Runtime response",
		},
	})
	var interfaceErr *projectinterface.Error
	if !errors.As(err, &interfaceErr) || interfaceErr.Code != blackboardcompat.ErrCodeCompatibilityAttemptRequired {
		t.Fatalf("Runtime Evidence error = %v, want %s", err, blackboardcompat.ErrCodeCompatibilityAttemptRequired)
	}
}

func TestRuntimeSummaryCompatibilityFinishesWhileOperatorSummaryStaysSeparate(t *testing.T) {
	db, compatibility, graph, projectRow, operator, _ := newCompatibilityFixture(t)
	ctx := context.Background()
	tasks := task.NewService(db)
	createdTask, err := tasks.Create(task.CreateRequest{
		ProjectID: projectRow.ID, Goal: "Finish compatibility", RuntimeProfileID: "profile", Runner: task.RunnerSandbox,
	})
	if err != nil {
		t.Fatalf("create Task: %v", err)
	}

	operatorResult, err := compatibility.Call(ctx, blackboardcompat.LegacyCall{
		Kind: blackboardcompat.CallPutTaskSummary, Transport: blackboardcompat.TransportHTTP,
		ProjectID: projectRow.ID, Principal: operator, IdempotencyKey: "summary:operator",
		TaskSummary: &blackboardcompat.TaskSummaryWrite{TaskID: createdTask.ID, Summary: "Operator note", SubmittedBy: "operator:test"},
	})
	if err != nil {
		t.Fatalf("put operator Task Summary: %v", err)
	}
	operatorSummary := operatorResult.Payload.(task.SummaryVersion)
	if operatorSummary.ContinuationID != "" || operatorSummary.Summary != "Operator note" {
		t.Fatalf("operator Summary = %+v", operatorSummary)
	}
	if _, err := db.Exec(`DELETE FROM blackboard_compatibility_results WHERE project_id=? AND idempotency_key=?`, projectRow.ID, "summary:operator"); err != nil {
		t.Fatalf("simulate lost generic Summary result: %v", err)
	}
	operatorReplay, err := compatibility.Call(ctx, blackboardcompat.LegacyCall{
		Kind: blackboardcompat.CallPutTaskSummary, Transport: blackboardcompat.TransportCLI,
		ProjectID: projectRow.ID, Principal: operator, IdempotencyKey: "summary:operator",
		TaskSummary: &blackboardcompat.TaskSummaryWrite{TaskID: createdTask.ID, Summary: "Operator note", SubmittedBy: "operator:test"},
	})
	if err != nil {
		t.Fatalf("replay operator Task Summary: %v", err)
	}
	if operatorReplay.Payload.(task.SummaryVersion).ID != operatorSummary.ID {
		t.Fatalf("operator replay = %+v, want exact Summary %s", operatorReplay.Payload, operatorSummary.ID)
	}

	configVersion, err := tasks.RecordRuntimeConfig(createdTask.ID, "profile", map[string]any{"model": "test"})
	if err != nil {
		t.Fatalf("record Runtime Configuration Version: %v", err)
	}
	continuation, err := tasks.CreateContinuation(createdTask.ID, "profile", "codex", task.RunnerSandbox)
	if err != nil {
		t.Fatalf("create Continuation: %v", err)
	}
	grants := projectinterface.NewGrantStore(db, projectinterface.SystemClock{}, projectinterface.RandomIDSource{}, projectinterface.RandomTokenSource{})
	token, grant, err := grants.Issue(ctx, projectinterface.IssueGrantRequest{
		ProjectID: projectRow.ID, TaskID: createdTask.ID, ContinuationID: continuation.ID,
		RuntimeConfigVersionID: configVersion.ID, RuntimeProfileID: "profile", RuntimePluginID: "codex", Runner: string(task.RunnerSandbox),
	})
	if err != nil {
		t.Fatalf("issue Continuation Interface Grant: %v", err)
	}
	runtimePrincipal := projectinterface.Principal{
		Grant: grant, ActorType: blackboard.ActorTypeRuntime, ActorID: grant.ActorID, ProjectID: projectRow.ID,
	}

	finished, err := compatibility.Call(ctx, blackboardcompat.LegacyCall{
		Kind: blackboardcompat.CallPutTaskSummary, Transport: blackboardcompat.TransportMCP,
		ProjectID: projectRow.ID, Principal: runtimePrincipal, IdempotencyKey: "summary:finish",
		TaskSummary: &blackboardcompat.TaskSummaryWrite{TaskID: createdTask.ID, Summary: "Runtime handoff"},
	})
	if err != nil {
		t.Fatalf("Finish through submit_task_summary: %v", err)
	}
	finishSummary := finished.Payload.(task.SummaryVersion)
	if finishSummary.ContinuationID != continuation.ID || finishSummary.Summary != "Runtime handoff" {
		t.Fatalf("Finish Summary = %+v", finishSummary)
	}
	closedGrant, err := grants.Resolve(ctx, token)
	if err != nil || closedGrant.Status() != projectinterface.GrantStatusFinished {
		t.Fatalf("closed grant = %+v err=%v", closedGrant, err)
	}

	secondTask, err := tasks.Create(task.CreateRequest{
		ProjectID: projectRow.ID, Goal: "Open Attempt", RuntimeProfileID: "profile", Runner: task.RunnerSandbox,
	})
	if err != nil {
		t.Fatalf("create second Task: %v", err)
	}
	secondConfig, _ := tasks.RecordRuntimeConfig(secondTask.ID, "profile", map[string]any{"model": "test"})
	secondContinuation, _ := tasks.CreateContinuation(secondTask.ID, "profile", "codex", task.RunnerSandbox)
	_, secondGrant, err := grants.Issue(ctx, projectinterface.IssueGrantRequest{
		ProjectID: projectRow.ID, TaskID: secondTask.ID, ContinuationID: secondContinuation.ID,
		RuntimeConfigVersionID: secondConfig.ID, RuntimeProfileID: "profile", RuntimePluginID: "codex", Runner: string(task.RunnerSandbox),
	})
	if err != nil {
		t.Fatalf("issue second grant: %v", err)
	}
	entity, err := graph.Apply(ctx, blackboard.MutationBatch{
		SchemaVersion: 1, IdempotencyKey: "summary:entity",
		Context: blackboard.SystemExecutionContext(projectRow.ID, projectRow.Kind, "fixture"),
		Operations: []blackboard.Operation{{
			OpID: "entity", Kind: blackboard.OpCreateNode,
			Node:   blackboard.NodeRef{NodeType: blackboard.NodeTypeEntity, StableKey: "entity:target"},
			Create: blackboard.CreateNodeInput{PropertyMap: map[string]any{"kind": "host", "name": "Target", "locator": "target.test", "scope_status": "in_scope"}},
		}},
	})
	if err != nil {
		t.Fatalf("create Attempt target: %v", err)
	}
	_, err = graph.Apply(ctx, blackboard.MutationBatch{
		SchemaVersion: 1, IdempotencyKey: "summary:attempt",
		Context: blackboard.ExecutionContext{
			ProjectID: projectRow.ID, ProjectKind: projectRow.Kind, ActorType: blackboard.ActorTypeRuntime, ActorID: secondGrant.ActorID,
			TaskID: secondTask.ID, ContinuationID: secondContinuation.ID, RuntimeProfileID: "profile", Runner: string(task.RunnerSandbox), InterfaceGrantID: secondGrant.ID,
		},
		Operations: []blackboard.Operation{
			{OpID: "attempt", Kind: blackboard.OpCreateNode, Node: blackboard.NodeRef{NodeType: blackboard.NodeTypeAttempt, StableKey: "attempt:open"}, Create: blackboard.CreateNodeInput{PropertyMap: map[string]any{"status": "open"}}},
			{OpID: "tests", Kind: blackboard.OpPutEdge, PutEdge: blackboard.PutEdgeInput{EdgeType: blackboard.EdgeTypeTests, From: blackboard.NodeRef{OpID: "attempt"}, To: blackboard.NodeRef{ID: entity.Operations[0].NodeID}}},
		},
	})
	if err != nil {
		t.Fatalf("create open Attempt: %v", err)
	}
	_, err = compatibility.Call(ctx, blackboardcompat.LegacyCall{
		Kind: blackboardcompat.CallPutTaskSummary, Transport: blackboardcompat.TransportMCP,
		ProjectID:      projectRow.ID,
		Principal:      projectinterface.Principal{Grant: secondGrant, ActorType: blackboard.ActorTypeRuntime, ActorID: secondGrant.ActorID, ProjectID: projectRow.ID},
		IdempotencyKey: "summary:open-attempt",
		TaskSummary:    &blackboardcompat.TaskSummaryWrite{TaskID: secondTask.ID, Summary: "Too early"},
	})
	var interfaceErr *projectinterface.Error
	if !errors.As(err, &interfaceErr) || interfaceErr.Code != projectinterface.ErrCodeContinuationOpenAttempts {
		t.Fatalf("open-Attempt Finish error = %v, want continuation_open_attempts", err)
	}
}

func TestLegacyReportDelegatesToPentestReportProjection(t *testing.T) {
	_, compatibility, _, projectRow, principal, _ := newCompatibilityFixture(t)
	result, err := compatibility.Call(context.Background(), blackboardcompat.LegacyCall{
		Kind: blackboardcompat.CallGenerateReport, Transport: blackboardcompat.TransportHTTP,
		ProjectID: projectRow.ID, Principal: principal, IdempotencyKey: "report:generate",
		Report: &blackboardcompat.ReportWrite{},
	})
	if err != nil {
		t.Fatalf("generate compatibility report: %v", err)
	}
	report := result.Payload.(blackboard.LegacyReportEnvelopeV1)
	if report.Status != "generated" || report.Format != "markdown" || report.Markdown == "" {
		t.Fatalf("legacy report = %+v", report)
	}
	if result.Mutation.MutationSequence != 0 {
		t.Fatalf("report unexpectedly created mutation %+v", result.Mutation)
	}
}

func TestUnkeyedCompatibilityWriteIsBestEffortRatherThanExactReplay(t *testing.T) {
	_, compatibility, graph, projectRow, principal, _ := newCompatibilityFixture(t)
	call := blackboardcompat.LegacyCall{
		Kind: blackboardcompat.CallUpsertFact, Transport: blackboardcompat.TransportHTTP,
		ProjectID: projectRow.ID, Principal: principal,
		Fact: &blackboardcompat.FactWrite{
			FactKey: "fact:unkeyed", Category: "service", Summary: "Unkeyed write",
			Confidence: "tentative", ScopeStatus: "in_scope",
		},
	}
	first, err := compatibility.Call(context.Background(), call)
	if err != nil {
		t.Fatalf("first unkeyed write: %v", err)
	}
	second, err := compatibility.Call(context.Background(), call)
	if err != nil {
		t.Fatalf("second unkeyed write: %v", err)
	}
	if first.Mutation.MutationSequence == second.Mutation.MutationSequence {
		t.Fatalf("unkeyed call unexpectedly received exact replay sequence %d", first.Mutation.MutationSequence)
	}
	stored, err := graph.ReadNode(context.Background(), blackboard.ReadNodeRequest{ProjectID: projectRow.ID, NodeType: blackboard.NodeTypeProjectFact, Key: "fact:unkeyed"})
	if err != nil || stored.Node.Version != 1 || stored.ObservedGraphRevision != 1 {
		t.Fatalf("unkeyed convergence = %+v err=%v", stored, err)
	}
}

func TestCompatibilityUseCounterRecordsNoRequestPayload(t *testing.T) {
	db, _, graph, projectRow, principal, _ := newCompatibilityFixture(t)
	counter := &recordingUseCounter{}
	tasks := task.NewService(db)
	projectInterface := projectinterface.NewService(projectinterface.Deps{DB: db, Graph: graph, Tasks: tasks})
	compatibility := blackboardcompat.NewService(blackboardcompat.Deps{
		DB: db, Graph: graph, Reads: blackboard.NewBlackboardReadService(db),
		ProjectInterface: projectInterface, Tasks: tasks, UseCounter: counter,
	})
	_, err := compatibility.Call(context.Background(), blackboardcompat.LegacyCall{
		Kind: blackboardcompat.CallUpsertFact, Transport: blackboardcompat.TransportMCP,
		ProjectID: projectRow.ID, Principal: principal, IdempotencyKey: "counter:fact",
		Fact: &blackboardcompat.FactWrite{
			FactKey: "fact:counter", Category: "secret-category", Summary: "sensitive summary",
			Body: "sensitive body", Confidence: "tentative", ScopeStatus: "in_scope",
		},
	})
	if err != nil {
		t.Fatalf("compatibility call: %v", err)
	}
	want := blackboardcompat.Use{ProjectID: projectRow.ID, Transport: blackboardcompat.TransportMCP, Kind: blackboardcompat.CallUpsertFact, Mode: blackboardcompat.UseModeWrite}
	if len(counter.uses) != 1 || counter.uses[0] != want {
		t.Fatalf("counter uses = %+v, want %+v", counter.uses, want)
	}
}

type recordingUseCounter struct {
	uses []blackboardcompat.Use
}

func (counter *recordingUseCounter) Increment(_ context.Context, use blackboardcompat.Use) error {
	counter.uses = append(counter.uses, use)
	return nil
}

func newCompatibilityFixture(t *testing.T) (*store.DB, *blackboardcompat.Service, *blackboard.GraphService, project.Project, projectinterface.Principal, string) {
	t.Helper()
	root := t.TempDir()
	db, err := store.Open(filepath.Join(root, "compatibility.db"))
	if err != nil {
		t.Fatalf("open file-backed store: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	projectRow, err := project.NewService(db).Create("Compatibility", "", project.Scope{}, project.Defaults{})
	if err != nil {
		t.Fatalf("create Project: %v", err)
	}
	if _, err := db.Exec(`UPDATE blackboard_store_state SET canonical_store=?,cutover_state='graph' WHERE id=1`, store.CanonicalStoreGraphV1); err != nil {
		t.Fatalf("activate disposable graph epoch: %v", err)
	}

	graph := blackboard.NewGraphService(db, blackboard.SystemClock{}, blackboard.RandomIDSource{})
	tasks := task.NewService(db)
	if err := os.MkdirAll(filepath.Join(root, "managed"), 0o700); err != nil {
		t.Fatalf("create managed Artifact Root: %v", err)
	}
	projectInterface := projectinterface.NewService(projectinterface.Deps{
		DB: db, Graph: graph, Tasks: tasks,
		ArtifactRoot: filepath.Join(root, "managed"), RuntimeRoot: filepath.Join(root, "runs"),
		OperatorRoots: []string{root},
	})
	compatibility := blackboardcompat.NewService(blackboardcompat.Deps{
		DB: db, Graph: graph, Reads: blackboard.NewBlackboardReadService(db),
		ProjectInterface: projectInterface, Tasks: tasks,
	})
	principal, err := projectinterface.OperatorPrincipal(projectRow.ID, "operator:compatibility-test")
	if err != nil {
		t.Fatalf("create operator principal: %v", err)
	}

	return db, compatibility, graph, projectRow, principal, root
}
