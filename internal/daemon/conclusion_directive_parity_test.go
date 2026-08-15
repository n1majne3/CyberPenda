package daemon

import (
	"strings"
	"testing"

	"pentest/internal/owner"
)

// The Task and Session conclusion directives are one owner-neutral prompt with
// owner-specific nouns. These tests hold both twins to the same semantic rules
// so the prompts cannot drift apart again (ADR 0020).
type directivePair struct {
	name     string
	conclude func(int) string
	repair   func(int, owner.ConclusionValidationDetail) string
	regen    func(int) string
	board    string
	finishes string
}

func directivePairs() []directivePair {
	return []directivePair{
		{
			name: "task", conclude: func(rev int) string { return concludeDirective(taskConclusionDirectiveProfile, rev) },
			repair: func(rev int, detail owner.ConclusionValidationDetail) string {
				return repairDirective(taskConclusionDirectiveProfile, rev, detail)
			},
			regen: func(rev int) string { return regenerateDirective(taskConclusionDirectiveProfile, rev) },
			board: "Blackboard", finishes: "finish the Task",
		},
		{
			name: "session", conclude: func(rev int) string { return concludeDirective(sessionConclusionDirectiveProfile, rev) },
			repair: func(rev int, detail owner.ConclusionValidationDetail) string {
				return repairDirective(sessionConclusionDirectiveProfile, rev, detail)
			},
			regen: func(rev int) string { return regenerateDirective(sessionConclusionDirectiveProfile, rev) },
			board: "Session Blackboard", finishes: "finish the Session",
		},
	}
}

func TestConcludeDirectivesShareAntiFabricationRules(t *testing.T) {
	for _, pair := range directivePairs() {
		directive := pair.conclude(7)
		for _, rule := range []string{
			"Use inconclusive/failed/blocked when the Turn did not create durable produced graph targets.",
			"succeeded requires at least one produced_targets entry that references an already-existing " + pair.board + " key with expected_version",
			"do not invent produced_targets on an empty board",
			"Describe one Attempt and at least one tested target.",
			"never change punctuation or switch between ':' and '/'",
		} {
			if !strings.Contains(directive, rule) {
				t.Errorf("%s conclude directive lost shared rule: %q", pair.name, rule)
			}
		}
		if !strings.Contains(directive, pair.finishes) {
			t.Errorf("%s conclude directive lost owner finish clause %q", pair.name, pair.finishes)
		}
		if !strings.Contains(directive, "with this shape and base_revision 7") || !strings.Contains(directive, `"base_revision":7`) {
			t.Errorf("%s conclude directive lost base_revision binding", pair.name)
		}
	}
}

func TestRepairDirectivesShareProducedTargetsFallbackAndExample(t *testing.T) {
	detail := owner.ConclusionValidationDetail{Reason: "shape", FieldPath: "attempt", Expected: "object"}
	for _, pair := range directivePairs() {
		directive := pair.repair(9, detail)
		for _, rule := range []string{
			`If the board has no existing produced targets, use outcome "inconclusive" (or failed/blocked) with produced_targets [].`,
			"Example:",
			`{"schema":"runtime-attempt-result/v1","base_revision":9`,
			"Validation: shape at attempt. Expected: object.",
			"Copy them exactly; never change punctuation or switch between ':' and '/'",
		} {
			if !strings.Contains(directive, rule) {
				t.Errorf("%s repair directive lost shared rule: %q", pair.name, rule)
			}
		}
		if !strings.Contains(directive, pair.finishes) {
			t.Errorf("%s repair directive lost owner finish clause %q", pair.name, pair.finishes)
		}
	}
}

func TestRegenerateDirectivesShareRules(t *testing.T) {
	for _, pair := range directivePairs() {
		directive := pair.regen(12)
		for _, rule := range []string{
			"Regenerate the semantic result against base_revision 12.",
			"Use only exact " + pair.board + " Keys and versions already present in the conversation",
			"use an inconclusive, failed, or blocked outcome with no produced targets",
		} {
			if !strings.Contains(directive, rule) {
				t.Errorf("%s regenerate directive lost shared rule: %q", pair.name, rule)
			}
		}
		if !strings.Contains(directive, pair.finishes) {
			t.Errorf("%s regenerate directive lost owner finish clause %q", pair.name, pair.finishes)
		}
	}
}
