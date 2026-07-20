// Package runtimeprofile owns the global runtime profile domain. A runtime
// profile is a global, user-editable configuration that chooses how a runtime
// should run for a task without storing secret values. Structured fields are the
// source of truth; generated config preview is derived, never edited directly.
package runtimeprofile

import (
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"pentest/internal/store"
)

// Provider names the runtime CLI or assistant process family.
type Provider string

const (
	// ProviderFake is the fake runtime used to exercise the harness and project
	// interfaces before real adapters exist.
	ProviderFake Provider = "fake"
	// ProviderCodex is the Codex runtime.
	ProviderCodex Provider = "codex"
	// ProviderClaudeCode is the Claude Code runtime.
	ProviderClaudeCode Provider = "claude_code"
	// ProviderPi is the Pi runtime.
	ProviderPi Provider = "pi"
)

// providers is the set of providers the product knows about.
var providers = map[Provider]bool{
	ProviderFake:       true,
	ProviderCodex:      true,
	ProviderClaudeCode: true,
	ProviderPi:         true,
}

// MCPServerMode marks an MCP server as trusted (a project interface) or
// external (available to the runtime but not trusted for project writes).
type MCPServerMode string

const (
	MCPServerTrusted  MCPServerMode = "trusted"
	MCPServerExternal MCPServerMode = "external"
)

// MCPServer is one structured MCP configuration entry. It is managed as a
// structured field, not a raw JSON blob.
type MCPServer struct {
	Name    string            `json:"name,omitempty"`
	Mode    MCPServerMode     `json:"mode,omitempty"`
	Command string            `json:"command,omitempty"`
	URL     string            `json:"url,omitempty"`
	Args    []string          `json:"args,omitempty"`
	Env     map[string]string `json:"env,omitempty"`
}

// RuntimeExtensionRef enables a runtime-native extension for profiles using a
// compatible runtime plugin. Config is non-secret per-profile extension input.
type RuntimeExtensionRef struct {
	ID      string            `json:"id"`
	Enabled *bool             `json:"enabled,omitempty"`
	Config  map[string]string `json:"config,omitempty"`
}

// Fields are the structured runtime profile fields. They are the source of
// truth for the generated config preview. Inline APIKeys are stored per profile
// and redacted in API responses; legacy CredentialRefs still resolve through
// global credential bindings when present.
type Fields struct {
	BinaryPath            string `json:"binary_path,omitempty"`
	Model                 string `json:"model,omitempty"`
	Endpoint              string `json:"endpoint,omitempty"`
	ModelProviderID       string `json:"model_provider_id,omitempty"`
	ModelProviderProtocol string `json:"model_provider_protocol,omitempty"`
	ModelOverride         string `json:"model_override,omitempty"`
	// ReasoningEffort is the optional Profile default for Requested Reasoning
	// Effort. When empty, resolution yields high without rewriting storage.
	ReasoningEffort   string                `json:"reasoning_effort,omitempty"`
	CustomArgs        []string              `json:"custom_args,omitempty"`
	Env               map[string]string     `json:"env,omitempty"`
	APIKeys           map[string]string     `json:"api_keys,omitempty"`
	CredentialRefs    []string              `json:"credential_refs,omitempty"`
	RuntimeExtensions []RuntimeExtensionRef `json:"runtime_extensions,omitempty"`
	MCPServers        []MCPServer           `json:"mcp_servers,omitempty"`
	DefaultRunner     string                `json:"default_runner,omitempty"`
	// SandboxImage overrides the daemon default sandbox image for tasks using
	// this profile. Leave empty to use the daemon-wide setting.
	SandboxImage string `json:"sandbox_image,omitempty"`
}

// ProfileKind classifies how a runtime profile was created.
type ProfileKind string

const (
	// ProfileKindManual marks user-authored presets intended for advanced launch.
	ProfileKindManual ProfileKind = "manual"
	// ProfileKindLaunchResolve marks minimal profiles created by launch resolution.
	ProfileKindLaunchResolve ProfileKind = "launch_resolve"
)

// Profile is a global runtime profile reusable across projects.
type Profile struct {
	ID        string      `json:"id"`
	Name      string      `json:"name"`
	Provider  Provider    `json:"provider"`
	Kind      ProfileKind `json:"kind"`
	Fields    Fields      `json:"fields"`
	CreatedAt time.Time   `json:"created_at"`
	UpdatedAt time.Time   `json:"updated_at"`
}

// ErrNotFound is returned when no profile matches the requested id.
var ErrNotFound = errors.New("runtime profile not found")

// Sentinel validation errors.
var (
	ErrMissingName     = errors.New("runtime profile name is required")
	ErrMissingProvider = errors.New("runtime profile provider is required")
	ErrUnknownProvider = errors.New("runtime profile provider is not supported")
)

