// Package reasontask owns operator-triggered planning Tasks and their
// approval-required Blackboard proposals.
package reasontask

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"pentest/internal/blackboardv2"
	"pentest/internal/store"
)

const LaunchGoal = "Read the complete Runtime Blackboard Snapshot and prepare an approval-required proposal for next Task Goals, Exploration Objective changes, and a readiness judgment. Do not mutate Blackboard records directly."

type Status string

const (
	StatusProposed Status = "proposed"
	StatusApproved Status = "approved"
	StatusRejected Status = "rejected"
)

type Proposal struct {
	ID                          string                `json:"id"`
	ProjectID                   string                `json:"project_id"`
	ReasonTaskID                string                `json:"reason_task_id"`
	NextTaskGoals               []string              `json:"next_task_goals"`
	ExplorationObjectiveChanges []string              `json:"exploration_objective_changes"`
	ReadinessJudgment           string                `json:"readiness_judgment"`
	Changes                     []blackboardv2.Change `json:"changes"`
	Status                      Status                `json:"status"`
	CreatedAt                   time.Time             `json:"created_at"`
	DecidedAt                   *time.Time            `json:"decided_at,omitempty"`
}

type ProposeRequest struct {
	ProjectID                   string
	ReasonTaskID                string
	NextTaskGoals               []string
	ExplorationObjectiveChanges []string
	ReadinessJudgment           string
	Changes                     []blackboardv2.Change
}

var (
	ErrInvalid     = errors.New("invalid Reason Task proposal")
	ErrNotFound    = errors.New("Reason Task proposal not found")
	ErrNotProposed = errors.New("Reason Task proposal is not proposed")
	ErrNotReason   = errors.New("Task is not a registered Reason Task")
)

type Service struct {
	db    *store.DB
	board *blackboardv2.Service
}

func NewService(db *store.DB, board *blackboardv2.Service) *Service {
	return &Service{db: db, board: board}
}

// Register records the server-owned Reason Task role after normal Task Launch
// has captured its Project Scope and Runtime configuration.
func (service *Service) Register(projectID, taskID string) error {
	projectID, taskID = strings.TrimSpace(projectID), strings.TrimSpace(taskID)
	var actualProjectID string
	if err := service.db.QueryRow(`SELECT project_id FROM tasks WHERE id=?`, taskID).Scan(&actualProjectID); err != nil || actualProjectID != projectID {
		return ErrNotReason
	}
	_, err := service.db.Exec(`INSERT OR IGNORE INTO reason_tasks(task_id,project_id,created_at) VALUES(?,?,?)`, taskID, projectID, time.Now().UTC().Format(time.RFC3339Nano))
	return err
}

func (service *Service) Propose(request ProposeRequest) (Proposal, error) {
	request.ProjectID, request.ReasonTaskID = strings.TrimSpace(request.ProjectID), strings.TrimSpace(request.ReasonTaskID)
	request.NextTaskGoals = normalizeText(request.NextTaskGoals)
	request.ExplorationObjectiveChanges = normalizeText(request.ExplorationObjectiveChanges)
	request.ReadinessJudgment = strings.TrimSpace(request.ReadinessJudgment)
	if request.ProjectID == "" || request.ReasonTaskID == "" || len(request.NextTaskGoals) == 0 ||
		len(request.ExplorationObjectiveChanges) == 0 || request.ReadinessJudgment == "" || len(request.Changes) == 0 {
		return Proposal{}, ErrInvalid
	}
	var registered int
	if err := service.db.QueryRow(`SELECT COUNT(*) FROM reason_tasks WHERE task_id=? AND project_id=?`, request.ReasonTaskID, request.ProjectID).Scan(&registered); err != nil {
		return Proposal{}, err
	}
	if registered != 1 {
		return Proposal{}, ErrNotReason
	}
	changesJSON, err := json.Marshal(request.Changes)
	if err != nil {
		return Proposal{}, ErrInvalid
	}
	var normalizedChanges []blackboardv2.Change
	if err := json.Unmarshal(changesJSON, &normalizedChanges); err != nil {
		return Proposal{}, fmt.Errorf("%w: %v", ErrInvalid, err)
	}
	metadataJSON, err := json.Marshal(struct {
		NextTaskGoals               []string `json:"next_task_goals"`
		ExplorationObjectiveChanges []string `json:"exploration_objective_changes"`
		ReadinessJudgment           string   `json:"readiness_judgment"`
	}{request.NextTaskGoals, request.ExplorationObjectiveChanges, request.ReadinessJudgment})
	if err != nil {
		return Proposal{}, err
	}
	now := time.Now().UTC()
	proposal := Proposal{
		ID: newID(), ProjectID: request.ProjectID, ReasonTaskID: request.ReasonTaskID,
		NextTaskGoals: request.NextTaskGoals, ExplorationObjectiveChanges: request.ExplorationObjectiveChanges,
		ReadinessJudgment: request.ReadinessJudgment, Changes: normalizedChanges, Status: StatusProposed, CreatedAt: now,
	}
	if _, err := service.db.Exec(`INSERT INTO reason_task_proposals
		(id,project_id,reason_task_id,proposal_json,change_batch_json,status,created_at) VALUES(?,?,?,?,?,?,?)`,
		proposal.ID, proposal.ProjectID, proposal.ReasonTaskID, string(metadataJSON), string(changesJSON), string(proposal.Status), now.Format(time.RFC3339Nano)); err != nil {
		return Proposal{}, err
	}
	return proposal, nil
}

