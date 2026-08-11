// Package scopeexpansion owns proposed additions to Project Scope. A proposal
// does not authorize work until an operator approves it.
package scopeexpansion

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"pentest/internal/project"
	"pentest/internal/store"
)

type Status string

const (
	StatusProposed Status = "proposed"
	StatusApproved Status = "approved"
	StatusRejected Status = "rejected"
)

type TrustedOriginKind string

const (
	TrustedOriginOperator TrustedOriginKind = "operator"
	TrustedOriginRuntime  TrustedOriginKind = "runtime"
)

// TrustedOrigin is internal integrity data. It is stored with the proposal but
// omitted from ordinary API JSON.
type TrustedOrigin struct {
	Kind           TrustedOriginKind `json:"-"`
	TaskID         string            `json:"-"`
	ContinuationID string            `json:"-"`
}

type Proposal struct {
	ID              string        `json:"id"`
	ProjectID       string        `json:"project_id"`
	Addition        project.Scope `json:"addition"`
	DiscoverySource string        `json:"discovery_source"`
	Reason          string        `json:"reason"`
	Risk            string        `json:"risk"`
	Status          Status        `json:"status"`
	TrustedOrigin   TrustedOrigin `json:"-"`
	CreatedAt       time.Time     `json:"created_at"`
	DecidedAt       *time.Time    `json:"decided_at,omitempty"`
}

type ProposeRequest struct {
	ProjectID       string
	Addition        project.Scope
	DiscoverySource string
	Reason          string
	Risk            string
	Origin          TrustedOrigin
}

var (
	ErrInvalid       = errors.New("invalid Scope Expansion")
	ErrNotFound      = errors.New("Scope Expansion not found")
	ErrNotProposed   = errors.New("Scope Expansion is not proposed")
	ErrTrustedOrigin = errors.New("Scope Expansion Trusted Origin is not proven")
)

type Service struct {
	db       *store.DB
	projects *project.Service
}

func NewService(db *store.DB, projects *project.Service) *Service {
	return &Service{db: db, projects: projects}
}

func (service *Service) Propose(request ProposeRequest) (Proposal, error) {
	request.ProjectID = strings.TrimSpace(request.ProjectID)
	request.DiscoverySource = strings.TrimSpace(request.DiscoverySource)
	request.Reason = strings.TrimSpace(request.Reason)
	request.Risk = strings.TrimSpace(request.Risk)
	request.Origin.TaskID = strings.TrimSpace(request.Origin.TaskID)
	request.Origin.ContinuationID = strings.TrimSpace(request.Origin.ContinuationID)
	request.Addition = normalizeScope(request.Addition)
	if request.ProjectID == "" || request.DiscoverySource == "" || request.Reason == "" || request.Risk == "" || !scopeHasAddition(request.Addition) {
		return Proposal{}, ErrInvalid
	}
	if _, err := service.projects.Get(request.ProjectID); err != nil {
		return Proposal{}, err
	}
	if err := service.validateTrustedOrigin(request.ProjectID, request.Origin); err != nil {
		return Proposal{}, err
	}
	additionJSON, err := json.Marshal(request.Addition)
	if err != nil {
		return Proposal{}, fmt.Errorf("encode Scope Expansion: %w", err)
	}
	now := time.Now().UTC()
	proposal := Proposal{
		ID: newID(), ProjectID: request.ProjectID, Addition: request.Addition,
		DiscoverySource: request.DiscoverySource, Reason: request.Reason, Risk: request.Risk,
		Status: StatusProposed, TrustedOrigin: request.Origin, CreatedAt: now,
	}
	var taskID, continuationID any
	if request.Origin.Kind == TrustedOriginRuntime {
		taskID, continuationID = request.Origin.TaskID, request.Origin.ContinuationID
	}
	if _, err := service.db.Exec(`INSERT INTO scope_expansions
		(id,project_id,addition_json,discovery_source,reason,risk,status,origin_kind,origin_task_id,origin_continuation_id,created_at)
		VALUES(?,?,?,?,?,?,?,?,?,?,?)`, proposal.ID, proposal.ProjectID, string(additionJSON), proposal.DiscoverySource,
		proposal.Reason, proposal.Risk, string(proposal.Status), string(proposal.TrustedOrigin.Kind), taskID, continuationID,
		now.Format(time.RFC3339Nano)); err != nil {
		return Proposal{}, fmt.Errorf("store Scope Expansion: %w", err)
	}
	return proposal, nil
}

