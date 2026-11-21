package projectinterface

import (
	"context"
	"crypto/subtle"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"pentest/internal/owner"
)

const grantSelectColumns = `grant_id, project_id, task_id, '' AS session_id, continuation_id, runtime_config_version_id,
runtime_profile_id, runtime_plugin_id, runner, actor_id, token_hash, access_mode,
issued_at, finished_at, revoked_at, terminal_at`

const sessionGrantSelectColumns = `grant_id, '' AS project_id, '' AS task_id, session_id, continuation_id, runtime_config_version_id,
runtime_profile_id, runtime_plugin_id, runner, actor_id, token_hash, access_mode,
issued_at, finished_at, revoked_at, terminal_at`

// queryGrant runs `SELECT <columns> FROM blackboard_continuation_grants WHERE
// <predicate> = ?` through queryer (a *sql.DB or *sql.Tx) and scans one row.
type rowQueryer interface {
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

func queryGrant(ctx context.Context, queryer rowQueryer, predicate, value string) (Grant, error) {
	// predicate is a hardcoded column name, never caller input, so it is safe to
	// interpolate. Only value is bound.
	row := queryer.QueryRowContext(ctx,
		"SELECT "+grantSelectColumns+" FROM blackboard_continuation_grants WHERE "+predicate+" = ?",
		value,
	)
	grant, err := scanGrant(row)
	if errors.Is(err, sql.ErrNoRows) {
		return Grant{}, ErrGrantNotFound
	}
	if err != nil {
		return Grant{}, err
	}
	return grant, nil
}

func querySessionGrant(ctx context.Context, queryer rowQueryer, predicate, value string) (Grant, error) {
	row := queryer.QueryRowContext(ctx,
		"SELECT "+sessionGrantSelectColumns+" FROM session_continuation_interface_grants WHERE "+predicate+" = ?",
		value,
	)
	grant, err := scanGrant(row)
	if errors.Is(err, sql.ErrNoRows) {
		return Grant{}, ErrGrantNotFound
	}
	if err != nil {
		return Grant{}, err
	}
	return grant, nil
}

// Resolve maps a plaintext bearer token to its Grant. It performs a
// constant-time comparison against the stored hash so timing does not leak
// token prefixes. An unknown token returns ErrGrantNotFound.
func (s *GrantStore) Resolve(ctx context.Context, plaintext string) (Grant, error) {
	if plaintext == "" {
		return Grant{}, ErrGrantNotFound
	}
	hash := hashToken(plaintext)
	grant, err := queryGrant(ctx, s.db, "token_hash", hash)
	if errors.Is(err, ErrGrantNotFound) {
		grant, err = querySessionGrant(ctx, s.db, "token_hash", hash)
	}
	if err != nil {
		return Grant{}, fmt.Errorf("resolve continuation grant: %w", err)
	}
	// Constant-time guard against a hash collision on a malformed index: the
	// resolved token hash must match exactly.
	if subtle.ConstantTimeCompare([]byte(grant.TokenHash), []byte(hash)) != 1 {
		return Grant{}, ErrGrantNotFound
	}
	return grant, nil
}

// Get returns the current state of a grant by ID. Write capabilities re-read the
// grant this way to revalidate lifecycle authoritatively at request time rather
// than trusting a principal snapshot cached at authentication.
func (s *GrantStore) Get(ctx context.Context, grantID string) (Grant, error) {
	if grantID == "" {
		return Grant{}, ErrGrantNotFound
	}
	grant, err := queryGrant(ctx, s.db, "grant_id", grantID)
	if errors.Is(err, ErrGrantNotFound) {
		grant, err = querySessionGrant(ctx, s.db, "grant_id", grantID)
	}
	if err != nil {
		return Grant{}, fmt.Errorf("read continuation grant: %w", err)
	}
	return grant, nil
}

// Finish closes the grant to new project-interface mutations. Exact idempotent
// replay and read operations remain allowed. This method is the grant-store
// lifecycle close used by Blackboard v2 Finish, reconciliation, and tests.
func (s *GrantStore) Finish(ctx context.Context, grantID string) (Grant, error) {
	return s.setLifecycleTimestamp(ctx, grantID, "finished_at", ErrGrantAlreadyFinished)
}

// Revoke marks the grant revoked. Every later use is rejected. Revocation is
// idempotent only when the grant is already revoked; finishing or terminating a
// revoked grant is reported as already closed.
func (s *GrantStore) Revoke(ctx context.Context, grantID string) (Grant, error) {
	return s.setLifecycleTimestamp(ctx, grantID, "revoked_at", ErrGrantAlreadyRevoked)
}

// MarkContinuationTerminal marks every open grant bound to the given
// Continuation terminal. It is called when a Continuation becomes terminal
// without Finish so the reconciler owns later semantic changes. Already-closed
// grants are left unchanged.
func (s *GrantStore) MarkContinuationTerminal(ctx context.Context, continuationID string) error {
	if continuationID == "" {
		return ErrGrantNotFound
	}
	ts := s.clock.Now().UTC().Format(time.RFC3339Nano)
	_, err := s.db.ExecContext(ctx,
		`UPDATE blackboard_continuation_grants
		    SET terminal_at = CASE WHEN terminal_at = '' AND finished_at = '' AND revoked_at = '' THEN ? ELSE terminal_at END
		  WHERE continuation_id = ?`,
		ts, continuationID,
	)
	if err != nil {
		return fmt.Errorf("mark continuation grants terminal: %w", err)
	}
	if _, err := s.db.ExecContext(ctx,
		`UPDATE session_continuation_interface_grants
		    SET terminal_at = CASE WHEN terminal_at = '' AND finished_at = '' AND revoked_at = '' THEN ? ELSE terminal_at END
		  WHERE continuation_id = ?`,
		ts, continuationID,
	); err != nil {
		return fmt.Errorf("mark Session continuation grants terminal: %w", err)
	}
	return nil
}

func (s *GrantStore) setLifecycleTimestamp(ctx context.Context, grantID, column string, already *Error) (Grant, error) {
	if grantID == "" {
		return Grant{}, ErrGrantNotFound
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Grant{}, fmt.Errorf("begin grant lifecycle transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	grant, err := queryGrant(ctx, tx, "grant_id", grantID)
	if errors.Is(err, ErrGrantNotFound) {
		grant, err = querySessionGrant(ctx, tx, "grant_id", grantID)
	}
	if err != nil {
		return Grant{}, fmt.Errorf("load continuation grant: %w", err)
	}
	if grant.Status() != GrantStatusOpen {
		// Finishing or revoking an already-closed grant is a no-op only when the
		// requested state matches; otherwise report the conflict so callers do
		// not mistake one close for another.
		if (column == "finished_at" && grant.Status() == GrantStatusFinished) ||
			(column == "revoked_at" && grant.Status() == GrantStatusRevoked) {
			return grant, nil
		}
		return grant, already
	}
	ts := s.clock.Now().UTC().Format(time.RFC3339Nano)
	table := "blackboard_continuation_grants"
	if grant.IsSession() {
		table = "session_continuation_interface_grants"
	}
	if _, err := tx.ExecContext(ctx,
		`UPDATE `+table+` SET `+column+`=? WHERE grant_id=?`,
		ts, grantID,
	); err != nil {
		return Grant{}, fmt.Errorf("update continuation grant lifecycle: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return Grant{}, fmt.Errorf("commit grant lifecycle: %w", err)
	}
	switch column {
	case "finished_at":
		grant.FinishedAt = ts
	case "revoked_at":
		grant.RevokedAt = ts
	case "terminal_at":
		grant.TerminalAt = ts
	}
	return grant, nil
}

// validateIssueRequest rejects incomplete bound context before touching the
// database. Every field is part of trusted provenance and is server-derived.
func validateIssueRequest(req IssueGrantRequest) error {
	if _, err := normalizeGrantAccess(req.Access); err != nil {
		return err
	}
	missing := []string{}
	if req.ProjectID == "" {
		missing = append(missing, "project_id")
	}
	if req.TaskID == "" {
		missing = append(missing, "task_id")
	}
	if req.ContinuationID == "" {
		missing = append(missing, "continuation_id")
	}
	if req.RuntimeConfigVersionID == "" {
		missing = append(missing, "runtime_config_version_id")
	}
	if req.RuntimePluginID == "" {
		missing = append(missing, "runtime_plugin_id")
	}
	if req.Runner == "" {
		missing = append(missing, "runner")
	}
	if len(missing) > 0 {
		return ValidationError(ErrCodeInvalidRequest, "continuation grant is missing bound fields: "+strings.Join(missing, ", "), "context")
	}
	return nil
}

func validateSessionIssueRequest(req IssueSessionGrantRequest) error {
	if _, err := normalizeGrantAccess(req.Access); err != nil {
		return err
	}
	missing := []string{}
	if req.SessionID == "" {
		missing = append(missing, "session_id")
	}
	if req.ContinuationID == "" {
		missing = append(missing, "continuation_id")
	}
	if req.RuntimeConfigVersionID == "" {
		missing = append(missing, "runtime_config_version_id")
	}
	if req.RuntimePluginID == "" {
		missing = append(missing, "runtime_plugin_id")
	}
	if req.Runner == "" {
		missing = append(missing, "runner")
	}
	if len(missing) > 0 {
		return ValidationError(ErrCodeInvalidRequest, "Session continuation grant is missing bound fields: "+strings.Join(missing, ", "), "context")
	}
	return nil
}

func validateBoundRows(tx *sql.Tx, req IssueGrantRequest) error {
	var taskProjectID string
	if err := tx.QueryRow(`SELECT project_id FROM tasks WHERE id=?`, req.TaskID).Scan(&taskProjectID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ValidationError(ErrCodeProjectNotFound, "continuation grant Task does not exist", "context.task_id")
		}
		return fmt.Errorf("read grant Task: %w", err)
	}
	if taskProjectID != req.ProjectID {
		return ValidationError(ErrCodeProjectMismatch, "continuation grant Task does not belong to the bound Project", "context.task_id")
	}
	var continuationTaskID, profileID, runner, configVersionID string
	if err := tx.QueryRow(
		`SELECT task_id, runtime_profile_id, runner, COALESCE(runtime_config_version_id,'') FROM task_continuations WHERE id=?`,
		req.ContinuationID,
	).Scan(&continuationTaskID, &profileID, &runner, &configVersionID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ValidationError(ErrCodeProjectNotFound, "continuation grant Continuation does not exist", "context.continuation_id")
		}
		return fmt.Errorf("read grant Continuation: %w", err)
	}
	if continuationTaskID != req.TaskID || profileID != req.RuntimeProfileID || runner != req.Runner {
		return ValidationError(ErrCodeProvenanceSpoofed, "continuation grant context does not match the durable Continuation", "context")
	}
	if req.RuntimeConfigVersionID != "" && configVersionID != "" && configVersionID != req.RuntimeConfigVersionID {
		return ValidationError(ErrCodeProvenanceSpoofed, "continuation grant runtime configuration version does not match the pinned Continuation", "context.runtime_config_version_id")
	}
	return nil
}

func validateSessionBoundRows(tx *sql.Tx, req IssueSessionGrantRequest) error {
	var lifecycle string
	if err := tx.QueryRow(`SELECT lifecycle FROM sessions WHERE id=?`, req.SessionID).Scan(&lifecycle); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ValidationError(ErrCodeProjectNotFound, "continuation grant Session does not exist", "context.session_id")
		}
		return fmt.Errorf("read grant Session: %w", err)
	}
	if lifecycle != "open" {
		return ValidationError(ErrCodeContinuationClosed, "continuation grant Session is archived", "context.session_id")
	}
	var sessionID, profileID, provider, runner, configVersionID string
	if err := tx.QueryRow(`SELECT session_id, runtime_profile_id, runtime_provider, runner, runtime_config_version_id FROM session_continuations WHERE id=?`, req.ContinuationID).
		Scan(&sessionID, &profileID, &provider, &runner, &configVersionID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ValidationError(ErrCodeProjectNotFound, "continuation grant Session Continuation does not exist", "context.continuation_id")
		}
		return fmt.Errorf("read Session grant Continuation: %w", err)
	}
	if sessionID != req.SessionID || profileID != req.RuntimeProfileID || provider != req.RuntimePluginID || runner != req.Runner || configVersionID != req.RuntimeConfigVersionID {
		return ValidationError(ErrCodeProvenanceSpoofed, "continuation grant context does not match the durable Session Continuation", "context")
	}
	return nil
}

