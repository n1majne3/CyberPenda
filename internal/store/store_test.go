package store_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"pentest/internal/store"
)

// TestOpenRunsMigrationsIdempotently guards against re-running migrations on an
// existing database failing.
func TestOpenRunsMigrationsIdempotently(t *testing.T) {
	path := filepath.Join(t.TempDir(), "pentest.db")

	first, err := store.Open(path)
	if err != nil {
		t.Fatalf("first open: %v", err)
	}
	if err := first.Close(); err != nil {
		t.Fatalf("first close: %v", err)
	}

	second, err := store.Open(path)
	if err != nil {
		t.Fatalf("second open (migration rerun): %v", err)
	}
	t.Cleanup(func() {
		if err := second.Close(); err != nil {
			t.Fatalf("second close: %v", err)
		}
	})

	if err := second.Ping(); err != nil {
		t.Fatalf("ping after reopen: %v", err)
	}
}

func TestMigrations37And38PreservePendingAssistedConclusionReceipts(t *testing.T) {
	path := filepath.Join(t.TempDir(), "pentest.db")
	db, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	var migration36Checksum string
	if err := db.QueryRow(`SELECT checksum FROM schema_migrations WHERE version=36`).Scan(&migration36Checksum); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`
		DROP INDEX assisted_conclusion_receipts_task_created;
		DROP TABLE assisted_conclusion_receipts;
		CREATE TABLE assisted_conclusion_receipts (
			id TEXT PRIMARY KEY,
			task_id TEXT NOT NULL,
			continuation_id TEXT NOT NULL,
			source_session_id TEXT NOT NULL,
			source_turn_id TEXT NOT NULL,
			state TEXT NOT NULL CHECK (state = 'pending'),
			terminal_tool_result_count INTEGER NOT NULL CHECK (terminal_tool_result_count > 0),
			created_at TEXT NOT NULL,
			updated_at TEXT NOT NULL,
			UNIQUE (task_id, continuation_id, source_turn_id)
		);
		CREATE INDEX assisted_conclusion_receipts_task_created
			ON assisted_conclusion_receipts(task_id, created_at DESC);
		INSERT INTO assisted_conclusion_receipts VALUES
			('receipt-1','task-1','continuation-1','session-1','turn-1','pending',2,'2026-07-27T00:00:00Z','2026-07-27T00:00:00Z');
		DELETE FROM schema_migrations WHERE version>=37;
	`); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	reopened, err := store.Open(path)
	if err != nil {
		t.Fatalf("upgrade v36 Store: %v", err)
	}
	defer reopened.Close()
	var state, sourceModelProviderID string
	var sourceWorkWatermark, semanticPersistenceWatermark int
	var dispatchRequestID sql.NullString
	if err := reopened.QueryRow(`SELECT state,source_model_provider_id,dispatch_request_id,source_work_watermark,semantic_persistence_watermark
		FROM assisted_conclusion_receipts WHERE id='receipt-1'`).Scan(&state, &sourceModelProviderID, &dispatchRequestID, &sourceWorkWatermark, &semanticPersistenceWatermark); err != nil {
		t.Fatal(err)
	}
	if state != "pending" || sourceModelProviderID != "" || dispatchRequestID.Valid || sourceWorkWatermark != 2 || semanticPersistenceWatermark != 0 {
		t.Fatalf("migrated receipt = state %q provider %q dispatch %#v watermarks=(%d,%d)", state, sourceModelProviderID, dispatchRequestID, sourceWorkWatermark, semanticPersistenceWatermark)
	}
	var preservedChecksum string
	if err := reopened.QueryRow(`SELECT checksum FROM schema_migrations WHERE version=36`).Scan(&preservedChecksum); err != nil {
		t.Fatal(err)
	}
	if preservedChecksum != migration36Checksum {
		t.Fatalf("migration 36 checksum changed: before %q after %q", migration36Checksum, preservedChecksum)
	}
	var migration37 int
	if err := reopened.QueryRow(`SELECT COUNT(*) FROM schema_migrations WHERE version=37`).Scan(&migration37); err != nil || migration37 != 1 {
		t.Fatalf("migration 37 count = %d, err=%v", migration37, err)
	}
	var migration38 int
	if err := reopened.QueryRow(`SELECT COUNT(*) FROM schema_migrations WHERE version=38`).Scan(&migration38); err != nil || migration38 != 1 {
		t.Fatalf("migration 38 count = %d, err=%v", migration38, err)
	}
	if _, err := reopened.Query(`SELECT terminal_tool_result_count FROM assisted_conclusion_receipts`); err == nil {
		t.Fatal("legacy terminal_tool_result_count column still exists after migration 38")
	}
}

func TestMigration38PreservesValidatedAssistedConclusionReceipt(t *testing.T) {
	path := filepath.Join(t.TempDir(), "pentest.db")
	db, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	var migration37Checksum string
	if err := db.QueryRow(`SELECT checksum FROM schema_migrations WHERE version=37`).Scan(&migration37Checksum); err != nil {
		t.Fatal(err)
	}
	hash := strings.Repeat("a", 64)
	if _, err := db.Exec(`
		DROP INDEX assisted_conclusion_receipts_task_created;
		DROP TABLE assisted_conclusion_receipts;
		CREATE TABLE assisted_conclusion_receipts (
			id TEXT PRIMARY KEY,
			task_id TEXT NOT NULL,
			continuation_id TEXT NOT NULL,
			source_session_id TEXT NOT NULL,
			source_turn_id TEXT NOT NULL,
			state TEXT NOT NULL,
			terminal_tool_result_count INTEGER NOT NULL,
			dispatch_request_id TEXT,
			control_turn_id TEXT,
			base_revision INTEGER,
			source_model_provider_id TEXT NOT NULL,
			source_model TEXT NOT NULL,
			source_reasoning_effort TEXT NOT NULL,
			canonical_result_json BLOB,
			canonical_result_sha256 TEXT,
			apply_idempotency_key TEXT,
			applied_revision INTEGER,
			created_at TEXT NOT NULL,
			updated_at TEXT NOT NULL
		);
		CREATE INDEX assisted_conclusion_receipts_task_created
			ON assisted_conclusion_receipts(task_id, created_at DESC);
		INSERT INTO assisted_conclusion_receipts VALUES
			('receipt-v37','task-1','continuation-1','session-1','turn-1','validated',4,
			 'dispatch-1','control-1',7,'provider-1','model-1','high',X'7B7D',?,
			 'apply-1',NULL,'2026-07-27T00:00:00Z','2026-07-27T00:01:00Z');
		DELETE FROM schema_migrations WHERE version>=38;
	`, hash); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	reopened, err := store.Open(path)
	if err != nil {
		t.Fatalf("upgrade v37 Store: %v", err)
	}
	defer reopened.Close()
	var state, dispatchID, controlTurnID, providerID, model, effort, resultHash, applyKey string
	var workWatermark, semanticWatermark, baseRevision int
	var canonical []byte
	if err := reopened.QueryRow(`SELECT state,source_work_watermark,semantic_persistence_watermark,
		dispatch_request_id,control_turn_id,base_revision,source_model_provider_id,source_model,
		source_reasoning_effort,canonical_result_json,canonical_result_sha256,apply_idempotency_key
		FROM assisted_conclusion_receipts WHERE id='receipt-v37'`).Scan(
		&state, &workWatermark, &semanticWatermark, &dispatchID, &controlTurnID, &baseRevision,
		&providerID, &model, &effort, &canonical, &resultHash, &applyKey); err != nil {
		t.Fatal(err)
	}
	if state != "validated" || workWatermark != 4 || semanticWatermark != 0 || dispatchID != "dispatch-1" ||
		controlTurnID != "control-1" || baseRevision != 7 || providerID != "provider-1" || model != "model-1" ||
		effort != "high" || !bytes.Equal(canonical, []byte("{}")) || resultHash != hash || applyKey != "apply-1" {
		t.Fatalf("migrated validated receipt lost state: state=%q watermarks=(%d,%d) dispatch=%q control=%q base=%d provider=%q model=%q effort=%q canonical=%q hash=%q apply=%q",
			state, workWatermark, semanticWatermark, dispatchID, controlTurnID, baseRevision, providerID, model, effort, canonical, resultHash, applyKey)
	}
	var preservedChecksum string
	if err := reopened.QueryRow(`SELECT checksum FROM schema_migrations WHERE version=37`).Scan(&preservedChecksum); err != nil {
		t.Fatal(err)
	}
	if preservedChecksum != migration37Checksum {
		t.Fatalf("migration 37 checksum changed: before %q after %q", migration37Checksum, preservedChecksum)
	}
}

