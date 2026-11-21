package blackboardv2

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
)

func rejectUnknownFields(fields map[string]json.RawMessage, allowed map[string]bool) error {
	for field := range fields {
		if !allowed[field] {
			return fmt.Errorf("unknown change field %q", field)
		}
	}
	return nil
}

func decodeCreateChange(fields map[string]json.RawMessage) (Change, error) {
	key, err := decodeRequiredString(fields, "key")
	if err != nil {
		return Change{}, err
	}
	typ, err := decodeRequiredString(fields, "type")
	if err != nil {
		return Change{}, err
	}
	recordRaw, ok := fields["record"]
	if !ok {
		return Change{}, fmt.Errorf("create record is required")
	}
	record, err := decodeCompleteRecord(typ, recordRaw)
	if err != nil {
		return Change{}, err
	}
	return Change{Op: "create", Key: key, Type: typ, Record: record}, nil
}

func decodeUpdateChange(fields map[string]json.RawMessage) (Change, error) {
	key, err := decodeRequiredString(fields, "key")
	if err != nil {
		return Change{}, err
	}
	typ, err := decodeRequiredString(fields, "type")
	if err != nil {
		return Change{}, err
	}
	var version int
	if raw, ok := fields["version"]; !ok {
		return Change{}, fmt.Errorf("update version is required")
	} else if err := decodeJSON(raw, &version); err != nil {
		return Change{}, fmt.Errorf("decode update version: %w", err)
	}
	recordRaw, ok := fields["record"]
	if !ok {
		return Change{}, fmt.Errorf("update record is required")
	}
	record, err := decodePartialRecord(typ, recordRaw)
	if err != nil {
		return Change{}, err
	}
	var clear []string
	if raw, ok := fields["clear"]; ok {
		if err := decodeJSON(raw, &clear); err != nil {
			return Change{}, fmt.Errorf("decode update clear: %w", err)
		}
	}
	return Change{Op: "update", Key: key, Version: version, Type: typ, Record: record, Clear: clear}, nil
}

func decodeRelateChange(fields map[string]json.RawMessage) (Change, error) {
	from, err := decodeRequiredString(fields, "from")
	if err != nil {
		return Change{}, err
	}
	relation, err := decodeRequiredString(fields, "relation")
	if err != nil {
		return Change{}, err
	}
	to, err := decodeRequiredString(fields, "to")
	if err != nil {
		return Change{}, err
	}
	if !isOrdinaryRelation(relation) && !isReasonRelation(relation) {
		return Change{}, fmt.Errorf("unsupported relationship type %q", relation)
	}
	if isOrdinaryRelation(relation) {
		if _, ok := fields["version"]; ok {
			return Change{}, fmt.Errorf("relationship version is forbidden for %s", relation)
		}
		if _, ok := fields["reason"]; ok {
			return Change{}, fmt.Errorf("relationship reason is forbidden for %s", relation)
		}
		return Change{Op: "relate", From: from, Relation: relation, To: to}, nil
	}
	_, hasVersion := fields["version"]
	_, hasReason := fields["reason"]
	if hasVersion && !hasReason {
		return Change{}, fmt.Errorf("relationship reason is required when version is provided")
	}
	var version int
	if raw, ok := fields["version"]; ok {
		if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
			return Change{}, fmt.Errorf("relationship version must be a positive integer")
		}
		if err := decodeJSON(raw, &version); err != nil {
			return Change{}, fmt.Errorf("decode relation version: %w", err)
		}
		if version < 1 {
			return Change{}, fmt.Errorf("relationship version must be a positive integer")
		}
	}
	reason := ""
	if hasReason {
		reason, err = decodeRequiredString(fields, "reason")
		if err != nil {
			return Change{}, err
		}
		if reason == "" {
			return Change{}, fmt.Errorf("relationship reason must not be empty")
		}
	}
	return Change{Op: "relate", From: from, Relation: relation, To: to, Version: version, Reason: reason}, nil
}

