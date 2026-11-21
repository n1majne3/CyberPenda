package blackboardv2

import (
	"bytes"
	"encoding/json"
	"fmt"
)

type ChangeBatch struct {
	Schema         string   `json:"schema"`
	IdempotencyKey string   `json:"idempotency_key"`
	Changes        []Change `json:"changes"`
}

// UnmarshalJSON enforces the closed semantic-change-batch/v2 envelope before
// adapters can lose required-field presence or the nil/empty changes boundary.
func (batch *ChangeBatch) UnmarshalJSON(raw []byte) error {
	var fields map[string]json.RawMessage
	if err := decodeJSON(raw, &fields); err != nil {
		return err
	}
	for field := range fields {
		switch field {
		case "schema", "idempotency_key", "changes":
		default:
			return fmt.Errorf("unknown ChangeBatch field %q", field)
		}
	}
	schema, err := decodeRequiredString(fields, "schema")
	if err != nil {
		return err
	}
	if schema != changeBatchSchema {
		return fmt.Errorf("schema must be %q", changeBatchSchema)
	}
	idempotencyKey, err := decodeRequiredString(fields, "idempotency_key")
	if err != nil {
		return err
	}
	if idempotencyKey == "" {
		return fmt.Errorf("idempotency_key must not be empty")
	}
	changesRaw, ok := fields["changes"]
	if !ok {
		return fmt.Errorf("changes is required")
	}
	if bytes.Equal(bytes.TrimSpace(changesRaw), []byte("null")) {
		return fmt.Errorf("changes must be an array")
	}
	var changes []Change
	if err := decodeJSON(changesRaw, &changes); err != nil {
		return fmt.Errorf("decode changes: %w", err)
	}
	*batch = ChangeBatch{Schema: schema, IdempotencyKey: idempotencyKey, Changes: changes}
	return nil
}

type Change struct {
	Op                  string   `json:"op"`
	Key                 string   `json:"key,omitempty"`
	Version             int      `json:"version,omitempty"`
	Type                string   `json:"type,omitempty"`
	Record              any      `json:"record,omitempty"`
	Clear               []string `json:"clear,omitempty"`
	From                string   `json:"from,omitempty"`
	Relation            string   `json:"relation,omitempty"`
	To                  string   `json:"to,omitempty"`
	Reason              string   `json:"reason,omitempty"`
	Status              string   `json:"status,omitempty"`
	Summary             string   `json:"summary,omitempty"`
	ResolutionSummary   string   `json:"resolution_summary,omitempty"`
	VerificationSummary string   `json:"verification_summary,omitempty"`
	Replacement         string   `json:"replacement,omitempty"`
	ReplacementVersion  int      `json:"replacement_version,omitempty"`
	Replaced            string   `json:"replaced,omitempty"`
	ReplacedVersion     int      `json:"replaced_version,omitempty"`
	Source              string   `json:"source,omitempty"`
	SourceVersion       int      `json:"source_version,omitempty"`
	Canonical           string   `json:"canonical,omitempty"`
	CanonicalVersion    int      `json:"canonical_version,omitempty"`
	CanonicalRecord     any      `json:"canonical_record,omitempty"`
}

