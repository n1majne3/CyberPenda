package blackboardv2

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"time"
)

// Apply atomically applies a semantic-change-batch/v2 to one Project.
func (s *Service) Apply(ctx context.Context, projectID string, batch ChangeBatch) (ChangeResult, error) {
	return s.apply(ctx, projectID, "", batch, nil)
}

// ApplyForContinuation applies a trusted Runtime batch using server-bound
// Continuation identity that never enters the semantic payload or result.
func (s *Service) ApplyForContinuation(ctx context.Context, projectID, continuationID string, batch ChangeBatch) (ChangeResult, error) {
	if continuationID == "" {
		return ChangeResult{}, semanticError("authority_denied", "trusted Continuation identity is required", "", nil)
	}
	return s.apply(ctx, projectID, continuationID, batch, nil)
}

// ApplyForContinuationAtRevision applies a trusted Runtime batch only when the
// current Project Blackboard still matches the supplied Runtime Snapshot. An
// exact idempotency replay is resolved before the revision precondition so a
// caller can recover an acknowledged commit after the revision advances.
func (s *Service) ApplyForContinuationAtRevision(ctx context.Context, projectID, continuationID string, expectedBaseRevision int, batch ChangeBatch) (ChangeResult, error) {
	if continuationID == "" {
		return ChangeResult{}, semanticError("authority_denied", "trusted Continuation identity is required", "", nil)
	}
	if expectedBaseRevision < 0 {
		return ChangeResult{}, semanticError("semantic_validation", "base_revision must not be negative", "base_revision", nil)
	}
	return s.apply(ctx, projectID, continuationID, batch, &expectedBaseRevision)
}

