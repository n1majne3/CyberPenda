package blackboardconclusion_test

import (
	"bytes"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"pentest/internal/blackboardconclusion"
	"pentest/internal/blackboardv2"
)

func TestDecodeAndCompileNewAttemptAgainstCreatedObjective(t *testing.T) {
	raw := []byte(`{
		"schema":"runtime-attempt-result/v1",
		"base_revision":0,
		"attempt":{
			"key":"attempt:juice-shop-search-sqli",
			"create":true,
			"summary":"Tested search SQL injection payloads without establishing exploitability.",
			"outcome":"inconclusive"
		},
		"tested_targets":[{
			"key":"objective:juice-shop-search-sqli",
			"create_objective":{"objective":"Determine whether Juice Shop search is vulnerable to SQL injection."}
		}],
		"produced_targets":[]
	}`)

	validated, err := blackboardconclusion.Decode(raw)
	if err != nil {
		t.Fatalf("Decode() error = %v", err)
	}
	if string(validated.CanonicalJSON) != `{"schema":"runtime-attempt-result/v1","base_revision":0,"attempt":{"key":"attempt:juice-shop-search-sqli","create":true,"summary":"Tested search SQL injection payloads without establishing exploitability.","outcome":"inconclusive"},"tested_targets":[{"key":"objective:juice-shop-search-sqli","create_objective":{"objective":"Determine whether Juice Shop search is vulnerable to SQL injection."}}],"produced_targets":[]}` {
		t.Fatalf("CanonicalJSON = %s", validated.CanonicalJSON)
	}
	if validated.SHA256 == "" {
		t.Fatal("SHA256 is empty")
	}

	batch, err := blackboardconclusion.Compile(validated.Result, "assisted-conclusion:receipt-1")
	if err != nil {
		t.Fatalf("Compile() error = %v", err)
	}
	want := blackboardv2.ChangeBatch{
		Schema:         "semantic-change-batch/v2",
		IdempotencyKey: "assisted-conclusion:receipt-1",
		Changes: []blackboardv2.Change{
			{Op: "create", Key: "objective:juice-shop-search-sqli", Type: "objective", Record: blackboardv2.ObjectiveRecord{Status: "open", Objective: "Determine whether Juice Shop search is vulnerable to SQL injection."}},
			{Op: "create", Key: "attempt:juice-shop-search-sqli", Type: "attempt", Record: blackboardv2.AttemptRecord{Status: "open", Summary: "Tested search SQL injection payloads without establishing exploitability."}},
			{Op: "relate", From: "attempt:juice-shop-search-sqli", Relation: "tests", To: "objective:juice-shop-search-sqli"},
			{Op: "transition", Key: "attempt:juice-shop-search-sqli", Version: 1, Status: "inconclusive", Summary: "Tested search SQL injection payloads without establishing exploitability."},
		},
	}
	if !reflect.DeepEqual(batch, want) {
		t.Fatalf("Compile() = %#v, want %#v", batch, want)
	}
}

func TestDecodeAndCompilePreservesProjectBlackboardKeyStyle(t *testing.T) {
	raw := []byte(`{
		"schema":"runtime-attempt-result/v1",
		"base_revision":12,
		"attempt":{
			"key":"attempt/3124",
			"create":true,
			"summary":"Recorded the current challenge work without changing its Blackboard Key style.",
			"outcome":"inconclusive"
		},
		"tested_targets":[{
			"key":"objective/solve-nssctf-arena",
			"expected_version":3
		}],
		"produced_targets":[]
	}`)

	validated, err := blackboardconclusion.Decode(raw)
	if err != nil {
		t.Fatalf("Decode() error = %v", err)
	}
	batch, err := blackboardconclusion.Compile(validated.Result, "assisted-conclusion:preserve-key-style")
	if err != nil {
		t.Fatalf("Compile() error = %v", err)
	}
	want := []blackboardv2.Change{
		{Op: "create", Key: "attempt/3124", Type: "attempt", Record: blackboardv2.AttemptRecord{Status: "open", Summary: "Recorded the current challenge work without changing its Blackboard Key style."}},
		{Op: "relate", From: "attempt/3124", Relation: "tests", To: "objective/solve-nssctf-arena"},
		{Op: "transition", Key: "attempt/3124", Version: 1, Status: "inconclusive", Summary: "Recorded the current challenge work without changing its Blackboard Key style."},
	}
	if !reflect.DeepEqual(batch.Changes, want) {
		t.Fatalf("Compile().Changes = %#v, want %#v", batch.Changes, want)
	}
}

