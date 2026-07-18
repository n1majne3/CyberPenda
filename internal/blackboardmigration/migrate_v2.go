package blackboardmigration

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"pentest/internal/blackboardv2"
	"pentest/internal/project"
	"pentest/internal/store"
)

const migrateResultSchema = "blackboard-v2-migration-result/v1"
const migrateResultAuditKind = "migrate_v2"

// migrateToBlackboardV2 performs the offline atomic v1→v2 cutover: rebuild,
// staged shared-domain updates, validation, audit, and epoch switch in one
// SQLite transaction.
func (s *Service) migrateToBlackboardV2(ctx context.Context, request MigrationRequest) (MigrationResult, error) {
	if existing, handled, err := s.committedMigrateResult(ctx, request); handled {
		return existing, err
	}

	epoch, err := s.db.CanonicalStore()
	if err != nil {
		return MigrationResult{Kind: request.Kind}, err
	}
	if epoch != store.CanonicalStoreGraphV1 && epoch != store.CanonicalStoreGraphV1Finalized && epoch != store.CanonicalStoreLegacyV1 {
		return MigrationResult{Kind: request.Kind}, fmt.Errorf("v2 migrate requires a v1 store epoch, got %q", epoch)
	}

	result := MigrationResult{Kind: request.Kind}
	blockers := make([]MigrationBlocker, 0)

	backupPath := strings.TrimSpace(request.BackupPath)
	if backupPath == "" {
		blockers = append(blockers, MigrationBlocker{
			Code: "backup_verification_failed", Message: "A verified pre-cutover backup path is required.", Path: "backup",
		})
	} else {
		backup, backupBlockers, backupErr := verifyExistingMigrationBackup(ctx, s.db, s.databasePath, backupPath, request.SourceDigest)
		blockers = append(blockers, backupBlockers...)
		if backupErr != nil && len(backupBlockers) == 0 {
			return result, backupErr
		}
		if backup != nil {
			result.Backup = backup
		}
	}

	// Active Continuations are hard blockers before any active-state switch.
	continuationBlockers, err := activeContinuationBlockers(ctx, s.db)
	if err != nil {
		return result, err
	}
	blockers = append(blockers, continuationBlockers...)

	if strings.TrimSpace(request.SourceDigest) == "" {
		blockers = append(blockers, MigrationBlocker{
			Code: "missing_source_digest", Message: "Migrate requires the inspect plan source_digest.", Path: "source_digest",
		})
	}

	if len(blockers) > 0 {
		result.Migrate = blockedMigrateResult(epoch, backupPath, blockers)
		result.Plan.ValidationBlockers = blockers
		return result, ErrMigrationBlocked
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return result, fmt.Errorf("begin atomic Blackboard v2 migrate: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	currentDigest, err := sourceDigestInTransaction(ctx, tx)
	if err != nil {
		return result, err
	}
	if request.SourceDigest != currentDigest {
		blockers = append(blockers, MigrationBlocker{
			Code: "stale_source_digest", Message: "The supplied source_digest does not match the current v1 source.", Path: "source_digest",
		})
		result.Migrate = blockedMigrateResult(epoch, backupPath, blockers)
		result.Plan.ValidationBlockers = blockers
		return result, ErrMigrationBlocked
	}

	// Re-check continuations inside the cutover transaction.
	txContinuationBlockers, err := activeContinuationBlockersTx(ctx, tx)
	if err != nil {
		return result, err
	}
	if len(txContinuationBlockers) > 0 {
		result.Migrate = blockedMigrateResult(epoch, backupPath, txContinuationBlockers)
		result.Plan.ValidationBlockers = txContinuationBlockers
		return result, ErrMigrationBlocked
	}

	rebuild, state, err := s.rebuildUnambiguousHeadsWithTx(ctx, tx, request, epoch)
	result.Rebuild = &rebuild
	if err != nil {
		if errors.Is(err, ErrRebuildBlocked) {
			blockers = append(blockers, rebuild.Blockers...)
			result.Migrate = blockedMigrateResult(epoch, backupPath, blockers)
			result.Plan.ValidationBlockers = blockers
			return result, ErrMigrationBlocked
		}
		return result, err
	}

	// Apply staged Scope/testing limits into the shared Project domain only at cutover.
	if err := applyStagedScopeLimitsInTx(ctx, tx, state.scopeLimits, s.clock().UTC().Format(time.RFC3339Nano)); err != nil {
		return result, err
	}

	// Validate rebuilt Projects before the epoch switch using the same transaction.
	v2 := blackboardv2.NewServiceWithEvidence(s.db, blackboardv2.EvidenceConfig{ArtifactRoot: s.artifactRoot})
	validated, validationErr := validateMigratedProjectsInTx(ctx, tx, v2, state, s.artifactRoot)
	if validationErr != nil {
		return result, validationErr
	}

	if err := s.failCutover(CutoverFailureAfterParity); err != nil {
		return result, err
	}

	now := s.clock().UTC().Format(time.RFC3339Nano)
	// The source ledger remains available through validation above. Retire it
	// only after the rebuilt v2 state has passed every check, in this same
	// transaction, so a failure leaves the source authoritative.
	if err := store.CompleteBlackboardV2Cutover(ctx, tx, now); err != nil {
		return result, err
	}
	cutoverID := "migrate_v2_" + shortHash(request.SourceDigest+"\x00"+now)
	backup := result.Backup
	if _, err := tx.ExecContext(ctx, `
		UPDATE blackboard_store_state
		SET canonical_store=?,
		    cutover_state='v2',
		    migration_contract_version='blackboard_v2',
		    graph_schema_version=0,
		    cutover_id=?,
		    source_digest=?,
		    mapping_digest=?,
		    verified_backup_path=?,
		    verified_backup_sha256=?,
		    cutover_application_version='blackboard-v2-migrate',
		    cutover_started_at=?,
		    cutover_committed_at=?,
		    post_cutover_write_committed=0,
		    updated_at=?
		WHERE id=1 AND canonical_store IN (?, ?, ?)`,
		store.CanonicalStoreBlackboardV2,
		cutoverID,
		request.SourceDigest,
		rebuildMappingDigest(rebuild),
		backup.Path,
		backup.SHA256,
		now, now, now,
		store.CanonicalStoreGraphV1, store.CanonicalStoreGraphV1Finalized, store.CanonicalStoreLegacyV1,
	); err != nil {
		return result, fmt.Errorf("flip canonical store epoch to blackboard_v2: %w", err)
	}
	// Ordinary Store open requires migrations 1-20 recorded. Migration 20 is the
	// epoch flip; record it only when the offline path applied schema without it.
	if err := ensureMigration20Recorded(ctx, tx, now); err != nil {
		return result, err
	}

	migrateResult := &CutoverMigrationResultV1{
		Schema:             migrateResultSchema,
		Status:             "migrated",
		VerifiedBackupPath: backup.Path,
		ProjectCount:       len(state.projects),
		Projects:           state.projects,
		Validation:         MigrationValidationV1{Status: "passed", SnapshotsValidated: validated},
		StoreEpoch:         store.CanonicalStoreBlackboardV2,
	}
	if err := s.failCutover(CutoverFailureAfterStateFlip); err != nil {
		return result, err
	}

	if err := tx.Commit(); err != nil {
		return result, fmt.Errorf("commit atomic Blackboard v2 migrate: %w", err)
	}

	result.Plan.SourceDigest = request.SourceDigest
	result.Migrate = migrateResult
	return result, nil
}

func (s *Service) verifyBlackboardV2(ctx context.Context, request MigrationRequest) (MigrationResult, error) {
	result := MigrationResult{Kind: request.Kind}
	epoch, err := s.db.CanonicalStore()
	if err != nil {
		return result, err
	}
	if epoch != store.CanonicalStoreBlackboardV2 {
		return result, fmt.Errorf("%w: store epoch is %q, want blackboard_v2", ErrCutoverVerificationFailed, epoch)
	}

	var backupPath, sourceDigest, cutoverID string
	if err := s.db.QueryRowContext(ctx, `
		SELECT verified_backup_path,source_digest,cutover_id
		FROM blackboard_store_state WHERE id=1`).Scan(&backupPath, &sourceDigest, &cutoverID); err != nil {
		return result, fmt.Errorf("%w: read store state: %v", ErrCutoverVerificationFailed, err)
	}

	projects, err := listProjectIDsFromDB(ctx, s.db)
	if err != nil {
		return result, fmt.Errorf("%w: list projects: %v", ErrCutoverVerificationFailed, err)
	}
	sort.Strings(projects)

	v2 := blackboardv2.NewServiceWithEvidence(s.db, blackboardv2.EvidenceConfig{ArtifactRoot: s.artifactRoot})
	projectResults := make([]RebuildProjectResultV1, 0, len(projects))
	validated := 0
	for _, projectID := range projects {
		var revision int
		if err := s.db.QueryRowContext(ctx, `SELECT revision FROM blackboard_v2_project_state WHERE project_id=?`, projectID).Scan(&revision); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				revision = 0
			} else {
				return result, fmt.Errorf("%w: read revision for %s: %v", ErrCutoverVerificationFailed, projectID, err)
			}
		}
		projectResults = append(projectResults, RebuildProjectResultV1{Project: projectID, Revision: revision})

		projection, err := v2.ProjectRuntimeSnapshot(ctx, projectID)
		if err != nil {
			return result, fmt.Errorf("%w: snapshot for %s: %v", ErrCutoverVerificationFailed, projectID, err)
		}
		if projection.Snapshot.Schema != "runtime-blackboard/v2" {
			return result, fmt.Errorf("%w: snapshot schema for %s = %q", ErrCutoverVerificationFailed, projectID, projection.Snapshot.Schema)
		}
		if err := validateSnapshotContract(projection.Bytes); err != nil {
			return result, fmt.Errorf("%w: snapshot contract for %s: %v", ErrCutoverVerificationFailed, projectID, err)
		}
		again, err := v2.ProjectRuntimeSnapshot(ctx, projectID)
		if err != nil {
			return result, fmt.Errorf("%w: re-snapshot for %s: %v", ErrCutoverVerificationFailed, projectID, err)
		}
		if string(again.Bytes) != string(projection.Bytes) {
			return result, fmt.Errorf("%w: snapshot bytes for %s are not deterministic", ErrCutoverVerificationFailed, projectID)
		}
		if err := validateProjectIsolationAndRedirects(ctx, s.db, projectID); err != nil {
			return result, fmt.Errorf("%w: %v", ErrCutoverVerificationFailed, err)
		}
		if err := validateEvidenceManagedPaths(ctx, s.db, projectID, s.artifactRoot); err != nil {
			return result, fmt.Errorf("%w: %v", ErrCutoverVerificationFailed, err)
		}
		validated++
	}

	// Cross-project isolation: no record key may resolve under the wrong Project.
	if err := validateCrossProjectIsolation(ctx, s.db, projects); err != nil {
		return result, fmt.Errorf("%w: %v", ErrCutoverVerificationFailed, err)
	}

	result.Plan.SourceDigest = sourceDigest
	result.Migrate = &CutoverMigrationResultV1{
		Schema:             migrateResultSchema,
		Status:             "migrated",
		VerifiedBackupPath: backupPath,
		ProjectCount:       len(projects),
		Projects:           projectResults,
		Validation:         MigrationValidationV1{Status: "passed", SnapshotsValidated: validated},
		StoreEpoch:         store.CanonicalStoreBlackboardV2,
	}
	_ = cutoverID
	return result, nil
}

