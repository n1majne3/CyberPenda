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
