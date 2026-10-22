// Package store owns the SQLite connection and schema bootstrap shared by
// every domain package. Domain repositories receive the opened database and
// keep their business logic separate from transport concerns so HTTP, MCP, and
// CLI handlers can all call the same services.
package store

import (
	"database/sql"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"

	_ "modernc.org/sqlite"
)

// memDBSeq makes each empty-path Open use an isolated in-memory database.
var memDBSeq atomic.Uint64

// CanonicalStoreBlackboardV2 is the only Store epoch this binary creates or
// opens.
const CanonicalStoreBlackboardV2 = "blackboard_v2"

// DB wraps a SQLite connection with the baseline schema applied.
type DB struct {
	*sql.DB
}

// Open connects to the SQLite database at path and creates the baseline schema
// when the file is empty. A database created by an older CyberPenda version is
// refused with ErrUnsupportedStoreSchema. An empty path uses an in-memory
// database, which is handy for tests.
func Open(path string) (*DB, error) {
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
	if err := restrictStoreFilePermissions(path); err != nil {
		_ = db.Close()
		return nil, err
	}

	if err := initializeSchema(db); err != nil {
		_ = db.Close()
		return nil, err
	}

	return &DB{db}, nil
}

// restrictStoreFilePermissions locks the SQLite database file and its WAL/SHM
// sidecars down to owner-only (0o600). The database persists plaintext "literal"
// credential secrets in credential_bindings.source_json, so it must not be
// readable by other local OS users. SQLite creates these files using the process
// umask (typically 0644); we tighten them explicitly once the connection exists.
// Sidecars that do not exist yet are skipped: a clean database has no WAL/SHM
// until the first write, and SQLite recreates them inheriting the main file mode.
func restrictStoreFilePermissions(path string) error {
	if path == "" || path == ":memory:" {
		return nil
	}
	for _, candidate := range []string{path, path + "-wal", path + "-shm"} {
		if err := os.Chmod(candidate, 0o600); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("restrict store file permissions %s: %w", filepath.Base(candidate), err)
		}
	}
	return nil
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
