// Package credential owns credential references and their bindings. A credential
// reference is a non-secret pointer that lets a task receive required credentials
// without storing the secret in a runtime profile. References resolve through
// project credential bindings first, then global credential bindings. A project
// may override or explicitly disable a binding.
package credential

import (
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"pentest/internal/store"
)

var (
	envVarNamePattern    = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
	secretLikeEnvPattern = regexp.MustCompile(`(?i)(^sk-|^tp-|bearer\s+|api[_-]?key=|token=|secret=|[=/])`)
)

// Scope identifies where a binding lives: global or project.
type Scope string

const (
	// ScopeGlobal applies to all projects unless overridden.
	ScopeGlobal Scope = "global"
	// ScopeProject applies to one project and overrides the global binding.
	ScopeProject Scope = "project"
)

// Source names how a credential is actually obtained. Most source kinds store
// a pointer to where the runtime can find the secret. SourceLiteral stores the
// local secret value itself and must be redacted before returning bindings to
// API clients.
type Source struct {
	Kind  string `json:"kind"`
	Value string `json:"value,omitempty"`
	// DestinationEnv names the environment variable the materialized secret is
	// projected as at launch. For env sources it defaults to Value (so existing
	// bindings behave unchanged); file, command, and literal sources must set
	// it, otherwise the runtime would project under a path/command/secret-shaped
	// key instead of a real env var name.
	DestinationEnv string `json:"destination_env,omitempty"`
}

// Source kinds the product understands. The value field's meaning depends on kind:
//
//   - env:      Value is the environment variable name (e.g. "OPENAI_API_KEY").
//   - file:     Value is a path to a file containing the secret.
//   - command:  Value is a command whose stdout is the secret (e.g. an agent of a
//     password store). Used sparingly.
//   - literal:  Value is the local secret value. API responses must redact it.
const (
	SourceEnv     = "env"
	SourceFile    = "file"
	SourceCommand = "command"
	SourceLiteral = "literal"
)

var sourceKinds = map[string]bool{
	SourceEnv:     true,
	SourceFile:    true,
	SourceCommand: true,
	SourceLiteral: true,
}

// ConfiguredSourceSentinel is returned to clients when a literal secret exists
// and can be sent back to preserve the current stored value.
const ConfiguredSourceSentinel = "[configured]"

// Binding maps a credential reference to its source at a given scope.
type Binding struct {
	ID            string    `json:"id"`
	CredentialRef string    `json:"credential_ref"`
	Scope         Scope     `json:"scope"`
	ScopeID       string    `json:"scope_id,omitempty"`
	Source        Source    `json:"source"`
	Disabled      bool      `json:"disabled,omitempty"`
	CreatedAt     time.Time `json:"created_at"`
	UpdatedAt     time.Time `json:"updated_at"`
}

// Resolution is the result of resolving a credential reference for a project.
type Resolution struct {
	CredentialRef string   `json:"credential_ref"`
	Binding       *Binding `json:"binding,omitempty"`
	Source        *Source  `json:"source,omitempty"`
	// Disabled is true when a project explicitly disabled the binding, which
	// also blocks fallback to the global binding.
	Disabled bool `json:"disabled,omitempty"`
	// Found is false when neither a project nor global binding exists.
	Found bool `json:"found,omitempty"`
}

// ErrNotFound is returned when no binding matches.
var ErrNotFound = errors.New("credential binding not found")

// Sentinel validation errors.
var (
	ErrMissingCredentialRef = errors.New("credential_ref is required")
	ErrInvalidSourceKind    = errors.New("source kind is not supported")
	// ErrCommandSourceDisabled is returned when a command credential source is
	// rejected because the operator has not opted in. A command source runs
	// arbitrary shell on the host (effectively host RCE for anyone who can write
	// a binding), so it is disabled by default; see commandSourceEnabled.
	ErrCommandSourceDisabled = errors.New("command credential source is disabled")
)

// Service implements credential binding business rules against SQLite.
type Service struct {
	db *store.DB
}

// NewService returns a Service backed by the given database.
func NewService(db *store.DB) *Service {
	return &Service{db: db}
}

