package runtimeprofile

import (
	"fmt"
	"strings"
	"unicode"
)

// Structured field identifiers used when Custom Args redefine CyberPenda controls.
const (
	StructuredFieldModel           = "model"
	StructuredFieldModelProvider   = "model_provider"
	StructuredFieldReasoningEffort = "reasoning_effort"
)

// CustomArgConflictError reports a Runtime Custom Argument that redefines a
// structured Model Provider, model, or Reasoning Effort field. CyberPenda does
// not migrate, strip, reorder, or fall back around the conflict.
//
// Argument is the complete offending Custom Arg form (flag/key plus value when
// present). Values that look secret are redacted so Error() and diagnostics are
// safe to log.
type CustomArgConflictError struct {
	Provider   Provider
	Argument   string // complete offending form with secrets redacted
	Flag       string // flag or config key only (e.g. "--model", "model_reasoning_effort")
	Field      string
	CustomArgs []string // original args, never mutated; may contain secrets — do not log raw
}

func (e *CustomArgConflictError) Error() string {
	if e == nil {
		return "custom argument conflict"
	}
	return fmt.Sprintf(
		"custom argument %q redefines structured field %s (%s); use the Runtime Profile %s instead",
		e.Argument,
		e.Field,
		structuredFieldLabel(e.Field),
		structuredFieldLabel(e.Field),
	)
}

// ErrCustomArgConflict is a sentinel for errors.Is matching.
var ErrCustomArgConflict = fmt.Errorf("custom argument redefines a structured field")

func (e *CustomArgConflictError) Is(target error) bool {
	return target == ErrCustomArgConflict
}

// ValidateCustomArgs rejects provider-native aliases that redefine structured
// Model Provider, model, or Reasoning Effort controls. Only aliases that the
// corresponding runtime CLI/argv surface actually uses are rejected. The input
// slice is never modified, stripped, or reordered.
func ValidateCustomArgs(provider Provider, args []string) error {
	rules := customArgRulesFor(provider)
	if len(rules.flags) == 0 && len(rules.configKeys) == 0 {
		return nil
	}

	for i := 0; i < len(args); i++ {
		token := args[i]
		if token == "" {
			continue
		}
		// Each stored Custom Arg is one argv token. Operators sometimes paste
		// "--flag value" on a single line; inspect the first field so the form
		// fails closed without rewriting the stored token.
		head := firstField(token)

		// Codex -c / --config KEY[=VALUE] forms (two-token or combined).
		if rules.configFlag(head) {
			assignment := ""
			consumedNext := false
			if eq := strings.Index(head, "="); eq >= 0 {
				// --config=key=value or -c=key=value
				assignment = head[eq+1:]
			} else if fields := strings.Fields(token); len(fields) > 1 {
				// single token: "-c model_reasoning_effort=high"
				assignment = fields[1]
			} else if i+1 < len(args) {
				assignment = args[i+1]
				consumedNext = true
			}
			if key, value, ok := conflictingConfigAssignment(assignment, rules.configKeys); ok {
				if consumedNext {
					i++
				}
				return &CustomArgConflictError{
					Provider:   provider,
					Argument:   formatConfigOffendingArg(token, head, key, value),
					Flag:       key,
					Field:      rules.configKeys[key],
					CustomArgs: args,
				}
			}
			if consumedNext {
				// Non-conflicting config assignment still consumed the next token.
				i++
			}
			continue
		}

		flag, inlineValue := splitFlag(head)
		if field, ok := rules.flags[flag]; ok {
			value := inlineValue
			if value == "" {
				if fields := strings.Fields(token); len(fields) > 1 {
					value = strings.Join(fields[1:], " ")
				} else if i+1 < len(args) && !strings.HasPrefix(strings.TrimSpace(args[i+1]), "-") {
					value = args[i+1]
				}
			}
			return &CustomArgConflictError{
				Provider:   provider,
				Argument:   formatFlagOffendingArg(flag, value, inlineValue != "" || strings.Contains(head, "=")),
				Flag:       flag,
				Field:      field,
				CustomArgs: args,
			}
		}
	}
	return nil
}

func firstField(token string) string {
	fields := strings.Fields(token)
	if len(fields) == 0 {
		return token
	}
	return fields[0]
}

type customArgRules struct {
	flags      map[string]string // flag -> structured field
	configKeys map[string]string // codex config key -> structured field
}

func (r customArgRules) configFlag(token string) bool {
	if len(r.configKeys) == 0 {
		return false
	}
	flag, _ := splitFlag(token)
	return flag == "-c" || flag == "--config"
}

