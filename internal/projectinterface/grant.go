// Package projectinterface owns the trusted Runtime and operator boundary to
// Blackboard v2. It binds a daemon-issued Continuation Interface Grant to
// server-side trusted execution context and exposes the semantic Runtime
// capabilities without storing semantic Blackboard data itself.
package projectinterface

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"pentest/internal/store"
)

// RuntimeProtocolVersion versions the project-interface request and response
// contract. It is independent of the Blackboard v2 semantic schema version.
const RuntimeProtocolVersion = 1

// Clock is the injected time source for grant issuance and lifecycle events.
// The issued_at, finished_at, revoked_at, and terminal_at timestamps are always
// server-generated; callers cannot supply them.
type Clock interface {
	Now() time.Time
}

// SystemClock is the production Clock implementation.
type SystemClock struct{}

// Now returns the current UTC time.
func (SystemClock) Now() time.Time { return time.Now().UTC() }

// IDSource is the injected immutable-ID source for grant IDs (slices §2.1).
type IDSource interface {
	NextID() string
}

// RandomIDSource is the production IDSource implementation using crypto/rand.
type RandomIDSource struct{}

// NextID returns a fresh opaque 16-byte hex ID.
func (RandomIDSource) NextID() string { return newID() }

// TokenSource generates cryptographically random bearer tokens. The plaintext
// is projected only to the task-local Runtime environment and trusted MCP
// configuration; the grant store keeps a SHA-256 hash.
type TokenSource interface {
	NewToken() (plaintext string, err error)
}

// RandomTokenSource is the production TokenSource: 32 crypto-random bytes
// rendered as unpadded base64url, which is safe to carry in an Authorization
// header, a query string, or an environment variable.
type RandomTokenSource struct{}

// NewToken returns a fresh opaque bearer token.
func (RandomTokenSource) NewToken() (string, error) { return newToken() }

func newID() string {
	var buf [16]byte
	if _, err := rand.Read(buf[:]); err != nil {
		panic("crypto/rand: " + err.Error())
	}
	return hex.EncodeToString(buf[:])
}

func newToken() (string, error) {
	var buf [32]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return "", err
	}
	return tokenEncoding.EncodeToString(buf[:]), nil
}

// hashToken returns the lowercase hex SHA-256 of a plaintext bearer token. The
// store indexes this hash; the plaintext is never persisted.
func hashToken(plaintext string) string {
	sum := sha256.Sum256([]byte(plaintext))
	return hex.EncodeToString(sum[:])
}

// GrantStatus names the lifecycle state of a Continuation Interface Grant.
// New writes are allowed only while the grant is open;
// reads and exact replay remain available in every state.
type GrantStatus string

const (
	// GrantStatusOpen allows new project-interface mutations. The bound
	// Continuation is pending, running, or paused and has not closed the
	// protocol.
	GrantStatusOpen GrantStatus = "open"
	// GrantStatusFinished is set by the Finish capability (I04) or a grant-level
	// close: new writes are rejected but exact replay and reads remain.
	GrantStatusFinished GrantStatus = "finished"
	// GrantStatusRevoked is set by explicit operator revocation or Task deletion.
	GrantStatusRevoked GrantStatus = "revoked"
	// GrantStatusTerminal is set when the bound Continuation becomes terminal
	// without Finish; the system reconciler owns later semantic changes.
	GrantStatusTerminal GrantStatus = "terminal"
)

// IsWriteable reports whether a grant in this state admits new mutations.
func (s GrantStatus) IsWriteable() bool { return s == GrantStatusOpen }

// IsReadable reports whether a grant in this state admits reads and exact
// idempotent replay. Finish and a terminal Continuation close only new writes;
// revocation rejects every use.
func (s GrantStatus) IsReadable() bool { return s != GrantStatusRevoked }

