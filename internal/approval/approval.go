// Package approval records human-gated workflow requests such as high-risk
// actions and scope expansions. Approvals are append-only requests; decisions
// are recorded later through audit entries.
package approval

import (
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"pentest/internal/project"
	"pentest/internal/store"
)

type Kind string

const (
	KindHighRiskAction Kind = "high_risk_action"
	KindScopeExpansion Kind = "scope_expansion"
)

type Status string

const (
	StatusPending  Status = "pending"
	StatusApproved Status = "approved"
	StatusRejected Status = "rejected"
)

type Decision string

const (
	DecisionApprove Decision = "approve"
	DecisionReject  Decision = "reject"
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
	Reviewer        string    `json:"reviewer,omitempty"`
	Decision        string    `json:"decision,omitempty"`
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

type DecideRequest struct {
	ApprovalID string
	Reviewer   string
	Decision   Decision
	Notes      string
}

var (
	ErrMissingProjectID       = errors.New("project id is required")
	ErrMissingRequestedAction = errors.New("requested action is required")
	ErrNotFound               = errors.New("approval not found")
	ErrAlreadyDecided         = errors.New("approval already decided")
	ErrInvalidDecision        = errors.New("decision must be approve or reject")
)

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

func (s *Service) ListByProject(projectID string, status Status) ([]Approval, error) {
	projectID = strings.TrimSpace(projectID)
	if projectID == "" {
		return nil, ErrMissingProjectID
	}

	query := `SELECT id, project_id, task_id, kind, status, requester, requested_action, rationale, payload_json, reviewer, decision, created_at, updated_at
		FROM approvals WHERE project_id = ?`
	args := []any{projectID}
	if status != "" {
		query += ` AND status = ?`
		args = append(args, string(status))
	}
	query += ` ORDER BY created_at DESC`

	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, fmt.Errorf("list approvals: %w", err)
	}
	defer rows.Close()

	var out []Approval
	for rows.Next() {
		got, err := scanApproval(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, got)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate approvals: %w", err)
	}
	if out == nil {
		out = []Approval{}
	}
	return out, nil
}

func (s *Service) CountPending(projectID string) (int, error) {
	projectID = strings.TrimSpace(projectID)
	if projectID == "" {
		return 0, ErrMissingProjectID
	}
	var count int
	err := s.db.QueryRow(
		`SELECT COUNT(*) FROM approvals WHERE project_id = ? AND status = ?`,
		projectID, string(StatusPending),
	).Scan(&count)
	if err != nil {
		return 0, fmt.Errorf("count pending approvals: %w", err)
	}
	return count, nil
}

func (s *Service) Get(id string) (Approval, error) {
	id = strings.TrimSpace(id)
	if id == "" {
		return Approval{}, ErrNotFound
	}
	row := s.db.QueryRow(
		`SELECT id, project_id, task_id, kind, status, requester, requested_action, rationale, payload_json, reviewer, decision, created_at, updated_at
		 FROM approvals WHERE id = ?`, id,
	)
	got, err := scanApproval(row)
	if errors.Is(err, sql.ErrNoRows) {
		return Approval{}, ErrNotFound
	}
	if err != nil {
		return Approval{}, err
	}
	return got, nil
}

func (s *Service) Decide(req DecideRequest) (Approval, error) {
	req.ApprovalID = strings.TrimSpace(req.ApprovalID)
	req.Reviewer = strings.TrimSpace(req.Reviewer)
	if req.ApprovalID == "" {
		return Approval{}, ErrNotFound
	}
	if req.Decision != DecisionApprove && req.Decision != DecisionReject {
		return Approval{}, ErrInvalidDecision
	}

	existing, err := s.Get(req.ApprovalID)
	if err != nil {
		return Approval{}, err
	}
	if existing.Status != StatusPending {
		return Approval{}, ErrAlreadyDecided
	}

	now := time.Now().UTC()
	newStatus := StatusRejected
	if req.Decision == DecisionApprove {
		newStatus = StatusApproved
	}

	_, err = s.db.Exec(
		`UPDATE approvals SET status = ?, reviewer = ?, decision = ?, updated_at = ? WHERE id = ?`,
		string(newStatus), req.Reviewer, string(req.Decision), now.Format(time.RFC3339Nano), existing.ID,
	)
	if err != nil {
		return Approval{}, fmt.Errorf("update approval: %w", err)
	}

	updated, err := s.Get(existing.ID)
	if err != nil {
		return Approval{}, err
	}

	summary := fmt.Sprintf("%s %s: %s", updated.Kind, req.Decision, updated.RequestedAction)
	if _, err := s.RecordAudit(AuditEntry{
		ID:        newID(),
		ProjectID: updated.ProjectID,
		TaskID:    updated.TaskID,
		Kind:      "approval_decided",
		Summary:   summary,
		Payload: map[string]any{
			"approval_id": updated.ID,
			"decision":    req.Decision,
			"reviewer":    req.Reviewer,
			"notes":       strings.TrimSpace(req.Notes),
			"approval":    updated,
		},
		CreatedAt: now,
	}); err != nil {
		return Approval{}, err
	}

	return updated, nil
}

