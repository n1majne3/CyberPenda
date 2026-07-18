// Package blackboardmigration owns the isolated, offline v1 source decoder
// and the one-way validated migration to blackboard_v2.
package blackboardmigration

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"pentest/internal/store"
)

type MigrationKind string

const (
	MigrationKindInspect            MigrationKind = "inspect"
	MigrationKindBackup             MigrationKind = "backup"
	MigrationKindVerify             MigrationKind = "verify"
	MigrationKindRebuildUnambiguous MigrationKind = "rebuild_unambiguous"
	MigrationKindMigrate            MigrationKind = "migrate"
)

var ErrUnsupportedMigrationKind = errors.New("unsupported Blackboard migration request kind")
var ErrMigrationBlocked = errors.New("Blackboard migration is blocked by inspection diagnostics")
var ErrCutoverVerificationFailed = errors.New("post-cutover Blackboard verification failed")
var ErrCutoverConflict = errors.New("Blackboard cutover retry conflicts with the committed source digest")

type MigrationRequest struct {
	Kind         MigrationKind       `json:"kind"`
	BackupPath   string              `json:"backup_path,omitempty"`
	SourceDigest string              `json:"source_digest,omitempty"`
	Decisions    []MigrationDecision `json:"decisions,omitempty"`
}

type MigrationResult struct {
	Kind    MigrationKind             `json:"kind"`
	Plan    LegacyMigrationPlanV1     `json:"plan"`
	Backup  *VerifiedBackup           `json:"backup,omitempty"`
	Rebuild *RebuildResultV1          `json:"rebuild,omitempty"`
	Migrate *CutoverMigrationResultV1 `json:"migrate,omitempty"`
}

type CutoverMigrationResultV1 struct {
	Schema             string                   `json:"schema"`
	Status             string                   `json:"status"`
	VerifiedBackupPath string                   `json:"verified_backup_path"`
	ProjectCount       int                      `json:"project_count"`
	Projects           []RebuildProjectResultV1 `json:"projects"`
	Validation         MigrationValidationV1    `json:"validation"`
	StoreEpoch         string                   `json:"store_epoch"`
	Blockers           []MigrationBlocker       `json:"validation_blockers,omitempty"`
}

type LegacyMigrationPlanV1 struct {
	Schema             string                 `json:"schema"`
	SourceDigest       string                 `json:"source_digest"`
	Projects           []MigrationProjectPlan `json:"projects"`
	ValidationBlockers []MigrationBlocker     `json:"validation_blockers"`
	RequiredDecisions  []MigrationDecision    `json:"required_decisions"`
	SourceCounts       map[string]int         `json:"-"`
	EstimatedMappings  map[string]int         `json:"-"`
	Blockers           []Diagnostic           `json:"-"`
	Warnings           []Diagnostic           `json:"-"`
}

