package blackboardv2

func stringPtr(value string) *string {
	return &value
}

func entityPatchEmpty(patch EntityPatch) bool {
	return patch.Kind == nil && patch.Name == nil && patch.Locator == nil && patch.Description == nil && patch.ScopeStatus == nil && patch.CredentialRef == nil
}

func factPatchEmpty(patch FactPatch) bool {
	return patch.Category == nil && patch.Summary == nil && patch.Body == nil && patch.ScopeStatus == nil
}

func findingPatchEmpty(patch FindingPatch) bool {
	return patch.Title == nil && patch.Target == nil && patch.Description == nil && patch.Proof == nil &&
		patch.Impact == nil && patch.Recommendation == nil && patch.CVSSVersion == nil && patch.CVSSVector == nil
}

func evidencePatchEmpty(patch EvidencePatch) bool {
	return patch.Summary == nil && patch.MediaType == nil && patch.CapturedAt == nil
}

func applyEntityPatch(existing EntityRecord, patch EntityPatch, clear []string, path string) (EntityRecord, error) {
	next := existing
	if patch.Kind != nil {
		next.Kind = *patch.Kind
	}
	if patch.Name != nil {
		next.Name = *patch.Name
	}
	if patch.Locator != nil {
		next.Locator = *patch.Locator
	}
	if patch.Description != nil {
		next.Description = *patch.Description
	}
	if patch.ScopeStatus != nil {
		next.ScopeStatus = *patch.ScopeStatus
	}
	if patch.CredentialRef != nil {
		next.CredentialRef = *patch.CredentialRef
	}
	for _, field := range clear {
		switch field {
		case "locator":
			next.Locator = ""
		case "description":
			next.Description = ""
		case "credential_ref":
			next.CredentialRef = ""
		default:
			return EntityRecord{}, semanticError("semantic_validation", "unsupported Entity clear field", path, map[string]any{"field": field})
		}
	}
	return next, nil
}

func applyFactPatch(existing FactRecord, patch FactPatch, clear []string, path string) (FactRecord, error) {
	next := existing
	if patch.Category != nil {
		next.Category = *patch.Category
	}
	if patch.Summary != nil {
		next.Summary = *patch.Summary
	}
	if patch.Body != nil {
		next.Body = *patch.Body
	}
	if patch.ScopeStatus != nil {
		next.ScopeStatus = *patch.ScopeStatus
	}
	for _, field := range clear {
		switch field {
		case "body":
			next.Body = ""
		default:
			return FactRecord{}, semanticError("semantic_validation", "unsupported Fact clear field", path, map[string]any{"field": field})
		}
	}
	return next, nil
}

func factsEqual(a, b FactRecord) bool {
	return a.Category == b.Category &&
		a.Summary == b.Summary &&
		a.Body == b.Body &&
		a.Confidence == b.Confidence &&
		a.ScopeStatus == b.ScopeStatus
}

func evidenceEqual(a, b EvidenceRecord) bool {
	return a.Status == b.Status && a.ArtifactType == b.ArtifactType && a.Summary == b.Summary &&
		a.MediaType == b.MediaType && a.SourcePath == b.SourcePath && a.ManagedPath == b.ManagedPath &&
		a.SHA256 == b.SHA256 && a.Size == b.Size && a.CapturedAt == b.CapturedAt
}

func objectivesEqual(a, b ObjectiveRecord) bool {
	return a.Status == b.Status && a.Objective == b.Objective && a.ResolutionSummary == b.ResolutionSummary
}

func attemptsEqual(a, b AttemptRecord) bool {
	return a.Status == b.Status && a.Summary == b.Summary
}

func entitiesEqual(a, b EntityRecord) bool {
	return a.Status == b.Status &&
		a.Kind == b.Kind &&
		a.Name == b.Name &&
		a.Locator == b.Locator &&
		a.Description == b.Description &&
		a.ScopeStatus == b.ScopeStatus &&
		a.CredentialRef == b.CredentialRef
}
