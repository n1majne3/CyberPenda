package runtimeprofile

import (
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/BurntSushi/toml"
	"gopkg.in/yaml.v3"
)

// ImportConfigRequest carries the edited provider-native config text for a
// Profile Config Import.
type ImportConfigRequest struct {
	ConfigText string
}

// ImportConfigKeyError reports one refused key from a Profile Config Import.
type ImportConfigKeyError struct {
	Key     string `json:"key"`
	Field   string `json:"field,omitempty"`
	Message string `json:"message"`
}

// ImportConfigError collects every refusal from one Profile Config Import.
// Nothing is persisted when it is returned.
type ImportConfigError struct {
	Provider Provider               `json:"provider"`
	Errors   []ImportConfigKeyError `json:"errors"`
}

func (e *ImportConfigError) Error() string {
	if e == nil || len(e.Errors) == 0 {
		return "profile config import refused"
	}
	parts := make([]string, 0, len(e.Errors))
	for _, keyErr := range e.Errors {
		if keyErr.Field != "" {
			parts = append(parts, fmt.Sprintf("%s: %s (owned by the %s field)", keyErr.Key, keyErr.Message, keyErr.Field))
			continue
		}
		parts = append(parts, fmt.Sprintf("%s: %s", keyErr.Key, keyErr.Message))
	}
	return fmt.Sprintf("profile config import refused: %s", strings.Join(parts, "; "))
}

// ValidateOverlaySecrets refuses a Custom Config File whose raw text contains
// a secret-shaped value. Every write path (Create, Update, Import) runs this
// so a Runtime Profile never stores secret values. Key-name scanning lives
// in refuseImportProblems, which sees the parsed document.
func ValidateOverlaySecrets(raw string) error {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return nil
	}
	if secretValuePattern.MatchString(trimmed) {
		return fmt.Errorf("custom config file contains a secret-shaped value; use the structured credential channels instead")
	}
	return nil
}

// ImportConfigResult reports what one Profile Config Import changed.
type ImportConfigResult struct {
	Profile    Profile
	MappedKeys []string
}

// secretKeyPattern matches key names that look like credential material. It
// mirrors the runner projection secret env key pattern, which is not
// importable from this package (import cycle); keep the two in sync.
var secretKeyPattern = regexp.MustCompile(`(?i)(token|api[_-]?key|secret|password|auth)`)

// secretValuePattern matches credential-shaped values from the providers
// CyberPenda projects. The editor renders redacted configs, so a value like
// this in imported text is a secret that must return to the structured
// credential channels instead of the stored overlay.
var secretValuePattern = regexp.MustCompile(`(?i)\b(sk-ant-api|sk-proj-|sk-svcacct-|sk-None-|sk-[A-Za-z0-9_-]{20,}|ghp_[A-Za-z0-9]{36}|github_pat_[A-Za-z0-9_]{22,}|xox[baprs]-[A-Za-z0-9-]{10,}|AKIA[0-9A-Z]{16})`)

// managedKeyDeclarations carries the Managed Config Key declarations the
// runtime plugin manifests own. The daemon injects them from the registry so
// policy stays data declared per Runtime Plugin Manifest, not a second
// hardcoded copy inside import logic.
type managedKeyDeclaration struct {
	Key       string
	Field     string
	Condition string
}

// ManagedKeyDeclaration is the injection shape the daemon passes from a
// Runtime Plugin Manifest into the Profile Config Import service.
type ManagedKeyDeclaration = managedKeyDeclaration

// SetManagedKeyDeclarations injects the Managed Config Key declarations the
// Runtime Plugin Manifests declare, keyed by provider.
func (s *Service) SetManagedKeyDeclarations(declarations map[Provider][]ManagedKeyDeclaration) {
	s.managedKeys = declarations
}

// importConfigFormat reports the provider-native config format.
func importConfigFormat(provider Provider) string {
	switch provider {
	case ProviderCodex:
		return "toml"
	case ProviderHermes:
		return "yaml"
	case ProviderClaudeCode, ProviderPi:
		return "json"
	default:
		return ""
	}
}