func decodeUnrelateChange(fields map[string]json.RawMessage) (Change, error) {
	from, err := decodeRequiredString(fields, "from")
	if err != nil {
		return Change{}, err
	}
	relation, err := decodeRequiredString(fields, "relation")
	if err != nil {
		return Change{}, err
	}
	to, err := decodeRequiredString(fields, "to")
	if err != nil {
		return Change{}, err
	}
	if !isOrdinaryRelation(relation) && !isReasonRelation(relation) {
		return Change{}, fmt.Errorf("unsupported mutable relationship type %q", relation)
	}
	var version int
	if raw, ok := fields["version"]; !ok {
		return Change{}, fmt.Errorf("unrelate version is required")
	} else if err := decodeJSON(raw, &version); err != nil {
		return Change{}, fmt.Errorf("decode unrelate version: %w", err)
	}
	if version < 1 {
		return Change{}, fmt.Errorf("unrelate version must be a positive integer")
	}
	return Change{Op: "unrelate", From: from, Relation: relation, To: to, Version: version}, nil
}

func decodeTransitionChange(fields map[string]json.RawMessage) (Change, error) {
	key, err := decodeRequiredString(fields, "key")
	if err != nil {
		return Change{}, err
	}
	var version int
	if raw, ok := fields["version"]; !ok {
		return Change{}, fmt.Errorf("transition version is required")
	} else if err := decodeJSON(raw, &version); err != nil {
		return Change{}, fmt.Errorf("decode transition version: %w", err)
	}
	status, err := decodeRequiredString(fields, "status")
	if err != nil {
		return Change{}, err
	}
	change := Change{Op: "transition", Key: key, Version: version, Status: status}
	switch status {
	case "verified", "rejected":
		if _, ok := fields["summary"]; ok {
			return Change{}, fmt.Errorf("transition summary is not allowed for status %s", status)
		}
		if _, ok := fields["resolution_summary"]; ok {
			return Change{}, fmt.Errorf("transition resolution_summary is not allowed for status %s", status)
		}
		verificationSummary, err := decodeRequiredString(fields, "verification_summary")
		if err != nil {
			return Change{}, err
		}
		change.VerificationSummary = verificationSummary
	case "resolved", "abandoned", "retired", "false_positive":
		if _, ok := fields["summary"]; ok {
			return Change{}, fmt.Errorf("transition summary is not allowed for status %s", status)
		}
		if _, ok := fields["verification_summary"]; ok {
			return Change{}, fmt.Errorf("transition verification_summary is not allowed for status %s", status)
		}
		resolutionSummary, err := decodeRequiredString(fields, "resolution_summary")
		if err != nil {
			return Change{}, err
		}
		change.ResolutionSummary = resolutionSummary
	case "succeeded", "failed", "blocked", "inconclusive", "interrupted", "deprecated", "missing":
		if _, ok := fields["resolution_summary"]; ok {
			return Change{}, fmt.Errorf("transition resolution_summary is not allowed for status %s", status)
		}
		if _, ok := fields["verification_summary"]; ok {
			return Change{}, fmt.Errorf("transition verification_summary is not allowed for status %s", status)
		}
		summary, err := decodeRequiredString(fields, "summary")
		if err != nil {
			return Change{}, err
		}
		change.Summary = summary
	default:
		if _, ok := fields["summary"]; ok {
			return Change{}, fmt.Errorf("transition summary is not allowed for status %s", status)
		}
		if _, ok := fields["resolution_summary"]; ok {
			return Change{}, fmt.Errorf("transition resolution_summary is not allowed for status %s", status)
		}
		if _, ok := fields["verification_summary"]; ok {
			return Change{}, fmt.Errorf("transition verification_summary is not allowed for status %s", status)
		}
	}
	return change, nil
}

func decodeSupersedeChange(fields map[string]json.RawMessage) (Change, error) {
	replacement, err := decodeRequiredString(fields, "replacement")
	if err != nil {
		return Change{}, err
	}
	replaced, err := decodeRequiredString(fields, "replaced")
	if err != nil {
		return Change{}, err
	}
	var replacementVersion int
	if raw, ok := fields["replacement_version"]; ok {
		if err := decodeJSON(raw, &replacementVersion); err != nil {
			return Change{}, fmt.Errorf("decode supersede replacement_version: %w", err)
		}
	}
	var replacedVersion int
	if raw, ok := fields["replaced_version"]; !ok {
		return Change{}, fmt.Errorf("supersede replaced_version is required")
	} else if err := decodeJSON(raw, &replacedVersion); err != nil {
		return Change{}, fmt.Errorf("decode supersede replaced_version: %w", err)
	}
	return Change{Op: "supersede", Replacement: replacement, ReplacementVersion: replacementVersion, Replaced: replaced, ReplacedVersion: replacedVersion}, nil
}