func TestMigration39AddsDurableAssistedConclusionRecoveryState(t *testing.T) {
	path := filepath.Join(t.TempDir(), "pentest.db")
	db, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	var automaticTurns, repairCount, retryCount int
	var nextEligible, errorCode sql.NullString
	if err := db.QueryRow(`SELECT automatic_turn_count,repair_count,explicit_retry_count,next_eligible_at,error_code
		FROM assisted_conclusion_receipts LIMIT 1`).Scan(&automaticTurns, &repairCount, &retryCount, &nextEligible, &errorCode); err != sql.ErrNoRows {
		t.Fatalf("migration 39 columns unavailable: %v", err)
	}
	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM schema_migrations WHERE version=39`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("migration 39 count = %d, err=%v", count, err)
	}
	_ = db.Close()
	reopened, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
}

func TestMigration40AddsAssistedConclusionRetryIdempotencyHistory(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "pentest.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var migrationCount int
	if err := db.QueryRow(`SELECT COUNT(*) FROM schema_migrations WHERE version=40`).Scan(&migrationCount); err != nil || migrationCount != 1 {
		t.Fatalf("migration 40 count = %d, err=%v", migrationCount, err)
	}
	if _, err := db.Exec(`INSERT INTO assisted_conclusion_retry_keys
		(task_id,receipt_id,idempotency_key,dispatch_request_id,created_at) VALUES ('task-1','receipt-1','key-1','request-1','2026-07-27T00:00:00Z')`); err != nil {
		t.Fatalf("insert retry history: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO assisted_conclusion_retry_keys
		(task_id,receipt_id,idempotency_key,dispatch_request_id,created_at) VALUES ('task-1','receipt-1','key-1','request-2','2026-07-27T00:01:00Z')`); err == nil {
		t.Fatal("duplicate per-receipt retry key was accepted")
	}
}

