package blackboardv2_test

import (
	"context"
	"errors"
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

func relationshipTuplePresent(haystack []blackboardv2.RelationshipTuple, needle blackboardv2.RelationshipTuple) bool {
	for _, relation := range haystack {
		if reflect.DeepEqual(relation, needle) {
			return true
		}
	}
	return false
}
