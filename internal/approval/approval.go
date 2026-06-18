// Package approval records human-gated workflow requests such as high-risk
// actions and scope expansions. Approvals are append-only requests; decisions
// are recorded later through audit entries.
package approval

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"pentest/internal/store"
)

type Kind string

const (
	KindHighRiskAction Kind = "high_risk_action"
	KindScopeExpansion Kind = "scope_expansion"
)

type Status string

const (
	StatusPending Status = "pending"
)

type Approval struct {
	ID              string    `json:"id"`
	ProjectID       string    `json:"project_id"`
	TaskID          string    `json:"task_id,omitempty"`
	Kind            Kind      `json:"kind"`
	Status          Status    `json:"status"`
	Requester       string    `json:"requester,omitempty"`
	RequestedAction string    `json:"requested_action"`
	Rationale       string    `json:"rationale,omitempty"`
	Payload         any       `json:"payload,omitempty"`
	CreatedAt       time.Time `json:"created_at"`
	UpdatedAt       time.Time `json:"updated_at"`
}

type AuditEntry struct {
	ID        string    `json:"id"`
	ProjectID string    `json:"project_id"`
	TaskID    string    `json:"task_id,omitempty"`
	Kind      string    `json:"kind"`
	Summary   string    `json:"summary"`
	Payload   any       `json:"payload,omitempty"`
	CreatedAt time.Time `json:"created_at"`
}

type Request struct {
	ProjectID       string
	TaskID          string
	Requester       string
	RequestedAction string
	Rationale       string
	Payload         any
}

var ErrMissingProjectID = errors.New("project id is required")
var ErrMissingRequestedAction = errors.New("requested action is required")

type Service struct {
	db *store.DB
}

func NewService(db *store.DB) *Service {
	return &Service{db: db}
}

func (s *Service) RequestHighRiskAction(req Request) (Approval, error) {
	return s.create(KindHighRiskAction, req)
}

func (s *Service) RequestScopeExpansion(req Request) (Approval, error) {
	return s.create(KindScopeExpansion, req)
}

func (s *Service) create(kind Kind, req Request) (Approval, error) {
	req.ProjectID = strings.TrimSpace(req.ProjectID)
	req.RequestedAction = strings.TrimSpace(req.RequestedAction)
	if req.ProjectID == "" {
		return Approval{}, ErrMissingProjectID
	}
	if req.RequestedAction == "" {
		return Approval{}, ErrMissingRequestedAction
	}

	payloadJSON, err := json.Marshal(req.Payload)
	if err != nil {
		return Approval{}, fmt.Errorf("marshal payload: %w", err)
	}

	now := time.Now().UTC()
	approval := Approval{
		ID:              newID(),
		ProjectID:       req.ProjectID,
		TaskID:          strings.TrimSpace(req.TaskID),
		Kind:            kind,
		Status:          StatusPending,
		Requester:       strings.TrimSpace(req.Requester),
		RequestedAction: req.RequestedAction,
		Rationale:       strings.TrimSpace(req.Rationale),
		Payload:         req.Payload,
		CreatedAt:       now,
		UpdatedAt:       now,
	}

	_, err = s.db.Exec(
		`INSERT INTO approvals (id, project_id, task_id, kind, status, requester, requested_action, rationale, payload_json, reviewer, decision, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, '', '', ?, ?)`,
		approval.ID, approval.ProjectID, approval.TaskID, string(approval.Kind), string(approval.Status),
		approval.Requester, approval.RequestedAction, approval.Rationale, string(payloadJSON),
		approval.CreatedAt.Format(time.RFC3339Nano), approval.UpdatedAt.Format(time.RFC3339Nano),
	)
	if err != nil {
		return Approval{}, fmt.Errorf("store approval: %w", err)
	}

	auditSummary := fmt.Sprintf("%s requested: %s", kind, approval.RequestedAction)
	if _, err := s.appendAudit(AuditEntry{
		ID:        newID(),
		ProjectID: approval.ProjectID,
		TaskID:    approval.TaskID,
		Kind:      "approval_requested",
		Summary:   auditSummary,
		Payload:   approval,
		CreatedAt: now,
	}); err != nil {
		return Approval{}, err
	}

	return approval, nil
}

func (s *Service) appendAudit(entry AuditEntry) (AuditEntry, error) {
	payloadJSON, err := json.Marshal(entry.Payload)
	if err != nil {
		return AuditEntry{}, fmt.Errorf("marshal audit payload: %w", err)
	}
	_, err = s.db.Exec(
		`INSERT INTO audit_logs (id, project_id, task_id, kind, summary, payload_json, created_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?)`,
		entry.ID, entry.ProjectID, entry.TaskID, entry.Kind, entry.Summary, string(payloadJSON),
		entry.CreatedAt.Format(time.RFC3339Nano),
	)
	if err != nil {
		return AuditEntry{}, fmt.Errorf("store audit log: %w", err)
	}
	return entry, nil
}

func newID() string {
	var bytes [16]byte
	if _, err := rand.Read(bytes[:]); err != nil {
		return hex.EncodeToString([]byte(time.Now().UTC().Format(time.RFC3339Nano)))
	}
	return hex.EncodeToString(bytes[:])
}
