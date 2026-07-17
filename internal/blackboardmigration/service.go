// Package blackboardmigration owns legacy Blackboard inspection, backup,
// cutover, verification, recovery, and finalization behind one deep seam.
package blackboardmigration

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"pentest/internal/blackboard"
	"pentest/internal/store"
)

type MigrationKind string

const (
	MigrationKindInspect            MigrationKind = "inspect"
	MigrationKindBackup             MigrationKind = "backup"
	MigrationKindCutover            MigrationKind = "cutover"
	MigrationKindVerify             MigrationKind = "verify"
	MigrationKindFinalizeLegacy     MigrationKind = "finalize_legacy"
	MigrationKindRebuildUnambiguous MigrationKind = "rebuild_unambiguous"
)

var ErrUnsupportedMigrationKind = errors.New("unsupported Blackboard migration request kind")
var ErrMigrationBlocked = errors.New("Blackboard migration is blocked by inspection diagnostics")
var ErrCutoverImplementationPending = errors.New("verified backup complete; atomic graph cutover is implemented by M05")
var ErrCutoverVerificationFailed = errors.New("post-cutover Blackboard verification failed")
var ErrCutoverConflict = errors.New("Blackboard cutover retry conflicts with the committed source digest")
var ErrFinalizationBlocked = errors.New("Blackboard legacy finalization is blocked by removal gates")

type MigrationRequest struct {
	Kind                     MigrationKind `json:"kind"`
	BackupPath               string        `json:"backup_path,omitempty"`
	CutoverID                string        `json:"cutover_id,omitempty"`
	MappingDigest            string        `json:"mapping_digest,omitempty"`
	BackupAcknowledged       bool          `json:"backup_acknowledged,omitempty"`
	MigrationSummaryExported bool          `json:"migration_summary_exported,omitempty"`
}

type MigrationResult struct {
	Kind         MigrationKind          `json:"kind"`
	Plan         LegacyMigrationPlanV1  `json:"plan"`
	Backup       *VerifiedBackup        `json:"backup,omitempty"`
	Import       *LegacyImportResultV1  `json:"import,omitempty"`
	Rebuild      *RebuildResultV1       `json:"rebuild,omitempty"`
	Verification *CutoverVerificationV1 `json:"verification,omitempty"`
	Recovery     *RecoveryGuidanceV1    `json:"recovery,omitempty"`
}

type CutoverVerificationV1 struct {
	CutoverID     string            `json:"cutover_id"`
	SourceDigest  string            `json:"source_digest"`
	MappingDigest string            `json:"mapping_digest"`
	ParityDigest  string            `json:"parity_digest"`
	ProjectHashes map[string]string `json:"project_hashes"`
	VerifiedAt    string            `json:"verified_at"`
	ResultHash    string            `json:"result_hash"`
}

type RecoveryGuidanceV1 struct {
	CutoverID         string `json:"cutover_id"`
	BackupPath        string `json:"backup_path"`
	BackupSHA256      string `json:"backup_sha256"`
	PostCutoverWrites bool   `json:"post_cutover_writes"`
	Warning           string `json:"warning"`
}

type LegacyMigrationPlanV1 struct {
	Schema             string                 `json:"schema"`
	SourceDigest       string                 `json:"source_digest"`
	Projects           []MigrationProjectPlan `json:"projects"`
	ValidationBlockers []MigrationBlocker     `json:"validation_blockers"`
	RequiredDecisions  []MigrationDecision    `json:"required_decisions"`

	SourceCounts      map[string]int `json:"-"`
	EstimatedMappings map[string]int `json:"-"`
	Blockers          []Diagnostic   `json:"-"`
	Warnings          []Diagnostic   `json:"-"`
}

type Diagnostic struct {
	Code          string   `json:"code"`
	ProjectID     string   `json:"project_id,omitempty"`
	SourceTable   string   `json:"source_table,omitempty"`
	SourceID      string   `json:"source_id,omitempty"`
	Message       string   `json:"message"`
	RepairOptions []string `json:"repair_options,omitempty"`
}

type MigrationProjectPlan struct {
	Project  string             `json:"project"`
	Mappings []MigrationMapping `json:"mappings"`
}

type MigrationMapping struct {
	Project    string `json:"project,omitempty"`
	SourceType string `json:"source_type"`
	SourceKey  string `json:"source_key"`
	Action     string `json:"action"`
	TargetKey  string `json:"target_key,omitempty"`
}

type MigrationBlocker struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	Path    string `json:"path"`
}

type MigrationDecision struct {
	Source         MigrationSourceRef `json:"source"`
	AllowedActions []string           `json:"allowed_actions"`
	Decision       string             `json:"decision,omitempty"`
	TargetKey      string             `json:"target_key,omitempty"`
}

type MigrationSourceRef struct {
	Project string `json:"project"`
	Type    string `json:"type"`
	Key     string `json:"key"`
}

// BlackboardMigrationService is the only public migration seam used by
// startup and operator adapters.
type BlackboardMigrationService interface {
	Execute(context.Context, MigrationRequest) (MigrationResult, error)
}

type Service struct {
	db                              *store.DB
	databasePath                    string
	artifactRoot                    string
	backup                          BackupImplementation
	clock                           func() time.Time
	commitDisposableImport          bool
	cutoverFailure                  CutoverFailureInjector
	finalizationFailure             FinalizationFailureInjector
	finalizationStateTransitionHook func(context.Context, *sql.Tx) error
}

type Option func(*Service)

type CutoverFailurePoint string

const (
	CutoverFailureAfterDDL           CutoverFailurePoint = "after_ddl"
	CutoverFailureAfterProjectImport CutoverFailurePoint = "after_project_import"
	CutoverFailureAfterMappings      CutoverFailurePoint = "after_mappings"
	CutoverFailureAfterHeadBuild     CutoverFailurePoint = "after_head_build"
	CutoverFailureAfterParity        CutoverFailurePoint = "after_parity"
	CutoverFailureAfterGuards        CutoverFailurePoint = "after_guards"
	CutoverFailureAfterStateFlip     CutoverFailurePoint = "after_state_flip"
	CutoverFailureAfterCommit        CutoverFailurePoint = "after_commit"
)

type CutoverFailureInjector interface {
	FailAfter(CutoverFailurePoint) error
}

type CutoverFailureInjectorFunc func(CutoverFailurePoint) error

func (fn CutoverFailureInjectorFunc) FailAfter(point CutoverFailurePoint) error { return fn(point) }

type FinalizationFailurePoint string

const FinalizationFailureBeforeAudit FinalizationFailurePoint = "before_audit"

// FinalizationFailureInjector is the stable persistence failure seam for
// proving that the numbered finalization transaction rolls back atomically.
type FinalizationFailureInjector interface {
	FailBefore(FinalizationFailurePoint) error
}

type FinalizationFailureInjectorFunc func(FinalizationFailurePoint) error

func (fn FinalizationFailureInjectorFunc) FailBefore(point FinalizationFailurePoint) error {
	return fn(point)
}

func WithBackupImplementation(backup BackupImplementation) Option {
	return func(service *Service) { service.backup = backup }
}

func WithClock(clock func() time.Time) Option {
	return func(service *Service) { service.clock = clock }
}

func WithCutoverFailureInjector(injector CutoverFailureInjector) Option {
	return func(service *Service) { service.cutoverFailure = injector }
}

func WithFinalizationFailureInjector(injector FinalizationFailureInjector) Option {
	return func(service *Service) { service.finalizationFailure = injector }
}

