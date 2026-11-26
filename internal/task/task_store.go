package task

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"pentest/internal/project"
	"pentest/internal/store"
)

// Service implements task business rules against SQLite. It depends on the
// project service only to read the scope at launch; it does not mutate projects.
// ContinuationTerminalMarker closes capabilities whose lifecycle is bound to
// a Continuation when that Continuation reaches a terminal Task status.
type ContinuationTerminalMarker interface {
	MarkContinuationTerminal(context.Context, string) error
}

// ContinuationReconciler closes open semantic work after the Task domain has
// durably made a Continuation terminal.
type ContinuationReconciler interface {
	ReconcileTerminalContinuation(context.Context, string, string) error
}

type Service struct {
	db             *store.DB
	projects       *project.Service
	terminalMarker ContinuationTerminalMarker
	reconciler     ContinuationReconciler
}

// NewService returns a Service backed by the given database. It reads project
// scope through the provided project service to capture the launch snapshot.
func NewService(db *store.DB, projects ...*project.Service) *Service {
	svc := &Service{db: db}
	// Optional dependency injection: callers may pass a project service so the
	// task service can capture scope snapshots. If omitted, scope snapshots are
	// empty and the HTTP layer supplies the project service before launch.
	if len(projects) > 0 {
		svc.projects = projects[0]
	}
	return svc
}

func (s *Service) SetProjectService(projects *project.Service) {
	s.projects = projects
}

// SetContinuationTerminalMarker wires the lifecycle projection that closes
// Continuation-scoped capabilities in the same production assembly.
func (s *Service) SetContinuationTerminalMarker(marker ContinuationTerminalMarker) {
	s.terminalMarker = marker
}

// SetContinuationReconciler wires post-terminal semantic reconciliation.
// adapter. The Task domain still owns the Continuation status transition.
func (s *Service) SetContinuationReconciler(reconciler ContinuationReconciler) {
	s.reconciler = reconciler
}