func (service *Service) List(projectID string) ([]Proposal, error) {
	rows, err := service.db.Query(`SELECT id,project_id,reason_task_id,proposal_json,change_batch_json,status,created_at,decided_at
		FROM reason_task_proposals WHERE project_id=? ORDER BY created_at,id`, strings.TrimSpace(projectID))
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

func (service *Service) Approve(ctx context.Context, projectID, proposalID string) (Proposal, blackboardv2.ChangeResult, error) {
	proposal, err := service.load(projectID, proposalID)
	if err != nil {
		return Proposal{}, blackboardv2.ChangeResult{}, err
	}
	if proposal.Status != StatusProposed {
		return Proposal{}, blackboardv2.ChangeResult{}, ErrNotProposed
	}
	result, err := service.board.Apply(ctx, proposal.ProjectID, blackboardv2.ChangeBatch{
		Schema: "semantic-change-batch/v2", IdempotencyKey: "reason-task-proposal:" + proposal.ID, Changes: proposal.Changes,
	})
	if err != nil {
		return Proposal{}, blackboardv2.ChangeResult{}, err
	}
	now := time.Now().UTC()
	update, err := service.db.Exec(`UPDATE reason_task_proposals SET status=?,decided_at=? WHERE id=? AND project_id=? AND status=?`,
		string(StatusApproved), now.Format(time.RFC3339Nano), proposal.ID, proposal.ProjectID, string(StatusProposed))
	if err != nil {
		return Proposal{}, blackboardv2.ChangeResult{}, err
	}
	changed, _ := update.RowsAffected()
	if changed != 1 {
		return Proposal{}, blackboardv2.ChangeResult{}, ErrNotProposed
	}
	proposal.Status, proposal.DecidedAt = StatusApproved, &now
	return proposal, result, nil
}

func (service *Service) Reject(projectID, proposalID string) (Proposal, error) {
	proposal, err := service.load(projectID, proposalID)
	if err != nil {
		return Proposal{}, err
	}
	if proposal.Status != StatusProposed {
		return Proposal{}, ErrNotProposed
	}
	now := time.Now().UTC()
	result, err := service.db.Exec(`UPDATE reason_task_proposals SET status=?,decided_at=? WHERE id=? AND project_id=? AND status=?`,
		string(StatusRejected), now.Format(time.RFC3339Nano), proposal.ID, proposal.ProjectID, string(StatusProposed))
	if err != nil {
		return Proposal{}, err
	}
	changed, _ := result.RowsAffected()
	if changed != 1 {
		return Proposal{}, ErrNotProposed
	}
	proposal.Status, proposal.DecidedAt = StatusRejected, &now
	return proposal, nil
}

func (service *Service) load(projectID, proposalID string) (Proposal, error) {
	proposal, err := scanProposal(service.db.QueryRow(`SELECT id,project_id,reason_task_id,proposal_json,change_batch_json,status,created_at,decided_at
		FROM reason_task_proposals WHERE id=? AND project_id=?`, strings.TrimSpace(proposalID), strings.TrimSpace(projectID)))
	if errors.Is(err, sql.ErrNoRows) {
		return Proposal{}, ErrNotFound
	}
	return proposal, err
}

func scanProposal(row interface{ Scan(...any) error }) (Proposal, error) {
	var proposal Proposal
	var metadataJSON, changesJSON, status, createdAt string
	var decidedAt sql.NullString
	if err := row.Scan(&proposal.ID, &proposal.ProjectID, &proposal.ReasonTaskID, &metadataJSON, &changesJSON, &status, &createdAt, &decidedAt); err != nil {
		return Proposal{}, err
	}
	var metadata struct {
		NextTaskGoals               []string `json:"next_task_goals"`
		ExplorationObjectiveChanges []string `json:"exploration_objective_changes"`
		ReadinessJudgment           string   `json:"readiness_judgment"`
	}
	if err := json.Unmarshal([]byte(metadataJSON), &metadata); err != nil {
		return Proposal{}, err
	}
	if err := json.Unmarshal([]byte(changesJSON), &proposal.Changes); err != nil {
		return Proposal{}, err
	}
	proposal.NextTaskGoals, proposal.ExplorationObjectiveChanges = metadata.NextTaskGoals, metadata.ExplorationObjectiveChanges
	proposal.ReadinessJudgment, proposal.Status = metadata.ReadinessJudgment, Status(status)
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

func normalizeText(values []string) []string {
	result := []string{}
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			result = append(result, value)
		}
	}
	return result
}

func newID() string { return fmt.Sprintf("reason-proposal-%d", time.Now().UTC().UnixNano()) }
