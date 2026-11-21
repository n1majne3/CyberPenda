package blackboardv2

import (
	"encoding/json"
)

func completeEntityRecord(value any, path string) (EntityRecord, error) {
	switch record := value.(type) {
	case EntityRecord:
		return record, nil
	case *EntityRecord:
		if record == nil {
			return EntityRecord{}, semanticError("semantic_validation", "Entity record is required", path, nil)
		}
		return *record, nil
	case json.RawMessage:
		decoded, err := decodeEntityRecord(record)
		if err != nil {
			return EntityRecord{}, semanticError("semantic_validation", err.Error(), path, nil)
		}
		return decoded, nil
	default:
		return EntityRecord{}, semanticError("semantic_validation", "Entity create requires a complete Entity record", path, nil)
	}
}

func partialEntityRecord(value any, path string) (EntityPatch, error) {
	switch patch := value.(type) {
	case EntityPatch:
		if entityPatchEmpty(patch) {
			return EntityPatch{}, semanticError("semantic_validation", "Entity partial record requires at least one property", path, nil)
		}
		return patch, nil
	case *EntityPatch:
		if patch == nil {
			return EntityPatch{}, semanticError("semantic_validation", "Entity update requires an Entity partial record", path, nil)
		}
		if entityPatchEmpty(*patch) {
			return EntityPatch{}, semanticError("semantic_validation", "Entity partial record requires at least one property", path, nil)
		}
		return *patch, nil
	case json.RawMessage:
		decoded, err := decodeEntityPatch(patch)
		if err != nil {
			return EntityPatch{}, semanticError("semantic_validation", err.Error(), path, nil)
		}
		return decoded, nil
	default:
		return EntityPatch{}, semanticError("semantic_validation", "Entity update requires an Entity partial record", path, nil)
	}
}

func completeObjectiveRecord(value any, path string) (ObjectiveRecord, error) {
	switch record := value.(type) {
	case ObjectiveRecord:
		return record, nil
	case *ObjectiveRecord:
		if record == nil {
			return ObjectiveRecord{}, semanticError("semantic_validation", "Objective record is required", path, nil)
		}
		return *record, nil
	case json.RawMessage:
		decoded, err := decodeObjectiveRecord(record)
		if err != nil {
			return ObjectiveRecord{}, semanticError("semantic_validation", err.Error(), path, nil)
		}
		return decoded, nil
	default:
		return ObjectiveRecord{}, semanticError("semantic_validation", "Objective create requires a complete Objective record", path, nil)
	}
}

func partialObjectiveRecord(value any, path string) (ObjectivePatch, error) {
	switch patch := value.(type) {
	case ObjectivePatch:
		if patch.Objective == nil {
			return ObjectivePatch{}, semanticError("semantic_validation", "Objective partial record requires at least one property", path, nil)
		}
		return patch, nil
	case *ObjectivePatch:
		if patch == nil {
			return ObjectivePatch{}, semanticError("semantic_validation", "Objective update requires an Objective partial record", path, nil)
		}
		if patch.Objective == nil {
			return ObjectivePatch{}, semanticError("semantic_validation", "Objective partial record requires at least one property", path, nil)
		}
		return *patch, nil
	case json.RawMessage:
		decoded, err := decodeObjectivePatch(patch)
		if err != nil {
			return ObjectivePatch{}, semanticError("semantic_validation", err.Error(), path, nil)
		}
		return decoded, nil
	default:
		return ObjectivePatch{}, semanticError("semantic_validation", "Objective update requires an Objective partial record", path, nil)
	}
}

func completeAttemptRecord(value any, path string) (AttemptRecord, error) {
	switch record := value.(type) {
	case AttemptRecord:
		return record, nil
	case *AttemptRecord:
		if record == nil {
			return AttemptRecord{}, semanticError("semantic_validation", "Attempt record is required", path, nil)
		}
		return *record, nil
	case json.RawMessage:
		decoded, err := decodeAttemptRecord(record)
		if err != nil {
			return AttemptRecord{}, semanticError("semantic_validation", err.Error(), path, nil)
		}
		return decoded, nil
	default:
		return AttemptRecord{}, semanticError("semantic_validation", "Attempt create requires a complete Attempt record", path, nil)
	}
}

func partialAttemptRecord(value any, path string) (AttemptPatch, error) {
	switch patch := value.(type) {
	case AttemptPatch:
		if patch.Summary == nil {
			return AttemptPatch{}, semanticError("semantic_validation", "Attempt partial record requires at least one property", path, nil)
		}
		return patch, nil
	case *AttemptPatch:
		if patch == nil {
			return AttemptPatch{}, semanticError("semantic_validation", "Attempt update requires an Attempt partial record", path, nil)
		}
		if patch.Summary == nil {
			return AttemptPatch{}, semanticError("semantic_validation", "Attempt partial record requires at least one property", path, nil)
		}
		return *patch, nil
	case json.RawMessage:
		decoded, err := decodeAttemptPatch(patch)
		if err != nil {
			return AttemptPatch{}, semanticError("semantic_validation", err.Error(), path, nil)
		}
		return decoded, nil
	default:
		return AttemptPatch{}, semanticError("semantic_validation", "Attempt update requires an Attempt partial record", path, nil)
	}
}

