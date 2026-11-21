package blackboardv2_test

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"pentest/internal/blackboardv2"
	"pentest/internal/project"
	"pentest/internal/store"
)

func TestSemanticHealthIsDeterministicActionableAndNeverBlocksLaunch(t *testing.T) {
	fixture := newHealthFixture(t, project.KindPentest)
	ctx := context.Background()

	empty, err := fixture.service.ProjectSemanticHealth(ctx, fixture.projectID)
	if err != nil {
		t.Fatalf("empty health: %v", err)
	}
	assertHealthyEmpty(t, empty)
	assertNoAuditNoise(t, empty)
	assertContractJSON(t, mustHarness(t), "semanticHealth", empty)

	// Operator can open a stranded Attempt without tests; Continuation owns the
	// active Attempt used for Evidence retention.
	if _, err := fixture.service.Apply(ctx, fixture.projectID, blackboardv2.ChangeBatch{
		Schema: "semantic-change-batch/v2", IdempotencyKey: "seed-stranded-work",
		Changes: []blackboardv2.Change{
			{Op: "create", Key: "objective:stranded", Type: "objective", Record: blackboardv2.ObjectiveRecord{Status: "open", Objective: "Map the abandoned dependency surface"}},
			{Op: "create", Key: "attempt:orphan", Type: "attempt", Record: blackboardv2.AttemptRecord{Status: "open", Summary: "Exploring without a tested target"}},
			{Op: "create", Key: "objective:active", Type: "objective", Record: blackboardv2.ObjectiveRecord{Status: "open", Objective: "Keep one healthy open path"}},
		},
	}); err != nil {
		t.Fatalf("seed stranded work: %v", err)
	}
	if _, err := fixture.service.ApplyForContinuation(ctx, fixture.projectID, fixture.continuationID, blackboardv2.ChangeBatch{
		Schema: "semantic-change-batch/v2", IdempotencyKey: "seed-active-owned-attempt",
		Changes: []blackboardv2.Change{
			{Op: "create", Key: "attempt:active", Type: "attempt", Record: blackboardv2.AttemptRecord{Status: "open", Summary: "Testing the healthy open path"}},
			{Op: "relate", From: "attempt:active", Relation: "tests", To: "objective:active"},
		},
	}); err != nil {
		t.Fatalf("seed owned attempt: %v", err)
	}

	if err := os.WriteFile(filepath.Join(fixture.workdir, "proof.txt"), []byte("proof\n"), 0o600); err != nil {
		t.Fatalf("write evidence source: %v", err)
	}
	if _, err := fixture.service.ApplyForContinuation(ctx, fixture.projectID, fixture.continuationID, blackboardv2.ChangeBatch{
		Schema: "semantic-change-batch/v2", IdempotencyKey: "seed-missing-evidence-context",
		Changes: []blackboardv2.Change{
			{Op: "create", Key: "entity:target", Type: "entity", Record: blackboardv2.EntityRecord{Status: "active", Kind: "endpoint", Name: "Target", ScopeStatus: "in_scope"}},
			{Op: "create", Key: "finding:confirmed", Type: "finding", Record: blackboardv2.FindingRecord{
				Status: "unconfirmed", Title: "Confirmed finding needing evidence", Target: "https://example.test",
				Description: "Finding description", Proof: "Proof body", Impact: "Impact body", Recommendation: "Fix it",
				CVSSVersion: "4.0", CVSSVector: "CVSS:4.0/AV:N/AC:L/AT:N/PR:N/UI:N/VC:H/VI:H/VA:N/SC:N/SI:N/SA:N",
			}},
			{Op: "create", Key: "fact:support", Type: "fact", Record: blackboardv2.FactRecord{Category: "auth", Summary: "Support fact", Confidence: "tentative", ScopeStatus: "in_scope"}},
			{Op: "relate", From: "finding:confirmed", Relation: "about", To: "entity:target"},
			{Op: "relate", From: "attempt:active", Relation: "produced", To: "fact:support"},
			{Op: "relate", From: "fact:support", Relation: "supports", To: "finding:confirmed", Reason: "Support before confirmation"},
		},
	}); err != nil {
		t.Fatalf("seed finding context: %v", err)
	}
	if _, err := fixture.service.RetainEvidenceForContinuation(ctx, fixture.projectID, fixture.continuationID, blackboardv2.RetainEvidenceRequest{
		IdempotencyKey: "retain-then-missing", Key: "evidence:missing", Attempt: "attempt:active",
		SourcePath: "proof.txt", ArtifactType: "text", Summary: "Proof that will go missing",
		Links: []blackboardv2.EvidenceLink{{"evidences", "finding:confirmed"}},
	}); err != nil {
		t.Fatalf("retain evidence: %v", err)
	}
	if _, err := fixture.service.ApplyForContinuation(ctx, fixture.projectID, fixture.continuationID, blackboardv2.ChangeBatch{
		Schema: "semantic-change-batch/v2", IdempotencyKey: "mark-evidence-missing",
		Changes: []blackboardv2.Change{{Op: "transition", Key: "evidence:missing", Version: 1, Status: "missing", Summary: "Payload is no longer available"}},
	}); err != nil {
		t.Fatalf("mark evidence missing: %v", err)
	}
	// Confirm finding with a separate available evidence path so confirmation is legal,
	// while the missing evidence still evidences the confirmed finding.
	if err := os.WriteFile(filepath.Join(fixture.workdir, "still-good.txt"), []byte("good\n"), 0o600); err != nil {
		t.Fatalf("write second evidence: %v", err)
	}
	if _, err := fixture.service.RetainEvidenceForContinuation(ctx, fixture.projectID, fixture.continuationID, blackboardv2.RetainEvidenceRequest{
		IdempotencyKey: "retain-available", Key: "evidence:available", Attempt: "attempt:active",
		SourcePath: "still-good.txt", ArtifactType: "text", Summary: "Still available proof",
		Links: []blackboardv2.EvidenceLink{{"evidences", "finding:confirmed"}},
	}); err != nil {
		t.Fatalf("retain available evidence: %v", err)
	}
	if _, err := fixture.service.ApplyForContinuation(ctx, fixture.projectID, fixture.continuationID, blackboardv2.ChangeBatch{
		Schema: "semantic-change-batch/v2", IdempotencyKey: "confirm-finding",
		Changes: []blackboardv2.Change{{Op: "transition", Key: "finding:confirmed", Version: 1, Status: "confirmed"}},
	}); err != nil {
		t.Fatalf("confirm finding: %v", err)
	}

	if _, err := fixture.service.Apply(ctx, fixture.projectID, blackboardv2.ChangeBatch{
		Schema: "semantic-change-batch/v2", IdempotencyKey: "seed-relation-anomalies",
		Changes: []blackboardv2.Change{
			{Op: "create", Key: "objective:satisfied-open", Type: "objective", Record: blackboardv2.ObjectiveRecord{Status: "open", Objective: "Still open despite satisfaction"}},
			{Op: "create", Key: "fact:a", Type: "fact", Record: blackboardv2.FactRecord{Category: "auth", Summary: "Fact A", Confidence: "confirmed", ScopeStatus: "in_scope"}},
			{Op: "create", Key: "fact:b", Type: "fact", Record: blackboardv2.FactRecord{Category: "auth", Summary: "Fact B", Confidence: "confirmed", ScopeStatus: "in_scope"}},
			{Op: "relate", From: "fact:a", Relation: "satisfies", To: "objective:satisfied-open"},
			{Op: "relate", From: "fact:a", Relation: "contradicts", To: "fact:b", Reason: "Confirmed conclusions disagree"},
		},
	}); err != nil {
		// Confirmed facts may require support basis; fall back to tentative + contradicts if needed.
		if _, retryErr := fixture.service.Apply(ctx, fixture.projectID, blackboardv2.ChangeBatch{
			Schema: "semantic-change-batch/v2", IdempotencyKey: "seed-relation-anomalies-retry",
			Changes: []blackboardv2.Change{
				{Op: "create", Key: "objective:satisfied-open", Type: "objective", Record: blackboardv2.ObjectiveRecord{Status: "open", Objective: "Still open despite satisfaction"}},
				{Op: "create", Key: "fact:a", Type: "fact", Record: blackboardv2.FactRecord{Category: "auth", Summary: "Fact A", Confidence: "tentative", ScopeStatus: "in_scope"}},
				{Op: "create", Key: "fact:b", Type: "fact", Record: blackboardv2.FactRecord{Category: "auth", Summary: "Fact B", Confidence: "tentative", ScopeStatus: "in_scope"}},
				{Op: "relate", From: "fact:a", Relation: "satisfies", To: "objective:satisfied-open"},
				{Op: "relate", From: "fact:a", Relation: "contradicts", To: "fact:b", Reason: "Conclusions disagree"},
			},
		}); retryErr != nil {
			t.Fatalf("seed relation anomalies: first=%v retry=%v", err, retryErr)
		}
	}

	// Redirect integrity: merge creates a redirect; health must not invent audit issues for a valid redirect.
	if _, err := fixture.service.Apply(ctx, fixture.projectID, blackboardv2.ChangeBatch{
		Schema: "semantic-change-batch/v2", IdempotencyKey: "seed-merge-redirect",
		Changes: []blackboardv2.Change{
			{Op: "create", Key: "entity:source", Type: "entity", Record: blackboardv2.EntityRecord{Status: "active", Kind: "host", Name: "Original admin host", Locator: "ADMIN.EXAMPLE.", ScopeStatus: "in_scope"}},
			{Op: "create", Key: "entity:canonical", Type: "entity", Record: blackboardv2.EntityRecord{Status: "active", Kind: "host", Name: "Admin host", Locator: "admin.example", ScopeStatus: "in_scope"}},
		},
	}); err != nil {
		t.Fatalf("seed merge pair: %v", err)
	}
	if _, err := fixture.service.Apply(ctx, fixture.projectID, blackboardv2.ChangeBatch{
		Schema: "semantic-change-batch/v2", IdempotencyKey: "apply-merge-redirect",
		Changes: []blackboardv2.Change{{
			Op: "merge", Source: "entity:source", SourceVersion: 1,
			Canonical: "entity:canonical", CanonicalVersion: 1,
			CanonicalRecord: blackboardv2.EntityPatch{Name: strPtr("Canonical admin host")},
		}},
	}); err != nil {
		t.Fatalf("merge redirect: %v", err)
	}

	first, err := fixture.service.ProjectSemanticHealth(ctx, fixture.projectID)
	if err != nil {
		t.Fatalf("project semantic health: %v", err)
	}
	if first.Schema != "blackboard-health/v2" {
		t.Fatalf("schema = %q", first.Schema)
	}
	if !first.Attention.Complete || !first.Attention.Launchable {
		t.Fatalf("health made Snapshot unlaunchable: %#v", first.Attention)
	}
	if first.Attention.State == "" || first.Attention.Bytes <= 0 || first.Attention.EstimatedTokens <= 0 {
		t.Fatalf("attention measurement incomplete: %#v", first.Attention)
	}
	assertNoAuditNoise(t, first)
	assertHasAnomalyCodes(t, first, []string{
		"stranded_objective",
		"stranded_attempt",
		"missing_evidence",
		"objective_satisfied_but_open",
		"unresolved_contradiction",
	})
	assertNoAnomalyCode(t, first, "redirect_integrity")
	missing := anomalyByCode(t, first, "missing_evidence")
	if missing.Severity != "critical" || missing.SubjectKey != "evidence:missing" {
		t.Fatalf("missing evidence anomaly = %#v", missing)
	}
	if !containsKey(missing.RelatedKeys, "finding:confirmed") {
		t.Fatalf("missing evidence related keys = %#v", missing.RelatedKeys)
	}
	if first.Status != "critical" && first.Status != "warning" {
		if missing.Severity == "critical" && first.Status != "critical" {
			t.Fatalf("status = %q with critical anomaly", first.Status)
		}
	}
	if first.Status != "critical" {
		t.Fatalf("status = %q, want critical from missing Evidence on confirmed Finding", first.Status)
	}

	firstBytes := mustHealthJSON(t, first)
	if err := fixture.db.Close(); err != nil {
		t.Fatalf("close store: %v", err)
	}
	reopened, err := store.Open(fixture.dbPath)
	if err != nil {
		t.Fatalf("reopen store: %v", err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	reopenedService := blackboardv2.NewServiceWithEvidence(reopened, blackboardv2.EvidenceConfig{
		RuntimeRoot: fixture.runtimeRoot, ArtifactRoot: fixture.runtimeRoot,
	})
	second, err := reopenedService.ProjectSemanticHealth(ctx, fixture.projectID)
	if err != nil {
		t.Fatalf("health after reopen: %v", err)
	}
	secondBytes := mustHealthJSON(t, second)
	if !bytes.Equal(firstBytes, secondBytes) {
		t.Fatalf("health drifted after reopen\nfirst=%s\nsecond=%s", firstBytes, secondBytes)
	}
	third, err := reopenedService.ProjectSemanticHealth(ctx, fixture.projectID)
	if err != nil {
		t.Fatalf("third health: %v", err)
	}
	if !bytes.Equal(secondBytes, mustHealthJSON(t, third)) {
		t.Fatal("health is not deterministic across repeated reads")
	}
	// Health never mutates semantic revision.
	if second.Revision != first.Revision {
		t.Fatalf("revision changed across pure health reads: %d vs %d", first.Revision, second.Revision)
	}
	snapshot, err := reopenedService.ProjectRuntimeSnapshot(ctx, fixture.projectID)
	if err != nil {
		t.Fatalf("snapshot after health: %v", err)
	}
	if snapshot.Snapshot.Revision != first.Revision {
		t.Fatalf("health mutated semantic revision: health=%d snapshot=%d", first.Revision, snapshot.Snapshot.Revision)
	}
}

func TestSemanticHealthAttentionThresholdsOfferConsolidationWithoutBlocking(t *testing.T) {
	for _, test := range []struct {
		name      string
		factCount int
		wantState blackboardv2.AttentionBudgetState
		offered   bool
		required  bool
		status    blackboardv2.HealthStatus
		code      string
	}{
		{name: "within target", factCount: 1, wantState: blackboardv2.AttentionWithinTarget, offered: false, required: false, status: "healthy", code: ""},
		{name: "above target", factCount: 60, wantState: blackboardv2.AttentionAboveTarget, offered: false, required: false, status: "attention", code: "attention_above_target"},
		{name: "warning", factCount: 120, wantState: blackboardv2.AttentionWarning, offered: true, required: false, status: "warning", code: "attention_warning"},
		{name: "required", factCount: 230, wantState: blackboardv2.AttentionRequired, offered: true, required: true, status: "critical", code: "attention_required"},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newHealthFixture(t, project.KindPentest)
			changes := make([]blackboardv2.Change, 0, test.factCount)
			for index := 0; index < test.factCount; index++ {
				prefix := fmt.Sprintf("Fact %03d ", index)
				changes = append(changes, blackboardv2.Change{
					Op: "create", Key: fmt.Sprintf("fact:budget:%03d", index), Type: "fact",
					Record: blackboardv2.FactRecord{
						Category: "budget", Summary: prefix + strings.Repeat("x", 1024-len(prefix)),
						Confidence: "tentative", ScopeStatus: "unknown",
					},
				})
			}
			if _, err := fixture.service.Apply(context.Background(), fixture.projectID, blackboardv2.ChangeBatch{
				Schema: "semantic-change-batch/v2", IdempotencyKey: "seed-attention", Changes: changes,
			}); err != nil {
				t.Fatalf("seed facts: %v", err)
			}
			health, err := fixture.service.ProjectSemanticHealth(context.Background(), fixture.projectID)
			if err != nil {
				t.Fatalf("health: %v", err)
			}
			if health.Attention.State != test.wantState {
				t.Fatalf("attention state = %s, want %s (tokens=%d bytes=%d)", health.Attention.State, test.wantState, health.Attention.EstimatedTokens, health.Attention.Bytes)
			}
			if health.Attention.ConsolidationOffered != test.offered || health.Attention.ConsolidationRequired != test.required {
				t.Fatalf("consolidation flags = offered=%t required=%t, want %t/%t", health.Attention.ConsolidationOffered, health.Attention.ConsolidationRequired, test.offered, test.required)
			}
			if !health.Attention.Complete || !health.Attention.Launchable {
				t.Fatalf("attention blocked launch: %#v", health.Attention)
			}
			if health.Status != test.status {
				t.Fatalf("status = %s, want %s", health.Status, test.status)
			}
			if test.code == "" {
				assertNoAnomalyCode(t, health, "attention_above_target")
				assertNoAnomalyCode(t, health, "attention_warning")
				assertNoAnomalyCode(t, health, "attention_required")
				if len(health.Proposals) != 0 {
					t.Fatalf("unexpected proposals: %#v", health.Proposals)
				}
			} else {
				anomaly := anomalyByCode(t, health, test.code)
				if !strings.Contains(strings.ToLower(anomaly.Message), "working graph") && test.offered {
					t.Fatalf("consolidation offer message missing Working Graph guidance: %#v", anomaly)
				}
				if len(health.Proposals) != 0 {
					t.Fatalf("retired Reason Task proposals remain: %#v", health.Proposals)
				}
			}
			projection, err := fixture.service.ProjectRuntimeSnapshot(context.Background(), fixture.projectID)
			if err != nil {
				t.Fatalf("projection: %v", err)
			}
			if health.Attention.Bytes != projection.ByteCount || health.Attention.EstimatedTokens != projection.EstimatedTokens || health.Attention.State != projection.AttentionState {
				t.Fatalf("health attention diverged from Snapshot measurement: health=%#v projection bytes=%d tokens=%d state=%s", health.Attention, projection.ByteCount, projection.EstimatedTokens, projection.AttentionState)
			}
			if len(projection.Snapshot.Knowledge.Facts) != test.factCount {
				t.Fatalf("health path filtered Snapshot facts: got %d want %d", len(projection.Snapshot.Knowledge.Facts), test.factCount)
			}
		})
	}
}