func (service *Service) List(projectID string) ([]Proposal, error) {
	rows, err := service.db.Query(`SELECT id,project_id,addition_json,discovery_source,reason,risk,status,origin_kind,origin_task_id,origin_continuation_id,created_at,decided_at
		FROM scope_expansions WHERE project_id=? ORDER BY created_at,id`, strings.TrimSpace(projectID))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	proposals := []Proposal{}
	for rows.Next() {
		proposal, err := scanProposal(rows)
		if err != nil {
			return nil, err
		}
		proposals = append(proposals, proposal)
	}
	return proposals, rows.Err()
}

func (service *Service) Approve(projectID, proposalID string) (Proposal, project.Project, error) {
	return service.decide(projectID, proposalID, StatusApproved)
}

func (service *Service) Reject(projectID, proposalID string) (Proposal, project.Project, error) {
	return service.decide(projectID, proposalID, StatusRejected)
}

func (service *Service) decide(projectID, proposalID string, decision Status) (Proposal, project.Project, error) {
	projectID, proposalID = strings.TrimSpace(projectID), strings.TrimSpace(proposalID)
	tx, err := service.db.Begin()
	if err != nil {
		return Proposal{}, project.Project{}, err
	}
	defer tx.Rollback()
	proposal, err := scanProposal(tx.QueryRow(`SELECT id,project_id,addition_json,discovery_source,reason,risk,status,origin_kind,origin_task_id,origin_continuation_id,created_at,decided_at
		FROM scope_expansions WHERE id=? AND project_id=?`, proposalID, projectID))
	if errors.Is(err, sql.ErrNoRows) {
		return Proposal{}, project.Project{}, ErrNotFound
	}
	if err != nil {
		return Proposal{}, project.Project{}, err
	}
	if proposal.Status != StatusProposed {
		return Proposal{}, project.Project{}, ErrNotProposed
	}
	now := time.Now().UTC()
	if decision == StatusApproved {
		var scopeJSON string
		if err := tx.QueryRow(`SELECT scope_json FROM projects WHERE id=?`, projectID).Scan(&scopeJSON); err != nil {
			return Proposal{}, project.Project{}, err
		}
		var current project.Scope
		if err := json.Unmarshal([]byte(scopeJSON), &current); err != nil {
			return Proposal{}, project.Project{}, fmt.Errorf("decode Project Scope: %w", err)
		}
		merged := mergeScope(current, proposal.Addition)
		mergedJSON, err := json.Marshal(merged)
		if err != nil {
			return Proposal{}, project.Project{}, err
		}
		if _, err := tx.Exec(`UPDATE projects SET scope_json=?,updated_at=? WHERE id=?`, string(mergedJSON), now.Format(time.RFC3339Nano), projectID); err != nil {
			return Proposal{}, project.Project{}, err
		}
	}
	result, err := tx.Exec(`UPDATE scope_expansions SET status=?,decided_at=? WHERE id=? AND status=?`, string(decision), now.Format(time.RFC3339Nano), proposal.ID, string(StatusProposed))
	if err != nil {
		return Proposal{}, project.Project{}, err
	}
	changed, _ := result.RowsAffected()
	if changed != 1 {
		return Proposal{}, project.Project{}, ErrNotProposed
	}
	if err := tx.Commit(); err != nil {
		return Proposal{}, project.Project{}, err
	}
	proposal.Status, proposal.DecidedAt = decision, &now
	updated, err := service.projects.Get(projectID)
	return proposal, updated, err
}

