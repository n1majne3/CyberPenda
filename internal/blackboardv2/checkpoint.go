package blackboardv2

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
)

// CheckpointAttemptRequest is the closed Runtime request for versioning one
// owned open Attempt summary.
type CheckpointAttemptRequest struct {
	IdempotencyKey string `json:"idempotency_key"`
	Key            string `json:"key"`
	Version        int    `json:"version"`
	Summary        string `json:"summary"`
}

// UnmarshalJSON keeps trusted authority, raw output, and provenance outside
// the checkpoint contract.
func (request *CheckpointAttemptRequest) UnmarshalJSON(raw []byte) error {
	var fields map[string]json.RawMessage
	if err := decodeJSON(raw, &fields); err != nil {
		return err
	}
	for field := range fields {
		switch field {
		case "idempotency_key", "key", "version", "summary":
		default:
			return fmt.Errorf("unknown CheckpointAttemptRequest field %q", field)
		}
	}
	idempotencyKey, err := decodeRequiredString(fields, "idempotency_key")
	if err != nil {
		return err
	}
	key, err := decodeRequiredString(fields, "key")
	if err != nil {
		return err
	}
	versionRaw, ok := fields["version"]
	if !ok {
		return fmt.Errorf("version is required")
	}
	if bytes.Equal(bytes.TrimSpace(versionRaw), []byte("null")) {
		return fmt.Errorf("version must be a positive integer")
	}
	var version int
	if err := decodeJSON(versionRaw, &version); err != nil {
		return fmt.Errorf("decode version: %w", err)
	}
	if version < 1 {
		return fmt.Errorf("version must be a positive integer")
	}
	summary, err := decodeRequiredString(fields, "summary")
	if err != nil {
		return err
	}
	*request = CheckpointAttemptRequest{
		IdempotencyKey: idempotencyKey,
		Key:            key,
		Version:        version,
		Summary:        summary,
	}
	return nil
}

// CheckpointAttemptForContinuation versions the compact summary of one owned
// open Attempt. The generated semantic update shares the normal atomic
// history, idempotency, and Working Snapshot transaction.
func (s *Service) CheckpointAttemptForContinuation(ctx context.Context, projectID, continuationID string, request CheckpointAttemptRequest) (ChangeResult, error) {
	if continuationID == "" {
		return ChangeResult{}, semanticError("authority_denied", "trusted Continuation identity is required", "", nil)
	}
	if request.IdempotencyKey == "" {
		return ChangeResult{}, semanticError("semantic_validation", "idempotency_key is required", "idempotency_key", nil)
	}
	if err := validateKey(request.Key, "key"); err != nil {
		return ChangeResult{}, err
	}
	if request.Version < 1 {
		return ChangeResult{}, semanticError("semantic_validation", "version must be positive", "version", nil)
	}
	if err := validateSemanticText(request.Summary, "summary"); err != nil {
		return ChangeResult{}, err
	}
	return s.apply(ctx, projectID, continuationID, ChangeBatch{
		Schema:         changeBatchSchema,
		IdempotencyKey: request.IdempotencyKey,
		Changes: []Change{{
			Op:      "update",
			Key:     request.Key,
			Version: request.Version,
			Type:    "attempt",
			Record:  AttemptPatch{Summary: &request.Summary},
		}},
	})
}