// WithFinalizationStateTransitionHook installs the deterministic concurrency
// seam used to prove a lost conditional epoch transition rolls back all DDL.
func WithFinalizationStateTransitionHook(hook func(context.Context, *sql.Tx) error) Option {
	return func(service *Service) { service.finalizationStateTransitionHook = hook }
}

// withDisposableImportCommitForTesting makes the M02 import observable on a
// disposable database without flipping the canonical store epoch. Production
// callers never enable this; M05 owns the real atomic commit and activation.
func withDisposableImportCommitForTesting() Option {
	return func(service *Service) { service.commitDisposableImport = true }
}

func NewService(db *store.DB, databasePath, artifactRoot string, options ...Option) *Service {
	service := &Service{
		db:           db,
		databasePath: filepath.Clean(databasePath),
		artifactRoot: filepath.Clean(artifactRoot),
		backup:       SQLiteBackupImplementation{},
		clock:        time.Now,
	}
	for _, option := range options {
		option(service)
	}
	return service
}

func (s *Service) Execute(ctx context.Context, request MigrationRequest) (MigrationResult, error) {
	switch request.Kind {
	case MigrationKindInspect:
		plan, err := s.inspect(ctx)
		if err != nil {
			return MigrationResult{}, err
		}
		return MigrationResult{Kind: request.Kind, Plan: plan}, nil
	case MigrationKindCutover:
		if existing, handled, err := s.committedCutoverResult(ctx); handled {
			return existing, err
		}
		plan, err := s.inspect(ctx)
		if err != nil {
			return MigrationResult{}, err
		}
		result := MigrationResult{Kind: request.Kind, Plan: plan}
		if len(plan.Blockers) > 0 {
			return result, ErrMigrationBlocked
		}
		backupPath := request.BackupPath
		if backupPath == "" {
			backupPath = s.defaultBackupPath()
		}
		backup, err := s.backup.CreateVerifiedBackup(ctx, s.db, s.databasePath, backupPath)
		if err != nil {
			return result, err
		}
		result.Backup = &backup
		cutoverID := "cutover_" + shortHash(plan.SourceDigest+"\x00"+s.clock().UTC().Format(time.RFC3339Nano))
		tx, err := s.db.BeginTx(ctx, nil)
		if err != nil {
			return result, fmt.Errorf("begin atomic Blackboard cutover: %w", err)
		}
		defer func() { _ = tx.Rollback() }()
		freshDigest, err := sourceDigestInTransaction(ctx, tx)
		if err != nil {
			return result, err
		}
		if freshDigest != plan.SourceDigest {
			return result, fmt.Errorf("legacy source changed after inspection: inspected=%s current=%s", plan.SourceDigest, freshDigest)
		}
		if err := s.failCutover(CutoverFailureAfterDDL); err != nil {
			return result, err
		}
		importResult, err := s.importLegacyGraphInTransaction(ctx, tx, plan.SourceDigest, cutoverID)
		if err != nil {
			return result, err
		}
		result.Import = &importResult
		if s.commitDisposableImport {
			if err := tx.Commit(); err != nil {
				return result, fmt.Errorf("commit disposable graph import: %w", err)
			}
			return result, ErrCutoverImplementationPending
		}
		if err := installLegacyWriteGuards(ctx, tx); err != nil {
			return result, err
		}
		if err := s.failCutover(CutoverFailureAfterGuards); err != nil {
			return result, err
		}
		now := s.clock().UTC().Format(time.RFC3339Nano)
		if _, err := tx.ExecContext(ctx, `UPDATE blackboard_store_state SET canonical_store=?,cutover_state='graph',migration_contract_version='legacy_blackboard_to_graph_v1',graph_schema_version=?,cutover_id=?,source_digest=?,mapping_digest=?,verified_backup_path=?,verified_backup_sha256=?,cutover_application_version='graph-blackboard-m05',cutover_started_at=?,cutover_committed_at=?,post_cutover_write_committed=0,updated_at=? WHERE id=1 AND canonical_store=?`,
			store.CanonicalStoreGraphV1, store.GraphSchemaVersion, cutoverID, plan.SourceDigest, importResult.MappingDigest, backup.Path, backup.SHA256, now, now, now, store.CanonicalStoreLegacyV1); err != nil {
			return result, fmt.Errorf("flip canonical Blackboard store epoch: %w", err)
		}
		if err := s.failCutover(CutoverFailureAfterStateFlip); err != nil {
			return result, err
		}
		if err := tx.Commit(); err != nil {
			return result, fmt.Errorf("commit atomic Blackboard cutover: %w", err)
		}
		if err := s.failCutover(CutoverFailureAfterCommit); err != nil {
			return result, err
		}
		verification, err := s.verifyCommittedCutover(ctx)
		if err != nil {
			s.markRecoveryRequired()
			result.Recovery = s.recoveryGuidance(context.Background())
			return result, fmt.Errorf("%w: %v", ErrCutoverVerificationFailed, err)
		}
		result.Verification = &verification
		if err := s.persistCutoverVerification(ctx, result); err != nil {
			return result, err
		}
		return result, nil
	case MigrationKindVerify:
		verification, err := s.verifyCommittedCutover(ctx)
		result := MigrationResult{Kind: request.Kind, Verification: &verification}
		if err != nil {
			s.markRecoveryRequired()
			result.Recovery = s.recoveryGuidance(context.Background())
			return result, fmt.Errorf("%w: %v", ErrCutoverVerificationFailed, err)
		}
		return result, nil
	case MigrationKindFinalizeLegacy:
		return s.finalizeLegacy(ctx, request)
	case MigrationKindRebuildUnambiguous:
		rebuild, err := s.rebuildUnambiguousHeads(ctx)
		result := MigrationResult{Kind: request.Kind, Rebuild: &rebuild}
		if err != nil {
			return result, err
		}
		return result, nil
	default:
		return MigrationResult{}, fmt.Errorf("%w: %q", ErrUnsupportedMigrationKind, request.Kind)
	}
}

// InspectMigrationSource inspects a v1 database opened through the dedicated
// read-only migration-source seam. It is the CLI-facing path for T01: ordinary
// daemon/runtime Store opens refuse v1, but the offline migrator can still read
// and validate a source snapshot.
func InspectMigrationSource(ctx context.Context, source *store.MigrationSource, artifactRoot string) (MigrationResult, error) {
	if source.Classification() != store.MigrationSourceNumberedV1 {
		return MigrationResult{}, fmt.Errorf("offline migration inspect requires numbered v1 migration history, got %q", source.Classification())
	}
	plan, err := inspectLegacyDatabase(ctx, source.DB, filepath.Clean(artifactRoot))
	if err != nil {
		return MigrationResult{}, err
	}
	return MigrationResult{Kind: MigrationKindInspect, Plan: plan}, nil
}

func BackupMigrationSource(ctx context.Context, source *store.MigrationSource, sourcePath, artifactRoot, backupPath string) (MigrationResult, error) {
	return backupMigrationSource(ctx, source, sourcePath, artifactRoot, backupPath, migrationSourceBackupOptions{})
}

type migrationSourceBackupOptions struct {
	afterBackup func() error
}

