// Package challengeworkflow provides read-only history for the retired workflow.
package challengeworkflow

import (
	"context"
	"pentest/internal/store"
	"pentest/internal/task"
)

type Service struct {
	db    *store.DB
	tasks *task.Service
}

func NewService(db *store.DB, tasks *task.Service) *Service { return &Service{db: db, tasks: tasks} }

type Attempt struct {
	ProjectID           string `json:"project_id"`
	TaskID              string `json:"task_id"`
	Platform            string `json:"platform"`
	ExternalAttemptID   string `json:"external_attempt_id"`
	ChallengeID         string `json:"challenge_id"`
	AttemptKey          string `json:"attempt_key"`
	ObjectiveKey        string `json:"objective_key"`
	Status              string `json:"status"`
	WrongSubmissions    int    `json:"wrong_submissions"`
	ConsecutiveFailures int    `json:"consecutive_failures"`
	InitialRating       int    `json:"initial_rating"`
	PeakRating          int    `json:"peak_rating"`
	CurrentRating       int    `json:"current_rating"`
	LastProgressAt      string `json:"last_progress_at"`
	CreatedAt           string `json:"created_at"`
	UpdatedAt           string `json:"updated_at"`
}

func (service *Service) ListAttempts(ctx context.Context, projectID, taskID string) ([]Attempt, error) {
	found, err := service.tasks.Get(taskID)
	if err != nil {
		return nil, err
	}
	if found.ProjectID != projectID {
		return nil, task.ErrNotFound
	}
	rows, err := service.db.QueryContext(ctx, `SELECT project_id,task_id,platform,external_attempt_id,challenge_id,attempt_key,objective_key,status,wrong_submissions,consecutive_failures,initial_rating,peak_rating,current_rating,last_progress_at,created_at,updated_at FROM challenge_attempts WHERE project_id=? AND task_id=? ORDER BY created_at DESC`, projectID, taskID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []Attempt{}
	for rows.Next() {
		var item Attempt
		if err := rows.Scan(&item.ProjectID, &item.TaskID, &item.Platform, &item.ExternalAttemptID, &item.ChallengeID, &item.AttemptKey, &item.ObjectiveKey, &item.Status, &item.WrongSubmissions, &item.ConsecutiveFailures, &item.InitialRating, &item.PeakRating, &item.CurrentRating, &item.LastProgressAt, &item.CreatedAt, &item.UpdatedAt); err != nil {
			return nil, err
		}
		result = append(result, item)
	}
	return result, rows.Err()
}

// Operation exposes metadata only. Request and response payloads may contain candidates.
type Operation struct {
	OperationID       string `json:"operation_id"`
	Platform          string `json:"platform"`
	Kind              string `json:"kind"`
	State             string `json:"state"`
	ExternalAttemptID string `json:"external_attempt_id"`
	EvidenceKey       string `json:"evidence_key,omitempty"`
	CreatedAt         string `json:"created_at"`
	UpdatedAt         string `json:"updated_at"`
}

func (service *Service) HasHistory(ctx context.Context, taskID string) (bool, error) {
	var exists bool
	err := service.db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM challenge_attempts WHERE task_id=?) OR EXISTS(SELECT 1 FROM challenge_operations WHERE task_id=?)`, taskID, taskID).Scan(&exists)
	return exists, err
}

func (service *Service) ListOperations(ctx context.Context, projectID, taskID string) ([]Operation, error) {
	found, err := service.tasks.Get(taskID)
	if err != nil {
		return nil, err
	}
	if found.ProjectID != projectID {
		return nil, task.ErrNotFound
	}
	rows, err := service.db.QueryContext(ctx, `SELECT operation_id,platform,kind,state,external_attempt_id,evidence_key,created_at,updated_at FROM challenge_operations WHERE project_id=? AND task_id=? ORDER BY created_at DESC,operation_id`, projectID, taskID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []Operation{}
	for rows.Next() {
		var item Operation
		if err := rows.Scan(&item.OperationID, &item.Platform, &item.Kind, &item.State, &item.ExternalAttemptID, &item.EvidenceKey, &item.CreatedAt, &item.UpdatedAt); err != nil {
			return nil, err
		}
		result = append(result, item)
	}
	return result, rows.Err()
}
