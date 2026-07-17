package blackboardv2

import (
	"errors"
	"fmt"
	"testing"

	"pentest/internal/blackboardv2contract"
)

func TestServiceRelationshipEndpointValidationMatchesCompleteContractMatrix(t *testing.T) {
	harness, err := blackboardv2contract.NewHarness()
	if err != nil {
		t.Fatalf("load contract harness: %v", err)
	}
	cases, err := harness.RelationshipCases()
	if err != nil {
		t.Fatalf("load relationship cases: %v", err)
	}
	for _, testCase := range cases {
		testCase := testCase
		t.Run(fmt.Sprintf("%s/%s/%s", testCase.Relation, testCase.From, testCase.To), func(t *testing.T) {
			err := validateRelationshipEndpoint(testCase.Relation, testCase.From, testCase.To, "changes[0].relation")
			if testCase.Relation == "supersedes" {
				var semanticErr *Error
				if !errors.As(err, &semanticErr) || semanticErr.Code != "semantic_validation" || semanticErr.Path != "changes[0].relation" || semanticErr.Message != "supersedes is created only by the supersede operation" {
					t.Fatalf("direct supersedes error = %#v", err)
				}
				return
			}
			if testCase.Allowed {
				if err != nil {
					t.Fatalf("allowed endpoint rejected: %#v", err)
				}
				return
			}
			var semanticErr *Error
			if !errors.As(err, &semanticErr) || semanticErr.Code != "semantic_validation" || semanticErr.Path != "changes[0].relation" {
				t.Fatalf("rejected endpoint error = %#v", err)
			}
			if semanticErr.Details["relation"] != testCase.Relation || semanticErr.Details["from_type"] != testCase.From || semanticErr.Details["to_type"] != testCase.To {
				t.Fatalf("rejected endpoint details = %#v", semanticErr.Details)
			}
		})
	}
}