func backupMigrationSource(ctx context.Context, source *store.MigrationSource, sourcePath, artifactRoot, backupPath string, options migrationSourceBackupOptions) (MigrationResult, error) {
	if source.Classification() != store.MigrationSourceNumberedV1 {
		return MigrationResult{}, fmt.Errorf("offline migration backup requires numbered v1 migration history, got %q", source.Classification())
	}
	if strings.TrimSpace(backupPath) == "" {
		backupPath = filepath.Clean(sourcePath) + ".pre-blackboard-v2.bak"
	}
	plan, err := inspectLegacyDatabase(ctx, source.DB, filepath.Clean(artifactRoot))
	if err != nil {
		return MigrationResult{}, err
	}
	result := MigrationResult{Kind: MigrationKindBackup, Plan: plan}
	if len(plan.ValidationBlockers) > 0 {
		return result, ErrMigrationBlocked
	}
	backup, err := CreateVerifiedMigrationSourceBackup(ctx, source.DB, backupPath)
	if err != nil {
		result.Plan.ValidationBlockers = append(result.Plan.ValidationBlockers, MigrationBlocker{
			Code: "backup_verification_failed", Message: "The pre-cutover backup could not be created and independently verified.", Path: backupPath,
		})
		result.Plan.Blockers = append(result.Plan.Blockers, Diagnostic{
			Code: "backup_verification_failed", Message: "The pre-cutover backup could not be created and independently verified.",
		})
		return result, fmt.Errorf("%w: %v", ErrMigrationBlocked, err)
	}
	result.Backup = &backup
	if options.afterBackup != nil {
		if err := options.afterBackup(); err != nil {
			return result, err
		}
	}
	current, err := store.OpenMigrationSource(sourcePath)
	if err != nil {
		return result, fmt.Errorf("reopen migration source after backup: %w", err)
	}
	defer current.Close()
	after, err := inspectLegacyDatabase(ctx, current.DB, filepath.Clean(artifactRoot))
	if err != nil {
		return result, err
	}
	if after.SourceDigest != plan.SourceDigest {
		result.Plan.ValidationBlockers = append(result.Plan.ValidationBlockers, MigrationBlocker{
			Code: "source_changed", Message: "The v1 source changed after inspection and before backup verification completed.", Path: "source_digest",
		})
		result.Plan.Blockers = append(result.Plan.Blockers, Diagnostic{
			Code: "source_changed", Message: "The v1 source changed after inspection and before backup verification completed.",
		})
		return result, ErrMigrationBlocked
	}
	return result, nil
}