// ImportConfig runs one Profile Config Import: it parses the edited text,
// syncs keys structured fields express into those fields, refuses Managed
// Config Key changes and secret-shaped values, and stores the remainder as
// the Custom Config File in the same persisted step.
func (s *Service) ImportConfig(id string, request ImportConfigRequest) (ImportConfigResult, error) {
	existing, err := s.Get(id)
	if err != nil {
		return ImportConfigResult{}, err
	}
	format := importConfigFormat(existing.Provider)
	if format == "" {
		return ImportConfigResult{}, &ImportConfigError{
			Provider: existing.Provider,
			Errors: []ImportConfigKeyError{{
				Key:     "",
				Message: fmt.Sprintf("provider %s has no config projection to import", existing.Provider),
			}},
		}
	}

	var doc map[string]any
	switch format {
	case "json":
		if err := json.Unmarshal([]byte(request.ConfigText), &doc); err != nil {
			return ImportConfigResult{}, &ImportConfigError{
				Provider: existing.Provider,
				Errors: []ImportConfigKeyError{{
					Key:     "",
					Message: fmt.Sprintf("parse config text: %v", err),
				}},
			}
		}
	case "toml":
		doc = map[string]any{}
		if err := toml.Unmarshal([]byte(request.ConfigText), &doc); err != nil {
			return ImportConfigResult{}, &ImportConfigError{
				Provider: existing.Provider,
				Errors: []ImportConfigKeyError{{
					Key:     "",
					Message: fmt.Sprintf("parse config text: %v", err),
				}},
			}
		}
	case "yaml":
		doc = map[string]any{}
		if err := yaml.Unmarshal([]byte(request.ConfigText), &doc); err != nil {
			return ImportConfigResult{}, &ImportConfigError{
				Provider: existing.Provider,
				Errors: []ImportConfigKeyError{{
					Key:     "",
					Message: fmt.Sprintf("parse config text: %v", err),
				}},
			}
		}
	}
	if doc == nil {
		doc = map[string]any{}
	}

	if refusal := s.refuseImportProblems(existing, doc); refusal != nil {
		return ImportConfigResult{}, refusal
	}

	fields, remainder, mapped := mapImportConfigDocument(existing, doc, request.ConfigText)
	fields.CustomConfigFile = remainder
	normalized, err := normalizeFields(existing.Provider, fields)
	if err != nil {
		return ImportConfigResult{}, err
	}

	updated := existing
	updated.Fields = normalized
	updated.UpdatedAt = time.Now().UTC()
	fieldsJSON, err := json.Marshal(updated.Fields)
	if err != nil {
		return ImportConfigResult{}, fmt.Errorf("encode fields: %w", err)
	}
	if _, err := s.db.Exec(
		`UPDATE runtime_profiles SET fields_json = ?, updated_at = ? WHERE id = ?`,
		string(fieldsJSON), updated.UpdatedAt.Format(time.RFC3339Nano), updated.ID,
	); err != nil {
		return ImportConfigResult{}, fmt.Errorf("store imported runtime profile fields: %w", err)
	}
	return ImportConfigResult{Profile: updated, MappedKeys: mapped}, nil
}

// refuseImportProblems collects managed-key changes and secret-shaped values
// from the parsed document. Nil means the document is importable.
func (s *Service) refuseImportProblems(profile Profile, doc map[string]any) *ImportConfigError {
	var problems []ImportConfigKeyError
	managed := s.managedKeys[profile.Provider]
	resolved := strings.TrimSpace(profile.Fields.ModelProviderID) != ""

	var walk func(prefix string, node map[string]any)
	walk = func(prefix string, node map[string]any) {
		for key, value := range node {
			path := key
			if prefix != "" {
				path = prefix + "." + key
			}
			if declaration, matched := managedKeyForPath(managed, path, resolved); matched {
				problems = append(problems, ImportConfigKeyError{
					Key:     path,
					Field:   declaration.Field,
					Message: "managed key is re-derived at every config projection; change the structured field instead",
				})
				continue
			}
			if secretKeyPattern.MatchString(key) {
				problems = append(problems, ImportConfigKeyError{
					Key:     path,
					Message: "key looks like a secret; use the API keys structured field instead",
				})
				continue
			}
			if text, ok := value.(string); ok && secretValuePattern.MatchString(text) {
				problems = append(problems, ImportConfigKeyError{
					Key:     path,
					Message: "value looks like a secret-shaped credential; use the structured credential channels instead",
				})
				continue
			}
			if child, ok := value.(map[string]any); ok {
				walk(path, child)
			}
		}
	}
	walk("", doc)

	if len(problems) == 0 {
		return nil
	}
	sort.Slice(problems, func(i, j int) bool { return problems[i].Key < problems[j].Key })
	return &ImportConfigError{Provider: profile.Provider, Errors: problems}
}