// ApplyScopeExpansion merges an approved scope-expansion payload into the project scope.
func ApplyScopeExpansion(scope project.Scope, payload any) project.Scope {
	data, ok := payload.(map[string]any)
	if !ok {
		if raw, err := json.Marshal(payload); err == nil {
			var decoded map[string]any
			if json.Unmarshal(raw, &decoded) == nil {
				data = decoded
			}
		}
	}
	if data == nil {
		return scope
	}

	scope.Domains = appendUnique(scope.Domains, stringList(data["domains"])...)
	scope.IPs = appendUnique(scope.IPs, stringList(data["ips"])...)
	scope.CIDRs = appendUnique(scope.CIDRs, stringList(data["cidrs"])...)
	scope.URLs = appendUnique(scope.URLs, stringList(data["urls"])...)
	scope.Ports = appendUnique(scope.Ports, stringList(data["ports"])...)

	assetType, _ := data["asset_type"].(string)
	asset, _ := data["asset"].(string)
	asset = strings.TrimSpace(asset)
	if asset != "" {
		switch strings.ToLower(strings.TrimSpace(assetType)) {
		case "domain":
			scope.Domains = appendUnique(scope.Domains, asset)
		case "ip":
			scope.IPs = appendUnique(scope.IPs, asset)
		case "cidr":
			scope.CIDRs = appendUnique(scope.CIDRs, asset)
		case "url":
			scope.URLs = appendUnique(scope.URLs, asset)
		case "port":
			scope.Ports = appendUnique(scope.Ports, asset)
		default:
			scope.Domains = appendUnique(scope.Domains, asset)
		}
	}
	return scope
}

func (s *Service) ListAudit(projectID string, limit int) ([]AuditEntry, error) {
	projectID = strings.TrimSpace(projectID)
	if projectID == "" {
		return nil, ErrMissingProjectID
	}
	if limit <= 0 {
		limit = 200
	}

	rows, err := s.db.Query(
		`SELECT id, project_id, task_id, kind, summary, payload_json, created_at
		 FROM audit_logs WHERE project_id = ? ORDER BY created_at DESC LIMIT ?`,
		projectID, limit,
	)
	if err != nil {
		return nil, fmt.Errorf("list audit logs: %w", err)
	}
	defer rows.Close()

	var out []AuditEntry
	for rows.Next() {
		got, err := scanAudit(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, got)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate audit logs: %w", err)
	}
	if out == nil {
		out = []AuditEntry{}
	}
	return out, nil
}

func (s *Service) RecordAudit(entry AuditEntry) (AuditEntry, error) {
	if entry.ID == "" {
		entry.ID = newID()
	}
	if entry.CreatedAt.IsZero() {
		entry.CreatedAt = time.Now().UTC()
	}
	return s.appendAudit(entry)
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

type rowScanner interface {
	Scan(dest ...any) error
}

func scanApproval(row rowScanner) (Approval, error) {
	var got Approval
	var kind, status, taskID, requester, rationale, payloadJSON, reviewer, decision string
	var createdAt, updatedAt string
	if err := row.Scan(
		&got.ID, &got.ProjectID, &taskID, &kind, &status, &requester,
		&got.RequestedAction, &rationale, &payloadJSON, &reviewer, &decision,
		&createdAt, &updatedAt,
	); err != nil {
		return Approval{}, fmt.Errorf("scan approval: %w", err)
	}
	got.TaskID = taskID
	got.Kind = Kind(kind)
	got.Status = Status(status)
	got.Requester = requester
	got.Rationale = rationale
	got.Reviewer = reviewer
	got.Decision = decision
	if payloadJSON != "" && payloadJSON != "{}" {
		var payload any
		if err := json.Unmarshal([]byte(payloadJSON), &payload); err == nil {
			got.Payload = payload
		}
	}
	got.CreatedAt, _ = time.Parse(time.RFC3339Nano, createdAt)
	got.UpdatedAt, _ = time.Parse(time.RFC3339Nano, updatedAt)
	return got, nil
}

func scanAudit(row rowScanner) (AuditEntry, error) {
	var got AuditEntry
	var taskID, payloadJSON, createdAt string
	if err := row.Scan(
		&got.ID, &got.ProjectID, &taskID, &got.Kind, &got.Summary, &payloadJSON, &createdAt,
	); err != nil {
		return AuditEntry{}, fmt.Errorf("scan audit log: %w", err)
	}
	got.TaskID = taskID
	if payloadJSON != "" && payloadJSON != "{}" {
		var payload any
		if err := json.Unmarshal([]byte(payloadJSON), &payload); err == nil {
			got.Payload = payload
		}
	}
	got.CreatedAt, _ = time.Parse(time.RFC3339Nano, createdAt)
	return got, nil
}

func stringList(raw any) []string {
	switch v := raw.(type) {
	case []string:
		return v
	case []any:
		out := make([]string, 0, len(v))
		for _, item := range v {
			if s, ok := item.(string); ok && strings.TrimSpace(s) != "" {
				out = append(out, strings.TrimSpace(s))
			}
		}
		return out
	default:
		return nil
	}
}

func appendUnique(existing []string, values ...string) []string {
	seen := make(map[string]struct{}, len(existing))
	for _, item := range existing {
		seen[item] = struct{}{}
	}
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		existing = append(existing, value)
	}
	return existing
}

func newID() string {
	var bytes [16]byte
	if _, err := rand.Read(bytes[:]); err != nil {
		return hex.EncodeToString([]byte(time.Now().UTC().Format(time.RFC3339Nano)))
	}
	return hex.EncodeToString(bytes[:])
}