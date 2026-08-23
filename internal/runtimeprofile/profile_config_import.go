package runtimeprofile

import (
	"encoding/json"
	"fmt"
	"reflect"
	"regexp"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/BurntSushi/toml"
	"gopkg.in/yaml.v3"
)

// ImportConfigRequest carries the edited provider-native config text for a
// Profile Config Import. The Managed Config Key baseline is derived by the
// service from the injected projector — never from this request — so a
// client cannot forge it.
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

// ValidateOverlaySecrets refuses a Custom Config File that contains a
// secret-shaped value or a secret-named key. Every write path (Create,
// Update, Import) runs this so a Runtime Profile never stores secret values.
func ValidateOverlaySecrets(raw string) error {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return nil
	}
	if secretValuePattern.MatchString(trimmed) {
		return fmt.Errorf("custom config file contains a secret-shaped value; use the structured credential channels instead")
	}
	if doc := parseOverlayAny(trimmed); doc != nil {
		if err := scanSecretNodes(doc); err != nil {
			return err
		}
	}
	return nil
}

func parseOverlayAny(raw string) map[string]any {
	var doc map[string]any
	if err := json.Unmarshal([]byte(raw), &doc); err == nil && doc != nil {
		return doc
	}
	doc = map[string]any{}
	if err := toml.Unmarshal([]byte(raw), &doc); err == nil && len(doc) > 0 {
		return doc
	}
	doc = map[string]any{}
	if err := yaml.Unmarshal([]byte(raw), &doc); err == nil && len(doc) > 0 {
		return doc
	}
	return nil
}