func (service *Service) validateTrustedOrigin(projectID string, origin TrustedOrigin) error {
	switch origin.Kind {
	case TrustedOriginOperator:
		if origin.TaskID != "" || origin.ContinuationID != "" {
			return ErrTrustedOrigin
		}
		return nil
	case TrustedOriginRuntime:
		var foundProjectID, foundTaskID string
		err := service.db.QueryRow(`SELECT tasks.project_id,task_continuations.task_id FROM task_continuations
			JOIN tasks ON tasks.id=task_continuations.task_id WHERE task_continuations.id=?`, origin.ContinuationID).Scan(&foundProjectID, &foundTaskID)
		if err != nil || foundProjectID != projectID || foundTaskID != origin.TaskID {
			return ErrTrustedOrigin
		}
		return nil
	default:
		return ErrTrustedOrigin
	}
}

func scanProposal(row interface{ Scan(...any) error }) (Proposal, error) {
	var proposal Proposal
	var additionJSON, status, originKind, createdAt string
	var taskID, continuationID, decidedAt sql.NullString
	if err := row.Scan(&proposal.ID, &proposal.ProjectID, &additionJSON, &proposal.DiscoverySource, &proposal.Reason, &proposal.Risk,
		&status, &originKind, &taskID, &continuationID, &createdAt, &decidedAt); err != nil {
		return Proposal{}, err
	}
	if err := json.Unmarshal([]byte(additionJSON), &proposal.Addition); err != nil {
		return Proposal{}, err
	}
	proposal.Status = Status(status)
	proposal.TrustedOrigin = TrustedOrigin{Kind: TrustedOriginKind(originKind), TaskID: taskID.String, ContinuationID: continuationID.String}
	var err error
	if proposal.CreatedAt, err = time.Parse(time.RFC3339Nano, createdAt); err != nil {
		return Proposal{}, err
	}
	if decidedAt.Valid {
		parsed, err := time.Parse(time.RFC3339Nano, decidedAt.String)
		if err != nil {
			return Proposal{}, err
		}
		proposal.DecidedAt = &parsed
	}
	return proposal, nil
}

func normalizeScope(scope project.Scope) project.Scope {
	scope.Domains = normalizeValues(scope.Domains)
	scope.IPs = normalizeValues(scope.IPs)
	scope.CIDRs = normalizeValues(scope.CIDRs)
	scope.URLs = normalizeValues(scope.URLs)
	scope.Ports = normalizeValues(scope.Ports)
	scope.Excluded = normalizeValues(scope.Excluded)
	scope.TestingLimits = normalizeValues(scope.TestingLimits)
	scope.Capabilities = normalizeValues(scope.Capabilities)
	scope.Notes = ""
	return scope
}

func normalizeValues(values []string) []string {
	result, seen := []string{}, map[string]bool{}
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value != "" && !seen[value] {
			seen[value] = true
			result = append(result, value)
		}
	}
	return result
}

func scopeHasAddition(scope project.Scope) bool {
	return len(scope.Domains)+len(scope.IPs)+len(scope.CIDRs)+len(scope.URLs)+len(scope.Ports)+len(scope.Excluded)+len(scope.TestingLimits)+len(scope.Capabilities) > 0
}

func mergeScope(current, addition project.Scope) project.Scope {
	current.Domains = normalizeValues(append(current.Domains, addition.Domains...))
	current.IPs = normalizeValues(append(current.IPs, addition.IPs...))
	current.CIDRs = normalizeValues(append(current.CIDRs, addition.CIDRs...))
	current.URLs = normalizeValues(append(current.URLs, addition.URLs...))
	current.Ports = normalizeValues(append(current.Ports, addition.Ports...))
	current.Excluded = normalizeValues(append(current.Excluded, addition.Excluded...))
	current.TestingLimits = normalizeValues(append(current.TestingLimits, addition.TestingLimits...))
	current.Capabilities = normalizeValues(append(current.Capabilities, addition.Capabilities...))
	return current
}

func newID() string {
	return fmt.Sprintf("scope-expansion-%d", time.Now().UTC().UnixNano())
}
