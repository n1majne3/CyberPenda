// Package blackboardconclusion owns the closed model-produced semantic result
// used by assisted Harness control Turns.
package blackboardconclusion

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
	"unicode/utf8"

	"pentest/internal/blackboardv2"
)

const (
	runtimeAttemptResultSchema = "runtime-attempt-result/v1"
	maxResultBytes             = 64 << 10
	// maxValidationFieldPathBytes bounds a reported field path so raw provider
	// field names cannot inflate repair directives or durable state.
	maxValidationFieldPathBytes = 96
)

type RuntimeAttemptOutcome string

const (
	RuntimeAttemptSucceeded    RuntimeAttemptOutcome = "succeeded"
	RuntimeAttemptFailed       RuntimeAttemptOutcome = "failed"
	RuntimeAttemptBlocked      RuntimeAttemptOutcome = "blocked"
	RuntimeAttemptInconclusive RuntimeAttemptOutcome = "inconclusive"
)

// ValidationReason is the closed machine vocabulary for one rejected closed
// semantic result. Decoder text, provider bytes, and model reasoning never
// become reasons; only these bounded tokens enter repair directives and
// durable state.
type ValidationReason string

const (
	ValidationReasonInvalidResult        ValidationReason = "invalid_result"
	ValidationReasonEmptyResult          ValidationReason = "empty_result"
	ValidationReasonResultTooLarge       ValidationReason = "result_too_large"
	ValidationReasonInvalidUTF8          ValidationReason = "result_not_utf8"
	ValidationReasonInvalidJSON          ValidationReason = "result_not_json"
	ValidationReasonDuplicateField       ValidationReason = "duplicate_field"
	ValidationReasonMissingField         ValidationReason = "missing_field"
	ValidationReasonUnknownField         ValidationReason = "unknown_field"
	ValidationReasonInvalidKeyFormat     ValidationReason = "invalid_key_format"
	ValidationReasonInvalidEnumValue     ValidationReason = "invalid_enum_value"
	ValidationReasonOversizedValue       ValidationReason = "value_too_large"
	ValidationReasonRuleViolation        ValidationReason = "rule_violation"
	ValidationReasonBaseRevisionMismatch ValidationReason = "base_revision_mismatch"
)

// ValidationDetail is the bounded public rejection detail for one closed
// semantic result. The reason is a closed token, while the field path and
// expected form are static or bounded strings from this package; raw provider
// output and decoder text never appear.
type ValidationDetail struct {
	Reason    ValidationReason
	FieldPath string
	Expected  string
}

func (detail ValidationDetail) Error() string {
	message := string(detail.Reason)
	if detail.FieldPath != "" {
		message += " at " + detail.FieldPath
	}
	if detail.Expected != "" {
		message += "; expected " + detail.Expected
	}
	return message
}

// Valid reports whether the detail carries a bounded reason.
func (detail ValidationDetail) Valid() bool {
	return detail.Reason != ""
}

// DecodeDetailOf extracts the bounded rejection detail from a Decode error.
// Any error without structured detail maps to the generic invalid_result
// token so callers always receive a closed reason.
func DecodeDetailOf(err error) ValidationDetail {
	var detail ValidationDetail
	if err != nil && errors.As(err, &detail) {
		return detail
	}
	return ValidationDetail{Reason: ValidationReasonInvalidResult}
}

// RuntimeAttemptResult is the complete closed semantic result for one assisted
// conclusion. Trusted identity and idempotency deliberately do not appear.
type RuntimeAttemptResult struct {
	Schema          string                   `json:"schema"`
	BaseRevision    int                      `json:"base_revision"`
	Attempt         RuntimeAttemptDescriptor `json:"attempt"`
	TestedTargets   []RuntimeTestedTarget    `json:"tested_targets"`
	ProducedTargets []RuntimeVersionedTarget `json:"produced_targets"`
}