func scanSecretNodes(node any) error {
	switch typed := node.(type) {
	case map[string]any:
		for key, value := range typed {
			if secretKeyPattern.MatchString(key) {
				return fmt.Errorf("custom config file key %q looks like a secret; use the API keys structured field instead", key)
			}
			if text, ok := value.(string); ok && secretValuePattern.MatchString(text) {
				return fmt.Errorf("custom config file contains a secret-shaped value; use the structured credential channels instead")
			}
			if err := scanSecretNodes(value); err != nil {
				return err
			}
		}
	case []any:
		for _, item := range typed {
			if err := scanSecretNodes(item); err != nil {
				return err
			}
		}
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

// SetImportBaseline injects the daemon-side projector that renders the
// current provider-native config for a profile. ImportConfig derives the
// Managed Config Key baseline from it, so the baseline is authoritative and
// never client-supplied.
func (s *Service) SetImportBaseline(projector func(Profile) (string, error)) {
	s.importBaseline = projector
}

// SetImportBaselineProvenance injects the daemon-side projector together with
// the config paths the daemon itself generated — credential-derived env keys
// rendered as placeholders. Import treats those paths as credential-channel
// output instead of guessing provenance from a sentinel value.
func (s *Service) SetImportBaselineProvenance(projector func(Profile) (string, []string, error)) {
	s.importBaselineProvenance = projector
	s.importBaseline = func(profile Profile) (string, error) {
		text, _, err := projector(profile)
		return text, err
	}
}

// SetKnownInstallRefs injects the resolver that reports the install refs
// the runtime extension catalog currently offers, so imports map known
// plugins back into the structured Runtime Extensions field instead of the
// Custom Config File.
func (s *Service) SetKnownInstallRefs(resolver func() []string) {
	s.knownInstallRefs = resolver
}

func (s *Service) knownInstallRefList() []string {
	if s == nil || s.knownInstallRefs == nil {
		return nil
	}
	return s.knownInstallRefs()
}

func (s *Service) isKnownInstallRef(ref string) bool {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return false
	}
	if slices.Contains(s.knownInstallRefList(), ref) {
		return true
	}
	// Official Claude marketplace plugins are catalog-known without a
	// network fetch: warp@claude-code-warp stays unknown remainder.
	return strings.HasSuffix(ref, "@claude-plugins-official")
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

	doc, err := parseConfigDocument(format, request.ConfigText)
	if err != nil {
		return ImportConfigResult{}, &ImportConfigError{
			Provider: existing.Provider,
			Errors: []ImportConfigKeyError{{
				Key:     "",
				Message: fmt.Sprintf("parse config text: %v", err),
			}},
		}
	}
	if doc == nil {
		doc = map[string]any{}
	}

	baseline, generatedPaths, err := s.importBaselineWithProvenance(existing, format)
	if err != nil {
		return ImportConfigResult{}, &ImportConfigError{
			Provider: existing.Provider,
			Errors: []ImportConfigKeyError{{
				Key:     "",
				Message: fmt.Sprintf("derive managed key baseline: %v", err),
			}},
		}
	}
	if refusal := s.refuseImportProblems(existing, doc, baseline, generatedPaths); refusal != nil {
		return ImportConfigResult{}, refusal
	}

	fields, remainder, mapped := s.mapImportConfigDocument(existing, doc, request.ConfigText, s.managedKeys[existing.Provider], baseline, generatedPaths)
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

// refuseImportProblems collects managed-key *changes* and secret-shaped
// values from the parsed document. Unchanged managed keys matching the
// projected baseline are not a change. Nil means the document is importable.
func (s *Service) refuseImportProblems(profile Profile, doc, baseline map[string]any, generatedPaths []string) *ImportConfigError {
	var problems []ImportConfigKeyError
	managed := s.managedKeys[profile.Provider]
	resolved := strings.TrimSpace(profile.Fields.ModelProviderID) != ""

	var walk func(prefix string, node any)
	walk = func(prefix string, node any) {
		switch typed := node.(type) {
		case map[string]any:
			for key, value := range typed {
				path := key
				if prefix != "" {
					path = prefix + "." + key
				}
				if declaration, matched := managedKeyForPath(managed, path, resolved); matched {
					if !managedValueUnchanged(baseline, path, value) {
						problems = append(problems, ImportConfigKeyError{
							Key:     path,
							Field:   declaration.Field,
							Message: "managed key is re-derived at every config projection; change the structured field instead",
						})
					}
					continue
				}
				// Credential-generated paths carry daemon provenance: the value
				// must stay the placeholder. Replacement or deletion is a
				// credential-channel change, never operator env data.
				if slices.Contains(generatedPaths, path) {
					text, isText := value.(string)
					if !isText || text != generatedRedactionPlaceholder {
						problems = append(problems, ImportConfigKeyError{
							Key:     path,
							Field:   "credentials",
							Message: "credential-generated value must stay redacted; change the credential binding instead",
						})
					}
					continue
				}
				// MCP config sections are out of scope for the Custom Config
				// File. The generated section may be stripped silently; any
				// drift must go through the structured MCPServers field.
				if key == "mcp_servers" && prefix == "" {
					if !managedValueUnchanged(baseline, "mcp_servers", value) {
						problems = append(problems, ImportConfigKeyError{
							Key:     "mcp_servers",
							Field:   "mcp_servers",
							Message: "MCP config is harness-generated; change the structured MCP servers field instead",
						})
					}
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
				walk(path, value)
			}
		case []any:
			for _, item := range typed {
				walk(prefix, item)
			}
		}
	}
	walk("", doc)

	var walkBaseline func(prefix string, node any)
	// walkBaseline refuses a Managed Config Key that vanished from the
	// edited document. Non-managed keys may be deleted freely.
	walkBaseline = func(prefix string, node any) {
		switch typed := node.(type) {
		case map[string]any:
			for key, value := range typed {
				path := key
				if prefix != "" {
					path = prefix + "." + key
				}
				declaration, matched := managedKeyForPath(managed, path, resolved)
				if matched {
					if _, present := lookupPath(doc, path); !present {
						problems = append(problems, ImportConfigKeyError{
							Key:     path,
							Field:   declaration.Field,
							Message: "managed key is re-derived at every config projection; deleting it is not allowed",
						})
					}
					continue
				}
				walkBaseline(path, value)
			}
		case []any:
			for _, item := range typed {
				walkBaseline(prefix, item)
			}
		}
	}

	// Deletion check: a Managed Config Key present in the baseline but
	// missing from the edited document is a change, too.
	walkBaseline("", baseline)

	// Credential-generated paths must survive the edit: deleting one is a
	// credential-channel change, not an env cleanup.
	for _, path := range generatedPaths {
		if _, present := lookupPath(doc, path); !present {
			problems = append(problems, ImportConfigKeyError{
				Key:     path,
				Field:   "credentials",
				Message: "credential-generated value must stay redacted; change the credential binding instead",
			})
		}
	}

	if len(problems) == 0 {
		return nil
	}
	sort.Slice(problems, func(i, j int) bool { return problems[i].Key < problems[j].Key })
	return &ImportConfigError{Provider: profile.Provider, Errors: problems}
}

// importBaselineText derives the Managed Config Key comparison baseline by
// rendering the profile's current provider-native config through the
// injected projector. No projector means no managed keys exist to compare,
// so an empty baseline is correct.
func (s *Service) importBaselineText(profile Profile, format string) (map[string]any, error) {
	if s.importBaseline == nil {
		return map[string]any{}, nil
	}
	projected, err := s.importBaseline(profile)
	if err != nil {
		return nil, err
	}
	baseline, err := parseConfigDocument(format, projected)
	if err != nil {
		return nil, fmt.Errorf("parse projected baseline: %w", err)
	}
	return baseline, nil
}

// generatedRedactionPlaceholder is the value the preview renders for
// credential-derived env keys. Import treats it as daemon provenance.
const generatedRedactionPlaceholder = "[REDACTED]"

// isGeneratedRedaction reports whether the edited path carries the daemon's
// own redaction placeholder for a credential-derived baseline entry.
func isGeneratedRedaction(baseline map[string]any, path, value string) bool {
	if value != generatedRedactionPlaceholder {
		return false
	}
	current, ok := lookupPath(baseline, path)
	if !ok {
		return false
	}
	text, ok := current.(string)
	return ok && text == generatedRedactionPlaceholder
}

// importBaselineWithProvenance derives the Managed Config Key baseline plus
// the config paths the daemon itself generated (credential-derived env keys
// rendered as placeholders).
func (s *Service) importBaselineWithProvenance(profile Profile, format string) (map[string]any, []string, error) {
	if s.importBaselineProvenance != nil {
		projected, generated, err := s.importBaselineProvenance(profile)
		if err != nil {
			return nil, nil, err
		}
		baseline, err := parseConfigDocument(format, projected)
		if err != nil {
			return nil, nil, fmt.Errorf("parse projected baseline: %w", err)
		}
		return baseline, generated, nil
	}
	baseline, err := s.importBaselineText(profile, format)
	return baseline, nil, err
}

func parseConfigDocument(format, raw string) (map[string]any, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return map[string]any{}, nil
	}
	var doc map[string]any
	var err error
	switch format {
	case "json":
		err = json.Unmarshal([]byte(trimmed), &doc)
	case "toml":
		doc = map[string]any{}
		err = toml.Unmarshal([]byte(trimmed), &doc)
	case "yaml":
		doc = map[string]any{}
		err = yaml.Unmarshal([]byte(trimmed), &doc)
	default:
		return nil, fmt.Errorf("unknown config format %q", format)
	}
	if err != nil {
		return nil, err
	}
	if doc == nil {
		doc = map[string]any{}
	}
	return doc, nil
}

func lookupPath(doc map[string]any, path string) (any, bool) {
	if doc == nil || path == "" {
		return nil, false
	}
	parts := strings.Split(path, ".")
	var current any = doc
	for _, part := range parts {
		asMap, ok := current.(map[string]any)
		if !ok {
			return nil, false
		}
		next, exists := asMap[part]
		if !exists {
			return nil, false
		}
		current = next
	}
	return current, true
}

func managedValueUnchanged(baseline map[string]any, path string, value any) bool {
	current, ok := lookupPath(baseline, path)
	if !ok {
		return false
	}
	return valuesMatchBaseline(path, current, value)
}

// valuesMatchBaseline compares an edited value against its baseline.
// Arrays compare by deep equality except for the Hermes plugins.enabled
// list, which locks only harness-derived entries (Story 21).
func valuesMatchBaseline(path string, baselineValue, editedValue any) bool {
	baseArr, baseOK := baselineValue.([]any)
	editArr, editOK := editedValue.([]any)
	if baseOK && editOK && path == "plugins.enabled" {
		for _, base := range baseArr {
			found := false
			for _, edited := range editArr {
				if reflect.DeepEqual(normalizeComparable(base), normalizeComparable(edited)) {
					found = true
					break
				}
			}
			if !found {
				return false
			}
		}
		return true
	}
	if baseOK != editOK {
		return false
	}
	return reflect.DeepEqual(normalizeComparable(baselineValue), normalizeComparable(editedValue))
}

func normalizeComparable(value any) any {
	switch typed := value.(type) {
	case map[string]any:
		out := make(map[string]any, len(typed))
		for key, item := range typed {
			out[key] = normalizeComparable(item)
		}
		return out
	case []any:
		out := make([]any, len(typed))
		for i, item := range typed {
			out[i] = normalizeComparable(item)
		}
		return out
	default:
		return value
	}
}

func stripUnchangedManagedKeys(remaining map[string]any, declarations []managedKeyDeclaration, baseline map[string]any, resolved bool) ([]string, bool) {
	stripped := false
	var strippedKeys []string
	var walk func(prefix string, node map[string]any)
	walk = func(prefix string, node map[string]any) {
		for key, value := range node {
			path := key
			if prefix != "" {
				path = prefix + "." + key
			}
			if _, matched := managedKeyForPath(declarations, path, resolved); matched {
				if managedValueUnchanged(baseline, path, value) {
					// Hermes plugins.enabled keeps operator-added entries
					// in the remainder. Every other managed array is a
					// whole leaf: unchanged means strip it entirely.
					if arr, ok := value.([]any); ok && path == "plugins.enabled" {
						baseArr, _ := lookupPath(baseline, path)
						if baseList, ok := baseArr.([]any); ok {
							kept := make([]any, 0, len(arr))
							for _, entry := range arr {
								derived := false
								for _, base := range baseList {
									if reflect.DeepEqual(normalizeComparable(base), normalizeComparable(entry)) {
										derived = true
										break
									}
								}
								if !derived {
									kept = append(kept, entry)
								}
							}
							if len(kept) == 0 {
								delete(node, key)
							} else {
								node[key] = kept
							}
							strippedKeys = append(strippedKeys, path)
							stripped = true
							continue
						}
					}
					delete(node, key)
					strippedKeys = append(strippedKeys, path)
					stripped = true
					continue
				}
			}
			if child, ok := value.(map[string]any); ok {
				walk(path, child)
				if len(child) == 0 {
					delete(node, key)
					stripped = true
				}
			}
		}
	}
	walk("", remaining)
	return strippedKeys, stripped
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
func (s *Service) mapImportConfigDocument(profile Profile, doc map[string]any, rawText string, declarations []managedKeyDeclaration, baseline map[string]any, generatedPaths []string) (Fields, string, []string) {
	fields := profile.Fields
	var mapped []string

	remaining := make(map[string]any, len(doc))
	for key, value := range doc {
		remaining[key] = value
	}

	if existingEnvMappable(profile.Provider) {
		if envNode, ok := remaining["env"].(map[string]any); ok {
			// The edited env map replaces the structured env wholesale — Story 6:
			// keys removed in the editor are removed from the structured field,
			// so raw edit → import round-trips instead of patching. Generated
			// redaction placeholders are daemon provenance, never operator
			// input: they drop out instead of persisting into Fields.Env.
			fields.Env = make(map[string]string, len(envNode))
			for envKey, envValue := range envNode {
				text, ok := envValue.(string)
				if !ok {
					continue
				}
				// Credential-generated paths are daemon output; they drop
				// out of the structured env regardless of their value.
				if slices.Contains(generatedPaths, "env."+envKey) {
					continue
				}
				fields.Env[envKey] = text
			}
			mapped = append(mapped, "env")
			delete(remaining, "env")
		} else if _, present := remaining["env"]; !present {
			fields.Env = nil
			mapped = append(mapped, "env")
		}
		// Without a Model Provider, ANTHROPIC_MODEL / ANTHROPIC_BASE_URL are
		// projections of the structured Model / Endpoint fields. Consume them
		// back so one open→import cycle does not freeze derived values into
		// Fields.Env and dethrone the structured form (Story 6). With a Model
		// Provider they stay Managed Keys and refuse above.
		if profile.Provider == ProviderClaudeCode && strings.TrimSpace(profile.Fields.ModelProviderID) == "" {
			// Absence of the key clears the structured field so deletion
			// round-trips, mirroring the Codex model mapping.
			if value, present := fields.Env["ANTHROPIC_MODEL"]; present {
				fields.Model = value
				mapped = append(mapped, "env.ANTHROPIC_MODEL")
			} else if strings.TrimSpace(fields.Model) != "" {
				fields.Model = ""
				mapped = append(mapped, "env.ANTHROPIC_MODEL")
			}
			delete(fields.Env, "ANTHROPIC_MODEL")
			if value, present := fields.Env["ANTHROPIC_BASE_URL"]; present {
				fields.Endpoint = value
				mapped = append(mapped, "env.ANTHROPIC_BASE_URL")
			} else if strings.TrimSpace(fields.Endpoint) != "" {
				fields.Endpoint = ""
				mapped = append(mapped, "env.ANTHROPIC_BASE_URL")
			}
			delete(fields.Env, "ANTHROPIC_BASE_URL")
		}
	}

	// A top-level Codex `model` maps into the structured model
	// field whenever it is not a Managed Config Key (model provider resolved)
	// — the structured form stays the single source of truth. Absence of
	// the key in the edited document clears the structured field so
	// deletion round-trips.
	if profile.Provider == ProviderCodex && strings.TrimSpace(profile.Fields.ModelProviderID) == "" {
		if model, ok := remaining["model"].(string); ok {
			fields.Model = strings.TrimSpace(model)
			mapped = append(mapped, "model")
			delete(remaining, "model")
		} else if _, present := remaining["model"]; !present && strings.TrimSpace(fields.Model) != "" {
			fields.Model = ""
			mapped = append(mapped, "model")
		}
	}

	// Catalog-sourced plugins the editor round-trips through enabledPlugins
	// map back into the structured Runtime Extensions field; unknown refs
	// stay in the remainder for the provider to resolve. The editor map is
	// the full set of catalog plugins: false and absence both drop the
	// structured entry, and known refs never linger in the remainder.
	if profile.Provider == ProviderClaudeCode {
		kept := make([]RuntimeExtensionRef, 0, len(profile.Fields.RuntimeExtensions))
		for _, existing := range profile.Fields.RuntimeExtensions {
			if s.isKnownInstallRef(existing.Config["install_ref"]) {
				continue
			}
			kept = append(kept, existing)
		}
		if plugins, ok := remaining["enabledPlugins"].(map[string]any); ok {
			for ref, value := range plugins {
				if !s.isKnownInstallRef(ref) {
					continue
				}
				delete(plugins, ref)
				mapped = append(mapped, "enabledPlugins."+ref)
				enabled, isBool := value.(bool)
				if !isBool || !enabled {
					continue
				}
				on := true
				kept = append(kept, RuntimeExtensionRef{
					ID:      pluginIDFromInstallRef(ref),
					Enabled: &on,
					Config:  map[string]string{"install_ref": ref},
				})
			}
			if len(plugins) == 0 {
				delete(remaining, "enabledPlugins")
			}
		} else {
			mapped = append(mapped, "enabledPlugins")
		}
		fields.RuntimeExtensions = kept
	}

	resolved := strings.TrimSpace(profile.Fields.ModelProviderID) != ""
	// MCP config sections are harness-generated at projection (trusted
	// server injection) and out of scope for the Custom Config File. They
	// never persist into the remainder; the structured MCPServers field is
	// their only source of truth.
	if _, present := remaining["mcp_servers"]; present {
		delete(remaining, "mcp_servers")
		mapped = append(mapped, "mcp_servers")
	}
	strippedKeys, stripped := stripUnchangedManagedKeys(remaining, declarations, baseline, resolved)

	// The operator's raw text is preserved verbatim — comments and
	// formatting included — whenever nothing mapped or stripped away.
	if len(mapped) == 0 && !stripped {
		return fields, rawText, mapped
	}
	// Line surgery drops the lines of mapped and stripped keys; comments
	// and formatting on the surviving lines stay byte-for-byte.
	dropped := append([]string{}, mapped...)
	dropped = append(dropped, strippedKeys...)
	remainder := s.renderRemainderVerbatim(profile.Provider, remaining, dropped, rawText, baseline)
	return fields, remainder, mapped
}

// renderRemainderVerbatim keeps the operator's raw text whenever a
// line-based edit can express the mapping: lines carrying mapped or
// stripped top-level keys drop out, everything else (comments, blank
// lines, formatting) survives untouched. When the shape defeats line
// surgery, fall back to re-encoding the parsed remainder.
func (s *Service) renderRemainderVerbatim(provider Provider, remaining map[string]any, dropped []string, rawText string, baseline map[string]any) string {
	if len(remaining) == 0 {
		return ""
	}
	if len(dropped) == 0 {
		return rawText
	}
	if edited, ok := surgicalRemainder(provider, dropped, rawText, baseline); ok {
		return edited
	}
	return renderRemainder(provider, remaining)
}

// surgicalRemainder removes the lines of top-level keys that moved into
// structured fields, and the lines of harness-derived array entries under a
// managed list. It reports false when the text's shape defeats line-based
// edits (a mapped key whose span cannot be located).
func surgicalRemainder(provider Provider, dropped []string, rawText string, baseline map[string]any) (string, bool) {
	if importConfigFormat(provider) == "json" {
		return surgicalJSONRemainder(dropped, rawText)
	}
	if importConfigFormat(provider) != "toml" && importConfigFormat(provider) != "yaml" {
		return "", false
	}
	lines := strings.Split(rawText, "\n")
	keep := make([]bool, len(lines))
	for i := range keep {
		keep[i] = true
	}
	// Harness-derived array entries under a managed list path: their lines
	// drop out while operator entries survive.
	managedListEntries := map[string][]any{}
	for _, key := range dropped {
		if value, ok := lookupPath(baseline, key); ok {
			if list, ok := value.([]any); ok {
				managedListEntries[key] = list
			}
		}
	}
	// Track whether scanned lines sit at the document root or under a
	// section header: mapped keys ("model", "env") are root-level only, so
	// same-named keys inside sections must survive. currentList names a
	// managed list whose "- entry" lines may need dropping; sectionPath is
	// the dotted YAML section prefix ("plugins" → "plugins.enabled").
	inSection := false
	currentList := ""
	sectionPath := ""
	currentTable := ""
	managedLists := map[string]bool{}
	for key := range managedListEntries {
		managedLists[key] = true
	}
	removed := 0
	// Whole-subtree drops: managed keys whose baseline value is a map (or a
	// dotted declaration like model_providers.*) must remove their complete
	// syntactic span — the TOML table through the next table, the YAML
	// subtree by indentation. Single-line removal would leave the harness
	// content in the overlay where deep merge can resurrect it.
	subtreeDrops := map[string]bool{}
	for _, key := range dropped {
		if managedListEntries[key] != nil {
			continue
		}
		if base, ok := lookupPath(baseline, key); ok {
			if _, isMap := base.(map[string]any); isMap || strings.Contains(key, "*") || strings.HasSuffix(key, ".*") {
				subtreeDrops[key] = true
			}
		} else if strings.Contains(key, "*") {
			subtreeDrops[key] = true
		}
	}
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		if importConfigFormat(provider) == "toml" && strings.HasPrefix(trimmed, "[") {
			inSection = true
			currentList = ""
			// A TOML table header opens a whole-table span: when the table
			// belongs to a managed subtree, drop it through the next header.
			tableName := strings.TrimSuffix(strings.TrimPrefix(trimmed, "["), "]")
			currentTable = tableName
			for drop := range subtreeDrops {
				prefix := strings.TrimSuffix(drop, ".*")
				if tableName == prefix || strings.HasPrefix(tableName, prefix+".") {
					keep[i] = false
					removed++
					break
				}
			}
			continue
		}
		if importConfigFormat(provider) == "yaml" && !strings.HasPrefix(line, " ") && strings.HasSuffix(trimmed, ":") {
			inSection = true
			currentList = ""
			sectionPath = strings.TrimSuffix(trimmed, ":")
			// A top-level YAML key with a map baseline drops its entire
			// subtree (indentation scope), not just the header line.
			if subtreeDrops[sectionPath] {
				keep[i] = false
				removed++
				continue
			}
			continue
		}
		// YAML: indented lines under a dropped managed subtree vanish with it.
		if importConfigFormat(provider) == "yaml" && strings.HasPrefix(line, " ") && sectionPath != "" && subtreeDrops[sectionPath] {
			if trimmed != "" {
				keep[i] = false
				removed++
			}
			continue
		}
		if importConfigFormat(provider) == "yaml" && strings.HasPrefix(trimmed, "- ") {
			if currentList != "" {
				entry := strings.TrimSpace(strings.TrimPrefix(trimmed, "- "))
				for _, base := range managedListEntries[currentList] {
					if baseText, ok := base.(string); ok && baseText == entry {
						keep[i] = false
						removed++
						break
					}
				}
			}
			continue
		}
		// A "key:" line inside a section can open a managed list context
		// when the section path plus key matches a managed list path.
		// Nested managed keys (approvals.mode, agent.*, model.*) drop too.
		if importConfigFormat(provider) == "yaml" && strings.Contains(trimmed, ":") && !strings.HasPrefix(trimmed, "- ") {
			candidate, rest, _ := strings.Cut(trimmed, ":")
			candidate = strings.TrimSpace(candidate)
			rest = strings.TrimSpace(rest)
			path := candidate
			if strings.HasPrefix(line, " ") && sectionPath != "" {
				path = sectionPath + "." + candidate
			}
			droppedHere := false
			for _, mappedKey := range dropped {
				if mappedKey == path || mappedKey == candidate {
					// A managed list container ("enabled:") must survive:
					// only its harness-derived entries drop, so the
					// remainder stays valid YAML.
					if managedLists[path] || managedLists[candidate] {
						break
					}
					keep[i] = false
					removed++
					droppedHere = true
					break
				}
			}
			if rest == "" {
				if managedLists[path] {
					currentList = path
				} else if !droppedHere {
					currentList = ""
				}
			}
			if droppedHere {
				continue
			}
			if rest == "" {
				continue
			}
		}
		// TOML: lines inside a dropped managed table vanish with it.
		if importConfigFormat(provider) == "toml" && inSection {
			for drop := range subtreeDrops {
				prefix := strings.TrimSuffix(drop, ".*")
				if currentTable == prefix || strings.HasPrefix(currentTable, prefix+".") {
					if trimmed != "" {
						keep[i] = false
						removed++
					}
					break
				}
			}
			continue
		}
		key, _, found := strings.Cut(trimmed, "=")
		if found && !inSection {
			candidate := strings.TrimSpace(key)
			for _, mappedKey := range dropped {
				if mappedKey == candidate {
					keep[i] = false
					removed++
					continue
				}
			}
		}
	}
	if removed == 0 {
		// Nothing dropped: either nothing mapped at root level or the
		// mapped keys all lived under sections; the raw text stands.
		return rawText, true
	}
	var b strings.Builder
	for i, line := range lines {
		if keep[i] {
			b.WriteString(line)
			if i < len(lines)-1 {
				b.WriteString("\n")
			}
		}
	}
	return b.String(), true
}

// pluginIDFromInstallRef derives a stable Runtime Extension ID from a
// catalog install ref ("plugin@marketplace" → "plugin").
func pluginIDFromInstallRef(ref string) string {
	if at := strings.Index(ref, "@"); at > 0 {
		return ref[:at]
	}
	return ref
}

// existingEnvMappable reports whether the provider's main config carries an
// env map that maps onto the structured env field.
func existingEnvMappable(provider Provider) bool {
	return provider == ProviderClaudeCode
}

// surgicalJSONRemainder drops the mapped top-level keys from the operator's
// raw JSON while keeping every surviving byte exactly as written (spacing,
// key order, layout). It locates each key's line span through a re-parse of
// the raw text, matching the value end by line scanning.
func surgicalJSONRemainder(dropped []string, rawText string) (string, bool) {
	lines := strings.Split(rawText, "\n")
	if len(lines) == 0 {
		return "", false
	}
	// Find each dropped key's first line: a line whose trimmed form starts
	// with the JSON-quoted key followed by a colon.
	startOf := map[string]int{}
	for _, key := range dropped {
		needle := `"` + key + `"`
		for i, line := range lines {
			trimmed := strings.TrimSpace(line)
			if !strings.HasPrefix(trimmed, needle) {
				continue
			}
			rest := strings.TrimPrefix(trimmed, needle)
			if strings.HasPrefix(rest, ":") || strings.HasPrefix(rest, " :") {
				startOf[key] = i
				break
			}
		}
	}
	if len(startOf) == 0 {
		return "", false
	}
	keep := make([]bool, len(lines))
	for i := range keep {
		keep[i] = true
	}
	// Track brace/bracket depth from the document start so each key's span
	// ends at its value's closing line (depth returns to the top level).
	depth := 0
	inString := false
	escaped := false
	keySpans := map[string][2]int{}
	activeKey := ""
	activeStart := -1
	for i, line := range lines {
		if activeKey == "" {
			if start, ok := startOfLabel(lines, i, startOf); ok {
				activeKey = start.key
				activeStart = start.line
				depth = 0
				inString = false
				escaped = false
			}
		}
		if activeKey != "" {
			for _, ch := range line {
				if escaped {
					escaped = false
					continue
				}
				switch {
				case ch == '\\' && inString:
					escaped = true
				case ch == '"' && !escaped:
					inString = !inString
				case !inString && (ch == '{' || ch == '['):
					depth++
				case !inString && (ch == '}' || ch == ']'):
					depth--
				}
			}
			// The span closes on the line where the value's depth returns
			// to zero after having opened (or immediately for scalars).
			if i == activeStart {
				// Value opening counted above; scalars close on same line.
			}
			if depth <= 0 {
				keySpans[activeKey] = [2]int{activeStart, i}
				activeKey = ""
				activeStart = -1
			}
		}
	}
	if len(keySpans) == 0 {
		return "", false
	}
	for _, span := range keySpans {
		for i := span[0]; i <= span[1] && i < len(keep); i++ {
			keep[i] = false
		}
	}
	var b strings.Builder
	for i, line := range lines {
		if keep[i] {
			b.WriteString(line)
			b.WriteString("\n")
		}
	}
	return strings.TrimRight(b.String(), "\n") + "\n", true
}

type jsonKeyStart struct {
	key  string
	line int
}

// startOfLabel reports the dropped key whose header line is the given line.
func startOfLabel(lines []string, i int, startOf map[string]int) (jsonKeyStart, bool) {
	for key, start := range startOf {
		if start == i {
			return jsonKeyStart{key: key, line: start}, true
		}
	}
	return jsonKeyStart{}, false
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
