package blackboardv2

import (
	"encoding/json"
)

type EntityRecord struct {
	Status        string `json:"status"`
	Kind          string `json:"kind"`
	Name          string `json:"name"`
	Locator       string `json:"locator,omitempty"`
	Description   string `json:"description,omitempty"`
	ScopeStatus   string `json:"scope_status"`
	CredentialRef string `json:"credential_ref,omitempty"`
}

type EntityPatch struct {
	Kind          *string `json:"kind,omitempty"`
	Name          *string `json:"name,omitempty"`
	Locator       *string `json:"locator,omitempty"`
	Description   *string `json:"description,omitempty"`
	ScopeStatus   *string `json:"scope_status,omitempty"`
	CredentialRef *string `json:"credential_ref,omitempty"`
}

type ObjectiveRecord struct {
	Status            string `json:"status"`
	Objective         string `json:"objective"`
	ResolutionSummary string `json:"resolution_summary,omitempty"`
}

type ObjectivePatch struct {
	Objective *string `json:"objective,omitempty"`
}

type AttemptRecord struct {
	Status  string `json:"status"`
	Summary string `json:"summary"`
}

type AttemptPatch struct {
	Summary *string `json:"summary,omitempty"`
}

type FactRecord struct {
	Category    string `json:"category"`
	Summary     string `json:"summary"`
	Body        string `json:"body,omitempty"`
	Confidence  string `json:"confidence"`
	ScopeStatus string `json:"scope_status"`
}

type FactPatch struct {
	Category    *string `json:"category,omitempty"`
	Summary     *string `json:"summary,omitempty"`
	Body        *string `json:"body,omitempty"`
	Confidence  *string `json:"-"`
	ScopeStatus *string `json:"scope_status,omitempty"`
}

// FindingRecord is the complete caller-writable Finding DTO. Severity and
// CVSSPending are deliberately absent because the service derives them.
type FindingRecord struct {
	Status         string `json:"status"`
	Title          string `json:"title"`
	Target         string `json:"target,omitempty"`
	Description    string `json:"description,omitempty"`
	Proof          string `json:"proof,omitempty"`
	Impact         string `json:"impact,omitempty"`
	Recommendation string `json:"recommendation,omitempty"`
	CVSSVersion    string `json:"cvss_version,omitempty"`
	CVSSVector     string `json:"cvss_vector,omitempty"`
}

// FindingPatch is the closed partial update shape for Findings. Lifecycle
// state changes use transition; derived scoring fields are never writable.
type FindingPatch struct {
	Title          *string `json:"title,omitempty"`
	Target         *string `json:"target,omitempty"`
	Description    *string `json:"description,omitempty"`
	Proof          *string `json:"proof,omitempty"`
	Impact         *string `json:"impact,omitempty"`
	Recommendation *string `json:"recommendation,omitempty"`
	CVSSVersion    *string `json:"cvss_version,omitempty"`
	CVSSVector     *string `json:"cvss_vector,omitempty"`
}

type SolutionRecord struct {
	Status              string `json:"status"`
	Kind                string `json:"kind"`
	Summary             string `json:"summary"`
	Value               string `json:"value,omitempty"`
	VerificationSummary string `json:"verification_summary,omitempty"`
}

type SolutionPatch struct {
	Kind                *string `json:"kind,omitempty"`
	Summary             *string `json:"summary,omitempty"`
	Value               *string `json:"value,omitempty"`
	VerificationSummary *string `json:"verification_summary,omitempty"`
}

// EvidenceRecord is the complete semantic Evidence detail DTO. Filesystem and
// integrity fields are server-derived by RetainEvidenceForContinuation.
type EvidenceRecord struct {
	Status       string `json:"status"`
	ArtifactType string `json:"artifact_type"`
	Summary      string `json:"summary"`
	MediaType    string `json:"media_type,omitempty"`
	SourcePath   string `json:"source_path,omitempty"`
	ManagedPath  string `json:"managed_path"`
	SHA256       string `json:"sha256"`
	Size         int64  `json:"size"`
	CapturedAt   string `json:"captured_at,omitempty"`
}

// EvidencePatch is the closed semantic-only update shape for Evidence.
type EvidencePatch struct {
	Summary    *string `json:"summary,omitempty"`
	MediaType  *string `json:"media_type,omitempty"`
	CapturedAt *string `json:"captured_at,omitempty"`
}

// Record is the current-detail union for the implemented Project Knowledge
// records. Empty fields are omitted so each type still serializes to its closed
// contract allowlist.
type Record struct {
	Status              string `json:"status,omitempty"`
	Objective           string `json:"objective,omitempty"`
	ResolutionSummary   string `json:"resolution_summary,omitempty"`
	Kind                string `json:"kind,omitempty"`
	Name                string `json:"name,omitempty"`
	Locator             string `json:"locator,omitempty"`
	Description         string `json:"description,omitempty"`
	ScopeStatus         string `json:"scope_status,omitempty"`
	CredentialRef       string `json:"credential_ref,omitempty"`
	Category            string `json:"category,omitempty"`
	Summary             string `json:"summary,omitempty"`
	Body                string `json:"body,omitempty"`
	Confidence          string `json:"confidence,omitempty"`
	Title               string `json:"title,omitempty"`
	Target              string `json:"target,omitempty"`
	Proof               string `json:"proof,omitempty"`
	Impact              string `json:"impact,omitempty"`
	Recommendation      string `json:"recommendation,omitempty"`
	CVSSVersion         string `json:"cvss_version,omitempty"`
	CVSSVector          string `json:"cvss_vector,omitempty"`
	Severity            string `json:"severity,omitempty"`
	CVSSPending         bool   `json:"cvss_pending,omitempty"`
	Value               string `json:"value,omitempty"`
	VerificationSummary string `json:"verification_summary,omitempty"`
	ArtifactType        string `json:"artifact_type,omitempty"`
	MediaType           string `json:"media_type,omitempty"`
	SourcePath          string `json:"source_path,omitempty"`
	ManagedPath         string `json:"managed_path,omitempty"`
	SHA256              string `json:"sha256,omitempty"`
	Size                int64  `json:"size,omitempty"`
	CapturedAt          string `json:"captured_at,omitempty"`
}