// Create launches a new task: it validates the goal and runner, captures an
// immutable scope snapshot from the project, and persists the task.
func (s *Service) Create(req CreateRequest) (Task, error) {
	if req.Goal == "" {
		return Task{}, ErrMissingGoal
	}
	if req.Runner != RunnerSandbox && req.Runner != RunnerHost {
		return Task{}, ErrUnsupportedRunner
	}
	mode, err := normalizeBlackboardMode(req.RunControls.BlackboardMode)
	if err != nil {
		return Task{}, err
	}
	// Canonicalize new input only; stored snapshots retain their original mode.
	if mode == BlackboardModeInteractive {
		mode = BlackboardModeWorkingGraph
	}
	req.RunControls.BlackboardMode = mode
	if err := req.RunControls.validate(); err != nil {
		return Task{}, err
	}

	// Capture the scope snapshot from the live project. If a project service is
	// wired, read it; otherwise the snapshot is empty (caller is responsible for
	// providing scope out-of-band, e.g. the HTTP layer).
	var snapshot ScopeSnapshot
	var projectKind string
	var blackboardProtocol string
	if s.projects != nil {
		proj, err := s.projects.Get(req.ProjectID)
		if err != nil {
			if errors.Is(err, project.ErrNotFound) {
				return Task{}, ErrProjectNotFound
			}
			return Task{}, fmt.Errorf("read project scope: %w", err)
		}
		snapshot = proj.Scope
		projectKind = proj.Kind
		blackboardProtocol = proj.BlackboardProtocol
	}
	if req.Type != TypePentest && req.Type != TypeCTFChallenge {
		return Task{}, ErrInvalidTaskType
	}
	if projectKind != "" && string(req.Type) != projectKind {
		return Task{}, ErrTaskTypeProjectKindMismatch
	}

	now := time.Now().UTC()
	created := Task{
		BlackboardProtocol:   blackboardProtocol,
		ID:                   newID(),
		ProjectID:            req.ProjectID,
		Type:                 req.Type,
		Goal:                 req.Goal,
		Status:               StatusPending,
		Runner:               req.Runner,
		RuntimeProfileID:     req.RuntimeProfileID,
		RunControls:          req.RunControls,
		BlackboardConclusion: BlackboardConclusion{Mode: mode, State: BlackboardConclusionStateClean},
		ScopeSnapshot:        snapshot,
		CreatedAt:            now,
		UpdatedAt:            now,
	}

	runControlsJSON, err := json.Marshal(created.RunControls)
	if err != nil {
		return Task{}, fmt.Errorf("encode run controls: %w", err)
	}
	scopeJSON, err := json.Marshal(created.ScopeSnapshot)
	if err != nil {
		return Task{}, fmt.Errorf("encode scope snapshot: %w", err)
	}

	var runtimeConfigJSON []byte
	if req.RuntimeConfig != nil {
		runtimeConfigJSON, err = json.Marshal(req.RuntimeConfig)
		if err != nil {
			return Task{}, fmt.Errorf("encode initial Runtime configuration: %w", err)
		}
	}
	tx, err := s.db.Begin()
	if err != nil {
		return Task{}, fmt.Errorf("begin Task creation: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	_, err = tx.Exec(
		`INSERT INTO tasks (id, project_id, task_type, goal, status, runner, runtime_profile_id, run_controls_json, scope_snapshot_json, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		created.ID, created.ProjectID, string(created.Type), created.Goal, string(created.Status), string(created.Runner),
		created.RuntimeProfileID, string(runControlsJSON), string(scopeJSON),
		created.CreatedAt.Format(time.RFC3339Nano), created.UpdatedAt.Format(time.RFC3339Nano),
	)
	if err != nil {
		return Task{}, fmt.Errorf("store task: %w", err)
	}
	if req.RuntimeConfig != nil {
		if _, err := tx.Exec(
			`INSERT INTO task_runtime_config_versions (id,task_id,version,runtime_profile_id,config_json,created_at) VALUES (?,?,?,?,?,?)`,
			newID(), created.ID, 1, created.RuntimeProfileID, string(runtimeConfigJSON), created.CreatedAt.Format(time.RFC3339Nano),
		); err != nil {
			return Task{}, fmt.Errorf("store initial Task Runtime configuration: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return Task{}, fmt.Errorf("commit Task creation: %w", err)
	}
	return created, nil
}

func (s *Service) Get(id string) (Task, error) {
	return scanTask(s.db.QueryRow(
		`SELECT id, project_id, task_type, goal, status, runner, runtime_profile_id, run_controls_json, scope_snapshot_json, created_at, updated_at, (SELECT blackboard_protocol FROM projects WHERE projects.id=tasks.project_id) FROM tasks WHERE id = ? AND deleted_at = ''`,
		id,
	))
}

// ListForProject returns tasks for a project ordered by creation time.
func (s *Service) ListForProject(projectID string) ([]Task, error) {
	rows, err := s.db.Query(
		`SELECT id, project_id, task_type, goal, status, runner, runtime_profile_id, run_controls_json, scope_snapshot_json, created_at, updated_at, (SELECT blackboard_protocol FROM projects WHERE projects.id=tasks.project_id)
		 FROM tasks WHERE project_id = ? AND deleted_at = '' ORDER BY created_at ASC`,
		projectID,
	)
	if err != nil {
		return nil, fmt.Errorf("list tasks: %w", err)
	}
	defer rows.Close()

	var tasks []Task
	for rows.Next() {
		found, err := scanTask(rows)
		if err != nil {
			return nil, fmt.Errorf("scan task: %w", err)
		}
		tasks = append(tasks, found)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list tasks: %w", err)
	}
	return tasks, nil
}

// taskSelectColumns lists the columns scanned by the shared Task projections.
// Keep it in sync with the table columns used by scanTask.
const taskSelectColumns = `id, project_id, task_type, goal, status, runner, runtime_profile_id, run_controls_json, scope_snapshot_json, created_at, updated_at, (SELECT blackboard_protocol FROM projects WHERE projects.id=tasks.project_id)`

func recentPerProjectSQL(limit, excludeCount int) string {
	query := `SELECT ` + taskSelectColumns + ` FROM tasks WHERE project_id = ? AND deleted_at = ''`
	if excludeCount > 0 {
		query += ` AND id NOT IN (` + strings.TrimSuffix(strings.Repeat(`?,`, excludeCount), `,`) + `)`
	}
	return query + ` ORDER BY updated_at DESC, created_at DESC LIMIT ?`
}

func (s *Service) ListRecentPerProject(projectIDs []string, limit int, excludeIDs ...string) (map[string][]Task, error) {
	if limit <= 0 {
		limit = 1
	}
	byProject := make(map[string][]Task, len(projectIDs))
	for _, projectID := range projectIDs {
		args := make([]any, 0, len(excludeIDs)+2)
		args = append(args, projectID)
		for _, id := range excludeIDs {
			args = append(args, id)
		}
		args = append(args, limit)
		rows, err := s.db.Query(recentPerProjectSQL(limit, len(excludeIDs)), args...)
		if err != nil {
			return nil, fmt.Errorf("list recent tasks: %w", err)
		}
		var tasks []Task
		for rows.Next() {
			found, err := scanTask(rows)
			if err != nil {
				rows.Close()
				return nil, fmt.Errorf("scan task: %w", err)
			}
			tasks = append(tasks, found)
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return nil, fmt.Errorf("list recent tasks: %w", err)
		}
		rows.Close()
		if tasks != nil {
			byProject[projectID] = tasks
		}
	}
	return byProject, nil
}

func (s *Service) LatestUpdate() (time.Time, error) {
	var value string
	if err := s.db.QueryRow(`SELECT COALESCE(MAX(updated_at), '') FROM tasks`).Scan(&value); err != nil {
		return time.Time{}, fmt.Errorf("latest task update: %w", err)
	}
	if value == "" {
		return time.Time{}, nil
	}
	parsed, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		return time.Time{}, fmt.Errorf("parse latest task update: %w", err)
	}
	return parsed, nil
}

func (s *Service) Delete(id string) error {
	now := time.Now().UTC().Format(time.RFC3339Nano)
	result, err := s.db.Exec(
		`UPDATE tasks SET deleted_at = ?, updated_at = ?
		 WHERE id = ? AND deleted_at = '' AND status NOT IN (?, ?, ?)`,
		now, now, id,
		string(StatusPending), string(StatusRunning), string(StatusPaused),
	)
	if err != nil {
		return fmt.Errorf("delete task: %w", err)
	}
	updated, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("delete task rows affected: %w", err)
	}
	if updated == 1 {
		return nil
	}

	var status string
	var deletedAt string
	if err := s.db.QueryRow(`SELECT status, deleted_at FROM tasks WHERE id = ?`, id).Scan(&status, &deletedAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		}
		return fmt.Errorf("read task deletion state: %w", err)
	}
	if deletedAt != "" {
		return ErrNotFound
	}
	return ErrActiveTask
}

type scanner interface {
	Scan(dest ...any) error
}

func scanTask(row scanner) (Task, error) {
	var found Task
	var status string
	var runner string
	var runControlsJSON string
	var scopeJSON string
	var createdAt string
	var updatedAt string

	err := row.Scan(&found.ID, &found.ProjectID, &found.Type, &found.Goal, &status, &runner, &found.RuntimeProfileID, &runControlsJSON, &scopeJSON, &createdAt, &updatedAt, &found.BlackboardProtocol)
	if errors.Is(err, sql.ErrNoRows) {
		return Task{}, ErrNotFound
	}
	if err != nil {
		return Task{}, err
	}
	found.Status = Status(status)
	found.Runner = Runner(runner)
	if err := json.Unmarshal([]byte(runControlsJSON), &found.RunControls); err != nil {
		return Task{}, fmt.Errorf("decode run controls: %w", err)
	}
	mode, err := normalizeBlackboardMode(found.RunControls.BlackboardMode)
	if err != nil {
		return Task{}, err
	}
	found.RunControls.BlackboardMode = mode
	found.BlackboardConclusion = BlackboardConclusion{Mode: mode, State: BlackboardConclusionStateClean}
	if err := json.Unmarshal([]byte(scopeJSON), &found.ScopeSnapshot); err != nil {
		return Task{}, fmt.Errorf("decode scope snapshot: %w", err)
	}
	if found.CreatedAt, err = time.Parse(time.RFC3339Nano, createdAt); err != nil {
		return Task{}, fmt.Errorf("parse created_at: %w", err)
	}
	if found.UpdatedAt, err = time.Parse(time.RFC3339Nano, updatedAt); err != nil {
		return Task{}, fmt.Errorf("parse updated_at: %w", err)
	}
	return found, nil
}