// Service implements runtime profile business rules against SQLite.
type Service struct {
	db        *store.DB
	providers map[Provider]bool
}

// NewService returns a Service backed by the given database.
func NewService(db *store.DB, supportedProviders ...[]Provider) *Service {
	svc := &Service{db: db, providers: defaultProviderSet()}
	if len(supportedProviders) > 0 {
		svc.providers = providerSet(supportedProviders[0])
	}
	return svc
}

// Create stores a new user-authored runtime profile preset and returns it.
func (s *Service) Create(name string, provider Provider, fields Fields) (Profile, error) {
	return s.create(name, provider, fields, ProfileKindManual)
}

// CreateLaunchResolved stores a minimal profile created by launch resolution.
func (s *Service) CreateLaunchResolved(name string, provider Provider, fields Fields) (Profile, error) {
	return s.create(name, provider, fields, ProfileKindLaunchResolve)
}

// PromoteToPreset marks a launch-resolved profile as a user-authored preset.
func (s *Service) PromoteToPreset(id string) (Profile, error) {
	existing, err := s.Get(id)
	if err != nil {
		return Profile{}, err
	}
	if existing.Kind == ProfileKindManual {
		return existing, nil
	}
	existing.Kind = ProfileKindManual
	existing.UpdatedAt = time.Now().UTC()
	_, err = s.db.Exec(
		`UPDATE runtime_profiles SET kind = ?, updated_at = ? WHERE id = ?`,
		string(existing.Kind), existing.UpdatedAt.Format(time.RFC3339Nano), existing.ID,
	)
	if err != nil {
		return Profile{}, fmt.Errorf("promote runtime profile: %w", err)
	}
	return existing, nil
}