func TestDecodeRejectsAuthorityLifecycleAndInvalidAttemptFields(t *testing.T) {
	valid := `{"schema":"runtime-attempt-result/v1","base_revision":2,"attempt":{"key":"attempt:search","expected_version":1,"summary":"Tested the search endpoint.","outcome":"failed"},"tested_targets":[{"key":"entity:search","expected_version":1}],"produced_targets":[]}`
	for _, field := range []string{"project_id", "task_id", "continuation_id", "trusted_origin", "idempotency_key", "credential_ref", "api_key", "task_status", "finish", "blackboard_finish"} {
		t.Run("unknown "+field, func(t *testing.T) {
			raw := strings.Replace(valid, `"schema":`, `"`+field+`":"forbidden","schema":`, 1)
			if _, err := blackboardconclusion.Decode([]byte(raw)); err == nil {
				t.Fatalf("Decode() accepted forbidden field %q", field)
			}
		})
	}
	for _, test := range []struct {
		name string
		raw  string
	}{
		{name: "model supplied interrupted", raw: strings.Replace(valid, `"outcome":"failed"`, `"outcome":"interrupted"`, 1)},
		{name: "create and expected version", raw: strings.Replace(valid, `"expected_version":1`, `"create":true,"expected_version":1`, 1)},
		{name: "false create intent", raw: strings.Replace(valid, `"expected_version":1`, `"create":false,"expected_version":1`, 1)},
		{name: "missing attempt intent", raw: strings.Replace(valid, `"expected_version":1,`, "", 1)},
		{name: "empty tested targets", raw: strings.Replace(valid, `"tested_targets":[{"key":"entity:search","expected_version":1}]`, `"tested_targets":[]`, 1)},
		{name: "produced target on failure", raw: strings.Replace(valid, `"produced_targets":[]`, `"produced_targets":[{"key":"fact:outcome","expected_version":1}]`, 1)},
		{name: "trailing JSON", raw: valid + `{}`},
	} {
		t.Run(test.name, func(t *testing.T) {
			if _, err := blackboardconclusion.Decode([]byte(test.raw)); err == nil {
				t.Fatal("Decode() accepted an invalid runtime Attempt result")
			}
		})
	}
	if _, err := blackboardconclusion.Decode(append([]byte(valid), bytes.Repeat([]byte(" "), (64<<10)-len(valid)+1)...)); err == nil {
		t.Fatal("Decode() accepted a result larger than 64 KiB")
	}
	invalidUTF8 := append([]byte(valid[:len(valid)-1]), 0xff, '}')
	if _, err := blackboardconclusion.Decode(invalidUTF8); err == nil {
		t.Fatal("Decode() accepted invalid UTF-8")
	}
}

func TestCompileExistingSucceededAttemptSortsTestedAndProducedTargets(t *testing.T) {
	result := blackboardconclusion.RuntimeAttemptResult{
		Schema: "runtime-attempt-result/v1", BaseRevision: 9,
		Attempt: blackboardconclusion.RuntimeAttemptDescriptor{
			Key: "attempt:search", ExpectedVersion: 3, Summary: "Established reusable search behavior.", Outcome: blackboardconclusion.RuntimeAttemptSucceeded,
		},
		TestedTargets: []blackboardconclusion.RuntimeTestedTarget{
			{Key: "objective:zeta", ExpectedVersion: 2},
			{Key: "entity:alpha", ExpectedVersion: 1},
		},
		ProducedTargets: []blackboardconclusion.RuntimeVersionedTarget{
			{Key: "finding:zeta", ExpectedVersion: 2},
			{Key: "fact:alpha", ExpectedVersion: 1},
		},
	}
	batch, err := blackboardconclusion.Compile(result, "assisted-conclusion:receipt-success")
	if err != nil {
		t.Fatalf("Compile() error = %v", err)
	}
	want := []blackboardv2.Change{
		{Op: "relate", From: "attempt:search", Relation: "tests", To: "entity:alpha"},
		{Op: "relate", From: "attempt:search", Relation: "tests", To: "objective:zeta"},
		{Op: "relate", From: "attempt:search", Relation: "produced", To: "fact:alpha"},
		{Op: "relate", From: "attempt:search", Relation: "produced", To: "finding:zeta"},
		{Op: "transition", Key: "attempt:search", Version: 3, Status: "succeeded", Summary: "Established reusable search behavior."},
	}
	if !reflect.DeepEqual(batch.Changes, want) {
		t.Fatalf("Compile().Changes = %#v, want %#v", batch.Changes, want)
	}
}

