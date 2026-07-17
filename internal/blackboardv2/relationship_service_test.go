package blackboardv2_test

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"pentest/internal/blackboardv2"
	"pentest/internal/store"
)

func TestFactDerivedFromEvidenceUsesRelationshipContractEverywhere(t *testing.T) {
	fixture := newEvidenceV2Fixture(t, "Fact derivation grammar")
	fixture.writeSource(t, "derivation.txt", "derived fact source")
	if _, err := fixture.service.RetainEvidenceForContinuation(context.Background(), fixture.projectID, fixture.continuationID, blackboardv2.RetainEvidenceRequest{
		IdempotencyKey: "retain-derivation-evidence",
		Key:            "evidence:derivation",
		Attempt:        "attempt:evidence",
		SourcePath:     "derivation.txt",
		ArtifactType:   "terminal_capture",
		Summary:        "Source material for a derived fact",
	}); err != nil {
		t.Fatalf("retain derivation Evidence: %v", err)
	}

	created, err := fixture.service.Apply(context.Background(), fixture.projectID, blackboardv2.ChangeBatch{
		Schema: "semantic-change-batch/v2", IdempotencyKey: "fact-derived-from-evidence",
		Changes: []blackboardv2.Change{
			{Op: "create", Key: "fact:derived", Type: "fact", Record: blackboardv2.FactRecord{Category: "analysis", Summary: "The response discloses a service version", Confidence: "tentative", ScopeStatus: "in_scope"}},
			{Op: "relate", From: "fact:derived", Relation: "derived_from", To: "evidence:derivation"},
		},
	})
	if err != nil {
		t.Fatalf("relate Fact derived from Evidence: %v", err)
	}
	if len(created.Relations) != 1 || !reflect.DeepEqual(created.Relations[0], blackboardv2.RelationVersionTuple{"fact:derived", "derived_from", "evidence:derivation", 1}) {
		t.Fatalf("derived_from result = %#v", created.Relations)
	}

	detail, err := fixture.service.ReadCurrent(context.Background(), fixture.projectID, "fact:derived")
	if err != nil {
		t.Fatalf("read derived Fact detail: %v", err)
	}
	wantRelation := blackboardv2.RelationshipTuple{"fact:derived", "derived_from", "evidence:derivation"}
	if len(detail.Relationships) != 1 || !reflect.DeepEqual(detail.Relationships[0], wantRelation) {
		t.Fatalf("derived Fact relationships = %#v, want %#v", detail.Relationships, wantRelation)
	}
	snapshot, err := fixture.service.RuntimeSnapshot(context.Background(), fixture.projectID)
	if err != nil {
		t.Fatalf("read Snapshot with Fact derivation: %v", err)
	}
	if len(snapshot.Relations) != 3 || !relationshipTuplePresent(snapshot.Relations, wantRelation) {
		t.Fatalf("Snapshot relationships = %#v, want Fact derivation", snapshot.Relations)
	}
}