// Grant is the stored Continuation Interface Grant. It never carries the
// plaintext bearer token, only its SHA-256 hash.
type Grant struct {
	ID                     string `json:"id"`
	ProjectID              string `json:"project_id"`
	TaskID                 string `json:"task_id"`
	ContinuationID         string `json:"continuation_id"`
	RuntimeConfigVersionID string `json:"runtime_config_version_id"`
	RuntimeProfileID       string `json:"runtime_profile_id"`
	RuntimePluginID        string `json:"runtime_plugin_id"`
	Runner                 string `json:"runner"`
	// ActorID is server-derived as runtime:<runtime_plugin_id>:<continuation_id>
	// and is never accepted from a Runtime request.
	ActorID    string `json:"actor_id"`
	TokenHash  string `json:"-"`
	IssuedAt   string `json:"issued_at"`
	FinishedAt string `json:"finished_at,omitempty"`
	RevokedAt  string `json:"revoked_at,omitempty"`
	TerminalAt string `json:"terminal_at,omitempty"`
}

// Status derives the grant lifecycle state from its timestamps.
func (g Grant) Status() GrantStatus {
	switch {
	case g.RevokedAt != "":
		return GrantStatusRevoked
	case g.TerminalAt != "":
		return GrantStatusTerminal
	case g.FinishedAt != "":
		return GrantStatusFinished
	default:
		return GrantStatusOpen
	}
}

// IssueGrantRequest carries the trusted, server-bound context for a new
// Continuation Interface Grant. The daemon assembles these fields during
// Continuation pinning; a Runtime request never
// supplies them.
type IssueGrantRequest struct {
	ProjectID              string
	TaskID                 string
	ContinuationID         string
	RuntimeConfigVersionID string
	RuntimeProfileID       string
	RuntimePluginID        string
	Runner                 string
}

// GrantStore owns Continuation Interface Grant persistence and trusted context
// resolution. It is the only authority that turns a
// bearer token into trusted execution context; transport adapters never inspect
// or fabricate Runtime provenance.
type GrantStore struct {
	db     *store.DB
	clock  Clock
	ids    IDSource
	tokens TokenSource
}

// NewGrantStore wires a GrantStore with injected deterministic dependencies
// (slices §2.1). Production passes SystemClock, RandomIDSource, and
// RandomTokenSource; tests pass deterministic sources.
func NewGrantStore(db *store.DB, clock Clock, ids IDSource, tokens TokenSource) *GrantStore {
	if clock == nil {
		clock = SystemClock{}
	}
	if ids == nil {
		ids = RandomIDSource{}
	}
	if tokens == nil {
		tokens = RandomTokenSource{}
	}
	return &GrantStore{db: db, clock: clock, ids: ids, tokens: tokens}
}

