package blackboardv2

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
)

// operatorAuthority is server-bound control data for authenticated operator
// Blackboard actions. It never enters a Runtime request or semantic record.
type operatorAuthority struct {
	actorID              string
	sourceTaskID         string
	sourceContinuationID string
}

type operatorAuthorityContextKey struct{}

func withOperatorAuthority(ctx context.Context, authority operatorAuthority) context.Context {
	return context.WithValue(ctx, operatorAuthorityContextKey{}, authority)
}

func operatorAuthorityFromContext(ctx context.Context) (operatorAuthority, bool) {
	authority, ok := ctx.Value(operatorAuthorityContextKey{}).(operatorAuthority)
	if !ok || strings.TrimSpace(authority.actorID) == "" {
		return operatorAuthority{}, false
	}
	return authority, true
}

// ApplyForOperator applies an authenticated operator reconciliation batch and
// records operator ownership for each newly created Attempt.
func (s *Service) ApplyForOperator(ctx context.Context, projectID, actorID string, batch ChangeBatch) (ChangeResult, error) {
	actorID = strings.TrimSpace(actorID)
	if actorID == "" {
		return ChangeResult{}, semanticError("authority_denied", "operator identity is required", "authorization", nil)
	}
	return s.Apply(withOperatorAuthority(ctx, operatorAuthority{actorID: actorID}), projectID, batch)
}

func bindOperatorAttemptOrigin(ctx context.Context, tx *sql.Tx, projectID, key, actorID, now string) error {
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO blackboard_v2_operator_attempt_origins (project_id, key, actor_id, created_at)
		VALUES (?, ?, ?, ?)`,
		projectID, key, actorID, now,
	); err != nil {
		return fmt.Errorf("bind operator Attempt trusted origin: %w", err)
	}
	return nil
}

func requireOperatorAttemptOwner(ctx context.Context, tx *sql.Tx, projectID, key, actorID, path string) error {
	var owner string
	err := tx.QueryRowContext(ctx, `
		SELECT actor_id
		FROM blackboard_v2_operator_attempt_origins
		WHERE project_id = ? AND key = ?`,
		projectID, key,
	).Scan(&owner)
	if errors.Is(err, sql.ErrNoRows) || (err == nil && owner != actorID) {
		return semanticError("authority_denied", "Attempt is owned by another trusted origin", path, map[string]any{"key": key})
	}
	if err != nil {
		return fmt.Errorf("read operator Attempt trusted origin: %w", err)
	}
	return nil
}

func recordOperatorEvidenceOrigin(ctx context.Context, tx *sql.Tx, projectID, key string, version int, authority operatorAuthority, sourcePath, now string) error {
	if version < 1 {
		return fmt.Errorf("operator Evidence version is required")
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO blackboard_v2_operator_evidence_origins (
			project_id, key, version, actor_id, source_task_id,
			source_continuation_id, source_path, created_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		projectID, key, version, authority.actorID, authority.sourceTaskID,
		authority.sourceContinuationID, sourcePath, now,
	); err != nil {
		return fmt.Errorf("record operator Evidence trusted origin: %w", err)
	}
	return nil
}
