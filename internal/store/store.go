// Package store owns the SQLite connection and schema migrations shared by
// every domain package. Domain repositories receive the opened database and
// keep their business logic separate from transport concerns so HTTP, MCP, and
// CLI handlers can all call the same services.
package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	_ "modernc.org/sqlite"
)

// memDBSeq makes each empty-path Open use an isolated in-memory database.
var memDBSeq atomic.Uint64

// Canonical store epochs for one SQLite database. Exactly one is active.
const (
	CanonicalStoreLegacyV1         = "legacy_v1"
	CanonicalStoreGraphV1          = "graph_v1"
	CanonicalStoreGraphV1Finalized = "graph_v1_finalized"
	CanonicalStoreBlackboardV2     = "blackboard_v2"
)

// DB wraps a SQLite connection with the daemon's schema applied.
type DB struct {
	*sql.DB
}

// MigrationSource is a read-only Blackboard v1 database opened exclusively
// for the offline v2 migration workflow. Its distinct type cannot be used as
// the daemon's active Store.
type MigrationSource struct {
	*sql.DB
	canonicalStore string
	classification MigrationSourceClassification
	inspection     *readOnlyInspection
}

// MigrationSourceClassification states how an offline v1 source was validated.
type MigrationSourceClassification string

const (
	MigrationSourceNumberedV1    MigrationSourceClassification = "numbered_v1"
	MigrationSourcePreNumberedV1 MigrationSourceClassification = "pre_numbered_v1"
)

// ErrMigrationHistoryUnavailable reports that a pre-numbered source has no
// checksum ledger and was validated through its legacy schema fingerprint.
var ErrMigrationHistoryUnavailable = errors.New("pre-numbered v1 source has no migration history; legacy schema fingerprint was validated")

// CanonicalStore reports the validated v1 epoch without activating it.
func (source *MigrationSource) CanonicalStore() string { return source.canonicalStore }

// Classification reports whether the source has numbered migration history.
func (source *MigrationSource) Classification() MigrationSourceClassification {
	return source.classification
}

func (source *MigrationSource) Close() error {
	if source.inspection != nil {
		return source.inspection.Close()
	}
	return source.DB.Close()
}

// Validate revalidates the source according to its explicit classification.
// Numbered sources validate checksums; pre-numbered sources validate the known
// legacy table/column fingerprint instead of claiming checksum coverage.
func (source *MigrationSource) Validate(ctx context.Context) error {
	switch source.classification {
	case MigrationSourceNumberedV1:
		return ValidateMigrationHistory(ctx, source.DB)
	case MigrationSourcePreNumberedV1:
		return validatePreNumberedLegacySchema(ctx, source.DB)
	default:
		return fmt.Errorf("unknown migration-source classification %q", source.classification)
	}
}

// ValidateMigrationHistory revalidates the source through the read-only
// connection and never applies a numbered migration.
func (source *MigrationSource) ValidateMigrationHistory(ctx context.Context) error {
	if source.classification == MigrationSourcePreNumberedV1 {
		return ErrMigrationHistoryUnavailable
	}
	return ValidateMigrationHistory(ctx, source.DB)
}

// Open connects to the SQLite database at path and runs numbered migrations.
// An empty path uses an in-memory database, which is handy for tests.
func Open(path string) (*DB, error) {
	if err := rejectV1BeforeActiveOpen(path); err != nil {
		return nil, err
	}
	dsn, err := buildDSN(path)
	if err != nil {
		return nil, err
	}

	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	if err := db.Ping(); err != nil {
		_ = db.Close()
		return nil, err
	}
	if err := rejectV1ActiveOpen(db); err != nil {
		_ = db.Close()
		return nil, err
	}

	if err := migrate(db); err != nil {
		_ = db.Close()
		return nil, err
	}

	return &DB{db}, nil
}

// rejectV1BeforeActiveOpen classifies an existing file without letting SQLite
// write to the source. The active DSN enables WAL during Ping, so this check must
// run first or even a refused v1 open can change the database header.
func rejectV1BeforeActiveOpen(path string) (returnErr error) {
	if path == "" || path == ":memory:" {
		return nil
	}
	info, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("inspect existing store path: %w", err)
	}
	if info.Size() == 0 {
		return nil
	}
	inspection, err := openReadOnlyInspection(path)
	if err != nil {
		return err
	}
	defer func() {
		returnErr = joinErrors(returnErr, inspection.Close())
	}()
	return rejectV1ActiveOpen(inspection.db)
}

// OpenWritableMigrationDB opens an existing Blackboard v1 database for the
// offline migrate cutover. Unlike Open, it does not refuse v1 epochs. Unlike
// OpenMigrationSource, the connection is writable so rebuild/cutover can run
// in one transaction. It never flips the store epoch and never activates the
// database for ordinary daemon use.
func OpenWritableMigrationDB(path string) (*DB, error) {
	if path == "" || path == ":memory:" {
		return nil, fmt.Errorf("offline Blackboard migration database must be an existing file-backed database")
	}
	// Classify first through the read-only seam so we never write to an unknown file.
	source, err := OpenMigrationSource(path)
	if err != nil {
		return nil, err
	}
	classification := source.Classification()
	epoch := source.CanonicalStore()
	if err := source.Close(); err != nil {
		return nil, err
	}
	if classification != MigrationSourceNumberedV1 {
		return nil, fmt.Errorf("offline writable migrate requires numbered v1 migration history, got %q", classification)
	}
	switch epoch {
	case CanonicalStoreLegacyV1, CanonicalStoreGraphV1, CanonicalStoreGraphV1Finalized:
	default:
		return nil, fmt.Errorf("offline writable migrate requires a v1 store epoch, got %q", epoch)
	}
	dsn, err := buildDSN(path)
	if err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	if err := db.Ping(); err != nil {
		_ = db.Close()
		return nil, err
	}
	// Confirm the live epoch still matches the read-only classification.
	var liveEpoch string
	if err := db.QueryRow(`SELECT canonical_store FROM blackboard_store_state WHERE id=1`).Scan(&liveEpoch); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("read offline migrator store epoch: %w", err)
	}
	if liveEpoch != epoch {
		_ = db.Close()
		return nil, fmt.Errorf("offline migrator epoch changed during open: classified=%s live=%s", epoch, liveEpoch)
	}
	// Apply additive v2 table migrations without migration 20's epoch flip so
	// rebuild/cutover still sees a v1-authoritative store until the explicit switch.
	if err := applyOfflineV2SchemaWithoutEpochFlip(db); err != nil {
		_ = db.Close()
		return nil, err
	}
	return &DB{db}, nil
}

func applyOfflineV2SchemaWithoutEpochFlip(db *sql.DB) error {
	if _, err := db.Exec(schemaMigrationsDDL); err != nil {
		return fmt.Errorf("ensure schema_migrations for offline migrate: %w", err)
	}
	applied, err := loadAppliedMigrations(db)
	if err != nil {
		return err
	}
	for _, definition := range migrations() {
		if definition.version < 21 {
			continue
		}
		if _, ok := applied[definition.version]; ok {
			continue
		}
		if err := applyMigration(db, definition); err != nil {
			return err
		}
	}
	return nil
}

// OpenMigrationSource opens an existing Blackboard v1 database read-only for
// offline inspection and migration. It never applies migrations or changes the
// canonical Store epoch.
func OpenMigrationSource(path string) (*MigrationSource, error) {
	if path == "" || path == ":memory:" {
		return nil, fmt.Errorf("offline Blackboard migration source must be an existing file-backed database")
	}
	inspection, err := openReadOnlyInspection(path)
	if err != nil {
		return nil, err
	}
	classification, epoch, err := classifyMigrationSource(context.Background(), inspection.db)
	if err != nil {
		return nil, joinErrors(err, inspection.Close())
	}
	return &MigrationSource{DB: inspection.db, canonicalStore: epoch, classification: classification, inspection: inspection}, nil
}

type readOnlyInspection struct {
	db        *sql.DB
	tempDir   string
	removeAll func(string) error
	closeOnce sync.Once
	closeErr  error
}

func (inspection *readOnlyInspection) Close() error {
	inspection.closeOnce.Do(func() {
		dbErr := inspection.db.Close()
		var removeErr error
		if inspection.tempDir != "" {
			if err := inspection.removeAll(inspection.tempDir); err != nil {
				removeErr = fmt.Errorf("remove SQLite inspection directory: %w", err)
			}
		}
		inspection.closeErr = joinErrors(dbErr, removeErr)
	})
	return inspection.closeErr
}

func joinErrors(primary, cleanup error) error {
	switch {
	case primary == nil:
		return cleanup
	case cleanup == nil:
		return primary
	default:
		return errors.Join(primary, cleanup)
	}
}

// openReadOnlyInspection never lets SQLite open the source in a mode that can
// create or modify sidecars. A clean source can be read immutable in place. If
// WAL state exists, main and WAL are copied to a private temporary directory so
// SQLite can reconstruct shared memory and read the current snapshot there.
func openReadOnlyInspection(path string) (*readOnlyInspection, error) {
	hasSidecars, err := sqliteSidecarsExist(path)
	if err != nil {
		return nil, err
	}
	inspectionPath := path
	tempDir := ""
	immutable := true
	if hasSidecars {
		inspectionPath, tempDir, err = copySQLiteInspection(path)
		if err != nil {
			return nil, err
		}
		immutable = false
	}
	dsn, err := buildReadOnlyInspectionDSN(inspectionPath, immutable)
	if err != nil {
		return nil, joinErrors(err, removeInspectionDir(tempDir))
	}
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, joinErrors(err, removeInspectionDir(tempDir))
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	inspection := &readOnlyInspection{db: db, tempDir: tempDir, removeAll: os.RemoveAll}
	if err := db.Ping(); err != nil {
		return nil, joinErrors(fmt.Errorf("inspect existing store read-only: %w", err), inspection.Close())
	}
	return inspection, nil
}

func sqliteSidecarsExist(path string) (bool, error) {
	for _, suffix := range []string{"-wal", "-shm"} {
		if _, err := os.Stat(path + suffix); err == nil {
			return true, nil
		} else if !os.IsNotExist(err) {
			return false, fmt.Errorf("inspect SQLite sidecar %s: %w", suffix, err)
		}
	}
	return false, nil
}

func copySQLiteInspection(path string) (string, string, error) {
	dir, err := os.MkdirTemp("", "cyberpenda-store-inspection-*")
	if err != nil {
		return "", "", fmt.Errorf("create SQLite inspection directory: %w", err)
	}
	target := filepath.Join(dir, "source.db")
	if err := copySQLiteFile(path, target, false); err != nil {
		return "", "", joinErrors(err, removeInspectionDir(dir))
	}
	if err := copySQLiteFile(path+"-wal", target+"-wal", true); err != nil {
		return "", "", joinErrors(err, removeInspectionDir(dir))
	}
	return target, dir, nil
}

func removeInspectionDir(dir string) error {
	if dir == "" {
		return nil
	}
	if err := os.RemoveAll(dir); err != nil {
		return fmt.Errorf("remove SQLite inspection directory: %w", err)
	}
	return nil
}

func copySQLiteFile(sourcePath, targetPath string, optional bool) error {
	source, err := os.Open(sourcePath)
	if err != nil {
		if optional && os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("open SQLite inspection source %s: %w", filepath.Base(sourcePath), err)
	}
	defer source.Close()
	target, err := os.OpenFile(targetPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("create SQLite inspection copy %s: %w", filepath.Base(targetPath), err)
	}
	if _, err := io.Copy(target, source); err != nil {
		_ = target.Close()
		return fmt.Errorf("copy SQLite inspection file %s: %w", filepath.Base(sourcePath), err)
	}
	if err := target.Close(); err != nil {
		return fmt.Errorf("close SQLite inspection copy %s: %w", filepath.Base(targetPath), err)
	}
	return nil
}

func classifyMigrationSource(ctx context.Context, db *sql.DB) (MigrationSourceClassification, string, error) {
	var hasMigrations, hasEpoch int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='schema_migrations'`).Scan(&hasMigrations); err != nil {
		return "", "", fmt.Errorf("inspect migration-source history table: %w", err)
	}
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='blackboard_store_state'`).Scan(&hasEpoch); err != nil {
		return "", "", fmt.Errorf("inspect migration-source epoch table: %w", err)
	}
	switch {
	case hasMigrations == 1 && hasEpoch == 1:
		if err := ValidateMigrationHistory(ctx, db); err != nil {
			return "", "", err
		}
		var epoch string
		if err := db.QueryRowContext(ctx, `SELECT canonical_store FROM blackboard_store_state WHERE id=1`).Scan(&epoch); err != nil {
			return "", "", fmt.Errorf("read migration-source store epoch: %w", err)
		}
		switch epoch {
		case CanonicalStoreLegacyV1, CanonicalStoreGraphV1, CanonicalStoreGraphV1Finalized:
			return MigrationSourceNumberedV1, epoch, nil
		default:
			return "", "", fmt.Errorf("offline Blackboard migration source requires a v1 store epoch, got %q", epoch)
		}
	case hasMigrations == 0 && hasEpoch == 0:
		if err := validatePreNumberedLegacySchema(ctx, db); err != nil {
			return "", "", err
		}
		return MigrationSourcePreNumberedV1, CanonicalStoreLegacyV1, nil
	default:
		return "", "", fmt.Errorf("offline Blackboard migration source has incomplete numbered-v1 metadata: schema_migrations=%d blackboard_store_state=%d", hasMigrations, hasEpoch)
	}
}

type schemaColumnFingerprint struct {
	Type       string
	NotNull    int
	PrimaryKey int
}

type schemaFingerprint map[string]map[string]schemaColumnFingerprint

func validatePreNumberedLegacySchema(ctx context.Context, db *sql.DB) error {
	expected, err := migration1BaselineFingerprint(ctx)
	if err != nil {
		return fmt.Errorf("build known pre-numbered v1 schema fingerprint: %w", err)
	}
	actual, err := readSchemaFingerprint(ctx, db)
	if err != nil {
		return fmt.Errorf("read pre-numbered v1 schema fingerprint: %w", err)
	}
	if len(actual) != len(expected) {
		return fmt.Errorf("pre-numbered v1 schema fingerprint mismatch: got %d user tables, want %d", len(actual), len(expected))
	}
	optionalMissing := map[string]map[string]bool{
		"projects": {"defaults_json": true},
	}
	for table, expectedColumns := range expected {
		actualColumns, ok := actual[table]
		if !ok {
			return fmt.Errorf("pre-numbered v1 schema fingerprint mismatch: required table %q is missing", table)
		}
		for column, got := range actualColumns {
			want, ok := expectedColumns[column]
			if !ok {
				return fmt.Errorf("pre-numbered v1 schema fingerprint mismatch: table %q has unknown column %q", table, column)
			}
			if got != want {
				return fmt.Errorf("pre-numbered v1 schema fingerprint mismatch: table %q column %q = %+v, want %+v", table, column, got, want)
			}
		}
		for column := range expectedColumns {
			if _, ok := actualColumns[column]; ok {
				continue
			}
			if optionalMissing[table][column] {
				continue
			}
			return fmt.Errorf("pre-numbered v1 schema fingerprint mismatch: table %q column %q is missing", table, column)
		}
	}
	for table := range actual {
		if _, ok := expected[table]; !ok {
			return fmt.Errorf("pre-numbered v1 schema fingerprint mismatch: unknown user table %q", table)
		}
	}
	return nil
}