func decodeMergeChange(fields map[string]json.RawMessage) (Change, error) {
	source, err := decodeRequiredString(fields, "source")
	if err != nil {
		return Change{}, err
	}
	canonical, err := decodeRequiredString(fields, "canonical")
	if err != nil {
		return Change{}, err
	}
	var sourceVersion, canonicalVersion int
	if raw, ok := fields["source_version"]; !ok {
		return Change{}, fmt.Errorf("merge source_version is required")
	} else if err := decodeJSON(raw, &sourceVersion); err != nil {
		return Change{}, fmt.Errorf("decode merge source_version: %w", err)
	}
	if raw, ok := fields["canonical_version"]; !ok {
		return Change{}, fmt.Errorf("merge canonical_version is required")
	} else if err := decodeJSON(raw, &canonicalVersion); err != nil {
		return Change{}, fmt.Errorf("decode merge canonical_version: %w", err)
	}
	var canonicalRecord any
	if raw, ok := fields["canonical_record"]; ok {
		canonicalRecord = json.RawMessage(append([]byte(nil), raw...))
	}
	var clear []string
	if raw, ok := fields["clear"]; ok {
		if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
			return Change{}, fmt.Errorf("merge clear must be an array")
		}
		if err := decodeJSON(raw, &clear); err != nil {
			return Change{}, fmt.Errorf("decode merge clear: %w", err)
		}
		seen := make(map[string]bool, len(clear))
		for _, field := range clear {
			if seen[field] {
				return Change{}, fmt.Errorf("merge clear fields must be unique")
			}
			seen[field] = true
		}
	}
	return Change{Op: "merge", Source: source, SourceVersion: sourceVersion, Canonical: canonical, CanonicalVersion: canonicalVersion, CanonicalRecord: canonicalRecord, Clear: clear}, nil
}

func decodeRequiredString(fields map[string]json.RawMessage, field string) (string, error) {
	raw, ok := fields[field]
	if !ok {
		return "", fmt.Errorf("%s is required", field)
	}
	var value string
	if err := decodeJSON(raw, &value); err != nil {
		return "", fmt.Errorf("decode %s: %w", field, err)
	}
	return value, nil
}

func decodeCompleteRecord(typ string, raw json.RawMessage) (any, error) {
	switch typ {
	case "entity":
		return decodeEntityRecord(raw)
	case "objective":
		return decodeObjectiveRecord(raw)
	case "attempt":
		return decodeAttemptRecord(raw)
	case "fact":
		return decodeFactRecord(raw)
	case "finding":
		return decodeFindingRecord(raw)
	case "solution":
		return decodeSolutionRecord(raw)
	default:
		return nil, fmt.Errorf("unsupported Blackboard v2 record type %q in this slice", typ)
	}
}

func decodePartialRecord(typ string, raw json.RawMessage) (any, error) {
	switch typ {
	case "entity":
		return decodeEntityPatch(raw)
	case "objective":
		return decodeObjectivePatch(raw)
	case "attempt":
		return decodeAttemptPatch(raw)
	case "fact":
		return decodeFactPatch(raw)
	case "finding":
		return decodeFindingPatch(raw)
	case "solution":
		return decodeSolutionPatch(raw)
	case "evidence":
		return decodeEvidencePatch(raw)
	default:
		return nil, fmt.Errorf("unsupported Blackboard v2 record type %q in this slice", typ)
	}
}

func decodeEntityRecord(raw json.RawMessage) (EntityRecord, error) {
	var record EntityRecord
	if err := strictDecodeJSON(raw, &record); err != nil {
		return EntityRecord{}, fmt.Errorf("decode Entity record: %w", err)
	}
	return record, nil
}