func TestMigration41ScopesAssistedConclusionRetryKeysToTask(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "pentest.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var migrationCount int
	if err := db.QueryRow(`SELECT COUNT(*) FROM schema_migrations WHERE version=41`).Scan(&migrationCount); err != nil || migrationCount != 1 {
		t.Fatalf("migration 41 count = %d, err=%v", migrationCount, err)
	}
	if _, err := db.Exec(`INSERT INTO assisted_conclusion_retry_keys
		(task_id,receipt_id,idempotency_key,dispatch_request_id,created_at) VALUES ('task-1','receipt-1','key-1','request-1','2026-07-27T00:00:00Z')`); err != nil {
		t.Fatalf("insert task retry history: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO assisted_conclusion_retry_keys
		(task_id,receipt_id,idempotency_key,dispatch_request_id,created_at) VALUES ('task-1','receipt-2','key-1','request-2','2026-07-27T00:01:00Z')`); err == nil {
		t.Fatal("duplicate task-scoped retry key was accepted on a newer receipt")
	}
}

func TestMigration42ChecksumCoversCompleteDeterministicMutation(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "pentest.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var checksum string
	if err := db.QueryRow(`SELECT checksum FROM schema_migrations WHERE version=42`).Scan(&checksum); err != nil {
		t.Fatal(err)
	}
	oldCommentOnly := "\n-- action_required may precede provider acceptance for a claimed repair/retry.\n"
	sum := sha256.Sum256([]byte(oldCommentOnly))
	if checksum == hex.EncodeToString(sum[:]) {
		t.Fatal("migration 42 checksum still covers only its descriptive comment")
	}
	if len(checksum) != 64 {
		t.Fatalf("migration 42 checksum = %q", checksum)
	}
}

func TestMigration43AddsVersionRegenerationStateAndPreservesReceipts(t *testing.T) {
	path := filepath.Join(t.TempDir(), "pentest.db")
	db, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	hash := strings.Repeat("a", 64)
	if _, err := db.Exec(`INSERT INTO assisted_conclusion_receipts
		(id,task_id,continuation_id,source_request_id,source_request_correlation_exact,source_session_id,source_turn_id,state,source_work_watermark,
		 semantic_persistence_watermark,dispatch_request_id,control_turn_id,base_revision,source_model_provider_id,
		 source_model,source_reasoning_effort,canonical_result_json,canonical_result_sha256,apply_idempotency_key,
		 automatic_turn_count,repair_count,explicit_retry_count,send_attempt_count,send_started_at,created_at,updated_at)
		VALUES ('receipt-validated','task-1','continuation-1','source-request-1',1,'session-1','turn-1','validated',3,1,
		 'request-validated','control-validated',7,'provider-1','model-1','high',X'7B7D',?,'apply-validated',
		 1,0,0,1,'2026-07-27T12:00:30Z','2026-07-27T12:00:00Z','2026-07-27T12:01:00Z');
		INSERT INTO assisted_conclusion_receipts
		(id,task_id,continuation_id,source_request_id,source_request_correlation_exact,source_session_id,source_turn_id,state,source_work_watermark,
		 semantic_persistence_watermark,dispatch_request_id,base_revision,source_model_provider_id,source_model,
		 source_reasoning_effort,apply_idempotency_key,automatic_turn_count,repair_count,explicit_retry_count,
		 operator_retry_key,send_attempt_count,send_started_at,next_eligible_at,error_code,created_at,updated_at)
		VALUES ('receipt-action','task-1','continuation-1','source-request-2',1,'session-1','turn-2','action_required',4,1,
		 'request-action',8,'provider-1','model-1','high','apply-action',2,1,1,'retry-1',
		 1,'2026-07-27T12:01:00Z','2026-07-27T12:03:00Z','semantic_conclusion_repair_exhausted','2026-07-27T12:00:30Z','2026-07-27T12:02:00Z');
		INSERT INTO assisted_conclusion_retry_keys
		(task_id,receipt_id,idempotency_key,dispatch_request_id,created_at)
		VALUES ('task-1','receipt-action','retry-1','request-action','2026-07-27T12:02:00Z');

		CREATE TABLE assisted_conclusion_receipts_v42_fixture (
		 id TEXT PRIMARY KEY, task_id TEXT NOT NULL, continuation_id TEXT NOT NULL,
		 source_session_id TEXT NOT NULL, source_turn_id TEXT NOT NULL,
		 state TEXT NOT NULL CHECK (state IN ('clean','pending','dispatch_requested','repair_dispatch_requested','awaiting_result','action_required','validated','applied')),
		 source_work_watermark INTEGER NOT NULL CHECK (source_work_watermark >= 0),
		 semantic_persistence_watermark INTEGER NOT NULL CHECK (semantic_persistence_watermark >= 0),
		 dispatch_request_id TEXT UNIQUE, control_turn_id TEXT,
		 base_revision INTEGER CHECK (base_revision >= 0),
		 source_model_provider_id TEXT NOT NULL DEFAULT '', source_model TEXT NOT NULL DEFAULT '',
		 source_reasoning_effort TEXT NOT NULL DEFAULT '', canonical_result_json BLOB,
		 canonical_result_sha256 TEXT, apply_idempotency_key TEXT UNIQUE,
		 applied_revision INTEGER CHECK (applied_revision >= 0),
		 automatic_turn_count INTEGER NOT NULL DEFAULT 0 CHECK (automatic_turn_count >= 0),
		 repair_count INTEGER NOT NULL DEFAULT 0 CHECK (repair_count >= 0),
		 explicit_retry_count INTEGER NOT NULL DEFAULT 0 CHECK (explicit_retry_count >= 0),
		 operator_retry_key TEXT, next_eligible_at TEXT, error_code TEXT,
		 created_at TEXT NOT NULL, updated_at TEXT NOT NULL,
		 UNIQUE (task_id,continuation_id,source_turn_id),
		 CHECK ((state = 'clean' AND source_work_watermark = semantic_persistence_watermark) OR
		        (state <> 'clean' AND source_work_watermark > semantic_persistence_watermark)),
		 CHECK (
		  (state IN ('clean','pending') AND dispatch_request_id IS NULL AND control_turn_id IS NULL AND base_revision IS NULL AND canonical_result_json IS NULL AND canonical_result_sha256 IS NULL AND apply_idempotency_key IS NULL AND applied_revision IS NULL) OR
		  (state IN ('dispatch_requested','repair_dispatch_requested') AND dispatch_request_id IS NOT NULL AND control_turn_id IS NULL AND base_revision IS NOT NULL AND canonical_result_json IS NULL AND canonical_result_sha256 IS NULL AND apply_idempotency_key IS NOT NULL AND applied_revision IS NULL) OR
		  (state = 'awaiting_result' AND dispatch_request_id IS NOT NULL AND control_turn_id IS NOT NULL AND base_revision IS NOT NULL AND canonical_result_json IS NULL AND canonical_result_sha256 IS NULL AND apply_idempotency_key IS NOT NULL AND applied_revision IS NULL) OR
		  (state = 'action_required' AND dispatch_request_id IS NOT NULL AND base_revision IS NOT NULL AND canonical_result_json IS NULL AND canonical_result_sha256 IS NULL AND apply_idempotency_key IS NOT NULL AND applied_revision IS NULL) OR
		  (state = 'validated' AND dispatch_request_id IS NOT NULL AND control_turn_id IS NOT NULL AND base_revision IS NOT NULL AND canonical_result_json IS NOT NULL AND length(canonical_result_json) > 0 AND canonical_result_sha256 IS NOT NULL AND length(canonical_result_sha256) = 64 AND apply_idempotency_key IS NOT NULL AND applied_revision IS NULL) OR
		  (state = 'applied' AND dispatch_request_id IS NOT NULL AND control_turn_id IS NOT NULL AND base_revision IS NOT NULL AND canonical_result_json IS NOT NULL AND length(canonical_result_json) > 0 AND canonical_result_sha256 IS NOT NULL AND length(canonical_result_sha256) = 64 AND apply_idempotency_key IS NOT NULL AND applied_revision IS NOT NULL)
		 ),
		 CHECK ((state = 'action_required' AND error_code IS NOT NULL AND next_eligible_at IS NOT NULL) OR state <> 'action_required')
		);
		INSERT INTO assisted_conclusion_receipts_v42_fixture
		 SELECT id,task_id,continuation_id,source_session_id,source_turn_id,state,source_work_watermark,
		 semantic_persistence_watermark,dispatch_request_id,control_turn_id,base_revision,source_model_provider_id,
		 source_model,source_reasoning_effort,canonical_result_json,canonical_result_sha256,apply_idempotency_key,
		 applied_revision,automatic_turn_count,repair_count,explicit_retry_count,operator_retry_key,next_eligible_at,
		 error_code,created_at,updated_at FROM assisted_conclusion_receipts;
		DROP INDEX assisted_conclusion_receipts_task_created;
		DROP TABLE assisted_conclusion_receipts;
		ALTER TABLE assisted_conclusion_receipts_v42_fixture RENAME TO assisted_conclusion_receipts;
		CREATE INDEX assisted_conclusion_receipts_task_created ON assisted_conclusion_receipts(task_id,created_at DESC);
		DELETE FROM schema_migrations WHERE version=43;
	`, hash); err != nil {
		t.Fatalf("build migration 42 fixture: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	reopened, err := store.Open(path)
	if err != nil {
		t.Fatalf("upgrade v42 assisted conclusion receipts: %v", err)
	}
	defer reopened.Close()
	var migrationCount int
	if err := reopened.QueryRow(`SELECT COUNT(*) FROM schema_migrations WHERE version=43`).Scan(&migrationCount); err != nil || migrationCount != 1 {
		t.Fatalf("migration 43 count = %d, err=%v", migrationCount, err)
	}
	var state, dispatchID, controlTurnID, providerID, model, effort, resultHash, applyKey string
	var canonical []byte
	var baseRevision, automaticTurns, repairCount, retryCount, versionRegenerationCount int
	if err := reopened.QueryRow(`SELECT state,dispatch_request_id,control_turn_id,base_revision,
		source_model_provider_id,source_model,source_reasoning_effort,canonical_result_json,
		canonical_result_sha256,apply_idempotency_key,automatic_turn_count,repair_count,
		explicit_retry_count,version_regeneration_count FROM assisted_conclusion_receipts WHERE id='receipt-validated'`).Scan(
		&state, &dispatchID, &controlTurnID, &baseRevision, &providerID, &model, &effort, &canonical,
		&resultHash, &applyKey, &automaticTurns, &repairCount, &retryCount, &versionRegenerationCount); err != nil {
		t.Fatal(err)
	}
	if state != "validated" || dispatchID != "request-validated" || controlTurnID != "control-validated" ||
		baseRevision != 7 || providerID != "provider-1" || model != "model-1" || effort != "high" ||
		!bytes.Equal(canonical, []byte("{}")) || resultHash != hash || applyKey != "apply-validated" ||
		automaticTurns != 1 || repairCount != 0 || retryCount != 0 || versionRegenerationCount != 0 {
		t.Fatalf("migrated validated receipt lost lineage: state=%q request=%q control=%q base=%d provider=%q model=%q effort=%q canonical=%q hash=%q apply=%q counts=(%d,%d,%d,%d)",
			state, dispatchID, controlTurnID, baseRevision, providerID, model, effort, canonical, resultHash, applyKey,
			automaticTurns, repairCount, retryCount, versionRegenerationCount)
	}
	var operatorKey, nextEligible, errorCode string
	if err := reopened.QueryRow(`SELECT state,dispatch_request_id,base_revision,apply_idempotency_key,
		automatic_turn_count,repair_count,explicit_retry_count,version_regeneration_count,operator_retry_key,
		next_eligible_at,error_code FROM assisted_conclusion_receipts WHERE id='receipt-action'`).Scan(
		&state, &dispatchID, &baseRevision, &applyKey, &automaticTurns, &repairCount, &retryCount,
		&versionRegenerationCount, &operatorKey, &nextEligible, &errorCode); err != nil {
		t.Fatal(err)
	}
	if state != "action_required" || dispatchID != "request-action" || baseRevision != 8 || applyKey != "apply-action" ||
		automaticTurns != 2 || repairCount != 1 || retryCount != 1 || versionRegenerationCount != 0 ||
		operatorKey != "retry-1" || nextEligible != "2026-07-27T12:03:00Z" || errorCode != "semantic_conclusion_repair_exhausted" {
		t.Fatalf("migrated action receipt lost recovery state: state=%q request=%q base=%d apply=%q counts=(%d,%d,%d,%d) operator=%q next=%q error=%q",
			state, dispatchID, baseRevision, applyKey, automaticTurns, repairCount, retryCount, versionRegenerationCount,
			operatorKey, nextEligible, errorCode)
	}
	var retryReceiptID string
	if err := reopened.QueryRow(`SELECT receipt_id FROM assisted_conclusion_retry_keys WHERE task_id='task-1' AND idempotency_key='retry-1'`).Scan(&retryReceiptID); err != nil || retryReceiptID != "receipt-action" {
		t.Fatalf("retry lineage = %q, err=%v", retryReceiptID, err)
	}
	if _, err := reopened.Exec(`INSERT INTO assisted_conclusion_receipts
		(id,task_id,continuation_id,source_session_id,source_turn_id,state,source_work_watermark,
		 semantic_persistence_watermark,dispatch_request_id,base_revision,source_model_provider_id,source_model,
		 source_reasoning_effort,apply_idempotency_key,version_regeneration_count,error_code,next_eligible_at,created_at,updated_at)
		VALUES ('receipt-version','task-1','continuation-1','session-1','turn-3','version_regeneration_dispatch_requested',5,
		 1,'request-version',9,'provider-1','model-1','high','apply-version',1,'semantic_conclusion_version_conflict',
		 '2026-07-27T12:04:00Z','2026-07-27T12:03:00Z','2026-07-27T12:03:00Z')`); err != nil {
		t.Fatalf("insert migrated version regeneration state: %v", err)
	}
	if _, err := reopened.Exec(`UPDATE assisted_conclusion_receipts SET version_regeneration_count=2 WHERE id='receipt-version'`); err == nil {
		t.Fatal("schema accepted more than one automatic version regeneration")
	}
}

func TestMigration44PreservesV43RecoveryLineageAndMarksExistingDispatchAttempted(t *testing.T) {
	path := filepath.Join(t.TempDir(), "pentest.db")
	db, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	hash := strings.Repeat("b", 64)
	if _, err := db.Exec(`
		DROP INDEX assisted_conclusion_receipts_task_created;
		DROP TABLE assisted_conclusion_receipts;
		CREATE TABLE assisted_conclusion_receipts (
		 id TEXT PRIMARY KEY, task_id TEXT NOT NULL, continuation_id TEXT NOT NULL,
		 source_session_id TEXT NOT NULL, source_turn_id TEXT NOT NULL, state TEXT NOT NULL,
		 source_work_watermark INTEGER NOT NULL, semantic_persistence_watermark INTEGER NOT NULL,
		 dispatch_request_id TEXT UNIQUE, control_turn_id TEXT, base_revision INTEGER,
		 source_model_provider_id TEXT NOT NULL, source_model TEXT NOT NULL, source_reasoning_effort TEXT NOT NULL,
		 canonical_result_json BLOB, canonical_result_sha256 TEXT, apply_idempotency_key TEXT UNIQUE,
		 applied_revision INTEGER, automatic_turn_count INTEGER NOT NULL, repair_count INTEGER NOT NULL,
		 version_regeneration_count INTEGER NOT NULL, explicit_retry_count INTEGER NOT NULL,
		 operator_retry_key TEXT, next_eligible_at TEXT, error_code TEXT,
		 created_at TEXT NOT NULL, updated_at TEXT NOT NULL,
		 UNIQUE(task_id,continuation_id,source_turn_id)
		);
		CREATE INDEX assisted_conclusion_receipts_task_created ON assisted_conclusion_receipts(task_id,created_at DESC);
		INSERT INTO assisted_conclusion_receipts VALUES
		 ('receipt-v43','task-v43','continuation-v43','session-v43','turn-v43','version_sync_requested',5,2,
		  'request-v43','control-v43',9,'provider-v43','model-v43','high',X'7B7D',?,'apply-v43',NULL,
		  2,0,0,1,'operator-v43','2026-07-27T12:30:00Z','semantic_conclusion_version_conflict',
		  '2026-07-27T12:00:00Z','2026-07-27T12:10:00Z');
		DELETE FROM schema_migrations WHERE version=44;
	`, hash); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	reopened, err := store.Open(path)
	if err != nil {
		t.Fatalf("upgrade v43 Store: %v", err)
	}
	defer reopened.Close()
	var sourceRequestID, state, dispatchID, controlID, providerID, model, effort, resultHash, applyKey string
	var canonical []byte
	var baseRevision, automaticTurns, repairCount, versionCount, retryCount, sendCount, sourceCorrelationExact int
	var synchronizedRevision sql.NullInt64
	var sendStartedAt, nextEligible, errorCode string
	if err := reopened.QueryRow(`SELECT source_request_id,source_request_correlation_exact,state,dispatch_request_id,control_turn_id,base_revision,
		synchronized_revision,source_model_provider_id,source_model,source_reasoning_effort,canonical_result_json,
		canonical_result_sha256,apply_idempotency_key,automatic_turn_count,repair_count,version_regeneration_count,
		explicit_retry_count,send_attempt_count,send_started_at,next_eligible_at,error_code
		FROM assisted_conclusion_receipts WHERE id='receipt-v43'`).Scan(&sourceRequestID, &sourceCorrelationExact, &state, &dispatchID,
		&controlID, &baseRevision, &synchronizedRevision, &providerID, &model, &effort, &canonical, &resultHash,
		&applyKey, &automaticTurns, &repairCount, &versionCount, &retryCount, &sendCount, &sendStartedAt,
		&nextEligible, &errorCode); err != nil {
		t.Fatal(err)
	}
	if sourceRequestID != "legacy:session-v43:turn-v43" || sourceCorrelationExact != 0 || state != "version_sync_requested" ||
		dispatchID != "request-v43" || controlID != "control-v43" || baseRevision != 9 || synchronizedRevision.Valid ||
		providerID != "provider-v43" || model != "model-v43" || effort != "high" ||
		!bytes.Equal(canonical, []byte("{}")) || resultHash != hash || applyKey != "apply-v43" ||
		automaticTurns != 2 || repairCount != 0 || versionCount != 0 || retryCount != 1 || sendCount != 1 ||
		sendStartedAt != "2026-07-27T12:10:00Z" || nextEligible != "2026-07-27T12:30:00Z" ||
		errorCode != "semantic_conclusion_version_conflict" {
		t.Fatalf("migrated v43 receipt lost recovery lineage: source=%q state=%q request=%q control=%q base=%d sync=%v provider=%q model=%q effort=%q canonical=%q hash=%q apply=%q counts=(%d,%d,%d,%d,%d) send_at=%q eligible=%q error=%q",
			sourceRequestID, state, dispatchID, controlID, baseRevision, synchronizedRevision, providerID, model, effort,
			canonical, resultHash, applyKey, automaticTurns, repairCount, versionCount, retryCount, sendCount,
			sendStartedAt, nextEligible, errorCode)
	}
}

func TestOpenRepairsMissingMigrationRowsForCurrentWatermarkSchema(t *testing.T) {
	path := filepath.Join(t.TempDir(), "pentest.db")
	db, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`
		INSERT INTO assisted_conclusion_receipts
			(id,task_id,continuation_id,source_request_id,source_request_correlation_exact,source_session_id,source_turn_id,state,source_work_watermark,
			 semantic_persistence_watermark,source_model_provider_id,source_model,source_reasoning_effort,created_at,updated_at)
		VALUES ('receipt-current','task-1','continuation-1','source-request-1',1,'session-1','turn-1','clean',5,5,
			'provider-1','model-1','high','2026-07-27T00:00:00Z','2026-07-27T00:00:00Z');
		DELETE FROM schema_migrations WHERE version>=37;
	`); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	reopened, err := store.Open(path)
	if err != nil {
		t.Fatalf("repair current receipt schema migration ledger: %v", err)
	}
	defer reopened.Close()
	var work, semantic, migrationRows int
	if err := reopened.QueryRow(`SELECT source_work_watermark,semantic_persistence_watermark
		FROM assisted_conclusion_receipts WHERE id='receipt-current'`).Scan(&work, &semantic); err != nil {
		t.Fatal(err)
	}
	if err := reopened.QueryRow(`SELECT COUNT(*) FROM schema_migrations WHERE version IN (37,38)`).Scan(&migrationRows); err != nil {
		t.Fatal(err)
	}
	if work != 5 || semantic != 5 || migrationRows != 2 {
		t.Fatalf("repaired Store = watermarks (%d,%d), migration rows %d", work, semantic, migrationRows)
	}
}

func TestOpenDoesNotBlessPartialWatermarkSchemaAsMigration38(t *testing.T) {
	path := filepath.Join(t.TempDir(), "pentest.db")
	db, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`
		DROP INDEX assisted_conclusion_receipts_task_created;
		DROP TABLE assisted_conclusion_receipts;
		CREATE TABLE assisted_conclusion_receipts (
			id TEXT PRIMARY KEY,
			source_work_watermark INTEGER NOT NULL,
			semantic_persistence_watermark INTEGER NOT NULL
		);
		CREATE INDEX assisted_conclusion_receipts_task_created
			ON assisted_conclusion_receipts(id);
		DELETE FROM schema_migrations WHERE version>=37;
	`); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	reopened, err := store.Open(path)
	if reopened != nil {
		_ = reopened.Close()
	}
	if err == nil {
		t.Fatal("partial watermark table was blessed as the current migration 38 schema")
	}
}

// TestOpenRestrictsDatabaseFilePermissions pins issue #160: the database stores
// plaintext "literal" credential secrets in credential_bindings.source_json, so
// the main file and its WAL/SHM sidecars must be owner-only (0600) rather than
// the umask default (typically 0644) that other local OS users could read.
func TestOpenRestrictsDatabaseFilePermissions(t *testing.T) {
	path := filepath.Join(t.TempDir(), "pentest.db")

	db, err := store.Open(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	for _, suffix := range []string{"", "-wal", "-shm"} {
		candidate := path + suffix
		info, err := os.Stat(candidate)
		if err != nil {
			if os.IsNotExist(err) && suffix != "" {
				continue // sidecar not present; nothing to assert
			}
			t.Fatalf("stat %s: %v", candidate, err)
		}
		if mode := info.Mode().Perm(); mode != 0o600 {
			t.Fatalf("expected %s mode 0600, got %04o", filepath.Base(candidate), mode)
		}
	}
}

func TestOpenActiveV2StoreDoesNotRetainRemovedWorkflowState(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "contracted.db"))
	if err != nil {
		t.Fatalf("open contracted store: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	if tableExists(t, db.DB, "task_summary_versions") {
		t.Fatal("active v2 store retains removed task_summary_versions table")
	}
	for _, column := range []string{
		"blackboard_finish_summary_version_id",
		"blackboard_finish_graph_revision",
		"blackboard_finish_mutation_sequence",
		"blackboard_finished_at",
	} {
		if columnExists(t, db.DB, "task_continuations", column) {
			t.Fatalf("active v2 store retains removed task_continuations.%s column", column)
		}
	}
}

// TestOpenRefusalDoesNotUpgradePreNumberedLegacyDatabase simulates a database
// created before numbered migrations and proves ordinary daemon/runtime open
// leaves it untouched for the offline migrator.
func TestOpenRefusalDoesNotUpgradePreNumberedLegacyDatabase(t *testing.T) {
	path := filepath.Join(t.TempDir(), "pentest.db")

	// Build a legacy schema that predates the defaults_json column and insert a
	// row the way the very first migration would have.
	legacy, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("open legacy db: %v", err)
	}
	_, err = legacy.Exec(`CREATE TABLE projects (
		id TEXT PRIMARY KEY,
		name TEXT NOT NULL,
		description TEXT NOT NULL DEFAULT '',
		scope_json TEXT NOT NULL,
		created_at TEXT NOT NULL,
		updated_at TEXT NOT NULL
	);`)
	if err != nil {
		t.Fatalf("create legacy table: %v", err)
	}
	_, err = legacy.Exec(`INSERT INTO projects (id, name, description, scope_json, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?)`,
		"legacy-1", "Legacy", "", "{}", "2026-01-01T00:00:00Z", "2026-01-01T00:00:00Z")
	if err != nil {
		t.Fatalf("insert legacy row: %v", err)
	}
	if err := legacy.Close(); err != nil {
		t.Fatalf("close legacy db: %v", err)
	}

	// Ordinary Open must not add numbered migrations or bootstrap v2 over this
	// source. The explicit offline migration-source path owns that work.
	upgraded, err := store.Open(path)
	if upgraded != nil {
		_ = upgraded.Close()
		t.Fatal("ordinary Open upgraded a pre-numbered v1 database")
	}
	if err == nil || !strings.Contains(err.Error(), "blackboard v2 inspect") {
		t.Fatalf("ordinary Open error = %v, want offline v2 migration guidance", err)
	}

	untouched, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("reopen untouched legacy db: %v", err)
	}
	t.Cleanup(func() { _ = untouched.Close() })

	var id string
	err = untouched.QueryRow("SELECT id FROM projects WHERE id = ?", "legacy-1").Scan(&id)
	if err != nil {
		t.Fatalf("read legacy row after refused open: %v", err)
	}
	if id != "legacy-1" {
		t.Fatalf("expected legacy-1, got %q", id)
	}
	if columnExists(t, untouched, "projects", "defaults_json") {
		t.Fatal("refused ordinary Open added projects.defaults_json")
	}
}

// TestOpenRejectsMigrationChecksumDriftWithoutChangingLegacyBlackboard is the
// C01 first red test: an applied migration whose checksum no longer matches the
// embedded definition fails closed, and legacy Blackboard rows stay untouched.
func TestOpenRejectsMigrationChecksumDriftWithoutChangingLegacyBlackboard(t *testing.T) {
	path := filepath.Join(t.TempDir(), "pentest.db")

	db, err := store.Open(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}

	const (
		projectID   = "proj-1"
		projectName = "must-survive-checksum-drift"
	)
	_, err = db.Exec(`INSERT INTO projects (id, name, description, scope_json, defaults_json, created_at, updated_at)
		VALUES (?, ?, '', '{}', '{}', ?, ?)`,
		projectID, projectName, "2026-01-01T00:00:00Z", "2026-01-01T00:00:00Z")
	if err != nil {
		t.Fatalf("insert project: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	raw, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("open raw: %v", err)
	}
	res, err := raw.Exec(`UPDATE schema_migrations SET checksum = ? WHERE version = 1`, "deadbeef-checksum-drift")
	if err != nil {
		t.Fatalf("tamper checksum: %v", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		t.Fatalf("rows affected: %v", err)
	}
	if n != 1 {
		t.Fatalf("expected to tamper exactly one migration row, got %d", n)
	}
	if err := raw.Close(); err != nil {
		t.Fatalf("close raw: %v", err)
	}

	_, err = store.Open(path)
	if err == nil {
		t.Fatal("expected Open to reject migration checksum drift")
	}
	if !strings.Contains(strings.ToLower(err.Error()), "checksum") {
		t.Fatalf("expected checksum error, got: %v", err)
	}

	// Fail-closed must not rewrite active rows.
	verify, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("open verify: %v", err)
	}
	t.Cleanup(func() { _ = verify.Close() })

	var gotName string
	err = verify.QueryRow(
		`SELECT name FROM projects WHERE id = ?`, projectID,
	).Scan(&gotName)
	if err != nil {
		t.Fatalf("project after failed open: %v", err)
	}
	if gotName != projectName {
		t.Fatalf("project changed: got %q want %q", gotName, projectName)
	}
}

// TestOpenRejectsUnknownNewerSchemaVersion fails closed when the database has
// applied a migration version the running binary does not know.
func TestOpenRejectsUnknownNewerSchemaVersion(t *testing.T) {
	path := filepath.Join(t.TempDir(), "pentest.db")

	db, err := store.Open(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	raw, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("open raw: %v", err)
	}
	_, err = raw.Exec(
		`INSERT INTO schema_migrations (version, name, checksum, applied_at) VALUES (?, ?, ?, ?)`,
		9999, "future", "abc", time.Now().UTC().Format(time.RFC3339Nano),
	)
	if err != nil {
		t.Fatalf("insert future migration: %v", err)
	}
	if err := raw.Close(); err != nil {
		t.Fatalf("close raw: %v", err)
	}

	_, err = store.Open(path)
	if err == nil {
		t.Fatal("expected Open to reject unknown newer schema version")
	}
	errText := strings.ToLower(err.Error())
	if !strings.Contains(errText, "newer") && !strings.Contains(errText, "unknown") {
		t.Fatalf("expected unknown/newer schema error, got: %v", err)
	}
}

// TestOpenDefaultsCanonicalStoreToBlackboardV2 records the accepted epoch on
// every fresh database while proving no v1 table is dropped during bootstrap.
func TestOpenDefaultsCanonicalStoreToBlackboardV2(t *testing.T) {
	path := filepath.Join(t.TempDir(), "pentest.db")

	db, err := store.Open(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	epoch, err := db.CanonicalStore()
	if err != nil {
		t.Fatalf("CanonicalStore: %v", err)
	}
	if epoch != store.CanonicalStoreBlackboardV2 {
		t.Fatalf("canonical store: got %q want %q", epoch, store.CanonicalStoreBlackboardV2)
	}

	// A fresh active store contains only v2 semantic state and trusted control
	// records; the historical v1 graph ledger is not installed.
	for _, table := range []string{
		"schema_migrations",
		"blackboard_store_state",
		"blackboard_read_state",
		"blackboard_v2_project_state",
		"blackboard_v2_records",
		"blackboard_v2_record_history",
		"blackboard_v2_relationships",
		"blackboard_v2_relationship_history",
		"blackboard_v2_idempotency_receipts",
		"blackboard_v2_attempt_origins",
		"blackboard_v2_evidence_requests",
		"blackboard_v2_evidence_payloads",
		"blackboard_v2_key_redirects",
	} {
		if !tableExists(t, db.DB, table) {
			t.Fatalf("expected table %s", table)
		}
	}
	for _, table := range []string{"blackboard_graph_mutations", "blackboard_nodes", "blackboard_migration_runs"} {
		if tableExists(t, db.DB, table) {
			t.Fatalf("retired table %s remains", table)
		}
	}

	// Trusted runtime and v2 evidence columns are present.
	for _, col := range []struct {
		table, column string
	}{
		{"projects", "kind"},
		{"task_events", "continuation_id"},
		{"task_events", "attempt_node_id"},
		{"task_continuations", "runtime_config_version_id"},
		{"task_continuations", "blackboard_reconciliation_status"},
		{"task_continuations", "blackboard_reconciliation_mutation_id"},
		{"task_continuations", "blackboard_reconciled_at"},
		{"blackboard_v2_idempotency_receipts", "continuation_id"},
		{"blackboard_v2_evidence_requests", "temp_internal_path"},
		{"blackboard_v2_evidence_requests", "publisher_token"},
		{"blackboard_v2_evidence_requests", "publisher_temp_identity"},
		{"blackboard_v2_evidence_requests", "previous_temp_internal_path"},
		{"blackboard_v2_evidence_requests", "migration27_temp_internal_path"},
	} {
		if !columnExists(t, db.DB, col.table, col.column) {
			t.Fatalf("expected column %s.%s", col.table, col.column)
		}
	}
}

// TestOpenRefusalPreservesEveryRowFromOlderDatabase proves ordinary open does
// not mutate a pre-numbered v1 source before the offline migrator handles it.
func TestOpenRefusalPreservesEveryRowFromOlderDatabase(t *testing.T) {
	path := filepath.Join(t.TempDir(), "pentest.db")

	legacy, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("open legacy: %v", err)
	}
	stmts := []string{
		`CREATE TABLE projects (
			id TEXT PRIMARY KEY,
			name TEXT NOT NULL,
			description TEXT NOT NULL DEFAULT '',
			scope_json TEXT NOT NULL,
			defaults_json TEXT NOT NULL DEFAULT '{}',
			created_at TEXT NOT NULL,
			updated_at TEXT NOT NULL
		);`,
		`CREATE TABLE project_facts (
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
		);`,
		`CREATE TABLE findings (
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
		);`,
		`CREATE TABLE evidence_artifacts (
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
		);`,
	}
	for _, s := range stmts {
		if _, err := legacy.Exec(s); err != nil {
			t.Fatalf("legacy ddl: %v", err)
		}
	}
	_, err = legacy.Exec(`INSERT INTO projects (id, name, description, scope_json, defaults_json, created_at, updated_at)
		VALUES ('p1', 'Old', '', '{}', '{}', '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z')`)
	if err != nil {
		t.Fatalf("insert project: %v", err)
	}
	_, err = legacy.Exec(`INSERT INTO project_facts (
		id, project_id, fact_key, category, summary, body, confidence, scope_status, created_at, updated_at
	) VALUES ('f1', 'p1', 'k1', 'cat', 'sum', 'body', 'confirmed', '', '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z')`)
	if err != nil {
		t.Fatalf("insert fact: %v", err)
	}
	_, err = legacy.Exec(`INSERT INTO findings (
		id, project_id, finding_key, version, title, description, status, created_at, updated_at
	) VALUES ('n1', 'p1', 'fk1', 1, 'title', '', 'open', '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z')`)
	if err != nil {
		t.Fatalf("insert finding: %v", err)
	}
	_, err = legacy.Exec(`INSERT INTO evidence_artifacts (
		id, project_id, evidence_key, attach_to_type, attach_to_key, artifact_type, managed_path, created_at, updated_at
	) VALUES ('e1', 'p1', 'ev1', 'fact', 'k1', 'note', '/a', '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z')`)
	if err != nil {
		t.Fatalf("insert evidence: %v", err)
	}
	if err := legacy.Close(); err != nil {
		t.Fatalf("close legacy: %v", err)
	}

	opened, err := store.Open(path)
	if opened != nil {
		_ = opened.Close()
		t.Fatal("ordinary Open upgraded a pre-numbered v1 database")
	}
	if err == nil || !strings.Contains(err.Error(), "blackboard v2 inspect") {
		t.Fatalf("ordinary Open error = %v, want offline v2 migration guidance", err)
	}

	untouched, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("reopen untouched v1 source: %v", err)
	}
	t.Cleanup(func() { _ = untouched.Close() })
	assertCount(t, untouched, "projects", 1)
	assertCount(t, untouched, "project_facts", 1)
	assertCount(t, untouched, "findings", 1)
	assertCount(t, untouched, "evidence_artifacts", 1)
	if columnExists(t, untouched, "projects", "kind") {
		t.Fatal("refused ordinary Open added projects.kind")
	}
}

// TestTransactionConnectionReportsRequiredPragmas proves every transaction
// connection carries the storage-contract PRAGMAs and immediate lock mode.
func TestTransactionConnectionReportsRequiredPragmas(t *testing.T) {
	path := filepath.Join(t.TempDir(), "pentest.db")
	db, err := store.Open(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	tx, err := db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer func() { _ = tx.Rollback() }()

	var foreignKeys int
	if err := tx.QueryRow(`PRAGMA foreign_keys`).Scan(&foreignKeys); err != nil {
		t.Fatalf("foreign_keys: %v", err)
	}
	if foreignKeys != 1 {
		t.Fatalf("foreign_keys: got %d want 1", foreignKeys)
	}

	var busyTimeout int
	if err := tx.QueryRow(`PRAGMA busy_timeout`).Scan(&busyTimeout); err != nil {
		t.Fatalf("busy_timeout: %v", err)
	}
	if busyTimeout < 5000 {
		t.Fatalf("busy_timeout: got %d want >= 5000", busyTimeout)
	}

	var synchronous int
	if err := tx.QueryRow(`PRAGMA synchronous`).Scan(&synchronous); err != nil {
		t.Fatalf("synchronous: %v", err)
	}
	// FULL == 2
	if synchronous != 2 {
		t.Fatalf("synchronous: got %d want 2 (FULL)", synchronous)
	}

	var journalMode string
	if err := tx.QueryRow(`PRAGMA journal_mode`).Scan(&journalMode); err != nil {
		t.Fatalf("journal_mode: %v", err)
	}
	if !strings.EqualFold(journalMode, "wal") {
		t.Fatalf("journal_mode: got %q want wal", journalMode)
	}

	// Immediate lock: a second connection must not acquire an exclusive write
	// lock while this transaction holds the reserved write lock without having
	// written yet (DEFERRED would still allow it until first write).
	second, err := sql.Open("sqlite", path+"?_pragma=busy_timeout(100)&_txlock=immediate")
	if err != nil {
		t.Fatalf("open second: %v", err)
	}
	t.Cleanup(func() { _ = second.Close() })
	second.SetMaxOpenConns(1)

	done := make(chan error, 1)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
		defer cancel()
		tx2, err := second.BeginTx(ctx, nil)
		if err != nil {
			done <- err
			return
		}
		_ = tx2.Rollback()
		done <- nil
	}()

	select {
	case err := <-done:
		if err == nil {
			t.Fatal("expected second IMMEDIATE begin to block/fail while first transaction holds write lock")
		}
	case <-time.After(2 * time.Second):
		// Blocked past timeout is also proof of immediate locking.
	}
}

func tableExists(t *testing.T, db *sql.DB, name string) bool {
	t.Helper()
	var n int
	err := db.QueryRow(
		`SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = ?`,
		name,
	).Scan(&n)
	if err != nil {
		t.Fatalf("tableExists %s: %v", name, err)
	}
	return n == 1
}

func columnExists(t *testing.T, db *sql.DB, table, column string) bool {
	t.Helper()
	rows, err := db.Query(`PRAGMA table_info(` + table + `)`)
	if err != nil {
		t.Fatalf("table_info %s: %v", table, err)
	}
	defer rows.Close()
	for rows.Next() {
		var cid, notNull, pk int
		var name, ctype string
		var dflt sql.NullString
		if err := rows.Scan(&cid, &name, &ctype, &notNull, &dflt, &pk); err != nil {
			t.Fatalf("scan table_info: %v", err)
		}
		if name == column {
			return true
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("table_info rows: %v", err)
	}
	return false
}

func assertCount(t *testing.T, db *sql.DB, table string, want int) {
	t.Helper()
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM ` + table).Scan(&n); err != nil {
		t.Fatalf("count %s: %v", table, err)
	}
	if n != want {
		t.Fatalf("%s count: got %d want %d", table, n, want)
	}
}

func TestBlackboardReadCursorSecretIsDatabaseSpecificAndPersistsAcrossReopen(t *testing.T) {
	firstPath := filepath.Join(t.TempDir(), "first.db")
	first, err := store.Open(firstPath)
	if err != nil {
		t.Fatalf("open first store: %v", err)
	}
	var before []byte
	if err := first.QueryRow(`SELECT cursor_secret FROM blackboard_read_state WHERE id=1`).Scan(&before); err != nil {
		t.Fatalf("read first cursor secret: %v", err)
	}
	if len(before) != 32 {
		t.Fatalf("cursor secret length = %d want 32", len(before))
	}
	if err := first.Close(); err != nil {
		t.Fatalf("close first store: %v", err)
	}
	first, err = store.Open(firstPath)
	if err != nil {
		t.Fatalf("reopen first store: %v", err)
	}
	defer first.Close()
	var after []byte
	if err := first.QueryRow(`SELECT cursor_secret FROM blackboard_read_state WHERE id=1`).Scan(&after); err != nil {
		t.Fatalf("read persisted cursor secret: %v", err)
	}
	if !bytes.Equal(before, after) {
		t.Fatal("cursor secret changed across reopen")
	}

	second, err := store.Open(filepath.Join(t.TempDir(), "second.db"))
	if err != nil {
		t.Fatalf("open second store: %v", err)
	}
	defer second.Close()
	var other []byte
	if err := second.QueryRow(`SELECT cursor_secret FROM blackboard_read_state WHERE id=1`).Scan(&other); err != nil {
		t.Fatalf("read second cursor secret: %v", err)
	}
	if bytes.Equal(before, other) {
		t.Fatal("independent databases share the same cursor secret")
	}
}

// TestMigration53ConvertsLegacyReceiptsIntoObligationsAndDispatches proves the
// legacy mutable assisted-conclusion receipt migrates into one durable
// Pending Blackboard Conclusion obligation plus one immutable Conclusion
// Dispatch that preserves the binding, correlation, watermarks, counters,
// errors, cooldowns, canonical result data, and timestamps (ADR 0021).
func TestMigration53ConvertsLegacyReceiptsIntoObligationsAndDispatches(t *testing.T) {
	path := filepath.Join(t.TempDir(), "pentest.db")
	db, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	hash := strings.Repeat("c", 64)
	if _, err := db.Exec(`
		DELETE FROM schema_migrations WHERE version>=53;
		DROP TABLE IF EXISTS session_conclusion_dispatches;
		DROP TABLE IF EXISTS session_pending_blackboard_conclusions;
		DROP TABLE IF EXISTS conclusion_dispatches;
		DROP TABLE IF EXISTS pending_blackboard_conclusions;
		INSERT INTO assisted_conclusion_receipts
			(id,task_id,continuation_id,source_request_id,source_request_correlation_exact,source_session_id,source_turn_id,state,source_work_watermark,
			 semantic_persistence_watermark,dispatch_request_id,control_turn_id,base_revision,source_model_provider_id,
			 source_model,source_reasoning_effort,canonical_result_json,canonical_result_sha256,apply_idempotency_key,
			 automatic_turn_count,repair_count,version_regeneration_count,explicit_retry_count,operator_retry_key,
			 send_attempt_count,send_started_at,next_eligible_at,error_code,created_at,updated_at)
			VALUES ('legacy-validated','task-legacy','continuation-legacy','legacy:session-legacy:turn-legacy',0,'session-legacy','turn-legacy','validated',5,2,
			 'dispatch-legacy','control-legacy',9,'provider-legacy','model-legacy','high',X'7B7D',?,'apply-legacy',
			 2,1,0,1,'operator-legacy',1,'2026-07-27T12:00:30Z','2026-07-27T12:03:00Z','semantic_conclusion_invalid_result',
			 '2026-07-27T12:00:00Z','2026-07-27T12:01:00Z');
	`, hash); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	reopened, err := store.Open(path)
	if err != nil {
		t.Fatalf("reopen migrated store: %v", err)
	}
	defer reopened.Close()

	var state, sourceRequestID, sourceSessionID, sourceTurnID, provider, model, effort, resultHash, applyKey string
	var sourceCorrelationExact, work, semantic, automaticTurns, repairCount, versionCount, retryCount int
	var canonical []byte
	if err := reopened.QueryRow(`SELECT state,source_request_id,source_request_correlation_exact,source_session_id,source_turn_id,
		source_model_provider_id,source_model,source_reasoning_effort,canonical_result_json,canonical_result_sha256,
		apply_idempotency_key,source_work_watermark,semantic_persistence_watermark,automatic_turn_count,repair_count,
		version_regeneration_count,explicit_retry_count
		FROM pending_blackboard_conclusions WHERE id='legacy-validated'`).Scan(
		&state, &sourceRequestID, &sourceCorrelationExact, &sourceSessionID, &sourceTurnID,
		&provider, &model, &effort, &canonical, &resultHash, &applyKey,
		&work, &semantic, &automaticTurns, &repairCount, &versionCount, &retryCount); err != nil {
		t.Fatal(err)
	}
	if state != "validated" || sourceRequestID != "legacy:session-legacy:turn-legacy" || sourceCorrelationExact != 0 ||
		sourceSessionID != "session-legacy" || sourceTurnID != "turn-legacy" || provider != "provider-legacy" ||
		model != "model-legacy" || effort != "high" || !bytes.Equal(canonical, []byte("{}")) || resultHash != hash ||
		applyKey != "apply-legacy" || work != 5 || semantic != 2 || automaticTurns != 2 || repairCount != 1 ||
		versionCount != 0 || retryCount != 1 {
		t.Fatalf("migrated obligation lost lineage: state=%q source=(%q,%d,%q,%q) selection=(%q,%q,%q) canonical=%q hash=%q apply=%q counts=(%d,%d,%d,%d,%d,%d)",
			state, sourceRequestID, sourceCorrelationExact, sourceSessionID, sourceTurnID, provider, model, effort,
			canonical, resultHash, applyKey, work, semantic, automaticTurns, repairCount, versionCount, retryCount)
	}

	var kind, directiveKind, continuationID, dispatchRequestID, controlTurnID, deliveryState, sendStartedAt, terminalOutcome string
	var baseRevision int
	if err := reopened.QueryRow(`SELECT kind,directive_kind,continuation_id,dispatch_request_id,control_turn_id,base_revision,delivery_state,
		send_started_at,terminal_outcome FROM conclusion_dispatches WHERE obligation_id='legacy-validated'`).Scan(
		&kind, &directiveKind, &continuationID, &dispatchRequestID, &controlTurnID, &baseRevision, &deliveryState,
		&sendStartedAt, &terminalOutcome); err != nil {
		t.Fatal(err)
	}
	if kind != "retry" || directiveKind != "repair" || continuationID != "continuation-legacy" || dispatchRequestID != "dispatch-legacy" ||
		controlTurnID != "control-legacy" || baseRevision != 9 || deliveryState != "validated" ||
		sendStartedAt != "2026-07-27T12:00:30Z" || terminalOutcome != "" {
		t.Fatalf("migrated dispatch lost binding: kind=%q directive=%q continuation=%q request=%q control=%q base=%d state=%q sent=%q outcome=%q",
			kind, directiveKind, continuationID, dispatchRequestID, controlTurnID, baseRevision, deliveryState, sendStartedAt, terminalOutcome)
	}

	// The Session retry-keys table now references the obligation table.
	var obligationFK string
	if err := reopened.QueryRow(`SELECT sql FROM sqlite_master WHERE type='table' AND name='session_assisted_conclusion_retry_keys'`).Scan(&obligationFK); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(obligationFK, "session_pending_blackboard_conclusions") {
		t.Fatalf("session retry keys still reference the legacy receipt table: %s", obligationFK)
	}
}

func TestMigration60BackfillsActiveRecoveryDirectiveKindsFromDispatchHistory(t *testing.T) {
	path := filepath.Join(t.TempDir(), "pentest.db")
	db, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	now := "2026-08-24T10:00:00Z"
	if _, err := db.Exec(`
		INSERT INTO pending_blackboard_conclusions
			(id,task_id,source_request_id,source_request_correlation_exact,source_continuation_id,source_session_id,source_turn_id,
			 state,source_work_watermark,semantic_persistence_watermark,explicit_retry_count,created_at,updated_at)
			VALUES ('task-obligation','task-1','task-request',1,'task-old','task-session-old','task-turn',
			 'dispatch_requested',1,0,1,?,?);
		INSERT INTO conclusion_dispatches
			(id,obligation_id,kind,directive_kind,continuation_id,source_session_id,dispatch_request_id,delivery_state,
			 terminal_outcome,created_at,updated_at)
			VALUES ('task-initial','task-obligation','initial','initial','task-old','task-session-old','task-request-initial',
			 'superseded','superseded_by_recovery','2026-08-24T10:00:01Z','2026-08-24T10:00:01Z'),
			('task-recovery','task-obligation','recovery','','task-new','task-session-new','task-request-recovery',
			 'dispatch_requested','','2026-08-24T10:00:02Z','2026-08-24T10:00:02Z');
		INSERT INTO pending_blackboard_conclusions
			(id,task_id,source_request_id,source_request_correlation_exact,source_continuation_id,source_session_id,source_turn_id,
			 state,source_work_watermark,semantic_persistence_watermark,created_at,updated_at)
			VALUES ('task-obligation-no-history','task-2','task-request-no-history',1,'task-old-no-history','task-session-no-history','task-turn-no-history',
			 'dispatch_requested',1,0,?,?);
		INSERT INTO conclusion_dispatches
			(id,obligation_id,kind,directive_kind,continuation_id,source_session_id,dispatch_request_id,delivery_state,
			 terminal_outcome,created_at,updated_at)
			VALUES ('task-recovery-no-history','task-obligation-no-history','recovery','','task-new-no-history','task-session-new-no-history',
			 'task-request-recovery-no-history','dispatch_requested','','2026-08-24T10:00:02Z','2026-08-24T10:00:02Z');

		INSERT INTO session_pending_blackboard_conclusions
			(id,session_id,source_request_id,source_request_correlation_exact,source_continuation_id,source_session_id,source_turn_id,
			 state,source_work_watermark,semantic_persistence_watermark,explicit_retry_count,created_at,updated_at)
			VALUES ('session-obligation','session-1','session-request',1,'session-old','native-session-old','session-turn',
			 'dispatch_requested',1,0,1,?,?);
		INSERT INTO session_conclusion_dispatches
			(id,obligation_id,kind,directive_kind,continuation_id,source_session_id,dispatch_request_id,delivery_state,
			 terminal_outcome,created_at,updated_at)
			VALUES ('session-repair','session-obligation','repair','repair','session-old','native-session-old','session-request-repair',
			 'superseded','superseded_by_recovery','2026-08-24T10:00:01Z','2026-08-24T10:00:01Z'),
			('session-recovery','session-obligation','recovery','','session-new','native-session-new','session-request-recovery',
			 'dispatch_requested','','2026-08-24T10:00:02Z','2026-08-24T10:00:02Z');
		INSERT INTO session_pending_blackboard_conclusions
			(id,session_id,source_request_id,source_request_correlation_exact,source_continuation_id,source_session_id,source_turn_id,
			 state,source_work_watermark,semantic_persistence_watermark,created_at,updated_at)
			VALUES ('session-obligation-no-history','session-2','session-request-no-history',1,'session-old-no-history','native-session-no-history','session-turn-no-history',
			 'dispatch_requested',1,0,?,?);
		INSERT INTO session_conclusion_dispatches
			(id,obligation_id,kind,directive_kind,continuation_id,source_session_id,dispatch_request_id,delivery_state,
			 terminal_outcome,created_at,updated_at)
			VALUES ('session-recovery-no-history','session-obligation-no-history','recovery','','session-new-no-history','native-session-new-no-history',
			 'session-request-recovery-no-history','dispatch_requested','','2026-08-24T10:00:02Z','2026-08-24T10:00:02Z');
		DELETE FROM schema_migrations WHERE version=60;
	`, now, now, now, now, now, now, now, now); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	reopened, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	for _, test := range []struct {
		table string
		id    string
		want  string
	}{
		{table: "conclusion_dispatches", id: "task-recovery", want: "initial"},
		{table: "conclusion_dispatches", id: "task-recovery-no-history", want: "initial"},
		{table: "session_conclusion_dispatches", id: "session-recovery", want: "repair"},
		{table: "session_conclusion_dispatches", id: "session-recovery-no-history", want: "initial"},
	} {
		var got string
		if err := reopened.QueryRow(`SELECT directive_kind FROM `+test.table+` WHERE id=?`, test.id).Scan(&got); err != nil {
			t.Fatal(err)
		}
		if got != test.want {
			t.Errorf("%s directive_kind = %q, want %q", test.id, got, test.want)
		}
	}
}

// historicalMigration56Checksum is the checksum recorded when migration 56
// (challenge_workflow) was first applied. The SQL body for that version is
// immutable; recovery settlement belongs to migration 58.
const historicalMigration56Checksum = "31e5eb8fc028154ee187046019ef9f08640fd82eba3c22f71df7ba8a78b00f68"

// TestMigration56ChecksumMatchesHistoricalChallengeWorkflow pins the applied
// migration 56 SQL so later recovery columns cannot rewrite the ledger.
func TestMigration56ChecksumMatchesHistoricalChallengeWorkflow(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "pentest.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	var checksum string
	if err := db.QueryRow(`SELECT checksum FROM schema_migrations WHERE version=56`).Scan(&checksum); err != nil {
		t.Fatal(err)
	}
	if checksum != historicalMigration56Checksum {
		t.Fatalf("migration 56 checksum = %q, want historical %q (do not rewrite applied migration SQL; put recovery settlement in migration 58)", checksum, historicalMigration56Checksum)
	}
}

// TestMigration58AddsChallengeOperationRecoverySettlement upgrades a database
// that still has the original migration 56 challenge_operations shape (no
// action_required state and no recovery_error column) into the recovery
// settlement schema used by Challenge Workflow restart recovery.
func TestMigration58AddsChallengeOperationRecoverySettlement(t *testing.T) {
	path := filepath.Join(t.TempDir(), "pentest.db")
	db, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}

	// Roll back to the applied migration 56 shape: operations without recovery
	// settlement fields, and no migration 58 ledger row.
	if _, err := db.Exec(`
		DELETE FROM schema_migrations WHERE version >= 58;
		DROP INDEX IF EXISTS idx_challenge_operations_recovery;
		ALTER TABLE challenge_operations RENAME TO challenge_operations_new;
		CREATE TABLE challenge_operations (
			task_id TEXT NOT NULL REFERENCES tasks(id) ON DELETE RESTRICT,
			operation_id TEXT NOT NULL,
			project_id TEXT NOT NULL REFERENCES projects(id) ON DELETE RESTRICT,
			platform TEXT NOT NULL,
			kind TEXT NOT NULL CHECK (kind IN ('claim','submit','abandon','finalize')),
			request_hash TEXT NOT NULL CHECK (length(request_hash) = 64),
			request_json TEXT NOT NULL,
			state TEXT NOT NULL CHECK (state IN ('pending','recording','completed')),
			external_attempt_id TEXT NOT NULL DEFAULT '',
			response_json TEXT NOT NULL DEFAULT '',
			evidence_key TEXT NOT NULL DEFAULT '',
			created_at TEXT NOT NULL,
			updated_at TEXT NOT NULL,
			PRIMARY KEY (task_id, operation_id)
		);
		CREATE INDEX idx_challenge_operations_recovery
			ON challenge_operations (state, updated_at);
		INSERT INTO projects (id, name, description, scope_json, defaults_json, created_at, updated_at)
			VALUES ('proj-challenge','Challenge','','{}','{}','2026-08-09T00:00:00Z','2026-08-09T00:00:00Z');
		INSERT INTO tasks (
			id,project_id,goal,status,runner,runtime_profile_id,run_controls_json,scope_snapshot_json,created_at,updated_at,task_type
		) VALUES (
			'task-challenge','proj-challenge','solve','pending','sandbox','profile-1','{}','{}','2026-08-09T00:00:00Z','2026-08-09T00:00:00Z','ctf_challenge'
		);
		INSERT INTO challenge_operations (
			task_id,operation_id,project_id,platform,kind,request_hash,request_json,state,
			external_attempt_id,response_json,evidence_key,created_at,updated_at
		) VALUES (
			'task-challenge','op-1','proj-challenge','arena','claim',?,
			'{}','pending','','','','2026-08-09T00:00:00Z','2026-08-09T00:00:00Z'
		);
		DROP TABLE challenge_operations_new;
	`, strings.Repeat("b", 64)); err != nil {
		t.Fatalf("seed pre-58 challenge_operations: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	reopened, err := store.Open(path)
	if err != nil {
		t.Fatalf("reopen after migration 58: %v", err)
	}
	defer reopened.Close()

	var tableSQL string
	if err := reopened.QueryRow(`SELECT sql FROM sqlite_master WHERE type='table' AND name='challenge_operations'`).Scan(&tableSQL); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(tableSQL, "'action_required'") {
		t.Fatalf("migration 58 did not allow action_required state: %s", tableSQL)
	}
	if !strings.Contains(tableSQL, "recovery_error") {
		t.Fatalf("migration 58 did not add recovery_error: %s", tableSQL)
	}

	var state, recoveryError string
	if err := reopened.QueryRow(`SELECT state,recovery_error FROM challenge_operations WHERE task_id='task-challenge' AND operation_id='op-1'`).Scan(&state, &recoveryError); err != nil {
		t.Fatalf("read upgraded operation: %v", err)
	}
	if state != "pending" || recoveryError != "" {
		t.Fatalf("upgraded operation = state=%q recovery_error=%q, want pending with empty recovery_error", state, recoveryError)
	}

	// Recovery settlement must accept action_required after the upgrade.
	if _, err := reopened.Exec(`UPDATE challenge_operations SET state='action_required',recovery_error='automatic recovery failed' WHERE task_id='task-challenge' AND operation_id='op-1'`); err != nil {
		t.Fatalf("set action_required after migration 58: %v", err)
	}
}
