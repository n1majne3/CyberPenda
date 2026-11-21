package blackboardv2

import (
	"encoding/hex"
	"net/url"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"
)

func validateEvidencePatch(patch EvidencePatch, path string) error {
	if patch.Summary != nil {
		if err := validateSemanticText(*patch.Summary, path+".summary"); err != nil {
			return err
		}
	}
	if patch.MediaType != nil {
		if err := validateConciseText(*patch.MediaType, path+".media_type"); err != nil {
			return err
		}
	}
	if patch.CapturedAt != nil {
		if *patch.CapturedAt == "" {
			return semanticError("semantic_validation", "captured_at must be cleared explicitly", path+".captured_at", nil)
		}
		if _, err := time.Parse(time.RFC3339, *patch.CapturedAt); err != nil {
			return semanticError("semantic_validation", "captured_at must be an RFC3339 timestamp", path+".captured_at", nil)
		}
	}
	return nil
}

func validateEntityRecord(record EntityRecord, path string) error {
	if record.Status != "active" {
		return semanticError("semantic_validation", "Entity status must be active", path+".status", nil)
	}
	if !isAcceptedEntityKind(record.Kind) {
		return semanticError("semantic_validation", "Entity kind is not accepted", path+".kind", nil)
	}
	if err := validateConciseText(record.Name, path+".name"); err != nil {
		return err
	}
	if containsSecretMarker(record.Name) {
		return semanticError("semantic_validation", "Entity name must not contain secrets", path+".name", nil)
	}
	if record.Locator != "" {
		if err := validateLocator(record.Locator, path+".locator"); err != nil {
			return err
		}
	}
	if record.Description != "" {
		if err := validateConciseText(record.Description, path+".description"); err != nil {
			return err
		}
		if containsSecretMarker(record.Description) {
			return semanticError("semantic_validation", "Entity description must not contain secrets", path+".description", nil)
		}
	}
	switch record.ScopeStatus {
	case "in_scope", "unknown", "out_of_scope":
	default:
		return semanticError("semantic_validation", "Entity scope_status must be in_scope, unknown, or out_of_scope", path+".scope_status", nil)
	}
	if record.CredentialRef != "" {
		if err := validateCredentialRef(record.CredentialRef, path+".credential_ref"); err != nil {
			return err
		}
	}
	return nil
}

func isAcceptedEntityKind(kind string) bool {
	switch kind {
	case "host", "service", "endpoint", "identity", "file", "function":
		return true
	default:
		return false
	}
}

func validateLocator(locator, path string) error {
	if err := validateConciseText(locator, path); err != nil {
		return err
	}
	if strings.ContainsAny(locator, " \t\r\n") {
		return semanticError("semantic_validation", "Entity locator must not contain whitespace", path, nil)
	}
	if containsSecretMarker(locator) {
		return semanticError("semantic_validation", "Entity locator must not contain secrets", path, nil)
	}
	parsed, err := url.Parse(locator)
	if err != nil {
		return semanticError("semantic_validation", "Entity locator must be a valid locator", path, nil)
	}
	if parsed.User != nil {
		return semanticError("semantic_validation", "Entity locator must not contain credentials", path, nil)
	}
	if strings.Contains(locator, "://") && (parsed.Scheme == "" || parsed.Host == "") {
		return semanticError("semantic_validation", "Entity locator URL must include scheme and host", path, nil)
	}
	if (parsed.Scheme == "http" || parsed.Scheme == "https") && parsed.Host == "" {
		return semanticError("semantic_validation", "Entity HTTP locator must include a host", path, nil)
	}
	return nil
}

func validateCredentialRef(value, path string) error {
	if err := validateConciseText(value, path); err != nil {
		return err
	}
	if containsSecretMarker(value) {
		return semanticError("semantic_validation", "Entity credential_ref must be a non-secret reference", path, nil)
	}
	return nil
}

var (
	secretAssignmentPattern = regexp.MustCompile(`(?i)(?:password|passwd|token|secret|api[_-]?key)\s*[:=]\s*\S+`)
	secretKeyPattern        = regexp.MustCompile(`(?i)(?:^|[^a-z0-9])sk-`)
)

