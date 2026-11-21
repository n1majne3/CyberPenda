package blackboardv2

import (
	"fmt"
)

func validateChangeShape(change Change, index int) error {
	allowedFields := map[string]bool{}
	switch change.Op {
	case "create":
		allowedFields = map[string]bool{"key": true, "type": true, "record": true}
	case "update":
		allowedFields = map[string]bool{"key": true, "version": true, "type": true, "record": true, "clear": true}
	case "relate":
		allowedFields = map[string]bool{"version": true, "from": true, "relation": true, "to": true, "reason": true}
	case "unrelate":
		allowedFields = map[string]bool{"version": true, "from": true, "relation": true, "to": true}
	case "transition":
		allowedFields = map[string]bool{"key": true, "version": true, "status": true, "summary": true, "resolution_summary": true, "verification_summary": true}
	case "supersede":
		allowedFields = map[string]bool{"replacement": true, "replacement_version": true, "replaced": true, "replaced_version": true}
	case "merge":
		allowedFields = map[string]bool{"source": true, "source_version": true, "canonical": true, "canonical_version": true, "canonical_record": true, "clear": true}
	}
	if len(allowedFields) != 0 {
		for _, field := range populatedChangeFields(change) {
			if field.populated && !allowedFields[field.name] {
				return semanticError("semantic_validation", fmt.Sprintf("%s does not accept %s", change.Op, field.name), fmt.Sprintf("changes[%d].%s", index, field.name), nil)
			}
		}
	}
	if change.Op == "relate" {
		path := fmt.Sprintf("changes[%d]", index)
		if isOrdinaryRelation(change.Relation) {
			if change.Version != 0 {
				return semanticError("semantic_validation", "ordinary relationship does not accept version", path+".version", nil)
			}
			if change.Reason != "" {
				return semanticError("semantic_validation", "ordinary relationship does not accept reason", path+".reason", nil)
			}
		} else if isReasonRelation(change.Relation) {
			if change.Version < 0 {
				return semanticError("semantic_validation", "relationship version must be positive when provided", path+".version", nil)
			}
			if change.Version != 0 && change.Reason == "" {
				return semanticError("semantic_validation", "relationship reason is required when version is provided", path+".reason", nil)
			}
		}
	}

	switch change.Op {
	case "create":
		if change.Version != 0 {
			return semanticError("semantic_validation", "create does not accept version", fmt.Sprintf("changes[%d].version", index), nil)
		}
		if len(change.Clear) != 0 {
			return semanticError("semantic_validation", "create does not accept clear", fmt.Sprintf("changes[%d].clear", index), nil)
		}
		if change.Record == nil {
			return semanticError("semantic_validation", "create record is required", fmt.Sprintf("changes[%d].record", index), nil)
		}
	case "update":
		if change.Version < 1 {
			return semanticError("semantic_validation", "update requires the current version", fmt.Sprintf("changes[%d].version", index), nil)
		}
		if change.Record == nil {
			return semanticError("semantic_validation", "update record is required", fmt.Sprintf("changes[%d].record", index), nil)
		}
	case "relate":
		if change.From == "" {
			return semanticError("semantic_validation", "relate from is required", fmt.Sprintf("changes[%d].from", index), nil)
		}
		if change.Relation == "" {
			return semanticError("semantic_validation", "relate relation is required", fmt.Sprintf("changes[%d].relation", index), nil)
		}
		if change.To == "" {
			return semanticError("semantic_validation", "relate to is required", fmt.Sprintf("changes[%d].to", index), nil)
		}
	case "unrelate":
		if change.From == "" {
			return semanticError("semantic_validation", "unrelate from is required", fmt.Sprintf("changes[%d].from", index), nil)
		}
		if change.Relation == "" {
			return semanticError("semantic_validation", "unrelate relation is required", fmt.Sprintf("changes[%d].relation", index), nil)
		}
		if change.To == "" {
			return semanticError("semantic_validation", "unrelate to is required", fmt.Sprintf("changes[%d].to", index), nil)
		}
		if change.Version < 1 {
			return semanticError("semantic_validation", "unrelate requires the current relationship version", fmt.Sprintf("changes[%d].version", index), nil)
		}
	case "transition":
		if change.Key == "" {
			return semanticError("semantic_validation", "transition key is required", fmt.Sprintf("changes[%d].key", index), nil)
		}
		if change.Version < 1 {
			return semanticError("semantic_validation", "transition requires the current version", fmt.Sprintf("changes[%d].version", index), nil)
		}
		if change.Status == "" {
			return semanticError("semantic_validation", "transition status is required", fmt.Sprintf("changes[%d].status", index), nil)
		}
		switch change.Status {
		case "resolved", "abandoned", "retired", "false_positive":
			if change.Summary != "" || change.VerificationSummary != "" {
				return semanticError("semantic_validation", "transition accepts only resolution_summary for this status", fmt.Sprintf("changes[%d].status", index), nil)
			}
		case "succeeded", "failed", "blocked", "inconclusive", "interrupted", "deprecated", "missing":
			if change.ResolutionSummary != "" || change.VerificationSummary != "" {
				return semanticError("semantic_validation", "transition accepts only summary for this status", fmt.Sprintf("changes[%d].status", index), nil)
			}
		case "verified", "rejected":
			if change.Summary != "" || change.ResolutionSummary != "" {
				return semanticError("semantic_validation", "Solution transition accepts only verification_summary", fmt.Sprintf("changes[%d].status", index), nil)
			}
			if change.VerificationSummary == "" {
				return semanticError("semantic_validation", "Solution transition requires verification_summary", fmt.Sprintf("changes[%d].verification_summary", index), nil)
			}
		default:
			if change.Summary != "" || change.ResolutionSummary != "" || change.VerificationSummary != "" {
				return semanticError("semantic_validation", "transition summary field is not allowed for this status", fmt.Sprintf("changes[%d].status", index), nil)
			}
		}
	case "supersede":
		if change.Replacement == "" {
			return semanticError("semantic_validation", "supersede replacement is required", fmt.Sprintf("changes[%d].replacement", index), nil)
		}
		if change.ReplacementVersion < 0 {
			return semanticError("semantic_validation", "supersede replacement_version must be positive when provided", fmt.Sprintf("changes[%d].replacement_version", index), nil)
		}
		if change.Replaced == "" {
			return semanticError("semantic_validation", "supersede replaced is required", fmt.Sprintf("changes[%d].replaced", index), nil)
		}
		if change.ReplacedVersion < 1 {
			return semanticError("semantic_validation", "supersede requires the current replaced version", fmt.Sprintf("changes[%d].replaced_version", index), nil)
		}
	case "merge":
		if change.Source == "" {
			return semanticError("semantic_validation", "merge source is required", fmt.Sprintf("changes[%d].source", index), nil)
		}
		if change.SourceVersion < 1 {
			return semanticError("semantic_validation", "merge requires the current source version", fmt.Sprintf("changes[%d].source_version", index), nil)
		}
		if change.Canonical == "" {
			return semanticError("semantic_validation", "merge canonical is required", fmt.Sprintf("changes[%d].canonical", index), nil)
		}
		if change.CanonicalVersion < 1 {
			return semanticError("semantic_validation", "merge requires the current canonical version", fmt.Sprintf("changes[%d].canonical_version", index), nil)
		}
	}
	return nil
}