func TestSemanticHealthReportsConsolidationWithoutSchedulingWork(t *testing.T) {
	fixture := newHealthFixture(t, project.KindPentest)
	changes := make([]blackboardv2.Change, 0, 120)
	for index := 0; index < 120; index++ {
		prefix := fmt.Sprintf("Fact %03d ", index)
		changes = append(changes, blackboardv2.Change{
			Op: "create", Key: fmt.Sprintf("fact:proposal:%03d", index), Type: "fact",
			Record: blackboardv2.FactRecord{
				Category: "budget", Summary: prefix + strings.Repeat("x", 1024-len(prefix)),
				Confidence: "tentative", ScopeStatus: "unknown",
			},
		})
	}
	if _, err := fixture.service.Apply(context.Background(), fixture.projectID, blackboardv2.ChangeBatch{
		Schema: "semantic-change-batch/v2", IdempotencyKey: "seed-proposal-attention", Changes: changes,
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	health, err := fixture.service.ProjectSemanticHealth(context.Background(), fixture.projectID)
	if err != nil {
		t.Fatalf("health: %v", err)
	}
	if !health.Attention.ConsolidationOffered || health.Attention.ConsolidationRequired {
		t.Fatalf("expected offered-not-required: %#v", health.Attention)
	}
	if len(health.Proposals) != 0 {
		t.Fatalf("retired Reason Task proposals remain: %#v", health.Proposals)
	}
	assertContractJSON(t, mustHarness(t), "semanticHealth", health)

	emptyFixture := newHealthFixture(t, project.KindPentest)
	empty, err := emptyFixture.service.ProjectSemanticHealth(context.Background(), emptyFixture.projectID)
	if err != nil {
		t.Fatalf("empty health: %v", err)
	}
	if len(empty.Proposals) != 0 {
		t.Fatalf("empty proposals = %#v", empty.Proposals)
	}
}