func (s *Service) finalizeLegacy(ctx context.Context, request MigrationRequest) (MigrationResult, error) {
	var epoch, state, cutoverID, mappingDigest, backupPath, backupSHA, verifiedAt, verificationHash string
	if err := s.db.QueryRowContext(ctx, `SELECT canonical_store,cutover_state,cutover_id,mapping_digest,verified_backup_path,verified_backup_sha256,latest_verification_at,latest_verification_result_hash FROM blackboard_store_state WHERE id=1`).Scan(&epoch, &state, &cutoverID, &mappingDigest, &backupPath, &backupSHA, &verifiedAt, &verificationHash); err != nil {
		return MigrationResult{Kind: request.Kind}, err
	}
	if epoch != store.CanonicalStoreGraphV1 || state != "graph" || request.CutoverID == "" || request.CutoverID != cutoverID || request.MappingDigest == "" || request.MappingDigest != mappingDigest || !request.BackupAcknowledged || !request.MigrationSummaryExported || backupPath == "" || backupSHA == "" || verifiedAt == "" || verificationHash == "" {
		return MigrationResult{Kind: request.Kind}, ErrFinalizationBlocked
	}
	var readsRetired int
	if err := s.db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM blackboard_compatibility_read_retirement WHERE id=1)`).Scan(&readsRetired); err != nil || readsRetired == 0 {
		return MigrationResult{Kind: request.Kind}, ErrFinalizationBlocked
	}
	verification, err := s.verifyCommittedCutover(ctx)
	if err != nil || verification.CutoverID != request.CutoverID || verification.MappingDigest != request.MappingDigest {
		if err == nil {
			err = errors.New("fresh verification does not match requested cutover")
		}
		return MigrationResult{Kind: request.Kind, Verification: &verification}, fmt.Errorf("%w: %v", ErrFinalizationBlocked, err)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return MigrationResult{Kind: request.Kind, Verification: &verification}, err
	}
	defer func() { _ = tx.Rollback() }()
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM blackboard_compatibility_read_retirement WHERE id=1)`).Scan(&readsRetired); err != nil || readsRetired == 0 {
		return MigrationResult{Kind: request.Kind, Verification: &verification}, ErrFinalizationBlocked
	}
	for _, table := range legacyBlackboardTables {
		if _, err := tx.ExecContext(ctx, `DROP TABLE IF EXISTS "`+table+`"`); err != nil {
			return MigrationResult{Kind: request.Kind, Verification: &verification}, fmt.Errorf("drop frozen legacy table %s: %w", table, err)
		}
	}
	now := s.clock().UTC().Format(time.RFC3339Nano)
	if s.finalizationStateTransitionHook != nil {
		if err := s.finalizationStateTransitionHook(ctx, tx); err != nil {
			return MigrationResult{Kind: request.Kind, Verification: &verification}, err
		}
	}
	transition, err := tx.ExecContext(ctx, `UPDATE blackboard_store_state SET canonical_store=?,cutover_state='finalized',updated_at=? WHERE id=1 AND canonical_store=? AND cutover_state='graph' AND cutover_id=?`, store.CanonicalStoreGraphV1Finalized, now, store.CanonicalStoreGraphV1, request.CutoverID)
	if err != nil {
		return MigrationResult{Kind: request.Kind, Verification: &verification}, err
	}
	rows, err := transition.RowsAffected()
	if err != nil || rows != 1 {
		return MigrationResult{Kind: request.Kind, Verification: &verification}, fmt.Errorf("%w: finalization state transition affected %d rows", ErrFinalizationBlocked, rows)
	}
	if s.finalizationFailure != nil {
		if err := s.finalizationFailure.FailBefore(FinalizationFailureBeforeAudit); err != nil {
			return MigrationResult{Kind: request.Kind, Verification: &verification}, err
		}
	}
	body, err := json.Marshal(MigrationResult{Kind: request.Kind, Verification: &verification})
	if err != nil {
		return MigrationResult{Kind: request.Kind, Verification: &verification}, fmt.Errorf("encode legacy finalization audit: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO blackboard_migration_runs(id,kind,state,diagnostic_code,source_digest,mapping_digest,backup_path,backup_sha256,counts_json,created_at,updated_at,finished_at) VALUES(?,?,?,?,?,?,?,?,?,?,?,?)`, request.CutoverID+":finalize:1", string(MigrationKindFinalizeLegacy), "committed", "finalize_legacy_transaction_1", verification.SourceDigest, verification.MappingDigest, backupPath, backupSHA, string(body), now, now, now); err != nil {
		return MigrationResult{Kind: request.Kind, Verification: &verification}, fmt.Errorf("record legacy finalization: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return MigrationResult{Kind: request.Kind, Verification: &verification}, err
	}
	return MigrationResult{Kind: request.Kind, Verification: &verification}, nil
}

func (s *Service) markRecoveryRequired() {
	_, _ = s.db.ExecContext(context.Background(), `UPDATE blackboard_store_state SET cutover_state='recovery_required',updated_at=? WHERE id=1 AND canonical_store=?`, s.clock().UTC().Format(time.RFC3339Nano), store.CanonicalStoreGraphV1)
}

func (s *Service) committedCutoverResult(ctx context.Context) (MigrationResult, bool, error) {
	var epoch, cutoverState, storedDigest, cutoverID, backupPath, backupSHA string
	err := s.db.QueryRowContext(ctx, `SELECT canonical_store,cutover_state,source_digest,cutover_id,verified_backup_path,verified_backup_sha256 FROM blackboard_store_state WHERE id=1`).Scan(&epoch, &cutoverState, &storedDigest, &cutoverID, &backupPath, &backupSHA)
	if err != nil {
		return MigrationResult{}, true, err
	}
	if epoch != store.CanonicalStoreGraphV1 && epoch != store.CanonicalStoreGraphV1Finalized {
		return MigrationResult{}, false, nil
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return MigrationResult{}, true, err
	}
	currentDigest, digestErr := sourceDigestInTransaction(ctx, tx)
	_ = tx.Rollback()
	if digestErr != nil {
		return MigrationResult{}, true, digestErr
	}
	if currentDigest != storedDigest {
		return MigrationResult{Kind: MigrationKindCutover, Plan: LegacyMigrationPlanV1{SourceDigest: currentDigest}}, true,
			fmt.Errorf("%w: cutover_id=%s committed=%s current=%s", ErrCutoverConflict, cutoverID, storedDigest, currentDigest)
	}
	if cutoverState == "graph" {
		if stored, ok, err := s.loadCutoverResult(ctx, cutoverID); err != nil {
			return MigrationResult{}, true, err
		} else if ok {
			return stored, true, nil
		}
	}
	verification, err := s.verifyCommittedCutover(ctx)
	result := MigrationResult{
		Kind:         MigrationKindCutover,
		Plan:         LegacyMigrationPlanV1{SourceDigest: storedDigest},
		Backup:       &VerifiedBackup{Path: backupPath, SHA256: backupSHA, QuickCheck: "ok"},
		Verification: &verification,
	}
	if err != nil {
		result.Recovery = s.recoveryGuidance(context.Background())
		return result, true, fmt.Errorf("%w: %v", ErrCutoverVerificationFailed, err)
	}
	result.Import = &LegacyImportResultV1{MappingDigest: verification.MappingDigest, MappingVerified: true, ParityDigest: verification.ParityDigest}
	if err := s.persistCutoverVerification(ctx, result); err != nil {
		return result, true, err
	}
	return result, true, nil
}

func (s *Service) recoveryGuidance(ctx context.Context) *RecoveryGuidanceV1 {
	var guidance RecoveryGuidanceV1
	var postCutoverWrites int
	if err := s.db.QueryRowContext(ctx, `SELECT cutover_id,verified_backup_path,verified_backup_sha256,post_cutover_write_committed FROM blackboard_store_state WHERE id=1`).Scan(&guidance.CutoverID, &guidance.BackupPath, &guidance.BackupSHA256, &postCutoverWrites); err != nil {
		return nil
	}
	guidance.PostCutoverWrites = postCutoverWrites != 0
	if guidance.PostCutoverWrites {
		guidance.Warning = "Restoring the verified pre-cutover backup would lose post-cutover graph writes; export the graph ledger, migration state, and Blackboard Health before following explicit restore instructions. No reverse migration was performed."
	} else {
		guidance.Warning = "Restore the verified pre-cutover backup only through an explicit operator recovery action. No reverse migration was performed."
	}
	return &guidance
}

func (s *Service) persistCutoverVerification(ctx context.Context, result MigrationResult) error {
	if result.Verification == nil {
		return nil
	}
	body, err := json.Marshal(result)
	if err != nil {
		return err
	}
	now := s.clock().UTC().Format(time.RFC3339Nano)
	_, err = s.db.ExecContext(ctx, `INSERT INTO blackboard_migration_runs(id,kind,state,source_digest,mapping_digest,backup_path,backup_sha256,counts_json,created_at,updated_at,finished_at) VALUES(?,?,?,?,?,?,?,?,?,?,?) ON CONFLICT(id) DO UPDATE SET state=excluded.state,source_digest=excluded.source_digest,mapping_digest=excluded.mapping_digest,backup_path=excluded.backup_path,backup_sha256=excluded.backup_sha256,counts_json=excluded.counts_json,updated_at=excluded.updated_at,finished_at=excluded.finished_at`,
		result.Verification.CutoverID, string(MigrationKindCutover), "committed", result.Verification.SourceDigest, result.Verification.MappingDigest, result.Backup.Path, result.Backup.SHA256, string(body), now, now, now)
	if err != nil {
		return fmt.Errorf("persist committed cutover result: %w", err)
	}
	return nil
}

func (s *Service) loadCutoverResult(ctx context.Context, cutoverID string) (MigrationResult, bool, error) {
	var body string
	err := s.db.QueryRowContext(ctx, `SELECT counts_json FROM blackboard_migration_runs WHERE id=? AND kind=? AND state='committed'`, cutoverID, string(MigrationKindCutover)).Scan(&body)
	if errors.Is(err, sql.ErrNoRows) {
		return MigrationResult{}, false, nil
	}
	if err != nil {
		return MigrationResult{}, false, err
	}
	var result MigrationResult
	if err := json.Unmarshal([]byte(body), &result); err != nil {
		return MigrationResult{}, false, fmt.Errorf("decode committed cutover result: %w", err)
	}
	return result, true, nil
}

func (s *Service) failCutover(point CutoverFailurePoint) error {
	if s.cutoverFailure == nil {
		return nil
	}
	return s.cutoverFailure.FailAfter(point)
}

var legacyBlackboardTables = []string{
	"project_facts", "project_fact_versions", "project_fact_relations", "fact_key_aliases",
	"findings", "finding_versions", "finding_key_aliases", "evidence_artifacts",
}

func installLegacyWriteGuards(ctx context.Context, tx *sql.Tx) error {
	for _, table := range legacyBlackboardTables {
		for _, operation := range []string{"insert", "update", "delete"} {
			trigger := "blackboard_legacy_" + table + "_" + operation + "_guard"
			statement := `CREATE TRIGGER "` + trigger + `" BEFORE ` + strings.ToUpper(operation) + ` ON "` + table + `" BEGIN SELECT RAISE(ABORT,'` + table + ` is frozen after graph_v1 cutover'); END`
			if _, err := tx.ExecContext(ctx, statement); err != nil {
				return fmt.Errorf("install %s legacy write guard: %w", table, err)
			}
		}
	}
	return nil
}

func sourceDigestInTransaction(ctx context.Context, tx *sql.Tx) (string, error) {
	hash := sha256.New()
	writeFrame(hash, []byte("legacy_blackboard_source_v1"))
	tables, err := migrationSourceTables(ctx, tx)
	if err != nil {
		return "", err
	}
	for _, table := range tables {
		rows, err := canonicalTableRows(ctx, tx, table)
		if err != nil {
			return "", err
		}
		writeFrame(hash, []byte(table))
		for _, row := range rows {
			writeFrame(hash, row)
		}
	}
	return "sha256:" + hex.EncodeToString(hash.Sum(nil)), nil
}

func (s *Service) verifyCommittedCutover(ctx context.Context) (CutoverVerificationV1, error) {
	var verification CutoverVerificationV1
	var epoch, state, migrationContract, backupPath, backupSHA string
	var graphSchemaVersion, postCutoverWrites int
	if err := s.db.QueryRowContext(ctx, `SELECT canonical_store,cutover_state,migration_contract_version,graph_schema_version,cutover_id,source_digest,mapping_digest,verified_backup_path,verified_backup_sha256,post_cutover_write_committed FROM blackboard_store_state WHERE id=1`).Scan(&epoch, &state, &migrationContract, &graphSchemaVersion, &verification.CutoverID, &verification.SourceDigest, &verification.MappingDigest, &backupPath, &backupSHA, &postCutoverWrites); err != nil {
		return verification, fmt.Errorf("read committed cutover state: %w", err)
	}
	if epoch != store.CanonicalStoreGraphV1 || (state != "graph" && state != "recovery_required") || migrationContract != "legacy_blackboard_to_graph_v1" || graphSchemaVersion != store.GraphSchemaVersion || verification.CutoverID == "" || verification.SourceDigest == "" || verification.MappingDigest == "" || backupPath == "" || backupSHA == "" {
		return verification, fmt.Errorf("committed cutover store state is incomplete or inconsistent")
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return verification, err
	}
	defer func() { _ = tx.Rollback() }()
	persistedMappingDigest, err := persistedLegacyMappingsDigest(ctx, tx)
	if err != nil {
		return verification, fmt.Errorf("verify legacy mapping digest: %w", err)
	}
	if persistedMappingDigest != verification.MappingDigest {
		return verification, fmt.Errorf("legacy mapping digest mismatch: state=%s persisted=%s", verification.MappingDigest, persistedMappingDigest)
	}
	for _, table := range legacyBlackboardTables {
		for _, operation := range []string{"insert", "update", "delete"} {
			trigger := "blackboard_legacy_" + table + "_" + operation + "_guard"
			var sqlText string
			if err := tx.QueryRowContext(ctx, `SELECT sql FROM sqlite_master WHERE type='trigger' AND name=?`, trigger).Scan(&sqlText); err != nil || !strings.Contains(sqlText, "frozen after graph_v1 cutover") {
				return verification, fmt.Errorf("legacy write guard missing or invalid: %s", trigger)
			}
		}
	}
	projectRows, err := tx.QueryContext(ctx, `SELECT id FROM projects ORDER BY id`)
	if err != nil {
		return verification, err
	}
	var projectIDs []string
	for projectRows.Next() {
		var projectID string
		if err := projectRows.Scan(&projectID); err != nil {
			projectRows.Close()
			return verification, err
		}
		projectIDs = append(projectIDs, projectID)
	}
	if err := projectRows.Close(); err != nil {
		return verification, err
	}
	parityHash := sha256.New()
	writeFrame(parityHash, []byte("legacy_blackboard_parity_v1"))
	for _, projectID := range projectIDs {
		var digest string
		if postCutoverWrites == 0 {
			digest, _, err = validateProjectImportParity(ctx, tx, projectID, s.artifactRoot)
		} else {
			digest, _, err = validateProjectPostCutoverCompatibility(ctx, tx, projectID)
		}
		if err != nil {
			return verification, fmt.Errorf("verify compatibility parity for Project %s: %w", projectID, err)
		}
		writeFrame(parityHash, []byte(digest))
	}
	verification.ParityDigest = hex.EncodeToString(parityHash.Sum(nil))
	if err := tx.Commit(); err != nil {
		return verification, err
	}

	verification.ProjectHashes = make(map[string]string, len(projectIDs))
	graph := blackboard.NewGraphService(s.db, nil, nil).WithArtifactRoot(s.artifactRoot)
	for _, projectID := range projectIDs {
		if err := graph.VerifyIntegrity(ctx, projectID); err != nil {
			return verification, fmt.Errorf("verify graph integrity for Project %s: %w", projectID, err)
		}
		var revision, dirty int
		var storedProjectionHash string
		if err := s.db.QueryRowContext(ctx, `SELECT current_graph_revision,projection_dirty_revision,COALESCE(current_main_projection_hash,'') FROM blackboard_graph_state WHERE project_id=?`, projectID).Scan(&revision, &dirty, &storedProjectionHash); err != nil {
			return verification, fmt.Errorf("read stored projection state for Project %s: %w", projectID, err)
		}
		reconstructed, err := graph.CanonicalMainGraph(ctx, projectID, revision)
		if err != nil {
			return verification, fmt.Errorf("reconstruct canonical projection for Project %s: %w", projectID, err)
		}
		if dirty != 0 || storedProjectionHash == "" || storedProjectionHash != reconstructed.Hash {
			return verification, fmt.Errorf("canonical projection corruption for Project %s: stored=%s reconstructed=%s dirty_revision=%d", projectID, storedProjectionHash, reconstructed.Hash, dirty)
		}
		projection, err := graph.RemeasureCanonicalMainGraph(ctx, projectID)
		if err != nil {
			return verification, fmt.Errorf("verify canonical projection for Project %s: %w", projectID, err)
		}
		if _, err := graph.RunHealth(ctx, projectID); err != nil {
			return verification, fmt.Errorf("verify Blackboard Health for Project %s: %w", projectID, err)
		}
		verification.ProjectHashes[projectID] = projection.Hash
	}
	verification.VerifiedAt = s.clock().UTC().Format(time.RFC3339Nano)
	resultHash := sha256.New()
	writeFrame(resultHash, []byte("legacy_blackboard_cutover_verification_v1"))
	writeFrame(resultHash, []byte(verification.CutoverID))
	writeFrame(resultHash, []byte(verification.SourceDigest))
	writeFrame(resultHash, []byte(verification.MappingDigest))
	writeFrame(resultHash, []byte(verification.ParityDigest))
	keys := make([]string, 0, len(verification.ProjectHashes))
	for key := range verification.ProjectHashes {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		writeFrame(resultHash, []byte(key))
		writeFrame(resultHash, []byte(verification.ProjectHashes[key]))
	}
	verification.ResultHash = hex.EncodeToString(resultHash.Sum(nil))
	if _, err := s.db.ExecContext(ctx, `UPDATE blackboard_store_state SET cutover_state='graph',latest_verification_at=?,latest_verification_result_hash=?,updated_at=? WHERE id=1 AND canonical_store=?`, verification.VerifiedAt, verification.ResultHash, verification.VerifiedAt, store.CanonicalStoreGraphV1); err != nil {
		return verification, fmt.Errorf("record successful cutover verification: %w", err)
	}
	return verification, nil
}

func (s *Service) defaultBackupPath() string {
	timestamp := s.clock().UTC().Format("20060102T150405.000000000Z")
	return s.databasePath + ".pre-graph-v1." + timestamp + ".bak"
}

var legacySourceTables = []string{
	"schema_migrations",
	"projects",
	"tasks",
	"task_runtime_config_versions",
	"task_continuations",
	"task_events",
	"task_summary_versions",
	"project_facts",
	"project_fact_versions",
	"project_fact_relations",
	"fact_key_aliases",
	"findings",
	"finding_versions",
	"finding_key_aliases",
	"evidence_artifacts",
}

func (s *Service) inspect(ctx context.Context) (LegacyMigrationPlanV1, error) {
	return inspectLegacyDatabase(ctx, s.db, s.artifactRoot)
}

type legacyInspectionDB interface {
	BeginTx(context.Context, *sql.TxOptions) (*sql.Tx, error)
}

func inspectLegacyDatabase(ctx context.Context, db legacyInspectionDB, artifactRoot string) (LegacyMigrationPlanV1, error) {
	tx, err := db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return LegacyMigrationPlanV1{}, fmt.Errorf("begin legacy inspection snapshot: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	schemaValidationErr := store.ValidateMigrationHistory(ctx, tx)

	counts := make(map[string]int, len(legacySourceTables))
	hash := sha256.New()
	writeFrame(hash, []byte("legacy_blackboard_source_v1"))
	tables, err := migrationSourceTables(ctx, tx)
	if err != nil {
		return LegacyMigrationPlanV1{}, err
	}
	for _, table := range tables {
		rows, err := canonicalTableRows(ctx, tx, table)
		if err != nil {
			return LegacyMigrationPlanV1{}, err
		}
		counts[table] = len(rows)
		writeFrame(hash, []byte(table))
		for _, row := range rows {
			writeFrame(hash, row)
		}
	}

	estimated := map[string]int{
		"projects":   counts["projects"],
		"task_goals": counts["tasks"],
		"facts":      counts["project_facts"],
		"findings":   counts["findings"],
		"evidence":   counts["evidence_artifacts"],
	}
	blockers, warnings, err := inspectDiagnostics(ctx, tx, schemaValidationErr, artifactRoot)
	if err != nil {
		return LegacyMigrationPlanV1{}, err
	}
	projects, err := inspectProjectPlans(ctx, tx)
	if err != nil {
		return LegacyMigrationPlanV1{}, err
	}
	decisions, err := inspectRequiredDecisions(ctx, tx)
	if err != nil {
		return LegacyMigrationPlanV1{}, err
	}
	return LegacyMigrationPlanV1{
		Schema:             "blackboard-v2-migration-plan/v1",
		SourceDigest:       "sha256:" + hex.EncodeToString(hash.Sum(nil)),
		Projects:           projects,
		ValidationBlockers: diagnosticBlockers(blockers),
		RequiredDecisions:  decisions,
		SourceCounts:       counts,
		EstimatedMappings:  estimated,
		Blockers:           blockers,
		Warnings:           warnings,
	}, nil
}

func migrationSourceTables(ctx context.Context, tx *sql.Tx) ([]string, error) {
	tables := append([]string(nil), legacySourceTables...)
	for _, table := range []string{"legacy_observations", "legacy_hypotheses", "legacy_project_directives"} {
		exists, err := tableExists(ctx, tx, table)
		if err != nil {
			return nil, err
		}
		if exists {
			tables = append(tables, table)
		}
	}
	return tables, nil
}

func tableExists(ctx context.Context, tx *sql.Tx, table string) (bool, error) {
	var count int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name=?`, table).Scan(&count); err != nil {
		return false, fmt.Errorf("inspect optional legacy source table %s: %w", table, err)
	}
	return count != 0, nil
}

func inspectProjectPlans(ctx context.Context, tx *sql.Tx) ([]MigrationProjectPlan, error) {
	plans := make(map[string][]MigrationMapping)
	projectRows, err := tx.QueryContext(ctx, `SELECT id FROM projects ORDER BY id`)
	if err != nil {
		return nil, fmt.Errorf("inspect migration Projects: %w", err)
	}
	for projectRows.Next() {
		var projectID string
		if err := projectRows.Scan(&projectID); err != nil {
			projectRows.Close()
			return nil, err
		}
		plans[projectID] = make([]MigrationMapping, 0)
	}
	if err := projectRows.Close(); err != nil {
		return nil, err
	}
	addRows := func(sourceType, query string) error {
		rows, err := tx.QueryContext(ctx, query)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var projectID, key string
			if err := rows.Scan(&projectID, &key); err != nil {
				return err
			}
			plans[projectID] = append(plans[projectID], MigrationMapping{SourceType: sourceType, SourceKey: key, Action: "retain", TargetKey: key})
		}
		return rows.Err()
	}
	if err := addRows("project_fact", `SELECT project_id,fact_key FROM project_facts ORDER BY project_id,fact_key`); err != nil {
		return nil, fmt.Errorf("inspect Project Fact mappings: %w", err)
	}
	if err := addRows("finding", `SELECT project_id,finding_key FROM findings ORDER BY project_id,finding_key`); err != nil {
		return nil, fmt.Errorf("inspect Finding mappings: %w", err)
	}
	if err := addRows("evidence", `SELECT project_id,evidence_key FROM evidence_artifacts ORDER BY project_id,evidence_key`); err != nil {
		return nil, fmt.Errorf("inspect Evidence mappings: %w", err)
	}
	projectIDs := make([]string, 0, len(plans))
	for projectID := range plans {
		projectIDs = append(projectIDs, projectID)
	}
	sort.Strings(projectIDs)
	result := make([]MigrationProjectPlan, 0, len(projectIDs))
	for _, projectID := range projectIDs {
		mappings := plans[projectID]
		sort.Slice(mappings, func(i, j int) bool {
			left := mappings[i].SourceType + "\x00" + mappings[i].SourceKey
			right := mappings[j].SourceType + "\x00" + mappings[j].SourceKey
			return left < right
		})
		result = append(result, MigrationProjectPlan{Project: projectID, Mappings: mappings})
	}
	return result, nil
}

func inspectRequiredDecisions(ctx context.Context, tx *sql.Tx) ([]MigrationDecision, error) {
	decisions := make([]MigrationDecision, 0)
	addOptionalRows := func(table, keyColumn, sourceType, where string, allowed []string) error {
		exists, err := tableExists(ctx, tx, table)
		if err != nil || !exists {
			return err
		}
		query := `SELECT project_id,` + keyColumn + ` FROM "` + table + `"` + where + ` ORDER BY project_id,` + keyColumn
		rows, err := tx.QueryContext(ctx, query)
		if err != nil {
			return fmt.Errorf("inspect %s decisions: %w", table, err)
		}
		defer rows.Close()
		for rows.Next() {
			var projectID, key string
			if err := rows.Scan(&projectID, &key); err != nil {
				return err
			}
			decisions = append(decisions, MigrationDecision{
				Source:         MigrationSourceRef{Project: projectID, Type: sourceType, Key: key},
				AllowedActions: append([]string(nil), allowed...),
			})
		}
		return rows.Err()
	}
	if err := addOptionalRows("legacy_hypotheses", "hypothesis_key", "hypothesis", ` WHERE status IN ('open','supported','contradicted','inconclusive')`, []string{"objective", "tentative_fact", "discard"}); err != nil {
		return nil, err
	}
	if err := addOptionalRows("legacy_project_directives", "directive_key", "project_directive", ` WHERE status='active'`, []string{"scope_limit", "objective"}); err != nil {
		return nil, err
	}
	if err := addOptionalRows("legacy_observations", "observation_key", "observation", ` WHERE confidence NOT IN ('tentative','confirmed')`, []string{"tentative_fact", "confirmed_fact"}); err != nil {
		return nil, err
	}
	sort.Slice(decisions, func(i, j int) bool {
		left := decisions[i].Source.Project + "\x00" + decisions[i].Source.Type + "\x00" + decisions[i].Source.Key
		right := decisions[j].Source.Project + "\x00" + decisions[j].Source.Type + "\x00" + decisions[j].Source.Key
		return left < right
	})
	return decisions, nil
}

func diagnosticBlockers(diagnostics []Diagnostic) []MigrationBlocker {
	blockers := make([]MigrationBlocker, 0, len(diagnostics))
	for _, diagnostic := range diagnostics {
		path := diagnostic.SourceTable
		if diagnostic.ProjectID != "" {
			path = diagnostic.ProjectID + "/" + path
		}
		if diagnostic.SourceID != "" {
			path += "/" + diagnostic.SourceID
		}
		blockers = append(blockers, MigrationBlocker{Code: diagnostic.Code, Message: diagnostic.Message, Path: path})
	}
	return blockers
}

func inspectDiagnostics(ctx context.Context, tx *sql.Tx, schemaValidationErr error, artifactRoot string) ([]Diagnostic, []Diagnostic, error) {
	blockers := make([]Diagnostic, 0)
	warnings := make([]Diagnostic, 0)

	if err := schemaValidationErr; err != nil {
		code := "unknown_schema_migration"
		if strings.Contains(err.Error(), "checksum mismatch") {
			code = "migration_checksum_mismatch"
		}
		blockers = append(blockers, Diagnostic{
			Code: code, SourceTable: "schema_migrations",
			Message:       "Numbered schema migration history is not valid for this binary.",
			RepairOptions: []string{"restore the expected migration history", "open the database with a compatible release"},
		})
	}

	var quickCheck string
	if err := tx.QueryRowContext(ctx, `PRAGMA quick_check`).Scan(&quickCheck); err != nil {
		return nil, nil, fmt.Errorf("run SQLite quick_check: %w", err)
	}
	if quickCheck != "ok" {
		blockers = append(blockers, Diagnostic{
			Code: "sqlite_integrity_failure", Message: "SQLite quick_check reported an integrity failure.",
			RepairOptions: []string{"restore a known-good database backup", "repair the SQLite database before retrying"},
		})
	}

	rows, err := tx.QueryContext(ctx, `
		SELECT c.id,t.project_id
		FROM task_continuations c
		JOIN tasks t ON t.id=c.task_id
		WHERE c.status IN ('pending','running','paused')
		ORDER BY t.project_id,c.id`)
	if err != nil {
		return nil, nil, fmt.Errorf("inspect active Continuations: %w", err)
	}
	for rows.Next() {
		var id, projectID string
		if err := rows.Scan(&id, &projectID); err != nil {
			rows.Close()
			return nil, nil, err
		}
		blockers = append(blockers, Diagnostic{
			Code: "active_continuation", ProjectID: projectID, SourceTable: "task_continuations", SourceID: id,
			Message:       "A pending, running, or paused Continuation must be reconciled before cutover.",
			RepairOptions: []string{"reconcile the Continuation to a terminal state"},
		})
	}
	if err := rows.Close(); err != nil {
		return nil, nil, err
	}

	factRows, err := tx.QueryContext(ctx, `SELECT id,project_id,fact_key,summary,confidence FROM project_facts ORDER BY project_id,id`)
	if err != nil {
		return nil, nil, fmt.Errorf("inspect current Project Facts: %w", err)
	}
	for factRows.Next() {
		var id, projectID, key, summary, confidence string
		if err := factRows.Scan(&id, &projectID, &key, &summary, &confidence); err != nil {
			factRows.Close()
			return nil, nil, err
		}
		if strings.TrimSpace(key) == "" || strings.TrimSpace(summary) == "" {
			blockers = append(blockers, Diagnostic{
				Code: "invalid_current_fact", ProjectID: projectID, SourceTable: "project_facts", SourceID: id,
				Message:       "A current Project Fact is missing its stable key or summary.",
				RepairOptions: []string{"supply a non-empty key and summary"},
			})
		}
		if !oneOf(confidence, "", "tentative", "confirmed", "deprecated") {
			blockers = append(blockers, Diagnostic{
				Code: "unknown_fact_confidence", ProjectID: projectID, SourceTable: "project_facts", SourceID: id,
				Message:       "A current Project Fact uses an unsupported confidence value.",
				RepairOptions: []string{"change confidence to tentative, confirmed, or deprecated"},
			})
		}
	}
	if err := factRows.Close(); err != nil {
		return nil, nil, err
	}

	findingRows, err := tx.QueryContext(ctx, `
		SELECT id,project_id,finding_key,title,status,target,proof,impact,recommendation,cvss_version,cvss_vector
		FROM findings ORDER BY project_id,id`)
	if err != nil {
		return nil, nil, fmt.Errorf("inspect current Findings: %w", err)
	}
	for findingRows.Next() {
		var id, projectID, key, title, status, target, proof, impact, recommendation, cvssVersion, cvssVector string
		if err := findingRows.Scan(&id, &projectID, &key, &title, &status, &target, &proof, &impact, &recommendation, &cvssVersion, &cvssVector); err != nil {
			findingRows.Close()
			return nil, nil, err
		}
		if strings.TrimSpace(key) == "" || strings.TrimSpace(title) == "" || !oneOf(status, "unconfirmed", "confirmed", "false_positive") {
			blockers = append(blockers, Diagnostic{
				Code: "invalid_current_finding", ProjectID: projectID, SourceTable: "findings", SourceID: id,
				Message:       "A current Finding is missing its key/title or uses an unsupported status.",
				RepairOptions: []string{"supply a key and title and use a supported status"},
			})
		}
		if status == "confirmed" && (blank(target) || blank(proof) || blank(impact) || blank(recommendation) || !validCVSS(cvssVersion, cvssVector)) {
			blockers = append(blockers, Diagnostic{
				Code: "confirmed_finding_incomplete", ProjectID: projectID, SourceTable: "findings", SourceID: id,
				Message:       "A confirmed Finding lacks complete report fields or a supported CVSS vector.",
				RepairOptions: []string{"complete the report fields and CVSS vector", "move the Finding out of confirmed status"},
			})
		}
	}
	if err := findingRows.Close(); err != nil {
		return nil, nil, err
	}

	historyBlockers, err := inspectHistoryGaps(ctx, tx)
	if err != nil {
		return nil, nil, err
	}
	blockers = append(blockers, historyBlockers...)

	factAliasWarnings, factAliasBlockers, err := inspectAliases(ctx, tx, "fact_key_aliases", "alias_fact_key", "canon_fact_key", "project_facts", "fact_key", "fact")
	if err != nil {
		return nil, nil, err
	}
	warnings = append(warnings, factAliasWarnings...)
	blockers = append(blockers, factAliasBlockers...)
	findingAliasWarnings, findingAliasBlockers, err := inspectAliases(ctx, tx, "finding_key_aliases", "alias_finding_key", "canon_finding_key", "findings", "finding_key", "finding")
	if err != nil {
		return nil, nil, err
	}
	warnings = append(warnings, findingAliasWarnings...)
	blockers = append(blockers, findingAliasBlockers...)

	evidenceRows, err := tx.QueryContext(ctx, `SELECT id,project_id,managed_path FROM evidence_artifacts ORDER BY project_id,id`)
	if err != nil {
		return nil, nil, fmt.Errorf("inspect Evidence Artifact paths: %w", err)
	}
	for evidenceRows.Next() {
		var id, projectID, managedPath string
		if err := evidenceRows.Scan(&id, &projectID, &managedPath); err != nil {
			evidenceRows.Close()
			return nil, nil, err
		}
		if !pathIsConfined(artifactRoot, managedPath) {
			warnings = append(warnings, Diagnostic{
				Code: "evidence_path_escape", ProjectID: projectID, SourceTable: "evidence_artifacts", SourceID: id,
				Message:       "An Evidence Artifact's managed path escapes the configured Artifact Root and will not be opened.",
				RepairOptions: []string{"move the artifact beneath the Artifact Root", "retain it as a deterministic missing reference"},
			})
		}
	}
	if err := evidenceRows.Close(); err != nil {
		return nil, nil, err
	}

	sortDiagnostics(blockers)
	sortDiagnostics(warnings)
	return blockers, warnings, nil
}

func inspectHistoryGaps(ctx context.Context, tx *sql.Tx) ([]Diagnostic, error) {
	checks := []struct{ table, keyColumn, code string }{
		{"project_fact_versions", "fact_key", "impossible_fact_history"},
		{"finding_versions", "finding_key", "impossible_finding_history"},
	}
	var diagnostics []Diagnostic
	for _, check := range checks {
		query := `SELECT project_id,` + check.keyColumn + `,MIN(version),MAX(version),COUNT(*) FROM "` + check.table + `" GROUP BY project_id,` + check.keyColumn + ` HAVING MIN(version)<>1 OR COUNT(*)<>MAX(version)`
		rows, err := tx.QueryContext(ctx, query)
		if err != nil {
			return nil, fmt.Errorf("inspect %s history: %w", check.table, err)
		}
		for rows.Next() {
			var projectID, key string
			var minVersion, maxVersion, count int
			if err := rows.Scan(&projectID, &key, &minVersion, &maxVersion, &count); err != nil {
				rows.Close()
				return nil, err
			}
			diagnostics = append(diagnostics, Diagnostic{
				Code: check.code, ProjectID: projectID, SourceTable: check.table, SourceID: key,
				Message:       "A legacy version sequence has a non-final gap and cannot be reconstructed deterministically.",
				RepairOptions: []string{"repair the version sequence before cutover"},
			})
		}
		if err := rows.Close(); err != nil {
			return nil, err
		}
	}
	return diagnostics, nil
}

func inspectAliases(ctx context.Context, tx *sql.Tx, aliasTable, aliasColumn, canonicalColumn, liveTable, liveKeyColumn, kind string) ([]Diagnostic, []Diagnostic, error) {
	query := `SELECT project_id,` + aliasColumn + `,` + canonicalColumn + ` FROM "` + aliasTable + `" ORDER BY project_id,` + aliasColumn
	rows, err := tx.QueryContext(ctx, query)
	if err != nil {
		return nil, nil, fmt.Errorf("inspect %s: %w", aliasTable, err)
	}
	aliases := make(map[string]map[string]string)
	for rows.Next() {
		var projectID, alias, canonical string
		if err := rows.Scan(&projectID, &alias, &canonical); err != nil {
			rows.Close()
			return nil, nil, err
		}
		if aliases[projectID] == nil {
			aliases[projectID] = make(map[string]string)
		}
		aliases[projectID][alias] = canonical
	}
	if err := rows.Close(); err != nil {
		return nil, nil, err
	}

	live := make(map[string]map[string]bool)
	liveRows, err := tx.QueryContext(ctx, `SELECT project_id,`+liveKeyColumn+` FROM "`+liveTable+`"`)
	if err != nil {
		return nil, nil, err
	}
	for liveRows.Next() {
		var projectID, key string
		if err := liveRows.Scan(&projectID, &key); err != nil {
			liveRows.Close()
			return nil, nil, err
		}
		if live[projectID] == nil {
			live[projectID] = make(map[string]bool)
		}
		live[projectID][key] = true
	}
	if err := liveRows.Close(); err != nil {
		return nil, nil, err
	}

	var warnings, blockers []Diagnostic
	for projectID, graph := range aliases {
		keys := make([]string, 0, len(graph))
		for key := range graph {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, alias := range keys {
			if live[projectID][alias] && alias != graph[alias] {
				blockers = append(blockers, Diagnostic{
					Code: "live_" + kind + "_key_shadowed_by_alias", ProjectID: projectID, SourceTable: aliasTable, SourceID: alias,
					Message:       "A live stable key is shadowed by an incompatible legacy alias.",
					RepairOptions: []string{"remove the conflicting alias or repair the live identity"},
				})
			}
			seen := map[string]bool{}
			cursor := alias
			for {
				if seen[cursor] {
					warnings = append(warnings, Diagnostic{
						Code: "cyclic_" + kind + "_alias", ProjectID: projectID, SourceTable: aliasTable, SourceID: alias,
						Message:       "A legacy alias cycle will remain audit-only and will not enter the current key registry.",
						RepairOptions: []string{"repair the alias chain before cutover", "retain it as audit-only metadata"},
					})
					break
				}
				seen[cursor] = true
				next, ok := graph[cursor]
				if !ok {
					if !live[projectID][cursor] {
						warnings = append(warnings, Diagnostic{
							Code: "dangling_" + kind + "_alias", ProjectID: projectID, SourceTable: aliasTable, SourceID: alias,
							Message:       "A legacy alias has no recoverable live target and will remain audit-only.",
							RepairOptions: []string{"repair the alias target before cutover", "retain it as audit-only metadata"},
						})
					}
					break
				}
				cursor = next
			}
		}
	}
	return warnings, blockers, nil
}

func validCVSS(version, vector string) bool {
	vector = strings.TrimSpace(vector)
	version = strings.TrimSpace(version)
	if strings.HasPrefix(vector, "CVSS:4.0/") {
		return version == "" || version == "4.0"
	}
	if strings.HasPrefix(vector, "CVSS:3.1/") {
		return version == "" || version == "3.1"
	}
	return false
}

func pathIsConfined(root, managedPath string) bool {
	if managedPath == "" || filepath.IsAbs(managedPath) {
		return false
	}
	root = filepath.Clean(root)
	candidate := filepath.Clean(filepath.Join(root, managedPath))
	relative, err := filepath.Rel(root, candidate)
	return err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))
}

func sortDiagnostics(diagnostics []Diagnostic) {
	sort.Slice(diagnostics, func(i, j int) bool {
		left := diagnostics[i]
		right := diagnostics[j]
		return left.Code+"\x00"+left.ProjectID+"\x00"+left.SourceTable+"\x00"+left.SourceID < right.Code+"\x00"+right.ProjectID+"\x00"+right.SourceTable+"\x00"+right.SourceID
	})
}

func oneOf(value string, allowed ...string) bool {
	for _, candidate := range allowed {
		if value == candidate {
			return true
		}
	}
	return false
}

func blank(value string) bool { return strings.TrimSpace(value) == "" }

func canonicalTableRows(ctx context.Context, tx *sql.Tx, table string) ([][]byte, error) {
	rows, err := tx.QueryContext(ctx, `SELECT * FROM "`+table+`"`)
	if err != nil {
		return nil, fmt.Errorf("read migration source table %s: %w", table, err)
	}
	defer rows.Close()

	columns, err := rows.Columns()
	if err != nil {
		return nil, fmt.Errorf("read migration source columns for %s: %w", table, err)
	}
	result := make([][]byte, 0)
	for rows.Next() {
		values := make([]any, len(columns))
		destinations := make([]any, len(columns))
		for i := range values {
			destinations[i] = &values[i]
		}
		if err := rows.Scan(destinations...); err != nil {
			return nil, fmt.Errorf("scan migration source table %s: %w", table, err)
		}
		var canonical bytes.Buffer
		for i, column := range columns {
			writeFrame(&canonical, []byte(column))
			writeFrame(&canonical, canonicalSQLValue(values[i]))
		}
		result = append(result, canonical.Bytes())
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate migration source table %s: %w", table, err)
	}
	sort.Slice(result, func(i, j int) bool { return bytes.Compare(result[i], result[j]) < 0 })
	return result, nil
}

func canonicalSQLValue(value any) []byte {
	switch value := value.(type) {
	case nil:
		return []byte("null")
	case int64:
		return []byte("integer:" + strconv.FormatInt(value, 10))
	case float64:
		return []byte("real:" + strconv.FormatFloat(value, 'g', -1, 64))
	case bool:
		return []byte("boolean:" + strconv.FormatBool(value))
	case []byte:
		return []byte("blob:" + hex.EncodeToString(value))
	case string:
		return append([]byte("text:"), value...)
	default:
		return []byte(fmt.Sprintf("%T:%v", value, value))
	}
}

type frameWriter interface {
	Write([]byte) (int, error)
}

func writeFrame(writer frameWriter, value []byte) {
	var length [8]byte
	binary.BigEndian.PutUint64(length[:], uint64(len(value)))
	_, _ = writer.Write(length[:])
	_, _ = writer.Write(value)
}