type Diagnostic struct {
	Code    string `json:"code"`
	Message string `json:"message"`
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

type BlackboardMigrationService interface {
	Execute(context.Context, MigrationRequest) (MigrationResult, error)
}

type Service struct {
	db             *store.DB
	databasePath   string
	artifactRoot   string
	backup         BackupImplementation
	clock          func() time.Time
	cutoverFailure CutoverFailureInjector
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

func WithBackupImplementation(backup BackupImplementation) Option {
	return func(s *Service) { s.backup = backup }
}
func WithClock(clock func() time.Time) Option { return func(s *Service) { s.clock = clock } }
func WithCutoverFailureInjector(injector CutoverFailureInjector) Option {
	return func(s *Service) { s.cutoverFailure = injector }
}

func NewService(db *store.DB, databasePath, artifactRoot string, options ...Option) *Service {
	s := &Service{db: db, databasePath: filepath.Clean(databasePath), artifactRoot: filepath.Clean(artifactRoot), backup: SQLiteBackupImplementation{}, clock: time.Now}
	for _, option := range options {
		option(s)
	}
	return s
}

func (s *Service) Execute(ctx context.Context, request MigrationRequest) (MigrationResult, error) {
	switch request.Kind {
	case MigrationKindInspect:
		plan, err := s.inspect(ctx)
		return MigrationResult{Kind: request.Kind, Plan: plan}, err
	case MigrationKindBackup:
		plan, err := s.inspect(ctx)
		if err != nil {
			return MigrationResult{Kind: request.Kind}, err
		}
		path := request.BackupPath
		if strings.TrimSpace(path) == "" {
			path = s.databasePath + ".pre-blackboard-v2.bak"
		}
		backup, err := s.backup.CreateVerifiedBackup(ctx, s.db, s.databasePath, path)
		return MigrationResult{Kind: request.Kind, Plan: plan, Backup: &backup}, err
	case MigrationKindVerify:
		return s.verifyBlackboardV2(ctx, request)
	case MigrationKindRebuildUnambiguous:
		rebuild, err := s.rebuildUnambiguousHeads(ctx, request)
		return MigrationResult{Kind: request.Kind, Rebuild: &rebuild}, err
	case MigrationKindMigrate:
		return s.migrateToBlackboardV2(ctx, request)
	default:
		return MigrationResult{}, fmt.Errorf("%w: %q", ErrUnsupportedMigrationKind, request.Kind)
	}
}

func (s *Service) failCutover(point CutoverFailurePoint) error {
	if s.cutoverFailure == nil {
		return nil
	}
	return s.cutoverFailure.FailAfter(point)
}

func (s *Service) inspect(ctx context.Context) (LegacyMigrationPlanV1, error) {
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return LegacyMigrationPlanV1{}, err
	}
	defer func() { _ = tx.Rollback() }()
	digest, err := sourceDigestInTransaction(ctx, tx)
	if err != nil {
		return LegacyMigrationPlanV1{}, err
	}
	projects, err := listProjectIDs(ctx, tx)
	if err != nil {
		return LegacyMigrationPlanV1{}, err
	}
	plan := LegacyMigrationPlanV1{
		Schema: "blackboard-v2-migration-plan/v1", SourceDigest: digest,
		Projects: []MigrationProjectPlan{}, ValidationBlockers: []MigrationBlocker{}, RequiredDecisions: []MigrationDecision{},
		SourceCounts: map[string]int{}, EstimatedMappings: map[string]int{},
	}
	for _, projectID := range projects {
		plan.Projects = append(plan.Projects, MigrationProjectPlan{Project: projectID, Mappings: []MigrationMapping{}})
	}
	return plan, nil
}

func InspectMigrationSource(ctx context.Context, source *store.MigrationSource, artifactRoot string) (MigrationResult, error) {
	if source.Classification() != store.MigrationSourceNumberedV1 {
		return MigrationResult{}, fmt.Errorf("offline migration inspect requires numbered v1 migration history, got %q", source.Classification())
	}
	plan, err := inspectSource(ctx, source.DB)
	return MigrationResult{Kind: MigrationKindInspect, Plan: plan}, err
}

func BackupMigrationSource(ctx context.Context, source *store.MigrationSource, sourcePath, artifactRoot, backupPath string) (MigrationResult, error) {
	if source.Classification() != store.MigrationSourceNumberedV1 {
		return MigrationResult{}, fmt.Errorf("offline migration backup requires numbered v1 migration history, got %q", source.Classification())
	}
	plan, err := inspectSource(ctx, source.DB)
	if err != nil {
		return MigrationResult{Kind: MigrationKindBackup}, err
	}
	if strings.TrimSpace(backupPath) == "" {
		backupPath = filepath.Clean(sourcePath) + ".pre-blackboard-v2.bak"
	}
	backup, err := CreateVerifiedMigrationSourceBackup(ctx, source.DB, backupPath)
	result := MigrationResult{Kind: MigrationKindBackup, Plan: plan}
	if err != nil {
		result.Plan.ValidationBlockers = append(result.Plan.ValidationBlockers, MigrationBlocker{
			Code: "backup_verification_failed", Message: "The pre-cutover backup could not be created and independently verified.", Path: backupPath,
		})
		return result, err
	}
	result.Backup = &backup
	return result, nil
}

func inspectSource(ctx context.Context, db *sql.DB) (LegacyMigrationPlanV1, error) {
	tx, err := db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return LegacyMigrationPlanV1{}, err
	}
	defer func() { _ = tx.Rollback() }()
	digest, err := sourceDigestInTransaction(ctx, tx)
	if err != nil {
		return LegacyMigrationPlanV1{}, err
	}
	rows, err := tx.QueryContext(ctx, `SELECT id FROM projects ORDER BY id`)
	if err != nil {
		return LegacyMigrationPlanV1{}, err
	}
	defer rows.Close()
	plan := LegacyMigrationPlanV1{
		Schema: "blackboard-v2-migration-plan/v1", SourceDigest: digest,
		Projects: []MigrationProjectPlan{}, ValidationBlockers: []MigrationBlocker{}, RequiredDecisions: []MigrationDecision{},
		SourceCounts: map[string]int{}, EstimatedMappings: map[string]int{},
	}
	for rows.Next() {
		var projectID string
		if err := rows.Scan(&projectID); err != nil {
			return LegacyMigrationPlanV1{}, err
		}
		plan.Projects = append(plan.Projects, MigrationProjectPlan{Project: projectID, Mappings: []MigrationMapping{}})
	}
	return plan, rows.Err()
}

