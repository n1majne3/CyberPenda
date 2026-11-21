package session

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"pentest/internal/store"
)

type Service struct {
	db             *store.DB
	workdirRoot    string
	removeAll      func(string) error
	terminalMarker ContinuationTerminalMarker
	outputMu       sync.Mutex
	outputFlushes  map[string]*outputFlushRegistration
}

// ContinuationTerminalMarker closes capabilities bound to a terminal Session
// Continuation through the same lifecycle projection used by Tasks.
type ContinuationTerminalMarker interface {
	MarkContinuationTerminal(context.Context, string) error
}

// NewService returns a Session service whose Workdirs are created directly
// beneath workdirRoot. The root itself is not removed by Session deletion.
func NewService(db *store.DB, workdirRoot string) *Service {
	if strings.TrimSpace(workdirRoot) == "" {
		workdirRoot = "."
	}
	if absolute, err := filepath.Abs(workdirRoot); err == nil {
		workdirRoot = absolute
	}
	return &Service{db: db, workdirRoot: filepath.Clean(workdirRoot), removeAll: os.RemoveAll}
}

func (s *Service) SetContinuationTerminalMarker(marker ContinuationTerminalMarker) {
	s.terminalMarker = marker
}

// CleanupDeletedWorkdirs retries filesystem cleanup for Sessions whose
// logical deletion already committed. The quarantined directory name is the
// durable retry marker, so a daemon restart cannot strand an unrecoverable
// half-deleted Session.

// It is deterministic and never invokes a model.
func DeriveTitle(input string) string {
	input = strings.ReplaceAll(input, "\r\n", "\n")
	input = strings.ReplaceAll(input, "\r", "\n")
	for _, line := range strings.Split(input, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		return truncateTitle(line)
	}
	return ""
}

func truncateTitle(value string) string {
	if utf8.RuneCountInString(value) <= MaxTitleRunes {
		return value
	}
	runes := []rune(value)
	return string(runes[:MaxTitleRunes-1]) + "…"
}

