package blackboardconclusion

import (
	"encoding/json"
	"fmt"
	"strings"
	"unicode/utf8"
)

func validateSuppliedVersions(fields map[string]json.RawMessage, result RuntimeAttemptResult) error {
	if rawAttempt, present := caseFoldedJSONField(fields, "attempt"); present {
		if err := validateSuppliedVersion(rawAttempt, result.Attempt.ExpectedVersion, "attempt"); err != nil {
			return err
		}
	}
	if rawTargets, present := caseFoldedJSONField(fields, "tested_targets"); present {
		var targets []json.RawMessage
		if err := json.Unmarshal(rawTargets, &targets); err != nil {
			return err
		}
		for index, target := range targets {
			if err := validateSuppliedVersion(target, result.TestedTargets[index].ExpectedVersion, fmt.Sprintf("tested_targets[%d]", index)); err != nil {
				return err
			}
		}
	}
	return nil
}

func validateSuppliedVersion(raw json.RawMessage, version int, path string) error {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return err
	}
	if _, present := caseFoldedJSONField(fields, "expected_version"); present && version < 1 {
		return validationDetailError(fmt.Sprintf("%s.expected_version must be positive", path),
			ValidationDetail{Reason: ValidationReasonRuleViolation, FieldPath: path + ".expected_version", Expected: "a positive integer"})
	}
	return nil
}

func caseFoldedJSONField(fields map[string]json.RawMessage, name string) (json.RawMessage, bool) {
	for field, value := range fields {
		if strings.EqualFold(field, name) {
			return value, true
		}
	}
	return nil, false
}

func validateResult(result RuntimeAttemptResult) error {
	if result.Schema != runtimeAttemptResultSchema {
		return validationDetailError(fmt.Sprintf("schema must be %q", runtimeAttemptResultSchema),
			ValidationDetail{Reason: ValidationReasonRuleViolation, FieldPath: "schema", Expected: fmt.Sprintf("schema must be %q", runtimeAttemptResultSchema)})
	}
	if result.BaseRevision < 0 {
		return validationDetailError("base_revision must not be negative",
			ValidationDetail{Reason: ValidationReasonRuleViolation, FieldPath: "base_revision", Expected: "base_revision must be a non-negative integer"})
	}
	if err := validateKey(result.Attempt.Key, "attempt.key"); err != nil {
		return err
	}
	if result.Attempt.Create == (result.Attempt.ExpectedVersion > 0) {
		return validationDetailError("attempt requires exactly one of create=true or expected_version",
			ValidationDetail{Reason: ValidationReasonRuleViolation, FieldPath: "attempt", Expected: "exactly one of create=true or expected_version"})
	}
	if result.Attempt.ExpectedVersion < 0 {
		return validationDetailError("attempt.expected_version must be positive",
			ValidationDetail{Reason: ValidationReasonRuleViolation, FieldPath: "attempt.expected_version", Expected: "a positive integer"})
	}
	if err := validateText(result.Attempt.Summary, 1024, "attempt.summary"); err != nil {
		return err
	}
	switch result.Attempt.Outcome {
	case RuntimeAttemptSucceeded, RuntimeAttemptFailed, RuntimeAttemptBlocked, RuntimeAttemptInconclusive:
	default:
		return validationDetailError("attempt.outcome must be succeeded, failed, blocked, or inconclusive",
			ValidationDetail{Reason: ValidationReasonInvalidEnumValue, FieldPath: "attempt.outcome", Expected: "outcome must be one of succeeded, failed, blocked, or inconclusive"})
	}
	if len(result.TestedTargets) == 0 {
		return validationDetailError("tested_targets must be a non-empty array",
			ValidationDetail{Reason: ValidationReasonRuleViolation, FieldPath: "tested_targets", Expected: "a non-empty array"})
	}
	if len(result.TestedTargets) > 32 {
		return validationDetailError("runtime Attempt result target count exceeds 32",
			ValidationDetail{Reason: ValidationReasonOversizedValue, FieldPath: "tested_targets", Expected: "at most 32 target entries"})
	}
	if len(result.ProducedTargets) > 32 {
		return validationDetailError("runtime Attempt result target count exceeds 32",
			ValidationDetail{Reason: ValidationReasonOversizedValue, FieldPath: "produced_targets", Expected: "at most 32 target entries"})
	}
	seen := map[string]bool{result.Attempt.Key: true}
	for index, target := range result.TestedTargets {
		path := fmt.Sprintf("tested_targets[%d]", index)
		if err := validateKey(target.Key, path+".key"); err != nil {
			return err
		}
		if seen[target.Key] {
			return validationDetailError(fmt.Sprintf("%s.key is duplicated", path),
				ValidationDetail{Reason: ValidationReasonRuleViolation, FieldPath: path + ".key", Expected: "the key must be unique across the result"})
		}
		seen[target.Key] = true
		created := target.CreateObjective != nil
		if created == (target.ExpectedVersion > 0) || target.ExpectedVersion < 0 {
			return validationDetailError(fmt.Sprintf("%s requires exactly one of create_objective or expected_version", path),
				ValidationDetail{Reason: ValidationReasonRuleViolation, FieldPath: path, Expected: "exactly one of create_objective or expected_version"})
		}
		if created {
			if err := validateText(target.CreateObjective.Objective, 1024, path+".create_objective.objective"); err != nil {
				return err
			}
		}
	}
	if result.Attempt.Outcome == RuntimeAttemptSucceeded && len(result.ProducedTargets) == 0 {
		return validationDetailError("succeeded Attempt requires at least one produced target",
			ValidationDetail{Reason: ValidationReasonRuleViolation, FieldPath: "produced_targets", Expected: "a succeeded Attempt requires at least one produced target"})
	}
	if result.Attempt.Outcome != RuntimeAttemptSucceeded && len(result.ProducedTargets) != 0 {
		return validationDetailError("produced_targets are accepted only for a succeeded Attempt",
			ValidationDetail{Reason: ValidationReasonRuleViolation, FieldPath: "produced_targets", Expected: "accepted only for a succeeded Attempt"})
	}
	for index, target := range result.ProducedTargets {
		path := fmt.Sprintf("produced_targets[%d]", index)
		if err := validateKey(target.Key, path+".key"); err != nil {
			return err
		}
		if target.ExpectedVersion < 1 {
			return validationDetailError(fmt.Sprintf("%s.expected_version must be positive", path),
				ValidationDetail{Reason: ValidationReasonRuleViolation, FieldPath: path + ".expected_version", Expected: "a positive integer"})
		}
		if seen[target.Key] {
			return validationDetailError(fmt.Sprintf("%s.key is duplicated", path),
				ValidationDetail{Reason: ValidationReasonRuleViolation, FieldPath: path + ".key", Expected: "the key must be unique across the result"})
		}
		seen[target.Key] = true
	}
	return nil
}