type RuntimeAttemptDescriptor struct {
	Key             string                `json:"key"`
	Create          bool                  `json:"create,omitempty"`
	ExpectedVersion int                   `json:"expected_version,omitempty"`
	Summary         string                `json:"summary"`
	Outcome         RuntimeAttemptOutcome `json:"outcome"`
}

type RuntimeTestedTarget struct {
	Key             string                  `json:"key"`
	ExpectedVersion int                     `json:"expected_version,omitempty"`
	CreateObjective *RuntimeObjectiveIntent `json:"create_objective,omitempty"`
}

type RuntimeObjectiveIntent struct {
	Objective string `json:"objective"`
}

type RuntimeVersionedTarget struct {
	Key             string `json:"key"`
	ExpectedVersion int    `json:"expected_version"`
}

type ValidatedResult struct {
	Result        RuntimeAttemptResult
	CanonicalJSON []byte
	SHA256        string
}

// Decode validates and canonicalizes one runtime-attempt-result/v1 document.
func Decode(raw []byte) (ValidatedResult, error) {
	if len(raw) == 0 {
		return ValidatedResult{}, validationDetailError("runtime Attempt result is empty",
			ValidationDetail{Reason: ValidationReasonEmptyResult, Expected: "the result must not be empty"})
	}
	if len(raw) > maxResultBytes {
		return ValidatedResult{}, validationDetailError("runtime Attempt result exceeds 64 KiB",
			ValidationDetail{Reason: ValidationReasonResultTooLarge, Expected: "the result must be at most 64 KiB"})
	}
	if !utf8.Valid(raw) {
		return ValidatedResult{}, validationDetailError("runtime Attempt result must be valid UTF-8",
			ValidationDetail{Reason: ValidationReasonInvalidUTF8, Expected: "the result must be valid UTF-8"})
	}
	if err := rejectDuplicateJSONFields(raw); err != nil {
		return ValidatedResult{}, err
	}
	var topLevel map[string]json.RawMessage
	if err := json.Unmarshal(raw, &topLevel); err != nil {
		return ValidatedResult{}, validationDetailError(fmt.Sprintf("decode runtime Attempt result: %v", err),
			ValidationDetail{Reason: ValidationReasonInvalidJSON, Expected: "the result must be exactly one JSON object"})
	}
	if _, ok := topLevel["base_revision"]; !ok {
		return ValidatedResult{}, validationDetailError("base_revision is required",
			ValidationDetail{Reason: ValidationReasonMissingField, FieldPath: "base_revision", Expected: "base_revision must be present"})
	}
	for _, field := range []string{"tested_targets", "produced_targets"} {
		if value, ok := topLevel[field]; ok && bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
			return ValidatedResult{}, validationDetailError(fmt.Sprintf("%s must be an array", field),
				ValidationDetail{Reason: ValidationReasonRuleViolation, FieldPath: field, Expected: "must be an array"})
		}
	}
	if rawAttempt, ok := topLevel["attempt"]; ok {
		var attemptFields map[string]json.RawMessage
		if err := json.Unmarshal(rawAttempt, &attemptFields); err == nil {
			if rawCreate, present := attemptFields["create"]; present {
				var create bool
				if err := json.Unmarshal(rawCreate, &create); err == nil && !create {
					return ValidatedResult{}, validationDetailError("attempt.create must be true when present",
						ValidationDetail{Reason: ValidationReasonRuleViolation, FieldPath: "attempt.create", Expected: "must be true when present"})
				}
			}
		}
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var result RuntimeAttemptResult
	if err := decoder.Decode(&result); err != nil {
		if fieldPath := unknownJSONFieldPath(err); fieldPath != "" {
			return ValidatedResult{}, validationDetailError(fmt.Sprintf("decode runtime Attempt result: %v", err),
				ValidationDetail{Reason: ValidationReasonUnknownField, FieldPath: fieldPath, Expected: "the result has no unknown fields"})
		}
		return ValidatedResult{}, validationDetailError(fmt.Sprintf("decode runtime Attempt result: %v", err),
			ValidationDetail{Reason: ValidationReasonInvalidJSON, Expected: "the result must be exactly one JSON object with the closed field types"})
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return ValidatedResult{}, validationDetailError("runtime Attempt result has trailing JSON",
			ValidationDetail{Reason: ValidationReasonInvalidJSON, Expected: "the result must be exactly one JSON object"})
	}
	if err := validateResult(result); err != nil {
		return ValidatedResult{}, err
	}
	if result.ProducedTargets == nil {
		result.ProducedTargets = []RuntimeVersionedTarget{}
	}
	canonical, err := json.Marshal(result)
	if err != nil {
		return ValidatedResult{}, fmt.Errorf("canonicalize runtime Attempt result: %w", err)
	}
	digest := sha256.Sum256(canonical)
	return ValidatedResult{Result: result, CanonicalJSON: canonical, SHA256: hex.EncodeToString(digest[:])}, nil
}

