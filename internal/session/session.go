// Package session owns the durable Non-Project Session aggregate. A Session is
// intentionally separate from Task: it has no Project identity, Scope, or
// Project artifacts, while its owner-local Events and managed Workdir remain
// durable for the Session's complete lifetime.
package session

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"

	"pentest/internal/owner"
	"pentest/internal/store"
)

// Lifecycle is the only durable Session state. Runtime liveness and turn
// failures are separate projections and never change this value implicitly.
type Lifecycle string

const (
	LifecycleOpen     Lifecycle = "open"
	LifecycleArchived Lifecycle = "archived"
)

// MaxTitleRunes bounds the operator-visible Session Title derived from the
// first non-empty input line.
const MaxTitleRunes = 80

// MaxAttachmentFileBytes and MaxAttachmentCount keep the domain safe even when
// callers other than the HTTP multipart adapter create a Session.
const (
	MaxAttachmentFileBytes int64 = 100 << 20
	MaxAttachmentCount           = 25
)

// EventKind classifies the owner-local Session timeline.
type EventKind string

const (
	EventKindConversation         EventKind = "conversation"
	EventKindAttachment           EventKind = "attachment"
	EventKindLifecycle            EventKind = "lifecycle"
	EventKindRuntimeOutput        EventKind = "runtime_output"
	EventKindSteering             EventKind = "steering"
	EventKindPermission           EventKind = "permission"
	EventKindTurn                 EventKind = "turn"
	EventKindBlackboardConclusion EventKind = "blackboard_conclusion"
)

// EventPayload is intentionally structured and compact. Raw files remain in
// the managed Workdir; an attachment Event stores only its safe reference and
// digest metadata.
type EventPayload map[string]any

// Event is one ordered, owner-local Session timeline entry.
type Event struct {
	ID        string       `json:"id"`
	SessionID string       `json:"session_id"`
	Seq       int          `json:"seq"`
	Kind      EventKind    `json:"kind"`
	Payload   EventPayload `json:"payload"`
	CreatedAt time.Time    `json:"created_at"`
}

// Runner identifies the execution boundary for a Session Runtime.
type Runner string

const (
	RunnerSandbox Runner = "sandbox"
	RunnerHost    Runner = "host"
)

// RuntimeStatus is the lifecycle of one Session-scoped Runtime Continuation.
// It is deliberately separate from Session Lifecycle and Runtime Activity.
type RuntimeStatus string

const (
	RuntimeStatusPending     RuntimeStatus = "pending"
	RuntimeStatusRunning     RuntimeStatus = "running"
	RuntimeStatusCompleted   RuntimeStatus = "completed"
	RuntimeStatusFailed      RuntimeStatus = "failed"
	RuntimeStatusStopped     RuntimeStatus = "stopped"
	RuntimeStatusInterrupted RuntimeStatus = "interrupted"
)

// RuntimeTurnSelection is the per-turn provider, model, and reasoning choice.
// It never mutates the selected Runtime Profile.
type RuntimeTurnSelection struct {
	ModelProviderID string `json:"model_provider_id,omitempty"`
	Model           string `json:"model,omitempty"`
	ReasoningEffort string `json:"reasoning_effort,omitempty"`
}

// RuntimeActivity reports current process/session health independently of
// whether a Session is open or archived.
type RuntimeActivity struct {
	Liveness     string `json:"liveness"`
	TurnActivity string `json:"turn_activity,omitempty"`
	Warning      string `json:"warning,omitempty"`
}

// RuntimeControls is the owner-local control projection exposed to the UI.
type RuntimeControls struct {
	NativeResumeAvailable   bool                  `json:"native_resume_available"`
	NativeSteerAvailable    bool                  `json:"native_steer_available"`
	NativeSteerMode         string                `json:"native_steer_mode,omitempty"`
	QueueSteerAvailable     bool                  `json:"queue_steer_available"`
	InterruptSteerAvailable bool                  `json:"interrupt_steer_available"`
	NativeSessionCaptured   bool                  `json:"native_session_captured"`
	RuntimeProvider         string                `json:"runtime_provider,omitempty"`
	TurnSelection           *RuntimeTurnSelection `json:"turn_selection,omitempty"`
	ProviderPermissions     []ProviderPermission  `json:"provider_permissions,omitempty"`
	RecoveryState           string                `json:"recovery_state,omitempty"`
	RecoveryReason          string                `json:"recovery_reason,omitempty"`
}

// RunControls are the owner-local launch controls that remain stable across
// Session Runtime Continuations. Sessions expose the same conclusion mode
// vocabulary as Tasks without acquiring a Project identity or Project scope.
type RunControls struct {
	BlackboardConclusionMode BlackboardConclusionMode `json:"blackboard_conclusion_mode"`
	// ContainerCLI is docker or podman for Session sandbox launches. Empty
	// means the daemon default (-container-cli / PENTEST_CONTAINER_CLI).
	ContainerCLI string `json:"container_cli,omitempty"`
	// SandboxNetwork mirrors Task sandbox network selection when non-empty.
	SandboxNetwork string `json:"sandbox_network,omitempty"`
	// SandboxVPNTun opts into /dev/net/tun + NET_ADMIN for OpenVPN in the container.
	SandboxVPNTun bool `json:"sandbox_vpn_tun,omitempty"`
}

// BlackboardConclusionMode selects whether the operator alone prompts the
// Runtime to persist conclusions or the Harness assists at work-Turn bounds.
type BlackboardConclusionMode string

const (
	BlackboardConclusionModeInteractive BlackboardConclusionMode = "interactive"
	BlackboardConclusionModeAssisted    BlackboardConclusionMode = "assisted"
)

// BlackboardConclusionState is the compact owner-local semantic-debt view.
type BlackboardConclusionState string

const (
	BlackboardConclusionStateClean          BlackboardConclusionState = "clean"
	BlackboardConclusionStatePending        BlackboardConclusionState = "pending"
	BlackboardConclusionStateConcluding     BlackboardConclusionState = "concluding"
	BlackboardConclusionStateActionRequired BlackboardConclusionState = "action_required"
)

type BlackboardConclusionErrorCode = owner.BlackboardConclusionErrorCode

// ConclusionValidationDetail is the bounded public reason for one rejected
// closed conclusion result, safe for repair directives and durable state.
type ConclusionValidationDetail = owner.ConclusionValidationDetail

const (
	BlackboardConclusionErrorInvalidResult           = owner.BlackboardConclusionErrorInvalidResult
	BlackboardConclusionErrorToolUseForbidden        = owner.BlackboardConclusionErrorToolUseForbidden
	BlackboardConclusionErrorRepairExhausted         = owner.BlackboardConclusionErrorRepairExhausted
	BlackboardConclusionErrorVersionConflict         = owner.BlackboardConclusionErrorVersionConflict
	BlackboardConclusionErrorRuntimeRecoveryRequired = owner.BlackboardConclusionErrorRuntimeRecoveryRequired
	BlackboardConclusionErrorWorkTurnNeverSettled    = owner.BlackboardConclusionErrorWorkTurnNeverSettled
	BlackboardConclusionAutomaticTurnLimit           = owner.BlackboardConclusionAutomaticTurnLimit
	BlackboardConclusionWorkTurnConflictLimit        = owner.BlackboardConclusionWorkTurnConflictLimit
)

type BlackboardConclusionReceiptState = owner.BlackboardConclusionReceiptState

const (
	BlackboardConclusionReceiptClean                                = owner.BlackboardConclusionReceiptClean
	BlackboardConclusionReceiptPending                              = owner.BlackboardConclusionReceiptPending
	BlackboardConclusionReceiptDispatchRequested                    = owner.BlackboardConclusionReceiptDispatchRequested
	BlackboardConclusionReceiptAwaitingResult                       = owner.BlackboardConclusionReceiptAwaitingResult
	BlackboardConclusionReceiptRepairDispatchRequested              = owner.BlackboardConclusionReceiptRepairDispatchRequested
	BlackboardConclusionReceiptVersionSyncRequested                 = owner.BlackboardConclusionReceiptVersionSyncRequested
	BlackboardConclusionReceiptVersionRegenerationDispatchRequested = owner.BlackboardConclusionReceiptVersionRegenerationDispatchRequested
	BlackboardConclusionReceiptActionRequired                       = owner.BlackboardConclusionReceiptActionRequired
	BlackboardConclusionReceiptValidated                            = owner.BlackboardConclusionReceiptValidated
	BlackboardConclusionReceiptApplied                              = owner.BlackboardConclusionReceiptApplied
)

// ConclusionDispatchKind is the immutable attempt category of one Conclusion
// Dispatch (ADR 0021). Recovery-created dispatches are kind recovery.
type ConclusionDispatchKind = owner.ConclusionDispatchKind