func TestDecodeRejectsAbsentBaseRevisionAndDuplicateJSONKeys(t *testing.T) {
	valid := `{"schema":"runtime-attempt-result/v1","base_revision":0,"attempt":{"key":"attempt:search","create":true,"summary":"Tested the search endpoint.","outcome":"failed"},"tested_targets":[{"key":"objective:search","create_objective":{"objective":"Test the search endpoint."}}],"produced_targets":[]}`
	for _, test := range []struct {
		name string
		raw  string
	}{
		{
			name: "absent base revision",
			raw:  strings.Replace(valid, `"base_revision":0,`, "", 1),
		},
		{
			name: "duplicate top-level field",
			raw:  strings.Replace(valid, `"base_revision":0,`, `"base_revision":0,"base_revision":1,`, 1),
		},
		{
			name: "duplicate nested field",
			raw:  strings.Replace(valid, `"create":true,`, `"create":true,"create":true,`, 1),
		},
		{
			name: "null scalar",
			raw:  strings.Replace(valid, `"base_revision":0`, `"base_revision":null`, 1),
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			if _, err := blackboardconclusion.Decode([]byte(test.raw)); err == nil {
				t.Fatal("Decode() accepted an ambiguous closed result")
			}
		})
	}
}

func TestDecodeRejectsNearIntegerBaseRevision(t *testing.T) {
	raw := []byte(`{"schema":"runtime-attempt-result/v1","base_revision":1.0000000000000001,"attempt":{"key":"attempt:near-integer","create":true,"summary":"Preserve the exact wire number.","outcome":"failed"},"tested_targets":[{"key":"objective:near-integer","create_objective":{"objective":"Reject rounded revisions."}}],"produced_targets":[]}`)
	if _, err := blackboardconclusion.Decode(raw); err == nil {
		t.Fatal("Decode() accepted a near-integer base_revision")
	}
}

func TestDecodeRejectsNullTargetArrays(t *testing.T) {
	raw := []byte(`{"schema":"runtime-attempt-result/v1","base_revision":0,"attempt":{"key":"attempt:search","create":true,"summary":"Tested the search endpoint.","outcome":"failed"},"tested_targets":[{"key":"objective:search","create_objective":{"objective":"Test the search endpoint."}}],"produced_targets":null}`)
	if _, err := blackboardconclusion.Decode(raw); err == nil {
		t.Fatal("Decode() accepted a null produced_targets array")
	}
}