// Create atomically persists the Session and its initial owner-local Events.
// Files are fully copied into the newly-created Workdir before the database
// transaction commits; any failed step removes the complete Workdir.
func (s *Service) Create(req CreateRequest) (Session, error) {
	if strings.TrimSpace(req.Input) == "" {
		return Session{}, ErrMissingInput
	}
	title := DeriveTitle(req.Input)
	if title == "" {
		return Session{}, ErrMissingInput
	}
	if len(req.Attachments) > MaxAttachmentCount {
		return Session{}, fmt.Errorf("%w: too many attachments (max %d)", ErrInvalidAttachment, MaxAttachmentCount)
	}
	mode, err := normalizeBlackboardMode(req.BlackboardMode)
	if err != nil {
		return Session{}, err
	}
	// Canonicalize new input only; stored snapshots retain their original mode.
	if mode == BlackboardModeInteractive {
		mode = BlackboardModeWorkingGraph
	}
	if req.InitialRuntime != nil {
		if req.InitialRuntime.Runner != RunnerSandbox && req.InitialRuntime.Runner != RunnerHost {
			return Session{}, ErrInvalidRunner
		}
		if strings.TrimSpace(req.InitialRuntime.RuntimeProvider) == "" {
			return Session{}, ErrMissingRuntimeProfile
		}
	}

	if err := s.ensureWorkdirRoot(); err != nil {
		return Session{}, fmt.Errorf("prepare Session data root: %w", err)
	}
	id, err := newID()
	if err != nil {
		return Session{}, fmt.Errorf("generate Session id: %w", err)
	}
	workdir, err := s.managedWorkdir(id)
	if err != nil {
		return Session{}, err
	}
	if err := os.Mkdir(workdir, 0o700); err != nil {
		return Session{}, fmt.Errorf("create Session Workdir: %w", err)
	}
	cleanupWorkdir := true
	defer func() {
		if cleanupWorkdir {
			_ = os.RemoveAll(workdir)
		}
	}()

	attachments, err := copyAttachments(workdir, req.Attachments)
	if err != nil {
		return Session{}, err
	}
	now := time.Now().UTC()
	created := Session{
		BlackboardProtocol: "fgs",
		ID:                 id, Title: title, Lifecycle: LifecycleOpen, Workdir: workdir,
		RunControls:          RunControls{BlackboardMode: mode},
		BlackboardConclusion: BlackboardConclusion{Mode: mode, State: BlackboardConclusionStateClean},
		CreatedAt:            now, UpdatedAt: now, LastActivityAt: now,
	}
	tx, err := s.db.Begin()
	if err != nil {
		return Session{}, fmt.Errorf("begin Session create: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.Exec(`INSERT INTO sessions (id,title,lifecycle,workdir,blackboard_mode,created_at,updated_at,last_activity_at,blackboard_protocol) VALUES (?,?,?,?,?,?,?,?,'fgs')`,
		created.ID, created.Title, string(created.Lifecycle), created.Workdir,
		string(created.RunControls.BlackboardMode),
		formatTime(created.CreatedAt), formatTime(created.UpdatedAt), formatTime(created.LastActivityAt)); err != nil {
		return Session{}, fmt.Errorf("store Session: %w", err)
	}
	var initialContinuation *Continuation
	if runtimeReq := req.InitialRuntime; runtimeReq != nil {
		encoded, err := json.Marshal(runtimeReq.RuntimeConfig)
		if err != nil {
			return Session{}, fmt.Errorf("encode initial Session Runtime config: %w", err)
		}
		configID := "session-config-" + strings.TrimPrefix(newIDMust(), "session-")
		if _, err := tx.Exec(`INSERT INTO session_runtime_config_versions (id,session_id,version,runtime_profile_id,config_json,created_at) VALUES (?,?,?,?,?,?)`, configID, created.ID, 1, runtimeReq.RuntimeProfileID, string(encoded), formatTime(now)); err != nil {
			return Session{}, fmt.Errorf("store initial Session Runtime config: %w", err)
		}
		continuation := Continuation{
			ID: "session-continuation-" + strings.TrimPrefix(newIDMust(), "session-"), SessionID: created.ID,
			Number: 1, RuntimeProfileID: runtimeReq.RuntimeProfileID, RuntimeProvider: runtimeReq.RuntimeProvider,
			Runner: runtimeReq.Runner, Status: RuntimeStatusPending, RuntimeConfigID: configID,
			StartedAt: now, UpdatedAt: now,
		}
		if _, err := tx.Exec(`INSERT INTO session_continuations (id,session_id,number,runtime_profile_id,runtime_provider,runner,status,container_id,native_session_id,native_session_path,runtime_config_version_id,started_at,updated_at,ended_at) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, continuation.ID, created.ID, continuation.Number, continuation.RuntimeProfileID, continuation.RuntimeProvider, string(continuation.Runner), string(continuation.Status), "", "", "", continuation.RuntimeConfigID, formatTime(now), formatTime(now), ""); err != nil {
			return Session{}, fmt.Errorf("store initial Session continuation: %w", err)
		}
		initialContinuation = &continuation
	}
	continuationID := ""
	if initialContinuation != nil {
		continuationID = initialContinuation.ID
	}
	if _, err := appendEventTx(tx, created.ID, EventKindConversation, payloadWithContinuation(EventPayload{
		"role": "user", "text": req.Input,
	}, continuationID), now); err != nil {
		return Session{}, fmt.Errorf("store initial Session Event: %w", err)
	}
	for _, attachment := range attachments {
		if _, err := appendEventTx(tx, created.ID, EventKindAttachment, payloadWithContinuation(EventPayload{
			"filename":      attachment.Filename,
			"relative_path": attachment.Filename,
			"size":          attachment.Size,
			"sha256":        attachment.SHA256,
		}, continuationID), now); err != nil {
			return Session{}, fmt.Errorf("store Session attachment Event: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return Session{}, fmt.Errorf("commit Session create: %w", err)
	}
	cleanupWorkdir = false
	created.ActiveContinuation = initialContinuation
	created.LatestContinuation = initialContinuation
	return created, nil
}

func (s *Service) Get(id string) (Session, error) {
	return scanSession(s.db.QueryRow(`SELECT id,title,lifecycle,workdir,blackboard_mode,created_at,updated_at,last_activity_at,blackboard_protocol FROM sessions WHERE id=?`, id))
}

// List returns Sessions for one lifecycle in most-recent-activity order.
func (s *Service) List(lifecycle Lifecycle) ([]Session, error) {
	return s.ListLimited(lifecycle, 0)
}

// ListLimited returns Sessions for one lifecycle in most-recent-activity
// order. A zero limit keeps the unbounded management-page behavior.
func (s *Service) ListLimited(lifecycle Lifecycle, limit int) ([]Session, error) {
	lifecycle, err := normalizeLifecycle(lifecycle)
	if err != nil {
		return nil, err
	}
	if limit < 0 {
		return nil, ErrInvalidLimit
	}
	query := `SELECT id,title,lifecycle,workdir,blackboard_mode,created_at,updated_at,last_activity_at,blackboard_protocol FROM sessions WHERE lifecycle=? ORDER BY last_activity_at DESC, created_at DESC, id ASC`
	args := []any{string(lifecycle)}
	if limit > 0 {
		query += ` LIMIT ?`
		args = append(args, limit)
	}
	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, fmt.Errorf("list Sessions: %w", err)
	}
	defer rows.Close()
	result := make([]Session, 0)
	for rows.Next() {
		found, err := scanSession(rows)
		if err != nil {
			return nil, fmt.Errorf("scan Session: %w", err)
		}
		result = append(result, found)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list Sessions: %w", err)
	}
	return result, nil
}

// Rename updates only the Session Title and records the owner-local lifecycle
// Event. Session identity and retained input are unchanged.

func (s *Service) ensureWorkdirRoot() error {
	if err := os.MkdirAll(s.workdirRoot, 0o700); err != nil {
		return err
	}
	return os.Chmod(s.workdirRoot, 0o700)
}

func (s *Service) managedWorkdir(id string) (string, error) {
	if strings.TrimSpace(id) == "" || strings.ContainsAny(id, `/\\`) || id == "." || id == ".." {
		return "", ErrInvalidWorkdir
	}
	root, err := filepath.Abs(s.workdirRoot)
	if err != nil {
		return "", fmt.Errorf("resolve Session data root: %w", err)
	}
	workdir := filepath.Join(root, id)
	relative, err := filepath.Rel(root, workdir)
	if err != nil || relative != id || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return "", ErrInvalidWorkdir
	}
	return workdir, nil
}

func normalizeLifecycle(value Lifecycle) (Lifecycle, error) {
	if value == "" {
		return LifecycleOpen, nil
	}
	if value != LifecycleOpen && value != LifecycleArchived {
		return "", ErrInvalidLifecycle
	}
	return value, nil
}

func normalizeBlackboardMode(value BlackboardMode) (BlackboardMode, error) {
	switch value {
	case "":
		return BlackboardModeDisabled, nil
	case BlackboardModeDisabled:
		return BlackboardModeDisabled, nil
	case BlackboardModeWorkingGraph:
		return BlackboardModeWorkingGraph, nil
	case BlackboardModeInteractive:
		return BlackboardModeInteractive, nil
	default:
		return "", ErrInvalidBlackboardMode
	}
}

type scanner interface{ Scan(dest ...any) error }

func scanSession(row scanner) (Session, error) {
	var found Session
	var lifecycle, mode, created, updated, activity string
	if err := row.Scan(&found.ID, &found.Title, &lifecycle, &found.Workdir, &mode, &created, &updated, &activity, &found.BlackboardProtocol); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Session{}, ErrNotFound
		}
		return Session{}, err
	}
	found.Lifecycle = Lifecycle(lifecycle)
	normalizedMode, err := normalizeBlackboardMode(BlackboardMode(mode))
	if err != nil {
		return Session{}, err
	}
	found.RunControls.BlackboardMode = normalizedMode
	found.BlackboardConclusion.Mode = found.RunControls.BlackboardMode
	found.BlackboardConclusion.State = BlackboardConclusionStateClean
	if found.CreatedAt, err = time.Parse(time.RFC3339Nano, created); err != nil {
		return Session{}, fmt.Errorf("parse Session created_at: %w", err)
	}
	if found.UpdatedAt, err = time.Parse(time.RFC3339Nano, updated); err != nil {
		return Session{}, fmt.Errorf("parse Session updated_at: %w", err)
	}
	if found.LastActivityAt, err = time.Parse(time.RFC3339Nano, activity); err != nil {
		return Session{}, fmt.Errorf("parse Session last_activity_at: %w", err)
	}
	return found, nil
}

func formatTime(value time.Time) string { return value.UTC().Format(time.RFC3339Nano) }

func newID() (string, error) {
	var bytes [16]byte
	if _, err := rand.Read(bytes[:]); err != nil {
		return "", err
	}
	return "session-" + hex.EncodeToString(bytes[:]), nil
}

func newIDMust() string {
	id, err := newID()
	if err != nil {
		return fmt.Sprintf("session-%d", time.Now().UnixNano())
	}
	return id
}