func TestAtomicFactSupersessionArchivesRecordRelationsAndReplays(t *testing.T) {
	fixture := newEvidenceV2Fixture(t, "Fact supersession")
	service := fixture.service
	ctx := context.Background()
	_, err := service.Apply(ctx, fixture.projectID, blackboardv2.ChangeBatch{
		Schema: "semantic-change-batch/v2", IdempotencyKey: "seed-fact-supersession",
		Changes: []blackboardv2.Change{
			{Op: "create", Key: "entity:fact-target", Type: "entity", Record: blackboardv2.EntityRecord{Status: "active", Kind: "service", Name: "Fact target", ScopeStatus: "in_scope"}},
			{Op: "create", Key: "fact:old", Type: "fact", Record: blackboardv2.FactRecord{Category: "service", Summary: "The service runs version one", Confidence: "tentative", ScopeStatus: "in_scope"}},
			{Op: "create", Key: "fact:new", Type: "fact", Record: blackboardv2.FactRecord{Category: "service", Summary: "The service runs version two", Confidence: "tentative", ScopeStatus: "in_scope"}},
			{Op: "relate", From: "fact:old", Relation: "about", To: "entity:fact-target"},
		},
	})
	if err != nil {
		t.Fatalf("seed Fact supersession: %v", err)
	}

	request := blackboardv2.ChangeBatch{
		Schema: "semantic-change-batch/v2", IdempotencyKey: "supersede-fact",
		Changes: []blackboardv2.Change{{Op: "supersede", Replacement: "fact:new", ReplacementVersion: 1, Replaced: "fact:old", ReplacedVersion: 1}},
	}
	result, err := service.Apply(ctx, fixture.projectID, request)
	if err != nil {
		t.Fatalf("supersede Fact: %v", err)
	}
	if len(result.Records) != 1 || !reflect.DeepEqual(result.Records[0], blackboardv2.RecordVersionTuple{"fact:old", 2}) {
		t.Fatalf("superseded Fact result records = %#v", result.Records)
	}
	if len(result.Relations) != 1 || !reflect.DeepEqual(result.Relations[0], blackboardv2.RelationVersionTuple{"fact:new", "supersedes", "fact:old", 1}) {
		t.Fatalf("superseded Fact result relations = %#v", result.Relations)
	}
	replay, err := service.Apply(ctx, fixture.projectID, request)
	if err != nil || !reflect.DeepEqual(replay, result) {
		t.Fatalf("supersede replay = %#v, %v; want %#v", replay, err, result)
	}
	if _, err := service.ReadCurrent(ctx, fixture.projectID, "fact:old"); !isSemanticCode(err, "not_found") {
		t.Fatalf("superseded Fact current read error = %#v, want not_found", err)
	}
	current, err := service.ReadCurrent(ctx, fixture.projectID, "fact:new")
	if err != nil {
		t.Fatalf("read replacement Fact: %v", err)
	}
	if len(current.Relationships) != 0 {
		t.Fatalf("replacement retained supersession history in current detail: %#v", current.Relationships)
	}
	history, err := service.ReadHistory(ctx, fixture.projectID, "fact:old", blackboardv2.HistoryOptions{})
	if err != nil {
		t.Fatalf("read superseded Fact history: %v", err)
	}
	if len(history.Items) != 4 || history.Items[1].Record == nil || history.Items[1].Record.Confidence != "deprecated" || history.Items[2].Relation != "about" || history.Items[3].Relation != "supersedes" {
		t.Fatalf("superseded Fact history = %#v", history.Items)
	}
	snapshot, err := service.RuntimeSnapshot(ctx, fixture.projectID)
	if err != nil {
		t.Fatalf("read Snapshot after Fact supersession: %v", err)
	}
	raw := string(mustJSON(t, snapshot))
	if strings.Contains(raw, "fact:old") || !strings.Contains(raw, "fact:new") {
		t.Fatalf("Snapshot retained replaced Fact or omitted replacement: %s", raw)
	}
	if err := fixture.db.Close(); err != nil {
		t.Fatalf("close store before supersession reload: %v", err)
	}
	reopened, err := store.Open(fixture.dbPath)
	if err != nil {
		t.Fatalf("reopen store after Fact supersession: %v", err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	reloaded := blackboardv2.NewService(reopened)
	if _, err := reloaded.ReadCurrent(ctx, fixture.projectID, "fact:old"); !isSemanticCode(err, "not_found") {
		t.Fatalf("reloaded superseded Fact current read error = %#v", err)
	}
	reloadedHistory, err := reloaded.ReadHistory(ctx, fixture.projectID, "fact:old", blackboardv2.HistoryOptions{})
	if err != nil || !reflect.DeepEqual(reloadedHistory.Items, history.Items) {
		t.Fatalf("reloaded superseded Fact history = %#v, %v; want %#v", reloadedHistory.Items, err, history.Items)
	}
}

func TestFactSupersessionVersionFailureIsAtomic(t *testing.T) {
	fixture := newEvidenceV2Fixture(t, "Atomic Fact supersession")
	ctx := context.Background()
	_, err := fixture.service.Apply(ctx, fixture.projectID, blackboardv2.ChangeBatch{
		Schema: "semantic-change-batch/v2", IdempotencyKey: "seed-atomic-fact-supersession",
		Changes: []blackboardv2.Change{
			{Op: "create", Key: "fact:atomic-old", Type: "fact", Record: blackboardv2.FactRecord{Category: "service", Summary: "Old atomic fact", Confidence: "tentative", ScopeStatus: "unknown"}},
			{Op: "create", Key: "fact:atomic-new", Type: "fact", Record: blackboardv2.FactRecord{Category: "service", Summary: "New atomic fact", Confidence: "tentative", ScopeStatus: "unknown"}},
		},
	})
	if err != nil {
		t.Fatalf("seed atomic Fact supersession: %v", err)
	}
	_, err = fixture.service.Apply(ctx, fixture.projectID, blackboardv2.ChangeBatch{
		Schema: "semantic-change-batch/v2", IdempotencyKey: "stale-fact-supersession",
		Changes: []blackboardv2.Change{
			{Op: "create", Key: "fact:must-rollback", Type: "fact", Record: blackboardv2.FactRecord{Category: "test", Summary: "Must roll back", Confidence: "tentative", ScopeStatus: "unknown"}},
			{Op: "supersede", Replacement: "fact:atomic-new", ReplacementVersion: 1, Replaced: "fact:atomic-old", ReplacedVersion: 99},
		},
	})
	var semanticErr *blackboardv2.Error
	if !errors.As(err, &semanticErr) || semanticErr.Code != "version_conflict" || semanticErr.Path != "changes[1].replaced_version" {
		t.Fatalf("stale supersession error = %#v", err)
	}
	if _, err := fixture.service.ReadCurrent(ctx, fixture.projectID, "fact:must-rollback"); !isSemanticCode(err, "not_found") {
		t.Fatalf("stale supersession retained partial mutation: %#v", err)
	}
}

func TestFactSupersessionPreservesConfirmedFactBasis(t *testing.T) {
	fixture := newEvidenceV2Fixture(t, "Fact supersession basis")
	ctx := context.Background()
	_, err := fixture.service.Apply(ctx, fixture.projectID, blackboardv2.ChangeBatch{
		Schema: "semantic-change-batch/v2", IdempotencyKey: "seed-fact-supersession-basis",
		Changes: []blackboardv2.Change{
			{Op: "create", Key: "fact:basis-old", Type: "fact", Record: blackboardv2.FactRecord{Category: "validation", Summary: "Confirmed supporting fact", Confidence: "confirmed", ScopeStatus: "in_scope"}},
			{Op: "create", Key: "fact:basis-new", Type: "fact", Record: blackboardv2.FactRecord{Category: "validation", Summary: "Confirmed replacement fact", Confidence: "confirmed", ScopeStatus: "in_scope"}},
			{Op: "create", Key: "fact:dependent", Type: "fact", Record: blackboardv2.FactRecord{Category: "finding", Summary: "Dependent confirmed fact", Confidence: "tentative", ScopeStatus: "in_scope"}},
			{Op: "relate", From: "fact:basis-old", Relation: "supports", To: "fact:dependent", Reason: "Independent confirmation"},
			{Op: "transition", Key: "fact:dependent", Version: 1, Status: "confirmed"},
		},
	})
	if err != nil {
		t.Fatalf("seed confirmed Fact dependency: %v", err)
	}
	_, err = fixture.service.Apply(ctx, fixture.projectID, blackboardv2.ChangeBatch{
		Schema: "semantic-change-batch/v2", IdempotencyKey: "reject-stranding-fact-supersession",
		Changes: []blackboardv2.Change{{Op: "supersede", Replacement: "fact:basis-new", ReplacementVersion: 1, Replaced: "fact:basis-old", ReplacedVersion: 1}},
	})
	if !isSemanticCode(err, "semantic_validation") {
		t.Fatalf("stranding Fact supersession error = %#v", err)
	}
	if _, err := fixture.service.ReadCurrent(ctx, fixture.projectID, "fact:basis-old"); err != nil {
		t.Fatalf("failed Fact supersession removed supporting Fact: %v", err)
	}
	dependent, err := fixture.service.ReadCurrent(ctx, fixture.projectID, "fact:dependent")
	if err != nil || dependent.Record.Confidence != "confirmed" || len(dependent.Relationships) != 1 {
		t.Fatalf("failed Fact supersession changed dependent Fact: %#v, %v", dependent, err)
	}

	_, err = fixture.service.Apply(ctx, fixture.projectID, blackboardv2.ChangeBatch{
		Schema: "semantic-change-batch/v2", IdempotencyKey: "add-replacement-fact-basis",
		Changes: []blackboardv2.Change{{Op: "relate", From: "fact:basis-new", Relation: "supports", To: "fact:dependent", Reason: "Replacement independently confirms the conclusion"}},
	})
	if err != nil {
		t.Fatalf("add replacement Fact basis: %v", err)
	}
	if _, err := fixture.service.Apply(ctx, fixture.projectID, blackboardv2.ChangeBatch{
		Schema: "semantic-change-batch/v2", IdempotencyKey: "supersede-with-replacement-fact-basis",
		Changes: []blackboardv2.Change{{Op: "supersede", Replacement: "fact:basis-new", ReplacementVersion: 1, Replaced: "fact:basis-old", ReplacedVersion: 1}},
	}); err != nil {
		t.Fatalf("supersede Fact with replacement basis: %v", err)
	}
}

func TestConfirmedSupportingFactDemotionPreservesDependentBasisAtomically(t *testing.T) {
	fixture := newEvidenceV2Fixture(t, "Supporting Fact demotion basis")
	ctx := context.Background()
	_, err := fixture.service.Apply(ctx, fixture.projectID, blackboardv2.ChangeBatch{
		Schema: "semantic-change-batch/v2", IdempotencyKey: "seed-supporting-fact-demotion",
		Changes: []blackboardv2.Change{
			{Op: "create", Key: "fact:demotion-support", Type: "fact", Record: blackboardv2.FactRecord{Category: "validation", Summary: "Confirmed source S", Confidence: "confirmed", ScopeStatus: "in_scope"}},
			{Op: "create", Key: "fact:demotion-target", Type: "fact", Record: blackboardv2.FactRecord{Category: "validation", Summary: "Confirmed target T", Confidence: "tentative", ScopeStatus: "in_scope"}},
			{Op: "relate", From: "fact:demotion-support", Relation: "supports", To: "fact:demotion-target", Reason: "Independent reproduction confirms the target"},
			{Op: "transition", Key: "fact:demotion-target", Version: 1, Status: "confirmed"},
		},
	})
	if err != nil {
		t.Fatalf("seed supporting Fact demotion: %v", err)
	}
	_, err = fixture.service.Apply(ctx, fixture.projectID, blackboardv2.ChangeBatch{
		Schema: "semantic-change-batch/v2", IdempotencyKey: "reject-stranding-support-demotion",
		Changes: []blackboardv2.Change{
			{Op: "create", Key: "fact:demotion-rollback", Type: "fact", Record: blackboardv2.FactRecord{Category: "test", Summary: "Must roll back", Confidence: "tentative", ScopeStatus: "unknown"}},
			{Op: "transition", Key: "fact:demotion-support", Version: 1, Status: "tentative"},
		},
	})
	var semanticErr *blackboardv2.Error
	if !errors.As(err, &semanticErr) || semanticErr.Code != "semantic_validation" || semanticErr.Path != "changes[1].status" || semanticErr.Details["fact"] != "fact:demotion-target" {
		t.Fatalf("supporting Fact demotion error = %#v", err)
	}
	if _, err := fixture.service.ReadCurrent(ctx, fixture.projectID, "fact:demotion-rollback"); !isSemanticCode(err, "not_found") {
		t.Fatalf("supporting Fact demotion retained rollback marker: %#v", err)
	}
	support, err := fixture.service.ReadCurrent(ctx, fixture.projectID, "fact:demotion-support")
	if err != nil || support.Version != 1 || support.Record.Confidence != "confirmed" {
		t.Fatalf("failed demotion changed supporting Fact: %#v, %v", support, err)
	}
	target, err := fixture.service.ReadCurrent(ctx, fixture.projectID, "fact:demotion-target")
	if err != nil || target.Record.Confidence != "confirmed" || len(target.Relationships) != 1 {
		t.Fatalf("failed demotion changed dependent Fact: %#v, %v", target, err)
	}
}

func TestRelationshipIdentityVersionContinuityAcrossRemovalAndRecreation(t *testing.T) {
	fixture := newEvidenceV2Fixture(t, "Relationship identity continuity")
	ctx := context.Background()
	_, err := fixture.service.Apply(ctx, fixture.projectID, blackboardv2.ChangeBatch{
		Schema: "semantic-change-batch/v2", IdempotencyKey: "seed-relationship-identity",
		Changes: []blackboardv2.Change{
			{Op: "create", Key: "fact:identity-source", Type: "fact", Record: blackboardv2.FactRecord{Category: "identity", Summary: "Identity source", Confidence: "tentative", ScopeStatus: "unknown"}},
			{Op: "create", Key: "fact:identity-target", Type: "fact", Record: blackboardv2.FactRecord{Category: "identity", Summary: "Identity target", Confidence: "tentative", ScopeStatus: "unknown"}},
			{Op: "relate", From: "fact:identity-source", Relation: "contradicts", To: "fact:identity-target", Reason: "Initial independent conflict"},
		},
	})
	if err != nil {
		t.Fatalf("seed relationship identity: %v", err)
	}
	if _, err := fixture.service.Apply(ctx, fixture.projectID, blackboardv2.ChangeBatch{
		Schema: "semantic-change-batch/v2", IdempotencyKey: "remove-relationship-v1",
		Changes: []blackboardv2.Change{{Op: "unrelate", From: "fact:identity-source", Relation: "contradicts", To: "fact:identity-target", Version: 1}},
	}); err != nil {
		t.Fatalf("unrelate relationship v1: %v", err)
	}
	recreated, err := fixture.service.Apply(ctx, fixture.projectID, blackboardv2.ChangeBatch{
		Schema: "semantic-change-batch/v2", IdempotencyKey: "recreate-relationship-v2",
		Changes: []blackboardv2.Change{{Op: "relate", From: "fact:identity-source", Relation: "contradicts", To: "fact:identity-target", Reason: "Recreated independent conflict"}},
	})
	if err != nil {
		t.Fatalf("recreate relationship v2: %v", err)
	}
	if len(recreated.Relations) != 1 || recreated.Relations[0][3] != 2 {
		t.Fatalf("recreated relationship result = %#v, want version 2", recreated.Relations)
	}
	_, err = fixture.service.Apply(ctx, fixture.projectID, blackboardv2.ChangeBatch{
		Schema: "semantic-change-batch/v2", IdempotencyKey: "reject-stale-recreated-reason",
		Changes: []blackboardv2.Change{{Op: "relate", From: "fact:identity-source", Relation: "contradicts", To: "fact:identity-target", Version: 1, Reason: "Stale reason change"}},
	})
	var semanticErr *blackboardv2.Error
	if !errors.As(err, &semanticErr) || semanticErr.Code != "version_conflict" || semanticErr.Path != "changes[0].version" || semanticErr.Details["current_version"] != float64(2) {
		t.Fatalf("stale recreated relationship error = %#v", err)
	}
	updated, err := fixture.service.Apply(ctx, fixture.projectID, blackboardv2.ChangeBatch{
		Schema: "semantic-change-batch/v2", IdempotencyKey: "update-recreated-reason-v3",
		Changes: []blackboardv2.Change{{Op: "relate", From: "fact:identity-source", Relation: "contradicts", To: "fact:identity-target", Version: 2, Reason: "Current informative reason"}},
	})
	if err != nil || len(updated.Relations) != 1 || updated.Relations[0][3] != 3 {
		t.Fatalf("update recreated relationship = %#v, %v", updated.Relations, err)
	}
	if _, err := fixture.service.Apply(ctx, fixture.projectID, blackboardv2.ChangeBatch{
		Schema: "semantic-change-batch/v2", IdempotencyKey: "remove-relationship-v3",
		Changes: []blackboardv2.Change{{Op: "unrelate", From: "fact:identity-source", Relation: "contradicts", To: "fact:identity-target", Version: 3}},
	}); err != nil {
		t.Fatalf("unrelate relationship v3: %v", err)
	}
	recreatedAgain, err := fixture.service.Apply(ctx, fixture.projectID, blackboardv2.ChangeBatch{
		Schema: "semantic-change-batch/v2", IdempotencyKey: "recreate-relationship-v4",
		Changes: []blackboardv2.Change{{Op: "relate", From: "fact:identity-source", Relation: "contradicts", To: "fact:identity-target", Reason: "Second recreated conflict"}},
	})
	if err != nil || len(recreatedAgain.Relations) != 1 || recreatedAgain.Relations[0][3] != 4 {
		t.Fatalf("second recreated relationship = %#v, %v", recreatedAgain.Relations, err)
	}
}

func TestMergeRewrittenRelationshipUsesCollisionSafeCanonicalVersion(t *testing.T) {
	fixture := newEvidenceV2Fixture(t, "Merge relationship version collision")
	ctx := context.Background()
	_, err := fixture.service.Apply(ctx, fixture.projectID, blackboardv2.ChangeBatch{
		Schema: "semantic-change-batch/v2", IdempotencyKey: "seed-merge-version-collision",
		Changes: []blackboardv2.Change{
			{Op: "create", Key: "fact:merge-version-source", Type: "fact", Record: blackboardv2.FactRecord{Category: "duplicate", Summary: "Same merge version fact", Confidence: "tentative", ScopeStatus: "unknown"}},
			{Op: "create", Key: "fact:merge-version-canonical", Type: "fact", Record: blackboardv2.FactRecord{Category: "duplicate", Summary: "same merge version fact", Confidence: "tentative", ScopeStatus: "unknown"}},
			{Op: "create", Key: "fact:merge-version-target", Type: "fact", Record: blackboardv2.FactRecord{Category: "target", Summary: "Merge version target", Confidence: "tentative", ScopeStatus: "unknown"}},
			{Op: "relate", From: "fact:merge-version-canonical", Relation: "contradicts", To: "fact:merge-version-target", Reason: "Archived canonical conflict"},
		},
	})
	if err != nil {
		t.Fatalf("seed merge relationship collision: %v", err)
	}
	if _, err := fixture.service.Apply(ctx, fixture.projectID, blackboardv2.ChangeBatch{
		Schema: "semantic-change-batch/v2", IdempotencyKey: "archive-canonical-relationship-v1",
		Changes: []blackboardv2.Change{{Op: "unrelate", From: "fact:merge-version-canonical", Relation: "contradicts", To: "fact:merge-version-target", Version: 1}},
	}); err != nil {
		t.Fatalf("archive canonical relationship v1: %v", err)
	}
	if _, err := fixture.service.Apply(ctx, fixture.projectID, blackboardv2.ChangeBatch{
		Schema: "semantic-change-batch/v2", IdempotencyKey: "create-source-relationship-v1",
		Changes: []blackboardv2.Change{{Op: "relate", From: "fact:merge-version-source", Relation: "contradicts", To: "fact:merge-version-target", Reason: "Source conflict to rewrite"}},
	}); err != nil {
		t.Fatalf("create source relationship: %v", err)
	}
	merged, err := fixture.service.Apply(ctx, fixture.projectID, blackboardv2.ChangeBatch{
		Schema: "semantic-change-batch/v2", IdempotencyKey: "merge-with-archived-canonical-collision",
		Changes: []blackboardv2.Change{{Op: "merge", Source: "fact:merge-version-source", SourceVersion: 1, Canonical: "fact:merge-version-canonical", CanonicalVersion: 1}},
	})
	if err != nil {
		t.Fatalf("merge with archived canonical collision: %v", err)
	}
	if len(merged.Relations) != 1 || merged.Relations[0][3] != 2 {
		t.Fatalf("merge rewritten relationship = %#v, want version 2", merged.Relations)
	}
	if _, err := fixture.service.Apply(ctx, fixture.projectID, blackboardv2.ChangeBatch{
		Schema: "semantic-change-batch/v2", IdempotencyKey: "unrelate-merge-rewrite-v2",
		Changes: []blackboardv2.Change{{Op: "unrelate", From: "fact:merge-version-canonical", Relation: "contradicts", To: "fact:merge-version-target", Version: 2}},
	}); err != nil {
		t.Fatalf("unrelate collision-safe merge rewrite: %v", err)
	}
	history, err := fixture.service.ReadHistory(ctx, fixture.projectID, "fact:merge-version-canonical", blackboardv2.HistoryOptions{})
	if err != nil {
		t.Fatalf("read canonical merge relationship history: %v", err)
	}
	versions := make([]int, 0, 2)
	for _, item := range history.Items {
		if item.Kind == "relationship" && item.From == "fact:merge-version-canonical" && item.Relation == "contradicts" && item.To == "fact:merge-version-target" {
			versions = append(versions, item.Version)
		}
	}
	if !reflect.DeepEqual(versions, []int{1, 2}) {
		t.Fatalf("canonical relationship history versions = %#v, want [1 2]", versions)
	}
}

func TestRelationshipCyclesContradictionAndRedirectSelfLinks(t *testing.T) {
	fixture := newEvidenceV2Fixture(t, "Relationship cycles and redirects")
	ctx := context.Background()
	_, err := fixture.service.Apply(ctx, fixture.projectID, blackboardv2.ChangeBatch{
		Schema: "semantic-change-batch/v2", IdempotencyKey: "seed-relationship-cycle-redirects",
		Changes: []blackboardv2.Change{
			{Op: "create", Key: "fact:lineage-a", Type: "fact", Record: blackboardv2.FactRecord{Category: "lineage", Summary: "Lineage A", Confidence: "tentative", ScopeStatus: "unknown"}},
			{Op: "create", Key: "fact:lineage-b", Type: "fact", Record: blackboardv2.FactRecord{Category: "lineage", Summary: "Lineage B", Confidence: "tentative", ScopeStatus: "unknown"}},
			{Op: "create", Key: "fact:lineage-c", Type: "fact", Record: blackboardv2.FactRecord{Category: "lineage", Summary: "Lineage C", Confidence: "tentative", ScopeStatus: "unknown"}},
			{Op: "relate", From: "fact:lineage-a", Relation: "derived_from", To: "fact:lineage-b"},
			{Op: "relate", From: "fact:lineage-b", Relation: "derived_from", To: "fact:lineage-c"},
			{Op: "relate", From: "fact:lineage-a", Relation: "contradicts", To: "fact:lineage-b", Reason: "A conflicts with B"},
			{Op: "relate", From: "fact:lineage-b", Relation: "contradicts", To: "fact:lineage-a", Reason: "B independently conflicts with A"},
			{Op: "create", Key: "fact:alias-source", Type: "fact", Record: blackboardv2.FactRecord{Category: "duplicate", Summary: "Same semantic fact", Confidence: "tentative", ScopeStatus: "unknown"}},
			{Op: "create", Key: "fact:alias-canonical", Type: "fact", Record: blackboardv2.FactRecord{Category: "duplicate", Summary: "same semantic fact", Confidence: "tentative", ScopeStatus: "unknown"}},
		},
	})
	if err != nil {
		t.Fatalf("seed relationship cycle and redirect records: %v", err)
	}
	_, err = fixture.service.Apply(ctx, fixture.projectID, blackboardv2.ChangeBatch{
		Schema: "semantic-change-batch/v2", IdempotencyKey: "reject-fact-derived-cycle",
		Changes: []blackboardv2.Change{{Op: "relate", From: "fact:lineage-c", Relation: "derived_from", To: "fact:lineage-a"}},
	})
	if !isSemanticCode(err, "semantic_validation") {
		t.Fatalf("Fact derived_from cycle error = %#v", err)
	}
	if _, err := fixture.service.Apply(ctx, fixture.projectID, blackboardv2.ChangeBatch{
		Schema: "semantic-change-batch/v2", IdempotencyKey: "merge-before-alias-self-link",
		Changes: []blackboardv2.Change{{Op: "merge", Source: "fact:alias-source", SourceVersion: 1, Canonical: "fact:alias-canonical", CanonicalVersion: 1}},
	}); err != nil {
		t.Fatalf("merge alias source: %v", err)
	}
	for name, change := range map[string]blackboardv2.Change{
		"relate":    {Op: "relate", From: "fact:alias-source", Relation: "supports", To: "fact:alias-canonical", Reason: "Aliases must not self-link"},
		"supersede": {Op: "supersede", Replacement: "fact:alias-source", ReplacementVersion: 1, Replaced: "fact:alias-canonical", ReplacedVersion: 1},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := fixture.service.Apply(ctx, fixture.projectID, blackboardv2.ChangeBatch{
				Schema: "semantic-change-batch/v2", IdempotencyKey: "reject-alias-self-" + name, Changes: []blackboardv2.Change{change},
			})
			if !isSemanticCode(err, "semantic_validation") {
				t.Fatalf("alias-equivalent %s self-link error = %#v", name, err)
			}
		})
	}
}

