package challengeworkflow

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"pentest/internal/blackboardv2"
	"pentest/internal/task"
)

// BlackboardRecorder converts Challenge Workflow results into one canonical
// Objective, Attempt, Solution, and Evidence graph. It never uses a temporary
// Runtime path as a semantic identity.
type BlackboardRecorder struct {
	blackboard  *blackboardv2.Service
	tasks       *task.Service
	runtimeRoot string
}

func NewBlackboardRecorder(blackboard *blackboardv2.Service, tasks *task.Service, runtimeRoot string) *BlackboardRecorder {
	return &BlackboardRecorder{blackboard: blackboard, tasks: tasks, runtimeRoot: runtimeRoot}
}

func (recorder *BlackboardRecorder) RecordClaim(ctx context.Context, request RecordClaimRequest) error {
	continuationID, err := recorder.continuationID(request.TaskID)
	if err != nil {
		return err
	}
	objectiveKey := objectiveKey(request.Platform, request.ExternalAttemptID)
	_, err = recorder.blackboard.ApplyForContinuation(ctx, request.ProjectID, continuationID, blackboardv2.ChangeBatch{
		Schema:         "semantic-change-batch/v2",
		IdempotencyKey: "challenge-claim-" + request.OperationID,
		Changes: []blackboardv2.Change{
			{Op: "create", Key: objectiveKey, Type: "objective", Record: blackboardv2.ObjectiveRecord{Status: "open", Objective: "Solve Challenge " + request.ChallengeID}},
			{Op: "create", Key: request.AttemptKey, Type: "attempt", Record: blackboardv2.AttemptRecord{Status: "open", Summary: nonEmpty(request.Summary, "Claimed Challenge "+request.ChallengeID)}},
			{Op: "relate", From: request.AttemptKey, Relation: "tests", To: objectiveKey},
		},
	})
	if err != nil {
		return fmt.Errorf("record Challenge claim: %w", err)
	}
	payload := map[string]any{"external_attempt_id": request.ExternalAttemptID, "challenge_id": request.ChallengeID, "summary": request.Summary, "rating": request.Rating}
	return recorder.retain(ctx, request.ProjectID, request.TaskID, continuationID, request.Platform, request.ExternalAttemptID, request.OperationID, request.AttemptKey, "claim", payload, nil)
}

func (recorder *BlackboardRecorder) RecordSubmission(ctx context.Context, request RecordSubmissionRequest) error {
	continuationID, err := recorder.continuationID(request.TaskID)
	if err != nil {
		return err
	}
	payload := map[string]any{"accepted": request.Accepted, "summary": request.Summary, "rating": request.Rating}
	if !request.Accepted {
		return recorder.retain(ctx, request.ProjectID, request.TaskID, continuationID, request.Platform, request.ExternalAttemptID, request.OperationID, request.AttemptKey, "submit", payload, nil)
	}

	solutionKey := solutionKey(request.Platform, request.ExternalAttemptID)
	objective := objectiveKey(request.Platform, request.ExternalAttemptID)
	_, err = recorder.blackboard.ApplyForContinuation(ctx, request.ProjectID, continuationID, blackboardv2.ChangeBatch{
		Schema:         "semantic-change-batch/v2",
		IdempotencyKey: "challenge-solution-" + request.OperationID,
		Changes: []blackboardv2.Change{{
			Op: "create", Key: solutionKey, Type: "solution",
			Record: blackboardv2.SolutionRecord{Status: "verified", Kind: "flag", Summary: nonEmpty(request.Summary, "Challenge Platform accepted the candidate"), Value: request.Candidate, VerificationSummary: "Accepted by " + request.Platform},
		}},
	})
	if err != nil {
		return fmt.Errorf("record verified Solution: %w", err)
	}
	if err := recorder.retain(ctx, request.ProjectID, request.TaskID, continuationID, request.Platform, request.ExternalAttemptID, request.OperationID, request.AttemptKey, "submit", payload, []blackboardv2.EvidenceLink{{"evidences", solutionKey}}); err != nil {
		return err
	}
	_, err = recorder.blackboard.ApplyForContinuation(ctx, request.ProjectID, continuationID, blackboardv2.ChangeBatch{
		Schema:         "semantic-change-batch/v2",
		IdempotencyKey: "challenge-accept-" + request.OperationID,
		Changes: []blackboardv2.Change{
			{Op: "relate", From: request.AttemptKey, Relation: "produced", To: solutionKey},
			{Op: "relate", From: solutionKey, Relation: "satisfies", To: objective},
			{Op: "transition", Key: request.AttemptKey, Version: 1, Status: "succeeded", Summary: nonEmpty(request.Summary, "Challenge Platform accepted the candidate")},
			{Op: "transition", Key: objective, Version: 1, Status: "resolved", ResolutionSummary: "The Challenge Platform accepted the verified Solution"},
		},
	})
	if err != nil {
		return fmt.Errorf("finish accepted Challenge Attempt: %w", err)
	}
	return nil
}

