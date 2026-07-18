package blackboardmigration

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"testing"

	"pentest/internal/blackboardv2"
	"pentest/internal/store"
)

func TestMigrateV2NumberedSourceValidatesBeforeDroppingGraphLedger(t *testing.T) {
	dbPath := createNumberedV1Source(t)
	beforeChecksums := inspectNumberedV1Source(t, dbPath)

	source, err := store.OpenMigrationSource(dbPath)
	if err != nil {
		t.Fatalf("open read-only migration source: %v", err)
	}
	if source.Classification() != store.MigrationSourceNumberedV1 || source.CanonicalStore() != store.CanonicalStoreGraphV1 {
		t.Fatalf("source classification=%q epoch=%q", source.Classification(), source.CanonicalStore())
	}
	planResult, err := InspectMigrationSource(context.Background(), source, t.TempDir())
	if err != nil {
		t.Fatalf("inspect source: %v", err)
	}
	backupResult, err := BackupMigrationSource(context.Background(), source, dbPath, t.TempDir(), dbPath+".bak")
	if err != nil {
		t.Fatalf("backup source: %v", err)
	}
	if backupResult.Backup == nil {
		t.Fatal("missing verified backup")
	}
	if _, err := source.ExecContext(context.Background(), `UPDATE blackboard_store_state SET canonical_store='blackboard_v2' WHERE id=1`); err == nil {
		t.Fatal("read-only migration source accepted a write")
	}
	if err := source.Close(); err != nil {
		t.Fatalf("close migration source: %v", err)
	}

	db, err := store.OpenWritableMigrationDB(dbPath)
	if err != nil {
		t.Fatalf("open writable offline migrator: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	result, err := NewService(db, dbPath, t.TempDir()).Execute(context.Background(), MigrationRequest{
		Kind: MigrationKindMigrate, SourceDigest: planResult.Plan.SourceDigest, BackupPath: backupResult.Backup.Path,
	})
	if err != nil {
		t.Fatalf("migrate: %v result=%#v", err, result.Migrate)
	}
	if result.Migrate == nil || result.Migrate.Validation.Status != "passed" {
		t.Fatalf("migration result = %#v", result.Migrate)
	}
	if epoch, err := db.CanonicalStore(); err != nil || epoch != store.CanonicalStoreBlackboardV2 {
		t.Fatalf("epoch after migrate = %q, %v", epoch, err)
	}
	for _, table := range []string{"blackboard_graph_state", "blackboard_nodes", "blackboard_graph_mutations", "task_summary_versions"} {
		if tablePresent(t, db, table) {
			t.Fatalf("validated cutover retained displaced table %s", table)
		}
	}
	assertHistoricalChecksums(t, db, beforeChecksums)

	v2 := blackboardv2.NewService(db)
	detail, err := v2.ReadCurrent(context.Background(), "project-1", "fact:source")
	if err != nil {
		t.Fatalf("read migrated v2 record: %v", err)
	}
	if detail.Record.Summary != "source fact v2" {
		t.Fatalf("current migrated Fact = %#v", detail.Record)
	}
	history, err := v2.ReadHistory(context.Background(), "project-1", "fact:source", blackboardv2.HistoryOptions{Limit: 20})
	if err != nil || len(history.Items) == 0 {
		t.Fatalf("migrated Semantic History = %#v, %v", history, err)
	}
	var relationships, redirects int
	if err := db.QueryRow(`SELECT COUNT(*) FROM blackboard_v2_relationships WHERE project_id='project-1' AND from_key='fact:source' AND relation='about' AND to_key='host:web'`).Scan(&relationships); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM blackboard_v2_key_redirects WHERE project_id='project-1' AND source_key='fact:legacy-source' AND canonical_key='fact:source'`).Scan(&redirects); err != nil {
		t.Fatal(err)
	}
	if relationships != 1 || redirects != 1 {
		t.Fatalf("migrated relationship/redirect counts = %d/%d", relationships, redirects)
	}
}

func TestMigrateV2FailureAfterRetirementRollsBackSourceAndEpoch(t *testing.T) {
	dbPath := createNumberedV1Source(t)
	source, err := store.OpenMigrationSource(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := InspectMigrationSource(context.Background(), source, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	backup, err := BackupMigrationSource(context.Background(), source, dbPath, t.TempDir(), dbPath+".bak")
	if err != nil {
		t.Fatal(err)
	}
	if err := source.Close(); err != nil {
		t.Fatal(err)
	}

	db, err := store.OpenWritableMigrationDB(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	injected := errors.New("injected after state flip")
	service := NewService(db, dbPath, t.TempDir(), WithCutoverFailureInjector(CutoverFailureInjectorFunc(func(point CutoverFailurePoint) error {
		if point == CutoverFailureAfterStateFlip {
			return injected
		}
		return nil
	})))
	result, err := service.Execute(context.Background(), MigrationRequest{
		Kind: MigrationKindMigrate, SourceDigest: plan.Plan.SourceDigest, BackupPath: backup.Backup.Path,
	})
	if !errors.Is(err, injected) {
		t.Fatalf("migrate error = %v, want injected rollback; result=%#v", err, result.Migrate)
	}
	if epoch, err := db.CanonicalStore(); err != nil || epoch != store.CanonicalStoreGraphV1 {
		t.Fatalf("epoch after rollback = %q, %v", epoch, err)
	}
	for _, table := range []string{"blackboard_nodes", "blackboard_graph_mutations", "task_summary_versions"} {
		if !tablePresent(t, db, table) {
			t.Fatalf("rollback did not restore source table %s", table)
		}
	}
	var retirementMigration int
	if err := db.QueryRow(`SELECT COUNT(*) FROM schema_migrations WHERE version=34`).Scan(&retirementMigration); err != nil {
		t.Fatal(err)
	}
	if retirementMigration != 0 {
		t.Fatal("failed cutover recorded retirement migration")
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := store.OpenMigrationSource(dbPath)
	if err != nil {
		t.Fatalf("reopen rolled-back source: %v", err)
	}
	defer reopened.Close()
	if reopened.CanonicalStore() != store.CanonicalStoreGraphV1 {
		t.Fatalf("reopened source epoch = %q", reopened.CanonicalStore())
	}
}

func createNumberedV1Source(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "numbered-v1.db")
	db, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, statement := range []string{
		`DELETE FROM schema_migrations WHERE version >= 20`,
		`UPDATE blackboard_store_state SET canonical_store='graph_v1',cutover_state='graph',migration_contract_version='legacy_blackboard_to_graph_v1' WHERE id=1`,
		`CREATE TABLE blackboard_graph_state (project_id TEXT PRIMARY KEY)`,
		`CREATE TABLE blackboard_graph_mutations (project_id TEXT, mutation_seq INTEGER)`,
		`CREATE TABLE blackboard_nodes (project_id TEXT,id TEXT,node_type TEXT,original_stable_key TEXT,created_mutation_seq INTEGER,created_operation_index INTEGER,created_at TEXT,PRIMARY KEY(project_id,id))`,
		`CREATE TABLE blackboard_node_versions (project_id TEXT,node_id TEXT,version INTEGER,result_graph_revision INTEGER,mutation_seq INTEGER,operation_index INTEGER,schema_version INTEGER,disposition TEXT,merge_target_id TEXT,properties_json TEXT,semantic_hash TEXT,updated_at TEXT,PRIMARY KEY(project_id,node_id,version))`,
		`CREATE TABLE blackboard_node_heads (project_id TEXT,node_id TEXT,node_type TEXT,version INTEGER,graph_revision INTEGER,disposition TEXT,merge_target_id TEXT,lifecycle_state TEXT,entity_kind TEXT,scope_status TEXT,semantic_hash TEXT,PRIMARY KEY(project_id,node_id))`,
		`CREATE TABLE blackboard_edge_heads (project_id TEXT,edge_id TEXT,edge_type TEXT,from_node_id TEXT,to_node_id TEXT,version INTEGER,graph_revision INTEGER,state TEXT,semantic_hash TEXT,PRIMARY KEY(project_id,edge_id))`,
		`CREATE TABLE blackboard_edge_versions (project_id TEXT,edge_id TEXT,version INTEGER,result_graph_revision INTEGER,mutation_seq INTEGER,operation_index INTEGER,from_node_id TEXT,to_node_id TEXT,state TEXT,summary TEXT,semantic_hash TEXT,updated_at TEXT,PRIMARY KEY(project_id,edge_id,version))`,
		`CREATE TABLE blackboard_key_registry (project_id TEXT,node_type TEXT,key TEXT,latest_key_version INTEGER,role TEXT,source_node_id TEXT,canonical_node_id TEXT,semantic_hash TEXT,PRIMARY KEY(project_id,node_type,key))`,
		`ALTER TABLE task_continuations ADD COLUMN blackboard_finish_summary_version_id TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE task_continuations ADD COLUMN blackboard_finish_graph_revision INTEGER NOT NULL DEFAULT 0`,
		`ALTER TABLE task_continuations ADD COLUMN blackboard_finish_mutation_sequence INTEGER NOT NULL DEFAULT 0`,
		`ALTER TABLE task_continuations ADD COLUMN blackboard_finished_at TEXT NOT NULL DEFAULT ''`,
		`CREATE TABLE task_summary_versions (id TEXT PRIMARY KEY,task_id TEXT NOT NULL,continuation_id TEXT,version INTEGER NOT NULL,summary TEXT NOT NULL,submitted_by TEXT NOT NULL DEFAULT '',created_at TEXT NOT NULL)`,
	} {
		if _, err := db.Exec(statement); err != nil {
			_ = db.Close()
			t.Fatalf("prepare numbered-v1 source: %v\n%s", err, statement)
		}
	}
	if _, err := db.Exec(`INSERT INTO projects(id,name,description,scope_json,defaults_json,kind,created_at,updated_at) VALUES('project-1','P','','{}','{}','pentest','2026-01-01T00:00:00Z','2026-01-01T00:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`
		INSERT INTO blackboard_graph_state VALUES('project-1');
		INSERT INTO blackboard_graph_mutations VALUES('project-1',1);
		INSERT INTO blackboard_nodes VALUES('project-1','node-fact','project_fact','fact:source',0,0,'2026-01-01T00:00:00Z');
		INSERT INTO blackboard_node_versions VALUES('project-1','node-fact',1,1,0,0,1,'main',NULL,'{"category":"target","summary":"source fact v1","confidence":"tentative","scope_status":"in_scope"}','hash-1','2026-01-01T00:00:00Z');
		INSERT INTO blackboard_node_versions VALUES('project-1','node-fact',2,2,0,0,1,'main',NULL,'{"category":"target","summary":"source fact v2","confidence":"confirmed","scope_status":"in_scope"}','hash-2','2026-01-02T00:00:00Z');
		INSERT INTO blackboard_node_heads VALUES('project-1','node-fact','project_fact',2,2,'main',NULL,'','','in_scope','hash-2');
		INSERT INTO blackboard_nodes VALUES('project-1','node-host','entity','host:web',0,0,'2026-01-01T00:00:00Z');
		INSERT INTO blackboard_node_versions VALUES('project-1','node-host',1,1,0,0,1,'main',NULL,'{"kind":"host","name":"web","locator":"web.example","scope_status":"in_scope","status":"active"}','hash-host','2026-01-01T00:00:00Z');
		INSERT INTO blackboard_node_heads VALUES('project-1','node-host','entity',1,1,'main',NULL,'','host','in_scope','hash-host');
		INSERT INTO blackboard_edge_versions VALUES('project-1','edge-about',1,2,0,0,'node-fact','node-host','active','source host','hash-edge','2026-01-02T00:00:00Z');
		INSERT INTO blackboard_edge_heads VALUES('project-1','edge-about','about','node-fact','node-host',1,2,'active','hash-edge');
		INSERT INTO blackboard_key_registry VALUES('project-1','project_fact','fact:legacy-source',1,'alias','node-alias','node-fact','hash-alias');
	`); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	return path
}

func inspectNumberedV1Source(t *testing.T, path string) []string {
	t.Helper()
	source, err := store.OpenMigrationSource(path)
	if err != nil {
		t.Fatal(err)
	}
	defer source.Close()
	rows, err := source.QueryContext(context.Background(), `SELECT printf('%d|%s|%s',version,name,checksum) FROM schema_migrations ORDER BY version`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var checksums []string
	for rows.Next() {
		var checksum string
		if err := rows.Scan(&checksum); err != nil {
			t.Fatal(err)
		}
		checksums = append(checksums, checksum)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if len(checksums) != 19 {
		t.Fatalf("numbered-v1 migration count = %d, want 19", len(checksums))
	}
	return checksums
}

func assertHistoricalChecksums(t *testing.T, db *store.DB, before []string) {
	t.Helper()
	rows, err := db.Query(`SELECT printf('%d|%s|%s',version,name,checksum) FROM schema_migrations WHERE version <= 19 ORDER BY version`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var after []string
	for rows.Next() {
		var checksum string
		if err := rows.Scan(&checksum); err != nil {
			t.Fatal(err)
		}
		after = append(after, checksum)
	}
	if fmt.Sprint(after) != fmt.Sprint(before) {
		t.Fatalf("historical migration checksums changed:\nbefore=%v\nafter=%v", before, after)
	}
}

func tablePresent(t *testing.T, db *store.DB, table string) bool {
	t.Helper()
	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name=?`, table).Scan(&count); err != nil {
		t.Fatal(err)
	}
	return count != 0
}
