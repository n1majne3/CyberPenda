// Package challengeworkflow owns idempotent Challenge Platform operations.
// It keeps platform protocol details behind PlatformAdapter and durable
// knowledge capture behind Recorder.
package challengeworkflow

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"pentest/internal/project"
	"pentest/internal/store"
	"pentest/internal/task"
)

const (
	PolicyMaxAttempts            = "max_attempts"
	PolicyMaxWrongSubmissions    = "max_wrong_submissions"
	PolicyMaxWallTime            = "max_wall_time"
	PolicyMaxConsecutiveFailures = "max_consecutive_failures"
	PolicyMaxRatingDrawdown      = "max_rating_drawdown"
	PolicyMaxNoProgress          = "max_no_progress"
)

var (
	ErrInvalidRequest          = errors.New("invalid Challenge Workflow request")
	ErrPlatformNotFound        = errors.New("Challenge Platform is not configured")
	ErrAttemptNotFound         = errors.New("Challenge Attempt not found")
	ErrOperationConflict       = errors.New("Challenge operation idempotency conflict")
	ErrOperationActionRequired = errors.New("Challenge operation needs operator action")
	ErrProjectKind             = errors.New("Challenge Workflow requires a CTF Challenge Project")
	ErrTaskType                = errors.New("Challenge Workflow requires a CTF Challenge Task")
	ErrAttemptNotOpen          = errors.New("Challenge Attempt is not open")
	ErrRecorderNotReady        = errors.New("Challenge Workflow Recorder is not configured")
)

// PolicyError reports one machine-enforced Task Policy stop condition.
type PolicyError struct {
	Code   string `json:"code"`
	Limit  int    `json:"limit"`
	Actual int    `json:"actual"`
}

func (err *PolicyError) Error() string {
	return fmt.Sprintf("Task Policy blocked Challenge Workflow: %s limit %d, actual %d", err.Code, err.Limit, err.Actual)
}

type PlatformClaimRequest struct {
	OperationID string `json:"operation_id"`
	ChallengeID string `json:"challenge_id"`
}
type PlatformClaimResponse struct {
	ExternalAttemptID string `json:"external_attempt_id"`
	ChallengeID       string `json:"challenge_id"`
	Summary           string `json:"summary"`
	Rating            int    `json:"rating"`
}
type PlatformSubmitRequest struct {
	OperationID       string `json:"operation_id"`
	ExternalAttemptID string `json:"external_attempt_id"`
	Candidate         string `json:"candidate"`
}
type PlatformSubmitResponse struct {
	Accepted bool   `json:"accepted"`
	Summary  string `json:"summary"`
	Rating   int    `json:"rating"`
}
type PlatformAbandonRequest struct {
	OperationID       string `json:"operation_id"`
	ExternalAttemptID string `json:"external_attempt_id"`
	Reason            string `json:"reason"`
}
type PlatformAbandonResponse struct {
	Summary string `json:"summary"`
	Rating  int    `json:"rating"`
}
type PlatformFinalizeRequest struct {
	OperationID       string `json:"operation_id"`
	ExternalAttemptID string `json:"external_attempt_id"`
}
type PlatformFinalizeResponse struct {
	Summary string `json:"summary"`
}

// PlatformAdapter is the only port that knows a Challenge Platform protocol.
// The operation ID must be forwarded so the platform can deduplicate retries.
type PlatformAdapter interface {
	Claim(context.Context, PlatformClaimRequest) (PlatformClaimResponse, error)
	Submit(context.Context, PlatformSubmitRequest) (PlatformSubmitResponse, error)
	Abandon(context.Context, PlatformAbandonRequest) (PlatformAbandonResponse, error)
	Finalize(context.Context, PlatformFinalizeRequest) (PlatformFinalizeResponse, error)
}