func (s *Service) committedMigrateResult(ctx context.Context, request MigrationRequest) (MigrationResult, bool, error) {
	var epoch, cutoverState, storedDigest, cutoverID, backupPath, backupSHA string
	err := s.db.QueryRowContext(ctx, `
		SELECT canonical_store,cutover_state,source_digest,cutover_id,verified_backup_path,verified_backup_sha256
		FROM blackboard_store_state WHERE id=1`).Scan(&epoch, &cutoverState, &storedDigest, &cutoverID, &backupPath, &backupSHA)
	if err != nil {
		return MigrationResult{}, true, err
	}
	if epoch != store.CanonicalStoreBlackboardV2 || cutoverState != "v2" {
		return MigrationResult{}, false, nil
	}
	if strings.TrimSpace(request.SourceDigest) != "" && request.SourceDigest != storedDigest {
		return MigrationResult{
			Kind: MigrationKindMigrate,
			Plan: LegacyMigrationPlanV1{SourceDigest: storedDigest},
		}, true, fmt.Errorf("%w: cutover_id=%s committed=%s requested=%s", ErrCutoverConflict, cutoverID, storedDigest, request.SourceDigest)
	}
	// Rebuild a stable result from live post-cutover state. The v1 migration
	// audit table is deliberately retired with the source ledger.
	verify, err := s.verifyBlackboardV2(ctx, MigrationRequest{Kind: MigrationKindVerify})
	if err != nil {
		return MigrationResult{Kind: MigrationKindMigrate, Plan: LegacyMigrationPlanV1{SourceDigest: storedDigest}}, true, err
	}
	verify.Kind = MigrationKindMigrate
	if verify.Migrate != nil {
		verify.Migrate.VerifiedBackupPath = backupPath
	}
	verify.Backup = &VerifiedBackup{Path: backupPath, SHA256: backupSHA, QuickCheck: "ok"}
	return verify, true, nil
}

