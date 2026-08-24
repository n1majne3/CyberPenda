// Package blackboardv2input owns shared Blackboard v2 Contract input
// validation for trusted adapters.
package blackboardv2input

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"
	"sync"

	googlejsonschema "github.com/google/jsonschema-go/jsonschema"
	detailedjsonschema "github.com/santhosh-tekuri/jsonschema/v6"
	"github.com/santhosh-tekuri/jsonschema/v6/kind"

	"pentest/internal/blackboardv2"
	"pentest/internal/blackboardv2contract"
	"pentest/internal/jsonnumber"
)

const detailedToolSchemaURL = "https://cyberpenda.local/schemas/blackboard-v2.schema.json"

var blackboardV2ContractInputSchemas = struct {
	sync.Mutex
	values map[string]*detailedjsonschema.Schema
}{values: make(map[string]*detailedjsonschema.Schema)}

// DecodeContractInput gives every trusted v2 adapter the same exact Contract
// validation, number normalization, and DTO decoding.
func DecodeContractInput(schemaName string, sessionOwner bool, raw json.RawMessage, target any) *blackboardv2.Error {
	cacheKey := schemaName + ":project"
	if sessionOwner {
		cacheKey = schemaName + ":session"
	}
	blackboardV2ContractInputSchemas.Lock()
	schema := blackboardV2ContractInputSchemas.values[cacheKey]
	if schema == nil {
		harness, err := blackboardv2contract.NewHarness()
		if err != nil {
			blackboardV2ContractInputSchemas.Unlock()
			panic(fmt.Errorf("load Blackboard v2 Contract input %s: %w", schemaName, err))
		}
		inputSchema, err := harness.ToolInputSchema(schemaName)
		if err != nil {
			blackboardV2ContractInputSchemas.Unlock()
			panic(fmt.Errorf("load Blackboard v2 input Schema %s: %w", schemaName, err))
		}
		if sessionOwner {
			inputSchema = SessionToolInputSchema(inputSchema)
		}
		schema, err = compileDetailedToolSchema(inputSchema)
		if err != nil {
			blackboardV2ContractInputSchemas.Unlock()
			panic(fmt.Errorf("compile Blackboard v2 input Schema %s: %w", schemaName, err))
		}
		blackboardV2ContractInputSchemas.values[cacheKey] = schema
	}
	blackboardV2ContractInputSchemas.Unlock()
	return decodeContractInput(schema, raw, target)
}

func decodeContractInput(schema *detailedjsonschema.Schema, raw json.RawMessage, target any) *blackboardv2.Error {
	if len(bytes.TrimSpace(raw)) == 0 {
		raw = json.RawMessage(`{}`)
	}
	instance, validationErr := validateDetailedToolArgs(schema, raw)
	if validationErr != nil {
		return validationErr
	}
	normalized, err := json.Marshal(instance)
	if err != nil {
		return invalidToolSchemaError("arguments", map[string]any{
			"reason": "decoder_mismatch", "next_action": "report_contract_defect",
		})
	}
	if err := json.Unmarshal(normalized, target); err != nil {
		return invalidToolSchemaError("arguments", map[string]any{
			"reason": "decoder_mismatch", "next_action": "report_contract_defect",
		})
	}
	return nil
}

// SessionToolInputSchema derives the Session-owned view of the same trusted
// protocol without adding a second hand-maintained schema. Project Scope is
// removed recursively from records and patches; owner capability validation
// still rejects Project-only record types, operations, and relationships.
func SessionToolInputSchema(source *googlejsonschema.Schema) *googlejsonschema.Schema {
	schema := source.CloneSchemas()
	var strip func(*googlejsonschema.Schema)
	strip = func(current *googlejsonschema.Schema) {
		if current == nil {
			return
		}
		delete(current.Properties, "scope_status")
		required := current.Required[:0]
		for _, name := range current.Required {
			if name != "scope_status" {
				required = append(required, name)
			}
		}
		current.Required = required
		for _, child := range current.Defs {
			strip(child)
		}
		for _, child := range current.Definitions {
			strip(child)
		}
		for _, child := range current.Properties {
			strip(child)
		}
		for _, child := range current.PatternProperties {
			strip(child)
		}
		for _, child := range current.AllOf {
			strip(child)
		}
		for _, child := range current.AnyOf {
			strip(child)
		}
		for _, child := range current.OneOf {
			strip(child)
		}
		for _, child := range current.PrefixItems {
			strip(child)
		}
		strip(current.Items)
		strip(current.AdditionalItems)
		strip(current.Contains)
		strip(current.Not)
		strip(current.If)
		strip(current.Then)
		strip(current.Else)
	}
	strip(schema)
	return schema
}

