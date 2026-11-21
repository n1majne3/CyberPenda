package blackboardv2_test

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"pentest/internal/blackboardv2"
	"pentest/internal/project"
	"pentest/internal/store"
)

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
	if health.Attention.Complete {
		t.Fatalf("attention claimed complete despite corrupted relationships: %#v", health.Attention)
	}
	if !health.Attention.Launchable {
		t.Fatalf("health blocked launch under corruption: %#v", health.Attention)
	}
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