func migration1BaselineFingerprint(ctx context.Context) (schemaFingerprint, error) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	defer db.Close()
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	if err := execStatements(tx, migration1BaselineSQL); err != nil {
		_ = tx.Rollback()
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return readSchemaFingerprint(ctx, db)
}

func readSchemaFingerprint(ctx context.Context, db migrationHistoryQueryer) (schemaFingerprint, error) {
	rows, err := db.QueryContext(ctx, `SELECT name FROM sqlite_master WHERE type='table' AND lower(name) NOT GLOB 'sqlite_*' ORDER BY name`)
	if err != nil {
		return nil, err
	}
	var tables []string
	for rows.Next() {
		var table string
		if err := rows.Scan(&table); err != nil {
			_ = rows.Close()
			return nil, err
		}
		tables = append(tables, table)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return nil, err
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}

	fingerprint := make(schemaFingerprint, len(tables))
	for _, table := range tables {
		quotedTable := strings.ReplaceAll(table, `"`, `""`)
		columnRows, err := db.QueryContext(ctx, `PRAGMA table_info("`+quotedTable+`")`)
		if err != nil {
			return nil, err
		}
		columns := map[string]schemaColumnFingerprint{}
		for columnRows.Next() {
			var cid, notNull, primaryKey int
			var name, columnType string
			var defaultValue any
			if err := columnRows.Scan(&cid, &name, &columnType, &notNull, &defaultValue, &primaryKey); err != nil {
				_ = columnRows.Close()
				return nil, err
			}
			columns[name] = schemaColumnFingerprint{
				Type: strings.ToUpper(strings.TrimSpace(columnType)), NotNull: notNull, PrimaryKey: primaryKey,
			}
		}
		if err := columnRows.Err(); err != nil {
			_ = columnRows.Close()
			return nil, err
		}
		if err := columnRows.Close(); err != nil {
			return nil, err
		}
		fingerprint[table] = columns
	}
	return fingerprint, nil
}

func rejectV1ActiveOpen(db *sql.DB) error {
	var hasStoreState int
	if err := db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='blackboard_store_state'`).Scan(&hasStoreState); err != nil {
		return fmt.Errorf("inspect canonical store epoch: %w", err)
	}
	if hasStoreState == 0 {
		schemaObjects, err := countUserSchemaObjects(db)
		if err != nil {
			return fmt.Errorf("inspect existing Store schema: %w", err)
		}
		if schemaObjects == 0 {
			return nil
		}
		var hasLegacySchema int
		if err := db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name IN ('projects','project_facts','blackboard_graph_state')`).Scan(&hasLegacySchema); err != nil {
			return fmt.Errorf("inspect legacy Blackboard schema: %w", err)
		}
		if hasLegacySchema > 0 {
			return fmt.Errorf("blackboard v1 store epoch %q cannot be opened for daemon/runtime use; stop the daemon and run 'blackboard v2 inspect', 'blackboard v2 migrate', then 'blackboard v2 verify'", CanonicalStoreLegacyV1)
		}
		return unknownCanonicalStoreEpochError("")
	}
	var epoch string
	if err := db.QueryRow(`SELECT canonical_store FROM blackboard_store_state WHERE id=1`).Scan(&epoch); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return unknownCanonicalStoreEpochError("")
		}
		return fmt.Errorf("read canonical store epoch: %w", err)
	}
	switch epoch {
	case CanonicalStoreLegacyV1, CanonicalStoreGraphV1, CanonicalStoreGraphV1Finalized:
		return fmt.Errorf("blackboard v1 store epoch %q cannot be opened for daemon/runtime use; stop the daemon and run 'blackboard v2 inspect', 'blackboard v2 migrate', then 'blackboard v2 verify'", epoch)
	case CanonicalStoreBlackboardV2:
		if err := ValidateMigrationHistory(context.Background(), db); err != nil {
			return fmt.Errorf("validate blackboard_v2 migration history: %w", err)
		}
		var requiredMigrations int
		if err := db.QueryRow(`SELECT COUNT(*) FROM schema_migrations WHERE version BETWEEN 1 AND 20`).Scan(&requiredMigrations); err != nil {
			return fmt.Errorf("inspect blackboard_v2 required migrations: %w", err)
		}
		if requiredMigrations != 20 {
			return fmt.Errorf("blackboard_v2 store is missing one or more required migrations 1-20; refusing daemon/runtime use")
		}
		return nil
	default:
		return unknownCanonicalStoreEpochError(epoch)
	}
}

func unknownCanonicalStoreEpochError(epoch string) error {
	return fmt.Errorf("unknown canonical store epoch %q cannot be opened for daemon/runtime use; restore a supported database or use an explicit offline migration workflow", epoch)
}

