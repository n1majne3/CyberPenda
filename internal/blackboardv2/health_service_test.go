package blackboardv2_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"pentest/internal/blackboardv2"
	"pentest/internal/project"
	"pentest/internal/store"
	"pentest/internal/task"
)

func TestSemanticHealthIsDeterministicActionableAndNeverBlocksLaunch(t *testing.T) {
	fixture := newHealthFixture(t, project.KindPentest)
	ctx := context.Background()

	// Empty Project: healthy, complete, launchable, no audit noise.
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

	// Missing Evidence plus confirmed Finding that relies on it.
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

	// Invalid relationship semantics: open objective already satisfied; contradiction between confirmed conclusions.
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
	// Valid redirects are not anomalies.
	assertNoAnomalyCode(t, first, "redirect_integrity")
	// Missing Evidence that evidences a confirmed Finding is critical.
	missing := anomalyByCode(t, first, "missing_evidence")
	if missing.Severity != "critical" || missing.SubjectKey != "evidence:missing" {
		t.Fatalf("missing evidence anomaly = %#v", missing)
	}
	if !containsKey(missing.RelatedKeys, "finding:confirmed") {
		t.Fatalf("missing evidence related keys = %#v", missing.RelatedKeys)
	}
	if first.Status != "critical" && first.Status != "warning" {
		// critical missing evidence should drive overall status to critical
		if missing.Severity == "critical" && first.Status != "critical" {
			t.Fatalf("status = %q with critical anomaly", first.Status)
		}
	}
	if first.Status != "critical" {
		t.Fatalf("status = %q, want critical from missing Evidence on confirmed Finding", first.Status)
	}

	// Deterministic after reopen.
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
	// Repeated reads are byte-identical.
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
				if !strings.Contains(strings.ToLower(anomaly.Message), "reason task") && test.offered {
					t.Fatalf("consolidation offer message missing Reason Task guidance: %#v", anomaly)
				}
				if test.offered {
					if len(health.Proposals) != 1 {
						t.Fatalf("proposals = %#v", health.Proposals)
					}
					proposal := health.Proposals[0]
					if proposal.Code != "consolidation_reason_task" || proposal.Action != "start_reason_task" || !proposal.ApprovalRequired {
						t.Fatalf("proposal = %#v", proposal)
					}
					if proposal.Required != test.required {
						t.Fatalf("proposal.required = %t, want %t", proposal.Required, test.required)
					}
				}
			}
			// Exact Snapshot measurement parity.
			projection, err := fixture.service.ProjectRuntimeSnapshot(context.Background(), fixture.projectID)
			if err != nil {
				t.Fatalf("projection: %v", err)
			}
			if health.Attention.Bytes != projection.ByteCount || health.Attention.EstimatedTokens != projection.EstimatedTokens || health.Attention.State != projection.AttentionState {
				t.Fatalf("health attention diverged from Snapshot measurement: health=%#v projection bytes=%d tokens=%d state=%s", health.Attention, projection.ByteCount, projection.EstimatedTokens, projection.AttentionState)
			}
			// All records remain present (no truncation/filtering).
			if len(projection.Snapshot.Knowledge.Facts) != test.factCount {
				t.Fatalf("health path filtered Snapshot facts: got %d want %d", len(projection.Snapshot.Knowledge.Facts), test.factCount)
			}
		})
	}
}

