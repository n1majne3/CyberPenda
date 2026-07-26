// Package blackboardconclusion owns the closed model-produced semantic result
// used by assisted Harness control Turns.
package blackboardconclusion

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
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
)

type RuntimeAttemptOutcome string

const (
	RuntimeAttemptSucceeded    RuntimeAttemptOutcome = "succeeded"
	RuntimeAttemptFailed       RuntimeAttemptOutcome = "failed"
	RuntimeAttemptBlocked      RuntimeAttemptOutcome = "blocked"
	RuntimeAttemptInconclusive RuntimeAttemptOutcome = "inconclusive"
)

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
		return ValidatedResult{}, fmt.Errorf("runtime Attempt result is empty")
	}
	if len(raw) > maxResultBytes {
		return ValidatedResult{}, fmt.Errorf("runtime Attempt result exceeds 64 KiB")
	}
	if !utf8.Valid(raw) {
		return ValidatedResult{}, fmt.Errorf("runtime Attempt result must be valid UTF-8")
	}
	if err := rejectDuplicateJSONFields(raw); err != nil {
		return ValidatedResult{}, err
	}
	var topLevel map[string]json.RawMessage
	if err := json.Unmarshal(raw, &topLevel); err != nil {
		return ValidatedResult{}, fmt.Errorf("decode runtime Attempt result: %w", err)
	}
	if _, ok := topLevel["base_revision"]; !ok {
		return ValidatedResult{}, fmt.Errorf("base_revision is required")
	}
	for _, field := range []string{"tested_targets", "produced_targets"} {
		if value, ok := topLevel[field]; ok && bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
			return ValidatedResult{}, fmt.Errorf("%s must be an array", field)
		}
	}
	if rawAttempt, ok := topLevel["attempt"]; ok {
		var attemptFields map[string]json.RawMessage
		if err := json.Unmarshal(rawAttempt, &attemptFields); err == nil {
			if rawCreate, present := attemptFields["create"]; present {
				var create bool
				if err := json.Unmarshal(rawCreate, &create); err == nil && !create {
					return ValidatedResult{}, fmt.Errorf("attempt.create must be true when present")
				}
			}
		}
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var result RuntimeAttemptResult
	if err := decoder.Decode(&result); err != nil {
		return ValidatedResult{}, fmt.Errorf("decode runtime Attempt result: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return ValidatedResult{}, fmt.Errorf("runtime Attempt result has trailing JSON")
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

func rejectDuplicateJSONFields(raw []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	first, err := decoder.Token()
	if err != nil {
		return fmt.Errorf("decode runtime Attempt result: %w", err)
	}
	if err := walkJSONValue(decoder, first); err != nil {
		return err
	}
	return nil
}

func walkJSONValue(decoder *json.Decoder, token json.Token) error {
	if token == nil {
		return fmt.Errorf("runtime Attempt result does not accept null values")
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
				return fmt.Errorf("decode runtime Attempt result: %w", err)
			}
			field, ok := fieldToken.(string)
			if !ok {
				return fmt.Errorf("decode runtime Attempt result: object field is not a string")
			}
			if _, duplicate := seen[field]; duplicate {
				return fmt.Errorf("duplicate JSON field %q", field)
			}
			seen[field] = struct{}{}
			valueToken, err := decoder.Token()
			if err != nil {
				return fmt.Errorf("decode runtime Attempt result: %w", err)
			}
			if err := walkJSONValue(decoder, valueToken); err != nil {
				return err
			}
		}
	case '[':
		for decoder.More() {
			valueToken, err := decoder.Token()
			if err != nil {
				return fmt.Errorf("decode runtime Attempt result: %w", err)
			}
			if err := walkJSONValue(decoder, valueToken); err != nil {
				return err
			}
		}
	default:
		return fmt.Errorf("decode runtime Attempt result: unexpected JSON delimiter %q", delim)
	}
	if _, err := decoder.Token(); err != nil {
		return fmt.Errorf("decode runtime Attempt result: %w", err)
	}
	return nil
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
		return fmt.Errorf("schema must be %q", runtimeAttemptResultSchema)
	}
	if result.BaseRevision < 0 {
		return fmt.Errorf("base_revision must not be negative")
	}
	if err := validateTypedKey(result.Attempt.Key, "attempt:", "attempt.key"); err != nil {
		return err
	}
	if result.Attempt.Create == (result.Attempt.ExpectedVersion > 0) {
		return fmt.Errorf("attempt requires exactly one of create=true or expected_version")
	}
	if result.Attempt.ExpectedVersion < 0 {
		return fmt.Errorf("attempt.expected_version must be positive")
	}
	if err := validateText(result.Attempt.Summary, 1024, "attempt.summary"); err != nil {
		return err
	}
	switch result.Attempt.Outcome {
	case RuntimeAttemptSucceeded, RuntimeAttemptFailed, RuntimeAttemptBlocked, RuntimeAttemptInconclusive:
	default:
		return fmt.Errorf("attempt.outcome must be succeeded, failed, blocked, or inconclusive")
	}
	if len(result.TestedTargets) == 0 {
		return fmt.Errorf("tested_targets must be a non-empty array")
	}
	if len(result.TestedTargets) > 32 || len(result.ProducedTargets) > 32 {
		return fmt.Errorf("runtime Attempt result target count exceeds 32")
	}
	seen := map[string]bool{result.Attempt.Key: true}
	for index, target := range result.TestedTargets {
		path := fmt.Sprintf("tested_targets[%d]", index)
		if err := validateKey(target.Key, path+".key"); err != nil {
			return err
		}
		if seen[target.Key] {
			return fmt.Errorf("%s.key is duplicated", path)
		}
		seen[target.Key] = true
		created := target.CreateObjective != nil
		if created == (target.ExpectedVersion > 0) || target.ExpectedVersion < 0 {
			return fmt.Errorf("%s requires exactly one of create_objective or expected_version", path)
		}
		if created {
			if !strings.HasPrefix(target.Key, "objective:") {
				return fmt.Errorf("%s.key must use the objective: prefix", path)
			}
			if err := validateText(target.CreateObjective.Objective, 1024, path+".create_objective.objective"); err != nil {
				return err
			}
		}
	}
	if result.Attempt.Outcome == RuntimeAttemptSucceeded && len(result.ProducedTargets) == 0 {
		return fmt.Errorf("succeeded Attempt requires at least one produced target")
	}
	if result.Attempt.Outcome != RuntimeAttemptSucceeded && len(result.ProducedTargets) != 0 {
		return fmt.Errorf("produced_targets are accepted only for a succeeded Attempt")
	}
	for index, target := range result.ProducedTargets {
		path := fmt.Sprintf("produced_targets[%d]", index)
		if err := validateKey(target.Key, path+".key"); err != nil {
			return err
		}
		if target.ExpectedVersion < 1 {
			return fmt.Errorf("%s.expected_version must be positive", path)
		}
		if seen[target.Key] {
			return fmt.Errorf("%s.key is duplicated", path)
		}
		seen[target.Key] = true
	}
	return nil
}

func validateTypedKey(key, prefix, path string) error {
	if err := validateKey(key, path); err != nil {
		return err
	}
	if !strings.HasPrefix(key, prefix) {
		return fmt.Errorf("%s must use the %s prefix", path, prefix)
	}
	return nil
}

func validateKey(key, path string) error {
	if key == "" || len(key) > 96 {
		return fmt.Errorf("%s must be non-empty and at most 96 ASCII characters", path)
	}
	for _, value := range key {
		if value < 0x20 || value > 0x7e {
			return fmt.Errorf("%s must be readable ASCII", path)
		}
	}
	return nil
}

func validateText(value string, limit int, path string) error {
	if strings.TrimSpace(value) == "" || !utf8.ValidString(value) || len([]byte(value)) > limit {
		return fmt.Errorf("%s must be non-empty valid UTF-8 and at most %d bytes", path, limit)
	}
	return nil
}