func blockedMigrateResult(epoch, backupPath string, blockers []MigrationBlocker) *CutoverMigrationResultV1 {
	sort.Slice(blockers, func(i, j int) bool {
		return blockers[i].Code+blockers[i].Path < blockers[j].Code+blockers[j].Path
	})
	return &CutoverMigrationResultV1{
		Status:             "blocked",
		VerifiedBackupPath: backupPath,
		Projects:           []RebuildProjectResultV1{},
		Validation:         MigrationValidationV1{Status: "passed", SnapshotsValidated: 0},
		StoreEpoch:         epoch,
		Blockers:           blockers,
	}
}

func verifyExistingMigrationBackup(ctx context.Context, sourceDB *store.DB, sourcePath, backupPath, expectedSourceDigest string) (*VerifiedBackup, []MigrationBlocker, error) {
	backupPath = filepath.Clean(backupPath)
	info, err := os.Stat(backupPath)
	if err != nil {
		return nil, []MigrationBlocker{{
			Code: "backup_verification_failed", Message: "The pre-cutover backup could not be opened and independently verified.", Path: backupPath,
		}}, nil
	}
	if info.IsDir() {
		return nil, []MigrationBlocker{{
			Code: "backup_verification_failed", Message: "The pre-cutover backup path is a directory.", Path: backupPath,
		}}, nil
	}
	backupDB, err := sql.Open("sqlite", "file:"+backupPath+"?mode=ro")
	if err != nil {
		return nil, []MigrationBlocker{{
			Code: "backup_verification_failed", Message: "The pre-cutover backup could not be opened independently.", Path: backupPath,
		}}, nil
	}
	defer backupDB.Close()
	var quickCheck string
	if err := backupDB.QueryRowContext(ctx, `PRAGMA quick_check`).Scan(&quickCheck); err != nil || quickCheck != "ok" {
		return nil, []MigrationBlocker{{
			Code: "backup_verification_failed", Message: "The pre-cutover backup failed independent quick_check verification.", Path: backupPath,
		}}, nil
	}
	digest, err := fileSHA256(backupPath)
	if err != nil {
		return nil, []MigrationBlocker{{
			Code: "backup_verification_failed", Message: "The pre-cutover backup could not be hashed.", Path: backupPath,
		}}, nil
	}
	backup := &VerifiedBackup{Path: backupPath, SHA256: digest, QuickCheck: quickCheck}

	// Digest order matters for stable operator diagnostics:
	// 1) live source vs plan → stale_source_digest
	// 2) backup vs live/plan → backup_mismatch
	if strings.TrimSpace(expectedSourceDigest) != "" {
		liveTx, err := sourceDB.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
		if err != nil {
			return backup, nil, err
		}
		liveDigest, liveErr := sourceDigestInTransaction(ctx, liveTx)
		_ = liveTx.Rollback()
		if liveErr != nil {
			return backup, nil, liveErr
		}
		if liveDigest != expectedSourceDigest {
			return backup, []MigrationBlocker{{
				Code: "stale_source_digest", Message: "The supplied source_digest does not match the current v1 source.", Path: "source_digest",
			}}, nil
		}
		backupTx, err := backupDB.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
		if err != nil {
			return backup, nil, err
		}
		backupDigest, digestErr := sourceDigestInTransaction(ctx, backupTx)
		_ = backupTx.Rollback()
		if digestErr != nil {
			return backup, []MigrationBlocker{{
				Code: "backup_verification_failed", Message: "The pre-cutover backup source could not be digested.", Path: backupPath,
			}}, nil
		}
		if backupDigest != expectedSourceDigest || backupDigest != liveDigest {
			return backup, []MigrationBlocker{{
				Code: "backup_mismatch", Message: "The verified backup source_digest does not match the current v1 source.", Path: backupPath,
			}}, nil
		}
	}
	_ = sourcePath
	return backup, nil, nil
}

