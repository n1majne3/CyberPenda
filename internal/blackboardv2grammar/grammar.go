// Package blackboardv2grammar owns the closed Blackboard v2 relationship
// vocabulary and endpoint matrix shared by semantic producers and consumers.
package blackboardv2grammar

import (
	"strings"
	"unicode/utf8"
)

const (
	ReasonForbidden = "forbidden"
	ReasonOptional  = "optional"

	SelfLinkReject = "reject"

	ReasonViolationForbidden   = "reason_forbidden"
	ReasonViolationInvalid     = "reason_invalid"
	ReasonViolationInvalidUTF8 = "reason_invalid_utf8"
	ReasonViolationTooLong     = "reason_too_long"
	ReasonViolationRedundant   = "reason_redundant"
	MaxReasonBytes             = 512
)

var recordTypes = []string{"entity", "objective", "attempt", "fact", "finding", "solution", "evidence"}

type endpoint struct {
	from string
	to   string
}

// Rule defines one relationship's endpoint and policy contract.
type Rule struct {
	Relation       string
	ReasonPolicy   string
	SelfLinkPolicy string
	CyclePolicy    string
	EndpointError  string
	allowed        map[endpoint]struct{}
}

// Allows reports whether the directed endpoint pair belongs to the rule.
func (rule Rule) Allows(fromType, toType string) bool {
	_, ok := rule.allowed[endpoint{from: fromType, to: toType}]
	return ok
}

// Case is one expanded source-type/target-type conformance case.
type Case struct {
	Relation       string
	From           string
	To             string
	Allowed        bool
	ReasonPolicy   string
	SelfLinkPolicy string
	CyclePolicy    string
}

var rules = []Rule{
	newRule("about", ReasonForbidden, "unrestricted", "about must connect an allowed record to an Entity",
		pairs([]string{"objective", "attempt", "fact", "finding", "solution", "evidence"}, []string{"entity"})...),
	newRule("part_of", ReasonForbidden, "acyclic_per_endpoint_family", "part_of must stay within the Entity or Objective endpoint family",
		endpoint{"entity", "entity"}, endpoint{"objective", "objective"}),
	newRule("tests", ReasonForbidden, "unrestricted", "tests must point from an Attempt to an approved tested target",
		pairs([]string{"attempt"}, []string{"entity", "objective", "fact", "finding", "solution"})...),
	newRule("produced", ReasonForbidden, "unrestricted", "produced must point from an Attempt to a reusable outcome",
		pairs([]string{"attempt"}, []string{"entity", "objective", "fact", "finding", "solution", "evidence"})...),
	newRule("evidences", ReasonForbidden, "unrestricted", "evidences must point from Evidence to supported Project Knowledge",
		pairs([]string{"evidence"}, []string{"fact", "finding", "solution"})...),
	newRule("supports", ReasonOptional, "project_fact_to_project_fact_acyclic", "supports must connect supported semantic knowledge",
		pairs([]string{"fact"}, []string{"fact", "finding", "solution"})...),
	newRule("contradicts", ReasonOptional, "reciprocal_allowed", "contradicts must connect supported semantic knowledge",
		pairs([]string{"fact"}, []string{"fact", "finding", "solution"})...),
	newRule("derived_from", ReasonForbidden, "acyclic", "derived_from endpoint types are not allowed",
		endpoint{"objective", "fact"}, endpoint{"objective", "finding"}, endpoint{"objective", "solution"},
		endpoint{"fact", "fact"}, endpoint{"fact", "evidence"}, endpoint{"evidence", "evidence"}),
	newRule("depends_on", ReasonOptional, "acyclic", "depends_on must point from an Objective to a prerequisite Objective",
		endpoint{"objective", "objective"}),
	newRule("satisfies", ReasonForbidden, "unrestricted", "satisfies must point from current knowledge to an Objective",
		pairs([]string{"fact", "finding", "solution"}, []string{"objective"})...),
	newRule("supersedes", ReasonForbidden, "acyclic_single_current_replacement", "supersedes requires replacement and replaced records of the same supersedable type",
		endpoint{"entity", "entity"}, endpoint{"objective", "objective"}, endpoint{"fact", "fact"},
		endpoint{"finding", "finding"}, endpoint{"solution", "solution"}, endpoint{"evidence", "evidence"}),
}

func newRule(relation, reasonPolicy, cyclePolicy, endpointError string, allowed ...endpoint) Rule {
	set := make(map[endpoint]struct{}, len(allowed))
	for _, pair := range allowed {
		set[pair] = struct{}{}
	}
	return Rule{
		Relation:       relation,
		ReasonPolicy:   reasonPolicy,
		SelfLinkPolicy: SelfLinkReject,
		CyclePolicy:    cyclePolicy,
		EndpointError:  endpointError,
		allowed:        set,
	}
}

func pairs(fromTypes, toTypes []string) []endpoint {
	result := make([]endpoint, 0, len(fromTypes)*len(toTypes))
	for _, fromType := range fromTypes {
		for _, toType := range toTypes {
			result = append(result, endpoint{from: fromType, to: toType})
		}
	}
	return result
}

// RecordTypes returns the canonical matrix axis order.
func RecordTypes() []string {
	return append([]string(nil), recordTypes...)
}

// Rules returns the canonical relationship order. Rule endpoint maps are
// private and read-only to callers.
func Rules() []Rule {
	return append([]Rule(nil), rules...)
}

// Lookup returns the rule for relation.
func Lookup(relation string) (Rule, bool) {
	for _, rule := range rules {
		if rule.Relation == relation {
			return rule, true
		}
	}
	return Rule{}, false
}

// ReasonViolation returns the stable policy violation for a supplied reason,
// or an empty string when the reason is valid for the relationship.
func ReasonViolation(relation, reason string) string {
	if reason == "" {
		return ""
	}
	rule, ok := Lookup(relation)
	if !ok || rule.ReasonPolicy != ReasonOptional {
		return ReasonViolationForbidden
	}
	if !utf8.ValidString(reason) {
		return ReasonViolationInvalidUTF8
	}
	if len([]byte(reason)) > MaxReasonBytes {
		return ReasonViolationTooLong
	}
	if strings.TrimSpace(reason) == "" {
		return ReasonViolationInvalid
	}
	if normalizeReasonToken(reason) == relation {
		return ReasonViolationRedundant
	}
	return ""
}

func normalizeReasonToken(reason string) string {
	return strings.Join(strings.Fields(strings.ToLower(reason)), "_")
}

// Cases expands the complete ordered 11-by-7-by-7 matrix.
func Cases() []Case {
	cases := make([]Case, 0, len(rules)*len(recordTypes)*len(recordTypes))
	for _, rule := range rules {
		for _, fromType := range recordTypes {
			for _, toType := range recordTypes {
				cases = append(cases, Case{
					Relation:       rule.Relation,
					From:           fromType,
					To:             toType,
					Allowed:        rule.Allows(fromType, toType),
					ReasonPolicy:   rule.ReasonPolicy,
					SelfLinkPolicy: rule.SelfLinkPolicy,
					CyclePolicy:    rule.CyclePolicy,
				})
			}
		}
	}
	return cases
}