func TestSemanticHealthIsProjectIsolatedAndDetectsRedirectIntegrity(t *testing.T) {
	root := t.TempDir()
	dbPath := filepath.Join(root, "health-isolation.db")
	db, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	projects := project.NewService(db)
	alpha, err := projects.Create("Alpha", "", project.Scope{}, project.Defaults{})
	if err != nil {
		t.Fatalf("create alpha: %v", err)
	}
	beta, err := projects.Create("Beta", "", project.Scope{}, project.Defaults{})
	if err != nil {
		t.Fatalf("create beta: %v", err)
	}
	service := blackboardv2.NewService(db)
	ctx := context.Background()

	if _, err := service.Apply(ctx, alpha.ID, blackboardv2.ChangeBatch{
		Schema: "semantic-change-batch/v2", IdempotencyKey: "alpha-stranded",
		Changes: []blackboardv2.Change{
			{Op: "create", Key: "objective:alpha-only", Type: "objective", Record: blackboardv2.ObjectiveRecord{Status: "open", Objective: "Alpha stranded objective"}},
		},
	}); err != nil {
		t.Fatalf("seed alpha: %v", err)
	}
	if _, err := service.Apply(ctx, beta.ID, blackboardv2.ChangeBatch{
		Schema: "semantic-change-batch/v2", IdempotencyKey: "beta-healthy",
		Changes: []blackboardv2.Change{
			{Op: "create", Key: "entity:beta-only", Type: "entity", Record: blackboardv2.EntityRecord{Status: "active", Kind: "host", Name: "Beta host", ScopeStatus: "in_scope"}},
		},
	}); err != nil {
		t.Fatalf("seed beta: %v", err)
	}

	alphaHealth, err := service.ProjectSemanticHealth(ctx, alpha.ID)
	if err != nil {
		t.Fatalf("alpha health: %v", err)
	}
	betaHealth, err := service.ProjectSemanticHealth(ctx, beta.ID)
	if err != nil {
		t.Fatalf("beta health: %v", err)
	}
	alphaJSON := mustHealthJSON(t, alphaHealth)
	betaJSON := mustHealthJSON(t, betaHealth)
	if bytes.Contains(alphaJSON, []byte("entity:beta-only")) || bytes.Contains(alphaJSON, []byte(beta.ID)) {
		t.Fatalf("alpha health leaked beta state: %s", alphaJSON)
	}
	if bytes.Contains(betaJSON, []byte("objective:alpha-only")) || bytes.Contains(betaJSON, []byte(alpha.ID)) {
		t.Fatalf("beta health leaked alpha state: %s", betaJSON)
	}
	assertHasAnomalyCodes(t, alphaHealth, []string{"stranded_objective"})
	assertNoAnomalyCode(t, betaHealth, "stranded_objective")
	if betaHealth.Status != "healthy" {
		t.Fatalf("beta status = %s, want healthy", betaHealth.Status)
	}

	// Broken redirect integrity within one Project (storage corruption that is still project-local).
	if _, err := db.Exec(`INSERT INTO blackboard_v2_key_redirects(project_id,source_key,canonical_key,created_at) VALUES(?,?,?,?)`,
		alpha.ID, "entity:broken-source", "entity:missing-canonical", "2026-07-18T00:00:00Z"); err != nil {
		t.Fatalf("insert broken redirect: %v", err)
	}
	broken, err := service.ProjectSemanticHealth(ctx, alpha.ID)
	if err != nil {
		t.Fatalf("broken redirect health: %v", err)
	}
	redirect := anomalyByCode(t, broken, "redirect_integrity")
	if redirect.Severity != "critical" || redirect.SubjectKey != "entity:broken-source" {
		t.Fatalf("redirect integrity anomaly = %#v", redirect)
	}
	// Beta remains unaffected.
	betaAfter, err := service.ProjectSemanticHealth(ctx, beta.ID)
	if err != nil {
		t.Fatalf("beta after broken alpha redirect: %v", err)
	}
	assertNoAnomalyCode(t, betaAfter, "redirect_integrity")
	if !bytes.Equal(betaJSON, mustHealthJSON(t, betaAfter)) {
		t.Fatalf("beta health changed when alpha redirect integrity failed")
	}
}