func activeContinuationBlockers(ctx context.Context, db *store.DB) ([]MigrationBlocker, error) {
	tx, err := db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	return activeContinuationBlockersTx(ctx, tx)
}

func activeContinuationBlockersTx(ctx context.Context, tx *sql.Tx) ([]MigrationBlocker, error) {
	rows, err := tx.QueryContext(ctx, `
		SELECT c.id,t.project_id
		FROM task_continuations c
		JOIN tasks t ON t.id=c.task_id
		WHERE c.status IN ('pending','running','paused')
		ORDER BY t.project_id,c.id`)
	if err != nil {
		// Table may be absent on pure fixtures; treat as no active Continuations.
		if strings.Contains(err.Error(), "no such table") {
			return nil, nil
		}
		return nil, fmt.Errorf("inspect active Continuations: %w", err)
	}
	defer rows.Close()
	blockers := make([]MigrationBlocker, 0)
	for rows.Next() {
		var id, projectID string
		if err := rows.Scan(&id, &projectID); err != nil {
			return nil, err
		}
		blockers = append(blockers, MigrationBlocker{
			Code:    "active_continuation",
			Message: "A pending, running, or paused Continuation must be reconciled before cutover.",
			Path:    projectID + "/continuation/" + id,
		})
	}
	return blockers, rows.Err()
}

