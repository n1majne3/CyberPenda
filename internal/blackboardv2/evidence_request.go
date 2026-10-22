package blackboardv2

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
)

func (s *Service) validateRetainedEvidencePreconditions(ctx context.Context, projectID, continuationID string, request RetainEvidenceRequest) error {
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return fmt.Errorf("begin producing Attempt validation: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if err := ensureProjectExists(ctx, tx, projectID); err != nil {
		return err
	}
	attempt, err := loadCurrentRecord(ctx, tx, projectID, request.Attempt)
	if errors.Is(err, sql.ErrNoRows) {
		return semanticError("semantic_validation", "producing Attempt must be current and open", "attempt", map[string]any{"key": request.Attempt})
	}
	if err != nil {
		return err
	}
	if attempt.typ != "attempt" {
		return semanticError("semantic_validation", "attempt must reference an Attempt", "attempt", nil)
	}
	if operator, ok := operatorAuthorityFromContext(ctx); ok {
		if err := requireOperatorAttemptOwner(ctx, tx, projectID, request.Attempt, operator.actorID, "attempt"); err != nil {
			return err
		}
	} else {
		if err := requireAttemptOwner(ctx, tx, projectID, request.Attempt, continuationID, "attempt"); err != nil {
			return err
		}
	}
	if attempt.record.attemptRecord().Status != "open" {
		return semanticError("semantic_validation", "producing Attempt must be current and open", "attempt", map[string]any{"key": request.Attempt})
	}
	existing, err := loadCurrentRecord(ctx, tx, projectID, request.Key)
	if errors.Is(err, sql.ErrNoRows) {
		if request.Version != 0 {
			return semanticError("not_found", fmt.Sprintf("%s was not found", request.Key), "key", map[string]any{"key": request.Key})
		}
		if used, err := historicalKeyExists(ctx, tx, projectID, request.Key); err != nil {
			return err
		} else if used {
			return semanticError("key_conflict", fmt.Sprintf("%s already exists in Semantic History", request.Key), "key", map[string]any{"key": request.Key})
		}
	} else {
		if err != nil {
			return err
		}
		if existing.typ != "evidence" {
			return semanticError("key_conflict", fmt.Sprintf("%s already exists", request.Key), "key", map[string]any{"key": request.Key})
		}
		if request.Version == 0 {
			return semanticError("semantic_validation", "current Evidence version is required for replacement", "version", nil)
		}
		if request.Version != existing.version {
			return semanticError("version_conflict", fmt.Sprintf("%s changed", request.Key), "version", map[string]any{"key": request.Key, "expected_version": float64(request.Version), "current_version": float64(existing.version), "next_action": "read_current_record"})
		}
	}
	for index, link := range request.Links {
		target, err := loadCurrentRecord(ctx, tx, projectID, link[1])
		if errors.Is(err, sql.ErrNoRows) {
			return semanticError("not_found", fmt.Sprintf("%s was not found", link[1]), fmt.Sprintf("links[%d][1]", index), map[string]any{"key": link[1]})
		}
		if err != nil {
			return err
		}
		if err := validateRelationshipEndpoint(link[0], "evidence", target.typ, fmt.Sprintf("links[%d][0]", index)); err != nil {
			return err
		}
	}
	return nil
}

func validateRetainEvidenceRequest(request RetainEvidenceRequest) error {
	if strings.TrimSpace(request.IdempotencyKey) == "" {
		return semanticError("semantic_validation", "idempotency_key is required", "idempotency_key", nil)
	}
	if err := validateKey(request.Key, "key"); err != nil {
		return err
	}
	if request.Version < 0 {
		return semanticError("semantic_validation", "version must be positive when provided", "version", nil)
	}
	if err := validateKey(request.Attempt, "attempt"); err != nil {
		return err
	}
	if strings.TrimSpace(request.SourcePath) == "" {
		return semanticError("semantic_validation", "source_path is required", "source_path", nil)
	}
	if err := validateConciseText(request.ArtifactType, "artifact_type"); err != nil {
		return err
	}
	if err := validateSemanticText(request.Summary, "summary"); err != nil {
		return err
	}
	if request.MediaType != "" {
		if err := validateConciseText(request.MediaType, "media_type"); err != nil {
			return err
		}
	}
	if request.CapturedAt != "" {
		if _, err := time.Parse(time.RFC3339, request.CapturedAt); err != nil {
			return semanticError("semantic_validation", "captured_at must be an RFC3339 timestamp", "captured_at", nil)
		}
	}
	for index, link := range request.Links {
		if link[0] != "evidences" && link[0] != "about" {
			return semanticError("semantic_validation", "Retain Evidence links may only be evidences or about", fmt.Sprintf("links[%d][0]", index), nil)
		}
		if err := validateKey(link[1], fmt.Sprintf("links[%d][1]", index)); err != nil {
			return err
		}
	}
	return nil
}

func retainedEvidenceRequestHash(request RetainEvidenceRequest) (string, error) {
	raw, err := json.Marshal(request)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:]), nil
}

func (s *Service) continuationTaskStatus(ctx context.Context, projectID, continuationID string) (string, string, error) {
	var taskID, status string
	err := s.db.QueryRowContext(ctx, `SELECT task.id, continuation.status FROM task_continuations AS continuation JOIN tasks AS task ON task.id=continuation.task_id WHERE continuation.id=? AND task.project_id=?`, continuationID, projectID).Scan(&taskID, &status)
	if errors.Is(err, sql.ErrNoRows) {
		return "", "", semanticError("authority_denied", "trusted Continuation does not own this Project interface", "", nil)
	}
	if err != nil {
		return "", "", fmt.Errorf("validate trusted Continuation Project: %w", err)
	}
	return taskID, status, nil
}