func TestRelationshipReasonBoundary(t *testing.T) {
	fixture := newEvidenceV2Fixture(t, "Relationship reason boundary")
	ctx := context.Background()
	_, err := fixture.service.Apply(ctx, fixture.projectID, blackboardv2.ChangeBatch{
		Schema: "semantic-change-batch/v2", IdempotencyKey: "seed-reason-boundary",
		Changes: []blackboardv2.Change{
			{Op: "create", Key: "fact:reason-source", Type: "fact", Record: blackboardv2.FactRecord{Category: "reason", Summary: "Reason source", Confidence: "tentative", ScopeStatus: "unknown"}},
			{Op: "create", Key: "fact:reason-target", Type: "fact", Record: blackboardv2.FactRecord{Category: "reason", Summary: "Reason target", Confidence: "tentative", ScopeStatus: "unknown"}},
		},
	})
	if err != nil {
		t.Fatalf("seed reason boundary: %v", err)
	}
	if _, err := fixture.service.Apply(ctx, fixture.projectID, blackboardv2.ChangeBatch{
		Schema: "semantic-change-batch/v2", IdempotencyKey: "accept-512-byte-reason",
		Changes: []blackboardv2.Change{{Op: "relate", From: "fact:reason-source", Relation: "contradicts", To: "fact:reason-target", Reason: strings.Repeat("r", 512)}},
	}); err != nil {
		t.Fatalf("accept 512-byte reason: %v", err)
	}
	_, err = fixture.service.Apply(ctx, fixture.projectID, blackboardv2.ChangeBatch{
		Schema: "semantic-change-batch/v2", IdempotencyKey: "reject-513-byte-reason",
		Changes: []blackboardv2.Change{{Op: "relate", From: "fact:reason-target", Relation: "contradicts", To: "fact:reason-source", Reason: strings.Repeat("r", 513)}},
	})
	if !isSemanticCode(err, "semantic_validation") {
		t.Fatalf("513-byte reason error = %#v", err)
	}
}

