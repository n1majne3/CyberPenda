package workinggraph

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"pentest/internal/blackboardv2"
	"pentest/internal/owner"
)

// SemanticBoard is the narrow Blackboard capability used by the Working Graph
// compiler. Runtime payloads cannot select versions or idempotency identities.
type SemanticBoard interface {
	ReadCurrentForOwner(context.Context, owner.Contract, string) (blackboardv2.CurrentDetail, error)
	CurrentRelationshipVersionForOwner(context.Context, owner.Contract, string, string, string) (int, error)
	ApplyForOwnerContinuation(context.Context, owner.Contract, string, blackboardv2.ChangeBatch) (blackboardv2.ChangeResult, error)
}

type SemanticCompiler struct{ board SemanticBoard }

func NewSemanticCompiler(board SemanticBoard) *SemanticCompiler { return &SemanticCompiler{board: board} }

func (compiler *SemanticCompiler) Apply(ctx context.Context, contract owner.Contract, continuationID string, intent Intent) (ApplyOutcome, error) {
	if compiler == nil || compiler.board == nil {
		return ApplyOutcome{}, errors.New("Working Graph semantic compiler is unavailable")
	}
	if intent.Kind != IntentSemanticChanges {
		return actionRequired("unsupported_intent_kind", fmt.Sprintf("intent kind %q requires another Harness compiler", intent.Kind)), nil
	}
	changes, err := compiler.compileSemanticChanges(ctx, contract, intent.Payload)
	if err != nil {
		return actionRequired("invalid_intent_payload", err.Error()), nil
	}
	if len(changes) == 0 {
		return ApplyOutcome{State: IntentStateNoop, Result: map[string]any{"revision_changed": false}}, nil
	}
	result, err := compiler.board.ApplyForOwnerContinuation(ctx, contract, continuationID, blackboardv2.ChangeBatch{
		Schema: "semantic-change-batch/v2",
		IdempotencyKey: fmt.Sprintf("working-graph:%s:%s:%s:%s", contract.Kind, contract.ID, continuationID, intent.ID),
		Changes: changes,
	})
	if err != nil {
		var semanticErr *blackboardv2.Error
		if errors.As(err, &semanticErr) && !semanticErr.Retryable {
			return ApplyOutcome{State: IntentStateActionRequired, Error: map[string]any{
				"code": semanticErr.Code, "message": semanticErr.Message, "path": semanticErr.Path,
			}}, nil
		}
		return ApplyOutcome{}, err
	}
	encoded, err := json.Marshal(result)
	if err != nil {
		return ApplyOutcome{}, err
	}
	var projected map[string]any
	if err := json.Unmarshal(encoded, &projected); err != nil {
		return ApplyOutcome{}, err
	}
	return ApplyOutcome{State: IntentStateApplied, Result: projected}, nil
}

func (compiler *SemanticCompiler) compileSemanticChanges(ctx context.Context, contract owner.Contract, payload map[string]any) ([]blackboardv2.Change, error) {
	raw, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	var envelope struct {
		Changes []json.RawMessage `json:"changes"`
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&envelope); err != nil {
		return nil, err
	}
	if envelope.Changes == nil {
		return nil, errors.New("changes must be an array")
	}
	compiled := make([]blackboardv2.Change, 0, len(envelope.Changes))
	for index, operationRaw := range envelope.Changes {
		change, err := compiler.compileSemanticChange(ctx, contract, operationRaw)
		if err != nil {
			return nil, fmt.Errorf("changes[%d]: %w", index, err)
		}
		compiled = append(compiled, change)
	}
	return compiled, nil
}

