package store_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"pentest/internal/store"
)

const v1MigrationGuidance = "blackboard v1 store epoch %q cannot be opened for daemon/runtime use; stop the daemon and run 'blackboard v2 inspect', 'blackboard v2 migrate', then 'blackboard v2 verify'"
const unknownStoreEpochRefusal = "unknown canonical store epoch %q cannot be opened for daemon/runtime use; restore a supported database or use an explicit offline migration workflow"

func TestOpenFreshDatabaseBootstrapsBlackboardV2AndReopensDeterministically(t *testing.T) {
	path := filepath.Join(t.TempDir(), "pentest.db")

	first, err := store.Open(path)
	if err != nil {
		t.Fatalf("open fresh store: %v", err)
	}
	epoch, err := first.CanonicalStore()
	if err != nil {
		t.Fatalf("read fresh store epoch: %v", err)
	}
	if epoch != "blackboard_v2" {
		t.Fatalf("fresh store epoch = %q, want blackboard_v2", epoch)
	}
	if err := first.Close(); err != nil {
		t.Fatalf("close fresh store: %v", err)
	}

	reopened, err := store.Open(path)
	if err != nil {
		t.Fatalf("reopen v2 store: %v", err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	reopenedEpoch, err := reopened.CanonicalStore()
	if err != nil {
		t.Fatalf("read reopened store epoch: %v", err)
	}
	if reopenedEpoch != epoch {
		t.Fatalf("reopened store epoch = %q, want stable %q", reopenedEpoch, epoch)
	}
}

func TestOpenRefusesV1WithoutCutoverAndReturnsStableMigrationGuidance(t *testing.T) {
	for _, epoch := range []string{"legacy_v1", "graph_v1", "graph_v1_finalized"} {
		t.Run(epoch, func(t *testing.T) {
			path := createV1StoreFixture(t, epoch)

			opened, err := store.Open(path)
			if opened != nil {
				_ = opened.Close()
				t.Fatal("ordinary Open returned an active Store for a v1 database")
			}
			want := fmt.Sprintf(v1MigrationGuidance, epoch)
			if err == nil || err.Error() != want {
				t.Fatalf("ordinary Open error = %v, want %q", err, want)
			}

			verify, err := sql.Open("sqlite", path)
			if err != nil {
				t.Fatalf("open refused source for verification: %v", err)
			}
			t.Cleanup(func() { _ = verify.Close() })
			var gotEpoch, marker string
			if err := verify.QueryRow(`SELECT canonical_store FROM blackboard_store_state WHERE id=1`).Scan(&gotEpoch); err != nil {
				t.Fatalf("read refused source epoch: %v", err)
			}
			if err := verify.QueryRow(`SELECT marker FROM blackboard_v1_marker`).Scan(&marker); err != nil {
				t.Fatalf("read preserved v1 marker: %v", err)
			}
			var migration20 int
			if err := verify.QueryRow(`SELECT COUNT(*) FROM schema_migrations WHERE version=20`).Scan(&migration20); err != nil {
				t.Fatalf("check v2 bootstrap migration: %v", err)
			}
			if gotEpoch != epoch || marker != "preserve-v1" || migration20 != 0 {
				t.Fatalf("refused source mutated: epoch=%q marker=%q migration20=%d", gotEpoch, marker, migration20)
			}
		})
	}
}

func TestOrdinaryOpenRefusesUnknownEpochWithoutTouchingSQLiteState(t *testing.T) {
	tests := []struct {
		name               string
		epoch              string
		make               func(*testing.T) string
		prepare            func(*testing.T, string) func()
		wantMigrationCount int
		wantSidecars       bool
	}{
		{
			name:  "before migration 20",
			epoch: "future_v3",
			make: func(t *testing.T) string {
				path := createV1StoreFixture(t, "future_v3")
				forceJournalMode(t, path, "DELETE")
				return path
			},
			wantMigrationCount: 19,
		},
		{
			name:  "empty epoch before migration 20",
			epoch: "",
			make: func(t *testing.T) string {
				path := createV1StoreFixture(t, "")
				forceJournalMode(t, path, "DELETE")
				return path
			},
			wantMigrationCount: 19,
		},
		{
			name:  "current v2 migrations recorded with unknown epoch in live WAL",
			epoch: "future_v3",
			make: func(t *testing.T) string {
				path := filepath.Join(t.TempDir(), "unknown-v2.db")
				db, err := store.Open(path)
				if err != nil {
					t.Fatalf("create current-v2 fixture: %v", err)
				}
				if err := db.Close(); err != nil {
					t.Fatalf("close current-v2 fixture: %v", err)
				}
				return path
			},
			prepare: func(t *testing.T, path string) func() {
				return holdStoreEpochUpdateInWAL(t, path, "future_v3")
			},
			wantMigrationCount: 35,
			wantSidecars:       true,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			path := test.make(t)
			if test.prepare != nil {
				defer test.prepare(t, path)()
			}
			before := captureSQLiteSourceState(t, path)
			if len(before.MigrationChecksums) != test.wantMigrationCount {
				t.Fatalf("fixture migration count = %d, want %d", len(before.MigrationChecksums), test.wantMigrationCount)
			}
			if test.wantSidecars && (!before.WAL.Exists || !before.SHM.Exists) {
				t.Fatalf("fixture lacks SQLite sidecars: wal=%v shm=%v", before.WAL.Exists, before.SHM.Exists)
			}

			opened, err := store.Open(path)
			if opened != nil {
				_ = opened.Close()
				t.Fatal("ordinary Open returned an active Store for an unknown epoch")
			}
			want := fmt.Sprintf(unknownStoreEpochRefusal, test.epoch)
			if err == nil || err.Error() != want {
				t.Fatalf("ordinary Open error = %v, want %q", err, want)
			}

			after := captureSQLiteSourceState(t, path)
			assertSQLiteSourceStateEqual(t, before, after)
		})
	}
}

func TestOrdinaryOpenRefusesExistingSchemaWithoutEpochMetadata(t *testing.T) {
	tests := []struct {
		name string
		sql  string
	}{
		{
			name: "empty schema migrations table",
			sql: `CREATE TABLE schema_migrations (
				version INTEGER PRIMARY KEY,
				name TEXT NOT NULL,
				checksum TEXT NOT NULL,
				applied_at TEXT NOT NULL
			)`,
		},
		{name: "unrecognized user table", sql: `CREATE TABLE notes(id TEXT PRIMARY KEY, body TEXT NOT NULL)`},
		{name: "user table resembling SQLite prefix", sql: `CREATE TABLE sqliteXnotes(id TEXT PRIMARY KEY, body TEXT NOT NULL)`},
		{name: "unrecognized user view without tables", sql: `CREATE VIEW user_notes AS SELECT 'unknown' AS body`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "missing-epoch.db")
			db, err := sql.Open("sqlite", path)
			if err != nil {
				t.Fatalf("open missing-epoch fixture: %v", err)
			}
			if _, err := db.Exec(test.sql); err != nil {
				_ = db.Close()
				t.Fatalf("create missing-epoch fixture: %v", err)
			}
			if err := db.Close(); err != nil {
				t.Fatalf("close missing-epoch fixture: %v", err)
			}
			forceJournalMode(t, path, "DELETE")
			before := captureSQLiteSourceState(t, path)
			if before.WAL.Exists || before.SHM.Exists {
				t.Fatalf("fixture has SQLite sidecars before refused Open: wal=%v shm=%v", before.WAL.Exists, before.SHM.Exists)
			}

			opened, err := store.Open(path)
			if opened != nil {
				_ = opened.Close()
				t.Fatal("ordinary Open bootstrapped an existing schema without epoch metadata")
			}
			want := fmt.Sprintf(unknownStoreEpochRefusal, "")
			if err == nil || err.Error() != want {
				t.Fatalf("ordinary Open error = %v, want %q", err, want)
			}

			after := captureSQLiteSourceState(t, path)
			assertSQLiteSourceStateEqual(t, before, after)
		})
	}
}