// Issue creates a Continuation Interface Grant and returns the plaintext bearer
// token. The plaintext is returned exactly once; the store keeps only its
// SHA-256 hash. The bound context is validated against the durable Task and
// Continuation rows before the grant is persisted.
func (s *GrantStore) Issue(ctx context.Context, req IssueGrantRequest) (string, Grant, error) {
	if err := validateIssueRequest(req); err != nil {
		return "", Grant{}, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return "", Grant{}, fmt.Errorf("begin grant issue transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	plaintext, grant, err := s.issueTx(ctx, tx, req)
	if err != nil {
		return "", Grant{}, err
	}
	if err := tx.Commit(); err != nil {
		return "", Grant{}, fmt.Errorf("commit grant issue: %w", err)
	}
	return plaintext, grant, nil
}

// IssueInTx inserts a Continuation Interface Grant inside an existing
// transaction so launch/pin/grant authority can commit atomically.
func (s *GrantStore) IssueInTx(ctx context.Context, tx *sql.Tx, req IssueGrantRequest) (string, Grant, error) {
	return s.issueTx(ctx, tx, req)
}

func (s *GrantStore) issueTx(ctx context.Context, tx *sql.Tx, req IssueGrantRequest) (string, Grant, error) {
	if err := validateIssueRequest(req); err != nil {
		return "", Grant{}, err
	}
	if err := validateBoundRows(tx, req); err != nil {
		return "", Grant{}, err
	}
	plaintext, err := s.tokens.NewToken()
	if err != nil {
		return "", Grant{}, fmt.Errorf("generate grant token: %w", err)
	}
	grant := Grant{
		ID: s.ids.NextID(), ProjectID: req.ProjectID, TaskID: req.TaskID,
		ContinuationID: req.ContinuationID, RuntimeConfigVersionID: req.RuntimeConfigVersionID,
		RuntimeProfileID: req.RuntimeProfileID, RuntimePluginID: req.RuntimePluginID,
		Runner: req.Runner, ActorID: runtimeActorID(req.RuntimePluginID, req.ContinuationID),
		TokenHash: hashToken(plaintext), IssuedAt: s.clock.Now().UTC().Format(time.RFC3339Nano),
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO blackboard_continuation_grants
		 (grant_id, token_hash, project_id, task_id, continuation_id, runtime_config_version_id,
		  runtime_profile_id, runtime_plugin_id, runner, actor_id, issued_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		grant.ID, grant.TokenHash, grant.ProjectID, grant.TaskID, grant.ContinuationID,
		grant.RuntimeConfigVersionID, grant.RuntimeProfileID, grant.RuntimePluginID,
		grant.Runner, grant.ActorID, grant.IssuedAt,
	); err != nil {
		return "", Grant{}, fmt.Errorf("insert continuation grant: %w", err)
	}
	return plaintext, grant, nil
}

// grantSelectColumns is the canonical column list for reading one grant row. It
// is shared by Resolve, Get, and setLifecycleTimestamp so the SELECT and
// scanGrant order cannot drift apart.
const grantSelectColumns = `grant_id, project_id, task_id, continuation_id, runtime_config_version_id,
runtime_profile_id, runtime_plugin_id, runner, actor_id, token_hash,
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

// Resolve maps a plaintext bearer token to its Grant. It performs a
// constant-time comparison against the stored hash so timing does not leak
// token prefixes. An unknown token returns ErrGrantNotFound.
func (s *GrantStore) Resolve(ctx context.Context, plaintext string) (Grant, error) {
	if plaintext == "" {
		return Grant{}, ErrGrantNotFound
	}
	hash := hashToken(plaintext)
	grant, err := queryGrant(ctx, s.db, "token_hash", hash)
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
	if _, err := tx.ExecContext(ctx,
		`UPDATE blackboard_continuation_grants SET `+column+`=? WHERE grant_id=?`,
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
	if req.RuntimeProfileID == "" {
		missing = append(missing, "runtime_profile_id")
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

// validateBoundRows verifies the durable Task and Continuation rows agree with
// the requested bound context so a grant never binds provenance the daemon no
// longer trusts.
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

// runtimeActorID derives the stable Runtime actor identifier.
func runtimeActorID(pluginID, continuationID string) string {
	return "runtime:" + pluginID + ":" + continuationID
}

type scanner interface {
	Scan(dest ...any) error
}

func scanGrant(row scanner) (Grant, error) {
	var g Grant
	var configVersionID string
	err := row.Scan(
		&g.ID, &g.ProjectID, &g.TaskID, &g.ContinuationID, &configVersionID,
		&g.RuntimeProfileID, &g.RuntimePluginID, &g.Runner, &g.ActorID, &g.TokenHash,
		&g.IssuedAt, &g.FinishedAt, &g.RevokedAt, &g.TerminalAt,
	)
	if err != nil {
		return Grant{}, err
	}
	g.RuntimeConfigVersionID = configVersionID
	return g, nil
}

// Sentinel grant errors let transports map failures without reclassification.
var (
	ErrGrantNotFound        = ValidationError(ErrCodeGrantNotFound, "continuation grant is missing or invalid", "authorization")
	ErrGrantAlreadyFinished = ValidationError(ErrCodeContinuationClosed, "continuation grant is already finished", "authorization")
	ErrGrantAlreadyRevoked  = ValidationError(ErrCodeContinuationClosed, "continuation grant is already revoked", "authorization")
)