// managedKeyForPath reports whether the dotted path is covered by a managed
// key declaration. A "*" declaration segment matches one path level; a
// declaration that names a whole subtree ("providers") covers every deeper
// path under it.
func managedKeyForPath(declarations []managedKeyDeclaration, path string, providerResolved bool) (managedKeyDeclaration, bool) {
	for _, declaration := range declarations {
		if declaration.Condition == "model_provider_resolved" && !providerResolved {
			continue
		}
		if managedPathCovers(declaration.Key, path) {
			return declaration, true
		}
	}
	return managedKeyDeclaration{}, false
}

func managedPathCovers(managed, path string) bool {
	managedParts := strings.Split(managed, ".")
	pathParts := strings.Split(path, ".")
	if len(managedParts) > len(pathParts) {
		return false
	}
	for i, part := range managedParts {
		if part == "*" {
			continue
		}
		if part != pathParts[i] {
			return false
		}
	}
	return true
}

// mapImportConfigDocument syncs structured-expressible keys into the profile
// fields and renders the remainder back as provider-native text.
func mapImportConfigDocument(profile Profile, doc map[string]any, rawText string) (Fields, string, []string) {
	fields := profile.Fields
	var mapped []string

	remaining := make(map[string]any, len(doc))
	for key, value := range doc {
		remaining[key] = value
	}

	if envNode, ok := remaining["env"].(map[string]any); ok && len(envNode) > 0 && existingEnvMappable(profile.Provider) {
		if fields.Env == nil {
			fields.Env = map[string]string{}
		}
		for envKey, envValue := range envNode {
			if text, ok := envValue.(string); ok {
				fields.Env[envKey] = text
			}
		}
		mapped = append(mapped, "env")
		delete(remaining, "env")
	}

	// A top-level Codex `model` maps into the structured model
	// field whenever it is not a Managed Config Key (model provider resolved)
	// — the structured form stays the single source of truth.
	if profile.Provider == ProviderCodex && strings.TrimSpace(profile.Fields.ModelProviderID) == "" {
		if model, ok := remaining["model"].(string); ok && strings.TrimSpace(model) != "" {
			fields.Model = strings.TrimSpace(model)
			mapped = append(mapped, "model")
			delete(remaining, "model")
		}
	}

	// The operator's raw text is preserved verbatim — comments and
	// formatting included — whenever nothing mapped away. Only when keys were
	// extracted into structured fields is the remainder re-encoded.
	if len(mapped) == 0 {
		return fields, rawText, mapped
	}
	remainder := renderRemainder(profile.Provider, remaining)
	return fields, remainder, mapped
}

// existingEnvMappable reports whether the provider's main config carries an
// env map that maps onto the structured env field.
func existingEnvMappable(provider Provider) bool {
	return provider == ProviderClaudeCode
}

// renderRemainder re-encodes the un-mappable remainder as provider-native
// text, preserving the operator's format expectations.
func renderRemainder(provider Provider, remaining map[string]any) string {
	if len(remaining) == 0 {
		return ""
	}
	switch importConfigFormat(provider) {
	case "json":
		raw, err := json.MarshalIndent(remaining, "", "  ")
		if err != nil {
			return ""
		}
		return string(raw)
	case "toml":
		var b strings.Builder
		if err := toml.NewEncoder(&b).Encode(remaining); err != nil {
			return ""
		}
		return b.String()
	case "yaml":
		var b strings.Builder
		encoder := yaml.NewEncoder(&b)
		encoder.SetIndent(2)
		if err := encoder.Encode(remaining); err != nil {
			return ""
		}
		_ = encoder.Close()
		return b.String()
	default:
		return ""
	}
}