// MarshalJSON keeps Evidence's required zero size while preserving the
// existing closed DTO shape for every other record type.
func (record Record) MarshalJSON() ([]byte, error) {
	if isOneOf(record.Status, "candidate", "verified", "rejected", "superseded") && isOneOf(record.Kind, "answer", "flag", "procedure") {
		return json.Marshal(record.solutionRecord())
	}
	if record.Title != "" && isOneOf(record.Status, "unconfirmed", "confirmed", "false_positive", "superseded") {
		return json.Marshal(record.findingOutputRecord())
	}
	if record.ArtifactType != "" && record.ManagedPath != "" && record.SHA256 != "" {
		return json.Marshal(record.evidenceRecord())
	}
	type recordJSON Record
	return json.Marshal(recordJSON(record))
}

func recordFromEntity(record EntityRecord) Record {
	return Record{
		Status:        record.Status,
		Kind:          record.Kind,
		Name:          record.Name,
		Locator:       record.Locator,
		Description:   record.Description,
		ScopeStatus:   record.ScopeStatus,
		CredentialRef: record.CredentialRef,
	}
}

func recordFromObjective(record ObjectiveRecord) Record {
	return Record{Status: record.Status, Objective: record.Objective, ResolutionSummary: record.ResolutionSummary}
}

func recordFromAttempt(record AttemptRecord) Record {
	return Record{Status: record.Status, Summary: record.Summary}
}

func recordFromFact(record FactRecord) Record {
	return Record{
		Category:    record.Category,
		Summary:     record.Summary,
		Body:        record.Body,
		Confidence:  record.Confidence,
		ScopeStatus: record.ScopeStatus,
	}
}

func recordFromFindingOutput(record findingOutputRecord) Record {
	return Record{
		Status: record.Status, ResolutionSummary: record.ResolutionSummary, Title: record.Title, Target: record.Target, Description: record.Description,
		Proof: record.Proof, Impact: record.Impact, Recommendation: record.Recommendation,
		CVSSVersion: record.CVSSVersion, CVSSVector: record.CVSSVector,
		Severity: record.Severity, CVSSPending: record.CVSSPending,
	}
}

func recordFromEvidence(record EvidenceRecord) Record {
	return Record{
		Status:       record.Status,
		ArtifactType: record.ArtifactType,
		Summary:      record.Summary,
		MediaType:    record.MediaType,
		SourcePath:   record.SourcePath,
		ManagedPath:  record.ManagedPath,
		SHA256:       record.SHA256,
		Size:         record.Size,
		CapturedAt:   record.CapturedAt,
	}
}

func (record Record) entityRecord() EntityRecord {
	return EntityRecord{
		Status:        record.Status,
		Kind:          record.Kind,
		Name:          record.Name,
		Locator:       record.Locator,
		Description:   record.Description,
		ScopeStatus:   record.ScopeStatus,
		CredentialRef: record.CredentialRef,
	}
}

func (record Record) objectiveRecord() ObjectiveRecord {
	return ObjectiveRecord{Status: record.Status, Objective: record.Objective, ResolutionSummary: record.ResolutionSummary}
}

func (record Record) attemptRecord() AttemptRecord {
	return AttemptRecord{Status: record.Status, Summary: record.Summary}
}

func (record Record) factRecord() FactRecord {
	return FactRecord{
		Category:    record.Category,
		Summary:     record.Summary,
		Body:        record.Body,
		Confidence:  record.Confidence,
		ScopeStatus: record.ScopeStatus,
	}
}

func (record Record) findingOutputRecord() findingOutputRecord {
	return findingOutputRecord{
		Status: record.Status, ResolutionSummary: record.ResolutionSummary, Title: record.Title, Target: record.Target, Description: record.Description,
		Proof: record.Proof, Impact: record.Impact, Recommendation: record.Recommendation,
		CVSSVersion: record.CVSSVersion, CVSSVector: record.CVSSVector,
		Severity: record.Severity, CVSSPending: record.CVSSPending,
	}
}

func (record Record) evidenceRecord() EvidenceRecord {
	return EvidenceRecord{
		Status:       record.Status,
		ArtifactType: record.ArtifactType,
		Summary:      record.Summary,
		MediaType:    record.MediaType,
		SourcePath:   record.SourcePath,
		ManagedPath:  record.ManagedPath,
		SHA256:       record.SHA256,
		Size:         record.Size,
		CapturedAt:   record.CapturedAt,
	}
}