// UnmarshalJSON enforces the closed semantic-change item shapes at the service
// DTO boundary so adapters cannot silently drop unknown fields before Apply.
func (change *Change) UnmarshalJSON(raw []byte) error {
	var fields map[string]json.RawMessage
	if err := decodeJSON(raw, &fields); err != nil {
		return err
	}
	opRaw, ok := fields["op"]
	if !ok {
		return fmt.Errorf("change op is required")
	}
	var op string
	if err := decodeJSON(opRaw, &op); err != nil {
		return fmt.Errorf("decode change op: %w", err)
	}
	switch op {
	case "create":
		if err := rejectUnknownFields(fields, map[string]bool{"op": true, "key": true, "type": true, "record": true}); err != nil {
			return err
		}
		decoded, err := decodeCreateChange(fields)
		if err != nil {
			return err
		}
		*change = decoded
	case "update":
		if err := rejectUnknownFields(fields, map[string]bool{"op": true, "key": true, "version": true, "type": true, "record": true, "clear": true}); err != nil {
			return err
		}
		decoded, err := decodeUpdateChange(fields)
		if err != nil {
			return err
		}
		*change = decoded
	case "relate":
		if err := rejectUnknownFields(fields, map[string]bool{"op": true, "from": true, "relation": true, "to": true, "version": true, "reason": true}); err != nil {
			return err
		}
		decoded, err := decodeRelateChange(fields)
		if err != nil {
			return err
		}
		*change = decoded
	case "unrelate":
		if err := rejectUnknownFields(fields, map[string]bool{"op": true, "from": true, "relation": true, "to": true, "version": true}); err != nil {
			return err
		}
		decoded, err := decodeUnrelateChange(fields)
		if err != nil {
			return err
		}
		*change = decoded
	case "transition":
		if err := rejectUnknownFields(fields, map[string]bool{"op": true, "key": true, "version": true, "status": true, "summary": true, "resolution_summary": true, "verification_summary": true}); err != nil {
			return err
		}
		decoded, err := decodeTransitionChange(fields)
		if err != nil {
			return err
		}
		*change = decoded
	case "supersede":
		if err := rejectUnknownFields(fields, map[string]bool{"op": true, "replacement": true, "replacement_version": true, "replaced": true, "replaced_version": true}); err != nil {
			return err
		}
		decoded, err := decodeSupersedeChange(fields)
		if err != nil {
			return err
		}
		*change = decoded
	case "merge":
		if err := rejectUnknownFields(fields, map[string]bool{"op": true, "source": true, "source_version": true, "canonical": true, "canonical_version": true, "canonical_record": true, "clear": true}); err != nil {
			return err
		}
		decoded, err := decodeMergeChange(fields)
		if err != nil {
			return err
		}
		*change = decoded
	default:
		*change = Change{Op: op}
	}
	return nil
}

type ChangeResult struct {
	Schema          string                 `json:"schema"`
	Revision        int                    `json:"revision"`
	Records         []RecordVersionTuple   `json:"records"`
	Relations       []RelationVersionTuple `json:"relations"`
	WorkingSnapshot WorkingSnapshot        `json:"working_snapshot"`
}

type RecordVersionTuple [2]any

// UnmarshalJSON keeps replayed idempotency receipts structurally stable.
func (tuple *RecordVersionTuple) UnmarshalJSON(raw []byte) error {
	var values []json.RawMessage
	if err := decodeJSON(raw, &values); err != nil {
		return err
	}
	if len(values) != 2 {
		return fmt.Errorf("record version tuple must have 2 values")
	}
	var key string
	var version int
	if err := decodeJSON(values[0], &key); err != nil {
		return err
	}
	if err := decodeJSON(values[1], &version); err != nil {
		return err
	}
	*tuple = RecordVersionTuple{key, version}
	return nil
}

type RelationVersionTuple [4]any

// UnmarshalJSON keeps replayed relationship tuples structurally stable.
func (tuple *RelationVersionTuple) UnmarshalJSON(raw []byte) error {
	var values []json.RawMessage
	if err := decodeJSON(raw, &values); err != nil {
		return err
	}
	if len(values) != 4 {
		return fmt.Errorf("relation version tuple must have 4 values")
	}
	var from, relation, to string
	var version int
	if err := decodeJSON(values[0], &from); err != nil {
		return err
	}
	if err := decodeJSON(values[1], &relation); err != nil {
		return err
	}
	if err := decodeJSON(values[2], &to); err != nil {
		return err
	}
	if err := decodeJSON(values[3], &version); err != nil {
		return err
	}
	*tuple = RelationVersionTuple{from, relation, to, version}
	return nil
}