func (s *Service) apply(ctx context.Context, projectID, continuationID string, batch ChangeBatch, expectedBaseRevision *int) (ChangeResult, error) {
	s.writeMu.Lock()
	writeLocked := true
	defer func() {
		if writeLocked {
			s.writeMu.Unlock()
		}
	}()
	if err := validateChangeBatchDTOShape(batch); err != nil {
		return ChangeResult{}, err
	}
	operator, operatorRequest := operatorAuthorityFromContext(ctx)
	requestHash, err := canonicalRequestHash(batch)
	if err != nil {
		return ChangeResult{}, err
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return ChangeResult{}, fmt.Errorf("begin Blackboard v2 change: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	if err := ensureProjectState(ctx, tx, projectID); err != nil {
		return ChangeResult{}, err
	}
	continuationStatus := ""
	if continuationID != "" {
		continuationStatus, err = continuationProjectStatus(ctx, tx, projectID, continuationID)
		if err != nil {
			return ChangeResult{}, err
		}
	}
	if replay, ok, err := idempotencyReplay(ctx, tx, projectID, continuationID, batch.IdempotencyKey, requestHash); err != nil {
		return ChangeResult{}, err
	} else if ok {
		if err := tx.Commit(); err != nil {
			return ChangeResult{}, fmt.Errorf("commit Blackboard v2 replay: %w", err)
		}
		s.writeMu.Unlock()
		writeLocked = false
		if continuationID != "" {
			if err := s.rematerializeContinuationWorkingSnapshot(ctx, continuationID); err != nil {
				return ChangeResult{}, fmt.Errorf("recover acknowledged Working Blackboard Snapshot: %w", err)
			}
		}
		return replay, nil
	}
	if continuationID != "" && !continuationCanWrite(continuationStatus) {
		return ChangeResult{}, semanticError("closed_continuation", "trusted Continuation is closed for new Blackboard writes", "", nil)
	}
	if continuationID != "" {
		currentOwner, pinned, err := continuationOwnsCurrentWorkingPath(ctx, tx, continuationID)
		if err != nil {
			return ChangeResult{}, err
		}
		if pinned && !currentOwner {
			return ChangeResult{}, semanticError("closed_continuation", "trusted Continuation no longer owns the Task Working Snapshot", "", nil)
		}
	}
	if continuationID != "" {
		for index, change := range batch.Changes {
			if change.Op == "merge" {
				return ChangeResult{}, semanticError("authority_denied", "governed Record Merge requires explicit operator approval", fmt.Sprintf("changes[%d].op", index), nil)
			}
		}
	}

	revision, err := currentRevision(ctx, tx, projectID)
	if err != nil {
		return ChangeResult{}, err
	}
	if expectedBaseRevision != nil && revision != *expectedBaseRevision {
		return ChangeResult{}, semanticError("version_conflict", "Blackboard revision changed", "base_revision", map[string]any{
			"expected_revision": float64(*expectedBaseRevision),
			"current_revision":  float64(revision),
			"next_action":       "synchronize_runtime_blackboard",
		})
	}
	runtimeConfirmedFacts := make(map[string]string)
	runtimeCreatedAttempts := make(map[string]string)
	dependentConfirmedFacts := make(map[string]string)
	affectedConclusions := make(map[string]bool)
	now := time.Now().UTC().Format(time.RFC3339Nano)
	baseRevision := revision
	kernel, err := applySemanticChangeSet(ctx, tx, revision, batch.Changes, now, semanticChangeBackend{
		prepare: func(ctx context.Context, tx *sql.Tx, change Change, index int) (Change, error) {
			change, err := resolveChangeRedirects(ctx, tx, projectID, change)
			if err != nil {
				return Change{}, err
			}
			// Capture dependents before destructive changes remove their support
			// edges (supersede/merge/unrelate), and include each explicit target.
			if err := collectAffectedConclusions(ctx, tx, projectID, []string{change.Key, change.From, change.To, change.Source, change.Canonical, change.Replaced, change.Replacement}, affectedConclusions); err != nil {
				return Change{}, err
			}
			if change.Op == "transition" && isOneOf(change.Status, "verified", "rejected") {
				if err := ensureCTFProject(ctx, tx, projectID, fmt.Sprintf("changes[%d].status", index)); err != nil {
					return Change{}, err
				}
			}
			if continuationID != "" {
				if err := validateContinuationChangeOwnership(ctx, tx, projectID, continuationID, change, index); err != nil {
					return Change{}, err
				}
			}
			if change.Op == "transition" && change.Status == "missing" {
				if err := collectEvidenceDependentConfirmedFacts(ctx, tx, projectID, change.Key, fmt.Sprintf("changes[%d].status", index), dependentConfirmedFacts); err != nil {
					return Change{}, err
				}
			}
			if change.Op == "transition" && change.Status == "tentative" {
				if err := collectSupportingFactDependentConfirmedFacts(ctx, tx, projectID, change.Key, fmt.Sprintf("changes[%d].status", index), dependentConfirmedFacts); err != nil {
					return Change{}, err
				}
			}
			if change.Op == "supersede" || change.Op == "merge" {
				key, field := change.Replaced, "replaced"
				if change.Op == "merge" {
					key, field = change.Source, "source"
				}
				if err := collectEvidenceDependentConfirmedFacts(ctx, tx, projectID, key, fmt.Sprintf("changes[%d].%s", index, field), dependentConfirmedFacts); err != nil {
					return Change{}, err
				}
				if err := collectSupportingFactDependentConfirmedFacts(ctx, tx, projectID, key, fmt.Sprintf("changes[%d].%s", index, field), dependentConfirmedFacts); err != nil {
					return Change{}, err
				}
			}
			if change.Op == "unrelate" && change.Relation == "evidences" {
				if err := collectEvidenceDependentConfirmedFacts(ctx, tx, projectID, change.From, fmt.Sprintf("changes[%d].relation", index), dependentConfirmedFacts); err != nil {
					return Change{}, err
				}
			}
			if change.Op == "unrelate" && change.Relation == "supports" {
				if err := collectSupportingFactDependentConfirmedFacts(ctx, tx, projectID, change.From, fmt.Sprintf("changes[%d].relation", index), dependentConfirmedFacts); err != nil {
					return Change{}, err
				}
			}
			return change, nil
		},
		create: func(ctx context.Context, tx *sql.Tx, revision, index int, change Change, now string) (int, string, int, bool, error) {
			return applyCreateRecord(ctx, tx, projectID, revision, index, change, now)
		},
		update: func(ctx context.Context, tx *sql.Tx, revision, index int, change Change, now string) (int, string, int, bool, error) {
			return applyUpdateRecord(ctx, tx, projectID, revision, index, change, now)
		},
		relate: func(ctx context.Context, tx *sql.Tx, revision, index int, change Change, now string) (int, RelationVersionTuple, bool, error) {
			return applyRelate(ctx, tx, projectID, revision, index, change, now)
		},
		unrelate: func(ctx context.Context, tx *sql.Tx, revision, index int, change Change, now string) (int, RelationVersionTuple, error) {
			return applyUnrelate(ctx, tx, projectID, revision, index, change, now)
		},
		transition: func(ctx context.Context, tx *sql.Tx, revision, index int, change Change, now string) (int, string, int, bool, error) {
			return applyTransition(ctx, tx, projectID, revision, index, change, now)
		},
		supersede: func(ctx context.Context, tx *sql.Tx, revision, index int, change Change, created map[string]bool, now string) (int, string, int, RelationVersionTuple, bool, error) {
			return applySupersede(ctx, tx, projectID, revision, index, change, created, now)
		},
		merge: func(ctx context.Context, tx *sql.Tx, revision, index int, change Change, now string) (int, string, int, []RelationVersionTuple, error) {
			return applyMerge(ctx, tx, projectID, revision, index, change, now)
		},
		after: func(ctx context.Context, tx *sql.Tx, index int, change Change, outcome semanticChangeOutcome) error {
			if !outcome.Changed {
				return nil
			}
			if continuationID == "" {
				if operatorRequest && change.Op == "create" && change.Type == "attempt" {
					return bindOperatorAttemptOrigin(ctx, tx, projectID, outcome.Key, operator.actorID, now)
				}
				return nil
			}
			if change.Op == "create" && change.Type == "fact" {
				created, err := loadCurrentRecord(ctx, tx, projectID, outcome.Key)
				if err != nil {
					return err
				}
				if created.record.factRecord().Confidence == "confirmed" {
					runtimeConfirmedFacts[outcome.Key] = fmt.Sprintf("changes[%d].record.confidence", index)
				}
			}
			if change.Op == "create" && change.Type == "attempt" {
				if err := bindAttemptOrigin(ctx, tx, projectID, outcome.Key, continuationID, now); err != nil {
					return err
				}
				runtimeCreatedAttempts[outcome.Key] = fmt.Sprintf("changes[%d]", index)
			}
			if change.Op == "transition" && change.Status == "confirmed" {
				runtimeConfirmedFacts[outcome.Key] = fmt.Sprintf("changes[%d].status", index)
			}
			return nil
		},
		unsupported: "unsupported Blackboard v2 operation",
	})
	if err != nil {
		return ChangeResult{}, err
	}
	revision = kernel.Revision
	if err := validateFinalTerminalAttempts(ctx, tx, projectID, kernel.TerminalAttempts); err != nil {
		return ChangeResult{}, err
	}
	if continuationID != "" {
		attemptKeys := make([]string, 0, len(runtimeCreatedAttempts))
		for key := range runtimeCreatedAttempts {
			attemptKeys = append(attemptKeys, key)
		}
		sort.Strings(attemptKeys)
		for _, key := range attemptKeys {
			current, err := loadCurrentRecord(ctx, tx, projectID, key)
			if errors.Is(err, sql.ErrNoRows) {
				continue
			}
			if err != nil {
				return ChangeResult{}, err
			}
			if current.typ != "attempt" {
				continue
			}
			tested, err := currentOutgoingRelationshipCount(ctx, tx, projectID, key, "tests")
			if err != nil {
				return ChangeResult{}, err
			}
			if tested == 0 {
				return ChangeResult{}, semanticError("semantic_validation", "Runtime-created Attempt requires a current tested target at batch end", runtimeCreatedAttempts[key], map[string]any{
					"key": key, "required_relation": "tests", "next_action": "add_tests_relation_in_same_batch",
				})
			}
		}

		keys := make([]string, 0, len(runtimeConfirmedFacts))
		for key := range runtimeConfirmedFacts {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			current, err := loadCurrentRecord(ctx, tx, projectID, key)
			if errors.Is(err, sql.ErrNoRows) {
				continue
			}
			if err != nil {
				return ChangeResult{}, err
			}
			if current.typ != "fact" || current.record.factRecord().Confidence != "confirmed" {
				continue
			}
			hasBasis, err := s.runtimeFactConfirmationHasBasis(ctx, tx, projectID, continuationID, key)
			if err != nil {
				return ChangeResult{}, err
			}
			if !hasBasis {
				return ChangeResult{}, semanticError("semantic_validation", "Runtime Fact confirmation requires an accepted semantic basis", runtimeConfirmedFacts[key], map[string]any{
					"key":            key,
					"accepted_basis": []string{"available_evidence", "confirmed_supporting_fact", "succeeded_producing_attempt"},
					"next_action":    "establish_basis_then_confirm",
				})
			}
		}
	}
	if err := s.validateDependentConfirmedFactBases(ctx, tx, projectID, dependentConfirmedFacts); err != nil {
		return ChangeResult{}, err
	}
	changedKeys := make([]string, 0, len(kernel.ChangedRecords)+2*len(kernel.ChangedRelations))
	for key := range kernel.ChangedRecords {
		changedKeys = append(changedKeys, key)
	}
	for _, tuple := range kernel.ChangedRelations {
		changedKeys = append(changedKeys, fmt.Sprint(tuple[0]), fmt.Sprint(tuple[2]))
	}
	if err := collectAffectedConclusions(ctx, tx, projectID, changedKeys, affectedConclusions); err != nil {
		return ChangeResult{}, err
	}
	conclusionKeys := make([]string, 0, len(affectedConclusions))
	for key := range affectedConclusions {
		conclusionKeys = append(conclusionKeys, key)
	}
	sort.Strings(conclusionKeys)
	if err := validateConfirmedFindings(ctx, tx, projectID, conclusionKeys); err != nil {
		return ChangeResult{}, err
	}
	if err := validateVerifiedSolutions(ctx, tx, projectID, conclusionKeys); err != nil {
		return ChangeResult{}, err
	}

	workingAdvanced := false
	if continuationID != "" {
		_, _, workingAdvanced, err = s.advanceContinuationWorkingSnapshotTx(ctx, tx, projectID, continuationID, workingSnapshotChanges{baseRevision: baseRevision, records: kernel.ChangedRecords})
		if err != nil {
			return ChangeResult{}, err
		}
	}
	result := makeChangeResult(revision, kernel.ChangedRecords, kernel.ChangedRelations)
	resultJSON, err := json.Marshal(result)
	if err != nil {
		return ChangeResult{}, fmt.Errorf("encode idempotency result: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO blackboard_v2_idempotency_receipts (project_id, idempotency_key, request_hash, result_json, created_at, continuation_id)
		VALUES (?, ?, ?, ?, ?, ?)`,
		projectID, batch.IdempotencyKey, requestHash, string(resultJSON), now, continuationID,
	); err != nil {
		return ChangeResult{}, fmt.Errorf("store idempotency receipt: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return ChangeResult{}, fmt.Errorf("commit Blackboard v2 change: %w", err)
	}
	s.writeMu.Unlock()
	writeLocked = false
	if continuationID != "" {
		if workingAdvanced && s.publicationFail != nil {
			if err := s.publicationFail(SnapshotPublicationAfterCommit, continuationID); err != nil {
				return ChangeResult{}, err
			}
		}
		if err := s.rematerializeContinuationWorkingSnapshot(ctx, continuationID); err != nil {
			return ChangeResult{}, fmt.Errorf("replace acknowledged Working Blackboard Snapshot: %w", err)
		}
	}
	return result, nil
}

func validateChangeBatchDTOShape(batch ChangeBatch) error {
	if batch.Schema != changeBatchSchema {
		return semanticError("invalid_schema", "unsupported semantic change schema", "", nil)
	}
	if batch.IdempotencyKey == "" {
		return semanticError("semantic_validation", "idempotency_key is required", "idempotency_key", nil)
	}
	if batch.Changes == nil {
		return semanticError("semantic_validation", "changes must be a non-null array", "changes", nil)
	}
	for index, change := range batch.Changes {
		if err := validateChangeDTOShape(change, index); err != nil {
			return err
		}
	}
	return nil
}

func continuationOwnsCurrentWorkingPath(ctx context.Context, tx *sql.Tx, continuationID string) (current, pinned bool, err error) {
	var status string
	var number int
	var taskID string
	err = tx.QueryRowContext(ctx, `
		SELECT c.task_id,c.number,c.status,
		       EXISTS(SELECT 1 FROM blackboard_v2_continuation_pins pin WHERE pin.continuation_id=c.id)
		FROM task_continuations c WHERE c.id=?`, continuationID,
	).Scan(&taskID, &number, &status, &pinned)
	if err != nil {
		return false, false, err
	}
	if !pinned || !continuationCanWrite(status) {
		return false, pinned, nil
	}
	var newer int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM task_continuations WHERE task_id=? AND number>?`, taskID, number).Scan(&newer); err != nil {
		return false, pinned, err
	}
	return newer == 0, pinned, nil
}
