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
	"errors"
	"fmt"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"pentest/internal/store"
)

type MigrationKind string

const (
	MigrationKindInspect        MigrationKind = "inspect"
	MigrationKindCutover        MigrationKind = "cutover"
	MigrationKindVerify         MigrationKind = "verify"
	MigrationKindFinalizeLegacy MigrationKind = "finalize_legacy"
)

var ErrUnsupportedMigrationKind = errors.New("unsupported Blackboard migration request kind")
var ErrMigrationBlocked = errors.New("Blackboard migration is blocked by inspection diagnostics")
var ErrCutoverImplementationPending = errors.New("verified backup complete; atomic graph cutover is implemented by M05")

type MigrationRequest struct {
	Kind       MigrationKind `json:"kind"`
	BackupPath string        `json:"backup_path,omitempty"`
}

type MigrationResult struct {
	Kind   MigrationKind         `json:"kind"`
	Plan   LegacyMigrationPlanV1 `json:"plan"`
	Backup *VerifiedBackup       `json:"backup,omitempty"`
	Import *LegacyImportResultV1 `json:"import,omitempty"`
}

type LegacyMigrationPlanV1 struct {
	SourceDigest      string         `json:"source_digest"`
	SourceCounts      map[string]int `json:"source_counts"`
	EstimatedMappings map[string]int `json:"estimated_mappings"`
	Blockers          []Diagnostic   `json:"blockers"`
	Warnings          []Diagnostic   `json:"warnings"`
}

type Diagnostic struct {
	Code          string   `json:"code"`
	ProjectID     string   `json:"project_id,omitempty"`
	SourceTable   string   `json:"source_table,omitempty"`
	SourceID      string   `json:"source_id,omitempty"`
	Message       string   `json:"message"`
	RepairOptions []string `json:"repair_options,omitempty"`
}

// BlackboardMigrationService is the only public migration seam used by
// startup and operator adapters.
type BlackboardMigrationService interface {
	Execute(context.Context, MigrationRequest) (MigrationResult, error)
}

type Service struct {
	db                     *store.DB
	databasePath           string
	artifactRoot           string
	backup                 BackupImplementation
	clock                  func() time.Time
	commitDisposableImport bool
}

type Option func(*Service)

func WithBackupImplementation(backup BackupImplementation) Option {
	return func(service *Service) { service.backup = backup }
}

func WithClock(clock func() time.Time) Option {
	return func(service *Service) { service.clock = clock }
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
		importResult, err := s.importLegacyGraph(ctx, plan.SourceDigest)
		if err != nil {
			return result, err
		}
		result.Import = &importResult
		return result, ErrCutoverImplementationPending
	default:
		return MigrationResult{}, fmt.Errorf("%w: %q", ErrUnsupportedMigrationKind, request.Kind)
	}
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
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return LegacyMigrationPlanV1{}, fmt.Errorf("begin legacy inspection snapshot: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	schemaValidationErr := store.ValidateMigrationHistory(ctx, tx)

	counts := make(map[string]int, len(legacySourceTables))
	hash := sha256.New()
	writeFrame(hash, []byte("legacy_blackboard_source_v1"))
	for _, table := range legacySourceTables {
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
	blockers, warnings, err := s.inspectDiagnostics(ctx, tx, schemaValidationErr)
	if err != nil {
		return LegacyMigrationPlanV1{}, err
	}
	return LegacyMigrationPlanV1{
		SourceDigest:      hex.EncodeToString(hash.Sum(nil)),
		SourceCounts:      counts,
		EstimatedMappings: estimated,
		Blockers:          blockers,
		Warnings:          warnings,
	}, nil
}

func (s *Service) inspectDiagnostics(ctx context.Context, tx *sql.Tx, schemaValidationErr error) ([]Diagnostic, []Diagnostic, error) {
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
		if !pathIsConfined(s.artifactRoot, managedPath) {
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