func (recorder *BlackboardRecorder) RecordAbandon(ctx context.Context, request RecordAbandonRequest) error {
	continuationID, err := recorder.continuationID(request.TaskID)
	if err != nil {
		return err
	}
	payload := map[string]any{"summary": request.Summary, "rating": request.Rating, "reason": request.Reason}
	if err := recorder.retain(ctx, request.ProjectID, request.TaskID, continuationID, request.Platform, request.ExternalAttemptID, request.OperationID, request.AttemptKey, "abandon", payload, nil); err != nil {
		return err
	}
	objective := objectiveKey(request.Platform, request.ExternalAttemptID)
	_, err = recorder.blackboard.ApplyForContinuation(ctx, request.ProjectID, continuationID, blackboardv2.ChangeBatch{
		Schema:         "semantic-change-batch/v2",
		IdempotencyKey: "challenge-abandon-" + request.OperationID,
		Changes: []blackboardv2.Change{
			{Op: "transition", Key: request.AttemptKey, Version: 1, Status: "failed", Summary: nonEmpty(request.Summary, "Challenge Attempt was abandoned")},
			{Op: "transition", Key: objective, Version: 1, Status: "abandoned", ResolutionSummary: nonEmpty(request.Reason, "The operator abandoned the Challenge")},
		},
	})
	if err != nil {
		return fmt.Errorf("finish abandoned Challenge Attempt: %w", err)
	}
	return nil
}

func (recorder *BlackboardRecorder) RecordFinalize(_ context.Context, request RecordFinalizeRequest) error {
	// Submit or Abandon already retained the exact terminal response. Finalize
	// is a platform acknowledgement and does not invent a second Evidence item.
	return nil
}

func (recorder *BlackboardRecorder) continuationID(taskID string) (string, error) {
	continuation, err := recorder.tasks.LatestContinuation(taskID)
	if err != nil {
		return "", err
	}
	if continuation == nil {
		return "", fmt.Errorf("Challenge Workflow requires a Task Continuation")
	}
	return continuation.ID, nil
}

func (recorder *BlackboardRecorder) retain(ctx context.Context, projectID, taskID, continuationID, platform, externalID, operationID, attemptKey, operation string, payload any, links []blackboardv2.EvidenceLink) error {
	relative := filepath.Join("challenge-workflow", sanitize(operationID)+".json")
	workdir := filepath.Join(recorder.runtimeRoot, taskID, "workdir")
	absolute := filepath.Join(workdir, relative)
	if err := os.MkdirAll(filepath.Dir(absolute), 0o700); err != nil {
		return fmt.Errorf("create Challenge Evidence directory: %w", err)
	}
	raw, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		return fmt.Errorf("encode Challenge response: %w", err)
	}
	raw = append(raw, '\n')
	if err := os.WriteFile(absolute, raw, 0o600); err != nil {
		return fmt.Errorf("write Challenge response: %w", err)
	}
	_, err = recorder.blackboard.RetainEvidenceForContinuation(ctx, projectID, continuationID, blackboardv2.RetainEvidenceRequest{
		IdempotencyKey: "challenge-evidence-" + operationID,
		Key:            evidenceKey(platform, externalID, operationID),
		Attempt:        attemptKey,
		SourcePath:     relative,
		ArtifactType:   "api_response",
		Summary:        "Challenge Platform " + operation + " response",
		MediaType:      "application/json",
		Links:          links,
	})
	if err != nil {
		return fmt.Errorf("retain Challenge Evidence: %w", err)
	}
	return nil
}

func objectiveKey(platform, externalID string) string {
	return boundedKey("objective/" + sanitize(platform) + "/" + sanitize(externalID))
}
func solutionKey(platform, externalID string) string {
	return boundedKey("solution/" + sanitize(platform) + "/" + sanitize(externalID))
}
func nonEmpty(value, fallback string) string {
	if strings.TrimSpace(value) == "" {
		return fallback
	}
	return value
}

func boundedKey(value string) string {
	if len(value) <= 96 {
		return value
	}
	// Keep the readable prefix. The suffix preserves a stable identity.
	sum := sha256Text(value)
	return value[:79] + "-" + sum[:16]
}

func sha256Text(value string) string {
	// Keep hashing local to this adapter so semantic identities never depend on
	// a temporary path or a process-local random value.
	return fmt.Sprintf("%x", sha256Bytes([]byte(value)))
}

func sha256Bytes(value []byte) [32]byte {
	return sha256.Sum256(value)
}