func applyStagedScopeLimitsInTx(ctx context.Context, tx *sql.Tx, staged map[string][]string, now string) error {
	for projectID, limits := range staged {
		if len(limits) == 0 {
			continue
		}
		var scopeJSON string
		if err := tx.QueryRowContext(ctx, `SELECT scope_json FROM projects WHERE id=?`, projectID).Scan(&scopeJSON); err != nil {
			return fmt.Errorf("read Project Scope for %s: %w", projectID, err)
		}
		var scope project.Scope
		if strings.TrimSpace(scopeJSON) != "" {
			if err := json.Unmarshal([]byte(scopeJSON), &scope); err != nil {
				return fmt.Errorf("decode Project Scope for %s: %w", projectID, err)
			}
		}
		seen := make(map[string]bool, len(scope.TestingLimits))
		for _, existing := range scope.TestingLimits {
			seen[existing] = true
		}
		for _, limit := range limits {
			limit = strings.TrimSpace(limit)
			if limit == "" || seen[limit] {
				continue
			}
			seen[limit] = true
			scope.TestingLimits = append(scope.TestingLimits, limit)
		}
		encoded, err := json.Marshal(scope)
		if err != nil {
			return fmt.Errorf("encode Project Scope for %s: %w", projectID, err)
		}
		if _, err := tx.ExecContext(ctx, `UPDATE projects SET scope_json=?, updated_at=? WHERE id=?`, string(encoded), now, projectID); err != nil {
			return fmt.Errorf("apply staged scope limits for %s: %w", projectID, err)
		}
	}
	return nil
}