func runtimeActorID(pluginID, continuationID string) string {
	return "runtime:" + pluginID + ":" + continuationID
}

func normalizeGrantAccess(access GrantAccess) (GrantAccess, error) {
	switch access {
	case "", GrantAccessFull:
		return GrantAccessFull, nil
	case GrantAccessReadOnly:
		return GrantAccessReadOnly, nil
	default:
		return "", ValidationError(ErrCodeInvalidRequest, "grant access must be full or read_only", "context.access")
	}
}

type scanner interface {
	Scan(dest ...any) error
}

func scanGrant(row scanner) (Grant, error) {
	var g Grant
	var projectID, taskID, sessionID, configVersionID string
	err := row.Scan(
		&g.ID, &projectID, &taskID, &sessionID, &g.ContinuationID, &configVersionID,
		&g.RuntimeProfileID, &g.RuntimePluginID, &g.Runner, &g.ActorID, &g.TokenHash, &g.Access,
		&g.IssuedAt, &g.FinishedAt, &g.RevokedAt, &g.TerminalAt,
	)
	if err != nil {
		return Grant{}, err
	}
	g.RuntimeConfigVersionID = configVersionID
	if sessionID != "" {
		g.Owner = owner.NewSessionContract(sessionID, "")
	} else {
		g.Owner = owner.NewTaskContract(taskID, projectID, "")
	}
	if err := g.Owner.Validate(); err != nil {
		return Grant{}, fmt.Errorf("decode continuation grant owner: %w", err)
	}
	return g, nil
}
