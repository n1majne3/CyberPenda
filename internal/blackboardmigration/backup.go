package blackboardmigration

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"pentest/internal/store"
)

type VerifiedBackup struct {
	Path       string `json:"path"`
	SHA256     string `json:"sha256"`
	QuickCheck string `json:"quick_check"`
}

type BackupImplementation interface {
	CreateVerifiedBackup(context.Context, *store.DB, string, string) (VerifiedBackup, error)
}

type BackupImplementationFunc func(context.Context, *store.DB, string, string) (VerifiedBackup, error)

func (fn BackupImplementationFunc) CreateVerifiedBackup(ctx context.Context, db *store.DB, sourcePath, destinationPath string) (VerifiedBackup, error) {
	return fn(ctx, db, sourcePath, destinationPath)
}

type SQLiteBackupImplementation struct{}

func (SQLiteBackupImplementation) CreateVerifiedBackup(ctx context.Context, db *store.DB, sourcePath, destinationPath string) (VerifiedBackup, error) {
	if sourcePath == "" || sourcePath == "." || sourcePath == ":memory:" {
		return VerifiedBackup{}, errors.New("file-backed SQLite database is required for verified backup")
	}
	sourcePath = filepath.Clean(sourcePath)
	destinationPath = filepath.Clean(destinationPath)
	if sourcePath == destinationPath {
		return VerifiedBackup{}, errors.New("backup destination must differ from source database")
	}
	if _, err := os.Stat(destinationPath); err == nil {
		return VerifiedBackup{}, fmt.Errorf("backup destination already exists: %s", destinationPath)
	} else if !errors.Is(err, os.ErrNotExist) {
		return VerifiedBackup{}, fmt.Errorf("inspect backup destination: %w", err)
	}

	var busy, logFrames, checkpointedFrames int
	if err := db.QueryRowContext(ctx, `PRAGMA wal_checkpoint(FULL)`).Scan(&busy, &logFrames, &checkpointedFrames); err != nil {
		return VerifiedBackup{}, fmt.Errorf("checkpoint SQLite WAL: %w", err)
	}
	if busy != 0 {
		return VerifiedBackup{}, fmt.Errorf("checkpoint SQLite WAL: %d connections remained busy", busy)
	}
	if _, err := db.ExecContext(ctx, `VACUUM INTO ?`, destinationPath); err != nil {
		return VerifiedBackup{}, fmt.Errorf("create SQLite backup: %w", err)
	}
	if err := os.Chmod(destinationPath, 0o600); err != nil {
		return VerifiedBackup{}, fmt.Errorf("set owner-only backup permissions: %w", err)
	}
	if err := syncPath(destinationPath); err != nil {
		return VerifiedBackup{}, err
	}
	if err := syncDirectory(filepath.Dir(destinationPath)); err != nil {
		return VerifiedBackup{}, err
	}

	backupDB, err := sql.Open("sqlite", "file:"+destinationPath+"?mode=ro")
	if err != nil {
		return VerifiedBackup{}, fmt.Errorf("open backup independently: %w", err)
	}
	defer backupDB.Close()
	var quickCheck string
	if err := backupDB.QueryRowContext(ctx, `PRAGMA quick_check`).Scan(&quickCheck); err != nil {
		return VerifiedBackup{}, fmt.Errorf("run independent backup quick_check: %w", err)
	}
	if quickCheck != "ok" {
		return VerifiedBackup{}, errors.New("independent backup quick_check failed")
	}

	digest, err := fileSHA256(destinationPath)
	if err != nil {
		return VerifiedBackup{}, err
	}
	return VerifiedBackup{Path: destinationPath, SHA256: digest, QuickCheck: quickCheck}, nil
}

func CreateVerifiedMigrationSourceBackup(ctx context.Context, sourceDB *sql.DB, destinationPath string) (VerifiedBackup, error) {
	destinationPath = filepath.Clean(destinationPath)
	if destinationPath == "" || destinationPath == "." {
		return VerifiedBackup{}, errors.New("backup destination is required")
	}
	if _, err := os.Stat(destinationPath); err == nil {
		return VerifiedBackup{}, fmt.Errorf("backup destination already exists: %s", destinationPath)
	} else if !errors.Is(err, os.ErrNotExist) {
		return VerifiedBackup{}, fmt.Errorf("inspect backup destination: %w", err)
	}
	if err := createLogicalSQLiteBackup(ctx, sourceDB, destinationPath); err != nil {
		return VerifiedBackup{}, err
	}
	if err := os.Chmod(destinationPath, 0o600); err != nil {
		return VerifiedBackup{}, fmt.Errorf("set owner-only backup permissions: %w", err)
	}
	if err := syncPath(destinationPath); err != nil {
		return VerifiedBackup{}, err
	}
	if err := syncDirectory(filepath.Dir(destinationPath)); err != nil {
		return VerifiedBackup{}, err
	}
	backupDB, err := sql.Open("sqlite", "file:"+destinationPath+"?mode=ro")
	if err != nil {
		return VerifiedBackup{}, fmt.Errorf("open backup independently: %w", err)
	}
	defer backupDB.Close()
	var quickCheck string
	if err := backupDB.QueryRowContext(ctx, `PRAGMA quick_check`).Scan(&quickCheck); err != nil {
		return VerifiedBackup{}, fmt.Errorf("run independent backup quick_check: %w", err)
	}
	if quickCheck != "ok" {
		return VerifiedBackup{}, errors.New("independent backup quick_check failed")
	}
	digest, err := fileSHA256(destinationPath)
	if err != nil {
		return VerifiedBackup{}, err
	}
	return VerifiedBackup{Path: destinationPath, SHA256: digest, QuickCheck: quickCheck}, nil
}