func compileDetailedToolSchema(source *googlejsonschema.Schema) (*detailedjsonschema.Schema, error) {
	raw, err := json.Marshal(source)
	if err != nil {
		return nil, fmt.Errorf("encode detailed tool schema: %w", err)
	}
	document, err := detailedjsonschema.UnmarshalJSON(bytes.NewReader(raw))
	if err != nil {
		return nil, fmt.Errorf("decode detailed tool schema: %w", err)
	}
	compiler := detailedjsonschema.NewCompiler()
	compiler.AssertFormat()
	if err := compiler.AddResource(detailedToolSchemaURL, document); err != nil {
		return nil, fmt.Errorf("register detailed tool schema: %w", err)
	}
	compiled, err := compiler.Compile(detailedToolSchemaURL)
	if err != nil {
		return nil, fmt.Errorf("compile detailed tool schema: %w", err)
	}
	return compiled, nil
}

func validateDetailedToolArgs(schema *detailedjsonschema.Schema, raw json.RawMessage) (any, *blackboardv2.Error) {
	var instance any
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if err := decoder.Decode(&instance); err != nil {
		details := map[string]any{"reason": "invalid_json"}
		if syntaxErr, ok := err.(*json.SyntaxError); ok {
			details["byte_offset"] = syntaxErr.Offset
		}
		return nil, invalidToolSchemaError("arguments", details)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return nil, invalidToolSchemaError("arguments", map[string]any{"reason": "invalid_json"})
	}
	if err := schema.Validate(instance); err != nil {
		validationErr, ok := err.(*detailedjsonschema.ValidationError)
		if !ok {
			return nil, invalidToolSchemaError("arguments", map[string]any{"reason": "invalid_schema"})
		}
		leaf := selectDetailedValidationLeaf(validationErr, instance)
		path, details := normalizeDetailedValidationError(leaf, instance)
		if nextAction := detailedValidationNextAction(instance, leaf); nextAction != "" {
			details["next_action"] = nextAction
		}
		return nil, invalidToolSchemaError(path, details)
	}
	if err := blackboardv2contract.ValidateInputExtensions(instance); err != nil {
		var extensionErr *blackboardv2contract.InputExtensionError
		if !errors.As(err, &extensionErr) {
			return nil, invalidToolSchemaError("arguments", map[string]any{"reason": "invalid_schema"})
		}
		path := strings.TrimPrefix(extensionErr.Path, "$.")
		if path == "$" || path == "" {
			path = "arguments"
		}
		return nil, invalidToolSchemaError(path, map[string]any{
			"reason": extensionErr.Reason, "expected": extensionErr.Expected, "actual": extensionErr.Actual,
		})
	}
	normalizeContractIntegerValues(&instance)
	return instance, nil
}

func normalizeContractIntegerValues(value *any) {
	switch typed := (*value).(type) {
	case json.Number:
		if integer, err := jsonnumber.ExactInteger(typed, 9007199254740991); err == nil {
			*value = integer
		}
	case []any:
		for index := range typed {
			normalizeContractIntegerValues(&typed[index])
		}
	case map[string]any:
		for key, child := range typed {
			normalizeContractIntegerValues(&child)
			typed[key] = child
		}
	}
}

func invalidToolSchemaError(path string, details map[string]any) *blackboardv2.Error {
	if path == "" {
		path = "arguments"
	}
	if details == nil {
		details = map[string]any{}
	}
	return &blackboardv2.Error{
		Code:      "invalid_schema",
		Message:   "tool arguments do not match the closed input schema",
		Path:      path,
		Retryable: false,
		Details:   details,
	}
}