func TestSemanticHealthDetectsDanglingRelationships(t *testing.T) {
	fixture := newHealthFixture(t, project.KindPentest)
	ctx := context.Background()
	if _, err := fixture.service.Apply(ctx, fixture.projectID, blackboardv2.ChangeBatch{
		Schema: "semantic-change-batch/v2", IdempotencyKey: "seed-relation-pair",
		Changes: []blackboardv2.Change{
			{Op: "create", Key: "entity:a", Type: "entity", Record: blackboardv2.EntityRecord{Status: "active", Kind: "host", Name: "A", ScopeStatus: "in_scope"}},
			{Op: "create", Key: "entity:b", Type: "entity", Record: blackboardv2.EntityRecord{Status: "active", Kind: "host", Name: "B", ScopeStatus: "in_scope"}},
			{Op: "relate", From: "entity:a", Relation: "part_of", To: "entity:b"},
		},
	}); err != nil {
		t.Fatalf("seed entities: %v", err)
	}
	// Force a dangling relationship by deleting one endpoint without retiring the edge.
	if _, err := fixture.db.Exec(`DELETE FROM blackboard_v2_records WHERE project_id=? AND key=?`, fixture.projectID, "entity:b"); err != nil {
		t.Fatalf("delete endpoint: %v", err)
	}
	health, err := fixture.service.ProjectSemanticHealth(ctx, fixture.projectID)
	if err != nil {
		t.Fatalf("health: %v", err)
	}
	anomaly := anomalyByCode(t, health, "dangling_relationship")
	if anomaly.Severity != "critical" {
		t.Fatalf("dangling severity = %s", anomaly.Severity)
	}
	// Prefer the surviving endpoint as subject so the operator UI can navigate.
	if anomaly.SubjectKey != "entity:a" {
		t.Fatalf("dangling subject = %q, want surviving entity:a: %#v", anomaly.SubjectKey, anomaly)
	}
	if !containsKey(anomaly.RelatedKeys, "entity:b") {
		t.Fatalf("dangling related keys missing deleted endpoint: %#v", anomaly)
	}
	// Canonical Runtime Snapshot is unavailable; do not claim exact complete measurement.
	if health.Attention.Complete {
		t.Fatalf("attention claimed complete despite corrupted relationships: %#v", health.Attention)
	}
	if !health.Attention.Launchable {
		t.Fatalf("health blocked launch under corruption: %#v", health.Attention)
	}
	// Diagnostic attention still measures all persisted record/relationship material.
	if health.Attention.Bytes <= 0 || health.Attention.EstimatedTokens <= 0 {
		t.Fatalf("diagnostic attention measurement empty: %#v", health.Attention)
	}
}

