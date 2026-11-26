package blackboardv2contract_test

import (
	"slices"
	"testing"

	"encoding/json"
	"io/fs"
	"pentest/internal/blackboardv2grammar"
)

func TestFrozenRelationshipFixtureMatchesAuthoritativeGrammar(t *testing.T) {
	harness := mustHarness(t)
	raw, err := fs.ReadFile(harness.Files(), "contractdata/relationships.json")
	if err != nil {
		t.Fatalf("read frozen relationship fixture: %v", err)
	}
	var fixture struct {
		Schema      string   `json:"schema"`
		RecordTypes []string `json:"record_types"`
		Relations   []struct {
			Relation       string   `json:"relation"`
			ReasonPolicy   string   `json:"reason_policy"`
			SelfLinkPolicy string   `json:"self_link_policy"`
			CyclePolicy    string   `json:"cycle_policy"`
			Matrix         [][]bool `json:"matrix"`
		} `json:"relations"`
	}
	if err := json.Unmarshal(raw, &fixture); err != nil {
		t.Fatalf("decode frozen relationship fixture: %v", err)
	}
	if fixture.Schema != "blackboard-v2-relationship-matrix/v1" || !slices.Equal(fixture.RecordTypes, blackboardv2grammar.RecordTypes()) {
		t.Fatalf("frozen relationship fixture header = %q, %#v", fixture.Schema, fixture.RecordTypes)
	}
	fixtureCases := make([]blackboardv2grammar.Case, 0, 11*7*7)
	for _, relation := range fixture.Relations {
		if len(relation.Matrix) != len(fixture.RecordTypes) {
			t.Fatalf("fixture relation %s has %d rows", relation.Relation, len(relation.Matrix))
		}
		for fromIndex, row := range relation.Matrix {
			if len(row) != len(fixture.RecordTypes) {
				t.Fatalf("fixture relation %s row %d has %d columns", relation.Relation, fromIndex, len(row))
			}
			for toIndex, allowed := range row {
				fixtureCases = append(fixtureCases, blackboardv2grammar.Case{
					Relation: relation.Relation, From: fixture.RecordTypes[fromIndex], To: fixture.RecordTypes[toIndex], Allowed: allowed,
					ReasonPolicy: relation.ReasonPolicy, SelfLinkPolicy: relation.SelfLinkPolicy, CyclePolicy: relation.CyclePolicy,
				})
			}
		}
	}
	grammarCases := blackboardv2grammar.Cases()
	if len(grammarCases) != 11*7*7 {
		t.Fatalf("authoritative grammar has %d cases, want 539", len(grammarCases))
	}
	if !slices.Equal(fixtureCases, grammarCases) {
		t.Fatal("frozen relationship fixture drifted from the authoritative grammar")
	}
}

func TestRelationshipSchemaEnumsMatchAuthoritativeGrammar(t *testing.T) {
	harness := mustHarness(t)
	raw, err := fs.ReadFile(harness.Files(), "contractdata/schemas/blackboard-v2.schema.json")
	if err != nil {
		t.Fatalf("read Blackboard v2 schema: %v", err)
	}
	var document struct {
		Definitions map[string]struct {
			Enum []string `json:"enum"`
		} `json:"$defs"`
	}
	if err := json.Unmarshal(raw, &document); err != nil {
		t.Fatalf("decode Blackboard v2 schema: %v", err)
	}
	all := make([]string, 0, 11)
	ordinary := make([]string, 0, 7)
	reasoned := make([]string, 0, 3)
	mutable := make([]string, 0, 10)
	for _, rule := range blackboardv2grammar.Rules() {
		all = append(all, rule.Relation)
		if rule.Relation != "supersedes" {
			mutable = append(mutable, rule.Relation)
		}
		if rule.ReasonPolicy == blackboardv2grammar.ReasonOptional {
			reasoned = append(reasoned, rule.Relation)
		} else if rule.Relation != "supersedes" {
			ordinary = append(ordinary, rule.Relation)
		}
	}
	for definition, want := range map[string][]string{
		"recordType":           blackboardv2grammar.RecordTypes(),
		"relationType":         all,
		"ordinaryRelationType": ordinary,
		"reasonRelationType":   reasoned,
		"mutableRelationType":  mutable,
	} {
		if got := document.Definitions[definition].Enum; !slices.Equal(got, want) {
			t.Errorf("schema %s = %#v, want %#v", definition, got, want)
		}
	}
}

func TestRelationshipTableEnumeratesEveryEndpointAndGraphPolicy(t *testing.T) {
	harness := mustHarness(t)
	cases, err := harness.RelationshipCases()
	if err != nil {
		t.Fatalf("load relationship cases: %v", err)
	}
	if got, want := len(cases), 11*7*7; got != want {
		t.Fatalf("relationship cases = %d, want %d", got, want)
	}

	wantCycles := map[string]string{
		"about":        "unrestricted",
		"part_of":      "acyclic_per_endpoint_family",
		"tests":        "unrestricted",
		"produced":     "unrestricted",
		"evidences":    "unrestricted",
		"supports":     "project_fact_to_project_fact_acyclic",
		"contradicts":  "reciprocal_allowed",
		"derived_from": "acyclic",
		"depends_on":   "acyclic",
		"satisfies":    "unrestricted",
		"supersedes":   "acyclic_single_current_replacement",
	}
	seen := map[string]bool{}
	allowed := 0
	for _, relationshipCase := range cases {
		identity := relationshipCase.Relation + ":" + relationshipCase.From + ":" + relationshipCase.To
		if seen[identity] {
			t.Fatalf("duplicate relationship case %s", identity)
		}
		seen[identity] = true
		if relationshipCase.Allowed {
			allowed++
		}
		if relationshipCase.SelfLinkPolicy != "reject" {
			t.Errorf("%s self-link policy = %q, want reject", identity, relationshipCase.SelfLinkPolicy)
		}
		if want := wantCycles[relationshipCase.Relation]; relationshipCase.CyclePolicy != want {
			t.Errorf("%s cycle policy = %q, want %q", identity, relationshipCase.CyclePolicy, want)
		}
		wantReason := "forbidden"
		if relationshipCase.Relation == "supports" || relationshipCase.Relation == "contradicts" || relationshipCase.Relation == "depends_on" {
			wantReason = "optional"
		}
		if relationshipCase.ReasonPolicy != wantReason {
			t.Errorf("%s reason policy = %q, want %q", identity, relationshipCase.ReasonPolicy, wantReason)
		}
	}
	if allowed != 44 {
		t.Fatalf("allowed endpoint cases = %d, want 44 (and 495 explicit rejections)", allowed)
	}
}