// validationDetailError attaches bounded rejection detail to a human-readable
// message. The detail remains extractable through errors.As while the message
// stays transient diagnostic text.
func validationDetailError(message string, detail ValidationDetail) error {
	return fmt.Errorf("%s: %w", message, detail)
}

// unknownJSONFieldPath extracts a bounded field name from the strict decoder's
// unknown-field error. The name is provider input, so it is truncated before
// it can enter a repair directive or durable state.
func unknownJSONFieldPath(err error) string {
	const marker = "unknown field "
	text := err.Error()
	index := strings.Index(text, marker)
	if index < 0 {
		return ""
	}
	name := strings.Trim(strings.TrimSpace(text[index+len(marker):]), `"`)
	if len(name) > maxValidationFieldPathBytes {
		name = name[:maxValidationFieldPathBytes]
		for len(name) > 0 && !utf8.ValidString(name) {
			name = name[:len(name)-1]
		}
	}
	return name
}

func rejectDuplicateJSONFields(raw []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	first, err := decoder.Token()
	if err != nil {
		return validationDetailError(fmt.Sprintf("decode runtime Attempt result: %v", err),
			ValidationDetail{Reason: ValidationReasonInvalidJSON, Expected: "the result must be exactly one JSON object"})
	}
	if err := walkJSONValue(decoder, first, ""); err != nil {
		return err
	}
	return nil
}

func walkJSONValue(decoder *json.Decoder, token json.Token, path string) error {
	if token == nil {
		return validationDetailError("runtime Attempt result does not accept null values",
			ValidationDetail{Reason: ValidationReasonRuleViolation, FieldPath: path, Expected: "null values are not accepted"})
	}
	delim, ok := token.(json.Delim)
	if !ok {
		return nil
	}
	switch delim {
	case '{':
		seen := make(map[string]struct{})
		for decoder.More() {
			fieldToken, err := decoder.Token()
			if err != nil {
				return validationDetailError(fmt.Sprintf("decode runtime Attempt result: %v", err),
					ValidationDetail{Reason: ValidationReasonInvalidJSON, Expected: "the result must be exactly one JSON object"})
			}
			field, ok := fieldToken.(string)
			if !ok {
				return validationDetailError("decode runtime Attempt result: object field is not a string",
					ValidationDetail{Reason: ValidationReasonInvalidJSON, Expected: "the result must be exactly one JSON object"})
			}
			if _, duplicate := seen[field]; duplicate {
				return validationDetailError(fmt.Sprintf("duplicate JSON field %q", field),
					ValidationDetail{Reason: ValidationReasonDuplicateField, FieldPath: joinJSONPath(path, field), Expected: "no duplicate JSON fields"})
			}
			seen[field] = struct{}{}
			valueToken, err := decoder.Token()
			if err != nil {
				return validationDetailError(fmt.Sprintf("decode runtime Attempt result: %v", err),
					ValidationDetail{Reason: ValidationReasonInvalidJSON, Expected: "the result must be exactly one JSON object"})
			}
			if err := walkJSONValue(decoder, valueToken, joinJSONPath(path, field)); err != nil {
				return err
			}
		}
	case '[':
		for index := 0; decoder.More(); index++ {
			valueToken, err := decoder.Token()
			if err != nil {
				return validationDetailError(fmt.Sprintf("decode runtime Attempt result: %v", err),
					ValidationDetail{Reason: ValidationReasonInvalidJSON, Expected: "the result must be exactly one JSON object"})
			}
			if err := walkJSONValue(decoder, valueToken, fmt.Sprintf("%s[%d]", path, index)); err != nil {
				return err
			}
		}
	default:
		return validationDetailError(fmt.Sprintf("decode runtime Attempt result: unexpected JSON delimiter %q", delim),
			ValidationDetail{Reason: ValidationReasonInvalidJSON, Expected: "the result must be exactly one JSON object"})
	}
	if _, err := decoder.Token(); err != nil {
		return validationDetailError(fmt.Sprintf("decode runtime Attempt result: %v", err),
			ValidationDetail{Reason: ValidationReasonInvalidJSON, Expected: "the result must be exactly one JSON object"})
	}
	return nil
}