func selectDetailedValidationLeaf(root *detailedjsonschema.ValidationError, instance any) *detailedjsonschema.ValidationError {
	best := root
	bestBranch := 0
	var visit func(*detailedjsonschema.ValidationError, int)
	visit = func(current *detailedjsonschema.ValidationError, parentBranch int) {
		currentBranch := detailedValidationOperationBranch(current, instance)
		if currentBranch == 0 {
			currentBranch = parentBranch
		}
		if detailedValidationErrorPrecedes(current, best, currentBranch, bestBranch, instance) {
			best = current
			bestBranch = currentBranch
		}
		for _, cause := range current.Causes {
			visit(cause, currentBranch)
		}
	}
	visit(root, 0)
	return best
}

type detailedValidationPriority int

const (
	validationPriorityStructural detailedValidationPriority = iota
	validationPriorityValue
	validationPriorityField
	validationPriorityUnion
)

func detailedValidationErrorPrecedes(candidate, current *detailedjsonschema.ValidationError, candidateBranch, currentBranch int, instance any) bool {
	if candidateBranch != currentBranch {
		return candidateBranch > currentBranch
	}
	candidateDiscriminator := detailedValidationIsDiscriminator(candidate)
	currentDiscriminator := detailedValidationIsDiscriminator(current)
	if candidateDiscriminator != currentDiscriminator {
		return candidateDiscriminator
	}
	if len(candidate.InstanceLocation) != len(current.InstanceLocation) {
		return len(candidate.InstanceLocation) > len(current.InstanceLocation)
	}
	candidatePriority := detailedValidationErrorPriority(candidate)
	currentPriority := detailedValidationErrorPriority(current)
	if candidatePriority != currentPriority {
		return candidatePriority > currentPriority
	}
	candidatePath, _ := normalizeDetailedValidationError(candidate, instance)
	currentPath, _ := normalizeDetailedValidationError(current, instance)
	if candidatePath != currentPath {
		return candidatePath < currentPath
	}
	return strings.Join(candidate.ErrorKind.KeywordPath(), "/") < strings.Join(current.ErrorKind.KeywordPath(), "/")
}

func detailedValidationOperationBranch(err *detailedjsonschema.ValidationError, instance any) int {
	if len(err.InstanceLocation) < 2 || err.InstanceLocation[0] != "changes" {
		return 0
	}
	root, ok := instance.(map[string]any)
	if !ok {
		return 0
	}
	changes, ok := root["changes"].([]any)
	if !ok {
		return 0
	}
	changeIndex, indexErr := strconv.Atoi(err.InstanceLocation[1])
	if indexErr != nil || changeIndex < 0 || changeIndex >= len(changes) {
		return 0
	}
	change, ok := changes[changeIndex].(map[string]any)
	if !ok {
		return 0
	}
	op, _ := change["op"].(string)
	branches := []struct{ op, definition string }{
		{"create", "createChange"}, {"update", "updateChange"}, {"transition", "transitionChange"},
		{"relate", "relateChange"}, {"unrelate", "unrelateChange"}, {"merge", "mergeChange"}, {"supersede", "supersedeChange"},
	}
	for _, branch := range branches {
		if strings.Contains(err.SchemaURL, "/"+branch.definition) || strings.Contains(err.SchemaURL, "#/$defs/"+branch.definition) {
			if op == branch.op {
				return 1
			}
			return -1
		}
	}
	return 0
}

func detailedValidationIsDiscriminator(err *detailedjsonschema.ValidationError) bool {
	keyword := detailedValidationKeyword(err)
	if len(err.InstanceLocation) > 0 && err.InstanceLocation[len(err.InstanceLocation)-1] == "type" && keyword == "enum" {
		return true
	}
	required, ok := err.ErrorKind.(*kind.Required)
	if !ok {
		return false
	}
	for _, field := range required.Missing {
		if field == "type" || field == "op" {
			return true
		}
	}
	return false
}

func detailedValidationErrorPriority(err *detailedjsonschema.ValidationError) detailedValidationPriority {
	switch detailedValidationKeyword(err) {
	case "oneOf", "anyOf":
		return validationPriorityUnion
	case "required", "additionalProperties":
		return validationPriorityField
	case "enum", "const", "type", "format":
		return validationPriorityValue
	default:
		return validationPriorityStructural
	}
}