func TestOpenMigrationSourceValidatesHistoricalV1ReadOnlyWithoutActivation(t *testing.T) {
	path := createV1StoreFixture(t, "graph_v1")

	source, err := store.OpenMigrationSource(path)
	if err != nil {
		t.Fatalf("open offline migration source: %v", err)
	}
	if source.CanonicalStore() != "graph_v1" {
		t.Fatalf("migration source epoch = %q, want graph_v1", source.CanonicalStore())
	}
	if source.Classification() != store.MigrationSourceNumberedV1 {
		t.Fatalf("migration source classification = %q, want numbered_v1", source.Classification())
	}
	if err := source.Validate(context.Background()); err != nil {
		t.Fatalf("revalidate numbered migration source: %v", err)
	}
	if err := source.ValidateMigrationHistory(context.Background()); err != nil {
		t.Fatalf("validate historical v1 migrations: %v", err)
	}
	if _, err := source.ExecContext(context.Background(), `UPDATE blackboard_store_state SET canonical_store='blackboard_v2' WHERE id=1`); err == nil {
		t.Fatal("offline migration source accepted a write")
	}
	if err := source.Close(); err != nil {
		t.Fatalf("close offline migration source: %v", err)
	}

	opened, err := store.Open(path)
	if opened != nil {
		_ = opened.Close()
		t.Fatal("migration-source open made v1 active as a v2 Store")
	}
	want := fmt.Sprintf(v1MigrationGuidance, "graph_v1")
	if err == nil || err.Error() != want {
		t.Fatalf("ordinary Open after migration-source read = %v, want %q", err, want)
	}
}