// Upsert creates or replaces a binding for a credential reference at a scope.
// There is exactly one binding per (credential_ref, scope, scope_id).
func (s *Service) Upsert(credentialRef string, scope Scope, scopeID string, source Source, disabled bool) (Binding, error) {
	credentialRef = strings.TrimSpace(credentialRef)
	if credentialRef == "" {
		return Binding{}, ErrMissingCredentialRef
	}
	if scope == ScopeProject && strings.TrimSpace(scopeID) == "" {
		return Binding{}, errors.New("project binding requires scope_id")
	}
	if scope == ScopeGlobal {
		scopeID = ""
	}
	if !disabled {
		if source.Kind == SourceLiteral && source.Value == ConfiguredSourceSentinel {
			existing, err := s.findOptional(credentialRef, scope, scopeID)
			if err != nil {
				return Binding{}, err
			}
			if existing == nil || existing.Source.Kind != SourceLiteral || strings.TrimSpace(existing.Source.Value) == "" {
				return Binding{}, errors.New("configured literal source is not available")
			}
			source.Value = existing.Source.Value
		}
		if err := validateSource(source); err != nil {
			return Binding{}, err
		}
	}

	now := time.Now().UTC()
	binding := Binding{
		ID:            newID(),
		CredentialRef: credentialRef,
		Scope:         scope,
		ScopeID:       scopeID,
		Source:        source,
		Disabled:      disabled,
		CreatedAt:     now,
		UpdatedAt:     now,
	}

	sourceJSON := mustEncode(source)
	_, err := s.db.Exec(
		`INSERT INTO credential_bindings (id, credential_ref, scope, scope_id, source_json, disabled, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		 ON CONFLICT(credential_ref, scope, scope_id) DO UPDATE SET
		   source_json = excluded.source_json,
		   disabled = excluded.disabled,
		   updated_at = excluded.updated_at`,
		binding.ID, binding.CredentialRef, string(binding.Scope), binding.ScopeID, sourceJSON, boolToInt(disabled),
		binding.CreatedAt.Format(time.RFC3339Nano), binding.UpdatedAt.Format(time.RFC3339Nano),
	)
	if err != nil {
		return Binding{}, fmt.Errorf("store credential binding: %w", err)
	}

	// Re-read so the returned row reflects the canonical id after upsert.
	return s.find(credentialRef, scope, scopeID)
}

// ListGlobal returns all global bindings.
func (s *Service) ListGlobal() ([]Binding, error) {
	return s.list(scopeFilter{scope: ScopeGlobal})
}

// ListForProject returns all project-scoped bindings for a project.
func (s *Service) ListForProject(projectID string) ([]Binding, error) {
	return s.list(scopeFilter{scope: ScopeProject, scopeID: projectID})
}

