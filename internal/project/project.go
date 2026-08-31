// Package project owns the project domain: the project, scope, and project
// defaults models plus the service that implements the business rules shared by
// HTTP, MCP, and CLI handlers. Transport layers call into Service; they do not
// touch SQLite directly.
package project

import (
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"pentest/internal/store"
)

// Scope captures the asset boundaries and testing limits that define what the
// agent is authorized to do within a project.
type Scope struct {
	Domains       []string `json:"domains,omitempty"`
	IPs           []string `json:"ips,omitempty"`
	CIDRs         []string `json:"cidrs,omitempty"`
	URLs          []string `json:"urls,omitempty"`
	Ports         []string `json:"ports,omitempty"`
	Excluded      []string `json:"excluded,omitempty"`
	TestingLimits []string `json:"testing_limits,omitempty"`
	Notes         string   `json:"notes,omitempty"`
	Capabilities  []string `json:"capabilities,omitempty"`
}

// Runner names the execution boundary selected for a task.
type Runner string

const (
	// RunnerSandbox runs a runtime inside a sandbox and is the default.
	RunnerSandbox Runner = "sandbox"
	// RunnerHost runs a runtime on the host and must be explicitly activated.
	RunnerHost Runner = "host"
)

// Defaults are project-level choices for new tasks. They select a default
// runtime profile, runner, and task policy; they do not copy global runtime
// profiles.
type Defaults struct {
	Runner     Runner `json:"runner,omitempty"`
	TaskPolicy string `json:"task_policy,omitempty"`
}

// KindPentest is the default Project kind: a bounded security-testing
// engagement. Tasks complete against their Task Goals; the Project has no
// automatic solved state.
const KindPentest = "pentest"

// KindCTFChallenge is the Project kind for a single CTF challenge whose
// Project is solved when the current Blackboard contains a verified flag Solution.
const KindCTFChallenge = "ctf_challenge"

// Project is a bounded security-testing engagement with its own scope, tasks,
// memory, evidence, and report.
type Project struct {
	ID          string    `json:"id"`
	Name        string    `json:"name"`
	Description string    `json:"description"`
	Kind        string    `json:"kind"`
	Scope       Scope     `json:"scope"`
	Defaults    Defaults  `json:"defaults"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

// ErrNotFound is returned when no project matches the requested id.
var ErrNotFound = errors.New("project not found")

// ErrInvalidKind is returned when a Project kind is outside the closed set.
var ErrInvalidKind = errors.New("invalid project kind")

// ErrKindConversionBlocked is returned when a Project cannot change kind
// without mixing incompatible lifecycle or Project Knowledge semantics.
var ErrKindConversionBlocked = errors.New("project kind conversion is blocked")

const (
	KindConversionBlockerActiveTasks           = "active_tasks"
	KindConversionBlockerIncompatibleFindings  = "incompatible_findings"
	KindConversionBlockerIncompatibleSolutions = "incompatible_solutions"
)

// KindConversionBlocker is one stable reason that prevents Project Kind
// Conversion. Count is included so callers do not need storage knowledge.
type KindConversionBlocker struct {
	Code  string `json:"code"`
	Count int    `json:"count"`
}

// KindConversionPreview is the read-only result shown before an operator
// confirms Project Kind Conversion.
type KindConversionPreview struct {
	ProjectID   string                  `json:"project_id"`
	CurrentKind string                  `json:"current_kind"`
	TargetKind  string                  `json:"target_kind"`
	Ready       bool                    `json:"ready"`
	Blockers    []KindConversionBlocker `json:"blockers"`
}

// Service implements project business rules against SQLite.
type Service struct {
	db *store.DB
}

// NewService returns a Service backed by the given database.
func NewService(db *store.DB) *Service {
	return &Service{db: db}
}

// Create stores a new Pentest Project and returns it with a generated id and timestamps.
func (s *Service) Create(name, description string, scope Scope, defaults Defaults) (Project, error) {
	return s.CreateWithKind(name, description, KindPentest, scope, defaults)
}

// CreateWithKind stores a Project of one of the closed Project kinds. It is the
// creation seam used by graph-domain tests and later Project Interface wiring
// for CTF challenges; ordinary callers continue to use Create.
func (s *Service) CreateWithKind(name, description, kind string, scope Scope, defaults Defaults) (Project, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return Project{}, ErrMissingName
	}
	if !validKind(kind) {
		return Project{}, ErrInvalidKind
	}

	now := time.Now().UTC()
	created := Project{
		ID:          newID(),
		Name:        name,
		Description: description,
		Kind:        kind,
		Scope:       scope,
		Defaults:    defaults,
		CreatedAt:   now,
		UpdatedAt:   now,
	}

	scopeJSON, err := json.Marshal(created.Scope)
	if err != nil {
		return Project{}, fmt.Errorf("encode scope: %w", err)
	}
	defaultsJSON, err := json.Marshal(created.Defaults)
	if err != nil {
		return Project{}, fmt.Errorf("encode defaults: %w", err)
	}

	_, err = s.db.Exec(
		`INSERT INTO projects (id, name, description, kind, scope_json, defaults_json, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		created.ID, created.Name, created.Description, created.Kind, string(scopeJSON), string(defaultsJSON),
		created.CreatedAt.Format(time.RFC3339Nano), created.UpdatedAt.Format(time.RFC3339Nano),
	)
	if err != nil {
		return Project{}, fmt.Errorf("store project: %w", err)
	}

	return created, nil
}

