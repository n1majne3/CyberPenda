package session

import (
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

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

// PreparedConversationInput stages attachment files and can append their safe
// Event projections inside a caller-owned transaction. Commit keeps the files;
// Rollback removes them. Both finalizers are idempotent.
type PreparedConversationInput struct {
	service        *Service
	sessionID      string
	continuationID string
	role           string
	text           string
	workdir        string
	copied         []copiedAttachment
	finalized      bool
}

// PrepareConversationInput validates one input and stages its attachment files
// without writing Events. The caller must call Commit after its transaction
// commits, or Rollback on every failure path.
func (s *Service) PrepareConversationInput(id, continuationID, role, text string, input []Attachment) (*PreparedConversationInput, error) {
	if err := s.FlushRuntimeOutputs(id); err != nil {
		return nil, err
	}
	role = strings.TrimSpace(role)
	text = strings.TrimSpace(text)
	if role == "" && len(input) == 0 {
		return &PreparedConversationInput{service: s, sessionID: id, continuationID: continuationID, finalized: true}, nil
	}
	if role == "" && text != "" || role != "" && text == "" {
		return nil, ErrMissingInput
	}
	if len(input) > MaxAttachmentCount {
		return nil, fmt.Errorf("%w: too many attachments (max %d)", ErrInvalidAttachment, MaxAttachmentCount)
	}
	found, err := s.Get(id)
	if err != nil {
		return nil, err
	}
	if found.Lifecycle != LifecycleOpen {
		return nil, ErrSessionNotOpen
	}
	workdir, err := s.managedWorkdir(id)
	if err != nil {
		return nil, err
	}
	if filepath.Clean(found.Workdir) != filepath.Clean(workdir) {
		return nil, ErrInvalidWorkdir
	}
	if continuationID != "" {
		continuation, continuationErr := s.Continuation(continuationID)
		if continuationErr != nil {
			return nil, continuationErr
		}
		if continuation.SessionID != id {
			return nil, ErrContinuationNotFound
		}
	}
	copied, err := copyAttachments(workdir, input)
	if err != nil {
		return nil, err
	}
	return &PreparedConversationInput{
		service: s, sessionID: id, continuationID: continuationID,
		role: role, text: text, workdir: workdir, copied: copied,
	}, nil
}

func (prepared *PreparedConversationInput) AttachmentReferences() []AttachmentReference {
	result := make([]AttachmentReference, 0, len(prepared.copied))
	for _, attachment := range prepared.copied {
		result = append(result, AttachmentReference{RelativePath: attachment.Filename, ContinuationID: prepared.continuationID})
	}
	return result
}

// AppendTx writes attachment and Conversation Events inside a caller-owned
// transaction. It does not finalize the staged files.
// AppendTx requires the caller to flush Runtime output before opening tx.
func (prepared *PreparedConversationInput) AppendTx(tx *sql.Tx) ([]Event, Event, error) {
	if prepared == nil || prepared.service == nil || prepared.finalized {
		return nil, Event{}, errors.New("prepared Session conversation input is unavailable")
	}
	var lifecycle string
	if err := tx.QueryRow(`SELECT lifecycle FROM sessions WHERE id=?`, prepared.sessionID).Scan(&lifecycle); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, Event{}, ErrNotFound
		}
		return nil, Event{}, fmt.Errorf("read Session lifecycle: %w", err)
	}
	if Lifecycle(lifecycle) != LifecycleOpen {
		return nil, Event{}, ErrSessionNotOpen
	}
	if prepared.continuationID != "" {
		var count int
		if err := tx.QueryRow(`SELECT COUNT(*) FROM session_continuations WHERE id=? AND session_id=?`, prepared.continuationID, prepared.sessionID).Scan(&count); err != nil {
			return nil, Event{}, fmt.Errorf("validate Session continuation: %w", err)
		}
		if count != 1 {
			return nil, Event{}, ErrContinuationNotFound
		}
	}
	now := time.Now().UTC()
	events := make([]Event, 0, len(prepared.copied))
	for _, attachment := range prepared.copied {
		event, err := appendEventTx(tx, prepared.sessionID, EventKindAttachment, payloadWithContinuation(EventPayload{
			"filename": attachment.Filename, "relative_path": attachment.Filename,
			"size": attachment.Size, "sha256": attachment.SHA256,
		}, prepared.continuationID), now)
		if err != nil {
			return nil, Event{}, fmt.Errorf("store Session attachment Event: %w", err)
		}
		events = append(events, event)
	}
	var conversation Event
	var err error
	if prepared.role != "" {
		conversation, err = appendEventTx(tx, prepared.sessionID, EventKindConversation, payloadWithContinuation(EventPayload{
			"role": prepared.role, "text": prepared.text,
		}, prepared.continuationID), now)
		if err != nil {
			return nil, Event{}, fmt.Errorf("store Session conversation Event: %w", err)
		}
	}
	if err := touchSessionActivityTx(tx, prepared.sessionID, now); err != nil {
		return nil, Event{}, err
	}
	return events, conversation, nil
}

// Commit keeps staged files after the caller transaction commits.
func (prepared *PreparedConversationInput) Commit() {
	if prepared != nil {
		prepared.finalized = true
	}
}

func (prepared *PreparedConversationInput) Rollback() {
	if prepared == nil || prepared.finalized {
		return
	}
	for _, attachment := range prepared.copied {
		_ = os.Remove(filepath.Join(prepared.workdir, attachment.Filename))
	}
	prepared.finalized = true
}

func (s *Service) appendConversationInput(id, continuationID string, input []Attachment, role, text string) ([]Event, Event, error) {
	if err := s.FlushRuntimeOutputs(id); err != nil {
		return nil, Event{}, err
	}
	prepared, err := s.PrepareConversationInput(id, continuationID, role, text, input)
	if err != nil {
		return nil, Event{}, err
	}
	defer prepared.Rollback()
	if prepared.finalized {
		return nil, Event{}, nil
	}
	if err := s.FlushRuntimeOutputs(id); err != nil {
		return nil, Event{}, err
	}
	tx, err := s.db.Begin()
	if err != nil {
		return nil, Event{}, fmt.Errorf("begin Session conversation input: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	events, conversation, err := prepared.AppendTx(tx)
	if err != nil {
		return nil, Event{}, err
	}
	if err := tx.Commit(); err != nil {
		return nil, Event{}, fmt.Errorf("commit Session conversation input: %w", err)
	}
	prepared.Commit()
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
	return s.EventsByKind(id, EventKindConversation)
}