func TestOpenMigrationSourceReadsWALSnapshotWithoutMutatingSource(t *testing.T) {
	path := createV1StoreFixture(t, store.CanonicalStoreGraphV1)
	defer holdStoreEpochUpdateInWAL(t, path, store.CanonicalStoreGraphV1Finalized)()
	before := captureSQLiteSourceState(t, path)
	if !before.WAL.Exists || !before.SHM.Exists {
		t.Fatalf("fixture lacks SQLite sidecars: wal=%v shm=%v", before.WAL.Exists, before.SHM.Exists)
	}

	source, err := store.OpenMigrationSource(path)
	if err != nil {
		t.Fatalf("open WAL-backed migration source: %v", err)
	}
	if source.CanonicalStore() != store.CanonicalStoreGraphV1Finalized {
		_ = source.Close()
		t.Fatalf("migration source epoch = %q, want graph_v1_finalized", source.CanonicalStore())
	}
	if source.Classification() != store.MigrationSourceNumberedV1 {
		_ = source.Close()
		t.Fatalf("migration source classification = %q, want numbered_v1", source.Classification())
	}
	if err := source.ValidateMigrationHistory(context.Background()); err != nil {
		_ = source.Close()
		t.Fatalf("validate WAL-backed migration history: %v", err)
	}
	if err := source.Close(); err != nil {
		t.Fatalf("close WAL-backed migration source: %v", err)
	}

	after := captureSQLiteSourceState(t, path)
	assertSQLiteSourceStateEqual(t, before, after)
}