const (
	ConclusionDispatchKindInitial             = owner.ConclusionDispatchKindInitial
	ConclusionDispatchKindRepair              = owner.ConclusionDispatchKindRepair
	ConclusionDispatchKindVersionRegeneration = owner.ConclusionDispatchKindVersionRegeneration
	ConclusionDispatchKindRetry               = owner.ConclusionDispatchKindRetry
	ConclusionDispatchKindRecovery            = owner.ConclusionDispatchKindRecovery
)

// ConclusionDispatchState is the delivery lifecycle of one immutable dispatch.
type ConclusionDispatchState = owner.ConclusionDispatchState

const (
	ConclusionDispatchRequested      = owner.ConclusionDispatchRequested
	ConclusionDispatchAwaitingResult = owner.ConclusionDispatchAwaitingResult
	ConclusionDispatchValidated      = owner.ConclusionDispatchValidated
	ConclusionDispatchApplied        = owner.ConclusionDispatchApplied
	ConclusionDispatchActionRequired = owner.ConclusionDispatchActionRequired
	ConclusionDispatchSuperseded     = owner.ConclusionDispatchSuperseded
	ConclusionDispatchLateTerminal   = owner.ConclusionDispatchLateTerminal
)

// ConclusionRecoveryReason is the closed operator-visible fail-closed reason.
type ConclusionRecoveryReason = owner.ConclusionRecoveryReason

const (
	ConclusionRecoveryRuntimeOwnershipNotProven      = owner.ConclusionRecoveryRuntimeOwnershipNotProven
	ConclusionRecoveryWritableReplacementUnavailable = owner.ConclusionRecoveryWritableReplacementUnavailable
	ConclusionRecoveryAcceptanceAmbiguous            = owner.ConclusionRecoveryAcceptanceAmbiguous
	ConclusionRecoveryDispatchFailed                 = owner.ConclusionRecoveryDispatchFailed
	ConclusionRecoveryLegacyCorrelationUnproven      = owner.ConclusionRecoveryLegacyCorrelationUnproven
)

// BlackboardConclusion is the compact Session read view for the latest
// assisted Work Runtime Turn checkpoint and any conclusion progress it
// triggered. Result bytes and provider correlation remain private.
type BlackboardConclusion struct {
	Mode                         BlackboardConclusionMode      `json:"mode"`
	State                        BlackboardConclusionState     `json:"state"`
	SourceTurnID                 string                        `json:"source_turn_id,omitempty"`
	SourceWorkWatermark          int                           `json:"source_work_watermark"`
	SemanticPersistenceWatermark int                           `json:"semantic_persistence_watermark"`
	AppliedRevision              *int                          `json:"applied_revision,omitempty"`
	ErrorCode                    BlackboardConclusionErrorCode `json:"error_code,omitempty"`
	// ValidationReason, ValidationFieldPath, and ValidationExpected expose the
	// bounded public reason for the last rejected closed result. They are
	// closed tokens only; raw provider output never appears.
	ValidationReason    string `json:"validation_reason,omitempty"`
	ValidationFieldPath string `json:"validation_field_path,omitempty"`
	ValidationExpected  string `json:"validation_expected,omitempty"`
	// RecoveryReason is the closed operator-visible reason for a fail-closed
	// action_required obligation (ADR 0021).
	RecoveryReason string     `json:"recovery_reason,omitempty"`
	RetryAvailable bool       `json:"retry_available"`
	NextEligibleAt *time.Time `json:"next_eligible_at,omitempty"`
}

// ProviderPermission is a redacted provider approval request owned by one
// Session and one active continuation.
type ProviderPermission struct {
	RequestID           string    `json:"request_id"`
	PermissionRequestID string    `json:"permission_request_id"`
	SessionID           string    `json:"session_id,omitempty"`
	ProviderTurnID      string    `json:"provider_turn_id,omitempty"`
	Provider            string    `json:"provider,omitempty"`
	CreatedAt           time.Time `json:"created_at"`
}

// RuntimeConfigVersion is durable Session configuration history. Secrets are
// never stored here; callers persist only the resolved non-secret selection.
type RuntimeConfigVersion struct {
	ID               string         `json:"id"`
	SessionID        string         `json:"session_id"`
	Version          int            `json:"version"`
	RuntimeProfileID string         `json:"runtime_profile_id"`
	Config           map[string]any `json:"config"`
	CreatedAt        time.Time      `json:"created_at"`
}

// Continuation is the durable Runtime pin for one Session turn.
type Continuation struct {
	ID                string        `json:"id"`
	SessionID         string        `json:"session_id"`
	Number            int           `json:"number"`
	RuntimeProfileID  string        `json:"runtime_profile_id"`
	RuntimeProvider   string        `json:"runtime_provider"`
	Runner            Runner        `json:"runner"`
	Status            RuntimeStatus `json:"status"`
	ContainerID       string        `json:"container_id,omitempty"`
	NativeSessionID   string        `json:"native_session_id,omitempty"`
	NativeSessionPath string        `json:"native_session_path,omitempty"`
	RuntimeConfigID   string        `json:"runtime_config_version_id,omitempty"`
	StartedAt         time.Time     `json:"started_at"`
	UpdatedAt         time.Time     `json:"updated_at"`
	EndedAt           *time.Time    `json:"ended_at,omitempty"`
}

// CreateContinuationRequest is the durable part of a Session Runtime launch.
type CreateContinuationRequest struct {
	RuntimeProfileID  string
	RuntimeProvider   string
	Runner            Runner
	RuntimeConfig     map[string]any
	RuntimeConfigID   string
	ContainerID       string
	NativeSessionID   string
	NativeSessionPath string
}

// Session is the durable Non-Project Session aggregate. Workdir is kept out of
// the JSON representation because it is a server-local path, not operator
// state or a Project artifact reference.
type Session struct {
	ID                   string               `json:"id"`
	Title                string               `json:"title"`
	Lifecycle            Lifecycle            `json:"lifecycle"`
	Workdir              string               `json:"-"`
	RunControls          RunControls          `json:"run_controls"`
	BlackboardConclusion BlackboardConclusion `json:"blackboard_conclusion"`
	RuntimeActivity      RuntimeActivity      `json:"runtime_activity"`
	RuntimeControls      RuntimeControls      `json:"runtime_controls"`
	ActiveContinuation   *Continuation        `json:"active_continuation,omitempty"`
	LatestContinuation   *Continuation        `json:"latest_continuation,omitempty"`
	CreatedAt            time.Time            `json:"created_at"`
	UpdatedAt            time.Time            `json:"updated_at"`
	LastActivityAt       time.Time            `json:"last_activity_at"`
}

// OwnerContract returns the explicit Session capability contract consumed by
// later Runtime and Blackboard adapters. It cannot carry a Project identity.
func (s Session) OwnerContract() owner.Contract {
	return owner.NewSessionContract(s.ID, s.Workdir)
}

// Attachment is an operator-supplied file staged into a Session Workdir. Open
// is called exactly once during creation; the service owns closing the stream.
type Attachment struct {
	Name string
	Size int64
	Open func() (io.ReadCloser, error)
}

// ConversationInput is one operator message and its optional attachments.
// It can be committed with a new Continuation so recovery never exposes a
// Continuation without the input that initiated it.
type ConversationInput struct {
	Role        string
	Text        string
	Attachments []Attachment
}

// AttachmentReference is the typed, safe Workdir-relative projection stored by
// an attachment Event. Runtime adapters consume this instead of interpreting
// the Event payload map themselves.
type AttachmentReference struct {
	RelativePath   string
	ContinuationID string
}

// AttachmentReference returns the safe attachment projection represented by
// this Event. Invalid or legacy malformed payloads are not runtime-visible.
func (event Event) AttachmentReference() (AttachmentReference, bool) {
	if event.Kind != EventKindAttachment {
		return AttachmentReference{}, false
	}
	relativePath, _ := event.Payload["relative_path"].(string)
	clean := filepath.Clean(strings.TrimSpace(relativePath))
	if clean == "." || filepath.IsAbs(clean) || filepath.Base(clean) != clean {
		return AttachmentReference{}, false
	}
	continuationID, _ := event.Payload["continuation_id"].(string)
	return AttachmentReference{RelativePath: clean, ContinuationID: strings.TrimSpace(continuationID)}, true
}

// CreateRequest is the initial Session input and optional initial attachments.
type CreateRequest struct {
	Input                    string
	Attachments              []Attachment
	BlackboardConclusionMode BlackboardConclusionMode
}