type ClaimRequest struct {
	ProjectID   string `json:"project_id"`
	TaskID      string `json:"task_id"`
	Platform    string `json:"platform"`
	OperationID string `json:"operation_id"`
	ChallengeID string `json:"challenge_id"`
}
type ClaimResult struct {
	ExternalAttemptID string `json:"external_attempt_id"`
	ChallengeID       string `json:"challenge_id"`
	AttemptKey        string `json:"attempt_key"`
	Summary           string `json:"summary"`
	Rating            int    `json:"rating"`
}
type SubmitRequest struct {
	ProjectID         string `json:"project_id"`
	TaskID            string `json:"task_id"`
	Platform          string `json:"platform"`
	OperationID       string `json:"operation_id"`
	ExternalAttemptID string `json:"external_attempt_id"`
	Candidate         string `json:"candidate"`
}
type SubmitResult struct {
	Accepted    bool   `json:"accepted"`
	AttemptKey  string `json:"attempt_key"`
	Summary     string `json:"summary"`
	Rating      int    `json:"rating"`
	EvidenceKey string `json:"evidence_key,omitempty"`
}
type AbandonRequest struct {
	ProjectID         string `json:"project_id"`
	TaskID            string `json:"task_id"`
	Platform          string `json:"platform"`
	OperationID       string `json:"operation_id"`
	ExternalAttemptID string `json:"external_attempt_id"`
	Reason            string `json:"reason"`
}
type AbandonResult struct {
	AttemptKey  string `json:"attempt_key"`
	Summary     string `json:"summary"`
	Rating      int    `json:"rating"`
	EvidenceKey string `json:"evidence_key,omitempty"`
}
type FinalizeRequest struct {
	ProjectID         string `json:"project_id"`
	TaskID            string `json:"task_id"`
	Platform          string `json:"platform"`
	OperationID       string `json:"operation_id"`
	ExternalAttemptID string `json:"external_attempt_id"`
}
type FinalizeResult struct {
	AttemptKey  string `json:"attempt_key"`
	Summary     string `json:"summary"`
	EvidenceKey string `json:"evidence_key,omitempty"`
}

type RecordClaimRequest struct {
	ProjectID, TaskID, Platform, OperationID, ChallengeID, ExternalAttemptID, AttemptKey, Summary string
	Rating                                                                                        int
}
type RecordSubmissionRequest struct {
	ProjectID, TaskID, Platform, OperationID, ExternalAttemptID, AttemptKey, Candidate, Summary string
	Accepted                                                                                    bool
	Rating                                                                                      int
}
type RecordAbandonRequest struct {
	ProjectID, TaskID, Platform, OperationID, ExternalAttemptID, AttemptKey, Reason, Summary string
	Rating                                                                                   int
}
type RecordFinalizeRequest struct{ ProjectID, TaskID, Platform, OperationID, ExternalAttemptID, AttemptKey, Summary string }

// Recorder projects a completed platform operation into Blackboard and retains
// the exact redacted response as Evidence. Its methods must be idempotent.
type Recorder interface {
	RecordClaim(context.Context, RecordClaimRequest) error
	RecordSubmission(context.Context, RecordSubmissionRequest) error
	RecordAbandon(context.Context, RecordAbandonRequest) error
	RecordFinalize(context.Context, RecordFinalizeRequest) error
}

type Service struct {
	db        *store.DB
	projects  *project.Service
	tasks     *task.Service
	platforms map[string]PlatformAdapter
	recorder  Recorder
	now       func() time.Time
}

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

type RecoveryFailure struct {
	TaskID      string `json:"task_id"`
	OperationID string `json:"operation_id"`
	Kind        string `json:"kind"`
	Error       string `json:"error"`
}