// customArgRulesFor lists only runtime-native aliases that redefine structured
// Model Provider, model, or Reasoning Effort — matching builtin launch argv and
// documented CLI surfaces. Ordinary non-interactive / stream options are not
// rejected.
func customArgRulesFor(provider Provider) customArgRules {
	switch provider {
	case ProviderCodex:
		// Builtin launch injects --model; Codex CLI also documents -m and
		// -c/--config model_reasoning_effort / model / model_provider.
		return customArgRules{
			flags: map[string]string{
				"--model": StructuredFieldModel,
				"-m":      StructuredFieldModel,
			},
			configKeys: map[string]string{
				"model":                  StructuredFieldModel,
				"model_provider":         StructuredFieldModelProvider,
				"model_reasoning_effort": StructuredFieldReasoningEffort,
			},
		}
	case ProviderClaudeCode:
		// Builtin launch injects --model; Claude Code CLI documents --effort.
		return customArgRules{
			flags: map[string]string{
				"--model":  StructuredFieldModel,
				"--effort": StructuredFieldReasoningEffort,
			},
		}
	case ProviderPi:
		// Builtin launch injects --model and optional --provider via
		// pi_provider_args; Pi CLI documents --thinking for effort.
		return customArgRules{
			flags: map[string]string{
				"--provider": StructuredFieldModelProvider,
				"--model":    StructuredFieldModel,
				"--thinking": StructuredFieldReasoningEffort,
			},
		}
	default:
		return customArgRules{}
	}
}

func splitFlag(token string) (flag, value string) {
	if token == "" {
		return "", ""
	}
	if strings.HasPrefix(token, "-") {
		if eq := strings.Index(token, "="); eq >= 0 {
			return token[:eq], token[eq+1:]
		}
		return token, ""
	}
	return token, ""
}

func conflictingConfigAssignment(assignment string, keys map[string]string) (key, value string, ok bool) {
	assignment = strings.TrimSpace(assignment)
	if assignment == "" {
		return "", "", false
	}
	key = assignment
	value = ""
	if eq := strings.Index(assignment, "="); eq >= 0 {
		key = assignment[:eq]
		value = assignment[eq+1:]
	}
	key = strings.TrimSpace(key)
	key = strings.Trim(key, `"'`)
	value = strings.TrimSpace(value)
	value = strings.Trim(value, `"'`)
	if _, exists := keys[key]; !exists {
		return "", "", false
	}
	return key, value, true
}

// formatFlagOffendingArg builds the complete offending Custom Arg form.
// combined=true means the original used --flag=value style.
func formatFlagOffendingArg(flag, value string, combined bool) string {
	if strings.TrimSpace(value) == "" {
		return flag
	}
	safe := redactSensitiveValue(value)
	if combined {
		return flag + "=" + safe
	}
	return flag + " " + safe
}

// formatConfigOffendingArg preserves the -c / --config token shape used by the
// operator while naming the conflicting key (and redacted value).
func formatConfigOffendingArg(rawToken, head, key, value string) string {
	flag, _ := splitFlag(head)
	body := key
	if value != "" {
		body = key + "=" + redactSensitiveValue(value)
	}
	// Combined --config=key=value keeps a single-token form.
	if strings.Contains(head, "=") || (strings.Contains(rawToken, "=") && !strings.Contains(rawToken, " ")) {
		return flag + "=" + body
	}
	return flag + " " + body
}

// redactSensitiveValue replaces secret-shaped values so ConflictError and
// diagnostics never echo raw credentials. Ordinary model ids and effort levels
// pass through.
func redactSensitiveValue(value string) string {
	v := strings.TrimSpace(value)
	if v == "" {
		return v
	}
	// Env-style KEY=secret inside a single token.
	if eq := strings.Index(v, "="); eq > 0 {
		key := v[:eq]
		upper := strings.ToUpper(key)
		if strings.Contains(upper, "KEY") || strings.Contains(upper, "TOKEN") ||
			strings.Contains(upper, "SECRET") || strings.Contains(upper, "PASSWORD") {
			return key + "=[REDACTED]"
		}
	}
	lower := strings.ToLower(v)
	switch {
	case strings.HasPrefix(lower, "sk-"),
		strings.HasPrefix(lower, "key-"),
		strings.HasPrefix(lower, "ghp_"),
		strings.HasPrefix(lower, "glpat-"),
		strings.HasPrefix(lower, "xoxb-"),
		strings.HasPrefix(lower, "bearer "):
		return "[REDACTED]"
	}
	// Long opaque tokens that are not short model ids / effort enums.
	if len(v) >= 24 && mostlyOpaque(v) {
		return "[REDACTED]"
	}
	return v
}

func mostlyOpaque(v string) bool {
	opaque := 0
	for _, r := range v {
		if unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_' || r == '-' || r == '+' || r == '/' || r == '=' {
			opaque++
		} else {
			return false
		}
	}
	return opaque == len([]rune(v))
}

func structuredFieldLabel(field string) string {
	switch field {
	case StructuredFieldModel:
		return "model"
	case StructuredFieldModelProvider:
		return "Model Provider"
	case StructuredFieldReasoningEffort:
		return "Reasoning Effort"
	default:
		return field
	}
}