func (s *Service) create(name string, provider Provider, fields Fields, kind ProfileKind) (Profile, error) {
	if err := s.validate(name, provider); err != nil {
		return Profile{}, err
	}
	normalizedFields, err := normalizeFields(provider, fields)
	if err != nil {
		return Profile{}, err
	}

	now := time.Now().UTC()
	created := Profile{
		ID:        newID(),
		Name:      strings.TrimSpace(name),
		Provider:  provider,
		Kind:      kind,
		Fields:    normalizedFields,
		CreatedAt: now,
		UpdatedAt: now,
	}

	fieldsJSON, err := json.Marshal(created.Fields)
	if err != nil {
		return Profile{}, fmt.Errorf("encode fields: %w", err)
	}

	_, err = s.db.Exec(
		`INSERT INTO runtime_profiles (id, name, provider, kind, fields_json, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		created.ID, created.Name, string(created.Provider), string(created.Kind), string(fieldsJSON),
		created.CreatedAt.Format(time.RFC3339Nano), created.UpdatedAt.Format(time.RFC3339Nano),
	)
	if err != nil {
		return Profile{}, fmt.Errorf("store runtime profile: %w", err)
	}
	return created, nil
}

// Get loads a single profile by id.
func (s *Service) Get(id string) (Profile, error) {
	return scanProfile(s.db.QueryRow(
		`SELECT id, name, provider, kind, fields_json, created_at, updated_at FROM runtime_profiles WHERE id = ?`,
		id,
	))
}

// List returns all profiles ordered by creation time.
func (s *Service) List() ([]Profile, error) {
	rows, err := s.db.Query(
		`SELECT id, name, provider, kind, fields_json, created_at, updated_at FROM runtime_profiles ORDER BY created_at ASC`,
	)
	if err != nil {
		return nil, fmt.Errorf("list runtime profiles: %w", err)
	}
	defer rows.Close()

	var profiles []Profile
	for rows.Next() {
		found, err := scanProfile(rows)
		if err != nil {
			return nil, fmt.Errorf("scan runtime profile: %w", err)
		}
		profiles = append(profiles, found)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list runtime profiles: %w", err)
	}
	return profiles, nil
}

// Update applies non-empty fields to an existing profile. An empty provider is
// rejected; omitted structured fields preserve the existing values so partial
// edits do not erase a working configuration.
func (s *Service) Update(id, name string, provider Provider, fields Fields, fieldsTouched bool) (Profile, error) {
	existing, err := s.Get(id)
	if err != nil {
		return Profile{}, err
	}

	// Name: empty string means "leave unchanged".
	if strings.TrimSpace(name) != "" {
		existing.Name = strings.TrimSpace(name)
	}
	// Provider: must always be valid; empty means keep current.
	if provider != "" {
		if err := s.validate(existing.Name, provider); err != nil {
			return Profile{}, err
		}
		existing.Provider = provider
	} else if err := s.validate(existing.Name, existing.Provider); err != nil {
		// Should not happen for a stored profile, but guard anyway.
		return Profile{}, err
	}
	if fieldsTouched {
		normalizedFields, err := normalizeFields(existing.Provider, fields)
		if err != nil {
			return Profile{}, err
		}
		mergedAPIKeys := MergeAPIKeys(existing.Fields.APIKeys, normalizedFields.APIKeys)
		existing.Fields = normalizedFields
		if strings.TrimSpace(existing.Fields.ModelProviderID) == "" {
			existing.Fields.APIKeys = mergedAPIKeys
		} else {
			existing.Fields.APIKeys = nil
		}
	} else if err := ValidateCustomArgs(existing.Provider, existing.Fields.CustomArgs); err != nil {
		// Provider-only edits still reject Custom Args that conflict under the
		// resolved provider family. Args are not rewritten.
		return Profile{}, err
	}
	existing.UpdatedAt = time.Now().UTC()

	fieldsJSON, err := json.Marshal(existing.Fields)
	if err != nil {
		return Profile{}, fmt.Errorf("encode fields: %w", err)
	}
	_, err = s.db.Exec(
		`UPDATE runtime_profiles SET name = ?, provider = ?, fields_json = ?, updated_at = ? WHERE id = ?`,
		existing.Name, string(existing.Provider), string(fieldsJSON),
		existing.UpdatedAt.Format(time.RFC3339Nano), existing.ID,
	)
	if err != nil {
		return Profile{}, fmt.Errorf("store runtime profile update: %w", err)
	}
	return existing, nil
}

// ReplaceFields replaces the structured fields on a profile without merging
// inline API keys. Management flows such as model-provider migration use this
// to clear legacy model-service fields explicitly.
func (s *Service) ReplaceFields(id string, fields Fields) (Profile, error) {
	existing, err := s.Get(id)
	if err != nil {
		return Profile{}, err
	}
	if err := s.validate(existing.Name, existing.Provider); err != nil {
		return Profile{}, err
	}
	normalizedFields, err := normalizeFields(existing.Provider, fields)
	if err != nil {
		return Profile{}, err
	}
	existing.Fields = normalizedFields
	existing.UpdatedAt = time.Now().UTC()

	fieldsJSON, err := json.Marshal(existing.Fields)
	if err != nil {
		return Profile{}, fmt.Errorf("encode fields: %w", err)
	}
	_, err = s.db.Exec(
		`UPDATE runtime_profiles SET fields_json = ?, updated_at = ? WHERE id = ?`,
		string(fieldsJSON), existing.UpdatedAt.Format(time.RFC3339Nano), existing.ID,
	)
	if err != nil {
		return Profile{}, fmt.Errorf("store runtime profile fields: %w", err)
	}
	return existing, nil
}

// Delete removes a profile. It does not cascade into tasks; tasks capture their
// own runtime configuration at launch.
func (s *Service) Delete(id string) error {
	result, err := s.db.Exec(`DELETE FROM runtime_profiles WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("delete runtime profile: %w", err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("delete runtime profile: %w", err)
	}
	if affected == 0 {
		return ErrNotFound
	}
	return nil
}

// GeneratedConfig returns a previewable generated runtime config derived from
// the structured fields. It is never the source of truth. It never contains
// secret values; credentials enter through bindings at config projection time.
func GeneratedConfig(profile Profile) map[string]any {
	cfg := map[string]any{
		"provider": string(profile.Provider),
	}
	if profile.Fields.BinaryPath != "" {
		cfg["binary"] = profile.Fields.BinaryPath
	}
	if profile.Fields.Model != "" {
		cfg["model"] = profile.Fields.Model
	}
	if profile.Fields.Endpoint != "" {
		cfg["endpoint"] = profile.Fields.Endpoint
	}
	if profile.Fields.ModelProviderID != "" {
		cfg["model_provider_id"] = profile.Fields.ModelProviderID
	}
	if profile.Fields.ModelProviderProtocol != "" {
		cfg["model_provider_protocol"] = profile.Fields.ModelProviderProtocol
	}
	if profile.Fields.ModelOverride != "" {
		cfg["model_override"] = profile.Fields.ModelOverride
	}
	if profile.Fields.ReasoningEffort != "" {
		cfg["reasoning_effort"] = profile.Fields.ReasoningEffort
	}
	if len(profile.Fields.CustomArgs) > 0 {
		cfg["custom_args"] = profile.Fields.CustomArgs
	}
	if len(profile.Fields.Env) > 0 {
		cfg["env"] = profile.Fields.Env
	}
	if len(profile.Fields.APIKeys) > 0 {
		cfg["api_keys"] = SanitizeAPIKeys(profile.Fields.APIKeys)
	}
	if len(profile.Fields.CredentialRefs) > 0 {
		// Emit references, never resolved values.
		cfg["credential_refs"] = profile.Fields.CredentialRefs
	}
	if len(profile.Fields.RuntimeExtensions) > 0 {
		extensions := make([]map[string]any, 0, len(profile.Fields.RuntimeExtensions))
		for _, extension := range profile.Fields.RuntimeExtensions {
			entry := map[string]any{"id": extension.ID}
			if extension.Enabled != nil {
				entry["enabled"] = *extension.Enabled
			}
			if len(extension.Config) > 0 {
				entry["config"] = extension.Config
			}
			extensions = append(extensions, entry)
		}
		cfg["runtime_extensions"] = extensions
	}
	if len(profile.Fields.MCPServers) > 0 {
		servers := make([]map[string]any, 0, len(profile.Fields.MCPServers))
		for _, server := range profile.Fields.MCPServers {
			entry := map[string]any{
				"name": server.Name,
				"mode": string(server.Mode),
			}
			if server.Command != "" {
				entry["command"] = server.Command
			}
			if server.URL != "" {
				entry["url"] = server.URL
			}
			if len(server.Args) > 0 {
				entry["args"] = server.Args
			}
			if len(server.Env) > 0 {
				entry["env"] = server.Env
			}
			servers = append(servers, entry)
		}
		cfg["mcp_servers"] = servers
	}
	if profile.Fields.DefaultRunner != "" {
		cfg["default_runner"] = profile.Fields.DefaultRunner
	}
	if profile.Fields.SandboxImage != "" {
		cfg["sandbox_image"] = profile.Fields.SandboxImage
	}
	return cfg
}

func (s *Service) validate(name string, provider Provider) error {
	if strings.TrimSpace(name) == "" {
		return ErrMissingName
	}
	if provider == "" {
		return ErrMissingProvider
	}
	if !s.providers[provider] {
		return fmt.Errorf("%w: %q", ErrUnknownProvider, provider)
	}
	return nil
}

// normalizeFields validates structured fields that have closed vocabularies and
// rejects Custom Args that redefine structured Model Provider, model, or
// Reasoning Effort controls. Empty ReasoningEffort is preserved so existing
// Profiles are not rewritten. Custom Args are never migrated, stripped, or
// reordered.
func normalizeFields(provider Provider, fields Fields) (Fields, error) {
	if err := ValidateCustomArgs(provider, fields.CustomArgs); err != nil {
		return Fields{}, err
	}
	if strings.TrimSpace(fields.ReasoningEffort) == "" {
		fields.ReasoningEffort = ""
		return fields, nil
	}
	effort, err := NormalizeReasoningEffort(fields.ReasoningEffort)
	if err != nil {
		return Fields{}, err
	}
	fields.ReasoningEffort = string(effort)
	return fields, nil
}

func defaultProviderSet() map[Provider]bool {
	return providerSet([]Provider{ProviderFake, ProviderCodex, ProviderClaudeCode, ProviderPi})
}

func providerSet(providerList []Provider) map[Provider]bool {
	out := map[Provider]bool{}
	for _, provider := range providerList {
		if provider != "" {
			out[provider] = true
		}
	}
	return out
}

type scanner interface {
	Scan(dest ...any) error
}

func scanProfile(row scanner) (Profile, error) {
	var found Profile
	var fieldsJSON string
	var provider string
	var kind string
	var createdAt string
	var updatedAt string

	err := row.Scan(&found.ID, &found.Name, &provider, &kind, &fieldsJSON, &createdAt, &updatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return Profile{}, ErrNotFound
	}
	if err != nil {
		return Profile{}, err
	}
	found.Provider = Provider(provider)
	found.Kind = ProfileKind(kind)
	if found.Kind == "" {
		found.Kind = ProfileKindManual
	}
	if err := json.Unmarshal([]byte(fieldsJSON), &found.Fields); err != nil {
		return Profile{}, fmt.Errorf("decode fields: %w", err)
	}
	if found.CreatedAt, err = time.Parse(time.RFC3339Nano, createdAt); err != nil {
		return Profile{}, fmt.Errorf("parse created_at: %w", err)
	}
	if found.UpdatedAt, err = time.Parse(time.RFC3339Nano, updatedAt); err != nil {
		return Profile{}, fmt.Errorf("parse updated_at: %w", err)
	}
	return found, nil
}

func newID() string {
	var bytes [16]byte
	if _, err := rand.Read(bytes[:]); err != nil {
		return hex.EncodeToString([]byte(time.Now().UTC().Format(time.RFC3339Nano)))
	}
	return hex.EncodeToString(bytes[:])
}
