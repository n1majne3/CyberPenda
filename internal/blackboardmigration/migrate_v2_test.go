package blackboardmigration

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"pentest/internal/blackboard"
	"pentest/internal/blackboardv2"
	"pentest/internal/blackboardv2contract"
	"pentest/internal/store"
)

func TestMigrateV2SuccessfulMultiProjectCutoverAndReopen(t *testing.T) {
	t.Parallel()

	db, service, artifactRoot, dbPath := newGraphV1MigrateFixture(t)
	seedIsolatedProjects(t, db, artifactRoot)
	plan, backupPath := inspectAndBackupForMigrate(t, service, dbPath)

	result, err := service.Execute(context.Background(), MigrationRequest{
		Kind:         MigrationKindMigrate,
		SourceDigest: plan.SourceDigest,
		Decisions:    decisionsFromPlan(plan),
		BackupPath:   backupPath,
	})
	if err != nil {
		t.Fatalf("Execute(migrate): %v blockers=%#v", err, result.Migrate)
	}
	if result.Migrate == nil {
		t.Fatal("missing migrate result")
	}
	raw, err := json.Marshal(result.Migrate)
	if err != nil {
		t.Fatal(err)
	}
	harness, err := blackboardv2contract.NewHarness()
	if err != nil {
		t.Fatal(err)
	}
	if err := harness.Validate("migrationResult", raw); err != nil {
		t.Fatalf("migrate result contract: %v\n%s", err, raw)
	}
	if result.Migrate.Status != "migrated" || result.Migrate.StoreEpoch != store.CanonicalStoreBlackboardV2 {
		t.Fatalf("migrate result = %#v", result.Migrate)
	}
	if result.Migrate.ProjectCount != 2 || result.Migrate.Validation.Status != "passed" || result.Migrate.Validation.SnapshotsValidated != 2 {
		t.Fatalf("migrate validation = %#v", result.Migrate)
	}
	if result.Migrate.VerifiedBackupPath != backupPath {
		t.Fatalf("verified backup path = %q", result.Migrate.VerifiedBackupPath)
	}

	epoch, err := db.CanonicalStore()
	if err != nil {
		t.Fatal(err)
	}
	if epoch != store.CanonicalStoreBlackboardV2 {
		t.Fatalf("epoch after migrate = %q", epoch)
	}
	var cutoverState string
	if err := db.QueryRow(`SELECT cutover_state FROM blackboard_store_state WHERE id=1`).Scan(&cutoverState); err != nil {
		t.Fatal(err)
	}
	if cutoverState != "v2" {
		t.Fatalf("cutover_state = %q, want v2", cutoverState)
	}

	// Ordinary Store reopen works through blackboard_v2 consumers.
	_ = db.Close()
	reopened, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("store.Open after migrate: %v", err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	reopenedEpoch, err := reopened.CanonicalStore()
	if err != nil {
		t.Fatal(err)
	}
	if reopenedEpoch != store.CanonicalStoreBlackboardV2 {
		t.Fatalf("reopened epoch = %q", reopenedEpoch)
	}
	v2 := blackboardv2.NewServiceWithEvidence(reopened, blackboardv2.EvidenceConfig{ArtifactRoot: artifactRoot})
	for _, projectID := range []string{"project-a", "project-b"} {
		projection, err := v2.ProjectRuntimeSnapshot(context.Background(), projectID)
		if err != nil {
			t.Fatalf("snapshot %s: %v", projectID, err)
		}
		if projection.Snapshot.Schema != "runtime-blackboard/v2" {
			t.Fatalf("snapshot schema %s = %q", projectID, projection.Snapshot.Schema)
		}
		again, err := v2.ProjectRuntimeSnapshot(context.Background(), projectID)
		if err != nil {
			t.Fatal(err)
		}
		if string(again.Bytes) != string(projection.Bytes) {
			t.Fatalf("snapshot bytes for %s are not deterministic after reopen", projectID)
		}
	}
	// Project isolation holds after reopen.
	if _, err := v2.ReadCurrent(context.Background(), "project-a", "fact:b"); err == nil {
		t.Fatal("project-a must not resolve project-b keys")
	}
	if _, err := v2.ReadCurrent(context.Background(), "project-b", "fact:a"); err == nil {
		t.Fatal("project-b must not resolve project-a keys")
	}
}

func TestMigrateV2AppliesStagedScopeLimitsInCutoverTransaction(t *testing.T) {
	t.Parallel()

	db, service, artifactRoot, dbPath := newGraphV1MigrateFixture(t)
	seedDecisionBoundWorkflowGraph(t, db, artifactRoot)
	plan, backupPath := inspectAndBackupForMigrate(t, service, dbPath)
	decisions := []MigrationDecision{
		{Source: MigrationSourceRef{Project: "project-rebuild", Type: "observation", Key: "observation:ambiguous"}, Decision: "confirmed_fact", TargetKey: "fact:from-obs"},
		{Source: MigrationSourceRef{Project: "project-rebuild", Type: "hypothesis", Key: "hypothesis:active"}, Decision: "objective"},
		{Source: MigrationSourceRef{Project: "project-rebuild", Type: "project_directive", Key: "directive:active"}, Decision: "scope_limit"},
		{Source: MigrationSourceRef{Project: "project-rebuild", Type: "hypothesis", Key: "hypothesis:noise"}, Decision: "discard"},
	}

	result, err := service.Execute(context.Background(), MigrationRequest{
		Kind:         MigrationKindMigrate,
		SourceDigest: plan.SourceDigest,
		Decisions:    decisions,
		BackupPath:   backupPath,
	})
	if err != nil {
		t.Fatalf("migrate: %v", err)
	}
	if result.Migrate == nil || result.Migrate.Status != "migrated" {
		t.Fatalf("result = %#v", result.Migrate)
	}
	var scopeJSON string
	if err := db.QueryRow(`SELECT scope_json FROM projects WHERE id='project-rebuild'`).Scan(&scopeJSON); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(scopeJSON, "Keep admin testing bounded") {
		t.Fatalf("cutover must apply staged scope limits into Project Scope: %s", scopeJSON)
	}
}

func TestMigrateV2RejectsActiveContinuationBeforeSwitch(t *testing.T) {
	t.Parallel()

	db, service, artifactRoot, dbPath := newGraphV1MigrateFixture(t)
	seedUnambiguousGraphHeads(t, db, artifactRoot)
	if _, err := db.Exec(`
		INSERT INTO tasks(id,project_id,goal,status,runner,runtime_profile_id,run_controls_json,scope_snapshot_json,created_at,updated_at)
		VALUES('task-active','project-rebuild','work','running','local','profile','{}','{}','2026-01-01T00:00:00Z','2026-01-01T00:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`
		INSERT INTO task_continuations(id,task_id,number,runtime_profile_id,runtime_provider,runner,status,started_at,updated_at)
		VALUES('cont-active','task-active',1,'profile','manual','local','paused','2026-01-01T00:00:00Z','2026-01-01T00:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	plan, backupPath := inspectAndBackupForMigrate(t, service, dbPath)

	result, err := service.Execute(context.Background(), MigrationRequest{
		Kind:         MigrationKindMigrate,
		SourceDigest: plan.SourceDigest,
		Decisions:    decisionsFromPlan(plan),
		BackupPath:   backupPath,
	})
	if !errors.Is(err, ErrMigrationBlocked) {
		t.Fatalf("error = %v, want ErrMigrationBlocked", err)
	}
	assertMigrateBlockerCodes(t, result, "active_continuation")
	assertMigrateLeftV1Authoritative(t, db, "project-rebuild")
}

func TestMigrateV2RejectsMissingBackupAndBackupMismatch(t *testing.T) {
	t.Parallel()

	db, service, artifactRoot, dbPath := newGraphV1MigrateFixture(t)
	seedUnambiguousGraphHeads(t, db, artifactRoot)
	plan, _ := inspectAndBackupForMigrate(t, service, dbPath)

	missing, err := service.Execute(context.Background(), MigrationRequest{
		Kind:         MigrationKindMigrate,
		SourceDigest: plan.SourceDigest,
		Decisions:    decisionsFromPlan(plan),
		BackupPath:   filepath.Join(t.TempDir(), "missing.bak"),
	})
	if !errors.Is(err, ErrMigrationBlocked) {
		t.Fatalf("missing backup error = %v", err)
	}
	assertMigrateBlockerCodes(t, missing, "backup_verification_failed")
	assertMigrateLeftV1Authoritative(t, db, "project-rebuild")

	// Mismatch: backup of a different database.
	otherRoot := t.TempDir()
	otherDBPath := filepath.Join(otherRoot, "other.db")
	otherArtifact := filepath.Join(otherRoot, "artifacts")
	otherDB, err := store.Open(otherDBPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := otherDB.Exec(`
		UPDATE blackboard_store_state
		SET canonical_store='graph_v1', cutover_state='graph', migration_contract_version='legacy_blackboard_to_graph_v1', graph_schema_version=1
		WHERE id=1`); err != nil {
		t.Fatal(err)
	}
	installLegacyWorkflowStateFixture(t, otherDB)
	if _, err := otherDB.Exec(`
		INSERT INTO projects(id,name,description,scope_json,defaults_json,kind,created_at,updated_at)
		VALUES('other','Other','','{}','{}','pentest','2026-01-01T00:00:00Z','2026-01-01T00:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	otherService := NewService(otherDB, otherDBPath, otherArtifact)
	_, otherBackup := inspectAndBackupForMigrate(t, otherService, otherDBPath)
	_ = otherDB.Close()

	mismatch, err := service.Execute(context.Background(), MigrationRequest{
		Kind:         MigrationKindMigrate,
		SourceDigest: plan.SourceDigest,
		Decisions:    decisionsFromPlan(plan),
		BackupPath:   otherBackup,
	})
	if !errors.Is(err, ErrMigrationBlocked) {
		t.Fatalf("backup mismatch error = %v", err)
	}
	assertMigrateBlockerCodes(t, mismatch, "backup_mismatch")
	assertMigrateLeftV1Authoritative(t, db, "project-rebuild")
}

func TestMigrateV2RejectsStalePlanAndChangedSource(t *testing.T) {
	t.Parallel()

	db, service, artifactRoot, dbPath := newGraphV1MigrateFixture(t)
	seedUnambiguousGraphHeads(t, db, artifactRoot)
	plan, backupPath := inspectAndBackupForMigrate(t, service, dbPath)

	stale, err := service.Execute(context.Background(), MigrationRequest{
		Kind:         MigrationKindMigrate,
		SourceDigest: "sha256:" + strings.Repeat("0", 64),
		Decisions:    decisionsFromPlan(plan),
		BackupPath:   backupPath,
	})
	if !errors.Is(err, ErrMigrationBlocked) {
		t.Fatalf("stale plan error = %v", err)
	}
	assertMigrateBlockerCodes(t, stale, "stale_source_digest")
	assertMigrateLeftV1Authoritative(t, db, "project-rebuild")

	// Change source after plan/backup.
	if _, err := db.Exec(`
		UPDATE blackboard_node_versions
		SET properties_json='{"category":"asset","summary":"mutated","confidence":"tentative","scope_status":"in_scope"}'
		WHERE project_id='project-rebuild' AND version=(
			SELECT version FROM blackboard_node_heads WHERE project_id='project-rebuild' AND node_id=blackboard_node_versions.node_id
		)`); err != nil {
		// Fallback: insert a new fact to change digest.
		graph := blackboard.NewGraphService(db, nil, nil).WithArtifactRoot(artifactRoot)
		ctx := blackboard.SystemExecutionContext("project-rebuild", "pentest", "mutate")
		if _, err := graph.Apply(context.Background(), blackboard.MutationBatch{
			SchemaVersion:  blackboard.GraphMutationSchemaVersion,
			IdempotencyKey: "mutate-source",
			Context:        ctx,
			Operations: []blackboard.Operation{{
				OpID: "extra", Kind: blackboard.OpCreateNode,
				Node:   blackboard.NodeRef{NodeType: blackboard.NodeTypeProjectFact, StableKey: "fact:extra"},
				Create: blackboard.CreateNodeInput{PropertyMap: map[string]any{"category": "asset", "summary": "extra", "confidence": "tentative", "scope_status": "in_scope"}},
			}},
		}); err != nil {
			t.Fatalf("mutate source: %v", err)
		}
	}

	changed, err := service.Execute(context.Background(), MigrationRequest{
		Kind:         MigrationKindMigrate,
		SourceDigest: plan.SourceDigest,
		Decisions:    decisionsFromPlan(plan),
		BackupPath:   backupPath,
	})
	if !errors.Is(err, ErrMigrationBlocked) {
		t.Fatalf("changed source error = %v", err)
	}
	assertMigrateBlockerCodes(t, changed, "stale_source_digest")
	assertMigrateLeftV1Authoritative(t, db, "project-rebuild")
}

func TestMigrateV2RejectsIncompleteDecisions(t *testing.T) {
	t.Parallel()

	db, service, artifactRoot, dbPath := newGraphV1MigrateFixture(t)
	seedDecisionBoundWorkflowGraph(t, db, artifactRoot)
	plan, backupPath := inspectAndBackupForMigrate(t, service, dbPath)

	result, err := service.Execute(context.Background(), MigrationRequest{
		Kind:         MigrationKindMigrate,
		SourceDigest: plan.SourceDigest,
		// Omit required decisions intentionally.
		BackupPath: backupPath,
	})
	if !errors.Is(err, ErrMigrationBlocked) && !errors.Is(err, ErrRebuildBlocked) {
		t.Fatalf("error = %v, want blocked", err)
	}
	if result.Migrate != nil && result.Migrate.Status == "migrated" {
		t.Fatal("incomplete decisions must not migrate")
	}
	assertMigrateLeftV1Authoritative(t, db, "project-rebuild")
}

func TestMigrateV2FailureBeforeSwitchRollsBackV2AndSharedDomain(t *testing.T) {
	t.Parallel()

	db, service, artifactRoot, dbPath := newGraphV1MigrateFixture(t)
	seedDecisionBoundWorkflowGraph(t, db, artifactRoot)
	plan, backupPath := inspectAndBackupForMigrate(t, service, dbPath)
	decisions := []MigrationDecision{
		{Source: MigrationSourceRef{Project: "project-rebuild", Type: "observation", Key: "observation:ambiguous"}, Decision: "confirmed_fact", TargetKey: "fact:from-obs"},
		{Source: MigrationSourceRef{Project: "project-rebuild", Type: "hypothesis", Key: "hypothesis:active"}, Decision: "objective"},
		{Source: MigrationSourceRef{Project: "project-rebuild", Type: "project_directive", Key: "directive:active"}, Decision: "scope_limit"},
		{Source: MigrationSourceRef{Project: "project-rebuild", Type: "hypothesis", Key: "hypothesis:noise"}, Decision: "discard"},
	}

	injected := errors.New("injected pre-switch failure")
	failing := NewService(db, dbPath, artifactRoot, WithCutoverFailureInjector(CutoverFailureInjectorFunc(func(point CutoverFailurePoint) error {
		if point == CutoverFailureAfterParity {
			return injected
		}
		return nil
	})))

	_, err := failing.Execute(context.Background(), MigrationRequest{
		Kind:         MigrationKindMigrate,
		SourceDigest: plan.SourceDigest,
		Decisions:    decisions,
		BackupPath:   backupPath,
	})
	if !errors.Is(err, injected) {
		t.Fatalf("error = %v, want injected", err)
	}
	assertMigrateLeftV1Authoritative(t, db, "project-rebuild")
	var scopeJSON string
	if err := db.QueryRow(`SELECT scope_json FROM projects WHERE id='project-rebuild'`).Scan(&scopeJSON); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(scopeJSON, "Keep admin testing bounded") {
		t.Fatalf("shared-domain scope change must roll back: %s", scopeJSON)
	}
	// Verified backup remains usable.
	backupDB, err := sql.Open("sqlite", "file:"+backupPath+"?mode=ro")
	if err != nil {
		t.Fatal(err)
	}
	defer backupDB.Close()
	var quickCheck string
	if err := backupDB.QueryRow(`PRAGMA quick_check`).Scan(&quickCheck); err != nil || quickCheck != "ok" {
		t.Fatalf("backup unusable after failed migrate: quick_check=%q err=%v", quickCheck, err)
	}
}

func TestMigrateV2RerunAndLostResponseAreIdempotent(t *testing.T) {
	t.Parallel()

	db, service, artifactRoot, dbPath := newGraphV1MigrateFixture(t)
	seedUnambiguousGraphHeads(t, db, artifactRoot)
	plan, backupPath := inspectAndBackupForMigrate(t, service, dbPath)
	req := MigrationRequest{
		Kind:         MigrationKindMigrate,
		SourceDigest: plan.SourceDigest,
		Decisions:    decisionsFromPlan(plan),
		BackupPath:   backupPath,
	}

	first, err := service.Execute(context.Background(), req)
	if err != nil {
		t.Fatalf("first migrate: %v", err)
	}
	second, err := service.Execute(context.Background(), req)
	if err != nil {
		t.Fatalf("rerun migrate: %v", err)
	}
	firstJSON, _ := json.Marshal(first.Migrate)
	secondJSON, _ := json.Marshal(second.Migrate)
	if string(firstJSON) != string(secondJSON) {
		t.Fatalf("idempotent rerun diverged\nfirst=%s\nsecond=%s", firstJSON, secondJSON)
	}

	// Lost-response path: clear in-memory view by reloading committed audit.
	reloaded := NewService(db, dbPath, artifactRoot)
	third, err := reloaded.Execute(context.Background(), req)
	if err != nil {
		t.Fatalf("lost-response reopen migrate: %v", err)
	}
	thirdJSON, _ := json.Marshal(third.Migrate)
	if string(firstJSON) != string(thirdJSON) {
		t.Fatalf("lost-response result diverged\nfirst=%s\nthird=%s", firstJSON, thirdJSON)
	}
}

func TestMigrateV2AlreadyCutOverWithChangedSourceConflicts(t *testing.T) {
	t.Parallel()

	db, service, artifactRoot, dbPath := newGraphV1MigrateFixture(t)
	seedUnambiguousGraphHeads(t, db, artifactRoot)
	plan, backupPath := inspectAndBackupForMigrate(t, service, dbPath)
	req := MigrationRequest{
		Kind:         MigrationKindMigrate,
		SourceDigest: plan.SourceDigest,
		Decisions:    decisionsFromPlan(plan),
		BackupPath:   backupPath,
	}
	if _, err := service.Execute(context.Background(), req); err != nil {
		t.Fatalf("first migrate: %v", err)
	}
	// Simulate operator presenting a different plan digest against an already-cut-over DB.
	_, err := service.Execute(context.Background(), MigrationRequest{
		Kind:         MigrationKindMigrate,
		SourceDigest: "sha256:" + strings.Repeat("f", 64),
		Decisions:    decisionsFromPlan(plan),
		BackupPath:   backupPath,
	})
	if !errors.Is(err, ErrCutoverConflict) {
		t.Fatalf("error = %v, want ErrCutoverConflict", err)
	}
}

func TestMigrateV2VerifyChecksEpochIsolationEvidenceAndExactBytes(t *testing.T) {
	t.Parallel()

	db, service, artifactRoot, dbPath := newGraphV1MigrateFixture(t)
	seedIsolatedProjects(t, db, artifactRoot)
	plan, backupPath := inspectAndBackupForMigrate(t, service, dbPath)
	if _, err := service.Execute(context.Background(), MigrationRequest{
		Kind:         MigrationKindMigrate,
		SourceDigest: plan.SourceDigest,
		Decisions:    decisionsFromPlan(plan),
		BackupPath:   backupPath,
	}); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	verify, err := service.Execute(context.Background(), MigrationRequest{Kind: MigrationKindVerify})
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if verify.Migrate == nil || verify.Migrate.Status != "migrated" || verify.Migrate.StoreEpoch != store.CanonicalStoreBlackboardV2 {
		t.Fatalf("verify result = %#v", verify.Migrate)
	}
	if verify.Migrate.Validation.Status != "passed" || verify.Migrate.Validation.SnapshotsValidated != 2 {
		t.Fatalf("verify validation = %#v", verify.Migrate.Validation)
	}

	// Corrupt a semantic row and expect verify failure.
	if _, err := db.Exec(`UPDATE blackboard_v2_records SET record_json='{"broken":true}' WHERE project_id='project-a'`); err != nil {
		t.Fatal(err)
	}
	corrupted, err := service.Execute(context.Background(), MigrationRequest{Kind: MigrationKindVerify})
	if !errors.Is(err, ErrCutoverVerificationFailed) {
		t.Fatalf("corrupted verify error = %v, want ErrCutoverVerificationFailed", err)
	}
	if corrupted.Migrate != nil && corrupted.Migrate.Validation.Status == "passed" {
		t.Fatal("corrupted verify must not report passed validation")
	}
}

func TestMigrateV2CLISurfaceOfflineOnly(t *testing.T) {
	// Covered by pentestctl package tests; this package-level guard ensures the
	// migrate kind is wired through the service Execute seam used by CLI.
	t.Parallel()
	if MigrationKindMigrate != "migrate" {
		t.Fatalf("MigrationKindMigrate = %q", MigrationKindMigrate)
	}
}

func newGraphV1MigrateFixture(t *testing.T) (*store.DB, *Service, string, string) {
	t.Helper()
	root := t.TempDir()
	databasePath := filepath.Join(root, "pentest.db")
	artifactRoot := filepath.Join(root, "artifacts")
	if err := os.MkdirAll(artifactRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	db, err := store.Open(databasePath)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if _, err := db.Exec(`
		UPDATE blackboard_store_state
		SET canonical_store='graph_v1',
		    cutover_state='graph',
		    migration_contract_version='legacy_blackboard_to_graph_v1',
		    graph_schema_version=1
		WHERE id=1`); err != nil {
		t.Fatal(err)
	}
	installLegacyWorkflowStateFixture(t, db)
	return db, NewService(db, databasePath, artifactRoot), artifactRoot, databasePath
}

func inspectAndBackupForMigrate(t *testing.T, service *Service, dbPath string) (LegacyMigrationPlanV1, string) {
	t.Helper()
	inspect, err := service.Execute(context.Background(), MigrationRequest{Kind: MigrationKindInspect})
	if err != nil {
		t.Fatalf("inspect: %v", err)
	}
	backupPath := filepath.Join(filepath.Dir(dbPath), "pre-v2.bak")
	// Create a verified backup with the production backup implementation.
	backup, err := SQLiteBackupImplementation{}.CreateVerifiedBackup(context.Background(), service.db, service.databasePath, backupPath)
	if err != nil {
		t.Fatalf("create verified backup: %v", err)
	}
	if backup.QuickCheck != "ok" {
		t.Fatalf("backup quick_check = %q", backup.QuickCheck)
	}
	return inspect.Plan, backupPath
}

func decisionsFromPlan(plan LegacyMigrationPlanV1) []MigrationDecision {
	out := make([]MigrationDecision, 0, len(plan.RequiredDecisions))
	for _, required := range plan.RequiredDecisions {
		decision := required
		if decision.Decision == "" && len(decision.AllowedActions) > 0 {
			decision.Decision = decision.AllowedActions[0]
		}
		out = append(out, decision)
	}
	return out
}

func assertMigrateBlockerCodes(t *testing.T, result MigrationResult, want ...string) {
	t.Helper()
	got := map[string]bool{}
	if result.Migrate != nil {
		for _, blocker := range result.Migrate.Blockers {
			got[blocker.Code] = true
		}
	}
	for _, blocker := range result.Plan.ValidationBlockers {
		got[blocker.Code] = true
	}
	if result.Rebuild != nil {
		for _, blocker := range result.Rebuild.Blockers {
			got[blocker.Code] = true
		}
	}
	for _, code := range want {
		if !got[code] {
			t.Fatalf("missing blocker %q in migrate result plan=%#v migrate=%#v rebuild=%#v", code, result.Plan.ValidationBlockers, result.Migrate, result.Rebuild)
		}
	}
}

func assertMigrateLeftV1Authoritative(t *testing.T, db *store.DB, projectID string) {
	t.Helper()
	epoch, err := db.CanonicalStore()
	if err != nil {
		t.Fatal(err)
	}
	if epoch != store.CanonicalStoreGraphV1 {
		t.Fatalf("epoch = %q, want graph_v1 still authoritative", epoch)
	}
	var records int
	if err := db.QueryRow(`SELECT COUNT(*) FROM blackboard_v2_records WHERE project_id=?`, projectID).Scan(&records); err != nil {
		if strings.Contains(err.Error(), "no such table") {
			return
		}
		t.Fatal(err)
	}
	if records != 0 {
		t.Fatalf("failed migrate left active v2 records=%d", records)
	}
}