func joinJSONPath(parent, field string) string {
	if parent == "" {
		return field
	}
	return parent + "." + field
}

// Compile deterministically lowers a validated result to Blackboard v2. The
// caller remains the sole owner of the idempotency key and trusted identity.
func Compile(result RuntimeAttemptResult, idempotencyKey string) (blackboardv2.ChangeBatch, error) {
	if err := validateResult(result); err != nil {
		return blackboardv2.ChangeBatch{}, err
	}
	if strings.TrimSpace(idempotencyKey) == "" {
		return blackboardv2.ChangeBatch{}, fmt.Errorf("idempotency key is required")
	}
	tested := append([]RuntimeTestedTarget(nil), result.TestedTargets...)
	produced := append([]RuntimeVersionedTarget(nil), result.ProducedTargets...)
	sort.Slice(tested, func(i, j int) bool { return tested[i].Key < tested[j].Key })
	sort.Slice(produced, func(i, j int) bool { return produced[i].Key < produced[j].Key })

	changes := make([]blackboardv2.Change, 0, len(tested)*2+len(produced)+2)
	for _, target := range tested {
		if target.CreateObjective != nil {
			changes = append(changes, blackboardv2.Change{
				Op: "create", Key: target.Key, Type: "objective",
				Record: blackboardv2.ObjectiveRecord{Status: "open", Objective: target.CreateObjective.Objective},
			})
		}
	}
	attemptVersion := result.Attempt.ExpectedVersion
	if result.Attempt.Create {
		attemptVersion = 1
		changes = append(changes, blackboardv2.Change{
			Op: "create", Key: result.Attempt.Key, Type: "attempt",
			Record: blackboardv2.AttemptRecord{Status: "open", Summary: result.Attempt.Summary},
		})
	}
	for _, target := range tested {
		changes = append(changes, blackboardv2.Change{Op: "relate", From: result.Attempt.Key, Relation: "tests", To: target.Key})
	}
	for _, target := range produced {
		changes = append(changes, blackboardv2.Change{Op: "relate", From: result.Attempt.Key, Relation: "produced", To: target.Key})
	}
	changes = append(changes, blackboardv2.Change{
		Op: "transition", Key: result.Attempt.Key, Version: attemptVersion,
		Status: string(result.Attempt.Outcome), Summary: result.Attempt.Summary,
	})
	return blackboardv2.ChangeBatch{Schema: "semantic-change-batch/v2", IdempotencyKey: idempotencyKey, Changes: changes}, nil
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
	if len(result.TestedTargets) > 32 || len(result.ProducedTargets) > 32 {
		return validationDetailError("runtime Attempt result target count exceeds 32",
			ValidationDetail{Reason: ValidationReasonOversizedValue, FieldPath: "tested_targets", Expected: "at most 32 target entries"})
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
