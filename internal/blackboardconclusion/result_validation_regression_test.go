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