var (
	// ErrNotFound deliberately has no owner details so cross-owner lookups do
	// not reveal whether another aggregate exists.
	ErrNotFound                        = errors.New("session not found")
	ErrMissingInput                    = errors.New("session initial input is required")
	ErrMissingTitle                    = errors.New("session title is required")
	ErrInvalidLifecycle                = errors.New("invalid session lifecycle")
	ErrInvalidLimit                    = errors.New("session list limit must not be negative")
	ErrAlreadyArchived                 = errors.New("session is already archived")
	ErrNotArchived                     = errors.New("session is not archived")
	ErrOpenSession                     = errors.New("open session cannot be deleted")
	ErrInvalidAttachment               = errors.New("invalid session attachment")
	ErrInvalidWorkdir                  = errors.New("session workdir is outside the managed root")
	ErrSessionNotOpen                  = errors.New("session is not open")
	ErrActiveContinuation              = errors.New("session already has an active continuation")
	ErrInvalidRunner                   = errors.New("runner must be sandbox or host")
	ErrContinuationNotFound            = errors.New("session continuation not found")
	ErrContinuationStatusConflict      = errors.New("session continuation status conflicts with its terminal state")
	ErrMissingRuntimeProfile           = errors.New("session runtime profile is required")
	ErrInvalidBlackboardConclusionMode = errors.New("invalid Session Blackboard conclusion mode")
)

// Service implements Session persistence and lifecycle rules against SQLite.
type Service struct {
	db             *store.DB
	workdirRoot    string
	removeAll      func(string) error
	terminalMarker ContinuationTerminalMarker
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
func (s *Service) CleanupDeletedWorkdirs() error {
	entries, err := os.ReadDir(s.workdirRoot)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("list Session cleanup markers: %w", err)
	}
	var cleanupErr error
	for _, entry := range entries {
		if !strings.HasPrefix(entry.Name(), ".deleting-") {
			continue
		}
		path := filepath.Join(s.workdirRoot, entry.Name())
		sessionID := strings.TrimPrefix(entry.Name(), ".deleting-")
		var storedWorkdir string
		err := s.db.QueryRow(`SELECT workdir FROM sessions WHERE id=?`, sessionID).Scan(&storedWorkdir)
		if err == nil {
			restorePath, pathErr := s.managedWorkdir(sessionID)
			if pathErr != nil || filepath.Clean(storedWorkdir) != filepath.Clean(restorePath) {
				cleanupErr = errors.Join(cleanupErr, ErrInvalidWorkdir)
				continue
			}
			if _, statErr := os.Stat(restorePath); statErr == nil {
				cleanupErr = errors.Join(cleanupErr, fmt.Errorf("restore %s: target already exists", entry.Name()))
				continue
			} else if !errors.Is(statErr, os.ErrNotExist) {
				cleanupErr = errors.Join(cleanupErr, fmt.Errorf("inspect restore target %s: %w", entry.Name(), statErr))
				continue
			}
			if renameErr := os.Rename(path, restorePath); renameErr != nil {
				cleanupErr = errors.Join(cleanupErr, fmt.Errorf("restore %s: %w", entry.Name(), renameErr))
			}
			continue
		}
		if !errors.Is(err, sql.ErrNoRows) {
			cleanupErr = errors.Join(cleanupErr, fmt.Errorf("resolve %s: %w", entry.Name(), err))
			continue
		}
		if err := s.removeAll(path); err != nil {
			cleanupErr = errors.Join(cleanupErr, fmt.Errorf("remove %s: %w", entry.Name(), err))
		}
	}
	return cleanupErr
}