func TestSemanticHealthToleratesInvalidGrammarAndCyclesWithout422(t *testing.T) {
	for _, testCase := range []struct {
		name    string
		seed    []blackboardv2.Change
		persist func(t *testing.T, fixture healthFixture)
		code    string
	}{
		{
			name: "invalid endpoint grammar",
			seed: []blackboardv2.Change{
				{Op: "create", Key: "entity:invalid-source", Type: "entity", Record: blackboardv2.EntityRecord{Status: "active", Kind: "service", Name: "Invalid source", ScopeStatus: "in_scope"}},
				{Op: "create", Key: "fact:invalid-target", Type: "fact", Record: blackboardv2.FactRecord{Category: "test", Summary: "Invalid target", Confidence: "tentative", ScopeStatus: "unknown"}},
			},
			persist: func(t *testing.T, fixture healthFixture) {
				t.Helper()
				if _, err := fixture.db.Exec(`
					INSERT INTO blackboard_v2_relationships(project_id,from_key,relation,to_key,version,reason,created_at,updated_at)
					VALUES(?, 'entity:invalid-source', 'about', 'fact:invalid-target', 1, '', '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z')`, fixture.projectID); err != nil {
					t.Fatalf("inject invalid grammar edge: %v", err)
				}
			},
			code: "invalid_relationship",
		},
		{
			name: "containment cycle",
			seed: []blackboardv2.Change{
				{Op: "create", Key: "entity:cycle-a", Type: "entity", Record: blackboardv2.EntityRecord{Status: "active", Kind: "host", Name: "Cycle A", ScopeStatus: "unknown"}},
				{Op: "create", Key: "entity:cycle-b", Type: "entity", Record: blackboardv2.EntityRecord{Status: "active", Kind: "host", Name: "Cycle B", ScopeStatus: "unknown"}},
			},
			persist: func(t *testing.T, fixture healthFixture) {
				t.Helper()
				for _, pair := range [][2]string{{"entity:cycle-a", "entity:cycle-b"}, {"entity:cycle-b", "entity:cycle-a"}} {
					if _, err := fixture.db.Exec(`INSERT INTO blackboard_v2_relationships(project_id,from_key,relation,to_key,version,reason,created_at,updated_at) VALUES(?, ?, 'part_of', ?, 1, '', '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z')`, fixture.projectID, pair[0], pair[1]); err != nil {
						t.Fatalf("inject cycle edge: %v", err)
					}
				}
			},
			code: "relationship_cycle",
		},
		{
			name: "malformed oversized reason",
			seed: []blackboardv2.Change{
				{Op: "create", Key: "fact:reason-a", Type: "fact", Record: blackboardv2.FactRecord{Category: "test", Summary: "Reason A", Confidence: "tentative", ScopeStatus: "unknown"}},
				{Op: "create", Key: "fact:reason-b", Type: "fact", Record: blackboardv2.FactRecord{Category: "test", Summary: "Reason B", Confidence: "tentative", ScopeStatus: "unknown"}},
			},
			persist: func(t *testing.T, fixture healthFixture) {
				t.Helper()
				if _, err := fixture.db.Exec(`INSERT INTO blackboard_v2_relationships(project_id,from_key,relation,to_key,version,reason,created_at,updated_at) VALUES(?, 'fact:reason-a', 'contradicts', 'fact:reason-b', 1, ?, '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z')`, fixture.projectID, strings.Repeat("r", 513)); err != nil {
					t.Fatalf("inject oversized reason: %v", err)
				}
			},
			code: "invalid_relationship",
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			fixture := newHealthFixture(t, project.KindPentest)
			ctx := context.Background()
			if _, err := fixture.service.Apply(ctx, fixture.projectID, blackboardv2.ChangeBatch{
				Schema: "semantic-change-batch/v2", IdempotencyKey: "seed-" + testCase.name, Changes: testCase.seed,
			}); err != nil {
				t.Fatalf("seed: %v", err)
			}
			// Canonical Snapshot rejects the corruption — health must not.
			if _, err := fixture.service.ProjectRuntimeSnapshot(ctx, fixture.projectID); err == nil {
				// Before injecting corruption Snapshot may work; inject next.
			}
			testCase.persist(t, fixture)
			if _, err := fixture.service.ProjectRuntimeSnapshot(ctx, fixture.projectID); err == nil {
				t.Fatal("expected canonical Snapshot to reject corruption")
			}
			health, err := fixture.service.ProjectSemanticHealth(ctx, fixture.projectID)
			if err != nil {
				t.Fatalf("health must return DTO under corruption, got error: %v", err)
			}
			if health.Schema != "blackboard-health/v2" {
				t.Fatalf("schema = %q", health.Schema)
			}
			anomaly := anomalyByCode(t, health, testCase.code)
			if anomaly.Severity != "critical" {
				t.Fatalf("%s severity = %s", testCase.code, anomaly.Severity)
			}
			if health.Attention.Complete {
				t.Fatalf("attention complete=true despite unavailable canonical Snapshot: %#v", health.Attention)
			}
			if !health.Attention.Launchable || health.Attention.Bytes <= 0 {
				t.Fatalf("diagnostic attention not launchable/measured: %#v", health.Attention)
			}
			assertContractJSON(t, mustHarness(t), "semanticHealth", health)
		})
	}
}

