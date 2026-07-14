package blackboardmigration

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"

	"pentest/internal/blackboard"
	"pentest/internal/store"
)

func TestCutoverCommitsImportParityEpochFlipAndLegacyWriteGuardsAtomically(t *testing.T) {
	t.Parallel()

	db, service := newInspectionService(t)
	insertCutoverGuardFixture(t, db)
	backupPath := filepath.Join(t.TempDir(), "legacy.bak")

	result, err := service.Execute(context.Background(), MigrationRequest{
		Kind:       MigrationKindCutover,
		BackupPath: backupPath,
	})
	if err != nil {
		t.Fatalf("Execute(cutover): %v", err)
	}
	if result.Backup == nil || result.Import == nil || !result.Import.MappingVerified {
		t.Fatalf("cutover result = %#v", result)
	}

	epoch, err := db.CanonicalStore()
	if err != nil {
		t.Fatal(err)
	}
	if epoch != store.CanonicalStoreGraphV1 {
		t.Fatalf("canonical store = %q, want graph_v1", epoch)
	}
	var cutoverState, cutoverID, sourceDigest, mappingDigest string
	if err := db.QueryRow(`SELECT cutover_state,cutover_id,source_digest,mapping_digest FROM blackboard_store_state WHERE id=1`).Scan(&cutoverState, &cutoverID, &sourceDigest, &mappingDigest); err != nil {
		t.Fatal(err)
	}
	if cutoverState != "graph" || cutoverID == "" || sourceDigest != result.Plan.SourceDigest || mappingDigest != result.Import.MappingDigest {
		t.Fatalf("store state = (%q,%q,%q,%q), result=%#v", cutoverState, cutoverID, sourceDigest, mappingDigest, result)
	}

	graph := blackboard.NewGraphService(db, nil, nil)
	if _, err := graph.ReadNode(context.Background(), blackboard.ReadNodeRequest{
		ProjectID: "project-1", NodeType: blackboard.NodeTypeProjectFact, Key: "key:fact-cutover",
	}); err != nil {
		t.Fatalf("read imported Project Fact: %v", err)
	}
	for _, table := range cutoverGuardTables {
		for _, statement := range legacyWriteStatements(table) {
			if _, err := db.Exec(statement); err == nil || !strings.Contains(err.Error(), "frozen after graph_v1 cutover") {
				t.Fatalf("legacy write was not guarded for %s: statement=%q err=%v", table, statement, err)
			}
		}
	}
}

