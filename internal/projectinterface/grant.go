// Package projectinterface owns the trusted Runtime and operator boundary to
// Blackboard v2. It binds a daemon-issued Continuation Interface Grant to
// server-side trusted execution context and exposes the semantic Runtime
// capabilities without storing semantic Blackboard data itself.
package projectinterface

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"fmt"
	"time"

	"pentest/internal/owner"
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

type SystemClock struct{}

func (SystemClock) Now() time.Time { return time.Now().UTC() }

type IDSource interface {
	NextID() string
}

type RandomIDSource struct{}

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
	GrantStatusRevoked  GrantStatus = "revoked"
	// GrantStatusTerminal is set when the bound Continuation becomes terminal
	// without Finish; the system reconciler owns later semantic changes.
	GrantStatusTerminal GrantStatus = "terminal"
)

func (s GrantStatus) IsWriteable() bool { return s == GrantStatusOpen }

// IsReadable reports whether a grant in this state admits reads and exact
// idempotent replay. Finish and a terminal Continuation close only new writes;
// revocation rejects every use.
func (s GrantStatus) IsReadable() bool { return s != GrantStatusRevoked }

// GrantAccess is the maximum Blackboard authority carried by one Continuation
// Interface Grant. Lifecycle state can further reduce this authority.
type GrantAccess string

const (
	GrantAccessFull     GrantAccess = "full"
	GrantAccessReadOnly GrantAccess = "read_only"
)

// Grant is the stored Continuation Interface Grant. It never carries the
// plaintext bearer token, only its SHA-256 hash.
type Grant struct {
	ID                     string         `json:"id"`
	Owner                  owner.Contract `json:"-"`
	ContinuationID         string         `json:"continuation_id"`
	RuntimeConfigVersionID string         `json:"runtime_config_version_id"`
	RuntimeProfileID       string         `json:"runtime_profile_id"`
	RuntimePluginID        string         `json:"runtime_plugin_id"`
	Runner                 string         `json:"runner"`
	Access                 GrantAccess    `json:"access"`
	// ActorID is server-derived as runtime:<runtime_plugin_id>:<continuation_id>
	// and is never accepted from a Runtime request.
	ActorID    string `json:"actor_id"`
	TokenHash  string `json:"-"`
	IssuedAt   string `json:"issued_at"`
	FinishedAt string `json:"finished_at,omitempty"`
	RevokedAt  string `json:"revoked_at,omitempty"`
	TerminalAt string `json:"terminal_at,omitempty"`
}

// IsSession reports whether this capability is bound to a Non-Project
// Session rather than a Project Task.
func (g Grant) IsSession() bool { return g.Owner.IsSession() }

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

// AllowsWrite reports whether this grant currently admits a new Blackboard
// mutation. Read-only access never admits writes or write replay.
func (g Grant) AllowsWrite() bool {
	return g.Access == GrantAccessFull && g.Status().IsWriteable()
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
	Access                 GrantAccess
}

// IssueSessionGrantRequest carries the server-derived Session launch binding.
// Session authority never fabricates Project or Task identity.
type IssueSessionGrantRequest struct {
	SessionID              string
	ContinuationID         string
	RuntimeConfigVersionID string
	RuntimeProfileID       string
	RuntimePluginID        string
	Runner                 string
	Access                 GrantAccess
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

// IssueSession creates a capability scoped to exactly one Session
// Continuation and returns its plaintext bearer token once.
func (s *GrantStore) IssueSession(ctx context.Context, req IssueSessionGrantRequest) (string, Grant, error) {
	if err := validateSessionIssueRequest(req); err != nil {
		return "", Grant{}, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return "", Grant{}, fmt.Errorf("begin Session grant issue transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if err := validateSessionBoundRows(tx, req); err != nil {
		return "", Grant{}, err
	}
	plaintext, err := s.tokens.NewToken()
	if err != nil {
		return "", Grant{}, fmt.Errorf("generate Session grant token: %w", err)
	}
	access, err := normalizeGrantAccess(req.Access)
	if err != nil {
		return "", Grant{}, err
	}
	grant := Grant{
		ID: s.ids.NextID(), Owner: owner.NewSessionContract(req.SessionID, ""), ContinuationID: req.ContinuationID,
		RuntimeConfigVersionID: req.RuntimeConfigVersionID, RuntimeProfileID: req.RuntimeProfileID,
		RuntimePluginID: req.RuntimePluginID, Runner: req.Runner, Access: access,
		ActorID:   runtimeActorID(req.RuntimePluginID, req.ContinuationID),
		TokenHash: hashToken(plaintext), IssuedAt: s.clock.Now().UTC().Format(time.RFC3339Nano),
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO session_continuation_interface_grants
		(grant_id, token_hash, session_id, continuation_id, runtime_config_version_id,
		 runtime_profile_id, runtime_plugin_id, runner, actor_id, access_mode, issued_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		grant.ID, grant.TokenHash, grant.Owner.SessionID, grant.ContinuationID, grant.RuntimeConfigVersionID,
		grant.RuntimeProfileID, grant.RuntimePluginID, grant.Runner, grant.ActorID, grant.Access, grant.IssuedAt); err != nil {
		return "", Grant{}, fmt.Errorf("insert Session continuation grant: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return "", Grant{}, fmt.Errorf("commit Session grant issue: %w", err)
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
	access, err := normalizeGrantAccess(req.Access)
	if err != nil {
		return "", Grant{}, err
	}
	grant := Grant{
		ID: s.ids.NextID(), Owner: owner.NewTaskContract(req.TaskID, req.ProjectID, ""),
		ContinuationID: req.ContinuationID, RuntimeConfigVersionID: req.RuntimeConfigVersionID,
		RuntimeProfileID: req.RuntimeProfileID, RuntimePluginID: req.RuntimePluginID,
		Runner: req.Runner, Access: access, ActorID: runtimeActorID(req.RuntimePluginID, req.ContinuationID),
		TokenHash: hashToken(plaintext), IssuedAt: s.clock.Now().UTC().Format(time.RFC3339Nano),
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO blackboard_continuation_grants
		 (grant_id, token_hash, project_id, task_id, continuation_id, runtime_config_version_id,
		  runtime_profile_id, runtime_plugin_id, runner, actor_id, access_mode, issued_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		grant.ID, grant.TokenHash, grant.Owner.ProjectID, grant.Owner.TaskID, grant.ContinuationID,
		grant.RuntimeConfigVersionID, grant.RuntimeProfileID, grant.RuntimePluginID,
		grant.Runner, grant.ActorID, grant.Access, grant.IssuedAt,
	); err != nil {
		return "", Grant{}, fmt.Errorf("insert continuation grant: %w", err)
	}
	return plaintext, grant, nil
}

// grantSelectColumns is the canonical column list for reading one grant row. It
// is shared by Resolve, Get, and setLifecycleTimestamp so the SELECT and
// scanGrant order cannot drift apart.

var (
	ErrGrantNotFound        = ValidationError(ErrCodeGrantNotFound, "continuation grant is missing or invalid", "authorization")
	ErrGrantAlreadyFinished = ValidationError(ErrCodeContinuationClosed, "continuation grant is already finished", "authorization")
	ErrGrantAlreadyRevoked  = ValidationError(ErrCodeContinuationClosed, "continuation grant is already revoked", "authorization")
)