func detailedValidationKeyword(err *detailedjsonschema.ValidationError) string {
	path := err.ErrorKind.KeywordPath()
	if len(path) == 0 {
		return ""
	}
	return path[len(path)-1]
}

func normalizeDetailedValidationError(err *detailedjsonschema.ValidationError, instance any) (string, map[string]any) {
	location := append([]string(nil), err.InstanceLocation...)
	details := map[string]any{"reason": "invalid_schema"}
	switch typed := err.ErrorKind.(type) {
	case *kind.Required:
		details["reason"] = "missing_field"
		missing := append([]string(nil), typed.Missing...)
		sort.Strings(missing)
		details["expected"] = missing
		if len(missing) > 0 {
			location = append(location, preferredMissingField(missing))
		}
	case *kind.AdditionalProperties:
		details["reason"] = "additional_field"
		properties := append([]string(nil), typed.Properties...)
		sort.Strings(properties)
		if len(properties) > 0 {
			location = append(location, properties[0])
		}
	case *kind.Enum:
		details["reason"] = "invalid_value"
		details["expected"] = append([]any(nil), typed.Want...)
	case *kind.Const:
		details["reason"] = "invalid_value"
		details["expected"] = typed.Want
	case *kind.Type:
		details["reason"] = "invalid_type"
		details["expected"] = append([]string(nil), typed.Want...)
	case *kind.Format:
		details["reason"] = "invalid_format"
		details["expected"] = typed.Want
	case *kind.OneOf:
		details["reason"] = "invalid_union"
	}
	if keyword := detailedValidationKeyword(err); keyword == "oneOf" || keyword == "anyOf" {
		details["reason"] = "invalid_union"
	}
	return dottedInstancePath(location, instance), details
}

func preferredMissingField(missing []string) string {
	for _, preferred := range []string{"op", "type", "schema", "idempotency_key", "changes"} {
		for _, field := range missing {
			if field == preferred {
				return field
			}
		}
	}
	return missing[0]
}

func dottedInstancePath(location []string, instance any) string {
	if len(location) == 0 {
		return "arguments"
	}
	var out strings.Builder
	current := instance
	for index, segment := range location {
		switch parent := current.(type) {
		case []any:
			out.WriteByte('[')
			out.WriteString(segment)
			out.WriteByte(']')
			position, err := strconv.Atoi(segment)
			if err == nil && position >= 0 && position < len(parent) {
				current = parent[position]
			} else {
				current = nil
			}
		case map[string]any:
			writeObjectPathSegment(&out, segment, index > 0)
			current = parent[segment]
		default:
			if index > 0 {
				out.WriteByte('.')
			}
			out.WriteString(segment)
		}
	}
	return out.String()
}

func writeObjectPathSegment(out *strings.Builder, segment string, separated bool) {
	if isPlainObjectPathSegment(segment) {
		if separated {
			out.WriteByte('.')
		}
		out.WriteString(segment)
		return
	}
	encoded, _ := json.Marshal(segment)
	out.WriteByte('[')
	out.Write(encoded)
	out.WriteByte(']')
}

func isPlainObjectPathSegment(segment string) bool {
	if segment == "" {
		return false
	}
	for index := 0; index < len(segment); index++ {
		character := segment[index]
		if (character < 'a' || character > 'z') && (character < 'A' || character > 'Z') &&
			(character < '0' || character > '9') && character != '_' && character != '-' {
			return false
		}
	}
	return true
}

func detailedValidationNextAction(instance any, err *detailedjsonschema.ValidationError) string {
	location := err.InstanceLocation
	if len(location) != 3 || location[0] != "changes" || location[2] != "type" {
		return ""
	}
	root, ok := instance.(map[string]any)
	if !ok {
		return ""
	}
	changes, ok := root["changes"].([]any)
	if !ok {
		return ""
	}
	changeIndex, errIndex := strconv.Atoi(location[1])
	if errIndex != nil || changeIndex < 0 || changeIndex >= len(changes) {
		return ""
	}
	change, ok := changes[changeIndex].(map[string]any)
	if !ok || change["op"] != "create" || change["type"] != "evidence" {
		return ""
	}
	return "use_blackboard_retain_evidence"
}