// Delete removes a binding. For project bindings, scopeID must match.
func (s *Service) Delete(id string) error {
	result, err := s.db.Exec(`DELETE FROM credential_bindings WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("delete credential binding: %w", err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("delete credential binding: %w", err)
	}
	if affected == 0 {
		return ErrNotFound
	}
	return nil
}

// Resolve returns the resolution for a credential reference for a project.
// Resolution order: project binding (overrides, including disable) → global
// binding → not found. A disabled project binding blocks global fallback.
func (s *Service) Resolve(credentialRef string, projectID string) (Resolution, error) {
	credentialRef = strings.TrimSpace(credentialRef)
	if credentialRef == "" {
		return Resolution{}, ErrMissingCredentialRef
	}

	project, err := s.findOptional(credentialRef, ScopeProject, projectID)
	if err != nil {
		return Resolution{}, err
	}
	if project != nil {
		res := Resolution{CredentialRef: credentialRef, Binding: project, Found: !project.Disabled}
		if !project.Disabled {
			source := project.Source
			res.Source = &source
		} else {
			res.Disabled = true
		}
		return res, nil
	}

	global, err := s.findOptional(credentialRef, ScopeGlobal, "")
	if err != nil {
		return Resolution{}, err
	}
	if global != nil {
		source := global.Source
		return Resolution{CredentialRef: credentialRef, Binding: global, Source: &source, Found: !global.Disabled, Disabled: global.Disabled}, nil
	}

	return Resolution{CredentialRef: credentialRef, Found: false}, nil
}

type scopeFilter struct {
	scope   Scope
	scopeID string
}

func (s *Service) list(filter scopeFilter) ([]Binding, error) {
	var (
		rows *sql.Rows
		err  error
	)
	if filter.scope == ScopeGlobal {
		rows, err = s.db.Query(
			`SELECT id, credential_ref, scope, scope_id, source_json, disabled, created_at, updated_at
			 FROM credential_bindings WHERE scope = ? ORDER BY credential_ref ASC`,
			string(filter.scope),
		)
	} else {
		rows, err = s.db.Query(
			`SELECT id, credential_ref, scope, scope_id, source_json, disabled, created_at, updated_at
			 FROM credential_bindings WHERE scope = ? AND scope_id = ? ORDER BY credential_ref ASC`,
			string(filter.scope), filter.scopeID,
		)
	}
	if err != nil {
		return nil, fmt.Errorf("list credential bindings: %w", err)
	}
	defer rows.Close()

	var bindings []Binding
	for rows.Next() {
		found, err := scanBinding(rows)
		if err != nil {
			return nil, fmt.Errorf("scan credential binding: %w", err)
		}
		bindings = append(bindings, found)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list credential bindings: %w", err)
	}
	return bindings, nil
}

func (s *Service) find(credentialRef string, scope Scope, scopeID string) (Binding, error) {
	row := s.db.QueryRow(
		`SELECT id, credential_ref, scope, scope_id, source_json, disabled, created_at, updated_at
		 FROM credential_bindings WHERE credential_ref = ? AND scope = ? AND scope_id = ?`,
		credentialRef, string(scope), scopeID,
	)
	return scanBinding(row)
}

func (s *Service) findOptional(credentialRef string, scope Scope, scopeID string) (*Binding, error) {
	binding, err := s.find(credentialRef, scope, scopeID)
	if errors.Is(err, ErrNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &binding, nil
}

func validateSource(source Source) error {
	if !sourceKinds[source.Kind] {
		return fmt.Errorf("%w: %q", ErrInvalidSourceKind, source.Kind)
	}
	if source.Kind == SourceCommand && !commandSourceEnabled() {
		return fmt.Errorf("%w; set %s=1 to enable", ErrCommandSourceDisabled, commandSourceOptInEnv)
	}
	if strings.TrimSpace(source.Value) == "" {
		return errors.New("source value is required")
	}
	if source.Kind == SourceEnv {
		if err := validateEnvSourceValue(source.Value); err != nil {
			return err
		}
	}
	if dest := strings.TrimSpace(source.DestinationEnv); dest != "" {
		if !envVarNamePattern.MatchString(dest) {
			return fmt.Errorf("destination_env must be an environment variable name, got %q", source.DestinationEnv)
		}
		if secretLikeEnvPattern.MatchString(dest) {
			return errors.New("destination_env looks like a secret value; use a variable name")
		}
	}
	return nil
}

func validateEnvSourceValue(value string) error {
	name := strings.TrimSpace(value)
	if !envVarNamePattern.MatchString(name) {
		return fmt.Errorf("env source must be an environment variable name, got %q", value)
	}
	if secretLikeEnvPattern.MatchString(name) {
		return errors.New("env source looks like a secret value; use literal source kind instead")
	}
	return nil
}

// SanitizeBinding returns a copy safe for API responses.
func SanitizeBinding(binding Binding) Binding {
	if binding.Source.Kind == SourceLiteral && strings.TrimSpace(binding.Source.Value) != "" {
		binding.Source.Value = ConfiguredSourceSentinel
	}
	return binding
}

// SanitizeBindings returns copies safe for API responses.
func SanitizeBindings(bindings []Binding) []Binding {
	if bindings == nil {
		return nil
	}
	out := make([]Binding, len(bindings))
	for i, binding := range bindings {
		out[i] = SanitizeBinding(binding)
	}
	return out
}

type scanner interface {
	Scan(dest ...any) error
}

func scanBinding(row scanner) (Binding, error) {
	var found Binding
	var scope string
	var sourceJSON string
	var disabled int
	var createdAt string
	var updatedAt string

	err := row.Scan(&found.ID, &found.CredentialRef, &scope, &found.ScopeID, &sourceJSON, &disabled, &createdAt, &updatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return Binding{}, ErrNotFound
	}
	if err != nil {
		return Binding{}, err
	}
	found.Scope = Scope(scope)
	found.Disabled = disabled != 0
	if err := decode(sourceJSON, &found.Source); err != nil {
		return Binding{}, fmt.Errorf("decode source: %w", err)
	}
	if found.CreatedAt, err = time.Parse(time.RFC3339Nano, createdAt); err != nil {
		return Binding{}, fmt.Errorf("parse created_at: %w", err)
	}
	if found.UpdatedAt, err = time.Parse(time.RFC3339Nano, updatedAt); err != nil {
		return Binding{}, fmt.Errorf("parse updated_at: %w", err)
	}
	return found, nil
}

func boolToInt(value bool) int {
	if value {
		return 1
	}
	return 0
}

func mustEncode(source Source) string {
	encoded, err := json.Marshal(source)
	if err != nil {
		// Source is a simple struct with string fields; encoding cannot fail in
		// practice, but fall back to an empty object to never block a write.
		return "{}"
	}
	return string(encoded)
}

func decode(raw string, target *Source) error {
	if raw == "" {
		return nil
	}
	return json.Unmarshal([]byte(raw), target)
}

func newID() string {
	var bytes [16]byte
	if _, err := rand.Read(bytes[:]); err != nil {
		return hex.EncodeToString([]byte(time.Now().UTC().Format(time.RFC3339Nano)))
	}
	return hex.EncodeToString(bytes[:])
}