func TestSemanticHealthDetectsEvidencePayloadIntegrityFailureWhileAvailable(t *testing.T) {
	fixture := newHealthFixture(t, project.KindPentest)
	ctx := context.Background()
	if _, err := fixture.service.ApplyForContinuation(ctx, fixture.projectID, fixture.continuationID, blackboardv2.ChangeBatch{
		Schema: "semantic-change-batch/v2", IdempotencyKey: "seed-integrity-context",
		Changes: []blackboardv2.Change{
			{Op: "create", Key: "entity:target", Type: "entity", Record: blackboardv2.EntityRecord{Status: "active", Kind: "endpoint", Name: "Target", ScopeStatus: "in_scope"}},
			{Op: "create", Key: "finding:confirmed", Type: "finding", Record: blackboardv2.FindingRecord{
				Status: "unconfirmed", Title: "Finding needing intact evidence", Target: "https://example.test",
				Description: "Finding description", Proof: "Proof body", Impact: "Impact body", Recommendation: "Fix it",
				CVSSVersion: "4.0", CVSSVector: "CVSS:4.0/AV:N/AC:L/AT:N/PR:N/UI:N/VC:H/VI:H/VA:N/SC:N/SI:N/SA:N",
			}},
			{Op: "create", Key: "fact:support", Type: "fact", Record: blackboardv2.FactRecord{Category: "auth", Summary: "Support fact", Confidence: "tentative", ScopeStatus: "in_scope"}},
			{Op: "create", Key: "attempt:active", Type: "attempt", Record: blackboardv2.AttemptRecord{Status: "open", Summary: "Active attempt"}},
			{Op: "create", Key: "objective:active", Type: "objective", Record: blackboardv2.ObjectiveRecord{Status: "open", Objective: "Active objective"}},
			{Op: "relate", From: "attempt:active", Relation: "tests", To: "objective:active"},
			{Op: "relate", From: "finding:confirmed", Relation: "about", To: "entity:target"},
			{Op: "relate", From: "attempt:active", Relation: "produced", To: "fact:support"},
			{Op: "relate", From: "fact:support", Relation: "supports", To: "finding:confirmed", Reason: "Support before confirmation"},
		},
	}); err != nil {
		t.Fatalf("seed integrity context: %v", err)
	}
	if err := os.WriteFile(filepath.Join(fixture.workdir, "integrity.txt"), []byte("trusted\n"), 0o600); err != nil {
		t.Fatalf("write source: %v", err)
	}
	if _, err := fixture.service.RetainEvidenceForContinuation(ctx, fixture.projectID, fixture.continuationID, blackboardv2.RetainEvidenceRequest{
		IdempotencyKey: "retain-integrity", Key: "evidence:integrity", Attempt: "attempt:active",
		SourcePath: "integrity.txt", ArtifactType: "text", Summary: "Integrity-checked payload",
		Links: []blackboardv2.EvidenceLink{{"evidences", "finding:confirmed"}},
	}); err != nil {
		t.Fatalf("retain evidence: %v", err)
	}
	if _, err := fixture.service.ApplyForContinuation(ctx, fixture.projectID, fixture.continuationID, blackboardv2.ChangeBatch{
		Schema: "semantic-change-batch/v2", IdempotencyKey: "confirm-finding-integrity",
		Changes: []blackboardv2.Change{{Op: "transition", Key: "finding:confirmed", Version: 1, Status: "confirmed"}},
	}); err != nil {
		t.Fatalf("confirm finding: %v", err)
	}
	// Status remains available; destroy the managed payload bytes.
	managed := retainedEvidenceFiles(t, fixture.runtimeRoot)
	if len(managed) == 0 {
		t.Fatal("expected managed Evidence payload files")
	}
	for _, path := range managed {
		if err := os.Remove(path); err != nil {
			t.Fatalf("remove managed payload %s: %v", path, err)
		}
	}
	health, err := fixture.service.ProjectSemanticHealth(ctx, fixture.projectID)
	if err != nil {
		t.Fatalf("health: %v", err)
	}
	anomaly := anomalyByCode(t, health, "evidence_integrity")
	if anomaly.Severity != "critical" {
		t.Fatalf("evidence integrity severity = %s, want critical for confirmed conclusion: %#v", anomaly.Severity, anomaly)
	}
	if anomaly.SubjectKey != "evidence:integrity" {
		t.Fatalf("subject = %q", anomaly.SubjectKey)
	}
	if !containsKey(anomaly.RelatedKeys, "finding:confirmed") {
		t.Fatalf("related keys = %#v", anomaly.RelatedKeys)
	}
	assertNoAnomalyCode(t, health, "missing_evidence")
	if health.Status != "critical" {
		t.Fatalf("status = %s", health.Status)
	}
}