func TestDecodeValidationDetailIsBounded(t *testing.T) {
	valid := `{"schema":"runtime-attempt-result/v1","base_revision":2,"attempt":{"key":"attempt:search","expected_version":1,"summary":"Tested the search endpoint.","outcome":"failed"},"tested_targets":[{"key":"entity:search","expected_version":1}],"produced_targets":[]}`
	tests := []struct {
		name      string
		raw       string
		reason    blackboardconclusion.ValidationReason
		fieldPath string
		expected  string
	}{
		{name: "empty result", raw: ``, reason: blackboardconclusion.ValidationReasonEmptyResult},
		{name: "oversized result", raw: strings.Repeat("x", (64<<10)+1), reason: blackboardconclusion.ValidationReasonResultTooLarge},
		{name: "empty attempt key", raw: strings.Replace(valid, `"key":"attempt:search"`, `"key":""`, 1), reason: blackboardconclusion.ValidationReasonRuleViolation, fieldPath: "attempt.key", expected: "non-empty"},
		{name: "unknown top-level field", raw: strings.Replace(valid, `"schema":`, `"surprise":true,"schema":`, 1), reason: blackboardconclusion.ValidationReasonUnknownField, fieldPath: "surprise"},
		{name: "unknown nested field", raw: strings.Replace(valid, `"summary":`, `"surprise":true,"summary":`, 1), reason: blackboardconclusion.ValidationReasonUnknownField, fieldPath: "surprise"},
		{name: "invalid enum", raw: strings.Replace(valid, `"outcome":"failed"`, `"outcome":"interrupted"`, 1), reason: blackboardconclusion.ValidationReasonInvalidEnumValue, fieldPath: "attempt.outcome", expected: "succeeded, failed, blocked, or inconclusive"},
		{name: "oversized summary", raw: strings.Replace(valid, "Tested the search endpoint.", strings.Repeat("x", 1100), 1), reason: blackboardconclusion.ValidationReasonOversizedValue, fieldPath: "attempt.summary", expected: "1024"},
		{name: "oversized key", raw: strings.Replace(valid, `"key":"attempt:search"`, `"key":"attempt:`+strings.Repeat("y", 200)+`"`, 1), reason: blackboardconclusion.ValidationReasonOversizedValue, fieldPath: "attempt.key", expected: "96"},
		{name: "missing base revision", raw: strings.Replace(valid, `"base_revision":2,`, "", 1), reason: blackboardconclusion.ValidationReasonMissingField, fieldPath: "base_revision"},
		{name: "duplicate field", raw: strings.Replace(valid, `"base_revision":2,`, `"base_revision":2,"base_revision":1,`, 1), reason: blackboardconclusion.ValidationReasonDuplicateField, fieldPath: "base_revision"},
		{name: "trailing JSON", raw: valid + `{}`, reason: blackboardconclusion.ValidationReasonInvalidJSON, expected: "exactly one JSON object"},
		{name: "empty tested targets", raw: strings.Replace(valid, `"tested_targets":[{"key":"entity:search","expected_version":1}]`, `"tested_targets":[]`, 1), reason: blackboardconclusion.ValidationReasonRuleViolation, fieldPath: "tested_targets", expected: "non-empty"},
		{name: "null produced targets", raw: strings.Replace(valid, `"produced_targets":[]`, `"produced_targets":null`, 1), reason: blackboardconclusion.ValidationReasonRuleViolation, fieldPath: "produced_targets", expected: "null"},
		{name: "succeeded without produced targets", raw: strings.Replace(valid, `"outcome":"failed"`, `"outcome":"succeeded"`, 1), reason: blackboardconclusion.ValidationReasonRuleViolation, fieldPath: "produced_targets", expected: "produced"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := blackboardconclusion.Decode([]byte(test.raw))
			if err == nil {
				t.Fatal("Decode() accepted an invalid result")
			}
			detail := blackboardconclusion.DecodeDetailOf(err)
			if detail.Reason != test.reason {
				t.Fatalf("DecodeDetailOf() reason = %q, want %q (error %v)", detail.Reason, test.reason, err)
			}
			if detail.FieldPath != test.fieldPath {
				t.Fatalf("DecodeDetailOf() field path = %q, want %q", detail.FieldPath, test.fieldPath)
			}
			if test.expected != "" && !strings.Contains(detail.Expected, test.expected) {
				t.Fatalf("DecodeDetailOf() expected = %q, want it to contain %q", detail.Expected, test.expected)
			}
			bounded := fmt.Sprintf("%#v", detail)
			for _, forbidden := range []string{test.raw} {
				if forbidden != "" && strings.Contains(bounded, forbidden) {
					t.Fatalf("DecodeDetailOf() leaked raw result %q: %s", forbidden, bounded)
				}
			}
		})
	}
}

func TestDecodeValidationDetailBoundsUnknownFieldName(t *testing.T) {
	longName := "surprise-" + strings.Repeat("z", 512)
	raw := `{"schema":"runtime-attempt-result/v1","base_revision":0,"` + longName + `":true}`
	_, err := blackboardconclusion.Decode([]byte(raw))
	if err == nil {
		t.Fatal("Decode() accepted an unknown field")
	}
	detail := blackboardconclusion.DecodeDetailOf(err)
	if detail.Reason != blackboardconclusion.ValidationReasonUnknownField {
		t.Fatalf("DecodeDetailOf() reason = %q, want unknown_field", detail.Reason)
	}
	if len(detail.FieldPath) > 96 {
		t.Fatalf("DecodeDetailOf() unbounded field path of %d bytes: %q", len(detail.FieldPath), detail.FieldPath)
	}
	if !strings.HasPrefix(detail.FieldPath, "surprise-") {
		t.Fatalf("DecodeDetailOf() field path = %q, want bounded surprise- prefix", detail.FieldPath)
	}
	if strings.Contains(detail.FieldPath, strings.Repeat("z", 100)) {
		t.Fatalf("DecodeDetailOf() kept the full raw field name: %q", detail.FieldPath)
	}
}

func TestDecodeDetailOfFallsBackToGenericReason(t *testing.T) {
	detail := blackboardconclusion.DecodeDetailOf(fmt.Errorf("unrelated transport failure"))
	if detail.Reason != blackboardconclusion.ValidationReasonInvalidResult || detail.FieldPath != "" || detail.Expected != "" {
		t.Fatalf("DecodeDetailOf() = %#v, want generic invalid_result", detail)
	}
}