func TestRelationshipReasonsRejectNormalizedRelationTokenAndAcceptInformation(t *testing.T) {
	fixture := newEvidenceV2Fixture(t, "Relationship reason semantics")
	ctx := context.Background()
	_, err := fixture.service.Apply(ctx, fixture.projectID, blackboardv2.ChangeBatch{
		Schema: "semantic-change-batch/v2", IdempotencyKey: "seed-redundant-reasons",
		Changes: []blackboardv2.Change{
			{Op: "create", Key: "fact:reason-a", Type: "fact", Record: blackboardv2.FactRecord{Category: "reason", Summary: "Reason A", Confidence: "tentative", ScopeStatus: "unknown"}},
			{Op: "create", Key: "fact:reason-b", Type: "fact", Record: blackboardv2.FactRecord{Category: "reason", Summary: "Reason B", Confidence: "tentative", ScopeStatus: "unknown"}},
			{Op: "create", Key: "objective:reason-a", Type: "objective", Record: blackboardv2.ObjectiveRecord{Status: "open", Objective: "Reason objective A"}},
			{Op: "create", Key: "objective:reason-b", Type: "objective", Record: blackboardv2.ObjectiveRecord{Status: "open", Objective: "Reason objective B"}},
		},
	})
	if err != nil {
		t.Fatalf("seed redundant reason records: %v", err)
	}
	for index, testCase := range []struct {
		from, relation, to, reason string
	}{
		{"fact:reason-a", "supports", "fact:reason-b", "  SuPpOrTs  "},
		{"fact:reason-a", "contradicts", "fact:reason-b", "CONTRADICTS"},
		{"objective:reason-a", "depends_on", "objective:reason-b", "  Depends   On "},
	} {
		_, err := fixture.service.Apply(ctx, fixture.projectID, blackboardv2.ChangeBatch{
			Schema: "semantic-change-batch/v2", IdempotencyKey: fmt.Sprintf("reject-redundant-reason-%d", index),
			Changes: []blackboardv2.Change{{Op: "relate", From: testCase.from, Relation: testCase.relation, To: testCase.to, Reason: testCase.reason}},
		})
		var semanticErr *blackboardv2.Error
		if !errors.As(err, &semanticErr) || semanticErr.Code != "semantic_validation" || semanticErr.Path != "changes[0].reason" || semanticErr.Details["relation"] != testCase.relation {
			t.Fatalf("redundant %s reason error = %#v", testCase.relation, err)
		}
	}
	created, err := fixture.service.Apply(ctx, fixture.projectID, blackboardv2.ChangeBatch{
		Schema: "semantic-change-batch/v2", IdempotencyKey: "accept-informative-support-reason",
		Changes: []blackboardv2.Change{{Op: "relate", From: "fact:reason-a", Relation: "supports", To: "fact:reason-b", Reason: "Independent reproduction matched the response"}},
	})
	if err != nil || len(created.Relations) != 1 || created.Relations[0][3] != 1 {
		t.Fatalf("informative reason create = %#v, %v", created.Relations, err)
	}
	_, err = fixture.service.Apply(ctx, fixture.projectID, blackboardv2.ChangeBatch{
		Schema: "semantic-change-batch/v2", IdempotencyKey: "reject-redundant-support-update",
		Changes: []blackboardv2.Change{{Op: "relate", From: "fact:reason-a", Relation: "supports", To: "fact:reason-b", Version: 1, Reason: " SUPPORTS "}},
	})
	var semanticErr *blackboardv2.Error
	if !errors.As(err, &semanticErr) || semanticErr.Code != "semantic_validation" || semanticErr.Path != "changes[0].reason" {
		t.Fatalf("redundant reason update error = %#v", err)
	}
	detail, err := fixture.service.ReadCurrent(ctx, fixture.projectID, "fact:reason-a")
	if err != nil || len(detail.Relationships) != 1 || detail.Relationships[0][3] != "Independent reproduction matched the response" {
		t.Fatalf("redundant update changed current reason: %#v, %v", detail.Relationships, err)
	}
}

func relationshipTuplePresent(haystack []blackboardv2.RelationshipTuple, needle blackboardv2.RelationshipTuple) bool {
	for _, relation := range haystack {
		if reflect.DeepEqual(relation, needle) {
			return true
		}
	}
	return false
}