// Recover resumes every non-completed operation from its durable request. It
// never returns candidate values in diagnostics.
func (service *Service) Recover(ctx context.Context) []RecoveryFailure {
	rows, err := service.db.QueryContext(ctx, `SELECT task_id,operation_id,kind,request_json FROM challenge_operations WHERE state IN ('pending','recording') ORDER BY created_at`)
	if err != nil {
		return []RecoveryFailure{{Error: err.Error()}}
	}
	defer rows.Close()
	type pending struct{ taskID, operationID, kind, raw string }
	items := []pending{}
	for rows.Next() {
		var item pending
		if err := rows.Scan(&item.taskID, &item.operationID, &item.kind, &item.raw); err != nil {
			return []RecoveryFailure{{Error: err.Error()}}
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return []RecoveryFailure{{Error: err.Error()}}
	}
	failures := []RecoveryFailure{}
	for _, item := range items {
		if ctx.Err() != nil {
			failures = append(failures, RecoveryFailure{TaskID: item.taskID, OperationID: item.operationID, Kind: item.kind, Error: ctx.Err().Error()})
			break
		}
		var operationErr error
		switch item.kind {
		case "claim":
			var request ClaimRequest
			operationErr = json.Unmarshal([]byte(item.raw), &request)
			if operationErr == nil {
				_, operationErr = service.Claim(ctx, request)
			}
		case "submit":
			var request SubmitRequest
			operationErr = json.Unmarshal([]byte(item.raw), &request)
			if operationErr == nil {
				_, operationErr = service.Submit(ctx, request)
			}
		case "abandon":
			var request AbandonRequest
			operationErr = json.Unmarshal([]byte(item.raw), &request)
			if operationErr == nil {
				_, operationErr = service.Abandon(ctx, request)
			}
		case "finalize":
			var request FinalizeRequest
			operationErr = json.Unmarshal([]byte(item.raw), &request)
			if operationErr == nil {
				_, operationErr = service.Finalize(ctx, request)
			}
		default:
			operationErr = fmt.Errorf("unknown Challenge operation kind %q", item.kind)
		}
		if operationErr != nil {
			if settleErr := service.requireRecoveryAction(item.taskID, item.operationID); settleErr != nil {
				operationErr = fmt.Errorf("%v; settle recovery: %w", operationErr, settleErr)
			}
			failures = append(failures, RecoveryFailure{TaskID: item.taskID, OperationID: item.operationID, Kind: item.kind, Error: operationErr.Error()})
		}
	}
	return failures
}

func (service *Service) requireRecoveryAction(taskID, operationID string) error {
	stamp := service.now().Format(time.RFC3339Nano)
	result, err := service.db.Exec(`UPDATE challenge_operations SET state='action_required',recovery_error='automatic recovery failed',updated_at=? WHERE task_id=? AND operation_id=? AND state IN ('pending','recording')`, stamp, taskID, operationID)
	if err != nil {
		return err
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if changed == 0 {
		return fmt.Errorf("Challenge operation was not recoverable")
	}
	service.event(taskID, "recovery", operationID, "", false)
	return nil
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

func NewService(db *store.DB, projects *project.Service, tasks *task.Service, platforms map[string]PlatformAdapter, recorder Recorder) *Service {
	return &Service{db: db, projects: projects, tasks: tasks, platforms: platforms, recorder: recorder, now: func() time.Time { return time.Now().UTC() }}
}

func (service *Service) Claim(ctx context.Context, request ClaimRequest) (ClaimResult, error) {
	var result ClaimResult
	taskValue, adapter, err := service.prepare(request.ProjectID, request.TaskID, request.Platform, request.OperationID)
	if err != nil {
		return result, err
	}
	if strings.TrimSpace(request.ChallengeID) == "" {
		return result, ErrInvalidRequest
	}
	if replay, found, err := loadReplay[ClaimResult](service.db, request.TaskID, request.OperationID, hashRequest("claim", request)); err != nil || found {
		return replay, err
	}
	known, err := service.operationKnown(request.TaskID, request.OperationID, hashRequest("claim", request))
	if err != nil {
		return result, err
	}
	if !known {
		if err := service.checkClaimPolicy(taskValue); err != nil {
			return result, err
		}
	}
	if err := service.reserve(request.ProjectID, request.TaskID, request.Platform, request.OperationID, "claim", hashRequest("claim", request), "", request); err != nil {
		return result, err
	}

	platformResult, err := adapter.Claim(ctx, PlatformClaimRequest{OperationID: request.OperationID, ChallengeID: request.ChallengeID})
	if err != nil {
		return result, err
	}
	if strings.TrimSpace(platformResult.ExternalAttemptID) == "" {
		return result, fmt.Errorf("%w: platform returned no external Attempt identity", ErrInvalidRequest)
	}
	result = ClaimResult{ExternalAttemptID: platformResult.ExternalAttemptID, ChallengeID: platformResult.ChallengeID, AttemptKey: stableAttemptKey(request.Platform, platformResult.ExternalAttemptID), Summary: platformResult.Summary, Rating: platformResult.Rating}
	if result.ChallengeID == "" {
		result.ChallengeID = request.ChallengeID
	}
	if err := service.saveRecording(request.TaskID, request.OperationID, result.ExternalAttemptID, result); err != nil {
		return ClaimResult{}, err
	}
	stamp := service.now().Format(time.RFC3339Nano)
	_, err = service.db.Exec(`INSERT OR IGNORE INTO challenge_attempts (project_id,task_id,platform,external_attempt_id,challenge_id,attempt_key,objective_key,status,initial_rating,peak_rating,current_rating,last_progress_at,created_at,updated_at) VALUES (?,?,?,?,?,?,?, 'open',?,?,?,?,?,?)`, request.ProjectID, request.TaskID, request.Platform, result.ExternalAttemptID, result.ChallengeID, result.AttemptKey, objectiveKey(request.Platform, result.ExternalAttemptID), result.Rating, result.Rating, result.Rating, stamp, stamp, stamp)
	if err != nil {
		return ClaimResult{}, fmt.Errorf("store Challenge Attempt: %w", err)
	}
	if service.recorder == nil {
		return ClaimResult{}, ErrRecorderNotReady
	}
	if err := service.recorder.RecordClaim(ctx, RecordClaimRequest{ProjectID: request.ProjectID, TaskID: request.TaskID, Platform: request.Platform, OperationID: request.OperationID, ChallengeID: result.ChallengeID, ExternalAttemptID: result.ExternalAttemptID, AttemptKey: result.AttemptKey, Summary: result.Summary, Rating: result.Rating}); err != nil {
		return ClaimResult{}, err
	}
	if err := service.complete(request.TaskID, request.OperationID, evidenceKey(request.Platform, result.ExternalAttemptID, request.OperationID)); err != nil {
		return ClaimResult{}, err
	}
	service.event(request.TaskID, "claim", request.OperationID, result.AttemptKey, true)
	return result, nil
}

func (service *Service) Submit(ctx context.Context, request SubmitRequest) (SubmitResult, error) {
	var result SubmitResult
	taskValue, adapter, err := service.prepare(request.ProjectID, request.TaskID, request.Platform, request.OperationID)
	if err != nil {
		return result, err
	}
	if request.ExternalAttemptID == "" || request.Candidate == "" {
		return result, ErrInvalidRequest
	}
	hash := hashRequest("submit", request)
	if replay, found, err := loadReplay[SubmitResult](service.db, request.TaskID, request.OperationID, hash); err != nil || found {
		return replay, err
	}
	known, err := service.operationKnown(request.TaskID, request.OperationID, hash)
	if err != nil {
		return result, err
	}
	attempt, err := service.openAttempt(request.ProjectID, request.TaskID, request.Platform, request.ExternalAttemptID)
	if err != nil {
		return result, err
	}
	if !known {
		if err := service.checkOperationPolicy(taskValue, attempt); err != nil {
			return result, err
		}
	}
	if err := service.reserve(request.ProjectID, request.TaskID, request.Platform, request.OperationID, "submit", hash, request.ExternalAttemptID, request); err != nil {
		return result, err
	}
	platformResult, err := adapter.Submit(ctx, PlatformSubmitRequest{OperationID: request.OperationID, ExternalAttemptID: request.ExternalAttemptID, Candidate: request.Candidate})
	if err != nil {
		return result, err
	}
	result = SubmitResult{Accepted: platformResult.Accepted, AttemptKey: attempt.AttemptKey, Summary: platformResult.Summary, Rating: platformResult.Rating, EvidenceKey: evidenceKey(request.Platform, request.ExternalAttemptID, request.OperationID)}
	if err := service.saveRecording(request.TaskID, request.OperationID, request.ExternalAttemptID, result); err != nil {
		return SubmitResult{}, err
	}
	if service.recorder == nil {
		return SubmitResult{}, ErrRecorderNotReady
	}
	if err := service.recorder.RecordSubmission(ctx, RecordSubmissionRequest{ProjectID: request.ProjectID, TaskID: request.TaskID, Platform: request.Platform, OperationID: request.OperationID, ExternalAttemptID: request.ExternalAttemptID, AttemptKey: attempt.AttemptKey, Candidate: request.Candidate, Summary: result.Summary, Accepted: result.Accepted, Rating: result.Rating}); err != nil {
		return SubmitResult{}, err
	}
	stamp := service.now().Format(time.RFC3339Nano)
	status := "open"
	wrongIncrement, failureValue := 1, attempt.ConsecutiveFailures+1
	progress := attempt.LastProgressAt
	if result.Accepted {
		status, wrongIncrement, failureValue, progress = "succeeded", 0, 0, stamp
	}
	peak := attempt.PeakRating
	if result.Rating > peak {
		peak = result.Rating
	}
	if err := service.completeSubmit(ctx, request, result, status, wrongIncrement, failureValue, peak, progress, stamp); err != nil {
		return SubmitResult{}, err
	}
	service.event(request.TaskID, "submit", request.OperationID, result.AttemptKey, result.Accepted)
	return result, nil
}

func (service *Service) Abandon(ctx context.Context, request AbandonRequest) (AbandonResult, error) {
	var result AbandonResult
	_, adapter, err := service.prepare(request.ProjectID, request.TaskID, request.Platform, request.OperationID)
	if err != nil {
		return result, err
	}
	hash := hashRequest("abandon", request)
	if replay, found, err := loadReplay[AbandonResult](service.db, request.TaskID, request.OperationID, hash); err != nil || found {
		return replay, err
	}
	attempt, err := service.openAttempt(request.ProjectID, request.TaskID, request.Platform, request.ExternalAttemptID)
	if err != nil {
		return result, err
	}
	if err := service.reserve(request.ProjectID, request.TaskID, request.Platform, request.OperationID, "abandon", hash, request.ExternalAttemptID, request); err != nil {
		return result, err
	}
	platformResult, err := adapter.Abandon(ctx, PlatformAbandonRequest{OperationID: request.OperationID, ExternalAttemptID: request.ExternalAttemptID, Reason: request.Reason})
	if err != nil {
		return result, err
	}
	result = AbandonResult{AttemptKey: attempt.AttemptKey, Summary: platformResult.Summary, Rating: platformResult.Rating, EvidenceKey: evidenceKey(request.Platform, request.ExternalAttemptID, request.OperationID)}
	if err := service.saveRecording(request.TaskID, request.OperationID, request.ExternalAttemptID, result); err != nil {
		return result, err
	}
	if service.recorder == nil {
		return result, ErrRecorderNotReady
	}
	if err := service.recorder.RecordAbandon(ctx, RecordAbandonRequest{ProjectID: request.ProjectID, TaskID: request.TaskID, Platform: request.Platform, OperationID: request.OperationID, ExternalAttemptID: request.ExternalAttemptID, AttemptKey: attempt.AttemptKey, Reason: request.Reason, Summary: result.Summary, Rating: result.Rating}); err != nil {
		return result, err
	}
	stamp := service.now().Format(time.RFC3339Nano)
	if err := service.completeAbandon(ctx, request, result, stamp); err != nil {
		return result, err
	}
	service.event(request.TaskID, "abandon", request.OperationID, result.AttemptKey, true)
	return result, nil
}

func (service *Service) Finalize(ctx context.Context, request FinalizeRequest) (FinalizeResult, error) {
	var result FinalizeResult
	_, adapter, err := service.prepare(request.ProjectID, request.TaskID, request.Platform, request.OperationID)
	if err != nil {
		return result, err
	}
	hash := hashRequest("finalize", request)
	if replay, found, err := loadReplay[FinalizeResult](service.db, request.TaskID, request.OperationID, hash); err != nil || found {
		return replay, err
	}
	attempt, err := service.attempt(request.ProjectID, request.TaskID, request.Platform, request.ExternalAttemptID)
	if err != nil {
		return result, err
	}
	if err := service.reserve(request.ProjectID, request.TaskID, request.Platform, request.OperationID, "finalize", hash, request.ExternalAttemptID, request); err != nil {
		return result, err
	}
	platformResult, err := adapter.Finalize(ctx, PlatformFinalizeRequest{OperationID: request.OperationID, ExternalAttemptID: request.ExternalAttemptID})
	if err != nil {
		return result, err
	}
	result = FinalizeResult{AttemptKey: attempt.AttemptKey, Summary: platformResult.Summary}
	if err := service.saveRecording(request.TaskID, request.OperationID, request.ExternalAttemptID, result); err != nil {
		return result, err
	}
	if service.recorder == nil {
		return result, ErrRecorderNotReady
	}
	if err := service.recorder.RecordFinalize(ctx, RecordFinalizeRequest{ProjectID: request.ProjectID, TaskID: request.TaskID, Platform: request.Platform, OperationID: request.OperationID, ExternalAttemptID: request.ExternalAttemptID, AttemptKey: attempt.AttemptKey, Summary: result.Summary}); err != nil {
		return result, err
	}
	if err := service.completeFinalize(ctx, request, result); err != nil {
		return result, err
	}
	service.event(request.TaskID, "finalize", request.OperationID, result.AttemptKey, true)
	return result, nil
}

type attemptState struct {
	AttemptKey, Status, LastProgressAt                               string
	WrongSubmissions, ConsecutiveFailures, PeakRating, CurrentRating int
}

func (service *Service) prepare(projectID, taskID, platform, operationID string) (task.Task, PlatformAdapter, error) {
	if projectID == "" || taskID == "" || platform == "" || operationID == "" {
		return task.Task{}, nil, ErrInvalidRequest
	}
	proj, err := service.projects.Get(projectID)
	if err != nil {
		return task.Task{}, nil, err
	}
	if proj.Kind != project.KindCTFChallenge {
		return task.Task{}, nil, ErrProjectKind
	}
	taskValue, err := service.tasks.Get(taskID)
	if err != nil {
		return task.Task{}, nil, err
	}
	if taskValue.ProjectID != projectID {
		return task.Task{}, nil, task.ErrNotFound
	}
	if taskValue.Type != task.TypeCTFChallenge {
		return task.Task{}, nil, ErrTaskType
	}
	adapter := service.platforms[platform]
	if adapter == nil {
		return task.Task{}, nil, ErrPlatformNotFound
	}
	return taskValue, adapter, nil
}

func (service *Service) checkClaimPolicy(taskValue task.Task) error {
	policy := taskValue.RunControls.Policy
	var count int
	if err := service.db.QueryRow(`SELECT COUNT(*) FROM challenge_attempts WHERE task_id=?`, taskValue.ID).Scan(&count); err != nil {
		return err
	}
	if policy.MaxAttempts > 0 && count >= policy.MaxAttempts {
		return service.policyBlocked(taskValue.ID, PolicyMaxAttempts, policy.MaxAttempts, count)
	}
	return service.checkTimePolicy(taskValue, "")
}

func (service *Service) checkOperationPolicy(taskValue task.Task, attempt attemptState) error {
	policy := taskValue.RunControls.Policy
	if policy.MaxWrongSubmissions > 0 && attempt.WrongSubmissions >= policy.MaxWrongSubmissions {
		return service.policyBlocked(taskValue.ID, PolicyMaxWrongSubmissions, policy.MaxWrongSubmissions, attempt.WrongSubmissions)
	}
	if policy.MaxConsecutiveFailures > 0 && attempt.ConsecutiveFailures >= policy.MaxConsecutiveFailures {
		return service.policyBlocked(taskValue.ID, PolicyMaxConsecutiveFailures, policy.MaxConsecutiveFailures, attempt.ConsecutiveFailures)
	}
	drawdown := attempt.PeakRating - attempt.CurrentRating
	if policy.MaxRatingDrawdown > 0 && drawdown >= policy.MaxRatingDrawdown {
		return service.policyBlocked(taskValue.ID, PolicyMaxRatingDrawdown, policy.MaxRatingDrawdown, drawdown)
	}
	return service.checkTimePolicy(taskValue, attempt.LastProgressAt)
}

func (service *Service) checkTimePolicy(taskValue task.Task, lastProgress string) error {
	policy, now := taskValue.RunControls.Policy, service.now()
	wall := int(now.Sub(taskValue.CreatedAt).Seconds())
	if policy.MaxWallTimeSeconds > 0 && wall >= policy.MaxWallTimeSeconds {
		return service.policyBlocked(taskValue.ID, PolicyMaxWallTime, policy.MaxWallTimeSeconds, wall)
	}
	if policy.MaxNoProgressSeconds > 0 && lastProgress != "" {
		stamp, err := time.Parse(time.RFC3339Nano, lastProgress)
		if err == nil {
			elapsed := int(now.Sub(stamp).Seconds())
			if elapsed >= policy.MaxNoProgressSeconds {
				return service.policyBlocked(taskValue.ID, PolicyMaxNoProgress, policy.MaxNoProgressSeconds, elapsed)
			}
		}
	}
	return nil
}

func (service *Service) policyBlocked(taskID, code string, limit, actual int) error {
	_, _ = service.tasks.AppendEvent(taskID, task.EventKindLifecycle, task.EventPayload{"event": "task_policy_blocked", "code": code, "limit": limit, "actual": actual})
	return &PolicyError{Code: code, Limit: limit, Actual: actual}
}

func (service *Service) attempt(projectID, taskID, platform, externalID string) (attemptState, error) {
	var found attemptState
	err := service.db.QueryRow(`SELECT attempt_key,status,wrong_submissions,consecutive_failures,peak_rating,current_rating,last_progress_at FROM challenge_attempts WHERE project_id=? AND task_id=? AND platform=? AND external_attempt_id=?`, projectID, taskID, platform, externalID).Scan(&found.AttemptKey, &found.Status, &found.WrongSubmissions, &found.ConsecutiveFailures, &found.PeakRating, &found.CurrentRating, &found.LastProgressAt)
	if errors.Is(err, sql.ErrNoRows) {
		return found, ErrAttemptNotFound
	}
	return found, err
}
func (service *Service) openAttempt(projectID, taskID, platform, externalID string) (attemptState, error) {
	found, err := service.attempt(projectID, taskID, platform, externalID)
	if err == nil && found.Status != "open" {
		err = ErrAttemptNotOpen
	}
	return found, err
}

func (service *Service) reserve(projectID, taskID, platform, operationID, kind, hash, externalID string, request any) error {
	stamp := service.now().Format(time.RFC3339Nano)
	requestJSON, err := json.Marshal(request)
	if err != nil {
		return fmt.Errorf("encode Challenge operation request: %w", err)
	}
	_, err = service.db.Exec(`INSERT INTO challenge_operations (task_id,operation_id,project_id,platform,kind,request_hash,request_json,state,external_attempt_id,created_at,updated_at) VALUES (?,?,?,?,?,?,?,'pending',?,?,?)`, taskID, operationID, projectID, platform, kind, hash, string(requestJSON), externalID, stamp, stamp)
	if err != nil {
		known, knownErr := service.operationKnown(taskID, operationID, hash)
		if knownErr == nil && known {
			return nil
		}
		return fmt.Errorf("reserve Challenge operation: %w", err)
	}
	return nil
}

func (service *Service) operationKnown(taskID, operationID, hash string) (bool, error) {
	var storedHash string
	err := service.db.QueryRow(`SELECT request_hash FROM challenge_operations WHERE task_id=? AND operation_id=?`, taskID, operationID).Scan(&storedHash)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if storedHash != hash {
		return false, ErrOperationConflict
	}
	return true, nil
}
func (service *Service) saveRecording(taskID, operationID, externalID string, value any) error {
	raw, err := json.Marshal(value)
	if err != nil {
		return err
	}
	_, err = service.db.Exec(`UPDATE challenge_operations SET state='recording',external_attempt_id=?,response_json=?,updated_at=? WHERE task_id=? AND operation_id=?`, externalID, string(raw), service.now().Format(time.RFC3339Nano), taskID, operationID)
	return err
}
func (service *Service) complete(taskID, operationID, evidence string) error {
	_, err := service.db.Exec(`UPDATE challenge_operations SET state='completed',request_json='{}',evidence_key=?,updated_at=? WHERE task_id=? AND operation_id=?`, evidence, service.now().Format(time.RFC3339Nano), taskID, operationID)
	return err
}

func (service *Service) completeSubmit(ctx context.Context, request SubmitRequest, result SubmitResult, status string, wrongIncrement, failureValue, peak int, progress, stamp string) error {
	tx, err := service.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, `UPDATE challenge_attempts SET status=?, wrong_submissions=wrong_submissions+?, consecutive_failures=?, peak_rating=?, current_rating=?, last_progress_at=?, updated_at=? WHERE project_id=? AND platform=? AND external_attempt_id=?`, status, wrongIncrement, failureValue, peak, result.Rating, progress, stamp, request.ProjectID, request.Platform, request.ExternalAttemptID); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE challenge_operations SET state='completed',request_json='{}',evidence_key=?,updated_at=? WHERE task_id=? AND operation_id=?`, result.EvidenceKey, stamp, request.TaskID, request.OperationID); err != nil {
		return err
	}
	return tx.Commit()
}

func (service *Service) completeAbandon(ctx context.Context, request AbandonRequest, result AbandonResult, stamp string) error {
	tx, err := service.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, `UPDATE challenge_attempts SET status='failed', current_rating=?, consecutive_failures=consecutive_failures+1, updated_at=? WHERE project_id=? AND platform=? AND external_attempt_id=?`, result.Rating, stamp, request.ProjectID, request.Platform, request.ExternalAttemptID); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE challenge_operations SET state='completed',request_json='{}',evidence_key=?,updated_at=? WHERE task_id=? AND operation_id=?`, result.EvidenceKey, stamp, request.TaskID, request.OperationID); err != nil {
		return err
	}
	return tx.Commit()
}

func (service *Service) completeFinalize(ctx context.Context, request FinalizeRequest, result FinalizeResult) error {
	stamp := service.now().Format(time.RFC3339Nano)
	tx, err := service.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, `UPDATE challenge_attempts SET status='finalized', updated_at=? WHERE project_id=? AND platform=? AND external_attempt_id=?`, stamp, request.ProjectID, request.Platform, request.ExternalAttemptID); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE challenge_operations SET state='completed',request_json='{}',evidence_key=?,updated_at=? WHERE task_id=? AND operation_id=?`, result.EvidenceKey, stamp, request.TaskID, request.OperationID); err != nil {
		return err
	}
	return tx.Commit()
}
func (service *Service) event(taskID, operation, operationID, attemptKey string, success bool) {
	_, _ = service.tasks.AppendEvent(taskID, task.EventKindLifecycle, task.EventPayload{"event": "challenge_operation", "operation": operation, "operation_id": operationID, "attempt_key": attemptKey, "success": success})
}

func loadReplay[T any](db *store.DB, taskID, operationID, hash string) (T, bool, error) {
	var zero T
	var storedHash, state, raw string
	err := db.QueryRow(`SELECT request_hash,state,response_json FROM challenge_operations WHERE task_id=? AND operation_id=?`, taskID, operationID).Scan(&storedHash, &state, &raw)
	if errors.Is(err, sql.ErrNoRows) {
		return zero, false, nil
	}
	if err != nil {
		return zero, false, err
	}
	if storedHash != hash {
		return zero, false, ErrOperationConflict
	}
	if state == "action_required" {
		return zero, false, ErrOperationActionRequired
	}
	if state != "completed" {
		return zero, false, nil
	}
	if err := json.Unmarshal([]byte(raw), &zero); err != nil {
		return zero, false, err
	}
	return zero, true, nil
}

func hashRequest(kind string, value any) string {
	raw, _ := json.Marshal(struct {
		Kind  string `json:"kind"`
		Value any    `json:"value"`
	}{kind, value})
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}
func stableAttemptKey(platform, externalID string) string {
	return boundedKey("attempt/" + sanitize(platform) + "/" + sanitize(externalID))
}
func evidenceKey(platform, externalID, operationID string) string {
	return boundedKey("evidence/" + sanitize(platform) + "/" + sanitize(externalID) + "/" + sanitize(operationID))
}
func sanitize(value string) string {
	return strings.NewReplacer("/", "-", " ", "-", ":", "-").Replace(strings.TrimSpace(value))
}