func TestOpenRefusesPreNumberedV1WithoutBootstrappingV2(t *testing.T) {
	path := filepath.Join(t.TempDir(), "pre-numbered-v1.db")
	legacy, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("open pre-numbered v1 fixture: %v", err)
	}
	if _, err := legacy.Exec(`
		CREATE TABLE projects (
			id TEXT PRIMARY KEY,
			name TEXT NOT NULL,
			description TEXT NOT NULL DEFAULT '',
			scope_json TEXT NOT NULL,
			created_at TEXT NOT NULL,
			updated_at TEXT NOT NULL
		);
		INSERT INTO projects (id,name,scope_json,created_at,updated_at)
		VALUES ('legacy-project','Legacy','{}','2026-07-15T00:00:00Z','2026-07-15T00:00:00Z');
	`); err != nil {
		t.Fatalf("seed pre-numbered v1 fixture: %v", err)
	}
	if err := legacy.Close(); err != nil {
		t.Fatalf("close pre-numbered v1 fixture: %v", err)
	}

	opened, err := store.Open(path)
	if opened != nil {
		_ = opened.Close()
		t.Fatal("ordinary Open upgraded a pre-numbered v1 database")
	}
	want := fmt.Sprintf(v1MigrationGuidance, "legacy_v1")
	if err == nil || err.Error() != want {
		t.Fatalf("ordinary Open error = %v, want %q", err, want)
	}

	verify, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("reopen pre-numbered v1 fixture: %v", err)
	}
	t.Cleanup(func() { _ = verify.Close() })
	var projectName string
	if err := verify.QueryRow(`SELECT name FROM projects WHERE id='legacy-project'`).Scan(&projectName); err != nil {
		t.Fatalf("read preserved pre-numbered row: %v", err)
	}
	var migrationsTable int
	if err := verify.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='schema_migrations'`).Scan(&migrationsTable); err != nil {
		t.Fatalf("check migration table absence: %v", err)
	}
	if projectName != "Legacy" || migrationsTable != 0 {
		t.Fatalf("pre-numbered source mutated: project=%q schema_migrations=%d", projectName, migrationsTable)
	}
}

func TestOrdinaryOpenRefusesV1WithoutTouchingSQLiteFileOrSidecars(t *testing.T) {
	tests := []struct {
		name        string
		epoch       string
		journalMode string
		make        func(*testing.T) string
		prepare     func(*testing.T, string) func()
		wantSidecar bool
	}{
		{name: "numbered graph_v1 delete journal", epoch: store.CanonicalStoreGraphV1, journalMode: "DELETE", make: func(t *testing.T) string {
			return createV1StoreFixture(t, store.CanonicalStoreGraphV1)
		}},
		{name: "numbered graph_v1 wal journal", epoch: store.CanonicalStoreGraphV1, journalMode: "WAL", make: func(t *testing.T) string {
			return createV1StoreFixture(t, store.CanonicalStoreGraphV1)
		}},
		{name: "numbered v1 with newer epoch in live wal", epoch: store.CanonicalStoreGraphV1Finalized, make: func(t *testing.T) string {
			return createV1StoreFixture(t, store.CanonicalStoreGraphV1)
		}, prepare: func(t *testing.T, path string) func() {
			return holdStoreEpochUpdateInWAL(t, path, store.CanonicalStoreGraphV1Finalized)
		}, wantSidecar: true},
		{name: "pre-numbered legacy_v1", epoch: store.CanonicalStoreLegacyV1, journalMode: "DELETE", make: createPreNumberedV1Fixture},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			path := test.make(t)
			if test.prepare != nil {
				defer test.prepare(t, path)()
			} else {
				forceJournalMode(t, path, test.journalMode)
			}
			before := captureSQLiteSourceState(t, path)
			if !test.wantSidecar && (before.WAL.Exists || before.SHM.Exists) {
				t.Fatalf("fixture has SQLite sidecars before refused Open: wal=%v shm=%v", before.WAL.Exists, before.SHM.Exists)
			}
			if test.wantSidecar && (!before.WAL.Exists || !before.SHM.Exists) {
				t.Fatalf("fixture lacks SQLite sidecars before refused Open: wal=%v shm=%v", before.WAL.Exists, before.SHM.Exists)
			}

			opened, err := store.Open(path)
			if opened != nil {
				_ = opened.Close()
				t.Fatal("ordinary Open returned an active v1 Store")
			}
			want := fmt.Sprintf(v1MigrationGuidance, test.epoch)
			if err == nil || err.Error() != want {
				t.Fatalf("ordinary Open error = %v, want %q", err, want)
			}

			after := captureSQLiteSourceState(t, path)
			assertSQLiteSourceStateEqual(t, before, after)
		})
	}
}

type sqliteFileImage struct {
	Exists bool
	Bytes  []byte
	SHA256 [sha256.Size]byte
}

type sqliteSourceState struct {
	Main               sqliteFileImage
	WAL                sqliteFileImage
	SHM                sqliteFileImage
	JournalMode        string
	MigrationChecksums []string
	EpochTableExists   bool
	Epoch              string
}

func captureSQLiteSourceState(t *testing.T, path string) sqliteSourceState {
	t.Helper()
	values := url.Values{"mode": {"ro"}, "immutable": {"1"}}
	escaped := strings.ReplaceAll(url.PathEscape(path), "%2F", "/")
	db, err := sql.Open("sqlite", "file:"+escaped+"?"+values.Encode())
	if err != nil {
		t.Fatalf("open immutable source snapshot: %v", err)
	}
	if err := db.Ping(); err != nil {
		_ = db.Close()
		t.Fatalf("ping immutable source snapshot: %v", err)
	}
	state := sqliteSourceState{}
	if err := db.QueryRow(`PRAGMA journal_mode`).Scan(&state.JournalMode); err != nil {
		_ = db.Close()
		t.Fatalf("read source journal mode: %v", err)
	}
	var hasMigrations int
	if err := db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='schema_migrations'`).Scan(&hasMigrations); err != nil {
		_ = db.Close()
		t.Fatalf("inspect source migration history: %v", err)
	}
	if hasMigrations == 1 {
		rows, err := db.Query(`SELECT printf('%d|%s|%s',version,name,checksum) FROM schema_migrations ORDER BY version`)
		if err != nil {
			_ = db.Close()
			t.Fatalf("read source migration checksums: %v", err)
		}
		for rows.Next() {
			var checksum string
			if err := rows.Scan(&checksum); err != nil {
				_ = rows.Close()
				_ = db.Close()
				t.Fatalf("scan source migration checksum: %v", err)
			}
			state.MigrationChecksums = append(state.MigrationChecksums, checksum)
		}
		if err := rows.Err(); err != nil {
			_ = rows.Close()
			_ = db.Close()
			t.Fatalf("iterate source migration checksums: %v", err)
		}
		if err := rows.Close(); err != nil {
			_ = db.Close()
			t.Fatalf("close source migration checksum rows: %v", err)
		}
	}
	var hasEpoch int
	if err := db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='blackboard_store_state'`).Scan(&hasEpoch); err != nil {
		_ = db.Close()
		t.Fatalf("inspect source epoch table: %v", err)
	}
	state.EpochTableExists = hasEpoch == 1
	if state.EpochTableExists {
		if err := db.QueryRow(`SELECT canonical_store FROM blackboard_store_state WHERE id=1`).Scan(&state.Epoch); err != nil {
			_ = db.Close()
			t.Fatalf("read source epoch: %v", err)
		}
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close immutable source snapshot: %v", err)
	}
	state.Main = captureSQLiteFileImage(t, path, true)
	state.WAL = captureSQLiteFileImage(t, path+"-wal", false)
	state.SHM = captureSQLiteFileImage(t, path+"-shm", false)
	return state
}

func captureSQLiteFileImage(t *testing.T, path string, required bool) sqliteFileImage {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		if !required && os.IsNotExist(err) {
			return sqliteFileImage{}
		}
		t.Fatalf("read SQLite file %s: %v", path, err)
	}
	return sqliteFileImage{Exists: true, Bytes: raw, SHA256: sha256.Sum256(raw)}
}

func assertSQLiteSourceStateEqual(t *testing.T, before, after sqliteSourceState) {
	t.Helper()
	if !bytes.Equal(after.Main.Bytes, before.Main.Bytes) {
		t.Errorf("SQLite main file bytes changed after read-only inspection")
	}
	if after.Main.SHA256 != before.Main.SHA256 {
		t.Errorf("SQLite main file SHA256 changed: before=%x after=%x", before.Main.SHA256, after.Main.SHA256)
	}
	if after.JournalMode != before.JournalMode {
		t.Errorf("SQLite journal_mode changed: before=%q after=%q", before.JournalMode, after.JournalMode)
	}
	for name, pair := range map[string][2]sqliteFileImage{"-wal": {before.WAL, after.WAL}, "-shm": {before.SHM, after.SHM}} {
		if pair[0].Exists != pair[1].Exists || !bytes.Equal(pair[0].Bytes, pair[1].Bytes) || pair[0].SHA256 != pair[1].SHA256 {
			t.Errorf("SQLite %s sidecar changed: before=%+v after=%+v", name, pair[0], pair[1])
		}
	}
	if !reflect.DeepEqual(after.MigrationChecksums, before.MigrationChecksums) {
		t.Errorf("migration checksum rows changed: before=%v after=%v", before.MigrationChecksums, after.MigrationChecksums)
	}
	if after.EpochTableExists != before.EpochTableExists || after.Epoch != before.Epoch {
		t.Errorf("canonical epoch changed: before=(exists=%v epoch=%q) after=(exists=%v epoch=%q)", before.EpochTableExists, before.Epoch, after.EpochTableExists, after.Epoch)
	}
}

func forceJournalMode(t *testing.T, path, want string) {
	t.Helper()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("open source to set journal mode: %v", err)
	}
	var mode string
	if err := db.QueryRow(`PRAGMA journal_mode=` + want).Scan(&mode); err != nil {
		_ = db.Close()
		t.Fatalf("set source journal mode: %v", err)
	}
	if mode != strings.ToLower(want) {
		_ = db.Close()
		t.Fatalf("source journal mode = %q, want %q", mode, strings.ToLower(want))
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close source journal setup: %v", err)
	}
}

func holdStoreEpochUpdateInWAL(t *testing.T, path, epoch string) func() {
	t.Helper()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("open v1 source for WAL update: %v", err)
	}
	closeDB := func() { _ = db.Close() }
	var mode string
	if err := db.QueryRow(`PRAGMA journal_mode=WAL`).Scan(&mode); err != nil {
		closeDB()
		t.Fatalf("enable WAL for v1 source: %v", err)
	}
	if mode != "wal" {
		closeDB()
		t.Fatalf("v1 source journal mode = %q, want wal", mode)
	}
	if _, err := db.Exec(`PRAGMA wal_autocheckpoint=0`); err != nil {
		closeDB()
		t.Fatalf("disable WAL autocheckpoint: %v", err)
	}
	if _, err := db.Exec(`UPDATE blackboard_store_state SET canonical_store=? WHERE id=1`, epoch); err != nil {
		closeDB()
		t.Fatalf("write latest v1 epoch into WAL: %v", err)
	}
	return closeDB
}

func createPreNumberedV1Fixture(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "pre-numbered-v1.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("open pre-numbered v1 fixture: %v", err)
	}
	if _, err := db.Exec(`
		CREATE TABLE projects (
			id TEXT PRIMARY KEY,
			name TEXT NOT NULL,
			description TEXT NOT NULL DEFAULT '',
			scope_json TEXT NOT NULL,
			created_at TEXT NOT NULL,
			updated_at TEXT NOT NULL
		);
		CREATE TABLE project_facts (
			id TEXT PRIMARY KEY,
			project_id TEXT NOT NULL,
			fact_key TEXT NOT NULL,
			category TEXT NOT NULL,
			summary TEXT NOT NULL,
			body TEXT NOT NULL DEFAULT '',
			confidence TEXT NOT NULL,
			scope_status TEXT NOT NULL DEFAULT '',
			created_at TEXT NOT NULL,
			updated_at TEXT NOT NULL
		);
		INSERT INTO projects (id,name,scope_json,created_at,updated_at)
		VALUES ('legacy-project','Legacy','{}','2026-07-15T00:00:00Z','2026-07-15T00:00:00Z');
	`); err != nil {
		_ = db.Close()
		t.Fatalf("seed pre-numbered v1 fixture: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close pre-numbered v1 fixture: %v", err)
	}
	return path
}

func createV1StoreFixture(t *testing.T, epoch string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "blackboard-v1.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("open v1 fixture: %v", err)
	}
	defer db.Close()
	if _, err := db.Exec(`
		CREATE TABLE schema_migrations (
			version INTEGER PRIMARY KEY,
			name TEXT NOT NULL,
			checksum TEXT NOT NULL,
			applied_at TEXT NOT NULL
		);
		CREATE TABLE blackboard_store_state (
			id INTEGER PRIMARY KEY CHECK (id = 1),
			canonical_store TEXT NOT NULL,
			cutover_state TEXT NOT NULL,
			migration_contract_version TEXT NOT NULL DEFAULT '',
			graph_schema_version INTEGER NOT NULL DEFAULT 0,
			updated_at TEXT NOT NULL
		);
		CREATE TABLE blackboard_v1_marker (marker TEXT NOT NULL);
		INSERT INTO blackboard_store_state (
			id, canonical_store, cutover_state, migration_contract_version, graph_schema_version, updated_at
		) VALUES (1, ?, 'v1', 'legacy_blackboard_to_graph_v1', 1, '2026-07-15T00:00:00Z');
		INSERT INTO blackboard_v1_marker (marker) VALUES ('preserve-v1');
	`, epoch); err != nil {
		t.Fatalf("create v1 fixture schema: %v", err)
	}
	for _, migration := range historicalV1Migrations {
		if _, err := db.Exec(`INSERT INTO schema_migrations (version,name,checksum,applied_at) VALUES (?,?,?,'2026-07-15T00:00:00Z')`, migration.version, migration.name, migration.checksum); err != nil {
			t.Fatalf("record historical migration %d: %v", migration.version, err)
		}
	}
	return path
}

var historicalV1Migrations = []struct {
	version  int
	name     string
	checksum string
}{
	{1, "baseline_legacy_schema", "b36f837af436f78c8a6236a82f57fd1e1e29c22ab05b7b2dade365a2f7141287"},
	{2, "store_epoch_and_graph_support", "43c8acf9a57fd4909f17b782ee14a90a3e49c0ab3f279b1451e5e4ef8ff50832"},
	{3, "graph_ledger_core", "b28e468642a5d696c1efc6398bd6d1e5419e5d10bb8398a4bb179cd30cb21e4a"},
	{4, "graph_edges", "e0ebaa632303e639ebd3408e01ec5e1f8f0da809eb35780f2d948bcfd752e8ad"},
	{5, "graph_edge_ledger_guards", "0a6fc36e0d076d555d805a261b2a6df23993ca277ea70082716c2e3d312c86f8"},
	{6, "graph_edge_version_endpoints", "4a8e1c3d26e718bd271f41e731786fe14cf57a1bf60640db591b58344747fc7a"},
	{7, "graph_edge_identity_and_integrity_cutover", "c31f3b6cb95a48d6f3b5c45f37b5e4d51947a23318bd997ed9c6f4711c7b9bc6"},
	{8, "graph_budget_compaction_and_health", "6a5a421db3e415518bb330bfa02818e2a8dc3c277877aff4701e531bd34ae95f"},
	{9, "blackboard_read_cursor_secret", "26afaa4fb753f7e60263e623299b14885c8151baabd35e09df13a86d9d3abb77"},
	{10, "blackboard_health_run_requests", "a1968cfa7f73b30f66da0869e281141d5fc35bedccc768a07ae8881f97da723d"},
	{11, "continuation_interface_grants", "ea87b913265e9eace05d68c1e55bf2d4c982dda189cc1f0d0d829f183fedda11"},
	{12, "project_interface_evidence_requests", "70ca172458d6d22731dd29f098c360c725363bc172d2f675d767afee740a3de5"},
	{13, "continuation_finish", "1900ac1507bfc5ed79d7b15f423ff7518ba0289a4af4c73260b4542a33a51f69"},
	{14, "attempt_checkpoint_requests", "d0759c1adb59d9667d328670edb3c86d73fe3e5d5d6ea4043faa64ba53430e91"},
	{15, "continuation_reconciliation_recovery", "ee509040faaad3f26da59f5b0df36c5f0efe4127569c7e8322a60d79c51d9fe8"},
	{16, "blackboard_compatibility_requests", "a4d64b73b7fe0e00a7c8444108aa3cfa2ca0d7df8a7de9f0ac38cd828cfc91cf"},
	{17, "blackboard_compatibility_write_retirement", "5b87dd9b0e12e3c59186f6b029bf4bd301984ff9db869739898c05e4a66eb6cc"},
	{18, "blackboard_compatibility_read_retirement", "d72f68f5aabe6641a86aa56558356ae6c5e46b49221e4aa146248c5e3b155243"},
	{19, "task_soft_deletion", "4ea15e3a0cd4d2e8e564b3ad9768f4a28d0b57a9dbc96a00bec62a71507bc7b4"},
}