// Get loads a single project by id.
func (s *Service) Get(id string) (Project, error) {
	row := s.db.QueryRow(
		`SELECT id, name, description, kind, scope_json, defaults_json, created_at, updated_at FROM projects WHERE id = ?`,
		id,
	)
	return scanProject(row)
}

// List returns all projects ordered by creation time.
func (s *Service) List() ([]Project, error) {
	rows, err := s.db.Query(
		`SELECT id, name, description, kind, scope_json, defaults_json, created_at, updated_at FROM projects ORDER BY created_at ASC`,
	)
	if err != nil {
		return nil, fmt.Errorf("list projects: %w", err)
	}
	defer rows.Close()

	var projects []Project
	for rows.Next() {
		found, err := scanProject(rows)
		if err != nil {
			return nil, fmt.Errorf("scan project: %w", err)
		}
		projects = append(projects, found)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list projects: %w", err)
	}
	return projects, nil
}

// PreviewKindConversion reports every current blocker without changing the
// Project. The closed Project kinds remain a strong semantic invariant.
func (s *Service) PreviewKindConversion(id, targetKind string) (KindConversionPreview, error) {
	if !validKind(targetKind) {
		return KindConversionPreview{}, ErrInvalidKind
	}
	tx, err := s.db.Begin()
	if err != nil {
		return KindConversionPreview{}, fmt.Errorf("begin Project Kind Conversion preview: %w", err)
	}
	defer tx.Rollback()
	preview, err := previewKindConversionTx(tx, id, targetKind)
	if err != nil {
		return KindConversionPreview{}, err
	}
	if err := tx.Commit(); err != nil {
		return KindConversionPreview{}, fmt.Errorf("commit Project Kind Conversion preview: %w", err)
	}
	return preview, nil
}

// ConvertKind changes only the Project kind after checking the same blockers
// within one transaction. It never converts Blackboard record types.
func (s *Service) ConvertKind(id, targetKind string) (Project, error) {
	if !validKind(targetKind) {
		return Project{}, ErrInvalidKind
	}
	tx, err := s.db.Begin()
	if err != nil {
		return Project{}, fmt.Errorf("begin Project Kind Conversion: %w", err)
	}
	defer tx.Rollback()
	preview, err := previewKindConversionTx(tx, id, targetKind)
	if err != nil {
		return Project{}, err
	}
	if !preview.Ready {
		return Project{}, ErrKindConversionBlocked
	}
	if preview.CurrentKind != targetKind {
		now := time.Now().UTC().Format(time.RFC3339Nano)
		if _, err := tx.Exec(`UPDATE projects SET kind=?, updated_at=? WHERE id=?`, targetKind, now, id); err != nil {
			return Project{}, fmt.Errorf("store Project Kind Conversion: %w", err)
		}
	}
	converted, err := scanProject(tx.QueryRow(`SELECT id,name,description,kind,scope_json,defaults_json,created_at,updated_at FROM projects WHERE id=?`, id))
	if err != nil {
		return Project{}, err
	}
	if err := tx.Commit(); err != nil {
		return Project{}, fmt.Errorf("commit Project Kind Conversion: %w", err)
	}
	return converted, nil
}

func previewKindConversionTx(tx *sql.Tx, id, targetKind string) (KindConversionPreview, error) {
	var currentKind string
	if err := tx.QueryRow(`SELECT kind FROM projects WHERE id=?`, id).Scan(&currentKind); errors.Is(err, sql.ErrNoRows) {
		return KindConversionPreview{}, ErrNotFound
	} else if err != nil {
		return KindConversionPreview{}, fmt.Errorf("read Project Kind: %w", err)
	}
	preview := KindConversionPreview{ProjectID: id, CurrentKind: currentKind, TargetKind: targetKind, Blockers: []KindConversionBlocker{}}
	if currentKind == targetKind {
		preview.Ready = true
		return preview, nil
	}
	var activeTasks int
	if err := tx.QueryRow(`SELECT COUNT(*) FROM tasks WHERE project_id=? AND status NOT IN ('completed','failed','stopped','interrupted')`, id).Scan(&activeTasks); err != nil {
		return KindConversionPreview{}, fmt.Errorf("count active Tasks: %w", err)
	}
	if activeTasks > 0 {
		preview.Blockers = append(preview.Blockers, KindConversionBlocker{Code: KindConversionBlockerActiveTasks, Count: activeTasks})
	}
	incompatibleType := "finding"
	incompatibleCode := KindConversionBlockerIncompatibleFindings
	if targetKind == KindPentest {
		incompatibleType = "solution"
		incompatibleCode = KindConversionBlockerIncompatibleSolutions
	}
	var incompatible int
	if err := tx.QueryRow(`SELECT COUNT(*) FROM blackboard_v2_records WHERE project_id=? AND type=?`, id, incompatibleType).Scan(&incompatible); err != nil {
		return KindConversionPreview{}, fmt.Errorf("count incompatible Project Knowledge: %w", err)
	}
	if incompatible > 0 {
		preview.Blockers = append(preview.Blockers, KindConversionBlocker{Code: incompatibleCode, Count: incompatible})
	}
	preview.Ready = len(preview.Blockers) == 0
	return preview, nil
}

