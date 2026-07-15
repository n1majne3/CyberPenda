package store

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestOpenMigrationSourceClassifiesAndValidatesPreNumberedV1ReadOnly(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(*testing.T, *sql.DB)
	}{
		{name: "migration-1 baseline"},
		{name: "earliest projects schema without defaults", mutate: func(t *testing.T, db *sql.DB) {
			if _, err := db.Exec(`ALTER TABLE projects DROP COLUMN defaults_json`); err != nil {
				t.Fatalf("remove optional pre-numbered column: %v", err)
			}
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			path := createFullPreNumberedV1Fixture(t, test.mutate)
			before := captureMigrationSourceFiles(t, path)
			if before.WAL.Exists || before.SHM.Exists {
				t.Fatalf("pre-numbered fixture has sidecars before open: wal=%v shm=%v", before.WAL.Exists, before.SHM.Exists)
			}

			source, err := OpenMigrationSource(path)
			if err != nil {
				t.Fatalf("open pre-numbered migration source: %v", err)
			}
			if source.Classification() != MigrationSourcePreNumberedV1 {
				t.Fatalf("classification = %q, want %q", source.Classification(), MigrationSourcePreNumberedV1)
			}
			if source.CanonicalStore() != CanonicalStoreLegacyV1 {
				t.Fatalf("canonical store = %q, want legacy_v1", source.CanonicalStore())
			}
			if err := source.Validate(context.Background()); err != nil {
				t.Fatalf("revalidate pre-numbered schema fingerprint: %v", err)
			}
			if err := source.ValidateMigrationHistory(context.Background()); !errors.Is(err, ErrMigrationHistoryUnavailable) {
				t.Fatalf("ValidateMigrationHistory error = %v, want ErrMigrationHistoryUnavailable", err)
			}
			if _, err := source.Exec(`CREATE TABLE must_not_write(id TEXT)`); err == nil {
				t.Fatal("pre-numbered migration source accepted a write")
			}
			if err := source.Close(); err != nil {
				t.Fatalf("close pre-numbered migration source: %v", err)
			}

			after := captureMigrationSourceFiles(t, path)
			assertMigrationSourceFilesEqual(t, before, after)
		})
	}
}

func TestOpenMigrationSourceRejectsUnrecognizedOrDamagedPreNumberedSchemaReadOnly(t *testing.T) {
	tests := []struct {
		name string
		make func(*testing.T) string
	}{
		{name: "empty SQLite", make: func(t *testing.T) string {
			path := filepath.Join(t.TempDir(), "empty.db")
			db := openFixtureDB(t, path)
			if _, err := db.Exec(`VACUUM`); err != nil {
				t.Fatalf("materialize empty SQLite: %v", err)
			}
			closeFixtureDB(t, db)
			return path
		}},
		{name: "random user table", make: func(t *testing.T) string {
			path := filepath.Join(t.TempDir(), "random.db")
			db := openFixtureDB(t, path)
			if _, err := db.Exec(`CREATE TABLE notes(id TEXT PRIMARY KEY, body TEXT NOT NULL)`); err != nil {
				t.Fatalf("create random schema: %v", err)
			}
			closeFixtureDB(t, db)
			return path
		}},
		{name: "partial legacy schema", make: func(t *testing.T) string {
			path := filepath.Join(t.TempDir(), "partial.db")
			db := openFixtureDB(t, path)
			if _, err := db.Exec(`CREATE TABLE projects(id TEXT PRIMARY KEY,name TEXT NOT NULL,description TEXT NOT NULL DEFAULT '',scope_json TEXT NOT NULL,created_at TEXT NOT NULL,updated_at TEXT NOT NULL)`); err != nil {
				t.Fatalf("create partial legacy schema: %v", err)
			}
			closeFixtureDB(t, db)
			return path
		}},
		{name: "damaged legacy column", make: func(t *testing.T) string {
			return createFullPreNumberedV1Fixture(t, func(t *testing.T, db *sql.DB) {
				if _, err := db.Exec(`ALTER TABLE projects RENAME COLUMN scope_json TO broken_scope_json`); err != nil {
					t.Fatalf("damage legacy schema: %v", err)
				}
			})
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			path := test.make(t)
			before := captureMigrationSourceFiles(t, path)
			source, err := OpenMigrationSource(path)
			if source != nil {
				_ = source.Close()
				t.Fatal("unrecognized pre-numbered schema returned a migration source")
			}
			if err == nil {
				t.Fatal("unrecognized pre-numbered schema was accepted")
			}
			after := captureMigrationSourceFiles(t, path)
			assertMigrationSourceFilesEqual(t, before, after)
		})
	}
}