func completeFactRecord(value any, path string) (FactRecord, error) {
	switch record := value.(type) {
	case FactRecord:
		return record, nil
	case *FactRecord:
		if record == nil {
			return FactRecord{}, semanticError("semantic_validation", "Fact record is required", path, nil)
		}
		return *record, nil
	case json.RawMessage:
		decoded, err := decodeFactRecord(record)
		if err != nil {
			return FactRecord{}, semanticError("semantic_validation", err.Error(), path, nil)
		}
		return decoded, nil
	default:
		return FactRecord{}, semanticError("semantic_validation", "Fact create requires a complete Fact record", path, nil)
	}
}

func partialFactRecord(value any, path string) (FactPatch, error) {
	switch patch := value.(type) {
	case FactPatch:
		if patch.Confidence != nil {
			return FactPatch{}, semanticError("semantic_validation", "Fact confidence changes require transition", path+".confidence", nil)
		}
		if factPatchEmpty(patch) {
			return FactPatch{}, semanticError("semantic_validation", "Fact partial record requires at least one property", path, nil)
		}
		return patch, nil
	case *FactPatch:
		if patch == nil {
			return FactPatch{}, semanticError("semantic_validation", "Fact update requires a Fact partial record", path, nil)
		}
		if patch.Confidence != nil {
			return FactPatch{}, semanticError("semantic_validation", "Fact confidence changes require transition", path+".confidence", nil)
		}
		if factPatchEmpty(*patch) {
			return FactPatch{}, semanticError("semantic_validation", "Fact partial record requires at least one property", path, nil)
		}
		return *patch, nil
	case json.RawMessage:
		decoded, err := decodeFactPatch(patch)
		if err != nil {
			return FactPatch{}, semanticError("semantic_validation", err.Error(), path, nil)
		}
		return decoded, nil
	default:
		return FactPatch{}, semanticError("semantic_validation", "Fact update requires a Fact partial record", path, nil)
	}
}

func completeFindingRecord(value any, path string) (FindingRecord, error) {
	switch record := value.(type) {
	case FindingRecord:
		return record, nil
	case *FindingRecord:
		if record == nil {
			return FindingRecord{}, semanticError("semantic_validation", "Finding record is required", path, nil)
		}
		return *record, nil
	case json.RawMessage:
		decoded, err := decodeFindingRecord(record)
		if err != nil {
			return FindingRecord{}, semanticError("semantic_validation", err.Error(), path, nil)
		}
		return decoded, nil
	default:
		return FindingRecord{}, semanticError("semantic_validation", "Finding create requires a complete closed Finding record", path, nil)
	}
}

func partialFindingRecord(value any, path string) (FindingPatch, error) {
	switch patch := value.(type) {
	case FindingPatch:
		if findingPatchEmpty(patch) {
			return FindingPatch{}, semanticError("semantic_validation", "Finding partial record requires at least one property", path, nil)
		}
		return patch, nil
	case *FindingPatch:
		if patch == nil || findingPatchEmpty(*patch) {
			return FindingPatch{}, semanticError("semantic_validation", "Finding update requires a non-empty Finding partial record", path, nil)
		}
		return *patch, nil
	case json.RawMessage:
		decoded, err := decodeFindingPatch(patch)
		if err != nil {
			return FindingPatch{}, semanticError("semantic_validation", err.Error(), path, nil)
		}
		return decoded, nil
	default:
		return FindingPatch{}, semanticError("semantic_validation", "Finding update requires a closed Finding partial record", path, nil)
	}
}

func partialEvidenceRecord(value any, path string) (EvidencePatch, error) {
	switch patch := value.(type) {
	case EvidencePatch:
		if evidencePatchEmpty(patch) {
			return EvidencePatch{}, semanticError("semantic_validation", "Evidence partial record requires at least one property", path, nil)
		}
		if err := validateEvidencePatch(patch, path); err != nil {
			return EvidencePatch{}, err
		}
		return patch, nil
	case *EvidencePatch:
		if patch == nil || evidencePatchEmpty(*patch) {
			return EvidencePatch{}, semanticError("semantic_validation", "Evidence update requires a non-empty Evidence partial record", path, nil)
		}
		if err := validateEvidencePatch(*patch, path); err != nil {
			return EvidencePatch{}, err
		}
		return *patch, nil
	case json.RawMessage:
		decoded, err := decodeEvidencePatch(patch)
		if err != nil {
			return EvidencePatch{}, semanticError("semantic_validation", err.Error(), path, nil)
		}
		if err := validateEvidencePatch(decoded, path); err != nil {
			return EvidencePatch{}, err
		}
		return decoded, nil
	default:
		return EvidencePatch{}, semanticError("semantic_validation", "Evidence update requires an Evidence partial record", path, nil)
	}
}