func validKind(kind string) bool {
	return kind == KindPentest || kind == KindCTFChallenge
}

// LatestUpdate returns the newest updated_at across every Project, the durable
// Project half of the Project Navigation Projection revision. Creation and
// rename both advance updated_at, so one indexed read covers every
// navigation-visible Project summary change (#201).
func (s *Service) LatestUpdate() (time.Time, error) {
	var value string
	if err := s.db.QueryRow(`SELECT MAX(updated_at) FROM projects`).Scan(&value); err != nil {
		return time.Time{}, fmt.Errorf("latest project update: %w", err)
	}
	if value == "" {
		return time.Time{}, nil
	}
	parsed, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		return time.Time{}, fmt.Errorf("parse latest project update: %w", err)
	}
	return parsed, nil
}

// Update applies non-empty fields to an existing project. Empty name fields are
// rejected; empty scope fields preserve the existing scope so partial updates
// do not erase configured boundaries.
func (s *Service) Update(id string, name, description string, scope Scope, scopeTouched bool, defaults Defaults, defaultsTouched bool) (Project, error) {
	existing, err := s.Get(id)
	if err != nil {
		return Project{}, err
	}

	if name = strings.TrimSpace(name); name == "" {
		return Project{}, ErrMissingName
	}
	existing.Name = name
	existing.Description = description
	if scopeTouched {
		existing.Scope = scope
	}
	if defaultsTouched {
		existing.Defaults = defaults
	}
	existing.UpdatedAt = time.Now().UTC()

	scopeJSON, err := json.Marshal(existing.Scope)
	if err != nil {
		return Project{}, fmt.Errorf("encode scope: %w", err)
	}
	defaultsJSON, err := json.Marshal(existing.Defaults)
	if err != nil {
		return Project{}, fmt.Errorf("encode defaults: %w", err)
	}

	_, err = s.db.Exec(
		`UPDATE projects SET name = ?, description = ?, scope_json = ?, defaults_json = ?, updated_at = ? WHERE id = ?`,
		existing.Name, existing.Description, string(scopeJSON), string(defaultsJSON),
		existing.UpdatedAt.Format(time.RFC3339Nano), existing.ID,
	)
	if err != nil {
		return Project{}, fmt.Errorf("store project update: %w", err)
	}
	return existing, nil
}

type scanner interface {
	Scan(dest ...any) error
}

func scanProject(row scanner) (Project, error) {
	var found Project
	var scopeJSON string
	var defaultsJSON string
	var createdAt string
	var updatedAt string

	err := row.Scan(&found.ID, &found.Name, &found.Description, &found.Kind, &scopeJSON, &defaultsJSON, &createdAt, &updatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return Project{}, ErrNotFound
	}
	if err != nil {
		return Project{}, err
	}
	if err := json.Unmarshal([]byte(scopeJSON), &found.Scope); err != nil {
		return Project{}, fmt.Errorf("decode scope: %w", err)
	}
	if err := json.Unmarshal([]byte(defaultsJSON), &found.Defaults); err != nil {
		// Older rows may predate the defaults column; treat as empty.
		found.Defaults = Defaults{}
	}
	if found.CreatedAt, err = time.Parse(time.RFC3339Nano, createdAt); err != nil {
		return Project{}, fmt.Errorf("parse created_at: %w", err)
	}
	if found.UpdatedAt, err = time.Parse(time.RFC3339Nano, updatedAt); err != nil {
		return Project{}, fmt.Errorf("parse updated_at: %w", err)
	}
	return found, nil
}

// ErrMissingName is returned when a project has no non-empty name.
var ErrMissingName = errors.New("project name is required")

func newID() string {
	var bytes [16]byte
	if _, err := rand.Read(bytes[:]); err != nil {
		return hex.EncodeToString([]byte(time.Now().UTC().Format(time.RFC3339Nano)))
	}
	return hex.EncodeToString(bytes[:])
}