func TestOpenMigrationSourceCleansPrivateWALInspectionCopies(t *testing.T) {
	t.Run("successful close", func(t *testing.T) {
		tempRoot := useInspectionTempRoot(t)
		path := createFullPreNumberedV1Fixture(t, nil)
		writer := holdFixtureWriteInWAL(t, path, `UPDATE projects SET name='WAL current'`)
		defer closeFixtureDB(t, writer)

		source, err := OpenMigrationSource(path)
		if err != nil {
			t.Fatalf("open WAL-backed migration source: %v", err)
		}
		if got := inspectionCopyDirs(t, tempRoot); len(got) != 1 {
			_ = source.Close()
			t.Fatalf("inspection copies while source is open = %v, want one", got)
		}
		if err := source.Close(); err != nil {
			t.Fatalf("close WAL-backed migration source: %v", err)
		}
		if got := inspectionCopyDirs(t, tempRoot); len(got) != 0 {
			t.Fatalf("inspection copies after source close = %v, want none", got)
		}
	})

	t.Run("classification failure", func(t *testing.T) {
		tempRoot := useInspectionTempRoot(t)
		path := filepath.Join(t.TempDir(), "unrecognized.db")
		db := openFixtureDB(t, path)
		if _, err := db.Exec(`CREATE TABLE notes(id TEXT PRIMARY KEY, body TEXT NOT NULL)`); err != nil {
			t.Fatalf("create unrecognized source: %v", err)
		}
		closeFixtureDB(t, db)
		writer := holdFixtureWriteInWAL(t, path, `INSERT INTO notes(id,body) VALUES ('n1','WAL current')`)
		defer closeFixtureDB(t, writer)

		source, err := OpenMigrationSource(path)
		if source != nil {
			_ = source.Close()
			t.Fatal("unrecognized WAL-backed schema returned a migration source")
		}
		if err == nil {
			t.Fatal("unrecognized WAL-backed schema was accepted")
		}
		if got := inspectionCopyDirs(t, tempRoot); len(got) != 0 {
			t.Fatalf("inspection copies after classification failure = %v, want none", got)
		}
	})

	t.Run("cleanup error is observable and idempotent", func(t *testing.T) {
		tempRoot := useInspectionTempRoot(t)
		path := createFullPreNumberedV1Fixture(t, nil)
		writer := holdFixtureWriteInWAL(t, path, `UPDATE projects SET name='WAL current'`)
		defer closeFixtureDB(t, writer)
		source, err := OpenMigrationSource(path)
		if err != nil {
			t.Fatalf("open WAL-backed migration source: %v", err)
		}
		copies := inspectionCopyDirs(t, tempRoot)
		if len(copies) != 1 {
			_ = source.Close()
			t.Fatalf("inspection copies while source is open = %v, want one", copies)
		}
		cleanupErr := errors.New("forced inspection cleanup failure")
		source.inspection.removeAll = func(path string) error {
			if path != copies[0] {
				t.Errorf("cleanup path = %q, want %q", path, copies[0])
			}
			return cleanupErr
		}
		if err := source.Close(); !errors.Is(err, cleanupErr) {
			t.Fatalf("first Close error = %v, want cleanup failure", err)
		}
		if err := source.Close(); !errors.Is(err, cleanupErr) {
			t.Fatalf("second Close error = %v, want same cleanup failure", err)
		}
		if err := os.RemoveAll(copies[0]); err != nil {
			t.Fatalf("remove injected-failure inspection copy: %v", err)
		}
	})
}

func useInspectionTempRoot(t *testing.T) string {
	t.Helper()
	root := filepath.Join(t.TempDir(), "inspection-tmp")
	if err := os.Mkdir(root, 0o700); err != nil {
		t.Fatalf("create inspection TMPDIR: %v", err)
	}
	t.Setenv("TMPDIR", root)
	return root
}

