package session

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// RecordRuntimeConfig stores the non-secret Runtime Turn Selection history for
// a Session. Each explicit configuration change creates a new version.
func (s *Service) RecordRuntimeConfig(sessionID, runtimeProfileID string, config map[string]any) (RuntimeConfigVersion, error) {
	if err := s.FlushRuntimeOutputs(sessionID); err != nil {
		return RuntimeConfigVersion{}, err
	}
	tx, err := s.db.Begin()
	if err != nil {
		return RuntimeConfigVersion{}, fmt.Errorf("begin Session Runtime config: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	version, err := s.RecordRuntimeConfigTx(tx, sessionID, runtimeProfileID, config)
	if err != nil {
		return RuntimeConfigVersion{}, err
	}
	if err := tx.Commit(); err != nil {
		return RuntimeConfigVersion{}, fmt.Errorf("commit Session Runtime config: %w", err)
	}
	return version, nil
}

// RecordRuntimeConfigTx appends one Runtime Turn Selection version inside a
// caller-owned transaction. Accepted Steering uses this to keep its selection,
// Conversation, attachments, and dispatch record atomic.
func (s *Service) RecordRuntimeConfigTx(tx *sql.Tx, sessionID, runtimeProfileID string, config map[string]any) (RuntimeConfigVersion, error) {
	if tx == nil {
		return RuntimeConfigVersion{}, errors.New("Session Runtime config transaction is required")
	}
	var count int
	if err := tx.QueryRow(`SELECT COUNT(*) FROM sessions WHERE id=?`, sessionID).Scan(&count); err != nil {
		return RuntimeConfigVersion{}, fmt.Errorf("validate Session Runtime config owner: %w", err)
	}
	if count != 1 {
		return RuntimeConfigVersion{}, ErrNotFound
	}
	encoded, err := json.Marshal(config)
	if err != nil {
		return RuntimeConfigVersion{}, fmt.Errorf("encode Session Runtime config: %w", err)
	}
	now := time.Now().UTC()
	version := RuntimeConfigVersion{ID: "session-config-" + strings.TrimPrefix(newIDMust(), "session-"), SessionID: sessionID, RuntimeProfileID: runtimeProfileID, Config: config, CreatedAt: now}
	var max sql.NullInt64
	if err := tx.QueryRow(`SELECT MAX(version) FROM session_runtime_config_versions WHERE session_id=?`, sessionID).Scan(&max); err != nil {
		return RuntimeConfigVersion{}, fmt.Errorf("read Session Runtime config version: %w", err)
	}
	version.Version = int(max.Int64) + 1
	if _, err := tx.Exec(`INSERT INTO session_runtime_config_versions (id,session_id,version,runtime_profile_id,config_json,created_at) VALUES (?,?,?,?,?,?)`, version.ID, sessionID, version.Version, runtimeProfileID, string(encoded), formatTime(now)); err != nil {
		return RuntimeConfigVersion{}, fmt.Errorf("store Session Runtime config: %w", err)
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
	if err := s.FlushRuntimeOutputs(sessionID); err != nil {
		return Continuation{}, err
	}
	if req.Runner != RunnerSandbox && req.Runner != RunnerHost {
		return Continuation{}, ErrInvalidRunner
	}
	if strings.TrimSpace(req.RuntimeProvider) == "" {
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
	if err := s.FlushRuntimeOutputs(sessionID); err != nil {
		return Continuation{}, err
	}
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
	if err := s.FlushRuntimeOutputs(found.SessionID); err != nil {
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
	// A lost compare-and-swap race must surface the conflict so the caller can
	// distinguish its own transition from a concurrent observer's write (the
	// same contract as the Task twin).
	if changed, err := result.RowsAffected(); err != nil {
		return Continuation{}, fmt.Errorf("count Session continuation status update: %w", err)
	} else if changed != 1 {
		current, readErr := s.Continuation(id)
		if readErr != nil {
			return Continuation{}, readErr
		}
		if current.Status == status {
			return current, nil
		}
		return current, ErrContinuationStatusConflict
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