func validateKey(key, path string) error {
	if key == "" {
		return validationDetailError(fmt.Sprintf("%s must be non-empty and at most 96 ASCII characters", path),
			ValidationDetail{Reason: ValidationReasonRuleViolation, FieldPath: path, Expected: "the key must be non-empty"})
	}
	if len(key) > 96 {
		return validationDetailError(fmt.Sprintf("%s must be non-empty and at most 96 ASCII characters", path),
			ValidationDetail{Reason: ValidationReasonOversizedValue, FieldPath: path, Expected: "the key must be at most 96 ASCII characters"})
	}
	for _, value := range key {
		if value < 0x20 || value > 0x7e {
			return validationDetailError(fmt.Sprintf("%s must be readable ASCII", path),
				ValidationDetail{Reason: ValidationReasonRuleViolation, FieldPath: path, Expected: "the key must be readable ASCII"})
		}
	}
	return nil
}

func validateText(value string, limit int, path string) error {
	if strings.TrimSpace(value) == "" {
		return validationDetailError(fmt.Sprintf("%s must be non-empty valid UTF-8 and at most %d bytes", path, limit),
			ValidationDetail{Reason: ValidationReasonRuleViolation, FieldPath: path, Expected: "the text must be non-empty"})
	}
	if !utf8.ValidString(value) {
		return validationDetailError(fmt.Sprintf("%s must be non-empty valid UTF-8 and at most %d bytes", path, limit),
			ValidationDetail{Reason: ValidationReasonRuleViolation, FieldPath: path, Expected: "the text must be valid UTF-8"})
	}
	if len([]byte(value)) > limit {
		return validationDetailError(fmt.Sprintf("%s must be non-empty valid UTF-8 and at most %d bytes", path, limit),
			ValidationDetail{Reason: ValidationReasonOversizedValue, FieldPath: path, Expected: fmt.Sprintf("the text must be at most %d bytes", limit)})
	}
	return nil
}