func inspectionCopyDirs(t *testing.T, root string) []string {
	t.Helper()
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatalf("read inspection TMPDIR: %v", err)
	}
	var paths []string
	for _, entry := range entries {
		if entry.IsDir() && strings.HasPrefix(entry.Name(), "cyberpenda-store-inspection-") {
			paths = append(paths, filepath.Join(root, entry.Name()))
		}
	}
	return paths
}

func holdFixtureWriteInWAL(t *testing.T, path, write string) *sql.DB {
	t.Helper()
	db := openFixtureDB(t, path)
	var mode string
	if err := db.QueryRow(`PRAGMA journal_mode=WAL`).Scan(&mode); err != nil {
		_ = db.Close()
		t.Fatalf("enable fixture WAL: %v", err)
	}
	if mode != "wal" {
		_ = db.Close()
		t.Fatalf("fixture journal mode = %q, want wal", mode)
	}
	if _, err := db.Exec(`PRAGMA wal_autocheckpoint=0`); err != nil {
		_ = db.Close()
		t.Fatalf("disable fixture WAL autocheckpoint: %v", err)
	}
	if _, err := db.Exec(write); err != nil {
		_ = db.Close()
		t.Fatalf("write fixture WAL: %v", err)
	}
	return db
}

func createFullPreNumberedV1Fixture(t *testing.T, mutate func(*testing.T, *sql.DB)) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "pre-numbered-v1.db")
	db := openFixtureDB(t, path)
	tx, err := db.Begin()
	if err != nil {
		t.Fatalf("begin pre-numbered schema: %v", err)
	}
	if err := execStatements(tx, migration1BaselineSQL); err != nil {
		_ = tx.Rollback()
		t.Fatalf("create pre-numbered schema: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit pre-numbered schema: %v", err)
	}
	if mutate != nil {
		mutate(t, db)
	}
	var mode string
	if err := db.QueryRow(`PRAGMA journal_mode=DELETE`).Scan(&mode); err != nil {
		t.Fatalf("set pre-numbered delete journal: %v", err)
	}
	if mode != "delete" {
		t.Fatalf("pre-numbered journal mode = %q, want delete", mode)
	}
	closeFixtureDB(t, db)
	return path
}

func openFixtureDB(t *testing.T, path string) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("open fixture database: %v", err)
	}
	return db
}

func closeFixtureDB(t *testing.T, db *sql.DB) {
	t.Helper()
	if err := db.Close(); err != nil {
		t.Fatalf("close fixture database: %v", err)
	}
}

type migrationSourceFileImage struct {
	Exists bool
	Bytes  []byte
	SHA256 [sha256.Size]byte
}

type migrationSourceFiles struct {
	Main migrationSourceFileImage
	WAL  migrationSourceFileImage
	SHM  migrationSourceFileImage
}

func captureMigrationSourceFiles(t *testing.T, path string) migrationSourceFiles {
	t.Helper()
	return migrationSourceFiles{
		Main: captureMigrationSourceFile(t, path, true),
		WAL:  captureMigrationSourceFile(t, path+"-wal", false),
		SHM:  captureMigrationSourceFile(t, path+"-shm", false),
	}
}

func captureMigrationSourceFile(t *testing.T, path string, required bool) migrationSourceFileImage {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		if !required && os.IsNotExist(err) {
			return migrationSourceFileImage{}
		}
		t.Fatalf("read migration-source file %s: %v", path, err)
	}
	return migrationSourceFileImage{Exists: true, Bytes: raw, SHA256: sha256.Sum256(raw)}
}

func assertMigrationSourceFilesEqual(t *testing.T, before, after migrationSourceFiles) {
	t.Helper()
	for name, pair := range map[string][2]migrationSourceFileImage{
		"main": {before.Main, after.Main}, "-wal": {before.WAL, after.WAL}, "-shm": {before.SHM, after.SHM},
	} {
		if pair[0].Exists != pair[1].Exists || !bytes.Equal(pair[0].Bytes, pair[1].Bytes) || pair[0].SHA256 != pair[1].SHA256 {
			t.Errorf("migration-source %s file changed: before_exists=%v after_exists=%v before_sha=%x after_sha=%x", name, pair[0].Exists, pair[1].Exists, pair[0].SHA256, pair[1].SHA256)
		}
	}
}