func (compiler *SemanticCompiler) compileSemanticChange(ctx context.Context, contract owner.Contract, raw json.RawMessage) (blackboardv2.Change, error) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return blackboardv2.Change{}, err
	}
	op, err := requiredRuntimeString(fields, "op")
	if err != nil {
		return blackboardv2.Change{}, err
	}
	compiled := make(map[string]any)
	switch op {
	case "upsert":
		if err := rejectRuntimeFields(fields, "op", "key", "type", "record", "clear"); err != nil {
			return blackboardv2.Change{}, err
		}
		key, err := requiredRuntimeString(fields, "key")
		if err != nil {
			return blackboardv2.Change{}, err
		}
		typ, err := requiredRuntimeString(fields, "type")
		if err != nil {
			return blackboardv2.Change{}, err
		}
		record, ok := fields["record"]
		if !ok {
			return blackboardv2.Change{}, errors.New("record is required")
		}
		current, readErr := compiler.board.ReadCurrentForOwner(ctx, contract, key)
		if isNotFound(readErr) {
			if _, hasClear := fields["clear"]; hasClear {
				return blackboardv2.Change{}, errors.New("clear is not allowed when upsert creates a record")
			}
			compiled = map[string]any{"op": "create", "key": key, "type": typ, "record": record}
		} else if readErr != nil {
			return blackboardv2.Change{}, readErr
		} else {
			compiled = map[string]any{"op": "update", "key": key, "version": current.Version, "type": typ, "record": record}
			copyRuntimeField(compiled, fields, "clear")
		}
	case "transition":
		if err := rejectRuntimeFields(fields, "op", "key", "status", "summary", "resolution_summary", "verification_summary"); err != nil {
			return blackboardv2.Change{}, err
		}
		key, err := requiredRuntimeString(fields, "key")
		if err != nil {
			return blackboardv2.Change{}, err
		}
		current, err := compiler.board.ReadCurrentForOwner(ctx, contract, key)
		if err != nil {
			return blackboardv2.Change{}, err
		}
		compiled = map[string]any{"op": op, "key": key, "version": current.Version}
		for _, field := range []string{"status", "summary", "resolution_summary", "verification_summary"} {
			copyRuntimeField(compiled, fields, field)
		}
	case "relate":
		if err := rejectRuntimeFields(fields, "op", "from", "relation", "to", "reason"); err != nil {
			return blackboardv2.Change{}, err
		}
		compiled = map[string]any{"op": op}
		for _, field := range []string{"from", "relation", "to", "reason"} {
			copyRuntimeField(compiled, fields, field)
		}
	case "unrelate":
		if err := rejectRuntimeFields(fields, "op", "from", "relation", "to"); err != nil {
			return blackboardv2.Change{}, err
		}
		from, err := requiredRuntimeString(fields, "from")
		if err != nil {
			return blackboardv2.Change{}, err
		}
		relation, err := requiredRuntimeString(fields, "relation")
		if err != nil {
			return blackboardv2.Change{}, err
		}
		to, err := requiredRuntimeString(fields, "to")
		if err != nil {
			return blackboardv2.Change{}, err
		}
		version, err := compiler.board.CurrentRelationshipVersionForOwner(ctx, contract, from, relation, to)
		if err != nil {
			return blackboardv2.Change{}, err
		}
		if version < 1 {
			return blackboardv2.Change{}, errors.New("relationship was not found")
		}
		compiled = map[string]any{"op": op, "from": from, "relation": relation, "to": to, "version": version}
	case "supersede":
		if err := rejectRuntimeFields(fields, "op", "replacement", "replaced"); err != nil {
			return blackboardv2.Change{}, err
		}
		replacement, err := requiredRuntimeString(fields, "replacement")
		if err != nil {
			return blackboardv2.Change{}, err
		}
		replaced, err := requiredRuntimeString(fields, "replaced")
		if err != nil {
			return blackboardv2.Change{}, err
		}
		replacementCurrent, err := compiler.board.ReadCurrentForOwner(ctx, contract, replacement)
		if err != nil {
			return blackboardv2.Change{}, err
		}
		replacedCurrent, err := compiler.board.ReadCurrentForOwner(ctx, contract, replaced)
		if err != nil {
			return blackboardv2.Change{}, err
		}
		compiled = map[string]any{"op": op, "replacement": replacement, "replacement_version": replacementCurrent.Version,
			"replaced": replaced, "replaced_version": replacedCurrent.Version}
	case "merge":
		if err := rejectRuntimeFields(fields, "op", "source", "canonical", "canonical_record", "clear"); err != nil {
			return blackboardv2.Change{}, err
		}
		source, err := requiredRuntimeString(fields, "source")
		if err != nil {
			return blackboardv2.Change{}, err
		}
		canonical, err := requiredRuntimeString(fields, "canonical")
		if err != nil {
			return blackboardv2.Change{}, err
		}
		sourceCurrent, err := compiler.board.ReadCurrentForOwner(ctx, contract, source)
		if err != nil {
			return blackboardv2.Change{}, err
		}
		canonicalCurrent, err := compiler.board.ReadCurrentForOwner(ctx, contract, canonical)
		if err != nil {
			return blackboardv2.Change{}, err
		}
		compiled = map[string]any{"op": op, "source": source, "source_version": sourceCurrent.Version,
			"canonical": canonical, "canonical_version": canonicalCurrent.Version}
		copyRuntimeField(compiled, fields, "canonical_record")
		copyRuntimeField(compiled, fields, "clear")
	default:
		return blackboardv2.Change{}, fmt.Errorf("unsupported semantic operation %q", op)
	}
	return decodeBlackboardChange(compiled)
}

func decodeBlackboardChange(fields map[string]any) (blackboardv2.Change, error) {
	raw, err := json.Marshal(fields)
	if err != nil {
		return blackboardv2.Change{}, err
	}
	var change blackboardv2.Change
	if err := json.Unmarshal(raw, &change); err != nil {
		return blackboardv2.Change{}, err
	}
	return change, nil
}

func requiredRuntimeString(fields map[string]json.RawMessage, name string) (string, error) {
	raw, ok := fields[name]
	if !ok {
		return "", fmt.Errorf("%s is required", name)
	}
	var value string
	if err := json.Unmarshal(raw, &value); err != nil || strings.TrimSpace(value) == "" {
		return "", fmt.Errorf("%s must be a non-empty string", name)
	}
	return value, nil
}

func rejectRuntimeFields(fields map[string]json.RawMessage, allowed ...string) error {
	set := make(map[string]bool, len(allowed))
	for _, name := range allowed {
		set[name] = true
	}
	for name := range fields {
		if !set[name] {
			return fmt.Errorf("Runtime cannot supply semantic field %q", name)
		}
	}
	return nil
}

func copyRuntimeField(target map[string]any, source map[string]json.RawMessage, name string) {
	if raw, ok := source[name]; ok {
		target[name] = raw
	}
}

func isNotFound(err error) bool {
	var semanticErr *blackboardv2.Error
	return errors.As(err, &semanticErr) && semanticErr.Code == "not_found"
}

func actionRequired(code, message string) ApplyOutcome {
	return ApplyOutcome{State: IntentStateActionRequired, Error: map[string]any{"code": code, "message": message}}
}