func decodeEntityPatch(raw json.RawMessage) (EntityPatch, error) {
	var patch EntityPatch
	if err := strictDecodeJSON(raw, &patch); err != nil {
		return EntityPatch{}, fmt.Errorf("decode Entity patch: %w", err)
	}
	if entityPatchEmpty(patch) {
		return EntityPatch{}, fmt.Errorf("Entity partial record requires at least one property")
	}
	return patch, nil
}

func decodeObjectiveRecord(raw json.RawMessage) (ObjectiveRecord, error) {
	var record ObjectiveRecord
	if err := strictDecodeJSON(raw, &record); err != nil {
		return ObjectiveRecord{}, fmt.Errorf("decode Objective record: %w", err)
	}
	if jsonFieldPresent(raw, "resolution_summary") {
		return ObjectiveRecord{}, fmt.Errorf("Objective create does not accept resolution_summary")
	}
	return record, nil
}

func decodeObjectivePatch(raw json.RawMessage) (ObjectivePatch, error) {
	var patch ObjectivePatch
	if err := strictDecodeJSON(raw, &patch); err != nil {
		return ObjectivePatch{}, fmt.Errorf("decode Objective patch: %w", err)
	}
	if patch.Objective == nil {
		return ObjectivePatch{}, fmt.Errorf("Objective partial record requires at least one property")
	}
	return patch, nil
}

func decodeAttemptRecord(raw json.RawMessage) (AttemptRecord, error) {
	var record AttemptRecord
	if err := strictDecodeJSON(raw, &record); err != nil {
		return AttemptRecord{}, fmt.Errorf("decode Attempt record: %w", err)
	}
	return record, nil
}

func decodeAttemptPatch(raw json.RawMessage) (AttemptPatch, error) {
	var patch AttemptPatch
	if err := strictDecodeJSON(raw, &patch); err != nil {
		return AttemptPatch{}, fmt.Errorf("decode Attempt patch: %w", err)
	}
	if patch.Summary == nil {
		return AttemptPatch{}, fmt.Errorf("Attempt partial record requires at least one property")
	}
	return patch, nil
}

func jsonFieldPresent(raw json.RawMessage, name string) bool {
	var fields map[string]json.RawMessage
	if err := decodeJSON(raw, &fields); err != nil {
		return false
	}
	_, ok := fields[name]
	return ok
}

func decodeFactRecord(raw json.RawMessage) (FactRecord, error) {
	var record FactRecord
	if err := strictDecodeJSON(raw, &record); err != nil {
		return FactRecord{}, fmt.Errorf("decode Fact record: %w", err)
	}
	return record, nil
}

func decodeFactPatch(raw json.RawMessage) (FactPatch, error) {
	var patch FactPatch
	if err := strictDecodeJSON(raw, &patch); err != nil {
		return FactPatch{}, fmt.Errorf("decode Fact patch: %w", err)
	}
	if factPatchEmpty(patch) {
		return FactPatch{}, fmt.Errorf("Fact partial record requires at least one property")
	}
	return patch, nil
}

func decodeFindingRecord(raw json.RawMessage) (FindingRecord, error) {
	var record FindingRecord
	if err := strictDecodeJSON(raw, &record); err != nil {
		return FindingRecord{}, fmt.Errorf("decode Finding record: %w", err)
	}
	return record, nil
}

func decodeFindingPatch(raw json.RawMessage) (FindingPatch, error) {
	var patch FindingPatch
	if err := strictDecodeJSON(raw, &patch); err != nil {
		return FindingPatch{}, fmt.Errorf("decode Finding patch: %w", err)
	}
	if findingPatchEmpty(patch) {
		return FindingPatch{}, fmt.Errorf("Finding partial record requires at least one property")
	}
	return patch, nil
}

func decodeEvidencePatch(raw json.RawMessage) (EvidencePatch, error) {
	var patch EvidencePatch
	if err := strictDecodeJSON(raw, &patch); err != nil {
		return EvidencePatch{}, fmt.Errorf("decode Evidence patch: %w", err)
	}
	if evidencePatchEmpty(patch) {
		return EvidencePatch{}, fmt.Errorf("Evidence partial record requires at least one property")
	}
	return patch, nil
}

func strictDecodeJSON(raw []byte, target any) error {
	if err := requireValidJSONUTF8(raw); err != nil {
		return err
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return fmt.Errorf("unexpected trailing JSON")
	}
	return nil
}