func TestSemanticHealthDetectsRedirectSourceStillCurrent(t *testing.T) {
	fixture := newHealthFixture(t, project.KindPentest)
	ctx := context.Background()
	if _, err := fixture.service.Apply(ctx, fixture.projectID, blackboardv2.ChangeBatch{
		Schema: "semantic-change-batch/v2", IdempotencyKey: "seed-redirect-source-current",
		Changes: []blackboardv2.Change{
			{Op: "create", Key: "entity:still-here", Type: "entity", Record: blackboardv2.EntityRecord{Status: "active", Kind: "host", Name: "Still here", ScopeStatus: "in_scope"}},
			{Op: "create", Key: "entity:canonical-ok", Type: "entity", Record: blackboardv2.EntityRecord{Status: "active", Kind: "host", Name: "Canonical", ScopeStatus: "in_scope"}},
		},
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	// Corrupt redirect: source key still has a current record (merge always removes source).
	if _, err := fixture.db.Exec(`INSERT INTO blackboard_v2_key_redirects(project_id,source_key,canonical_key,created_at) VALUES(?,?,?,?)`,
		fixture.projectID, "entity:still-here", "entity:canonical-ok", "2026-07-18T00:00:00Z"); err != nil {
		t.Fatalf("insert corrupt redirect: %v", err)
	}
	health, err := fixture.service.ProjectSemanticHealth(ctx, fixture.projectID)
	if err != nil {
		t.Fatalf("health: %v", err)
	}
	redirect := anomalyByCode(t, health, "redirect_integrity")
	if redirect.SubjectKey != "entity:still-here" {
		t.Fatalf("redirect anomaly = %#v", redirect)
	}
	if !strings.Contains(strings.ToLower(redirect.Message), "current") {
		t.Fatalf("redirect message should mention current record: %#v", redirect)
	}
	if !containsKey(redirect.RelatedKeys, "entity:canonical-ok") {
		t.Fatalf("related keys = %#v", redirect.RelatedKeys)
	}
}

func TestSemanticHealthOffersExplicitConsolidationProposal(t *testing.T) {
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
	if len(health.Proposals) != 1 {
		t.Fatalf("proposals = %#v, want one consolidation proposal", health.Proposals)
	}
	proposal := health.Proposals[0]
	if proposal.Code != "consolidation_reason_task" || proposal.Action != "start_reason_task" || !proposal.ApprovalRequired {
		t.Fatalf("proposal shape = %#v", proposal)
	}
	if proposal.Required {
		t.Fatalf("proposal.required = true at warning threshold")
	}
	assertContractJSON(t, mustHarness(t), "semanticHealth", health)

	// Empty health has no proposals.
	emptyFixture := newHealthFixture(t, project.KindPentest)
	empty, err := emptyFixture.service.ProjectSemanticHealth(context.Background(), emptyFixture.projectID)
	if err != nil {
		t.Fatalf("empty health: %v", err)
	}
	if len(empty.Proposals) != 0 {
		t.Fatalf("empty proposals = %#v", empty.Proposals)
	}
}

type healthFixture struct {
	db             *store.DB
	dbPath         string
	service        *blackboardv2.Service
	projectID      string
	continuationID string
	workdir        string
	runtimeRoot    string
}

func newHealthFixture(t *testing.T, kind string) healthFixture {
	t.Helper()
	root := t.TempDir()
	dbPath := filepath.Join(root, "pentest.db")
	db, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("open health store: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	projects := project.NewService(db)
	createdProject, err := projects.CreateWithKind("Health fixture", "", kind, project.Scope{}, project.Defaults{})
	if err != nil {
		t.Fatalf("create Project: %v", err)
	}
	tasks := task.NewService(db, projects)
	createdTask, err := tasks.Create(task.CreateRequest{ProjectID: createdProject.ID, Goal: "Semantic health", Runner: task.RunnerSandbox})
	if err != nil {
		t.Fatalf("create Task: %v", err)
	}
	continuation, err := tasks.CreateContinuation(createdTask.ID, "profile-health", "codex", task.RunnerSandbox)
	if err != nil {
		t.Fatalf("create Continuation: %v", err)
	}
	runtimeRoot := filepath.Join(root, "runtime")
	workdir := filepath.Join(runtimeRoot, createdTask.ID, "workdir")
	if err := os.MkdirAll(workdir, 0o700); err != nil {
		t.Fatalf("mkdir workdir: %v", err)
	}
	return healthFixture{
		db: db, dbPath: dbPath,
		service: blackboardv2.NewServiceWithEvidence(db, blackboardv2.EvidenceConfig{
			RuntimeRoot: runtimeRoot, ArtifactRoot: runtimeRoot,
		}),
		projectID: createdProject.ID, continuationID: continuation.ID,
		workdir: workdir, runtimeRoot: runtimeRoot,
	}
}

func assertHealthyEmpty(t *testing.T, health blackboardv2.SemanticHealth) {
	t.Helper()
	if health.Schema != "blackboard-health/v2" {
		t.Fatalf("schema = %q", health.Schema)
	}
	if health.Revision != 0 || health.Status != "healthy" {
		t.Fatalf("empty health = %#v", health)
	}
	if health.Attention.State != blackboardv2.AttentionWithinTarget || !health.Attention.Complete || !health.Attention.Launchable {
		t.Fatalf("empty attention = %#v", health.Attention)
	}
	if health.Attention.ConsolidationOffered || health.Attention.ConsolidationRequired {
		t.Fatalf("empty consolidation flags set: %#v", health.Attention)
	}
	if len(health.Anomalies) != 0 {
		t.Fatalf("empty anomalies = %#v", health.Anomalies)
	}
	if len(health.Proposals) != 0 {
		t.Fatalf("empty proposals = %#v", health.Proposals)
	}
}

func assertNoAuditNoise(t *testing.T, health blackboardv2.SemanticHealth) {
	t.Helper()
	raw := mustHealthJSON(t, health)
	for _, forbidden := range []string{
		"provenance", "state_hash", "projection_hash", "semantic_hash",
		"graph_hash", "history_hash", "operation_history", "recent_changes",
		"audit", "checker_version", "health_run", "sqlite_integrity",
		"frontier", "current_truth", "project_id", "trusted_origin",
	} {
		if bytes.Contains(bytes.ToLower(raw), []byte(forbidden)) {
			t.Fatalf("health leaked audit/provenance noise %q: %s", forbidden, raw)
		}
	}
}

func assertHasAnomalyCodes(t *testing.T, health blackboardv2.SemanticHealth, codes []string) {
	t.Helper()
	present := map[string]bool{}
	for _, anomaly := range health.Anomalies {
		present[anomaly.Code] = true
	}
	for _, code := range codes {
		if !present[code] {
			t.Fatalf("missing anomaly code %q in %#v", code, health.Anomalies)
		}
	}
}

func assertNoAnomalyCode(t *testing.T, health blackboardv2.SemanticHealth, code string) {
	t.Helper()
	for _, anomaly := range health.Anomalies {
		if anomaly.Code == code {
			t.Fatalf("unexpected anomaly %q: %#v", code, anomaly)
		}
	}
}

func anomalyByCode(t *testing.T, health blackboardv2.SemanticHealth, code string) blackboardv2.HealthAnomaly {
	t.Helper()
	for _, anomaly := range health.Anomalies {
		if anomaly.Code == code {
			return anomaly
		}
	}
	t.Fatalf("anomaly %q not found in %#v", code, health.Anomalies)
	return blackboardv2.HealthAnomaly{}
}

func containsKey(keys []string, want string) bool {
	for _, key := range keys {
		if key == want {
			return true
		}
	}
	return false
}

func mustHealthJSON(t *testing.T, health blackboardv2.SemanticHealth) []byte {
	t.Helper()
	raw, err := json.Marshal(health)
	if err != nil {
		t.Fatalf("marshal health: %v", err)
	}
	// Anomalies must be sorted deterministically in the marshaled form.
	var decoded blackboardv2.SemanticHealth
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("decode health: %v", err)
	}
	if !reflect.DeepEqual(decoded.Anomalies, health.Anomalies) {
		t.Fatalf("anomaly order unstable under marshal")
	}
	return raw
}
