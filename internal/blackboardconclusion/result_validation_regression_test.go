package blackboardconclusion_test

import (
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