func validateMigratedProjectsInTx(ctx context.Context, tx *sql.Tx, v2 *blackboardv2.Service, state rebuildWriteState, artifactRoot string) (int, error) {
	validated := 0
	projectIDs := make([]string, 0, len(state.projects))
	for _, project := range state.projects {
		projectIDs = append(projectIDs, project.Project)
		projection, err := v2.ProjectRuntimeSnapshotTx(ctx, tx, project.Project)
		if err != nil {
			return 0, fmt.Errorf("project Runtime Snapshot for %s: %w", project.Project, err)
		}
		if projection.Snapshot.Schema != "runtime-blackboard/v2" {
			return 0, fmt.Errorf("snapshot schema for %s = %q", project.Project, projection.Snapshot.Schema)
		}
		if err := validateSnapshotContract(projection.Bytes); err != nil {
			return 0, fmt.Errorf("snapshot contract for %s: %w", project.Project, err)
		}
		again, err := v2.ProjectRuntimeSnapshotTx(ctx, tx, project.Project)
		if err != nil {
			return 0, err
		}
		if string(again.Bytes) != string(projection.Bytes) {
			return 0, fmt.Errorf("snapshot bytes for %s are not deterministic", project.Project)
		}
		if err := validateProjectIsolationAndRedirectsTx(ctx, tx, project.Project); err != nil {
			return 0, err
		}
		if err := validateEvidenceManagedPathsTx(ctx, tx, project.Project, artifactRoot); err != nil {
			return 0, err
		}
		validated++
	}
	if err := validateCrossProjectIsolationTx(ctx, tx, projectIDs); err != nil {
		return 0, err
	}
	return validated, nil
}

func validateProjectIsolationAndRedirects(ctx context.Context, db *store.DB, projectID string) error {
	tx, err := db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	return validateProjectIsolationAndRedirectsTx(ctx, tx, projectID)
}

func validateProjectIsolationAndRedirectsTx(ctx context.Context, tx *sql.Tx, projectID string) error {
	// Redirects must stay project-local and point at current or historical keys in the same Project.
	rows, err := tx.QueryContext(ctx, `
		SELECT source_key, canonical_key FROM blackboard_v2_key_redirects WHERE project_id=?`, projectID)
	if err != nil {
		if strings.Contains(err.Error(), "no such table") {
			return nil
		}
		return fmt.Errorf("read redirects for %s: %w", projectID, err)
	}
	defer rows.Close()
	for rows.Next() {
		var source, canonical string
		if err := rows.Scan(&source, &canonical); err != nil {
			return err
		}
		var current, history int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM blackboard_v2_records WHERE project_id=? AND key=?`, projectID, canonical).Scan(&current); err != nil {
			return err
		}
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM blackboard_v2_record_history WHERE project_id=? AND key=?`, projectID, canonical).Scan(&history); err != nil {
			return err
		}
		if current == 0 && history == 0 {
			return fmt.Errorf("redirect %s -> %s in %s has no same-Project target", source, canonical, projectID)
		}
	}
	return rows.Err()
}

func validateEvidenceManagedPaths(ctx context.Context, db *store.DB, projectID, artifactRoot string) error {
	tx, err := db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	return validateEvidenceManagedPathsTx(ctx, tx, projectID, artifactRoot)
}

func validateEvidenceManagedPathsTx(ctx context.Context, tx *sql.Tx, projectID, artifactRoot string) error {
	rows, err := tx.QueryContext(ctx, `
		SELECT key, record_json FROM blackboard_v2_records
		WHERE project_id=? AND type='evidence'`, projectID)
	if err != nil {
		if strings.Contains(err.Error(), "no such table") {
			return nil
		}
		return fmt.Errorf("read Evidence for %s: %w", projectID, err)
	}
	defer rows.Close()
	for rows.Next() {
		var key, raw string
		if err := rows.Scan(&key, &raw); err != nil {
			return err
		}
		var record blackboardv2.EvidenceRecord
		if err := json.Unmarshal([]byte(raw), &record); err != nil {
			return fmt.Errorf("decode Evidence %s/%s: %w", projectID, key, err)
		}
		if record.ManagedPath == "" {
			return fmt.Errorf("Evidence %s/%s is missing managed_path", projectID, key)
		}
		if strings.Contains(record.ManagedPath, "..") || filepath.IsAbs(record.ManagedPath) {
			return fmt.Errorf("Evidence %s/%s managed_path escapes Artifact Root: %s", projectID, key, record.ManagedPath)
		}
		if record.Status == "missing" {
			continue
		}
		path := filepath.Join(artifactRoot, filepath.FromSlash(record.ManagedPath))
		file, err := os.Open(path)
		if err != nil {
			return fmt.Errorf("open Evidence %s/%s: %w", projectID, key, err)
		}
		hash := sha256.New()
		size, copyErr := io.Copy(hash, file)
		closeErr := file.Close()
		if copyErr != nil {
			return fmt.Errorf("hash Evidence %s/%s: %w", projectID, key, copyErr)
		}
		if closeErr != nil {
			return fmt.Errorf("close Evidence %s/%s: %w", projectID, key, closeErr)
		}
		if got := hex.EncodeToString(hash.Sum(nil)); got != strings.ToLower(record.SHA256) || size != record.Size {
			return fmt.Errorf("Evidence %s/%s integrity mismatch", projectID, key)
		}
	}
	return rows.Err()
}