type sqliteSchemaEntry struct {
	Type string
	Name string
	SQL  string
}

func createLogicalSQLiteBackup(ctx context.Context, sourceDB *sql.DB, destinationPath string) (returnErr error) {
	backupDB, err := sql.Open("sqlite", destinationPath)
	if err != nil {
		return fmt.Errorf("open destination backup: %w", err)
	}
	defer func() {
		if closeErr := backupDB.Close(); closeErr != nil && returnErr == nil {
			returnErr = closeErr
		}
		if returnErr != nil {
			_ = os.Remove(destinationPath)
		}
	}()
	if _, err := backupDB.ExecContext(ctx, `PRAGMA foreign_keys=OFF`); err != nil {
		return fmt.Errorf("disable backup foreign keys: %w", err)
	}
	entries, err := readSQLiteSchema(ctx, sourceDB)
	if err != nil {
		return err
	}
	tx, err := backupDB.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin logical SQLite backup: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	var tables []string
	for _, entry := range entries {
		if entry.Type != "table" {
			continue
		}
		if _, err := tx.ExecContext(ctx, entry.SQL); err != nil {
			return fmt.Errorf("create backup table %s: %w", entry.Name, err)
		}
		tables = append(tables, entry.Name)
	}
	for _, table := range tables {
		if err := copySQLiteTable(ctx, sourceDB, tx, table); err != nil {
			return err
		}
	}
	for _, entry := range entries {
		if entry.Type == "table" {
			continue
		}
		if _, err := tx.ExecContext(ctx, entry.SQL); err != nil {
			return fmt.Errorf("create backup %s %s: %w", entry.Type, entry.Name, err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit logical SQLite backup: %w", err)
	}
	return nil
}

func readSQLiteSchema(ctx context.Context, db *sql.DB) ([]sqliteSchemaEntry, error) {
	rows, err := db.QueryContext(ctx, `
		SELECT type,name,sql
		FROM sqlite_master
		WHERE sql IS NOT NULL
		  AND type IN ('table','index','trigger','view')
		  AND name NOT LIKE 'sqlite_%'
		ORDER BY CASE type WHEN 'table' THEN 0 WHEN 'view' THEN 1 WHEN 'index' THEN 2 ELSE 3 END,name`)
	if err != nil {
		return nil, fmt.Errorf("read migration source schema: %w", err)
	}
	defer rows.Close()
	var entries []sqliteSchemaEntry
	for rows.Next() {
		var entry sqliteSchemaEntry
		if err := rows.Scan(&entry.Type, &entry.Name, &entry.SQL); err != nil {
			return nil, err
		}
		entries = append(entries, entry)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return entries, nil
}

func copySQLiteTable(ctx context.Context, sourceDB *sql.DB, tx *sql.Tx, table string) error {
	rows, err := sourceDB.QueryContext(ctx, `SELECT * FROM `+quoteSQLiteIdent(table))
	if err != nil {
		return fmt.Errorf("read source table %s: %w", table, err)
	}
	defer rows.Close()
	columns, err := rows.Columns()
	if err != nil {
		return fmt.Errorf("read source columns for %s: %w", table, err)
	}
	if len(columns) == 0 {
		return nil
	}
	columnNames := make([]string, len(columns))
	placeholders := make([]string, len(columns))
	for i, column := range columns {
		columnNames[i] = quoteSQLiteIdent(column)
		placeholders[i] = "?"
	}
	insertSQL := `INSERT INTO ` + quoteSQLiteIdent(table) + ` (` + strings.Join(columnNames, ",") + `) VALUES (` + strings.Join(placeholders, ",") + `)`
	for rows.Next() {
		values := make([]any, len(columns))
		destinations := make([]any, len(columns))
		for i := range values {
			destinations[i] = &values[i]
		}
		if err := rows.Scan(destinations...); err != nil {
			return fmt.Errorf("scan source row for %s: %w", table, err)
		}
		if _, err := tx.ExecContext(ctx, insertSQL, values...); err != nil {
			return fmt.Errorf("insert backup row for %s: %w", table, err)
		}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("read source rows for %s: %w", table, err)
	}
	return nil
}

func quoteSQLiteIdent(identifier string) string {
	return `"` + strings.ReplaceAll(identifier, `"`, `""`) + `"`
}

func syncPath(path string) error {
	file, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		return fmt.Errorf("open backup for fsync: %w", err)
	}
	defer file.Close()
	if err := file.Sync(); err != nil {
		return fmt.Errorf("fsync backup: %w", err)
	}
	return nil
}

func syncDirectory(path string) error {
	directory, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("open backup directory for fsync: %w", err)
	}
	defer directory.Close()
	if err := directory.Sync(); err != nil {
		return fmt.Errorf("fsync backup directory: %w", err)
	}
	return nil
}

func fileSHA256(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", fmt.Errorf("open backup for SHA-256: %w", err)
	}
	defer file.Close()
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return "", fmt.Errorf("hash backup: %w", err)
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}
