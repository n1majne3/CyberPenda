package blackboardv2

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
)

// EvidenceConfig supplies the managed and Runtime filesystem roots used by
// RetainEvidenceForContinuation.
type EvidenceConfig struct {
	ArtifactRoot string
	RuntimeRoot  string
	Failures     EvidenceFailureInjector
}

// EvidenceFailurePoint names a durable retention boundary used to verify
// uncertain-retry recovery.
type EvidenceFailurePoint string

const (
	EvidenceFailureBeforeReservation      EvidenceFailurePoint = "before_reservation"
	EvidenceFailureBeforeFilePublish      EvidenceFailurePoint = "before_file_publish"
	EvidenceFailureAfterDirectoryCreate   EvidenceFailurePoint = "after_managed_directory_create"
	EvidenceFailureAfterDirectorySync     EvidenceFailurePoint = "after_managed_directory_parent_sync"
	EvidenceFailureBeforeTempCopy         EvidenceFailurePoint = "before_temp_copy"
	EvidenceFailureMidTempCopy            EvidenceFailurePoint = "mid_temp_copy"
	EvidenceFailureAfterTempDirectorySync EvidenceFailurePoint = "after_temp_directory_sync"
	EvidenceFailureAfterTempSync          EvidenceFailurePoint = "after_temp_sync"
	EvidenceFailureBeforeFileRename       EvidenceFailurePoint = "before_file_rename"
	EvidenceFailureAfterFileRename        EvidenceFailurePoint = "after_file_rename"
	EvidenceFailureBeforePublishStore     EvidenceFailurePoint = "before_publication_checkpoint"
	EvidenceFailureAfterFilePublish       EvidenceFailurePoint = "file_publish"
	EvidenceFailureAfterPayloadGCClaim    EvidenceFailurePoint = "after_payload_gc_claim"
	EvidenceFailureAfterPayloadUnlink     EvidenceFailurePoint = "after_payload_unlink"
	EvidenceFailureAfterGraphCommit       EvidenceFailurePoint = "semantic_commit"
	EvidenceFailureAfterResultStore       EvidenceFailurePoint = "result_store"
)

// EvidenceFailureInjector can simulate a lost response or process failure at
// a durable retention boundary.
type EvidenceFailureInjector interface {
	FailAfter(EvidenceFailurePoint) error
}

// EvidenceLink is a closed [relation,target_key] pair.
type EvidenceLink [2]string

// UnmarshalJSON rejects non-pair and non-string Evidence links.
func (link *EvidenceLink) UnmarshalJSON(raw []byte) error {
	var values []string
	if err := decodeJSON(raw, &values); err != nil {
		return fmt.Errorf("decode Evidence link: %w", err)
	}
	if len(values) != 2 {
		return fmt.Errorf("Evidence link must contain relation and target key")
	}
	*link = EvidenceLink{values[0], values[1]}
	return nil
}

// RetainEvidenceRequest is the closed runtime Evidence request from the v2
// contract. Project, origin, produced, and integrity values are server-bound.
type RetainEvidenceRequest struct {
	IdempotencyKey string         `json:"idempotency_key"`
	Key            string         `json:"key"`
	Version        int            `json:"version,omitempty"`
	Attempt        string         `json:"attempt"`
	SourcePath     string         `json:"source_path"`
	ArtifactType   string         `json:"artifact_type"`
	Summary        string         `json:"summary"`
	MediaType      string         `json:"media_type,omitempty"`
	CapturedAt     string         `json:"captured_at,omitempty"`
	Links          []EvidenceLink `json:"links,omitempty"`
}

// UnmarshalJSON enforces the frozen closed request before adapters can discard
// unknown fields.
func (request *RetainEvidenceRequest) UnmarshalJSON(raw []byte) error {
	var fields map[string]json.RawMessage
	if err := decodeJSON(raw, &fields); err != nil {
		return err
	}
	allowed := map[string]bool{
		"idempotency_key": true, "key": true, "version": true, "attempt": true,
		"source_path": true, "artifact_type": true, "summary": true,
		"media_type": true, "captured_at": true, "links": true,
	}
	if err := rejectUnknownFields(fields, allowed); err != nil {
		return err
	}
	result := RetainEvidenceRequest{}
	var err error
	if result.IdempotencyKey, err = decodeRequiredString(fields, "idempotency_key"); err != nil {
		return err
	}
	if result.Key, err = decodeRequiredString(fields, "key"); err != nil {
		return err
	}
	if result.Attempt, err = decodeRequiredString(fields, "attempt"); err != nil {
		return err
	}
	if result.SourcePath, err = decodeRequiredString(fields, "source_path"); err != nil {
		return err
	}
	if result.ArtifactType, err = decodeRequiredString(fields, "artifact_type"); err != nil {
		return err
	}
	if result.Summary, err = decodeRequiredString(fields, "summary"); err != nil {
		return err
	}
	for field, destination := range map[string]*string{"media_type": &result.MediaType, "captured_at": &result.CapturedAt} {
		if value, ok := fields[field]; ok {
			if bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
				return fmt.Errorf("%s must be a string", field)
			}
			if err := decodeJSON(value, destination); err != nil {
				return fmt.Errorf("decode %s: %w", field, err)
			}
			if *destination == "" {
				return fmt.Errorf("%s must not be empty", field)
			}
		}
	}
	if value, ok := fields["version"]; ok {
		if bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
			return fmt.Errorf("version must be a positive integer")
		}
		if err := decodeJSON(value, &result.Version); err != nil {
			return fmt.Errorf("decode version: %w", err)
		}
		if result.Version < 1 {
			return fmt.Errorf("version must be a positive integer")
		}
	}
	if value, ok := fields["links"]; ok {
		if bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
			return fmt.Errorf("links must be an array")
		}
		if err := decodeJSON(value, &result.Links); err != nil {
			return fmt.Errorf("decode links: %w", err)
		}
	}
	*request = result
	return nil
}

type evidenceSource struct {
	root         *os.Root
	file         *os.File
	relativePath string
	identity     string
	sha256       string
	size         int64
	info         os.FileInfo
}

type evidenceRequestRow struct {
	requestHash    string
	sourceIdentity string
	sha256         string
	size           int64
	internalPath   string
	tempPath       string
	previousTemp   string
	publisherToken string
	publisherID    string
	payloadOwned   bool
	status         string
	resultJSON     string
}

type retainedEvidenceMetadata struct {
	sha256 string
	size   int64
}
