package blackboardconclusion_test

import (
	"fmt"
	"strings"
	"testing"

	"pentest/internal/blackboardconclusion"
)

const validFailedResult = `{"schema":"runtime-attempt-result/v1","base_revision":2,"attempt":{"key":"attempt:search","expected_version":1,"summary":"Tested the search endpoint.","outcome":"failed"},"tested_targets":[{"key":"entity:search","expected_version":1}],"produced_targets":[]}`

func TestDecodeRequiresProducedTargets(t *testing.T) {
	for _, outcome := range []string{"failed", "blocked", "inconclusive"} {
		t.Run(outcome, func(t *testing.T) {
			valid := strings.Replace(validFailedResult, `"outcome":"failed"`, `"outcome":"`+outcome+`"`, 1)
			result, err := blackboardconclusion.Decode([]byte(valid))
			if err != nil {
				t.Fatalf("Decode() rejected an empty produced_targets array: %v", err)
			}
			if !strings.Contains(string(result.CanonicalJSON), `"produced_targets":[]`) {
				t.Fatalf("CanonicalJSON = %s; want an empty produced_targets array", result.CanonicalJSON)
			}
			missing := strings.Replace(valid, `,"produced_targets":[]`, "", 1)
			_, err = blackboardconclusion.Decode([]byte(missing))
			if err == nil {
				t.Fatal("Decode() accepted an absent produced_targets field")
			}
			detail := blackboardconclusion.DecodeDetailOf(err)
			if detail.Reason != blackboardconclusion.ValidationReasonMissingField || detail.FieldPath != "produced_targets" {
				t.Fatalf("DecodeDetailOf() = %#v; want missing_field at produced_targets", detail)
			}
		})
	}
}

func TestDecodeRejectsNonPositiveSuppliedVersions(t *testing.T) {
	createdAttempt := strings.Replace(validFailedResult, `"expected_version":1`, `"create":true`, 1)
	createdObjective := strings.Replace(createdAttempt, `"expected_version":1`, `"create_objective":{"objective":"Test the search endpoint."}`, 1)
	for _, valid := range []string{validFailedResult, createdAttempt, createdObjective} {
		if _, err := blackboardconclusion.Decode([]byte(valid)); err != nil {
			t.Fatalf("Decode() rejected valid create or version intent: %v", err)
		}
	}
	for _, version := range []string{"0", "0.0", "0e0", "-1"} {
		for _, test := range []struct {
			name string
			raw  string
		}{
			{name: "Attempt create", raw: strings.Replace(createdAttempt, `"create":true`, `"create":true,"expected_version":`+version, 1)},
			{name: "Attempt update", raw: strings.Replace(validFailedResult, `"expected_version":1`, `"expected_version":`+version, 1)},
			{name: "Objective create", raw: strings.Replace(createdObjective, `"create_objective":`, `"expected_version":`+version+`,"create_objective":`, 1)},
			{name: "tested target update", raw: strings.Replace(createdAttempt, `"expected_version":1`, `"expected_version":`+version, 1)},
			{name: "produced target", raw: strings.Replace(strings.Replace(validFailedResult, `"outcome":"failed"`, `"outcome":"succeeded"`, 1), `"produced_targets":[]`, `"produced_targets":[{"key":"fact:search","expected_version":`+version+`}]`, 1)},
		} {
			t.Run(test.name+"/"+version, func(t *testing.T) {
				if _, err := blackboardconclusion.Decode([]byte(test.raw)); err == nil {
					t.Fatal("Decode() accepted a non-positive supplied expected_version")
				}
			})
		}
	}
}

func TestDecodeTargetCountFieldPath(t *testing.T) {
	for _, field := range []string{"tested_targets", "produced_targets"} {
		for _, count := range []int{32, 33} {
			t.Run(fmt.Sprintf("%s/%d", field, count), func(t *testing.T) {
				targets := make([]string, count)
				for index := range targets {
					targets[index] = fmt.Sprintf(`{"key":"fact:target-%d","expected_version":1}`, index)
				}
				raw := validFailedResult
				if field == "tested_targets" {
					raw = strings.Replace(raw, `"tested_targets":[{"key":"entity:search","expected_version":1}]`, `"tested_targets":[`+strings.Join(targets, ",")+`]`, 1)
				} else {
					raw = strings.Replace(raw, `"outcome":"failed"`, `"outcome":"succeeded"`, 1)
					raw = strings.Replace(raw, `"produced_targets":[]`, `"produced_targets":[`+strings.Join(targets, ",")+`]`, 1)
				}
				_, err := blackboardconclusion.Decode([]byte(raw))
				if count == 32 {
					if err != nil {
						t.Fatalf("Decode() rejected 32 targets: %v", err)
					}
					return
				}
				if err == nil {
					t.Fatal("Decode() accepted 33 targets")
				}
				detail := blackboardconclusion.DecodeDetailOf(err)
				if detail.Reason != blackboardconclusion.ValidationReasonOversizedValue || detail.FieldPath != field {
					t.Fatalf("DecodeDetailOf() = %#v; want value_too_large at %s", detail, field)
				}
			})
		}
	}
}