func countUserSchemaObjects(db *sql.DB) (int, error) {
	var count int
	err := db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE lower(name) NOT GLOB 'sqlite_*'`).Scan(&count)
	return count, err
}

// CanonicalStore returns the global store epoch for this database.
func (db *DB) CanonicalStore() (string, error) {
	var epoch string
	err := db.QueryRow(`SELECT canonical_store FROM blackboard_store_state WHERE id = 1`).Scan(&epoch)
	if err != nil {
		return "", fmt.Errorf("read canonical store epoch: %w", err)
	}
	return epoch, nil
}

type BlackboardStoreState struct {
	CanonicalStore         string
	CutoverState           string
	CutoverID              string
	LatestVerificationHash string
}

func (db *DB) BlackboardStoreState() (BlackboardStoreState, error) {
	var state BlackboardStoreState
	err := db.QueryRow(`SELECT canonical_store,cutover_state,cutover_id,latest_verification_result_hash FROM blackboard_store_state WHERE id=1`).Scan(&state.CanonicalStore, &state.CutoverState, &state.CutoverID, &state.LatestVerificationHash)
	if err != nil {
		return BlackboardStoreState{}, fmt.Errorf("read Blackboard store state: %w", err)
	}
	return state, nil
}

// ValidateMigrationHistory verifies that every applied numbered migration is
// known to this binary and still has its recorded checksum. It performs no
// writes and is used by pre-cutover inspection.
func (db *DB) ValidateMigrationHistory(ctx context.Context) error {
	return ValidateMigrationHistory(ctx, db)
}

type migrationHistoryQueryer interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}

// ValidateMigrationHistory performs the same read-only check through a caller
// supplied transaction when inspection needs one consistent SQLite snapshot.
func ValidateMigrationHistory(ctx context.Context, queryer migrationHistoryQueryer) error {
	rows, err := queryer.QueryContext(ctx, `SELECT version, name, checksum FROM schema_migrations ORDER BY version ASC`)
	if err != nil {
		return fmt.Errorf("read schema_migrations: %w", err)
	}
	defer rows.Close()

	known := make(map[int]migration)
	for _, definition := range migrations() {
		known[definition.version] = definition
	}
	for rows.Next() {
		var version int
		var name, checksum string
		if err := rows.Scan(&version, &name, &checksum); err != nil {
			return fmt.Errorf("scan schema_migrations: %w", err)
		}
		definition, ok := known[version]
		if !ok {
			return fmt.Errorf("database schema is newer/unknown: applied migration version %d (%s) is not known to this binary", version, name)
		}
		if checksum != definition.checksum {
			return fmt.Errorf("migration checksum mismatch for version %d (%s)", version, definition.name)
		}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterate schema_migrations: %w", err)
	}
	return nil
}

func buildDSN(path string) (string, error) {
	values := url.Values{}
	values.Add("_pragma", "busy_timeout(5000)")
	values.Add("_pragma", "foreign_keys(1)")
	values.Add("_pragma", "synchronous(FULL)")
	values.Set("_txlock", "immediate")

	if path == "" || path == ":memory:" {
		// Unique shared-memory name per Open so concurrent tests stay isolated
		// while still allowing the pool to reopen the same image.
		name := "pentest_mem_" + strconv.FormatUint(memDBSeq.Add(1), 10)
		return "file:" + name + "?mode=memory&cache=shared&" + values.Encode(), nil
	}

	values.Add("_pragma", "journal_mode(WAL)")

	// Prefer a file URI so query parameters always attach cleanly. Absolute
	// paths become file:/abs/path; relative paths become file:rel/path.
	escaped := url.PathEscape(path)
	// PathEscape encodes slashes; restore them for a usable filesystem path.
	escaped = strings.ReplaceAll(escaped, "%2F", "/")
	return "file:" + escaped + "?" + values.Encode(), nil
}

func buildReadOnlyInspectionDSN(path string, immutable bool) (string, error) {
	values := url.Values{}
	values.Set("mode", "ro")
	if immutable {
		values.Set("immutable", "1")
	}
	values.Add("_pragma", "query_only(1)")
	escaped := strings.ReplaceAll(url.PathEscape(path), "%2F", "/")
	return "file:" + escaped + "?" + values.Encode(), nil
}

type migration struct {
	version  int
	name     string
	checksum string
	up       func(tx *sql.Tx) error
}

func migrate(db *sql.DB) error {
	schemaObjects, err := countUserSchemaObjects(db)
	if err != nil {
		return fmt.Errorf("inspect Store schema before migrations: %w", err)
	}
	if schemaObjects == 0 {
		return bootstrapFresh(db, migrations())
	}
	if _, err := db.Exec(schemaMigrationsDDL); err != nil {
		return fmt.Errorf("ensure schema_migrations: %w", err)
	}

	applied, err := loadAppliedMigrations(db)
	if err != nil {
		return err
	}

	defs := migrations()
	byVersion := make(map[int]migration, len(defs))
	for _, m := range defs {
		byVersion[m.version] = m
	}

	for version, row := range applied {
		def, ok := byVersion[version]
		if !ok {
			return fmt.Errorf("database schema is newer/unknown: applied migration version %d (%s) is not known to this binary", version, row.name)
		}
		if row.checksum != def.checksum {
			return fmt.Errorf("migration checksum mismatch for version %d (%s): database has %s, binary expects %s", version, def.name, row.checksum, def.checksum)
		}
	}

	for _, m := range defs {
		if _, ok := applied[m.version]; ok {
			continue
		}
		if err := applyMigration(db, m); err != nil {
			return err
		}
	}
	return nil
}

const schemaMigrationsDDL = `
CREATE TABLE IF NOT EXISTS schema_migrations (
	version INTEGER PRIMARY KEY,
	name TEXT NOT NULL,
	checksum TEXT NOT NULL,
	applied_at TEXT NOT NULL
);
`

func bootstrapFresh(db *sql.DB, definitions []migration) error {
	tx, err := db.Begin()
	if err != nil {
		return fmt.Errorf("begin fresh Store bootstrap: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.Exec(schemaMigrationsDDL); err != nil {
		return fmt.Errorf("create fresh schema_migrations: %w", err)
	}
	for _, definition := range definitions {
		if err := definition.up(tx); err != nil {
			return fmt.Errorf("apply fresh migration %d (%s): %w", definition.version, definition.name, err)
		}
		if _, err := tx.Exec(
			`INSERT INTO schema_migrations (version, name, checksum, applied_at) VALUES (?, ?, ?, ?)`,
			definition.version,
			definition.name,
			definition.checksum,
			time.Now().UTC().Format(time.RFC3339Nano),
		); err != nil {
			return fmt.Errorf("record fresh migration %d: %w", definition.version, err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit fresh Store bootstrap: %w", err)
	}
	return nil
}

type appliedMigration struct {
	name     string
	checksum string
}

func loadAppliedMigrations(db *sql.DB) (map[int]appliedMigration, error) {
	rows, err := db.Query(`SELECT version, name, checksum FROM schema_migrations ORDER BY version ASC`)
	if err != nil {
		return nil, fmt.Errorf("read schema_migrations: %w", err)
	}
	defer rows.Close()

	out := make(map[int]appliedMigration)
	for rows.Next() {
		var version int
		var name, checksum string
		if err := rows.Scan(&version, &name, &checksum); err != nil {
			return nil, err
		}
		out[version] = appliedMigration{name: name, checksum: checksum}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

func applyMigration(db *sql.DB, m migration) error {
	tx, err := db.Begin()
	if err != nil {
		return fmt.Errorf("begin migration %d: %w", m.version, err)
	}
	defer func() { _ = tx.Rollback() }()

	if err := m.up(tx); err != nil {
		return fmt.Errorf("apply migration %d (%s): %w", m.version, m.name, err)
	}

	_, err = tx.Exec(
		`INSERT INTO schema_migrations (version, name, checksum, applied_at) VALUES (?, ?, ?, ?)`,
		m.version, m.name, m.checksum, time.Now().UTC().Format(time.RFC3339Nano),
	)
	if err != nil {
		return fmt.Errorf("record migration %d: %w", m.version, err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit migration %d: %w", m.version, err)
	}
	return nil
}

func migrations() []migration {
	return []migration{
		newMigration(1, "baseline_legacy_schema", migration1BaselineSQL, migration1Up),
		newMigration(2, "store_epoch_and_graph_support", migration2SQL, migration2Up),
		newMigration(3, "graph_ledger_core", migration3SQL, migration3Up),
		newMigration(4, "graph_edges", migration4SQL, migration4Up),
		newMigration(5, "graph_edge_ledger_guards", migration5SQL, migration5Up),
		newMigration(6, "graph_edge_version_endpoints", migration6SQL, migration6Up),
		newMigration(7, "graph_edge_identity_and_integrity_cutover", migration7SQL, migration7Up),
		newMigration(8, "graph_budget_compaction_and_health", migration8SQL, migration8Up),
		newMigration(9, "blackboard_read_cursor_secret", migration9SQL, migration9Up),
		newMigration(10, "blackboard_health_run_requests", migration10SQL, migration10Up),
		newMigration(11, "continuation_interface_grants", migration11SQL, migration11Up),
		newMigration(12, "project_interface_evidence_requests", migration12SQL, migration12Up),
		newMigration(13, "continuation_finish", migration13SQL, migration13Up),
		newMigration(14, "attempt_checkpoint_requests", migration14SQL, migration14Up),
		newMigration(15, "continuation_reconciliation_recovery", migration15SQL, migration15Up),
		newMigration(16, "blackboard_compatibility_requests", migration16SQL, migration16Up),
		newMigration(17, "blackboard_compatibility_write_retirement", migration17SQL, migration17Up),
		newMigration(18, "blackboard_compatibility_read_retirement", migration18SQL, migration18Up),
		newMigration(19, "task_soft_deletion", migration19SQL, migration19Up),
		newMigration(20, "blackboard_v2_store_epoch", migration20SQL, migration20Up),
		newMigration(21, "blackboard_v2_semantic_facts", migration21SQL, migration21Up),
		newMigration(22, "blackboard_v2_current_relationships", migration22SQL, migration22Up),
		newMigration(23, "blackboard_v2_attempt_ownership", migration23SQL, migration23Up),
		newMigration(24, "blackboard_v2_evidence_requests", migration24SQL, migration24Up),
		newMigration(25, "blackboard_v2_evidence_payload_claims", migration25SQL, migration25Up),
		newMigration(26, "blackboard_v2_evidence_publisher_claims", migration26SQL, migration26Up),
		newMigration(27, "blackboard_v2_private_evidence_staging", migration27SQL, migration27Up),
		newMigration(28, "blackboard_v2_fixed_evidence_staging_scope", migration28SQL, migration28Up),
		newMigration(29, "blackboard_v2_key_redirects", migration29SQL, migration29Up),
		newMigration(30, "blackboard_v2_continuation_snapshots", migration30SQL, migration30Up),
		newMigration(31, "blackboard_v2_continuation_finish", migration31SQL, migration31Up),
		newMigration(32, "blackboard_v2_sync_delivery_receipts", migration32SQL, migration32Up),
	}
}

// migration32 installs request-scoped sync delivery receipts. Each
// (continuation_id, request_fingerprint) preserves its own attachment so
// response-loss retries redeliver the original sync even after later
// deliveries. At most one open claim per Continuation reserves the pending
// notice before the trusted action runs.
const migration32SQL = `
CREATE TABLE IF NOT EXISTS blackboard_v2_sync_delivery_receipts (
	continuation_id TEXT NOT NULL REFERENCES blackboard_v2_continuation_pins(continuation_id) ON DELETE RESTRICT,
	request_fingerprint TEXT NOT NULL,
	status TEXT NOT NULL CHECK (status IN ('claimed', 'finalized')),
	from_revision INTEGER NOT NULL,
	revision INTEGER NOT NULL DEFAULT 0,
	attachment_json TEXT NOT NULL DEFAULT '',
	working_snapshot_bytes BLOB NOT NULL DEFAULT x'',
	created_at TEXT NOT NULL,
	updated_at TEXT NOT NULL,
	PRIMARY KEY (continuation_id, request_fingerprint),
	CHECK (
		(status = 'claimed' AND revision = 0 AND attachment_json = '')
		OR (status = 'finalized' AND attachment_json <> '')
	)
);
CREATE UNIQUE INDEX IF NOT EXISTS ux_blackboard_v2_sync_delivery_open_claim
	ON blackboard_v2_sync_delivery_receipts (continuation_id)
	WHERE status = 'claimed';
`

func migration32Up(tx *sql.Tx) error {
	return execStatements(tx, migration32SQL)
}

const migration31SQL = `
CREATE TABLE IF NOT EXISTS blackboard_v2_continuation_finishes (
	continuation_id TEXT PRIMARY KEY REFERENCES blackboard_v2_continuation_pins(continuation_id) ON DELETE RESTRICT,
	project_id TEXT NOT NULL REFERENCES projects(id) ON DELETE RESTRICT,
	idempotency_key TEXT NOT NULL,
	request_hash TEXT NOT NULL CHECK (length(request_hash) = 64),
	result_json TEXT NOT NULL,
	finished_at TEXT NOT NULL,
	UNIQUE (project_id, idempotency_key)
);
`

func migration31Up(tx *sql.Tx) error {
	if err := execStatements(tx, migration31SQL); err != nil {
		return err
	}
	for _, statement := range []string{
		`CREATE TRIGGER IF NOT EXISTS blackboard_v2_continuation_finishes_no_update BEFORE UPDATE ON blackboard_v2_continuation_finishes BEGIN SELECT RAISE(ABORT, 'Blackboard v2 Finish receipt is immutable'); END`,
		`CREATE TRIGGER IF NOT EXISTS blackboard_v2_continuation_finishes_no_delete BEFORE DELETE ON blackboard_v2_continuation_finishes BEGIN SELECT RAISE(ABORT, 'Blackboard v2 Finish receipt is immutable'); END`,
	} {
		if _, err := tx.Exec(statement); err != nil {
			return fmt.Errorf("install Blackboard v2 Finish receipt guard: %w", err)
		}
	}
	return nil
}

const migration30SQL = `
CREATE TABLE IF NOT EXISTS blackboard_v2_continuation_pins (
	continuation_id TEXT PRIMARY KEY REFERENCES task_continuations(id) ON DELETE RESTRICT,
	snapshot_schema TEXT NOT NULL CHECK (snapshot_schema = 'runtime-blackboard/v2'),
	snapshot_revision INTEGER NOT NULL CHECK (snapshot_revision >= 0),
	snapshot_bytes BLOB NOT NULL CHECK (length(snapshot_bytes) > 0),
	integrity_sha256 TEXT NOT NULL CHECK (length(integrity_sha256) = 64),
	created_at TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS blackboard_v2_continuation_state (
	continuation_id TEXT PRIMARY KEY REFERENCES blackboard_v2_continuation_pins(continuation_id) ON DELETE RESTRICT,
	last_acknowledged_revision INTEGER NOT NULL CHECK (last_acknowledged_revision >= 0),
	working_snapshot_bytes BLOB NOT NULL CHECK (length(working_snapshot_bytes) > 0),
	updated_at TEXT NOT NULL
);
`

func migration30Up(tx *sql.Tx) error {
	if err := execStatements(tx, migration30SQL); err != nil {
		return err
	}
	for _, statement := range []string{
		`CREATE TRIGGER IF NOT EXISTS blackboard_v2_continuation_pins_no_update BEFORE UPDATE ON blackboard_v2_continuation_pins BEGIN SELECT RAISE(ABORT, 'Blackboard v2 Launch Pin is immutable'); END`,
		`CREATE TRIGGER IF NOT EXISTS blackboard_v2_continuation_pins_no_delete BEFORE DELETE ON blackboard_v2_continuation_pins BEGIN SELECT RAISE(ABORT, 'Blackboard v2 Launch Pin is immutable'); END`,
		`CREATE TRIGGER IF NOT EXISTS blackboard_v2_continuation_state_monotonic BEFORE UPDATE ON blackboard_v2_continuation_state WHEN NEW.continuation_id <> OLD.continuation_id OR NEW.last_acknowledged_revision < OLD.last_acknowledged_revision BEGIN SELECT RAISE(ABORT, 'Blackboard v2 acknowledged revision cannot move backwards'); END`,
	} {
		if _, err := tx.Exec(statement); err != nil {
			return fmt.Errorf("install Blackboard v2 Continuation snapshot guard: %w", err)
		}
	}
	return nil
}

const migration29SQL = `
CREATE TABLE IF NOT EXISTS blackboard_v2_key_redirects (
	project_id TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
	source_key TEXT NOT NULL,
	canonical_key TEXT NOT NULL,
	created_at TEXT NOT NULL,
	PRIMARY KEY (project_id, source_key),
	CHECK (source_key <> canonical_key)
);
CREATE INDEX IF NOT EXISTS idx_blackboard_v2_key_redirects_canonical
	ON blackboard_v2_key_redirects (project_id, canonical_key);
`

func migration29Up(tx *sql.Tx) error { return execStatements(tx, migration29SQL) }

const migration28SQL = `SELECT 1;`

func migration28Up(tx *sql.Tx) error {
	if err := ensureColumn(tx, "blackboard_v2_evidence_requests", "migration27_temp_internal_path", "TEXT NOT NULL DEFAULT ''"); err != nil {
		return fmt.Errorf("ensure blackboard_v2_evidence_requests.migration27_temp_internal_path: %w", err)
	}
	rows, err := tx.Query(`SELECT project_id,continuation_id,idempotency_key,request_hash,managed_internal_path,temp_internal_path,migration27_temp_internal_path FROM blackboard_v2_evidence_requests`)
	if err != nil {
		return fmt.Errorf("read Evidence staging paths for migration 28: %w", err)
	}
	type stagingRow struct {
		projectID, continuationID, key, requestHash, managedPath, tempPath, migration27Path string
	}
	var stagingRows []stagingRow
	for rows.Next() {
		var row stagingRow
		if err := rows.Scan(&row.projectID, &row.continuationID, &row.key, &row.requestHash, &row.managedPath, &row.tempPath, &row.migration27Path); err != nil {
			_ = rows.Close()
			return fmt.Errorf("scan Evidence staging path for migration 28: %w", err)
		}
		stagingRows = append(stagingRows, row)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return fmt.Errorf("iterate Evidence staging paths for migration 28: %w", err)
	}
	if err := rows.Close(); err != nil {
		return fmt.Errorf("close Evidence staging paths for migration 28: %w", err)
	}
	for _, row := range stagingRows {
		fixedPath, err := fixedEvidenceStagingPath(row.managedPath, row.continuationID, row.key, row.requestHash)
		if err != nil {
			return fmt.Errorf("derive fixed Evidence staging path: %w", err)
		}
		if row.tempPath == fixedPath {
			continue
		}
		migration27Path := row.migration27Path
		if migration27Path == "" {
			migration27Path = row.tempPath
		}
		if _, err := tx.Exec(`UPDATE blackboard_v2_evidence_requests SET migration27_temp_internal_path=?,temp_internal_path=? WHERE project_id=? AND continuation_id=? AND idempotency_key=?`, migration27Path, fixedPath, row.projectID, row.continuationID, row.key); err != nil {
			return fmt.Errorf("migrate fixed Evidence staging path: %w", err)
		}
	}
	return execStatements(tx, migration28SQL)
}

func fixedEvidenceStagingPath(managedPath, continuationID, key, requestHash string) (string, error) {
	marker := string(filepath.Separator) + "retained" + string(filepath.Separator)
	index := strings.Index(managedPath, marker)
	if index <= 0 || len(requestHash) != 64 {
		return "", fmt.Errorf("invalid retained Evidence path")
	}
	scope := sha256.Sum256([]byte(continuationID + "\x00" + key))
	return filepath.Join(managedPath[:index], ".evidence-staging", hex.EncodeToString(scope[:]), requestHash), nil
}

const migration27SQL = `
UPDATE blackboard_v2_evidence_requests
SET previous_temp_internal_path = CASE
		WHEN previous_temp_internal_path = '' THEN temp_internal_path
		ELSE previous_temp_internal_path
	END,
	temp_internal_path =
		substr(managed_internal_path,1,instr(managed_internal_path,'/retained/')-1) ||
		'/.evidence-staging/' || lower(hex(continuation_id)) || '/' ||
		lower(hex(idempotency_key)) || '/' || request_hash
WHERE instr(managed_internal_path,'/retained/') > 0
	AND temp_internal_path <>
		substr(managed_internal_path,1,instr(managed_internal_path,'/retained/')-1) ||
		'/.evidence-staging/' || lower(hex(continuation_id)) || '/' ||
		lower(hex(idempotency_key)) || '/' || request_hash;
`

func migration27Up(tx *sql.Tx) error {
	if err := ensureColumn(tx, "blackboard_v2_evidence_requests", "previous_temp_internal_path", "TEXT NOT NULL DEFAULT ''"); err != nil {
		return fmt.Errorf("ensure blackboard_v2_evidence_requests.previous_temp_internal_path: %w", err)
	}
	return execStatements(tx, migration27SQL)
}

const migration26SQL = `SELECT 1;`

func migration26Up(tx *sql.Tx) error {
	for _, column := range []struct {
		name       string
		definition string
	}{
		{name: "publisher_token", definition: "TEXT NOT NULL DEFAULT ''"},
		{name: "publisher_temp_identity", definition: "TEXT NOT NULL DEFAULT ''"},
	} {
		if err := ensureColumn(tx, "blackboard_v2_evidence_requests", column.name, column.definition); err != nil {
			return fmt.Errorf("ensure blackboard_v2_evidence_requests.%s: %w", column.name, err)
		}
	}
	return execStatements(tx, migration26SQL)
}

const migration25SQL = `
UPDATE blackboard_v2_evidence_requests
SET temp_internal_path = managed_internal_path || '.stage-' || substr(request_hash,1,24)
WHERE temp_internal_path = '';
CREATE TABLE IF NOT EXISTS blackboard_v2_evidence_payloads (
	project_id TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
	managed_internal_path TEXT NOT NULL,
	sha256 TEXT NOT NULL,
	size_bytes INTEGER NOT NULL CHECK (size_bytes >= 0),
	state TEXT NOT NULL CHECK (state IN ('active','gc')),
	gc_continuation_id TEXT NOT NULL DEFAULT '',
	gc_idempotency_key TEXT NOT NULL DEFAULT '',
	created_at TEXT NOT NULL,
	updated_at TEXT NOT NULL,
	PRIMARY KEY (project_id, managed_internal_path),
	CHECK (state = 'active' OR (gc_continuation_id <> '' AND gc_idempotency_key <> ''))
);
INSERT OR IGNORE INTO blackboard_v2_evidence_payloads(
	project_id,managed_internal_path,sha256,size_bytes,state,created_at,updated_at
)
SELECT project_id,managed_internal_path,source_sha256,source_size_bytes,'active',created_at,updated_at
FROM blackboard_v2_evidence_requests;
`

func migration25Up(tx *sql.Tx) error {
	if err := ensureColumn(tx, "blackboard_v2_evidence_requests", "temp_internal_path", "TEXT NOT NULL DEFAULT ''"); err != nil {
		return fmt.Errorf("ensure blackboard_v2_evidence_requests.temp_internal_path: %w", err)
	}
	return execStatements(tx, migration25SQL)
}

const migration24SQL = `
CREATE TABLE IF NOT EXISTS blackboard_v2_evidence_requests (
	project_id TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
	continuation_id TEXT NOT NULL REFERENCES task_continuations(id) ON DELETE RESTRICT,
	idempotency_key TEXT NOT NULL,
	request_hash TEXT NOT NULL,
	source_identity TEXT NOT NULL,
	source_sha256 TEXT NOT NULL,
	source_size_bytes INTEGER NOT NULL CHECK (source_size_bytes >= 0),
	managed_internal_path TEXT NOT NULL,
	payload_owned INTEGER NOT NULL DEFAULT 0 CHECK (payload_owned IN (0,1)),
	status TEXT NOT NULL CHECK (status IN ('reserved','published','completed')),
	result_json TEXT NOT NULL DEFAULT '',
	created_at TEXT NOT NULL,
	updated_at TEXT NOT NULL,
	PRIMARY KEY (project_id, continuation_id, idempotency_key)
);
CREATE INDEX IF NOT EXISTS idx_blackboard_v2_evidence_requests_managed_path
	ON blackboard_v2_evidence_requests (project_id, managed_internal_path);
`

func migration24Up(tx *sql.Tx) error { return execStatements(tx, migration24SQL) }

const migration23SQL = `
CREATE TABLE IF NOT EXISTS blackboard_v2_attempt_origins (
	project_id TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
	key TEXT NOT NULL,
	continuation_id TEXT NOT NULL REFERENCES task_continuations(id) ON DELETE RESTRICT,
	created_at TEXT NOT NULL,
	PRIMARY KEY (project_id, key)
);
CREATE INDEX IF NOT EXISTS idx_blackboard_v2_attempt_origins_continuation
	ON blackboard_v2_attempt_origins (continuation_id);
`

func migration23Up(tx *sql.Tx) error {
	if err := ensureColumn(tx, "blackboard_v2_idempotency_receipts", "continuation_id", "TEXT NOT NULL DEFAULT ''"); err != nil {
		return fmt.Errorf("ensure blackboard_v2_idempotency_receipts.continuation_id: %w", err)
	}
	return execStatements(tx, migration23SQL)
}

const migration22SQL = `
CREATE TABLE IF NOT EXISTS blackboard_v2_relationships (
	project_id TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
	from_key TEXT NOT NULL,
	relation TEXT NOT NULL CHECK (relation IN ('about','part_of','tests','produced','evidences','supports','contradicts','derived_from','depends_on','satisfies','supersedes')),
	to_key TEXT NOT NULL,
	version INTEGER NOT NULL CHECK (version >= 1),
	reason TEXT NOT NULL DEFAULT '',
	created_at TEXT NOT NULL,
	updated_at TEXT NOT NULL,
	PRIMARY KEY (project_id, from_key, relation, to_key)
);
CREATE TABLE IF NOT EXISTS blackboard_v2_relationship_history (
	project_id TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
	from_key TEXT NOT NULL,
	relation TEXT NOT NULL CHECK (relation IN ('about','part_of','tests','produced','evidences','supports','contradicts','derived_from','depends_on','satisfies','supersedes')),
	to_key TEXT NOT NULL,
	version INTEGER NOT NULL CHECK (version >= 1),
	reason TEXT NOT NULL DEFAULT '',
	recorded_at TEXT NOT NULL,
	PRIMARY KEY (project_id, from_key, relation, to_key, version)
);
`

func migration22Up(tx *sql.Tx) error { return execStatements(tx, migration22SQL) }

const migration21SQL = `
CREATE TABLE IF NOT EXISTS blackboard_v2_project_state (
	project_id TEXT PRIMARY KEY REFERENCES projects(id) ON DELETE CASCADE,
	revision INTEGER NOT NULL CHECK (revision >= 0)
);
CREATE TABLE IF NOT EXISTS blackboard_v2_records (
	project_id TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
	key TEXT NOT NULL,
	type TEXT NOT NULL CHECK (type IN ('entity','objective','attempt','fact','finding','solution','evidence')),
	version INTEGER NOT NULL CHECK (version >= 1),
	record_json TEXT NOT NULL,
	created_at TEXT NOT NULL,
	updated_at TEXT NOT NULL,
	PRIMARY KEY (project_id, key)
);
CREATE TABLE IF NOT EXISTS blackboard_v2_record_history (
	project_id TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
	key TEXT NOT NULL,
	version INTEGER NOT NULL CHECK (version >= 1),
	type TEXT NOT NULL CHECK (type IN ('entity','objective','attempt','fact','finding','solution','evidence')),
	record_json TEXT NOT NULL,
	recorded_at TEXT NOT NULL,
	PRIMARY KEY (project_id, key, version)
);
CREATE TABLE IF NOT EXISTS blackboard_v2_idempotency_receipts (
	project_id TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
	idempotency_key TEXT NOT NULL,
	request_hash TEXT NOT NULL,
	result_json TEXT NOT NULL,
	created_at TEXT NOT NULL,
	PRIMARY KEY (project_id, idempotency_key)
);
`

func migration21Up(tx *sql.Tx) error { return execStatements(tx, migration21SQL) }

const migration20SQL = `
UPDATE blackboard_store_state
SET canonical_store = 'blackboard_v2',
    cutover_state = 'v2',
    migration_contract_version = 'blackboard_v2',
    graph_schema_version = 0,
    updated_at = '1970-01-01T00:00:00Z'
WHERE id = 1;
`

func migration20Up(tx *sql.Tx) error { return execStatements(tx, migration20SQL) }

const migration19SQL = `-- tasks.deleted_at TEXT NOT NULL DEFAULT ''`

func migration19Up(tx *sql.Tx) error {
	if err := ensureColumn(tx, "tasks", "deleted_at", "TEXT NOT NULL DEFAULT ''"); err != nil {
		return fmt.Errorf("ensure tasks.deleted_at: %w", err)
	}
	return nil
}

const migration18SQL = `
CREATE TABLE blackboard_compatibility_read_retirement (
 id INTEGER PRIMARY KEY CHECK (id = 1),
 retired_at TEXT NOT NULL,
 bundled_web_cli_projections_only INTEGER NOT NULL CHECK (bundled_web_cli_projections_only = 1),
 observation_waived INTEGER NOT NULL CHECK (observation_waived IN (0,1)),
 waiver_operator_id TEXT NOT NULL DEFAULT '',
 waiver_reason TEXT NOT NULL DEFAULT '',
 CHECK (observation_waived = 0 OR (waiver_operator_id <> '' AND waiver_reason <> ''))
);
`

func migration18Up(tx *sql.Tx) error { return execStatements(tx, migration18SQL) }

const migration17SQL = `
CREATE TABLE blackboard_compatibility_write_retirement (
 id INTEGER PRIMARY KEY CHECK (id = 1),
 retired_at TEXT NOT NULL,
 graph_native_stable_releases INTEGER NOT NULL,
 bundled_runtime_v1_only INTEGER NOT NULL CHECK (bundled_runtime_v1_only = 1),
 replacement_docs_ready INTEGER NOT NULL CHECK (replacement_docs_ready = 1),
 observation_waived INTEGER NOT NULL CHECK (observation_waived IN (0,1)),
 waiver_operator_id TEXT NOT NULL DEFAULT '',
 waiver_reason TEXT NOT NULL DEFAULT '',
 CHECK (observation_waived = 0 OR (waiver_operator_id <> '' AND waiver_reason <> ''))
);
`

func migration17Up(tx *sql.Tx) error { return execStatements(tx, migration17SQL) }

const migration16SQL = `
CREATE TABLE blackboard_compatibility_requests (
 project_id TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
 idempotency_scope TEXT NOT NULL,
 idempotency_key TEXT NOT NULL,
 call_kind TEXT NOT NULL,
 request_hash TEXT NOT NULL,
 translated_request_json TEXT NOT NULL,
 created_at TEXT NOT NULL,
 PRIMARY KEY(project_id,idempotency_scope,idempotency_key)
);
CREATE TABLE blackboard_compatibility_use (
 project_id TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
 transport TEXT NOT NULL,
 call_kind TEXT NOT NULL,
 use_mode TEXT NOT NULL CHECK (use_mode IN ('read','write')),
 use_count INTEGER NOT NULL,
 last_used_at TEXT NOT NULL,
 PRIMARY KEY(project_id,transport,call_kind,use_mode)
);
CREATE TABLE blackboard_compatibility_results (
 project_id TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
 idempotency_scope TEXT NOT NULL,
 idempotency_key TEXT NOT NULL,
 call_kind TEXT NOT NULL,
 request_hash TEXT NOT NULL,
 payload_json TEXT NOT NULL,
 mutation_json TEXT NOT NULL,
 created_at TEXT NOT NULL,
 PRIMARY KEY(project_id,idempotency_scope,idempotency_key)
);
CREATE TABLE blackboard_compatibility_task_summaries (
 project_id TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
 idempotency_scope TEXT NOT NULL,
 idempotency_key TEXT NOT NULL,
 request_hash TEXT NOT NULL,
 task_id TEXT NOT NULL REFERENCES tasks(id) ON DELETE CASCADE,
 result_json TEXT NOT NULL,
 created_at TEXT NOT NULL,
 PRIMARY KEY(project_id,idempotency_scope,idempotency_key)
);
`

func migration16Up(tx *sql.Tx) error { return execStatements(tx, migration16SQL) }

const migration15SQL = `
CREATE INDEX IF NOT EXISTS idx_blackboard_graph_mutations_maintenance_recovery
 ON blackboard_graph_mutations (project_id,mutation_kind,maintenance_subject_id,mutation_seq DESC);
`

func migration15Up(tx *sql.Tx) error { return execStatements(tx, migration15SQL) }

// migration14SQL persists the Task Event chosen for one checkpoint request
// before graph Apply runs. An empty result_json means the Event is durable and
// the same request must resume/replay Apply on retry.
const migration14SQL = `
CREATE TABLE blackboard_attempt_checkpoint_requests (
 project_id TEXT NOT NULL,
 continuation_id TEXT NOT NULL,
 idempotency_key TEXT NOT NULL,
 request_hash TEXT NOT NULL,
 event_id TEXT NOT NULL,
 attempt_node_id TEXT NOT NULL,
 result_json TEXT NOT NULL DEFAULT '',
 created_at TEXT NOT NULL,
 updated_at TEXT NOT NULL,
 PRIMARY KEY(project_id,continuation_id,idempotency_key),
 UNIQUE(event_id)
);
`

func migration14Up(tx *sql.Tx) error { return execStatements(tx, migration14SQL) }

// migration13SQL adds the durable Finish marker owned by the Task domain. The
// finish request identity and graph position live with the Continuation-bound
// Task Summary Version so an exact replay can be returned after the grant is
// write-closed.
const migration13SQL = `
-- task_summary_versions.objective_outcome_json TEXT NOT NULL DEFAULT ''
-- task_summary_versions.blackboard_graph_revision INTEGER NOT NULL DEFAULT 0
-- task_summary_versions.blackboard_mutation_sequence INTEGER NOT NULL DEFAULT 0
-- task_summary_versions.finish_idempotency_key TEXT NOT NULL DEFAULT ''
-- task_summary_versions.finish_request_hash TEXT NOT NULL DEFAULT ''
-- task_continuations.blackboard_finish_summary_version_id TEXT NOT NULL DEFAULT ''
-- task_continuations.blackboard_finish_graph_revision INTEGER NOT NULL DEFAULT 0
-- task_continuations.blackboard_finish_mutation_sequence INTEGER NOT NULL DEFAULT 0
-- task_continuations.blackboard_finished_at TEXT NOT NULL DEFAULT ''
CREATE UNIQUE INDEX IF NOT EXISTS ux_task_summary_versions_continuation_finish
	ON task_summary_versions (continuation_id, finish_idempotency_key)
	WHERE continuation_id IS NOT NULL AND continuation_id <> '' AND finish_idempotency_key <> '';
`

func migration13Up(tx *sql.Tx) error {
	for _, column := range []struct {
		table, name, definition string
	}{
		{"task_summary_versions", "objective_outcome_json", "TEXT NOT NULL DEFAULT ''"},
		{"task_summary_versions", "blackboard_graph_revision", "INTEGER NOT NULL DEFAULT 0"},
		{"task_summary_versions", "blackboard_mutation_sequence", "INTEGER NOT NULL DEFAULT 0"},
		{"task_summary_versions", "finish_idempotency_key", "TEXT NOT NULL DEFAULT ''"},
		{"task_summary_versions", "finish_request_hash", "TEXT NOT NULL DEFAULT ''"},
		{"task_continuations", "blackboard_finish_summary_version_id", "TEXT NOT NULL DEFAULT ''"},
		{"task_continuations", "blackboard_finish_graph_revision", "INTEGER NOT NULL DEFAULT 0"},
		{"task_continuations", "blackboard_finish_mutation_sequence", "INTEGER NOT NULL DEFAULT 0"},
		{"task_continuations", "blackboard_finished_at", "TEXT NOT NULL DEFAULT ''"},
	} {
		if err := ensureColumn(tx, column.table, column.name, column.definition); err != nil {
			return fmt.Errorf("ensure %s.%s: %w", column.table, column.name, err)
		}
	}
	return execStatements(tx, migration13SQL)
}

// migration12SQL stores the cross-domain Retain Evidence saga checkpoint. It
// contains metadata and exact result JSON only; artifact payload bytes remain
// under the managed Artifact Root.
const migration12SQL = `
CREATE TABLE blackboard_interface_requests (
 project_id TEXT NOT NULL,
 idempotency_scope TEXT NOT NULL,
 request_kind TEXT NOT NULL,
 idempotency_key TEXT NOT NULL,
 request_hash TEXT NOT NULL,
 source_identity TEXT NOT NULL,
 source_sha256 TEXT NOT NULL,
 source_size_bytes INTEGER NOT NULL,
 status TEXT NOT NULL CHECK(status IN ('reserved','published','completed')),
 managed_path TEXT NOT NULL DEFAULT '',
 result_json TEXT NOT NULL DEFAULT '',
 created_at TEXT NOT NULL,
 updated_at TEXT NOT NULL,
 PRIMARY KEY(project_id,idempotency_scope,request_kind,idempotency_key)
);
`

func migration12Up(tx *sql.Tx) error { return execStatements(tx, migration12SQL) }

const migration10SQL = `
CREATE TABLE blackboard_health_run_requests (
 project_id TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
 idempotency_key TEXT NOT NULL,
 request_hash TEXT NOT NULL,
 run_id TEXT NOT NULL,
 created_at TEXT NOT NULL,
 PRIMARY KEY(project_id,idempotency_key),
 FOREIGN KEY(project_id,run_id) REFERENCES blackboard_health_runs(project_id,run_id) ON DELETE CASCADE
);
`

func migration10Up(tx *sql.Tx) error {
	if err := ensureColumn(tx, "blackboard_health_runs", "run_status", "TEXT NOT NULL DEFAULT 'completed'"); err != nil {
		return fmt.Errorf("ensure blackboard_health_runs.run_status: %w", err)
	}
	if err := ensureColumn(tx, "blackboard_health_runs", "overall", "TEXT NOT NULL DEFAULT 'unknown'"); err != nil {
		return fmt.Errorf("ensure blackboard_health_runs.overall: %w", err)
	}
	if err := ensureColumn(tx, "blackboard_health_runs", "artifact_scan_fingerprint", "TEXT NOT NULL DEFAULT ''"); err != nil {
		return fmt.Errorf("ensure blackboard_health_runs.artifact_scan_fingerprint: %w", err)
	}
	if _, err := tx.Exec(`UPDATE blackboard_health_runs SET overall=status,run_status=CASE WHEN completed_at IS NULL THEN 'running' WHEN status='unknown' THEN 'failed' ELSE 'completed' END`); err != nil {
		return fmt.Errorf("backfill Health run lifecycle: %w", err)
	}
	return execStatements(tx, migration10SQL)
}

// migration11SQL creates the Continuation Interface Grant store (runtime
// protocol §4.1). The server stores only a SHA-256 hash of the cryptographically
// random bearer token; the plaintext is projected solely to the task-local
// Runtime environment and trusted MCP configuration. Grant lifecycle timestamps
// gate new writes while reads and exact replay remain allowed after finish,
// revocation, or a terminal Continuation (runtime protocol §4.2).
const migration11SQL = `
CREATE TABLE blackboard_continuation_grants (
 grant_id TEXT PRIMARY KEY,
 token_hash TEXT NOT NULL UNIQUE,
 project_id TEXT NOT NULL,
 task_id TEXT NOT NULL,
 continuation_id TEXT NOT NULL,
 runtime_config_version_id TEXT NOT NULL,
 runtime_profile_id TEXT NOT NULL,
 runtime_plugin_id TEXT NOT NULL,
 runner TEXT NOT NULL,
 actor_id TEXT NOT NULL,
 issued_at TEXT NOT NULL,
 finished_at TEXT NOT NULL DEFAULT '',
 revoked_at TEXT NOT NULL DEFAULT '',
 terminal_at TEXT NOT NULL DEFAULT '',
 CHECK (token_hash <> '')
);
CREATE INDEX idx_blackboard_continuation_grants_continuation
	ON blackboard_continuation_grants (continuation_id);
`

func migration11Up(tx *sql.Tx) error { return execStatements(tx, migration11SQL) }

const migration9SQL = `
CREATE TABLE blackboard_read_state (
 id INTEGER PRIMARY KEY CHECK(id=1),
 cursor_secret BLOB NOT NULL CHECK(length(cursor_secret)=32)
);
INSERT INTO blackboard_read_state(id,cursor_secret) VALUES(1,randomblob(32));
`

func migration9Up(tx *sql.Tx) error { return execStatements(tx, migration9SQL) }

const migration4SQL = `
CREATE TABLE blackboard_edges (
 project_id TEXT NOT NULL, id TEXT NOT NULL, edge_type TEXT NOT NULL,
 from_node_id TEXT NOT NULL, to_node_id TEXT NOT NULL,
 created_mutation_seq INTEGER NOT NULL, created_operation_index INTEGER NOT NULL, created_at TEXT NOT NULL,
 PRIMARY KEY(project_id,id), UNIQUE(project_id,edge_type,from_node_id,to_node_id),
 FOREIGN KEY(project_id,from_node_id) REFERENCES blackboard_nodes(project_id,id),
 FOREIGN KEY(project_id,to_node_id) REFERENCES blackboard_nodes(project_id,id),
 FOREIGN KEY(project_id,created_mutation_seq,created_operation_index) REFERENCES blackboard_graph_operations(project_id,mutation_seq,operation_index));
CREATE TABLE blackboard_edge_versions (
 project_id TEXT NOT NULL, edge_id TEXT NOT NULL, version INTEGER NOT NULL, result_graph_revision INTEGER NOT NULL,
 mutation_seq INTEGER NOT NULL, operation_index INTEGER NOT NULL, state TEXT NOT NULL CHECK(state IN ('active','retired')),
 summary TEXT NOT NULL DEFAULT '', semantic_hash TEXT NOT NULL, updated_at TEXT NOT NULL,
 PRIMARY KEY(project_id,edge_id,version), FOREIGN KEY(project_id,edge_id) REFERENCES blackboard_edges(project_id,id),
 FOREIGN KEY(project_id,mutation_seq,operation_index) REFERENCES blackboard_graph_operations(project_id,mutation_seq,operation_index));
CREATE TABLE blackboard_edge_heads (
 project_id TEXT NOT NULL, edge_id TEXT NOT NULL, edge_type TEXT NOT NULL, from_node_id TEXT NOT NULL, to_node_id TEXT NOT NULL,
 version INTEGER NOT NULL, graph_revision INTEGER NOT NULL, state TEXT NOT NULL CHECK(state IN ('active','retired')), semantic_hash TEXT NOT NULL,
 PRIMARY KEY(project_id,edge_id), FOREIGN KEY(project_id,edge_id,version) REFERENCES blackboard_edge_versions(project_id,edge_id,version));
CREATE INDEX idx_blackboard_edge_heads_from ON blackboard_edge_heads(project_id,from_node_id,state,edge_type);
CREATE INDEX idx_blackboard_edge_heads_to ON blackboard_edge_heads(project_id,to_node_id,state,edge_type);
`

func migration4Up(tx *sql.Tx) error { return execStatements(tx, migration4SQL) }

const migration5SQL = `-- C07 append-only guards for edge identity and version history.`

func migration5Up(tx *sql.Tx) error {
	for _, table := range []string{"blackboard_edges", "blackboard_edge_versions"} {
		for _, stmt := range []string{
			`CREATE TRIGGER IF NOT EXISTS ` + table + `_no_update BEFORE UPDATE ON ` + table + ` BEGIN SELECT RAISE(ABORT, '` + table + ` is append-only'); END`,
			`CREATE TRIGGER IF NOT EXISTS ` + table + `_no_delete BEFORE DELETE ON ` + table + ` BEGIN SELECT RAISE(ABORT, '` + table + ` is append-only'); END`,
		} {
			if _, err := tx.Exec(stmt); err != nil {
				return fmt.Errorf("install edge ledger guard for %s: %w", table, err)
			}
		}
	}
	return nil
}

const migration6SQL = `-- C07 full edge post-images carry endpoint IDs on every version.`

func migration6Up(tx *sql.Tx) error {
	// Migration 5 protects existing ledgers. Temporarily remove only the update
	// guard while the new post-image columns are backfilled, then restore it.
	if _, err := tx.Exec(`DROP TRIGGER IF EXISTS blackboard_edge_versions_no_update`); err != nil {
		return fmt.Errorf("drop edge-version update guard for backfill: %w", err)
	}
	if err := ensureColumn(tx, "blackboard_edge_versions", "from_node_id", "TEXT"); err != nil {
		return fmt.Errorf("ensure edge version from_node_id: %w", err)
	}
	if err := ensureColumn(tx, "blackboard_edge_versions", "to_node_id", "TEXT"); err != nil {
		return fmt.Errorf("ensure edge version to_node_id: %w", err)
	}
	if _, err := tx.Exec(`UPDATE blackboard_edge_versions
		SET from_node_id=(SELECT from_node_id FROM blackboard_edges e WHERE e.project_id=blackboard_edge_versions.project_id AND e.id=blackboard_edge_versions.edge_id),
		    to_node_id=(SELECT to_node_id FROM blackboard_edges e WHERE e.project_id=blackboard_edge_versions.project_id AND e.id=blackboard_edge_versions.edge_id)
		WHERE from_node_id IS NULL OR to_node_id IS NULL`); err != nil {
		return fmt.Errorf("backfill edge-version endpoints: %w", err)
	}
	for _, stmt := range []string{
		`CREATE TRIGGER blackboard_edge_versions_no_update BEFORE UPDATE ON blackboard_edge_versions BEGIN SELECT RAISE(ABORT, 'blackboard_edge_versions is append-only'); END`,
		`CREATE TRIGGER blackboard_edge_versions_require_endpoints BEFORE INSERT ON blackboard_edge_versions WHEN NEW.from_node_id IS NULL OR NEW.to_node_id IS NULL BEGIN SELECT RAISE(ABORT, 'blackboard_edge_versions endpoints are required'); END`,
	} {
		if _, err := tx.Exec(stmt); err != nil {
			return fmt.Errorf("install edge-version endpoint guard: %w", err)
		}
	}
	return nil
}

const migration7SQL = `
CREATE TABLE blackboard_edges_new (
 project_id TEXT NOT NULL, id TEXT NOT NULL, edge_type TEXT NOT NULL,
 created_mutation_seq INTEGER NOT NULL, created_operation_index INTEGER NOT NULL, created_at TEXT NOT NULL,
 PRIMARY KEY(project_id,id), UNIQUE(id),
 FOREIGN KEY(project_id,created_mutation_seq,created_operation_index) REFERENCES blackboard_graph_operations(project_id,mutation_seq,operation_index));
CREATE TABLE blackboard_edge_versions_new (
 project_id TEXT NOT NULL, edge_id TEXT NOT NULL, version INTEGER NOT NULL, result_graph_revision INTEGER NOT NULL,
 mutation_seq INTEGER NOT NULL, operation_index INTEGER NOT NULL,
 from_node_id TEXT NOT NULL, to_node_id TEXT NOT NULL,
 state TEXT NOT NULL CHECK(state IN ('active','retired')), summary TEXT NOT NULL DEFAULT '', semantic_hash TEXT NOT NULL, updated_at TEXT NOT NULL,
 PRIMARY KEY(project_id,edge_id,version),
 FOREIGN KEY(project_id,edge_id) REFERENCES blackboard_edges_new(project_id,id),
 FOREIGN KEY(project_id,from_node_id) REFERENCES blackboard_nodes(project_id,id),
 FOREIGN KEY(project_id,to_node_id) REFERENCES blackboard_nodes(project_id,id),
 FOREIGN KEY(project_id,mutation_seq,operation_index) REFERENCES blackboard_graph_operations(project_id,mutation_seq,operation_index));
CREATE TABLE blackboard_edge_heads_new (
 project_id TEXT NOT NULL, edge_id TEXT NOT NULL, edge_type TEXT NOT NULL, from_node_id TEXT NOT NULL, to_node_id TEXT NOT NULL,
 version INTEGER NOT NULL, graph_revision INTEGER NOT NULL, state TEXT NOT NULL CHECK(state IN ('active','retired')), semantic_hash TEXT NOT NULL,
 CHECK(state <> 'active' OR from_node_id <> to_node_id),
 PRIMARY KEY(project_id,edge_id), FOREIGN KEY(project_id,edge_id,version) REFERENCES blackboard_edge_versions_new(project_id,edge_id,version));
INSERT INTO blackboard_edges_new(project_id,id,edge_type,created_mutation_seq,created_operation_index,created_at)
 SELECT project_id,id,edge_type,created_mutation_seq,created_operation_index,created_at FROM blackboard_edges;
INSERT INTO blackboard_edge_versions_new(project_id,edge_id,version,result_graph_revision,mutation_seq,operation_index,from_node_id,to_node_id,state,summary,semantic_hash,updated_at)
 SELECT project_id,edge_id,version,result_graph_revision,mutation_seq,operation_index,from_node_id,to_node_id,state,summary,semantic_hash,updated_at FROM blackboard_edge_versions;
INSERT INTO blackboard_edge_heads_new(project_id,edge_id,edge_type,from_node_id,to_node_id,version,graph_revision,state,semantic_hash)
 SELECT project_id,edge_id,edge_type,from_node_id,to_node_id,version,graph_revision,state,semantic_hash FROM blackboard_edge_heads;
DROP TABLE blackboard_edge_heads;
DROP TABLE blackboard_edge_versions;
DROP TABLE blackboard_edges;
ALTER TABLE blackboard_edges_new RENAME TO blackboard_edges;
ALTER TABLE blackboard_edge_versions_new RENAME TO blackboard_edge_versions;
ALTER TABLE blackboard_edge_heads_new RENAME TO blackboard_edge_heads;
CREATE INDEX idx_blackboard_edge_heads_from ON blackboard_edge_heads(project_id,from_node_id,state,edge_type);
CREATE INDEX idx_blackboard_edge_heads_to ON blackboard_edge_heads(project_id,to_node_id,state,edge_type);
CREATE UNIQUE INDEX ux_blackboard_edge_heads_active_tuple ON blackboard_edge_heads(project_id,edge_type,from_node_id,to_node_id) WHERE state='active';
CREATE TABLE blackboard_graph_integrity_cutovers (
 project_id TEXT PRIMARY KEY REFERENCES projects(id) ON DELETE RESTRICT,
 legacy_through_mutation_seq INTEGER NOT NULL CHECK(legacy_through_mutation_seq >= 0));
INSERT INTO blackboard_graph_integrity_cutovers(project_id,legacy_through_mutation_seq)
 SELECT project_id,MAX(mutation_seq) FROM blackboard_graph_mutations GROUP BY project_id;
CREATE VIEW blackboard_graph_legacy_current_records AS
 SELECT DISTINCT p.project_id,o.mutation_seq,1 AS record_kind,json_array(p.id) AS record_identity,
  json_object('project_id',p.project_id,'id',p.id,'actor_type',p.actor_type,'actor_id',p.actor_id,'task_id',p.task_id,'continuation_id',p.continuation_id,'runtime_profile_id',p.runtime_profile_id,'runner',p.runner,'migration_source_json',p.migration_source_json,'recorded_at',p.recorded_at) AS record_json
 FROM blackboard_graph_provenance p JOIN blackboard_graph_operations o ON o.project_id=p.project_id AND o.provenance_id=p.id
 UNION ALL
 SELECT pe.project_id,o.mutation_seq,8,json_array(pe.provenance_id,pe.ordinal),
  json_object('project_id',pe.project_id,'provenance_id',pe.provenance_id,'ordinal',pe.ordinal,'event_id',pe.event_id)
 FROM blackboard_graph_provenance_events pe JOIN blackboard_graph_operations o ON o.project_id=pe.project_id AND o.provenance_id=pe.provenance_id
 UNION ALL
 SELECT o.project_id,o.mutation_seq,2,json_array(o.operation_index),
  json_object('project_id',o.project_id,'mutation_seq',o.mutation_seq,'operation_index',o.operation_index,'op_id',o.op_id,'operation_kind',o.operation_kind,'operation_json',o.operation_json,'result_json',o.result_json,'changed',o.changed,'provenance_id',o.provenance_id)
 FROM blackboard_graph_operations o
 UNION ALL
 SELECT n.project_id,n.created_mutation_seq,3,json_array(n.id),
  json_object('project_id',n.project_id,'id',n.id,'node_type',n.node_type,'original_stable_key',n.original_stable_key,'created_mutation_seq',n.created_mutation_seq,'created_operation_index',n.created_operation_index,'created_at',n.created_at)
 FROM blackboard_nodes n
 UNION ALL
 SELECT v.project_id,v.mutation_seq,4,json_array(v.node_id,v.version),
  json_object('project_id',v.project_id,'node_id',v.node_id,'version',v.version,'result_graph_revision',v.result_graph_revision,'mutation_seq',v.mutation_seq,'operation_index',v.operation_index,'schema_version',v.schema_version,'disposition',v.disposition,'merge_target_id',v.merge_target_id,'properties_json',v.properties_json,'semantic_hash',v.semantic_hash,'updated_at',v.updated_at)
 FROM blackboard_node_versions v
 UNION ALL
 SELECT e.project_id,e.created_mutation_seq,5,json_array(e.id),
  json_object('project_id',e.project_id,'id',e.id,'edge_type',e.edge_type,'created_mutation_seq',e.created_mutation_seq,'created_operation_index',e.created_operation_index,'created_at',e.created_at)
 FROM blackboard_edges e
 UNION ALL
 SELECT v.project_id,v.mutation_seq,6,json_array(v.edge_id,v.version),
  json_object('project_id',v.project_id,'edge_id',v.edge_id,'version',v.version,'result_graph_revision',v.result_graph_revision,'mutation_seq',v.mutation_seq,'operation_index',v.operation_index,'from_node_id',v.from_node_id,'to_node_id',v.to_node_id,'state',v.state,'summary',v.summary,'semantic_hash',v.semantic_hash,'updated_at',v.updated_at)
 FROM blackboard_edge_versions v
 UNION ALL
 SELECT k.project_id,k.mutation_seq,7,json_array(k.node_type,k.key,k.key_version),
  json_object('project_id',k.project_id,'node_type',k.node_type,'key',k.key,'key_version',k.key_version,'role',k.role,'source_node_id',k.source_node_id,'canonical_node_id',k.canonical_node_id,'legacy_nonconforming',k.legacy_nonconforming,'result_graph_revision',k.result_graph_revision,'mutation_seq',k.mutation_seq,'operation_index',k.operation_index,'semantic_hash',k.semantic_hash)
 FROM blackboard_key_events k;
CREATE TABLE blackboard_graph_legacy_record_anchors (
 project_id TEXT NOT NULL, mutation_seq INTEGER NOT NULL, record_kind INTEGER NOT NULL, record_identity TEXT NOT NULL, record_json TEXT NOT NULL,
 PRIMARY KEY(project_id,mutation_seq,record_kind,record_identity));
INSERT INTO blackboard_graph_legacy_record_anchors(project_id,mutation_seq,record_kind,record_identity,record_json)
 SELECT r.project_id,r.mutation_seq,r.record_kind,r.record_identity,r.record_json
 FROM blackboard_graph_legacy_current_records r
 JOIN blackboard_graph_integrity_cutovers c ON c.project_id=r.project_id AND r.mutation_seq<=c.legacy_through_mutation_seq;
`

func migration7Up(tx *sql.Tx) error {
	if err := execStatements(tx, migration7SQL); err != nil {
		return err
	}
	for _, stmt := range []string{
		`CREATE TRIGGER blackboard_edges_no_update BEFORE UPDATE ON blackboard_edges BEGIN SELECT RAISE(ABORT, 'blackboard_edges is append-only'); END`,
		`CREATE TRIGGER blackboard_edges_no_delete BEFORE DELETE ON blackboard_edges BEGIN SELECT RAISE(ABORT, 'blackboard_edges is append-only'); END`,
		`CREATE TRIGGER blackboard_edge_versions_no_update BEFORE UPDATE ON blackboard_edge_versions BEGIN SELECT RAISE(ABORT, 'blackboard_edge_versions is append-only'); END`,
		`CREATE TRIGGER blackboard_edge_versions_no_delete BEFORE DELETE ON blackboard_edge_versions BEGIN SELECT RAISE(ABORT, 'blackboard_edge_versions is append-only'); END`,
		`CREATE TRIGGER blackboard_edge_versions_require_endpoints BEFORE INSERT ON blackboard_edge_versions WHEN NEW.from_node_id IS NULL OR NEW.to_node_id IS NULL BEGIN SELECT RAISE(ABORT, 'blackboard_edge_versions endpoints are required'); END`,
		`CREATE TRIGGER blackboard_graph_legacy_record_anchors_no_update BEFORE UPDATE ON blackboard_graph_legacy_record_anchors BEGIN SELECT RAISE(ABORT, 'blackboard_graph_legacy_record_anchors is append-only'); END`,
		`CREATE TRIGGER blackboard_graph_legacy_record_anchors_no_delete BEFORE DELETE ON blackboard_graph_legacy_record_anchors BEGIN SELECT RAISE(ABORT, 'blackboard_graph_legacy_record_anchors is append-only'); END`,
	} {
		if _, err := tx.Exec(stmt); err != nil {
			return fmt.Errorf("install canonical edge ledger guard: %w", err)
		}
	}
	return nil
}

const migration8SQL = `
CREATE TABLE blackboard_projection_metrics (
  project_id TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
  graph_revision INTEGER NOT NULL,
  projection_hash TEXT NOT NULL,
  renderer_version TEXT NOT NULL,
  estimator_version TEXT NOT NULL,
  projection_bytes INTEGER NOT NULL,
  estimated_tokens INTEGER NOT NULL,
  budget_state TEXT NOT NULL CHECK (budget_state IN ('within_target','above_target','warning','required','unknown')),
  measured_at TEXT NOT NULL,
  PRIMARY KEY(project_id, graph_revision)
);
CREATE TABLE blackboard_compactions (
  project_id TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
  manifest_id TEXT NOT NULL UNIQUE,
  base_graph_revision INTEGER NOT NULL,
  result_graph_revision INTEGER NOT NULL,
  before_hash TEXT NOT NULL,
  after_hash TEXT NOT NULL,
  before_bytes INTEGER NOT NULL,
  after_bytes INTEGER NOT NULL,
  before_tokens INTEGER NOT NULL,
  after_tokens INTEGER NOT NULL,
  expected_versions_json TEXT NOT NULL,
  archived_node_ids_json TEXT NOT NULL,
  retired_edge_ids_json TEXT NOT NULL,
  preserved_anchor_ids_json TEXT NOT NULL,
  rationale_json TEXT NOT NULL,
  mutation_id TEXT NOT NULL,
  created_at TEXT NOT NULL,
  PRIMARY KEY(project_id, manifest_id)
);
CREATE TABLE blackboard_restore_manifests (
  project_id TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
  manifest_id TEXT NOT NULL UNIQUE,
  base_graph_revision INTEGER NOT NULL,
  result_graph_revision INTEGER NOT NULL,
  restored_node_ids_json TEXT NOT NULL,
  restored_edge_ids_json TEXT NOT NULL,
  before_hash TEXT NOT NULL,
  after_hash TEXT NOT NULL,
  before_tokens INTEGER NOT NULL,
  after_tokens INTEGER NOT NULL,
  mutation_id TEXT NOT NULL,
  created_at TEXT NOT NULL,
  PRIMARY KEY(project_id, manifest_id)
);
CREATE TABLE blackboard_health_runs (
  project_id TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
  run_id TEXT NOT NULL UNIQUE,
  checked_graph_revision INTEGER NOT NULL,
  checked_state_hash TEXT NOT NULL,
  checked_projection_hash TEXT NOT NULL,
  checker_version TEXT NOT NULL,
  status TEXT NOT NULL CHECK (status IN ('healthy','attention','degraded','critical','unknown')),
  artifact_scan_status TEXT NOT NULL,
  started_at TEXT NOT NULL,
  completed_at TEXT,
  metrics_json TEXT NOT NULL,
  PRIMARY KEY(project_id, run_id)
);
CREATE TABLE blackboard_health_results (
  project_id TEXT NOT NULL,
  run_id TEXT NOT NULL,
  fingerprint TEXT NOT NULL,
  code TEXT NOT NULL,
  severity TEXT NOT NULL CHECK (severity IN ('info','warning','critical')),
  subject_kind TEXT NOT NULL,
  subject_id TEXT NOT NULL,
  details_json TEXT NOT NULL,
  PRIMARY KEY(project_id, run_id, fingerprint),
  FOREIGN KEY(project_id, run_id) REFERENCES blackboard_health_runs(project_id, run_id) ON DELETE CASCADE
);
`

func migration8Up(tx *sql.Tx) error {
	if err := execStatements(tx, migration8SQL); err != nil {
		return err
	}
	for _, statement := range []string{
		`CREATE TRIGGER blackboard_compactions_no_update BEFORE UPDATE ON blackboard_compactions BEGIN SELECT RAISE(ABORT, 'blackboard compaction manifests are append-only'); END`,
		`CREATE TRIGGER blackboard_compactions_no_delete BEFORE DELETE ON blackboard_compactions BEGIN SELECT RAISE(ABORT, 'blackboard compaction manifests are append-only'); END`,
		`CREATE TRIGGER blackboard_restore_manifests_no_update BEFORE UPDATE ON blackboard_restore_manifests BEGIN SELECT RAISE(ABORT, 'blackboard restore manifests are append-only'); END`,
		`CREATE TRIGGER blackboard_restore_manifests_no_delete BEFORE DELETE ON blackboard_restore_manifests BEGIN SELECT RAISE(ABORT, 'blackboard restore manifests are append-only'); END`,
	} {
		if _, err := tx.Exec(statement); err != nil {
			return err
		}
	}
	return nil
}

func newMigration(version int, name, sqlBody string, up func(*sql.Tx) error) migration {
	sum := sha256.Sum256([]byte(sqlBody))
	return migration{
		version:  version,
		name:     name,
		checksum: hex.EncodeToString(sum[:]),
		up:       up,
	}
}

func execStatements(tx *sql.Tx, sqlBody string) error {
	// Split on semicolon-terminated statements. Migration SQL is static and
	// does not embed semicolons inside string literals.
	parts := strings.Split(sqlBody, ";")
	for _, part := range parts {
		stmt := strings.TrimSpace(part)
		if stmt == "" || strings.HasPrefix(stmt, "--") {
			// Skip empty fragments and pure-comment chunks.
			lines := strings.Split(stmt, "\n")
			onlyComments := true
			for _, line := range lines {
				line = strings.TrimSpace(line)
				if line == "" || strings.HasPrefix(line, "--") {
					continue
				}
				onlyComments = false
				break
			}
			if onlyComments {
				continue
			}
		}
		if _, err := tx.Exec(stmt); err != nil {
			return fmt.Errorf("exec %q: %w", truncate(stmt, 80), err)
		}
	}
	return nil
}

func truncate(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}

// ensureColumn adds a column when missing. Used for upgrading databases that
// already had CREATE TABLE IF NOT EXISTS without the newer columns.
func ensureColumn(tx *sql.Tx, table, column, definition string) error {
	rows, err := tx.Query(fmt.Sprintf("PRAGMA table_info(%s)", table))
	if err != nil {
		return err
	}
	defer rows.Close()

	for rows.Next() {
		var cid int
		var name, ctype string
		var notNull, pk int
		var dflt sql.NullString
		if err := rows.Scan(&cid, &name, &ctype, &notNull, &dflt, &pk); err != nil {
			return err
		}
		if name == column {
			return nil
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}

	_, err = tx.Exec(fmt.Sprintf("ALTER TABLE %s ADD COLUMN %s %s", table, column, definition))
	return err
}

const migration1BaselineSQL = `
CREATE TABLE IF NOT EXISTS projects (
	id TEXT PRIMARY KEY,
	name TEXT NOT NULL,
	description TEXT NOT NULL DEFAULT '',
	scope_json TEXT NOT NULL,
	defaults_json TEXT NOT NULL DEFAULT '{}',
	created_at TEXT NOT NULL,
	updated_at TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS runtime_profiles (
	id TEXT PRIMARY KEY,
	name TEXT NOT NULL,
	provider TEXT NOT NULL,
	fields_json TEXT NOT NULL DEFAULT '{}',
	created_at TEXT NOT NULL,
	updated_at TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS model_providers (
	id TEXT PRIMARY KEY,
	name TEXT NOT NULL,
	base_url TEXT NOT NULL,
	protocols_json TEXT NOT NULL DEFAULT '[]',
	catalog_json TEXT NOT NULL DEFAULT '{}',
	created_at TEXT NOT NULL,
	updated_at TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS skills (
	id TEXT PRIMARY KEY,
	name TEXT NOT NULL,
	description TEXT NOT NULL DEFAULT '',
	source_provenance_json TEXT NOT NULL DEFAULT '{}',
	created_at TEXT NOT NULL,
	updated_at TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS skill_profile_opt_outs (
	profile_id TEXT NOT NULL,
	skill_id TEXT NOT NULL,
	created_at TEXT NOT NULL,
	PRIMARY KEY (profile_id, skill_id)
);
CREATE TABLE IF NOT EXISTS credential_bindings (
	id TEXT PRIMARY KEY,
	credential_ref TEXT NOT NULL,
	scope TEXT NOT NULL,
	scope_id TEXT NOT NULL DEFAULT '',
	source_json TEXT NOT NULL,
	disabled INTEGER NOT NULL DEFAULT 0,
	created_at TEXT NOT NULL,
	updated_at TEXT NOT NULL,
	UNIQUE (credential_ref, scope, scope_id)
);
CREATE TABLE IF NOT EXISTS tasks (
	id TEXT PRIMARY KEY,
	project_id TEXT NOT NULL,
	goal TEXT NOT NULL,
	status TEXT NOT NULL,
	runner TEXT NOT NULL,
	runtime_profile_id TEXT NOT NULL,
	run_controls_json TEXT NOT NULL,
	scope_snapshot_json TEXT NOT NULL,
	created_at TEXT NOT NULL,
	updated_at TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS task_runtime_config_versions (
	id TEXT PRIMARY KEY,
	task_id TEXT NOT NULL,
	version INTEGER NOT NULL,
	runtime_profile_id TEXT NOT NULL,
	config_json TEXT NOT NULL,
	created_at TEXT NOT NULL,
	UNIQUE (task_id, version)
);
CREATE TABLE IF NOT EXISTS task_continuations (
	id TEXT PRIMARY KEY,
	task_id TEXT NOT NULL,
	number INTEGER NOT NULL,
	runtime_profile_id TEXT NOT NULL,
	runtime_provider TEXT NOT NULL,
	runner TEXT NOT NULL,
	status TEXT NOT NULL,
	container_id TEXT NOT NULL DEFAULT '',
	native_session_id TEXT NOT NULL DEFAULT '',
	native_session_path TEXT NOT NULL DEFAULT '',
	started_at TEXT NOT NULL,
	updated_at TEXT NOT NULL,
	ended_at TEXT NOT NULL DEFAULT '',
	UNIQUE (task_id, number)
);
CREATE TABLE IF NOT EXISTS task_events (
	id TEXT PRIMARY KEY,
	task_id TEXT NOT NULL,
	seq INTEGER NOT NULL,
	kind TEXT NOT NULL,
	payload_json TEXT NOT NULL DEFAULT '{}',
	created_at TEXT NOT NULL,
	UNIQUE (task_id, seq)
);
CREATE TABLE IF NOT EXISTS task_summary_versions (
	id TEXT PRIMARY KEY,
	task_id TEXT NOT NULL,
	version INTEGER NOT NULL,
	summary TEXT NOT NULL,
	submitted_by TEXT NOT NULL DEFAULT '',
	created_at TEXT NOT NULL,
	UNIQUE (task_id, version)
);
CREATE TABLE IF NOT EXISTS project_facts (
	id TEXT PRIMARY KEY,
	project_id TEXT NOT NULL,
	fact_key TEXT NOT NULL,
	category TEXT NOT NULL,
	summary TEXT NOT NULL,
	body TEXT NOT NULL DEFAULT '',
	confidence TEXT NOT NULL,
	scope_status TEXT NOT NULL DEFAULT '',
	created_at TEXT NOT NULL,
	updated_at TEXT NOT NULL,
	UNIQUE (project_id, fact_key)
);
CREATE TABLE IF NOT EXISTS project_fact_versions (
	id TEXT PRIMARY KEY,
	project_id TEXT NOT NULL,
	fact_key TEXT NOT NULL,
	version INTEGER NOT NULL,
	category TEXT NOT NULL,
	summary TEXT NOT NULL,
	body TEXT NOT NULL DEFAULT '',
	confidence TEXT NOT NULL,
	scope_status TEXT NOT NULL DEFAULT '',
	created_at TEXT NOT NULL,
	UNIQUE (project_id, fact_key, version)
);
CREATE TABLE IF NOT EXISTS project_fact_relations (
	id TEXT PRIMARY KEY,
	project_id TEXT NOT NULL,
	source_fact_key TEXT NOT NULL,
	target_fact_key TEXT NOT NULL,
	relation TEXT NOT NULL,
	summary TEXT NOT NULL DEFAULT '',
	created_at TEXT NOT NULL,
	updated_at TEXT NOT NULL,
	UNIQUE (project_id, source_fact_key, target_fact_key, relation)
);
CREATE TABLE IF NOT EXISTS fact_key_aliases (
	id TEXT PRIMARY KEY,
	project_id TEXT NOT NULL,
	alias_fact_key TEXT NOT NULL,
	canon_fact_key TEXT NOT NULL,
	created_at TEXT NOT NULL,
	UNIQUE (project_id, alias_fact_key)
);
CREATE TABLE IF NOT EXISTS finding_key_aliases (
	id TEXT PRIMARY KEY,
	project_id TEXT NOT NULL,
	alias_finding_key TEXT NOT NULL,
	canon_finding_key TEXT NOT NULL,
	created_at TEXT NOT NULL,
	UNIQUE (project_id, alias_finding_key)
);
CREATE TABLE IF NOT EXISTS findings (
	id TEXT PRIMARY KEY,
	project_id TEXT NOT NULL,
	finding_key TEXT NOT NULL,
	version INTEGER NOT NULL,
	title TEXT NOT NULL,
	description TEXT NOT NULL DEFAULT '',
	status TEXT NOT NULL,
	target TEXT NOT NULL DEFAULT '',
	proof TEXT NOT NULL DEFAULT '',
	impact TEXT NOT NULL DEFAULT '',
	recommendation TEXT NOT NULL DEFAULT '',
	cvss_version TEXT NOT NULL DEFAULT '',
	cvss_vector TEXT NOT NULL DEFAULT '',
	cvss_pending INTEGER NOT NULL DEFAULT 1,
	severity TEXT NOT NULL DEFAULT 'pending',
	created_at TEXT NOT NULL,
	updated_at TEXT NOT NULL,
	UNIQUE (project_id, finding_key)
);
CREATE TABLE IF NOT EXISTS finding_versions (
	id TEXT PRIMARY KEY,
	project_id TEXT NOT NULL,
	finding_key TEXT NOT NULL,
	version INTEGER NOT NULL,
	title TEXT NOT NULL,
	description TEXT NOT NULL DEFAULT '',
	status TEXT NOT NULL,
	target TEXT NOT NULL DEFAULT '',
	proof TEXT NOT NULL DEFAULT '',
	impact TEXT NOT NULL DEFAULT '',
	recommendation TEXT NOT NULL DEFAULT '',
	cvss_version TEXT NOT NULL DEFAULT '',
	cvss_vector TEXT NOT NULL DEFAULT '',
	cvss_pending INTEGER NOT NULL DEFAULT 1,
	severity TEXT NOT NULL DEFAULT 'pending',
	created_at TEXT NOT NULL,
	UNIQUE (project_id, finding_key, version)
);
CREATE TABLE IF NOT EXISTS evidence_artifacts (
	id TEXT PRIMARY KEY,
	project_id TEXT NOT NULL,
	evidence_key TEXT NOT NULL,
	attach_to_type TEXT NOT NULL,
	attach_to_key TEXT NOT NULL,
	artifact_type TEXT NOT NULL,
	source_path TEXT NOT NULL DEFAULT '',
	managed_path TEXT NOT NULL,
	sha256 TEXT NOT NULL DEFAULT '',
	summary TEXT NOT NULL DEFAULT '',
	created_at TEXT NOT NULL,
	updated_at TEXT NOT NULL,
	UNIQUE (project_id, evidence_key)
);
-- additive columns for pre-numbered legacy databases
-- projects.defaults_json TEXT NOT NULL DEFAULT '{}'
-- runtime_profiles.kind TEXT NOT NULL DEFAULT 'manual'
-- model_providers.endpoints_json TEXT NOT NULL DEFAULT '[]'
`

func migration1Up(tx *sql.Tx) error {
	if err := execStatements(tx, migration1BaselineSQL); err != nil {
		return err
	}
	if err := ensureColumn(tx, "projects", "defaults_json", "TEXT NOT NULL DEFAULT '{}'"); err != nil {
		return fmt.Errorf("ensure projects.defaults_json: %w", err)
	}
	if err := ensureColumn(tx, "runtime_profiles", "kind", "TEXT NOT NULL DEFAULT 'manual'"); err != nil {
		return fmt.Errorf("ensure runtime_profiles.kind: %w", err)
	}
	if err := ensureColumn(tx, "model_providers", "endpoints_json", "TEXT NOT NULL DEFAULT '[]'"); err != nil {
		return fmt.Errorf("ensure model_providers.endpoints_json: %w", err)
	}
	return nil
}

const migration2SQL = `
-- projects.kind TEXT NOT NULL DEFAULT 'pentest'
-- task_events.continuation_id TEXT
-- task_events.attempt_node_id TEXT
-- task_summary_versions.continuation_id TEXT
-- task_continuations.runtime_config_version_id TEXT
-- task_continuations.blackboard_graph_revision INTEGER
-- task_continuations.blackboard_renderer_version TEXT NOT NULL DEFAULT ''
-- task_continuations.blackboard_estimator_version TEXT NOT NULL DEFAULT ''
-- task_continuations.blackboard_projection_hash TEXT NOT NULL DEFAULT ''
-- task_continuations.blackboard_projection_bytes INTEGER
-- task_continuations.blackboard_projection_estimated_tokens INTEGER
-- task_continuations.blackboard_reconciliation_status TEXT NOT NULL DEFAULT ''
-- task_continuations.blackboard_reconciliation_mutation_id TEXT NOT NULL DEFAULT ''
-- task_continuations.blackboard_reconciled_at TEXT NOT NULL DEFAULT ''
CREATE TABLE IF NOT EXISTS blackboard_store_state (
	id INTEGER PRIMARY KEY CHECK (id = 1),
	canonical_store TEXT NOT NULL,
	cutover_state TEXT NOT NULL,
	migration_contract_version TEXT NOT NULL DEFAULT '',
	graph_schema_version INTEGER NOT NULL DEFAULT 0,
	cutover_id TEXT NOT NULL DEFAULT '',
	source_digest TEXT NOT NULL DEFAULT '',
	mapping_digest TEXT NOT NULL DEFAULT '',
	verified_backup_path TEXT NOT NULL DEFAULT '',
	verified_backup_sha256 TEXT NOT NULL DEFAULT '',
	cutover_application_version TEXT NOT NULL DEFAULT '',
	cutover_started_at TEXT NOT NULL DEFAULT '',
	cutover_committed_at TEXT NOT NULL DEFAULT '',
	post_cutover_write_committed INTEGER NOT NULL DEFAULT 0,
	latest_verification_at TEXT NOT NULL DEFAULT '',
	latest_verification_result_hash TEXT NOT NULL DEFAULT '',
	updated_at TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS blackboard_migration_runs (
	id TEXT PRIMARY KEY,
	kind TEXT NOT NULL,
	state TEXT NOT NULL,
	diagnostic_code TEXT NOT NULL DEFAULT '',
	message TEXT NOT NULL DEFAULT '',
	source_digest TEXT NOT NULL DEFAULT '',
	mapping_digest TEXT NOT NULL DEFAULT '',
	backup_path TEXT NOT NULL DEFAULT '',
	backup_sha256 TEXT NOT NULL DEFAULT '',
	counts_json TEXT NOT NULL DEFAULT '{}',
	created_at TEXT NOT NULL,
	updated_at TEXT NOT NULL,
	finished_at TEXT NOT NULL DEFAULT ''
);
CREATE TABLE IF NOT EXISTS blackboard_legacy_mappings (
	id TEXT PRIMARY KEY,
	project_id TEXT NOT NULL,
	source_table TEXT NOT NULL,
	source_kind TEXT NOT NULL DEFAULT '',
	legacy_primary_id TEXT NOT NULL,
	original_stable_key TEXT NOT NULL DEFAULT '',
	original_version INTEGER,
	source_row_hash TEXT NOT NULL,
	target_kind TEXT NOT NULL DEFAULT '',
	target_id TEXT NOT NULL DEFAULT '',
	target_version INTEGER,
	mapping_status TEXT NOT NULL,
	compatibility_metadata_json TEXT NOT NULL DEFAULT '{}',
	migration_mutation_seq INTEGER,
	cutover_id TEXT NOT NULL DEFAULT '',
	created_at TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_blackboard_legacy_mappings_project
	ON blackboard_legacy_mappings (project_id);
CREATE INDEX IF NOT EXISTS idx_blackboard_legacy_mappings_cutover
	ON blackboard_legacy_mappings (cutover_id);
INSERT INTO blackboard_store_state (
	id, canonical_store, cutover_state, migration_contract_version, graph_schema_version, updated_at
) VALUES (
	1, 'legacy_v1', 'legacy', 'legacy_blackboard_to_graph_v1', 0, '1970-01-01T00:00:00Z'
);
`

func migration2Up(tx *sql.Tx) error {
	// Surrounding schema required before graph cutover (storage contract §3).
	if err := ensureColumn(tx, "projects", "kind", "TEXT NOT NULL DEFAULT 'pentest'"); err != nil {
		return fmt.Errorf("ensure projects.kind: %w", err)
	}
	if err := ensureColumn(tx, "task_events", "continuation_id", "TEXT"); err != nil {
		return fmt.Errorf("ensure task_events.continuation_id: %w", err)
	}
	if err := ensureColumn(tx, "task_events", "attempt_node_id", "TEXT"); err != nil {
		return fmt.Errorf("ensure task_events.attempt_node_id: %w", err)
	}
	if err := ensureColumn(tx, "task_summary_versions", "continuation_id", "TEXT"); err != nil {
		return fmt.Errorf("ensure task_summary_versions.continuation_id: %w", err)
	}
	if err := ensureColumn(tx, "task_continuations", "runtime_config_version_id", "TEXT"); err != nil {
		return fmt.Errorf("ensure task_continuations.runtime_config_version_id: %w", err)
	}
	if err := ensureColumn(tx, "task_continuations", "blackboard_graph_revision", "INTEGER"); err != nil {
		return fmt.Errorf("ensure task_continuations.blackboard_graph_revision: %w", err)
	}
	if err := ensureColumn(tx, "task_continuations", "blackboard_renderer_version", "TEXT NOT NULL DEFAULT ''"); err != nil {
		return fmt.Errorf("ensure task_continuations.blackboard_renderer_version: %w", err)
	}
	if err := ensureColumn(tx, "task_continuations", "blackboard_estimator_version", "TEXT NOT NULL DEFAULT ''"); err != nil {
		return fmt.Errorf("ensure task_continuations.blackboard_estimator_version: %w", err)
	}
	if err := ensureColumn(tx, "task_continuations", "blackboard_projection_hash", "TEXT NOT NULL DEFAULT ''"); err != nil {
		return fmt.Errorf("ensure task_continuations.blackboard_projection_hash: %w", err)
	}
	if err := ensureColumn(tx, "task_continuations", "blackboard_projection_bytes", "INTEGER"); err != nil {
		return fmt.Errorf("ensure task_continuations.blackboard_projection_bytes: %w", err)
	}
	if err := ensureColumn(tx, "task_continuations", "blackboard_projection_estimated_tokens", "INTEGER"); err != nil {
		return fmt.Errorf("ensure task_continuations.blackboard_projection_estimated_tokens: %w", err)
	}
	if err := ensureColumn(tx, "task_continuations", "blackboard_reconciliation_status", "TEXT NOT NULL DEFAULT ''"); err != nil {
		return fmt.Errorf("ensure task_continuations.blackboard_reconciliation_status: %w", err)
	}
	if err := ensureColumn(tx, "task_continuations", "blackboard_reconciliation_mutation_id", "TEXT NOT NULL DEFAULT ''"); err != nil {
		return fmt.Errorf("ensure task_continuations.blackboard_reconciliation_mutation_id: %w", err)
	}
	if err := ensureColumn(tx, "task_continuations", "blackboard_reconciled_at", "TEXT NOT NULL DEFAULT ''"); err != nil {
		return fmt.Errorf("ensure task_continuations.blackboard_reconciled_at: %w", err)
	}

	if err := execStatements(tx, migration2SQL); err != nil {
		return err
	}

	// Fresh databases insert the singleton via migration2SQL. Upgrades that
	// already have the table without a row still need the default epoch.
	var n int
	if err := tx.QueryRow(`SELECT COUNT(*) FROM blackboard_store_state WHERE id = 1`).Scan(&n); err != nil {
		return fmt.Errorf("check store state: %w", err)
	}
	if n == 0 {
		_, err := tx.Exec(`
			INSERT INTO blackboard_store_state (
				id, canonical_store, cutover_state, migration_contract_version, graph_schema_version, updated_at
			) VALUES (1, ?, 'legacy', 'legacy_blackboard_to_graph_v1', 0, ?)
		`, CanonicalStoreLegacyV1, time.Now().UTC().Format(time.RFC3339Nano))
		if err != nil {
			return fmt.Errorf("insert default store epoch: %w", err)
		}
	}
	return nil
}

// GraphSchemaVersion is the Blackboard typed-property-graph schema version
// implemented by this binary. It matches the graph contract's schema version.
const GraphSchemaVersion = 1

// mutation3SQL creates the C02 subset of the Blackboard graph ledger and
// rebuildable head tables (storage contract §6/§7). Edge, compaction, health,
// and projection_metrics tables are intentionally deferred to the slices that
// first need them (C03 edges, C09/C10 projection/health). Append-only triggers
// are installed in migration3Up because their BEGIN...END bodies contain
// semicolons that execStatements' simple splitter cannot parse.
const migration3SQL = `
-- C02 ledger tables (storage contract §6). All append-only: BEFORE UPDATE and
-- BEFORE DELETE triggers installed in migration3Up abort mutation.
CREATE TABLE IF NOT EXISTS blackboard_graph_mutations (
	project_id TEXT NOT NULL REFERENCES projects(id) ON DELETE RESTRICT,
	mutation_seq INTEGER NOT NULL,
	mutation_id TEXT NOT NULL,
	base_graph_revision INTEGER NOT NULL,
	result_graph_revision INTEGER NOT NULL,
	schema_version INTEGER NOT NULL,
	mutation_kind TEXT NOT NULL CHECK (mutation_kind IN ('normal','merge','compaction','restore','reconciliation','projection','migration')),
	maintenance_metadata_json TEXT NOT NULL DEFAULT '{}',
	maintenance_subject_id TEXT,
	idempotency_scope TEXT NOT NULL,
	idempotency_key TEXT NOT NULL,
	request_hash TEXT NOT NULL,
	request_json TEXT NOT NULL,
	result_json TEXT NOT NULL,
	result_hash TEXT NOT NULL,
	recorded_at TEXT NOT NULL,
	previous_mutation_hash TEXT NOT NULL,
	mutation_hash TEXT NOT NULL,
	resulting_state_hash TEXT NOT NULL,
	projection_status TEXT NOT NULL CHECK (projection_status IN ('measured','dirty')),
	resulting_main_projection_hash TEXT,
	projection_renderer_version TEXT NOT NULL DEFAULT '',
	projection_estimator_version TEXT NOT NULL DEFAULT '',
	projection_bytes INTEGER,
	projection_estimated_tokens INTEGER,
	CHECK (result_graph_revision >= base_graph_revision),
	CHECK (mutation_seq >= 0),
	CHECK (base_graph_revision >= 0),
	CHECK (result_graph_revision >= 0),
	PRIMARY KEY (project_id, mutation_seq)
);
CREATE UNIQUE INDEX IF NOT EXISTS ux_blackboard_graph_mutations_id
	ON blackboard_graph_mutations (mutation_id);
CREATE UNIQUE INDEX IF NOT EXISTS ux_blackboard_graph_mutations_idempotency
	ON blackboard_graph_mutations (project_id, idempotency_scope, idempotency_key);
CREATE INDEX IF NOT EXISTS idx_blackboard_graph_mutations_order
	ON blackboard_graph_mutations (project_id, mutation_seq);
CREATE INDEX IF NOT EXISTS idx_blackboard_graph_mutations_revision
	ON blackboard_graph_mutations (project_id, result_graph_revision);
CREATE UNIQUE INDEX IF NOT EXISTS ux_blackboard_graph_mutations_one_change_per_revision
	ON blackboard_graph_mutations (project_id, result_graph_revision)
	WHERE result_graph_revision > base_graph_revision;

CREATE TABLE IF NOT EXISTS blackboard_graph_provenance (
	project_id TEXT NOT NULL REFERENCES projects(id) ON DELETE RESTRICT,
	id TEXT NOT NULL,
	actor_type TEXT NOT NULL CHECK (actor_type IN ('runtime','operator','system','migration')),
	actor_id TEXT NOT NULL,
	task_id TEXT,
	continuation_id TEXT,
	runtime_profile_id TEXT,
	runner TEXT,
	migration_source_json TEXT,
	recorded_at TEXT NOT NULL,
	PRIMARY KEY (project_id, id)
);
CREATE UNIQUE INDEX IF NOT EXISTS ux_blackboard_graph_provenance_id
	ON blackboard_graph_provenance (id);
CREATE INDEX IF NOT EXISTS idx_blackboard_graph_provenance_task
	ON blackboard_graph_provenance (project_id, task_id);
CREATE INDEX IF NOT EXISTS idx_blackboard_graph_provenance_continuation
	ON blackboard_graph_provenance (project_id, continuation_id);

CREATE TABLE IF NOT EXISTS blackboard_graph_provenance_events (
	project_id TEXT NOT NULL REFERENCES projects(id) ON DELETE RESTRICT,
	provenance_id TEXT NOT NULL,
	ordinal INTEGER NOT NULL,
	event_id TEXT NOT NULL,
	PRIMARY KEY (project_id, provenance_id, ordinal),
	FOREIGN KEY (project_id, provenance_id) REFERENCES blackboard_graph_provenance (project_id, id) ON DELETE RESTRICT,
	CHECK (ordinal >= 0)
);
CREATE UNIQUE INDEX IF NOT EXISTS ux_blackboard_graph_provenance_events_event
	ON blackboard_graph_provenance_events (project_id, provenance_id, event_id);

CREATE TABLE IF NOT EXISTS blackboard_graph_operations (
	project_id TEXT NOT NULL,
	mutation_seq INTEGER NOT NULL,
	operation_index INTEGER NOT NULL,
	op_id TEXT NOT NULL,
	operation_kind TEXT NOT NULL,
	operation_json TEXT NOT NULL,
	result_json TEXT NOT NULL,
	changed INTEGER NOT NULL CHECK (changed IN (0,1)),
	provenance_id TEXT NOT NULL,
	CHECK (operation_index >= 0),
	PRIMARY KEY (project_id, mutation_seq, operation_index),
	FOREIGN KEY (project_id, mutation_seq) REFERENCES blackboard_graph_mutations (project_id, mutation_seq) ON DELETE RESTRICT,
	FOREIGN KEY (project_id, provenance_id) REFERENCES blackboard_graph_provenance (project_id, id) ON DELETE RESTRICT
);
CREATE UNIQUE INDEX IF NOT EXISTS ux_blackboard_graph_operations_opid
	ON blackboard_graph_operations (project_id, mutation_seq, op_id);

CREATE TABLE IF NOT EXISTS blackboard_nodes (
	project_id TEXT NOT NULL REFERENCES projects(id) ON DELETE RESTRICT,
	id TEXT NOT NULL,
	node_type TEXT NOT NULL,
	original_stable_key TEXT NOT NULL,
	created_mutation_seq INTEGER NOT NULL,
	created_operation_index INTEGER NOT NULL,
	created_at TEXT NOT NULL,
	CHECK (created_mutation_seq >= 0),
	CHECK (created_operation_index >= 0),
	PRIMARY KEY (project_id, id),
	FOREIGN KEY (project_id, created_mutation_seq, created_operation_index)
		REFERENCES blackboard_graph_operations (project_id, mutation_seq, operation_index) ON DELETE RESTRICT
);
CREATE UNIQUE INDEX IF NOT EXISTS ux_blackboard_nodes_id
	ON blackboard_nodes (id);
CREATE UNIQUE INDEX IF NOT EXISTS ux_blackboard_nodes_type_key
	ON blackboard_nodes (project_id, node_type, original_stable_key);

CREATE TABLE IF NOT EXISTS blackboard_node_versions (
	project_id TEXT NOT NULL,
	node_id TEXT NOT NULL,
	version INTEGER NOT NULL,
	result_graph_revision INTEGER NOT NULL,
	mutation_seq INTEGER NOT NULL,
	operation_index INTEGER NOT NULL,
	schema_version INTEGER NOT NULL,
	disposition TEXT NOT NULL CHECK (disposition IN ('main','archived','merged')),
	merge_target_id TEXT,
	properties_json TEXT NOT NULL CHECK (json_valid(properties_json)),
	semantic_hash TEXT NOT NULL,
	updated_at TEXT NOT NULL,
	CHECK (version > 0),
	CHECK (result_graph_revision >= 0),
	CHECK (mutation_seq >= 0),
	CHECK (operation_index >= 0),
	CHECK (schema_version >= 0),
	CHECK ((disposition = 'merged' AND merge_target_id IS NOT NULL) OR (disposition <> 'merged' AND merge_target_id IS NULL)),
	PRIMARY KEY (project_id, node_id, version),
	FOREIGN KEY (project_id, node_id) REFERENCES blackboard_nodes (project_id, id) ON DELETE RESTRICT,
	FOREIGN KEY (project_id, mutation_seq, operation_index)
		REFERENCES blackboard_graph_operations (project_id, mutation_seq, operation_index) ON DELETE RESTRICT
);
CREATE INDEX IF NOT EXISTS idx_blackboard_node_versions_node_desc
	ON blackboard_node_versions (project_id, node_id, version DESC);
CREATE INDEX IF NOT EXISTS idx_blackboard_node_versions_revision
	ON blackboard_node_versions (project_id, result_graph_revision);

CREATE TABLE IF NOT EXISTS blackboard_key_events (
	project_id TEXT NOT NULL,
	node_type TEXT NOT NULL,
	key TEXT NOT NULL,
	key_version INTEGER NOT NULL,
	role TEXT NOT NULL CHECK (role IN ('stable','alias')),
	source_node_id TEXT NOT NULL,
	canonical_node_id TEXT NOT NULL,
	legacy_nonconforming INTEGER NOT NULL DEFAULT 0 CHECK (legacy_nonconforming IN (0,1)),
	result_graph_revision INTEGER NOT NULL,
	mutation_seq INTEGER NOT NULL,
	operation_index INTEGER NOT NULL,
	semantic_hash TEXT NOT NULL,
	CHECK (key_version > 0),
	CHECK (result_graph_revision >= 0),
	CHECK (mutation_seq >= 0),
	CHECK (operation_index >= 0),
	PRIMARY KEY (project_id, node_type, key, key_version),
	FOREIGN KEY (project_id, source_node_id) REFERENCES blackboard_nodes (project_id, id) ON DELETE RESTRICT,
	FOREIGN KEY (project_id, canonical_node_id) REFERENCES blackboard_nodes (project_id, id) ON DELETE RESTRICT,
	FOREIGN KEY (project_id, mutation_seq, operation_index)
		REFERENCES blackboard_graph_operations (project_id, mutation_seq, operation_index) ON DELETE RESTRICT
);
CREATE INDEX IF NOT EXISTS idx_blackboard_key_events_canonical
	ON blackboard_key_events (project_id, node_type, canonical_node_id);

-- C02 rebuildable materialized tables (storage contract §7). Mutable caches.
CREATE TABLE IF NOT EXISTS blackboard_graph_state (
	project_id TEXT PRIMARY KEY REFERENCES projects(id) ON DELETE RESTRICT,
	latest_mutation_seq INTEGER NOT NULL,
	current_graph_revision INTEGER NOT NULL,
	materialized_mutation_seq INTEGER NOT NULL,
	history_head_hash TEXT NOT NULL,
	current_semantic_state_hash TEXT NOT NULL,
	current_main_projection_hash TEXT,
	projection_renderer_version TEXT NOT NULL DEFAULT '',
	projection_estimator_version TEXT NOT NULL DEFAULT '',
	projection_bytes INTEGER,
	projection_estimated_tokens INTEGER,
	budget_state TEXT NOT NULL DEFAULT 'unknown',
	projection_dirty_revision INTEGER NOT NULL DEFAULT 0,
	updated_at TEXT NOT NULL,
	CHECK (latest_mutation_seq >= 0),
	CHECK (current_graph_revision >= 0),
	CHECK (materialized_mutation_seq >= 0)
);

CREATE TABLE IF NOT EXISTS blackboard_node_heads (
	project_id TEXT NOT NULL,
	node_id TEXT NOT NULL,
	node_type TEXT NOT NULL,
	version INTEGER NOT NULL,
	graph_revision INTEGER NOT NULL,
	disposition TEXT NOT NULL CHECK (disposition IN ('main','archived','merged')),
	merge_target_id TEXT,
	lifecycle_state TEXT NOT NULL DEFAULT '',
	entity_kind TEXT NOT NULL DEFAULT '',
	scope_status TEXT NOT NULL DEFAULT '',
	semantic_hash TEXT NOT NULL,
	CHECK (version > 0),
	CHECK (graph_revision >= 0),
	CHECK ((disposition = 'merged' AND merge_target_id IS NOT NULL) OR (disposition <> 'merged' AND merge_target_id IS NULL)),
	PRIMARY KEY (project_id, node_id),
	FOREIGN KEY (project_id, node_id, version) REFERENCES blackboard_node_versions (project_id, node_id, version) ON DELETE RESTRICT
);
CREATE INDEX IF NOT EXISTS idx_blackboard_node_heads_type
	ON blackboard_node_heads (project_id, node_type, disposition, lifecycle_state, node_id);
CREATE INDEX IF NOT EXISTS idx_blackboard_node_heads_entity
	ON blackboard_node_heads (project_id, entity_kind, disposition, node_id);

CREATE TABLE IF NOT EXISTS blackboard_key_registry (
	project_id TEXT NOT NULL,
	node_type TEXT NOT NULL,
	key TEXT NOT NULL,
	latest_key_version INTEGER NOT NULL,
	role TEXT NOT NULL CHECK (role IN ('stable','alias')),
	source_node_id TEXT NOT NULL,
	canonical_node_id TEXT NOT NULL,
	semantic_hash TEXT NOT NULL,
	CHECK (latest_key_version > 0),
	PRIMARY KEY (project_id, node_type, key),
	FOREIGN KEY (project_id, node_type, key, latest_key_version)
		REFERENCES blackboard_key_events (project_id, node_type, key, key_version) ON DELETE RESTRICT
);
CREATE INDEX IF NOT EXISTS idx_blackboard_key_registry_canonical
	ON blackboard_key_registry (project_id, canonical_node_id);
`

// appendOnlyLedgerTables are the storage contract §6 tables that must reject
// any UPDATE or DELETE; repair rebuilds materialized heads rather than
// rewriting the ledger.
var appendOnlyLedgerTables = []string{
	"blackboard_graph_mutations",
	"blackboard_graph_provenance",
	"blackboard_graph_provenance_events",
	"blackboard_graph_operations",
	"blackboard_nodes",
	"blackboard_node_versions",
	"blackboard_key_events",
}

func migration3Up(tx *sql.Tx) error {
	if err := execStatements(tx, migration3SQL); err != nil {
		return err
	}

	// Append-only ledger guard triggers (storage contract §6). Each is a
	// self-contained statement; install directly because execStatements'
	// splitter cannot handle semicolons inside BEGIN...END bodies.
	for _, table := range appendOnlyLedgerTables {
		for _, stmt := range []string{
			`CREATE TRIGGER IF NOT EXISTS ` + table + `_no_update BEFORE UPDATE ON ` + table + ` BEGIN SELECT RAISE(ABORT, '` + table + ` is append-only'); END`,
			`CREATE TRIGGER IF NOT EXISTS ` + table + `_no_delete BEFORE DELETE ON ` + table + ` BEGIN SELECT RAISE(ABORT, '` + table + ` is append-only'); END`,
		} {
			if _, err := tx.Exec(stmt); err != nil {
				return fmt.Errorf("install ledger guard for %s: %w", table, err)
			}
		}
	}

	// The graph ledger now exists; record its schema version while the store
	// epoch remains legacy_v1 (production writes stay dark until the M05
	// cutover).
	if _, err := tx.Exec(
		`UPDATE blackboard_store_state SET graph_schema_version = ?, updated_at = ? WHERE id = 1`,
		GraphSchemaVersion, time.Now().UTC().Format(time.RFC3339Nano),
	); err != nil {
		return fmt.Errorf("set graph_schema_version: %w", err)
	}
	return nil
}