func TestCutoverFailurePointsRollbackLegacyEpochAndAllGraphState(t *testing.T) {
	t.Parallel()

	for _, point := range []CutoverFailurePoint{
		CutoverFailureAfterDDL,
		CutoverFailureAfterProjectImport,
		CutoverFailureAfterMappings,
		CutoverFailureAfterHeadBuild,
		CutoverFailureAfterParity,
		CutoverFailureAfterGuards,
		CutoverFailureAfterStateFlip,
	} {
		point := point
		t.Run(string(point), func(t *testing.T) {
			db, base := newInspectionService(t)
			insertProjectAndFacts(t, db, []string{"fact-failure"})
			injected := errors.New("injected cutover failure at " + string(point))
			service := NewService(db, base.databasePath, base.artifactRoot, WithCutoverFailureInjector(
				CutoverFailureInjectorFunc(func(got CutoverFailurePoint) error {
					if got == point {
						return injected
					}
					return nil
				}),
			))

			_, err := service.Execute(context.Background(), MigrationRequest{Kind: MigrationKindCutover, BackupPath: filepath.Join(t.TempDir(), "legacy.bak")})
			if !errors.Is(err, injected) {
				t.Fatalf("Execute(cutover) error = %v, want injected failure", err)
			}
			assertLegacySourceAndEpoch(t, db, 1)
			for _, table := range []string{"blackboard_graph_mutations", "blackboard_node_versions", "blackboard_legacy_mappings"} {
				var count int
				if err := db.QueryRow(`SELECT COUNT(*) FROM ` + table).Scan(&count); err != nil {
					t.Fatal(err)
				}
				if count != 0 {
					t.Fatalf("%s retained %d rolled-back rows", table, count)
				}
			}
			var guards int
			if err := db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='trigger' AND name LIKE 'blackboard_legacy_%_guard'`).Scan(&guards); err != nil {
				t.Fatal(err)
			}
			if guards != 0 {
				t.Fatalf("rolled-back cutover retained %d legacy guards", guards)
			}
		})
	}
}

func TestCommittedCutoverRetryReturnsOriginalResultAndChangedSourceDigestConflicts(t *testing.T) {
	t.Parallel()

	db, service := newInspectionService(t)
	insertProjectAndFacts(t, db, []string{"fact-retry"})
	first, err := service.Execute(context.Background(), MigrationRequest{Kind: MigrationKindCutover, BackupPath: filepath.Join(t.TempDir(), "legacy.bak")})
	if err != nil {
		t.Fatalf("first Execute(cutover): %v", err)
	}
	second, err := service.Execute(context.Background(), MigrationRequest{Kind: MigrationKindCutover})
	if err != nil {
		t.Fatalf("retry Execute(cutover): %v", err)
	}
	if first.Verification == nil || second.Verification == nil || first.Verification.CutoverID != second.Verification.CutoverID || first.Verification.ResultHash != second.Verification.ResultHash {
		t.Fatalf("cutover retry changed result: first=%#v second=%#v", first.Verification, second.Verification)
	}
	var mutations int
	if err := db.QueryRow(`SELECT COUNT(*) FROM blackboard_graph_mutations`).Scan(&mutations); err != nil {
		t.Fatal(err)
	}
	if mutations != 1 {
		t.Fatalf("cutover retry imported twice: mutations=%d", mutations)
	}

	if _, err := db.Exec(`UPDATE projects SET name='changed after cutover' WHERE id='project-1'`); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Execute(context.Background(), MigrationRequest{Kind: MigrationKindCutover}); !errors.Is(err, ErrCutoverConflict) {
		t.Fatalf("changed-digest retry error = %v, want ErrCutoverConflict", err)
	}
}

func TestPostCommitVerifyDetectsProjectionCorruptionAndEntersRecoveryRequired(t *testing.T) {
	t.Parallel()

	db, service := newInspectionService(t)
	insertProjectAndFacts(t, db, []string{"fact-verify"})
	if _, err := service.Execute(context.Background(), MigrationRequest{Kind: MigrationKindCutover, BackupPath: filepath.Join(t.TempDir(), "legacy.bak")}); err != nil {
		t.Fatalf("Execute(cutover): %v", err)
	}
	if _, err := db.Exec(`UPDATE blackboard_graph_state SET current_main_projection_hash='corrupt' WHERE project_id='project-1'`); err != nil {
		t.Fatal(err)
	}

	_, err := service.Execute(context.Background(), MigrationRequest{Kind: MigrationKindVerify})
	if !errors.Is(err, ErrCutoverVerificationFailed) || !strings.Contains(err.Error(), "projection") {
		t.Fatalf("Execute(verify) error = %v, want projection verification failure", err)
	}
	var state string
	if err := db.QueryRow(`SELECT cutover_state FROM blackboard_store_state WHERE id=1`).Scan(&state); err != nil {
		t.Fatal(err)
	}
	if state != "recovery_required" {
		t.Fatalf("cutover state = %q, want recovery_required", state)
	}
}

func TestInitialPostCommitVerifyDetectsProjectionCorruptionAfterLostResponse(t *testing.T) {
	t.Parallel()

	db, base := newInspectionService(t)
	insertProjectAndFacts(t, db, []string{"fact-initial-verify"})
	injected := errors.New("lost cutover response")
	service := NewService(db, base.databasePath, base.artifactRoot, WithCutoverFailureInjector(
		CutoverFailureInjectorFunc(func(point CutoverFailurePoint) error {
			if point == CutoverFailureAfterCommit {
				return injected
			}
			return nil
		}),
	))
	if _, err := service.Execute(context.Background(), MigrationRequest{Kind: MigrationKindCutover, BackupPath: filepath.Join(t.TempDir(), "legacy.bak")}); !errors.Is(err, injected) {
		t.Fatalf("Execute(cutover) error = %v, want lost response", err)
	}
	if _, err := db.Exec(`UPDATE blackboard_graph_state SET current_main_projection_hash='corrupt' WHERE project_id='project-1'`); err != nil {
		t.Fatal(err)
	}

	service = NewService(db, base.databasePath, base.artifactRoot)
	_, err := service.Execute(context.Background(), MigrationRequest{Kind: MigrationKindVerify})
	if !errors.Is(err, ErrCutoverVerificationFailed) || !strings.Contains(err.Error(), "projection") {
		t.Fatalf("initial Execute(verify) error = %v, want projection verification failure", err)
	}
	var state string
	if err := db.QueryRow(`SELECT cutover_state FROM blackboard_store_state WHERE id=1`).Scan(&state); err != nil {
		t.Fatal(err)
	}
	if state != "recovery_required" {
		t.Fatalf("cutover state = %q, want recovery_required", state)
	}
}

func TestPostCommitVerifyDetectsStoreStateCorruption(t *testing.T) {
	t.Parallel()

	db, service := newInspectionService(t)
	insertProjectAndFacts(t, db, []string{"fact-source-verify"})
	if _, err := service.Execute(context.Background(), MigrationRequest{Kind: MigrationKindCutover, BackupPath: filepath.Join(t.TempDir(), "legacy.bak")}); err != nil {
		t.Fatalf("Execute(cutover): %v", err)
	}
	if _, err := db.Exec(`UPDATE blackboard_store_state SET graph_schema_version=999 WHERE id=1`); err != nil {
		t.Fatal(err)
	}

	_, err := service.Execute(context.Background(), MigrationRequest{Kind: MigrationKindVerify})
	if !errors.Is(err, ErrCutoverVerificationFailed) || !strings.Contains(err.Error(), "store state") {
		t.Fatalf("Execute(verify) error = %v, want store state failure", err)
	}
}

func TestRecoveryGuidanceNamesPostCutoverWritesWithoutImplicitReverseMigration(t *testing.T) {
	t.Parallel()

	db, service := newInspectionService(t)
	insertProjectAndFacts(t, db, []string{"fact-recovery"})
	cutover, err := service.Execute(context.Background(), MigrationRequest{Kind: MigrationKindCutover, BackupPath: filepath.Join(t.TempDir(), "legacy.bak")})
	if err != nil {
		t.Fatalf("Execute(cutover): %v", err)
	}
	graph := blackboard.NewGraphService(db, nil, nil)
	if _, err := graph.Apply(context.Background(), blackboard.MutationBatch{
		SchemaVersion:  blackboard.GraphMutationSchemaVersion,
		IdempotencyKey: "post-cutover-write",
		Context:        blackboard.SystemExecutionContext("project-1", "pentest", "test"),
		Operations: []blackboard.Operation{{
			OpID: "fact", Kind: blackboard.OpCreateNode,
			Node:   blackboard.NodeRef{NodeType: blackboard.NodeTypeProjectFact, StableKey: "fact:post-cutover"},
			Create: blackboard.CreateNodeInput{PropertyMap: map[string]any{"category": "test", "summary": "Post-cutover write", "body": "", "confidence": "tentative", "scope_status": "in_scope"}},
		}},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`DROP TRIGGER blackboard_legacy_project_facts_insert_guard`); err != nil {
		t.Fatal(err)
	}

	result, err := service.Execute(context.Background(), MigrationRequest{Kind: MigrationKindVerify})
	if !errors.Is(err, ErrCutoverVerificationFailed) {
		t.Fatalf("Execute(verify) error = %v", err)
	}
	if result.Recovery == nil || !result.Recovery.PostCutoverWrites || result.Recovery.CutoverID != cutover.Verification.CutoverID || result.Recovery.BackupPath != cutover.Backup.Path || result.Recovery.BackupSHA256 != cutover.Backup.SHA256 {
		t.Fatalf("recovery guidance = %#v", result.Recovery)
	}
	if !strings.Contains(result.Recovery.Warning, "would lose") || !strings.Contains(result.Recovery.Warning, "post-cutover") {
		t.Fatalf("recovery warning = %q", result.Recovery.Warning)
	}
	epoch, epochErr := db.CanonicalStore()
	if epochErr != nil {
		t.Fatal(epochErr)
	}
	if epoch != store.CanonicalStoreGraphV1 {
		t.Fatalf("verify implicitly reversed canonical store to %q", epoch)
	}
}

func TestPostCutoverVerifyUsesCurrentGraphCompatibilityParityAfterWrites(t *testing.T) {
	t.Parallel()

	db, service := newInspectionService(t)
	insertProjectAndFacts(t, db, []string{"fact-before-cutover"})
	if _, err := service.Execute(context.Background(), MigrationRequest{Kind: MigrationKindCutover, BackupPath: filepath.Join(t.TempDir(), "legacy.bak")}); err != nil {
		t.Fatalf("Execute(cutover): %v", err)
	}
	graph := blackboard.NewGraphService(db, nil, nil)
	if _, err := graph.Apply(context.Background(), blackboard.MutationBatch{
		SchemaVersion:  blackboard.GraphMutationSchemaVersion,
		IdempotencyKey: "post-cutover-parity",
		Context:        blackboard.SystemExecutionContext("project-1", "pentest", "test"),
		Operations: []blackboard.Operation{{
			OpID: "fact", Kind: blackboard.OpCreateNode,
			Node:   blackboard.NodeRef{NodeType: blackboard.NodeTypeProjectFact, StableKey: "fact:after-cutover"},
			Create: blackboard.CreateNodeInput{PropertyMap: map[string]any{"category": "test", "summary": "After cutover", "body": "", "confidence": "tentative", "scope_status": "in_scope"}},
		}},
	}); err != nil {
		t.Fatal(err)
	}

	result, err := service.Execute(context.Background(), MigrationRequest{Kind: MigrationKindVerify})
	if err != nil {
		t.Fatalf("Execute(verify) after graph write: %v", err)
	}
	if result.Verification == nil || result.Verification.ParityDigest == "" {
		t.Fatalf("post-cutover verification = %#v", result.Verification)
	}
}

func TestNoOpGraphCallDoesNotClaimPostCutoverWritesWouldBeLost(t *testing.T) {
	t.Parallel()

	db, service := newInspectionService(t)
	insertProjectAndFacts(t, db, []string{"fact-noop"})
	if _, err := service.Execute(context.Background(), MigrationRequest{Kind: MigrationKindCutover, BackupPath: filepath.Join(t.TempDir(), "legacy.bak")}); err != nil {
		t.Fatalf("Execute(cutover): %v", err)
	}
	graph := blackboard.NewGraphService(db, nil, nil)
	result, err := graph.Apply(context.Background(), blackboard.MutationBatch{
		SchemaVersion:  blackboard.GraphMutationSchemaVersion,
		IdempotencyKey: "post-cutover-noop",
		Context:        blackboard.SystemExecutionContext("project-1", "pentest", "test"),
		Operations: []blackboard.Operation{{
			OpID: "noop", Kind: blackboard.OpSetDisposition,
			Node:        blackboard.NodeRef{NodeType: blackboard.NodeTypeProjectFact, StableKey: "key:fact-noop"},
			Disposition: blackboard.SetDispositionInput{ExpectedVersion: 1, Disposition: blackboard.DispositionMain},
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Operations[0].Changed {
		t.Fatalf("expected no-op disposition result, got %#v", result.Operations[0])
	}
	var postCutoverWrites int
	if err := db.QueryRow(`SELECT post_cutover_write_committed FROM blackboard_store_state WHERE id=1`).Scan(&postCutoverWrites); err != nil {
		t.Fatal(err)
	}
	if postCutoverWrites != 0 {
		t.Fatalf("no-op graph call marked post-cutover writes committed: %d", postCutoverWrites)
	}
}

func TestCutoverCrashReopenBeforeAndAfterCommitSelectsOnlyOneCanonicalEpoch(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name      string
		point     CutoverFailurePoint
		wantEpoch string
	}{
		{name: "before_commit", point: CutoverFailureAfterStateFlip, wantEpoch: store.CanonicalStoreLegacyV1},
		{name: "after_commit", point: CutoverFailureAfterCommit, wantEpoch: store.CanonicalStoreGraphV1},
	} {
		test := test
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			databasePath := filepath.Join(root, "pentest.db")
			db, err := store.Open(databasePath)
			if err != nil {
				t.Fatal(err)
			}
			insertProjectAndFacts(t, db, []string{"fact-crash"})
			injected := errors.New("simulated process death")
			service := NewService(db, databasePath, root, WithCutoverFailureInjector(CutoverFailureInjectorFunc(func(point CutoverFailurePoint) error {
				if point == test.point {
					return injected
				}
				return nil
			})))
			if _, err := service.Execute(context.Background(), MigrationRequest{Kind: MigrationKindCutover, BackupPath: filepath.Join(root, "legacy.bak")}); !errors.Is(err, injected) {
				t.Fatalf("Execute(cutover) error = %v", err)
			}
			if err := db.Close(); err != nil {
				t.Fatal(err)
			}
			reopened, err := store.Open(databasePath)
			if err != nil {
				t.Fatal(err)
			}
			defer reopened.Close()
			epoch, err := reopened.CanonicalStore()
			if err != nil {
				t.Fatal(err)
			}
			if epoch != test.wantEpoch {
				t.Fatalf("reopened epoch = %q, want %q", epoch, test.wantEpoch)
			}
			if test.wantEpoch == store.CanonicalStoreGraphV1 {
				retry := NewService(reopened, databasePath, root)
				if _, err := retry.Execute(context.Background(), MigrationRequest{Kind: MigrationKindCutover}); err != nil {
					t.Fatalf("retry committed cutover: %v", err)
				}
				var mutations int
				if err := reopened.QueryRow(`SELECT COUNT(*) FROM blackboard_graph_mutations`).Scan(&mutations); err != nil {
					t.Fatal(err)
				}
				if mutations != 1 {
					t.Fatalf("committed cutover retry imported twice: %d mutations", mutations)
				}
			}
		})
	}
}

func TestPostCommitVerifyDetectsMappingGuardAndParityCorruption(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name    string
		damage  func(*testing.T, *store.DB)
		message string
	}{
		{name: "mapping", message: "mapping digest", damage: func(t *testing.T, db *store.DB) {
			if _, err := db.Exec(`DELETE FROM blackboard_legacy_mappings WHERE id=(SELECT MIN(id) FROM blackboard_legacy_mappings)`); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "guard", message: "write guard", damage: func(t *testing.T, db *store.DB) {
			if _, err := db.Exec(`DROP TRIGGER blackboard_legacy_project_facts_insert_guard`); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "parity", message: "parity", damage: func(t *testing.T, db *store.DB) {
			if _, err := db.Exec(`DROP TRIGGER blackboard_legacy_project_facts_update_guard`); err != nil {
				t.Fatal(err)
			}
			if _, err := db.Exec(`UPDATE project_facts SET summary='tampered parity' WHERE id='fact-corruption'`); err != nil {
				t.Fatal(err)
			}
			if _, err := db.Exec(`CREATE TRIGGER blackboard_legacy_project_facts_update_guard BEFORE UPDATE ON project_facts BEGIN SELECT RAISE(ABORT,'project_facts is frozen after graph_v1 cutover'); END`); err != nil {
				t.Fatal(err)
			}
		}},
	} {
		test := test
		t.Run(test.name, func(t *testing.T) {
			db, service := newInspectionService(t)
			insertProjectAndFacts(t, db, []string{"fact-corruption"})
			if _, err := service.Execute(context.Background(), MigrationRequest{Kind: MigrationKindCutover, BackupPath: filepath.Join(t.TempDir(), "legacy.bak")}); err != nil {
				t.Fatal(err)
			}
			test.damage(t, db)
			if _, err := service.Execute(context.Background(), MigrationRequest{Kind: MigrationKindVerify}); !errors.Is(err, ErrCutoverVerificationFailed) || !strings.Contains(err.Error(), test.message) {
				t.Fatalf("Execute(verify) error = %v, want %q corruption", err, test.message)
			}
		})
	}
}

func TestCutoverInitializesEmptyProjectsAndVerifiesRevisionZero(t *testing.T) {
	t.Parallel()

	db, service := newInspectionService(t)
	if _, err := db.Exec(`INSERT INTO projects (id,name,description,scope_json,defaults_json,kind,created_at,updated_at) VALUES ('project-empty','Empty','', '{}','{}','pentest','2026-01-01T00:00:00Z','2026-01-01T00:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	result, err := service.Execute(context.Background(), MigrationRequest{Kind: MigrationKindCutover, BackupPath: filepath.Join(t.TempDir(), "legacy.bak")})
	if err != nil {
		t.Fatalf("Execute(cutover): %v", err)
	}
	if result.Verification == nil || result.Verification.ProjectHashes["project-empty"] == "" {
		t.Fatalf("empty Project verification = %#v", result.Verification)
	}
	projection, err := blackboard.NewGraphService(db, nil, nil).CanonicalMainGraph(context.Background(), "project-empty", 0)
	if err != nil {
		t.Fatal(err)
	}
	if projection.GraphRevision != 0 || projection.Hash != result.Verification.ProjectHashes["project-empty"] {
		t.Fatalf("empty Project projection = %#v", projection)
	}
}

var cutoverGuardTables = []string{
	"project_facts",
	"project_fact_versions",
	"project_fact_relations",
	"fact_key_aliases",
	"findings",
	"finding_versions",
	"finding_key_aliases",
	"evidence_artifacts",
}

func legacyWriteStatements(table string) []string {
	return []string{
		`INSERT INTO ` + table + ` SELECT * FROM ` + table + ` LIMIT 1`,
		`UPDATE ` + table + ` SET rowid=rowid WHERE rowid=(SELECT MIN(rowid) FROM ` + table + `)`,
		`DELETE FROM ` + table + ` WHERE rowid=(SELECT MIN(rowid) FROM ` + table + `)`,
	}
}

func insertCutoverGuardFixture(t *testing.T, db *store.DB) {
	t.Helper()
	statements := []string{
		`INSERT INTO projects (id,name,description,scope_json,defaults_json,kind,created_at,updated_at) VALUES ('project-1','Example','', '{}','{}','pentest','2026-01-01T00:00:00Z','2026-01-01T00:00:00Z')`,
		`INSERT INTO project_facts (id,project_id,fact_key,category,summary,body,confidence,scope_status,created_at,updated_at) VALUES ('fact-cutover','project-1','key:fact-cutover','asset','Cutover fact','body','tentative','in_scope','2026-01-01T00:00:00Z','2026-01-01T00:00:00Z')`,
		`INSERT INTO project_facts (id,project_id,fact_key,category,summary,body,confidence,scope_status,created_at,updated_at) VALUES ('fact-cutover-target','project-1','key:fact-cutover-target','asset','Cutover target','body','tentative','in_scope','2026-01-01T00:00:00Z','2026-01-01T00:00:00Z')`,
		`INSERT INTO project_fact_versions (id,project_id,fact_key,version,category,summary,body,confidence,scope_status,created_at) VALUES ('fact-cutover-v1','project-1','key:fact-cutover',1,'asset','Cutover fact','body','tentative','in_scope','2026-01-01T00:00:00Z')`,
		`INSERT INTO project_fact_relations (id,project_id,source_fact_key,target_fact_key,relation,summary,created_at,updated_at) VALUES ('relation-cutover','project-1','key:fact-cutover','key:fact-cutover-target','supports','legacy relation','2026-01-01T00:00:00Z','2026-01-01T00:00:00Z')`,
		`INSERT INTO fact_key_aliases (id,project_id,alias_fact_key,canon_fact_key,created_at) VALUES ('fact-alias-cutover','project-1','key:old-cutover','key:fact-cutover','2026-01-01T00:00:00Z')`,
		`INSERT INTO findings (id,project_id,finding_key,version,title,description,status,target,proof,impact,recommendation,cvss_version,cvss_vector,cvss_pending,severity,created_at,updated_at) VALUES ('finding-cutover','project-1','finding:cutover',1,'Cutover finding','','unconfirmed','','','','','','',1,'pending','2026-01-01T00:00:00Z','2026-01-01T00:00:00Z')`,
		`INSERT INTO finding_versions (id,project_id,finding_key,version,title,description,status,target,proof,impact,recommendation,cvss_version,cvss_vector,cvss_pending,severity,created_at) VALUES ('finding-cutover-v1','project-1','finding:cutover',1,'Cutover finding','','unconfirmed','','','','','','',1,'pending','2026-01-01T00:00:00Z')`,
		`INSERT INTO finding_key_aliases (id,project_id,alias_finding_key,canon_finding_key,created_at) VALUES ('finding-alias-cutover','project-1','finding:old-cutover','finding:cutover','2026-01-01T00:00:00Z')`,
		`INSERT INTO evidence_artifacts (id,project_id,evidence_key,attach_to_type,attach_to_key,artifact_type,source_path,managed_path,sha256,summary,created_at,updated_at) VALUES ('evidence-cutover','project-1','evidence:cutover','finding','finding:cutover','log','','artifacts/missing.log','','Missing evidence','2026-01-01T00:00:00Z','2026-01-01T00:00:00Z')`,
	}
	for _, statement := range statements {
		if _, err := db.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
}

func TestLegacyFindingEvidenceCorpusPreservesHistoryAttachmentsAndMissingArtifactsWithoutSyntheticCounts(t *testing.T) {
	t.Parallel()

	db, service := newInspectionService(t)
	managedPath := filepath.Join("artifacts", "captures", "response.txt")
	managedFile := filepath.Join(service.artifactRoot, managedPath)
	if err := os.MkdirAll(filepath.Dir(managedFile), 0o700); err != nil {
		t.Fatal(err)
	}
	const managedContent = "HTTP/1.1 500 Internal Server Error\n"
	if err := os.WriteFile(managedFile, []byte(managedContent), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(service.artifactRoot, "..", "outside.txt"), []byte("must never be opened"), 0o600); err != nil {
		t.Fatal(err)
	}
	actualDigest := fmt.Sprintf("%x", sha256.Sum256([]byte(managedContent)))

	statements := []string{
		`INSERT INTO projects (id,name,description,scope_json,defaults_json,kind,created_at,updated_at) VALUES ('project-m03','Legacy','', '{}','{}','pentest','2026-01-01T00:00:00Z','2026-01-01T00:00:00Z')`,
		`INSERT INTO project_facts (id,project_id,fact_key,category,summary,body,confidence,scope_status,created_at,updated_at) VALUES ('fact-target','project-m03','fact:target','asset','Target service','','tentative','in_scope','2026-01-01T00:00:00Z','2026-01-01T00:00:00Z')`,
		`INSERT INTO findings (id,project_id,finding_key,version,title,description,status,target,proof,impact,recommendation,cvss_version,cvss_vector,cvss_pending,severity,created_at,updated_at) VALUES ('finding-main','project-m03','finding:sqli',2,'SQL injection','Legacy description','confirmed','https://example.test/login','SQL error','Account takeover','Use parameters','','CVSS:3.1/AV:N/AC:L/PR:N/UI:N/S:U/C:H/I:H/A:H',1,'pending','2026-01-01T00:00:00Z','2026-01-03T00:00:00Z')`,
		`INSERT INTO finding_versions (id,project_id,finding_key,version,title,description,status,target,proof,impact,recommendation,cvss_version,cvss_vector,cvss_pending,severity,created_at) VALUES ('finding-main-v1','project-m03','finding:sqli',1,'SQL injection','Legacy description','unconfirmed','https://example.test/login','SQL error','Account takeover','Use parameters','','',1,'pending','2026-01-01T00:00:00Z')`,
		`INSERT INTO finding_versions (id,project_id,finding_key,version,title,description,status,target,proof,impact,recommendation,cvss_version,cvss_vector,cvss_pending,severity,created_at) VALUES ('finding-main-v2','project-m03','finding:sqli',2,'SQL injection','Legacy description','confirmed','https://example.test/login','SQL error','Account takeover','Use parameters','','CVSS:3.1/AV:N/AC:L/PR:N/UI:N/S:U/C:H/I:H/A:H',0,'low','2026-01-03T00:00:00Z')`,
		`INSERT INTO findings (id,project_id,finding_key,version,title,description,status,target,proof,impact,recommendation,cvss_version,cvss_vector,cvss_pending,severity,created_at,updated_at) VALUES ('finding-fp','project-m03','finding:false-alarm',1,'False alarm','','false_positive','','','','','','',1,'pending','2026-01-02T00:00:00Z','2026-01-02T00:00:00Z')`,
		`INSERT INTO findings (id,project_id,finding_key,version,title,description,status,target,proof,impact,recommendation,cvss_version,cvss_vector,cvss_pending,severity,created_at,updated_at) VALUES ('finding-unsupported','project-m03','finding:unsupported',1,'Unsupported legacy confirmation','','confirmed','https://example.test/admin','Administrative access','Full compromise','Restrict access','3.1','CVSS:3.1/AV:N/AC:L/PR:N/UI:N/S:U/C:H/I:H/A:H',0,'critical','2026-01-02T00:00:00Z','2026-01-02T00:00:00Z')`,
		`INSERT INTO finding_key_aliases (id,project_id,alias_finding_key,canon_finding_key,created_at) VALUES ('finding-alias','project-m03','finding:old-sqli','finding:sqli','2026-01-04T00:00:00Z')`,
		`INSERT INTO evidence_artifacts (id,project_id,evidence_key,attach_to_type,attach_to_key,artifact_type,source_path,managed_path,sha256,summary,created_at,updated_at) VALUES ('evidence-live','project-m03','evidence:response','finding','finding:old-sqli','http_exchange','captures/raw.txt','` + filepath.ToSlash(managedPath) + `','deadbeef','Captured response','2026-01-04T00:00:00Z','2026-01-04T00:00:00Z')`,
		`INSERT INTO evidence_artifacts (id,project_id,evidence_key,attach_to_type,attach_to_key,artifact_type,source_path,managed_path,sha256,summary,created_at,updated_at) VALUES ('evidence-missing','project-m03','evidence:missing','fact','fact:missing','custom_trace','','artifacts/missing.trace','','Missing trace','2026-01-05T00:00:00Z','2026-01-05T00:00:00Z')`,
		`INSERT INTO evidence_artifacts (id,project_id,evidence_key,attach_to_type,attach_to_key,artifact_type,source_path,managed_path,sha256,summary,created_at,updated_at) VALUES ('evidence-escape','project-m03','evidence:escape','fact','fact:target','file','','../outside.txt','cafebabe','Escaping path','2026-01-06T00:00:00Z','2026-01-06T00:00:00Z')`,
	}
	for _, statement := range statements {
		if _, err := db.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}

	service = NewService(db, service.databasePath, service.artifactRoot, withDisposableImportCommitForTesting())
	result, err := service.Execute(context.Background(), MigrationRequest{Kind: MigrationKindCutover})
	if !errors.Is(err, ErrCutoverImplementationPending) {
		t.Fatalf("Execute(cutover): %v", err)
	}
	if result.Import == nil || result.Import.MappingDigest == "" || !result.Import.MappingVerified || result.Import.ParityDigest == "" || result.Import.ParityChecks["dashboard"] != 1 || result.Import.ParityChecks["report"] != 1 {
		t.Fatalf("import result = %#v", result.Import)
	}

	var facts, findings, evidence int
	for query, destination := range map[string]*int{
		`SELECT COUNT(*) FROM blackboard_node_heads WHERE project_id='project-m03' AND node_type='project_fact'`:      &facts,
		`SELECT COUNT(*) FROM blackboard_node_heads WHERE project_id='project-m03' AND node_type='finding'`:           &findings,
		`SELECT COUNT(*) FROM blackboard_node_heads WHERE project_id='project-m03' AND node_type='evidence_artifact'`: &evidence,
	} {
		if err := db.QueryRow(query).Scan(destination); err != nil {
			t.Fatal(err)
		}
	}
	if facts != 1 || findings != 3 || evidence != 3 {
		t.Fatalf("semantic counts facts=%d findings=%d evidence=%d", facts, findings, evidence)
	}

	reads := blackboard.NewBlackboardReadService(db)
	findingEnvelope, err := reads.Read(context.Background(), blackboard.ReadRequest{ProtocolVersion: 1, ProjectID: "project-m03", Kind: blackboard.ReadKindLegacyFindingCollectionV1, LegacyFindingCollection: &blackboard.LegacyFindingCollectionRequest{}})
	if err != nil {
		t.Fatal(err)
	}
	legacyFindings := findingEnvelope.Result.(blackboard.LegacyFindingCollectionV1)
	if len(legacyFindings.Findings) != 3 || legacyFindings.Findings[0].Severity == "low" {
		t.Fatalf("legacy Findings = %#v", legacyFindings.Findings)
	}
	versionsEnvelope, err := reads.Read(context.Background(), blackboard.ReadRequest{ProtocolVersion: 1, ProjectID: "project-m03", Kind: blackboard.ReadKindLegacyFindingVersionsV1, LegacyFindingVersions: &blackboard.LegacyFindingVersionsRequest{FindingKey: "finding:old-sqli"}})
	if err != nil {
		t.Fatal(err)
	}
	versions := versionsEnvelope.Result.(blackboard.LegacyFindingVersionsV1)
	if len(versions.Versions) != 2 || versions.Versions[0].Status != "unconfirmed" || versions.Versions[1].Status != "confirmed" || versions.Versions[1].CVSSVersion != "3.1" {
		t.Fatalf("legacy Finding versions = %#v", versions.Versions)
	}

	evidenceEnvelope, err := reads.Read(context.Background(), blackboard.ReadRequest{ProtocolVersion: 1, ProjectID: "project-m03", Kind: blackboard.ReadKindLegacyEvidenceCollectionV1, LegacyEvidenceCollection: &blackboard.LegacyEvidenceCollectionRequest{}})
	if err != nil {
		t.Fatal(err)
	}
	legacyEvidence := evidenceEnvelope.Result.(blackboard.LegacyEvidenceCollectionV1)
	byKey := make(map[string]blackboard.LegacyEvidenceArtifactV1, len(legacyEvidence.Evidence))
	for _, artifact := range legacyEvidence.Evidence {
		byKey[artifact.EvidenceKey] = artifact
	}
	live := byKey["evidence:response"]
	if live.SHA256 != actualDigest || live.AttachToType != "finding" || live.AttachToKey != "finding:sqli" || len(live.Attachments) != 1 {
		t.Fatalf("available Evidence = %#v", live)
	}
	missing := byKey["evidence:missing"]
	if missing.ArtifactType != "other" || missing.ManagedPath != "artifacts/missing.trace" || missing.AttachToType != "fact" || missing.AttachToKey != "fact:missing" || len(missing.Attachments) != 0 {
		t.Fatalf("missing Evidence = %#v", missing)
	}
	escaping := byKey["evidence:escape"]
	if !strings.HasPrefix(escaping.ManagedPath, "missing://") || len(escaping.Attachments) != 1 || escaping.SHA256 != "" {
		t.Fatalf("escaping Evidence = %#v", escaping)
	}

	health, err := blackboard.NewGraphService(db, nil, nil).RunHealth(context.Background(), "project-m03")
	if err != nil {
		t.Fatal(err)
	}
	warnings := map[string]bool{}
	for _, item := range health.Results {
		warnings[item.Code] = true
	}
	if !warnings["legacy_confirmed_finding_without_support"] || !warnings["legacy_evidence_digest_mismatch"] {
		t.Fatalf("Health results = %#v", health.Results)
	}
	graph := blackboard.NewGraphService(db, nil, nil)
	_, err = graph.Apply(context.Background(), blackboard.MutationBatch{
		SchemaVersion: blackboard.GraphMutationSchemaVersion, IdempotencyKey: "later-native-attachment",
		Context:    blackboard.SystemExecutionContext("project-m03", "pentest", "test"),
		Operations: []blackboard.Operation{{OpID: "edge", Kind: blackboard.OpPutEdge, PutEdge: blackboard.PutEdgeInput{EdgeType: blackboard.EdgeTypeEvidences, From: blackboard.NodeRef{NodeType: blackboard.NodeTypeEvidenceArtifact, StableKey: "evidence:missing"}, To: blackboard.NodeRef{NodeType: blackboard.NodeTypeProjectFact, StableKey: "fact:target"}}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	evidenceEnvelope, err = reads.Read(context.Background(), blackboard.ReadRequest{ProtocolVersion: 1, ProjectID: "project-m03", Kind: blackboard.ReadKindLegacyEvidenceCollectionV1, LegacyEvidenceCollection: &blackboard.LegacyEvidenceCollectionRequest{}})
	if err != nil {
		t.Fatal(err)
	}
	for _, artifact := range evidenceEnvelope.Result.(blackboard.LegacyEvidenceCollectionV1).Evidence {
		if artifact.EvidenceKey == "evidence:missing" && (artifact.AttachToType != "fact" || artifact.AttachToKey != "fact:missing" || len(artifact.Attachments) != 1) {
			t.Fatalf("later attachment replaced migrated preference: %#v", artifact)
		}
	}
	_, err = graph.Apply(context.Background(), blackboard.MutationBatch{
		SchemaVersion: blackboard.GraphMutationSchemaVersion, IdempotencyKey: "nonqualifying-support",
		Context: blackboard.SystemExecutionContext("project-m03", "pentest", "test"),
		Operations: []blackboard.Operation{
			{OpID: "fact", Kind: blackboard.OpCreateNode, Node: blackboard.NodeRef{NodeType: blackboard.NodeTypeProjectFact, StableKey: "fact:tentative-support"}, Create: blackboard.CreateNodeInput{PropertyMap: map[string]any{"category": "test", "summary": "Tentative support", "body": "", "confidence": "tentative", "scope_status": "in_scope"}}},
			{OpID: "edge", Kind: blackboard.OpPutEdge, PutEdge: blackboard.PutEdgeInput{EdgeType: blackboard.EdgeTypeSupports, From: blackboard.NodeRef{OpID: "fact"}, To: blackboard.NodeRef{NodeType: blackboard.NodeTypeFinding, StableKey: "finding:unsupported"}}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	health, err = graph.RunHealth(context.Background(), "project-m03")
	if err != nil {
		t.Fatal(err)
	}
	stillWarns := false
	for _, item := range health.Results {
		stillWarns = stillWarns || item.Code == "legacy_confirmed_finding_without_support"
	}
	if !stillWarns {
		t.Fatalf("nonqualifying support cleared migration warning: %#v", health.Results)
	}
	_, err = blackboard.NewGraphService(db, nil, nil).Apply(context.Background(), blackboard.MutationBatch{
		SchemaVersion: blackboard.GraphMutationSchemaVersion, IdempotencyKey: "ordinary-unsupported-finding",
		Context: blackboard.SystemExecutionContext("project-m03", "pentest", "test"),
		Operations: []blackboard.Operation{{OpID: "finding", Kind: blackboard.OpCreateNode, Node: blackboard.NodeRef{NodeType: blackboard.NodeTypeFinding, StableKey: "finding:new-unsupported"}, Create: blackboard.CreateNodeInput{PropertyMap: map[string]any{
			"title": "New unsupported", "status": "confirmed", "target": "https://example.test", "proof": "proof", "impact": "impact", "recommendation": "fix",
			"cvss_version": "3.1", "cvss_vector": "CVSS:3.1/AV:N/AC:L/PR:N/UI:N/S:U/C:H/I:H/A:H",
		}}}},
	})
	var validation *blackboard.ValidationError
	if !errors.As(err, &validation) || validation.Code != blackboard.ErrCodeTransitionGuardFailed {
		t.Fatalf("ordinary unsupported Finding error = %v", err)
	}
}

func TestHistoryOnlyFindingRemainsAddressableWithoutChangingCurrentCounts(t *testing.T) {
	t.Parallel()

	db, service := newInspectionService(t)
	statements := []string{
		`INSERT INTO projects (id,name,description,scope_json,defaults_json,kind,created_at,updated_at) VALUES ('project-history-only','Legacy','', '{}','{}','pentest','2026-01-01T00:00:00Z','2026-01-01T00:00:00Z')`,
		`INSERT INTO finding_versions (id,project_id,finding_key,version,title,description,status,target,proof,impact,recommendation,cvss_version,cvss_vector,cvss_pending,severity,created_at) VALUES ('finding-old-v1','project-history-only','finding:old',1,'Historical Finding','','false_positive','','','','','','',1,'pending','2026-01-01T00:00:00Z')`,
	}
	for _, statement := range statements {
		if _, err := db.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
	service = NewService(db, service.databasePath, service.artifactRoot, withDisposableImportCommitForTesting())
	if _, err := service.Execute(context.Background(), MigrationRequest{Kind: MigrationKindCutover}); !errors.Is(err, ErrCutoverImplementationPending) {
		t.Fatalf("Execute(cutover): %v", err)
	}
	var main, archived int
	if err := db.QueryRow(`SELECT COUNT(*) FROM blackboard_node_heads WHERE project_id='project-history-only' AND node_type='finding' AND disposition='main'`).Scan(&main); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM blackboard_node_heads WHERE project_id='project-history-only' AND node_type='finding' AND disposition='archived'`).Scan(&archived); err != nil {
		t.Fatal(err)
	}
	if main != 0 || archived != 1 {
		t.Fatalf("Finding dispositions main=%d archived=%d", main, archived)
	}
	envelope, err := blackboard.NewBlackboardReadService(db).Read(context.Background(), blackboard.ReadRequest{ProtocolVersion: 1, ProjectID: "project-history-only", Kind: blackboard.ReadKindLegacyFindingVersionsV1, LegacyFindingVersions: &blackboard.LegacyFindingVersionsRequest{FindingKey: "finding:old"}})
	if err != nil {
		t.Fatal(err)
	}
	versions := envelope.Result.(blackboard.LegacyFindingVersionsV1)
	if len(versions.Versions) != 1 || versions.Versions[0].Title != "Historical Finding" {
		t.Fatalf("historical versions = %#v", versions.Versions)
	}
}

func TestParityGatesFollowEveryLegacyFindingAndEvidencePage(t *testing.T) {
	t.Parallel()

	db, service := newInspectionService(t)
	if _, err := db.Exec(`INSERT INTO projects (id,name,description,scope_json,defaults_json,kind,created_at,updated_at) VALUES ('project-pages','Legacy','', '{}','{}','pentest','2026-01-01T00:00:00Z','2026-01-01T00:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	for index := 0; index < 201; index++ {
		key := fmt.Sprintf("finding:page-%03d", index)
		id := fmt.Sprintf("finding-page-%03d", index)
		if _, err := db.Exec(`INSERT INTO findings (id,project_id,finding_key,version,title,description,status,target,proof,impact,recommendation,cvss_version,cvss_vector,cvss_pending,severity,created_at,updated_at) VALUES (?,?,?,?,?,'','unconfirmed','','','','','','',1,'pending','2026-01-01T00:00:00Z','2026-01-01T00:00:00Z')`, id, "project-pages", key, 1, key); err != nil {
			t.Fatal(err)
		}
		evidenceKey := fmt.Sprintf("evidence:page-%03d", index)
		evidenceID := fmt.Sprintf("evidence-page-%03d", index)
		if _, err := db.Exec(`INSERT INTO evidence_artifacts (id,project_id,evidence_key,attach_to_type,attach_to_key,artifact_type,source_path,managed_path,sha256,summary,created_at,updated_at) VALUES (?, ?, ?, 'fact', 'fact:missing', 'log', '', ?, '', ?, '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z')`, evidenceID, "project-pages", evidenceKey, "artifacts/"+evidenceID+".log", evidenceKey); err != nil {
			t.Fatal(err)
		}
	}
	service = NewService(db, service.databasePath, service.artifactRoot, withDisposableImportCommitForTesting())
	result, err := service.Execute(context.Background(), MigrationRequest{Kind: MigrationKindCutover})
	if !errors.Is(err, ErrCutoverImplementationPending) {
		t.Fatalf("Execute(cutover): %v", err)
	}
	if result.Import == nil || result.Import.ParityChecks["legacy_findings"] != 2 || result.Import.ParityChecks["legacy_evidence"] != 2 {
		t.Fatalf("paged parity result = %#v", result.Import)
	}
}

func TestLegacyFactCorpusImportsDeterministicallyWithExactGoalAndCompatibilityParity(t *testing.T) {
	t.Parallel()

	db, service := newInspectionService(t)
	insertLegacyM02Corpus(t, db)
	service = NewService(db, service.databasePath, service.artifactRoot, withDisposableImportCommitForTesting())

	result, err := service.Execute(context.Background(), MigrationRequest{Kind: MigrationKindCutover})
	if !errors.Is(err, ErrCutoverImplementationPending) {
		t.Fatalf("Execute(cutover) error = %v, want incremental cutover sentinel", err)
	}
	if result.Import == nil || result.Import.MappingDigest == "" {
		t.Fatalf("Execute(cutover) import result = %#v, want deterministic mapping digest", result.Import)
	}
	var migrationKey string
	if err := db.QueryRow(`SELECT idempotency_key FROM blackboard_graph_mutations WHERE project_id='project-m02'`).Scan(&migrationKey); err != nil {
		t.Fatal(err)
	}
	wantMigrationKey := "legacy-blackboard-v1:" + result.Plan.SourceDigest + ":project-m02"
	if migrationKey != wantMigrationKey {
		t.Fatalf("migration idempotency key = %q, want %q", migrationKey, wantMigrationKey)
	}

	var projectKind string
	if err := db.QueryRow(`SELECT kind FROM projects WHERE id='project-m02'`).Scan(&projectKind); err != nil {
		t.Fatal(err)
	}
	if projectKind != "pentest" {
		t.Fatalf("legacy Project kind = %q, want immutable pentest", projectKind)
	}

	graph := blackboard.NewGraphService(db, nil, nil)
	if err := graph.VerifyIntegrity(context.Background(), "project-m02"); err != nil {
		t.Fatalf("verify imported graph ledger: %v", err)
	}
	goal, err := graph.ReadNode(context.Background(), blackboard.ReadNodeRequest{
		ProjectID: "project-m02", NodeType: blackboard.NodeTypeGoal, Key: "task:task-m02:goal",
	})
	if err != nil {
		t.Fatalf("read imported Task Goal: %v", err)
	}
	if goal.Node.PropertyMap["text"] != "Map the CTF-looking perimeter" || goal.Node.PropertyMap["task_status"] != "completed" {
		t.Fatalf("imported Task Goal = %#v", goal.Node.PropertyMap)
	}

	detail := readLegacyFact[blackboard.LegacyFactDetailV1](t, db, blackboard.ReadKindLegacyFactDetailV1,
		blackboard.LegacyFactDetailRequest{FactKey: "Host Web"})
	if detail.ID != "fact-m02" || detail.Version != 2 || detail.Summary != "Current web host" || detail.Category != "uncategorized" || detail.ScopeStatus != "unknown" {
		t.Fatalf("legacy Fact detail parity = %#v", detail)
	}
	if detail.ResolvedFromAlias == nil || *detail.ResolvedFromAlias != "Host Web" {
		t.Fatalf("nonconforming legacy key did not resolve as an alias: %#v", detail.ResolvedFromAlias)
	}

	versions := readLegacyFact[blackboard.LegacyFactVersionsV1](t, db, blackboard.ReadKindLegacyFactVersionsV1,
		blackboard.LegacyFactVersionsRequest{FactKey: "Host Web"})
	if len(versions.Versions) != 2 || versions.Versions[0].Summary != "Historical web host" || versions.Versions[1].Summary != "Current web host" {
		t.Fatalf("legacy Fact version parity = %#v", versions.Versions)
	}

	relations := readLegacyFact[blackboard.LegacyFactRelationsV1](t, db, blackboard.ReadKindLegacyFactRelationsV1,
		blackboard.LegacyFactRelationsRequest{FactKey: "Host Web"})
	if len(relations.Relations) != 2 || relations.Relations[0].Relation != "leads_to" || relations.Relations[1].Relation != "depends_on" {
		t.Fatalf("legacy Fact relation parity = %#v", relations.Relations)
	}

	firstDigest := result.Import.MappingDigest
	secondDB, secondService := newInspectionService(t)
	insertLegacyM02Corpus(t, secondDB)
	secondService = NewService(secondDB, secondService.databasePath, secondService.artifactRoot, withDisposableImportCommitForTesting())
	second, secondErr := secondService.Execute(context.Background(), MigrationRequest{Kind: MigrationKindCutover})
	if !errors.Is(secondErr, ErrCutoverImplementationPending) {
		t.Fatalf("second Execute(cutover): %v", secondErr)
	}
	if second.Import == nil || second.Import.MappingDigest != firstDigest {
		t.Fatalf("mapping digest is not deterministic: first=%q second=%#v", firstDigest, second.Import)
	}
}

func TestLegacyContinuationBackfillUsesOnlyOneExactRuntimeConfigurationMatch(t *testing.T) {
	t.Parallel()

	db, service := newInspectionService(t)
	statements := []string{
		`INSERT INTO projects (id,name,description,scope_json,defaults_json,kind,created_at,updated_at) VALUES ('project-cont','Legacy','', '{}','{}','pentest','2026-01-01T00:00:00Z','2026-01-01T00:00:00Z')`,
		`INSERT INTO tasks (id,project_id,goal,status,runner,runtime_profile_id,run_controls_json,scope_snapshot_json,created_at,updated_at) VALUES ('task-cont','project-cont','Preserve provenance','completed','local','profile-a','{}','{}','2026-01-01T00:00:00Z','2026-01-03T00:00:00Z')`,
		`INSERT INTO task_runtime_config_versions (id,task_id,version,runtime_profile_id,config_json,created_at) VALUES ('config-old','task-cont',1,'profile-a','{}','2026-01-01T00:00:00Z')`,
		`INSERT INTO task_runtime_config_versions (id,task_id,version,runtime_profile_id,config_json,created_at) VALUES ('config-exact','task-cont',2,'profile-a','{}','2026-01-02T00:00:00Z')`,
		`INSERT INTO task_continuations (id,task_id,number,runtime_profile_id,runtime_provider,runner,status,started_at,updated_at,ended_at) VALUES ('continuation-exact','task-cont',1,'profile-a','manual','local','completed','2026-01-02T12:00:00Z','2026-01-03T00:00:00Z','2026-01-03T00:00:00Z')`,
		`INSERT INTO task_continuations (id,task_id,number,runtime_profile_id,runtime_provider,runner,status,started_at,updated_at,ended_at) VALUES ('continuation-missing','task-cont',2,'profile-missing','manual','local','completed','2026-01-02T12:00:00Z','2026-01-03T00:00:00Z','2026-01-03T00:00:00Z')`,
		`INSERT INTO task_events (id,task_id,seq,kind,payload_json,created_at) VALUES ('event-legacy','task-cont',1,'output','{}','2026-01-02T13:00:00Z')`,
		`INSERT INTO task_summary_versions (id,task_id,version,summary,submitted_by,created_at) VALUES ('summary-legacy','task-cont',1,'legacy summary','operator','2026-01-03T00:00:00Z')`,
	}
	for _, statement := range statements {
		if _, err := db.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
	service = NewService(db, service.databasePath, service.artifactRoot, withDisposableImportCommitForTesting())
	if _, err := service.Execute(context.Background(), MigrationRequest{Kind: MigrationKindCutover}); !errors.Is(err, ErrCutoverImplementationPending) {
		t.Fatalf("Execute(cutover): %v", err)
	}

	var exact sql.NullString
	var reconciliation string
	if err := db.QueryRow(`SELECT runtime_config_version_id,blackboard_reconciliation_status FROM task_continuations WHERE id='continuation-exact'`).Scan(&exact, &reconciliation); err != nil {
		t.Fatal(err)
	}
	if !exact.Valid || exact.String != "config-exact" || reconciliation != "legacy_not_applicable" {
		t.Fatalf("exact Continuation backfill = (%#v,%q)", exact, reconciliation)
	}
	var missing, eventContinuation, eventAttempt, summaryContinuation sql.NullString
	if err := db.QueryRow(`SELECT runtime_config_version_id FROM task_continuations WHERE id='continuation-missing'`).Scan(&missing); err != nil {
		t.Fatal(err)
	}
	if missing.Valid {
		t.Fatalf("missing Runtime Configuration was guessed as %q", missing.String)
	}
	if err := db.QueryRow(`SELECT continuation_id,attempt_node_id FROM task_events WHERE id='event-legacy'`).Scan(&eventContinuation, &eventAttempt); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT continuation_id FROM task_summary_versions WHERE id='summary-legacy'`).Scan(&summaryContinuation); err != nil {
		t.Fatal(err)
	}
	if eventContinuation.Valid || eventAttempt.Valid || summaryContinuation.Valid {
		t.Fatalf("legacy Event/Summary associations were inferred: event_cont=%#v attempt=%#v summary_cont=%#v", eventContinuation, eventAttempt, summaryContinuation)
	}
}

func TestLegacyConfirmedFactImportsWithMigrationWarningWhileOrdinaryConfirmationStillFails(t *testing.T) {
	t.Parallel()

	db, service := newInspectionService(t)
	if _, err := db.Exec(`INSERT INTO projects (id,name,description,scope_json,defaults_json,kind,created_at,updated_at) VALUES ('project-confirmed','Legacy','', '{}','{}','pentest','2026-01-01T00:00:00Z','2026-01-01T00:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO project_facts (id,project_id,fact_key,category,summary,body,confidence,scope_status,created_at,updated_at) VALUES ('fact-confirmed','project-confirmed','fact:confirmed','asset','Legacy confirmed','','confirmed','in_scope','2026-01-01T00:00:00Z','2026-01-01T00:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	service = NewService(db, service.databasePath, service.artifactRoot, withDisposableImportCommitForTesting())
	if _, err := service.Execute(context.Background(), MigrationRequest{Kind: MigrationKindCutover}); !errors.Is(err, ErrCutoverImplementationPending) {
		t.Fatalf("Execute(cutover): %v", err)
	}
	graph := blackboard.NewGraphService(db, nil, nil)
	health, err := graph.RunHealth(context.Background(), "project-confirmed")
	if err != nil {
		t.Fatal(err)
	}
	foundWarning := false
	for _, result := range health.Results {
		foundWarning = foundWarning || result.Code == "legacy_confirmed_fact_without_basis"
	}
	if !foundWarning {
		t.Fatalf("Health results = %#v, want legacy confirmation warning", health.Results)
	}
	_, err = graph.Apply(context.Background(), blackboard.MutationBatch{
		SchemaVersion: blackboard.GraphMutationSchemaVersion, IdempotencyKey: "support-imported-confirmation",
		Context: blackboard.SystemExecutionContext("project-confirmed", "pentest", "test"),
		Operations: []blackboard.Operation{
			{OpID: "supporter", Kind: blackboard.OpCreateNode, Node: blackboard.NodeRef{NodeType: blackboard.NodeTypeProjectFact, StableKey: "fact:supporter"}, Create: blackboard.CreateNodeInput{PropertyMap: map[string]any{"category": "asset", "summary": "Supporting Fact", "body": "durable support", "confidence": "confirmed", "scope_status": "in_scope"}}},
			{OpID: "supports", Kind: blackboard.OpPutEdge, PutEdge: blackboard.PutEdgeInput{EdgeType: blackboard.EdgeTypeSupports, From: blackboard.NodeRef{OpID: "supporter"}, To: blackboard.NodeRef{NodeType: blackboard.NodeTypeProjectFact, StableKey: "fact:confirmed"}}},
		},
	})
	if err != nil {
		t.Fatalf("add normal support: %v", err)
	}
	health, err = graph.RunHealth(context.Background(), "project-confirmed")
	if err != nil {
		t.Fatal(err)
	}
	for _, result := range health.Results {
		if result.Code == "legacy_confirmed_fact_without_basis" {
			t.Fatalf("legacy confirmation warning remained after normal support: %#v", health.Results)
		}
	}
	_, err = graph.Apply(context.Background(), blackboard.MutationBatch{
		SchemaVersion: blackboard.GraphMutationSchemaVersion, IdempotencyKey: "ordinary-confirmation",
		Context:    blackboard.SystemExecutionContext("project-confirmed", "pentest", "test"),
		Operations: []blackboard.Operation{{OpID: "fact", Kind: blackboard.OpCreateNode, Node: blackboard.NodeRef{NodeType: blackboard.NodeTypeProjectFact, StableKey: "fact:new-confirmed"}, Create: blackboard.CreateNodeInput{PropertyMap: map[string]any{"category": "asset", "summary": "New confirmed", "body": "", "confidence": "confirmed", "scope_status": "in_scope"}}}},
	})
	var validation *blackboard.ValidationError
	if !errors.As(err, &validation) || validation.Code != blackboard.ErrCodeTransitionGuardFailed {
		t.Fatalf("ordinary unsupported confirmation error = %v", err)
	}
}

func TestLegacyAliasHistoryImportsAsMergedIdentityAndUnresolvableRelationsStayAuditOnly(t *testing.T) {
	t.Parallel()

	db, service := newInspectionService(t)
	statements := []string{
		`INSERT INTO projects (id,name,description,scope_json,defaults_json,kind,created_at,updated_at) VALUES ('project-alias','Legacy','', '{}','{}','pentest','2026-01-01T00:00:00Z','2026-01-01T00:00:00Z')`,
		`INSERT INTO project_facts (id,project_id,fact_key,category,summary,body,confidence,scope_status,created_at,updated_at) VALUES ('fact-canonical','project-alias','fact:canonical','asset','Canonical','','tentative','in_scope','2026-01-01T00:00:00Z','2026-01-02T00:00:00Z')`,
		`INSERT INTO project_fact_versions (id,project_id,fact_key,version,category,summary,body,confidence,scope_status,created_at) VALUES ('fact-canonical-v1','project-alias','fact:canonical',1,'asset','Canonical','','tentative','in_scope','2025-12-31T00:00:00Z')`,
		`INSERT INTO project_fact_versions (id,project_id,fact_key,version,category,summary,body,confidence,scope_status,created_at) VALUES ('fact-canonical-v2','project-alias','fact:canonical',2,'asset','Source history','','tentative','in_scope','2026-01-01T00:00:00Z')`,
		`INSERT INTO project_fact_versions (id,project_id,fact_key,version,category,summary,body,confidence,scope_status,created_at) VALUES ('fact-source-v1','project-alias','fact:source',1,'asset','Source history','','tentative','in_scope','2026-01-01T00:00:00Z')`,
		`INSERT INTO fact_key_aliases (id,project_id,alias_fact_key,canon_fact_key,created_at) VALUES ('alias-source','project-alias','fact:source','fact:canonical','2026-01-03T00:00:00Z')`,
		`INSERT INTO fact_key_aliases (id,project_id,alias_fact_key,canon_fact_key,created_at) VALUES ('alias-cycle-a','project-alias','cycle:a','cycle:b','2026-01-03T00:00:00Z')`,
		`INSERT INTO fact_key_aliases (id,project_id,alias_fact_key,canon_fact_key,created_at) VALUES ('alias-cycle-b','project-alias','cycle:b','cycle:a','2026-01-03T00:00:00Z')`,
		`INSERT INTO project_fact_relations (id,project_id,source_fact_key,target_fact_key,relation,summary,created_at,updated_at) VALUES ('relation-duplicates','project-alias','fact:canonical','fact:source','duplicates','advisory only','2026-01-04T00:00:00Z','2026-01-04T00:00:00Z')`,
		`INSERT INTO project_fact_relations (id,project_id,source_fact_key,target_fact_key,relation,summary,created_at,updated_at) VALUES ('relation-unknown','project-alias','fact:canonical','fact:source','nearby-to','audit only','2026-01-05T00:00:00Z','2026-01-05T00:00:00Z')`,
	}
	for _, statement := range statements {
		if _, err := db.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
	service = NewService(db, service.databasePath, service.artifactRoot, withDisposableImportCommitForTesting())
	if _, err := service.Execute(context.Background(), MigrationRequest{Kind: MigrationKindCutover}); !errors.Is(err, ErrCutoverImplementationPending) {
		t.Fatalf("Execute(cutover): %v", err)
	}

	graph := blackboard.NewGraphService(db, nil, nil)
	resolved, err := graph.ReadNode(context.Background(), blackboard.ReadNodeRequest{ProjectID: "project-alias", NodeType: blackboard.NodeTypeProjectFact, Key: "fact:source"})
	if err != nil {
		t.Fatal(err)
	}
	if resolved.Node.ID != "fact-canonical" || resolved.ResolvedFromAlias != "fact:source" {
		t.Fatalf("merged source resolution = %#v", resolved)
	}
	var sourceID string
	if err := db.QueryRow(`SELECT id FROM blackboard_nodes WHERE project_id='project-alias' AND original_stable_key='fact:source'`).Scan(&sourceID); err != nil {
		t.Fatal(err)
	}
	literal, err := graph.ReadLiteralNode(context.Background(), blackboard.ReadLiteralNodeRequest{ProjectID: "project-alias", NodeID: sourceID})
	if err != nil {
		t.Fatal(err)
	}
	if literal.Node.Disposition != blackboard.DispositionMerged || len(literal.Versions) != 2 || literal.Versions[0].PropertyMap["summary"] != "Source history" {
		t.Fatalf("literal merged source history = %#v", literal)
	}
	var auditAliases, auditRelations, activeDuplicateEdges int
	if err := db.QueryRow(`SELECT COUNT(*) FROM blackboard_legacy_mappings WHERE project_id='project-alias' AND mapping_status='unresolvable_legacy_alias'`).Scan(&auditAliases); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM blackboard_legacy_mappings WHERE project_id='project-alias' AND mapping_status='audit_only_relation'`).Scan(&auditRelations); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM blackboard_edge_heads WHERE project_id='project-alias' AND edge_type IN ('duplicates','depends_on')`).Scan(&activeDuplicateEdges); err != nil {
		t.Fatal(err)
	}
	if auditAliases != 2 || auditRelations != 2 || activeDuplicateEdges != 0 {
		t.Fatalf("audit-only preservation aliases=%d relations=%d active=%d", auditAliases, auditRelations, activeDuplicateEdges)
	}
	var rebadgedCopies int
	var unknownMetadata string
	if err := db.QueryRow(`SELECT COUNT(*) FROM blackboard_legacy_mappings WHERE project_id='project-alias' AND mapping_status='legacy_rebadged_copy'`).Scan(&rebadgedCopies); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT compatibility_metadata_json FROM blackboard_legacy_mappings WHERE legacy_primary_id='relation-unknown'`).Scan(&unknownMetadata); err != nil {
		t.Fatal(err)
	}
	if rebadgedCopies != 1 || !strings.Contains(unknownMetadata, `"relation":"nearby-to"`) {
		t.Fatalf("rebadged copies=%d unknown relation metadata=%s", rebadgedCopies, unknownMetadata)
	}
}

func readLegacyFact[V any](t *testing.T, db *store.DB, kind blackboard.ReadKind, request any) V {
	t.Helper()
	readRequest := blackboard.ReadRequest{ProtocolVersion: blackboard.BlackboardReadProtocolVersion, ProjectID: "project-m02", Kind: kind}
	switch value := request.(type) {
	case blackboard.LegacyFactDetailRequest:
		readRequest.LegacyFactDetail = &value
	case blackboard.LegacyFactVersionsRequest:
		readRequest.LegacyFactVersions = &value
	case blackboard.LegacyFactRelationsRequest:
		readRequest.LegacyFactRelations = &value
	default:
		t.Fatalf("unsupported legacy request %T", request)
	}
	envelope, err := blackboard.NewBlackboardReadService(db).Read(context.Background(), readRequest)
	if err != nil {
		t.Fatalf("Read(%s): %v", kind, err)
	}
	result, ok := envelope.Result.(V)
	if !ok {
		t.Fatalf("Read(%s) result type = %T", kind, envelope.Result)
	}
	return result
}

func insertLegacyM02Corpus(t *testing.T, db *store.DB) {
	t.Helper()
	statements := []string{
		`INSERT INTO projects (id,name,description,scope_json,defaults_json,kind,created_at,updated_at) VALUES ('project-m02','CTF Flag Lab','', '{}','{}','ctf_challenge','2026-01-01T00:00:00Z','2026-01-01T00:00:00Z')`,
		`INSERT INTO tasks (id,project_id,goal,status,runner,runtime_profile_id,run_controls_json,scope_snapshot_json,created_at,updated_at) VALUES ('task-m02','project-m02','Map the CTF-looking perimeter','completed','local','profile-m02','{}','{}','2026-01-01T00:00:00Z','2026-01-02T00:00:00Z')`,
		`INSERT INTO project_facts (id,project_id,fact_key,category,summary,body,confidence,scope_status,created_at,updated_at) VALUES ('fact-m02','project-m02','Host Web','','Current web host','current body','tentative','','2026-01-01T00:00:00Z','2026-01-03T00:00:00Z')`,
		`INSERT INTO project_fact_versions (id,project_id,fact_key,version,category,summary,body,confidence,scope_status,created_at) VALUES ('fact-m02-v1','project-m02','Host Web',1,'','Historical web host','old body','tentative','','2026-01-01T00:00:00Z')`,
		`INSERT INTO project_facts (id,project_id,fact_key,category,summary,body,confidence,scope_status,created_at,updated_at) VALUES ('fact-api','project-m02','service:api','service','API service','','tentative','in_scope','2026-01-01T00:00:00Z','2026-01-01T00:00:00Z')`,
		`INSERT INTO fact_key_aliases (id,project_id,alias_fact_key,canon_fact_key,created_at) VALUES ('alias-host','project-m02','old host','Host Web','2026-01-04T00:00:00Z')`,
		`INSERT INTO project_fact_relations (id,project_id,source_fact_key,target_fact_key,relation,summary,created_at,updated_at) VALUES ('relation-leads','project-m02','Host Web','service:api','leads-to','discovery path','2026-01-05T00:00:00Z','2026-01-05T00:00:00Z')`,
		`INSERT INTO project_fact_relations (id,project_id,source_fact_key,target_fact_key,relation,summary,created_at,updated_at) VALUES ('relation-depends','project-m02','Host Web','service:api','depends_on','legacy ordering only','2026-01-06T00:00:00Z','2026-01-06T00:00:00Z')`,
	}
	for _, statement := range statements {
		if _, err := db.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
}

func TestInspectIsDeterministicAndWritesNeitherDatabaseNorFilesystem(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	databasePath := filepath.Join(root, "pentest.db")
	artifactRoot := filepath.Join(root, "artifacts")
	if err := os.Mkdir(artifactRoot, 0o700); err != nil {
		t.Fatal(err)
	}

	db, err := store.Open(databasePath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	insertLegacyInspectionFixture(t, db)
	service := NewService(db, databasePath, artifactRoot)

	beforeChanges := totalChanges(t, db)
	beforeFiles := directoryEntries(t, root)

	first, err := service.Execute(context.Background(), MigrationRequest{Kind: MigrationKindInspect})
	if err != nil {
		t.Fatal(err)
	}
	second, err := service.Execute(context.Background(), MigrationRequest{Kind: MigrationKindInspect})
	if err != nil {
		t.Fatal(err)
	}

	if !reflect.DeepEqual(first, second) {
		firstJSON, _ := json.MarshalIndent(first, "", "  ")
		secondJSON, _ := json.MarshalIndent(second, "", "  ")
		t.Fatalf("inspection is not deterministic\nfirst: %s\nsecond: %s", firstJSON, secondJSON)
	}
	if first.Plan.SourceDigest == "" {
		t.Fatal("inspection returned an empty source digest")
	}
	if first.Plan.SourceCounts["project_facts"] != 1 || first.Plan.SourceCounts["findings"] != 1 {
		t.Fatalf("unexpected source counts: %#v", first.Plan.SourceCounts)
	}
	if got := totalChanges(t, db); got != beforeChanges {
		t.Fatalf("inspection wrote to the database: total_changes before=%d after=%d", beforeChanges, got)
	}
	if got := directoryEntries(t, root); !reflect.DeepEqual(got, beforeFiles) {
		t.Fatalf("inspection wrote to the filesystem: before=%v after=%v", beforeFiles, got)
	}
}

func TestInspectDigestIgnoresSQLRowOrderAndChangesWithAnySourceRow(t *testing.T) {
	t.Parallel()

	db, service := newInspectionService(t)
	insertProjectAndFacts(t, db, []string{"fact-a", "fact-b"})
	first, err := service.Execute(context.Background(), MigrationRequest{Kind: MigrationKindInspect})
	if err != nil {
		t.Fatal(err)
	}

	if _, err := db.Exec(`DELETE FROM project_facts`); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"fact-b", "fact-a"} {
		if _, err := db.Exec(`INSERT INTO project_facts (id,project_id,fact_key,category,summary,body,confidence,scope_status,created_at,updated_at) VALUES (?,?,?,?,?,?,?,?,?,?)`,
			id, "project-1", "key:"+id, "asset", "Summary "+id, "body "+id, "tentative", "in_scope", "2026-01-01T00:00:00Z", "2026-01-01T00:00:00Z"); err != nil {
			t.Fatal(err)
		}
	}
	second, err := service.Execute(context.Background(), MigrationRequest{Kind: MigrationKindInspect})
	if err != nil {
		t.Fatal(err)
	}
	if first.Plan.SourceDigest != second.Plan.SourceDigest {
		t.Fatalf("row order changed digest: %s != %s", first.Plan.SourceDigest, second.Plan.SourceDigest)
	}

	if _, err := db.Exec(`UPDATE project_facts SET body='changed secret' WHERE id='fact-a'`); err != nil {
		t.Fatal(err)
	}
	changed, err := service.Execute(context.Background(), MigrationRequest{Kind: MigrationKindInspect})
	if err != nil {
		t.Fatal(err)
	}
	if changed.Plan.SourceDigest == second.Plan.SourceDigest {
		t.Fatal("source-row change did not change digest")
	}
}

func TestInspectReportsStableRedactedBlockersAndWarnings(t *testing.T) {
	t.Parallel()

	db, service := newInspectionService(t)
	statements := []string{
		`INSERT INTO projects (id,name,description,scope_json,defaults_json,kind,created_at,updated_at) VALUES ('project-1','Example','', '{}','{}','pentest','2026-01-01T00:00:00Z','2026-01-01T00:00:00Z')`,
		`INSERT INTO tasks (id,project_id,goal,status,runner,runtime_profile_id,run_controls_json,scope_snapshot_json,created_at,updated_at) VALUES ('task-1','project-1','Inspect','running','local','profile-1','{}','{}','2026-01-01T00:00:00Z','2026-01-01T00:00:00Z')`,
		`INSERT INTO task_continuations (id,task_id,number,runtime_profile_id,runtime_provider,runner,status,started_at,updated_at) VALUES ('continuation-1','task-1',1,'profile-1','manual','local','paused','2026-01-01T00:00:00Z','2026-01-01T00:00:00Z')`,
		`INSERT INTO project_facts (id,project_id,fact_key,category,summary,body,confidence,scope_status,created_at,updated_at) VALUES ('fact-1','project-1','fact:bad','asset','Bad fact','BODY_SECRET','certain','in_scope','2026-01-01T00:00:00Z','2026-01-01T00:00:00Z')`,
		`INSERT INTO findings (id,project_id,finding_key,version,title,description,status,target,proof,impact,recommendation,cvss_version,cvss_vector,cvss_pending,severity,created_at,updated_at) VALUES ('finding-1','project-1','finding:bad',1,'Bad finding','','confirmed','','PROOF_SECRET','','','','',1,'pending','2026-01-01T00:00:00Z','2026-01-01T00:00:00Z')`,
		`INSERT INTO fact_key_aliases (id,project_id,alias_fact_key,canon_fact_key,created_at) VALUES ('alias-1','project-1','old:a','old:b','2026-01-01T00:00:00Z')`,
		`INSERT INTO fact_key_aliases (id,project_id,alias_fact_key,canon_fact_key,created_at) VALUES ('alias-2','project-1','old:b','old:a','2026-01-01T00:00:00Z')`,
		`INSERT INTO evidence_artifacts (id,project_id,evidence_key,attach_to_type,attach_to_key,artifact_type,source_path,managed_path,sha256,summary,created_at,updated_at) VALUES ('evidence-1','project-1','evidence:bad','fact','fact:bad','text','/Users/operator/secret.txt','../escape.txt','','TOKEN_SECRET','2026-01-01T00:00:00Z','2026-01-01T00:00:00Z')`,
		`UPDATE schema_migrations SET checksum='mismatched' WHERE version=1`,
	}
	for _, statement := range statements {
		if _, err := db.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}

	result, err := service.Execute(context.Background(), MigrationRequest{Kind: MigrationKindInspect})
	if err != nil {
		t.Fatal(err)
	}
	assertDiagnosticCodes(t, result.Plan.Blockers,
		"active_continuation",
		"confirmed_finding_incomplete",
		"migration_checksum_mismatch",
		"unknown_fact_confidence",
	)
	assertDiagnosticCodes(t, result.Plan.Warnings,
		"cyclic_fact_alias",
		"evidence_path_escape",
	)

	encoded, err := json.Marshal(result.Plan)
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"BODY_SECRET", "PROOF_SECRET", "TOKEN_SECRET", "/Users/operator/secret.txt", "../escape.txt"} {
		if strings.Contains(string(encoded), secret) {
			t.Fatalf("diagnostics leaked sensitive value %q: %s", secret, encoded)
		}
	}
}

func TestCutoverCreatesVerifiedWALConsistentOwnerOnlyBackup(t *testing.T) {
	t.Parallel()

	db, service := newInspectionService(t)
	insertProjectAndFacts(t, db, []string{"fact-a"})
	backupPath := filepath.Join(t.TempDir(), "legacy.bak")

	result, err := service.Execute(context.Background(), MigrationRequest{
		Kind:       MigrationKindCutover,
		BackupPath: backupPath,
	})
	if err != nil {
		t.Fatalf("Execute(cutover): %v", err)
	}
	if result.Backup == nil {
		t.Fatal("cutover preparation did not return verified backup metadata")
	}
	if result.Backup.Path != backupPath || result.Backup.QuickCheck != "ok" {
		t.Fatalf("unexpected backup metadata: %#v", result.Backup)
	}
	info, err := os.Stat(backupPath)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("backup permissions = %o, want 600", got)
	}
	content, err := os.ReadFile(backupPath)
	if err != nil {
		t.Fatal(err)
	}
	wantDigest := fmt.Sprintf("%x", sha256.Sum256(content))
	if result.Backup.SHA256 != wantDigest {
		t.Fatalf("backup SHA-256 = %s, want %s", result.Backup.SHA256, wantDigest)
	}

	backupDB, err := sql.Open("sqlite", "file:"+backupPath+"?mode=ro")
	if err != nil {
		t.Fatal(err)
	}
	defer backupDB.Close()
	var factCount int
	if err := backupDB.QueryRow(`SELECT COUNT(*) FROM project_facts WHERE id='fact-a'`).Scan(&factCount); err != nil {
		t.Fatal(err)
	}
	if factCount != 1 {
		t.Fatalf("backup missed committed WAL content: fact count=%d", factCount)
	}
	epoch, err := db.CanonicalStore()
	if err != nil {
		t.Fatal(err)
	}
	if epoch != store.CanonicalStoreGraphV1 {
		t.Fatalf("canonical store = %q, want graph_v1", epoch)
	}
}

func TestBackupFailureLeavesSourceRowsAndStoreEpochUnchanged(t *testing.T) {
	t.Parallel()

	db, _ := newInspectionService(t)
	insertProjectAndFacts(t, db, []string{"fact-a"})
	backupFailure := errors.New("injected backup failure")
	service := NewService(db, "ignored.db", t.TempDir(), WithBackupImplementation(
		BackupImplementationFunc(func(context.Context, *store.DB, string, string) (VerifiedBackup, error) {
			return VerifiedBackup{}, backupFailure
		}),
	))

	_, err := service.Execute(context.Background(), MigrationRequest{Kind: MigrationKindCutover})
	if !errors.Is(err, backupFailure) {
		t.Fatalf("expected injected backup failure, got %v", err)
	}
	assertLegacySourceAndEpoch(t, db, 1)
}

func assertLegacySourceAndEpoch(t *testing.T, db *store.DB, wantFacts int) {
	t.Helper()
	var facts int
	if err := db.QueryRow(`SELECT COUNT(*) FROM project_facts`).Scan(&facts); err != nil {
		t.Fatal(err)
	}
	if facts != wantFacts {
		t.Fatalf("legacy source facts changed: got %d want %d", facts, wantFacts)
	}
	epoch, err := db.CanonicalStore()
	if err != nil {
		t.Fatal(err)
	}
	if epoch != store.CanonicalStoreLegacyV1 {
		t.Fatalf("canonical store changed on backup path: %s", epoch)
	}
}

func assertDiagnosticCodes(t *testing.T, diagnostics []Diagnostic, want ...string) {
	t.Helper()
	got := make([]string, 0, len(diagnostics))
	for _, diagnostic := range diagnostics {
		got = append(got, diagnostic.Code)
	}
	sort.Strings(got)
	sort.Strings(want)
	for _, code := range want {
		if !containsString(got, code) {
			t.Fatalf("missing diagnostic %q; got %v", code, got)
		}
	}
}

func containsString(values []string, wanted string) bool {
	for _, value := range values {
		if value == wanted {
			return true
		}
	}
	return false
}

func newInspectionService(t *testing.T) (*store.DB, *Service) {
	t.Helper()
	root := t.TempDir()
	databasePath := filepath.Join(root, "pentest.db")
	artifactRoot := filepath.Join(root, "artifacts")
	if err := os.Mkdir(artifactRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	db, err := store.Open(databasePath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db, NewService(db, databasePath, artifactRoot)
}

func insertProjectAndFacts(t *testing.T, db *store.DB, factIDs []string) {
	t.Helper()
	if _, err := db.Exec(`INSERT INTO projects (id,name,description,scope_json,defaults_json,kind,created_at,updated_at) VALUES ('project-1','Example','', '{}','{}','pentest','2026-01-01T00:00:00Z','2026-01-01T00:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	for _, id := range factIDs {
		if _, err := db.Exec(`INSERT INTO project_facts (id,project_id,fact_key,category,summary,body,confidence,scope_status,created_at,updated_at) VALUES (?,?,?,?,?,?,?,?,?,?)`,
			id, "project-1", "key:"+id, "asset", "Summary "+id, "body "+id, "tentative", "in_scope", "2026-01-01T00:00:00Z", "2026-01-01T00:00:00Z"); err != nil {
			t.Fatal(err)
		}
	}
}

func insertLegacyInspectionFixture(t *testing.T, db *store.DB) {
	t.Helper()
	statements := []string{
		`INSERT INTO projects (id,name,description,scope_json,defaults_json,kind,created_at,updated_at) VALUES ('project-1','Example','', '{}','{}','pentest','2026-01-01T00:00:00Z','2026-01-01T00:00:00Z')`,
		`INSERT INTO project_facts (id,project_id,fact_key,category,summary,body,confidence,scope_status,created_at,updated_at) VALUES ('fact-1','project-1','host:web','asset','Web host','sensitive body','tentative','in_scope','2026-01-01T00:00:00Z','2026-01-01T00:00:00Z')`,
		`INSERT INTO project_fact_versions (id,project_id,fact_key,version,category,summary,body,confidence,scope_status,created_at) VALUES ('fact-version-1','project-1','host:web',1,'asset','Web host','sensitive body','tentative','in_scope','2026-01-01T00:00:00Z')`,
		`INSERT INTO findings (id,project_id,finding_key,version,title,description,status,target,proof,impact,recommendation,cvss_version,cvss_vector,cvss_pending,severity,created_at,updated_at) VALUES ('finding-1','project-1','finding:web','1','Example finding','','unconfirmed','','secret proof','','','','',1,'pending','2026-01-01T00:00:00Z','2026-01-01T00:00:00Z')`,
		`INSERT INTO finding_versions (id,project_id,finding_key,version,title,description,status,target,proof,impact,recommendation,cvss_version,cvss_vector,cvss_pending,severity,created_at) VALUES ('finding-version-1','project-1','finding:web',1,'Example finding','','unconfirmed','','secret proof','','','','',1,'pending','2026-01-01T00:00:00Z')`,
	}
	for _, statement := range statements {
		if _, err := db.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
}

func totalChanges(t *testing.T, db *store.DB) int64 {
	t.Helper()
	var changes int64
	if err := db.QueryRow(`SELECT total_changes()`).Scan(&changes); err != nil {
		t.Fatal(err)
	}
	return changes
}

func directoryEntries(t *testing.T, root string) []string {
	t.Helper()
	var entries []string
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if path == root {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		entries = append(entries, rel)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	sort.Strings(entries)
	return entries
}