func validateChangeDTOShape(change Change, index int) error {
	if err := validateChangeShape(change, index); err != nil {
		return err
	}
	path := fmt.Sprintf("changes[%d].record", index)
	if change.Op == "create" {
		switch change.Type {
		case "entity":
			_, err := completeEntityRecord(change.Record, path)
			return err
		case "objective":
			_, err := completeObjectiveRecord(change.Record, path)
			return err
		case "attempt":
			_, err := completeAttemptRecord(change.Record, path)
			return err
		case "fact":
			_, err := completeFactRecord(change.Record, path)
			return err
		case "finding":
			_, err := completeFindingRecord(change.Record, path)
			return err
		case "solution":
			_, err := completeSolutionRecord(change.Record, path)
			return err
		}
	}
	if change.Op == "update" {
		switch change.Type {
		case "entity":
			_, err := partialEntityRecord(change.Record, path)
			return err
		case "objective":
			_, err := partialObjectiveRecord(change.Record, path)
			return err
		case "attempt":
			_, err := partialAttemptRecord(change.Record, path)
			return err
		case "fact":
			_, err := partialFactRecord(change.Record, path)
			return err
		case "finding":
			_, err := partialFindingRecord(change.Record, path)
			return err
		case "solution":
			_, err := partialSolutionRecord(change.Record, path)
			return err
		case "evidence":
			_, err := partialEvidenceRecord(change.Record, path)
			return err
		}
	}
	return nil
}

type populatedChangeField struct {
	name      string
	populated bool
}

func populatedChangeFields(change Change) []populatedChangeField {
	return []populatedChangeField{
		{name: "key", populated: change.Key != ""},
		{name: "version", populated: change.Version != 0},
		{name: "type", populated: change.Type != ""},
		{name: "record", populated: change.Record != nil},
		{name: "clear", populated: len(change.Clear) != 0},
		{name: "from", populated: change.From != ""},
		{name: "relation", populated: change.Relation != ""},
		{name: "to", populated: change.To != ""},
		{name: "reason", populated: change.Reason != ""},
		{name: "status", populated: change.Status != ""},
		{name: "summary", populated: change.Summary != ""},
		{name: "resolution_summary", populated: change.ResolutionSummary != ""},
		{name: "verification_summary", populated: change.VerificationSummary != ""},
		{name: "replacement", populated: change.Replacement != ""},
		{name: "replacement_version", populated: change.ReplacementVersion != 0},
		{name: "replaced", populated: change.Replaced != ""},
		{name: "replaced_version", populated: change.ReplacedVersion != 0},
		{name: "source", populated: change.Source != ""},
		{name: "source_version", populated: change.SourceVersion != 0},
		{name: "canonical", populated: change.Canonical != ""},
		{name: "canonical_version", populated: change.CanonicalVersion != 0},
		{name: "canonical_record", populated: change.CanonicalRecord != nil},
	}
}