func sourceDigestInTransaction(ctx context.Context, tx *sql.Tx) (string, error) {
	// Migration history is validated independently by the read-only source seam.
	// The writable migrator adds disposable v2 migrations before rebuilding, so
	// including schema_migrations here would invalidate every inspected source
	// without any legacy semantic row having changed.
	rows, err := tx.QueryContext(ctx, `SELECT name FROM sqlite_master WHERE type='table' AND name NOT LIKE 'blackboard_v2_%' AND name NOT IN ('sqlite_sequence','schema_migrations') ORDER BY name`)
	if err != nil {
		return "", err
	}
	defer rows.Close()
	var tables []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return "", err
		}
		tables = append(tables, name)
	}
	if err := rows.Err(); err != nil {
		return "", err
	}
	h := sha256.New()
	for _, table := range tables {
		writeFrame(h, []byte(table))
		values, err := canonicalTableRows(ctx, tx, table)
		if err != nil {
			return "", err
		}
		for _, value := range values {
			writeFrame(h, value)
		}
	}
	return "sha256:" + hex.EncodeToString(h.Sum(nil)), nil
}

func canonicalTableRows(ctx context.Context, tx *sql.Tx, table string) ([][]byte, error) {
	quoted := `"` + strings.ReplaceAll(table, `"`, `""`) + `"`
	rows, err := tx.QueryContext(ctx, `SELECT * FROM `+quoted)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	columns, err := rows.Columns()
	if err != nil {
		return nil, err
	}
	values := make([][]byte, 0)
	for rows.Next() {
		raw := make([]any, len(columns))
		scan := make([]any, len(columns))
		for i := range raw {
			scan[i] = &raw[i]
		}
		if err := rows.Scan(scan...); err != nil {
			return nil, err
		}
		parts := make([]string, len(raw))
		for i, value := range raw {
			parts[i] = fmt.Sprintf("%T:%v", value, value)
		}
		values = append(values, []byte(strings.Join(parts, "\x00")))
	}
	return values, rows.Err()
}

func writeFrame(h interface{ Write([]byte) (int, error) }, value []byte) {
	_, _ = h.Write(value)
	_, _ = h.Write([]byte{0})
}

func tableExists(ctx context.Context, tx *sql.Tx, table string) (bool, error) {
	var found int
	err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name=?`, table).Scan(&found)
	return found != 0, err
}

func shortHash(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:8])
}

func sortedStrings(values []string) []string { sort.Strings(values); return values }