// DeriveTitle returns the bounded title from the first non-empty input line.
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
	mode, err := normalizeBlackboardConclusionMode(req.BlackboardConclusionMode)
	if err != nil {
		return Session{}, err
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
		ID: id, Title: title, Lifecycle: LifecycleOpen, Workdir: workdir,
		RunControls:          RunControls{BlackboardConclusionMode: mode},
		BlackboardConclusion: BlackboardConclusion{Mode: mode, State: BlackboardConclusionStateClean},
		CreatedAt:            now, UpdatedAt: now, LastActivityAt: now,
	}
	tx, err := s.db.Begin()
	if err != nil {
		return Session{}, fmt.Errorf("begin Session create: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.Exec(`INSERT INTO sessions (id,title,lifecycle,workdir,blackboard_conclusion_mode,created_at,updated_at,last_activity_at) VALUES (?,?,?,?,?,?,?,?)`,
		created.ID, created.Title, string(created.Lifecycle), created.Workdir,
		string(created.RunControls.BlackboardConclusionMode),
		formatTime(created.CreatedAt), formatTime(created.UpdatedAt), formatTime(created.LastActivityAt)); err != nil {
		return Session{}, fmt.Errorf("store Session: %w", err)
	}
	if _, err := appendEventTx(tx, created.ID, EventKindConversation, EventPayload{
		"role": "user", "text": req.Input,
	}, now); err != nil {
		return Session{}, fmt.Errorf("store initial Session Event: %w", err)
	}
	for _, attachment := range attachments {
		if _, err := appendEventTx(tx, created.ID, EventKindAttachment, EventPayload{
			"filename":      attachment.Filename,
			"relative_path": attachment.Filename,
			"size":          attachment.Size,
			"sha256":        attachment.SHA256,
		}, now); err != nil {
			return Session{}, fmt.Errorf("store Session attachment Event: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return Session{}, fmt.Errorf("commit Session create: %w", err)
	}
	cleanupWorkdir = false
	return created, nil
}

// Get loads one Session by its own durable identity.
func (s *Service) Get(id string) (Session, error) {
	return scanSession(s.db.QueryRow(`SELECT id,title,lifecycle,workdir,blackboard_conclusion_mode,created_at,updated_at,last_activity_at FROM sessions WHERE id=?`, id))
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
	query := `SELECT id,title,lifecycle,workdir,blackboard_conclusion_mode,created_at,updated_at,last_activity_at FROM sessions WHERE lifecycle=? ORDER BY last_activity_at DESC, created_at DESC, id ASC`
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
func (s *Service) Rename(id, title string) (Session, error) {
	title = truncateTitle(strings.TrimSpace(title))
	if title == "" {
		return Session{}, ErrMissingTitle
	}
	found, err := s.Get(id)
	if err != nil {
		return Session{}, err
	}
	now := time.Now().UTC()
	tx, err := s.db.Begin()
	if err != nil {
		return Session{}, fmt.Errorf("begin Session rename: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.Exec(`UPDATE sessions SET title=?,updated_at=?,last_activity_at=? WHERE id=?`, title, formatTime(now), formatTime(now), id); err != nil {
		return Session{}, fmt.Errorf("rename Session: %w", err)
	}
	if _, err := appendEventTx(tx, id, EventKindLifecycle, EventPayload{"phase": "renamed", "title": title}, now); err != nil {
		return Session{}, fmt.Errorf("store Session rename Event: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return Session{}, fmt.Errorf("commit Session rename: %w", err)
	}
	found.Title, found.UpdatedAt, found.LastActivityAt = title, now, now
	return found, nil
}

// Archive closes an open Session's durable lifecycle while retaining all
// Session-owned Events and its Workdir.
func (s *Service) Archive(id string) (Session, error) {
	return s.transition(id, LifecycleOpen, LifecycleArchived, ErrAlreadyArchived, "archived")
}

// Restore reopens an archived Session with the same identity and state.
func (s *Service) Restore(id string) (Session, error) {
	return s.transition(id, LifecycleArchived, LifecycleOpen, ErrNotArchived, "restored")
}

func (s *Service) transition(id string, from, to Lifecycle, wrongState error, phase string) (Session, error) {
	found, err := s.Get(id)
	if err != nil {
		return Session{}, err
	}
	if found.Lifecycle != from {
		return Session{}, wrongState
	}
	now := time.Now().UTC()
	tx, err := s.db.Begin()
	if err != nil {
		return Session{}, fmt.Errorf("begin Session transition: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	result, err := tx.Exec(`UPDATE sessions SET lifecycle=?,updated_at=?,last_activity_at=? WHERE id=? AND lifecycle=?`, string(to), formatTime(now), formatTime(now), id, string(from))
	if err != nil {
		return Session{}, fmt.Errorf("update Session lifecycle: %w", err)
	}
	if affected, err := result.RowsAffected(); err != nil || affected != 1 {
		if err != nil {
			return Session{}, fmt.Errorf("read Session lifecycle update: %w", err)
		}
		return Session{}, wrongState
	}
	if _, err := appendEventTx(tx, id, EventKindLifecycle, EventPayload{"phase": phase}, now); err != nil {
		return Session{}, fmt.Errorf("store Session lifecycle Event: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return Session{}, fmt.Errorf("commit Session transition: %w", err)
	}
	found.Lifecycle, found.UpdatedAt, found.LastActivityAt = to, now, now
	return found, nil
}

// Delete permanently removes an archived Session, its Events, and Workdir.
// The Workdir is first quarantined under the managed root so a failed database
// transaction can restore it without ever touching an arbitrary path.
func (s *Service) Delete(id string) error {
	found, err := s.Get(id)
	if err != nil {
		return err
	}
	if found.Lifecycle == LifecycleOpen {
		return ErrOpenSession
	}
	if found.Lifecycle != LifecycleArchived {
		return ErrInvalidLifecycle
	}
	if active, err := s.ActiveContinuation(id); err != nil {
		return fmt.Errorf("check Session Runtime before delete: %w", err)
	} else if active != nil {
		return ErrActiveContinuation
	}
	workdir, err := s.managedWorkdir(id)
	if err != nil {
		return err
	}
	if filepath.Clean(found.Workdir) != filepath.Clean(workdir) {
		return ErrInvalidWorkdir
	}
	quarantine := filepath.Join(s.workdirRoot, ".deleting-"+id)
	if _, err := os.Stat(quarantine); err == nil {
		return fmt.Errorf("delete Session: cleanup already in progress")
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("inspect Session cleanup path: %w", err)
	}
	quarantined := false
	if _, err := os.Stat(workdir); err == nil {
		if err := os.Rename(workdir, quarantine); err != nil {
			return fmt.Errorf("quarantine Session Workdir: %w", err)
		}
		quarantined = true
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("inspect Session Workdir: %w", err)
	}
	restore := func() {
		if quarantined {
			_ = os.Rename(quarantine, workdir)
		}
	}
	tx, err := s.db.Begin()
	if err != nil {
		restore()
		return fmt.Errorf("begin Session delete: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.Exec(`DELETE FROM session_events WHERE session_id=?`, id); err != nil {
		restore()
		return fmt.Errorf("delete Session Events: %w", err)
	}
	if _, err := tx.Exec(`DELETE FROM session_continuations WHERE session_id=?`, id); err != nil {
		restore()
		return fmt.Errorf("delete Session continuations: %w", err)
	}
	if _, err := tx.Exec(`DELETE FROM session_runtime_config_versions WHERE session_id=?`, id); err != nil {
		restore()
		return fmt.Errorf("delete Session Runtime configs: %w", err)
	}
	result, err := tx.Exec(`DELETE FROM sessions WHERE id=? AND lifecycle=?`, id, string(LifecycleArchived))
	if err != nil {
		restore()
		return fmt.Errorf("delete Session: %w", err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		restore()
		return fmt.Errorf("read Session deletion: %w", err)
	}
	if affected != 1 {
		restore()
		return ErrNotFound
	}
	if err := tx.Commit(); err != nil {
		restore()
		return fmt.Errorf("commit Session delete: %w", err)
	}
	if quarantined {
		// The database commit is the logical deletion boundary. A filesystem
		// failure leaves the quarantine path as a durable retry marker and must
		// not turn the already-committed operation into a false API failure.
		_ = s.removeAll(quarantine)
	}
	return nil
}

// Events returns the ordered owner-local Session timeline.
func (s *Service) Events(id string) ([]Event, error) {
	if _, err := s.Get(id); err != nil {
		return nil, err
	}
	rows, err := s.db.Query(`SELECT id,session_id,seq,kind,payload_json,created_at FROM session_events WHERE session_id=? ORDER BY seq ASC`, id)
	if err != nil {
		return nil, fmt.Errorf("list Session Events: %w", err)
	}
	defer rows.Close()
	return scanEvents(rows)
}

// EventProjection selects the Session Event kinds needed by one Runtime Owner
// history projection.
type EventProjection string

const (
	EventProjectionTimeline   EventProjection = "timeline"
	EventProjectionTranscript EventProjection = "transcript"
)

type EventWindowQuery struct {
	Projection EventProjection
	BeforeSet  bool
	Before     int
	AfterSet   bool
	After      int
	Limit      int
}

type EventWindow struct {
	Events                 []Event
	Cursor                 int
	HasOlder               bool
	HasNewer               bool
	ScanCursor             int
	PriorContinuation      int
	PriorTranscriptAdapter string
}

// HistoryEventWindow reads a fixed-size keyset window independently of the
// Session's full Event history.
func (s *Service) HistoryEventWindow(id string, query EventWindowQuery) (EventWindow, error) {
	if _, err := s.Get(id); err != nil {
		return EventWindow{}, err
	}
	if query.Limit < 1 || query.Before < 0 || query.After < 0 || (query.BeforeSet && query.AfterSet) {
		return EventWindow{}, fmt.Errorf("invalid Session Event window query")
	}
	kinds := "('runtime_output','lifecycle','steering','attachment','blackboard_conclusion')"
	if query.Projection == EventProjectionTranscript {
		kinds = "('conversation','runtime_output','lifecycle','steering')"
	} else if query.Projection != EventProjectionTimeline {
		return EventWindow{}, fmt.Errorf("invalid Session Event projection")
	}
	var cursor int
	if err := s.db.QueryRow(`SELECT COALESCE(MAX(seq),0) FROM session_events WHERE session_id=? AND kind IN `+kinds, id).Scan(&cursor); err != nil {
		return EventWindow{}, fmt.Errorf("read Session Event projection cursor: %w", err)
	}
	order := "DESC"
	predicate := ""
	args := []any{id}
	if query.BeforeSet {
		predicate = " AND seq < ?"
		args = append(args, query.Before)
	} else if query.AfterSet {
		predicate = " AND seq > ?"
		args = append(args, query.After)
		order = "ASC"
	}
	args = append(args, query.Limit+1)
	rows, err := s.db.Query(
		`SELECT id,session_id,seq,kind,payload_json,created_at FROM session_events
		 WHERE session_id=? AND kind IN `+kinds+predicate+` ORDER BY seq `+order+` LIMIT ?`, args...,
	)
	if err != nil {
		return EventWindow{}, fmt.Errorf("list Session Event window: %w", err)
	}
	defer rows.Close()
	events, err := scanEvents(rows)
	if err != nil {
		return EventWindow{}, err
	}
	hasOlder := false
	if order == "DESC" {
		if len(events) > query.Limit {
			hasOlder = true
			events = events[:query.Limit]
		}
		for left, right := 0, len(events)-1; left < right; left, right = left+1, right-1 {
			events[left], events[right] = events[right], events[left]
		}
	} else if len(events) > query.Limit {
		events = events[:query.Limit]
	}
	hasNewer := order == "ASC" && len(events) == query.Limit && len(events) > 0 && events[len(events)-1].Seq < cursor
	scanCursor := query.After
	if len(events) > 0 {
		scanCursor = events[len(events)-1].Seq
	}
	window := EventWindow{Events: events, Cursor: cursor, HasOlder: hasOlder, HasNewer: hasNewer, ScanCursor: scanCursor}
	if query.Projection == EventProjectionTranscript && len(events) > 0 {
		if err := s.readTranscriptContextBefore(id, events[0].Seq, &window); err != nil {
			return EventWindow{}, err
		}
	}
	return window, nil
}

func (s *Service) readTranscriptContextBefore(sessionID string, seq int, window *EventWindow) error {
	var continuationID string
	err := s.db.QueryRow(`
		SELECT COALESCE(json_extract(payload_json,'$.continuation_id'),''),COALESCE(json_extract(payload_json,'$.adapter'),'') FROM session_events
		WHERE session_id=? AND seq<? AND kind='lifecycle'
		  AND json_extract(payload_json,'$.phase')='started'
		ORDER BY seq DESC LIMIT 1`, sessionID, seq,
	).Scan(&continuationID, &window.PriorTranscriptAdapter)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("read Session Transcript context: %w", err)
	}
	if continuationID != "" {
		var provider string
		err = s.db.QueryRow(`SELECT number,runtime_provider FROM session_continuations WHERE id=? AND session_id=?`, continuationID, sessionID).
			Scan(&window.PriorContinuation, &provider)
		if err == nil {
			if provider != "" {
				window.PriorTranscriptAdapter = provider
			}
			return nil
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("read Session Transcript Continuation pin: %w", err)
		}
	}
	// Legacy lifecycle Events can predate durable Continuation pins. Preserve
	// their historical numbering with the old count only on that fallback path.
	if err := s.db.QueryRow(`
		SELECT COUNT(*) FROM session_events
		WHERE session_id=? AND seq<? AND kind='lifecycle'
		  AND json_extract(payload_json,'$.phase')='started'`, sessionID, seq,
	).Scan(&window.PriorContinuation); err != nil {
		return fmt.Errorf("read legacy Session Transcript Continuation context: %w", err)
	}
	return nil
}

func scanEvents(rows *sql.Rows) ([]Event, error) {
	events := make([]Event, 0)
	for rows.Next() {
		var event Event
		var kind, payload, created string
		if err := rows.Scan(&event.ID, &event.SessionID, &event.Seq, &kind, &payload, &created); err != nil {
			return nil, fmt.Errorf("scan Session Event: %w", err)
		}
		event.Kind = EventKind(kind)
		if err := json.Unmarshal([]byte(payload), &event.Payload); err != nil {
			return nil, fmt.Errorf("decode Session Event: %w", err)
		}
		var err error
		if event.CreatedAt, err = time.Parse(time.RFC3339Nano, created); err != nil {
			return nil, fmt.Errorf("parse Session Event time: %w", err)
		}
		events = append(events, event)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list Session Events: %w", err)
	}
	return events, nil
}

// AppendEvent records one owner-local timeline event and advances Session
// activity. Conversation events are deliberately stored in the same durable
// stream but are projected separately by Conversation and Timeline.
func (s *Service) AppendEvent(id string, kind EventKind, payload EventPayload) (Event, error) {
	found, err := s.Get(id)
	if err != nil {
		return Event{}, err
	}
	if found.Lifecycle != LifecycleOpen {
		return Event{}, ErrSessionNotOpen
	}
	if continuationID, _ := payload["continuation_id"].(string); strings.TrimSpace(continuationID) != "" {
		continuation, continuationErr := s.Continuation(continuationID)
		if continuationErr != nil || continuation.SessionID != id {
			return Event{}, ErrContinuationNotFound
		}
	}
	now := time.Now().UTC()
	tx, err := s.db.Begin()
	if err != nil {
		return Event{}, fmt.Errorf("begin Session Event: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	event, err := appendEventTx(tx, id, kind, payload, now)
	if err != nil {
		return Event{}, fmt.Errorf("store Session Event: %w", err)
	}
	if _, err := tx.Exec(`UPDATE sessions SET updated_at=?,last_activity_at=? WHERE id=?`, formatTime(now), formatTime(now), id); err != nil {
		return Event{}, fmt.Errorf("update Session activity: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return Event{}, fmt.Errorf("commit Session Event: %w", err)
	}
	return event, nil
}

// AppendEventTx appends a structured session event inside a caller-owned
// transaction so it can be committed atomically with another owner-neutral
// record (for example a durable Accepted Steering request).
func (s *Service) AppendEventTx(tx *sql.Tx, sessionID string, kind EventKind, payload EventPayload) (Event, error) {
	return appendEventTx(tx, sessionID, kind, payload, time.Now().UTC())
}

// AppendConversationEvent stores one user/runtime conversation entry. It is
// the only write path for transcript content, preventing synthetic timeline
// message duplication.
func (s *Service) AppendConversationEvent(id, continuationID, role, text string) (Event, error) {
	role = strings.TrimSpace(role)
	if role == "" || strings.TrimSpace(text) == "" {
		return Event{}, ErrMissingInput
	}
	payload := EventPayload{"role": role, "text": text}
	if continuationID != "" {
		continuation, err := s.Continuation(continuationID)
		if err != nil || continuation.SessionID != id {
			if err != nil {
				return Event{}, err
			}
			return Event{}, ErrContinuationNotFound
		}
	}
	return s.AppendEvent(id, EventKindConversation, payloadWithContinuation(payload, continuationID))
}

// AddAttachments stages additional operator files into the existing Session
// Workdir and records only safe references in the Session event stream.
// Existing files are never overwritten; attachment names are made unique in
// the same way as initial Session attachments.
func (s *Service) AddAttachments(id, continuationID string, input []Attachment) ([]Event, error) {
	events, _, err := s.appendConversationInput(id, continuationID, input, "", "")
	return events, err
}

// AppendConversationInput atomically records one operator message and all of
// its attachments. Failed persistence removes newly copied files, so callers
// never observe an attachment without the input that introduced it.
func (s *Service) AppendConversationInput(id, continuationID, role, text string, input []Attachment) ([]Event, Event, error) {
	role = strings.TrimSpace(role)
	text = strings.TrimSpace(text)
	if role == "" || text == "" {
		return nil, Event{}, ErrMissingInput
	}
	return s.appendConversationInput(id, continuationID, input, role, text)
}

func (s *Service) appendConversationInput(id, continuationID string, input []Attachment, role, text string) ([]Event, Event, error) {
	found, err := s.Get(id)
	if err != nil {
		return nil, Event{}, err
	}
	if found.Lifecycle != LifecycleOpen {
		return nil, Event{}, ErrSessionNotOpen
	}
	if len(input) == 0 && role == "" {
		return nil, Event{}, nil
	}
	if len(input) > MaxAttachmentCount {
		return nil, Event{}, fmt.Errorf("%w: too many attachments (max %d)", ErrInvalidAttachment, MaxAttachmentCount)
	}
	workdir, err := s.managedWorkdir(id)
	if err != nil {
		return nil, Event{}, err
	}
	if filepath.Clean(found.Workdir) != filepath.Clean(workdir) {
		return nil, Event{}, ErrInvalidWorkdir
	}
	if continuationID != "" {
		continuation, continuationErr := s.Continuation(continuationID)
		if continuationErr != nil {
			return nil, Event{}, continuationErr
		}
		if continuation.SessionID != id {
			return nil, Event{}, ErrContinuationNotFound
		}
	}
	copied, err := copyAttachments(workdir, input)
	if err != nil {
		return nil, Event{}, err
	}
	cleanup := true
	defer func() {
		if !cleanup {
			return
		}
		for _, attachment := range copied {
			_ = os.Remove(filepath.Join(workdir, attachment.Filename))
		}
	}()

	now := time.Now().UTC()
	tx, err := s.db.Begin()
	if err != nil {
		return nil, Event{}, fmt.Errorf("begin Session conversation input: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	var lifecycle string
	if err := tx.QueryRow(`SELECT lifecycle FROM sessions WHERE id=?`, id).Scan(&lifecycle); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, Event{}, ErrNotFound
		}
		return nil, Event{}, fmt.Errorf("read Session lifecycle: %w", err)
	}
	if Lifecycle(lifecycle) != LifecycleOpen {
		return nil, Event{}, ErrSessionNotOpen
	}
	events := make([]Event, 0, len(copied))
	for _, attachment := range copied {
		event, err := appendEventTx(tx, id, EventKindAttachment, payloadWithContinuation(EventPayload{
			"filename": attachment.Filename, "relative_path": attachment.Filename,
			"size": attachment.Size, "sha256": attachment.SHA256,
		}, continuationID), now)
		if err != nil {
			return nil, Event{}, fmt.Errorf("store Session attachment Event: %w", err)
		}
		events = append(events, event)
	}
	var conversation Event
	if role != "" {
		conversation, err = appendEventTx(tx, id, EventKindConversation, payloadWithContinuation(EventPayload{
			"role": role, "text": text,
		}, continuationID), now)
		if err != nil {
			return nil, Event{}, fmt.Errorf("store Session conversation Event: %w", err)
		}
	}
	if _, err := tx.Exec(`UPDATE sessions SET updated_at=?,last_activity_at=? WHERE id=?`, formatTime(now), formatTime(now), id); err != nil {
		return nil, Event{}, fmt.Errorf("update Session activity: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return nil, Event{}, fmt.Errorf("commit Session conversation input: %w", err)
	}
	cleanup = false
	return events, conversation, nil
}

func payloadWithContinuation(payload EventPayload, continuationID string) EventPayload {
	if continuationID != "" {
		payload["continuation_id"] = continuationID
	}
	return payload
}

// Conversation returns only user/runtime conversation entries.
func (s *Service) Conversation(id string) ([]Event, error) {
	events, err := s.Events(id)
	if err != nil {
		return nil, err
	}
	conversation := make([]Event, 0, len(events))
	for _, event := range events {
		if event.Kind == EventKindConversation {
			conversation = append(conversation, event)
		}
	}
	return conversation, nil
}

// Timeline returns startup, runtime, turn, permission, attachment, and
// lifecycle markers without synthetic conversation messages.
func (s *Service) Timeline(id string) ([]Event, error) {
	events, err := s.Events(id)
	if err != nil {
		return nil, err
	}
	timeline := make([]Event, 0, len(events))
	for _, event := range events {
		if event.Kind != EventKindConversation {
			timeline = append(timeline, event)
		}
	}
	return timeline, nil
}

// RecordRuntimeConfig stores the non-secret Runtime Turn Selection history for
// a Session. Each explicit configuration change creates a new version.
func (s *Service) RecordRuntimeConfig(sessionID, runtimeProfileID string, config map[string]any) (RuntimeConfigVersion, error) {
	if _, err := s.Get(sessionID); err != nil {
		return RuntimeConfigVersion{}, err
	}
	if strings.TrimSpace(runtimeProfileID) == "" {
		return RuntimeConfigVersion{}, ErrMissingRuntimeProfile
	}
	encoded, err := json.Marshal(config)
	if err != nil {
		return RuntimeConfigVersion{}, fmt.Errorf("encode Session Runtime config: %w", err)
	}
	now := time.Now().UTC()
	version := RuntimeConfigVersion{ID: "session-config-" + strings.TrimPrefix(newIDMust(), "session-"), SessionID: sessionID, RuntimeProfileID: runtimeProfileID, Config: config, CreatedAt: now}
	tx, err := s.db.Begin()
	if err != nil {
		return RuntimeConfigVersion{}, fmt.Errorf("begin Session Runtime config: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	var max sql.NullInt64
	if err := tx.QueryRow(`SELECT MAX(version) FROM session_runtime_config_versions WHERE session_id=?`, sessionID).Scan(&max); err != nil {
		return RuntimeConfigVersion{}, fmt.Errorf("read Session Runtime config version: %w", err)
	}
	version.Version = int(max.Int64) + 1
	if _, err := tx.Exec(`INSERT INTO session_runtime_config_versions (id,session_id,version,runtime_profile_id,config_json,created_at) VALUES (?,?,?,?,?,?)`, version.ID, sessionID, version.Version, runtimeProfileID, string(encoded), formatTime(now)); err != nil {
		return RuntimeConfigVersion{}, fmt.Errorf("store Session Runtime config: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return RuntimeConfigVersion{}, fmt.Errorf("commit Session Runtime config: %w", err)
	}
	return version, nil
}

// CreateContinuation persists one Session-scoped Runtime pin. It does not
// start a provider; the daemon opens the provider only after this succeeds.
// The optional config keeps the simple Task-equivalent call shape while
// allowing launch callers to persist a resolved selection.
func (s *Service) CreateContinuation(sessionID, runtimeProfileID, runtimeProvider string, runner Runner, configs ...map[string]any) (Continuation, error) {
	req := CreateContinuationRequest{RuntimeProfileID: runtimeProfileID, RuntimeProvider: runtimeProvider, Runner: runner}
	if len(configs) > 0 {
		req.RuntimeConfig = configs[0]
	}
	return s.createContinuation(sessionID, req, false, nil)
}

// CreateContinuationWithInput atomically persists a new Continuation and the
// operator input that initiates it.
func (s *Service) CreateContinuationWithInput(sessionID, runtimeProfileID, runtimeProvider string, runner Runner, config map[string]any, input ConversationInput) (Continuation, error) {
	return s.createContinuation(sessionID, CreateContinuationRequest{
		RuntimeProfileID: runtimeProfileID, RuntimeProvider: runtimeProvider, Runner: runner, RuntimeConfig: config,
	}, false, &input)
}

func (s *Service) createContinuation(sessionID string, req CreateContinuationRequest, allowActive bool, input *ConversationInput) (Continuation, error) {
	if req.Runner != RunnerSandbox && req.Runner != RunnerHost {
		return Continuation{}, ErrInvalidRunner
	}
	if strings.TrimSpace(req.RuntimeProfileID) == "" || strings.TrimSpace(req.RuntimeProvider) == "" {
		return Continuation{}, ErrMissingRuntimeProfile
	}
	var copied []copiedAttachment
	cleanupCopied := false
	if input != nil {
		input.Role = strings.TrimSpace(input.Role)
		input.Text = strings.TrimSpace(input.Text)
		if input.Role == "" || input.Text == "" {
			return Continuation{}, ErrMissingInput
		}
		if len(input.Attachments) > MaxAttachmentCount {
			return Continuation{}, fmt.Errorf("%w: too many attachments (max %d)", ErrInvalidAttachment, MaxAttachmentCount)
		}
		found, err := s.Get(sessionID)
		if err != nil {
			return Continuation{}, err
		}
		if found.Lifecycle != LifecycleOpen {
			return Continuation{}, ErrSessionNotOpen
		}
		workdir, err := s.managedWorkdir(sessionID)
		if err != nil {
			return Continuation{}, err
		}
		if filepath.Clean(found.Workdir) != filepath.Clean(workdir) {
			return Continuation{}, ErrInvalidWorkdir
		}
		copied, err = copyAttachments(workdir, input.Attachments)
		if err != nil {
			return Continuation{}, err
		}
		cleanupCopied = true
		defer func() {
			if !cleanupCopied {
				return
			}
			for _, attachment := range copied {
				_ = os.Remove(filepath.Join(workdir, attachment.Filename))
			}
		}()
	}
	encoded, err := json.Marshal(req.RuntimeConfig)
	if err != nil {
		return Continuation{}, fmt.Errorf("encode Session launch Runtime config: %w", err)
	}
	now := time.Now().UTC()
	tx, err := s.db.Begin()
	if err != nil {
		return Continuation{}, fmt.Errorf("begin Session continuation: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	var lifecycle string
	if err := tx.QueryRow(`SELECT lifecycle FROM sessions WHERE id=?`, sessionID).Scan(&lifecycle); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Continuation{}, ErrNotFound
		}
		return Continuation{}, fmt.Errorf("read Session lifecycle: %w", err)
	}
	if Lifecycle(lifecycle) != LifecycleOpen {
		return Continuation{}, ErrSessionNotOpen
	}
	if !allowActive {
		var active int
		if err := tx.QueryRow(`SELECT COUNT(*) FROM session_continuations WHERE session_id=? AND status IN (?,?)`, sessionID, string(RuntimeStatusPending), string(RuntimeStatusRunning)).Scan(&active); err != nil {
			return Continuation{}, fmt.Errorf("check active Session continuation: %w", err)
		}
		if active != 0 {
			return Continuation{}, ErrActiveContinuation
		}
	}
	configID := strings.TrimSpace(req.RuntimeConfigID)
	if configID == "" {
		var latestID, latestProfile, latestJSON string
		latestErr := tx.QueryRow(`SELECT id,runtime_profile_id,config_json FROM session_runtime_config_versions WHERE session_id=? ORDER BY version DESC LIMIT 1`, sessionID).Scan(&latestID, &latestProfile, &latestJSON)
		if latestErr == nil && latestProfile == req.RuntimeProfileID && latestJSON == string(encoded) {
			configID = latestID
		} else if latestErr != nil && !errors.Is(latestErr, sql.ErrNoRows) {
			return Continuation{}, fmt.Errorf("read Session launch config: %w", latestErr)
		} else {
			configID = "session-config-" + strings.TrimPrefix(newIDMust(), "session-")
			var maxConfig sql.NullInt64
			if err := tx.QueryRow(`SELECT MAX(version) FROM session_runtime_config_versions WHERE session_id=?`, sessionID).Scan(&maxConfig); err != nil {
				return Continuation{}, fmt.Errorf("read Session launch config version: %w", err)
			}
			if _, err := tx.Exec(`INSERT INTO session_runtime_config_versions (id,session_id,version,runtime_profile_id,config_json,created_at) VALUES (?,?,?,?,?,?)`, configID, sessionID, int(maxConfig.Int64)+1, req.RuntimeProfileID, string(encoded), formatTime(now)); err != nil {
				return Continuation{}, fmt.Errorf("store Session launch config: %w", err)
			}
		}
	}
	var maxNumber sql.NullInt64
	if err := tx.QueryRow(`SELECT MAX(number) FROM session_continuations WHERE session_id=?`, sessionID).Scan(&maxNumber); err != nil {
		return Continuation{}, fmt.Errorf("read Session continuation number: %w", err)
	}
	continuation := Continuation{
		ID: "session-continuation-" + strings.TrimPrefix(newIDMust(), "session-"), SessionID: sessionID,
		Number: int(maxNumber.Int64) + 1, RuntimeProfileID: req.RuntimeProfileID, RuntimeProvider: req.RuntimeProvider,
		Runner: req.Runner, Status: RuntimeStatusPending, RuntimeConfigID: configID, StartedAt: now, UpdatedAt: now,
		ContainerID: strings.TrimSpace(req.ContainerID), NativeSessionID: strings.TrimSpace(req.NativeSessionID),
		NativeSessionPath: strings.TrimSpace(req.NativeSessionPath),
	}
	if _, err := tx.Exec(`INSERT INTO session_continuations (id,session_id,number,runtime_profile_id,runtime_provider,runner,status,container_id,native_session_id,native_session_path,runtime_config_version_id,started_at,updated_at,ended_at) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, continuation.ID, sessionID, continuation.Number, continuation.RuntimeProfileID, continuation.RuntimeProvider, string(continuation.Runner), string(continuation.Status), continuation.ContainerID, continuation.NativeSessionID, continuation.NativeSessionPath, continuation.RuntimeConfigID, formatTime(now), formatTime(now), ""); err != nil {
		return Continuation{}, fmt.Errorf("store Session continuation: %w", err)
	}
	if input != nil {
		for _, attachment := range copied {
			if _, err := appendEventTx(tx, sessionID, EventKindAttachment, payloadWithContinuation(EventPayload{
				"filename": attachment.Filename, "relative_path": attachment.Filename,
				"size": attachment.Size, "sha256": attachment.SHA256,
			}, continuation.ID), now); err != nil {
				return Continuation{}, fmt.Errorf("store Session attachment Event: %w", err)
			}
		}
		if _, err := appendEventTx(tx, sessionID, EventKindConversation, payloadWithContinuation(EventPayload{
			"role": input.Role, "text": input.Text,
		}, continuation.ID), now); err != nil {
			return Continuation{}, fmt.Errorf("store Session conversation Event: %w", err)
		}
		if _, err := tx.Exec(`UPDATE sessions SET updated_at=?,last_activity_at=? WHERE id=?`, formatTime(now), formatTime(now), sessionID); err != nil {
			return Continuation{}, fmt.Errorf("update Session activity: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return Continuation{}, fmt.Errorf("commit Session continuation: %w", err)
	}
	cleanupCopied = false
	return continuation, nil
}

// CreateReplacementContinuation starts a fresh turn boundary while retaining
// provider/container identity for the same persistent Session Runtime.
func (s *Service) CreateReplacementContinuation(previous Continuation, config map[string]any) (Continuation, error) {
	next, err := s.createContinuation(previous.SessionID, CreateContinuationRequest{
		RuntimeProfileID: previous.RuntimeProfileID, RuntimeProvider: previous.RuntimeProvider, Runner: previous.Runner,
		RuntimeConfig: config, ContainerID: previous.ContainerID,
		NativeSessionID: previous.NativeSessionID, NativeSessionPath: previous.NativeSessionPath,
	}, true, nil)
	if err != nil {
		return Continuation{}, err
	}
	return next, nil
}

// CreateReplacementContinuationWithInput atomically persists a replacement
// Continuation and the operator input that initiates it before carrying forward
// native provider identity.
func (s *Service) CreateReplacementContinuationWithInput(previous Continuation, config map[string]any, input ConversationInput) (Continuation, error) {
	next, err := s.createContinuation(previous.SessionID, CreateContinuationRequest{
		RuntimeProfileID: previous.RuntimeProfileID, RuntimeProvider: previous.RuntimeProvider, Runner: previous.Runner,
		RuntimeConfig: config, ContainerID: previous.ContainerID,
		NativeSessionID: previous.NativeSessionID, NativeSessionPath: previous.NativeSessionPath,
	}, true, &input)
	if err != nil {
		return Continuation{}, err
	}
	return next, nil
}

// ActiveContinuation returns the current pending/running Session Runtime pin.
func (s *Service) ActiveContinuation(sessionID string) (*Continuation, error) {
	row := s.db.QueryRow(`SELECT id,session_id,number,runtime_profile_id,runtime_provider,runner,status,container_id,native_session_id,native_session_path,runtime_config_version_id,started_at,updated_at,ended_at FROM session_continuations WHERE session_id=? AND status IN (?,?) ORDER BY number DESC LIMIT 1`, sessionID, string(RuntimeStatusPending), string(RuntimeStatusRunning))
	found, err := scanContinuation(row)
	if errors.Is(err, ErrContinuationNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &found, nil
}

// LatestContinuation returns the latest Session Runtime pin, if one exists.
func (s *Service) LatestContinuation(sessionID string) (*Continuation, error) {
	row := s.db.QueryRow(`SELECT id,session_id,number,runtime_profile_id,runtime_provider,runner,status,container_id,native_session_id,native_session_path,runtime_config_version_id,started_at,updated_at,ended_at FROM session_continuations WHERE session_id=? ORDER BY number DESC LIMIT 1`, sessionID)
	found, err := scanContinuation(row)
	if errors.Is(err, ErrContinuationNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &found, nil
}

func (s *Service) Continuation(id string) (Continuation, error) {
	return scanContinuation(s.db.QueryRow(`SELECT id,session_id,number,runtime_profile_id,runtime_provider,runner,status,container_id,native_session_id,native_session_path,runtime_config_version_id,started_at,updated_at,ended_at FROM session_continuations WHERE id=?`, id))
}

// UpdateContinuationStatus is idempotent for the same terminal status and
// rejects a late observer attempting to overwrite a different terminal state.
func (s *Service) UpdateContinuationStatus(id string, status RuntimeStatus) (Continuation, error) {
	if !validRuntimeStatus(status) {
		return Continuation{}, ErrContinuationStatusConflict
	}
	found, err := s.Continuation(id)
	if err != nil {
		return Continuation{}, err
	}
	if isTerminalRuntimeStatus(found.Status) {
		if found.Status == status {
			return s.notifyTerminalContinuation(found)
		}
		return found, ErrContinuationStatusConflict
	}
	now := time.Now().UTC()
	ended := ""
	if isTerminalRuntimeStatus(status) {
		ended = formatTime(now)
		found.EndedAt = &now
	}
	result, err := s.db.Exec(`UPDATE session_continuations SET status=?,updated_at=?,ended_at=? WHERE id=? AND status=?`, string(status), formatTime(now), ended, id, string(found.Status))
	if err != nil {
		return Continuation{}, fmt.Errorf("update Session continuation status: %w", err)
	}
	if changed, _ := result.RowsAffected(); changed != 1 {
		return s.Continuation(id)
	}
	found.Status, found.UpdatedAt = status, now
	if isTerminalRuntimeStatus(status) {
		return s.notifyTerminalContinuation(found)
	}
	return found, nil
}

func (s *Service) notifyTerminalContinuation(found Continuation) (Continuation, error) {
	if s.terminalMarker != nil {
		if err := s.terminalMarker.MarkContinuationTerminal(context.Background(), found.ID); err != nil {
			return found, fmt.Errorf("mark Session continuation capabilities terminal: %w", err)
		}
	}
	return found, nil
}

// UpdateContinuationRuntimeMetadata stores ownership data without exposing
// process handles to the API.
func (s *Service) UpdateContinuationRuntimeMetadata(id, containerID, nativeSessionID, nativeSessionPath string) (Continuation, error) {
	found, err := s.Continuation(id)
	if err != nil {
		return Continuation{}, err
	}
	if strings.TrimSpace(containerID) != "" {
		found.ContainerID = strings.TrimSpace(containerID)
	}
	if strings.TrimSpace(nativeSessionID) != "" {
		found.NativeSessionID = strings.TrimSpace(nativeSessionID)
	}
	if strings.TrimSpace(nativeSessionPath) != "" {
		found.NativeSessionPath = strings.TrimSpace(nativeSessionPath)
	}
	found.UpdatedAt = time.Now().UTC()
	if _, err := s.db.Exec(`UPDATE session_continuations SET container_id=?,native_session_id=?,native_session_path=?,updated_at=? WHERE id=?`, found.ContainerID, found.NativeSessionID, found.NativeSessionPath, formatTime(found.UpdatedAt), id); err != nil {
		return Continuation{}, fmt.Errorf("update Session Runtime metadata: %w", err)
	}
	return found, nil
}

// RuntimeConfigVersions lists all non-secret Session Runtime configuration
// versions in order.
func (s *Service) RuntimeConfigVersions(sessionID string) ([]RuntimeConfigVersion, error) {
	if _, err := s.Get(sessionID); err != nil {
		return nil, err
	}
	rows, err := s.db.Query(`SELECT id,session_id,version,runtime_profile_id,config_json,created_at FROM session_runtime_config_versions WHERE session_id=? ORDER BY version ASC`, sessionID)
	if err != nil {
		return nil, fmt.Errorf("list Session Runtime configs: %w", err)
	}
	defer rows.Close()
	versions := make([]RuntimeConfigVersion, 0)
	for rows.Next() {
		var version RuntimeConfigVersion
		var encoded, created string
		if err := rows.Scan(&version.ID, &version.SessionID, &version.Version, &version.RuntimeProfileID, &encoded, &created); err != nil {
			return nil, fmt.Errorf("scan Session Runtime config: %w", err)
		}
		if err := json.Unmarshal([]byte(encoded), &version.Config); err != nil {
			return nil, fmt.Errorf("decode Session Runtime config: %w", err)
		}
		if version.CreatedAt, err = time.Parse(time.RFC3339Nano, created); err != nil {
			return nil, fmt.Errorf("parse Session Runtime config time: %w", err)
		}
		versions = append(versions, version)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list Session Runtime configs: %w", err)
	}
	return versions, nil
}

type copiedAttachment struct {
	Filename string
	Size     int64
	SHA256   string
}

func copyAttachments(workdir string, input []Attachment) ([]copiedAttachment, error) {
	names, err := resolveAttachmentNames(workdir, input)
	if err != nil {
		return nil, err
	}
	result := make([]copiedAttachment, 0, len(names))
	created := make([]string, 0, len(names))
	cleanup := func() {
		for _, name := range created {
			_ = os.Remove(name)
		}
	}
	for index, item := range names {
		if item.Source.Open == nil {
			cleanup()
			return nil, fmt.Errorf("%w: %q has no source", ErrInvalidAttachment, item.Filename)
		}
		reader, err := item.Source.Open()
		if err != nil {
			cleanup()
			return nil, fmt.Errorf("open Session attachment %q: %w", item.Filename, err)
		}
		if item.Source.Size > MaxAttachmentFileBytes {
			_ = reader.Close()
			cleanup()
			return nil, fmt.Errorf("%w: attachment %q exceeds the %d MiB limit", ErrInvalidAttachment, item.Filename, MaxAttachmentFileBytes>>20)
		}
		temporary, err := os.CreateTemp(workdir, fmt.Sprintf(".session-attachment-%d-*", index))
		if err != nil {
			_ = reader.Close()
			cleanup()
			return nil, fmt.Errorf("stage Session attachment %q: %w", item.Filename, err)
		}
		_ = temporary.Chmod(0o600)
		digest := sha256.New()
		written, copyErr := io.Copy(io.MultiWriter(temporary, digest), io.LimitReader(reader, MaxAttachmentFileBytes+1))
		closeReaderErr := reader.Close()
		closeFileErr := temporary.Close()
		if copyErr != nil || closeReaderErr != nil || closeFileErr != nil || written > MaxAttachmentFileBytes {
			_ = os.Remove(temporary.Name())
			cleanup()
			if written > MaxAttachmentFileBytes {
				return nil, fmt.Errorf("%w: attachment %q exceeds the %d MiB limit", ErrInvalidAttachment, item.Filename, MaxAttachmentFileBytes>>20)
			}
			return nil, fmt.Errorf("write Session attachment %q: %v", item.Filename, firstError(copyErr, closeReaderErr, closeFileErr))
		}
		destination := filepath.Join(workdir, item.Filename)
		if filepath.Dir(destination) != filepath.Clean(workdir) {
			_ = os.Remove(temporary.Name())
			cleanup()
			return nil, fmt.Errorf("%w: %q", ErrInvalidAttachment, item.Filename)
		}
		if err := os.Rename(temporary.Name(), destination); err != nil {
			_ = os.Remove(temporary.Name())
			cleanup()
			return nil, fmt.Errorf("publish Session attachment %q: %w", item.Filename, err)
		}
		created = append(created, destination)
		result = append(result, copiedAttachment{Filename: item.Filename, Size: written, SHA256: hex.EncodeToString(digest.Sum(nil))})
	}
	return result, nil
}

type namedAttachment struct {
	Source   Attachment
	Filename string
}

func resolveAttachmentNames(workdir string, input []Attachment) ([]namedAttachment, error) {
	result := make([]namedAttachment, 0, len(input))
	used := make(map[string]struct{}, len(input))
	for _, attachment := range input {
		name := sanitizeAttachmentFilename(attachment.Name)
		if name == "" {
			return nil, fmt.Errorf("%w: %q has no usable filename", ErrInvalidAttachment, attachment.Name)
		}
		if attachment.Size < 0 {
			return nil, fmt.Errorf("%w: %q has an invalid size", ErrInvalidAttachment, name)
		}
		final := name
		if _, exists := used[attachmentNameCollisionKey(final)]; exists {
			final = uniqueAttachmentName(workdir, final, used)
		}
		if _, err := os.Stat(filepath.Join(workdir, final)); err == nil {
			final = uniqueAttachmentName(workdir, final, used)
		} else if !errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("%w: inspect existing filename %q: %v", ErrInvalidAttachment, final, err)
		}
		if _, exists := used[attachmentNameCollisionKey(final)]; exists {
			final = uniqueAttachmentName(workdir, final, used)
		}
		if _, exists := used[attachmentNameCollisionKey(final)]; exists {
			return nil, fmt.Errorf("%w: filename collision for %q", ErrInvalidAttachment, final)
		}
		used[attachmentNameCollisionKey(final)] = struct{}{}
		result = append(result, namedAttachment{Source: attachment, Filename: final})
	}
	return result, nil
}

func uniqueAttachmentName(workdir, name string, used map[string]struct{}) string {
	extension := filepath.Ext(name)
	stem := strings.TrimSuffix(name, extension)
	for suffix := 1; ; suffix++ {
		candidate := fmt.Sprintf("%s-%d%s", stem, suffix, extension)
		if _, exists := used[attachmentNameCollisionKey(candidate)]; exists {
			continue
		}
		if _, err := os.Stat(filepath.Join(workdir, candidate)); errors.Is(err, os.ErrNotExist) {
			return candidate
		}
	}
}

func attachmentNameCollisionKey(name string) string {
	return strings.ToLower(name)
}

func sanitizeAttachmentFilename(name string) string {
	name = strings.ReplaceAll(strings.TrimSpace(name), "\\", "/")
	name = path.Base(name)
	if name == "" || name == "." || name == ".." {
		return ""
	}
	return name
}

func firstError(errs ...error) error {
	for _, err := range errs {
		if err != nil {
			return err
		}
	}
	return errors.New("unknown attachment write error")
}

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

func normalizeBlackboardConclusionMode(value BlackboardConclusionMode) (BlackboardConclusionMode, error) {
	switch value {
	case "", BlackboardConclusionModeInteractive:
		return BlackboardConclusionModeInteractive, nil
	case BlackboardConclusionModeAssisted:
		return BlackboardConclusionModeAssisted, nil
	default:
		return "", ErrInvalidBlackboardConclusionMode
	}
}

type scanner interface{ Scan(dest ...any) error }

func scanSession(row scanner) (Session, error) {
	var found Session
	var lifecycle, mode, created, updated, activity string
	if err := row.Scan(&found.ID, &found.Title, &lifecycle, &found.Workdir, &mode, &created, &updated, &activity); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Session{}, ErrNotFound
		}
		return Session{}, err
	}
	found.Lifecycle = Lifecycle(lifecycle)
	found.RunControls.BlackboardConclusionMode = BlackboardConclusionMode(mode)
	found.BlackboardConclusion.Mode = found.RunControls.BlackboardConclusionMode
	found.BlackboardConclusion.State = BlackboardConclusionStateClean
	var err error
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

func scanContinuation(row scanner) (Continuation, error) {
	var found Continuation
	var runner, status, started, updated, ended string
	if err := row.Scan(&found.ID, &found.SessionID, &found.Number, &found.RuntimeProfileID, &found.RuntimeProvider, &runner, &status, &found.ContainerID, &found.NativeSessionID, &found.NativeSessionPath, &found.RuntimeConfigID, &started, &updated, &ended); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Continuation{}, ErrContinuationNotFound
		}
		return Continuation{}, err
	}
	found.Runner = Runner(runner)
	found.Status = RuntimeStatus(status)
	var err error
	if found.StartedAt, err = time.Parse(time.RFC3339Nano, started); err != nil {
		return Continuation{}, fmt.Errorf("parse Session continuation started_at: %w", err)
	}
	if found.UpdatedAt, err = time.Parse(time.RFC3339Nano, updated); err != nil {
		return Continuation{}, fmt.Errorf("parse Session continuation updated_at: %w", err)
	}
	if strings.TrimSpace(ended) != "" {
		stamp, parseErr := time.Parse(time.RFC3339Nano, ended)
		if parseErr != nil {
			return Continuation{}, fmt.Errorf("parse Session continuation ended_at: %w", parseErr)
		}
		found.EndedAt = &stamp
	}
	return found, nil
}

func validRuntimeStatus(status RuntimeStatus) bool {
	switch status {
	case RuntimeStatusPending, RuntimeStatusRunning, RuntimeStatusCompleted, RuntimeStatusFailed, RuntimeStatusStopped, RuntimeStatusInterrupted:
		return true
	default:
		return false
	}
}

func isTerminalRuntimeStatus(status RuntimeStatus) bool {
	switch status {
	case RuntimeStatusCompleted, RuntimeStatusFailed, RuntimeStatusStopped, RuntimeStatusInterrupted:
		return true
	default:
		return false
	}
}

func appendEventTx(tx *sql.Tx, sessionID string, kind EventKind, payload EventPayload, now time.Time) (Event, error) {
	if payload == nil {
		payload = EventPayload{}
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return Event{}, fmt.Errorf("encode Event payload: %w", err)
	}
	var max sql.NullInt64
	if err := tx.QueryRow(`SELECT MAX(seq) FROM session_events WHERE session_id=?`, sessionID).Scan(&max); err != nil {
		return Event{}, fmt.Errorf("read Session Event sequence: %w", err)
	}
	id, err := newID()
	if err != nil {
		return Event{}, fmt.Errorf("generate Session Event id: %w", err)
	}
	event := Event{ID: id, SessionID: sessionID, Seq: int(max.Int64) + 1, Kind: kind, Payload: payload, CreatedAt: now.UTC()}
	if _, err := tx.Exec(`INSERT INTO session_events (id,session_id,seq,kind,payload_json,created_at) VALUES (?,?,?,?,?,?)`, event.ID, event.SessionID, event.Seq, string(event.Kind), string(encoded), formatTime(event.CreatedAt)); err != nil {
		return Event{}, err
	}
	return event, nil
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