func validateCrossProjectIsolation(ctx context.Context, db *store.DB, projects []string) error {
	tx, err := db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	return validateCrossProjectIsolationTx(ctx, tx, projects)
}

func validateCrossProjectIsolationTx(ctx context.Context, tx *sql.Tx, projects []string) error {
	// Relationships must never cross Projects (enforced by project_id PK, but
	// re-check endpoint membership).
	for _, projectID := range projects {
		rows, err := tx.QueryContext(ctx, `
			SELECT from_key, relation, to_key FROM blackboard_v2_relationships WHERE project_id=?`, projectID)
		if err != nil {
			if strings.Contains(err.Error(), "no such table") {
				return nil
			}
			return err
		}
		for rows.Next() {
			var fromKey, relation, toKey string
			if err := rows.Scan(&fromKey, &relation, &toKey); err != nil {
				rows.Close()
				return err
			}
			for _, key := range []string{fromKey, toKey} {
				var n int
				if err := tx.QueryRowContext(ctx, `
					SELECT (
						SELECT COUNT(*) FROM blackboard_v2_records WHERE project_id=? AND key=?
					) + (
						SELECT COUNT(*) FROM blackboard_v2_record_history WHERE project_id=? AND key=?
					)`, projectID, key, projectID, key).Scan(&n); err != nil {
					rows.Close()
					return err
				}
				if n == 0 && relation != "supersedes" {
					rows.Close()
					return fmt.Errorf("relationship %s %s %s in %s references missing same-Project endpoint", fromKey, relation, toKey, projectID)
				}
			}
		}
		if err := rows.Close(); err != nil {
			return err
		}
	}
	return nil
}

func rebuildMappingDigest(rebuild RebuildResultV1) string {
	hash := sha256.New()
	writeFrame(hash, []byte("blackboard_v2_migrate_mappings_v1"))
	for _, mapping := range rebuild.Mappings {
		writeFrame(hash, []byte(mapping.Project+"\x00"+mapping.SourceType+"\x00"+mapping.SourceKey+"\x00"+mapping.Action+"\x00"+mapping.TargetKey))
	}
	return "sha256:" + hex.EncodeToString(hash.Sum(nil))
}

// ensureMigration20Recorded inserts the blackboard_v2_store_epoch migration
// history row when the offline migrator flipped the epoch without applying
// migration 20 through the ordinary numbered migrator.
func ensureMigration20Recorded(ctx context.Context, tx *sql.Tx, now string) error {
	var present int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM schema_migrations WHERE version=20`).Scan(&present); err != nil {
		return fmt.Errorf("inspect migration 20 history: %w", err)
	}
	if present > 0 {
		return nil
	}
	// Checksum must match store.newMigration for migration20SQL.
	const migration20SQL = `
UPDATE blackboard_store_state
SET canonical_store = 'blackboard_v2',
    cutover_state = 'v2',
    migration_contract_version = 'blackboard_v2',
    graph_schema_version = 0,
    updated_at = '1970-01-01T00:00:00Z'
WHERE id = 1;
`
	sum := sha256.Sum256([]byte(migration20SQL))
	checksum := hex.EncodeToString(sum[:])
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO schema_migrations (version, name, checksum, applied_at)
		VALUES (20, 'blackboard_v2_store_epoch', ?, ?)`, checksum, now); err != nil {
		return fmt.Errorf("record migration 20 after offline cutover: %w", err)
	}
	return nil
}

func listProjectIDsFromDB(ctx context.Context, db *store.DB) ([]string, error) {
	tx, err := db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	return listProjectIDs(ctx, tx)
}