func containsSecretMarker(value string) bool {
	return secretAssignmentPattern.MatchString(value) || secretKeyPattern.MatchString(value)
}

func validateObjectiveRecord(record ObjectiveRecord, path string) error {
	if record.Status != "open" {
		return semanticError("semantic_validation", "Objective status must be open", path+".status", nil)
	}
	if record.ResolutionSummary != "" {
		return semanticError("semantic_validation", "open Objective must not include resolution_summary", path+".resolution_summary", nil)
	}
	return validateSemanticText(record.Objective, path+".objective")
}

func validateAttemptRecord(record AttemptRecord, path string) error {
	if record.Status != "open" {
		return semanticError("semantic_validation", "Attempt status must be open", path+".status", nil)
	}
	return validateSemanticText(record.Summary, path+".summary")
}

func validateFactRecord(record FactRecord, path string) error {
	if err := validateConciseText(record.Category, path+".category"); err != nil {
		return err
	}
	if err := validateSemanticText(record.Summary, path+".summary"); err != nil {
		return err
	}
	if record.Body != "" && !utf8.ValidString(record.Body) {
		return semanticError("semantic_validation", "Fact body must be valid UTF-8", path+".body", nil)
	}
	if record.Confidence != "tentative" && record.Confidence != "confirmed" {
		return semanticError("semantic_validation", "Fact confidence must be tentative or confirmed", path+".confidence", nil)
	}
	switch record.ScopeStatus {
	case "in_scope", "unknown", "out_of_scope":
		return nil
	default:
		return semanticError("semantic_validation", "Fact scope_status must be in_scope, unknown, or out_of_scope", path+".scope_status", nil)
	}
}

func validateEvidenceRecord(record EvidenceRecord, path string) error {
	if record.Status != "available" && record.Status != "missing" {
		return semanticError("semantic_validation", "Evidence status must be available or missing", path+".status", nil)
	}
	if err := validateConciseText(record.ArtifactType, path+".artifact_type"); err != nil {
		return err
	}
	if err := validateSemanticText(record.Summary, path+".summary"); err != nil {
		return err
	}
	if record.MediaType != "" {
		if err := validateConciseText(record.MediaType, path+".media_type"); err != nil {
			return err
		}
	}
	if record.ManagedPath == "" || len(record.SHA256) != 64 || record.Size < 0 {
		return semanticError("semantic_validation", "Evidence integrity details are invalid", path, nil)
	}
	if _, err := hex.DecodeString(record.SHA256); err != nil {
		return semanticError("semantic_validation", "Evidence sha256 must be lowercase hexadecimal", path+".sha256", nil)
	}
	if record.SHA256 != strings.ToLower(record.SHA256) {
		return semanticError("semantic_validation", "Evidence sha256 must be lowercase hexadecimal", path+".sha256", nil)
	}
	if record.CapturedAt != "" {
		if _, err := time.Parse(time.RFC3339, record.CapturedAt); err != nil {
			return semanticError("semantic_validation", "Evidence captured_at must be an RFC3339 timestamp", path+".captured_at", nil)
		}
	}
	return nil
}

func validateConciseText(value, path string) error {
	if strings.TrimSpace(value) == "" || !utf8.ValidString(value) || len([]byte(value)) > 512 {
		return semanticError("semantic_validation", "concise semantic text is required and must fit the v2 limit", path, nil)
	}
	return nil
}

func validateSemanticText(value, path string) error {
	if strings.TrimSpace(value) == "" || !utf8.ValidString(value) || len([]byte(value)) > 1024 {
		return semanticError("semantic_validation", "primary semantic text is required and must fit the v2 limit", path, nil)
	}
	return nil
}

func validateKey(key, path string) error {
	if key == "" || len(key) > 96 {
		return semanticError("semantic_validation", "Blackboard Key must be non-empty and at most 96 ASCII characters", path, nil)
	}
	for _, r := range key {
		if r < 0x20 || r > 0x7e {
			return semanticError("semantic_validation", "Blackboard Key must be readable ASCII", path, nil)
		}
	}
	return nil
}
